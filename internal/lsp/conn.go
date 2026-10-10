package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
)

// conn is one LSP session's JSON-RPC transport over a byte stream (a child
// process's stdio in production, a net.Pipe in tests). It implements the LSP
// 3.17 framing — a Content-Length header block followed by the raw JSON body
// — which is not the newline-delimited dialect internal/jsonrpc speaks, so
// this is a self-contained implementation rather than a reuse.
//
// Message dispatch:
//   - response (id, result/error) → the pending Call's channel;
//   - server→client request (id + method) → onRequest, answered automatically;
//   - notification (method only) → onNotify.
//
// Writes are serialized by a mutex, so Call and Notify are safe for
// concurrent use.
type conn struct {
	w io.Writer

	writeMu sync.Mutex
	nextID  int64

	mu      sync.Mutex
	pending map[int64]chan rpcResponse
	closed  bool
	closeErr error

	// onRequest answers a server→client request; returning an error sends a
	// JSON-RPC error response. onNotify receives server→client
	// notifications. Both run on the reader goroutine and must not block.
	onRequest func(method string, params json.RawMessage) (any, error)
	onNotify  func(method string, params json.RawMessage)

	// done closes when the reader goroutine exits (peer closed / framing
	// error); err carries the reason for tests and diagnostics.
	done chan struct{}
	err  error
}

type rpcResponse struct {
	result json.RawMessage
	err    *rpcError
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("rpc %d: %s", e.Code, e.Message) }

// Common JSON-RPC error codes (the ones this client sends or surfaces).
const (
	codeParseError     = -32700
	codeMethodNotFound = -32601
	codeServerErrorMin = -32000 // LSP servers report server errors from here up
)

func newConn(r io.Reader, w io.Writer, onRequest func(string, json.RawMessage) (any, error), onNotify func(string, json.RawMessage)) *conn {
	c := &conn{
		w:         w,
		pending:   map[int64]chan rpcResponse{},
		onRequest: onRequest,
		onNotify:  onNotify,
		done:      make(chan struct{}),
	}
	go c.readLoop(r)
	return c
}

// Call sends a request and blocks until its response arrives, the context is
// done, or the connection closes. The raw result is returned undecoded so
// each call site owns its result shape.
func (c *conn) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id, body, err := c.encode("request", method, params)
	if err != nil {
		return nil, err
	}
	ch := make(chan rpcResponse, 1)
	c.mu.Lock()
	if c.closed {
		closeErr := c.closeErr
		c.mu.Unlock()
		return nil, closeErr
	}
	c.pending[id] = ch
	c.mu.Unlock()

	if _, err := c.write(body); err != nil {
		c.takePending(id)
		return nil, fmt.Errorf("%s: %w", method, err)
	}
	select {
	case res := <-ch:
		// fail() closes pending channels; a zero-value response means the
		// conn died before the reply arrived.
		if res.result == nil && res.err == nil {
			return nil, fmt.Errorf("%s: connection closed: %w", method, c.closeReason())
		}
		if res.err != nil {
			return nil, fmt.Errorf("%s: %w", method, res.err)
		}
		return res.result, nil
	case <-ctx.Done():
		c.takePending(id)
		return nil, fmt.Errorf("%s: %w", method, ctx.Err())
	case <-c.done:
		c.takePending(id)
		return nil, fmt.Errorf("%s: connection closed: %w", method, c.closeReason())
	}
}

// Notify sends a notification (no id, no reply expected).
func (c *conn) Notify(method string, params any) error {
	_, body, err := c.encode("notify", method, params)
	if err != nil {
		return err
	}
	_, err = c.write(body)
	return err
}

func (c *conn) encode(kind, method string, params any) (int64, []byte, error) {
	c.writeMu.Lock()
	id := c.nextID
	c.nextID++
	c.writeMu.Unlock()

	msg := struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      *int64          `json:"id,omitempty"`
		Method  string          `json:"method,omitempty"`
		Params  json.RawMessage `json:"params,omitempty"`
	}{JSONRPC: "2.0", Method: method}
	if kind == "request" {
		msg.ID = &id
	}
	if params != nil {
		raw, err := json.Marshal(params)
		if err != nil {
			return 0, nil, err
		}
		msg.Params = raw
	}
	body, err := json.Marshal(msg)
	if err != nil {
		return 0, nil, err
	}
	framed := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(body))
	return id, append([]byte(framed), body...), nil
}

func (c *conn) write(body []byte) (int, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.w.Write(body)
}

func (c *conn) takePending(id int64) chan rpcResponse {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := c.pending[id]
	delete(c.pending, id)
	return ch
}

func (c *conn) closeReason() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closeErr != nil {
		return c.closeErr
	}
	return io.ErrClosedPipe
}

// Close marks the conn closed; blocked and later Calls fail with ErrClosed.
// The underlying stream is owned by the caller (the Server closes the
// process pipes).
func (c *conn) Close() { c.fail(ErrConnClosed) }

// fail marks the conn closed with a reason and wakes every pending Call.
func (c *conn) fail(reason error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	if reason != nil {
		c.closeErr = reason
	}
	for id, ch := range c.pending {
		close(ch) // zero-value response → Call reads closeErr
		delete(c.pending, id)
	}
	c.mu.Unlock()
}

// ErrConnClosed is returned by Call after Close (or a dead peer).
var ErrConnClosed = errors.New("lsp: connection closed")

func (c *conn) readLoop(r io.Reader) {
	br := bufio.NewReader(r)
	for {
		body, err := readFrame(br)
		if err != nil {
			c.err = err
			c.fail(fmt.Errorf("lsp: reader stopped: %w", err))
			close(c.done)
			return
		}
		c.dispatch(body)
	}
}

// readFrame parses one Content-Length framed message. Header keys are
// case-insensitive per the LSP spec; the block ends at a blank line.
func readFrame(br *bufio.Reader) ([]byte, error) {
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
				return nil, fmt.Errorf("bad Content-Length %q: %w", v, err)
			}
			length = n
		}
	}
	if length < 0 {
		return nil, errors.New("frame without Content-Length")
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(br, body); err != nil {
		return nil, err
	}
	return body, nil
}

// dispatch routes one decoded body to the pending map, the request handler,
// or the notification handler. Malformed bodies are dropped (a server that
// sends garbage fails the next real exchange anyway).
func (c *conn) dispatch(body []byte) {
	var msg struct {
		ID      *int64          `json:"id"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
		Result  json.RawMessage `json:"result"`
		Error   *rpcError       `json:"error"`
	}
	if err := json.Unmarshal(body, &msg); err != nil {
		return
	}
	switch {
	case msg.ID != nil && msg.Method != "": // server→client request: answer
		var result any
		var rpcErr *rpcError
		if c.onRequest != nil {
			res, err := c.onRequest(msg.Method, msg.Params)
			if err != nil {
				rpcErr = &rpcError{Code: codeMethodNotFound, Message: err.Error()}
			} else {
				result = res
			}
		} else {
			rpcErr = &rpcError{Code: codeMethodNotFound, Message: "client does not handle " + msg.Method}
		}
		c.reply(*msg.ID, result, rpcErr)
	case msg.ID != nil: // response to our request
		c.mu.Lock()
		ch, ok := c.pending[*msg.ID]
		delete(c.pending, *msg.ID)
		c.mu.Unlock()
		if !ok {
			return
		}
		ch <- rpcResponse{result: msg.Result, err: msg.Error}
		close(ch)
	case msg.Method != "": // notification
		if c.onNotify != nil {
			c.onNotify(msg.Method, msg.Params)
		}
	}
}

func (c *conn) reply(id int64, result any, rpcErr *rpcError) {
	msg := struct {
		JSONRPC string    `json:"jsonrpc"`
		ID      int64     `json:"id"`
		Result  any       `json:"result,omitempty"`
		Error   *rpcError `json:"error,omitempty"`
	}{JSONRPC: "2.0", ID: id, Result: result, Error: rpcErr}
	body, err := json.Marshal(msg)
	if err != nil {
		return
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.w.Write(append([]byte(fmt.Sprintf("Content-Length: %d\r\n\r\n", len(body))), body...))
}
