package main

import (
	"context"
	"errors"
	sdk "github.com/apteva/app-sdk"
	"strings"
	"time"
)

func (a *App) startMaintenance(ctx *sdk.AppCtx) {
	startLogSink(ctx.AppDB(), func(err error) { ctx.Logger().Warn("request log batch failed", "error", safeUpstreamError(err)) })
	run, cancel := context.WithCancel(context.Background())
	a.maintenanceCancel = cancel
	a.maintenanceDone = make(chan struct{})
	go func() {
		defer close(a.maintenanceDone)
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-run.Done():
				return
			case <-ticker.C:
				if value, ok := logSinks.Load(ctx.AppDB()); ok {
					s := value.(*logSink)
					if dropped := s.dropped.Swap(0); dropped > 0 {
						ctx.Logger().Warn("request log queue full", "dropped", dropped)
					}
				}
				if err := pruneRequestLogs(ctx.AppDB()); err != nil {
					ctx.Logger().Warn("log retention failed", "error", safeUpstreamError(err))
				}
				a.mutationMu.Lock()
				err := a.retryPendingWork(ctx)
				a.mutationMu.Unlock()
				if err != nil {
					ctx.Logger().Warn("gateway reconciliation pending", "error", safeUpstreamError(err))
				}
			}
		}
	}()
}
func (a *App) retryPendingWork(ctx *sdk.AppCtx) error {
	rows, err := ctx.AppDB().Query(`SELECT api_id,project_id,delete_requested FROM api_policy_sync WHERE pending=1`)
	if err != nil {
		return err
	}
	type job struct {
		id      int64
		pid     string
		deleted bool
	}
	var jobs []job
	for rows.Next() {
		var j job
		if err := rows.Scan(&j.id, &j.pid, &j.deleted); err != nil {
			rows.Close()
			return err
		}
		jobs = append(jobs, j)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	var errs []error
	for _, j := range jobs {
		scoped := ctx.WithProject(j.pid)
		if j.deleted {
			errs = append(errs, deleteAPIBrowserPolicy(scoped, j.id))
			continue
		}
		api, err := dbGetAPIByID(ctx.AppDB(), j.pid, j.id)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if api != nil {
			_, err := syncAPIBrowserOriginPolicy(scoped, api)
			errs = append(errs, err)
		}
	}
	errs = append(errs, a.reconcileExposures(ctx))
	apis, listErr := dbListAllAPIs(ctx.AppDB())
	errs = append(errs, listErr)
	for _, api := range apis {
		if api.Status == "active" && api.Hostname != "" && (api.IngressStatus == "" || strings.HasPrefix(api.DNSStatus, "error:") || strings.HasPrefix(api.IngressStatus, "error:")) {
			a.configureExposure(ctx.WithProject(api.ProjectID), api)
		}
	}
	return errors.Join(errs...)
}
func deleteAPIBrowserPolicy(ctx *sdk.AppCtx, id int64) error {
	_, err := ctx.AppDB().Exec(`INSERT INTO api_policy_sync(api_id,project_id,delete_requested,pending) VALUES(?,?,1,1) ON CONFLICT(api_id) DO UPDATE SET delete_requested=1,pending=1`, id, ctx.CurrentProject())
	if err != nil {
		return err
	}
	if ctx.BrowserOriginsAPI() == nil {
		return errors.New("platform browser-origin API unavailable")
	}
	err = ctx.DeleteBrowserOrigins(browserOriginRegistrationKey(id))
	msg := ""
	if err != nil {
		msg = safeUpstreamError(err)
	}
	_, dbErr := ctx.AppDB().Exec(`UPDATE api_policy_sync SET pending=?,error=? WHERE api_id=?`, err != nil, msg, id)
	return errors.Join(err, dbErr)
}
