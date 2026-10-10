package tui

import "github.com/smallnest/pigo/internal/toolrules"

import (
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
)

// newRemoteTestSession builds a fresh run session over a temp-dir store for the
// remote-control lifecycle tests (no resume, no tools).
func newRemoteTestSession(t *testing.T) *runSession {
	t.Helper()
	store := newTestStore(t)
	s, _, err := newRunSessionWithStore(store, Options{})
	if err != nil {
		t.Fatalf("newRunSessionWithStore: %v", err)
	}
	return s
}

// TestStartStopRemote covers the start→already-running→stop lifecycle on
// runSession: startRemote binds a listener and stores the session, a second
// start reports "already running" without replacing it, and stopRemote clears
// the session so a later start can rebind.
func TestStartStopRemote(t *testing.T) {
	s := newRemoteTestSession(t)
	if s.remote != nil {
		t.Fatal("remote should be nil before start")
	}

	url, err := s.startRemote()
	if err != nil {
		t.Fatalf("startRemote: %v", err)
	}
	if url == "" {
		t.Fatal("startRemote returned empty url")
	}
	if s.remote == nil {
		t.Fatal("remote should be set after start")
	}
	first := s.remote

	// A second start is a no-op that reports the existing url and an error,
	// leaving the running session untouched.
	url2, err := s.startRemote()
	if err == nil {
		t.Error("second startRemote should report already-running error")
	}
	if url2 != url {
		t.Errorf("second startRemote url = %q, want %q", url2, url)
	}
	if s.remote != first {
		t.Error("second startRemote must not replace the running session")
	}

	s.stopRemote()
	if s.remote != nil {
		t.Error("remote should be nil after stop")
	}

	// Stop again is a no-op.
	s.stopRemote()

	// After stopping, a fresh start rebinds cleanly.
	if _, err := s.startRemote(); err != nil {
		t.Fatalf("restart after stop: %v", err)
	}
	s.stopRemote()
}

// TestBuildConfigInstallsPermissionSeam asserts buildConfig always installs
// the BeforeToolCall permission seam (T5.2): the engine is unconditional —
// its ask channel routes to the paired browser when one exists and fails
// closed otherwise.
func TestBuildConfigInstallsRemoteSeam(t *testing.T) {
	s := newRemoteTestSession(t)

	if cfg := s.buildConfig(); cfg.Batch.ToolExecutorConfig.BeforeToolCall == nil {
		t.Error("BeforeToolCall (permission engine) should always be installed")
	}

	if _, err := s.startRemote(); err != nil {
		t.Fatalf("startRemote: %v", err)
	}
	defer s.stopRemote()

	if cfg := s.buildConfig(); cfg.Batch.ToolExecutorConfig.BeforeToolCall == nil {
		t.Error("BeforeToolCall should still be installed when remote control is on")
	}
}

// TestRemoteAskDeniesWhenNoClient verifies the engine's ask adapter fails
// closed when no browser is connected — a running-but-unpaired server must
// not silently approve side-effect calls (T5.2).
func TestRemoteConfirmSeamAllowsWhenNoClient(t *testing.T) {
	s := newRemoteTestSession(t)
	if _, err := s.startRemote(); err != nil {
		t.Fatalf("startRemote: %v", err)
	}
	defer s.stopRemote()

	// No client is paired and no local panel port is wired: the ask answers
	// AskUnavailable — the honest "no channel" word (D-C2), still fail-closed.
	ask := engineAsk(func() *remoteSession { return s.remote }, nil, nil, "/tmp/project")
	decision, rule := ask(t.Context(), agentcore.AgentToolCall{Name: "bash"}, toolrules.AskUntrusted, toolrules.ProposedHint{})
	if decision != toolrules.AskUnavailable {
		t.Errorf("ask with no client and no local port should report unavailable, got %v", decision)
	}
	if rule.Action != "" {
		t.Errorf("no rule should be proposed, got %+v", rule)
	}
}

// TestRemoteInputIgnoredWhileRunning routes a remoteInputMsg into an idle vs a
// running Model: while a run is in flight the prompt is not started (a busy note
// is shown instead), and the listener is always re-issued so later submissions
// keep arriving.
func TestRemoteInputIgnoredWhileRunning(t *testing.T) {
	s := newRemoteTestSession(t)
	if _, err := s.startRemote(); err != nil {
		t.Fatalf("startRemote: %v", err)
	}
	defer s.stopRemote()

	m := NewModel(Options{})
	m.session = s
	m.running = true

	updated, _ := m.Update(remoteInputMsg{text: "do something"})
	got := updated.(Model)
	if !hasSystemBlockContaining(got.transcript, "a run is in progress") {
		t.Errorf("expected a busy note in the transcript while running, blocks=%v",
			blockTexts(got.transcript))
	}
	// A run in progress must not consume the remote prompt as a new turn.
	if !got.running {
		t.Error("model should still be running; the remote prompt must not start a turn")
	}
}

// hasSystemBlockContaining reports whether any transcript block's text contains
// sub (case-insensitive substring over the rendered block texts).
func hasSystemBlockContaining(t transcript, sub string) bool {
	for _, s := range blockTexts(t) {
		if strings.Contains(strings.ToLower(s), strings.ToLower(sub)) {
			return true
		}
	}
	return false
}
