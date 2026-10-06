package repl

// Tests for the persisted /btw side thread (T4.3): a side thread's own Q&A
// lands in a peek_* session file that is durable and reopenable — including
// from a later process — while staying invisible to the surfaces that enumerate
// real conversations, and while never writing to (or copying) the main session.

import (
	"bytes"
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/session"
)

// peekHeaders returns every peek session in the store (ListAll sees them; List
// deliberately does not).
func peekHeaders(t *testing.T, store *session.Store) []session.SessionHeader {
	t.Helper()
	all, err := store.ListAll()
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	var out []session.SessionHeader
	for _, h := range all {
		if session.IsPeek(h.ID) {
			out = append(out, h)
		}
	}
	return out
}

// peekText flattens a session file's user/assistant text so a test can assert
// what a peek session actually holds.
func peekText(t *testing.T, store *session.Store, id string) string {
	t.Helper()
	_, entries, err := store.LoadEntries(id)
	if err != nil {
		t.Fatalf("LoadEntries(%s): %v", id, err)
	}
	var b strings.Builder
	for _, e := range entries {
		switch m := e.Message.(type) {
		case agentcore.UserMessage:
			b.WriteString(agentcore.ContentToText(m.Content))
			b.WriteString("\n")
		case agentcore.AssistantMessage:
			b.WriteString(agentcore.ContentToText(m.Content))
			b.WriteString("\n")
		}
	}
	return b.String()
}

// TestBtwSavesSideThreadToPeekSession verifies one /btw Q&A is persisted into a
// peek session that is hidden from the session list, holds only the side Q&A,
// and leaves the main session's file untouched.
func TestBtwSavesSideThreadToPeekSession(t *testing.T) {
	p := &replProvider{reply: "side answer"}
	deps, store := newTestDeps(t, p)
	seedMainContext(&deps)
	// Persist the main conversation first so the session list is non-empty and
	// "the peek session is not in it" is a meaningful assertion.
	if err := store.Save(deps.header, deps.agentCtx.Messages); err != nil {
		t.Fatalf("save main session: %v", err)
	}

	var out bytes.Buffer
	if err := runREPL(strings.NewReader("/btw why pointers?\n/exit\n"), &out, deps); err != nil {
		t.Fatalf("runREPL: %v", err)
	}
	if p.calls != 1 {
		t.Fatalf("expected 1 side run, got %d", p.calls)
	}

	peeks := peekHeaders(t, store)
	if len(peeks) != 1 {
		t.Fatalf("expected exactly 1 peek session, got %d", len(peeks))
	}
	if peeks[0].ParentSession != deps.header.ID {
		t.Errorf("peek ParentSession = %q, want %q", peeks[0].ParentSession, deps.header.ID)
	}

	// The whole point of the purpose prefix: peek sessions never appear where
	// conversations are enumerated.
	listed, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, h := range listed {
		if session.IsPeek(h.ID) {
			t.Fatalf("peek session %q leaked into List()", h.ID)
		}
	}
	if len(listed) != 1 || listed[0].ID != deps.header.ID {
		t.Fatalf("List = %+v, want only the main session", listed)
	}

	text := peekText(t, store, peeks[0].ID)
	if !strings.Contains(text, "why pointers?") {
		t.Errorf("peek session must hold the side question, got: %q", text)
	}
	if !strings.Contains(text, "side answer") {
		t.Errorf("peek session must hold the side answer, got: %q", text)
	}
	// Only the side Q&A — never a copy of the main background.
	if strings.Contains(text, "main question") {
		t.Errorf("peek session must not copy the main background, got: %q", text)
	}

	// And the main session's file is unchanged by the side thread.
	_, entries, err := store.LoadEntries(deps.header.ID)
	if err != nil {
		t.Fatalf("LoadEntries(main): %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("main session entries = %d, want 2 (unchanged)", len(entries))
	}
}

// TestBtwReopenSideThreadFromDisk verifies the durability half of T4.3: a bare
// "/btw" in a later process (a fresh replDeps over the same store and session
// id, with no in-memory side thread) reopens the peek session, replays its
// history, and keeps growing the same file.
func TestBtwReopenSideThreadFromDisk(t *testing.T) {
	p := &replProvider{reply: "first answer"}
	deps, store := newTestDeps(t, p)
	if err := store.Save(deps.header, nil); err != nil {
		t.Fatalf("save main session: %v", err)
	}
	var first bytes.Buffer
	if err := runREPL(strings.NewReader("/btw q1?\n/exit\n"), &first, deps); err != nil {
		t.Fatalf("runREPL (first process): %v", err)
	}
	if len(peekHeaders(t, store)) != 1 {
		t.Fatal("expected the first process to create one peek session")
	}

	// Simulate a restart: same session id and store, no in-memory side thread.
	deps2 := newTestDepsOnStore(t, p, store, deps.header.ID)
	callsBefore := p.calls
	var second bytes.Buffer
	if err := runREPL(strings.NewReader("/btw\nfollowup?\n/exit\n/exit\n"), &second, deps2); err != nil {
		t.Fatalf("runREPL (second process): %v", err)
	}
	s := second.String()
	if strings.Contains(s, "usage: /btw") {
		t.Fatalf("bare /btw must reopen the peek session instead of printing usage, got: %q", s)
	}
	if !strings.Contains(s, "q1?") {
		t.Errorf("reopen must replay the earlier side question, got: %q", s)
	}
	if !strings.Contains(s, "first answer") {
		t.Errorf("reopen must replay the earlier side answer, got: %q", s)
	}
	if p.calls != callsBefore+1 {
		t.Errorf("expected exactly 1 new side run (the follow-up), got %d", p.calls-callsBefore)
	}

	// The follow-up joined the SAME side thread: still one peek session, now
	// holding both turns.
	peeks := peekHeaders(t, store)
	if len(peeks) != 1 {
		t.Fatalf("expected the follow-up to reuse the existing peek session, got %d", len(peeks))
	}
	text := peekText(t, store, peeks[0].ID)
	for _, want := range []string{"q1?", "first answer", "followup?"} {
		if !strings.Contains(text, want) {
			t.Errorf("peek session missing %q, got: %q", want, text)
		}
	}
}

// TestBtwReopenIsScopedToOwnSession verifies a side thread is never reopened
// from another session's peek file: a fresh session in the same store has no
// side thread of its own, so a bare /btw still prints usage.
func TestBtwReopenIsScopedToOwnSession(t *testing.T) {
	p := &replProvider{reply: "answer"}
	deps, store := newTestDeps(t, p)
	if err := store.Save(deps.header, nil); err != nil {
		t.Fatalf("save main session: %v", err)
	}
	var first bytes.Buffer
	if err := runREPL(strings.NewReader("/btw q1?\n/exit\n"), &first, deps); err != nil {
		t.Fatalf("runREPL: %v", err)
	}

	// A different session id over the same store: the existing peek file belongs
	// to the first session, so it must not be reopened here.
	other := newTestDepsOnStore(t, p, store, "another-session")
	callsBefore := p.calls
	var out bytes.Buffer
	if err := runREPL(strings.NewReader("/btw\n/exit\n"), &out, other); err != nil {
		t.Fatalf("runREPL (other session): %v", err)
	}
	if p.calls != callsBefore {
		t.Errorf("bare /btw in an unrelated session must not launch a run, got %d", p.calls-callsBefore)
	}
	if !strings.Contains(out.String(), "usage: /btw") {
		t.Errorf("expected usage guidance for a session with no side thread, got: %q", out.String())
	}
}
