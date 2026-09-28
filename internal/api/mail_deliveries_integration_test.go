package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hkjang/jupiq/internal/auth"
	"github.com/hkjang/jupiq/internal/mail"
	"github.com/hkjang/jupiq/internal/secure"
	"github.com/hkjang/jupiq/internal/store"
)

func TestMailDeliveriesStatusIntegration(t *testing.T) {
	dsn := os.Getenv("JUPIQ_INTEGRATION_TEST_DSN")
	if dsn == "" {
		t.Skip("set JUPIQ_INTEGRATION_TEST_DSN to run PostgreSQL integration coverage")
	}
	ctx := context.Background()
	cipher, err := secure.NewCipher(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	database, err := store.Open(ctx, dsn, cipher)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	marker := fmt.Sprintf("api-mail-status-%d", time.Now().UnixNano())
	var ids []int64
	defer func() {
		if _, err := database.Pool.Exec(ctx, `DELETE FROM mail_deliveries WHERE id=ANY($1)`, ids); err != nil {
			t.Error(err)
		}
		if _, err := database.Pool.Exec(ctx, `DELETE FROM users WHERE username=$1`, marker); err != nil {
			t.Error(err)
		}
	}()
	if err := database.Seed(ctx, marker, "IntegrationPassword!123"); err != nil {
		t.Fatal(err)
	}
	var adminID int64
	if err := database.Pool.QueryRow(ctx, `SELECT id FROM users WHERE username=$1`, marker).Scan(&adminID); err != nil {
		t.Fatal(err)
	}
	user, err := database.GetUser(ctx, adminID)
	if err != nil {
		t.Fatal(err)
	}
	authService := auth.NewService(database, cipher)
	_, token, expires, err := authService.CreateSession(ctx, user, "127.0.0.1", "mail-status-test")
	if err != nil {
		t.Fatal(err)
	}
	handler := New(database, authService, slog.New(slog.NewTextHandler(io.Discard, nil))).Handler()
	request := func(query url.Values) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/mail/deliveries?"+query.Encode(), nil)
		req.AddCookie(auth.SecureCookie(token, expires, false))
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		return response
	}
	// 201 records exercise the default and upper limit as well as all three states.
	for i := 0; i < 201; i++ {
		id, err := database.RecordMailDelivery(ctx, mail.Delivery{Event: mail.EventTest, Recipient: marker + "@corp.internal", Subject: marker})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	fixtures := map[int64]string{ids[198]: mail.StatusQueued, ids[199]: mail.StatusSent, ids[200]: mail.StatusFailed}
	for id, status := range fixtures {
		if status != mail.StatusQueued {
			if err := database.FinishMailDelivery(ctx, id, status, 1, ""); err != nil {
				t.Fatal(err)
			}
		}
	}
	// Query the aggregate independently so filtered pages must retain global counts.
	summary := map[string]int{}
	rows, err := database.Pool.Query(ctx, `SELECT status,count(*) FROM mail_deliveries GROUP BY status`)
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		summary[status] = count
		total += count
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, raw, status string
		omitted           bool
	}{
		{name: "omitted", omitted: true}, {name: "empty"}, {name: "spaces", raw: "   "},
		{name: "tab", raw: "\t"}, {name: "nbsp", raw: "\u00a0"},
		{name: "queued", raw: mail.StatusQueued, status: mail.StatusQueued},
		{name: "sent", raw: mail.StatusSent, status: mail.StatusSent},
		{name: "failed", raw: mail.StatusFailed, status: mail.StatusFailed},
		{name: "padded_sent", raw: " sent ", status: mail.StatusSent},
		{name: "tab_sent", raw: "\tsent\t", status: mail.StatusSent},
		{name: "nbsp_sent", raw: "\u00a0sent\u00a0", status: mail.StatusSent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			query := url.Values{}
			if !tc.omitted {
				query.Set("status", tc.raw)
			}
			response := request(query)
			if response.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			var body struct {
				Data store.MailDeliveryPage `json:"data"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			page := body.Data
			if page.Total != total || !reflect.DeepEqual(page.Summary, summary) {
				t.Fatalf("global aggregate changed: %+v", page)
			}
			seen := map[int64]string{}
			for _, item := range page.Items {
				seen[item.ID] = item.Status
				if tc.status != "" && item.Status != tc.status {
					t.Fatalf("unexpected status %q for ID %d", item.Status, item.ID)
				}
			}
			for id, status := range fixtures {
				if tc.status == "" || status == tc.status {
					if seen[id] != status {
						t.Errorf("fixture %d: status=%q, want %q", id, seen[id], status)
					}
				} else if _, ok := seen[id]; ok {
					t.Errorf("unexpected fixture %d", id)
				}
			}
			if tc.status == "" && len(page.Items) != 50 {
				t.Errorf("default limit: got %d, want 50", len(page.Items))
			}
		})
	}
	for _, limit := range []string{"0", "1", "200", "201", "500"} {
		t.Run("limit_"+limit, func(t *testing.T) {
			response := request(url.Values{"limit": {limit}})
			if response.Code != http.StatusOK {
				t.Fatalf("status=%d", response.Code)
			}
			var body struct {
				Data store.MailDeliveryPage `json:"data"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			want := 200
			if limit == "0" {
				want = 50
			}
			if limit == "1" {
				want = 1
			}
			if len(body.Data.Items) != want || body.Data.Total != total || !reflect.DeepEqual(body.Data.Summary, summary) {
				t.Fatalf("limit %s: count=%d total=%d summary=%v", limit, len(body.Data.Items), body.Data.Total, body.Data.Summary)
			}
		})
	}
	for _, status := range []string{"nonsense", "faild", "SENT", "sent,failed", "se nt", " \tnonsense\u00a0"} {
		t.Run("invalid_"+status, func(t *testing.T) {
			response := request(url.Values{"status": {status}})
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status=%q: HTTP %d, want 400", status, response.Code)
			}
			var body struct {
				Error struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Error.Code != "invalid_query" {
				t.Errorf("error.code=%q", body.Error.Code)
			}
			for _, word := range []string{"status", mail.StatusQueued, mail.StatusSent, mail.StatusFailed} {
				if !strings.Contains(body.Error.Message, word) {
					t.Errorf("message must describe %q: %q", word, body.Error.Message)
				}
			}
			if strings.Contains(body.Error.Message, strings.TrimSpace(status)) {
				t.Errorf("message reflects invalid input: %q", body.Error.Message)
			}
		})
	}

}
