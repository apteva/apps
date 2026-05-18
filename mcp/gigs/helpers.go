package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ─── Project resolution ─────────────────────────────────────────────
//
// In `scope: project` installs APTEVA_PROJECT_ID is set at boot and
// every call is implicitly on that project. In `scope: global` the
// agent (via the platform's _project_id injection) and the dashboard
// (via ?project_id=) supply the project per call. Mirrors CRM's helpers.

func resolveProjectFromArgs(args map[string]any) (string, error) {
	if pid, _ := args["_project_id"].(string); pid != "" {
		return pid, nil
	}
	if pid := os.Getenv("APTEVA_PROJECT_ID"); pid != "" {
		return pid, nil
	}
	return "", errors.New("_project_id required (no install-scope default available)")
}

func resolveProjectFromRequest(r *http.Request) (string, error) {
	if pid := r.URL.Query().Get("project_id"); pid != "" {
		return pid, nil
	}
	if pid := os.Getenv("APTEVA_PROJECT_ID"); pid != "" {
		return pid, nil
	}
	return "", errors.New("project_id required")
}

// ─── Argument extractors ────────────────────────────────────────────

func strArg(args map[string]any, key string) string {
	if v, ok := args[key].(string); ok {
		return v
	}
	return ""
}

func intArg(args map[string]any, key string, def int) int {
	switch v := args[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case string:
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func int64Arg(args map[string]any, key string) int64 {
	switch v := args[key].(type) {
	case int:
		return int64(v)
	case int64:
		return v
	case float64:
		return int64(v)
	case string:
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
	}
	return 0
}

func boolArg(args map[string]any, key string, def bool) bool {
	switch v := args[key].(type) {
	case bool:
		return v
	case string:
		s := strings.ToLower(v)
		if s == "true" || s == "1" || s == "yes" {
			return true
		}
		if s == "false" || s == "0" || s == "no" {
			return false
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

// ─── JSON Schema helpers ────────────────────────────────────────────

func schemaObject(props map[string]any, required []string) map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": props,
		"required":   required,
	}
}

// ─── HTTP helpers ───────────────────────────────────────────────────

func httpJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func httpErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func httpDecode(r *http.Request, out any) error {
	if r.Body == nil {
		return errors.New("empty body")
	}
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(out)
}

// ─── JSON column round-trip ─────────────────────────────────────────

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "null"
	}
	return string(b)
}

func parseJSON(s string, out any) error {
	if s == "" {
		return nil
	}
	return json.Unmarshal([]byte(s), out)
}

// nullStr returns sql.NullString — empty string becomes NULL.
func nullStr(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

func nullInt64(n int64) sql.NullInt64 {
	if n == 0 {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: n, Valid: true}
}

// ─── Variable interpolation ─────────────────────────────────────────
//
// We support the tiny `{{name}}` form found across the platform — the
// same shape templates_render_preview surfaces. Unknown variables are
// left in place so the operator sees them missing in the preview.

var varRe = regexp.MustCompile(`\{\{\s*([a-zA-Z_][a-zA-Z0-9_]*)\s*\}\}`)

func interpolate(s string, vars map[string]any) string {
	if s == "" || len(vars) == 0 {
		return s
	}
	return varRe.ReplaceAllStringFunc(s, func(m string) string {
		sub := varRe.FindStringSubmatch(m)
		if len(sub) != 2 {
			return m
		}
		v, ok := vars[sub[1]]
		if !ok {
			return m
		}
		return fmt.Sprintf("%v", v)
	})
}

func declaredVariables(s string) []string {
	matches := varRe.FindAllStringSubmatch(s, -1)
	seen := map[string]bool{}
	out := []string{}
	for _, m := range matches {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	return out
}

// ─── Misc ───────────────────────────────────────────────────────────

// atoi is a forgiving string→int — empty/invalid yields 0.
func atoi(s string) int {
	if s == "" {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}

func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	prevDash := false
	for _, r := range s {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			prevDash = false
		case r == '-' || r == '_' || r == ' ':
			if !prevDash && b.Len() > 0 {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	out := strings.TrimRight(b.String(), "-")
	if out == "" {
		out = "untitled"
	}
	return out
}

func randomToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func nowRFC3339() string {
	return time.Now().UTC().Format(time.RFC3339)
}
