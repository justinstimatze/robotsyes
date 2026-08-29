package export

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
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

// assertBundledPaths bundles b and asserts the result's paths exactly
// match want (order-sensitive — see equalPaths). Shared by every test
// that reduces to "build this bundler, bundle it, check what came out."
func assertBundledPaths(t *testing.T, b *Bundler, want []string) {
	t.Helper()
	pages, err := b.Bundle()
	if err != nil {
		t.Fatalf("Bundle: %v", err)
	}
	if got := bundledPaths(pages); !equalPaths(got, want) {
		t.Errorf("bundled paths = %v, want %v", got, want)
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

// TestBuildManifestComputesHashAndBytes is the base case: one page's
// entry carries its own content hash and byte length, not some
// derivative of the bundle as a whole.
func TestBuildManifestComputesHashAndBytes(t *testing.T) {
	pages := []Page{{Path: "/a", Markdown: "hello"}}
	m := buildManifest(pages, time.Now(), time.Minute)
	if len(m.Pages) != 1 {
		t.Fatalf("got %d entries, want 1", len(m.Pages))
	}
	entry := m.Pages[0]
	if entry.Path != "/a" {
		t.Errorf("Path = %q, want %q", entry.Path, "/a")
	}
	if entry.Bytes != len("hello") {
		t.Errorf("Bytes = %d, want %d", entry.Bytes, len("hello"))
	}
	wantSum := sha256.Sum256([]byte("hello"))
	if want := "sha256:" + hex.EncodeToString(wantSum[:]); entry.Hash != want {
		t.Errorf("Hash = %q, want %q", entry.Hash, want)
	}
	if m.Count != 1 {
		t.Errorf("Count = %d, want 1", m.Count)
	}
	if !strings.HasPrefix(m.BundleHash, "sha256:") {
		t.Errorf("BundleHash = %q, want a sha256: prefix", m.BundleHash)
	}
}

// TestBuildManifestSortsEntriesByPath guards BundleHash's "one
// comparison tells you nothing changed" property: without a stable
// order, a sitemap re-emitting the same URLs differently would flip the
// hash despite no content change.
func TestBuildManifestSortsEntriesByPath(t *testing.T) {
	pages := []Page{{Path: "/z", Markdown: "z"}, {Path: "/a", Markdown: "a"}, {Path: "/m", Markdown: "m"}}
	m := buildManifest(pages, time.Now(), time.Minute)
	got := make([]string, len(m.Pages))
	for i, e := range m.Pages {
		got[i] = e.Path
	}
	if want := []string{"/a", "/m", "/z"}; !equalPaths(got, want) {
		t.Errorf("entry order = %v, want %v (sorted by path)", got, want)
	}
}

// TestBuildManifestBundleHashStableAcrossBuiltAtAndTTL confirms
// BundleHash is purely a function of page content — a rebuild that
// changes nothing but the clock must not flip it.
func TestBuildManifestBundleHashStableAcrossBuiltAtAndTTL(t *testing.T) {
	pages := []Page{{Path: "/a", Markdown: "hello"}}
	m1 := buildManifest(pages, time.Now(), time.Minute)
	m2 := buildManifest(pages, time.Now().Add(time.Hour), 5*time.Minute)
	if m1.BundleHash != m2.BundleHash {
		t.Errorf("BundleHash changed with builtAt/ttl alone (%q vs %q) — it must depend only on page content", m1.BundleHash, m2.BundleHash)
	}
}

// TestBuildManifestBundleHashChangesWithContent is the other half of the
// stability guarantee: a real content change must actually be visible.
func TestBuildManifestBundleHashChangesWithContent(t *testing.T) {
	m1 := buildManifest([]Page{{Path: "/a", Markdown: "hello"}}, time.Now(), time.Minute)
	m2 := buildManifest([]Page{{Path: "/a", Markdown: "goodbye"}}, time.Now(), time.Minute)
	if m1.BundleHash == m2.BundleHash {
		t.Error("BundleHash unchanged despite different page content")
	}
}

// TestServeManifestWritesJSON is the HTTP-level check mirroring
// TestServeHTTPPlainWithoutAcceptEncoding's shape for the bundle.
func TestServeManifestWritesJSON(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/a", pageHandler("A"))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	b := NewBundler(BundlerConfig{Origin: srv.URL, Paths: []string{"/a"}, TTL: time.Minute})
	req := httptest.NewRequest(http.MethodGet, "/manifest", nil)
	w := httptest.NewRecorder()
	b.ServeManifest(w, req)

	if got := w.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want %q", got, "application/json")
	}
	var m Manifest
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("response isn't valid JSON: %v", err)
	}
	if m.Count != 1 {
		t.Errorf("Count = %d, want 1", m.Count)
	}
	if len(m.Pages) != 1 {
		t.Fatalf("got %d entries, want 1", len(m.Pages))
	}
	if m.Pages[0].Path != "/a" {
		t.Errorf("Pages[0].Path = %q, want %q", m.Pages[0].Path, "/a")
	}
}

// TestManifestFirstBuildFailurePropagatesToCaller mirrors
// TestBundleFirstBuildFailurePropagatesToCaller: with no prior
// successful build, Manifest() must surface Bundle()'s own error rather
// than a misleadingly-"successful" zero-value Manifest.
func TestManifestFirstBuildFailurePropagatesToCaller(t *testing.T) {
	srv := httptest.NewServer(http.NewServeMux()) // no handlers -> 404 for everything
	t.Cleanup(srv.Close)

	b := NewBundler(BundlerConfig{Origin: srv.URL, Paths: []string{"/missing"}, TTL: time.Minute})
	m, err := b.Manifest()
	if err == nil {
		t.Fatal("expected the first-ever build's failure to reach the caller, got none")
	}
	if m.Count != 0 || m.Pages != nil {
		t.Errorf("expected a zero-value Manifest alongside the error, got %+v", m)
	}
}

// TestManifestServesStaleWhileRebuildingInBackground mirrors
// TestBundleServesStaleWhileRebuildingInBackground: Manifest() during a
// stale window must return the last-good BundleHash immediately, not
// block behind an in-flight background rebuild.
func TestManifestServesStaleWhileRebuildingInBackground(t *testing.T) {
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
	m1, err := b.Manifest()
	if err != nil {
		t.Fatalf("first Manifest: %v", err)
	}
	staleHash := m1.BundleHash

	time.Sleep(20 * time.Millisecond) // past TTL
	atomic.StoreInt32(&version, 2)

	assertManifestRespondsInstantly(t, b, staleHash)

	close(release)
	assertManifestEventuallyChanges(t, b, staleHash)
}

// assertManifestRespondsInstantly proves Manifest() doesn't block behind
// an in-flight background rebuild: it must return the stale BundleHash
// within the timeout, not hang until the rebuild (blocked on release)
// finishes.
func assertManifestRespondsInstantly(t *testing.T, b *Bundler, staleHash string) {
	t.Helper()
	done := make(chan Manifest, 1)
	go func() {
		// t.Errorf, not Fatalf: FailNow is documented as unsafe from a
		// goroutine other than the test's own — this closure runs in one.
		m, err := b.Manifest()
		if err != nil {
			t.Errorf("stale-window Manifest: %v", err)
		}
		done <- m
	}()
	select {
	case m := <-done:
		if m.BundleHash != staleHash {
			t.Errorf("BundleHash = %q during the stale window, want unchanged %q", m.BundleHash, staleHash)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Manifest() blocked instead of returning the stale manifest immediately")
	}
}

// assertManifestEventuallyChanges polls until the background rebuild's
// manifest lands (a BundleHash different from staleHash), within a
// generous safety deadline.
func assertManifestEventuallyChanges(t *testing.T, b *Bundler, staleHash string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		m, err := b.Manifest()
		if err == nil && m.BundleHash != staleHash {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("background rebuild's manifest never landed")
}

// TestManifestPreviousResultUnaffectedByLaterRebuild is the regression
// test for buildManifest's fresh-slice invariant: if a rebuild reused or
// mutated a previous call's backing array, a Manifest returned before
// the rebuild would silently change out from under its caller once the
// rebuild landed.
func TestManifestPreviousResultUnaffectedByLaterRebuild(t *testing.T) {
	var version int32 = 1
	mux := http.NewServeMux()
	mux.HandleFunc("/a", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "<html><body><main><h1>v%d</h1></main></body></html>", atomic.LoadInt32(&version))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	b := NewBundler(BundlerConfig{Origin: srv.URL, Paths: []string{"/a"}, TTL: 10 * time.Millisecond})
	m1, err := b.Manifest()
	if err != nil {
		t.Fatalf("first Manifest: %v", err)
	}
	if len(m1.Pages) != 1 {
		t.Fatalf("got %d entries, want 1", len(m1.Pages))
	}
	firstHash := m1.Pages[0].Hash

	atomic.StoreInt32(&version, 2)
	time.Sleep(20 * time.Millisecond) // past TTL
	waitForManifestEntryHashChange(t, b, firstHash)

	if m1.Pages[0].Hash != firstHash {
		t.Errorf("a previously-returned Manifest's entry was mutated by a later rebuild: got %q, want unchanged %q", m1.Pages[0].Hash, firstHash)
	}
}

// waitForManifestEntryHashChange polls until a rebuild lands a manifest
// whose first entry's hash differs from from, within a generous safety
// deadline — the trigger TestManifestPreviousResultUnaffectedByLaterRebuild
// needs before it can check whether a Manifest captured before the
// rebuild was mutated by it.
func waitForManifestEntryHashChange(t *testing.T, b *Bundler, from string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		m, err := b.Manifest()
		if err != nil || len(m.Pages) != 1 {
			time.Sleep(5 * time.Millisecond)
			continue
		}
		if m.Pages[0].Hash != from {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("background rebuild never landed a changed manifest")
}

// TestServeTorrentDisabledIsNotFound and TestServeTorrentSeedDisabledIsNotFound
// live here rather than torrent_test.go — that file only compiles under
// `-tags torrent`, but ServeTorrent/ServeTorrentSeed's disabled-path
// branch (in export.go) is always compiled, so it needs coverage in the
// default build too.
func TestServeTorrentDisabledIsNotFound(t *testing.T) {
	b := NewBundler(BundlerConfig{Origin: "http://unused.invalid", Paths: []string{"/a"}, TTL: time.Minute})
	req := httptest.NewRequest(http.MethodGet, "/export.torrent", nil)
	w := httptest.NewRecorder()
	b.ServeTorrent(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d when torrent is disabled", w.Code, http.StatusNotFound)
	}
}

func TestServeTorrentSeedDisabledIsNotFound(t *testing.T) {
	b := NewBundler(BundlerConfig{Origin: "http://unused.invalid", Paths: []string{"/a"}, TTL: time.Minute})
	req := httptest.NewRequest(http.MethodGet, "/torrent-seed/pages/a", nil)
	w := httptest.NewRecorder()
	b.ServeTorrentSeed(w, req, "pages/a")
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d when torrent is disabled", w.Code, http.StatusNotFound)
	}
}
