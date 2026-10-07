package tui

import (
	"os"

	tea "charm.land/bubbletea/v2"

	"github.com/smallnest/pigo/internal/cli/run"
	"github.com/smallnest/pigo/internal/spans"
)

// Run starts the full-screen TUI and blocks until the user quits (Ctrl+C /
// Ctrl+D) or the program errors. It is the alt-screen counterpart to repl.Run:
// cmd/pigo's dispatch calls it on the (no prompt + TTY + no --no-tui) path and
// maps its error to the process exit code. The alt-screen is entered/left via
// the View returned by the root Model, so a clean return here restores the
// terminal to the user's prior scrollback.
func Run(opts Options) error {
	// ask_user port (T4.2): the panel-backed port is installed on the session's
	// ask_user tool before any run starts; the model reads it via m.askPort
	// (nil for session-less tests, which keeps the degraded tool path).
	ask := newTeaAskPort()
	run.SetAskPort(opts.Tools, ask)
	// Assemble the session (store, resume-or-fresh context, live config) before
	// entering the alt-screen, mirroring repl.Run: a store/resume failure is a
	// clean pre-launch error rather than a broken interactive session.
	s, history, err := newRunSession(opts)
	if err != nil {
		return err
	}
	// startup.ui_init (T1.1) covers model construction through the first rendered
	// frame: the probe closes the span from the first View call (Mark "first
	// frame" + End), the closest thing to a first-frame hook bubbletea exposes.
	uiInit := spans.Begin("startup.ui_init")
	m := NewModel(opts).withSession(s, history)
	m.askPort = ask
	m.uiProbe = spans.NewProbe(uiInit)
	p := tea.NewProgram(m)
	_, err = p.Run()
	// Farewell easter egg (carried over from the grok fork's exit path): only
	// on a graceful quit — p.Run returns after the alt-screen is restored, so
	// the line lands in the user's scrollback via stderr.
	if err == nil {
		printFarewell(os.Stderr)
	}
	return err
}
