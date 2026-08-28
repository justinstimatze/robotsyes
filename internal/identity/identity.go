// Package identity implements pillar 3: verified bot identity — currently
// stubbed. Real cryptographic verification (HTTP Message Signatures over a
// self-published Signature Agent Card, per the IETF WebBotAuth line of
// work) needs a registry/trust layer that doesn't exist in open form yet.
// This package defines the seam a real Verifier plugs into once one does,
// and ships two verifiers that work today without it.
package identity

import "net/http"

// Tier is a graduated identity tier: the stronger the identification, the
// higher the tier, and (via internal/ratelimit) the higher the ceiling.
type Tier string

const (
	// TierUnverified is the default: no identity claim was made, or the
	// claim couldn't be checked.
	TierUnverified Tier = "unverified"
	// TierDeclared is a self-published, unsigned identity claim — a bot
	// says who it is but nothing checks the claim cryptographically.
	TierDeclared Tier = "declared"
	// TierVerified is a claim backed by a valid signature. No shipped
	// Verifier grants this yet — see NoopVerifier and DeclaredVerifier.
	TierVerified Tier = "verified"
)

// Identity is what a Verifier decides about one request.
type Identity struct {
	Tier    Tier
	AgentID string // opaque identifier from the claim; empty if unverified
}

// Verifier inspects a request and returns the identity it's willing to
// grant. Implementations must be safe for concurrent use.
type Verifier interface {
	Verify(r *http.Request) Identity
}

// NoopVerifier never grants anything above TierUnverified. It's the
// correct default until a real signature-checking Verifier exists.
type NoopVerifier struct{}

func (NoopVerifier) Verify(*http.Request) Identity {
	return Identity{Tier: TierUnverified}
}

// SignatureAgentHeader is the header a bot uses to self-declare an agent
// identity under DeclaredVerifier. Named after the WebBotAuth draft's
// "Signature-Agent" header, ahead of this package actually checking a
// signature over it.
const SignatureAgentHeader = "Signature-Agent"

// DeclaredVerifier grants TierDeclared to any request carrying a
// Signature-Agent header, without checking a signature — an unsigned,
// self-published claim. It never grants TierVerified; that tier is
// reserved for a real crypto-checking Verifier.
type DeclaredVerifier struct{}

func (DeclaredVerifier) Verify(r *http.Request) Identity {
	if id := r.Header.Get(SignatureAgentHeader); id != "" {
		return Identity{Tier: TierDeclared, AgentID: id}
	}
	return Identity{Tier: TierUnverified}
}
