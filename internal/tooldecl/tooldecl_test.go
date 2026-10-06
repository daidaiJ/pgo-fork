package tooldecl

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
)

// fakeTool is a minimal AgentTool stub for face-building tests.
type fakeTool struct {
	name string
	desc string
}

func (f fakeTool) Name() string        { return f.name }
func (f fakeTool) Description() string { return f.desc }
func (f fakeTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object"}`)
}
func (f fakeTool) ExecutionMode() agentcore.ToolExecutionMode { return agentcore.ToolExecutionParallel }
func (f fakeTool) Execute(context.Context, string, json.RawMessage, agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
	return agentcore.AgentToolResult{}, nil
}

func toolSet(names ...string) []agentcore.AgentTool {
	out := make([]agentcore.AgentTool, 0, len(names))
	for _, n := range names {
		out = append(out, fakeTool{name: n, desc: "the " + n + " tool"})
	}
	return out
}

func namesOf(tools []agentcore.AgentTool) map[string]bool {
	out := map[string]bool{}
	for _, t := range tools {
		out[t.Name()] = true
	}
	return out
}

func TestBuildPlanDeferredModeDefersExternalFace(t *testing.T) {
	all := toolSet("read", "bash", "plug_a", "plug_b")
	external := toolSet("plug_a", "plug_b")
	plan := BuildPlan(Deferred, nil, nil, nil, all, external)
	if plan == nil || !plan.HasDeferred() {
		t.Fatalf("want deferred plan, got %+v", plan)
	}
	if len(plan.Deferred) != 2 {
		t.Errorf("want 2 deferred (the plugin face), got %d", len(plan.Deferred))
	}
	if _, ok := plan.Deferred["plug_a"]; !ok {
		t.Error("plug_a should be deferred")
	}
	if _, ok := plan.Deferred["read"]; ok {
		t.Error("builtin read must never default-defer")
	}
}

func TestBuildPlanDirectModeExplicitList(t *testing.T) {
	all := toolSet("read", "bash", "webfetch")
	plan := BuildPlan(Direct, []string{"WebFetch"}, nil, nil, all, nil)
	if plan == nil || !plan.HasDeferred() {
		t.Fatalf("explicit list must defer even in direct mode, got %+v", plan)
	}
	if _, ok := plan.Deferred["webfetch"]; !ok {
		t.Error("case-insensitive name match: webfetch should be deferred")
	}
}

func TestBuildPlanDirectExemptionAndHiddenWin(t *testing.T) {
	all := toolSet("plug_a", "plug_b", "plug_c")
	external := toolSet("plug_a", "plug_b", "plug_c")
	plan := BuildPlan(Deferred, []string{"plug_c"}, []string{"plug_a"}, []string{"plug_b"}, all, external)
	if plan == nil {
		t.Fatal("want plan")
	}
	if _, ok := plan.Deferred["plug_a"]; ok {
		t.Error("direct exemption must remove plug_a from the deferred set")
	}
	if _, ok := plan.Deferred["plug_b"]; ok {
		t.Error("hidden must remove plug_b from the deferred set")
	}
	if _, ok := plan.Hidden["plug_b"]; !ok {
		t.Error("plug_b should be hidden")
	}
	if _, ok := plan.Deferred["plug_c"]; !ok {
		t.Error("plug_c should stay deferred")
	}
}

func TestBuildPlanUnknownNamesIgnored(t *testing.T) {
	all := toolSet("read")
	plan := BuildPlan(Direct, []string{"no_such_tool"}, nil, nil, all, nil)
	if plan != nil {
		t.Errorf("a plan of only unknown names must be nil, got %+v", plan)
	}
}

func TestBuildPlanNothingDeferredNil(t *testing.T) {
	all := toolSet("read", "bash")
	if plan := BuildPlan(Direct, nil, nil, nil, all, nil); plan != nil {
		t.Errorf("direct mode with no lists must be nil, got %+v", plan)
	}
}

func TestStateClaimLifecycle(t *testing.T) {
	plan := Plan{Deferred: map[string]ToolInfo{
		"plug_a": {Name: "plug_a"},
		"plug_b": {Name: "plug_b"},
	}}
	st := NewState(plan)
	if got := st.Claim([]string{"plug_a", "plug_b", "unknown"}); len(got) != 2 {
		t.Errorf("claim should return only the two real tools, got %v", got)
	}
	if st.Revision() != 1 {
		t.Errorf("one claim batch = revision 1, got %d", st.Revision())
	}
	// Idempotent: re-claiming changes nothing.
	if got := st.Claim([]string{"plug_a"}); len(got) != 0 {
		t.Errorf("re-claim must be a no-op, got %v", got)
	}
	if len(st.Unclaimed()) != 0 {
		t.Errorf("unclaimed should be empty, got %v", st.Unclaimed())
	}
	if st.StatusOf("plug_a") != StatusDeclared {
		t.Error("claimed tool is declared")
	}
	if st.StatusOf("unknown") != StatusDeclared {
		t.Error("plan-unknown tool is declared (not gated)")
	}
}

func TestStateRestore(t *testing.T) {
	plan := Plan{Deferred: map[string]ToolInfo{"plug_a": {Name: "plug_a"}}}
	st := NewState(plan)
	st.Restore([]string{"plug_a"})
	if st.StatusOf("plug_a") != StatusDeclared {
		t.Error("restore should mark the tool claimed")
	}
	st.Restore([]string{"plug_a"})
	if st.Revision() != 1 {
		t.Errorf("a second restore of the same names is a no-op, rev = %d", st.Revision())
	}
}

func TestDeclaredToolsFilter(t *testing.T) {
	plan := Plan{
		Deferred: map[string]ToolInfo{"plug_a": {Name: "plug_a"}},
		Hidden:   map[string]ToolInfo{"plug_b": {Name: "plug_b"}},
	}
	st := NewState(plan)
	all := toolSet("read", "plug_a", "plug_b")
	face := namesOf(DeclaredTools(all, st))
	if face["read"] != true || len(face) != 1 {
		t.Errorf("fresh state: only direct tools declared, got %v", face)
	}
	st.Claim([]string{"plug_a"})
	face = namesOf(DeclaredTools(all, st))
	if len(face) != 2 || !face["plug_a"] {
		t.Errorf("claim enters the face, got %v", face)
	}
}

func TestCheckToolGate(t *testing.T) {
	plan := Plan{
		Deferred: map[string]ToolInfo{"plug_a": {Name: "plug_a"}},
		Hidden:   map[string]ToolInfo{"plug_b": {Name: "plug_b"}},
	}
	st := NewState(plan)
	if g, blocked := st.CheckTool("plug_a"); !blocked || g == "" {
		t.Errorf("unclaimed deferred must be blocked with guidance, got (%q, %v)", g, blocked)
	}
	if g, blocked := st.CheckTool("plug_b"); !blocked || g != "unknown tool \"plug_b\"" {
		t.Errorf("hidden must look unknown, got (%q, %v)", g, blocked)
	}
	if _, blocked := st.CheckTool("read"); blocked {
		t.Error("direct tool must pass the gate")
	}
	st.Claim([]string{"plug_a"})
	if _, blocked := st.CheckTool("plug_a"); blocked {
		t.Error("claimed tool must pass the gate")
	}
}

func TestAnnouncementStableAndComplete(t *testing.T) {
	plan := Plan{Deferred: map[string]ToolInfo{
		"plug_a": {Name: "plug_a", Description: "does a\n b  c"},
		"plug_b": {Name: "plug_b"},
	}}
	st := NewState(plan)
	first := st.Announcement()
	if first == "" {
		t.Fatal("want announcement")
	}
	if want := "- plug_a: does a b c"; !contains(first, want) {
		t.Errorf("description must be one-line clipped, want %q in:\n%s", want, first)
	}
	if !contains(first, "- plug_b\n") && !strings.HasSuffix(first, "- plug_b") {
		t.Errorf("plug_b without description should have no colon, announcement:\n%s", first)
	}
	// Byte-stability across turns with no claims (验收 6).
	for i := 0; i < 3; i++ {
		if st.Announcement() != first {
			t.Fatal("no-claim turns must produce byte-identical announcements")
		}
	}
	st.Claim([]string{"plug_a"})
	if contains(st.Announcement(), "plug_a") {
		t.Error("claimed tool leaves the announcement")
	}
	st.Claim([]string{"plug_b"})
	if st.Announcement() != "" {
		t.Error("fully-claimed state produces no announcement")
	}
}

func TestFingerprintChangesWithFace(t *testing.T) {
	a := []ToolInfo{{Name: "x", Description: "d"}}
	b := []ToolInfo{{Name: "x", Description: "e"}}
	if Fingerprint(a) == Fingerprint(b) {
		t.Error("different descriptions must fingerprint differently")
	}
	if Fingerprint(a) != Fingerprint([]ToolInfo{{Name: "x", Description: "d"}}) {
		t.Error("same content must fingerprint identically")
	}
}

func TestSearchScoring(t *testing.T) {
	un := []ToolInfo{
		{Name: "web_search"},
		{Name: "webfetch", Description: "fetch a web page"},
		{Name: "search_web_deep"},
	}
	// Exact beats prefix beats substring.
	got := Search(un, "web_search", 0)
	if len(got) == 0 || got[0].Name != "web_search" {
		t.Errorf("exact name first, got %v", got)
	}
	// Prefix match on partial query.
	if got := Search(un, "webf", 0); len(got) != 1 || got[0].Name != "webfetch" {
		t.Errorf("prefix match, got %v", got)
	}
	// Description keyword hits.
	if got := Search(un, "page", 0); len(got) != 1 || got[0].Name != "webfetch" {
		t.Errorf("description keyword match, got %v", got)
	}
	// Limit caps the result count.
	if got := Search(un, "web", 2); len(got) != 2 {
		t.Errorf("limit respected, got %v", got)
	}
	// Empty query and no match return empty — never an error.
	if got := Search(un, "", 0); len(got) != 0 {
		t.Errorf("empty query = empty result, got %v", got)
	}
	if got := Search(un, "zzz", 0); len(got) != 0 {
		t.Errorf("no match = empty result, got %v", got)
	}
}

func TestPlanIntersect(t *testing.T) {
	plan := Plan{
		Deferred: map[string]ToolInfo{"plug_a": {Name: "plug_a"}, "plug_b": {Name: "plug_b"}},
		Hidden:   map[string]ToolInfo{"plug_h": {Name: "plug_h"}},
	}
	child := plan.Intersect(map[string]bool{"plug_a": true, "plug_h": true, "read": true})
	if !child.HasDeferred() || len(child.Deferred) != 1 {
		t.Errorf("child plan keeps only its own tools, got %+v", child)
	}
	if _, ok := child.Hidden["plug_h"]; !ok {
		t.Error("child hidden set intersects too")
	}
	if empty := plan.Intersect(map[string]bool{"read": true}); empty.HasDeferred() {
		t.Errorf("empty intersection has no deferred, got %+v", empty)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
