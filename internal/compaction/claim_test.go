package compaction

// Projection tests for the ToolClaimMessage entries (T4.1): claim records are
// history (persisted, replayed) but never request messages.

import (
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
)

func claimRecord(tools ...string) agentcore.ToolClaimMessage {
	return agentcore.ToolClaimMessage{RoleField: agentcore.RoleToolClaim, Tools: tools, Timestamp: 1}
}

func TestProjectViewDropsToolClaimRecords(t *testing.T) {
	msgs := agentcore.MessageList{
		agentcore.UserMessage{RoleField: agentcore.RoleUser,
			Content: agentcore.ContentList{agentcore.NewTextContent("go")}},
		agentcore.ToolClaimMessage{RoleField: agentcore.RoleToolClaim, Tools: []string{"a"}, Timestamp: 1},
		claimRecord("plug_a", "plug_b"),
		agentcore.AssistantMessage{RoleField: agentcore.RoleAssistant, StopReason: agentcore.StopReasonEndTurn,
			Content: agentcore.ContentList{agentcore.NewTextContent("done")}},
	}
	view := ProjectView(msgs)
	if len(view) != len(msgs)-2 {
		t.Fatalf("want %d view entries (both claim records dropped), got %d", len(msgs)-2, len(view))
	}
	for _, m := range view {
		if _, ok := m.(agentcore.ToolClaimMessage); ok {
			t.Fatal("claim record must never enter the view")
		}
	}
	// Raw history stays lossless.
	if len(msgs) != 4 {
		t.Fatalf("raw list must stay untouched, got %d", len(msgs))
	}
}
