package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

func (a *App) lockEnvironment(deploymentID, environmentID int64) func() {
	value, _ := a.operationLocks.LoadOrStore(fmt.Sprintf("%d/%d", deploymentID, environmentID), &sync.Mutex{})
	mu := value.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}
func (a *App) refreshDeployment(d *Deployment) (*Deployment, error) {
	if globalCtx == nil || globalCtx.AppDB() == nil {
		return nil, errors.New("deployment database unavailable")
	}
	fresh, err := dbGetDeploymentByID(globalCtx.AppDB(), d.ID)
	if err != nil || fresh == nil {
		return nil, errors.New("deployment no longer exists")
	}
	if d.EnvironmentID > 0 {
		env, err := dbGetEnvironment(globalCtx.AppDB(), d.EnvironmentID)
		if err != nil || env == nil || env.DeploymentID != d.ID {
			return nil, errors.New("environment no longer exists")
		}
		fresh = effectiveDeploymentForEnvironment(fresh, env)
	}
	return fresh, nil
}
func (a *App) setIntent(d *Deployment, state string, releaseID int64) error {
	a.transitionMu.Lock()
	defer a.transitionMu.Unlock()
	_, err := globalCtx.AppDB().Exec(`INSERT INTO deployment_intents(deployment_id,environment_id,desired_state,release_id) VALUES(?,?,?,?) ON CONFLICT(deployment_id,environment_id) DO UPDATE SET desired_state=excluded.desired_state,release_id=excluded.release_id,generation=generation+1`, d.ID, d.EnvironmentID, state, releaseID)
	return err
}
func (a *App) recoveryAllowed(d *Deployment, id int64) bool {
	fresh, err := a.refreshDeployment(d)
	if err != nil || fresh.ArchivedAt != "" {
		return false
	}
	var state string
	var current int64
	err = globalCtx.AppDB().QueryRow(`SELECT desired_state,release_id FROM deployment_intents WHERE deployment_id=? AND environment_id=?`, d.ID, d.EnvironmentID).Scan(&state, &current)
	if err == nil {
		return state == "running" && current == id
	}
	return errors.Is(err, sql.ErrNoRows) && fresh.CurrentReleaseID != nil && *fresh.CurrentReleaseID == id
}
func (a *App) releaseMayPromote(rel *Release) bool {
	if rel == nil {
		return false
	}
	d, err := a.deploymentForRelease(rel)
	if err != nil || d == nil || d.ArchivedAt != "" {
		return false
	}
	var state string
	var id int64
	err = globalCtx.AppDB().QueryRow(`SELECT desired_state,release_id FROM deployment_intents WHERE deployment_id=? AND environment_id=?`, rel.DeploymentID, rel.EnvironmentID).Scan(&state, &id)
	return errors.Is(err, sql.ErrNoRows) || (err == nil && state == "running" && id == rel.ID)
}
func (a *App) releaseConfiguration(d *Deployment, id int64) *Deployment {
	var body string
	if globalCtx.AppDB().QueryRow(`SELECT config_json FROM release_runtime WHERE release_id=?`, id).Scan(&body) == nil {
		var snapshot Deployment
		if json.Unmarshal([]byte(body), &snapshot) == nil && snapshot.ID == d.ID {
			snapshot.EnvironmentID = d.EnvironmentID
			snapshot.EnvironmentName = d.EnvironmentName
			snapshot.CurrentReleaseID = d.CurrentReleaseID
			return &snapshot
		}
	}
	return d
}
func releaseOwnedPort(db *sql.DB, releaseID int64, port int) error {
	_, err := db.Exec(`DELETE FROM port_leases WHERE port=? AND release_id=?`, port, releaseID)
	return err
}
func (a *App) stopDeployment(d *Deployment) (any, error) {
	unlock := a.lockEnvironment(d.ID, d.EnvironmentID)
	defer unlock()
	// Persist before waiting for children: already queued callbacks see the stop.
	err := a.setIntent(d, "stopped", 0)
	if err != nil {
		return nil, err
	}
	_, err = globalCtx.AppDB().Exec(`UPDATE builds SET release_requested=0 WHERE deployment_id=? AND COALESCE(environment_id,0)=?`, d.ID, d.EnvironmentID)
	if err != nil {
		return nil, err
	}
	if err = a.stopRunningReleasesForDeployment(d.ID, d.EnvironmentID, 5*time.Second); err != nil {
		return nil, err
	}
	if d.EnvironmentID > 0 {
		err = dbSetEnvironmentCurrentRelease(globalCtx.AppDB(), d.EnvironmentID, nil)
	} else {
		err = dbSetCurrentRelease(globalCtx.AppDB(), d.ID, nil)
	}
	return map[string]any{"stopped": true}, err
}
func (a *App) retirePreviousRelease(id int64) {
	rel, err := dbGetRelease(globalCtx.AppDB(), id)
	if err != nil || rel == nil || rel.Status != "live" {
		return
	}
	unlock := a.lockEnvironment(rel.DeploymentID, rel.EnvironmentID)
	defer unlock()
	d, err := a.deploymentForRelease(rel)
	if err != nil || d == nil || d.CurrentReleaseID == nil || *d.CurrentReleaseID != id {
		return
	}
	var previous int64
	if globalCtx.AppDB().QueryRow(`SELECT previous_release_id FROM release_runtime WHERE release_id=?`, id).Scan(&previous) != nil || previous == 0 || previous == id {
		return
	}
	old, err := dbGetRelease(globalCtx.AppDB(), previous)
	if err != nil || old == nil {
		return
	}
	if err = a.stopReleaseAuthoritative(old, 5*time.Second); err != nil {
		emit("deploy.release.cleanup_failed", map[string]any{"release_id": previous, "error": err.Error()})
		return
	}
	a.markStopped(previous)
}

// An unpromoted candidate has no route to preserve. Invalidate its callbacks
// under the transition lock before waiting for its process to exit.
func (a *App) stopSupersededCandidates(d *Deployment) error {
	a.transitionMu.Lock()
	releases, err := dbListLiveReleases(globalCtx.AppDB())
	var candidates []Release
	if err == nil {
		for _, r := range releases {
			if r.DeploymentID == d.ID && r.EnvironmentID == d.EnvironmentID && r.Status == "starting" {
				if err = dbUpdateRelease(globalCtx.AppDB(), r.ID, map[string]any{"status": "stopped", "stopped_at": nowUTC()}); err != nil {
					break
				}
				candidates = append(candidates, r)
			}
		}
	}
	a.transitionMu.Unlock()
	if err != nil {
		return err
	}
	for _, r := range candidates {
		if err := a.stopReleaseAuthoritative(&r, 5*time.Second); err != nil {
			return err
		}
		a.markStopped(r.ID)
	}
	return nil
}
