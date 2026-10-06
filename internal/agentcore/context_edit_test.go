package agentcore

// Tests for the T3.4 context-edit message type: role-discriminated JSON
// round-trip and the content digest that anchors edits across persist/load.

import (
	"encoding/json"
	"testing"
)

func TestContextEditMessageJSONRoundTrip(t *testing.T) {
	orig := MessageList{ContextEditMessage{
		RoleField: RoleContextEdit,
		Edits: []ContextEdit{
			{TargetSeq: 3, TargetCallID: "c1", Mode: ModeReplace, Digest: "short summary"},
			{TargetSeq: 0, Mode: ModeHide, ContentHash: "abc123"},
		},
		Timestamp: 42,
	}}
	blob, err := json.Marshal(orig)
	if err != nil {
		t.Fatal(err)
	}
	var back MessageList
	if err := json.Unmarshal(blob, &back); err != nil {
		t.Fatal(err)
	}
	if len(back) != 1 {
		t.Fatalf("round-trip must keep one message, got %d", len(back))
	}
	ce, ok := back[0].(ContextEditMessage)
	if !ok {
		t.Fatalf("role discriminant must decode to ContextEditMessage, got %T", back[0])
	}
	if ce.Role() != RoleContextEdit || ce.Timestamp != 42 || len(ce.Edits) != 2 {
		t.Fatalf("fields lost in round-trip: %+v", ce)
	}
	if ce.Edits[0].TargetCallID != "c1" || ce.Edits[0].Digest != "short summary" ||
		ce.Edits[1].ContentHash != "abc123" {
		t.Fatalf("edit records lost in round-trip: %+v", ce.Edits)
	}
}

func TestMessageTextDigestStableAcrossRoundTrip(t *testing.T) {
	m := ToolResultMessage{
		RoleField:  RoleToolResult,
		ToolCallID: "c1",
		ToolName:   "bash",
		Content:    ContentList{NewTextContent("some output\nwith lines")},
	}
	// Persist/load round-trip through the role-discriminated decoder.
	blob, err := json.Marshal(MessageList{m})
	if err != nil {
		t.Fatal(err)
	}
	var back MessageList
	if err := json.Unmarshal(blob, &back); err != nil {
		t.Fatal(err)
	}
	if MessageTextDigest(m) != MessageTextDigest(back[0]) {
		t.Fatal("digest must be stable across a persist/load round-trip")
	}
	// Distinct content ⇒ distinct digest; equal content ⇒ equal digest.
	other := m
	other.Content = ContentList{NewTextContent("different")}
	if MessageTextDigest(m) == MessageTextDigest(other) {
		t.Fatal("different content must hash differently")
	}
	other2 := m
	other2.Timestamp = 999 // non-content fields must not affect the digest
	if MessageTextDigest(m) != MessageTextDigest(other2) {
		t.Fatal("non-content fields must not affect the digest")
	}
}
