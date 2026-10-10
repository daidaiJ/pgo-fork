package usage

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// allowLoopback swaps the egress guard for the duration of a transport test so
// the request can reach an httptest server. guardURL itself is covered
// separately by TestGuardURL, which is what keeps this swap honest.
func allowLoopback(t *testing.T) {
	t.Helper()
	saved := endpointGuard
	endpointGuard = func(*url.URL) error { return nil }
	t.Cleanup(func() { endpointGuard = saved })
}

func TestGuardURLRules(t *testing.T) {
	cases := []struct {
		raw     string
		wantErr bool
	}{
		{raw: "https://api.commandcode.ai/alpha/billing/credits"},
		{raw: "https://opencode.ai/zen/go/v1/usage"},
		{raw: "http://api.commandcode.ai/x", wantErr: true},
		{raw: "https://127.0.0.1/x", wantErr: true},
		{raw: "https://10.0.0.5/x", wantErr: true},
		{raw: "https://192.168.1.10/x", wantErr: true},
		{raw: "https://169.254.169.254/latest/meta-data", wantErr: true},
		{raw: "https://localhost/x", wantErr: true},
		{raw: "https://quota.local/x", wantErr: true},
		{raw: "https://svc.internal/x", wantErr: true},
		{raw: "https://user:pw@api.commandcode.ai/x", wantErr: true},
	}
	for _, tc := range cases {
		u, err := url.Parse(tc.raw)
		if err != nil {
			t.Fatalf("parse %q: %v", tc.raw, err)
		}
		err = guardURL(u)
		if (err != nil) != tc.wantErr {
			t.Errorf("guardURL(%q) error = %v, wantErr %v", tc.raw, err, tc.wantErr)
		}
	}
}

func TestDoSendsCredentialAndReadsJSON(t *testing.T) {
	allowLoopback(t)
	var gotAuth, gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotUA = r.Header.Get("Authorization"), r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	body, err := Do(context.Background(), Request{Endpoint: srv.URL, Key: "sk-unit", Headers: map[string]string{"User-Agent": "pigo-test"}})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if string(body) != `{"ok":true}` {
		t.Errorf("body = %q", body)
	}
	if gotAuth != "Bearer sk-unit" {
		t.Errorf("Authorization = %q, want a Bearer token", gotAuth)
	}
	if gotUA != "pigo-test" {
		t.Errorf("User-Agent = %q, want the request's override", gotUA)
	}
}

func TestDoRejectsHTMLBody(t *testing.T) {
	allowLoopback(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<!doctype html><html><body>console</body></html>"))
	}))
	defer srv.Close()
	_, err := Do(context.Background(), Request{Endpoint: srv.URL, Key: "sk-unit"})
	if err == nil || !strings.Contains(err.Error(), "not JSON") {
		t.Fatalf("Do on an HTML body = %v, want a not-JSON error", err)
	}
}

// A provider's business error envelope is surfaced, and never the key.
func TestDoSurfacesProviderErrorEnvelope(t *testing.T) {
	allowLoopback(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"success":false,"error":{"code":"UNAUTHORIZED","message":"Missing API key."}}`))
	}))
	defer srv.Close()
	_, err := Do(context.Background(), Request{Endpoint: srv.URL, Key: "sk-secret-xyz"})
	if err == nil {
		t.Fatal("Do on a 401 returned no error")
	}
	for _, want := range []string{"401", "UNAUTHORIZED"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "sk-secret-xyz") {
		t.Errorf("error leaked the credential: %q", err)
	}
}

func TestDoRejectsCrossHostRedirect(t *testing.T) {
	allowLoopback(t)
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ok":true}`))
	}))
	defer other.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusFound)
	}))
	defer redirector.Close()

	_, err := Do(context.Background(), Request{Endpoint: redirector.URL, Key: "sk-unit"})
	if err == nil || !strings.Contains(err.Error(), "redirect") {
		t.Fatalf("cross-host redirect error = %v, want a redirect refusal", err)
	}
}
