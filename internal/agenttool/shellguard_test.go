package agenttool

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/shellguard"
)

func bashCall(command string) agentcore.AgentToolCall {
	args, _ := json.Marshal(map[string]any{"command": command})
	return agentcore.AgentToolCall{ID: "call_1", Name: "bash", Arguments: args}
}

func TestShellguardSeamOff(t *testing.T) {
	if seam := ShellguardSeam(shellguard.ModeOff, nil); seam != nil {
		t.Fatal("ShellguardSeam(off) != nil; off must install nothing")
	}
}

func TestShellguardSeamNonBashIgnored(t *testing.T) {
	seam := ShellguardSeam(shellguard.ModeStrict, nil)
	if dec := seam(context.Background(), agentcore.AgentToolCall{Name: "read", Arguments: json.RawMessage(`{"path":"x"}`)}); dec != nil {
		t.Fatal("seam gated a non-bash tool call")
	}
}

func TestShellguardSeamSafeFallsThrough(t *testing.T) {
	seam := ShellguardSeam(shellguard.ModeStrict, nil)
	if dec := seam(context.Background(), bashCall("go build ./...")); dec != nil {
		t.Fatal("seam blocked a safe command")
	}
}

func TestShellguardSeamStrictBlocks(t *testing.T) {
	for _, cmd := range []string{"rm -rf /", "echo hi > out.txt", "timeout 5 rm -rf x"} {
		seam := ShellguardSeam(shellguard.ModeStrict, nil)
		dec := seam(context.Background(), bashCall(cmd))
		if dec == nil || !dec.Block {
			t.Errorf("strict mode allowed %q", cmd)
			continue
		}
		if dec.Content == nil || len(*dec.Content) == 0 {
			t.Errorf("strict mode block for %q has no content", cmd)
		}
	}
}

func TestShellguardSeamIncompleteFailsClosed(t *testing.T) {
	// Ask mode with a nil ask callback: an unparseable command must be
	// blocked, never auto-allowed (fail-closed acceptance 2).
	seam := ShellguardSeam(shellguard.ModeAsk, nil)
	if dec := seam(context.Background(), bashCall(`timeout --unknown 5 rm -rf x`)); dec == nil || !dec.Block {
		t.Fatal("ask mode with nil ask allowed an Incomplete command")
	}
}

func TestShellguardSeamAskConsultsCallback(t *testing.T) {
	for _, allowed := range []bool{true, false} {
		ask := func(ctx context.Context, call agentcore.AgentToolCall, d shellguard.Decision) bool {
			if d.Verdict != shellguard.Hazardous {
				t.Errorf("ask callback verdict = %s, want hazardous", d.Verdict)
			}
			return allowed
		}
		seam := ShellguardSeam(shellguard.ModeAsk, ask)
		dec := seam(context.Background(), bashCall("rm -rf ./build"))
		if allowed && dec != nil {
			t.Fatal("ask mode blocked an approved command")
		}
		if !allowed && (dec == nil || !dec.Block) {
			t.Fatal("ask mode allowed a denied command")
		}
	}
}

func TestShellguardDenialSeamOff(t *testing.T) {
	seam, terminated := ShellguardDenialSeam(shellguard.ModeOff, false, nil)
	if seam != nil || terminated != nil {
		t.Fatal("off mode must install nothing")
	}
}

func TestShellguardDenialSeamContinue(t *testing.T) {
	canceled := false
	cancel := func() { canceled = true }
	seam, terminated := ShellguardDenialSeam(shellguard.ModeAsk, true, cancel)
	dec := seam(context.Background(), bashCall("rm -rf ./build"))
	if dec == nil || !dec.Block {
		t.Fatal("continue mode did not block the flagged command")
	}
	if terminated() {
		t.Fatal("continue mode reported termination")
	}
	if canceled {
		t.Fatal("continue mode canceled the run context")
	}
	// The denial is a failed tool result: content must explain the refusal so
	// the agent can route around it.
	if dec.Content == nil || len(*dec.Content) == 0 {
		t.Fatal("continue denial carries no content")
	}
}

func TestShellguardDenialSeamTerminate(t *testing.T) {
	canceled := false
	cancel := func() { canceled = true }
	seam, terminated := ShellguardDenialSeam(shellguard.ModeAsk, false, cancel)
	dec := seam(context.Background(), bashCall("rm -rf ./build"))
	if dec == nil || !dec.Block {
		t.Fatal("terminate mode did not block the flagged command")
	}
	if !terminated() || !canceled {
		t.Fatalf("terminate mode: terminated=%v canceled=%v, want true/true", terminated(), canceled)
	}
	// Safe commands must not terminate anything.
	if dec := seam(context.Background(), bashCall("ls")); dec != nil {
		t.Fatal("terminate mode blocked a safe command")
	}
}

func TestChainBeforeToolCallShellguard(t *testing.T) {
	block := &agentcore.BeforeToolCallDecision{Block: true}
	deny := func(context.Context, agentcore.AgentToolCall) *agentcore.BeforeToolCallDecision { return block }
	allow := func(context.Context, agentcore.AgentToolCall) *agentcore.BeforeToolCallDecision { return nil }
	spyCalled := false
	spy := func(context.Context, agentcore.AgentToolCall) *agentcore.BeforeToolCallDecision {
		spyCalled = true
		return nil
	}

	if ChainBeforeToolCall(nil, nil) != nil {
		t.Fatal("nil chain is not nil")
	}
	if ChainBeforeToolCall(nil, deny) == nil || ChainBeforeToolCall(allow, nil) == nil {
		t.Fatal("nil operand is not identity")
	}
	if dec := ChainBeforeToolCall(deny, spy)(context.Background(), agentcore.AgentToolCall{}); dec == nil || !dec.Block {
		t.Fatal("prev blocking decision did not win")
	}
	spyCalled = false
	ChainBeforeToolCall(allow, spy)(context.Background(), agentcore.AgentToolCall{})
	if !spyCalled {
		t.Fatal("next seam did not run after prev allowed")
	}
}

func TestBashCommandExtraction(t *testing.T) {
	if got := bashCommand(json.RawMessage(`{"command":"ls -la"}`)); got != "ls -la" {
		t.Fatalf("bashCommand = %q", got)
	}
	if got := bashCommand(json.RawMessage(`not json`)); got != "" {
		t.Fatalf("malformed payload = %q, want empty", got)
	}
}
