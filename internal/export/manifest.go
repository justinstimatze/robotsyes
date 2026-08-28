// The manifest design in this file — a per-file, per-hash listing
// alongside a bulk download, letting a fetcher selectively pull and
// verify a subset without any peer-to-peer networking — is lifted
// directly from the Internet Archive's own item-distribution pattern
// (its auto-generated .torrent files carry exactly this per-file
// structure via BitTorrent's multi-file info dict). Archive.org has
// been running this in production, in the open, for the exact same
// cache-economics reason this project cares about it, for far longer
// than robots.yes has existed. Credit where it's due.
package export

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"time"
)

// ManifestEntry describes one bundled page without its content — enough
// for a bot to decide whether it wants this path and to verify what it
// gets back.
type ManifestEntry struct {
	Path  string `json:"path"`
	Hash  string `json:"hash"` // "sha256:<hex>" over Page.Markdown's UTF-8 bytes
	Bytes int    `json:"bytes"`
}

// Manifest describes the currently cached bundle without its content —
// lets a bot selectively fetch a subtree of a bulk export (via the
// per-path content-negotiation route) or detect an unchanged bundle in
// one comparison (BundleHash) instead of re-downloading and diffing
// every page on every crawl.
type Manifest struct {
	GeneratedAt time.Time       `json:"generated_at"`
	TTLSeconds  int             `json:"ttl_seconds"`
	Count       int             `json:"count"`
	BundleHash  string          `json:"bundle_hash"`
	Pages       []ManifestEntry `json:"pages"`
}

// buildManifest computes a Manifest from pages, always allocating a
// fresh entries slice — never reusing or mutating a slice returned by a
// previous call. A Manifest handed back by a past Bundler.Manifest call
// aliases its Pages slice header, not a copy; sharing a backing array
// across calls would let a later rebuild's sort mutate an
// already-returned Manifest out from under its caller.
//
// Entries are sorted by Path before both the returned slice and the
// BundleHash computation. This is deliberate, not just tidiness:
// without it, a sitemap that re-emits the same URLs in a different
// order on a later fetch — with no actual content change — would still
// flip BundleHash, defeating the "one comparison tells you nothing
// changed" property it exists for.
func buildManifest(pages []Page, builtAt time.Time, ttl time.Duration) Manifest {
	entries := make([]ManifestEntry, len(pages))
	for i, p := range pages {
		sum := sha256.Sum256([]byte(p.Markdown))
		entries[i] = ManifestEntry{
			Path:  p.Path,
			Hash:  "sha256:" + hex.EncodeToString(sum[:]),
			Bytes: len(p.Markdown),
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })

	h := sha256.New()
	for _, e := range entries {
		h.Write([]byte(e.Path))
		h.Write([]byte(e.Hash))
	}

	return Manifest{
		GeneratedAt: builtAt,
		TTLSeconds:  int(ttl / time.Second),
		Count:       len(entries),
		BundleHash:  "sha256:" + hex.EncodeToString(h.Sum(nil)),
		Pages:       entries,
	}
}
