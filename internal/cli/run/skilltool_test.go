package run

// T6.5 skill-as-tool assembly tests: skills materialize as sub-agent tools in
// SetupEnv (model-invocable ones only), and the per-spawn run config honors the
// frontmatter model (inherit by default, re-resolve when pinned, D-7 error on
// resolution failure).

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/cli/config"
	"github.com/smallnest/pigo/internal/lsp"
	"github.com/smallnest/pigo/internal/provider"
	"github.com/smallnest/pigo/internal/runtime"
	"github.com/smallnest/pigo/internal/testenv"
)

func TestSetupEnvMaterializesSkillTools(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "test-key")
	t.Setenv("PIGO_HOME", testenv.Dir(t))
	// FileConfigPath ignores PIGO_HOME (registered config-path defect):
	// isolate the XDG config too, or the host config's [skills] disabled
	// bleeds into the loaded skill set.
	t.Setenv("XDG_CONFIG_HOME", testenv.Dir(t))
	skillsDir := testenv.Dir(t)
	t.Setenv("PIGO_SKILLS_DIR", skillsDir)
	writeSkillFile(t, skillsDir, "weather", "---\nname: weather\ndescription: get the weather\n---\nDo weather.")
	writeSkillFile(t, skillsDir, "secret", "---\nname: secret\ndescription: hidden skill\ndisable-model-invocation: true\n---\nSlash only.")

	env, err := SetupEnv("openai/gpt-4o", "", "", "", "", "", false /*noTools*/, false /*noSkills*/, "", nil, false, config.MaxContext{}, config.ToolsConfig{}, config.MCPConfig{}, lsp.Settings{}, ToolPolicy{})
	if err != nil {
		t.Fatalf("SetupEnv: %v", err)
	}
	got := names(env.Tools)
	if !contains(got, "weather") {
		t.Errorf("model-invocable skill must materialize as a tool, got %q", got)
	}
	if contains(got, "secret") {
		t.Errorf("disable-model-invocation skill must stay slash-only, got %q", got)
	}
}

func TestSetupEnvNoToolsSkipsSkillTools(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "test-key")
	t.Setenv("PIGO_HOME", testenv.Dir(t))
	// FileConfigPath ignores PIGO_HOME (registered config-path defect):
	// isolate the XDG config too, or the host config's [skills] disabled
	// bleeds into the loaded skill set.
	t.Setenv("XDG_CONFIG_HOME", testenv.Dir(t))
	skillsDir := testenv.Dir(t)
	t.Setenv("PIGO_SKILLS_DIR", skillsDir)
	writeSkillFile(t, skillsDir, "weather", "---\nname: weather\ndescription: get the weather\n---\nDo weather.")

	env, err := SetupEnv("openai/gpt-4o", "", "", "", "", "", true /*noTools*/, false, "", nil, false, config.MaxContext{}, config.ToolsConfig{}, config.MCPConfig{}, lsp.Settings{}, ToolPolicy{})
	if err != nil {
		t.Fatalf("SetupEnv: %v", err)
	}
	if got := names(env.Tools); len(got) != 0 {
		t.Errorf("--no-tools must leave no tools (skill tools included), got %q", got)
	}
}

func TestSkillChildRunConfigInheritsByDefault(t *testing.T) {
	parentProv, parentName, err := provider.ResolveProvider("openai/gpt-4o", "", "", "", os.Getenv)
	if err != nil {
		t.Fatalf("ResolveProvider: %v", err)
	}
	parentCreds := provider.NewCredentialStore(nil)
	sk := &runtime.Skill{Frontmatter: runtime.SkillFrontmatter{Name: "r"}}
	factory := skillChildRunConfig(sk, "openai/gpt-4o", "", "", parentProv, parentName, "key", parentCreds, nil, config.ToolsConfig{})
	cfg, err := factory(ChildToolSet("/tmp", ToolPolicy{}))
	if err != nil {
		t.Fatalf("factory = %v, want nil", err)
	}
	if cfg.Model != "openai/gpt-4o" || cfg.Provider != parentName {
		t.Errorf("child run = %q/%q, want parent inherit", cfg.Model, cfg.Provider)
	}
}

func TestSkillChildRunConfigResolvesPinnedModel(t *testing.T) {
	parentProv, parentName, err := provider.ResolveProvider("openai/gpt-4o", "", "", "", os.Getenv)
	if err != nil {
		t.Fatalf("ResolveProvider: %v", err)
	}
	parentCreds := provider.NewCredentialStore(nil)
	parentCreds.SetOverride(parentName, "parent-key")
	// Same provider family, different model id: the pinned model rides the same
	// provider and keeps the parent credential override.
	sk := &runtime.Skill{Frontmatter: runtime.SkillFrontmatter{Name: "r", Model: "openai/gpt-4o"}}
	factory := skillChildRunConfig(sk, "other/model", "", "", parentProv, parentName, "parent-key", parentCreds, nil, config.ToolsConfig{})
	cfg, err := factory(ChildToolSet("/tmp", ToolPolicy{}))
	if err != nil {
		t.Fatalf("factory = %v, want nil", err)
	}
	if cfg.Model != "openai/gpt-4o" {
		t.Errorf("child model = %q, want the frontmatter pin", cfg.Model)
	}
	if cfg.Provider != parentName {
		t.Errorf("child provider = %q, want %q (same family keeps the override)", cfg.Provider, parentName)
	}
	if cfg.GetAPIKey == nil {
		t.Fatal("child GetAPIKey must be wired")
	}
}

func TestSkillChildRunConfigFailsOnUnresolvableModel(t *testing.T) {
	parentProv, parentName, err := provider.ResolveProvider("openai/gpt-4o", "", "", "", os.Getenv)
	if err != nil {
		t.Fatalf("ResolveProvider: %v", err)
	}
	sk := &runtime.Skill{Frontmatter: runtime.SkillFrontmatter{Name: "r", Model: "whatever"}}
	// An explicit protocol without a base URL cannot resolve any model: the
	// factory must return the error (D-7 envelope upstream), never silently
	// degrade to the parent provider/model.
	factory := skillChildRunConfig(sk, "openai/gpt-4o", "", "openai", parentProv, parentName, "key", provider.NewCredentialStore(nil), nil, config.ToolsConfig{})
	cfg, err := factory(ChildToolSet("/tmp", ToolPolicy{}))
	if err == nil {
		t.Fatal("factory = nil error, want a resolution failure")
	}
	if !strings.Contains(err.Error(), `skill "r"`) || !strings.Contains(err.Error(), "whatever") {
		t.Errorf("error = %v, want it to name the skill and the model", err)
	}
	if cfg.Model != "" {
		t.Errorf("on error the run config must stay zero, got model %q", cfg.Model)
	}
}

// writeSkillFile drops a skill file (flat *.md layout) into dir.
func writeSkillFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte(content), 0o644); err != nil {
		t.Fatalf("write skill %s: %v", name, err)
	}
}
