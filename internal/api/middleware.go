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
		setSecurityHeaders(w, r)
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

// contentSecurityPolicy는 SPA와 API 응답 모두에 적용되는 정책이다.
// default-src만으로는 막히지 않는 세 가지를 명시적으로 닫는다.
//   - frame-ancestors: X-Frame-Options를 무시하는 최신 브라우저에서도 clickjacking 차단
//   - base-uri: 주입된 <base>가 상대 경로 자산을 외부로 돌리지 못하게 차단
//   - form-action: 주입된 <form>이 세션 쿠키가 붙는 요청을 외부로 보내지 못하게 차단
//
// OIDC 로그인은 302 redirect라 form-action의 영향을 받지 않는다.
//
// 방문 추적 스니펫이 켜진 화면은 serveSPA가 요청마다 nonce를 붙인 정책으로
// 이 값을 덮어쓴다(analytics_handlers.go의 pagePolicy). 'unsafe-inline'은
// 어디에도 넣지 않는다 — 한 번 풀면 추적을 끈 뒤에도 느슨한 채 남는다.
const contentSecurityPolicy = "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self'; connect-src 'self'; object-src 'none'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'"

// apiSecurityPolicy는 화면이 아닌 응답(API·MCP·probe·프록시)의 정책이다.
// JSON에는 실행할 것이 없으므로 아무것도 허용하지 않는다.
const apiSecurityPolicy = "default-src 'none'; frame-ancestors 'none'"

// isPagePath는 SPA 셸이나 그 정적 파일을 돌려주는 경로인지 가른다.
func isPagePath(path string) bool {
	for _, prefix := range []string{"/api/", "/mcp", "/healthz", "/readyz", "/momento/"} {
		if strings.HasPrefix(path, prefix) {
			return false
		}
	}
	return true
}

// hstsMaxAge는 1년이다. jupiq는 사내 도메인의 한 호스트로 배포되는 경우가 많아
// includeSubDomains·preload는 붙이지 않는다. 같은 도메인의 다른 서비스까지
// HTTPS를 강제해 폐쇄망 운영을 막을 수 있기 때문이다.
const hstsMaxAge = "max-age=31536000"

func setSecurityHeaders(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Referrer-Policy", "same-origin")
	w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
	if isPagePath(r.URL.Path) {
		w.Header().Set("Content-Security-Policy", contentSecurityPolicy)
	} else {
		w.Header().Set("Content-Security-Policy", apiSecurityPolicy)
	}
	// HTTP로 접근하는 폐쇄망 배포에 HSTS를 남기면 이후 평문 접속이 영구히
	// 막히므로, TLS로 도달한 요청에만 붙인다.
	if auth.IsSecureRequest(r) {
		w.Header().Set("Strict-Transport-Security", hstsMaxAge)
	}
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
