package main

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func nowUTC() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func randomToken(bytes int) (string, error) {
	b := make([]byte, bytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func bytesHash(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func parseRFC3339(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, errors.New("timestamp required")
	}
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("timestamp must be RFC3339: %w", err)
	}
	return t.UTC(), nil
}

func defaultExpiry(ctx *sdk.AppCtx) time.Time {
	days := 14
	if ctx != nil {
		if n, err := strconv.Atoi(strings.TrimSpace(ctx.Config().Get("default_expiry_days"))); err == nil && n >= 1 && n <= 365 {
			days = n
		}
	}
	return time.Now().UTC().Add(time.Duration(days) * 24 * time.Hour)
}

func outputFolder(ctx *sdk.AppCtx) string {
	folder := "/signatures/"
	if ctx != nil && strings.TrimSpace(ctx.Config().Get("output_folder")) != "" {
		folder = strings.TrimSpace(ctx.Config().Get("output_folder"))
	}
	if !strings.HasPrefix(folder, "/") {
		folder = "/" + folder
	}
	if !strings.HasSuffix(folder, "/") {
		folder += "/"
	}
	return folder
}

func publicBaseURL(ctx *sdk.AppCtx) (string, error) {
	if ctx == nil {
		return "", errors.New("app is not mounted")
	}
	if v := strings.TrimSpace(ctx.Config().Get("public_base_url")); v != "" {
		return strings.TrimRight(v, "/"), nil
	}
	info, err := ctx.PlatformInfo()
	if err != nil {
		return "", fmt.Errorf("discover platform public URL: %w", err)
	}
	if info == nil || strings.TrimSpace(info.PublicURL) == "" {
		return "", errors.New("platform public URL is not configured; set public_base_url")
	}
	return strings.TrimRight(info.PublicURL, "/") + "/api/apps/signatures", nil
}

func signingURL(ctx *sdk.AppCtx, token string) (string, error) {
	base, err := publicBaseURL(ctx)
	if err != nil {
		return "", err
	}
	// project_id lets the platform's anonymous no_auth resolver find a
	// project-scoped install; without it the proxy only matches
	// global-scoped installs and the link 401s before reaching the app.
	u := base + "/sign/" + token
	if pid := strings.TrimSpace(ctx.CurrentProject()); pid != "" {
		u += "?project_id=" + url.QueryEscape(pid)
	}
	return u, nil
}

func requestCtx(r *http.Request) *sdk.AppCtx {
	if globalCtx == nil {
		return nil
	}
	if pid := strings.TrimSpace(r.URL.Query().Get("project_id")); pid != "" {
		return globalCtx.WithProject(pid)
	}
	return globalCtx
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		status = http.StatusNotFound
	}
	writeJSON(w, status, map[string]any{"error": err.Error()})
}

func decodeJSON(r *http.Request, out any) error {
	r.Body = http.MaxBytesReader(nil, r.Body, 1<<20)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(out)
}

func stringArg(args map[string]any, key string) string {
	if v, ok := args[key]; ok && v != nil {
		return strings.TrimSpace(fmt.Sprint(v))
	}
	return ""
}

func int64Arg(args map[string]any, key string) int64 {
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
	n := int(int64Arg(args, key))
	if n == 0 {
		return fallback
	}
	return n
}

func boolArg(args map[string]any, key string, fallback bool) bool {
	v, ok := args[key]
	if !ok {
		return fallback
	}
	switch x := v.(type) {
	case bool:
		return x
	case string:
		b, err := strconv.ParseBool(x)
		return err == nil && b
	default:
		return fallback
	}
}

func mapSliceArg(args map[string]any, key string) ([]map[string]any, error) {
	raw, ok := args[key]
	if !ok {
		return nil, fmt.Errorf("%s required", key)
	}
	items, ok := raw.([]any)
	if !ok {
		if typed, ok := raw.([]map[string]any); ok {
			return typed, nil
		}
		return nil, fmt.Errorf("%s must be an array", key)
	}
	out := make([]map[string]any, 0, len(items))
	for i, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s[%d] must be an object", key, i)
		}
		out = append(out, m)
	}
	return out, nil
}

func schemaObject(props map[string]any, required ...string) map[string]any {
	out := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

func sString() map[string]any  { return map[string]any{"type": "string"} }
func sInteger() map[string]any { return map[string]any{"type": "integer"} }
func sNumber() map[string]any  { return map[string]any{"type": "number"} }
func sBool() map[string]any    { return map[string]any{"type": "boolean"} }

func emit(ctx *sdk.AppCtx, topic string, envelope *Envelope, recipientID int64) {
	if ctx == nil || envelope == nil {
		return
	}
	payload := map[string]any{
		"envelope_id": envelope.ID,
		"public_id":   envelope.PublicID,
		"title":       envelope.Title,
		"status":      envelope.Status,
		"occurred_at": nowUTC(),
	}
	if recipientID != 0 {
		payload["recipient_id"] = recipientID
	}
	if envelope.CompletedFileID != 0 {
		payload["completed_file_id"] = envelope.CompletedFileID
	}
	ctx.EmitWithProject(topic, envelope.ProjectID, payload)
}
