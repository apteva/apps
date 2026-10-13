package main

import (
	"bufio"
	"context"
	"errors"
	"net"
	"net/http"
)

// Preserve native streaming/hijacking. Ownership and credential monitors cancel
// the request; this closes an upgraded socket even while its reader is blocked.
type runtimeSocketResponse struct {
	http.ResponseWriter
	ctx context.Context
}

func (w runtimeSocketResponse) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w runtimeSocketResponse) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
func (w runtimeSocketResponse) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("WebSocket hijacking unavailable")
	}
	c, rw, err := h.Hijack()
	if err != nil {
		return nil, nil, err
	}
	go func() { <-w.ctx.Done(); _ = c.Close() }()
	return c, rw, nil
}
