// This file implements wrapper and interpreter stripping, translated from
// Step-Code's COMMAND_WRAPPERS/WRAPPER_FLAGS tables (command-policy.ts) and
// grok's bash_command_splitting.rs, keeping each source's arity discipline:
// value-taking options consume exactly one literal word (or an attached
// value), unknown options never guess — they fail closed (Incomplete).
package shellguard

import (
	"strings"
)

// wrapError carries the fail-closed reason for an unstrippable wrapper.
type wrapError struct{ reason string }

// stripFunc consumes one wrapper invocation from the front of the word list
// (head at index 0) and returns the remaining words. done=true means the
// call is a display/query form that executes nothing (command -v).
type stripFunc func(words []wordVal) (rest []wordVal, done bool, err *wrapError)

// wrapperTable maps normalized command heads to their strippers (step's
// wrapper set). busybox is handled in headRules (its applet is a head, not a
// flag consumer).
var wrapperTable = map[string]stripFunc{
	"timeout": stripTimeout,
	"nice":    stripNice,
	"ionice":  stripIonice,
	"chrt":    stripChrt,
	"stdbuf":  stripStdbuf,
	"env":     stripEnv,
	"sudo":    stripSudo,
	"doas":    stripDoas,
	"xargs":   stripXargs,
	"nohup":   stripNohup,
	"setsid":  stripSetsid,
	"exec":    stripExec,
	"command": stripCommand,
	"builtin": stripBuiltin,
}

// interpreterNames are POSIX-family shells whose -c string is script code
// (step models interpreters; grok resolves `bash -ec -- '...'` to the inner
// command). Non-POSIX shells (csh/tcsh/fish) and PowerShell have dedicated
// fail-closed handling below.
var interpreterNames = map[string]bool{
	"sh": true, "bash": true, "dash": true, "zsh": true, "ksh": true,
	"ash": true, "mksh": true, "lksh": true, "rbash": true,
}

// isInterpreterName reports whether the normalized head is a POSIX-family
// shell interpreter.
func isInterpreterName(norm string) bool { return interpreterNames[norm] }

// isShellLikeName covers every other program that executes a script body
// (csh/tcsh/fish dialects pigo cannot analyze, plus the Windows family).
var shellLikeNames = map[string]bool{
	"csh": true, "tcsh": true, "fish": true, "pwsh": true, "powershell": true,
	"cmd": true,
}

// interpreterBoolChars are bash/POSIX sh option chars that take no value
// (bash(1) OPTIONS). Anything outside this set (except c/o/O) fails closed.
const interpreterBoolChars = "abefDhiklnrstuvxBCPHTE"

// stripInterpreter consumes a shell interpreter invocation. With a literal
// -c string the code is re-analyzed as a script; the words after it are
// positional parameters (data, not code — step's `sh -c 'printf %s' label
// 'rm -rf'` test). Without -c the script comes from a file or stdin, which
// cannot be resolved statically — fail closed — unless the heredoc rule
// already covers the body (heredocCode).
//
// A literal `--` between -c and its argument is skipped (grok resolves
// `bash -ec -- 'rm -rf x'` to the inner command).
func (a *analyzer) stripInterpreter(words []wordVal, heredocCode bool) {
	i := 1
	for i < len(words) {
		w := words[i]
		if w.dynamic {
			a.inc(RDynamicWrapper)
			return
		}
		lit := w.lit
		switch {
		case lit == "--":
			i++
		case lit == "-":
			// "-" reads the script from stdin.
			if !heredocCode {
				a.inc(RDynamicScript)
			}
			return
		case strings.HasPrefix(lit, "--"):
			name, _, hasVal := splitLong(lit)
			switch name {
			case "--rcfile":
				if !hasVal {
					i++
					if i >= len(words) || words[i].dynamic {
						a.inc(RInterpreterOptions)
						return
					}
				}
			case "--login", "--norc", "--noprofile", "--restricted",
				"--verbose", "--posix", "--noediting", "--debugger":
			default:
				a.inc(RInterpreterOptions)
				return
			}
			i++
		case len(lit) > 1 && lit[0] == '-':
			chars := []rune(lit[1:])
			for j := 0; j < len(chars); j++ {
				switch chars[j] {
				case 'c':
					rem := string(chars[j+1:])
					var code wordVal
					if rem != "" {
						code = wordVal{lit: unescapeLit(rem)}
					} else {
						i++
						if i >= len(words) {
							a.inc(RInterpreterOptions)
							return
						}
						code = words[i]
						if !code.dynamic && code.lit == "--" && i+1 < len(words) {
							// `bash -ec -- 'script'`: grok resolves the
							// inner command past the end-of-options marker.
							i++
							code = words[i]
						}
					}
					if code.dynamic {
						a.inc(RDynamicScript)
						return
					}
					a.runInterpreterCode(code.lit)
					return
				case 'o', 'O':
					rem := string(chars[j+1:])
					if rem == "" {
						i++
						if i >= len(words) || words[i].dynamic {
							a.inc(RInterpreterOptions)
							return
						}
					}
					j = len(chars)
				default:
					if !containsRune(interpreterBoolChars, chars[j]) {
						a.inc(RInterpreterOptions)
						return
					}
				}
			}
			i++
		default:
			// First operand without -c: a script file whose contents cannot
			// be resolved statically.
			a.inc(RDynamicScript)
			return
		}
	}
	// Bare interpreter (no -c, no operand): the script is stdin.
	if !heredocCode {
		a.inc(RDynamicScript)
	}
}

// runInterpreterCode analyzes a literal interpreter code string.
func (a *analyzer) runInterpreterCode(code string) {
	if !a.scriptBudget() {
		return
	}
	defer func() { a.depth-- }()
	a.scriptText(code)
}

// stripPowerShell handles the Windows shell family (pwsh/powershell/cmd).
// PowerShell flags are case-insensitive; -Command consumes every remaining
// word as one command line (step's pwsh test), -File/-EncodedCommand cannot
// be resolved statically, and cmd.exe is never analyzed.
func (a *analyzer) stripPowerShell(words []wordVal) {
	i := 1
	for i < len(words) {
		w := words[i]
		if w.dynamic {
			a.inc(RDynamicScript)
			return
		}
		lit := w.lit
		low := strings.ToLower(lit)
		switch {
		case low == "-command" || low == "-c":
			if i+1 >= len(words) {
				a.inc(RInterpreterOptions)
				return
			}
			var parts []string
			for _, rest := range words[i+1:] {
				if rest.dynamic {
					a.inc(RDynamicScript)
					return
				}
				parts = append(parts, rest.lit)
			}
			a.runInterpreterCode(strings.Join(parts, " "))
			return
		case low == "-file" || low == "-encodedcommand":
			a.inc(RDynamicScript)
			return
		case low == "-executionpolicy" || low == "-inputformat" ||
			low == "-outputformat" || low == "-windowstyle" || low == "-version":
			i++
			if i >= len(words) || words[i].dynamic {
				a.inc(RInterpreterOptions)
				return
			}
		case low == "-noprofile" || low == "-nologo" || low == "-noninteractive" ||
			low == "-noexit" || low == "-sta" || low == "-mta" || low == "-login":
		default:
			a.inc(RInterpreterOptions)
			return
		}
		i++
	}
	a.inc(RDynamicScript)
}

// stripTimeout consumes `timeout [OPTION] DURATION COMMAND` with mandatory
// DURATION (grok's arity discipline: missing/dynamic duration fails closed).
// A `--` before the duration ends the options; after the duration it is left
// in place (step's `timeout 5 -- rm` test stays unflagged).
func stripTimeout(words []wordVal) ([]wordVal, bool, *wrapError) {
	i := 1
	for i < len(words) {
		w := words[i]
		if w.dynamic {
			return nil, false, &wrapError{RDynamicWrapper}
		}
		lit := w.lit
		if lit == "--" {
			i++
			break
		}
		if strings.HasPrefix(lit, "--") {
			name, _, hasVal := splitLong(lit)
			switch name {
			case "--kill-after", "--signal":
				if !hasVal {
					i++
					if i >= len(words) || words[i].dynamic {
						return nil, false, &wrapError{RDynamicWrapper}
					}
				}
			case "--preserve-status", "--foreground", "--verbose":
			default:
				return nil, false, &wrapError{RWrapperOptions}
			}
			i++
			continue
		}
		if len(lit) > 1 && lit[0] == '-' {
			chars := lit[1:]
			for j := 0; j < len(chars); j++ {
				switch chars[j] {
				case 'k', 's':
					if j+1 >= len(chars) {
						i++
						if i >= len(words) || words[i].dynamic {
							return nil, false, &wrapError{RDynamicWrapper}
						}
					}
					j = len(chars)
				case 'v':
				default:
					return nil, false, &wrapError{RWrapperOptions}
				}
			}
			i++
			continue
		}
		break
	}
	if i >= len(words) || words[i].dynamic {
		return nil, false, &wrapError{RDynamicWrapper}
	}
	i++ // the duration
	if i >= len(words) {
		return nil, false, &wrapError{RDynamicWrapper}
	}
	return words[i:], false, nil
}

// stripNice consumes `nice [-n ADJ|--adjustment=ADJ] COMMAND` (step: bare
// numeric short flags like `nice -5` are unmodeled → fail closed).
func stripNice(words []wordVal) ([]wordVal, bool, *wrapError) {
	i := 1
	for i < len(words) {
		w := words[i]
		if w.dynamic {
			return nil, false, &wrapError{RDynamicWrapper}
		}
		lit := w.lit
		if lit == "--" {
			i++
			break
		}
		if strings.HasPrefix(lit, "--") {
			name, _, hasVal := splitLong(lit)
			if name != "--adjustment" {
				return nil, false, &wrapError{RWrapperOptions}
			}
			if !hasVal {
				i++
				if i >= len(words) || words[i].dynamic {
					return nil, false, &wrapError{RDynamicWrapper}
				}
			}
			i++
			continue
		}
		if len(lit) > 1 && lit[0] == '-' {
			if lit[1] != 'n' {
				return nil, false, &wrapError{RWrapperOptions}
			}
			if len(lit) == 2 {
				i++
				if i >= len(words) || words[i].dynamic {
					return nil, false, &wrapError{RDynamicWrapper}
				}
			}
			i++
			continue
		}
		break
	}
	return words[i:], false, nil
}

// stripIonice consumes `ionice [-c CLASS] [-n LEVEL] [-t] COMMAND`.
func stripIonice(words []wordVal) ([]wordVal, bool, *wrapError) {
	i := 1
	for i < len(words) {
		w := words[i]
		if w.dynamic {
			return nil, false, &wrapError{RDynamicWrapper}
		}
		lit := w.lit
		if lit == "--" {
			i++
			break
		}
		if lit[0] != '-' || len(lit) == 1 {
			break
		}
		if strings.HasPrefix(lit, "--") {
			return nil, false, &wrapError{RWrapperOptions}
		}
		chars := lit[1:]
		for j := 0; j < len(chars); j++ {
			switch chars[j] {
			case 'n', 'c':
				if j+1 >= len(chars) {
					i++
					if i >= len(words) || words[i].dynamic {
						return nil, false, &wrapError{RDynamicWrapper}
					}
				}
				j = len(chars)
			case 't':
			default:
				return nil, false, &wrapError{RWrapperOptions}
			}
		}
		i++
	}
	return words[i:], false, nil
}

// stripChrt consumes `chrt [OPTIONS] PRIORITY COMMAND` with mandatory
// PRIORITY (grok's arity discipline).
func stripChrt(words []wordVal) ([]wordVal, bool, *wrapError) {
	i := 1
	for i < len(words) {
		w := words[i]
		if w.dynamic {
			return nil, false, &wrapError{RDynamicWrapper}
		}
		lit := w.lit
		if lit == "--" {
			i++
			break
		}
		if lit[0] != '-' || len(lit) == 1 {
			break
		}
		if strings.HasPrefix(lit, "--") {
			return nil, false, &wrapError{RWrapperOptions}
		}
		chars := lit[1:]
		for j := 0; j < len(chars); j++ {
			switch chars[j] {
			case 'p':
				if j+1 >= len(chars) {
					i++
					if i >= len(words) || words[i].dynamic {
						return nil, false, &wrapError{RDynamicWrapper}
					}
				}
				j = len(chars)
			case 'a', 'b', 'm', 'R', 'v', 'f':
			default:
				return nil, false, &wrapError{RWrapperOptions}
			}
		}
		i++
	}
	if i >= len(words) || words[i].dynamic {
		return nil, false, &wrapError{RDynamicWrapper}
	}
	i++ // the priority
	if i >= len(words) {
		return nil, false, &wrapError{RDynamicWrapper}
	}
	return words[i:], false, nil
}

// stripStdbuf consumes `stdbuf [-iMODE] [-oMODE] [-eMODE] COMMAND` (values
// attach to the flag: `-oL`, `--output=L`).
func stripStdbuf(words []wordVal) ([]wordVal, bool, *wrapError) {
	i := 1
	for i < len(words) {
		w := words[i]
		if w.dynamic {
			return nil, false, &wrapError{RDynamicWrapper}
		}
		lit := w.lit
		if lit == "--" {
			i++
			break
		}
		if strings.HasPrefix(lit, "--") {
			name, _, hasVal := splitLong(lit)
			switch name {
			case "--input", "--output", "--error":
				if !hasVal {
					i++
					if i >= len(words) || words[i].dynamic {
						return nil, false, &wrapError{RDynamicWrapper}
					}
				}
			default:
				return nil, false, &wrapError{RWrapperOptions}
			}
			i++
			continue
		}
		if len(lit) > 1 && lit[0] == '-' {
			switch lit[1] {
			case 'i', 'o', 'e':
				if len(lit) == 2 {
					i++
					if i >= len(words) || words[i].dynamic {
						return nil, false, &wrapError{RDynamicWrapper}
					}
				}
			default:
				return nil, false, &wrapError{RWrapperOptions}
			}
			i++
			continue
		}
		break
	}
	return words[i:], false, nil
}

// stripEnv consumes env options, NAME=VALUE assignments, and `--` (grok's
// env_scan discipline: -S/--split-string is never stripped, unknown options
// fail closed).
func stripEnv(words []wordVal) ([]wordVal, bool, *wrapError) {
	i := 1
	for i < len(words) {
		w := words[i]
		if w.dynamic {
			return nil, false, &wrapError{RDynamicWrapper}
		}
		lit := w.lit
		if lit == "--" {
			i++
			break
		}
		if !strings.HasPrefix(lit, "-") {
			if validAssign(lit) {
				i++
				continue
			}
			break
		}
		if lit == "-S" || lit == "--split-string" || strings.HasPrefix(lit, "--split-string=") {
			return nil, false, &wrapError{RWrapperSplitString}
		}
		if strings.HasPrefix(lit, "--") {
			name, _, hasVal := splitLong(lit)
			switch name {
			case "--unset", "--chdir":
				if !hasVal {
					i++
					if i >= len(words) || words[i].dynamic {
						return nil, false, &wrapError{RDynamicWrapper}
					}
				}
			case "--ignore-environment", "--null", "--default-signal", "--ignore-signal":
				// --default-signal/--ignore-signal take optional names; bare
				// form is modeled, the attached form fails closed below.
				if hasVal && name == "--ignore-signal" {
					return nil, false, &wrapError{RWrapperOptions}
				}
			default:
				return nil, false, &wrapError{RWrapperOptions}
			}
			i++
			continue
		}
		chars := lit[1:]
		for j := 0; j < len(chars); j++ {
			switch chars[j] {
			case 'u', 'C':
				if j+1 >= len(chars) {
					i++
					if i >= len(words) || words[i].dynamic {
						return nil, false, &wrapError{RDynamicWrapper}
					}
				}
				j = len(chars)
			case 'i', 'v', '0':
			default:
				return nil, false, &wrapError{RWrapperOptions}
			}
		}
		i++
	}
	return words[i:], false, nil
}

// validAssign reports whether lit is a NAME=VALUE environment assignment
// (also NAME+=VALUE).
func validAssign(lit string) bool {
	eq := strings.IndexByte(lit, '=')
	if eq <= 0 {
		return false
	}
	name := lit[:eq]
	name = strings.TrimSuffix(name, "+")
	if name == "" {
		return false
	}
	for i, r := range name {
		ok := r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(i > 0 && r >= '0' && r <= '9')
		if !ok {
			return false
		}
	}
	return true
}

// stripSudo consumes sudo/doas-style privilege wrappers (step's table: value
// flags may attach, `sudo -un root rm` leaves `root` as the command).
func stripSudo(words []wordVal) ([]wordVal, bool, *wrapError) {
	return stripPrivilege(words, "bnElv", "ugpCRTB",
		map[string]bool{"--login": true, "--non-interactive": true, "--reset-env": true, "--shell": true},
		map[string]bool{"--user": true, "--group": true, "--preserve-env": true, "--other-user": true},
	)
}

func stripDoas(words []wordVal) ([]wordVal, bool, *wrapError) {
	return stripPrivilege(words, "nL", "u",
		map[string]bool{}, map[string]bool{},
	)
}

func stripPrivilege(words []wordVal, boolShort, valShort string, boolLong, valLong map[string]bool) ([]wordVal, bool, *wrapError) {
	i := 1
	for i < len(words) {
		w := words[i]
		if w.dynamic {
			return nil, false, &wrapError{RDynamicWrapper}
		}
		lit := w.lit
		if lit == "--" {
			i++
			break
		}
		if strings.HasPrefix(lit, "--") {
			name, _, hasVal := splitLong(lit)
			switch {
			case boolLong[name]:
			case valLong[name]:
				if !hasVal {
					i++
					if i >= len(words) || words[i].dynamic {
						return nil, false, &wrapError{RDynamicWrapper}
					}
				}
			default:
				return nil, false, &wrapError{RWrapperOptions}
			}
			i++
			continue
		}
		if len(lit) > 1 && lit[0] == '-' {
			chars := []rune(lit[1:])
			for j := 0; j < len(chars); j++ {
				c := chars[j]
				switch {
				case containsRune(valShort, c):
					if j+1 >= len(chars) {
						i++
						if i >= len(words) || words[i].dynamic {
							return nil, false, &wrapError{RDynamicWrapper}
						}
					}
					j = len(chars)
				case containsRune(boolShort, c):
				default:
					return nil, false, &wrapError{RWrapperOptions}
				}
			}
			i++
			continue
		}
		break
	}
	return words[i:], false, nil
}

// stripXargs consumes xargs options and leaves its static command tail.
// xargs appends stdin items to that tail, so the tail's head is still the
// executed program (step: `printf %s ./build | xargs rm -rf` is matched).
func stripXargs(words []wordVal) ([]wordVal, bool, *wrapError) {
	i := 1
	for i < len(words) {
		w := words[i]
		if w.dynamic {
			// xargs' own options are dynamic → cannot verify the tail.
			return nil, false, &wrapError{RDynamicWrapper}
		}
		lit := w.lit
		if lit == "--" {
			i++
			break
		}
		if strings.HasPrefix(lit, "--") {
			name, _, hasVal := splitLong(lit)
			switch name {
			case "--arg-file", "--delimiter", "--eof-str", "--max-args",
				"--max-chars", "--max-procs", "--replace", "--responses-file":
				if !hasVal {
					i++
					if i >= len(words) || words[i].dynamic {
						return nil, false, &wrapError{RDynamicWrapper}
					}
				}
			case "--null", "--no-run-if-empty", "--verbose", "--interactive",
				"--exit", "--no-quote":
			default:
				return nil, false, &wrapError{RWrapperOptions}
			}
			i++
			continue
		}
		if len(lit) > 1 && lit[0] == '-' {
			chars := lit[1:]
			for j := 0; j < len(chars); j++ {
				switch chars[j] {
				case 'a', 'd', 'E', 'I', 'L', 'n', 'P', 's':
					if j+1 >= len(chars) {
						i++
						if i >= len(words) || words[i].dynamic {
							return nil, false, &wrapError{RDynamicWrapper}
						}
					}
					j = len(chars)
				case '0', 'r', 't', 'x', 'p':
				default:
					return nil, false, &wrapError{RWrapperOptions}
				}
			}
			i++
			continue
		}
		break
	}
	return words[i:], false, nil
}

// stripNohup consumes nohup (GNU nohup has only --help/--version, both
// display forms; anything flag-shaped fails closed).
func stripNohup(words []wordVal) ([]wordVal, bool, *wrapError) {
	i := 1
	if i < len(words) && !words[i].dynamic && (words[i].lit == "--help" || words[i].lit == "--version") {
		return nil, true, nil
	}
	if i < len(words) && !words[i].dynamic && words[i].lit == "--" {
		i++
	}
	return words[i:], false, nil
}

// stripSetsid consumes setsid boolean options (-c -f -w; step's
// `setsid -cfw reboot` and `setsid --wait=yes` tests).
func stripSetsid(words []wordVal) ([]wordVal, bool, *wrapError) {
	i := 1
	for i < len(words) {
		w := words[i]
		if w.dynamic {
			return nil, false, &wrapError{RDynamicWrapper}
		}
		lit := w.lit
		if lit == "--" {
			i++
			break
		}
		if strings.HasPrefix(lit, "--") {
			return nil, false, &wrapError{RWrapperOptions}
		}
		if len(lit) > 1 && lit[0] == '-' {
			for _, c := range lit[1:] {
				switch c {
				case 'c', 'f', 'w':
				default:
					return nil, false, &wrapError{RWrapperOptions}
				}
			}
			i++
			continue
		}
		break
	}
	return words[i:], false, nil
}

// stripExec consumes the transparent prefix `exec [-c] [-l] [-a NAME]`.
func stripExec(words []wordVal) ([]wordVal, bool, *wrapError) {
	i := 1
	for i < len(words) {
		w := words[i]
		if w.dynamic {
			return nil, false, &wrapError{RDynamicWrapper}
		}
		lit := w.lit
		if lit == "--" {
			i++
			break
		}
		if len(lit) > 1 && lit[0] == '-' {
			chars := lit[1:]
			for j := 0; j < len(chars); j++ {
				switch chars[j] {
				case 'a':
					if j+1 >= len(chars) {
						i++
						if i >= len(words) || words[i].dynamic {
							return nil, false, &wrapError{RDynamicWrapper}
						}
					}
					j = len(chars)
				case 'c', 'l':
				default:
					return nil, false, &wrapError{RWrapperOptions}
				}
			}
			i++
			continue
		}
		break
	}
	return words[i:], false, nil
}

// stripCommand consumes the transparent prefix `command`, honoring its
// display mode: `command -v/-V ...` is a query that executes nothing
// (step's `command -v rm -rf` test).
func stripCommand(words []wordVal) ([]wordVal, bool, *wrapError) {
	i := 1
	for i < len(words) {
		w := words[i]
		if w.dynamic {
			return nil, false, &wrapError{RDynamicWrapper}
		}
		lit := w.lit
		if lit == "--" {
			i++
			break
		}
		if len(lit) > 1 && lit[0] == '-' {
			for _, c := range lit[1:] {
				switch c {
				case 'v', 'V':
					return nil, true, nil // display mode
				case 'p':
				default:
					return nil, false, &wrapError{RWrapperOptions}
				}
			}
			i++
			continue
		}
		break
	}
	return words[i:], false, nil
}

// stripBuiltin consumes the transparent prefix `builtin`; any flag fails
// closed (grok: builtin with flags is Ambiguous).
func stripBuiltin(words []wordVal) ([]wordVal, bool, *wrapError) {
	i := 1
	if i < len(words) && !words[i].dynamic && words[i].lit == "--" {
		i++
	}
	if i < len(words) && !words[i].dynamic && len(words[i].lit) > 1 && words[i].lit[0] == '-' {
		return nil, false, &wrapError{RWrapperOptions}
	}
	return words[i:], false, nil
}

// splitLong splits "--name=value" into name/value.
func splitLong(lit string) (name, val string, hasVal bool) {
	if eq := strings.IndexByte(lit, '='); eq >= 0 {
		return lit[:eq], lit[eq+1:], true
	}
	return lit, "", false
}
