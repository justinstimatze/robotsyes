// Command robotsyes is a self-hostable reverse proxy that answers content
// negotiation, serves a bulk/structured export, and publishes graduated
// rate limits in front of any origin — the "yes" file's reference
// implementation.
package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"runtime/debug"
	"time"

	"github.com/justinstimatze/robotsyes/internal/config"
	"github.com/justinstimatze/robotsyes/internal/identity"
	"github.com/justinstimatze/robotsyes/internal/paymentgate/chitgate"
	"github.com/justinstimatze/robotsyes/internal/payments"
	"github.com/justinstimatze/robotsyes/internal/proxy"
)

// version is "dev" by default and baked at release time via
//
//	go install -ldflags "-X main.version=$(git describe --tags --always --dirty)" ./cmd/robotsyes
//
// The git tag is the single source of truth — there is no hand-maintained
// version constant to drift out of sync. buildVersion() resolves it.
var version = "dev"

func buildVersion() string {
	if version != "dev" {
		return version
	}
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return version
	}
	if v := bi.Main.Version; v != "" && v != "(devel)" {
		return v
	}
	var rev, dirty string
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			if len(s.Value) >= 12 {
				rev = s.Value[:12]
			} else {
				rev = s.Value
			}
		case "vcs.modified":
			if s.Value == "true" {
				dirty = "-dirty"
			}
		}
	}
	if rev != "" {
		return rev + dirty
	}
	return version
}

func main() {
	if len(os.Args) < 2 {
		usage(os.Stderr)
		os.Exit(1)
	}
	switch os.Args[1] {
	case "serve":
		cmdServe(os.Args[2:])
	case "version":
		fmt.Println(buildVersion())
	default:
		usage(os.Stderr)
		os.Exit(1)
	}
}

func usage(w io.Writer) {
	fmt.Fprintln(w, "usage: robotsyes <command>")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "commands:")
	fmt.Fprintln(w, "  serve [-config path]   run the proxy")
	fmt.Fprintln(w, "  version                print the build version")
}

func cmdServe(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	configPath := fs.String("config", "", "path to robotsyes.yaml (defaults if omitted)")
	_ = fs.Parse(args)

	cfg := loadConfig(*configPath)

	cards := identity.NewCardFetcher(5*time.Minute, identity.DefaultMaxCardCacheEntries)
	verifier := identity.NewSignedVerifier(cards)
	merchant := buildMerchant(&cfg)

	srv, err := proxy.New(cfg, verifier, merchant)
	if err != nil {
		log.Fatalf("robotsyes: %v", err)
	}

	serve(cfg, srv)
}

// loadConfig returns config.Default() when path is empty (a bare
// `robotsyes serve` with no config file), otherwise loads and parses
// path, exiting fatally on error.
func loadConfig(path string) config.Config {
	if path == "" {
		return config.Default()
	}
	cfg, err := config.Load(path)
	if err != nil {
		log.Fatalf("robotsyes: %v", err)
	}
	return cfg
}

// buildMerchant returns nil when payments are disabled — proxy.New
// treats a nil Merchant as "no paid-overflow tier configured". Resolves
// Network/Asset defaults into cfg here, once, so both the merchant and
// the discovery document (which reads cfg.Payments back out of the
// Server) agree on the effective values instead of the discovery doc
// publishing an empty string when the operator relied on chitgate's
// built-in default.
func buildMerchant(cfg *config.Config) payments.Merchant {
	if !cfg.Payments.Enabled {
		return nil
	}
	if cfg.Payments.Network == "" {
		cfg.Payments.Network = chitgate.DefaultNetwork
	}
	if cfg.Payments.Asset == "" {
		cfg.Payments.Asset = chitgate.DefaultAsset
	}
	m, err := chitgate.New(chitgate.Config{
		PayoutAddress:        cfg.Payments.PayoutAddress,
		PriceCentsPerRequest: cfg.Payments.PriceCentsPerRequest,
		Network:              cfg.Payments.Network,
		Asset:                cfg.Payments.Asset,
	})
	if err != nil {
		log.Fatalf("robotsyes: %v", err)
	}
	return m
}

// serve starts the HTTP server and blocks until it exits.
//
// A bare http.ListenAndServe has no read/write/idle timeouts at all — a
// client that opens a connection and never finishes sending, or never
// reads the response, can hold it open indefinitely. That matters more
// now than it used to: the paid-overflow path (see
// proxy.handleRateLimited) can make a real outbound call to a
// third-party settlement endpoint, and without a bound here a slow
// client sitting on that request ties up the goroutine for as long as
// the client is willing to wait. WriteTimeout is set comfortably above
// proxy.paymentRequestTimeout so that timeout's own clean 429 has time
// to be written before this blunter one would abort the connection.
func serve(cfg config.Config, srv http.Handler) {
	log.Printf("robotsyes %s: listening on %s, proxying %s", buildVersion(), cfg.Addr, cfg.Origin)
	httpServer := &http.Server{
		Addr:              cfg.Addr,
		Handler:           srv,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	if err := httpServer.ListenAndServe(); err != nil {
		log.Fatalf("robotsyes: %v", err)
	}
}
