package prompts

import (
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/agenttool"
	"github.com/smallnest/pigo/internal/lsp"
	"github.com/smallnest/pigo/internal/runtime"
)

// TestParseLSP pins the /lsp grammar (T8.2): bare and "status" resolve to the
// listing, enable|disable take an optional server name, and anything else is
// refused with the usage line (pure parse — no live state touched).
func TestParseLSP(t *testing.T) {
	cases := []struct {
		args string
		want runtime.Intent
	}{
		{"", runtime.Intent{Kind: runtime.IntentLSPShow}},
		{"status", runtime.Intent{Kind: runtime.IntentLSPShow}},
		{"enable", runtime.Intent{Kind: runtime.IntentLSPServerToggle, LSPEnable: true}},
		{"disable gopls", runtime.Intent{Kind: runtime.IntentLSPServerToggle, LSPServer: "gopls"}},
		{"ENABLE gopls", runtime.Intent{Kind: runtime.IntentLSPServerToggle, LSPServer: "gopls", LSPEnable: true}},
	}
	for _, tc := range cases {
		got, err := parseLSP(tc.args)
		if err != nil {
			t.Errorf("parseLSP(%q): %v", tc.args, err)
			continue
		}
		if got != tc.want {
			t.Errorf("parseLSP(%q) = %+v, want %+v", tc.args, got, tc.want)
		}
	}
	for _, bad := range []string{"restart", "enable a b", "status x", "disable a b"} {
		if _, err := parseLSP(bad); err == nil {
			t.Errorf("parseLSP(%q) = nil error, want a usage refusal", bad)
		}
	}
}

// TestExecutorLSPIntents pins the executor wiring: a nil surface degrades to
// the context note; a surface with a nil LSP manager lists the disabled
// state with both enable paths (neutral, never an error); the toggle without
// a store refuses.
func TestExecutorLSPIntents(t *testing.T) {
	x := &Executor{}
	if msg := x.Execute(runtime.Intent{Kind: runtime.IntentLSPShow}).Message; msg != "lsp: config surface unavailable in this context" {
		t.Fatalf("nil surface show = %q", msg)
	}
	x = &Executor{Surface: &SurfaceDeps{}}
	msg := x.Execute(runtime.Intent{Kind: runtime.IntentLSPShow}).Message
	if !containsAll(msg, "lsp: disabled", "config.toml", "./.pigo/config.json") {
		t.Fatalf("disabled listing = %q", msg)
	}
	msg = x.Execute(runtime.Intent{Kind: runtime.IntentLSPServerToggle, LSPEnable: true}).Message
	if !containsAll(msg, "config writes unavailable") {
		t.Fatalf("toggle without store = %q", msg)
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}

// Batch 2 (T8.2 ②): the /lsp tool grammar and the per-tool face switch.

func TestParseLSPToolToggle(t *testing.T) {
	cases := []struct {
		args string
		want runtime.Intent
	}{
		{"tool enable hover", runtime.Intent{Kind: runtime.IntentLSPToolToggle, LSPTool: "hover", LSPEnable: true}},
		{"tool disable lsp_hover", runtime.Intent{Kind: runtime.IntentLSPToolToggle, LSPTool: "lsp_hover"}},
	}
	for _, tc := range cases {
		got, err := parseLSP(tc.args)
		if err != nil {
			t.Errorf("parseLSP(%q): %v", tc.args, err)
			continue
		}
		if got != tc.want {
			t.Errorf("parseLSP(%q) = %+v, want %+v", tc.args, got, tc.want)
		}
	}
	for _, bad := range []string{"tool", "tool enable", "tool frobnicate hover", "tool enable hover extra"} {
		if _, err := parseLSP(bad); err == nil {
			t.Errorf("parseLSP(%q) = nil error, want a usage refusal", bad)
		}
	}
}

func TestLSPToolToggle(t *testing.T) {
	var stored [][]string
	deps := &SurfaceDeps{
		LSPToolsList:  func() []string { return nil }, // all on
		LSPToolsStore: func(tools []string) error { stored = append(stored, tools); return nil },
	}
	msg := deps.LSPToolToggle("lsp_hover", false)
	if !containsAll(msg, "lsp_hover disabled", "next session") {
		t.Fatalf("disable msg = %q", msg)
	}
	if len(stored) != 1 {
		t.Fatalf("store calls = %d", len(stored))
	}
	if len(stored[0]) != len(agenttool.LSPToolNames)-1 {
		t.Fatalf("stored list = %v (want the family minus one)", stored[0])
	}
	for _, name := range stored[0] {
		if name == "hover" || name == "lsp_hover" {
			t.Fatalf("disabled tool still stored: %v", stored[0])
		}
	}
	msg = deps.LSPToolToggle("hover", true)
	if !containsAll(msg, "hover enabled") {
		t.Fatalf("enable msg = %q", msg)
	}
	// Refusal: disabling the last enabled tool would flip an empty filter
	// back to "all on".
	deps2 := &SurfaceDeps{
		LSPToolsList:  func() []string { return []string{"hover"} },
		LSPToolsStore: func([]string) error { return nil },
	}
	if msg := deps2.LSPToolToggle("hover", false); !containsAll(msg, "refusing", "last enabled") {
		t.Fatalf("last-tool refusal = %q", msg)
	}
	// Unknown tool names carry the family in the note.
	if msg := deps.LSPToolToggle("frobnicate", true); !containsAll(msg, "unknown tool") {
		t.Fatalf("unknown tool msg = %q", msg)
	}
	// Read-only surfaces refuse without a store.
	if msg := (&SurfaceDeps{}).LSPToolToggle("hover", true); !containsAll(msg, "config writes unavailable") {
		t.Fatalf("nil store msg = %q", msg)
	}
}

func TestLSPToolRowsFromFamily(t *testing.T) {
	deps := &SurfaceDeps{LSP: lsp.NewManager(lsp.Settings{Enabled: false}, t.TempDir())}
	rows := deps.LSPToolRows()
	if len(rows) != len(agenttool.LSPToolNames) {
		t.Fatalf("rows = %v, want the %d-name family", rows, len(agenttool.LSPToolNames))
	}
	if rows := (&SurfaceDeps{}).LSPToolRows(); rows != nil {
		t.Fatal("nil manager must yield nil rows")
	}
}

func TestLSPListShowsToolFaceState(t *testing.T) {
	deps := &SurfaceDeps{
		LSP:          lsp.NewManager(lsp.Settings{Enabled: false}, t.TempDir()),
		LSPToolsList: func() []string { return []string{"hover"} },
	}
	msg := deps.lspList()
	if !containsAll(msg, "lsp_diagnostics [off", "lsp_hover\n") {
		t.Fatalf("listing = %q", msg)
	}
}
