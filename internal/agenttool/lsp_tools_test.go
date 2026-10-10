package agenttool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/lsp"
)

func runLSP(t *testing.T, tool agentcore.AgentTool, args string) agentcore.AgentToolResult {
	t.Helper()
	res, gerr := tool.Execute(context.Background(), "call-1", json.RawMessage(args), nil)
	if gerr != nil {
		t.Fatalf("execute returned go error: %v", gerr)
	}
	return res
}

func lspText(t *testing.T, res agentcore.AgentToolResult) string {
	t.Helper()
	tc, ok := res.Content[0].(agentcore.TextContent)
	if !ok {
		t.Fatalf("first content block = %T", res.Content[0])
	}
	return tc.Text
}

func TestLSPToolsFilterNormalization(t *testing.T) {
	mgr := lsp.NewManager(lsp.Settings{}, t.TempDir()) // disabled; construction only
	if got := LSPTools(nil, nil); got != nil {
		t.Fatalf("LSPTools(nil) = %v", got)
	}
	all := LSPTools(mgr, nil)
	if len(all) != len(LSPToolNames) {
		t.Fatalf("full face = %d tools, want %d", len(all), len(LSPToolNames))
	}
	for i, tool := range all {
		if tool.Name() != LSPToolNames[i] {
			t.Fatalf("tool[%d] = %q, want %q", i, tool.Name(), LSPToolNames[i])
		}
	}
	// Filter by bare names, with and without the prefix, case-insensitively;
	// unknown entries are ignored.
	got := LSPTools(mgr, []string{"Diagnostics", "lsp_hover", "unknown_one"})
	if len(got) != 2 {
		t.Fatalf("filtered face = %d tools", len(got))
	}
	if got[0].Name() != "lsp_diagnostics" || got[1].Name() != "lsp_hover" {
		t.Fatalf("filtered face = %q %q", got[0].Name(), got[1].Name())
	}
}

func TestLSPDiagnosticsToolDisabledManager(t *testing.T) {
	// A manager built disabled answers with the enable note, not a Go error.
	mgr := lsp.NewManager(lsp.Settings{Enabled: false}, t.TempDir())
	res := runLSP(t, &LSPDiagnosticsTool{Mgr: mgr}, `{}`)
	if text := lspText(t, res); !strings.Contains(text, "lsp: disabled") {
		t.Fatalf("result = %q", text)
	}
}

func TestLSPPositionToolArgValidation(t *testing.T) {
	mgr := lsp.NewManager(lsp.Settings{Enabled: false}, t.TempDir())
	tool := &LSPDefinitionTool{lspPositionTool{Mgr: mgr, name: "lsp_definition"}}
	if text := lspText(t, runLSP(t, tool, `{"line": 3}`)); !strings.Contains(text, "path is required") {
		t.Fatalf("missing path → %q", text)
	}
	if text := lspText(t, runLSP(t, tool, `{"path": "a.go"}`)); !strings.Contains(text, "line is required") {
		t.Fatalf("missing line → %q", text)
	}
	if text := lspText(t, runLSP(t, tool, `{"path": "../outside.go", "line": 1}`)); !strings.Contains(text, "lsp_definition:") {
		t.Fatalf("out-of-root → %q", text)
	}
}

func TestInjectLSPOverlay(t *testing.T) {
	mgr := &lsp.Manager{}
	edit := &EditTool{Root: t.TempDir()}
	write := &WriteTool{Root: t.TempDir()}
	InjectLSPOverlay([]agentcore.AgentTool{edit, write}, mgr)
	if edit.LSP == nil || write.LSP == nil {
		t.Fatal("overlay sink not injected into edit/write")
	}
	// A nil manager is a no-op (never clobbers).
	InjectLSPOverlay([]agentcore.AgentTool{edit}, nil)
	if edit.LSP == nil {
		t.Fatal("nil manager cleared the sink")
	}
}

// TestLSPErrorBlock pins the inline edit-report shape (opencode edit.ts
// alignment): error-level only, capped, silence when clean.
func TestLSPErrorBlock(t *testing.T) {
	if got := lspErrorBlock(nil); got != "" {
		t.Fatalf("empty diags → %q", got)
	}
	if got := lspErrorBlock([]lsp.Diagnostic{{Severity: 2, Message: "warn"}}); got != "" {
		t.Fatalf("warning-only → %q", got)
	}
	got := lspErrorBlock([]lsp.Diagnostic{
		{Severity: 1, Line: 2, Character: 0, Message: "undefined: Foo", Source: "compile"},
		{Severity: 1, Line: 8, Character: 4, Message: "missing return"},
	})
	want := "\n\nLSP errors detected in this file, please fix:" +
		"\n  E [3:1] undefined: Foo (compile)" +
		"\n  E [9:5] missing return"
	if got != want {
		t.Fatalf("block = %q, want %q", got, want)
	}
	// Cap: 12 errors → 10 shown + a pointer to the explicit query.
	many := make([]lsp.Diagnostic, 0, 12)
	for i := 0; i < 12; i++ {
		many = append(many, lsp.Diagnostic{Severity: 1, Line: i, Message: "x"})
	}
	got = lspErrorBlock(many)
	if !strings.Contains(got, "and 2 more") {
		t.Fatalf("capped block = %q", got)
	}
}
