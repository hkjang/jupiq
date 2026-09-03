package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hkjang/jupiq/internal/auth"
	"github.com/hkjang/jupiq/internal/secure"
	"github.com/hkjang/jupiq/internal/store"
)

func TestIntegrationPreflightPermissionsAreSeparated(t *testing.T) {
	tests := []struct {
		name        string
		kind        string
		permissions []string
	}{
		{name: "Hub test needs Hub administration", kind: "jupyterhub", permissions: []string{"settings:write"}},
		{name: "provider test needs settings administration", kind: "prometheus", permissions: []string{"hubs:write"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/integrations/test", strings.NewReader(`{"type":"`+test.kind+`","config":{"base_url":"https://service.internal"}}`))
			req = withPrincipal(req, auth.Principal{UserPermissions: test.permissions})
			response := httptest.NewRecorder()
			(&Server{}).integrationTest(response, req)
			if response.Code != http.StatusForbidden {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestSavedSecretBindingRejectsTargetOrTLSChanges(t *testing.T) {
	saved := map[string]any{"base_url": "https://metrics.internal/", "verify_tls": true}
	if !integrationBindingMatches("prometheus", map[string]any{"base_url": "https://metrics.internal", "verify_tls": true}, saved) {
		t.Fatal("same canonical target and TLS binding was rejected")
	}
	if integrationBindingMatches("prometheus", map[string]any{"base_url": "https://attacker.internal", "verify_tls": true}, saved) {
		t.Fatal("saved secret could be sent to a changed target")
	}
	if integrationBindingMatches("prometheus", map[string]any{"base_url": "https://metrics.internal", "verify_tls": false}, saved) {
		t.Fatal("saved secret could be sent after disabling TLS verification")
	}
	if !integrationBindingMatches("oidc", map[string]any{"issuer_url": "https://sso.internal/realms/main", "client_id": "jupiq", "verify_tls": true}, map[string]any{"issuer_url": "https://sso.internal/realms/main/", "client_id": "jupiq", "verify_tls": true}) {
		t.Fatal("OIDC issuer binding was not recognized")
	}
	if integrationBindingMatches("oidc", map[string]any{"issuer_url": "https://sso.internal/realms/main", "client_id": "other-client", "verify_tls": true}, map[string]any{"issuer_url": "https://sso.internal/realms/main", "client_id": "jupiq", "verify_tls": true}) {
		t.Fatal("saved OIDC secret could be reused with a different client ID")
	}
}

func TestSecureIntegrationDefaultsKeepTLSVerificationEnabled(t *testing.T) {
	values := map[string]any{
		"auth.oidc":  map[string]any{"issuer_url": "https://sso.internal", "client_id": "jupiq"},
		"ai":         map[string]any{"base_url": "https://ai.internal"},
		"prometheus": map[string]any{"base_url": "https://metrics.internal"},
		"kubernetes": map[string]any{"base_url": "https://cluster.internal"},
	}
	applySecureIntegrationDefaults(values)
	for section, raw := range values {
		if raw.(map[string]any)["verify_tls"] != true {
			t.Fatalf("%s omitted verify_tls did not default to true: %#v", section, raw)
		}
	}
	ai := values["ai"].(map[string]any)
	if ai["provider"] != "openai-compatible" || ai["streaming"] != true {
		t.Fatalf("AI provider/streaming persistence invariant was not forced: %#v", ai)
	}

	values["ai"] = map[string]any{"provider": "unsupported", "streaming": false}
	applySecureIntegrationDefaults(values)
	ai = values["ai"].(map[string]any)
	if ai["provider"] != "openai-compatible" || ai["streaming"] != true {
		t.Fatalf("AI invariant could be overridden by a client: %#v", ai)
	}
}

func TestJupyterHubPreflightUsesCandidateValuesAndSavedHubTokenIntegration(t *testing.T) {
	dsn := os.Getenv("JUPIQ_INTEGRATION_TEST_DSN")
	if dsn == "" {
		t.Skip("set JUPIQ_INTEGRATION_TEST_DSN to run PostgreSQL integration coverage")
	}
	ip := nonLoopbackIPv4(t)
	listener, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	const savedToken = "saved-hub-token"
	var remoteCalls atomic.Int32
	remote := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		remoteCalls.Add(1)
		if r.Header.Get("Authorization") != "token "+savedToken {
			http.Error(w, "missing saved token", http.StatusUnauthorized)
			return
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/hub/api/info"):
			_, _ = w.Write([]byte(`{"version":"5.3.0"}`))
		case strings.HasSuffix(r.URL.Path, "/hub/api/users"):
			_, _ = w.Write([]byte(`[]`))
		default:
			http.NotFound(w, r)
		}
	}))
	remote.Listener = listener
	remote.Start()
	defer remote.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	candidateURL := fmt.Sprintf("http://%s:%d", ip, port)

	cipher, err := secure.NewCipher(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	database, err := store.Open(context.Background(), dsn, cipher)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	marker := fmt.Sprintf("hub-preflight-%d", time.Now().UnixNano())
	if _, err := database.CreateHub(context.Background(), store.HubWrite{Name: marker + "-missing-token", Network: "test", BaseURL: candidateURL}); err == nil || !strings.Contains(err.Error(), "api_token") {
		t.Fatalf("Hub creation without an admin token was accepted: %v", err)
	}
	verifyTLS := false
	hub, err := database.CreateHub(context.Background(), store.HubWrite{Name: marker, Network: "test", BaseURL: candidateURL, APIToken: savedToken, VerifyTLS: &verifyTLS})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.DeleteHub(context.Background(), hub.ID) }()

	body := fmt.Sprintf(`{"type":"jupyterhub","config":{"hub_id":%d,"base_url":%q,"verify_tls":false}}`, hub.ID, candidateURL)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/integrations/test", strings.NewReader(body))
	req.Header.Set("X-Request-ID", marker)
	scopedPrincipal := auth.Principal{User: store.User{
		Username:          marker,
		Permissions:       []string{"hubs:write"},
		GlobalPermissions: []string{},
		ScopedPermissions: []string{"hubs:write"},
		PermissionGrants:  []store.PermissionGrant{{Permissions: []string{"hubs:write"}, HubIDs: []int64{hub.ID}}},
	}}
	keyLimitedPrincipal := scopedPrincipal
	keyLimitedPrincipal.APIKeyID = 101
	keyLimitedPrincipal.APIKeyScopes = []string{"hubs:read"}
	keyLimitedReq := httptest.NewRequest(http.MethodPost, "/api/v1/integrations/test", strings.NewReader(body))
	keyLimitedReq = withPrincipal(keyLimitedReq, keyLimitedPrincipal)
	keyLimitedResponse := httptest.NewRecorder()
	(&Server{Store: database}).integrationTest(keyLimitedResponse, keyLimitedReq)
	if keyLimitedResponse.Code != http.StatusForbidden {
		t.Fatalf("API key scope did not constrain scoped Hub preflight: status=%d body=%s", keyLimitedResponse.Code, keyLimitedResponse.Body.String())
	}
	req = withPrincipal(req, scopedPrincipal)
	response := httptest.NewRecorder()
	(&Server{Store: database}).integrationTest(response, req)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"success":true`) || !strings.Contains(response.Body.String(), `"version":"5.3.0"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if remoteCalls.Load() != 2 {
		t.Fatalf("matching saved-token preflight calls=%d want=2", remoteCalls.Load())
	}

	wrongHubBody := fmt.Sprintf(`{"type":"jupyterhub","config":{"hub_id":%d,"base_url":%q,"verify_tls":false},"secret":%q}`, hub.ID+1, candidateURL, savedToken)
	wrongHubReq := httptest.NewRequest(http.MethodPost, "/api/v1/integrations/test", strings.NewReader(wrongHubBody))
	wrongHubReq = withPrincipal(wrongHubReq, scopedPrincipal)
	wrongHubResponse := httptest.NewRecorder()
	(&Server{Store: database}).integrationTest(wrongHubResponse, wrongHubReq)
	if wrongHubResponse.Code != http.StatusForbidden {
		t.Fatalf("scoped Hub preflight opened an unassigned Hub: status=%d body=%s", wrongHubResponse.Code, wrongHubResponse.Body.String())
	}

	newHubReq := httptest.NewRequest(http.MethodPost, "/api/v1/integrations/test", strings.NewReader(fmt.Sprintf(`{"type":"jupyterhub","config":{"base_url":%q,"verify_tls":false},"secret":%q}`, candidateURL, savedToken)))
	newHubReq = withPrincipal(newHubReq, scopedPrincipal)
	newHubResponse := httptest.NewRecorder()
	(&Server{Store: database}).integrationTest(newHubResponse, newHubReq)
	if newHubResponse.Code != http.StatusForbidden {
		t.Fatalf("scoped Hub permission opened a new-Hub preflight: status=%d body=%s", newHubResponse.Code, newHubResponse.Body.String())
	}

	// A candidate target/TLS change cannot reuse the saved credential.
	changedURL := candidateURL + "/changed"
	changedBody := fmt.Sprintf(`{"type":"jupyterhub","config":{"hub_id":%d,"base_url":%q,"verify_tls":false}}`, hub.ID, changedURL)
	changedReq := httptest.NewRequest(http.MethodPost, "/api/v1/integrations/test", strings.NewReader(changedBody))
	changedReq = withPrincipal(changedReq, auth.Principal{UserPermissions: []string{"hubs:write"}})
	changedResponse := httptest.NewRecorder()
	(&Server{Store: database}).integrationTest(changedResponse, changedReq)
	if changedResponse.Code != http.StatusBadRequest || !strings.Contains(changedResponse.Body.String(), `"code":"hub_token_reentry_required"`) {
		t.Fatalf("changed target reused saved token: status=%d body=%s", changedResponse.Code, changedResponse.Body.String())
	}
	if remoteCalls.Load() != 2 {
		t.Fatalf("rejected preflight contacted candidate target: calls=%d", remoteCalls.Load())
	}

	// Supplying the credential again permits testing a changed target, but does
	// not persist that candidate configuration.
	changedWithSecret := fmt.Sprintf(`{"type":"jupyterhub","config":{"hub_id":%d,"base_url":%q,"verify_tls":false},"secret":%q}`, hub.ID, changedURL, savedToken)
	secretReq := httptest.NewRequest(http.MethodPost, "/api/v1/integrations/test", strings.NewReader(changedWithSecret))
	secretReq = withPrincipal(secretReq, auth.Principal{UserPermissions: []string{"hubs:write"}})
	secretResponse := httptest.NewRecorder()
	(&Server{Store: database}).integrationTest(secretResponse, secretReq)
	if secretResponse.Code != http.StatusOK || !strings.Contains(secretResponse.Body.String(), `"success":true`) {
		t.Fatalf("explicit-token candidate test failed: status=%d body=%s", secretResponse.Code, secretResponse.Body.String())
	}
	persisted, err := database.GetHub(context.Background(), hub.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.BaseURL != candidateURL {
		t.Fatalf("preflight unexpectedly saved candidate URL: %q", persisted.BaseURL)
	}
	if _, err := database.UpdateHub(context.Background(), hub.ID, store.HubWrite{BaseURL: changedURL}); err == nil || !strings.Contains(err.Error(), "token") {
		t.Fatalf("Hub target changed while silently retaining its token: %v", err)
	}
}

func TestEffectiveSystemSettingsRoundTripIntegration(t *testing.T) {
	dsn := os.Getenv("JUPIQ_INTEGRATION_TEST_DSN")
	if dsn == "" {
		t.Skip("set JUPIQ_INTEGRATION_TEST_DSN to run PostgreSQL integration coverage")
	}
	cipher, err := secure.NewCipher(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	database, err := store.Open(context.Background(), dsn, cipher)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var previous []byte
	if err := database.Pool.QueryRow(context.Background(), `SELECT value FROM settings WHERE setting_key='system'`).Scan(&previous); err != nil {
		t.Fatal(err)
	}
	marker := fmt.Sprintf("settings-roundtrip-%d", time.Now().UnixNano())
	var actorID int64
	if err := database.Pool.QueryRow(context.Background(), `INSERT INTO users(username,display_name,auth_source) VALUES($1,$1,'local') RETURNING id`, marker).Scan(&actorID); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = database.Pool.Exec(context.Background(), `UPDATE settings SET value=$1,updated_by=NULL WHERE setting_key='system'`, previous)
		_, _ = database.Pool.Exec(context.Background(), `DELETE FROM audit_logs WHERE actor_username=$1`, marker)
		_, _ = database.Pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, actorID)
	}()
	server := &Server{Store: database}
	call := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPut, "/api/v1/settings", strings.NewReader(body))
		req = withPrincipal(req, auth.Principal{User: store.User{ID: actorID, Username: marker}})
		response := httptest.NewRecorder()
		server.settingsPut(response, req)
		return response
	}

	valid := call(`{"system":{"raw_retention_days":45}}`)
	if valid.Code != http.StatusOK || !strings.Contains(valid.Body.String(), `"raw_retention_days":45`) {
		t.Fatalf("effective setting did not round-trip: status=%d body=%s", valid.Code, valid.Body.String())
	}
	var current map[string]any
	if err := database.GetSetting(context.Background(), "system", &current); err != nil {
		t.Fatal(err)
	}
	if len(current) != 1 || current["raw_retention_days"] != float64(45) {
		t.Fatalf("unexpected stored system settings: %#v", current)
	}

	legacy := call(`{"system":{"raw_retention_days":45,"service_name":"not-applied"}}`)
	if legacy.Code != http.StatusBadRequest || !strings.Contains(legacy.Body.String(), "service_name") {
		t.Fatalf("inactive setting was not rejected: status=%d body=%s", legacy.Code, legacy.Body.String())
	}
	if err := database.GetSetting(context.Background(), "jupyterhub", &current); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("obsolete generic Hub setting survived migration: %#v err=%v", current, err)
	}
}

func TestOIDCEnablementSecretInvariantIntegration(t *testing.T) {
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
	var previousSetting []byte
	if err := database.Pool.QueryRow(ctx, `SELECT value FROM settings WHERE setting_key='auth.oidc'`).Scan(&previousSetting); err != nil {
		t.Fatal(err)
	}
	previousSecret, previousSecretErr := database.GetSecret(ctx, "oidc.client_secret")
	if previousSecretErr != nil && !errors.Is(previousSecretErr, store.ErrNotFound) {
		t.Fatal(previousSecretErr)
	}
	marker := fmt.Sprintf("oidc-invariant-%d", time.Now().UnixNano())
	var actorID int64
	if err := database.Pool.QueryRow(ctx, `INSERT INTO users(username,display_name,auth_source) VALUES($1,$1,'local') RETURNING id`, marker).Scan(&actorID); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = database.Pool.Exec(ctx, `UPDATE settings SET value=$1,updated_by=NULL WHERE setting_key='auth.oidc'`, previousSetting)
		_, _ = database.Pool.Exec(ctx, `DELETE FROM secrets WHERE secret_key='oidc.client_secret'`)
		if previousSecretErr == nil {
			_ = database.SetSecret(ctx, "oidc.client_secret", previousSecret, actorID)
		}
		_, _ = database.Pool.Exec(ctx, `DELETE FROM audit_logs WHERE actor_username=$1`, marker)
		_, _ = database.Pool.Exec(ctx, `DELETE FROM users WHERE id=$1`, actorID)
	}()
	_, _ = database.Pool.Exec(ctx, `DELETE FROM secrets WHERE secret_key='oidc.client_secret'`)
	oidcValues := map[string]any{"enabled": true, "issuer_url": "https://sso.internal/realms/test", "client_id": "jupiq", "verify_tls": true}
	oidcRaw, _ := json.Marshal(oidcValues)
	// Simulate a legacy/incomplete row that predates the transactional invariant.
	if _, err := database.Pool.Exec(ctx, `UPDATE settings SET value=$1,updated_by=$2 WHERE setting_key='auth.oidc'`, oidcRaw, actorID); err != nil {
		t.Fatal(err)
	}

	server := &Server{Store: database, Auth: auth.NewService(database, cipher)}
	publicRequest := httptest.NewRequest(http.MethodGet, "/api/v1/auth/oidc/config", nil)
	publicResponse := httptest.NewRecorder()
	server.oidcConfig(publicResponse, publicRequest)
	if publicResponse.Code != http.StatusOK || !strings.Contains(publicResponse.Body.String(), `"enabled":false`) || !strings.Contains(publicResponse.Body.String(), `"secret_configured":false`) {
		t.Fatalf("public config exposed unusable SSO: status=%d body=%s", publicResponse.Code, publicResponse.Body.String())
	}

	call := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPut, "/api/v1/settings", strings.NewReader(body))
		req = withPrincipal(req, auth.Principal{User: store.User{ID: actorID, Username: marker}})
		response := httptest.NewRecorder()
		server.settingsPut(response, req)
		return response
	}
	withoutSecret := `{"auth.oidc":{"enabled":true,"issuer_url":"https://sso.internal/realms/test","client_id":"jupiq","username_claim":"preferred_username","scopes":["openid","profile"],"verify_tls":true}}`
	missing := call(withoutSecret)
	if missing.Code != http.StatusBadRequest || !strings.Contains(missing.Body.String(), `"code":"oidc_secret_required"`) {
		t.Fatalf("enabled OIDC without secret was accepted: status=%d body=%s", missing.Code, missing.Body.String())
	}
	withNewSecret := call(`{"auth.oidc":{"enabled":true,"issuer_url":"https://sso.internal/realms/test","client_id":"jupiq","username_claim":"preferred_username","scopes":["openid","profile"],"client_secret":"new-client-secret","verify_tls":true}}`)
	if withNewSecret.Code != http.StatusOK {
		t.Fatalf("enabled OIDC with new secret was rejected: status=%d body=%s", withNewSecret.Code, withNewSecret.Body.String())
	}
	changedTarget := call(`{"auth.oidc":{"enabled":true,"issuer_url":"https://other-sso.internal/realms/test","client_id":"jupiq","username_claim":"preferred_username","scopes":["openid","profile"],"verify_tls":true}}`)
	if changedTarget.Code != http.StatusBadRequest || !strings.Contains(changedTarget.Body.String(), `"code":"secret_reentry_required"`) {
		t.Fatalf("OIDC target changed while silently retaining its secret: status=%d body=%s", changedTarget.Code, changedTarget.Body.String())
	}
	withExistingSecret := call(withoutSecret)
	if withExistingSecret.Code != http.StatusOK {
		t.Fatalf("enabled OIDC with existing secret was rejected: status=%d body=%s", withExistingSecret.Code, withExistingSecret.Body.String())
	}
}

func nonLoopbackIPv4(t *testing.T) string {
	t.Helper()
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatal(err)
	}
	for _, address := range addresses {
		network, ok := address.(*net.IPNet)
		if ok && network.IP.To4() != nil && !network.IP.IsLoopback() {
			return network.IP.String()
		}
	}
	t.Skip("non-loopback IPv4 is required for SSRF-safe integration test")
	return ""
}
