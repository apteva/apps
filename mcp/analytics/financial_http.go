package main

import (
	"encoding/json"
	"net/http"
	"time"
)

func financialOperator(w http.ResponseWriter, r *http.Request) (string, bool) {
	// The platform enforces editor access on write requests and supplies these
	// headers after authentication. A sibling-app/delegated-agent request cannot
	// turn its installation owner's identity into durable sharing consent.
	if !requireUser(r) || r.Header.Get("X-Apteva-Bound-Caller-Install-ID") != "" || r.Header.Get("X-Apteva-Caller-Agent") != "" || r.Header.Get("X-Apteva-Subject-Type") != "" {
		http.Error(w, "authenticated project operator required", 403)
		return "", false
	}
	return requireRequestProject(w, r)
}
func financialJSON(w http.ResponseWriter, r *http.Request, out any) bool {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		http.Error(w, "invalid request: "+err.Error(), 400)
		return false
	}
	return true
}
func (a *App) handleFinancialRefresh(w http.ResponseWriter, r *http.Request) {
	project, ok := financialOperator(w, r)
	if !ok {
		return
	}
	db := requestWriteDB(r)
	if _, err := db.Exec(`INSERT OR IGNORE INTO financial_projects(project_id) VALUES(?)`, project); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	switch r.Method {
	case "GET":
		state, err := financialStatus(requestReadDB(r), project)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		writeJSON(w, state)
	case "PUT":
		var in struct {
			Enabled   bool   `json:"enabled"`
			FXEnabled bool   `json:"fx_enabled"`
			Provider  string `json:"provider"`
		}
		if !financialJSON(w, r, &in) {
			return
		}
		if in.Provider != "ecb" {
			http.Error(w, "only ECB is supported", 400)
			return
		}
		_, err := db.Exec(`UPDATE financial_projects SET enabled=?,fx_enabled=?,provider=?,revision=revision+1 WHERE project_id=?`, in.Enabled, in.FXEnabled, in.Provider, project)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	case "POST":
		var in struct {
			ObjectiveID     int64 `json:"objective_id"`
			VerifyTarget    int64 `json:"verify_target"`
			VerifiedThrough int64 `json:"verified_through"`
		}
		if r.ContentLength != 0 && !financialJSON(w, r, &in) {
			return
		}
		if in.ObjectiveID != 0 {
			if _, err := getObjective(db, project, in.ObjectiveID); err != nil {
				http.Error(w, "objective not found", 404)
				return
			}
		}
		if in.VerifyTarget != 0 {
			now := time.Now().UnixMilli()
			if in.VerifiedThrough <= 0 || in.VerifiedThrough > now {
				http.Error(w, "verified_through must describe a completed reconciliation, not the future", 400)
				return
			}
			// Verification is an explicit operator attestation, never inferred from an
			// empty Analytics table or a fresh cache. It expires with input changes.
			res, err := db.Exec(`INSERT INTO financial_targets(target_id,verified_revision,verified_through,verified_by,verified_at) SELECT t.id,p.revision,?,?,? FROM objective_targets t JOIN objectives o ON o.id=t.objective_id JOIN financial_projects p ON p.project_id=o.project_id WHERE t.id=? AND o.project_id=? AND t.retired_at IS NULL ON CONFLICT(target_id) DO UPDATE SET verified_revision=excluded.verified_revision,verified_through=excluded.verified_through,verified_by=excluded.verified_by,verified_at=excluded.verified_at,last_attempt=0,next_retry=0`, in.VerifiedThrough, r.Header.Get("X-User-ID"), now, in.VerifyTarget, project)
			if err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			n, _ := res.RowsAffected()
			if n == 0 {
				http.Error(w, "target not found", 404)
				return
			}
		} else if err := queueFinancial(db, project); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		writeJSON(w, map[string]bool{"queued": true})
	default:
		http.Error(w, "GET, PUT or POST only", 405)
	}
}
func financialStatus(db sqlRunner, project string) (map[string]any, error) {
	var enabled, fx bool
	var revision, attempt, success int64
	var provider, message string
	err := db.QueryRow(`SELECT enabled,fx_enabled,provider,revision,last_attempt,last_success,last_error FROM financial_projects WHERE project_id=?`, project).Scan(&enabled, &fx, &provider, &revision, &attempt, &success, &message)
	if err != nil {
		return nil, err
	}
	targets, err := financialStatusRows(db, `SELECT t.id AS target_id,t.name,o.id AS objective_id,o.name AS objective_name,COALESCE(f.state,'pending') AS state,COALESCE(f.last_attempt,0) AS last_attempt,COALESCE(f.last_success,0) AS last_success,COALESCE(f.next_retry,0) AS next_retry,COALESCE(f.last_error,'') AS last_error,CASE WHEN COALESCE(f.input_revision,0)!=? OR COALESCE(f.definition_revision,0)!=t.updated_at THEN 1 ELSE 0 END AS pending FROM objective_targets t JOIN objectives o ON o.id=t.objective_id LEFT JOIN financial_targets f ON f.target_id=t.id WHERE o.project_id=? AND o.status='active' AND t.retired_at IS NULL ORDER BY o.id,t.id`, revision, project)
	if err != nil {
		return nil, err
	}
	fxrows, err := financialStatusRows(db, `SELECT base,quote,day,last_attempt,last_success,retry_count,next_retry,last_error FROM financial_fx_requests WHERE project_id=? ORDER BY last_error DESC,day DESC LIMIT 500`, project)
	if err != nil {
		return nil, err
	}
	mappings, err := financialStatusRows(db, `SELECT m.id,m.destination_target,m.share_id,m.component_event_id,m.enabled,m.last_attempt,m.last_success,m.source_measured_at,m.last_error,s.source_project,s.target_id AS source_target,s.metric_meaning,s.revoked_at FROM financial_mappings m JOIN financial_shares s ON s.id=m.share_id WHERE m.destination_project=?`, project)
	if err != nil {
		return nil, err
	}
	return map[string]any{"enabled": enabled, "fx_enabled": fx, "provider": provider, "revision": revision, "last_attempt": attempt, "last_success": success, "last_error": message, "targets": targets, "fx_requests": fxrows, "mappings": mappings, "cadence_seconds": 60, "reconcile_seconds": 300}, nil
}
func financialStatusRows(db sqlRunner, query string, args ...any) ([]map[string]any, error) {
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for rows.Next() {
		values := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range ptrs {
			ptrs[i] = &values[i]
		}
		if err = rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		item := map[string]any{}
		for i, c := range cols {
			item[c] = values[i]
		}
		out = append(out, item)
	}
	return out, rows.Err()
}
func (a *App) handleFinancialShares(w http.ResponseWriter, r *http.Request) {
	project, ok := financialOperator(w, r)
	if !ok {
		return
	}
	switch r.Method {
	case "GET":
		rows, err := financialStatusRows(requestReadDB(r), `SELECT * FROM financial_shares WHERE source_project=? ORDER BY approved_at DESC`, project)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		writeJSON(w, map[string]any{"shares": rows})
	case "POST":
		var in struct {
			TargetID    int64  `json:"target_id"`
			Destination string `json:"destination_project"`
			Meaning     string `json:"metric_meaning"`
			EconomicKey string `json:"economic_key"`
		}
		if !financialJSON(w, r, &in) {
			return
		}
		id, err := grantFinancialShare(requestWriteDB(r), project, in.TargetID, in.Destination, in.Meaning, r.Header.Get("X-User-ID"), in.EconomicKey)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		writeJSON(w, map[string]any{"id": id})
	case "DELETE":
		id := r.URL.Query().Get("id")
		err := revokeFinancialShare(requestWriteDB(r), project, id)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	default:
		http.Error(w, "GET, POST or DELETE only", 405)
	}
}
func (a *App) handleFinancialMappings(w http.ResponseWriter, r *http.Request) {
	project, ok := financialOperator(w, r)
	if !ok {
		return
	}
	switch r.Method {
	case "POST":
		var in struct {
			ShareID           string `json:"share_id"`
			DestinationTarget int64  `json:"destination_target"`
			ComponentID       int64  `json:"component_event_id"`
		}
		if !financialJSON(w, r, &in) {
			return
		}
		id, err := createFinancialMapping(r.Context(), globalCtx.AppDB(), project, in.ShareID, in.DestinationTarget, in.ComponentID, r.Header.Get("X-User-ID"))
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		writeJSON(w, map[string]any{"id": id})
	case "DELETE":
		res, err := requestWriteDB(r).Exec(`UPDATE financial_mappings SET enabled=0,last_error='mapping disabled' WHERE id=? AND destination_project=?`, r.URL.Query().Get("id"), project)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			http.Error(w, "mapping not found", 404)
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	default:
		http.Error(w, "POST or DELETE only", 405)
	}
}
