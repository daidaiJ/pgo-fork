// Tests for the /skills and /mcp surface actions (T6.9), driven through the
// T7.7 contract (Parse -> typed Intent -> Executor) so the registry-sync
// behavior is exercised the way production invokes it: toggles write config (the projection rule), a
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
func surfaceSetup(t *testing.T, skills ...*runtime.Skill) (reg *runtime.SlashRegistry, deps *SurfaceDeps, cfgPath, skillsDir string) {
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
	deps = &SurfaceDeps{
		Skills:     func() []*runtime.Skill { return view },
		SetSkills:  func(s []*runtime.Skill) { view = s },
		SkillsDir:  skillsDir,
		ConfigPath: cfgPath,
	}
	RegisterSurfaceCommands(reg, deps)
	return reg, deps, cfgPath, skillsDir
}

// skillsAction runs /skills through the contract (T7.7): the registry parses
// the typed intent, the executor drives the surface face.
func skillsAction(t *testing.T, reg *runtime.SlashRegistry, deps *SurfaceDeps, args string) string {
	t.Helper()
	return surfaceExec(t, reg, deps, "/skills "+args)
}

// mcpAction runs /mcp through the contract the same way.
func mcpAction(t *testing.T, reg *runtime.SlashRegistry, deps *SurfaceDeps, args string) string {
	t.Helper()
	return surfaceExec(t, reg, deps, "/mcp "+args)
}

// surfaceExec resolves line and executes the intent against deps. A resolve
// (usage) error surfaces as the message the old Action path printed.
func surfaceExec(t *testing.T, reg *runtime.SlashRegistry, deps *SurfaceDeps, line string) string {
	t.Helper()
	out, err := reg.ResolveOutcome(line)
	if err != nil {
		return err.Error()
	}
	if out.Kind != runtime.SlashIntent {
		t.Fatalf("%s: kind = %v, want SlashIntent", line, out.Kind)
	}
	return (&Executor{Surface: deps}).Execute(out.Intent).Message
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
	reg, deps, cfgPath, dir := surfaceSetup(t, testSkill("weather", false))
	writeSkillFile(t, dir, "weather")
	if _, ok := reg.Lookup("weather"); !ok {
		t.Fatal("precondition: /weather registered")
	}
	got := skillsAction(t, reg, deps, "disable weather")
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
	got = skillsAction(t, reg, deps, "enable weather")
	if !strings.Contains(got, "re-registered") {
		t.Fatalf("enable message = %q", got)
	}
	if _, ok := reg.Lookup("weather"); !ok {
		t.Error("/weather must be re-registered on enable")
	}
}

func TestSkillsListAndInfo(t *testing.T) {
	reg, deps, _, _ := surfaceSetup(t, testSkill("weather", false), testSkill("notes", true))
	got := skillsAction(t, reg, deps, "")
	if !strings.Contains(got, "weather  [model-invocable") || !strings.Contains(got, "notes  [slash-only") {
		t.Fatalf("/skills list = %q", got)
	}
	info := skillsAction(t, reg, deps, "info notes")
	if !strings.Contains(info, "slash-only") || !strings.Contains(info, "notes.md") {
		t.Fatalf("/skills info = %q", info)
	}
	if got := skillsAction(t, reg, deps, "info nosuch"); !strings.Contains(got, "no skill named") {
		t.Fatalf("/skills info unknown = %q", got)
	}
}

func TestSkillsReloadRescansAndSyncs(t *testing.T) {
	reg, deps, _, dir := surfaceSetup(t, testSkill("weather", false))
	// Add notes.md and remove weather.md: reload must pick up the new skill
	// and drop the deleted one from both the view and the registry.
	writeSkillFile(t, dir, "notes")
	if err := os.Remove(filepath.Join(dir, "weather.md")); err != nil {
		t.Fatal(err)
	}
	got := skillsAction(t, reg, deps, "reload")
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
	reg, deps, cfgPath, dir := surfaceSetup(t)
	writeSkillFile(t, dir, "weather")
	// Disable first (writes config), then reload: the skill must stay out of
	// the registry and the view — the hidden-tier semantics hold across reload.
	skillsAction(t, reg, deps, "disable weather")
	got := skillsAction(t, reg, deps, "reload")
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
	reg, deps, _, _ := surfaceSetup(t)
	if got := mcpAction(t, reg, deps, ""); !strings.Contains(got, "no MCP servers configured") {
		t.Fatalf("/mcp without servers = %q, want neutral state", got)
	}
	if got := mcpAction(t, reg, deps, "enable x"); !strings.Contains(got, "no MCP servers configured") {
		t.Fatalf("/mcp enable without servers = %q", got)
	}
}

func TestMCPToggleUnknownServerReports(t *testing.T) {
	// A manager with zero configured servers: toggles must report the unknown
	// name rather than pretending success. (Fresh registry: AddBuiltin panics
	// on a same-name re-registration.)
	reg := runtime.NewSlashRegistry()
	deps := &SurfaceDeps{MCP: mcp.Connect(context.Background(), nil, nil, nil), ConfigPath: ""}
	RegisterSurfaceCommands(reg, deps)
	if got := mcpAction(t, reg, deps, "tool disable fs write_file"); !strings.Contains(got, "no server named") {
		t.Fatalf("/mcp tool on unknown server = %q", got)
	}
	if got := mcpAction(t, reg, deps, "disable fs"); !strings.Contains(got, "no server named") {
		t.Fatalf("/mcp disable on unknown server = %q", got)
	}
}
