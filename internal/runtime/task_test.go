package runtime

// Tests for the generic task tool (US-002/003/004, #454): its identity/schema
// contract, the shared concurrency semaphore (N > cap never exceeds cap), the
// nesting guard (child tool set excludes "task"), that a task returns the
// child's final text, and that a failed child surfaces as a tool error. The
// child loop is driven through the faux provider seam (mirrors orchestration_test.go);
// only the provider boundary is faked.

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/agenttool"
	"github.com/smallnest/pigo/internal/provider"
)

// TestTaskToolContract pins the tool identity, parallel execution mode, and the
// {description?, prompt} schema with prompt required.
func TestTaskToolContract(t *testing.T) {
	tool := NewTaskTool(func() RunConfig { return RunConfig{} }, nil)
	if tool.Name() != "task" {
		t.Errorf("Name() = %q, want task", tool.Name())
	}
	if tool.ExecutionMode() != agentcore.ToolExecutionParallel {
		t.Errorf("ExecutionMode() = %v, want parallel", tool.ExecutionMode())
	}
	var schema struct {
		Properties struct {
			Description json.RawMessage `json:"description"`
			Prompt      json.RawMessage `json:"prompt"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(tool.Schema(), &schema); err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}
	if len(schema.Properties.Prompt) == 0 || len(schema.Properties.Description) == 0 {
		t.Errorf("schema must declare both prompt and description properties")
	}
	if len(schema.Required) != 1 || schema.Required[0] != "prompt" {
		t.Errorf("required = %v, want [prompt]", schema.Required)
	}
}

// TestTaskReturnsChildText verifies a dispatched task drives an independent child
// loop and returns the child's final assistant text as the tool result.
func TestTaskReturnsChildText(t *testing.T) {
	child := &fauxProvider{
		name:   "faux-child",
		models: []provider.Model{{Provider: "faux-child", ID: "child"}},
		turns:  []fauxTurn{textTurn("child final report")},
	}
	factory := func() RunConfig {
		return RunConfig{
			LoopConfig: LoopConfig{Model: "child", Stream: provider.StreamFnFromProvider(child)},
			Batch:      agenttool.BatchConfig{ToolExecutorConfig: agenttool.ToolExecutorConfig{Registry: agenttool.NewToolRegistry()}},
		}
	}
	tool := NewTaskTool(factory, nil)
	res, err := tool.Execute(context.Background(), "id", json.RawMessage(`{"description":"do x","prompt":"do the work"}`), nil)
	if err != nil {
		t.Fatalf("Execute err = %v", err)
	}
	if got := agentcore.ContentToText(res.Content); got != "child final report" {
		t.Errorf("task result = %q, want 'child final report'", got)
	}
	if child.callCount() != 1 {
		t.Errorf("child provider calls = %d, want 1", child.callCount())
	}
}

// TestTaskFailedChildErrors verifies a child whose final turn stops on error is
// surfaced to the parent as a structured T5.1 envelope (a normal tool result
// carrying status/stop_reason/next_step), not a silent success and not an
// opaque Go-error string.
func TestTaskFailedChildErrors(t *testing.T) {
	// A child turn ending on StopReason=error, carrying diagnostic text as content
	// (executeGoroutine surfaces the child's Content on failure).
	errTurn := func(text string) fauxTurn {
		partial := agentcore.AssistantMessage{RoleField: agentcore.RoleAssistant}
		withText := partial
		withText.Content = agentcore.ContentList{agentcore.NewTextContent(text)}
		final := withText
		final.StopReason = agentcore.StopReasonError
		return fauxTurn{
			provider.StreamStartEvent{Partial: partial},
			provider.StreamTextEvent{Partial: withText},
			provider.StreamDoneEvent{Message: final},
		}
	}
	child := &fauxProvider{
		name:   "faux-child",
		models: []provider.Model{{Provider: "faux-child", ID: "child"}},
		turns:  []fauxTurn{errTurn("child exploded")},
	}
	factory := func() RunConfig {
		return RunConfig{
			LoopConfig: LoopConfig{Model: "child", Stream: provider.StreamFnFromProvider(child)},
			Batch:      agenttool.BatchConfig{ToolExecutorConfig: agenttool.ToolExecutorConfig{Registry: agenttool.NewToolRegistry()}},
		}
	}
	tool := NewTaskTool(factory, nil)
	res, err := tool.Execute(context.Background(), "id", json.RawMessage(`{"prompt":"go"}`), nil)
	if err != nil {
		t.Fatalf("a failed child must be a normal (envelope) result, got tool error %v", err)
	}
	got := agentcore.ContentToText(res.Content)
	for _, want := range []string{
		"[subagent result]",
		"agent_id: id",
		"status: failed",
		"stop_reason: error",
		"next_step:",
		"child exploded",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("envelope result missing %q:\n%s", want, got)
		}
	}
	// The envelope also rides in Details for structured consumers (TUI).
	env, ok := res.Details.(SubAgentEnvelope)
	if !ok {
		t.Fatalf("Details = %T, want SubAgentEnvelope", res.Details)
	}
	if env.Status != SubAgentStatusFailed || env.StopReason != "error" || env.AgentID != "id" {
		t.Errorf("Details envelope = %+v", env)
	}
}

// TestTaskSemaphoreBoundsConcurrency dispatches N tasks concurrently through a
// shared semaphore of capacity cap (< N) and asserts the number of children
// running at once never exceeds cap. Each child calls a blocking fake tool that
// parks on a barrier, so all admitted children pile up simultaneously and the
// peak concurrency is observable.
func TestTaskSemaphoreBoundsConcurrency(t *testing.T) {
	const capN, n = 2, 6
	sem := make(chan struct{}, capN)

	var running, peak int64
	release := make(chan struct{})
	// blockTool parks until the test closes release, holding a semaphore slot for
	// the duration and recording the peak number of concurrent children.
	blockTool := execTool{
		name: "block",
		run: func(ctx context.Context, id string, args json.RawMessage, onUpdate agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
			cur := atomic.AddInt64(&running, 1)
			for {
				p := atomic.LoadInt64(&peak)
				if cur <= p || atomic.CompareAndSwapInt64(&peak, p, cur) {
					break
				}
			}
			defer atomic.AddInt64(&running, -1)
			select {
			case <-release:
			case <-ctx.Done():
			}
			return agentcore.AgentToolResult{Content: agentcore.ContentList{agentcore.NewTextContent("blocked")}}, nil
		},
	}
	// Each child runs one turn that calls the blocking tool, then (after release)
	// a final text turn.
	factory := func() RunConfig {
		p := &fauxProvider{
			name:   "faux-child",
			models: []provider.Model{{Provider: "c", ID: "c"}},
			turns:  []fauxTurn{toolCallTurn("t", "block", `{}`), textTurn("done")},
		}
		reg := agenttool.NewToolRegistry()
		_ = reg.Register(blockTool)
		return RunConfig{
			LoopConfig: LoopConfig{Model: "c", Stream: provider.StreamFnFromProvider(p)},
			Batch:      agenttool.BatchConfig{ToolExecutorConfig: agenttool.ToolExecutorConfig{Registry: reg}},
		}
	}
	tool := NewTaskTool(factory, sem)

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = tool.Execute(context.Background(), "id", json.RawMessage(`{"prompt":"go"}`), nil)
		}()
	}
	// Give the admitted children time to reach the barrier, then let them go.
	deadline := time.After(2 * time.Second)
	for atomic.LoadInt64(&running) < int64(capN) {
		select {
		case <-deadline:
			t.Fatalf("only %d children started, expected the semaphore to admit %d", atomic.LoadInt64(&running), capN)
		default:
			time.Sleep(time.Millisecond)
		}
	}
	// Hold briefly so any over-admission (a semaphore bug) would push peak > cap.
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()

	if got := atomic.LoadInt64(&peak); got > int64(capN) {
		t.Errorf("peak concurrent children = %d, must not exceed cap %d", got, capN)
	}
	if got := atomic.LoadInt64(&peak); got == 0 {
		t.Error("no child ever ran; the semaphore blocked everything")
	}
}

// TestTaskAdvertisesRegistryTools verifies the child sub-agent is told about the
// tools it can actually run: when the spec pins no explicit tool set, the child
// context's Tools are populated from the run config's registry. Without this the
// model receives an empty tool list and cannot do real work (the "non-functional
// sub-agent" bug), so this guards the wiring, not just the result.
func TestTaskAdvertisesRegistryTools(t *testing.T) {
	// Capture the tools the provider is handed for the child request.
	var gotTools []agentcore.AgentTool
	capturing := provider.StreamFn(func(ctx context.Context, model string, llm provider.LlmContext, cfg provider.StreamConfig) (*provider.AssistantMessageEventStream, error) {
		gotTools = llm.Tools
		child := &fauxProvider{
			name:   "faux-child",
			models: []provider.Model{{Provider: "faux-child", ID: "child"}},
			turns:  []fauxTurn{textTurn("done")},
		}
		return provider.StreamFnFromProvider(child)(ctx, model, llm, cfg)
	})
	reg := agenttool.NewToolRegistry()
	_ = reg.Register(echoTool("read", agentcore.ToolExecutionParallel, false))
	_ = reg.Register(echoTool("bash", agentcore.ToolExecutionParallel, false))
	factory := func() RunConfig {
		return RunConfig{
			LoopConfig: LoopConfig{Model: "child", Stream: capturing},
			Batch:      agenttool.BatchConfig{ToolExecutorConfig: agenttool.ToolExecutorConfig{Registry: reg}},
		}
	}
	tool := NewTaskTool(factory, nil)
	if _, err := tool.Execute(context.Background(), "id", json.RawMessage(`{"prompt":"go"}`), nil); err != nil {
		t.Fatalf("Execute err = %v", err)
	}
	if len(gotTools) != 2 {
		t.Fatalf("child was advertised %d tools, want 2 (from the registry)", len(gotTools))
	}
	names := map[string]bool{gotTools[0].Name(): true, gotTools[1].Name(): true}
	if !names["read"] || !names["bash"] {
		t.Errorf("child tools = %v, want read+bash from the registry", names)
	}
}

// stopTurn scripts one faux child turn that ends on the given raw stop reason
// with the given content text and (optionally) ErrorMessage, driving the T5.1
// envelope table-driven acceptance cases.
func stopTurn(reason, text, errMsg string) fauxTurn {
	partial := agentcore.AssistantMessage{RoleField: agentcore.RoleAssistant}
	if text != "" {
		partial.Content = agentcore.ContentList{agentcore.NewTextContent(text)}
	}
	final := partial
	final.StopReason = reason
	final.ErrorMessage = errMsg
	evs := []provider.AssistantMessageEvent{provider.StreamStartEvent{Partial: partial}}
	if text != "" {
		evs = append(evs, provider.StreamTextEvent{Partial: final})
	}
	return fauxTurn(append(evs, provider.StreamDoneEvent{Message: final}))
}

// TestTaskEnvelopeOutcome drives the T5.1 acceptance table: every settled stop
// reason maps to its documented envelope outcome (status/stop_reason/next_step)
// on the goroutine path. Table rows mirror wiki/port/subagent-result-envelope.md
// §6.1 (kimi NEXT_STEP_BY_REASON, adapted for the v1 no-resume wording).
func TestTaskEnvelopeOutcome(t *testing.T) {
	factoryFor := func(turn fauxTurn) func() RunConfig {
		child := &fauxProvider{
			name:   "faux-child",
			models: []provider.Model{{Provider: "faux-child", ID: "child"}},
			turns:  []fauxTurn{turn},
		}
		return func() RunConfig {
			return RunConfig{
				LoopConfig: LoopConfig{Model: "child", Stream: provider.StreamFnFromProvider(child)},
				Batch:      agenttool.BatchConfig{ToolExecutorConfig: agenttool.ToolExecutorConfig{Registry: agenttool.NewToolRegistry()}},
			}
		}
	}

	cases := []struct {
		name       string
		turn       fauxTurn
		wantStatus string
		wantReason string
		// wantBody is asserted as contained in the result text ("" = skip).
		wantBody string
		// wantTextIsBody marks the completed contract: the result text equals the
		// body verbatim with NO envelope header (byte-identical happy path).
		wantTextIsBody bool
		wantNextStepFrags []string
	}{
		{
			name:           "completed end_turn returns text verbatim",
			turn:           stopTurn(agentcore.StopReasonEndTurn, "the report", ""),
			wantStatus:     SubAgentStatusCompleted,
			wantReason:     "completed",
			wantTextIsBody: true,
			wantBody:       "the report",
		},
		// NOTE: the raw "length" stop reason is intentionally absent from this
		// full-loop table — the child loop itself consumes length (it fails
		// truncated tool calls and resends, loop.go StopReasonLength branch), so
		// it cannot reach the task settle point through a real child run. The
		// length->max_tokens mapping is pinned at the unit level instead
		// (envelope_test.go TestStopReasonOf / TestNextStepFor).
		{
			name:              "aborted maps to cancelled",
			turn:              stopTurn(agentcore.StopReasonAborted, "", "aborted"),
			wantStatus:        SubAgentStatusFailed,
			wantReason:        "cancelled",
			wantBody:          "aborted",
			wantNextStepFrags: []string{"stopped by the user", "Do not restart"},
		},
		{
			name:              "end_turn without text maps to no_final_message",
			turn:              stopTurn(agentcore.StopReasonEndTurn, "", ""),
			wantStatus:        SubAgentStatusFailed,
			wantReason:        "no_final_message",
			wantNextStepFrags: []string{"no final report"},
		},
		{
			name:              "error without content surfaces ErrorMessage",
			turn:              stopTurn(agentcore.StopReasonError, "", "provider connection refused"),
			wantStatus:        SubAgentStatusFailed,
			wantReason:        "error",
			wantBody:          "provider connection refused",
			wantNextStepFrags: []string{"Re-dispatch"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tool := NewTaskTool(factoryFor(tc.turn), nil)
			res, err := tool.Execute(context.Background(), "id", json.RawMessage(`{"prompt":"go"}`), nil)
			if err != nil {
				t.Fatalf("Execute err = %v, want a normal (envelope) result", err)
			}
			env, ok := res.Details.(SubAgentEnvelope)
			if !ok {
				t.Fatalf("Details = %T, want SubAgentEnvelope", res.Details)
			}
			if env.Status != tc.wantStatus || env.StopReason != tc.wantReason || env.AgentID != "id" {
				t.Errorf("envelope = %+v, want status=%s reason=%s agent_id=id", env, tc.wantStatus, tc.wantReason)
			}
			got := agentcore.ContentToText(res.Content)
			if tc.wantTextIsBody {
				if got != tc.wantBody {
					t.Errorf("completed result = %q, want verbatim body %q (no envelope header)", got, tc.wantBody)
				}
				if env.NextStep != "" {
					t.Errorf("completed next_step = %q, want empty", env.NextStep)
				}
				return
			}
			for _, frag := range []string{"[subagent result]", "status: " + tc.wantStatus, "stop_reason: " + tc.wantReason} {
				if !strings.Contains(got, frag) {
					t.Errorf("result missing %q:\n%s", frag, got)
				}
			}
			for _, frag := range tc.wantNextStepFrags {
				if !strings.Contains(env.NextStep, frag) {
					t.Errorf("next_step %q missing %q", env.NextStep, frag)
				}
			}
			if tc.wantBody != "" && !strings.Contains(got, tc.wantBody) {
				t.Errorf("result missing body fragment %q:\n%s", tc.wantBody, got)
			}
		})
	}
}
