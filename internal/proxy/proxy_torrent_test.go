//go:build torrent

package proxy

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/justinstimatze/robotsyes/internal/config"
)

// TestExportTorrentEndpointsServeThroughServeHTTP confirms both new
// routes — the .torrent itself and the BEP-19 web-seed prefix — are
// actually wired into ServeHTTP's dispatch, not just constructed in
// isolation the way the internal/export tests already cover. Needs the
// torrent build tag: the default build's stub buildTorrentInfo never
// produces bytes to serve, so this would 404 there regardless of wiring.
func TestExportTorrentEndpointsServeThroughServeHTTP(t *testing.T) {
	s := newTestServer(t, func(cfg *config.Config) {
		cfg.Export.Torrent.Enabled = true
		cfg.Export.Torrent.PublicURL = "https://example.com"
	})

	req := httptest.NewRequest(http.MethodGet, torrentPath, nil)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET %s: status = %d, want 200", torrentPath, w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/x-bittorrent" {
		t.Errorf("Content-Type = %q, want %q", ct, "application/x-bittorrent")
	}

	// newTestServer's origin bundles "/" and "/about" — pathToSeedKey
	// turns root into "index".
	seedReq := httptest.NewRequest(http.MethodGet, torrentSeedPrefix+"pages/index", nil)
	seedW := httptest.NewRecorder()
	s.ServeHTTP(seedW, seedReq)
	if seedW.Code != http.StatusOK {
		t.Fatalf("GET %spages/index: status = %d, want 200", torrentSeedPrefix, seedW.Code)
	}
	if seedW.Body.Len() == 0 {
		t.Error("seed route returned an empty body")
	}
}
