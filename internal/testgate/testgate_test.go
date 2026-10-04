package testgate

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
)

func sprintf(format string, args ...any) string { return fmt.Sprintf(format, args...) }

func contains(s, sub string) bool { return strings.Contains(s, sub) }

func isWindows() bool { return os.Getenv("OS") == "Windows_NT" || runtime.GOOS == "windows" }

// fakeTB records Skipf/Fatalf without terminating the caller, so WinSkip's
// branching can be exercised like any other function under test.
type fakeTB struct {
	testing.TB
	skipMsg string
	failMsg string
}

func (f *fakeTB) Skipf(format string, args ...any)  { f.skipMsg = sprintf(format, args...) }
func (f *fakeTB) Fatalf(format string, args ...any) { f.failMsg = sprintf(format, args...) }
func (f *fakeTB) Helper()                           {}

func TestWinSkipRegisteredFamilySkipsOnWindows(t *testing.T) {
	if !isWindows() {
		t.Skip("only meaningful on windows")
	}
	tb := &fakeTB{}
	WinSkip(tb, PathSeparator)
	if tb.skipMsg == "" {
		t.Fatal("registered family on Windows did not skip")
	}
	if !contains(tb.skipMsg, "pigo:win-skip(path-separator):") {
		t.Errorf("skip message %q lacks the unified prefix", tb.skipMsg)
	}
	if tb.failMsg != "" {
		t.Errorf("registered family must not fail: %q", tb.failMsg)
	}
}

func TestWinSkipNoopOffWindows(t *testing.T) {
	if isWindows() {
		t.Skip("only meaningful off windows")
	}
	tb := &fakeTB{}
	WinSkip(tb, PathSeparator)
	if tb.skipMsg != "" || tb.failMsg != "" {
		t.Errorf("off-Windows WinSkip must be a no-op, got skip=%q fail=%q", tb.skipMsg, tb.failMsg)
	}
}

func TestWinSkipUnregisteredFamilyFails(t *testing.T) {
	tb := &fakeTB{}
	WinSkip(tb, "typo-family")
	if tb.failMsg == "" {
		t.Fatal("unregistered family must fail loudly, not skip silently")
	}
	if !contains(tb.failMsg, "unregistered family") {
		t.Errorf("failure message %q is not actionable", tb.failMsg)
	}
}

func TestWinSkipGateEnvForceRuns(t *testing.T) {
	if !isWindows() {
		t.Skip("only meaningful on windows")
	}
	t.Setenv(GateEnvVar, "run")
	tb := &fakeTB{}
	WinSkip(tb, PathSeparator)
	if tb.skipMsg != "" {
		t.Errorf("gate escape hatch did not force-run: skipped with %q", tb.skipMsg)
	}
}
