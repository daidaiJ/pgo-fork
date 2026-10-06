// This file implements the ask_user tool (T4.2, questionnaire-ask-user.md): a
// structured multi-step questionnaire the model uses to ask the user a set of
// clarifying questions (option picks, free text, skip) instead of a free-form
// "let me know" sentence.
//
// Execution blocks synchronously on the QuestionPort seam (the zcode/kimi
// blocking shape, not minimax's stop-the-turn daemon round-trip — deviation
// D-1): the REPL port asks on the shared stdin under ConfirmMu, the TUI port
// shows the question panel, and a nil port (headless/SDK/webhook) degrades to
// auto-answered defaults (D-3). Q&A lands in history as the ordinary tool
// result — no new message type.
package agenttool

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/questionnaire"
)

// AskName is the tool name registered for the questionnaire flow.
const AskName = "ask_user"

// QuestionPort is the seam the ask_user tool blocks on to collect answers from
// a real user. Implementations live per driver (REPL stdin, TUI panel); a nil
// port means no interactive user is available and the tool degrades.
type QuestionPort interface {
	// Ask presents the questionnaire and blocks until the user submits, skips
	// through, or ctx is cancelled (run interrupt). The returned Reply must
	// cover every step of q.
	Ask(ctx context.Context, q questionnaire.Questionnaire) (questionnaire.Reply, error)
}

// AskUserTool is the structured-questions tool. Port is injected per driver
// (run.SetAskPort); nil degrades to auto-answered defaults.
type AskUserTool struct {
	// Port collects the answers; nil = degraded (wiki spec §4.3).
	Port QuestionPort
}

// askToolArgs mirrors questionnaire.Input (kept local so the tool's decode
// shape is stable even if the leaf package grows non-wire fields).
type askToolArgs = questionnaire.Input

// Name implements AgentTool.
func (t *AskUserTool) Name() string { return AskName }

// Effect declares ask_user as read-only (T5.2): it only consults the user.
func (t *AskUserTool) Effect() agentcore.ToolEffect {
	return agentcore.ToolEffect{ReadOnly: true, Scope: agentcore.ScopeNone}
}

// Description implements AgentTool. The wording steers the model toward using
// it for genuine decision points and writing concrete options.
func (t *AskUserTool) Description() string {
	return "Ask the user a structured multi-step questionnaire (1-4 questions, " +
		"each with up to 4 options plus a free-text 'other' entry and an " +
		"explicit skip). Use it at real decision points — ambiguous " +
		"requirements, a fork in approach, or before an irreversible action — " +
		"and write concrete, mutually distinguishable options. Mark at most " +
		"one option per question with recommended=true as the safe default. " +
		"The call blocks until the user answers every question and the " +
		"answers come back as the tool result."
}

// Schema implements AgentTool. Bounds mirror minimax: 1..4 steps, 0..4 options
// per step; everything but step.question is optional.
func (t *AskUserTool) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "title": {"type": "string", "description": "Optional overall heading for the questionnaire."},
    "requires_explicit_response": {"type": "boolean", "description": "Set true only for terminal-action confirmations (e.g. before an irreversible operation). Without an interactive user such a questionnaire is NOT auto-answered; the result tells you to state an assumption instead of proceeding."},
    "steps": {
      "type": "array",
      "minItems": 1,
      "maxItems": 4,
      "description": "The questions, asked in order. One question per step.",
      "items": {
        "type": "object",
        "properties": {
          "id": {"type": "string", "description": "Optional stable step id; auto-assigned when omitted."},
          "header": {"type": "string", "description": "Optional short section label shown above the question."},
          "question": {"type": "string", "description": "The question body."},
          "description": {"type": "string", "description": "Optional extra context for the question."},
          "selection_mode": {"type": "string", "enum": ["single", "multiple"], "description": "Whether the user picks one option or any number. Default single."},
          "allow_other": {"type": "boolean", "description": "Whether a free-text 'other' entry is offered. Default true."},
          "options": {
            "type": "array",
            "maxItems": 4,
            "description": "Up to 4 answer choices. Omit for a free-text-only question.",
            "items": {
              "type": "object",
              "properties": {
                "id": {"type": "string", "description": "Optional stable option id; auto-assigned when omitted."},
                "label": {"type": "string", "description": "The option text shown to the user."},
                "description": {"type": "string", "description": "Optional hint text shown next to the label."},
                "recommended": {"type": "boolean", "description": "Mark at most one option per step as the safe default. A no-user run auto-picks it (or the first option); interactive front-ends highlight it and Enter selects it."}
              },
              "required": ["label"],
              "additionalProperties": false
            }
          }
        },
        "required": ["question"],
        "additionalProperties": false
      }
    }
  },
  "required": ["steps"],
  "additionalProperties": false
}`)
}

// ExecutionMode implements AgentTool. Asking the user touches the shared
// stdin/panel channel → sequential so a batch cannot race two questionnaires
// (the same discipline the trust confirmation relies on).
func (t *AskUserTool) ExecutionMode() agentcore.ToolExecutionMode {
	return agentcore.ToolExecutionSequential
}

// Execute implements AgentTool. It normalizes the authored questions, collects
// answers through the port (or the degraded auto-answer), and returns the
// rendered Q&A as the result text. Invalid input degrades to an error result
// rather than a Go error; a port failure (e.g. run interrupt) likewise.
func (t *AskUserTool) Execute(ctx context.Context, id string, args json.RawMessage, onUpdate agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
	a, bad := decodeArgs[askToolArgs](args, AskName)
	if bad != nil {
		return *bad, nil
	}
	q, err := questionnaire.Normalize(a)
	if err != nil {
		return errorResult(fmt.Sprintf("%s: %v", AskName, err)), nil
	}

	var reply questionnaire.Reply
	if t.Port != nil {
		reply, err = t.Port.Ask(ctx, q)
		if err != nil {
			return errorResult(fmt.Sprintf("%s: not answered (%v); continue without the user's input and state your assumption", AskName, err)), nil
		}
	} else if q.RequiresExplicitResponse {
		return errorResult(askNoUserGuidance), nil
	} else {
		reply = questionnaire.DegradedReply(q)
	}

	text := questionnaire.ResultText(q, reply)
	return agentcore.AgentToolResult{
		Content: agentcore.ContentList{agentcore.NewTextContent(text)},
		Details: map[string]any{
			"source": reply.Source,
			"steps":  len(q.Steps),
		},
	}, nil
}

// askNoUserGuidance is the degraded result for a requires_explicit_response
// questionnaire: the model must not proceed with the irreversible action.
const askNoUserGuidance = AskName + ": no interactive user is available to confirm. " +
	"Do NOT perform the irreversible action; state your assumption or plan explicitly and continue with a reversible step instead."
