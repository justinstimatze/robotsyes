# Changelog

## Unreleased

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
