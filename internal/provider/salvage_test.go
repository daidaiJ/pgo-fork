package provider

import (
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
)

// toolCallMsg assembles an assistant message with a length stop and the given
// content blocks, the shape the decoders hand the salvage.
func toolCallMsg(content ...agentcore.Content) *agentcore.AssistantMessage {
	return &agentcore.AssistantMessage{
		RoleField:  agentcore.RoleAssistant,
		StopReason: agentcore.StopReasonLength,
		Content:    content,
	}
}

func TestSalvageRewritesCompleteToolCalls(t *testing.T) {
	msg := toolCallMsg(
		agentcore.NewTextContent("let me check"),
		agentcore.NewToolCallContent("c1", "read", []byte(`{"path":"a.go"}`)),
		agentcore.NewTextContent(`{"partial`), // 残尾: cut mid-sentence/JSON
	)
	if !SalvageLengthTruncation(msg) {
		t.Fatal("complete tool calls under a length stop must be salvaged")
	}
	if msg.StopReason != agentcore.StopReasonToolUse {
		t.Errorf("stop reason = %q, want tool_use", msg.StopReason)
	}
	if len(msg.Content) != 2 {
		t.Fatalf("tail not dropped: %d content blocks, want 2", len(msg.Content))
	}
	if _, ok := msg.Content[1].(agentcore.ToolCallContent); !ok {
		t.Errorf("last block = %T, want the tool call", msg.Content[1])
	}
}

func TestSalvageDropsTailAfterLastToolCall(t *testing.T) {
	msg := toolCallMsg(
		agentcore.NewToolCallContent("c1", "read", nil), // empty args = complete
		agentcore.NewToolCallContent("c2", "grep", []byte(`{}`)),
		agentcore.NewTextContent("trunca"),
	)
	if !SalvageLengthTruncation(msg) {
		t.Fatal("empty-arguments tool calls must count as complete")
	}
	if len(msg.Content) != 2 {
		t.Errorf("content = %d blocks, want the 2 tool calls only", len(msg.Content))
	}
}

func TestSalvageKeepsPlainTruncatedText(t *testing.T) {
	msg := toolCallMsg(agentcore.NewTextContent("half an answe"))
	if SalvageLengthTruncation(msg) {
		t.Error("a truncated answer without tool calls must stay length")
	}
	if msg.StopReason != agentcore.StopReasonLength {
		t.Errorf("stop reason = %q, want length", msg.StopReason)
	}
}

func TestSalvageRefusesPartialArguments(t *testing.T) {
	msg := toolCallMsg(
		agentcore.NewToolCallContent("c1", "write", []byte(`{"path":"a.go","content":"cut of`)),
	)
	if SalvageLengthTruncation(msg) {
		t.Error("cut-off JSON arguments must not be salvaged (no CompletePartial)")
	}
	if msg.StopReason != agentcore.StopReasonLength {
		t.Errorf("stop reason = %q, want length", msg.StopReason)
	}
}

func TestSalvageRefusesUnnamedToolCall(t *testing.T) {
	msg := toolCallMsg(agentcore.NewToolCallContent("c1", "", []byte(`{}`)))
	if SalvageLengthTruncation(msg) {
		t.Error("a tool call without a name is not dispatchable and must stay length")
	}
}

func TestSalvageIgnoresOtherStopReasons(t *testing.T) {
	msg := &agentcore.AssistantMessage{
		RoleField:  agentcore.RoleAssistant,
		StopReason: agentcore.StopReasonEndTurn,
		Content:    agentcore.ContentList{agentcore.NewToolCallContent("c1", "read", []byte(`{}`))},
	}
	if SalvageLengthTruncation(msg) {
		t.Error("only a length stop may be rewritten")
	}
	if msg.StopReason != agentcore.StopReasonEndTurn {
		t.Errorf("stop reason mutated: %q", msg.StopReason)
	}
}

func TestSalvageNilAndEmptySafe(t *testing.T) {
	if SalvageLengthTruncation(nil) {
		t.Error("nil message must be a no-op")
	}
	if SalvageLengthTruncation(&agentcore.AssistantMessage{StopReason: agentcore.StopReasonLength}) {
		t.Error("an empty message must be a no-op")
	}
}

// --- Decoder integration (the wiring is the point: the salvage rides the
// terminal assembly, so a length stream with complete tool calls settles as an
// executable tool-call turn on both wire protocols). ---

func TestOpenAIDecoderSalvagesLengthWithToolCalls(t *testing.T) {
	body := `data: {"id":"c","model":"m","choices":[{"delta":{"content":"checking"}}]}

data: {"id":"c","model":"m","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"read","arguments":"{\"path\":\"a.go\"}"}}]}}]}

data: {"id":"c","model":"m","choices":[{"delta":{"content":"{\"trunca"}}]}

data: {"id":"c","model":"m","choices":[{"delta":{},"finish_reason":"length"}]}

data: [DONE]

`
	dec := NewOpenAIDecoder()
	_, final := feedSSE(t, dec, body)
	if final.StopReason != agentcore.StopReasonToolUse {
		t.Errorf("salvaged stop reason = %q, want tool_use", final.StopReason)
	}
	// Content: text + tool call; the trailing partial text is dropped.
	if len(final.Content) != 2 {
		t.Fatalf("content = %d blocks, want 2 (text + tool call)", len(final.Content))
	}
}

func TestOpenAIDecoderKeepsLengthOnPartialArguments(t *testing.T) {
	// The arguments VALUE is an incomplete JSON object ({"path": — cut before
	// the value/close), while the wire chunk itself stays parseable.
	body := `data: {"id":"c","model":"m","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"write","arguments":"{\"path\":"}}]}}]}

data: {"id":"c","model":"m","choices":[{"delta":{},"finish_reason":"length"}]}

data: [DONE]

`
	dec := NewOpenAIDecoder()
	_, final := feedSSE(t, dec, body)
	if final.StopReason != agentcore.StopReasonLength {
		t.Errorf("stop reason = %q, want length (arguments cut mid-JSON)", final.StopReason)
	}
}

func TestAnthropicDecoderSalvagesLengthWithToolCalls(t *testing.T) {
	body := `event: message_start
data: {"type":"message_start","message":{"id":"m","model":"m","usage":{"input_tokens":10}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"t1","name":"read"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"a.go\"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"max_tokens"},"usage":{"output_tokens":100}}

event: message_stop
data: {"type":"message_stop"}

`
	dec := NewAnthropicDecoder()
	_, final := feedSSE(t, dec, body)
	if final.StopReason != agentcore.StopReasonToolUse {
		t.Errorf("salvaged stop reason = %q, want tool_use", final.StopReason)
	}
}
