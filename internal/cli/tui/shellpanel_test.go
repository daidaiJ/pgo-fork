package tui

import (
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/agenttool"
	"github.com/smallnest/pigo/internal/cli/prompts"
)

// TestGatherShellRows pins the /shell panel data pass (T8.4): the backend
// table in cascade order with the live one tagged [current], and a no-tool
// session degrading to the neutral note.
func TestGatherShellRows(t *testing.T) {
	rows, note := gatherShellRows(nil)
	if rows != nil || !strings.Contains(note, "无活动会话") {
		t.Fatalf("gatherShellRows(nil) = %v, %q; want nil + note", rows, note)
	}
	s := &runSession{surface: prompts.SurfaceDeps{}}
	rows, note = gatherShellRows(s)
	if rows != nil || !strings.Contains(note, "bash 工具不可用") {
		t.Fatalf("no-tool rows = %v, %q; want nil + the neutral note", rows, note)
	}
	bash := &agenttool.BashTool{}
	s = &runSession{surface: prompts.SurfaceDeps{Bash: bash}}
	rows, note = gatherShellRows(s)
	if len(rows) != len(agenttool.ShellBackendNames()) {
		t.Fatalf("rows = %d, want %d", len(rows), len(agenttool.ShellBackendNames()))
	}
	if rows[0].title != "bash" || rows[0].tag != "[current]" {
		t.Fatalf("row[0] = %+v, want bash [current] (cascade first)", rows[0])
	}
	if rows[1].tag != "" || !strings.Contains(rows[1].desc, "-Command") {
		t.Fatalf("row[1] = %+v, want untagged with the flag form", rows[1])
	}
	if !strings.Contains(note, "config.toml [shell]") {
		t.Fatalf("note = %q, want the switch semantics", note)
	}
	// A switched backend moves the tag.
	bash.SetShellSpec(agenttool.ShellSpec{Program: "pwsh", Kind: agenttool.ShellKindPwsh})
	rows, _ = gatherShellRows(s)
	for _, r := range rows {
		want := ""
		if r.title == "pwsh" {
			want = "[current]"
		}
		if r.tag != want {
			t.Fatalf("row %q tag = %q, want %q", r.title, r.tag, want)
		}
	}
}
