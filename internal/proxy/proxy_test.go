package proxy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/justinstimatze/robotsyes/internal/config"
	"github.com/justinstimatze/robotsyes/internal/export"
	"github.com/justinstimatze/robotsyes/internal/identity"
)

func testOrigin(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html><body><nav>nav</nav><main><h1>Home</h1><p>hello</p></main></body></html>`))
	})
	mux.HandleFunc("/about", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html><body><main><h1>About</h1></main></body></html>`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func newTestServer(t *testing.T, mutate func(*config.Config)) *Server {
	t.Helper()
	origin := testOrigin(t)
	cfg := config.Default()
	cfg.Origin = origin.URL
	cfg.Export.Paths = []string{"/", "/about"}
	if mutate != nil {
		mutate(&cfg)
	}
	s, err := New(cfg, identity.DeclaredVerifier{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

func TestPassthroughServesRawHTML(t *testing.T) {
	s := newTestServer(t, nil)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept", "text/html")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "<nav>nav</nav>") {
		t.Errorf("expected raw HTML passthrough, got: %s", w.Body.String())
	}
	if got := w.Header().Get("Vary"); got != "Accept" {
		t.Errorf("Vary header = %q, want %q", got, "Accept")
	}
}

func TestMarkdownNegotiationStripsContent(t *testing.T) {
	s := newTestServer(t, nil)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept", "text/markdown")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "# Home") {
		t.Errorf("expected stripped markdown heading, got: %s", body)
	}
	if strings.Contains(body, "nav") {
		t.Errorf("expected nav chrome stripped, got: %s", body)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/markdown") {
		t.Errorf("Content-Type = %q, want text/markdown prefix", ct)
	}
}

func TestDiscoveryDocument(t *testing.T) {
	s := newTestServer(t, nil)
	req := httptest.NewRequest(http.MethodGet, discoveryPath, nil)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var doc discovery
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatalf("decoding discovery doc: %v", err)
	}
	if !doc.ContentNegotiation.Supported {
		t.Error("expected content_negotiation.supported = true")
	}
	if doc.Export.URL != exportPath {
		t.Errorf("export.url = %q, want %q", doc.Export.URL, exportPath)
	}
	if _, ok := doc.RateLimits["declared"]; !ok {
		t.Errorf("expected a declared tier in published rate limits, got %v", doc.RateLimits)
	}
}

func TestExportBundleContainsConfiguredPaths(t *testing.T) {
	s := newTestServer(t, nil)
	req := httptest.NewRequest(http.MethodGet, exportPath, nil)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var pages []export.Page
	sc := bufio.NewScanner(bytes.NewReader(w.Body.Bytes()))
	for sc.Scan() {
		var p export.Page
		if err := json.Unmarshal(sc.Bytes(), &p); err != nil {
			t.Fatalf("decoding bundle line: %v", err)
		}
		pages = append(pages, p)
	}
	if len(pages) != 2 {
		t.Fatalf("got %d pages, want 2", len(pages))
	}
	if pages[0].Path != "/" || !strings.Contains(pages[0].Markdown, "# Home") {
		t.Errorf("unexpected first page: %+v", pages[0])
	}
	if pages[1].Path != "/about" || !strings.Contains(pages[1].Markdown, "# About") {
		t.Errorf("unexpected second page: %+v", pages[1])
	}
}

func TestRateLimitReturns429(t *testing.T) {
	s := newTestServer(t, func(c *config.Config) {
		c.RateLimits = map[string]int{"declared": 1}
	})

	req := func() *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("Accept", "text/html")
		r.Header.Set(identity.SignatureAgentHeader, "test-bot")
		return r
	}

	w1 := httptest.NewRecorder()
	s.ServeHTTP(w1, req())
	if w1.Code != http.StatusOK {
		t.Fatalf("first request status = %d, want 200", w1.Code)
	}

	w2 := httptest.NewRecorder()
	s.ServeHTTP(w2, req())
	if w2.Code != http.StatusTooManyRequests {
		t.Fatalf("second request status = %d, want 429", w2.Code)
	}
	if w2.Header().Get("Retry-After") == "" {
		t.Error("expected a Retry-After header on 429")
	}
}

// TestRateLimitKeysOnIPNotPort guards against a real bug found by an
// end-to-end smoke test (not caught by TestRateLimitReturns429, which
// happens to reuse the same httptest.NewRequest RemoteAddr on every
// call): every real TCP connection carries its own ephemeral client
// port, so keying the limiter on the full RemoteAddr gave a fresh bucket
// to every new connection from the same IP and never actually limited
// anything.
func TestRateLimitKeysOnIPNotPort(t *testing.T) {
	s := newTestServer(t, func(c *config.Config) {
		c.RateLimits = map[string]int{"unverified": 1}
	})

	reqFrom := func(port string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("Accept", "text/html")
		r.RemoteAddr = "203.0.113.9:" + port
		return r
	}

	w1 := httptest.NewRecorder()
	s.ServeHTTP(w1, reqFrom("1111"))
	if w1.Code != http.StatusOK {
		t.Fatalf("first request (port 1111) status = %d, want 200", w1.Code)
	}

	w2 := httptest.NewRecorder()
	s.ServeHTTP(w2, reqFrom("2222"))
	if w2.Code != http.StatusTooManyRequests {
		t.Fatalf("second request from same IP, different port (2222) status = %d, want 429", w2.Code)
	}

	w3 := httptest.NewRecorder()
	req3 := httptest.NewRequest(http.MethodGet, "/", nil)
	req3.Header.Set("Accept", "text/html")
	req3.RemoteAddr = "198.51.100.4:1111"
	s.ServeHTTP(w3, req3)
	if w3.Code != http.StatusOK {
		t.Fatalf("request from a genuinely different IP status = %d, want 200", w3.Code)
	}
}
