// helpers.go — small utility funcs shared by main.go / broker /
// hooks / handlers. Kept separate so the bigger files stay focused.

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// ─── config helpers ─────────────────────────────────────────────────

func configString(ctx *sdk.AppCtx, key, def string) string {
	if ctx == nil {
		return def
	}
	if v, ok := ctx.Config()[key]; ok && v != "" {
		return v
	}
	return def
}

func configInt(ctx *sdk.AppCtx, key string, def int) int {
	s := configString(ctx, key, "")
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

func clampConfigInt(ctx *sdk.AppCtx, key string, def, min, max int) int {
	n := configInt(ctx, key, def)
	if n < min {
		return min
	}
	if n > max {
		return max
	}
	return n
}

func configFlag(ctx *sdk.AppCtx, key string, def bool) bool {
	s := strings.ToLower(configString(ctx, key, ""))
	switch s {
	case "true", "1", "yes":
		return true
	case "false", "0", "no":
		return false
	}
	return def
}

func encodePayload(value any, present bool) ([]byte, error) {
	switch value := value.(type) {
	case string:
		return []byte(value), nil
	case []byte:
		return append([]byte(nil), value...), nil
	case nil:
		if present {
			return []byte("null"), nil
		}
		return nil, nil
	default:
		return json.Marshal(value)
	}
}

// ─── project scope ──────────────────────────────────────────────────

// projectScope is used only after the HTTP/MCP boundary has resolved a
// trusted owner. There is deliberately no shared default scope.
func projectScope(ctx *sdk.AppCtx) string { return ctx.CurrentProject() }

// ─── HTTP helpers ───────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

func httpErr(w http.ResponseWriter, code int, msg string) {
	http.Error(w, msg, code)
}

// ─── retention sweep ────────────────────────────────────────────────

// runRetentionSweep prunes mqtt_acl_log + mqtt_message_log on a
// schedule. Cheap because the rows are small and indexed on ts.
func (a *App) runRetentionSweep(ctx context.Context) error {
	t := time.NewTicker(15 * time.Minute)
	defer t.Stop()
	a.sweepOnce()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			a.sweepOnce()
		}
	}
}

func (a *App) sweepOnce() {
	hours := configInt(a.ctx, "message_log_retention_hours", 168)
	if hours <= 0 {
		return
	}
	cutoff := time.Now().UTC().Add(-time.Duration(hours) * time.Hour).Format(time.RFC3339)
	db := a.ctx.AppDB()
	_, _ = db.Exec(`DELETE FROM mqtt_acl_log WHERE ts < ?`, cutoff)
	_, _ = db.Exec(`DELETE FROM mqtt_message_log WHERE ts < ?`, cutoff)

	maxRetain := configInt(a.ctx, "retain_max_count", 10000)
	if maxRetain > 0 {
		rows, err := db.Query(
			`SELECT topic FROM (
 SELECT topic, ROW_NUMBER() OVER (PARTITION BY scope_id ORDER BY updated_at DESC, topic) AS position
 FROM mqtt_retained WHERE scope_id != ''
 ) WHERE position > ?`, maxRetain)
		if err == nil {
			topics := []string{}
			for rows.Next() {
				var topic string
				if rows.Scan(&topic) == nil {
					topics = append(topics, topic)
				}
			}
			rows.Close()
			for _, topic := range topics {
				if a.broker != nil {
					_ = a.broker.DeleteRetained(topic)
				}
				_, _ = db.Exec(`DELETE FROM mqtt_retained WHERE topic = ?`, topic)
			}
		}
	}
}
