package main

import (
	"database/sql"
	"errors"
)

// Creating the release and acknowledging its build intent is one commit.
// After a crash, the durable release is reconciled rather than dispatched twice.
func (a *App) createOperationRelease(d *Deployment, b *Build) (*Release, bool, error) {
	a.transitionMu.Lock()
	defer a.transitionMu.Unlock()
	db := globalCtx.AppDB()
	tx, err := db.Begin()
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	if d.EnvironmentID > 0 {
		if err = tx.QueryRow(`SELECT current_release_id FROM deployment_environments WHERE id=?`, d.EnvironmentID).Scan(&d.CurrentReleaseID); err != nil {
			return nil, false, err
		}
	} else {
		if err = tx.QueryRow(`SELECT current_release_id FROM deployments WHERE id=?`, d.ID).Scan(&d.CurrentReleaseID); err != nil {
			return nil, false, err
		}
	}
	if d.AutomaticRelease {
		var existing sql.NullInt64
		var requested bool
		var status string
		if err = tx.QueryRow(`SELECT automatic_release_id,release_requested,status FROM builds WHERE id=?`, b.ID).Scan(&existing, &requested, &status); err != nil {
			return nil, false, err
		}
		if existing.Valid {
			tx.Rollback()
			r, err := dbGetRelease(db, existing.Int64)
			return r, r != nil && r.Status == "starting" && r.Provider == "pending_mobile", err
		}
		if !requested || status != "succeeded" {
			return nil, false, errors.New("automatic release request was cancelled")
		}
	}
	mobile := isMobileDeployment(d, b)
	provider := ""
	if mobile {
		provider = "pending_mobile"
	}
	res, err := tx.Exec(`INSERT INTO releases(deployment_id,environment_id,build_id,status,provider,created_at) VALUES(?,?,?,'starting',?,?)`, d.ID, nullInt64(d.EnvironmentID), b.ID, provider, nowUTC())
	if err != nil {
		return nil, false, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, false, err
	}
	if d.AutomaticRelease {
		if _, err = tx.Exec(`UPDATE builds SET automatic_release_id=?,release_requested=? WHERE id=?`, id, mobile, b.ID); err != nil {
			return nil, false, err
		}
	}
	if !mobile {
		if _, err = tx.Exec(`INSERT INTO deployment_intents(deployment_id,environment_id,desired_state,release_id) VALUES(?,?,'running',?) ON CONFLICT(deployment_id,environment_id) DO UPDATE SET desired_state='running',release_id=excluded.release_id,generation=generation+1`, d.ID, d.EnvironmentID, id); err != nil {
			return nil, false, err
		}
		previous := int64(0)
		if d.CurrentReleaseID != nil {
			previous = *d.CurrentReleaseID
		}
		if _, err = tx.Exec(`INSERT INTO release_runtime(release_id,config_json,previous_release_id) VALUES(?,?,?)`, id, mustJSON(d), previous); err != nil {
			return nil, false, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, false, err
	}
	r, err := dbGetRelease(db, id)
	return r, true, err
}

func (a *App) submitBuild(d *Deployment, opts *releaseOptions) (*Build, error) {
	if normalizeBuildBackend(d.BuildBackend) != buildBackendLocal {
		return a.runBuildWithOptions(d, opts)
	}
	build, err := dbCreateBuildForEnv(globalCtx.AppDB(), d.ID, d.EnvironmentID, d.Framework, d.BuildCmd)
	if err != nil {
		return nil, err
	}
	if opts != nil {
		if err = dbUpdateBuild(globalCtx.AppDB(), build.ID, map[string]any{"release_requested": true, "release_options_json": mustJSON(opts)}); err != nil {
			return nil, err
		}
	}
	snapshot := *d
	go func() {
		if snapshot.TargetKind == "ios" || snapshot.TargetKind == "android" {
			cfg, err := parseMobileTargetConfig(snapshot.TargetConfigJSON)
			if err != nil {
				a.failBuild(build, err.Error())
				return
			}
			if !cfg.SmokeOnly {
				if _, err = a.setupMobileSigning(buildContext(BuildOverrides{}), &snapshot, snapshot.BuildBackend, false); err != nil {
					a.failBuild(build, err.Error())
					return
				}
			}
		}
		result, err := a.runLocalBuildRecord(&snapshot, build)
		if err != nil || result == nil {
			return
		}
		if result.Status == "succeeded" && result.ReleaseRequested {
			_ = a.runRequestedCloudRelease(&snapshot, result)
		}
	}()
	return dbGetBuild(globalCtx.AppDB(), build.ID)
}
