// Package tooldecl implements the deferred tool declaration machinery (T4.1):
// a three-tier exposure model over the declared tool face plus the claim /
// announce / gate triangle the loop and the executor consume.
//
// The three exposure tiers (spec deferred-tool-exposure.md §2.1):
//
//	direct    — declared to the model (today's behavior, the default)
//	deferred  — NOT declared; the model claims tools by name via search_tools
//	            and they enter the declared face on its next turn
//	hidden    — not declared and not discoverable (connectivity preserved)
//
// Only-growth invariant (spec §2.2): the declared face has no removal path —
// claiming moves a tool from the deferred set into the declared face and it
// stays there for the rest of the session. The claim record persists in the
// session itself (agentcore.ToolClaimMessage tree entries), so resume restores
// both the claims and the declared face in one batch (spec §2.4, pi 1.0.0's
// lost-claims bug is structurally impossible here).
//
// Everything in this package is a pure function of the plan plus the claim
// set: announcements derive byte-stable from the unclaimed set (a turn with
// no claims produces a byte-identical announcement — the cache-friendly
// assertion, spec验收 6), and the fingerprint is a plain FNV-1a hash.
package tooldecl

import (
	"fmt"
	"hash/fnv"
	"sort"
	"strings"

	"github.com/smallnest/pigo/internal/agentcore"
)

// Exposure tier values (config-facing strings).
const (
	Direct   = "direct"
	Deferred = "deferred"
	Hidden   = "hidden"
)

// ToolInfo is one tool's declaration-relevant identity: its registry name, a
// one-line description (announcement/search material), and the surface it came
// from ("plugin", "mcp:<server>", "config" for name-list entries).
type ToolInfo struct {
	Name        string
	Description string
	Source      string
}

// Plan is the static declaration plan assembled once per run (before the loop
// starts): which tool names start deferred and which stay hidden. The declared
// (direct) set is implicit — everything else. A Plan with no deferred tools is
// inert and the loop skips the whole machinery.
type Plan struct {
	// Deferred carries the tools that start in the deferred tier, keyed by
	// registry name.
	Deferred map[string]ToolInfo
	// Hidden carries the tools that are neither declared nor discoverable.
	Hidden map[string]ToolInfo
}

// HasDeferred reports whether the plan defers anything. A false return makes
// the loop-side machinery a no-op (direct declaration, zero overhead).
func (p Plan) HasDeferred() bool { return len(p.Deferred) > 0 }

// Len returns the total number of non-direct tools in the plan.
func (p Plan) Len() int { return len(p.Deferred) + len(p.Hidden) }

// Intersect returns the sub-plan restricted to the given tool-name set. It
// backs the task sub-agent wiring: a child's registry is a subset of the
// parent's, so its declaration plan must be too (capability 只减不增, spec §3.4).
// nil is passed through (no parent plan → no child plan).
func (p Plan) Intersect(names map[string]bool) *Plan {
	if p.Deferred == nil && p.Hidden == nil {
		return nil
	}
	out := Plan{Deferred: map[string]ToolInfo{}, Hidden: map[string]ToolInfo{}}
	for tier, set := range map[bool]map[string]ToolInfo{false: p.Deferred, true: p.Hidden} {
		for name, info := range set {
			if names[name] {
				if tier {
					out.Hidden[name] = info
				} else {
					out.Deferred[name] = info
				}
			}
		}
	}
	return &out
}

// BuildPlan assembles the run's declaration plan from config + the assembled
// tool face. all is the post-policy full tool set (registration is never
// pruned); external carries the tools from an external surface (today the
// plugin tools; the future MCP face slots in the same way), which under
// declaration_mode="deferred" become the default deferred tier.
//
// Rules:
//   - mode "deferred": every external tool defers, plus any all-tool named in
//     deferredNames; directNames wins per tool (explicit exemption);
//   - mode anything else (including ""): only deferredNames defer — so a
//     direct-mode run can still opt individual tools in;
//   - hiddenNames always hides, regardless of mode;
//   - names are matched case-insensitively against the actual tool face;
//     unknown names are ignored (admission stays ValidateToolPolicy's job).
//
// The returned plan is nil when nothing defers, which is the loop's inert
// signal.
func BuildPlan(mode string, deferredNames, directNames, hiddenNames []string, all, external []agentcore.AgentTool) *Plan {
	return BuildPlanWithSources(mode, deferredNames, directNames, hiddenNames, all, external, nil)
}

// BuildPlanWithSources is BuildPlan plus a per-tool surface label, so the
// announcement and diagnostics can say where a tool came from instead of
// calling every external tool "plugin" (T6.8: with MCP in the face there are
// two external surfaces, plus per-server granularity inside the MCP one).
//
// sourceOf maps a registry name to its surface label ("plugin", "mcp:<server>");
// a nil sourceOf — or one returning "" for a name — falls back to the
// pre-existing behaviour, so every caller that does not care is unaffected.
func BuildPlanWithSources(mode string, deferredNames, directNames, hiddenNames []string, all, external []agentcore.AgentTool, sourceOf func(string) string) *Plan {
	norm := func(s string) string { return strings.ToLower(strings.TrimSpace(s)) }
	byLower := make(map[string]string, len(all)) // lower → registry name
	for _, t := range all {
		byLower[norm(t.Name())] = t.Name()
	}
	desc := make(map[string]string, len(all))
	for _, t := range all {
		desc[t.Name()] = oneLine(t.Description())
	}
	externalNames := make(map[string]bool, len(external))
	for _, t := range external {
		externalNames[t.Name()] = true
	}
	// label resolves a tool's surface, preferring the caller's map.
	label := func(name, fallback string) string {
		if sourceOf != nil {
			if s := strings.TrimSpace(sourceOf(name)); s != "" {
				return s
			}
		}
		return fallback
	}

	deferMode := strings.TrimSpace(strings.ToLower(mode)) == Deferred
	deferred := map[string]ToolInfo{}
	if deferMode {
		for _, t := range external {
			deferred[t.Name()] = ToolInfo{Name: t.Name(), Description: oneLine(t.Description()), Source: label(t.Name(), "plugin")}
		}
	}
	for _, n := range deferredNames {
		if name, ok := byLower[norm(n)]; ok {
			source := "config"
			if externalNames[name] {
				source = label(name, "plugin")
			}
			deferred[name] = ToolInfo{Name: name, Description: desc[name], Source: source}
		}
	}
	hidden := map[string]ToolInfo{}
	for _, n := range hiddenNames {
		if name, ok := byLower[norm(n)]; ok {
			hidden[name] = ToolInfo{Name: name, Description: desc[name], Source: "config"}
			delete(deferred, name)
		}
	}
	for _, n := range directNames {
		delete(deferred, byLower[norm(n)])
	}
	if len(deferred) == 0 && len(hidden) == 0 {
		return nil
	}
	return &Plan{Deferred: deferred, Hidden: hidden}
}

// Status is a tool's current position in the declaration machinery.
type Status int

const (
	// StatusDeclared: in the declared face (direct, or a claimed deferred).
	StatusDeclared Status = iota
	// StatusDeferredUnclaimed: deferred and not yet claimed — not declared,
	// discoverable via search_tools.
	StatusDeferredUnclaimed
	// StatusHidden: not declared, not discoverable.
	StatusHidden
)

// State is the run's mutable declaration state: the plan plus the claim set.
// The executor gate, the search_tools tool, and the loop's face refresh all
// share one State. It is not safe for concurrent use — the loop is
// single-goroutine and tool calls execute sequentially per call site
// (search_tools declares itself sequential).
type State struct {
	plan    Plan
	claimed map[string]bool
	rev     int64
}

// NewState creates the run state over plan. A plan with only hidden tools is
// allowed: the face is pruned at assembly and the gate reports hidden tools as
// unknown, but no announcement is produced and search is empty (the loop mounts
// the machinery for any non-empty plan).
func NewState(plan Plan) *State {
	return &State{plan: plan, claimed: map[string]bool{}}
}

// Revision increases on every successful claim. The loop compares revisions
// across turns to decide when to rebuild the declared face ("下一轮进声明面").
func (s *State) Revision() int64 { return s.rev }

// Plan returns the immutable plan.
func (s *State) Plan() Plan { return s.plan }

// StatusOf classifies one tool name.
func (s *State) StatusOf(name string) Status {
	if _, hidden := s.plan.Hidden[name]; hidden {
		return StatusHidden
	}
	if _, deferred := s.plan.Deferred[name]; deferred {
		if s.claimed[name] {
			return StatusDeclared
		}
		return StatusDeferredUnclaimed
	}
	return StatusDeclared
}

// Claim marks the named tools as claimed and returns the ones that actually
// changed state (already-claimed and plan-unknown names are ignored, so a
// model guessing names cannot fabricate entries). Empty result = no-op.
func (s *State) Claim(names []string) []string {
	var newly []string
	for _, n := range names {
		if _, deferred := s.plan.Deferred[n]; deferred && !s.claimed[n] {
			s.claimed[n] = true
			newly = append(newly, n)
		}
	}
	if len(newly) > 0 {
		s.rev++
	}
	return newly
}

// Restore replay-loads persisted claims (resume path). It bumps the revision
// once when anything restored, so a driver that restores before the first
// request still gets a consistent face build.
func (s *State) Restore(names []string) {
	restored := false
	for _, n := range names {
		if _, deferred := s.plan.Deferred[n]; deferred && !s.claimed[n] {
			s.claimed[n] = true
			restored = true
		}
	}
	if restored {
		s.rev++
	}
}

// ClaimedTools returns the claimed names sorted (stable for persistence).
func (s *State) ClaimedTools() []string {
	out := make([]string, 0, len(s.claimed))
	for n := range s.claimed {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Unclaimed returns the deferred-but-unclaimed tools sorted by name — the
// announcement's and search's universe.
func (s *State) Unclaimed() []ToolInfo {
	out := make([]ToolInfo, 0, len(s.plan.Deferred))
	for name, info := range s.plan.Deferred {
		if !s.claimed[name] {
			out = append(out, info)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// DeclaredTools filters the full tool set down to the declared face: direct
// tools and claimed deferred tools stay; unclaimed deferred and hidden tools
// drop. Registration is never pruned — this face only shapes what the model
// sees (spec §3.2). The gate covers the dispatch side.
func DeclaredTools(all []agentcore.AgentTool, st *State) []agentcore.AgentTool {
	out := make([]agentcore.AgentTool, 0, len(all))
	for _, t := range all {
		if st.StatusOf(t.Name()) == StatusDeclared {
			out = append(out, t)
		}
	}
	return out
}

// Check implements the executor-side declaration gate: calling a tool that is
// deferred-but-unclaimed gets the kimi guidance text (spec §6.3 — "Tool X is
// available but not loaded. Call search_tools first…"), calling a hidden tool
// looks exactly like an unknown tool. Anything declared passes.
func (s *State) CheckTool(name string) (guidance string, blocked bool) {
	switch s.StatusOf(name) {
	case StatusDeferredUnclaimed:
		return fmt.Sprintf("Tool %q is available but not loaded. Call search_tools with query %q first; "+
			"it becomes callable on your next turn and stays loaded for the rest of the session.", name, name), true
	case StatusHidden:
		return fmt.Sprintf("unknown tool %q", name), true
	default:
		return "", false
	}
}

// Announcement renders the current announcement body: one line per unclaimed
// deferred tool (name + a clipped one-line description), plus the claim
// instructions. Empty when nothing is unclaimed — after the last claim the
// announcement drops out of the request entirely. The output is a pure
// function of the unclaimed set, so consecutive no-claim turns produce
// byte-identical text (spec验收 6: 变更才追加, prompt-cache friendly).
func (s *State) Announcement() string {
	un := s.Unclaimed()
	if len(un) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Additional tools are available but not loaded yet. To use one, call the search_tools tool with its exact name (or a name prefix / keyword); loaded tools become callable on your next turn and stay loaded for the rest of the session:\n")
	for _, t := range un {
		b.WriteString("- ")
		b.WriteString(t.Name)
		if t.Description != "" {
			b.WriteString(": ")
			b.WriteString(clip(oneLine(t.Description), 100))
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// Fingerprint hashes the tool list (name + description, FNV-1a — the grok
// announcement-delta scheme). It backs the "announcement changed" assertion in
// tests; the runtime derives announcements deterministically and needs no
// persisted fingerprint.
func Fingerprint(tools []ToolInfo) uint64 {
	h := fnv.New64a()
	for _, t := range tools {
		fmt.Fprintf(h, "%s\x00%s\x00", t.Name, t.Description)
	}
	return h.Sum64()
}

// Search scores the unclaimed tools against query: exact name (100), name
// prefix (80), name substring (60), description keyword hit (40 — per-token,
// best token wins). Ties break by name so results are deterministic. limit
// caps the result count (<= 0 = all). No match returns an empty slice — never
// an error (spec验收 2).
func Search(unclaimed []ToolInfo, query string, limit int) []ToolInfo {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return nil
	}
	tokens := strings.FieldsFunc(q, func(r rune) bool {
		return r == ' ' || r == ',' || r == '\t' || r == '\n'
	})
	type scored struct {
		info  ToolInfo
		score int
	}
	var hits []scored
	for _, t := range unclaimed {
		name := strings.ToLower(t.Name)
		desc := strings.ToLower(t.Description)
		best := 0
		switch {
		case name == q:
			best = 100
		case strings.HasPrefix(name, q):
			best = 80
		case strings.Contains(name, q):
			best = 60
		}
		for _, tok := range tokens {
			if tok == "" {
				continue
			}
			if strings.Contains(name, tok) && best < 80 {
				best = 80
			}
			if desc != "" && strings.Contains(desc, tok) && best < 40 {
				best = 40
			}
		}
		if best > 0 {
			hits = append(hits, scored{t, best})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		return hits[i].info.Name < hits[j].info.Name
	})
	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}
	out := make([]ToolInfo, 0, len(hits))
	for _, h := range hits {
		out = append(out, h.info)
	}
	return out
}

// oneLine collapses a description into a single whitespace-normalized line.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// clip truncates s to at most n runes, appending an ellipsis when cut.
func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
