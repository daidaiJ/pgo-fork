package tui

import (
	"image/color"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// This file ports crush's internal/ui/common/ansi16.go (T2.3, R11 direct
// translation). Tools capture raw terminal output (bash, git, test runners)
// that emits basic 16-color SGR codes and trusts the terminal to pick the
// color; replayed inside the transcript those defaults are often illegible on
// the dark background. RemapANSI16 rewrites them to explicit truecolor from
// the theme palette (Theme.ANSI), and StripCursorControl removes the cursor /
// screen-control sequences progress bars emit, which would corrupt the render
// state if replayed verbatim. The pair is applied to tool result text before
// it is parsed into a card's response tree.

// RemapANSI16 replaces basic ANSI 16-color SGR codes with 24-bit truecolor
// from palette. Extended colors (38/48/58 with ;5;n or ;2;r;g;b sub-params)
// pass through unchanged, as do non-color attributes (bold, italic, ...) and
// default color resets (39/49/59). Strings without ESC are returned as-is.
//
// Parsing uses [ansi.DecodeSequence] (the same approach as colorprofile's
// Writer) since there is no upstream palette-remap API.
func RemapANSI16(s string, palette [16]color.Color) string {
	if !strings.ContainsRune(s, 0x1b) {
		return s
	}

	var buf strings.Builder
	buf.Grow(len(s))

	parser := ansi.GetParser()
	defer ansi.PutParser(parser)

	var state byte
	for len(s) > 0 {
		parser.Reset()
		seq, _, n, newState := ansi.DecodeSequence(s, state, parser)

		if ansi.HasCsiPrefix(seq) && parser.Command() == 'm' {
			remapSGR(parser.Params(), palette, &buf)
		} else {
			buf.WriteString(seq)
		}

		s = s[n:]
		state = newState
	}

	return buf.String()
}

// remapSGR rewrites one SGR sequence, replacing 16-color params with
// truecolor from palette. Extended-color introducers consume their
// subsequent params as arguments and are skipped whole so the argument
// run is never misread as another attribute.
func remapSGR(params ansi.Params, palette [16]color.Color, buf *strings.Builder) {
	buf.WriteString("\x1b[")

	first := true
	for i := 0; i < len(params); i++ {
		p := params[i].Param(0)

		if !first {
			buf.WriteByte(';')
		}
		first = false

		switch {
		// Extended color introducers consume subsequent params as
		// arguments. Skip them whole so they aren't misread.
		case p == 38 || p == 48 || p == 58:
			buf.WriteString(strconv.Itoa(p))
			if i+1 < len(params) {
				sub := params[i+1].Param(0)
				switch sub {
				case 5: // 256-color: 38;5;n
					buf.WriteByte(';')
					buf.WriteString(strconv.Itoa(sub))
					if i+2 < len(params) {
						buf.WriteByte(';')
						buf.WriteString(strconv.Itoa(params[i+2].Param(0)))
						i += 2
					} else {
						i++
					}
				case 2: // truecolor: 38;2;r;g;b
					buf.WriteByte(';')
					buf.WriteString(strconv.Itoa(sub))
					for j := 2; j <= 4 && i+j < len(params); j++ {
						buf.WriteByte(';')
						buf.WriteString(strconv.Itoa(params[i+j].Param(0)))
					}
					i += min(4, len(params)-i-1)
				default:
					i++
				}
			}

		case p >= 30 && p <= 37:
			writeTruecolor(buf, 38, palette[p-30])
		case p >= 90 && p <= 97:
			writeTruecolor(buf, 38, palette[8+p-90])
		case p >= 40 && p <= 47:
			writeTruecolor(buf, 48, palette[p-40])
		case p >= 100 && p <= 107:
			writeTruecolor(buf, 48, palette[8+p-100])

		default:
			buf.WriteString(strconv.Itoa(p))
		}
	}

	buf.WriteByte('m')
}

// StripCursorControl removes ANSI escape sequences that move the cursor,
// erase regions of the screen, or change terminal modes. Programs like git
// push, cargo build and npm install emit them to animate progress bars and
// status lines; replayed inside the transcript they corrupt the render
// state. SGR (color/style) sequences, OSC hyperlinks and printable text are
// preserved. Bare carriage returns are handled by simulating line-overwrite:
// within each line, text after the last \r wins, matching what a real
// terminal would display.
func StripCursorControl(s string) string {
	if !strings.ContainsRune(s, 0x1b) && !strings.ContainsRune(s, '\r') {
		return s
	}

	var buf strings.Builder
	buf.Grow(len(s))

	parser := ansi.GetParser()
	defer ansi.PutParser(parser)

	var state byte
	for len(s) > 0 {
		parser.Reset()
		seq, _, n, newState := ansi.DecodeSequence(s, state, parser)

		if ansi.HasCsiPrefix(seq) {
			switch parser.Command() & 0xff {
			case 'm':
				// SGR: keep (colors/styles).
				buf.WriteString(seq)
			case 'h', 'l':
				// DEC private mode set/reset (?h, ?l): strip. Regular
				// h/l without ? prefix are also non-rendering.
			case 'A', 'B', 'C', 'D', // cursor up/down/forward/back
				'E', 'F', // cursor next/prev line
				'G',      // cursor horizontal absolute
				'H', 'f', // cursor position
				'J',      // erase display
				'K',      // erase line
				'S', 'T', // scroll up/down
				's', 'u': // save/restore cursor
				// Strip all cursor/screen control.
			default:
				// Unknown CSI: pass through to avoid data loss.
				buf.WriteString(seq)
			}
		} else if ansi.HasEscPrefix(seq) && len(seq) == 2 {
			// ESC followed by single byte: check for DEC save/restore.
			switch seq[1] {
			case '7', '8':
				// DEC save/restore cursor: strip.
			default:
				buf.WriteString(seq)
			}
		} else {
			buf.WriteString(seq)
		}

		s = s[n:]
		state = newState
	}

	result := buf.String()

	// Handle bare \r by simulating line-overwrite. Split on newlines
	// first so we only process \r within individual lines.
	if strings.ContainsRune(result, '\r') {
		result = simulateCarriageReturns(result)
	}

	return result
}

// simulateCarriageReturns processes bare \r characters within each line,
// keeping only the text after the last \r. This matches terminal behavior
// where \r moves the cursor to column 0 and subsequent text overwrites what
// was there before. Progress bars use this pattern extensively.
func simulateCarriageReturns(s string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if idx := strings.LastIndex(line, "\r"); idx >= 0 {
			lines[i] = line[idx+1:]
		}
	}
	return strings.Join(lines, "\n")
}

// writeTruecolor appends "introducer;2;r;g;b" to buf. Nil color emits the
// bare introducer so the terminal default applies.
func writeTruecolor(buf *strings.Builder, introducer int, c color.Color) {
	if c == nil {
		buf.WriteString(strconv.Itoa(introducer))
		return
	}
	r, g, b, _ := c.RGBA()
	buf.WriteString(strconv.Itoa(introducer))
	buf.WriteString(";2;")
	buf.WriteString(strconv.Itoa(int(r >> 8)))
	buf.WriteByte(';')
	buf.WriteString(strconv.Itoa(int(g >> 8)))
	buf.WriteByte(';')
	buf.WriteString(strconv.Itoa(int(b >> 8)))
}
