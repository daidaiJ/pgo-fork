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

	"github.com/smallnest/pigo/internal/agenttool"
	"github.com/smallnest/pigo/internal/cli/config"
	"github.com/smallnest/pigo/internal/lsp"
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
	// LSP is the workspace language server (T8.2, run.Env.LSP); nil when LSP
	// is disabled. The /lsp listing reads its status; the toggle writes the
	// project switch through LSPStore and applies live when non-nil.
	LSP *lsp.Manager
	// LSPStore persists the project-layer LSP switch (run.SetProjectLSPEnabled,
	// trust-gated); nil makes /lsp enable|disable refuse (headless-style
	// read-only surface).
	LSPStore func(enabled bool) error
	// LSPConfigured reports whether the project layer carries an lsp section
	// (informational for the listing); nil = unknown.
	LSPConfigured func() bool
	// Bash is the run's live bash tool (T8.4); nil when tools are off or the
	// policy removed it. The /shell listing reads its backend; the switch
	// swaps it live (the next command runs under the new backend).
	Bash *agenttool.BashTool
	// ShellStore persists the [shell] backend to the global config.toml
	// (config.SetShellBackend); nil makes /shell <backend> refuse (read-only
	// surface). The project layer carries no shell key (T8.4 user ruling).
	ShellStore func(backend string) error
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
func RegisterSurfaceCommands(reg *runtime.SlashRegistry, deps *SurfaceDeps) {
	deps.registry = reg
	reg.AddBuiltin(runtime.SlashCommand{
		Name:         "skills",
		ArgumentHint: "[list|disable|enable|info|reload]",
		Description:  "list skills or toggle one: /skills disable <name> (writes config; next-session effect)",
		Projection:   runtime.ProjSkillsPanel,
		Parse:        parseSkills,
	})
	reg.AddBuiltin(runtime.SlashCommand{
		Name:         "mcp",
		ArgumentHint: "[enable|disable <server> | tool enable|disable <server> <tool> | reload <server>]",
		Description:  "MCP server and per-tool switches, connection state and schema reload",
		Projection:   runtime.ProjMCPPanel,
		Parse:        parseMCP,
	})
	reg.AddBuiltin(runtime.SlashCommand{
		Name:         "lsp",
		ArgumentHint: "[enable|disable [server]]",
		Description:  "LSP server state and diagnostics counts; enable/disable writes the project switch",
		Projection:   runtime.ProjLSPPanel,
		Parse:        parseLSP,
	})
	reg.AddBuiltin(runtime.SlashCommand{
		Name:         "shell",
		ArgumentHint: "[<backend>]",
		Description:  "show or switch the shell backend (bash|powershell|pwsh|cmd|wsl); writes config, live for the next command",
		Projection:   runtime.ProjShellPanel,
		Parse:        parseShell,
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

// lspList renders the LSP face (T8.2): the configured server's state, live
// diagnostics load, and where the switch lives. A disabled LSP is a NEUTRAL
// state — a sentence with the enable paths, never an error (spec §5,
// usage-counter convention).
func (d *SurfaceDeps) lspList() string {
	if d.LSP == nil {
		msg := "lsp: disabled — enable with [lsp] enabled = true in config.toml, or {\"lsp\": {\"enabled\": true}} in ./.pigo/config.json (project layer)"
		if d.LSPConfigured != nil && d.LSPConfigured() {
			msg += "\n  (a project ./.pigo/config.json lsp section exists; it only applies when the directory is trusted)"
		}
		return msg
	}
	var b strings.Builder
	b.WriteString("lsp:")
	for _, st := range d.LSP.Status() {
		fmt.Fprintf(&b, "\n  %s  [%s]", st.Name, st.State)
		if st.ServerInfo != "" {
			fmt.Fprintf(&b, " — %s", st.ServerInfo)
		}
		if st.DiagFiles > 0 {
			fmt.Fprintf(&b, "\n    diagnostics: %d in %d file(s)", st.DiagTotal, st.DiagFiles)
		}
		if st.Error != "" {
			fmt.Fprintf(&b, "\n    error: %s", st.Error)
		}
	}
	b.WriteString("\n  tools: lsp_diagnostics, lsp_definition, lsp_references, lsp_hover, lsp_symbols (deferred — search_tools claims them)")
	return b.String()
}

// lspServerToggle writes the project-layer switch (trust-gated) and applies
// it live when the manager is in the session. When LSP is off at startup the
// face stays frozen for the session (the D-7 session-frozen semantics the
// skill face uses): the write lands, the next session picks it up.
func (d *SurfaceDeps) lspServerToggle(enable bool) string {
	verb := map[bool]string{true: "enable", false: "disable"}[enable]
	if d.LSPStore == nil {
		return "lsp: config writes unavailable in this context (read-only surface)"
	}
	if err := d.LSPStore(enable); err != nil {
		return fmt.Sprintf("lsp: %s failed: %v", verb, err)
	}
	if d.LSP == nil {
		return fmt.Sprintf("lsp: %s — written to ./.pigo/config.json; takes effect on the next session", verb)
	}
	d.LSP.SetEnabled(enable)
	detail := "server stopped"
	if enable {
		detail = "server starting in the background"
	}
	return fmt.Sprintf("lsp: %s — written to ./.pigo/config.json; live: %s", verb, detail)
}

// LSPServerToggle is the panel-facing wrapper of the /lsp enable|disable path.
func (d *SurfaceDeps) LSPServerToggle(enable bool) string { return d.lspServerToggle(enable) }

// LSPToolRows returns the deferred tool family rows for the /lsp panel's
// tool level (the panel-facing data face; empty when LSP is off).
func (d *SurfaceDeps) LSPToolRows() []string {
	if d.LSP == nil {
		return nil
	}
	return []string{"lsp_diagnostics", "lsp_definition", "lsp_references", "lsp_hover", "lsp_symbols"}
}

// shellList renders the shell face (T8.4): the selectable backends with the
// live one marked and where the switch persists. A missing bash tool is a
// NEUTRAL state (tools disabled / policy-removed), never an error.
func (d *SurfaceDeps) shellList() string {
	if d.Bash == nil {
		return "shell: bash tool unavailable (tools disabled or policy-removed)"
	}
	kind := d.Bash.ShellKind()
	var b strings.Builder
	if kind == agenttool.ShellKindCustom {
		b.WriteString("shell: custom (current — see config.toml [shell] command/args)")
	} else {
		fmt.Fprintf(&b, "shell: %s (current)", kind)
	}
	for _, name := range agenttool.ShellBackendNames() {
		spec, err := agenttool.ShellSpecFor(name)
		if err != nil {
			continue
		}
		fmt.Fprintf(&b, "\n  %s — %s %s", name, spec.Program, strings.Join(spec.Args, " "))
	}
	b.WriteString("\n  switch: /shell <backend> (writes config.toml [shell]; live: the next command)")
	return b.String()
}

// shellSwitch swaps the live backend and persists it (T8.4 user ruling: hot
// switch — the next command runs under the new backend — plus a global
// config.toml [shell] write; the project layer carries no shell key).
func (d *SurfaceDeps) shellSwitch(backend string) string {
	if d.Bash == nil {
		return "shell: bash tool unavailable (tools disabled or policy-removed)"
	}
	if d.ShellStore == nil {
		return "shell: config writes unavailable in this context (read-only surface)"
	}
	spec, err := agenttool.ShellSpecFor(backend)
	if err != nil {
		return fmt.Sprintf("shell: %v", err)
	}
	d.Bash.SetShellSpec(spec)
	if err := d.ShellStore(spec.Kind); err != nil {
		return fmt.Sprintf("shell: switched to %s for this session, but the config write failed: %v", spec.Kind, err)
	}
	msg := fmt.Sprintf("shell: switched to %s — written to config.toml; live: the next command runs under %s", spec.Kind, spec.Kind)
	if !agenttool.GuardableShellKind(spec.Kind) {
		msg += " (shellguard static analysis is skipped for this backend)"
	}
	return msg
}

// ShellSwitch is the panel-facing wrapper of the /shell <backend> path.
func (d *SurfaceDeps) ShellSwitch(backend string) string { return d.shellSwitch(backend) }
