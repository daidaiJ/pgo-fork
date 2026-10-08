package tui

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/session"
	"github.com/smallnest/pigo/internal/testenv"
)

// newTestStore opens a session store rooted at a temp dir so persistence/resume
// can be exercised without touching ~/.pigo.
func newTestStore(t *testing.T) *session.Store {
	t.Helper()
	store, err := session.NewStore(testenv.Dir(t))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return store
}

// saveSession writes a linear session with the given messages and returns its id.
func saveSession(t *testing.T, store *session.Store, msgs agentcore.MessageList) string {
	t.Helper()
	now := time.Now().UTC()
	header := session.SessionHeader{
		ID:        session.NewID(now),
		CreatedAt: now,
		UpdatedAt: now,
		Model:     "test-model",
		Provider:  "test-provider",
	}
	if err := store.Save(header, msgs); err != nil {
		t.Fatalf("Save: %v", err)
	}
	return header.ID
}

// TestResumeSeedsTranscript constructs a session with a few messages, resumes it
// through newRunSessionWithStore, seeds a transcript with the returned history,
// and asserts the initial transcript blocks carry those messages (FR-16 resume).
func TestResumeSeedsTranscript(t *testing.T) {
	store := newTestStore(t)
	id := saveSession(t, store, agentcore.MessageList{
		agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("hello, world")}},
		agentcore.AssistantMessage{RoleField: agentcore.RoleAssistant, Content: agentcore.ContentList{agentcore.NewTextContent("hello back")}},
		agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("second question")}},
	})

	s, history, err := newRunSessionWithStore(store, Options{ResumeID: id})
	if err != nil {
		t.Fatalf("newRunSessionWithStore: %v", err)
	}
	if len(history) != 3 {
		t.Fatalf("history len = %d, want 3", len(history))
	}
	// The persisted cursor must cover the full resumed history so the first new
	// turn appends only fresh messages, not a re-save of history.
	if s.persisted != 3 {
		t.Errorf("persisted = %d, want 3", s.persisted)
	}
	if s.curLeaf == "" {
		t.Error("curLeaf should be the resumed leaf, got empty")
	}

	tr := newTranscript(DefaultTheme())
	seedTranscript(&tr, history)

	wantTexts := []string{"hello, world", "hello back", "second question"}
	if len(tr.blocks) != len(wantTexts) {
		t.Fatalf("transcript blocks = %d, want %d", len(tr.blocks), len(wantTexts))
	}
	for i, want := range wantTexts {
		if tr.blocks[i].text != want {
			t.Errorf("block[%d] = %q, want %q", i, tr.blocks[i].text, want)
		}
	}
}

// TestBuildConfigAssembly asserts the run-config assembly maps the live config
// onto RunConfig without a live provider: the model/provider/thinking/window
// fields flow through, compaction is enabled, and the tool registry is wired.
func TestBuildConfigAssembly(t *testing.T) {
	store := newTestStore(t)
	s, _, err := newRunSessionWithStore(store, Options{
		Model:         "opus-test",
		ProviderName:  "anthropic",
		ThinkingLevel: agentcore.ThinkingLevel("high"),
	})
	if err != nil {
		t.Fatalf("newRunSessionWithStore: %v", err)
	}

	cfg := s.buildConfig()
	if cfg.Model != "opus-test" {
		t.Errorf("cfg.Model = %q, want opus-test", cfg.Model)
	}
	if cfg.Provider != "anthropic" {
		t.Errorf("cfg.Provider = %q, want anthropic", cfg.Provider)
	}
	if cfg.ThinkingLevel != agentcore.ThinkingLevel("high") {
		t.Errorf("cfg.ThinkingLevel = %q, want high", cfg.ThinkingLevel)
	}
	if cfg.ContextWindow <= 0 {
		t.Errorf("cfg.ContextWindow = %d, want a positive default", cfg.ContextWindow)
	}
	if !cfg.Compaction.Enabled {
		t.Error("cfg.Compaction.Enabled = false, want true (DefaultCompactionSettings)")
	}
	if cfg.Batch.Registry == nil {
		t.Error("cfg.Batch.Registry is nil, want the assembled tool registry")
	}
	if cfg.Stream == nil {
		t.Error("cfg.Stream is nil, want a stream fn derived from the provider")
	}
}

// TestFreshSessionPersists starts a fresh session, appends a turn to the context,
// persists it, and confirms it round-trips back through the store (FR-16 persist).
func TestFreshSessionPersists(t *testing.T) {
	store := newTestStore(t)
	s, history, err := newRunSessionWithStore(store, Options{Model: "m", ProviderName: "p"})
	if err != nil {
		t.Fatalf("newRunSessionWithStore: %v", err)
	}
	if history != nil {
		t.Fatalf("fresh session history = %v, want nil", history)
	}

	s.agentCtx.Messages = append(s.agentCtx.Messages,
		agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("hi")}},
		agentcore.AssistantMessage{RoleField: agentcore.RoleAssistant, Content: agentcore.ContentList{agentcore.NewTextContent("yo")}},
	)
	if err := s.persist(); err != nil {
		t.Fatalf("persist: %v", err)
	}
	if s.persisted != 2 {
		t.Errorf("persisted = %d, want 2", s.persisted)
	}

	_, msgs, err := store.Load(s.header.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("persisted messages = %d, want 2", len(msgs))
	}

	// A second persist with no new messages is a no-op.
	before := s.curLeaf
	if err := s.persist(); err != nil {
		t.Fatalf("persist (no-op): %v", err)
	}
	if s.curLeaf != before {
		t.Errorf("curLeaf changed on no-op persist: %q -> %q", before, s.curLeaf)
	}
}

// TestPersistAfterCompaction reproduces the crash where an automatic compaction
// shrinks agentCtx.Messages below the persisted cursor: an incremental
// Messages[persisted:] would panic with a slice-bounds error. persist() must
// instead re-save the flattened context and reset the cursor to the new length.
// TestPersistAfterCompaction covers the T3.3 marker-entry contract: auto-
// compaction no longer rewrites the context — it INSERTS a compaction marker
// into the live list, so persist() keeps doing a plain tail append. The
// pre-compaction entries must survive on disk (append-only tree) and the
// marker must join the same branch (defect-② fix: the old flatten-and-save
// path physically destroyed abandoned branches).
func TestPersistAfterCompaction(t *testing.T) {
	store := newTestStore(t)
	s, _, err := newRunSessionWithStore(store, Options{Model: "m", ProviderName: "p"})
	if err != nil {
		t.Fatalf("newRunSessionWithStore: %v", err)
	}

	// Persist a few turns so the cursor advances.
	for i := 0; i < 4; i++ {
		s.agentCtx.Messages = append(s.agentCtx.Messages,
			agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("q")}},
			agentcore.AssistantMessage{RoleField: agentcore.RoleAssistant, Content: agentcore.ContentList{agentcore.NewTextContent("a")}},
		)
	}
	if err := s.persist(); err != nil {
		t.Fatalf("persist: %v", err)
	}
	if s.persisted != 8 {
		t.Fatalf("persisted = %d, want 8 before compaction", s.persisted)
	}

	// Simulate the run loop compacting T3.3-style: a marker inserted at the
	// persisted cursor (cut lands inside persisted territory, KeptBefore
	// records the kept wrap) plus a fresh turn after it.
	marker := agentcore.CompactionMessage{
		RoleField:       agentcore.RoleCompaction,
		Summary:         "summary of the earlier turns",
		Timestamp:       time.Now().UnixMilli(),
		FirstKeptIndex:  4,
		KeptBefore:      4,
		StrategyVersion: 1,
	}
	msgs := append(agentcore.MessageList{}, s.agentCtx.Messages[:8]...)
	msgs = append(msgs, marker)
	msgs = append(msgs,
		agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("recent q")}},
	)
	s.agentCtx.Messages = msgs

	if err := s.persist(); err != nil {
		t.Fatalf("persist after compaction: %v", err)
	}
	if s.persisted != len(msgs) {
		t.Errorf("persisted = %d, want %d (marker + tail appended)", s.persisted, len(msgs))
	}

	// The on-disk tree keeps every original entry AND the marker: append-only.
	header, got, err := store.Load(s.header.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	_ = header
	if len(got) != len(msgs) {
		t.Fatalf("persisted messages = %d, want %d (8 original + marker + tail)", len(got), len(msgs))
	}
	if got[8].Role() != agentcore.RoleCompaction {
		t.Errorf("entry 9 = %s, want a compaction marker entry", got[8].Role())
	}
	for i := 0; i < 8; i++ {
		if got[i].Role() == agentcore.RoleCompaction {
			t.Errorf("original entry %d was replaced by a compaction entry", i)
		}
	}
}

// TestTUIApproveGrantsEngineTrust reproduces the 2026-10-07 incident: the TUI
// never ran the REPL's EstablishTrust step, so --approve never reached the
// permission engine — a launch whose trust store had no entry for the cwd was
// "restricted" in the engine's eyes, and with no remote browser paired the ask
// channel denied, silently blocking even read-only bash (git log/status) while
// the model saw "permission denied by the user".
func TestTUIApproveGrantsEngineTrust(t *testing.T) {
	t.Setenv("PIGO_HOME", testenv.Dir(t)) // isolated trust store: no persisted grants
	store := newTestStore(t)
	call := agentcore.AgentToolCall{
		ID:        "t1",
		Name:      "bash",
		Arguments: json.RawMessage(`{"command":"git log --oneline -5"}`),
	}

	// With --approve the engine's trust fast path must allow the call.
	s, _, err := newRunSessionWithStore(store, Options{Model: "m", ProviderName: "p", Approve: true})
	if err != nil {
		t.Fatalf("newRunSessionWithStore(approve): %v", err)
	}
	if d := s.permEngine.BeforeToolCall(t.Context(), call); d != nil {
		t.Errorf("--approve should allow bash in a store-untracked directory, got block: %+v", d.Content)
	}

	// Without --approve and without /trust the unpaired-remote ask channel
	// still fails closed: the call is blocked (the TUI-local approval channel
	// and the misleading "denied by the user" wording are registered for
	// design review, not changed here).
	s2, _, err := newRunSessionWithStore(store, Options{Model: "m", ProviderName: "p"})
	if err != nil {
		t.Fatalf("newRunSessionWithStore: %v", err)
	}
	if d := s2.permEngine.BeforeToolCall(t.Context(), call); d == nil || !d.Block {
		t.Error("restricted TUI (no --approve, no /trust) must block bash when no approval channel exists")
	}
}
