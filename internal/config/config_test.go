package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeConfigFile(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "robotsyes.yaml")
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("writing test config: %v", err)
	}
	return path
}

func TestLoadRejectsTorrentEnabledWithoutPublicURL(t *testing.T) {
	path := writeConfigFile(t, `
origin: https://example.com
export:
  torrent:
    enabled: true
`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected an error for export.torrent.enabled with no public_url, got nil")
	}
}

func TestLoadAcceptsTorrentEnabledWithPublicURL(t *testing.T) {
	path := writeConfigFile(t, `
origin: https://example.com
export:
  torrent:
    enabled: true
    public_url: https://robotsyes.dev
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Export.Torrent.Enabled {
		t.Error("Export.Torrent.Enabled = false, want true")
	}
	if cfg.Export.Torrent.PublicURL != "https://robotsyes.dev" {
		t.Errorf("Export.Torrent.PublicURL = %q, want %q", cfg.Export.Torrent.PublicURL, "https://robotsyes.dev")
	}
}

func TestDefaultLeavesTorrentDisabled(t *testing.T) {
	cfg := Default()
	if cfg.Export.Torrent.Enabled {
		t.Error("Default().Export.Torrent.Enabled = true, want false")
	}
}

// TestLoadRejectsEmptyRateLimits is the regression test for a real,
// reachable lockout: yaml.v3 merges a map field's keys into Default()'s
// existing map for most configs, which is why setting only one tier
// normally leaves the other two at their defaults. But `rate_limits:
// null` (a plausible typo, or a template that renders empty) clears the
// map to empty instead of merging — verified directly against yaml.v3,
// not assumed. Every tier then gets zero capacity, denying every
// request regardless of identity. Load must reject this outright rather
// than start a server that quietly denies all traffic.
func TestLoadRejectsEmptyRateLimits(t *testing.T) {
	path := writeConfigFile(t, `
origin: https://example.com
rate_limits: null
`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected an error for rate_limits: null (every tier missing), got nil")
	}
}

// TestLoadAcceptsPartialRateLimitOverride confirms Load doesn't demand
// every tier be spelled out explicitly — setting just one tier merges
// into Default()'s other two (yaml.v3's own map-merge behavior, verified
// directly), which the new per-tier check must still accept.
func TestLoadAcceptsPartialRateLimitOverride(t *testing.T) {
	path := writeConfigFile(t, `
origin: https://example.com
rate_limits:
  unverified: 5
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.RateLimits["unverified"] != 5 {
		t.Errorf("RateLimits[unverified] = %d, want 5", cfg.RateLimits["unverified"])
	}
	if cfg.RateLimits["declared"] != 60 {
		t.Errorf("RateLimits[declared] = %d, want 60 (Default()'s value, untouched)", cfg.RateLimits["declared"])
	}
}
