package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	sdk "github.com/apteva/app-sdk"
	"github.com/apteva/apps/mcp/database/engine"
)

func (a *App) handleWatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		w.WriteHeader(405)
		return
	}
	for _, h := range []string{"X-Apteva-Caller-Agent", "X-Apteva-Caller-Instance", "X-Apteva-Subject-ID", sdk.HeaderBoundCallerInstallID} {
		if r.Header.Get(h) != "" {
			httpResult(w, nil, &engine.Error{Code: "permission_denied", Message: "use MCP for scoped caller access"})
			return
		}
	}
	project := r.Header.Get("X-Apteva-Project-ID")
	if project == "" {
		httpResult(w, nil, &engine.Error{Code: "permission_denied", Message: "trusted project header required"})
		return
	}
	if a.manager == nil {
		httpResult(w, nil, &engine.Error{Code: "storage_error", Message: "app is not mounted"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	dec := json.NewDecoder(r.Body)
	dec.UseNumber()
	var args map[string]any
	if e := dec.Decode(&args); e != nil {
		httpResult(w, nil, engine.Invalid("invalid JSON body"))
		return
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		httpResult(w, nil, engine.Invalid("expected one JSON object"))
		return
	}
	req, e := parseArgs(args, toolSpec{op: "changes", args: []string{"collections", "after", "initial"}, required: []string{"collections"}})
	if e != nil {
		httpResult(w, nil, e)
		return
	}
	// Reconnect through the authenticated gateway at least every five minutes.
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	watch, e := a.manager.OpenWatch(ctx, scope(project, nil), req)
	if e != nil {
		httpResult(w, nil, publicWatchError(e))
		return
	}
	defer watch.Close()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	control := http.NewResponseController(w)
	send := func(name string, value any, cursor string) error {
		data, e := json.Marshal(value)
		if e != nil {
			return e
		}
		if len(data) > 5<<20 {
			return engine.Invalid("stream frame exceeds 5 MiB")
		}
		_ = control.SetWriteDeadline(time.Now().Add(30 * time.Second))
		if cursor != "" {
			if _, e = fmt.Fprintf(w, "id: %s\n", cursor); e != nil {
				return e
			}
		}
		if _, e = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, data); e != nil {
			return e
		}
		return control.Flush()
	}
	if req.Initial != nil {
		if send("snapshot", map[string]any{"result": watch.Initial}, "") != nil {
			return
		}
	}
	if send("ready", map[string]any{"cursor": watch.Ready}, watch.Ready) != nil {
		return
	}
	type next struct {
		event engine.WatchEvent
		err   error
	}
	events := make(chan next, 1)
	go func() {
		defer close(events)
		for {
			event, e := watch.Next()
			select {
			case events <- next{event, e}:
			case <-ctx.Done():
				return
			}
			if e != nil || event.Cursor == "" {
				return
			}
		}
	}()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-heartbeat.C:
			_ = control.SetWriteDeadline(time.Now().Add(30 * time.Second))
			if _, e = io.WriteString(w, ": heartbeat\n\n"); e != nil {
				return
			}
			if control.Flush() != nil {
				return
			}
		case item, ok := <-events:
			if !ok {
				return
			}
			if item.err != nil {
				_ = send("error", map[string]any{"error": publicWatchError(item.err)}, "")
				return
			}
			if item.event.Cursor == "" {
				_ = send("closed", map[string]any{"reason": "capture stopped; reconnect"}, "")
				return
			}
			if send(item.event.Type, item.event, item.event.Cursor) != nil {
				return
			}
		}
	}
}
func publicWatchError(e error) error {
	var known *engine.Error
	if errors.As(e, &known) {
		return known
	}
	if errors.Is(e, context.DeadlineExceeded) || errors.Is(e, context.Canceled) {
		return &engine.Error{Code: "resource_limit", Message: "watch deadline exceeded"}
	}
	return &engine.Error{Code: "storage_error", Message: "local watch failed"}
}
