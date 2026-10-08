package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/smallnest/pigo/internal/agentcore"
)

// sessMsg builds a one-turn session body: a user prompt and an assistant reply.
func sessMsg(prompt, reply string) agentcore.MessageList {
	return agentcore.MessageList{
		agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent(prompt)}},
		agentcore.AssistantMessage{RoleField: agentcore.RoleAssistant, Content: agentcore.ContentList{agentcore.NewTextContent(reply)}},
	}
}

// TestGatherSessionsTitlesAndCounts checks the picker's data pass: entries carry
// the first user prompt as the title, the message count, and the current-session
// marker; a missing store degrades to a note instead of panicking.
func TestGatherSessionsTitlesAndCounts(t *testing.T) {
	store := newTestStore(t)
	idA := saveSession(t, store, sessMsg("alpha question about maps", "alpha reply"))
	entries, note := gatherSessions(store, idA)
	if note != "" {
		t.Fatalf("gatherSessions note = %q, want empty", note)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	e := entries[0]
	if e.id != idA || !e.current {
		t.Errorf("entry = %+v, want id %s marked current", e, idA)
	}
	if e.title != "alpha question about maps" {
		t.Errorf("title = %q, want the first user prompt", e.title)
	}
	if e.count != 2 {
		t.Errorf("count = %d, want 2 (user + assistant)", e.count)
	}

	if _, note := gatherSessions(nil, ""); note == "" {
		t.Error("nil store should produce a note, not entries")
	}
}

// TestSessionsPanelFilterAndNavigation covers the pure view behavior: filter
// narrows the rows and navigation resolves within the visible set.
func TestSessionsPanelFilterAndNavigation(t *testing.T) {
	p := sessionsPanel{open: true}
	mk := func(id, title string) sessionEntry {
		return sessionEntry{id: id, title: title, updated: time.Now()}
	}
	p.entries = []sessionEntry{mk("a", "alpha maps"), mk("b", "beta rerank"), mk("c", "gamma maps")}

	p.filter = "maps"
	if got := len(p.visible()); got != 2 {
		t.Fatalf("visible with filter 'maps' = %d, want 2", got)
	}
	p.selected = 0
	if e, ok := p.selectedEntry(); !ok || e.id != "a" {
		t.Errorf("selectedEntry = %+v (ok=%v), want a", e, ok)
	}

	p.filter = "no-such-thing"
	if len(p.visible()) != 0 {
		t.Fatalf("visible with unmatched filter = %d, want 0", len(p.visible()))
	}
	if _, ok := p.selectedEntry(); ok {
		t.Error("selectedEntry should fail on an empty filtered set")
	}
}

// TestSessionsPickerResumeAndDelete drives the full interaction against a real
// store: /sessions opens the picker, Enter resumes the highlighted session into
// the TUI (header/leaf rebind, transcript reseeded without the old
// conversation), the current session refuses an armed delete, and d+y deletes
// another session for real.
func TestSessionsPickerResumeAndDelete(t *testing.T) {
	store := newTestStore(t)
	idA := saveSession(t, store, sessMsg("alpha question", "alpha reply"))
	idB := saveSession(t, store, sessMsg("beta question", "beta reply"))

	s, _, err := newRunSessionWithStore(store, Options{Model: "m", ProviderName: "p"})
	if err != nil {
		t.Fatalf("newRunSessionWithStore: %v", err)
	}
	m := apply(t, NewModel(Options{Model: "m", ProviderName: "p"}).withSession(s, nil),
		tea.WindowSizeMsg{Width: 80, Height: 30})

	// Open the picker through the real slash path. The fresh session is not
	// persisted yet, so the list carries exactly the two saved sessions.
	m = typeInto(t, m, "/sessions").(Model)
	m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.sessionsP.open {
		t.Fatal("/sessions did not open the picker")
	}
	if len(m.sessionsP.entries) != 2 {
		t.Fatalf("picker entries = %d, want 2", len(m.sessionsP.entries))
	}

	// Resume the "beta" session: filter narrows to it, Enter swaps the session.
	m = typeInto(t, m, "beta").(Model)
	m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.sessionsP.open {
		t.Fatal("picker stayed open after resume")
	}
	if m.session.header.ID != idB {
		t.Fatalf("resumed header id = %s, want %s", m.session.header.ID, idB)
	}
	if m.session.persisted != 2 || m.session.curLeaf == "" {
		t.Errorf("resume cursor: persisted=%d curLeaf=%q, want 2/non-empty", m.session.persisted, m.session.curLeaf)
	}
	content := stripANSI(m.transcript.renderAll())
	if !strings.Contains(content, "beta question") || !strings.Contains(content, "beta reply") {
		t.Errorf("resumed transcript missing the beta turn:\n%s", content)
	}
	if strings.Contains(content, "alpha question") {
		t.Errorf("resumed transcript leaked the previous session:\n%s", content)
	}

	// Reopen and try to delete the now-current session: refused, nothing armed.
	m = typeInto(t, m, "/sessions").(Model)
	m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = typeInto(t, m, idB).(Model)
	if e, ok := m.sessionsP.selectedEntry(); !ok || e.id != idB {
		t.Fatalf("filter to id selected %+v (ok=%v), want the resumed session", e, ok)
	}
	m = typeInto(t, m, "d").(Model)
	if m.sessionsP.armed != "" {
		t.Error("arming a delete on the current session must be refused")
	}

	// Delete the other session with the armed d/y grammar; Esc disarms first.
	m.sessionsP.filter = ""
	m.sessionsP.selected = 0
	m = typeInto(t, m, idA).(Model)
	if e, ok := m.sessionsP.selectedEntry(); !ok || e.id != idA {
		t.Fatalf("filter to id selected %+v (ok=%v), want session A", e, ok)
	}
	m = typeInto(t, m, "d").(Model)
	if m.sessionsP.armed != idA {
		t.Fatalf("armed = %q, want %q", m.sessionsP.armed, idA)
	}
	m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.sessionsP.armed != "" || !m.sessionsP.open {
		t.Fatalf("esc should disarm without closing: armed=%q open=%v", m.sessionsP.armed, m.sessionsP.open)
	}
	m = typeInto(t, m, "d").(Model)
	m = apply(t, m, tea.KeyPressMsg{Code: 'y'})
	if m.sessionsP.armed != "" {
		t.Fatalf("confirmed delete left armed=%q", m.sessionsP.armed)
	}
	if _, _, err := store.LoadEntries(idA); err == nil {
		t.Error("deleted session still loads from the store")
	}
}
