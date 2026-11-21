package main

import (
	"errors"
	"net/url"

	sdk "github.com/apteva/app-sdk"
)

// Stage hostnames use their own exposure ledger so legacy API hostname
// cleanup cannot accidentally retire a promoted stage.
func (a *App) configureStageExposure(ctx *sdk.AppCtx, stage *APIStage) {
	if stage == nil {
		return
	}
	if _, err := ctx.AppDB().Exec(`UPDATE api_stage_exposures SET cleanup=1 WHERE project_id=? AND stage_id=? AND hostname<>?`, stage.ProjectID, stage.ID, stage.Hostname); err != nil {
		ctx.Logger().Warn("stage exposure cleanup queue failed", "error", safeUpstreamError(err))
		return
	}
	if stage.Hostname == "" || stage.Status != "active" {
		_ = a.reconcileStageExposures(ctx)
		return
	}
	if ctx.PlatformAPI() == nil {
		return
	}
	if err := ensureStageHostnameAvailable(ctx.AppDB(), stage.ProjectID, stage.Hostname, stage.ID); err != nil {
		_, _ = ctx.AppDB().Exec(`INSERT INTO api_stage_exposures(hostname,project_id,stage_id,cleanup,error) VALUES(?,?,?,?,?) ON CONFLICT(hostname) DO UPDATE SET error=?,cleanup=0`, stage.Hostname, stage.ProjectID, stage.ID, 0, safeUpstreamError(err), safeUpstreamError(err))
		return
	}
	if _, err := ctx.AppDB().Exec(`INSERT INTO api_stage_exposures(hostname,project_id,stage_id,cleanup,error) VALUES(?,?,?,?,?) ON CONFLICT(hostname) DO UPDATE SET project_id=excluded.project_id,stage_id=excluded.stage_id,cleanup=0,error=''`, stage.Hostname, stage.ProjectID, stage.ID, 0, ""); err != nil {
		ctx.Logger().Warn("stage exposure ledger update failed", "error", safeUpstreamError(err))
		return
	}
	_, err := ctx.PlatformAPI().ExposeIngress(sdk.IngressExposeRequest{
		Hostname:  stage.Hostname,
		Target:    "app://api/gw?project_id=" + url.QueryEscape(stage.ProjectID),
		ProjectID: stage.ProjectID,
		OwnerKind: "api_stage",
		CertFQDN:  stage.Hostname,
		TLSMode:   "auto",
	})
	if err != nil {
		_, _ = ctx.AppDB().Exec(`UPDATE api_stage_exposures SET error=? WHERE hostname=?`, safeUpstreamError(err), stage.Hostname)
	}
	_ = a.reconcileStageExposures(ctx)
}

func (a *App) reconcileStageExposures(ctx *sdk.AppCtx) error {
	if ctx.PlatformAPI() == nil {
		return nil
	}
	if _, err := ctx.AppDB().Exec(`UPDATE api_stage_exposures SET cleanup=1 WHERE NOT EXISTS
 (SELECT 1 FROM api_stages WHERE api_stages.id=api_stage_exposures.stage_id AND api_stages.project_id=api_stage_exposures.project_id AND api_stages.hostname=api_stage_exposures.hostname AND api_stages.status='active')`); err != nil {
		return err
	}
	rows, err := ctx.AppDB().Query(`SELECT hostname,project_id FROM api_stage_exposures WHERE cleanup=1`)
	if err != nil {
		return err
	}
	type stale struct{ hostname, project string }
	var pending []stale
	for rows.Next() {
		var x stale
		if err := rows.Scan(&x.hostname, &x.project); err != nil {
			rows.Close()
			return err
		}
		pending = append(pending, x)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	var errs []error
	for _, x := range pending {
		err := ctx.WithProject(x.project).PlatformAPI().UnexposeIngress(x.hostname)
		if err != nil {
			_, _ = ctx.AppDB().Exec(`UPDATE api_stage_exposures SET error=? WHERE hostname=?`, safeUpstreamError(err), x.hostname)
			errs = append(errs, err)
			continue
		}
		_, err = ctx.AppDB().Exec(`DELETE FROM api_stage_exposures WHERE hostname=?`, x.hostname)
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}
