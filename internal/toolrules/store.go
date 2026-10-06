// store.go persists rules to $PIGO_HOME/permissions.json. The file schema is
// {"rules":[...]} with every entry ScopePersisted; session rules live only in
// the Engine and are never written. Persistence follows the trust manager's
// discipline: missing file = empty store (created lazily), malformed file =
// hard error (a corrupted boundary store must be surfaced, never silently
// overwritten), atomic write via CreateTemp+rename with sorted stable output.
package toolrules

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// DefaultPath returns the permissions file location: $PIGO_HOME/permissions.json
// or ~/.pigo/permissions.json, mirroring trust.DefaultPath. It returns "" when
// the home directory cannot be resolved and no override is set (store disabled).
func DefaultPath() string {
	if dir := os.Getenv("PIGO_HOME"); dir != "" {
		return filepath.Join(dir, "permissions.json")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".pigo", "permissions.json")
}

// storeFile is the on-disk schema.
type storeFile struct {
	Rules []Rule `json:"rules"`
}

// Store is the persisted rule set. Safe for concurrent use.
type Store struct {
	mu    sync.Mutex
	path  string
	rules []Rule
}

// LoadStore loads the permissions file at path. A missing file is not an
// error. path=="" yields an inert store (Add returns an error, Rules is
// empty) so callers can wire the engine without persistence.
func LoadStore(path string) (*Store, error) {
	s := &Store{path: path}
	if path == "" {
		return s, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, fmt.Errorf("toolrules: read %s: %w", path, err)
	}
	if len(data) == 0 {
		return s, nil
	}
	var f storeFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("toolrules: parse %s: %w", path, err)
	}
	for i, r := range f.Rules {
		norm, err := NormalizeRule(r)
		if err != nil {
			return nil, fmt.Errorf("toolrules: %s entry %d: %w", path, i, err)
		}
		f.Rules[i] = norm
	}
	s.rules = f.Rules
	return s, nil
}

// Rules returns a snapshot of the persisted rules.
func (s *Store) Rules() []Rule {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Rule, len(s.rules))
	copy(out, s.rules)
	return out
}

// Add appends a persisted rule and saves the file. A duplicate (same tool +
// pattern + action) is a no-op so repeated "save this rule" answers do not
// grow the file.
func (s *Store) Add(r Rule) error {
	norm, err := NormalizeRule(r)
	if err != nil {
		return err
	}
	norm.Scope = ScopePersisted
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.rules {
		if existing.Tool == norm.Tool && existing.Pattern == norm.Pattern && existing.Action == norm.Action {
			return nil
		}
	}
	if s.path == "" {
		return fmt.Errorf("toolrules: no permissions file configured")
	}
	s.rules = append(s.rules, norm)
	if err := s.saveLocked(); err != nil {
		s.rules = s.rules[:len(s.rules)-1]
		return err
	}
	return nil
}

// saveLocked writes the file atomically. Called with mu held.
func (s *Store) saveLocked() error {
	// Sorted output keeps the diff stable across saves (trust discipline).
	rules := make([]Rule, len(s.rules))
	copy(rules, s.rules)
	sort.SliceStable(rules, func(i, j int) bool {
		if rules[i].Tool != rules[j].Tool {
			return rules[i].Tool < rules[j].Tool
		}
		if rules[i].Pattern != rules[j].Pattern {
			return rules[i].Pattern < rules[j].Pattern
		}
		return rules[i].Action < rules[j].Action
	})
	data, err := json.MarshalIndent(storeFile{Rules: rules}, "", "  ")
	if err != nil {
		return fmt.Errorf("toolrules: encode: %w", err)
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".permissions-*.json")
	if err != nil {
		return fmt.Errorf("toolrules: temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("toolrules: chmod: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("toolrules: write: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("toolrules: close: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("toolrules: rename: %w", err)
	}
	return nil
}
