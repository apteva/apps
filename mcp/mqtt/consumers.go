package main

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	"github.com/mochi-mqtt/server/v2/packets"
)

type consumerError struct {
	status  int
	message string
}

func (e *consumerError) Error() string             { return e.message }
func consumerErr(status int, message string) error { return &consumerError{status, message} }

type durableMessage struct {
	Sequence   string             `json:"sequence"`
	ID         string             `json:"message_id"`
	Topic      string             `json:"topic"`
	Payload    string             `json:"payload_base64"`
	QoS        byte               `json:"qos"`
	Retain     bool               `json:"retain"`
	Properties packets.Properties `json:"properties"`
	ClientID   string             `json:"client_id"`
	Username   string             `json:"username"`
	AcceptedAt int64              `json:"accepted_at"`
	ExpiresAt  int64              `json:"expires_at,omitempty"`
}

func sequenceArg(args map[string]any) (int64, error) {
	value, ok := args["after_sequence"]
	if !ok || value == "" {
		return 0, nil
	}
	str, ok := value.(string)
	if !ok {
		return 0, consumerErr(400, "after_sequence must be a decimal string")
	}
	n, err := strconv.ParseInt(str, 10, 64)
	if err != nil || n < 0 {
		return 0, consumerErr(400, "invalid after_sequence")
	}
	return n, nil
}
func consumerOwner(c *sdk.Caller) (string, error) {
	if c == nil {
		return "", consumerErr(403, "authenticated consumer identity required")
	}
	if c.AppInstallID > 0 {
		return fmt.Sprintf("app:%d", c.AppInstallID), nil
	}
	if c.SubjectType != "" && c.SubjectID != "" {
		data, _ := json.Marshal([]string{c.SubjectType, c.SubjectID})
		return "subject:" + string(data), nil
	}
	if c.AgentID > 0 {
		return fmt.Sprintf("agent:%d", c.AgentID), nil
	}
	return "", consumerErr(403, "authenticated consumer identity required")
}
func httpConsumerOwner(r *http.Request) (string, error) {
	p, err := sdk.PrincipalFromRequest(r)
	if err != nil {
		return "", consumerErr(403, "invalid principal")
	}
	if p != nil {
		if p.SubjectType != "" && p.SubjectID != "" {
			data, _ := json.Marshal([]string{p.SubjectType, p.SubjectID})
			return "subject:" + string(data), nil
		}
		if p.UserID > 0 {
			return fmt.Sprintf("operator:%d", p.UserID), nil
		}
	}
	id, err := strconv.ParseInt(r.Header.Get("X-Apteva-Operator-ID"), 10, 64)
	if err == nil && id > 0 {
		return fmt.Sprintf("operator:%d", id), nil
	}
	return "", consumerErr(403, "authenticated consumer identity required")
}
func journalBounds(tx *sql.Tx, scope string) (floor, high int64, err error) {
	err = tx.QueryRow(`SELECT coalesce((SELECT seq FROM mqtt_journal_floor WHERE scope_id=?),0),max(coalesce((SELECT max(seq) FROM mqtt_journal WHERE scope_id=?),0),coalesce((SELECT seq FROM mqtt_journal_floor WHERE scope_id=?),0))`, scope, scope, scope).Scan(&floor, &high)
	return
}

// journalPage scans a bounded number of rows and returns a cursor even for an
// empty filtered page. Bodies are complete and responses capped at ~4 MiB.
func journalPage(tx *sql.Tx, scope, filter string, after, high int64, limit int) ([]durableMessage, int64, error) {
	rows, err := tx.Query(`SELECT seq,message_id,topic,packet,username,accepted_at FROM mqtt_journal WHERE scope_id=? AND seq>? AND seq<=? ORDER BY seq LIMIT 10000`, scope, after, high)
	if err != nil {
		return nil, after, err
	}
	defer rows.Close()
	out := []durableMessage{}
	next := after
	total := 0
	for rows.Next() {
		var seq, accepted int64
		var id, topic, username string
		var data []byte
		if err = rows.Scan(&seq, &id, &topic, &data, &username, &accepted); err != nil {
			return nil, after, err
		}
		if !mqttTopicMatch(filter, topic) {
			next = seq
			continue
		}
		if total+len(data) > 4*1024*1024 && len(out) > 0 {
			break
		}
		var pk packets.Packet
		if err = json.Unmarshal(data, &pk); err != nil {
			return nil, after, err
		}
		out = append(out, durableMessage{strconv.FormatInt(seq, 10), id, topic, base64.StdEncoding.EncodeToString(pk.Payload), pk.FixedHeader.Qos, pk.FixedHeader.Retain, pk.Properties, pk.Origin, username, accepted, pk.Expiry})
		total += len(data)
		next = seq
		if len(out) >= limit {
			break
		}
	}
	return out, next, rows.Err()
}
func (a *App) consumerAction(scope, owner string, args map[string]any) (any, error) {
	if !validScope(scope) || owner == "" {
		return nil, consumerErr(403, "authenticated consumer ownership required")
	}
	action := strArg(args, "action")
	name := strArg(args, "name")
	if action == "" {
		action = "claim"
	}
	if action != "history" && (name == "" || len(name) > 128 || strings.TrimSpace(name) != name || strings.ContainsRune(name, 0)) {
		return nil, consumerErr(400, "consumer name required (1-128 characters)")
	}
	limit := intArg(args, "limit", 100)
	if limit < 1 || limit > 1000 {
		return nil, consumerErr(400, "limit must be 1-1000")
	}
	tx, err := a.ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	floor, high, err := journalBounds(tx, scope)
	if err != nil {
		return nil, err
	}
	if action == "history" {
		after, err := sequenceArg(args)
		if err != nil {
			return nil, err
		}
		if _, provided := args["after_sequence"]; !provided {
			after = floor
		}
		if after < floor {
			return nil, consumerErr(410, fmt.Sprintf("history was pruned; earliest_sequence is %d", floor))
		}
		filter := strArg(args, "topic_filter")
		if filter == "" {
			filter = scopeTopicRoot(scope) + "#"
		}
		if !scopedFilter(scope, filter) {
			return nil, consumerErr(400, "topic_filter must be in ownership scope")
		}
		records, next, err := journalPage(tx, scope, filter, after, high, limit)
		if err != nil {
			return nil, err
		}
		return map[string]any{"messages": records, "next_sequence": strconv.FormatInt(next, 10), "earliest_sequence": strconv.FormatInt(floor, 10), "high_sequence": strconv.FormatInt(high, 10), "has_more": next < high}, nil
	}
	if action == "create" {
		filter := strArg(args, "topic_filter")
		if !scopedFilter(scope, filter) {
			return nil, consumerErr(400, "topic_filter must be in ownership scope")
		}
		start := strArg(args, "start")
		cursor := floor
		switch start {
		case "", "earliest":
		case "latest":
			cursor = high
		case "after":
			cursor, err = sequenceArg(args)
			if err != nil {
				return nil, err
			}
			if cursor < floor || cursor > high {
				return nil, consumerErr(410, "requested history is unavailable")
			}
		default:
			return nil, consumerErr(400, "start must be earliest, latest, or after")
		}
		var count int
		if err = tx.QueryRow(`SELECT count(*) FROM mqtt_consumers WHERE scope_id=?`, scope).Scan(&count); err != nil {
			return nil, err
		}
		if count >= 1000 {
			return nil, consumerErr(429, "consumer capacity reached")
		}
		_, err = tx.Exec(`INSERT INTO mqtt_consumers(scope_id,owner,name,topic_filter,cursor) VALUES(?,?,?,?,?) ON CONFLICT(scope_id,owner,name) DO NOTHING`, scope, owner, name, filter, cursor)
		if err != nil {
			return nil, err
		}
	}
	var filter, token string
	var cursor, end, until, retry int64
	var attempts int
	err = tx.QueryRow(`SELECT topic_filter,cursor,lease_end,lease_token,lease_until,attempts,retry_at FROM mqtt_consumers WHERE scope_id=? AND owner=? AND name=?`, scope, owner, name).Scan(&filter, &cursor, &end, &token, &until, &attempts, &retry)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, consumerErr(404, "consumer not found")
	}
	if err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	out := map[string]any{"name": name, "topic_filter": filter, "committed_sequence": strconv.FormatInt(cursor, 10), "earliest_sequence": strconv.FormatInt(floor, 10)}
	switch action {
	case "create":
		if strArg(args, "topic_filter") != filter {
			return nil, consumerErr(409, "consumer exists with another topic_filter")
		}
	case "status":
		out["lease_until"] = until
		out["attempts"] = attempts
		out["retry_at"] = retry
	case "claim":
		if token != "" && until > now {
			return nil, consumerErr(409, "batch is already leased")
		}
		if retry > now {
			return nil, consumerErr(429, "consumer retry delay is active")
		}
		seconds := intArg(args, "lease_seconds", 60)
		if seconds < 1 || seconds > 3600 {
			return nil, consumerErr(400, "lease_seconds must be 1-3600")
		}
		// Reclaim exactly the outstanding range after an expired/retried lease.
		bound := high
		if end > cursor {
			bound = end
		}
		records, next, err := journalPage(tx, scope, filter, cursor, bound, limit)
		if err != nil {
			return nil, err
		}
		if len(records) == 0 {
			_, err = tx.Exec(`UPDATE mqtt_consumers SET cursor=?,lease_end=0,lease_token='',lease_until=0,retry_at=0 WHERE scope_id=? AND owner=? AND name=?`, next, scope, owner, name)
			out["committed_sequence"] = strconv.FormatInt(next, 10)
			out["messages"] = records
		} else {
			token = randomPassword(40)
			attempts++
			_, err = tx.Exec(`UPDATE mqtt_consumers SET lease_end=?,lease_token=?,lease_until=?,attempts=?,retry_at=0 WHERE scope_id=? AND owner=? AND name=?`, next, token, now+int64(seconds), attempts, scope, owner, name)
			out["messages"] = records
			out["lease_token"] = token
			out["lease_until"] = now + int64(seconds)
			out["attempt"] = attempts
			out["batch_end_sequence"] = strconv.FormatInt(next, 10)
		}
		if err != nil {
			return nil, err
		}
	case "ack", "retry":
		if token == "" || until <= now || strArg(args, "lease_token") != token {
			return nil, consumerErr(409, "stale or expired lease token")
		}
		if action == "ack" {
			_, err = tx.Exec(`UPDATE mqtt_consumers SET cursor=lease_end,lease_end=0,lease_token='',lease_until=0,attempts=0,retry_at=0 WHERE scope_id=? AND owner=? AND name=?`, scope, owner, name)
			out["committed_sequence"] = strconv.FormatInt(end, 10)
		} else {
			delay := intArg(args, "retry_seconds", 0)
			if delay < 0 || delay > 86400 {
				return nil, consumerErr(400, "retry_seconds must be 0-86400")
			}
			_, err = tx.Exec(`UPDATE mqtt_consumers SET lease_token='',lease_until=0,retry_at=? WHERE scope_id=? AND owner=? AND name=?`, now+int64(delay), scope, owner, name)
		}
		if err != nil {
			return nil, err
		}
	case "replay":
		after, err := sequenceArg(args)
		if err != nil {
			return nil, err
		}
		if after < floor || after > high {
			return nil, consumerErr(410, "requested history is unavailable")
		}
		_, err = tx.Exec(`UPDATE mqtt_consumers SET cursor=?,lease_end=0,lease_token='',lease_until=0,attempts=0,retry_at=0 WHERE scope_id=? AND owner=? AND name=?`, after, scope, owner, name)
		if err != nil {
			return nil, err
		}
		out["committed_sequence"] = strconv.FormatInt(after, 10)
	case "delete":
		if _, err = tx.Exec(`DELETE FROM mqtt_consumers WHERE scope_id=? AND owner=? AND name=?`, scope, owner, name); err != nil {
			return nil, err
		}
	default:
		return nil, consumerErr(400, "unknown consumer action")
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}
func (a *App) handleConsumers(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		httpErr(w, 405, "POST only")
		return
	}
	owner, err := httpConsumerOwner(r)
	if err != nil {
		httpErr(w, 403, err.Error())
		return
	}
	args := map[string]any{}
	if err = json.NewDecoder(http.MaxBytesReader(w, r.Body, 65536)).Decode(&args); err != nil {
		httpErr(w, 400, "invalid JSON")
		return
	}
	result, err := a.consumerAction(projectScope(a.requestContext(r)), owner, args)
	if err != nil {
		status := 503
		var e *consumerError
		if errors.As(err, &e) {
			status = e.status
		}
		httpErr(w, status, err.Error())
		return
	}
	writeJSON(w, result)
}
func (a *App) consumerTool() sdk.Tool {
	return sdk.Tool{Name: "mqtt_consumer", Description: "Durable application consumption: create a named consumer, claim a leased batch, ack all records after processing, retry, replay from after_sequence, inspect status, delete, or read paginated history. At-least-once; deduplicate message_id. Consumer ownership comes from the authenticated caller. Payloads are complete base64. Sequences are decimal strings.", InputSchema: schemaObj(map[string]any{
		"action": map[string]any{"type": "string", "enum": []string{"create", "status", "claim", "ack", "retry", "replay", "delete", "history"}}, "name": map[string]any{"type": "string"}, "topic_filter": map[string]any{"type": "string"}, "start": map[string]any{"type": "string", "enum": []string{"earliest", "latest", "after"}}, "after_sequence": map[string]any{"type": "string"}, "lease_token": map[string]any{"type": "string"}, "limit": map[string]any{"type": "integer"}, "lease_seconds": map[string]any{"type": "integer"}, "retry_seconds": map[string]any{"type": "integer"}}, []string{"action"}), HandlerCtx: func(call context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
		owner, err := consumerOwner(sdk.CallerFrom(call))
		if err != nil {
			return nil, err
		}
		return a.consumerAction(projectScope(ctx), owner, args)
	}}
}
