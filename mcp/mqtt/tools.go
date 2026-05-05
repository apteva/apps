// tools.go — MCP tool handlers. Each tool is a thin wrapper around
// the underlying DB helpers + broker; argument parsing happens here
// so the helpers stay reusable from the HTTP routes too.

package main

import (
	"errors"
	"fmt"
	"strings"

	sdk "github.com/apteva/app-sdk"
	mqtt "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/packets"
)

// MCPTools registers the tool set. Schemas are minimal — descriptive
// `description` carries the operator-facing detail.
func (a *App) mcpTools() []sdk.Tool {
	return []sdk.Tool{
		{
			Name: "mqtt_publish",
			Description: "Publish one MQTT message. Args: topic (str, required), payload (str), retain (bool), qos (int 0|1|2).",
			InputSchema: schemaObj(map[string]any{
				"topic":   map[string]any{"type": "string"},
				"payload": map[string]any{"type": "string"},
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
			Name:        "mqtt_subscribe",
			Description: "Bridge an MQTT topic pattern to the platform event bus as `mqtt.<bus_topic>`. Args: topic_pattern (MQTT-style, e.g. motion/+/state), bus_topic (a-z0-9._-).",
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
			Description: "Create or update an MQTT user. Args: username, password, allow_publish (string array, default [\"#\"]), allow_subscribe (string array, default [\"#\"]).",
			InputSchema: schemaObj(map[string]any{
				"username":        map[string]any{"type": "string"},
				"password":        map[string]any{"type": "string"},
				"allow_publish":   map[string]any{"type": "array"},
				"allow_subscribe": map[string]any{"type": "array"},
			}, []string{"username", "password"}),
			Handler: a.toolUsersAdd,
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
			Name:        "mqtt_devices",
			Description: "List HA-discovered devices. Args: filter (optional substring on slug, model, manufacturer).",
			InputSchema: schemaObj(map[string]any{"filter": map[string]any{"type": "string"}}, nil),
			Handler:     a.toolDevices,
		},
		{
			Name:        "mqtt_status",
			Description: "Broker status: port, client count, retained count, recent message rate.",
			InputSchema: schemaObj(nil, nil),
			Handler:     a.toolStatus,
		},
	}
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
	payload := []byte(strArg(args, "payload"))
	retain := boolArg(args, "retain", false)
	qos := byte(intArg(args, "qos", 0))
	if err := a.broker.Publish(topic, payload, retain, qos); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "topic": topic, "bytes": len(payload)}, nil
}

func (a *App) toolTopicsRecent(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	limit := intArg(args, "limit", 50)
	if limit <= 0 {
		limit = 50
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
	return out, nil
}

func (a *App) toolSubscribe(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pat := strArg(args, "topic_pattern")
	bus := strArg(args, "bus_topic")
	sub, err := addBusSubscription(a.ctx.AppDB(), projectScope(), pat, bus, callerLabel(ctx))
	if err != nil {
		return nil, err
	}
	// Wire it into the broker right now so the operator doesn't
	// have to wait for a restart.
	_ = a.broker.Subscribe(sub.TopicPattern, int(100+sub.ID),
		func(_ *mqtt.Client, _ packets.Subscription, pk packets.Packet) {
			a.emitBusMessage(sub.BusTopic, pk)
		})
	return sub, nil
}

func (a *App) toolSubscribeList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	subs, err := listBusSubscriptions(a.ctx.AppDB())
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
	if err := deleteBusSubscription(a.ctx.AppDB(), id); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true}, nil
}

func (a *App) toolUsersAdd(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	username := strArg(args, "username")
	password := strArg(args, "password")
	pub := strArrayArg(args, "allow_publish")
	sub := strArrayArg(args, "allow_subscribe")
	if err := addUser(a.ctx.AppDB(), username, password, pub, sub); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "username": username}, nil
}

func (a *App) toolUsersList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return listUsers(a.ctx.AppDB())
}

func (a *App) toolUsersDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	username := strArg(args, "username")
	if username == "" {
		return nil, errors.New("username required")
	}
	if err := deleteUser(a.ctx.AppDB(), username); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true}, nil
}

func (a *App) toolUsersSetEnabled(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	username := strArg(args, "username")
	enabled := boolArg(args, "enabled", true)
	if err := setUserEnabled(a.ctx.AppDB(), username, enabled); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "username": username, "enabled": enabled}, nil
}

func (a *App) toolDevices(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	filter := strings.ToLower(strArg(args, "filter"))
	q := `SELECT id, project_id, slug, component, object_id, display_name,
	             manufacturer, model, state_topic, command_topic, last_seen
	        FROM mqtt_devices WHERE project_id = ?`
	params := []any{projectScope()}
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
			id                                                                int64
			pid, slug, component, objectID, name, manuf, model, st, cmd       string
			lastSeen                                                          *string
		)
		if err := rows.Scan(&id, &pid, &slug, &component, &objectID, &name, &manuf, &model, &st, &cmd, &lastSeen); err != nil {
			return nil, err
		}
		row := map[string]any{
			"id": id, "slug": slug, "component": component, "object_id": objectID,
			"display_name": name, "manufacturer": manuf, "model": model,
			"state_topic": st, "command_topic": cmd,
		}
		if lastSeen != nil {
			row["last_seen"] = *lastSeen
		}
		out = append(out, row)
	}
	return out, nil
}

func (a *App) toolStatus(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return a.snapshotStatus(), nil
}

func (a *App) snapshotStatus() map[string]any {
	port := 0
	if a.broker != nil {
		port = a.broker.Port()
	}
	var retained, msgs, users, devices int
	db := a.ctx.AppDB()
	_ = db.QueryRow(`SELECT COUNT(*) FROM mqtt_retained`).Scan(&retained)
	_ = db.QueryRow(`SELECT COUNT(*) FROM mqtt_message_log`).Scan(&msgs)
	_ = db.QueryRow(`SELECT COUNT(*) FROM mqtt_users WHERE enabled = 1`).Scan(&users)
	_ = db.QueryRow(`SELECT COUNT(*) FROM mqtt_devices WHERE project_id = ?`, projectScope()).Scan(&devices)
	return map[string]any{
		"port":           port,
		"retained_count": retained,
		"message_count":  msgs,
		"users_enabled":  users,
		"devices":        devices,
	}
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
