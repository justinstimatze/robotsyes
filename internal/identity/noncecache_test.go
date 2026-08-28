package identity

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestNonceCacheFirstSeenIsNotAReplay(t *testing.T) {
	c := newNonceCache(10, time.Minute)
	if c.seen("n1") {
		t.Fatal("expected the first sighting of a nonce to report false")
	}
	if n := c.len(); n != 1 {
		t.Errorf("len = %d, want 1", n)
	}
}

func TestNonceCacheSecondSightingIsAReplay(t *testing.T) {
	c := newNonceCache(10, time.Minute)
	c.seen("n1")
	if !c.seen("n1") {
		t.Fatal("expected the second sighting of the same nonce to report true (replay)")
	}
}

func TestNonceCacheExpiredEntryIsNotAReplay(t *testing.T) {
	c := newNonceCache(10, time.Minute)
	c.seen("n1")

	// Backdate the entry instead of sleeping.
	el := c.items["n1"]
	el.Value.(*nonceCacheEntry).seen = time.Now().Add(-2 * time.Minute)

	if c.seen("n1") {
		t.Fatal("expected an expired nonce to be treated as unseen, not a replay")
	}
	if n := c.len(); n != 1 {
		t.Errorf("len = %d, want 1 (the fresh re-insertion)", n)
	}
}

func TestNonceCacheEvictsLeastRecentlyUsedAtCapacity(t *testing.T) {
	c := newNonceCache(2, time.Minute)
	c.seen("a")
	c.seen("b")
	c.seen("c") // over capacity: evicts "a" (oldest, never touched again)

	// Inspect membership directly rather than via seen(), which is a
	// check-AND-set — calling it on an absent key would insert it and
	// trigger a second eviction, corrupting the very state being checked.
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

func TestNonceCacheConcurrentAccess(t *testing.T) {
	c := newNonceCache(1000, time.Minute)
	var wg sync.WaitGroup
	var replays int32
	var mu sync.Mutex

	// Every goroutine checks the same fixed set of nonces repeatedly;
	// exactly one caller per nonce should ever see seen()==false.
	for g := 0; g < 20; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				nonce := fmt.Sprintf("n%d", i)
				if c.seen(nonce) {
					mu.Lock()
					replays++
					mu.Unlock()
				}
			}
		}()
	}
	wg.Wait()

	// 20 goroutines x 50 distinct nonces each = 1000 checks total, 50
	// of which (one per nonce) must be the legitimate first sighting;
	// the rest are correctly-detected replays.
	if want := 20*50 - 50; int(replays) != want {
		t.Errorf("replays = %d, want %d", replays, want)
	}
}
