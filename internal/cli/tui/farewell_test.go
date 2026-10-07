package tui

import (
	"bytes"
	"strings"
	"testing"
)

// TestFarewellCarriesTheEasterEgg mirrors the grok fork's
// farewell_line_carries_the_easter_egg guard (xai-grok-pager app/mod.rs): the
// LOCAL exit line must keep the panda emoji and the exact phrase, so later
// changelog/banner work cannot silently drop it.
func TestFarewellCarriesTheEasterEgg(t *testing.T) {
	if !strings.Contains(farewell, "🐼") {
		t.Errorf("farewell lost the panda emoji: %q", farewell)
	}
	for _, want := range []string{"Code together", "cola together", "pairing with Panda"} {
		if !strings.Contains(farewell, want) {
			t.Errorf("farewell missing %q: %q", want, farewell)
		}
	}
}

// TestPrintFarewellWritesOneLine verifies the writer path used by Run on a
// graceful quit.
func TestPrintFarewellWritesOneLine(t *testing.T) {
	var buf bytes.Buffer
	printFarewell(&buf)
	if got := buf.String(); got != farewell+"\n" {
		t.Errorf("printFarewell wrote %q, want %q", got, farewell+"\n")
	}
}
