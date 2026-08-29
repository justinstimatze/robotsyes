//go:build torrent

package export

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/anacrolix/torrent/metainfo"
)

func TestPathToSeedKey(t *testing.T) {
	cases := []struct{ path, want string }{
		{"/", "index"},
		{"/blog/foo", "blog/foo"},
		{"/blog/foo/", "blog/foo"},
		{"blog/foo", "blog/foo"},
	}
	for _, c := range cases {
		if got := pathToSeedKey(c.path); got != c.want {
			t.Errorf("pathToSeedKey(%q) = %q, want %q", c.path, got, c.want)
		}
	}
}

// parseTestTorrent builds a .torrent from pages via buildTorrentInfo and
// decodes it back, so each of the focused tests below can assert one
// property against an already-built, already-parsed result instead of
// repeating the build-and-parse ceremony.
func parseTestTorrent(t *testing.T, pages []Page, seedBaseURL string, trackers []string) (*metainfo.MetaInfo, metainfo.Info, map[string]Page) {
	t.Helper()
	encoded, seedIndex, err := buildTorrentInfo(pages, seedBaseURL, trackers)
	if err != nil {
		t.Fatalf("buildTorrentInfo: %v", err)
	}
	mi, err := metainfo.Load(bytes.NewReader(encoded))
	if err != nil {
		t.Fatalf("encoded .torrent doesn't parse: %v", err)
	}
	info, err := mi.UnmarshalInfo()
	if err != nil {
		t.Fatalf("UnmarshalInfo: %v", err)
	}
	return mi, info, seedIndex
}

// TestBuildTorrentInfoSeedIndexMatchesPages confirms buildTorrentInfo's
// seedIndex return value — the map ServeTorrentSeed later looks pages up
// in — carries the original page back, keyed by its seed key.
func TestBuildTorrentInfoSeedIndexMatchesPages(t *testing.T) {
	pages := []Page{{Path: "/blog/foo", Markdown: "hello world"}}
	_, _, seedIndex := parseTestTorrent(t, pages, "https://example.com/seed/", nil)
	if len(seedIndex) != 1 {
		t.Fatalf("got %d seedIndex entries, want 1", len(seedIndex))
	}
	page, ok := seedIndex["blog/foo"]
	if !ok {
		t.Fatalf("seedIndex has no entry for %q", "blog/foo")
	}
	if page.Markdown != "hello world" {
		t.Errorf("seedIndex[%q].Markdown = %q, want %q", "blog/foo", page.Markdown, "hello world")
	}
}

// TestBuildTorrentInfoFilesMatchPages is the base case: one page in, one
// FileInfo out, keyed and path-split consistently with seedIndex.
func TestBuildTorrentInfoFilesMatchPages(t *testing.T) {
	pages := []Page{{Path: "/blog/foo", Markdown: "hello world"}}
	_, info, _ := parseTestTorrent(t, pages, "https://example.com/seed/", nil)

	if len(info.Files) != 1 {
		t.Fatalf("got %d Info.Files, want 1", len(info.Files))
	}
	file := info.Files[0]
	wantPath := []string{"blog", "foo"}
	if len(file.Path) != len(wantPath) {
		t.Fatalf("Files[0].Path = %v, want %v", file.Path, wantPath)
	}
	if file.Path[0] != wantPath[0] {
		t.Errorf("Files[0].Path[0] = %q, want %q", file.Path[0], wantPath[0])
	}
	if file.Path[1] != wantPath[1] {
		t.Errorf("Files[0].Path[1] = %q, want %q", file.Path[1], wantPath[1])
	}
	if file.Length != int64(len("hello world")) {
		t.Errorf("Files[0].Length = %d, want %d", file.Length, len("hello world"))
	}
}

// TestBuildTorrentInfoGeneratesPieces confirms GeneratePieces actually
// hashed the page content rather than leaving Pieces empty.
func TestBuildTorrentInfoGeneratesPieces(t *testing.T) {
	pages := []Page{{Path: "/blog/foo", Markdown: "hello world"}}
	_, info, _ := parseTestTorrent(t, pages, "https://example.com/seed/", nil)
	if len(info.Pieces) == 0 {
		t.Error("Info.Pieces is empty — GeneratePieces didn't hash anything")
	}
}

// TestBuildTorrentInfoSetsUrlList confirms the web-seed URL is exactly
// the caller-supplied seedBaseURL, unmodified — this is the field a real
// BitTorrent client uses to construct its BEP-19 web-seed GET.
func TestBuildTorrentInfoSetsUrlList(t *testing.T) {
	pages := []Page{{Path: "/blog/foo", Markdown: "hello world"}}
	mi, _, _ := parseTestTorrent(t, pages, "https://example.com/seed/", nil)
	if len(mi.UrlList) != 1 {
		t.Fatalf("UrlList = %v, want exactly one entry", mi.UrlList)
	}
	if want := "https://example.com/seed/"; mi.UrlList[0] != want {
		t.Errorf("UrlList[0] = %q, want %q", mi.UrlList[0], want)
	}
}

// TestBuildTorrentInfoInfohashStableAcrossCalls mirrors
// TestBuildManifestBundleHashStableAcrossBuiltAtAndTTL: identical page
// content must yield the same infohash regardless of when it's built.
func TestBuildTorrentInfoInfohashStableAcrossCalls(t *testing.T) {
	pages := []Page{{Path: "/a", Markdown: "hello"}}
	e1, _, err := buildTorrentInfo(pages, "https://example.com/seed/", nil)
	if err != nil {
		t.Fatalf("buildTorrentInfo (1): %v", err)
	}
	time.Sleep(time.Millisecond) // SetDefaults stamps CreationDate from time.Now()
	e2, _, err := buildTorrentInfo(pages, "https://example.com/seed/", nil)
	if err != nil {
		t.Fatalf("buildTorrentInfo (2): %v", err)
	}
	h1 := infohashOf(t, e1)
	h2 := infohashOf(t, e2)
	if h1 != h2 {
		t.Errorf("infohash changed across calls with identical page content (%v vs %v)", h1, h2)
	}
}

// TestBuildTorrentInfoInfohashChangesWithContent is the other half of the
// stability guarantee.
func TestBuildTorrentInfoInfohashChangesWithContent(t *testing.T) {
	e1, _, err := buildTorrentInfo([]Page{{Path: "/a", Markdown: "hello"}}, "https://example.com/seed/", nil)
	if err != nil {
		t.Fatalf("buildTorrentInfo (1): %v", err)
	}
	e2, _, err := buildTorrentInfo([]Page{{Path: "/a", Markdown: "goodbye"}}, "https://example.com/seed/", nil)
	if err != nil {
		t.Fatalf("buildTorrentInfo (2): %v", err)
	}
	if infohashOf(t, e1) == infohashOf(t, e2) {
		t.Error("infohash unchanged despite different page content")
	}
}

func infohashOf(t *testing.T, encoded []byte) metainfo.Hash {
	t.Helper()
	mi, err := metainfo.Load(bytes.NewReader(encoded))
	if err != nil {
		t.Fatalf("encoded .torrent doesn't parse: %v", err)
	}
	return mi.HashInfoBytes()
}

// TestBuildTorrentInfoSkipsSeedKeyCollision guards against two page paths
// that trim to the same seed key silently overwriting one another's slot
// — the later one must be skipped, not clobber the earlier entry.
func TestBuildTorrentInfoSkipsSeedKeyCollision(t *testing.T) {
	pages := []Page{
		{Path: "/blog/foo", Markdown: "first"},
		{Path: "/blog/foo/", Markdown: "second"},
	}
	_, seedIndex, err := buildTorrentInfo(pages, "https://example.com/seed/", nil)
	if err != nil {
		t.Fatalf("buildTorrentInfo: %v", err)
	}
	if len(seedIndex) != 1 {
		t.Fatalf("got %d seedIndex entries, want 1 (the collision should be skipped)", len(seedIndex))
	}
	if got := seedIndex["blog/foo"].Markdown; got != "first" {
		t.Errorf("seedIndex[%q].Markdown = %q, want %q (the earlier page should win)", "blog/foo", got, "first")
	}
}

// TestBuildTorrentInfoWrapsTrackersAsOneTier confirms the AnnounceList
// shape fix: Trackers ([]string) becomes exactly one BEP-12 tier, not a
// flattened or malformed list.
func TestBuildTorrentInfoWrapsTrackersAsOneTier(t *testing.T) {
	trackers := []string{"udp://tracker.example.com:6969/announce"}
	encoded, _, err := buildTorrentInfo([]Page{{Path: "/a", Markdown: "x"}}, "https://example.com/seed/", trackers)
	if err != nil {
		t.Fatalf("buildTorrentInfo: %v", err)
	}
	mi, err := metainfo.Load(bytes.NewReader(encoded))
	if err != nil {
		t.Fatalf("encoded .torrent doesn't parse: %v", err)
	}
	if len(mi.AnnounceList) != 1 {
		t.Fatalf("AnnounceList = %v, want exactly one tier", mi.AnnounceList)
	}
	if len(mi.AnnounceList[0]) != 1 {
		t.Fatalf("AnnounceList[0] = %v, want exactly one tracker", mi.AnnounceList[0])
	}
	if mi.AnnounceList[0][0] != trackers[0] {
		t.Errorf("AnnounceList[0][0] = %q, want %q", mi.AnnounceList[0][0], trackers[0])
	}
}

// TestServeTorrentWritesParsableTorrent is the HTTP-level check that a
// running Bundler with torrent enabled actually serves one, end to end
// through Bundle()'s build cycle.
func TestServeTorrentWritesParsableTorrent(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/a", pageHandler("A"))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	b := NewBundler(BundlerConfig{
		Origin: srv.URL, Paths: []string{"/a"}, TTL: time.Minute,
		TorrentEnabled: true, TorrentSeedBaseURL: "https://example.com/seed/",
	})
	req := httptest.NewRequest(http.MethodGet, "/export.torrent", nil)
	w := httptest.NewRecorder()
	b.ServeTorrent(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	if got := w.Header().Get("Content-Type"); got != "application/x-bittorrent" {
		t.Errorf("Content-Type = %q, want %q", got, "application/x-bittorrent")
	}
	mi, err := metainfo.Load(w.Body)
	if err != nil {
		t.Fatalf("response body doesn't parse as a .torrent: %v", err)
	}
	if len(mi.UrlList) != 1 || mi.UrlList[0] != "https://example.com/seed/" {
		t.Errorf("UrlList = %v, want [%q]", mi.UrlList, "https://example.com/seed/")
	}
}

// TestServeTorrentSeedServesExactMarkdownBytes is the concrete regression
// test for this feature's whole reason for existing: the web-seed route
// must serve the exact bytes the .torrent's pieces were hashed over, not
// whatever pillar 1's content-negotiated route would return to a plain
// GET with no Accept header.
func TestServeTorrentSeedServesExactMarkdownBytes(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/blog/foo", pageHandler("Foo"))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	b := NewBundler(BundlerConfig{
		Origin: srv.URL, Paths: []string{"/blog/foo"}, TTL: time.Minute,
		TorrentEnabled: true, TorrentSeedBaseURL: "https://example.com/seed/",
	})
	pages, err := b.Bundle()
	if err != nil {
		t.Fatalf("Bundle: %v", err)
	}
	wantBody := pages[0].Markdown

	req := httptest.NewRequest(http.MethodGet, "/torrent-seed/pages/blog/foo", nil)
	w := httptest.NewRecorder()
	b.ServeTorrentSeed(w, req, "pages/blog/foo")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	if got := w.Body.String(); got != wantBody {
		t.Errorf("body = %q, want the exact bundled Page.Markdown %q", got, wantBody)
	}
}

// TestServeTorrentSeedSupportsRangeRequests is the other half of the same
// regression test: BEP-19 clients fetch pieces via Range, and
// http.ServeContent must actually honor that, not just serve the whole
// body on every request.
func TestServeTorrentSeedSupportsRangeRequests(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/a", pageHandler("A Longer Title For Range Testing"))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	b := NewBundler(BundlerConfig{
		Origin: srv.URL, Paths: []string{"/a"}, TTL: time.Minute,
		TorrentEnabled: true, TorrentSeedBaseURL: "https://example.com/seed/",
	})
	pages, err := b.Bundle()
	if err != nil {
		t.Fatalf("Bundle: %v", err)
	}
	wantBody := pages[0].Markdown
	if len(wantBody) < 10 {
		t.Fatalf("test page too short (%d bytes) for a bytes=0-9 range check", len(wantBody))
	}

	req := httptest.NewRequest(http.MethodGet, "/torrent-seed/pages/a", nil)
	req.Header.Set("Range", "bytes=0-9")
	w := httptest.NewRecorder()
	b.ServeTorrentSeed(w, req, "pages/a")

	if w.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, want %d for a Range request", w.Code, http.StatusPartialContent)
	}
	if got := w.Body.String(); got != wantBody[:10] {
		t.Errorf("range body = %q, want %q", got, wantBody[:10])
	}
}

func TestServeTorrentSeedUnknownPathIsNotFound(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/a", pageHandler("A"))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	b := NewBundler(BundlerConfig{
		Origin: srv.URL, Paths: []string{"/a"}, TTL: time.Minute,
		TorrentEnabled: true, TorrentSeedBaseURL: "https://example.com/seed/",
	})
	if _, err := b.Bundle(); err != nil {
		t.Fatalf("Bundle: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/torrent-seed/pages/nope", nil)
	w := httptest.NewRecorder()
	b.ServeTorrentSeed(w, req, "pages/nope")
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d for an unknown seed path", w.Code, http.StatusNotFound)
	}
}
