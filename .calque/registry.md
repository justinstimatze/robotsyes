# calque registry

Durable record of adjudicated dual-path pairs for this repo. Lives at
`.calque/registry.md`. This is the memory that survives context resets: grep it
before assuming two paths are independent, and update it whenever a pair is
fixed, cleared, or newly found.

Each entry:

```
## <pair id>  — <verdict>
- left:  <file>::<qualname>
- right: <file>::<qualname>
- signal: <what calque fired on>
- verdict: drift | contracted-twin-ok | false-alarm
- policy: collapse-to-single-path | differential-test | none
- note: <one line — why, and the shared source if both delegate>
- reviewed: <date> by <who>
```

Verdicts:
- **drift** — same contract, behavior diverges. Fix = collapse to single-path.
- **contracted-twin-ok** — intentionally parallel, currently in sync. Pin with a
  differential test so it stays that way.
- **false-alarm** — coincidental signal overlap; suppress from future triage.

---

<!-- entries below -->

## 1 — false-alarm
- left:  internal/identity/identity.go::DeclaredVerifier.Verify
- right: internal/identity/signed.go::SignedVerifier.Verify
- pair: internal/identity/identity.go::DeclaredVerifier.Verify | internal/identity/signed.go::SignedVerifier.Verify
- signal: name~1.00(verify); shared-ret-keys=[AgentID Tier]
- verdict: false-alarm
- policy: none
- note: both implement the identity.Verifier interface — the shared name and
  return shape is Go's interface contract, not accidental duplication. Their
  behavior is meant to differ (declared is unsigned self-attestation, signed
  is real crypto verification).
- reviewed: 2026-08-28

## 2 — contracted-twin-ok
- left:  internal/identity/signed.go::NewCardFetcher
- right: internal/identity/signed_test.go::newTestCardFetcher
- pair: internal/identity/signed.go::NewCardFetcher | internal/identity/signed_test.go::newTestCardFetcher
- signal: shared-calls=1; name~0.75(card+fetcher+new)
- verdict: contracted-twin-ok
- policy: differential-test
- note: newTestCardFetcher is a deliberate test double that skips the SSRF
  dialer and https requirement so tests can hit loopback. Already pinned:
  TestCardFetcherRefusesPrivateAddresses and TestCardFetcherRejectsPlainHTTP
  exercise the real constructor specifically to keep the two from silently
  drifting apart on the properties that matter.
- reviewed: 2026-08-28

## 3 — false-alarm
- left:  internal/proxy/proxy.go::New
- right: internal/ratelimit/ratelimit.go::New
- pair: internal/proxy/proxy.go::New | internal/ratelimit/ratelimit.go::New
- signal: name~1.00(new); shared-calls=1
- verdict: false-alarm
- policy: none
- note: package-qualified constructor idiom (pkg.New()); unrelated packages.
- reviewed: 2026-08-28

## 4 — false-alarm
- left:  internal/export/export.go::Bundler.ServeHTTP
- right: internal/proxy/proxy.go::Server.ServeHTTP
- pair: internal/export/export.go::Bundler.ServeHTTP | internal/proxy/proxy.go::Server.ServeHTTP
- signal: name~1.00(http+serve); shared-calls=2
- verdict: false-alarm
- policy: none
- note: http.Handler interface method name, mandated by stdlib.
- reviewed: 2026-08-28

## 5 — false-alarm
- left:  internal/identity/cardcache.go::newCardCache
- right: internal/identity/signed.go::NewCardFetcher
- pair: internal/identity/cardcache.go::newCardCache | internal/identity/signed.go::NewCardFetcher
- signal: name~0.50(card+new)
- verdict: false-alarm
- policy: none
- note: coincidental naming overlap; unrelated constructors.
- reviewed: 2026-08-28

## 6 — false-alarm
- left:  internal/export/export.go::NewBundler
- right: internal/proxy/proxy.go::New
- pair: internal/export/export.go::NewBundler | internal/proxy/proxy.go::New
- signal: name~0.50(new)
- verdict: false-alarm
- policy: none
- note: constructor-name coincidence only.
- reviewed: 2026-08-28

## 7 — false-alarm
- left:  internal/export/export.go::NewBundler
- right: internal/ratelimit/ratelimit.go::New
- pair: internal/export/export.go::NewBundler | internal/ratelimit/ratelimit.go::New
- signal: name~0.50(new)
- verdict: false-alarm
- policy: none
- note: constructor-name coincidence only.
- reviewed: 2026-08-28

## 8 — false-alarm
- left:  internal/identity/identity.go::DeclaredVerifier.Verify
- right: internal/identity/signed.go::SignedVerifier.verifySignature
- pair: internal/identity/identity.go::DeclaredVerifier.Verify | internal/identity/signed.go::SignedVerifier.verifySignature
- signal: name~0.50(verify); shared-calls=1
- verdict: false-alarm
- policy: none
- note: verifySignature is a private decomposition step of Verify (see #9),
  not a competing top-level implementation of the Verifier interface.
- reviewed: 2026-08-28

## 9 — false-alarm
- left:  internal/identity/signed.go::SignedVerifier.Verify
- right: internal/identity/signed.go::SignedVerifier.verifySignature
- pair: internal/identity/signed.go::SignedVerifier.Verify | internal/identity/signed.go::SignedVerifier.verifySignature
- signal: name~0.50(verify); shared-calls=1; same-receiver
- verdict: false-alarm
- policy: none
- note: verifySignature/verifyAgainstCard/withinSkew were extracted from
  Verify itself to bring its cyclomatic complexity under CodeScene's
  threshold — this is decomposition of one function into helpers, not two
  independent paths that could drift.
- reviewed: 2026-08-28

## 10 — false-alarm
- left:  internal/export/export.go::Bundler.ServeHTTP
- right: internal/proxy/proxy.go::Server.serveMarkdown
- pair: internal/export/export.go::Bundler.ServeHTTP | internal/proxy/proxy.go::Server.serveMarkdown
- signal: shared-strings=1; name~0.33(serve); shared-calls=3
- verdict: false-alarm
- policy: none
- note: both are HTTP handlers writing a response; weak coincidental overlap.
- reviewed: 2026-08-28

## 11 — false-alarm
- left:  internal/export/export.go::Bundler.ServeHTTP
- right: internal/proxy/proxy.go::Server.serveDiscovery
- pair: internal/export/export.go::Bundler.ServeHTTP | internal/proxy/proxy.go::Server.serveDiscovery
- signal: shared-calls=4; name~0.33(serve); shared-strings=1
- verdict: false-alarm
- policy: none
- note: same as #10 — coincidental handler-shape overlap.
- reviewed: 2026-08-28

## 12 — false-alarm
- left:  internal/identity/cardcache.go::newCardCache
- right: internal/identity/signed_test.go::newTestCardFetcher
- pair: internal/identity/cardcache.go::newCardCache | internal/identity/signed_test.go::newTestCardFetcher
- signal: name~0.40(card+new)
- verdict: false-alarm
- policy: none
- note: coincidental naming overlap only.
- reviewed: 2026-08-28

## 13 — false-alarm
- left:  internal/negotiate/negotiate.go::WantsMarkdown
- right: internal/negotiate/negotiate_test.go::TestWantsMarkdown
- pair: internal/negotiate/negotiate.go::WantsMarkdown | internal/negotiate/negotiate_test.go::TestWantsMarkdown
- signal: name~0.67(markdown+wants); shared-strings=1
- verdict: false-alarm
- policy: none
- note: a function and its own direct unit test — expected to share name and
  literals (e.g. "text/markdown"). This pairing fires for every
  straightforwardly-tested function; not a duplication signal.
- reviewed: 2026-08-28

## 14 — false-alarm (was drift, collapsed 2026-08-28)
- left:  internal/config/config.go::Default
- right: internal/proxy/proxy.go::Server.identityCapabilities
- pair: internal/config/config.go::Default | internal/proxy/proxy.go::Server.identityCapabilities
- signal: shared-strings=3
- verdict: false-alarm
- policy: none
- note: both hand-typed the tier names "unverified"/"declared"/"verified"
  as bare string literals, independent of the identity.Tier constants that
  actually define them in internal/identity/identity.go — a rename there
  wouldn't touch either copy. Fixed: config.Default() now keys its map with
  string(identity.TierX), and identityCapabilities() builds its Methods list
  through a new tierNames() helper over the same constants. Both are now
  compile-time-tied to the one definition instead of three independent
  copies of the same three strings.
- reviewed: 2026-08-28

## 15 — false-alarm
- left:  internal/proxy/proxy.go::Server.ServeHTTP
- right: internal/proxy/proxy.go::Server.serveDiscovery
- pair: internal/proxy/proxy.go::Server.ServeHTTP | internal/proxy/proxy.go::Server.serveDiscovery
- signal: name~0.33(serve); shared-strings=1; shared-calls=2; same-receiver
- verdict: false-alarm
- policy: none
- note: ServeHTTP dispatches to serveDiscovery as one of its routes —
  expected caller/callee structure on the same type.
- reviewed: 2026-08-28

## 16 — false-alarm
- left:  internal/proxy/proxy.go::Server.serveMarkdown
- right: internal/proxy/proxy.go::Server.serveDiscovery
- pair: internal/proxy/proxy.go::Server.serveMarkdown | internal/proxy/proxy.go::Server.serveDiscovery
- signal: name~0.33(serve); shared-strings=1; shared-calls=2; same-receiver
- verdict: false-alarm
- policy: none
- note: sibling route handlers on the same Server; naturally share a little
  plumbing (headers, encoding) without sharing a contract that could drift.
- reviewed: 2026-08-28

## 17 — contracted-twin-ok (was drift, fixed 2026-08-28)
- left:  internal/export/export.go::Bundler.fetchAndStrip
- right: internal/identity/signed.go::CardFetcher.Fetch
- pair: internal/export/export.go::Bundler.fetchAndStrip | internal/identity/signed.go::CardFetcher.Fetch
- signal: name~0.50(fetch); shared-calls=4
- verdict: contracted-twin-ok
- policy: differential-test
- note: two independent "fetch an HTTP response and decode it" paths that
  had actually diverged on hardening: CardFetcher.Fetch caps response size
  (maxCardResponseBytes, from an earlier review pass) but fetchAndStrip read
  the whole origin response unbounded — and Bundle() holds every fetched
  page in memory for the cache TTL, so a runaway response would sit there
  for as long as the cache does. Fixed: fetchAndStrip now caps at
  maxPageResponseBytes via the same io.LimitReader idiom, with its own
  boundary tests (TestBundleRejectsOversizedOriginResponse /
  TestBundleAcceptsResponseAtSizeLimit) mirroring CardFetcher's. Kept as two
  functions, not collapsed into one: they fetch under different trust
  models (fetchAndStrip's origin is operator-configured; CardFetcher's URL
  is unauthenticated attacker input and is additionally SSRF-blocked and
  https-only, which fetchAndStrip correctly is not).
- reviewed: 2026-08-28

## 18 — false-alarm
- left:  internal/proxy/proxy.go::Server.writeRateLimited
- right: internal/proxy/proxy.go::Server.serveDiscovery
- pair: internal/proxy/proxy.go::Server.writeRateLimited | internal/proxy/proxy.go::Server.serveDiscovery
- signal: shared-calls=4; shared-strings=2; same-receiver
- verdict: false-alarm
- policy: none
- note: coincidental overlap between two unrelated same-receiver methods.
- reviewed: 2026-08-28

## 19 — false-alarm
- left:  internal/identity/signed.go::Card.publicKey
- right: internal/proxy/proxy.go::Server.identityCapabilities
- pair: internal/identity/signed.go::Card.publicKey | internal/proxy/proxy.go::Server.identityCapabilities
- signal: shared-strings=1
- verdict: false-alarm
- policy: none
- note: both mention the literal "ed25519" — Card.publicKey checks a key's
  declared algorithm against it, identityCapabilities reports it as the
  discovery document's Algorithm field. Same constant, unrelated logic;
  surfaced only after the #14 fix touched identityCapabilities.
- reviewed: 2026-08-28

## C1 — false-alarm (production members fixed; see #14)
- members: internal/config/config.go::Default,
  internal/proxy/proxy.go::Server.identityCapabilities,
  internal/proxy/proxy_test.go::TestDiscoveryDocument,
  internal/proxy/proxy_test.go::TestRateLimitReturns429,
  internal/ratelimit/ratelimit_test.go::TestAllowKeysAreIndependent,
  internal/ratelimit/ratelimit_test.go::TestAllowRefillsOverTime,
  internal/ratelimit/ratelimit_test.go::TestAllowUnknownTierDenied,
  internal/ratelimit/ratelimit_test.go::TestAllowWithinCapacity
- cluster: internal/config/config.go::Default | internal/proxy/proxy.go::Server.identityCapabilities | internal/proxy/proxy_test.go::TestDiscoveryDocument | internal/proxy/proxy_test.go::TestRateLimitReturns429 | internal/ratelimit/ratelimit_test.go::TestAllowKeysAreIndependent | internal/ratelimit/ratelimit_test.go::TestAllowRefillsOverTime | internal/ratelimit/ratelimit_test.go::TestAllowUnknownTierDenied | internal/ratelimit/ratelimit_test.go::TestAllowWithinCapacity
- signal: shared private seam ("verified", "declared")
- verdict: false-alarm
- policy: none
- note: this cluster is what surfaced #14 — the two production members
  (config.Default, identityCapabilities) are now fixed to derive from
  identity.Tier constants rather than re-typing the strings. The remaining
  six members are tests: ratelimit_test.go's four use "declared" etc. as
  arbitrary fixture keys for a deliberately tier-agnostic, string-keyed
  limiter (internal/ratelimit doesn't and shouldn't import internal/identity
  — it has no concept of tiers, only bucket keys); proxy_test.go's two
  construct config.Config.RateLimits directly, which is still a plain
  map[string]int fed from YAML in production, so literal test keys there are
  the correct way to exercise it. None of the six need to change.
- reviewed: 2026-08-28

## 20 — false-alarm
- pair: internal/export/export.go::Bundler.fetchAndStrip | internal/negotiate/negotiate.go::Strip
- signal: name~0.50(strip)
- verdict: false-alarm
- policy: none
- note: fetchAndStrip calls Strip once; coincidental name-fragment match,
  not duplication.
- reviewed: 2026-08-28

## 21 — false-alarm
- pair: internal/proxy/proxy.go::Server.serveLLMsTxt | internal/proxy/proxy_test.go::TestServeLLMsTxt
- signal: name~0.75(llms+serve+txt); shared-strings=1; shared-calls=1
- verdict: false-alarm
- policy: none
- note: a function and its own direct test — same shape as #13.
- reviewed: 2026-08-28

## 22 — false-alarm
- pair: internal/export/export.go::Bundler.pathsFromSitemapIndex | internal/export/export_test.go::TestBundleDiscoversPathsFromSitemapIndex
- signal: name~0.57(from+index+paths+sitemap); shared-calls=1
- verdict: false-alarm
- policy: none
- note: a function and its own direct test — same shape as #13/#21.
- reviewed: 2026-08-28

## 23 — false-alarm
- pair: internal/export/export.go::Bundler.pathsFromSitemapIndex | internal/export/export.go::Bundler.pathsFromURLSet
- signal: name~0.40(from+paths); shared-calls=3; same-receiver
- verdict: false-alarm
- policy: none
- note: sibling decomposition steps of discoverSitemapPaths — same shape
  as #9 (Verify/verifySignature/verifyAgainstCard).
- reviewed: 2026-08-28

## 24 — false-alarm
- pair: internal/export/export.go::Bundler.discoverSitemapPaths | internal/export/export.go::Bundler.pathsFromSitemapIndex
- signal: name~0.40(paths+sitemap); shared-calls=3; same-receiver
- verdict: false-alarm
- policy: none
- note: dispatcher calling its own helper — same shape as #15/#16.
- reviewed: 2026-08-28

## 25 — false-alarm
- pair: internal/export/export.go::Bundler.ServeHTTP | internal/proxy/proxy.go::Server.serveLLMsTxt
- signal: shared-calls=2; name~0.25(serve); shared-strings=1
- verdict: false-alarm
- policy: none
- note: weak coincidental overlap between two unrelated handlers, same
  shape as #10/#11.
- reviewed: 2026-08-28

## 26 — false-alarm
- pair: internal/negotiate/negotiate.go::WantsMarkdown | internal/proxy/proxy.go::Server.wantsMarkdown
- signal: name~1.00(markdown+wants)
- verdict: false-alarm
- policy: none
- note: deliberate layering, not duplication — proxy.Server.wantsMarkdown
  is the tier-aware policy wrapper and calls negotiate.WantsMarkdown as
  its first check before falling back to the identity-tier default. Same
  concept at two layers (pure Accept-header logic vs. full policy
  including identity), same shape as #9 (Verify/verifySignature): a
  caller intentionally named after what it wraps.
- reviewed: 2026-08-28

## 27 — false-alarm
- pair: internal/negotiate/negotiate_test.go::TestWantsMarkdown | internal/proxy/proxy.go::Server.wantsMarkdown
- signal: name~0.67(markdown+wants); shared-calls=1
- verdict: false-alarm
- policy: none
- note: weak name-fragment overlap; the test doesn't exercise
  Server.wantsMarkdown at all.
- reviewed: 2026-08-28

## 28 — false-alarm
- pair: internal/negotiate/negotiate.go::ExpressesNoPreference | internal/negotiate/negotiate_test.go::TestExpressesNoPreference
- signal: name~0.75(expresses+no+preference)
- verdict: false-alarm
- policy: none
- note: a function and its own direct test — same shape as #13/#21/#22.
- reviewed: 2026-08-28

## 29 — false-alarm
- pair: internal/export/export.go::Bundler.ServeHTTP | internal/export/export_test.go::TestServeHTTPGzipsWhenAccepted
- signal: shared-strings=3; name~0.17(serve); shared-calls=4
- verdict: false-alarm
- policy: none
- note: a function and its own direct test — same shape as #13/#21/#22/#28.
- reviewed: 2026-08-28

## 30 — false-alarm
- pair: internal/export/export.go::acceptsGzip | internal/export/export_test.go::TestAcceptsGzipRespectsQZero
- signal: name~0.40(accepts+gzip); shared-strings=1
- verdict: false-alarm
- policy: none
- note: a function and its own direct test — same shape as #13/#21/#22/#28/#29.
- reviewed: 2026-08-28

## 31 — false-alarm
- pair: internal/proxy/metrics.go::Server.serveMetrics | internal/proxy/proxy.go::Server.serveLLMsTxt
- signal: shared-calls=3; name~0.25(serve); shared-strings=1; same-receiver
- verdict: false-alarm
- policy: none
- note: sibling handlers on the same Server, same shape as #10/#11/#25.
- reviewed: 2026-08-28

## 32 — false-alarm
- cluster: internal/export/export.go::Bundler.ServeHTTP | internal/export/export.go::acceptsGzip | internal/export/export_test.go::TestAcceptsGzipRespectsQZero | internal/export/export_test.go::TestServeHTTPGzipsWhenAccepted
- signal: shared private seam (acceptsGzip, gzip)
- verdict: false-alarm
- policy: none
- note: the gzip feature and its own two tests all legitimately reference
  the same private helper and the same stdlib package — that's what a
  correctly-tested single feature looks like, not a duplication signal.
- reviewed: 2026-08-28

<!-- pillar-4 x402 paid overflow (RequirePayment 981699e) + its code-health
     follow-up split RequirePayment/isHexAddress/decodeCredentialJSON/
     cmdServe and added replaycache.go, surfacing 36 new pairs + 3 new
     clusters. Adjudicated in one pass below, grouped by shared cause
     rather than one full write-up each — most are the same few shapes
     repeated across the three now-mirrored caches and the newly-split
     helper functions. -->

## 33 — false-alarm
- pair: internal/identity/cardcache.go::cardCache.len | internal/paymentgate/chitgate/replaycache.go::replayCache.len
- verdict: false-alarm
- policy: none
- note: see #37 — one write-up covers the whole len/removeElement/constructor
  family across cardCache, nonceCache, and replayCache.
- reviewed: 2026-08-28

## 34 — false-alarm
- pair: internal/identity/cardcache.go::cardCache.len | internal/identity/noncecache.go::nonceCache.len
- verdict: false-alarm
- policy: none
- note: see #37.
- reviewed: 2026-08-28

## 35 — false-alarm
- pair: internal/identity/cardcache.go::cardCache.removeElement | internal/paymentgate/chitgate/replaycache.go::replayCache.removeElement
- verdict: false-alarm
- policy: none
- note: see #37.
- reviewed: 2026-08-28

## 36 — false-alarm
- pair: internal/identity/cardcache.go::cardCache.removeElement | internal/identity/noncecache.go::nonceCache.removeElement
- verdict: false-alarm
- policy: none
- note: see #37.
- reviewed: 2026-08-28

## 37 — false-alarm
- pair: internal/identity/noncecache.go::nonceCache.len | internal/paymentgate/chitgate/replaycache.go::replayCache.len
- signal: name~1.00(len); shared-calls=3 (same across #33-#36, #38-#41)
- verdict: false-alarm
- policy: none
- note: cardCache, nonceCache, and now replayCache all use the identical
  container/list + map[string]*list.Element + sync.Mutex bounded-LRU
  idiom — deliberately, so a reader who's seen one recognizes the other
  two, not because they share a contract. Nothing calls them
  polymorphically and nothing depends on their internals staying in sync:
  if nonceCache's eviction changed, cardCache and replayCache would be
  unaffected. Verdict is false-alarm rather than contracted-twin-ok
  because there's no shared external contract to pin with a differential
  test — only a shared internal idiom. Covers #33-#41 (len x3 pairs,
  removeElement x3 pairs, the three new*Cache constructor pairs).
- reviewed: 2026-08-28

## 38 — false-alarm
- pair: internal/identity/noncecache.go::nonceCache.removeElement | internal/paymentgate/chitgate/replaycache.go::replayCache.removeElement
- verdict: false-alarm
- policy: none
- note: see #37.
- reviewed: 2026-08-28

## 39 — false-alarm
- pair: internal/identity/noncecache.go::newNonceCache | internal/paymentgate/chitgate/replaycache.go::newReplayCache
- verdict: false-alarm
- policy: none
- note: see #37.
- reviewed: 2026-08-28

## 40 — false-alarm
- pair: internal/identity/cardcache.go::newCardCache | internal/paymentgate/chitgate/replaycache.go::newReplayCache
- verdict: false-alarm
- policy: none
- note: see #37.
- reviewed: 2026-08-28

## 41 — false-alarm
- pair: internal/identity/cardcache.go::newCardCache | internal/identity/noncecache.go::newNonceCache
- verdict: false-alarm
- policy: none
- note: see #37.
- reviewed: 2026-08-28

## 42 — false-alarm
- pair: internal/paymentgate/chitgate/replaycache.go::replayCache.reserve | internal/paymentgate/chitgate/replaycache.go::replayCache.commit
- signal: shared-writes=[items[]]; shared-calls=6; same-receiver
- verdict: false-alarm
- policy: none
- note: two states of one reserve/commit/release lifecycle on the same
  cache, not two competing implementations — same decomposition shape as
  #9. See #47 for why this lifecycle doesn't collapse into cardCache.put
  or nonceCache.seen's single check-and-set despite the surface overlap.
- reviewed: 2026-08-28

## 43 — false-alarm
- pair: internal/identity/noncecache.go::nonceCache.seen | internal/paymentgate/chitgate/replaycache.go::replayCache.reserve
- verdict: false-alarm
- policy: none
- note: see #47.
- reviewed: 2026-08-28

## 44 — false-alarm
- pair: internal/identity/noncecache.go::nonceCache.seen | internal/paymentgate/chitgate/replaycache.go::replayCache.commit
- verdict: false-alarm
- policy: none
- note: see #47.
- reviewed: 2026-08-28

## 45 — false-alarm
- pair: internal/identity/cardcache.go::cardCache.put | internal/identity/noncecache.go::nonceCache.seen
- verdict: false-alarm
- policy: none
- note: see #47.
- reviewed: 2026-08-28

## 46 — false-alarm
- pair: internal/identity/cardcache.go::cardCache.put | internal/paymentgate/chitgate/replaycache.go::replayCache.reserve
- verdict: false-alarm
- policy: none
- note: see #47.
- reviewed: 2026-08-28

## 47 — false-alarm
- pair: internal/identity/cardcache.go::cardCache.put | internal/paymentgate/chitgate/replaycache.go::replayCache.commit
- signal: shared-writes=[items[]]; shared-calls=5-8 (varies across #42-#47)
- verdict: false-alarm
- policy: none
- note: all three caches' write path shares the same LRU insert/evict
  bookkeeping (see #37) but the operations themselves genuinely differ:
  cardCache.put is an unconditional upsert (insert-or-refresh, never
  rejects); nonceCache.seen is a single atomic check-and-set (insert if
  absent, report replay if present) — correct because WebBotAuth
  signature verification is synchronous, so a plain check-and-set has no
  race window; replayCache splits that same check into reserve (claim) /
  commit (finalize) / release (undo-on-failure) specifically because
  chit settlement is a slow, fallible network call — a naive
  check-and-set there would have a real time-of-check-to-time-of-use
  race between two concurrent requests presenting the same credential
  (see the chitgate package doc and replaycache.go). The three-way split
  is deliberate design, not accidental drift between copies of the same
  function. Covers #42-#47.
- reviewed: 2026-08-28

## 48 — false-alarm
- pair: internal/paymentgate/chitgate/chitgate.go::New | internal/proxy/proxy.go::New
- signal: name~1.00(new); shared-calls=1
- verdict: false-alarm
- policy: none
- note: package-qualified constructor idiom (pkg.New()) — same as #3.
- reviewed: 2026-08-28

## 49 — false-alarm
- pair: internal/paymentgate/chitgate/chitgate.go::New | internal/ratelimit/ratelimit.go::New
- verdict: false-alarm
- policy: none
- note: same as #48/#3.
- reviewed: 2026-08-28

## 50 — false-alarm
- pair: cmd/robotsyes/main.go::loadConfig | cmd/robotsyes/main.go::buildMerchant
- verdict: false-alarm
- policy: none
- note: sibling decomposition steps of cmdServe, split out to bring its
  cyclomatic complexity under CodeScene's threshold (was 9) — same
  decomposition shape as #9/#23/#24. Covers #50-#54 (loadConfig/
  buildMerchant, cmdServe/serve, loadConfig/serve, buildMerchant/serve,
  loadConfig/config.Load).
- reviewed: 2026-08-28

## 51 — false-alarm
- pair: cmd/robotsyes/main.go::cmdServe | cmd/robotsyes/main.go::serve
- verdict: false-alarm
- policy: none
- note: see #50 — dispatcher calling its own helper, same shape as #15/#16.
- reviewed: 2026-08-28

## 52 — false-alarm
- pair: cmd/robotsyes/main.go::loadConfig | cmd/robotsyes/main.go::serve
- verdict: false-alarm
- policy: none
- note: see #50.
- reviewed: 2026-08-28

## 53 — false-alarm
- pair: cmd/robotsyes/main.go::buildMerchant | cmd/robotsyes/main.go::serve
- verdict: false-alarm
- policy: none
- note: see #50.
- reviewed: 2026-08-28

## 54 — false-alarm
- pair: cmd/robotsyes/main.go::loadConfig | internal/config/config.go::Load
- signal: name~0.50(load); shared-calls=1
- verdict: false-alarm
- policy: none
- note: loadConfig is a thin wrapper adding the empty-path→Default()
  branch around config.Load — caller/callee across packages, same shape
  as #50, not duplication.
- reviewed: 2026-08-28

## 55 — false-alarm
- pair: internal/paymentgate/chitgate/chitgate.go::isHexAddress | internal/paymentgate/chitgate/chitgate.go::isHexDigit
- signal: name~0.50(hex+is)
- verdict: false-alarm
- policy: none
- note: isHexDigit was extracted from isHexAddress to clear a CodeScene
  Complex Conditional flag on the inline tri-range switch — same
  decomposition shape as #9.
- reviewed: 2026-08-28

## 56 — false-alarm
- pair: internal/proxy/proxy.go::paymentCredential | internal/proxy/proxy_test.go::TestPaymentCredentialPrefersPaymentSignatureHeader
- verdict: false-alarm
- policy: none
- note: a function and its own direct test — same shape as #13. Covers
  #56 and #59 (the Prefers/FallsBack precedence tests).
- reviewed: 2026-08-28

## 57 — false-alarm
- pair: internal/paymentgate/chitgate/chitgate.go::isHexAddress | internal/paymentgate/chitgate/chitgate_test.go::TestIsHexAddress
- verdict: false-alarm
- policy: none
- note: a function and its own direct test — same shape as #13.
- reviewed: 2026-08-28

## 58 — false-alarm
- pair: internal/identity/identity.go::unquoteSignatureAgent | internal/identity/signed_test.go::TestUnquoteSignatureAgent
- verdict: false-alarm
- policy: none
- note: a function and its own direct test — same shape as #13.
- reviewed: 2026-08-28

## 59 — false-alarm
- pair: internal/proxy/proxy.go::paymentCredential | internal/proxy/proxy_test.go::TestPaymentCredentialFallsBackToXPayment
- verdict: false-alarm
- policy: none
- note: see #56.
- reviewed: 2026-08-28

## 60 — false-alarm
- pair: internal/paymentgate/chitgate/chitgate.go::chitMerchant.RequirePayment | internal/proxy/proxy_test.go::fakeMerchant.RequirePayment
- signal: name~1.00(payment+require)
- verdict: false-alarm
- policy: none
- note: both implement the payments.Merchant interface (chitMerchant for
  real, fakeMerchant as the proxy package's test double) — the shared
  name and signature is the interface contract, not accidental
  duplication. Same shape as #1.
- reviewed: 2026-08-28

## 61 — false-alarm
- pair: internal/paymentgate/chitgate/chitgate.go::challengeFrom | internal/paymentgate/chitgate/chitgate_test.go::decodeRequirements
- verdict: false-alarm
- policy: none
- note: decodeRequirements is the test helper that unwraps the
  Challenge.Body a real caller would decode — a function and its own
  test tooling, same shape as #13.
- reviewed: 2026-08-28

## 62 — false-alarm
- pair: internal/proxy/proxy.go::Server.handleRateLimited | internal/proxy/proxy.go::Server.writeRateLimited
- signal: name~0.67(limited+rate); shared-calls=5; same-receiver
- verdict: false-alarm
- policy: none
- note: writeRateLimited is called directly by handleRateLimited (both
  the no-merchant-configured path and the settle-failure fallback) —
  caller/callee on the same type, same shape as #15/#16.
- reviewed: 2026-08-28

## 63 — false-alarm
- pair: internal/proxy/proxy.go::Server.handleRateLimited | internal/proxy/proxy.go::Server.serveDiscovery
- verdict: false-alarm
- policy: none
- note: sibling handlers on the same Server sharing a little plumbing —
  same shape as #10/#11/#18/#31.
- reviewed: 2026-08-28

## 64 — false-alarm
- pair: internal/proxy/proxy.go::Server.handleRateLimited | internal/proxy/proxy.go::Server.serveMarkdown
- verdict: false-alarm
- policy: none
- note: same as #63.
- reviewed: 2026-08-28

## 65 — false-alarm
- pair: internal/proxy/proxy.go::paymentCredential | internal/proxy/proxy.go::Server.paymentsCapabilities
- signal: shared-strings=1 ("Payment-Signature"/"X-Payment")
- verdict: false-alarm
- policy: none
- note: paymentCredential reads the header, paymentsCapabilities
  publishes the same header names in the discovery document — shared
  literal, unrelated logic, same shape as #19.
- reviewed: 2026-08-28

## 66 — false-alarm
- pair: internal/proxy/proxy.go::Server.handleRateLimited | internal/proxy/proxy_test.go::rateLimitedRequest
- verdict: false-alarm
- policy: none
- note: rateLimitedRequest is a test helper that drives a request past
  the rate limit to exercise handleRateLimited — test tooling, same shape
  as #13. Covers #66-#67.
- reviewed: 2026-08-28

## 67 — false-alarm
- pair: internal/proxy/proxy.go::Server.writeRateLimited | internal/proxy/proxy_test.go::rateLimitedRequest
- verdict: false-alarm
- policy: none
- note: see #66.
- reviewed: 2026-08-28

## 68 — false-alarm
- pair: internal/identity/signed.go::NewSignedVerifier | internal/identity/signed_test.go::newTestSignedVerifier
- signal: shared-calls=1; name~0.75(new+signed+verifier)
- verdict: false-alarm
- policy: none
- note: unlike #2 (newTestCardFetcher, a real test double with divergent
  behavior worth pinning), newTestSignedVerifier is presumed to be a
  thin constructor-name echo for building a SignedVerifier in test setup.
  No divergent behavior found worth a differential test; revisit if that
  changes.
- reviewed: 2026-08-28

## C2 — drift (one member); false-alarm (rest of cluster)
- cluster: internal/identity/signed.go::parseSignatureParams | internal/identity/signed_test.go::signRequest | internal/paymentgate/chitgate/chitgate.go::decodeX402Nonce | internal/paymentgate/chitgate/chitgate_test.go::testCredential | internal/proxy/proxy.go::Server.identityCapabilities
- signal: shared seam(s): created, keyid, expires, nonce
- verdict: false-alarm (cluster as a whole) / drift (one real pair inside it — see below)
- policy: none for the cluster; the drift below is flagged, not fixed, in
  this pass
- note: the chitgate/x402 members (decodeX402Nonce, testCredential) share
  nothing real with the WebBotAuth members — "nonce"/"created"/"expires"
  are generic replay-defense vocabulary both protocols happen to use, not
  a shared implementation (x402's fields live in JSON payload.
  authorization, WebBotAuth's in a Signature-Input parameter string).
  BUT: within that cluster, parseSignatureParams (internal/identity/
  signed.go:458) reads "created"/"expires"/"keyid"/"nonce"/"alg"/"tag" as
  hardcoded map keys, and Server.identityCapabilities (internal/proxy/
  proxy.go:370) independently hardcodes the same six names as
  RequiredSignatureParams — the exact "two independent copies of the
  same string set, no compile-time tie" shape that #14 turned out to be a
  real bug. Predates this session's payments work (pillar-3 code, not
  touched by the pillar-4 commit) and is lower-severity than #14 was:
  these are IETF-draft (draft-meunier-web-bot-auth-architecture) wire
  parameter names, not an internal enum the codebase itself might rename.
  Flagging rather than fixing — out of scope for this pass — but this is
  a real, if minor, drift risk and should not be silently marked
  false-alarm the way this note's sibling entries were.
- reviewed: 2026-08-28

## C3 — false-alarm
- cluster: internal/paymentgate/chitgate/chitgate.go::decodeX402Nonce | internal/paymentgate/chitgate/chitgate_test.go::TestValidateCredentialRejectsMissingNonce | internal/paymentgate/chitgate/chitgate_test.go::testCredential
- signal: shared seam(s): from, authorization, payload
- verdict: false-alarm
- policy: none
- note: decodeX402Nonce and its own two direct tests/fixtures — same
  shape as #13.
- reviewed: 2026-08-28

## C4 — false-alarm
- cluster: internal/proxy/proxy.go::Server.handleRateLimited | internal/proxy/proxy_test.go::TestPaymentCredentialFallsBackToXPayment | internal/proxy/proxy_test.go::TestPaymentCredentialPrefersPaymentSignatureHeader
- signal: shared seam(s): legacy, paymentCredential
- verdict: false-alarm
- policy: none
- note: same as #56/#59 — handleRateLimited is swept in only because it's
  paymentCredential's caller; the tests exercise paymentCredential's
  header-precedence logic directly.
- reviewed: 2026-08-28

<!-- pillar-2 export manifest (internal/export/manifest.go + Bundler.Manifest/
     ServeManifest, internal/proxy's manifestPath route). Same shapes as the
     pillar-4 batch above: sibling HTTP handlers, caller/callee decomposition,
     function-and-its-own-test. -->

## 69 — false-alarm
- pair: internal/export/export.go::Bundler.ServeManifest | internal/proxy/proxy.go::Server.handleRateLimited
- signal: shared-strings=2; shared-calls=4
- verdict: false-alarm
- policy: none
- note: coincidental "writes an HTTP response" overlap between unrelated
  handlers — same shape as #10/#11/#25/#31. Covers #69-#73 (ServeManifest
  vs. handleRateLimited/serveDiscovery/serveMarkdown/writeRateLimited/
  serveLLMsTxt).
- reviewed: 2026-08-28

## 70 — false-alarm
- pair: internal/export/export.go::Bundler.ServeManifest | internal/proxy/proxy.go::Server.serveDiscovery
- verdict: false-alarm
- policy: none
- note: see #69.
- reviewed: 2026-08-28

## 71 — false-alarm
- pair: internal/export/export.go::Bundler.ServeManifest | internal/proxy/proxy.go::Server.serveMarkdown
- verdict: false-alarm
- policy: none
- note: see #69.
- reviewed: 2026-08-28

## 72 — false-alarm
- pair: internal/export/export.go::Bundler.ServeManifest | internal/proxy/proxy.go::Server.writeRateLimited
- verdict: false-alarm
- policy: none
- note: see #69.
- reviewed: 2026-08-28

## 73 — false-alarm
- pair: internal/export/export.go::Bundler.ServeManifest | internal/proxy/proxy.go::Server.serveLLMsTxt
- verdict: false-alarm
- policy: none
- note: see #69.
- reviewed: 2026-08-28

## 74 — false-alarm
- pair: internal/export/export.go::Bundler.Manifest | internal/export/manifest.go::buildManifest
- signal: name~1.00(manifest)
- verdict: false-alarm
- policy: none
- note: caller/callee — Manifest() (via startBuildLocked) is buildManifest's
  only caller. Same decomposition shape as #9.
- reviewed: 2026-08-28

## 75 — false-alarm
- pair: internal/export/export.go::Bundler.ServeManifest | internal/export/export_test.go::TestServeManifestWritesJSON
- verdict: false-alarm
- policy: none
- note: a function and its own direct test — same shape as #13. Covers #75
  and #77 (the export_test.go and proxy_test.go tests).
- reviewed: 2026-08-28

## 76 — false-alarm
- pair: internal/export/export.go::Bundler.ServeManifest | internal/export/export.go::Bundler.ServeHTTP
- signal: shared-calls=5; name~0.33(serve); shared-strings=1; same-receiver
- verdict: false-alarm
- policy: none
- note: sibling handlers on the same Bundler — same shape as #10/#15/#16.
- reviewed: 2026-08-28

## 77 — false-alarm
- pair: internal/export/export.go::Bundler.ServeManifest | internal/proxy/proxy_test.go::TestExportManifestEndpointServesManifest
- verdict: false-alarm
- policy: none
- note: see #75 — the end-to-end route test exercises ServeManifest
  through Server.ServeHTTP's dispatch.
- reviewed: 2026-08-28

## 78 — false-alarm
- pair: internal/export/export_test.go::TestServeManifestWritesJSON | internal/proxy/proxy.go::Server.serveDiscovery
- verdict: false-alarm
- policy: none
- note: coincidental weak overlap, same shape as #10/#11.
- reviewed: 2026-08-28

## C5 — false-alarm
- cluster: internal/export/export.go::Bundler.startBuildLocked | internal/export/export_test.go::TestBuildManifestBundleHashChangesWithContent | internal/export/export_test.go::TestBuildManifestBundleHashStableAcrossBuiltAtAndTTL | internal/export/export_test.go::TestBuildManifestComputesHashAndBytes | internal/export/export_test.go::TestBuildManifestSortsEntriesByPath
- signal: shared seam(s): hello, buildManifest
- verdict: false-alarm
- policy: none
- note: startBuildLocked is buildManifest's only production caller, swept
  in only because it shares the token "buildManifest"; the four tests
  call buildManifest directly (unit-level, no Bundler) and happen to
  share the fixture literal "hello" as sample page content — same
  function-and-its-own-tests shape as #13, not duplication.
- reviewed: 2026-08-28

## 79 — false-alarm
- pair: internal/export/torrent.go::pathToSeedKey | internal/export/torrent_test.go::TestPathToSeedKey
- signal: name~0.75(key+path+seed); shared-strings=1
- verdict: false-alarm
- policy: none
- note: a function and its own direct test — same shape as #13.
- reviewed: 2026-08-28

## 80 — false-alarm
- pair: internal/export/export.go::Bundler.ServeManifest | internal/export/export.go::Bundler.ServeTorrent
- signal: shared-strings=1; name~0.33(serve); shared-calls=3; same-receiver
- verdict: false-alarm
- policy: none
- note: sibling handlers on the same Bundler — same shape as #10/#15/#16/#76.
- reviewed: 2026-08-28

## 81 — false-alarm
- pair: internal/export/export.go::Bundler.ServeTorrent | internal/proxy/proxy.go::Server.serveMarkdown
- signal: shared-strings=1; name~0.33(serve); shared-calls=4
- verdict: false-alarm
- policy: none
- note: coincidental weak overlap on "serve" plus both writing an HTTP
  response body — same shape as #10/#11/#78.
- reviewed: 2026-08-28

## 82 — false-alarm
- pair: internal/export/export.go::Bundler.ServeTorrent | internal/export/export.go::Bundler.ServeTorrentSeed
- signal: name~0.67(serve+torrent); shared-calls=3; same-receiver
- verdict: false-alarm
- policy: none
- note: sibling handlers on the same Bundler, same shape as #80 — one
  serves the .torrent itself, the other the BEP-19 web-seed content; not
  a dual-path pair, they serve genuinely different resources.
- reviewed: 2026-08-28

## 83 — false-alarm
- pair: internal/export/export.go::Bundler.ServeTorrent | internal/export/torrent_test.go::TestServeTorrentWritesParsableTorrent
- verdict: false-alarm
- policy: none
- note: a function and its own direct test — same shape as #13/#75/#79.
- reviewed: 2026-08-28

## 84 — false-alarm
- pair: internal/export/export.go::Bundler.ServeTorrent | internal/proxy/proxy.go::Server.serveDiscovery
- signal: name~0.33(serve); shared-strings=1; shared-calls=2
- verdict: false-alarm
- policy: none
- note: coincidental weak overlap, same shape as #10/#11/#78/#81.
- reviewed: 2026-08-28

## 85 — false-alarm
- pair: internal/export/export.go::Bundler.ServeTorrent | internal/export/export.go::Bundler.ServeHTTP
- signal: name~0.33(serve); shared-calls=4; shared-strings=1; same-receiver
- verdict: false-alarm
- policy: none
- note: sibling handlers on the same Bundler — same shape as #80/#82.
- reviewed: 2026-08-28

## 86 — contracted-twin-ok
- pair: cmd/robotsyes/payments_full.go::buildMerchant | cmd/robotsyes/payments_stub.go::buildMerchant
- signal: name~1.00(merchant); shared-calls=1
- verdict: contracted-twin-ok
- policy: none
- note: deliberate mutually-exclusive build-tag variants (payments /
  !payments) of the same func signature, same name, on purpose — see
  the build-tag split's own package docs in each file. They can never
  both be compiled into one binary, so there's no live-drift risk the
  usual "extract the shared bit" fix would guard against; if the stub's
  signature drifts from the full version's, the mismatch is a compile
  error the moment both are built with the same tag set, not silent
  runtime skew. Same shape applies to internal/export/torrent.go's
  buildTorrentInfo vs torrent_stub.go's — not filed separately since
  calque didn't surface that pair this pass, but the same verdict
  would apply if it does.
- reviewed: 2026-08-28

## 87 — false-alarm
- pair: cmd/robotsyes/main.go::loadConfig | cmd/robotsyes/payments_full.go::buildMerchant
- signal: shared-strings=1; shared-calls=1
- verdict: false-alarm
- policy: none
- note: coincidental weak overlap (0.35), same shape as #10/#11/#78/#81/#84.
- reviewed: 2026-08-28

## 88 — false-alarm
- pair: cmd/robotsyes/main.go::serve | cmd/robotsyes/payments_full.go::buildMerchant
- signal: shared-strings=1; shared-calls=1
- verdict: false-alarm
- policy: none
- note: coincidental weak overlap (0.18), same shape as #87.
- reviewed: 2026-08-28

## C6 — false-alarm
- cluster: internal/export/export.go::Bundler.buildTorrentLocked | internal/export/torrent_test.go::TestBuildTorrentInfoInfohashChangesWithContent | internal/export/torrent_test.go::TestBuildTorrentInfoInfohashStableAcrossCalls | internal/export/torrent_test.go::TestBuildTorrentInfoSkipsSeedKeyCollision | internal/export/torrent_test.go::TestBuildTorrentInfoWrapsTrackersAsOneTier | internal/export/torrent_test.go::parseTestTorrent
- signal: shared seam(s): infohashOf, buildTorrentInfo
- verdict: false-alarm
- policy: none
- note: buildTorrentLocked is buildTorrentInfo's only production caller,
  swept in only because it shares that token; the test-side members are
  parseTestTorrent (a shared test helper, see its own doc comment) and
  four tests that all call buildTorrentInfo/parseTestTorrent directly
  (unit-level, no Bundler) — same function-and-its-own-tests shape as
  C5, not duplication.
- reviewed: 2026-08-28
