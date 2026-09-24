package auth

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/hkjang/jupiq/internal/integration"
)

const (
	// oidcProviderTTL bounds how long a discovered provider is reused before the
	// Discovery document is fetched again. Endpoint URLs practically never move
	// under the same issuer, and signing-key rotation is handled by the
	// provider's own key set (it refetches JWKS whenever a token names an
	// unknown key), so the TTL only has to catch an operator relocating the
	// provider — for which forget() already gives an immediate path.
	oidcProviderTTL = 10 * time.Minute
	// oidcDiscoveryTimeout caps one Discovery fetch. It matches the HTTP
	// client's own timeout; the explicit deadline is what keeps a detached
	// discovery from outliving the request that started it by more than this.
	oidcDiscoveryTimeout = 10 * time.Second
)

// oidcProviderKey identifies one cached provider. verify_tls is part of the key
// because it selects the HTTP client the provider keeps for later JWKS fetches.
type oidcProviderKey struct {
	issuer    string
	verifyTLS bool
}

// oidcProviderEntry is one discovery attempt. provider, err and expires are
// written exactly once, before ready is closed, and read only after it.
type oidcProviderEntry struct {
	ready    chan struct{}
	client   *http.Client
	provider *oidc.Provider
	err      error
	expires  time.Time
}

// oidcProviderCache keeps discovered OIDC providers so that a login start and
// its callback no longer each fetch the Discovery document, and so that the
// callback's ID token verification reuses one JWKS cache across logins instead
// of downloading the keys for every sign-in. Before the cache, one browser
// login cost the identity provider three metadata requests on top of the
// token exchange; it now costs one every TTL per issuer.
//
// Concurrent callers for the same issuer join a single in-flight discovery
// rather than each starting their own, and a failed discovery is never kept,
// so the next caller retries at once.
type oidcProviderCache struct {
	mu        sync.Mutex
	entries   map[oidcProviderKey]*oidcProviderEntry
	now       func() time.Time
	discover  func(ctx context.Context, issuer string) (*oidc.Provider, error)
	newClient func(verifyTLS bool) *http.Client
}

func newOIDCProviderCache() *oidcProviderCache {
	return &oidcProviderCache{
		entries:   map[oidcProviderKey]*oidcProviderEntry{},
		now:       time.Now,
		discover:  oidc.NewProvider,
		newClient: func(verifyTLS bool) *http.Client { return integration.SafeHTTPClient(verifyTLS, oidcDiscoveryTimeout) },
	}
}

// get returns the provider for issuer together with the HTTP client it was
// discovered with, so the token exchange reuses the same connection pool.
// A nil cache falls back to an uncached discovery: a Service built as a bare
// literal keeps working, it just does not remember anything.
func (c *oidcProviderCache) get(ctx context.Context, issuer string, verifyTLS bool) (*oidc.Provider, *http.Client, error) {
	if c == nil {
		client := integration.SafeHTTPClient(verifyTLS, oidcDiscoveryTimeout)
		provider, err := oidc.NewProvider(oidc.ClientContext(ctx, client), issuer)
		return provider, client, err
	}
	key := oidcProviderKey{issuer: issuer, verifyTLS: verifyTLS}
	c.mu.Lock()
	c.evictExpiredLocked()
	entry := c.entries[key]
	if entry == nil {
		entry = &oidcProviderEntry{ready: make(chan struct{}), client: c.newClient(verifyTLS)}
		c.entries[key] = entry
		go c.resolve(key, entry)
	}
	c.mu.Unlock()
	select {
	case <-entry.ready:
	case <-ctx.Done():
		// The discovery keeps running on its own deadline so the entry is
		// ready for the next caller even though this one gave up.
		return nil, nil, ctx.Err()
	}
	if entry.err != nil {
		return nil, nil, entry.err
	}
	return entry.provider, entry.client, nil
}

// resolve performs the discovery for a freshly inserted entry. It runs on a
// context detached from any request: the first caller's browser closing the
// tab must not cancel a fetch that every other caller is waiting on.
func (c *oidcProviderCache) resolve(key oidcProviderKey, entry *oidcProviderEntry) {
	ctx, cancel := context.WithTimeout(oidc.ClientContext(context.Background(), entry.client), oidcDiscoveryTimeout)
	defer cancel()
	provider, err := c.discover(ctx, key.issuer)
	c.mu.Lock()
	if err != nil {
		if c.entries[key] == entry {
			delete(c.entries, key)
		}
	} else {
		entry.provider = provider
		entry.expires = c.now().Add(oidcProviderTTL)
	}
	entry.err = err
	c.mu.Unlock()
	close(entry.ready)
}

// forget drops the cached provider for issuer so the next caller discovers it
// again. Used when the provider stopped answering at the cached endpoints.
func (c *oidcProviderCache) forget(issuer string, verifyTLS bool) {
	if c == nil {
		return
	}
	c.mu.Lock()
	delete(c.entries, oidcProviderKey{issuer: issuer, verifyTLS: verifyTLS})
	c.mu.Unlock()
}

// evictExpiredLocked removes finished entries past their TTL. In-flight entries
// (ready not yet closed) are left alone so their waiters still get an answer.
func (c *oidcProviderCache) evictExpiredLocked() {
	now := c.now()
	for key, entry := range c.entries {
		select {
		case <-entry.ready:
			if !now.Before(entry.expires) {
				delete(c.entries, key)
			}
		default:
		}
	}
}
