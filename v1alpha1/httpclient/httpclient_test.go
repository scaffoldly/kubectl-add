package httpclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSharedClientStampsHeaders(t *testing.T) {
	var gotUA, gotVer string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		gotVer = r.Header.Get(VersionHeader)
	}))
	defer srv.Close()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := Default().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if !strings.HasPrefix(gotUA, "kubectl-add/") {
		t.Errorf("User-Agent = %q, want kubectl-add/ prefix", gotUA)
	}
	if gotVer == "" {
		t.Errorf("%s not set", VersionHeader)
	}
}

func TestExplicitHeaderNotOverridden(t *testing.T) {
	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
	}))
	defer srv.Close()

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	req.Header.Set("User-Agent", "custom/1.0")
	resp, err := Default().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if gotUA != "custom/1.0" {
		t.Errorf("User-Agent = %q, want custom/1.0 (caller value preserved)", gotUA)
	}
}
