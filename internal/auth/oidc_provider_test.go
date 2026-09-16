package auth

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
)

// fakeDiscovery counts discovery calls and answers with a provider built from
// static metadata, so no network is touched. release, when set, holds every
// discovery open until the test closes it.
type fakeDiscovery struct {
	calls   atomic.Int32
	err     error
	release chan struct{}
}

func (f *fakeDiscovery) discover(ctx context.Context, issuer string) (*oidc.Provider, error) {
	f.calls.Add(1)
	if f.release != nil {
		select {
		case <-f.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if f.err != nil {
		return nil, f.err
	}
	cfg := oidc.ProviderConfig{IssuerURL: issuer, AuthURL: issuer + "/auth", TokenURL: issuer + "/token", JWKSURL: issuer + "/keys"}
	return cfg.NewProvider(ctx), nil
}

func newTestProviderCache(fake *fakeDiscovery, now *time.Time) *oidcProviderCache {
	cache := newOIDCProviderCache()
	cache.discover = fake.discover
	cache.newClient = func(bool) *http.Client { return &http.Client{} }
	cache.now = func() time.Time { return *now }
	return cache
}

func TestOIDCProviderCacheDiscoversOncePerIssuerWithinTTL(t *testing.T) {
	now := time.Date(2026, 9, 17, 9, 0, 0, 0, time.UTC)
	fake := &fakeDiscovery{}
	cache := newTestProviderCache(fake, &now)
	ctx := context.Background()

	first, firstClient, err := cache.get(ctx, "https://idp.example.com/realms/a", true)
	if err != nil {
		t.Fatal(err)
	}
	// Login start and callback share one discovery and one HTTP client.
	second, secondClient, err := cache.get(ctx, "https://idp.example.com/realms/a", true)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || firstClient != secondClient {
		t.Fatal("expected the cached provider and client to be reused")
	}
	if got := fake.calls.Load(); got != 1 {
		t.Fatalf("discovery calls = %d, want 1", got)
	}
	if first.Endpoint().TokenURL != "https://idp.example.com/realms/a/token" {
		t.Fatalf("unexpected token endpoint %q", first.Endpoint().TokenURL)
	}

	// Just inside the TTL the entry still serves; at the TTL it is refetched.
	now = now.Add(oidcProviderTTL - time.Second)
	if _, _, err := cache.get(ctx, "https://idp.example.com/realms/a", true); err != nil {
		t.Fatal(err)
	}
	if got := fake.calls.Load(); got != 1 {
		t.Fatalf("discovery calls before expiry = %d, want 1", got)
	}
	now = now.Add(time.Second)
	third, _, err := cache.get(ctx, "https://idp.example.com/realms/a", true)
	if err != nil {
		t.Fatal(err)
	}
	if third == first {
		t.Fatal("expected a fresh provider after the TTL")
	}
	if got := fake.calls.Load(); got != 2 {
		t.Fatalf("discovery calls after expiry = %d, want 2", got)
	}
}

func TestOIDCProviderCacheKeysOnIssuerAndVerifyTLS(t *testing.T) {
	now := time.Now()
	fake := &fakeDiscovery{}
	cache := newTestProviderCache(fake, &now)
	ctx := context.Background()
	verified, _, err := cache.get(ctx, "https://idp.example.com", true)
	if err != nil {
		t.Fatal(err)
	}
	// Same issuer with verification off must not share the provider: it would
	// otherwise keep fetching JWKS with the strict client after the operator
	// relaxed TLS, or the other way round.
	unverified, _, err := cache.get(ctx, "https://idp.example.com", false)
	if err != nil {
		t.Fatal(err)
	}
	if verified == unverified {
		t.Fatal("verify_tls must be part of the cache key")
	}
	if _, _, err := cache.get(ctx, "https://other.example.com", true); err != nil {
		t.Fatal(err)
	}
	if got := fake.calls.Load(); got != 3 {
		t.Fatalf("discovery calls = %d, want 3", got)
	}
}

func TestOIDCProviderCacheDoesNotKeepFailures(t *testing.T) {
	now := time.Now()
	fake := &fakeDiscovery{err: errors.New("503 Service Unavailable")}
	cache := newTestProviderCache(fake, &now)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if _, _, err := cache.get(ctx, "https://idp.example.com", true); err == nil || err.Error() != "503 Service Unavailable" {
			t.Fatalf("attempt %d: err = %v, want the discovery error", i, err)
		}
	}
	if got := fake.calls.Load(); got != 2 {
		t.Fatalf("discovery calls = %d, want 2 (a failure must not be cached)", got)
	}
	if len(cache.entries) != 0 {
		t.Fatalf("failed entries left in cache: %d", len(cache.entries))
	}
	// Once the provider recovers the next caller gets it.
	fake.err = nil
	if _, _, err := cache.get(ctx, "https://idp.example.com", true); err != nil {
		t.Fatal(err)
	}
}

func TestOIDCProviderCacheForgetForcesRediscovery(t *testing.T) {
	now := time.Now()
	fake := &fakeDiscovery{}
	cache := newTestProviderCache(fake, &now)
	ctx := context.Background()
	first, _, err := cache.get(ctx, "https://idp.example.com", true)
	if err != nil {
		t.Fatal(err)
	}
	cache.forget("https://idp.example.com", false) // different key: no effect
	if again, _, _ := cache.get(ctx, "https://idp.example.com", true); again != first {
		t.Fatal("forget with another verify_tls value must not evict the entry")
	}
	cache.forget("https://idp.example.com", true)
	second, _, err := cache.get(ctx, "https://idp.example.com", true)
	if err != nil {
		t.Fatal(err)
	}
	if second == first {
		t.Fatal("expected a new provider after forget")
	}
	if got := fake.calls.Load(); got != 2 {
		t.Fatalf("discovery calls = %d, want 2", got)
	}
}

func TestOIDCProviderCacheConcurrentCallersShareOneDiscovery(t *testing.T) {
	now := time.Now()
	fake := &fakeDiscovery{release: make(chan struct{})}
	cache := newTestProviderCache(fake, &now)
	const callers = 16
	var wg sync.WaitGroup
	results := make([]*oidc.Provider, callers)
	errs := make([]error, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], _, errs[i] = cache.get(context.Background(), "https://idp.example.com", true)
		}(i)
	}
	// Wait until the single discovery is in flight, then let it finish.
	deadline := time.Now().Add(5 * time.Second)
	for fake.calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	close(fake.release)
	wg.Wait()
	for i := range results {
		if errs[i] != nil {
			t.Fatalf("caller %d: %v", i, errs[i])
		}
		if results[i] != results[0] {
			t.Fatalf("caller %d received a different provider", i)
		}
	}
	if got := fake.calls.Load(); got != 1 {
		t.Fatalf("discovery calls = %d, want 1 for %d concurrent callers", got, callers)
	}
}

func TestOIDCProviderCacheCancelledCallerLeavesDiscoveryRunning(t *testing.T) {
	now := time.Now()
	fake := &fakeDiscovery{release: make(chan struct{})}
	cache := newTestProviderCache(fake, &now)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, _, err := cache.get(ctx, "https://idp.example.com", true)
		done <- err
	}()
	deadline := time.Now().Add(5 * time.Second)
	for fake.calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled caller err = %v, want context.Canceled", err)
	}
	// The discovery was not cancelled along with the caller: once it finishes
	// the next caller is served from the cache without another fetch.
	close(fake.release)
	if _, _, err := cache.get(context.Background(), "https://idp.example.com", true); err != nil {
		t.Fatal(err)
	}
	if got := fake.calls.Load(); got != 1 {
		t.Fatalf("discovery calls = %d, want 1", got)
	}
}

func TestOIDCProviderCacheNilFallsBackToDirectDiscovery(t *testing.T) {
	var cache *oidcProviderCache
	// A bare Service literal has no cache; the lookup must still reach the
	// provider (here: refused by the network layer, not by a nil dereference).
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, _, err := cache.get(ctx, "http://127.0.0.1:9", true); err == nil {
		t.Fatal("expected the uncached discovery to fail against a closed port")
	}
	cache.forget("http://127.0.0.1:9", true)
}

func TestOIDCLoginTokensAreDistinct(t *testing.T) {
	state, nonce, verifier, err := oidcLoginTokens()
	if err != nil {
		t.Fatal(err)
	}
	if state == "" || nonce == "" || verifier == "" || state == nonce || nonce == verifier {
		t.Fatalf("tokens must be non-empty and distinct: %q %q %q", state, nonce, verifier)
	}
}
