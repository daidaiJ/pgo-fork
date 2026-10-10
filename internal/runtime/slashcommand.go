// This file implements slash-commands (US-029, #45): typed "/name" shortcuts a
// user invokes in the REPL. There are two sources, resolved with a fixed
// priority:
//
//   - Built-in commands are registered at compile time via RegisterBuiltin
//     (from init() in the fork's own code). They are always available.
//   - User commands are declarative markdown templates loaded from a directory
//     (mirrors the .../commands/*.md convention): the file name is the command
//     name and the body is a prompt template that may reference $ARGUMENTS.
//
// Conflict rule: same-name commands resolve by priority tier (built-in >
// project > global > package > settings > CLI); the higher tier wins and the
// loser is reported via Shadowed. Built-ins are load-bearing and always win.
// Within a tier, the last-added command overrides earlier ones (a re-load).
//
// There is deliberately no standalone plugin mechanism: a fork adds built-ins
// via init() registration, and external extensions go through MCP (deferred).
package runtime

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// SlashCommandSource identifies where a command came from, used for the
// conflict/priority rule and for display.
type SlashCommandSource int

const (
	// SourceBuiltin is a compile-time registered command (highest priority).
	SourceBuiltin SlashCommandSource = iota
	// SourceUser is a declarative markdown command template loaded from disk
	// (e.g. ~/.pigo/commands/*.md).
	SourceUser
	// SourceSkill is a skill loaded from ~/.agents/skills and surfaced as a
	// /skill-name command. It behaves like SourceUser for the built-in-wins
	// conflict rule; the finer tag is for display only (e.g. /status).
	SourceSkill
	// SourcePlugin is a command declared by a loaded plugin. It behaves like
	// SourceUser for the built-in-wins conflict rule; the finer tag is for
	// display only.
	SourcePlugin
)

func (s SlashCommandSource) String() string {
	switch s {
	case SourceBuiltin:
		return "builtin"
	case SourceSkill:
		return "skill"
	case SourcePlugin:
		return "plugin"
	default:
		return "user"
	}
}

// Badge is the short source marker every candidate surface shows next to a
// non-builtin command ("[skill]", "[plugin]", "[template]" in the TUI menu,
// the /help list and the REPL completion hint). Builtins — the untagged
// majority — return "". One source means the three surfaces cannot drift
// (T7.7 §6: /help, the completion menu and the panels list the same rows).
func (s SlashCommandSource) Badge() string {
	switch s {
	case SourceSkill:
		return "skill"
	case SourcePlugin:
		return "plugin"
	case SourceUser:
		return "template"
	default:
		return ""
	}
}

// SplitInvocation splits a raw input line into a slash-command name (without
// the leading "/") and its trimmed argument text. ok is false when the line is
// not a slash invocation. Both front-ends and the headless guard resolve the
// name through this one splitter, so "is this /name?" has a single answer.
func SplitInvocation(line string) (name, args string, ok bool) {
	trimmed := strings.TrimLeft(line, " \t")
	if !strings.HasPrefix(trimmed, "/") {
		return "", "", false
	}
	rest := trimmed[1:]
	if i := strings.IndexAny(rest, " \t"); i >= 0 {
		return rest[:i], strings.TrimSpace(rest[i+1:]), true
	}
	return rest, "", true
}

// Tier is the priority tier of a command, used to resolve same-name conflicts
// across sources (mirrors pi prompt-templates discovery priority). Higher tiers
// win; the loser is recorded in Shadowed. Within the same tier the last-added
// command wins (a re-load overrides). Skills and plugins are treated as
// Global-tier for priority - their finer Source label is for display only.
type Tier int

const (
	// Tier values are ordered lowest-to-highest priority: in a same-name
	// conflict the higher Tier value wins, so TierBuiltin always wins and
	// TierCLI always loses. Declared ascending so the natural > comparison
	// matches "higher priority wins".
	TierCLI Tier = iota
	// TierSettings is a prompt template referenced by the config.toml prompts array.
	TierSettings
	// TierPackage is a prompt template discovered from an installed package
	// source (distinct from one copied into the global dir).
	TierPackage
	// TierGlobal is a global user prompt template (e.g. ~/.pigo/prompts or the
	// legacy ~/.pigo/commands); also the tier used for skills and plugins.
	TierGlobal
	// TierProject is a project-local prompt template (e.g. .pigo/prompts).
	TierProject
	// TierBuiltin is a compile-time or instance built-in command (highest).
	TierBuiltin
)

func (t Tier) String() string {
	switch t {
	case TierBuiltin:
		return "builtin"
	case TierProject:
		return "project"
	case TierGlobal:
		return "global"
	case TierPackage:
		return "package"
	case TierSettings:
		return "settings"
	case TierCLI:
		return "cli"
	default:
		return "unknown"
	}
}

// ShadowedEntry records a command that lost a same-name conflict to a higher-
// tier command, for diagnostics. It carries the loser's name, tier, and source
// label so /help and the startup warning can say which source was shadowed.
type ShadowedEntry struct {
	Name   string
	Tier   Tier
	Source SlashCommandSource
}

// String renders a shadowed entry as "name (tier)" for log lines.
func (e ShadowedEntry) String() string { return fmt.Sprintf("%s (%s)", e.Name, e.Tier) }

// Projection declares the loop-owned interactive face the front-ends project
// for a command (T7.7 §4.4: the TUI opens its panel from the declaration, not
// from a per-name intercept list). The set is closed data: a front-end switches
// on it to build its projection of the face (grok: the client matches typed
// command results to open modals), while the REPL/headless projectors degrade a
// face they cannot project to the explicit unavailability notice ResolveOutcome
// returns. Zero value ProjNone: a text/argument command that resolves through
// its contract (Parse→Executor, Action, Run or Expand).
type Projection int

const (
	// ProjNone declares no loop face: the command resolves through its
	// executable contract (Parse/Action/Run/Expand).
	ProjNone Projection = iota
	// ProjQuit terminates the front-end loop (/exit, /quit).
	ProjQuit
	// ProjModelMenu is the bare-submit model chain dropdown (/model).
	ProjModelMenu
	// ProjThinkMenu is the bare-submit effort dropdown (/think, /effect).
	ProjThinkMenu
	// ProjSkillsPanel is the bare-submit skills toggle panel (/skills).
	ProjSkillsPanel
	// ProjMCPPanel is the bare-submit MCP server/tool panel (/mcp).
	ProjMCPPanel
	// ProjSessionsPicker opens the session picker (/sessions, /resume).
	ProjSessionsPicker
	// ProjContextPanel toggles the context-usage overlay (/context).
	ProjContextPanel
	// ProjRename renames the session title (/rename).
	ProjRename
	// ProjRebuild reconstructs the context off the UI loop (/rebuild).
	ProjRebuild
	// ProjCompact compacts the conversation off the UI loop (/compact).
	ProjCompact
	// ProjRemoteControl starts/stops the LAN mirror (/remote-control).
	ProjRemoteControl
	// ProjRewind lists/restores conversation rewind points (/rewind).
	ProjRewind
	// The remaining faces live in the REPL loop only (the TUI and headless
	// reject them explicitly until a panel lands). They are declared per
	// command, not as one "REPL face", so the REPL dispatches each from the
	// declaration with one projection site per face — no per-name chain.
	//
	// ProjFork branches a new session from a historical message (/fork).
	ProjFork
	// ProjClone duplicates the current session at its leaf (/clone).
	ProjClone
	// ProjTree prints the branch tree or switches the active branch (/tree).
	ProjTree
	// ProjExport writes the session transcript to a file (/export).
	ProjExport
	// ProjImport loads a JSONL export as a new session (/import).
	ProjImport
	// ProjCopy copies the last assistant reply to the clipboard (/copy).
	ProjCopy
	// ProjGoal drives the autonomous goal loop (/goal).
	ProjGoal
	// ProjBtw asks a side question in a hidden peek session (/btw).
	ProjBtw
	// ProjDream runs memory consolidation (/dream).
	ProjDream
)

func (p Projection) String() string {
	switch p {
	case ProjQuit:
		return "quit"
	case ProjModelMenu:
		return "model menu"
	case ProjThinkMenu:
		return "effort menu"
	case ProjSkillsPanel:
		return "skills panel"
	case ProjMCPPanel:
		return "mcp panel"
	case ProjSessionsPicker:
		return "sessions picker"
	case ProjContextPanel:
		return "context panel"
	case ProjRename:
		return "rename"
	case ProjRebuild:
		return "rebuild"
	case ProjCompact:
		return "compact"
	case ProjRemoteControl:
		return "remote control"
	case ProjRewind:
		return "rewind"
	case ProjFork:
		return "fork"
	case ProjClone:
		return "clone"
	case ProjTree:
		return "tree"
	case ProjExport:
		return "export"
	case ProjImport:
		return "import"
	case ProjCopy:
		return "copy"
	case ProjGoal:
		return "goal"
	case ProjBtw:
		return "btw"
	case ProjDream:
		return "dream"
	default:
		return "none"
	}
}

// REPLOnly reports whether the projection's loop face lives in the REPL loop
// (pigo --no-tui). A front-end that cannot run that loop rejects the command
// explicitly; the REPL dispatches each of these faces from the declaration.
func (p Projection) REPLOnly() bool {
	switch p {
	case ProjFork, ProjClone, ProjTree, ProjExport, ProjImport, ProjCopy, ProjGoal, ProjBtw, ProjDream:
		return true
	default:
		return false
	}
}

// faceNotice is the phrase a front-end's unavailability notice uses for a
// declared command it cannot project (T7.7 §6: execution or explicit
// rejection, never a silent no-op).
func faceNotice(p Projection) string {
	switch {
	case p == ProjQuit:
		return "loop command"
	case p.REPLOnly():
		return "REPL-face command — run pigo --no-tui"
	case p == ProjNone:
		return "declared without an executable face"
	default:
		return "TUI-face command"
	}
}

// UnavailableNotice renders the explicit rejection a front-end shows when it
// cannot project the command's declared face (T7.7 §6). ResolveOutcome and the
// front-end projectors share it so the wording has one source.
func (p Projection) UnavailableNotice(name string) string {
	return fmt.Sprintf("(%s unavailable: %s)", name, faceNotice(p))
}

// SlashCommand is a resolved command: its name (without the leading "/"), a
// short description for the command palette, and its source. A command is one
// of three kinds, distinguished by which callback is set:
//
//   - A prompt command sets Expand: it turns the invocation arguments into the
//     prompt text fed to the agent (the original slash-command behavior).
//   - An action command sets Action instead: it performs a side effect (e.g.
//     switching the runtime model) and returns a status line to show the user,
//     rather than producing a prompt. No agent run is started.
//   - A hybrid command sets Run: it performs a side effect AND may return prompt
//     text to run — used by plugin commands, which RPC their plugin, surface the
//     returned notifications, then inject the returned prompt as the next turn.
//
// Exactly one of Expand/Action/Run should be set. Precedence when more than one
// is set: Action wins over Run, which wins over Expand. This split is what lets
// a control command like "/model" change runtime state — the old design could
// only emit prompt text.
type SlashCommand struct {
	Name        string
	Description string
	// ArgumentHint is an optional frontmatter hint shown before the description
	// in autocomplete (e.g. "<PR-URL>"). Convention: <angle> for required args,
	// [square] for optional. Empty when not set; display-only, not enforced.
	ArgumentHint string
	Source       SlashCommandSource
	// Tier is the priority tier used to resolve same-name conflicts across
	// sources (built-in > project > global > package > settings > CLI). It is
	// set by the AddX method matching the command's source; callers should not
	// set it directly.
	Tier Tier
	// Aliases are alternative invocation names (declared identity, T7.7
	// slice 4). They resolve through the registry lookup for typed
	// invocations and appear as candidate rows (Candidates), while the
	// model-authored resolution ignores them (ResolveModelAuthored —
	// fail-closed exact canonical). Aliases share the command namespace: an
	// alias colliding with another command's key follows the same tier rule
	// as a name, and two built-ins claiming one key is a programming error.
	Aliases []string
	// Audience gates who may invoke the command (T7.7 §4.3). Zero value is
	// AudienceHumanOnly: fail-closed, a command stays human-only unless it
	// explicitly opts in.
	Audience Audience
	// Projection declares the loop-owned interactive face the front-ends
	// project on submit (T7.7 §4.4). Zero value ProjNone: a text/argument
	// command that resolves through its contract — the parameter forms stay
	// on Parse as the same command's non-interactive projection (user
	// ratified); a declared face the calling front-end cannot project degrades
	// to the explicit unavailability notice ResolveOutcome returns.
	Projection Projection
	// Offered reports whether the command applies in the given loop state
	// (T7.7 §4.2 applicability predicate). Nil means always offered; the
	// menu, /help and dispatch share this one judgment (grok: "advertising
	// and resolution consult the same gate"). Unadorned in slice 1 — the
	// commands that need it declare it as the intercept lists fold in.
	Offered func(SessionState) bool
	// Parse turns the raw argument string into a typed Intent (T7.7 §4.2):
	// pure — no live state, no side effects — so resolution never executes.
	// Usage errors return an error carrying the same message text the old
	// Action path printed. When set, ResolveOutcome returns the intent
	// (Kind SlashIntent) for the front-end Executor to run; exactly one of
	// Parse/Expand/Action/Run should be set, with Parse winning the dispatch.
	Parse func(args string) (Intent, error)
	// Expand maps the argument string (everything after "/name ") to the prompt
	// text the command produces. For a built-in it may be arbitrary Go; for a
	// user template it substitutes $ARGUMENTS into the markdown body. Nil for an
	// action command.
	Expand func(args string) string
	// Action performs a side effect for the invocation and returns a status
	// message to display (may be empty). Set instead of Expand for a control
	// command like "/model". Because it is an arbitrary Go closure it can capture
	// and mutate live runtime state, which Expand (a pure prompt producer)
	// cannot. Nil for a prompt command.
	Action func(args string) string
	// Run is the hybrid of Action and Expand: it performs a side effect AND may
	// produce prompt text to run as the next agent turn. It returns
	// (message, prompt): message is shown to the user immediately (like an
	// Action's status, e.g. plugin notifications), and prompt, when non-empty, is
	// run as a normal turn (like Expand's output). This is what a plugin command
	// needs — it RPCs its plugin (side effect), surfaces the returned
	// notifications (message), then injects the returned prompt (prompt). Set
	// instead of Expand/Action for such a command; nil otherwise. When Run is set
	// it takes precedence over Expand (but Action still wins over Run).
	Run func(args string) (message, prompt string)
}

// SlashKind classifies how a resolved invocation should be handled by the
// caller: run its prompt through the agent, or treat it as a completed action.
type SlashKind int

const (
	// SlashPrompt means the outcome carries prompt text to run (or, when not a
	// command at all, the verbatim input).
	SlashPrompt SlashKind = iota
	// SlashAction means an action command already ran; the outcome carries only
	// a status Message and no agent run should start.
	SlashAction
	// SlashIntent means the resolver parsed the invocation into a typed Intent
	// (T7.7): the registry no longer runs side effects for these commands —
	// the caller executes the Intent through its front-end Executor, which
	// owns the loop state. The executor answers with the same SlashOutcome
	// shape (Kind SlashAction), so front-end projection is unchanged.
	SlashIntent
)

// SlashOutcome is the structured result of resolving one input line. Handled is
// false when the input was not a slash command (Prompt holds the verbatim input
// to run). When Handled is true, Kind says whether Prompt should be run
// (SlashPrompt) or an action already ran and Message should be shown without
// starting a run (SlashAction).
//
// A hybrid (Run) command resolves to Kind SlashPrompt with BOTH fields set: its
// side effect already ran, Message carries the text to show the user first
// (e.g. plugin notifications), and Prompt, when non-empty, is the turn to run
// after. The caller shows Message (if any) then runs Prompt (if non-empty).
type SlashOutcome struct {
	Handled bool
	Kind    SlashKind
	Prompt  string
	Message string
	// Intent carries the typed invocation when Kind is SlashIntent: pure data
	// (slashintent.go) for the front-end Executor to run against loop state.
	Intent Intent
}

// builtinCommands holds compile-time registered commands, keyed by name. It is
// populated by RegisterBuiltin from init() and read when building a registry.
//
// Concurrency contract: this global is written only by RegisterBuiltin, which
// must be called from init() (single-threaded, before main), and read only
// afterwards by NewSlashRegistry. It carries no lock because that init-only
// discipline means there is never a concurrent write; do not call
// RegisterBuiltin after startup.
var builtinCommands = map[string]SlashCommand{}

// RegisterBuiltin registers a built-in slash command at compile time. It is
// intended to be called from init(); a duplicate name panics, since two
// built-ins claiming the same name is a programming error in the fork.
func RegisterBuiltin(cmd SlashCommand) {
	if cmd.Name == "" {
		panic("agent: RegisterBuiltin with empty name")
	}
	if _, exists := builtinCommands[cmd.Name]; exists {
		panic(fmt.Sprintf("agent: duplicate built-in slash command %q", cmd.Name))
	}
	cmd.Source = SourceBuiltin
	cmd.Tier = TierBuiltin
	builtinCommands[cmd.Name] = cmd
}

// SlashRegistry resolves "/name" invocations against built-in and user
// commands, applying the built-in-wins priority rule.
type SlashRegistry struct {
	commands map[string]SlashCommand
	// aliases maps declared alias names to the canonical command name they
	// invoke (T7.7 slice 4). Aliases share the command namespace: a key is
	// either exactly one command's canonical name or exactly one command's
	// alias, and the same tier rule resolves collisions between the two —
	// a lower-tier command arriving later loses its claim to a built-in
	// alias exactly as it would to a built-in name (recorded in shadowed),
	// and two built-ins claiming one key is a programming error (panic, the
	// grok registry's rebuild_triggers rule).
	aliases map[string]string
	// shadowed records commands that lost a same-name conflict to a higher-tier
	// command, with their tier and source for diagnostics. Same-tier overrides
	// (last-write-wins) are not recorded.
	shadowed []ShadowedEntry
}

// NewSlashRegistry builds a registry seeded with all registered built-ins.
func NewSlashRegistry() *SlashRegistry {
	r := &SlashRegistry{commands: make(map[string]SlashCommand, len(builtinCommands)), aliases: map[string]string{}}
	for _, cmd := range builtinCommands {
		r.add(cmd)
	}
	return r
}

// AddBuiltin installs a built-in command directly on this registry instance,
// bypassing the compile-time global. It exists for action commands whose
// closure must capture live, per-run state (e.g. a model controller created in
// main) — such state cannot be reached from an init()-time RegisterBuiltin. The
// command is marked SourceBuiltin so it wins over a same-named user command,
// exactly like a globally registered built-in. A duplicate name panics, since
// two built-ins claiming one name is a programming error.
func (r *SlashRegistry) AddBuiltin(cmd SlashCommand) {
	if cmd.Name == "" {
		panic("agent: AddBuiltin with empty name")
	}
	if existing, ok := r.commands[cmd.Name]; ok && existing.Source == SourceBuiltin {
		panic(fmt.Sprintf("agent: duplicate built-in slash command %q", cmd.Name))
	}
	cmd.Source = SourceBuiltin
	cmd.Tier = TierBuiltin
	r.add(cmd)
}

// AddUser installs a user command (TierGlobal), e.g. a prompt template from
// ~/.pigo/prompts or the legacy ~/.pigo/commands. A same-named built-in or
// project-tier command wins; same-tier (global) adds override silently.
func (r *SlashRegistry) AddUser(cmd SlashCommand) {
	cmd.Source = SourceUser
	cmd.Tier = TierGlobal
	r.add(cmd)
}

// AddSkill installs a skill command (loaded from ~/.agents/skills) at TierGlobal.
// It follows the same tier rule as AddUser - a built-in or project-tier command
// wins - only the source tag differs, so /status can report skills separately.
func (r *SlashRegistry) AddSkill(cmd SlashCommand) {
	cmd.Source = SourceSkill
	cmd.Tier = TierGlobal
	r.add(cmd)
}

// AddPlugin installs a plugin-declared command at TierGlobal, mirroring AddUser
// with a SourcePlugin tag for display.
func (r *SlashRegistry) AddPlugin(cmd SlashCommand) {
	cmd.Source = SourcePlugin
	cmd.Tier = TierGlobal
	r.add(cmd)
}

// Shadowed returns the commands that lost a same-name conflict to a higher-tier
// command, with their tier and source for diagnostics. Same-tier overrides
// (last-write-wins) are not recorded here.
func (r *SlashRegistry) Shadowed() []ShadowedEntry { return r.shadowed }

// AddProject installs a project-local prompt template (TierProject), which
// overrides a same-named global/package/settings/CLI template but loses to a
// built-in.
func (r *SlashRegistry) AddProject(cmd SlashCommand) {
	cmd.Source = SourceUser
	cmd.Tier = TierProject
	r.add(cmd)
}

// AddPackage installs a package-discovered prompt template (TierPackage).
func (r *SlashRegistry) AddPackage(cmd SlashCommand) {
	cmd.Source = SourceUser
	cmd.Tier = TierPackage
	r.add(cmd)
}

// AddSettings installs a prompt template referenced by config.toml (TierSettings).
func (r *SlashRegistry) AddSettings(cmd SlashCommand) {
	cmd.Source = SourceUser
	cmd.Tier = TierSettings
	r.add(cmd)
}

// AddCLI installs a prompt template referenced by --prompt-template (TierCLI,
// the lowest priority).
func (r *SlashRegistry) AddCLI(cmd SlashCommand) {
	cmd.Source = SourceUser
	cmd.Tier = TierCLI
	r.add(cmd)
}

// add installs cmd: its canonical name first, then each declared alias, both
// through the one namespace-wide tier rule (bind). A built-in always wins
// because TierBuiltin is highest.
func (r *SlashRegistry) add(cmd SlashCommand) {
	r.bind(cmd.Name, cmd, true)
	for _, alias := range cmd.Aliases {
		if alias == "" || alias == cmd.Name {
			continue
		}
		r.bind(alias, cmd, false)
	}
}

// bind resolves one key claim (canonical when named, else a declared alias)
// under the tier rule that governs the whole command namespace. When the key
// is already owned, the higher tier keeps it and the loser is recorded in
// shadowed (a losing canonical claim shadows the whole command; a losing
// alias claim leaves the command reachable by its canonical name, so it is
// not recorded); same tier is last-write-wins (a re-load), and two built-ins
// claiming one key is a programming error (panic).
func (r *SlashRegistry) bind(key string, cmd SlashCommand, canonical bool) {
	if prev, ok := r.owner(key); ok {
		if cmd.Source == SourceBuiltin && prev.Source == SourceBuiltin {
			panic(fmt.Sprintf("agent: duplicate built-in slash command key %q", key))
		}
		switch {
		case prev.Tier > cmd.Tier:
			if canonical {
				r.shadowed = append(r.shadowed, ShadowedEntry{Name: cmd.Name, Tier: cmd.Tier, Source: cmd.Source})
			}
			return
		case prev.Tier < cmd.Tier:
			r.shadowed = append(r.shadowed, ShadowedEntry{Name: prev.Name, Tier: prev.Tier, Source: prev.Source})
		}
		// Lower-tier owner or same-tier reload: the key moves.
		r.unbind(key)
	}
	if canonical {
		r.commands[key] = cmd
		return
	}
	r.aliases[key] = cmd.Name
}

// owner returns the command a key currently resolves to, whether it is bound
// as a canonical name or as a declared alias. A stale alias (its canonical
// command was removed) is dropped and reported as unowned.
func (r *SlashRegistry) owner(key string) (SlashCommand, bool) {
	if cmd, ok := r.commands[key]; ok {
		return cmd, true
	}
	canonical, ok := r.aliases[key]
	if !ok {
		return SlashCommand{}, false
	}
	cmd, ok := r.commands[canonical]
	if !ok {
		delete(r.aliases, key)
		return SlashCommand{}, false
	}
	return cmd, true
}

// unbind releases a key from both namespace shapes; the caller rebinds it.
func (r *SlashRegistry) unbind(key string) {
	delete(r.commands, key)
	delete(r.aliases, key)
}

// Lookup returns the command bound to name (without the leading "/"). A
// declared alias resolves to the command that declared it (the human path:
// grok's dispatch resolves canonical names and aliases through one key map —
// the model-authored path does not; see ResolveModelAuthored).
func (r *SlashRegistry) Lookup(name string) (SlashCommand, bool) {
	return r.owner(name)
}

// Remove deletes a command by name (without the leading "/"), regardless of
// tier, together with every alias resolving to it. It backs the config-surface
// commands (T6.9): disabling a skill takes its /name command off the list
// immediately, and /skills reload re-registers the survivors. Returns false
// when the name was not registered. Remove is deliberately not tier-aware —
// the surface controllers know exactly which commands they own (they track the
// names they registered), so a blanket delete is the honest primitive.
func (r *SlashRegistry) Remove(name string) bool {
	if _, ok := r.commands[name]; !ok {
		return false
	}
	delete(r.commands, name)
	for alias, canonical := range r.aliases {
		if canonical == name {
			delete(r.aliases, alias)
		}
	}
	return true
}

// List returns all commands sorted by name.
func (r *SlashRegistry) List() []SlashCommand {
	out := make([]SlashCommand, 0, len(r.commands))
	for _, c := range r.commands {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Candidates returns the candidate-surface rows: every canonical command plus
// one row per declared alias, in name order. It is the source the completion
// menus, /help and every other advertised surface render (grok: the
// completion triggers are the canonical names and the aliases, so a typed
// alias stays discoverable). An alias row is the owning command with the
// alias as its Name and an "alias of /canonical:" description prefix, so the
// surfaces need no per-row alias logic. The canonical catalog stays List —
// /status counts and identity checks read it without alias duplicates.
func (r *SlashRegistry) Candidates() []SlashCommand {
	out := r.List()
	keys := make([]string, 0, len(r.aliases))
	for alias := range r.aliases {
		keys = append(keys, alias)
	}
	sort.Strings(keys)
	for _, alias := range keys {
		cmd, ok := r.owner(alias)
		if !ok {
			continue
		}
		row := cmd
		row.Name = alias
		row.Description = "alias of /" + cmd.Name
		if cmd.Description != "" {
			row.Description += ": " + cmd.Description
		}
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Resolve parses a raw input line and, if it is a slash-command invocation,
// expands it to the prompt text the agent should run. It returns (prompt, true)
// when input begins with "/" and names a known PROMPT command; (input, false)
// when the input is not a slash command (the caller runs it verbatim); and an
// error when input is a "/name" for an unknown command.
//
// This is the legacy string API, kept for callers that only handle prompt
// commands. It reports an action command as handled with an empty prompt (the
// action does NOT run here) — callers that want action commands to execute must
// use ResolveOutcome instead.
func (r *SlashRegistry) Resolve(input string) (prompt string, handled bool, err error) {
	out, err := r.ResolveOutcome(input)
	if err != nil {
		return "", false, err
	}
	return out.Prompt, out.Handled, nil
}

// ResolveOutcome parses a raw input line into a structured SlashOutcome. For a
// non-command it returns {Handled:false, Prompt:input}. For a known prompt
// command it returns {Handled:true, Kind:SlashPrompt, Prompt:<expanded>}. For a
// known action command it RUNS the action and returns {Handled:true,
// Kind:SlashAction, Message:<status>} — no prompt to run. For a known hybrid
// (Run) command it RUNS the side effect and returns {Handled:true,
// Kind:SlashPrompt, Message:<status>, Prompt:<text>} — the caller shows Message
// then runs Prompt when non-empty. A Parse command returns {Handled:true,
// Kind:SlashIntent, Intent:<typed>} for the caller's Executor. A declared
// command with no executable face (its Projection face lives in another
// front-end) returns {Handled:true, Kind:SlashAction, Message:<unavailability
// notice>} — never a silent no-op. An unknown "/name" yields an error.
//
// The name is resolved through the one splitter (SplitInvocation) and the one
// lookup (aliases included), so the registry, the REPL, the TUI and the
// headless guard all answer "is this /name?" identically; the unavailability
// notice names the invoked form (a typed alias stays "/resume", not the
// canonical "/sessions").
func (r *SlashRegistry) ResolveOutcome(input string) (SlashOutcome, error) {
	name, args, ok := SplitInvocation(input)
	if !ok {
		return SlashOutcome{Handled: false, Kind: SlashPrompt, Prompt: input}, nil
	}
	cmd, found := r.Lookup(name)
	if !found {
		return SlashOutcome{}, fmt.Errorf("unknown command %q", "/"+name)
	}
	return r.resolveCommand(cmd, name, args)
}

// resolveCommand folds one resolved command + arguments into its outcome. It
// is the body both resolution entries share (the human path above and the
// model-authored gate below) so the Parse/Action/Run/Expand precedence and
// the unavailability notice have one source.
func (r *SlashRegistry) resolveCommand(cmd SlashCommand, name, args string) (SlashOutcome, error) {
	if cmd.Parse != nil {
		// T7.7 contract command: parse into a typed intent and hand it back —
		// resolution never executes. Usage errors surface the same message
		// text the old Action path printed.
		it, err := cmd.Parse(args)
		if err != nil {
			return SlashOutcome{}, err
		}
		return SlashOutcome{Handled: true, Kind: SlashIntent, Intent: it}, nil
	}
	if cmd.Action != nil {
		return SlashOutcome{Handled: true, Kind: SlashAction, Message: cmd.Action(args)}, nil
	}
	if cmd.Run != nil {
		// A hybrid command runs its side effect now and may yield prompt text.
		// The outcome is a prompt (SlashPrompt) that also carries a Message to
		// surface first; the caller shows Message then runs Prompt if non-empty.
		message, prompt := cmd.Run(args)
		return SlashOutcome{Handled: true, Kind: SlashPrompt, Message: message, Prompt: prompt}, nil
	}
	if cmd.Expand == nil {
		// A declaration with no executable face (T7.7 §6): identity-only
		// entries whose loop face another front-end owns resolve to an
		// explicit unavailability outcome, never a silent no-op. The notice
		// names the invoked form, so a typed alias reads back as typed.
		return SlashOutcome{Handled: true, Kind: SlashAction,
			Message: cmd.Projection.UnavailableNotice(name),
		}, nil
	}
	return SlashOutcome{Handled: true, Kind: SlashPrompt, Prompt: cmd.Expand(args)}, nil
}

// ResolveModelAuthored resolves input as model-authored text (T7.7 §4.3, the
// grok slash_authority alignment): fail-closed — only a command that opted in
// via AudienceHumanAndModel, named by its exact canonical name, resolves.
// Aliases are ignored (an alias form stays plain text even for an opted-in
// command, grok ExactCanonical), an unknown "/name" is plain text rather than
// an error, and a human-only command is refused: the model may not compact the
// context, switch models or toggle surfaces by writing their names. The
// refused, unknown and non-invocation cases all return {Handled:false,
// Prompt:input}, so the caller treats the text verbatim (grok: demote to the
// skill-candidate path); a usage error in model-authored text degrades the
// same way instead of surfacing as a front-end error.
//
// No production surface feeds model output through the registry yet (model
// output streams to the transcript and is never re-resolved as input); this
// is the entry any future such path must use, pinned fail-closed by tests.
// The Offered predicate is deliberately not consulted here: it gates
// session-state applicability on the human surfaces, while this gate is about
// who may invoke at all.
func (r *SlashRegistry) ResolveModelAuthored(input string) SlashOutcome {
	name, args, ok := SplitInvocation(input)
	if !ok {
		return SlashOutcome{Handled: false, Kind: SlashPrompt, Prompt: input}
	}
	// Exact canonical names only: the alias index is deliberately bypassed.
	cmd, found := r.commands[name]
	if !found || cmd.Audience != AudienceHumanAndModel {
		return SlashOutcome{Handled: false, Kind: SlashPrompt, Prompt: input}
	}
	out, err := r.resolveCommand(cmd, name, args)
	if err != nil {
		return SlashOutcome{Handled: false, Kind: SlashPrompt, Prompt: input}
	}
	return out
}

// firstNonEmptyLine returns the first line of s whose trimmed form is non-empty,
// itself trimmed. It is the description fallback for templates whose frontmatter
// omits a description (mirrors pi: "If missing, the first non-empty line is used").
func firstNonEmptyLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			return t
		}
	}
	return ""
}

// LoadPromptFile loads a single prompt-template file. The command name is the
// filename without its extension (e.g. /x/review.md -> "review"). It is the
// single-file counterpart of LoadUserCommandsDir, used for settings/CLI paths
// that point at one file rather than a directory.
func LoadPromptFile(path string) (SlashCommand, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return SlashCommand{}, fmt.Errorf("read prompt %s: %w", path, err)
	}
	base := filepath.Base(path)
	name := strings.TrimSuffix(base, filepath.Ext(base))
	return ParseUserCommand(name, content)
}

// LoadUserCommandsDir loads declarative markdown command templates from dir
// (non-recursively). Each "*.md" file defines a command named after the file
// (without extension). The file may carry an optional YAML frontmatter block
// with a "description" (mirrors skills); the remaining body is the prompt template,
// expanded via ExpandTemplate at invoke time. A missing directory yields no
// commands and no error.
func LoadUserCommandsDir(dir string) ([]SlashCommand, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read commands dir %s: %w", dir, err)
	}
	var cmds []SlashCommand
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".md") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil, fmt.Errorf("read command %s: %w", path, readErr)
		}
		name := strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))
		cmd, parseErr := ParseUserCommand(name, content)
		if parseErr != nil {
			return nil, parseErr
		}
		cmds = append(cmds, cmd)
	}
	sort.Slice(cmds, func(i, j int) bool { return cmds[i].Name < cmds[j].Name })
	return cmds, nil
}

// ParseUserCommand parses a declarative command template. An optional YAML
// frontmatter block supplies a description; the body is the prompt template,
// expanded at invoke time via ExpandTemplate (positional $N, $@/$ARGUMENTS,
// ${1:-default}, ${@:N}). If arg tokenization fails (e.g. an unterminated
// quote) the raw arg string is used as $ARGUMENTS so the invocation still works.
func ParseUserCommand(name string, content []byte) (SlashCommand, error) {
	body := string(content)
	description := ""
	hint := ""
	// Reuse the skills frontmatter splitter when a fence is present; otherwise
	// treat the whole file as the template body.
	if strings.HasPrefix(strings.TrimLeft(strings.TrimPrefix(body, "\ufeff"), "\r\n"), "---") {
		fm, rest, splitErr := splitFrontmatter(content)
		if splitErr != nil {
			return SlashCommand{}, fmt.Errorf("command %s: %w", name, splitErr)
		}
		var meta struct {
			Description  string `yaml:"description"`
			Name         string `yaml:"name"`
			ArgumentHint string `yaml:"argument-hint"`
		}
		if err := yaml.Unmarshal(fm, &meta); err != nil {
			return SlashCommand{}, fmt.Errorf("command %s: parse frontmatter: %w", name, err)
		}
		description = meta.Description
		hint = meta.ArgumentHint
		if meta.Name != "" {
			name = meta.Name
		}
		body = string(rest)
	}
	// When the frontmatter omits a description, fall back to the first non-empty
	// line of the body (\u5bf9\u6807 pi: "If missing, the first non-empty line is used").
	if description == "" {
		description = firstNonEmptyLine(body)
	}
	template := strings.TrimSpace(body)
	return SlashCommand{
		Name:         name,
		Description:  description,
		ArgumentHint: hint,
		Source:       SourceUser,
		Expand: func(args string) string {
			tokens, err := SplitArgs(args)
			if err != nil {
				// Split failure (e.g. an unterminated quote): treat the raw arg
				// string as a single $ARGUMENTS rather than feeding a malformed
				// arg list to the engine, so a bad invocation stays usable.
				tokens = []string{args}
			}
			return ExpandTemplate(template, tokens)
		},
	}, nil
}
