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
