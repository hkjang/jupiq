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
	"strings"
	"testing"
	"time"

	"github.com/hkjang/jupiq/internal/auth"
	"github.com/hkjang/jupiq/internal/secure"
	"github.com/hkjang/jupiq/internal/store"
)

func TestScopedRBACHTTPFailClosedIntegration(t *testing.T) {
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
	marker := fmt.Sprintf("api-scope-%d", time.Now().UnixNano())
	var hubOne, hubTwo int64
	if err := database.Pool.QueryRow(ctx, `INSERT INTO hubs(name,base_url,network) VALUES($1,$2,$1) RETURNING id`, marker+"-allowed-hub", "https://"+marker+"-one.internal").Scan(&hubOne); err != nil {
		t.Fatal(err)
	}
	if err := database.Pool.QueryRow(ctx, `INSERT INTO hubs(name,base_url,network) VALUES($1,$2,$1) RETURNING id`, marker+"-denied-hub", "https://"+marker+"-two.internal").Scan(&hubTwo); err != nil {
		t.Fatal(err)
	}
	var userID int64
	if err := database.Pool.QueryRow(ctx, `INSERT INTO users(username,display_name,auth_source) VALUES($1,$1,'oidc') RETURNING id`, marker+"-operator").Scan(&userID); err != nil {
		t.Fatal(err)
	}
	role, err := database.SaveRole(ctx, store.Role{Key: marker + "-role", Name: "범위 운영자", Permissions: []string{"*"}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = database.Pool.Exec(ctx, `DELETE FROM users WHERE id=$1`, userID)
		_, _ = database.Pool.Exec(ctx, `DELETE FROM roles WHERE id=$1`, role.ID)
		_, _ = database.Pool.Exec(ctx, `DELETE FROM hubs WHERE id=ANY($1::bigint[])`, []int64{hubOne, hubTwo})
	}()
	if err := database.SetUserRoleBindings(ctx, userID, []store.RoleBinding{{RoleID: role.ID, ScopeMode: "restricted", Scopes: []store.ScopeClause{{Type: "hub", Value: fmt.Sprint(hubOne)}}}}); err != nil {
		t.Fatal(err)
	}
	insertInventory := func(hubID int64, suffix string) int64 {
		var managedID, serverID int64
		username := marker + "-" + suffix
		if err := database.Pool.QueryRow(ctx, `INSERT INTO managed_users(hub_id,username,display_name,department) VALUES($1,$2,$2,'AI') RETURNING id`, hubID, username).Scan(&managedID); err != nil {
			t.Fatal(err)
		}
		if err := database.Pool.QueryRow(ctx, `INSERT INTO servers(hub_id,managed_user_id,username,status) VALUES($1,$2,$3,'running') RETURNING id`, hubID, managedID, username).Scan(&serverID); err != nil {
			t.Fatal(err)
		}
		return serverID
	}
	insertInventory(hubOne, "allowed-user")
	deniedServerID := insertInventory(hubTwo, "denied-user")

	user, err := database.GetUser(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	authService := auth.NewService(database, cipher)
	_, token, expires, err := authService.CreateSession(ctx, user, "127.0.0.1", "scoped-rbac-test")
	if err != nil {
		t.Fatal(err)
	}
	handler := New(database, authService, slog.New(slog.NewTextHandler(io.Discard, nil))).Handler()
	request := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.AddCookie(auth.SecureCookie(token, expires, false))
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		return response
	}
	response := request(http.MethodGet, "/api/v1/auth/me", "")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"global_permissions":[]`) || !strings.Contains(response.Body.String(), `"scoped_permissions":["*"]`) {
		t.Fatalf("current-user payload did not distinguish global and scoped permissions: status=%d body=%s", response.Code, response.Body.String())
	}

	response = request(http.MethodGet, "/api/v1/hubs", "")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), marker+"-allowed-hub") || strings.Contains(response.Body.String(), marker+"-denied-hub") {
		t.Fatalf("scoped Hub list leaked or omitted data: status=%d body=%s", response.Code, response.Body.String())
	}
	if response = request(http.MethodGet, fmt.Sprintf("/api/v1/hubs/%d", hubOne), ""); response.Code != http.StatusOK {
		t.Fatalf("assigned Hub target was denied: status=%d body=%s", response.Code, response.Body.String())
	}
	if response = request(http.MethodGet, fmt.Sprintf("/api/v1/hubs/%d", hubTwo), ""); response.Code != http.StatusForbidden {
		t.Fatalf("unassigned Hub target was not denied: status=%d body=%s", response.Code, response.Body.String())
	}
	if response = request(http.MethodPost, "/api/v1/hubs", `{}`); response.Code != http.StatusForbidden {
		t.Fatalf("restricted wildcard opened global-only Hub creation: status=%d body=%s", response.Code, response.Body.String())
	}
	if response = request(http.MethodGet, "/api/v1/users?page=1&page_size=1&search="+marker, ""); response.Code != http.StatusOK || !strings.Contains(response.Body.String(), marker+"-allowed-user") || strings.Contains(response.Body.String(), marker+"-denied-user") || !strings.Contains(response.Body.String(), `"total":1`) {
		t.Fatalf("managed-user HTTP pagination was not scope-filtered: status=%d body=%s", response.Code, response.Body.String())
	}
	if response = request(http.MethodGet, "/api/v1/servers?page=1&page_size=1&search="+marker, ""); response.Code != http.StatusOK || !strings.Contains(response.Body.String(), marker+"-allowed-user") || strings.Contains(response.Body.String(), marker+"-denied-user") || !strings.Contains(response.Body.String(), `"total":1`) {
		t.Fatalf("server HTTP pagination was not scope-filtered: status=%d body=%s", response.Code, response.Body.String())
	}
	if response = request(http.MethodPost, fmt.Sprintf("/api/v1/servers/%d/stop", deniedServerID), ""); response.Code != http.StatusForbidden {
		t.Fatalf("unassigned server action was not denied: status=%d body=%s", response.Code, response.Body.String())
	}
	for _, path := range []string{"/api/v1/settings", "/api/v1/local-users", "/api/v1/dashboard", "/api/v1/audit", "/api/v1/search?q=" + marker} {
		response = request(http.MethodGet, path, "")
		if path == "/api/v1/search?q="+marker {
			var payload struct {
				Data struct {
					Items []any `json:"items"`
				} `json:"data"`
			}
			decodeErr := json.Unmarshal(response.Body.Bytes(), &payload)
			if response.Code != http.StatusOK || decodeErr != nil || len(payload.Data.Items) != 0 {
				t.Fatalf("global-only search leaked scoped data: status=%d body=%s", response.Code, response.Body.String())
			}
			continue
		}
		if response.Code != http.StatusForbidden {
			t.Fatalf("restricted wildcard opened global-only endpoint %s: status=%d body=%s", path, response.Code, response.Body.String())
		}
	}
	if response = request(http.MethodGet, "/api/v1/users/"+marker+"-allowed-user", ""); response.Code != http.StatusForbidden {
		t.Fatalf("unfiltered user detail was opened by a scoped permission: status=%d body=%s", response.Code, response.Body.String())
	}
}
