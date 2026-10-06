// This file implements the context_edit tool (T3.4 canonical context edit,
// wiki/port/canonical-context-edit.md): the model's explicit handle to slim the
// VISIBLE context without touching history. Each call appends one durable
// ContextEditMessage to the live conversation — the edit is itself a session
// tree entry (replay re-applies it, forked branches inherit it) — and the
// compaction projection applies it to every later request view. Targets are
// addressed by tool-call id (exact; the model knows its own call ids) or by
// seq (0-based index in the request view it saw), and are anchored with a
// content digest so a later compaction/rewind can re-locate or safely skip
// them.
package agenttool

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/compaction"
)

// ContextEditTool edits the visible context of the running conversation. It
// carries no state of its own: it reaches the live conversation through the
// loop-injected AgentContext (agentcore.WithAgentContext), so it works in every
// driver without wiring and is inert outside a loop run.
type ContextEditTool struct{}

// contextEditArgs is the decoded argument shape for one edit request.
type contextEditArgs struct {
	// ToolCallID targets a tool result by its tool-call id (preferred: exact).
	ToolCallID string `json:"tool_call_id,omitempty"`
	// Seq targets any visible message by its 0-based index in the request view
	// the model saw. Ignored when tool_call_id is set.
	Seq *int `json:"seq,omitempty"`
	// Mode is "replace" (show the digest instead of the content) or "hide"
	// (remove from the view; tool results keep a one-line placeholder to
	// preserve pairing).
	Mode string `json:"mode"`
	// Digest is the replacement summary; required for replace.
	Digest string `json:"digest,omitempty"`
}

type contextEditToolArgs struct {
	Edits []contextEditArgs `json:"edits"`
}

// Name implements AgentTool.
func (t *ContextEditTool) Name() string { return "context_edit" }

// Effect declares context_edit as read-only for permission purposes (T5.2):
// it edits the conversation's own history view (T3.4), an in-process effect
// with no user-observable footprint outside the session.
func (t *ContextEditTool) Effect() agentcore.ToolEffect {
	return agentcore.ToolEffect{ReadOnly: true, Scope: agentcore.ScopeNone}
}

// Description implements AgentTool. It doubles as the prompt guidance the spec
// asks for (§3.3): what the tool is for, what may be targeted, and the
// discipline around it.
func (t *ContextEditTool) Description() string {
	return "Edit the visible conversation context without changing history: replace an " +
		"earlier entry's content with a short digest you write, or hide it from view. " +
		"Target a tool result by its tool_call_id (preferred, exact), or any visible " +
		"message by its seq (0-based index counting user/assistant/tool-result messages " +
		"in order). Use it to retire large, no-longer-needed earlier outputs once their " +
		"information is captured elsewhere (todo, summary, your reply); never edit an " +
		"entry you still need verbatim. Edits apply from the next turn onward, are " +
		"persisted as session entries, and cannot be undone — first edit wins."
}

// Schema implements AgentTool.
func (t *ContextEditTool) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "edits": {
      "type": "array",
      "minItems": 1,
      "description": "The visibility edits to apply, in order.",
      "items": {
        "type": "object",
        "properties": {
          "tool_call_id": {"type": "string", "description": "Tool-call id of a tool result to edit (exact; preferred over seq)."},
          "seq":          {"type": "integer", "minimum": 0, "description": "0-based index of the target message in the conversation view (user/assistant/tool-result messages, in order)."},
          "mode":         {"type": "string", "enum": ["replace", "hide"], "description": "replace = show your digest instead of the content; hide = drop from view."},
          "digest":       {"type": "string", "description": "Your replacement summary. Required for replace; keep it short but faithful."}
        },
        "anyOf": [
          {"required": ["tool_call_id", "mode"]},
          {"required": ["seq", "mode"]}
        ]
      }
    }
  },
  "required": ["edits"]
}`)
}

// ExecutionMode implements AgentTool. The tool mutates the shared conversation
// list, so a batch containing it runs serially.
func (t *ContextEditTool) ExecutionMode() agentcore.ToolExecutionMode {
	return agentcore.ToolExecutionSequential
}

// Execute validates each requested edit against the live conversation, appends
// one ContextEditMessage marker, and reports what was applied and what was
// rejected (per-edit, so one bad target does not sink the rest).
func (t *ContextEditTool) Execute(ctx context.Context, id string, args json.RawMessage, onUpdate agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
	agentCtx := agentcore.AgentContextFromContext(ctx)
	if agentCtx == nil {
		return errorResult("context_edit is only available inside a running agent loop"), nil
	}
	var decoded contextEditToolArgs
	if err := json.Unmarshal(args, &decoded); err != nil {
		return errorResult(fmt.Sprintf("context_edit: invalid arguments: %v", err)), nil
	}
	if len(decoded.Edits) == 0 {
		return errorResult("context_edit: edits must not be empty"), nil
	}

	view := compaction.ProjectView(agentCtx.Messages)
	marker := agentcore.ContextEditMessage{
		RoleField: agentcore.RoleContextEdit,
		Timestamp: time.Now().UnixMilli(),
	}
	report := ""
	for i, a := range decoded.Edits {
		e, err := t.resolve(agentCtx.Messages, view, a)
		if err != nil {
			report += fmt.Sprintf("edit %d: rejected: %v\n", i, err)
			continue
		}
		marker.Edits = append(marker.Edits, e)
		report += fmt.Sprintf("edit %d: ok (%s %s)\n", i, describeTarget(e), e.Mode)
	}
	if len(marker.Edits) == 0 {
		return errorResult("context_edit: no edit was applicable:\n" + report), nil
	}
	agentCtx.Messages = append(agentCtx.Messages, marker)
	return agentcore.AgentToolResult{
		Content: agentcore.ContentList{agentcore.NewTextContent(
			fmt.Sprintf("Applied %d context edit(s); the change takes effect from the next turn.\n%s",
				len(marker.Edits), report))},
	}, nil
}

// resolve validates one edit request against the live history and its view,
// returning the durable edit record. Targeting rules mirror the projection
// (compaction.edit.go): a tool-call id resolves exactly against the raw
// history (validated here, so a typo is rejected up front instead of silently
// no-oping at projection time); a seq resolves against the view and is
// anchored with the entry's content digest.
func (t *ContextEditTool) resolve(raw, view agentcore.MessageList, a contextEditArgs) (agentcore.ContextEdit, error) {
	if a.Mode != agentcore.ModeReplace && a.Mode != agentcore.ModeHide {
		return agentcore.ContextEdit{}, fmt.Errorf("mode must be %q or %q, got %q",
			agentcore.ModeReplace, agentcore.ModeHide, a.Mode)
	}
	if a.Mode == agentcore.ModeReplace && a.Digest == "" {
		return agentcore.ContextEdit{}, fmt.Errorf("mode %q requires a non-empty digest", agentcore.ModeReplace)
	}
	e := agentcore.ContextEdit{Mode: a.Mode, Digest: a.Digest}

	if a.ToolCallID != "" {
		found := false
		for _, m := range raw {
			if tr, ok := m.(agentcore.ToolResultMessage); ok && tr.ToolCallID == a.ToolCallID {
				found = true
				break
			}
		}
		if !found {
			return agentcore.ContextEdit{}, fmt.Errorf("no tool result with call id %q", a.ToolCallID)
		}
		e.TargetCallID = a.ToolCallID
		// Seq stays 0 for call-id edits; the projection resolves by id and
		// never consults the seq hint or the digest for them.
		return e, nil
	}
	if a.Seq == nil {
		return agentcore.ContextEdit{}, fmt.Errorf("one of tool_call_id or seq is required")
	}
	if *a.Seq < 0 || *a.Seq >= len(view) {
		return agentcore.ContextEdit{}, fmt.Errorf("seq %d out of range (view has %d messages)", *a.Seq, len(view))
	}
	target := view[*a.Seq]
	switch m := target.(type) {
	case agentcore.UserMessage, agentcore.ToolResultMessage:
		// Both editable as-is.
	case agentcore.AssistantMessage:
		if a.Mode == agentcore.ModeHide && len(m.ToolCalls()) > 0 {
			return agentcore.ContextEdit{}, fmt.Errorf(
				"seq %d is an assistant message carrying tool calls; hiding it would orphan its tool results", *a.Seq)
		}
	default:
		return agentcore.ContextEdit{}, fmt.Errorf("seq %d is not an editable message (%s)", *a.Seq, target.Role())
	}
	e.TargetSeq = *a.Seq
	e.ContentHash = agentcore.MessageTextDigest(target)
	return e, nil
}

// describeTarget renders a human-readable target tag for the tool report.
func describeTarget(e agentcore.ContextEdit) string {
	if e.TargetCallID != "" {
		return "call " + e.TargetCallID
	}
	return fmt.Sprintf("seq %d", e.TargetSeq)
}
