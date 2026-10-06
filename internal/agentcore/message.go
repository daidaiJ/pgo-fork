package agentcore

import (
	"encoding/json"
	"fmt"
)

// Message roles, matching pi's wire format.
const (
	RoleUser       = "user"
	RoleAssistant  = "assistant"
	RoleToolResult = "toolResult"
	// RoleCompaction marks a compaction checkpoint persisted inline in the
	// message list: it replaces the history summarized before it (pi's
	// "compactionSummary"). It is not sent to the model verbatim; the LLM
	// conversion turns it into a user text block.
	RoleCompaction = "compaction"
	// RoleMicrocompact marks a microcompaction decision record (T3.3): cleared
	// tool-result ids, projection metadata — never rendered into a request.
	RoleMicrocompact = "microcompact"
	// RoleContextEdit marks a canonical context edit record (T3.4): an explicit,
	// model-requested visibility edit over earlier history (replace with a
	// digest / hide from the view). Projection metadata like microcompact: it
	// is persisted as a first-class tree entry — the edit is itself history —
	// but never rendered into a request; the projection applies it.
	RoleContextEdit = "contextEdit"
)

// Message is the sealed interface implemented by the three message roles.
// AgentMessage (the loop's message abstraction) is simply Message: custom
// message kinds implement the same interface and convertToLlm filters out any
// that are not LLM-bound. This deliberately replaces pi's declaration merging,
// which has no Go equivalent.
type Message interface {
	isMessage()
	// Role returns the discriminant ("user" | "assistant" | "toolResult").
	Role() string
}

// AgentMessage is the loop-level message type. It is the same as Message; the
// alias documents intent at call sites that deal with the loop rather than raw
// LLM messages.
type AgentMessage = Message

// Usage reports token accounting for an assistant response. Cache buckets are
// optional: providers whose decoders do not surface them leave them zero, and
// consumers must treat them as "unknown", not "absent cost" — context-token
// math adds them on top of InputTokens only when the source API reports cache
// tokens separately from input (anthropic does; openai's prompt_tokens already
// folds its cached share into prompt_tokens and is decoded without them).
type Usage struct {
	InputTokens      int `json:"inputTokens"`
	OutputTokens     int `json:"outputTokens"`
	CacheReadTokens  int `json:"cacheReadTokens,omitempty"`
	CacheWriteTokens int `json:"cacheWriteTokens,omitempty"`
}

// UserMessage is input from the user. Content is restricted at construction to
// text/image blocks (runtime constraint, not a separate interface).
type UserMessage struct {
	RoleField string      `json:"role"`
	Content   ContentList `json:"content"`
	Timestamp int64       `json:"timestamp"`
}

func (UserMessage) isMessage()     {}
func (m UserMessage) Role() string { return RoleUser }

// AssistantMessage is a model response. Content may hold text/thinking/toolCall
// blocks. StopReason follows pi's set (end_turn/tool_use/length/error/aborted).
type AssistantMessage struct {
	RoleField    string      `json:"role"`
	Content      ContentList `json:"content"`
	API          string      `json:"api,omitempty"`
	Provider     string      `json:"provider,omitempty"`
	Model        string      `json:"model,omitempty"`
	Usage        *Usage      `json:"usage,omitempty"`
	StopReason   string      `json:"stopReason,omitempty"`
	ErrorMessage string      `json:"errorMessage,omitempty"`
	Timestamp    int64       `json:"timestamp"`

	// Optional diagnostics, kept for cross-provider replay/observability.
	ResponseModel string `json:"responseModel,omitempty"`
	ResponseID    string `json:"responseId,omitempty"`
}

func (AssistantMessage) isMessage()     {}
func (m AssistantMessage) Role() string { return RoleAssistant }

// ToolCalls returns the tool call blocks in this assistant message, in order.
func (m AssistantMessage) ToolCalls() []ToolCallContent {
	var calls []ToolCallContent
	for _, c := range m.Content {
		if tc, ok := c.(ToolCallContent); ok {
			calls = append(calls, tc)
		}
	}
	return calls
}

// ToolResultMessage carries the outcome of executing a single tool call.
// Content is restricted to text/image blocks at construction.
type ToolResultMessage struct {
	RoleField  string      `json:"role"`
	ToolCallID string      `json:"toolCallId"`
	ToolName   string      `json:"toolName"`
	Content    ContentList `json:"content"`
	Details    any         `json:"details,omitempty"`
	IsError    bool        `json:"isError"`
	Timestamp  int64       `json:"timestamp"`
}

func (ToolResultMessage) isMessage()     {}
func (m ToolResultMessage) Role() string { return RoleToolResult }

// CompactionMessage is a summarization checkpoint persisted inline in the
// message list. It stands in for the history compacted before it: Summary is
// the structured checkpoint text and TokensBefore records the estimated context
// size at compaction time (for observability). Details optionally holds the
// file operations extracted from the compacted range. Mirrors pi's
// CompactionSummaryMessage + CompactionEntry.
type CompactionMessage struct {
	RoleField    string `json:"role"`
	Summary      string `json:"summary"`
	TokensBefore int    `json:"tokensBefore,omitempty"`
	// Details is opaque at this layer (the compaction package owns its shape);
	// kept as raw JSON so agentcore stays free of a compaction dependency.
	Details   json.RawMessage `json:"details,omitempty"`
	Timestamp int64           `json:"timestamp"`

	// FirstKeptIndex is the compaction cut expressed in the live message-list
	// coordinates at compaction time (observability / driver-side math only;
	// replay projection uses KeptBefore, which does not drift when the tree
	// grows or the active leaf moves).
	FirstKeptIndex int `json:"firstKeptIndex,omitempty"`
	// KeptBefore is the replay-projection anchor: how many entries immediately
	// before this marker on the persisted path belong to the KEPT window rather
	// than the summarized range. Projection restores them ahead of the marker.
	// Zero when everything before the marker on the path is summarized.
	KeptBefore int `json:"keptBefore,omitempty"`
	// TokensAfter is the estimated context tokens of the post-compaction view
	// (the "post" of the pre/post dual-caliber boundary event, T3.3).
	TokensAfter int `json:"tokensAfter,omitempty"`
	// StrategyVersion tags the compaction strategy that produced this marker
	// (minimax metadata alignment); 1 = the T3.3 marker+projection model.
	StrategyVersion int `json:"strategyVersion,omitempty"`
}

func (CompactionMessage) isMessage()     {}
func (m CompactionMessage) Role() string { return RoleCompaction }

// compactionSummaryPrefix / compactionSummarySuffix wrap a compaction summary
// when it is rendered into an LLM user message, matching pi's
// COMPACTION_SUMMARY_PREFIX / COMPACTION_SUMMARY_SUFFIX.
const (
	compactionSummaryPrefix = "The conversation history before this point was compacted into the following summary:\n\n<summary>\n"
	compactionSummarySuffix = "\n</summary>"
)

// AsUserMessage renders a compaction checkpoint as the user text message that
// stands in for the compacted history when building the LLM request. The
// provider encoders call this so a persisted compaction line replays as
// context rather than being dropped.
func (m CompactionMessage) AsUserMessage() UserMessage {
	return UserMessage{
		RoleField: RoleUser,
		Content:   ContentList{NewTextContent(compactionSummaryPrefix + m.Summary + compactionSummarySuffix)},
		Timestamp: m.Timestamp,
	}
}

// MicrocompactMessage is a microcompaction decision record (T3.3): it does not
// stand in for history — it records WHICH tool results were evicted from the
// request view (by tool-call id) and the estimated tokens saved. It is
// projection metadata: appended to the live list and persisted as a tree entry
// (sticky, so evicted results never resurrect across turns or resume), but
// never rendered into an LLM request — the compaction projection replaces the
// matching tool results with placeholders and drops these markers.
type MicrocompactMessage struct {
	RoleField string `json:"role"`
	// ClearedCallIDs are the tool-call ids whose results were evicted.
	ClearedCallIDs []string `json:"clearedCallIds"`
	// SavedTokens is the estimated token footprint removed from the view.
	SavedTokens int   `json:"savedTokens,omitempty"`
	Timestamp   int64 `json:"timestamp"`
}

func (MicrocompactMessage) isMessage()       {}
func (m MicrocompactMessage) Role() string   { return RoleMicrocompact }

// Context edit modes (T3.4). ModeReplace swaps the target's visible content
// for the model-supplied digest; ModeHide removes it from the view — for tool
// results both keep a one-line placeholder so the tool_use/tool_result pairing
// stays intact for strict providers.
const (
	ModeReplace = "replace"
	ModeHide    = "hide"
)

// ContextEdit is one visibility edit inside a ContextEditMessage: it targets
// one earlier history entry by tool-call id (exact, the primary handle — the
// model knows its own call ids) or by seq (its 0-based index in the request
// view it saw) plus a content hash anchoring the resolved entry.
type ContextEdit struct {
	// TargetSeq is the target's index in the request view at edit time. It
	// doubles as the proximity hint when the projection re-locates the target
	// by ContentHash after markers/edits shifted the list, and as the "#seq"
	// shown in the placeholder text.
	TargetSeq int `json:"targetSeq"`
	// TargetCallID, when non-empty, is the tool-call id of a ToolResultMessage
	// target — an exact, shift-proof anchor resolved by scanning the list.
	TargetCallID string `json:"targetCallId,omitempty"`
	// ContentHash is a short digest of the target's rendered text at edit
	// time; the projection re-locates and re-verifies the target with it, so
	// an edit whose target was folded away by a later compaction (or rewind)
	// is skipped instead of misapplied.
	ContentHash string `json:"contentHash,omitempty"`
	// Mode is "replace" or "hide".
	Mode string `json:"mode"`
	// Digest is the model-supplied summary that replaces the target's visible
	// content (replace mode only).
	Digest string `json:"digest,omitempty"`
}

// ContextEditMessage is the durable record of one context_edit tool call
// (T3.4 canonical context edit). Like MicrocompactMessage it is projection
// metadata — appended to the live list (and thus persisted as a tree entry via
// the next PersistTurn, so the edit is itself history: replay re-applies it,
// forked branches inherit it) — but it never renders into a request: the
// compaction projection resolves and applies each edit to the view. The raw
// history is never touched (canonical: append-only, lossless).
type ContextEditMessage struct {
	RoleField string        `json:"role"`
	Edits     []ContextEdit `json:"edits"`
	Timestamp int64         `json:"timestamp"`
}

func (ContextEditMessage) isMessage()     {}
func (m ContextEditMessage) Role() string { return RoleContextEdit }

// StopReason values, matching pi.
const (
	StopReasonEndTurn = "end_turn"
	StopReasonToolUse = "tool_use"
	StopReasonLength  = "length"
	StopReasonError   = "error"
	StopReasonAborted = "aborted"
)

// MessageList is a slice of Message with discriminated JSON (un)marshalling,
// dispatching on the "role" field. Used by AgentContext and session persistence.
type MessageList []Message

// UnmarshalJSON decodes a JSON array of messages, dispatching each element on
// its "role" discriminant.
func (ml *MessageList) UnmarshalJSON(data []byte) error {
	var raws []json.RawMessage
	if err := json.Unmarshal(data, &raws); err != nil {
		return err
	}
	out := make(MessageList, 0, len(raws))
	for i, raw := range raws {
		m, err := decodeMessage(raw)
		if err != nil {
			return fmt.Errorf("message[%d]: %w", i, err)
		}
		out = append(out, m)
	}
	*ml = out
	return nil
}

// decodeMessage peeks at the "role" field and decodes into the matching
// concrete message struct.
func decodeMessage(raw json.RawMessage) (Message, error) {
	var probe struct {
		Role string `json:"role"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, fmt.Errorf("peek role: %w", err)
	}
	switch probe.Role {
	case RoleUser:
		var m UserMessage
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, err
		}
		return m, nil
	case RoleAssistant:
		var m AssistantMessage
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, err
		}
		return m, nil
	case RoleToolResult:
		var m ToolResultMessage
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, err
		}
		return m, nil
	case RoleCompaction:
		var m CompactionMessage
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, err
		}
		return m, nil
	case RoleMicrocompact:
		var m MicrocompactMessage
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, err
		}
		return m, nil
	case RoleContextEdit:
		var m ContextEditMessage
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, err
		}
		return m, nil
	case "":
		return nil, fmt.Errorf("missing role discriminant")
	default:
		return nil, fmt.Errorf("unknown role %q", probe.Role)
	}
}
