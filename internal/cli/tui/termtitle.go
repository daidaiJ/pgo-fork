package tui

import (
	"fmt"
	"io"
	"os"
	"strings"
	"unicode"

	"github.com/smallnest/pigo/internal/agentcore"
)

// Session display title + terminal window title (T7.3 S2). The title chain
// and OSC plumbing are ported from grok (xai-grok-pager views/session_title.rs
// entry_title, app/mod.rs set_terminal_title) and qwen-code
// (ui/utils/windowTitle.ts); deviations are registered in
// wiki/port/tui-slash-ux.md §9.

const (
	// maxTitleRunes caps a /rename title (grok MAX_TITLE_SCALARS, persistence.rs:54).
	maxTitleRunes = 100
	// maxTitleChars is the first-prompt auto-title cap (grok MAX_TITLE_CHARS).
	maxTitleChars = 60
	// maxTerminalTitleRunes caps the composed OSC payload (grok
	// terminal_title_string keeps the whole title within 80 chars).
	maxTerminalTitleRunes = 80
	// titleSuffix labels every terminal title with the app (grok " - grok").
	titleSuffix = " - pigo"
	// appName is the bare title when nothing else is available (grok falls
	// back to "grok").
	appName = "pigo"
	// runningTitlePrefix marks an in-flight run in the tab title (qwen
	// titleStatusPrefix Responding icon).
	runningTitlePrefix = "◐ "
)

// isForbiddenTitleChar reports whether r must not survive into a display
// title: C0/C1 controls (an embedded ESC/BEL inside an OSC sequence would
// terminate it early and let the tail inject escape codes) plus the bidi and
// directional-format runes that can spoof or visually reorder the title
// (grok is_forbidden_title_char).
func isForbiddenTitleChar(r rune) bool {
	if unicode.IsControl(r) {
		return true
	}
	switch {
	case r >= 0x202A && r <= 0x202E, // LRE/LRO/RLE/RLO/PDF
		r >= 0x2066 && r <= 0x2069, // LRI/RLI/FSI/PDI
		r == 0x200E || r == 0x200F: // LRM/RLM
		return true
	}
	return false
}

// sanitizeTitle replaces forbidden runes with U+FFFD (grok
// sanitize_display_text): the replacement marks the damage instead of
// silently dropping characters from untrusted content.
func sanitizeTitle(s string) string {
	if !strings.ContainsFunc(s, isForbiddenTitleChar) {
		return s
	}
	return strings.Map(func(r rune) rune {
		if isForbiddenTitleChar(r) {
			return '\uFFFD'
		}
		return r
	}, s)
}

// stripRenameTitle removes forbidden runes outright from a /rename argument
// (grok sanitize_rename_title). Tab is kept so the reserved "--auto\tmore"
// form still trips the sole-argument check rather than merging into one word.
func stripRenameTitle(s string) string {
	return strings.Map(func(r rune) rune {
		if r != '\t' && isForbiddenTitleChar(r) {
			return -1
		}
		return r
	}, s)
}

// truncateTitle takes the first maxTitleChars runes and appends an ellipsis
// when truncated (grok truncate_title; rune-based so multi-byte codepoints
// are not split).
func truncateTitle(text string) string {
	r := []rune(text)
	if len(r) <= maxTitleChars {
		return text
	}
	return string(r[:maxTitleChars]) + "…"
}

// firstUserPromptText resolves the auto-title tier: the first user message's
// text, newlines collapsed, capped at 60 runes. It is the S2 chain tier the
// /sessions picker shows through firstUserPrompt.
func firstUserPromptText(msgs agentcore.MessageList) string {
	for _, m := range msgs {
		u, ok := m.(agentcore.UserMessage)
		if !ok {
			continue
		}
		text := strings.TrimSpace(agentcore.ContentToText(u.Content))
		if text == "" {
			continue
		}
		return truncateTitle(strings.ReplaceAll(text, "\n", " "))
	}
	return ""
}

// sessionTitle resolves the display chain (grok entry_title): a manual rename
// wins over the first user prompt, which wins over "session <id8>".
func sessionTitle(manual, firstPrompt, id string) string {
	if t := strings.TrimSpace(manual); t != "" {
		return t
	}
	if t := strings.TrimSpace(firstPrompt); t != "" {
		return t
	}
	return "session " + shortID(id)
}

// terminalTitle composes the OSC payload: the base capped so the whole title
// stays within maxTerminalTitleRunes, with the app suffix appended (grok
// terminal_title_string); an empty base degrades to the bare app name.
func terminalTitle(base string) string {
	base = strings.TrimSpace(sanitizeTitle(base))
	if base == "" {
		return appName
	}
	limit := maxTerminalTitleRunes - len(titleSuffix)
	if r := []rune(base); len(r) > limit {
		base = string(r[:limit])
	}
	return base + titleSuffix
}

// displayTitle resolves the live session's title through the full chain.
func (s *runSession) displayTitle() string {
	first := ""
	if s.agentCtx != nil {
		first = firstUserPromptText(s.agentCtx.Messages)
	}
	return sessionTitle(s.header.Title, first, s.header.ID)
}

// terminalWindowTitle is the tea.View.WindowTitle payload for the current
// frame. The running prefix flips with the run state; bubbletea's renderer
// dedups unchanged titles, so per-frame recomposition is free.
func (m Model) terminalWindowTitle() string {
	base := ""
	if m.session != nil {
		base = m.session.displayTitle()
	}
	if m.running {
		base = runningTitlePrefix + base
	}
	return terminalTitle(base)
}

// renameMessage applies /rename to the live session and returns the
// transcript feedback line. The grammar mirrors grok's RenameCommand:
// `--auto` resets to the auto chain and must stand alone ("--auto Something"
// is rejected), `--AUTO`/`--automatic`/`--autoSomething` are ordinary
// renames, forbidden control runes are stripped before the reserved-verb
// check, and titles beyond maxTitleRunes are refused.
func renameMessage(s *runSession, raw string) string {
	arg := strings.TrimSpace(raw)
	if arg == "" {
		// grok ghost-prefills the current title into the slash menu here;
		// pigo echoes usage with the resolved title instead (v1 deviation).
		return fmt.Sprintf("usage: /rename <title> — /rename --auto resets to the auto title (current: %s)", s.displayTitle())
	}
	stripped := strings.TrimSpace(stripRenameTitle(arg))
	switch {
	case stripped == "--auto":
		if err := s.store.SetTitle(s.header, "", false); err != nil {
			return fmt.Sprintf("(rename failed: %v)", err)
		}
		s.header.Title, s.header.TitleIsManual = "", false
		return fmt.Sprintf("title reset to auto — %s", s.displayTitle())
	case strings.HasPrefix(stripped, "--auto "), strings.HasPrefix(stripped, "--auto\t"):
		return "(rename: --auto takes no title)"
	}
	title := strings.TrimSpace(stripped)
	if title == "" {
		return "(rename: title must not be blank)"
	}
	if len([]rune(title)) > maxTitleRunes {
		return fmt.Sprintf("(rename: title too long (max %d characters))", maxTitleRunes)
	}
	if err := s.store.SetTitle(s.header, title, true); err != nil {
		return fmt.Sprintf("(rename failed: %v)", err)
	}
	s.header.Title, s.header.TitleIsManual = title, true
	return fmt.Sprintf("renamed session to %q", title)
}

// inTerminalMultiplexer reports whether the process runs under tmux/screen/
// zellij/dvtm (qwen MULTIPLEXER_ENV_KEYS): multiplexers maintain their own
// window lists, so only OSC 2 (window title) is written there.
func inTerminalMultiplexer() bool {
	for _, k := range []string{"TMUX", "STY", "ZELLIJ", "DVTM"} {
		if os.Getenv(k) != "" {
			return true
		}
	}
	return false
}

// restoreTerminalTitle re-asserts the bare app title after a graceful quit
// (grok resets the title on exit). bubbletea's renderer clears the title to
// empty when it closes, so run.go calls this after p.Run returns, writing raw
// OSC 0+2 (OSC 2 only under a multiplexer) once the renderer is fully torn
// down and direct stdout writes can no longer interleave with frames.
func restoreTerminalTitle(w io.Writer) {
	title := terminalTitle("")
	if inTerminalMultiplexer() {
		fmt.Fprintf(w, "\x1b]2;%s\x07", title)
		return
	}
	fmt.Fprintf(w, "\x1b]0;%s\x07\x1b]2;%s\x07", title, title)
}
