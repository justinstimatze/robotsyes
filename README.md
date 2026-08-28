# robots.yes

A self-hostable reverse proxy that says yes to well-behaved bots: content
negotiation, bulk/structured export, verified identity, and a published rate
limit instead of a 403 you have to discover by tripping it.

`robots.txt` has been the web's "no" file since 1994. `llms.txt` is a
"maybe" that still points back at the same crawl. robots.yes sits in front
of your existing site and answers four things a crawler or agent actually
needs, without you changing your origin at all.

## The four pillars

1. **Content negotiation** — `Accept: text/markdown` on the same URL returns
   a stripped page (no nav/JS/CSS) instead of full HTML, via `Vary: Accept`
   so caches don't serve the wrong version to the wrong requester.
2. **Bulk/structured export** — `/.well-known/robots-yes/export.ndjson`
   bundles every configured page as one gzip-able download, so a crawler
   stops hammering your origin's long tail one request at a time.
   `export-manifest.json` lets a returning crawler check one hash to see if
   anything changed at all before re-fetching, and an optional
   BEP-19-compatible `export.torrent` lets a swarm form for long-lived,
   long-tail content — robots.yes only ever plays the web-seed's role, it
   never runs a tracker or peer client.
3. **Verified bot identity** — Ed25519-signed Signature Agent Cards, wire-
   compatible with the IETF Web Bot Auth draft already running in
   production at Cloudflare, Anthropic, and OpenAI. A request that signs
   correctly against the key published at its own claimed URL earns a
   higher-trust tier; no central registry required for the crypto check
   itself.
4. **Graduated, published rate limits** — a tier's ceiling is published in
   the discovery document up front, keyed by identity tier and (optionally)
   backed by an [x402](https://x402.org) paid-overflow option past the free
   ceiling instead of a flat 429.

Every pillar rides an existing, already-adopted wire format rather than
inventing a new spec: plain Markdown/`llms.txt` for negotiation, BEP 19 for
the torrent export, the IETF Web Bot Auth draft for identity, x402 for paid
overflow. See [HANDOFF.md](HANDOFF.md) for the design reasoning and prior
art behind each choice.

## Quickstart

```sh
go install github.com/justinstimatze/robotsyes/cmd/robotsyes@latest

cp robotsyes.example.yaml robotsyes.yaml
# edit robotsyes.yaml: point origin at your real site

robotsyes serve -config robotsyes.yaml
```

A bare `robotsyes serve` with no `-config` flag runs against
`config.Default()` — useful for a quick smoke test, not a real deployment
(no origin is configured).

Every capability the running server actually has — which tiers it can
grant, whether the torrent or payments extensions are on — is published at
`/.well-known/robots-yes.json`. A client should read that document rather
than assuming a fixed feature set.

## Configuration

See [`robotsyes.example.yaml`](robotsyes.example.yaml) for a complete,
commented example. The two required fields are `origin` (the real site
robots.yes proxies to) and `export.paths` (or `export.sitemap_url`, to
discover pages automatically instead of hand-listing them). Everything
else — the torrent export, x402 paid overflow — is opt-in and off by
default.

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

## Status

Reference implementation, single Go binary, MIT-licensed. Two required
runtime dependencies beyond the standard library and `gopkg.in/yaml.v3`:
`anacrolix/torrent` (torrent construction only, not its peer client) and
`justinstimatze/chit` (x402 settlement, only reachable when payments are
enabled). See [CHANGELOG.md](CHANGELOG.md) for what's shipped.

## Security

See [SECURITY.md](SECURITY.md) for how to report a vulnerability.

## License

MIT — see [LICENSE](LICENSE).
