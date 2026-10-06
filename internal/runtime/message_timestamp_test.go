package runtime

// Tests for the T3.5 随件② timestamp fix: the loop stamps every message it
// appends to the live list, so the microcompaction idle gate (which reads the
// newest message timestamp) actually opens after 60 minutes of inactivity.
// Before this fix no production path stamped a timestamp — provider decoders
// and drivers all leave it zero — and the idle gate was silently dead.

import (
	"context"
	"testing"
	"time"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/compaction"
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

func TestIdleGateFiresWithStampedTimestamp(t *testing.T) {
	cfg := RunConfig{LoopConfig: LoopConfig{
		ContextWindow: 20_000,
		Compaction:    compaction.CompactionSettings{Enabled: true, ReserveTokens: 2_000, KeepRecentTokens: 1_000},
	}}
	// Low pressure (8 thin groups ≈ 1.2k tokens ≪ 16k line) but the newest
	// message is two hours old → the idle branch must evict everything
	// eligible (3 groups × ~95 tokens ≥ 256 floor).
	msgs := fatReadTurns(8, 500)
	last := msgs[len(msgs)-1].(agentcore.ToolResultMessage)
	last.Timestamp = time.Now().Add(-2 * time.Hour).UnixMilli()
	msgs[len(msgs)-1] = last
	agentCtx := &agentcore.AgentContext{Messages: msgs}
	before := len(agentCtx.Messages)

	maybeMicrocompact(context.Background(), agentCtx, &cfg, func(_ agentcore.AgentEvent) error { return nil })

	if len(agentCtx.Messages) != before+1 {
		t.Fatal("two-hour-old newest message must open the idle gate and append a marker")
	}
	marker := agentCtx.Messages[len(agentCtx.Messages)-1].(agentcore.MicrocompactMessage)
	if len(marker.ClearedCallIDs) == 0 {
		t.Fatalf("expected an idle-window eviction, got %+v", marker)
	}
}

func TestIdleGateSilentWithoutTimestamps(t *testing.T) {
	// The production status quo ante: no message carries a timestamp → the
	// idle gate must stay closed (never idle on unknowns), and a low-pressure
	// context is left untouched.
	cfg := RunConfig{LoopConfig: LoopConfig{
		ContextWindow: 20_000,
		Compaction:    compaction.CompactionSettings{Enabled: true, ReserveTokens: 2_000, KeepRecentTokens: 1_000},
	}}
	agentCtx := &agentcore.AgentContext{Messages: fatReadTurns(8, 500)}
	before := len(agentCtx.Messages)
	maybeMicrocompact(context.Background(), agentCtx, &cfg, func(_ agentcore.AgentEvent) error { return nil })
	if len(agentCtx.Messages) != before {
		t.Fatal("zero timestamps must never open the idle gate")
	}
}
