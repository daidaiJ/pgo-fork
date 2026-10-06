// ToolEffect is the side-effect contract (T5.2): what a tool declares about
// its own effects so the permission system READS the declaration instead of
// guessing at the call site (zcode semantics). Declared via the optional
// EffectAware interface — implementing AgentTool is never broken, and tools
// that do not declare (plugin adapters) get the conservative default, which
// is exactly the fail-closed direction.
package agentcore

import "time"

// EffectScope bounds where a tool's side effects land (zcode subset).
type EffectScope string

const (
	// ScopeNone: no observable side effects (pure computation).
	ScopeNone EffectScope = "none"
	// ScopeWorkspace: touches files inside the workspace.
	ScopeWorkspace EffectScope = "workspace"
	// ScopeGit: mutates git state (index, refs, worktree).
	ScopeGit EffectScope = "git"
	// ScopeNetwork: performs network I/O.
	ScopeNetwork EffectScope = "network"
	// ScopeSystem: escapes the workspace (process env, global paths, shell).
	ScopeSystem EffectScope = "system"
)

// ToolEffect is the per-tool side-effect declaration. Scope is a declaration
// placeholder for v1: the permission layer consumes ReadOnly/Destructive,
// while Scope is carried for /status display and future scoping policies.
type ToolEffect struct {
	// ReadOnly marks a tool that never mutates observable state. Read-only
	// tools skip the permission confirm gate entirely.
	ReadOnly bool
	// Destructive marks a tool whose persistent effects are dangerous by
	// design. An allow RULE can never auto-approve it (minimax hard-layer
	// spirit): only directory trust or a human at the prompt may let it run.
	Destructive bool
	// Scope bounds the side effects; see the Scope* constants.
	Scope EffectScope
	// Timeout, when > 0, makes the executor wrap the tool's context in
	// context.WithTimeout. Zero means "the tool manages its own deadlines"
	// (bash background jobs, webfetch HTTP timeouts) — the default that
	// preserves current behavior.
	Timeout time.Duration
}

// EffectAware is the optional interface a tool implements to declare its
// side-effect contract. Tools that do not implement it read as conservative
// (not read-only, not destructive, needs confirmation) via EffectOf.
type EffectAware interface {
	Effect() ToolEffect
}

// EffectOf returns a tool's declared effect, or the conservative default when
// the tool does not implement EffectAware. The default scope is workspace
// (the least surprising assumption for an un-declared tool in a coding agent).
func EffectOf(t AgentTool) ToolEffect {
	if ea, ok := t.(EffectAware); ok {
		return ea.Effect()
	}
	return ToolEffect{Scope: ScopeWorkspace}
}
