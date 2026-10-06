package agenttool

// Tests for the ToolEffect contract consumption (T5.2): a tool that declares
// Effect().Timeout gets its execute-phase context wrapped in WithTimeout;
// a tool without a timeout keeps the caller's context untouched.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/smallnest/pigo/internal/agentcore"
)

type slowTool struct {
	name    string
	timeout time.Duration
}

func (s *slowTool) Name() string        { return s.name }
func (s *slowTool) Description() string { return "slow " + s.name }
func (s *slowTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object"}`)
}
func (s *slowTool) ExecutionMode() agentcore.ToolExecutionMode {
	return agentcore.ToolExecutionSequential
}
func (s *slowTool) Effect() agentcore.ToolEffect {
	return agentcore.ToolEffect{Timeout: s.timeout}
}
func (s *slowTool) Execute(ctx context.Context, id string, args json.RawMessage, onUpdate agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
	select {
	case <-ctx.Done():
		return agentcore.AgentToolResult{}, ctx.Err()
	case <-time.After(2 * time.Second):
		return agentcore.AgentToolResult{}, nil
	}
}

func TestEffectTimeoutWrapsExecute(t *testing.T) {
	if testing.Short() {
		t.Skip("sleep-based timing test")
	}
	reg := NewToolRegistry()
	if err := reg.Register(&slowTool{name: "slow_declared", timeout: 30 * time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	cfg := ToolExecutorConfig{Registry: reg}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	msg, _ := executeToolCall(ctx, cfg, agentcore.AgentToolCall{ID: "c1", Name: "slow_declared", Arguments: json.RawMessage(`{}`)}, nil)
	text := ""
	for _, c := range msg.Content {
		if tc, ok := c.(agentcore.TextContent); ok {
			text += tc.Text
		}
	}
	if !msg.IsError || !strings.Contains(text, "deadline exceeded") && !strings.Contains(text, "canceled") {
		t.Errorf("declared timeout must surface as an error result, got isError=%v text=%q", msg.IsError, text)
	}
}

type deadlineProbeTool struct {
	slowTool
	sawDeadline bool
}

func (d *deadlineProbeTool) Execute(ctx context.Context, id string, args json.RawMessage, onUpdate agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
	_, d.sawDeadline = ctx.Deadline()
	return agentcore.AgentToolResult{}, nil
}

// TestEffectNoTimeoutUnwrapped: timeout = 0 means "the tool manages its own
// deadlines" — the executor must NOT wrap the context, so the tool observes
// no deadline even when the caller's context has none.
func TestEffectNoTimeoutUnwrapped(t *testing.T) {
	probe := &deadlineProbeTool{slowTool: slowTool{name: "probe", timeout: 0}}
	reg := NewToolRegistry()
	if err := reg.Register(probe); err != nil {
		t.Fatal(err)
	}
	msg, _ := executeToolCall(context.Background(), ToolExecutorConfig{Registry: reg},
		agentcore.AgentToolCall{ID: "c2", Name: "probe", Arguments: json.RawMessage(`{}`)}, nil)
	if msg.IsError {
		t.Fatalf("probe run errored: %v", msg.Content)
	}
	if probe.sawDeadline {
		t.Error("zero Timeout must not wrap the execute context in a deadline")
	}
}
