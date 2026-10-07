package tui

import (
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/smallnest/pigo/internal/cli/ui"
	"github.com/smallnest/pigo/internal/provider"
	"github.com/smallnest/pigo/internal/selfupdate"
)

// This file builds the startup splash shown at the top of the transcript, in
// the grok welcome-screen style (views/welcome: hero box + menu): the braille
// logo in resting gray — grok paints its logo gray with a subtle shimmer, not
// a rainbow, and the shimmer is not reproduced on a scrolling transcript block
// — beside a grok-menu information column: a bold version badge with a dim
// subtitle, then `Label ··· value` rows with dot leaders, then a dim tip line
// for first-run discoverability. It is seeded once by withSession so it
// scrolls up as the conversation grows, like a shell's login banner.

// logoLines is the pigo braille-art logo, one string per row.
var logoLines = []string{
	"⠀⠀⠀⠀⠀⢄⢤⣤⣼⣼⣼⣼⣼⣼⣼⣼⣼⣼⣼⣼⣼⣼⣤⣄⢄⠀⠀⠀⠀⠀",
	"⠀⠀⢀⣴⣾⣿⣿⡻⡻⡛⠛⠛⠛⠛⠛⠛⠛⠛⠛⠛⠻⡻⡻⣿⣿⣽⣬⠄⠀⠀",
	"⠀⢠⣾⡿⠛⠃⠀⣀⡀⠀⠀⠀⠀⢠⣤⣤⣤⠀⠀⠀⠀⠀⣤⣤⣧⣿⣻⣽⡄⠀",
	"⠀⣾⡟⠁⠀⣠⣾⣿⡇⠀⠀⠀⠀⣿⣿⣿⣿⠀⠀⠀⠀⠀⣿⣿⣿⣿⠂⢻⣽⠀",
	"⢰⣿⠀⠀⣾⣿⣿⣿⡇⠀⠀⠀⠀⣿⣿⣿⣿⠀⠀⠀⠀⠀⣿⣿⣿⣿⠀⠀⣿⡌",
	"⢲⣿⠀⠀⣿⣿⣿⣿⠀⠀⠀⠀⢀⣿⣿⣿⣿⠀⠀⠀⠀⠀⣿⣿⣿⣿⠀⠀⣿⡍",
	"⢲⣿⠀⠀⣿⣿⣿⡟⠀⠀⠀⠀⢸⣿⣿⣿⣿⠀⠀⠀⠀⠀⣿⣿⣿⣿⠀⠀⣿⡍",
	"⢲⣿⠀⠀⣿⣿⣿⠇⠀⠀⠀⠀⣾⣿⣿⣿⣿⠀⠀⠀⠀⠀⣿⣿⣿⣿⠀⠀⣿⡍",
	"⠲⣿⠀⠀⣿⣿⣿⡟⠀⠀⠀⠀⢠⣿⣿⣿⣿⣿⠀⠀⠀⠀⣿⣿⣿⡿⠀⠀⣿⡉",
	"⠀⣻⣭⠄⣿⡿⠁⠀⠀⠀⠀⣾⣿⣿⣿⣿⣿⠀⠀⠀⠀⠀⣿⣿⡟⠀⢀⣶⡿⠀",
	"⠀⠒⣻⣽⣿⢅⠀⠀⠀⠀⣸⣿⣿⣿⣿⣿⣿⡀⠀⠀⠀⠀⠛⠛⢅⣤⣾⡿⠉⠀",
	"⠀⠀⠂⠻⣻⣿⣿⣼⣼⣬⣤⣤⣤⣤⣤⣤⣤⣤⣤⣤⣴⣼⣼⣿⣿⡿⡛⠁⠀⠀",
	"⠀⠀⠀⠀⠀⠃⠓⠛⡻⡻⡻⡻⡻⡻⡻⡻⡻⡻⡻⡻⡻⡻⠛⠋⠃⠀⠀⠀⠀⠀",
}

// renderBannerTip paints the first-run hint line under the info rows in the
// grok welcome hint grammar: the key bright bold, two spaces, the dim action
// label, then four spaces before the next pair (views/welcome mod.rs hint
// slot). The keys row at the bottom of the shell owns the live key hints, so
// the banner keeps only the two essentials and stays within an 80-column
// terminal (the banner block renders verbatim, no reflow).
func renderBannerTip(theme Theme) string {
	return theme.KeyHint.Render("Enter") + theme.Chrome.Render("  发送") +
		"    " +
		theme.KeyHint.Render("/help") + theme.Chrome.Render("  全部命令")
}

// centerLines centers a pre-rendered block in the given width by prepending a
// uniform left margin — the banner never reflows, so whole-block centering is
// the honest equivalent of grok's Alignment::Center welcome column. The pad is
// measured on display width (ANSI escapes stripped) and dropped when the
// terminal is narrower than the block's widest line, keeping narrow windows
// left-aligned instead of clipped.
func centerLines(block string, width int) string {
	if width <= 0 {
		return block
	}
	lines := strings.Split(block, "\n")
	widest := 0
	for _, l := range lines {
		if w := ui.Width(l); w > widest {
			widest = w
		}
	}
	pad := (width - widest) / 2
	if pad <= 0 {
		return block
	}
	margin := strings.Repeat(" ", pad)
	for i, l := range lines {
		lines[i] = margin + l
	}
	return strings.Join(lines, "\n")
}

// renderBanner paints the logo in resting gray and joins it with the grok-menu
// information column. Its only I/O is a single cheap read of the local
// update-check cache (no network — CachedLatest); it never panics, so it is
// safe to build eagerly at startup.
func renderBanner(theme Theme, opts Options, cwd string) string {
	logoStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(colorGray))
	var logo strings.Builder
	for i, line := range logoLines {
		if i > 0 {
			logo.WriteByte('\n')
		}
		logo.WriteString(logoStyle.Render(line))
	}

	// Version badge row: bold "pigo" + dim version (grok hero version slot);
	// a cached newer release appends an amber upgrade pointer.
	badge := theme.KeyHint.Render("pigo")
	ver := firstNonEmpty(opts.Version, "dev")
	if latest, _ := selfupdate.CachedLatest(); latest != "" {
		if avail, comparable := selfupdate.UpdateAvailable(opts.Version, latest); comparable && avail {
			ver += "  →  " + lipgloss.NewStyle().
				Foreground(lipgloss.Color(colorVerbEdt)).Bold(true).Render(latest)
		}
	}
	head := badge + theme.Chrome.Render("  "+ver) + "\n" +
		theme.Chrome.Render("Terminal AI coding assistant")

	// Information rows in the grok menu grammar: dim label, dotted leader,
	// bright value.
	label := theme.Chrome
	value := theme.User
	rows := [][2]string{
		{"Model", firstNonEmpty(opts.Model, "—")},
		{"Provider", firstNonEmpty(opts.ProviderName, "—")},
		{"Protocol", firstNonEmpty(provider.ProtocolLabel(opts.Protocol), "—")},
		{"Thinking", firstNonEmpty(string(opts.ThinkingLevel), "off")},
		{"Directory", firstNonEmpty(cwd, "—")},
	}

	const rowWidth = 34 // label + leader + value layout width inside the column
	var info strings.Builder
	info.WriteString(head)
	for _, r := range rows {
		info.WriteString("\n" + menuRow(label, value, rowWidth, r[0], r[1]))
	}
	// Upgrade affordance keeps its own highlighted line under the rows.
	if latest, _ := selfupdate.CachedLatest(); latest != "" {
		if avail, comparable := selfupdate.UpdateAvailable(opts.Version, latest); comparable && avail {
			info.WriteString("\n" + lipgloss.NewStyle().
				Foreground(lipgloss.Color(colorVerbEdt)).
				Render("Run pigo update to upgrade"))
		}
	}
	info.WriteString("\n" + renderBannerTip(theme))

	return lipgloss.JoinHorizontal(lipgloss.Center, logo.String(), "   ", info.String())
}

// menuRow renders one "Label ··· value" row: the label dim, a dot leader
// filling the row width, and the value bright (grok welcome menu rows).
func menuRow(labelStyle, valueStyle lipgloss.Style, width int, label, value string) string {
	head := label + " "
	vw := ui.Width(value)
	lw := ui.Width(label)
	dots := width - lw - vw - 2
	if dots < 3 {
		dots = 3
	}
	leader := strings.Repeat("·", dots) + " "
	// Width math runs on the plain text; styling is applied per segment.
	return labelStyle.Render(head) + lipgloss.NewStyle().
		Foreground(lipgloss.Color(colorTrack)).Render(leader) +
		valueStyle.Render(value)
}

// firstNonEmpty returns s when it is non-empty, otherwise the fallback.
func firstNonEmpty(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}
