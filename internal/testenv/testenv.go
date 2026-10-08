// Package testenv is the repo-wide stand-in for t.TempDir() in tests.
//
// Why: on the Windows dev machine the antivirus real-time scan briefly holds
// handles on freshly created-and-closed files, so testing's t.TempDir()
// cleanup (a RemoveAll run by the testing framework after the test returns)
// intermittently fails with "The directory is not empty" and FAILs tests whose
// assertions all passed. Observed across internal/cli/tui, cli/headless,
// cli/repl, cli, webhook and compaction — roughly one run in six, per the
// session package's earlier tally. internal/session fixed this for itself
// first (internal/session/testmain_test.go); this package generalizes that
// pattern repo-wide:
//
//   - testenv.Main is the shared TestMain body: it creates one private scratch
//     root per test-binary run and removes it best-effort at process exit.
//   - testenv.Dir(t) hands out a fresh, unique directory per call under that
//     root, with no per-directory cleanup at all: a test's outcome is its
//     assertions, never the janitorial work afterwards.
//
// The root still lives under the OS temp dir (same as t.TempDir); only the
// failure semantics change. Nothing in production behavior depends on where
// these directories live. Stale roots from crashed runs are swept by age on
// the next run (see ensureRoot).
package testenv

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const (
	rootPattern = "pigo-tests"
	// sweepAge is how long an abandoned root from a previous run may linger
	// before the next run's janitor removes it. Generous: only crashed runs
	// leave roots behind (normal exits remove their own), and a live run
	// touches its root constantly, so a day-old root is guaranteed dead.
	sweepAge = 24 * time.Hour
)

var (
	rootOnce sync.Once
	root     string
	dirSeq   int64
)

// Main is the shared body for a package's TestMain:
//
//	func TestMain(m *testing.M) { os.Exit(testenv.Main(m)) }
//
// It creates the private scratch root, runs the tests, then removes the root
// best-effort — a leftover helper file must never turn a passing run into a
// failing one.
func Main(m *testing.M) int {
	ensureRoot()
	code := m.Run()
	// Best-effort: the antivirus may still hold handles on files the run just
	// closed; leave them for the next run's janitor rather than failing.
	_ = os.RemoveAll(root)
	return code
}

// Dir returns a fresh, empty directory under the scratch root, unique per call
// (t.TempDir() semantics: each call yields its own directory). It is the
// drop-in replacement for t.TempDir(); there is no per-directory cleanup to
// fail — the root is removed wholesale at process exit.
func Dir(t *testing.T) string {
	t.Helper()
	ensureRoot()
	dir := filepath.Join(root, sanitizeTestName(t.Name()),
		fmt.Sprintf("%03d", atomic.AddInt64(&dirSeq, 1)))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("testenv: create test dir: %v", err)
	}
	return dir
}

// ensureRoot lazily creates the scratch root (TestMain normally beats it to
// it) and sweeps roots abandoned by earlier crashed runs.
func ensureRoot() {
	rootOnce.Do(func() {
		sweepStaleRoots()
		r, err := os.MkdirTemp("", rootPattern)
		if err != nil {
			// No scratch root means no tests can run; say where to look.
			fmt.Fprintf(os.Stderr, "testenv: create test root: %v\n", err)
			os.Exit(1)
		}
		root = r
	})
}

// sweepStaleRoots removes pigo-tests* roots older than sweepAge. Best-effort
// and race-free against live runs: only age-old roots are touched.
func sweepStaleRoots() {
	matches, err := filepath.Glob(filepath.Join(os.TempDir(), rootPattern+"*"))
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-sweepAge)
	for _, m := range matches {
		if info, err := os.Stat(m); err == nil && info.ModTime().Before(cutoff) {
			_ = os.RemoveAll(m)
		}
	}
}

// sanitizeTestName makes a test name safe as a single path segment on Windows,
// where subtests introduce "/" and table names may contain spaces or
// punctuation.
func sanitizeTestName(name string) string {
	return strings.NewReplacer(
		"/", "_", "\\", "_", ":", "_", "*", "_", "?", "_",
		`"`, "_", "<", "_", ">", "_", "|", "_", " ", "_",
	).Replace(name)
}
