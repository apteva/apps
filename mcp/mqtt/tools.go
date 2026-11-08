// tools.go — MCP tool handlers. Each tool is a thin wrapper around
// the underlying DB helpers + broker; argument parsing happens here
// so the helpers stay reusable from the HTTP routes too.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	mqtt "github.com/apteva/apps/mcp/mqtt/internal/broker"
	"github.com/mochi-mqtt/server/v2/packets"
)

// MCPTools registers the tool set. Schemas are minimal — descriptive
// `description` carries the operator-facing detail.
func (a *App) mcpTools() []sdk.Tool {
	tools := []sdk.Tool{
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
			Handler: func(ctx *sdk.AppCtx, _ map[string]any) (any, error) {
				return a.snapshotClients(ctx), nil
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
			Description: "Legacy create-only alias; existing credentials are never replaced. Explicit scoped allow_publish and allow_subscribe arrays required. Prefer mqtt_users_create.",
			InputSchema: schemaObj(map[string]any{
				"username":        map[string]any{"type": "string"},
				"password":        map[string]any{"type": "string"},
				"allow_publish":   map[string]any{"type": "array"},
				"allow_subscribe": map[string]any{"type": "array"},
			}, []string{"username", "password", "allow_publish", "allow_subscribe"}),
			Handler: a.toolUsersAdd,
		},
		{
			Name:        "mqtt_users_create",
			Description: "Create a scope-owned MQTT credential. Explicit allow_publish and allow_subscribe arrays are required; [] denies access. Omit password to generate one returned once. Use the returned scope-qualified username when connecting.",
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
	tools = append(tools, a.consumerTool(), a.usageTool())
	for i := range tools {
		handler := tools[i].Handler
		contextHandler := tools[i].HandlerCtx
		tools[i].Handler = nil
		tools[i].HandlerCtx = func(call context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
			scope, err := a.toolScope(call)
			if err != nil {
				return nil, err
			}
			if contextHandler != nil {
				return contextHandler(call, ctx.WithProject(scope), args)
			}
			return handler(ctx.WithProject(scope), args)
		}
	}
	return tools
}

func userWriteSchema() map[string]any {
	return schemaObj(map[string]any{
		"username":        map[string]any{"type": "string"},
		"password":        map[string]any{"type": "string", "description": "Omit to generate a password returned once."},
		"allow_publish":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		"allow_subscribe": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
	}, []string{"username", "allow_publish", "allow_subscribe"})
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
	if err := requireTopicScope(ctx, topic); err != nil {
		return nil, err
	}
	if err := validatePublish(topic, qos); err != nil {
		return nil, err
	}
	if err := a.validatePayloadSize(payload); err != nil {
		return nil, err
	}
	id, err := a.broker.PublishMessage(topic, payload, retain, byte(qos))
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "accepted": true, "durable": true, "message_id": id, "topic": topic, "bytes": len(payload)}, nil
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
		   FROM mqtt_message_log WHERE scope_id = ?
		  GROUP BY topic
		  ORDER BY last_ts DESC
		  LIMIT ?`, projectScope(ctx), limit)
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

func (a *App) snapshotClients(ctx *sdk.AppCtx) []map[string]any {
	out := []map[string]any{}
	if a.broker != nil {
		for _, client := range a.broker.Clients() {
			identity := a.clientIdentity(client)
			if identity == nil || identity.scope != projectScope(ctx) {
				continue
			}
			client.RLock()
			row := map[string]any{
				"scope_id":         identity.scope,
				"credential_id":    identity.credentialID,
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
	scope, err := managementScope(ctx)
	if err != nil {
		return nil, err
	}
	if !scopedFilter(scope, pat) {
		return nil, errors.New("topic_pattern must be inside the ownership topic root")
	}
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
	return a.toolUsersCreate(ctx, args)
}

func (a *App) toolUsersCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	scope, err := managementScope(ctx)
	if err != nil {
		return nil, err
	}
	pub, err := aclArrayArg(args, "allow_publish", true)
	if err != nil {
		return nil, err
	}
	sub, err := aclArrayArg(args, "allow_subscribe", true)
	if err != nil {
		return nil, err
	}
	password := strArg(args, "password")
	generated := false
	if _, exists := args["password"]; !exists {
		password = randomPassword(32)
		generated = true
	}
	a.credentialsMu.Lock()
	defer a.credentialsMu.Unlock()
	username, nameErr := credentialName(scope, strings.TrimSpace(strArg(args, "username")))
	if nameErr != nil {
		return nil, nameErr
	}
	if err := createUser(a.ctx.AppDB(), scope, username, password, pub, sub); err != nil {
		return nil, err
	}
	if err := a.reloadUserCache(); err != nil {
		return nil, err
	}
	result := map[string]any{"ok": true, "username": username, "scope_id": scope, "topic_root": scopeTopicRoot(scope)}
	if generated {
		result["password"] = password
	}
	return result, nil
}

func aclArrayArg(args map[string]any, name string, required bool) ([]string, error) {
	value, present := args[name]
	if !present && !required {
		return nil, nil
	}
	out := []string{}
	switch v := value.(type) {
	case []string:
		out = append(out, v...)
	case []any:
		for _, item := range v {
			str, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("%s must contain only strings", name)
			}
			out = append(out, str)
		}
	default:
		return nil, fmt.Errorf("%s must be an explicit array; [] denies access", name)
	}
	return out, nil
}

func (a *App) toolUsersUpdateACL(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pub, err := aclArrayArg(args, "allow_publish", false)
	if err != nil {
		return nil, err
	}
	sub, err := aclArrayArg(args, "allow_subscribe", false)
	if err != nil {
		return nil, err
	}
	return a.mutateCredential(ctx, args, func(scope, username string) error {
		return updateUserACL(a.ctx.AppDB(), scope, username, pub, sub)
	})
}

func (a *App) toolUsersRotatePassword(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return a.mutateCredential(ctx, args, func(scope, username string) error {
		return rotateUserPassword(a.ctx.AppDB(), scope, username, strArg(args, "password"))
	})
}

func (a *App) toolUsersList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	scope, err := managementScope(ctx)
	if err != nil {
		return nil, err
	}
	return listUsers(a.ctx.AppDB(), scope)
}

func (a *App) toolUsersDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return a.mutateCredential(ctx, args, func(scope, username string) error { return deleteUser(a.ctx.AppDB(), scope, username) })
}

func (a *App) toolUsersSetEnabled(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	enabled, ok := args["enabled"].(bool)
	if !ok {
		return nil, errors.New("enabled must be a boolean")
	}
	return a.mutateCredential(ctx, args, func(scope, username string) error { return setUserEnabled(a.ctx.AppDB(), scope, username, enabled) })
}

func (a *App) mutateCredential(ctx *sdk.AppCtx, args map[string]any, change func(string, string) error) (any, error) {
	scope, err := managementScope(ctx)
	if err != nil {
		return nil, err
	}
	revoked := ""
	a.credentialsMu.Lock()
	defer func() {
		a.credentialsMu.Unlock()
		if revoked != "" {
			a.revokeUser(revoked)
		}
	}()
	username, nameErr := credentialName(scope, strings.TrimSpace(strArg(args, "username")))
	if nameErr != nil {
		return nil, nameErr
	}
	if err := change(scope, username); err != nil {
		return nil, err
	}
	// Revoke before returning even if cache reload fails. Existing auth snapshots
	// cannot inherit the permissions of a deleted/recreated username.
	revoked = username
	if err := a.reloadUserCache(); err != nil {
		a.usersMu.Lock()
		a.users = nil
		a.usersMu.Unlock()
		return nil, err
	}
	return map[string]any{"ok": true, "username": username}, nil
}

func (a *App) toolDevices(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	filter := strings.ToLower(strArg(args, "filter"))
	q := `SELECT id, project_id, slug, component, object_id, display_name,
	             manufacturer, model, state_topic, command_topic, ha_config_json, last_seen
	        FROM mqtt_devices WHERE scope_id = ?`
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
	return listRetainedMessages(a.ctx.AppDB(), projectScope(ctx), strings.TrimSpace(strArg(args, "topic_pattern")), intArg(args, "limit", 100))
}

func (a *App) toolRetainedGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	topic := strings.TrimSpace(strArg(args, "topic"))
	if topic == "" {
		return nil, errors.New("topic required")
	}
	return getRetainedMessage(a.ctx.AppDB(), projectScope(ctx), topic)
}

func (a *App) toolRetainedDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	topic := strings.TrimSpace(strArg(args, "topic"))
	if topic == "" {
		return nil, errors.New("topic required")
	}
	if _, err := getRetainedMessage(a.ctx.AppDB(), projectScope(ctx), topic); err != nil {
		return nil, err
	}
	if a.broker != nil {
		if err := a.broker.DeleteRetained(topic); err != nil {
			return nil, err
		}
	}
	if _, err := a.ctx.AppDB().Exec(`DELETE FROM mqtt_retained WHERE scope_id = ? AND topic = ?`, projectScope(ctx), topic); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "topic": topic}, nil
}

func (a *App) toolStatus(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	if _, err := managementScope(ctx); err != nil {
		return nil, err
	}
	return a.snapshotStatus(ctx), nil
}

func (a *App) snapshotStatus(ctx *sdk.AppCtx) map[string]any {
	port := 0
	if a.broker != nil {
		port = a.broker.Port()
	}
	var retained, msgs, users, devices int
	db := a.ctx.AppDB()
	_ = db.QueryRow(`SELECT COUNT(*) FROM mqtt_retained WHERE scope_id = ?`, projectScope(ctx)).Scan(&retained)
	_ = db.QueryRow(`SELECT COUNT(*) FROM mqtt_message_log WHERE scope_id = ?`, projectScope(ctx)).Scan(&msgs)
	_ = db.QueryRow(`SELECT COUNT(*) FROM mqtt_users WHERE enabled = 1 AND scope_id = ?`, projectScope(ctx)).Scan(&users)
	_ = db.QueryRow(`SELECT COUNT(*) FROM mqtt_devices WHERE scope_id = ?`, projectScope(ctx)).Scan(&devices)
	clients := 0
	brokerMetrics := map[string]any{} // broker-wide counters are never exposed to a scope.
	clients = len(a.snapshotClients(ctx))
	counters := a.counters(projectScope(ctx))
	bind := bindAddress(a, port)
	if a.broker != nil && a.broker.Address() != "" {
		bind = a.broker.Address()
	}
	endpoint := map[string]any{"endpoint": "", "advertised_host": "", "advertised_port": 0, "tls": true, "ready": false}
	if a.broker != nil {
		endpoint = a.broker.endpointStatus()
	}

	durability := map[string]any{"ready": false}
	if a.durable != nil {
		var journalCount, journalBytes, sessionCount, sessionBytes, consumers int64
		scope := projectScope(ctx)
		_ = db.QueryRow(`SELECT count(*),coalesce(sum(length(packet)),0) FROM mqtt_journal WHERE scope_id=?`, scope).Scan(&journalCount, &journalBytes)
		_ = db.QueryRow(`SELECT count(*),coalesce(sum(length(snapshot)),0) FROM mqtt_sessions WHERE scope_id=?`, scope).Scan(&sessionCount, &sessionBytes)
		_ = db.QueryRow(`SELECT count(*) FROM mqtt_consumers WHERE scope_id=?`, scope).Scan(&consumers)
		durability = map[string]any{"ready": a.durable.ready.Load(), "storage_fault": a.durable.failed.Load(), "journal_messages": journalCount, "journal_bytes": journalBytes, "sessions": sessionCount, "session_bytes": sessionBytes, "consumers": consumers, "max_journal_messages": a.durable.maxMessages, "max_journal_bytes": a.durable.maxBytes, "max_session_bytes": a.durable.maxSessionBytes, "application_delivery": "at-least-once via mqtt_consumer or POST /consumers; deduplicate message_id", "publisher_ack": "SQLite FULL commit of journal, retained mutation and QoS subscriber obligations"}
	}
	var usage any
	if a.usage != nil {
		var err error
		usage, err = a.usageSnapshot(projectScope(ctx))
		if err != nil {
			usage = map[string]any{"error": "persistent usage unavailable"}
		}
	}
	return map[string]any{
		"usage":                 usage,
		"durability":            durability,
		"scope_id":              projectScope(ctx),
		"topic_root":            scopeTopicRoot(projectScope(ctx)),
		"port":                  port,
		"listen_address":        bind,
		"bind_address":          bind,
		"advertised_host":       endpoint["advertised_host"],
		"advertised_port":       endpoint["advertised_port"],
		"endpoint":              endpoint["endpoint"],
		"ready":                 endpoint["ready"],
		"transport":             endpoint,
		"overload_policy":       map[string]any{"mqtt5": "disconnect with quota-exceeded reason (0x97), no publish acknowledgment", "mqtt311": "close before publish acknowledgment", "http": 429, "retry": "reconnect with exponential backoff and jitter; retry unacknowledged messages using stable application identifiers"},
		"tls":                   endpoint["tls"],
		"clients":               clients,
		"retained_count":        retained,
		"message_count":         msgs,
		"users_enabled":         users,
		"devices":               devices,
		"audit_rows_dropped":    counters.droppedLogs.Load(),
		"events_dropped":        counters.droppedEvents.Load(),
		"events_emitted":        counters.eventsEmitted.Load(),
		"rate_limited_messages": counters.rateLimited.Load(),
		"auth_rejected":         counters.authRejected.Load(),
		"acl_rejected":          counters.aclRejected.Load(),
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
		return fmt.Errorf("%w: payload is %d bytes; maximum is %d", errPayloadTooLarge, len(payload), max)
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

func callerLabel(ctx *sdk.AppCtx) string {
	if ctx == nil {
		return ""
	}
	// Best-effort: surface the install id if the SDK exposes it via
	// env. Avoids growing the SDK API for a label-only field.
	return strings.TrimSpace(strArg(map[string]any{"install_id": ctx.Manifest().Name}, "install_id"))
}
