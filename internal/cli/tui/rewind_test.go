// Tests for the TUI /rewind interception (T3.1 G4): listing tree-derived
// restore points and switching the conversation back to a selected one, with
// the turn's prompt refilled into the input line. The TUI keeps no file
// snapshot journal, so rewind here is conversation-only.
package tui

import (
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/session"
)

// rewindFixture builds a model with an active session holding one persisted
// user turn ("hello there") as the session's first branch entry.
func rewindFixture(t *testing.T) Model {
	t.Helper()
	store, err := session.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	s, _, err := newRunSessionWithStore(store, Options{})
	if err != nil {
		t.Fatalf("newRunSessionWithStore: %v", err)
	}
	msg := agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("hello there")}}
	leaf, err := s.store.AppendBranch(s.header, "", agentcore.MessageList{msg})
	if err != nil {
		t.Fatalf("AppendBranch: %v", err)
	}
	s.agentCtx.Messages = agentcore.MessageList{msg}
	s.persisted = 1
	s.curLeaf = leaf
	m := NewModel(Options{})
	m.session = s
	return m
}

func transcriptText(m Model) string {
	return strings.Join(blockTexts(m.transcript), "\n")
}

// /rewind with no argument lists the tree-derived restore points.
func TestSlashRewindListsPoints(t *testing.T) {
	m := rewindFixture(t)
	got, _ := m.runSlash("/rewind")
	joined := transcriptText(got.(Model))
	if !strings.Contains(joined, "restore points") || !strings.Contains(joined, "hello there") {
		t.Errorf("listing missing expected content:\n%s", joined)
	}
}

// /rewind N moves the conversation back before the selected turn and refills
// the input line with that turn's prompt.
func TestSlashRewindSwitchesAndRefills(t *testing.T) {
	m := rewindFixture(t)
	got, _ := m.runSlash("/rewind 1")
	gm := got.(Model)
	joined := transcriptText(gm)
	if !strings.Contains(joined, "rewound to before point 1") {
		t.Errorf("missing confirmation:\n%s", joined)
	}
	if len(gm.session.agentCtx.Messages) != 0 || gm.session.curLeaf != "" || gm.session.persisted != 0 {
		t.Errorf("conversation not rewound: %d messages, leaf %q, persisted %d",
			len(gm.session.agentCtx.Messages), gm.session.curLeaf, gm.session.persisted)
	}
	if v := gm.input.Value(); v != "hello there" {
		t.Errorf("input not refilled: got %q", v)
	}
}
