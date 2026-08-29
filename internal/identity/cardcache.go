package identity

import (
	"container/list"
	"sync"
	"time"
)

// DefaultMaxCardCacheEntries is a reasonable cap for most deployments —
// pass a different value to NewCardFetcher if this proxy expects to see
// more distinct declared/verified bots than that within one TTL window.
const DefaultMaxCardCacheEntries = 10000

// cardCache is a fixed-capacity, TTL-expiring, LRU-evicting cache of
// fetched Signature Agent Cards, keyed by URL. It exists because that key
// comes straight from an unauthenticated request header
// (Signature-Agent): without a bound, a requester could force unlimited
// distinct cache entries — and unlimited outbound fetches — just by
// varying that header's query string on every request, no new
// connection or IP required. Eviction is LRU rather than
// insertion-order, so a card that's still actively in use stays cached
// even if it was one of the first ones ever inserted; only a genuinely
// idle entry gets pushed out to make room.
type cardCache struct {
	ttl        time.Duration
	maxEntries int

	mu    sync.Mutex
	order *list.List               // front = most recently used
	items map[string]*list.Element // -> *cardCacheEntry
}

type cardCacheEntry struct {
	key     string
	card    Card
	fetched time.Time
}

// newCardCache builds a cache capped at maxEntries. A non-positive
// maxEntries falls back to DefaultMaxCardCacheEntries rather than
// meaning "unbounded" — this cache exists specifically to close off an
// unbounded-growth vector, so silently allowing an uncapped cache back
// in through a zero-value default would defeat the point.
func newCardCache(maxEntries int, ttl time.Duration) *cardCache {
	if maxEntries <= 0 {
		maxEntries = DefaultMaxCardCacheEntries
	}
	return &cardCache{
		ttl:        ttl,
		maxEntries: maxEntries,
		order:      list.New(),
		items:      make(map[string]*list.Element),
	}
}

// get returns the cached card for key if present and not yet expired. A
// hit counts as use and moves the entry to the front.
func (c *cardCache) get(key string) (Card, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	el, ok := c.items[key]
	if !ok {
		return Card{}, false
	}
	entry := el.Value.(*cardCacheEntry)
	if time.Since(entry.fetched) >= c.ttl {
		c.removeElement(el)
		return Card{}, false
	}
	c.order.MoveToFront(el)
	return entry.card, true
}

// put inserts or refreshes key, evicting the least-recently-used entry
// first if the cache is at capacity and key is new.
func (c *cardCache) put(key string, card Card) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if el, ok := c.items[key]; ok {
		entry := el.Value.(*cardCacheEntry)
		entry.card = card
		entry.fetched = time.Now()
		c.order.MoveToFront(el)
		return
	}

	el := c.order.PushFront(&cardCacheEntry{key: key, card: card, fetched: time.Now()})
	c.items[key] = el
	// maxEntries is always positive: newCardCache never leaves it <= 0.
	if c.order.Len() > c.maxEntries {
		if oldest := c.order.Back(); oldest != nil {
			c.removeElement(oldest)
		}
	}
}

// removeElement drops el from both the list and the index. Callers must
// hold c.mu.
func (c *cardCache) removeElement(el *list.Element) {
	c.order.Remove(el)
	delete(c.items, el.Value.(*cardCacheEntry).key)
}

// len reports the current entry count.
func (c *cardCache) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.order.Len()
}
