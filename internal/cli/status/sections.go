// The T6.9 status extensions: MCP, Skills, and Permissions sections appended
// before Telemetry (spec slash-config-surface.md §5). The data comes through
// OPTIONAL capabilities the host may implement — cli.Host itself stays
// unchanged, so a front-end (or test host) that carries no MCP manager or
// skill view simply omits the capability and the section renders its neutral
// state, never an error and never a bare 0.
package status

import (
	"fmt"
	"io"

	"github.com/smallnest/pigo/internal/cli"
	"github.com/smallnest/pigo/internal/cli/ui"
	"github.com/smallnest/pigo/internal/mcp"
	"github.com/smallnest/pigo/internal/runtime"
	"github.com/smallnest/pigo/internal/toolrules"
)

// MCPSurfaceSource is the optional capability exposing the live MCP manager.
type MCPSurfaceSource interface {
	MCPSurface() *mcp.Manager
}

// SkillsSurfaceSource is the optional capability exposing the current skill
// view (the filtered LoadSkills result) plus the config's disabled names.
type SkillsSurfaceSource interface {
	SkillsSurface() (loaded []*runtime.Skill, disabled []string)
}

// PermEngineSource is the optional capability exposing the permission engine
// for the rules count.
type PermEngineSource interface {
	PermEngine() *toolrules.Engine
}

// printSurfaceStatus prints the MCP / Skills / Permissions sections. Each
// degrades to its neutral state when the host lacks the capability or the
// face is empty (spec §5: "no MCP servers configured", not 0).
func printSurfaceStatus(out io.Writer, color bool, host cli.Host) {
	fmt.Fprintln(out)
	printMCPSection(out, color, host)
	fmt.Fprintln(out)
	printSkillsSection(out, color, host)
	fmt.Fprintln(out)
	printPermissionsSection(out, color, host)
}

func printMCPSection(out io.Writer, color bool, host cli.Host) {
	fmt.Fprintf(out, "%s\n", ui.Colorize(color, ui.Bold, "mcp:"))
	src, ok := host.(MCPSurfaceSource)
	if !ok || src.MCPSurface() == nil {
		fmt.Fprintf(out, "  %s\n", ui.Colorize(color, ui.Dim, "no MCP servers configured"))
		return
	}
	sts := src.MCPSurface().Status()
	if len(sts) == 0 {
		fmt.Fprintf(out, "  %s\n", ui.Colorize(color, ui.Dim, "no MCP servers configured"))
		return
	}
	for _, st := range sts {
		state := "connected"
		switch {
		case !st.Enabled:
			state = "disabled (config)"
		case !st.Connected:
			state = "not connected"
		}
		line := fmt.Sprintf("  %s [%s, %d tools", st.Name, state, st.ToolCount)
		if st.DisabledCount > 0 {
			line += fmt.Sprintf(", %d hidden", st.DisabledCount)
		}
		line += "]"
		if st.ServerInfo != "" {
			line += " — " + st.ServerInfo
		}
		fmt.Fprintln(out, line)
		if st.Error != "" {
			fmt.Fprintf(out, "    %s %s\n", ui.Colorize(color, ui.Dim, "error:"), st.Error)
		}
	}
}

func printSkillsSection(out io.Writer, color bool, host cli.Host) {
	fmt.Fprintf(out, "%s\n", ui.Colorize(color, ui.Bold, "skills:"))
	src, ok := host.(SkillsSurfaceSource)
	if !ok {
		fmt.Fprintf(out, "  %s\n", ui.Colorize(color, ui.Dim, "skill view unavailable"))
		return
	}
	loaded, disabled := src.SkillsSurface()
	model := 0
	for _, s := range loaded {
		if !s.Frontmatter.DisableModelInvocation {
			model++
		}
	}
	fmt.Fprintf(out, "  %s %d (%s %d)\n", ui.Colorize(color, ui.Dim, "loaded:"), len(loaded),
		ui.Colorize(color, ui.Dim, "model-invocable:"), model)
	if len(disabled) > 0 {
		fmt.Fprintf(out, "  %s %d%s\n", ui.Colorize(color, ui.Dim, "disabled:"), len(disabled), namesSuffix(disabled))
	}
}

func printPermissionsSection(out io.Writer, color bool, host cli.Host) {
	fmt.Fprintf(out, "%s\n", ui.Colorize(color, ui.Bold, "permissions & confirmations:"))
	fmt.Fprintf(out, "  %s %s\n", ui.Colorize(color, ui.Dim, "trust:"), trustStatus(host.Trust(), host.Cwd()))
	if src, ok := host.(PermEngineSource); ok && src.PermEngine() != nil {
		session := src.PermEngine().SessionRules()
		fmt.Fprintf(out, "  %s %d\n", ui.Colorize(color, ui.Dim, "session rules:"), len(session))
	}
	fmt.Fprintf(out, "  %s\n", ui.Colorize(color, ui.Dim, "confirmations: asked inline per side-effect call (no queue)"))
}
