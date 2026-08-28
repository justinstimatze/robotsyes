// Package ratelimit implements pillar 4: graduated, published rate
// limits. A well-behaved bot reads its ceiling from the discovery
// document instead of finding it by tripping a 403 — the limiter here
// just has to honor the numbers it publishes.
package ratelimit

import (
	"container/list"
	"sync"
	"time"
)

// Limit is one tier's published ceiling.
type Limit struct {
	RequestsPerMinute int `json:"requests_per_minute"`
}

// DefaultMaxBucketEntries is a reasonable cap for most deployments — pass
// a different value to New if this proxy expects to see more distinct
// tier+key pairs than that within one process lifetime.
const DefaultMaxBucketEntries = 100000

// Limiter is a per-tier, per-key token bucket, bounded to maxEntries
// distinct tier+key pairs with LRU eviction. Tiers are plain strings (not
// identity.Tier) so this package doesn't need to import identity — the
// caller maps an identity.Tier to a tier name when it calls Allow. The
// bound exists because key is often a bare client IP (see
// internal/proxy's RemoteAddr handling): without one, an address-rotating
// scraper grows this map for the life of the process, exactly the
// unbounded-cache shape identity.cardCache was built to close off —
// same fix, applied here for the same reason.
type Limiter struct {
	limits     map[string]Limit
	maxEntries int

	mu      sync.Mutex
	order   *list.List               // front = most recently used
	buckets map[string]*list.Element // -> *bucketEntry
}

type bucket struct {
	tokens     float64
	lastRefill time.Time
}

type bucketEntry struct {
	key    string
	bucket *bucket
}

// New builds a Limiter from a tier -> ceiling map, capped at maxEntries
// distinct tier+key pairs (least-recently-used evicted first past the
// cap). A non-positive maxEntries falls back to DefaultMaxBucketEntries
// rather than meaning "unbounded" — mirrors identity.newCardCache's same
// guard, for the same reason: this exists specifically to close off an
// unbounded-growth vector, so a zero-value default shouldn't reopen it.
//
// A tier with no entry in limits gets zero capacity (Allow always denies
// it) — callers should give every tier they use an explicit entry,
// including a nonzero floor for TierUnverified, or unrecognized bots get
// shut out entirely.
func New(limits map[string]Limit, maxEntries int) *Limiter {
	if maxEntries <= 0 {
		maxEntries = DefaultMaxBucketEntries
	}
	return &Limiter{
		limits:     limits,
		maxEntries: maxEntries,
		order:      list.New(),
		buckets:    make(map[string]*list.Element),
	}
}

// Published returns the tier -> ceiling map this Limiter enforces, for
// serving in the discovery document.
func (l *Limiter) Published() map[string]Limit {
	return l.limits
}

// Allow reports whether one more request is permitted for the given tier
// and client key (e.g. remote IP, or a verified AgentID once pillar 3 is
// real), consuming a token if so.
func (l *Limiter) Allow(tier, key string) bool {
	lim, ok := l.limits[tier]
	if !ok || lim.RequestsPerMinute <= 0 {
		return false
	}
	rate := float64(lim.RequestsPerMinute) / 60.0 // tokens per second
	capacity := float64(lim.RequestsPerMinute)

	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	fullKey := tier + "|" + key
	el, ok := l.buckets[fullKey]
	if !ok {
		bk := &bucket{tokens: capacity - 1, lastRefill: now}
		el := l.order.PushFront(&bucketEntry{key: fullKey, bucket: bk})
		l.buckets[fullKey] = el
		// maxEntries is always positive: New never leaves it <= 0.
		if l.order.Len() > l.maxEntries {
			if oldest := l.order.Back(); oldest != nil {
				l.removeElement(oldest)
			}
		}
		return true
	}
	l.order.MoveToFront(el)
	bk := el.Value.(*bucketEntry).bucket
	elapsed := now.Sub(bk.lastRefill).Seconds()
	bk.tokens += elapsed * rate
	if bk.tokens > capacity {
		bk.tokens = capacity
	}
	bk.lastRefill = now
	if bk.tokens < 1 {
		return false
	}
	bk.tokens--
	return true
}

// removeElement drops el from both the LRU list and the index. Callers
// must hold l.mu.
func (l *Limiter) removeElement(el *list.Element) {
	l.order.Remove(el)
	delete(l.buckets, el.Value.(*bucketEntry).key)
}

// len reports the current count of distinct tier+key buckets.
func (l *Limiter) len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.order.Len()
}
