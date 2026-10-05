// This file wires the shellguard static bash-command analysis onto the
// BeforeToolCall seam (T2.1). shellguard is an opt-in advanced feature
// (user decision 2026-10-05): the default mode is off, which costs nothing —
// the seam is not installed at all. In ask mode an interactive host (REPL)
// supplies an ask callback; hosts without a per-call channel (TUI, headless)
// leave it nil so flagged commands fail closed. The headless denial seam
// additionally implements --non-interactive-denial terminate|continue.
package agenttool

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/shellguard"
)

// bashToolName is the only tool the shellguard seam gates.
const bashToolName = "bash"

// ShellguardAsk is the interactive approval callback for ask mode. It reports
// whether the user allowed the flagged command this once. Implementations
// must honor ctx cancellation (a canceled context denies).
type ShellguardAsk func(ctx context.Context, call agentcore.AgentToolCall, d shellguard.Decision) bool

// ShellguardSeam builds the BeforeToolCallFunc gating bash commands through
// shellguard.Analyze. mode==off (or the zero-value "", as embedded in
// zero-value session structs) returns nil — the seam is never installed, so
// the hot path pays nothing. Safe commands fall through to the next seam in
// the chain; Hazardous/Incomplete are blocked — ask mode consults the ask
// callback first (nil ask fails closed), strict mode blocks outright.
//
// Hazardous verdicts are never waivable by allow-lists: the seam has no
// grant-store memory, so an ask-mode allow is per-call only.
func ShellguardSeam(mode shellguard.Mode, ask ShellguardAsk) agentcore.BeforeToolCallFunc {
	if mode == shellguard.ModeOff || mode == "" {
		return nil
	}
	return func(ctx context.Context, call agentcore.AgentToolCall) *agentcore.BeforeToolCallDecision {
		if call.Name != bashToolName {
			return nil
		}
		d := shellguard.Analyze(bashCommand(call.Arguments))
		switch d.Verdict {
		case shellguard.Safe:
			return nil
		case shellguard.Incomplete:
			// fail closed: an unanalyzed command never auto-allows
		case shellguard.Hazardous:
		}
		if mode == shellguard.ModeAsk && ask != nil {
			if ask(ctx, call, d) {
				return nil
			}
		}
		return &agentcore.BeforeToolCallDecision{
			Block:   true,
			Content: &agentcore.ContentList{agentcore.NewTextContent(blockMessage("ask", d, call))},
		}
	}
}

// ShellguardDenialSeam builds the headless variant of the seam: there is no
// interactive channel, so Hazardous/Incomplete commands are always denied.
// allowContinue mirrors --non-interactive-denial continue: the denial becomes
// a failed tool result the agent can route around, and the run keeps going.
// Without it the seam cancels the run context (terminate) and terminated
// reports true afterwards so the driver can explain the exit. mode==off
// returns (nil, nil) — nothing is installed.
func ShellguardDenialSeam(mode shellguard.Mode, allowContinue bool, cancel context.CancelFunc) (agentcore.BeforeToolCallFunc, func() bool) {
	if mode == shellguard.ModeOff || mode == "" {
		return nil, nil
	}
	terminated := false
	seam := func(ctx context.Context, call agentcore.AgentToolCall) *agentcore.BeforeToolCallDecision {
		if call.Name != bashToolName {
			return nil
		}
		d := shellguard.Analyze(bashCommand(call.Arguments))
		switch d.Verdict {
		case shellguard.Safe:
			return nil
		case shellguard.Incomplete, shellguard.Hazardous:
		}
		denialMode := "ask"
		if mode == shellguard.ModeStrict {
			denialMode = "strict"
		}
		if allowContinue {
			return &agentcore.BeforeToolCallDecision{
				Block:   true,
				Content: &agentcore.ContentList{agentcore.NewTextContent(blockMessage(denialMode, d, call))},
			}
		}
		terminated = true
		if cancel != nil {
			cancel()
		}
		return &agentcore.BeforeToolCallDecision{
			Block:   true,
			Content: &agentcore.ContentList{agentcore.NewTextContent(blockMessage(denialMode, d, call))},
		}
	}
	return seam, func() bool { return terminated }
}

// ChainBeforeToolCall composes two BeforeToolCall seams: prev runs first and
// a blocking decision wins; otherwise next decides. A nil operand is
// identity. (This is the exported form of the hook chain's composer; the
// shellguard seam must be chainable from drivers that already occupy the
// seam with hooks or trust confirmation.)
func ChainBeforeToolCall(prev, next agentcore.BeforeToolCallFunc) agentcore.BeforeToolCallFunc {
	if prev == nil {
		return next
	}
	if next == nil {
		return prev
	}
	return func(ctx context.Context, call agentcore.AgentToolCall) *agentcore.BeforeToolCallDecision {
		if dec := prev(ctx, call); dec != nil && dec.Block {
			return dec
		}
		return next(ctx, call)
	}
}

// bashCommand extracts the bash tool's "command" argument. BeforeToolCall
// runs after schema validation, so the field is normally present and a
// string; a malformed payload degrades to "" (which Analyze reports Safe —
// the tool executor will reject the malformed call on its own).
func bashCommand(args json.RawMessage) string {
	var parsed struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(args, &parsed); err != nil {
		return ""
	}
	return parsed.Command
}

// blockMessage renders the failed tool result text for a denied command.
// Findings carry fixed descriptions only (no command text) by design; the
// command preview at the end is the model's own input echoed back.
func blockMessage(mode string, d shellguard.Decision, call agentcore.AgentToolCall) string {
	msg := fmt.Sprintf("pigo shellguard denied this bash command (mode: %s).\nverdict: %s", mode, d.Verdict)
	if d.Reason != "" {
		msg += fmt.Sprintf("\nreason: %s (the command could not be fully analyzed and is never auto-allowed)", d.Reason)
	}
	for _, f := range d.Findings {
		msg += fmt.Sprintf("\nfinding: %s — %s", f.Token, f.Description)
	}
	msg += fmt.Sprintf("\ncommand: %s", preview(call.Arguments))
	if d.Verdict == shellguard.Incomplete {
		msg += "\nre-run with a simpler, fully literal command, or ask the user to adjust the shellguard mode."
	} else {
		msg += "\nchoose a different approach that avoids this command."
	}
	return msg
}

// preview renders a short echo of the raw arguments for the denial message.
func preview(args json.RawMessage) string {
	const max = 200
	s := string(args)
	r := []rune(s)
	if len(r) > max {
		return string(r[:max]) + " …"
	}
	return s
}
