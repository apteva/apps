package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"time"
)

var errBrowserTelemetryInvalid = errors.New("invalid telemetry")
var errBrowserTelemetryLimit = errors.New("telemetry limit reached")
var browserTelemetryIDPattern = regexp.MustCompile(`^[A-Za-z0-9_.:#-]{1,256}$`)

func safeBrowserTelemetryID(v string) bool { return browserTelemetryIDPattern.MatchString(v) }
func telemetryRequestID(r *http.Request) string {
	v := r.Header.Get("X-Request-ID")
	if v == "" {
		v = r.URL.Query().Get("request_id")
	}
	if safeBrowserTelemetryID(v) {
		return v
	}
	return "tel-" + newSecret()[:24]
}

type telemetryStatusWriter struct {
	http.ResponseWriter
	status int
}

func (w *telemetryStatusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *telemetryStatusWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}
func (w *telemetryStatusWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.status = 200
	}
	return w.ResponseWriter.Write(data)
}

// HTTP correlation stays outside admission/auth wrappers, including rejected reads.
func (a *App) deliveryRequestHTTP(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := telemetryRequestID(r)
		r.Header.Set("X-Request-ID", id)
		w.Header().Set("X-Request-ID", id)
		out := &telemetryStatusWriter{ResponseWriter: w}
		next(out, r)
		if out.status >= 400 {
			log.Printf("telephony_request request_id=%s method=%s path=%s status=%d", id, r.Method, r.URL.Path, out.status)
		}
	}
}

type browserWatchFailure struct {
	Category        string `json:"category"`
	Status          *int   `json:"status,omitempty"`
	Code            string `json:"code,omitempty"`
	RequestID       string `json:"requestId,omitempty"`
	ClientRequestID string `json:"clientRequestId,omitempty"`
}
type browserWatchHealth struct {
	BrowserTabID            string               `json:"browser_tab_id"`
	BrowserSessionID        string               `json:"browser_session_id"`
	WatchID                 string               `json:"watch_id"`
	State                   string               `json:"state"`
	Connections             int                  `json:"connections"`
	Disconnects             int                  `json:"disconnects"`
	Reconnects              int                  `json:"reconnects"`
	ConsecutiveListFailures int                  `json:"consecutive_list_failures"`
	LastConnectAt           string               `json:"last_connect_at,omitempty"`
	LastDisconnectAt        string               `json:"last_disconnect_at,omitempty"`
	LastReconnectAttemptAt  string               `json:"last_reconnect_attempt_at,omitempty"`
	LastHeartbeatAt         string               `json:"last_heartbeat_at,omitempty"`
	LastStreamEventAt       string               `json:"last_stream_event_at,omitempty"`
	LastSuccessfulReadAt    string               `json:"last_successful_call_list_at,omitempty"`
	LastRequestID           string               `json:"last_request_id,omitempty"`
	LastFailure             *browserWatchFailure `json:"last_failure,omitempty"`
}

func (h browserWatchHealth) validate() error {
	if !safeBrowserTelemetryID(h.BrowserTabID) || !safeBrowserTelemetryID(h.BrowserSessionID) || !safeBrowserTelemetryID(h.WatchID) {
		return errors.New("browser correlation IDs required")
	}
	if h.State != "disabled" && h.State != "connecting" && h.State != "connected" && h.State != "reconnecting" && h.State != "closed" {
		return errors.New("invalid stream state")
	}
	for _, n := range []int{h.Connections, h.Disconnects, h.Reconnects, h.ConsecutiveListFailures} {
		if n < 0 || n > 10000000 {
			return errors.New("invalid stream counter")
		}
	}
	for _, s := range []string{h.LastConnectAt, h.LastDisconnectAt, h.LastReconnectAttemptAt, h.LastHeartbeatAt, h.LastStreamEventAt, h.LastSuccessfulReadAt} {
		if s != "" {
			if _, err := time.Parse(time.RFC3339Nano, s); err != nil {
				return errors.New("invalid browser timestamp")
			}
		}
	}
	if h.LastRequestID != "" && !safeBrowserTelemetryID(h.LastRequestID) {
		return errors.New("invalid request ID")
	}
	if f := h.LastFailure; f != nil {
		switch f.Category {
		case "http", "network", "timeout", "canceled", "invalid_response", "callback", "stream_closed":
		default:
			return errors.New("invalid failure category")
		}
		if f.Status != nil && (*f.Status < 0 || *f.Status > 599) {
			return errors.New("invalid status")
		}
		for _, s := range []string{f.Code, f.RequestID, f.ClientRequestID} {
			if s != "" && !safeBrowserTelemetryID(s) {
				return errors.New("invalid failure label")
			}
		}
	}
	return nil
}

type browserOfferEvent struct {
	CallID     string `json:"call_id"`
	OfferID    string `json:"offer_id"`
	Stage      string `json:"stage"`
	EventID    string `json:"event_id"`
	OccurredAt string `json:"occurred_at,omitempty"`
	Visibility string `json:"visibility,omitempty"`
	Focused    *bool  `json:"focused,omitempty"`
	Code       string `json:"code,omitempty"`
	RequestID  string `json:"request_id,omitempty"`
}

func (e browserOfferEvent) validate(receipt bool) error {
	if !safeBrowserTelemetryID(e.CallID) || !safeBrowserTelemetryID(e.OfferID) || !safeBrowserTelemetryID(e.EventID) {
		return errors.New("call, offer and event IDs required")
	}
	valid := false
	for _, s := range []string{"ui_displayed", "ui_hidden", "ui_failed", "ringtone_attempted", "ringtone_started", "ringtone_failed", "ringtone_stopped", "ringtone_suppressed", "answer_attempted", "answer_failed"} {
		valid = valid || e.Stage == s
	}
	if receipt && e.Stage == "browser_acknowledged" {
		valid = true
	}
	if !valid {
		return errors.New("unsupported browser evidence stage")
	}
	if e.OccurredAt != "" {
		if _, err := time.Parse(time.RFC3339Nano, e.OccurredAt); err != nil {
			return errors.New("invalid browser time")
		}
	}
	if e.Visibility != "" && e.Visibility != "visible" && e.Visibility != "hidden" && e.Visibility != "prerender" {
		return errors.New("invalid visibility")
	}
	for _, s := range []string{e.Code, e.RequestID} {
		if s != "" && !safeBrowserTelemetryID(s) {
			return errors.New("invalid diagnostic label")
		}
	}
	return nil
}
func (a *App) telemetryOfferAllowed(r *http.Request, project string, e browserOfferEvent) error {
	var dest, kind, offered string
	err := a.db().db.QueryRow(`SELECT destination_id,kind,offered_at FROM call_offers WHERE project_id=? AND call_id=? AND id=?`, project, e.CallID, e.OfferID).Scan(&dest, &kind, &offered)
	if err != nil {
		return err
	}
	if kind != "browser" || offered == "" {
		return sql.ErrNoRows
	}
	row, err := a.db().findCall(e.CallID)
	if err != nil {
		return err
	}
	if row == nil || row.ProjectID != project {
		return sql.ErrNoRows
	}
	if p := phoneUserFrom(r); p != nil && (!p.Destinations[dest] || !a.destinationAllowsIdentity(project, dest, p.Identity)) {
		return sql.ErrNoRows
	}
	if at, err := time.Parse(time.RFC3339Nano, offered); err != nil || at.Before(time.Now().Add(-7*24*time.Hour)) {
		return sql.ErrNoRows
	}
	return nil
}
func (a *App) storeBrowserTelemetry(r *http.Request, project, tab, session string, events []browserOfferEvent, health *browserWatchHealth, receipt bool) error {
	if !safeBrowserTelemetryID(tab) || !safeBrowserTelemetryID(session) || len(events) > 16 || (len(events) == 0 && health == nil) {
		return errBrowserTelemetryInvalid
	}
	for _, e := range events {
		if err := e.validate(receipt); err != nil {
			return fmt.Errorf("%w: %v", errBrowserTelemetryInvalid, err)
		}
		if err := a.telemetryOfferAllowed(r, project, e); err != nil {
			return err
		}
	}
	if health != nil {
		if err := health.validate(); err != nil {
			return fmt.Errorf("%w: %v", errBrowserTelemetryInvalid, err)
		}
		if health.BrowserTabID != tab || health.BrowserSessionID != session {
			return errBrowserTelemetryInvalid
		}
	}
	tx, err := a.db().db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := ringTime(time.Now())
	principal := autoAnswerOwner(r)
	// Bounded indexed retention work only when telemetry arrives; no new polling worker.
	for _, q := range []string{`DELETE FROM telephony_offer_browser_events WHERE id IN (SELECT id FROM telephony_offer_browser_events WHERE expires_at<=? ORDER BY expires_at,id LIMIT 64)`, `DELETE FROM telephony_browser_watch_health WHERE rowid IN (SELECT rowid FROM telephony_browser_watch_health WHERE expires_at<=? ORDER BY expires_at LIMIT 64)`} {
		if _, err = tx.Exec(q, now); err != nil {
			return err
		}
	}
	for _, e := range events {
		var count, duplicate int
		if err = tx.QueryRow(`SELECT COUNT(*),COALESCE(MAX(event_id=?),0) FROM telephony_offer_browser_events WHERE offer_id=? AND principal=? AND browser_session_id=?`, e.EventID, e.OfferID, principal, session).Scan(&count, &duplicate); err != nil {
			return err
		}
		if count >= 64 && duplicate == 0 {
			return errBrowserTelemetryLimit
		}
		var total int
		if err = tx.QueryRow(`SELECT COUNT(*) FROM telephony_offer_browser_events WHERE offer_id=?`, e.OfferID).Scan(&total); err != nil {
			return err
		}
		if total >= 512 && duplicate == 0 {
			return errBrowserTelemetryLimit
		}
		facts, _ := json.Marshal(map[string]any{"visibility": e.Visibility, "focused": e.Focused, "code": e.Code})
		_, err = tx.Exec(`INSERT INTO telephony_offer_browser_events(project_id,call_id,offer_id,principal,browser_tab_id,browser_session_id,event_id,stage,client_occurred_at,received_at,request_id,facts_json,expires_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(project_id,principal,browser_session_id,event_id) DO NOTHING`, project, e.CallID, e.OfferID, principal, tab, session, e.EventID, e.Stage, e.OccurredAt, now, firstNonEmpty(e.RequestID, r.Header.Get("X-Request-ID")), string(facts), ringTime(time.Now().Add(7*24*time.Hour)))
		if err != nil {
			return err
		}
	}
	if health != nil {
		var count int
		if err = tx.QueryRow(`SELECT COUNT(*) FROM telephony_browser_watch_health WHERE project_id=? AND principal=?`, project, principal).Scan(&count); err != nil {
			return err
		}
		if count >= 32 {
			var existing int
			if err = tx.QueryRow(`SELECT COUNT(*) FROM telephony_browser_watch_health WHERE project_id=? AND principal=? AND browser_session_id=? AND watch_id=?`, project, principal, session, health.WatchID).Scan(&existing); err != nil {
				return err
			}
			if existing == 0 {
				return errBrowserTelemetryLimit
			}
		}
		raw, _ := json.Marshal(health)
		_, err = tx.Exec(`INSERT INTO telephony_browser_watch_health VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(project_id,principal,browser_session_id,watch_id) DO UPDATE SET browser_tab_id=excluded.browser_tab_id,snapshot_json=excluded.snapshot_json,received_at=excluded.received_at,expires_at=excluded.expires_at`, project, principal, tab, session, health.WatchID, string(raw), now, ringTime(time.Now().Add(24*time.Hour)))
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (a *App) handleBrowserTelemetry(w http.ResponseWriter, r *http.Request, project string) {
	if r.Method != "POST" {
		http.Error(w, "POST required", 405)
		return
	}
	var body struct {
		BrowserTabID     string              `json:"browser_tab_id"`
		BrowserSessionID string              `json:"browser_session_id"`
		Events           []browserOfferEvent `json:"events"`
		Watch            *browserWatchHealth `json:"watch,omitempty"`
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384))
	d.DisallowUnknownFields()
	if d.Decode(&body) != nil {
		http.Error(w, "invalid browser telemetry", 400)
		return
	}
	if err := a.storeBrowserTelemetry(r, project, body.BrowserTabID, body.BrowserSessionID, body.Events, body.Watch, false); err != nil {
		status, code := 503, "telemetry_unavailable"
		if errors.Is(err, sql.ErrNoRows) {
			status, code = 404, "offer_not_visible"
		} else if errors.Is(err, errBrowserTelemetryInvalid) {
			status, code = 400, "invalid_telemetry"
		} else if errors.Is(err, errBrowserTelemetryLimit) {
			status, code = 429, "telemetry_limit_reached"
		}
		writeJSONStatus(w, status, map[string]any{"code": code, "request_id": telemetryRequestID(r)})
		return
	}
	writeJSON(w, map[string]any{"accepted": true, "evidence": "browser_reported", "physical_audibility": "unknown"})
}
func (a *App) browserOfferTelemetry(project, callID, principal string) (map[string]any, error) {
	var allowed map[string]bool
	if principal != "" {
		var identity phoneIdentity
		if json.Unmarshal([]byte(principal), &identity) != nil || !identity.valid() {
			return nil, errPhoneAccessDenied
		}
		p, err := a.phonePrincipal(project, identity)
		if err != nil {
			return nil, err
		}
		allowed = map[string]bool{}
		for dest, have := range p.Destinations {
			if have && a.destinationAllowsIdentity(project, dest, identity) {
				allowed[dest] = true
			}
		}
	}

	rows, err := a.db().db.Query(`SELECT id,offer_id,principal,browser_tab_id,browser_session_id,stage,client_occurred_at,received_at,request_id,facts_json FROM telephony_offer_browser_events WHERE project_id=? AND call_id=? AND (?='' OR principal=?) ORDER BY id DESC LIMIT 257`, project, callID, principal, principal)
	if err != nil {
		return nil, err
	}
	events := []map[string]any{}
	for rows.Next() {
		var id int64
		var offer, p, tab, session, stage, clientAt, received, requestID, raw string
		if err = rows.Scan(&id, &offer, &p, &tab, &session, &stage, &clientAt, &received, &requestID, &raw); err != nil {
			break
		}
		var facts map[string]any
		if err = json.Unmarshal([]byte(raw), &facts); err != nil {
			break
		}
		var identity phoneIdentity
		_ = json.Unmarshal([]byte(p), &identity)
		e := map[string]any{"id": fmt.Sprint(id), "offer_id": offer, "browser_tab_id": tab, "browser_session_id": session, "stage": stage, "client_occurred_at": clientAt, "received_at": received, "request_id": requestID, "facts": facts, "evidence": "browser_reported"}
		if identity.valid() {
			e["adviser_identity"] = identity
		} else {
			e["operator"] = p
		}
		events = append(events, e)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	truncated := len(events) > 256
	if truncated {
		events = events[:256]
	}
	offers := []map[string]any{}
	rows, err = a.db().db.Query(`SELECT id,destination_id,status,offered_at,acknowledged_at,claimed_at,expires_at FROM call_offers WHERE project_id=? AND call_id=? ORDER BY position,id`, project, callID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, dest, status, created, ack, claimed, expires string
		if err = rows.Scan(&id, &dest, &status, &created, &ack, &claimed, &expires); err != nil {
			break
		}
		if allowed != nil && !allowed[dest] {
			continue
		}
		offers = append(offers, map[string]any{"offer_id": id, "destination_id": dest, "status": status, "offer_created_at": created, "receipt_acknowledged_at": ack, "claimed_at": claimed, "expires_at": expires})
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	health := []map[string]any{}
	hrows, err := a.db().db.Query(`SELECT h.snapshot_json,h.received_at FROM telephony_browser_watch_health h WHERE h.project_id=? AND (?='' OR h.principal=?) AND h.expires_at>? AND EXISTS(SELECT 1 FROM telephony_offer_browser_events e WHERE e.project_id=h.project_id AND e.call_id=? AND e.principal=h.principal AND e.browser_session_id=h.browser_session_id) ORDER BY h.received_at DESC LIMIT 33`, project, principal, principal, ringTime(time.Now()), callID)
	if err != nil {
		return nil, err
	}
	for hrows.Next() {
		var raw, received string
		if err = hrows.Scan(&raw, &received); err != nil {
			break
		}
		var snap map[string]any
		if err = json.Unmarshal([]byte(raw), &snap); err != nil {
			break
		}
		snap["server_received_at"] = received
		health = append(health, snap)
	}
	if err == nil {
		err = hrows.Err()
	}
	hrows.Close()
	if err != nil {
		return nil, err
	}
	healthTruncated := len(health) > 32
	if healthTruncated {
		health = health[:32]
	}
	return map[string]any{"offers": offers, "events": events, "watch_health": health, "watch_health_truncated": healthTruncated, "events_truncated": truncated, "physical_audibility": "unknown", "missing_event_means": "no retained evidence; not proof of adviser inaction"}, nil
}
