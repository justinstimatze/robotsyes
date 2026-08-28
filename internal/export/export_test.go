package export

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestBundleRejectsOversizedOriginResponse is the regression test for the
// body-size cap on fetchAndStrip: Bundle() must fail, not hang onto an
// unbounded page for the life of the cache TTL, when the origin returns
// more than maxPageResponseBytes for one of the bundled paths.
func TestBundleRejectsOversizedOriginResponse(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/big", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(strings.Repeat("a", maxPageResponseBytes+1)))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	b := NewBundler(BundlerConfig{Origin: srv.URL, Paths: []string{"/big"}, TTL: time.Minute})
	if _, err := b.Bundle(); err == nil {
		t.Fatal("expected an error for an oversized origin response, got none")
	} else if !strings.Contains(err.Error(), "exceeds") {
		t.Errorf("error = %q, want it to mention the size limit", err.Error())
	}
}

// TestBundleAcceptsResponseAtSizeLimit is the boundary check from the
// other side: a response of exactly maxPageResponseBytes must still be
// bundled. Guards the same off-by-one class as
// TestCardFetcherAcceptsBodyAtSizeLimit in internal/identity.
func TestBundleAcceptsResponseAtSizeLimit(t *testing.T) {
	body := strings.Repeat("a", maxPageResponseBytes)
	mux := http.NewServeMux()
	mux.HandleFunc("/exact", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(body))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	b := NewBundler(BundlerConfig{Origin: srv.URL, Paths: []string{"/exact"}, TTL: time.Minute})
	pages, err := b.Bundle()
	if err != nil {
		t.Fatalf("expected a response exactly at the limit to be accepted, got error: %v", err)
	}
	if len(pages) != 1 {
		t.Fatalf("got %d pages, want 1", len(pages))
	}
}

func TestServeHTTPGzipsWhenAccepted(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/a", pageHandler("A"))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	b := NewBundler(BundlerConfig{Origin: srv.URL, Paths: []string{"/a"}, TTL: time.Minute})
	req := httptest.NewRequest(http.MethodGet, "/export", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	w := httptest.NewRecorder()
	b.ServeHTTP(w, req)

	if got := w.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want %q", got, "gzip")
	}
	zr, err := gzip.NewReader(w.Body)
	if err != nil {
		t.Fatalf("response wasn't valid gzip: %v", err)
	}
	defer zr.Close()
	decoded, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("decompressing body: %v", err)
	}
	var page Page
	if err := json.Unmarshal(decoded[:bytes.IndexByte(decoded, '\n')], &page); err != nil {
		t.Fatalf("decoded body isn't valid ndjson: %v (%q)", err, decoded)
	}
	if page.Path != "/a" {
		t.Errorf("page.Path = %q, want %q", page.Path, "/a")
	}
}

func TestServeHTTPPlainWithoutAcceptEncoding(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/a", pageHandler("A"))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	b := NewBundler(BundlerConfig{Origin: srv.URL, Paths: []string{"/a"}, TTL: time.Minute})
	req := httptest.NewRequest(http.MethodGet, "/export", nil)
	w := httptest.NewRecorder()
	b.ServeHTTP(w, req)

	if got := w.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("Content-Encoding = %q, want none", got)
	}
	var page Page
	if err := json.Unmarshal(w.Body.Bytes()[:bytes.IndexByte(w.Body.Bytes(), '\n')], &page); err != nil {
		t.Fatalf("body isn't valid ndjson: %v", err)
	}
}

func TestAcceptsGzipRespectsQZero(t *testing.T) {
	if acceptsGzip("gzip;q=0") {
		t.Error("acceptsGzip(\"gzip;q=0\") = true, want false (explicit q=0 means never)")
	}
	if !acceptsGzip("gzip") {
		t.Error("acceptsGzip(\"gzip\") = false, want true")
	}
	if !acceptsGzip("deflate, gzip;q=0.5") {
		t.Error("acceptsGzip(\"deflate, gzip;q=0.5\") = false, want true")
	}
	if acceptsGzip("deflate") {
		t.Error("acceptsGzip(\"deflate\") = true, want false")
	}
	if acceptsGzip("") {
		t.Error("acceptsGzip(\"\") = true, want false")
	}
}

func sitemapXML(locs ...string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">`)
	for _, loc := range locs {
		b.WriteString("<url><loc>" + loc + "</loc></url>")
	}
	b.WriteString(`</urlset>`)
	return b.String()
}

func sitemapIndexXML(locs ...string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><sitemapindex xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">`)
	for _, loc := range locs {
		b.WriteString("<sitemap><loc>" + loc + "</loc></sitemap>")
	}
	b.WriteString(`</sitemapindex>`)
	return b.String()
}

func pageHandler(title string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<html><body><main><h1>" + title + "</h1></main></body></html>"))
	}
}

func bundledPaths(pages []Page) []string {
	paths := make([]string, len(pages))
	for i, p := range pages {
		paths[i] = p.Path
	}
	return paths
}

// equalPaths is an ordered slice comparison — sitemap discovery preserves
// document order, so tests that assert an exact bundled-path list want
// order-sensitive equality, not set equality.
func equalPaths(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i, g := range got {
		if g != want[i] {
			return false
		}
	}
	return true
}

// newSitemapTestBundler spins up an origin serving pages (path -> heading
// text) plus a /sitemap.xml built from locs, and returns a Bundler
// pointed at it with SitemapURL set. A loc already starting with "http"
// is used as-is (for a deliberately foreign entry); anything else is
// resolved against the origin — the shared setup behind the single-file
// sitemap-discovery tests below.
func newSitemapTestBundler(t *testing.T, pages map[string]string, locs ...string) *Bundler {
	t.Helper()
	mux := http.NewServeMux()
	for path, title := range pages {
		mux.HandleFunc(path, pageHandler(title))
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	resolved := make([]string, len(locs))
	for i, loc := range locs {
		if strings.HasPrefix(loc, "http") {
			resolved[i] = loc
		} else {
			resolved[i] = srv.URL + loc
		}
	}
	mux.HandleFunc("/sitemap.xml", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(sitemapXML(resolved...)))
	})
	return NewBundler(BundlerConfig{Origin: srv.URL, TTL: time.Minute, SitemapURL: srv.URL + "/sitemap.xml"})
}

// TestBundleDiscoversPathsFromSitemap is the base case for pillar 2's
// long-tail fix: a flat <urlset> sitemap contributes paths without any of
// them being hand-listed in Paths.
func TestBundleDiscoversPathsFromSitemap(t *testing.T) {
	b := newSitemapTestBundler(t, map[string]string{"/a": "A", "/b": "B"}, "/a", "/b")
	pages, err := b.Bundle()
	if err != nil {
		t.Fatalf("Bundle: %v", err)
	}
	if got, want := bundledPaths(pages), []string{"/a", "/b"}; !equalPaths(got, want) {
		t.Errorf("bundled paths = %v, want %v", got, want)
	}
}

// TestBundleDiscoversPathsFromSitemapIndex covers the shape real large
// sites actually publish — a sitemapindex of per-section child sitemaps —
// since that's the case the long-tail argument is really about.
func TestBundleDiscoversPathsFromSitemapIndex(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/a", pageHandler("A"))
	mux.HandleFunc("/b", pageHandler("B"))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	mux.HandleFunc("/sitemap-a.xml", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(sitemapXML(srv.URL + "/a")))
	})
	mux.HandleFunc("/sitemap-b.xml", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(sitemapXML(srv.URL + "/b")))
	})
	mux.HandleFunc("/sitemap-index.xml", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(sitemapIndexXML(srv.URL+"/sitemap-a.xml", srv.URL+"/sitemap-b.xml")))
	})

	b := NewBundler(BundlerConfig{Origin: srv.URL, TTL: time.Minute, SitemapURL: srv.URL + "/sitemap-index.xml"})
	pages, err := b.Bundle()
	if err != nil {
		t.Fatalf("Bundle: %v", err)
	}
	if len(pages) != 2 {
		t.Fatalf("got %d pages, want 2 (one per child sitemap): %v", len(pages), bundledPaths(pages))
	}
}

// TestBundleSitemapPathsCappedAtMaxSitemapPages guards the memory-bound
// contract: Bundle() holds every page for the whole cache TTL, so a
// sitemap listing more than MaxSitemapPages must be truncated, not fully
// consumed.
func TestBundleSitemapPathsCappedAtMaxSitemapPages(t *testing.T) {
	mux := http.NewServeMux()
	locs := make([]string, 5)
	for i := range locs {
		p := fmt.Sprintf("/p%d", i)
		mux.HandleFunc(p, pageHandler(p))
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	for i := range locs {
		locs[i] = srv.URL + fmt.Sprintf("/p%d", i)
	}
	mux.HandleFunc("/sitemap.xml", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(sitemapXML(locs...)))
	})

	b := NewBundler(BundlerConfig{Origin: srv.URL, TTL: time.Minute, SitemapURL: srv.URL + "/sitemap.xml", MaxSitemapPages: 2})
	pages, err := b.Bundle()
	if err != nil {
		t.Fatalf("Bundle: %v", err)
	}
	if len(pages) != 2 {
		t.Fatalf("got %d pages, want 2 (MaxSitemapPages cap): %v", len(pages), bundledPaths(pages))
	}
}

// TestBundleSitemapSkipsForeignHostEntries confirms a <loc> pointing at a
// different host than Origin is skipped rather than fetched — Origin is
// the only host this proxy actually fronts, so bundling a foreign URL
// would mean serving content it has no business speaking for.
func TestBundleSitemapSkipsForeignHostEntries(t *testing.T) {
	b := newSitemapTestBundler(t, map[string]string{"/a": "A"}, "https://evil.example/other", "/a")
	pages, err := b.Bundle()
	if err != nil {
		t.Fatalf("Bundle: %v", err)
	}
	if got, want := bundledPaths(pages), []string{"/a"}; !equalPaths(got, want) {
		t.Errorf("bundled paths = %v, want %v (foreign-host entry skipped)", got, want)
	}
}

// TestDecodeSitemapLocsStopsAtEntryCap is the regression test for
// bounding decode by entry count, not just response bytes: a urlset well
// under maxSitemapResponseBytes but packed with more than
// maxSitemapEntriesPerFile minimal entries must still stop decoding at
// the cap rather than materializing every entry before MaxSitemapPages
// ever gets a chance to trim the result.
func TestDecodeSitemapLocsStopsAtEntryCap(t *testing.T) {
	locs := make([]string, maxSitemapEntriesPerFile+500)
	for i := range locs {
		locs[i] = fmt.Sprintf("http://example.com/p%d", i)
	}
	body := []byte(sitemapXML(locs...))

	got := decodeSitemapLocs(body, "url")
	if len(got) != maxSitemapEntriesPerFile {
		t.Fatalf("decoded %d locs, want exactly %d (the entry cap)", len(got), maxSitemapEntriesPerFile)
	}
}

// TestSameHostIgnoresCaseAndDefaultPort is the regression test for the
// host-comparison fix: DNS names are case-insensitive, and a sitemap
// generator that writes out a redundant default port (":443" on an
// https:// URL) shouldn't cause an otherwise-legitimate same-site entry
// to be silently skipped.
func TestSameHostIgnoresCaseAndDefaultPort(t *testing.T) {
	mustParse := func(t *testing.T, raw string) *url.URL {
		t.Helper()
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatalf("url.Parse(%q): %v", raw, err)
		}
		return u
	}
	cases := []struct {
		name        string
		loc, origin string
		want        bool
	}{
		{"identical", "https://example.com/a", "https://example.com/", true},
		{"different case", "https://EXAMPLE.com/a", "https://example.com/", true},
		{"loc has redundant https default port", "https://example.com:443/a", "https://example.com/", true},
		{"loc has redundant http default port", "http://example.com:80/a", "http://example.com/", true},
		{"both have same non-default port", "http://example.com:8080/a", "http://example.com:8080/", true},
		{"different non-default ports", "http://example.com:8080/a", "http://example.com:9090/", false},
		{"different host entirely", "https://evil.example/a", "https://example.com/", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			loc := mustParse(t, c.loc)
			origin := mustParse(t, c.origin)
			if got := sameHost(loc, origin); got != c.want {
				t.Errorf("sameHost(%q, %q) = %v, want %v", c.loc, c.origin, got, c.want)
			}
		})
	}
}

// TestBundleCombinesExplicitAndSitemapPathsWithoutDuplicating confirms a
// path present in both Paths and the sitemap is only fetched once.
func TestBundleCombinesExplicitAndSitemapPathsWithoutDuplicating(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/a", pageHandler("A"))
	mux.HandleFunc("/b", pageHandler("B"))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	mux.HandleFunc("/sitemap.xml", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(sitemapXML(srv.URL+"/a", srv.URL+"/b")))
	})

	b := NewBundler(BundlerConfig{Origin: srv.URL, Paths: []string{"/a"}, TTL: time.Minute, SitemapURL: srv.URL + "/sitemap.xml"})
	pages, err := b.Bundle()
	if err != nil {
		t.Fatalf("Bundle: %v", err)
	}
	if len(pages) != 2 {
		t.Fatalf("got %d pages, want 2 (no duplicate /a): %v", len(pages), bundledPaths(pages))
	}
}

// TestBundleSitemapPathFailureDoesNotAbortBundle is the behavioral
// distinction from explicit Paths: a sitemap is auto-discovered and
// expected to carry some noise (a stale entry, a page that started
// 404ing), so one bad entry skips instead of failing the whole bundle.
func TestBundleSitemapPathFailureDoesNotAbortBundle(t *testing.T) {
	b := newSitemapTestBundler(t, map[string]string{"/a": "A"}, "/a", "/missing")
	pages, err := b.Bundle()
	if err != nil {
		t.Fatalf("expected the missing sitemap entry to be skipped, not fail the bundle: %v", err)
	}
	if got, want := bundledPaths(pages), []string{"/a"}; !equalPaths(got, want) {
		t.Errorf("bundled paths = %v, want %v (/missing skipped, 404)", got, want)
	}
}

// TestBundleFirstBuildIsSharedAcrossConcurrentCallers proves concurrent
// callers hitting a never-built Bundler join the same synchronous build
// instead of each starting their own — the origin should see exactly one
// fetch for 20 simultaneous callers.
func TestBundleFirstBuildIsSharedAcrossConcurrentCallers(t *testing.T) {
	var hits int32
	release := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("/a", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		<-release
		w.Write([]byte("<html><body><main><h1>A</h1></main></body></html>"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	b := NewBundler(BundlerConfig{Origin: srv.URL, Paths: []string{"/a"}, TTL: time.Minute})

	const callers = 20
	var wg sync.WaitGroup
	errs := make([]error, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = b.Bundle()
		}(i)
	}
	time.Sleep(20 * time.Millisecond) // let every caller reach the join point
	close(release)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("caller %d: %v", i, err)
		}
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("origin hit %d times, want 1 (%d concurrent first-build callers should share one build)", got, callers)
	}
}

// TestBundleFirstBuildFailurePropagatesToCaller: with no prior successful
// build to fall back on, the caller has nothing else to serve — the
// error must still reach them, same as before stale-while-revalidate
// existed.
func TestBundleFirstBuildFailurePropagatesToCaller(t *testing.T) {
	srv := httptest.NewServer(http.NewServeMux()) // no handlers -> 404 for everything
	t.Cleanup(srv.Close)

	b := NewBundler(BundlerConfig{Origin: srv.URL, Paths: []string{"/missing"}, TTL: time.Minute})
	if _, err := b.Bundle(); err == nil {
		t.Fatal("expected the first-ever build's failure to reach the caller, got none")
	}
}

// TestBundleServesStaleWhileRebuildingInBackground is the regression test
// for the fix itself: once a bundle has been built at least once, a
// request during a stale window must return immediately with the last
// good content — never block behind however long a rebuild takes, which
// can now involve hundreds of sequential fetches once SitemapURL is set.
func TestBundleServesStaleWhileRebuildingInBackground(t *testing.T) {
	var version, hits int32
	version = 1
	release := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("/a", func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&hits, 1) > 1 {
			<-release // every fetch after the first (the background rebuild) blocks
		}
		fmt.Fprintf(w, "<html><body><main><h1>v%d</h1></main></body></html>", atomic.LoadInt32(&version))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	b := NewBundler(BundlerConfig{Origin: srv.URL, Paths: []string{"/a"}, TTL: 10 * time.Millisecond})

	pages, err := b.Bundle()
	if err != nil {
		t.Fatalf("first Bundle: %v", err)
	}
	if !hasVersion(pages, err, 1) {
		t.Fatalf("first build: pages = %v, err = %v — want v1", pages, err)
	}

	time.Sleep(20 * time.Millisecond) // past TTL
	atomic.StoreInt32(&version, 2)

	assertBundleRespondsInstantly(t, b, 1)

	close(release)
	assertBundleEventuallyServesVersion(t, b, 2)
}

// hasVersion reports whether pages' first entry is a successful fetch
// containing "vN" — one predicate instead of an inline multi-term
// conditional, shared by the assertions below.
func hasVersion(pages []Page, err error, version int) bool {
	return err == nil && len(pages) > 0 && strings.Contains(pages[0].Markdown, fmt.Sprintf("v%d", version))
}

// assertBundleRespondsInstantly proves Bundle() doesn't block behind an
// in-flight background rebuild: it must return the stale content within
// the timeout, not hang until the rebuild (blocked on release) finishes.
func assertBundleRespondsInstantly(t *testing.T, b *Bundler, staleVersion int) {
	t.Helper()
	done := make(chan []Page, 1)
	go func() {
		// t.Errorf, not assertHasVersion/Fatalf: FailNow (which Fatalf
		// calls) is documented as unsafe from a goroutine other than the
		// test's own — this closure runs in one.
		p, err := b.Bundle()
		if !hasVersion(p, err, staleVersion) {
			t.Errorf("stale-window: pages = %v, err = %v — want v%d", p, err, staleVersion)
		}
		done <- p
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Bundle() blocked instead of returning the stale bundle immediately")
	}
}

// assertBundleEventuallyServesVersion polls until the background rebuild
// lands, within a generous safety deadline.
func assertBundleEventuallyServesVersion(t *testing.T, b *Bundler, version int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if p, err := b.Bundle(); hasVersion(p, err, version) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("background rebuild never landed v%d", version)
}

// TestBundleConcurrentStaleRequestsTriggerOnlyOneBackgroundRebuild proves
// the dedup: many callers arriving during the same stale window must
// spawn exactly one rebuild between them, not one each.
func TestBundleConcurrentStaleRequestsTriggerOnlyOneBackgroundRebuild(t *testing.T) {
	var hits int32
	release := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("/a", func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&hits, 1) > 1 {
			<-release
		}
		w.Write([]byte("<html><body><main><h1>A</h1></main></body></html>"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	b := NewBundler(BundlerConfig{Origin: srv.URL, Paths: []string{"/a"}, TTL: time.Millisecond})
	if _, err := b.Bundle(); err != nil {
		t.Fatalf("first Bundle: %v", err)
	}
	time.Sleep(5 * time.Millisecond) // past TTL

	const callers = 20
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := b.Bundle(); err != nil {
				t.Errorf("concurrent stale Bundle: %v", err)
			}
		}()
	}
	wg.Wait()
	close(release)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && atomic.LoadInt32(&hits) < 2 {
		time.Sleep(5 * time.Millisecond)
	}
	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Errorf("origin hit %d times, want 2 (1 initial build + exactly 1 deduped background rebuild across %d concurrent stale callers)", got, callers)
	}
}

// TestBuildFailuresIncrementsOnFailedRebuild confirms the counter behind
// robotsyes_export_bundle_build_failures_total actually moves — the
// operator-visible signal that a background rebuild has started failing,
// now that a failure no longer surfaces as an error to every caller.
func TestBuildFailuresIncrementsOnFailedRebuild(t *testing.T) {
	var failing int32
	mux := http.NewServeMux()
	mux.HandleFunc("/a", func(w http.ResponseWriter, r *http.Request) {
		if atomic.LoadInt32(&failing) == 1 {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte("<html><body><main><h1>A</h1></main></body></html>"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	b := NewBundler(BundlerConfig{Origin: srv.URL, Paths: []string{"/a"}, TTL: time.Millisecond})
	if _, err := b.Bundle(); err != nil {
		t.Fatalf("first Bundle: %v", err)
	}

	atomic.StoreInt32(&failing, 1)
	time.Sleep(5 * time.Millisecond)
	pages, err := b.Bundle()
	if err != nil {
		t.Fatalf("expected the stale bundle despite the origin failing, got error: %v", err)
	}
	if len(pages) == 0 {
		t.Fatal("expected the last-good (stale) bundle, got none")
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && b.BuildFailures() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if b.BuildFailures() == 0 {
		t.Fatal("expected BuildFailures() to increment once the origin started failing")
	}
}
