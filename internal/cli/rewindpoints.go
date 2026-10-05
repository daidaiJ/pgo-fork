// This file derives /rewind restore points from the persisted session tree
// (T3.1 G1+G3). The REPL's FileSnapshotRecorder only knows turns that touched
// files and drops consumed points on restore, so listing from it alone hides
// abandoned branches and turns without file edits. Deriving from the tree
// instead lists every user turn on every branch: points on the active path are
// the usual rewind targets, points off it are abandoned branches the user can
// switch back to (AppendBranch semantics keep their entries on disk), and each
// point carries the file snapshots still alive for its turn. Points earlier
// than the most recent compaction on their branch are flagged lossy (G3):
// rewinding to them rebuilds context from the compaction summary, not verbatim.
package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/agenttool"
	"github.com/smallnest/pigo/internal/session"
)

// RewindPoint is one selectable /rewind target: a user turn somewhere in the
// session tree. Rewinding to it restores the files its turn (and any later
// turn) changed, switches the conversation leaf to LeafID (the state before
// the turn), and refills the input with the turn's prompt for edit-and-resend.
type RewindPoint struct {
	// Number is the 1-based selection number in the printed list.
	Number int
	// LeafID is the conversation leaf to switch to (the anchor entry's parent).
	// Empty means the session's first turn: rewind resets to an empty
	// conversation.
	LeafID string
	// Prompt is the turn's full user prompt text, for refilling the input line.
	Prompt string
	// Label is a single-line scannable form of Prompt for the list.
	Label string
	// Time is when the turn's user entry was persisted.
	Time time.Time
	// Files is the number of file snapshots still alive for this turn (0 when
	// the turn touched no files or its snapshots were consumed by an earlier
	// rewind).
	Files int
	// Abandoned marks a point off the active leaf's path: the session has
	// branched away from it and selecting it switches back to that branch.
	Abandoned bool
	// Lossy marks a point earlier than the most recent compaction on its
	// branch: rewinding to it rebuilds context from the compaction summary
	// rather than the verbatim entries.
	Lossy bool
	// SnapFrom is the index into the snapshot slice passed to
	// DeriveRewindPoints at which file restore starts for this point (-1 when
	// there is nothing to restore). FileSnapshotRecorder.Restore replays that
	// index and every later point, matching the chronological-suffix replay
	// the source projects use.
	SnapFrom int
}

// RewindLabel collapses a prompt to one scannable line for the /rewind list.
func RewindLabel(prompt string) string {
	label := strings.Join(strings.Fields(prompt), " ")
	const max = 60
	if len(label) > max {
		label = label[:max-1] + "…"
	}
	return label
}

// LoadLeafPath returns the root→leaf message path for leafID. found is false
// when the leaf's path cannot be rebuilt (missing or cyclic ancestry).
func LoadLeafPath(store *session.Store, sessionID, leafID string) (msgs agentcore.MessageList, found bool, err error) {
	_, entries, err := store.LoadEntries(sessionID)
	if err != nil {
		return nil, false, err
	}
	path := session.PathToLeaf(entries, leafID)
	if len(path) == 0 {
		return nil, false, nil
	}
	msgs = make(agentcore.MessageList, len(path))
	for i, e := range path {
		msgs[i] = e.Message
	}
	return msgs, true, nil
}

// DeriveRewindPoints lists every user turn in the session tree as a rewind
// point, oldest first. curLeaf is the active leaf ("" for an empty
// conversation); snaps are the file-snapshot recorder's currently alive
// points, oldest first. Each point's SnapFrom is the first snapshot index to
// replay when rewinding to it.
func DeriveRewindPoints(store *session.Store, sessionID, curLeaf string, snaps []agenttool.RestorePoint) ([]RewindPoint, error) {
	_, entries, err := store.LoadEntries(sessionID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []RewindPoint{}, nil // brand-new session, nothing on disk yet
		}
		return nil, err
	}
	byIdx := make(map[string]int, len(entries))
	children := make(map[string][]int)
	for i, e := range entries {
		byIdx[e.ID] = i
		children[e.ParentID] = append(children[e.ParentID], i)
	}

	// Turn anchors: every persisted user message is a rewind target.
	var anchors []int
	for i, e := range entries {
		if e.Message.Role() == agentcore.RoleUser {
			anchors = append(anchors, i)
		}
	}
	sort.SliceStable(anchors, func(a, b int) bool {
		return entries[anchors[a]].Timestamp.Before(entries[anchors[b]].Timestamp)
	})

	// Abandoned: not an ancestor-or-self of the active leaf.
	active := make(map[string]bool)
	for _, e := range session.PathToLeaf(entries, curLeaf) {
		active[e.ID] = true
	}

	// Lossy: a compaction entry exists at or below the anchor, so context
	// rebuilt from that point onward passes through the summary.
	lossy := make(map[int]bool)
	for _, a := range anchors {
		stack := append([]int(nil), children[entries[a].ID]...)
		for len(stack) > 0 {
			i := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if entries[i].Message.Role() == agentcore.RoleCompaction {
				lossy[a] = true
				break
			}
			stack = append(stack, children[entries[i].ID]...)
		}
	}

	// Snapshot association: a recorder point is tagged with the leaf before
	// its turn, which is the anchor entry's parent. A parent can host several
	// anchors (rewind then re-prompt from the same leaf), so prefer the anchor
	// persisted nearest the snapshot's commit time.
	snapFrom := make(map[int]int, len(anchors))
	files := make(map[int]int, len(anchors))
	for si, sp := range snaps {
		var best int = -1
		for _, a := range anchors {
			if entries[a].ParentID != sp.LeafID {
				continue
			}
			if best == -1 || betterSnapAnchor(entries[a].Timestamp, entries[best].Timestamp, sp.Time) {
				best = a
			}
		}
		if best == -1 {
			continue
		}
		files[best] = len(sp.Snapshots)
		// This snapshot — and every later one chronologically after it — must
		// be replayed when rewinding to this anchor or any of its ancestors.
		for i := best; i != -1; i = parentAnchor(anchors, entries, i) {
			if _, ok := snapFrom[i]; !ok {
				snapFrom[i] = si
			}
		}
	}

	points := make([]RewindPoint, 0, len(anchors))
	for n, a := range anchors {
		e := entries[a]
		prompt := agentcore.ContentToText(userContent(e.Message))
		points = append(points, RewindPoint{
			Number:    n + 1,
			LeafID:    e.ParentID,
			Prompt:    prompt,
			Label:     RewindLabel(prompt),
			Time:      e.Timestamp,
			Files:     files[a],
			Abandoned: !active[e.ID],
			Lossy:     lossy[a],
			SnapFrom:  -1,
		})
		if sf, ok := snapFrom[a]; ok {
			points[n].SnapFrom = sf
		}
	}
	return points, nil
}

// PrintRewindPoints renders the numbered restore points, oldest first, with
// time, file count, ↩/⚠ markers, and label — shared by the REPL and TUI. The
// file count is omitted for points without live snapshots (turns that touched
// no files, or entrypoints with no snapshot journal such as the TUI).
func PrintRewindPoints(out io.Writer, points []RewindPoint) {
	if len(points) == 0 {
		fmt.Fprintln(out, "no restore points yet — prompts create them")
		return
	}
	fmt.Fprintln(out, "restore points (run /rewind <n> to roll files + conversation back to before that point):")
	for _, p := range points {
		when := p.Time.Local().Format(time.Kitchen)
		label := p.Label
		if label == "" {
			label = "(no prompt)"
		}
		status := ""
		if p.Abandoned {
			status = "↩ abandoned branch  "
		}
		if p.Lossy {
			status += "⚠ pre-compaction (context rebuilds from summary)  "
		}
		files := ""
		if p.Files > 0 {
			unit := "files"
			if p.Files == 1 {
				unit = "file"
			}
			files = fmt.Sprintf("%d %s  ", p.Files, unit)
		}
		fmt.Fprintf(out, "  %d. %s  %s%s%s\n", p.Number, when, status, files, label)
	}
}

func userContent(m agentcore.Message) agentcore.ContentList {
	if u, ok := m.(agentcore.UserMessage); ok {
		return u.Content
	}
	return nil
}

// betterSnapAnchor reports whether candidate's timestamp ts is a closer match
// for the snapshot's commit time than the current best: the latest anchor not
// after the commit (plus a small skew), falling back to the latest overall.
func betterSnapAnchor(ts, bestTs, commit time.Time) bool {
	limit := commit.Add(2 * time.Second)
	switch {
	case ts.Before(limit) && bestTs.After(limit):
		return true
	case ts.After(limit) && bestTs.Before(limit):
		return false
	default:
		return ts.After(bestTs)
	}
}

// parentAnchor returns the anchor index of idx's parent entry, or -1.
func parentAnchor(anchors []int, entries []session.Entry, idx int) int {
	for _, a := range anchors {
		if entries[a].ID == entries[idx].ParentID {
			return a
		}
	}
	return -1
}
