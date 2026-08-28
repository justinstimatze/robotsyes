// Package export implements pillar 2: bulk/structured export as a named
// cache-economics defense. A bot that would otherwise "bulk read" the
// long tail — forcing every hit through to the origin — gets a static
// bundle instead, built once per TTL and served from memory regardless of
// how many times it's downloaded.
package export

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/justinstimatze/robotsyes/internal/negotiate"
)

// Page is one bundled path's stripped content.
type Page struct {
	Path     string `json:"path"`
	Markdown string `json:"markdown"`
}

// Bundler fetches Paths from Origin, strips each via negotiate.Strip, and
// caches the resulting bundle for TTL before rebuilding.
type Bundler struct {
	Origin string
	Paths  []string
	TTL    time.Duration
	Client *http.Client

	mu      sync.Mutex
	cached  []Page
	builtAt time.Time
}

// NewBundler returns a Bundler with a bounded HTTP client for fetching
// origin pages during a rebuild.
func NewBundler(origin string, paths []string, ttl time.Duration) *Bundler {
	return &Bundler{
		Origin: origin,
		Paths:  paths,
		TTL:    ttl,
		Client: &http.Client{Timeout: 10 * time.Second},
	}
}

// Bundle returns the cached bundle, rebuilding it first if it's stale or
// has never been built.
func (b *Bundler) Bundle() ([]Page, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.cached != nil && time.Since(b.builtAt) < b.TTL {
		return b.cached, nil
	}

	pages := make([]Page, 0, len(b.Paths))
	for _, p := range b.Paths {
		md, err := b.fetchAndStrip(p)
		if err != nil {
			return nil, fmt.Errorf("bundling %s: %w", p, err)
		}
		pages = append(pages, Page{Path: p, Markdown: md})
	}
	b.cached = pages
	b.builtAt = time.Now()
	return pages, nil
}

func (b *Bundler) fetchAndStrip(path string) (string, error) {
	url := strings.TrimSuffix(b.Origin, "/") + path
	resp, err := b.Client.Get(url)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("origin returned %s", resp.Status)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return negotiate.Strip(string(body))
}

// ServeHTTP writes the bundle as newline-delimited JSON — one Page object
// per line — so a consumer can stream it without holding the whole thing
// in memory, and a partial download still yields whole records.
func (b *Bundler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	pages, err := b.Bundle()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	enc := json.NewEncoder(w)
	for _, p := range pages {
		if err := enc.Encode(p); err != nil {
			return
		}
	}
}
