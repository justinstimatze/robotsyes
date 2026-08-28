// Package ratelimit implements pillar 4: graduated, published rate
// limits. A well-behaved bot reads its ceiling from the discovery
// document instead of finding it by tripping a 403 — the limiter here
// just has to honor the numbers it publishes.
package ratelimit

import (
	"sync"
	"time"
)

// Limit is one tier's published ceiling.
type Limit struct {
	RequestsPerMinute int `json:"requests_per_minute"`
}

// Limiter is a per-tier, per-key token bucket. Tiers are plain strings
// (not identity.Tier) so this package doesn't need to import identity —
// the caller maps an identity.Tier to a tier name when it calls Allow.
type Limiter struct {
	limits map[string]Limit

	mu      sync.Mutex
	buckets map[string]*bucket
}

type bucket struct {
	tokens     float64
	lastRefill time.Time
}

// New builds a Limiter from a tier -> ceiling map. A tier with no entry
// gets zero capacity (Allow always denies it) — callers should give every
// tier they use an explicit entry, including a nonzero floor for
// TierUnverified, or unrecognized bots get shut out entirely.
func New(limits map[string]Limit) *Limiter {
	return &Limiter{
		limits:  limits,
		buckets: make(map[string]*bucket),
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
	bk, ok := l.buckets[tier+"|"+key]
	if !ok {
		bk = &bucket{tokens: capacity - 1, lastRefill: now}
		l.buckets[tier+"|"+key] = bk
		return true
	}
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
