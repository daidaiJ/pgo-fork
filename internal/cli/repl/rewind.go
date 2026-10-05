// This file implements the /rewind command (edit checkpoint / rewind): pigo's
// analogue of Claude Code's Esc-Esc rewind. Where /tree only moves the
// conversation leaf, /rewind also restores the working tree — it replays the
// file-snapshot journal (see agenttool.FileSnapshotRecorder) so a turn's write
// and edit mutations are rolled back, then switches the active conversation leaf
// to the point before that turn. The two together return the session to an
// earlier state in code and dialogue at once.
//
// The restore-point list is derived from the session tree (cli.DeriveRewindPoints),
// not from the in-memory snapshot journal: every user turn on every branch is
// listed, with abandoned branches marked ↩ (selecting one switches back to it)
// and points earlier than the most recent compaction marked ⚠ (context rebuilds
// from the summary). After a rewind the turn's prompt is handed back to the line
// editor so the user can edit and resend it.
//
// Scope (v1): only pigo's own write/edit tools are journaled. Files changed by
// bash commands are not captured and are left untouched by a rewind.
package repl

import (
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/smallnest/pigo/internal/agenttool"
	"github.com/smallnest/pigo/internal/cli"
)

// runRewind handles the /rewind command. With no argument it persists the live
// turn and prints the numbered restore points (most useful last). With "/rewind
// N" it restores files to their state before the N-th listed turn, switches the
// conversation to the leaf that preceded it, and puts the turn's prompt back
// into the input line.
func runRewind(out io.Writer, deps *replDeps, line string) {
	// Persist any un-saved turn first so the just-run turn's restore point exists
	// and the leaf ids we switch to are on disk.
	cli.PersistTurn(out, deps)

	var snaps []agenttool.RestorePoint
	if deps.snap != nil {
		snaps = deps.snap.Points()
	}
	points, err := cli.DeriveRewindPoints(deps.store, deps.header.ID, deps.curLeaf, snaps)
	if err != nil {
		fmt.Fprintf(out, "pigo: cannot read session tree: %v\n", err)
		return
	}

	fields := strings.Fields(line)
	if len(fields) < 2 {
		cli.PrintRewindPoints(out, points)
		return
	}
	if len(points) == 0 {
		fmt.Fprintln(out, "no restore points yet — prompts create them")
		return
	}

	n, err := strconv.Atoi(fields[1])
	if err != nil || n < 1 || n > len(points) {
		fmt.Fprintf(out, "invalid selection %q — run /rewind to list points (1..%d)\n", fields[1], len(points))
		return
	}
	p := points[n-1]

	// Roll the working tree back to the state before this turn: replay this
	// turn's snapshots and every later one. Snapshot-less points (turns that
	// touched no files, or whose journal was consumed by an earlier rewind)
	// change no files.
	if deps.snap != nil && p.SnapFrom >= 0 {
		_, restored, warnings, rErr := deps.snap.Restore(p.SnapFrom)
		if rErr != nil {
			fmt.Fprintf(out, "pigo: rewind failed: %v\n", rErr)
			return
		}
		if len(restored) > 0 {
			fmt.Fprintf(out, "restored %d file(s):\n", len(restored))
			for _, path := range restored {
				fmt.Fprintf(out, "  %s\n", displayPath(deps.cwd, path))
			}
		} else {
			fmt.Fprintln(out, "no files to restore for this point")
		}
		for _, w := range warnings {
			fmt.Fprintf(out, "  warning: %s\n", w)
		}
	} else {
		fmt.Fprintln(out, "no files to restore for this point")
	}

	// Move the conversation back to the leaf that preceded the turn, rebuilding the
	// shared context from that leaf's root→leaf path (same mechanism as /tree). An
	// empty leaf id means the turn was the first in the session: reset to an empty
	// conversation.
	if !rewindConversation(out, deps, p.LeafID) {
		return
	}
	fmt.Fprintf(out, "rewound to before point %d — next prompt continues from here\n", n)
	if p.Lossy {
		fmt.Fprintln(out, "  note: this point predates a compaction; context was rebuilt from the summary")
	}
	if p.Prompt != "" && deps.editor != nil {
		deps.editor.prefill = p.Prompt
	}
}

// rewindConversation switches the active leaf to leafID and rebuilds the shared
// context from its path. A "" leafID resets to an empty conversation (the turn
// was the session's first). It reports whether the switch succeeded.
func rewindConversation(out io.Writer, deps *replDeps, leafID string) bool {
	if leafID == "" {
		deps.agentCtx.Messages = nil
		deps.curLeaf = ""
		deps.persisted = 0
		return true
	}
	msgs, found, err := cli.LoadLeafPath(deps.store, deps.header.ID, leafID)
	if err != nil {
		fmt.Fprintf(out, "pigo: cannot read session tree: %v\n", err)
		return false
	}
	if !found {
		fmt.Fprintf(out, "pigo: restore point's conversation node is no longer in the tree; files were restored but the conversation was left unchanged\n")
		return false
	}
	deps.agentCtx.Messages = msgs
	deps.curLeaf = leafID
	deps.persisted = len(msgs)
	return true
}

// displayPath shortens an absolute snapshot path to a workspace-relative form for
// display when it lives under cwd; otherwise it returns the absolute path.
func displayPath(cwd, abs string) string {
	if cwd == "" {
		return abs
	}
	if rel, err := filepath.Rel(cwd, abs); err == nil && !strings.HasPrefix(rel, "..") {
		return rel
	}
	return abs
}
