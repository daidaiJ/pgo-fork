package run

// T5.2 contract snapshot: every builtin tool must declare its side-effect
// contract (EffectAware) so the permission engine reads declarations instead
// of guessing at the call site. A new tool without Effect() fails here rather
// than silently falling back to the conservative default and prompting on
// every call.

import (
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
)

func TestBuiltinToolsDeclareEffect(t *testing.T) {
	tools := BuiltinTools(t.TempDir(), false)
	if len(tools) == 0 {
		t.Fatal("expected builtin tools")
	}
	readonly := map[string]bool{
		"read": true, "grep": true, "find": true, "ls": true,
		"memory_search": true, "webfetch": true, "websearch": true,
		"todo": true, "bash_output": true, "search_tools": true,
		"context_edit": true, "ask_user": true,
		"goal_complete": true, "goal_blocked": true, "schedule_list": true,
	}
	for _, tl := range tools {
		eff, ok := tl.(agentcore.EffectAware)
		if !ok {
			t.Errorf("tool %q does not declare Effect() (T5.2 contract)", tl.Name())
			continue
		}
		got := eff.Effect()
		if want := readonly[tl.Name()]; got.ReadOnly != want {
			t.Errorf("tool %q ReadOnly = %v, want %v", tl.Name(), got.ReadOnly, want)
		}
		if got.Scope == "" {
			t.Errorf("tool %q declares an empty Scope", tl.Name())
		}
	}
}
