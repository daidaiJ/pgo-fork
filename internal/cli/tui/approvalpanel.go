// approvalpanel.go implements the TUI's local per-call approval channel
// (T7.6+D-C1, tui-blank-header-fixes.md §5.4): the permission engine's ask
// face when no remote browser is paired. A pending ask crosses into the tea
// loop as an approvalReqMsg carried by a waitApproval Cmd (the ask_user
// port's exact shape — one listener re-issued after every consumption), and
// the panel's answer crosses back over the replies channel.
//
// Keys: y allows the call, n / Esc / Enter deny (the REPL prompt's [y/N]
// default), a allows and grants session trust, s allows and settles the
// proposed rule (offered only when the engine proposed a pattern).
//
// Threading: the engine's ask blocks on the port from the tool-executor
// goroutine; replies carry the request's sequence id so a reply that races
// a cancelled run is discarded instead of answering the next call (the
// ask_user port has no such guard; this one is written defensively because
// approval asks are far more frequent).
package tui

import (
	"context"
	"fmt"
	"strings"
	"sync"

	tea "charm.land/bubbletea/v2"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/toolrules"
	"github.com/smallnest/pigo/internal/trust"
)

// approvalRequest is one pending ask handed to the tea loop.
type approvalRequest struct {
	id     uint64
	call   agentcore.AgentToolCall
	reason toolrules.AskReason
	hint   toolrules.ProposedHint
}

// approvalReply carries the panel's answer back to the blocked ask.
type approvalReply struct {
	id       uint64
	decision toolrules.AskDecision
	rule     toolrules.Rule
	always   bool // [a]: also grant session trust
}

// approvalPort is the TUI ask channel: a request channel the waitApproval
// Cmd reads and a reply channel the panel's answer feeds. Both are capacity
// 1 so a stale request or a raced reply never blocks its sender.
type approvalPort struct {
	requests chan approvalRequest
	replies  chan approvalReply
	mu       sync.Mutex
	seq      uint64
}

// newApprovalPort builds the channel pair.
func newApprovalPort() *approvalPort {
	return &approvalPort{
		requests: make(chan approvalRequest, 1),
		replies:  make(chan approvalReply, 1),
	}
}

// Ask implements the engine's ask face: hand the request to the tea loop,
// then block until the user's answer or ctx cancellation (the two-stage run
// interrupt unblocks the wait). Stale replies (an answer racing a cancelled
// earlier ask) are drained and discarded.
func (p *approvalPort) Ask(ctx context.Context, call agentcore.AgentToolCall, reason toolrules.AskReason, hint toolrules.ProposedHint) (toolrules.AskDecision, toolrules.Rule) {
	if ctx.Err() != nil {
		return toolrules.AskDeny, toolrules.Rule{}
	}
	p.mu.Lock()
	p.seq++
	req := approvalRequest{id: p.seq, call: call, reason: reason, hint: hint}
	p.mu.Unlock()
	select {
	case p.requests <- req:
	case <-ctx.Done():
		return toolrules.AskDeny, toolrules.Rule{}
	}
	for {
		select {
		case r := <-p.replies:
			if r.id != req.id {
				continue
			}
			return r.decision, r.rule
		case <-ctx.Done():
			return toolrules.AskDeny, toolrules.Rule{}
		}
	}
}

// respond delivers the panel's answer; a full buffer (the asker cancelled
// and no future ask will drain it) drops the reply instead of blocking the
// tea loop.
func (p *approvalPort) respond(r approvalReply) {
	select {
	case p.replies <- r:
	default:
	}
}

// approvalReqMsg carries a pending ask into the tea loop.
type approvalReqMsg struct{ req approvalRequest }

// waitApproval returns a tea.Cmd that blocks until the next ask arrives.
// The model re-issues it after every consumption (answer or stale drop), so
// exactly one listener exists at any time; nil when no session carries the
// channel.
func (m Model) waitApproval() tea.Cmd {
	if m.session == nil || m.session.approvalCh == nil {
		return nil
	}
	ch := m.session.approvalCh
	return func() tea.Msg {
		req, ok := <-ch.requests
		if !ok {
			return nil
		}
		return approvalReqMsg{req: req}
	}
}

// approvalPanel holds the one pending ask. A nil panel renders nothing and
// captures no keys.
type approvalPanel struct {
	req approvalRequest
}

// newApprovalPanel binds the pending request.
func newApprovalPanel(req approvalRequest) *approvalPanel { return &approvalPanel{req: req} }

// active reports whether the panel should render / capture keys.
func (p *approvalPanel) active() bool { return p != nil }

// handleKey drives the panel. It reports whether the key was consumed, and
// sets reply when the user answered (the caller sends it over the port,
// applies the [a] trust grant, and clears the panel). Esc/Enter/n deny —
// Esc stays inside the panel so a pending approval is never mistaken for
// the run interrupt.
func (p *approvalPanel) handleKey(msg tea.KeyPressMsg) (consumed bool, reply *approvalReply) {
	if !p.active() {
		return false, nil
	}
	switch msg.String() {
	case "y":
		return true, &approvalReply{id: p.req.id, decision: toolrules.AskApprove}
	case "a":
		return true, &approvalReply{id: p.req.id, decision: toolrules.AskApprove, always: true}
	case "s":
		if p.req.hint.Pattern == "" {
			return true, nil // no settle suggestion: the key is a no-op
		}
		return true, &approvalReply{
			id:       p.req.id,
			decision: toolrules.AskApproveWithRule,
			rule: toolrules.Rule{
				Tool:    p.req.hint.Tool,
				Pattern: p.req.hint.Pattern,
				Action:  toolrules.ActionAllow,
				Scope:   toolrules.ScopePersisted,
			},
		}
	case "n", "esc", "enter":
		return true, &approvalReply{id: p.req.id, decision: toolrules.AskDeny}
	default:
		return false, nil
	}
}

// view renders the pending ask: the pending badge (the ask panel's Warn
// convention), the call summary, and the key hints. Multi-line; the caller
// reserves its rows via lineCount.
func (p *approvalPanel) view(theme Theme, width int) string {
	if !p.active() {
		return ""
	}
	var b strings.Builder
	reason := "untrusted directory"
	if p.req.reason == toolrules.AskSelfEdit {
		reason = "pigo's own configuration (self-edit surface)"
	}
	fmt.Fprintf(&b, "%s  ·  %s\n", theme.Warn.Render("⏸ approval needed"), theme.System.Render(reason))
	fmt.Fprintf(&b, "pigo wants to run %q\n", p.req.call.Name)
	if summary := trust.ToolCallSummary(p.req.call); summary != "" {
		for _, line := range strings.Split(strings.TrimRight(summary, "\n"), "\n") {
			fmt.Fprintf(&b, "  %s\n", theme.System.Render(WrapToWidth(line, width-2)))
		}
	}
	hint := "  [y] allow · [n] deny"
	if p.req.hint.Pattern != "" {
		fmt.Fprintf(&b, "%s\n", theme.System.Render(fmt.Sprintf("  [s] save rule: allow %s %q from now on (persisted)",
			p.req.hint.Tool, p.req.hint.Pattern)))
	}
	hint += " · [a] always (trust this session) · esc deny"
	fmt.Fprintf(&b, "%s", theme.System.Render(hint))
	return b.String()
}

// lineCount reports how many terminal rows the panel currently occupies so
// relayout can reserve them from the transcript. It uses a canonical width
// for the estimate; wrapped lines may make the real render taller, which
// only costs a transient overlap until the next relayout.
func (p *approvalPanel) lineCount() int {
	if !p.active() {
		return 0
	}
	return strings.Count(p.view(DefaultTheme(), 80), "\n") + 1
}
