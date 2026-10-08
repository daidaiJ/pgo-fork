// Tests for the tree-derived rewind restore points (T3.1 G1+G3): user turns on
// every branch become points, abandoned branches and pre-compaction points are
// flagged, and file snapshots associate to their turn's anchor.
package cli

import (
	"testing"
	"time"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/agenttool"
	"github.com/smallnest/pigo/internal/session"
	"github.com/smallnest/pigo/internal/testenv"
)

func TestDeriveRewindPointsTree(t *testing.T) {
	store, err := session.NewStore(testenv.Dir(t))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	user := func(s string) agentcore.UserMessage {
		return agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent(s)}}
	}
	// Tree: e1 "one" ← e2 (compaction); e3 "two" is a sibling branch under e1.
	entries := []session.Entry{
		{ID: "e1", Timestamp: now, Message: user("one")},
		{ID: "e2", ParentID: "e1", Timestamp: now.Add(time.Second), Message: agentcore.CompactionMessage{RoleField: agentcore.RoleCompaction, Summary: "summary"}},
		{ID: "e3", ParentID: "e1", Timestamp: now.Add(2 * time.Second), Message: user("two")},
	}
	header := session.SessionHeader{ID: session.NewID(now)}
	if err := store.SaveEntries(header, entries); err != nil {
		t.Fatal(err)
	}
	// One live snapshot tagged with the leaf before turn e3 ("two").
	snaps := []agenttool.RestorePoint{{LeafID: "e1", Time: now.Add(3 * time.Second)}}

	points, err := DeriveRewindPoints(store, header.ID, "e3", snaps)
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 2 {
		t.Fatalf("want 2 points, got %d: %+v", len(points), points)
	}
	one, two := points[0], points[1]
	if one.Prompt != "one" || two.Prompt != "two" {
		t.Errorf("points out of order: %q then %q", one.Prompt, two.Prompt)
	}
	// e1 is an ancestor of the active leaf e3, so only... both are on the
	// active path; neither is abandoned.
	if one.Abandoned || two.Abandoned {
		t.Errorf("active-path points must not be abandoned: %+v", points)
	}
	// The compaction under e1 makes every point at-or-before it lossy.
	if !one.Lossy || two.Lossy {
		t.Errorf("lossy flags wrong: one=%v two=%v", one.Lossy, two.Lossy)
	}
	// The snapshot belongs to turn e3; rewinding to e1 must replay it too.
	if two.SnapFrom != 0 || one.SnapFrom != 0 {
		t.Errorf("SnapFrom wrong: one=%d two=%d", one.SnapFrom, two.SnapFrom)
	}
	if two.Files != 0 || one.Files != 0 {
		t.Errorf("snapshot with no files must contribute 0: %+v", points)
	}

	// From the compaction leaf e2's perspective, the sibling turn "two" is an
	// abandoned branch; the ancestor "one" stays on the active path.
	points, err = DeriveRewindPoints(store, header.ID, "e2", snaps)
	if err != nil {
		t.Fatal(err)
	}
	if points[0].Abandoned {
		t.Errorf("ancestor point %q must not be abandoned", points[0].Prompt)
	}
	if !points[1].Abandoned {
		t.Errorf("sibling point %q should be abandoned from leaf e2", points[1].Prompt)
	}
}
