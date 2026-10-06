// This file implements the Streamable HTTP transport for one MCP server
// connection (T6.8 follow-up, spec mcp-integration-shape.md D-2 amendment):
// one POST per JSON-RPC message, the session kept in the Mcp-Session-Id
// header, and responses accepted in either form the spec allows — a plain
// application/json envelope or a text/event-stream carrying it.
//
// What is deliberately NOT here (first batch stays minimal):
//   - GET: server-initiated streams and standalone SSE receive loops. pigo
//     never listens for server pushes (D-5 already covers tools/list_changed
//     the same way on stdio), so there is nothing to receive.
//   - Authorization headers / browser reauth: out of scope per spec §3.6.
//   - Retry/reconnect: a failed HTTP request degrades to an error result, the
//     same isolation an MCPTool transport failure gets.
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	// httpAccept is the header pair the Streamable HTTP spec requires on every
	// POST: the server may answer with either content type.
	httpAccept = "application/json, text/event-stream"
	// sessionHeader carries the server-assigned session id on every request
	// after initialize answers with one.
	sessionHeader = "Mcp-Session-Id"
	// notifyTimeout bounds a notification POST, which has no caller deadline.
	notifyTimeout = 15 * time.Second
)

// httpTransport speaks MCP over Streamable HTTP. It is safe for concurrent
// Calls (each is an independent request); the session id and id counter are
// guarded by mu.
type httpTransport struct {
	endpoint string
	client   *http.Client

	mu      sync.Mutex
	session string
	nextID  int64
}

// newHTTPTransport validates the endpoint and returns the transport. The URL
// must parse and be http(s) — a bad endpoint should surface as a per-server
// connect failure, not as a confusing request error later.
func newHTTPTransport(raw string) (*httpTransport, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("mcp: invalid http url %q", raw)
	}
	return &httpTransport{endpoint: u.String(), client: &http.Client{}}, nil
}

// post sends one message and returns the response with its body unclosed —
// Call must read either the JSON body or the SSE stream from it.
func (t *httpTransport) post(ctx context.Context, msg []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.endpoint, bytes.NewReader(msg))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", httpAccept)
	t.mu.Lock()
	s := t.session
	t.mu.Unlock()
	if s != "" {
		req.Header.Set(sessionHeader, s)
	}
	return t.client.Do(req)
}

// rpcEnvelope is one JSON-RPC 2.0 message as it comes back over either
// transport form. Only the fields Call needs are modeled.
type rpcEnvelope struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Call performs one JSON-RPC request over Streamable HTTP. The caller's ctx
// (cfg.Timeout, in practice) bounds the whole exchange, including a server
// that answers as a long-lived SSE stream.
//
// Session-expiry recovery (qwen's reconnect contract, adapted): a 404 means
// the server dropped the session (restart or teardown) and rejected the
// request BEFORE dispatch — nothing executed, so re-handshaking once and
// re-sending the original request is safe for every tool regardless of its
// annotations, unlike a post-execution retry (which stays fail-closed per
// Tool.Replayable). Anything else surfaces as an error; MCPTool isolates it.
func (t *httpTransport) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	res, err := t.callOnce(ctx, method, params)
	if err == nil {
		return res, nil
	}
	var gone sessionGoneError
	if !errors.As(err, &gone) || method == "initialize" {
		return nil, err
	}
	if herr := t.rehandshake(ctx); herr != nil {
		return nil, fmt.Errorf("mcp: session expired and re-handshake failed: %v (first error: %w)", herr, err)
	}
	return t.callOnce(ctx, method, params)
}

// sessionGoneError marks a pre-execution rejection caused by a lost session.
type sessionGoneError struct{ status string }

func (e sessionGoneError) Error() string {
	return fmt.Sprintf("mcp: http %s: session expired", e.status)
}

// callOnce is one request/response exchange with no recovery.
func (t *httpTransport) callOnce(ctx context.Context, method string, params any) (json.RawMessage, error) {
	t.mu.Lock()
	t.nextID++
	id := t.nextID
	t.mu.Unlock()

	msg, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	})
	if err != nil {
		return nil, err
	}
	resp, err := t.post(ctx, msg)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// A server that assigns sessions does so on the initialize response; from
	// then on every request carries it.
	if s := resp.Header.Get(sessionHeader); s != "" {
		t.mu.Lock()
		t.session = s
		t.mu.Unlock()
	}
	if resp.StatusCode == http.StatusNotFound {
		t.mu.Lock()
		t.session = "" // forget the dead session; the retry gets a fresh one
		t.mu.Unlock()
		return nil, sessionGoneError{status: resp.Status}
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("mcp: http %s on %s: %s", resp.Status, method, strings.TrimSpace(string(body)))
	}

	if strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		return t.readSSE(resp.Body, id)
	}
	var env rpcEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return nil, fmt.Errorf("mcp: %s: decode response: %w", method, err)
	}
	return env.resolve(method)
}

// rehandshake re-runs initialize + notifications/initialized against the
// current endpoint, restoring the state a new session starts with. It bounds
// itself by the caller's ctx (cfg.Timeout) so a server that accepts the
// re-handshake but never answers cannot hang the retry.
func (t *httpTransport) rehandshake(ctx context.Context) error {
	if _, err := t.callOnce(ctx, "initialize", initParams{
		ProtocolVersion: ProtocolVersion,
		ClientInfo:      clientInfo{Name: "pigo"},
	}); err != nil {
		return err
	}
	return t.Notify("notifications/initialized", map[string]any{})
}

// readSSE scans a text/event-stream response for the data line carrying the
// reply to the given id and returns its result. Everything else on the stream
// (server-initiated requests, other ids, comments) is skipped.
func (t *httpTransport) readSSE(body io.Reader, id int64) (json.RawMessage, error) {
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" {
			continue
		}
		var env rpcEnvelope
		if err := json.Unmarshal([]byte(data), &env); err != nil {
			continue // not a reply (e.g. a server-pushed notification); skip
		}
		// Match the reply by its numeric id; a message without one is a
		// notification and never the reply we are waiting for.
		var got int64
		if err := json.Unmarshal(env.ID, &got); err != nil || got != id {
			continue
		}
		return env.resolve("request")
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("mcp: sse stream: %w", err)
	}
	return nil, fmt.Errorf("mcp: sse stream ended without a reply")
}

// resolve turns a reply envelope into Call's return: the server's result, or
// its error as a Go error.
func (e rpcEnvelope) resolve(method string) (json.RawMessage, error) {
	if e.Error != nil {
		return nil, fmt.Errorf("mcp: %s: rpc error %d: %s", method, e.Error.Code, e.Error.Message)
	}
	if e.Result == nil {
		return nil, fmt.Errorf("mcp: %s: response carried neither result nor error", method)
	}
	return e.Result, nil
}

// Notify sends one JSON-RPC notification (no id). The spec says notifications
// are answered with 202 Accepted and no body; any success status is fine, and
// failure is reported but never fatal — callers treat it the same way they
// treat the stdio notification write.
func (t *httpTransport) Notify(method string, params any) error {
	msg, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), notifyTimeout)
	defer cancel()
	resp, err := t.post(ctx, msg)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("mcp: http %s on %s", resp.Status, method)
	}
	return nil
}

// Close terminates the session with a best-effort DELETE (the Streamable HTTP
// spec's session-teardown request) and forgets it. An error from the DELETE is
// returned but never blocks shutdown: the Manager folds it into its first-error
// contract the same way it folds a child process exit.
func (t *httpTransport) Close() error {
	t.mu.Lock()
	s := t.session
	t.session = ""
	t.mu.Unlock()
	if s == "" {
		return nil
	}
	req, err := http.NewRequest(http.MethodDelete, t.endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set(sessionHeader, s)
	ctx, cancel := context.WithTimeout(context.Background(), notifyTimeout)
	defer cancel()
	resp, err := t.client.Do(req.WithContext(ctx))
	if err != nil {
		return nil // the session dies with the server anyway; teardown is best-effort
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
	return nil
}
