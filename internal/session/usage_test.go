package session

import (
	"os"
	"testing"
	"time"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/statline"
	"github.com/smallnest/pigo/internal/testenv"
)

// TestUsageLedgerIsNotASessionFile pins the ledger's naming contract: the
// <id>.usage.jsonl sidecar (O1/T7.3c) lives in the sessions directory but is
// never listed as a session, and Delete takes it with the session.
func TestUsageLedgerIsNotASessionFile(t *testing.T) {
	store, err := NewStore(testenv.Dir(t))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	now := time.Now().UTC()
	header := SessionHeader{ID: NewID(now), CreatedAt: now, UpdatedAt: now}
	if err := store.Save(header, agentcore.MessageList{
		agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("hi")}},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := statline.AppendUsage(store.Dir(), header.ID, statline.Record{At: now, Input: 7, Output: 2}); err != nil {
		t.Fatalf("AppendUsage: %v", err)
	}
	// The ledger exists on disk...
	path := statline.UsagePath(store.Dir(), header.ID)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("ledger not written: %v", err)
	}
	// ...but the listing sees exactly one session (the ledger is filtered).
	headers, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(headers) != 1 || headers[0].ID != header.ID {
		t.Fatalf("List = %+v, want exactly the one session %s", headers, header.ID)
	}
	// A delete removes the session and its ledger together.
	if err := store.Delete(header.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("ledger survived Delete (stat err = %v)", err)
	}
}
