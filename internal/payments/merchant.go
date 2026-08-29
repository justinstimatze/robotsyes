// Package payments defines the paid-overflow seam pillar 4 gates a
// rate-limit-denied request through: a request past the published free
// ceiling can settle a payment instead of drawing a flat 429. This
// package stays free of any concrete payment rail's types (x402, chit,
// or otherwise) so the gating logic — challenge on no credential, settle
// before serve — is unit-testable against a fake Merchant with no
// network or blockchain calls. A concrete rail is adapted onto Merchant
// at the composition root (see internal/paymentgate/chitgate).
package payments

import "context"

// PaymentRequest is what the proxy hands a Merchant for one denied
// request. Credential is empty on the first (unpaid) call, which asks
// the merchant for a challenge to emit rather than settling anything.
// Price is intentionally not a field here: it's fixed per Merchant at
// construction (one flat price, not a per-request negotiation — see
// internal/paymentgate/chitgate.Config), not something the proxy chooses
// per call.
type PaymentRequest struct {
	// Resource labels what payment is for (e.g. the request path).
	Resource string
	// Credential is the payer's settle credential from the retried
	// request's payment header, if present.
	Credential string
}

// Challenge is what the proxy writes back when payment is required: an
// HTTP status (402) and a JSON body describing how to pay.
type Challenge struct {
	StatusCode int
	Body       map[string]any
}

// Settlement is a confirmed, on-chain-settled charge.
type Settlement struct {
	// PayerAddress is the verified signer of the settled payment —
	// trustworthy because the Merchant verified it, not merely decoded
	// it from an unauthenticated credential.
	PayerAddress string
}

// Merchant is the narrow slice of a concrete payment rail the proxy
// depends on. Exactly one of the first two results is non-nil on a nil
// error:
//
//	(*Settlement, nil, nil) → settled; the charge is confirmed.
//	(nil, *Challenge, nil)  → payment required; emit the challenge.
//	(nil, nil, non-nil)     → infrastructure/settle error; fail closed.
type Merchant interface {
	RequirePayment(ctx context.Context, req PaymentRequest) (*Settlement, *Challenge, error)
}
