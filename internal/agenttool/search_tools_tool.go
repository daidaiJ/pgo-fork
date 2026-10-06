// This file implements the search_tools built-in (T4.1 deferred tool
// declaration): the claim path of the deferred exposure tier. The model calls
// it with a query (exact name, name prefix, or description keyword); matching
// deferred tools are claimed and enter the declared face on the model's NEXT
// turn — the loop rebuilds the face when the declaration revision changes.
//
// Only-growth invariant (spec deferred-tool-exposure.md §2.2): claiming never
// removes anything — there is no tools_removed. A claim is persisted as an
// agentcore.ToolClaimMessage tree entry via the live AgentContext, so resume
// restores claims and declared face in one batch (spec §2.4).
//
// The tool is registered whenever the run's declaration plan defers anything;
// its description is static (pi 0.99.2 #10212: the announcement and this
// description are frozen once at startup, so prompt cache prefixes stay
// stable — variable content rides the announcement body, not the schema).
package agenttool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/tooldecl"
)

// searchToolsMaxResults caps one search_tools result list.
const searchToolsMaxResults = 10

// SearchToolsTool is the claim path for deferred tools. State is bound by the
// loop after it constructs the declaration state (Bind); an unbound tool
// reports that deferred declaration is inactive.
type SearchToolsTool struct {
	State *tooldecl.State
}

// Bind wires the run's declaration state. The loop calls it once after the
// state is created (including on the resume path, before the first request).
func (t *SearchToolsTool) Bind(st *tooldecl.State) { t.State = st }

// Name implements AgentTool.
func (t *SearchToolsTool) Name() string { return "search_tools" }

// Description implements AgentTool. Static by design (prompt cache).
func (t *SearchToolsTool) Description() string {
	return "Search for additional tools that are not loaded yet. " +
		"Pass an exact tool name, a name prefix, or a keyword from the tool's purpose. " +
		"Matching tools are loaded and become callable on your next turn; " +
		"they stay loaded for the rest of the session. If nothing matches, the result is an empty list — not an error."
}

// searchToolsArgs is the tool's input schema.
type searchToolsArgs struct {
	// Query is the exact tool name, a name prefix, or a description keyword.
	Query string `json:"query"`
}

// Schema implements AgentTool.
func (t *SearchToolsTool) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "query": {
      "type": "string",
      "description": "Exact tool name, name prefix, or a keyword from the tool's description"
    }
  },
  "required": ["query"]
}`)
}

// ExecutionMode implements AgentTool. Claims mutate the shared declaration
// state, so calls run sequentially.
func (t *SearchToolsTool) ExecutionMode() agentcore.ToolExecutionMode {
	return agentcore.ToolExecutionSequential
}

// Execute implements AgentTool: score the unclaimed deferred set against the
// query, claim the hits, persist the claim record, and report what happened.
// An empty match set is a normal (non-error) result — spec验收 2.
func (t *SearchToolsTool) Execute(ctx context.Context, _ string, args json.RawMessage, _ agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
	a, bad := decodeArgs[searchToolsArgs](args, t.Name())
	if bad != nil {
		return *bad, nil
	}
	if t.State == nil {
		res := errorResult("search_tools: deferred tool declaration is not active in this run")
		return res, nil
	}
	matches := tooldecl.Search(t.State.Unclaimed(), a.Query, searchToolsMaxResults)
	if len(matches) == 0 {
		// The query may target already-claimed tools (a repeat call): report
		// them as loaded rather than as a miss.
		var claimedInfos []tooldecl.ToolInfo
		for _, n := range t.State.ClaimedTools() {
			if info, ok := t.State.Plan().Deferred[n]; ok {
				claimedInfos = append(claimedInfos, info)
			}
		}
		if already := tooldecl.Search(claimedInfos, a.Query, searchToolsMaxResults); len(already) > 0 {
			names := make([]string, 0, len(already))
			for _, m := range already {
				names = append(names, m.Name)
			}
			return agentcore.AgentToolResult{
				Content: agentcore.ContentList{agentcore.NewTextContent(fmt.Sprintf(
					"Already loaded: %s. They are callable now.", strings.Join(names, ", ")))},
				Details: map[string]any{"alreadyLoaded": names},
			}, nil
		}
		// 未命中返回空集不报错 (spec验收 2): a miss is a normal result.
		return agentcore.AgentToolResult{
			Content: agentcore.ContentList{agentcore.NewTextContent(fmt.Sprintf(
				"No tools matched %q. The announcement lists every loadable tool by name.", a.Query))},
			Details: map[string]any{"matched": []string{}},
		}, nil
	}
	names := make([]string, 0, len(matches))
	for _, m := range matches {
		names = append(names, m.Name)
	}
	newly := t.State.Claim(names)
	// Persist the claim as history (canonical claim model, spec §2.4): the
	// record rides the live context like a context_edit record — replay
	// restores it, branches inherit it, resume rebuilds the declared face
	// from it without a re-search.
	if agentCtx := agentcore.AgentContextFromContext(ctx); agentCtx != nil {
		agentCtx.Messages = append(agentCtx.Messages, agentcore.ToolClaimMessage{
			RoleField: agentcore.RoleToolClaim,
			Tools:     newly,
			Timestamp: time.Now().UnixMilli(),
		})
	}
	return agentcore.AgentToolResult{
		Content: agentcore.ContentList{agentcore.NewTextContent(fmt.Sprintf(
			"Loaded %d tool(s): %s. They are NOT callable this turn — call them on your next turn.",
			len(newly), strings.Join(newly, ", ")))},
		Details: map[string]any{"claimed": newly},
	}, nil
}
