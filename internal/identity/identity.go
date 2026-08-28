// Package identity implements pillar 3: verified bot identity. The
// signature check itself doesn't need a registry — SignedVerifier grants
// TierVerified to any request whose signature checks out against the
// Ed25519 key published at the URL the request itself names. What's
// still missing is a trust/reputation layer over that: nothing here says
// the key at that URL belongs to a bot worth trusting, only that
// whoever holds the matching private key sent this exact request. A
// real WebBotAuth-style registry would add that layer on top; this
// package defines the seam (Verifier) it would plug into.
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
	// TierVerified is a claim backed by a valid Ed25519 signature — see
	// SignedVerifier.
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
