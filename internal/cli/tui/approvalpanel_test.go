// approvalpanel_test.go locks the TUI's local per-call approval channel
// (T7.6+D-C1): the port's request/reply round trip with stale-reply
// discarding, the panel key mapping, the mode ring + label, and the panel's
// lifecycle inside a running model.
package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/toolrules"
)

// shiftTabKey is the mode-cycle gesture (bubbletea decodes CSI Z — the
// standard shift+tab sequence — as KeyTab+ModShift, rendered "shift+tab").
var shiftTabKey = tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}

func TestApprovalPortRoundTrip(t *testing.T) {
	p := newApprovalPort()
	done := make(chan struct{})
	var decision toolrules.AskDecision
	var rule toolrules.Rule
	go func() {
		defer close(done)
		decision, rule = p.Ask(context.Background(), agentcore.AgentToolCall{Name: "bash"}, toolrules.AskUntrusted, toolrules.ProposedHint{})
	}()
	var req approvalRequest
	select {
	case req = <-p.requests:
	case <-time.After(time.Second):
		t.Fatal("no request within 1s")
	}
	if req.call.Name != "bash" || req.id == 0 {
		t.Fatalf("request = %+v", req)
	}
	p.respond(approvalReply{id: req.id, decision: toolrules.AskApprove})
	<-done
	if decision != toolrules.AskApprove || rule.Action != "" {
		t.Fatalf("decision = %v, rule = %+v", decision, rule)
	}
}

func TestApprovalPortStaleReplyDiscarded(t *testing.T) {
	p := newApprovalPort()
	done := make(chan struct{})
	var decision toolrules.AskDecision
	go func() {
		defer close(done)
		decision, _ = p.Ask(context.Background(), agentcore.AgentToolCall{Name: "bash"}, toolrules.AskUntrusted, toolrules.ProposedHint{})
	}()
	req := <-p.requests
	// A reply from an earlier (cancelled) ask arrives first: it must be
	// discarded, not delivered as this ask's answer.
	p.respond(approvalReply{id: req.id + 100, decision: toolrules.AskApprove})
	p.respond(approvalReply{id: req.id, decision: toolrules.AskDeny})
	<-done
	if decision != toolrules.AskDeny {
		t.Fatalf("stale reply was delivered: %v", decision)
	}
}

func TestApprovalPortCancelDenies(t *testing.T) {
	p := newApprovalPort()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var decision toolrules.AskDecision
	go func() {
		defer close(done)
		decision, _ = p.Ask(ctx, agentcore.AgentToolCall{Name: "bash"}, toolrules.AskUntrusted, toolrules.ProposedHint{})
	}()
	<-p.requests
	cancel()
	<-done
	if decision != toolrules.AskDeny {
		t.Fatalf("cancel should deny, got %v", decision)
	}
}

func TestApprovalPanelKeys(t *testing.T) {
	hint := toolrules.ProposedHint{Tool: "bash", Pattern: "git status"}
	panel := newApprovalPanel(approvalRequest{id: 7, call: agentcore.AgentToolCall{Name: "bash"}, reason: toolrules.AskUntrusted, hint: hint})
	cases := []struct {
		key      string
		decision toolrules.AskDecision
		always   bool
		rule     bool
	}{
		{"y", toolrules.AskApprove, false, false},
		{"a", toolrules.AskApprove, true, false},
		{"s", toolrules.AskApproveWithRule, false, true},
		{"n", toolrules.AskDeny, false, false},
		{"esc", toolrules.AskDeny, false, false},
		{"enter", toolrules.AskDeny, false, false},
	}
	for _, tc := range cases {
		var msg tea.KeyPressMsg
		switch tc.key {
		case "esc":
			msg = tea.KeyPressMsg{Code: tea.KeyEscape}
		case "enter":
			msg = tea.KeyPressMsg{Code: tea.KeyEnter}
		default:
			msg = tea.KeyPressMsg{Code: []rune(tc.key)[0]}
		}
		consumed, reply := panel.handleKey(msg)
		if !consumed || reply == nil {
			t.Fatalf("key %q: consumed=%v reply=%v", tc.key, consumed, reply)
		}
		if reply.id != 7 || reply.decision != tc.decision || reply.always != tc.always {
			t.Errorf("key %q → reply %+v", tc.key, reply)
		}
		if tc.rule && reply.rule.Pattern != "git status" {
			t.Errorf("key %q → rule %+v", tc.key, reply.rule)
		}
	}
	// s without a settle suggestion is a consumed no-op.
	plain := newApprovalPanel(approvalRequest{id: 1, call: agentcore.AgentToolCall{Name: "write"}})
	consumed, reply := plain.handleKey(tea.KeyPressMsg{Code: 's'})
	if !consumed || reply != nil {
		t.Errorf("s without hint: consumed=%v reply=%v", consumed, reply)
	}
	// Typing falls through to the composer (unconsumed).
	if consumed, _ := plain.handleKey(tea.KeyPressMsg{Code: 'x'}); consumed {
		t.Error("unrelated key should not be consumed")
	}
	// The view names the call, the reason and the keys.
	if v := plain.view(DefaultTheme(), 80); !strings.Contains(v, `"write"`) || !strings.Contains(v, "untrusted directory") || !strings.Contains(v, "[a] always") {
		t.Errorf("view = %q", v)
	}
}

// TestApprovalModeDerivedAndCycled: everything but --approve seeds ask;
// shift+tab walks the ask → plan → all → ask ring with a feedback line per
// switch; the plan posture gates the session engine directly; the /mode
// panel switches exactly.
func TestApprovalModeDerivedAndCycled(t *testing.T) {
	store := newTestStore(t)
	s, _, err := newRunSessionWithStore(store, Options{Model: "m", ProviderName: "p"})
	if err != nil {
		t.Fatalf("newRunSessionWithStore: %v", err)
	}
	m := apply(t, NewModel(Options{Model: "m", ProviderName: "p"}).withSession(s, nil),
		tea.WindowSizeMsg{Width: 80, Height: 30})
	if got := m.approvalLabel(); got != "ask" {
		t.Fatalf("initial label = %q, want ask", got)
	}

	m = apply(t, m, shiftTabKey)
	if got := s.approval.Mode(); got != toolrules.ModePlan {
		t.Fatalf("after one shift+tab = %v, want plan", got)
	}
	if got := m.approvalLabel(); got != "plan" {
		t.Fatalf("label = %q, want plan", got)
	}
	if !transcriptHas(m, "approval mode → plan") {
		t.Error("switch feedback line missing")
	}

	m = apply(t, m, shiftTabKey)
	if s.approval.Mode() != toolrules.ModeAll {
		t.Fatalf("after two = %v, want always-approve", s.approval.Mode())
	}
	if got := m.approvalLabel(); got != "always-approve" {
		t.Fatalf("label = %q, want always-approve", got)
	}
	m = apply(t, m, shiftTabKey)
	if s.approval.Mode() != toolrules.ModeAsk {
		t.Fatalf("after three = %v, want ask (ring closed)", s.approval.Mode())
	}

	// The plan posture gates the session's engine: a bash call blocks with
	// the plan message even though the session-less-directory engine would
	// otherwise reach the ask channel.
	s.approval.Set(toolrules.ModePlan)
	dec := s.permEngine.BeforeToolCall(context.Background(), agentcore.AgentToolCall{
		Name:      "bash",
		Arguments: []byte(`{"command":"git status"}`),
	})
	if dec == nil || !dec.Block || !strings.Contains(agentcore.ContentToText(*dec.Content), "plan mode is active") {
		t.Fatalf("plan mode engine block = %+v", dec)
	}
	s.approval.Set(toolrules.ModeAsk)

	// The /mode panel switches exactly.
	m = typeInto(t, m, "/mode").(Model)
	m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.modeP.open {
		t.Fatal("/mode did not open the panel")
	}
	if len(m.modeP.rows) != 3 {
		t.Fatalf("panel rows = %d, want 3 postures", len(m.modeP.rows))
	}
	if m.modeP.rows[0].tag != "[current]" {
		t.Errorf("ask row should carry [current], got %+v", m.modeP.rows[0])
	}
	// Space on the highlighted row (ask) — move down to plan first.
	m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	m = apply(t, m, tea.KeyPressMsg{Code: ' '})
	if s.approval.Mode() != toolrules.ModePlan {
		t.Fatalf("panel Space = %v, want plan", s.approval.Mode())
	}
	if !strings.Contains(m.modeP.note, "plan") {
		t.Errorf("panel note = %q", m.modeP.note)
	}
	m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.modeP.open {
		t.Error("esc did not close the mode panel")
	}
}

// TestApprovalModeFromApproveFlag: --approve seeds always-approve (the
// --approve trust semantics keep their meaning under the mode state).
func TestApprovalModeFromApproveFlag(t *testing.T) {
	store := newTestStore(t)
	s, _, err := newRunSessionWithStore(store, Options{Model: "m", ProviderName: "p", Approve: true})
	if err != nil {
		t.Fatalf("newRunSessionWithStore: %v", err)
	}
	if s.approval.Mode() != toolrules.ModeAll {
		t.Fatalf("--approve should seed always-approve, got %v", s.approval.Mode())
	}
	// And the engine fast-path allows an effect call without a channel.
	dec := s.permEngine.BeforeToolCall(context.Background(), agentcore.AgentToolCall{
		Name:      "bash",
		Arguments: []byte(`{"command":"git status"}`),
	})
	if dec != nil {
		t.Fatalf("--approve must allow, got block: %+v", dec.Content)
	}
}

// TestApprovalPanelFlowInRun drives the panel inside a running model: a real
// Ask blocks on the port, the request opens the panel, y answers over the
// port, and a second ask's n denies — the engine's exact conversation.
func TestApprovalPanelFlowInRun(t *testing.T) {
	store := newTestStore(t)
	s, _, err := newRunSessionWithStore(store, Options{Model: "m", ProviderName: "p"})
	if err != nil {
		t.Fatalf("newRunSessionWithStore: %v", err)
	}
	m := apply(t, NewModel(Options{Model: "m", ProviderName: "p"}).withSession(s, nil),
		tea.WindowSizeMsg{Width: 80, Height: 30})
	m.startRunFn = func(prompt string) (chan tea.Msg, tea.Cmd) {
		return make(chan tea.Msg, 1), nil
	}
	m = typeInto(t, m, "run something").(Model)
	m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.running {
		t.Fatal("run should be in flight")
	}

	// y approves; the next two asks deny via n and esc.
	if got := askWithKey(t, &m, s, "approval needed", "y"); got != toolrules.AskApprove {
		t.Fatalf("y → %v, want approve", got)
	}
	if m.approval.active() {
		t.Fatal("y did not clear the panel")
	}
	if got := askWithKey(t, &m, s, "", "n"); got != toolrules.AskDeny {
		t.Fatalf("n → %v, want deny", got)
	}
	if got := askWithKey(t, &m, s, "", "esc"); got != toolrules.AskDeny {
		t.Fatalf("esc → %v, want deny", got)
	}
}

// askWithKey runs one port round trip: the ask blocks, the panel opens (the
// badge text is asserted on the first call), the key answers, the panel
// clears and the answer crosses back.
func askWithKey(t *testing.T, m *Model, s *runSession, wantBadge, key string) toolrules.AskDecision {
	t.Helper()
	answer := make(chan toolrules.AskDecision, 1)
	go func() {
		d, _ := s.approvalCh.Ask(context.Background(), agentcore.AgentToolCall{Name: "bash", Arguments: []byte(`{"command":"git status"}`)}, toolrules.AskUntrusted, toolrules.ProposedHint{Tool: "bash", Pattern: "git status"})
		answer <- d
	}()
	reqMsg := m.waitApproval()().(approvalReqMsg)
	*m = apply(t, *m, reqMsg)
	if !m.approval.active() {
		t.Fatal("approval panel did not open")
	}
	if wantBadge != "" && !strings.Contains(stripANSI(m.View().Content), wantBadge) {
		t.Errorf("panel view missing %q:\n%s", wantBadge, stripANSI(m.View().Content))
	}
	code := []rune(key)[0]
	var msg tea.KeyPressMsg
	if key == "esc" {
		msg = tea.KeyPressMsg{Code: tea.KeyEscape}
	} else {
		msg = tea.KeyPressMsg{Code: code}
	}
	*m = apply(t, *m, msg)
	if m.approval.active() {
		t.Fatalf("%s did not clear the panel", key)
	}
	return <-answer
}

// transcriptHas reports whether any system block carries the text.
func transcriptHas(m Model, want string) bool {
	for _, b := range m.transcript.blocks {
		if b.role == roleSystem && strings.Contains(b.text, want) {
			return true
		}
	}
	return false
}
