package tui

import (
	"fmt"
	"io"
)

// farewell is the LOCAL easter egg carried over from the grok fork's exit path
// (xai-grok-pager app/mod.rs FAREWELL): one warm line on stderr after the
// alt-screen is restored, so it lands in the user's scrollback — never inside
// the TUI itself. grok guards the line with a dedicated test so changelog or
// announcement work cannot silently drop it; farewell_test.go is that guard
// here.
const farewell = "🐼 Code together, cola together — thanks for pairing with Panda!"

// printFarewell writes the easter egg after a graceful quit. Callers pass the
// process stderr; error paths (p.Run returning an error) stay silent so a
// broken session reports its real problem without a cheerful sticker on top.
func printFarewell(w io.Writer) {
	fmt.Fprintln(w, farewell)
}
