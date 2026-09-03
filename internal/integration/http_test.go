package integration

import (
	"net/http"
	"testing"
	"time"
)

func TestValidateEndpoint(t *testing.T) {
	for _, good := range []string{"https://jupyter.internal", "http://10.0.0.2:8000/base"} {
		if _, err := ValidateEndpoint(good); err != nil {
			t.Errorf("ValidateEndpoint(%q): %v", good, err)
		}
	}
	for _, bad := range []string{"file:///etc/passwd", "https://user:password@example.com", "//missing-scheme", "javascript:alert(1)", "http://127.0.0.1:8080", "http://[::1]", "http://169.254.169.254", "http://metadata.google.internal"} {
		if _, err := ValidateEndpoint(bad); err == nil {
			t.Errorf("ValidateEndpoint(%q) accepted unsafe URL", bad)
		}
	}
}

func TestHTTPClientRejectsTLSRedirectDowngrade(t *testing.T) {
	client := newHTTPClient(HTTPOptions{VerifyTLS: true, Timeout: time.Second})
	previous, _ := http.NewRequest(http.MethodGet, "https://service.example.internal/api", nil)
	next, _ := http.NewRequest(http.MethodGet, "http://service.example.internal/api", nil)
	next.Header.Set("Authorization", "Bearer must-not-leak")
	if err := client.CheckRedirect(next, []*http.Request{previous}); err == nil {
		t.Fatal("HTTPS to HTTP redirect was accepted")
	}
}

func TestHTTPClientStripsCredentialsAcrossOrigins(t *testing.T) {
	client := newHTTPClient(HTTPOptions{VerifyTLS: true, Timeout: time.Second})
	previous, _ := http.NewRequest(http.MethodGet, "https://service.example.internal/api", nil)
	next, _ := http.NewRequest(http.MethodGet, "https://other.example.internal/api", nil)
	next.Header.Set("Authorization", "Bearer must-not-leak")
	next.Header.Set("Cookie", "session=must-not-leak")
	if err := client.CheckRedirect(next, []*http.Request{previous}); err != nil {
		t.Fatal(err)
	}
	if next.Header.Get("Authorization") != "" || next.Header.Get("Cookie") != "" {
		t.Fatalf("credentials survived cross-origin redirect: %#v", next.Header)
	}
}
