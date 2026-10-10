// engine.go is the decision engine (T5.2): the single authoritative judgment
// order for every BeforeToolCall, consuming the side-effect contract
// (agentcore.EffectOf), the rule tables (session + persisted), the
// self-edit surface, and the directory trust manager — replacing the old
// "trusted directory or confirm every call" dichotomy.
//
// Judgment order (spec §3.2, the authority table):
//
//	1. deny rule hit          → terminal block (trust/bypass never overrides)
//	2. self-edit surface hit  → force the ask channel (skips rules + trust)
//	3. allow rule hit, tool not destructive → allow
//	4. tool declares ReadOnly → allow
//	5. directory trusted      → allow
//	6. ask port               → approve (optionally settling a rule) / deny
//
// A nil AskPort fails closed at step 6 (headless: the block becomes a failed
// tool result the agent can route around, mirroring shellguard continue).
package toolrules

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/shellguard"
)

// AskReason tells the interactive channel why a call needs a human.
type AskReason string

const (
	// AskUntrusted: no rule matched, the tool has effects, the directory is
	// not trusted.
	AskUntrusted AskReason = "untrusted"
	// AskSelfEdit: the call targets pigo's own configuration surface.
	AskSelfEdit AskReason = "self-edit"
)

// ProposedHint carries the settle-suggestion for an approval. Pattern==""
// means "no rule can be proposed for this call" (compound/unsafe bash,
// unparseable args).
type ProposedHint struct {
	Tool    string
	Pattern string
}

// AskDecision is what an AskPort answers.
type AskDecision int

const (
	// AskDeny blocks the call.
	AskDeny AskDecision = iota
	// AskApprove allows this one call.
	AskApprove
	// AskApproveWithRule allows the call and settles the returned rule
	// (persisted when the store accepts it, session otherwise).
	AskApproveWithRule
)

// AskPort is the injected interactive channel (zcode broker精神, T4.2
// QuestionPort precedent): the engine is zero-IO, drivers supply the UI.
// Implementations must honor ctx cancellation (a canceled ctx denies). The
// second return is the rule to settle when the decision is
// AskApproveWithRule (ignored otherwise).
type AskPort func(ctx context.Context, call agentcore.AgentToolCall, reason AskReason, hint ProposedHint) (AskDecision, Rule)

// TrustedFunc reports whether the working directory carries a trust grant
// (session or persisted). Wired from trust.Manager.IsTrusted; nil = never.
type TrustedFunc func(cwd string) bool

// Engine is the permission decision engine. Safe for concurrent use (the
// batch runs tool calls serially today, but the mutex is cheap insurance —
// trust.BeforeToolCall's same argument).
type Engine struct {
	cwd      string
	effects  map[string]agentcore.ToolEffect
	store    *Store
	session  []Rule
	surface  *SelfEditSurface
	trusted  TrustedFunc
	ask      AskPort
	mu       sync.Mutex
}

// EngineConfig wires an Engine. Effects must cover every tool the registry
// can dispatch (the wiring point builds them from the assembled tool set).
type EngineConfig struct {
	Cwd     string
	// Effects maps tool name → declared contract. Names not present read as
	// the conservative default (needs confirmation).
	Effects map[string]agentcore.ToolEffect
	// Store is the persisted rule table; nil = no persistence.
	Store *Store
	// Session rules are typically empty at construction (they accumulate via
	// AskPort approvals without a settle rule).
	SessionRules []Rule
	// Surface is the self-edit guard; nil = disabled.
	Surface *SelfEditSurface
	// Trusted reports directory trust; nil = never trusted.
	Trusted TrustedFunc
	// Ask is the interactive channel; nil = fail closed at the ask step.
	Ask AskPort
}

// NewEngine builds an Engine. Session rules are normalized; an invalid
// session rule returns an error (config-facing input must fail loudly).
func NewEngine(cfg EngineConfig) (*Engine, error) {
	e := &Engine{
		cwd:     cfg.Cwd,
		effects: make(map[string]agentcore.ToolEffect, len(cfg.Effects)),
		store:   cfg.Store,
		surface: cfg.Surface,
		trusted: cfg.Trusted,
		ask:     cfg.Ask,
	}
	for name, eff := range cfg.Effects {
		e.effects[normalizeToolName(name)] = eff
	}
	for _, r := range cfg.SessionRules {
		norm, err := NormalizeRule(r)
		if err != nil {
			return nil, err
		}
		e.session = append(e.session, norm)
	}
	return e, nil
}

// AddSessionRule appends an in-memory rule (never persisted).
func (e *Engine) AddSessionRule(r Rule) error {
	norm, err := NormalizeRule(r)
	if err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.session = append(e.session, norm)
	return nil
}

// SessionRules returns a snapshot of the in-memory rules.
func (e *Engine) SessionRules() []Rule {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]Rule, len(e.session))
	copy(out, e.session)
	return out
}

// effectOf looks up the declared contract, defaulting conservatively.
func (e *Engine) effectOf(name string) agentcore.ToolEffect {
	if eff, ok := e.effects[normalizeToolName(name)]; ok {
		return eff
	}
	return agentcore.ToolEffect{Scope: agentcore.ScopeWorkspace}
}

// RuleSeam exposes the deny layer alone as a BeforeToolCall seam: it runs
// only judgment step 1 (terminal deny rules). Drivers that route approvals
// through an alternate human channel (a paired browser) place this seam
// AHEAD of that channel so a remote "allow" can never override a deny rule —
// the full Engine (with its ask port) still runs as the channel's fallback.
func (e *Engine) RuleSeam() agentcore.BeforeToolCallFunc {
	return func(ctx context.Context, call agentcore.AgentToolCall) *agentcore.BeforeToolCallDecision {
		if _, ok := e.matchRule(call, ActionDeny); ok {
			return e.block(call, fmt.Sprintf("tool %q denied by a permission rule (rules are terminal: no approval channel can override them).\n"+
				"adjust the rule in the permissions file if this is wrong.", call.Name))
		}
		return nil
	}
}

// BeforeToolCall implements agentcore.BeforeToolCallFunc with the §3.2
// judgment order. A nil return allows the call.
func (e *Engine) BeforeToolCall(ctx context.Context, call agentcore.AgentToolCall) *agentcore.BeforeToolCallDecision {
	// 1. Deny rules are terminal: no trust grant, session rule, or
	// bypass can override them (minimax hard-layer semantics).
	if _, ok := e.matchRule(call, ActionDeny); ok {
		return e.block(call, fmt.Sprintf("tool %q denied by a permission rule (rules are terminal: no trust grant can override them).\n"+
			"adjust the rule in the permissions file if this is wrong.", call.Name))
	}

	// 2. Self-edit surface: the call targets pigo's own boundary files.
	// Rules and directory trust are skipped — the boundary cannot rewrite
	// itself. Only a human at the ask channel (step 6) may allow it.
	selfEdit := false
	switch call.Name {
	case "write", "edit":
		selfEdit = e.surface.HitPath(argPath(call.Arguments))
	case "bash":
		selfEdit = e.surface.HitCommand(bashCommandText(call.Arguments))
	}

	// 3. Allow rules, but never for destructive tools: a settled rule can
	// not auto-run a tool declared destructive — only directory trust or a
	// human may (then the tool is de-facto destructive-by-declaration only).
	if !selfEdit {
		if rule, ok := e.matchRule(call, ActionAllow); ok && !e.effectOf(call.Name).Destructive {
			_ = rule
			return nil
		}

		// 4. Read-only tools skip the gate entirely (contract-driven; this
		// replaces the hardcoded SideEffectTools negative list).
		if e.effectOf(call.Name).ReadOnly {
			return nil
		}

		// 5. Directory trust grant (session or persisted).
		if e.trusted != nil && e.trusted(e.cwd) {
			return nil
		}
	}

	// 6. Ask channel. nil port fails closed (headless unattended).
	if e.ask == nil {
		if selfEdit {
			return e.block(call, fmt.Sprintf("tool %q blocked: it targets pigo's own configuration (the self-edit surface) and there is no interactive channel to approve it.", call.Name))
		}
		return e.block(call, fmt.Sprintf("tool %q blocked: %s is not trusted and there is no interactive channel to approve it (use /trust, a permissions rule, or run interactively).", call.Name, e.cwd))
	}
	reason := AskUntrusted
	if selfEdit {
		reason = AskSelfEdit
	}
	decision, rule := e.ask(ctx, call, reason, e.proposeHint(call))
	switch decision {
	case AskApprove:
		return nil
	case AskApproveWithRule:
		if err := e.settle(rule); err != nil {
			// Persistence failed: keep the grant session-scoped rather
			// than losing the approval entirely.
			_ = e.AddSessionRule(Rule{Tool: rule.Tool, Pattern: rule.Pattern, Action: ActionAllow, Scope: ScopeSession})
		}
		return nil
	default: // AskDeny, and any out-of-range value
	}
	if selfEdit {
		return e.block(call, fmt.Sprintf("tool %q blocked: the self-edit surface (pigo's own configuration) requires explicit human approval.", call.Name))
	}
	return e.block(call, fmt.Sprintf("tool %q blocked: permission denied by the user.", call.Name))
}

// ruleFamily maps a tool name to its rule family (T8.4): the "shell" alias
// is the bash tool under a second name, so bash rules match alias calls and
// a settled ask-hint is stored as a bash rule — one boundary, two names.
func ruleFamily(name string) string {
	n := normalizeToolName(name)
	if n == "shell" {
		return "bash"
	}
	return n
}

// matchRule finds the first rule of the given action matching the call.
// Persisted rules are consulted before session rules (deterministic order).
func (e *Engine) matchRule(call agentcore.AgentToolCall, action Action) (Rule, bool) {
	e.mu.Lock()
	session := make([]Rule, len(e.session))
	copy(session, e.session)
	e.mu.Unlock()

	var persisted []Rule
	if e.store != nil {
		persisted = e.store.Rules()
	}
	for _, table := range [][]Rule{persisted, session} {
		for _, r := range table {
			if r.Action != action || r.Tool != ruleFamily(call.Name) {
				continue
			}
			if ruleMatches(r, call) {
				return r, true
			}
		}
	}
	return Rule{}, false
}

// ruleMatches dispatches on the tool's pattern semantics.
func ruleMatches(r Rule, call agentcore.AgentToolCall) bool {
	switch r.Tool {
	case "bash":
		cmd := strings.TrimSpace(bashCommandText(call.Arguments))
		if cmd == "" {
			return false
		}
		if r.Action == ActionDeny {
			return bashMatchesDeny(cmd, r.Pattern)
		}
		return bashMatchesAllow(cmd, r.Pattern)
	case "write", "edit":
		return matchPath(argPath(call.Arguments), r.Pattern)
	default:
		return r.Pattern == ""
	}
}

// proposeHint builds the settle suggestion for the ask channel. For bash it
// takes the first two words — but only when shellguard rates the command
// Safe: a hazardous or unanalyzable command never becomes a persistent
// allow rule (grok: dangerous commands are never whitelisted). For
// write/edit it proposes the target's directory.
func (e *Engine) proposeHint(call agentcore.AgentToolCall) ProposedHint {
	switch ruleFamily(call.Name) {
	case "bash":
		cmd := strings.TrimSpace(bashCommandText(call.Arguments))
		if cmd == "" || strings.ContainsAny(cmd, compoundMarkers) {
			return ProposedHint{Tool: "bash"}
		}
		if d := shellguard.Analyze(cmd); d.Verdict != shellguard.Safe {
			return ProposedHint{Tool: "bash"}
		}
		words := strings.Fields(cmd)
		n := len(words)
		if n > 2 {
			n = 2
		}
		return ProposedHint{Tool: "bash", Pattern: strings.Join(words[:n], " ")}
	case "write", "edit":
		p := argPath(call.Arguments)
		if p == "" {
			return ProposedHint{Tool: call.Name}
		}
		dir := strings.TrimRight(canonicalize(p), "\\/")
		if dir == "" {
			return ProposedHint{Tool: call.Name}
		}
		return ProposedHint{Tool: call.Name, Pattern: dir + "/"}
	default:
		return ProposedHint{Tool: call.Name}
	}
}

// settle persists an approved rule; a store-less engine keeps it in session.
func (e *Engine) settle(r Rule) error {
	norm, err := NormalizeRule(r)
	if err != nil {
		return err
	}
	if e.store != nil {
		return e.store.Add(norm)
	}
	return e.AddSessionRule(Rule{Tool: norm.Tool, Pattern: norm.Pattern, Action: norm.Action, Scope: ScopeSession})
}

// block renders the failed tool result for a denied call.
func (e *Engine) block(call agentcore.AgentToolCall, msg string) *agentcore.BeforeToolCallDecision {
	return &agentcore.BeforeToolCallDecision{
		Block:   true,
		Content: &agentcore.ContentList{agentcore.NewTextContent(msg)},
	}
}

// EffectTable builds the name → declared-contract table the Engine expects,
// from an assembled tool set. Tools that do not implement EffectAware are
// absent — effectOf's conservative default covers them.
func EffectTable(tools []agentcore.AgentTool) map[string]agentcore.ToolEffect {
	table := make(map[string]agentcore.ToolEffect, len(tools))
	for _, t := range tools {
		if t == nil {
			continue
		}
		if ea, ok := t.(agentcore.EffectAware); ok {
			table[normalizeToolName(t.Name())] = ea.Effect()
		}
	}
	return table
}

// bashCommandText extracts the bash tool's "command" argument. A malformed
// payload degrades to "" (no rule can match an unparseable call — the
// executor rejects it on its own).
func bashCommandText(args json.RawMessage) string {
	var parsed struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(args, &parsed); err != nil {
		return ""
	}
	return parsed.Command
}

// argPath extracts the write/edit "path" argument.
func argPath(args json.RawMessage) string {
	var parsed struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(args, &parsed); err != nil {
		return ""
	}
	return parsed.Path
}
