package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// The fake language server runs as a helper process of this test binary
// (GO_LSP_FAKE_SERVER=1): it answers initialize, replies to didOpen/didChange
// with a canned publishDiagnostics, and serves one definition. This exercises
// the real exec launch path, the Content-Length framing both ways, the
// handshake and the overlay/diagnostics store end to end.

const fakeServerEnv = "GO_LSP_FAKE_SERVER"

func TestMain(m *testing.M) {
	if os.Getenv(fakeServerEnv) == "1" {
		runFakeServer()
		return
	}
	os.Exit(m.Run())
}

// framedRead reads one Content-Length framed message.
func framedRead(br *bufio.Reader) ([]byte, error) {
	length := -1
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		if k, v, ok := strings.Cut(line, ":"); ok && strings.EqualFold(strings.TrimSpace(k), "Content-Length") {
			n, err := strconv.Atoi(strings.TrimSpace(v))
			if err != nil {
				return nil, err
			}
			length = n
		}
	}
	if length < 0 {
		return nil, fmt.Errorf("no Content-Length")
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(br, body); err != nil {
		return nil, err
	}
	return body, nil
}

func framedWrite(w io.Writer, msg any) {
	body, _ := json.Marshal(msg)
	fmt.Fprintf(w, "Content-Length: %d\r\n\r\n", len(body))
	w.Write(body)
}

// fakeMsg is the wire message shape the fake server decodes.
type fakeMsg struct {
	ID     *int64          `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

func runFakeServer() {
	br := bufio.NewReader(os.Stdin)
	initialized := false
	for {
		body, err := framedRead(br)
		if err != nil {
			return
		}
		var msg struct {
			ID     *int64          `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Result json.RawMessage `json:"result"`
		}
		if err := json.Unmarshal(body, &msg); err != nil {
			continue
		}
		switch {
		case msg.ID != nil && msg.Method == "initialize":
			framedWrite(os.Stdout, map[string]any{
				"jsonrpc": "2.0", "id": *msg.ID,
				"result": map[string]any{
					"capabilities": map[string]any{},
					"serverInfo":   map[string]any{"name": "fake-lsp", "version": "0.0.1"},
				},
			})
		case msg.ID != nil && msg.Method == "shutdown":
			framedWrite(os.Stdout, map[string]any{"jsonrpc": "2.0", "id": *msg.ID, "result": nil})
		case msg.Method == "initialized":
			initialized = true
		case msg.Method == "textDocument/didOpen" || msg.Method == "textDocument/didChange":
			if !initialized {
				continue
			}
			var p struct {
				TextDocument struct {
					URI string `json:"uri"`
				} `json:"textDocument"`
			}
			json.Unmarshal(msg.Params, &p)
			framedWrite(os.Stdout, map[string]any{
				"jsonrpc": "2.0", "method": "textDocument/publishDiagnostics",
				"params": map[string]any{
					"uri": p.TextDocument.URI,
					"diagnostics": []map[string]any{
						{
							"range":    map[string]any{"start": map[string]any{"line": 4, "character": 2}, "end": map[string]any{"line": 4, "character": 9}},
							"severity": 1,
							"source":   "compile",
							"code":     "Undefined",
							"message":  "undefined: Foo",
						},
					},
				},
			})
		case msg.Method == "textDocument/definition":
			var p struct {
				TextDocument struct {
					URI string `json:"uri"`
				} `json:"textDocument"`
			}
			json.Unmarshal(msg.Params, &p)
			framedWrite(os.Stdout, map[string]any{
				"jsonrpc": "2.0", "id": *msg.ID,
				"result": []map[string]any{
					{"uri": p.TextDocument.URI, "range": map[string]any{"start": map[string]any{"line": 9, "character": 5}, "end": map[string]any{"line": 9, "character": 8}}},
				},
			})
		case msg.ID != nil:
			framedWrite(os.Stdout, map[string]any{"jsonrpc": "2.0", "id": *msg.ID, "error": map[string]any{"code": -32601, "message": "unknown"}})
		case msg.Method == "exit":
			return
		}
	}
}

// fakeServerConfig launches this test binary in fake-server mode.
func fakeServerConfig(t *testing.T, dir string) ServerConfig {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Skipf("no test executable: %v", err)
	}
	return ServerConfig{
		Name:       "fake",
		Command:    exe,
		Args:       []string{"-test.run=TestFakeServerEntry"},
		Dir:        dir,
		Extensions: []string{"go"},
		LanguageID: "go",
		Env:        append(os.Environ(), fakeServerEnv+"=1", "GO_LSP_FAKE_SERVER=1"),
	}
}

// TestFakeServerEntry is never run as a test: the helper process re-executes
// this binary with -test.run matching this name and the fake-server env set,
// so TestMain routes it to runFakeServer before any test runs.
func TestFakeServerEntry(t *testing.T) {}

func newFakeServer(t *testing.T) *Server {
	t.Helper()
	cfg := fakeServerConfig(t, t.TempDir())
	s, err := NewServer(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewServer: %v (stderr: %s)", err, s.StderrTail())
	}
	t.Cleanup(s.Stop)
	return s
}

func TestServerHandshakeAndOverlay(t *testing.T) {
	s := newFakeServer(t)
	if got := s.State(); got != StateReady {
		t.Fatalf("state = %q", got)
	}
	if info := s.ServerInfo(); info == "" {
		t.Fatal("no serverInfo captured")
	}
	// Overlay a file: didOpen triggers the fake server's publishDiagnostics.
	dir := t.TempDir()
	path := filepath.Join(dir, "a.go")
	s.Overlay(path, "package a\n\nfunc F() {}\n")
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := s.Diagnostics("", 0)[path]; ok {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	diags := s.Diagnostics(path, 2*time.Second)
	list, ok := diags[path]
	if !ok || len(list) != 1 {
		t.Fatalf("diagnostics for %v: %v", path, diags)
	}
	d := list[0]
	if d.Severity != 1 || d.Message != "undefined: Foo" || d.Source != "compile" || d.Code != "Undefined" {
		t.Fatalf("diagnostic = %+v", d)
	}
	if d.Line != 4 || d.Character != 2 {
		t.Fatalf("range start = %d:%d", d.Line, d.Character)
	}
}

func TestServerOverlayNonMatchingExtensionIgnored(t *testing.T) {
	s := newFakeServer(t)
	s.Overlay("notes.txt", "hello")
	// No crash, no publish; the diagnostics store stays empty.
	if diags := s.Diagnostics("", 200*time.Millisecond); len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
}

func TestServerDefinition(t *testing.T) {
	s := newFakeServer(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "a.go")
	// position() prefers the overlay text when the file is open; overlay a
	// body where line 2 carries the query symbol.
	s.Overlay(path, "package a\n\nvar target = 1\n")
	locs, err := s.Definition(context.Background(), path, 3, "target")
	if err != nil {
		t.Fatalf("definition: %v", err)
	}
	if len(locs) != 1 {
		t.Fatalf("locations = %v", locs)
	}
	if locs[0].Path != path || locs[0].Line != 9 || locs[0].Character != 5 {
		t.Fatalf("location = %+v", locs[0])
	}
}

func TestServerQueryNotFoundOnLine(t *testing.T) {
	s := newFakeServer(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "a.go")
	s.Overlay(path, "package a\n\nvar other = 1\n")
	if _, err := s.Definition(context.Background(), path, 3, "missing"); err == nil {
		t.Fatal("expected an error for a query absent from the line")
	}
}

func TestServerStopTwiceSafe(t *testing.T) {
	s := newFakeServer(t)
	s.Stop()
	s.Stop() // idempotent
}

func TestConnCallAfterClose(t *testing.T) {
	s := newFakeServer(t)
	c := s.c
	c.Close()
	if _, err := c.Call(context.Background(), "initialize", nil); err == nil {
		t.Fatal("expected ErrConnClosed after Close")
	}
}

// --- conn-level unit tests over a socketpair-like pipe ---

type pipePair struct {
	client io.ReadWriteCloser
	server io.ReadWriteCloser
}

func newPipePair() pipePair {
	// os.Pipe gives real blocking reads; the fake server side runs in a
	// goroutine of this test.
	r1, w1, _ := os.Pipe()
	r2, w2, _ := os.Pipe()
	return pipePair{client: &pipeEnd{r: r1, w: w2}, server: &pipeEnd{r: r2, w: w1}}
}

type pipeEnd struct {
	r *os.File
	w *os.File
}

func (p *pipeEnd) Read(b []byte) (int, error)  { return p.r.Read(b) }
func (p *pipeEnd) Write(b []byte) (int, error) { return p.w.Write(b) }
func (p *pipeEnd) Close() error {
	p.r.Close()
	p.w.Close()
	return nil
}

func TestConnFramingAndDispatch(t *testing.T) {
	pair := newPipePair()
	var mu sync.Mutex
	var notifications []string
	answered := make(chan string, 1)

	c := newConn(pair.client, pair.client,
		func(method string, params json.RawMessage) (any, error) {
			answered <- method
			return map[string]string{"ok": "yes"}, nil
		},
		func(method string, params json.RawMessage) {
			mu.Lock()
			notifications = append(notifications, method)
			mu.Unlock()
		})
	defer c.Close()

	// Server→client request is answered by onRequest.
	framedWrite(pair.server, map[string]any{"jsonrpc": "2.0", "id": 7, "method": "workspace/configuration", "params": map[string]any{}})
	select {
	case m := <-answered:
		if m != "workspace/configuration" {
			t.Fatalf("answered %q", m)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server request not answered")
	}

	// Notification dispatches to onNotify.
	framedWrite(pair.server, map[string]any{"jsonrpc": "2.0", "method": "textDocument/publishDiagnostics", "params": map[string]any{}})
	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		n := len(notifications)
		mu.Unlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("notification not dispatched")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// A Call gets its response.
	resCh := make(chan json.RawMessage, 1)
	errCh := make(chan error, 1)
	go func() {
		raw, err := c.Call(context.Background(), "textDocument/hover", map[string]any{})
		if err != nil {
			errCh <- err
			return
		}
		resCh <- raw
	}()
	var req struct {
		ID     int64           `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	br := bufio.NewReader(pair.server)
	for {
		body, err := framedRead(br)
		if err != nil {
			t.Fatalf("read request: %v", err)
		}
		json.Unmarshal(body, &req)
		if req.Method == "textDocument/hover" {
			break
		}
	}
	framedWrite(pair.server, map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"contents": "hi"}})
	select {
	case raw := <-resCh:
		var out map[string]any
		json.Unmarshal(raw, &out)
		if out["contents"] != "hi" {
			t.Fatalf("result = %v", out)
		}
	case err := <-errCh:
		t.Fatalf("call: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("call timed out")
	}
}

func TestConnErrorResponse(t *testing.T) {
	pair := newPipePair()
	c := newConn(pair.client, pair.client, nil, nil)
	defer c.Close()
	errCh := make(chan error, 1)
	go func() {
		_, err := c.Call(context.Background(), "nope", nil)
		errCh <- err
	}()
	body, err := framedRead(bufio.NewReader(pair.server))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var req struct {
		ID int64 `json:"id"`
	}
	json.Unmarshal(body, &req)
	framedWrite(pair.server, map[string]any{"jsonrpc": "2.0", "id": req.ID, "error": map[string]any{"code": -32601, "message": "method not found"}})
	select {
	case err := <-errCh:
		if err == nil || !strings.Contains(err.Error(), "-32601") {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out")
	}
}

func TestUTF16Col(t *testing.T) {
	// ASCII: byte offset == code units.
	if got := utf16Col("func F() {}", 5); got != 5 {
		t.Fatalf("ascii col = %d", got)
	}
	// A non-BMP rune before the offset costs two UTF-16 units.
	line := "a\U0001F600b(c)"
	// byte offset of 'c': 1 (a) + 4 (emoji) + 1 (b) = 6
	if got := utf16Col(line, 6); got != 4 { // a(1) + emoji(2) + b(1)
		t.Fatalf("astral col = %d", got)
	}
}

func TestDiagnosticsCount(t *testing.T) {
	s := newFakeServer(t)
	dir := t.TempDir()
	p1 := filepath.Join(dir, "a.go")
	p2 := filepath.Join(dir, "b.go")
	s.Overlay(p1, "package a\n")
	s.Overlay(p2, "package b\n")
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if files, total := s.DiagnosticsCount(); files >= 2 {
			if total < 2 {
				t.Fatalf("files=%d total=%d", files, total)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("diagnostics never arrived")
}
