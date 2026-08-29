package httpx

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGetBoundedRejectsOversizedBody(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/big", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(strings.Repeat("a", 101)))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	_, err := GetBounded(srv.Client(), srv.URL+"/big", 100)
	if err == nil {
		t.Fatal("expected an error for an oversized body, got none")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Errorf("error = %q, want it to mention the size limit", err.Error())
	}
}

func TestGetBoundedAcceptsBodyAtLimit(t *testing.T) {
	body := strings.Repeat("a", 100)
	mux := http.NewServeMux()
	mux.HandleFunc("/exact", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(body))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	got, err := GetBounded(srv.Client(), srv.URL+"/exact", 100)
	if err != nil {
		t.Fatalf("expected a body exactly at the limit to be accepted, got error: %v", err)
	}
	if string(got) != body {
		t.Errorf("got %d bytes, want %d", len(got), len(body))
	}
}

func TestGetBoundedRejectsNonOKStatus(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/missing", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	_, err := GetBounded(srv.Client(), srv.URL+"/missing", 100)
	if err == nil {
		t.Fatal("expected an error for a 404, got none")
	}
}
