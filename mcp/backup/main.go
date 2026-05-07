// Backup app v0.1 — periodic snapshots of the whole Apteva instance.
//
// Architecture sketch:
//
//   ┌──────────────────────┐  cron tick   ┌──────────────────────┐
//   │  jobs app            │ ───────────► │  backup app          │
//   │  (jobs_schedule)     │   POST /run  │  (this binary)       │
//   └──────────────────────┘              └──────┬───────────────┘
//                                                │
//                                                │  GET /api/platform/snapshot
//                                                ▼
//                                         ┌──────────────────────┐
//                                         │  apteva-server       │
//                                         │   • VACUUM INTO each │
//                                         │     SQLite DB        │
//                                         │   • streams tar.gz   │
//                                         └──────┬───────────────┘
//                                                │
//                                                ▼
//                                         ┌──────────────────────┐
//                                         │  destination         │
//                                         │  local | s3 | r2     │
//                                         └──────────────────────┘
//
// The platform owns the privileged primitive (read every install's
// data dir + the server DB). This app owns scheduling, destinations,
// retention, encryption, and the UI.
package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

// ─── Manifest (also lives in apteva.yaml; embedded so the binary is
// self-describing for `backup --help` and so install-time loaders can
// read it without re-fetching from disk) ───────────────────────────

const manifestYAML = `schema: apteva-app/v1
name: backup
display_name: Backup
version: 0.2.7
description: |
  Periodic backups of your Apteva instance — server DB plus every
  installed app's data — driven by the platform snapshot endpoint
  with destinations on local disk or S3-compatible buckets.
author: Apteva
scopes: [global]
requires:
  permissions:
    - db.write.app
    - net.egress
    - platform.apps.call
    - platform.connections.execute
  apps:
    - name: jobs
      version: ">=0.1.7"
      reason: Cron scheduling for periodic backup runs.
  integrations:
    - role: cloud_storage
      kind: integration
      compatible_slugs: [aws-s3, cloudflare-r2]
      capabilities: [object.put, object.list, object.delete]
      tools:
        object.put:    put_object
        object.list:   list_objects
        object.delete: delete_object
      required: false
      label: "Cloud storage (optional)"
      hint: "Bind an S3-compatible connection (R2, S3, …) to enable cloud destinations. Local destinations work without this."
provides:
  http_routes:
    - prefix: /
  mcp_tools:
    - { name: backup_now,     description: "Run a backup immediately." }
    - { name: backup_list,    description: "List past backup runs." }
    - { name: backup_restore, description: "Restore a past backup. App DBs swap live; the platform DB is staged for the next server boot." }
  ui_panels:
    - slot: project.page
      label: Backup
      icon: archive
      entry: /ui/BackupPanel.mjs
runtime:
  kind: source
  source:
    repo: github.com/apteva/apps
    ref: main
    entry: mcp/backup
  port: 8080
  health_check: /health
db:
  driver: sqlite
  path: /data/backup.db
  migrations: migrations/
upgrade_policy: auto-patch
`

type App struct{}

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		panic("invalid embedded manifest: " + err.Error())
	}
	return *m
}

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx.AppDB() == nil {
		return errors.New("backup requires a db block")
	}
	globalCtx = ctx
	ctx.Logger().Info("backup mounted",
		"gateway", os.Getenv("APTEVA_GATEWAY_URL"))
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error       { return nil }
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) Workers() []sdk.Worker             { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

// ─── HTTP routes (REST surface for the UI panel + the cron callback) ─

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/destinations", Handler: a.handleDestinationsCollection},
		{Pattern: "/destinations/", Handler: a.handleDestinationItem},
		{Pattern: "/policies", Handler: a.handlePoliciesCollection},
		{Pattern: "/policies/", Handler: a.handlePolicyItem},
		{Pattern: "/runs", Handler: a.handleRunsCollection},
		{Pattern: "/runs/", Handler: a.handleRunItem},
		{Pattern: "/run", Handler: a.handleRunNow},     // cron + UI entry
		{Pattern: "/restore", Handler: a.handleRestore}, // POST {run_id}
	}
}

// ─── MCP tools (the agent's surface) ───────────────────────────────

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{
			Name:        "backup_now",
			Description: "Run a backup immediately. Args: destination_id (default: only enabled destination, or error if multiple).",
			InputSchema: schemaObject(map[string]any{
				"destination_id": map[string]any{"type": "integer"},
			}, nil),
			Handler: a.toolBackupNow,
		},
		{
			Name:        "backup_list",
			Description: "List past backup runs. Args: destination_id (filter), limit (default 50, max 500).",
			InputSchema: schemaObject(map[string]any{
				"destination_id": map[string]any{"type": "integer"},
				"limit":          map[string]any{"type": "integer"},
			}, nil),
			Handler: a.toolBackupList,
		},
		{
			Name:        "backup_restore",
			Description: "Restore the bytes of a past run. App DBs swap live; the platform DB is staged for the next server boot. Args: run_id (required).",
			InputSchema: schemaObject(map[string]any{
				"run_id": map[string]any{"type": "integer"},
			}, []string{"run_id"}),
			Handler: a.toolBackupRestore,
		},
	}
}

func main() { sdk.Run(&App{}) }

// ─── Tool handlers ─────────────────────────────────────────────────

func (a *App) toolBackupNow(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	destID := int64Arg(args, "destination_id")
	dest, err := pickDestination(ctx.AppDB(), destID)
	if err != nil {
		return nil, err
	}
	run, err := runBackup(ctx, dest, nil)
	if err != nil {
		return nil, err
	}
	return map[string]any{"run": run}, nil
}

func (a *App) toolBackupList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	destID := int64Arg(args, "destination_id")
	limit := intArg(args, "limit", 50)
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	runs, err := dbListRuns(ctx.AppDB(), destID, limit)
	if err != nil {
		return nil, err
	}
	return map[string]any{"runs": runs, "count": len(runs)}, nil
}

func (a *App) toolBackupRestore(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	runID := int64Arg(args, "run_id")
	if runID == 0 {
		return nil, errors.New("run_id required")
	}
	report, err := restoreFromRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"report": report}, nil
}

// ─── Destinations REST ──────────────────────────────────────────────

func (a *App) handleDestinationsCollection(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	switch r.Method {
	case http.MethodGet:
		rows, err := dbListDestinations(ctx.AppDB())
		if err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		httpJSON(w, map[string]any{"destinations": rows})
	case http.MethodPost:
		var body Destination
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			httpErr(w, http.StatusBadRequest, "invalid json")
			return
		}
		if err := validateDestination(&body); err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		d, err := dbCreateDestination(ctx.AppDB(), &body)
		if err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		httpJSON(w, map[string]any{"destination": d})
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleDestinationItem(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	id, _ := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/destinations/"), 10, 64)
	if id == 0 {
		httpErr(w, http.StatusBadRequest, "id required")
		return
	}
	switch r.Method {
	case http.MethodGet:
		d, err := dbGetDestination(ctx.AppDB(), id)
		if err != nil {
			httpErr(w, http.StatusNotFound, err.Error())
			return
		}
		httpJSON(w, map[string]any{"destination": d})
	case http.MethodDelete:
		if _, err := ctx.AppDB().Exec(`DELETE FROM destinations WHERE id = ?`, id); err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		httpJSON(w, map[string]any{"deleted": true})
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// ─── Policies REST ──────────────────────────────────────────────────

func (a *App) handlePoliciesCollection(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	switch r.Method {
	case http.MethodGet:
		rows, err := dbListPolicies(ctx.AppDB())
		if err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		httpJSON(w, map[string]any{"policies": rows})
	case http.MethodPost:
		var body Policy
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			httpErr(w, http.StatusBadRequest, "invalid json")
			return
		}
		if body.Schedule == "" || body.DestinationID == 0 {
			httpErr(w, http.StatusBadRequest, "schedule and destination_id required")
			return
		}
		if body.RetentionKeep == 0 {
			body.RetentionKeep = 14
		}
		p, err := dbCreatePolicy(ctx.AppDB(), &body)
		if err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		// Schedule via the jobs app. Failure here doesn't roll back the
		// row — the operator can fix the dependency and re-trigger via
		// PATCH later. We surface the error in the response.
		jobsErr := scheduleViaJobs(ctx, p)
		out := map[string]any{"policy": p}
		if jobsErr != nil {
			out["jobs_warning"] = jobsErr.Error()
		}
		httpJSON(w, out)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handlePolicyItem(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	id, _ := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/policies/"), 10, 64)
	if id == 0 {
		httpErr(w, http.StatusBadRequest, "id required")
		return
	}
	switch r.Method {
	case http.MethodGet:
		p, err := dbGetPolicy(ctx.AppDB(), id)
		if err != nil {
			httpErr(w, http.StatusNotFound, err.Error())
			return
		}
		httpJSON(w, map[string]any{"policy": p})
	case http.MethodDelete:
		// Best-effort cancel the jobs row. Failure doesn't block delete:
		// an orphan job is harmless (it'll POST /run with a stale
		// policy_id and that path is idempotent — we treat unknown ids
		// as a no-op).
		if p, err := dbGetPolicy(ctx.AppDB(), id); err == nil && p.JobsID != "" {
			_ = cancelViaJobs(ctx, p.JobsID)
		}
		if _, err := ctx.AppDB().Exec(`DELETE FROM policies WHERE id = ?`, id); err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		httpJSON(w, map[string]any{"deleted": true})
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// ─── Runs REST ──────────────────────────────────────────────────────

func (a *App) handleRunsCollection(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ctx := getAppCtx(r)
	destID, _ := strconv.ParseInt(r.URL.Query().Get("destination_id"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	runs, err := dbListRuns(ctx.AppDB(), destID, limit)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"runs": runs})
}

func (a *App) handleRunItem(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ctx := getAppCtx(r)
	id, _ := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/runs/"), 10, 64)
	run, err := dbGetRun(ctx.AppDB(), id)
	if err != nil {
		httpErr(w, http.StatusNotFound, err.Error())
		return
	}
	httpJSON(w, map[string]any{"run": run})
}

// handleRunNow is both the cron callback (POSTed by jobs) and the
// "run now" button in the UI. Body: {"policy_id": <int>} (cron) or
// {"destination_id": <int>} (UI ad-hoc).
func (a *App) handleRunNow(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ctx := getAppCtx(r)
	var body struct {
		PolicyID      int64 `json:"policy_id"`
		DestinationID int64 `json:"destination_id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	var dest *Destination
	var policy *Policy
	if body.PolicyID != 0 {
		p, err := dbGetPolicy(ctx.AppDB(), body.PolicyID)
		if err != nil {
			// Unknown policy id from a stale jobs row — succeed silently
			// so jobs doesn't keep retrying.
			httpJSON(w, map[string]any{"skipped": "unknown policy"})
			return
		}
		policy = p
		d, err := dbGetDestination(ctx.AppDB(), p.DestinationID)
		if err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		dest = d
	} else {
		d, err := pickDestination(ctx.AppDB(), body.DestinationID)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		dest = d
	}
	run, err := runBackup(ctx, dest, policy)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"run": run})
}

// handleRestore expects POST {run_id: <int>}.
func (a *App) handleRestore(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ctx := getAppCtx(r)
	var body struct {
		RunID int64 `json:"run_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.RunID == 0 {
		httpErr(w, http.StatusBadRequest, "run_id required")
		return
	}
	report, err := restoreFromRun(ctx, body.RunID)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"report": report})
}

// ─── Domain types ──────────────────────────────────────────────────

type Destination struct {
	ID           int64           `json:"id,omitempty"`
	Name         string          `json:"name"`
	Kind         string          `json:"kind"`         // "local" | "s3" | "storage_app"
	Config       json.RawMessage `json:"config"`        // shape depends on kind
	ConnectionID int64           `json:"connection_id,omitempty"`
	Enabled      bool            `json:"enabled"`
	CreatedAt    string          `json:"created_at,omitempty"`
}

type Policy struct {
	ID            int64  `json:"id,omitempty"`
	Name          string `json:"name"`
	Schedule      string `json:"schedule"` // cron
	DestinationID int64  `json:"destination_id"`
	RetentionKeep int    `json:"retention_keep"`
	Enabled       bool   `json:"enabled"`
	JobsID        string `json:"jobs_id,omitempty"`
	CreatedAt     string `json:"created_at,omitempty"`
	UpdatedAt     string `json:"updated_at,omitempty"`
}

type Run struct {
	ID              int64  `json:"id"`
	PolicyID        int64  `json:"policy_id,omitempty"`
	DestinationID   int64  `json:"destination_id"`
	DestinationName string `json:"destination_name"`
	StartedAt       string `json:"started_at"`
	FinishedAt      string `json:"finished_at,omitempty"`
	Status          string `json:"status"`
	BytesCompressed int64  `json:"bytes_compressed"`
	SHA256          string `json:"sha256,omitempty"`
	RemoteKey       string `json:"remote_key,omitempty"`
	Error           string `json:"error,omitempty"`
}

// ─── DB helpers ────────────────────────────────────────────────────

func dbCreateDestination(db *sql.DB, d *Destination) (*Destination, error) {
	res, err := db.Exec(
		`INSERT INTO destinations (name, kind, config_json, connection_id, enabled)
		 VALUES (?, ?, ?, ?, ?)`,
		d.Name, d.Kind, string(d.Config), nullInt(d.ConnectionID), boolToInt(d.Enabled || true))
	if err != nil {
		return nil, err
	}
	d.ID, _ = res.LastInsertId()
	d.Enabled = true
	return d, nil
}

func dbListDestinations(db *sql.DB) ([]*Destination, error) {
	rows, err := db.Query(
		`SELECT id, name, kind, config_json, COALESCE(connection_id,0), enabled, created_at
		 FROM destinations ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Destination{}
	for rows.Next() {
		d := &Destination{}
		var cfg string
		var enabled int
		if err := rows.Scan(&d.ID, &d.Name, &d.Kind, &cfg, &d.ConnectionID, &enabled, &d.CreatedAt); err != nil {
			continue
		}
		d.Config = json.RawMessage(cfg)
		d.Enabled = enabled != 0
		out = append(out, d)
	}
	return out, nil
}

func dbGetDestination(db *sql.DB, id int64) (*Destination, error) {
	d := &Destination{}
	var cfg string
	var enabled int
	err := db.QueryRow(
		`SELECT id, name, kind, config_json, COALESCE(connection_id,0), enabled, created_at
		 FROM destinations WHERE id = ?`, id).
		Scan(&d.ID, &d.Name, &d.Kind, &cfg, &d.ConnectionID, &enabled, &d.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("destination %d not found", id)
	}
	if err != nil {
		return nil, err
	}
	d.Config = json.RawMessage(cfg)
	d.Enabled = enabled != 0
	return d, nil
}

func dbCreatePolicy(db *sql.DB, p *Policy) (*Policy, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := db.Exec(
		`INSERT INTO policies (name, schedule, destination_id, retention_keep, enabled, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		p.Name, p.Schedule, p.DestinationID, p.RetentionKeep, boolToInt(true), now, now)
	if err != nil {
		return nil, err
	}
	p.ID, _ = res.LastInsertId()
	p.Enabled = true
	p.CreatedAt = now
	p.UpdatedAt = now
	return p, nil
}

func dbListPolicies(db *sql.DB) ([]*Policy, error) {
	rows, err := db.Query(
		`SELECT id, name, schedule, destination_id, retention_keep, enabled, jobs_id, created_at, updated_at
		 FROM policies ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Policy{}
	for rows.Next() {
		p := &Policy{}
		var enabled int
		if err := rows.Scan(&p.ID, &p.Name, &p.Schedule, &p.DestinationID, &p.RetentionKeep, &enabled, &p.JobsID, &p.CreatedAt, &p.UpdatedAt); err != nil {
			continue
		}
		p.Enabled = enabled != 0
		out = append(out, p)
	}
	return out, nil
}

func dbGetPolicy(db *sql.DB, id int64) (*Policy, error) {
	p := &Policy{}
	var enabled int
	err := db.QueryRow(
		`SELECT id, name, schedule, destination_id, retention_keep, enabled, jobs_id, created_at, updated_at
		 FROM policies WHERE id = ?`, id).
		Scan(&p.ID, &p.Name, &p.Schedule, &p.DestinationID, &p.RetentionKeep, &enabled, &p.JobsID, &p.CreatedAt, &p.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("policy %d not found", id)
	}
	if err != nil {
		return nil, err
	}
	p.Enabled = enabled != 0
	return p, nil
}

func dbInsertRun(db *sql.DB, r *Run) (int64, error) {
	res, err := db.Exec(
		`INSERT INTO runs (policy_id, destination_id, destination_name, status)
		 VALUES (?, ?, ?, 'running')`,
		nullInt(r.PolicyID), r.DestinationID, r.DestinationName)
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	return id, nil
}

func dbFinishRun(db *sql.DB, id int64, status string, bytes int64, sha, remoteKey, manifestJSON, errMsg string) error {
	_, err := db.Exec(
		`UPDATE runs SET status = ?, finished_at = CURRENT_TIMESTAMP,
		   bytes_compressed = ?, sha256 = ?, remote_key = ?, manifest_json = ?, error = ?
		 WHERE id = ?`,
		status, bytes, sha, remoteKey, manifestJSON, errMsg, id)
	return err
}

func dbListRuns(db *sql.DB, destID int64, limit int) ([]*Run, error) {
	q := `SELECT id, COALESCE(policy_id,0), destination_id, destination_name,
	             started_at, COALESCE(finished_at,''), status, bytes_compressed,
	             sha256, remote_key, error
	      FROM runs`
	args := []any{}
	if destID > 0 {
		q += ` WHERE destination_id = ?`
		args = append(args, destID)
	}
	q += ` ORDER BY started_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Run{}
	for rows.Next() {
		r := &Run{}
		if err := rows.Scan(&r.ID, &r.PolicyID, &r.DestinationID, &r.DestinationName,
			&r.StartedAt, &r.FinishedAt, &r.Status, &r.BytesCompressed,
			&r.SHA256, &r.RemoteKey, &r.Error); err != nil {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

func dbGetRun(db *sql.DB, id int64) (*Run, error) {
	r := &Run{}
	err := db.QueryRow(
		`SELECT id, COALESCE(policy_id,0), destination_id, destination_name,
		        started_at, COALESCE(finished_at,''), status, bytes_compressed,
		        sha256, remote_key, error
		 FROM runs WHERE id = ?`, id).
		Scan(&r.ID, &r.PolicyID, &r.DestinationID, &r.DestinationName,
			&r.StartedAt, &r.FinishedAt, &r.Status, &r.BytesCompressed,
			&r.SHA256, &r.RemoteKey, &r.Error)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("run %d not found", id)
	}
	if err != nil {
		return nil, err
	}
	return r, nil
}

// pickDestination resolves the destination_id arg. Zero means "the
// only enabled destination" — callers that omit it are typically
// laptop self-hosters with one local destination.
func pickDestination(db *sql.DB, id int64) (*Destination, error) {
	if id != 0 {
		return dbGetDestination(db, id)
	}
	dests, err := dbListDestinations(db)
	if err != nil {
		return nil, err
	}
	enabled := []*Destination{}
	for _, d := range dests {
		if d.Enabled {
			enabled = append(enabled, d)
		}
	}
	if len(enabled) == 0 {
		return nil, errors.New("no enabled destinations — create one first via /destinations")
	}
	if len(enabled) > 1 {
		return nil, errors.New("multiple destinations exist — pass destination_id explicitly")
	}
	return enabled[0], nil
}

// ─── Tiny utils + globalCtx + http helpers ──────────────────────────

var globalCtx *sdk.AppCtx

func getAppCtx(_ *http.Request) *sdk.AppCtx { return globalCtx }

func httpJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func httpErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func intArg(args map[string]any, key string, def int) int {
	if v, ok := args[key].(float64); ok {
		return int(v)
	}
	if v, ok := args[key].(int); ok {
		return v
	}
	return def
}

func int64Arg(args map[string]any, key string) int64 {
	switch v := args[key].(type) {
	case float64:
		return int64(v)
	case int:
		return int64(v)
	case int64:
		return v
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		return n
	}
	return 0
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nullInt(i int64) any {
	if i == 0 {
		return nil
	}
	return i
}

func schemaObject(props map[string]any, required []string) map[string]any {
	out := map[string]any{
		"type":       "object",
		"properties": props,
	}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}
