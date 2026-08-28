package proxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/justinstimatze/robotsyes/internal/config"
	"github.com/justinstimatze/robotsyes/internal/export"
	"github.com/justinstimatze/robotsyes/internal/identity"
	"github.com/justinstimatze/robotsyes/internal/payments"
)

func testOrigin(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html><body><nav>nav</nav><main><h1>Home</h1><p>hello</p></main></body></html>`))
	})
	mux.HandleFunc("/about", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html><body><main><h1>About</h1></main></body></html>`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func newTestServer(t *testing.T, mutate func(*config.Config)) *Server {
	t.Helper()
	return newTestServerWithMerchant(t, mutate, nil)
}

func newTestServerWithMerchant(t *testing.T, mutate func(*config.Config), merchant payments.Merchant) *Server {
	t.Helper()
	origin := testOrigin(t)
	cfg := config.Default()
	cfg.Origin = origin.URL
	cfg.Export.Paths = []string{"/", "/about"}
	if mutate != nil {
		mutate(&cfg)
	}
	s, err := New(cfg, identity.DeclaredVerifier{}, merchant)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

func TestPassthroughServesRawHTML(t *testing.T) {
	s := newTestServer(t, nil)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept", "text/html")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "<nav>nav</nav>") {
		t.Errorf("expected raw HTML passthrough, got: %s", w.Body.String())
	}
	if got := w.Header().Get("Vary"); got != "Accept" {
		t.Errorf("Vary header = %q, want %q", got, "Accept")
	}
}

func TestMarkdownNegotiationStripsContent(t *testing.T) {
	s := newTestServer(t, nil)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept", "text/markdown")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "# Home") {
		t.Errorf("expected stripped markdown heading, got: %s", body)
	}
	if strings.Contains(body, "nav") {
		t.Errorf("expected nav chrome stripped, got: %s", body)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/markdown") {
		t.Errorf("Content-Type = %q, want text/markdown prefix", ct)
	}
}

func TestNegotiationDefaultsToMarkdownForDeclaredWithNoAcceptPreference(t *testing.T) {
	s := newTestServer(t, nil)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(identity.SignatureAgentHeader, "https://bot.example/card.json")
	// No Accept header at all — the case this default exists for.
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)

	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/markdown") {
		t.Errorf("Content-Type = %q, want text/markdown prefix (declared tier should default to markdown)", ct)
	}
}

func TestNegotiationStaysHTMLForUnverifiedWithNoAcceptPreference(t *testing.T) {
	s := newTestServer(t, nil)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	// No Accept header, and no identity signal either.
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)

	body := w.Body.String()
	if !strings.Contains(body, "<nav>nav</nav>") {
		t.Errorf("expected raw HTML for an unverified request with no Accept preference, got: %s", body)
	}
}

func TestNegotiationExplicitHTMLOverridesTierDefault(t *testing.T) {
	s := newTestServer(t, nil)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(identity.SignatureAgentHeader, "https://bot.example/card.json")
	req.Header.Set("Accept", "text/html")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)

	body := w.Body.String()
	if !strings.Contains(body, "<nav>nav</nav>") {
		t.Errorf("expected an explicit Accept: text/html to override the declared-tier default, got: %s", body)
	}
}

func TestServeLLMsTxt(t *testing.T) {
	s := newTestServer(t, nil)
	req := httptest.NewRequest(http.MethodGet, llmsTxtPath, nil)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q, want text/plain prefix", ct)
	}
	body := w.Body.String()
	if !strings.Contains(body, discoveryPath) {
		t.Errorf("expected body to point at %q, got: %s", discoveryPath, body)
	}
	if !strings.Contains(body, exportPath) {
		t.Errorf("expected body to point at %q, got: %s", exportPath, body)
	}
}

func TestMetricsCountsRequestsByTierAndExcludesItself(t *testing.T) {
	s := newTestServer(t, nil)

	// Two unverified requests, then a scrape — the scrape itself must not
	// bump unverified's count, or /metrics would be lying about its own
	// polling traffic.
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		s.ServeHTTP(httptest.NewRecorder(), req)
	}

	req := httptest.NewRequest(http.MethodGet, metricsPath, nil)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, `robotsyes_requests_total{tier="unverified"} 2`) {
		t.Errorf("expected unverified count of 2, got:\n%s", body)
	}
	if !strings.Contains(body, `robotsyes_requests_total{tier="declared"} 0`) {
		t.Errorf("expected declared count of 0 (the scrape itself shouldn't count), got:\n%s", body)
	}
}

func TestMetricsCountsRateLimitDenials(t *testing.T) {
	s := newTestServer(t, func(c *config.Config) {
		c.RateLimits = map[string]int{"unverified": 1}
	})
	// First request consumes the one token; the second is denied.
	s.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	s.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

	req := httptest.NewRequest(http.MethodGet, metricsPath, nil)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)

	if body := w.Body.String(); !strings.Contains(body, `robotsyes_rate_limit_denied_total{tier="unverified"} 1`) {
		t.Errorf("expected 1 denial recorded, got:\n%s", body)
	}
}

// TestMetricsBypassesRateLimit is the regression test for the claim in
// metricsPath's own doc comment: scraping /metrics must never itself get
// rate-limited, since it's operator infrastructure polling on its own
// schedule, not bot traffic. Proven against a limiter that would reject
// almost anything else — a single request budget, hit many times over.
func TestMetricsBypassesRateLimit(t *testing.T) {
	s := newTestServer(t, func(c *config.Config) {
		c.RateLimits = map[string]int{"unverified": 1}
	})

	for i := 0; i < 10; i++ {
		req := httptest.NewRequest(http.MethodGet, metricsPath, nil)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("scrape %d: status = %d, want 200 (metrics must never be rate-limited)", i, w.Code)
		}
	}

	// The limiter itself still works for everything else: this request
	// (the budget's only slot) succeeds, the next is denied.
	s.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429 (confirms the limiter itself is active, not just skipped globally)", w.Code)
	}
}

func TestMetricsReportsExportBundleStats(t *testing.T) {
	s := newTestServer(t, nil)
	s.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, exportPath, nil))

	req := httptest.NewRequest(http.MethodGet, metricsPath, nil)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)

	body := w.Body.String()
	if !strings.Contains(body, "robotsyes_export_bundle_builds_total 1") {
		t.Errorf("expected 1 bundle build, got:\n%s", body)
	}
	if !strings.Contains(body, "robotsyes_export_bundle_pages 2") {
		t.Errorf("expected 2 pages (matching newTestServer's Export.Paths), got:\n%s", body)
	}
	if !strings.Contains(body, "robotsyes_export_bundle_build_failures_total 0") {
		t.Errorf("expected 0 build failures (nothing failed) and the metric wired through, got:\n%s", body)
	}
}

func TestDiscoveryDocument(t *testing.T) {
	s := newTestServer(t, nil)
	req := httptest.NewRequest(http.MethodGet, discoveryPath, nil)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var doc discovery
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatalf("decoding discovery doc: %v", err)
	}
	if !doc.ContentNegotiation.Supported {
		t.Error("expected content_negotiation.supported = true")
	}
	if doc.Export.URL != exportPath {
		t.Errorf("export.url = %q, want %q", doc.Export.URL, exportPath)
	}
	if doc.Export.ManifestURL != manifestPath {
		t.Errorf("export.manifest_url = %q, want %q", doc.Export.ManifestURL, manifestPath)
	}
	if doc.Export.TorrentURL != "" {
		t.Errorf("export.torrent_url = %q, want empty (torrent not configured)", doc.Export.TorrentURL)
	}
	if _, ok := doc.RateLimits["declared"]; !ok {
		t.Errorf("expected a declared tier in published rate limits, got %v", doc.RateLimits)
	}
}

// TestDiscoveryDocumentTorrentURLWhenEnabled mirrors paymentsCapabilities'
// own rule (never advertise a capability the running server can't back)
// for the torrent route specifically.
func TestDiscoveryDocumentTorrentURLWhenEnabled(t *testing.T) {
	s := newTestServer(t, func(cfg *config.Config) {
		cfg.Export.Torrent.Enabled = true
		cfg.Export.Torrent.PublicURL = "https://example.com"
	})
	req := httptest.NewRequest(http.MethodGet, discoveryPath, nil)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)

	var doc discovery
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatalf("decoding discovery doc: %v", err)
	}
	if doc.Export.TorrentURL != torrentPath {
		t.Errorf("export.torrent_url = %q, want %q", doc.Export.TorrentURL, torrentPath)
	}
}

// TestExportTorrentEndpointsServeThroughServeHTTP confirms both new
// routes — the .torrent itself and the BEP-19 web-seed prefix — are
// actually wired into ServeHTTP's dispatch, not just constructed in
// isolation the way the internal/export tests already cover.
func TestExportTorrentEndpointsServeThroughServeHTTP(t *testing.T) {
	s := newTestServer(t, func(cfg *config.Config) {
		cfg.Export.Torrent.Enabled = true
		cfg.Export.Torrent.PublicURL = "https://example.com"
	})

	req := httptest.NewRequest(http.MethodGet, torrentPath, nil)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET %s: status = %d, want 200", torrentPath, w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/x-bittorrent" {
		t.Errorf("Content-Type = %q, want %q", ct, "application/x-bittorrent")
	}

	// newTestServer's origin bundles "/" and "/about" — pathToSeedKey
	// turns root into "index".
	seedReq := httptest.NewRequest(http.MethodGet, torrentSeedPrefix+"pages/index", nil)
	seedW := httptest.NewRecorder()
	s.ServeHTTP(seedW, seedReq)
	if seedW.Code != http.StatusOK {
		t.Fatalf("GET %spages/index: status = %d, want 200", torrentSeedPrefix, seedW.Code)
	}
	if seedW.Body.Len() == 0 {
		t.Error("seed route returned an empty body")
	}
}

// TestExportManifestEndpointServesManifest confirms the new route is
// actually wired into ServeHTTP's dispatch, not just the discovery
// document that points at it.
func TestExportManifestEndpointServesManifest(t *testing.T) {
	s := newTestServer(t, nil)
	req := httptest.NewRequest(http.MethodGet, manifestPath, nil)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want %q", ct, "application/json")
	}
	var m export.Manifest
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("decoding manifest: %v", err)
	}
	if m.Count != 2 {
		t.Errorf("manifest.count = %d, want 2 (matching newTestServer's configured paths)", m.Count)
	}
}

func TestExportBundleContainsConfiguredPaths(t *testing.T) {
	s := newTestServer(t, nil)
	req := httptest.NewRequest(http.MethodGet, exportPath, nil)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var pages []export.Page
	sc := bufio.NewScanner(bytes.NewReader(w.Body.Bytes()))
	for sc.Scan() {
		var p export.Page
		if err := json.Unmarshal(sc.Bytes(), &p); err != nil {
			t.Fatalf("decoding bundle line: %v", err)
		}
		pages = append(pages, p)
	}
	if len(pages) != 2 {
		t.Fatalf("got %d pages, want 2", len(pages))
	}
	if pages[0].Path != "/" || !strings.Contains(pages[0].Markdown, "# Home") {
		t.Errorf("unexpected first page: %+v", pages[0])
	}
	if pages[1].Path != "/about" || !strings.Contains(pages[1].Markdown, "# About") {
		t.Errorf("unexpected second page: %+v", pages[1])
	}
}

func TestRateLimitReturns429(t *testing.T) {
	s := newTestServer(t, func(c *config.Config) {
		c.RateLimits = map[string]int{"declared": 1}
	})

	req := func() *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("Accept", "text/html")
		r.Header.Set(identity.SignatureAgentHeader, "test-bot")
		return r
	}

	w1 := httptest.NewRecorder()
	s.ServeHTTP(w1, req())
	if w1.Code != http.StatusOK {
		t.Fatalf("first request status = %d, want 200", w1.Code)
	}

	w2 := httptest.NewRecorder()
	s.ServeHTTP(w2, req())
	if w2.Code != http.StatusTooManyRequests {
		t.Fatalf("second request status = %d, want 429", w2.Code)
	}
	if w2.Header().Get("Retry-After") == "" {
		t.Error("expected a Retry-After header on 429")
	}
}

// TestRateLimitKeysOnIPNotPort guards against a real bug found by an
// end-to-end smoke test (not caught by TestRateLimitReturns429, which
// happens to reuse the same httptest.NewRequest RemoteAddr on every
// call): every real TCP connection carries its own ephemeral client
// port, so keying the limiter on the full RemoteAddr gave a fresh bucket
// to every new connection from the same IP and never actually limited
// anything.
func TestRateLimitKeysOnIPNotPort(t *testing.T) {
	s := newTestServer(t, func(c *config.Config) {
		c.RateLimits = map[string]int{"unverified": 1}
	})

	reqFrom := func(port string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("Accept", "text/html")
		r.RemoteAddr = "203.0.113.9:" + port
		return r
	}

	w1 := httptest.NewRecorder()
	s.ServeHTTP(w1, reqFrom("1111"))
	if w1.Code != http.StatusOK {
		t.Fatalf("first request (port 1111) status = %d, want 200", w1.Code)
	}

	w2 := httptest.NewRecorder()
	s.ServeHTTP(w2, reqFrom("2222"))
	if w2.Code != http.StatusTooManyRequests {
		t.Fatalf("second request from same IP, different port (2222) status = %d, want 429", w2.Code)
	}

	w3 := httptest.NewRecorder()
	req3 := httptest.NewRequest(http.MethodGet, "/", nil)
	req3.Header.Set("Accept", "text/html")
	req3.RemoteAddr = "198.51.100.4:1111"
	s.ServeHTTP(w3, req3)
	if w3.Code != http.StatusOK {
		t.Fatalf("request from a genuinely different IP status = %d, want 200", w3.Code)
	}
}

// fakeMerchant is a payments.Merchant test double. Exactly one of
// challenge/settlement/err is set per test, matching the real
// contract's "exactly one of three" shape.
type fakeMerchant struct {
	challenge  *payments.Challenge
	settlement *payments.Settlement
	err        error
	calls      []payments.PaymentRequest
	ctxs       []context.Context // the context each call actually received
}

func (f *fakeMerchant) RequirePayment(ctx context.Context, req payments.PaymentRequest) (*payments.Settlement, *payments.Challenge, error) {
	f.calls = append(f.calls, req)
	f.ctxs = append(f.ctxs, ctx)
	return f.settlement, f.challenge, f.err
}

func rateLimitedRequest() *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Accept", "text/html")
	return r
}

// exhaustLimit forces the next request on s to be rate-limit-denied,
// using the same single-request-budget pattern as TestRateLimitReturns429.
func exhaustLimit(s *Server) {
	s.ServeHTTP(httptest.NewRecorder(), rateLimitedRequest())
}

func TestPaidOverflowChallengeWithoutCredential(t *testing.T) {
	merchant := &fakeMerchant{challenge: &payments.Challenge{
		StatusCode: http.StatusPaymentRequired,
		Body:       map[string]any{"x402": map[string]any{"x402Version": 2}},
	}}
	s := newTestServerWithMerchant(t, func(c *config.Config) {
		c.RateLimits = map[string]int{"unverified": 1}
	}, merchant)
	exhaustLimit(s)

	w := httptest.NewRecorder()
	s.ServeHTTP(w, rateLimitedRequest())

	if w.Code != http.StatusPaymentRequired {
		t.Fatalf("status = %d, want 402", w.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding 402 body: %v", err)
	}
	if _, ok := body["x402"]; !ok {
		t.Errorf("expected an x402 key in the challenge body, got: %v", body)
	}
	if len(merchant.calls) != 1 || merchant.calls[0].Credential != "" {
		t.Errorf("expected one call with no credential, got: %+v", merchant.calls)
	}

	metricsBody := scrapeMetrics(t, s)
	if !strings.Contains(metricsBody, "robotsyes_x402_challenged_total 1") {
		t.Errorf("expected 1 challenge recorded, got:\n%s", metricsBody)
	}
	if strings.Contains(metricsBody, `robotsyes_rate_limit_denied_total{tier="unverified"} 1`) {
		t.Errorf("a challenge is not a plain denial and should not double-count in deniedByTier, got:\n%s", metricsBody)
	}
}

func TestPaidOverflowSettlesAndServesRequest(t *testing.T) {
	merchant := &fakeMerchant{settlement: &payments.Settlement{PayerAddress: "0xpayer"}}
	s := newTestServerWithMerchant(t, func(c *config.Config) {
		c.RateLimits = map[string]int{"unverified": 1}
	}, merchant)
	exhaustLimit(s)

	req := rateLimitedRequest()
	req.Header.Set("Payment-Signature", "a-settle-credential")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (a settled payment should serve the request)", w.Code)
	}
	if len(merchant.calls) != 1 || merchant.calls[0].Credential != "a-settle-credential" {
		t.Errorf("expected one call carrying the presented credential, got: %+v", merchant.calls)
	}

	metricsBody := scrapeMetrics(t, s)
	if !strings.Contains(metricsBody, "robotsyes_x402_settled_total 1") {
		t.Errorf("expected 1 settlement recorded, got:\n%s", metricsBody)
	}
}

// TestPaidOverflowFallsBackTo429OnMerchantError guards the fail-closed
// contract: an infrastructure error from the merchant must never read as
// "let the request through," only ever as today's flat 429.
func TestPaidOverflowFallsBackTo429OnMerchantError(t *testing.T) {
	merchant := &fakeMerchant{err: errors.New("boom")}
	s := newTestServerWithMerchant(t, func(c *config.Config) {
		c.RateLimits = map[string]int{"unverified": 1}
	}, merchant)
	exhaustLimit(s)

	req := rateLimitedRequest()
	req.Header.Set("Payment-Signature", "whatever")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)

	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 (merchant error must fail closed)", w.Code)
	}
}

// TestPaidOverflowBoundsMerchantCallWithATimeout guards against the
// unbounded-connection-hold finding: without a deadline shorter than a
// Merchant implementation's own client timeout, a slow settlement
// endpoint could hold a request open for as long as that implementation
// is willing to wait. This asserts the context handleRateLimited passes
// to RequirePayment actually carries a deadline — not that the timeout
// fires, which would need the test to wait out the real duration.
func TestPaidOverflowBoundsMerchantCallWithATimeout(t *testing.T) {
	merchant := &fakeMerchant{settlement: &payments.Settlement{PayerAddress: "0xpayer"}}
	s := newTestServerWithMerchant(t, func(c *config.Config) {
		c.RateLimits = map[string]int{"unverified": 1}
	}, merchant)
	exhaustLimit(s)

	before := time.Now()
	req := rateLimitedRequest()
	req.Header.Set("Payment-Signature", "whatever")
	s.ServeHTTP(httptest.NewRecorder(), req)

	if len(merchant.ctxs) != 1 {
		t.Fatalf("expected exactly one RequirePayment call, got %d", len(merchant.ctxs))
	}
	deadline, ok := merchant.ctxs[0].Deadline()
	if !ok {
		t.Fatal("expected the context passed to RequirePayment to carry a deadline")
	}
	// A little slack above paymentRequestTimeout: `before` is captured
	// slightly earlier than context.WithTimeout's own now(), inside
	// handleRateLimited.
	const slack = 200 * time.Millisecond
	if got := deadline.Sub(before); got <= 0 || got > paymentRequestTimeout+slack {
		t.Errorf("deadline is %v out from the call, want (0, %v]", got, paymentRequestTimeout+slack)
	}
}

func TestPaymentCredentialPrefersPaymentSignatureHeader(t *testing.T) {
	h := http.Header{}
	h.Set("X-Payment", "legacy")
	h.Set("Payment-Signature", "current")
	if got := paymentCredential(h); got != "current" {
		t.Errorf("paymentCredential = %q, want the Payment-Signature value", got)
	}
}

func TestPaymentCredentialFallsBackToXPayment(t *testing.T) {
	h := http.Header{}
	h.Set("X-Payment", "legacy")
	if got := paymentCredential(h); got != "legacy" {
		t.Errorf("paymentCredential = %q, want the X-Payment fallback", got)
	}
}

func TestDiscoveryOmitsPaymentsWhenNotConfigured(t *testing.T) {
	s := newTestServer(t, nil)
	req := httptest.NewRequest(http.MethodGet, discoveryPath, nil)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)

	var doc discovery
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatalf("decoding discovery doc: %v", err)
	}
	if doc.Payments.Supported {
		t.Error("expected payments.supported = false with no merchant configured")
	}
}

func TestDiscoveryPublishesPaymentsWhenConfigured(t *testing.T) {
	merchant := &fakeMerchant{}
	s := newTestServerWithMerchant(t, func(c *config.Config) {
		c.Payments = config.PaymentsConfig{Enabled: true, Network: "eip155:8453", Asset: "0xasset", PriceCentsPerRequest: 5}
	}, merchant)
	req := httptest.NewRequest(http.MethodGet, discoveryPath, nil)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)

	var doc discovery
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatalf("decoding discovery doc: %v", err)
	}
	if !doc.Payments.Supported {
		t.Fatal("expected payments.supported = true with a merchant configured")
	}
	if doc.Payments.PriceCentsPerRequest != 5 {
		t.Errorf("price_cents_per_request = %d, want 5", doc.Payments.PriceCentsPerRequest)
	}
	if doc.Payments.Header == "" {
		t.Error("expected a non-empty header hint")
	}
}

// scrapeMetrics fetches metricsPath and returns the raw Prometheus text
// body, the same shape TestMetricsCountsRateLimitDenials asserts against.
func scrapeMetrics(t *testing.T, s *Server) string {
	t.Helper()
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodGet, metricsPath, nil))
	return w.Body.String()
}
