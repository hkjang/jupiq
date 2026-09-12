package auth

import (
	"strings"
	"testing"
	"time"

	"github.com/hkjang/jupiq/internal/secure"
)

func newStatelessService(t *testing.T) *Service {
	t.Helper()
	cipher, err := secure.NewCipher(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	return &Service{Cipher: cipher}
}

func TestSilentLoginAllowedOnlyWhenAutoLoginIsOn(t *testing.T) {
	for _, tc := range []struct {
		name      string
		cfg       OIDCConfig
		requested bool
		want      bool
	}{
		{"default installation ignores ?prompt=none", OIDCConfig{Enabled: true}, true, false},
		{"auto_login without OIDC enabled is inert", OIDCConfig{AutoLogin: true}, true, false},
		{"auto_login on honours the request", OIDCConfig{Enabled: true, AutoLogin: true}, true, true},
		{"auto_login on never forces a silent attempt", OIDCConfig{Enabled: true, AutoLogin: true}, false, false},
	} {
		if got := SilentLoginAllowed(tc.cfg, tc.requested); got != tc.want {
			t.Fatalf("%s: SilentLoginAllowed = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestOIDCRefusalReportsSilentAttemptAndReturnTo(t *testing.T) {
	s := newStatelessService(t)
	cookie, err := s.sealOIDCState(oidcState{State: "abc", Silent: true, ReturnTo: "/users/user01?tab=servers", ExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	silent, returnTo := s.OIDCRefusal(cookie, "abc")
	if !silent || returnTo != "/users/user01?tab=servers" {
		t.Fatalf("silent state was not recognised: silent=%v return_to=%q", silent, returnTo)
	}

	plain, err := s.sealOIDCState(oidcState{State: "abc", ReturnTo: "/dashboard", ExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if silent, returnTo := s.OIDCRefusal(plain, "abc"); silent || returnTo != "/dashboard" {
		t.Fatalf("interactive state was reported as silent: silent=%v return_to=%q", silent, returnTo)
	}
}

func TestOIDCRefusalFailsClosedWhenStateCannotBeRead(t *testing.T) {
	s := newStatelessService(t)
	valid, err := s.sealOIDCState(oidcState{State: "abc", Silent: true, ReturnTo: "/hubs", ExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	expired, err := s.sealOIDCState(oidcState{State: "abc", Silent: true, ReturnTo: "/hubs", ExpiresAt: time.Now().Add(-time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct{ cookie, state string }{
		"missing cookie":   {"", "abc"},
		"garbage cookie":   {"not-a-ciphertext", "abc"},
		"state mismatch":   {valid, "other"},
		"expired state":    {expired, "abc"},
		"missing state":    {valid, ""},
		"unsafe return_to": {mustSeal(t, s, oidcState{State: "abc", Silent: true, ReturnTo: "//evil.example", ExpiresAt: time.Now().Add(time.Minute)}), "abc"},
	} {
		silent, returnTo := s.OIDCRefusal(tc.cookie, tc.state)
		if name == "unsafe return_to" {
			if !silent || returnTo != DefaultReturnTo {
				t.Fatalf("%s: off-site return_to survived: silent=%v return_to=%q", name, silent, returnTo)
			}
			continue
		}
		if silent || returnTo != DefaultReturnTo {
			t.Fatalf("%s: unreadable state must not count as silent: silent=%v return_to=%q", name, silent, returnTo)
		}
	}
}

func mustSeal(t *testing.T, s *Service, value oidcState) string {
	t.Helper()
	cookie, err := s.sealOIDCState(value)
	if err != nil {
		t.Fatal(err)
	}
	return cookie
}

func TestLoginPathAfterRefusalMarksOutcomeAndKeepsDeepLink(t *testing.T) {
	for _, tc := range []struct {
		silent   bool
		returnTo string
		want     string
	}{
		{true, "/", "/login?sso=none"},
		{true, "/users/user01?tab=servers", "/login?return_to=%2Fusers%2Fuser01%3Ftab%3Dservers&sso=none"},
		{false, "/", "/login?sso=error"},
		{false, "/hubs", "/login?return_to=%2Fhubs&sso=error"},
		{true, "https://evil.example/", "/login?sso=none"},
		{true, "//evil.example", "/login?sso=none"},
	} {
		got := LoginPathAfterRefusal(tc.silent, tc.returnTo)
		if got != tc.want {
			t.Fatalf("LoginPathAfterRefusal(%v, %q) = %q, want %q", tc.silent, tc.returnTo, got, tc.want)
		}
		if !strings.HasPrefix(got, LoginPath+"?") {
			t.Fatalf("refusal must land on the login page, got %q", got)
		}
	}
}
