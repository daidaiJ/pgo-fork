package tui

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/smallnest/pigo/internal/agenttool"
	"github.com/smallnest/pigo/internal/questionnaire"
)

// This file implements the TUI side of the ask_user questionnaire (T4.2,
// questionnaire-ask-user.md §4.2): the teaAskPort QuestionPort that hands the
// questionnaire to the tea loop, and the askPanel state machine that renders it
// above the working spinner (the subagent-panel slot) and walks the user
// through one step at a time.
//
// Threading: the ask_user tool blocks on Port.Ask from the run goroutine; the
// questionnaire crosses into the tea goroutine as an askUserMsg carried by a
// waitAsk Cmd (the same shape as the remote-control listener), and the user's
// reply crosses back over the replies channel. Exactly one waitAsk is kept in
// flight for the whole program lifetime (re-issued after every consumption),
// so a request is never lost and no goroutine accumulates.

// askUserMsg carries a questionnaire that arrived from a running ask_user tool.
type askUserMsg struct {
	q questionnaire.Questionnaire
}

// teaAskPort is the TUI QuestionPort: a request channel the waitAsk Cmd reads
// and a reply channel the panel's final submission feeds. The request channel
// has capacity 1 so a stale request from a cancelled run never blocks the tool
// goroutine; the model drops such a request when no run is in flight.
type teaAskPort struct {
	requests chan questionnaire.Questionnaire
	replies  chan questionnaire.Reply
}

// compile-time check: the TUI port satisfies the tool-side seam.
var _ agenttool.QuestionPort = (*teaAskPort)(nil)

// newTeaAskPort builds the port pair. Run installs it on the session's
// ask_user tool via run.SetAskPort before the first run starts.
func newTeaAskPort() *teaAskPort {
	return &teaAskPort{
		requests: make(chan questionnaire.Questionnaire, 1),
		replies:  make(chan questionnaire.Reply, 1),
	}
}

// Ask implements agenttool.QuestionPort: hand the questionnaire to the tea
// loop, then block until the user's reply or ctx cancellation (the two-stage
// run interrupt unblocks the wait).
func (p *teaAskPort) Ask(ctx context.Context, q questionnaire.Questionnaire) (questionnaire.Reply, error) {
	select {
	case p.requests <- q:
	case <-ctx.Done():
		return questionnaire.Reply{}, ctx.Err()
	}
	select {
	case r := <-p.replies:
		return r, nil
	case <-ctx.Done():
		return questionnaire.Reply{}, ctx.Err()
	}
}

// waitAsk returns a tea.Cmd that blocks until the next questionnaire arrives.
// The model re-issues it after every consumption (answer or stale drop), so
// exactly one listener exists at any time; it returns nil when no port is
// wired (session-less models).
func (m Model) waitAsk() tea.Cmd {
	if m.askPort == nil {
		return nil
	}
	return func() tea.Msg {
		q, ok := <-m.askPort.requests
		if !ok {
			return nil
		}
		return askUserMsg{q: q}
	}
}

// askPanel is the interactive question state machine. It holds the whole
// questionnaire plus the per-step draft: the current step index, the answers
// accumulated so far, the live multi-select set, and the free-text entry
// buffer (otherMode). A nil panel or an exhausted step cursor renders nothing
// and captures no keys.
type askPanel struct {
	q       questionnaire.Questionnaire
	step    int // index into q.Steps
	answers []questionnaire.Answer
	// picked holds the selected option ids for the current multiple-mode step.
	picked map[string]bool
	// otherMode routes printable keys into otherBuf instead of driving
	// options; Enter commits, Esc leaves.
	otherMode bool
	otherBuf  string
	// note is a one-line transient hint (invalid choice feedback).
	note string
}

// newAskPanel builds the panel for q with a fresh draft.
func newAskPanel(q questionnaire.Questionnaire) *askPanel {
	return &askPanel{q: q, picked: make(map[string]bool)}
}

// active reports whether the panel should render / capture keys.
func (p *askPanel) active() bool { return p != nil && p.step < len(p.q.Steps) }

// stepView returns the current step.
func (p *askPanel) stepView() questionnaire.Step { return p.q.Steps[p.step] }

// recommended returns the index of the step's recommended option, or -1.
func (p *askPanel) recommended(s questionnaire.Step) int {
	for i, o := range s.Options {
		if o.Recommended {
			return i
		}
	}
	return -1
}

// handleKey drives the panel. It reports whether the key was consumed, and
// sets done to the finished reply when the last step was submitted (the caller
// sends it back over the port and clears the panel).
func (p *askPanel) handleKey(msg tea.KeyPressMsg) (consumed bool, done *questionnaire.Reply) {
	if !p.active() {
		return false, nil
	}
	if p.otherMode {
		return p.handleOtherKey(msg), nil
	}
	s := p.stepView()
	switch msg.String() {
	case "1", "2", "3", "4", "5", "6", "7", "8", "9":
		n, err := strconv.Atoi(msg.String())
		if err != nil || n < 1 || n > len(s.Options) {
			p.note = "no such option"
			return true, nil
		}
		p.note = ""
		if s.SelectionMode == questionnaire.Multiple {
			id := s.Options[n-1].ID
			p.picked[id] = !p.picked[id]
			return true, nil
		}
		return true, p.commit(questionnaire.Answer{StepID: s.ID, SelectedOptionIDs: []string{s.Options[n-1].ID}})
	case "enter":
		if s.SelectionMode == questionnaire.Multiple && len(p.picked) > 0 {
			return true, p.commit(questionnaire.Answer{StepID: s.ID, SelectedOptionIDs: pickedIDs(s, p.picked)})
		}
		if rec := p.recommended(s); rec >= 0 {
			return true, p.commit(questionnaire.Answer{StepID: s.ID, SelectedOptionIDs: []string{s.Options[rec].ID}})
		}
		p.note = "pick an option number"
		return true, nil
	case "o", "O":
		if !s.AllowOther {
			p.note = "this question has no free-text entry"
			return true, nil
		}
		p.otherMode = true
		p.otherBuf = ""
		p.note = ""
		return true, nil
	case "s", "S":
		p.note = ""
		return true, p.commit(questionnaire.Answer{StepID: s.ID, Skipped: true})
	}
	return false, nil
}

// handleOtherKey edits the free-text buffer: Enter commits the text, Esc
// leaves the mode, Backspace deletes, and any printable text appends. It
// always consumes the key while the mode is on.
func (p *askPanel) handleOtherKey(msg tea.KeyPressMsg) bool {
	switch msg.String() {
	case "enter":
		if text := strings.TrimSpace(p.otherBuf); text != "" {
			p.otherMode = false
			p.commitText(text)
			return true
		}
		p.note = "empty — type something or esc"
		return true
	case "esc":
		p.otherMode = false
		p.otherBuf = ""
		return true
	case "backspace", "delete":
		if r := []rune(p.otherBuf); len(r) > 0 {
			p.otherBuf = string(r[:len(r)-1])
		}
		return true
	}
	if msg.Text != "" {
		p.otherBuf += msg.Text
		p.note = ""
	}
	return true
}

// commit appends the answer, advances to the next step (resetting the draft),
// and returns the finished reply after the last step.
func (p *askPanel) commit(a questionnaire.Answer) *questionnaire.Reply {
	p.answers = append(p.answers, a)
	p.picked = make(map[string]bool)
	p.step++
	if !p.active() {
		return p.finish()
	}
	return nil
}

// commitText commits an Other answer for the current step.
func (p *askPanel) commitText(text string) *questionnaire.Reply {
	return p.commit(questionnaire.Answer{StepID: p.q.Steps[p.step].ID, OtherText: text})
}

// finish builds the reply over the accumulated answers.
func (p *askPanel) finish() *questionnaire.Reply {
	return &questionnaire.Reply{Title: p.q.Title, Answers: p.answers, Source: questionnaire.SourceUser}
}

// pickedIDs extracts the picked option ids in the step's declared order (so
// the rendered answer reads 1, 3 rather than map order).
func pickedIDs(s questionnaire.Step, picked map[string]bool) []string {
	var ids []string
	for _, o := range s.Options {
		if picked[o.ID] {
			ids = append(ids, o.ID)
		}
	}
	return ids
}

// view renders the panel: a "waiting for your answer" badge (the unified
// pending-confirmation color, Warn), the current step, its options, and the
// key hints. Multi-line; the caller reserves its rows via lineCount.
func (p *askPanel) view(theme Theme, width int) string {
	if !p.active() {
		return ""
	}
	s := p.stepView()
	var b strings.Builder
	badge := theme.Warn.Render("⏸ waiting for your answer")
	fmt.Fprintf(&b, "%s  ·  question %d/%d", badge, p.step+1, len(p.q.Steps))
	if p.q.Title != "" {
		fmt.Fprintf(&b, "  ·  %s", theme.System.Render(p.q.Title))
	}
	b.WriteString("\n")
	if s.Header != "" {
		fmt.Fprintf(&b, "%s\n", theme.Accent.Render(s.Header))
	}
	fmt.Fprintf(&b, "%s\n", theme.User.Render(WrapToWidth(s.Question, width)))
	if s.Description != "" {
		fmt.Fprintf(&b, "%s\n", theme.System.Render(WrapToWidth(s.Description, width-2)))
	}
	if p.otherMode {
		fmt.Fprintf(&b, "  other: %s▏\n", p.otherBuf)
		fmt.Fprintf(&b, "%s\n", theme.System.Render("  enter=commit  esc=cancel"))
	} else {
		rec := p.recommended(s)
		for i, o := range s.Options {
			mark := " "
			if s.SelectionMode == questionnaire.Multiple && p.picked[o.ID] {
				mark = "x"
			}
			line := WrapToWidth(o.Label, width)
			if o.Description != "" {
				line += theme.System.Render(" — " + o.Description)
			}
			if i == rec {
				line += theme.Success.Render(" (recommended)")
			}
			fmt.Fprintf(&b, "  [%s] %d) %s\n", mark, i+1, line)
		}
		hint := "  1-9 select"
		if s.SelectionMode == questionnaire.Multiple {
			hint += " (toggle) · enter=confirm selection"
		} else if rec >= 0 {
			hint += " · enter=recommended"
		}
		if s.AllowOther {
			hint += " · o=other"
		}
		hint += " · s=skip"
		fmt.Fprintf(&b, "%s\n", theme.System.Render(hint))
	}
	if p.note != "" {
		fmt.Fprintf(&b, "%s\n", theme.Warn.Render("  "+p.note))
	}
	return strings.TrimRight(b.String(), "\n")
}

// lineCount reports how many terminal rows the panel currently occupies so
// relayout can reserve them from the transcript. It uses a canonical width for
// the estimate; wrapped lines may make the real render taller, which only
// costs a transient overlap until the next relayout.
func (p *askPanel) lineCount() int {
	if p == nil || !p.active() {
		return 0
	}
	return strings.Count(p.view(DefaultTheme(), 80), "\n") + 1
}
