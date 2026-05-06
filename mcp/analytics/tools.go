package main

// MCP tool handlers. Five tools: track (write) + query / count / top /
// topics (read). Track is the V1 ingest path — auto-capture from the
// platform firehose is a v0.2 feature.

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// toolTrack — record one event. The caller passes the event name and
// optionally an `app` slug, project_id, user/session ids, props, and a
// back-dated ts. We don't currently derive the caller's app from the
// MCP token — the calling install just sends it. Trust-but-verify is
// fine at this scope; analytics is for aggregates, not audit.
func (a *App) toolTrack(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	event := stringArg(args, "event")
	if event == "" {
		return nil, errors.New("event required")
	}

	app := stringArg(args, "app")
	if app == "" {
		app = "_explicit"
	}

	ts := int64Arg(args, "ts")
	if ts == 0 {
		ts = time.Now().UnixMilli()
	}

	propsJSON := "{}"
	if raw, ok := args["props"]; ok && raw != nil {
		// Re-marshal so we store a normalized string. Reject anything
		// that doesn't round-trip — analytics_track gets fed by other
		// apps and a bad payload should fail loudly, not silently.
		b, err := json.Marshal(raw)
		if err != nil {
			return nil, fmt.Errorf("props not JSON-encodable: %w", err)
		}
		// Top-level must be an object — keeps json_extract(props, '$.X')
		// working uniformly. Arrays / scalars get wrapped under "value".
		if len(b) > 0 && b[0] != '{' {
			b, _ = json.Marshal(map[string]any{"value": raw})
		}
		propsJSON = string(b)
	}

	id, err := insertEvent(ctx.AppDB(), EventInsert{
		TS:        ts,
		App:       app,
		Topic:     event,
		ProjectID: stringArg(args, "project_id"),
		InstallID: int64Arg(args, "install_id"),
		UserID:    stringArg(args, "user_id"),
		SessionID: stringArg(args, "session_id"),
		Source:    "track",
		Props:     propsJSON,
	})
	if err != nil {
		return nil, fmt.Errorf("insert: %w", err)
	}
	return map[string]any{"id": id, "ts": ts}, nil
}

// toolQuery — read events. Without group_by, returns the most recent
// rows first. With group_by, returns aggregated buckets sorted by
// count desc.
func (a *App) toolQuery(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	f := filterFromArgs(args)
	limit := intArg(args, "limit")

	if gb, ok := args["group_by"]; ok && gb != nil {
		keys, err := stringSlice(gb)
		if err != nil {
			return nil, fmt.Errorf("group_by: %w", err)
		}
		if len(keys) > 0 {
			buckets, err := queryGrouped(ctx.AppDB(), f, keys, limit)
			if err != nil {
				return nil, err
			}
			return map[string]any{"buckets": buckets}, nil
		}
	}

	rows, err := queryRows(ctx.AppDB(), f, limit)
	if err != nil {
		return nil, err
	}
	return map[string]any{"events": rows, "count": len(rows)}, nil
}

func (a *App) toolCount(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	n, err := countEvents(ctx.AppDB(), filterFromArgs(args))
	if err != nil {
		return nil, err
	}
	return map[string]any{"count": n}, nil
}

func (a *App) toolTop(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	by := stringArg(args, "by")
	if by == "" {
		return nil, errors.New("by required (e.g. \"props.platform\")")
	}
	rows, err := topByPropsKey(ctx.AppDB(), filterFromArgs(args), by, intArg(args, "limit"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"top": rows, "by": by}, nil
}

func (a *App) toolTopics(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	rows, err := listTopics(ctx.AppDB(), stringArg(args, "app"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"topics": rows}, nil
}

// ─── arg helpers ──────────────────────────────────────────────────

func filterFromArgs(args map[string]any) Filter {
	f := Filter{
		App:       stringArg(args, "app"),
		Topic:     stringArg(args, "topic"),
		ProjectID: stringArg(args, "project_id"),
		Since:     int64Arg(args, "since"),
		Until:     int64Arg(args, "until"),
	}
	if w, ok := args["where"].(map[string]any); ok {
		f.Where = w
	}
	return f
}

func stringArg(args map[string]any, name string) string {
	v, ok := args[name]
	if !ok || v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func intArg(args map[string]any, name string) int {
	return int(int64Arg(args, name))
}

func int64Arg(args map[string]any, name string) int64 {
	v, ok := args[name]
	if !ok || v == nil {
		return 0
	}
	switch x := v.(type) {
	case float64:
		return int64(x)
	case int:
		return int64(x)
	case int64:
		return x
	case json.Number:
		n, _ := x.Int64()
		return n
	}
	return 0
}

func stringSlice(v any) ([]string, error) {
	arr, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("expected array, got %T", v)
	}
	out := make([]string, 0, len(arr))
	for i, x := range arr {
		s, ok := x.(string)
		if !ok {
			return nil, fmt.Errorf("[%d] expected string, got %T", i, x)
		}
		out = append(out, s)
	}
	return out, nil
}
