package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/smallnest/pigo/internal/testenv"
)

// TestFileConfigPath_PIGOHome pins the unified path (T8.1): the config lives
// in the pigo home, like every other piece of pigo state.
func TestFileConfigPath_PIGOHome(t *testing.T) {
	t.Setenv("PIGO_HOME", "D:/custom/pigo-home")
	if got, want := FileConfigPath(), filepath.Join("D:/custom/pigo-home", "config.toml"); got != want {
		t.Fatalf("FileConfigPath() = %q, want %q", got, want)
	}
}

func TestFileConfigPath_DefaultHome(t *testing.T) {
	t.Setenv("PIGO_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	got := FileConfigPath()
	want := filepath.Join(home, ".pigo", "config.toml")
	if got != want {
		t.Fatalf("FileConfigPath() = %q, want %q", got, want)
	}
}

// TestLegacyFileConfigPath pins the read-only fallback location an upgraded
// install's config is migrated from.
func TestLegacyFileConfigPath(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdgroot")
	if got, want := legacyFileConfigPath(), filepath.Join("/tmp/xdgroot", "pigo", "config.toml"); got != want {
		t.Fatalf("legacyFileConfigPath() = %q, want %q", got, want)
	}
}

func TestLoadFileConfig_Missing(t *testing.T) {
	cfg, err := LoadFileConfig(filepath.Join(testenv.Dir(t), "does-not-exist.toml"))
	if err != nil {
		t.Fatalf("missing file should not error, got %v", err)
	}
	if !reflect.DeepEqual(cfg, FileConfig{}) {
		t.Fatalf("missing file should yield zero config, got %+v", cfg)
	}
}

func TestLoadFileConfig_EmptyPath(t *testing.T) {
	cfg, err := LoadFileConfig("")
	if err != nil {
		t.Fatalf("empty path should not error, got %v", err)
	}
	if !reflect.DeepEqual(cfg, FileConfig{}) {
		t.Fatalf("empty path should yield zero config, got %+v", cfg)
	}
}

func TestLoadFileConfig_Valid(t *testing.T) {
	path := filepath.Join(testenv.Dir(t), "config.toml")
	content := `
model = "claude-opus-4-8"
base_url = "https://example.com"
api_key = "sk-test"
protocol = "anthropic"
provider = "deepseek"
thinking_level = "high"
output_format = "stream-json"
no_tools = true
no_skills = true
approve = true
system_prompt = "be terse"
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadFileConfig(path)
	if err != nil {
		t.Fatalf("valid file should parse, got %v", err)
	}
	want := FileConfig{
		Model:         "claude-opus-4-8",
		BaseURL:       "https://example.com",
		APIKey:        "sk-test",
		Protocol:      "anthropic",
		Provider:      "deepseek",
		ThinkingLevel: "high",
		OutputFormat:  "stream-json",
		NoTools:       true,
		NoSkills:      true,
		Approve:       true,
		SystemPrompt:  "be terse",
	}
	if !reflect.DeepEqual(cfg, want) {
		t.Fatalf("parsed config = %+v, want %+v", cfg, want)
	}
}

func TestLoadFileConfig_Malformed(t *testing.T) {
	path := filepath.Join(testenv.Dir(t), "bad.toml")
	if err := os.WriteFile(path, []byte("model = = ="), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFileConfig(path); err == nil {
		t.Fatal("malformed file should error")
	}
}

func TestLoadFileConfigPromptsArray(t *testing.T) {
	path := filepath.Join(testenv.Dir(t), "config.toml")
	content := "prompts = [\"./my-prompts\", \"/abs/x.md\"]\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadFileConfig(path)
	if err != nil {
		t.Fatalf("LoadFileConfig: %v", err)
	}
	if len(cfg.Prompts) != 2 || cfg.Prompts[0] != "./my-prompts" || cfg.Prompts[1] != "/abs/x.md" {
		t.Errorf("Prompts = %v, want [./my-prompts /abs/x.md]", cfg.Prompts)
	}
}

// TestGenericBaseURLEnvVar verifies the <PROVIDER>_BASE_URL name derivation,
// especially the hyphen→underscore conversion and uppercasing.
func TestGenericBaseURLEnvVar(t *testing.T) {
	cases := []struct {
		name string
		want string
	}{
		{"deepseek", "DEEPSEEK_BASE_URL"},
		{"zai-coding-cn", "ZAI_CODING_CN_BASE_URL"},
		{"vercel-ai-gateway", "VERCEL_AI_GATEWAY_BASE_URL"},
		{"", ""},
	}
	for _, c := range cases {
		if got := GenericBaseURLEnvVar(c.name); got != c.want {
			t.Errorf("GenericBaseURLEnvVar(%q) = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestLoadFileConfig_DreamTable(t *testing.T) {
	path := filepath.Join(testenv.Dir(t), "config.toml")
	content := `
[dream]
enabled = false
interval_days = 14
recent_sessions = 50
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadFileConfig(path)
	if err != nil {
		t.Fatalf("LoadFileConfig: %v", err)
	}
	if cfg.Dream.Enabled == nil || *cfg.Dream.Enabled {
		t.Errorf("Dream.Enabled = %v, want explicit false", cfg.Dream.Enabled)
	}
	if cfg.Dream.IntervalDays != 14 {
		t.Errorf("Dream.IntervalDays = %d, want 14", cfg.Dream.IntervalDays)
	}
	if cfg.Dream.RecentSessions != 50 {
		t.Errorf("Dream.RecentSessions = %d, want 50", cfg.Dream.RecentSessions)
	}
}

func TestLoadFileConfig_DreamTableAbsent(t *testing.T) {
	path := filepath.Join(testenv.Dir(t), "config.toml")
	if err := os.WriteFile(path, []byte("model = \"foo\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadFileConfig(path)
	if err != nil {
		t.Fatalf("LoadFileConfig: %v", err)
	}
	// Absent [dream] table: Enabled pointer nil (→ default true downstream),
	// ints zero (→ defaults downstream). Parsing must not error.
	if cfg.Dream.Enabled != nil || cfg.Dream.IntervalDays != 0 || cfg.Dream.RecentSessions != 0 {
		t.Errorf("absent dream table = %+v, want zero-value", cfg.Dream)
	}
}

// TestLoadUserConfig_MigratesLegacy pins the unification semantics (T8.1):
// with only the legacy XDG file present, the config loads from it, a copy is
// migrated to the canonical path, and the returned path names the legacy
// file so the caller can warn. The next load resolves canonically.
func TestLoadUserConfig_MigratesLegacy(t *testing.T) {
	root := testenv.Dir(t)
	t.Setenv("PIGO_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", root)
	// Isolate the home both ways so the canonical path lands in the test root
	// instead of the real ~/.pigo (os.UserHomeDir reads USERPROFILE on
	// Windows and HOME elsewhere).
	t.Setenv("USERPROFILE", root)
	t.Setenv("HOME", root)
	legacy := filepath.Join(root, "pigo", "config.toml")
	if err := os.MkdirAll(filepath.Dir(legacy), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, []byte("model = \"legacy-model\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, path, err := LoadUserConfig()
	if err != nil {
		t.Fatalf("LoadUserConfig: %v", err)
	}
	if path != legacy || cfg.Model != "legacy-model" {
		t.Fatalf("LoadUserConfig = (%+v, %q), want the legacy file", cfg, path)
	}
	// The canonical copy now exists and carries the same content.
	canonical := FileConfigPath()
	data, err := os.ReadFile(canonical)
	if err != nil || string(data) != "model = \"legacy-model\"\n" {
		t.Fatalf("canonical copy = %q, %v; want the migrated config", data, err)
	}
	// The next load resolves canonically: no legacy path, no warning case.
	if _, path, _ = LoadUserConfig(); path != canonical {
		t.Fatalf("second LoadUserConfig path = %q, want the canonical %q", path, canonical)
	}
}

// TestLoadUserConfig_PIGOHomeNeverReadsLegacy pins the isolation rule: with
// PIGO_HOME set, only the canonical path is consulted — the host's XDG tree
// is out of scope (the dual-path defect this closes).
func TestLoadUserConfig_PIGOHomeNeverReadsLegacy(t *testing.T) {
	root := testenv.Dir(t)
	t.Setenv("PIGO_HOME", root)
	t.Setenv("XDG_CONFIG_HOME", root)
	legacy := filepath.Join(root, "pigo", "config.toml")
	if err := os.MkdirAll(filepath.Dir(legacy), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, []byte("model = \"host-model\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, path, err := LoadUserConfig()
	if err != nil {
		t.Fatalf("LoadUserConfig: %v", err)
	}
	if path != "" || cfg.Model != "" {
		t.Fatalf("LoadUserConfig = (%+v, %q), want nothing (the legacy file is out of scope under PIGO_HOME)", cfg, path)
	}
}

// TestLoadUserConfig_CanonicalWins pins the resolution order once both files
// exist: the canonical path is the single truth.
func TestLoadUserConfig_CanonicalWins(t *testing.T) {
	root := testenv.Dir(t)
	t.Setenv("PIGO_HOME", root)
	t.Setenv("XDG_CONFIG_HOME", root)
	if err := os.WriteFile(FileConfigPath(), []byte("model = \"canonical\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, path, err := LoadUserConfig()
	if err != nil {
		t.Fatalf("LoadUserConfig: %v", err)
	}
	if path != FileConfigPath() || cfg.Model != "canonical" {
		t.Fatalf("LoadUserConfig = (%+v, %q), want the canonical file", cfg, path)
	}
}
