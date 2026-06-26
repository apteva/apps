package main

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

var envKeyRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func schemaObject(props map[string]any, required []string) map[string]any {
	out := map[string]any{"type": "object"}
	if props != nil {
		out["properties"] = props
	}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

func configInt(ctx *sdk.AppCtx, key string, def int) int {
	if ctx == nil {
		return def
	}
	if v, ok := ctx.Config()[key]; ok {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return n
		}
	}
	return def
}

func configBool(ctx *sdk.AppCtx, key string, def bool) bool {
	if ctx == nil {
		return def
	}
	if v, ok := ctx.Config()[key]; ok {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "1", "true", "yes", "on":
			return true
		case "0", "false", "no", "off":
			return false
		}
	}
	return def
}

func contextProjectID(args map[string]any) string {
	for _, k := range []string{"_project_id", "project_id"} {
		if v, ok := args[k].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func projectIDFromRequest(r *http.Request) string {
	return strings.TrimSpace(r.URL.Query().Get("project_id"))
}

func strArg(args map[string]any, key string) string {
	if args == nil {
		return ""
	}
	if v, ok := args[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
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

func int64Arg(args map[string]any, key string) int64 {
	if args == nil {
		return 0
	}
	switch v := args[key].(type) {
	case int64:
		return v
	case int:
		return int64(v)
	case float64:
		return int64(v)
	case string:
		if n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
			return n
		}
	}
	return 0
}

func parseInt64(s string) int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return n
}

func parseLimit(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	if n <= 0 {
		return 50
	}
	if n > 200 {
		return 200
	}
	return n
}

func nullableInt64(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

func safeEnvKey(k string) bool {
	return envKeyRE.MatchString(k)
}

func truncate(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	return s[:n]
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func httpErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": msg})
}

func emitJob(ctx *sdk.AppCtx, topic string, job *ComputeJob) {
	if ctx == nil || job == nil {
		return
	}
	ctx.EmitWithProject(topic, job.ProjectID, map[string]any{
		"id":             job.ID,
		"owner_app":      job.OwnerApp,
		"owner_ref":      job.OwnerRef,
		"priority":       job.Priority,
		"resource_class": job.ResourceClass,
		"pool":           job.Pool,
		"host_id":        job.HostID,
		"executor":       job.Executor,
		"status":         job.Status,
		"progress_pct":   job.ProgressPct,
	})
}

func emitCurrentJob(ctx *sdk.AppCtx, topic, projectID string, id int64) {
	if ctx == nil {
		return
	}
	job, err := getJob(ctx.AppDB(), projectID, id)
	if err != nil || job == nil {
		return
	}
	emitJob(ctx, topic, job)
}
