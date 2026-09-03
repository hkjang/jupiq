package api

import (
	"log/slog"
	"net/http"
	"net/url"
	"runtime/debug"
	"strings"
	"time"

	"github.com/hkjang/jupiq/internal/auth"
	"github.com/hkjang/jupiq/internal/secure"
	"github.com/hkjang/jupiq/internal/store"
)

func (s *Server) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		requestID := strings.TrimSpace(r.Header.Get("X-Request-ID"))
		if requestID == "" {
			requestID, _ = secure.RandomToken(12)
			r.Header.Set("X-Request-ID", requestID)
		}
		w.Header().Set("X-Request-ID", requestID)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self'; connect-src 'self'")
		defer func() {
			if recovered := recover(); recovered != nil {
				s.Logger.Error("panic in HTTP handler", slog.Any("panic", recovered), slog.String("stack", string(debug.Stack())), slog.String("request_id", requestID))
				apiError(w, r, http.StatusInternalServerError, "internal_error", "요청을 처리하지 못했습니다")
			}
			s.Logger.Info("http request", "method", r.Method, "path", r.URL.Path, "duration_ms", time.Since(started).Milliseconds(), "request_id", requestID)
		}()
		if isMutation(r.Method) && !sameOrigin(r) {
			apiError(w, r, http.StatusForbidden, "origin_denied", "요청 출처를 확인할 수 없습니다")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func scopedAccess(w http.ResponseWriter, r *http.Request, permission string) (store.AccessFilter, bool) {
	access := principal(r).AccessFilter(permission)
	if !access.Global && len(access.Groups) == 0 {
		apiError(w, r, http.StatusForbidden, "forbidden", "이 작업을 수행할 권한이 없습니다")
		return store.AccessFilter{}, false
	}
	return access, true
}

func scopedTargetAllowed(w http.ResponseWriter, r *http.Request, permission string, hubID int64, department string) bool {
	if principal(r).AllowsTarget(permission, hubID, department) {
		return true
	}
	apiError(w, r, http.StatusForbidden, "forbidden", "이 대상에 대한 권한이 없습니다")
	return false
}

func (s *Server) require(permission string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, err := s.Auth.AuthenticateRequest(r.Context(), r)
		if err != nil {
			apiError(w, r, http.StatusUnauthorized, "unauthorized", err.Error())
			return
		}
		if permission != "" && !p.Allows(permission) {
			apiError(w, r, http.StatusForbidden, "forbidden", "이 작업을 수행할 권한이 없습니다")
			return
		}
		next(w, withPrincipal(r, p))
	}
}

func isMutation(method string) bool {
	return method == http.MethodPost || method == http.MethodPut || method == http.MethodPatch || method == http.MethodDelete
}

func sameOrigin(r *http.Request) bool {
	// Modern browsers send Fetch Metadata even when Origin is omitted.  Reject
	// cross-site browser mutations while keeping non-browser API clients usable.
	if site := strings.ToLower(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site"))); site == "cross-site" {
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	if !strings.EqualFold(u.Host, r.Host) {
		return false
	}
	wantScheme := "http"
	if auth.IsSecureRequest(r) {
		wantScheme = "https"
	}
	return strings.EqualFold(u.Scheme, wantScheme)
}
