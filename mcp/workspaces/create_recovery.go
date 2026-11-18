package main

// A workspace that fails while it is being created must not keep its name:
// names are unique per project for every workspace that is not destroyed, and
// callers such as Code retry with the same name. These helpers release the
// name of workspaces that never became ready, let app callers get their
// resource's existing workspace back, and turn a real name clash into an
// error that says which workspace holds the name.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// stuckProvisioningAfter is how long a workspace may stay provisioning before
// the reconcile worker treats the create as abandoned (for example Workspaces
// restarted mid-create). Creation itself runs within one tool call.
const stuckProvisioningAfter = 15 * time.Minute

// neverReady reports whether a workspace failed before it was ever handed to
// its caller, so it holds no work and can be discarded.
func neverReady(app *sdk.AppCtx, w *Workspace) bool {
	if w == nil {
		return false
	}
	if w.LifecycleStatus == statusProvisioning {
		return true
	}
	if w.LifecycleStatus != statusFailed {
		return false
	}
	if w.WorkloadID == "" || w.RuntimeStatus == "destroyed" {
		return true
	}
	var ready int
	_ = app.AppDB().QueryRow(`SELECT COUNT(*) FROM workspace_activity WHERE workspace_id=? AND event_type='workspace.created'`, w.ID).Scan(&ready)
	return ready == 0
}

// retireFailedCreate removes whatever a failed create left behind and marks
// the workspace destroyed, which releases its name. The error is kept on the
// record. If the leftover container cannot be removed the workspace stays
// failed, so a container is never orphaned.
func (a *App) retireFailedCreate(app *sdk.AppCtx, w *Workspace, reason string) error {
	if w.WorkloadID != "" && w.RuntimeStatus != "destroyed" {
		var out map[string]any
		if err := app.PlatformAPI().CallAppResult("containers", "containers_destroy", map[string]any{
			"workload_id": w.WorkloadID, "delete_volumes": true,
		}, &out); err != nil && !strings.Contains(strings.ToLower(err.Error()), "not found") {
			_ = updateWorkspace(app.AppDB(), w.ID, map[string]any{
				"lifecycle_status": statusFailed, "last_error": reason + "; cleanup failed: " + err.Error(), "updated_at": nowUTC(),
			})
			return fmt.Errorf("clean up failed workspace %s: %w", w.ID, err)
		}
	}
	now := nowUTC()
	if err := updateWorkspace(app.AppDB(), w.ID, map[string]any{
		"lifecycle_status": statusDestroyed, "activity_status": activityIdle, "runtime_status": "destroyed",
		"destroyed_at": now, "last_error": reason, "updated_at": now,
	}); err != nil {
		return err
	}
	_ = recordActivity(app.AppDB(), w.ID, w.ProjectID, "workspace.create_failed", Actor{Kind: "system", ID: "workspaces", Label: "Workspaces"},
		"Workspace creation failed; name released", map[string]any{"error": reason})
	return nil
}

// failCreate is the single exit for a create that failed after its record
// was inserted.
func (a *App) failCreate(app *sdk.AppCtx, w *Workspace, err error) error {
	if retireErr := a.retireFailedCreate(app, w, err.Error()); retireErr != nil {
		return errors.Join(err, retireErr)
	}
	return err
}

func findActiveWorkspaceByName(db *sql.DB, projectID, name string) (*Workspace, error) {
	w, err := scanWorkspace(db.QueryRow(`SELECT `+workspaceColumns+` FROM workspaces WHERE project_id=? AND name=? AND lifecycle_status!='destroyed' LIMIT 1`, projectID, name))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return w, err
}

func findWorkspaceForResource(db *sql.DB, projectID string, installID int64, resourceKind, resourceID string) (*Workspace, error) {
	w, err := scanWorkspace(db.QueryRow(`SELECT `+workspaceColumns+` FROM workspaces
		WHERE project_id=? AND consumer_install_id=? AND resource_kind=? AND resource_id=?
		  AND kind='dev' AND lifecycle_status!='destroyed'
		ORDER BY created_at DESC LIMIT 1`, projectID, installID, resourceKind, resourceID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return w, err
}

// claimName makes sure a create may use name. A workspace of the same caller
// that never became ready is retired; any other holder is reported by id.
func (a *App) claimName(app *sdk.AppCtx, actor Actor, name string) error {
	holder, err := findActiveWorkspaceByName(app.AppDB(), actor.ProjectID, name)
	if err != nil || holder == nil {
		return err
	}
	sameCaller := holder.ConsumerInstallID == actor.InstallID && (actor.InstallID > 0 || holder.OwnerAgentID == actor.AgentID)
	if sameCaller && holder.LifecycleStatus != statusProvisioning && neverReady(app, holder) {
		return a.retireFailedCreate(app, holder, firstNonEmpty(holder.LastError, "workspace creation failed"))
	}
	return nameTakenError(name, holder)
}

func nameTakenError(name string, holder *Workspace) error {
	return fmt.Errorf("workspace name %q is already used by workspace %s (status %s); use that workspace, destroy it with workspace_destroy, or choose another name",
		name, holder.ID, holder.LifecycleStatus)
}

// reuseResourceWorkspace returns the caller's existing workspace for a
// resource, made usable again where that is safe. ok=false means the caller
// should create a new workspace.
func (a *App) reuseResourceWorkspace(callCtx context.Context, app *sdk.AppCtx, actor Actor, args map[string]any, ttl int) (*Workspace, bool, error) {
	kind, id := strArg(args, "resource_kind"), strArg(args, "resource_id")
	if actor.InstallID <= 0 || kind == "" || id == "" {
		return nil, false, nil
	}
	w, err := findWorkspaceForResource(app.AppDB(), actor.ProjectID, actor.InstallID, kind, id)
	if err != nil || w == nil {
		return nil, false, err
	}
	if neverReady(app, w) {
		if w.LifecycleStatus == statusProvisioning && time.Since(parseTime(w.UpdatedAt)) < stuckProvisioningAfter {
			// Another create for this resource is still running.
			return w, true, nil
		}
		return nil, false, a.retireFailedCreate(app, w, firstNonEmpty(w.LastError, "workspace creation did not finish"))
	}
	if w.LifecycleStatus != statusRunning {
		// A workspace that once ran keeps its volumes: resume it rather than
		// replace it, so no work is lost. Extend it first if its TTL ran out.
		status := w.LifecycleStatus
		if expires := parseTime(w.ExpiresAt); w.LifecycleStatus == statusExpired || !expires.After(time.Now().UTC()) {
			if w, err = a.extendWorkspace(app, actor, w, ttl); err != nil {
				return nil, false, err
			}
		}
		resumed, err := a.resumeWorkspace(app, actor, w)
		if err != nil {
			return nil, false, fmt.Errorf("resource already has workspace %s (status %s) and it could not be resumed: %w", w.ID, status, err)
		}
		w = resumed
	}
	_ = recordActivity(app.AppDB(), w.ID, w.ProjectID, "workspace.reused", actor, "Existing workspace returned for its resource", map[string]any{"resource_kind": kind, "resource_id": id})
	return w, true, nil
}

// retireStuckCreates handles creates that never finished, for example when
// Workspaces restarted in the middle of one.
func (a *App) retireStuckCreates(app *sdk.AppCtx, rows []*Workspace) error {
	var errs []error
	for _, w := range rows {
		if w.LifecycleStatus != statusProvisioning || time.Since(parseTime(w.UpdatedAt)) < stuckProvisioningAfter {
			continue
		}
		if err := a.retireFailedCreate(app, w, "workspace creation did not finish"); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// insertError replaces the raw unique-index error (two creates raced for one
// name) with one that names the workspace holding it.
func insertError(app *sdk.AppCtx, projectID, name string, err error) error {
	if !strings.Contains(err.Error(), "UNIQUE") {
		return err
	}
	if holder, findErr := findActiveWorkspaceByName(app.AppDB(), projectID, name); findErr == nil && holder != nil {
		return nameTakenError(name, holder)
	}
	return err
}
