package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/hkjang/jupiq/internal/auth/authtest"
)

// MCP OAuth의 리소스 서버 절반 중 저장소가 필요 없는 부분: 토큰의 서명·발급자·
// 만료·nbf·typ·cnf·대상 검사와 메타데이터·401 도전의 모양. 계정 매핑과 전체
// 배선은 api 패키지의 통합 테스트(PostgreSQL)가 본다.

func mcpSettings(idp *authtest.FakeIDP, audience ...string) MCPOAuthSettings {
	cfg := DefaultMCPOAuthConfig()
	cfg.Enabled = true
	cfg.Resource = "https://jupiq.example.test/mcp"
	cfg.Audience = audience
	return MCPOAuthSettings{Config: cfg, Issuer: idp.URL(), VerifyTLS: true}
}

func mcpRequest() *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	r.Host = "jupiq.example.test"
	r.Header.Set("X-Forwarded-Proto", "https")
	return r
}

func accessClaims(overrides map[string]any) map[string]any {
	claims := map[string]any{
		"sub": "subject-1", "aud": "https://jupiq.example.test/mcp", "typ": "Bearer",
		"exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(), "preferred_username": "member",
	}
	for key, value := range overrides {
		claims[key] = value
	}
	return claims
}

// refusalFor는 검증이 어디서 멈췄는지 본다. 저장소가 없으므로 모든 검사를
// 통과한 토큰은 계정 조회에서 nil 저장소를 만나기 전에 멈춰야 한다 — 그래서
// 여기서는 거부 사례만 다루고 통과 사례는 통합 테스트가 맡는다.
func refusalFor(t *testing.T, service *Service, settings MCPOAuthSettings, token string) *MCPOAuthRefusal {
	t.Helper()
	_, err := service.authenticateMCPOAuth(context.Background(), mcpRequest(), token, settings)
	if err == nil {
		t.Fatal("token was accepted")
	}
	var refusal *MCPOAuthRefusal
	if !errors.As(err, &refusal) {
		t.Fatalf("error is not a refusal: %v", err)
	}
	return refusal
}

func TestMCPOAuthRejectsTokensForTheRightReason(t *testing.T) {
	idp := authtest.NewFakeIDP(t)
	service := &Service{}
	settings := mcpSettings(idp)

	cases := []struct {
		name    string
		token   string
		message string
		cause   string
	}{
		{"expired", idp.Sign(t, accessClaims(map[string]any{"exp": time.Now().Add(-time.Minute).Unix()})), "유효하지 않습니다", "expired"},
		{"not yet valid", idp.Sign(t, accessClaims(map[string]any{"nbf": time.Now().Add(time.Hour).Unix()})), "유효하지 않습니다", "nbf"},
		{"other issuer", idp.Sign(t, accessClaims(map[string]any{"iss": "https://other-idp.example.test/realms/x"})), "유효하지 않습니다", "different provider"},
		{"HS256 with a shared secret", idp.SignWith(t, jwt.SigningMethodHS256, []byte("shared"), accessClaims(nil)), "유효하지 않습니다", "malformed jwt"},
		{"ID token", idp.Sign(t, accessClaims(map[string]any{"typ": "ID"})), "ID 토큰", "typ=ID"},
		{"sender constrained", idp.Sign(t, accessClaims(map[string]any{"cnf": map[string]any{"jkt": "abc"}})), "cnf", "cnf present"},
		{"empty subject", idp.Sign(t, accessClaims(map[string]any{"sub": ""})), "sub", "empty sub"},
		{"other application's token", idp.Sign(t, accessClaims(map[string]any{"aud": "account", "azp": "some-other-app"})), "발급된 것이 아닙니다", "not accepted"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			refusal := refusalFor(t, service, settings, tc.token)
			if !strings.Contains(refusal.Message, tc.message) {
				t.Errorf("client message %q does not say %q", refusal.Message, tc.message)
			}
			if refusal.Cause == nil || !strings.Contains(refusal.Cause.Error(), tc.cause) {
				t.Errorf("logged cause %v does not name %q", refusal.Cause, tc.cause)
			}
		})
	}

	// A foreign key with the right issuer string: the signature is what the
	// JWKS decides, not the claim.
	other := authtest.NewFakeIDP(t)
	forged := other.SignWith(t, jwt.SigningMethodRS256, other.Key, accessClaims(map[string]any{"iss": idp.URL()}))
	if refusal := refusalFor(t, service, settings, forged); !strings.Contains(refusal.Cause.Error(), "signature") && !strings.Contains(refusal.Cause.Error(), "verify") {
		t.Errorf("foreign signature cause: %v", refusal.Cause)
	}

	// The audience refusal tells the operator what was seen and what to write.
	refusal := refusalFor(t, service, settings, idp.Sign(t, accessClaims(map[string]any{"aud": "account", "azp": "claude-mcp"})))
	for _, want := range []string{"aud=[account]", `azp="claude-mcp"`, "mcp.oauth.audience", "https://jupiq.example.test/mcp"} {
		if !strings.Contains(refusal.Message, want) {
			t.Errorf("audience refusal %q lacks %q", refusal.Message, want)
		}
	}
}

// 리소스 식별자가 비어 있을 때 요청의 Host로 만든 값은 표시용일 뿐 허용 대상이
// 아니다. 같은 realm의 다른 리소스 서버용 토큰(aud=https://other.example/mcp)을
// 가진 쪽이 Host: other.example 로 보내도 대상 검사를 통과하지 못해야 한다 —
// 이 서버는 Host를 검증하지 않으므로 그 값을 믿으면 aud 검사가 없는 것과 같다.
func TestMCPOAuthEmptyResourceDoesNotTrustTheHostHeader(t *testing.T) {
	idp := authtest.NewFakeIDP(t)
	service := &Service{}
	settings := mcpSettings(idp)
	settings.Config.Resource = ""
	otherResource := "https://other.example.test/mcp"
	token := idp.Sign(t, accessClaims(map[string]any{"aud": otherResource, "azp": "other-app"}))

	r := mcpRequest()
	r.Host = "other.example.test"
	if got := settings.Resource(r); got != otherResource {
		t.Fatalf("the display resource should follow the Host header for this check to mean anything: %q", got)
	}
	_, err := service.authenticateMCPOAuth(context.Background(), r, token, settings)
	var refusal *MCPOAuthRefusal
	if !errors.As(err, &refusal) {
		t.Fatalf("a token for another resource server was accepted via a forged Host header: %v", err)
	}
	if !strings.Contains(refusal.Cause.Error(), "not accepted") {
		t.Errorf("refused for a different reason than the audience check: %v", refusal.Cause)
	}
	if strings.Contains(refusal.Message, `매퍼에 "`+otherResource) || !strings.Contains(refusal.Message, "mcp.oauth.resource") {
		t.Errorf("the guidance should ask the administrator to write mcp.oauth.resource, not put the forged Host in the mapper: %q", refusal.Message)
	}
	// The display value and the challenge still follow the Host — that is
	// what tells a client where the metadata lives — but only there.
	if challenge := settings.Challenge(r, true); !strings.Contains(challenge, "https://other.example.test/.well-known/oauth-protected-resource/mcp") {
		t.Errorf("challenge no longer follows the Host for display: %s", challenge)
	}
}

func TestMCPOAuthAudienceRules(t *testing.T) {
	resource := "https://jupiq.example.test/mcp"
	cases := []struct {
		name     string
		audience []string
		azp      string
		resource string
		allowed  []string
		want     bool
	}{
		{"resource in aud (mapper path)", []string{"account", resource}, "web", resource, nil, true},
		{"azp listed by the administrator (Keycloak 26 default shape)", []string{"account"}, "claude-mcp", resource, []string{"claude-mcp"}, true},
		{"aud listed by the administrator", []string{"other-app"}, "", resource, []string{"other-app"}, true},
		{"nothing matches", []string{"account"}, "claude-mcp", resource, []string{"cursor-mcp"}, false},
		{"empty list entries are ignored", []string{"account"}, "", resource, []string{"", " "}, false},
		{"no audience, no azp", nil, "", resource, []string{"claude-mcp"}, false},
		// No configured resource: only the administrator's list admits a token.
		{"empty resource, aud of another resource server", []string{"https://other.example.test/mcp"}, "", "", nil, false},
		{"empty resource never matches an empty aud entry", []string{""}, "", "", nil, false},
		{"empty resource, azp listed", []string{"account"}, "claude-mcp", "", []string{"claude-mcp"}, true},
	}
	for _, tc := range cases {
		if got := mcpAudienceAccepted(tc.audience, tc.azp, tc.resource, tc.allowed); got != tc.want {
			t.Errorf("%s: got %t want %t", tc.name, got, tc.want)
		}
	}
}

// 검증을 모두 통과한 토큰은 계정 조회까지 간다. 저장소 없는 Service에서는
// 거기서 panic이 나므로, 그 직전까지 통과했다는 것을 별도 경로로 확인한다:
// 같은 토큰을 aud만 바꿔 거부시키면 대상 검사가 마지막 관문이다.
func TestMCPOAuthAcceptsAsymmetricAlgorithmsUpToTheAccountLookup(t *testing.T) {
	idp := authtest.NewFakeIDP(t)
	service := &Service{}
	settings := mcpSettings(idp, "claude-mcp")
	// ES256 with a key the provider does not publish must fail at the
	// signature, proving the algorithm list is not what admits a token.
	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	refusal := refusalFor(t, service, settings, idp.SignWith(t, jwt.SigningMethodES256, ecKey, accessClaims(nil)))
	if !strings.Contains(refusal.Message, "유효하지 않습니다") {
		t.Errorf("unpublished ES256 key: %q", refusal.Message)
	}
	// Discovery happened once for the failures above; the provider is cached
	// so the audience refusal below does not fetch it again.
	before := idp.JWKSRequests.Load()
	refusal = refusalFor(t, service, settings, idp.Sign(t, accessClaims(map[string]any{"aud": "account", "azp": "cursor-mcp"})))
	if !strings.Contains(refusal.Cause.Error(), "not accepted") {
		t.Errorf("expected the audience check to be the last gate, got %v", refusal.Cause)
	}
	if after := idp.JWKSRequests.Load(); after != before {
		t.Errorf("JWKS was fetched again for a known key id: %d -> %d", before, after)
	}
}

func TestMCPOAuthDiscoveryIsCachedPerIssuer(t *testing.T) {
	idp := authtest.NewFakeIDP(t)
	service := &Service{}
	first, err := service.mcpProvider(context.Background(), idp.URL(), true)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.mcpProvider(context.Background(), idp.URL(), true)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Error("discovery was repeated within the TTL")
	}
	if insecure, _ := service.mcpProvider(context.Background(), idp.URL(), false); insecure == first {
		t.Error("verify_tls=false shares the provider built with TLS verification")
	}
	service.ForgetMCPProviders()
	if third, _ := service.mcpProvider(context.Background(), idp.URL(), true); third == first {
		t.Error("ForgetMCPProviders kept the cached provider")
	}
	if _, err := service.mcpProvider(context.Background(), "http://127.0.0.1:1/realms/x", true); err == nil {
		t.Error("a loopback issuer passed the integration endpoint guard")
	}
}

// Keycloak이 닿지 않는 동안 JWT 모양 bearer를 실은 /mcp 요청마다 Discovery를
// 다시 시도하면 안 된다: 실패도 짧게 기억해 그 동안은 IdP에 가지 않고 즉시
// 같은 오류로 거부한다. 기억이 끝나면 다시 시도해 복구를 알아챈다.
func TestMCPOAuthDiscoveryFailureIsCachedBriefly(t *testing.T) {
	idp := authtest.NewFakeIDP(t)
	service := &Service{}
	idp.DiscoveryDown.Store(true)

	if _, err := service.mcpProvider(context.Background(), idp.URL(), true); err == nil {
		t.Fatal("discovery succeeded while the provider was down")
	}
	attempts := idp.DiscoveryRequests.Load()
	if attempts == 0 {
		t.Fatal("the first call never reached the provider")
	}
	// The provider recovers, but the failure is still remembered: the next
	// calls must not reach it and must answer at once with the same error.
	idp.DiscoveryDown.Store(false)
	started := time.Now()
	for i := 0; i < 3; i++ {
		if _, err := service.mcpProvider(context.Background(), idp.URL(), true); err == nil {
			t.Fatal("a cached discovery failure was not returned")
		}
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Errorf("cached failures took %s to answer", elapsed)
	}
	if got := idp.DiscoveryRequests.Load(); got != attempts {
		t.Errorf("discovery was retried while the failure was cached: %d -> %d", attempts, got)
	}
	// A token in that window is refused with the discovery guidance, without
	// reaching the provider either.
	settings := mcpSettings(idp)
	refusal := refusalFor(t, service, settings, idp.Sign(t, accessClaims(nil)))
	if !strings.Contains(refusal.Message, "발급자 정보") || !strings.Contains(refusal.Cause.Error(), "discovery") {
		t.Errorf("token refused for another reason during the outage: %q / %v", refusal.Message, refusal.Cause)
	}
	if got := idp.DiscoveryRequests.Load(); got != attempts {
		t.Errorf("a token during the outage triggered discovery: %d -> %d", attempts, got)
	}

	// Once the negative entry expires the provider is consulted again and
	// the recovery is seen.
	service.mcpMu.Lock()
	key := idp.URL() + "|true"
	entry := service.mcpProviders[key]
	if entry.err == nil || entry.provider != nil {
		t.Fatalf("no negative entry was cached: %+v", entry)
	}
	if ttl := time.Until(entry.expires); ttl > mcpProviderFailureTTL || ttl < mcpProviderFailureTTL-10*time.Second {
		t.Errorf("negative entry TTL is %s, want about %s", ttl, mcpProviderFailureTTL)
	}
	entry.expires = time.Now().Add(-time.Second)
	service.mcpProviders[key] = entry
	service.mcpMu.Unlock()
	if _, err := service.mcpProvider(context.Background(), idp.URL(), true); err != nil {
		t.Fatalf("discovery was not retried after the negative entry expired: %v", err)
	}
	if got := idp.DiscoveryRequests.Load(); got != attempts+1 {
		t.Errorf("expected exactly one retry after expiry: %d -> %d", attempts, got)
	}
}

func TestMCPOAuthMetadataAndChallengeShape(t *testing.T) {
	settings := MCPOAuthSettings{Config: DefaultMCPOAuthConfig(), Issuer: "https://keycloak.example.test/realms/corp"}
	settings.Config.Enabled = true
	r := mcpRequest()
	if got := settings.Resource(r); got != "https://jupiq.example.test/mcp" {
		t.Errorf("resource from the request: %q", got)
	}
	settings.Config.Resource = "https://public.example.test/mcp"
	if got := settings.Resource(r); got != "https://public.example.test/mcp" {
		t.Errorf("configured resource was not preferred: %q", got)
	}
	if got := MCPResourceMetadataURL("https://public.example.test/mcp"); got != "https://public.example.test/.well-known/oauth-protected-resource/mcp" {
		t.Errorf("metadata URL: %q", got)
	}
	if got := MCPResourceMetadataURL("not a url"); got != "" {
		t.Errorf("metadata URL for garbage: %q", got)
	}
	metadata := settings.Metadata(r)
	if metadata["resource"] != "https://public.example.test/mcp" || metadata["resource_name"] != "jupiq MCP" {
		t.Errorf("metadata: %#v", metadata)
	}
	if servers := metadata["authorization_servers"].([]string); len(servers) != 1 || servers[0] != settings.Issuer {
		t.Errorf("authorization servers: %v", servers)
	}
	if scopes := metadata["scopes_supported"].([]string); len(scopes) != len(DefaultMCPOAuthScopes()) {
		t.Errorf("scopes: %v", scopes)
	}
	challenge := settings.Challenge(r, false)
	if challenge != `Bearer realm="jupiq", resource_metadata="https://public.example.test/.well-known/oauth-protected-resource/mcp"` {
		t.Errorf("challenge: %s", challenge)
	}
	if withToken := settings.Challenge(r, true); !strings.HasSuffix(withToken, `, error="invalid_token"`) {
		t.Errorf("challenge after a refused token: %s", withToken)
	}

	// Inactive: on but without an issuer, or simply off.
	if reason := settings.Inactive(); reason != "" {
		t.Errorf("active settings reported inactive: %s", reason)
	}
	if reason := (MCPOAuthSettings{Config: settings.Config}).Inactive(); !strings.Contains(reason, "issuer_url") {
		t.Errorf("missing issuer reason: %q", reason)
	}
	if reason := (MCPOAuthSettings{Config: DefaultMCPOAuthConfig(), Issuer: settings.Issuer}).Inactive(); !strings.Contains(reason, "enabled") {
		t.Errorf("disabled reason: %q", reason)
	}
}

func TestMCPOAuthBearerClassification(t *testing.T) {
	if !LooksLikeJWT("a.b.c") || LooksLikeJWT("a.b") || LooksLikeJWT("a..c") || LooksLikeJWT("jqk_abc") {
		t.Error("JWT shape test")
	}
	if !IsMCPPath("/mcp") || !IsMCPPath("/api/v1/mcp") || IsMCPPath("/api/v1/dashboard") || IsMCPPath("/mcp/") {
		t.Error("MCP path test")
	}
	session := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"iss": Issuer, "sub": "1"})
	signed, err := session.SignedString([]byte("k"))
	if err != nil {
		t.Fatal(err)
	}
	if got := unverifiedIssuer(signed); got != Issuer {
		t.Errorf("session issuer: %q", got)
	}
	if got := unverifiedIssuer("a.b.c"); got != "" {
		t.Errorf("garbage issuer: %q", got)
	}
}

func TestOAuthScopesAreACeilingLikeAKey(t *testing.T) {
	p := Principal{UserPermissions: []string{"*"}, OAuthScopes: []string{"mcp:use", "dashboard:read"}}
	if !p.Allows("mcp:use") || !p.Allows("dashboard:read") {
		t.Error("scoped permission refused")
	}
	if p.Allows("hubs:read") || p.Allows("settings:write") {
		t.Error("OAuth subject exceeded mcp.oauth.scopes")
	}
	if (Principal{UserPermissions: []string{"mcp:use"}, OAuthScopes: []string{"mcp:use", "hubs:read"}}).Allows("hubs:read") {
		t.Error("OAuth subject exceeded the user's own permissions")
	}
	if (Principal{UserPermissions: []string{"*"}, OAuthScopes: []string{}}).Allows("mcp:use") {
		t.Error("empty scope list granted access")
	}
}
