// Tests for the /skills and /mcp surface actions (T6.9), driven through the
// registered Action closures so the registry-sync behavior is exercised the
// way production invokes it: toggles write config (the projection rule), a
// disabled skill's /name command leaves the registry immediately, /skills
// reload re-syncs, and the MCP face renders its neutral state when nothing is
// configured.
package prompts

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/cli/config"
	"github.com/smallnest/pigo/internal/mcp"
	"github.com/smallnest/pigo/internal/runtime"
	"github.com/smallnest/pigo/internal/testenv"
)

func testSkill(name string, slashOnly bool) *runtime.Skill {
	return &runtime.Skill{
		Frontmatter: runtime.SkillFrontmatter{
			Name:                   name,
			Description:            "desc of " + name,
			DisableModelInvocation: slashOnly,
		},
		Body: "body of " + name,
		Path: "/skills/" + name + ".md",
	}
}

// surfaceSetup builds the deps + registry over a temp config and skills dir,
// registering the surface commands exactly as the front-ends do.
func surfaceSetup(t *testing.T, skills ...*runtime.Skill) (reg *runtime.SlashRegistry, cfgPath, skillsDir string) {
	t.Helper()
	dir := testenv.Dir(t)
	cfgPath = filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfgPath, []byte("# test config\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	skillsDir = filepath.Join(dir, "skills")
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	view := append([]*runtime.Skill{}, skills...)
	reg = runtime.NewSlashRegistry()
	// Mirror BuildSlashRegistry's skill registration AND the on-disk source
	// files (reload rescans the dir, so files must exist).
	for _, s := range skills {
		writeSkillFile(t, skillsDir, s.Frontmatter.Name)
		reg.AddSkill(s.SlashCommand())
	}
	RegisterSurfaceCommands(reg, SurfaceDeps{
		Skills:     func() []*runtime.Skill { return view },
		SetSkills:  func(s []*runtime.Skill) { view = s },
		SkillsDir:  skillsDir,
		ConfigPath: cfgPath,
	})
	return reg, cfgPath, skillsDir
}

// skillsAction runs /skills through the registry (nil-safe for tests that
// registered no skills command).
func skillsAction(t *testing.T, reg *runtime.SlashRegistry, args string) string {
	t.Helper()
	cmd, ok := reg.Lookup("skills")
	if !ok {
		t.Fatal("/skills not registered")
	}
	return cmd.Action(args)
}

// writeSkillFile drops one skill file into the dir.
func writeSkillFile(t *testing.T, dir, name string) {
	t.Helper()
	content := "---\nname: " + name + "\ndescription: desc of " + name + "\n---\nbody of " + name
	if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSkillsToggleWritesConfigAndSyncsRegistry(t *testing.T) {
	reg, cfgPath, dir := surfaceSetup(t, testSkill("weather", false))
	writeSkillFile(t, dir, "weather")
	if _, ok := reg.Lookup("weather"); !ok {
		t.Fatal("precondition: /weather registered")
	}
	got := skillsAction(t, reg, "disable weather")
	if !strings.Contains(got, "written to config") {
		t.Fatalf("disable message = %q, want config-write confirmation", got)
	}
	if _, ok := reg.Lookup("weather"); ok {
		t.Error("/weather must leave the registry immediately on disable")
	}
	cfg, err := config.LoadFileConfig(cfgPath)
	if err != nil || !cfg.Skills.SkillDisabled("weather") {
		t.Fatalf("config not persisted: %v %+v", err, cfg)
	}
	// Enable: the skill file exists on disk, so the reload path re-registers it.
	got = skillsAction(t, reg, "enable weather")
	if !strings.Contains(got, "re-registered") {
		t.Fatalf("enable message = %q", got)
	}
	if _, ok := reg.Lookup("weather"); !ok {
		t.Error("/weather must be re-registered on enable")
	}
}

func TestSkillsListAndInfo(t *testing.T) {
	reg, _, _ := surfaceSetup(t, testSkill("weather", false), testSkill("notes", true))
	got := skillsAction(t, reg, "")
	if !strings.Contains(got, "weather  [model-invocable") || !strings.Contains(got, "notes  [slash-only") {
		t.Fatalf("/skills list = %q", got)
	}
	info := skillsAction(t, reg, "info notes")
	if !strings.Contains(info, "slash-only") || !strings.Contains(info, "notes.md") {
		t.Fatalf("/skills info = %q", info)
	}
	if got := skillsAction(t, reg, "info nosuch"); !strings.Contains(got, "no skill named") {
		t.Fatalf("/skills info unknown = %q", got)
	}
}

func TestSkillsReloadRescansAndSyncs(t *testing.T) {
	reg, _, dir := surfaceSetup(t, testSkill("weather", false))
	// Add notes.md and remove weather.md: reload must pick up the new skill
	// and drop the deleted one from both the view and the registry.
	writeSkillFile(t, dir, "notes")
	if err := os.Remove(filepath.Join(dir, "weather.md")); err != nil {
		t.Fatal(err)
	}
	got := skillsAction(t, reg, "reload")
	if !strings.Contains(got, "reloaded 1 skill") {
		t.Fatalf("/skills reload = %q", got)
	}
	if _, ok := reg.Lookup("weather"); ok {
		t.Error("removed skill's command must leave the registry on reload")
	}
	if _, ok := reg.Lookup("notes"); !ok {
		t.Error("new skill's command must join the registry on reload")
	}
}

func TestSkillsReloadHonorsConfigDisabled(t *testing.T) {
	reg, cfgPath, dir := surfaceSetup(t)
	writeSkillFile(t, dir, "weather")
	// Disable first (writes config), then reload: the skill must stay out of
	// the registry and the view — the hidden-tier semantics hold across reload.
	skillsAction(t, reg, "disable weather")
	got := skillsAction(t, reg, "reload")
	if !strings.Contains(got, "reloaded 0 skill") {
		t.Fatalf("/skills reload after disable = %q", got)
	}
	if _, ok := reg.Lookup("weather"); ok {
		t.Error("disabled skill must not re-register on reload")
	}
	if cfg, _ := config.LoadFileConfig(cfgPath); !cfg.Skills.SkillDisabled("weather") {
		t.Error("disable state lost")
	}
}

func TestMCPNeutralWithoutServers(t *testing.T) {
	reg, _, _ := surfaceSetup(t)
	cmd, ok := reg.Lookup("mcp")
	if !ok {
		t.Fatal("/mcp not registered")
	}
	if got := cmd.Action(""); !strings.Contains(got, "no MCP servers configured") {
		t.Fatalf("/mcp without servers = %q, want neutral state", got)
	}
	if got := cmd.Action("enable x"); !strings.Contains(got, "no MCP servers configured") {
		t.Fatalf("/mcp enable without servers = %q", got)
	}
}

func TestMCPToggleUnknownServerReports(t *testing.T) {
	// A manager with zero configured servers: toggles must report the unknown
	// name rather than pretending success. (Fresh registry: AddBuiltin panics
	// on a same-name re-registration.)
	reg := runtime.NewSlashRegistry()
	RegisterSurfaceCommands(reg, SurfaceDeps{MCP: mcp.Connect(context.Background(), nil, nil, nil), ConfigPath: ""})
	cmd, _ := reg.Lookup("mcp")
	if got := cmd.Action("tool disable fs write_file"); !strings.Contains(got, "no server named") {
		t.Fatalf("/mcp tool on unknown server = %q", got)
	}
	if got := cmd.Action("disable fs"); !strings.Contains(got, "no server named") {
		t.Fatalf("/mcp disable on unknown server = %q", got)
	}
}
