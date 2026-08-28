package ratelimit

import (
	"testing"
	"time"
)

func TestAllowWithinCapacity(t *testing.T) {
	l := New(map[string]Limit{"declared": {RequestsPerMinute: 3}}, 0)
	for i := 0; i < 3; i++ {
		if !l.Allow("declared", "1.2.3.4") {
			t.Fatalf("request %d: expected allow within capacity", i+1)
		}
	}
	if l.Allow("declared", "1.2.3.4") {
		t.Fatal("4th immediate request should be denied, bucket exhausted")
	}
}

func TestAllowUnknownTierDenied(t *testing.T) {
	l := New(map[string]Limit{"declared": {RequestsPerMinute: 10}}, 0)
	if l.Allow("verified", "1.2.3.4") {
		t.Fatal("tier with no configured limit should always be denied")
	}
}

func TestAllowKeysAreIndependent(t *testing.T) {
	l := New(map[string]Limit{"declared": {RequestsPerMinute: 1}}, 0)
	if !l.Allow("declared", "client-a") {
		t.Fatal("client-a should get its first request")
	}
	if !l.Allow("declared", "client-b") {
		t.Fatal("client-b should get its own bucket, independent of client-a")
	}
	if l.Allow("declared", "client-a") {
		t.Fatal("client-a should be exhausted on its second immediate request")
	}
}

func TestAllowRefillsOverTime(t *testing.T) {
	l := New(map[string]Limit{"declared": {RequestsPerMinute: 60}}, 0) // 1/sec
	if !l.Allow("declared", "k") {
		t.Fatal("first request should be allowed")
	}
	// Manually rewind lastRefill to simulate elapsed time without sleeping.
	l.mu.Lock()
	l.buckets["declared|k"].Value.(*bucketEntry).bucket.lastRefill = time.Now().Add(-2 * time.Second)
	l.mu.Unlock()
	if !l.Allow("declared", "k") {
		t.Fatal("expected a refilled token after simulated elapsed time")
	}
}

func TestPublished(t *testing.T) {
	limits := map[string]Limit{"unverified": {RequestsPerMinute: 10}}
	l := New(limits, 0)
	got := l.Published()
	if got["unverified"].RequestsPerMinute != 10 {
		t.Fatalf("Published() = %v, want unverified: 10", got)
	}
}

// TestAllowEvictsLeastRecentlyUsedAtCapacity is the regression test for
// the bug three independent review passes converged on: an address-
// rotating client used to grow this map without bound for the life of
// the process.
func TestAllowEvictsLeastRecentlyUsedAtCapacity(t *testing.T) {
	l := New(map[string]Limit{"declared": {RequestsPerMinute: 10}}, 2)
	l.Allow("declared", "a")
	l.Allow("declared", "b")
	l.Allow("declared", "c") // over capacity: evicts "a" (oldest, never touched again)

	if n := l.len(); n != 2 {
		t.Fatalf("len = %d, want 2 (capacity enforced)", n)
	}
	l.mu.Lock()
	_, aStillCached := l.buckets["declared|a"]
	l.mu.Unlock()
	if aStillCached {
		t.Error("expected \"a\" to have been evicted")
	}
}

// TestAllowHitPromotesEntry is the reason eviction is LRU and not
// insertion-order: a key still in active use shouldn't get evicted just
// because it happened to be inserted first.
func TestAllowHitPromotesEntry(t *testing.T) {
	l := New(map[string]Limit{"declared": {RequestsPerMinute: 10}}, 2)
	l.Allow("declared", "a")
	l.Allow("declared", "b")
	l.Allow("declared", "a") // touch "a" again: "b" is now the least recently used
	l.Allow("declared", "c") // over capacity: evicts "b", not "a"

	l.mu.Lock()
	_, aStillCached := l.buckets["declared|a"]
	_, bStillCached := l.buckets["declared|b"]
	l.mu.Unlock()
	if !aStillCached {
		t.Error("expected \"a\" to still be cached: it was used more recently than \"b\"")
	}
	if bStillCached {
		t.Error("expected \"b\" to have been evicted")
	}
}
