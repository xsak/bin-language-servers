package jsonrpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func TestFraming(t *testing.T) {
	var buf bytes.Buffer
	w := NewConn(nil, &buf)
	if err := w.Notify("x/y", map[string]int{"a": 1}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(buf.String(), "Content-Length: ") {
		t.Fatalf("missing header: %q", buf.String())
	}
	m, err := NewConn(&buf, io.Discard).ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if m.Method != "x/y" || !m.IsNotification() || string(m.Params) != `{"a":1}` {
		t.Fatalf("got %+v", m)
	}
}

type blockingHandler struct{ started chan struct{} }

func (h blockingHandler) Handle(ctx context.Context, method string, _ json.RawMessage) (any, error) {
	if method == "echo" {
		return "ok", nil
	}
	close(h.started)
	<-ctx.Done()
	return nil, ctx.Err()
}

func (blockingHandler) Notify(context.Context, string, json.RawMessage) {}

func TestCallAndCancel(t *testing.T) {
	aIn, bOut := io.Pipe()
	bIn, aOut := io.Pipe()
	a, b := NewConn(aIn, aOut), NewConn(bIn, bOut)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := blockingHandler{started: make(chan struct{})}
	go func() { _ = b.Serve(ctx, h) }()
	go func() { _ = a.Serve(ctx, blockingHandler{started: make(chan struct{})}) }()

	var got string
	if err := a.Call(ctx, "echo", nil, &got); err != nil || got != "ok" {
		t.Fatalf("echo: %q %v", got, err)
	}

	callCtx, callCancel := context.WithTimeout(ctx, 5*time.Second)
	defer callCancel()
	done := make(chan error, 1)
	go func() { done <- a.Call(callCtx, "block", nil, nil) }()
	<-h.started
	callCancel()
	if err := <-done; err == nil {
		t.Fatal("expected cancellation error")
	}
}

type panicHandler struct{}

func (panicHandler) Handle(_ context.Context, method string, _ json.RawMessage) (any, error) {
	if method == "boom" {
		var m map[string]int
		m["x"] = 1 // nil map write: panics
	}
	return "ok", nil
}

func (panicHandler) Notify(context.Context, string, json.RawMessage) { panic("notify") }

func TestPanicBecomesInternalError(t *testing.T) {
	aIn, bOut := io.Pipe()
	bIn, aOut := io.Pipe()
	a, b := NewConn(aIn, aOut), NewConn(bIn, bOut)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = b.Serve(ctx, panicHandler{}) }()
	go func() { _ = a.Serve(ctx, panicHandler{}) }()

	_ = a.Notify("explode", nil) // a panicking notification must not end the server
	err := a.Call(ctx, "boom", nil, nil)
	var rpcErr *Error
	if !errors.As(err, &rpcErr) || rpcErr.Code != CodeInternalError || !strings.Contains(rpcErr.Message, "nil map") {
		t.Fatalf("err = %v", err)
	}
	var got string
	if err := a.Call(ctx, "echo", nil, &got); err != nil || got != "ok" {
		t.Fatalf("server did not survive: %q %v", got, err)
	}
}

func TestContentLengthLimit(t *testing.T) {
	r := strings.NewReader("Content-Length: 999999999999\r\n\r\n{}")
	if _, err := NewConn(r, io.Discard).ReadMessage(); err == nil || !strings.Contains(err.Error(), "Content-Length") {
		t.Fatalf("err = %v", err)
	}
}
