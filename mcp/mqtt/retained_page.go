package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	"github.com/mochi-mqtt/server/v2/hooks/storage"
)

const retainedScanLimit = 1000
const retainedPageBytes = 4 << 20
const retainedCursorMaxBytes = 2 << 20
const retainedCursorTTL = 15 * 60

type retainedReadError struct {
	status  int
	message string
}

func (e *retainedReadError) Error() string             { return e.message }
func retainedReadErr(status int, message string) error { return &retainedReadError{status, message} }

type retainedPageRequest struct {
	TopicPattern string `json:"topic_pattern"`
	Cursor       string `json:"cursor"`
	Limit        int    `json:"limit"`
	patternSet   bool
}
type retainedCursor struct {
	Version       int    `json:"v"`
	Scope         string `json:"scope"`
	Filter        string `json:"filter"`
	After         string `json:"after"`
	Issued        int64  `json:"issued_at"`
	Until         int64  `json:"expires_at"`
	StartSequence int64  `json:"start_sequence"`
}
type retainedPage struct {
	Messages        []retainedMessage `json:"messages"`
	NextCursor      string            `json:"next_cursor"`
	HasMore         bool              `json:"has_more"`
	TopicPattern    string            `json:"topic_pattern"`
	AsOf            int64             `json:"as_of"`
	StartSequence   string            `json:"start_sequence"`
	HighSequence    string            `json:"high_sequence"`
	CursorExpiresAt int64             `json:"cursor_expires_at,omitempty"`
	Consistency     string            `json:"consistency"`
	Scanned         int               `json:"scanned"`
}

func retainedRequest(args map[string]any) (retainedPageRequest, error) {
	r := retainedPageRequest{Limit: 100}
	if v, ok := args["topic_pattern"]; ok {
		s, ok := v.(string)
		if !ok {
			return r, retainedReadErr(400, "topic_pattern must be a string")
		}
		r.TopicPattern = s
		r.patternSet = true
	}
	if v, ok := args["cursor"]; ok {
		s, ok := v.(string)
		if !ok {
			return r, retainedReadErr(400, "cursor must be a string")
		}
		if len(s) > retainedCursorMaxBytes {
			return r, retainedReadErr(400, "cursor is too large")
		}
		r.Cursor = s
	}
	if v, ok := args["limit"]; ok {
		switch n := v.(type) {
		case int:
			r.Limit = n
		case int64:
			if n < 1 || n > 1000 {
				return r, retainedReadErr(400, "limit must be an integer from 1 to 1000")
			}
			r.Limit = int(n)
		case float64:
			if math.Trunc(n) != n || n < 1 || n > 1000 {
				return r, retainedReadErr(400, "limit must be an integer from 1 to 1000")
			}
			r.Limit = int(n)
		case json.Number:
			i, err := n.Float64()
			if err != nil || math.Trunc(i) != i || i < 1 || i > 1000 {
				return r, retainedReadErr(400, "limit must be an integer from 1 to 1000")
			}
			r.Limit = int(i)
		default:
			return r, retainedReadErr(400, "limit must be an integer from 1 to 1000")
		}
	}
	if r.Limit < 1 || r.Limit > 1000 {
		return r, retainedReadErr(400, "limit must be an integer from 1 to 1000")
	}
	return r, nil
}
func signRetainedCursor(key []byte, c retainedCursor) (string, error) {
	data, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("mqtt-retained-page/v1\x00"))
	mac.Write(data)
	return base64.RawURLEncoding.EncodeToString(data) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}
func parseRetainedCursor(key []byte, token, scope string, now int64) (retainedCursor, error) {
	c := retainedCursor{}
	invalid := func() (retainedCursor, error) {
		return c, retainedReadErr(400, "invalid retained cursor for this ownership scope")
	}
	if len(token) > retainedCursorMaxBytes {
		return invalid()
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return invalid()
	}
	data, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return invalid()
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return invalid()
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("mqtt-retained-page/v1\x00"))
	mac.Write(data)
	if base64.RawURLEncoding.EncodeToString(data) != parts[0] || base64.RawURLEncoding.EncodeToString(signature) != parts[1] || !hmac.Equal(signature, mac.Sum(nil)) {
		return invalid()
	}
	if err = json.Unmarshal(data, &c); err != nil {
		return invalid()
	}
	if c.Version != 1 || c.Scope != scope || !scopedFilter(scope, c.Filter) || !scopedTopic(scope, c.After) || c.StartSequence < 0 || c.Until-c.Issued != retainedCursorTTL {
		return invalid()
	}
	if now < c.Issued || now >= c.Until {
		return c, retainedReadErr(410, "retained cursor expired; restart the retained scan")
	}
	return c, nil
}

// Live keyset pagination holds a SQLite read transaction only for one bounded
// page, never across requests. A signed cursor is a position, not authorization.
func readRetainedPage(db *sql.DB, scope string, r retainedPageRequest, now int64) (retainedPage, error) {
	out := retainedPage{Messages: []retainedMessage{}, Consistency: "live", AsOf: now}
	if !validScope(scope) {
		return out, retainedReadErr(403, "authenticated ownership scope required")
	}
	if r.Limit == 0 {
		r.Limit = 100
	}
	if r.Limit < 1 || r.Limit > 1000 {
		return out, retainedReadErr(400, "limit must be an integer from 1 to 1000")
	}
	key, err := retainedCursorKey(db)
	if err != nil {
		return out, err
	}
	tx, err := db.Begin()
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	if now == 0 {
		now = time.Now().Unix()
		out.AsOf = now
	}
	_, high, err := journalBounds(tx, scope)
	if err != nil {
		return out, err
	}
	c := retainedCursor{Version: 1, Scope: scope, Filter: r.TopicPattern, Issued: now, Until: now + retainedCursorTTL, StartSequence: high}
	if r.Cursor != "" {
		c, err = parseRetainedCursor(key, r.Cursor, scope, now)
		if err != nil {
			return out, err
		}
		if r.patternSet || r.TopicPattern != "" {
			filter := r.TopicPattern
			if filter == "" {
				filter = scopeTopicRoot(scope) + "#"
			}
			if filter != c.Filter {
				return out, retainedReadErr(400, "retained cursor does not match topic_pattern")
			}
		}
	} else {
		if c.Filter == "" {
			c.Filter = scopeTopicRoot(scope) + "#"
		}
		if !scopedFilter(scope, c.Filter) {
			return out, retainedReadErr(400, "topic_pattern must be a valid filter in the ownership scope")
		}
	}
	out.TopicPattern = c.Filter
	out.StartSequence = strconv.FormatInt(c.StartSequence, 10)
	out.HighSequence = strconv.FormatInt(high, 10)
	// Scan metadata first; sparse filters must not load discarded payloads.
	rows, err := tx.Query(`SELECT topic FROM mqtt_retained WHERE scope_id=? AND topic>? COLLATE BINARY AND (expires_at=0 OR expires_at>?) ORDER BY topic COLLATE BINARY LIMIT ?`, scope, c.After, now, retainedScanLimit+1)
	if err != nil {
		return out, err
	}
	topics := []string{}
	for rows.Next() {
		var topic string
		if err = rows.Scan(&topic); err != nil {
			rows.Close()
			return out, err
		}
		topics = append(topics, topic)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	bytes := 0
	for i, topic := range topics {
		if i == retainedScanLimit || len(out.Messages) == r.Limit {
			out.HasMore = true
			break
		}
		if !scopedTopic(scope, topic) {
			return out, errors.New("invalid retained ownership namespace")
		}
		if !mqttTopicMatch(c.Filter, topic) {
			c.After = topic
			out.Scanned++
			continue
		}
		message, err := getRetainedTx(tx, scope, topic, now)
		if err != nil {
			return out, err
		}
		data, err := json.Marshal(message)
		if err != nil {
			return out, err
		}
		if len(out.Messages) > 0 && bytes+len(data) > retainedPageBytes {
			out.HasMore = true
			break
		}
		out.Messages = append(out.Messages, *message)
		bytes += len(data)
		c.After = topic
		out.Scanned++
	}
	if out.HasMore {
		out.NextCursor, err = signRetainedCursor(key, c)
		if err != nil {
			return out, err
		}
		out.CursorExpiresAt = c.Until
	}
	return out, nil
}

type retainedQuerier interface{ QueryRow(string, ...any) *sql.Row }

func getRetainedTx(db retainedQuerier, scope, topic string, now int64) (*retainedMessage, error) {
	if !scopedTopic(scope, topic) {
		return nil, retainedReadErr(404, "retained message not found")
	}
	var payload []byte
	var updated, raw string
	var qos int
	var expiry int64
	err := db.QueryRow(`SELECT payload,qos,properties,updated_at,expires_at FROM mqtt_retained WHERE scope_id=? AND topic=? AND (expires_at=0 OR expires_at>CASE WHEN ?=0 THEN unixepoch() ELSE ? END)`, scope, topic, now, now).Scan(&payload, &qos, &raw, &updated, &expiry)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, retainedReadErr(404, "retained message not found")
	}
	if err != nil {
		return nil, err
	}
	var properties storage.MessageProperties
	if raw != "" {
		if err = json.Unmarshal([]byte(raw), &properties); err != nil {
			return nil, fmt.Errorf("invalid retained properties: %w", err)
		}
	}
	properties.TopicAlias = 0 // connection aliases cannot be reused by a reader.
	message := makeRetainedMessage(topic, payload, len(payload), qos, updated, true)
	encoded := base64.StdEncoding.EncodeToString(payload)
	message.PayloadBase64 = &encoded
	message.Properties = &properties
	message.ExpiresAt = expiry
	message.Retain = true
	for _, property := range properties.User {
		if property.Key == "apteva.message_id" {
			message.MessageID = property.Val
			break
		}
	}
	return &message, nil
}
func (a *App) toolRetainedPage(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	r, err := retainedRequest(args)
	if err != nil {
		return nil, err
	}
	page, err := readRetainedPage(a.ctx.AppDB(), projectScope(ctx), r, 0)
	if err != nil {
		return nil, err
	}
	return page, nil
}
func (a *App) handleRetainedPage(w http.ResponseWriter, r *http.Request) {
	args := map[string]any{}
	switch r.Method {
	case http.MethodGet:
		q, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil {
			retainedHTTPError(w, retainedReadErr(400, "invalid retained paging query"))
			return
		}
		for _, key := range []string{"topic_pattern", "cursor", "limit"} {
			values, ok := q[key]
			if !ok {
				continue
			}
			if len(values) != 1 {
				retainedHTTPError(w, retainedReadErr(400, "duplicate retained paging parameter"))
				return
			}
			if key == "limit" {
				n, err := strconv.Atoi(values[0])
				if err != nil {
					retainedHTTPError(w, retainedReadErr(400, "invalid limit"))
					return
				}
				args[key] = n
			} else {
				args[key] = values[0]
			}
		}
	case http.MethodPost:
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, retainedCursorMaxBytes+65536))
		decoder.UseNumber()
		if err := decoder.Decode(&args); err != nil || args == nil {
			retainedHTTPError(w, retainedReadErr(400, "invalid JSON object"))
			return
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			retainedHTTPError(w, retainedReadErr(400, "trailing JSON"))
			return
		}
	default:
		retainedHTTPError(w, retainedReadErr(405, "GET or POST only"))
		return
	}
	result, err := a.toolRetainedPage(a.requestContext(r), args)
	if err != nil {
		retainedHTTPError(w, err)
		return
	}
	writeJSON(w, result)
}
func retainedHTTPError(w http.ResponseWriter, err error) {
	status := 503
	var e *retainedReadError
	if errors.As(err, &e) {
		status = e.status
	}
	httpErr(w, status, err.Error())
}

// Generate cursor secrets with the OS CSPRNG, never a database PRNG. The unique
// singleton row arbitrates concurrent initialization and survives restart.
func retainedCursorKey(db *sql.DB) ([]byte, error) {
	var key []byte
	err := db.QueryRow(`SELECT secret FROM mqtt_retained_read_key WHERE id=1`).Scan(&key)
	if errors.Is(err, sql.ErrNoRows) {
		candidate := make([]byte, 32)
		if _, err = rand.Read(candidate); err != nil {
			return nil, err
		}
		if _, err = db.Exec(`INSERT OR IGNORE INTO mqtt_retained_read_key(id,secret) VALUES(1,?)`, candidate); err != nil {
			return nil, err
		}
		err = db.QueryRow(`SELECT secret FROM mqtt_retained_read_key WHERE id=1`).Scan(&key)
	}
	if err != nil {
		return nil, err
	}
	if len(key) != 32 {
		return nil, errors.New("invalid retained cursor signing key")
	}
	return key, nil
}
