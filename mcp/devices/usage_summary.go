package main

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

func (a *App) resourceSnapshot(db sqlStore) (map[string]int64, error) {
	out := map[string]int64{}
	type resourceQuery struct {
		names []string
		query string
	}
	specs := []resourceQuery{
		{[]string{"devices", "enabled_devices", "online_devices"}, `SELECT COUNT(*),COALESCE(SUM(enabled),0),COALESCE(SUM(status='online'),0) FROM devices WHERE project_id=?`},
		{[]string{"pending_provisions"}, `SELECT COUNT(*) FROM device_operations WHERE project_id=? AND kind='provision' AND status='pending'`},
		{[]string{"pending_commands", "inflight_commands"}, `SELECT COUNT(*),COALESCE(SUM(status='sent'),0) FROM device_commands WHERE device_id IN(SELECT id FROM devices WHERE project_id=?) AND status IN('queued','sent')`},
		{[]string{"state_keys", "state_bytes"}, `SELECT COUNT(*),COALESCE(SUM(length(CAST(value_json AS BLOB))),0) FROM device_state WHERE device_id IN(SELECT id FROM devices WHERE project_id=?)`},
		{[]string{"history_records", "history_bytes"}, `SELECT COUNT(*),COALESCE(SUM(length(CAST(data_json AS BLOB))+length(CAST(metadata_json AS BLOB))),0) FROM device_messages WHERE project_id=?`},
		{[]string{"pending_events"}, `SELECT COUNT(*) FROM device_outbox WHERE project_id=? AND delivered_at IS NULL`},
		{[]string{"active_http_credentials"}, `SELECT COUNT(*) FROM device_http_credentials WHERE project_id=? AND revoked_at IS NULL AND expires_at>strftime('%Y-%m-%dT%H:%M:%f000000Z','now')`},
	}
	specs = append(specs,
		resourceQuery{[]string{"max_device_state_keys", "max_device_state_bytes"}, `SELECT COALESCE(MAX(keys),0),COALESCE(MAX(bytes),0) FROM(SELECT COUNT(*) AS keys,SUM(length(CAST(value_json AS BLOB))) AS bytes FROM device_state WHERE device_id IN(SELECT id FROM devices WHERE project_id=?) GROUP BY device_id)`},
		resourceQuery{[]string{"max_device_history_records", "max_device_history_bytes"}, `SELECT COALESCE(MAX(records),0),COALESCE(MAX(bytes),0) FROM(SELECT COUNT(*) AS records,SUM(length(CAST(data_json AS BLOB))+length(CAST(metadata_json AS BLOB))) AS bytes FROM device_messages WHERE project_id=? GROUP BY device_id)`},
		resourceQuery{[]string{"max_device_pending_commands", "max_device_inflight_commands"}, `SELECT COALESCE(MAX(pending),0),COALESCE(MAX(inflight),0) FROM(SELECT COUNT(*) AS pending,SUM(status='sent') AS inflight FROM device_commands WHERE status IN('queued','sent') AND device_id IN(SELECT id FROM devices WHERE project_id=?) GROUP BY device_id)`},
		resourceQuery{[]string{"max_device_http_credentials"}, `SELECT COALESCE(MAX(n),0) FROM(SELECT COUNT(*) AS n FROM device_http_credentials WHERE project_id=? AND revoked_at IS NULL AND expires_at>strftime('%Y-%m-%dT%H:%M:%f000000Z','now') GROUP BY device_id)`},
	)
	specs = append(specs, resourceQuery{[]string{"transport_receipts"}, `SELECT COUNT(*) FROM device_transport_receipts WHERE project_id=?`}, resourceQuery{[]string{"quarantined_messages"}, `SELECT COUNT(*) FROM device_ingest_rejections WHERE project_id=?`})
	for _, spec := range specs {
		values := make([]int64, len(spec.names))
		dest := make([]any, len(values))
		for i := range values {
			dest[i] = &values[i]
		}
		if err := db.QueryRow(spec.query, a.projectID).Scan(dest...); err != nil {
			return nil, err
		}
		for i, name := range spec.names {
			out[name] = values[i]
		}
	}
	return out, nil
}
func (a *App) usageSummary(args map[string]any) (map[string]any, error) {
	now := time.Now().UTC()
	to := formatTime(now)
	from := formatTime(now.Add(-24 * time.Hour))
	var err error
	if value := str(args, "from"); value != "" {
		from, err = parseQueryTime(value)
		if err != nil {
			return nil, err
		}
	}
	if value := str(args, "to"); value != "" {
		to, err = parseQueryTime(value)
		if err != nil {
			return nil, err
		}
	}
	if from >= to {
		return nil, errors.New("from must precede to")
	}
	a.mutationMu.Lock()
	defer a.mutationMu.Unlock()
	tx, err := a.ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	query := `SELECT measurement,SUM(quantity) FROM device_usage WHERE project_id=? AND created_at>=? AND created_at<?`
	params := []any{a.projectID, from, to}
	if measurement := str(args, "measurement"); measurement != "" {
		query += ` AND measurement=?`
		params = append(params, measurement)
	}
	rows, err := tx.Query(query+` GROUP BY measurement ORDER BY measurement`, params...)
	if err != nil {
		return nil, err
	}
	totals := map[string]string{}
	for rows.Next() {
		var measurement string
		var value int64
		if err = rows.Scan(&measurement, &value); err != nil {
			rows.Close()
			return nil, err
		}
		totals[measurement] = strconv.FormatInt(value, 10)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	resources, err := a.resourceSnapshot(tx)
	if err != nil {
		return nil, err
	}
	var since, pruned string
	err = tx.QueryRow(`SELECT tracking_since,pruned_before FROM device_usage_coverage WHERE project_id=?`, a.projectID).Scan(&since, &pruned)
	if err != nil {
		return nil, err
	}
	var earliest sql.NullString
	if err = tx.QueryRow(`SELECT MIN(created_at) FROM device_usage WHERE project_id=?`, a.projectID).Scan(&earliest); err != nil {
		return nil, err
	}
	coverageFrom := since
	if pruned > coverageFrom {
		coverageFrom = pruned
	}
	limits := a.activeLimits()
	a.policyMu.RLock()
	revision := a.policyRevision
	a.policyMu.RUnlock()
	return map[string]any{"scope_id": a.projectID, "window": map[string]any{"from": from, "to": to, "end_exclusive": true}, "totals": totals, "quantity_encoding": "decimal-string", "resources": resources, "policy_revision": revision, "over_limit": overLimits(resources, limits), "coverage": map[string]any{"tracking_since": since, "pruned_before": pruned, "earliest_retained_record": earliest.String, "complete": from >= coverageFrom && to <= formatTime(now)}, "as_of": formatTime(now), "mqtt_ingestion": a.transportStatus()}, nil
}
func (a *App) handleUsageSummary(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	args := queryArgs(r)
	args["measurement"] = r.URL.Query().Get("measurement")
	result, err := a.usageSummary(args)
	respond(w, result, err)
}
func (a *App) initUsageCoverage() error {
	_, err := a.ctx.AppDB().Exec(`INSERT INTO device_usage_coverage(project_id,tracking_since)VALUES(?,?) ON CONFLICT DO NOTHING`, a.projectID, nowText())
	if err != nil {
		return fmt.Errorf("usage coverage: %w", err)
	}
	return nil
}
