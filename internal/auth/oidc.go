package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/hkjang/jupiq/internal/integration"
	"github.com/hkjang/jupiq/internal/secure"
	"github.com/hkjang/jupiq/internal/store"
	"golang.org/x/oauth2"
)

const OIDCStateCookie = "jupiq_oidc_state"

type OIDCConfig struct {
	Enabled         bool     `json:"enabled"`
	IssuerURL       string   `json:"issuer_url"`
	ClientID        string   `json:"client_id"`
	RedirectURL     string   `json:"redirect_url"`
	Scopes          []string `json:"scopes"`
	UsernameClaim   string   `json:"username_claim"`
	AutoCreateUsers bool     `json:"auto_create_users"`
	VerifyTLS       bool     `json:"verify_tls"`
}

type oidcState struct {
	State        string    `json:"state"`
	Nonce        string    `json:"nonce"`
	CodeVerifier string    `json:"code_verifier"`
	ReturnTo     string    `json:"return_to,omitempty"`
	ExpiresAt    time.Time `json:"expires_at"`
}

// DefaultReturnTo is where a completed SSO login lands when the browser did not
// ask for a specific page. The SPA resolves "/" to the integrated dashboard for
// every account that may read it.
const DefaultReturnTo = "/"

const maxReturnToLength = 512

// SanitizeReturnTo keeps only same-origin absolute paths. Anything that could
// send the browser to another host after login - a scheme, a protocol-relative
// "//host" path, a backslash variant or a control character - falls back to the
// default landing page.
func SanitizeReturnTo(raw string) string {
	if raw == "" || len(raw) > maxReturnToLength {
		return DefaultReturnTo
	}
	decoded, err := url.QueryUnescape(raw)
	if err != nil {
		return DefaultReturnTo
	}
	decoded = strings.TrimSpace(decoded)
	if !strings.HasPrefix(decoded, "/") {
		return DefaultReturnTo
	}
	if strings.HasPrefix(decoded, "//") || strings.HasPrefix(decoded, "/\\") {
		return DefaultReturnTo
	}
	for _, r := range decoded {
		if r < 0x20 || r == 0x7f {
			return DefaultReturnTo
		}
	}
	parsed, err := url.Parse(decoded)
	if err != nil || parsed.IsAbs() || parsed.Host != "" {
		return DefaultReturnTo
	}
	return parsed.String()
}

func (s *Service) OIDCConfig(ctx context.Context) (OIDCConfig, bool, error) {
	cfg, _, configured, err := s.oidcConfigAndSecret(ctx)
	return cfg, configured, err
}

func (s *Service) oidcConfigAndSecret(ctx context.Context) (OIDCConfig, string, bool, error) {
	var cfg OIDCConfig
	secret, configured, err := s.Store.GetSettingAndSecret(ctx, "auth.oidc", "oidc.client_secret", &cfg)
	return cfg, secret, configured, err
}

func (s *Service) OIDCLogin(ctx context.Context, redirectOverride, returnTo string) (string, string, time.Time, error) {
	cfg, secret, configured, err := s.oidcConfigAndSecret(ctx)
	if err != nil || !cfg.Enabled || !configured || cfg.IssuerURL == "" || cfg.ClientID == "" {
		return "", "", time.Time{}, errors.New("OIDC 로그인이 설정되지 않았습니다")
	}
	if cfg.RedirectURL == "" {
		cfg.RedirectURL = redirectOverride
	}
	if _, err := integration.ValidateEndpoint(cfg.IssuerURL); err != nil {
		return "", "", time.Time{}, err
	}
	if _, err := url.ParseRequestURI(cfg.RedirectURL); err != nil {
		return "", "", time.Time{}, errors.New("OIDC redirect URL이 올바르지 않습니다")
	}
	providerCtx := oidc.ClientContext(ctx, integration.SafeHTTPClient(cfg.VerifyTLS, 10*time.Second))
	provider, err := oidc.NewProvider(providerCtx, cfg.IssuerURL)
	if err != nil {
		return "", "", time.Time{}, err
	}
	state, _ := secure.RandomToken(24)
	nonce, _ := secure.RandomToken(24)
	verifier, _ := secure.RandomToken(48)
	expires := time.Now().UTC().Add(10 * time.Minute)
	stateValue := oidcState{State: state, Nonce: nonce, CodeVerifier: verifier, ReturnTo: SanitizeReturnTo(returnTo), ExpiresAt: expires}
	raw, _ := json.Marshal(stateValue)
	encrypted, err := s.Cipher.EncryptString(string(raw), "oidc-state")
	if err != nil {
		return "", "", time.Time{}, err
	}
	if len(cfg.Scopes) == 0 {
		cfg.Scopes = []string{oidc.ScopeOpenID, "profile", "email"}
	}
	oauthConfig := oauth2.Config{ClientID: cfg.ClientID, ClientSecret: secret, Endpoint: provider.Endpoint(), RedirectURL: cfg.RedirectURL, Scopes: cfg.Scopes}
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	authURL := oauthConfig.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.SetAuthURLParam("code_challenge", challenge), oauth2.SetAuthURLParam("code_challenge_method", "S256"))
	return authURL, encrypted, expires, nil
}

func (s *Service) OIDCCallback(ctx context.Context, code, state, stateCookie, redirectOverride string) (store.User, string, error) {
	plain, err := s.Cipher.DecryptString(stateCookie, "oidc-state")
	if err != nil {
		return store.User{}, DefaultReturnTo, errors.New("OIDC state 쿠키가 유효하지 않습니다")
	}
	var saved oidcState
	if json.Unmarshal([]byte(plain), &saved) != nil || saved.State != state || time.Now().After(saved.ExpiresAt) {
		return store.User{}, DefaultReturnTo, errors.New("OIDC state가 만료되었거나 일치하지 않습니다")
	}
	cfg, secret, configured, err := s.oidcConfigAndSecret(ctx)
	if err != nil {
		return store.User{}, DefaultReturnTo, err
	}
	if !cfg.Enabled || !configured {
		return store.User{}, DefaultReturnTo, errors.New("OIDC 로그인이 비활성화되었습니다")
	}
	if cfg.RedirectURL == "" {
		cfg.RedirectURL = redirectOverride
	}
	if _, err := integration.ValidateEndpoint(cfg.IssuerURL); err != nil {
		return store.User{}, DefaultReturnTo, err
	}
	providerCtx := oidc.ClientContext(ctx, integration.SafeHTTPClient(cfg.VerifyTLS, 10*time.Second))
	provider, err := oidc.NewProvider(providerCtx, cfg.IssuerURL)
	if err != nil {
		return store.User{}, DefaultReturnTo, err
	}
	oauthConfig := oauth2.Config{ClientID: cfg.ClientID, ClientSecret: secret, Endpoint: provider.Endpoint(), RedirectURL: cfg.RedirectURL, Scopes: cfg.Scopes}
	token, err := oauthConfig.Exchange(providerCtx, code, oauth2.SetAuthURLParam("code_verifier", saved.CodeVerifier))
	if err != nil {
		return store.User{}, DefaultReturnTo, errors.New("OIDC authorization code 교환에 실패했습니다")
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok {
		return store.User{}, DefaultReturnTo, errors.New("OIDC 응답에 ID token이 없습니다")
	}
	idToken, err := provider.Verifier(&oidc.Config{ClientID: cfg.ClientID}).Verify(providerCtx, rawIDToken)
	if err != nil {
		return store.User{}, DefaultReturnTo, errors.New("OIDC ID token 검증에 실패했습니다")
	}
	var claims map[string]any
	if err := idToken.Claims(&claims); err != nil {
		return store.User{}, DefaultReturnTo, err
	}
	if nonce, _ := claims["nonce"].(string); nonce != saved.Nonce {
		return store.User{}, DefaultReturnTo, errors.New("OIDC nonce가 일치하지 않습니다")
	}
	claimName := cfg.UsernameClaim
	if claimName == "" {
		claimName = "preferred_username"
	}
	username, _ := claims[claimName].(string)
	subject, _ := claims["sub"].(string)
	if username == "" || subject == "" || strings.ContainsAny(username, "\r\n") {
		return store.User{}, DefaultReturnTo, errors.New("OIDC 사용자 식별 claim이 없습니다")
	}
	display, _ := claims["name"].(string)
	email, _ := claims["email"].(string)
	department, _ := claims["department"].(string)
	user, err := s.Store.UpsertOIDCUser(ctx, oidcExternalIdentity(cfg.IssuerURL, subject), username, display, email, department, cfg.AutoCreateUsers)
	if err != nil {
		return store.User{}, DefaultReturnTo, err
	}
	return user, SanitizeReturnTo(saved.ReturnTo), nil
}

// oidcExternalIdentity binds an account to both issuer and subject.  A subject
// value reused by a newly configured identity provider can therefore never
// inherit the earlier provider's local roles.
func oidcExternalIdentity(issuer, subject string) string {
	canonicalIssuer := strings.TrimRight(strings.TrimSpace(issuer), "/")
	sum := sha256.Sum256([]byte(canonicalIssuer + "\x00" + subject))
	return "oidc:" + base64.RawURLEncoding.EncodeToString(sum[:])
}
