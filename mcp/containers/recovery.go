package main

import (
	"context"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"strings"
	"sync"
	"time"
)

// Record before pausing so a sidecar crash leaves a durable resume job.
func (a *App) pauseArchive(ctx context.Context, app *sdk.AppCtx, w *Workload) (func() error, error) {
	if _, ok := a.backend.(LocalDocker); !ok {
		return func() error { return nil }, nil
	}
	state, err := docker(ctx, "inspect", "--format", "{{.State.Running}} {{.State.Paused}}", w.ContainerName)
	if dockerExecUnavailable(err) {
		return func() error { return nil }, nil
	}
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(state) != "true false" {
		if strings.TrimSpace(state) == "true true" {
			return nil, fmt.Errorf("%w: workload is already paused", errConflict)
		}
		return func() error { return nil }, nil
	}
	if _, err = app.AppDB().Exec(`INSERT OR REPLACE INTO containers_archive_pauses(workload_id,project_id,container_name) VALUES(?,?,?)`, w.ID, w.ProjectID, w.ContainerName); err != nil {
		return nil, err
	}
	resume := func() error {
		c, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return a.resumeArchive(c, app, w.ID, w.ContainerName)
	}
	if _, err = docker(ctx, "pause", w.ContainerName); err != nil {
		return nil, errors.Join(err, resume())
	}
	return resume, nil
}
func (a *App) resumeArchive(ctx context.Context, app *sdk.AppCtx, id, name string) error {
	state, err := docker(ctx, "inspect", "--format", "{{.State.Paused}}", name)
	if err != nil && !isDockerMissingResourceError(err, "container") {
		return err
	}
	if strings.TrimSpace(state) == "true" {
		if _, err = docker(ctx, "unpause", name); err != nil {
			return err
		}
	}
	_, err = app.AppDB().Exec(`DELETE FROM containers_archive_pauses WHERE workload_id=?`, id)
	return err
}
func (a *App) recoverArchivePauses(ctx context.Context, app *sdk.AppCtx) error {
	rows, err := app.AppDB().Query(`SELECT workload_id,container_name FROM containers_archive_pauses WHERE project_id=?`, app.CurrentProject())
	if err != nil {
		return err
	}
	type pause struct{ id, name string }
	var pauses []pause
	for rows.Next() {
		var p pause
		if err = rows.Scan(&p.id, &p.name); err != nil {
			rows.Close()
			return err
		}
		pauses = append(pauses, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, p := range pauses {
		c, cancel := context.WithTimeout(ctx, 15*time.Second)
		c, release, err := a.lockWorkload(c, p.id, false)
		if err == nil {
			err = a.resumeArchive(c, app, p.id, p.name)
			release()
		}
		cancel()
		if err != nil {
			app.Logger().Warn("archive resume pending", "workload_id", p.id, "error", err)
		}
	}
	return nil
}

// Called before workers/HTTP start; shell state is deliberately process-local.
func (a *App) recoverShellSessions(ctx context.Context, app *sdk.AppCtx) error {
	rows, err := app.AppDB().Query(`SELECT DISTINCT w.container_name FROM containers_workloads w JOIN containers_executions e ON e.workload_id=w.id WHERE w.host_id=0 AND w.instance_id=0 AND w.status!='destroyed' AND e.session_key!=''`)
	if err != nil {
		return err
	}
	var names []string
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		names = append(names, name)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var errs []error
	for _, name := range names {
		if ctx.Err() != nil {
			break
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			defer func() { <-sem }()
			c, cancel := context.WithTimeout(ctx, 20*time.Second)
			defer cancel()
			raw, err := docker(c, "exec", name, "sh", "-c", `for d in /tmp/.apteva-sessions/*; do [ ! -d "$d" ] || printf '%s\n' "$d"; done`)
			if dockerExecUnavailable(err) {
				return
			}
			if err == nil {
				for _, dir := range strings.Fields(raw) {
					if !strings.HasPrefix(dir, "/tmp/.apteva-sessions/") || strings.Contains(strings.TrimPrefix(dir, "/tmp/.apteva-sessions/"), "/") {
						continue
					}
					if err = containerTreeKill(c, name, dir); err != nil {
						break
					}
					if _, err = docker(c, "exec", name, "rm", "-rf", dir); err != nil {
						break
					}
				}
			}
			if err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
		}(name)
	}
	wg.Wait()
	if len(errs) > 0 {
		return fmt.Errorf("orphan session cleanup: %v", errs)
	}
	return ctx.Err()
}

// Docker may finish a daemon request after an SSH timeout. Keep checking the
// labelled container identity after failed creation, even when the first cleanup
// found nothing. Never remove retained volumes in this late-result sweep.
func (a *App) recoverRuntimeCleanup(ctx context.Context, app *sdk.AppCtx) error {
	rows, err := app.AppDB().Query(`SELECT workload_id,retry_until FROM containers_runtime_cleanup WHERE project_id=? ORDER BY retry_until LIMIT 100`, app.CurrentProject())
	if err != nil {
		return err
	}
	type item struct{ id, until string }
	var items []item
	for rows.Next() {
		var i item
		if err = rows.Scan(&i.id, &i.until); err != nil {
			rows.Close()
			return err
		}
		items = append(items, i)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, i := range items {
		c, cancel := context.WithTimeout(ctx, 45*time.Second)
		c, release, err := a.lockWorkload(c, i.id, false)
		if err == nil {
			w, wErr := requireWorkload(app.AppDB(), i.id)
			err = wErr
			if err == nil {
				var backend DockerBackend
				backend, err = a.backendForWorkload(app, w)
				if err == nil {
					err = backend.RemoveManagedContainer(c, w.ContainerName, w.ID)
					if isDockerMissingResourceError(err, "container") {
						err = nil
					}
				}
			}
			release()
		}
		cancel()
		if err != nil {
			app.Logger().Warn("interrupted creation cleanup pending", "workload_id", i.id, "error", err)
			continue
		}
		until, _ := time.Parse(time.RFC3339, i.until)
		if time.Now().After(until) {
			if _, err = app.AppDB().Exec(`DELETE FROM containers_runtime_cleanup WHERE workload_id=?`, i.id); err != nil {
				return err
			}
		}
	}
	return nil
}

func (a *App) recoverAllArchivePauses(ctx context.Context, app *sdk.AppCtx) error {
	rows, err := app.AppDB().Query(`SELECT DISTINCT project_id FROM containers_archive_pauses`)
	if err != nil {
		return err
	}
	var projects []string
	for rows.Next() {
		var project string
		if err = rows.Scan(&project); err != nil {
			rows.Close()
			return err
		}
		projects = append(projects, project)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, project := range projects {
		if err = a.recoverArchivePauses(ctx, app.WithProject(project)); err != nil {
			return err
		}
	}
	return nil
}
