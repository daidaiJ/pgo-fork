// Package mcp implements pigo's MCP (Model Context Protocol) client (T6.8):
// pigo launches each configured MCP server as a child process and speaks
// line-delimited JSON-RPC 2.0 over its stdio, reusing internal/jsonrpc as the
// transport (the same foundation the plugin system uses).
//
// Scope of this first batch (spec mcp-integration-shape.md §3.3): the tools
// face only — initialize (protocol-version negotiation) followed by
// notifications/initialized, tools/list, and tools/call. Resources, prompts,
// sampling, roots and non-stdio transports are deliberately out of scope
// (deviation D-1/D-2): pigo has no consumption point for them — slash commands
// are skills, and there is no resource injection surface.
//
// Wire-level notes that matter for correctness:
//
//   - The tool registry name is the three-segment mcp__<server>__<tool>,
//     matching the convention minimax/qwen/grok converged on. That name is the
//     key both the declaration tiers (internal/tooldecl) and the permission
//     rules (internal/toolrules) address a single tool by, which is what makes
//     per-tool enable/disable possible without a new mechanism.
//   - Disabling a tool hides its registration but keeps the connection (grok's
//     stash): re-enabling never re-runs tools/list.
//   - A server that fails to start or handshake is skipped: bootstrap-style
//     fault tolerance, never fatal to the run.
package mcp

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"
)

// ProtocolVersion is the MCP revision this client announces in initialize. A
// server that answers without a protocolVersion is rejected: silently accepting
// an unknown revision is how incompatible servers get wired in half-working.
const ProtocolVersion = "2025-06-18"

const (
	// DefaultTimeout bounds a single initialize / tools/list / tools/call round
	// trip when the server config does not set timeout_seconds.
	DefaultTimeout = 30 * time.Second
	// DefaultMaxParallel bounds in-flight calls against one server. The JSON-RPC
	// transport correlates by id so concurrency is safe; the cap keeps one slow
	// server from absorbing the whole batch (spec §3.4).
	DefaultMaxParallel = 4
)

// ToolName builds the three-segment registry name for one MCP tool:
// mcp__<server>__<tool>. It is the single place the naming convention lives —
// the declaration tiers, the permission rules and diagnostics all key off it.
func ToolName(server, tool string) string { return "mcp__" + server + "__" + tool }

// ServerConfig is one [[mcp.servers]] entry: how to reach the server and which
// of its tools stay hidden. For the stdio transport it mirrors jsonrpc.Config's
// process fields because that is exactly what the transport needs; for the HTTP
// transport only Type and URL matter.
type ServerConfig struct {
	// Name namespaces this server's tools (middle segment of mcp__<name>__<tool>).
	Name string
	// Type selects the transport: "stdio" (the default; launch Command as a
	// child process and speak line-delimited JSON-RPC over its stdio) or
	// "http" (Streamable HTTP against URL: one POST per message, session kept
	// in the Mcp-Session-Id header, SSE or JSON responses accepted).
	Type string
	// URL is the Streamable HTTP endpoint, used when Type is "http".
	URL string
	// Command is the executable to launch (stdio transport).
	Command string
	// Args are the process arguments (excluding the command itself).
	Args []string
	// Env is the child's environment (os/exec form). nil inherits the parent's.
	Env []string
	// Dir is the child's working directory; empty means the parent's.
	Dir string
	// Enabled is nil (default on) or an explicit on/off. A disabled server is
	// never launched, so "disabled" here costs nothing — no connection exists.
	Enabled *bool
	// TimeoutSeconds bounds each round trip; <= 0 means DefaultTimeout.
	TimeoutSeconds int
	// MaxParallel bounds in-flight calls; <= 0 means DefaultMaxParallel.
	MaxParallel int
	// DisabledTools names tools (by their server-local name) to keep out of the
	// declared face. Disabling hides the registration only: the connection and
	// the cached tools/list snapshot survive, so re-enabling is free.
	DisabledTools []string
}

// IsEnabled reports whether the server should be launched. An unset Enabled
// defaults to on, so a server the user merely described is still connected.
func (c ServerConfig) IsEnabled() bool { return c.Enabled == nil || *c.Enabled }

// Timeout resolves the per-call deadline.
func (c ServerConfig) Timeout() time.Duration {
	if c.TimeoutSeconds > 0 {
		return time.Duration(c.TimeoutSeconds) * time.Second
	}
	return DefaultTimeout
}

// Concurrency resolves the in-flight call cap for this server.
func (c ServerConfig) Concurrency() int {
	if c.MaxParallel > 0 {
		return c.MaxParallel
	}
	return DefaultMaxParallel
}

// Disabled reports whether the named server-local tool is hidden. Matching is
// case-insensitive, matching how [tools] name lists behave.
func (c ServerConfig) Disabled(tool string) bool {
	for _, n := range c.DisabledTools {
		if strings.EqualFold(strings.TrimSpace(n), tool) {
			return true
		}
	}
	return false
}

// Validate reports a configuration problem that makes the entry unusable. It is
// checked before launch so a typo surfaces as a diagnostic, not as a server
// that starts and then registers nothing.
func (c ServerConfig) Validate() error {
	if strings.TrimSpace(c.Name) == "" {
		return fmt.Errorf("mcp: server entry needs a name")
	}
	if strings.ContainsAny(c.Name, "_ \t") {
		return fmt.Errorf("mcp: server name %q must not contain '_' or whitespace (it namespaces tool names)", c.Name)
	}
	if c.isHTTP() {
		if strings.TrimSpace(c.URL) == "" {
			return fmt.Errorf("mcp: http server %q needs a url", c.Name)
		}
		return nil
	}
	if strings.TrimSpace(c.Command) == "" {
		return fmt.Errorf("mcp: server %q needs a command", c.Name)
	}
	return nil
}

// isHTTP reports whether this entry uses the Streamable HTTP transport. An
// absent Type means stdio, the shape the first batch shipped with.
func (c ServerConfig) isHTTP() bool {
	return strings.EqualFold(strings.TrimSpace(c.Type), "http")
}

// Manager owns the set of connected MCP servers and their aggregated tools. It
// is not safe for concurrent modification; connect once at startup, then read.
//
// A Manager is never nil-on-error: Connect records per-server failures and
// returns a Manager describing what did come up, so a dead server can never
// stop a run (the plugin system's contract, applied here).
type Manager struct {
	servers []*server

	// warnLog and serverStderr are kept from Connect so the live-toggle and
	// reload methods (SetServerEnabled, Reload) reuse the same diagnostics
	// plumbing; both may be nil.
	warnLog      io.Writer
	serverStderr io.Writer
}

// server is one configured entry plus its connection outcome.
type server struct {
	cfg    ServerConfig
	client *Client
	// err records why this server is not connected (config invalid, launch
	// failed, handshake failed). It is nil on success and on "disabled".
	err error
}

// Connect launches every enabled server, handshakes it, and caches its tool
// list. A server that fails is written to warnLog (when non-nil) and skipped;
// others still connect. serverStderr, when non-nil, receives each child's
// stderr. Disabled servers are recorded (visible in Status) but never launched.
func Connect(ctx context.Context, cfgs []ServerConfig, warnLog, serverStderr io.Writer) *Manager {
	m := &Manager{warnLog: warnLog, serverStderr: serverStderr}
	for _, cfg := range cfgs {
		s := &server{cfg: cfg}
		if !cfg.IsEnabled() {
			m.servers = append(m.servers, s)
			continue
		}
		if err := cfg.Validate(); err != nil {
			s.err = err
			m.servers = append(m.servers, s)
			if warnLog != nil {
				fmt.Fprintf(warnLog, "pigo: %v\n", err)
			}
			continue
		}
		c, err := dial(ctx, cfg, serverStderr)
		if err != nil {
			s.err = err
			if warnLog != nil {
				fmt.Fprintf(warnLog, "pigo: mcp server %q failed to start: %v\n", cfg.Name, err)
			}
			m.servers = append(m.servers, s)
			continue
		}
		s.client = c
		m.servers = append(m.servers, s)
	}
	return m
}

// Tools returns the aggregated tools of every connected server, in config
// order, minus each server's disabled tools. A hidden tool is absent from this
// slice, which is what removes it from the declared face.
func (m *Manager) Tools() []*MCPTool {
	var out []*MCPTool
	for _, s := range m.servers {
		if s.client == nil {
			continue
		}
		for _, t := range s.client.Tools() {
			if s.cfg.Disabled(t.Name) {
				continue
			}
			out = append(out, &MCPTool{server: s.cfg.Name, client: s.client, tool: t})
		}
	}
	return out
}

// HiddenNames returns the full three-segment names of every tool a server
// config disables, whether or not the server is connected. Callers fold these
// into the deferred-declaration hidden tier, which is where "disabled" lands
// (spec §3.1): not declared, not discoverable, connectivity preserved.
func (m *Manager) HiddenNames() []string {
	var out []string
	for _, s := range m.servers {
		for _, t := range s.cfg.DisabledTools {
			t = strings.TrimSpace(t)
			if t == "" {
				continue
			}
			out = append(out, ToolName(s.cfg.Name, t))
		}
	}
	return out
}

// ServerStatus is the diagnostic view of one configured server. It backs the
// /mcp slash entry and the startup span (T6.9).
type ServerStatus struct {
	// Name is the configured server name.
	Name string
	// Enabled reports whether the config turns the server on.
	Enabled bool
	// Connected reports whether a live connection exists.
	Connected bool
	// ToolCount is the number of tools the server advertises (all of them,
	// including disabled ones, so the count does not move when a tool is
	// hidden).
	ToolCount int
	// DisabledCount is how many advertised tools the config hides.
	DisabledCount int
	// ServerInfo is the server's self-reported name/version, when it sent one.
	ServerInfo string
	// Error is why the server is not connected, empty when it is.
	Error string
}

// Status returns one entry per configured server, in config order.
func (m *Manager) Status() []ServerStatus {
	out := make([]ServerStatus, 0, len(m.servers))
	for _, s := range m.servers {
		st := ServerStatus{Name: s.cfg.Name, Enabled: s.cfg.IsEnabled()}
		if s.client != nil {
			st.Connected = true
			tools := s.client.Tools()
			st.ToolCount = len(tools)
			for _, t := range tools {
				if s.cfg.Disabled(t.Name) {
					st.DisabledCount++
				}
			}
			st.ServerInfo = s.client.ServerInfo()
		}
		if s.err != nil {
			st.Error = s.err.Error()
		}
		out = append(out, st)
	}
	return out
}

// Close shuts down every connected server, returning the first error. All
// servers are attempted regardless.
func (m *Manager) Close() error {
	var firstErr error
	for _, s := range m.servers {
		if s.client == nil {
			continue
		}
		if err := s.client.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// SetToolDisabled flips the per-tool switch on the LIVE manager (T6.9, the
// /mcp tool enable|disable projection). The manager's own copy of the config
// is mutated, so a later Tools()/Status() reflects it immediately and the
// connection is untouched — disabling hides the registration only (grok
// stash semantics, spec §3.1). Returns false when no such server exists.
func (m *Manager) SetToolDisabled(server, tool string, disabled bool) bool {
	for _, s := range m.servers {
		if s.cfg.Name != server {
			continue
		}
		mutateDisabledTools(&s.cfg, tool, disabled)
		return true
	}
	return false
}

// SetServerEnabled flips the server-level switch on the LIVE manager (T6.9,
// /mcp enable|disable). Disable closes the connection: the server's tools
// leave the aggregated face immediately and Status reports it disabled.
// Enable (re)connects: a stdio server is relaunched, an HTTP one re-handshakes
// — a failed reconnect is recorded on the server (visible in Status) and
// returned as an error, never fatal. Returns false when no such server exists.
func (m *Manager) SetServerEnabled(ctx context.Context, server string, enabled bool) (bool, error) {
	for _, s := range m.servers {
		if s.cfg.Name != server {
			continue
		}
		if enabled {
			if s.client != nil {
				return true, nil // already connected
			}
			c, err := dial(ctx, s.cfg, m.serverStderr)
			if err != nil {
				s.err = err
				return true, err
			}
			s.err = nil
			s.client = c
			return true, nil
		}
		if s.client != nil {
			_ = s.client.Close()
			s.client = nil
		}
		s.err = nil // disabled is a state, not a failure
		return true, nil
	}
	return false, nil
}

// Reload re-runs tools/list for one connected server and reports whether its
// schema snapshot changed (minimax's double-check, spec §3.3: a changed
// snapshot is REPORTED via the returned flag, never silently swapped under a
// live fingerprint comparison). The cached snapshot is refreshed either way.
// A disabled or failed server yields an error naming the state.
func (m *Manager) Reload(ctx context.Context, server string) (changed bool, err error) {
	for _, s := range m.servers {
		if s.cfg.Name != server {
			continue
		}
		if s.client == nil {
			return false, fmt.Errorf("mcp: server %q is not connected (disabled or failed to start)", server)
		}
		before := s.client.Fingerprint()
		if err := s.client.refresh(ctx); err != nil {
			return false, err
		}
		return before != s.client.Fingerprint(), nil
	}
	return false, fmt.Errorf("mcp: no server named %q", server)
}

// mutateDisabledTools adds or removes tool from cfg.DisabledTools,
// case-insensitively, in place.
func mutateDisabledTools(cfg *ServerConfig, tool string, disabled bool) {
	kept := cfg.DisabledTools[:0:0]
	for _, t := range cfg.DisabledTools {
		if !strings.EqualFold(strings.TrimSpace(t), tool) {
			kept = append(kept, t)
		}
	}
	if disabled {
		kept = append(kept, tool)
	}
	cfg.DisabledTools = kept
}
