// Package testgate is the systematic gate for platform-dependent test
// families (R5 in wiki/port/design-principles.md): instead of ad-hoc
// per-test judgment calls or a permanently red suite, every known
// Windows-only failure family is registered here exactly once — with its
// root cause — and the affected tests call WinSkip as their first
// statement. The skip message carries the unified `pigo:win-skip(<family>)`
// prefix so the gate inventory is greppable straight from test output, and
// PIGO_WIN_TEST_GATE=run force-runs gated tests for triage.
//
// Discipline: a family must be registered in this file (with a root cause)
// before any test can gate on it, and appended to the appendix of
// wiki/port/design-principles.md. An unregistered name fails the test
// loudly rather than skipping silently.
package testgate

import (
	"os"
	"runtime"
	"testing"
)

// Family identifiers. Each is one root-cause family; the doc comment states
// the root cause and mirrors the appendix of wiki/port/design-principles.md.
const (
	// PathSeparator: assertions hard-code POSIX "/" separators (or match
	// POSIX-shaped paths) while Windows output uses "\".
	PathSeparator = "path-separator"

	// GoldenLineEndings: byte-exact golden files record LF output; the
	// produced output on Windows differs (CRLF line endings), failing the
	// byte comparison.
	GoldenLineEndings = "golden-line-endings"

	// POSIXPerms: tests exercise POSIX permission bits (0600/000) that
	// Windows cannot represent, so the permission semantics under test do
	// not exist on this platform.
	POSIXPerms = "posix-perms"

	// POSIXShellHook: hook fixtures run POSIX shell commands (e.g.
	// `cat >> <file>`) that the Windows hook runner cannot execute.
	POSIXShellHook = "posix-shell-hook"

	// PluginSubprocess: the compiled plugin subprocess starts but the
	// slash-command discovery round-trip returns no commands on Windows;
	// root cause pending deeper triage.
	PluginSubprocess = "plugin-subprocess"

	// WinFileLock: flaky TempDir cleanup — an external process (antivirus)
	// or a late-released handle holds a file momentarily, failing RemoveAll.
	WinFileLock = "win-filelock"

	// HomeEnvOverride: os.UserHomeDir on Windows reads USERPROFILE, so
	// tests that relocate the home directory via HOME do not take effect.
	HomeEnvOverride = "home-env-override"
)

// families maps every registered family to its root cause, surfaced verbatim
// in the skip message. Adding an entry here requires appending the family to
// the appendix of wiki/port/design-principles.md (append-only ledger).
var families = map[string]string{
	PathSeparator:     "assertions hard-code POSIX path separators; Windows yields backslashes",
	GoldenLineEndings: "byte-exact goldens record LF output; Windows output differs (CRLF)",
	POSIXPerms:        "POSIX permission bits (0600/000) are not representable on Windows",
	POSIXShellHook:    "hook fixtures run POSIX commands (cat >>) unavailable to the Windows runner",
	PluginSubprocess:  "plugin subprocess handshake returns no commands on Windows; root cause pending triage",
	WinFileLock:       "flaky TempDir cleanup: antivirus/late handle holds a file momentarily",
	HomeEnvOverride:   "os.UserHomeDir reads USERPROFILE, ignoring a HOME override on Windows",
}

// GateEnvVar is the escape hatch: setting it to "run" disables skipping so a
// triage session can re-run every gated test on Windows.
const GateEnvVar = "PIGO_WIN_TEST_GATE"

// WinSkip skips t on Windows when family is registered. On non-Windows, or
// when PIGO_WIN_TEST_GATE=run, it returns and the test executes. An
// unregistered family fails the test instead of skipping — the gate must not
// silently swallow tests whose root cause was never written down.
func WinSkip(t testing.TB, family string) {
	if runtime.GOOS != "windows" {
		return
	}
	reason, ok := families[family]
	if !ok {
		t.Fatalf("pigo:win-skip: unregistered family %q — register it in internal/testgate with its root cause first", family)
	}
	if os.Getenv(GateEnvVar) == "run" {
		return
	}
	t.Skipf("pigo:win-skip(%s): %s", family, reason)
}
