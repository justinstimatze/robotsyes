# Changelog

## Unreleased

- Content negotiation now defaults toward markdown for a self-identified
  agent instead of only reacting to an explicit `Accept: text/markdown`.
  `negotiate.ExpressesNoPreference` reports when the Accept header states
  no real choice at all (absent, or a bare `*/*`); `Server.wantsMarkdown`
  falls back to markdown in that case only when the identity tier is
  declared or verified. An explicit Accept preference — for HTML, or for
  anything else — always wins over the tier default; this only fills the
  gap where nothing was actually asked for.
- Bulk export gzips its ndjson response when `Accept-Encoding` allows it
  (respecting an explicit `q=0`) — the whole pillar is a bandwidth
  argument, and ndjson full of repeated JSON keys and HTML-derived
  markdown compresses well.
- Added `/.well-known/robots-yes/metrics`: request counts by identity
  tier, rate-limit denials by tier, and export-bundle build/page counts,
  in Prometheus text format via a new dependency-free `internal/metrics`
  package (no client library — the project's whole footprint was two
  dependencies before this). The endpoint itself is excluded from its own
  counters and from rate limiting, since it's operator infrastructure
  polling on its own schedule, not part of the bot-facing contract.
- Pillar 2 covers the long tail now instead of requiring every path to be
  hand-listed: `export.ExportConfig.SitemapURL` (+ `MaxSitemapPages`,
  default 1000) fetches a sitemap on every rebuild and bundles what it
  finds, following one level of `sitemapindex` (the shape real large sites
  actually publish — a list of per-section child sitemaps, not one flat
  file). A `<loc>` pointing at a different host than `Origin` is skipped,
  not bundled. Sitemap-discovered paths fail individually rather than
  aborting the whole bundle — auto-discovered input is expected to carry
  some noise (a stale entry, a page that started 404ing) — while the
  original hand-listed `Paths` still fail the whole bundle on any error,
  since that's a deliberate operator list where a broken entry is signal.
  `NewBundler` now takes a `BundlerConfig` struct instead of three
  positional args, since it was about to grow past four.
- Added `/llms.txt` as a compatibility bridge: today's crawlers already
  check that path by convention (HANDOFF.md's own framing: "a 'maybe'
  that still points back at the same crawl"), so this exists purely to
  point them at the real discovery document rather than duplicating it.
- Extracted `internal/httpx.GetBounded` — the "GET a URL, cap the response
  size" idiom that `export.Bundler.fetchAndStrip` and
  `identity.CardFetcher.Fetch` had each independently implemented (the
  exact drift calque's first scan caught, `.calque/registry.md` #17).
  Both now call the one shared function; the SSRF-safe dialer and
  https-only check for card URLs still live entirely in
  `identity.CardFetcher`'s own `http.Client`, upstream of the shared read.
- Code-health pass: `SignedVerifier.Verify` split into `verifySignature` /
  `verifyAgainstCard` / `withinSkew` (cyclomatic complexity 12 → each
  well under CodeScene's threshold of 9); `parseSignatureInput` rewritten
  to parse into a map first instead of a switch nested inside a for loop
  (clears the "Bumpy Road" nesting flag); `signRequest`'s five test
  arguments collapsed into a `signParams` struct; the two structurally
  identical "degrades to declared" tests now share an
  `assertDegradesToDeclared` helper instead of repeating the same
  assertion body.
- Wired `calque` in as a second pre-commit gate (warn-only, alongside
  CodeScene) and adjudicated its first scan: of 18 suspect pairs and one
  8-member cluster, 16 were false alarms (Go/stdlib interface methods
  forced to share a signature, same-receiver helper/caller pairs, or a
  function paired with its own test) — recorded in `.calque/registry.md`
  so they don't re-surface. Two were real: `config.Default` and
  `Server.identityCapabilities` both hand-typed the tier name strings
  ("unverified"/"declared"/"verified") independently of the
  `identity.Tier` constants that actually define them — a rename in
  `identity.go` wouldn't have touched either copy. Fixed by routing both
  through the constants (`identityCapabilities` via a new `tierNames`
  helper); this incidentally dissolved the cluster, since the shared
  string literals it was clustering on no longer exist in
  `identityCapabilities`. Separately, `Bundler.fetchAndStrip` had no
  response-size cap while `CardFetcher.Fetch` did (from an earlier review
  pass) — since `Bundle()` holds every fetched page in memory for the
  whole cache TTL, an unbounded origin response would sit there for as
  long as the cache does. Fixed with the same `io.LimitReader` idiom,
  capped at `maxPageResponseBytes` (10MB — origin pages are trusted
  operator config, not attacker input, so this is a sanity bound rather
  than the tight 64KB card limit), with its own boundary tests.
- Pillar 3 is real: `identity.SignedVerifier` grants `TierVerified` to a
  request whose Ed25519 signature checks out against the key published
  at the URL the request itself names (`Signature-Agent` /
  `Signature-Input` / `Signature`), no trust registry required for the
  crypto check itself — only the reputation layer on top is still open.
  Rate-limit bucketing keys on the verified `AgentID`; every other tier
  keys on remote IP (not the full `RemoteAddr`, which includes an
  ephemeral port that changes per connection — a real bug found via a
  live smoke test, since the port defaults to a fixed value in
  `httptest.NewRequest` and so never showed up in the unit tests).
  Discovery document now advertises the signing headers and only claims
  the tiers the running server's `Verifier` can actually grant.
  `CardFetcher` dials through an SSRF-safe transport: the card URL comes
  from an unauthenticated request header, so it refuses to fetch
  anything resolving to a loopback/private/link-local address — without
  it, a requester could point `Signature-Agent` at internal
  infrastructure (a cloud metadata endpoint, an admin port) and use the
  proxy as an SSRF relay. This also means a card served from `localhost`
  during local development will be refused by design; use a real
  reachable URL (or a tunnel) when testing pillar 3 by hand.
- Two follow-up hardening fixes from a closer review of the above:
  `CardFetcher.Fetch` now requires `https` on the card URL — over plain
  `http`, anyone in a network position between this server and the card
  URL could substitute their own key and forge `TierVerified` for any
  agent ID they like, since TLS is what actually backs the "this key
  belongs to whoever's signing" claim, not the signature check alone.
  And the card cache is now a bounded, LRU-evicting `cardCache`
  (`DefaultMaxCardCacheEntries`, 10000) instead of an unbounded map: the
  cache key comes straight from the same unauthenticated
  `Signature-Agent` header, so without a bound, varying that header's
  query string on every request would grow the cache (and trigger a
  fresh outbound fetch) without limit — no new IP or connection needed,
  cheaper than the equivalent attack against the rate limiter's bucket
  map. Eviction is LRU rather than insertion-order so a card still in
  active use doesn't get pushed out just because it was cached early.
- Third pass, same review thread: `CardFetcher.Fetch` now caps a card
  response at 64KB (`maxCardResponseBytes`) before decoding — the card
  URL is the requester's own choice, so the server answering it is
  effectively attacker-controlled, and the client's 5-second timeout
  bounds fetch *time*, not *bytes*. Rejection is an explicit
  length check with its own error, not an implicit JSON-parse failure
  from a truncated body, so it fails for the right reason and is
  testable as such. Also added a concurrent-access test for `cardCache`
  (`TestCardCacheConcurrentAccess`) — every prior test drove it
  sequentially, so `go test -race` passing on those never actually gave
  the detector real concurrent traffic against one cache instance to
  check.
- Initial prototype: reverse proxy implementing content negotiation
  (`Accept: text/markdown`) and bulk export as real, and identity
  verification / graduated rate limits with pillar 3 stubbed behind the
  `identity.Verifier` interface pending a real WebBotAuth-style registry.
