package tui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/smallnest/pigo/internal/spans"
)

// Run starts the full-screen TUI and blocks until the user quits (Ctrl+C /
// Ctrl+D) or the program errors. It is the alt-screen counterpart to repl.Run:
// cmd/pigo's dispatch calls it on the (no prompt + TTY + no --no-tui) path and
// maps its error to the process exit code. The alt-screen is entered/left via
// the View returned by the root Model, so a clean return here restores the
// terminal to the user's prior scrollback.
func Run(opts Options) error {
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
	m.uiProbe = spans.NewProbe(uiInit)
	p := tea.NewProgram(m)
	_, err = p.Run()
	return err
}
