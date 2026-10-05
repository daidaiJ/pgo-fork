// Package shellguard performs static three-state safety analysis of shell
// command strings before the bash tool executes them. It is the built-in
// default defense line for command content (the --allowed-tools boundary only
// gates whether the bash tool exists at all; once admitted, "rm -rf /" and
// "ls" were previously indistinguishable).
//
// The analysis is AST-based (mvdan.cc/sh/v3/syntax) and returns one of three
// verdicts, never conflating them:
//
//   - Safe: no dangerous construct was found. This is a statement about the
//     rules this package knows, not a proof of harmlessness.
//   - Hazardous: at least one rule matched (finding tokens below). Hazardous
//     verdicts must not be waived by any allow-list.
//   - Incomplete: the command could not be fully analyzed (parse error,
//     dynamic values, unmodeled wrappers, budget exhaustion). Fail-closed:
//     an unanalyzed command must never be auto-allowed — "I could not parse
//     it" never impersonates "it is safe".
//
// Semantics are translated test-by-test from two reference implementations
// (see wiki/port/shell-command-analysis.md §6): the wrapper/interpreter
// stripping tables and the fail-closed decision order follow Step-Code's
// shell-analysis.ts/command-policy.ts; the dangerous-word list, the git
// read-only single source of truth and the redirect-write rule follow
// grok-build-proxy's grants.rs/exec_risk.rs. Divergences are registered in
// the spec's deviation log.
//
// Findings use static tokens plus fixed descriptions and never embed command
// or path text, so a finding can be shown to a model without becoming an
// injection surface (grok's finding-token pattern).
//
// The package is a pure leaf library: no IO, no pigo-internal imports, no
// global state — Analyze is safe for concurrent use.
package shellguard

import (
	"fmt"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// Verdict is the outcome of analyzing one command string.
type Verdict int

const (
	// Safe means no known dangerous rule matched. It does not mean harmless.
	Safe Verdict = iota
	// Hazardous means at least one dangerous rule matched. Allow-lists and
	// blanket trust must not waive it.
	Hazardous
	// Incomplete means the command could not be fully analyzed. Fail-closed:
	// treat like Hazardous for auto-allow purposes.
	Incomplete
)

// String renders the verdict for logs and tool-result text.
func (v Verdict) String() string {
	switch v {
	case Hazardous:
		return "hazardous"
	case Incomplete:
		return "incomplete"
	default:
		return "safe"
	}
}

// Finding is one matched rule. Token is a stable machine-readable identifier
// and Description is fixed prose that never contains command or path text
// from the analyzed input (anti-injection, classifier-ready shape).
type Finding struct {
	Token       string
	Description string
}

// Decision is the full analysis result. When Verdict is Incomplete, Reason
// carries the first fail-closed trigger token (stable, machine-readable);
// Findings may still carry matches found in other parts of the command.
type Decision struct {
	Verdict  Verdict
	Findings []Finding
	Reason   string
}

// Mode is the shellguard operating mode configured by the host.
type Mode string

const (
	// ModeOff disables analysis entirely (zero overhead; the default —
	// shellguard is an opt-in advanced feature, user decision 2026-10-05).
	ModeOff Mode = "off"
	// ModeAsk asks (or blocks where no interactive channel exists) on
	// Hazardous/Incomplete commands.
	ModeAsk Mode = "ask"
	// ModeStrict blocks Hazardous/Incomplete commands outright, no prompt.
	ModeStrict Mode = "strict"
)

// ParseMode maps a config string onto a Mode.
func ParseMode(s string) (Mode, error) {
	switch Mode(s) {
	case ModeOff:
		return ModeOff, nil
	case ModeAsk:
		return ModeAsk, nil
	case ModeStrict:
		return ModeStrict, nil
	default:
		return ModeOff, fmt.Errorf("unknown shellguard mode %q (want off|ask|strict)", s)
	}
}

// Analysis budgets. Exceeding any marks the decision Incomplete rather than
// truncating silently (fail-closed, step's analysis-limit semantics).
const (
	maxSourceLen    = 256 << 10 // input longer than this is not analyzed
	maxCommands     = 4096      // command invocations visited
	maxScriptDepth  = 12        // nested script re-analysis depth
	maxWrapperDepth = 8         // wrapper/transparent-prefix stripping rounds
)

// Finding tokens (all tokens produced by this package).
const (
	// FTokenDangerous matches the dangerous command word list: rm, chmod,
	// chown, chgrp, chattr, pkill, kill, killall, mkfs*, dd-if, and git push
	// (grok grants.rs is_dangerous_command_words + step command-policy).
	FTokenDangerous = "dangerous-command"
	// FTokenLifecycle matches system lifecycle commands: reboot, shutdown,
	// poweroff, halt, and the systemctl/loginctl/telinit/init variants.
	FTokenLifecycle = "system-lifecycle"
	// FTokenDestructiveGit matches destructive git operations short of push:
	// reset --hard and clean -f (step's destructive-git rule).
	FTokenDestructiveGit = "destructive-git"
	// FTokenRedirectWrite matches output redirection into a file that cannot
	// be verified as a safe sink (grok's redirect_write; only /dev/null is a
	// safe sink). Never waivable by allow-lists.
	FTokenRedirectWrite = "redirect-write"
	// FTokenTruncateDevice matches writes aimed at /dev/* device nodes
	// (step's truncate-device, covering ": > /dev/..." and dd of=/dev/...).
	FTokenTruncateDevice = "truncate-device"
)

// Incomplete reason tokens (stable, machine-readable; see Decision.Reason).
const (
	// RShellSyntax: the input did not parse as a shell script.
	RShellSyntax = "shell-syntax"
	// RAnalysisLimit: a budget was exhausted (source size, command count,
	// script nesting depth).
	RAnalysisLimit = "analysis-limit"
	// RDynamicCommand: the command word is not a literal, so the executed
	// program cannot be known statically.
	RDynamicCommand = "dynamic-command"
	// RDynamicWrapper: a wrapper's mandatory argument (duration, priority) is
	// missing or not a literal.
	RDynamicWrapper = "dynamic-wrapper"
	// RWrapperOptions: an unknown or unmodeled wrapper option was seen.
	RWrapperOptions = "wrapper-options"
	// RWrapperSplitString: env -S / --split-string (word splitting is not
	// modeled; grok never strips it either).
	RWrapperSplitString = "wrapper-split-string"
	// RWrapperDepth: the wrapper stripping budget was exhausted.
	RWrapperDepth = "wrapper-depth"
	// RDynamicScript: a script body could not be statically resolved
	// (interpreters without a literal -c string, eval, sourcing a file,
	// dynamic -c arguments).
	RDynamicScript = "dynamic-script"
	// RInterpreterOptions: an unknown interpreter option was seen.
	RInterpreterOptions = "interpreter-options"
	// RUnsupportedConstruct: a shell construct outside the modeled subset
	// (function definitions, exotic clauses).
	RUnsupportedConstruct = "unsupported-construct"
	// RDynamicAssign: a variable declaration with non-literal contents.
	RDynamicAssign = "dynamic-assign"
	// RGitGlobalOptions: git global options that can execute arbitrary
	// programs or redirect repositories (grok's exec-risk global flags).
	RGitGlobalOptions = "git-global-options"
	// RGitUnsafeQueryOption: a query option on a read-only git subcommand
	// that can execute programs or write files (grok's unsafe query flags).
	RGitUnsafeQueryOption = "git-unsafe-query-option"
	// RDynamicOptions: a dynamically-valued argument in a re-analyzed
	// sub-command position (step's dynamic-options, e.g. find -exec).
	RDynamicOptions = "dynamic-options"
)

// Analyze runs the full analysis over one command string. It never panics on
// any input; anything it cannot confidently analyze comes back as Incomplete.
func Analyze(cmd string) Decision {
	if len(cmd) > maxSourceLen {
		return Decision{Verdict: Incomplete, Reason: RAnalysisLimit}
	}
	if strings.TrimSpace(cmd) == "" {
		return Decision{Verdict: Safe}
	}
	file, err := syntax.NewParser().Parse(strings.NewReader(cmd), "")
	if err != nil {
		return Decision{Verdict: Incomplete, Reason: RShellSyntax}
	}
	a := &analyzer{src: cmd}
	a.stmtList(file.Stmts)
	return a.decision()
}

// decision folds the accumulated findings and fail-closed reasons into a
// Decision. Incomplete outranks Hazardous outranks Safe: a partially
// unanalyzable command must not present as merely hazardous-or-safe.
func (a *analyzer) decision() Decision {
	var d Decision
	switch {
	case len(a.reasons) > 0:
		d.Verdict = Incomplete
		d.Reason = a.reasons[0]
	case len(a.findings) > 0:
		d.Verdict = Hazardous
	default:
		d.Verdict = Safe
	}
	d.Findings = append(d.Findings, a.findings...)
	return d
}
