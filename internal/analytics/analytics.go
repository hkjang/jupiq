// Package analytics는 관리자가 화면에서 붙이는 방문 추적 스니펫을 다룬다.
//
// jupiq의 콘텐츠 보안 정책은 script-src 'self'로 잠겨 있어 스니펫을 그냥
// 넣으면 브라우저가 조용히 차단한다. 이 패키지는 그 문제의 두 반쪽을 함께
// 만든다: 페이지에 넣을 마크업과, 그 마크업이 정책에서 필요로 하는 출처.
// 인라인 코드는 요청마다 다른 nonce로 허용하므로 'unsafe-inline'을 쓰지 않는다.
package analytics

import (
	"encoding/json"
	"fmt"
	"html"
	"net/url"
	"strings"
)

const (
	ProviderNone    = "none"
	ProviderMomento = "momento"
	ProviderGA4     = "ga4"
	ProviderGTM     = "gtm"
	ProviderMatomo  = "matomo"
	ProviderCustom  = "custom"

	// MaxSnippetBytes는 붙여넣은 스니펫의 상한이다. 추적 로더는 몇백 바이트면
	// 충분하고, 그보다 큰 것은 스니펫이 아니라 애플리케이션 코드다.
	MaxSnippetBytes = 8 * 1024

	// MomentoProxyPrefix는 같은 오리진 프록시가 받는 경로다. 스니펫이 이 경로로
	// 로더와 수집 요청을 보내면 외부 출처가 정책에 등장하지 않는다.
	MomentoProxyPrefix = "/momento"

	// SettingKey는 settings 테이블에서 이 설정이 저장되는 문서 키다.
	SettingKey = "analytics"
)

// Providers는 화면과 검증이 공유하는 순서다. Momento가 첫 자리인 이유는 사내
// 자체 호스팅 수집기라 데이터가 밖으로 나가지 않는 유일한 선택지이기 때문이다.
var Providers = []string{ProviderNone, ProviderMomento, ProviderGA4, ProviderGTM, ProviderMatomo, ProviderCustom}

type Config struct {
	Enabled          bool   `json:"enabled"`
	Provider         string `json:"provider"`
	MomentoURL       string `json:"momento_url,omitempty"`
	MomentoSiteID    string `json:"momento_site_id,omitempty"`
	MomentoProxy     bool   `json:"momento_proxy"`
	MomentoVerifyTLS bool   `json:"momento_verify_tls"`
	MeasurementID    string `json:"measurement_id,omitempty"`
	MatomoURL        string `json:"matomo_url,omitempty"`
	MatomoSiteID     string `json:"matomo_site_id,omitempty"`
	CustomSnippet    string `json:"custom_snippet,omitempty"`
	AllowedHosts     string `json:"allowed_hosts,omitempty"`
	IncludeAdmin     bool   `json:"include_admin"`
	Placement        string `json:"placement"`
}

// Default는 새로 설치한 곳의 상태다. 꺼져 있고, 켜더라도 Momento를 같은
// 오리진 프록시로 붙이는 쪽이 기본이다.
func Default() Config {
	return Config{Provider: ProviderNone, MomentoProxy: true, MomentoVerifyTLS: true, Placement: "head"}
}

// ReadConfig는 저장된 설정 문서를 Config로 옮긴다. 없는 항목은 기본값을 따르고
// 형식이 다른 항목은 무시하므로, 손상된 문서가 페이지 응답을 막지 못한다.
func ReadConfig(values map[string]any) Config {
	config := Default()
	if values == nil {
		return config
	}
	config.Enabled = boolValue(values, "enabled", false)
	config.Provider = strings.ToLower(stringValue(values, "provider", ProviderNone))
	config.MomentoURL = stringValue(values, "momento_url", "")
	config.MomentoSiteID = stringValue(values, "momento_site_id", "")
	config.MomentoProxy = boolValue(values, "momento_proxy", true)
	config.MomentoVerifyTLS = boolValue(values, "momento_verify_tls", true)
	config.MeasurementID = stringValue(values, "measurement_id", "")
	config.MatomoURL = stringValue(values, "matomo_url", "")
	config.MatomoSiteID = stringValue(values, "matomo_site_id", "")
	config.CustomSnippet = stringValue(values, "custom_snippet", "")
	config.AllowedHosts = stringValue(values, "allowed_hosts", "")
	config.IncludeAdmin = boolValue(values, "include_admin", false)
	config.Placement = strings.ToLower(stringValue(values, "placement", "head"))
	if config.Placement != "body" {
		config.Placement = "head"
	}
	return config
}

// ParseConfig는 settings 테이블의 JSON 문서를 읽는다. 문서가 없거나 깨져 있으면
// 기본값(꺼짐)이다.
func ParseConfig(raw json.RawMessage) Config {
	if len(raw) == 0 {
		return Default()
	}
	var values map[string]any
	if err := json.Unmarshal(raw, &values); err != nil {
		return Default()
	}
	return ReadConfig(values)
}

// Active는 이 경로의 페이지에 스니펫을 붙일지 정한다. 관리 화면은 관리자가
// 따로 켜지 않는 한 제외한다 — 운영 콘솔의 트래픽은 누구도 원하는 방문 자료가
// 아니다.
func (c Config) Active(path string) bool {
	if !c.Enabled || c.Provider == ProviderNone || c.Provider == "" {
		return false
	}
	if !c.IncludeAdmin && strings.HasPrefix(path, "/admin") {
		return false
	}
	return strings.TrimSpace(c.Snippet("")) != ""
}

// ProxyActive는 같은 오리진 Momento 프록시를 열어 둘지 정한다. 추적이 켜져
// 있고 provider가 Momento이며 프록시 방식을 골랐을 때만이다.
func (c Config) ProxyActive() bool {
	return c.Enabled && c.Provider == ProviderMomento && c.MomentoProxy && strings.TrimSpace(c.MomentoURL) != ""
}

// Validate는 고른 provider에 빠진 것이 무엇인지 알려 준다. 꺼져 있으면
// 어떤 값도 요구하지 않는다 — 나중에 켤 때 채우면 된다.
func (c Config) Validate() error {
	if c.Placement != "" && c.Placement != "head" && c.Placement != "body" {
		return fmt.Errorf("analytics.placement는 head 또는 body여야 합니다")
	}
	if !contains(Providers, c.Provider) && c.Provider != "" {
		return fmt.Errorf("analytics.provider는 %s 중 하나여야 합니다", strings.Join(Providers, ", "))
	}
	if len(c.CustomSnippet) > MaxSnippetBytes {
		return fmt.Errorf("추적 코드는 %d바이트를 넘을 수 없습니다", MaxSnippetBytes)
	}
	if !c.Enabled {
		return nil
	}
	switch c.Provider {
	case ProviderNone, "":
		return nil
	case ProviderMomento:
		if strings.TrimSpace(c.MomentoURL) == "" || strings.TrimSpace(c.MomentoSiteID) == "" {
			return fmt.Errorf("Momento 사용 시 momento_url과 momento_site_id가 필요합니다")
		}
		if originOf(c.MomentoURL) == "" {
			return fmt.Errorf("analytics.momento_url이 올바른 주소가 아닙니다")
		}
	case ProviderGA4, ProviderGTM:
		if strings.TrimSpace(c.MeasurementID) == "" {
			return fmt.Errorf("%s 사용 시 measurement_id가 필요합니다", strings.ToUpper(c.Provider))
		}
	case ProviderMatomo:
		if strings.TrimSpace(c.MatomoURL) == "" || strings.TrimSpace(c.MatomoSiteID) == "" {
			return fmt.Errorf("Matomo 사용 시 matomo_url과 matomo_site_id가 필요합니다")
		}
		if originOf(c.MatomoURL) == "" {
			return fmt.Errorf("analytics.matomo_url이 올바른 주소가 아닙니다")
		}
	case ProviderCustom:
		if strings.TrimSpace(c.CustomSnippet) == "" {
			return fmt.Errorf("custom 사용 시 custom_snippet이 비어 있을 수 없습니다")
		}
	}
	return nil
}

// Snippet은 페이지에 넣을 마크업이다. 스니펫의 모든 <script> 태그에 nonce를
// 붙이므로 정책은 그대로 엄격하게 남는다.
func (c Config) Snippet(nonce string) string {
	switch c.Provider {
	case ProviderMomento:
		site := html.EscapeString(strings.TrimSpace(c.MomentoSiteID))
		base := strings.TrimRight(strings.TrimSpace(c.MomentoURL), "/")
		if site == "" || base == "" {
			return ""
		}
		if c.MomentoProxy {
			// 로더와 수집 요청이 모두 같은 오리진의 프록시 경로로 나간다.
			return withNonce(fmt.Sprintf(`<script async src="%s/tracker.js" data-site-id="%s" data-endpoint="%s" data-environment="prd" data-contract-version="1"></script>`, MomentoProxyPrefix, site, MomentoProxyPrefix), nonce)
		}
		return withNonce(fmt.Sprintf(`<script async src="%s/tracker.js" data-site-id="%s" data-environment="prd" data-contract-version="1"></script>`, html.EscapeString(base), site), nonce)
	case ProviderGA4:
		id := html.EscapeString(strings.TrimSpace(c.MeasurementID))
		if id == "" {
			return ""
		}
		return withNonce(fmt.Sprintf(`<script async src="https://www.googletagmanager.com/gtag/js?id=%s"></script>
<script>window.dataLayer=window.dataLayer||[];function gtag(){dataLayer.push(arguments);}gtag('js',new Date());gtag('config','%s');</script>`, id, id), nonce)
	case ProviderGTM:
		id := html.EscapeString(strings.TrimSpace(c.MeasurementID))
		if id == "" {
			return ""
		}
		return withNonce(fmt.Sprintf(`<script>(function(w,d,s,l,i){w[l]=w[l]||[];w[l].push({'gtm.start':new Date().getTime(),event:'gtm.js'});var f=d.getElementsByTagName(s)[0],j=d.createElement(s),dl=l!='dataLayer'?'&l='+l:'';j.async=true;j.src='https://www.googletagmanager.com/gtm.js?id='+i+dl;f.parentNode.insertBefore(j,f);})(window,document,'script','dataLayer','%s');</script>`, id), nonce)
	case ProviderMatomo:
		base := strings.TrimRight(strings.TrimSpace(c.MatomoURL), "/")
		site := html.EscapeString(strings.TrimSpace(c.MatomoSiteID))
		if base == "" || site == "" {
			return ""
		}
		return withNonce(fmt.Sprintf(`<script>var _paq=window._paq=window._paq||[];_paq.push(['trackPageView']);_paq.push(['enableLinkTracking']);(function(){var u="%s/";_paq.push(['setTrackerUrl',u+'matomo.php']);_paq.push(['setSiteId','%s']);var d=document,g=d.createElement('script'),s=d.getElementsByTagName('script')[0];g.async=true;g.src=u+'matomo.js';s.parentNode.insertBefore(g,s);})();</script>`, html.EscapeString(base), site), nonce)
	case ProviderCustom:
		return withNonce(strings.TrimSpace(c.CustomSnippet), nonce)
	}
	return ""
}

// withNonce는 아직 nonce가 없는 모든 <script> 태그에 nonce를 단다. 붙여넣은
// 스니펫을 고치지 않고도 엄격한 정책 아래에서 돌게 하는 것이 이 함수다.
func withNonce(snippet, nonce string) string {
	if nonce == "" || snippet == "" {
		return snippet
	}
	var builder strings.Builder
	remaining := snippet
	for {
		index := strings.Index(strings.ToLower(remaining), "<script")
		if index < 0 {
			builder.WriteString(remaining)
			return builder.String()
		}
		end := index + len("<script")
		builder.WriteString(remaining[:end])
		tag := remaining[end:]
		if closing := strings.Index(tag, ">"); closing >= 0 {
			tag = tag[:closing]
		}
		if !strings.Contains(strings.ToLower(tag), "nonce=") {
			builder.WriteString(` nonce="` + html.EscapeString(nonce) + `"`)
		}
		remaining = remaining[end:]
	}
}

// PolicySources는 스니펫이 정책에서 필요로 하는 추가 출처다. provider에서 바로
// 알 수 있는 것은 관리자가 정책을 몰라도 되게 여기서 채운다.
func (c Config) PolicySources() (scripts []string, connects []string, images []string) {
	add := func(origin string) {
		scripts = append(scripts, origin)
		connects = append(connects, origin)
		images = append(images, origin)
	}
	switch c.Provider {
	case ProviderMomento:
		// 프록시 방식이면 모든 요청이 같은 오리진이라 더할 출처가 없다.
		if !c.MomentoProxy {
			if origin := originOf(c.MomentoURL); origin != "" {
				add(origin)
			}
		}
	case ProviderGA4, ProviderGTM:
		scripts = append(scripts, "https://www.googletagmanager.com")
		connects = append(connects, "https://www.google-analytics.com", "https://analytics.google.com", "https://*.google-analytics.com")
		images = append(images, "https://www.google-analytics.com", "https://www.googletagmanager.com")
	case ProviderMatomo:
		if origin := originOf(c.MatomoURL); origin != "" {
			add(origin)
		}
	case ProviderCustom:
		// 붙여넣은 스니펫은 자기가 읽고 보내는 주소를 안에 적어 두므로, 정책
		// 오류를 먼저 읽지 않아도 그 출처를 허용할 수 있다.
		for _, origin := range SnippetOrigins(c.CustomSnippet) {
			add(origin)
		}
	}
	for _, host := range SplitHosts(c.AllowedHosts) {
		add(host)
	}
	return scripts, connects, images
}

// SplitHosts는 쉼표·공백·줄바꿈으로 나뉜 허용 목록을 항목으로 나눈다.
func SplitHosts(list string) []string {
	hosts := make([]string, 0, 2)
	for _, host := range strings.FieldsFunc(list, func(letter rune) bool {
		return letter == ',' || letter == ' ' || letter == '\n' || letter == '\r' || letter == '\t'
	}) {
		if trimmed := strings.TrimSpace(host); trimmed != "" {
			hosts = append(hosts, trimmed)
		}
	}
	return hosts
}

// SnippetOrigins는 스니펫 문자열에 적힌 모든 http(s) 출처다: 읽어 오는 스크립트,
// 보내는 endpoint, 요청하는 픽셀. 추적 도구는 거의 항상 자기 주소를 로더 안에
// 적어 두므로, 여기서 읽어 두면 관리자가 브라우저 콘솔의 정책 오류를 호스트
// 이름으로 번역할 필요가 없다.
func SnippetOrigins(snippet string) []string {
	origins := make([]string, 0, 2)
	seen := make(map[string]struct{}, 2)
	lower := strings.ToLower(snippet)
	for index := 0; index < len(snippet); {
		start := strings.Index(lower[index:], "http")
		if start < 0 {
			break
		}
		start += index
		end := start
		for end < len(snippet) && !isURLBoundary(snippet[end]) {
			end++
		}
		index = end
		origin := originOf(snippet[start:end])
		if origin == "" {
			continue
		}
		if _, duplicate := seen[origin]; duplicate {
			continue
		}
		seen[origin] = struct{}{}
		origins = append(origins, origin)
	}
	return origins
}

// isURLBoundary는 HTML이나 JavaScript 안에 적힌 URL에 들어갈 수 없는 글자다.
// 주소는 거기서 끝난다.
func isURLBoundary(letter byte) bool {
	switch letter {
	case '"', '\'', '`', '<', '>', ' ', '\t', '\n', '\r', ')', ',', ';', '\\', '+':
		return true
	}
	return false
}

// originOf는 주소의 scheme://host 부분이다. http(s)가 아니면 빈 문자열이다.
func originOf(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" {
		return ""
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return ""
	}
	return scheme + "://" + strings.ToLower(parsed.Host)
}

func stringValue(values map[string]any, key, fallback string) string {
	if value, ok := values[key].(string); ok && strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	return fallback
}

func boolValue(values map[string]any, key string, fallback bool) bool {
	if value, ok := values[key].(bool); ok {
		return value
	}
	return fallback
}

func contains(items []string, item string) bool {
	for _, candidate := range items {
		if candidate == item {
			return true
		}
	}
	return false
}
