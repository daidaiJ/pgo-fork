package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/smallnest/pigo/internal/testenv"
)

// TestModelProfilesDecodeAndLookup drives the [models."<id>"] face end to
// end over a real config file: sub-tables decode into profiles, a grok-style
// scalar directly under [models] degrades to an ignorable empty profile
// instead of failing the file, and ProfileFor matches by key
// (case-insensitively) and by wire model id.
func TestModelProfilesDecodeAndLookup(t *testing.T) {
	path := filepath.Join(testenv.Dir(t), "config.toml")
	toml := `
model = "gpt-56"

[models]
default = "glm-5.3-flash"

[models.gpt-56]
model = "gpt-5.6-luna"
name = "GPT-5.6 Luna"
description = "OpenAI GPT-5.6 via gateway"
base_url = "https://gw.example/v1"
protocol = "openai/resp_api"
api_key = "sk-test"
context_window = 1050000
max_output_tokens = 128000
thinking_level = "medium"

[models."deepseek.v4"]
name = "DeepSeek V4"
provider = "openrouter"
`
	if err := os.WriteFile(path, []byte(toml), 0o644); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	cfg, err := LoadFileConfig(path)
	if err != nil {
		t.Fatalf("LoadFileConfig: %v", err)
	}
	if len(cfg.Models) != 3 {
		t.Fatalf("Models = %d entries, want 3 (two profiles + the tolerated scalar)", len(cfg.Models))
	}

	// The scalar under [models] decodes to an empty profile and stays out of
	// the face.
	if !cfg.Models["default"].Empty() {
		t.Errorf("scalar under [models] = %+v, want an ignored empty profile", cfg.Models["default"])
	}
	if ids := cfg.ProfileIDs(); len(ids) != 2 || ids[0] != "deepseek.v4" || ids[1] != "gpt-56" {
		t.Errorf("ProfileIDs = %v, want [deepseek.v4 gpt-56]", ids)
	}

	// Exact-key, case-insensitive, and wire-model lookups.
	key, p, ok := cfg.ProfileFor("gpt-56")
	if !ok || key != "gpt-56" || p.WireModel(key) != "gpt-5.6-luna" {
		t.Errorf("ProfileFor(gpt-56) = (%q, %+v, %v)", key, p, ok)
	}
	if p.ContextWindow != 1050000 || p.MaxOutputTokens != 128000 || p.ThinkingLevel != "medium" {
		t.Errorf("profile scalars = %d/%d/%q", p.ContextWindow, p.MaxOutputTokens, p.ThinkingLevel)
	}
	if key, _, ok := cfg.ProfileFor("GPT-56"); !ok || key != "gpt-56" {
		t.Errorf("case-insensitive lookup = (%q, %v)", key, ok)
	}
	if key, _, ok := cfg.ProfileFor("gpt-5.6-luna"); !ok || key != "gpt-56" {
		t.Errorf("wire-model lookup = (%q, %v)", key, ok)
	}
	if _, _, ok := cfg.ProfileFor("default"); ok {
		t.Error("empty (scalar) profile matched, want it skipped")
	}
	if _, _, ok := cfg.ProfileFor("nope"); ok {
		t.Error("unknown id matched")
	}
	// WireModel falls back to the table key when Model is unset.
	if got := cfg.Models["deepseek.v4"].WireModel("deepseek.v4"); got != "deepseek.v4" {
		t.Errorf("WireModel fallback = %q", got)
	}
}
