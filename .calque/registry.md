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
