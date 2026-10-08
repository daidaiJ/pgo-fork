// This file implements the config-surface slash commands (T6.9, spec
// slash-config-surface.md): /skills, /mcp, and the promoted /status text hook.
// They are registered into the SHARED registry (BuildSlashRegistry), so the
// REPL and the TUI both get them from one wiring — the commands are the
// interactive projection of the config face, never a second source of truth:
// every toggle writes config.toml (persist.go) and only mirrors the change
// into live state.
//
// Effect timing is honest about the run architecture (deviation D-7 of the
// spec): the declared tool face and the system prompt are assembled once per
// run in SetupEnv, so config writes take full effect on the NEXT SESSION.
// What CAN take effect immediately does: a disabled skill's /name command
// leaves the registry now, and a disabled MCP server's connection closes now.
package prompts

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/smallnest/pigo/internal/cli/config"
	"github.com/smallnest/pigo/internal/mcp"
	"github.com/smallnest/pigo/internal/runtime"
)

// SurfaceDeps carries the live state the config-surface commands need. Every
// field is optional in the fail-closed sense: a nil manager yields neutral
// /mcp output, an empty ConfigPath makes the write commands refuse (headless
// safety, spec §2.4), and a nil Skills view yields an empty /skills list.
type SurfaceDeps struct {
	// MCP is the live manager from run.SetupEnv (nil when tools are disabled
	// or no server is configured).
	MCP *mcp.Manager
	// Skills returns the current skill view (the filtered LoadSkills result).
	Skills func() []*runtime.Skill
	// SetSkills swaps the caller's skill view (used by /skills reload); nil
	// means the view cannot be swapped and reload reports that.
	SetSkills func([]*runtime.Skill)
	// SkillsDir is the directory /skills reload rescans; empty disables reload.
	SkillsDir string
	// ConfigPath is the config.toml the toggles write; empty = read-only.
	ConfigPath string
	// Registry is the shared registry these commands were added to, so
	// disable/enable/reload can keep the /name command list in sync. Set by
	// RegisterSurfaceCommands.
	registry *runtime.SlashRegistry
	// StatusText renders the promoted /status report (REPL: the full
	// status.RunStatus output; TUI: the sections available there). Nil makes
	// /status report itself unavailable.
	StatusText func() string
}

// RegisterSurfaceCommands adds /skills and /mcp to reg. It is called once per
// front-end from the shared BuildSlashRegistry wiring; both front-ends
// construct their own SurfaceDeps (what they can see differs, the commands do
// not).
func RegisterSurfaceCommands(reg *runtime.SlashRegistry, deps SurfaceDeps) {
	deps.registry = reg
	reg.AddBuiltin(runtime.SlashCommand{
		Name:         "skills",
		ArgumentHint: "[list|disable|enable|info|reload]",
		Description:  "list skills or toggle one: /skills disable <name> (writes config; next-session effect)",
		Action:       deps.skillsAction,
	})
	reg.AddBuiltin(runtime.SlashCommand{
		Name:         "mcp",
		ArgumentHint: "[enable|disable <server> | tool enable|disable <server> <tool> | reload <server>]",
		Description:  "MCP server and per-tool switches, connection state and schema reload",
		Action:       deps.mcpAction,
	})
}

// RegisterStatusCommand installs the promoted /status (T6.9 G-4 promotion: out
// of the REPL's hardcoded intercept into the shared registry, so the TUI gets
// it too). render is front-end supplied — the REPL renders the full
// status.RunStatus report through its cli.Host; the TUI renders the sections
// it can see. Nil render means the caller skipped registration entirely.
func RegisterStatusCommand(reg *runtime.SlashRegistry, render func() string) {
	if render == nil {
		return
	}
	reg.AddBuiltin(runtime.SlashCommand{
		Name:        "status",
		Description: "show runtime config, context, MCP, skills, permissions, credentials, telemetry",
		Action: func(string) string { return render() },
	})
}

// MCPList renders the MCP section text (the /mcp bare listing). It is exported
// so the TUI's promoted /status can embed the same face without duplicating
// the renderer.
func (d *SurfaceDeps) MCPList() string { return d.mcpList() }

// disabledSkillNames reads the config's [skills] disabled list for display.
// A read error degrades to an empty list: the surface stays usable, the
// toggles (which need the write path) surface their own errors.
func (d *SurfaceDeps) disabledSkillNames() []string {
	cfg, err := config.LoadFileConfig(d.ConfigPath)
	if err != nil {
		return nil
	}
	out := append([]string{}, cfg.Skills.Disabled...)
	sort.Strings(out)
	return out
}

// skillsAction implements /skills: bare list, disable/enable (config write +
// registry sync), info, reload (rescan + view swap + registry sync).
func (d *SurfaceDeps) skillsAction(args string) string {
	fields := strings.Fields(args)
	sub := ""
	if len(fields) > 0 {
		sub = strings.ToLower(fields[0])
	}
	switch sub {
	case "":
		return d.skillsList()
	case "disable", "enable":
		if len(fields) < 2 {
			return fmt.Sprintf("skills: usage: /skills %s <name>", sub)
		}
		return d.skillsToggle(fields[1], sub == "disable")
	case "info":
		if len(fields) < 2 {
			return "skills: usage: /skills info <name>"
		}
		return d.skillsInfo(fields[1])
	case "reload":
		return d.skillsReload()
	default:
		return fmt.Sprintf("skills: unknown subcommand %q (want list | disable | enable | info | reload)", sub)
	}
}

// skillsList renders the skill face: loaded skills with their invocation mode
// and size, plus the disabled names from config (they are filtered out of the
// loaded view, so they appear only here).
func (d *SurfaceDeps) skillsList() string {
	var b strings.Builder
	b.WriteString("skills:")
	if d.Skills == nil {
		b.WriteString(" (skill view unavailable)")
		return b.String()
	}
	skills := d.Skills()
	if len(skills) == 0 {
		b.WriteString(" none loaded")
	}
	for _, s := range skills {
		mode := "model-invocable"
		if s.Frontmatter.DisableModelInvocation {
			mode = "slash-only"
		}
		fmt.Fprintf(&b, "\n  %s  [%s, %s]", s.Frontmatter.Name, mode, humanBytes(len(s.Body)))
	}
	disabled := d.disabledSkillNames()
	if len(disabled) > 0 {
		b.WriteString("\n\ndisabled (in [skills], file kept on disk):")
		for _, n := range disabled {
			b.WriteString("\n  " + n)
		}
	}
	return b.String()
}

// skillsToggle writes the toggle to config and syncs the registry: disable
// removes the /name command now (the prompt ad and the materialized tool
// follow on the next session); enable re-registers from a fresh rescan when
// the skill exists on disk.
func (d *SurfaceDeps) skillsToggle(name string, disable bool) string {
	if d.ConfigPath == "" {
		return "skills: config writes unavailable in this context (read-only surface)"
	}
	if err := config.SetSkillsDisabled(d.ConfigPath, name, disable); err != nil {
		return fmt.Sprintf("skills: %s %s failed: %v", map[bool]string{true: "disable", false: "enable"}[disable], name, err)
	}
	if disable {
		if d.registry != nil {
			d.registry.Remove(name) // ok when the skill had no command (shadowed etc.)
		}
		return fmt.Sprintf("skills: %s disabled — written to config; /%s removed from the command list now; prompt ads and the materialized tool update on the next session", name, name)
	}
	// Enable: refresh the whole view so a newly created skill file is picked
	// up the same way (and the just-enabled skill rejoins every face it can).
	if msg := d.skillsReload(); !strings.HasPrefix(msg, "skills: reload") {
		return msg
	}
	return fmt.Sprintf("skills: %s enabled — written to config; /%s re-registered (prompt ads and the materialized tool update on the next session)", name, name)
}

// skillsInfo renders one skill's frontmatter summary.
func (d *SurfaceDeps) skillsInfo(name string) string {
	if d.Skills == nil {
		return "skills: skill view unavailable"
	}
	for _, s := range d.Skills() {
		if !strings.EqualFold(s.Frontmatter.Name, name) {
			continue
		}
		var b strings.Builder
		fmt.Fprintf(&b, "skill %s:\n", s.Frontmatter.Name)
		fmt.Fprintf(&b, "  description: %s\n", s.Frontmatter.Description)
		fmt.Fprintf(&b, "  path: %s\n", s.Path)
		fmt.Fprintf(&b, "  invocation: %s\n", map[bool]string{true: "slash-only (disable-model-invocation)", false: "model-invocable"}[s.Frontmatter.DisableModelInvocation])
		fmt.Fprintf(&b, "  body: %s\n", humanBytes(len(s.Body)))
		return b.String()
	}
	// Not in the loaded view: distinguish "disabled" from "unknown".
	for _, n := range d.disabledSkillNames() {
		if strings.EqualFold(n, name) {
			return fmt.Sprintf("skill %s: disabled (in [skills] disabled; file kept on disk)", name)
		}
	}
	return fmt.Sprintf("skills: no skill named %q", name)
}

// skillsReload rescans the skills directory, swaps the caller's view, and
// re-syncs the registry so /name commands match the new view. The prompt ads
// and materialized tools stay as loaded at startup (session-frozen, D-7).
func (d *SurfaceDeps) skillsReload() string {
	if d.SkillsDir == "" || d.SetSkills == nil {
		return "skills: reload unavailable in this context"
	}
	skills, err := runtime.LoadSkillsDir(d.SkillsDir)
	if err != nil {
		return fmt.Sprintf("skills: reload failed: %v", err)
	}
	// Apply the config filter so the reloaded view matches what a fresh
	// session would load (disabled skills stay off every face).
	cfg, cfgErr := config.LoadFileConfig(d.ConfigPath)
	if cfgErr == nil && len(cfg.Skills.Disabled) > 0 {
		filtered := skills[:0:0]
		for _, s := range skills {
			if !cfg.Skills.SkillDisabled(s.Frontmatter.Name) {
				filtered = append(filtered, s)
			}
		}
		skills = filtered
	}
	// Registry sync: drop every skill command not in the new view, add the
	// new ones (same-tier AddSkill overrides survive re-registration).
	if d.registry != nil {
		current := map[string]bool{}
		for _, s := range skills {
			current[s.Frontmatter.Name] = true
		}
		if d.Skills != nil {
			for _, s := range d.Skills() {
				if !current[s.Frontmatter.Name] {
					d.registry.Remove(s.Frontmatter.Name)
				}
			}
		}
		for _, s := range skills {
			d.registry.AddSkill(s.SlashCommand())
		}
	}
	d.SetSkills(skills)
	return fmt.Sprintf("skills: reloaded %d skill(s) from %s (slash list synced; prompt ads and materialized tools refresh next session)", len(skills), d.SkillsDir)
}

// mcpAction implements /mcp: bare list, server enable/disable, per-tool
// enable/disable, and reload. Subcommand grammar mirrors the spec §4 table.
func (d *SurfaceDeps) mcpAction(args string) string {
	fields := strings.Fields(args)
	sub := ""
	if len(fields) > 0 {
		sub = strings.ToLower(fields[0])
	}
	switch sub {
	case "":
		return d.mcpList()
	case "enable", "disable":
		if len(fields) < 2 {
			return fmt.Sprintf("mcp: usage: /mcp %s <server>", sub)
		}
		return d.mcpServerToggle(fields[1], sub == "enable")
	case "tool":
		// /mcp tool enable|disable <server> <tool>
		if len(fields) < 4 || (fields[1] != "enable" && fields[1] != "disable") {
			return "mcp: usage: /mcp tool enable|disable <server> <tool>"
		}
		return d.mcpToolToggle(fields[2], fields[3], fields[1] == "disable")
	case "reload":
		if len(fields) < 2 {
			return "mcp: usage: /mcp reload <server>"
		}
		return d.mcpReload(fields[1])
	default:
		return fmt.Sprintf("mcp: unknown subcommand %q (want enable | disable | tool | reload)", sub)
	}
}

// mcpList renders the server face. No configured servers is a NEUTRAL state —
// a sentence, never an error and never a bare 0 (spec §5, usage-counter
// convention).
func (d *SurfaceDeps) mcpList() string {
	if d.MCP == nil {
		return "mcp: no MCP servers configured ([[mcp.servers]] in config.toml)"
	}
	sts := d.MCP.Status()
	if len(sts) == 0 {
		return "mcp: no MCP servers configured ([[mcp.servers]] in config.toml)"
	}
	var b strings.Builder
	b.WriteString("mcp servers:")
	for _, st := range sts {
		state := "connected"
		switch {
		case !st.Enabled:
			state = "disabled (config)"
		case !st.Connected:
			state = "not connected"
		}
		fmt.Fprintf(&b, "\n  %s  [%s, %d tools", st.Name, state, st.ToolCount)
		if st.DisabledCount > 0 {
			fmt.Fprintf(&b, ", %d hidden", st.DisabledCount)
		}
		b.WriteString("]")
		if st.ServerInfo != "" {
			fmt.Fprintf(&b, " — %s", st.ServerInfo)
		}
		if st.Error != "" {
			fmt.Fprintf(&b, "\n    error: %s", st.Error)
		}
	}
	return b.String()
}

// mcpServerToggle flips the server-level switch (which outranks per-tool: a
// disabled server has no connection, so all its tools are off the face
// regardless of disabled_tools — spec §3.1). Live: the connection closes (or
// relaunches) now; the declared tool face follows next session (D-7).
func (d *SurfaceDeps) mcpServerToggle(server string, enable bool) string {
	if d.MCP == nil {
		return "mcp: no MCP servers configured"
	}
	verb := map[bool]string{true: "enable", false: "disable"}[enable]
	ok, err := d.MCP.SetServerEnabled(context.Background(), server, enable)
	if !ok {
		return fmt.Sprintf("mcp: no server named %q", server)
	}
	if err != nil {
		return fmt.Sprintf("mcp: %s %s: reconnect failed: %v (config written state preserved; retry with /mcp enable %s)", verb, server, err, server)
	}
	msg := fmt.Sprintf("mcp: %s %s", verb, server)
	if enable {
		msg += " — reconnected"
	} else {
		msg += " — connection closed now, its tools off the face (next session for the declared face)"
	}
	if d.ConfigPath == "" {
		return msg + "\n(config writes unavailable here: session-only, not persisted)"
	}
	if err := config.SetMCPServerEnabled(d.ConfigPath, server, enable); err != nil {
		return msg + fmt.Sprintf("\n(config write FAILED: %v — the live toggle is session-only)", err)
	}
	return msg + "\n(written to config)"
}

// mcpToolToggle flips the per-tool switch: config write + live manager
// mutation. The connection is kept either way (stash semantics), so
// re-enabling never re-runs tools/list.
func (d *SurfaceDeps) mcpToolToggle(server, tool string, disable bool) string {
	if d.MCP == nil {
		return "mcp: no MCP servers configured"
	}
	if !d.MCP.SetToolDisabled(server, tool, disable) {
		return fmt.Sprintf("mcp: no server named %q", server)
	}
	verb := map[bool]string{true: "disable", false: "enable"}[disable]
	msg := fmt.Sprintf("mcp: %s %s %s — %s", verb, server, tool,
		map[bool]string{true: "hidden from the face, connection kept", false: "restored to the face (cached tools/list reused, no re-list)"}[disable])
	if d.ConfigPath == "" {
		return msg + "\n(config writes unavailable here: session-only, not persisted)"
	}
	if err := config.SetMCPToolDisabled(d.ConfigPath, server, tool, disable); err != nil {
		return msg + fmt.Sprintf("\n(config write FAILED: %v — the live toggle is session-only)", err)
	}
	return msg + "\n(written to config; declared face updates next session)"
}

// mcpReload re-runs tools/list for one server and reports the minimax-style
// double-check result: a changed schema snapshot is announced, not swapped
// silently.
func (d *SurfaceDeps) mcpReload(server string) string {
	if d.MCP == nil {
		return "mcp: no MCP servers configured"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	changed, err := d.MCP.Reload(ctx, server)
	if err != nil {
		return fmt.Sprintf("mcp: reload %s failed: %v", server, err)
	}
	if changed {
		return fmt.Sprintf("mcp: %s reloaded — schema snapshot CHANGED (announced, not silent); new snapshot in effect", server)
	}
	return fmt.Sprintf("mcp: %s reloaded — schema snapshot unchanged", server)
}

// humanBytes renders a byte count for the /skills list.
func humanBytes(n int) string {
	switch {
	case n >= 1024*1024:
		return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
	case n >= 1024:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// SkillRow is one row of the TUI skills panel (T7.3 interactive redesign,
// grok ExtensionsModal Skills-tab alignment): an enabled skill from the live
// view, or a config-disabled name whose file stays on disk.
type SkillRow struct {
	Name        string
	Description string
	// Mode is "model-invocable" or "slash-only" for enabled rows.
	Mode string
	// Disabled marks a config-disabled skill; toggling it re-registers the
	// /name command through the reload path.
	Disabled bool
	Bytes    int
}

// SkillRows lists the skills panel population: the enabled view rows plus the
// config-disabled names. A name present in both renders ONCE, as disabled —
// the config switch is the face of record (the loaded view keeps serving the
// session until the next one, but the panel manages the switch).
func (d *SurfaceDeps) SkillRows() []SkillRow {
	disabledNames := d.disabledSkillNames()
	disabled := make(map[string]bool, len(disabledNames))
	for _, n := range disabledNames {
		disabled[strings.ToLower(n)] = true
	}
	emitted := make(map[string]bool, len(disabledNames))
	var out []SkillRow
	if d.Skills != nil {
		for _, s := range d.Skills() {
			name := s.Frontmatter.Name
			if disabled[strings.ToLower(name)] {
				// Disabled in place: the row keeps its position but flips to
				// the disabled face (config is the switch of record).
				out = append(out, SkillRow{Name: name, Disabled: true})
				emitted[strings.ToLower(name)] = true
				continue
			}
			mode := "model-invocable"
			if s.Frontmatter.DisableModelInvocation {
				mode = "slash-only"
			}
			out = append(out, SkillRow{
				Name:        name,
				Description: s.Frontmatter.Description,
				Mode:        mode,
				Bytes:       len(s.Body),
			})
		}
	}
	// Config-disabled names with no view entry (file deleted/moved) trail.
	for _, n := range disabledNames {
		if !emitted[strings.ToLower(n)] {
			out = append(out, SkillRow{Name: n, Disabled: true})
		}
	}
	return out
}

// ToggleSkill flips a skill's [skills] disabled switch (config write +
// registry sync). It is the panel-facing wrapper of the /skills disable|
// enable path and returns the status message for the transcript.
func (d *SurfaceDeps) ToggleSkill(name string, disable bool) string {
	return d.skillsToggle(name, disable)
}

// MCPServerRow is one row of the TUI MCP panel (grok ExtensionsModal MCP-tab
// alignment): a configured server with its live connection state.
type MCPServerRow struct {
	Name    string
	State   string // "connected" / "not connected" / "disabled (config)"
	Info    string
	Error   string
	Tools   int
	Hidden  int // per-tool disabled count
	Enabled bool
}

// MCPServerRows lists the configured MCP servers with their live state.
func (d *SurfaceDeps) MCPServerRows() []MCPServerRow {
	if d.MCP == nil {
		return nil
	}
	var out []MCPServerRow
	for _, st := range d.MCP.Status() {
		state := "connected"
		switch {
		case !st.Enabled:
			state = "disabled (config)"
		case !st.Connected:
			state = "not connected"
		}
		out = append(out, MCPServerRow{
			Name:    st.Name,
			State:   state,
			Info:    st.ServerInfo,
			Error:   st.Error,
			Tools:   st.ToolCount,
			Hidden:  st.DisabledCount,
			Enabled: st.Enabled,
		})
	}
	return out
}

// MCPToolRow is one tool row of the TUI MCP panel (T7.3 实测反馈: the panel
// drills into a server's tools — grok's /mcps modal shape — instead of only
// counting them): the server-local tool name, its advertised description, and
// the per-tool switch state.
type MCPToolRow struct {
	Name        string
	Description string
	Disabled    bool
}

// MCPToolRows lists one server's advertised tools with their switch state.
// ok=false when no such server exists; a connected server that advertises no
// tools yields an empty slice. Not-connected servers yield nil with ok=true —
// there is nothing to list, which the panel renders as an empty expansion.
func (d *SurfaceDeps) MCPToolRows(server string) ([]MCPToolRow, bool) {
	if d.MCP == nil {
		return nil, false
	}
	for _, st := range d.MCP.Status() {
		if st.Name != server {
			continue
		}
		out := make([]MCPToolRow, 0, len(st.Tools))
		for _, t := range st.Tools {
			out = append(out, MCPToolRow{Name: t.Name, Description: t.Description, Disabled: t.Disabled})
		}
		return out, true
	}
	return nil, false
}

// ToggleMCPServer flips a server-level switch (live reconnect/close + config
// write). It is the panel-facing wrapper of the /mcp enable|disable path.
func (d *SurfaceDeps) ToggleMCPServer(server string, enable bool) string {
	return d.mcpServerToggle(server, enable)
}

// ToggleMCPTool flips a per-tool switch (config write + live manager stash
// semantics: the connection is kept either way). It is the panel-facing
// wrapper of the /mcp tool enable|disable path.
func (d *SurfaceDeps) ToggleMCPTool(server, tool string, disable bool) string {
	return d.mcpToolToggle(server, tool, disable)
}
