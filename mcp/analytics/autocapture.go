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
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
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
	captureMu       sync.RWMutex
	captureCfg      captureConfig
	capturedTotal   atomic.Int64
	captureProjects map[string]captureConfig
	captureStop     context.CancelFunc
	captureDone     chan struct{}
	captureLifetime context.Context
)

func getCaptureConfig() captureConfig {
	captureMu.RLock()
	defer captureMu.RUnlock()
	return captureCfg
}

func loadCaptureConfig(db sqlRunner) captureConfig {
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

func saveCaptureConfig(db sqlRunner, c captureConfig) error {
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
	captureLifetime, captureStop = context.WithCancel(context.Background())
	captureDone = make(chan struct{})
	loadCaptureProjects(ctx.AppDB())
	go func() { defer close(captureDone); runAutoCapture(ctx) }()
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
	_ = ctx.AppDB().QueryRow(`SELECT seq FROM capture_state WHERE id=1`).Scan(&lastSeq)
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
		if !anyCaptureEnabled() {
			// Idle: poll config until enabled. Keeps the connection
			// closed (no wasted firehose traffic) while off.
			if !captureWait(ctx, 5*time.Second) {
				return
			}
			continue
		}
		start := time.Now()
		if err := replayCaptureInbox(ctx); err != nil {
			log.Warn("capture inbox retry", "err", err)
			if !captureWait(ctx, backoff) {
				return
			}
			continue
		}
		if err := streamFirehose(ctx, gateway, token, &lastSeq); err != nil {
			captureError(ctx, err)
			log.Warn("auto-capture stream ended", "err", err, "retry_in", backoff)
		}
		if !captureWait(ctx, backoff) {
			return
		}
		if time.Since(start) > 30*time.Second {
			backoff = time.Second
		} else {
			backoff *= 2
		}
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
	parent := captureLifetime
	if parent == nil {
		parent = context.Background()
	}
	reqCtx, cancel := context.WithCancel(parent)
	defer cancel()
	if ctx.Done() != nil {
		go func() {
			select {
			case <-ctx.Done():
				cancel()
			case <-reqCtx.Done():
			}
		}()
	}
	req, err := http.NewRequestWithContext(reqCtx, "GET", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "text/event-stream")

	resp, err := captureClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return &httpStatusError{resp.StatusCode}
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 4096), 512*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		payload := bytes.TrimSpace(line[5:])
		if len(payload) == 0 {
			continue
		}
		var ev captureEnvelope
		if err := json.Unmarshal(payload, &ev); err != nil {
			return fmt.Errorf("invalid firehose event: %w", err)
		}
		canonical, _ := json.Marshal(ev)
		identity := fmt.Sprintf("%x", sha256.Sum256(canonical))
		var pendingCount int
		if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM capture_inbox WHERE processed_at IS NULL`).Scan(&pendingCount); err != nil {
			return err
		}
		if pendingCount >= 10000 {
			return fmt.Errorf("capture inbox limit reached; ingestion paused")
		}
		// Persist before acknowledging. Replays use the same delivery identity.
		if _, err = ctx.AppDB().Exec(`INSERT OR IGNORE INTO capture_inbox(identity,payload,received_at) VALUES(?,?,?)`, identity, string(payload), time.Now().UnixMilli()); err != nil {
			return err
		}
		if err = processCaptureEnvelope(ctx, identity, ev); err != nil {
			captureError(ctx, err)
			return err
		}
		if ev.Seq > 0 {
			gap := 0
			message := ""
			if *lastSeq > 0 && ev.Seq > *lastSeq+1 {
				gap = 1
				message = "firehose replay gap: events may be missing"
			}
			if ev.Seq < *lastSeq {
				gap = 1
				message = "producer sequence reset or replay overlap; delivery continuity cannot be verified"
			}
			_, err = ctx.AppDB().Exec(`UPDATE capture_state SET seq=?,gaps=gaps+?,last_error=CASE WHEN ?!='' THEN ? ELSE last_error END,updated_at=? WHERE id=1`, ev.Seq, gap, message, message, time.Now().UnixMilli())
			if err != nil {
				return err
			}
			*lastSeq = ev.Seq
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return io.EOF
}

type captureEnvelope struct {
	Topic     string          `json:"topic"`
	App       string          `json:"app"`
	ProjectID string          `json:"project_id"`
	InstallID int64           `json:"install_id"`
	Seq       uint64          `json:"seq"`
	Time      string          `json:"time"`
	ID        string          `json:"id"`
	Epoch     string          `json:"epoch"`
	Data      json.RawMessage `json:"data"`
}

var captureClient = &http.Client{Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, DialContext: (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 15 * time.Second}}

func captureWait(ctx *sdk.AppCtx, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	var stopped <-chan struct{}
	if captureLifetime != nil {
		stopped = captureLifetime.Done()
	}
	select {
	case <-ctx.Done():
		return false
	case <-stopped:
		return false
	case <-timer.C:
		return true
	}
}
func captureError(ctx *sdk.AppCtx, err error) {
	_, _ = ctx.AppDB().Exec(`UPDATE capture_state SET last_error=?,updated_at=? WHERE id=1`, err.Error(), time.Now().UnixMilli())
}

func processCaptureEnvelope(ctx *sdk.AppCtx, identity string, ev captureEnvelope) error {
	var processed any
	if err := ctx.AppDB().QueryRow(`SELECT processed_at FROM capture_inbox WHERE identity=?`, identity).Scan(&processed); err != nil {
		return err
	}
	if processed != nil {
		return nil
	}
	cfg := projectCaptureConfig(ev.ProjectID)
	hash := sha256.Sum256([]byte(identity))
	sample := float64(binary.BigEndian.Uint64(hash[:8])>>11) / float64(uint64(1)<<53)
	if ev.App != "analytics" && cfg.Enabled && shouldCapture(ev.Topic, cfg) && sample < cfg.SampleRate {
		ts := int64(0)
		if ev.Time != "" {
			t, err := time.Parse(time.RFC3339Nano, ev.Time)
			if err != nil {
				return fmt.Errorf("invalid producer timestamp: %w", err)
			}
			ts = t.UnixMilli()
		}
		// For legacy envelopes without time use the durable inbox receipt time.
		if ts == 0 {
			if err := ctx.AppDB().QueryRow(`SELECT received_at FROM capture_inbox WHERE identity=?`, identity).Scan(&ts); err != nil {
				return err
			}
		}
		_, err := insertEvent(ctx.AppDB(), EventInsert{TS: ts, App: ev.App, Topic: ev.Topic, ProjectID: ev.ProjectID, InstallID: ev.InstallID, Source: "bus", Props: normalizeBusProps(ev.Data), DeliveryID: "bus:" + identity})
		if err != nil {
			var rejected *rejectedEventError
			if !errors.As(err, &rejected) {
				return err
			}
		} else {
			capturedTotal.Add(1)
			emitCaptureInvalidation(ctx, ev.ProjectID)
		}
	}
	_, err := ctx.AppDB().Exec(`UPDATE capture_inbox SET processed_at=? WHERE identity=?`, time.Now().UnixMilli(), identity)
	return err
}

func replayCaptureInbox(ctx *sdk.AppCtx) error {
	rows, err := ctx.AppDB().Query(`SELECT identity,payload FROM capture_inbox WHERE processed_at IS NULL ORDER BY received_at LIMIT 256`)
	if err != nil {
		return err
	}
	type pending struct{ id, raw string }
	items := []pending{}
	for rows.Next() {
		var p pending
		if err = rows.Scan(&p.id, &p.raw); err != nil {
			rows.Close()
			return err
		}
		items = append(items, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, p := range items {
		var ev captureEnvelope
		if err = json.Unmarshal([]byte(p.raw), &ev); err != nil {
			return err
		}
		if err = processCaptureEnvelope(ctx, p.id, ev); err != nil {
			return err
		}
	}
	return nil
}

var captureEmits = struct {
	sync.Mutex
	last map[string]time.Time
}{last: map[string]time.Time{}}

func emitCaptureInvalidation(ctx *sdk.AppCtx, project string) {
	captureEmits.Lock()
	now := time.Now()
	last := captureEmits.last[project]
	if now.Sub(last) < time.Second {
		captureEmits.Unlock()
		return
	}
	if len(captureEmits.last) > 4096 {
		for key, t := range captureEmits.last {
			if now.Sub(t) > time.Minute {
				delete(captureEmits.last, key)
			}
		}
	}
	captureEmits.last[project] = now
	captureEmits.Unlock()
	ctx.EmitWithProject("event.recorded", project, map[string]any{"source": "bus"})
}

func recordBusEvent(ctx *sdk.AppCtx, app, projectID string, installID int64, topic string, data json.RawMessage) error {
	ev := captureEnvelope{App: app, ProjectID: projectID, InstallID: installID, Topic: topic, Data: data, Time: time.Now().UTC().Format(time.RFC3339Nano)}
	raw, _ := json.Marshal(ev)
	id := fmt.Sprintf("%x", sha256.Sum256(raw))
	if _, err := ctx.AppDB().Exec(`INSERT INTO capture_inbox(identity,payload,received_at) VALUES(?,?,?)`, id, string(raw), time.Now().UnixMilli()); err != nil {
		return err
	}
	return processCaptureEnvelope(ctx, id, ev)
}

func loadCaptureProjects(db sqlRunner) {
	projects := map[string]captureConfig{}
	rows, err := db.Query(`SELECT project_id,config FROM capture_projects`)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var id, raw string
		var c captureConfig
		if rows.Scan(&id, &raw) == nil && json.Unmarshal([]byte(raw), &c) == nil {
			projects[id] = c
		}
	}
	captureMu.Lock()
	captureProjects = projects
	captureMu.Unlock()
}
func projectCaptureConfig(project string) captureConfig {
	captureMu.RLock()
	defer captureMu.RUnlock()
	if captureProjects == nil {
		return captureCfg
	}
	return captureProjects[project]
}
func anyCaptureEnabled() bool {
	captureMu.RLock()
	defer captureMu.RUnlock()
	if captureProjects == nil {
		return captureCfg.Enabled
	}
	for _, c := range captureProjects {
		if c.Enabled {
			return true
		}
	}
	return false
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
	projectID, ok := requireRequestProject(w, r)
	if !ok {
		return
	}
	cfg := projectCaptureConfig(projectID)
	var captured int64
	q := "SELECT COUNT(*) FROM events WHERE source = 'bus'"
	var args []any
	if pid := projectID; pid != "" {
		q += " AND project_id = ?"
		args = append(args, pid)
	}
	_ = globalCtx.AppDB().QueryRow(q, args...).Scan(&captured)
	var gaps int64
	var lastError string
	_ = globalCtx.AppDB().QueryRow(`SELECT gaps,last_error FROM capture_state WHERE id=1`).Scan(&gaps, &lastError)
	writeJSON(w, map[string]any{
		"scope": "project", "delivery": "best_effort_with_durable_inbox", "gaps": gaps, "last_error": lastError,
		"continuity":     "The platform firehose has a finite replay buffer. Offline gaps cannot always be recovered.",
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
	projectID, ok := requireRequestProject(w, r)
	if !ok {
		return
	}
	var c captureConfig
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if !stringIn(c.Mode, []string{"all", "allowlist", "denylist"}) || c.SampleRate < 0 || c.SampleRate > 1 || len(c.Topics) > 100 {
		http.Error(w, "invalid capture policy", http.StatusBadRequest)
		return
	}
	raw, _ := json.Marshal(c)
	if _, err := globalCtx.AppDB().Exec(`INSERT INTO capture_projects VALUES(?,?) ON CONFLICT(project_id) DO UPDATE SET config=excluded.config`, projectID, string(raw)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	loadCaptureProjects(globalCtx.AppDB())
	writeJSON(w, map[string]any{"ok": true})
}

// ─── small helpers ────────────────────────────────────────────────────

type httpStatusError struct{ code int }

func (e *httpStatusError) Error() string { return fmt.Sprintf("firehose http %d", e.code) }
