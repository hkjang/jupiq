package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hkjang/jupiq/internal/secure"
)

// testProvider is an identity provider served over HTTP and reached through the
// production discovery path — real oidc.NewProvider over
// integration.SafeHTTPClient — so the wiring that the cache replaced is what
// these tests exercise. It counts the requests it serves per endpoint, which is
// how "a login start and its callback no longer each fetch Discovery" is
// measured instead of assumed.
type testProvider struct {
	url       string
	discovery atomic.Int32
	keys      atomic.Int32
	token     atomic.Int32
}

// startTestProvider binds a non-loopback address of this machine: SafeHTTPClient
// refuses loopback destinations, so an ordinary httptest server would never be
// dialled and the test would prove nothing about the real client.
func startTestProvider(t *testing.T) *testProvider {
	t.Helper()
	provider := &testProvider{}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		provider.discovery.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                provider.url,
			"authorization_endpoint":                provider.url + "/auth",
			"token_endpoint":                        provider.url + "/token",
			"jwks_uri":                              provider.url + "/keys",
			"response_types_supported":              []string{"code"},
			"subject_types_supported":               []string{"public"},
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/keys", func(w http.ResponseWriter, r *http.Request) {
		provider.keys.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"keys":[]}`))
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		provider.token.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
	})
	listener, err := net.Listen("tcp", net.JoinHostPort(nonLoopbackIPv4(t), "0"))
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	server := httptest.NewUnstartedServer(mux)
	_ = server.Listener.Close()
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)
	provider.url = server.URL
	return provider
}

// nonLoopbackIPv4 reports an address of this machine that the SSRF guard
// accepts. Without one the discovery path cannot be exercised at all, so the
// test skips rather than pretending to have covered it.
func nonLoopbackIPv4(t *testing.T) string {
	t.Helper()
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		t.Skipf("interface addresses: %v", err)
	}
	for _, address := range addresses {
		if network, ok := address.(*net.IPNet); ok && network.IP.To4() != nil && !network.IP.IsLoopback() {
			return network.IP.String()
		}
	}
	t.Skip("this machine has no non-loopback IPv4 address; SafeHTTPClient refuses loopback targets")
	return ""
}

func newOIDCTestService(t *testing.T, cfg OIDCConfig) *Service {
	t.Helper()
	cipher, err := secure.NewCipher(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	return &Service{
		Cipher:    cipher,
		providers: newOIDCProviderCache(),
		oidcSettings: func(context.Context) (OIDCConfig, string, bool, error) {
			return cfg, "client-secret-for-test", true, nil
		},
	}
}

func testOIDCConfig(issuer string) OIDCConfig {
	return OIDCConfig{
		Enabled:       true,
		IssuerURL:     issuer,
		ClientID:      "jupiq",
		RedirectURL:   "https://jupiq.internal/api/auth/oidc/callback",
		UsernameClaim: "preferred_username",
		VerifyTLS:     true,
	}
}

// TestNewServiceInstallsTheProviderCache pins the wiring itself: a Service built
// the production way must carry the cache, because oidcProviderCache.get falls
// back to an uncached discovery on a nil receiver and would therefore lose the
// caching silently, with nothing failing.
func TestNewServiceInstallsTheProviderCache(t *testing.T) {
	cipher, err := secure.NewCipher(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(nil, cipher)
	if service.providers == nil {
		t.Fatal("NewService left providers nil: every login would fetch Discovery again")
	}
	if service.providers.discover == nil || service.providers.newClient == nil || service.providers.now == nil {
		t.Fatal("NewService installed an unusable provider cache")
	}
	if service.providers.entries == nil {
		t.Fatal("NewService installed a cache without its entry map")
	}
}

// TestOIDCProviderCacheDiscoversOnceAgainstARealProvider runs the untouched
// newOIDCProviderCache() — real oidc.NewProvider, real SafeHTTPClient — against
// a live Discovery document and counts what the provider actually served.
func TestOIDCProviderCacheDiscoversOnceAgainstARealProvider(t *testing.T) {
	idp := startTestProvider(t)
	cache := newOIDCProviderCache()
	ctx := context.Background()

	first, firstClient, err := cache.get(ctx, idp.url, true)
	if err != nil {
		t.Fatalf("first discovery: %v", err)
	}
	second, secondClient, err := cache.get(ctx, idp.url, true)
	if err != nil {
		t.Fatalf("second lookup: %v", err)
	}
	if first != second || firstClient != secondClient {
		t.Fatal("the second lookup did not reuse the cached provider and client")
	}
	if got := idp.discovery.Load(); got != 1 {
		t.Fatalf("Discovery requests served = %d, want 1", got)
	}
	if first.Endpoint().TokenURL != idp.url+"/token" {
		t.Fatalf("token endpoint %q was not read from the served document", first.Endpoint().TokenURL)
	}
}

// TestOIDCLoginAndCallbackShareOneDiscovery is the claim ADMIN_GUIDE.md makes to
// operators, end to end: the login start and the callback that follows it cost
// the identity provider one Discovery request in total. The callback stops at
// the token exchange (the provider rejects the code) — everything before it,
// including the reuse of the cached endpoints, has already happened by then.
func TestOIDCLoginAndCallbackShareOneDiscovery(t *testing.T) {
	idp := startTestProvider(t)
	service := newOIDCTestService(t, testOIDCConfig(idp.url))
	ctx := context.Background()

	authURL, cookie, expires, err := service.OIDCLogin(ctx, "", "/hubs", false)
	if err != nil {
		t.Fatalf("login start: %v", err)
	}
	if cookie == "" || expires.IsZero() {
		t.Fatal("login start returned no state cookie")
	}
	parsed, err := url.Parse(authURL)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Host != strings.TrimPrefix(idp.url, "http://") || parsed.Path != "/auth" {
		t.Fatalf("authorization URL %q was not built from the served document", authURL)
	}
	state := parsed.Query().Get("state")
	if state == "" {
		t.Fatal("authorization URL carries no state")
	}
	if got := idp.discovery.Load(); got != 1 {
		t.Fatalf("Discovery requests after the login start = %d, want 1", got)
	}

	if _, _, err := service.OIDCCallback(ctx, "code-from-provider", state, cookie, ""); err == nil || !strings.Contains(err.Error(), "authorization code 교환") {
		t.Fatalf("callback error = %v, want the token exchange refusal", err)
	}
	// oauth2 probes both client-authentication styles against a refusing token
	// endpoint, so the count is "at least one" rather than exactly one.
	if got := idp.token.Load(); got < 1 {
		t.Fatal("the callback never reached the discovered token endpoint")
	}
	if got := idp.discovery.Load(); got != 1 {
		t.Fatalf("Discovery requests after the callback = %d, want 1: the callback refetched the document", got)
	}
}

// TestOIDCLoginRefusesWhenATokenDrawFails covers what f3834e0 changed: a failed
// entropy draw must abort the login instead of continuing with an empty state,
// nonce or PKCE verifier. Each of the three draws is failed in turn.
func TestOIDCLoginRefusesWhenATokenDrawFails(t *testing.T) {
	entropyFailure := errors.New("entropy source unavailable")
	for _, failAt := range []int{1, 2, 3} {
		now := time.Now()
		service := newOIDCTestService(t, testOIDCConfig("https://idp.example.com/realms/jupiq"))
		// The provider lookup is faked here: this test is about the draw, and it
		// must not depend on a reachable identity provider.
		service.providers = newTestProviderCache(&fakeDiscovery{}, &now)

		original := randomToken
		draws := 0
		randomToken = func(bytes int) (string, error) {
			draws++
			if draws == failAt {
				return "", entropyFailure
			}
			return original(bytes)
		}
		authURL, cookie, expires, err := service.OIDCLogin(context.Background(), "", "/hubs", false)
		randomToken = original

		if !errors.Is(err, entropyFailure) {
			t.Fatalf("draw %d failed: err = %v, want the entropy failure", failAt, err)
		}
		if authURL != "" {
			t.Fatalf("draw %d failed but an authorization URL was still issued: %q", failAt, authURL)
		}
		if cookie != "" {
			t.Fatalf("draw %d failed but a state cookie was still issued: %q", failAt, cookie)
		}
		if !expires.IsZero() {
			t.Fatalf("draw %d failed but an expiry was still issued: %v", failAt, expires)
		}
	}
}
