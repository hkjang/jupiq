package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hkjang/jupiq/internal/auth"
	"github.com/hkjang/jupiq/internal/store"
)

func (s *Server) registerPublic(mux *http.ServeMux) {
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { data(w, http.StatusOK, map[string]any{"status": "ok"}) })
	mux.HandleFunc("GET /readyz", s.ready)
	mux.HandleFunc("GET /api/v1/version", s.version)
	mux.HandleFunc("GET /api/v1/openapi.yaml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
		http.ServeFile(w, r, "openapi/openapi.yaml")
	})
	mux.HandleFunc("POST /api/v1/auth/login", s.login)
	mux.HandleFunc("GET /api/v1/auth/oidc/config", s.oidcConfig)
	mux.HandleFunc("GET /api/v1/auth/oidc/login", s.oidcLogin)
	mux.HandleFunc("GET /api/v1/auth/oidc/callback", s.oidcCallback)
}

func (s *Server) registerAuth(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/auth/me", s.require("", s.me))
	mux.HandleFunc("PATCH /api/v1/auth/me", s.require("", s.meUpdate))
	mux.HandleFunc("POST /api/v1/auth/logout", s.require("", s.logout))
	mux.HandleFunc("POST /api/v1/auth/password", s.require("", s.changePassword))
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 2*time.Second)
	defer cancel()
	if err := s.Store.Ping(ctx); err != nil {
		apiError(w, r, http.StatusServiceUnavailable, "not_ready", "데이터베이스 연결을 확인할 수 없습니다")
		return
	}
	data(w, http.StatusOK, map[string]any{"status": "ready"})
}

func (s *Server) version(w http.ResponseWriter, _ *http.Request) {
	data(w, http.StatusOK, s.versionInfo())
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &input); err != nil || strings.TrimSpace(input.Username) == "" || input.Password == "" {
		apiError(w, r, http.StatusBadRequest, "invalid_request", "아이디와 비밀번호를 입력하세요")
		return
	}
	ip := clientIP(r)
	if !s.loginLimiter.allow(ip, input.Username) {
		w.Header().Set("Retry-After", strconv.FormatInt(int64((10*time.Minute)/time.Second), 10))
		apiError(w, r, http.StatusTooManyRequests, "login_rate_limited", "로그인 시도가 너무 많습니다. 잠시 후 다시 시도하세요")
		return
	}
	p, token, expires, err := s.Auth.Login(r.Context(), input.Username, input.Password, ip, r.UserAgent())
	if err != nil {
		s.loginLimiter.failed(ip, input.Username)
		_ = s.Store.RecordAudit(r.Context(), store.AuditEvent{ActorUsername: input.Username, Action: "auth.login", ResourceType: "session", IPAddress: clientIP(r), UserAgent: r.UserAgent(), Result: "failure", Reason: "invalid_credentials", RequestID: requestID(r)})
		apiError(w, r, http.StatusUnauthorized, "invalid_credentials", err.Error())
		return
	}
	s.loginLimiter.succeeded(ip, input.Username)
	http.SetCookie(w, auth.SecureCookie(token, expires, auth.IsSecureRequest(r)))
	_ = s.Store.RecordAudit(r.Context(), store.AuditEvent{ActorUserID: &p.User.ID, ActorUsername: p.User.Username, Action: "auth.login", ResourceType: "session", ResourceID: p.JTI, IPAddress: clientIP(r), UserAgent: r.UserAgent(), Result: "success", RequestID: requestID(r)})
	data(w, http.StatusOK, map[string]any{"user": p.User, "expires_at": expires})
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	data(w, http.StatusOK, map[string]any{"user": p.User, "api_key_authenticated": p.APIKeyID != 0})
}

func (s *Server) meUpdate(w http.ResponseWriter, r *http.Request) {
	var input struct {
		DisplayName string `json:"display_name"`
		Email       string `json:"email"`
		Department  string `json:"department"`
	}
	if err := decodeJSON(r, &input); err != nil {
		apiError(w, r, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	p := principal(r)
	user, err := s.Store.UpdateProfile(r.Context(), p.User.ID, input.DisplayName, input.Email, input.Department)
	if err != nil {
		handleStoreError(w, r, err)
		return
	}
	_ = s.Store.RecordAudit(r.Context(), requestAudit(r, "profile.update", "user", strconv.FormatInt(user.ID, 10), "success", "", nil, map[string]any{"display_name": user.DisplayName, "email": user.Email, "department": user.Department}))
	data(w, http.StatusOK, map[string]any{"user": user})
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	var input struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := decodeJSON(r, &input); err != nil || input.CurrentPassword == "" || input.NewPassword == "" {
		apiError(w, r, http.StatusBadRequest, "invalid_password_request", "현재 비밀번호와 새 비밀번호를 입력하세요")
		return
	}
	p := principal(r)
	if err := s.Store.ChangeLocalPassword(r.Context(), p.User.ID, input.CurrentPassword, input.NewPassword); err != nil {
		switch {
		case errors.Is(err, store.ErrInvalidPassword):
			apiError(w, r, http.StatusBadRequest, "current_password_invalid", "현재 비밀번호가 올바르지 않습니다")
		case errors.Is(err, store.ErrLocalAuthOnly):
			apiError(w, r, http.StatusBadRequest, "local_auth_only", "OIDC 계정 비밀번호는 Keycloak에서 변경하세요")
		default:
			apiError(w, r, http.StatusBadRequest, "password_policy_denied", err.Error())
		}
		return
	}
	if err := s.Store.RevokeOtherSessions(r.Context(), p.User.ID, p.JTI); err != nil {
		handleStoreError(w, r, err)
		return
	}
	_ = s.Store.RecordAudit(r.Context(), requestAudit(r, "auth.password.change", "user", strconv.FormatInt(p.User.ID, 10), "success", "", nil, map[string]any{"other_sessions_revoked": true}))
	data(w, http.StatusOK, map[string]any{"changed": true, "other_sessions_revoked": true})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	if err := s.Auth.Logout(r.Context(), p); err != nil {
		handleStoreError(w, r, err)
		return
	}
	http.SetCookie(w, auth.ClearCookie(auth.IsSecureRequest(r)))
	_ = s.Store.RecordAudit(r.Context(), requestAudit(r, "auth.logout", "session", p.JTI, "success", "", nil, nil))
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) oidcConfig(w http.ResponseWriter, r *http.Request) {
	cfg, secretConfigured, err := s.Auth.OIDCConfig(r.Context())
	if err != nil {
		handleStoreError(w, r, err)
		return
	}
	data(w, http.StatusOK, map[string]any{"enabled": cfg.Enabled, "issuer_url": cfg.IssuerURL, "client_id": cfg.ClientID, "secret_configured": secretConfigured})
}

func (s *Server) oidcLogin(w http.ResponseWriter, r *http.Request) {
	redirect := absoluteURL(r, "/api/v1/auth/oidc/callback")
	authURL, stateCookie, expires, err := s.Auth.OIDCLogin(r.Context(), redirect)
	if err != nil {
		apiError(w, r, http.StatusServiceUnavailable, "oidc_unavailable", err.Error())
		return
	}
	http.SetCookie(w, &http.Cookie{Name: auth.OIDCStateCookie, Value: stateCookie, Path: "/api/v1/auth/oidc/callback", HttpOnly: true, Secure: auth.IsSecureRequest(r), SameSite: http.SameSiteLaxMode, Expires: expires, MaxAge: 600})
	http.Redirect(w, r, authURL, http.StatusFound)
}

func (s *Server) oidcCallback(w http.ResponseWriter, r *http.Request) {
	stateCookie, err := r.Cookie(auth.OIDCStateCookie)
	if err != nil || r.URL.Query().Get("code") == "" || r.URL.Query().Get("state") == "" {
		apiError(w, r, http.StatusBadRequest, "oidc_callback_invalid", "OIDC callback 정보가 없습니다")
		return
	}
	user, err := s.Auth.OIDCCallback(r.Context(), r.URL.Query().Get("code"), r.URL.Query().Get("state"), stateCookie.Value, absoluteURL(r, "/api/v1/auth/oidc/callback"))
	if err != nil {
		_ = s.Store.RecordAudit(r.Context(), store.AuditEvent{Action: "auth.oidc", ResourceType: "session", Result: "failure", Reason: "oidc_callback_failed", IPAddress: clientIP(r), UserAgent: r.UserAgent(), RequestID: requestID(r)})
		apiError(w, r, http.StatusUnauthorized, "oidc_failed", err.Error())
		return
	}
	p, token, expires, err := s.Auth.CreateSession(r.Context(), user, clientIP(r), r.UserAgent())
	if err != nil {
		handleStoreError(w, r, err)
		return
	}
	http.SetCookie(w, auth.SecureCookie(token, expires, auth.IsSecureRequest(r)))
	http.SetCookie(w, &http.Cookie{Name: auth.OIDCStateCookie, Value: "", Path: "/api/v1/auth/oidc/callback", MaxAge: -1, HttpOnly: true, Secure: auth.IsSecureRequest(r), SameSite: http.SameSiteLaxMode})
	_ = s.Store.RecordAudit(r.Context(), store.AuditEvent{ActorUserID: &user.ID, ActorUsername: user.Username, Action: "auth.oidc", ResourceType: "session", ResourceID: p.JTI, Result: "success", IPAddress: clientIP(r), UserAgent: r.UserAgent(), RequestID: requestID(r)})
	http.Redirect(w, r, "/", http.StatusFound)
}

func absoluteURL(r *http.Request, path string) string {
	scheme := "http"
	if auth.IsSecureRequest(r) {
		scheme = "https"
	}
	return scheme + "://" + r.Host + path
}
