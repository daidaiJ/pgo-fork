// Tests for the connect-time failure capture (spec wiki/port/reqdump.md): a
// provider request that fails before streaming starts is handed to the dump
// recorder with its raw request and response.
package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/reqdump"
	"github.com/smallnest/pigo/internal/testenv"
)

// TestConnectFailureRecorded pins the >=400 path: status, raw error body and the
// request (with the credential header redacted) reach the recorder.
func TestConnectFailureRecorded(t *testing.T) {
	reqdump.Clear()
	reqdump.SetSession("")
	t.Cleanup(func() { reqdump.Clear(); reqdump.SetSession("") })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid api key"}`))
	}))
	defer srv.Close()

	newReq := func(ctx context.Context) (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/chat/completions", strings.NewReader(`{"model":"m"}`))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer sk-secret")
		return req, nil
	}
	if _, err := StreamRequest(context.Background(), TransportConfig{NewRequest: newReq, Decoder: &jsonDecoder{}}); err == nil {
		t.Fatal("a 401 should fail the request")
	}

	rec, ok := reqdump.Last()
	if !ok {
		t.Fatal("the failed request was not recorded")
	}
	if rec.Response == nil || rec.Response.Status != http.StatusUnauthorized {
		t.Errorf("recorded response = %+v, want status 401", rec.Response)
	}
	if !strings.Contains(rec.Response.Body, "invalid api key") {
		t.Errorf("recorded response body = %q, want the raw error body", rec.Response.Body)
	}
	if rec.Request.Method != http.MethodPost || rec.Request.Body != `{"model":"m"}` {
		t.Errorf("recorded request = %+v, want method+body captured", rec.Request)
	}
	if v := rec.Request.Headers["Authorization"]; len(v) != 1 || v[0] != "<redacted>" {
		t.Errorf("recorded Authorization = %v, want <redacted>", v)
	}
}

// TestConnectFailureAutoDumpsUnderSession pins the end-to-end automatic dump:
// with a session in flight the failure lands in <PIGO_DUMP_DIR>/<session>-<ts>/.
func TestConnectFailureAutoDumpsUnderSession(t *testing.T) {
	reqdump.Clear()
	t.Cleanup(func() { reqdump.Clear(); reqdump.SetSession("") })

	root := testenv.Dir(t)
	t.Setenv(reqdump.DirEnv, root)
	reqdump.SetSession("20261009-213005-ab12")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"unknown model"}`))
	}))
	defer srv.Close()

	if _, err := StreamRequest(context.Background(), TransportConfig{NewRequest: newReqFn(srv.URL), Decoder: &jsonDecoder{}}); err == nil {
		t.Fatal("a 400 should fail the request")
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read dump root: %v", err)
	}
	if len(entries) != 1 || !strings.HasPrefix(entries[0].Name(), "20261009-213005-ab12-") {
		t.Fatalf("dump root = %v, want one <session>-<timestamp> dir", entries)
	}
	if _, err := os.Stat(filepath.Join(root, entries[0].Name(), "dump.json")); err != nil {
		t.Errorf("dump.json missing: %v", err)
	}
}
