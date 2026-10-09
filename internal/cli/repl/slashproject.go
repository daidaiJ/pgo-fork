// Slash-face projections for the REPL (T7.7 slice 3, spec
// wiki/port/slash-command-surface.md §4.5): a loop face the REPL cannot run
// interactively — the TUI pickers and overlays — degrades to a candidate list
// plus a usage hint. It never opens a picker, never accepts a numeric
// selection and never prompts: the interactive form stays the TUI's, and the
// REPL only advertises what could be chosen there (R12: the interactive form
// is the command's primary identity; non-interactive surfaces only project a
// degraded form).
package repl

import (
	"fmt"
	"io"

	"github.com/smallnest/pigo/internal/cli/prompts"
	"github.com/smallnest/pigo/internal/runtime"
)

// projectTextFace prints the REPL's degraded projection of a declared TUI-face
// command: the candidate list this front-end can produce from its own state,
// the usage line, and the explicit availability notice (§6: execution or
// explicit rejection, never a silent no-op).
func projectTextFace(out io.Writer, deps *replDeps, cmd runtime.SlashCommand) {
	if cmd.Projection == runtime.ProjSessionsPicker {
		writeSessionCandidates(out, deps)
	}
	fmt.Fprintf(out, "usage: %s\n", prompts.FormatCommandLine(cmd))
	fmt.Fprintln(out, cmd.Projection.UnavailableNotice(cmd.Name))
}

// writeSessionCandidates lists the saved sessions as the /sessions and /resume
// candidate list: id, model, update time and title, the current session marked,
// followed by the shell invocation that actually resumes one (the REPL has no
// in-session session switch). A read failure is reported rather than swallowed.
func writeSessionCandidates(out io.Writer, deps *replDeps) {
	if deps.store == nil {
		return
	}
	headers, err := deps.store.List()
	if err != nil {
		fmt.Fprintf(out, "sessions: %v\n", err)
		return
	}
	if len(headers) == 0 {
		fmt.Fprintln(out, "no saved sessions")
		return
	}
	fmt.Fprintln(out, "saved sessions (resume with: pigo --resume <id>):")
	for _, h := range headers {
		line := "  " + h.ID
		if h.Model != "" {
			line += "  " + h.Model
		}
		if !h.UpdatedAt.IsZero() {
			line += "  " + h.UpdatedAt.Local().Format("2006-01-02 15:04")
		}
		if h.Title != "" {
			line += "  " + h.Title
		}
		if h.ID == deps.header.ID {
			line += "  (current)"
		}
		fmt.Fprintln(out, line)
	}
}
