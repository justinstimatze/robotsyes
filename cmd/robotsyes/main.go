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

	"github.com/justinstimatze/robotsyes/internal/config"
	"github.com/justinstimatze/robotsyes/internal/identity"
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

	cfg := config.Default()
	if *configPath != "" {
		loaded, err := config.Load(*configPath)
		if err != nil {
			log.Fatalf("robotsyes: %v", err)
		}
		cfg = loaded
	}

	srv, err := proxy.New(cfg, identity.DeclaredVerifier{})
	if err != nil {
		log.Fatalf("robotsyes: %v", err)
	}

	log.Printf("robotsyes %s: listening on %s, proxying %s", buildVersion(), cfg.Addr, cfg.Origin)
	if err := http.ListenAndServe(cfg.Addr, srv); err != nil {
		log.Fatalf("robotsyes: %v", err)
	}
}
