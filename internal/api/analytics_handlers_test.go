package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/hkjang/jupiq/internal/analytics"
)

const testIndex = "<!doctype html><html><head><title>jupiq</title></head><body><div id=\"root\"></div></body></html>"

func momentoConfig(proxy bool) analytics.Config {
	config := analytics.Default()
	config.Enabled = true
	config.Provider = analytics.ProviderMomento
	config.MomentoURL = "https://momento.corp.example"
	config.MomentoSiteID = "SITE_1"
	config.MomentoProxy = proxy
	return config
}

func servePage(t *testing.T, path string, config analytics.Config) *httptest.ResponseRecorder {
	t.Helper()
	s := &Server{}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.URL.Path = path
	rec := httptest.NewRecorder()
	s.serveSPAWith(rec, r, config)
	return rec
}

var noncePattern = regexp.MustCompile(`nonce="([A-Za-z0-9_-]+)"`)

func TestServeSPAWithoutTrackingIsUnchanged(t *testing.T) {
	writeDist(t, map[string]string{"index.html": testIndex})
	for _, config := range []analytics.Config{analytics.Default(), {Enabled: true, Provider: analytics.ProviderNone}, {Enabled: false, Provider: analytics.ProviderMomento, MomentoURL: "https://m.example", MomentoSiteID: "S"}} {
		rec := servePage(t, "/dashboard", config)
		if rec.Code != http.StatusOK || rec.Body.String() != testIndex {
			t.Fatalf("page must be served untouched: status=%d body=%q", rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("Content-Security-Policy"); got != contentSecurityPolicy {
			t.Fatalf("policy must stay strict when tracking is off: %q", got)
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Fatalf("Cache-Control=%q want no-store", got)
		}
	}
}

func TestServeSPAInjectsNoncedSnippetAndMatchingPolicy(t *testing.T) {
	writeDist(t, map[string]string{"index.html": testIndex})
	rec := servePage(t, "/dashboard", momentoConfig(true))
	body := rec.Body.String()
	match := noncePattern.FindStringSubmatch(body)
	if match == nil {
		t.Fatalf("no nonce in injected page: %q", body)
	}
	nonce := match[1]
	if !strings.Contains(body, `<script nonce="`+nonce+`" async src="/momento/tracker.js"`) || !strings.Contains(body, `data-endpoint="/momento"`) {
		t.Fatalf("Momento proxy snippet missing: %q", body)
	}
	if head := strings.Index(body, "</head>"); head < 0 || strings.Index(body, "<script") > head {
		t.Fatalf("head placement should put the snippet before </head>: %q", body)
	}
	policy := rec.Header().Get("Content-Security-Policy")
	for _, want := range []string{"script-src 'self' 'nonce-" + nonce + "'", "connect-src 'self'", "report-uri " + cspReportPath, "object-src 'none'", "frame-ancestors 'none'", "base-uri 'self'", "form-action 'self'"} {
		if !strings.Contains(policy, want) {
			t.Fatalf("policy %q is missing %q", policy, want)
		}
	}
	if strings.Contains(policy, "momento.corp.example") {
		t.Fatalf("proxied Momento must not name the collector in the policy: %q", policy)
	}
	if strings.Contains(policy, "script-src 'self' 'unsafe-inline'") || strings.Contains(strings.SplitN(policy, "script-src", 2)[1], "'unsafe-inline'") {
		t.Fatalf("script-src must never be relaxed with 'unsafe-inline': %q", policy)
	}

	// 같은 페이지를 다시 받으면 nonce가 달라야 한다.
	again := noncePattern.FindStringSubmatch(servePage(t, "/dashboard", momentoConfig(true)).Body.String())
	if again == nil || again[1] == nonce {
		t.Fatalf("nonce must differ per request: %q vs %v", nonce, again)
	}
}

func TestServeSPADirectMomentoAddsCollectorOriginAndBodyPlacement(t *testing.T) {
	writeDist(t, map[string]string{"index.html": testIndex})
	config := momentoConfig(false)
	config.Placement = "body"
	rec := servePage(t, "/dashboard", config)
	body := rec.Body.String()
	if strings.Index(body, "<script") < strings.Index(body, "</head>") || strings.Index(body, "<script") > strings.Index(body, "</body>") {
		t.Fatalf("body placement should put the snippet before </body>: %q", body)
	}
	policy := rec.Header().Get("Content-Security-Policy")
	for _, want := range []string{"script-src 'self' 'nonce-", " https://momento.corp.example; connect-src 'self' https://momento.corp.example;", "img-src 'self' data: https://momento.corp.example;"} {
		if !strings.Contains(policy, want) {
			t.Fatalf("policy %q is missing %q", policy, want)
		}
	}
}

func TestServeSPASkipsAdminPagesUnlessIncluded(t *testing.T) {
	writeDist(t, map[string]string{"index.html": testIndex})
	config := momentoConfig(true)
	rec := servePage(t, "/admin/settings", config)
	if rec.Body.String() != testIndex || rec.Header().Get("Content-Security-Policy") != contentSecurityPolicy {
		t.Fatalf("admin page must not be tracked by default: %q %q", rec.Body.String(), rec.Header().Get("Content-Security-Policy"))
	}
	config.IncludeAdmin = true
	if body := servePage(t, "/admin/settings", config).Body.String(); !strings.Contains(body, "/momento/tracker.js") {
		t.Fatalf("include_admin should track admin pages: %q", body)
	}
}

func TestInjectSnippetFallsBackToDocumentEnd(t *testing.T) {
	got := string(injectSnippet([]byte("<p>no tags</p>"), "<script>x</script>", "head"))
	if !strings.HasSuffix(strings.TrimSpace(got), "<script>x</script>") {
		t.Fatalf("got %q", got)
	}
}

func TestCSPReportIsRecordedAndListed(t *testing.T) {
	s := &Server{violations: analytics.NewRecorder()}
	body := `{"csp-report":{"blocked-uri":"https://momento.corp.example/collect/v1/events","effective-directive":"connect-src","document-uri":"https://jupiq.corp.example/dashboard"}}`
	for i := 0; i < 3; i++ {
		r := httptest.NewRequest(http.MethodPost, cspReportPath, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/csp-report")
		rec := httptest.NewRecorder()
		s.receiveCSPReport(rec, r)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("status=%d want 204", rec.Code)
		}
	}
	// 깨진 본문과 http가 아닌 출처는 204로 받되 기록하지 않는다.
	for _, junk := range []string{"", "{", `{"csp-report":{"blocked-uri":"chrome-extension://x/y.js","effective-directive":"script-src"}}`} {
		rec := httptest.NewRecorder()
		s.receiveCSPReport(rec, httptest.NewRequest(http.MethodPost, cspReportPath, strings.NewReader(junk)))
		if rec.Code != http.StatusNoContent {
			t.Fatalf("junk %q: status=%d want 204", junk, rec.Code)
		}
	}
	items := s.violations.List(analytics.Default())
	if len(items) != 1 || items[0].Origin != "https://momento.corp.example" || items[0].Directive != "connect-src" || items[0].Count != 3 || items[0].Page != "https://jupiq.corp.example/dashboard" {
		t.Fatalf("items=%+v", items)
	}
	if items[0].Allowed {
		t.Fatal("origin must not be marked allowed by a disabled configuration")
	}
	if len(s.violations.List(analytics.Config{Enabled: true, Provider: analytics.ProviderCustom, CustomSnippet: "<script src='https://momento.corp.example/t.js'></script>"})) != 1 || !s.violations.List(analytics.Config{AllowedHosts: "https://momento.corp.example"})[0].Allowed {
		t.Fatal("allowed_hosts should mark the origin as allowed")
	}

	rec := httptest.NewRecorder()
	s.analyticsViolationsClear(rec, httptest.NewRequest(http.MethodDelete, "/api/v1/analytics/violations", nil))
	if rec.Code != http.StatusNoContent || len(s.violations.List(analytics.Default())) != 0 {
		t.Fatalf("clear: status=%d remaining=%d", rec.Code, len(s.violations.List(analytics.Default())))
	}
}

func TestCSPReportWithoutRecorderStillAnswers204(t *testing.T) {
	s := &Server{}
	rec := httptest.NewRecorder()
	s.receiveCSPReport(rec, httptest.NewRequest(http.MethodPost, cspReportPath, strings.NewReader(`{}`)))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d want 204", rec.Code)
	}
}

// recordingTransport는 프록시가 수집기로 보내는 요청을 붙잡아 돌려준다.
type recordingTransport struct {
	requests []*http.Request
	bodies   []string
}

func (rt *recordingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	var body []byte
	if r.Body != nil {
		body, _ = io.ReadAll(r.Body)
	}
	rt.requests = append(rt.requests, r)
	rt.bodies = append(rt.bodies, string(body))
	header := http.Header{"Content-Type": {"application/javascript"}, "Set-Cookie": {"collector=1"}, "Cache-Control": {"public, max-age=60"}}
	return &http.Response{StatusCode: http.StatusOK, Header: header, Body: io.NopCloser(strings.NewReader("tracker-js")), Request: r}, nil
}

func proxyRequest(t *testing.T, s *Server, config analytics.Config, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, "http://jupiq.corp.example"+path, strings.NewReader(body))
	r.Header.Set("Cookie", "jupiq_session=secret")
	r.Header.Set("Authorization", "Bearer secret")
	r.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.momentoProxyWith(config, rec, r)
	return rec
}

func TestMomentoProxyForwardsLoaderAndCollectWithoutCredentials(t *testing.T) {
	transport := &recordingTransport{}
	s := &Server{proxyTransport: transport}
	config := momentoConfig(true)
	config.MomentoURL = "https://momento.corp.example/base/"

	rec := proxyRequest(t, s, config, http.MethodGet, "/momento/tracker.js", "")
	if rec.Code != http.StatusOK || rec.Body.String() != "tracker-js" {
		t.Fatalf("loader: status=%d body=%q", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Set-Cookie") != "" {
		t.Fatal("collector cookies must not reach this origin")
	}
	if rec.Header().Get("Cache-Control") != "public, max-age=60" {
		t.Fatalf("other collector headers should pass through: %v", rec.Header())
	}
	rec = proxyRequest(t, s, config, http.MethodPost, "/momento/collect/v1/events", `{"site_id":"SITE_1"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("collect: status=%d", rec.Code)
	}
	if len(transport.requests) != 2 {
		t.Fatalf("requests=%d want 2", len(transport.requests))
	}
	if got := transport.requests[0].URL.String(); got != "https://momento.corp.example/base/tracker.js" {
		t.Fatalf("loader url=%q", got)
	}
	if got := transport.requests[1].URL.String(); got != "https://momento.corp.example/base/collect/v1/events" {
		t.Fatalf("collect url=%q", got)
	}
	if transport.bodies[1] != `{"site_id":"SITE_1"}` {
		t.Fatalf("collect body=%q", transport.bodies[1])
	}
	for _, r := range transport.requests {
		if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" {
			t.Fatalf("credentials leaked to the collector: %v", r.Header)
		}
		// SetURL은 Host를 비워 두어 transport가 URL의 호스트를 Host 헤더로 쓰게 한다.
		if r.Host != "" && r.Host != "momento.corp.example" {
			t.Fatalf("Host=%q must not keep this app's host", r.Host)
		}
		if r.Header.Get("X-Forwarded-Host") != "jupiq.corp.example" {
			t.Fatalf("X-Forwarded-Host=%q", r.Header.Get("X-Forwarded-Host"))
		}
	}
}

func TestMomentoProxyIsClosedUnlessConfiguredForIt(t *testing.T) {
	transport := &recordingTransport{}
	s := &Server{proxyTransport: transport}
	closed := []analytics.Config{analytics.Default(), momentoConfig(false)}
	off := momentoConfig(true)
	off.Enabled = false
	closed = append(closed, off)
	for _, config := range closed {
		if rec := proxyRequest(t, s, config, http.MethodGet, "/momento/tracker.js", ""); rec.Code != http.StatusNotFound {
			t.Fatalf("proxy must be closed for %+v: status=%d", config, rec.Code)
		}
	}
	// 열려 있어도 로더와 수집 외의 경로·메서드는 넘기지 않는다.
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/momento/api/v1/sites"},
		{http.MethodGet, "/momento/collect/v1/events"},
		{http.MethodPost, "/momento/tracker.js"},
		{http.MethodDelete, "/momento/collect/v1/events"},
		{http.MethodGet, "/momento/"},
	} {
		if rec := proxyRequest(t, s, momentoConfig(true), tc.method, tc.path, ""); rec.Code != http.StatusNotFound {
			t.Fatalf("%s %s: status=%d want 404", tc.method, tc.path, rec.Code)
		}
	}
	if len(transport.requests) != 0 {
		t.Fatalf("nothing should have been forwarded: %d", len(transport.requests))
	}
}

func TestSettingsValidationCoversAnalytics(t *testing.T) {
	valid := map[string]any{"analytics": map[string]any{"enabled": true, "provider": "momento", "momento_url": "https://momento.corp.example", "momento_site_id": "SITE_1", "momento_proxy": true, "momento_verify_tls": true, "include_admin": false, "placement": "head", "allowed_hosts": ""}}
	if err := validateSettingsUpdate(valid, nil); err != nil {
		t.Fatalf("valid analytics settings rejected: %v", err)
	}
	if err := validateSettingsUpdate(map[string]any{"analytics": map[string]any{"enabled": false, "provider": "momento"}}, nil); err != nil {
		t.Fatalf("disabled analytics must not require provider fields: %v", err)
	}
	invalid := []struct {
		name   string
		object map[string]any
		want   string
	}{
		{"unknown field", map[string]any{"tracking_id": "x"}, "tracking_id"},
		{"bool as string", map[string]any{"enabled": "true"}, "true 또는 false"},
		{"missing site", map[string]any{"enabled": true, "provider": "momento", "momento_url": "https://m.example"}, "momento_site_id"},
		{"loopback collector", map[string]any{"enabled": true, "provider": "momento", "momento_url": "http://localhost:8090", "momento_site_id": "S"}, "loopback"},
		{"oversized snippet", map[string]any{"enabled": true, "provider": "custom", "custom_snippet": strings.Repeat("<script></script>", 600)}, "바이트"},
		{"unknown provider", map[string]any{"provider": "piwik"}, "provider"},
	}
	for _, tc := range invalid {
		err := validateSettingsUpdate(map[string]any{"analytics": tc.object}, nil)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: err=%v want %q", tc.name, err, tc.want)
		}
	}
}
