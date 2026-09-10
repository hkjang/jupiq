package integration

import (
	"net/http"
	"net/url"
	"strings"
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

// joinURL receives paths whose segments the caller already escaped. Assigning
// that text to URL.Path alone re-escaped every '%', so a JupyterHub user named
// "홍길동" or "john doe" was requested under a mangled, non-existent name.
func TestJoinURLKeepsCallerEncodingSingleEncoded(t *testing.T) {
	for _, test := range []struct {
		username string
		want     string
	}{
		{"plain", "https://hub.example.internal/hub/api/users/plain/server"},
		{"john doe", "https://hub.example.internal/hub/api/users/john%20doe/server"},
		{"홍길동", "https://hub.example.internal/hub/api/users/%ED%99%8D%EA%B8%B8%EB%8F%99/server"},
		{"a@b.com", "https://hub.example.internal/hub/api/users/a@b.com/server"},
		{"100%", "https://hub.example.internal/hub/api/users/100%25/server"},
	} {
		got, err := joinURL("https://hub.example.internal", "hub/api/users/"+url.PathEscape(test.username)+"/server")
		if err != nil {
			t.Fatalf("joinURL(%q): %v", test.username, err)
		}
		if got != test.want {
			t.Errorf("joinURL(%q)=%q want %q", test.username, got, test.want)
		}
	}
}

func TestJoinURLKeepsBasePathAndDropsQuery(t *testing.T) {
	got, err := joinURL("https://hub.example.internal/prefix/?stale=1", "api/v1/query")
	if err != nil {
		t.Fatal(err)
	}
	if want := "https://hub.example.internal/prefix/api/v1/query"; got != want {
		t.Fatalf("joinURL=%q want %q", got, want)
	}
}

// An escaped separator must stay escaped so a hub-supplied name can never walk
// out of the endpoint it belongs to.
func TestJoinURLKeepsEscapedSeparatorsEscaped(t *testing.T) {
	got, err := joinURL("https://hub.example.internal", "hub/api/users/"+url.PathEscape("user/../admin")+"/server")
	if err != nil {
		t.Fatal(err)
	}
	if want := "https://hub.example.internal/hub/api/users/user%2F..%2Fadmin/server"; got != want {
		t.Fatalf("joinURL=%q want %q", got, want)
	}
	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	if requestURI := parsed.RequestURI(); strings.Contains(requestURI, "/users/user/") {
		t.Fatalf("RequestURI leaked a decoded separator: %q", requestURI)
	}
}

func TestJupyterHubServerEndpointEscapesOnce(t *testing.T) {
	client := &JupyterHubClient{BaseURL: "https://hub.example.internal"}
	got, err := client.serverEndpoint("김연구", "")
	if err != nil {
		t.Fatal(err)
	}
	if want := "https://hub.example.internal/hub/api/users/%EA%B9%80%EC%97%B0%EA%B5%AC/server"; got != want {
		t.Fatalf("serverEndpoint=%q want %q", got, want)
	}
	got, err = client.serverEndpoint("김연구", "gpu lab")
	if err != nil {
		t.Fatal(err)
	}
	if want := "https://hub.example.internal/hub/api/users/%EA%B9%80%EC%97%B0%EA%B5%AC/servers/gpu%20lab"; got != want {
		t.Fatalf("serverEndpoint=%q want %q", got, want)
	}
}
