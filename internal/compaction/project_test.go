package compaction

// Tests for the T3.3 request-view projection: full-compaction markers collapse
// the summarized prefix (with the KeptBefore wrap for tip-appended markers),
// superseded markers are dropped from the view, and dangling tool calls get
// synthetic results so the request is always a well-formed pairing.

import (
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
)

func user(body string) agentcore.UserMessage {
	return agentcore.UserMessage{
		RoleField: agentcore.RoleUser,
		Content:   agentcore.ContentList{agentcore.NewTextContent(body)},
	}
}

func assistant(body string, calls ...string) agentcore.AssistantMessage {
	a := agentcore.AssistantMessage{
		RoleField: agentcore.RoleAssistant,
		Content:   agentcore.ContentList{agentcore.NewTextContent(body)},
	}
	for _, c := range calls {
		a.Content = append(a.Content, agentcore.ToolCallContent{
			Type: "toolCall", ID: c, Name: "tool" + c, Arguments: []byte(`{}`),
		})
	}
	return a
}

func trResult(callID, body string) agentcore.ToolResultMessage {
	return agentcore.ToolResultMessage{
		RoleField:  agentcore.RoleToolResult,
		ToolCallID: callID,
		ToolName:   "tool" + callID,
		Content:    agentcore.ContentList{agentcore.NewTextContent(body)},
	}
}

func marker(summary string, keptBefore int) agentcore.CompactionMessage {
	return agentcore.CompactionMessage{
		RoleField:  agentcore.RoleCompaction,
		Summary:    summary,
		KeptBefore: keptBefore,
	}
}

func roles(msgs agentcore.MessageList) []string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = m.Role()
	}
	return out
}

func TestProjectViewNoMarkerUnchanged(t *testing.T) {
	msgs := agentcore.MessageList{user("hi"), assistant("yo")}
	got := ProjectView(msgs)
	if len(got) != 2 || &got[0] != &msgs[0] {
		t.Fatalf("no-marker view should be the original slice, got %+v", got)
	}
}

func TestProjectViewMarkerMidList(t *testing.T) {
	// Live shape: the marker inserted at the cut, everything before it summarized.
	msgs := agentcore.MessageList{
		user("old1"), user("old2"),
		marker("the summary", 0),
		user("kept1"), assistant("kept2"),
	}
	view := ProjectView(msgs)
	if len(view) != 3 {
		t.Fatalf("view length = %d, want 3 (marker + kept): %+v", len(view), view)
	}
	if view[0].Role() != agentcore.RoleCompaction {
		t.Fatalf("view[0] = %s, want the marker", view[0].Role())
	}
	if got := roles(view[1:]); got[0] != agentcore.RoleUser || got[1] != agentcore.RoleAssistant {
		t.Fatalf("kept tail order wrong: %v", got)
	}
	// The original list is untouched (projection only).
	if len(msgs) != 5 {
		t.Fatalf("projection mutated the input list")
	}
}

func TestProjectViewMarkerWrapRestoresKeptBefore(t *testing.T) {
	// Replayed path shape: the marker chained at the branch tip AFTER kept
	// entries that were already persisted when compaction ran. KeptBefore=2
	// restores them ahead of the marker in the view.
	msgs := agentcore.MessageList{
		user("summarized1"), user("summarized2"),
		user("kept-persisted-1"), user("kept-persisted-2"),
		marker("the summary", 2),
		user("kept-after"),
	}
	view := ProjectView(msgs)
	if len(view) != 4 {
		t.Fatalf("view length = %d, want 4 (marker + wrap 2 + after): %+v", len(view), view)
	}
	if view[0].Role() != agentcore.RoleCompaction {
		t.Fatalf("view[0] = %s, want the marker", view[0].Role())
	}
	for i, want := range []string{"kept-persisted-1", "kept-persisted-2", "kept-after"} {
		um, ok := view[i+1].(agentcore.UserMessage)
		if !ok || agentcore.ContentToText(um.Content) != want {
			t.Fatalf("view[%d] = %+v, want user %q", i+1, view[i+1], want)
		}
	}
}

func TestProjectViewDropsSupersededMarkers(t *testing.T) {
	// A second compaction's marker supersedes the first: only the LAST marker
	// renders into the view (its summary chain inherited the earlier one).
	msgs := agentcore.MessageList{
		user("old"),
		marker("first summary", 0),
		user("mid"),
		marker("second summary", 0),
		user("tail"),
	}
	view := ProjectView(msgs)
	if len(view) != 2 {
		t.Fatalf("view length = %d, want 2 (last marker + tail): %+v", len(view), view)
	}
	m, ok := view[0].(agentcore.CompactionMessage)
	if !ok || m.Summary != "second summary" {
		t.Fatalf("view[0] should be the LAST marker, got %+v", view[0])
	}
}

func TestProjectViewWrapDropsSupersededMarkerInsideKeptSpan(t *testing.T) {
	// Degenerate wrap case: the new marker was tip-appended and its KeptBefore
	// span swallows an older marker — the superseded marker must not re-enter
	// the view (its content lives in the new marker's chain).
	msgs := agentcore.MessageList{
		user("old1"),
		marker("first", 0),
		user("kept1"), user("kept2"),
		marker("second", 3),
		user("tail"),
	}
	view := ProjectView(msgs)
	for _, m := range view {
		if c, ok := m.(agentcore.CompactionMessage); ok && c.Summary == "first" {
			t.Fatalf("superseded marker re-entered the view: %+v", view)
		}
	}
	if len(view) != 4 {
		t.Fatalf("view length = %d, want 4 (marker + wrap 2 + tail; old-marker slot dropped)", len(view))
	}
}

func TestProjectViewRepairsDanglingToolCalls(t *testing.T) {
	msgs := agentcore.MessageList{
		user("go"),
		assistant("working", "c1", "c2"),
		trResult("c1", "did c1"),
		// c2 never got a result (interrupted run).
	}
	view := ProjectView(msgs)
	if len(view) != 4 {
		t.Fatalf("view length = %d, want 4 (synthetic result for c2)", len(view))
	}
	last, ok := view[3].(agentcore.ToolResultMessage)
	if !ok || last.ToolCallID != "c2" || !last.IsError {
		t.Fatalf("view[3] should be a synthetic error result for c2, got %+v", view[3])
	}
	if !strings.Contains(agentcore.ContentToText(last.Content), "interrupted") {
		t.Fatalf("synthetic result should explain the interruption: %q", agentcore.ContentToText(last.Content))
	}
	// The original list stays dangling (projection only).
	if len(msgs) != 3 {
		t.Fatalf("projection mutated the input list")
	}
}

func TestProjectViewRepairIdempotent(t *testing.T) {
	msgs := agentcore.MessageList{
		user("go"),
		assistant("working", "c1"),
	}
	once := ProjectView(msgs)
	twice := ProjectView(once)
	if len(twice) != len(once) {
		t.Fatalf("repair must be idempotent: %d then %d", len(once), len(twice))
	}
}

func TestProjectViewHistoricalDanglingRepaired(t *testing.T) {
	// Strict providers reject ANY dangling tool_use in the request, not just at
	// the tail — an interrupted partial message kept mid-history must be
	// repaired too.
	msgs := agentcore.MessageList{
		user("go"),
		assistant("partial", "c1"), // dangling, mid-history
		trResult("c1", "ok"),
		user("next"),
		assistant("dangling too", "c9"),
		user("tail"),
	}
	view := ProjectView(msgs)
	// c9 is dangling: one synthetic result inserted after its assistant.
	found := 0
	for _, m := range view {
		if tr, ok := m.(agentcore.ToolResultMessage); ok && tr.ToolCallID == "c9" {
			found++
		}
	}
	if found != 1 {
		t.Fatalf("expected exactly 1 synthetic result for c9, got %d: %+v", found, view)
	}
}

func TestProjectViewMicrocompactPlaceholder(t *testing.T) {
	msgs := agentcore.MessageList{
		user("go"),
		assistant("call", "c1"),
		trResult("c1", strings.Repeat("big", 1000)),
		assistant("call2", "c2"),
		trResult("c2", "small result"),
		agentcore.MicrocompactMessage{
			RoleField:      agentcore.RoleMicrocompact,
			ClearedCallIDs: []string{"c1"},
			SavedTokens:    750,
		},
	}
	view := ProjectView(msgs)
	if len(view) != 5 {
		t.Fatalf("view length = %d, want 5 (marker dropped, result replaced)", len(view))
	}
	got, ok := view[2].(agentcore.ToolResultMessage)
	if !ok || got.ToolCallID != "c1" {
		t.Fatalf("view[2] should be the cleared c1 result, got %+v", view[2])
	}
	text := agentcore.ContentToText(got.Content)
	if !strings.Contains(text, "cleared to reduce context") || len(text) > 200 {
		t.Fatalf("c1 should be a short placeholder, got %q", text)
	}
	// Untouched results stay verbatim.
	kept, ok := view[4].(agentcore.ToolResultMessage)
	if !ok || agentcore.ContentToText(kept.Content) != "small result" {
		t.Fatalf("c2 result should stay verbatim, got %+v", view[4])
	}
}
