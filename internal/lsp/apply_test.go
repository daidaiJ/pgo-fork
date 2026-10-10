// apply_test.go exercises the workspace-edit application math alone (the
// tool's file IO is gated separately): same-line and multi-line ranges,
// CRLF round-trip, UTF-16 surrogate columns, and the fail-loud edges.
package lsp

import (
	"strings"
	"testing"
)

func TestApplyEditsSingleLine(t *testing.T) {
	out, err := ApplyEdits("package a\n\nvar old = 1\n", []TextEdit{
		{StartLine: 2, StartChar: 4, EndLine: 2, EndChar: 7, NewText: "new"},
	})
	if err != nil {
		t.Fatalf("ApplyEdits: %v", err)
	}
	if out != "package a\n\nvar new = 1\n" {
		t.Errorf("out = %q", out)
	}
}

func TestApplyEditsBottomUpStaysValid(t *testing.T) {
	// Two edits on different lines given in ascending order: applying
	// bottom-up must keep the earlier line's offsets valid.
	out, err := ApplyEdits("aa\nbb\ncc\n", []TextEdit{
		{StartLine: 0, StartChar: 0, EndLine: 0, EndChar: 1, NewText: "X"},
		{StartLine: 2, StartChar: 1, EndLine: 2, EndChar: 2, NewText: "Z"},
	})
	if err != nil {
		t.Fatalf("ApplyEdits: %v", err)
	}
	if out != "Xa\nbb\ncZ\n" {
		t.Errorf("out = %q", out)
	}
}

func TestApplyEditsMultiLineAndCRLF(t *testing.T) {
	out, err := ApplyEdits("a\r\nb\r\nc\r\n", []TextEdit{
		{StartLine: 0, StartChar: 1, EndLine: 2, EndChar: 0, NewText: "X\nY"},
	})
	if err != nil {
		t.Fatalf("ApplyEdits: %v", err)
	}
	if out != "aX\r\nYc\r\n" {
		t.Errorf("out = %q", out)
	}
}

func TestApplyEditsUTF16Surrogate(t *testing.T) {
	// "🙂" is one rune = two UTF-16 units; column 2 sits after it.
	out, err := ApplyEdits("a 🙂b\n", []TextEdit{
		{StartLine: 0, StartChar: 2, EndLine: 0, EndChar: 4, NewText: "Y"},
	})
	if err != nil {
		t.Fatalf("ApplyEdits: %v", err)
	}
	if out != "a Yb\n" {
		t.Errorf("out = %q", out)
	}
	// A column inside the surrogate pair is refused.
	if _, err := ApplyEdits("a 🙂b\n", []TextEdit{
		{StartLine: 0, StartChar: 3, EndLine: 0, EndChar: 4, NewText: "Y"},
	}); err == nil || !strings.Contains(err.Error(), "surrogate") {
		t.Errorf("in-surrogate column should refuse, got %v", err)
	}
}

func TestApplyEditsFailLoudEdges(t *testing.T) {
	cases := []struct {
		name  string
		edits []TextEdit
		want  string
	}{
		{"overlap", []TextEdit{
			{StartLine: 0, StartChar: 0, EndLine: 0, EndChar: 2, NewText: "X"},
			{StartLine: 0, StartChar: 1, EndLine: 0, EndChar: 3, NewText: "Y"},
		}, "overlapping"},
		{"past EOF line", []TextEdit{
			{StartLine: 9, StartChar: 0, EndLine: 9, EndChar: 1, NewText: "X"},
		}, "outside the file"},
		{"column past line", []TextEdit{
			{StartLine: 0, StartChar: 9, EndLine: 0, EndChar: 10, NewText: "X"},
		}, "past the line end"},
	}
	for _, tc := range cases {
		if _, err := ApplyEdits("abc\n", tc.edits); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: want %q failure, got %v", tc.name, tc.want, err)
		}
	}
}

// TestEditsByFileShapes covers both 3.17 WorkspaceEdit shapes (changes keyed
// by URI, documentChanges list) and their merge.
func TestEditsByFileShapes(t *testing.T) {
	uri := "file:///w/main.go"
	we := workspaceEdit{
		Changes: map[string][]wireTextEdit{
			uri: {{Range: wireRange{Start: wirePosition{Line: 1, Character: 2}, End: wirePosition{Line: 1, Character: 5}}, NewText: "a"}},
		},
		DocumentChanges: []textDocumentEdit{
			{TextDocument: optionalVersionedTextDocumentIdentifier{URI: uri}, Edits: []wireTextEdit{
				{Range: wireRange{Start: wirePosition{Line: 3, Character: 0}, End: wirePosition{Line: 3, Character: 1}}, NewText: "b"},
			}},
		},
	}
	edits := we.EditsByFile()
	if len(edits) != 1 {
		t.Fatalf("files = %v", edits)
	}
	if got := edits["/w/main.go"]; len(got) != 2 || got[0].NewText != "a" || got[1].NewText != "b" {
		t.Errorf("merged edits = %+v", got)
	}
}
