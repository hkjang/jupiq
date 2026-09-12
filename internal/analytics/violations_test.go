package analytics

import (
	"fmt"
	"testing"
	"time"
)

func TestRecorderKeepsOneEntryPerOrigin(t *testing.T) {
	recorder := NewRecorder()
	for i := 0; i < 5; i++ {
		recorder.Record("https://momento.corp.example/collect/v1/events", "connect-src", "https://jupiq.corp.example/dashboard")
	}
	recorder.Record("https://momento.corp.example/tracker.js", "script-src-elem", "/")
	recorder.Record("chrome-extension://abc/inject.js", "script-src", "/")
	recorder.Record("data", "img-src", "/")
	recorder.Record("", "img-src", "/")

	items := recorder.List(Default())
	if len(items) != 2 {
		t.Fatalf("items=%d want 2 (%v)", len(items), items)
	}
	byDirective := map[string]Violation{}
	for _, item := range items {
		byDirective[item.Directive] = item
	}
	if got := byDirective["connect-src"]; got.Origin != "https://momento.corp.example" || got.Count != 5 || got.Allowed {
		t.Fatalf("connect-src=%+v", got)
	}
	if got := byDirective["script-src-elem"]; got.Origin != "https://momento.corp.example" || got.Count != 1 {
		t.Fatalf("script-src-elem=%+v", got)
	}
}

func TestRecorderNormalizesDirectiveAndDefaults(t *testing.T) {
	recorder := NewRecorder()
	recorder.Record("https://a.example/x", " Script-Src 'self' ", "/")
	recorder.Record("https://b.example/x", "", "/")
	items := recorder.List(Default())
	directives := map[string]string{}
	for _, item := range items {
		directives[item.Origin] = item.Directive
	}
	if directives["https://a.example"] != "script-src" || directives["https://b.example"] != "connect-src" {
		t.Fatalf("directives=%v", directives)
	}
}

func TestRecorderEvictsOldestBeyondLimit(t *testing.T) {
	recorder := NewRecorder()
	clock := time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)
	recorder.now = func() time.Time { clock = clock.Add(time.Second); return clock }
	for i := 0; i < MaxViolations+10; i++ {
		recorder.Record(fmt.Sprintf("https://host%03d.example/x", i), "connect-src", "/")
	}
	items := recorder.List(Default())
	if len(items) != MaxViolations {
		t.Fatalf("items=%d want %d", len(items), MaxViolations)
	}
	if items[0].Origin != fmt.Sprintf("https://host%03d.example", MaxViolations+9) {
		t.Fatalf("newest first, got %s", items[0].Origin)
	}
	for _, item := range items {
		if item.Origin == "https://host000.example" || item.Origin == "https://host009.example" {
			t.Fatalf("oldest entries must be evicted: %s", item.Origin)
		}
	}
}

func TestListMarksOriginsTheConfigurationAllows(t *testing.T) {
	recorder := NewRecorder()
	recorder.Record("https://momento.corp.example/collect", "connect-src", "/")
	recorder.Record("https://region1.google-analytics.com/g/collect", "connect-src", "/")
	recorder.Record("https://blocked.example/x", "img-src", "/")
	config := Config{Enabled: true, Provider: ProviderGA4, MeasurementID: "G-1", AllowedHosts: "https://momento.corp.example/"}
	allowed := map[string]bool{}
	for _, item := range recorder.List(config) {
		allowed[item.Origin] = item.Allowed
	}
	if !allowed["https://momento.corp.example"] || !allowed["https://region1.google-analytics.com"] || allowed["https://blocked.example"] {
		t.Fatalf("allowed=%v", allowed)
	}
	recorder.Forget()
	if len(recorder.List(config)) != 0 {
		t.Fatal("Forget should drop every entry")
	}
}

func TestAddAllowedHost(t *testing.T) {
	cases := []struct{ existing, origin, want string }{
		{"", "https://a.example/", "https://a.example"},
		{"https://a.example", "https://a.example", "https://a.example"},
		{"https://a.example, https://b.example", "HTTPS://A.EXAMPLE", "https://a.example, https://b.example"},
		{"https://a.example", "https://b.example", "https://a.example, https://b.example"},
		{"https://a.example", "   ", "https://a.example"},
	}
	for _, tc := range cases {
		if got := AddAllowedHost(tc.existing, tc.origin); got != tc.want {
			t.Fatalf("AddAllowedHost(%q, %q)=%q want %q", tc.existing, tc.origin, got, tc.want)
		}
	}
}
