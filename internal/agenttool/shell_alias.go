// The shell tool alias (T8.4 user ruling): the bash tool is additionally
// cataloged under the name "shell" — two entries, one implementation. Same
// schema, same execution, same side-effect contract. The boundary follows
// the capability, both in the tool policy (run package: the alias maps onto
// bash's allow/deny family) and in the shellguard seam (which gates both
// names).
package agenttool

import (
	"context"
	"encoding/json"

	"github.com/smallnest/pigo/internal/agentcore"
)

// ShellAliasTool delegates to the live BashTool, so a /shell backend switch
// applies to calls through either name.
type ShellAliasTool struct {
	Bash *BashTool
}

// Name implements AgentTool.
func (t *ShellAliasTool) Name() string { return "shell" }

// Effect mirrors bash (T5.2): a shell escapes the workspace (ScopeSystem).
func (t *ShellAliasTool) Effect() agentcore.ToolEffect { return t.Bash.Effect() }

// Description implements AgentTool — one line; the full contract lives on
// bash, and every extra token here is a per-turn declaration cost.
func (t *ShellAliasTool) Description() string {
	return "Alias of bash — run a shell command (same arguments and behavior; " +
		"the interpreter follows the configured [shell] backend)."
}

// Schema implements AgentTool.
func (t *ShellAliasTool) Schema() json.RawMessage { return t.Bash.Schema() }

// ExecutionMode implements AgentTool.
func (t *ShellAliasTool) ExecutionMode() agentcore.ToolExecutionMode {
	return agentcore.ToolExecutionSequential
}

// Execute implements AgentTool.
func (t *ShellAliasTool) Execute(ctx context.Context, id string, args json.RawMessage, onUpdate agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
	return t.Bash.Execute(ctx, id, args, onUpdate)
}
