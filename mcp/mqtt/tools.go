// tools.go — MCP tool handlers. Each tool is a thin wrapper around
// the underlying DB helpers + broker; argument parsing happens here
// so the helpers stay reusable from the HTTP routes too.

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"sort"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	mqtt "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/packets"
)

// MCPTools registers the tool set. Schemas are minimal — descriptive
// `description` carries the operator-facing detail.
func (a *App) mcpTools() []sdk.Tool {
	return []sdk.Tool{
		{
			Name:        "mqtt_publish",
			Description: "Publish one MQTT message. Payload may be a string or JSON value; non-strings are JSON-encoded. Args: topic, payload, retain, qos.",
			InputSchema: schemaObj(map[string]any{
				"topic":   map[string]any{"type": "string"},
				"payload": map[string]any{"description": "String or JSON value; non-strings are JSON-encoded."},
				"retain":  map[string]any{"type": "boolean"},
				"qos":     map[string]any{"type": "integer", "minimum": 0, "maximum": 2},
			}, []string{"topic"}),
			Handler: a.toolPublish,
		},
		{
			Name:        "mqtt_topics_recent",
			Description: "Distinct topics seen by the broker recently. Args: limit (int, default 50).",
			InputSchema: schemaObj(map[string]any{
				"limit": map[string]any{"type": "integer"},
			}, nil),
			Handler: a.toolTopicsRecent,
		},
		{
			Name:        "mqtt_clients_list",
			Description: "List currently connected network MQTT clients with authenticated identity and protocol metadata.",
			InputSchema: schemaObj(nil, nil),
			Handler: func(_ *sdk.AppCtx, _ map[string]any) (any, error) {
				return a.snapshotClients(), nil
			},
		},
		{
			Name:        "mqtt_subscribe",
			Description: "Bridge an MQTT topic pattern to the platform event bus as `mqtt.<bus_topic>`. Args: topic_pattern (MQTT-style, e.g. motion/+/state), bus_topic (a-z0-9._-).",
			InputSchema: schemaObj(map[string]any{
				"topic_pattern": map[string]any{"type": "string"},
				"bus_topic":     map[string]any{"type": "string"},
			}, []string{"topic_pattern", "bus_topic"}),
			Handler: a.toolSubscribe,
		},
		{
			Name:        "mqtt_subscribe_ensure",
			Description: "Idempotently ensure an MQTT-to-platform event bridge exists. Repeated calls return the same subscription. Args: topic_pattern, bus_topic.",
			InputSchema: schemaObj(map[string]any{
				"topic_pattern": map[string]any{"type": "string"},
				"bus_topic":     map[string]any{"type": "string"},
			}, []string{"topic_pattern", "bus_topic"}),
			Handler: a.toolSubscribe,
		},
		{
			Name:        "mqtt_subscribe_list",
			Description: "List bus-bridge subscriptions.",
			InputSchema: schemaObj(nil, nil),
			Handler:     a.toolSubscribeList,
		},
		{
			Name:        "mqtt_subscribe_delete",
			Description: "Delete a bus-bridge subscription. Args: id.",
			InputSchema: schemaObj(map[string]any{"id": map[string]any{"type": "integer"}}, []string{"id"}),
			Handler:     a.toolSubscribeDelete,
		},
		{
			Name:        "mqtt_users_add",
			Description: "Legacy create-or-replace operation. Prefer mqtt_users_create, mqtt_users_update_acl, and mqtt_users_rotate_password.",
			InputSchema: schemaObj(map[string]any{
				"username":        map[string]any{"type": "string"},
				"password":        map[string]any{"type": "string"},
				"allow_publish":   map[string]any{"type": "array"},
				"allow_subscribe": map[string]any{"type": "array"},
			}, []string{"username", "password"}),
			Handler: a.toolUsersAdd,
		},
		{
			Name:        "mqtt_users_create",
			Description: "Create a new MQTT user without replacing an existing credential. Args: username, password, allow_publish, allow_subscribe.",
			InputSchema: userWriteSchema(),
			Handler:     a.toolUsersCreate,
		},
		{
			Name:        "mqtt_users_update_acl",
			Description: "Update an MQTT user's publish and/or subscribe ACL without changing its password.",
			InputSchema: schemaObj(map[string]any{
				"username":        map[string]any{"type": "string"},
				"allow_publish":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"allow_subscribe": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			}, []string{"username"}),
			Handler: a.toolUsersUpdateACL,
		},
		{
			Name:        "mqtt_users_rotate_password",
			Description: "Replace an MQTT user's password without changing ACLs; active sessions using that user are disconnected.",
			InputSchema: schemaObj(map[string]any{
				"username": map[string]any{"type": "string"},
				"password": map[string]any{"type": "string"},
			}, []string{"username", "password"}),
			Handler: a.toolUsersRotatePassword,
		},
		{
			Name:        "mqtt_users_list",
			Description: "List broker users (passwords never returned).",
			InputSchema: schemaObj(nil, nil),
			Handler:     a.toolUsersList,
		},
		{
			Name:        "mqtt_users_delete",
			Description: "Delete a broker user. Args: username.",
			InputSchema: schemaObj(map[string]any{"username": map[string]any{"type": "string"}}, []string{"username"}),
			Handler:     a.toolUsersDelete,
		},
		{
			Name:        "mqtt_users_set_enabled",
			Description: "Enable or disable a broker user. Args: username, enabled (bool).",
			InputSchema: schemaObj(map[string]any{
				"username": map[string]any{"type": "string"},
				"enabled":  map[string]any{"type": "boolean"},
			}, []string{"username", "enabled"}),
			Handler: a.toolUsersSetEnabled,
		},
		{
			Name:        "mqtt_retained_list",
			Description: "List retained MQTT state. Args: topic_pattern (optional), limit (default 100, max 1000).",
			InputSchema: schemaObj(map[string]any{
				"topic_pattern": map[string]any{"type": "string"},
				"limit":         map[string]any{"type": "integer", "minimum": 1, "maximum": 1000},
			}, nil),
			Handler: a.toolRetainedList,
		},
		{
			Name:        "mqtt_retained_get",
			Description: "Get one retained MQTT message by exact topic.",
			InputSchema: schemaObj(map[string]any{"topic": map[string]any{"type": "string"}}, []string{"topic"}),
			Handler:     a.toolRetainedGet,
		},
		{
			Name:        "mqtt_retained_delete",
			Description: "Delete one retained MQTT message by exact topic from memory and durable storage.",
			InputSchema: schemaObj(map[string]any{"topic": map[string]any{"type": "string"}}, []string{"topic"}),
			Handler:     a.toolRetainedDelete,
		},
		{
			Name:        "mqtt_devices",
			Description: "List HA-discovered devices. Args: filter (optional substring on slug, model, manufacturer).",
			InputSchema: schemaObj(map[string]any{"filter": map[string]any{"type": "string"}}, nil),
			Handler:     a.toolDevices,
		},
		{
			Name:        "mqtt_status",
			Description: "Broker endpoint, uptime, client/retained counts, traffic counters, rejection/drop metrics, and active safety limits.",
			InputSchema: schemaObj(nil, nil),
			Handler:     a.toolStatus,
		},
	}
}

func userWriteSchema() map[string]any {
	return schemaObj(map[string]any{
		"username":        map[string]any{"type": "string"},
		"password":        map[string]any{"type": "string"},
		"allow_publish":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		"allow_subscribe": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
	}, []string{"username", "password"})
}

func schemaObj(props map[string]any, required []string) map[string]any {
	out := map[string]any{"type": "object"}
	if props != nil {
		out["properties"] = props
	}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

// ─── handlers ──────────────────────────────────────────────────────

func (a *App) toolPublish(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	if a.broker == nil {
		return nil, errors.New("broker not running")
	}
	topic := strArg(args, "topic")
	if topic == "" {
		return nil, errors.New("topic required")
	}
	payloadValue, hasPayload := args["payload"]
	payload, err := encodePayload(payloadValue, hasPayload)
	if err != nil {
		return nil, fmt.Errorf("encode payload: %w", err)
	}
	retain := boolArg(args, "retain", false)
	qos := intArg(args, "qos", 0)
	if err := validatePublish(topic, qos); err != nil {
		return nil, err
	}
	if err := a.validatePayloadSize(payload); err != nil {
		return nil, err
	}
	if err := a.broker.Publish(topic, payload, retain, byte(qos)); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "topic": topic, "bytes": len(payload)}, nil
}

func (a *App) toolTopicsRecent(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	limit := intArg(args, "limit", 50)
	if limit <= 0 {
		limit = 50
	}
	if limit > 1000 {
		limit = 1000
	}
	rows, err := a.ctx.AppDB().Query(
		`SELECT topic, MAX(ts) AS last_ts, COUNT(*) AS n
		   FROM mqtt_message_log
		  GROUP BY topic
		  ORDER BY last_ts DESC
		  LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var topic, ts string
		var n int
		if err := rows.Scan(&topic, &ts, &n); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"topic": topic, "last_seen": ts, "count": n})
	}
	return out, rows.Err()
}

func (a *App) snapshotClients() []map[string]any {
	out := []map[string]any{}
	if a.broker != nil {
		for _, client := range a.broker.Clients() {
			client.RLock()
			row := map[string]any{
				"client_id":        client.ID,
				"username":         string(client.Properties.Username),
				"remote":           client.Net.Remote,
				"listener":         client.Net.Listener,
				"protocol_version": int(client.Properties.ProtocolVersion),
				"clean_session":    client.Properties.Clean,
			}
			client.RUnlock()
			out = append(out, row)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i]["client_id"].(string) < out[j]["client_id"].(string)
	})
	return out
}

func (a *App) toolSubscribe(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pat := strArg(args, "topic_pattern")
	bus := strArg(args, "bus_topic")
	sub, err := addBusSubscription(a.ctx.AppDB(), projectScope(ctx), pat, bus, callerLabel(ctx))
	if err != nil {
		return nil, err
	}
	// Wire it into the broker right now so the operator doesn't
	// have to wait for a restart.
	if a.broker == nil {
		return nil, errors.New("broker not running")
	}
	_ = a.broker.Unsubscribe(sub.TopicPattern, int(100+sub.ID))
	if err := a.broker.Subscribe(sub.TopicPattern, int(100+sub.ID),
		func(cl *mqtt.Client, _ packets.Subscription, pk packets.Packet) {
			a.emitBusMessageForProject(sub.BusTopic, sub.ProjectID, cl, pk)
		}); err != nil {
		return nil, err
	}
	return sub, nil
}

func (a *App) toolSubscribeList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	subs, err := listBusSubscriptions(a.ctx.AppDB(), projectScope(ctx))
	if err != nil {
		return nil, err
	}
	return subs, nil
}

func (a *App) toolSubscribeDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64(intArg(args, "id", 0))
	if id == 0 {
		return nil, errors.New("id required")
	}
	sub, err := deleteBusSubscription(a.ctx.AppDB(), projectScope(ctx), id)
	if err != nil {
		return nil, err
	}
	if a.broker != nil {
		_ = a.broker.Unsubscribe(sub.TopicPattern, int(100+sub.ID))
	}
	return map[string]any{"ok": true}, nil
}

func (a *App) toolUsersAdd(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	username := strings.TrimSpace(strArg(args, "username"))
	password := strArg(args, "password")
	pub := strArrayArg(args, "allow_publish")
	sub := strArrayArg(args, "allow_subscribe")
	if err := addUser(a.ctx.AppDB(), username, password, pub, sub); err != nil {
		return nil, err
	}
	if err := a.reloadUserCache(); err != nil {
		return nil, err
	}
	a.broker.DisconnectUser(username)
	return map[string]any{"ok": true, "username": username}, nil
}

func (a *App) toolUsersCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	username := strings.TrimSpace(strArg(args, "username"))
	if err := createUser(a.ctx.AppDB(), username, strArg(args, "password"),
		strArrayArg(args, "allow_publish"), strArrayArg(args, "allow_subscribe")); err != nil {
		return nil, err
	}
	if err := a.reloadUserCache(); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "username": username}, nil
}

func (a *App) toolUsersUpdateACL(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	username := strings.TrimSpace(strArg(args, "username"))
	var pub, sub []string
	if _, ok := args["allow_publish"]; ok {
		pub = strArrayArg(args, "allow_publish")
	}
	if _, ok := args["allow_subscribe"]; ok {
		sub = strArrayArg(args, "allow_subscribe")
	}
	if err := updateUserACL(a.ctx.AppDB(), username, pub, sub); err != nil {
		return nil, err
	}
	if err := a.reloadUserCache(); err != nil {
		return nil, err
	}
	a.broker.DisconnectUser(username)
	return map[string]any{"ok": true, "username": username}, nil
}

func (a *App) toolUsersRotatePassword(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	username := strings.TrimSpace(strArg(args, "username"))
	if err := rotateUserPassword(a.ctx.AppDB(), username, strArg(args, "password")); err != nil {
		return nil, err
	}
	if err := a.reloadUserCache(); err != nil {
		return nil, err
	}
	a.broker.DisconnectUser(username)
	return map[string]any{"ok": true, "username": username}, nil
}

func (a *App) toolUsersList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return listUsers(a.ctx.AppDB())
}

func (a *App) toolUsersDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	username := strings.TrimSpace(strArg(args, "username"))
	if username == "" {
		return nil, errors.New("username required")
	}
	if err := deleteUser(a.ctx.AppDB(), username); err != nil {
		return nil, err
	}
	if err := a.reloadUserCache(); err != nil {
		return nil, err
	}
	a.broker.DisconnectUser(username)
	return map[string]any{"ok": true}, nil
}

func (a *App) toolUsersSetEnabled(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	username := strings.TrimSpace(strArg(args, "username"))
	enabled := boolArg(args, "enabled", true)
	if err := setUserEnabled(a.ctx.AppDB(), username, enabled); err != nil {
		return nil, err
	}
	if err := a.reloadUserCache(); err != nil {
		return nil, err
	}
	if !enabled {
		a.broker.DisconnectUser(username)
	}
	return map[string]any{"ok": true, "username": username, "enabled": enabled}, nil
}

func (a *App) toolDevices(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	filter := strings.ToLower(strArg(args, "filter"))
	q := `SELECT id, project_id, slug, component, object_id, display_name,
	             manufacturer, model, state_topic, command_topic, ha_config_json, last_seen
	        FROM mqtt_devices WHERE project_id = ?`
	params := []any{projectScope(ctx)}
	if filter != "" {
		q += ` AND (LOWER(slug) LIKE ? OR LOWER(model) LIKE ? OR LOWER(manufacturer) LIKE ?)`
		like := "%" + filter + "%"
		params = append(params, like, like, like)
	}
	q += ` ORDER BY display_name, slug`
	rows, err := a.ctx.AppDB().Query(q, params...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var (
			id                                                                      int64
			pid, slug, component, objectID, name, manuf, model, st, cmd, configJSON string
			lastSeen                                                                *string
		)
		if err := rows.Scan(&id, &pid, &slug, &component, &objectID, &name, &manuf, &model, &st, &cmd, &configJSON, &lastSeen); err != nil {
			return nil, err
		}
		row := map[string]any{
			"id": id, "slug": slug, "component": component, "object_id": objectID,
			"display_name": name, "manufacturer": manuf, "model": model,
			"state_topic": st, "command_topic": cmd,
		}
		var config map[string]any
		if json.Unmarshal([]byte(configJSON), &config) == nil {
			row["config"] = config
			for _, key := range []string{"availability_topic", "json_attributes_topic", "unique_id"} {
				if value, ok := config[key]; ok {
					row[key] = value
				}
			}
		}
		if lastSeen != nil {
			row["last_seen"] = *lastSeen
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (a *App) toolRetainedList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return listRetainedMessages(a.ctx.AppDB(), strings.TrimSpace(strArg(args, "topic_pattern")), intArg(args, "limit", 100))
}

func (a *App) toolRetainedGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	topic := strings.TrimSpace(strArg(args, "topic"))
	if topic == "" {
		return nil, errors.New("topic required")
	}
	return getRetainedMessage(a.ctx.AppDB(), topic)
}

func (a *App) toolRetainedDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	topic := strings.TrimSpace(strArg(args, "topic"))
	if topic == "" {
		return nil, errors.New("topic required")
	}
	if _, err := getRetainedMessage(a.ctx.AppDB(), topic); err != nil {
		return nil, err
	}
	if a.broker != nil {
		if err := a.broker.DeleteRetained(topic); err != nil {
			return nil, err
		}
	}
	if _, err := a.ctx.AppDB().Exec(`DELETE FROM mqtt_retained WHERE topic = ?`, topic); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "topic": topic}, nil
}

func (a *App) toolStatus(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return a.snapshotStatus(ctx), nil
}

func (a *App) snapshotStatus(ctxs ...*sdk.AppCtx) map[string]any {
	port := 0
	if a.broker != nil {
		port = a.broker.Port()
	}
	var retained, msgs, users, devices int
	db := a.ctx.AppDB()
	_ = db.QueryRow(`SELECT COUNT(*) FROM mqtt_retained`).Scan(&retained)
	_ = db.QueryRow(`SELECT COUNT(*) FROM mqtt_message_log`).Scan(&msgs)
	_ = db.QueryRow(`SELECT COUNT(*) FROM mqtt_users WHERE enabled = 1`).Scan(&users)
	_ = db.QueryRow(`SELECT COUNT(*) FROM mqtt_devices WHERE project_id = ?`, projectScope(ctxs...)).Scan(&devices)
	clients := 0
	brokerMetrics := map[string]any{}
	if a.broker != nil {
		clients = len(a.broker.Clients())
		info := a.broker.server.Info.Clone()
		brokerMetrics = map[string]any{
			"bytes_received":    info.BytesReceived,
			"bytes_sent":        info.BytesSent,
			"messages_received": info.MessagesReceived,
			"messages_sent":     info.MessagesSent,
			"messages_dropped":  info.MessagesDropped,
			"packets_received":  info.PacketsReceived,
			"packets_sent":      info.PacketsSent,
			"subscriptions":     info.Subscriptions,
			"inflight":          info.Inflight,
		}
	}
	bind := bindAddress(a, port)
	if a.broker != nil && a.broker.Address() != "" {
		bind = a.broker.Address()
	}
	host := advertisedHost(a)
	endpoint := ""
	if port > 0 {
		endpoint = "mqtt://" + net.JoinHostPort(host, fmt.Sprintf("%d", port))
	}
	return map[string]any{
		"port":                  port,
		"listen_address":        bind,
		"bind_address":          bind,
		"advertised_host":       host,
		"advertised_port":       port,
		"endpoint":              endpoint,
		"tls":                   false,
		"clients":               clients,
		"retained_count":        retained,
		"message_count":         msgs,
		"users_enabled":         users,
		"devices":               devices,
		"audit_rows_dropped":    a.droppedLogs.Load(),
		"events_dropped":        a.droppedEvents.Load(),
		"events_emitted":        a.eventsEmitted.Load(),
		"rate_limited_messages": a.rateLimitedMessages.Load(),
		"auth_rejected":         a.authRejected.Load(),
		"acl_rejected":          a.aclRejected.Load(),
		"started_at":            a.startedAt.Format(time.RFC3339),
		"uptime_seconds":        int64(time.Since(a.startedAt).Seconds()),
		"broker_metrics":        brokerMetrics,
		"limits": map[string]any{
			"max_clients":                   clampConfigInt(a.ctx, "max_clients", 1000, 1, 1000000),
			"max_payload_bytes":             clampConfigInt(a.ctx, "max_payload_bytes", 1048576, 1024, 268435455),
			"max_inflight_per_client":       clampConfigInt(a.ctx, "max_inflight_per_client", 128, 1, 65535),
			"max_pending_writes_per_client": clampConfigInt(a.ctx, "max_pending_writes_per_client", 1024, 1, 1048576),
			"max_publish_per_second":        clampConfigInt(a.ctx, "max_publish_per_second", 100, 0, 1000000),
			"max_publish_burst":             clampConfigInt(a.ctx, "max_publish_burst", 200, 1, 10000000),
			"max_event_per_second":          clampConfigInt(a.ctx, "max_event_per_second", 1000, 0, 1000000),
			"max_event_burst":               clampConfigInt(a.ctx, "max_event_burst", 2000, 1, 10000000),
			"max_event_payload_bytes":       clampConfigInt(a.ctx, "max_event_payload_bytes", 131072, 0, 268435455),
			"max_log_payload_bytes":         clampConfigInt(a.ctx, "max_log_payload_bytes", 65536, 0, 268435455),
		},
	}
}

func advertisedHost(a *App) string {
	if host := strings.TrimSpace(configString(a.ctx, "advertised_host", "")); host != "" {
		return host
	}
	if bind := strings.TrimSpace(configString(a.ctx, "bind_interface", "")); net.ParseIP(bind) != nil {
		return bind
	}
	if host, err := os.Hostname(); err == nil && strings.TrimSpace(host) != "" {
		return strings.TrimSpace(host)
	}
	return "localhost"
}

func validatePublish(topic string, qos int) error {
	topic = strings.TrimSpace(topic)
	if topic == "" {
		return errors.New("topic required")
	}
	if !mqtt.IsValidFilter(topic, true) {
		return fmt.Errorf("topic %q is not a valid MQTT publish topic", topic)
	}
	if qos < 0 || qos > 2 {
		return errors.New("qos must be 0, 1, or 2")
	}
	return nil
}

func (a *App) validatePayloadSize(payload []byte) error {
	max := clampConfigInt(a.ctx, "max_payload_bytes", 1048576, 1024, 268435455)
	if len(payload) > max {
		return fmt.Errorf("payload is %d bytes; maximum is %d", len(payload), max)
	}
	return nil
}

// ─── arg helpers ────────────────────────────────────────────────────

func strArg(args map[string]any, key string) string {
	if args == nil {
		return ""
	}
	if v, ok := args[key].(string); ok {
		return v
	}
	return ""
}

func intArg(args map[string]any, key string, def int) int {
	if args == nil {
		return def
	}
	switch v := args[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case string:
		if v == "" {
			return def
		}
		n := 0
		fmt.Sscanf(v, "%d", &n)
		return n
	}
	return def
}

func boolArg(args map[string]any, key string, def bool) bool {
	if args == nil {
		return def
	}
	switch v := args[key].(type) {
	case bool:
		return v
	case string:
		switch strings.ToLower(v) {
		case "true", "1", "yes":
			return true
		case "false", "0", "no":
			return false
		}
	}
	return def
}

func strArrayArg(args map[string]any, key string) []string {
	if args == nil {
		return nil
	}
	switch v := args[key].(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, x := range v {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func callerLabel(ctx *sdk.AppCtx) string {
	if ctx == nil {
		return ""
	}
	// Best-effort: surface the install id if the SDK exposes it via
	// env. Avoids growing the SDK API for a label-only field.
	return strings.TrimSpace(strArg(map[string]any{"install_id": ctx.Manifest().Name}, "install_id"))
}
