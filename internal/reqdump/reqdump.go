// Package reqdump keeps the most recent failed provider request in memory and
// writes it out — the raw request and the raw response — so another agent (or
// a human) can diagnose the failure from the exact bytes that crossed the
// wire. It backs the /dump command and the automatic dump taken when a
// provider request fails at connect time.
//
// Only the connect-time failures are captured: the transport has both the
// request and the response there. A failure after streaming has begun (read
// error, idle timeout, decode mismatch) has no reconstructible raw response;
// its message still rides the stream untouched, but no dump is written for it.
//
// The dump is a diagnostic artifact, never part of the request path: every
// entry point is best-effort and swallows its own errors, because failing to
// write a dump must never mask the provider error the caller is reporting.
// Credentials never reach the dump: Authorization/api-key/cookie headers and
// sensitive query parameters are redacted (see RedactHeaders/RedactURL).
package reqdump

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// DirEnv overrides the dump root directory ($PIGO_DUMP_DIR).
const DirEnv = "PIGO_DUMP_DIR"

// maxBodyBytes bounds each captured body so a huge prompt or error page cannot
// turn a diagnostic into an unbounded write. The overflow flag records that the
// text is a prefix.
const maxBodyBytes = 256 << 10

// ErrNoRecord reports that no failed request has been recorded yet.
var ErrNoRecord = errors.New("no failed request recorded yet")

// Request is the raw request captured at failure time.
type Request struct {
	Method  string              `json:"method"`
	URL     string              `json:"url"`
	Headers map[string][]string `json:"headers,omitempty"`
	Body    string              `json:"body,omitempty"`
	// BodyTruncated marks Body as a prefix of the original request body.
	BodyTruncated bool `json:"bodyTruncated,omitempty"`
}

// Response is the raw response captured at failure time. Nil on the record when
// the request never got a response (connection refused, DNS, timeout).
type Response struct {
	Status  int                 `json:"status"`
	Headers map[string][]string `json:"headers,omitempty"`
	Body    string              `json:"body,omitempty"`
	// BodyTruncated marks Body as a prefix of the original response body.
	BodyTruncated bool `json:"bodyTruncated,omitempty"`
}

// Record is one captured provider failure: the request, the response (when
// there was one) and the error the transport reported.
type Record struct {
	RecordedAt time.Time `json:"recordedAt"`
	// SessionID names the session the failure is attributed to (the front-end
	// session in flight when it happened); empty when unknown.
	SessionID string    `json:"sessionId,omitempty"`
	Error     string    `json:"error"`
	Request   Request   `json:"request"`
	Response  *Response `json:"response,omitempty"`
}

var (
	mu       sync.Mutex
	last     *Record
	lastPath string
	session  string
)

// SetSession records the session id in flight, used to name an automatic dump
// (and stamped into the next record for attribution). The run loop sets it per
// run; an empty id disables automatic dumps.
func SetSession(id string) {
	mu.Lock()
	session = id
	mu.Unlock()
}

// Session returns the session id currently in flight ("" when unset).
func Session() string {
	mu.Lock()
	defer mu.Unlock()
	return session
}

// RecordFailure stores rec as the most recent failed request and, when a
// session is in flight, writes it to the dump directory immediately — the
// point of an automatic dump is that a later process (another agent, a script)
// finds it without having to ask this one. Best-effort: a write failure is
// swallowed rather than surfacing as a second error.
func RecordFailure(rec Record) {
	mu.Lock()
	rec.SessionID = session
	last = &rec
	lastPath = ""
	sid := session
	mu.Unlock()

	if sid == "" {
		return
	}
	root, err := DefaultDir()
	if err != nil {
		return
	}
	path, err := Write(root, sid, time.Now())
	if err != nil {
		return
	}
	mu.Lock()
	lastPath = path
	mu.Unlock()
}

// Last returns the most recent record, or ok=false when nothing failed yet.
func Last() (Record, bool) {
	mu.Lock()
	defer mu.Unlock()
	if last == nil {
		return Record{}, false
	}
	return *last, true
}

// Clear drops the recorded failure (tests).
func Clear() {
	mu.Lock()
	last = nil
	lastPath = ""
	mu.Unlock()
}

// Write writes the most recent record to root/<sessionID>-<timestamp>/dump.json
// and returns the file path. A record the automatic dump already wrote under the
// same root is rewritten in place — the dump describes one failure, so an
// on-demand re-read must not scatter copies of it. It reports ErrNoRecord when
// nothing has failed yet.
func Write(root, sessionID string, now time.Time) (string, error) {
	rec, ok := Last()
	if !ok {
		return "", ErrNoRecord
	}
	mu.Lock()
	existing := lastPath
	mu.Unlock()

	path := ""
	if existing != "" && underRoot(root, existing) {
		path = existing
	} else {
		dir := filepath.Join(root, dumpDirName(sessionID, now))
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", fmt.Errorf("dump: create %s: %w", dir, err)
		}
		path = filepath.Join(dir, "dump.json")
	}
	payload, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return "", fmt.Errorf("dump: encode: %w", err)
	}
	if err := os.WriteFile(path, append(payload, '\n'), 0o600); err != nil {
		return "", fmt.Errorf("dump: write %s: %w", path, err)
	}
	return path, nil
}

// Latest returns the newest dump.json on disk for sessionID (any session when
// sessionID is empty), or "" when the root holds none. It lets a front-end in a
// fresh process point at the dump an earlier process wrote for the same session.
func Latest(root, sessionID string) (string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("dump: read %s: %w", root, err)
	}
	prefix := sanitizeSegment(sessionID)
	newest := ""
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if prefix != "" && !strings.HasPrefix(e.Name(), prefix+"-") {
			continue
		}
		if e.Name() > newest {
			newest = e.Name()
		}
	}
	if newest == "" {
		return "", nil
	}
	path := filepath.Join(root, newest, "dump.json")
	if _, err := os.Stat(path); err != nil {
		return "", nil
	}
	return path, nil
}

// underRoot reports whether path sits under root (so a rewrite never escapes the
// configured dump directory when the caller changed PIGO_DUMP_DIR).
func underRoot(root, path string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// DefaultDir resolves the dump root: $PIGO_DUMP_DIR when set, else
// $PIGO_HOME/dumps, else ~/.pigo/dumps (the same home the other pigo state
// uses). It errors only when neither env var is set and the user home directory
// is unresolvable.
func DefaultDir() (string, error) {
	if dir := strings.TrimSpace(os.Getenv(DirEnv)); dir != "" {
		return dir, nil
	}
	if dir := strings.TrimSpace(os.Getenv("PIGO_HOME")); dir != "" {
		return filepath.Join(dir, "dumps"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("dump: no dump directory (set %s): %w", DirEnv, err)
	}
	return filepath.Join(home, ".pigo", "dumps"), nil
}

// dumpDirName builds the "<session id>-<timestamp>" directory name (the
// session id is sanitized so a hostile/odd id cannot escape the root).
func dumpDirName(sessionID string, now time.Time) string {
	id := sanitizeSegment(sessionID)
	if id == "" {
		id = "unknown-session"
	}
	return id + "-" + now.UTC().Format("20060102-150405")
}

// sanitizeSegment keeps a session id to one path-safe segment.
func sanitizeSegment(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return strings.Trim(b.String(), ".")
}

// CaptureRequest snapshots req: the method, the (redacted) URL and headers, and
// the raw body, read back through GetBody so the body was not disturbed when
// the request was sent. A request built without GetBody captures no body.
func CaptureRequest(req *http.Request) Request {
	if req == nil {
		return Request{}
	}
	out := Request{Method: req.Method, URL: RedactURL(req.URL), Headers: RedactHeaders(req.Header)}
	if req.GetBody != nil {
		if body, err := req.GetBody(); err == nil {
			defer body.Close()
			if text, truncated, err := readCapped(body); err == nil {
				out.Body, out.BodyTruncated = text, truncated
			}
		}
	}
	return out
}

// CaptureResponse snapshots resp with body as the raw response body text.
func CaptureResponse(resp *http.Response, body []byte) Response {
	if resp == nil {
		return Response{}
	}
	text := string(body)
	truncated := len(body) > maxBodyBytes
	if truncated {
		text = text[:maxBodyBytes]
	}
	return Response{Status: resp.StatusCode, Headers: RedactHeaders(resp.Header), Body: text, BodyTruncated: truncated}
}

// readCapped reads r up to maxBodyBytes, reporting whether it truncated.
func readCapped(r io.Reader) (string, bool, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxBodyBytes+1))
	if err != nil {
		return "", false, err
	}
	if len(data) > maxBodyBytes {
		return string(data[:maxBodyBytes]), true, nil
	}
	return string(data), false, nil
}

// RedactHeaders copies h with credential-bearing values replaced by
// "<redacted>", so a dumped request stays safe to hand to another agent.
func RedactHeaders(h http.Header) map[string][]string {
	if len(h) == 0 {
		return nil
	}
	out := make(map[string][]string, len(h))
	for k, v := range h {
		if isSensitiveName(k) {
			out[k] = []string{"<redacted>"}
			continue
		}
		out[k] = append([]string(nil), v...)
	}
	return out
}

// RedactURL rewrites sensitive query parameters (key/token/secret/auth shapes)
// to "<redacted>"; the path and other parameters are preserved.
func RedactURL(u *url.URL) string {
	if u == nil {
		return ""
	}
	q := u.Query()
	changed := false
	for name := range q {
		if isSensitiveName(name) {
			q.Set(name, "<redacted>")
			changed = true
		}
	}
	if !changed {
		return u.String()
	}
	clone := *u
	clone.RawQuery = q.Encode()
	return clone.String()
}

// isSensitiveName reports whether a header or query-parameter name carries a
// credential. It matches by shape (key/token/secret/auth/cookie) rather than an
// exhaustive list, so a provider inventing a new credential header still gets
// redacted.
func isSensitiveName(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	for _, s := range []string{"key", "token", "secret", "auth", "cookie", "password", "credential"} {
		if strings.Contains(n, s) {
			return true
		}
	}
	return false
}
