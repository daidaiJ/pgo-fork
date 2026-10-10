// apply.go turns a decoded workspace edit into new file content. It stays a
// pure content → content function: reading, writing, and snapshotting the
// files are the tool's side-effectful half, so the permission engine gates
// them and tests can exercise the range math alone.
package lsp

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf16"
)

// ApplyEdits applies the edits to content and returns the new content.
// Edits are applied bottom-up so earlier offsets stay valid; overlapping
// ranges are a decode error (fail loud — a corrupt edit must not write a
// silently mangled file). Line/character offsets are LSP wire semantics:
// 0-based lines, UTF-16 code-unit columns.
func ApplyEdits(content string, edits []TextEdit) (string, error) {
	if len(edits) == 0 {
		return content, nil
	}
	sorted := make([]TextEdit, len(edits))
	copy(sorted, edits)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].StartLine != sorted[j].StartLine {
			return sorted[i].StartLine > sorted[j].StartLine
		}
		return sorted[i].StartChar > sorted[j].StartChar
	})
	// Overlap check on the descending order: an earlier-in-file edit (b)
	// must end before the later one (a) starts.
	for i := 1; i < len(sorted); i++ {
		a, b := sorted[i-1], sorted[i]
		if a.StartLine < b.EndLine || (a.StartLine == b.EndLine && a.StartChar < b.EndChar) {
			return "", fmt.Errorf("apply: overlapping edit ranges are ambiguous")
		}
	}
	crlf := strings.Contains(content, "\r\n")
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	for _, e := range sorted {
		if e.StartLine < 0 || e.StartLine >= len(lines) || e.EndLine < e.StartLine || e.EndLine >= len(lines) {
			return "", fmt.Errorf("apply: edit range %d:%d-%d:%d outside the file (%d lines)",
				e.StartLine, e.StartChar, e.EndLine, e.EndChar, len(lines))
		}
		startByte, err := utf16ByteOffset(lines[e.StartLine], e.StartChar)
		if err != nil {
			return "", fmt.Errorf("apply: edit start %d:%d: %w", e.StartLine+1, e.StartChar, err)
		}
		endByte, err := utf16ByteOffset(lines[e.EndLine], e.EndChar)
		if err != nil {
			return "", fmt.Errorf("apply: edit end %d:%d: %w", e.EndLine+1, e.EndChar, err)
		}
		// Everything stays in the \n domain (including the replacement
		// text); the CRLF form is restored once at the end, so an edit
		// carrying its own newlines is never doubled.
		combined := lines[e.StartLine][:startByte] + e.NewText + lines[e.EndLine][endByte:]
		lines = append(lines[:e.StartLine], append([]string{combined}, lines[e.EndLine+1:]...)...)
	}
	out := strings.Join(lines, "\n")
	if crlf {
		out = strings.ReplaceAll(out, "\n", "\r\n")
	}
	return out, nil
}

// utf16ByteOffset converts a UTF-16 code-unit column within line into a byte
// offset (the inverse of the wire encoding utf16Col produces). A column
// exactly at the line end clamps to the end; one past it, or inside a
// surrogate pair, is an error.
func utf16ByteOffset(line string, units int) (int, error) {
	if units == 0 {
		return 0, nil
	}
	u := 0
	for i, r := range line {
		if u == units {
			return i, nil
		}
		n := utf16.RuneLen(r)
		if n == -1 {
			n = 1 // invalid rune counts as one replacement unit
		}
		if u+n > units {
			return 0, fmt.Errorf("column %d lands inside a surrogate pair", units)
		}
		u += n
	}
	if u == units {
		return len(line), nil
	}
	return 0, fmt.Errorf("column %d past the line end (%d units)", units, u)
}
