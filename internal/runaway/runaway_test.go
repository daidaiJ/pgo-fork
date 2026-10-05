// Tests for the runaway sentinel (T3.2). The scenario cases adapt the
// minimax-code runaway-guard behavioral suite (streak threshold, reminder
// body, boundary resets) and the qwen-code loop-detection cases (canonical
// repeat-key, result-fingerprint corroboration against polling false
// positives) to the stateless message-tail detector.
package runaway

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
)

// builders construct one step (assistant tool call + its executed result) and
// prepend it after the given messages, so a scenario reads chronologically.
func userTurn(msgs *agentcore.MessageList, text string) {
	*msgs = append(*msgs, agentcore.UserMessage{
		RoleField: agentcore.RoleUser,
		Content:   agentcore.ContentList{agentcore.NewTextContent(text)},
	})
}

func addStepResult(msgs *agentcore.MessageList, id, name, args, result string, isErr bool) {
	*msgs = append(*msgs, agentcore.AssistantMessage{
		RoleField: agentcore.RoleAssistant,
		Content: agentcore.ContentList{agentcore.ToolCallContent{
			Type:      agentcore.ContentTypeToolCall,
			ID:        id,
			Name:      name,
			Arguments: json.RawMessage(args),
		}},
		StopReason: agentcore.StopReasonToolUse,
	})
	*msgs = append(*msgs, agentcore.ToolResultMessage{
		RoleField:  agentcore.RoleToolResult,
		ToolCallID: id,
		ToolName:   name,
		Content:    agentcore.ContentList{agentcore.NewTextContent(result)},
		IsError:    isErr,
	})
}

func addStep(msgs *agentcore.MessageList, id, name, args, result string) {
	addStepResult(msgs, id, name, args, result, false)
}

func TestActionKeyCanonicalizesObjectKeyOrder(t *testing.T) {
	a := ActionKey("write", json.RawMessage(`{"path":"x.go","content":"hi"}`))
	b := ActionKey("write", json.RawMessage(`{"content":"hi","path":"x.go"}`))
	if a != b {
		t.Errorf("args differing only in object key order must hash equal: %q vs %q", a, b)
	}
	c := ActionKey("write", json.RawMessage(`{"path":"x.go","content":"ho"}`))
	if a == c {
		t.Error("different args must hash differently")
	}
	// Array order is preserved: element position is argument meaning.
	d := ActionKey("read", json.RawMessage(`{"offsets":[1,2]}`))
	e := ActionKey("read", json.RawMessage(`{"offsets":[2,1]}`))
	if d == e {
		t.Error("reordered array elements must hash differently")
	}
}

func TestActionKeyStableForMalformedArgs(t *testing.T) {
	bad := json.RawMessage(`{"todos": []{}...`)
	if ActionKey("todo", bad) != ActionKey("todo", bad) {
		t.Error("two identical malformed arg payloads must hash equal")
	}
	if ActionKey("todo", bad) == ActionKey("todo", json.RawMessage(`{"todos": []`)) {
		t.Error("different malformed payloads must hash differently")
	}
	if ActionKey("todo", nil) != ActionKey("todo", json.RawMessage(`  {}  `)) {
		t.Error("empty and whitespace-padded empty args must hash equal")
	}
}

func TestResultFingerprintIgnoresCallIdentity(t *testing.T) {
	a := agentcore.ToolResultMessage{
		RoleField:  agentcore.RoleToolResult,
		ToolCallID: "call-1",
		ToolName:   "read",
		Content:    agentcore.ContentList{agentcore.NewTextContent("same")},
		Timestamp:  1000,
	}
	b := a
	b.ToolCallID = "call-2"
	b.Timestamp = 9999
	if ResultFingerprint(a) != ResultFingerprint(b) {
		t.Error("call id and timestamp must not affect the result fingerprint")
	}
	c := a
	c.Content = agentcore.ContentList{agentcore.NewTextContent("changed")}
	if ResultFingerprint(a) == ResultFingerprint(c) {
		t.Error("changed content must change the fingerprint")
	}
	d := a
	d.IsError = true
	if ResultFingerprint(a) == ResultFingerprint(d) {
		t.Error("error results must fingerprint differently from success")
	}
}

func TestDetectSilentBelowStreak(t *testing.T) {
	msgs := agentcore.MessageList{}
	userTurn(&msgs, "go")
	for i := range ReminderStreak - 1 {
		addStep(&msgs, idAt(i), "read", `{"path":"x.go"}`, "same")
	}
	if got := Detect(msgs); got != "" {
		t.Errorf("streak of %d must stay silent, got %q", ReminderStreak-1, got)
	}
}

func TestDetectFiresAtStreak(t *testing.T) {
	msgs := agentcore.MessageList{}
	userTurn(&msgs, "go")
	for i := range ReminderStreak {
		addStep(&msgs, idAt(i), "read", `{"path":"x.go"}`, "same")
	}
	got := Detect(msgs)
	if got == "" {
		t.Fatal("streak of identical calls with identical results must fire")
	}
	if !strings.Contains(got, "read") {
		t.Errorf("reminder must name the tool, got %q", got)
	}
	if !strings.Contains(got, "temporary runtime context for the current turn only") {
		t.Errorf("reminder must be marked turn-scoped, got %q", got)
	}
}

func TestDetectSuppressedWhenResultChanges(t *testing.T) {
	// qwen #9450: identical calls whose results keep changing are productive
	// polling; a frozen board (identical results) is the loop.
	msgs := agentcore.MessageList{}
	userTurn(&msgs, "watch the board")
	for i := range ReminderStreak {
		addStep(&msgs, idAt(i), "read", `{"path":"board.txt"}`, "status "+string(rune('a'+i)))
	}
	if got := Detect(msgs); got != "" {
		t.Errorf("changing results must suppress the reminder, got %q", got)
	}
	msgs = agentcore.MessageList{}
	userTurn(&msgs, "watch the board")
	for range ReminderStreak {
		addStep(&msgs, "x", "read", `{"path":"board.txt"}`, "status frozen")
	}
	if got := Detect(msgs); got == "" {
		t.Error("identical results across the streak must fire")
	}
}

func TestDetectBrokenByInterleavedCall(t *testing.T) {
	msgs := agentcore.MessageList{}
	userTurn(&msgs, "go")
	for i := range ReminderStreak {
		addStep(&msgs, idAt(2*i), "read", `{"path":"a.txt"}`, "same")
		addStep(&msgs, idAt(2*i+1), "read", `{"path":"b.txt"}`, "same")
	}
	if got := Detect(msgs); got != "" {
		t.Errorf("interleaved different calls break adjacency and must stay silent, got %q", got)
	}
}

func TestDetectStopsAtUserBoundary(t *testing.T) {
	// A streak belongs to one run: a new user turn resets the window.
	msgs := agentcore.MessageList{}
	userTurn(&msgs, "first")
	for range ReminderStreak {
		addStep(&msgs, "x", "read", `{"path":"x.go"}`, "same")
	}
	userTurn(&msgs, "second")
	addStep(&msgs, "y", "read", `{"path":"x.go"}`, "same")
	if got := Detect(msgs); got != "" {
		t.Errorf("streak must not cross a user turn, got %q", got)
	}
}

func TestDetectStopsAtCompactionBoundary(t *testing.T) {
	msgs := agentcore.MessageList{}
	userTurn(&msgs, "first")
	for range ReminderStreak {
		addStep(&msgs, "x", "read", `{"path":"x.go"}`, "same")
	}
	msgs = append(msgs, agentcore.CompactionMessage{RoleField: agentcore.RoleCompaction, Summary: "summary"})
	addStep(&msgs, "y", "read", `{"path":"x.go"}`, "same")
	if got := Detect(msgs); got != "" {
		t.Errorf("streak must not cross a compaction checkpoint, got %q", got)
	}
}

func TestDetectTextOnlyTurnBreaksWindow(t *testing.T) {
	msgs := agentcore.MessageList{}
	userTurn(&msgs, "go")
	for range ReminderStreak {
		addStep(&msgs, "x", "read", `{"path":"x.go"}`, "same")
	}
	msgs = append(msgs, agentcore.AssistantMessage{
		RoleField:  agentcore.RoleAssistant,
		Content:    agentcore.ContentList{agentcore.NewTextContent("done")},
		StopReason: agentcore.StopReasonEndTurn,
	})
	if got := Detect(msgs); got != "" {
		t.Errorf("text-only turn ends the window, got %q", got)
	}
}

func TestDetectMultiCallBatch(t *testing.T) {
	// minimax: every key in the trailing batch is a candidate; one stuck key
	// in a varied batch still fires.
	msgs := agentcore.MessageList{}
	userTurn(&msgs, "go")
	for i := range ReminderStreak {
		msgs = append(msgs, agentcore.AssistantMessage{
			RoleField: agentcore.RoleAssistant,
			Content: agentcore.ContentList{
				agentcore.ToolCallContent{Type: agentcore.ContentTypeToolCall, ID: idAt(2 * i), Name: "read",
					Arguments: json.RawMessage(`{"path":"stuck.txt"}`)},
				agentcore.ToolCallContent{Type: agentcore.ContentTypeToolCall, ID: idAt(2*i+1), Name: "read",
					Arguments: json.RawMessage(`{"path":"varied.txt"}`)},
			},
			StopReason: agentcore.StopReasonToolUse,
		})
		msgs = append(msgs,
			agentcore.ToolResultMessage{RoleField: agentcore.RoleToolResult, ToolCallID: idAt(2 * i), ToolName: "read",
				Content: agentcore.ContentList{agentcore.NewTextContent("frozen")}},
			agentcore.ToolResultMessage{RoleField: agentcore.RoleToolResult, ToolCallID: idAt(2*i+1), ToolName: "read",
				Content: agentcore.ContentList{agentcore.NewTextContent("status " + string(rune('a'+i)))}},
		)
	}
	got := Detect(msgs)
	if got == "" {
		t.Fatal("stuck key inside a varied batch must fire")
	}
	if !strings.Contains(got, "read") {
		t.Errorf("reminder should name the stuck call's tool, got %q", got)
	}
}

func TestDetectMissingResultFailsOpen(t *testing.T) {
	// Incomplete evidence never produces a reminder.
	msgs := agentcore.MessageList{}
	userTurn(&msgs, "go")
	for i := range ReminderStreak {
		addStep(&msgs, idAt(i), "read", `{"path":"x.go"}`, "same")
	}
	msgs = append(msgs, agentcore.AssistantMessage{
		RoleField: agentcore.RoleAssistant,
		Content: agentcore.ContentList{agentcore.ToolCallContent{
			Type: agentcore.ContentTypeToolCall, ID: "no-result", Name: "read",
			Arguments: json.RawMessage(`{"path":"x.go"}`),
		}},
		StopReason: agentcore.StopReasonToolUse,
	})
	if got := Detect(msgs); got != "" {
		t.Errorf("call without its executed result must fail open, got %q", got)
	}
}

func TestDetectFiresBeyondWindow(t *testing.T) {
	// A streak longer than the scan window still fires: the window bounds
	// work, not detection.
	msgs := agentcore.MessageList{}
	userTurn(&msgs, "go")
	for i := range 2 * maxScanSteps {
		addStep(&msgs, idAt(i), "read", `{"path":"x.go"}`, "same")
	}
	if got := Detect(msgs); got == "" {
		t.Error("long streak beyond the scan window must still fire")
	}
}

func TestDetectEmptyAndToolless(t *testing.T) {
	if got := Detect(nil); got != "" {
		t.Errorf("empty history must stay silent, got %q", got)
	}
	msgs := agentcore.MessageList{}
	userTurn(&msgs, "hello")
	if got := Detect(msgs); got != "" {
		t.Errorf("toolless history must stay silent, got %q", got)
	}
}

func idAt(i int) string {
	return "call-" + strconv.Itoa(i)
}
