package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httputil"
	"strings"
	"time"

	"github.com/hkjang/jupiq/internal/analytics"
	"github.com/hkjang/jupiq/internal/integration"
	"github.com/hkjang/jupiq/internal/secure"
)

// cspReportPath는 브라우저가 정책이 거부한 요청을 신고하는 곳이다. 브라우저는
// 자격 증명 없이 보내므로 인증하지 않으며, 메모리의 제한된 출처 목록 외에는
// 아무것도 저장하지 않는다.
const cspReportPath = "/api/v1/analytics/csp-report"

// maxReportBytes는 인증 없는 경로로 큰 본문을 밀어 넣지 못하게 한다.
const maxReportBytes = 8 * 1024

// maxProxyBodyBytes는 프록시가 수집기로 넘기는 이벤트 묶음의 상한이다.
// 추적기는 열 개씩 묶어 보내므로 몇 KB면 충분하다.
const maxProxyBodyBytes = 256 * 1024

// momentoProxyTimeout은 수집기 응답을 기다리는 시간이다. 수집기가 죽어도
// 페이지 로딩이 이 시간 넘게 잡혀 있지 않게 한다.
const momentoProxyTimeout = 10 * time.Second

func (s *Server) registerAnalytics(mux router) {
	mux.HandleFunc("POST "+cspReportPath, s.receiveCSPReport)
	mux.HandleFunc("GET /api/v1/analytics/violations", s.require("settings:read", s.analyticsViolations))
	mux.HandleFunc("DELETE /api/v1/analytics/violations", s.require("settings:write", s.analyticsViolationsClear))
	// 같은 오리진 Momento 프록시. 추적기 로더는 GET, 수집 요청은 POST로 오며
	// 정확한 경로는 momentoProxyAllowed가 좁힌다.
	mux.HandleFunc("GET "+analytics.MomentoProxyPrefix+"/", s.momentoProxy)
	mux.HandleFunc("POST "+analytics.MomentoProxyPrefix+"/", s.momentoProxy)
}

// analyticsConfig는 추적 설정을 읽는다. 저장소 장애는 "추적 없음"으로 다루어
// 설정 문제가 페이지 응답을 막지 못하게 한다.
func (s *Server) analyticsConfig(ctx context.Context) analytics.Config {
	if s.Store == nil {
		return analytics.Default()
	}
	var raw json.RawMessage
	if err := s.Store.GetSetting(ctx, analytics.SettingKey, &raw); err != nil {
		return analytics.Default()
	}
	return analytics.ParseConfig(raw)
}

// pagePolicy는 화면 응답의 정책이다. 추적이 켜진 경로에서만 nonce와 스니펫이
// 필요로 하는 출처를 더하고, 그 밖에는 contentSecurityPolicy 그대로다.
func pagePolicy(config analytics.Config, path, nonce string) string {
	if !config.Active(path) || nonce == "" {
		return contentSecurityPolicy
	}
	extraScripts, extraConnects, extraImages := config.PolicySources()
	scripts := append([]string{"'self'", "'nonce-" + nonce + "'"}, extraScripts...)
	connects := append([]string{"'self'"}, extraConnects...)
	images := append([]string{"'self'", "data:"}, extraImages...)
	// 추적이 켜진 동안에만 브라우저에 거부한 것을 알려 달라고 한다. 그 신고가
	// 콘솔 오류를 관리 화면의 한 줄로 바꾼다.
	return "default-src 'self'; img-src " + strings.Join(images, " ") +
		"; style-src 'self' 'unsafe-inline'; script-src " + strings.Join(scripts, " ") +
		"; connect-src " + strings.Join(connects, " ") +
		"; object-src 'none'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'; report-uri " + cspReportPath
}

// injectSnippet은 마크업을 닫는 태그 바로 앞에 넣는다. 태그가 없으면 문서 끝이다.
func injectSnippet(page []byte, snippet, placement string) []byte {
	marker := "</head>"
	if placement == "body" {
		marker = "</body>"
	}
	text := string(page)
	index := strings.LastIndex(strings.ToLower(text), marker)
	if index < 0 {
		return []byte(text + "\n" + snippet + "\n")
	}
	return []byte(text[:index] + snippet + "\n" + text[index:])
}

// trackedPage는 index.html에 넣을 스니펫과 그 페이지의 정책을 만든다. 추적이
// 꺼져 있거나 이 경로가 대상이 아니면 페이지와 정책을 그대로 둔다.
func trackedPage(config analytics.Config, r *http.Request, page []byte) ([]byte, string) {
	if !config.Active(r.URL.Path) {
		return page, contentSecurityPolicy
	}
	nonce, err := secure.RandomToken(16)
	if err != nil {
		return page, contentSecurityPolicy
	}
	return injectSnippet(page, config.Snippet(nonce), config.Placement), pagePolicy(config, r.URL.Path, nonce)
}

type cspReport struct {
	Report struct {
		BlockedURI         string `json:"blocked-uri"`
		ViolatedDirective  string `json:"violated-directive"`
		EffectiveDirective string `json:"effective-directive"`
		DocumentURI        string `json:"document-uri"`
	} `json:"csp-report"`
}

// receiveCSPReport는 브라우저가 거부한 것을 적는다. 신고는 항상 204로 받아
// 문제가 있는 페이지가 우리에게서 오류를 보는 일이 없게 한다.
func (s *Server) receiveCSPReport(w http.ResponseWriter, r *http.Request) {
	defer w.WriteHeader(http.StatusNoContent)
	if s.violations == nil {
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxReportBytes))
	if err != nil || len(body) == 0 {
		return
	}
	var report cspReport
	if json.Unmarshal(body, &report) != nil {
		return
	}
	directive := report.Report.EffectiveDirective
	if directive == "" {
		directive = report.Report.ViolatedDirective
	}
	s.violations.Record(report.Report.BlockedURI, directive, report.Report.DocumentURI)
}

// analyticsViolations는 정책이 막고 있는 주소를 관리자에게 보여 준다. 브라우저
// 콘솔을 읽지 않고도 스니펫을 고칠 수 있게 하는 목록이다.
func (s *Server) analyticsViolations(w http.ResponseWriter, r *http.Request) {
	items := []analytics.Violation{}
	if s.violations != nil {
		items = s.violations.List(s.analyticsConfig(r.Context()))
	}
	data(w, http.StatusOK, map[string]any{"items": items, "total": len(items)})
}

// analyticsViolationsClear는 기록을 비운다. 관리자가 바꾼 설정이 실제로 스니펫을
// 고쳤는지 확인하는 방법이다.
func (s *Server) analyticsViolationsClear(w http.ResponseWriter, r *http.Request) {
	if s.violations != nil {
		s.violations.Forget()
	}
	w.WriteHeader(http.StatusNoContent)
}

// momentoProxyAllowed는 프록시가 넘겨 주는 두 요청이다: 추적기 로더와 이벤트
// 수집. 수집기의 다른 경로(관리 API 등)는 이 앱의 오리진으로 열지 않는다.
func momentoProxyAllowed(method, path string) bool {
	rest := strings.TrimPrefix(path, analytics.MomentoProxyPrefix)
	switch {
	case method == http.MethodGet && rest == "/tracker.js":
		return true
	case method == http.MethodPost && strings.HasPrefix(rest, "/collect/"):
		return true
	}
	return false
}

// momentoProxy는 /momento/* 를 관리자가 적은 수집기로 넘긴다. 추적이 켜져 있고
// Momento를 프록시 방식으로 붙였을 때만 열리며, 세션 쿠키는 수집기로 가지 않는다.
func (s *Server) momentoProxy(w http.ResponseWriter, r *http.Request) {
	s.momentoProxyWith(s.analyticsConfig(r.Context()), w, r)
}

func (s *Server) momentoProxyWith(config analytics.Config, w http.ResponseWriter, r *http.Request) {
	if !config.ProxyActive() || !momentoProxyAllowed(r.Method, r.URL.Path) {
		http.NotFound(w, r)
		return
	}
	target, err := integration.ValidateEndpoint(config.MomentoURL)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	target.RawQuery, target.Fragment = "", ""
	r.Body = http.MaxBytesReader(w, r.Body, maxProxyBodyBytes)
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Path = strings.TrimPrefix(pr.In.URL.Path, analytics.MomentoProxyPrefix)
			pr.Out.URL.RawPath = ""
			pr.SetURL(target)
			pr.SetXForwarded()
			// 이 앱의 자격 증명은 수집기가 알 일이 아니다.
			pr.Out.Header.Del("Cookie")
			pr.Out.Header.Del("Authorization")
		},
		ModifyResponse: func(response *http.Response) error {
			// 수집기가 쿠키를 심으면 이 앱의 오리진에 남으므로 걸러 낸다.
			response.Header.Del("Set-Cookie")
			return nil
		},
		Transport: s.momentoTransport(config.MomentoVerifyTLS),
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if s.Logger != nil {
				s.Logger.Warn("momento proxy failed", "path", r.URL.Path, "error", err.Error())
			}
			w.WriteHeader(http.StatusBadGateway)
		},
	}
	proxy.ServeHTTP(w, r)
}

// momentoTransport는 다른 연동과 같은 제한(프록시 없음·loopback 차단·시간 제한)을
// 가진 transport를 TLS 검증 여부별로 하나씩만 만들어 연결을 재사용한다.
func (s *Server) momentoTransport(verifyTLS bool) http.RoundTripper {
	if s.proxyTransport != nil {
		return s.proxyTransport
	}
	s.proxyMu.Lock()
	defer s.proxyMu.Unlock()
	if s.proxyTransports == nil {
		s.proxyTransports = map[bool]http.RoundTripper{}
	}
	if transport, ok := s.proxyTransports[verifyTLS]; ok {
		return transport
	}
	transport := integration.SafeHTTPClient(verifyTLS, momentoProxyTimeout).Transport
	s.proxyTransports[verifyTLS] = transport
	return transport
}
