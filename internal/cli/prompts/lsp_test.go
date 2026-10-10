package prompts

import (
	"strings"
	"testing"

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
