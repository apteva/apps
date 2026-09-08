package main

import (
	"io"
	"net/http"
	"sync"
	"time"
)

// The deadline belongs to body consumption, never the lifetime of a request.
// In particular, net/http starts a background read after EOF; leaving a read
// deadline installed then can poison a reusable HTTP/1 connection's context.
func boundGatewayBody(w http.ResponseWriter, r *http.Request, timeout time.Duration) (http.ResponseWriter, func()) {
	if r.Body == nil || r.Body == http.NoBody || r.ContentLength == 0 {
		return w, func() {}
	}
	body := &deadlineBody{body: http.MaxBytesReader(w, r.Body, maxRequestBytes), controller: http.NewResponseController(w), timeout: timeout, remaining: r.ContentLength}
	r.Body = body
	return &bodyResponseWriter{ResponseWriter: w, body: body, request: r}, body.clear
}

type deadlineBody struct {
	body                  io.ReadCloser
	controller            *http.ResponseController
	timeout               time.Duration
	mu                    sync.Mutex
	started, done, failed bool
	remaining             int64
}

func (b *deadlineBody) start() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.started && !b.done {
		b.started = true
		_ = b.controller.SetReadDeadline(time.Now().Add(b.timeout))
	}
}
func (b *deadlineBody) clear() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.started {
		_ = b.controller.SetReadDeadline(time.Time{})
		b.started = false
	}
}
func (b *deadlineBody) Read(p []byte) (int, error) {
	b.start()
	n, err := b.body.Read(p)
	b.mu.Lock()
	if b.remaining > 0 {
		b.remaining -= int64(n)
	}
	if err != nil || b.remaining == 0 {
		b.done = err == nil || err == io.EOF
		b.failed = !b.done
		_ = b.controller.SetReadDeadline(time.Time{})
		b.started = false
	}
	b.mu.Unlock()
	return n, err
}
func (b *deadlineBody) Close() error {
	// net/http may drain an incompletely read body on Close. Keep that read
	// bounded as well; incomplete requests are never advertised as reusable.
	b.mu.Lock()
	failed := b.failed
	b.mu.Unlock()
	if failed {
		_ = b.controller.SetReadDeadline(time.Now())
	} else {
		b.start()
	}
	err := b.body.Close()
	if failed {
		_ = b.controller.SetReadDeadline(time.Time{})
	}
	b.clear()
	return err
}
func (b *deadlineBody) complete() bool { b.mu.Lock(); defer b.mu.Unlock(); return b.done }

type bodyResponseWriter struct {
	http.ResponseWriter
	body    *deadlineBody
	request *http.Request
	wrote   bool
}

func (w *bodyResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *bodyResponseWriter) WriteHeader(code int) {
	if !w.wrote && code >= 200 {
		w.wrote = true
		if !w.body.complete() {
			w.Header().Set("Connection", "close")
			w.request.Close = true
		}
	}
	w.ResponseWriter.WriteHeader(code)
}
func (w *bodyResponseWriter) Write(p []byte) (int, error) {
	if !w.wrote {
		w.WriteHeader(200)
	}
	return w.ResponseWriter.Write(p)
}
func (w *bodyResponseWriter) FlushError() error {
	if !w.wrote {
		w.WriteHeader(200)
	}
	return http.NewResponseController(w.ResponseWriter).Flush()
}
func (w *bodyResponseWriter) Flush() { _ = w.FlushError() }
