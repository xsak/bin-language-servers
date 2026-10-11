// Package jsonrpc implements JSON-RPC 2.0 over a byte stream with LSP base-protocol framing
// (Content-Length headers).
package jsonrpc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/textproto"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// Standard and LSP-specific error codes.
const (
	CodeParseError           = -32700
	CodeInvalidRequest       = -32600
	CodeMethodNotFound       = -32601
	CodeInvalidParams        = -32602
	CodeInternalError        = -32603
	CodeServerNotInitialized = -32002
	CodeRequestCancelled     = -32800
	CodeContentModified      = -32801
)

// MaxMessageSize bounds a single message body, so a corrupt or hostile Content-Length cannot
// make the server allocate without limit. Real documents are far smaller.
const MaxMessageSize = 256 << 20

// Error is a JSON-RPC error object. Handlers may return it to control the error code.
type Error struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *Error) Error() string { return fmt.Sprintf("jsonrpc %d: %s", e.Code, e.Message) }

// Errorf builds an *Error.
func Errorf(code int, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// ID is a request id: either a number or a string, kept verbatim.
type ID struct{ raw json.RawMessage }

func (id ID) MarshalJSON() ([]byte, error) {
	if id.raw == nil {
		return []byte("null"), nil
	}
	return id.raw, nil
}

func (id *ID) UnmarshalJSON(b []byte) error {
	id.raw = append(json.RawMessage(nil), b...)
	return nil
}

func (id ID) String() string { return string(id.raw) }

// Message is the wire form of requests, notifications and responses.
type Message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *ID             `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// IsRequest reports whether m expects a response.
func (m *Message) IsRequest() bool { return m.Method != "" && m.ID != nil }

// IsNotification reports whether m is a notification.
func (m *Message) IsNotification() bool { return m.Method != "" && m.ID == nil }

// IsResponse reports whether m answers a call we made.
func (m *Message) IsResponse() bool { return m.Method == "" && m.ID != nil }

// Handler processes incoming requests and notifications.
//
// Notifications are delivered one at a time, in order, on the read goroutine, so document
// changes are never reordered. Requests are delivered on their own goroutine with a context
// that is cancelled by $/cancelRequest or when the connection closes.
type Handler interface {
	Handle(ctx context.Context, method string, params json.RawMessage) (any, error)
	Notify(ctx context.Context, method string, params json.RawMessage)
}

// Conn is a bidirectional JSON-RPC connection.
type Conn struct {
	r  *bufio.Reader
	w  io.Writer
	wm sync.Mutex

	nextID  atomic.Int64
	mu      sync.Mutex
	pending map[string]chan *Message      // outgoing calls awaiting a response
	active  map[string]context.CancelFunc // incoming requests in flight
	wg      sync.WaitGroup
}

// NewConn wraps r and w.
func NewConn(r io.Reader, w io.Writer) *Conn {
	return &Conn{
		r:       bufio.NewReader(r),
		w:       w,
		pending: map[string]chan *Message{},
		active:  map[string]context.CancelFunc{},
	}
}

// ReadMessage reads one framed message.
func (c *Conn) ReadMessage() (*Message, error) {
	tp := textproto.NewReader(c.r)
	hdr, err := tp.ReadMIMEHeader()
	if err != nil {
		if errors.Is(err, io.EOF) && len(hdr) == 0 {
			return nil, io.EOF
		}
		return nil, fmt.Errorf("read header: %w", err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(hdr.Get("Content-Length")))
	if err != nil || n < 0 || n > MaxMessageSize {
		return nil, fmt.Errorf("bad Content-Length %q", hdr.Get("Content-Length"))
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(c.r, body); err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	var m Message
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, &Error{Code: CodeParseError, Message: err.Error()}
	}
	return &m, nil
}

// WriteMessage frames and writes m. Safe for concurrent use.
func (c *Conn) WriteMessage(m *Message) error {
	m.JSONRPC = "2.0"
	body, err := json.Marshal(m)
	if err != nil {
		return err
	}
	c.wm.Lock()
	defer c.wm.Unlock()
	if _, err := fmt.Fprintf(c.w, "Content-Length: %d\r\n\r\n", len(body)); err != nil {
		return err
	}
	_, err = c.w.Write(body)
	return err
}

// Notify sends a notification to the peer.
func (c *Conn) Notify(method string, params any) error {
	raw, err := marshalParams(params)
	if err != nil {
		return err
	}
	return c.WriteMessage(&Message{Method: method, Params: raw})
}

// Call sends a request to the peer and waits for its response. The result is decoded into
// result if it is non-nil. Call only works while Serve is running.
func (c *Conn) Call(ctx context.Context, method string, params, result any) error {
	raw, err := marshalParams(params)
	if err != nil {
		return err
	}
	id := ID{raw: json.RawMessage(strconv.FormatInt(c.nextID.Add(1), 10))}
	ch := make(chan *Message, 1)
	c.mu.Lock()
	c.pending[id.String()] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, id.String())
		c.mu.Unlock()
	}()

	if err := c.WriteMessage(&Message{ID: &id, Method: method, Params: raw}); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		_ = c.Notify("$/cancelRequest", map[string]any{"id": id})
		return ctx.Err()
	case resp, ok := <-ch:
		if !ok {
			return io.ErrClosedPipe
		}
		if resp.Error != nil {
			return resp.Error
		}
		if result != nil && len(resp.Result) > 0 {
			return json.Unmarshal(resp.Result, result)
		}
		return nil
	}
}

// Serve reads messages until EOF or a read error and dispatches them to h. It waits for
// in-flight requests to finish before returning. A clean EOF returns nil.
func (c *Conn) Serve(ctx context.Context, h Handler) error {
	ctx, cancel := context.WithCancel(ctx)
	defer func() {
		cancel()
		c.wg.Wait()
		c.mu.Lock()
		for k, ch := range c.pending {
			close(ch)
			delete(c.pending, k)
		}
		c.mu.Unlock()
	}()

	for {
		m, err := c.ReadMessage()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			var rpcErr *Error
			if errors.As(err, &rpcErr) {
				_ = c.WriteMessage(&Message{ID: &ID{}, Error: rpcErr})
				continue
			}
			return err
		}
		switch {
		case m.IsResponse():
			c.mu.Lock()
			ch := c.pending[m.ID.String()]
			c.mu.Unlock()
			if ch != nil {
				ch <- m
			}
		case m.Method == "$/cancelRequest":
			var p struct {
				ID ID `json:"id"`
			}
			if json.Unmarshal(m.Params, &p) == nil {
				c.mu.Lock()
				if cancelReq := c.active[p.ID.String()]; cancelReq != nil {
					cancelReq()
				}
				c.mu.Unlock()
			}
		case m.IsNotification():
			_ = Guard(func() error { h.Notify(ctx, m.Method, m.Params); return nil })
		case m.IsRequest():
			c.dispatch(ctx, h, m)
		default:
			_ = c.WriteMessage(&Message{ID: m.ID, Error: Errorf(CodeInvalidRequest, "invalid message")})
		}
	}
}

func (c *Conn) dispatch(ctx context.Context, h Handler, m *Message) {
	reqCtx, cancel := context.WithCancel(ctx)
	key := m.ID.String()
	c.mu.Lock()
	c.active[key] = cancel
	c.mu.Unlock()

	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		defer func() {
			c.mu.Lock()
			delete(c.active, key)
			c.mu.Unlock()
			cancel()
		}()

		var result any
		err := Guard(func() (err error) {
			result, err = h.Handle(reqCtx, m.Method, m.Params)
			return err
		})
		resp := &Message{ID: m.ID}
		switch {
		case err != nil:
			resp.Error = toError(reqCtx, err)
		default:
			raw, mErr := json.Marshal(result)
			if mErr != nil {
				resp.Error = Errorf(CodeInternalError, "marshal result: %v", mErr)
			} else {
				resp.Result = raw
			}
		}
		_ = c.WriteMessage(resp)
	}()
}

// Guard runs fn and turns a panic into an internal error, so one bad document or a parser bug
// fails a single request instead of the whole server. The stack is kept in Error.Data.
func Guard(fn func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			stack, _ := json.Marshal(string(debug.Stack()))
			err = &Error{Code: CodeInternalError, Message: fmt.Sprintf("internal error: %v", r), Data: stack}
		}
	}()
	return fn()
}

func toError(ctx context.Context, err error) *Error {
	var rpcErr *Error
	if errors.As(err, &rpcErr) {
		return rpcErr
	}
	if ctx.Err() != nil || errors.Is(err, context.Canceled) {
		return Errorf(CodeRequestCancelled, "request cancelled")
	}
	return Errorf(CodeInternalError, "%v", err)
}

func marshalParams(params any) (json.RawMessage, error) {
	if params == nil {
		return nil, nil
	}
	if raw, ok := params.(json.RawMessage); ok {
		return raw, nil
	}
	return json.Marshal(params)
}
