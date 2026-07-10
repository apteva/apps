package main

import (
	"fmt"
	"strconv"
	"strings"
	"sync"

	sdk "github.com/apteva/app-sdk"
)

func (a *App) lockSimRun(simID string) func() {
	a.runMu.Lock()
	if a.runLocks == nil {
		a.runLocks = make(map[string]*sync.Mutex)
	}
	lock := a.runLocks[simID]
	if lock == nil {
		lock = &sync.Mutex{}
		a.runLocks[simID] = lock
	}
	a.runMu.Unlock()
	lock.Lock()
	return lock.Unlock
}

func maxConcurrentSims(ctx *sdk.AppCtx) int {
	raw := ""
	if ctx != nil {
		raw = strings.TrimSpace(ctx.Config().Get("max_concurrent_sims"))
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 2
	}
	if n > 32 {
		return 32
	}
	return n
}

// enforceSimCapacity runs while App.bootMu is held, so concurrent boot
// requests cannot all observe the same free slot. Booting rows count toward
// the cap; stale booted rows are demoted as they are discovered.
func (a *App) enforceSimCapacity(ctx *sdk.AppCtx, requestedID string) error {
	rows, err := dbListLiveSims(ctx.AppDB())
	if err != nil {
		return err
	}
	live := 0
	for _, row := range rows {
		if row.ID == requestedID {
			continue
		}
		if a.sup.probeAlive(row) {
			live++
			continue
		}
		_ = dbUpdateSim(ctx.AppDB(), row.ID, map[string]any{
			"status": "shutdown", "pid": 0, "error": "sim was no longer alive",
		})
	}
	limit := maxConcurrentSims(ctx)
	if live >= limit {
		return fmt.Errorf("sim capacity reached: %d live/booting, max_concurrent_sims=%d", live, limit)
	}
	return nil
}
