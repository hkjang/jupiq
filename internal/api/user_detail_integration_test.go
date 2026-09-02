package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/hkjang/jupiq/internal/auth"
	"github.com/hkjang/jupiq/internal/secure"
	"github.com/hkjang/jupiq/internal/store"
)

func TestUserDetailEndpointIntegration(t *testing.T) {
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

	marker := fmt.Sprintf("detail-api-%d", time.Now().UnixNano())
	adminUsername := marker + "-admin"
	const password = "IntegrationPassword!123"
	if err := database.Seed(ctx, adminUsername, password); err != nil {
		t.Fatal(err)
	}
	admin, err := database.GetUserByUsername(ctx, adminUsername)
	if err != nil {
		t.Fatal(err)
	}
	var hubID int64
	if err := database.Pool.QueryRow(ctx, `INSERT INTO hubs(name,base_url,network) VALUES($1,'https://api-detail.example.internal','개발망') RETURNING id`, marker).Scan(&hubID); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = database.Pool.Exec(ctx, `DELETE FROM hubs WHERE id=$1`, hubID)
		_, _ = database.Pool.Exec(ctx, `DELETE FROM users WHERE id=$1`, admin.ID)
	}()
	if _, err := database.Pool.Exec(ctx, `INSERT INTO managed_users(hub_id,username,display_name,active,raw) VALUES($1,$2,'API 상세 사용자',true,$3)`, hubID, marker, json.RawMessage(`{"prompt":"SHOULD_NOT_LEAK"}`)); err != nil {
		t.Fatal(err)
	}

	expires := time.Now().UTC().Add(time.Hour)
	_, deniedSecret, err := database.CreateAPIKey(ctx, admin.ID, "denied", []string{"dashboard:read"}, &expires, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, allowedSecret, err := database.CreateAPIKey(ctx, admin.ID, "allowed", []string{"users:read"}, &expires, nil)
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := New(database, auth.NewService(database, cipher), logger).Handler()

	request := func(target, secret string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		req.Header.Set("Authorization", "Bearer "+secret)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		return response
	}

	if response := request("/api/v1/users/"+marker, deniedSecret); response.Code != http.StatusForbidden {
		t.Fatalf("users:read scope was not enforced: status=%d body=%s", response.Code, response.Body.String())
	}
	response := request("/api/v1/users/"+marker+"?page=1&limit=10&include_llm=false", allowedSecret)
	if response.Code != http.StatusOK {
		t.Fatalf("detail endpoint status=%d body=%s", response.Code, response.Body.String())
	}
	var envelope struct {
		Data store.UserDetail `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data.Username != marker || len(envelope.Data.Hubs) != 1 || envelope.Data.LLMUsage["included"] != false {
		t.Fatalf("unexpected detail payload: %#v", envelope.Data)
	}
	if response := request("/api/v1/users/.invalid", allowedSecret); response.Code != http.StatusBadRequest {
		t.Fatalf("invalid username status=%d body=%s", response.Code, response.Body.String())
	}
	if response := request("/api/v1/users/"+marker+"?limit=101", allowedSecret); response.Code != http.StatusBadRequest {
		t.Fatalf("limit cap status=%d body=%s", response.Code, response.Body.String())
	}
	if response := request("/api/v1/users/does-not-exist", allowedSecret); response.Code != http.StatusNotFound {
		t.Fatalf("missing user status=%d body=%s", response.Code, response.Body.String())
	}
}
