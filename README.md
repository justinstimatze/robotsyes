# robots.yes

**A self-hostable reverse proxy that answers content negotiation, bulk
export, bot identity verification, and graduated rate limits for any
existing site, with no changes to the origin.**

It sits in front of an HTTP origin and adds four things a crawler or agent
can use, all published at `/.well-known/robots-yes.json` so a client can
discover what a given deployment actually supports instead of assuming a
fixed feature set.

## Quickstart

```sh
go install github.com/justinstimatze/robotsyes/cmd/robotsyes@latest

cp robotsyes.example.yaml robotsyes.yaml
# edit robotsyes.yaml: set origin to the real site being proxied
```

The default install builds content negotiation and bulk export only — it
carries no BitTorrent or x402/payments dependency. Adding either
`export.torrent.enabled` or `payments.enabled` to a config against this
build fails fast at startup with the fix. To get both, build with tags:

```sh
go install -tags payments,torrent github.com/justinstimatze/robotsyes/cmd/robotsyes@latest
```

```yaml
origin: http://localhost:3000
addr: ":8080"
export:
  paths: ["/", "/about"]
rate_limits:
  unverified: 10
  declared: 60
  verified: 300
```

```sh
robotsyes serve -config robotsyes.yaml
```

```sh
curl -H 'Accept: text/markdown' http://localhost:8080/about
```

`robotsyes serve` with no `-config` runs against `config.Default()` — no
origin is set, so it's a smoke test, not a deployment.

## Status

- **Content negotiation**: done. `Accept: text/markdown` on any proxied URL
  returns a stripped page instead of full HTML, via `Vary: Accept`. No
  config required beyond `origin`.
- **Bulk export**: done. `export.ndjson` bundles every configured page;
  `export-manifest.json` adds a per-page hash plus one bundle-level hash so
  a repeat crawl can check "did anything change" in one request.
  `export.torrent` (BEP-19 web seed) is opt-in and requires the `torrent`
  build tag — see `export.torrent` in `robotsyes.example.yaml`.
- **Identity verification**: done, wire-compatible with IETF Web Bot Auth
  (`draft-meunier-web-bot-auth-architecture`), the scheme Cloudflare,
  Anthropic, and OpenAI already run in production. Verified against the
  draft's own RFC 9421 Appendix A.2 worked examples. Handles the signature
  check only — there's no trust/reputation registry, and none is required
  for a request to earn the verified tier.
- **Graduated rate limits**: done. Ceilings are set per identity tier in
  config and published in the discovery document. The optional x402
  paid-overflow tier (`payments.enabled`) requires the `payments` build
  tag and is scoped to what's actually proven live: EVM "exact" scheme
  only, one flat price, Base mainnet and USDC by default, no local ledger
  or bulk-credit purchase. Settlement is delegated to
  [`justinstimatze/chit`](https://github.com/justinstimatze/chit).

See [CHANGELOG.md](CHANGELOG.md) for what shipped and why.

## Well-known routes

| Route | Purpose |
| --- | --- |
| `/.well-known/robots-yes.json` | Discovery document: capabilities, rate-limit tiers, identity/payments config |
| `/.well-known/robots-yes/export.ndjson` | Bulk export bundle |
| `/.well-known/robots-yes/export-manifest.json` | Per-page hashes + one bundle-level hash, for cheap change detection |
| `/.well-known/robots-yes/export.torrent` | BEP-19 web-seeded torrent (opt-in) |
| `/.well-known/robots-yes/metrics` | Prometheus text-format metrics |
| `/.well-known/signature-agent-card` | This server's own Signature Agent Card (Web Bot Auth) |
| `/llms.txt` | Compatibility bridge pointing at the discovery document |

## Configuration

See [`robotsyes.example.yaml`](robotsyes.example.yaml) for every field,
commented. Required: `origin`, and either `export.paths` or
`export.sitemap_url`. Everything else — torrent export, x402 payments — is
opt-in and off by default; a config that sets neither produces the same
behavior as no config at all.

## Testing

```sh
make check       # vet, fmt, lint, test — the default build
make check-full  # same, with -tags payments,torrent
```

Run both before a release: `check` and `check-full` cover different code
— `chitgate`'s proxy integration and `torrent.go` only compile under the
`payments`/`torrent` tags, so the default build's test run never touches
them. No live-network test suite either way — the x402 payment path is
exercised with a fake `payments.Merchant` in unit tests, not a funded
wallet.

## Security

See [SECURITY.md](SECURITY.md) for how to report a vulnerability.

## License

MIT — see [LICENSE](LICENSE).
