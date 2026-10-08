package ui

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
func TestMain(m *testing.M) { os.Exit(testenv.Main(m)) }
