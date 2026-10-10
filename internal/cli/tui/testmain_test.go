package tui

import (
	"os"
	"testing"

	"github.com/smallnest/pigo/internal/testenv"
)

// TestMain routes this package's test scratch dirs through testenv's private
// root: t.TempDir()'s cleanup fails intermittently on Windows (antivirus holds
// handles on freshly closed files) and FAILs tests whose assertions all
// passed. testenv.Dir(t) is the drop-in replacement; root removal is
// best-effort. See internal/testenv/testenv.go.
//
// It also points $PIGO_HOME at a scratch directory for the whole package: the
// trust store, config.toml and the session store all hang off it, and the
// developer's real home leaks state into tests. Observed 2026-10-10: a real
// "always trust this directory" entry in ~/.pigo/trust.json turned
// TestApprovalModeDerivedAndCycled's initial approval label into "ask·trusted"
// on a machine where the suite had been green minutes earlier.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "pigo-tui-home")
	if err != nil {
		panic(err) // no scratch home means the package's tests are not hermetic
	}
	if err := os.Setenv("PIGO_HOME", home); err != nil {
		panic(err)
	}
	code := testenv.Main(m)
	// Best-effort, like the testenv root: a leftover helper dir must never turn
	// a passing run into a failing one.
	_ = os.RemoveAll(home)
	os.Exit(code)
}
