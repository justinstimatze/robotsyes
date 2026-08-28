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

func newTestCardServer(t *testing.T, agentID, keyID string, pub ed25519.PublicKey) *testCardServer {
	t.Helper()
	tcs := &testCardServer{}
	mux := http.NewServeMux()
	mux.HandleFunc("/card.json", func(w http.ResponseWriter, r *http.Request) {
		tcs.hits++
		card := Card{
			AgentID: agentID,
			Keys: []CardKey{{
				KeyID:     keyID,
				Algorithm: "ed25519",
				PublicKey: base64.StdEncoding.EncodeToString(pub),
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

// signRequest signs r for agentURL/keyID/created using priv, and sets the
// three headers a real signer would send.
func signRequest(r *http.Request, priv ed25519.PrivateKey, agentURL, keyID string, created int64) {
	r.Header.Set(SignatureAgentHeader, agentURL)
	r.Header.Set(SignatureInputHeader, fmt.Sprintf(`keyid="%s";created=%d`, keyID, created))
	msg := signatureBase(r, agentURL, created)
	sig := ed25519.Sign(priv, []byte(msg))
	r.Header.Set(SignatureHeader, base64.StdEncoding.EncodeToString(sig))
}

func TestSignedVerifierValidSignature(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := newTestCardServer(t, "https://bot.example/", "key-1", pub)

	v := NewSignedVerifier(newTestCardFetcher(srv.Client(), time.Minute))
	req := httptest.NewRequest(http.MethodGet, "/some/path", nil)
	signRequest(req, priv, srv.URL+"/card.json", "key-1", time.Now().Unix())

	id := v.Verify(req)
	if id.Tier != TierVerified {
		t.Fatalf("Tier = %v, want %v", id.Tier, TierVerified)
	}
	if id.AgentID != "https://bot.example/" {
		t.Errorf("AgentID = %q, want the card's agent_id", id.AgentID)
	}
}

func TestSignedVerifierNoHeaders(t *testing.T) {
	v := NewSignedVerifier(NewCardFetcher(time.Minute, DefaultMaxCardCacheEntries))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if id := v.Verify(req); id.Tier != TierUnverified {
		t.Fatalf("Tier = %v, want %v", id.Tier, TierUnverified)
	}
}

func TestSignedVerifierAgentHeaderOnlyDegradesToDeclared(t *testing.T) {
	v := NewSignedVerifier(NewCardFetcher(time.Minute, DefaultMaxCardCacheEntries))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(SignatureAgentHeader, "https://bot.example/card.json")
	if id := v.Verify(req); id.Tier != TierDeclared {
		t.Fatalf("Tier = %v, want %v", id.Tier, TierDeclared)
	}
}

func TestSignedVerifierTamperedSignatureDegradesToDeclared(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	srv := newTestCardServer(t, "https://bot.example/", "key-1", pub)

	v := NewSignedVerifier(newTestCardFetcher(srv.Client(), time.Minute))
	req := httptest.NewRequest(http.MethodGet, "/a", nil)
	signRequest(req, priv, srv.URL+"/card.json", "key-1", time.Now().Unix())
	// Tamper: change the path after signing, so the signed message no
	// longer matches what's actually being requested.
	req.URL.Path = "/b"

	id := v.Verify(req)
	if id.Tier != TierDeclared {
		t.Fatalf("Tier = %v, want %v (tampered request should not verify)", id.Tier, TierDeclared)
	}
}

func TestSignedVerifierExpiredSignatureDegradesToDeclared(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	srv := newTestCardServer(t, "https://bot.example/", "key-1", pub)

	v := NewSignedVerifier(NewCardFetcher(time.Minute, DefaultMaxCardCacheEntries))
	v.MaxSkew = time.Minute
	req := httptest.NewRequest(http.MethodGet, "/a", nil)
	signRequest(req, priv, srv.URL+"/card.json", "key-1", time.Now().Add(-time.Hour).Unix())

	id := v.Verify(req)
	if id.Tier != TierDeclared {
		t.Fatalf("Tier = %v, want %v (stale signature should not verify)", id.Tier, TierDeclared)
	}
}

func TestSignedVerifierUnknownKeyIDDegradesToDeclared(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	srv := newTestCardServer(t, "https://bot.example/", "key-1", pub)

	v := NewSignedVerifier(newTestCardFetcher(srv.Client(), time.Minute))
	req := httptest.NewRequest(http.MethodGet, "/a", nil)
	signRequest(req, priv, srv.URL+"/card.json", "wrong-key", time.Now().Unix())

	id := v.Verify(req)
	if id.Tier != TierDeclared {
		t.Fatalf("Tier = %v, want %v (unknown keyid should not verify)", id.Tier, TierDeclared)
	}
}

// TestSignedVerifierUnreachableCardDegradesToDeclared confirms Verify()
// degrades gracefully when Fetch() fails for any reason — here, the
// SSRF-safe dialer refusing loopback (the URL is otherwise well-formed
// https, so that's the only thing standing in the way; see
// TestCardFetcherRefusesPrivateAddresses for that mechanism in
// isolation).
func TestSignedVerifierUnreachableCardDegradesToDeclared(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	v := NewSignedVerifier(NewCardFetcher(time.Minute, DefaultMaxCardCacheEntries))
	req := httptest.NewRequest(http.MethodGet, "/a", nil)
	signRequest(req, priv, "https://127.0.0.1:1/card.json", "key-1", time.Now().Unix())

	id := v.Verify(req)
	if id.Tier != TierDeclared {
		t.Fatalf("Tier = %v, want %v (unreachable card should not verify)", id.Tier, TierDeclared)
	}
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
			AgentID: "https://bot.example/",
			Keys:    []CardKey{{KeyID: "key-1", Algorithm: "ed25519", PublicKey: base64.StdEncoding.EncodeToString(pub)}},
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
// and fail to parse for an unrelated reason.
func TestCardFetcherRejectsOversizedBody(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/card.json", func(w http.ResponseWriter, r *http.Request) {
		// Oversized but otherwise well-formed JSON, so a rejection here
		// can only be the size check doing its job.
		w.Write([]byte(`{"agent_id":"` + strings.Repeat("a", maxCardResponseBytes+1) + `"}`))
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
	base := Card{Keys: []CardKey{{KeyID: "key-1", Algorithm: "ed25519", PublicKey: base64.StdEncoding.EncodeToString(pub)}}}
	baseBytes, err := json.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	pad := maxCardResponseBytes - len(baseBytes)
	if pad < 0 {
		t.Fatal("test setup: base card already exceeds the limit")
	}
	body, err := json.Marshal(Card{AgentID: strings.Repeat("a", pad), Keys: base.Keys})
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
