package analytics

import (
	"encoding/json"
	"strings"
	"testing"
)

func momento(proxy bool) Config {
	config := Default()
	config.Enabled = true
	config.Provider = ProviderMomento
	config.MomentoURL = "https://momento.corp.example/"
	config.MomentoSiteID = "SITE_1"
	config.MomentoProxy = proxy
	return config
}

func TestDefaultIsOff(t *testing.T) {
	for _, config := range []Config{Default(), ParseConfig(nil), ParseConfig(json.RawMessage(`{`)), ReadConfig(map[string]any{"enabled": "yes"})} {
		if config.Enabled || config.Active("/dashboard") || config.ProxyActive() || config.Snippet("n") != "" {
			t.Fatalf("new installation must not track: %#v", config)
		}
		if !config.MomentoProxy || config.Placement != "head" || config.Provider != ProviderNone {
			t.Fatalf("unexpected defaults: %#v", config)
		}
	}
}

func TestParseConfigReadsStoredDocument(t *testing.T) {
	config := ParseConfig(json.RawMessage(`{"enabled":true,"provider":"Momento","momento_url":" https://momento.corp.example ","momento_site_id":"SITE_1","momento_proxy":false,"placement":"BODY","include_admin":true,"allowed_hosts":"https://a.example"}`))
	if !config.Enabled || config.Provider != ProviderMomento || config.MomentoURL != "https://momento.corp.example" || config.MomentoProxy || config.Placement != "body" || !config.IncludeAdmin {
		t.Fatalf("stored document was not read: %#v", config)
	}
	if got := ParseConfig(json.RawMessage(`{"placement":"sidebar"}`)).Placement; got != "head" {
		t.Fatalf("placement=%q want head for unknown values", got)
	}
}

func TestMomentoProxySnippetStaysSameOrigin(t *testing.T) {
	config := momento(true)
	snippet := config.Snippet("abc")
	for _, want := range []string{`src="/momento/tracker.js"`, `data-endpoint="/momento"`, `data-site-id="SITE_1"`, `nonce="abc"`, `data-environment="prd"`, `data-contract-version="1"`} {
		if !strings.Contains(snippet, want) {
			t.Fatalf("snippet %q is missing %q", snippet, want)
		}
	}
	if strings.Contains(snippet, "momento.corp.example") {
		t.Fatalf("proxied snippet must not name the collector: %q", snippet)
	}
	scripts, connects, images := config.PolicySources()
	if len(scripts)+len(connects)+len(images) != 0 {
		t.Fatalf("proxied Momento must add no origin to the policy: %v %v %v", scripts, connects, images)
	}
	if !config.ProxyActive() || !config.Active("/dashboard") {
		t.Fatal("proxy and snippet should be active")
	}
}

func TestMomentoDirectSnippetAddsCollectorOrigin(t *testing.T) {
	config := momento(false)
	snippet := config.Snippet("abc")
	if !strings.Contains(snippet, `src="https://momento.corp.example/tracker.js"`) || strings.Contains(snippet, "data-endpoint") {
		t.Fatalf("direct snippet should load from the collector: %q", snippet)
	}
	scripts, connects, images := config.PolicySources()
	for _, group := range [][]string{scripts, connects, images} {
		if len(group) != 1 || group[0] != "https://momento.corp.example" {
			t.Fatalf("policy sources=%v %v %v want the collector origin", scripts, connects, images)
		}
	}
	if config.ProxyActive() {
		t.Fatal("direct mode must not open the proxy")
	}
}

func TestAdminPagesExcludedUnlessRequested(t *testing.T) {
	config := momento(true)
	if config.Active("/admin/settings") {
		t.Fatal("admin pages must be excluded by default")
	}
	config.IncludeAdmin = true
	if !config.Active("/admin/settings") {
		t.Fatal("include_admin should include admin pages")
	}
	config.Enabled = false
	if config.Active("/dashboard") || config.ProxyActive() {
		t.Fatal("disabled configuration must not be active")
	}
}

func TestValidateRequiresProviderFields(t *testing.T) {
	cases := []struct {
		name   string
		config Config
		want   string
	}{
		{"momento without site", Config{Enabled: true, Provider: ProviderMomento, MomentoURL: "https://m.example"}, "momento_site_id"},
		{"momento bad url", Config{Enabled: true, Provider: ProviderMomento, MomentoURL: "ftp://m.example", MomentoSiteID: "S"}, "올바른 주소"},
		{"ga4 without id", Config{Enabled: true, Provider: ProviderGA4}, "measurement_id"},
		{"matomo without site", Config{Enabled: true, Provider: ProviderMatomo, MatomoURL: "https://m.example"}, "matomo_site_id"},
		{"custom empty", Config{Enabled: true, Provider: ProviderCustom}, "custom_snippet"},
		{"unknown provider", Config{Provider: "piwik"}, "provider"},
		{"bad placement", Config{Placement: "footer"}, "placement"},
		{"oversized snippet", Config{Provider: ProviderCustom, CustomSnippet: strings.Repeat("x", MaxSnippetBytes+1)}, "바이트"},
	}
	for _, tc := range cases {
		err := tc.config.Validate()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: err=%v want %q", tc.name, err, tc.want)
		}
	}
	if err := (Config{Provider: ProviderMomento}).Validate(); err != nil {
		t.Fatalf("disabled configuration must not require fields: %v", err)
	}
	if err := momento(true).Validate(); err != nil {
		t.Fatalf("complete Momento configuration rejected: %v", err)
	}
}

func TestWithNonceTagsEveryScript(t *testing.T) {
	config := Default()
	config.Enabled = true
	config.Provider = ProviderCustom
	config.CustomSnippet = `<SCRIPT src="https://t.example/a.js"></SCRIPT>
<script nonce="keep">x()</script>
<script>y()</script>`
	snippet := config.Snippet("n1")
	if got := strings.Count(snippet, `nonce="n1"`); got != 2 {
		t.Fatalf("nonce count=%d want 2 in %q", got, snippet)
	}
	if !strings.Contains(snippet, `nonce="keep"`) {
		t.Fatalf("existing nonce was replaced: %q", snippet)
	}
	if got := config.Snippet(""); got != strings.TrimSpace(config.CustomSnippet) {
		t.Fatalf("empty nonce must leave the snippet alone: %q", got)
	}
}

func TestSnippetOriginsAreReadFromCustomCode(t *testing.T) {
	snippet := `<script src="https://cdn.example/t.js"></script>
<script>window.__t={endpoint:"https://collect.example/v1/events",pixel:'HTTP://pixel.example/p.gif?id=1'};fetch("https://cdn.example/x");</script>
<img src="data:image/gif;base64,R0lGOD">`
	origins := SnippetOrigins(snippet)
	want := []string{"https://cdn.example", "https://collect.example", "http://pixel.example"}
	if strings.Join(origins, " ") != strings.Join(want, " ") {
		t.Fatalf("origins=%v want %v", origins, want)
	}
	config := Config{Enabled: true, Provider: ProviderCustom, CustomSnippet: snippet, AllowedHosts: "https://extra.example, https://cdn.example\nhttps://more.example"}
	scripts, _, _ := config.PolicySources()
	if strings.Join(scripts, " ") != "https://cdn.example https://collect.example http://pixel.example https://extra.example https://cdn.example https://more.example" {
		t.Fatalf("scripts=%v", scripts)
	}
}

func TestGA4AndMatomoSources(t *testing.T) {
	ga := Config{Enabled: true, Provider: ProviderGA4, MeasurementID: "G-1"}
	scripts, connects, _ := ga.PolicySources()
	if scripts[0] != "https://www.googletagmanager.com" || len(connects) != 3 {
		t.Fatalf("ga4 sources=%v %v", scripts, connects)
	}
	if !strings.Contains(ga.Snippet("n"), `gtag('config','G-1')`) {
		t.Fatalf("ga4 snippet=%q", ga.Snippet("n"))
	}
	matomo := Config{Enabled: true, Provider: ProviderMatomo, MatomoURL: "https://matomo.corp.example/analytics/", MatomoSiteID: "7"}
	scripts, _, _ = matomo.PolicySources()
	if len(scripts) != 1 || scripts[0] != "https://matomo.corp.example" {
		t.Fatalf("matomo sources=%v", scripts)
	}
	if !strings.Contains(matomo.Snippet("n"), `var u="https://matomo.corp.example/analytics/";`) {
		t.Fatalf("matomo snippet=%q", matomo.Snippet("n"))
	}
}
