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
// below does.
type Card struct {
	AgentID string    `json:"agent_id"`
	Keys    []CardKey `json:"keys"`
}

// CardKey is one signing key in a Card. Only "ed25519" is supported.
type CardKey struct {
	KeyID     string `json:"kid"`
	Algorithm string `json:"alg"`
	// PublicKey is the raw 32-byte Ed25519 public key, standard base64.
	PublicKey string `json:"public_key"`
}

func (c Card) publicKey(keyID string) (ed25519.PublicKey, bool) {
	for _, k := range c.Keys {
		if k.KeyID != keyID || k.Algorithm != "ed25519" {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(k.PublicKey)
		if err != nil || len(raw) != ed25519.PublicKeySize {
			return nil, false
		}
		return ed25519.PublicKey(raw), true
	}
	return nil, false
}

// These header names are robots.yes's own narrowed profile of the idea
// behind IETF HTTP Message Signatures (RFC 9421) and the WebBotAuth
// Signature-Agent draft — not a claim of wire compatibility with either.
// Signature-Input carries `keyid="..."` and `created=<unix-seconds>`;
// Signature carries the base64 Ed25519 signature over signatureBase().
const (
	SignatureInputHeader = "Signature-Input"
	SignatureHeader      = "Signature"
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
	Cards *CardFetcher
	// MaxSkew bounds how old a signature's `created` timestamp may be,
	// which bounds replay of a captured request. Zero disables the check
	// (not recommended outside tests).
	MaxSkew time.Duration
}

// NewSignedVerifier builds a SignedVerifier with a 5-minute replay window.
func NewSignedVerifier(cards *CardFetcher) *SignedVerifier {
	return &SignedVerifier{Cards: cards, MaxSkew: 5 * time.Minute}
}

func (v *SignedVerifier) Verify(r *http.Request) Identity {
	agentURL := r.Header.Get(SignatureAgentHeader)
	if agentURL == "" {
		return Identity{Tier: TierUnverified}
	}
	if agentID, ok := v.verifySignature(r, agentURL); ok {
		return Identity{Tier: TierVerified, AgentID: agentID}
	}
	return Identity{Tier: TierDeclared, AgentID: agentURL}
}

// verifySignature checks the Signature-Input/Signature headers against
// agentURL's published card. It returns the card's own agent_id (which
// may differ from agentURL) on success.
func (v *SignedVerifier) verifySignature(r *http.Request, agentURL string) (agentID string, ok bool) {
	sigInput := r.Header.Get(SignatureInputHeader)
	sig := r.Header.Get(SignatureHeader)
	if sigInput == "" || sig == "" {
		return "", false
	}
	keyID, created, ok := parseSignatureInput(sigInput)
	if !ok || !v.withinSkew(created) {
		return "", false
	}
	sigBytes, err := base64.StdEncoding.DecodeString(sig)
	if err != nil {
		return "", false
	}
	return v.verifyAgainstCard(r, signatureClaim{agentURL, keyID, created, sigBytes})
}

// signatureClaim is what verifyAgainstCard needs to check a decoded
// signature against the card at agentURL: which key signed it, when, and
// the signature bytes themselves.
type signatureClaim struct {
	agentURL string
	keyID    string
	created  int64
	sigBytes []byte
}

// verifyAgainstCard fetches the card at c.agentURL and checks c.sigBytes
// against the key named by c.keyID.
func (v *SignedVerifier) verifyAgainstCard(r *http.Request, c signatureClaim) (agentID string, ok bool) {
	card, err := v.Cards.Fetch(c.agentURL)
	if err != nil {
		return "", false
	}
	pub, ok := card.publicKey(c.keyID)
	if !ok {
		return "", false
	}
	msg := signatureBase(r, c.agentURL, c.created)
	if !ed25519.Verify(pub, []byte(msg), c.sigBytes) {
		return "", false
	}
	return card.AgentID, true
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

// signatureBase is the exact byte sequence a signer must sign — method,
// request path, the claimed agent identity, and the timestamp, each on
// its own line. Binding Signature-Agent and the timestamp into the
// signed message (rather than trusting them as unsigned headers) is what
// stops a captured signature from being replayed against a different
// claimed identity or indefinitely into the future.
func signatureBase(r *http.Request, agentURL string, created int64) string {
	return fmt.Sprintf("@method: %s\n@path: %s\nsignature-agent: %s\ncreated: %d",
		r.Method, r.URL.Path, agentURL, created)
}

// parseSignatureInput reads `keyid="...";created=1735689600` — a
// deliberately small structured-field parser, not the general RFC 9421
// dictionary grammar.
func parseSignatureInput(s string) (keyID string, created int64, ok bool) {
	params := make(map[string]string)
	for _, part := range strings.Split(s, ";") {
		name, val, found := strings.Cut(strings.TrimSpace(part), "=")
		if !found {
			continue
		}
		params[name] = strings.Trim(val, `"`)
	}

	keyID = params["keyid"]
	created, err := strconv.ParseInt(params["created"], 10, 64)
	if err != nil {
		return "", 0, false
	}
	return keyID, created, keyID != "" && created != 0
}
