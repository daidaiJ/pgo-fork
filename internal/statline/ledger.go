// This file holds the usage ledger's on-disk form (O1 / T7.3c): one
// <session id>.usage.jsonl file beside the session file, one Record per line.
//
// The ledger is a sidecar rather than a session message role on purpose. Usage
// is already carried by every persisted assistant message (agentcore.Usage), but
// a message cannot carry the timing, retry count, or sub-agent attribution the
// session-cumulative semantics need, and inserting a synthetic message into the
// live list would make the request projection responsible for stripping it. A
// sidecar keyed by the session id keeps the session schema (v3) untouched,
// survives reload/resume exactly like the session file, and is deleted with it.
package statline

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// UsageSuffix is the ledger file suffix: <session id><UsageSuffix>. A name with
// this suffix is not a session file (session.Store's listing skips it), and the
// session delete path removes it alongside the session.
const UsageSuffix = ".usage.jsonl"

// UsageFileName returns the ledger file name for a session id.
func UsageFileName(sessionID string) string { return sessionID + UsageSuffix }

// IsUsageFileName reports whether a directory entry name is a usage ledger
// rather than a session file.
func IsUsageFileName(name string) bool { return strings.HasSuffix(name, UsageSuffix) }

// UsagePath returns the ledger path for a session id under root.
func UsagePath(root, sessionID string) string {
	return filepath.Join(root, UsageFileName(sessionID))
}

// appendMu serializes appends within this process. The sub-agent goroutines of
// one run can settle concurrently, and O_APPEND alone does not make two
// concurrent write syscalls a single atomic line on every platform; the mutex
// keeps each record's bytes contiguous. Cross-process concurrent appends to one
// session's ledger are possible only when two processes share a session, which
// the session write lease already treats as unsupported.
var appendMu sync.Mutex

// AppendUsage appends records to the session's ledger as JSONL lines, creating
// the file (and root) when needed. A no-op for an empty root/session id or an
// empty record slice.
func AppendUsage(root, sessionID string, recs ...Record) error {
	if root == "" || sessionID == "" || len(recs) == 0 {
		return nil
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return fmt.Errorf("statline: create ledger dir: %w", err)
	}
	buf := make([]byte, 0, 256*len(recs))
	for _, r := range recs {
		line, err := json.Marshal(r)
		if err != nil {
			return fmt.Errorf("statline: encode usage record: %w", err)
		}
		buf = append(buf, line...)
		buf = append(buf, '\n')
	}
	appendMu.Lock()
	defer appendMu.Unlock()
	f, err := os.OpenFile(UsagePath(root, sessionID), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("statline: open ledger: %w", err)
	}
	if _, err := f.Write(buf); err != nil {
		f.Close()
		return fmt.Errorf("statline: write ledger: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("statline: close ledger: %w", err)
	}
	return nil
}

// LoadUsage reads a session's ledger records in file order. A missing ledger is
// not an error (a session that never ran has none). A torn trailing line — the
// crash-mid-append case — is dropped rather than failing the read, the same
// tolerance the session decoder applies to its own tail.
func LoadUsage(root, sessionID string) ([]Record, error) {
	if root == "" || sessionID == "" {
		return nil, nil
	}
	f, err := os.Open(UsagePath(root, sessionID))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("statline: open ledger: %w", err)
	}
	defer f.Close()

	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 16*1024), 1024*1024)
	for sc.Scan() {
		if raw := strings.TrimSpace(sc.Text()); raw != "" {
			lines = append(lines, raw)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("statline: scan ledger: %w", err)
	}
	recs := make([]Record, 0, len(lines))
	for i, raw := range lines {
		var r Record
		if err := json.Unmarshal([]byte(raw), &r); err != nil {
			// A malformed FINAL line is the torn-tail case (a crash between the
			// write's bytes and its newline) and is dropped; damage with content
			// after it is real corruption and is reported, so a bit flip never
			// silently truncates the accounting.
			if i == len(lines)-1 {
				break
			}
			return nil, fmt.Errorf("statline: parse ledger line %d: %w", i+1, err)
		}
		recs = append(recs, r)
	}
	return recs, nil
}
