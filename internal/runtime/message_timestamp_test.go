package runtime

// Tests for the T3.5 随件② timestamp fix: the loop stamps every message it
// appends to the live list, so the microcompaction idle gate (which reads the
// newest message timestamp) actually opens after 60 minutes of inactivity.
// Before this fix no production path stamped a timestamp — provider decoders
// and drivers all leave it zero — and the idle gate was silently dead.
// (The idle-gate behavior tests themselves moved with the pipeline into
// internal/compaction — T3.3.1.)

import (
	"context"
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/provider"
)

func TestLoopStampsAppendedMessages(t *testing.T) {
	p := &fauxProvider{
		name:   "faux",
		models: []provider.Model{{Provider: "faux", ID: "faux"}},
		turns: []fauxTurn{
			toolCallTurn("call-1", "echo", `{"msg":"hello"}`),
			textTurn("done"),
		},
	}
	agentCtx := &agentcore.AgentContext{Messages: agentcore.MessageList{
		agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("start")}},
	}}
	cfg := newFauxRunCfg(p, echoTool("echo", agentcore.ToolExecutionParallel, false))
	cfg.GetFollowUpMessages = func(ctx context.Context, agentCtx *agentcore.AgentContext) []agentcore.AgentMessage {
		return nil
	}
	_, msgs := collectStream(t, agentLoop(context.Background(), agentCtx, cfg))
	if len(msgs) < 3 {
		t.Fatalf("expected assistant+result+assistant, got %d", len(msgs))
	}
	for i, m := range msgs {
		var ts int64
		switch t := m.(type) {
		case agentcore.AssistantMessage:
			ts = t.Timestamp
		case agentcore.ToolResultMessage:
			ts = t.Timestamp
		case agentcore.UserMessage:
			ts = t.Timestamp
		}
		if ts <= 0 {
			t.Errorf("appended message %d (%T) must carry a loop-stamped timestamp", i, m)
		}
	}
}
