package lsp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Server states (Status reporting and tests).
const (
	StateOff      = "off"
	StateStarting = "starting"
	StateReady    = "ready"
	StateError    = "error"
)

// ServerConfig is one language server's launch configuration. Dir is the
// workspace root (the initialize rootUri); Extensions selects the file types
// the server overlays; LanguageID is the didOpen languageId for those files.
// Env, when nil, inherits the parent environment (os/exec default).
type ServerConfig struct {
	Name       string
	Command    string
	Args       []string
	Dir        string
	Extensions []string // without the dot: "go"
	LanguageID string
	Env        []string
}

// Server is one language-server process: the initialize handshake, the
// overlay document store, the collected diagnostics, and the read-only
// query calls the tool family uses. Safe for concurrent use.
type Server struct {
	cfg ServerConfig

	cmd *exec.Cmd
	c   *conn

	mu        sync.Mutex
	state     string
	stopped   bool
	startErr  error
	serverVer string // initializeResult.serverInfo, e.g. "gopls v0.19.1"
	docs      map[string]*overlayDoc
	diags     map[string][]Diagnostic
	// diagGen counts publishes per URI so a diagnostics waiter can detect a
	// fresh publish after a didChange without holding the lock.
	diagGen map[string]int
	// pull capability state: advertised comes from initializeResult, rejected
	// is set after the server answers textDocument/diagnostic with
	// MethodNotFound (grok pull.rs: the advertised capability is not enough —
	// only the server's own rejection writes it off). pullMu skips concurrent
	// pull attempts so a burst of queries spends one round trip, not five.
	pullAdvertised bool
	pullRejected   bool
	pullMu         sync.Mutex
	pullRunning    bool
	sterrMu        sync.Mutex
	stderr         []string // ring of the process's last stderr lines
	stopOnce       sync.Once
	// mailbox serializes overlay notifications so didChange versions reach
	// the wire in the order they were assigned.
	mailbox chan func()
	// done closes when the process exits; startErrAfterExit carries the
	// failure for the next caller.
	done      chan struct{}
	exitErrMu sync.Mutex
	exitErr   error
}

type overlayDoc struct {
	version int
	text    string
}

// NewServer launches the process and runs the initialize handshake. The
// returned server is in StateReady (or StateError with StartError set).
// ctx bounds the handshake only; requests afterwards use their own
// deadlines.
func NewServer(ctx context.Context, cfg ServerConfig) (*Server, error) {
	s := &Server{
		cfg:     cfg,
		state:   StateStarting,
		docs:    map[string]*overlayDoc{},
		diags:   map[string][]Diagnostic{},
		diagGen: map[string]int{},
		mailbox: make(chan func(), 64),
		done:    make(chan struct{}),
	}
	if err := s.launch(); err != nil {
		s.state = StateError
		return s, err
	}
	if err := s.initialize(ctx); err != nil {
		s.state = StateError
		s.startErr = err
		// The handshake failed but the process may still be alive: tear it
		// down so a failed start never leaks a server.
		if s.cmd != nil && s.cmd.Process != nil {
			_ = s.cmd.Process.Kill()
		}
		if s.c != nil {
			s.c.Close()
		}
		return s, err
	}
	s.mu.Lock()
	s.state = StateReady
	s.mu.Unlock()
	go s.mailLoop()
	go func() {
		err := s.cmd.Wait()
		s.exitErrMu.Lock()
		s.exitErr = err
		s.exitErrMu.Unlock()
		close(s.done)
		s.fail(fmt.Errorf("server %s exited: %v", cfg.Name, err))
	}()
	return s, nil
}

func (s *Server) launch() error {
	cmd := exec.Command(s.cfg.Command, s.cfg.Args...)
	cmd.Dir = s.cfg.Dir
	cmd.Env = s.cfg.Env
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("launch %s: %w", s.cfg.Command, err)
	}
	s.cmd = cmd
	go s.pumpStderr(stderr)
	s.c = newConn(stdout, stdin, s.handleServerRequest, s.handleNotification)
	return nil
}

// pumpStderr keeps the last few server stderr lines for error reporting
// (gopls explains flag problems and panics there).
func (s *Server) pumpStderr(r io.Reader) {
	sc := make(chan string, 1)
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := r.Read(buf)
			if n > 0 {
				sc <- string(buf[:n])
			}
			if err != nil {
				close(sc)
				return
			}
		}
	}()
	for chunk, ok := <-sc; ok; chunk, ok = <-sc {
		s.sterrMu.Lock()
		s.stderr = append(s.stderr, chunk)
		if len(s.stderr) > 8 {
			s.stderr = s.stderr[len(s.stderr)-8:]
		}
		s.sterrMu.Unlock()
	}
}

func (s *Server) stderrTail() string {
	s.sterrMu.Lock()
	defer s.sterrMu.Unlock()
	return strings.TrimSpace(strings.Join(s.stderr, ""))
}

// StderrTail is the exported view of the process's last stderr lines — the
// surface and tests use it to explain a failed start.
func (s *Server) StderrTail() string { return s.stderrTail() }

// initialize runs the LSP initialize/initialized handshake.
func (s *Server) initialize(ctx context.Context) error {
	params := map[string]any{
		"processId": os.Getpid(),
		"rootUri":   PathToURI(s.cfg.Dir),
		"workspaceFolders": []map[string]string{
			{"uri": PathToURI(s.cfg.Dir), "name": s.cfg.Name},
		},
		"capabilities": map[string]any{
			// Position encoding: accept the default (UTF-16) explicitly so a
			// server that probes capabilities does not guess.
			"general": map[string]any{"positionEncodings": []string{"utf-16"}},
			"textDocument": map[string]any{
				"publishDiagnostics": map[string]any{"relatedInformation": false},
				"synchronization":    map[string]any{"didSave": false},
				"hover":              map[string]any{"contentFormat": []string{"markdown", "plaintext"}},
				"documentSymbol":     map[string]any{"hierarchicalDocumentSymbolSupport": true},
			},
			"workspace": map[string]any{
				"configuration":          true,
				"workspaceFolders":       true,
				"didChangeConfiguration": map[string]any{"dynamicRegistration": false},
			},
		},
	}
	raw, err := s.c.Call(ctx, "initialize", params)
	if err != nil {
		return fmt.Errorf("initialize: %w", err)
	}
	var res struct {
		ServerInfo *struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"serverInfo"`
		Capabilities *struct {
			DiagnosticProvider json.RawMessage `json:"diagnosticProvider"`
		} `json:"capabilities"`
	}
	json.Unmarshal(raw, &res)
	if res.Capabilities != nil && len(res.Capabilities.DiagnosticProvider) > 0 && string(res.Capabilities.DiagnosticProvider) != "null" {
		s.mu.Lock()
		s.pullAdvertised = true
		s.mu.Unlock()
	}
	if res.ServerInfo != nil {
		ver := strings.TrimSpace(res.ServerInfo.Name + " " + res.ServerInfo.Version)
		// Some builds pack the full build-info JSON into Version (gopls
		// v0.23 observed); the status row needs a label, not a dump.
		if len(ver) > 80 {
			ver = ver[:77] + "..."
		}
		s.mu.Lock()
		s.serverVer = ver
		s.mu.Unlock()
	}
	if err := s.c.Notify("initialized", map[string]any{}); err != nil {
		return fmt.Errorf("initialized: %w", err)
	}
	return nil
}

// handleServerRequest answers the server→client requests this client
// understands; anything else is method-not-found.
func (s *Server) handleServerRequest(method string, params json.RawMessage) (any, error) {
	switch method {
	case "workspace/configuration":
		var req struct {
			Items []struct {
				Section string `json:"section"`
			} `json:"items"`
		}
		json.Unmarshal(params, &req)
		// One empty settings object per item: the server applies its defaults
		// (pigo drives gopls via CLI flags, not workspace settings).
		out := make([]any, len(req.Items))
		for i := range out {
			out[i] = map[string]any{}
		}
		return out, nil
	case "client/registerCapability", "client/unregisterCapability":
		return nil, nil
	case "window/workDoneProgress/create":
		return nil, nil
	default:
		return nil, fmt.Errorf("unsupported server request %q", method)
	}
}

// handleNotification receives server→client notifications; only
// publishDiagnostics carries state this client keeps.
func (s *Server) handleNotification(method string, params json.RawMessage) {
	if method != "textDocument/publishDiagnostics" {
		return
	}
	var p publishDiagnosticsParams
	if err := json.Unmarshal(params, &p); err != nil {
		return
	}
	path := URIToPath(p.URI)
	diags := make([]Diagnostic, 0, len(p.Diagnostics))
	for _, w := range p.Diagnostics {
		diags = append(diags, w.flatten())
	}
	s.mu.Lock()
	s.diags[path] = diags
	s.diagGen[path]++
	s.mu.Unlock()
}

// fail marks the server errored and drops pending calls.
func (s *Server) fail(err error) {
	s.stopOnce.Do(func() {
		if s.c != nil {
			s.c.Close()
		}
	})
	s.mu.Lock()
	s.state = StateError
	if s.startErr == nil {
		s.startErr = err
	}
	s.mu.Unlock()
}

// State reports the current lifecycle state.
func (s *Server) State() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

// StartError reports why the server is in StateError.
func (s *Server) StartError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.startErr
}

// ServerInfo reports the initialize serverInfo string.
func (s *Server) ServerInfo() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.serverVer
}

// ErrNotReady is returned by the query methods when the server has not
// completed (or has lost) its handshake.
var ErrNotReady = errors.New("lsp: server not ready")

// Overlay pushes one file's in-memory content: didOpen for a new file,
// didChange (full replace) for a known one. The version is assigned here so
// ordering is monotonic even though the send rides the mailbox goroutine.
// Non-matching extensions are ignored (per-server file-type selection).
// After Stop the mailbox is drained and unused (no close), so a late Overlay
// from an edit-hook goroutine is a no-op rather than a send-on-closed panic.
func (s *Server) Overlay(path, text string) {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	if !s.handles(path) {
		s.mu.Unlock()
		return
	}
	doc, ok := s.docs[path]
	if !ok {
		doc = &overlayDoc{version: 1}
		s.docs[path] = doc
	}
	doc.version++
	doc.text = text
	version := doc.version
	s.mu.Unlock()
	uri := PathToURI(path)
	lang := languageIDFor(path, s.cfg.LanguageID)
	s.mailbox <- func() {
		if !ok {
			s.c.Notify("textDocument/didOpen", map[string]any{
				"textDocument": map[string]any{
					"uri": uri, "languageId": lang, "version": version, "text": text,
				},
			})
			return
		}
		s.c.Notify("textDocument/didChange", map[string]any{
			"textDocument":   map[string]any{"uri": uri, "version": version},
			"contentChanges": []map[string]string{{"text": text}},
		})
	}
}

// handles reports whether path is one of this server's file types.
func (s *Server) handles(path string) bool {
	if len(s.cfg.Extensions) == 0 {
		return true
	}
	dot := strings.LastIndexByte(path, '.')
	if dot < 0 {
		return false
	}
	ext := strings.ToLower(path[dot+1:])
	for _, e := range s.cfg.Extensions {
		if strings.EqualFold(e, ext) {
			return true
		}
	}
	return false
}

// languageIDFor maps a path's extension to the didOpen languageId. Mod/work
// files are first-class gopls documents (modfile diagnostics); the fallback
// covers plain text files.
func languageIDFor(path, fallback string) string {
	switch strings.ToLower(strings.TrimPrefix(filepath.Ext(path), ".")) {
	case "mod":
		return "go.mod"
	case "work":
		return "go.work"
	default:
		return fallback
	}
}

// overlaySnapshot copies the overlay document store (path → text) — the
// manager harvests it before dropping a server so a rebuild (crash, idle
// reclaim, disable) can re-push state the new process would otherwise never
// learn (grok replay_tracked_documents, harvested from memory rather than
// re-read from disk).
func (s *Server) overlaySnapshot() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]string, len(s.docs))
	for p, d := range s.docs {
		out[p] = d.text
	}
	return out
}

func (s *Server) mailLoop() {
	for fn := range s.mailbox {
		fn()
	}
}

// Diagnostics returns the collected diagnostics for path (empty = every
// file). When wait is > 0 and path has never received a publish since start
// (or since the last Overlay), it polls until a fresh generation arrives or
// the wait elapses — the tool face uses this so a query right after an edit
// sees the just-pushed state instead of the empty pre-publish snapshot.
// A wait that found nothing fresh falls back to one textDocument/diagnostic
// pull for push-silent servers (grok pull.rs; gopls pushes, so against it the
// fallback costs one rejected round trip, ever). Workspace-wide queries
// (path == "") only sleep: the pull method is per-document.
func (s *Server) Diagnostics(path string, wait time.Duration) map[string][]Diagnostic {
	if path != "" {
		if !s.awaitDiagnostics(path, wait) {
			s.pullDiagnostics(context.Background(), path, wait)
		}
	} else if wait > 0 {
		time.Sleep(wait) // workspace-wide query: give in-flight publishes a beat
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string][]Diagnostic{}
	if path != "" {
		if d, ok := s.diags[path]; ok {
			out[path] = d
		}
		return out
	}
	for p, d := range s.diags {
		if len(d) > 0 {
			out[p] = d
		}
	}
	return out
}

// awaitDiagnostics polls until path's diagnostic generation moves past the
// baseline captured at entry (a publish after this moment), or the wait
// elapses. It reports whether a fresh publish landed. A server that never
// publishes for a clean file leaves the baseline unmoved — the wait bounds
// the tool call, the empty result is the honest answer.
func (s *Server) awaitDiagnostics(path string, wait time.Duration) bool {
	if wait <= 0 {
		return false
	}
	deadline := time.Now().Add(wait)
	s.mu.Lock()
	base := s.diagGen[path]
	s.mu.Unlock()
	for time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		s.mu.Lock()
		moved := s.diagGen[path] > base
		s.mu.Unlock()
		if moved {
			return true
		}
	}
	return false
}

// pullDiagnostics issues one textDocument/diagnostic round trip and merges
// the report into the store as a fresh generation (so repeated queries
// answer from the store until the next push/wait cycle). Skipped when the
// server already rejected the method (MethodNotFound is the only conclusive
// write-off — grok pull.rs asks even unadvertised capability once) or when a
// pull is already in flight; ctx carries the response deadline only.
func (s *Server) pullDiagnostics(ctx context.Context, path string, wait time.Duration) {
	s.mu.Lock()
	rejected := s.pullRejected
	s.mu.Unlock()
	if rejected {
		return
	}
	s.pullMu.Lock()
	if s.pullRunning {
		s.pullMu.Unlock()
		return
	}
	s.pullRunning = true
	s.pullMu.Unlock()
	defer func() {
		s.pullMu.Lock()
		s.pullRunning = false
		s.pullMu.Unlock()
	}()

	if wait <= 0 {
		wait = DiagnosticsWait
	}
	pctx, cancel := context.WithTimeout(ctx, wait+2*time.Second)
	defer cancel()
	raw, err := s.c.Call(pctx, "textDocument/diagnostic", map[string]any{
		"textDocument": map[string]any{"uri": PathToURI(path)},
	})
	if err != nil {
		var re *rpcError
		if errors.As(err, &re) && re.Code == codeMethodNotFound {
			s.mu.Lock()
			s.pullRejected = true
			s.mu.Unlock()
		}
		return
	}
	diags, ok := decodeDocumentDiagnosticReport(raw)
	if !ok {
		return
	}
	s.mu.Lock()
	s.diags[path] = diags
	s.diagGen[path]++
	s.mu.Unlock()
}

// DiagnosticsCount returns the total diagnostic count across all files.
func (s *Server) DiagnosticsCount() (files, total int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range s.diags {
		if len(d) > 0 {
			files++
			total += len(d)
		}
	}
	return files, total
}

// position converts a 1-based line and a query substring into the wire
// (line, character) pair. The column is the UTF-16 offset of the first
// occurrence of query in that line (0 when query is empty or absent). The
// line text comes from the overlay when the file is open (the agent's edit
// may not be on disk yet) and from disk otherwise.
func (s *Server) position(path string, line int, query string) (map[string]any, error) {
	char := 0
	if query != "" {
		var lines []string
		s.mu.Lock()
		if doc, ok := s.docs[path]; ok {
			lines = strings.Split(doc.text, "\n")
		}
		s.mu.Unlock()
		if lines == nil {
			text, err := os.ReadFile(path)
			if err != nil {
				return nil, err
			}
			lines = strings.Split(string(text), "\n")
		}
		if line < 1 || line > len(lines) {
			return nil, fmt.Errorf("line %d out of range (%s has %d lines)", line, path, len(lines))
		}
		l := lines[line-1]
		idx := strings.Index(l, query)
		if idx < 0 {
			return nil, fmt.Errorf("query %q not found on line %d of %s", query, line, path)
		}
		char = utf16Col(l, idx)
	}
	return map[string]any{"line": line - 1, "character": char}, nil
}

// utf16Col converts a byte offset within line into UTF-16 code units.
func utf16Col(line string, byteIdx int) int {
	col := 0
	for _, r := range line[:byteIdx] {
		if r > 0xFFFF {
			col += 2
		} else {
			col++
		}
	}
	return col
}

// Definition resolves the symbol at (1-based) line, optionally locating the
// column via query.
func (s *Server) Definition(ctx context.Context, path string, line int, query string) ([]Location, error) {
	pos, err := s.position(path, line, query)
	if err != nil {
		return nil, err
	}
	return s.locations(ctx, "textDocument/definition", map[string]any{
		"textDocument": map[string]any{"uri": PathToURI(path)},
		"position":     pos,
	})
}

// Rename asks the server to rename the symbol at (1-based) line and returns
// the workspace edit flattened to path → edits (B5). The server computes the
// full reference set — including cross-package hits text search cannot see;
// applying the edits is the caller's (the tool's) side-effectful half.
func (s *Server) Rename(ctx context.Context, path string, line int, newName, query string) (map[string][]TextEdit, error) {
	if strings.TrimSpace(newName) == "" {
		return nil, fmt.Errorf("rename: new name is empty")
	}
	pos, err := s.position(path, line, query)
	if err != nil {
		return nil, err
	}
	raw, err := s.c.Call(ctx, "textDocument/rename", map[string]any{
		"textDocument": map[string]any{"uri": PathToURI(path)},
		"position":     pos,
		"newName":      newName,
	})
	if err != nil {
		return nil, err
	}
	var we workspaceEdit
	if err := json.Unmarshal(raw, &we); err != nil {
		return nil, fmt.Errorf("rename: decode workspace edit: %w", err)
	}
	edits := we.EditsByFile()
	if len(edits) == 0 {
		// null result = the position is not renameable (server-reported via
		// an empty edit set rather than an error).
		return nil, fmt.Errorf("rename: the server returned no edits for this position (not a renameable symbol?)")
	}
	return edits, nil
}

// References returns the references to the symbol at the position.
func (s *Server) References(ctx context.Context, path string, line int, query string, includeDeclaration bool) ([]Location, error) {
	pos, err := s.position(path, line, query)
	if err != nil {
		return nil, err
	}
	return s.locations(ctx, "textDocument/references", map[string]any{
		"textDocument": map[string]any{"uri": PathToURI(path)},
		"position":     pos,
		"context":      map[string]any{"includeDeclaration": includeDeclaration},
	})
}

func (s *Server) locations(ctx context.Context, method string, params any) ([]Location, error) {
	raw, err := s.c.Call(ctx, method, params)
	if err != nil {
		return nil, err
	}
	// Response is Location[] or a single Location (or null).
	var list []wireLocation
	if err := json.Unmarshal(raw, &list); err == nil && list != nil {
		return flattenLocations(list), nil
	}
	var one wireLocation
	if err := json.Unmarshal(raw, &one); err == nil && one.URI != "" {
		return flattenLocations([]wireLocation{one}), nil
	}
	return nil, nil
}

func flattenLocations(list []wireLocation) []Location {
	out := make([]Location, 0, len(list))
	for _, l := range list {
		out = append(out, Location{Path: URIToPath(l.URI), Line: l.Range.Start.Line, Character: l.Range.Start.Character})
	}
	return out
}

// Hover returns the hover text at the position ("" when none).
func (s *Server) Hover(ctx context.Context, path string, line int, query string) (Hover, error) {
	pos, err := s.position(path, line, query)
	if err != nil {
		return Hover{}, err
	}
	raw, err := s.c.Call(ctx, "textDocument/hover", map[string]any{
		"textDocument": map[string]any{"uri": PathToURI(path)},
		"position":     pos,
	})
	if err != nil {
		return Hover{}, err
	}
	h := decodeHover(raw)
	h.Line = pos["line"].(int)
	h.Character = pos["character"].(int)
	return h, nil
}

// Symbols returns the document's symbol tree.
func (s *Server) Symbols(ctx context.Context, path string) ([]Symbol, error) {
	raw, err := s.c.Call(ctx, "textDocument/documentSymbol", map[string]any{
		"textDocument": map[string]any{"uri": PathToURI(path)},
	})
	if err != nil {
		return nil, err
	}
	return decodeSymbols(raw), nil
}

// WorkspaceSymbols queries the project-wide symbol index (workspace/symbol):
// the retrieval face for "where does this live" across the whole module.
func (s *Server) WorkspaceSymbols(ctx context.Context, query string) ([]WorkspaceSymbol, error) {
	raw, err := s.c.Call(ctx, "workspace/symbol", map[string]any{"query": query})
	if err != nil {
		return nil, err
	}
	return decodeWorkspaceSymbols(raw), nil
}

// Implementations resolves the implementations of the symbol at (1-based)
// line, optionally locating the column via query.
func (s *Server) Implementations(ctx context.Context, path string, line int, query string) ([]Location, error) {
	pos, err := s.position(path, line, query)
	if err != nil {
		return nil, err
	}
	return s.locations(ctx, "textDocument/implementation", map[string]any{
		"textDocument": map[string]any{"uri": PathToURI(path)},
		"position":     pos,
	})
}

// Stop shuts the process down (shutdown/exit, then kill on a 3s grace).
func (s *Server) Stop() {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	s.stopped = true
	s.mu.Unlock()
	s.stopOnce.Do(func() {
		if s.c != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_, _ = s.c.Call(ctx, "shutdown", nil)
			_ = s.c.Notify("exit", nil)
			cancel()
			s.c.Close()
		}
		if s.cmd != nil && s.cmd.Process != nil {
			select {
			case <-s.done:
			case <-time.After(3 * time.Second):
				_ = s.cmd.Process.Kill()
			}
		}
	})
	s.mu.Lock()
	if s.state != StateError {
		s.state = StateOff
	}
	s.mu.Unlock()
}
