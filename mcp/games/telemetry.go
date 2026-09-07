package main

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type PlayEvent struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	SessionID   string         `json:"session_id"`
	RunID       string         `json:"run_id,omitempty"`
	Release     string         `json:"release"`
	Environment string         `json:"environment"`
	Time        string         `json:"time"`
	Props       map[string]any `json:"props"`
}

func validatePlayEvent(ev PlayEvent) error {
	allowed := map[string]bool{"session_started": true, "run_started": true, "run_completed": true, "run_failed": true, "tutorial_completed": true, "performance_summary": true}
	if !allowed[ev.Name] {
		return errors.New("unknown gameplay event")
	}
	for _, v := range []string{ev.ID, ev.SessionID, ev.Release, ev.Environment} {
		if len(v) < 1 || len(v) > 128 {
			return errors.New("event id, session_id, release and environment required (maximum 128 characters)")
		}
	}
	t, e := time.Parse(time.RFC3339, ev.Time)
	if e != nil || t.After(time.Now().Add(5*time.Minute)) || t.Before(time.Now().AddDate(0, 0, -7)) {
		return errors.New("event time must be within the last seven days")
	}
	if len(ev.RunID) > 128 || len(ev.Props) > 20 {
		return errors.New("event metadata too large")
	}
	for k, v := range ev.Props {
		if len(k) > 64 || strings.HasPrefix(k, "_") {
			return errors.New("invalid property name")
		}
		switch value := v.(type) {
		case nil, bool, float64:
		case string:
			if len(value) > 256 {
				return errors.New("property value too long")
			}
		default:
			return errors.New("event properties must be scalar")
		}
	}
	return nil
}
func recordPlayEvents(ctx *sdk.AppCtx, s GameScope, player int64, events []PlayEvent) (int, error) {
	if len(events) == 0 || len(events) > 50 {
		return 0, errors.New("batch must contain 1–50 events")
	}
	for _, ev := range events {
		if e := validatePlayEvent(ev); e != nil {
			return 0, e
		}
	}
	var pending int
	if e := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM (SELECT id FROM game_outbox WHERE project_id=? AND game_id=? AND analytics=1 LIMIT 10001)`, s.ProjectID, s.GameID).Scan(&pending); e != nil {
		return 0, e
	}
	if pending+len(events) > 10000 {
		return 0, errors.New("telemetry backlog full; retry after Analytics reconnects")
	}
	tx, e := ctx.AppDB().Begin()
	if e != nil {
		return 0, e
	}
	defer tx.Rollback()
	accepted := 0
	for _, ev := range events {
		fingerprint := studioHash(ev)
		var old string
		e = tx.QueryRow(`SELECT fingerprint FROM game_telemetry_receipts WHERE project_id=? AND game_id=? AND player_id=? AND event_id=?`, s.ProjectID, s.GameID, player, ev.ID).Scan(&old)
		if e == nil {
			if old != fingerprint {
				return 0, errors.New("event ID reused with different content")
			}
			continue
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return 0, e
		}
		_, e = tx.Exec(`INSERT INTO game_telemetry_receipts VALUES(?,?,?,?,?,?)`, s.ProjectID, s.GameID, player, ev.ID, fingerprint, nowRFC())
		if e != nil {
			return 0, e
		}
		props := map[string]any{"player_id": player, "client_event_id": ev.ID, "session_id": ev.SessionID, "run_id": ev.RunID, "release": ev.Release, "environment": ev.Environment, "event_time": ev.Time, "properties": ev.Props, "schema_version": 1}
		if e = queueEvent(tx, s, "play."+ev.Name, props, true); e != nil {
			return 0, e
		}
		accepted++
	}
	return accepted, tx.Commit()
}
func (a *App) handleTelemetry(w http.ResponseWriter, r *http.Request) {
	ctx, s, player, ok := a.requirePlayer(w, r)
	if !ok {
		return
	}
	if !cfgBool(ctx, "analytics_enabled", true) {
		httpJSON(w, map[string]any{"accepted": 0, "disabled": true})
		return
	}
	if !allowRequest(ctx, s, fmt.Sprintf("telemetry:%d", player.ID), 20) {
		httpErr(w, 429, "telemetry rate limit")
		return
	}
	var body struct {
		Events []PlayEvent `json:"events"`
	}
	if e := decodeBody(w, r, &body); e != nil {
		httpErr(w, 400, e.Error())
		return
	}
	n, e := recordPlayEvents(ctx, s, player.ID, body.Events)
	if e != nil {
		httpErr(w, 400, e.Error())
		return
	}
	httpJSON(w, map[string]any{"accepted": n})
}
func analyticsEventInput(ctx *sdk.AppCtx, itID int64, s GameScope, topic string, payload map[string]any) map[string]any {
	var namespace string
	_ = ctx.AppDB().QueryRow(`SELECT namespace FROM game_studio_identity WHERE id=1`).Scan(&namespace)
	key := fmt.Sprintf("games:%s:%s:%s:%d", namespace, s.ProjectID, s.GameID, itID)
	in := map[string]any{"_project_id": s.ProjectID, "event": topic, "app": "games", "user_id": fmt.Sprintf("game:%s:player:%v", s.GameID, payload["player_id"]), "props": payload, "upsert_key": key, "delivery_id": key}
	if t, e := time.Parse(time.RFC3339, txt(payload["event_time"])); e == nil {
		in["ts"] = t.UnixMilli()
	}
	if id := txt(payload["session_id"]); id != "" {
		in["session_id"] = id
	}
	return in
}
