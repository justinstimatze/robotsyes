package proxy

import (
	"fmt"
	"net/http"

	"github.com/justinstimatze/robotsyes/internal/identity"
	"github.com/justinstimatze/robotsyes/internal/metrics"
)

// metricsPath is deliberately outside the tier-counted, rate-limited
// request path (see ServeHTTP): it's operator infrastructure polling on
// its own schedule, not part of the bot-facing contract, and counting its
// own scrapes into requests_total would pollute the very numbers it
// exists to report.
const metricsPath = "/.well-known/robots-yes/metrics"

// knownTiers is every tier a Verifier can currently grant — fixed at
// startup so serverMetrics' maps need no locking of their own beyond what
// each Counter already does.
var knownTiers = []identity.Tier{identity.TierUnverified, identity.TierDeclared, identity.TierVerified}

// serverMetrics is the operator-facing counterpart to identityCapabilities:
// where that reports what the server CAN grant, this reports what it
// ACTUALLY has — how much traffic showed up at each tier, and how much of
// it got turned away. Answers the question an operator deciding whether
// to keep this proxy running actually has: is bulk export saving me
// anything, and how much of my traffic is verified.
type serverMetrics struct {
	requestsByTier map[identity.Tier]*metrics.Counter
	deniedByTier   map[identity.Tier]*metrics.Counter
	// x402Challenged/x402Settled count pillar 4's paid-overflow path
	// (see handleRateLimited). Not per-tier: a payment credential is
	// its own grant, independent of identity tier.
	x402Challenged metrics.Counter
	x402Settled    metrics.Counter
}

func newServerMetrics() *serverMetrics {
	m := &serverMetrics{
		requestsByTier: make(map[identity.Tier]*metrics.Counter, len(knownTiers)),
		deniedByTier:   make(map[identity.Tier]*metrics.Counter, len(knownTiers)),
	}
	for _, t := range knownTiers {
		m.requestsByTier[t] = &metrics.Counter{}
		m.deniedByTier[t] = &metrics.Counter{}
	}
	return m
}

func (m *serverMetrics) recordRequest(tier identity.Tier) {
	if c, ok := m.requestsByTier[tier]; ok {
		c.Inc()
	}
}

func (m *serverMetrics) recordDenied(tier identity.Tier) {
	if c, ok := m.deniedByTier[tier]; ok {
		c.Inc()
	}
}

func (m *serverMetrics) recordPaymentChallenged() { m.x402Challenged.Inc() }

func (m *serverMetrics) recordPaymentSettled() { m.x402Settled.Inc() }

// serveMetrics renders counters in Prometheus text exposition format —
// no client library, since the project's whole dependency footprint is
// two libraries and a handful of counters doesn't need a third.
func (s *Server) serveMetrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")

	fmt.Fprintln(w, "# HELP robotsyes_requests_total Requests handled, by identity tier.")
	fmt.Fprintln(w, "# TYPE robotsyes_requests_total counter")
	for _, t := range knownTiers {
		fmt.Fprintf(w, "robotsyes_requests_total{tier=%q} %d\n", t, s.metrics.requestsByTier[t].Value())
	}

	fmt.Fprintln(w, "# HELP robotsyes_rate_limit_denied_total Requests denied by the rate limiter, by identity tier.")
	fmt.Fprintln(w, "# TYPE robotsyes_rate_limit_denied_total counter")
	for _, t := range knownTiers {
		fmt.Fprintf(w, "robotsyes_rate_limit_denied_total{tier=%q} %d\n", t, s.metrics.deniedByTier[t].Value())
	}

	fmt.Fprintln(w, "# HELP robotsyes_x402_challenged_total Rate-limit-denied requests offered a payment challenge instead of a flat 429.")
	fmt.Fprintln(w, "# TYPE robotsyes_x402_challenged_total counter")
	fmt.Fprintf(w, "robotsyes_x402_challenged_total %d\n", s.metrics.x402Challenged.Value())

	fmt.Fprintln(w, "# HELP robotsyes_x402_settled_total Payment challenges that settled and let the request through.")
	fmt.Fprintln(w, "# TYPE robotsyes_x402_settled_total counter")
	fmt.Fprintf(w, "robotsyes_x402_settled_total %d\n", s.metrics.x402Settled.Value())

	fmt.Fprintln(w, "# HELP robotsyes_export_bundle_builds_total Times the bulk export bundle was actually rebuilt, not served from cache.")
	fmt.Fprintln(w, "# TYPE robotsyes_export_bundle_builds_total counter")
	fmt.Fprintf(w, "robotsyes_export_bundle_builds_total %d\n", s.bundler.Builds())

	fmt.Fprintln(w, "# HELP robotsyes_export_bundle_pages Pages in the currently cached export bundle.")
	fmt.Fprintln(w, "# TYPE robotsyes_export_bundle_pages gauge")
	fmt.Fprintf(w, "robotsyes_export_bundle_pages %d\n", s.bundler.PageCount())

	fmt.Fprintln(w, "# HELP robotsyes_export_bundle_build_failures_total Bundle rebuild attempts that failed. A stale bundle keeps being served while this climbs, so builds_total alone can look merely stalled rather than actively failing.")
	fmt.Fprintln(w, "# TYPE robotsyes_export_bundle_build_failures_total counter")
	fmt.Fprintf(w, "robotsyes_export_bundle_build_failures_total %d\n", s.bundler.BuildFailures())
}
