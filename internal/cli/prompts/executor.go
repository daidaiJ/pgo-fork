// This file holds the Executor (T7.7 §4.3): the execute face of the slash
// contract. The registry resolves an invocation into a typed Intent; this
// Executor runs it against the loop state the former Action closures captured
// inline. One executor per front-end run: the fields are the collaborators
// each surface supplies, and the outcome is the same SlashOutcome shape the
// front-ends already project (Kind SlashAction), so the REPL prints Message
// and the TUI renders a system block exactly as before.
package prompts

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/cli"
	"github.com/smallnest/pigo/internal/provider"
	"github.com/smallnest/pigo/internal/reqdump"
	"github.com/smallnest/pigo/internal/runtime"
)

// Executor executes typed slash Intents against one front-end's loop state.
// Live and Surface back the model/think and config-surface commands; Status,
// Session and Compact are the loop-owned projections the front-end supplies
// (nil → an explicit unavailability message, never a silent no-op).
type Executor struct {
	// Live is the mutable run config the model/think commands mutate.
	Live *cli.LiveConfig
	// Creds resolves credentials for /model profile switches and /models
	// fetch. May be nil (fetch then reports unavailability).
	Creds *provider.CredentialStore
	// Surface is the config-surface face (/skills, /mcp). May be nil — those
	// commands then report unavailability.
	Surface *SurfaceDeps
	// Status renders the promoted /status report (REPL: the full
	// status.RunStatus through its Host; TUI: the same shared renderer over
	// its session). Nil → unavailable notice.
	Status func() string
	// Session renders the /session summary. Nil → unavailable notice.
	Session func() string
	// Compact runs a manual compaction with loop ownership (REPL: blocking
	// stream + PersistTurn; the TUI projects the ProjCompact face off the tea
	// loop instead). Nil → unavailable notice.
	Compact func(userContext string) string
	// Memory renders the /memory report over the front-end's memory state
	// (both interactive front-ends share memstatus.RunMemory). Nil →
	// unavailable notice.
	Memory func() string
	// Rebuild runs a manual context rebuild with loop ownership (REPL:
	// blocking runManualRebuild core + PersistTurn; the TUI projects the
	// ProjRebuild face off the tea loop instead). Nil → unavailable notice.
	Rebuild func() string
	// DumpSession reports the front-end's active session id at call time, used
	// to name a /dump directory (<session id>-<timestamp>). Nil (or "") falls
	// back to the recorder's in-flight session, published by the run loop, and
	// then to "unknown-session".
	DumpSession func() string
}

// Execute runs one parsed intent and returns the outcome to project.
func (x *Executor) Execute(it runtime.Intent) runtime.SlashOutcome {
	return runtime.SlashOutcome{Handled: true, Kind: runtime.SlashAction, Message: x.execute(it)}
}

// execute dispatches one intent to its implementation. The bodies are the
// former Action closures of /model, /models, /think and the SurfaceDeps
// methods behind /skills and /mcp, moved verbatim.
func (x *Executor) execute(it runtime.Intent) string {
	switch it.Kind {
	case runtime.IntentModelShow:
		if x.Live == nil {
			return "model: live config unavailable"
		}
		return fmt.Sprintf("model: %s (provider: %s)\nrun /models to see presets, or /model <id> [effort] to switch", x.Live.Model, x.Live.ProviderName)
	case runtime.IntentModelSwitch:
		return x.modelSwitch(it.ModelID, it.Effort)
	case runtime.IntentModelsList:
		if x.Live == nil {
			return "models: live config unavailable"
		}
		if it.Filter != "" {
			return presetListing(it.Filter)
		}
		// The config faces come first (T8.1 providers, then the T7.3
		// profiles), the preset catalog stays the catalog fallback.
		var parts []string
		if s := providerListing(x.Live); s != "" {
			parts = append(parts, s)
		}
		if s := profileListing(x.Live); s != "" {
			parts = append(parts, s)
		}
		parts = append(parts, presetListing(""))
		return strings.Join(parts, "\n\n")
	case runtime.IntentModelsFetch:
		return fetchModelCatalog(x.Live, x.Creds)
	case runtime.IntentThinkShow:
		if x.Live == nil {
			return "think: live config unavailable"
		}
		cur := x.Live.ThinkingLevel
		if cur == "" {
			cur = agentcore.ThinkingOff
		}
		return fmt.Sprintf("think: %s\nswitch with /think <off|minimal|low|medium|high|xhigh|max>", cur)
	case runtime.IntentThinkSet:
		v, ok := validThinkingLevel(it.Level)
		if !ok {
			// Parse already gates this; the check keeps a hand-built intent
			// from writing an unknown level into live state.
			return fmt.Sprintf("think: invalid level %q (want off|minimal|low|medium|high|xhigh|max)", it.Level)
		}
		if x.Live == nil {
			return "think: live config unavailable"
		}
		x.Live.ThinkingLevel = v
		return fmt.Sprintf("think level set to %s (applies to the next turn)", v)
	case runtime.IntentSkillsList:
		if x.Surface == nil {
			return "skills: config surface unavailable in this context"
		}
		return x.Surface.skillsList()
	case runtime.IntentSkillToggle:
		if x.Surface == nil {
			return "skills: config surface unavailable in this context"
		}
		return x.Surface.skillsToggle(it.SkillName, it.SkillDisable)
	case runtime.IntentSkillInfo:
		if x.Surface == nil {
			return "skills: config surface unavailable in this context"
		}
		return x.Surface.skillsInfo(it.SkillName)
	case runtime.IntentSkillsReload:
		if x.Surface == nil {
			return "skills: config surface unavailable in this context"
		}
		return x.Surface.skillsReload()
	case runtime.IntentMCPList:
		if x.Surface == nil {
			return "mcp: config surface unavailable in this context"
		}
		return x.Surface.mcpList()
	case runtime.IntentMCPServerToggle:
		if x.Surface == nil {
			return "mcp: config surface unavailable in this context"
		}
		return x.Surface.mcpServerToggle(it.MCPServer, it.ServerEnable)
	case runtime.IntentMCPToolToggle:
		if x.Surface == nil {
			return "mcp: config surface unavailable in this context"
		}
		return x.Surface.mcpToolToggle(it.MCPServer, it.MCPTool, it.ToolDisable)
	case runtime.IntentMCPReload:
		if x.Surface == nil {
			return "mcp: config surface unavailable in this context"
		}
		return x.Surface.mcpReload(it.MCPServer)
	case runtime.IntentLSPShow:
		if x.Surface == nil {
			return "lsp: config surface unavailable in this context"
		}
		return x.Surface.lspList()
	case runtime.IntentLSPServerToggle:
		if x.Surface == nil {
			return "lsp: config surface unavailable in this context"
		}
		return x.Surface.lspServerToggle(it.LSPEnable)
	case runtime.IntentStatusShow:
		if x.Status == nil {
			return "(status unavailable: no active session)"
		}
		return x.Status()
	case runtime.IntentSessionShow:
		if x.Session == nil {
			return "(session unavailable: no active session)"
		}
		return x.Session()
	case runtime.IntentCompact:
		if x.Compact == nil {
			return "(compact unavailable: no active session)"
		}
		return x.Compact(it.UserContext)
	case runtime.IntentMemoryShow:
		if x.Memory == nil {
			return "(memory unavailable: no memory state in this front-end)"
		}
		return x.Memory()
	case runtime.IntentRebuild:
		if x.Rebuild == nil {
			return "(rebuild unavailable: no active session)"
		}
		return x.Rebuild()
	case runtime.IntentDump:
		return x.dumpLastFailure()
	default:
		return fmt.Sprintf("unknown intent %s", it.Kind)
	}
}

// dumpLastFailure writes the most recent failed provider request to the dump
// directory and reports the path. Both interactive front-ends share it — the
// dump is front-end independent (the recorder holds the record, the run loop
// publishes the session id) — so there is no per-face hook. A failure the
// automatic dump already wrote is reported at its own path; with nothing
// recorded in this process the newest dump on disk for the session is named
// instead, so a resumed process can still find the earlier failure.
func (x *Executor) dumpLastFailure() string {
	session := ""
	if x.DumpSession != nil {
		session = x.DumpSession()
	}
	if session == "" {
		session = reqdump.Session()
	}
	root, err := reqdump.DefaultDir()
	if err != nil {
		return fmt.Sprintf("dump: %v", err)
	}
	path, err := reqdump.Write(root, session, time.Now())
	if errors.Is(err, reqdump.ErrNoRecord) {
		if latest, lerr := reqdump.Latest(root, session); lerr == nil && latest != "" {
			return fmt.Sprintf("nothing failed in this run; latest dump on disk: %s", latest)
		}
		return "dump: no failed provider request recorded yet (only a request that fails before streaming starts is captured)"
	}
	if err != nil {
		return fmt.Sprintf("dump: %v", err)
	}
	return fmt.Sprintf("dumped the last failed provider request to %s", path)
}

// modelSwitch applies /model <id> [effort]: a config profile ([models."<id>"],
// T7.3 实测反馈) is the switch face of record — the provider is rebuilt from
// the profile; a fetched-catalog id stays on the gateway that served it; any
// other id resolves through the heuristic provider chain. An explicit effort
// argument switches the reasoning level in the same line (grok grammar).
// Bodies moved verbatim from the former /model Action closure.
func (x *Executor) modelSwitch(id, effort string) string {
	live := x.Live
	if live == nil {
		return "model: live config unavailable"
	}
	applyEffort := func(msg string) string {
		if effort == "" {
			return msg
		}
		live.ThinkingLevel = agentcore.ThinkingLevel(effort)
		return fmt.Sprintf("%s; effort: %s (next turn)", msg, effort)
	}
	if key, prof, ok := live.ProfileFor(id); ok {
		return switchToProfile(live, x.Creds, key, prof, effort)
	}
	// An id from the fetched online catalog (issue #566) stays on the
	// gateway that served it: resolve with the live provider name explicit
	// instead of the heuristic chain, which could route a gateway-specific
	// id elsewhere. The live connection's proxy (T8.1) rides along.
	if providerName := live.ProviderName; len(live.FetchedModels) > 0 && slices.Contains(live.FetchedModels, id) {
		prov, name, err := provider.ResolveProviderWithProxy(id, live.BaseURL, live.Protocol, providerName, os.Getenv, live.Proxy)
		if err != nil {
			return fmt.Sprintf("model: cannot switch to %q: %v", id, err)
		}
		live.Model = id
		live.ProviderName = name
		live.Provider = prov
		// The compaction window follows the model: re-resolve against the
		// new model's catalog window (the explicit [compaction] max_context
		// cap stays applied, config still wins).
		live.ContextWindow = cli.ResolveContextWindow(prov, live.Model, live.MaxContext)
		live.MaxOutputTokens = cli.ResolveMaxOutputTokens(prov, live.Model)
		return applyEffort(fmt.Sprintf("model switched to %s (provider: %s, from fetched catalog)", id, name))
	}
	model := provider.CanonicalizeModel(id)
	prov, providerName, err := provider.ResolveProviderWithProxy(model, live.BaseURL, live.Protocol, "", os.Getenv, live.Proxy)
	if err != nil {
		return fmt.Sprintf("model: cannot switch to %q: %v", id, err)
	}
	live.Model = model
	live.ProviderName = providerName
	live.Provider = prov
	// The compaction window follows the model: re-resolve against the new
	// model's catalog window (the explicit [compaction] max_context cap
	// stays applied, config still wins).
	live.ContextWindow = cli.ResolveContextWindow(prov, live.Model, live.MaxContext)
	live.MaxOutputTokens = cli.ResolveMaxOutputTokens(prov, live.Model)
	return applyEffort(fmt.Sprintf("model switched to %s (provider: %s)", model, providerName))
}
