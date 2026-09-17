package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hkjang/jupiq/internal/auth"
	"github.com/hkjang/jupiq/internal/auth/authtest"
	"github.com/hkjang/jupiq/internal/secure"
	"github.com/hkjang/jupiq/internal/store"
)

// MCP를 개인 키 없이 Keycloak 토큰으로 — 프로덕션 배선(Handler → middleware →
// require → AuthenticateRequest → 계정 조회) 전체를 실제 PostgreSQL과 실제
// 서명을 내는 가짜 제공자로 통과시킨다. 인가 흐름(PKCE·리다이렉트·코드 교환)은
// Keycloak과 클라이언트의 몫이고, 여기서 보는 것은 리소스 서버의 절반이다.

const mcpListTools = `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`

func mcpCall(handler http.Handler, path, bearer, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	r.Host = "jupiq.example.test"
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Forwarded-Proto", "https")
	if bearer != "" {
		r.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, r)
	return rec
}

func mcpToolCall(name string) string {
	return fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":%q,"arguments":{}}}`, name)
}

// saveSettingsForTest는 설정 문서를 쓰고 테스트가 끝나면 원래 값으로 되돌린다.
func saveSettingsForTest(t *testing.T, database *store.Store, actorID int64, values map[string]any) {
	t.Helper()
	ctx := context.Background()
	previous := map[string]json.RawMessage{}
	for key := range values {
		var raw json.RawMessage
		if err := database.Pool.QueryRow(ctx, `SELECT value FROM settings WHERE setting_key=$1`, key).Scan(&raw); err == nil {
			previous[key] = raw
		}
	}
	if err := database.UpdateSettingsAndSecrets(ctx, values, nil, actorID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for key := range values {
			if raw, ok := previous[key]; ok {
				_, _ = database.Pool.Exec(ctx, `UPDATE settings SET value=$2 WHERE setting_key=$1`, key, raw)
			} else {
				_, _ = database.Pool.Exec(ctx, `DELETE FROM settings WHERE setting_key=$1`, key)
			}
		}
	})
}

func TestMCPOAuthResourceServerIntegration(t *testing.T) {
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
	idp := authtest.NewFakeIDP(t)
	logs := &bytes.Buffer{}
	authService := auth.NewService(database, cipher)
	handler := New(database, authService, slog.New(slog.NewTextHandler(logs, nil))).Handler()

	marker := fmt.Sprintf("mcp-oauth-%d", time.Now().UnixNano())
	resource := "https://jupiq.example.test/mcp"
	oidcSection := map[string]any{"enabled": false, "issuer_url": idp.URL(), "client_id": marker + "-web", "redirect_url": "", "scopes": []string{"openid"}, "username_claim": "preferred_username", "auto_create_users": true, "verify_tls": true, "auto_login": false}
	mcpSection := func(enabled bool, audience ...string) map[string]any {
		if audience == nil {
			audience = []string{}
		}
		return map[string]any{"enabled": enabled, "resource": resource, "audience": audience, "scopes": []string{"mcp:use", "dashboard:read", "hubs:read", "servers:read", "usage:read"}}
	}

	// An account the web sign-in registered for this issuer, with less than
	// the OAuth scope ceiling so the intersection is visible.
	member, err := database.UpsertOIDCUser(ctx, auth.OIDCExternalIdentity(idp.URL(), marker+"-subject"), marker+"-member", "Member", "", "", true)
	if err != nil {
		t.Fatal(err)
	}
	role, err := database.SaveRole(ctx, store.Role{Key: marker + "-role", Name: "MCP 열람", Permissions: []string{"mcp:use", "dashboard:read", "hubs:read"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.SetUserRoleBindings(ctx, member.ID, []store.RoleBinding{{RoleID: role.ID, ScopeMode: "global"}}); err != nil {
		t.Fatal(err)
	}
	suspended, err := database.UpsertOIDCUser(ctx, auth.OIDCExternalIdentity(idp.URL(), marker+"-suspended"), marker+"-suspended", "Suspended", "", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Pool.Exec(ctx, `UPDATE users SET active=false WHERE id=$1`, suspended.ID); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = database.Pool.Exec(ctx, `DELETE FROM audit_logs WHERE actor_username LIKE $1`, marker+"%")
		_, _ = database.Pool.Exec(ctx, `DELETE FROM users WHERE username LIKE $1`, marker+"%")
		_, _ = database.Pool.Exec(ctx, `DELETE FROM roles WHERE id=$1`, role.ID)
	}()
	token := func(overrides map[string]any) string {
		claims := map[string]any{"sub": marker + "-subject", "aud": resource, "typ": "Bearer", "exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(), "preferred_username": marker + "-member"}
		for key, value := range overrides {
			claims[key] = value
		}
		return idp.Sign(t, claims)
	}

	t.Run("off by default", func(t *testing.T) {
		saveSettingsForTest(t, database, member.ID, map[string]any{"auth.oidc": oidcSection, "mcp.oauth": mcpSection(false)})
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/.well-known/oauth-protected-resource/mcp", nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("metadata served while off: %d %s", rec.Code, rec.Body.String())
		}
		refused := mcpCall(handler, "/mcp", token(nil), mcpListTools)
		if refused.Code != http.StatusUnauthorized || refused.Header().Get("WWW-Authenticate") != "" {
			t.Errorf("token accepted or challenged while off: %d %q", refused.Code, refused.Header().Get("WWW-Authenticate"))
		}
		// Exactly the wording a foreign JWT always got: the session check's.
		if !strings.Contains(refused.Body.String(), "세션이 만료되었거나 유효하지 않습니다") {
			t.Errorf("an installation with SSO off said something new: %s", refused.Body.String())
		}
	})

	saveSettingsForTest(t, database, member.ID, map[string]any{"auth.oidc": oidcSection, "mcp.oauth": mcpSection(true)})

	t.Run("a refused client is told where to sign in", func(t *testing.T) {
		rec := httptest.NewRecorder()
		metadataRequest := httptest.NewRequest(http.MethodGet, "/.well-known/oauth-protected-resource/mcp", nil)
		handler.ServeHTTP(rec, metadataRequest)
		if rec.Code != http.StatusOK || rec.Header().Get("Access-Control-Allow-Origin") != "*" {
			t.Fatalf("metadata: %d CORS=%q %s", rec.Code, rec.Header().Get("Access-Control-Allow-Origin"), rec.Body.String())
		}
		var metadata struct {
			Resource string   `json:"resource"`
			Servers  []string `json:"authorization_servers"`
			Methods  []string `json:"bearer_methods_supported"`
			Scopes   []string `json:"scopes_supported"`
			Data     any      `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &metadata); err != nil {
			t.Fatal(err)
		}
		if metadata.Resource != resource || len(metadata.Servers) != 1 || metadata.Servers[0] != idp.URL() || len(metadata.Methods) != 1 || metadata.Methods[0] != "header" || len(metadata.Scopes) != 5 || metadata.Data != nil {
			t.Errorf("metadata is not the bare RFC 9728 document: %s", rec.Body.String())
		}
		bare := httptest.NewRecorder()
		handler.ServeHTTP(bare, httptest.NewRequest(http.MethodGet, "/.well-known/oauth-protected-resource", nil))
		if bare.Code != http.StatusOK {
			t.Errorf("bare well-known path: %d", bare.Code)
		}

		noBearer := mcpCall(handler, "/mcp", "", mcpListTools)
		want := `Bearer realm="jupiq", resource_metadata="https://jupiq.example.test/.well-known/oauth-protected-resource/mcp"`
		if noBearer.Code != http.StatusUnauthorized || noBearer.Header().Get("WWW-Authenticate") != want {
			t.Errorf("401 without bearer: %d %q", noBearer.Code, noBearer.Header().Get("WWW-Authenticate"))
		}
		badToken := mcpCall(handler, "/api/v1/mcp", token(map[string]any{"exp": time.Now().Add(-time.Minute).Unix()}), mcpListTools)
		if badToken.Code != http.StatusUnauthorized || badToken.Header().Get("WWW-Authenticate") != want+`, error="invalid_token"` {
			t.Errorf("401 after a refused token: %d %q", badToken.Code, badToken.Header().Get("WWW-Authenticate"))
		}
		// REST 401s stay as they were: no pointer at the authorization server.
		rest := httptest.NewRecorder()
		handler.ServeHTTP(rest, httptest.NewRequest(http.MethodGet, "/api/v1/dashboard", nil))
		if rest.Code != http.StatusUnauthorized || rest.Header().Get("WWW-Authenticate") != "" {
			t.Errorf("REST 401 carries the MCP challenge: %d %q", rest.Code, rest.Header().Get("WWW-Authenticate"))
		}
	})

	t.Run("a token for this resource opens MCP for a registered account", func(t *testing.T) {
		opened := mcpCall(handler, "/mcp", token(nil), mcpListTools)
		if opened.Code != http.StatusOK || !strings.Contains(opened.Body.String(), "jupiq.dashboard") {
			t.Fatalf("tools/list: %d %s", opened.Code, opened.Body.String())
		}
		// Scopes: the administrator's ceiling AND the user's own permissions.
		dashboard := mcpCall(handler, "/mcp", token(nil), mcpToolCall("jupiq.dashboard"))
		if dashboard.Code != http.StatusOK || strings.Contains(dashboard.Body.String(), `"isError":true`) {
			t.Errorf("dashboard tool: %d %s", dashboard.Code, dashboard.Body.String())
		}
		servers := mcpCall(handler, "/mcp", token(nil), mcpToolCall("jupiq.list_servers"))
		if servers.Code != http.StatusOK || !strings.Contains(servers.Body.String(), "세부 권한") {
			t.Errorf("a permission the user lacks was granted through the OAuth ceiling: %d %s", servers.Code, servers.Body.String())
		}
		// No token role or claim raises anything: the ceiling itself is what
		// the administrator wrote, whatever the token says.
		elevated := mcpCall(handler, "/mcp", token(map[string]any{"realm_access": map[string]any{"roles": []string{"admin"}}, "scope": "servers:read settings:write"}), mcpToolCall("jupiq.list_servers"))
		if !strings.Contains(elevated.Body.String(), "세부 권한") {
			t.Errorf("token claims raised permissions: %s", elevated.Body.String())
		}
	})

	t.Run("a valid token opens nothing outside MCP", func(t *testing.T) {
		rest := httptest.NewRequest(http.MethodGet, "/api/v1/dashboard", nil)
		rest.Header.Set("Authorization", "Bearer "+token(nil))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, rest)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("REST opened with an SSO token: %d %s", rec.Code, rec.Body.String())
		}
		me := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
		me.Header.Set("Authorization", "Bearer "+token(nil))
		rec = httptest.NewRecorder()
		handler.ServeHTTP(rec, me)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("account API opened with an SSO token: %d", rec.Code)
		}
	})

	t.Run("a token is refused for the right reason", func(t *testing.T) {
		refusedWith := func(what, bearer, want string) {
			t.Helper()
			rec := mcpCall(handler, "/mcp", bearer, mcpListTools)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("%s: %d %s", what, rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), want) {
				t.Errorf("%s: refusal does not say %q: %s", what, want, rec.Body.String())
			}
		}
		logs.Reset()
		refusedWith("another application's token", token(map[string]any{"aud": "account", "azp": marker + "-other-app"}), "발급된 것이 아닙니다")
		if body := mcpCall(handler, "/mcp", token(map[string]any{"aud": "account", "azp": marker + "-other-app"}), mcpListTools).Body.String(); !strings.Contains(body, "aud=[account]") || !strings.Contains(body, marker+"-other-app") || !strings.Contains(body, resource) {
			t.Errorf("audience refusal lacks what was seen and what to write: %s", body)
		}
		if !strings.Contains(logs.String(), "mcp oauth token refused") || !strings.Contains(logs.String(), "not accepted") {
			t.Errorf("the audience failure was not logged with its cause: %s", logs.String())
		}
		refusedWith("unknown account", token(map[string]any{"sub": marker + "-stranger"}), "등록되지 않았거나")
		var strangers int
		if err := database.Pool.QueryRow(ctx, `SELECT count(*) FROM users WHERE external_subject=$1`, auth.OIDCExternalIdentity(idp.URL(), marker+"-stranger")).Scan(&strangers); err != nil || strangers != 0 {
			t.Errorf("a token created an account: %d %v", strangers, err)
		}
		refusedWith("suspended account", token(map[string]any{"sub": marker + "-suspended"}), "비활성")
		refusedWith("expired", token(map[string]any{"exp": time.Now().Add(-time.Minute).Unix()}), "유효하지 않습니다")
		refusedWith("ID token", token(map[string]any{"typ": "ID"}), "ID 토큰")
		refusedWith("sender constrained", token(map[string]any{"cnf": map[string]any{"jkt": "x"}}), "cnf")
		other := authtest.NewFakeIDP(t)
		refusedWith("foreign signature", other.Sign(t, map[string]any{"iss": idp.URL(), "sub": marker + "-subject", "aud": resource, "typ": "Bearer", "exp": time.Now().Add(time.Hour).Unix()}), "유효하지 않습니다")
		refusedWith("garbage bearer", "not-a-token", "세션이 만료되었거나")
		if strings.Contains(logs.String(), marker+"-subject\"") && strings.Contains(logs.String(), "level=ERROR") {
			t.Errorf("refusals were logged as errors: %s", logs.String())
		}
	})

	t.Run("the administrator lists the client id instead of adding a mapper", func(t *testing.T) {
		saveSettingsForTest(t, database, member.ID, map[string]any{"mcp.oauth": mcpSection(true, marker+"-claude")})
		// Measured against Keycloak 26: aud=["account"], client in azp.
		viaAzp := mcpCall(handler, "/mcp", token(map[string]any{"aud": "account", "azp": marker + "-claude"}), mcpListTools)
		if viaAzp.Code != http.StatusOK {
			t.Errorf("azp listed by the administrator was refused: %d %s", viaAzp.Code, viaAzp.Body.String())
		}
		viaAud := mcpCall(handler, "/mcp", token(map[string]any{"aud": []string{"account", marker + "-claude"}}), mcpListTools)
		if viaAud.Code != http.StatusOK {
			t.Errorf("aud listed by the administrator was refused: %d %s", viaAud.Code, viaAud.Body.String())
		}
		stillOther := mcpCall(handler, "/mcp", token(map[string]any{"aud": "account", "azp": marker + "-cursor"}), mcpListTools)
		if stillOther.Code != http.StatusUnauthorized {
			t.Errorf("an unlisted client passed: %d", stillOther.Code)
		}
	})

	t.Run("keys and sessions work exactly as before", func(t *testing.T) {
		_, plain, err := database.CreateAPIKey(ctx, member.ID, "mcp", []string{"mcp:use", "dashboard:read"}, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if rec := mcpCall(handler, "/mcp", plain, mcpListTools); rec.Code != http.StatusOK {
			t.Errorf("personal key refused with SSO on: %d %s", rec.Code, rec.Body.String())
		}
		user, err := database.GetUser(ctx, member.ID)
		if err != nil {
			t.Fatal(err)
		}
		_, session, _, err := authService.CreateSession(ctx, user, "127.0.0.1", "test")
		if err != nil {
			t.Fatal(err)
		}
		// jupiq's own session JWT is a JWT too; it must still take the session
		// path on /mcp rather than being sent to Keycloak for verification.
		if rec := mcpCall(handler, "/mcp", session, mcpListTools); rec.Code != http.StatusOK {
			t.Errorf("session JWT as bearer refused on /mcp: %d %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("enabled without an issuer behaves as off and says why in the log", func(t *testing.T) {
		withoutIssuer := map[string]any{}
		for key, value := range oidcSection {
			withoutIssuer[key] = value
		}
		withoutIssuer["issuer_url"] = ""
		saveSettingsForTest(t, database, member.ID, map[string]any{"auth.oidc": withoutIssuer})
		logs.Reset()
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/.well-known/oauth-protected-resource/mcp", nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("metadata served without an issuer: %d", rec.Code)
		}
		refused := mcpCall(handler, "/mcp", token(nil), mcpListTools)
		if refused.Code != http.StatusUnauthorized || refused.Header().Get("WWW-Authenticate") != "" {
			t.Errorf("challenge issued without an issuer: %d %q", refused.Code, refused.Header().Get("WWW-Authenticate"))
		}
		if !strings.Contains(logs.String(), "mcp oauth is enabled but inactive") || !strings.Contains(logs.String(), "issuer_url") {
			t.Errorf("the reason was not logged: %s", logs.String())
		}
	})
}
