// This file holds the Parse face of the T7.7 contract commands (spec
// wiki/port/slash-command-surface.md §4): pure argument-string → typed Intent
// functions with no live state and no side effects. Usage errors carry the
// same message text the former Action closures printed, so front-end output is
// unchanged; the Execute face lives in executor.go.
package prompts

import (
	"fmt"
	"strings"

	"github.com/smallnest/pigo/internal/runtime"
)

// parseModel implements /model's grammar: a bare invocation views the active
// model, "<id> [effort]" switches (grok grammar — the pair switches model AND
// reasoning level in one line), and anything else is refused without touching
// live state.
func parseModel(args string) (runtime.Intent, error) {
	fields := strings.Fields(args)
	if len(fields) == 0 {
		return runtime.Intent{Kind: runtime.IntentModelShow}, nil
	}
	if len(fields) > 2 {
		return runtime.Intent{}, fmt.Errorf("model: unexpected extra argument %q (usage: /model <id> [effort])", fields[2])
	}
	it := runtime.Intent{Kind: runtime.IntentModelSwitch, ModelID: fields[0]}
	if len(fields) > 1 {
		v, ok := validThinkingLevel(fields[1])
		if !ok {
			return runtime.Intent{}, fmt.Errorf("model: unknown effort level %q (want off|minimal|low|medium|high|xhigh|max)", fields[1])
		}
		it.Effort = string(v)
	}
	return it, nil
}

// parseModels implements /models: bare (or a provider filter) lists the
// catalog, "fetch" queries the live endpoint.
func parseModels(args string) (runtime.Intent, error) {
	if strings.TrimSpace(args) == "fetch" {
		return runtime.Intent{Kind: runtime.IntentModelsFetch}, nil
	}
	return runtime.Intent{Kind: runtime.IntentModelsList, Filter: strings.TrimSpace(args)}, nil
}

// parseThink implements /think (and its /effect alias): bare views the current
// level, a known level switches it.
func parseThink(args string) (runtime.Intent, error) {
	lvl := strings.TrimSpace(args)
	if lvl == "" {
		return runtime.Intent{Kind: runtime.IntentThinkShow}, nil
	}
	v, ok := validThinkingLevel(lvl)
	if !ok {
		return runtime.Intent{}, fmt.Errorf("think: invalid level %q (want off|minimal|low|medium|high|xhigh|max)", lvl)
	}
	return runtime.Intent{Kind: runtime.IntentThinkSet, Level: string(v)}, nil
}

// parseSkills implements /skills's subcommand grammar (slash-config-surface
// spec §4 table).
func parseSkills(args string) (runtime.Intent, error) {
	fields := strings.Fields(args)
	sub := ""
	if len(fields) > 0 {
		sub = strings.ToLower(fields[0])
	}
	switch sub {
	case "":
		return runtime.Intent{Kind: runtime.IntentSkillsList}, nil
	case "disable", "enable":
		if len(fields) < 2 {
			return runtime.Intent{}, fmt.Errorf("skills: usage: /skills %s <name>", sub)
		}
		return runtime.Intent{Kind: runtime.IntentSkillToggle, SkillName: fields[1], SkillDisable: sub == "disable"}, nil
	case "info":
		if len(fields) < 2 {
			return runtime.Intent{}, fmt.Errorf("skills: usage: /skills info <name>")
		}
		return runtime.Intent{Kind: runtime.IntentSkillInfo, SkillName: fields[1]}, nil
	case "reload":
		return runtime.Intent{Kind: runtime.IntentSkillsReload}, nil
	default:
		return runtime.Intent{}, fmt.Errorf("skills: unknown subcommand %q (want list | disable | enable | info | reload)", sub)
	}
}

// parseMCP implements /mcp's subcommand grammar.
func parseMCP(args string) (runtime.Intent, error) {
	fields := strings.Fields(args)
	sub := ""
	if len(fields) > 0 {
		sub = strings.ToLower(fields[0])
	}
	switch sub {
	case "":
		return runtime.Intent{Kind: runtime.IntentMCPList}, nil
	case "enable", "disable":
		if len(fields) < 2 {
			return runtime.Intent{}, fmt.Errorf("mcp: usage: /mcp %s <server>", sub)
		}
		return runtime.Intent{Kind: runtime.IntentMCPServerToggle, MCPServer: fields[1], ServerEnable: sub == "enable"}, nil
	case "tool":
		// /mcp tool enable|disable <server> <tool>
		if len(fields) < 4 || (fields[1] != "enable" && fields[1] != "disable") {
			return runtime.Intent{}, fmt.Errorf("mcp: usage: /mcp tool enable|disable <server> <tool>")
		}
		return runtime.Intent{Kind: runtime.IntentMCPToolToggle, MCPServer: fields[2], MCPTool: fields[3], ToolDisable: fields[1] == "disable"}, nil
	case "reload":
		if len(fields) < 2 {
			return runtime.Intent{}, fmt.Errorf("mcp: usage: /mcp reload <server>")
		}
		return runtime.Intent{Kind: runtime.IntentMCPReload, MCPServer: fields[1]}, nil
	default:
		return runtime.Intent{}, fmt.Errorf("mcp: unknown subcommand %q (want enable | disable | tool | reload)", sub)
	}
}

// parseStatus accepts the invocation as-is: /status has no argument grammar
// today (the report renders regardless), matching the promoted command's
// behavior.
func parseStatus(string) (runtime.Intent, error) {
	return runtime.Intent{Kind: runtime.IntentStatusShow}, nil
}

// parseSession refuses arguments — the summary takes none, and the old
// fall-through was a silent stub no-op (T7.7: explicit rejection instead).
func parseSession(args string) (runtime.Intent, error) {
	if strings.TrimSpace(args) != "" {
		return runtime.Intent{}, fmt.Errorf("session: takes no arguments")
	}
	return runtime.Intent{Kind: runtime.IntentSessionShow}, nil
}

// parseCompact refuses arguments: the compactor takes no user-focus hint yet
// (spec deviation register), so the argument form is an explicit rejection
// rather than the old silent stub no-op.
func parseCompact(args string) (runtime.Intent, error) {
	if strings.TrimSpace(args) != "" {
		return runtime.Intent{}, fmt.Errorf("compact: takes no arguments")
	}
	return runtime.Intent{Kind: runtime.IntentCompact}, nil
}

// parseMemory refuses arguments — the memory report renders the live state and
// takes none (the former intercept silently ignored them; T7.7: explicit
// rejection instead).
func parseMemory(args string) (runtime.Intent, error) {
	if strings.TrimSpace(args) != "" {
		return runtime.Intent{}, fmt.Errorf("memory: takes no arguments")
	}
	return runtime.Intent{Kind: runtime.IntentMemoryShow}, nil
}

// parseRebuild refuses arguments — the rebuild works from the persisted
// checkpoint and takes none.
func parseRebuild(args string) (runtime.Intent, error) {
	if strings.TrimSpace(args) != "" {
		return runtime.Intent{}, fmt.Errorf("rebuild: takes no arguments")
	}
	return runtime.Intent{Kind: runtime.IntentRebuild}, nil
}
