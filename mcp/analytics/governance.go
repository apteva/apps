package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"net/http"
	"time"
)

type retentionPolicy struct {
	EventDays      int `json:"event_days"`
	DiagnosticDays int `json:"diagnostic_days"`
	ArchiveDays    int `json:"archive_days"`
}

func getRetention(db sqlRunner, project string) (retentionPolicy, error) {
	p := retentionPolicy{DiagnosticDays: 30, ArchiveDays: 30}
	err := db.QueryRow(`SELECT event_days,diagnostic_days,archive_days FROM retention_policy WHERE project_id=?`, project).Scan(&p.EventDays, &p.DiagnosticDays, &p.ArchiveDays)
	if err == sql.ErrNoRows {
		err = nil
	}
	return p, err
}
func (a *App) handleRetention(w http.ResponseWriter, r *http.Request) {
	if !requireUser(r) {
		http.Error(w, "unauthorized", 401)
		return
	}
	project, ok := requireRequestProject(w, r)
	if !ok {
		return
	}
	switch r.Method {
	case "GET":
		p, err := getRetention(requestReadDB(r), project)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		writeJSON(w, p)
	case "PUT":
		var p retentionPolicy
		if json.NewDecoder(r.Body).Decode(&p) != nil || p.EventDays < 0 || p.EventDays > 36500 || p.DiagnosticDays < 1 || p.DiagnosticDays > 3650 || p.ArchiveDays < 1 || p.ArchiveDays > 3650 {
			http.Error(w, "invalid retention policy", 400)
			return
		}
		_, err := globalCtx.AppDB().ExecContext(r.Context(), `INSERT INTO retention_policy VALUES(?,?,?,?) ON CONFLICT(project_id) DO UPDATE SET event_days=excluded.event_days,diagnostic_days=excluded.diagnostic_days,archive_days=excluded.archive_days`, project, p.EventDays, p.DiagnosticDays, p.ArchiveDays)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		writeJSON(w, p)
	default:
		http.Error(w, "GET or PUT only", 405)
	}
}
func pruneProject(ctx context.Context, db *sql.DB, project string, now int64) (int64, error) {
	policy, err := getRetention(contextualDB{db: db, ctx: ctx}, project)
	if err != nil {
		return 0, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var archived int64
	if policy.EventDays > 0 {
		// Select eligible rows before applying the batch limit, so retained
		// rollup inputs cannot starve older raw events.
		rows, err := tx.Query(`SELECT e.id,json_object('id',e.id,'ts',e.ts,'app',e.app,'topic',e.topic,'project_id',e.project_id,'install_id',e.install_id,'user_id',e.user_id,'session_id',e.session_id,'source',e.source,'upsert_key',e.upsert_key,'props',json(e.props))
          FROM events e WHERE e.project_id=? AND e.ts<? AND e.source!='rollup'
          AND NOT EXISTS(SELECT 1 FROM financial_mappings fm WHERE fm.component_event_id=e.id)
          AND NOT EXISTS(SELECT 1 FROM event_specs s WHERE s.project_id=e.project_id AND s.app=e.app AND s.topic=e.topic AND s.ingest_mode IN ('upsert','raw_plus_rollup')) ORDER BY e.ts,e.id LIMIT 1000`, project, now-int64(policy.EventDays)*86400000)
		if err != nil {
			return 0, err
		}
		type record struct {
			id  int64
			raw string
		}
		records := []record{}
		for rows.Next() {
			var row record
			if err = rows.Scan(&row.id, &row.raw); err != nil {
				rows.Close()
				return 0, err
			}
			records = append(records, row)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return 0, err
		}
		for _, row := range records {
			if _, err = tx.Exec(`INSERT OR IGNORE INTO event_archive VALUES(?,?,?,?)`, row.id, project, row.raw, now); err != nil {
				return 0, err
			}
			if _, err = tx.Exec(`DELETE FROM events WHERE id=?`, row.id); err != nil {
				return 0, err
			}
			archived++
		}
	}
	if _, err = tx.Exec(`DELETE FROM event_spec_violations WHERE id IN (SELECT id FROM event_spec_violations WHERE project_id=? AND seen_at<? LIMIT 1000)`, project, now-int64(policy.DiagnosticDays)*86400000); err != nil {
		return 0, err
	}
	if _, err = tx.Exec(`DELETE FROM event_archive WHERE id IN (SELECT id FROM event_archive WHERE project_id=? AND archived_at<? LIMIT 1000)`, project, now-int64(policy.ArchiveDays)*86400000); err != nil {
		return 0, err
	}
	return archived, tx.Commit()
}
func (a *App) retentionWorker(ctx context.Context, app *sdk.AppCtx) error {
	project := app.CurrentProject()
	if project == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, err := pruneProject(ctx, app.AppDB(), project, time.Now().UnixMilli()); err != nil {
		return err
	}
	_, err := app.AppDB().ExecContext(ctx, `DELETE FROM capture_inbox WHERE identity IN (SELECT identity FROM capture_inbox WHERE CASE WHEN json_valid(payload) THEN json_extract(payload,'$.project_id') END=? AND processed_at IS NOT NULL AND processed_at<? LIMIT 1000)`, project, time.Now().Add(-7*24*time.Hour).UnixMilli())
	return err
}

func (a *App) handleArchiveRestore(w http.ResponseWriter, r *http.Request) {
	if !requireUser(r) {
		http.Error(w, "unauthorized", 401)
		return
	}
	project, ok := requireRequestProject(w, r)
	if !ok {
		return
	}
	if r.Method != "POST" {
		http.Error(w, "POST only", 405)
		return
	}
	var body struct {
		ID int64 `json:"id"`
	}
	if json.NewDecoder(r.Body).Decode(&body) != nil {
		http.Error(w, "invalid json", 400)
		return
	}
	tx, err := globalCtx.AppDB().BeginTx(r.Context(), nil)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	defer tx.Rollback()
	var raw string
	if err = tx.QueryRow(`SELECT payload FROM event_archive WHERE id=? AND project_id=?`, body.ID, project).Scan(&raw); err != nil {
		http.Error(w, "archive not found", 404)
		return
	}
	var row EventRow
	if err = json.Unmarshal([]byte(raw), &row); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	_, err = tx.Exec(`INSERT INTO events(id,ts,app,topic,project_id,install_id,user_id,session_id,source,upsert_key,props) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, row.ID, row.TS, row.App, row.Topic, row.ProjectID, nullInt(row.InstallID), nullStr(row.UserID), nullStr(row.SessionID), row.Source, nullStr(row.UpsertKey), string(row.Props))
	if err != nil {
		http.Error(w, err.Error(), 409)
		return
	}
	if _, err = tx.Exec(`DELETE FROM event_archive WHERE id=?`, row.ID); err == nil {
		err = tx.Commit()
	}
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, map[string]any{"restored": row.ID})
}
func (a *App) handleHealth(w http.ResponseWriter, r *http.Request) {
	if !requireUser(r) {
		http.Error(w, "unauthorized", 401)
		return
	}
	project, ok := requireRequestProject(w, r)
	if !ok {
		return
	}
	db := requestReadDB(r)
	var events, violations, archived, pending, gaps, pageCount, pageSize, migrationIssues int64
	for _, item := range []struct {
		q    string
		dest *int64
	}{{`SELECT COUNT(*) FROM events WHERE project_id=?`, &events}, {`SELECT COUNT(*) FROM event_spec_violations WHERE project_id=?`, &violations}, {`SELECT COUNT(*) FROM event_archive WHERE project_id=?`, &archived}, {`SELECT COUNT(*) FROM capture_inbox WHERE processed_at IS NULL AND json_extract(payload,'$.project_id')=?`, &pending}} {
		if err := db.QueryRow(item.q, project).Scan(item.dest); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
	}
	var lastError string
	_ = db.QueryRow(`SELECT gaps,last_error FROM capture_state WHERE id=1`).Scan(&gaps, &lastError)
	_ = db.QueryRow(`PRAGMA page_count`).Scan(&pageCount)
	_ = db.QueryRow(`PRAGMA page_size`).Scan(&pageSize)
	_ = db.QueryRow(`SELECT COUNT(*) FROM analytics_migration_issues i JOIN events e ON e.id=i.event_id WHERE e.project_id=?`, project).Scan(&migrationIssues)
	writeJSON(w, map[string]any{"migration_issues": migrationIssues, "events": events, "violations": violations, "archived": archived, "capture_pending": pending, "capture_gaps": gaps, "capture_error": lastError, "database_bytes_global": pageCount * pageSize, "query_count_global": queryCount.Load(), "query_total_ms_global": queryMillis.Load(), "delivery_guarantee": "best effort; upstream firehose is not durable"})
}
func (a *App) handleFX(w http.ResponseWriter, r *http.Request) {
	if !requireUser(r) {
		http.Error(w, "unauthorized", 401)
		return
	}
	project, ok := requireRequestProject(w, r)
	if !ok {
		return
	}
	switch r.Method {
	case "GET":
		rows, err := listFXRates(requestReadDB(r), project, r.URL.Query().Get("base_currency"), r.URL.Query().Get("quote_currency"), intQuery64(r, "since"), intQuery64(r, "until"), 200)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		writeJSON(w, map[string]any{"rates": rows, "has_more": len(rows) == 200})
	case "POST":
		var rate FXRate
		if json.NewDecoder(r.Body).Decode(&rate) != nil {
			http.Error(w, "invalid json", 400)
			return
		}
		saved, err := upsertFXRate(contextualDB{db: globalCtx.AppDB(), ctx: r.Context()}, project, rate)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		writeJSON(w, saved)
	default:
		http.Error(w, "GET or POST only", 405)
	}
}
func intQuery64(r *http.Request, key string) int64 { return parseInt64(r.URL.Query().Get(key)) }
func (a *App) handleReferences(w http.ResponseWriter, r *http.Request) {
	if !requireUser(r) {
		http.Error(w, "unauthorized", 401)
		return
	}
	project, ok := requireRequestProject(w, r)
	if !ok {
		return
	}
	if r.Method == "POST" {
		var body struct {
			Set      string          `json:"reference_set"`
			Key      string          `json:"key"`
			Label    string          `json:"label"`
			Value    string          `json:"value"`
			Status   string          `json:"status"`
			Metadata json.RawMessage `json:"metadata"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			http.Error(w, "invalid json", 400)
			return
		}
		var saved any
		var err error
		if body.Set == "" {
			saved, err = upsertReferenceSet(globalCtx.AppDB(), ReferenceSet{ProjectID: project, Key: body.Key, Label: body.Label})
		} else {
			saved, err = upsertReferenceValue(globalCtx.AppDB(), project, body.Set, ReferenceValue{Value: body.Value, Label: body.Label, Status: body.Status, Metadata: body.Metadata})
		}
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		writeJSON(w, saved)
		return
	}
	if r.Method != "GET" {
		http.Error(w, "GET or POST only", 405)
		return
	}
	if key := r.URL.Query().Get("reference_set"); key != "" {
		result, err := referencePage(requestReadDB(r), project, key, r.URL.Query().Get("status"), r.URL.Query().Get("search"), intQuery64(r, "after"), 200)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		writeJSON(w, result)
		return
	}
	rows, err := requestReadDB(r).Query(`SELECT key,label FROM reference_sets WHERE project_id=? ORDER BY key LIMIT 1000`, project)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	defer rows.Close()
	sets := []map[string]string{}
	for rows.Next() {
		var key, label string
		if err = rows.Scan(&key, &label); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		sets = append(sets, map[string]string{"key": key, "label": label})
	}
	writeJSON(w, map[string]any{"sets": sets, "has_more": len(sets) == 1000})
}
func referencePage(db sqlRunner, project, key, status, search string, after int64, limit int) (map[string]any, error) {
	set, err := getReferenceSet(db, project, key)
	if err != nil {
		return nil, err
	}
	if status != "" && status != "active" && status != "inactive" {
		return nil, fmt.Errorf("invalid status")
	}
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	where := `reference_set_id=? AND (?='' OR status=?) AND (?='' OR instr(lower(value||' '||label),lower(?))>0)`
	args := []any{set.ID, status, status, search, search}
	var total int64
	if err = db.QueryRow(`SELECT COUNT(*) FROM reference_values WHERE `+where, args...).Scan(&total); err != nil {
		return nil, err
	}
	rows, err := db.Query(`SELECT id,value,label,status,metadata_json FROM reference_values WHERE `+where+` AND id>? ORDER BY id LIMIT ?`, append(args, after, limit+1)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []ReferenceValue{}
	for rows.Next() {
		var v ReferenceValue
		var raw string
		if err = rows.Scan(&v.ID, &v.Value, &v.Label, &v.Status, &raw); err != nil {
			return nil, err
		}
		v.Metadata = json.RawMessage(raw)
		values = append(values, v)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	more := len(values) > limit
	if more {
		values = values[:limit]
	}
	next := int64(0)
	if more {
		next = values[len(values)-1].ID
	}
	return map[string]any{"reference_set": set, "values": values, "count": len(values), "total": total, "has_more": more, "next_cursor": next}, nil
}
