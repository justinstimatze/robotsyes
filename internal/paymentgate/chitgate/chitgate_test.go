package chitgate

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	chit "github.com/justinstimatze/chit/server"

	"github.com/justinstimatze/robotsyes/internal/payments"
)

const testPayoutAddress = "0x000000000000000000000000000000000000dEaD"
const testPayerAddress = "0x111111111111111111111111111111111111111a"

// testCredential builds a minimal, structurally well-formed x402
// credential (the shape validateCredential and chit's own
// ExtractX402PayerAddress read), base64-std-encoded the same way a real
// x402 client's X-Payment/Payment-Signature header value is.
func testCredential(t *testing.T, from, nonce string) string {
	t.Helper()
	body := map[string]any{
		"payload": map[string]any{
			"authorization": map[string]any{
				"from":  from,
				"nonce": nonce,
			},
		},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshaling test credential: %v", err)
	}
	return base64.StdEncoding.EncodeToString(raw)
}

// These tests exercise only the credential-less (call 1, self-minted
// challenge) path, which never touches chit.Merchant's network calls —
// see the package doc comment. Anything past a presented credential
// needs a real authorization server and is out of scope for unit tests
// (mirrors chit's own unit-vs-serverlive split; see the manual
// smoke-test note on New).

func TestNewRejectsMissingPayoutAddress(t *testing.T) {
	_, err := New(Config{PriceCentsPerRequest: 1})
	if err == nil {
		t.Fatal("expected an error for a missing PayoutAddress")
	}
}

func TestNewRejectsNonPositivePrice(t *testing.T) {
	_, err := New(Config{PayoutAddress: "0x000000000000000000000000000000000000dEaD", PriceCentsPerRequest: 0})
	if err == nil {
		t.Fatal("expected an error for a non-positive PriceCentsPerRequest")
	}
}

func TestNewSucceedsAndDefaultsNetworkAndAsset(t *testing.T) {
	m, err := New(Config{
		PayoutAddress:        "0x000000000000000000000000000000000000dEaD",
		PriceCentsPerRequest: 1,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, challenge, err := m.RequirePayment(context.Background(), payments.PaymentRequest{Resource: "/foo"})
	if err != nil {
		t.Fatalf("RequirePayment: %v", err)
	}
	if challenge == nil {
		t.Fatal("expected a challenge with no credential presented")
	}
	reqs := decodeRequirements(t, challenge)
	if reqs.Accepts[0].Network != DefaultNetwork {
		t.Errorf("network = %q, want default %q", reqs.Accepts[0].Network, DefaultNetwork)
	}
	if reqs.Accepts[0].Asset != DefaultAsset {
		t.Errorf("asset = %q, want default %q", reqs.Accepts[0].Asset, DefaultAsset)
	}
}

func TestRequirePaymentSelfMintsChallengeWithoutCredential(t *testing.T) {
	m, err := New(Config{
		PayoutAddress:        "0x000000000000000000000000000000000000dEaD",
		PriceCentsPerRequest: 250, // $2.50
		Network:              "eip155:8453",
		Asset:                "0xasset",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	settlement, challenge, err := m.RequirePayment(context.Background(), payments.PaymentRequest{Resource: "/foo"})
	if err != nil {
		t.Fatalf("RequirePayment: %v", err)
	}
	if settlement != nil {
		t.Fatal("expected no settlement with no credential presented")
	}
	if challenge == nil {
		t.Fatal("expected a challenge")
	}
	if challenge.StatusCode != 402 {
		t.Errorf("StatusCode = %d, want 402", challenge.StatusCode)
	}

	reqs := decodeRequirements(t, challenge)
	if len(reqs.Accepts) != 1 {
		t.Fatalf("Accepts has %d entries, want 1", len(reqs.Accepts))
	}
	opt := reqs.Accepts[0]
	// 250 cents = $2.50 = 2_500_000 USDC atomic units (10_000 per cent).
	if opt.Amount != "2500000" {
		t.Errorf("Amount = %q, want %q", opt.Amount, "2500000")
	}
	if opt.Scheme != "exact" {
		t.Errorf("Scheme = %q, want %q", opt.Scheme, "exact")
	}
	if opt.Network != "eip155:8453" {
		t.Errorf("Network = %q, want %q", opt.Network, "eip155:8453")
	}
	if opt.Asset != "0xasset" {
		t.Errorf("Asset = %q, want %q", opt.Asset, "0xasset")
	}
	if opt.PayTo != "0x000000000000000000000000000000000000dEaD" {
		t.Errorf("PayTo = %q, want the configured payout address", opt.PayTo)
	}
	if opt.Resource != "/foo" {
		t.Errorf("Resource = %q, want %q", opt.Resource, "/foo")
	}
}

func TestRequirePaymentChallengeIsDeterministicAcrossCalls(t *testing.T) {
	m, err := New(Config{PayoutAddress: "0x000000000000000000000000000000000000dEaD", PriceCentsPerRequest: 1})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, c1, err := m.RequirePayment(context.Background(), payments.PaymentRequest{Resource: "/foo"})
	if err != nil {
		t.Fatalf("RequirePayment (call 1): %v", err)
	}
	_, c2, err := m.RequirePayment(context.Background(), payments.PaymentRequest{Resource: "/foo"})
	if err != nil {
		t.Fatalf("RequirePayment (call 2): %v", err)
	}
	r1, r2 := decodeRequirements(t, c1), decodeRequirements(t, c2)
	if r1.Accepts[0].Amount != r2.Accepts[0].Amount || r1.Accepts[0].Network != r2.Accepts[0].Network {
		t.Errorf("two credential-less calls for the same resource minted different requirements: %+v vs %+v", r1, r2)
	}
}

func decodeRequirements(t *testing.T, challenge *payments.Challenge) chit.X402PaymentRequirements {
	t.Helper()
	reqs, ok := challenge.Body["x402"].(chit.X402PaymentRequirements)
	if !ok {
		t.Fatalf("challenge.Body[\"x402\"] is %T, want chit.X402PaymentRequirements", challenge.Body["x402"])
	}
	return reqs
}

func TestNewRejectsMalformedPayoutAddress(t *testing.T) {
	cases := []string{
		"",                                       // caught by the earlier empty check, but confirm no panic
		"0xdead",                                 // too short
		"1111111111111111111111111111111111111a", // missing 0x prefix
		"0x111111111111111111111111111111111111zz", // non-hex trailing chars
		"0x0000000000000000000000000000000000dEaD", // the exact off-by-two typo this check exists to catch
	}
	for _, addr := range cases {
		if _, err := New(Config{PayoutAddress: addr, PriceCentsPerRequest: 1}); err == nil {
			t.Errorf("New(PayoutAddress=%q) succeeded, want an error for a malformed address", addr)
		}
	}
}

func TestIsHexAddress(t *testing.T) {
	cases := []struct {
		addr string
		want bool
	}{
		{testPayoutAddress, true},
		{"", false},
		{"0x" + "1234567890abcdef1234567890abcdef1234567", false},   // 39 hex chars, one short
		{"0x" + "1234567890abcdef1234567890abcdef123456789", false}, // 41 hex chars, one long
		{"1234567890abcdef1234567890abcdef12345678", false},         // no 0x prefix
		{"0X1234567890abcdef1234567890abcdef12345678", false},       // capital X
		{"0x" + "1234567890ABCDEF1234567890abcdef1234567g", false},  // trailing non-hex
	}
	for _, c := range cases {
		if got := isHexAddress(c.addr); got != c.want {
			t.Errorf("isHexAddress(%q) = %v, want %v", c.addr, got, c.want)
		}
	}
}

func TestValidateCredentialAcceptsWellFormedCredential(t *testing.T) {
	cred := testCredential(t, testPayerAddress, "nonce-1")
	payer, nonce, err := validateCredential(cred)
	if err != nil {
		t.Fatalf("validateCredential: %v", err)
	}
	if payer != testPayerAddress {
		t.Errorf("payer = %q, want %q", payer, testPayerAddress)
	}
	if nonce != "nonce-1" {
		t.Errorf("nonce = %q, want %q", nonce, "nonce-1")
	}
}

func TestValidateCredentialRejectsGarbage(t *testing.T) {
	if _, _, err := validateCredential("not valid base64 or json"); err == nil {
		t.Fatal("expected an error for a garbage credential")
	}
}

func TestValidateCredentialRejectsMissingNonce(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{
		"payload": map[string]any{"authorization": map[string]any{"from": testPayerAddress}},
	})
	cred := base64.StdEncoding.EncodeToString(raw)
	if _, _, err := validateCredential(cred); err == nil {
		t.Fatal("expected an error for a credential with no nonce")
	}
}

func TestValidateCredentialRejectsMalformedFromAddress(t *testing.T) {
	cred := testCredential(t, "not-an-address", "nonce-1")
	if _, _, err := validateCredential(cred); err == nil {
		t.Fatal("expected an error for a credential whose authorization.from is not a well-formed address")
	}
}

// TestRequirePaymentRejectsMalformedCredentialWithoutNetworkCall is the
// regression test for the amplification finding this package's doc
// comment names: chit.DetectProtocol accepts any non-empty header value,
// so without a local well-formedness gate, a single garbage credential
// would still force a real outbound settle call. This test runs with no
// network available at all (no test server, no stub) — if
// RequirePayment tried to reach chit's settle path here, it would hang
// or error on a connection failure instead of returning promptly with
// the local validation error asserted below.
func TestRequirePaymentRejectsMalformedCredentialWithoutNetworkCall(t *testing.T) {
	m, err := New(Config{PayoutAddress: testPayoutAddress, PriceCentsPerRequest: 1})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	done := make(chan struct{})
	var settlement *payments.Settlement
	var challenge *payments.Challenge
	var reqErr error
	go func() {
		settlement, challenge, reqErr = m.RequirePayment(context.Background(), payments.PaymentRequest{
			Resource:   "/foo",
			Credential: "garbage-not-a-real-credential",
		})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RequirePayment did not return within 2s — a malformed credential should be rejected locally, never reach the network")
	}
	if reqErr == nil {
		t.Fatal("expected an error for a malformed credential")
	}
	if settlement != nil || challenge != nil {
		t.Errorf("expected no settlement and no challenge alongside the error, got settlement=%v challenge=%v", settlement, challenge)
	}
}

// TestRequirePaymentRejectsReplayedNonce exercises the replay-cache
// integration directly (constructing a chitMerchant rather than going
// through New, so the test can pre-seed a committed nonce) without
// touching the network: rejection happens at c.replays.reserve, before
// chit.DetectProtocol or any settle call.
func TestRequirePaymentRejectsReplayedNonce(t *testing.T) {
	c := &chitMerchant{
		payTo:      testPayoutAddress,
		network:    DefaultNetwork,
		asset:      DefaultAsset,
		priceCents: 1,
		merchantID: merchantAccountPrefix + testPayoutAddress,
		replays:    newReplayCache(10, time.Minute),
	}
	c.replays.commit("already-spent-nonce")

	cred := testCredential(t, testPayerAddress, "already-spent-nonce")
	settlement, challenge, err := c.RequirePayment(context.Background(), payments.PaymentRequest{
		Resource:   "/foo",
		Credential: cred,
	})
	if err == nil {
		t.Fatal("expected an error for a credential carrying an already-committed nonce")
	}
	if settlement != nil || challenge != nil {
		t.Errorf("expected no settlement and no challenge, got settlement=%v challenge=%v", settlement, challenge)
	}
}
