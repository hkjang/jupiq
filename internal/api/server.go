package api

import (
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/hkjang/jupiq/internal/auth"
	"github.com/hkjang/jupiq/internal/store"
)

type Server struct {
	Store        *store.Store
	Auth         *auth.Service
	Logger       *slog.Logger
	loginLimiter *loginLimiter
	liveMu       sync.Mutex
	liveSnapshot []byte
	liveCachedAt time.Time
}

func New(s *store.Store, authService *auth.Service, logger *slog.Logger) *Server {
	return &Server{Store: s, Auth: authService, Logger: logger, loginLimiter: newLoginLimiter()}
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
	mux.HandleFunc("GET /", s.serveSPA)
	return s.middleware(mux)
}

func (s *Server) serveSPA(w http.ResponseWriter, r *http.Request) {
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
		http.ServeFile(w, r, requested)
		return
	}
	index := filepath.Join(root, "index.html")
	if _, err := os.Stat(index); err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	http.ServeFile(w, r, index)
}
