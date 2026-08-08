// Backup captures database snapshots from Apteva and full managed data
// directories from providers such as Fleet.
//
// Architecture sketch:
//
//	┌──────────────────────┐  cron tick   ┌──────────────────────┐
//	│  jobs app            │ ───────────► │  backup app          │
//	│  (jobs_schedule)     │   POST /run  │  (this binary)       │
//	└──────────────────────┘              └──────┬───────────────┘
//	                                             │
//	                                             │  GET /api/platform/snapshot
//	                                             ▼
//	                                      ┌──────────────────────┐
//	                                      │  apteva-server       │
//	                                      │   • VACUUM INTO each │
//	                                      │     SQLite DB        │
//	                                      │   • streams tar.gz   │
//	                                      └──────┬───────────────┘
//	                                             │
//	                                             ▼
//	                                      ┌──────────────────────┐
//	                                      │  destination         │
//	                                      │  local | s3 | r2     │
//	                                      └──────────────────────┘
//
// The platform owns the privileged database snapshot primitive. This app
// owns scheduling, destinations, retention, encryption, and the UI.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
version: 0.3.3
description: |
  Periodic database backups of the platform DB and app.db from running
  sidecars. Supports local disk, AWS S3, Cloudflare R2, and local Fleet tenants.
author: Apteva
scopes: [global]
min_apteva_version: "0.10.0"
requires:
  permissions:
    - db.write.app
    - net.egress
    - platform.apps.call
    - platform.connections.read_credentials
  apps:
    - name: jobs
      version: ">=0.1.8"
      reason: Cron scheduling for periodic backup runs.
  integrations:
    - role: fleet_provider
      kind: app
      required: false
      compatible_app_names: [fleet]
      label: Fleet app
      hint: Bind Fleet to create, schedule, and restore per-tenant backups.
    - role: cloud_storage
      kind: integration
      compatible_slugs: [aws-s3, cloudflare-r2]
      capabilities: [object.put, object.get, object.list, object.delete]
      tools:
        object.put: put_object
        object.get: get_object
        object.list: list_objects
        object.delete: delete_object
      required: false
      label: Cloud storage (optional)
      hint: Bind AWS S3 or Cloudflare R2 to enable cloud destinations.
provides:
  http_routes:
    - prefix: /
  mcp_tools:
    - { name: backup_now, description: "Run a platform or Fleet tenant backup immediately." }
    - { name: backup_schedule, description: "Create a scheduled platform or Fleet tenant backup policy." }
    - { name: backup_list, description: "List past backup runs." }
    - { name: backup_restore, description: "Verify and restore a past backup." }
  ui_panels:
    - slot: project.page
      label: Backup
      icon: archive
      entry: /ui/BackupPanel.mjs
runtime:
  kind: source
  source:
    repo: github.com/apteva/apps
    ref: backup/v0.3.3
    entry: mcp/backup
  port: 8080
  health_check: /health
db:
  driver: sqlite
  path: /data/backup.db
  migrations: migrations/
config_schema:
  - name: keep_last_n
    type: text
    default: "14"
    label: Default retention (last N runs)
    description: Default used by new policies. 0 disables pruning.
  - name: encryption_passphrase
    type: password
    default: ""
    label: Encryption passphrase (optional)
    description: Age-encrypt backups before upload and verify them before restore.
  - name: failed_history_retention_days
    type: text
    default: "90"
    label: Failed-run history retention (days)
    description: Failed and interrupted run rows older than this are removed. Successful restore history is preserved with its stored object.
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
	if err := reconcileInterruptedRuns(ctx); err != nil {
		return fmt.Errorf("reconcile interrupted backup runs: %w", err)
	}
	if err := pruneFailedRunHistory(ctx); err != nil {
		ctx.Logger().Warn("prune failed backup history", "err", err.Error())
	}
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
		{Pattern: "/scopes", Handler: a.handleScopes},
		{Pattern: "/run", Handler: a.handleRunNow},      // cron + UI entry
		{Pattern: "/restore", Handler: a.handleRestore}, // POST {run_id}
	}
}

// ─── MCP tools (the agent's surface) ───────────────────────────────

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{
			Name:        "backup_now",
			Description: "Run a backup immediately. Args: destination_id (default: only enabled destination), scope_kind? (default platform), scope_id?, source_app?. For Fleet tenant backups use scope_kind=fleet_tenant, source_app=fleet, scope_id=<tenant_id>.",
			InputSchema: schemaObject(map[string]any{
				"policy_id":      map[string]any{"type": "integer"},
				"destination_id": map[string]any{"type": "integer"},
				"scope_kind":     map[string]any{"type": "string"},
				"scope_id":       map[string]any{"type": "string"},
				"source_app":     map[string]any{"type": "string"},
				"async":          map[string]any{"type": "boolean", "description": "Return after queueing the run. Used by Jobs schedules."},
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
			Name:        "backup_schedule",
			Description: "Create a scheduled backup policy through the Jobs app. Args: name, schedule (cron), destination_id? (defaults to only enabled destination), retention_keep? (default 14), scope_kind? (default platform), scope_id?, source_app?, project_id?. For Fleet tenant backups use scope_kind=fleet_tenant, source_app=fleet, scope_id=<tenant_id>.",
			InputSchema: schemaObject(map[string]any{
				"name":           map[string]any{"type": "string"},
				"schedule":       map[string]any{"type": "string"},
				"destination_id": map[string]any{"type": "integer"},
				"retention_keep": map[string]any{"type": "integer"},
				"scope_kind":     map[string]any{"type": "string"},
				"scope_id":       map[string]any{"type": "string"},
				"source_app":     map[string]any{"type": "string"},
				"project_id":     map[string]any{"type": "string"},
			}, []string{"name", "schedule"}),
			Handler: a.toolBackupSchedule,
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
	if policyID := int64Arg(args, "policy_id"); policyID != 0 {
		policy, err := dbGetPolicy(ctx.AppDB(), policyID)
		if err != nil {
			return nil, err
		}
		dest, err := dbGetDestination(ctx.AppDB(), policy.DestinationID)
		if err != nil {
			return nil, err
		}
		if boolArg(args, "async") {
			go func() {
				if _, runErr := runBackup(ctx, dest, policy, policy.Scope); runErr != nil {
					ctx.Logger().Error("scheduled backup failed", "policy_id", policy.ID, "err", runErr.Error())
				}
			}()
			return map[string]any{"status": "accepted", "policy_id": policy.ID}, nil
		}
		run, err := runBackup(ctx, dest, policy, policy.Scope)
		if err != nil {
			return map[string]any{"run": run, "status": "failed", "error": err.Error()}, err
		}
		return map[string]any{"run": run, "status": "success"}, nil
	}
	destID := int64Arg(args, "destination_id")
	dest, err := pickDestination(ctx.AppDB(), destID)
	if err != nil {
		return nil, err
	}
	scope := scopeFromArgs(args)
	if err := validateScope(scope); err != nil {
		return nil, err
	}
	run, err := runBackup(ctx, dest, nil, scope)
	if err != nil {
		return map[string]any{"run": run, "status": "failed", "error": err.Error()}, err
	}
	return map[string]any{"run": run, "status": "success"}, nil
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

func (a *App) toolBackupSchedule(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	name := strings.TrimSpace(getStringArg(args, "name"))
	schedule := strings.TrimSpace(getStringArg(args, "schedule"))
	if name == "" || schedule == "" {
		return nil, errors.New("name and schedule required")
	}
	dest, err := pickDestination(ctx.AppDB(), int64Arg(args, "destination_id"))
	if err != nil {
		return nil, err
	}
	writer, err := openDestination(dest, ctx, defaultLocalBackupDir(ctx))
	if err != nil {
		return nil, fmt.Errorf("open destination: %w", err)
	}
	checkCtx, cancelCheck := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelCheck()
	if err := writer.Check(checkCtx); err != nil {
		return nil, fmt.Errorf("destination check failed: %w", err)
	}
	scope := scopeFromArgs(args)
	if err := validateScope(scope); err != nil {
		return nil, err
	}
	keep := intArg(args, "retention_keep", defaultRetention(ctx))
	if keep < 0 {
		return nil, errors.New("retention_keep must be 0 or greater")
	}
	p, err := dbCreatePolicy(ctx.AppDB(), &Policy{
		Name:          name,
		Schedule:      schedule,
		DestinationID: dest.ID,
		RetentionKeep: keep,
		Scope:         scope,
	})
	if err != nil {
		return nil, err
	}
	if err := scheduleViaJobs(ctx, p, getStringArg(args, "project_id")); err != nil {
		if _, deleteErr := ctx.AppDB().Exec(`DELETE FROM policies WHERE id = ?`, p.ID); deleteErr != nil {
			return nil, fmt.Errorf("schedule policy: %v; remove incomplete policy: %w", err, deleteErr)
		}
		return nil, fmt.Errorf("schedule policy: %w", err)
	}
	return map[string]any{"policy": p}, nil
}

func defaultRetention(ctx *sdk.AppCtx) int {
	if ctx == nil {
		return 14
	}
	n, err := strconv.Atoi(strings.TrimSpace(ctx.Config().Get("keep_last_n")))
	if err != nil || n < 0 {
		return 14
	}
	return n
}

func failedHistoryRetentionDays(ctx *sdk.AppCtx) int {
	if ctx == nil {
		return 90
	}
	n, err := strconv.Atoi(strings.TrimSpace(ctx.Config().Get("failed_history_retention_days")))
	if err != nil || n < 0 {
		return 90
	}
	return n
}

func reconcileInterruptedRuns(ctx *sdk.AppCtx) error {
	_, err := ctx.AppDB().Exec(
		`UPDATE runs
		 SET status = 'failed', stage = 'failed', finished_at = CURRENT_TIMESTAMP,
		     error = CASE WHEN error = '' THEN 'backup process restarted before completion' ELSE error END
		 WHERE status = 'running'`)
	return err
}

func pruneFailedRunHistory(ctx *sdk.AppCtx) error {
	days := failedHistoryRetentionDays(ctx)
	if days == 0 {
		return nil
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -days).Format(time.RFC3339Nano)
	_, err := ctx.AppDB().Exec(
		`DELETE FROM runs WHERE status = 'failed' AND datetime(started_at) < datetime(?)`, cutoff)
	return err
}

func (a *App) handleScopes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ctx := getAppCtx(r)
	out := map[string]any{
		"default_retention":  defaultRetention(ctx),
		"encryption_enabled": backupPassphrase(ctx) != "",
		"platform": map[string]any{
			"kind": "platform", "label": "Platform databases",
			"coverage": "Platform DB and app.db from running sidecars",
			"gaps":     []string{"non-database app files", "stopped sidecars", "external storage", "repositories", "host configuration"},
		},
		"fleet_bound":   false,
		"fleet_tenants": []any{},
	}
	if ctx.IntegrationFor("fleet_provider") == nil {
		httpJSON(w, out)
		return
	}
	var result struct {
		Tenants []struct {
			ID         string `json:"id"`
			Slug       string `json:"slug"`
			Kind       string `json:"kind"`
			Status     string `json:"status"`
			InstanceID int64  `json:"instance_id"`
			ConfigDir  string `json:"config_dir"`
		} `json:"tenants"`
	}
	if err := ctx.PlatformAPI().CallAppResult("fleet", "tenant_list", map[string]any{}, &result); err != nil {
		out["fleet_error"] = err.Error()
		httpJSON(w, out)
		return
	}
	tenantScopes := make([]map[string]any, 0, len(result.Tenants))
	for _, tenant := range result.Tenants {
		restorable := tenant.Kind == "local" && tenant.InstanceID == 0 && tenant.ConfigDir != ""
		tenantScopes = append(tenantScopes, map[string]any{
			"id": tenant.ID, "slug": tenant.Slug, "status": tenant.Status,
			"restorable": restorable,
		})
	}
	out["fleet_bound"] = true
	out["fleet_tenants"] = tenantScopes
	httpJSON(w, out)
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
	status := "success"
	if partial, _ := report["partial_failure"].(bool); partial {
		status = "partial"
	}
	return map[string]any{"report": report, "status": status}, nil
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
		if body.Kind == kindS3 {
			bound := ctx.IntegrationFor("cloud_storage")
			if bound == nil {
				httpErr(w, http.StatusBadRequest, "bind a cloud_storage connection before creating an S3 destination")
				return
			}
			if body.ConnectionID == 0 {
				body.ConnectionID = bound.ConnectionID
			} else if body.ConnectionID != bound.ConnectionID {
				httpErr(w, http.StatusConflict, fmt.Sprintf("connection_id %d is not the currently bound cloud_storage connection %d", body.ConnectionID, bound.ConnectionID))
				return
			}
		}
		writer, err := openDestination(&body, ctx, defaultLocalBackupDir(ctx))
		if err != nil {
			httpErr(w, http.StatusBadRequest, "open destination: "+err.Error())
			return
		}
		checkCtx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		if err := writer.Check(checkCtx); err != nil {
			httpErr(w, http.StatusBadRequest, "destination check failed: "+err.Error())
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
	suffix := strings.Trim(strings.TrimPrefix(r.URL.Path, "/destinations/"), "/")
	parts := strings.Split(suffix, "/")
	id, _ := strconv.ParseInt(parts[0], 10, 64)
	if id == 0 {
		httpErr(w, http.StatusBadRequest, "id required")
		return
	}
	if len(parts) == 2 && parts[1] == "test" {
		if r.Method != http.MethodPost {
			httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		d, err := dbGetDestination(ctx.AppDB(), id)
		if err != nil || !d.Enabled {
			httpErr(w, http.StatusNotFound, "enabled destination not found")
			return
		}
		writer, err := openDestination(d, ctx, defaultLocalBackupDir(ctx))
		if err == nil {
			checkCtx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
			defer cancel()
			err = writer.Check(checkCtx)
		}
		if err != nil {
			httpErr(w, http.StatusBadGateway, err.Error())
			return
		}
		httpJSON(w, map[string]any{"ok": true})
		return
	}
	if len(parts) != 1 {
		httpErr(w, http.StatusNotFound, "not found")
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
		if err := dbSoftDeleteDestination(ctx.AppDB(), id); err != nil {
			if errors.Is(err, errDestinationInUse) {
				httpErr(w, http.StatusConflict, err.Error())
			} else if errors.Is(err, sql.ErrNoRows) {
				httpErr(w, http.StatusNotFound, fmt.Sprintf("destination %d not found", id))
			} else {
				httpErr(w, http.StatusInternalServerError, err.Error())
			}
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
		body.Name = strings.TrimSpace(body.Name)
		body.Schedule = strings.TrimSpace(body.Schedule)
		if body.Name == "" || body.Schedule == "" || body.DestinationID == 0 {
			httpErr(w, http.StatusBadRequest, "name, schedule, and destination_id required")
			return
		}
		if body.RetentionKeep < 0 {
			httpErr(w, http.StatusBadRequest, "retention_keep must be 0 or greater")
			return
		}
		if body.Scope.Kind == "" {
			body.Scope = defaultScope()
		}
		if err := validateScope(body.Scope); err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		destination, err := dbGetDestination(ctx.AppDB(), body.DestinationID)
		if err != nil || !destination.Enabled {
			httpErr(w, http.StatusBadRequest, "destination_id must reference an enabled destination")
			return
		}
		writer, err := openDestination(destination, ctx, defaultLocalBackupDir(ctx))
		if err != nil {
			httpErr(w, http.StatusBadRequest, "open destination: "+err.Error())
			return
		}
		checkCtx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		if err := writer.Check(checkCtx); err != nil {
			httpErr(w, http.StatusBadRequest, "destination check failed: "+err.Error())
			return
		}
		p, err := dbCreatePolicy(ctx.AppDB(), &body)
		if err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		// Pass the operator's currently-selected project_id (from the
		// dashboard URL) so the cron job lands in that project's Jobs
		// panel. Backup itself is scope:global, so its sidecar has no
		// natural project context — the panel sends ?project_id=<pid>
		// on every call and we forward it. Empty falls through to a
		// project-less ("global") tag.
		if jobsErr := scheduleViaJobs(ctx, p, r.URL.Query().Get("project_id")); jobsErr != nil {
			if _, deleteErr := ctx.AppDB().Exec(`DELETE FROM policies WHERE id = ?`, p.ID); deleteErr != nil {
				httpErr(w, http.StatusInternalServerError, fmt.Sprintf("schedule policy: %v; remove incomplete policy: %v", jobsErr, deleteErr))
				return
			}
			httpErr(w, http.StatusBadGateway, "schedule policy: "+jobsErr.Error())
			return
		}
		httpJSON(w, map[string]any{"policy": p})
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
		// Cancel the Jobs row before deleting the policy so a failed
		// cancellation cannot leave a recurring call to a missing policy.
		if p, err := dbGetPolicy(ctx.AppDB(), id); err == nil && p.JobsID != "" {
			projectID := p.JobsProjectID
			if projectID == "" {
				projectID = r.URL.Query().Get("project_id")
			}
			if err := cancelViaJobs(ctx, p.JobsID, projectID); err != nil {
				httpErr(w, http.StatusBadGateway, "cancel scheduled job: "+err.Error())
				return
			}
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
	runs, err := dbListRuns(ctx.AppDB(), destID, limit+1)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	hasMore := len(runs) > limit
	if hasMore {
		runs = runs[:limit]
	}
	httpJSON(w, map[string]any{"runs": runs, "has_more": hasMore})
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
		PolicyID      int64  `json:"policy_id"`
		DestinationID int64  `json:"destination_id"`
		ScopeKind     string `json:"scope_kind"`
		ScopeID       string `json:"scope_id"`
		SourceApp     string `json:"source_app"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		httpErr(w, http.StatusBadRequest, "invalid json")
		return
	}

	var dest *Destination
	var policy *Policy
	scope := defaultScope()
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
		scope = p.Scope
	} else {
		d, err := pickDestination(ctx.AppDB(), body.DestinationID)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		dest = d
		scope = Scope{Kind: body.ScopeKind, ID: body.ScopeID, SourceApp: body.SourceApp}
		if scope.Kind == "" {
			scope = defaultScope()
		}
	}
	if err := validateScope(scope); err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	run, err := runBackup(ctx, dest, policy, scope)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": err.Error(), "run": run})
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
	Kind         string          `json:"kind"`   // "local" | "s3" | "storage_app"
	Config       json.RawMessage `json:"config"` // shape depends on kind
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
	JobsProjectID string `json:"jobs_project_id,omitempty"`
	Scope         Scope  `json:"scope"`
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
	Stage           string `json:"stage,omitempty"`
	BytesCompressed int64  `json:"bytes_compressed"`
	SHA256          string `json:"sha256,omitempty"`
	RemoteKey       string `json:"remote_key,omitempty"`
	Error           string `json:"error,omitempty"`
	Encrypted       bool   `json:"encrypted"`
	Scope           Scope  `json:"scope"`
}

type Scope struct {
	Kind      string `json:"kind"`
	ID        string `json:"id,omitempty"`
	SourceApp string `json:"source_app,omitempty"`
}

// ─── DB helpers ────────────────────────────────────────────────────

func dbCreateDestination(db *sql.DB, d *Destination) (*Destination, error) {
	res, err := db.Exec(
		`INSERT INTO destinations (name, kind, config_json, connection_id, enabled)
		 VALUES (?, ?, ?, ?, ?)`,
		d.Name, d.Kind, string(d.Config), nullInt(d.ConnectionID), 1)
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
		 FROM destinations WHERE deleted_at = '' ORDER BY id`)
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
			return nil, err
		}
		d.Config = json.RawMessage(cfg)
		d.Enabled = enabled != 0
		out = append(out, d)
	}
	return out, rows.Err()
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

var errDestinationInUse = errors.New("destination is referenced by a policy; delete the policy first")

func dbSoftDeleteDestination(db *sql.DB, id int64) error {
	var policies int
	if err := db.QueryRow(`SELECT COUNT(*) FROM policies WHERE destination_id = ?`, id).Scan(&policies); err != nil {
		return err
	}
	if policies > 0 {
		return errDestinationInUse
	}
	result, err := db.Exec(
		`UPDATE destinations SET enabled = 0, deleted_at = CURRENT_TIMESTAMP,
		 name = name || '-deleted-' || id WHERE id = ? AND deleted_at = ''`, id)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func dbCreatePolicy(db *sql.DB, p *Policy) (*Policy, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	if p.Scope.Kind == "" {
		p.Scope = defaultScope()
	}
	res, err := db.Exec(
		`INSERT INTO policies (name, schedule, destination_id, retention_keep, enabled, jobs_id, jobs_project_id, scope_kind, scope_id, source_app, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, '', '', ?, ?, ?, ?, ?)`,
		p.Name, p.Schedule, p.DestinationID, p.RetentionKeep, boolToInt(true),
		p.Scope.Kind, p.Scope.ID, p.Scope.SourceApp, now, now)
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
		`SELECT id, name, schedule, destination_id, retention_keep, enabled, jobs_id, jobs_project_id,
		        scope_kind, scope_id, source_app, created_at, updated_at
		 FROM policies ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Policy{}
	for rows.Next() {
		p := &Policy{}
		var enabled int
		if err := rows.Scan(&p.ID, &p.Name, &p.Schedule, &p.DestinationID, &p.RetentionKeep, &enabled, &p.JobsID, &p.JobsProjectID,
			&p.Scope.Kind, &p.Scope.ID, &p.Scope.SourceApp, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		p.Enabled = enabled != 0
		out = append(out, p)
	}
	return out, rows.Err()
}

func dbGetPolicy(db *sql.DB, id int64) (*Policy, error) {
	p := &Policy{}
	var enabled int
	err := db.QueryRow(
		`SELECT id, name, schedule, destination_id, retention_keep, enabled, jobs_id, jobs_project_id,
		        scope_kind, scope_id, source_app, created_at, updated_at
		 FROM policies WHERE id = ?`, id).
		Scan(&p.ID, &p.Name, &p.Schedule, &p.DestinationID, &p.RetentionKeep, &enabled, &p.JobsID, &p.JobsProjectID,
			&p.Scope.Kind, &p.Scope.ID, &p.Scope.SourceApp, &p.CreatedAt, &p.UpdatedAt)
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
	if r.Scope.Kind == "" {
		r.Scope = defaultScope()
	}
	if r.StartedAt == "" {
		r.StartedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	res, err := db.Exec(
		`INSERT INTO runs (policy_id, destination_id, destination_name, started_at, status, scope_kind, scope_id, source_app)
		 VALUES (?, ?, ?, ?, 'running', ?, ?, ?)`,
		nullInt(r.PolicyID), r.DestinationID, r.DestinationName,
		r.StartedAt, r.Scope.Kind, r.Scope.ID, r.Scope.SourceApp)
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	return id, nil
}

func dbFinishRun(db *sql.DB, id int64, status string, bytes int64, sha, remoteKey, manifestJSON, errMsg string, encrypted bool) error {
	_, err := db.Exec(
		`UPDATE runs SET status = ?, finished_at = CURRENT_TIMESTAMP,
		   stage = ?, bytes_compressed = ?, sha256 = ?, remote_key = ?, manifest_json = ?, error = ?, encrypted = ?
		 WHERE id = ?`,
		status, status, bytes, sha, remoteKey, manifestJSON, errMsg, boolToInt(encrypted), id)
	return err
}

func dbUpdateRunStage(db *sql.DB, id int64, stage string) error {
	_, err := db.Exec(`UPDATE runs SET stage = ? WHERE id = ? AND status = 'running'`, stage, id)
	return err
}

func dbListRuns(db *sql.DB, destID int64, limit int) ([]*Run, error) {
	q := `SELECT id, COALESCE(policy_id,0), destination_id, destination_name,
	             started_at, COALESCE(finished_at,''), status, bytes_compressed,
		             sha256, remote_key, error, encrypted, stage, scope_kind, scope_id, source_app
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
			&r.SHA256, &r.RemoteKey, &r.Error, &r.Encrypted, &r.Stage, &r.Scope.Kind, &r.Scope.ID, &r.Scope.SourceApp); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func dbGetRun(db *sql.DB, id int64) (*Run, error) {
	r := &Run{}
	err := db.QueryRow(
		`SELECT id, COALESCE(policy_id,0), destination_id, destination_name,
		        started_at, COALESCE(finished_at,''), status, bytes_compressed,
		        sha256, remote_key, error, encrypted, stage, scope_kind, scope_id, source_app
		 FROM runs WHERE id = ?`, id).
		Scan(&r.ID, &r.PolicyID, &r.DestinationID, &r.DestinationName,
			&r.StartedAt, &r.FinishedAt, &r.Status, &r.BytesCompressed,
			&r.SHA256, &r.RemoteKey, &r.Error, &r.Encrypted, &r.Stage, &r.Scope.Kind, &r.Scope.ID, &r.Scope.SourceApp)
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
		destination, err := dbGetDestination(db, id)
		if err != nil {
			return nil, err
		}
		if !destination.Enabled {
			return nil, fmt.Errorf("destination %d is disabled", id)
		}
		return destination, nil
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

func defaultScope() Scope {
	return Scope{Kind: "platform"}
}

func scopeFromArgs(args map[string]any) Scope {
	s := Scope{
		Kind:      strings.TrimSpace(getStringArg(args, "scope_kind")),
		ID:        strings.TrimSpace(getStringArg(args, "scope_id")),
		SourceApp: strings.TrimSpace(getStringArg(args, "source_app")),
	}
	if s.Kind == "" {
		return defaultScope()
	}
	return s
}

func validateScope(s Scope) error {
	switch s.Kind {
	case "", "platform":
		if s.ID != "" || s.SourceApp != "" {
			return errors.New("platform backups must not set scope_id or source_app")
		}
		return nil
	case "fleet_tenant":
		if s.ID == "" {
			return errors.New("fleet_tenant backups require scope_id")
		}
		if s.SourceApp == "" {
			return errors.New("fleet_tenant backups require source_app=fleet")
		}
		if s.SourceApp != "fleet" {
			return fmt.Errorf("fleet_tenant backups must use source_app=fleet, got %q", s.SourceApp)
		}
		return nil
	default:
		return fmt.Errorf("unsupported backup scope %q", s.Kind)
	}
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
	switch v := args[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	case json.Number:
		n, err := strconv.Atoi(v.String())
		if err == nil {
			return n
		}
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err == nil {
			return n
		}
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

func getStringArg(args map[string]any, key string) string {
	if v, ok := args[key].(string); ok {
		return v
	}
	return ""
}

func boolArg(args map[string]any, key string) bool {
	v, _ := args[key].(bool)
	return v
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
