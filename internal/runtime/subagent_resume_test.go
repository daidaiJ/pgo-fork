package runtime

// Tests for T7.1 sub-agent resume (wiki/port/subagent-resume.md §5.4): transcript
// persistence, the resume prefix (projection + T5.2 sanitization + window
// budget), the model pin / degrade rulings, the envelope's resume handle, and
// the sidecar store's own unit contract. The child loop is driven through the
// faux provider seam (only the provider boundary is faked); the store uses
// testenv.Dir(t) for scratch space, per repo practice.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/agenttool"
	"github.com/smallnest/pigo/internal/provider"
	"github.com/smallnest/pigo/internal/testenv"
)

// --- helpers -----------------------------------------------------------------

// resumeStore returns a sub-agent store rooted in scratch space and bound to
// sessionID.
func resumeStore(t *testing.T, sessionID string) *SubagentStore {
	t.Helper()
	st := NewSubagentStore(filepath.Join(testenv.Dir(t), "sessions"))
	if st == nil {
		t.Fatal("NewSubagentStore returned nil for a non-empty root")
	}
	st.BindSession(sessionID)
	return st
}

// childFactory builds the task tool's fresh-spawn factory over one faux child
// provider and an optional child tool set.
func childFactory(p *fauxProvider, tools ...agentcore.AgentTool) func() RunConfig {
	reg := agenttool.NewToolRegistry()
	for _, tl := range tools {
		if err := reg.Register(tl); err != nil {
			panic(err)
		}
	}
	return func() RunConfig {
		return RunConfig{
			LoopConfig: LoopConfig{Model: p.name, Stream: provider.StreamFnFromProvider(p)},
			Batch:      agenttool.BatchConfig{ToolExecutorConfig: agenttool.ToolExecutorConfig{Registry: reg}},
		}
	}
}

// effectTool is an execTool that declares a T5.2 effect, so resume-prefix
// sanitization (read-only verbatim vs side-effect marker) is exercisable.
type effectTool struct {
	execTool
	readOnly bool
}

func (t effectTool) Effect() agentcore.ToolEffect {
	return agentcore.ToolEffect{ReadOnly: t.readOnly}
}

// newReadTool / newWriteTool are minimal report-producing tools for the
// sanitization tests.
func newReadTool() effectTool {
	return effectTool{
		execTool: execTool{name: "read", run: func(context.Context, string, json.RawMessage, agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
			return agentcore.AgentToolResult{Content: agentcore.ContentList{agentcore.NewTextContent("read result")}}, nil
		}},
		readOnly: true,
	}
}

func newWriteTool() effectTool {
	return effectTool{
		execTool: execTool{name: "write", run: func(context.Context, string, json.RawMessage, agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
			return agentcore.AgentToolResult{Content: agentcore.ContentList{agentcore.NewTextContent("write result")}}, nil
		}},
	}
}

func assistantMsg(text string) agentcore.Message {
	return agentcore.AssistantMessage{RoleField: agentcore.RoleAssistant, Content: agentcore.ContentList{agentcore.NewTextContent(text)}}
}

func toolCallMsg(id, name string) agentcore.Message {
	return agentcore.AssistantMessage{RoleField: agentcore.RoleAssistant, Content: agentcore.ContentList{agentcore.NewToolCallContent(id, name, json.RawMessage(`{}`))}}
}

func toolResultMsg(id, name, text string) agentcore.Message {
	return agentcore.ToolResultMessage{
		RoleField:  agentcore.RoleToolResult,
		ToolCallID: id,
		ToolName:   name,
		Content:    agentcore.ContentList{agentcore.NewTextContent(text)},
	}
}

// writeSettled persists a transcript plus a terminal meta, as a settled run
// would (without a model pin).
func writeSettled(t *testing.T, st *SubagentStore, agentID string, msgs agentcore.MessageList, status, reason string) {
	t.Helper()
	writeSettledMeta(t, st, agentID, msgs, SubagentMeta{Status: status, StopReason: reason})
}

// writeSettledMeta is writeSettled with an explicit meta record, so the model
// pin a resume compares against is under the test's control.
func writeSettledMeta(t *testing.T, st *SubagentStore, agentID string, msgs agentcore.MessageList, meta SubagentMeta) {
	t.Helper()
	if err := st.Append(agentID, msgs); err != nil {
		t.Fatalf("Append(%s): %v", agentID, err)
	}
	meta.AgentID = agentID
	meta.Messages = len(msgs)
	if err := st.Finalize(agentID, meta); err != nil {
		t.Fatalf("Finalize(%s): %v", agentID, err)
	}
}

// requestToolResults maps a request's tool results by tool-call id.
func requestToolResults(req provider.CompletionRequest) map[string]agentcore.ToolResultMessage {
	out := map[string]agentcore.ToolResultMessage{}
	for _, m := range req.Context.Messages {
		if tr, ok := m.(agentcore.ToolResultMessage); ok {
			out[tr.ToolCallID] = tr
		}
	}
	return out
}

// assertNoDanglingToolCalls fails when any tool call in msgs lacks a result.
func assertNoDanglingToolCalls(t *testing.T, msgs agentcore.MessageList) {
	t.Helper()
	answered := map[string]bool{}
	for _, m := range msgs {
		if tr, ok := m.(agentcore.ToolResultMessage); ok {
			answered[tr.ToolCallID] = true
		}
	}
	for _, m := range msgs {
		if a, ok := m.(agentcore.AssistantMessage); ok {
			for _, c := range a.ToolCalls() {
				if !answered[c.ID] {
					t.Errorf("request prefix has a dangling tool call %q", c.ID)
				}
			}
		}
	}
}

// --- §5.4 row 1: settle persistence, all four terminal shapes ---------------

func TestSubagentResumeSettlePersistence(t *testing.T) {
	cases := []struct {
		name       string
		turn       fauxTurn
		wantStatus string
		wantReason string
	}{
		{"completed", textTurn("final report"), SubAgentStatusCompleted, "completed"},
		{"failed on error", stopTurn(agentcore.StopReasonError, "", "child exploded"), SubAgentStatusFailed, "error"},
		{"cancelled", stopTurn(agentcore.StopReasonAborted, "", "aborted"), SubAgentStatusFailed, "cancelled"},
		{"transport failure", stopTurn(agentcore.StopReasonError, "", "transport: upstream 500: bad gateway"), SubAgentStatusFailed, "error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := resumeStore(t, "sess-settle")
			child := &fauxProvider{name: "child", turns: []fauxTurn{tc.turn}}
			tool := NewTaskToolWithConfig(childFactory(child), nil, TaskToolConfig{
				Store:  st,
				Target: SubAgentTarget{Model: "child"},
			})
			if _, err := tool.Execute(context.Background(), "ag-1", json.RawMessage(`{"prompt":"go"}`), nil); err != nil {
				t.Fatalf("Execute: %v", err)
			}
			msgs, meta, err := st.Load("ag-1")
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if meta == nil {
				t.Fatal("settle must persist a meta record")
			}
			if meta.Status != tc.wantStatus || meta.StopReason != tc.wantReason {
				t.Errorf("meta = %+v, want status=%s stop_reason=%s", meta, tc.wantStatus, tc.wantReason)
			}
			if meta.Model != "child" {
				t.Errorf("meta.Model = %q, want the pin target's model", meta.Model)
			}
			if len(msgs) < 2 || meta.Messages != len(msgs) {
				t.Errorf("transcript = %d messages (meta.Messages=%d), want >= 2 and equal", len(msgs), meta.Messages)
			}
			if meta.CreatedAt.IsZero() || meta.UpdatedAt.IsZero() {
				t.Errorf("meta timestamps not set: %+v", meta)
			}
		})
	}
}

// TestSubagentResumeUnboundStoreIsInert pins that a store without a bound
// session neither writes nor advertises a resume handle.
func TestSubagentResumeUnboundStoreIsInert(t *testing.T) {
	st := NewSubagentStore(testenv.Dir(t))
	child := &fauxProvider{name: "child", turns: []fauxTurn{stopTurn(agentcore.StopReasonError, "", "boom")}}
	tool := NewTaskToolWithConfig(childFactory(child), nil, TaskToolConfig{Store: st, Target: SubAgentTarget{Model: "child"}})
	res, err := tool.Execute(context.Background(), "ag-1", json.RawMessage(`{"prompt":"go"}`), nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if strings.Contains(agentcore.ContentToText(res.Content), "resume_hint:") {
		t.Errorf("an unbound store must not advertise a resume handle:\n%s", agentcore.ContentToText(res.Content))
	}
	msgs, meta, err := st.Load("ag-1")
	if err != nil || meta != nil || msgs != nil {
		t.Errorf("unbound Load = (%v, %+v, %v), want empty", msgs, meta, err)
	}
}

// --- §5.4 row 2/3: prefix replay, no re-execution, T5.2 sanitization --------

func TestSubagentResumeReplaysPrefixWithoutReexecuting(t *testing.T) {
	st := resumeStore(t, "sess-replay")
	var runs int32
	countTool := effectTool{
		execTool: execTool{name: "count", run: func(context.Context, string, json.RawMessage, agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
			atomic.AddInt32(&runs, 1)
			return agentcore.AgentToolResult{Content: agentcore.ContentList{agentcore.NewTextContent("counted")}}, nil
		}},
		readOnly: false,
	}
	// First dispatch: one tool call, then a failure — the transcript a resume
	// would continue.
	first := &fauxProvider{name: "child", turns: []fauxTurn{
		toolCallTurn("t1", "count", `{}`),
		stopTurn(agentcore.StopReasonError, "", "ran out of steam"),
	}}
	tool1 := NewTaskToolWithConfig(childFactory(first, countTool), nil, TaskToolConfig{
		Store:  st,
		Target: SubAgentTarget{Model: "child"},
	})
	if _, err := tool1.Execute(context.Background(), "ag-1", json.RawMessage(`{"prompt":"original task"}`), nil); err != nil {
		t.Fatalf("first Execute: %v", err)
	}
	stored, _, err := st.Load("ag-1")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(stored) != 4 {
		t.Fatalf("stored transcript = %d messages, want 4: %+v", len(stored), stored)
	}

	second := &fauxProvider{name: "child", turns: []fauxTurn{textTurn("continued work")}}
	tool2 := NewTaskToolWithConfig(childFactory(second, countTool), nil, TaskToolConfig{
		Store:  st,
		Target: SubAgentTarget{Model: "child"},
	})
	res, err := tool2.Execute(context.Background(), "ag-2", json.RawMessage(`{"prompt":"keep going","resume":"ag-1"}`), nil)
	if err != nil {
		t.Fatalf("resume Execute: %v", err)
	}
	if got := agentcore.ContentToText(res.Content); got != "continued work" {
		t.Errorf("resumed result = %q, want the child's new final text", got)
	}
	// The resumed run's FIRST request carries the replayed prefix + the new
	// prompt: the stored 4 messages plus the prompt.
	if second.callCount() != 1 {
		t.Fatalf("resumed child provider called %d times, want 1", second.callCount())
	}
	msgs := second.requestAt(0).Context.Messages
	if len(msgs) != 5 {
		t.Fatalf("resumed request carries %d messages, want 5 (4 replayed + prompt)", len(msgs))
	}
	if u, ok := msgs[0].(agentcore.UserMessage); !ok || agentcore.ContentToText(u.Content) != "original task" {
		t.Errorf("replayed prefix must start with the original prompt, got %T %+v", msgs[0], msgs[0])
	}
	if u, ok := msgs[4].(agentcore.UserMessage); !ok || agentcore.ContentToText(u.Content) != "keep going" {
		t.Errorf("the new prompt must be the last message, got %T %+v", msgs[4], msgs[4])
	}
	// The earlier tool call must NOT have been re-executed (that is the token
	// saving: the prefix replays its result instead of redoing the work).
	if n := atomic.LoadInt32(&runs); n != 1 {
		t.Errorf("tool ran %d times, want 1 (the replayed result must not re-execute)", n)
	}
	// Lineage is recorded on the new record.
	_, meta, err := st.Load("ag-2")
	if err != nil || meta == nil {
		t.Fatalf("Load(ag-2) = %+v, %v", meta, err)
	}
	if meta.ResumedFrom != "ag-1" {
		t.Errorf("meta.ResumedFrom = %q, want ag-1", meta.ResumedFrom)
	}
}

func TestSubagentResumeSanitizesSideEffects(t *testing.T) {
	st := resumeStore(t, "sess-sanitize")
	writeSettledMeta(t, st, "ag-1", agentcore.MessageList{
		userMsg("task"),
		toolCallMsg("c1", "read"),
		toolResultMsg("c1", "read", "FILE-CONTENTS-VERBATIM"),
		toolCallMsg("c2", "write"),
		toolResultMsg("c2", "write", "WROTE-THE-FILE"),
		assistantMsg("partial progress"),
	}, SubagentMeta{Status: SubAgentStatusFailed, StopReason: "error", Model: "child"})

	child := &fauxProvider{name: "child", turns: []fauxTurn{textTurn("done")}}
	tool := NewTaskToolWithConfig(childFactory(child, newReadTool(), newWriteTool()), nil, TaskToolConfig{
		Store:  st,
		Target: SubAgentTarget{Model: "child"},
	})
	if _, err := tool.Execute(context.Background(), "ag-2", json.RawMessage(`{"prompt":"continue","resume":"ag-1"}`), nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	results := requestToolResults(child.requestAt(0))
	read := agentcore.ContentToText(results["c1"].Content)
	if read != "FILE-CONTENTS-VERBATIM" {
		t.Errorf("read-only result = %q, want the verbatim record", read)
	}
	write := agentcore.ContentToText(results["c2"].Content)
	if strings.Contains(write, "WROTE-THE-FILE") {
		t.Errorf("side-effect result must not replay its output verbatim, got %q", write)
	}
	if !strings.Contains(write, "already ran") {
		t.Errorf("side-effect result = %q, want the already-executed marker", write)
	}
	// The stored transcript keeps the real records (only the in-memory prefix is
	// rewritten).
	stored, _, err := st.Load("ag-1")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := agentcore.ContentToText(stored[4].(agentcore.ToolResultMessage).Content); got != "WROTE-THE-FILE" {
		t.Errorf("stored transcript mutated: %q", got)
	}
}

// --- §5.4 row 4: dangling tool calls are repaired before replay -------------

func TestSubagentResumeRepairsDanglingToolCalls(t *testing.T) {
	st := resumeStore(t, "sess-dangling")
	writeSettledMeta(t, st, "ag-1", agentcore.MessageList{
		userMsg("task"),
		toolCallMsg("c1", "read"), // no result: the run died mid-flight
		assistantMsg("gave up"),
	}, SubagentMeta{Status: SubAgentStatusFailed, StopReason: "error", Model: "child"})

	child := &fauxProvider{name: "child", turns: []fauxTurn{textTurn("done")}}
	tool := NewTaskToolWithConfig(childFactory(child, newReadTool()), nil, TaskToolConfig{
		Store:  st,
		Target: SubAgentTarget{Model: "child"},
	})
	if _, err := tool.Execute(context.Background(), "ag-2", json.RawMessage(`{"prompt":"continue","resume":"ag-1"}`), nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	msgs := child.requestAt(0).Context.Messages
	assertNoDanglingToolCalls(t, msgs)
	results := requestToolResults(child.requestAt(0))
	tr, ok := results["c1"]
	if !ok {
		t.Fatal("the dangling call must get a synthesized result")
	}
	if !tr.IsError || !strings.Contains(agentcore.ContentToText(tr.Content), "missing") {
		t.Errorf("synthetic repair result = %+v, want an error result explaining the missing result", tr)
	}
}

// --- §5.4 row 5: model pin, cross-model re-resolution, degrade -------------

func TestSubagentResumePinSameTarget(t *testing.T) {
	st := resumeStore(t, "sess-pin-same")
	writeSettledMeta(t, st, "ag-1", agentcore.MessageList{userMsg("a"), assistantMsg("b")}, SubagentMeta{
		Status: SubAgentStatusFailed, StopReason: "error", Model: "child", Provider: "p", Protocol: "openai",
	})

	child := &fauxProvider{name: "child", turns: []fauxTurn{textTurn("same-target run")}}
	var crossCalls int32
	cross := func(SubAgentTarget) (RunConfig, error) {
		atomic.AddInt32(&crossCalls, 1)
		return RunConfig{}, errors.New("must not be consulted for an identical triple")
	}
	tool := NewTaskToolWithConfig(childFactory(child), nil, TaskToolConfig{
		Store:           st,
		Target:          SubAgentTarget{Model: "child", ProviderName: "p", Protocol: "openai"},
		NewRunConfigFor: cross,
	})
	res, err := tool.Execute(context.Background(), "ag-2", json.RawMessage(`{"prompt":"go","resume":"ag-1"}`), nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got := agentcore.ContentToText(res.Content); got != "same-target run" {
		t.Errorf("result = %q", got)
	}
	if n := atomic.LoadInt32(&crossCalls); n != 0 {
		t.Errorf("NewRunConfigFor called %d times for an identical triple, want 0", n)
	}
}

func TestSubagentResumePinDifferentTarget(t *testing.T) {
	st := resumeStore(t, "sess-pin-diff")
	writeSettled(t, st, "ag-1", agentcore.MessageList{userMsg("a"), assistantMsg("b")}, SubAgentStatusFailed, "error")
	// Record a source triple that differs from the resuming process's target.
	if err := st.Finalize("ag-1", SubagentMeta{Status: SubAgentStatusFailed, StopReason: "error", Model: "old-model", Provider: "p"}); err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	fresh := &fauxProvider{name: "fresh", turns: []fauxTurn{textTurn("fresh-model run")}}
	pinned := &fauxProvider{name: "pinned", turns: []fauxTurn{textTurn("pinned-model run")}}
	var gotTarget SubAgentTarget
	cross := func(target SubAgentTarget) (RunConfig, error) {
		gotTarget = target
		reg := agenttool.NewToolRegistry()
		return RunConfig{
			LoopConfig: LoopConfig{Model: "old-model", Stream: provider.StreamFnFromProvider(pinned)},
			Batch:      agenttool.BatchConfig{ToolExecutorConfig: agenttool.ToolExecutorConfig{Registry: reg}},
		}, nil
	}
	tool := NewTaskToolWithConfig(childFactory(fresh), nil, TaskToolConfig{
		Store:           st,
		Target:          SubAgentTarget{Model: "new-model"},
		NewRunConfigFor: cross,
	})
	res, err := tool.Execute(context.Background(), "ag-2", json.RawMessage(`{"prompt":"go","resume":"ag-1"}`), nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got := agentcore.ContentToText(res.Content); got != "pinned-model run" {
		t.Errorf("result = %q, want the pinned source model's run", got)
	}
	if gotTarget.Model != "old-model" {
		t.Errorf("NewRunConfigFor target = %+v, want the recorded source triple", gotTarget)
	}
	if fresh.callCount() != 0 {
		t.Errorf("the fresh-model provider ran %d times, want 0 (the pin must win)", fresh.callCount())
	}
}

func TestSubagentResumeDegradesWhenSourceUnresolvable(t *testing.T) {
	st := resumeStore(t, "sess-degrade-resolve")
	writeSettled(t, st, "ag-1", agentcore.MessageList{userMsg("a"), assistantMsg("b")}, SubAgentStatusFailed, "error")
	if err := st.Finalize("ag-1", SubagentMeta{Status: SubAgentStatusFailed, StopReason: "error", Model: "gone-model"}); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	child := &fauxProvider{name: "child", turns: []fauxTurn{textTurn("fresh fallback")}}
	cross := func(SubAgentTarget) (RunConfig, error) {
		return RunConfig{}, errors.New("unknown model")
	}
	tool := NewTaskToolWithConfig(childFactory(child), nil, TaskToolConfig{
		Store:           st,
		Target:          SubAgentTarget{Model: "child"},
		NewRunConfigFor: cross,
	})
	res, err := tool.Execute(context.Background(), "ag-2", json.RawMessage(`{"prompt":"go","resume":"ag-1"}`), nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got := agentcore.ContentToText(res.Content)
	if !strings.Contains(got, "[resume degraded:") || !strings.Contains(got, "gone-model") {
		t.Errorf("result must lead with the degrade note naming the source model:\n%s", got)
	}
	if !strings.Contains(got, "fresh fallback") {
		t.Errorf("result must carry the fresh run's output:\n%s", got)
	}
	// The fresh run carries only the new prompt — no replayed prefix.
	if n := len(child.requestAt(0).Context.Messages); n != 1 {
		t.Errorf("degraded fresh run carried %d messages, want 1", n)
	}
}

func TestSubagentResumeDegradesOnRateLimit(t *testing.T) {
	st := resumeStore(t, "sess-degrade-429")
	writeSettledMeta(t, st, "ag-1", agentcore.MessageList{userMsg("a"), assistantMsg("b")}, SubagentMeta{
		Status: SubAgentStatusFailed, StopReason: "error", Model: "child",
	})
	// One provider drives both attempts: the resumed run gets the 429 turn, the
	// fresh run gets the following text turn.
	child := &fauxProvider{name: "child", turns: []fauxTurn{
		stopTurn(agentcore.StopReasonError, "", "transport: upstream 429"),
		textTurn("recovered on a fresh run"),
	}}
	tool := NewTaskToolWithConfig(childFactory(child), nil, TaskToolConfig{
		Store:  st,
		Target: SubAgentTarget{Model: "child"},
	})
	res, err := tool.Execute(context.Background(), "ag-2", json.RawMessage(`{"prompt":"go","resume":"ag-1"}`), nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got := agentcore.ContentToText(res.Content)
	if !strings.Contains(got, "[resume degraded:") || !strings.Contains(got, "rate-limit") {
		t.Errorf("result must lead with the rate-limit degrade note:\n%s", got)
	}
	if !strings.Contains(got, "recovered on a fresh run") {
		t.Errorf("result must carry the fresh run's output:\n%s", got)
	}
	if n := len(child.requestAt(1).Context.Messages); n != 1 {
		t.Errorf("the fresh retry carried %d messages, want 1 (no prefix)", n)
	}
}

func TestSubagentResumeDegradesOnlyOnce(t *testing.T) {
	st := resumeStore(t, "sess-degrade-once")
	writeSettledMeta(t, st, "ag-1", agentcore.MessageList{userMsg("a"), assistantMsg("b")}, SubagentMeta{
		Status: SubAgentStatusFailed, StopReason: "error", Model: "child",
	})
	child := &fauxProvider{name: "child", turns: []fauxTurn{
		stopTurn(agentcore.StopReasonError, "", "transport: upstream 429"),
		stopTurn(agentcore.StopReasonError, "", "transport: upstream 429"),
	}}
	tool := NewTaskToolWithConfig(childFactory(child), nil, TaskToolConfig{
		Store:  st,
		Target: SubAgentTarget{Model: "child"},
	})
	res, err := tool.Execute(context.Background(), "ag-2", json.RawMessage(`{"prompt":"go","resume":"ag-1"}`), nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got := agentcore.ContentToText(res.Content)
	if n := strings.Count(got, "[resume degraded:"); n != 1 {
		t.Errorf("degrade note appears %d times, want exactly 1 (no second degrade):\n%s", n, got)
	}
	env, ok := res.Details.(SubAgentEnvelope)
	if !ok || env.Status != SubAgentStatusFailed || env.StopReason != "error" {
		t.Errorf("a twice-failed resume must surface the normal error envelope, got %T %+v", res.Details, res.Details)
	}
}

// --- §5.4 row 6: parent cancellation behavior is unchanged -----------------

func TestSubagentResumeParentCancelKeepsBehavior(t *testing.T) {
	st := resumeStore(t, "sess-cancel")
	child := &fauxProvider{name: "child", turns: []fauxTurn{textTurn("never delivered")}, delay: 40 * time.Millisecond}
	tool := NewTaskToolWithConfig(childFactory(child), nil, TaskToolConfig{
		Store:  st,
		Target: SubAgentTarget{Model: "child"},
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := tool.Execute(ctx, "ag-1", json.RawMessage(`{"prompt":"go"}`), nil)
		done <- err
	}()
	time.Sleep(20 * time.Millisecond) // let the child start streaming
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled parent must surface context.Canceled, got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Execute did not return after cancellation")
	}
	msgs, meta, err := st.Load("ag-1")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if meta == nil {
		t.Fatal("a cancelled run must still persist its transcript (cross-run resume)")
	}
	if meta.StopReason != "cancelled" || meta.Status != SubAgentStatusFailed {
		t.Errorf("cancelled meta = %+v, want failed/cancelled", meta)
	}
	if len(msgs) == 0 {
		t.Error("cancelled transcript is empty")
	}
}

// --- §5.4 row 7: envelope wording ------------------------------------------

func TestSubagentEnvelopeResumeHint(t *testing.T) {
	cases := []struct {
		name        string
		turn        fauxTurn
		bound       bool
		wantHint    bool
		wantNoHint  bool
		wantNext    []string
		verbatimOut string
	}{
		{
			name:     "failed with a bound store advertises resume",
			turn:     stopTurn(agentcore.StopReasonError, "", "boom"),
			bound:    true,
			wantHint: true,
			wantNext: []string{"Resume to continue where it stopped", `resume="ag-hint"`},
		},
		{
			name:       "failed without a store keeps re-dispatch wording",
			turn:       stopTurn(agentcore.StopReasonError, "", "boom"),
			bound:      false,
			wantNoHint: true,
			wantNext:   []string{"The sub-agent failed"},
		},
		{
			name:       "cancelled never advertises resume",
			turn:       stopTurn(agentcore.StopReasonAborted, "", "aborted"),
			bound:      true,
			wantNoHint: true,
			wantNext:   []string{"Do not restart"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var cfg TaskToolConfig
			if tc.bound {
				cfg.Store = resumeStore(t, "sess-hint")
			}
			cfg.Target = SubAgentTarget{Model: "child"}
			child := &fauxProvider{name: "child", turns: []fauxTurn{tc.turn}}
			tool := NewTaskToolWithConfig(childFactory(child), nil, cfg)
			res, err := tool.Execute(context.Background(), "ag-hint", json.RawMessage(`{"prompt":"go"}`), nil)
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			got := agentcore.ContentToText(res.Content)
			env, ok := res.Details.(SubAgentEnvelope)
			if !ok {
				t.Fatalf("Details = %T, want SubAgentEnvelope", res.Details)
			}
			if tc.wantHint {
				if env.ResumeHint == "" || !strings.Contains(got, "resume_hint:") {
					t.Errorf("envelope must advertise a resume handle: %+v\n%s", env, got)
				}
				if !strings.Contains(env.ResumeHint, `resume="ag-hint"`) {
					t.Errorf("resume hint must name the handle: %q", env.ResumeHint)
				}
			}
			if tc.wantNoHint {
				if env.ResumeHint != "" || strings.Contains(got, "resume_hint:") {
					t.Errorf("envelope must not advertise a handle: %+v\n%s", env, got)
				}
			}
			for _, frag := range tc.wantNext {
				if !strings.Contains(env.NextStep, frag) {
					t.Errorf("next_step %q missing %q", env.NextStep, frag)
				}
			}
		})
	}
}

// TestSubagentCompletedKeepsVerbatimContract pins that a completed run — with a
// bound store, hence resumable — still returns the child's text verbatim with
// no envelope header and no hint (P4: the byte contract wins over the kimi
// "advertise on success" shape; the handle stays reachable in Details).
func TestSubagentCompletedKeepsVerbatimContract(t *testing.T) {
	st := resumeStore(t, "sess-completed")
	child := &fauxProvider{name: "child", turns: []fauxTurn{textTurn("the final report")}}
	tool := NewTaskToolWithConfig(childFactory(child), nil, TaskToolConfig{Store: st, Target: SubAgentTarget{Model: "child"}})
	res, err := tool.Execute(context.Background(), "ag-1", json.RawMessage(`{"prompt":"go"}`), nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got := agentcore.ContentToText(res.Content); got != "the final report" {
		t.Errorf("completed result = %q, want the verbatim body", got)
	}
	env := res.Details.(SubAgentEnvelope)
	if env.Status != SubAgentStatusCompleted || env.NextStep != "" || env.ResumeHint != "" {
		t.Errorf("completed envelope = %+v, want no guidance and no hint", env)
	}
	// The transcript is still written, so an explicit agent_id can resume it.
	if _, meta, err := st.Load("ag-1"); err != nil || meta == nil || meta.Status != SubAgentStatusCompleted {
		t.Errorf("completed run must still persist a resumable transcript: %+v, %v", meta, err)
	}
}

// --- §5.4 row 8: durable across store instances ----------------------------

func TestSubagentStoreDurableAcrossInstances(t *testing.T) {
	root := filepath.Join(testenv.Dir(t), "sessions")
	first := NewSubagentStore(root)
	first.BindSession("sess-durable")
	writeSettled(t, first, "ag-1", agentcore.MessageList{userMsg("a"), assistantMsg("b")}, SubAgentStatusFailed, "max_tokens")

	second := NewSubagentStore(root)
	second.BindSession("sess-durable")
	msgs, meta, err := second.Load("ag-1")
	if err != nil {
		t.Fatalf("Load from a fresh store instance: %v", err)
	}
	if meta == nil || meta.StopReason != "max_tokens" || len(msgs) != 2 {
		t.Errorf("fresh store Load = (%d messages, %+v), want the persisted record", len(msgs), meta)
	}
	if meta.Status != SubAgentStatusFailed {
		t.Errorf("meta.Status = %q, want the persisted terminal status", meta.Status)
	}
}

// --- §5.4 row 9: ownership isolation + still-running refusal ---------------

func TestSubagentResumeOwnershipAndStillRunning(t *testing.T) {
	root := filepath.Join(testenv.Dir(t), "sessions")
	writer := NewSubagentStore(root)
	writer.BindSession("sess-a")
	writeSettled(t, writer, "ag-1", agentcore.MessageList{userMsg("a"), assistantMsg("b")}, SubAgentStatusFailed, "error")

	t.Run("cross-session sidecar is invisible", func(t *testing.T) {
		other := NewSubagentStore(root)
		other.BindSession("sess-b")
		msgs, meta, err := other.Load("ag-1")
		if err != nil || msgs != nil || meta != nil {
			t.Fatalf("another session's Load = (%v, %+v, %v), want invisible", msgs, meta, err)
		}
		child := &fauxProvider{name: "child", turns: []fauxTurn{textTurn("nope")}}
		tool := NewTaskToolWithConfig(childFactory(child), nil, TaskToolConfig{Store: other, Target: SubAgentTarget{Model: "child"}})
		_, err = tool.Execute(context.Background(), "ag-2", json.RawMessage(`{"prompt":"go","resume":"ag-1"}`), nil)
		if err == nil || !strings.Contains(err.Error(), "no such sub-agent") {
			t.Errorf("cross-session resume error = %v, want a fail-closed not-found", err)
		}
	})

	t.Run("still-running record is refused", func(t *testing.T) {
		// A non-terminal status stands in for a live agent (v1 writes meta only
		// at settle, so this guards the durable incremental writer).
		if err := writer.Finalize("ag-live", SubagentMeta{Status: "running", Model: "child"}); err != nil {
			t.Fatalf("Finalize: %v", err)
		}
		if err := writer.Append("ag-live", agentcore.MessageList{userMsg("a")}); err != nil {
			t.Fatalf("Append: %v", err)
		}
		child := &fauxProvider{name: "child", turns: []fauxTurn{textTurn("nope")}}
		tool := NewTaskToolWithConfig(childFactory(child), nil, TaskToolConfig{Store: writer, Target: SubAgentTarget{Model: "child"}})
		_, err := tool.Execute(context.Background(), "ag-2", json.RawMessage(`{"prompt":"go","resume":"ag-live"}`), nil)
		if err == nil || !strings.Contains(err.Error(), "still running") {
			t.Errorf("still-running resume error = %v, want a refusal", err)
		}
		if child.callCount() != 0 {
			t.Errorf("a refused resume must not run the child (%d calls)", child.callCount())
		}
	})

	t.Run("missing handle fails closed", func(t *testing.T) {
		child := &fauxProvider{name: "child", turns: []fauxTurn{textTurn("nope")}}
		tool := NewTaskToolWithConfig(childFactory(child), nil, TaskToolConfig{Store: writer, Target: SubAgentTarget{Model: "child"}})
		_, err := tool.Execute(context.Background(), "ag-2", json.RawMessage(`{"prompt":"go","resume":"ag-does-not-exist"}`), nil)
		if err == nil || !strings.Contains(err.Error(), "no such sub-agent") {
			t.Errorf("unknown handle error = %v, want a fail-closed not-found", err)
		}
	})

	t.Run("nil store fails closed", func(t *testing.T) {
		child := &fauxProvider{name: "child", turns: []fauxTurn{textTurn("nope")}}
		tool := NewTaskToolWithConfig(childFactory(child), nil, TaskToolConfig{Target: SubAgentTarget{Model: "child"}})
		_, err := tool.Execute(context.Background(), "ag-2", json.RawMessage(`{"prompt":"go","resume":"ag-1"}`), nil)
		if err == nil || !strings.Contains(err.Error(), "no sub-agent transcript store") {
			t.Errorf("nil-store resume error = %v, want a fail-closed refusal", err)
		}
	})
}

// --- §5.4 row 11: window budget (pass / distill / fail closed) -------------

func bigText(n int) string { return strings.Repeat("x", n) }

func TestSubagentResumeBudget(t *testing.T) {
	newTool := func(summary string) *SubAgentTool {
		child := &fauxProvider{name: "child", turns: []fauxTurn{textTurn(summary)}}
		return NewTaskToolWithConfig(childFactory(child), nil, TaskToolConfig{Target: SubAgentTarget{Model: "child"}})
	}
	// The summarizer rejects degenerate (<500 char) output, so use a long one.
	summary := strings.Repeat("summary detail. ", 60)

	t.Run("within budget passes through", func(t *testing.T) {
		tool := newTool(summary)
		msgs := agentcore.MessageList{userMsg(bigText(4000)), assistantMsg(bigText(4000))}
		got, err := tool.applyResumeBudget(context.Background(), msgs, RunConfig{}, 1_000_000, "new prompt")
		if err != nil {
			t.Fatalf("applyResumeBudget: %v", err)
		}
		if len(got) != len(msgs) {
			t.Errorf("within-budget prefix changed: %d -> %d messages", len(msgs), len(got))
		}
	})

	t.Run("over budget distills the older history", func(t *testing.T) {
		tool := newTool(summary)
		// 5 messages of ~12.5k tokens each = ~62k tokens; window 60000 -> budget
		// 57000, so the prefix is over and must be distilled.
		msgs := agentcore.MessageList{
			userMsg(bigText(50000)), assistantMsg(bigText(50000)), userMsg(bigText(50000)),
			assistantMsg(bigText(50000)), userMsg(bigText(50000)),
		}
		cfg := RunConfig{LoopConfig: LoopConfig{Model: "child", Stream: provider.StreamFnFromProvider(&fauxProvider{name: "child", turns: []fauxTurn{textTurn(summary)}})}}
		got, err := tool.applyResumeBudget(context.Background(), msgs, cfg, 60000, "new prompt")
		if err != nil {
			t.Fatalf("applyResumeBudget: %v", err)
		}
		if len(got) >= len(msgs) {
			t.Fatalf("distillation did not shrink the prefix: %d -> %d", len(msgs), len(got))
		}
		if _, ok := got[0].(agentcore.CompactionMessage); !ok {
			t.Errorf("distilled prefix must start with a compaction summary, got %T", got[0])
		}
	})

	t.Run("nothing distillable fails closed", func(t *testing.T) {
		tool := newTool(summary)
		// One oversized message: no cut point exists, so Compact returns nil and
		// the resume must be refused with guidance rather than silently coming
		// over-window.
		msgs := agentcore.MessageList{userMsg(bigText(100000))}
		_, err := tool.applyResumeBudget(context.Background(), msgs, RunConfig{}, 20000, "new prompt")
		if err == nil || !strings.Contains(err.Error(), "refused") {
			t.Fatalf("applyResumeBudget error = %v, want a fail-closed refusal", err)
		}
	})

	t.Run("still over budget after distillation fails closed", func(t *testing.T) {
		tool := newTool(summary)
		// The retained tail (the newest oversized message) alone exceeds the
		// budget, so distillation cannot bring the prefix under it.
		msgs := agentcore.MessageList{
			userMsg(bigText(60000)), assistantMsg(bigText(60000)), userMsg(bigText(100000)),
		}
		cfg := RunConfig{LoopConfig: LoopConfig{Model: "child", Stream: provider.StreamFnFromProvider(&fauxProvider{name: "child", turns: []fauxTurn{textTurn(summary)}})}}
		_, err := tool.applyResumeBudget(context.Background(), msgs, cfg, 20000, "new prompt")
		if err == nil || !strings.Contains(err.Error(), "refused") {
			t.Fatalf("applyResumeBudget error = %v, want a fail-closed refusal after distillation", err)
		}
		if err != nil && !strings.Contains(err.Error(), "after distillation") {
			t.Errorf("error should name the post-distillation overflow: %v", err)
		}
	})
}

// --- §5.4 row 12: store unit contract -------------------------------------

func TestSubagentStoreRoundTrip(t *testing.T) {
	st := resumeStore(t, "sess-unit")
	msgs := agentcore.MessageList{
		userMsg("hello <system-reminder>x</system-reminder>"),
		assistantMsg("working"),
		toolCallMsg("c1", "read"),
		toolResultMsg("c1", "read", "contents"),
	}
	if err := st.Append("ag-1", msgs); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := st.Finalize("ag-1", SubagentMeta{Status: SubAgentStatusFailed, StopReason: "error", Model: "m", Provider: "p", Messages: len(msgs)}); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	got, meta, err := st.Load("ag-1")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if meta == nil || meta.Model != "m" || meta.Provider != "p" || meta.Messages != len(msgs) {
		t.Errorf("meta = %+v", meta)
	}
	if len(got) != len(msgs) {
		t.Fatalf("round trip = %d messages, want %d", len(got), len(msgs))
	}
	for i := range msgs {
		if got[i].Role() != msgs[i].Role() {
			t.Errorf("message[%d] role = %s, want %s", i, got[i].Role(), msgs[i].Role())
		}
	}
	if u, ok := got[0].(agentcore.UserMessage); !ok || !strings.Contains(agentcore.ContentToText(u.Content), "<system-reminder>") {
		t.Errorf("message[0] lost its content: %+v", got[0])
	}
	if tr, ok := got[3].(agentcore.ToolResultMessage); !ok || tr.ToolCallID != "c1" || agentcore.ContentToText(tr.Content) != "contents" {
		t.Errorf("message[3] = %+v, want the tool result", got[3])
	}
}

func TestSubagentStoreTornTail(t *testing.T) {
	st := resumeStore(t, "sess-torn")
	dir := st.agentDir("sess-torn", "ag-1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	good := `{"role":"user","content":[{"type":"text","text":"first"}]}`
	other := `{"role":"assistant","content":[{"type":"text","text":"second"}]}`
	path := filepath.Join(dir, subagentTranscriptFile)

	t.Run("torn final line is dropped", func(t *testing.T) {
		if err := os.WriteFile(path, []byte(good+"\n"+`{"role":"assist`), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		msgs, _, err := st.Load("ag-1")
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if len(msgs) != 1 {
			t.Fatalf("torn tail load = %d messages, want the 1-message prefix", len(msgs))
		}
	})

	t.Run("corrupt middle line is an error", func(t *testing.T) {
		if err := os.WriteFile(path, []byte(good+"\n"+`{"role":"assist`+"\n"+other+"\n"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		if _, _, err := st.Load("ag-1"); err == nil || !strings.Contains(err.Error(), "corrupt") {
			t.Fatalf("Load error = %v, want a corruption error", err)
		}
	})

	t.Run("missing transcript is not an error", func(t *testing.T) {
		msgs, meta, err := st.Load("ag-absent")
		if err != nil || msgs != nil || meta != nil {
			t.Fatalf("Load(missing) = (%v, %+v, %v), want not-found", msgs, meta, err)
		}
	})
}

func TestSubagentStoreUnboundSkipsWrites(t *testing.T) {
	root := filepath.Join(testenv.Dir(t), "sessions")
	st := NewSubagentStore(root)
	if err := st.Append("ag-1", agentcore.MessageList{userMsg("a")}); err != nil {
		t.Fatalf("unbound Append: %v", err)
	}
	if err := st.Finalize("ag-1", SubagentMeta{Status: SubAgentStatusCompleted}); err != nil {
		t.Fatalf("unbound Finalize: %v", err)
	}
	if entries, err := os.ReadDir(root); err == nil && len(entries) > 0 {
		t.Errorf("an unbound store wrote %d entries under the root", len(entries))
	}
}

// --- task schema / description -------------------------------------------

func TestTaskSchemaAdvertisesResume(t *testing.T) {
	tool := NewTaskTool(func() RunConfig { return RunConfig{} }, nil)
	var schema struct {
		Properties struct {
			Resume json.RawMessage `json:"resume"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(tool.Schema(), &schema); err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}
	if len(schema.Properties.Resume) == 0 {
		t.Error("task schema must declare the optional resume property")
	}
	if !strings.Contains(tool.Description(), "resume=") {
		t.Error("task description must teach the resume argument")
	}
}

// TestSubagentStoreSidecarSuffix pins the literal the session package duplicates
// when it removes a session's sidecar (the packages do not import each other).
func TestSubagentStoreSidecarSuffix(t *testing.T) {
	if SubagentsSidecarSuffix != ".subagents" {
		t.Errorf("SubagentsSidecarSuffix = %q, want .subagents (session.Store.Delete mirrors it)", SubagentsSidecarSuffix)
	}
}
