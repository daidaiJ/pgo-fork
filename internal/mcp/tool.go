// This file adapts one MCP tool to the agentcore.AgentTool interface so MCP
// servers register into the same tool face as built-ins and plugins.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/smallnest/pigo/internal/agentcore"
)

// MCPTool is one MCP tool exposed to the agent. Its registry name is the
// three-segment mcp__<server>__<tool>, which is what makes it addressable by
// the declaration tiers and by a permission rule (per-tool disable, spec §3.1).
type MCPTool struct {
	server string
	client *Client
	tool   Tool
}

// Name implements AgentTool: the namespaced mcp__<server>__<tool> form.
func (t *MCPTool) Name() string { return ToolName(t.server, t.tool.Name) }

// Server reports which configured server this tool came from. It is the seam
// diagnostics and the declaration plan use to label the tool's surface as
// mcp:<server> instead of a blanket external label (spec mcp-integration-shape.md
// deviation D-6: the announcement must distinguish servers).
func (t *MCPTool) Server() string { return t.server }

// Description implements AgentTool: the server's description, falling back to
// the tool name so an undescribed tool still has a non-empty announcement.
func (t *MCPTool) Description() string {
	if t.tool.Description != "" {
		return t.tool.Description
	}
	return t.tool.Name
}

// Schema implements AgentTool. An absent schema degrades to a permissive object
// schema so registration can never fail on a server's omission.
func (t *MCPTool) Schema() json.RawMessage {
	if len(t.tool.InputSchema) == 0 {
		return json.RawMessage(`{"type":"object"}`)
	}
	return t.tool.InputSchema
}

// ExecutionMode implements AgentTool. MCP calls are correlated by id over one
// stdio transport, so concurrency is safe, and a server's tools are independent
// of pigo's own; running them in parallel is what keeps one slow server from
// serializing a whole batch. The per-server cap is applied inside CallTool.
//
// This differs from plugin tools, which stay sequential — plugin tools predate
// that cap and changing their mode is out of scope here (deviation D-6).
func (t *MCPTool) ExecutionMode() agentcore.ToolExecutionMode {
	return agentcore.ToolExecutionParallel
}

// Effect implements agentcore.EffectAware: it maps the server's annotations
// onto pigo's side-effect contract (T5.2), so a tool published read-only skips
// the confirmation gate the same way a built-in read-only tool does. Hints the
// server omits stay conservative (not read-only, workspace scope).
func (t *MCPTool) Effect() agentcore.ToolEffect {
	scope := agentcore.ScopeWorkspace
	if t.tool.OpenWorld {
		// openWorldHint means the tool reaches outside the workspace.
		scope = agentcore.ScopeNetwork
	}
	return agentcore.ToolEffect{
		ReadOnly:    t.tool.ReadOnly,
		Destructive: t.tool.Destructive,
		Scope:       scope,
		Timeout:     t.client.cfg.Timeout(),
	}
}

// Replayable reports whether an interrupted call against this tool may be
// re-sent. It is the seam the reconnect path will consult; slice 1 has no
// reconnect, so nothing replays yet (spec §3.4, deviation D-7).
func (t *MCPTool) Replayable() bool { return t.tool.Replayable() }

// Execute implements AgentTool by forwarding the call to the server. A
// transport error (dead or unreachable server) is isolated: it degrades to an
// error result so one crashed MCP server cannot take down the agent loop.
func (t *MCPTool) Execute(ctx context.Context, id string, args json.RawMessage, onUpdate agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
	text, isError, err := t.client.CallTool(ctx, t.tool.Name, args)
	if err != nil {
		return agentcore.AgentToolResult{
			Content: agentcore.ContentList{agentcore.NewTextContent(
				fmt.Sprintf("%s: mcp call failed: %v", t.Name(), err))},
		}, nil
	}
	result := agentcore.AgentToolResult{
		Content: agentcore.ContentList{agentcore.NewTextContent(text)},
	}
	if isError {
		result.Details = map[string]any{"isError": true}
	}
	return result, nil
}
