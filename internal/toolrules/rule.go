// Package toolrules is the permission rule layer (T5.2): persistent and
// session rules (allow/deny per tool + pattern), the self-edit surface guard,
// and the BeforeToolCall decision engine that replaces the old
// "trusted directory or confirm every call" dichotomy.
//
// It is a leaf package: it depends only on agentcore (+shellguard for the
// bash-propose safety gate) and performs zero interactive IO — confirmation
// UI is injected by drivers through the AskPort seam (T4.2 QuestionPort
// precedent). Decision semantics follow the references converged in
// wiki/port/tool-rules-layer.md §2: deny is terminal (trust/bypass never
// overrides it, minimax hard-layer spirit), read-only tools skip the gate,
// and repeated approvals can be settled into persistent rules (kimi
// session-approval-history + minimax ProposedRule).
package toolrules

import (
	"fmt"
	"strings"
)

// Action is what a rule decides for matching calls.
type Action string

const (
	ActionAllow Action = "allow"
	ActionDeny  Action = "deny"
)

// Scope distinguishes in-memory rules (this session only, mirroring the old
// "always" grant) from rules persisted to permissions.json.
type Scope string

const (
	ScopeSession   Scope = "session"
	ScopePersisted Scope = "persisted"
)

// Rule gates calls to one tool by pattern. Pattern semantics per tool:
//
//   - bash: word-boundary command prefix (grok制度 — the pattern must be
//     followed by whitespace or end of segment). Allow rules never match
//     compound commands (`;`, `&&`, pipes, command substitution); deny rules
//     match any compound segment (deny does not need precision).
//   - write/edit: path prefix (see matchPath).
//   - other tools: empty pattern = the whole tool.
type Rule struct {
	Tool    string `json:"tool"`
	Pattern string `json:"pattern,omitempty"`
	Action  Action `json:"action"`
	Scope   Scope  `json:"scope,omitempty"`
}

// NormalizeRule lowercases and trims the tool name and validates the rest.
// It returns an error for config-facing input (the caller maps that to a
// startup failure rather than silently dropping a boundary).
func NormalizeRule(r Rule) (Rule, error) {
	r.Tool = strings.ToLower(strings.TrimSpace(r.Tool))
	r.Pattern = strings.TrimSpace(r.Pattern)
	switch r.Action {
	case ActionAllow, ActionDeny:
	case "":
		return r, fmt.Errorf("toolrules: rule for %q: missing action (allow|deny)", r.Tool)
	default:
		return r, fmt.Errorf("toolrules: rule for %q: unknown action %q", r.Tool, r.Action)
	}
	switch r.Scope {
	case ScopeSession, ScopePersisted, "":
		r.Scope = ScopePersisted
	default:
		return r, fmt.Errorf("toolrules: rule for %q: unknown scope %q", r.Tool, r.Scope)
	}
	if r.Tool == "" {
		return r, fmt.Errorf("toolrules: rule: empty tool name")
	}
	return r, nil
}

// normalizeToolName matches toolpolicy.NormalizeToolName semantics
// (lowercase + trim) so user-written rules hit canonical names.
func normalizeToolName(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}
