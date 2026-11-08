package main

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	sdk "github.com/apteva/app-sdk"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Cursors are scoped and filter-bound. They are positions, never credentials.
type queryCursor struct {
	After   int64  `json:"after"`
	Through int64  `json:"through"`
	Filter  string `json:"filter"`
}
type Page struct {
	Items      []any  `json:"items"`
	NextCursor string `json:"next_cursor,omitempty"`
}

func (a *App) extraTools() []sdk.Tool {
	queryProps := map[string]any{"device_id": stringProp(), "kind": stringProp(), "from": stringProp(), "to": stringProp(), "cursor": stringProp(), "limit": intProp()}
	tools := []sdk.Tool{}
	for _, kind := range []string{"data", "events", "usage", "responses"} {
		k := kind
		tools = append(tools, tool("devices_"+k+"_list", "Query scoped "+k+" with stable cursor pagination.", schema(queryProps, nil), func(_ *sdk.AppCtx, args map[string]any) (any, error) { return a.queryHistory(k, args) }))
	}
	tools = append(tools,
		tool("devices_operation_get", "Get lifecycle status and recover a credential within its recovery window.", schema(map[string]any{"operation_id": stringProp()}, []string{"operation_id"}), func(_ *sdk.AppCtx, args map[string]any) (any, error) {
			return a.operationResult(str(args, "operation_id"))
		}),
		tool("devices_state_query", "Query current state with key cursor pagination.", schema(map[string]any{"device_id": stringProp(), "cursor": stringProp(), "limit": intProp()}, []string{"device_id"}), func(_ *sdk.AppCtx, args map[string]any) (any, error) { return a.queryState(args) }),
		tool("devices_limits_get", "Get active operational limits.", schema(map[string]any{}, nil), func(_ *sdk.AppCtx, _ map[string]any) (any, error) { return a.activeLimits(), nil }),
	)
	tools = append(tools, a.ingressPolicyTools()...)
	return tools
}
func (a *App) extraRoutes() []sdk.Route {
	routes := []sdk.Route{}
	for _, kind := range []string{"data", "events", "usage", "responses"} {
		k := kind
		path := "/" + k
		if k == "events" {
			path = "/history/events"
		}
		routes = append(routes, sdk.Route{Pattern: path, Handler: func(w http.ResponseWriter, r *http.Request) {
			if !requireMethod(w, r, http.MethodGet) {
				return
			}
			args := queryArgs(r)
			result, err := a.queryHistory(k, args)
			respond(w, result, err)
		}})
	}
	routes = append(routes,
		sdk.Route{Pattern: "/operations/", Handler: func(w http.ResponseWriter, r *http.Request) {
			if !requireMethod(w, r, http.MethodGet) {
				return
			}
			result, err := a.operationResult(strings.TrimPrefix(r.URL.Path, "/operations/"))
			respond(w, result, err)
		}},
		sdk.Route{Pattern: "/state", Handler: func(w http.ResponseWriter, r *http.Request) {
			if !requireMethod(w, r, http.MethodGet) {
				return
			}
			result, err := a.queryState(queryArgs(r))
			respond(w, result, err)
		}},
		sdk.Route{Pattern: "/limits", Handler: func(w http.ResponseWriter, r *http.Request) {
			if !requireMethod(w, r, http.MethodGet) {
				return
			}
			writeJSON(w, 200, a.activeLimits())
		}},
	)
	routes = append(routes, sdk.Route{Pattern: "/usage/summary", Handler: a.handleUsageSummary}, sdk.Route{Pattern: "/policy", Handler: a.handlePolicy})
	return routes
}
func queryArgs(r *http.Request) map[string]any {
	args := map[string]any{}
	for _, k := range []string{"device_id", "kind", "from", "to", "cursor"} {
		args[k] = r.URL.Query().Get(k)
	}
	n, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	args["limit"] = n
	return args
}
func (a *App) queryHistory(kind string, args map[string]any) (Page, error) {
	page := Page{Items: []any{}}
	db := a.ctx.AppDB()
	device := str(args, "device_id")
	filterKind := str(args, "kind")
	// History events/usage deliberately survive deletion; scope is on every row.
	table, columns, timeColumn := "", "", "created_at"
	switch kind {
	case "data":
		table = "device_messages"
		columns = "id,message_id,device_id,kind,data_json,metadata_json,observed_at,received_at,session_id,sequence"
		timeColumn = "observed_at"
	case "events":
		table = "device_outbox"
		columns = "seq,event_id,device_id,topic,data_json,created_at"
	case "responses":
		table = "device_responses"
		columns = "seq,device_id,command_id,response_json,disposition,created_at"
	case "usage":
		table = "device_usage"
		columns = "seq,device_id,measurement,quantity,source_id,created_at"
	default:
		return page, errors.New("unknown query")
	}
	idColumn := "seq"
	if kind == "data" {
		idColumn = "id"
	}
	filter := fingerprint(map[string]any{"scope": a.projectID, "kind": kind, "device": device, "filter": filterKind, "from": str(args, "from"), "to": str(args, "to")})
	cursor := queryCursor{Filter: filter}
	if raw := str(args, "cursor"); raw != "" {
		decoded, e := base64.RawURLEncoding.DecodeString(raw)
		if e != nil {
			return page, errors.New("invalid cursor")
		}
		if e = json.Unmarshal(decoded, &cursor); e != nil || cursor.Filter != filter || cursor.After < 0 || cursor.Through < cursor.After {
			return page, errors.New("cursor scope or filters mismatch")
		}
	} else {
		if err := db.QueryRow(`SELECT COALESCE(MAX(`+idColumn+`),0) FROM `+table+` WHERE project_id=?`, a.projectID).Scan(&cursor.Through); err != nil {
			return page, err
		}
	}
	if str(args, "cursor") != "" {
		var floor int64
		e := db.QueryRow(`SELECT through_seq FROM device_retention WHERE project_id=? AND kind=?`, a.projectID, kind).Scan(&floor)
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return page, e
		}
		if cursor.After < floor {
			return page, ErrCursorExpired
		}
	}
	query := `SELECT ` + columns + ` FROM ` + table + ` WHERE project_id=? AND ` + idColumn + `>? AND ` + idColumn + `<=?`
	params := []any{a.projectID, cursor.After, cursor.Through}
	if device != "" {
		query += ` AND device_id=?`
		params = append(params, device)
	}
	if filterKind != "" {
		field := "kind"
		if kind == "events" {
			field = "topic"
		}
		if kind == "responses" {
			field = "disposition"
		}
		if kind == "usage" {
			field = "measurement"
		}
		query += ` AND ` + field + `=?`
		params = append(params, filterKind)
	}
	for _, v := range []struct{ key, op string }{{"from", ">="}, {"to", "<="}} {
		if value := str(args, v.key); value != "" {
			at, err := parseQueryTime(value)
			if err != nil {
				return page, err
			}
			query += ` AND ` + timeColumn + v.op + `?`
			params = append(params, at)
		}
	}
	limit := integer(args, "limit", 100)
	if limit < 1 || limit > 500 {
		limit = 100
	}
	query += ` ORDER BY ` + idColumn + ` LIMIT ?`
	params = append(params, limit+1)
	rows, err := db.Query(query, params...)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	last := cursor.After
	for rows.Next() {
		var seq int64
		var item any
		switch kind {
		case "data":
			var mid, device, k, data, metadata, observed, received, session string
			var sequence sql.NullInt64
			err = rows.Scan(&seq, &mid, &device, &k, &data, &metadata, &observed, &received, &session, &sequence)
			var value any
			_ = decodeJSON(data, &value)
			m := map[string]any{"id": seq, "scope_id": a.projectID, "message_id": mid, "device_id": device, "kind": k, "data": value, "metadata": jsonObject(metadata), "observed_at": observed, "received_at": received, "session_id": session}
			if sequence.Valid {
				m["sequence"] = sequence.Int64
			}
			item = m
		case "events":
			var id, device, topic, data, at string
			err = rows.Scan(&seq, &id, &device, &topic, &data, &at)
			item = map[string]any{"seq": seq, "event_id": id, "device_id": device, "topic": topic, "data": jsonObject(data), "created_at": at}
		case "responses":
			var device, command, raw, disposition, at string
			err = rows.Scan(&seq, &device, &command, &raw, &disposition, &at)
			item = map[string]any{"seq": seq, "scope_id": a.projectID, "device_id": device, "command_id": command, "response": jsonObject(raw), "disposition": disposition, "created_at": at}
		case "usage":
			var device, measurement, source, at string
			var quantity int64
			err = rows.Scan(&seq, &device, &measurement, &quantity, &source, &at)
			item = map[string]any{"seq": seq, "scope_id": a.projectID, "device_id": device, "measurement": measurement, "quantity": quantity, "source_id": source, "created_at": at}
		}
		if err != nil {
			return page, err
		}
		if len(page.Items) == limit {
			cursor.After = last
			raw, _ := json.Marshal(cursor)
			page.NextCursor = base64.RawURLEncoding.EncodeToString(raw)
			break
		}
		page.Items = append(page.Items, item)
		last = seq
	}
	return page, rows.Err()
}
func (a *App) queryState(args map[string]any) (Page, error) {
	page := Page{Items: []any{}}
	id := str(args, "device_id")
	if _, err := getDevice(a.ctx.AppDB(), a.projectID, id, false); err != nil {
		return page, err
	}
	after := ""
	filter := fingerprint([]string{a.projectID, id})
	if token := str(args, "cursor"); token != "" {
		raw, e := base64.RawURLEncoding.DecodeString(token)
		if e != nil {
			return page, errors.New("invalid cursor")
		}
		var c []string
		if e = json.Unmarshal(raw, &c); e != nil || len(c) != 2 || c[0] != filter {
			return page, errors.New("cursor scope mismatch")
		}
		after = c[1]
	}
	limit := integer(args, "limit", 100)
	if limit < 1 || limit > 500 {
		limit = 100
	}
	rows, err := a.ctx.AppDB().Query(`SELECT key,value_json,value_type,unit,source,updated_at,observed_at,message_id FROM device_state WHERE device_id=? AND key>? ORDER BY key LIMIT ?`, id, after, limit+1)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	last := after
	for rows.Next() {
		var s StateValue
		var raw string
		if err = rows.Scan(&s.Key, &raw, &s.ValueType, &s.Unit, &s.Source, &s.UpdatedAt, &s.ObservedAt, &s.MessageID); err != nil {
			return page, err
		}
		_ = decodeJSON(raw, &s.Value)
		if len(page.Items) == limit {
			raw, _ := json.Marshal([]string{filter, last})
			page.NextCursor = base64.RawURLEncoding.EncodeToString(raw)
			break
		}
		page.Items = append(page.Items, s)
		last = s.Key
	}
	return page, rows.Err()
}
func (a *App) baseLimits() map[string]int {
	return map[string]int{
		"max_transport_receipts": a.baseLimit("max_transport_receipts", 1000000), "max_ingest_rejections": a.baseLimit("max_ingest_rejections", 10000), "transport_receipt_retention_hours": a.baseLimit("transport_receipt_retention_hours", 168),
		"command_timeout_ms": a.baseLimit("command_timeout_ms", 10000), "max_http_credentials_per_device": a.baseLimit("max_http_credentials_per_device", 5), "max_http_credential_ttl_seconds": a.baseLimit("max_http_credential_ttl_seconds", 31536000),
		"operation_retention_days": a.baseLimit("operation_retention_days", 90), "max_state_bytes_per_device": a.baseLimit("max_state_bytes_per_device", 1048576),
		"max_pending_events": a.baseLimit("max_pending_events", 50000),
		"max_devices":        a.baseLimit("max_devices", 10000), "max_device_payload_bytes": a.baseLimit("max_device_payload_bytes", 131072), "max_state_keys_per_device": a.baseLimit("max_state_keys_per_device", 1024), "max_pending_commands": a.baseLimit("max_pending_commands", 10000), "max_pending_commands_per_device": a.baseLimit("max_pending_commands_per_device", 100), "max_concurrent_commands": a.baseLimit("max_concurrent_commands", 100), "max_concurrent_commands_per_device": a.baseLimit("max_concurrent_commands_per_device", 1), "max_history_bytes_per_device": a.baseLimit("max_history_bytes_per_device", 67108864), "telemetry_retention_hours": a.baseLimit("telemetry_retention_hours", 168), "telemetry_max_rows_per_device": a.baseLimit("telemetry_max_rows_per_device", 10000), "event_retention_days": a.baseLimit("event_retention_days", 90), "usage_retention_days": a.baseLimit("usage_retention_days", 365), "command_retention_days": a.baseLimit("command_retention_days", 90), "secret_recovery_hours": a.baseLimit("secret_recovery_hours", 24), "max_clock_skew_seconds": a.baseLimit("max_clock_skew_seconds", 300),
	}
}

func parseQueryTime(value string) (string, error) {
	at, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return "", errors.New("time filter must be RFC3339")
	}
	return formatTime(at), nil
}
