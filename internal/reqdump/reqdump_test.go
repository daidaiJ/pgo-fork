// Tests for the request-dump recorder (T7.7 era diagnostic, spec
// wiki/port/reqdump.md): the last failed provider request is kept in memory,
// written as <session id>-<timestamp>/dump.json, and scrubbed of credentials.
package reqdump

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/smallnest/pigo/internal/testenv"
)

// reset clears the package state around a test so cases cannot leak into each
// other (the recorder is process-global by design).
func reset(t *testing.T) {
	t.Helper()
	Clear()
	SetSession("")
	t.Cleanup(func() {
		Clear()
		SetSession("")
	})
}

func sampleRecord() Record {
	return Record{
		RecordedAt: time.Date(2026, 10, 9, 21, 30, 5, 0, time.UTC),
		Error:      "transport: upstream 401: invalid key",
		Request: Request{
			Method:  http.MethodPost,
			URL:     "https://api.example.com/v1/chat/completions",
			Headers: map[string][]string{"Content-Type": {"application/json"}},
			Body:    `{"model":"m","messages":[]}`,
		},
		Response: &Response{Status: 401, Body: `{"error":"invalid key"}`},
	}
}

func TestWriteNamesDirectoryAfterSessionAndTimestamp(t *testing.T) {
	reset(t)
	RecordFailure(sampleRecord())
	root := testenv.Dir(t)
	now := time.Date(2026, 10, 9, 21, 30, 5, 0, time.UTC)

	path, err := Write(root, "20261009-213005-ab12", now)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	wantDir := filepath.Join(root, "20261009-213005-ab12-20261009-213005")
	if filepath.Dir(path) != wantDir {
		t.Errorf("dump dir = %q, want %q", filepath.Dir(path), wantDir)
	}
	if filepath.Base(path) != "dump.json" {
		t.Errorf("dump file = %q, want dump.json", filepath.Base(path))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read dump: %v", err)
	}
	var got Record
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("dump is not valid JSON: %v", err)
	}
	if got.Error != sampleRecord().Error || got.Request.Method != http.MethodPost {
		t.Errorf("dump payload lost fields: %+v", got)
	}
	if got.Response == nil || got.Response.Status != 401 {
		t.Errorf("dump payload lost the response: %+v", got.Response)
	}
}

func TestWriteWithoutRecord(t *testing.T) {
	reset(t)
	if _, err := Write(testenv.Dir(t), "s", time.Now()); err != ErrNoRecord {
		t.Errorf("Write without a record = %v, want ErrNoRecord", err)
	}
}

// TestRecordFailureAutoDumpsWithSession pins the automatic side: with a session
// in flight, a recorded failure lands on disk immediately (a later process —
// another agent — finds it without asking).
func TestRecordFailureAutoDumpsWithSession(t *testing.T) {
	reset(t)
	root := testenv.Dir(t)
	t.Setenv(DirEnv, root)
	SetSession("20261009-213005-ab12")

	RecordFailure(sampleRecord())

	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read dump root: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("dump root holds %d entries, want 1: %v", len(entries), entries)
	}
	if !strings.HasPrefix(entries[0].Name(), "20261009-213005-ab12-") {
		t.Errorf("dump dir %q should start with the session id", entries[0].Name())
	}
}

// TestRecordFailureWithoutSessionStaysInMemory pins the guard: no session id
// means no directory name, so nothing is written (and no test run can litter a
// developer's home directory).
func TestRecordFailureWithoutSessionStaysInMemory(t *testing.T) {
	reset(t)
	root := testenv.Dir(t)
	t.Setenv(DirEnv, root)

	RecordFailure(sampleRecord())

	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read dump root: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("no session id should write nothing, got %v", entries)
	}
	if _, ok := Last(); !ok {
		t.Error("the record should still be kept in memory")
	}
}

func TestRedactHeadersScrubsCredentialShapes(t *testing.T) {
	h := http.Header{
		"Authorization": {"Bearer sk-secret"},
		"X-Api-Key":     {"sk-secret"},
		"X-Request-Id":  {"abc"},
		"Cookie":        {"session=1"},
		"Content-Type":  {"application/json"},
	}
	got := RedactHeaders(h)
	for _, name := range []string{"Authorization", "X-Api-Key", "Cookie"} {
		if v := got[name]; len(v) != 1 || v[0] != "<redacted>" {
			t.Errorf("%s = %v, want <redacted>", name, v)
		}
	}
	if v := got["X-Request-Id"]; len(v) != 1 || v[0] != "abc" {
		t.Errorf("X-Request-Id = %v, want the value preserved", v)
	}
	if v := got["Content-Type"]; len(v) != 1 || v[0] != "application/json" {
		t.Errorf("Content-Type = %v, want the value preserved", v)
	}
}

func TestRedactURLScrubsSensitiveQuery(t *testing.T) {
	u, err := url.Parse("https://api.example.com/v1?key=sk-secret&model=m&access_token=t")
	if err != nil {
		t.Fatal(err)
	}
	got := RedactURL(u)
	if strings.Contains(got, "sk-secret") || strings.Contains(got, "access_token=t") {
		t.Errorf("RedactURL left a credential: %s", got)
	}
	if !strings.Contains(got, "model=m") {
		t.Errorf("RedactURL dropped a benign parameter: %s", got)
	}
}

// TestCaptureRequestReadsBodyBackThroughGetBody pins that the captured body is
// the bytes actually sent, without disturbing the request (GetBody is set by
// http.NewRequest for a bytes/strings body).
func TestCaptureRequestReadsBodyBackThroughGetBody(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://api.example.com/v1/chat/completions",
		strings.NewReader(`{"model":"m"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer sk-secret")

	got := CaptureRequest(req)
	if got.Body != `{"model":"m"}` {
		t.Errorf("body = %q, want the request body", got.Body)
	}
	if got.Method != http.MethodPost || !strings.Contains(got.URL, "/v1/chat/completions") {
		t.Errorf("capture lost the request line: %+v", got)
	}
	if v := got.Headers["Authorization"]; len(v) != 1 || v[0] != "<redacted>" {
		t.Errorf("Authorization = %v, want <redacted>", v)
	}
	// The original request must still be sendable (its body untouched).
	if req.GetBody == nil {
		t.Fatal("GetBody should be set for a strings.Reader body")
	}
}

func TestCaptureResponseTruncatesLargeBody(t *testing.T) {
	resp := &http.Response{StatusCode: 500, Header: http.Header{"Content-Type": {"text/plain"}}}
	big := strings.Repeat("x", maxBodyBytes+10)
	got := CaptureResponse(resp, []byte(big))
	if !got.BodyTruncated || len(got.Body) != maxBodyBytes || got.Status != 500 {
		t.Errorf("large body = %d bytes (truncated=%v, status=%d), want %d bytes truncated at 500",
			len(got.Body), got.BodyTruncated, got.Status, maxBodyBytes)
	}
}

// TestWriteReusesTheAutoDumpedFile pins that an on-demand write does not
// scatter copies: a failure the automatic dump already wrote under the same
// root is rewritten in place.
func TestWriteReusesTheAutoDumpedFile(t *testing.T) {
	reset(t)
	root := testenv.Dir(t)
	t.Setenv(DirEnv, root)
	SetSession("20261009-213005-ab12")
	RecordFailure(sampleRecord())

	first, err := Latest(root, "20261009-213005-ab12")
	if err != nil || first == "" {
		t.Fatalf("Latest after auto-dump = %q, %v", first, err)
	}
	path, err := Write(root, "20261009-213005-ab12", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if path != first {
		t.Errorf("Write reused = %q, want the auto-dumped %q", path, first)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("dump root holds %d entries, want 1 (no duplicate directory)", len(entries))
	}
}

// TestLatestFiltersBySession pins the cross-process lookup: only directories for
// the session are considered, and the newest wins.
func TestLatestFiltersBySession(t *testing.T) {
	reset(t)
	root := testenv.Dir(t)
	for _, name := range []string{
		"session-a-20261009-100000",
		"session-a-20261009-120000",
		"session-b-20261009-230000",
	} {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "dump.json"), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	got, err := Latest(root, "session-a")
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	want := filepath.Join(root, "session-a-20261009-120000", "dump.json")
	if got != want {
		t.Errorf("Latest = %q, want the newest session-a dump %q", got, want)
	} else if got, err = Latest(root, ""); err != nil {
		t.Fatalf("Latest(any): %v", err)
	} else if !strings.Contains(got, "session-b-20261009-230000") {
		t.Errorf("Latest(any) = %q, want the globally newest dump", got)
	}

	if got, err := Latest(testenv.Dir(t), "session-a"); err != nil || got != "" {
		t.Errorf("Latest on an empty root = %q, %v; want empty", got, err)
	}
}

func TestDefaultDirPrefersEnvThenPigoHome(t *testing.T) {
	t.Setenv(DirEnv, "")
	t.Setenv("PIGO_HOME", "")
	t.Setenv(DirEnv, filepath.Join("C:", "dumps-here"))

	got, err := DefaultDir()
	if err != nil {
		t.Fatalf("DefaultDir: %v", err)
	}
	if got != filepath.Join("C:", "dumps-here") {
		t.Errorf("DefaultDir = %q, want the %s override", got, DirEnv)
	}

	t.Setenv(DirEnv, "")
	t.Setenv("PIGO_HOME", testenv.Dir(t))
	got, err = DefaultDir()
	if err != nil {
		t.Fatalf("DefaultDir: %v", err)
	}
	if filepath.Base(got) != "dumps" {
		t.Errorf("DefaultDir = %q, want <PIGO_HOME>/dumps", got)
	}
}
