package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type rowScanner interface{ Scan(...any) error }

func callAppMap(ctx *sdk.AppCtx, appName, tool string, input map[string]any) (map[string]any, error) {
	if ctx.PlatformAPI() == nil {
		return nil, errors.New("platform API unavailable")
	}
	var out map[string]any
	if err := ctx.PlatformAPI().CallAppResult(appName, tool, input, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func withProject(projectID string, input map[string]any) map[string]any {
	if projectID != "" {
		input["_project_id"] = projectID
	}
	return input
}

func unwrapMap(in map[string]any, key string) map[string]any {
	if out := mapFromAny(in[key]); len(out) > 0 {
		return out
	}
	return in
}

var slugRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

func normalizeSlug(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if !slugRE.MatchString(s) {
		return "", errors.New("slug must be 1-63 chars: lowercase letters, numbers, dashes, starting with a letter/number")
	}
	return s, nil
}

func newID(prefix string) string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return prefix + "_" + strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return prefix + "_" + hex.EncodeToString(b[:])
}

func configString(ctx *sdk.AppCtx, key, fallback string) string {
	if ctx == nil || ctx.Config() == nil {
		return fallback
	}
	if v := strings.TrimSpace(ctx.Config().Get(key)); v != "" {
		return v
	}
	return fallback
}

func schemaObject(props map[string]any, required []string) map[string]any {
	if props == nil {
		props = map[string]any{}
	}
	return map[string]any{"type": "object", "properties": props, "required": required}
}

func tenantIDSchema() map[string]any {
	return schemaObject(map[string]any{"tenant_id": strSchema()}, []string{"tenant_id"})
}

func strSchema() map[string]any   { return map[string]any{"type": "string"} }
func intSchema() map[string]any   { return map[string]any{"type": "integer"} }
func boolSchema() map[string]any  { return map[string]any{"type": "boolean"} }
func objSchema() map[string]any   { return map[string]any{"type": "object"} }
func arraySchema() map[string]any { return map[string]any{"type": "array"} }

func strArg(args map[string]any, key string) string {
	if args == nil {
		return ""
	}
	raw, ok := args[key]
	if !ok || raw == nil {
		return ""
	}
	switch v := raw.(type) {
	case string:
		return strings.TrimSpace(v)
	case fmt.Stringer:
		return strings.TrimSpace(v.String())
	default:
		return strings.TrimSpace(fmt.Sprint(v))
	}
}

func int64Arg(args map[string]any, key string) int64 {
	if args == nil {
		return 0
	}
	switch v := args[key].(type) {
	case int:
		return int64(v)
	case int64:
		return v
	case float64:
		return int64(v)
	case json.Number:
		n, _ := v.Int64()
		return n
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		return n
	default:
		return 0
	}
}

func intArg(args map[string]any, key string, fallback int) int {
	if n := int64Arg(args, key); n > 0 {
		return int(n)
	}
	return fallback
}

func mapArg(args map[string]any, key string) map[string]any {
	out := map[string]any{}
	if args == nil {
		return out
	}
	switch v := args[key].(type) {
	case map[string]any:
		for k, vv := range v {
			out[k] = vv
		}
	case map[string]string:
		for k, vv := range v {
			out[k] = vv
		}
	case json.RawMessage:
		_ = json.Unmarshal(v, &out)
	case []byte:
		_ = json.Unmarshal(v, &out)
	case string:
		if strings.TrimSpace(v) != "" {
			_ = json.Unmarshal([]byte(v), &out)
		}
	}
	return out
}

func mapFromAny(v any) map[string]any {
	out := map[string]any{}
	switch x := v.(type) {
	case map[string]any:
		for k, vv := range x {
			out[k] = vv
		}
	case map[string]string:
		for k, vv := range x {
			out[k] = vv
		}
	case json.RawMessage:
		_ = json.Unmarshal(x, &out)
	case []byte:
		_ = json.Unmarshal(x, &out)
	case string:
		if strings.TrimSpace(x) != "" {
			_ = json.Unmarshal([]byte(x), &out)
		}
	}
	return out
}

func firstPresent(first, second map[string]any, key string) any {
	if first != nil {
		if v, ok := first[key]; ok {
			return v
		}
	}
	if second != nil {
		if v, ok := second[key]; ok {
			return v
		}
	}
	return nil
}

func boolValue(v any, fallback bool) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		switch strings.ToLower(strings.TrimSpace(x)) {
		case "true", "1", "yes", "y", "on":
			return true
		case "false", "0", "no", "n", "off":
			return false
		default:
			return fallback
		}
	case int:
		return x != 0
	case int64:
		return x != 0
	case float64:
		return x != 0
	case nil:
		return fallback
	default:
		return fallback
	}
}

func stringSliceFromAny(v any) []string {
	switch x := v.(type) {
	case nil:
		return nil
	case []string:
		return x
	case []any:
		out := make([]string, 0, len(x))
		for _, item := range x {
			if s := strings.TrimSpace(fmt.Sprint(item)); s != "" {
				out = append(out, s)
			}
		}
		return out
	case string:
		if strings.TrimSpace(x) == "" {
			return nil
		}
		var parsed []string
		if json.Unmarshal([]byte(x), &parsed) == nil {
			return parsed
		}
		parts := strings.Split(x, ",")
		out := make([]string, 0, len(parts))
		for _, part := range parts {
			if s := strings.TrimSpace(part); s != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func arrayFromAny(v any) []any {
	switch x := v.(type) {
	case []any:
		return x
	case []map[string]any:
		out := make([]any, 0, len(x))
		for _, item := range x {
			out = append(out, item)
		}
		return out
	case json.RawMessage:
		var out []any
		_ = json.Unmarshal(x, &out)
		return out
	case []byte:
		var out []any
		_ = json.Unmarshal(x, &out)
		return out
	case string:
		if strings.TrimSpace(x) != "" {
			var out []any
			_ = json.Unmarshal([]byte(x), &out)
			return out
		}
	}
	return nil
}

func mergeMaps(base map[string]any, overlays ...map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range base {
		out[k] = v
	}
	for _, overlay := range overlays {
		for k, v := range overlay {
			out[k] = v
		}
	}
	return out
}

func stringMapArg(args map[string]any, key string) map[string]string {
	out := map[string]string{}
	raw := mapArg(args, key)
	for k, v := range raw {
		if strings.TrimSpace(k) != "" {
			out[k] = strings.TrimSpace(fmt.Sprint(v))
		}
	}
	return out
}

func boolArg(args map[string]any, key string) bool {
	if args == nil {
		return false
	}
	switch v := args[key].(type) {
	case bool:
		return v
	case string:
		return strings.EqualFold(v, "true") || v == "1"
	default:
		return false
	}
}

func boolArgDefault(args map[string]any, key string, fallback bool) bool {
	if args == nil {
		return fallback
	}
	if _, ok := args[key]; !ok {
		return fallback
	}
	return boolArg(args, key)
}

func boolFromAny(v any, fallback bool) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		if strings.TrimSpace(x) == "" {
			return fallback
		}
		return strings.EqualFold(x, "true") || x == "1" || strings.EqualFold(x, "yes")
	case int:
		return x != 0
	case int64:
		return x != 0
	case float64:
		return x != 0
	default:
		return fallback
	}
}

func actor(args map[string]any) string {
	return firstNonEmpty(strArg(args, "actor"), "tool")
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func firstPositive(vals ...int64) int64 {
	for _, v := range vals {
		if v > 0 {
			return v
		}
	}
	return 0
}

func derefInt64(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}

func firstPositiveInt(vals ...int) int {
	for _, v := range vals {
		if v > 0 {
			return v
		}
	}
	return 0
}

func mergeStringMaps(base map[string]string, override map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range override {
		out[k] = v
	}
	return out
}

func mapValueString(vals map[string]any, key string) string {
	v, ok := vals[key]
	if !ok || v == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(v))
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func firstErr(err error, fallback error) error {
	if err != nil {
		return err
	}
	return fallback
}

func nullableInt64(v int64) *int64 {
	if v == 0 {
		return nil
	}
	return &v
}

func nullStr(v string) any {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	return v
}

func jsonOrEmpty(v any, fallback string) string {
	if v == nil {
		return fallback
	}
	b, err := json.Marshal(v)
	if err != nil || len(b) == 0 || string(b) == "null" {
		return fallback
	}
	return string(b)
}

func jsonRaw(v any) json.RawMessage {
	return json.RawMessage(jsonOrEmpty(v, "{}"))
}

func stringOrJSON(raw json.RawMessage, fallback string) string {
	if len(raw) == 0 {
		return fallback
	}
	return string(raw)
}
func strFromAny(v any) string {
	if v == nil {
		return ""
	}
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	case fmt.Stringer:
		return strings.TrimSpace(x.String())
	default:
		return strings.TrimSpace(fmt.Sprint(v))
	}
}
func int64FromAny(v any) int64 {
	switch x := v.(type) {
	case int:
		return int64(x)
	case int64:
		return x
	case int32:
		return int64(x)
	case float64:
		return int64(x)
	case float32:
		return int64(x)
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(x), 10, 64)
		return n
	default:
		return 0
	}
}
