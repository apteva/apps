package main

import (
	"context"
	"errors"
	sdk "github.com/apteva/app-sdk"
	"time"
)

func (a *App) queueIngress(ctx *sdk.AppCtx, hostname, project, target string, releaseID int64) error {
	if ctx == nil || ctx.AppDB() == nil || ctx.PlatformAPI() == nil {
		return errors.New("ingress platform unavailable")
	}
	a.ingressMu.Lock()
	defer a.ingressMu.Unlock()
	_, err := ctx.AppDB().Exec(`INSERT INTO ingress_work(hostname,project_id,release_id,target,updated_at) VALUES(?,?,?,?,?) ON CONFLICT(hostname) DO UPDATE SET project_id=excluded.project_id,release_id=excluded.release_id,target=excluded.target,last_error='',applied_at='',updated_at=excluded.updated_at`, hostname, project, releaseID, target, nowUTC())
	if err != nil {
		return err
	}
	return a.applyIngress(ctx, hostname, project, target, releaseID)
}
func (a *App) applyIngress(ctx *sdk.AppCtx, hostname, project, target string, releaseID int64) error {
	var err error
	if target == "" {
		err = ctx.PlatformAPI().UnexposeIngress(hostname)
	} else {
		rel, lookupErr := dbGetRelease(ctx.AppDB(), releaseID)
		if lookupErr != nil || rel == nil || rel.Status != "live" {
			return errors.New("ingress target release is not live")
		}
		d, lookupErr := dbGetDeploymentByID(ctx.AppDB(), rel.DeploymentID)
		if lookupErr == nil && d != nil && rel.EnvironmentID > 0 {
			env, envErr := dbGetEnvironment(ctx.AppDB(), rel.EnvironmentID)
			if envErr != nil {
				return envErr
			}
			if env != nil {
				d = effectiveDeploymentForEnvironment(d, env)
			}
		}
		if lookupErr != nil || d == nil || d.CurrentReleaseID == nil || *d.CurrentReleaseID != releaseID || d.Domain != hostname {
			return errors.New("ingress target was superseded")
		}
		if !pidOwnsPort(rel.PID, rel.Port) {
			return errors.New("ingress target no longer owns its port")
		}
		_, err = ctx.PlatformAPI().ExposeIngress(sdk.IngressExposeRequest{Hostname: hostname, Target: target, OwnerKind: "deploy", CertFQDN: hostname, ProjectID: project})
	}
	errorText, applied := "", nowUTC()
	if err != nil {
		errorText = err.Error()
		applied = ""
	}
	_, saveErr := ctx.AppDB().Exec(`UPDATE ingress_work SET last_error=?,applied_at=? WHERE hostname=? AND target=? AND release_id=?`, errorText, applied, hostname, target, releaseID)
	if err != nil {
		return err
	}
	return saveErr
}
func (a *App) reconcileIngress(ctx context.Context) error {
	a.ingressMu.Lock()
	defer a.ingressMu.Unlock()
	db := globalCtx.AppDB()
	rows, err := db.Query(`SELECT hostname,project_id,target,release_id FROM ingress_work WHERE applied_at='' ORDER BY updated_at LIMIT 100`)
	if err != nil {
		return err
	}
	type item struct {
		host, project, target string
		id                    int64
	}
	var work []item
	for rows.Next() {
		var i item
		if err = rows.Scan(&i.host, &i.project, &i.target, &i.id); err != nil {
			rows.Close()
			return err
		}
		work = append(work, i)
	}
	rows.Close()
	var first error
	for _, i := range work {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err = a.applyIngress(globalCtx, i.host, i.project, i.target, i.id); err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		if i.id > 0 {
			var previous int64
			if db.QueryRow(`SELECT previous_release_id FROM release_runtime WHERE release_id=?`, i.id).Scan(&previous) == nil && previous > 0 {
				go a.retirePreviousRelease(i.id)
			}
		}
	}
	return first
}
func (a *App) restorePreviousIntent(rel *Release) {
	if rel == nil {
		return
	}
	db := globalCtx.AppDB()
	// Once readiness and ingress committed, this release is the recovery target.
	// Its later crash must never silently roll back to an older artifact.
	if rel.LastHealthAt != "" {
		var pending int
		_ = db.QueryRow(`SELECT COUNT(*) FROM ingress_work WHERE release_id=? AND applied_at='' AND target!=''`, rel.ID).Scan(&pending)
		if pending == 0 {
			return
		}
	}
	var previous int64
	if db.QueryRow(`SELECT previous_release_id FROM release_runtime WHERE release_id=?`, rel.ID).Scan(&previous) != nil || previous == 0 {
		return
	}
	result, err := db.Exec(`UPDATE deployment_intents SET release_id=?,generation=generation+1 WHERE deployment_id=? AND environment_id=? AND desired_state='running' AND release_id=?`, previous, rel.DeploymentID, rel.EnvironmentID, rel.ID)
	if err != nil {
		return
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return
	}
	old, err := dbGetRelease(db, previous)
	if err != nil || old == nil || old.Status != "live" {
		return
	}
	if rel.EnvironmentID > 0 {
		_, _ = db.Exec(`UPDATE deployment_environments SET current_release_id=? WHERE id=? AND current_release_id=?`, previous, rel.EnvironmentID, rel.ID)
	} else {
		_, _ = db.Exec(`UPDATE deployments SET current_release_id=? WHERE id=? AND current_release_id=?`, previous, rel.DeploymentID, rel.ID)
	}
	if d, err := a.deploymentForRelease(old); err == nil && d != nil && d.Domain != "" && d.CurrentReleaseID != nil && *d.CurrentReleaseID == previous {
		_ = registerRouteForDeployment(globalCtx, a, d)
	}
}
func (a *App) failUnreadyRelease(rel *Release) {
	if err := a.stopReleaseAuthoritative(rel, 5*time.Second); err != nil {
		return
	}
	a.markCrashed(rel.ID, errors.New("release did not become ready before its startup deadline"))
}

// Routing has its own status: a healthy process can still be unreachable while
// the platform retries an ingress change.
func routingStatus(d *Deployment) map[string]any {
	if d == nil || d.Domain == "" || globalCtx == nil {
		return nil
	}
	var target, lastError, applied string
	var releaseID int64
	err := globalCtx.AppDB().QueryRow(`SELECT target,last_error,applied_at,release_id FROM ingress_work WHERE hostname=? AND project_id=?`, d.Domain, d.ProjectID).Scan(&target, &lastError, &applied, &releaseID)
	if err != nil {
		return nil
	}
	state := "pending"
	if lastError != "" {
		state = "degraded"
	} else if applied != "" {
		state = "applied"
	}
	return map[string]any{"state": state, "error": lastError, "release_id": releaseID, "target": target}
}
