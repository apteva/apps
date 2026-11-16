package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// ─── args ───────────────────────────────────────────────────────────

func stringArg(args map[string]any, key string) string {
	switch v := args[key].(type) {
	case string:
		return strings.TrimSpace(v)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case int64:
		return strconv.FormatInt(v, 10)
	case int:
		return strconv.Itoa(v)
	case json.Number:
		return v.String()
	}
	return ""
}

func hasArg(args map[string]any, key string) bool {
	v, ok := args[key]
	return ok && v != nil
}

func floatArg(args map[string]any, key string, def float64) float64 {
	switch v := args[key].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case json.Number:
		f, err := v.Float64()
		if err == nil {
			return f
		}
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err == nil {
			return f
		}
	}
	return def
}

func int64Arg(args map[string]any, key string) int64 {
	return int64(math.Round(floatArg(args, key, 0)))
}

func intArg(args map[string]any, key string, def int) int {
	if !hasArg(args, key) {
		return def
	}
	return int(math.Round(floatArg(args, key, float64(def))))
}

func boolArg(args map[string]any, key string, def bool) bool {
	switch v := args[key].(type) {
	case bool:
		return v
	case string:
		b, err := strconv.ParseBool(v)
		if err == nil {
			return b
		}
	case float64:
		return v != 0
	}
	return def
}

func int64sArg(args map[string]any, key string) []int64 {
	raw, ok := args[key].([]any)
	if !ok {
		if ids, ok := args[key].([]int64); ok {
			return ids
		}
		return nil
	}
	out := make([]int64, 0, len(raw))
	for _, r := range raw {
		switch v := r.(type) {
		case float64:
			out = append(out, int64(v))
		case int64:
			out = append(out, v)
		case int:
			out = append(out, int64(v))
		case string:
			if n, err := strconv.ParseInt(v, 10, 64); err == nil {
				out = append(out, n)
			}
		}
	}
	return out
}

func objectArg(args map[string]any, key string) map[string]any {
	if m, ok := args[key].(map[string]any); ok {
		return m
	}
	if s, ok := args[key].(string); ok && strings.TrimSpace(s) != "" {
		var m map[string]any
		if json.Unmarshal([]byte(s), &m) == nil {
			return m
		}
	}
	return nil
}

func arrayArg(args map[string]any, key string) []any {
	if a, ok := args[key].([]any); ok {
		return a
	}
	if s, ok := args[key].(string); ok && strings.TrimSpace(s) != "" {
		var a []any
		if json.Unmarshal([]byte(s), &a) == nil {
			return a
		}
	}
	return nil
}

// ─── json ───────────────────────────────────────────────────────────

func mustJSON(v any) string {
	if v == nil {
		return "null"
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "null"
	}
	return string(b)
}

func jsonOr(v any, def string) string {
	if v == nil {
		return def
	}
	s := mustJSON(v)
	if s == "null" {
		return def
	}
	return s
}

func decodeInto(raw string, out any) {
	if strings.TrimSpace(raw) == "" {
		return
	}
	_ = json.Unmarshal([]byte(raw), out)
}

// remarshal converts a loosely typed value (map[string]any from tool
// args) into a typed struct.
func remarshal(in any, out any) error {
	b, err := json.Marshal(in)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

// ─── time ───────────────────────────────────────────────────────────

func nowUTC() string { return time.Now().UTC().Format(time.RFC3339) }

var clock = func() time.Time { return time.Now().UTC() }

func parseTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, errors.New("time required")
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04", "2006-01-02 15:04", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid time %q (use RFC3339, e.g. 2026-11-20T14:00:00+01:00)", s)
}

func fmtTime(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func loadLocation(name string) *time.Location {
	if name == "" {
		return time.UTC
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return time.UTC
	}
	return loc
}

// ─── misc ───────────────────────────────────────────────────────────

func notFound(err error, what string) error {
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%s not found", what)
	}
	return err
}

func nullInt(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

func nullFloat(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}

func floatPtrArg(args map[string]any, key string) *float64 {
	if !hasArg(args, key) {
		return nil
	}
	f := floatArg(args, key, 0)
	return &f
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func haversineKm(lat1, lng1, lat2, lng2 float64) float64 {
	const r = 6371.0
	p1, p2 := lat1*math.Pi/180, lat2*math.Pi/180
	dp, dl := (lat2-lat1)*math.Pi/180, (lng2-lng1)*math.Pi/180
	a := math.Sin(dp/2)*math.Sin(dp/2) + math.Cos(p1)*math.Cos(p2)*math.Sin(dl/2)*math.Sin(dl/2)
	return 2 * r * math.Asin(math.Sqrt(a))
}

// ─── JSON-schema helpers for tool definitions ───────────────────────

func schemaObject(props map[string]any, required []string) map[string]any {
	s := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func sString() map[string]any             { return map[string]any{"type": "string"} }
func sStringDesc(d string) map[string]any { return map[string]any{"type": "string", "description": d} }
func sInteger() map[string]any            { return map[string]any{"type": "integer"} }
func sIntDesc(d string) map[string]any    { return map[string]any{"type": "integer", "description": d} }
func sNumber() map[string]any             { return map[string]any{"type": "number"} }
func sBool() map[string]any               { return map[string]any{"type": "boolean"} }
func sObject(d string) map[string]any     { return map[string]any{"type": "object", "description": d} }
func sArray(d string) map[string]any      { return map[string]any{"type": "array", "description": d} }
