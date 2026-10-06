package compaction

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/provider"
)

func TestExtractFileOpsAndLists(t *testing.T) {
	readArgs, _ := json.Marshal(map[string]string{"path": "a.go"})
	writeArgs, _ := json.Marshal(map[string]string{"path": "b.go"})
	editArgs, _ := json.Marshal(map[string]string{"path": "a.go"}) // a.go also edited -> modified wins
	msgs := []agentcore.Message{
		assistantToolCall("1", "read"),
		assistantToolCall("2", "write"),
		assistantToolCall("3", "edit"),
	}
	// Attach args by rebuilding with arguments.
	msgs[0] = agentcore.AssistantMessage{RoleField: agentcore.RoleAssistant, Content: agentcore.ContentList{agentcore.NewToolCallContent("1", "read", readArgs)}}
	msgs[1] = agentcore.AssistantMessage{RoleField: agentcore.RoleAssistant, Content: agentcore.ContentList{agentcore.NewToolCallContent("2", "write", writeArgs)}}
	msgs[2] = agentcore.AssistantMessage{RoleField: agentcore.RoleAssistant, Content: agentcore.ContentList{agentcore.NewToolCallContent("3", "edit", editArgs)}}

	ops := NewFileOps()
	for _, m := range msgs {
		extractFileOpsFromMessage(m, ops)
	}
	read, modified := computeFileLists(ops)
	// a.go was read AND edited -> only in modified; b.go written -> modified.
	if len(read) != 0 {
		t.Fatalf("readFiles: got %v, want []", read)
	}
	if strings.Join(modified, ",") != "a.go,b.go" {
		t.Fatalf("modifiedFiles: got %v, want [a.go b.go]", modified)
	}
}

func TestFormatFileOperations(t *testing.T) {
	if got := formatFileOperations(nil, nil); got != "" {
		t.Fatalf("empty: got %q, want empty", got)
	}
	got := formatFileOperations([]string{"r.go"}, []string{"m.go"})
	if !strings.Contains(got, "<read-files>\nr.go\n</read-files>") {
		t.Fatalf("missing read-files block: %q", got)
	}
	if !strings.Contains(got, "<modified-files>\nm.go\n</modified-files>") {
		t.Fatalf("missing modified-files block: %q", got)
	}
}

func TestSerializeConversation(t *testing.T) {
	args, _ := json.Marshal(map[string]any{"path": "x.go", "n": 1})
	msgs := []agentcore.Message{
		userMsg("hello"),
		agentcore.AssistantMessage{
			RoleField: agentcore.RoleAssistant,
			Content: agentcore.ContentList{
				agentcore.NewThinkingContent("thinking hard"),
				agentcore.NewTextContent("here goes"),
				agentcore.NewToolCallContent("t1", "read", args),
			},
		},
		toolResult("t1"),
	}
	got := serializeConversation(msgs)
	for _, want := range []string{
		"[User]: hello",
		"[Assistant thinking]: thinking hard",
		"[Assistant]: here goes",
		"[Assistant tool calls]: read(",
		"[Tool result]: result",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("serialize missing %q in:\n%s", want, got)
		}
	}
}

func TestTruncateForSummary(t *testing.T) {
	if got := truncateForSummary("short", 100); got != "short" {
		t.Fatalf("no truncation expected: %q", got)
	}
	long := strings.Repeat("z", 2500)
	got := truncateForSummary(long, toolResultMaxChars)
	if !strings.Contains(got, "more characters truncated") {
		t.Fatalf("expected truncation marker: %q", got[len(got)-60:])
	}
}

// fakeStreamFn returns a StreamFn that yields a single done event with the
// given assistant message, capturing the LlmContext it was called with.
func fakeStreamFn(final agentcore.AssistantMessage, capture *provider.LlmContext) provider.StreamFn {
	return func(ctx context.Context, model string, llm provider.LlmContext, cfg provider.StreamConfig) (*provider.AssistantMessageEventStream, error) {
		if capture != nil {
			*capture = llm
		}
		s := provider.NewAssistantMessageEventStream(4)
		go func() {
			_ = s.Emit(ctx, provider.StreamDoneEvent{Message: final})
			s.SetResult(final)
			s.Close()
		}()
		return s, nil
	}
}

func assistantText(text string) agentcore.AssistantMessage {
	// Pad past the degenerate-summary floor (T3.3: a sub-500-char summary is a
	// failed attempt); real summaries always clear it.
	const floor = 600
	for len(text) < floor {
		text += "\n- filler context line for the test fixture"
	}
	return agentcore.AssistantMessage{
		RoleField:  agentcore.RoleAssistant,
		Content:    agentcore.ContentList{agentcore.NewTextContent(text)},
		StopReason: agentcore.StopReasonEndTurn,
	}
}

func TestGenerateSummaryFirstTime(t *testing.T) {
	var captured provider.LlmContext
	stream := fakeStreamFn(assistantText("## Goal\ndo the thing"), &captured)
	model := provider.Model{ID: "m", MaxOutputTokens: 8000}
	msgs := []agentcore.Message{userMsg("please do X")}

	got, _, err := GenerateSummary(context.Background(), stream, model, msgs, 16384, "", provider.StreamConfig{})
	if err != nil {
		t.Fatalf("GenerateSummary: %v", err)
	}
	if !strings.Contains(got, "## Goal") {
		t.Fatalf("summary text: %q", got)
	}
	// System prompt must be the summarization system prompt.
	if captured.SystemPrompt != SUMMARIZATION_SYSTEM_PROMPT {
		t.Fatalf("system prompt mismatch")
	}
	// First-time prompt uses the non-update template and wraps the conversation.
	promptText := textOf(captured.Messages[0].(agentcore.UserMessage).Content)
	if !strings.Contains(promptText, "<conversation>") || strings.Contains(promptText, "<previous-summary>") {
		t.Fatalf("first-time prompt shape wrong:\n%s", promptText)
	}
	if !strings.Contains(promptText, "Create a structured context checkpoint") {
		t.Fatalf("expected first-time template")
	}
}

func TestGenerateSummaryUpdateUsesPrevious(t *testing.T) {
	var captured provider.LlmContext
	stream := fakeStreamFn(assistantText("updated summary"), &captured)
	model := provider.Model{ID: "m"}
	msgs := []agentcore.Message{userMsg("more work")}

	_, _, err := GenerateSummary(context.Background(), stream, model, msgs, 16384, "PRIOR SUMMARY", provider.StreamConfig{})
	if err != nil {
		t.Fatalf("GenerateSummary: %v", err)
	}
	promptText := textOf(captured.Messages[0].(agentcore.UserMessage).Content)
	if !strings.Contains(promptText, "<previous-summary>\nPRIOR SUMMARY") {
		t.Fatalf("update prompt should embed previous summary:\n%s", promptText)
	}
	if !strings.Contains(promptText, "NEW conversation messages to incorporate") {
		t.Fatalf("expected update template")
	}
}

func TestGenerateSummaryMaxTokensCap(t *testing.T) {
	var gotMax int
	stream := func(ctx context.Context, model string, llm provider.LlmContext, cfg provider.StreamConfig) (*provider.AssistantMessageEventStream, error) {
		if v, ok := cfg.Extra["max_tokens"].(int); ok {
			gotMax = v
		}
		s := provider.NewAssistantMessageEventStream(2)
		go func() {
			m := assistantText("ok")
			_ = s.Emit(ctx, provider.StreamDoneEvent{Message: m})
			s.SetResult(m)
			s.Close()
		}()
		return s, nil
	}
	// 0.8 * 16384 = 13107, but model max output is 5000 -> cap at 5000.
	model := provider.Model{ID: "m", MaxOutputTokens: 5000}
	_, _, err := GenerateSummary(context.Background(), stream, model, []agentcore.Message{userMsg("x")}, 16384, "", provider.StreamConfig{})
	if err != nil {
		t.Fatalf("GenerateSummary: %v", err)
	}
	if gotMax != 5000 {
		t.Fatalf("max_tokens: got %d, want 5000", gotMax)
	}
}

func TestGenerateSummaryErrorStopReason(t *testing.T) {
	errMsg := agentcore.AssistantMessage{RoleField: agentcore.RoleAssistant, StopReason: agentcore.StopReasonError, ErrorMessage: "boom"}
	stream := fakeStreamFn(errMsg, nil)
	_, _, err := GenerateSummary(context.Background(), stream, provider.Model{ID: "m"}, []agentcore.Message{userMsg("x")}, 16384, "", provider.StreamConfig{})
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected error containing 'boom', got %v", err)
	}
}

func TestCompactRebuildsContext(t *testing.T) {
	readArgs, _ := json.Marshal(map[string]string{"path": "old.go"})
	msgs := []agentcore.Message{
		userMsg("turn one"), // 0
		agentcore.AssistantMessage{RoleField: agentcore.RoleAssistant, Content: agentcore.ContentList{agentcore.NewToolCallContent("t1", "read", readArgs)}}, // 1
		toolResult("t1"),                // 2
		bigUser(100),                    // 3
		assistantMsg("recent", nil, ""), // 4
	}
	stream := fakeStreamFn(assistantText("## Goal\nx"), nil)
	// Small keepRecentTokens so the cut lands on the recent bigUser(100) turn,
	// leaving the earlier read/toolResult prefix to be summarized.
	settings := CompactionSettings{Enabled: true, ReserveTokens: 16384, KeepRecentTokens: 50}
	res, err := Compact(context.Background(), stream, provider.Model{ID: "m"}, msgs, settings, -1, nil, "", provider.StreamConfig{})
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if res == nil {
		t.Fatal("Compact returned nil result")
	}
	// old.go was read in the summarized prefix.
	if strings.Join(res.Details.ReadFiles, ",") != "old.go" {
		t.Fatalf("readFiles: got %v, want [old.go]", res.Details.ReadFiles)
	}
	if !strings.Contains(res.Summary, "<read-files>") {
		t.Fatalf("summary should carry file metadata: %q", res.Summary)
	}
	rebuilt := res.RebuildContext(msgs, 123)
	if rebuilt[0].Role() != agentcore.RoleCompaction {
		t.Fatalf("first rebuilt message must be compaction, got %s", rebuilt[0].Role())
	}
	// Retained tail begins at FirstKeptIndex.
	if len(rebuilt) != 1+(len(msgs)-res.FirstKeptIndex) {
		t.Fatalf("rebuilt length: got %d", len(rebuilt))
	}
}

func TestCompactNothingToSummarize(t *testing.T) {
	// prevCompactionIndex already at/after the cut -> nil result.
	msgs := []agentcore.Message{userMsg("a"), assistantMsg("b", nil, "")}
	stream := fakeStreamFn(assistantText("unused"), nil)
	res, err := Compact(context.Background(), stream, provider.Model{ID: "m"}, msgs, DefaultCompactionSettings, 5, nil, "", provider.StreamConfig{})
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if res != nil {
		t.Fatalf("expected nil result when nothing to summarize, got %+v", res)
	}
}

func TestCompactionMessageRoundTrip(t *testing.T) {
	details, _ := json.Marshal(CompactionDetails{ReadFiles: []string{"a"}, ModifiedFiles: []string{"b"}})
	cm := agentcore.CompactionMessage{
		RoleField:    agentcore.RoleCompaction,
		Summary:      "the summary",
		TokensBefore: 42,
		Details:      details,
		Timestamp:    7,
	}
	list := agentcore.MessageList{cm}
	raw, err := json.Marshal(list)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back agentcore.MessageList
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(back) != 1 || back[0].Role() != agentcore.RoleCompaction {
		t.Fatalf("round-trip role: %+v", back)
	}
	got := back[0].(agentcore.CompactionMessage)
	if got.Summary != "the summary" || got.TokensBefore != 42 {
		t.Fatalf("round-trip fields: %+v", got)
	}
}

func TestCompactIterativeChainCarriesPreviousSummary(t *testing.T) {
	// T3.3 defect-① fix: a second compaction over a view whose head is the
	// previous compaction's marker must (a) summarize only the messages after
	// the marker and (b) feed the previous summary into the update template,
	// so the old summary's content is never silently dropped.
	readArgs, _ := json.Marshal(map[string]string{"path": "old.go"})
	marker := agentcore.CompactionMessage{
		RoleField: agentcore.RoleCompaction,
		Summary:   "PRIOR CHAIN SUMMARY",
		Details:   mustJSON(t, CompactionDetails{ReadFiles: []string{"carried.go"}}),
		Timestamp: 1,
	}
	msgs := []agentcore.Message{
		marker, // 0: previous compaction
		agentcore.AssistantMessage{RoleField: agentcore.RoleAssistant, Content: agentcore.ContentList{agentcore.NewToolCallContent("t1", "read", readArgs)}}, // 1
		toolResult("t1"),                // 2
		bigUser(100),                    // 3
		assistantMsg("recent", nil, ""), // 4
	}
	var captured provider.LlmContext
	stream := fakeStreamFn(assistantText("## Goal\nchained"), &captured)
	settings := CompactionSettings{Enabled: true, ReserveTokens: 16384, KeepRecentTokens: 50}
	res, err := Compact(context.Background(), stream, provider.Model{ID: "m"}, msgs, settings, 0, mustDetails(t, marker), "PRIOR CHAIN SUMMARY", provider.StreamConfig{})
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if res == nil {
		t.Fatal("Compact returned nil result")
	}
	promptText := textOf(captured.Messages[0].(agentcore.UserMessage).Content)
	if !strings.Contains(promptText, "<previous-summary>\nPRIOR CHAIN SUMMARY") {
		t.Fatalf("second compaction must carry the previous summary: %q", promptText)
	}
	// Only the post-marker range is serialized into the conversation block.
	if strings.Contains(promptText, "[Previous compaction checkpoint]") {
		t.Fatalf("the marker itself must not be re-serialized as conversation: %q", promptText)
	}
	// File lists seed from the previous compaction's details.
	joined := strings.Join(res.Details.ReadFiles, ",")
	if !strings.Contains(joined, "carried.go") || !strings.Contains(joined, "old.go") {
		t.Fatalf("readFiles must union prev+range: got %v", res.Details.ReadFiles)
	}
	if !strings.Contains(res.Summary, "<read-files>") {
		t.Fatalf("summary should carry file metadata: %q", res.Summary)
	}
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func mustDetails(t *testing.T, m agentcore.CompactionMessage) *CompactionDetails {
	t.Helper()
	var d CompactionDetails
	if err := json.Unmarshal(m.Details, &d); err != nil {
		t.Fatalf("unmarshal details: %v", err)
	}
	return &d
}

func TestSerializeConversationCarriesCompactionMarker(t *testing.T) {
	// Defensive defect-① branch: a marker passed through unprojected still
	// contributes its summary to the serialization.
	msgs := []agentcore.Message{
		agentcore.CompactionMessage{RoleField: agentcore.RoleCompaction, Summary: "OLD CHAIN SUMMARY"},
		userMsg("turn"),
	}
	got := serializeConversation(msgs)
	if !strings.Contains(got, "OLD CHAIN SUMMARY") {
		t.Fatalf("serializeConversation must carry a compaction marker's summary: %q", got)
	}
	// Microcompact records are projection metadata: never serialized.
	msgs = append(msgs, agentcore.MicrocompactMessage{RoleField: agentcore.RoleMicrocompact, ClearedCallIDs: []string{"x"}})
	if strings.Count(serializeConversation(msgs), "OLD CHAIN SUMMARY") != 1 {
		t.Fatalf("serialization changed shape unexpectedly")
	}
}
