//go:build !payments

// This file is the default build's counterpart to payments_full.go — it
// carries no import of chitgate or chit, so `go install ./cmd/robotsyes`
// with no tags links none of it in. See payments_full.go's package doc.
package main

import (
	"log"

	"github.com/justinstimatze/robotsyes/internal/config"
	"github.com/justinstimatze/robotsyes/internal/payments"
)

// buildMerchant always returns nil in this build — payments support
// isn't compiled in. A config that turns payments on anyway is a real
// operator mistake, not something to silently ignore: fail fast with
// the fix, same as failIfTorrentUnsupported does for export.torrent.
func buildMerchant(cfg *config.Config) payments.Merchant {
	if cfg.Payments.Enabled {
		log.Fatalf("robotsyes: payments.enabled is true, but this binary wasn't built with payments support; rebuild with `go build -tags payments`")
	}
	return nil
}
