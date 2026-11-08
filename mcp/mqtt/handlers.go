// handlers.go — HTTP routes for the panel + ad-hoc operator use.
// Each handler is a thin shell around the same helpers the MCP
// tools call, with HTTP method routing + JSON envelopes.

package main

import (
	"database/sql"
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
	writeJSON(w, a.snapshotStatus(a.requestContext(r)))
}

func (a *App) handleClients(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpErr(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	writeJSON(w, a.snapshotClients(a.requestContext(r)))
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

	out := []map[string]any{}
	beforeID := int64(1<<63 - 1)
	for len(out) < limit {
		batchSize := 500
		if pat == "" && limit-len(out) < batchSize {
			batchSize = limit - len(out)
		}
		rows, err := a.ctx.AppDB().Query(
			`SELECT id, ts, topic, payload, payload_size, payload_truncated, qos, retain, client_id, username, is_printable
			   FROM mqtt_message_log
			  WHERE scope_id = ? AND id < ?
			  ORDER BY id DESC
			  LIMIT ?`, projectScope(a.requestContext(r)), beforeID, batchSize)
		if err != nil {
			httpErr(w, 500, err.Error())
			return
		}
		seen := 0
		for rows.Next() {
			var (
				id                            int64
				ts, topic, clientID, username string
				payload                       []byte
				payloadSize, payloadTruncated int
				qos, retain, isPrintable      int
			)
			if err := rows.Scan(&id, &ts, &topic, &payload, &payloadSize, &payloadTruncated, &qos, &retain, &clientID, &username, &isPrintable); err != nil {
				rows.Close()
				httpErr(w, 500, err.Error())
				return
			}
			seen++
			beforeID = id
			if pat != "" && !mqttTopicMatch(pat, topic) {
				continue
			}
			row := map[string]any{
				"id": id, "ts": ts, "topic": topic, "qos": qos,
				"retain": retain == 1, "client_id": clientID, "username": username,
				"payload_size_bytes": payloadSize, "payload_truncated": payloadTruncated == 1,
			}
			if isPrintable == 1 {
				row["payload"] = string(payload)
			} else {
				row["payload_binary"] = true
			}
			out = append(out, row)
			if len(out) == limit {
				break
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			httpErr(w, 500, err.Error())
			return
		}
		rows.Close()
		if seen < batchSize {
			break
		}
	}
	writeJSON(w, out)
}

func (a *App) handleUsers(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		out, err := a.toolUsersList(a.requestContext(r), nil)
		if err != nil {
			httpErr(w, 500, err.Error())
			return
		}
		writeJSON(w, out)
	case http.MethodPost:
		var args map[string]any
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&args); err != nil {
			httpErr(w, 400, "invalid json")
			return
		}
		out, err := a.toolUsersCreate(a.requestContext(r), args)
		if err != nil {
			httpErr(w, 400, err.Error())
			return
		}
		writeJSON(w, out)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "GET or POST")
	}
}

func (a *App) handleUserItem(w http.ResponseWriter, r *http.Request) {
	username := strings.TrimPrefix(r.URL.Path, "/users/")
	if username == "" {
		httpErr(w, 400, "username required")
		return
	}
	args := map[string]any{"username": username}
	var out any
	var err error
	switch r.Method {
	case http.MethodDelete:
		out, err = a.toolUsersDelete(a.requestContext(r), args)
	case http.MethodPatch:
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&args); err != nil {
			httpErr(w, 400, "invalid json")
			return
		}
		if args == nil {
			httpErr(w, 400, "JSON object required")
			return
		}
		args["username"] = username
		_, aclPub := args["allow_publish"]
		_, aclSub := args["allow_subscribe"]
		_, password := args["password"]
		_, enabled := args["enabled"]
		operations := 0
		if aclPub || aclSub {
			operations++
		}
		if password {
			operations++
		}
		if enabled {
			operations++
		}
		if operations != 1 {
			httpErr(w, 400, "choose one operation: ACL update, password rotation, or enabled state")
			return
		}
		switch {
		case password:
			out, err = a.toolUsersRotatePassword(a.requestContext(r), args)
		case enabled:
			out, err = a.toolUsersSetEnabled(a.requestContext(r), args)
		default:
			out, err = a.toolUsersUpdateACL(a.requestContext(r), args)
		}
	default:
		httpErr(w, http.StatusMethodNotAllowed, "DELETE or PATCH")
		return
	}
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	writeJSON(w, out)
}

func (a *App) handleRetained(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpErr(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	out, err := listRetainedMessages(a.ctx.AppDB(), projectScope(a.requestContext(r)), r.URL.Query().Get("topic_pattern"), limit)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	writeJSON(w, out)
}

func (a *App) handleRetainedItem(w http.ResponseWriter, r *http.Request) {
	topic := strings.TrimPrefix(r.URL.Path, "/retained/")
	if topic == "" {
		httpErr(w, 400, "topic required")
		return
	}
	switch r.Method {
	case http.MethodGet:
		out, err := getRetainedMessage(a.ctx.AppDB(), projectScope(a.requestContext(r)), topic)
		if err != nil {
			httpErr(w, 404, err.Error())
			return
		}
		writeJSON(w, out)
	case http.MethodDelete:
		if _, err := getRetainedMessage(a.ctx.AppDB(), projectScope(a.requestContext(r)), topic); err != nil {
			httpErr(w, 404, err.Error())
			return
		}
		if a.broker != nil {
			if err := a.broker.DeleteRetained(topic); err != nil {
				publishHTTPError(w, err)
				return
			}
		}
		if _, err := a.ctx.AppDB().Exec(`DELETE FROM mqtt_retained WHERE scope_id = ? AND topic = ?`, projectScope(a.requestContext(r)), topic); err != nil {
			httpErr(w, 500, err.Error())
			return
		}
		writeJSON(w, map[string]any{"ok": true, "topic": topic})
	default:
		httpErr(w, http.StatusMethodNotAllowed, "GET or DELETE")
	}
}

func (a *App) handleSubscriptions(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		subs, err := listBusSubscriptions(a.ctx.AppDB(), projectScope(a.requestContext(r)))
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
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&body); err != nil {
			httpErr(w, 400, "invalid json: "+err.Error())
			return
		}
		// Reuse the MCP path so the in-memory broker subscription
		// gets registered too.
		out, err := a.toolSubscribe(a.requestContext(r), map[string]any{
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
	sub, err := deleteBusSubscription(a.ctx.AppDB(), projectScope(a.requestContext(r)), id)
	if err == sql.ErrNoRows {
		httpErr(w, http.StatusNotFound, "subscription not found")
		return
	}
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	if a.broker != nil {
		_ = a.broker.Unsubscribe(sub.TopicPattern, int(100+sub.ID))
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (a *App) handleDevices(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpErr(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	out, err := a.toolDevices(a.requestContext(r), map[string]any{
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
	maxBody := int64(clampConfigInt(a.ctx, "max_payload_bytes", 1048576, 1024, 268435455)) + 64<<10
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody)).Decode(&body); err != nil {
		httpErr(w, 400, "invalid json: "+err.Error())
		return
	}
	if err := requireTopicScope(a.requestContext(r), body.Topic); err != nil {
		httpErr(w, 403, err.Error())
		return
	}
	if err := validatePublish(body.Topic, body.QoS); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	if err := a.validatePayloadSize([]byte(body.Payload)); err != nil {
		publishHTTPError(w, err)
		return
	}
	if a.broker == nil {
		httpErr(w, 503, "broker not running")
		return
	}
	id, err := a.broker.PublishMessage(body.Topic, []byte(body.Payload), body.Retain, byte(body.QoS))
	if err != nil {
		publishHTTPError(w, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "accepted": true, "durable": true, "message_id": id, "ts": time.Now().UTC().Format(time.RFC3339)})
}
