package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHTTPAccountSource_StructuredFormat(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":"acct-1","weight":10},{"id":"acct-2","weight":3}]`))
	}))
	defer srv.Close()

	source := NewHTTPAccountSource(srv.URL)
	accounts, err := source.FetchAccounts(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if len(accounts) != 2 {
		t.Fatalf("expected 2 accounts, got %d", len(accounts))
	}
	if accounts[0].ID != "acct-1" || accounts[0].Weight != 10 {
		t.Errorf("accounts[0] = %+v, want {ID:acct-1 Weight:10}", accounts[0])
	}
	if accounts[1].ID != "acct-2" || accounts[1].Weight != 3 {
		t.Errorf("accounts[1] = %+v, want {ID:acct-2 Weight:3}", accounts[1])
	}
}

func TestHTTPAccountSource_StringArrayFallback(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`["acct-1","acct-2","acct-3"]`))
	}))
	defer srv.Close()

	source := NewHTTPAccountSource(srv.URL)
	accounts, err := source.FetchAccounts(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if len(accounts) != 3 {
		t.Fatalf("expected 3 accounts, got %d", len(accounts))
	}
	for i, acct := range accounts {
		if acct.Weight != 0 {
			t.Errorf("accounts[%d].Weight = %d, want 0 (default)", i, acct.Weight)
		}
	}
	if accounts[0].ID != "acct-1" {
		t.Errorf("accounts[0].ID = %q, want %q", accounts[0].ID, "acct-1")
	}
}

func TestHTTPAccountSource_EmptyArray(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	source := NewHTTPAccountSource(srv.URL)
	accounts, err := source.FetchAccounts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 0 {
		t.Fatalf("expected 0 accounts, got %d", len(accounts))
	}
}

func TestHTTPAccountSource_StructuredWithoutWeight(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"id":"acct-1"},{"id":"acct-2"}]`))
	}))
	defer srv.Close()

	source := NewHTTPAccountSource(srv.URL)
	accounts, err := source.FetchAccounts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 2 {
		t.Fatalf("expected 2 accounts, got %d", len(accounts))
	}
	if accounts[0].Weight != 0 {
		t.Errorf("Weight = %d, want 0 (omitted)", accounts[0].Weight)
	}
}

func TestHTTPAccountSource_NonOKStatus(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	source := NewHTTPAccountSource(srv.URL)
	_, err := source.FetchAccounts(context.Background())
	if err == nil {
		t.Fatal("expected error for non-200 status")
	}
}
