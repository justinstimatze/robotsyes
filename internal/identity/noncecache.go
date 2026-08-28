package identity

import (
	"container/list"
	"sync"
	"time"
)

// DefaultMaxNonceCacheEntries mirrors DefaultMaxCardCacheEntries — a
// reasonable cap for most deployments. Pass a different value to
// newNonceCache if this proxy expects more distinct in-flight signed
// requests than that within one validity window.
const DefaultMaxNonceCacheEntries = 10000

// nonceCache is a fixed-capacity, TTL-expiring, LRU-evicting record of
// nonces already seen, keyed by the nonce string itself. It exists because
// WebBotAuth's replay defense is the nonce, not the request path: without
// this, a captured Signature-Input/Signature pair could be replayed
// verbatim against any method/path on the host until its expires
// timestamp, since the signature no longer binds to one specific request
// (see the host-only scope decision in signatureBase).
type nonceCache struct {
	ttl        time.Duration
	maxEntries int

	mu    sync.Mutex
	order *list.List               // front = most recently used
	items map[string]*list.Element // -> *nonceCacheEntry
}

type nonceCacheEntry struct {
	nonce string
	seen  time.Time
}

// newNonceCache builds a cache capped at maxEntries. A non-positive
// maxEntries falls back to DefaultMaxNonceCacheEntries for the same reason
// newCardCache does: this cache exists specifically to close off an
// unbounded-growth vector (an attacker could otherwise force unlimited
// entries by varying the nonce on every replayed request), so a zero-value
// default silently allowing unbounded growth would defeat the point.
func newNonceCache(maxEntries int, ttl time.Duration) *nonceCache {
	if maxEntries <= 0 {
		maxEntries = DefaultMaxNonceCacheEntries
	}
	return &nonceCache{
		ttl:        ttl,
		maxEntries: maxEntries,
		order:      list.New(),
		items:      make(map[string]*list.Element),
	}
}

// seen checks whether nonce has already been recorded (and not yet
// expired), then records it — a single locked check-and-set, so two
// concurrent requests racing to replay the same nonce can't both observe
// "not seen yet". Returns true if this nonce was already present, in which
// case the caller must reject the request as a replay.
func (c *nonceCache) seen(nonce string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	if el, ok := c.items[nonce]; ok {
		entry := el.Value.(*nonceCacheEntry)
		if time.Since(entry.seen) < c.ttl {
			c.order.MoveToFront(el)
			return true
		}
		c.removeElement(el)
	}

	el := c.order.PushFront(&nonceCacheEntry{nonce: nonce, seen: time.Now()})
	c.items[nonce] = el
	// maxEntries is always positive: newNonceCache never leaves it <= 0.
	if c.order.Len() > c.maxEntries {
		if oldest := c.order.Back(); oldest != nil {
			c.removeElement(oldest)
		}
	}
	return false
}

// removeElement drops el from both the list and the index. Callers must
// hold c.mu.
func (c *nonceCache) removeElement(el *list.Element) {
	c.order.Remove(el)
	delete(c.items, el.Value.(*nonceCacheEntry).nonce)
}

// len reports the current entry count.
func (c *nonceCache) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.order.Len()
}
