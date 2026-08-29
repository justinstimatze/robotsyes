//go:build torrent

// The real .torrent this file builds is the second half of the same
// Internet Archive-lifted design manifest.go credits: a per-page
// BitTorrent piece listing, BEP-19 web-seeded straight back to this
// server so a swarm can form for long-tail content without robots.yes
// ever running a tracker or peer client — the same role archive.org's own
// production .torrents play for its own items.
//
// This file only compiles into a binary built with `-tags torrent`. The
// default build gets torrent_stub.go instead, so `go install
// ./cmd/robotsyes` with no tags never links in anacrolix/torrent at all —
// content negotiation and bulk export shouldn't require auditing a
// BitTorrent implementation to adopt.
package export

import (
	"bytes"
	"io"
	"log"
	"strings"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
)

// TorrentSupported reports whether this binary was built with `-tags
// torrent`. main.go checks this at startup so a config that turns
// export.torrent on against a binary that can't build one fails fast
// with the fix, instead of silently 404ing on every request.
const TorrentSupported = true

// pathToSeedKey converts a bundled page's URL path into the slash-joined
// key used both as this page's BEP-3 file-path segments and as the
// lookup key ServeTorrentSeed reverses the same web-seed GET back
// through — one function, so the two can never drift apart. Root ("/")
// has no path segments to speak of, so it's given the literal name
// "index".
func pathToSeedKey(path string) string {
	trimmed := strings.Trim(path, "/")
	if trimmed == "" {
		return "index"
	}
	return trimmed
}

// buildTorrentInfo constructs a v1 (BEP 3) multi-file .torrent from
// pages, bencoded and ready to serve as-is, plus the seedIndex
// ServeTorrentSeed looks pages back up in by the same key this function
// derives their file paths from. seedBaseURL is the full, already-joined
// web-seed URL (the operator's public_url plus proxy.go's own
// torrentSeedPrefix) — this package never references that constant
// itself, since internal/proxy is the package that owns every
// well-known-path constant and internal/export can't import it back
// without a cycle. Every call allocates a fresh seedIndex
// and a fresh encoded []byte — never reuses or mutates either from a
// previous call, the same discipline buildManifest's doc comment already
// states for its own slice. This matters for seedIndex specifically
// because a Go map is a reference type: Bundler.startBuildLocked must
// replace b.cachedSeedIndex with this map wholesale, never mutate the
// previous one's entries in place, or a concurrent ServeTorrentSeed
// reader that captured a reference to the old map under an earlier lock
// hold could observe a half-updated table after releasing its own lock.
//
// A single-file torrent has no analogous naming collision, but a
// multi-file one can: two distinct page paths (e.g. "/blog/foo" and
// "/blog/foo/") can trim to the same seed key. The later page in
// iteration order is skipped and logged rather than silently
// overwriting the earlier one's slot — the same "skip a bad entry, don't
// abort the whole build" choice bundleSitemapPages already makes for a
// lesser-quality auto-discovered path.
func buildTorrentInfo(pages []Page, seedBaseURL string, trackers []string) (encoded []byte, seedIndex map[string]Page, err error) {
	seedIndex = make(map[string]Page, len(pages))
	info := metainfo.Info{Name: torrentInfoName}
	for _, p := range pages {
		key := pathToSeedKey(p.Path)
		if _, dup := seedIndex[key]; dup {
			log.Printf("robotsyes: skipping torrent entry for %s: seed key %q collides with an earlier page", p.Path, key)
			continue
		}
		seedIndex[key] = p
		info.Files = append(info.Files, metainfo.FileInfo{
			Path:   strings.Split(key, "/"),
			Length: int64(len(p.Markdown)),
		})
	}

	info.PieceLength = metainfo.ChoosePieceLength(info.TotalLength())
	if err := info.GeneratePieces(func(fi metainfo.FileInfo) (io.ReadCloser, error) {
		page := seedIndex[strings.Join(fi.Path, "/")]
		return io.NopCloser(strings.NewReader(page.Markdown)), nil
	}); err != nil {
		return nil, nil, err
	}

	mi := metainfo.MetaInfo{
		UrlList: metainfo.UrlList{seedBaseURL},
	}
	if len(trackers) > 0 {
		mi.AnnounceList = metainfo.AnnounceList{trackers}
	}
	mi.SetDefaults()
	mi.InfoBytes, err = bencode.Marshal(info)
	if err != nil {
		return nil, nil, err
	}

	var buf bytes.Buffer
	if err := mi.Write(&buf); err != nil {
		return nil, nil, err
	}
	return buf.Bytes(), seedIndex, nil
}
