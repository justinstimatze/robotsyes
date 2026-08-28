// Package config loads robotsyes' YAML configuration: the origin to
// proxy, the listen address, which paths to bundle for bulk export, and
// the published rate-limit ceiling per identity tier.
package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"

	"github.com/justinstimatze/robotsyes/internal/identity"
)

// Config is the whole robotsyes.yaml document.
type Config struct {
	// Origin is the upstream server every request proxies to.
	Origin string `yaml:"origin"`
	// Addr is the listen address, e.g. ":8080".
	Addr string `yaml:"addr"`
	// Export lists the paths bundled into the bulk export.
	Export ExportConfig `yaml:"export"`
	// RateLimits maps an identity tier name (see internal/identity) to
	// its published requests-per-minute ceiling.
	RateLimits map[string]int `yaml:"rate_limits"`
	// Payments configures pillar 4's optional paid-overflow tier: past
	// RateLimits' ceiling, a requester can settle a payment instead of
	// drawing a flat 429. Off by default.
	Payments PaymentsConfig `yaml:"payments"`
}

// PaymentsConfig configures the optional x402 paid-overflow path (see
// internal/paymentgate/chitgate). Enabled must be explicitly true —
// there is no flag or auto-detect that turns this on by inference, since
// once on it moves real money.
type PaymentsConfig struct {
	// Enabled turns on the paid-overflow path. Hard default false.
	Enabled bool `yaml:"enabled"`
	// PayoutAddress is the bare 0x EVM address payments settle to.
	// Required when Enabled.
	PayoutAddress string `yaml:"payout_address"`
	// PriceCentsPerRequest is the flat US-cent price for one
	// over-ceiling request. Required (and must be positive) when
	// Enabled.
	PriceCentsPerRequest int64 `yaml:"price_cents_per_request"`
	// Network is the x402 CAIP-2 network id advertised in challenges.
	// Defaults to Base mainnet ("eip155:8453") if unset.
	Network string `yaml:"network"`
	// Asset is the ERC-20 contract address payment is accepted in.
	// Defaults to USDC on Base if unset.
	Asset string `yaml:"asset"`
}

// ExportConfig configures pillar 2: bulk/structured export.
type ExportConfig struct {
	// Paths are fetched from Origin, stripped, and bundled directly.
	Paths []string `yaml:"paths"`
	// TTLSeconds is how long a bundle is served before being rebuilt.
	TTLSeconds int `yaml:"ttl_seconds"`
	// SitemapURL, if set, is fetched on every rebuild to discover
	// additional paths beyond Paths — see internal/export's
	// discoverSitemapPaths. This is what lets bulk export cover a site's
	// long tail without hand-enumerating every path in Paths.
	SitemapURL string `yaml:"sitemap_url"`
	// MaxSitemapPages caps how many paths SitemapURL can contribute to
	// one bundle. Zero means export.DefaultMaxSitemapPages.
	MaxSitemapPages int `yaml:"max_sitemap_pages"`
}

// Default returns a Config with every field a runnable prototype needs,
// so a bare `robotsyes serve` (no config file) still does something.
func Default() Config {
	return Config{
		Origin: "http://localhost:3000",
		Addr:   ":8080",
		Export: ExportConfig{
			Paths:      []string{"/"},
			TTLSeconds: 300,
		},
		RateLimits: map[string]int{
			string(identity.TierUnverified): 10,
			string(identity.TierDeclared):   60,
			string(identity.TierVerified):   300,
		},
	}
}

// Load reads and parses a YAML config file, filling any field the file
// omits from Default().
func Load(path string) (Config, error) {
	cfg := Default()
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("reading %s: %w", path, err)
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parsing %s: %w", path, err)
	}
	if cfg.Origin == "" {
		return Config{}, fmt.Errorf("%s: origin is required", path)
	}
	return cfg, nil
}
