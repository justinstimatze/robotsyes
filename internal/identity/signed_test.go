package identity

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// testCardServer serves a single Card JSON for pub at path "/card.json"
// over TLS (httptest.NewTLSServer) and reports how many times it was
// hit. TLS, not plain HTTP, because Fetch now refuses non-https URLs —
// testing against plain HTTP would silently stop exercising that check.
type testCardServer struct {
	*httptest.Server
	hits int
}

func newTestCardServer(t *testing.T, clientName, keyID string, pub ed25519.PublicKey) *testCardServer {
	t.Helper()
	tcs := &testCardServer{}
	mux := http.NewServeMux()
	mux.HandleFunc("/card.json", func(w http.ResponseWriter, r *http.Request) {
		tcs.hits++
		card := Card{
			ClientName: clientName,
			Keys: []JWK{{
				Kty: "OKP",
				Crv: "Ed25519",
				Kid: keyID,
				X:   base64.RawURLEncoding.EncodeToString(pub),
				Use: "sig",
			}},
		}
		_ = json.NewEncoder(w).Encode(card)
	})
	tcs.Server = httptest.NewTLSServer(mux)
	t.Cleanup(tcs.Close)
	return tcs
}

// newTestCardFetcher builds a CardFetcher around client instead of the
// production SSRF-safe one — every test card server here runs on
// loopback, exactly what the production dialer exists to block. client
// is normally srv.Client(), which trusts that one server's self-signed
// certificate. TestCardFetcherRefusesPrivateAddresses and
// TestCardFetcherRejectsPlainHTTP exercise the real, safe constructor
// instead.
func newTestCardFetcher(client *http.Client, ttl time.Duration) *CardFetcher {
	return &CardFetcher{
		client: client,
		cache:  newCardCache(DefaultMaxCardCacheEntries, ttl),
	}
}

// newTestSignedVerifier wraps a SignedVerifier around cards with a fresh
// nonce cache and skew/validity windows generous enough for a whole test
// function's worth of signing calls.
func newTestSignedVerifier(cards *CardFetcher) *SignedVerifier {
	return &SignedVerifier{
		Cards:       cards,
		Nonces:      newNonceCache(DefaultMaxNonceCacheEntries, time.Hour),
		MaxSkew:     time.Hour,
		MaxValidity: time.Hour,
	}
}

// signParams is what signRequest needs beyond the request itself, mirroring
// what a real WebBotAuth signer decides per request. expires defaults to
// created+60s and nonce to a fresh value derived from t's name when left
// zero, so most call sites only need to set what the test actually cares
// about.
type signParams struct {
	priv     ed25519.PrivateKey
	agentURL string
	keyID    string
	created  int64
	expires  int64
	nonce    string
}

// nonceCounter hands out distinct nonces across a test binary run, so
// signRequest's default doesn't accidentally collide across test
// functions that each build their own SignedVerifier (and thus their own,
// independent nonce cache) — collisions would only matter within one
// verifier, but a shared, ever-incrementing source avoids the question
// entirely.
var nonceCounter int64

func nextTestNonce() string {
	nonceCounter++
	return fmt.Sprintf("test-nonce-%d", nonceCounter)
}

// signRequest signs r as label "sig1", covering "@authority" and
// "signature-agent" (the current, RECOMMENDED labeled form — the legacy
// unlabeled shape is covered separately, against the draft's own worked
// example, by TestSignatureConformsToWebBotAuthDraftExamples), and sets
// the three headers a real signer would send.
func signRequest(r *http.Request, p signParams) {
	if p.expires == 0 {
		p.expires = p.created + 60
	}
	if p.nonce == "" {
		p.nonce = nextTestNonce()
	}
	const label = "sig1"
	r.Header.Set(SignatureAgentHeader, fmt.Sprintf("%s=%q", label, p.agentURL))

	raw := fmt.Sprintf(`("@authority" "signature-agent";key=%q);created=%d;expires=%d;keyid=%q;alg=%q;nonce=%q;tag=%q`,
		label, p.created, p.expires, p.keyID, wbaAlgorithm, p.nonce, wbaTag)
	r.Header.Set(SignatureInputHeader, label+"="+raw)

	sp := sigParams{
		coversSignatureAgent: true,
		agentLabel:           label,
		created:              p.created,
		expires:              p.expires,
		keyID:                p.keyID,
		nonce:                p.nonce,
		raw:                  raw,
	}
	msg := signatureBase(r, p.agentURL, sp)
	sig := ed25519.Sign(p.priv, []byte(msg))
	r.Header.Set(SignatureHeader, base64.StdEncoding.EncodeToString(sig))
}

// assertDegradesToDeclared calls v.Verify(req) and fails the test unless
// it returns TierDeclared, naming reason in the failure message.
func assertDegradesToDeclared(t *testing.T, v *SignedVerifier, req *http.Request, reason string) {
	t.Helper()
	id := v.Verify(req)
	if id.Tier != TierDeclared {
		t.Fatalf("Tier = %v, want %v (%s)", id.Tier, TierDeclared, reason)
	}
}

func TestSignedVerifierValidSignature(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := newTestCardServer(t, "https://bot.example/", "key-1", pub)

	v := newTestSignedVerifier(newTestCardFetcher(srv.Client(), time.Minute))
	req := httptest.NewRequest(http.MethodGet, "/some/path", nil)
	signRequest(req, signParams{priv, srv.URL + "/card.json", "key-1", time.Now().Unix(), 0, ""})

	id := v.Verify(req)
	if id.Tier != TierVerified {
		t.Fatalf("Tier = %v, want %v", id.Tier, TierVerified)
	}
	if id.AgentID != "https://bot.example/" {
		t.Errorf("AgentID = %q, want the card's client_name", id.AgentID)
	}
}

// TestSignedVerifierThrottlesRepeatedVerificationAttempts is the
// regression test for the flood-eviction finding: TierVerified requires
// no registration, so a single source presenting the same signed
// identity with a fresh nonce on every request could otherwise insert
// distinct nonces fast enough to evict a legitimately seen one from the
// bounded cache before its real TTL. preVerify must cap attempts per
// source: once the cap trips, Verify degrades to Declared even for an
// otherwise cryptographically valid signature, instead of continuing to
// spend a nonce-cache slot on every request forever.
func TestSignedVerifierThrottlesRepeatedVerificationAttempts(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := newTestCardServer(t, "https://bot.example/", "key-1", pub)
	v := NewSignedVerifier(newTestCardFetcher(srv.Client(), time.Minute))

	var sawThrottled bool
	for i := 0; i < maxPreVerifyAttemptsPerMinute+5; i++ {
		req := httptest.NewRequest(http.MethodGet, "/some/path", nil)
		signRequest(req, signParams{priv, srv.URL + "/card.json", "key-1", time.Now().Unix(), 0, ""})
		if v.Verify(req).Tier == TierDeclared {
			sawThrottled = true
			break
		}
	}
	if !sawThrottled {
		t.Errorf("expected at least one Verify call to degrade to %v within %d attempts (the pre-verify cap), got %v every time",
			TierDeclared, maxPreVerifyAttemptsPerMinute+5, TierVerified)
	}
}

// TestSignedVerifierScopeCoversAnyPathOnSameHost documents the deliberate
// trade this package makes for wire compatibility: a signature covers
// @authority (and, if present, signature-agent) but not @method or @path
// — see the host-only-scope note on signed.go's header constants. A
// signature made for one path must therefore verify unmodified against a
// different path on the same host. This is checked with a single Verify
// call against a freshly-built request carrying the same headers, not by
// calling Verify twice on the original request — a second call with the
// same nonce would correctly be rejected as a replay, which would prove
// the wrong thing here.
func TestSignedVerifierScopeCoversAnyPathOnSameHost(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	srv := newTestCardServer(t, "https://bot.example/", "key-1", pub)

	v := newTestSignedVerifier(newTestCardFetcher(srv.Client(), time.Minute))
	signedFor := httptest.NewRequest(http.MethodGet, "/a", nil)
	signRequest(signedFor, signParams{priv, srv.URL + "/card.json", "key-1", time.Now().Unix(), 0, ""})

	replayedOnDifferentPath := httptest.NewRequest(http.MethodPost, "/b", nil)
	replayedOnDifferentPath.Host = signedFor.Host
	replayedOnDifferentPath.Header = signedFor.Header.Clone()

	id := v.Verify(replayedOnDifferentPath)
	if id.Tier != TierVerified {
		t.Fatalf("Tier = %v, want %v — a WebBotAuth signature is host-scoped, not path/method-scoped", id.Tier, TierVerified)
	}
}

func TestSignedVerifierNoHeaders(t *testing.T) {
	v := newTestSignedVerifier(NewCardFetcher(time.Minute, DefaultMaxCardCacheEntries))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if id := v.Verify(req); id.Tier != TierUnverified {
		t.Fatalf("Tier = %v, want %v", id.Tier, TierUnverified)
	}
}

func TestSignedVerifierAgentHeaderOnlyDegradesToDeclared(t *testing.T) {
	v := newTestSignedVerifier(NewCardFetcher(time.Minute, DefaultMaxCardCacheEntries))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(SignatureAgentHeader, "https://bot.example/card.json")
	if id := v.Verify(req); id.Tier != TierDeclared {
		t.Fatalf("Tier = %v, want %v", id.Tier, TierDeclared)
	}
}

func TestSignedVerifierTamperedAuthorityDegradesToDeclared(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	srv := newTestCardServer(t, "https://bot.example/", "key-1", pub)

	v := newTestSignedVerifier(newTestCardFetcher(srv.Client(), time.Minute))
	req := httptest.NewRequest(http.MethodGet, "/a", nil)
	signRequest(req, signParams{priv, srv.URL + "/card.json", "key-1", time.Now().Unix(), 0, ""})
	// Tamper: change the host after signing. @authority is covered (path
	// and method deliberately are not — see
	// TestSignedVerifierScopeCoversAnyPathOnSameHost), so this must still
	// invalidate the signature.
	req.Host = "evil.example"

	assertDegradesToDeclared(t, v, req, "a signature for one authority must not verify against another")
}

func TestSignedVerifierExpiredSignatureDegradesToDeclared(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	srv := newTestCardServer(t, "https://bot.example/", "key-1", pub)

	v := newTestSignedVerifier(NewCardFetcher(time.Minute, DefaultMaxCardCacheEntries))
	v.MaxSkew = time.Minute
	req := httptest.NewRequest(http.MethodGet, "/a", nil)
	signRequest(req, signParams{priv, srv.URL + "/card.json", "key-1", time.Now().Add(-time.Hour).Unix(), 0, ""})

	assertDegradesToDeclared(t, v, req, "stale created timestamp should not verify")
}

// TestSignedVerifierExpiredButFreshCreatedDegradesToDeclared isolates the
// expires check from the created/MaxSkew check: created is fresh (so
// MaxSkew alone would accept this), but expires has already passed.
// expires is a mandatory, independently-enforced parameter — a signer
// that asks for a signature good for only a few seconds must be able to
// have that honored even when the request arrives a little late.
func TestSignedVerifierExpiredButFreshCreatedDegradesToDeclared(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	srv := newTestCardServer(t, "https://bot.example/", "key-1", pub)

	v := newTestSignedVerifier(newTestCardFetcher(srv.Client(), time.Minute))
	req := httptest.NewRequest(http.MethodGet, "/a", nil)
	now := time.Now().Unix()
	signRequest(req, signParams{priv, srv.URL + "/card.json", "key-1", now - 30, now - 1, ""})

	assertDegradesToDeclared(t, v, req, "expires already in the past should not verify even with a fresh created")
}

// TestSignedVerifierExcessiveValidityWindowDegradesToDeclared checks
// MaxValidity: a signer asking for a validity window longer than this
// server allows must be rejected even though expires itself is still in
// the future — otherwise a signer could hand out a signature valid for
// years and defeat the point of requiring expires at all.
func TestSignedVerifierExcessiveValidityWindowDegradesToDeclared(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	srv := newTestCardServer(t, "https://bot.example/", "key-1", pub)

	v := newTestSignedVerifier(newTestCardFetcher(srv.Client(), time.Minute))
	v.MaxValidity = time.Minute
	req := httptest.NewRequest(http.MethodGet, "/a", nil)
	now := time.Now().Unix()
	signRequest(req, signParams{priv, srv.URL + "/card.json", "key-1", now, now + int64(time.Hour.Seconds()), ""})

	assertDegradesToDeclared(t, v, req, "a validity window far beyond MaxValidity should not verify")
}

func TestSignedVerifierReplayedNonceDegradesToDeclared(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	srv := newTestCardServer(t, "https://bot.example/", "key-1", pub)

	v := newTestSignedVerifier(newTestCardFetcher(srv.Client(), time.Minute))
	req1 := httptest.NewRequest(http.MethodGet, "/a", nil)
	signRequest(req1, signParams{priv, srv.URL + "/card.json", "key-1", time.Now().Unix(), 0, "reused-nonce"})
	if id := v.Verify(req1); id.Tier != TierVerified {
		t.Fatalf("first use: Tier = %v, want %v", id.Tier, TierVerified)
	}

	req2 := httptest.NewRequest(http.MethodGet, "/a", nil)
	req2.Header = req1.Header.Clone()
	assertDegradesToDeclared(t, v, req2, "a reused nonce is a replay and must not verify a second time")
}

func TestSignedVerifierUnknownKeyIDDegradesToDeclared(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	srv := newTestCardServer(t, "https://bot.example/", "key-1", pub)

	v := newTestSignedVerifier(newTestCardFetcher(srv.Client(), time.Minute))
	req := httptest.NewRequest(http.MethodGet, "/a", nil)
	signRequest(req, signParams{priv, srv.URL + "/card.json", "wrong-key", time.Now().Unix(), 0, ""})

	assertDegradesToDeclared(t, v, req, "unknown keyid should not verify")
}

// TestSignedVerifierUnreachableCardDegradesToDeclared confirms Verify()
// degrades gracefully when Fetch() fails for any reason — here, the
// SSRF-safe dialer refusing loopback (the URL is otherwise well-formed
// https, so that's the only thing standing in the way; see
// TestCardFetcherRefusesPrivateAddresses for that mechanism in
// isolation).
func TestSignedVerifierUnreachableCardDegradesToDeclared(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	v := newTestSignedVerifier(NewCardFetcher(time.Minute, DefaultMaxCardCacheEntries))
	req := httptest.NewRequest(http.MethodGet, "/a", nil)
	signRequest(req, signParams{priv, "https://127.0.0.1:1/card.json", "key-1", time.Now().Unix(), 0, ""})

	assertDegradesToDeclared(t, v, req, "unreachable card should not verify")
}

func TestCardFetcherCaches(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(nil)
	srv := newTestCardServer(t, "https://bot.example/", "key-1", pub)

	f := newTestCardFetcher(srv.Client(), time.Minute)
	url := srv.URL + "/card.json"
	if _, err := f.Fetch(url); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Fetch(url); err != nil {
		t.Fatal(err)
	}
	if srv.hits != 1 {
		t.Errorf("origin hit %d times, want 1 (second fetch should be cached)", srv.hits)
	}
}

// TestCardFetcherRefusesPrivateAddresses is the regression test for the
// SSRF fix: the Signature-Agent URL is unauthenticated attacker input, so
// the production CardFetcher must refuse to fetch it when it resolves to
// loopback/private/link-local — even though, unlike
// TestSignedVerifierUnreachableCardDegradesToDeclared, something is
// genuinely listening and would happily answer if dialed.
func TestCardFetcherRefusesPrivateAddresses(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(nil)
	srv := newTestCardServer(t, "https://bot.example/", "key-1", pub)

	f := NewCardFetcher(time.Minute, DefaultMaxCardCacheEntries) // the real, safe constructor
	if _, err := f.Fetch(srv.URL + "/card.json"); err == nil {
		t.Fatal("expected the safe dialer to refuse a loopback address, got no error")
	}
}

// TestCardFetcherRejectsPlainHTTP is the regression test for the
// scheme-enforcement fix: a card fetched over plain http can't prove the
// key it hands back belongs to whoever's signing, since anyone on the
// network path could substitute their own. The rejection has to happen
// before any network call — proven here by pointing at a server that
// would otherwise happily answer (it's not the SSRF filter or a
// connection failure doing the rejecting).
func TestCardFetcherRejectsPlainHTTP(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(nil)
	mux := http.NewServeMux()
	mux.HandleFunc("/card.json", func(w http.ResponseWriter, r *http.Request) {
		t.Error("Fetch should have rejected the URL before ever reaching the network")
		_ = json.NewEncoder(w).Encode(Card{
			ClientName: "https://bot.example/",
			Keys:       []JWK{{Kty: "OKP", Crv: "Ed25519", Kid: "key-1", X: base64.RawURLEncoding.EncodeToString(pub)}},
		})
	})
	srv := httptest.NewServer(mux) // plain HTTP, deliberately
	t.Cleanup(srv.Close)

	f := NewCardFetcher(time.Minute, DefaultMaxCardCacheEntries)
	_, err := f.Fetch(srv.URL + "/card.json")
	if err == nil {
		t.Fatal("expected an error for a plain-http card URL, got none")
	}
}

// TestCardFetcherRejectsOversizedBody is the regression test for the
// body-size cap: a card server that returns more than
// maxCardResponseBytes must be rejected with an error that names the
// size limit specifically — not just any error, which could just as
// easily mean the oversized body happened to get truncated mid-token
// and fail to parse for an unrelated reason. The body doesn't need to be
// valid Card JSON: GetBounded's size check fires before Fetch ever
// attempts to unmarshal it.
func TestCardFetcherRejectsOversizedBody(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/card.json", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"client_name":"` + strings.Repeat("a", maxCardResponseBytes+1) + `"}`))
	})
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)

	f := newTestCardFetcher(srv.Client(), time.Minute)
	_, err := f.Fetch(srv.URL + "/card.json")
	if err == nil {
		t.Fatal("expected an error for an oversized card response, got none")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Errorf("error = %q, want it to mention the size limit", err.Error())
	}
}

// TestCardFetcherAcceptsBodyAtSizeLimit is the boundary check for the
// same fix, from the other side: a body of exactly maxCardResponseBytes
// must still be accepted. Written as ">" rather than ">=" deliberately —
// this test exists to catch that specific off-by-one if it ever
// regresses.
func TestCardFetcherAcceptsBodyAtSizeLimit(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(nil)
	keys := []JWK{{Kty: "OKP", Crv: "Ed25519", Kid: "key-1", X: base64.RawURLEncoding.EncodeToString(pub)}}
	// ClientName has `json:",omitempty"`, so marshaling it empty (as a
	// zero-length pad would) drops the field entirely rather than
	// emitting `"client_name":"",` — measure the field's own wrapper
	// overhead with a 1-byte placeholder instead of assuming it away.
	withPlaceholder, err := json.Marshal(Card{ClientName: "x", Keys: keys})
	if err != nil {
		t.Fatal(err)
	}
	pad := maxCardResponseBytes - (len(withPlaceholder) - 1)
	if pad < 0 {
		t.Fatal("test setup: base card already exceeds the limit")
	}
	body, err := json.Marshal(Card{ClientName: strings.Repeat("a", pad), Keys: keys})
	if err != nil {
		t.Fatal(err)
	}
	if len(body) != maxCardResponseBytes {
		t.Fatalf("test setup: encoded body is %d bytes, want exactly %d", len(body), maxCardResponseBytes)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/card.json", func(w http.ResponseWriter, r *http.Request) {
		w.Write(body)
	})
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)

	f := newTestCardFetcher(srv.Client(), time.Minute)
	if _, err := f.Fetch(srv.URL + "/card.json"); err != nil {
		t.Fatalf("expected a body exactly at the limit to be accepted, got error: %v", err)
	}
}

func TestValidateCardURLRequiresHTTPS(t *testing.T) {
	cases := []struct {
		url     string
		wantErr bool
	}{
		{"https://bot.example/card.json", false},
		{"HTTPS://bot.example/card.json", false}, // scheme is case-insensitive
		{"http://bot.example/card.json", true},
		{"ftp://bot.example/card.json", true},
		{"https://", true},  // no host
		{"not a url", true}, // no scheme at all
		{"", true},
	}
	for _, c := range cases {
		err := validateCardURL(c.url)
		if (err != nil) != c.wantErr {
			t.Errorf("validateCardURL(%q) error = %v, wantErr %v", c.url, err, c.wantErr)
		}
	}
}

func TestNewCardFetcherConfiguresCache(t *testing.T) {
	f := NewCardFetcher(42*time.Second, 7)
	if f.cache.ttl != 42*time.Second {
		t.Errorf("cache ttl = %v, want 42s", f.cache.ttl)
	}
	if f.cache.maxEntries != 7 {
		t.Errorf("cache maxEntries = %d, want 7", f.cache.maxEntries)
	}
}

func TestUnquoteSignatureAgent(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		wantURL string
		wantOK  bool
	}{
		{"empty", "", "", false},
		{"bare unquoted URL", "https://bot.example/card.json", "https://bot.example/card.json", true},
		{"bare unquoted URL with query string", "https://bot.example/card.json?v=2", "https://bot.example/card.json?v=2", true},
		{"legacy quoted sf-string", `"https://bot.example/card.json"`, "https://bot.example/card.json", true},
		{"labeled sf-string", `sig3="https://bot.example/card.json"`, "https://bot.example/card.json", true},
		{"labeled sf-string, URL with query string", `sig3="https://bot.example/card.json?v=2"`, "https://bot.example/card.json?v=2", true},
		{"unterminated quote is passed through, not stripped", `"https://bot.example/card.json`, `"https://bot.example/card.json`, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotURL, gotOK := unquoteSignatureAgent(c.in)
			if gotURL != c.wantURL || gotOK != c.wantOK {
				t.Errorf("unquoteSignatureAgent(%q) = (%q, %v), want (%q, %v)", c.in, gotURL, gotOK, c.wantURL, c.wantOK)
			}
		})
	}
}

func TestParseSignatureInput(t *testing.T) {
	cases := []struct {
		name   string
		in     string
		wantOK bool
	}{
		{
			name:   "authority only",
			in:     `sig1=("@authority");created=100;expires=200;keyid="k";alg="ed25519";nonce="n";tag="web-bot-auth"`,
			wantOK: true,
		},
		{
			name:   "authority and unlabeled signature-agent (legacy)",
			in:     `sig1=("@authority" "signature-agent");created=100;expires=200;keyid="k";alg="ed25519";nonce="n";tag="web-bot-auth"`,
			wantOK: true,
		},
		{
			name:   "authority and labeled signature-agent",
			in:     `sig1=("@authority" "signature-agent";key="sig1");created=100;expires=200;keyid="k";alg="ed25519";nonce="n";tag="web-bot-auth"`,
			wantOK: true,
		},
		{name: "no label prefix", in: `("@authority");created=100;expires=200;keyid="k";alg="ed25519";nonce="n";tag="web-bot-auth"`, wantOK: false},
		{name: "target-uri not supported", in: `sig1=("@target-uri");created=100;expires=200;keyid="k";alg="ed25519";nonce="n";tag="web-bot-auth"`, wantOK: false},
		{name: "wrong component order", in: `sig1=("signature-agent" "@authority");created=100;expires=200;keyid="k";alg="ed25519";nonce="n";tag="web-bot-auth"`, wantOK: false},
		{name: "unrecognized extra component", in: `sig1=("@authority" "content-digest");created=100;expires=200;keyid="k";alg="ed25519";nonce="n";tag="web-bot-auth"`, wantOK: false},
		{name: "missing expires", in: `sig1=("@authority");created=100;keyid="k";alg="ed25519";nonce="n";tag="web-bot-auth"`, wantOK: false},
		{name: "missing nonce", in: `sig1=("@authority");created=100;expires=200;keyid="k";alg="ed25519";tag="web-bot-auth"`, wantOK: false},
		{name: "wrong algorithm", in: `sig1=("@authority");created=100;expires=200;keyid="k";alg="rsa-pss-sha512";nonce="n";tag="web-bot-auth"`, wantOK: false},
		{name: "wrong tag", in: `sig1=("@authority");created=100;expires=200;keyid="k";alg="ed25519";nonce="n";tag="other"`, wantOK: false},
		{name: "not a component list at all", in: `sig1=garbage`, wantOK: false},
		{name: "empty", in: "", wantOK: false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, ok := parseSignatureInput(c.in)
			if ok != c.wantOK {
				t.Errorf("parseSignatureInput(%q) ok = %v, want %v", c.in, ok, c.wantOK)
			}
		})
	}
}

// TestSignatureConformsToWebBotAuthDraftExamples is the interop proof:
// each case is lifted verbatim from
// draft-meunier-web-bot-auth-architecture-05 Appendix A.2 (unwrapped from
// its RFC 8792 line-folding), using the Ed25519 test key published in RFC
// 9421 Appendix B.1.4 — the same key the draft's own reference
// implementation notes cite. This checks that signatureBase(), fed the
// draft's own header values, reconstructs the exact byte sequence that
// makes the draft's own published Signature bytes verify — proof against
// an external, independently authored source, not just that this
// package's signer and verifier agree with each other.
//
// This deliberately calls parseSignatureInput/signatureBase/ed25519.Verify
// directly rather than routing through SignedVerifier.Verify: every
// example's created/expires timestamps are fixed at authoring time (one
// is already years in the past relative to when this test runs), so this
// package's own freshness policy (MaxSkew/MaxValidity/expires-in-the-past)
// would reject all of them regardless of whether the underlying signature
// math is correct — that policy is this project's own choice, not part of
// the wire format being conformance-tested here.
func TestSignatureConformsToWebBotAuthDraftExamples(t *testing.T) {
	// RFC 9421 Appendix B.1.4's published Ed25519 test key, JWK form.
	pubKeyX := "JrQLj5P_89iXES9-vFgrIy29clF9CC_oPPsw3c5D0bs"
	pub, err := base64.RawURLEncoding.DecodeString(pubKeyX)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		t.Fatalf("test setup: bad public key fixture: %v", err)
	}

	cases := []struct {
		name            string
		host            string
		signatureAgent  string // "" if the request has no Signature-Agent header
		signatureInput  string
		signatureBase64 string // the ":..." wire form, sans the label= prefix and colons
	}{
		{
			name:            "A.2.1 Signature-Agent absent",
			host:            "example.com",
			signatureInput:  `sig1=("@authority");created=1735689600;keyid="poqkLGiymh_W0uP6PZFw-dvez3QJT5SolqXBCW38r0U";alg="ed25519";expires=4889289600;nonce="g0iqFa9e1ffijlyOScDkXpfSmTbYpRNSGPJrQ1It20ahwgzB3jOUcdgLgFxUg7RMtW4V8IILaKKtA+YuSyIgJQ==";tag="web-bot-auth"`,
			signatureBase64: "FFASViSdcgsyaqqYiCnkHreeZzbNKcTzDvZC5uVlP/dn9IbWj8j0o4wKFTH3rBnUiSUBduwm1Gp5VlIPCp01Ag==",
		},
		{
			// The IETF draft's own prose Appendix A.2.2 pairs this
			// scenario's created/expires/keyid with a nonce and Signature
			// that don't actually verify against each other (a real
			// erratum: they verify fine individually for A.2.1 and A.2.3
			// below, both transcribed from the same document the same
			// way, so this isn't a transcription mistake on this side).
			// Using the equivalent vector from Cloudflare's own maintained
			// conformance suite instead — a stronger source than draft
			// prose regardless, since it's the suite their web-bot-auth
			// package is actually tested against.
			name:            "A.2.2 Signature-Agent present, labeled (Cloudflare web-bot-auth conformance suite)",
			host:            "example.com",
			signatureAgent:  `agent2="https://signature-agent.test"`,
			signatureInput:  `sig2=("@authority" "signature-agent";key="agent2");created=1735689600;keyid="poqkLGiymh_W0uP6PZFw-dvez3QJT5SolqXBCW38r0U";alg="ed25519";expires=4889289600;nonce="n9p433xm+NJ3ph3upfBIGmsuwHw387YV7Q/F+6BSpGCVjYCqQw6rznNA8PVVLySrAWsv0hQtFioQb6E1YsauiA==";tag="web-bot-auth"`,
			signatureBase64: "RdNFx5Bj6au3YgAMQL/RzmUlZE8QZLIaXGRpw985hWnwPfMxT228NMk6ehRS1PSl4e8PhbNZACSanGdhEwYCCg==",
		},
		{
			name:            "A.2.3 legacy unlabeled Signature-Agent",
			host:            "example.com",
			signatureAgent:  `"https://signature-agent.test"`,
			signatureInput:  `sig2=("@authority" "signature-agent");created=1735689600;keyid="poqkLGiymh_W0uP6PZFw-dvez3QJT5SolqXBCW38r0U";alg="ed25519";expires=1735693200;nonce="e8N7S2MFd/qrd6T2R3tdfAuuANngKI7LFtKYI/vowzk4lAZYadIX6wW25MwG7DCT9RUKAJ0qVkU0mEeLElW1qg==";tag="web-bot-auth"`,
			signatureBase64: "jdq0SqOwHdyHr9+r5jw3iYZH6aNGKijYp/EstF4RQTQdi5N5YYKrD+mCT1HA1nZDsi6nJKuHxUi/5Syp3rLWBA==",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/test-request", nil)
			req.Host = c.host

			var agentURL string
			if c.signatureAgent != "" {
				var ok bool
				agentURL, ok = unquoteSignatureAgent(c.signatureAgent)
				if !ok {
					t.Fatalf("test setup: could not unquote Signature-Agent fixture %q", c.signatureAgent)
				}
			}

			p, ok := parseSignatureInput(c.signatureInput)
			if !ok {
				t.Fatalf("parseSignatureInput(%q) failed to parse a draft-published example", c.signatureInput)
			}

			base := signatureBase(req, agentURL, p)
			sigBytes, err := base64.StdEncoding.DecodeString(c.signatureBase64)
			if err != nil {
				t.Fatalf("test setup: bad signature fixture: %v", err)
			}

			if !ed25519.Verify(pub, []byte(base), sigBytes) {
				t.Errorf("signature base built by this package did not verify against the draft's own published signature\ngot base:\n%s", base)
			}
		})
	}
}
