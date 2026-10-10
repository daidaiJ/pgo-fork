package lsp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Default gopls launch parameters (T8.2 ②). -remote=auto connects to (or
// starts) the shared gopls daemon, so pigo sessions reuse the warmed cache
// across sessions; serve speaks LSP over stdio. A gopls too old to know
// -remote degrades on start (classifyStartFailure).
var (
	DefaultCommand    = "gopls"
	DefaultGoplsArgs  = []string{"-remote=auto", "serve"}
	DefaultIdlePeriod = 10 * time.Minute
)

// DiagnosticsWait is how long a diagnostics query waits for a fresh
// publish after an overlay push before answering with what it has.
const DiagnosticsWait = 2500 * time.Millisecond

// EditDiagnosticsWait is the shorter budget the edit/write tools use to
// attach a diagnostics report to their result: long enough for the common
// publish-after-change (grok's drain is 500ms, opencode waits up to 5s —
// this sits between), short enough that a burst of edits does not crawl.
const EditDiagnosticsWait = 1500 * time.Millisecond

// InitializeTimeout bounds the handshake; big repositories can take a while,
// and the daemon path makes the wait a one-off per machine.
const InitializeTimeout = 90 * time.Second

// Settings is the resolved LSP configuration (config layering done by the
// caller — internal/cli/run merges config.toml [lsp], the project
// ./.pigo/config.json switch and the PIGO_LSP env var).
type Settings struct {
	Enabled     bool
	Command     string
	Args        []string
	Idle        time.Duration
	ToolFilter  []string // bare tool names ("diagnostics"); empty = all
	Extensions  []string // file types the server overlays; empty = "go"
	LanguageID  string   // didOpen languageId; empty = "go"
	Prewarm     bool     // prewarm the server in the background at startup
	IdleReclaim bool     // stop the server after Settings.Idle unused
	AutoInstall bool     // install the default command via `go install` when it is missing (config default true; zero-value constructors stay off)
}

// ErrDisabled is returned by the tool face when LSP is switched off.
var ErrDisabled = errors.New("lsp: disabled (enable via config [lsp] or the project .pigo/config.json switch)")

// Manager owns the workspace's language server: lazy start, the prewarm
// goroutine, idle reclaim, and the enable/disable switch the /lsp surface
// flips. All methods are safe for concurrent use.
type Manager struct {
	dir string
	st  Settings

	mu sync.Mutex
	// startMu serializes start attempts so two concurrent first uses cannot
	// both spawn a server (the loser would leak a process).
	startMu sync.Mutex
	srv     *Server
	on      bool // runtime switch (config enabled && not live-disabled)
	janit   time.Time
	close   bool
	// pendingOverlays carries overlay docs harvested from a dropped server
	// (crash, idle reclaim, disable) until the next successful start replays
	// them — restart state restoration so queries after a rebuild still see
	// the edited files.
	pendingOverlays map[string]string
	// lastStartErr is the most recent start failure (missing command, failed
	// auto-install, failed handshake). Status reports it so /lsp can explain
	// an [off] server; the next successful start clears it.
	lastStartErr error
}

// NewManager builds the manager over dir. enabled=false managers are inert
// (every call fails with ErrDisabled) — building one for a disabled run keeps
// the /lsp surface able to explain the state.
func NewManager(st Settings, dir string) *Manager {
	if st.Command == "" {
		st.Command = DefaultCommand
	}
	if len(st.Args) == 0 {
		st.Args = append([]string{}, DefaultGoplsArgs...)
	}
	if st.Idle == 0 {
		st.Idle = DefaultIdlePeriod
	}
	if len(st.Extensions) == 0 {
		// go.mod / go.work are first-class gopls documents (modfile
		// diagnostics follow an edit); go.sum stays watcher-only.
		st.Extensions = []string{"go", "mod", "work"}
	}
	if st.LanguageID == "" {
		st.LanguageID = "go"
	}
	m := &Manager{dir: dir, st: st, on: st.Enabled}
	if st.IdleReclaim {
		go m.janitor()
	}
	return m
}

// Enabled reports the live switch.
func (m *Manager) Enabled() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.on
}

// Root is the workspace root the manager was built over (the tool family
// resolves paths against it).
func (m *Manager) Root() string { return m.dir }

// SetEnabled flips the live switch (/lsp enable|disable). enable=true starts
// the server in the background; disable stops it. The persisted switch is
// the caller's job (project config write).
func (m *Manager) SetEnabled(on bool) {
	m.mu.Lock()
	m.on = on
	srv := m.srv
	m.mu.Unlock()
	if !on {
		if srv != nil {
			srv.Stop()
		}
		return
	}
	go func() { _, _ = m.ensureStarted() }()
}

// janitor stops the server after Settings.Idle without any use.
func (m *Manager) janitor() {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for range t.C {
		m.mu.Lock()
		if m.close || !m.on || m.srv == nil {
			m.mu.Unlock()
			if m.close {
				return
			}
			continue
		}
		idle := time.Since(m.janit)
		m.mu.Unlock()
		if idle > m.st.Idle {
			m.Stop()
		}
	}
}

// touch records a use for the idle reclaim.
func (m *Manager) touch() {
	m.mu.Lock()
	m.janit = time.Now()
	m.mu.Unlock()
}

// ensureStarted returns the live server, starting it when needed. The
// handshake failure is degraded once: gopls builds that predate the
// -remote flag fail with a flag error on stderr — retry with the bare
// command before surfacing the error.
func (m *Manager) ensureStarted() (*Server, error) {
	m.startMu.Lock()
	defer m.startMu.Unlock()

	m.mu.Lock()
	if m.srv != nil {
		srv, state := m.srv, m.srv.State()
		if state == StateReady || state == StateStarting {
			m.mu.Unlock()
			return srv, nil
		}
		// Errored or exited: harvest what the dead process still knew before
		// dropping it, so the rebuild below can replay the overlay state.
		m.pendingOverlays = srv.overlaySnapshot()
		m.srv = nil // errored: rebuild
	}
	m.mu.Unlock()

	srv, err := m.start()
	if err != nil {
		m.mu.Lock()
		m.lastStartErr = err
		m.mu.Unlock()
		return nil, err
	}
	m.mu.Lock()
	m.srv = srv
	m.lastStartErr = nil
	m.mu.Unlock()
	return srv, nil
}

func (m *Manager) start() (*Server, error) {
	command := m.st.Command
	if _, err := exec.LookPath(command); err != nil {
		if !m.st.AutoInstall {
			return nil, fmt.Errorf("lsp: %s not found in PATH — install it (go install golang.org/x/tools/gopls@latest) or point [lsp.gopls] command at it", command)
		}
		installed, ierr := autoInstall(command)
		if ierr != nil {
			return nil, fmt.Errorf("lsp: %s not found in PATH and auto-install failed: %w", command, ierr)
		}
		command = installed
	}
	cfg := ServerConfig{
		Name:       m.st.Command,
		Command:    command,
		Args:       m.st.Args,
		Dir:        m.dir,
		Extensions: m.st.Extensions,
		LanguageID: m.st.LanguageID,
	}
	ctx, cancel := context.WithTimeout(context.Background(), InitializeTimeout)
	defer cancel()
	srv, err := NewServer(ctx, cfg)
	if err == nil {
		m.replayOverlays(srv)
		return srv, nil
	}
	// Degrade: a gopls without -remote dies with "flag provided but not
	// defined" before the handshake; retry with the default arguments.
	if classifyStartFailure(err, srv) {
		cfg.Args = []string{"serve"}
		srv2, err2 := NewServer(ctx, cfg)
		if err2 == nil {
			m.replayOverlays(srv2)
			return srv2, nil
		}
		return srv2, err2
	}
	return srv, err
}

// replayOverlays re-pushes harvested overlay docs into a freshly started
// server (restart state restoration). Files deleted since the overlay was
// captured are skipped — a ghost document would fight the on-disk truth.
func (m *Manager) replayOverlays(srv *Server) {
	m.mu.Lock()
	pending := m.pendingOverlays
	m.pendingOverlays = nil
	m.mu.Unlock()
	for path, text := range pending {
		if _, err := os.Stat(path); err != nil {
			continue
		}
		srv.Overlay(path, text)
	}
}

// classifyStartFailure reports whether the start failure looks like an
// unknown-flag rejection (the degradation signal). The partial server is
// stopped when the decision is positive so its process never leaks.
func classifyStartFailure(err error, srv *Server) bool {
	if srv == nil {
		return false
	}
	tail := srv.stderrTail() + " " + err.Error()
	if !strings.Contains(tail, "flag provided but not defined") &&
		!strings.Contains(tail, "unknown flag") &&
		!strings.Contains(tail, "flag needs an argument") {
		return false
	}
	srv.Stop()
	return true
}

// waitReady blocks until the server's state leaves starting.
func (s *Server) waitReady() error {
	deadline := time.Now().Add(InitializeTimeout)
	for time.Now().Before(deadline) {
		switch s.State() {
		case StateReady:
			return nil
		case StateError:
			return s.StartError()
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("lsp: server %s did not become ready", s.cfg.Name)
}

// Prewarm starts the server in the background when the workspace looks like
// a Go module or a Go tree (the non-Go idle guard: a repository without
// go.mod or any .go file never spawns gopls from the prewarm path — an
// explicit tool call always may). Errors are dropped: the first real use
// re-tries and surfaces.
func (m *Manager) Prewarm() {
	if !m.Enabled() || !looksLikeGo(m.dir) {
		return
	}
	go func() { _, _ = m.ensureStarted() }()
}

func looksLikeGo(dir string) bool {
	if fileExists(filepath.Join(dir, "go.mod")) || fileExists(filepath.Join(dir, "go.sum")) {
		return true
	}
	if any, _ := filepath.Glob(filepath.Join(dir, "*.go")); len(any) > 0 {
		return true
	}
	one, _ := filepath.Glob(filepath.Join(dir, "*", "*.go"))
	return len(one) > 0
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

// Overlay pushes an edited file's content to the server (the edit/write tool
// hook). Disabled managers and stopped servers are no-ops. It never blocks
// the caller on the handshake: the overlay rides the server's own mailbox
// once the handshake completes.
func (m *Manager) Overlay(path, content string) {
	m.mu.Lock()
	on, srv := m.on, m.srv
	m.mu.Unlock()
	if !on || srv == nil {
		// Not started yet: start lazily in the background, then overlay. The
		// start is best-effort — a failed start leaves the edit tool clean.
		if !on {
			return
		}
		srv, _ = m.ensureStarted()
		if srv == nil {
			return
		}
	}
	m.touch()
	srv.Overlay(path, content)
}

// Diagnostics returns the server's diagnostics for path ("" = all files),
// waiting up to DiagnosticsWait for a fresh publish.
func (m *Manager) Diagnostics(path string) (map[string][]Diagnostic, error) {
	return m.DiagnosticsWithWait(path, DiagnosticsWait)
}

// DiagnosticsWithWait is Diagnostics with an explicit wait budget.
func (m *Manager) DiagnosticsWithWait(path string, wait time.Duration) (map[string][]Diagnostic, error) {
	srv, err := m.ready()
	if err != nil {
		return nil, err
	}
	m.touch()
	return srv.Diagnostics(path, wait), nil
}

// Definition, References, Hover, Symbols are the query passthroughs.
func (m *Manager) Definition(ctx context.Context, path string, line int, query string) ([]Location, error) {
	srv, err := m.ready()
	if err != nil {
		return nil, err
	}
	m.touch()
	return srv.Definition(ctx, path, line, query)
}

func (m *Manager) References(ctx context.Context, path string, line int, query string, includeDecl bool) ([]Location, error) {
	srv, err := m.ready()
	if err != nil {
		return nil, err
	}
	m.touch()
	return srv.References(ctx, path, line, query, includeDecl)
}

func (m *Manager) Hover(ctx context.Context, path string, line int, query string) (Hover, error) {
	srv, err := m.ready()
	if err != nil {
		return Hover{}, err
	}
	m.touch()
	return srv.Hover(ctx, path, line, query)
}

func (m *Manager) Symbols(ctx context.Context, path string) ([]Symbol, error) {
	srv, err := m.ready()
	if err != nil {
		return nil, err
	}
	m.touch()
	return srv.Symbols(ctx, path)
}

// WorkspaceSymbols queries the project-wide symbol index.
func (m *Manager) WorkspaceSymbols(ctx context.Context, query string) ([]WorkspaceSymbol, error) {
	srv, err := m.ready()
	if err != nil {
		return nil, err
	}
	m.touch()
	return srv.WorkspaceSymbols(ctx, query)
}

// Implementations resolves implementations of the symbol at the position.
func (m *Manager) Implementations(ctx context.Context, path string, line int, query string) ([]Location, error) {
	srv, err := m.ready()
	if err != nil {
		return nil, err
	}
	m.touch()
	return srv.Implementations(ctx, path, line, query)
}

// ready returns a ready server or the disabled/failed reason.
func (m *Manager) ready() (*Server, error) {
	m.mu.Lock()
	if !m.on {
		m.mu.Unlock()
		return nil, ErrDisabled
	}
	m.mu.Unlock()
	srv, err := m.ensureStarted()
	if err != nil {
		return nil, err
	}
	if err := srv.waitReady(); err != nil {
		return nil, err
	}
	return srv, nil
}

// ServerStatus is the /lsp surface's row shape.
type ServerStatus struct {
	Name        string
	State       string
	Enabled     bool
	ServerInfo  string
	DiagFiles   int
	DiagTotal   int
	Error       string
}

// Status reports the workspace's server state (one row: the configured
// server, whether or not it is running). A server that never started carries
// the last start error (missing command, failed auto-install) so the /lsp
// face can explain the [off] state.
func (m *Manager) Status() []ServerStatus {
	m.mu.Lock()
	on, srv, lastErr := m.on, m.srv, m.lastStartErr
	m.mu.Unlock()
	st := ServerStatus{Name: m.st.Command, Enabled: on, State: StateOff}
	if lastErr != nil {
		st.Error = lastErr.Error()
	}
	if srv != nil {
		st.State = srv.State()
		st.ServerInfo = srv.ServerInfo()
		if err := srv.StartError(); err != nil {
			st.Error = err.Error()
		}
		st.DiagFiles, st.DiagTotal = srv.DiagnosticsCount()
	}
	return []ServerStatus{st}
}

// Stop shuts the server down (idle reclaim and run-end close share this).
// The overlay state is harvested first so the next start replays it.
func (m *Manager) Stop() {
	m.mu.Lock()
	srv := m.srv
	m.srv = nil
	if srv != nil {
		m.pendingOverlays = srv.overlaySnapshot()
	}
	m.janit = time.Now()
	m.mu.Unlock()
	if srv != nil {
		srv.Stop()
	}
}

// Close stops the server and ends the janitor. The Env owner calls it when
// the run ends. The error return mirrors the other managers' Close so the
// shared closeWithSpan helper takes it unchanged (it is always nil: a failed
// shutdown only means the process was already gone).
func (m *Manager) Close() error {
	m.mu.Lock()
	m.close = true
	m.mu.Unlock()
	m.Stop()
	return nil
}
