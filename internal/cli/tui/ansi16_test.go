package tui

import (
	"image/color"
	"strconv"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

// This file covers the T2.3 tool-output filter: RemapANSI16 rewrites basic
// 16-color SGR codes to theme truecolor, StripCursorControl removes cursor /
// screen-control sequences and simulates \r overwrites, and the model applies
// both to tool result text before it reaches a card's response tree. The
// narrow-width card degradation (spec §3.3) is covered too.

// rgb is a test palette color that records its components so rewrites can be
// asserted byte-exactly.
type rgb struct{ r, g, b uint8 }

func (c rgb) RGBA() (uint32, uint32, uint32, uint32) {
	return uint32(c.r)<<8 | uint32(c.r), uint32(c.g)<<8 | uint32(c.g), uint32(c.b)<<8 | uint32(c.b), 0xffff
}

// testPalette is a 16-slot palette with one distinguishable color per slot:
// slot i is RGB (i, 100+i, 200) — never a real terminal default.
func testPalette() [16]color.Color {
	var p [16]color.Color
	for i := range p {
		p[i] = rgb{uint8(i), uint8(100 + i), 200}
	}
	return p
}

// slotTruecolor renders the "38;2;r;g;b" / "48;2;r;g;b" body a slot's color
// rewrites to, so assertions stay byte-exact.
func slotTruecolor(introducer int, c rgb) string {
	return strconv.Itoa(introducer) + ";2;" + strconv.Itoa(int(c.r)) + ";" +
		strconv.Itoa(int(c.g)) + ";" + strconv.Itoa(int(c.b))
}

// TestRemapANSI16BasicColors walks the four SGR color families (30-37,
// 90-97, 40-47, 100-107) and asserts each maps onto its palette slot as
// truecolor while trailing text survives.
func TestRemapANSI16BasicColors(t *testing.T) {
	pal := testPalette()
	cases := []struct {
		in         string
		introducer int
		slot       int
	}{
		{"\x1b[31mx", 38, 1},   // red foreground
		{"\x1b[0;35mx", 38, 5}, // magenta mixed with a reset
		{"\x1b[92mx", 38, 10},  // bright green foreground
		{"\x1b[44mx", 48, 4},   // blue background
		{"\x1b[105mx", 48, 13}, // bright magenta background
	}
	for _, tc := range cases {
		got := RemapANSI16(tc.in, pal)
		want := "\x1b[" + slotTruecolor(tc.introducer, pal[tc.slot].(rgb)) + "mx"
		// The "\x1b[0;35m" case keeps its leading reset param and only
		// rewrites the color slot, so its expectation gets the 0; prefix.
		if strings.HasPrefix(tc.in, "\x1b[0;") {
			want = "\x1b[0;" + slotTruecolor(tc.introducer, pal[tc.slot].(rgb)) + "mx"
		}
		if got != want {
			t.Errorf("RemapANSI16(%q) = %q, want %q", tc.in, got, want)
		}
	}
}

// TestRemapANSI16ExtendedPassthrough verifies extended colors (256 and
// truecolor), attributes and default-color resets survive untouched.
func TestRemapANSI16ExtendedPassthrough(t *testing.T) {
	pal := testPalette()
	for _, s := range []string{
		"\x1b[38;5;9m",        // 256-color foreground
		"\x1b[48;5;200m",      // 256-color background
		"\x1b[38;2;10;20;30m", // truecolor foreground
		"\x1b[48;2;1;2;3m",    // truecolor background
		"\x1b[1;4m",           // bold + underline
		"\x1b[39;49m",         // default fg/bg resets
		"\x1b[0m",             // full reset
	} {
		if got := RemapANSI16(s, pal); got != s {
			t.Errorf("RemapANSI16(%q) = %q, want passthrough", s, got)
		}
	}
}

// TestRemapANSI16MixedSequence rewrites only the 16-color param inside a
// sequence that also carries attributes and an extended color, keeping the
// extended color's argument run intact.
func TestRemapANSI16MixedSequence(t *testing.T) {
	pal := testPalette()
	got := RemapANSI16("\x1b[1;31;38;5;9;4m", pal)
	// Bold kept, red rewritten to slot-1 truecolor, 256-color intact,
	// underline kept.
	want := "\x1b[1;" + slotTruecolor(38, pal[1].(rgb)) + ";38;5;9;4m"
	if got != want {
		t.Errorf("RemapANSI16 mixed = %q, want %q", got, want)
	}
}

// TestRemapANSI16PlainAndNilPalette: ESC-free text is returned as-is, and a
// nil palette slot emits the bare introducer (terminal default applies).
func TestRemapANSI16PlainAndNilPalette(t *testing.T) {
	if got := RemapANSI16("no escapes here", testPalette()); got != "no escapes here" {
		t.Errorf("plain text rewritten: %q", got)
	}
	var empty [16]color.Color
	empty[1] = rgb{1, 2, 3}
	if got := RemapANSI16("\x1b[31mx\x1b[32my", empty); got != "\x1b["+slotTruecolor(38, empty[1].(rgb))+"mx\x1b[38my" {
		t.Errorf("nil slot = %q, want bare 38 introducer for green", got)
	}
}

// TestStripCursorControlTable walks the strip table: SGR and OSC hyperlinks
// survive; cursor movement, erase, scroll, mode set/reset and DEC
// save/restore are removed; unknown CSI passes through.
func TestStripCursorControlTable(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"a\x1b[1;32mb", "a\x1b[1;32mb"},   // SGR kept
		{"a\x1b]8;;http://x\x1b\\link\x1b]8;;\x1b\\b", "a\x1b]8;;http://x\x1b\\link\x1b]8;;\x1b\\b"}, // OSC hyperlink kept verbatim
		{"up\x1b[2A", "up"},                                    // cursor up
		{"clr\x1b[K", "clr"},                                   // erase line
		{"scr\x1b[2J", "scr"},                                  // erase display
		{"scroll\x1b[1S", "scroll"},                            // scroll up
		{"mode\x1b[?25l", "mode"},                              // DEC private reset
		{"save\x1b7", "save"},                                  // DEC save cursor
		{"rstr\x1b8", "rstr"},                                  // DEC restore cursor
		{"mov\x1b[3;4H", "mov"},                                // cursor position
		{"unk\x1b[9999X", "unk\x1b[9999X"},                     // unknown CSI passes through
	}
	for _, tc := range cases {
		if got := StripCursorControl(tc.in); got != tc.want {
			t.Errorf("StripCursorControl(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestStripCursorControlCarriageReturn: within a line the text after the last
// \r wins (progress-bar overwrite), while other lines are untouched.
func TestStripCursorControlCarriageReturn(t *testing.T) {
	in := "downloading\n  10%\r  50%\r 100% done\nnext"
	want := "downloading\n 100% done\nnext"
	if got := StripCursorControl(in); got != want {
		t.Errorf("StripCursorControl = %q, want %q", got, want)
	}
}

// themeSlotRGBA resolves one of the theme's 256-cube palette constants to its
// 16-bit RGBA components for palette-slot assertions.
func themeSlotRGBA(name string) (uint32, uint32, uint32) {
	r, g, b, _ := lipgloss.Color(name).RGBA()
	return r, g, b
}

// TestThemeANSIPalette: DefaultTheme carries a fully populated 16-slot
// palette and the primary slots map onto the theme's own colors.
func TestThemeANSIPalette(t *testing.T) {
	th := DefaultTheme()
	for i, c := range th.ANSI {
		if c == nil {
			t.Fatalf("ANSI[%d] is nil; every slot must map to a theme color", i)
		}
	}
	// Slot 1 (red) must be the theme error color, slot 2 (green) the success
	// color: raw tool output should land on the same colors the chrome uses.
	// Black (slot 0) must not be a literal black — invisible on the dark
	// background — so it maps to the recessive track gray.
	for slot, name := range map[int]string{0: colorTrack, 1: colorError, 2: colorSuccess, 3: colorWarn, 4: colorAccent} {
		wantR, wantG, wantB := themeSlotRGBA(name)
		gotR, gotG, gotB, _ := th.ANSI[slot].RGBA()
		if gotR != wantR || gotG != wantG || gotB != wantB {
			t.Errorf("ANSI[%d] = %v, want theme color %s", slot, th.ANSI[slot], name)
		}
	}
}

// TestModelToolResultFiltered drives a tool end carrying 16-color output and
// a progress bar, and asserts the card's response holds the normalized text:
// cursor control stripped, \r overwritten, 16-color SGR rewritten to theme
// truecolor.
func TestModelToolResultFiltered(t *testing.T) {
	m := NewModel(Options{})
	next, _ := m.Update(toolStartMsg{id: "t1", name: "bash"})
	mm := next.(Model)
	raw := "\x1b[32m ok \x1b[0m\n10%\r100%"
	next, _ = mm.Update(toolEndMsg{id: "t1", ok: true, result: raw})
	mm = next.(Model)

	var joined string
	for _, n := range mm.toolCards["t1"].response {
		joined += n.text + "\n"
	}
	if strings.Contains(joined, "\r") {
		t.Errorf("response still contains \\r: %q", joined)
	}
	if strings.Contains(joined, "\x1b[32m") {
		t.Errorf("response still contains the raw 16-color code: %q", joined)
	}
	if !strings.Contains(joined, "\x1b[38;2;") {
		t.Errorf("response missing the theme truecolor rewrite: %q", joined)
	}
	if !strings.Contains(joined, " ok ") || !strings.Contains(joined, "100%") {
		t.Errorf("response lost content: %q", joined)
	}
}

// TestToolCardNarrowDegradation: below narrowCardWidth the card renders flat
// (no border, no Input arguments) but keeps header and response; at the
// threshold and above the full bordered layout is used.
func TestToolCardNarrowDegradation(t *testing.T) {
	theme := DefaultTheme()
	card := toolCard{
		name:     "bash",
		input:    map[string]any{"command": "go test ./..."},
		response: parseToolResult("PASS"),
		state:    cardSuccess,
	}

	narrow := card.render(theme, narrowCardWidth-1)
	if strings.ContainsRune(narrow, '╭') || strings.ContainsRune(narrow, '│') {
		t.Errorf("narrow render should drop the border:\n%s", narrow)
	}
	if strings.Contains(narrow, "Input arguments") {
		t.Errorf("narrow render should omit the Input arguments section:\n%s", narrow)
	}
	if !strings.Contains(narrow, "bash") || !strings.Contains(narrow, "PASS") {
		t.Errorf("narrow render lost header/response:\n%s", narrow)
	}

	full := card.render(theme, narrowCardWidth)
	if !strings.ContainsRune(full, '╭') {
		t.Errorf("full render should keep the border:\n%s", full)
	}
	if !strings.Contains(full, "Input arguments") {
		t.Errorf("full render should keep the Input arguments section:\n%s", full)
	}
}
