// Typed slash-command contract (T7.7, spec wiki/port/slash-command-surface.md
// §4): the declaration face carries identity, applicability and audience as
// data; Parse turns an invocation into a closed set of typed Intents; the
// front-end Executor runs them against loop state. Intent and SessionState are
// pure data — no pointers into front-end state — so a future server-side
// execution layer can receive them over the wire (spec §7 future-split seam).

package runtime

// Audience declares who may invoke a command. It is the model-authored-input
// gate (grok ModelAuthoredEligibility alignment): fail-closed — a command is
// human-only unless it explicitly opts in.
type Audience int

const (
	// AudienceHumanOnly admits human-typed input only. Model-authored text
	// carrying the command name is refused: the model may not compact the
	// context, switch models or toggle surfaces on its own. This is the zero
	// value, so a command that forgets to declare an audience stays locked.
	AudienceHumanOnly Audience = iota
	// AudienceHumanAndModel additionally admits model-authored invocations
	// (e.g. a prompt-like command a model may expand on the user's behalf).
	AudienceHumanAndModel
)

func (a Audience) String() string {
	switch a {
	case AudienceHumanAndModel:
		return "human+model"
	default:
		return "human-only"
	}
}

// SessionState is the serializable snapshot of the front-end loop state that
// applicability predicates (Offered) and the interactive projections consult.
// Front-ends fill it from their own loop state; carrying only primitives keeps
// it wire-ready for the future server split (spec §7).
type SessionState struct {
	// HasSession reports whether an active session is bound (pickers, stats
	// and compaction degrade to explicit notices without one).
	HasSession bool
	// Running reports whether an agent run is in flight (interactive
	// projections refuse to open panels mid-run).
	Running bool
}

// IntentKind enumerates the closed set of typed command intents (T7.7 §4.2).
// The set is closed on purpose: adding a command means adding a kind here and
// an Execute arm in the front-end executor, never a string dispatch.
type IntentKind int

const (
	// IntentModelShow reports the active model (bare /model).
	IntentModelShow IntentKind = iota
	// IntentModelSwitch switches the active model (and optionally the
	// reasoning effort) — /model <id> [effort].
	IntentModelSwitch
	// IntentModelsList lists the preset/profile catalog — /models [filter].
	IntentModelsList
	// IntentModelsFetch fetches the live endpoint catalog — /models fetch.
	IntentModelsFetch
	// IntentThinkShow reports the reasoning-effort level (bare /think).
	IntentThinkShow
	// IntentThinkSet switches the reasoning-effort level — /think <level>.
	IntentThinkSet
	// IntentSkillsList lists the skill face — bare /skills.
	IntentSkillsList
	// IntentSkillToggle enables or disables one skill — /skills enable|disable <name>.
	IntentSkillToggle
	// IntentSkillInfo shows one skill's summary — /skills info <name>.
	IntentSkillInfo
	// IntentSkillsReload rescans the skills directory — /skills reload.
	IntentSkillsReload
	// IntentMCPList lists configured MCP servers — bare /mcp.
	IntentMCPList
	// IntentMCPServerToggle enables or disables one server — /mcp enable|disable <server>.
	IntentMCPServerToggle
	// IntentMCPToolToggle flips one tool switch — /mcp tool enable|disable <server> <tool>.
	IntentMCPToolToggle
	// IntentMCPReload re-runs tools/list for one server — /mcp reload <server>.
	IntentMCPReload
	// IntentLSPShow lists the LSP server state — bare /lsp (the REPL's
	// degraded projection of the TUI panel).
	IntentLSPShow
	// IntentLSPServerToggle flips the project-layer LSP switch —
	// /lsp enable|disable [server].
	IntentLSPServerToggle
	// IntentStatusShow renders the runtime status report — /status.
	IntentStatusShow
	// IntentSessionShow renders the session summary — /session.
	IntentSessionShow
	// IntentCompact compacts the conversation now — /compact.
	IntentCompact
	// IntentMemoryShow renders the persistent-memory + infinite-context
	// report — /memory.
	IntentMemoryShow
	// IntentRebuild reconstructs the conversation context from the session's
	// persisted checkpoint (falling back to summarizing compaction) — /rebuild.
	IntentRebuild
	// IntentDump writes the most recent failed provider request (raw request +
	// response) to the dump directory — /dump.
	IntentDump
)

func (k IntentKind) String() string {
	switch k {
	case IntentModelShow:
		return "model-show"
	case IntentModelSwitch:
		return "model-switch"
	case IntentModelsList:
		return "models-list"
	case IntentModelsFetch:
		return "models-fetch"
	case IntentThinkShow:
		return "think-show"
	case IntentThinkSet:
		return "think-set"
	case IntentSkillsList:
		return "skills-list"
	case IntentSkillToggle:
		return "skill-toggle"
	case IntentSkillInfo:
		return "skill-info"
	case IntentSkillsReload:
		return "skills-reload"
	case IntentMCPList:
		return "mcp-list"
	case IntentMCPServerToggle:
		return "mcp-server-toggle"
	case IntentMCPToolToggle:
		return "mcp-tool-toggle"
	case IntentMCPReload:
		return "mcp-reload"
	case IntentLSPShow:
		return "lsp-show"
	case IntentLSPServerToggle:
		return "lsp-server-toggle"
	case IntentStatusShow:
		return "status-show"
	case IntentSessionShow:
		return "session-show"
	case IntentCompact:
		return "compact"
	case IntentMemoryShow:
		return "memory-show"
	case IntentRebuild:
		return "rebuild"
	case IntentDump:
		return "dump"
	default:
		return "unknown"
	}
}

// Intent is one parsed slash invocation: pure data selected by Kind, with
// every field not meaningful for that kind left at its zero value. Polarities
// follow the executor methods each kind drives (skills and per-tool toggles
// speak "disable", the server toggle speaks "enable"), so transcription
// between parse and execute stays literal.
type Intent struct {
	Kind IntentKind

	// IntentModelSwitch: the model/profile id and an optional reasoning
	// effort (grok grammar "/model <id> [effort]").
	ModelID string
	Effort  string
	// IntentModelsList: optional provider-name filter.
	Filter string
	// IntentThinkSet: the reasoning-effort level.
	Level string
	// IntentSkillToggle / IntentSkillInfo: the skill name; SkillDisable is
	// the toggle direction (true = disable).
	SkillName    string
	SkillDisable bool
	// IntentMCPServerToggle / IntentMCPToolToggle / IntentMCPReload: the
	// server (and tool) names plus each toggle's direction — ServerEnable
	// (true = enable, matching the manager's reconnect-on-enable semantics)
	// and ToolDisable (true = disable, stash semantics).
	MCPServer    string
	ServerEnable bool
	MCPTool      string
	ToolDisable  bool
	// IntentLSPServerToggle: the optional server name (empty = the one
	// configured server) and the toggle direction (LSPEnable, true = enable —
	// the project switch speaks "enable", mirroring the server toggle).
	LSPServer  string
	LSPEnable  bool
	// IntentCompact: optional free-text user focus for the summarizer (grok
	// Compact{user_context}). Reserved: the compactor takes no focus argument
	// yet (spec deviation register), so Parse currently refuses arguments.
	UserContext string
}
