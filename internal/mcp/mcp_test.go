package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/smallnest/pigo/internal/agentcore"
)

// fakeEnv re-executes this test binary as an MCP server: `go test` cannot ship
// a second binary, so the standard trick is to have TestMain notice the env var
// and serve on stdio instead of running tests. That exercises the real
// subprocess + stdio + line-delimited JSON-RPC path end to end.
const (
	fakeEnv     = "PIGO_MCP_FAKE_SERVER"
	fakeFailEnv = "PIGO_MCP_FAKE_FAIL"
)

// fakeTools is what the in-test server advertises.
var fakeTools = []wireTool{
	{Name: "read_thing", Description: "reads a thing", InputSchema: json.RawMessage(`{"type":"object"}`),
		Annotations: &annotations{ReadOnly: true, Idempotent: true}},
	{Name: "write_thing", Description: "writes a thing", InputSchema: json.RawMessage(`{"type":"object"}`),
		Annotations: &annotations{Destructive: true, OpenWorld: true}},
	{Name: "plain_thing", Description: "no annotations"},
}

func TestMain(m *testing.M) {
	if os.Getenv(fakeEnv) == "1" {
		runFakeServer()
		return
	}
	os.Exit(m.Run())
}

// runFakeServer is the child: one JSON-RPC reply per request line on stdout.
func runFakeServer() {
	if os.Getenv(fakeFailEnv) == "1" {
		os.Exit(1) // simulate a server that dies at startup
	}
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	out := bufio.NewWriter(os.Stdout)
	for in.Scan() {
		line := in.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		var req struct {
			ID     *json.RawMessage `json:"id"`
			Method string           `json:"method"`
			Params json.RawMessage  `json:"params"`
		}
		if err := json.Unmarshal(line, &req); err != nil {
			continue
		}
		if req.ID == nil {
			continue // notification: no reply
		}
		var result any
		switch req.Method {
		case "initialize":
			result = initResult{ProtocolVersion: ProtocolVersion, ServerInfo: &serverInfo{Name: "fake", Version: "1.0"}}
		case "tools/list":
			result = toolListResult{Tools: fakeTools}
		case "tools/call":
			var p callParams
			_ = json.Unmarshal(req.Params, &p)
			result = callResult{Content: []contentBlock{{Type: "text", Text: "called " + p.Name + " with " + string(p.Arguments)}}}
		default:
			out.Write(append(mustJSON(map[string]any{
				"jsonrpc": "2.0", "id": req.ID,
				"error": map[string]any{"code": -32601, "message": "method not found"},
			}), '\n'))
			out.Flush()
			continue
		}
		out.Write(append(mustJSON(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result}), '\n'))
		out.Flush()
	}
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// fakeServerConfig builds a config pointing at this test binary in server mode.
// fail=true makes the child exit immediately.
func fakeServerConfig(t *testing.T, name string, fail bool) ServerConfig {
	t.Helper()
	env := append(os.Environ(), fakeEnv+"=1")
	if fail {
		env = append(env, fakeFailEnv+"=1")
	}
	return ServerConfig{Name: name, Command: os.Args[0], Env: env, TimeoutSeconds: 20}
}

func TestToolName(t *testing.T) {
	if got := ToolName("fs", "read_file"); got != "mcp__fs__read_file" {
		t.Errorf("ToolName = %q, want mcp__fs__read_file", got)
	}
}

func TestServerConfigValidate(t *testing.T) {
	cases := []struct {
		name    string
		cfg     ServerConfig
		wantErr bool
	}{
		{"ok", ServerConfig{Name: "fs", Command: "npx"}, false},
		{"no name", ServerConfig{Command: "npx"}, true},
		{"no command", ServerConfig{Name: "fs"}, true},
		{"underscore in name", ServerConfig{Name: "my_fs", Command: "npx"}, true},
	}
	for _, c := range cases {
		err := c.cfg.Validate()
		if (err != nil) != c.wantErr {
			t.Errorf("%s: Validate() error = %v, wantErr %v", c.name, err, c.wantErr)
		}
	}
}

func TestServerConfigDefaults(t *testing.T) {
	cfg := ServerConfig{Name: "fs"}
	if !cfg.IsEnabled() {
		t.Error("unset Enabled should default to on")
	}
	if cfg.Timeout() != DefaultTimeout {
		t.Errorf("Timeout() = %v, want %v", cfg.Timeout(), DefaultTimeout)
	}
	if cfg.Concurrency() != DefaultMaxParallel {
		t.Errorf("Concurrency() = %d, want %d", cfg.Concurrency(), DefaultMaxParallel)
	}
	off := false
	cfg.Enabled = &off
	if cfg.IsEnabled() {
		t.Error("explicit false should disable")
	}
	cfg.Enabled = nil
	cfg.DisabledTools = []string{"Write_File"}
	if !cfg.Disabled("write_file") {
		t.Error("Disabled should match case-insensitively")
	}
	if cfg.Disabled("read_file") {
		t.Error("Disabled should not match other tools")
	}
}

// TestConnectListsToolsAndCalls is the end-to-end path: launch, handshake,
// cache the tool list, invoke a tool, and read the result.
func TestConnectListsToolsAndCalls(t *testing.T) {
	m := Connect(context.Background(), []ServerConfig{fakeServerConfig(t, "fake", false)}, nil, nil)
	defer m.Close()

	tools := m.Tools()
	if len(tools) != len(fakeTools) {
		t.Fatalf("Tools() = %d, want %d", len(tools), len(fakeTools))
	}
	// Sorted by server-local name.
	if tools[0].Name() != "mcp__fake__plain_thing" {
		t.Errorf("first tool = %q, want mcp__fake__plain_thing (sorted)", tools[0].Name())
	}

	res, err := tools[0].Execute(context.Background(), "id1", json.RawMessage(`{"a":1}`), nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	tc, ok := res.Content[0].(agentcore.TextContent)
	if !ok {
		t.Fatalf("content[0] = %T, want agentcore.TextContent", res.Content[0])
	}
	if !strings.Contains(tc.Text, "called plain_thing") {
		t.Errorf("content = %q, want it to mention plain_thing", tc.Text)
	}

	st := m.Status()
	if len(st) != 1 || !st[0].Connected || st[0].ToolCount != len(fakeTools) {
		t.Errorf("Status = %+v, want one connected server with %d tools", st, len(fakeTools))
	}
	if st[0].ServerInfo != "fake 1.0" {
		t.Errorf("ServerInfo = %q, want %q", st[0].ServerInfo, "fake 1.0")
	}
}

// TestConnectDisabledToolHidesOnlyRegistration is the per-tool disable contract
// (spec §3.1): the tool leaves the face, the connection stays up, and the
// server is never asked to re-list.
func TestConnectDisabledToolHidesOnlyRegistration(t *testing.T) {
	cfg := fakeServerConfig(t, "fake", false)
	cfg.DisabledTools = []string{"write_thing"}
	m := Connect(context.Background(), []ServerConfig{cfg}, nil, nil)
	defer m.Close()

	for _, tl := range m.Tools() {
		if tl.Name() == "mcp__fake__write_thing" {
			t.Error("disabled tool should not be in the face")
		}
	}
	if got := m.HiddenNames(); len(got) != 1 || got[0] != "mcp__fake__write_thing" {
		t.Errorf("HiddenNames() = %v, want [mcp__fake__write_thing]", got)
	}
	st := m.Status()
	// The connection survives: still connected, still counting all 3 tools.
	if !st[0].Connected || st[0].ToolCount != 3 || st[0].DisabledCount != 1 {
		t.Errorf("Status = %+v, want connected/3 tools/1 disabled", st)
	}
}

// TestConnectDisabledServerNeverLaunched covers the server-level switch.
func TestConnectDisabledServerNeverLaunched(t *testing.T) {
	off := false
	cfg := fakeServerConfig(t, "fake", false)
	cfg.Enabled = &off
	m := Connect(context.Background(), []ServerConfig{cfg}, nil, nil)
	defer m.Close()

	if len(m.Tools()) != 0 {
		t.Errorf("disabled server should contribute no tools")
	}
	st := m.Status()
	if len(st) != 1 || st[0].Connected || st[0].Enabled {
		t.Errorf("Status = %+v, want one enabled=false, connected=false entry", st)
	}
}

// TestConnectFailureIsolated is the bootstrap contract: one dead server is
// recorded and skipped, the healthy one still comes up.
func TestConnectFailureIsolated(t *testing.T) {
	var warn strings.Builder
	m := Connect(context.Background(), []ServerConfig{
		fakeServerConfig(t, "dead", true),
		fakeServerConfig(t, "alive", false),
	}, &warn, nil)
	defer m.Close()

	if len(m.Tools()) != len(fakeTools) {
		t.Errorf("Tools() = %d, want %d (the live server only)", len(m.Tools()), len(fakeTools))
	}
	if !strings.Contains(warn.String(), "dead") {
		t.Errorf("warn log = %q, want it to name the dead server", warn.String())
	}
	st := m.Status()
	if len(st) != 2 || st[0].Error == "" || !st[1].Connected {
		t.Errorf("Status = %+v, want first errored, second connected", st)
	}
}

func TestConnectInvalidConfigRecorded(t *testing.T) {
	var warn strings.Builder
	m := Connect(context.Background(), []ServerConfig{{Command: "npx"}}, &warn, nil)
	if len(m.Tools()) != 0 {
		t.Error("invalid config should yield no tools")
	}
	if !strings.Contains(warn.String(), "needs a name") {
		t.Errorf("warn log = %q, want the validation reason", warn.String())
	}
}

// TestMCPToolEffectAndSchema pins the annotation → side-effect mapping (T5.2)
// and the schema fallback.
func TestMCPToolEffectAndSchema(t *testing.T) {
	m := Connect(context.Background(), []ServerConfig{fakeServerConfig(t, "fake", false)}, nil, nil)
	defer m.Close()

	byName := map[string]*MCPTool{}
	for _, tl := range m.Tools() {
		byName[tl.Name()] = tl
	}
	read := byName["mcp__fake__read_thing"]
	if read == nil {
		t.Fatal("read_thing missing")
	}
	if eff := agentcore.EffectOf(read); !eff.ReadOnly {
		t.Errorf("read_thing effect = %+v, want ReadOnly", eff)
	}
	if !read.Replayable() {
		t.Error("read-only tool should be replayable")
	}
	write := byName["mcp__fake__write_thing"]
	if write == nil {
		t.Fatal("write_thing missing")
	}
	if eff := agentcore.EffectOf(write); !eff.Destructive || eff.Scope != agentcore.ScopeNetwork {
		t.Errorf("write_thing effect = %+v, want destructive + network scope", eff)
	}
	if write.Replayable() {
		t.Error("destructive non-idempotent tool must not be replayable")
	}
	plain := byName["mcp__fake__plain_thing"]
	if string(plain.Schema()) != `{"type":"object"}` {
		t.Errorf("absent schema = %s, want object fallback", plain.Schema())
	}
	if plain.Description() != "no annotations" {
		t.Errorf("Description = %q", plain.Description())
	}
}

// TestCallToolTimeoutKeepsRunAlive guards against a hung server blocking
// forever: every round trip is bounded by the configured timeout.
func TestCallToolTimeoutKeepsRunAlive(t *testing.T) {
	cfg := fakeServerConfig(t, "fake", false)
	cfg.TimeoutSeconds = 1
	m := Connect(context.Background(), []ServerConfig{cfg}, nil, nil)
	defer m.Close()

	tools := m.Tools()
	if len(tools) == 0 {
		t.Fatal("no tools")
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = tools[0].Execute(context.Background(), "id", json.RawMessage(`{}`), nil)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Execute hung past the timeout")
	}
}

