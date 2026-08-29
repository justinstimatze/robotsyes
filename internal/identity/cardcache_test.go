package identity

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestCardCacheGetMiss(t *testing.T) {
	c := newCardCache(10, time.Minute)
	if _, ok := c.get("missing"); ok {
		t.Fatal("expected a miss on an empty cache")
	}
}

func TestCardCachePutThenGet(t *testing.T) {
	c := newCardCache(10, time.Minute)
	want := Card{ClientName: "https://bot.example/"}
	c.put("k", want)

	got, ok := c.get("k")
	if !ok {
		t.Fatal("expected a hit after put")
	}
	if got.ClientName != want.ClientName {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if n := c.len(); n != 1 {
		t.Errorf("len = %d, want 1", n)
	}
}

func TestCardCacheExpiresAfterTTL(t *testing.T) {
	c := newCardCache(10, time.Minute)
	c.put("k", Card{ClientName: "a"})

	// Backdate the entry instead of sleeping.
	el := c.items["k"]
	el.Value.(*cardCacheEntry).fetched = time.Now().Add(-2 * time.Minute)

	if _, ok := c.get("k"); ok {
		t.Fatal("expected a miss on an expired entry")
	}
	if n := c.len(); n != 0 {
		t.Errorf("expired entry should be removed on access, len = %d, want 0", n)
	}
}

func TestCardCacheEvictsLeastRecentlyUsedAtCapacity(t *testing.T) {
	c := newCardCache(2, time.Minute)
	c.put("a", Card{ClientName: "a"})
	c.put("b", Card{ClientName: "b"})
	c.put("c", Card{ClientName: "c"}) // over capacity: evicts "a" (oldest, never touched)

	if _, ok := c.get("a"); ok {
		t.Error("expected \"a\" to have been evicted")
	}
	if _, ok := c.get("b"); !ok {
		t.Error("expected \"b\" to still be cached")
	}
	if _, ok := c.get("c"); !ok {
		t.Error("expected \"c\" to still be cached")
	}
	if n := c.len(); n != 2 {
		t.Errorf("len = %d, want 2 (capacity enforced)", n)
	}
}

// TestCardCacheHitPromotesEntry is the reason this is LRU and not a
// simpler insertion-order cache: an entry that's still being used
// shouldn't get evicted just because it happened to be inserted first.
func TestCardCacheHitPromotesEntry(t *testing.T) {
	c := newCardCache(2, time.Minute)
	c.put("a", Card{ClientName: "a"})
	c.put("b", Card{ClientName: "b"})

	if _, ok := c.get("a"); !ok {
		t.Fatal("expected \"a\" to be cached")
	} // "a" is now more recently used than "b"

	c.put("c", Card{ClientName: "c"}) // over capacity: should evict "b", not "a"

	if _, ok := c.get("a"); !ok {
		t.Error("expected \"a\" to survive eviction — it was used more recently than \"b\"")
	}
	if _, ok := c.get("b"); ok {
		t.Error("expected \"b\" to have been evicted as the least recently used")
	}
}

func TestCardCachePutOnExistingKeyUpdatesWithoutGrowing(t *testing.T) {
	c := newCardCache(10, time.Minute)
	c.put("k", Card{ClientName: "old"})
	c.put("k", Card{ClientName: "new"})

	got, ok := c.get("k")
	if !ok {
		t.Fatal("expected a hit")
	}
	if got.ClientName != "new" {
		t.Errorf("ClientName = %q, want %q (put should overwrite, not duplicate)", got.ClientName, "new")
	}
	if n := c.len(); n != 1 {
		t.Errorf("len = %d, want 1", n)
	}
}

// TestCardCacheNonPositiveMaxEntriesDefaultsSafely guards against a
// footgun: maxEntries<=0 must NOT mean "unbounded". This cache exists
// specifically to close off an unbounded-growth vector (see
// cardcache.go's doc comment), so a zero-value or negative maxEntries —
// whether from a bug or a lazy default — has to fall back to a safe cap,
// not silently reopen it.
func TestCardCacheNonPositiveMaxEntriesDefaultsSafely(t *testing.T) {
	for _, n := range []int{0, -1, -100} {
		c := newCardCache(n, time.Minute)
		if c.maxEntries != DefaultMaxCardCacheEntries {
			t.Errorf("newCardCache(%d, ...).maxEntries = %d, want %d", n, c.maxEntries, DefaultMaxCardCacheEntries)
		}
	}
}

// TestCardCacheConcurrentAccess gives the race detector real concurrent
// traffic against one cache instance — every other test in this file
// runs sequentially, so none of them actually exercised the mutex under
// contention despite go test -race passing on all of them. It also
// checks a correctness property that a race detector alone wouldn't
// catch: capacity must hold even under concurrent put()s, which requires
// each put's read-check-evict sequence to be fully serialized rather
// than merely data-race-free.
func TestCardCacheConcurrentAccess(t *testing.T) {
	const (
		maxEntries      = 50
		distinctKeys    = 30 // deliberately fewer than maxEntries, so gets and puts overlap keys
		goroutines      = 20
		opsPerGoroutine = 200
	)
	c := newCardCache(maxEntries, time.Minute)

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < opsPerGoroutine; i++ {
				key := fmt.Sprintf("key-%d", (g*opsPerGoroutine+i)%distinctKeys)
				c.put(key, Card{ClientName: key})
				c.get(key)
			}
		}(g)
	}
	wg.Wait()

	if n := c.len(); n > maxEntries {
		t.Errorf("len = %d, want <= %d (capacity must hold under concurrent access)", n, maxEntries)
	}
}
