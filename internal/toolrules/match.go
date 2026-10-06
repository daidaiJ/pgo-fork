// match.go holds the per-tool pattern matching semantics. The bash rules
// translate grok's word-boundary prefix制度 (matches_command_prefix): a
// pattern matches only when it is followed by whitespace or the end of the
// segment — "tr" can never match "truncate" (CWE-183). Compound execution
// surfaces never match allow rules (fail closed); deny rules match any
// segment because denial does not need precision.
package toolrules

import (
	"strings"
)

// compoundMarkers are the shell metacharacters that make a command
// "compound" for allow-rule purposes: multiple commands, pipelines,
// command substitution, backgrounding. A command containing any of these
// is never auto-allowed by a pattern rule — the user must trust the
// directory or answer the prompt (shellguard additionally analyzes it).
const compoundMarkers = ";|&`$><\n\\"

// compoundSegments splits a bash command on the segment separators so a deny
// rule can match any of its parts. Substitution payloads are not parsed —
// this is a coarse filter, not an analyzer (shellguard owns AST analysis).
func compoundSegments(cmd string) []string {
	fields := strings.FieldsFunc(cmd, func(r rune) bool {
		return r == ';' || r == '\n'
	})
	// Pipes and && / || also start new segments; a simple split on the
	// separator characters is enough for deny matching (over-splitting is
	// safe for deny: more segments, more chances to match, deny-only).
	segments := make([]string, 0, len(fields))
	for _, f := range fields {
		for _, p := range splitAll(f, []string{"&&", "||", "|"}) {
			if s := strings.TrimSpace(p); s != "" {
				segments = append(segments, s)
			}
		}
	}
	if len(segments) == 0 && strings.TrimSpace(cmd) != "" {
		return []string{strings.TrimSpace(cmd)}
	}
	return segments
}

// splitAll splits s on every occurrence of any separator in seps.
func splitAll(s string, seps []string) []string {
	parts := []string{s}
	for _, sep := range seps {
		var next []string
		for _, p := range parts {
			next = append(next, strings.Split(p, sep)...)
		}
		parts = next
	}
	return parts
}

// bashMatchesAllow reports whether cmd is a single (non-compound) command
// whose first segment has the word-boundary prefix pattern.
func bashMatchesAllow(cmd, pattern string) bool {
	if strings.ContainsAny(cmd, compoundMarkers) {
		return false
	}
	return wordBoundaryPrefix(strings.TrimSpace(cmd), pattern)
}

// bashMatchesDeny reports whether ANY segment of cmd has the word-boundary
// prefix pattern. Deny is deliberately segment-aware but precision-free.
func bashMatchesDeny(cmd, pattern string) bool {
	for _, seg := range compoundSegments(cmd) {
		if wordBoundaryPrefix(seg, pattern) {
			return true
		}
	}
	return false
}

// wordBoundaryPrefix reports whether s starts with pattern and the character
// right after the pattern is whitespace or the end of the string — grok's
// matches_command_prefix invariant ("tr" must not match "truncate").
func wordBoundaryPrefix(s, pattern string) bool {
	if pattern == "" {
		return false
	}
	if !strings.HasPrefix(s, pattern) {
		return false
	}
	rest := s[len(pattern):]
	return rest == "" || rest[0] == ' ' || rest[0] == '\t'
}

// matchPath reports whether target falls under a write/edit rule pattern.
// Path patterns are PREFIXES, not globs (deviation D-2): "docs/" matches
// everything under docs/, "config.toml" matches the exact file. Prefix
// semantics are predictable and fail closed (a typo cannot widen the rule
// to unexpected territory). Comparison is case-insensitive because the
// primary platform (Windows) has case-insensitive paths; on POSIX this can
// in theory over-match a case variant — accepted and registered as a
// limitation, since rules are user-authored boundary hints, not a sandbox.
func matchPath(target, pattern string) bool {
	if pattern == "" {
		return true
	}
	t := strings.ToLower(strings.TrimRight(target, "\\/"))
	p := strings.ToLower(strings.TrimRight(pattern, "\\/"))
	if t == p {
		return true
	}
	return strings.HasPrefix(t, p+"/") || strings.HasPrefix(t, p+"\\")
}
