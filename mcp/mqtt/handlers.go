// handlers.go — HTTP routes for the panel + ad-hoc operator use.
// Each handler is a thin shell around the same helpers the MCP
// tools call, with HTTP method routing + JSON envelopes.

package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func (a *App) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpErr(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	writeJSON(w, a.snapshotStatus())
}

// /clients — currently a stub. mochi exposes a clients map but
// there's no public iterator we can rely on across versions; the
// operator gets the count via /status, which is enough for v0.1.
func (a *App) handleClients(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, []any{})
}

// /messages?limit=N&topic_pattern=foo/+
func (a *App) handleMessages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpErr(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		n, _ := strconv.Atoi(v)
		if n > 0 && n <= 1000 {
			limit = n
		}
	}
	pat := r.URL.Query().Get("topic_pattern")

	rows, err := a.ctx.AppDB().Query(
		`SELECT id, ts, topic, payload, qos, retain, client_id, is_printable
		   FROM mqtt_message_log
		  ORDER BY id DESC
		  LIMIT ?`, limit)
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var (
			id                          int64
			ts, topic, clientID         string
			payload                     []byte
			qos, retain, isPrintable    int
		)
		if err := rows.Scan(&id, &ts, &topic, &payload, &qos, &retain, &clientID, &isPrintable); err != nil {
			httpErr(w, 500, err.Error())
			return
		}
		if pat != "" && !mqttTopicMatch(pat, topic) {
			continue
		}
		row := map[string]any{
			"id": id, "ts": ts, "topic": topic, "qos": qos,
			"retain": retain == 1, "client_id": clientID,
		}
		if isPrintable == 1 {
			row["payload"] = string(payload)
		} else {
			row["payload_size_bytes"] = len(payload)
			row["payload_binary"] = true
		}
		out = append(out, row)
	}
	writeJSON(w, out)
}

func (a *App) handleUsers(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		users, err := listUsers(a.ctx.AppDB())
		if err != nil {
			httpErr(w, 500, err.Error())
			return
		}
		writeJSON(w, users)
	case http.MethodPost:
		var body struct {
			Username       string   `json:"username"`
			Password       string   `json:"password"`
			AllowPublish   []string `json:"allow_publish"`
			AllowSubscribe []string `json:"allow_subscribe"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			httpErr(w, 400, "invalid json: "+err.Error())
			return
		}
		if err := addUser(a.ctx.AppDB(), body.Username, body.Password, body.AllowPublish, body.AllowSubscribe); err != nil {
			httpErr(w, 400, err.Error())
			return
		}
		writeJSON(w, map[string]any{"ok": true, "username": body.Username})
	default:
		httpErr(w, http.StatusMethodNotAllowed, "GET or POST")
	}
}

// /users/{username}    DELETE | PATCH (toggle enabled)
func (a *App) handleUserItem(w http.ResponseWriter, r *http.Request) {
	username := strings.TrimPrefix(r.URL.Path, "/users/")
	if username == "" {
		httpErr(w, 400, "username required")
		return
	}
	switch r.Method {
	case http.MethodDelete:
		if err := deleteUser(a.ctx.AppDB(), username); err != nil {
			httpErr(w, 500, err.Error())
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	case http.MethodPatch:
		var body struct {
			Enabled *bool `json:"enabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			httpErr(w, 400, "invalid json: "+err.Error())
			return
		}
		if body.Enabled == nil {
			httpErr(w, 400, "enabled required")
			return
		}
		if err := setUserEnabled(a.ctx.AppDB(), username, *body.Enabled); err != nil {
			httpErr(w, 500, err.Error())
			return
		}
		writeJSON(w, map[string]any{"ok": true, "enabled": *body.Enabled})
	default:
		httpErr(w, http.StatusMethodNotAllowed, "DELETE or PATCH")
	}
}

func (a *App) handleSubscriptions(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		subs, err := listBusSubscriptions(a.ctx.AppDB())
		if err != nil {
			httpErr(w, 500, err.Error())
			return
		}
		writeJSON(w, subs)
	case http.MethodPost:
		var body struct {
			TopicPattern string `json:"topic_pattern"`
			BusTopic     string `json:"bus_topic"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			httpErr(w, 400, "invalid json: "+err.Error())
			return
		}
		// Reuse the MCP path so the in-memory broker subscription
		// gets registered too.
		out, err := a.toolSubscribe(a.ctx, map[string]any{
			"topic_pattern": body.TopicPattern,
			"bus_topic":     body.BusTopic,
		})
		if err != nil {
			httpErr(w, 400, err.Error())
			return
		}
		writeJSON(w, out)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "GET or POST")
	}
}

func (a *App) handleSubscriptionItem(w http.ResponseWriter, r *http.Request) {
	idStr := strings.TrimPrefix(r.URL.Path, "/subscriptions/")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id == 0 {
		httpErr(w, 400, "id required")
		return
	}
	if r.Method != http.MethodDelete {
		httpErr(w, http.StatusMethodNotAllowed, "DELETE only")
		return
	}
	if err := deleteBusSubscription(a.ctx.AppDB(), id); err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (a *App) handleDevices(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpErr(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	out, err := a.toolDevices(a.ctx, map[string]any{
		"filter": r.URL.Query().Get("filter"),
	})
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	writeJSON(w, out)
}

// /test_publish — operator tool from the panel. POST {topic, payload, retain?, qos?}.
func (a *App) handleTestPublish(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	var body struct {
		Topic   string `json:"topic"`
		Payload string `json:"payload"`
		Retain  bool   `json:"retain"`
		QoS     int    `json:"qos"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, 400, "invalid json: "+err.Error())
		return
	}
	if body.Topic == "" {
		httpErr(w, 400, "topic required")
		return
	}
	if a.broker == nil {
		httpErr(w, 503, "broker not running")
		return
	}
	if err := a.broker.Publish(body.Topic, []byte(body.Payload), body.Retain, byte(body.QoS)); err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true, "ts": time.Now().UTC().Format(time.RFC3339)})
}
