# robots.yes

**A self-hostable reverse proxy that answers content negotiation, bulk
export, bot identity verification, and graduated rate limits for any
existing site, with no changes to the origin.**

The default visitor to a site is no longer just a human with a browser.
Agents are already reading, comparing, and acting on web content at
scale, and a site that only knows how to answer browsers either gets
scraped anyway or gets left out of what an agent decides to trust.
robots.yes lets a site opt into that traffic on its own terms: negotiate
content by what's asking, verify who's asking, and hand back exactly
what they came for — instead of pretending the only visitor that matters
is a browser.

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

Or with Docker — same two variants, published on every push to `main`:

```sh
docker run -p 8080:8080 \
  -v $(pwd)/robotsyes.yaml:/etc/robotsyes/robotsyes.yaml:ro \
  ghcr.io/justinstimatze/robotsyes:edge

# payments + torrent:
docker run -p 8080:8080 \
  -v $(pwd)/robotsyes.yaml:/etc/robotsyes/robotsyes.yaml:ro \
  ghcr.io/justinstimatze/robotsyes:edge-full
```

`:latest`/`:latest-full` track the most recent `vX.Y.Z` tag; `:edge`/`:edge-full`
track `main`. No config is baked into the image — the container fails fast
with a clear error if nothing's mounted at `/etc/robotsyes/robotsyes.yaml`,
rather than silently listening against an unreachable default origin.

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

## Why not just llms.txt?

[llms.txt](https://llmstxt.org) is a real, widely-adopted standard, and
robots.yes speaks it — `/llms.txt` redirects to the discovery document.
Platforms like Mintlify also generate its companion,
[`llms-full.txt`](https://www.mintlify.com/docs/ai/llmstxt): the whole
documentation site concatenated into one file, each page as its title,
source URL, and full markdown. Both are good answers to "what's on this
site and where" — just to a narrower question than the one robots.yes
answers:

- **Coverage is opt-in per page; negotiation is automatic per request.**
  llms.txt/llms-full.txt include whatever pages an author (or their doc
  generator) chose to add. `Accept: text/markdown` works on any proxied
  URL, no curation step required.
- **No verified requester identity.** The "authentication" llms.txt
  implementations have governs which pages get included in a static
  file at generation time — not who's asking for it right now. There's
  no signal to grant a higher rate limit or a paid overflow tier to.
- **No rate limiting.** Nothing in the convention addresses it — a
  static file has no concept of a requester to limit.
- **No change detection.** llms-full.txt is a monolith: checking "did
  anything change since I last crawled" means re-fetching and diffing
  the whole file. `export-manifest.json` adds a hash per page and one
  for the whole bundle, so that check is one small request instead of a
  full re-download.

This is also a well-worn pattern outside any single site: Wikipedia's
full-dump mirrors, Internet Archive's per-item torrents, and arXiv's bulk
S3 access all exist for the same underlying reason — scraping the live
site doesn't scale to crawler volume, so the operator builds a side
channel by hand. robots.yes turns that side channel into a standard
capability instead of a one-off.

llms.txt is the right tool for a hand-curated doc index. robots.yes is
the layer underneath it: per-request negotiation, verified identity, and
rate limits on every page a site has — not just the ones an author
remembered to list.

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
