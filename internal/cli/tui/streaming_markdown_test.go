package tui

import (
	"strings"
	"testing"
)

// The boundary tests translate crush's incremental_glamour_test.go suite
// (T2.2): findSafeMarkdownBoundary's decision tree, streaming equivalence
// against a fresh full render, and the conservative fallbacks.

// TestFindSafeMarkdownBoundaryTable exercises the boundary decision tree
// across paragraphs, fences (open and closed), lists, tables, quotes, setext
// underlines, and indented code. -1 means "no safe boundary".
func TestFindSafeMarkdownBoundaryTable(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    int
	}{
		{"empty", "", -1},
		{"single line", "Just a single paragraph", -1},
		{"two paragraphs", "First paragraph.\n\nSecond paragraph.", len("First paragraph.\n\n")},
		{"three paragraphs picks latest", "First.\n\nSecond.\n\nThird.", len("First.\n\nSecond.\n\n")},
		{
			// The only blank line sits before the fence opener; the fence is
			// still closed at that point, so the boundary before it is safe.
			name:    "open fence at end",
			content: "Para.\n\n```go\nfoo()\n",
			want:    len("Para.\n\n"),
		},
		{
			// Blank lines inside the open fence have odd fence parity and are
			// rejected; the boundary before the fence survives.
			name:    "inside open fence",
			content: "Para.\n\n```go\nfoo()\n\nbar()\n",
			want:    len("Para.\n\n"),
		},
		{
			name:    "closed fence followed by paragraph",
			content: "Para1.\n\n```\nfoo()\n```\n\nPara2.",
			want:    len("Para1.\n\n```\nfoo()\n```\n\n"),
		},
		{
			// The boundary BEFORE the list is accepted (a list opening after
			// the cut does not change the prefix's render).
			name:    "open list at end",
			content: "Para.\n\n- one\n- two\n",
			want:    len("Para.\n\n"),
		},
		{"list interior no blank line", "- one\n- two\n", -1},
		{
			// The blank line after the list has a list item as the last
			// non-blank prefix line — conservatively rejected.
			name:    "closed list then paragraph",
			content: "- one\n- two\n\nPara.",
			want:    -1,
		},
		{
			name:    "table at end",
			content: "Para.\n\n| a | b |\n| --- | --- |\n| 1 | 2 |\n",
			want:    len("Para.\n\n"),
		},
		{
			// The mid-table blank line's prefix ends on a pipe line — rejected.
			name:    "table interior with blank line",
			content: "| a | b |\n| --- | --- |\n\n| 1 | 2 |\n",
			want:    -1,
		},
		{
			name:    "block quote at end",
			content: "Para.\n\n> quoted\n> still quoted\n",
			want:    len("Para.\n\n"),
		},
		{
			// The suffix's first line looks like a setext underline; splitting
			// would retroactively change the prefix's paragraph render.
			name:    "setext underline pending",
			content: "Heading\n\n=====\n",
			want:    -1,
		},
		{
			// Indented-code line as the last non-blank prefix line is rejected;
			// the earlier boundary survives.
			name:    "indented code in prefix",
			content: "Para.\n\n    code line\n\nNext.",
			want:    len("Para.\n\n"),
		},
		{
			// HTML block opener in the prefix forfeits the boundary (B2).
			name:    "html block opener in prefix",
			content: "<div>\n\nPara.\n\nNext.",
			want:    -1,
		},
		{
			// Link reference definition in the prefix forfeits the boundary (B3).
			name:    "link ref definition in prefix",
			content: "[label]: http://x\n\nPara.\n\nNext.",
			want:    -1,
		},
		{
			// Refined B1: a closed list followed by a NON-indented paragraph
			// keeps the boundary after that paragraph (CHARM-1785).
			name:    "closed list then non-indented paragraph",
			content: "- one\n- two\n\nPara.\n\nNext.",
			want:    len("- one\n- two\n\nPara.\n\n"),
		},
		{
			// Refined B1: a loose-list continuation (indented line after the
			// blank line) keeps the list open — boundary forfeited.
			name:    "loose list continuation",
			content: "- one\n- two\n\n   continuation\n\nNext.",
			want:    -1,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := findSafeMarkdownBoundary(c.content)
			if got != c.want {
				t.Fatalf("findSafeMarkdownBoundary(%q) = %d, want %d", c.content, got, c.want)
			}
			if got > 0 {
				if got > len(c.content) {
					t.Fatalf("boundary %d out of range (len=%d)", got, len(c.content))
				}
				if c.content[got-1] != '\n' {
					t.Fatalf("boundary %d does not sit immediately after a newline", got)
				}
			}
		})
	}
}

// streamingScenarios returns canonical document shapes exercising different
// boundary paths (translated from crush's streamingScenarios).
func streamingScenarios() []struct {
	name string
	doc  string
} {
	return []struct {
		name string
		doc  string
	}{
		{"plain-paragraphs", strings.Join([]string{
			"This is the first paragraph of the document.",
			"",
			"Here is the second paragraph; it has some words.",
			"",
			"And a third paragraph for good measure.",
			"",
			"Finally a fourth paragraph to push past one boundary.",
		}, "\n")},
		{"paragraphs-with-fence", strings.Join([]string{
			"Intro paragraph.",
			"",
			"Some explanatory prose before the code.",
			"",
			"```go",
			"func hello() {",
			"\tfmt.Println(\"hi\")",
			"}",
			"```",
			"",
			"And a closing paragraph after the code block.",
		}, "\n")},
		{"paragraphs-with-list", strings.Join([]string{
			"Intro paragraph.",
			"",
			"- list item one",
			"- list item two",
			"- list item three",
			"",
			"Trailing paragraph.",
		}, "\n")},
		{"paragraphs-with-table", strings.Join([]string{
			"Intro paragraph.",
			"",
			"| col a | col b |",
			"| ----- | ----- |",
			"| 1     | 2     |",
			"| 3     | 4     |",
			"",
			"Trailing paragraph after the table.",
		}, "\n")},
	}
}

// progressivePrefixes splits doc into n monotonically growing byte prefixes,
// ending with the full document.
func progressivePrefixes(doc string, n int) []string {
	if n < 1 {
		n = 1
	}
	out := make([]string, 0, n)
	for i := 1; i <= n; i++ {
		size := len(doc) * i / n
		if i == n {
			size = len(doc)
		}
		out = append(out, doc[:size])
	}
	return out
}

// TestStreamingMarkdownFinalVisuallyEquivalent drives progressive prefixes
// through streamingMarkdown and asserts the final glued output matches a fresh
// full-document render on visible content (ANSI stripped, margins
// normalized): glue seams may differ cosmetically, never in content.
func TestStreamingMarkdownFinalVisuallyEquivalent(t *testing.T) {
	const width = 80
	const steps = 15

	for _, sc := range streamingScenarios() {
		t.Run(sc.name, func(t *testing.T) {
			var sm streamingMarkdown
			var got string
			for _, prefix := range progressivePrefixes(sc.doc, steps) {
				got = sm.render(prefix, width)
			}
			want := freshMarkdownRender(t, sc.doc, width)
			if normalizeRender(got) != normalizeRender(want) {
				t.Errorf("streaming final render mismatch:\n got: %q\nwant: %q", got, want)
			}
		})
	}
}

// TestStreamingMarkdownOpenFence pins the fence semantics: the boundary BEFORE
// an open fence is safe and cached; no boundary inside the fence ever
// advances it; once the fence closes the cache advances past it.
func TestStreamingMarkdownOpenFence(t *testing.T) {
	var sm streamingMarkdown
	const width = 80

	const before = "Para.\n\n"
	if got := sm.render(before+"```go\nfoo()\n", width); got == "" {
		t.Fatal("render produced nothing")
	}
	if sm.stablePrefix != before {
		t.Errorf("stablePrefix = %q, want the boundary before the open fence %q", sm.stablePrefix, before)
	}

	if got := sm.render(before+"```go\nfoo()\n\nbar()\n", width); got == "" {
		t.Fatal("render produced nothing")
	}
	if sm.stablePrefix != before {
		t.Errorf("stablePrefix advanced inside an open fence: %q", sm.stablePrefix)
	}

	const closed = before + "```go\nfoo()\n```\n\n"
	if got := sm.render(closed+"After.\n", width); got == "" {
		t.Fatal("render produced nothing")
	}
	if sm.stablePrefix != closed {
		t.Errorf("stablePrefix = %q, want the boundary after the closed fence %q", sm.stablePrefix, closed)
	}
}

// TestStreamingMarkdownResets pins the two self-invalidation paths: a width
// change and a non-prefix-extension (retried turn) both drop the cache.
func TestStreamingMarkdownResets(t *testing.T) {
	var sm streamingMarkdown
	const width = 80

	if got := sm.render("Para one.\n\nPara two.", width); got == "" {
		t.Fatal("first render produced nothing")
	}
	if sm.stablePrefix == "" {
		t.Fatal("cache did not seed")
	}
	seeded := sm.stablePrefix

	// Same document at a new width: cache must reset and reseed.
	if got := sm.render("Para one.\n\nPara two.", width-1); got == "" {
		t.Fatal("width-change render produced nothing")
	}
	if sm.width != width-1 {
		t.Errorf("width change did not reset the cache (width=%d)", sm.width)
	}
	if sm.stablePrefix != seeded {
		t.Errorf("reseeded prefix = %q, want %q", sm.stablePrefix, seeded)
	}

	// A retried turn (new document not extending the prefix): reset, reseed.
	if got := sm.render("Different doc.\n\nSecond para.", width-1); got == "" {
		t.Fatal("non-prefix render produced nothing")
	}
	if !strings.HasPrefix("Different doc.\n\nSecond para.", sm.stablePrefix) {
		t.Errorf("stablePrefix %q is not a prefix of the new content", sm.stablePrefix)
	}
}

// TestStreamingMarkdownProseRelaxBoundary covers the O(n²) guard: newline-
// separated prose with no blank lines never advances the blank-line cache,
// but once the unstable tail outgrows relaxBoundaryAfter a plain-newline cut
// is allowed.
func TestStreamingMarkdownProseRelaxBoundary(t *testing.T) {
	var sm streamingMarkdown
	const width = 80

	small := strings.Repeat("word line\n", 16)
	if got := sm.render(small, width); got == "" {
		t.Fatal("small prose render produced nothing")
	}
	if sm.stablePrefix != "" {
		t.Error("cache advanced on blank-line-free prose below the relax cap")
	}

	big := strings.Repeat("word line\n", 1024) // > relaxBoundaryAfter bytes
	if got := sm.render(big, width); got == "" {
		t.Fatal("large prose render produced nothing")
	}
	if sm.stablePrefix == "" {
		t.Error("relaxed boundary did not advance the cache on a large prose tail")
	}
}

// freshMarkdownRender renders content as one glamour document at width —
// the same trim renderMarkdown applies — for equivalence comparisons.
func freshMarkdownRender(t *testing.T, content string, width int) string {
	t.Helper()
	r := rendererFor(width)
	if r == nil {
		t.Fatal("no glamour renderer for width")
	}
	out, err := r.Render(content)
	if err != nil {
		t.Fatalf("glamour render: %v", err)
	}
	return strings.Trim(out, "\n")
}

// normalizeRender canonicalises a rendered string for visual comparison:
// strip ANSI, drop per-line trailing whitespace, collapse blank-line runs,
// and trim leading/trailing blanks. Glamour margins differ subtly between a
// monolithic render and glued halves; visible content must not.
func normalizeRender(s string) string {
	clean := stripANSI(s)
	lines := strings.Split(clean, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " \t")
	}
	out := make([]string, 0, len(lines))
	prevBlank := false
	for _, l := range lines {
		blank := l == ""
		if blank && prevBlank {
			continue
		}
		out = append(out, l)
		prevBlank = blank
	}
	for len(out) > 0 && out[0] == "" {
		out = out[1:]
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return strings.Join(out, "\n")
}
