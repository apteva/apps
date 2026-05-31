package main

// Auto-capture — the zero-dependency ingest path. A background subscriber
// streams the platform's all-apps firehose (GET /api/app-events/_all) and
// records every emitted event as an analytics row (source="bus"). Apps
// need no dependency on analytics and no code: they already emit on the
// bus for live panels; we just listen.
//
// Off by default (capture_config.enabled). Operators flip it on from the
// panel's Capture tab and shape it with mode + topic patterns + sampling.
// The SSE-consumer shape mirrors the media app's storage subscriber.

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type captureConfig struct {
	Enabled    bool     `json:"enabled"`
	Mode       string   `json:"mode"` // all | denylist | allowlist
	Topics     []string `json:"topic_patterns"`
	SampleRate float64  `json:"sample_rate"`
}

var (
	captureMu     sync.RWMutex
	captureCfg    captureConfig
	capturedTotal atomic.Int64
)

func getCaptureConfig() captureConfig {
	captureMu.RLock()
	defer captureMu.RUnlock()
	return captureCfg
}

func loadCaptureConfig(db *sql.DB) captureConfig {
	var c captureConfig
	var patterns string
	var enabled int
	err := db.QueryRow(
		`SELECT enabled, mode, topic_patterns, sample_rate FROM capture_config WHERE id = 1`,
	).Scan(&enabled, &c.Mode, &patterns, &c.SampleRate)
	if err != nil {
		// No row yet (fresh DB before migration, or error) — safe default.
		return captureConfig{Enabled: false, Mode: "denylist", SampleRate: 1.0}
	}
	c.Enabled = enabled == 1
	if patterns != "" {
		_ = json.Unmarshal([]byte(patterns), &c.Topics)
	}
	if c.SampleRate < 0 || c.SampleRate > 1 {
		c.SampleRate = 1.0
	}
	return c
}

func saveCaptureConfig(db *sql.DB, c captureConfig) error {
	enabled := 0
	if c.Enabled {
		enabled = 1
	}
	if c.Mode != "all" && c.Mode != "allowlist" && c.Mode != "denylist" {
		c.Mode = "denylist"
	}
	if c.SampleRate < 0 || c.SampleRate > 1 {
		c.SampleRate = 1.0
	}
	patterns, _ := json.Marshal(c.Topics)
	_, err := db.Exec(
		`UPDATE capture_config SET enabled=?, mode=?, topic_patterns=?, sample_rate=?, updated_at=? WHERE id=1`,
		enabled, c.Mode, string(patterns), c.SampleRate, time.Now().UnixMilli(),
	)
	if err == nil {
		captureMu.Lock()
		captureCfg = c
		captureMu.Unlock()
	}
	return err
}

// matchTopic reports whether topic matches a pattern: "*" (any), a
// "prefix.*" wildcard, or an exact string.
func matchTopic(topic, pattern string) bool {
	if pattern == "*" {
		return true
	}
	if strings.HasSuffix(pattern, ".*") {
		return strings.HasPrefix(topic, pattern[:len(pattern)-1])
	}
	return topic == pattern
}

// shouldCapture applies the mode + patterns to one topic.
func shouldCapture(topic string, c captureConfig) bool {
	matched := false
	for _, p := range c.Topics {
		if matchTopic(topic, p) {
			matched = true
			break
		}
	}
	switch c.Mode {
	case "allowlist":
		return matched
	case "denylist":
		return !matched
	default: // "all"
		return true
	}
}

// ─── subscriber ───────────────────────────────────────────────────────

func startAutoCapture(ctx *sdk.AppCtx) {
	captureMu.Lock()
	captureCfg = loadCaptureConfig(ctx.AppDB())
	captureMu.Unlock()
	go runAutoCapture(ctx)
	ctx.Logger().Info("analytics auto-capture subscriber started")
}

func runAutoCapture(ctx *sdk.AppCtx) {
	log := ctx.Logger()
	gateway := strings.TrimRight(os.Getenv("APTEVA_GATEWAY_URL"), "/")
	if gateway == "" {
		gateway = strings.TrimRight(os.Getenv("APTEVA_PUBLIC_URL"), "/")
	}
	token := os.Getenv("APTEVA_OUTBOUND_TOKEN")
	if token == "" {
		token = os.Getenv("APTEVA_APP_TOKEN")
	}
	if gateway == "" || token == "" {
		log.Warn("auto-capture: missing APTEVA_GATEWAY_URL or token; subscriber disabled")
		return
	}

	var lastSeq uint64
	backoff := time.Second
	const backoffCap = 30 * time.Second

	for {
		if ctx.Done() != nil {
			select {
			case <-ctx.Done():
				return
			default:
			}
		}
		if !getCaptureConfig().Enabled {
			// Idle: poll config until enabled. Keeps the connection
			// closed (no wasted firehose traffic) while off.
			time.Sleep(5 * time.Second)
			continue
		}
		if err := streamFirehose(ctx, gateway, token, &lastSeq); err != nil {
			log.Warn("auto-capture stream ended", "err", err, "retry_in", backoff)
		}
		time.Sleep(backoff)
		backoff *= 2
		if backoff > backoffCap {
			backoff = backoffCap
		}
	}
}

// streamFirehose opens one SSE connection to the _all lane and records
// matching events until the connection drops or capture is disabled.
func streamFirehose(ctx *sdk.AppCtx, gateway, token string, lastSeq *uint64) error {
	url := gateway + "/api/app-events/_all"
	if *lastSeq > 0 {
		url += "?since=" + strconv.FormatUint(*lastSeq, 10)
	}
	reqCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if ctx.Done() != nil {
		go func() {
			<-ctx.Done()
			cancel()
		}()
	}
	req, err := http.NewRequestWithContext(reqCtx, "GET", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "text/event-stream")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return &httpStatusError{resp.StatusCode}
	}

	reader := bufio.NewReader(resp.Body)
	for {
		if !getCaptureConfig().Enabled {
			return nil // toggled off — disconnect
		}
		line, err := reader.ReadBytes('\n')
		if err != nil {
			return err
		}
		line = bytes.TrimSpace(line)
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue // ping / comment / blank
		}
		payload := bytes.TrimSpace(line[len("data:"):])
		if len(payload) == 0 {
			continue
		}
		var ev struct {
			Topic     string          `json:"topic"`
			App       string          `json:"app"`
			ProjectID string          `json:"project_id"`
			InstallID int64           `json:"install_id"`
			Seq       uint64          `json:"seq"`
			Data      json.RawMessage `json:"data"`
		}
		if json.Unmarshal(payload, &ev) != nil {
			continue
		}
		if ev.Seq > 0 {
			if ev.Seq <= *lastSeq {
				continue // ring-replay overlap
			}
			*lastSeq = ev.Seq
		}
		recordBusEvent(ctx, ev.App, ev.ProjectID, ev.InstallID, ev.Topic, ev.Data)
	}
}

// recordBusEvent applies the capture filters and inserts one row.
func recordBusEvent(ctx *sdk.AppCtx, app, projectID string, installID int64, topic string, data json.RawMessage) {
	if app == "analytics" {
		return // never record our own emissions (event.recorded) — avoids a loop
	}
	cfg := getCaptureConfig()
	if !cfg.Enabled || !shouldCapture(topic, cfg) {
		return
	}
	if cfg.SampleRate < 1.0 && rand.Float64() > cfg.SampleRate {
		return
	}
	if _, err := insertEvent(ctx.AppDB(), EventInsert{
		TS:        time.Now().UnixMilli(),
		App:       app,
		Topic:     topic,
		ProjectID: projectID,
		InstallID: installID,
		Source:    "bus",
		Props:     normalizeBusProps(data),
	}); err == nil {
		capturedTotal.Add(1)
	}
}

// normalizeBusProps keeps props a JSON object: pass objects through, wrap
// scalars/arrays under "value", and map null/empty to {}.
func normalizeBusProps(data json.RawMessage) string {
	t := bytes.TrimSpace(data)
	if len(t) == 0 || string(t) == "null" {
		return "{}"
	}
	if t[0] == '{' {
		return string(t)
	}
	return `{"value":` + string(t) + `}`
}

// ─── HTTP (operator-only) ─────────────────────────────────────────────

// GET /capture — current config + a captured-row count for the project.
func (a *App) handleCaptureGet(w http.ResponseWriter, r *http.Request) {
	if !requireUser(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	cfg := getCaptureConfig()
	var captured int64
	q := "SELECT COUNT(*) FROM events WHERE source = 'bus'"
	var args []any
	if pid := r.URL.Query().Get("project_id"); pid != "" {
		q += " AND project_id = ?"
		args = append(args, pid)
	}
	_ = globalCtx.AppDB().QueryRow(q, args...).Scan(&captured)
	writeJSON(w, map[string]any{
		"enabled":        cfg.Enabled,
		"mode":           cfg.Mode,
		"topic_patterns": cfg.Topics,
		"sample_rate":    cfg.SampleRate,
		"captured":       captured,
	})
}

// POST /capture — update config. Body mirrors captureConfig.
func (a *App) handleCaptureSet(w http.ResponseWriter, r *http.Request) {
	if !requireUser(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var c captureConfig
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if err := saveCaptureConfig(globalCtx.AppDB(), c); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// ─── small helpers ────────────────────────────────────────────────────

type httpStatusError struct{ code int }

func (e *httpStatusError) Error() string { return fmt.Sprintf("firehose http %d", e.code) }
