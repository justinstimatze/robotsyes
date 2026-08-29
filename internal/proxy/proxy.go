// Package proxy wires the four pillars into one HTTP server: a reverse
// proxy in front of an origin that negotiates content on the fly, serves
// a bulk export and a discovery document, checks identity, and enforces
// published rate limits.
package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/justinstimatze/robotsyes/internal/config"
	"github.com/justinstimatze/robotsyes/internal/export"
	"github.com/justinstimatze/robotsyes/internal/httpx"
	"github.com/justinstimatze/robotsyes/internal/identity"
	"github.com/justinstimatze/robotsyes/internal/negotiate"
	"github.com/justinstimatze/robotsyes/internal/payments"
	"github.com/justinstimatze/robotsyes/internal/ratelimit"
)

const (
	discoveryPath = "/.well-known/robots-yes.json"
	exportPath    = "/.well-known/robots-yes/export.ndjson"
	manifestPath  = "/.well-known/robots-yes/export-manifest.json"
	torrentPath   = "/.well-known/robots-yes/export.torrent"
	llmsTxtPath   = "/llms.txt"

	// torrentSeedPrefix is the BEP-19 web-seed target: every page's raw
	// markdown, served unconditionally (no content negotiation), so the
	// bytes a torrent client GETs always match the piece hashes
	// export.buildTorrentInfo computed over the same content. The
	// internal/export package doesn't reference this constant itself —
	// it can't import internal/proxy back without a cycle — so New()
	// joins it with the operator's configured public_url once, here,
	// and passes the whole thing through as BundlerConfig.TorrentSeedBaseURL.
	torrentSeedPrefix = "/.well-known/robots-yes/torrent-seed/"

	// paymentRequestTimeout bounds a Merchant.RequirePayment call. A
	// real settle call is a network round trip to a third-party
	// authorization server (see internal/paymentgate/chitgate's package
	// doc); without a bound here, a slow or unresponsive one would hold
	// the request open for as long as the Merchant implementation's own
	// client timeout allows (chit's own default is 65s). This is
	// deliberately tighter than that default and than the server's own
	// WriteTimeout (see cmd/robotsyes/main.go), so a timeout here
	// produces this package's own clean 429 rather than the server
	// aborting the connection uncleanly.
	paymentRequestTimeout = 15 * time.Second
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
	payments payments.Merchant
}

// New assembles a Server from a Config. verifier chooses which
// identity.Verifier grants tiers; pass identity.NoopVerifier{} to disable
// pillar 3 entirely, or identity.DeclaredVerifier{} for the unsigned
// self-declaration tier documented in internal/identity. merchant enables
// pillar 4's paid-overflow path (see handleRateLimited); pass nil to
// disable it — a rate-limit denial then behaves exactly as it always
// has, a flat 429.
func New(cfg config.Config, verifier identity.Verifier, merchant payments.Merchant) (*Server, error) {
	target, err := url.Parse(cfg.Origin)
	if err != nil {
		return nil, err
	}
	limits := make(map[string]ratelimit.Limit, len(cfg.RateLimits))
	for tier, rpm := range cfg.RateLimits {
		limits[tier] = ratelimit.Limit{RequestsPerMinute: rpm}
	}
	ttl := time.Duration(cfg.Export.TTLSeconds) * time.Second
	var seedBaseURL string
	if cfg.Export.Torrent.Enabled {
		seedBaseURL = strings.TrimSuffix(cfg.Export.Torrent.PublicURL, "/") + torrentSeedPrefix
	}
	return &Server{
		cfg:      cfg,
		verifier: verifier,
		limiter:  ratelimit.New(limits, ratelimit.DefaultMaxBucketEntries),
		bundler: export.NewBundler(export.BundlerConfig{
			Origin:             cfg.Origin,
			Paths:              cfg.Export.Paths,
			TTL:                ttl,
			SitemapURL:         cfg.Export.SitemapURL,
			MaxSitemapPages:    cfg.Export.MaxSitemapPages,
			TorrentEnabled:     cfg.Export.Torrent.Enabled,
			TorrentSeedBaseURL: seedBaseURL,
			TorrentTrackers:    cfg.Export.Torrent.Trackers,
		}),
		upstream: httputil.NewSingleHostReverseProxy(target),
		client:   &http.Client{Timeout: 10 * time.Second},
		metrics:  newServerMetrics(),
		payments: merchant,
	}, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == metricsPath {
		s.serveMetrics(w, r)
		return
	}

	id := s.verifier.Verify(r)
	s.metrics.recordRequest(id.Tier)
	if !s.limiter.Allow(string(id.Tier), clientKey(r, id)) {
		if !s.handleRateLimited(w, r, id.Tier) {
			return
		}
		// Paid: fall through and serve this one request, bypassing the
		// token bucket entirely — a settled payment is its own grant,
		// not a bucket refill.
	}

	if s.serveWellKnown(w, r) {
		return
	}

	w.Header().Set("Vary", "Accept")
	if s.wantsMarkdown(r.Header.Get("Accept"), id.Tier) {
		s.serveMarkdown(w, r)
		return
	}
	s.upstream.ServeHTTP(w, r)
}

// serveWellKnown dispatches the "yes file" machine-readable paths
// (discovery, bulk export, its manifest, llms.txt) and reports whether
// it handled the request. Split out of ServeHTTP so this switch's own
// branch count doesn't push the dispatcher itself over CodeScene's
// complexity threshold as more well-known paths are added — it already
// did once, adding manifestPath.
func (s *Server) serveWellKnown(w http.ResponseWriter, r *http.Request) bool {
	if strings.HasPrefix(r.URL.Path, torrentSeedPrefix) {
		s.bundler.ServeTorrentSeed(w, r, strings.TrimPrefix(r.URL.Path, torrentSeedPrefix))
		return true
	}
	switch r.URL.Path {
	case discoveryPath:
		s.serveDiscovery(w, r)
	case exportPath:
		s.bundler.ServeHTTP(w, r)
	case manifestPath:
		s.bundler.ServeManifest(w, r)
	case torrentPath:
		s.bundler.ServeTorrent(w, r)
	case llmsTxtPath:
		s.serveLLMsTxt(w, r)
	default:
		return false
	}
	return true
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
	return httpx.RemoteIP(r)
}

// handleRateLimited responds to a request the rate limiter denied. With
// no payment merchant configured (s.payments == nil), this is
// byte-for-byte the same 429 this project has always returned. When one
// is configured: a request with no payment credential gets a 402
// challenge instead of the flat 429; a request presenting a credential
// that settles is let through this one time (paid == true) — ServeHTTP
// then serves it without re-checking the limiter. Any infrastructure
// error, or a Merchant returning neither a settlement nor a challenge,
// fails closed to the existing 429 rather than surface an internal error
// or let the request through unpaid.
func (s *Server) handleRateLimited(w http.ResponseWriter, r *http.Request, tier identity.Tier) (paid bool) {
	if s.payments == nil {
		s.metrics.recordDenied(tier)
		s.writeRateLimited(w, string(tier))
		return false
	}
	ctx, cancel := context.WithTimeout(r.Context(), paymentRequestTimeout)
	defer cancel()
	settlement, challenge, err := s.payments.RequirePayment(ctx, payments.PaymentRequest{
		Resource:   r.URL.Path,
		Credential: paymentCredential(r.Header),
	})
	if !paymentGranted(err, settlement, challenge) {
		s.metrics.recordDenied(tier)
		s.writeRateLimited(w, string(tier))
		return false
	}
	if challenge != nil {
		s.metrics.recordPaymentChallenged()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(challenge.StatusCode)
		_ = json.NewEncoder(w).Encode(challenge.Body)
		return false
	}
	s.metrics.recordPaymentSettled()
	return true
}

// paymentGranted reports whether a payments.Merchant.RequirePayment
// result counts as "let this request through". The interface contract
// is exactly one of (Settlement, nil, nil) / (nil, Challenge, nil) /
// (nil, nil, err) — settlement == nil && challenge == nil (with err ==
// nil) guards against a Merchant implementation violating that
// contract. In a payment gate, an ambiguous result must deny, not
// allow.
func paymentGranted(err error, settlement *payments.Settlement, challenge *payments.Challenge) bool {
	return err == nil && (settlement != nil || challenge != nil)
}

// paymentCredential extracts a settle credential from the request's
// payment header, checking the same precedence chit's own
// DetectProtocol uses for x402: Payment-Signature (the current wire
// name) first, falling back to X-Payment (the legacy name real clients
// still send) — no new parsing convention invented here.
func paymentCredential(h http.Header) string {
	if v := h.Get("Payment-Signature"); v != "" {
		return v
	}
	return h.Get("X-Payment")
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

// serveMarkdown forwards the request to s.cfg.Origin, a fixed operator
// config value — RequestURI() contributes only the path and query, never
// a host, so a client can't redirect this request to an arbitrary host
// no matter what it sends. gosec's generic SSRF heuristic can't see that
// distinction, hence the annotations below.
func (s *Server) serveMarkdown(w http.ResponseWriter, r *http.Request) {
	fullURL := s.cfg.Origin + r.URL.RequestURI()
	upReq, err := http.NewRequestWithContext(r.Context(), r.Method, fullURL, nil) //nolint:gosec // see func doc
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	resp, err := s.client.Do(upReq) //nolint:gosec // see func doc

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
	Payments           discoveryPayments          `json:"payments"`
}

type discoveryNegotiation struct {
	Supported bool     `json:"supported"`
	Accept    []string `json:"accept"`
	Vary      string   `json:"vary"`
}

type discoveryExport struct {
	URL    string `json:"url"`
	Format string `json:"format"`
	// ManifestURL points at the per-page hash/size listing (see
	// internal/export.Manifest) — lets a bot detect an unchanged bundle
	// in one comparison, or selectively fetch a subtree via the
	// per-path content-negotiation route, instead of re-downloading and
	// diffing the whole bundle on every crawl.
	ManifestURL string `json:"manifest_url"`
	// TorrentURL points at a real, BEP-19 web-seeded .torrent covering
	// the same pages — omitted entirely when export.torrent.enabled is
	// false, so the document never advertises a capability the running
	// server can't back.
	TorrentURL string `json:"torrent_url,omitempty"`
}

type discoveryIdentity struct {
	Methods              []string `json:"methods"`
	DeclareVia           string   `json:"declare_via"`
	SignatureInputHeader string   `json:"signature_input_header,omitempty"`
	SignatureHeader      string   `json:"signature_header,omitempty"`
	Algorithm            string   `json:"algorithm,omitempty"`
	// CardDiscovery is the well-known path convention a Signature Agent
	// Card is expected to be published at, per the WebBotAuth registry
	// draft — the Signature-Agent header can still name any https URL,
	// this is just where a real signer conventionally already puts it.
	CardDiscovery string `json:"card_discovery,omitempty"`
	// RequiredSignatureParams are the Signature-Input parameters this
	// server requires and checks: a request missing any of these, or
	// carrying the wrong Algorithm/Tag, degrades to TierDeclared exactly
	// as an unsigned claim would.
	RequiredSignatureParams []string `json:"required_signature_params,omitempty"`
	Tag                     string   `json:"tag,omitempty"`
	// Spec names the wire format this server actually speaks, for an
	// implementer who wants the authoritative definition rather than
	// this document's summary of it.
	Spec string `json:"spec,omitempty"`
}

// discoveryPayments describes pillar 4's optional paid-overflow path —
// so a well-behaved bot learns the price past its free ceiling the same
// way it already learns the ceiling itself, instead of discovering it by
// tripping a 402.
type discoveryPayments struct {
	Supported            bool   `json:"supported"`
	Network              string `json:"network,omitempty"`
	Asset                string `json:"asset,omitempty"`
	PriceCentsPerRequest int64  `json:"price_cents_per_request,omitempty"`
	// Header names the header a paying retry should carry its settle
	// credential in. Payment-Signature and X-Payment (see
	// paymentCredential) are both accepted; this names the current one.
	Header string `json:"header,omitempty"`
}

// torrentURL returns torrentPath when export.torrent.enabled, or "" —
// which discoveryExport's omitempty tag drops entirely — otherwise.
func (s *Server) torrentURL() string {
	if !s.cfg.Export.Torrent.Enabled {
		return ""
	}
	return torrentPath
}

// paymentsCapabilities reports what the discovery document publishes for
// pillar 4's paid-overflow path — populated only when a merchant is
// actually configured, so the document never advertises a price the
// running server can't collect.
func (s *Server) paymentsCapabilities() discoveryPayments {
	if s.payments == nil {
		return discoveryPayments{Supported: false}
	}
	return discoveryPayments{
		Supported:            true,
		Network:              s.cfg.Payments.Network,
		Asset:                s.cfg.Payments.Asset,
		PriceCentsPerRequest: s.cfg.Payments.PriceCentsPerRequest,
		Header:               "Payment-Signature",
	}
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
			Methods:                 tierNames(identity.TierUnverified, identity.TierDeclared, identity.TierVerified),
			DeclareVia:              identity.SignatureAgentHeader,
			SignatureInputHeader:    identity.SignatureInputHeader,
			SignatureHeader:         identity.SignatureHeader,
			Algorithm:               "ed25519",
			CardDiscovery:           "/.well-known/signature-agent-card",
			RequiredSignatureParams: []string{"created", "expires", "keyid", "alg", "nonce", "tag"},
			Tag:                     "web-bot-auth",
			Spec:                    "draft-meunier-web-bot-auth-architecture (IETF), wire-compatible incl. Cloudflare/Anthropic/OpenAI implementations",
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
// check by habit — this exists purely to get an agent from the path it
// already knows to check to the actual discovery document, not to
// duplicate what that document says.
func (s *Server) serveLLMsTxt(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintf(w, "# %s\n\n", s.cfg.Origin)
	fmt.Fprintf(w, "This site publishes a robots.yes capabilities document: content\n")
	fmt.Fprintf(w, "negotiation, bulk export, and published rate limits keyed to identity.\n\n")
	fmt.Fprintf(w, "- Discovery: %s\n", discoveryPath)
	fmt.Fprintf(w, "- Bulk export: %s\n", exportPath)
	fmt.Fprintf(w, "- Export manifest: %s\n", manifestPath)
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
			URL:         exportPath,
			Format:      "ndjson",
			ManifestURL: manifestPath,
			TorrentURL:  s.torrentURL(),
		},
		Identity:   s.identityCapabilities(),
		RateLimits: s.limiter.Published(),
		Payments:   s.paymentsCapabilities(),
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(doc)
}
