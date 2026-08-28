// Package chitgate is the composition-root adapter binding robots.yes's
// payments.Merchant to a chit bare-402 x402 merchant
// (github.com/justinstimatze/chit/server). It lives outside
// internal/payments on purpose: payments must stay free of the chit
// import so its gating logic stays unit-testable against a fake
// merchant, and the on-chain seam — which can only be validated live —
// is isolated here.
//
// The two-call shape and its ordering rules are ported from a sibling
// project's proven chit integration (justinstimatze/gemot's
// internal/chitgate), not invented fresh:
//
//	call 1 (no credential): robots.yes SELF-MINTS the x402 challenge to
//	                         emit — chit is not consulted. Asking chit for
//	                         call 1's challenge risks a self-charge false
//	                         positive: a credential-less call sets
//	                         User=merchantID with no OAuth, so chit's pull
//	                         /charge probe can see source==destination and
//	                         report the charge as already-settled.
//	call 2 (credential):    locally validate the credential's shape →
//	                        reserve its nonce against replay →
//	                        DetectProtocol → OpenPaymentSession →
//	                        RequirePayment (charges the session) →
//	                        CloseSession (settles on-chain) → commit the
//	                        reservation → the verified payer address.
//
// Since price is a static per-request lookup, both calls recompute the
// SAME X402PaymentRequirements deterministically — no cached challenge
// state, no correlation needed between call 1 and call 2.
//
// Two things call 2 does NOT get for free from chit, and this package
// adds explicitly:
//
//   - chit.DetectProtocol accepts any non-empty header value as a
//     "detected" credential — it does no format or signature check. Left
//     alone, a single garbage header would still force a real outbound
//     settle call to the authorization server on every rate-limited
//     request. validateCredential rejects anything that isn't at least a
//     structurally well-formed x402 "exact" authorization BEFORE any
//     network call happens, using chit's own exported
//     ExtractX402PayerAddress so the check accepts exactly what chit
//     itself would accept.
//   - chit's CloseSession (read directly in server/paymentsession.go)
//     only ever branches on a transport error and an amount comparison —
//     it never inspects SettleResult.AlreadySettled. Whether
//     auth.atxp.ai's own /settle endpoint would report AlreadySettled
//     for a DIFFERENT caller replaying an already-spent credential
//     (versus only the same caller's own retry) isn't something this
//     codebase can verify — that's third-party server behavior. The
//     replayCache below closes the gap unconditionally on robots.yes's
//     own side: it will never report a settlement for a nonce it has
//     already committed, regardless of what the authorization server
//     would have said.
package chitgate

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	chit "github.com/justinstimatze/chit/server"

	"github.com/justinstimatze/robotsyes/internal/payments"
)

// x402 challenge constants for the Base / USDC "exact" scheme — the one
// path chit's own docs describe as live-verified (settlement confirmed
// on Base mainnet, checked against the chain's own Transfer event log).
const (
	x402Version           = 2
	x402Scheme            = "exact" // EIP-3009 transferWithAuthorization
	x402MaxTimeoutSeconds = 300
	usdcEIP712Name        = "USD Coin" // EIP-712 domain name — MUST match USDC's real domain
	usdcEIP712Version     = "2"        // EIP-712 domain version on Base
	// usdcAtomicPerCent scales integer US cents to USDC atomic units (6
	// decimals): 1 cent = $0.01 = 0.01 USDC = 10_000 atomic units.
	usdcAtomicPerCent = 10_000

	// DefaultNetwork is the x402 CAIP-2 network id advertised in
	// challenges when Config.Network is unset. Base mainnet is the only
	// network this package builds a chit merchant account id for (see
	// merchantAccountPrefix) — a second chain would need that prefix
	// configurable too, not just this default. Exported so callers that
	// resolve Config before constructing (see cmd/robotsyes/main.go, for
	// publishing the effective value in the discovery document) share
	// the one definition rather than duplicating the literal.
	DefaultNetwork = "eip155:8453"
	// DefaultAsset is USD Coin's contract address on Base mainnet, used
	// when Config.Asset is unset.
	DefaultAsset = "0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913"
	// merchantAccountPrefix is chit's own account-id namespace for a
	// Base address, distinct from the CAIP-2 network id above — chit
	// addresses accounts as "base:0x...", not "eip155:8453:0x...".
	merchantAccountPrefix = "base:"
)

// Config configures the chit-backed merchant gate.
type Config struct {
	// PayoutAddress is the bare 0x EVM address payments settle to.
	// Required, must look like a well-formed address (0x + 40 hex
	// chars) — checked at construction so a typo in robotsyes.yaml
	// fails loudly at startup instead of silently advertising a bad
	// payTo to every real payer.
	PayoutAddress string
	// PriceCentsPerRequest is the flat US-cent price for one
	// over-ceiling request. Required, must be positive.
	PriceCentsPerRequest int64
	// Network is the x402 CAIP-2 network id advertised in challenges.
	// Defaults to Base mainnet ("eip155:8453") — the only network this
	// package has a merchant-account-id mapping for.
	Network string
	// Asset is the ERC-20 contract address payment is accepted in.
	// Defaults to USDC on Base.
	Asset string
}

// New builds a payments.Merchant backed by a chit bare-402 x402
// merchant.
func New(cfg Config) (payments.Merchant, error) {
	if cfg.PayoutAddress == "" {
		return nil, errors.New("chitgate: PayoutAddress is required")
	}
	if !isHexAddress(cfg.PayoutAddress) {
		return nil, fmt.Errorf("chitgate: PayoutAddress %q is not a well-formed 0x EVM address", cfg.PayoutAddress)
	}
	if cfg.PriceCentsPerRequest <= 0 {
		return nil, errors.New("chitgate: PriceCentsPerRequest must be positive")
	}
	network := cfg.Network
	if network == "" {
		network = DefaultNetwork
	}
	asset := cfg.Asset
	if asset == "" {
		asset = DefaultAsset
	}
	merchantID := merchantAccountPrefix + cfg.PayoutAddress
	m, err := chit.New(chit.Config{Destination: chit.StaticDestination{ID: merchantID}})
	if err != nil {
		return nil, fmt.Errorf("chitgate: building merchant: %w", err)
	}
	return &chitMerchant{
		m:          m,
		merchantID: merchantID,
		payTo:      cfg.PayoutAddress,
		network:    network,
		asset:      asset,
		priceCents: cfg.PriceCentsPerRequest,
		replays:    newReplayCache(DefaultMaxReplayCacheEntries, DefaultReplayCacheTTL),
	}, nil
}

// chitMerchant adapts a *chit.Merchant onto payments.Merchant.
type chitMerchant struct {
	m          *chit.Merchant
	merchantID string // chit's own "base:0x..." account id
	payTo      string // bare on-chain address
	network    string // x402 CAIP-2 network id
	asset      string // ERC-20 contract address
	priceCents int64
	replays    *replayCache
}

// RequirePayment implements payments.Merchant.
func (c *chitMerchant) RequirePayment(ctx context.Context, req payments.PaymentRequest) (*payments.Settlement, *payments.Challenge, error) {
	reqs := c.mintRequirements(req.Resource)

	// Call 1: no credential yet — emit a self-minted challenge. chit is
	// not consulted here (see the package doc: this is the whole point
	// of the fix).
	if req.Credential == "" {
		return nil, challengeFrom(reqs), nil
	}

	// Reject anything too malformed to be worth a network round trip
	// before touching chit at all — see the package doc for why this
	// check exists. payer is reused at the end rather than re-extracted
	// after settlement, so a structurally invalid credential is refused
	// before any money moves, not merely tolerated after.
	payer, nonce, err := validateCredential(req.Credential)
	if err != nil {
		return nil, nil, fmt.Errorf("chitgate: %w", err)
	}

	// Claim the nonce before attempting settlement — robots.yes's own
	// replay defense, independent of the authorization server's
	// idempotency semantics (see the package doc). Every reserve that
	// succeeds is matched by exactly one commit or release below.
	if !c.replays.reserve(nonce) {
		return nil, nil, errors.New("chitgate: payment credential already used")
	}
	committed := false
	defer func() {
		if !committed {
			c.replays.release(nonce)
		}
	}()

	settlement, challenge, err := c.settle(ctx, req, reqs, payer)
	if err != nil {
		return nil, nil, err
	}
	if settlement == nil {
		// Still needs payment (credential rejected / insufficient), or
		// settlement failed to reach chit at all — challenge is nil in
		// the latter case too, since c.settle only returns one of
		// (settlement, challenge) non-nil. Falls through to the
		// deferred release: this nonce never settled, so a later retry
		// (a fresh signed authorization, not this same rejected one)
		// must not be blocked by it.
		return nil, challenge, nil
	}

	committed = true
	c.replays.commit(nonce)
	return settlement, nil, nil
}

// settle performs call 2 against chit: detect the protocol, open a
// session, charge it, then settle on-chain. Split out of RequirePayment
// so the replay-cache reserve/commit/release lifecycle (which wraps this
// call) stays readable independent of chit's own multi-step call shape.
func (c *chitMerchant) settle(ctx context.Context, req payments.PaymentRequest, reqs chit.X402PaymentRequirements, payer string) (*payments.Settlement, *payments.Challenge, error) {
	// chit's Session branch skips the pull /charge entirely; the
	// requirements it settles against are the same ones advertised in
	// call 1 (recomputed by the caller, deterministically).
	hdr := http.Header{}
	hdr.Set("X-Payment", req.Credential)
	detected := chit.DetectProtocol(hdr)
	if detected == nil {
		return nil, nil, errors.New("chitgate: presented credential is not a recognized payment credential")
	}

	price, err := centsToAmount(c.priceCents)
	if err != nil {
		return nil, nil, err
	}
	sctx := chit.SettlementContext{
		PaymentRequirements:  &reqs,
		SourceAccountID:      c.merchantID,
		DestinationAccountID: c.merchantID,
	}
	session := c.m.OpenPaymentSession(*detected, sctx)

	ch, err := c.m.RequirePayment(ctx, chit.PaymentRequest{
		Price:    price,
		User:     c.merchantID, // nominal source id the bare-402 flow requires; never checked against the actual payer
		Resource: req.Resource,
		Session:  session,
	})
	if err != nil {
		return nil, nil, err
	}
	if ch != nil {
		// Never trust a chit-built challenge here — re-challenge with
		// our own minted requirements instead.
		return nil, challengeFrom(reqs), nil
	}

	// The charge is recorded against the session — settle it on-chain
	// BEFORE reporting success. A failed settle is not a paid charge:
	// fail closed.
	if err := c.m.CloseSession(ctx, session); err != nil {
		return nil, nil, fmt.Errorf("chitgate: settlement failed: %w", err)
	}
	return &payments.Settlement{PayerAddress: payer}, nil, nil
}

// mintRequirements builds the x402 "exact" requirements for the
// configured flat price. Both call 1 (challenge) and call 2 (settle)
// call this, so the settle path presents exactly what was advertised.
func (c *chitMerchant) mintRequirements(resource string) chit.X402PaymentRequirements {
	return chit.X402PaymentRequirements{
		X402Version: x402Version,
		Accepts: []chit.X402PaymentOption{{
			Scheme:            x402Scheme,
			Network:           c.network,
			Amount:            fmt.Sprintf("%d", c.priceCents*usdcAtomicPerCent),
			Resource:          resource,
			Description:       "robots.yes rate-limit overflow",
			PayTo:             c.payTo,
			MaxTimeoutSeconds: x402MaxTimeoutSeconds,
			Asset:             c.asset,
			Extra:             map[string]any{"name": usdcEIP712Name, "version": usdcEIP712Version},
		}},
	}
}

// challengeFrom wraps minted requirements in the Challenge shape the
// proxy writes as the 402 body.
func challengeFrom(reqs chit.X402PaymentRequirements) *payments.Challenge {
	return &payments.Challenge{
		StatusCode: http.StatusPaymentRequired,
		Body:       map[string]any{"x402": reqs},
	}
}

// centsToAmount converts integer US cents to a chit Amount via its
// decimal string parser (0.01 USDC granularity), so 100 -> "1.00", 4 ->
// "0.04".
func centsToAmount(cents int64) (chit.Amount, error) {
	if cents <= 0 {
		return chit.Amount{}, fmt.Errorf("chitgate: price must be positive, got %d cents", cents)
	}
	return chit.ParseAmount(fmt.Sprintf("%d.%02d", cents/100, cents%100))
}

// validateCredential locally checks that credential is at least a
// structurally well-formed x402 "exact" authorization, and returns the
// payer address and nonce it carries. It does NOT check the
// cryptographic signature — only chit's own settle call (which reaches
// the authorization server) actually verifies that. The point is
// narrower: reject anything too malformed to be worth a network round
// trip at all, before chit.DetectProtocol (which accepts any non-empty
// string) ever gets a chance to trigger one.
//
// payer is extracted via chit's own exported ExtractX402PayerAddress
// rather than a second hand-rolled JSON reader, so this check accepts
// exactly what chit itself would accept for that field. nonce has no
// exported chit equivalent, so decodeX402Nonce mirrors chit's own
// (unexported) three-way decode fallback — see that function's doc
// comment.
func validateCredential(credential string) (payer, nonce string, err error) {
	payer, err = chit.ExtractX402PayerAddress(credential)
	if err != nil {
		return "", "", err
	}
	if !isHexAddress(payer) {
		return "", "", fmt.Errorf("credential's authorization.from %q is not a well-formed address", payer)
	}
	nonce, err = decodeX402Nonce(credential)
	if err != nil {
		return "", "", err
	}
	return payer, nonce, nil
}

// decodeX402Nonce extracts payload.authorization.nonce from a
// credential. Mirrors chit's own unexported parseCredentialJSON decode
// chain (server/protocol.go) exactly — standard base64, then base64url
// without padding (the form MPP credentials use), then raw JSON — since
// that helper isn't exported for this package to reuse directly. Used
// only to key the replay cache; well-formedness of the credential as a
// whole is chit.ExtractX402PayerAddress's job, called first in
// validateCredential.
func decodeX402Nonce(credential string) (string, error) {
	payload := decodeCredentialJSON(credential)
	if payload == nil {
		return "", errors.New("credential is not valid base64 or raw JSON")
	}
	inner, ok := payload["payload"].(map[string]any)
	if !ok {
		return "", errors.New("credential has no payload field")
	}
	auth, ok := inner["authorization"].(map[string]any)
	if !ok {
		return "", errors.New("credential's payload has no authorization field")
	}
	nonce, _ := auth["nonce"].(string)
	if nonce == "" {
		return "", errors.New("credential's authorization has no nonce")
	}
	return nonce, nil
}

// credentialDecoders is chit's own unexported parseCredentialJSON
// (server/protocol.go) decode order, tried in sequence: standard
// base64 first, then base64url without padding, then raw JSON. Keeping
// this order in lockstep with chit's matters — a divergence would make
// this package reject (or accept) a shape chit itself does the opposite
// for.
var credentialDecoders = []func(string) ([]byte, error){
	base64.StdEncoding.DecodeString,
	base64.RawURLEncoding.DecodeString,
	func(s string) ([]byte, error) { return []byte(s), nil },
}

// decodeCredentialJSON mirrors chit's unexported parseCredentialJSON:
// try each decoder in credentialDecoders, in order, and return the first
// one that both decodes and unmarshals to a non-nil JSON object.
func decodeCredentialJSON(credential string) map[string]any {
	for _, decode := range credentialDecoders {
		if obj := decodeJSONObject(decode, credential); obj != nil {
			return obj
		}
	}
	return nil
}

func decodeJSONObject(decode func(string) ([]byte, error), credential string) map[string]any {
	raw, err := decode(credential)
	if err != nil {
		return nil
	}
	var obj map[string]any
	if json.Unmarshal(raw, &obj) != nil || obj == nil {
		return nil
	}
	return obj
}

// isHexAddress reports whether s is a 20-byte 0x-prefixed hex address.
// Ported from a sibling project's own x402 credential validation
// (justinstimatze/gemot's internal/payments/x402gate.go) — the same
// check, used here for both the operator-configured payout address and
// a credential's extracted payer address.
func isHexAddress(s string) bool {
	if len(s) != 42 || s[0] != '0' || s[1] != 'x' {
		return false
	}
	for i := 2; i < len(s); i++ {
		if !isHexDigit(s[i]) {
			return false
		}
	}
	return true
}

func isHexDigit(b byte) bool {
	switch {
	case b >= '0' && b <= '9':
		return true
	case b >= 'a' && b <= 'f':
		return true
	case b >= 'A' && b <= 'F':
		return true
	default:
		return false
	}
}
