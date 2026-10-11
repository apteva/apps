package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	sdk "github.com/apteva/app-sdk"
	"net/http"
	"regexp"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

var errCarrierTelemetryBusy = errors.New("carrier telemetry flush already in progress")

const carrierEvidenceLimit = 2048
const proxyConnectionHeader = "X-Apteva-Proxy-Connection-ID"

// Identifiers are observational only: neither headers nor telemetry authorize a
// call. The proxy must overwrite the incoming header. Missing metadata is valid.
func boundedTransportID(value string) string {
	if len(value) > 128 {
		return ""
	}
	for _, c := range value {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.' || c == ':') {
			return ""
		}
	}
	return value
}
func authenticatedCarrierHeaders(w http.ResponseWriter, r *http.Request, row *callRow) string {
	w.Header().Set("X-Apteva-Call-ID", row.ID)
	return boundedTransportID(r.Header.Get(proxyConnectionHeader))
}

var diagnosticURLPattern = regexp.MustCompile(`(?i)(?:https?|wss?|sips?)://[^\s<>"']+`)

func sanitizeCarrierDetail(row *callRow, value string) string {
	return carrierEvidenceDetail(row, diagnosticURLPattern.ReplaceAllString(value, "[redacted URL]"))
}

type carrierTelemetryRecord struct {
	ID                string                 `json:"id"`
	InstallID         int64                  `json:"install_id"`
	ProjectID         string                 `json:"project_id"`
	CallID            string                 `json:"call_id"`
	Kind              string                 `json:"kind"`
	At                string                 `json:"at"`
	BridgeGeneration  string                 `json:"bridge_generation,omitempty"`
	ProxyConnectionID string                 `json:"proxy_connection_id,omitempty"`
	StreamID          string                 `json:"stream_id,omitempty"`
	RecoveryEpisodeID string                 `json:"recovery_episode_id,omitempty"`
	Event             *carrierBridgeEvidence `json:"event,omitempty"`
	Carrier           *carrierLiveSnapshot   `json:"carrier,omitempty"`
	SIP               *sipLiveSnapshot       `json:"sip,omitempty"`
}

// Enqueue is nonblocking, even while SQLite is unavailable. The single worker
// samples dialogs and persists batches. No payload, SDP or credentials enter it.
type carrierTelemetryCollector struct {
	once        sync.Once
	queue       chan carrierTelemetryRecord
	dropped     atomic.Uint64
	mu          sync.Mutex
	sources     map[string]carrierTelemetrySource
	flushMu     sync.Mutex
	pending     []carrierTelemetryRecord
	lastCleanup time.Time
}

func (c *carrierTelemetryCollector) init() {
	c.once.Do(func() { c.queue = make(chan carrierTelemetryRecord, carrierEvidenceLimit) })
}
func (c *carrierTelemetryCollector) enqueue(r carrierTelemetryRecord) {
	c.init()
	if r.ID == "" {
		r.ID = newCallID()
	}
	if r.At == "" {
		r.At = ringTime(time.Now())
	}
	select {
	case c.queue <- r:
	default:
		c.dropped.Add(1)
	}
}
func (c *carrierTelemetryCollector) register(d *sipLiveDiagnostics) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sources == nil {
		c.sources = map[string]carrierTelemetrySource{}
	}
	if len(c.sources) >= 1024 {
		c.dropped.Add(1)
		return
	}
	c.sources[d.id] = d
}
func (c *carrierTelemetryCollector) unregister(d *sipLiveDiagnostics) {
	c.mu.Lock()
	delete(c.sources, d.id)
	c.mu.Unlock()
	c.enqueue(d.sample(time.Now()))
}
func (c *carrierTelemetryCollector) sample(now time.Time) {
	c.mu.Lock()
	ds := make([]carrierTelemetrySource, 0, len(c.sources))
	for _, d := range c.sources {
		ds = append(ds, d)
	}
	c.mu.Unlock()
	for _, d := range ds {
		c.enqueue(d.sample(now))
	}
}
func (c *carrierTelemetryCollector) flush(ctx context.Context, db *callsDB, now time.Time) (map[string][]string, error) {
	c.init()
	if !c.flushMu.TryLock() {
		return nil, errCarrierTelemetryBusy
	}
	defer c.flushMu.Unlock()
	for len(c.pending) < carrierEvidenceLimit {
		select {
		case r := <-c.queue:
			c.pending = append(c.pending, r)
		default:
			goto drained
		}
	}
drained:
	n := min(len(c.pending), 200)
	changed := map[string][]string{}
	if n == 0 && !c.lastCleanup.IsZero() && now.Sub(c.lastCleanup) < time.Minute {
		return changed, nil
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	tx, err := db.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	calls := map[[2]string]bool{}
	for _, r := range c.pending[:n] {
		raw, e := json.Marshal(r)
		if e != nil {
			return nil, e
		}
		if retainCarrierSample(r) {
			if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO telephony_carrier_transport_history SELECT ?,project_id,id,?,?,?,? FROM calls WHERE project_id=? AND id=?`, r.ID, r.Kind, r.At, ringTime(now.Add(7*24*time.Hour)), string(raw), r.ProjectID, r.CallID); err != nil {
				return nil, err
			}
			calls[[2]string{r.ProjectID, r.CallID}] = true
		}
		if r.SIP != nil || r.Carrier != nil {
			generation := ""
			if r.Carrier != nil {
				generation = r.BridgeGeneration
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO telephony_carrier_transport_latest SELECT project_id,id,?,? FROM calls WHERE project_id=? AND id=? AND (?='' OR media_generation=?) ON CONFLICT(project_id,call_id) DO UPDATE SET occurred_at=excluded.occurred_at,sample_json=excluded.sample_json WHERE excluded.occurred_at>=telephony_carrier_transport_latest.occurred_at`, r.At, string(raw), r.ProjectID, r.CallID, generation, generation); err != nil {
				return nil, err
			}
		}
		changed[r.ProjectID] = append(changed[r.ProjectID], r.CallID)
	}
	for key := range calls {
		for _, kind := range []string{"sample", "event", "incident"} {
			limit := 96
			if kind != "sample" {
				limit = 128
			}
			if _, err = tx.ExecContext(ctx, `DELETE FROM telephony_carrier_transport_history WHERE id IN (SELECT id FROM telephony_carrier_transport_history WHERE project_id=? AND call_id=? AND kind=? ORDER BY occurred_at DESC,id DESC LIMIT -1 OFFSET ?)`, key[0], key[1], kind, limit); err != nil {
				return nil, err
			}
		}
	}
	cleanup := c.lastCleanup.IsZero() || now.Sub(c.lastCleanup) >= time.Minute
	if cleanup {
		if _, err = tx.ExecContext(ctx, `DELETE FROM telephony_carrier_transport_history WHERE id IN (SELECT id FROM telephony_carrier_transport_history WHERE expires_at<=? ORDER BY expires_at LIMIT 200)`, ringTime(now)); err != nil {
			return nil, err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM telephony_carrier_transport_latest WHERE (project_id,call_id) IN (SELECT project_id,call_id FROM telephony_carrier_transport_latest WHERE occurred_at<? ORDER BY occurred_at LIMIT 100)`, ringTime(now.Add(-7*24*time.Hour))); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	clear(c.pending[:n])
	c.pending = append(c.pending[:0], c.pending[n:]...)
	if cleanup {
		c.lastCleanup = now
	}
	return changed, nil
}

// Live state is updated every second. Healthy history is retained every five
// seconds; incidents, events and the final snapshot are always retained.
func retainCarrierSample(r carrierTelemetryRecord) bool {
	if r.Kind != "sample" {
		return true
	}
	if r.SIP != nil && r.SIP.State != "healthy" && r.SIP.State != "inactive" {
		return true
	}
	if r.Carrier != nil && r.Carrier.State != "healthy" {
		return true
	}
	at, err := time.Parse(time.RFC3339Nano, r.At)
	return err != nil || at.Unix()%5 == 0
}

func (a *App) runCarrierTelemetryTick(ctx context.Context, appCtx *sdk.AppCtx) error {
	now := time.Now()
	a.carrierTelemetry.sample(now)
	changed, err := a.carrierTelemetry.flush(ctx, a.db(), now)
	if err != nil {
		appCtx.Logger().Warn("carrier telemetry persistence delayed", "error", err)
		return err
	}
	if n := a.carrierTelemetry.dropped.Swap(0); n > 0 {
		appCtx.Logger().Warn("carrier telemetry collection overflow", "observations_dropped", n)
	}
	if len(changed) == 0 {
		return nil
	}
	return a.db().refreshAudioDashboardAndNotify(ctx, func(projects map[string][]string) {
		for project, ids := range projects {
			appCtx.WithProject(project).Emit("telephony.audio.reports.changed", map[string]any{"call_ids": ids, "occurred_at": ringTime(now)})
		}
	})
}
func (c *callsDB) carrierTransportEvidence(ctx context.Context, project, call string) ([]carrierTelemetryRecord, *carrierTelemetryRecord, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	out := []carrierTelemetryRecord{}
	rows, err := c.db.QueryContext(ctx, `SELECT evidence_json FROM telephony_carrier_transport_history WHERE project_id=? AND call_id=? AND expires_at>? ORDER BY occurred_at DESC,id DESC LIMIT 256`, project, call, ringTime(time.Now()))
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var raw string
		var r carrierTelemetryRecord
		if err = rows.Scan(&raw); err == nil {
			err = json.Unmarshal([]byte(raw), &r)
		}
		if err != nil {
			rows.Close()
			return nil, nil, err
		}
		out = append(out, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, nil, err
	}
	var raw string
	var latest carrierTelemetryRecord
	err = c.db.QueryRowContext(ctx, `SELECT sample_json FROM telephony_carrier_transport_latest WHERE project_id=? AND call_id=? AND occurred_at>?`, project, call, ringTime(time.Now().Add(-7*24*time.Hour))).Scan(&raw)
	if err == sql.ErrNoRows {
		return out, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	if err = json.Unmarshal([]byte(raw), &latest); err != nil {
		return nil, nil, err
	}
	return out, &latest, nil
}

func (a *App) recordCarrierHandshakeRejection(row *callRow, proxyID string, status int, reason string) {
	e := normalizeCarrierEvidence(row, carrierBridgeEvidence{Kind: "handshake_rejected", Trigger: reason, HTTPStatus: status, ProxyConnectionID: proxyID})
	a.carrierTelemetry.enqueue(carrierTelemetryRecord{ID: e.ID, InstallID: a.installID, ProjectID: row.ProjectID, CallID: row.ID, Kind: "event", At: e.At, ProxyConnectionID: proxyID, Event: &e})
}

type carrierCommandObservationKey struct{}
type carrierCommandObservation struct {
	Status    int
	RequestID string
}

type carrierTelemetrySource interface {
	sample(time.Time) carrierTelemetryRecord
}

func (c *carrierTelemetryCollector) registerBridge(b *carrierBridgeLease) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sources == nil {
		c.sources = map[string]carrierTelemetrySource{}
	}
	if len(c.sources) >= 1024 {
		c.dropped.Add(1)
		return
	}
	c.sources[b.generation] = b
}
func (c *carrierTelemetryCollector) unregisterBridge(b *carrierBridgeLease) {
	c.mu.Lock()
	delete(c.sources, b.generation)
	c.mu.Unlock()
	c.enqueue(b.sample(time.Now()))
}

type carrierArrivalTiming struct{ last, frames, maxGap, decodeErrors atomic.Int64 }
type carrierLiveSnapshot struct {
	At             string  `json:"at"`
	State          string  `json:"state"`
	LastMediaAt    string  `json:"last_media_arrival_at,omitempty"`
	LastMediaAgeMS float64 `json:"last_media_age_ms"`
	MaxGapMS       float64 `json:"max_gap_across_generations_ms"`
	Frames         int64   `json:"media_frames"`
	DecodeErrors   int64   `json:"decode_errors"`
}

func (b *carrierBridgeLease) sample(now time.Time) carrierTelemetryRecord {
	v := carrierLiveSnapshot{At: ringTime(now), State: "unobserved", Frames: b.arrival.frames.Load(), MaxGapMS: float64(b.arrival.maxGap.Load()) / 1e6, DecodeErrors: b.arrival.decodeErrors.Load()}
	if n := b.arrival.last.Load(); n > 0 {
		v.LastMediaAt = ringTime(time.Unix(0, n))
		v.LastMediaAgeMS = max(0, float64(now.UnixNano()-n)/1e6)
	}
	if b.flowing.Load() {
		v.State = "healthy"
	}
	if b.ctx.Err() != nil {
		v.State = "disconnected"
	}
	rec := carrierTelemetryRecord{InstallID: b.app.installID, ProjectID: b.row.ProjectID, CallID: b.row.ID, Kind: "sample", At: ringTime(now), BridgeGeneration: b.generation, ProxyConnectionID: b.proxyID, Carrier: &v}
	if b.mu.TryLock() {
		rec.StreamID = b.stream
		rec.RecoveryEpisodeID = b.episodeID
		b.mu.Unlock()
	}
	return rec
}
func carrierRecoveryEpisodes(history []map[string]any) []map[string]any {
	episodes := map[string]int{}
	for _, b := range history {
		id, _ := b["recovery_episode_id"].(string)
		attempts, _ := b["recovery_attempts"].(int)
		if id != "" {
			episodes[id] = max(episodes[id], attempts)
		}
	}
	ids := make([]string, 0, len(episodes))
	for id := range episodes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := []map[string]any{}
	for _, id := range ids {
		out = append(out, map[string]any{"episode_id": id, "attempts": episodes[id]})
	}
	return out
}

// Called only after transport teardown. SDK workers have been canceled; drain
// their remaining evidence best-effort without delaying shutdown on a busy DB.
func (a *App) flushCarrierTelemetryOnShutdown() error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for i := 0; i < 22; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		changed, err := a.carrierTelemetry.flush(ctx, a.db(), time.Now())
		if err != nil {
			return err
		}
		if len(changed) == 0 {
			return nil
		}
	}
	return nil
}
