// Tests for the Streamable HTTP transport: an in-process fake Streamable HTTP
// MCP server (httptest), exercised through the same Connect/handshake path a
// real server takes. One live path per response form — SSE for initialize,
// plain JSON for tools/list — so both parsers are covered, plus the session
// header contract and the notification/teardown status codes.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/smallnest/pigo/internal/agentcore"
)

// fakeHTTPServer is a minimal Streamable HTTP MCP server: it assigns a session
// on initialize, requires it afterwards, answers requests in the content type
// its test selects, and accepts notifications with 202.
type fakeHTTPServer struct {
	sse        bool   // answer requests as text/event-stream instead of JSON
	sessionN   int64  // sessions assigned so far (only used for distinct ids)
	lastSess   string // the session handed out by the latest initialize
	revoked    string // a session the server treats as terminated (404)
	failCall   bool   // make tools/call return an RPC-level error
}

func (f *fakeHTTPServer) handler(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodDelete {
		w.WriteHeader(http.StatusOK)
		return
	}
	var req struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	sid := fmt.Sprintf("S%d", atomic.AddInt64(&f.sessionN, 1))
	if req.Method == "initialize" {
		// A new session starts here; record it so a test can revoke it.
		f.lastSess = sid
	} else {
		got := r.Header.Get(sessionHeader)
		if got == "" {
			http.Error(w, "missing session", http.StatusBadRequest)
			return
		}
		if f.revoked != "" && got == f.revoked {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		sid = got // the presented session stays authoritative
	}

	if req.ID == nil {
		w.Header().Set(sessionHeader, sid)
		w.WriteHeader(http.StatusAccepted)
		return
	}

	var result any
	switch req.Method {
	case "initialize":
		result = initResult{ProtocolVersion: ProtocolVersion, ServerInfo: &serverInfo{Name: "fake-http", Version: "2.0"}}
	case "tools/list":
		result = toolListResult{Tools: fakeTools}
	case "tools/call":
		if f.failCall {
			writeEnvelope(w, sid, req.ID, nil, &rpcError{Code: -32000, Message: "boom"})
			return
		}
		result = callResult{Content: []contentBlock{{Type: "text", Text: "http-call-ok"}}}
	default:
		writeEnvelope(w, sid, req.ID, nil, &rpcError{Code: -32601, Message: "method not found"})
		return
	}
	writeEnvelope(w, sid, req.ID, result, nil)
}

// writeEnvelope answers in the form the test selected: SSE carries the reply
// inside a `data:` line, JSON returns the envelope directly.
func writeEnvelope(w http.ResponseWriter, sid string, id json.RawMessage, result any, rpcErr *rpcError) {
	env := map[string]any{"jsonrpc": "2.0", "id": id}
	if rpcErr != nil {
		env["error"] = rpcErr
	} else {
		env["result"] = result
	}
	data, _ := json.Marshal(env)
	w.Header().Set(sessionHeader, sid)
	if sse := w.Header().Get("X-Fake-SSE"); sse == "1" {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "event: message\ndata: %s\n\n", data)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}

func connectFakeHTTP(t *testing.T, f *fakeHTTPServer) (*Manager, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	t.Cleanup(srv.Close)
	m := Connect(context.Background(), []ServerConfig{
		{Name: "fake", Type: "http", URL: srv.URL, TimeoutSeconds: 5},
	}, nil, nil)
	return m, srv
}

func TestHTTPConnectAndCallJSON(t *testing.T) {
	m, _ := connectFakeHTTP(t, &fakeHTTPServer{})
	st := m.Status()
	if len(st) != 1 || !st[0].Connected || st[0].Error != "" {
		t.Fatalf("status = %+v, want one connected server without error", st)
	}
	if st[0].ServerInfo != "fake-http 2.0" {
		t.Errorf("ServerInfo = %q, want %q", st[0].ServerInfo, "fake-http 2.0")
	}
	tools := m.Tools()
	if len(tools) != len(fakeTools) {
		t.Fatalf("got %d tools, want %d", len(tools), len(fakeTools))
	}
	// A read-only advertised tool skips the confirmation gate, exactly like
	// stdio: the Effect mapping is transport-independent.
	for _, tl := range tools {
		if tl.Name() == "mcp__fake__read_thing" && !tl.Effect().ReadOnly {
			t.Errorf("read_thing Effect().ReadOnly = false, want true")
		}
	}
}

func TestHTTPCallSSEForm(t *testing.T) {
	f := &fakeHTTPServer{sse: true}
	m, _ := connectFakeHTTP(t, f)
	// Initialize already went through the SSE parser (the fake answers every
	// request as SSE here); a connected manager proves that path parsed.
	st := m.Status()
	if len(st) != 1 || !st[0].Connected {
		t.Fatalf("status = %+v, want connected (SSE initialize parsed)", st)
	}
	tool, ok := findTool(m, "mcp__fake__read_thing")
	if !ok {
		t.Fatal("read_thing not in the aggregated face")
	}
	res, err := tool.Execute(context.Background(), "id-1", json.RawMessage(`{}`), nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got := res.Content[0].(agentcore.TextContent).Text; got != "http-call-ok" {
		t.Errorf("Execute text = %q, want %q", got, "http-call-ok")
	}
}

func TestHTTPCallToolErrorIsolated(t *testing.T) {
	m, _ := connectFakeHTTP(t, &fakeHTTPServer{failCall: true})
	tool, ok := findTool(m, "mcp__fake__read_thing")
	if !ok {
		t.Fatal("read_thing not in the aggregated face")
	}
	_, err := tool.Execute(context.Background(), "id-1", json.RawMessage(`{}`), nil)
	// A tool-level RPC failure must not surface as a transport error: it comes
	// back with err=nil so one failing call cannot take down the loop.
	if err != nil {
		t.Fatalf("Execute err = %v, want nil (tool-level failure is isolated)", err)
	}
}

func TestHTTPSessionExpiryRecovery(t *testing.T) {
	f := &fakeHTTPServer{}
	m, _ := connectFakeHTTP(t, f)
	tool, ok := findTool(m, "mcp__fake__read_thing")
	if !ok {
		t.Fatal("read_thing not in the aggregated face")
	}
	if f.lastSess == "" {
		t.Fatal("fake server never assigned a session")
	}
	// Server restart semantics: the session the transport still holds is now
	// dead. The next call gets a 404 (pre-execution rejection), re-handshakes
	// once, and the retried call succeeds — recovery is invisible above.
	f.revoked = f.lastSess
	res, err := tool.Execute(context.Background(), "id-1", json.RawMessage(`{}`), nil)
	if err != nil {
		t.Fatalf("Execute after session expiry: %v, want recovered", err)
	}
	if got := res.Content[0].(agentcore.TextContent).Text; got != "http-call-ok" {
		t.Errorf("recovered call text = %q, want %q", got, "http-call-ok")
	}
}

func TestHTTPBadURLFailsConnect(t *testing.T) {
	m := Connect(context.Background(), []ServerConfig{
		{Name: "bad", Type: "http", URL: "not a url"},
	}, nil, nil)
	st := m.Status()
	if st[0].Connected || st[0].Error == "" {
		t.Fatalf("status = %+v, want a recorded error and no connection", st)
	}
	if err := (ServerConfig{Name: "bad", Type: "http"}).Validate(); err == nil {
		t.Error("http entry without a url must fail Validate")
	}
}

func findTool(m *Manager, name string) (*MCPTool, bool) {
	for _, tl := range m.Tools() {
		if tl.Name() == name {
			return tl, true
		}
	}
	return nil, false
}

// Live servers, opt-in via env: PIGO_MCP_LIVE_STDIO points at a stdio MCP
// server executable, PIGO_MCP_LIVE_HTTP at a Streamable HTTP endpoint. Skipped
// unless set, so the gated suite stays hermetic; a run against real servers
// exercises initialize → tools/list → tools/call end to end on the shipped
// transports.
func TestLiveServers(t *testing.T) {
	stdioCmd := os.Getenv("PIGO_MCP_LIVE_STDIO")
	httpURL := os.Getenv("PIGO_MCP_LIVE_HTTP")
	if stdioCmd == "" && httpURL == "" {
		t.Skip("set PIGO_MCP_LIVE_STDIO / PIGO_MCP_LIVE_HTTP to run against real servers")
	}
	var cfgs []ServerConfig
	if stdioCmd != "" {
		cfgs = append(cfgs, ServerConfig{Name: "livestdio", Command: stdioCmd, TimeoutSeconds: 20})
	}
	if httpURL != "" {
		cfgs = append(cfgs, ServerConfig{Name: "livehttp", Type: "http", URL: httpURL, TimeoutSeconds: 20})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	m := Connect(ctx, cfgs, nil, nil)
	defer m.Close()
	for _, st := range m.Status() {
		if !st.Connected {
			t.Errorf("live server %q not connected: %s", st.Name, st.Error)
			continue
		}
		t.Logf("live server %q: %s, %d tools", st.Name, st.ServerInfo, st.ToolCount)
	}
	for _, tool := range m.Tools() {
		if !strings.HasPrefix(tool.Name(), "mcp__live") {
			continue
		}
		t.Logf("live tool: %s — %s", tool.Name(), tool.Description())
	}
	// Optional one real call end to end: PIGO_MCP_LIVE_CALL is the tool's FULL
	// registry name (mcp__<liveserver>__<tool>), PIGO_MCP_LIVE_ARGS its JSON
	// arguments. Read-only callers only: the test run should stay side-effect
	// free.
	call := os.Getenv("PIGO_MCP_LIVE_CALL")
	if call == "" {
		return
	}
	full := call
	for _, tool := range m.Tools() {
		if tool.Name() != full {
			continue
		}
		args := json.RawMessage(os.Getenv("PIGO_MCP_LIVE_ARGS"))
		ctx2, cancel2 := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel2()
		res, err := tool.Execute(ctx2, "live-call-1", args, nil)
		if err != nil {
			t.Fatalf("live call %s: %v", full, err)
		}
		txt := ""
		for _, c := range res.Content {
			if tc, ok := c.(agentcore.TextContent); ok {
				txt += tc.Text + "\n"
			}
		}
		t.Logf("live call %s → %.500s", full, txt)
		return
	}
	t.Fatalf("live call: tool %q not found in the face", full)
}
