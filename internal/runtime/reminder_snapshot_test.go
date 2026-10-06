package runtime

// Tests for the T3.5 随件③ post-compaction reminder upgrade: when the
// readFileState ledger holds content snapshots for the recently-read files the
// reminder carries those contents (zcode form, closing deviation D-5); without
// a snapshot it degrades to the original path-reference form.

import (
	"strings"
	"testing"
	"time"

	"github.com/smallnest/pigo/internal/agentcore"
)

func reminderRangeMsgs(paths ...string) []agentcore.Message {
	msgs := []agentcore.Message{
		agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("go")}},
	}
	for i, p := range paths {
		args := `{"path":"` + p + `"}`
		msgs = append(msgs, agentcore.AssistantMessage{
			RoleField: agentcore.RoleAssistant,
			Content: agentcore.ContentList{
				agentcore.NewTextContent("reading"),
				agentcore.ToolCallContent{Type: "toolCall", ID: string(rune('a'+i)), Name: "read", Arguments: []byte(args)},
			},
			StopReason: agentcore.StopReasonToolUse,
		})
	}
	return msgs
}

func TestPostCompactReminderCarriesSnapshots(t *testing.T) {
	st := agentcore.NewReadFileState()
	st.RecordRead(agentcore.ReadRecord{
		CallID: "a", ArgPath: "a.go", ResolvedPath: "/w/a.go",
		Content: "package a\n\nfunc A() {}\n", ModTime: time.Now(), Size: 22,
	})
	// b.go was read through the tool but its snapshot is absent (e.g. restored
	// session): the reminder degrades that one entry to a reference line.
	st.RecordRead(agentcore.ReadRecord{
		CallID: "b", ArgPath: "b.go", ResolvedPath: "/w/b.go",
		Content: "", ModTime: time.Now(), Size: 5,
	})

	body := postCompactReminder(reminderRangeMsgs("a.go", "b.go"), st)
	if !strings.Contains(body, "<system-reminder>") {
		t.Fatal("reminder must stay wrapped")
	}
	if !strings.Contains(body, "package a\n\nfunc A() {}") {
		t.Errorf("snapshot content for a.go must be injected, got %q", body)
	}
	if !strings.Contains(body, "b.go (no content snapshot; re-read if needed)") {
		t.Errorf("b.go without a snapshot must degrade to a reference line, got %q", body)
	}
}

func TestPostCompactReminderPerFileCap(t *testing.T) {
	st := agentcore.NewReadFileState()
	st.RecordRead(agentcore.ReadRecord{
		CallID: "a", ArgPath: "big.go", ResolvedPath: "/w/big.go",
		Content: strings.Repeat("x", 6*1024), // below the 8K snapshot cap, above the 5K injection cap
		ModTime: time.Now(), Size: 6 * 1024,
	})
	body := postCompactReminder(reminderRangeMsgs("big.go"), st)
	if !strings.Contains(body, "… (snapshot truncated)") {
		t.Errorf("oversized snapshot must be truncated with a visible mark, got %d chars", len(body))
	}
	if len(body) > reminderMaxFileChars+1024 {
		t.Errorf("injected snapshot must respect the per-file cap, got %d chars", len(body))
	}
}

func TestPostCompactReminderNilLedgerKeepsReferenceForm(t *testing.T) {
	// No ledger (standalone use, or a driver that never ran the loop): the
	// original path-reference wording must be preserved verbatim.
	body := postCompactReminder(reminderRangeMsgs("a.go"), nil)
	if !strings.Contains(body, "whose exact contents may no longer be in context") {
		t.Errorf("nil ledger must keep the reference-hint form, got %q", body)
	}
	if !strings.Contains(body, "- a.go") {
		t.Errorf("reference form must list the path, got %q", body)
	}
	if strings.Contains(body, "package a") {
		t.Error("no content must be injected without a ledger")
	}
}
