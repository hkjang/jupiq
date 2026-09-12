package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hkjang/jupiq/internal/auth"
	"github.com/hkjang/jupiq/internal/secure"
)

// oidcStateCookieFor는 로그인 단계가 굽는 것과 같은 형식의 state 쿠키를 만든다.
// 콜백은 저장소 없이 쿠키만으로 조용한 시도였는지 판단하므로 DB가 필요 없다.
func oidcStateCookieFor(t *testing.T, service *auth.Service, state string, silent bool, returnTo string) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"state": state, "nonce": "n", "code_verifier": "v", "return_to": returnTo, "silent": silent, "expires_at": time.Now().UTC().Add(5 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	cookie, err := service.Cipher.EncryptString(string(raw), "oidc-state")
	if err != nil {
		t.Fatal(err)
	}
	return cookie
}

func newOIDCTestServer(t *testing.T) *Server {
	t.Helper()
	cipher, err := secure.NewCipher(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	return &Server{Auth: auth.NewService(nil, cipher), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func callbackWithProviderError(t *testing.T, s *Server, query, cookie string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/oidc/callback?"+query, nil)
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: auth.OIDCStateCookie, Value: cookie})
	}
	rec := httptest.NewRecorder()
	s.oidcCallback(rec, req)
	return rec
}

func TestOIDCCallbackLoginRequiredAfterSilentAttemptLandsOnLoginWithMarker(t *testing.T) {
	s := newOIDCTestServer(t)
	cookie := oidcStateCookieFor(t, s.Auth, "st", true, "/users/user01?tab=servers")
	rec := callbackWithProviderError(t, s, "error=login_required&state=st", cookie)
	if rec.Code != http.StatusFound {
		t.Fatalf("status=%d want 302: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Location"); got != "/login?return_to=%2Fusers%2Fuser01%3Ftab%3Dservers&sso=none" {
		t.Fatalf("Location=%q", got)
	}
	cleared := false
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.OIDCStateCookie && c.MaxAge < 0 && c.Value == "" {
			cleared = true
		}
	}
	if !cleared {
		t.Fatal("state cookie was not cleared after the provider refused")
	}
}

func TestOIDCCallbackProviderErrorOnInteractiveAttemptIsMarkedAsError(t *testing.T) {
	s := newOIDCTestServer(t)
	cookie := oidcStateCookieFor(t, s.Auth, "st", false, "/hubs")
	rec := callbackWithProviderError(t, s, "error=access_denied&state=st", cookie)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/login?return_to=%2Fhubs&sso=error" {
		t.Fatalf("status=%d Location=%q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestOIDCCallbackProviderErrorWithoutReadableStateFailsClosed(t *testing.T) {
	s := newOIDCTestServer(t)
	silentCookie := oidcStateCookieFor(t, s.Auth, "st", true, "/hubs")
	for name, tc := range map[string]struct{ query, cookie string }{
		"no cookie":        {"error=login_required&state=st", ""},
		"state mismatch":   {"error=login_required&state=other", silentCookie},
		"tampered cookie":  {"error=login_required&state=st", silentCookie + "x"},
		"no state at all":  {"error=login_required", silentCookie},
		"unrelated cookie": {"error=login_required&state=st", "garbage"},
	} {
		rec := callbackWithProviderError(t, s, tc.query, tc.cookie)
		if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/login?sso=error" {
			t.Fatalf("%s: status=%d Location=%q want /login?sso=error", name, rec.Code, rec.Header().Get("Location"))
		}
	}
}

func TestOIDCCallbackWithoutCodeStillRejectsWhenNoProviderError(t *testing.T) {
	s := newOIDCTestServer(t)
	rec := callbackWithProviderError(t, s, "state=st", oidcStateCookieFor(t, s.Auth, "st", true, "/"))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"oidc_callback_invalid"`) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestValidateSettingsAcceptsOIDCAutoLoginAsBoolean(t *testing.T) {
	base := map[string]any{
		"enabled": false, "issuer_url": "", "client_id": "", "redirect_url": "",
		"username_claim": "preferred_username", "scopes": []any{"openid"}, "verify_tls": true,
	}
	withAutoLogin := func(value any) map[string]any {
		object := map[string]any{}
		for k, v := range base {
			object[k] = v
		}
		object["auto_login"] = value
		return object
	}
	if err := validateSettingsUpdate(map[string]any{"auth.oidc": withAutoLogin(true)}, nil); err != nil {
		t.Fatalf("auto_login=true was rejected: %v", err)
	}
	if err := validateSettingsUpdate(map[string]any{"auth.oidc": withAutoLogin("yes")}, nil); err == nil || !strings.Contains(err.Error(), "auto_login") {
		t.Fatalf("non-boolean auto_login was accepted: %v", err)
	}
}
