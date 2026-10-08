package compaction

// Tests for the canonical context edit projection (T3.4): replace/hide applied
// from ContextEditMessage tree entries, first-edit-wins, digest re-location
// after index drift, unresolvable-anchor skipping, raw-list immutability, the
// view→raw map staying exact across dropped entries, and session round-trip /
// fork-branch semantics.

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/session"
	"github.com/smallnest/pigo/internal/testenv"
)

// toolTurn builds an assistant tool call + its result; it returns the pair and
// is the canonical edit target fixture.
func toolTurn(id, text string) []agentcore.Message {
	return []agentcore.Message{
		agentcore.AssistantMessage{
			RoleField: agentcore.RoleAssistant,
			Content: agentcore.ContentList{
				agentcore.NewTextContent("working"),
				agentcore.NewToolCallContent(id, "bash", []byte(`{"command":"ls"}`)),
			},
			StopReason: agentcore.StopReasonToolUse,
		},
		agentcore.ToolResultMessage{
			RoleField:  agentcore.RoleToolResult,
			ToolCallID: id,
			ToolName:   "bash",
			Content:    agentcore.ContentList{agentcore.NewTextContent(text)},
		},
	}
}

func editUserMsg(text string) agentcore.UserMessage {
	return agentcore.UserMessage{RoleField: agentcore.RoleUser,
		Content: agentcore.ContentList{agentcore.NewTextContent(text)}}
}

func editMarker(edits ...agentcore.ContextEdit) agentcore.ContextEditMessage {
	return agentcore.ContextEditMessage{RoleField: agentcore.RoleContextEdit, Edits: edits, Timestamp: 1}
}

func resultText(t *testing.T, msgs agentcore.MessageList, callID string) string {
	t.Helper()
	for _, m := range msgs {
		if tr, ok := m.(agentcore.ToolResultMessage); ok && tr.ToolCallID == callID {
			return agentcore.ContentToText(tr.Content)
		}
	}
	t.Fatalf("no tool result with call id %q in view", callID)
	return ""
}

func TestProjectContextEditReplaceByCallID(t *testing.T) {
	base := append(agentcore.MessageList{editUserMsg("q")}, toolTurn("c1", "THE ORIGINAL LONG OUTPUT")...)
	raw := append(base, editMarker(agentcore.ContextEdit{
		TargetCallID: "c1", Mode: agentcore.ModeReplace, Digest: "the ls listing",
	}))
	view := ProjectView(raw)

	if got := resultText(t, view, "c1"); got != "[result #0 summarized: the ls listing]" {
		t.Fatalf("view should show the digest placeholder, got %q", got)
	}
	// The raw list is untouched (canonical: history lossless).
	if got := resultText(t, raw, "c1"); got != "THE ORIGINAL LONG OUTPUT" {
		t.Fatalf("raw result must stay verbatim, got %q", got)
	}
	// The edit record itself never enters the view.
	for _, m := range view {
		if _, ok := m.(agentcore.ContextEditMessage); ok {
			t.Fatal("ContextEditMessage must not enter the request view")
		}
	}
}

func TestProjectContextEditHideBySeq(t *testing.T) {
	// A user message can be dropped outright; a tool result keeps a placeholder
	// so the tool_use/tool_result pairing survives.
	big := editUserMsg(strings.Repeat("pasted\n", 500))
	base := append(agentcore.MessageList{big}, toolTurn("c1", "output")...)
	raw := append(base, editMarker(agentcore.ContextEdit{
		TargetSeq: 0, Mode: agentcore.ModeHide,
		ContentHash: agentcore.MessageTextDigest(big),
	}))
	view := ProjectView(raw)
	if len(view) != 2 { // assistant + result; the hidden user is gone
		t.Fatalf("hidden user message should drop from the view, got %d entries", len(view))
	}
	for _, m := range view {
		if u, ok := m.(agentcore.UserMessage); ok && agentcore.ContentToText(u.Content) == agentcore.ContentToText(big.Content) {
			t.Fatal("hidden user message still visible")
		}
	}

	// Hide on a tool result keeps the pairing anchor.
	raw2 := append(agentcore.MessageList{editUserMsg("q")},
		append(toolTurn("c1", "output"),
			editMarker(agentcore.ContextEdit{TargetCallID: "c1", Mode: agentcore.ModeHide}))...)
	view2 := ProjectView(raw2)
	if got := resultText(t, view2, "c1"); got != "[result #0 hidden from context]" {
		t.Fatalf("hidden tool result should keep a placeholder, got %q", got)
	}
}

func TestProjectContextEditFirstEditWins(t *testing.T) {
	raw := append(agentcore.MessageList{editUserMsg("q")},
		append(toolTurn("c1", "output"),
			editMarker(agentcore.ContextEdit{TargetCallID: "c1", Mode: agentcore.ModeReplace, Digest: "first"}),
			editMarker(agentcore.ContextEdit{TargetCallID: "c1", Mode: agentcore.ModeReplace, Digest: "second"}))...)
	view := ProjectView(raw)
	if got := resultText(t, view, "c1"); !strings.Contains(got, "first") || strings.Contains(got, "second") {
		t.Fatalf("first edit must win, got %q", got)
	}
}

func TestProjectContextEditRelocatesByDigest(t *testing.T) {
	// The recorded seq went stale (the edit was made against an earlier view;
	// markers since shifted everything) — the digest still resolves the target,
	// preferring the candidate nearest the stale hint.
	big := editUserMsg("the entry to hide")
	base := append(agentcore.MessageList{big}, toolTurn("c1", "output")...)
	raw := append(base,
		agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("filler")}},
		editMarker(agentcore.ContextEdit{TargetSeq: 9, Mode: agentcore.ModeHide,
			ContentHash: agentcore.MessageTextDigest(big)}),
	)
	view := ProjectView(raw)
	if len(view) != 3 { // assistant + result + filler, the hidden user is gone
		t.Fatalf("digest relocation should still hide the target, got %d entries", len(view))
	}
}

func TestProjectContextEditUnresolvableSkipped(t *testing.T) {
	raw := append(agentcore.MessageList{editUserMsg("q")},
		append(toolTurn("c1", "output"),
			editMarker(
				agentcore.ContextEdit{TargetCallID: "missing", Mode: agentcore.ModeReplace, Digest: "x"},
				agentcore.ContextEdit{TargetSeq: 7, Mode: agentcore.ModeHide, ContentHash: "deadbeef"},
			))...)
	view := ProjectView(raw)
	if got := resultText(t, view, "c1"); got != "output" {
		t.Fatalf("unresolvable edits must be skipped, got %q", got)
	}
	if len(view) != 3 {
		t.Fatalf("edit records must still drop from the view, got %d entries", len(view))
	}
}

func TestProjectContextEditAssistantReplaceKeepsToolCalls(t *testing.T) {
	calls := toolTurn("c1", "output")
	raw := append(agentcore.MessageList{editUserMsg("q")}, calls...)
	raw = append(raw, editMarker(agentcore.ContextEdit{
		TargetSeq: 1, Mode: agentcore.ModeReplace, Digest: "assistant digest",
		ContentHash: agentcore.MessageTextDigest(calls[0]),
	}))
	view := ProjectView(raw)
	a, ok := view[1].(agentcore.AssistantMessage)
	if !ok {
		t.Fatalf("assistant must stay in the view, got %T", view[1])
	}
	if len(a.ToolCalls()) != 1 {
		t.Fatalf("replace must keep tool-call blocks, got %d", len(a.ToolCalls()))
	}
	if got := agentcore.ContentToText(a.Content); !strings.Contains(got, "assistant digest") {
		t.Fatalf("replace must swap the text for the digest, got %q", got)
	}
	// Hiding an assistant with tool calls is refused (would orphan its results).
	raw2 := append(agentcore.MessageList{editUserMsg("q")}, calls...)
	raw2 = append(raw2, editMarker(agentcore.ContextEdit{
		TargetSeq: 1, Mode: agentcore.ModeHide,
		ContentHash: agentcore.MessageTextDigest(calls[0]),
	}))
	view2 := ProjectView(raw2)
	if len(view2) != 3 {
		t.Fatalf("hide must not drop a tool_use half, got %d entries", len(view2))
	}
}

func TestProjectViewMappedIndicesWithDroppedEntries(t *testing.T) {
	// Regression: the marker-anchor formula drifts once context-edit records
	// (or microcompact markers) drop out of the view — the map must not.
	base := append(agentcore.MessageList{editUserMsg("q")}, toolTurn("c1", "output")...)
	raw := append(base, editUserMsg("next"))
	raw = append(raw, editMarker(agentcore.ContextEdit{TargetCallID: "c1", Mode: agentcore.ModeReplace, Digest: "d"}))
	raw = append(raw, editUserMsg("tail"))

	view, rawOf := ProjectViewMapped(raw)
	// View = user, assistant, placeholder result, user "next", user "tail" (5);
	// the edit record at raw index 4 is gone, so "tail" (raw 5) sits at view 4.
	if len(view) != 5 {
		t.Fatalf("view should hold 5 entries, got %d", len(view))
	}
	if rawOf == nil || len(rawOf) != 5 || rawOf[3] != 3 || rawOf[4] != 5 {
		t.Fatalf("raw map must skip the dropped edit record, got %v", rawOf)
	}
	if ViewRawOf(rawOf, 4) != 5 {
		t.Fatal("ViewRawOf must resolve through the map, not the identity")
	}

	// With a compaction marker in front, the formula and the map disagree —
	// the map is authoritative.
	markerRaw := append(agentcore.MessageList{
		agentcore.CompactionMessage{RoleField: agentcore.RoleCompaction, KeptBefore: 0},
	}, raw...)
	markerView, markerRawOf := ProjectViewMapped(markerRaw)
	// markerRaw: comp0, user1, asst2, res3, user4, edit5, user6 — the edit at
	// raw 5 drops from the view, so "tail" (raw 6) sits at view 5. The formula
	// (m0=0, k0=0) would answer 5: exactly one entry of drift.
	if got := markerRawOf[5]; got != 6 {
		t.Fatalf("map must resolve view[5] to raw 6, got %d (formula would say %d)",
			got, ViewIndexToRaw(5, 0, 0))
	}
	if ViewRawOf(markerRawOf, 5) != 6 {
		t.Fatal("ViewRawOf must return the mapped index")
	}
	_ = markerView
}

func TestProjectContextEditSessionRoundTripAndBranches(t *testing.T) {
	store, err := session.NewStore(filepath.Join(testenv.Dir(t), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	// Tree: user(q) → result(c1) → user(next) → editMarker  (branch A, leaf e4)
	//                                    └→ user(other)      (branch B, leaf e5)
	base := append(agentcore.MessageList{editUserMsg("q")}, toolTurn("c1", "SECRET OUTPUT")...)
	msgs := append(base, editUserMsg("next"))
	entries := make([]session.Entry, 0, 6)
	parent := ""
	for i, m := range msgs {
		e := session.Entry{ID: fmtEntryID(i), ParentID: parent, Message: m}
		entries = append(entries, e)
		parent = e.ID
	}
	leafA := parent
	edit := editMarker(agentcore.ContextEdit{TargetCallID: "c1", Mode: agentcore.ModeReplace, Digest: "digest A"})
	entries = append(entries, session.Entry{ID: "e4", ParentID: leafA, Message: edit})
	leafA = "e4"
	entries = append(entries, session.Entry{ID: "e5", ParentID: "e3",
		Message: editUserMsg("other branch")})
	leafB := "e5"

	header := session.SessionHeader{ID: "ctxedit-test", Version: session.SchemaVersion}
	if err := store.SaveEntries(header, entries); err != nil {
		t.Fatal(err)
	}

	viewOf := func(leaf string) agentcore.MessageList {
		_, loaded, err := store.LoadEntries(header.ID)
		if err != nil {
			t.Fatal(err)
		}
		path := session.PathToLeaf(loaded, leaf)
		var msgs agentcore.MessageList
		for _, e := range path {
			msgs = append(msgs, e.Message)
		}
		return ProjectView(msgs)
	}

	// Replay: the persisted edit re-applies (round-trip idempotent).
	if got := resultText(t, viewOf(leafA), "c1"); !strings.Contains(got, "digest A") {
		t.Fatalf("persisted edit must re-apply on replay, got %q", got)
	}
	// No cross-branch leak: the sibling branch never saw the edit.
	if got := resultText(t, viewOf(leafB), "c1"); got != "SECRET OUTPUT" {
		t.Fatalf("sibling branch must show the original, got %q", got)
	}

	// Fork inheritance: a fork at leaf A carries the ancestor edit.
	forkHeader, _, err := store.Fork(header.ID, leafA, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	_, forkEntries, err := store.LoadEntries(forkHeader.ID)
	if err != nil {
		t.Fatal(err)
	}
	var forkMsgs agentcore.MessageList
	for _, e := range forkEntries {
		forkMsgs = append(forkMsgs, e.Message)
	}
	if got := resultText(t, ProjectView(forkMsgs), "c1"); !strings.Contains(got, "digest A") {
		t.Fatalf("fork must inherit the ancestor edit, got %q", got)
	}
}

func fmtEntryID(i int) string { return "e" + string(rune('0'+i)) }
