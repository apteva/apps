package main

import (
	"database/sql"
	"errors"
	sdk "github.com/apteva/app-sdk"
	"time"
)

func (a *App) syncConversionCache(ctx *sdk.AppCtx, acct *adAccount, r *genericPerformanceRequest, base []analyticsPoint) map[string]any {
	points := []conversionPoint{}
	if acct.Platform == "google" {
		var out map[string]any
		points, out = a.fetchGoogleConversionPoints(ctx, acct, r)
		if out != nil {
			recordConversionSync(ctx, acct, r.Level, mcpErrorTextValue(out))
			return out
		}
	} else {
		for _, p := range base {
			points = append(points, providerConversionPoints(p)...)
		}
	}
	if err := storeConversionPoints(ctx, acct, r, points); err != nil {
		recordConversionSync(ctx, acct, r.Level, err.Error())
		return mcpError(err.Error())
	}
	recordConversionSync(ctx, acct, r.Level, "")
	ctx.EmitWithProject("conversion.updated", acct.ProjectID, map[string]any{"ad_account_id": acct.ID, "level": r.Level, "date_from": r.DateFrom, "date_to": r.DateTo})
	return nil
}
func recordConversionSync(ctx *sdk.AppCtx, acct *adAccount, level, message string) {
	now := time.Now().UTC()
	success, next := "", ""
	failures := 0
	if message != "" {
		_ = ctx.AppDB().QueryRow(`SELECT failure_count FROM ad_conversion_sync_state WHERE project_id=? AND ad_account_id=? AND level=?`, acct.ProjectID, acct.ID, level).Scan(&failures)
		failures++
		delay := time.Minute * time.Duration(1<<min(failures, 8))
		next = now.Add(delay).Format(time.RFC3339)
	} else {
		success = now.Format(time.RFC3339)
	}
	_, _ = ctx.AppDB().Exec(`INSERT INTO ad_conversion_sync_state(project_id,ad_account_id,level,last_success_at,last_error,failure_count,next_attempt_at) VALUES(?,?,?,?,?,?,?) ON CONFLICT DO UPDATE SET last_success_at=CASE WHEN excluded.last_success_at='' THEN last_success_at ELSE excluded.last_success_at END,last_error=excluded.last_error,failure_count=excluded.failure_count,next_attempt_at=excluded.next_attempt_at`, acct.ProjectID, acct.ID, level, success, message, failures, next)
}
func conversionSyncStatus(ctx *sdk.AppCtx, acct *adAccount, level string) (map[string]any, error) {
	var fetched, message, next string
	var failures int
	err := ctx.AppDB().QueryRow(`SELECT last_success_at,last_error,failure_count,next_attempt_at FROM ad_conversion_sync_state WHERE project_id=? AND ad_account_id=? AND level=?`, acct.ProjectID, acct.ID, level).Scan(&fetched, &message, &failures, &next)
	if errors.Is(err, sql.ErrNoRows) {
		return map[string]any{"status": "never_synced"}, nil
	}
	if err != nil {
		return nil, err
	}
	status := "ok"
	if message != "" {
		status = "failed"
	}
	return map[string]any{"status": status, "last_success_at": fetched, "last_error": message, "failure_count": failures, "next_attempt_at": next}, nil
}
func (a *App) collectMobileConversions(ctx *sdk.AppCtx, acct *adAccount, r *genericPerformanceRequest, points []analyticsPoint) {
	var bound int
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM ad_mobile_campaigns WHERE project_id=? AND ad_account_id=?`, acct.ProjectID, acct.ID).Scan(&bound); err != nil || bound == 0 {
		return
	}
	state, err := conversionSyncStatus(ctx, acct, r.Level)
	if err != nil {
		return
	}
	if next, err := time.Parse(time.RFC3339, firstString(state, "next_attempt_at")); err == nil && time.Now().Before(next) {
		return
	}
	if out := a.syncConversionCache(ctx, acct, r, points); out != nil {
		ctx.Logger().Warn("mobile conversion sync failed", "account", acct.ID, "level", r.Level, "err", mcpErrorTextValue(out))
	}
}
