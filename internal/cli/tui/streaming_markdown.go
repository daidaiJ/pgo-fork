package tui

import (
	"strings"

	"github.com/charmbracelet/glamour"
)

// This file ports crush's streaming-markdown stable-prefix cache
// (charmbracelet/crush internal/ui/chat/streaming_markdown.go, T2.2). A still-
// streaming assistant reply is rendered incrementally: each flush re-renders
// only the trailing portion after a "safe boundary", while the render of the
// stable prefix before it is cached and glued on.
//
// The boundary between "stable" and "trailing" is detected by
// findSafeMarkdownBoundary: a position immediately after a blank line at which
// no markdown construct can be proven open (fenced code block, list, table,
// block quote, setext header, HTML block, link reference definition).
//
// Two renders concatenated are NOT generally equal to a single render of the
// whole document — glamour's wrap state resets between calls — so the boundary
// check is deliberately conservative: whenever it has the slightest doubt the
// caller falls back to a full render and the cache is left untouched. The
// turn-end renderMarkdown pass remains the final layout authority either way,
// so a glued render only has to look right, not byte-match the final one.
//
// Invariants:
//
//   - stablePrefix is always a literal byte prefix of the most recently
//     rendered content. If new content does not have stablePrefix as its
//     prefix the cache is dropped.
//   - stablePrefixRender is the glamour render of stablePrefix alone, with
//     surrounding whitespace trimmed for clean concatenation.
//   - width is the wrap width that produced stablePrefixRender. A width change
//     drops the cache (reflow can lay the transcript out at two widths — full
//     and full-1 with the scrollbar column — so the transcript keeps one cache
//     per width; see transcript.streamMd).
//
// Concurrency: glamour's Render is stateful and not safe for concurrent
// invocation on a shared renderer. Like renderMarkdown, this module is only
// ever called from bubbletea's single-threaded Update loop, so no extra
// serialization is added beyond mdMu's cache-map guard in rendererFor.

// streamingMarkdown caches the stable-prefix glamour render for one streaming
// document at one width.
type streamingMarkdown struct {
	width              int
	stablePrefix       string
	stablePrefixRender string
	// Cached cumulative state at the stable-prefix boundary, used by
	// findBoundaryAfter to validate new boundary candidates without re-scanning
	// the entire prefix from scratch. baseFenceCount is always even (safe
	// boundaries require even fence parity), so the delta scan always starts
	// outside a fence.
	baseFenceCount    int
	baseHasListMarker bool
}

// reset drops every cached field. After reset the next render call is
// guaranteed to be a full render.
func (s *streamingMarkdown) reset() {
	s.width = 0
	s.stablePrefix = ""
	s.stablePrefixRender = ""
	s.baseFenceCount = 0
	s.baseHasListMarker = false
}

// render returns the glamour render of content at the given width, reusing the
// cached stable-prefix render when it is safe to do so. On any uncertainty the
// call falls back to a full render and leaves the cache untouched (or drops
// it). A nil renderer (cache build failure) returns the raw source.
func (s *streamingMarkdown) render(content string, width int) string {
	r := rendererFor(width)
	if r == nil {
		return content
	}
	full := func() string {
		out, err := r.Render(content)
		if err != nil {
			return content
		}
		return strings.Trim(out, "\n")
	}

	// Width change OR content not a prefix-extension (e.g. a retried turn):
	// drop the cache, full render, then try to seed a fresh boundary from this
	// content so the next flush can go incremental again.
	if width != s.width || !strings.HasPrefix(content, s.stablePrefix) {
		s.reset()
		s.width = width
		out := full()
		s.tryAdvanceFromEmpty(content, width, r)
		return out
	}

	// Incremental boundary search: only scan the delta after the stable prefix.
	boundary := s.findBoundaryAfter(content)
	if boundary < 0 {
		// No safe boundary anywhere yet. Full render; do not modify the cache
		// (a future flush may find one).
		return full()
	}

	if boundary <= len(s.stablePrefix) {
		// Cached prefix already covers an at-least-as-late boundary. Render the
		// trailing partial fresh and glue.
		trail := content[len(s.stablePrefix):]
		return glueRenders(s.stablePrefixRender, s.renderTrailing(trail, r))
	}

	// A NEW chunk of safe content: render it, append to the cached prefix
	// render, promote the boundary, then render the remaining trail.
	newChunk := content[len(s.stablePrefix):boundary]
	newChunkRender := s.renderTrailing(newChunk, r)
	s.stablePrefixRender = glueRenders(s.stablePrefixRender, newChunkRender)
	s.stablePrefix = content[:boundary]
	s.baseFenceCount += countFenceLines(newChunk)
	s.baseHasListMarker = s.baseHasListMarker || chunkHasListMarker(newChunk)

	trail := content[boundary:]
	if trail == "" {
		// boundary == len(content): the cached prefix render is the answer.
		return s.stablePrefixRender
	}
	return glueRenders(s.stablePrefixRender, s.renderTrailing(trail, r))
}

// tryAdvanceFromEmpty seeds the cache from a fresh state. A full render of
// content has already been paid; if there is a safe boundary inside it, render
// the prefix once more (cheap relative to the full render) and cache it so the
// next flush can avoid the full work. The prefix is rendered separately rather
// than recovered from the full render output because two renders concatenated
// are not a single render of the whole, and the cached prefix render must be
// byte-for-byte what a future cached call would produce.
func (s *streamingMarkdown) tryAdvanceFromEmpty(content string, width int, r *glamour.TermRenderer) {
	boundary := findSafeMarkdownBoundary(content)
	if boundary <= 0 {
		boundary = s.relaxedBoundary(content, 0)
	}
	if boundary <= 0 {
		return
	}
	prefix := content[:boundary]
	out, err := r.Render(prefix)
	if err != nil {
		return
	}
	s.stablePrefix = prefix
	s.stablePrefixRender = trimGlamourMargins(out)
	s.width = width
	s.baseFenceCount = countFenceLines(prefix)
	s.baseHasListMarker = chunkHasListMarker(prefix)
}

// findBoundaryAfter searches for the latest safe boundary in content strictly
// after the stable prefix, using the cached cumulative state to validate
// candidates in O(delta) instead of re-scanning the whole prefix. Returns
// len(stablePrefix) when no new boundary exists (the caller then renders only
// the trailing partial and keeps the cache).
func (s *streamingMarkdown) findBoundaryAfter(content string) int {
	if len(s.stablePrefix) == 0 {
		if b := findSafeMarkdownBoundary(content); b > 0 {
			return b
		}
		return s.relaxedBoundary(content, 0)
	}

	// Scan blank-line candidates from latest to earliest, but only those
	// strictly after the stable prefix.
	for p := blankLineBefore(content, len(content)); p > len(s.stablePrefix); p = blankLineBefore(content, p-1) {
		if s.isSafeBoundaryIncremental(content, p) {
			return p
		}
	}
	if b := s.relaxedBoundary(content, len(s.stablePrefix)); b > len(s.stablePrefix) {
		return b
	}
	return len(s.stablePrefix)
}

// relaxBoundaryAfter bounds how much unstable tail one flush will re-render.
// Prose holding no blank line never satisfies the blank-line predicate, so
// without this cap the cache would never advance and every flush would
// re-render the whole document: O(n) per flush, O(n²) over a turn — enough to
// outrun the GC on a few-hundred-KB reasoning trace (crush #3162). Sized just
// above a prose paragraph so structured text never reaches it: the blank-line
// boundary fires first and the tail stays small.
const relaxBoundaryAfter = 2 << 10

// relaxedBoundaryCandidates caps how many newline candidates one flush will
// validate, keeping the search cost flat when none of them pass.
const relaxedBoundaryCandidates = 8

// relaxedBoundary looks for a cut at a plain newline once the tail after
// `after` has outgrown relaxBoundaryAfter. Candidates are validated by the
// same predicate the blank-line search uses, so every construct check
// survives; the only conservatism given up is paragraph wrapping across the
// cut, which costs one re-wrap at a point the reader has usually already
// scrolled past. Returns -1 while the tail is still small and when no
// candidate validates.
func (s *streamingMarkdown) relaxedBoundary(content string, after int) int {
	if len(content)-after <= relaxBoundaryAfter {
		return -1
	}
	tried := 0
	for p := newlineBefore(content, len(content)); p > after; p = newlineBefore(content, p-1) {
		if s.isSafeBoundaryIncremental(content, p) {
			return p
		}
		if tried++; tried >= relaxedBoundaryCandidates {
			break
		}
	}
	return -1
}

// newlineBefore returns the byte offset of the first character AFTER the
// latest newline that ends strictly before `until`, or -1 when there is none.
func newlineBefore(content string, until int) int {
	if until <= 0 {
		return -1
	}
	nl := strings.LastIndexByte(content[:until], '\n')
	if nl < 0 {
		return -1
	}
	return nl + 1
}

// isSafeBoundaryIncremental validates a boundary candidate at position p using
// the cached cumulative state plus a delta scan of content[stablePrefix:p],
// avoiding the O(n) re-scan of the full prefix on every candidate.
func (s *streamingMarkdown) isSafeBoundaryIncremental(content string, p int) bool {
	delta := content[len(s.stablePrefix):p]

	// Fence parity: base count + delta count must be even.
	if (s.baseFenceCount+countFenceLines(delta))%2 != 0 {
		return false
	}

	// HTML and link-ref hazards anywhere in the delta.
	if deltaHasHTMLorRef(delta) {
		return false
	}

	// List hazard: if a list marker exists anywhere before the boundary (base
	// OR delta), the last non-blank line before it must not be an indented
	// continuation paragraph (a loose-list continuation would keep the list
	// open across the cut).
	hasListMarker := s.baseHasListMarker || chunkHasListMarker(delta)
	if hasListMarker {
		lastLine := lastNonBlankLine(content[:p])
		if lastLine != "" && !isListItemMarker(strings.TrimLeft(lastLine, " \t")) {
			if lastLine[0] == ' ' || lastLine[0] == '\t' {
				return false
			}
		}
	}

	// The last non-blank line must not open a construct.
	lastLine := lastNonBlankLine(content[:p])
	if lastLine != "" && lineOpensConstruct(lastLine) {
		return false
	}

	// Setext underline check: if the line immediately after the boundary looks
	// like a setext underline, rendering the prefix as a paragraph would change
	// once the underline arrived.
	if rest := content[p:]; rest != "" {
		first := firstNonBlankLine(rest)
		if isSetextUnderlineCandidate(first) {
			return false
		}
	}

	return true
}

// deltaHasHTMLorRef reports whether the delta (text between the stable prefix
// and a boundary candidate) contains an HTML block opener or a link reference
// definition.
func deltaHasHTMLorRef(delta string) bool {
	inFence := false
	for line := range splitLines(delta) {
		if isFenceLine(line) {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		if isHTMLBlockOpener(line) || isLinkRefDefinition(line) {
			return true
		}
	}
	return false
}

// chunkHasListMarker reports whether any line in chunk is a list-item marker
// (outside fenced code blocks).
func chunkHasListMarker(chunk string) bool {
	inFence := false
	for line := range splitLines(chunk) {
		if isFenceLine(line) {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		if isListItemMarker(strings.TrimLeft(line, " \t")) {
			return true
		}
	}
	return false
}

// renderTrailing renders a trailing partial as a fresh glamour document and
// trims the surrounding whitespace so it can be glued to a cached prefix
// render without doubled blank lines.
func (s *streamingMarkdown) renderTrailing(text string, r *glamour.TermRenderer) string {
	if text == "" {
		return ""
	}
	out, err := r.Render(text)
	if err != nil {
		return text
	}
	return trimGlamourMargins(out)
}

// glueRenders concatenates two glamour-rendered fragments with a single blank
// line separator. Glamour outputs carry their own surrounding margins; trimming
// on both sides and gluing with "\n\n" prevents the visible double-margin seam.
// Empty fragments are tolerated so the same helper works for the
// boundary==len(content) path where there is no trailing segment.
func glueRenders(prefix, trail string) string {
	prefix = trimGlamourMargins(prefix)
	trail = trimGlamourMargins(trail)
	switch {
	case prefix == "" && trail == "":
		return ""
	case prefix == "":
		return trail
	case trail == "":
		return prefix
	default:
		return prefix + "\n\n" + trail
	}
}

// trimGlamourMargins strips leading and trailing whitespace (including
// newlines) from a glamour-rendered fragment: glamour adds a leading blank
// line for documents that open with a heading or paragraph, plus a trailing
// newline, and both must go before concatenation.
func trimGlamourMargins(s string) string {
	return strings.Trim(s, " \t\n")
}

// findSafeMarkdownBoundary returns the byte offset of the END of the latest
// safe boundary in content, i.e. the offset such that content[:boundary] is a
// valid stable-prefix candidate. The returned offset always points immediately
// after a blank-line separator, so concatenating a fresh render of
// content[boundary:] to a cached render of content[:boundary:] does not require
// glamour to share state across the cut.
//
// Returns -1 when no safe boundary exists. SAFETY FIRST: any time we have the
// slightest doubt we return -1 and let the caller fall back to a full render.
//
// Decision tree, in order of preference (latest boundary wins):
//
//  1. Walk backward through every blank-line position p.
//  2. Reject unless content[:p] has an even number of fence lines (no open
//     fenced block) — an odd count would cut inside a fence and mis-highlight
//     the trailing partial.
//     2b. Reject if any line in content[:p] (outside fences) opens an HTML
//     block or a link reference definition, or if a list is potentially open
//     (see prefixHasOpenHazard).
//  3. Reject if the last non-blank line of content[:p] opens a construct
//     (list marker, table pipe, block quote, setext underline, indented code).
//  4. Reject if the first non-blank line AFTER the boundary looks like a
//     setext underline — rendering the prefix as a paragraph would change once
//     the underline arrived.
func findSafeMarkdownBoundary(content string) int {
	if len(content) == 0 {
		return -1
	}
	for p := blankLineBefore(content, len(content)); p > 0; p = blankLineBefore(content, p-1) {
		if isSafeBoundaryAt(content, p) {
			return p
		}
	}
	return -1
}

// blankLineBefore returns the byte offset of the first character AFTER the
// latest blank-line separator that ends strictly before `until`. A blank-line
// separator is "\n([ \t]*\n)+" — one newline, then one or more lines holding
// only spaces or tabs, terminated by another newline. Returns -1 when none
// exists before `until`.
func blankLineBefore(content string, until int) int {
	if until <= 0 {
		return -1
	}
	end := until
	for end > 0 {
		nl := strings.LastIndexByte(content[:end], '\n')
		if nl < 0 {
			return -1
		}
		prev := strings.LastIndexByte(content[:nl], '\n')
		for prev >= 0 {
			if isBlankOrSpaces(content[prev+1 : nl]) {
				return nl + 1
			}
			break
		}
		end = nl
	}
	return -1
}

// isBlankOrSpaces reports whether s consists entirely of spaces and tabs (or
// is empty).
func isBlankOrSpaces(s string) bool {
	for i := range len(s) {
		if s[i] != ' ' && s[i] != '\t' {
			return false
		}
	}
	return true
}

// isSafeBoundaryAt reports whether content[:p] is a safe stable prefix. p must
// be a blank-line boundary (start of a line, blank line immediately before).
func isSafeBoundaryAt(content string, p int) bool {
	prefix := content[:p]

	// Even number of fence lines: no open fenced block.
	if countFenceLines(prefix)%2 != 0 {
		return false
	}

	// Anywhere-in-prefix hazards: open list (B1), HTML block opener (B2),
	// link reference definition (B3).
	if prefixHasOpenHazard(prefix) {
		return false
	}

	// The last non-blank line of the prefix must not open a construct.
	lastLine := lastNonBlankLine(prefix)
	if lastLine != "" && lineOpensConstruct(lastLine) {
		return false
	}

	// Whatever follows must not retroactively change the prefix's render
	// (setext underline turning the last paragraph into a header).
	if rest := content[p:]; rest != "" {
		first := firstNonBlankLine(rest)
		if isSetextUnderlineCandidate(first) {
			return false
		}
	}

	return true
}

// prefixHasOpenHazard reports whether prefix contains any of three constructs
// that cannot be safely cut at a blank-line boundary even when the immediately
// preceding line looks fine.
//
//	B1 (loose lists, refined in crush CHARM-1785). A loose list has a blank
//	   line between an item and a continuation paragraph that begins with
//	   indentation but no list marker. If a candidate boundary lands on that
//	   blank line, the prefix's trailing non-blank line is the continuation
//	   paragraph, NOT a list marker, so the last-line check would accept it
//	   even though the list is still open.
//
//	   Rule chosen: a list is potentially open only when a marker appeared
//	   earlier AND the last non-blank line is indented but is not itself a
//	   list marker. A non-indented last line means the list was closed by the
//	   blank line before it, and the boundary after that line is safe. (The
//	   original "any list marker anywhere forces a reject" rule killed the
//	   streaming cache for every document that ever contained a list — the
//	   dominant case for LLM output.)
//
//	B2 (HTML blocks). CommonMark defines seven HTML-block opener patterns. If
//	   the prefix opens an HTML block that the suffix closes, splitting renders
//	   the prefix as raw HTML and the suffix as prose.
//
//	   Rule chosen: any HTML-block opener anywhere in the prefix forces a
//	   reject. Typical assistant output contains no raw HTML, so the perf cost
//	   is zero in the common case.
//
//	B3 (link reference definitions). A "[label]: url" line defines a reference
//	   the suffix may later use; each half renders as an independent glamour
//	   document, so splitting loses the definition.
//
//	   Rule chosen: any reference-link definition line anywhere in the prefix
//	   forces a reject. Suffix-side reference detection is fragile (three
//	   syntaxes), so the prefix-side check is the simpler safe choice.
func prefixHasOpenHazard(prefix string) bool {
	inFence := false
	hasListMarker := false
	var lastNonBlankTrimmed string
	var lastNonBlankRaw string
	for line := range splitLines(prefix) {
		// Track fenced state so list/html/ref patterns inside a fenced code
		// block do not falsely trigger the hazards.
		if isFenceLine(line) {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		trimmed := strings.TrimLeft(line, " \t")
		if trimmed == "" {
			continue
		}
		lastNonBlankTrimmed = trimmed
		lastNonBlankRaw = line
		if isListItemMarker(trimmed) {
			hasListMarker = true
		}
		if isHTMLBlockOpener(line) {
			return true
		}
		if isLinkRefDefinition(line) {
			return true
		}
	}
	// B1 (refined): see doc comment.
	if hasListMarker && lastNonBlankTrimmed != "" && !isListItemMarker(lastNonBlankTrimmed) {
		if lastNonBlankRaw[0] == ' ' || lastNonBlankRaw[0] == '\t' {
			return true
		}
	}
	return false
}

// countFenceLines counts lines that begin or end a fenced code block in the
// CommonMark sense: a line whose first non-whitespace run is at least three
// consecutive backticks or tildes. Each such line toggles the fenced state, so
// an even count means every opened fence has been closed. Info-strings are not
// parsed; a closing fence is just any line whose first non-whitespace run is
// >=3 of the same fence char.
func countFenceLines(s string) int {
	n := 0
	for line := range splitLines(s) {
		if isFenceLine(line) {
			n++
		}
	}
	return n
}

// isFenceLine reports whether line opens or closes a fenced code block.
func isFenceLine(line string) bool {
	i := 0
	for i < len(line) && i < 3 && line[i] == ' ' {
		i++
	}
	if i >= len(line) {
		return false
	}
	c := line[i]
	if c != '`' && c != '~' {
		return false
	}
	run := 0
	for i < len(line) && line[i] == c {
		i++
		run++
	}
	return run >= 3
}

// lastNonBlankLine returns the last non-blank line of s, or "" when every line
// is blank.
func lastNonBlankLine(s string) string {
	last := ""
	for line := range splitLines(s) {
		if strings.TrimSpace(line) != "" {
			last = line
		}
	}
	return last
}

// firstNonBlankLine returns the first non-blank line of s, or "" when every
// line is blank.
func firstNonBlankLine(s string) string {
	for line := range splitLines(s) {
		if strings.TrimSpace(line) != "" {
			return line
		}
	}
	return ""
}

// splitLines yields the lines of s without their terminators. The final
// segment is yielded even if not newline-terminated.
func splitLines(s string) func(yield func(string) bool) {
	return func(yield func(string) bool) {
		start := 0
		for i := 0; i < len(s); i++ {
			if s[i] == '\n' {
				if !yield(s[start:i]) {
					return
				}
				start = i + 1
			}
		}
		if start <= len(s)-1 {
			yield(s[start:])
		}
	}
}

// lineOpensConstruct reports whether line keeps a markdown construct open
// across the boundary. Conservative: anything that smells like
// list/table/quote/setext/indented-code returns true.
func lineOpensConstruct(line string) bool {
	// Indented code: a tab, or 4+ leading spaces.
	if len(line) > 0 && (line[0] == '\t' || strings.HasPrefix(line, "    ")) {
		return true
	}

	trimmed := strings.TrimLeft(line, " \t")
	if trimmed == "" {
		return false
	}

	// Block quote.
	if trimmed[0] == '>' {
		return true
	}

	// List item.
	if isListItemMarker(trimmed) {
		return true
	}

	// Table: any pipe anywhere in the line. Pipe-in-prose is rare and the cost
	// of bailing is one slow frame.
	if strings.ContainsRune(line, '|') {
		return true
	}

	// Setext underline as the LAST line of the prefix: the boundary would sit
	// in the middle of a header.
	return isSetextUnderlineCandidate(trimmed)
}

// isListItemMarker reports whether line (already left-trimmed) starts with a
// CommonMark list-item marker followed by a space or tab.
func isListItemMarker(line string) bool {
	if line == "" {
		return false
	}
	c := line[0]
	if c == '-' || c == '*' || c == '+' {
		return len(line) >= 2 && (line[1] == ' ' || line[1] == '\t')
	}
	// Ordered list: digits followed by '.' or ')' and a space.
	i := 0
	for i < len(line) && line[i] >= '0' && line[i] <= '9' {
		i++
	}
	if i == 0 || i > 9 {
		return false
	}
	if i >= len(line) || (line[i] != '.' && line[i] != ')') {
		return false
	}
	if i+1 >= len(line) {
		return false
	}
	return line[i+1] == ' ' || line[i+1] == '\t'
}

// isSetextUnderlineCandidate reports whether line (with optional leading
// whitespace) consists entirely of '=' or entirely of '-' characters with
// optional trailing whitespace. CommonMark requires no leading whitespace on
// the underline; up to three spaces are accepted for safety so an indented
// underline still blocks a split.
func isSetextUnderlineCandidate(line string) bool {
	i := 0
	for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
		i++
	}
	if i == len(line) {
		return false
	}
	c := line[i]
	if c != '=' && c != '-' {
		return false
	}
	j := i
	for j < len(line) && line[j] == c {
		j++
	}
	for j < len(line) {
		if line[j] != ' ' && line[j] != '\t' {
			return false
		}
		j++
	}
	// "-" alone is also a list marker without a trailing space; the list-item
	// check covers that case before we get here.
	return j-i >= 1
}

// isHTMLBlockOpener reports whether line begins one of the seven CommonMark
// HTML block patterns. Matching is intentionally loose — we only need to know
// the line "looks like an HTML block start", not parse the markup.
func isHTMLBlockOpener(line string) bool {
	i := 0
	for i < len(line) && i < 3 && line[i] == ' ' {
		i++
	}
	rest := line[i:]
	if len(rest) < 2 || rest[0] != '<' {
		return false
	}

	// Type 2: comment "<!--". Type 3: processing instruction "<?".
	// Type 5: CDATA "<![CDATA[". Type 4: declaration "<!" + ASCII letter.
	if strings.HasPrefix(rest, "<!--") || strings.HasPrefix(rest, "<?") ||
		strings.HasPrefix(rest, "<![CDATA[") {
		return true
	}
	if len(rest) >= 3 && rest[1] == '!' && isASCIILetter(rest[2]) {
		return true
	}

	// Type 1: <script | <pre | <style | <textarea (case-insensitive) followed
	// by whitespace, '>', or end-of-line.
	low := strings.ToLower(rest)
	for _, t := range []string{"<script", "<pre", "<style", "<textarea"} {
		if strings.HasPrefix(low, t) {
			next := byte(0)
			if len(low) > len(t) {
				next = low[len(t)]
			}
			if next == 0 || next == ' ' || next == '\t' || next == '>' {
				return true
			}
		}
	}

	// Types 6 & 7: open or close of a block-level tag, collapsed into one
	// check — the line must start (after up to 3 spaces) with '<' or '</'
	// followed by an ASCII letter. "<3", "<-", "<<", or mid-line "<foo>" do
	// NOT trigger.
	j := 1
	if j < len(rest) && rest[j] == '/' {
		j++
	}
	if j >= len(rest) || !isASCIILetter(rest[j]) {
		return false
	}
	return true
}

// isASCIILetter reports whether b is an ASCII letter.
func isASCIILetter(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// isLinkRefDefinition reports whether line matches a CommonMark link reference
// definition opener:
//
//	^[ ]{0,3}\[[^\]]+\]:\s*\S+
//
// The destination is not validated — presence of a ref-def opener anywhere in
// the prefix is enough to forfeit the boundary.
func isLinkRefDefinition(line string) bool {
	i := 0
	for i < len(line) && i < 3 && line[i] == ' ' {
		i++
	}
	if i >= len(line) || line[i] != '[' {
		return false
	}
	i++
	labelStart := i
	for i < len(line) && line[i] != ']' {
		i++
	}
	if i >= len(line) || i == labelStart {
		return false
	}
	i++ // ']'
	if i >= len(line) || line[i] != ':' {
		return false
	}
	i++
	for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
		i++
	}
	return i < len(line)
}
