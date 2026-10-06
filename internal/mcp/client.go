// This file implements one MCP server connection: process launch, the
// initialize handshake, the cached tools/list snapshot, and tools/call.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"sort"
	"strings"

	"github.com/smallnest/pigo/internal/jsonrpc"
)

// clientInfo is pigo's self-description in the initialize handshake.
type clientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// initParams is the initialize request body. Capabilities is sent empty: pigo
// implements none of the optional client capabilities (no sampling, no roots)
// in this batch, and advertising one would be a promise it cannot keep.
type initParams struct {
	ProtocolVersion string     `json:"protocolVersion"`
	Capabilities    struct{}   `json:"capabilities"`
	ClientInfo      clientInfo `json:"clientInfo"`
}

// initResult is the initialize response.
type initResult struct {
	ProtocolVersion string      `json:"protocolVersion"`
	ServerInfo      *serverInfo `json:"serverInfo,omitempty"`
}

type serverInfo struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

// annotations carries the tool hints MCP servers may publish. They feed pigo's
// side-effect contract (internal/agentcore.ToolEffect) and the replay-safety
// decision: only a read-only or idempotent tool may be re-sent after a
// reconnect (qwen's canSafelyReplay contract, spec §3.4).
type annotations struct {
	ReadOnly    bool `json:"readOnlyHint,omitempty"`
	Destructive bool `json:"destructiveHint,omitempty"`
	Idempotent  bool `json:"idempotentHint,omitempty"`
	OpenWorld   bool `json:"openWorldHint,omitempty"`
}

// Tool is one tool as advertised by a server, with its hints resolved.
type Tool struct {
	// Name is the server-local tool name (the third segment of the registry
	// name; the registry name itself comes from ToolName).
	Name string
	// Description is the server's one-line description, used verbatim as the
	// agent-facing description and as the deferred-declaration announcement.
	Description string
	// InputSchema is the JSON Schema for the tool's arguments, passed through
	// to the agent's tool registry.
	InputSchema json.RawMessage
	// ReadOnly / Destructive / Idempotent / OpenWorld are the resolved hints.
	// They are false when the server publishes no annotations — the
	// conservative reading, matching how an un-declared pigo tool is treated.
	ReadOnly    bool
	Destructive bool
	Idempotent  bool
	OpenWorld   bool
}

// Replayable reports whether a call may be re-sent after an interrupted
// connection. Anything that is neither read-only nor idempotent fails closed:
// it is reported as interrupted rather than silently retried (spec §3.4).
func (t Tool) Replayable() bool { return t.ReadOnly || t.Idempotent }

// wireTool is the tools/list entry as it appears on the wire.
type wireTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema,omitempty"`
	Annotations *annotations    `json:"annotations,omitempty"`
}

type toolListResult struct {
	Tools []wireTool `json:"tools"`
	// NextCursor is intentionally not followed (deviation D-3): servers that
	// paginate report their first page, and following cursors at startup would
	// trade a bounded handshake for an unbounded one.
	NextCursor string `json:"nextCursor,omitempty"`
}

// callParams is the tools/call request body.
type callParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// callResult is the tools/call response. Content is the MCP content array;
// pigo renders the text blocks and summarizes anything else (deviation D-4:
// image/resource blocks are not surfaced as pigo content in this batch).
type callResult struct {
	Content []contentBlock `json:"content"`
	IsError bool           `json:"isError,omitempty"`
}

type contentBlock struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	MIMEType string `json:"mimeType,omitempty"`
}

// Client is one live MCP server connection.
type Client struct {
	cfg ServerConfig
	rpc transport

	tools      []Tool
	serverName string

	// sem bounds in-flight calls against this server.
	sem chan struct{}
}

// transport is the request seam one MCP connection speaks through: either a
// child process via internal/jsonrpc (stdio) or the Streamable HTTP client.
// *jsonrpc.Client satisfies it as-is; http.go adds the second implementation.
type transport interface {
	Call(ctx context.Context, method string, params any) (json.RawMessage, error)
	Notify(method string, params any) error
	Close() error
}

// dial connects one server (launching it for stdio), performs the handshake,
// and caches its tools. For stdio the process is killed when any step fails so
// a half-initialized server is never left running; HTTP connections are
// stateless requests, so failure cleanup is a no-op.
func dial(ctx context.Context, cfg ServerConfig, stderr io.Writer) (*Client, error) {
	var t transport
	var err error
	if cfg.isHTTP() {
		t, err = newHTTPTransport(cfg.URL)
	} else {
		t, err = jsonrpc.NewClient(jsonrpc.Config{
			Command: cfg.Command,
			Args:    cfg.Args,
			Env:     cfg.Env,
			Dir:     cfg.Dir,
			Stderr:  stderr,
		})
	}
	if err != nil {
		return nil, err
	}
	c := &Client{
		cfg: cfg,
		rpc: t,
		sem: make(chan struct{}, cfg.Concurrency()),
	}
	if err := c.handshake(ctx); err != nil {
		_ = t.Close()
		return nil, err
	}
	return c, nil
}

// handshake runs initialize + notifications/initialized + tools/list. It is
// bounded by the configured timeout so a server that accepts the connection but
// never answers cannot hang startup.
func (c *Client) handshake(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout())
	defer cancel()

	var res initResult
	raw, err := c.rpc.Call(ctx, "initialize", initParams{
		ProtocolVersion: ProtocolVersion,
		ClientInfo:      clientInfo{Name: "pigo"},
	})
	if err != nil {
		return fmt.Errorf("initialize: %w", err)
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return fmt.Errorf("initialize: decode result: %w", err)
	}
	if strings.TrimSpace(res.ProtocolVersion) == "" {
		return errors.New("initialize: server sent no protocolVersion")
	}
	// The spec requires this notification before any other request; a server
	// that ignores it stays conformant, so a write failure is not fatal.
	_ = c.rpc.Notify("notifications/initialized", map[string]any{})

	if err := c.refresh(ctx); err != nil {
		return err
	}
	if res.ServerInfo != nil {
		c.serverName = strings.TrimSpace(res.ServerInfo.Name)
		if v := strings.TrimSpace(res.ServerInfo.Version); v != "" {
			c.serverName += " " + v
		}
	}
	return nil
}

// refresh re-runs tools/list and replaces the cached snapshot. It backs an
// explicit reload rather than a notification-driven one: internal/jsonrpc's
// reader drops server->client notifications, so tools/list_changed cannot be
// observed yet (deviation D-5).
func (c *Client) refresh(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout())
	defer cancel()

	raw, err := c.rpc.Call(ctx, "tools/list", map[string]any{})
	if err != nil {
		return fmt.Errorf("tools/list: %w", err)
	}
	var list toolListResult
	if err := json.Unmarshal(raw, &list); err != nil {
		return fmt.Errorf("tools/list: decode: %w", err)
	}
	tools := make([]Tool, 0, len(list.Tools))
	for _, wt := range list.Tools {
		if strings.TrimSpace(wt.Name) == "" {
			continue // an unnamed tool cannot be addressed; skip it
		}
		t := Tool{
			Name:        wt.Name,
			Description: wt.Description,
			InputSchema: wt.InputSchema,
		}
		if wt.Annotations != nil {
			t.ReadOnly = wt.Annotations.ReadOnly
			t.Destructive = wt.Annotations.Destructive
			t.Idempotent = wt.Annotations.Idempotent
			t.OpenWorld = wt.Annotations.OpenWorld
		}
		tools = append(tools, t)
	}
	// Stable order so the declared face (and every snapshot of it) does not
	// depend on the server's own ordering.
	sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })
	c.tools = tools
	return nil
}

// Tools returns the cached tool snapshot. It is owned by the caller in
// practice: the slice is replaced wholesale by refresh, never mutated.
func (c *Client) Tools() []Tool { return c.tools }

// ServerInfo returns the server's self-reported name and version, or "" when it
// sent none.
func (c *Client) ServerInfo() string { return c.serverName }

// Fingerprint hashes the cached snapshot (names, descriptions, schemas). It is
// the schema-snapshot identity a reload can compare against (minimax's
// double-check: a tool whose schema moved between list and call is reported,
// not silently used).
func (c *Client) Fingerprint() string {
	h := fnv.New64a()
	for _, t := range c.tools {
		fmt.Fprintf(h, "%s\x00%s\x00%s\x00", t.Name, t.Description, t.InputSchema)
	}
	return fmt.Sprintf("%016x", h.Sum64())
}

// CallTool invokes one tool and renders its content. It returns the rendered
// text and the server's isError flag. A transport failure is returned as an
// error so the caller can isolate it; a tool-level failure comes back with
// isError=true and err=nil, exactly like a plugin's CallResult.
func (c *Client) CallTool(ctx context.Context, name string, args json.RawMessage) (string, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout())
	defer cancel()

	select {
	case c.sem <- struct{}{}:
		defer func() { <-c.sem }()
	case <-ctx.Done():
		return "", false, ctx.Err()
	}

	params := callParams{Name: name}
	if len(args) > 0 && string(args) != "null" {
		params.Arguments = args
	}
	raw, err := c.rpc.Call(ctx, "tools/call", params)
	if err != nil {
		return "", false, err
	}
	var res callResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return "", false, fmt.Errorf("tools/call %q: decode: %w", name, err)
	}
	return renderContent(res.Content), res.IsError, nil
}

// renderContent flattens an MCP content array into one text block. Text blocks
// are joined with a blank line; anything else is summarized by type and MIME
// type (deviation D-4).
func renderContent(blocks []contentBlock) string {
	if len(blocks) == 0 {
		return ""
	}
	var b strings.Builder
	first := true
	for _, blk := range blocks {
		text := blk.Text
		if blk.Type != "text" || strings.TrimSpace(text) == "" {
			if blk.Type == "" {
				continue
			}
			text = fmt.Sprintf("[%s content", blk.Type)
			if blk.MIMEType != "" {
				text += ": " + blk.MIMEType
			}
			text += "]"
		}
		if !first {
			b.WriteString("\n\n")
		}
		b.WriteString(text)
		first = false
	}
	return b.String()
}

// Close terminates the server process. It is idempotent, matching the
// underlying jsonrpc client.
func (c *Client) Close() error { return c.rpc.Close() }
