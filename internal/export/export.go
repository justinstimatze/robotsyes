// Package export implements pillar 2: bulk/structured export as a named
// cache-economics defense. A bot that would otherwise "bulk read" the
// long tail — forcing every hit through to the origin — gets a static
// bundle instead, built once per TTL and served from memory regardless of
// how many times it's downloaded.
package export

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/justinstimatze/robotsyes/internal/httpx"
	"github.com/justinstimatze/robotsyes/internal/metrics"
	"github.com/justinstimatze/robotsyes/internal/negotiate"
)

// Page is one bundled path's stripped content.
type Page struct {
	Path     string `json:"path"`
	Markdown string `json:"markdown"`
}

// torrentInfoName is the multi-file torrent's suggested root directory —
// also, per BEP 19, the fixed first path segment every web-seed GET a
// torrent client issues will carry ahead of a file's own path segments.
// ServeTorrentSeed strips exactly this prefix back off. Lives here
// rather than in torrent.go since it's needed regardless of whether
// this binary was built with `-tags torrent` — the route still exists
// (and still 404s) in the default build.
const torrentInfoName = "pages"

// BundlerConfig configures a Bundler.
type BundlerConfig struct {
	// Origin is the upstream server every bundled path is fetched from.
	Origin string
	// Paths are fetched from Origin, stripped, and bundled directly. A
	// failure fetching any of these aborts the whole bundle — this is
	// the operator's own hand-picked list, so a broken entry is signal,
	// not noise.
	Paths []string
	// TTL is how long a built bundle is served before being rebuilt.
	TTL time.Duration
	// SitemapURL, if set, is fetched on every rebuild to discover
	// additional paths beyond Paths — see discoverSitemapPaths. This is
	// what lets bulk export cover the long tail without hand-enumerating
	// every path.
	SitemapURL string
	// MaxSitemapPages caps how many paths SitemapURL can contribute to
	// one bundle. Zero means DefaultMaxSitemapPages.
	MaxSitemapPages int
	// TorrentEnabled turns on building a real .torrent (see torrent.go)
	// alongside the manifest on every rebuild. TorrentSeedBaseURL must be
	// set when true — it's the full, already-joined web-seed URL
	// (operator's public_url plus proxy.go's torrentSeedPrefix); this
	// package never references that constant itself, since
	// internal/proxy owns every well-known-path constant.
	TorrentEnabled     bool
	TorrentSeedBaseURL string
	TorrentTrackers    []string
}

// Bundler fetches Paths (and, if configured, sitemap-discovered paths)
// from Origin, strips each via negotiate.Strip, and caches the resulting
// bundle for TTL before rebuilding.
type Bundler struct {
	Origin             string
	Paths              []string
	TTL                time.Duration
	SitemapURL         string
	MaxSitemapPages    int
	TorrentEnabled     bool
	TorrentSeedBaseURL string
	TorrentTrackers    []string
	Client             *http.Client

	mu              sync.Mutex
	cached          []Page
	cachedManifest  Manifest
	cachedTorrent   []byte
	cachedSeedIndex map[string]Page
	builtAt         time.Time
	building        bool
	buildDone       chan struct{} // non-nil and open while a build is in flight
	buildErr        error         // result of the most recent build that had a waiter
	builds          metrics.Counter
	buildFailures   metrics.Counter
}

// DefaultMaxSitemapPages caps how many sitemap-discovered paths one
// bundle includes, absent an explicit BundlerConfig.MaxSitemapPages.
const DefaultMaxSitemapPages = 1000

// NewBundler returns a Bundler with a bounded HTTP client for fetching
// origin pages during a rebuild.
func NewBundler(cfg BundlerConfig) *Bundler {
	maxPages := cfg.MaxSitemapPages
	if maxPages <= 0 {
		maxPages = DefaultMaxSitemapPages
	}
	return &Bundler{
		Origin:             cfg.Origin,
		Paths:              cfg.Paths,
		TTL:                cfg.TTL,
		SitemapURL:         cfg.SitemapURL,
		MaxSitemapPages:    maxPages,
		TorrentEnabled:     cfg.TorrentEnabled,
		TorrentSeedBaseURL: cfg.TorrentSeedBaseURL,
		TorrentTrackers:    cfg.TorrentTrackers,
		Client:             &http.Client{Timeout: 10 * time.Second},
	}
}

// Bundle returns the cached bundle. What happens depends on whether one
// has ever been built:
//
//   - Never built: the caller waits for a synchronous build — there's
//     nothing else to serve. Concurrent callers in this state join the
//     same build rather than each starting their own.
//   - Built and fresh (within TTL): returned immediately, no lock
//     contention with any in-flight rebuild.
//   - Built but stale: the stale bundle is returned immediately, and a
//     background rebuild starts if one isn't already running (deduped —
//     one rebuild in flight at a time, however many callers arrive while
//     it runs).
//
// The stale case is deliberate: a rebuild can now involve hundreds of
// sequential fetches once SitemapURL is set (a sitemap index's children,
// then every page each one names), and the whole point of bulk export is
// to be cheap and fast — a caller blocking behind however long that takes
// would defeat it. The tradeoff is that a rebuild failure (the origin is
// briefly down, say) no longer surfaces as an error to every caller
// during that window; it's logged and retried on the next request
// instead, and BuildFailures / the /metrics counter it backs is how an
// operator notices a rebuild has stopped succeeding. Only a first-ever
// build failure — where there's no stale bundle to fall back to —
// reaches the caller as an error, same as before this existed.
func (b *Bundler) Bundle() ([]Page, error) {
	b.mu.Lock()
	if b.cached != nil {
		cached := b.cached
		if time.Since(b.builtAt) >= b.TTL && !b.building {
			b.startBuildLocked(false)
		}
		b.mu.Unlock()
		return cached, nil
	}

	// Never built before: this caller has to wait. Join an in-flight
	// build if one's already running rather than starting a second.
	if b.building {
		done := b.buildDone
		b.mu.Unlock()
		<-done
		b.mu.Lock()
		defer b.mu.Unlock()
		return b.cached, b.buildErr
	}
	done := b.startBuildLocked(true)
	b.mu.Unlock()
	<-done
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.cached, b.buildErr
}

// Manifest returns the Manifest describing the currently cached bundle,
// triggering the same build-or-refresh behavior as Bundle (see its doc
// comment) since a manifest with no bundle behind it is meaningless. If
// the bundle has never successfully built, Bundle's own error is
// returned as-is and cachedManifest (still its zero value in that case)
// is never touched — a caller must see the real failure, not an
// empty-but-"successful" Manifest.
//
// A Manifest fetched via a separate request from the bundle can observe
// a slightly newer background-refresh cycle than a concurrently
// fetched bundle did. That's an inherent property of two independently
// fetchable resources over a TTL-refreshed cache, already true of the
// ndjson bundle today — not a new race, since cachedManifest is only
// ever written in the same locked commit as cached (see
// startBuildLocked), so whatever it currently holds always describes
// some bundle state that was real at that commit point.
func (b *Bundler) Manifest() (Manifest, error) {
	if _, err := b.Bundle(); err != nil {
		return Manifest{}, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.cachedManifest, nil
}

// ServeManifest writes the current Manifest as JSON. Unlike ServeHTTP's
// ndjson bundle, this is never gzipped — a manifest holds only metadata
// (paths, hashes, sizes), not page content, so it's small enough that
// plain encoding/json (matching serveDiscovery's style elsewhere in
// this project) is the right level of ceremony.
func (b *Bundler) ServeManifest(w http.ResponseWriter, r *http.Request) {
	m, err := b.Manifest()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(m)
}

// ServeTorrent writes the current .torrent, bencoded and ready for a
// BitTorrent client. Not found when the feature is disabled, or when it's
// enabled but no build has ever succeeded — never serves a stale or
// empty-but-"successful" .torrent.
func (b *Bundler) ServeTorrent(w http.ResponseWriter, r *http.Request) {
	if !b.TorrentEnabled {
		http.NotFound(w, r)
		return
	}
	if _, err := b.Bundle(); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	b.mu.Lock()
	encoded := b.cachedTorrent
	b.mu.Unlock()
	if encoded == nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/x-bittorrent")
	_, _ = w.Write(encoded)
}

// ServeTorrentSeed is the BEP-19 web-seed target: seedPath is the
// request path with proxy.go's torrentSeedPrefix already stripped (e.g.
// "pages/blog/some-post"). It serves the exact cached Page.Markdown
// bytes for the page that path names, unconditionally — no content
// negotiation — so what's served is always byte-for-byte what the
// .torrent's piece hashes were computed over. http.ServeContent handles
// Range requests natively, which is what a real BEP-19 client needs for
// partial-piece fetches.
func (b *Bundler) ServeTorrentSeed(w http.ResponseWriter, r *http.Request, seedPath string) {
	if !b.TorrentEnabled {
		http.NotFound(w, r)
		return
	}
	key := strings.TrimPrefix(seedPath, torrentInfoName+"/")
	b.mu.Lock()
	page, ok := b.cachedSeedIndex[key]
	modTime := b.builtAt
	b.mu.Unlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	http.ServeContent(w, r, key, modTime, strings.NewReader(page.Markdown))
}

// startBuildLocked starts a rebuild in its own goroutine and returns the
// channel that closes when it's done. Must be called with b.mu held;
// b.building must be false. recordErr controls whether the build's error
// is stashed in b.buildErr for a waiter to read — the background-refresh
// path has no waiter, so it only logs.
func (b *Bundler) startBuildLocked(recordErr bool) chan struct{} {
	b.building = true
	done := make(chan struct{})
	b.buildDone = done
	go func() {
		pages, err := b.rebuild()
		b.mu.Lock()
		b.building = false
		if recordErr {
			b.buildErr = err
		} else if err != nil {
			log.Printf("robotsyes: background bundle rebuild failed: %v", err)
		}
		if err == nil {
			b.cached = pages
			b.builtAt = time.Now()
			b.cachedManifest = buildManifest(pages, b.builtAt, b.TTL)
			if b.TorrentEnabled {
				b.buildTorrentLocked(pages)
			}
			b.builds.Inc()
		}
		b.mu.Unlock()
		close(done)
	}()
	return done
}

// buildTorrentLocked builds a fresh .torrent and seed index from pages
// and commits them, replacing b.cachedTorrent/b.cachedSeedIndex wholesale
// (never mutating the previous map — see buildTorrentInfo's doc comment
// for why that matters to a concurrent ServeTorrentSeed reader). Must be
// called with b.mu held. A build failure here is logged and leaves the
// previous cached torrent in place — the same "a bad rebuild doesn't
// blow away a good stale cache" property Bundle() already has for the
// ndjson bundle, not a new behavior.
func (b *Bundler) buildTorrentLocked(pages []Page) {
	encoded, seedIndex, err := buildTorrentInfo(pages, b.TorrentSeedBaseURL, b.TorrentTrackers)
	if err != nil {
		log.Printf("robotsyes: torrent build failed: %v", err)
		return
	}
	b.cachedTorrent = encoded
	b.cachedSeedIndex = seedIndex
}

// rebuild fetches every configured and sitemap-discovered path fresh. It
// runs without b.mu held — the whole point is to let readers proceed
// against the still-valid b.cached while this is in flight — so it must
// not touch any Bundler field except through metrics.Counter (which is
// its own atomic and safe to call unlocked). The caller (startBuildLocked)
// commits b.cached/b.builtAt itself, under lock, once this returns.
func (b *Bundler) rebuild() ([]Page, error) {
	pages := make([]Page, 0, len(b.Paths))
	for _, p := range b.Paths {
		md, err := b.fetchAndStrip(p)
		if err != nil {
			b.buildFailures.Inc()
			return nil, fmt.Errorf("bundling %s: %w", p, err)
		}
		pages = append(pages, Page{Path: p, Markdown: md})
	}

	if b.SitemapURL != "" {
		pages = append(pages, b.bundleSitemapPages(b.Paths)...)
	}

	return pages, nil
}

// bundleSitemapPages discovers paths from SitemapURL and fetches each,
// skipping (rather than aborting the whole bundle on) any individual
// failure — auto-discovered paths are expected to carry some noise (a
// stale sitemap entry, a page that started 404ing), and one bad entry
// shouldn't take down bulk export for everything else. seen is skipped so
// a path already covered by the explicit list isn't fetched twice.
func (b *Bundler) bundleSitemapPages(seen []string) []Page {
	already := make(map[string]bool, len(seen))
	for _, p := range seen {
		already[p] = true
	}

	discovered, err := b.discoverSitemapPaths()
	if err != nil {
		log.Printf("robotsyes: sitemap discovery failed for %s: %v", b.SitemapURL, err)
		return nil
	}

	toFetch := make([]string, 0, len(discovered))
	for _, p := range discovered {
		if already[p] {
			continue
		}
		already[p] = true
		toFetch = append(toFetch, p)
	}
	return b.fetchPagesConcurrently(toFetch)
}

// maxConcurrentSitemapFetches bounds how many origin fetches
// fetchPagesConcurrently runs at once. Fetching strictly one at a time
// means a rebuild's wall time is roughly len(paths) x origin latency —
// at MaxSitemapPages' default of 1000 and a realistic 100-300ms/page,
// that's 100-300s, which can exceed the bundle's own TTL. A bound (not
// unlimited fan-out) keeps this from turning into a self-inflicted
// thundering herd against the operator's own origin.
const maxConcurrentSitemapFetches = 10

// fetchPagesConcurrently fetches paths with up to
// maxConcurrentSitemapFetches requests in flight, preserving the
// caller's ordering in the result and the same skip-and-log-on-failure
// behavior a sequential loop would have — one bad path is dropped, not
// escalated to abort the rest. Each path's result is written to its own
// index in a preallocated slice rather than appended from multiple
// goroutines, so no synchronization is needed beyond the semaphore
// itself.
func (b *Bundler) fetchPagesConcurrently(paths []string) []Page {
	type result struct {
		page Page
		ok   bool
	}
	results := make([]result, len(paths))
	sem := make(chan struct{}, maxConcurrentSitemapFetches)
	var wg sync.WaitGroup
	for i, p := range paths {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, p string) {
			defer wg.Done()
			defer func() { <-sem }()
			md, err := b.fetchAndStrip(p)
			if err != nil {
				log.Printf("robotsyes: skipping sitemap path %s: %v", p, err)
				return
			}
			results[i] = result{page: Page{Path: p, Markdown: md}, ok: true}
		}(i, p)
	}
	wg.Wait()

	pages := make([]Page, 0, len(paths))
	for _, r := range results {
		if r.ok {
			pages = append(pages, r.page)
		}
	}
	return pages
}

// maxPageResponseBytes bounds how much of an origin page fetchAndStrip will
// read. Bundle() holds every fetched page in memory for the whole cache
// TTL, so one runaway response — a misconfigured origin, a redirect loop
// into unbounded content — would otherwise sit there for as long as the
// cache does, multiplied by however many paths are bundled.
const maxPageResponseBytes = 10 * 1024 * 1024

func (b *Bundler) fetchAndStrip(path string) (string, error) {
	pageURL := strings.TrimSuffix(b.Origin, "/") + path
	body, err := httpx.GetBounded(b.Client, pageURL, maxPageResponseBytes)
	if err != nil {
		return "", err
	}
	return negotiate.Strip(string(body))
}

// maxSitemapResponseBytes bounds a single sitemap (or sitemap index)
// fetch. The sitemap protocol's own ceiling is 50,000 URLs / 50MB
// uncompressed per file; this is more conservative since the whole body
// is held in memory just to extract <loc> text.
const maxSitemapResponseBytes = 5 * 1024 * 1024

// maxSitemapIndexChildren bounds how many child sitemaps a sitemapindex
// fetch will follow, independent of MaxSitemapPages — otherwise an index
// listing thousands of (possibly near-empty) children means thousands of
// outbound fetches before the page budget ever kicks in.
const maxSitemapIndexChildren = 50

type sitemapLoc struct {
	Loc string `xml:"loc"`
}

// maxSitemapEntriesPerFile bounds how many <url> or <sitemap> entries a
// single fetch will decode, independent of maxSitemapResponseBytes — a
// response near that byte cap but packed with minimal entries could
// otherwise decode into tens of thousands of Go strings in memory before
// MaxSitemapPages ever gets a chance to trim the result down.
// decodeSitemapLocs stops reading once it hits this, rather than
// decoding the whole document and truncating afterward.
const maxSitemapEntriesPerFile = 20000

// decodeSitemapLocs streams body and returns the <loc> text of every
// top-level occurrence of elementName ("url" for a <urlset>, "sitemap"
// for a <sitemapindex>) — both wrap a single <loc> child, so one function
// covers both document shapes. Stops early at maxSitemapEntriesPerFile;
// a malformed element is skipped rather than aborting the whole scan.
func decodeSitemapLocs(body []byte, elementName string) []string {
	dec := xml.NewDecoder(bytes.NewReader(body))
	var locs []string
	for len(locs) < maxSitemapEntriesPerFile {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		se, ok := tok.(xml.StartElement)
		if !ok || se.Name.Local != elementName {
			continue
		}
		var entry sitemapLoc
		if err := dec.DecodeElement(&entry, &se); err != nil {
			continue
		}
		if entry.Loc != "" {
			locs = append(locs, entry.Loc)
		}
	}
	return locs
}

// discoverSitemapPaths fetches b.SitemapURL and returns the paths
// (relative to b.Origin) it names, up to b.MaxSitemapPages. A sitemap
// index is followed one level (its child sitemaps are fetched too, up to
// maxSitemapIndexChildren of them) — real large sites publish an index of
// per-section sitemaps rather than one flat file, and that's exactly the
// long-tail case this feature exists for.
func (b *Bundler) discoverSitemapPaths() ([]string, error) {
	body, err := httpx.GetBounded(b.Client, b.SitemapURL, maxSitemapResponseBytes)
	if err != nil {
		return nil, err
	}
	isIndex, err := isSitemapIndex(body)
	if err != nil {
		return nil, fmt.Errorf("parsing sitemap: %w", err)
	}

	var paths []string
	if !isIndex {
		paths, err = b.pathsFromURLSet(body)
		if err != nil {
			return nil, fmt.Errorf("parsing sitemap: %w", err)
		}
	} else {
		paths = b.pathsFromSitemapIndex(body)
	}

	if len(paths) > b.MaxSitemapPages {
		paths = paths[:b.MaxSitemapPages]
	}
	return paths, nil
}

// pathsFromSitemapIndex fetches each child sitemap named in body (a
// sitemapindex document) and aggregates their paths, skipping any child
// that fails to fetch or parse rather than aborting the whole discovery.
func (b *Bundler) pathsFromSitemapIndex(body []byte) []string {
	children := decodeSitemapLocs(body, "sitemap")
	if len(children) > maxSitemapIndexChildren {
		children = children[:maxSitemapIndexChildren]
	}

	var paths []string
	for _, loc := range children {
		if len(paths) >= b.MaxSitemapPages {
			break
		}
		childBody, err := httpx.GetBounded(b.Client, loc, maxSitemapResponseBytes)
		if err != nil {
			log.Printf("robotsyes: skipping child sitemap %s: %v", loc, err)
			continue
		}
		childPaths, err := b.pathsFromURLSet(childBody)
		if err != nil {
			log.Printf("robotsyes: skipping child sitemap %s: %v", loc, err)
			continue
		}
		paths = append(paths, childPaths...)
	}
	return paths
}

// pathsFromURLSet parses a <urlset> sitemap body and returns the path
// portion of each <loc> that shares b.Origin's host — an entry pointing
// elsewhere (a cross-domain canonical, a copy-paste mistake in the
// sitemap) is skipped rather than bundled, since Origin is the only host
// this proxy actually fronts.
func (b *Bundler) pathsFromURLSet(body []byte) ([]string, error) {
	origin, err := url.Parse(b.Origin)
	if err != nil {
		return nil, err
	}

	locs := decodeSitemapLocs(body, "url")
	paths := make([]string, 0, len(locs))
	for _, rawLoc := range locs {
		loc, err := url.Parse(rawLoc)
		if err != nil || !sameHost(loc, origin) {
			continue
		}
		p := loc.Path
		if p == "" {
			p = "/"
		}
		paths = append(paths, p)
	}
	return paths, nil
}

// isSitemapIndex reports whether body's root element is <sitemapindex>
// (a list of child sitemaps) rather than <urlset> (a flat list of pages).
func isSitemapIndex(body []byte) (bool, error) {
	dec := xml.NewDecoder(bytes.NewReader(body))
	for {
		tok, err := dec.Token()
		if err != nil {
			return false, err
		}
		if se, ok := tok.(xml.StartElement); ok {
			return se.Name.Local == "sitemapindex", nil
		}
	}
}

// sameHost reports whether loc and origin name the same host: compared
// case-insensitively (DNS names aren't case-sensitive, but net/url
// doesn't normalize case on Parse), and ignoring an explicit port that's
// just the URL's own scheme's default (":443" written out on an https://
// URL, ":80" on http://) — a sitemap generator that writes out a
// redundant default port shouldn't cause an otherwise-legitimate
// same-site entry to be silently skipped.
func sameHost(loc, origin *url.URL) bool {
	return strings.EqualFold(hostWithoutDefaultPort(loc), hostWithoutDefaultPort(origin))
}

func hostWithoutDefaultPort(u *url.URL) string {
	host, port := u.Hostname(), u.Port()
	if port == "" || isDefaultPort(u.Scheme, port) {
		return host
	}
	return host + ":" + port
}

var defaultPortByScheme = map[string]string{"http": "80", "https": "443"}

func isDefaultPort(scheme, port string) bool {
	return defaultPortByScheme[scheme] == port
}

// Builds reports how many times Bundle has actually rebuilt the bundle
// (as opposed to serving it from cache) — for /metrics.
func (b *Bundler) Builds() int64 { return b.builds.Value() }

// BuildFailures reports how many rebuild attempts have failed. Since a
// stale-but-cached bundle now keeps being served while a failing rebuild
// retries quietly in the background (see Bundle's doc comment), this is
// how an operator notices a rebuild has stopped succeeding — Builds
// alone would just look like it stalled, not that it's actively failing.
func (b *Bundler) BuildFailures() int64 { return b.buildFailures.Value() }

// PageCount reports how many pages are in the currently cached bundle.
func (b *Bundler) PageCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.cached)
}

// ServeHTTP writes the bundle as newline-delimited JSON — one Page object
// per line — so a consumer can stream it without holding the whole thing
// in memory, and a partial download still yields whole records. Gzips the
// response when the requester's Accept-Encoding allows it: the whole
// point of bulk export is a bandwidth argument, and ndjson full of
// repeated JSON keys and HTML-derived markdown compresses well.
func (b *Bundler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	pages, err := b.Bundle()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Vary", "Accept-Encoding")

	out := io.Writer(w)
	if acceptsGzip(r.Header.Get("Accept-Encoding")) {
		w.Header().Set("Content-Encoding", "gzip")
		gz := gzip.NewWriter(w)
		defer func() { _ = gz.Close() }()
		out = gz
	}

	enc := json.NewEncoder(out)
	for _, p := range pages {
		if err := enc.Encode(p); err != nil {
			return
		}
	}
}

// acceptsGzip reports whether the Accept-Encoding header allows gzip.
// Bulk export only ever has two representations (gzip or not), so this
// only needs a yes/no answer — not full content-coding negotiation across
// multiple competing encodings — but it does respect an explicit "q=0"
// (RFC 7231's way of saying "never this one"), since that's a real,
// if rare, signal a client can send.
func acceptsGzip(acceptEncoding string) bool {
	for _, part := range strings.Split(acceptEncoding, ",") {
		coding, qRaw, hasParam := strings.Cut(strings.TrimSpace(part), ";")
		if !strings.EqualFold(strings.TrimSpace(coding), "gzip") {
			continue
		}
		if !hasParam {
			return true
		}
		q, ok := strings.CutPrefix(strings.TrimSpace(qRaw), "q=")
		if !ok {
			return true
		}
		v, err := strconv.ParseFloat(q, 64)
		return err != nil || v > 0
	}
	return false
}
