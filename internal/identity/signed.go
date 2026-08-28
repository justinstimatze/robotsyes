package identity

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/justinstimatze/robotsyes/internal/httpx"
)

// Card is the self-published Signature Agent Card a requester serves at
// the URL it names in its own Signature-Agent header — "here's who I am
// and which keys I sign with." It's self-attested: fetching it proves
// nothing about the fetched content beyond what the signature check
// below does. The shape is the WebBotAuth registry draft's Signature
// Agent Card format (draft-meunier-webbotauth-registry): every field
// besides Keys is optional there, but this package only ever reads
// ClientName and Keys.
type Card struct {
	ClientName string `json:"client_name,omitempty"`
	Keys       []JWK  `json:"keys"`
}

// JWK is one signing key in a Card, in JSON Web Key form (RFC 7517) using
// the Octet Key Pair type (RFC 8037). Only Ed25519 keys
// (Kty=="OKP", Crv=="Ed25519") are recognized; any other key type or curve
// is ignored, not an error — a card MAY list keys this verifier doesn't
// support alongside ones it does.
type JWK struct {
	Kty string `json:"kty"`
	Crv string `json:"crv"`
	Kid string `json:"kid"`
	// X is the public key, base64url-encoded per RFC 4648 §5 with no
	// padding — this is JWK's own encoding (RFC 8037 §2), not the
	// standard base64 used elsewhere in this file for the Signature
	// header itself.
	X   string `json:"x"`
	Use string `json:"use,omitempty"`
}

func (c Card) publicKey(keyID string) (ed25519.PublicKey, bool) {
	for _, k := range c.Keys {
		if k.Kid != keyID || k.Kty != "OKP" || k.Crv != "Ed25519" {
			continue
		}
		raw, err := base64.RawURLEncoding.DecodeString(k.X)
		if err != nil || len(raw) != ed25519.PublicKeySize {
			return nil, false
		}
		return ed25519.PublicKey(raw), true
	}
	return nil, false
}

// agentID is what identity.Identity.AgentID reports for a request this
// card verified: the card's own ClientName when it publishes one, else
// the URL the card was fetched from. The card is self-attested either
// way — this is "which name did the agent's own card give itself", not an
// independently verified identity beyond "holds the private key for the
// signature that just checked out".
func (c Card) agentID(cardURL string) string {
	if c.ClientName != "" {
		return c.ClientName
	}
	return cardURL
}

// These header names and the signature scheme they carry are
// wire-compatible with IETF HTTP Message Signatures (RFC 9421) as
// profiled by the WebBotAuth Signature-Agent draft
// (draft-meunier-web-bot-auth-architecture): Signature-Input is an RFC
// 9421 signature-params dictionary entry covering "@authority" and
// optionally "signature-agent", with created/expires/keyid/alg/nonce/tag
// parameters; Signature carries the resulting Ed25519 signature, base64
// (standard, not url-safe — RFC 9421 §4.2's byte-sequence encoding).
//
// One deliberate deviation from method+path-scoped signing: per the
// draft, a signature covers the request's authority (and, if present, the
// claimed agent identity) but not its method or path — a single signature
// is valid for any request to this host until it expires, with the nonce
// (checked against a replay cache, see verifySignature) as the sole
// per-request defense. That's the trade this package makes for wire
// compatibility with real WebBotAuth signers (Cloudflare, Anthropic,
// OpenAI all speak this exact scheme) rather than robots.yes's own
// narrower, tighter-scoped signing convention.
const (
	SignatureInputHeader = "Signature-Input"
	SignatureHeader      = "Signature"
)

// wbaAlgorithm and wbaTag are the two literal signature-params values this
// package requires — see the draft's §4.2: alg identifies the signing
// algorithm (only "ed25519" is supported here), and tag distinguishes this
// signature scheme from any other RFC 9421 usage sharing the same
// request.
const (
	wbaAlgorithm = "ed25519"
	wbaTag       = "web-bot-auth"
)

// CardFetcher fetches and caches Signature Agent Cards by URL.
type CardFetcher struct {
	client *http.Client
	cache  *cardCache
}

// NewCardFetcher builds a CardFetcher that re-fetches a card at most once
// per ttl, keeping at most maxEntries cards cached at a time (pass
// DefaultMaxCardCacheEntries absent a specific reason to size it
// differently). The card URL comes straight from an unauthenticated
// request header (Signature-Agent), which shapes two independent
// defenses here:
//
//   - The client refuses to dial anything that resolves to a private,
//     loopback, or link-local address — otherwise a requester could
//     point it at internal infrastructure (a cloud metadata endpoint, an
//     admin port) and use this server as an SSRF proxy.
//   - The card must be fetched over https. TierVerified's whole meaning
//     rests on "this key really belongs to whoever's signing" — fetching
//     it over plain http would let anyone in a network position between
//     this server and the card URL swap in their own key and claim any
//     agent identity they like.
func NewCardFetcher(ttl time.Duration, maxEntries int) *CardFetcher {
	return &CardFetcher{
		client: &http.Client{
			Timeout:   5 * time.Second,
			Transport: &http.Transport{DialContext: safeDialContext},
		},
		cache: newCardCache(maxEntries, ttl),
	}
}

// validateCardURL rejects anything that isn't a well-formed https URL,
// without doing any network I/O — kept separate from Fetch so it's
// unit-testable on its own.
func validateCardURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid agent card URL: %w", err)
	}
	if u.Scheme != "https" {
		return fmt.Errorf("agent card URL must use https, got %q", u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("agent card URL has no host")
	}
	return nil
}

// safeDialContext resolves addr, rejects it if every candidate IP is
// private/loopback/link-local/reserved, and dials the first IP that
// passed — directly, by address, rather than re-resolving the hostname a
// second time at connect time. Re-resolving would leave a DNS-rebinding
// gap: a name that resolves to a public IP at check time and a private
// one moments later.
func safeDialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	for _, ip := range ips {
		if isDisallowedTarget(ip) {
			continue
		}
		d := net.Dialer{Timeout: 5 * time.Second}
		return d.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
	}
	return nil, fmt.Errorf("refusing to fetch agent card from %s: resolves only to private/reserved addresses", host)
}

func isDisallowedTarget(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast()
}

// maxCardResponseBytes bounds how much of a card response Fetch will
// read. The card URL is the requester's own choice, so the server on the
// other end is effectively attacker-controlled; a real card is a few
// hundred bytes, so 64KB is generous headroom, not a tight budget. The
// client's 5-second timeout bounds fetch *time*, not *bytes* — a fast
// connection can push a lot of data well within that window, so this is
// a separate, necessary bound.
const maxCardResponseBytes = 64 * 1024

// Fetch returns the card at cardURL, from cache if it's fresh.
func (f *CardFetcher) Fetch(cardURL string) (Card, error) {
	if err := validateCardURL(cardURL); err != nil {
		return Card{}, err
	}
	if card, ok := f.cache.get(cardURL); ok {
		return card, nil
	}

	body, err := httpx.GetBounded(f.client, cardURL, maxCardResponseBytes)
	if err != nil {
		return Card{}, err
	}
	var card Card
	if err := json.Unmarshal(body, &card); err != nil {
		return Card{}, fmt.Errorf("decoding agent card: %w", err)
	}

	f.cache.put(cardURL, card)
	return card, nil
}

// SignedVerifier grants TierVerified to a request whose Signature header
// is a valid Ed25519 signature, checked against the key its own
// Signature-Agent card publishes. It degrades gracefully: a
// Signature-Agent header with no valid signature still earns
// TierDeclared (the same unsigned claim DeclaredVerifier grants), and no
// header at all earns TierUnverified.
type SignedVerifier struct {
	Cards  *CardFetcher
	Nonces *nonceCache
	// MaxSkew bounds how old a signature's `created` timestamp may be,
	// which bounds replay of a captured request. Zero disables the check
	// (not recommended outside tests).
	MaxSkew time.Duration
	// MaxValidity bounds how far a signature's signer-chosen `expires`
	// may sit beyond `created`. Without this, a signer could set an
	// arbitrarily distant expiry and force the nonce cache (see Nonces)
	// to remember that nonce for just as long to keep the replay defense
	// sound — this caps that worst case at the same order of magnitude
	// as MaxSkew. Zero disables the check.
	MaxValidity time.Duration
}

// NewSignedVerifier builds a SignedVerifier with a 5-minute replay window
// and a matching bound on signature validity.
func NewSignedVerifier(cards *CardFetcher) *SignedVerifier {
	return &SignedVerifier{
		Cards:       cards,
		Nonces:      newNonceCache(DefaultMaxNonceCacheEntries, 5*time.Minute),
		MaxSkew:     5 * time.Minute,
		MaxValidity: 5 * time.Minute,
	}
}

func (v *SignedVerifier) Verify(r *http.Request) Identity {
	agentURL, ok := unquoteSignatureAgent(r.Header.Get(SignatureAgentHeader))
	if !ok || agentURL == "" {
		return Identity{Tier: TierUnverified}
	}
	if agentID, ok := v.verifySignature(r, agentURL); ok {
		return Identity{Tier: TierVerified, AgentID: agentID}
	}
	return Identity{Tier: TierDeclared, AgentID: agentURL}
}

// verifySignature checks the Signature-Input/Signature headers against
// agentURL's published card. It returns the card's own agent identity
// (which may differ from agentURL) on success.
func (v *SignedVerifier) verifySignature(r *http.Request, agentURL string) (agentID string, ok bool) {
	sigInput := r.Header.Get(SignatureInputHeader)
	sig := r.Header.Get(SignatureHeader)
	if sigInput == "" || sig == "" {
		return "", false
	}
	p, ok := parseSignatureInput(sigInput)
	if !ok || !v.withinSkew(p.created) || !v.withinValidity(p) {
		return "", false
	}
	sigBytes, err := base64.StdEncoding.DecodeString(sig)
	if err != nil {
		return "", false
	}
	agentID, ok = v.verifyAgainstCard(r, agentURL, p, sigBytes)
	if !ok {
		return "", false
	}
	// The nonce is only spent once the signature is confirmed valid — an
	// attacker replaying a *bad* signature under a victim's real nonce
	// must not be able to burn that nonce and deny the victim's own,
	// genuine request.
	if v.Nonces.seen(p.nonce) {
		return "", false
	}
	return agentID, true
}

// verifyAgainstCard fetches the card at agentURL and checks sigBytes
// against the key p.keyID names, over the signature base derived from p.
func (v *SignedVerifier) verifyAgainstCard(r *http.Request, agentURL string, p sigParams, sigBytes []byte) (agentID string, ok bool) {
	card, err := v.Cards.Fetch(agentURL)
	if err != nil {
		return "", false
	}
	pub, ok := card.publicKey(p.keyID)
	if !ok {
		return "", false
	}
	msg := signatureBase(r, agentURL, p)
	if !ed25519.Verify(pub, []byte(msg), sigBytes) {
		return "", false
	}
	return card.agentID(agentURL), true
}

// withinSkew reports whether created is within MaxSkew of now. A
// non-positive MaxSkew disables the check.
func (v *SignedVerifier) withinSkew(created int64) bool {
	if v.MaxSkew <= 0 {
		return true
	}
	age := time.Since(time.Unix(created, 0))
	if age < 0 {
		age = -age
	}
	return age <= v.MaxSkew
}

// withinValidity reports whether p's signature is still live (now <=
// expires) and whether the signer didn't ask for an unreasonably long
// validity window (expires - created <= MaxValidity). A non-positive
// MaxValidity disables the second check only — expiry itself is always
// enforced, since expires is a mandatory WebBotAuth signature parameter.
func (v *SignedVerifier) withinValidity(p sigParams) bool {
	now := time.Now().Unix()
	if now > p.expires {
		return false
	}
	if v.MaxValidity <= 0 {
		return true
	}
	return time.Duration(p.expires-p.created)*time.Second <= v.MaxValidity
}

// signatureBase is the RFC 9421 signature base for p: one line per
// covered component, followed by the @signature-params line itself — the
// exact byte sequence a WebBotAuth-conformant signer signs. No trailing
// newline after the last line (RFC 9421 §2.5).
//
// The @signature-params line echoes p.raw — the component-list-and-params
// substring exactly as the signer sent it in Signature-Input — rather than
// reassembling it from the parsed fields. RFC 9421 requires this line to
// be that value verbatim, byte for byte; a signer is free to serialize its
// params in any order, and reconstructing from parsed fields in a fixed
// order would silently break verification against any signer that ordered
// them differently (the WebBotAuth draft's own worked examples use
// created/keyid/alg/expires/nonce/tag, not alphabetical or field-declared
// order).
//
// Deliberately not general RFC 9421 base construction: only the two
// covered-component shapes parseSignatureInput accepts are handled, so
// this always produces exactly what a conformant signer sent — see the
// host-only-scope note on the header constants above for why @method and
// @path are absent.
func signatureBase(r *http.Request, agentURL string, p sigParams) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\"@authority\": %s\n", strings.ToLower(r.Host))

	if p.coversSignatureAgent {
		agentComponent := `"signature-agent"`
		if p.agentLabel != "" {
			agentComponent = fmt.Sprintf(`"signature-agent";key=%q`, p.agentLabel)
		}
		fmt.Fprintf(&b, "%s: %q\n", agentComponent, agentURL)
	}

	fmt.Fprintf(&b, `"@signature-params": %s`, p.raw)
	return b.String()
}

// sigParams is a parsed Signature-Input: which components it covers, the
// individual signature-params values (used for the semantic checks in
// verifySignature/withinSkew/withinValidity), and the raw component-list-
// and-params substring signatureBase echoes verbatim.
type sigParams struct {
	coversSignatureAgent bool
	// agentLabel is the key= parameter on the signature-agent component
	// (the current, RECOMMENDED form) — empty when the component list
	// used the legacy unlabeled form, or when signature-agent isn't
	// covered at all.
	agentLabel string
	created    int64
	expires    int64
	keyID      string
	nonce      string
	// raw is `(<component-list>);<params...>` exactly as received —
	// see signatureBase's doc comment for why this can't be
	// reconstructed from the parsed fields above.
	raw string
}

// parseSignatureInput reads a WebBotAuth-shaped Signature-Input value:
// `<label>=(<component-list>);created=...;expires=...;keyid="...";
// alg="ed25519";nonce="...";tag="web-bot-auth"`. This is a deliberately
// small, purpose-built reader for exactly the two component-list shapes
// this package supports — `("@authority")` and `("@authority"
// "signature-agent"[;key="..."])` — not a general RFC 8941 structured-field
// parser. Anything else, or a missing/mismatched required parameter,
// fails.
func parseSignatureInput(s string) (sigParams, bool) {
	s = strings.TrimSpace(s)
	_, rest, ok := strings.Cut(s, "=")
	if !ok {
		return sigParams{}, false
	}
	rest = strings.TrimSpace(rest)
	if !strings.HasPrefix(rest, "(") {
		return sigParams{}, false
	}
	closeParen := strings.Index(rest, ")")
	if closeParen < 0 {
		return sigParams{}, false
	}

	p, ok := parseComponentList(rest[1:closeParen])
	if !ok {
		return sigParams{}, false
	}
	if !parseSignatureParams(rest[closeParen+1:], &p) {
		return sigParams{}, false
	}
	p.raw = rest
	return p, true
}

// parseComponentList recognizes exactly the two covered-component shapes
// this package supports (see parseSignatureInput's doc comment).
func parseComponentList(s string) (sigParams, bool) {
	const authorityOnly = `"@authority"`
	const authorityAndAgent = `"@authority" "signature-agent"`

	switch {
	case s == authorityOnly:
		return sigParams{}, true
	case s == authorityAndAgent:
		return sigParams{coversSignatureAgent: true}, true
	case strings.HasPrefix(s, authorityAndAgent+`;key="`) && strings.HasSuffix(s, `"`):
		label := strings.TrimSuffix(strings.TrimPrefix(s, authorityAndAgent+`;key="`), `"`)
		if label == "" {
			return sigParams{}, false
		}
		return sigParams{coversSignatureAgent: true, agentLabel: label}, true
	default:
		return sigParams{}, false
	}
}

// parseSignatureParams reads the `;name=value` tail following a
// Signature-Input's component list into p, requiring created, expires,
// keyid, and nonce to be present, and alg/tag to match the literal values
// this package supports.
func parseSignatureParams(s string, p *sigParams) bool {
	params := make(map[string]string)
	for _, part := range strings.Split(s, ";") {
		name, val, found := strings.Cut(strings.TrimSpace(part), "=")
		if !found {
			continue
		}
		params[name] = strings.Trim(val, `"`)
	}

	var err error
	if p.created, err = strconv.ParseInt(params["created"], 10, 64); err != nil {
		return false
	}
	if p.expires, err = strconv.ParseInt(params["expires"], 10, 64); err != nil {
		return false
	}
	p.keyID = params["keyid"]
	p.nonce = params["nonce"]
	return p.keyID != "" && p.nonce != "" &&
		params["alg"] == wbaAlgorithm && params["tag"] == wbaTag
}
