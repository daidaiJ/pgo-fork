package runtime

// End-to-end shellguard acceptance tests (T2.1 acceptance 4): a faux provider
// issues a dangerous bash command and the shellguard seam (the exact one the
// headless driver installs) either converts the denial into a failed tool
// result the agent routes around (--non-interactive-denial continue) or
// terminates the run (the default). The bash tool itself is a stub: the seam
// blocks before execution, so nothing real ever runs.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/agenttool"
	"github.com/smallnest/pigo/internal/provider"
	"github.com/smallnest/pigo/internal/shellguard"
)

func bashStub() execTool {
	return execTool{
		name: "bash",
		mode: agentcore.ToolExecutionSequential,
		run: func(ctx context.Context, id string, args json.RawMessage, onUpdate agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
			return agentcore.AgentToolResult{}, nil
		},
	}
}

func toolResultOf(t *testing.T, msgs []agentcore.AgentMessage, callID string) agentcore.ToolResultMessage {
	t.Helper()
	for i := range msgs {
		if tr, ok := msgs[i].(agentcore.ToolResultMessage); ok && tr.ToolCallID == callID {
			return tr
		}
	}
	t.Fatalf("no tool result for %s in %d messages", callID, len(msgs))
	return agentcore.ToolResultMessage{}
}

// TestShellguardHeadlessContinue: denial becomes a failed tool result; the
// agent routes around it and the run completes with the follow-up turn.
func TestShellguardHeadlessContinue(t *testing.T) {
	p := &fauxProvider{
		name:   "faux",
		models: []provider.Model{{Provider: "faux", ID: "faux"}},
		turns: []fauxTurn{
			toolCallTurn("call-1", "bash", `{"command":"rm -rf /tmp/x"}`),
			textTurn("routed around the denial"),
		},
	}
	cfg := newFauxRunCfg(p, bashStub())
	seam, terminated := agenttool.ShellguardDenialSeam(shellguard.ModeAsk, true, nil)
	if seam == nil {
		t.Fatal("seam not installed")
	}
	cfg.Batch.ToolExecutorConfig.BeforeToolCall = seam
	agentCtx := &agentcore.AgentContext{Messages: agentcore.MessageList{agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("clean the dir")}}}}

	var out strings.Builder
	if err := RunHeadless(context.Background(), agentCtx, HeadlessConfig{Run: cfg, Mode: PrintMode, Out: &out}); err != nil {
		t.Fatalf("RunHeadless continue: unexpected error %v", err)
	}
	if terminated() {
		t.Fatal("continue mode terminated the run")
	}
	if got := out.String(); got != "routed around the denial\n" {
		t.Errorf("final output = %q, want the follow-up turn", got)
	}
	res := toolResultOf(t, agentCtx.Messages, "call-1")
	if !res.IsError {
		t.Error("denial must land as an ERROR tool result")
	}
	if text := textContentOf(res.Content); !strings.Contains(text, "shellguard denied") {
		t.Errorf("denial text missing: %q", text)
	}
}

// TestShellguardHeadlessTerminate: the default stops the run — RunHeadless
// reports the cancellation the seam triggered.
func TestShellguardHeadlessTerminate(t *testing.T) {
	p := &fauxProvider{
		name:   "faux",
		models: []provider.Model{{Provider: "faux", ID: "faux"}},
		turns: []fauxTurn{
			toolCallTurn("call-1", "bash", `{"command":"rm -rf /tmp/x"}`),
			textTurn("must never be reached"),
		},
	}
	cfg := newFauxRunCfg(p, bashStub())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	seam, terminated := agenttool.ShellguardDenialSeam(shellguard.ModeAsk, false, cancel)
	cfg.Batch.ToolExecutorConfig.BeforeToolCall = seam
	agentCtx := &agentcore.AgentContext{Messages: agentcore.MessageList{agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("clean the dir")}}}}

	var out strings.Builder
	err := RunHeadless(ctx, agentCtx, HeadlessConfig{Run: cfg, Mode: PrintMode, Out: &out})
	if !terminated() {
		t.Fatal("seam did not report termination")
	}
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("RunHeadless error = %v, want context cancellation", err)
	}
	// The turn-2 text proves nothing ran past the denial.
	if strings.Contains(out.String(), "must never be reached") {
		t.Error("run continued past the denial")
	}
}

// TestShellguardOffNoSeam: off mode installs nothing — the same dangerous
// command runs the (stub) tool and no denial exists.
func TestShellguardOffNoSeam(t *testing.T) {
	p := &fauxProvider{
		name:   "faux",
		models: []provider.Model{{Provider: "faux", ID: "faux"}},
		turns: []fauxTurn{
			toolCallTurn("call-1", "bash", `{"command":"rm -rf /tmp/x"}`),
			textTurn("done"),
		},
	}
	cfg := newFauxRunCfg(p, bashStub())
	if seam, _ := agenttool.ShellguardDenialSeam(shellguard.ModeOff, false, nil); seam != nil {
		t.Fatal("off mode installed a seam")
	}
	agentCtx := &agentcore.AgentContext{Messages: agentcore.MessageList{agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("clean")}}}}
	var out strings.Builder
	if err := RunHeadless(context.Background(), agentCtx, HeadlessConfig{Run: cfg, Mode: PrintMode, Out: &out}); err != nil {
		t.Fatalf("RunHeadless: %v", err)
	}
	res := toolResultOf(t, agentCtx.Messages, "call-1")
	if res.IsError {
		t.Errorf("off mode must not deny: %q", textContentOf(res.Content))
	}
}
