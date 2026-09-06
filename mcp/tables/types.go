package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// ─── domain types ──────────────────────────────────────────────────

type Table struct {
	LegacyStorage bool     `json:"-"`
	ID            int64    `json:"id"`
	Name          string   `json:"name"`
	Scope         string   `json:"scope"`
	PhysicalName  string   `json:"-"`
	Columns       []Column `json:"columns"`
	RowCount      int64    `json:"row_count"`
	CreatedAt     string   `json:"created_at,omitempty"`
	RowCountKnown bool     `json:"-"`
}

type Column struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Nullable bool   `json:"nullable"`
	// Default is the JSON-decoded default value, or nil when unset.
	Default any `json:"default,omitempty"`
}

// reservedColumns are the metadata columns every physical table gets;
// the user can't define columns with these names.
var reservedColumns = map[string]bool{
	"id":         true,
	"created_at": true,
	"updated_at": true,
	"_revision":  true,
}

// validColumnTypes is the closed set of types user columns can take.
var validColumnTypes = map[string]bool{
	"text":     true,
	"number":   true,
	"bool":     true,
	"datetime": true,
	"json":     true,
	"file_id":  true,
}

// identifierRe restricts both table and column names. The whole
// generated-SQL safety story rests on this — every name we ever
// inject into a SQL string is validated against this regex first.
var identifierRe = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

func validateIdentifier(kind, name string) error {
	if name == "" {
		return errf("%s name required", kind)
	}
	if len(name) > 64 {
		return errf("%s name too long (max 64 chars): %q", kind, name)
	}
	if !identifierRe.MatchString(name) {
		return errf("%s name must match [a-z][a-z0-9_]*: %q", kind, name)
	}
	return nil
}

// sqliteType maps a column type to its physical sqlite affinity.
func sqliteType(t string) (string, error) {
	switch t {
	case "text", "datetime", "json":
		return "TEXT", nil
	case "number":
		return "REAL", nil
	case "bool", "file_id":
		return "INTEGER", nil
	}
	return "", errf("unknown column type %q", t)
}

// ─── value coercion (insert/update path) ───────────────────────────

func coerceForStorage(col Column, v any) (any, error) {
	if v == nil {
		if !col.Nullable {
			return nil, errf("column %q is not nullable", col.Name)
		}
		return nil, nil
	}
	switch col.Type {
	case "integer":
		return exactInteger(v)
	case "text":
		s, ok := v.(string)
		if !ok {
			return nil, typeMismatch(col, "string", v)
		}
		return s, nil
	case "number":
		switch n := v.(type) {
		case float64:
			if math.IsNaN(n) || math.IsInf(n, 0) {
				return nil, errf("column %q: number must be finite", col.Name)
			}
			return n, nil
		case int:
			return float64(n), nil
		case int64:
			return float64(n), nil
		case json.Number:
			f, err := n.Float64()
			if err != nil {
				return nil, errf("column %q: %w", col.Name, err)
			}
			if math.IsNaN(f) || math.IsInf(f, 0) {
				return nil, errf("column %q: number must be finite", col.Name)
			}
			return f, nil
		}
		return nil, typeMismatch(col, "number", v)
	case "bool":
		b, ok := v.(bool)
		if !ok {
			return nil, typeMismatch(col, "bool", v)
		}
		if b {
			return int64(1), nil
		}
		return int64(0), nil
	case "datetime":
		s, ok := v.(string)
		if !ok {
			return nil, typeMismatch(col, "RFC3339 datetime string", v)
		}
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return nil, errf("column %q: invalid datetime %q: %w", col.Name, s, err)
		}
		return t.UTC().Format(timestampLayout), nil
	case "json":
		b, err := json.Marshal(v)
		if err != nil {
			return nil, errf("column %q: %w", col.Name, err)
		}
		return string(b), nil
	case "file_id":
		if n, err := exactInteger(v); err == nil && n > 0 {
			return n, nil
		}
		return nil, errf("column %q: file_id must be a positive integer", col.Name)

	}
	return nil, errf("unknown column type %q on column %q", col.Type, col.Name)
}

// hydrateForResult converts a sqlite scan value back into a typed
// JSON-friendly value the agent expects.
func hydrateForResult(col Column, raw any) any {
	if raw == nil {
		return nil
	}
	switch col.Type {
	case "text", "datetime":
		if b, ok := raw.([]byte); ok {
			return string(b)
		}
		return raw
	case "number":
		switch n := raw.(type) {
		case float64:
			return n
		case int64:
			return float64(n)
		}
		return raw
	case "bool":
		switch n := raw.(type) {
		case int64:
			return n != 0
		case bool:
			return n
		}
		return raw
	case "json":
		var v any
		var b []byte
		switch n := raw.(type) {
		case []byte:
			b = n
		case string:
			b = []byte(n)
		default:
			return raw
		}
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.UseNumber()
		if err := dec.Decode(&v); err != nil {
			return string(b)
		}
		return v
	case "file_id":
		switch n := raw.(type) {
		case int64:
			return n
		case float64:
			return int64(n)
		}
		return raw
	}
	return raw
}

func typeMismatch(col Column, want string, got any) error {
	return errf("column %q expected %s, got %T", col.Name, want, got)
}

// ─── arg helpers (mirror storage/main.go) ──────────────────────────

func strArg(args map[string]any, key string) string {
	if v, ok := args[key].(string); ok {
		return v
	}
	return ""
}

func boolArg(args map[string]any, key string) bool {
	if v, ok := args[key].(bool); ok {
		return v
	}
	return false
}

func int64Arg(args map[string]any, key string) int64 {
	switch v := args[key].(type) {
	case float64:
		n, err := exactInteger(v)
		if err == nil {
			return n
		}
	case int:
		return int64(v)
	case int64:
		return v
	case string:
		n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		if err == nil {
			return n
		}
	case json.Number:
		n, err := v.Int64()
		if err == nil {
			return n
		}
	}
	return 0
}

func intArg(args map[string]any, key string, def int) int {
	switch v := args[key].(type) {
	case float64:
		n, err := exactInteger(v)
		if err == nil {
			return int(n)
		}
	case int64:
		return int(v)
	case int:
		return v
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err == nil {
			return n
		}
	case json.Number:
		n, err := strconv.Atoi(v.String())
		if err == nil {
			return n
		}
	}
	return def
}

func mapArg(args map[string]any, key string) map[string]any {
	if v, ok := args[key].(map[string]any); ok {
		return v
	}
	return nil
}

func sliceArg(args map[string]any, key string) []any {
	if v, ok := args[key].([]any); ok {
		return v
	}
	return nil
}

// ─── project resolution ────────────────────────────────────────────

func resolveProjectFromArgs(args map[string]any) (string, error) {
	if env := strings.TrimSpace(os.Getenv("APTEVA_PROJECT_ID")); env != "" {
		return env, nil
	}
	if v, ok := args["_project_id"].(string); ok && v != "" {
		return v, nil
	}
	return "", errf("project_id missing — pass _project_id when scope=global")
}

// ─── config getters ────────────────────────────────────────────────

func maxRowsPerTable(ctx *sdk.AppCtx) int64 {
	return cfgInt64Range(ctx, "max_rows_per_table", 1_000_000, 0, 1_000_000_000)
}

func maxQueryRows(ctx *sdk.AppCtx) int {
	return int(cfgInt64Range(ctx, "max_query_rows", 1000, 1, 10_000))
}

func maxQueryMs(ctx *sdk.AppCtx) int {
	return int(cfgInt64Range(ctx, "max_query_ms", 2000, 1, 60_000))
}

func maxReadQueueMs(ctx *sdk.AppCtx) int {
	return int(cfgInt64Range(ctx, "max_read_queue_ms", 1000, 1, 60_000))
}

func maxReadConns(ctx *sdk.AppCtx) int {
	return int(cfgInt64Range(ctx, "max_read_conns", 4, 1, 16))
}

func slowQueryMs(ctx *sdk.AppCtx) int {
	return int(cfgInt64Range(ctx, "slow_query_ms", 250, 1, 60_000))
}

func maxQueryBytes(ctx *sdk.AppCtx) int64 {
	return cfgInt64Range(ctx, "max_query_bytes", 4<<20, 1024, 64<<20)
}

func maxValueBytes(ctx *sdk.AppCtx) int64 {
	return cfgInt64Range(ctx, "max_value_bytes", 1<<20, 1024, 16<<20)
}

func maxBatchRows(ctx *sdk.AppCtx) int {
	return int(cfgInt64Range(ctx, "max_batch_rows", 1000, 1, 10_000))
}

func cfgInt64Range(ctx *sdk.AppCtx, key string, def, min, max int64) int64 {
	v := ctx.Config().Get(key)
	if v == "" {
		return def
	}
	n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	if err != nil || n < min || n > max {
		return def
	}
	return n
}

func queryTimeoutContext(ctx *sdk.AppCtx) (context.Context, context.CancelFunc) {
	return context.WithTimeout(requestContext(ctx), time.Duration(maxQueryMs(ctx))*time.Millisecond)
}

func validateStoredValueSize(ctx *sdk.AppCtx, col Column, v any) error {
	var n int64
	switch x := v.(type) {
	case string:
		n = int64(len(x))
	case []byte:
		n = int64(len(x))
	default:
		return nil
	}
	if n > maxValueBytes(ctx) {
		return errf("column %q exceeds max_value_bytes (%d)", col.Name, maxValueBytes(ctx))
	}
	return nil
}

func valueSize(v any) int64 {
	switch x := v.(type) {
	case nil:
		return 4
	case string:
		return int64(len(x))
	case []byte:
		return int64(len(x))
	case bool:
		return 5
	case int64, float64, int:
		return 24
	default:
		b, err := json.Marshal(x)
		if err != nil {
			return 0
		}
		return int64(len(b))
	}
}

// ─── HTTP small helpers ────────────────────────────────────────────

func httpJSON(w http.ResponseWriter, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, "response encoding failed")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(append(b, '\n'))
}

func httpErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// quote wraps an identifier for safe inline SQL. Only callers that
// have already validated the identifier should use this.
const timestampLayout = "2006-01-02T15:04:05.000000000Z"

func quote(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// strSliceContains is a tiny helper for the keyword-set checks in
// query.go.
func strSliceContains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// jsonStringify marshals a default value to its on-wire JSON form for
// storage in columns_meta.default_value. Used by tables_create and
// tables_alter.
func jsonStringify(v any) (string, error) {
	if v == nil {
		return "", nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("encode default: %w", err)
	}
	return string(b), nil
}

func jsonParse(s string) (any, error) {
	if s == "" {
		return nil, nil
	}
	var v any
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return v, nil
}
