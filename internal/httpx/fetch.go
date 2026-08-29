// Package httpx holds HTTP-client helpers shared across robots.yes's
// fetch paths. Currently just one: the "read a response body, capped at a
// fixed size" idiom that internal/export and internal/identity would
// otherwise each reimplement independently — and did, until calque's
// drift scan caught the two copies diverging (one had a cap, one didn't;
// see .calque/registry.md #17). One function, two callers, can't drift
// apart again.
package httpx

import (
	"fmt"
	"io"
	"net/http"
)

// GetBounded issues client.Get(rawURL) and returns the response body,
// refusing anything over max bytes rather than reading it fully into
// memory. It reads one byte past max so an oversized body is caught by an
// explicit length check with its own named error, not by relying on a
// downstream decoder (JSON, HTML) to happen to fail on a truncated body.
//
// Callers stay responsible for anything upstream of the read itself —
// which transport to dial with (a plain client, or one with an SSRF-safe
// DialContext), and whether the URL is trusted operator config or
// unauthenticated request input. GetBounded only bounds the read.
func GetBounded(client *http.Client, rawURL string, max int) ([]byte, error) {
	resp, err := client.Get(rawURL)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetching %s: origin returned %s", rawURL, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(max)+1))
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", rawURL, err)
	}
	if len(body) > max {
		return nil, fmt.Errorf("response for %s exceeds %d bytes, refusing to read", rawURL, max)
	}
	return body, nil
}
