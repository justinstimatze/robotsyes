package ratelimit

import (
	"testing"
	"time"
)

func TestAllowWithinCapacity(t *testing.T) {
	l := New(map[string]Limit{"declared": {RequestsPerMinute: 3}})
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
	l := New(map[string]Limit{"declared": {RequestsPerMinute: 10}})
	if l.Allow("verified", "1.2.3.4") {
		t.Fatal("tier with no configured limit should always be denied")
	}
}

func TestAllowKeysAreIndependent(t *testing.T) {
	l := New(map[string]Limit{"declared": {RequestsPerMinute: 1}})
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
	l := New(map[string]Limit{"declared": {RequestsPerMinute: 60}}) // 1/sec
	if !l.Allow("declared", "k") {
		t.Fatal("first request should be allowed")
	}
	// Manually rewind lastRefill to simulate elapsed time without sleeping.
	l.mu.Lock()
	l.buckets["declared|k"].lastRefill = time.Now().Add(-2 * time.Second)
	l.mu.Unlock()
	if !l.Allow("declared", "k") {
		t.Fatal("expected a refilled token after simulated elapsed time")
	}
}

func TestPublished(t *testing.T) {
	limits := map[string]Limit{"unverified": {RequestsPerMinute: 10}}
	l := New(limits)
	got := l.Published()
	if got["unverified"].RequestsPerMinute != 10 {
		t.Fatalf("Published() = %v, want unverified: 10", got)
	}
}
