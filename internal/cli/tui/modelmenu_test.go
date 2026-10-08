package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/cli"
	"github.com/smallnest/pigo/internal/cli/config"
	"github.com/smallnest/pigo/internal/provider"
)

// TestModelArgToken pins the /model argument-stage trigger: it fires only once
// the name stage is over (whitespace right after "/model"), so a bare "/model"
// and the different command "/models" stay with the command-name popup.
func TestModelArgToken(t *testing.T) {
	cases := []struct {
		buffer  string
		partial string
		ok      bool
	}{
		{"/model", "", false},
		{"/model ", "", true},
		{"/model g", "g", true},
		{"/model  openai", "openai", true},
		{"/models", "", false},
		{"/models fetch", "", false},
		{"model gl", "", false},
		{"hey /model g", "", false},
	}

	for _, tc := range cases {
		partial, ok := modelArgToken(tc.buffer)
		if ok != tc.ok || (ok && partial != tc.partial) {
			t.Errorf("modelArgToken(%q) = %q, %v; want %q, %v", tc.buffer, partial, ok, tc.partial, tc.ok)
		}
	}
}

// TestModelMenuRefreshAndFilter covers the candidate assembly: preset catalog
// ids plus fetched ids, case-insensitive filtering, dedup, and the current
// model marker.
func TestModelMenuRefreshAndFilter(t *testing.T) {
	if len(provider.PresetCatalog) == 0 {
		t.Skip("no preset catalog in this build")
	}
	cur := provider.PresetCatalog[0].ID
	live := &cli.LiveConfig{Model: cur, ProviderName: "openrouter", FetchedModels: []string{"custom/model-x"}}

	mm := &modelMenu{theme: DefaultTheme()}
	mm.refresh("", live)
	if !mm.active || len(mm.items) == 0 {
		t.Fatal("refresh with empty partial should open the popup")
	}
	var sawCurrent, sawFetched bool
	for _, it := range mm.items {
		if it.id == cur && it.current {
			sawCurrent = true
		}
		if it.id == "custom/model-x" {
			sawFetched = true
		}
	}
	if !sawCurrent {
		t.Errorf("current model %q not marked in the candidate set", cur)
	}
	if !sawFetched {
		t.Error("fetched model id missing from the candidate set")
	}

	// Filtering narrows; a filter matching nothing closes the popup.
	mm.refresh("gpt-4o", live)
	if !mm.active {
		t.Fatal("filtered refresh closed the popup despite matches")
	}
	for _, it := range mm.items {
		if !strings.Contains(strings.ToLower(it.id+" "+it.label), "gpt-4o") {
			t.Errorf("candidate %q does not match filter %q", it.id, "gpt-4o")
		}
	}
	mm.refresh("zzz-no-such-model", live)
	if mm.active {
		t.Error("popup stayed active with an empty filtered set")
	}
}

// TestModelMenuEnterSwitchesModel drives the chained loop (grok model
// switcher): typing "/model " opens the dropdown over the composer, the first
// Enter chains into the effort sub-list WITHOUT switching, and the second
// Enter dispatches "/model <id> <level>" — the live config (the pointer the
// run loop reads) reflects both the model and the effort.
func TestModelMenuEnterSwitchesModel(t *testing.T) {
	if len(provider.PresetCatalog) < 2 {
		t.Skip("no preset catalog in this build")
	}
	cur := provider.PresetCatalog[0].ID
	m := NewModel(Options{Model: cur})
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	m = typeInto(t, m, "/model ").(Model)
	if !m.modelMenu.active {
		t.Fatalf("typing %q did not open the model dropdown", "/model ")
	}
	want, ok := m.modelMenu.current()
	if !ok {
		t.Fatal("no highlighted candidate")
	}

	// First Enter: chain into the effort phase, nothing applied yet.
	next, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(Model)
	if !m.modelMenu.active || m.modelMenu.phase != modelPhaseEffort {
		t.Fatalf("first Enter did not chain into the effort phase (active=%v phase=%v)", m.modelMenu.active, m.modelMenu.phase)
	}
	if m.live.Model != cur {
		t.Errorf("chaining Enter switched the model early: %q, want %q kept", m.live.Model, cur)
	}
	if m.input.Value() != "/model "+want.id+" " {
		t.Errorf("buffer after chain = %q, want %q", m.input.Value(), "/model "+want.id+" ")
	}

	// Second Enter on the preselected/highlighted level applies both.
	levelItem, ok := m.modelMenu.current()
	if !ok {
		t.Fatal("no highlighted level after chaining")
	}
	level := levelItem.id
	next, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(Model)
	if m.live.Model != want.id {
		t.Errorf("live model = %q, want switched to %q", m.live.Model, want.id)
	}
	if m.live.ThinkingLevel != agentcore.ThinkingLevel(level) {
		t.Errorf("live effort = %q, want %q", m.live.ThinkingLevel, level)
	}
	if m.modelMenu.active {
		t.Error("dropdown stayed open after the applying Enter")
	}
	if content := stripANSI(m.transcript.renderAll()); !strings.Contains(content, want.id) {
		t.Errorf("transcript missing the switch echo for %q:\n%s", want.id, content)
	}
}

// TestThinkArgToken pins the /think//effect argument-stage trigger: same
// whitespace rule as /model; the alias maps to the same popup.
func TestThinkArgToken(t *testing.T) {
	cases := []struct {
		buffer string
		prefix string
		ok     bool
	}{
		{"/think", "", false},
		{"/think ", "/think ", true},
		{"/think hi", "/think ", true},
		{"/effect ", "/think ", true},
		{"/effects", "", false},
		{"/thinks", "", false},
	}
	for _, tc := range cases {
		partial, prefix, ok := thinkArgToken(tc.buffer)
		if ok != tc.ok || (ok && prefix != tc.prefix) {
			t.Errorf("thinkArgToken(%q) = %q, %v; want prefix %q, %v", tc.buffer, partial, ok, tc.prefix, tc.ok)
		}
	}
}

// TestThinkDropdownSwitchesLevel covers the /think popup: the effort levels
// list with the current level preselected, Enter switching live.ThinkingLevel
// through the shared registry action.
func TestThinkDropdownSwitchesLevel(t *testing.T) {
	m := NewModel(Options{Model: "m"})
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	m = typeInto(t, m, "/think ").(Model)
	if !m.modelMenu.active || m.modelMenu.phase != modelPhaseEffort {
		t.Fatalf("/think did not open the effort dropdown (active=%v phase=%v)", m.modelMenu.active, m.modelMenu.phase)
	}
	if len(m.modelMenu.items) != len(effortLevels) {
		t.Fatalf("effort items = %d, want %d", len(m.modelMenu.items), len(effortLevels))
	}
	if cur := m.modelMenu.items[m.modelMenu.selected]; !cur.current || cur.id != string(agentcore.ThinkingOff) {
		t.Errorf("preselected level = %q (current=%v), want off preselected on a fresh live config", cur.id, cur.current)
	}

	// Down twice: off → minimal → low; Enter applies "low".
	m.modelMenu.moveDown()
	m.modelMenu.moveDown()
	next, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(Model)
	if m.live.ThinkingLevel != agentcore.ThinkingLow {
		t.Errorf("live effort = %q, want low", m.live.ThinkingLevel)
	}
	if m.modelMenu.active {
		t.Error("effort dropdown stayed open after Enter")
	}
}

// TestModelMenuEffortFilter pins the effort sub-list behavior: the text after
// the chain prefix filters levels and the current one carries the marker.
func TestModelMenuEffortFilter(t *testing.T) {
	live := &cli.LiveConfig{Model: "m", ThinkingLevel: agentcore.ThinkingHigh}
	mm := &modelMenu{theme: DefaultTheme()}
	mm.refreshEffort("", "/model m ", live)
	if !mm.active || mm.phase != modelPhaseEffort || mm.chainCmd != "/model m " {
		t.Fatalf("refreshEffort state = active=%v phase=%v chain=%q", mm.active, mm.phase, mm.chainCmd)
	}
	if cur := mm.items[mm.selected]; !cur.current || cur.id != "high" {
		t.Errorf("preselected = %q (current=%v), want high", cur.id, cur.current)
	}
	mm.refreshEffort("xh", "/model m ", live)
	if len(mm.items) != 1 || mm.items[0].id != "xhigh" {
		t.Errorf("filtered levels = %v, want [xhigh]", mm.items)
	}
}

// TestModelMenuEscKeepsModel pins the Esc semantics: closing the dropdown never
// switches the model (qwen "Kept model as" parity).
func TestModelMenuEscKeepsModel(t *testing.T) {
	if len(provider.PresetCatalog) == 0 {
		t.Skip("no preset catalog in this build")
	}
	cur := provider.PresetCatalog[0].ID
	m := NewModel(Options{Model: cur})
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m = typeInto(t, m, "/model ").(Model)
	if !m.modelMenu.active {
		t.Fatal("dropdown did not open")
	}
	next, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = next.(Model)
	if m.modelMenu.active {
		t.Error("esc did not close the dropdown")
	}
	if m.live.Model != cur {
		t.Errorf("esc switched the model to %q; want %q kept", m.live.Model, cur)
	}
}

// TestModelMenuProfilesAreTheFace pins the config-profile source (T7.3 实测
// 反馈: /model 列表来源 = 配置里的模型 id，grok 对齐): when [models."<id>"]
// profiles are declared the dropdown lists exactly those (id — label —
// description), the active profile's wire model carries the current marker,
// and the preset catalog stays out. With no usable profiles the preset +
// fetched fallback face remains.
func TestModelMenuProfilesAreTheFace(t *testing.T) {
	live := &cli.LiveConfig{
		Model:        "gpt-5.6-luna",
		ProviderName: "openai",
	}
	live.ModelProfiles = map[string]config.ModelProfile{
		"gpt-56": {Model: "gpt-5.6-luna", Name: "GPT-5.6 Luna", Description: "gateway profile", Provider: "openai", ContextWindow: 1050000},
		"ds":     {Name: "DeepSeek V4", Provider: "deepseek"},
		// A grok-style scalar under [models] decodes to an empty profile and
		// must stay out of the face.
		"default": {},
	}
	mm := &modelMenu{theme: DefaultTheme()}
	mm.refresh("", live)
	if !mm.active {
		t.Fatal("profile face did not open")
	}
	if len(mm.items) != 2 {
		t.Fatalf("items = %d, want 2 profiles (empty one ignored)", len(mm.items))
	}
	// Alphabetical id order: deepseek.v4-style ids sort before gpt keys —
	// here "ds" < "gpt-56".
	if mm.items[0].id != "ds" || mm.items[1].id != "gpt-56" {
		t.Errorf("ids = %q, %q; want ds then gpt-56", mm.items[0].id, mm.items[1].id)
	}
	if !mm.items[1].current {
		t.Error("the active profile (wire model = live.Model) is not marked current")
	}
	if mm.items[1].desc != "gateway profile" {
		t.Errorf("profile description = %q, want it on the row", mm.items[1].desc)
	}
	// The presets stay out while a profile face exists.
	for _, it := range mm.items {
		if it.provider == "openrouter" && it.label != "" {
			t.Errorf("preset row %q leaked into the profile face", it.id)
		}
	}

	// Filtering narrows to the matching profile.
	mm.refresh("luna", live)
	if len(mm.items) != 1 || mm.items[0].id != "gpt-56" {
		t.Fatalf("filtered = %v, want gpt-56 only", mm.items)
	}

	// No usable profiles → the preset/fetched fallback face.
	fallback := &cli.LiveConfig{Model: "m", ProviderName: "openrouter", FetchedModels: []string{"custom/x"}}
	mm.refresh("", fallback)
	if !mm.active || len(mm.items) == 0 {
		t.Fatal("fallback face did not open without profiles")
	}
}
