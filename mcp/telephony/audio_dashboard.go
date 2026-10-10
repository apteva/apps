package main

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Values describe observation boundaries, not a diagnosis of the network or carrier.
var audioDashboardIssues = map[string]bool{"concealed_audio": true, "playback_underrun": true, "dropped_audio": true, "sequence_gaps": true, "carrier_stall": true, "browser_error": true, "reconnect": true, "context_suspended": true, "scheduling_pause": true, "write_delay": true, "high_rtt": true, "audio_degraded": true}
var audioDashboardStages = map[string]bool{"carrier_to_telephony": true, "telephony_to_browser": true, "browser_to_telephony": true}

type audioDashboardSummary struct {
	BrowserState string              `json:"browser_state,omitempty"`
	ContextState string              `json:"context_state,omitempty"`
	ObservedAt   string              `json:"observed_at"`
	Telemetry    bool                `json:"telemetry"`
	Issues       []string            `json:"issues"`
	Stages       []string            `json:"stages"`
	Health       audioHealthSnapshot `json:"health"`
	Metrics      map[string]float64  `json:"metrics"`
}
type audioDashboardCall struct {
	ID           string `json:"call_id"`
	Provider     string `json:"provider"`
	Status       string `json:"status"`
	From         string `json:"from"`
	To           string `json:"to"`
	Adviser      string `json:"adviser"`
	AdviserLabel string `json:"adviser_label"`
	Destination  string `json:"destination"`
	Active       bool   `json:"active"`
	State        string `json:"state"`
	audioDashboardSummary
}

func audioDashboardTime(value string) int64 {
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return 0
	}
	return t.UnixMilli()
}
func summarizeAudioDashboard(row *callRow) audioDashboardSummary {
	var b browserAudioDiagnostics
	var c carrierAudioDiagnostics
	_ = json.Unmarshal([]byte(row.BrowserAudioDiagnostics), &b)
	_ = json.Unmarshal([]byte(row.CarrierAudioDiagnostics), &c)
	s := audioDashboardSummary{Issues: []string{}, Stages: []string{}, Metrics: map[string]float64{}, Health: audioHealthSnapshot{Stages: map[string]audioHealthStage{}}}
	observed := max(audioDashboardTime(b.ReceivedAt), audioDashboardTime(c.UpdatedAt))
	s.BrowserState = b.ConnectionState
	s.ContextState = b.AudioContextState
	s.Telemetry = b.ReceivedAt != "" || c.UpdatedAt != "" || b.Server != nil
	issues := map[string]bool{}
	stages := map[string]bool{}
	add := func(issue, stage string, bad bool) {
		if bad {
			issues[issue] = true
			if stage != "" {
				stages[stage] = true
			}
		}
	}
	m := s.Metrics
	m["playback_dropped_ms"] = float64(b.PlaybackDroppedMS)
	if b.MediaTransport != "webrtc" {
		m["playback_underruns"] = float64(b.PlaybackUnderruns)
		m["playback_underrun_ms"] = b.PlaybackUnderrunMS
	}
	m["browser_queue_ms"] = float64(b.PlaybackQueueMS)
	m["browser_max_queue_ms"] = float64(b.PlaybackMaxQueueMS)
	m["playback_sequence_gaps"] = float64(b.PlaybackSequenceGaps)
	m["capture_sequence_gaps"] = float64(b.CaptureSequenceGaps)
	if b.WebRTC != nil {
		m["webrtc_packets_lost"] = b.WebRTC.PacketsLost
		m["webrtc_packets_discarded"] = b.WebRTC.PacketsDiscarded
		if b.WebRTC.ConcealedMS != nil {
			m["webrtc_concealed_ms"] = *b.WebRTC.ConcealedMS
			add("concealed_audio", "telephony_to_browser", *b.WebRTC.ConcealedMS > 0)
		}
		m["webrtc_jitter_buffer_ms"] = b.WebRTC.JitterBufferMS
		add("dropped_audio", "telephony_to_browser", b.WebRTC.PacketsDiscarded > 0)
		add("audio_degraded", "telephony_to_browser", b.WebRTC.JitterBufferMS > 320)
	}
	add("playback_underrun", "telephony_to_browser", b.PlaybackUnderrunMS > 0)
	if b.RTTMS != nil {
		m["rtt_ms"] = float64(*b.RTTMS)
		m["max_rtt_ms"] = float64(*b.RTTMS)
	}
	m["buffered_bytes"] = float64(b.WebSocketBufferedBytes)
	m["carrier_send_dropped_ms"] = float64(c.DroppedStaleMS)
	m["carrier_send_max_queue_ms"] = float64(c.MaxQueuedMS)
	m["carrier_sequence_gaps"] = float64(c.CarrierSequenceGaps)
	add("dropped_audio", "telephony_to_browser", b.PlaybackDroppedMS > 0)
	add("sequence_gaps", "telephony_to_browser", b.PlaybackSequenceGaps > 0)
	add("sequence_gaps", "browser_to_telephony", b.CaptureSequenceGaps > 0)
	add("sequence_gaps", "carrier_to_telephony", c.CarrierSequenceGaps > 0)
	// Carrier send metrics remain a separate fourth boundary, never mislabelled as browser ingress.
	add("dropped_audio", "", c.DroppedStaleMS > 0)
	if b.Timing != nil {
		tr := b.Timing.Transport
		rt := b.Timing.Runtime
		m["capture_dropped_ms"] = tr.CaptureDroppedMS
		m["capture_muted_frames"] = tr.CaptureMutedFrames
		m["capture_muted_ms"] = tr.CaptureMutedMS
		m["browser_transport_dropped_ms"] = tr.PlaybackTransportDroppedMS
		m["browser_source_dropped_ms"] = tr.PlaybackSourceDroppedMS
		m["max_rtt_ms"] = max(m["max_rtt_ms"], tr.RTTMaxMS)
		m["max_buffered_bytes"] = tr.WebSocketMaxBufferedBytes
		m["reconnect_attempts"] = tr.ReconnectAttempts
		m["reconnect_successes"] = tr.ReconnectSuccesses
		m["worker_pauses"] = tr.WorkerPauseCount
		m["worker_max_tick_gap_ms"] = tr.WorkerMaxTickGapMS
		m["main_thread_pauses"] = rt.MainThreadPauseCount
		m["main_thread_max_pause_ms"] = rt.MainThreadMaxPauseMS
		m["context_suspensions"] = rt.AudioContextSuspendCount
		m["context_suspended_ms"] = rt.AudioContextSuspendedMS
		add("dropped_audio", "browser_to_telephony", tr.CaptureDroppedMS > 0)
		add("dropped_audio", "telephony_to_browser", tr.PlaybackTransportDroppedMS+tr.PlaybackSourceDroppedMS > 0)
	}
	if b.Server != nil {
		v := b.Server
		m["server_webrtc_outbound_dropped_ms"] = float64(v.WebRTC.OutboundDroppedMS)
		m["server_webrtc_pacing_skipped_ms"] = float64(v.WebRTC.PacingSkippedMS)
		add("audio_degraded", "telephony_to_browser", v.WebRTC.PacingSkippedMS > 0)
		m["server_webrtc_ingress_rejected_packets"] = float64(v.WebRTC.IngressRejectedPackets + v.WebRTC.IngressQueueDrops + v.WebRTC.DecodeErrors)
		m["server_webrtc_ingress_concealed_ms"] = float64(v.WebRTC.IngressConcealedMS)
		add("concealed_audio", "browser_to_telephony", v.WebRTC.IngressConcealedMS > 0)
		add("dropped_audio", "telephony_to_browser", v.WebRTC.OutboundDroppedMS > 0)
		add("dropped_audio", "browser_to_telephony", m["server_webrtc_ingress_rejected_packets"] > 0)
		observed = max(observed, audioDashboardTime(v.UpdatedAt))
		s.Health = v.Health
		m["connections"] = float64(v.Socket.Connections)
		m["reconnects"] = float64(v.Socket.Reconnects)
		m["disconnects"] = float64(v.Socket.Disconnects)
		m["max_rtt_ms"] = max(m["max_rtt_ms"], v.Socket.MaxRTTMS)
		m["max_buffered_bytes"] = max(m["max_buffered_bytes"], float64(v.Socket.MaxBufferedBytes))
		for k, n := range v.Socket.BrowserTotals {
			switch k {
			case "playback_worklet_dropped_ms":
				m["playback_dropped_ms"] = max(m["playback_dropped_ms"], n)
			case "playback_transport_dropped_ms":
				m["browser_transport_dropped_ms"] = max(m["browser_transport_dropped_ms"], n)
			case "playback_source_dropped_ms":
				m["browser_source_dropped_ms"] = max(m["browser_source_dropped_ms"], n)
			case "capture_worker_dropped_ms":
				m["capture_dropped_ms"] = max(m["capture_dropped_ms"], n)
			case "playback_worklet_sequence_gaps":
				m["playback_sequence_gaps"] = max(m["playback_sequence_gaps"], n)
			case "playback_transport_sequence_gaps":
				m["browser_transport_sequence_gaps"] = max(m["browser_transport_sequence_gaps"], n)
			case "main_thread_pause_count":
				m["main_thread_pauses"] = max(m["main_thread_pauses"], n)
			case "worker_pause_count":
				m["worker_pauses"] = max(m["worker_pauses"], n)
			case "audio_context_suspend_count":
				m["context_suspensions"] = max(m["context_suspensions"], n)
			case "audio_context_suspended_ms":
				m["context_suspended_ms"] = max(m["context_suspended_ms"], n)
			case "playback_underrun_ms", "reconnect_attempts", "reconnect_successes", "capture_muted_ms", "capture_muted_frames":
				m[k] = max(m[k], n)
			}
		}
		add("playback_underrun", "telephony_to_browser", m["playback_underrun_ms"] > 0)
		m["carrier_max_gap_ms"] = max(v.Reception.MaxGapMS, float64(c.InputAudio.MaxGapMS))
		m["carrier_stalls"] = float64(v.Reception.Stalls)
		m["carrier_source_dropped_ms"] = v.Reception.StaleDroppedMS
		m["carrier_max_age_ms"] = v.Reception.MaxExcessAgeMS
		m["carrier_max_batch_ms"] = v.Reception.MaxBatchMS
		m["server_browser_queue_ms"] = float64(v.ToBrowser.QueuedMS)
		m["server_browser_max_queue_ms"] = float64(v.ToBrowser.MaxQueuedMS)
		m["browser_max_write_ms"] = float64(v.ToBrowser.MaxWriteMS)
		m["browser_max_queue_delay_ms"] = float64(v.ToBrowser.MaxResidenceMS)
		m["server_browser_dropped_ms"] = float64(v.ToBrowser.StaleBytes+v.ToBrowser.SourceStaleBytes+v.ToBrowser.OverflowBytes+v.ToBrowser.WriteTimeoutBytes) / 48
		for _, e := range v.CaptureDropEvents {
			if e.BrowserDropTimestamp != "" {
				m["capture_correlated_missing_ms"] += float64(e.DurationMS)
			}
		}
		m["browser_socket_write_timeouts"] = float64(v.ToBrowser.Transport.WriteTimeouts)
		m["browser_socket_write_timeout_dropped_ms"] = float64(v.ToBrowser.WriteTimeoutBytes) / 48
		m["server_capture_dropped_ms"] = float64(v.CaptureStaleBytes) / 48
		m["capture_muted_frames"] = max(m["capture_muted_frames"], float64(v.CaptureMutedFrames))
		m["capture_muted_ms"] = max(m["capture_muted_ms"], float64(v.CaptureMutedMS))
		m["carrier_max_write_ms"] = float64(v.CarrierPacer.MaxWriteMS)
		add("carrier_stall", "carrier_to_telephony", v.Reception.Stalls > 0 || v.Reception.GapsOverBudget > 0)
		add("dropped_audio", "carrier_to_telephony", v.Reception.StaleDroppedMS > 0)
		add("dropped_audio", "telephony_to_browser", m["server_browser_dropped_ms"] > 0)
		add("dropped_audio", "browser_to_telephony", v.CaptureStaleBytes > 0)
		add("write_delay", "telephony_to_browser", v.ToBrowser.MaxWriteMS > 250)
		add("write_delay", "", v.CarrierPacer.MaxWriteMS > 250)
		for stage, h := range v.Health.Stages {
			add("audio_degraded", stage, h.State == "audio_degraded" || h.LastBadAt != "")
		}
		for _, e := range v.Socket.Events {
			add("browser_error", "", audioBrowserCloseError(row, e.Code, e.At, e.ShutdownIntent))
		}
	}
	for _, e := range b.SessionEvents {
		add("browser_error", "", e.Outcome == "audio_error" || e.Outcome == "error" || e.Outcome == "transport_error" || e.Status >= 400 || e.Outcome == "failed" || e.Outcome == "revoked" || e.Outcome == "expired")
	}
	add("browser_error", "", row.MediaErrorMessage != "" || audioBrowserCloseError(row, row.MediaCloseCode, row.MediaDisconnectedAt, ""))
	add("dropped_audio", "telephony_to_browser", m["playback_dropped_ms"]+m["browser_transport_dropped_ms"]+m["browser_source_dropped_ms"] > 0)
	add("dropped_audio", "browser_to_telephony", m["capture_dropped_ms"] > 0)
	add("sequence_gaps", "telephony_to_browser", m["playback_sequence_gaps"]+m["browser_transport_sequence_gaps"] > 0)
	add("reconnect", "", m["reconnects"] > 0 || m["reconnect_attempts"] > 0)
	add("context_suspended", "", m["context_suspensions"] > 0)
	add("scheduling_pause", "", m["worker_pauses"] > 0 || m["main_thread_pauses"] > 0)
	add("high_rtt", "", m["max_rtt_ms"] > 250)
	for k := range issues {
		s.Issues = append(s.Issues, k)
	}
	sort.Strings(s.Issues)
	for k := range stages {
		s.Stages = append(s.Stages, k)
	}
	sort.Strings(s.Stages)
	if observed == 0 {
		observed = audioDashboardTime(row.PlacedAt)
	}
	s.ObservedAt = time.UnixMilli(observed).UTC().Format(time.RFC3339Nano)
	return s
}

// Atomic bounded batch: a simultaneous diagnostic write cannot be lost when
// the queue item is removed. Failure leaves every selected item queued.
func (c *callsDB) refreshAudioDashboard(ctx context.Context) error {
	return c.refreshAudioDashboardAndNotify(ctx, nil)
}

// Notify only after commit, so an SSE refresh sees the indexed diagnostics.
// One hint per project/batch keeps notifications out of the media frame path.
func (c *callsDB) refreshAudioDashboardAndNotify(ctx context.Context, notify func(map[string][]string)) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT call_id FROM telephony_audio_pending ORDER BY rowid LIMIT 100`)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	changed := map[string][]string{}
	for _, id := range ids {
		var r callRow
		err = tx.QueryRowContext(ctx, `SELECT id,project_id,carrier_slug,placed_at,COALESCE(browser_audio_diagnostics,'{}'),COALESCE(carrier_audio_diagnostics,'{}'),COALESCE(media_error_message,''),COALESCE(media_close_code,0),COALESCE(peer_kind,'') FROM calls WHERE id=?`, id).Scan(&r.ID, &r.ProjectID, &r.CarrierSlug, &r.PlacedAt, &r.BrowserAudioDiagnostics, &r.CarrierAudioDiagnostics, &r.MediaErrorMessage, &r.MediaCloseCode, &r.PeerKind)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if errors.Is(err, sql.ErrNoRows) || r.PeerKind != peerKindHuman {
			_, err = tx.ExecContext(ctx, `DELETE FROM telephony_audio_reports WHERE call_id=?`, id)
		} else {
			s := summarizeAudioDashboard(&r)
			raw, e := json.Marshal(s)
			if e != nil {
				return e
			}
			_, err = tx.ExecContext(ctx, `INSERT INTO telephony_audio_reports VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(call_id) DO UPDATE SET project_id=excluded.project_id,observed_ms=excluded.observed_ms,provider=excluded.provider,has_issues=excluded.has_issues,issue_codes=excluded.issue_codes,stage_codes=excluded.stage_codes,summary_json=excluded.summary_json`, id, r.ProjectID, audioDashboardTime(s.ObservedAt), r.CarrierSlug, len(s.Issues) > 0, "|"+strings.Join(s.Issues, "|")+"|", "|"+strings.Join(s.Stages, "|")+"|", string(raw))
		}
		if err != nil {
			return err
		}
		if r.ProjectID != "" {
			changed[r.ProjectID] = append(changed[r.ProjectID], id)
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM telephony_audio_pending WHERE call_id=?`, id); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if notify != nil && len(changed) > 0 {
		notify(changed)
	}
	return nil
}

func (c *callsDB) saveAudioDashboardAlert(alert audioAlert) error {
	raw, err := json.Marshal(alert)
	if err != nil {
		return err
	}
	_, err = c.db.Exec(`INSERT INTO telephony_audio_alert_history(project_id,occurred_ms,provider,stage,payload_json) VALUES(?,?,?,?,?)`, alert.ProjectID, audioDashboardTime(alert.At), alert.Provider, alert.Stage, string(raw))
	return err
}

type audioDashboardFilter struct {
	From, Until                                                 int64
	Provider, Adviser, Destination, Stage, Issue, State, Search string
	Limit                                                       int
	Cursor                                                      string
}

func parseAudioDashboardFilter(r *http.Request, now time.Time) (audioDashboardFilter, error) {
	q := r.URL.Query()
	f := audioDashboardFilter{From: now.Add(-24 * time.Hour).UnixMilli(), Until: now.UnixMilli(), Provider: q.Get("provider"), Adviser: q.Get("adviser"), Destination: q.Get("destination"), Stage: q.Get("stage"), Issue: q.Get("issue"), State: q.Get("state"), Search: strings.TrimSpace(q.Get("search")), Limit: 50, Cursor: q.Get("cursor")}
	for k, p := range map[string]*int64{"from": &f.From, "until": &f.Until} {
		if q.Get(k) != "" {
			t, e := time.Parse(time.RFC3339Nano, q.Get(k))
			if e != nil {
				return f, fmt.Errorf("invalid %s time", k)
			}
			*p = t.UnixMilli()
		}
	}
	if f.From >= f.Until || f.Until-f.From > 31*24*time.Hour.Milliseconds() {
		return f, errors.New("choose a time range of at most 31 days")
	}
	if f.Stage != "" && !audioDashboardStages[f.Stage] || f.Issue != "" && !audioDashboardIssues[f.Issue] {
		return f, errors.New("unknown stage or issue")
	}
	if f.State != "" && f.State != "issues" && f.State != "degraded" && f.State != "active" && f.State != "unobserved" {
		return f, errors.New("unknown state")
	}
	if q.Get("limit") != "" {
		n, e := strconv.Atoi(q.Get("limit"))
		if e != nil || n < 1 || n > 100 {
			return f, errors.New("limit must be 1–100")
		}
		f.Limit = n
	}
	if len(f.Search) > 128 || len(f.Adviser) > 1024 || len(f.Provider) > 64 || len(f.Destination) > 128 {
		return f, errors.New("filter too long")
	}
	if f.Cursor != "" {
		raw, e := base64.RawURLEncoding.DecodeString(f.Cursor)
		var v struct {
			At int64
			ID string
		}
		if e != nil || json.Unmarshal(raw, &v) != nil || v.At < f.From || v.At > f.Until || v.ID == "" || len(v.ID) > 128 {
			return f, errors.New("invalid cursor")
		}
	}
	return f, nil
}

const audioDashboardJoin = ` FROM telephony_audio_reports a JOIN calls c ON c.id=a.call_id AND c.project_id=a.project_id LEFT JOIN telephony_call_owners o ON o.call_id=c.id AND o.project_id=c.project_id LEFT JOIN routing_destinations d ON d.id=COALESCE(NULLIF(o.destination_id,''),c.routing_destination_id) AND d.project_id=c.project_id `
const audioDashboardActive = `(c.ended_at IS NULL OR c.ended_at='') AND c.status NOT IN ('completed','failed','canceled','no-answer','busy')`
const audioDashboardDegraded = `(` + audioDashboardActive + `) AND a.observed_ms>=? AND json_extract(a.summary_json,'$.health.state')='audio_degraded'`

func (f audioDashboardFilter) where(project string, now time.Time) (string, []any) {
	w := `a.project_id=? AND a.observed_ms>=? AND a.observed_ms<=?`
	args := []any{project, f.From, f.Until}
	add := func(s string, v any) { w += " AND " + s; args = append(args, v) }
	if f.Provider != "" {
		add("a.provider=?", f.Provider)
	}
	if f.Adviser == "__unassigned__" {
		w += " AND COALESCE(o.principal,'')=''"
	} else if f.Adviser != "" {
		add("COALESCE(o.principal,'')=?", f.Adviser)
	}
	if f.Destination != "" {
		add("COALESCE(NULLIF(o.destination_id,''),c.routing_destination_id)=?", f.Destination)
	}
	if f.Stage != "" {
		add("instr(a.stage_codes,?)>0", "|"+f.Stage+"|")
	}
	if f.Issue != "" {
		add("instr(a.issue_codes,?)>0", "|"+f.Issue+"|")
	}
	switch f.State {
	case "issues":
		w += " AND a.has_issues=1"
	case "active":
		w += " AND (" + audioDashboardActive + ")"
	case "degraded":
		add(audioDashboardDegraded, now.Add(-20*time.Second).UnixMilli())
	case "unobserved":
		w += " AND json_extract(a.summary_json,'$.telemetry')=0"
	}
	if f.Search != "" {
		w += ` AND (instr(lower(c.id),lower(?))>0 OR instr(c.from_number,?)>0 OR instr(c.to_number,?)>0 OR instr(lower(COALESCE(d.name,'')),lower(?))>0)`
		for range 4 {
			args = append(args, f.Search)
		}
	}
	return w, args
}
func audioDashboardAdviser(principal, name string) string {
	if principal == "" {
		if name != "" {
			return name
		}
		return "Operator / unassigned"
	}
	var p phoneIdentity
	if json.Unmarshal([]byte(principal), &p) == nil && p.SubjectID != "" {
		return strings.TrimSpace(name + " · user " + p.SubjectID + " (" + p.IssuerApp + "/" + p.IssuerInstallID + ")")
	}
	return name
}
func (a *App) handleAudioDashboard(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "method not allowed", 405)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	project, err := a.panelProject(r)
	if err != nil {
		http.Error(w, err.Error(), 403)
		return
	}
	// This project-wide operational view is an operator API; delegated users
	// retain their existing resource-scoped call APIs and cannot enumerate it.
	if p, delegated := phoneRequestIdentity(r); delegated || phoneUserFrom(r) != nil || p.valid() {
		http.Error(w, "operator access required", 403)
		return
	}
	if id := r.URL.Query().Get("call_id"); id != "" {
		row, e := a.db().findCall(id)
		if e != nil {
			http.Error(w, "load diagnostics", 500)
			return
		}
		if row == nil || row.ProjectID != project {
			http.NotFound(w, r)
			return
		}
		network, err := a.db().browserNetworkEvents(r.Context(), project, id, time.Now())
		if err != nil {
			http.Error(w, "load network diagnostics", 500)
			return
		}
		samples, err := a.db().browserTransportSamples(r.Context(), project, id, time.Now())
		if err != nil {
			http.Error(w, "load transport diagnostics", 500)
			return
		}
		bridges, err := a.db().carrierBridgeHistory(project, id)
		if err != nil {
			http.Error(w, "load carrier bridge diagnostics", 500)
			return
		}
		policy, err := a.aiPolicyDiagnostics(project, id)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "load AI policy diagnostics", 500)
			return
		}
		writeJSON(w, map[string]any{"ai_call_policy": policy, "carrier_bridges": bridges, "transport_samples": samples, "browser": audioDiagnosticsPublic(row.BrowserAudioDiagnostics), "carrier": audioDiagnosticsPublic(row.CarrierAudioDiagnostics), "network_events": network})
		return
	}
	now := time.Now()
	f, err := parseAudioDashboardFilter(r, now)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	result, err := a.db().audioDashboard(r.Context(), project, f, now)
	if err != nil {
		http.Error(w, "load audio health", 500)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, result)
}
func (c *callsDB) audioDashboard(ctx context.Context, project string, f audioDashboardFilter, now time.Time) (map[string]any, error) {
	where, args := f.where(project, now)
	totals := map[string]int64{}
	var total, affected, active, degraded, unobserved int64
	err := c.db.QueryRowContext(ctx, `SELECT count(*),COALESCE(sum(a.has_issues),0),COALESCE(sum(CASE WHEN `+audioDashboardActive+` THEN 1 ELSE 0 END),0),COALESCE(sum(CASE WHEN `+audioDashboardDegraded+` THEN 1 ELSE 0 END),0),COALESCE(sum(CASE WHEN json_extract(a.summary_json,'$.telemetry')=0 THEN 1 ELSE 0 END),0)`+audioDashboardJoin+`WHERE `+where, append([]any{now.Add(-20 * time.Second).UnixMilli()}, args...)...).Scan(&total, &affected, &active, &degraded, &unobserved)
	if err != nil {
		return nil, err
	}
	totals["calls"] = total
	totals["affected"] = affected
	totals["active"] = active
	totals["degraded"] = degraded
	totals["unobserved"] = unobserved
	listWhere := where
	listArgs := append([]any{}, args...)
	if f.Cursor != "" {
		raw, e := base64.RawURLEncoding.DecodeString(f.Cursor)
		var cursor struct {
			At int64
			ID string
		}
		if e != nil || json.Unmarshal(raw, &cursor) != nil || cursor.At < f.From || cursor.At > f.Until || len(cursor.ID) > 128 {
			return nil, errors.New("invalid cursor")
		}
		listWhere += " AND (a.observed_ms,a.call_id)<(?,?)"
		listArgs = append(listArgs, cursor.At, cursor.ID)
	}
	rows, err := c.db.QueryContext(ctx, `SELECT a.call_id,a.provider,c.status,c.from_number,c.to_number,COALESCE(o.principal,''),COALESCE(d.name,''),COALESCE(NULLIF(o.destination_id,''),c.routing_destination_id),CASE WHEN `+audioDashboardActive+` THEN 1 ELSE 0 END,a.summary_json,a.observed_ms`+audioDashboardJoin+`WHERE `+listWhere+` ORDER BY a.observed_ms DESC,a.call_id DESC LIMIT ?`, append(listArgs, f.Limit+1)...)
	if err != nil {
		return nil, err
	}
	calls := []audioDashboardCall{}
	var ats []int64
	for rows.Next() {
		var v audioDashboardCall
		var raw string
		var at int64
		var name string
		if err = rows.Scan(&v.ID, &v.Provider, &v.Status, &v.From, &v.To, &v.Adviser, &name, &v.Destination, &v.Active, &raw, &at); err != nil {
			rows.Close()
			return nil, err
		}
		if err = json.Unmarshal([]byte(raw), &v.audioDashboardSummary); err != nil {
			rows.Close()
			return nil, err
		}
		v.AdviserLabel = audioDashboardAdviser(v.Adviser, name)
		v.State = "healthy"
		if v.Health.State == "" {
			v.State = "unavailable"
		}
		if !v.Telemetry {
			v.State = "unobserved"
		} else if !v.Active {
			v.State = "ended"
		} else if at < now.Add(-20*time.Second).UnixMilli() {
			v.State = "stale"
		} else if v.Health.State == "audio_degraded" {
			v.State = "audio_degraded"
		} else if v.BrowserState != "" && v.BrowserState != "connected" || v.ContextState != "" && v.ContextState != "running" {
			v.State = "inactive"
		}
		calls = append(calls, v)
		ats = append(ats, at)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	cursor := ""
	if len(calls) > f.Limit {
		calls = calls[:f.Limit]
		raw, _ := json.Marshal(struct {
			At int64
			ID string
		}{ats[f.Limit-1], calls[f.Limit-1].ID})
		cursor = base64.RawURLEncoding.EncodeToString(raw)
	}
	// Facets use the full time window, independent of the current row filters.
	facets := map[string]any{}
	for _, field := range []string{"provider", "adviser", "destination"} {
		expr := "a.provider"
		if field == "adviser" {
			expr = "COALESCE(o.principal,'')"
		}
		if field == "destination" {
			expr = "COALESCE(NULLIF(o.destination_id,''),c.routing_destination_id)"
		}
		rr, e := c.db.QueryContext(ctx, `SELECT `+expr+`,MAX(COALESCE(d.name,''))`+audioDashboardJoin+`WHERE a.project_id=? AND a.observed_ms BETWEEN ? AND ? GROUP BY `+expr+` ORDER BY `+expr+` LIMIT 500`, project, f.From, f.Until)
		if e != nil {
			return nil, e
		}
		options := []map[string]string{}
		for rr.Next() {
			var key, label string
			if e = rr.Scan(&key, &label); e != nil {
				rr.Close()
				return nil, e
			}
			if key == "" {
				if field == "adviser" {
					options = append(options, map[string]string{"value": "__unassigned__", "label": "Operator / unassigned"})
				}
				continue
			}
			if field == "adviser" {
				label = audioDashboardAdviser(key, label)
			} else if field == "provider" {
				label = key
			}
			options = append(options, map[string]string{"value": key, "label": label})
		}
		e = rr.Err()
		rr.Close()
		if e != nil {
			return nil, e
		}
		facets[field] = options
	}
	alerts := []audioAlert{}
	aw := `project_id=? AND occurred_ms BETWEEN ? AND ?`
	aa := []any{project, f.From, f.Until}
	if f.Provider != "" {
		aw += " AND provider=?"
		aa = append(aa, f.Provider)
	}
	if f.Stage != "" {
		aw += " AND stage=?"
		aa = append(aa, f.Stage)
	}
	ar, err := c.db.QueryContext(ctx, `SELECT payload_json FROM telephony_audio_alert_history WHERE `+aw+` ORDER BY occurred_ms DESC,id DESC LIMIT 20`, aa...)
	if err != nil {
		return nil, err
	}
	for ar.Next() {
		var raw string
		if err = ar.Scan(&raw); err != nil {
			ar.Close()
			return nil, err
		}
		var v audioAlert
		if json.Unmarshal([]byte(raw), &v) == nil {
			alerts = append(alerts, v)
		}
	}
	err = ar.Err()
	ar.Close()
	if err != nil {
		return nil, err
	}
	var pending int
	err = c.db.QueryRowContext(ctx, `SELECT count(*) FROM telephony_audio_pending p JOIN calls c ON c.id=p.call_id WHERE c.project_id=?`, project).Scan(&pending)
	if err != nil {
		return nil, err
	}
	return map[string]any{"calls": calls, "totals": totals, "facets": facets, "alerts": alerts, "next_cursor": cursor, "pending_reports": pending, "generated_at": now.UTC().Format(time.RFC3339Nano), "from": time.UnixMilli(f.From).UTC().Format(time.RFC3339Nano), "until": time.UnixMilli(f.Until).UTC().Format(time.RFC3339Nano)}, nil
}
