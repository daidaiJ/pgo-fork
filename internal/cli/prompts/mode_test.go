// mode_test.go pins the /mode face (T7.6): the parse grammar, the executor
// wiring, and the posture listing/set text.
package prompts

import (
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/runtime"
	"github.com/smallnest/pigo/internal/toolrules"
)

func TestParseMode(t *testing.T) {
	cases := []struct {
		args string
		want runtime.Intent
	}{
		{"", runtime.Intent{Kind: runtime.IntentModeShow}},
		{"plan", runtime.Intent{Kind: runtime.IntentModeSet, Mode: "plan"}},
		{"  ASK ", runtime.Intent{Kind: runtime.IntentModeSet, Mode: "ask"}},
		{"all", runtime.Intent{Kind: runtime.IntentModeSet, Mode: "all"}},
		{"always-approve", runtime.Intent{Kind: runtime.IntentModeSet, Mode: "all"}},
	}
	for _, tc := range cases {
		got, err := parseMode(tc.args)
		if err != nil {
			t.Errorf("parseMode(%q): %v", tc.args, err)
			continue
		}
		if got != tc.want {
			t.Errorf("parseMode(%q) = %+v, want %+v", tc.args, got, tc.want)
		}
	}
	for _, bad := range []string{"yolo", "plan extra", "always approve"} {
		if _, err := parseMode(bad); err == nil {
			t.Errorf("parseMode(%q) should refuse", bad)
		}
	}
}

func TestExecutorModeIntents(t *testing.T) {
	x := &Executor{}
	if msg := x.Execute(runtime.Intent{Kind: runtime.IntentModeShow}).Message; msg != "mode: session posture unavailable in this context" {
		t.Fatalf("nil surface show = %q", msg)
	}
	state := toolrules.NewModeState(toolrules.ModeAsk)
	x = &Executor{Surface: &SurfaceDeps{ModeGet: state.Mode, ModeStore: state.Set}}
	list := x.Execute(runtime.Intent{Kind: runtime.IntentModeShow}).Message
	if !strings.Contains(list, "mode: ask (current)") || !strings.Contains(list, "plan —") || !strings.Contains(list, "always-approve —") || !strings.Contains(list, "shift+tab cycles") {
		t.Fatalf("listing = %q", list)
	}
	if got := x.Execute(runtime.Intent{Kind: runtime.IntentModeSet, Mode: "plan"}).Message; got != ApprovalModeNote(toolrules.ModePlan) {
		t.Fatalf("set(plan) = %q", got)
	}
	if state.Mode() != toolrules.ModePlan {
		t.Fatalf("posture after set = %v", state.Mode())
	}
	if got := x.Execute(runtime.Intent{Kind: runtime.IntentModeSet, Mode: "always-approve"}).Message; !strings.Contains(got, "always-approve") {
		t.Fatalf("set(always-approve) = %q", got)
	}
	if state.Mode() != toolrules.ModeAll {
		t.Fatalf("alias should reach all, got %v", state.Mode())
	}
	if got := x.Execute(runtime.Intent{Kind: runtime.IntentModeSet, Mode: "yolo"}).Message; !strings.Contains(got, "unknown approval mode") {
		t.Fatalf("bad name = %q", got)
	}
}

// TestApprovalModeNoteCopy locks the three switch notes (the TUI system line
// and the executor reply share them).
func TestApprovalModeNoteCopy(t *testing.T) {
	cases := map[toolrules.ApprovalMode]string{
		toolrules.ModeAsk:  "approval mode → ask",
		toolrules.ModePlan: "approval mode → plan",
		toolrules.ModeAll:  "approval mode → always-approve",
	}
	for m, want := range cases {
		if got := ApprovalModeNote(m); !strings.HasPrefix(got, want) {
			t.Errorf("note(%v) = %q, want prefix %q", m, got, want)
		}
	}
}
