# robots.yes — design stage, 2026-08-27

## One-liner

robots.txt has been the web's "no" file since 1994. llms.txt is a "maybe" that still
points back at the same crawl. Nobody has shipped the "yes" — one bundled, self-hostable
implementation that answers content negotiation, hands over bulk export as a real
cache-economics defense (not just a convenience), verifies who's asking, and hands out a
published rate limit instead of making a bot find the ceiling by tripping a 403.

## The four pillars

1. **Content negotiation** — `Accept: text/markdown` (or similar) on the same URL returns
   a stripped response (no nav/JS/CSS) instead of the full HTML page, via `Vary: Accept`
   so caches don't serve the wrong version to the wrong requester. Precedent: Cloudflare's
   "Markdown for Agents" (zone-level feature, 2026), Vercel's native Next.js support.
   Adoption on the *requesting* side is still partial — a Checkly survey (Feb 2026) found
   only 3 of 7 major coding agents (Claude Code, Cursor, OpenCode) actually send the
   markdown `Accept` header.

2. **Bulk/structured export as a formal cache-economics defense** — not "nice to have,"
   a specific answer to a specific failure mode: bots don't just re-request popular pages,
   they "bulk read" the long tail that never stays warm in cache, forcing every hit to the
   origin database. Wikimedia's own postmortems: bots are ~35% of traffic but ~65% of the
   *costly* (uncached, origin-hitting) traffic, and multimedia bandwidth rose 50% between
   Jan 2024 and April 2025 from this alone. A static dump built once and served from the
   edge costs the origin the same regardless of download volume, because nothing hits the
   database per request anymore. This is the piece the prior-art search below found nobody
   has formalized as a named architectural pillar — see "The gap" below.

3. **Verified bot identity** — cryptographic signatures over self-published "Signature
   Agent Cards" (public keys + declared identity/purpose/rate expectations), not a
   spoofable User-Agent string. Standardizing via the IETF WebBotAuth working group
   (chartered 2026 off a BoF at IETF 123), with Google, Cloudflare, Akamai, and AWS
   Bedrock AgentCore all separately documenting or shipping pieces of it. Verifying a
   signature is cheap; the open problem is the registry/trust layer, not the crypto.

4. **Graduated, published rate limits** — "the stronger the provided identification, the
   higher the provided limit" (Wikimedia's own March 2026 framing), so a well-behaved bot
   knows its ceiling in advance instead of discovering it via 403s. Cloudflare's
   "pay-per-crawl" (HTTP 402 + signed identity, 2025) is the metered/priced version of the
   same idea.

## Naming and domains

- **robots.yes** as a literal domain doesn't exist — `.yes` is not a delegated gTLD.
  Verified twice, 2026-08-27: IANA's root zone TLD list (fetched fresh, version
  `2026082700`) has no `YES` entry, and a live query to a root server for `yes NS`
  returns the root zone's own SOA instead of a delegation. No trace of `.yes` ever being
  applied for either (Amazon holds 54 gTLDs — `.bot`, `.buy`, `.free`, `.spot`, etc. — and
  none of them is `.yes`).
- **robotsyes.dev** — canonical choice, developer/spec audience. Confirmed available via
  RDAP query to the .dev registry (Google/Charleston Road Registry), explicit
  `"robotsyes.dev not found"` response, 2026-08-27.
- **robotsyes.com** — defensive registration, point at `.dev` or park it. Confirmed
  available via RDAP query to Verisign's `.com` registry (404, same date).
- **yesbots.dev** — backup pun, also confirmed available same way, unused so far.
- RDAP "not found" means not registered *as of that query*, not reserved — re-check before
  actually buying.

## Prior art / competitive landscape

Four-agent parallel research pass, 2026-08-27. Findings, by layer:

**Commercial products** — no bundled implementation-as-a-service product exists. Two
adjacent categories: (a) scanners/scorers that grade a site against these pillars without
implementing them — Cloudflare's `isitagentready.com` (launched April 17, 2026, 19 checks
across 5 categories, hands back copy-paste fix prompts for coding agents, exposes an MCP
server at `/.well-known/mcp.json`) and AgentGrade (`agentgrade.com`, 0-100 score, GitHub
Action, public leaderboard); (b) tools for the *crawler* side, not the *site* side —
Firecrawl (YC-backed, 25k+ GitHub stars) scrapes FROM sites for AI companies, wrong
direction. Cloudflare itself is the closest informal bundle (Markdown for Agents + Web Bot
Auth + pay-per-crawl + bot management) but it's proprietary infrastructure tied to being
on their CDN, not a portable library.

**Open-source tooling** — fragmented, confirmed no unified installable toolkit. Numerous
narrow llms.txt/llms-full.txt generators (`firecrawl/llmstxt-generator`, `ammit/llms-txt`,
framework plugins like `vitepress-plugin-llmstxt`); generic (non-AI-aware) content
negotiation middleware exists as decades-old web infra (PSR-15 packages); nothing
open-source and self-hostable found for bot identity verification or identity-keyed rate
limiting — those live inside vendor infrastructure only. `agentgateway/agentgateway` and
`unclecode/crawl4ai` are agent-side tooling (calling out), not site-side (serving agents
well) — a real scope distinction, not a match.

**Standards bodies** — real institutional plumbing, no merger yet. Linux Foundation's
Agentic AI Foundation (AAIF, stood up December 2025, explicitly to keep this vendor-
neutral), a live W3C AI Agent Protocol Community Group, and an IETF CATALIST coordination
BOF heading toward formal working-group chartering at IETF #126 (July 2026). Trade
coverage's own framing: 15 months from MCP shipping (Nov 2024) to Feb 2026, the space went
from one protocol to "an ecosystem of overlapping, complementary, and occasionally
competing standards," not toward consolidation. No one expects real unification before
2027.

**Academic literature** — the closest existing match, and worth reading in full before
building anything:

> Bandara, E., Gore, R., Mukkamala, R., Gunaratna, A., et al. (20 authors total).
> **"Towards an Agent-First Web: Redesigning the Web for AI Agents."** arXiv:2606.19116,
> submitted 2026-06-17. https://arxiv.org/abs/2606.19116

Proposes a dual-layer architecture bundling agent-identification HTTP headers, graduated
rate limiting by intent/auth level, and a content-handling scheme called ATML ("Agent Text
Markup Language," described in the abstract as "a four-level human supervision tier
model" — narrower/different framing than the content-negotiation pitch above, check the
full paper before assuming they're the same mechanism). It explicitly diagnoses robots.txt
as lacking identity standards, enforcement, and any way to distinguish a personal
assistant from a mass scraper — same diagnosis this project's pitch relies on. **OAuth
delegation tokens were reported by the research subagent that found this paper but were
NOT independently confirmed in the abstract when checked directly — verify against the
full paper text before citing that detail anywhere.**

Also surfaced: `agents.json` (yet another competing partial spec, via the Agent Web
Protocol / AWP), meaning there are at least six overlapping partial specs in the wild
(llms.txt, WebMCP, SDF Protocol, CAP, agents.json, ATML) with no convergence yet.

### The gap

Across all four searches, nobody — not the academic paper, not any standards body, not
any product — treats bulk static export as a *named architectural pillar specifically
justified by cache economics* (the long-tail/origin-hit argument in pillar 2 above). It
shows up as an operational fix in Wikimedia's own postmortems, not as a formalized piece
of any proposed stack. That's the actual unclaimed part of "robots.yes" as an idea — not
"nobody's thought about agent-friendly websites" (several groups have, some closely), but
"nobody's connected the caching-infrastructure argument to the content-export argument as
one deliberate design principle."

## Confidence notes

- High confidence, well-established: robots.txt/Robots Exclusion Protocol history since
  1994 (became IETF RFC 9309 only in 2022); Wikimedia's own published bandwidth/bot-
  traffic figures (Diff blog, cited with URLs above); the arXiv paper's existence, title,
  and authors (independently fetched and checked, not just subagent-reported).
- Medium confidence: the "no bundled product/library exists" verdict — corroborated
  independently by four separate research passes converging on the same answer, which is
  reasonably strong, but none of the four did an exhaustive search.
- **Resolved** (full paper read directly, not just the abstract, in a later session): the
  OAuth-delegation-token detail is real — §4.4 describes an `Agent-Auth` header carrying a
  token derived from a human's existing subscription credential, answering "does this agent
  inherit *my* subscription." That's a different question from this project's pillar 3
  (Ed25519-signed Signature Agent Cards, answering "is this actually the agent it claims to
  be, verifiable with no live handshake") — the two are complementary, not overlapping; a
  request could carry both. ATML (the paper's proposed agent-content format) has no
  confirmed production adopter anywhere found in a broader 2026 SOTA search — pillar 1's bet
  on plain Markdown + llms.txt is the better-grounded position: llms.txt already has real
  adoption (8.7% of the top 1,000 sites, June 2026) and a W3C standardization draft in
  progress, versus ATML's single-paper status.
- The same later-session search surfaced the piece worth acting on: IETF Web Bot Auth
  (`draft-meunier-web-bot-auth-architecture`, `draft-meunier-webbotauth-registry`) is
  already running in production at Cloudflare, Anthropic, and OpenAI as the de facto
  identity layer for agent traffic — ahead of the IETF even chartering a working group for
  it. Pillar 3 built the same shape (self-published, Ed25519-signed card fetched by URL, a
  `Signature-Agent` header) with an incompatible custom wire format; that gap is now closed
  (see CHANGELOG.md's "Unreleased" entry) — pillar 3 is wire-compatible with the real,
  already-deployed scheme, not just conceptually similar to it.

## Open questions for prototyping

- ~~Read the full "Towards an Agent-First Web" paper (not just the abstract) before
  designing around it — decide whether ATML overlaps with or diverges from the content-
  negotiation pillar above.~~ Done — see Confidence notes above.
- Decide the actual deliverable shape: a spec doc (like llms.txt), a reference
  implementation/library (like nothing that currently exists per the search above), or
  both.
- Pick a static-site/framework target for a first reference implementation — candidates
  raised in conversation: something Next.js/Vercel-adjacent (content negotiation already
  has a native pattern there) or a framework-agnostic middleware.
- Register robotsyes.dev (and optionally .com) before prototyping publicly, given
  availability is a live-query snapshot, not a hold.
