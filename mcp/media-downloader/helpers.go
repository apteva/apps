package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const (
	statusQueued    = "queued"
	statusRunning   = "running"
	statusCompleted = "completed"
	statusFailed    = "failed"
	statusCanceled  = "canceled"

	stageQueued         = "queued"
	stageProbing        = "probing"
	stageDownloading    = "downloading"
	stagePostprocessing = "postprocessing"
	stagePreparing      = "preparing"
	stageUploading      = "uploading"
	stageTranscribing   = "transcribing"
	stageCompleted      = "completed"
	stageFailed         = "failed"
	stageCanceled       = "canceled"
)

func schemaObj(props map[string]any, required []string) map[string]any {
	out := map[string]any{"type": "object"}
	if props != nil {
		out["properties"] = props
	}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

func nowRFC3339() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func newID(prefix string) (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return prefix + "_" + hex.EncodeToString(b[:]), nil
}

func configString(ctx *sdk.AppCtx, key, def string) string {
	if ctx == nil {
		return def
	}
	if v := strings.TrimSpace(ctx.Config().Get(key)); v != "" {
		return v
	}
	return def
}

func configBool(ctx *sdk.AppCtx, key string, def bool) bool {
	v := strings.TrimSpace(strings.ToLower(configString(ctx, key, "")))
	if v == "" {
		return def
	}
	return v == "1" || v == "true" || v == "yes" || v == "on"
}

func configInt(ctx *sdk.AppCtx, key string, def int) int {
	v := strings.TrimSpace(configString(ctx, key, ""))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return def
	}
	return n
}

func strArg(args map[string]any, key string) string {
	if args == nil {
		return ""
	}
	switch v := args[key].(type) {
	case string:
		return strings.TrimSpace(v)
	case fmt.Stringer:
		return strings.TrimSpace(v.String())
	default:
		return ""
	}
}

func boolArg(args map[string]any, key string, def bool) bool {
	if args == nil {
		return def
	}
	switch v := args[key].(type) {
	case bool:
		return v
	case string:
		s := strings.TrimSpace(strings.ToLower(v))
		if s == "" {
			return def
		}
		return s == "1" || s == "true" || s == "yes" || s == "on"
	}
	return def
}

func intArg(args map[string]any, key string, def int) int {
	if args == nil {
		return def
	}
	switch v := args[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case string:
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return n
		}
	}
	return def
}

func stringSliceArg(args map[string]any, key string) []string {
	if args == nil {
		return nil
	}
	switch v := args[key].(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
		return out
	case string:
		if strings.TrimSpace(v) == "" {
			return nil
		}
		parts := strings.Split(v, ",")
		out := make([]string, 0, len(parts))
		for _, part := range parts {
			if s := strings.TrimSpace(part); s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func projectScope(ctx *sdk.AppCtx, args map[string]any) string {
	if p := strArg(args, "_project_id"); p != "" {
		return p
	}
	if ctx != nil {
		return strings.TrimSpace(ctx.CurrentProject())
	}
	return ""
}

func storageArgs(projectID string, args map[string]any) map[string]any {
	if projectID == "" {
		return args
	}
	cp := make(map[string]any, len(args)+1)
	for k, v := range args {
		cp[k] = v
	}
	if _, ok := cp["_project_id"]; !ok {
		cp["_project_id"] = projectID
	}
	return cp
}

func trimLogLine(s string) string {
	s = strings.TrimSpace(s)
	const max = 1800
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}
