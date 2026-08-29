//go:build !torrent

// This file is the default build's counterpart to torrent.go — it
// carries no import of anacrolix/torrent, so `go install ./cmd/robotsyes`
// with no tags links none of it in. See torrent.go's package doc for why.
package export

import "errors"

// TorrentSupported reports whether this binary was built with `-tags
// torrent`. See torrent.go's copy of this const for how main.go uses it.
const TorrentSupported = false

var errTorrentNotCompiled = errors.New("torrent support not compiled into this binary; rebuild with `go build -tags torrent`")

// buildTorrentInfo always fails in this build — main.go refuses to start
// with export.torrent.enabled against a binary built without the
// torrent tag, so in practice this is never reached, but Bundler still
// needs a buildTorrentInfo to link against regardless of build tag.
func buildTorrentInfo(pages []Page, seedBaseURL string, trackers []string) (encoded []byte, seedIndex map[string]Page, err error) {
	return nil, nil, errTorrentNotCompiled
}
