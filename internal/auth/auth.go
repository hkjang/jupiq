package auth

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/hkjang/jupiq/internal/secure"
	"github.com/hkjang/jupiq/internal/store"
	"golang.org/x/crypto/bcrypt"
)

const (
	CookieName = "jupiq_session"
	Issuer     = "jupiq"
	Audience   = "jupiq-api"
)

type Claims struct {
	Username string   `json:"username"`
	Roles    []string `json:"roles"`
	jwt.RegisteredClaims
}

type Principal struct {
	User            store.User
	JTI             string
	APIKeyID        int64
	APIKeyScopes    []string
	UserPermissions []string
}

func (p Principal) Allows(permission string) bool {
	if !store.EnsurePermission(p.UserPermissions, permission) {
		return false
	}
	if p.APIKeyID == 0 {
		return true
	}
	if len(p.APIKeyScopes) == 0 {
		return false
	}
	return store.EnsurePermission(p.APIKeyScopes, permission)
}

type Service struct {
	Store  *store.Store
	Cipher *secure.Cipher
	key    []byte
	now    func() time.Time
}

func NewService(s *store.Store, cipher *secure.Cipher) *Service {
	return &Service{Store: s, Cipher: cipher, key: cipher.Derive("jwt-signing-v1"), now: time.Now}
}

func (s *Service) Login(ctx context.Context, username, password, ip, userAgent string) (Principal, string, time.Time, error) {
	credential, err := s.Store.GetUserByUsername(ctx, username)
	dummy := "$2a$10$ZrWj5TmW.3VssVK4KzGtnuCN0MybvBkGUuD3Wc8rFwRRUWUT5F5X2"
	hash := dummy
	if err == nil && credential.PasswordHash != nil {
		hash = *credential.PasswordHash
	}
	compareErr := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	if err != nil || compareErr != nil || !credential.Active || credential.AuthSource != "local" {
		return Principal{}, "", time.Time{}, errors.New("아이디 또는 비밀번호가 올바르지 않습니다")
	}
	principal := Principal{User: credential.User, UserPermissions: credential.Permissions}
	token, expires, jti, err := s.issue(principal.User)
	if err != nil {
		return Principal{}, "", time.Time{}, err
	}
	if err := s.Store.CreateSession(ctx, jti, principal.User.ID, expires, ip, userAgent); err != nil {
		return Principal{}, "", time.Time{}, err
	}
	_ = s.Store.TouchLogin(ctx, principal.User.ID)
	principal.JTI = jti
	return principal, token, expires, nil
}

func (s *Service) CreateSession(ctx context.Context, user store.User, ip, userAgent string) (Principal, string, time.Time, error) {
	token, expires, jti, err := s.issue(user)
	if err != nil {
		return Principal{}, "", time.Time{}, err
	}
	if err := s.Store.CreateSession(ctx, jti, user.ID, expires, ip, userAgent); err != nil {
		return Principal{}, "", time.Time{}, err
	}
	_ = s.Store.TouchLogin(ctx, user.ID)
	return Principal{User: user, UserPermissions: user.Permissions, JTI: jti}, token, expires, nil
}

func (s *Service) issue(user store.User) (string, time.Time, string, error) {
	now := s.now().UTC()
	expires := now.Add(8 * time.Hour)
	jti, err := secure.RandomToken(24)
	if err != nil {
		return "", time.Time{}, "", err
	}
	claims := Claims{Username: user.Username, Roles: user.Roles, RegisteredClaims: jwt.RegisteredClaims{
		Issuer: Issuer, Subject: strconv.FormatInt(user.ID, 10), Audience: jwt.ClaimStrings{Audience}, ExpiresAt: jwt.NewNumericDate(expires), NotBefore: jwt.NewNumericDate(now.Add(-30 * time.Second)), IssuedAt: jwt.NewNumericDate(now), ID: jti,
	}}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(s.key)
	return token, expires, jti, err
}

func (s *Service) AuthenticateRequest(ctx context.Context, r *http.Request) (Principal, error) {
	authorization := strings.TrimSpace(r.Header.Get("Authorization"))
	if strings.HasPrefix(strings.ToLower(authorization), "bearer ") {
		plain := strings.TrimSpace(authorization[7:])
		if strings.HasPrefix(plain, "jqk_") {
			user, scopes, keyID, err := s.Store.AuthenticateAPIKey(ctx, plain)
			if err != nil || !user.Active {
				return Principal{}, errors.New("유효하지 않은 API 키입니다")
			}
			return Principal{User: user, UserPermissions: user.Permissions, APIKeyScopes: scopes, APIKeyID: keyID}, nil
		}
		return s.authenticateJWT(ctx, plain)
	}
	cookie, err := r.Cookie(CookieName)
	if err != nil {
		return Principal{}, errors.New("인증이 필요합니다")
	}
	return s.authenticateJWT(ctx, cookie.Value)
}

func (s *Service) authenticateJWT(ctx context.Context, token string) (Principal, error) {
	claims := &Claims{}
	parsed, err := jwt.ParseWithClaims(token, claims, func(token *jwt.Token) (any, error) {
		if token.Method != jwt.SigningMethodHS256 {
			return nil, errors.New("unexpected signing method")
		}
		return s.key, nil
	}, jwt.WithIssuer(Issuer), jwt.WithAudience(Audience), jwt.WithExpirationRequired(), jwt.WithValidMethods([]string{"HS256"}), jwt.WithLeeway(30*time.Second))
	if err != nil || !parsed.Valid {
		return Principal{}, errors.New("세션이 만료되었거나 유효하지 않습니다")
	}
	userID, err := strconv.ParseInt(claims.Subject, 10, 64)
	if err != nil || claims.ID == "" {
		return Principal{}, errors.New("세션 정보가 올바르지 않습니다")
	}
	active, err := s.Store.SessionActive(ctx, claims.ID, userID)
	if err != nil || !active {
		return Principal{}, errors.New("폐기된 세션입니다")
	}
	user, err := s.Store.GetUser(ctx, userID)
	if err != nil || !user.Active {
		return Principal{}, errors.New("사용할 수 없는 계정입니다")
	}
	if subtle.ConstantTimeCompare([]byte(claims.Username), []byte(user.Username)) != 1 {
		return Principal{}, errors.New("세션 계정 정보가 일치하지 않습니다")
	}
	return Principal{User: user, UserPermissions: user.Permissions, JTI: claims.ID}, nil
}

func (s *Service) Logout(ctx context.Context, principal Principal) error {
	if principal.JTI == "" {
		return nil
	}
	return s.Store.RevokeSession(ctx, principal.JTI)
}

func SecureCookie(token string, expires time.Time, secureOnly bool) *http.Cookie {
	return &http.Cookie{Name: CookieName, Value: token, Path: "/", HttpOnly: true, Secure: secureOnly, SameSite: http.SameSiteLaxMode, Expires: expires, MaxAge: int(time.Until(expires).Seconds())}
}

func ClearCookie(secureOnly bool) *http.Cookie {
	return &http.Cookie{Name: CookieName, Value: "", Path: "/", HttpOnly: true, Secure: secureOnly, SameSite: http.SameSiteLaxMode, Expires: time.Unix(1, 0), MaxAge: -1}
}

func IsSecureRequest(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0]), "https")
}

func Subject(principal Principal) string {
	return fmt.Sprintf("%d:%s", principal.User.ID, principal.User.Username)
}
