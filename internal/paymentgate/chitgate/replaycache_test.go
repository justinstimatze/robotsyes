package chitgate

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestReplayCacheFirstReserveSucceeds(t *testing.T) {
	c := newReplayCache(10, time.Minute)
	if !c.reserve("n1") {
		t.Fatal("expected the first reserve of a nonce to succeed")
	}
	if n := c.len(); n != 1 {
		t.Errorf("len = %d, want 1", n)
	}
}

func TestReplayCacheConcurrentReserveOnlyOneSucceeds(t *testing.T) {
	c := newReplayCache(10, time.Minute)
	var wg sync.WaitGroup
	var successes int32
	var mu sync.Mutex

	for g := 0; g < 50; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if c.reserve("shared-nonce") {
				mu.Lock()
				successes++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if successes != 1 {
		t.Errorf("successes = %d, want exactly 1 — a nonce must never be reserved twice concurrently", successes)
	}
}

func TestReplayCacheReserveFailsWhileReserved(t *testing.T) {
	c := newReplayCache(10, time.Minute)
	c.reserve("n1")
	if c.reserve("n1") {
		t.Fatal("expected a second reserve of a still-pending nonce to fail")
	}
}

func TestReplayCacheReserveFailsAfterCommit(t *testing.T) {
	c := newReplayCache(10, time.Minute)
	c.reserve("n1")
	c.commit("n1")
	if c.reserve("n1") {
		t.Fatal("expected reserve to fail for an already-committed (spent) nonce")
	}
}

func TestReplayCacheReleaseAllowsRetry(t *testing.T) {
	c := newReplayCache(10, time.Minute)
	c.reserve("n1")
	c.release("n1")
	if !c.reserve("n1") {
		t.Fatal("expected reserve to succeed again after release (a legitimate retry after a transient failure)")
	}
}

func TestReplayCacheReleaseIsANoOpAfterCommit(t *testing.T) {
	c := newReplayCache(10, time.Minute)
	c.reserve("n1")
	c.commit("n1")
	c.release("n1") // must never undo a confirmed settlement
	if c.reserve("n1") {
		t.Fatal("release must not un-commit an already-settled nonce")
	}
}

func TestReplayCacheCommitAfterEvictionStillBlocksReserve(t *testing.T) {
	c := newReplayCache(1, time.Minute)
	c.reserve("n1")
	c.commit("n1")
	c.reserve("n2") // over capacity: evicts "n1"'s entry

	if _, ok := c.items["n1"]; ok {
		t.Fatal("test setup: expected n1 to have been evicted by n2's reserve")
	}
	// commit must re-insert on a miss, not silently drop the fact that
	// n1 was spent.
	c.commit("n1")
	if c.reserve("n1") {
		t.Fatal("expected reserve to still fail for n1 after commit re-inserted it post-eviction")
	}
}

func TestReplayCacheExpiredEntryAllowsReserve(t *testing.T) {
	c := newReplayCache(10, time.Minute)
	c.reserve("n1")
	c.commit("n1")

	// Backdate the entry instead of sleeping.
	el := c.items["n1"]
	el.Value.(*replayCacheEntry).seen = time.Now().Add(-2 * time.Minute)

	if !c.reserve("n1") {
		t.Fatal("expected an expired, previously-committed nonce to be reservable again")
	}
}

func TestReplayCacheEvictsLeastRecentlyUsedAtCapacity(t *testing.T) {
	c := newReplayCache(2, time.Minute)
	c.reserve("a")
	c.reserve("b")
	c.reserve("c") // over capacity: evicts "a" (oldest, never touched again)

	// Inspect membership directly rather than via reserve(), which
	// would insert a fresh entry for an absent key and trigger a second
	// eviction, corrupting the state being checked.
	if _, ok := c.items["a"]; ok {
		t.Error("expected \"a\" to have been evicted")
	}
	if _, ok := c.items["b"]; !ok {
		t.Error("expected \"b\" to still be cached")
	}
	if _, ok := c.items["c"]; !ok {
		t.Error("expected \"c\" to still be cached")
	}
	if n := c.len(); n != 2 {
		t.Errorf("len = %d, want 2", n)
	}
}

func TestReplayCacheDefaultsOnNonPositiveArgs(t *testing.T) {
	c := newReplayCache(0, 0)
	if c.maxEntries != DefaultMaxReplayCacheEntries {
		t.Errorf("maxEntries = %d, want default %d", c.maxEntries, DefaultMaxReplayCacheEntries)
	}
	if c.ttl != DefaultReplayCacheTTL {
		t.Errorf("ttl = %v, want default %v", c.ttl, DefaultReplayCacheTTL)
	}
}

func TestReplayCacheConcurrentAccess(t *testing.T) {
	c := newReplayCache(1000, time.Minute)
	var wg sync.WaitGroup
	var successes int32
	var mu sync.Mutex

	for g := 0; g < 20; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				nonce := fmt.Sprintf("n%d", i)
				if c.reserve(nonce) {
					mu.Lock()
					successes++
					mu.Unlock()
					c.commit(nonce)
				}
			}
		}()
	}
	wg.Wait()

	// 20 goroutines x 50 distinct nonces each = 1000 attempts, exactly
	// one successful reserve per distinct nonce (50 total).
	if successes != 50 {
		t.Errorf("successes = %d, want 50 (exactly one reserve per distinct nonce)", successes)
	}
}
