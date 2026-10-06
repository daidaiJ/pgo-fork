package session

// TestMain routes this package's temporary directories through one private root
// instead of t.TempDir() (see testDir).
//
// Why: the session store keeps two helper files next to each session — a write
// lease and a digest sidecar. On Windows a file that was just created and closed
// is not always deletable right away, so t.TempDir()'s cleanup can fail with
// "directory is not empty" and fail the test even though every assertion passed
// (roughly one run in six on this machine). Routing through our own root keeps
// cleanup best-effort: a test's outcome should be its assertions, not the
// janitorial work afterwards.
//
// This is a Windows-environment accommodation only; nothing in the package's
// behavior depends on where the store's directory lives.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// testRoot is the package's scratch root, created by TestMain.
var testRoot string

// dirSeq disambiguates tests that share a name (subtests, table cases).
var dirSeq int64

func TestMain(m *testing.M) {
	root, err := os.MkdirTemp("", "pigo-session-tests")
	if err != nil {
		fmt.Fprintf(os.Stderr, "session: create test root: %v\n", err)
		os.Exit(1)
	}
	testRoot = root
	// Opt out of the write fsync: it is the point of the durability work but
	// costs ~40ms per call on this machine, and this package performs hundreds of
	// writes per run. Durability is covered by the fsync path being unconditional
	// in production, not by paying for it in every test.
	fsyncWrites = false
	code := m.Run()
	// Best-effort: a leftover helper file must never turn a passing run into a
	// failing one.
	_ = os.RemoveAll(root)
	os.Exit(code)
}

// testDir returns a fresh, empty directory under the package's test root. It is
// this package's stand-in for t.TempDir() (see TestMain for why).
func testDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(testRoot, sanitizeTestName(t.Name()), fmt.Sprintf("%03d", atomic.AddInt64(&dirSeq, 1)))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("session: create test dir: %v", err)
	}
	return dir
}

// sanitizeTestName makes a test name safe as a single path segment on Windows,
// where subtests introduce "/" and table names may contain spaces or punctuation.
func sanitizeTestName(name string) string {
	return strings.NewReplacer(
		"/", "_", "\\", "_", ":", "_", "*", "_", "?", "_",
		`"`, "_", "<", "_", ">", "_", "|", "_", " ", "_",
	).Replace(name)
}
