package main

import (
	"encoding/json"
	"fmt"
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
	ID           int64    `json:"id"`
	Name         string   `json:"name"`
	Scope        string   `json:"scope"`
	PhysicalName string   `json:"-"`
	Columns      []Column `json:"columns"`
	RowCount     int64    `json:"row_count"`
	CreatedAt    string   `json:"created_at,omitempty"`
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
	case "text":
		s, ok := v.(string)
		if !ok {
			return nil, typeMismatch(col, "string", v)
		}
		return s, nil
	case "number":
		switch n := v.(type) {
		case float64:
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
		return t.UTC().Format(time.RFC3339), nil
	case "json":
		b, err := json.Marshal(v)
		if err != nil {
			return nil, errf("column %q: %w", col.Name, err)
		}
		return string(b), nil
	case "file_id":
		switch n := v.(type) {
		case float64:
			return int64(n), nil
		case int:
			return int64(n), nil
		case int64:
			return n, nil
		case json.Number:
			i, err := n.Int64()
			if err != nil {
				return nil, errf("column %q: %w", col.Name, err)
			}
			return i, nil
		}
		return nil, typeMismatch(col, "integer file_id", v)
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
		if err := json.Unmarshal(b, &v); err != nil {
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
		return int64(v)
	case int:
		return int64(v)
	case int64:
		return v
	case string:
		n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		if err == nil {
			return n
		}
	}
	return 0
}

func intArg(args map[string]any, key string, def int) int {
	switch v := args[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(v))
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
	return cfgInt64(ctx, "max_rows_per_table", 1_000_000)
}

func maxQueryRows(ctx *sdk.AppCtx) int {
	return int(cfgInt64(ctx, "max_query_rows", 1000))
}

func maxQueryMs(ctx *sdk.AppCtx) int {
	return int(cfgInt64(ctx, "max_query_ms", 2000))
}

func cfgInt64(ctx *sdk.AppCtx, key string, def int64) int64 {
	v := ctx.Config().Get(key)
	if v == "" {
		return def
	}
	n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	if err != nil {
		return def
	}
	return n
}

// ─── HTTP small helpers ────────────────────────────────────────────

func httpJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func httpErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// quote wraps an identifier for safe inline SQL. Only callers that
// have already validated the identifier should use this.
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
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return nil, err
	}
	return v, nil
}
