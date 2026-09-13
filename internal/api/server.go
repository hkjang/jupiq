package api

import (
	"bytes"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/hkjang/jupiq/internal/analytics"
	"github.com/hkjang/jupiq/internal/auth"
	"github.com/hkjang/jupiq/internal/store"
)

type Server struct {
	Store        *store.Store
	Auth         *auth.Service
	Logger       *slog.Logger
	loginLimiter *loginLimiter
	// oidcStartLimiter는 익명으로 부를 수 있는 OIDC 로그인 시작을 IP별로 제한한다.
	oidcStartLimiter *requestLimiter
	// violations는 방문 추적 스니펫이 켜진 동안 브라우저가 신고한 CSP 차단 출처다.
	violations *analytics.Recorder
	// proxyTransport가 있으면 Momento 프록시가 그것만 쓴다(테스트용). 없으면
	// proxyTransports에 TLS 검증 여부별로 하나씩 만들어 둔다.
	proxyTransport  http.RoundTripper
	proxyMu         sync.Mutex
	proxyTransports map[bool]http.RoundTripper
	liveMu          sync.Mutex
	liveSnapshot    []byte
	liveCachedAt    time.Time
}

func New(s *store.Store, authService *auth.Service, logger *slog.Logger) *Server {
	return &Server{Store: s, Auth: authService, Logger: logger, loginLimiter: newLoginLimiter(), oidcStartLimiter: newRequestLimiter(oidcStartWindow, oidcStartIPLimit), violations: analytics.NewRecorder()}
}

// router is the subset of *http.ServeMux the register 함수들이 사용하는 부분이다.
// 테스트가 등록 경로를 수집해 OpenAPI 문서와 대조할 수 있게 한다.
type router interface {
	HandleFunc(pattern string, handler func(http.ResponseWriter, *http.Request))
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	s.registerPublic(mux)
	s.registerAuth(mux)
	s.registerCore(mux)
	s.registerHubs(mux)
	s.registerResources(mux)
	s.registerAI(mux)
	s.registerMCP(mux)
	s.registerAnalytics(mux)
	mux.HandleFunc("GET /", s.serveSPA)
	return s.middleware(mux)
}

func (s *Server) serveSPA(w http.ResponseWriter, r *http.Request) {
	s.serveSPAWith(w, r, s.analyticsConfig(r.Context()))
}

// serveSPAWith는 추적 설정을 바깥에서 받는다. 설정 저장소 없이 스니펫 주입을
// 검증하는 테스트를 위한 분리다.
func (s *Server) serveSPAWith(w http.ResponseWriter, r *http.Request, config analytics.Config) {
	if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/mcp") {
		apiError(w, r, http.StatusNotFound, "not_found", "API 경로를 찾을 수 없습니다")
		return
	}
	root := filepath.Clean("web/dist")
	requested := filepath.Join(root, filepath.Clean("/"+r.URL.Path))
	if !strings.HasPrefix(requested, root) {
		http.NotFound(w, r)
		return
	}
	if info, err := os.Stat(requested); err == nil && !info.IsDir() {
		w.Header().Set("Cache-Control", staticCacheControl(r.URL.Path))
		http.ServeFile(w, r, requested)
		return
	}
	index := filepath.Join(root, "index.html")
	page, err := os.ReadFile(index)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	// 방문 추적이 켜져 있으면 스니펫과 그 nonce를 허용하는 정책이 함께 나간다.
	// 파일은 작고 no-store로 응답하므로 요청마다 읽어 다시 쓴다.
	page, policy := trackedPage(config, r, page)
	w.Header().Set("Content-Security-Policy", policy)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(page))
}

// staticCacheControl은 SPA 정적 파일의 캐시 정책을 정한다.
// Vite는 번들 산출물을 전부 content hash가 붙은 이름으로 assets/ 아래에 두므로
// 내용이 바뀌면 URL도 바뀐다. 따라서 그 파일들만 장기 immutable 캐시가 안전하다.
// public/에서 그대로 복사되는 favicon 같은 파일은 이름이 고정이라 릴리스마다
// 내용이 바뀔 수 있으므로, 브라우저 heuristic 캐시로 오래된 파일이 남지 않도록
// 매번 재검증(If-Modified-Since)하게 한다.
func staticCacheControl(urlPath string) string {
	if strings.HasPrefix(urlPath, "/assets/") {
		return "public, max-age=31536000, immutable"
	}
	return "public, max-age=0, must-revalidate"
}
