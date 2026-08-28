# Changelog

## Unreleased

- Pillar 4 gained an optional paid-overflow tier: past a rate-limit
  tier's published ceiling, an operator can let a requester pay per
  over-ceiling request via **x402** (HTTP 402 + EIP-3009
  `transferWithAuthorization` for the EVM "exact" scheme) instead of
  always returning a flat 429 — the same protocol Cloudflare's
  Monetization Gateway now runs (see HANDOFF.md's Confidence notes),
  and the literal completion of pillar 4's own framing: "the stronger
  the identification, the higher the [access]," now extended past a
  free ceiling to a paid one instead of stopping there.

  This is a consumer integration, not a from-scratch payments build:
  `internal/paymentgate/chitgate` adapts
  [`github.com/justinstimatze/chit/server`](https://github.com/justinstimatze/chit)
  — a production-validated, live-settled-on-Base-mainnet Go x402
  merchant library — onto a new `internal/payments.Merchant` interface
  kept free of `chit`'s own types, so the gating logic in
  `proxy.handleRateLimited` (challenge on no credential, settle before
  serve, fail closed on any error) is unit-testable with a fake
  merchant and no network calls. The two-call shape and its ordering
  rules — self-mint the first challenge rather than asking `chit` for
  it (avoids a documented self-charge false positive), settle
  on-chain via `CloseSession` before ever reporting success — are
  ported from a sibling project's own proven `chit` integration
  (`justinstimatze/gemot`'s `internal/chitgate`), not invented fresh.

  Off by default: `config.PaymentsConfig.Enabled` must be explicitly
  `true` in `robotsyes.yaml`, and a bare `robotsyes serve` with no
  config file is byte-for-byte unchanged. Scoped hard to what's
  actually been proven live anywhere in this ecosystem — EVM "exact"
  only, one flat price, one chain (Base mainnet default), one asset
  (USDC default), no local ledger or bulk-credit purchase, no
  automated CI test against a real wallet (mirrors `chit`'s own
  `serverlive` build-tag split: unit tests only, a documented manual
  smoke-test step for the real settle path). `go.sum` now carries
  `chit`'s dependency closure, but nothing from its `x402signer`
  client-signing subpackage (or its `go-ethereum` dependency) is
  reachable from robots.yes's own binary — confirmed via `go list
  -deps`, since robots.yes is the merchant here, never the payer.

  Verified live this session (no funded wallet needed for this half):
  exceeding a 1-request-per-minute tier locally returns a
  well-formed x402 `402` challenge matching the real wire shape, and
  the discovery document's new `payments` block correctly resolves
  and publishes the effective network/asset when left at their
  defaults.

  A same-session security review of the staged diff (traced into
  `chit`'s own `paymentsession.go`/`settlement.go`/`requirepayment.go`
  rather than assumed from the API surface) found four real gaps,
  fixed here:

  1. `chit.DetectProtocol` accepts any non-empty header value as a
     "detected" credential — no format check. A single garbage
     `Payment-Signature` header would otherwise still force a real
     outbound settle call to the authorization server on every
     rate-limited request. `chitgate.validateCredential` now rejects
     anything that isn't at least a structurally well-formed x402
     "exact" authorization *before* any network call, reusing chit's
     own exported `ExtractX402PayerAddress` so the check accepts
     exactly what chit itself would accept, plus a ported
     `isHexAddress` check chit's own function stops short of.
     Live-verified: a garbage credential now round-trips in ~7ms
     (previously would have run all the way to the real settlement
     endpoint); a structurally-valid-but-unsigned one still correctly
     reaches and is rejected by it, in ~260ms.
  2. `chit.CloseSession` never inspects `SettleResult.AlreadySettled`
     — only a transport error and an amount comparison. Whether
     `auth.atxp.ai` would report `AlreadySettled` for a *different*
     caller replaying an already-spent credential (vs. only the same
     caller's own retry) isn't something this codebase can verify —
     that's third-party behavior. `internal/paymentgate/chitgate
     /replaycache.go` closes the gap unconditionally on robots.yes's
     own side regardless: a bounded, TTL-expiring, LRU-evicting
     reserve/commit/release cache (mirroring pillar 3's `nonceCache`,
     but with a claim step before the slow settle call starts, since
     unlike a signature nonce this one has a real
     time-of-check-to-time-of-use window between two concurrent
     requests presenting the same credential) — reserve before
     attempting settlement, commit only once confirmed, release on
     any failure so a legitimate retry after a transient error isn't
     permanently locked out by robots.yes's own bookkeeping.
  3. `main.go` called bare `http.ListenAndServe`, with none of
     Go's read/write/idle timeouts set — a pre-existing gap this
     feature made materially worse, since it's the first thing in
     this codebase to add a slow, request-blocking outbound call
     reachable by any rate-limited client. Now: real
     `ReadHeaderTimeout`/`ReadTimeout`/`WriteTimeout`/`IdleTimeout` on
     the `http.Server`, plus a tighter, request-scoped
     `paymentRequestTimeout` (15s — deliberately shorter than chit's
     own 65s default client timeout and the server's 30s
     `WriteTimeout`) wrapped around the `Merchant.RequirePayment` call
     specifically, so a slow settlement produces this package's own
     clean 429 rather than an aborted connection.
  4. `chitgate.New` only checked `PayoutAddress` was non-empty, not
     that it looked like a real address. Now validated as `0x` + 40
     hex chars at construction — caught the exact off-by-two typo in
     this session's own test fixtures and smoke-test config when the
     check went in.

  20 new tests (11 in the new `replaycache_test.go`, 9 across
  `chitgate_test.go` and `proxy_test.go`), all passing under `-race`;
  `make check` clean.

- Pillar 3's signature scheme is now wire-compatible with IETF Web Bot
  Auth (`draft-meunier-web-bot-auth-architecture`, `draft-meunier-
  webbotauth-registry`), the scheme Cloudflare, Anthropic, and OpenAI
  already run in production — closing the hedge the previous
  implementation's own comment carried ("not a claim of wire
  compatibility"). Concretely: `identity.Card` is now a JWKS-shaped
  Signature Agent Card (`client_name` + `keys` as JWK `OKP`/`Ed25519`
  objects, `x` base64url-encoded per RFC 8037) instead of a bespoke
  `agent_id`/`public_key` shape; `Signature-Input` is parsed as an RFC
  9421 signature-params dictionary entry covering `@authority` and
  optionally `signature-agent`, with `created`/`expires`/`keyid`/`alg`/
  `nonce`/`tag` parameters (`alg="ed25519"` and `tag="web-bot-auth"`
  required exactly); `signatureBase` echoes the parsed value's raw
  component-list-and-params substring verbatim rather than reassembling
  it, since RFC 9421 requires that line to match what the signer actually
  sent byte-for-byte, order included. `Signature-Agent` header values are
  now unquoted per RFC 8941 (a real WBA signer always sends a quoted
  sf-string), leniently — a bare unquoted value still passes through
  unchanged rather than being rejected.

  Deliberate, user-confirmed trade: signatures are now scoped to
  `@authority` (+ optionally `signature-agent`), not `@method`/`@path` as
  before — a signature is valid for any request to the same host until it
  expires, with a new bounded, TTL-expiring, LRU-evicting nonce cache
  (`internal/identity/noncecache.go`, mirroring `cardcache.go`'s shape) as
  the sole per-request replay defense, plus a `MaxValidity` bound so a
  signer can't hand out an arbitrarily long-lived signature and force the
  nonce cache to remember it indefinitely. This is the trade for real
  interop with Cloudflare/Anthropic/OpenAI-style signers rather than this
  project's own narrower, tighter-scoped convention.

  Verified against the wire format itself, not just internal
  self-consistency: `TestSignatureConformsToWebBotAuthDraftExamples`
  checks `signatureBase`'s output against the IETF draft's own Appendix
  A.2 worked examples (RFC 9421 Appendix B.1.4's published Ed25519 test
  key) — this caught two real transcription errors while writing the
  test (a swapped param order, a swapped `expires` value) and one genuine
  erratum in the draft's own prose (Appendix A.2.2 pairs a nonce and
  Signature that don't verify against each other; Cloudflare's own
  maintained `web-bot-auth` conformance-suite JSON has a self-consistent
  equivalent, used instead). The proxy's discovery document
  (`identityCapabilities`) now publishes the card discovery convention
  (`/.well-known/signature-agent-card`), the required signature
  parameters, and a reference to the spec being spoken.

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
