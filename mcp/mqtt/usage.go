package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
	mqtt "github.com/apteva/apps/mcp/mqtt/internal/broker"
	"github.com/mochi-mqtt/server/v2/packets"
)

// Policies are supplied by trusted install configuration, never tool arguments.
// Zero disables a quota; the broker's global safety limits still apply.
type quotaPolicy map[string]int64

var usageMetrics = []string{"connection_admissions", "accepted_messages", "accepted_payload_bytes", "mqtt_ingress_messages", "mqtt_ingress_bytes", "mqtt_egress_messages", "mqtt_egress_bytes", "ingress_packets", "ingress_packet_bytes", "egress_packets", "egress_packet_bytes", "quota_rejections"}

var quotaKeys = map[string]bool{
	"connections": true, "connection_admissions": true, "accepted_messages": true, "accepted_payload_bytes": true,
	"mqtt_ingress_messages": true, "mqtt_ingress_bytes": true, "mqtt_egress_messages": true, "mqtt_egress_bytes": true,
	"payload_bytes": true, "retained_messages": true, "retained_bytes": true,
}

type quotaError struct {
	metric  string
	payload bool
}

func (e *quotaError) Error() string { return "MQTT ownership scope quota exceeded: " + e.metric }
func (e *quotaError) Unwrap() error { return mqtt.ErrDurableCapacity }
func (e *quotaError) Is(target error) bool {
	return (target == errPayloadTooLarge || target == mqtt.ErrPayloadLimit) && e.payload
}

type connectionUsage struct{ scope, token string }
type usageMeter struct {
	app         *App
	defaults    quotaPolicy
	overrides   map[string]quotaPolicy
	period      int64
	connections sync.Map // *mqtt.Client -> connectionUsage
	now         func() time.Time
}

func decodePolicy(raw string) (quotaPolicy, error) {
	if strings.TrimSpace(raw) == "" {
		raw = "{}"
	}
	var values map[string]json.RawMessage
	dec := json.NewDecoder(strings.NewReader(raw))
	if err := dec.Decode(&values); err != nil {
		return nil, err
	}
	if values == nil {
		return nil, errors.New("quota policy must be an object")
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return nil, errors.New("trailing quota JSON")
	}
	out := quotaPolicy{}
	for key, value := range values {
		if !quotaKeys[key] {
			return nil, fmt.Errorf("unknown quota %q", key)
		}
		n, err := strconv.ParseInt(string(value), 10, 64)
		if err != nil || n < 0 || n > 1<<53-1 {
			return nil, fmt.Errorf("quota %s must be an integer from 0 to 9007199254740991", key)
		}
		out[key] = n
	}
	return out, nil
}
func (m *usageMeter) policy(scope string) quotaPolicy {
	out := quotaPolicy{}
	for k, v := range m.defaults {
		out[k] = v
	}
	for k, v := range m.overrides[scope] {
		out[k] = v
	}
	return out
}
func newUsageMeter(a *App) (*usageMeter, error) {
	period, err := strictInt(a.ctx, "quota_period_seconds", 86400, 60, 2678400)
	if err != nil {
		return nil, err
	}
	retained, err := strictInt(a.ctx, "retain_max_count", 10000, 0, 10000000)
	if err != nil {
		return nil, err
	}
	m := &usageMeter{app: a, period: int64(period), overrides: map[string]quotaPolicy{}, now: time.Now, defaults: quotaPolicy{}}
	for key := range quotaKeys {
		m.defaults[key] = 0
	}
	m.defaults["connections"] = int64(clampConfigInt(a.ctx, "max_clients", 1000, 1, 1000000))
	m.defaults["payload_bytes"] = int64(clampConfigInt(a.ctx, "max_payload_bytes", 1048576, 1024, 268435455))
	m.defaults["retained_messages"] = int64(retained)
	m.defaults["retained_bytes"] = 67108864
	defaults, err := decodePolicy(configString(a.ctx, "quota_defaults_json", "{}"))
	if err != nil {
		return nil, fmt.Errorf("quota_defaults_json: %w", err)
	}
	for k, v := range defaults {
		m.defaults[k] = v
	}
	raw := configString(a.ctx, "scope_quotas_json", "{}")
	var scopes map[string]json.RawMessage
	dec := json.NewDecoder(strings.NewReader(raw))
	if err = dec.Decode(&scopes); err != nil || scopes == nil {
		return nil, errors.New("scope_quotas_json must be an object")
	}
	if err = dec.Decode(new(any)); err != io.EOF {
		return nil, errors.New("trailing scope quota JSON")
	}
	if len(scopes) > 1000 {
		return nil, errors.New("at most 1000 scope quota overrides are supported")
	}
	for scope, value := range scopes {
		if !validScope(scope) {
			return nil, errors.New("invalid quota ownership scope")
		}
		p, err := decodePolicy(string(value))
		if err != nil {
			return nil, fmt.Errorf("scope quota %s: %w", scope, err)
		}
		m.overrides[scope] = p
	}
	tx, err := a.ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// The MQTT app is a single broker per database. Surviving sessions are offline
	// after startup; old transport reservations are not active sockets.
	if _, err = tx.Exec(`DELETE FROM mqtt_usage_connections`); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(`DELETE FROM mqtt_quota_policies`); err != nil {
		return nil, err
	}
	for scope := range scopes {
		p, _ := json.Marshal(m.policy(scope))
		if _, err = tx.Exec(`INSERT INTO mqtt_quota_policies VALUES(?,?)`, scope, string(p)); err != nil {
			return nil, err
		}
	}
	p, _ := json.Marshal(m.defaults)
	if _, err = tx.Exec(`INSERT INTO mqtt_quota_policies VALUES('',?)`, string(p)); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return m, nil
}

// A persisted window cannot be shortened/reset by restart or config changes.
// A backwards clock does not refill it. Only the next natural expiry rolls it.
func (m *usageMeter) window(tx *sql.Tx, scope string) (int64, int64, error) {
	now := m.now().Unix()
	if now < m.period {
		return 0, 0, errors.New("MQTT quota clock is outside the supported epoch")
	}
	var start, end int64
	err := tx.QueryRow(`SELECT starts_at,ends_at FROM mqtt_usage_windows WHERE scope_id=?`, scope).Scan(&start, &end)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, 0, err
	}
	if errors.Is(err, sql.ErrNoRows) || now >= end {
		start = now / m.period * m.period
		end = start + m.period
		if _, err = tx.Exec(`INSERT INTO mqtt_usage_windows VALUES(?,?,?) ON CONFLICT(scope_id) DO UPDATE SET starts_at=excluded.starts_at,ends_at=excluded.ends_at`, scope, start, end); err != nil {
			return 0, 0, err
		}
		if _, err = tx.Exec(`DELETE FROM mqtt_usage_counters WHERE scope_id=? AND period_start!=0`, scope); err != nil {
			return 0, 0, err
		}
	}
	return start, end, nil
}
func counter(tx *sql.Tx, scope string, period int64, key string) (int64, error) {
	var value int64
	err := tx.QueryRow(`SELECT value FROM mqtt_usage_counters WHERE scope_id=? AND period_start=? AND metric=?`, scope, period, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return value, err
}
func addCounter(tx *sql.Tx, scope string, period int64, key string, delta int64) error {
	old, err := counter(tx, scope, period, key)
	if err != nil {
		return err
	}
	if delta < 0 || old > math.MaxInt64-delta {
		return errors.New("MQTT usage counter overflow")
	}
	_, err = tx.Exec(`INSERT INTO mqtt_usage_counters VALUES(?,?,?,?) ON CONFLICT(scope_id,period_start,metric) DO UPDATE SET value=excluded.value`, scope, period, key, old+delta)
	return err
}
func (m *usageMeter) charge(tx *sql.Tx, scope string, deltas map[string]int64) error {
	start, _, err := m.window(tx, scope)
	if err != nil {
		return err
	}
	policy := m.policy(scope)
	keys := make([]string, 0, len(deltas))
	for key := range deltas {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		delta := deltas[key]
		current, err := counter(tx, scope, start, key)
		if err != nil {
			return err
		}
		if limit := policy[key]; limit > 0 && (delta > limit || current > limit-delta) {
			return &quotaError{metric: key}
		}
		if err = addCounter(tx, scope, 0, key, delta); err != nil {
			return err
		}
		if err = addCounter(tx, scope, start, key, delta); err != nil {
			return err
		}
	}
	return nil
}
func (m *usageMeter) rejected(scope string) error {
	tx, err := m.app.ctx.AppDB().Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	start, _, err := m.window(tx, scope)
	if err != nil {
		return err
	}
	if err = addCounter(tx, scope, 0, "quota_rejections", 1); err != nil {
		return err
	}
	if err = addCounter(tx, scope, start, "quota_rejections", 1); err != nil {
		return err
	}
	return tx.Commit()
}
func (m *usageMeter) finish(scope string, err error) error {
	if err == nil {
		return nil
	}
	var exceeded *quotaError
	if errors.As(err, &exceeded) {
		if e := m.rejected(scope); e != nil {
			return m.fault(e)
		}
		return err
	}
	return m.fault(err)
}
func (m *usageMeter) fault(err error) error {
	var s *mqtt.Server
	if m.app.broker != nil {
		s = m.app.broker.server
	}
	return m.app.durable.fault(err, s)
}
func (m *usageMeter) transaction(scope string, fn func(*sql.Tx) error) (err error) {
	if m.app.durable.failed.Load() {
		return errors.New("MQTT durable storage unavailable")
	}
	tx, err := m.app.ctx.AppDB().Begin()
	if err != nil {
		return m.fault(err)
	}
	defer func() { _ = tx.Rollback(); err = m.finish(scope, err) }()
	if err = fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}
func transportQuotaError(err error) error {
	var q *quotaError
	if errors.As(err, &q) {
		if q.payload {
			return packets.ErrPacketTooLarge
		}
		return packets.ErrQuotaExceeded
	}
	return err
}
func (m *usageMeter) Open(cl *mqtt.Client, pk packets.Packet) error {
	identity := m.app.clientIdentity(cl)
	if identity == nil {
		return packets.ErrNotAuthorized
	}
	entry := connectionUsage{identity.scope, randomPassword(40)}
	err := m.transaction(entry.scope, func(tx *sql.Tx) error {
		var valid int
		if err := tx.QueryRow(`SELECT count(*) FROM mqtt_users WHERE id=? AND revision=? AND scope_id=? AND enabled=1`, identity.credentialID, identity.revision, entry.scope).Scan(&valid); err != nil {
			return err
		}
		if valid != 1 {
			return packets.ErrNotAuthorized
		}
		policy := m.policy(entry.scope)
		if limit := policy["payload_bytes"]; limit > 0 && int64(len(pk.Connect.WillPayload)) > limit {
			return &quotaError{"payload_bytes", true}
		}
		var active int64
		if err := tx.QueryRow(`SELECT count(*) FROM mqtt_usage_connections WHERE scope_id=? AND client_id!=?`, entry.scope, cl.ID).Scan(&active); err != nil {
			return err
		}
		if limit := policy["connections"]; limit > 0 && active >= limit {
			return &quotaError{metric: "connections"}
		}
		if err := m.charge(tx, entry.scope, map[string]int64{"connection_admissions": 1, "ingress_packets": 1, "ingress_packet_bytes": int64(cl.InboundPacketBytes(pk))}); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO mqtt_usage_connections VALUES(?,?,?) ON CONFLICT(client_id) DO UPDATE SET scope_id=excluded.scope_id,token=excluded.token`, cl.ID, entry.scope, entry.token); err != nil {
			return err
		}
		peak, err := counter(tx, entry.scope, 0, "peak_connections")
		if err != nil {
			return err
		}
		if active+1 > peak {
			return addCounter(tx, entry.scope, 0, "peak_connections", active+1-peak)
		}
		return nil
	})
	if err == nil {
		m.connections.Store(cl, entry)
	}
	return transportQuotaError(err)
}
func (m *usageMeter) Close(cl *mqtt.Client) {
	value, ok := m.connections.LoadAndDelete(cl)
	if !ok {
		return
	}
	entry := value.(connectionUsage)
	_ = m.transaction(entry.scope, func(tx *sql.Tx) error {
		_, err := tx.Exec(`DELETE FROM mqtt_usage_connections WHERE client_id=? AND token=?`, cl.ID, entry.token)
		return err
	})
}
func (m *usageMeter) Read(cl *mqtt.Client, pk packets.Packet, size int) error {
	value, ok := m.connections.Load(cl)
	if !ok {
		return nil
	}
	entry := value.(connectionUsage)
	return transportQuotaError(m.transaction(entry.scope, func(tx *sql.Tx) error {
		deltas := map[string]int64{"ingress_packets": 1, "ingress_packet_bytes": int64(size)}
		if pk.FixedHeader.Type == packets.Publish {
			if limit := m.policy(entry.scope)["payload_bytes"]; limit > 0 && int64(len(pk.Payload)) > limit {
				return &quotaError{"payload_bytes", true}
			}
			deltas["mqtt_ingress_messages"] = 1
			deltas["mqtt_ingress_bytes"] = int64(size)
		}
		return m.charge(tx, entry.scope, deltas)
	}))
}
func (m *usageMeter) Write(cl *mqtt.Client, pk packets.Packet, size int) error {
	value, ok := m.connections.Load(cl)
	if !ok {
		return nil
	}
	entry := value.(connectionUsage)
	return transportQuotaError(m.transaction(entry.scope, func(tx *sql.Tx) error {
		deltas := map[string]int64{"egress_packets": 1, "egress_packet_bytes": int64(size)}
		if pk.FixedHeader.Type == packets.Publish {
			deltas["mqtt_egress_messages"] = 1
			deltas["mqtt_egress_bytes"] = int64(size)
		}
		return m.charge(tx, entry.scope, deltas)
	}))
}
func retainedUsage(tx *sql.Tx, scope string) (int64, int64, error) {
	var count, bytes int64
	err := tx.QueryRow(`SELECT messages,bytes FROM mqtt_retained_usage WHERE scope_id=?`, scope).Scan(&count, &bytes)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, nil
	}
	return count, bytes, err
}
func (m *usageMeter) accepted(tx *sql.Tx, scope string, pk packets.Packet) error {
	if limit := m.policy(scope)["payload_bytes"]; limit > 0 && int64(len(pk.Payload)) > limit {
		return &quotaError{"payload_bytes", true}
	}
	return m.charge(tx, scope, map[string]int64{"accepted_messages": 1, "accepted_payload_bytes": int64(len(pk.Payload))})
}
func (m *usageMeter) retained(tx *sql.Tx, scope string, oldCount, oldBytes int64) error {
	count, bytes, err := retainedUsage(tx, scope)
	if err != nil {
		return err
	}
	policy := m.policy(scope)
	if limit := policy["retained_messages"]; limit > 0 && count > limit && count > oldCount {
		return &quotaError{metric: "retained_messages"}
	}
	if limit := policy["retained_bytes"]; limit > 0 && bytes > limit && bytes > oldBytes {
		return &quotaError{metric: "retained_bytes"}
	}
	return nil
}
func (a *App) usageSnapshot(scope string) (any, error) {
	if a.usage == nil {
		return nil, errors.New("usage metering unavailable")
	}
	tx, err := a.ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	start, end, err := a.usage.window(tx, scope)
	if err != nil {
		return nil, err
	}
	lifetime, window := map[string]string{"peak_connections": "0"}, map[string]string{}
	for _, key := range usageMetrics {
		lifetime[key] = "0"
		window[key] = "0"
	}
	rows, err := tx.Query(`SELECT period_start,metric,value FROM mqtt_usage_counters WHERE scope_id=? ORDER BY metric`, scope)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var period, value int64
		var metric string
		if err = rows.Scan(&period, &metric, &value); err != nil {
			rows.Close()
			return nil, err
		}
		if period == 0 {
			lifetime[metric] = strconv.FormatInt(value, 10)
		} else if period == start {
			window[metric] = strconv.FormatInt(value, 10)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	var active, started int64
	if err = tx.QueryRow(`SELECT count(*) FROM mqtt_usage_connections WHERE scope_id=?`, scope).Scan(&active); err != nil {
		return nil, err
	}
	if err = tx.QueryRow(`SELECT value FROM mqtt_usage_state WHERE name='started_at'`).Scan(&started); err != nil {
		return nil, err
	}
	retained, bytes, err := retainedUsage(tx, scope)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return map[string]any{"scope_id": scope, "storage_fault": a.durable != nil && a.durable.failed.Load(), "started_at": started, "window_starts_at": start, "window_ends_at": end, "lifetime": lifetime, "window": window, "connections": active, "retained_messages": retained, "retained_bytes": bytes, "quotas": a.usage.policy(scope), "global_safety_limits": map[string]int{"connections": clampConfigInt(a.ctx, "max_clients", 1000, 1, 1000000), "mqtt_packet_bytes": clampConfigInt(a.ctx, "max_payload_bytes", 1048576, 1024, 268435455), "payload_bytes": clampConfigInt(a.ctx, "max_payload_bytes", 1048576, 1024, 268435455)}, "accounting": "admitted reservations; MQTT packet bytes exclude TLS/TCP framing; PUBLISH retries and fan-out count separately; counters are decimal strings"}, nil
}
func (a *App) handleUsage(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		httpErr(w, 405, "GET only; quotas are set by trusted install configuration")
		return
	}
	result, err := a.usageSnapshot(projectScope(a.requestContext(r)))
	if err != nil {
		httpErr(w, 503, err.Error())
		return
	}
	writeJSON(w, result)
}
func (a *App) usageTool() sdk.Tool {
	return sdk.Tool{Name: "mqtt_usage", Description: "Persistent usage, retained storage, active connections, quota window and effective limits for the authenticated ownership scope. Counter values are decimal strings. Limits are administered through trusted install configuration.", InputSchema: schemaObj(nil, nil), Handler: func(ctx *sdk.AppCtx, _ map[string]any) (any, error) { return a.usageSnapshot(projectScope(ctx)) }}
}

var _ mqtt.TransportUsage = (*usageMeter)(nil)
