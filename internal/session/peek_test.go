package session

// Tests for peek side-thread sessions (T4.3): the "peek_" purpose prefix, the
// fact that List hides them while ListAll sees them, the lineage recorded on the
// peek header, and the guarantee that a peek file holds only the side thread's
// own Q&A — never a copy of the parent's messages, never a write to the parent.

import (
	"strings"
	"testing"
	"time"

	"github.com/smallnest/pigo/internal/agentcore"
)

// newTestStore returns a Store rooted at a fresh temp dir.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s
}

// userMsg / assistantMsg build the two message kinds a /btw turn produces.
func userMsg(text string) agentcore.Message {
	return agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent(text)}}
}

func assistantMsg(text string) agentcore.Message {
	return agentcore.AssistantMessage{RoleField: agentcore.RoleAssistant, Content: agentcore.ContentList{agentcore.NewTextContent(text)}}
}

// saveParent persists a main conversation with two turns and returns its header.
func saveParent(t *testing.T, s *Store, id string) SessionHeader {
	t.Helper()
	now := time.Now().UTC()
	h := SessionHeader{ID: id, CreatedAt: now, UpdatedAt: now, Model: "faux", Provider: "faux", SystemPrompt: "sys", Cwd: "/work"}
	msgs := agentcore.MessageList{userMsg("main question"), assistantMsg("main answer")}
	if err := s.Save(h, msgs); err != nil {
		t.Fatalf("Save parent: %v", err)
	}
	return h
}

// TestPeekIDPrefix verifies the id scheme: a peek id carries the purpose prefix
// and an ordinary session id does not (the prefix is the whole mechanism).
func TestPeekIDPrefix(t *testing.T) {
	peekID := NewPeekID(time.Now().UTC())
	if !IsPeek(peekID) {
		t.Errorf("IsPeek(%q) = false, want true", peekID)
	}
	if !strings.HasPrefix(peekID, PeekPrefix) {
		t.Errorf("peek id %q missing prefix %q", peekID, PeekPrefix)
	}
	if IsPeek(NewID(time.Now().UTC())) {
		t.Error("IsPeek must be false for an ordinary session id")
	}
	if IsPeek("") {
		t.Error("IsPeek(\"\") = true, want false")
	}
}

// TestListHidesPeekSessions verifies the T4.3 acceptance criterion: a peek side
// thread never shows up where conversations are enumerated, while ListAll still
// reaches it.
func TestListHidesPeekSessions(t *testing.T) {
	s := newTestStore(t)
	parent := saveParent(t, s, NewID(time.Now().UTC()))
	if _, err := OpenPeek(s, parent, time.Now().UTC()); err != nil {
		t.Fatalf("OpenPeek: %v", err)
	}

	listed, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(listed) != 1 || listed[0].ID != parent.ID {
		t.Fatalf("List = %+v, want only the parent %q", listed, parent.ID)
	}
	all, err := s.ListAll()
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("ListAll = %d sessions, want 2 (parent + peek)", len(all))
	}
}

// TestOpenPeekLineageAndNoBackground verifies a peek session records its parent
// for lineage and inherits the run metadata — but copies none of the parent's
// messages, so it can never resurrect a stale copy of the main transcript.
func TestOpenPeekLineageAndNoBackground(t *testing.T) {
	s := newTestStore(t)
	parent := saveParent(t, s, NewID(time.Now().UTC()))

	peek, err := OpenPeek(s, parent, time.Now().UTC())
	if err != nil {
		t.Fatalf("OpenPeek: %v", err)
	}
	h := peek.Header()
	if !IsPeek(h.ID) {
		t.Errorf("peek id %q missing the purpose prefix", h.ID)
	}
	if h.ParentSession != parent.ID {
		t.Errorf("ParentSession = %q, want %q", h.ParentSession, parent.ID)
	}
	if h.Model != parent.Model || h.Provider != parent.Provider || h.SystemPrompt != parent.SystemPrompt || h.Cwd != parent.Cwd {
		t.Errorf("peek header did not inherit the parent metadata: %+v vs %+v", h, parent)
	}
	msgs, err := peek.Messages()
	if err != nil {
		t.Fatalf("peek.Messages: %v", err)
	}
	if len(msgs) != 0 {
		t.Fatalf("a fresh peek session must be empty, got %d messages", len(msgs))
	}
}

// TestPeekAppendGrowsOneChain verifies successive appends land on one chain in
// order (the tree grows, nothing is flattened or re-rooted).
func TestPeekAppendGrowsOneChain(t *testing.T) {
	s := newTestStore(t)
	parent := saveParent(t, s, NewID(time.Now().UTC()))
	peek, err := OpenPeek(s, parent, time.Now().UTC())
	if err != nil {
		t.Fatalf("OpenPeek: %v", err)
	}
	if err := peek.Append(agentcore.MessageList{userMsg("q1"), assistantMsg("a1")}); err != nil {
		t.Fatalf("Append 1: %v", err)
	}
	if err := peek.Append(agentcore.MessageList{userMsg("q2"), assistantMsg("a2")}); err != nil {
		t.Fatalf("Append 2: %v", err)
	}

	msgs, err := peek.Messages()
	if err != nil {
		t.Fatalf("peek.Messages: %v", err)
	}
	var got []string
	for _, m := range msgs {
		switch v := m.(type) {
		case agentcore.UserMessage:
			got = append(got, agentcore.ContentToText(v.Content))
		case agentcore.AssistantMessage:
			got = append(got, agentcore.ContentToText(v.Content))
		}
	}
	want := []string{"q1", "a1", "q2", "a2"}
	if len(got) != len(want) {
		t.Fatalf("peek messages = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("peek messages = %v, want %v", got, want)
		}
	}

	// On disk the four entries form a single chain: exactly one root, and every
	// later entry's parentId points at an existing entry.
	_, entries, err := s.LoadEntries(peek.ID())
	if err != nil {
		t.Fatalf("LoadEntries: %v", err)
	}
	if len(entries) != 4 {
		t.Fatalf("peek entries = %d, want 4", len(entries))
	}
	roots := 0
	ids := map[string]bool{}
	for _, e := range entries {
		ids[e.ID] = true
		if e.ParentID == "" {
			roots++
		}
	}
	if roots != 1 {
		t.Errorf("peek chain has %d roots, want 1", roots)
	}
	for _, e := range entries {
		if e.ParentID != "" && !ids[e.ParentID] {
			t.Errorf("entry %s has dangling parentId %q", e.ID, e.ParentID)
		}
	}
}

// TestPeekAppendLeavesParentUntouched verifies the isolation half of the
// contract: appending to a peek session writes nothing to the parent's file.
func TestPeekAppendLeavesParentUntouched(t *testing.T) {
	s := newTestStore(t)
	parent := saveParent(t, s, NewID(time.Now().UTC()))
	peek, err := OpenPeek(s, parent, time.Now().UTC())
	if err != nil {
		t.Fatalf("OpenPeek: %v", err)
	}
	if err := peek.Append(agentcore.MessageList{userMsg("q1"), assistantMsg("a1")}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	_, entries, err := s.LoadEntries(parent.ID)
	if err != nil {
		t.Fatalf("LoadEntries(parent): %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("parent entries = %d, want 2 (unchanged)", len(entries))
	}
	for _, e := range entries {
		if IsPeek(e.ID) {
			t.Fatalf("parent file must never contain peek entries")
		}
	}
}

// TestLatestPeek verifies a bare /btw can find its side thread again: the most
// recently updated peek of THIS parent, and none for a parent without one.
func TestLatestPeek(t *testing.T) {
	s := newTestStore(t)
	parentA := saveParent(t, s, NewID(time.Now().UTC()))
	parentB := saveParent(t, s, NewID(time.Now().Add(-time.Minute).UTC()))

	// No side thread yet.
	got, err := s.LatestPeek(parentA.ID)
	if err != nil {
		t.Fatalf("LatestPeek: %v", err)
	}
	if got != nil {
		t.Fatalf("LatestPeek with no peek session = %v, want nil", got.ID())
	}

	old, err := OpenPeek(s, parentA, time.Now().Add(-time.Hour).UTC())
	if err != nil {
		t.Fatalf("OpenPeek old: %v", err)
	}
	if err := old.Append(agentcore.MessageList{userMsg("old q")}); err != nil {
		t.Fatalf("Append old: %v", err)
	}
	// A peek belonging to a different parent must never be returned.
	if _, err := OpenPeek(s, parentB, time.Now().Add(-time.Minute).UTC()); err != nil {
		t.Fatalf("OpenPeek b: %v", err)
	}
	recent, err := OpenPeek(s, parentA, time.Now().UTC())
	if err != nil {
		t.Fatalf("OpenPeek recent: %v", err)
	}
	if err := recent.Append(agentcore.MessageList{userMsg("new q")}); err != nil {
		t.Fatalf("Append recent: %v", err)
	}

	got, err = s.LatestPeek(parentA.ID)
	if err != nil {
		t.Fatalf("LatestPeek: %v", err)
	}
	if got == nil {
		t.Fatal("LatestPeek = nil, want the parent's peek session")
	}
	if got.ID() != recent.ID() {
		t.Errorf("LatestPeek = %q, want the most recent peek %q", got.ID(), recent.ID())
	}
	// The recovered handle must continue the existing chain, not root a new one.
	if err := got.Append(agentcore.MessageList{assistantMsg("a")}); err != nil {
		t.Fatalf("Append after recovery: %v", err)
	}
	msgs, err := got.Messages()
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("recovered peek messages = %d, want 2 (existing + appended)", len(msgs))
	}
	if _, err := s.LatestPeek("no-such-parent"); err != nil {
		t.Fatalf("LatestPeek unknown parent: %v", err)
	}
}

// TestOpenPeekRejectsUnusableInput verifies the guards: a nil store or a parent
// without an id cannot open a peek session (the caller then falls back to a
// memory-only side thread).
func TestOpenPeekRejectsUnusableInput(t *testing.T) {
	s := newTestStore(t)
	if _, err := OpenPeek(nil, SessionHeader{ID: "x"}, time.Now().UTC()); err == nil {
		t.Error("OpenPeek(nil store) should fail")
	}
	if _, err := OpenPeek(s, SessionHeader{}, time.Now().UTC()); err == nil {
		t.Error("OpenPeek(empty parent id) should fail")
	}
	var nilPeek *PeekSession
	if nilPeek.ID() != "" {
		t.Error("nil PeekSession.ID() should be empty")
	}
	if msgs, err := nilPeek.Messages(); err != nil || len(msgs) != 0 {
		t.Errorf("nil PeekSession.Messages() = %v, %v; want empty, nil", msgs, err)
	}
	if err := nilPeek.Append(agentcore.MessageList{userMsg("q")}); err != nil {
		t.Errorf("nil PeekSession.Append should be a no-op, got %v", err)
	}
}
