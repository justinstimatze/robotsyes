package chitgate

import (
	"container/list"
	"sync"
	"time"
)

// DefaultMaxReplayCacheEntries mirrors internal/identity's
// DefaultMaxNonceCacheEntries — a reasonable cap for most deployments.
const DefaultMaxReplayCacheEntries = 10000

// DefaultReplayCacheTTL matches x402MaxTimeoutSeconds, the window a
// challenge itself advertises as valid: once a credential's own validity
// window has passed, chit (or the chain) reject it anyway, so
// remembering it longer than that buys only a little safety margin.
const DefaultReplayCacheTTL = 5 * time.Minute

// replayCache is a fixed-capacity, TTL-expiring, LRU-evicting record of
// x402 authorization nonces already reserved or settled, mirroring
// internal/identity's nonceCache (see that package's noncecache.go) —
// but with a reserve/commit/release lifecycle instead of a single
// check-and-set. A signature's freshness nonce (pillar 3) is checked
// against a fast, synchronous, in-process operation, so a bare
// check-and-set has no window for a race. Settling a payment is a slow,
// fallible network call: without a claim step BEFORE that call starts,
// two concurrent requests presenting the same credential could both pass
// a naive "not yet seen" check and both attempt settlement.
//
// This is robots.yes's own, independently-verifiable guarantee that it
// will never serve two requests off one settled payment. It does not
// depend on whatever idempotency semantics the upstream authorization
// server happens to implement for an already-consumed authorization —
// see chitgate.go's RequirePayment for why that can't be assumed.
type replayCache struct {
	ttl        time.Duration
	maxEntries int

	mu    sync.Mutex
	order *list.List               // front = most recently used
	items map[string]*list.Element // -> *replayCacheEntry
}

type replayCacheEntry struct {
	nonce     string
	seen      time.Time
	committed bool
}

// newReplayCache builds a cache capped at maxEntries. Non-positive
// maxEntries or ttl fall back to the package defaults, for the same
// reason nonceCache's constructor does: this cache exists specifically
// to close off an unbounded-growth vector, so a zero-value default
// silently allowing unbounded growth (or an unbounded TTL) would defeat
// the point.
func newReplayCache(maxEntries int, ttl time.Duration) *replayCache {
	if maxEntries <= 0 {
		maxEntries = DefaultMaxReplayCacheEntries
	}
	if ttl <= 0 {
		ttl = DefaultReplayCacheTTL
	}
	return &replayCache{
		ttl:        ttl,
		maxEntries: maxEntries,
		order:      list.New(),
		items:      make(map[string]*list.Element),
	}
}

// reserve atomically claims nonce for an in-flight settlement attempt.
// Returns false if nonce is already reserved (a concurrent attempt is in
// flight) or already committed (already spent), and not yet expired —
// the caller must not attempt settlement in that case. Every reserve
// that returns true must be followed by exactly one commit (once
// settlement is confirmed) or release (on any failure path), never both,
// so a legitimate retry after a transient error isn't permanently locked
// out by this cache's own bookkeeping.
func (c *replayCache) reserve(nonce string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	if el, ok := c.items[nonce]; ok {
		entry := el.Value.(*replayCacheEntry)
		if time.Since(entry.seen) < c.ttl {
			c.order.MoveToFront(el)
			return false
		}
		c.removeElement(el)
	}

	el := c.order.PushFront(&replayCacheEntry{nonce: nonce, seen: time.Now()})
	c.items[nonce] = el
	c.evictIfOverCapacity()
	return true
}

// commit marks a reserved nonce as permanently spent (within the
// cache's TTL). Safe to call even if the entry was evicted between
// reserve and commit — it re-inserts a committed entry, since losing the
// "this nonce is spent" fact to eviction pressure would defeat the
// point of this cache.
func (c *replayCache) commit(nonce string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if el, ok := c.items[nonce]; ok {
		el.Value.(*replayCacheEntry).committed = true
		c.order.MoveToFront(el)
		return
	}
	el := c.order.PushFront(&replayCacheEntry{nonce: nonce, seen: time.Now(), committed: true})
	c.items[nonce] = el
	c.evictIfOverCapacity()
}

// release undoes a reservation that did not end in a confirmed
// settlement, so a legitimate retry of the same credential (e.g. after a
// transient error talking to the settlement endpoint) is not
// permanently locked out by robots.yes's own bookkeeping. A no-op if
// nonce was already committed (release must never undo a confirmed
// settlement) or is no longer present.
func (c *replayCache) release(nonce string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if el, ok := c.items[nonce]; ok {
		if !el.Value.(*replayCacheEntry).committed {
			c.removeElement(el)
		}
	}
}

// evictIfOverCapacity drops the least-recently-used entry once the
// cache is over capacity. Callers must hold c.mu and have just inserted
// at the front.
func (c *replayCache) evictIfOverCapacity() {
	// maxEntries is always positive: newReplayCache never leaves it <= 0.
	if c.order.Len() > c.maxEntries {
		if oldest := c.order.Back(); oldest != nil {
			c.removeElement(oldest)
		}
	}
}

// removeElement drops el from both the list and the index. Callers must
// hold c.mu.
func (c *replayCache) removeElement(el *list.Element) {
	c.order.Remove(el)
	delete(c.items, el.Value.(*replayCacheEntry).nonce)
}

// len reports the current entry count.
func (c *replayCache) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.order.Len()
}
