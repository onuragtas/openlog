package cloudconnect

import (
	"sync"
	"time"
)

// tokenCache holds the OAuth access tokens of the Azure and GCP clients. Both providers hand out bearer
// tokens that live for an hour, and a poll makes one request per resource: without this a connection with 200
// resources would ask the identity endpoint 200 times and be rate-limited there rather than at the metrics API.
//
// The cache is per provider client, which is per collection run, plus a process-wide one in the collector, so
// consecutive polls of the same connection reuse a token until it is nearly expired.
type tokenCache struct {
	mu     sync.Mutex
	tokens map[string]cachedToken
}

type cachedToken struct {
	token   string
	expires time.Time
}

// tokenExpiryMargin is how long before the provider's expiry a token is treated as stale, so a request never
// travels with a token that expires in flight.
const tokenExpiryMargin = 2 * time.Minute

func newTokenCache() *tokenCache { return &tokenCache{tokens: map[string]cachedToken{}} }

// get returns a cached token for key that is still valid at now.
func (c *tokenCache) get(key string, now time.Time) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	t, ok := c.tokens[key]
	if !ok || !now.Add(tokenExpiryMargin).Before(t.expires) {
		return "", false
	}
	return t.token, true
}

// put stores a token valid for expiresIn from now.
func (c *tokenCache) put(key, token string, now time.Time, expiresIn time.Duration) {
	if token == "" || expiresIn <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tokens[key] = cachedToken{token: token, expires: now.Add(expiresIn)}
}
