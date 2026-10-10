package runtime

import (
	"context"
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/agenttool"
	"github.com/smallnest/pigo/internal/provider"
	"github.com/smallnest/pigo/internal/statline"
	"github.com/smallnest/pigo/internal/testenv"
)

// usageTextTurn scripts a turn that streams text and settles with a usage
// payload, so the loop's record carries the four token buckets.
func usageTextTurn(text string, in, out, cacheRead int) fauxTurn {
	t := textTurn(text)
	done := t[len(t)-1].(provider.StreamDoneEvent)
	done.Message.Usage = &agentcore.Usage{InputTokens: in, OutputTokens: out, CacheReadTokens: cacheRead}
	t[len(t)-1] = done
	return t
}

// TestUsageRecorderRecordsSettledTurns drives the real loop through the faux
// provider and checks the session ledger receives one record per turn, with the
// provider's token buckets.
func TestUsageRecorderRecordsSettledTurns(t *testing.T) {
	root := testenv.Dir(t)
	rec := NewUsageRecorder(root)
	rec.BindSession("sess-usage")

	p := &fauxProvider{
		name:   "faux",
		models: []provider.Model{{Provider: "faux", ID: "m"}},
		turns:  []fauxTurn{usageTextTurn("hello", 1000, 50, 900)},
	}
	cfg := newFauxRunCfg(p)
	cfg.RecordUsage = rec.Record
	agentCtx := &agentcore.AgentContext{Messages: agentcore.MessageList{
		agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("hi")}},
	}}
	collectStream(t, agentLoop(context.Background(), agentCtx, cfg))

	recs, err := statline.LoadUsage(root, "sess-usage")
	if err != nil {
		t.Fatalf("LoadUsage: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("records = %d, want 1", len(recs))
	}
	if recs[0].Input != 1000 || recs[0].Output != 50 || recs[0].CacheRead != 900 {
		t.Errorf("record = %+v, want the turn's token buckets", recs[0])
	}
	if recs[0].Err {
		t.Error("a successful turn was recorded as failed")
	}
	if recs[0].Model != "faux" {
		t.Errorf("model = %q, want the request's resolved model", recs[0].Model)
	}
	if stats := rec.Stats(); stats.Calls != 1 || stats.OK() != 1 || stats.Input != 1000 {
		t.Errorf("Stats = %+v, want one successful recorded call", stats)
	}
}

// TestUsageRecorderAttributesSubagentTurns verifies a task child's turns land in
// the parent session's ledger tagged Subagent + AgentID, while the parent's own
// turns stay untagged — the O1 requirement that sub-agent overhead counts.
func TestUsageRecorderAttributesSubagentTurns(t *testing.T) {
	root := testenv.Dir(t)
	rec := NewUsageRecorder(root)
	rec.BindSession("sess-child-usage")

	child := &fauxProvider{
		name:   "faux-child",
		models: []provider.Model{{Provider: "faux-child", ID: "child"}},
		turns:  []fauxTurn{usageTextTurn("child done", 200, 20, 0)},
	}
	sub := NewSubAgentTool(SubAgentSpec{
		Name:         "researcher",
		Description:  "delegate",
		SystemPrompt: "you are a researcher",
		NewRunConfig: func() RunConfig {
			return RunConfig{
				LoopConfig: LoopConfig{Model: "child", Stream: provider.StreamFnFromProvider(child)},
				Batch:      agenttool.BatchConfig{ToolExecutorConfig: agenttool.ToolExecutorConfig{Registry: agenttool.NewToolRegistry()}},
			}
		},
		Usage: rec,
	})
	parent := &fauxProvider{
		name:   "faux-parent",
		models: []provider.Model{{Provider: "faux-parent", ID: "parent"}},
		turns: []fauxTurn{
			toolCallTurn("call-sub", "researcher", `{"prompt":"go"}`),
			usageTextTurn("final", 300, 30, 250),
		},
	}
	cfg := newFauxRunCfg(parent, sub)
	cfg.RecordUsage = rec.Record
	agentCtx := &agentcore.AgentContext{Messages: agentcore.MessageList{
		agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("delegate")}},
	}}
	collectStream(t, agentLoop(context.Background(), agentCtx, cfg))

	recs, err := statline.LoadUsage(root, "sess-child-usage")
	if err != nil {
		t.Fatalf("LoadUsage: %v", err)
	}
	var parentRecs, childRecs []statline.Record
	for _, r := range recs {
		if r.Subagent {
			childRecs = append(childRecs, r)
		} else {
			parentRecs = append(parentRecs, r)
		}
	}
	if len(childRecs) != 1 {
		t.Fatalf("sub-agent records = %d, want 1 (all: %+v)", len(childRecs), recs)
	}
	if childRecs[0].AgentID != "call-sub" {
		t.Errorf("agent id = %q, want the parent tool call id", childRecs[0].AgentID)
	}
	if childRecs[0].Input != 200 || childRecs[0].Output != 20 {
		t.Errorf("sub-agent record = %+v, want the child's buckets", childRecs[0])
	}
	if len(parentRecs) != 2 {
		t.Fatalf("parent records = %d, want 2 (all: %+v)", len(parentRecs), recs)
	}
	stats := rec.Stats()
	if stats.SubagentCalls != 1 || stats.Calls != 3 {
		t.Errorf("stats = %+v, want 3 calls of which 1 sub-agent", stats)
	}
	if stats.Input != 500 {
		t.Errorf("session input = %d, want 500 (the child's 200 plus the parent's 300; both count)", stats.Input)
	}
}

func TestUsageRecorderNilReceiverIsInert(t *testing.T) {
	var rec *UsageRecorder
	rec.BindSession("s")
	if rec.SessionID() != "" || rec.Root() != "" {
		t.Error("nil recorder should report no session")
	}
	rec.Record(statline.Record{Input: 1})
	if rec.Records() != nil {
		t.Error("nil recorder should hold no records")
	}
	if rec.Stats().Calls != 0 {
		t.Error("nil recorder should aggregate to zero")
	}
	// An unbound (but non-nil) recorder is equally inert.
	live := NewUsageRecorder(testenv.Dir(t))
	live.Record(statline.Record{Input: 1})
	if live.Records() != nil {
		t.Error("unbound recorder should write nothing")
	}
}

// TestChildSinkMarksIncompleteOnFailure pins the attribution wrapper's marking:
// a failed child turn is recorded incomplete (the fork's RecordSubagentUsage
// marker), a successful one is not.
func TestChildSinkMarksIncompleteOnFailure(t *testing.T) {
	root := testenv.Dir(t)
	rec := NewUsageRecorder(root)
	rec.BindSession("sess-mark")
	sink := rec.ChildSink("call-x")
	sink(statline.Record{Input: 5})
	sink(statline.Record{Input: 1, Err: true})

	recs, err := statline.LoadUsage(root, "sess-mark")
	if err != nil {
		t.Fatalf("LoadUsage: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("records = %d, want 2", len(recs))
	}
	for _, r := range recs {
		if !r.Subagent || r.AgentID != "call-x" {
			t.Errorf("record not attributed: %+v", r)
		}
	}
	if recs[0].Incomplete {
		t.Error("a successful child turn was marked incomplete")
	}
	if !recs[1].Incomplete {
		t.Error("a failed child turn should be marked incomplete")
	}
}
