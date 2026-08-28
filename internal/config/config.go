// Package config loads robotsyes' YAML configuration: the origin to
// proxy, the listen address, which paths to bundle for bulk export, and
// the published rate-limit ceiling per identity tier.
package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
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
}

// ExportConfig configures pillar 2: bulk/structured export.
type ExportConfig struct {
	// Paths are fetched from Origin, stripped, and bundled.
	Paths []string `yaml:"paths"`
	// TTLSeconds is how long a bundle is served before being rebuilt.
	TTLSeconds int `yaml:"ttl_seconds"`
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
			"unverified": 10,
			"declared":   60,
			"verified":   300,
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
