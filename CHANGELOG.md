# Changelog

## Unreleased

### Added

- **Pillar 2: opt-in `.torrent` export.** `/.well-known/robots-yes/export.torrent`
  publishes a v1 (BEP 3) multi-file torrent whose pieces are the same bundled
  pages the manifest hashes, BEP-19 web-seeded back to a new
  `/.well-known/robots-yes/torrent-seed/` route on this same server — so a
  swarm can form for long-lived, long-tail content without robots.yes ever
  running a tracker, DHT node, or peer client. Off by default
  (`export.torrent.enabled`); requires `export.torrent.public_url`, since
  neither the configured origin nor the bind address is a public URL. A
  startup warning (not a hard block) fires when enabled with a bundle TTL
  under an hour, since a bundle that regenerates its infohash faster than a
  swarm can form gets none of the benefit over plain HTTP. Adds one new
  dependency, `github.com/anacrolix/torrent` (`metainfo`/`bencode`
  subpackages only, not the full BitTorrent client/peer engine).

- **Pillar 2: export manifest.** `/.well-known/robots-yes/export-manifest.json`
  lists every bundled page's path, a `sha256:` content hash, and byte size,
  plus one `bundle_hash` over the whole sorted set — a returning crawler can
  check that single field to see whether anything changed at all, instead of
  re-downloading and diffing the whole bundle. Generated whenever a `Bundler`
  exists; no config toggle.

- **Pillar 4: optional x402 paid-overflow tier.** Past a rate-limit tier's
  published ceiling, an operator can let a requester pay per over-ceiling
  request via [x402](https://x402.org) (HTTP 402 + EIP-3009
  `transferWithAuthorization` for the EVM "exact" scheme) instead of always
  returning a flat 429. `internal/paymentgate/chitgate` adapts
  [`github.com/justinstimatze/chit/server`](https://github.com/justinstimatze/chit),
  a production x402 merchant library settled on Base mainnet, onto a
  `internal/payments.Merchant` interface kept free of `chit`'s own types, so
  the gating logic in `proxy.handleRateLimited` is unit-testable with a fake
  merchant and no network calls. Off by default (`payments.enabled`); a bare
  `robotsyes serve` with no config file is byte-for-byte unchanged. Scoped to
  what's actually proven live: EVM "exact" only, one flat price, one chain
  (Base mainnet default), one asset (USDC default) — no local ledger, no
  bulk-credit purchase.

- **`/.well-known/robots-yes/metrics`** — request counts by identity tier,
  rate-limit denials by tier, and export-bundle build/page counts, in
  Prometheus text format via a dependency-free `internal/metrics` package.
  Excluded from its own counters and from rate limiting.

- **Sitemap-driven export discovery.** `export.sitemap_url` (+
  `max_sitemap_pages`, default 1000) fetches a sitemap on every rebuild and
  bundles what it finds, following one level of `sitemapindex`. A `<loc>`
  pointing at a different host than `origin` is skipped. Sitemap-discovered
  paths fail individually rather than aborting the whole bundle; the
  original hand-listed `export.paths` still fails the whole bundle on any
  error, since a broken entry there is operator signal, not noise.

- **`/llms.txt`** as a compatibility bridge, pointing crawlers that already
  check that path by convention at the real discovery document.

### Changed

- **Pillar 3 is now wire-compatible with IETF Web Bot Auth**
  (`draft-meunier-web-bot-auth-architecture`,
  `draft-meunier-webbotauth-registry`), the scheme Cloudflare, Anthropic, and
  OpenAI already run in production, replacing an earlier bespoke wire
  format. `identity.Card` is now a JWKS-shaped Signature Agent Card
  (`client_name` + `keys` as JWK `OKP`/`Ed25519` objects); `Signature-Input`
  is parsed as an RFC 9421 signature-params dictionary entry. Signatures are
  now scoped to `@authority` (+ optionally `signature-agent`) rather than
  `@method`/`@path` — a signature is valid for any request to the same host
  until it expires, with a bounded, TTL-expiring, LRU-evicting nonce cache as
  the sole per-request replay defense, plus a `MaxValidity` bound. This is
  the trade for real interop with existing signers rather than a narrower,
  self-invented convention. Verified against the IETF draft's own Appendix
  A.2 worked examples (RFC 9421 Appendix B.1.4's published Ed25519 test key).
  The discovery document now publishes the card-discovery convention, the
  required signature parameters, and a reference to the spec being spoken.

- **Content negotiation now defaults toward markdown for a self-identified
  agent**, not only an explicit `Accept: text/markdown`. An explicit Accept
  preference — for HTML or anything else — always wins; the default only
  fills the gap where nothing was actually asked for and the identity tier
  is declared or verified.

- **Bulk export gzips its ndjson response** when `Accept-Encoding` allows it
  (respecting an explicit `q=0`).

- `export.NewBundler` takes a `BundlerConfig` struct instead of positional
  arguments.

### Fixed / Security

- **x402 credential validation.** A garbage `Payment-Signature` header would
  previously still force a real outbound settle call on every rate-limited
  request. Credentials are now checked for structural validity before any
  network call, cutting a garbage-credential round trip from a real
  settlement-endpoint call down to local validation only.
- **x402 replay/double-spend window.** `internal/paymentgate/chitgate/replaycache.go`
  adds a bounded, TTL-expiring, LRU-evicting reserve/commit/release cache on
  robots.yes's own side, closing a time-of-check-to-time-of-use gap where two
  concurrent requests could present the same payment credential.
- **HTTP server timeouts.** The server previously ran on a bare
  `http.ListenAndServe` with no read/write/idle timeouts — a real gap made
  materially worse once a slow, request-blocking outbound settlement call
  became reachable by any rate-limited client. Now sets real
  `ReadHeaderTimeout`/`ReadTimeout`/`WriteTimeout`/`IdleTimeout`, plus a
  tighter, request-scoped timeout around the payment-settlement call
  specifically.
- **Payout address validation.** `chitgate.New` now validates
  `PayoutAddress` as a well-formed `0x`-plus-40-hex-char address at
  construction, instead of only checking it's non-empty.
- **Identity: SSRF-safe card fetch.** `CardFetcher` refuses to fetch a
  Signature Agent Card from an address resolving to loopback/private/
  link-local — without it, a requester could point `Signature-Agent` at
  internal infrastructure and use the proxy as an SSRF relay. HTTPS is now
  required on the card URL, since TLS is what actually backs the "this key
  belongs to whoever's signing" claim.
- **Identity: bounded card cache.** The card cache is a bounded,
  LRU-evicting cache (10,000 entries default) instead of an unbounded map —
  the cache key comes from an unauthenticated request header, so an
  unbounded cache was a cheap memory-growth vector. Card responses are
  capped at 64KB before decoding.
- **Rate-limit bucketing** now keys on remote IP rather than the full
  `RemoteAddr` (which includes an ephemeral port that changes per
  connection) for every tier except the cryptographically verified one,
  which keys on the verified agent ID.
- **Bundler response size cap.** `Bundler.fetchAndStrip` now caps a fetched
  origin page at 10MB before buffering it, matching the existing cap on
  card fetches — `Bundle()` holds every fetched page in memory for the
  whole cache TTL, so an unbounded origin response would sit there for as
  long as the cache does.

### Internal

- Extracted `internal/httpx.GetBounded`, the shared "GET a URL, cap the
  response size" implementation used by both the export bundler and the
  identity card fetcher.
- `identity.SignedVerifier.Verify` split into focused helpers
  (`verifySignature` / `verifyAgainstCard` / `withinSkew`) for readability.

## Initial prototype

- Reverse proxy implementing content negotiation (`Accept: text/markdown`)
  and bulk export; identity verification and graduated rate limits behind
  the `identity.Verifier` interface, pending a real WebBotAuth-style
  registry (delivered in Unreleased above).
