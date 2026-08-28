// Package proxy wires the four pillars into one HTTP server: a reverse
// proxy in front of an origin that negotiates content on the fly, serves
// a bulk export and a discovery document, checks identity, and enforces
// published rate limits.
package proxy

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"

	"github.com/justinstimatze/robotsyes/internal/config"
	"github.com/justinstimatze/robotsyes/internal/export"
	"github.com/justinstimatze/robotsyes/internal/identity"
	"github.com/justinstimatze/robotsyes/internal/negotiate"
	"github.com/justinstimatze/robotsyes/internal/ratelimit"
)

const (
	discoveryPath = "/.well-known/robots-yes.json"
	exportPath    = "/.well-known/robots-yes/export.ndjson"
	llmsTxtPath   = "/llms.txt"
)

// Server is the assembled robots.yes proxy.
type Server struct {
	cfg      config.Config
	verifier identity.Verifier
	limiter  *ratelimit.Limiter
	bundler  *export.Bundler
	upstream *httputil.ReverseProxy
	client   *http.Client
	metrics  *serverMetrics
}

// New assembles a Server from a Config. verifier chooses which
// identity.Verifier grants tiers; pass identity.NoopVerifier{} to disable
// pillar 3 entirely, or identity.DeclaredVerifier{} for the unsigned
// self-declaration tier documented in internal/identity.
func New(cfg config.Config, verifier identity.Verifier) (*Server, error) {
	target, err := url.Parse(cfg.Origin)
	if err != nil {
		return nil, err
	}
	limits := make(map[string]ratelimit.Limit, len(cfg.RateLimits))
	for tier, rpm := range cfg.RateLimits {
		limits[tier] = ratelimit.Limit{RequestsPerMinute: rpm}
	}
	ttl := time.Duration(cfg.Export.TTLSeconds) * time.Second
	return &Server{
		cfg:      cfg,
		verifier: verifier,
		limiter:  ratelimit.New(limits),
		bundler: export.NewBundler(export.BundlerConfig{
			Origin:          cfg.Origin,
			Paths:           cfg.Export.Paths,
			TTL:             ttl,
			SitemapURL:      cfg.Export.SitemapURL,
			MaxSitemapPages: cfg.Export.MaxSitemapPages,
		}),
		upstream: httputil.NewSingleHostReverseProxy(target),
		client:   &http.Client{Timeout: 10 * time.Second},
		metrics:  newServerMetrics(),
	}, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == metricsPath {
		s.serveMetrics(w, r)
		return
	}

	id := s.verifier.Verify(r)
	s.metrics.recordRequest(id.Tier)
	tier := string(id.Tier)
	if !s.limiter.Allow(tier, clientKey(r, id)) {
		s.metrics.recordDenied(id.Tier)
		s.writeRateLimited(w, tier)
		return
	}

	switch r.URL.Path {
	case discoveryPath:
		s.serveDiscovery(w, r)
		return
	case exportPath:
		s.bundler.ServeHTTP(w, r)
		return
	case llmsTxtPath:
		s.serveLLMsTxt(w, r)
		return
	}

	w.Header().Set("Vary", "Accept")
	if s.wantsMarkdown(r.Header.Get("Accept"), id.Tier) {
		s.serveMarkdown(w, r)
		return
	}
	s.upstream.ServeHTTP(w, r)
}

// wantsMarkdown decides whether to serve the stripped view. An explicit
// Accept preference always wins (negotiate.WantsMarkdown). When the
// request expresses no preference at all — no Accept header, or a bare
// "*/*" — a self-identified agent (declared or verified) defaults to
// markdown instead of HTML: a signed or declared identity is itself a
// stronger signal of agent intent than an anonymous client's implicit
// "accept anything." An unverified request keeps the current HTML
// default, since there's no signal at all to act on.
func (s *Server) wantsMarkdown(accept string, tier identity.Tier) bool {
	if negotiate.WantsMarkdown(accept) {
		return true
	}
	if !negotiate.ExpressesNoPreference(accept) {
		return false
	}
	return tier == identity.TierDeclared || tier == identity.TierVerified
}

// clientKey identifies a caller for rate-limit bucketing. TierVerified
// keys on the cryptographically-bound AgentID, so a bot keeps its bucket
// across IP changes. Every other tier keys on remote IP instead:
// TierDeclared's AgentID is an unsigned, self-published claim, so keying
// on it there would let one IP mint unlimited buckets (send a new
// claimed identity, get a fresh one) or exhaust a specific bot's bucket
// by replaying its AgentID without ever holding its private key.
func clientKey(r *http.Request, id identity.Identity) string {
	if id.Tier == identity.TierVerified {
		return id.AgentID
	}
	// r.RemoteAddr is "ip:port", and the port is the client's ephemeral
	// source port — different on every TCP connection, even from the
	// same client. Keying on the whole thing gives a fresh bucket to
	// every new connection, which defeats the limiter entirely.
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *Server) writeRateLimited(w http.ResponseWriter, tier string) {
	w.Header().Set("Retry-After", "60")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusTooManyRequests)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error":       "rate limit exceeded",
		"tier":        tier,
		"discover_at": discoveryPath,
	})
}

func (s *Server) serveMarkdown(w http.ResponseWriter, r *http.Request) {
	fullURL := s.cfg.Origin + r.URL.RequestURI()
	upReq, err := http.NewRequestWithContext(r.Context(), r.Method, fullURL, nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	resp, err := s.client.Do(upReq)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	md, err := negotiate.Strip(string(body))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", negotiate.MarkdownType+"; charset=utf-8")
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write([]byte(md))
}

// discovery is the "yes file" — one machine-readable manifest tying the
// four pillars together, so an agent finds the ceiling, the export, and
// the accepted identity methods without probing for them.
type discovery struct {
	Version            string                     `json:"version"`
	ContentNegotiation discoveryNegotiation       `json:"content_negotiation"`
	Export             discoveryExport            `json:"export"`
	Identity           discoveryIdentity          `json:"identity"`
	RateLimits         map[string]ratelimit.Limit `json:"rate_limits"`
}

type discoveryNegotiation struct {
	Supported bool     `json:"supported"`
	Accept    []string `json:"accept"`
	Vary      string   `json:"vary"`
}

type discoveryExport struct {
	URL    string `json:"url"`
	Format string `json:"format"`
}

type discoveryIdentity struct {
	Methods              []string `json:"methods"`
	DeclareVia           string   `json:"declare_via"`
	SignatureInputHeader string   `json:"signature_input_header,omitempty"`
	SignatureHeader      string   `json:"signature_header,omitempty"`
	Algorithm            string   `json:"algorithm,omitempty"`
}

// tierNames renders tiers as the strings the discovery document publishes,
// so the advertised method list can never drift from the identity.Tier
// constants that clientKey and Verify actually key on.
func tierNames(tiers ...identity.Tier) []string {
	names := make([]string, len(tiers))
	for i, t := range tiers {
		names[i] = string(t)
	}
	return names
}

// identityCapabilities describes what the Server can actually check,
// keyed off the concrete Verifier it was built with — so the discovery
// document never advertises a tier the running server can't grant.
func (s *Server) identityCapabilities() discoveryIdentity {
	switch s.verifier.(type) {
	case *identity.SignedVerifier:
		return discoveryIdentity{
			Methods:              tierNames(identity.TierUnverified, identity.TierDeclared, identity.TierVerified),
			DeclareVia:           identity.SignatureAgentHeader,
			SignatureInputHeader: identity.SignatureInputHeader,
			SignatureHeader:      identity.SignatureHeader,
			Algorithm:            "ed25519",
		}
	case identity.DeclaredVerifier:
		return discoveryIdentity{
			Methods:    tierNames(identity.TierUnverified, identity.TierDeclared),
			DeclareVia: identity.SignatureAgentHeader,
		}
	default:
		return discoveryIdentity{Methods: tierNames(identity.TierUnverified)}
	}
}

// serveLLMsTxt is the bridge from the llms.txt convention agents already
// check by habit — HANDOFF.md's own framing is that llms.txt is a "maybe"
// that still points back at the same crawl, so this exists purely to get
// an agent from the path it already knows to check to the actual "yes"
// file, not to duplicate what the discovery document says.
func (s *Server) serveLLMsTxt(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintf(w, "# %s\n\n", s.cfg.Origin)
	fmt.Fprintf(w, "This site publishes a robots.yes capabilities document: content\n")
	fmt.Fprintf(w, "negotiation, bulk export, and published rate limits keyed to identity.\n\n")
	fmt.Fprintf(w, "- Discovery: %s\n", discoveryPath)
	fmt.Fprintf(w, "- Bulk export: %s\n", exportPath)
	fmt.Fprintf(w, "- Content negotiation: send `Accept: %s` on any page for a stripped view\n", negotiate.MarkdownType)
}

func (s *Server) serveDiscovery(w http.ResponseWriter, r *http.Request) {
	doc := discovery{
		Version: "1",
		ContentNegotiation: discoveryNegotiation{
			Supported: true,
			Accept:    []string{negotiate.MarkdownType},
			Vary:      "Accept",
		},
		Export: discoveryExport{
			URL:    exportPath,
			Format: "ndjson",
		},
		Identity:   s.identityCapabilities(),
		RateLimits: s.limiter.Published(),
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(doc)
}
