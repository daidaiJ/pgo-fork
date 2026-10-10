// approvalmode_test.go locks the T7.6 approval-posture semantics: the plan
// gate (read-only passes, effect calls block with the plan-directed
// message), the ask-posture honest unavailability wording (D-C2), and the
// posture value mapping.
package toolrules

import (
	"context"
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
)

// TestEnginePlanGate: plan mode allows read-only tools by contract and
// blocks every effect call — deny rules stay terminal ahead of it, allow
// rules and trust never auto-run a write, and the ask channel is never
// consulted (per-call approval of a write would defeat the posture).
func TestEnginePlanGate(t *testing.T) {
	state := NewModeState(ModePlan)
	var asked int
	e := newTestEngine(t, EngineConfig{
		Cwd:     "/w",
		Effects: EffectTable([]agentcore.AgentTool{&fakeTool{name: "bash"}, &fakeTool{name: "grep", eff: &agentcore.ToolEffect{ReadOnly: true, Scope: agentcore.ScopeWorkspace}}}),
		// A trusted directory and a settled allow rule: in plan mode
		// neither may auto-run an effect call.
		Trusted:      func(string) bool { return true },
		SessionRules: []Rule{{Tool: "bash", Pattern: "echo", Action: ActionAllow}},
		Ask: func(ctx context.Context, c agentcore.AgentToolCall, r AskReason, h ProposedHint) (AskDecision, Rule) {
			asked++
			return AskApprove, Rule{}
		},
		Mode: state.Mode,
	})
	if dec := e.BeforeToolCall(context.Background(), call("grep", `{"pattern":"x"}`)); dec != nil {
		t.Fatalf("read-only tool must pass the plan gate, got %+v", dec)
	}
	dec := e.BeforeToolCall(context.Background(), call("bash", `{"command":"echo hi"}`))
	if dec == nil || !dec.Block {
		t.Fatalf("effect call must block in plan mode, got %+v", dec)
	}
	if text := agentcore.ContentToText(*dec.Content); !strings.Contains(text, "plan mode is active") || !strings.Contains(text, "/mode") {
		t.Errorf("plan block message should name the mode and the switch path, got %q", text)
	}
	if asked != 0 {
		t.Errorf("plan mode must never consult the ask channel, asked %d times", asked)
	}
	// Deny rules stay terminal (their own message, not the plan one).
	e2 := newTestEngine(t, EngineConfig{
		Cwd:     "/w",
		Effects: EffectTable([]agentcore.AgentTool{&fakeTool{name: "bash"}}),
		SessionRules: []Rule{
			{Tool: "bash", Pattern: "curl", Action: ActionDeny},
		},
		Mode: state.Mode,
	})
	dec = e2.BeforeToolCall(context.Background(), call("bash", `{"command":"curl example.com"}`))
	if dec == nil || !dec.Block {
		t.Fatalf("deny rule must stay terminal in plan mode, got %+v", dec)
	}
	if text := agentcore.ContentToText(*dec.Content); strings.Contains(text, "plan mode is active") {
		t.Errorf("deny-rule block should keep its terminal wording, got %q", text)
	}
}

// TestEngineAskModeUntrustedMessage pins D-C2: AskUnavailable renders the
// honest "no channel" wording while AskDeny keeps the user-refusal wording.
func TestEngineAskModeUnavailable(t *testing.T) {
	for _, tc := range []struct {
		decision AskDecision
		want     string
	}{
		{AskUnavailable, "no interactive channel was available"},
		{AskDeny, "permission denied by the user"},
	} {
		e := newTestEngine(t, EngineConfig{
			Cwd:     "/w",
			Effects: EffectTable([]agentcore.AgentTool{&fakeTool{name: "bash"}}),
			Ask: func(ctx context.Context, c agentcore.AgentToolCall, r AskReason, h ProposedHint) (AskDecision, Rule) {
				return tc.decision, Rule{}
			},
		})
		dec := e.BeforeToolCall(context.Background(), call("bash", `{"command":"ls"}`))
		if dec == nil || !dec.Block {
			t.Fatalf("%v must block, got %+v", tc.decision, dec)
		}
		if text := agentcore.ContentToText(*dec.Content); !strings.Contains(text, tc.want) {
			t.Errorf("decision %v: block text missing %q, got %q", tc.decision, tc.want, text)
		}
	}
}

// TestApprovalModeValues locks the display names and the parse surface: the
// /mode grammar's aliases fold onto the canonical postures, everything else
// refuses.
func TestApprovalModeValues(t *testing.T) {
	if ModeAsk.String() != "ask" || ModePlan.String() != "plan" || ModeAll.String() != "always-approve" {
		t.Fatalf("display names = %q %q %q", ModeAsk, ModePlan, ModeAll)
	}
	cases := []struct {
		in   string
		want ApprovalMode
		ok   bool
	}{
		{"ask", ModeAsk, true},
		{"plan", ModePlan, true},
		{"all", ModeAll, true},
		{"always-approve", ModeAll, true},
		{"Always-Approve", 0, false}, // case-sensitive: parse lowercases first
		{"yolo", 0, false},
		{"", 0, false},
	}
	for _, tc := range cases {
		got, err := ParseApprovalMode(tc.in)
		if tc.ok && (err != nil || got != tc.want) {
			t.Errorf("ParseApprovalMode(%q) = %v, %v; want %v", tc.in, got, err, tc.want)
		}
		if !tc.ok && err == nil {
			t.Errorf("ParseApprovalMode(%q) should refuse", tc.in)
		}
	}
	// The ring: ask → plan → all → ask.
	for m, want := range map[ApprovalMode]ApprovalMode{ModeAsk: ModePlan, ModePlan: ModeAll, ModeAll: ModeAsk} {
		if m.Next() != want {
			t.Errorf("%v.Next() = %v, want %v", m, m.Next(), want)
		}
	}
}

// TestModeStateConcurrent covers the holder's set/read round trip (the
// engine reads from tool goroutines while the front-end writes).
func TestModeStateConcurrent(t *testing.T) {
	s := NewModeState(ModeAsk)
	if s.Mode() != ModeAsk {
		t.Fatalf("seed = %v", s.Mode())
	}
	s.Set(ModePlan)
	if s.Mode() != ModePlan {
		t.Fatalf("after Set(plan) = %v", s.Mode())
	}
	s.Set(ModeAll)
	if s.Mode() != ModeAll {
		t.Fatalf("after Set(all) = %v", s.Mode())
	}
}
