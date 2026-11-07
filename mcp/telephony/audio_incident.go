package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"slices"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// One read-only answer to "what failed first, for whom, and when did it come
// back?" across a project. Read on request only, from events already stored for
// each connection, so this adds nothing to a live call.
const (
	audioIncidentDefaultWindow = 15 * time.Minute
	audioIncidentMaxWindow     = 6 * time.Hour
	audioIncidentRowLimit      = 5000
	audioIncidentHealthyLimit  = 200
	// Reconnections and late recovery records belong to a fault in the window.
	audioIncidentLookAhead = 2 * time.Minute
)

type audioIncidentCall struct {
	CallID         string               `json:"call_id"`
	Adviser        string               `json:"adviser,omitempty"`
	DisconnectedAt string               `json:"disconnected_at"`
	FirstFailed    string               `json:"first_failed"`
	AudioAgeMS     *int64               `json:"audio_age_ms,omitempty"`
	SignalingAgeMS *int64               `json:"signaling_age_ms,omitempty"`
	ErrorClass     string               `json:"error_class,omitempty"`
	ICEState       string               `json:"ice_state,omitempty"`
	ReconnectedAt  string               `json:"reconnected_at,omitempty"`
	ReconnectMS    *int64               `json:"reconnect_ms,omitempty"`
	Disconnects    int                  `json:"disconnects"`
	FailedAttempts int                  `json:"failed_attempts"`
	Recovery       *audioRecoveryRecord `json:"recovery,omitempty"`
}
type audioIncidentHealthyCall struct {
	CallID  string `json:"call_id"`
	Adviser string `json:"adviser,omitempty"`
}
type audioIncidentSummary struct {
	AffectedCalls  int            `json:"affected_calls"`
	Reconnected    int            `json:"reconnected"`
	NotReconnected int            `json:"not_reconnected"`
	HealthyCalls   int            `json:"healthy_calls"`
	FirstFailed    map[string]int `json:"first_failed"`
	MedianMS       int64          `json:"median_reconnect_ms,omitempty"`
	LongestMS      int64          `json:"longest_reconnect_ms,omitempty"`
	FirstAt        string         `json:"first_disconnect_at,omitempty"`
	LastAt         string         `json:"last_disconnect_at,omitempty"`
}
type audioIncidentReport struct {
	From      string                     `json:"from"`
	To        string                     `json:"to"`
	Reading   string                     `json:"reading"`
	Summary   audioIncidentSummary       `json:"summary"`
	Calls     []audioIncidentCall        `json:"calls"`
	Healthy   []audioIncidentHealthyCall `json:"healthy_calls"`
	Alerts    []audioAlert               `json:"alerts"`
	Probes    []audioProbeRound          `json:"network_checks"`
	Truncated bool                       `json:"truncated"`
}

func parseAudioIncidentWindow(fromText, toText string, now time.Time) (time.Time, time.Time, error) {
	to, from := now, time.Time{}
	var err error
	if toText != "" {
		if to, err = time.Parse(time.RFC3339Nano, toText); err != nil {
			return from, to, errors.New("to must be an RFC 3339 time")
		}
	}
	from = to.Add(-audioIncidentDefaultWindow)
	if fromText != "" {
		if from, err = time.Parse(time.RFC3339Nano, fromText); err != nil {
			return from, to, errors.New("from must be an RFC 3339 time")
		}
	}
	if !from.Before(to) {
		return from, to, errors.New("from must be before to")
	}
	if to.Sub(from) > audioIncidentMaxWindow {
		return from, to, errors.New("the window can be at most 6 hours")
	}
	return from.UTC(), to.UTC(), nil
}

// Faults only. A browser that hung up, or a session replaced on purpose, is not one.
func audioDisconnectUnplanned(e audioNetworkEvent) bool {
	if e.Event == "softphone.browser.signaling_lost" {
		return true
	}
	if e.Event != "softphone.browser.disconnected" || (e.ShutdownIntent != "" && e.ShutdownIntent != "audio_error") {
		return false
	}
	return e.Reason == "transport_read_error" || e.Reason == "peer_close"
}

func buildAudioIncidentCalls(events []audioNetworkEvent, from, to time.Time) []audioIncidentCall {
	byCall := map[string][]audioNetworkEvent{}
	for _, e := range events {
		byCall[e.CallID] = append(byCall[e.CallID], e)
	}
	at := func(e audioNetworkEvent) time.Time {
		t, _ := time.Parse(time.RFC3339Nano, e.OccurredAt)
		return t
	}
	calls := []audioIncidentCall{}
	for id, list := range byCall {
		sort.SliceStable(list, func(i, j int) bool { return at(list[i]).Before(at(list[j])) })
		var row *audioIncidentCall
		var first time.Time
		for _, e := range list {
			when := at(e)
			if audioDisconnectUnplanned(e) && !when.Before(from) && when.Before(to) {
				if row == nil {
					cause := audioDisconnectCauseAt(e.Disconnect, when)
					row = &audioIncidentCall{CallID: id, Adviser: e.AdviserIdentity.SubjectID, DisconnectedAt: e.OccurredAt, FirstFailed: cause.FirstFailed, AudioAgeMS: cause.AudioAgeMS, SignalingAgeMS: cause.SignalingAgeMS}
					if e.Disconnect != nil {
						row.ErrorClass, row.ICEState = e.Disconnect.ErrorClass, e.Disconnect.ICEState
					}
					// Audio was checked and still flowing, so this is certain.
					if e.Event == "softphone.browser.signaling_lost" {
						row.FirstFailed = "signaling"
					}
					first = when
				}
				row.Disconnects++
				continue
			}
			if row == nil {
				continue
			}
			switch e.Event {
			case "softphone.browser.connected":
				if row.ReconnectedAt == "" {
					ms := when.Sub(first).Milliseconds()
					row.ReconnectedAt, row.ReconnectMS = e.OccurredAt, &ms
				}
			case "softphone.browser.setup_failed":
				if row.ReconnectedAt == "" {
					row.FailedAttempts++
				}
			case "softphone.browser.recovery":
				if row.Recovery == nil {
					row.Recovery = e.Recovery
				}
			}
		}
		if row != nil {
			calls = append(calls, *row)
		}
	}
	sort.Slice(calls, func(i, j int) bool { return calls[i].DisconnectedAt < calls[j].DisconnectedAt })
	return calls
}

func summarizeAudioIncident(calls []audioIncidentCall, healthy int) audioIncidentSummary {
	s := audioIncidentSummary{AffectedCalls: len(calls), HealthyCalls: healthy, FirstFailed: map[string]int{}}
	times := []int64{}
	for _, c := range calls {
		s.FirstFailed[c.FirstFailed]++
		if c.ReconnectMS == nil {
			s.NotReconnected++
		} else {
			s.Reconnected++
			times = append(times, *c.ReconnectMS)
		}
	}
	if len(times) > 0 {
		slices.Sort(times)
		s.MedianMS, s.LongestMS = times[len(times)/2], times[len(times)-1]
	}
	if len(calls) > 0 {
		s.FirstAt, s.LastAt = calls[0].DisconnectedAt, calls[len(calls)-1].DisconnectedAt
	}
	return s
}

func readAudioIncident(s audioIncidentSummary, probes []audioProbeRound) string {
	if s.AffectedCalls == 0 {
		return "No browser audio connection dropped unexpectedly in this window."
	}
	parts := []string{}
	for _, item := range []struct{ key, label string }{{"signaling", "signaling only"}, {"audio", "audio only"}, {"both", "both signaling and audio"}, {"neither", "neither (both were still active)"}, {"unknown", "not recorded"}} {
		if n := s.FirstFailed[item.key]; n > 0 {
			parts = append(parts, fmt.Sprintf("%s on %d", item.label, n))
		}
	}
	text := fmt.Sprintf("%d of %d active browser calls dropped; %d reconnected, %d did not. Stopped first: %s.", s.AffectedCalls, s.AffectedCalls+s.HealthyCalls, s.Reconnected, s.NotReconnected, strings.Join(parts, ", "))
	if s.LongestMS > 0 {
		text += fmt.Sprintf(" Reconnecting took %.1f s at the median and %.1f s at most.", float64(s.MedianMS)/1000, float64(s.LongestMS)/1000)
	}
	failed, total := 0, 0
	for _, round := range probes {
		for _, r := range round.Results {
			total++
			if !r.OK {
				failed++
			}
		}
	}
	if total > 0 {
		text += fmt.Sprintf(" Network checks from this server during the burst: %d of %d failed.", failed, total)
	}
	return text
}

func (c *callsDB) audioIncidentEvents(ctx context.Context, project string, from, to, now time.Time) ([]audioNetworkEvent, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	rows, err := c.db.QueryContext(ctx, `SELECT event_json FROM telephony_browser_network_events WHERE project_id=? AND occurred_at>=? AND occurred_at<? AND expires_at>? AND (id LIKE '%:disconnected' OR id LIKE '%:connected' OR id LIKE '%:setup_failed' OR id LIKE '%:recovery' OR id LIKE '%:signaling_lost') ORDER BY occurred_at LIMIT ?`,
		project, audioNetworkTimestamp(from), audioNetworkTimestamp(to.Add(audioIncidentLookAhead)), audioNetworkTimestamp(now), audioIncidentRowLimit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	events := []audioNetworkEvent{}
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err != nil {
			return nil, false, err
		}
		var e audioNetworkEvent
		if json.Unmarshal([]byte(raw), &e) == nil {
			events = append(events, e)
		}
	}
	truncated := len(events) > audioIncidentRowLimit
	if truncated {
		events = events[:audioIncidentRowLimit]
	}
	return events, truncated, rows.Err()
}

// Calls with a browser attached at some point in the window and no fault in it.
func (c *callsDB) audioIncidentHealthyCalls(ctx context.Context, project string, from, to time.Time, affected map[string]bool) ([]audioIncidentHealthyCall, int, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	// A call can last at most 4 hours (connected_call_max_duration_seconds).
	rows, err := c.db.QueryContext(ctx, `SELECT calls.id,COALESCE(calls.answered_at,''),COALESCE(calls.ended_at,''),COALESCE(o.principal,'') FROM calls LEFT JOIN telephony_call_owners o ON o.call_id=calls.id WHERE calls.project_id=? AND unixepoch(calls.placed_at)>=? AND unixepoch(calls.placed_at)<? AND calls.peer_kind=? AND COALESCE(calls.answered_at,'')<>''`,
		project, from.Add(-4*time.Hour-time.Minute).Unix(), to.Unix(), peerKindHuman)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	list := []audioIncidentHealthyCall{}
	count := 0
	for rows.Next() {
		var id, answered, ended, principal string
		if err = rows.Scan(&id, &answered, &ended, &principal); err != nil {
			return nil, 0, err
		}
		answeredAt, e := time.Parse(time.RFC3339Nano, answered)
		if e != nil || !answeredAt.Before(to) || affected[id] {
			continue
		}
		if endedAt, e := time.Parse(time.RFC3339Nano, ended); e == nil && !endedAt.After(from) {
			continue
		}
		count++
		if len(list) < audioIncidentHealthyLimit {
			row := audioIncidentHealthyCall{CallID: id}
			var identity phoneIdentity
			if json.Unmarshal([]byte(principal), &identity) == nil {
				row.Adviser = identity.SubjectID
			}
			list = append(list, row)
		}
	}
	return list, count, rows.Err()
}

func (c *callsDB) audioIncidentAlerts(ctx context.Context, project string, from, to time.Time) ([]audioAlert, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	rows, err := c.db.QueryContext(ctx, `SELECT payload_json FROM telephony_audio_alert_history WHERE project_id=? AND occurred_ms>=? AND occurred_ms<? ORDER BY occurred_ms LIMIT 100`, project, from.UnixMilli(), to.Add(audioIncidentLookAhead).UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	alerts := []audioAlert{}
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		var alert audioAlert
		if json.Unmarshal([]byte(raw), &alert) == nil {
			alerts = append(alerts, alert)
		}
	}
	return alerts, rows.Err()
}

func (a *App) audioIncident(ctx context.Context, project string, from, to, now time.Time) (audioIncidentReport, error) {
	report := audioIncidentReport{From: from.Format(time.RFC3339Nano), To: to.Format(time.RFC3339Nano)}
	events, truncated, err := a.db().audioIncidentEvents(ctx, project, from, to, now)
	if err != nil {
		return report, err
	}
	report.Truncated = truncated
	report.Calls = buildAudioIncidentCalls(events, from, to)
	affected := map[string]bool{}
	for _, c := range report.Calls {
		affected[c.CallID] = true
	}
	healthy, count, err := a.db().audioIncidentHealthyCalls(ctx, project, from, to, affected)
	if err != nil {
		return report, err
	}
	report.Healthy = healthy
	if report.Alerts, err = a.db().audioIncidentAlerts(ctx, project, from, to); err != nil {
		return report, err
	}
	if report.Probes, err = a.db().audioProbeRounds(ctx, project, from, to, now); err != nil {
		return report, err
	}
	report.Summary = summarizeAudioIncident(report.Calls, count)
	report.Reading = readAudioIncident(report.Summary, report.Probes)
	return report, nil
}

func (a *App) toolAudioIncident(c context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	project := currentProject(ctx)
	if project == "" {
		return nil, errors.New("project context required")
	}
	text := func(key string) string { v, _ := args[key].(string); return v }
	now := time.Now().UTC()
	from, to, err := parseAudioIncidentWindow(text("from"), text("to"), now)
	if err != nil {
		return nil, err
	}
	return a.audioIncident(c, project, from, to, now)
}

func (a *App) serveAudioIncident(w http.ResponseWriter, r *http.Request, project string) {
	now := time.Now().UTC()
	from, to, err := parseAudioIncidentWindow(r.URL.Query().Get("from"), r.URL.Query().Get("to"), now)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	report, err := a.audioIncident(r.Context(), project, from, to, now)
	if err != nil {
		http.Error(w, "incident diagnostics unavailable", 503)
		return
	}
	writeJSON(w, report)
}

// ─── Network checks during a burst of disconnects ───────────────────────

const (
	audioProbeWindow   = 15 * time.Second
	audioProbeMinCalls = 3
	audioProbeCooldown = 5 * time.Minute
	audioProbeTimeout  = 2 * time.Second
	audioProbeMaxHosts = 5
	audioProbeTerm     = 7 * 24 * time.Hour
)

type audioProbeResult struct {
	Target string `json:"target"`
	OK     bool   `json:"ok"`
	MS     int64  `json:"ms"`
	Error  string `json:"error,omitempty"`
}
type audioProbeRound struct {
	At      string             `json:"occurred_at"`
	Calls   int                `json:"calls"`
	Results []audioProbeResult `json:"results"`
}

// Never does I/O and only sees disconnects, so it costs a healthy call nothing.
type audioProbeMonitor struct {
	mu     sync.Mutex
	recent map[string]map[string]time.Time
	last   map[string]time.Time
}

// Reports how many calls dropped together when a round should start now.
func (m *audioProbeMonitor) observe(project, call string, now time.Time) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.recent == nil {
		m.recent, m.last = map[string]map[string]time.Time{}, map[string]time.Time{}
	}
	calls := m.recent[project]
	if calls == nil {
		calls = map[string]time.Time{}
		m.recent[project] = calls
	}
	calls[call] = now
	for id, at := range calls {
		if now.Sub(at) > audioProbeWindow {
			delete(calls, id)
		}
	}
	if len(calls) < audioProbeMinCalls || now.Sub(m.last[project]) < audioProbeCooldown {
		return 0
	}
	m.last[project] = now
	return len(calls)
}

func (a *App) collectBrowserNetwork(e audioNetworkEvent) {
	a.audioNetworks.enqueue(e)
	if !audioDisconnectUnplanned(e) {
		return
	}
	if calls := a.audioProbes.observe(e.ProjectID, e.CallID, time.Now()); calls > 0 {
		go a.runAudioProbeRound(e.ProjectID, calls)
	}
}

// Comma-separated host:port. "off" disables the checks.
func audioProbeTargets(setting, gatewayURL string) []string {
	setting = strings.TrimSpace(setting)
	if strings.EqualFold(setting, "off") {
		return nil
	}
	targets := []string{}
	if setting == "" {
		if parsed, err := url.Parse(gatewayURL); err == nil && parsed.Hostname() != "" {
			port := parsed.Port()
			if port == "" {
				port = "80"
				if parsed.Scheme == "https" {
					port = "443"
				}
			}
			targets = append(targets, net.JoinHostPort(parsed.Hostname(), port))
		}
		return append(targets, "sip.telnyx.com:5060", "1.1.1.1:443")
	}
	for _, raw := range strings.Split(setting, ",") {
		raw = strings.TrimSpace(raw)
		host, port, err := net.SplitHostPort(raw)
		if err != nil || host == "" || port == "" || len(raw) > 255 || len(targets) >= audioProbeMaxHosts {
			continue
		}
		targets = append(targets, raw)
	}
	return targets
}

func audioProbeError(err error) string {
	var dns *net.DNSError
	switch {
	case err == nil:
		return ""
	case errors.As(err, &dns):
		return "dns_failed"
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, os.ErrDeadlineExceeded):
		return "timeout"
	case errors.Is(err, syscall.ECONNREFUSED):
		return "refused"
	case errors.Is(err, syscall.ENETUNREACH), errors.Is(err, syscall.EHOSTUNREACH):
		return "unreachable"
	}
	var timeout net.Error
	if errors.As(err, &timeout) && timeout.Timeout() {
		return "timeout"
	}
	return "connect_failed"
}

// Connects only, sends nothing, and closes at once.
func runAudioProbes(ctx context.Context, targets []string, timeout time.Duration) []audioProbeResult {
	results := make([]audioProbeResult, len(targets))
	var wg sync.WaitGroup
	for i, target := range targets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			started := time.Now()
			dialer := net.Dialer{Timeout: timeout}
			conn, err := dialer.DialContext(ctx, "tcp", target)
			results[i] = audioProbeResult{Target: target, OK: err == nil, MS: time.Since(started).Milliseconds(), Error: audioProbeError(err)}
			if conn != nil {
				_ = conn.Close()
			}
		}()
	}
	wg.Wait()
	return results
}

func (a *App) runAudioProbeRound(project string, calls int) {
	setting := ""
	if globalCtx != nil {
		setting = globalCtx.WithProject(project).Config()["audio_incident_probe_targets"]
	}
	targets := audioProbeTargets(setting, os.Getenv("APTEVA_GATEWAY_URL"))
	if len(targets) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), audioProbeTimeout+500*time.Millisecond)
	defer cancel()
	at := time.Now().UTC()
	round := audioProbeRound{At: at.Format(time.RFC3339Nano), Calls: calls, Results: runAudioProbes(ctx, targets, audioProbeTimeout)}
	if err := a.db().saveAudioProbeRound(project, round, at); err != nil && globalCtx != nil {
		globalCtx.Logger().Warn("audio network check not saved", "project_id", project, "err", err)
	}
}

func (c *callsDB) saveAudioProbeRound(project string, round audioProbeRound, at time.Time) error {
	raw, err := json.Marshal(round)
	if err != nil {
		return err
	}
	if _, err = c.db.Exec(`INSERT OR IGNORE INTO telephony_audio_probes(id,project_id,occurred_at,expires_at,round_json) VALUES(?,?,?,?,?)`, newAudioConnectionID()+":probe", project, audioNetworkTimestamp(at), audioNetworkTimestamp(at.Add(audioProbeTerm)), string(raw)); err != nil {
		return err
	}
	_, err = c.db.Exec(`DELETE FROM telephony_audio_probes WHERE id IN (SELECT id FROM telephony_audio_probes WHERE expires_at<=? ORDER BY expires_at LIMIT 100)`, audioNetworkTimestamp(at))
	return err
}

func (c *callsDB) audioProbeRounds(ctx context.Context, project string, from, to, now time.Time) ([]audioProbeRound, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	rows, err := c.db.QueryContext(ctx, `SELECT round_json FROM telephony_audio_probes WHERE project_id=? AND occurred_at>=? AND occurred_at<? AND expires_at>? ORDER BY occurred_at LIMIT 50`, project, audioNetworkTimestamp(from), audioNetworkTimestamp(to.Add(audioIncidentLookAhead)), audioNetworkTimestamp(now))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	rounds := []audioProbeRound{}
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		var round audioProbeRound
		if json.Unmarshal([]byte(raw), &round) == nil {
			rounds = append(rounds, round)
		}
	}
	return rounds, rows.Err()
}
