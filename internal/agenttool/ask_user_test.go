package agenttool

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/questionnaire"
)

// toolText extracts the first text part of a tool result (helper for asserts).
func toolText(t *testing.T, r agentcore.AgentToolResult) string {
	t.Helper()
	for _, c := range r.Content {
		if tc, ok := c.(agentcore.TextContent); ok {
			return tc.Text
		}
	}
	t.Fatal("result has no text content")
	return ""
}

func askInput(t *testing.T, raw string) json.RawMessage {
	t.Helper()
	return json.RawMessage(raw)
}

func TestAskUserDegradedAutoAnswer(t *testing.T) {
	tool := &AskUserTool{} // nil port → degraded
	res, err := tool.Execute(context.Background(), "t1", askInput(t, `{
		"title": "Deploy",
		"steps": [
			{"question": "Strategy?", "options": [
				{"label": "Blue-green", "recommended": true},
				{"label": "Canary"}]},
			{"question": "Notes?"}
		]}`), nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	text := toolText(t, res)
	if !strings.Contains(text, "[source: degraded") {
		t.Errorf("degraded not flagged:\n%s", text)
	}
	if !strings.Contains(text, "Blue-green") {
		t.Errorf("recommended option not auto-picked:\n%s", text)
	}
	if !strings.Contains(text, "(skipped)") {
		t.Errorf("free-text step should degrade to skip:\n%s", text)
	}
	if details, ok := res.Details.(map[string]any); !ok || details["source"] != questionnaire.SourceDegraded {
		t.Errorf("details = %#v, want source=degraded", res.Details)
	}
}

func TestAskUserDegradedRequiresExplicitResponse(t *testing.T) {
	tool := &AskUserTool{}
	res, err := tool.Execute(context.Background(), "t1", askInput(t, `{
		"requires_explicit_response": true,
		"steps": [{"question": "Delete prod?"}]
	}`), nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(toolText(t, res), "no interactive user") {
		t.Errorf("guidance missing:\n%s", toolText(t, res))
	}
}

func TestAskUserInvalidInputIsErrorResult(t *testing.T) {
	tool := &AskUserTool{}
	for _, raw := range []string{`{}`, `{"steps": []}`, `{"steps": [{"question": ""}]}`} {
		res, err := tool.Execute(context.Background(), "t1", askInput(t, raw), nil)
		if err != nil {
			t.Fatalf("Execute(%s): %v", raw, err)
		}
		if !strings.Contains(toolText(t, res), "ask_user:") {
			t.Errorf("input %s should degrade to an ask_user error result, got %q", raw, toolText(t, res))
		}
	}
}

func TestAskUserPortRoundTripAndCancel(t *testing.T) {
	port := &stubPort{mu: sync.Mutex{}}
	tool := &AskUserTool{Port: port}
	ctx, cancel := context.WithCancel(context.Background())

	// Cancel before Ask: the tool must surface "not answered" instead of
	// returning auto-answers.
	cancel()
	res, err := tool.Execute(ctx, "t1", askInput(t, `{"steps": [{"question": "q", "options": [{"label": "a"}]}]}`), nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(toolText(t, res), "not answered") {
		t.Errorf("cancelled ask should report not-answered:\n%s", toolText(t, res))
	}

	// A live port receives the normalized questionnaire and its reply is
	// rendered verbatim.
	port.reply = questionnaire.Reply{
		Source: questionnaire.SourceUser,
		Answers: []questionnaire.Answer{
			{StepID: "q1", OtherText: "canary with 10%"},
		},
	}
	res, err = tool.Execute(context.Background(), "t2", askInput(t, `{"steps": [{"question": "Strategy?", "options": [{"label": "a"}]}]}`), nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(toolText(t, res), `"canary with 10%" (other)`) {
		t.Errorf("port reply not rendered:\n%s", toolText(t, res))
	}
	if port.got == nil || port.got.Steps[0].ID != "q1" {
		t.Errorf("port should receive the normalized questionnaire, got %+v", port.got)
	}
	if port.got != nil && port.got.Steps[0].Options[0].ID != "o1" {
		t.Errorf("option id not normalized before the port: %+v", port.got.Steps[0].Options)
	}
}

// stubPort records the questionnaire and returns a canned reply.
type stubPort struct {
	mu    sync.Mutex
	reply questionnaire.Reply
	got   *questionnaire.Questionnaire
}

func (p *stubPort) Ask(ctx context.Context, q questionnaire.Questionnaire) (questionnaire.Reply, error) {
	// The port contract: an already-cancelled ctx surfaces as an error (the
	// tool then reports "not answered" instead of auto-answers).
	if err := ctx.Err(); err != nil {
		return questionnaire.Reply{}, err
	}
	p.mu.Lock()
	p.got = &q
	p.mu.Unlock()
	return p.reply, nil
}

func TestAskUserToolShape(t *testing.T) {
	tool := &AskUserTool{}
	if tool.Name() != "ask_user" {
		t.Errorf("name = %q", tool.Name())
	}
	if tool.ExecutionMode() != agentcore.ToolExecutionSequential {
		t.Errorf("ask_user must be sequential (shared stdin/panel channel)")
	}
	var schema map[string]any
	if err := json.Unmarshal(tool.Schema(), &schema); err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}
	props, _ := schema["properties"].(map[string]any)
	if props == nil || props["steps"] == nil {
		t.Fatal("schema must declare steps")
	}
}
