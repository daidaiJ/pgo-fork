// Tests for the config.toml textual patcher (T6.9 persist.go): every toggle
// must survive a round trip (set → load → assert), must preserve unrelated
// lines byte-for-byte (the patcher's whole reason to exist), and must refuse
// to guess when the file shape is ambiguous.
package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/testenv"
)

const sampleConfig = `# my hand-written config — comments must survive
model = "openrouter/free"

[tools]
declaration_mode = "deferred"
direct = ["read"]              # keep read direct

[[mcp.servers]]
name = "fs"
command = "npx"
args = ["-y", "@modelcontextprotocol/server-filesystem", "."]

[[mcp.servers]]
name    = "web"                # second server, spaced style
type    = "http"
url     = "http://localhost:8338/mcp"
disabled_tools = ["smartsearch"]  # existing list

[skills]
# skills notes
`

func writeSample(t *testing.T) string {
	t.Helper()
	path := filepath.Join(testenv.Dir(t), "config.toml")
	if err := os.WriteFile(path, []byte(sampleConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func mustLoad(t *testing.T, path string) FileConfig {
	t.Helper()
	cfg, err := LoadFileConfig(path)
	if err != nil {
		t.Fatalf("LoadFileConfig: %v", err)
	}
	return cfg
}

func TestSetSkillsDisabledRoundTrip(t *testing.T) {
	path := writeSample(t)
	if err := SetSkillsDisabled(path, "weather", true); err != nil {
		t.Fatalf("disable: %v", err)
	}
	cfg := mustLoad(t, path)
	if !cfg.Skills.SkillDisabled("weather") {
		t.Fatal("weather not disabled after write")
	}
	// Dedup: disabling again must not duplicate the entry.
	if err := SetSkillsDisabled(path, "Weather", true); err != nil {
		t.Fatalf("re-disable: %v", err)
	}
	cfg = mustLoad(t, path)
	if len(cfg.Skills.Disabled) != 1 {
		t.Fatalf("disabled = %v, want exactly one entry", cfg.Skills.Disabled)
	}
	// Enable removes it and drops the empty key.
	if err := SetSkillsDisabled(path, "weather", false); err != nil {
		t.Fatalf("enable: %v", err)
	}
	cfg = mustLoad(t, path)
	if cfg.Skills.SkillDisabled("weather") || len(cfg.Skills.Disabled) != 0 {
		t.Fatalf("after enable: disabled = %v", cfg.Skills.Disabled)
	}
}

func TestSetSkillsDisabledPreservesComments(t *testing.T) {
	path := writeSample(t)
	if err := SetSkillsDisabled(path, "weather", true); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	text := string(data)
	for _, want := range []string{"# my hand-written config", "# skills notes", `declaration_mode = "deferred"`} {
		if !strings.Contains(text, want) {
			t.Errorf("patched config lost %q", want)
		}
	}
}

func TestSetMCPToolDisabledRoundTrip(t *testing.T) {
	path := writeSample(t)
	// Server with an existing list: append and remove.
	if err := SetMCPToolDisabled(path, "web", "cleanfetch", true); err != nil {
		t.Fatalf("disable: %v", err)
	}
	cfg := mustLoad(t, path)
	if len(cfg.MCP.Servers) != 2 {
		t.Fatalf("servers = %d, want 2 (patcher must not touch the other entry)", len(cfg.MCP.Servers))
	}
	web := cfg.MCP.Servers[1]
	if len(web.DisabledTools) != 2 || web.DisabledTools[0] != "smartsearch" || web.DisabledTools[1] != "cleanfetch" {
		t.Fatalf("web disabled_tools = %v, want [smartsearch cleanfetch]", web.DisabledTools)
	}
	// Server without the key: creates it.
	if err := SetMCPToolDisabled(path, "fs", "write_file", true); err != nil {
		t.Fatalf("disable fs: %v", err)
	}
	cfg = mustLoad(t, path)
	if got := cfg.MCP.Servers[0].DisabledTools; len(got) != 1 || got[0] != "write_file" {
		t.Fatalf("fs disabled_tools = %v, want [write_file]", got)
	}
	// Enable removes.
	if err := SetMCPToolDisabled(path, "web", "cleanfetch", false); err != nil {
		t.Fatalf("enable: %v", err)
	}
	cfg = mustLoad(t, path)
	if got := cfg.MCP.Servers[1].DisabledTools; len(got) != 1 || got[0] != "smartsearch" {
		t.Fatalf("web disabled_tools after enable = %v, want [smartsearch]", got)
	}
}

func TestSetMCPServerEnabledRoundTrip(t *testing.T) {
	path := writeSample(t)
	// Disable writes enabled = false.
	if err := SetMCPServerEnabled(path, "fs", false); err != nil {
		t.Fatalf("disable: %v", err)
	}
	cfg := mustLoad(t, path)
	if cfg.MCP.Servers[0].Enabled == nil || *cfg.MCP.Servers[0].Enabled {
		t.Fatalf("fs after disable: Enabled = %v, want explicit false", cfg.MCP.Servers[0].Enabled)
	}
	// Enable removes the key (default-on), not writes enabled = true.
	if err := SetMCPServerEnabled(path, "fs", true); err != nil {
		t.Fatalf("enable: %v", err)
	}
	cfg = mustLoad(t, path)
	if cfg.MCP.Servers[0].Enabled != nil {
		t.Fatalf("fs after enable: Enabled = %v, want nil (default-on, key removed)", cfg.MCP.Servers[0].Enabled)
	}
}

func TestPatchUnknownOrAmbiguousServerFails(t *testing.T) {
	path := writeSample(t)
	if err := SetMCPToolDisabled(path, "nosuch", "tool", true); err == nil {
		t.Fatal("unknown server must error")
	}
	// Duplicate names must refuse rather than patch one arbitrarily.
	path2 := filepath.Join(testenv.Dir(t), "dup.toml")
	dup := strings.Replace(sampleConfig, `name    = "web"`, `name    = "fs"`, 1)
	if err := os.WriteFile(path2, []byte(dup), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SetMCPToolDisabled(path2, "fs", "tool", true); err == nil {
		t.Fatal("ambiguous server names must error")
	}
}

func TestPatchMissingFileFails(t *testing.T) {
	if err := SetSkillsDisabled(filepath.Join(testenv.Dir(t), "absent.toml"), "x", true); err == nil {
		t.Fatal("missing file must error (no silent creation)")
	}
}
