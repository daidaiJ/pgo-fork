// This file implements the config.toml write path for the slash config
// surface (T6.9, spec slash-config-surface.md §2.1): /skills and /mcp toggle
// switches must land on disk because the slash command is an interactive
// projection of the config face, never a second source of truth.
//
// The strategy is a textual patch, not a whole-file rewrite: BurntSushi/toml
// cannot round-trip comments, and a rewrite would silently destroy every
// hand-written note in the user's config. A patch edits only the lines that
// carry the switched key — everything else, including ordering, comments and
// unrelated sections, survives byte-for-byte. The cost is that the patcher is
// line-based and must be conservative: it refuses (error, no write) whenever
// it cannot locate the section or key unambiguously, and it never creates a
// config file out of nothing (a missing file is a caller-visible error — the
// file should exist because pigo read it at startup).
package config

import (
	"fmt"
	"os"
	"strings"
)

// tomlline classification helpers. TOML tables ([name]) and array-of-tables
// entries ([[name]]) always start at column 0 in practice; keys are indented
// `key = value` lines. A comment or blank line inside a table block belongs to
// the block (comments are preserved by in-place key edits).

// splitSections cuts the file into blocks: each block starts at a header line
// ([table] or [[table]]) and runs to just before the next header. Prologue
// lines before the first header form block index 0 with an empty header.
// Header lines keep their exact original text (spacing and comments included).
type block struct {
	header string // e.g. "[skills]", "[[mcp.servers]]"; "" for the prologue
	lines  []string
}

func splitSections(lines []string) []block {
	var blocks []block
	cur := block{}
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]") && !strings.HasPrefix(t, "[[") || strings.HasPrefix(t, "[[") && strings.HasSuffix(t, "]]") {
			blocks = append(blocks, cur)
			cur = block{header: t}
			continue
		}
		cur.lines = append(cur.lines, line)
	}
	blocks = append(blocks, cur)
	return blocks
}

// blockName extracts the table path from a header: "[skills]" → "skills",
// "[[mcp.servers]]" → "mcp.servers", "[tools]" → "tools". Array-of-tables
// headers carry double brackets — both layers are stripped.
func blockName(header string) string {
	h := strings.TrimSpace(header)
	h = strings.TrimPrefix(h, "[")
	h = strings.TrimSuffix(h, "]")
	h = strings.TrimPrefix(h, "[")
	h = strings.TrimSuffix(h, "]")
	return h
}

// isHeader reports whether the line is a table or array-of-tables header.
func isHeader(line string) bool {
	t := strings.TrimSpace(line)
	if !strings.HasPrefix(t, "[") || !strings.HasSuffix(t, "]") {
		return false
	}
	// A line like "[foo]: bar" is not a header; require the bare-table shape
	// (optionally followed by a comment).
	inner := strings.TrimSuffix(strings.TrimPrefix(t, "["), "]")
	if strings.HasPrefix(inner, "[") && strings.HasSuffix(inner, "]") {
		inner = strings.TrimSuffix(strings.TrimPrefix(inner, "["), "]")
	}
	return inner != "" && !strings.Contains(inner, " ")
}

// headerMatches reports whether a header line names the wanted table. For
// array-of-tables ([[mcp.servers]]) every entry matches the same name; the
// caller disambiguates by the block's `name` key.
func headerMatches(line, table string) bool {
	if !isHeader(line) {
		return false
	}
	return blockName(strings.TrimSpace(line)) == table
}

// findKey scans a block's body for `key = ...` and returns its line index
// within the block (-1 when absent). The key is the whole left side of the
// first '=', trimmed — so any spacing around the '=' matches (sample configs
// in the wild use both `name = "x"` and `name    = "x"`). Keys are compared
// case-sensitively (TOML bare keys).
func findKey(b block, key string) int {
	for i, line := range b.lines {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "[") {
			continue // a header line cannot be a key
		}
		eq := strings.Index(t, "=")
		if eq <= 0 {
			continue
		}
		if strings.TrimSpace(t[:eq]) == key {
			return i
		}
	}
	return -1
}

// blockNameValue returns the string value of the block's `name = "..."` key
// (used to tell [[mcp.servers]] entries apart). Empty when absent.
func blockNameValue(b block) string {
	if i := findKey(b, "name"); i >= 0 {
		v, _ := stringValue(b.lines[i], "name")
		return v
	}
	return ""
}

// stringValue extracts a TOML basic-string value from a `key = value` line:
// strips the key and '=', then reads the first double-quoted string, so a
// trailing comment (`name = "web" # second`) parses as "web".
func stringValue(line, key string) (string, bool) {
	v := keyValue(line, key)
	if !strings.HasPrefix(v, `"`) {
		return "", false
	}
	rest := v[1:]
	var b strings.Builder
	for i := 0; i < len(rest); i++ {
		switch c := rest[i]; c {
		case '\\':
			if i+1 < len(rest) {
				i++
				b.WriteByte(rest[i]) // minimal escape handling (\" is the common case)
			}
		case '"':
			return b.String(), true
		default:
			b.WriteByte(c)
		}
	}
	return "", false
}

// keyValue returns the value part of a `key = value` line: everything after
// the key and the '=', trimmed. The '=' can sit on either side of whitespace
// (`disabled= x` / `disabled = x`), so both the key and a leading '=' are
// stripped before the caller parses the value.
func keyValue(line, key string) string {
	v := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), key))
	return strings.TrimSpace(strings.TrimPrefix(v, "="))
}

// marshalStringList renders []string as a single-line TOML array:
// ["a", "b"]. Empty slice renders [].
func marshalStringList(items []string) string {
	quoted := make([]string, len(items))
	for i, s := range items {
		quoted[i] = `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

// parseStringList reads a single-line TOML string array into a slice. The
// array body is taken as the text between the first '[' and its matching ']',
// so a trailing comment (`disabled_tools = ["a"]  # note`) is ignored. A
// multi-line array or a malformed value yields ok=false, and the caller
// refuses to patch rather than corrupting the file.
func parseStringList(v string) ([]string, bool) {
	v = strings.TrimSpace(v)
	if !strings.HasPrefix(v, "[") {
		return nil, false
	}
	end := strings.Index(v, "]")
	if end < 0 {
		return nil, false // multi-line array: refuse rather than guess
	}
	inner := strings.TrimSpace(v[1:end])
	if inner == "" {
		return []string{}, true
	}
	var out []string
	for _, part := range strings.Split(inner, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if !strings.HasPrefix(part, `"`) || !strings.HasSuffix(part, `"`) {
			return nil, false
		}
		out = append(out, strings.ReplaceAll(part[1:len(part)-1], `\"`, `"`))
	}
	return out, true
}

// writeSections reassembles blocks into the file text (trailing newline kept).
func writeSections(blocks []block) string {
	var b strings.Builder
	for i, bl := range blocks {
		if i > 0 || bl.header != "" {
			b.WriteString(bl.header)
			b.WriteString("\n")
		}
		for _, line := range bl.lines {
			b.WriteString(line)
			b.WriteString("\n")
		}
	}
	return b.String()
}

// saveFile writes text atomically-ish: write to a sibling temp file then
// rename, so a crash mid-write cannot leave a truncated config (mirrors the
// permissions store's save discipline).
func saveFile(path, text string) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(text), 0o600); err != nil {
		return fmt.Errorf("write config %s: %w", tmp, err)
	}
	return os.Rename(tmp, path)
}

// loadBlocks reads the config file and splits it into blocks. A missing file
// is an error: the config was read at startup, so a missing file now means the
// environment moved under us and silently creating one would hide that.
func loadBlocks(path string) ([]block, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\r\n"), "\r\n")
	lines = strings.Split(strings.Join(lines, "\n"), "\n")
	return splitSections(lines), nil
}

// SetSkillsDisabled adds or removes name from [skills] disabled (T6.9). The
// section is created when absent; the key is added when missing; the list is
// rewritten in place when present. Dedup is case-insensitive. Comments inside
// the [skills] block survive (the key line is replaced, not the block).
func SetSkillsDisabled(path, name string, disabled bool) error {
	if path == "" {
		return fmt.Errorf("skills: no config path")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("skills: empty name")
	}
	blocks, err := loadBlocks(path)
	if err != nil {
		return err
	}
	// Locate or synthesize the [skills] block.
	idx := -1
	for i := range blocks {
		if blocks[i].header != "" && blockName(blocks[i].header) == "skills" {
			idx = i
			break
		}
	}
	if idx == -1 {
		idx = len(blocks)
		blocks = append(blocks, block{header: "[skills]"})
	}
	b := &blocks[idx]

	// Read-modify-write the disabled list.
	var items []string
	keyLine := -1
	if i := findKey(*b, "disabled"); i >= 0 {
		val := keyValue(b.lines[i], "disabled")
		parsed, ok := parseStringList(val)
		if !ok {
			return fmt.Errorf("skills: [skills] disabled is not a single-line string array; edit %s by hand", path)
		}
		items = parsed
		keyLine = i
	}
	kept := items[:0:0]
	for _, it := range items {
		if !strings.EqualFold(strings.TrimSpace(it), name) {
			kept = append(kept, it)
		}
	}
	if disabled {
		kept = append(kept, name)
	}
	// An empty list carries no information: drop the key (the section header
	// may remain, harmlessly).
	if len(kept) == 0 && keyLine >= 0 {
		b.lines = append(b.lines[:keyLine], b.lines[keyLine+1:]...)
		return saveFile(path, writeSections(blocks))
	}
	line := "disabled = " + marshalStringList(kept)
	if keyLine >= 0 {
		b.lines[keyLine] = line
	} else {
		b.lines = append([]string{line}, b.lines...)
	}
	return saveFile(path, writeSections(blocks))
}

// SetMCPToolDisabled adds or removes mcp__<server>__<tool> from the matching
// [[mcp.servers]] entry's disabled_tools (T6.9, the per-tool switch §4). The
// entry is located by its `name` key. Enable-before-any-disable is a no-op.
func SetMCPToolDisabled(path, server, tool string, disabled bool) error {
	return patchMCPServer(path, server, func(b *block) error {
		var cur []string
		if i := findKey(*b, "disabled_tools"); i >= 0 {
			val := keyValue(b.lines[i], "disabled_tools")
			items, ok := parseStringList(val)
			if !ok {
				return fmt.Errorf("mcp: disabled_tools of server %q is not a single-line string array; edit %s by hand", server, path)
			}
			for _, it := range items {
				if !strings.EqualFold(strings.TrimSpace(it), tool) {
					cur = append(cur, it)
				}
			}
			if disabled {
				items = append(cur, tool)
			} else {
				items = cur
			}
			b.lines[i] = "disabled_tools = " + marshalStringList(items)
			return nil
		}
		if !disabled {
			return nil // nothing to enable
		}
		b.lines = append(b.lines, "disabled_tools = "+marshalStringList([]string{tool}))
		return nil
	})
}

// SetMCPServerEnabled sets or removes the enabled key of the matching
// [[mcp.servers]] entry (T6.9, the server-level switch §4). Setting true
// removes the key when it would be redundant with the default (nil = on).
func SetMCPServerEnabled(path, server string, enabled bool) error {
	return patchMCPServer(path, server, func(b *block) error {
		i := findKey(*b, "enabled")
		switch {
		case i >= 0:
			if enabled {
				b.lines = append(b.lines[:i], b.lines[i+1:]...) // default is on; drop the key
			} else {
				b.lines[i] = "enabled = false"
			}
		case !enabled:
			b.lines = append(b.lines, "enabled = false")
		}
		return nil
	})
}

// SetShellBackend writes the [shell] backend key (T8.4, the /shell switch's
// write path): the section is created when absent, the key replaced when
// present, comments inside the block preserved. Global config only — the
// project layer carries no shell key (T8.4 user ruling).
func SetShellBackend(path, backend string) error {
	if path == "" {
		return fmt.Errorf("shell: no config path")
	}
	backend = strings.TrimSpace(backend)
	if backend == "" {
		return fmt.Errorf("shell: empty backend")
	}
	blocks, err := loadBlocks(path)
	if err != nil {
		return err
	}
	idx := -1
	for i := range blocks {
		if blocks[i].header != "" && blockName(blocks[i].header) == "shell" {
			idx = i
			break
		}
	}
	if idx == -1 {
		idx = len(blocks)
		blocks = append(blocks, block{header: "[shell]"})
	}
	b := &blocks[idx]
	quoted := `"` + strings.ReplaceAll(backend, `"`, `\"`) + `"`
	if i := findKey(*b, "backend"); i >= 0 {
		b.lines[i] = "backend = " + quoted
	} else {
		b.lines = append([]string{"backend = " + quoted}, b.lines...)
	}
	return saveFile(path, writeSections(blocks))
}

// SetLSPTools writes the [lsp.gopls] tools allow-list (the deferred lsp_*
// tool-family filter) to the global config, preserving comments and every
// other key. A missing [lsp.gopls] table is created. A missing config file
// is an error (the same refuse-to-create rule SetShellBackend inherits), and
// an empty list is refused: an empty filter means "all tools", so writing it
// for "disable the last tool" would silently re-enable everything.
func SetLSPTools(path string, tools []string) error {
	if path == "" {
		return fmt.Errorf("lsp: no config path")
	}
	if len(tools) == 0 {
		return fmt.Errorf("lsp: empty tools list (an empty filter means all tools — disable would be lost)")
	}
	quoted := make([]string, 0, len(tools))
	for _, t := range tools {
		t = strings.TrimSpace(t)
		if t == "" {
			return fmt.Errorf("lsp: empty tool name")
		}
		quoted = append(quoted, `"`+strings.ReplaceAll(t, `"`, `\"`)+`"`)
	}
	blocks, err := loadBlocks(path)
	if err != nil {
		return err
	}
	idx := -1
	for i := range blocks {
		if blocks[i].header != "" && blockName(blocks[i].header) == "lsp.gopls" {
			idx = i
			break
		}
	}
	if idx == -1 {
		idx = len(blocks)
		blocks = append(blocks, block{header: "[lsp.gopls]"})
	}
	b := &blocks[idx]
	line := "tools = [" + strings.Join(quoted, ", ") + "]"
	if i := findKey(*b, "tools"); i >= 0 {
		b.lines[i] = line
	} else {
		b.lines = append(b.lines, line)
	}
	return saveFile(path, writeSections(blocks))
}

// patchMCPServer locates the [[mcp.servers]] entry whose `name` key equals
// server and applies fn to its block. Unambiguous match required: no entry is
// an error, and (because a user could name two entries the same — config
// today does not reject it) an ambiguous match refuses to patch.
func patchMCPServer(path, server string, fn func(*block) error) error {
	if path == "" {
		return fmt.Errorf("mcp: no config path")
	}
	blocks, err := loadBlocks(path)
	if err != nil {
		return err
	}
	matches := []int{}
	for i := range blocks {
		bl := blocks[i]
		if bl.header == "" || blockName(bl.header) != "mcp.servers" {
			continue
		}
		if blockNameValue(bl) == server {
			matches = append(matches, i)
		}
	}
	if len(matches) == 0 {
		return fmt.Errorf("mcp: no [[mcp.servers]] entry named %q in %s", server, path)
	}
	if len(matches) > 1 {
		return fmt.Errorf("mcp: %d [[mcp.servers]] entries named %q; edit %s by hand", len(matches), server, path)
	}
	if err := fn(&blocks[matches[0]]); err != nil {
		return err
	}
	return saveFile(path, writeSections(blocks))
}
