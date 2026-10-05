// This file walks one CallExpr: argument expansion analysis, then the
// wrapper-stripping loop, then the head rules. The stripping loop is the
// heart of the analysis — `env MODE=test timeout -s TERM 5 nice -n 0 setsid
// -w stdbuf -oL rm -rf ./build` must surface the inner rm.
package shellguard

import (
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// call analyzes one CallExpr statement.
func (a *analyzer) call(s *syntax.Stmt, ce *syntax.CallExpr, heredocCode bool) {
	a.calls++
	if a.calls > maxCommands {
		a.inc(RAnalysisLimit)
		return
	}
	// Assignment values execute their expansions even though assignments
	// themselves are inert (`FOO=$(cmd)`).
	for _, as := range ce.Assigns {
		a.assign(as)
	}
	words := make([]wordVal, 0, len(ce.Args))
	for _, w := range ce.Args {
		words = append(words, a.word(w))
	}
	a.commandWords(words, heredocCode)
}

// commandWords runs the wrapper-stripping loop over a word list and applies
// the head rules to whatever command remains. It is re-entered for find
// -exec/-ok segments (step re-queues their contents as commands).
func (a *analyzer) commandWords(words []wordVal, heredocCode bool) {
	for depth := 0; depth < maxWrapperDepth; depth++ {
		if len(words) == 0 {
			return // nothing left to run (assignments, flags only)
		}
		head := words[0]
		if head.dynamic {
			a.inc(RDynamicCommand)
			return
		}
		norm := normHead(head.lit)

		// Shell interpreters and command-wrapper tables have dedicated
		// strippers; anything else falls through to the dangerous rules.
		if isInterpreterName(norm) {
			a.stripInterpreter(words, heredocCode)
			return
		}
		if shellLikeNames[norm] {
			// PowerShell gets dedicated flag handling; other shell dialects
			// (csh/tcsh/fish/cmd) cannot be analyzed at all — fail closed.
			if norm == "pwsh" || norm == "powershell" {
				a.stripPowerShell(words)
			} else {
				a.inc(RDynamicScript)
			}
			return
		}
		if strip, ok := wrapperTable[norm]; ok {
			rest, done, err := strip(words)
			if err != nil {
				a.inc(err.reason)
				return
			}
			if done {
				return // display/query form: nothing executes
			}
			words = rest
			continue
		}

		a.headRules(norm, words, heredocCode)
		return
	}
	a.inc(RWrapperDepth)
}

// headRules applies the dangerous-command tables to the resolved command.
// The word list still contains the head at index 0.
func (a *analyzer) headRules(norm string, words []wordVal, heredocCode bool) {
	args := words[1:]

	switch {
	case dangerousWords[norm]:
		a.haz(FTokenDangerous, "command matches the dangerous command word list (rm/chmod/chown/chgrp/chattr/pkill/kill/killall)")
	case norm == "mkfs" || (len(norm) > 5 && norm[:5] == "mkfs."):
		a.haz(FTokenDangerous, "command formats a filesystem (mkfs)")
	case norm == "dd" && anyArgHasPrefix(args, "if=", "of="):
		a.haz(FTokenDangerous, "command converts/copies raw data (dd)")
	case lifecycleHeads[norm]:
		a.haz(FTokenLifecycle, "command changes system power state (reboot/shutdown/poweroff/halt)")
	case lifecycleControllers[norm] && hasLifecycleVerb(args):
		a.haz(FTokenLifecycle, "command changes system power state via a lifecycle controller")
	case norm == "git":
		a.gitRules(words[0], args)
	case norm == "find":
		a.findRules(args)
	case norm == "tee":
		// tee is not a safe write sink (grok's CWE-862 note): it writes its
		// arguments to files just like a redirect.
		a.haz(FTokenRedirectWrite, "command writes its input into a file (tee)")
	case norm == "eval":
		a.inc(RDynamicScript)
	case norm == "source" || norm == ".":
		a.inc(RDynamicScript)
	case norm == "busybox":
		// busybox is a multi-call binary; its first argument selects the
		// applet, so strip and re-enter the rules.
		if len(args) > 0 && !args[0].dynamic {
			a.commandWords(args, heredocCode)
		} else {
			a.inc(RDynamicCommand)
		}
	}
}

// gitRules implements the git single source of truth (grok exec_risk.rs).
// The head must be the literal lowercase "git": path-qualified or
// case-variant spellings (Git, /usr/bin/git, git.exe) could be a different
// binary — grok fails closed on them for the read-only path, so does pigo.
func (a *analyzer) gitRules(head wordVal, args []wordVal) {
	if head.lit != "git" {
		a.inc(RGitGlobalOptions)
		return
	}
	i := 0
	// Global options before the verb. Benign: -C <dir> (also attached
	// -Cpath), --no-pager, -P. Everything else (including -c config,
	// --exec-path, --git-dir, --work-tree, and bare -p) can execute a
	// program or redirect the repository: fail closed.
	for i < len(args) {
		w := args[i]
		if w.dynamic {
			a.inc(RGitGlobalOptions)
			return
		}
		lit := w.lit
		if !strings.HasPrefix(lit, "-") || lit == "-" {
			break
		}
		switch {
		case lit == "-C" || strings.HasPrefix(lit, "-C") && len(lit) > 2:
			if lit == "-C" {
				i++
				if i >= len(args) {
					a.inc(RGitGlobalOptions)
					return
				}
				if args[i].dynamic {
					a.inc(RGitGlobalOptions)
					return
				}
			}
		case lit == "--no-pager" || lit == "-P":
			// benign
		default:
			a.inc(RGitGlobalOptions)
			return
		}
		i++
	}
	if i >= len(args) {
		return // bare "git" (prints usage)
	}
	verb := args[i]
	if verb.dynamic {
		a.inc(RDynamicCommand)
		return
	}
	rest := args[i+1:]
	switch {
	case verb.lit == "push":
		a.haz(FTokenDangerous, "git push publishes and overwrites remote history")
	case verb.lit == "reset" && anyArgEquals(rest, "--hard"):
		a.haz(FTokenDestructiveGit, "git reset --hard discards working tree changes")
	case verb.lit == "clean" && gitCleanForce(rest):
		a.haz(FTokenDestructiveGit, "git clean -f deletes untracked files")
	case safeGitSubcommands[verb.lit]:
		if gitUnsafeQueryOption(rest) {
			a.inc(RGitUnsafeQueryOption)
		}
	}
	// Other verbs: mutating-but-ordinary git operations pass (registered
	// deviation; grok floors them to a classifier prompt pigo v1 lacks).
}

// findRules re-queues find -exec/-ok/-execdir/-okdir command segments as
// commands (step's find rule: their contents execute verbatim).
func (a *analyzer) findRules(args []wordVal) {
	for i := 0; i < len(args); i++ {
		w := args[i]
		if w.dynamic || !execVerbs[w.lit] {
			continue
		}
		seg := make([]wordVal, 0, 8)
		j := i + 1
		for ; j < len(args); j++ {
			nw := args[j]
			if !nw.dynamic && (nw.lit == ";" || nw.lit == "+") {
				break
			}
			if !nw.dynamic && nw.lit == "{}" {
				continue // placeholder for the streamed filename
			}
			seg = append(seg, nw)
		}
		a.commandWords(seg, false)
		i = j
	}
}

// gitCleanForce reports whether a git clean argument list requests deletion
// (-f in any short cluster, or --force). -n/--dry-run without f is safe.
func gitCleanForce(args []wordVal) bool {
	for _, w := range args {
		if w.dynamic {
			continue
		}
		lit := w.lit
		if strings.HasPrefix(lit, "--") {
			if lit == "--force" {
				return true
			}
			continue
		}
		if strings.HasPrefix(lit, "-") && lit != "-" && containsRune(lit, 'f') {
			return true
		}
	}
	return false
}

// gitUnsafeQueryOption reports whether a read-only git subcommand's options
// can execute a program or write a file (grok's GIT_QUERY_UNSAFE_OPTIONS,
// with abbreviation fail-closed: --filt and --op are blocked too).
func gitUnsafeQueryOption(args []wordVal) bool {
	unsafe := []string{
		"--filters", "--textconv", "--output", "--ext-diff", "--open-files-in-pager",
	}
	for _, w := range args {
		if w.dynamic {
			return true // value unknown, could be any option
		}
		lit := w.lit
		if !strings.HasPrefix(lit, "-") {
			continue
		}
		if strings.HasPrefix(lit, "-O") && len(lit) > 1 {
			return true // git grep -O<cmd> pipes matches into a program
		}
		tok := lit
		if eq := strings.IndexByte(tok, '='); eq >= 0 {
			tok = tok[:eq]
		}
		for _, u := range unsafe {
			if tok == u || (len(tok) >= 2 && strings.HasPrefix(u, tok)) {
				return true
			}
		}
	}
	return false
}

// dangerousWords is the hard dangerous command word list (grok grants.rs
// is_dangerous_command_words). Word-boundary matching is structural here:
// heads are whole normalized words, so `truncate`/`rmdir` never match `rm`.
// These verdicts must not be waivable by allow-lists.
//
// R11 note: grok flags ANY rm/chmod/kill invocation; step only flags
// rm -rf (recursive+force). The stricter grok list is adopted (unattended
// safety first); the divergence is registered in the spec.
var dangerousWords = map[string]bool{
	"rm":      true,
	"chmod":   true,
	"chown":   true,
	"chgrp":   true,
	"chattr":  true,
	"pkill":   true,
	"kill":    true,
	"killall": true,
}

// lifecycleHeads are direct system power-state commands (step's
// system-lifecycle rule).
var lifecycleHeads = map[string]bool{
	"reboot":   true,
	"shutdown": true,
	"poweroff": true,
	"halt":     true,
}

// lifecycleControllers change power state through a sub-verb.
var lifecycleControllers = map[string]bool{
	"systemctl": true,
	"loginctl":  true,
	"telinit":   true,
	"init":      true,
}

// lifecycleVerbs are the sub-verbs that matter on lifecycle controllers.
var lifecycleVerbs = map[string]bool{
	"reboot":    true,
	"shutdown":  true,
	"poweroff":  true,
	"halt":      true,
	"suspend":   true,
	"hibernate": true,
}

// execVerbs are the find flags whose remainder is a command.
var execVerbs = map[string]bool{
	"-exec":    true,
	"-ok":      true,
	"-execdir": true,
	"-okdir":   true,
}

// safeGitSubcommands is grok's SAFE_GIT_SUBCOMMANDS single source of truth
// (exec_risk.rs:234-256): the read-only query verbs.
var safeGitSubcommands = map[string]bool{
	"status":       true,
	"branch":       true,
	"log":          true,
	"diff":         true,
	"ls-files":     true,
	"show":         true,
	"rev-parse":    true,
	"blame":        true,
	"grep":         true,
	"describe":     true,
	"merge-base":   true,
	"check-ignore": true,
	"check-attr":   true,
	"cat-file":     true,
	"ls-tree":      true,
	"show-ref":     true,
	"for-each-ref": true,
	"rev-list":     true,
	"name-rev":     true,
	"shortlog":     true,
	"count-objects": true,
}

func anyArgEquals(args []wordVal, want string) bool {
	for _, w := range args {
		if !w.dynamic && w.lit == want {
			return true
		}
	}
	return false
}

func anyArgHasPrefix(args []wordVal, prefixes ...string) bool {
	for _, w := range args {
		if w.dynamic {
			continue
		}
		for _, p := range prefixes {
			if strings.HasPrefix(w.lit, p) {
				return true
			}
		}
	}
	return false
}

func hasLifecycleVerb(args []wordVal) bool {
	for _, w := range args {
		if w.dynamic {
			continue
		}
		if strings.HasPrefix(w.lit, "-") {
			continue
		}
		return lifecycleVerbs[w.lit]
	}
	return false
}
