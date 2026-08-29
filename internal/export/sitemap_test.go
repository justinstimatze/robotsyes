package export

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

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
	assertBundledPaths(t, b, []string{"/a", "/b"})
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
	assertBundledPaths(t, b, []string{"/a"})
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
	assertBundledPaths(t, b, []string{"/a"})
}

// TestFetchPagesConcurrentlyBoundsInFlightRequests is the regression
// test for the scalability finding that a large sitemap fetched strictly
// serially can take longer to rebuild than the bundle's own TTL:
// fetchPagesConcurrently must run more than one fetch at a time, but
// never more than maxConcurrentSitemapFetches at once.
func TestFetchPagesConcurrentlyBoundsInFlightRequests(t *testing.T) {
	var inFlight, maxObserved int32
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&inFlight, 1)
		for {
			old := atomic.LoadInt32(&maxObserved)
			if n <= old || atomic.CompareAndSwapInt32(&maxObserved, old, n) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		atomic.AddInt32(&inFlight, -1)
		w.Write([]byte("<html><body><main><h1>x</h1></main></body></html>"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	b := NewBundler(BundlerConfig{Origin: srv.URL, TTL: time.Minute})
	paths := make([]string, maxConcurrentSitemapFetches*3)
	for i := range paths {
		paths[i] = fmt.Sprintf("/p%d", i)
	}
	pages := b.fetchPagesConcurrently(paths)

	if len(pages) != len(paths) {
		t.Fatalf("got %d pages, want %d", len(pages), len(paths))
	}
	if maxObserved < 2 {
		t.Errorf("max observed concurrent requests = %d, want > 1 (fetches should overlap)", maxObserved)
	}
	if maxObserved > maxConcurrentSitemapFetches {
		t.Errorf("max observed concurrent requests = %d, want <= %d", maxObserved, maxConcurrentSitemapFetches)
	}
}

// TestFetchPagesConcurrentlyPreservesOrder confirms the result order
// matches the input path order regardless of which fetch actually
// completes first — later tests and callers (e.g.
// TestBundleCombinesExplicitAndSitemapPathsWithoutDuplicating) assert
// ordered equality against bundled paths, which concurrent completion
// order alone wouldn't guarantee.
func TestFetchPagesConcurrentlyPreservesOrder(t *testing.T) {
	mux := http.NewServeMux()
	// /p0 deliberately answers slower than /p1 and /p2, so completion
	// order differs from input order unless results are placed by index.
	mux.HandleFunc("/p0", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(20 * time.Millisecond)
		w.Write([]byte("<html><body><main><h1>0</h1></main></body></html>"))
	})
	mux.HandleFunc("/p1", pageHandler("1"))
	mux.HandleFunc("/p2", pageHandler("2"))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	b := NewBundler(BundlerConfig{Origin: srv.URL, TTL: time.Minute})
	pages := b.fetchPagesConcurrently([]string{"/p0", "/p1", "/p2"})

	if got, want := bundledPaths(pages), []string{"/p0", "/p1", "/p2"}; !equalPaths(got, want) {
		t.Errorf("bundled paths = %v, want %v (input order, not completion order)", got, want)
	}
}
