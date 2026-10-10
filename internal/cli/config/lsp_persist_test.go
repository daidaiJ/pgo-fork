package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/testenv"
)

// SetLSPTools writes the [lsp.gopls] tools allow-list in place (T8.2 batch
// 2, the /lsp tool toggle's write path).

func TestSetLSPToolsRoundTrip(t *testing.T) {
	dir := testenv.Dir(t)
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("[lsp]\nenabled = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SetLSPTools(path, []string{"diagnostics", "definition"}); err != nil {
		t.Fatalf("SetLSPTools: %v", err)
	}
	cfg, err := LoadFileConfig(path)
	if err != nil {
		t.Fatalf("LoadFileConfig: %v", err)
	}
	if got := cfg.LSP.Gopls.Tools; len(got) != 2 || got[0] != "diagnostics" || got[1] != "definition" {
		t.Fatalf("tools = %v", got)
	}
	if !cfg.LSP.Enabled {
		t.Fatal("[lsp] enabled lost")
	}
	// A second write replaces the key in place.
	if err := SetLSPTools(path, []string{"hover"}); err != nil {
		t.Fatalf("SetLSPTools(2): %v", err)
	}
	data, _ := os.ReadFile(path)
	if got := strings.Count(string(data), "tools = "); got != 1 {
		t.Fatalf("tools key count = %d, want 1 (replaced in place):\n%s", got, data)
	}
}

func TestSetLSPToolsCreatesGoplsTablePreservingComments(t *testing.T) {
	dir := testenv.Dir(t)
	path := filepath.Join(dir, "config.toml")
	content := "# lsp prefs\n[lsp]\nenabled = true\n\n[skills]\ndisabled = []\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SetLSPTools(path, []string{"lsp_symbols"}); err != nil {
		t.Fatalf("SetLSPTools: %v", err)
	}
	data, _ := os.ReadFile(path)
	text := string(data)
	for _, want := range []string{"# lsp prefs", "[lsp.gopls]", "tools = [\"lsp_symbols\"]", "[skills]", "disabled = []"} {
		if !strings.Contains(text, want) {
			t.Fatalf("config lost %q after the write:\n%s", want, text)
		}
	}
}

func TestSetLSPToolsRefusals(t *testing.T) {
	dir := testenv.Dir(t)
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SetLSPTools(path, nil); err == nil {
		t.Fatal("empty list = nil error (an empty filter means all tools)")
	}
	if err := SetLSPTools(path, []string{" "}); err == nil {
		t.Fatal("blank name = nil error")
	}
	if err := SetLSPTools("", []string{"hover"}); err == nil {
		t.Fatal("empty path = nil error")
	}
	if err := SetLSPTools(filepath.Join(dir, "absent.toml"), []string{"hover"}); err == nil {
		t.Fatal("missing config = nil error (the config is read at startup; silently creating one would hide that)")
	}
}
