package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/smallnest/pigo/internal/cli/config"
	"github.com/smallnest/pigo/internal/cli/prompts"
	"github.com/smallnest/pigo/internal/mcp"
	"github.com/smallnest/pigo/internal/runtime"
	"github.com/smallnest/pigo/internal/testenv"
)

// testSurface builds a SurfaceDeps wired to a temp config + a two-skill view,
// so the panel toggles write a real file without touching ~/.pigo.
func testSurface(t *testing.T) (prompts.SurfaceDeps, string) {
	t.Helper()
	cfgPath := filepath.Join(testenv.Dir(t), "config.toml")
	// SetSkillsDisabled reads-then-writes the file, so it must exist.
	if err := os.WriteFile(cfgPath, nil, 0o644); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	view := []*runtime.Skill{
		{Frontmatter: runtime.SkillFrontmatter{Name: "alpha-skill", Description: "does alpha things"}},
		{Frontmatter: runtime.SkillFrontmatter{Name: "beta-skill", Description: "does beta things", DisableModelInvocation: true}},
	}
	return prompts.SurfaceDeps{
		Skills:     func() []*runtime.Skill { return view },
		ConfigPath: cfgPath,
	}, cfgPath
}

// TestGatherSkillRowsAndToggle covers the skills panel data pass: enabled
// rows carry their invocation-mode tag, config-disabled names appear tagged
// [disabled], and ToggleSkill flips the config + the gathered rows.
func TestGatherSkillRowsAndToggle(t *testing.T) {
	deps, cfgPath := testSurface(t)
	s := &runSession{surface: deps}
	rows, note := gatherSkillRows(s)
	if note != "" {
		t.Fatalf("gather note = %q", note)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	if rows[0].title != "alpha-skill" || rows[0].tag != "[model-invocable]" || rows[0].off {
		t.Errorf("row0 = %+v, want alpha-skill [model-invocable] on", rows[0])
	}
	if rows[1].tag != "[slash-only]" {
		t.Errorf("row1 tag = %q, want [slash-only]", rows[1].tag)
	}

	// Toggle the first skill off: config write + row flips to [disabled].
	if msg := deps.ToggleSkill("alpha-skill", true); !strings.Contains(msg, "disabled") {
		t.Fatalf("toggle feedback = %q", msg)
	}
	cfg, err := config.LoadFileConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadFileConfig: %v", err)
	}
	if len(cfg.Skills.Disabled) != 1 || cfg.Skills.Disabled[0] != "alpha-skill" {
		t.Errorf("config disabled = %v, want [alpha-skill]", cfg.Skills.Disabled)
	}
	rows, _ = gatherSkillRows(s)
	if rows[0].tag != "[disabled]" || !rows[0].off {
		t.Errorf("row0 after disable = %+v, want [disabled] off", rows[0])
	}

	// Toggle back on: the row rejoins the enabled view.
	if msg := deps.ToggleSkill("alpha-skill", false); !strings.Contains(msg, "enabled") {
		t.Fatalf("re-enable feedback = %q", msg)
	}
	rows, _ = gatherSkillRows(s)
	if rows[0].tag != "[model-invocable]" || rows[0].off {
		t.Errorf("row0 after enable = %+v, want back on", rows[0])
	}
}

// TestGatherMCPRowsNilManager pins the empty face: no manager configured is a
// note, not an error or a panic.
func TestGatherMCPRowsNilManager(t *testing.T) {
	s := &runSession{}
	rows, note := gatherMCPRows(s)
	if rows != nil || note == "" {
		t.Errorf("gatherMCPRows(nil manager) = %v, %q; want nil rows + note", rows, note)
	}
}

// TestSkillsPanelFlow drives the bare /skills panel end to end: the command
// opens the modal over the transcript, Enter toggles the highlighted row
// through the surface deps (feedback lands in the note row), and Esc closes.
func TestSkillsPanelFlow(t *testing.T) {
	store := newTestStore(t)
	s, _, err := newRunSessionWithStore(store, Options{Model: "m", ProviderName: "p"})
	if err != nil {
		t.Fatalf("newRunSessionWithStore: %v", err)
	}
	deps, _ := testSurface(t)
	s.surface = deps
	m := apply(t, NewModel(Options{Model: "m", ProviderName: "p"}).withSession(s, nil),
		tea.WindowSizeMsg{Width: 80, Height: 30})

	m = typeInto(t, m, "/skills").(Model)
	m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.skillsP.open {
		t.Fatal("/skills did not open the panel")
	}
	if len(m.skillsP.rows) != 2 {
		t.Fatalf("panel rows = %d, want 2", len(m.skillsP.rows))
	}
	content := stripANSI(m.View().Content) // the panel is a View overlay, not transcript blocks
	if !strings.Contains(content, "does alpha things") {
		t.Errorf("panel row missing the skill description:\n%s", content)
	}

	// Enter toggles the highlighted (first) skill off; the note reports it.
	m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.skillsP.rows[0].tag != "[disabled]" {
		t.Errorf("row after toggle = %+v, want [disabled]", m.skillsP.rows[0])
	}
	if !strings.Contains(m.skillsP.note, "disabled") {
		t.Errorf("note after toggle = %q, want the disable feedback", m.skillsP.note)
	}

	// Filter narrows; a filter matching nothing shows the empty note.
	m = typeInto(t, m, "beta").(Model)
	if vis := m.skillsP.visible(); len(vis) != 1 || vis[0].title != "beta-skill" {
		t.Fatalf("filtered rows = %+v, want beta-skill only", vis)
	}

	m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.skillsP.open {
		t.Error("esc did not close the panel")
	}
}

// TestMCPPanelFlow opens the /mcp panel against a nil-manager surface: the
// note explains the empty face and Esc closes.
func TestMCPPanelFlow(t *testing.T) {
	store := newTestStore(t)
	s, _, err := newRunSessionWithStore(store, Options{Model: "m", ProviderName: "p"})
	if err != nil {
		t.Fatalf("newRunSessionWithStore: %v", err)
	}
	s.surface = prompts.SurfaceDeps{}
	m := apply(t, NewModel(Options{Model: "m", ProviderName: "p"}).withSession(s, nil),
		tea.WindowSizeMsg{Width: 80, Height: 30})

	m = typeInto(t, m, "/mcp").(Model)
	m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.mcpP.open {
		t.Fatal("/mcp did not open the panel")
	}
	if m.mcpP.note == "" {
		t.Error("empty MCP face should carry a note")
	}
	if len(m.mcpP.rows) != 0 {
		t.Errorf("rows = %d, want 0 for a nil manager", len(m.mcpP.rows))
	}
	m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.mcpP.open {
		t.Error("esc did not close the panel")
	}
}

// TestBareModelOpensDropdown pins the approved entry rule: a bare "/model"
// submitted fills the trailing space and opens the dropdown instead of the
// registry's text echo; same for /think with the effort list.
func TestBareModelOpensDropdown(t *testing.T) {
	m := NewModel(Options{Model: "m"})
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	m = typeInto(t, m, "/model").(Model)
	m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.modelMenu.active || m.modelMenu.phase != modelPhaseList {
		t.Fatalf("bare /model did not open the model dropdown")
	}
	if m.input.Value() != "/model " {
		t.Errorf("buffer = %q, want %q", m.input.Value(), "/model ")
	}

	m.input.Clear()
	m.modelMenu.close()
	m = typeInto(t, m, "/think").(Model)
	m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.modelMenu.active || m.modelMenu.phase != modelPhaseEffort {
		t.Fatalf("bare /think did not open the effort dropdown")
	}
	if m.input.Value() != "/think " {
		t.Errorf("buffer = %q, want %q", m.input.Value(), "/think ")
	}
}

// TestSkillsPanelClipsLongDescriptions pins the grok row shape (T7.3 实测反
// 馈: skill name + 一部分描述, not the full blurb): a long description is
// clipped to the row width with an ellipsis while the name and mode tag stay
// readable, and the full text stays available through the /skills info path.
func TestSkillsPanelClipsLongDescriptions(t *testing.T) {
	long := "does alpha things — " + strings.Repeat("细节描述填充，", 40)
	deps, _ := testSurface(t)
	view := []*runtime.Skill{
		{Frontmatter: runtime.SkillFrontmatter{Name: "alpha-skill", Description: long}},
	}
	deps.Skills = func() []*runtime.Skill { return view }
	store := newTestStore(t)
	s, _, err := newRunSessionWithStore(store, Options{Model: "m", ProviderName: "p"})
	if err != nil {
		t.Fatalf("newRunSessionWithStore: %v", err)
	}
	s.surface = deps
	m := apply(t, NewModel(Options{Model: "m", ProviderName: "p"}).withSession(s, nil),
		tea.WindowSizeMsg{Width: 80, Height: 30})
	m = typeInto(t, m, "/skills").(Model)
	m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})

	content := stripANSI(m.View().Content)
	if !strings.Contains(content, "alpha-skill") || !strings.Contains(content, "[model-invocable]") {
		t.Errorf("row lost the name or the mode tag:\n%s", content)
	}
	if strings.Contains(content, long) {
		t.Error("the full description rendered; want it clipped to the row")
	}
	if !strings.Contains(content, "…") {
		t.Errorf("clipped description missing the ellipsis:\n%s", content)
	}
	// The rendered row never exceeds the panel width (no wrap).
	for _, line := range strings.Split(content, "\n") {
		if w := uiW(line); w > 96+4 { // panel body cap + border
			t.Errorf("row of width %d overflows the panel: %q", w, line)
		}
	}
}

// fakeMCPHTTP is a minimal Streamable HTTP MCP server (the same wire shape
// internal/mcp's own fakes speak) so the tui panel test can drive a real
// Manager with advertised tools.
type fakeMCPHTTP struct{ session int }

func (f *fakeMCPHTTP) handler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	sid := "S1"
	if req.Method == "initialize" {
		f.session++
		sid = fmt.Sprintf("S%d", f.session)
	} else if got := r.Header.Get("Mcp-Session-Id"); got != "" {
		sid = got
	}
	if req.ID == nil {
		w.Header().Set("Mcp-Session-Id", sid)
		w.WriteHeader(http.StatusAccepted)
		return
	}
	var result any
	switch req.Method {
	case "initialize":
		result = map[string]any{"protocolVersion": "2025-06-18", "serverInfo": map[string]any{"name": "fake-http", "version": "1.0"}}
	case "tools/list":
		result = map[string]any{"tools": []map[string]any{
			{"name": "read_thing", "description": "reads a thing", "inputSchema": map[string]any{"type": "object"}},
			{"name": "write_thing", "description": "writes a thing", "inputSchema": map[string]any{"type": "object"}},
		}}
	default:
		w.Header().Set("Mcp-Session-Id", sid)
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "error": map[string]any{"code": -32601, "message": "nope"}})
		return
	}
	w.Header().Set("Mcp-Session-Id", sid)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
}

// TestMCPPanelTreeFlow drives the two-level MCP panel (T7.3 实测反馈: server
// 和 tool 级别都可看可切，grok /mcps 对齐) against a real Manager: the server
// row shows the connection face, Enter expands it into tool rows carrying
// their per-tool switch state, Space on a tool row disables it (config write
// included), Enter re-enables it, the expansion survives the regather, and
// Esc closes.
func TestMCPPanelTreeFlow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc((&fakeMCPHTTP{}).handler))
	t.Cleanup(srv.Close)

	cfgPath := filepath.Join(testenv.Dir(t), "config.toml")
	cfgToml := fmt.Sprintf("[[mcp.servers]]\nname = \"fs\"\ntype = \"http\"\nurl = %q\ndisabled_tools = []\n", srv.URL)
	if err := os.WriteFile(cfgPath, []byte(cfgToml), 0o644); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	mgr := mcp.Connect(context.Background(), []mcp.ServerConfig{
		{Name: "fs", Type: "http", URL: srv.URL, TimeoutSeconds: 5, DisabledTools: []string{"write_thing"}},
	}, nil, nil)

	deps := prompts.SurfaceDeps{MCP: mgr, ConfigPath: cfgPath}
	store := newTestStore(t)
	s, _, err := newRunSessionWithStore(store, Options{Model: "m", ProviderName: "p"})
	if err != nil {
		t.Fatalf("newRunSessionWithStore: %v", err)
	}
	s.surface = deps
	m := apply(t, NewModel(Options{Model: "m", ProviderName: "p"}).withSession(s, nil),
		tea.WindowSizeMsg{Width: 80, Height: 30})

	m = typeInto(t, m, "/mcp").(Model)
	m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.mcpP.open || len(m.mcpP.rows) != 1 {
		t.Fatalf("/mcp did not open with one server row (open=%v rows=%d)", m.mcpP.open, len(m.mcpP.rows))
	}
	content := stripANSI(m.View().Content)
	if !strings.Contains(content, "fs") || !strings.Contains(content, "[connected]") {
		t.Errorf("server face missing:\n%s", content)
	}
	if strings.Contains(content, "read_thing") {
		t.Error("tools rendered before the server was expanded")
	}

	// Enter on the server row expands into its tool rows.
	m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	content = stripANSI(m.View().Content)
	if !strings.Contains(content, "read_thing") || !strings.Contains(content, "write_thing") {
		t.Errorf("expansion did not list the tools:\n%s", content)
	}
	if !strings.Contains(content, "[disabled]") {
		t.Errorf("the config-disabled tool is not tagged:\n%s", content)
	}

	// Filter to the enabled tool and Space-disable it (writes config).
	m = typeInto(t, m, "read").(Model)
	if vis := m.mcpP.visible(); len(vis) != 1 || vis[0].tool != "read_thing" {
		t.Fatalf("filtered rows = %+v, want read_thing only", vis)
	}
	m = apply(t, m, tea.KeyPressMsg{Code: tea.KeySpace})
	if !strings.Contains(m.mcpP.note, "hidden") && !strings.Contains(m.mcpP.note, "disable") {
		t.Errorf("note after space toggle = %q, want the disable feedback", m.mcpP.note)
	}
	cfg, err := config.LoadFileConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadFileConfig: %v", err)
	}
	if len(cfg.MCP.Servers) != 1 || len(cfg.MCP.Servers[0].DisabledTools) != 1 {
		t.Fatalf("config after disable = %+v, want read_thing disabled", cfg.MCP.Servers)
	}
	if !strings.EqualFold(cfg.MCP.Servers[0].DisabledTools[0], "read_thing") {
		t.Errorf("disabled tool = %q, want read_thing", cfg.MCP.Servers[0].DisabledTools[0])
	}

	// Clear the filter: the expansion survived the regather, and the toggle
	// flipped the row to [disabled] in place.
	for range 5 {
		m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyBackspace})
	}
	if !m.mcpP.rows[0].expanded {
		t.Error("regather collapsed the expanded server")
	}
	if vis := m.mcpP.visible(); len(vis) != 3 {
		t.Fatalf("rows after filter clear = %d, want server + 2 tools", len(vis))
	}
	if vis := m.mcpP.visible(); vis[1].title != "read_thing" || !vis[1].off {
		t.Errorf("row after disable = %+v, want read_thing off in place", vis[1])
	}

	// Down to read_thing and Enter re-enables it (config write clears).
	m.mcpP.moveDown()
	if row, _ := m.mcpP.selectedRow(); row.tool != "read_thing" {
		t.Fatalf("selected = %+v, want read_thing", row)
	}
	m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	cfg, err = config.LoadFileConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadFileConfig: %v", err)
	}
	if len(cfg.MCP.Servers) != 1 || len(cfg.MCP.Servers[0].DisabledTools) != 0 {
		t.Errorf("config after enable = %+v, want disabled_tools cleared", cfg.MCP.Servers)
	}

	m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.skillsP.open {
		t.Error("esc did not close the panel")
	}
}
