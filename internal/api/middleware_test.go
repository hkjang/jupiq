package api

import (
	"crypto/tls"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func middlewareHandler(t *testing.T, next http.HandlerFunc) http.Handler {
	t.Helper()
	s := &Server{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	return s.middleware(next)
}

func serveMiddleware(t *testing.T, r *http.Request, next http.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	middlewareHandler(t, next).ServeHTTP(rec, r)
	return rec
}

func okHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func TestMiddlewareSetsSecurityHeaders(t *testing.T) {
	rec := serveMiddleware(t, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil), okHandler)

	want := map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "same-origin",
		"Permissions-Policy":     "camera=(), microphone=(), geolocation=()",
	}
	for name, value := range want {
		if got := rec.Header().Get(name); got != value {
			t.Fatalf("%s=%q want %q", name, got, value)
		}
	}
	// 화면이 아닌 응답은 실행할 것이 없으므로 아무것도 허용하지 않는다.
	if csp := rec.Header().Get("Content-Security-Policy"); csp != "default-src 'none'; frame-ancestors 'none'" {
		t.Fatalf("API Content-Security-Policy=%q", csp)
	}
	for _, path := range []string{"/mcp", "/healthz", "/readyz", "/momento/tracker.js"} {
		if csp := serveMiddleware(t, httptest.NewRequest(http.MethodGet, path, nil), okHandler).Header().Get("Content-Security-Policy"); csp != apiSecurityPolicy {
			t.Fatalf("%s: Content-Security-Policy=%q want the API policy", path, csp)
		}
	}

	page := serveMiddleware(t, httptest.NewRequest(http.MethodGet, "/dashboard", nil), okHandler)
	csp := page.Header().Get("Content-Security-Policy")
	for _, directive := range []string{"default-src 'self'", "script-src 'self'", "object-src 'none'", "frame-ancestors 'none'", "base-uri 'self'", "form-action 'self'"} {
		if !strings.Contains(csp, directive) {
			t.Fatalf("page Content-Security-Policy=%q is missing %q", csp, directive)
		}
	}
	if strings.Contains(csp, "script-src 'self' 'unsafe-inline'") || strings.Contains(csp, "nonce-") {
		t.Fatalf("page policy without tracking must stay strict and nonce-free: %q", csp)
	}
}

func TestMiddlewareSendsHSTSOnlyOverTLS(t *testing.T) {
	plain := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	if got := serveMiddleware(t, plain, okHandler).Header().Get("Strict-Transport-Security"); got != "" {
		// 평문 HTTP로만 접근하는 폐쇄망에서 HSTS를 남기면 접속이 영구히 막힌다.
		t.Fatalf("Strict-Transport-Security=%q want empty over plain HTTP", got)
	}

	direct := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	direct.TLS = &tls.ConnectionState{}
	proxied := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	proxied.Header.Set("X-Forwarded-Proto", "https, http")
	for _, r := range []*http.Request{direct, proxied} {
		if got := serveMiddleware(t, r, okHandler).Header().Get("Strict-Transport-Security"); got != hstsMaxAge {
			t.Fatalf("Strict-Transport-Security=%q want %q", got, hstsMaxAge)
		}
	}
}

func TestMiddlewareRejectsCrossOriginMutations(t *testing.T) {
	cases := []struct {
		name    string
		method  string
		origin  string
		fetch   string
		allowed bool
	}{
		{name: "동일 출처 POST", method: http.MethodPost, origin: "http://example.test", allowed: true},
		{name: "다른 호스트 POST", method: http.MethodPost, origin: "http://evil.test", allowed: false},
		{name: "다른 스킴 POST", method: http.MethodPost, origin: "https://example.test", allowed: false},
		{name: "Sec-Fetch-Site cross-site DELETE", method: http.MethodDelete, fetch: "cross-site", allowed: false},
		{name: "Origin 없는 API 클라이언트 PUT", method: http.MethodPut, allowed: true},
		{name: "다른 호스트 GET", method: http.MethodGet, origin: "http://evil.test", allowed: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, "http://example.test/api/v1/hubs", nil)
			if tc.origin != "" {
				r.Header.Set("Origin", tc.origin)
			}
			if tc.fetch != "" {
				r.Header.Set("Sec-Fetch-Site", tc.fetch)
			}
			reached := false
			rec := serveMiddleware(t, r, func(w http.ResponseWriter, r *http.Request) {
				reached = true
				okHandler(w, r)
			})
			if reached != tc.allowed {
				t.Fatalf("handler reached=%v want %v", reached, tc.allowed)
			}
			if tc.allowed {
				return
			}
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status=%d want 403", rec.Code)
			}
			if !strings.Contains(rec.Body.String(), `"origin_denied"`) {
				t.Fatalf("body=%q want origin_denied", rec.Body.String())
			}
		})
	}
}

func TestMiddlewareEchoesAndGeneratesRequestID(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	r.Header.Set("X-Request-ID", "caller-supplied")
	if got := serveMiddleware(t, r, okHandler).Header().Get("X-Request-ID"); got != "caller-supplied" {
		t.Fatalf("X-Request-ID=%q want the caller value", got)
	}

	generated := serveMiddleware(t, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil), okHandler)
	if generated.Header().Get("X-Request-ID") == "" {
		t.Fatal("X-Request-ID want a generated value when the caller sends none")
	}
}

func TestMiddlewareRecoversFromHandlerPanic(t *testing.T) {
	rec := serveMiddleware(t, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil), func(http.ResponseWriter, *http.Request) {
		panic("boom")
	})
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"internal_error"`) {
		t.Fatalf("body=%q want the internal_error envelope", rec.Body.String())
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("security headers must survive a panicking handler")
	}
}
