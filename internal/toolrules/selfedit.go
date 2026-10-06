// selfedit.go implements the self-modification-surface guard (qwen参照): the
// agent's own configuration files (trust.json, permissions.json, config.toml)
// must never be writable by an allow rule or a directory trust grant — they
// ARE the permission boundary. A write targeting them always escalates to
// the interactive ask channel (or fails closed when there is none), and the
// comparison resolves symlinks first so a junction/symlink cannot smuggle a
// write past the literal-path check (qwen's symlink-penetration check).
package toolrules

import (
	"path/filepath"
	"strings"
)

// SelfEditSurface holds the canonical (symlink-resolved) protected paths.
type SelfEditSurface struct {
	paths []string // lowercased canonical forms for comparison
}

// NewSelfEditSurface canonicalizes the given protected paths (missing files
// fall back to resolving the nearest existing ancestor, since a write may
// target a not-yet-existing file inside a protected directory). Empty input
// yields a disabled surface.
func NewSelfEditSurface(paths ...string) *SelfEditSurface {
	s := &SelfEditSurface{}
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if c := canonicalize(p); c != "" {
			s.paths = append(s.paths, strings.ToLower(c))
		}
	}
	return s
}

// canonicalize resolves symlinks along the path. For a target that does not
// exist yet it resolves the deepest existing ancestor and re-attaches the
// remaining elements, so "write to $PIGO_HOME/sub/new-permissions.json" is
// still seen through the same lens as its real parent.
func canonicalize(p string) string {
	p = filepath.Clean(p)
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	dir, base := filepath.Split(p)
	dir = filepath.Clean(dir)
	if dir == "." || dir == "" {
		return p
	}
	resolvedDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		// The parent is missing too: recurse towards the root.
		resolvedDir = canonicalize(dir)
		if resolvedDir == "" {
			return ""
		}
	}
	return filepath.Join(resolvedDir, base)
}

// HitPath reports whether a write/edit target lands on the protected
// surface. An empty surface never hits.
func (s *SelfEditSurface) HitPath(target string) bool {
	if s == nil || len(s.paths) == 0 || target == "" {
		return false
	}
	c := strings.ToLower(canonicalize(target))
	for _, p := range s.paths {
		if c == p {
			return true
		}
	}
	return false
}

// HitCommand reports whether a bash command's TEXT mentions a protected
// path. This is a best-effort literal scan (deviation D-3): it does not
// parse argv, resolve relative paths, or decode obfuscation — the shellguard
// AST layer is where deeper analysis belongs. The direction is safe: a false
// positive only forces a prompt; the compound-marker rule already prevents
// redirects from matching allow rules.
func (s *SelfEditSurface) HitCommand(cmd string) bool {
	if s == nil || len(s.paths) == 0 || cmd == "" {
		return false
	}
	lower := strings.ToLower(cmd)
	for _, p := range s.paths {
		if strings.Contains(lower, p) {
			return true
		}
		// Windows-agnostic: also match the forward-slash rendering of the
		// path, since command text often uses "D:/..." style.
		if alt := strings.ReplaceAll(p, "\\", "/"); alt != p && strings.Contains(lower, alt) {
			return true
		}
	}
	return false
}

// ProtectedPaths returns the canonical input paths (for tests and /status
// style reporting).
func (s *SelfEditSurface) ProtectedPaths() []string {
	if s == nil {
		return nil
	}
	return s.paths
}
