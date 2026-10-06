package toolrules

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
)

// quoteJSON renders s as a JSON string literal.
func quoteJSON(s string) string { return fmt.Sprintf("%q", s) }

// fakeTool is a minimal AgentTool with an optional contract.
type fakeTool struct {
	name string
	eff  *agentcore.ToolEffect
}

func (f *fakeTool) Name() string        { return f.name }
func (f *fakeTool) Description() string { return "fake " + f.name }
func (f *fakeTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object"}`)
}
func (f *fakeTool) ExecutionMode() agentcore.ToolExecutionMode {
	return agentcore.ToolExecutionSequential
}
func (f *fakeTool) Execute(ctx context.Context, id string, args json.RawMessage, onUpdate agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
	return agentcore.AgentToolResult{}, nil
}

func (f *fakeTool) Effect() agentcore.ToolEffect {
	if f.eff == nil {
		return agentcore.ToolEffect{Scope: agentcore.ScopeWorkspace}
	}
	return *f.eff
}

func call(name, args string) agentcore.AgentToolCall {
	return agentcore.AgentToolCall{ID: "c1", Name: name, Arguments: json.RawMessage(args)}
}

// newTestEngine builds an engine over fake tools with the given options.
func newTestEngine(t *testing.T, cfg EngineConfig) *Engine {
	t.Helper()
	e, err := NewEngine(cfg)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	return e
}

func TestEffectOfDefaults(t *testing.T) {
	plain := &fakeTool{name: "plugin_tool"} // no eff pointer, but fakeTool always implements EffectAware
	if eff := plain.Effect(); eff.ReadOnly || eff.Destructive || eff.Scope != agentcore.ScopeWorkspace {
		t.Errorf("fakeTool default = %+v, want conservative workspace", eff)
	}
	// EffectOf falls back for a tool that does NOT implement EffectAware.
	none := bareTool{}
	eff := agentcore.EffectOf(none)
	if eff.ReadOnly || eff.Destructive || eff.Scope != agentcore.ScopeWorkspace {
		t.Errorf("EffectOf(non-aware) = %+v, want conservative workspace", eff)
	}
}

// bareTool implements only AgentTool — no Effect declaration (embedding
// would promote fakeTool's Effect, so this is a standalone type). It models
// a plugin adapter.
type bareTool struct{}

func (bareTool) Name() string        { return "bare" }
func (bareTool) Description() string { return "bare" }
func (bareTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object"}`)
}
func (bareTool) ExecutionMode() agentcore.ToolExecutionMode {
	return agentcore.ToolExecutionSequential
}
func (bareTool) Execute(ctx context.Context, id string, args json.RawMessage, onUpdate agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
	return agentcore.AgentToolResult{}, nil
}

// TestEngineDenyTerminal locks the minimax hard-layer semantics: a deny rule
// blocks even a trusted directory and even when the ask port would approve.
func TestEngineDenyTerminal(t *testing.T) {
	e := newTestEngine(t, EngineConfig{
		Cwd: "/w",
		Effects: EffectTable([]agentcore.AgentTool{
			&fakeTool{name: "bash"},
		}),
		SessionRules: []Rule{{Tool: "bash", Pattern: "rm", Action: ActionDeny}},
		Trusted:      func(string) bool { return true },
		Ask: func(ctx context.Context, c agentcore.AgentToolCall, r AskReason, h ProposedHint) (AskDecision, Rule) {
			t.Fatal("ask must not be consulted for a denied call")
			return AskDeny, Rule{}
		},
	})
	dec := e.BeforeToolCall(context.Background(), call("bash", `{"command":"rm -rf build"}`))
	if dec == nil || !dec.Block {
		t.Fatalf("deny rule must block, got %+v", dec)
	}
}

// TestEngineOrderTable walks the §3.2 judgment order end to end.
func TestEngineOrderTable(t *testing.T) {
	askCalls := 0
	ask := func(ctx context.Context, c agentcore.AgentToolCall, r AskReason, h ProposedHint) (AskDecision, Rule) {
		askCalls++
		return AskApprove, Rule{}
	}
	e := newTestEngine(t, EngineConfig{
		Cwd: "/w",
		Effects: EffectTable([]agentcore.AgentTool{
			&fakeTool{name: "bash"},
			&fakeTool{name: "read", eff: &agentcore.ToolEffect{ReadOnly: true}},
			&fakeTool{name: "wipe", eff: &agentcore.ToolEffect{Destructive: true}},
		}),
		SessionRules: []Rule{
			{Tool: "bash", Pattern: "git status", Action: ActionAllow},
			{Tool: "write", Action: ActionAllow},
		},
		Trusted: func(dir string) bool { return dir == "/w" },
		Ask:     ask,
	})

	ctx := context.Background()
	// 3. allow rule (single command) → allow, no ask.
	if dec := e.BeforeToolCall(ctx, call("bash", `{"command":"git status --short"}`)); dec != nil {
		t.Errorf("allow rule should pass, got %+v", dec)
	}
	// 3+. compound command never matches an allow rule → ask.
	if dec := e.BeforeToolCall(ctx, call("bash", `{"command":"git status; ls"}`)); dec != nil {
		t.Errorf("compound command should not match allow, got %+v", dec)
	}
	// 4. readonly → allow without ask.
	if dec := e.BeforeToolCall(ctx, call("read", `{"path":"x"}`)); dec != nil {
		t.Errorf("readonly should pass, got %+v", dec)
	}
	// 5. trusted directory → effect tool passes without ask.
	if dec := e.BeforeToolCall(ctx, call("bash", `{"command":"make build"}`)); dec != nil {
		t.Errorf("trusted dir should pass, got %+v", dec)
	}
	// 3 with destructive: allow rule for write exists, but wipe is
	// destructive — rules cannot auto-run it; trusted dir still can.
	if dec := e.BeforeToolCall(ctx, call("wipe", `{}`)); dec != nil {
		t.Errorf("trusted dir allows destructive (step 5), got %+v", dec)
	}
	// Word boundary: "tr" must not match "truncate".
	e2 := newTestEngine(t, EngineConfig{
		Cwd: "/w",
		Effects: EffectTable([]agentcore.AgentTool{&fakeTool{name: "bash"}}),
		SessionRules: []Rule{{Tool: "bash", Pattern: "tr", Action: ActionAllow}},
	})
	if dec := e2.BeforeToolCall(ctx, call("bash", `{"command":"truncate -s 0 f"}`)); dec == nil || !dec.Block {
		t.Errorf("word-boundary violation must not auto-allow, got %+v", dec)
	}
	if askCalls != 0 {
		t.Errorf("trusted-dir / rule paths must not consult ask, askCalls=%d", askCalls)
	}
}

// TestEngineUntrustedAsk covers the untrusted fallback: ask approve/deny and
// rule settlement.
func TestEngineUntrustedAsk(t *testing.T) {
	dir := t.TempDir()
	var askDecision AskDecision
	var settled Rule
	e := newTestEngine(t, EngineConfig{
		Cwd:     dir,
		Effects: EffectTable([]agentcore.AgentTool{&fakeTool{name: "bash"}}),
		Trusted: func(string) bool { return false },
		Ask: func(ctx context.Context, c agentcore.AgentToolCall, r AskReason, h ProposedHint) (AskDecision, Rule) {
			return askDecision, settled
		},
	})
	ctx := context.Background()

	askDecision = AskDeny
	if dec := e.BeforeToolCall(ctx, call("bash", `{"command":"ls"}`)); dec == nil || !dec.Block {
		t.Fatalf("deny must block, got %+v", dec)
	}

	askDecision = AskApprove
	if dec := e.BeforeToolCall(ctx, call("bash", `{"command":"ls"}`)); dec != nil {
		t.Fatalf("approve must allow, got %+v", dec)
	}

	askDecision = AskApproveWithRule
	settled = Rule{Tool: "bash", Pattern: "ls", Action: ActionAllow}
	if dec := e.BeforeToolCall(ctx, call("bash", `{"command":"ls -la"}`)); dec != nil {
		t.Fatalf("approve-with-rule must allow, got %+v", dec)
	}
	// The settled rule is session-scoped (no store) and now matches without ask.
	askDecision = AskDeny
	if dec := e.BeforeToolCall(ctx, call("bash", `{"command":"ls -la"}`)); dec != nil {
		t.Fatalf("settled session rule should allow without ask, got %+v", dec)
	}
}

// TestEngineNilAskFailsClosed: no interactive channel = block (headless).
func TestEngineNilAskFailsClosed(t *testing.T) {
	e := newTestEngine(t, EngineConfig{
		Cwd:     "/w",
		Effects: EffectTable([]agentcore.AgentTool{&fakeTool{name: "bash"}}),
	})
	if dec := e.BeforeToolCall(context.Background(), call("bash", `{"command":"ls"}`)); dec == nil || !dec.Block {
		t.Fatalf("nil ask must fail closed, got %+v", dec)
	}
}

// TestEngineSelfEdit locks the self-edit surface: rules and trust never
// auto-approve a write to pigo's own configuration.
func TestEngineSelfEdit(t *testing.T) {
	home := t.TempDir()
	trustFile := filepath.Join(home, "trust.json")
	e := newTestEngine(t, EngineConfig{
		Cwd:     t.TempDir(),
		Effects: EffectTable([]agentcore.AgentTool{&fakeTool{name: "write"}, &fakeTool{name: "bash"}}),
		SessionRules: []Rule{
			{Tool: "write", Action: ActionAllow},
			{Tool: "bash", Pattern: "echo", Action: ActionAllow},
		},
		Trusted: func(string) bool { return true },
		Surface: NewSelfEditSurface(trustFile),
		Ask: func(ctx context.Context, c agentcore.AgentToolCall, r AskReason, h ProposedHint) (AskDecision, Rule) {
			if r != AskSelfEdit {
				t.Errorf("reason = %q, want self-edit", r)
			}
			return AskDeny, Rule{}
		},
	})
	ctx := context.Background()
	// write targeting the protected file: allow rule + trusted dir are skipped.
	if dec := e.BeforeToolCall(ctx, call("write", `{"path":`+quoteJSON(trustFile)+`,"content":"x"}`)); dec == nil || !dec.Block {
		t.Fatalf("self-edit write must escalate (denied by ask), got %+v", dec)
	}
	// bash command text mentioning the protected path: same. (The command is
	// built through json.Marshal so backslashes in the Windows path stay
	// valid JSON.)
	bashArgs, merr := json.Marshal(map[string]string{"command": "echo hacked > " + trustFile})
	if merr != nil {
		t.Fatal(merr)
	}
	if dec := e.BeforeToolCall(ctx, call("bash", string(bashArgs))); dec == nil || !dec.Block {
		t.Fatalf("self-edit bash must escalate, got %+v", dec)
	}
	// An unrelated write passes via the allow rule.
	if dec := e.BeforeToolCall(ctx, call("write", `{"path":"other.txt","content":"x"}`)); dec != nil {
		t.Fatalf("normal write should pass, got %+v", dec)
	}
}

// TestSelfEditSymlinkPenetration: a symlink pointing at a protected file hits
// the surface (qwen symlink-penetration check).
func TestSelfEditSymlinkPenetration(t *testing.T) {
	home := t.TempDir()
	trustFile := filepath.Join(home, "trust.json")
	if err := os.WriteFile(trustFile, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link.json")
	if err := os.Symlink(trustFile, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	s := NewSelfEditSurface(trustFile)
	if !s.HitPath(link) {
		t.Errorf("symlink to protected file must hit the surface")
	}
	// A protected path that does not exist YET still resolves via its
	// nearest existing ancestor (parent resolution), so a not-yet-written
	// trust.json is guarded the same as an existing one.
	missing := filepath.Join(home, "new-permissions.json")
	s2 := NewSelfEditSurface(missing)
	if !s2.HitPath(missing) {
		t.Errorf("missing protected file must hit via parent resolution")
	}
	// Sibling files are NOT protected (exact-file semantics).
	if s2.HitPath(filepath.Join(home, "unrelated.json")) {
		t.Errorf("unprotected sibling must not hit")
	}
}

// TestStoreRoundTrip covers persistence: add, dedupe, reload.
func TestStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "permissions.json")
	s, err := LoadStore(path)
	if err != nil {
		t.Fatalf("LoadStore: %v", err)
	}
	if err := s.Add(Rule{Tool: "bash", Pattern: "git status", Action: ActionAllow}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	// Duplicate is a no-op.
	if err := s.Add(Rule{Tool: "bash", Pattern: "git status", Action: ActionAllow}); err != nil {
		t.Fatalf("Add dup: %v", err)
	}
	if got := len(s.Rules()); got != 1 {
		t.Fatalf("rules = %d, want 1", got)
	}
	s2, err := LoadStore(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	rules := s2.Rules()
	if len(rules) != 1 || rules[0].Tool != "bash" || rules[0].Pattern != "git status" || rules[0].Action != ActionAllow {
		t.Fatalf("reloaded rules = %+v", rules)
	}
}

// TestStoreCorruptFile: a malformed store is a hard error.
func TestStoreCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "permissions.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadStore(path); err == nil {
		t.Fatal("corrupt store must error")
	}
}

// TestNormalizeRule rejects invalid config-facing rules.
func TestNormalizeRule(t *testing.T) {
	if _, err := NormalizeRule(Rule{Tool: "bash", Action: "maybe"}); err == nil {
		t.Error("unknown action must error")
	}
	if _, err := NormalizeRule(Rule{Tool: "  ", Action: ActionAllow}); err == nil {
		t.Error("empty tool must error")
	}
	n, err := NormalizeRule(Rule{Tool: " BASH ", Pattern: " git ", Action: ActionAllow})
	if err != nil || n.Tool != "bash" || n.Pattern != "git" {
		t.Errorf("normalize = %+v, err=%v", n, err)
	}
}
