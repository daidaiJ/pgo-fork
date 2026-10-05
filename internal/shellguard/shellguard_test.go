// Translated test corpus. Each table entry carries its provenance in the
// comment: "step" = Step-Code command-policy/shell-analysis tests (semantic
// translation), "grok" = grok-build-proxy grants/exec_risk tests, "pigo" =
// pigo-specific cases. Where the two sources disagree, the merged rule (grok
// dangerous words + step wrapper tables) decides and the divergence is
// registered in wiki/port/shell-command-analysis.md §7.
package shellguard

import (
	"strings"
	"testing"
)

func TestAnalyzeHazardous(t *testing.T) {
	tests := []struct{ cmd, token string }{
		// Dangerous word list (grok grants.rs; step's rm subset is superseded
		// by the stricter any-rm rule — registered divergence).
		{"rm -rf ./build", FTokenDangerous},
		{"rm -fr /tmp/cache", FTokenDangerous},
		{"rm -r -f project", FTokenDangerous},
		{"rm --recursive --force project", FTokenDangerous},
		{"rm ./build -rf", FTokenDangerous},
		{"rm -r 2>/tmp/output -f ./build", FTokenDangerous},
		{"/bin/rm -rf ./build", FTokenDangerous},
		{"rm -r ./build", FTokenDangerous}, // step: not matched (needs -rf); grok: any rm
		{"rm -- -rf", FTokenDangerous},     // same divergence
		{"chmod 777 /etc/passwd", FTokenDangerous},
		{"chown root:root /etc/shadow", FTokenDangerous},
		{"chattr +i /etc/passwd", FTokenDangerous},
		{"pkill -f myserver", FTokenDangerous},
		{"kill -9 1234", FTokenDangerous},
		{"killall nginx", FTokenDangerous},
		// Word boundary (grok: `tr` != `truncate`, rmdir not rm) — these are
		// SAFE and live in TestAnalyzeSafe.
		// Disguised command heads must still match.
		{"r\\m '-rf' ./build", FTokenDangerous},          // step: escaped head
		{"rm -r\\\nf ./build", FTokenDangerous},          // step: line continuation
		{"r\"m\" -rf ./build", FTokenDangerous},          // pigo: concatenated quoting
		{"/bin/rm -rf ./build 2>/dev/null", FTokenDangerous}, // safe sink must not mask the rm
		// mkfs / dd / truncate-device (step rules).
		{"mkfs.ext4 /dev/test", FTokenDangerous},
		{"mkfs /dev/test", FTokenDangerous},
		{"dd if=image of=/dev/test", FTokenDangerous},
		{":> /dev/test", FTokenTruncateDevice},
		{"echo hi > /dev/sda", FTokenTruncateDevice},
		// System lifecycle (step).
		{"reboot", FTokenLifecycle},
		{"shutdown now", FTokenLifecycle},
		{"sudo reboot", FTokenLifecycle},
		{"sh -c 'shutdown now'", FTokenLifecycle},
		{"pwsh -Command rm -rf ./build", FTokenDangerous},
		{"setsid -cfw reboot", FTokenLifecycle},
		{"systemctl reboot", FTokenLifecycle},
		// Destructive git (step) + push (grok).
		{"git push", FTokenDangerous},
		{"git push -f origin main", FTokenDangerous},
		{"git push --force-with-lease=main:expected origin main", FTokenDangerous},
		{"git reset --hard", FTokenDestructiveGit},
		{"git clean -f", FTokenDestructiveGit},
		{"git clean -xdf", FTokenDestructiveGit},
		// Wrapper stripping exposes the inner command (step wrapper tests).
		{"timeout 5 rm -rf ./build", FTokenDangerous},
		{"timeout -sTERM -k1 5 rm -rf", FTokenDangerous},
		{"timeout --signal=TERM --kill-after=1 5 rm", FTokenDangerous},
		{"timeout --verbose -- 5 rm -rf", FTokenDangerous},
		{"nice -- rm -rf", FTokenDangerous},
		{"stdbuf -oL -- bash -c 'rm -rf'", FTokenDangerous},
		{"env MODE=test command rm -rf ./build", FTokenDangerous},
		{"env --chdir=/tmp rm -rf ./build", FTokenDangerous},
		{"env -C /tmp rm -rf ./build", FTokenDangerous},
		{"env --unset SECRET rm -rf ./build", FTokenDangerous},
		{"sudo -u root -- /bin/rm -rf ./build", FTokenDangerous},
		{"env MODE=test timeout -s TERM 5 nice -n 0 setsid -w stdbuf -oL rm -rf ./build", FTokenDangerous},
		{"nohup rm -rf ./build", FTokenDangerous},
		{"exec rm -rf ./build", FTokenDangerous},
		{"exec -a name rm -rf x", FTokenDangerous},
		{"time rm -rf ./build", FTokenDangerous},
		{"cd project && rm -rf ./build", FTokenDangerous},
		{"false || rm -rf ./build", FTokenDangerous},
		{"sh -c 'rm -rf ./build'", FTokenDangerous},
		{"bash -ec -- 'rm -rf x'", FTokenDangerous}, // grok: resolves past --
		{"bash -ec 'rm -rf x'", FTokenDangerous},
		{"find . -exec rm -rf {} +", FTokenDangerous},
		{"printf '%s' ./build | xargs rm -rf", FTokenDangerous},
		// Command substitutions execute when the word expands (step).
		{"echo \"$(rm -rf ./build)\"", FTokenDangerous},
		{"echo `rm -rf ./build`", FTokenDangerous},
		{"! rm -rf ./build", FTokenDangerous},  // negation runs it too
		{"rm -rf ./build &", FTokenDangerous},  // background runs it too
		{"FOO=bar rm -rf ./build", FTokenDangerous},
		{"x=-rf; rm $x ./build", FTokenDangerous},
		{"cat <<EOF\n$(rm -rf ./build)\nEOF", FTokenDangerous}, // unquoted heredoc expands
		{"cat <<'EOF' | sh\nrm -rf ./build\nEOF", FTokenDangerous}, // quoted body fed to sh is code
		{"sh <<EOF\nrm -rf ./build\nEOF", FTokenDangerous},
		// Redirect write floor (grok redirect_write; only /dev/null is safe).
		{"cat payload > out", FTokenRedirectWrite},
		{"touch CANARY > $OUT", FTokenRedirectWrite},
		{"echo hi >> log.txt", FTokenRedirectWrite},
		{"go build > log.txt", FTokenRedirectWrite},
		{"cmd1 | tee out.txt", FTokenRedirectWrite},
		{"'2'>/tmp/output rm -rf ./build", FTokenRedirectWrite}, // step: unresolved; pigo: redirect-write
	}
	for _, tt := range tests {
		d := Analyze(tt.cmd)
		if d.Verdict != Hazardous {
			t.Errorf("Analyze(%q) = %s (reason=%q), want hazardous", tt.cmd, d.Verdict, d.Reason)
			continue
		}
		if tt.token != "" && !hasToken(d, tt.token) {
			t.Errorf("Analyze(%q) findings %v, want token %q", tt.cmd, d.Findings, tt.token)
		}
	}
}

func TestAnalyzeSafe(t *testing.T) {
	tests := []string{
		"", "   ", "ls", "ls -la", "echo hi > /dev/null", "echo hi 2>/dev/null",
		"go build ./...", "go test ./internal/...", "true", "exit 0",
		// Quote disguise: the string is data, not code (step tests).
		"echo rm -rf ./build",
		"printf '%s' 'rm -rf ./build'",
		"grep 'rm -rf' script.sh",
		"printf '%s' '$(rm -rf ./build)'",
		"sh -c '' 'rm -rf ./build'",            // second arg is $0, not code
		"sh -c 'printf %s' label 'rm -rf ./build'", // positionals are data
		"command -v rm -rf ./build",            // display mode
		"sudo -un root rm -rf ./build",         // -un = user "n"; command is "root"
		"qemu-system-x86_64 -no-reboot -no-shutdown",
		"timeout 5 echo rm -rf ./build",        // wrapper does not promote operands
		"timeout 5 -- rm -rf ./build",
		"timeout 5 bash -c 'printf %s' label 'rm -rf ./build'",
		"stdbuf --output='rm -rf ./build' printf safe",
		"nice --adjustment=5 echo hi",
		"env MODE=test echo hi",
		"export FOO=bar", "export FOO=\"$BAR\"", "declare -a x=(a b)",
		// Quoted heredoc bodies are data (step tests).
		"cat <<'EOF'\nrm -rf ./build\nEOF",
		"cat <<'EOF'\n$(rm -rf ./build)\nEOF",
		"cat <<-EOF\n\trm -rf ./build\n\tEOF",
		// Structured walks.
		"if true; then echo hi; fi",
		"while read l; do echo $l; done < in",
		"{ echo a; echo b; }", "(echo a)",
		// Word boundary: whole-word head matching never hits substrings
		// (grok: `tr` != `truncate`).
		"rmdir ./empty", "truncate -s 0 ./log",
		"git clean -xdn", // dry run, no force
		// Everything after # on a line is a comment: the rm never runs.
		"echo ok # cleanup; rm -rf ./build",
	}
	for _, cmd := range tests {
		d := Analyze(cmd)
		if d.Verdict != Safe {
			t.Errorf("Analyze(%q) = %s (reason=%q, findings=%v), want safe", cmd, d.Verdict, d.Reason, d.Findings)
		}
	}
}

func TestAnalyzeIncomplete(t *testing.T) {
	tests := []struct{ cmd, reason string }{
		// Parse failures.
		{"rm -rf 'unclosed", RShellSyntax},
		{"if true; then", RShellSyntax},
		// Wrapper fail-closed cases (step wrapper tests).
		{"timeout --unknown 5 rm -rf", RWrapperOptions},
		{"timeout -x 5", RWrapperOptions},
		{"timeout", RDynamicWrapper},
		{"timeout -s", RDynamicWrapper},
		{"timeout \"$DURATION\" rm -rf", RDynamicWrapper},
		{"timeout 5", RDynamicWrapper},
		{"timeout 5 \"$COMMAND\" -rf", ""}, // incomplete; reason is dynamic-command
		{"nice -5", RWrapperOptions},
		{"nice --unknown", RWrapperOptions},
		{"nice -n", RDynamicWrapper},
		{"stdbuf --unknown", RWrapperOptions},
		{"stdbuf -o", RDynamicWrapper},
		{"setsid --wait=yes", RWrapperOptions},
		{"env -S 'rm -rf x'", RWrapperSplitString},
		{"env --split-string 'x'", RWrapperSplitString},
		// Dynamic command / script bodies.
		{"\"$COMMAND\" -rf", RDynamicCommand},
		{"timeout 5 \"$COMMAND\"", RDynamicCommand},
		{"bash script.sh", RDynamicScript},
		{"sh -s args", RDynamicScript},
		{"sh -c \"$USER_INPUT\"", RDynamicScript},
		{"eval \"$1\"", RDynamicScript},
		{"source ./script.sh", RDynamicScript},
		{". ./script.sh", RDynamicScript},
		{"csh script.csh", RDynamicScript},
		{"pwsh -File ./x.ps1", RDynamicScript},
		{"pwsh", RDynamicScript},
		{"f() { rm -rf x; }", RUnsupportedConstruct},
		// Git fail-closed (grok tests).
		{"git -c core.fsmonitor=/x status", RGitGlobalOptions},
		{"git --exec-path=/evil status", RGitGlobalOptions},
		{"git -p status", RGitGlobalOptions},
		{"/usr/bin/git status", RGitGlobalOptions},
		{"Git status", RGitGlobalOptions},
		{"git -- status", RGitGlobalOptions},
		{"git log --output /tmp/out", RGitUnsafeQueryOption},
		{"git cat-file --filt HEAD:data.bin", RGitUnsafeQueryOption},
		{"git grep -O touch-evil TODO", RGitUnsafeQueryOption},
	}
	for _, tt := range tests {
		d := Analyze(tt.cmd)
		if d.Verdict != Incomplete {
			t.Errorf("Analyze(%q) = %s, want incomplete", tt.cmd, d.Verdict)
			continue
		}
		if tt.reason != "" && d.Reason != tt.reason {
			t.Errorf("Analyze(%q) reason = %q, want %q", tt.cmd, d.Reason, tt.reason)
		}
	}
}

func TestAnalyzeGitReadOnly(t *testing.T) {
	tests := []string{
		"git status", "git -C sub status", "git --no-pager diff --stat",
		"git cat-file -p HEAD:src/main.rs", "git shortlog -sn",
		"git rev-parse HEAD", "git log --oneline -5", "git diff HEAD~1",
	}
	for _, cmd := range tests {
		d := Analyze(cmd)
		if d.Verdict != Safe {
			t.Errorf("Analyze(%q) = %s (reason=%q), want safe", cmd, d.Verdict, d.Reason)
		}
	}
}

func TestAnalyzeBudgets(t *testing.T) {
	if d := Analyze(strings.Repeat("x", maxSourceLen+1)); d.Verdict != Incomplete || d.Reason != RAnalysisLimit {
		t.Errorf("oversized input verdict = %s reason=%q, want analysis-limit", d.Verdict, d.Reason)
	}
	// Command count budget.
	var b strings.Builder
	for i := 0; i < maxCommands+10; i++ {
		b.WriteString("true; ")
	}
	if d := Analyze(b.String()); d.Verdict != Incomplete || d.Reason != RAnalysisLimit {
		t.Errorf("command budget verdict = %s reason=%q, want analysis-limit", d.Verdict, d.Reason)
	}
	// Substitution nesting budget.
	deep := "true"
	for i := 0; i < maxScriptDepth+2; i++ {
		deep = "echo $(" + deep + ")"
	}
	if d := Analyze(deep); d.Verdict != Incomplete || d.Reason != RAnalysisLimit {
		t.Errorf("nesting budget verdict = %s reason=%q, want analysis-limit", d.Verdict, d.Reason)
	}
	// Wrapper depth budget.
	wrapped := "rm -rf x"
	for i := 0; i < maxWrapperDepth+3; i++ {
		wrapped = "env " + wrapped
	}
	if d := Analyze(wrapped); d.Verdict != Incomplete {
		t.Errorf("wrapper budget verdict = %s, want incomplete", d.Verdict)
	}
}

func TestParseMode(t *testing.T) {
	for s, want := range map[string]Mode{
		"off": ModeOff, "ask": ModeAsk, "strict": ModeStrict,
	} {
		got, err := ParseMode(s)
		if err != nil || got != want {
			t.Errorf("ParseMode(%q) = %v, %v; want %v, nil", s, got, err, want)
		}
	}
	if _, err := ParseMode("yes"); err == nil {
		t.Error("ParseMode(\"yes\") = nil error, want error")
	}
}

func hasToken(d Decision, token string) bool {
	for _, f := range d.Findings {
		if f.Token == token {
			return true
		}
	}
	return false
}
