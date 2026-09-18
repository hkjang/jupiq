package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/golang-jwt/jwt/v5"
	"github.com/hkjang/jupiq/internal/integration"
	"github.com/hkjang/jupiq/internal/store"
)

// MCP를 개인 키 없이 Keycloak 액세스 토큰으로.
//
// MCP 인가 규격(2025-06-18 이후)은 OAuth 2.1이다. 이 서버는 리소스 서버다 —
// 어디에 인증 서버가 있는지 알리고(RFC 9728), 그 서버가 이 리소스를 위해
// 발급한 토큰인지 검사한다. 로그인·토큰 발급·클라이언트 등록은 Keycloak의
// 몫이고 여기서는 하지 않는다.
//
// 개인 키(jqk_)는 그대로다. OAuth 토큰은 이미 웹으로 로그인해 등록된 계정을
// 두 번째 문으로 여는 것뿐이다: 계정을 만들지 않고, 정지된 계정을 열지 않고,
// 토큰의 role로 권한을 올리지 않으며, 범위는 관리자 설정 mcp.oauth.scopes가
// 정한다. /mcp 밖(REST·관리 API)에서는 받지 않는다.

// MCPOAuthSettingKey는 settings 표의 문서 키다. 필드 이름은 사내 표준
// (mcp.oauth.enabled·resource·audience·scopes)을 그대로 따른다.
const MCPOAuthSettingKey = "mcp.oauth"

// MCPPath는 MCP 엔드포인트다. /api/v1/mcp는 같은 핸들러의 별칭이다.
const MCPPath = "/mcp"

// MCPResourceMetadataPath는 RFC 9728 보호 리소스 메타데이터 경로다.
const MCPResourceMetadataPath = "/.well-known/oauth-protected-resource"

type MCPOAuthConfig struct {
	Enabled bool `json:"enabled"`
	// Resource는 클라이언트가 실제로 접속하는 공개 주소 + MCP 경로다
	// (RFC 8707). 토큰의 aud와 비교되는 값은 이것뿐이다. 비어 있으면 메타데이터·
	// 401 도전에 보이는 주소만 요청의 Host로 만들고, aud 비교는 건너뛰어
	// mcp.oauth.audience 목록만 본다 — 프록시 뒤에서는 관리자가 적어야 한다.
	Resource string `json:"resource"`
	// Audience는 aud 또는 azp와 비교할 허용 대상이다. 실제 Keycloak 26은
	// aud에 account만 싣고 클라이언트 ID는 azp에 담으므로, Audience 매퍼 없이
	// 쓰려면 MCP 클라이언트 ID를 여기 적는다.
	Audience []string `json:"audience"`
	// Scopes는 SSO 토큰 주체에게 주는 권한이다. 개인 키의 scope와 같은 어휘로,
	// 사용자 자신의 권한과 교집합만 유효하다.
	Scopes []string `json:"scopes"`
}

// DefaultMCPOAuthScopes는 MCP 도구 넷이 요구하는 읽기 권한이다.
func DefaultMCPOAuthScopes() []string {
	return []string{"mcp:use", "dashboard:read", "hubs:read", "servers:read", "usage:read"}
}

func DefaultMCPOAuthConfig() MCPOAuthConfig {
	return MCPOAuthConfig{Enabled: false, Resource: "", Audience: []string{}, Scopes: DefaultMCPOAuthScopes()}
}

// MCPOAuthSettings는 리소스 서버 역할에 필요한 값을 한 번에 읽은 것이다.
// Issuer·VerifyTLS는 웹 로그인의 auth.oidc 설정을 재사용한다.
type MCPOAuthSettings struct {
	Config    MCPOAuthConfig
	Issuer    string
	VerifyTLS bool
}

func (s *Service) MCPOAuthSettings(ctx context.Context) (MCPOAuthSettings, error) {
	settings := MCPOAuthSettings{Config: DefaultMCPOAuthConfig()}
	if s.Store == nil {
		return settings, nil
	}
	var cfg MCPOAuthConfig
	err := s.Store.GetSetting(ctx, MCPOAuthSettingKey, &cfg)
	switch {
	case errors.Is(err, store.ErrNotFound):
		// 문서가 없으면 꺼짐이다. 기존 설치는 아무것도 달라지지 않는다.
	case err != nil:
		return settings, err
	default:
		settings.Config = cfg
		if len(cfg.Scopes) == 0 {
			settings.Config.Scopes = DefaultMCPOAuthScopes()
		}
	}
	if !settings.Config.Enabled {
		return settings, nil
	}
	oidcCfg, _, err := s.OIDCConfig(ctx)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return settings, err
	}
	settings.Issuer = strings.TrimRight(strings.TrimSpace(oidcCfg.IssuerURL), "/")
	settings.VerifyTLS = oidcCfg.VerifyTLS
	return settings, nil
}

// Inactive는 켜 두었는데도 꺼진 것처럼 동작하는 이유를 돌려준다. 활성이면
// 빈 문자열이다. 켜는 조건은 스위치와 OIDC issuer 둘 다 있는 것이다 — MCP
// 자체는 항상 켜져 있고, 리소스 식별자가 비어 있으면 대상 검사는
// mcp.oauth.audience 목록만으로 한다(AudienceResource).
func (m MCPOAuthSettings) Inactive() string {
	if !m.Config.Enabled {
		return "mcp.oauth.enabled가 꺼져 있습니다"
	}
	if m.Issuer == "" {
		return "auth.oidc.issuer_url이 비어 있어 토큰을 검증할 발급자가 없습니다"
	}
	return ""
}

func (m MCPOAuthSettings) Active() bool { return m.Inactive() == "" }

// AudienceResource는 토큰의 aud와 비교하는 리소스 식별자다. 관리자가 적은
// 값만 쓴다 — 비어 있으면 빈 문자열이고 대상 검사는 aud 비교를 건너뛴다.
// 요청의 Host로 만든 값은 절대 여기 오지 않는다: 이 서버는 Host를 검증하지
// 않으므로, 같은 realm의 다른 리소스 서버용 토큰을 가진 쪽이 Host를 그
// 리소스로 적어 보내면 대상 검사를 통과해 버린다(audience confusion).
func (m MCPOAuthSettings) AudienceResource() string {
	return strings.TrimSpace(m.Config.Resource)
}

// Resource는 메타데이터·WWW-Authenticate에 보이는 리소스 식별자다. 설정이
// 비어 있을 때만 요청의 Host로 만든다 — 표시용일 뿐, 대상 검사에는 쓰지
// 않는다(AudienceResource).
func (m MCPOAuthSettings) Resource(r *http.Request) string {
	if resource := m.AudienceResource(); resource != "" {
		return resource
	}
	scheme := "http"
	if IsSecureRequest(r) {
		scheme = "https"
	}
	return scheme + "://" + r.Host + MCPPath
}

// MCPResourceMetadataURL은 리소스 식별자에서 메타데이터 문서 주소를 만든다.
// RFC 9728의 경로 삽입 규칙(origin + well-known + 리소스 경로)이다.
func MCPResourceMetadataURL(resource string) string {
	u, err := url.Parse(resource)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host + MCPResourceMetadataPath + u.Path
}

// Metadata는 거부된 클라이언트가 읽는 RFC 9728 문서다. 공개 문서다 — 어디서
// 로그인하는지 말할 뿐 누가 로그인했는지는 담지 않는다.
func (m MCPOAuthSettings) Metadata(r *http.Request) map[string]any {
	return map[string]any{
		"resource":                 m.Resource(r),
		"authorization_servers":    []string{m.Issuer},
		"bearer_methods_supported": []string{"header"},
		"scopes_supported":         append([]string{}, m.Config.Scopes...),
		"resource_name":            "jupiq MCP",
	}
}

// Challenge는 /mcp의 401에 붙는 WWW-Authenticate 값이다. 이 헤더가 있어야
// MCP 클라이언트가 메타데이터를 읽고 OAuth 흐름을 시작한다. 토큰이 있었는데
// 거부했으면 error="invalid_token"을 더한다.
func (m MCPOAuthSettings) Challenge(r *http.Request, tokenPresented bool) string {
	header := fmt.Sprintf(`Bearer realm="jupiq", resource_metadata=%q`, MCPResourceMetadataURL(m.Resource(r)))
	if tokenPresented {
		header += `, error="invalid_token"`
	}
	return header
}

// IsMCPPath는 OAuth 토큰을 받는 유일한 경로인지 가른다.
func IsMCPPath(path string) bool {
	return path == MCPPath || path == "/api/v1"+MCPPath
}

// LooksLikeJWT는 "키가 아니다"와 "우리가 받는 어떤 토큰도 아니다"를 가르는
// 값싼 모양 검사다: 점 두 개, 세 조각 모두 비어 있지 않음.
func LooksLikeJWT(token string) bool {
	parts := strings.Split(token, ".")
	return len(parts) == 3 && parts[0] != "" && parts[1] != "" && parts[2] != ""
}

// unverifiedIssuer는 서명을 보기 전에 iss만 읽는다. jupiq 자신의 세션 JWT와
// 외부 발급자의 액세스 토큰이 같은 Bearer 헤더로 오므로, 누구에게 검증을
// 맡길지 정하는 데만 쓴다. 검증은 그 뒤 각자의 경로가 한다.
func unverifiedIssuer(token string) string {
	claims := jwt.MapClaims{}
	if _, _, err := jwt.NewParser().ParseUnverified(token, claims); err != nil {
		return ""
	}
	issuer, _ := claims["iss"].(string)
	return issuer
}

// MCPOAuthRefusal은 토큰을 거부한 이유다. Message는 클라이언트에게 보이는
// 조치 안내이고 Cause는 서명·발급자·만료 중 무엇이 실패했는지 담은 원래
// 오류로, API 계층이 서버 로그에 남긴다.
type MCPOAuthRefusal struct {
	Message string
	Cause   error
}

func (e *MCPOAuthRefusal) Error() string { return e.Message }
func (e *MCPOAuthRefusal) Unwrap() error { return e.Cause }

func refuse(message string, cause error) error {
	return &MCPOAuthRefusal{Message: message, Cause: cause}
}

// mcpSigningAlgs는 비대칭 서명만 허용한다. HS*는 JWKS로 검증할 수 없는
// 공유 비밀이고 none은 서명이 아니다.
var mcpSigningAlgs = []string{oidc.RS256, oidc.RS384, oidc.RS512, oidc.ES256, oidc.ES384, oidc.ES512, oidc.PS256, oidc.PS384, oidc.PS512}

const (
	mcpProviderTTL = 10 * time.Minute
	// mcpProviderFailureTTL은 실패한 Discovery를 기억하는 시간이다. Keycloak이
	// 닿지 않는 동안 JWT 모양 bearer를 실은 /mcp 요청마다(익명 요청도 iss만
	// 다르게 적으면 된다) 10초 타임아웃까지 아웃바운드 호출을 하지 않도록,
	// 그 동안은 같은 오류로 즉시 거부한다. 복구는 이 시간 안에 알아챈다.
	mcpProviderFailureTTL = 30 * time.Second
)

// mcpProviderEntry는 issuer별 Discovery 결과다. Provider가 JWKS를 안에서
// 캐시하고 모르는 kid는 스스로 다시 받아 오므로 키 회전에 무효화가 필요 없다.
// 실패도 항목이다(provider nil, err 있음) — 짧은 TTL의 negative cache.
type mcpProviderEntry struct {
	provider *oidc.Provider
	err      error
	expires  time.Time
}

// mcpProvider는 Discovery를 issuer·TLS 검증 여부별로 재사용한다. 요청마다
// 받아 오면 Keycloak의 지연이 MCP 호출마다 앞에 붙는다. 요청 컨텍스트에서
// 떼어 낸(WithoutCancel) 컨텍스트로 만들어, 돌려받은 Provider가 나중의 키
// 조회에 취소된 요청을 붙들지 않게 한다. 잠금은 맵 읽기·쓰기에만 둔다 —
// Discovery 자체를 잠금 안에서 하면 Keycloak의 지연이 모든 MCP 요청을 세운다.
func (s *Service) mcpProvider(ctx context.Context, issuer string, verifyTLS bool) (*oidc.Provider, error) {
	key := fmt.Sprintf("%s|%t", issuer, verifyTLS)
	s.mcpMu.Lock()
	if entry, ok := s.mcpProviders[key]; ok && time.Now().Before(entry.expires) {
		s.mcpMu.Unlock()
		return entry.provider, entry.err
	}
	s.mcpMu.Unlock()
	if _, err := integration.ValidateEndpoint(issuer); err != nil {
		return nil, err
	}
	providerCtx := oidc.ClientContext(context.WithoutCancel(ctx), integration.SafeHTTPClient(verifyTLS, 10*time.Second))
	discoverCtx, cancel := context.WithTimeout(providerCtx, 10*time.Second)
	defer cancel()
	provider, err := oidc.NewProvider(discoverCtx, issuer)
	entry := mcpProviderEntry{provider: provider, expires: time.Now().Add(mcpProviderTTL)}
	if err != nil {
		entry = mcpProviderEntry{err: err, expires: time.Now().Add(mcpProviderFailureTTL)}
	}
	s.mcpMu.Lock()
	if s.mcpProviders == nil {
		s.mcpProviders = map[string]mcpProviderEntry{}
	}
	if current, ok := s.mcpProviders[key]; err != nil && ok && current.err == nil && time.Now().Before(current.expires) {
		// 동시에 뛴 다른 Discovery가 그 사이 성공했다면 그쪽이 맞다.
		s.mcpMu.Unlock()
		return current.provider, nil
	}
	s.mcpProviders[key] = entry
	s.mcpMu.Unlock()
	return provider, err
}

// ForgetMCPProviders는 캐시한 Discovery를 버린다(테스트·설정 변경용).
func (s *Service) ForgetMCPProviders() {
	s.mcpMu.Lock()
	s.mcpProviders = nil
	s.mcpMu.Unlock()
}

// mcpAccessClaims는 go-oidc가 검사하지 않는 항목이다.
type mcpAccessClaims struct {
	Type         string `json:"typ"`
	AuthorizedBy string `json:"azp"`
	Confirmation any    `json:"cnf"`
}

// authenticateMCPOAuth는 Bearer 액세스 토큰을 Principal로 바꾸거나 왜 안
// 되는지 정확히 말한다. AuthenticateRequest가 /mcp에서 JWT 모양인데 jupiq
// 세션이 아닌 값에만 부른다.
func (s *Service) authenticateMCPOAuth(ctx context.Context, r *http.Request, token string, settings MCPOAuthSettings) (Principal, error) {
	provider, err := s.mcpProvider(ctx, settings.Issuer, settings.VerifyTLS)
	if err != nil {
		return Principal{}, refuse("Keycloak 발급자 정보를 읽지 못해 SSO 토큰을 확인할 수 없습니다. 잠시 후 다시 시도하거나 관리자에게 알리세요.", fmt.Errorf("mcp oauth discovery %s: %w", settings.Issuer, err))
	}
	// 서명·iss·exp·nbf는 라이브러리가 본다. 대상은 아래에서 직접 본다 —
	// 라이브러리는 aud만 알고 azp를 모르며 허용 값이 여럿이다.
	verified, err := provider.Verifier(&oidc.Config{SkipClientIDCheck: true, SupportedSigningAlgs: mcpSigningAlgs}).Verify(ctx, token)
	if err != nil {
		return Principal{}, refuse("SSO 액세스 토큰이 유효하지 않습니다(서명·발급자·만료). 클라이언트에서 다시 로그인하세요.", fmt.Errorf("mcp oauth token rejected: %w", err))
	}
	var claims mcpAccessClaims
	if err := verified.Claims(&claims); err != nil {
		return Principal{}, refuse("SSO 토큰의 내용을 읽을 수 없습니다.", fmt.Errorf("mcp oauth claims: %w", err))
	}
	if strings.EqualFold(strings.TrimSpace(claims.Type), "ID") {
		// ID 토큰은 로그인 증거지 API 자격이 아니다.
		return Principal{}, refuse("SSO ID 토큰은 MCP 자격이 아닙니다. 액세스 토큰을 보내세요.", errors.New("mcp oauth token rejected: typ=ID"))
	}
	if claims.Confirmation != nil {
		// 검증할 수 없는 소지자 증명(DPoP·mTLS)이 묶인 토큰이다.
		return Principal{}, refuse("소지자 증명(cnf)이 묶인 SSO 토큰은 받지 않습니다.", errors.New("mcp oauth token rejected: cnf present"))
	}
	if strings.TrimSpace(verified.Subject) == "" {
		return Principal{}, refuse("SSO 토큰에 사용자 식별 정보(sub)가 없습니다.", errors.New("mcp oauth token rejected: empty sub"))
	}
	// 대상 검사에는 관리자가 적은 리소스 식별자만 쓴다. 요청의 Host로 만든
	// 표시용 값(settings.Resource)은 여기 오면 안 된다 — 클라이언트가 고르는
	// 헤더를 허용 대상으로 삼는 것이 되어 aud 검사가 막아야 할 audience
	// confusion을 그대로 연다.
	resource := settings.AudienceResource()
	if !mcpAudienceAccepted(verified.Audience, claims.AuthorizedBy, resource, settings.Config.Audience) {
		mapperHint := fmt.Sprintf("Keycloak 클라이언트의 Audience 매퍼에 %q를 넣어야 합니다", resource)
		if resource == "" {
			mapperHint = "리소스 식별자(mcp.oauth.resource)를 이 서버의 공개 주소로 적고 Keycloak 클라이언트의 Audience 매퍼에 같은 값을 넣어야 합니다"
		}
		return Principal{}, refuse(
			fmt.Sprintf("SSO 토큰이 이 서버를 위해 발급된 것이 아닙니다(aud=%v, azp=%q). 관리자가 허용 대상(mcp.oauth.audience)에 그 클라이언트 ID를 적거나, %s.", verified.Audience, claims.AuthorizedBy, mapperHint),
			fmt.Errorf("mcp oauth token rejected: aud %v / azp %q not accepted for %q", verified.Audience, claims.AuthorizedBy, resource))
	}
	// 웹 로그인이 묶어 둔 계정을 찾는다. 만드는 쪽 절반은 없다.
	user, err := s.Store.GetOIDCUserBySubject(ctx, OIDCExternalIdentity(settings.Issuer, verified.Subject))
	if store.IsNotFound(err) || (err == nil && !user.Active) {
		return Principal{}, refuse("이 SSO 계정은 jupiq에 등록되지 않았거나 비활성입니다. 먼저 웹으로 한 번 로그인하세요.", errors.New("mcp oauth token rejected: no active jupiq account for subject"))
	}
	if err != nil {
		// 저장소 오류의 원문(pgx 메시지 등)은 로그에만 남긴다. 세션·API 키
		// 경로(authenticateJWT)와 같이 클라이언트에는 고정 문구만 간다.
		return Principal{}, refuse("계정 정보를 확인할 수 없습니다. 잠시 후 다시 시도하세요.", fmt.Errorf("mcp oauth account lookup: %w", err))
	}
	return Principal{User: user, UserPermissions: user.Permissions, OAuthScopes: append([]string{}, settings.Config.Scopes...)}, nil
}

// mcpAudienceAccepted는 토큰이 이 서버를 위한 것인지 본다. aud에 관리자가
// 적은 리소스 식별자가 있거나(Audience 매퍼를 둔 정식 경로), aud 또는 azp가
// 관리자의 허용 대상에 있으면(매퍼 없이 쓰는 호환 경로) 받는다. resource가
// 비어 있으면 첫 분기는 없다 — 허용 목록만 본다.
func mcpAudienceAccepted(audience []string, azp, resource string, allowed []string) bool {
	if resource != "" && slices.Contains(audience, resource) {
		return true
	}
	for _, value := range allowed {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if slices.Contains(audience, value) || azp == value {
			return true
		}
	}
	return false
}
