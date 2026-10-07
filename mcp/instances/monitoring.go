package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
	"github.com/apteva/apps/mcp/instances/internal/history"
	"github.com/apteva/apps/mcp/instances/internal/monitor"
)

type monitorWorker struct {
	cancel context.CancelFunc
	done   chan struct{}
}
type monitoringManager struct {
	ctx     *sdk.AppCtx
	store   *history.Store
	cancel  context.CancelFunc
	work    context.Context
	mu      sync.Mutex
	workers map[int64]*monitorWorker
	done    chan struct{}
	started bool
	slots   chan struct{}
}

var monitoringManagersMu sync.Mutex
var monitoringManagers = map[*sql.DB]*monitoringManager{}

func monitoringFor(ctx *sdk.AppCtx) (*monitoringManager, error) {
	if ctx == nil || ctx.AppDB() == nil {
		return nil, errors.New("app context unavailable")
	}
	monitoringManagersMu.Lock()
	defer monitoringManagersMu.Unlock()
	if m := monitoringManagers[ctx.AppDB()]; m != nil {
		return m, nil
	}
	if ctx.DataDir() == "" || !filepath.IsAbs(ctx.DataDir()) {
		return nil, errors.New("absolute APTEVA_DATA_DIR or DB_PATH required for monitoring")
	}
	store, err := history.OpenStore(filepath.Join(ctx.DataDir(), "monitoring.db"), history.DefaultBudget)
	if err != nil {
		return nil, err
	}
	work, cancel := context.WithCancel(context.Background())
	m := &monitoringManager{ctx: ctx, store: store, cancel: cancel, work: work, workers: map[int64]*monitorWorker{}, done: make(chan struct{}), slots: make(chan struct{}, 8)}
	monitoringManagers[ctx.AppDB()] = m
	return m, nil
}
func startMonitoring(ctx *sdk.AppCtx) error {
	m, err := monitoringFor(ctx)
	if err != nil {
		return err
	}
	m.mu.Lock()
	if m.started {
		m.mu.Unlock()
		return nil
	}
	m.started = true
	m.mu.Unlock()
	go func() {
		select {
		case <-ctx.Done():
			m.cancel()
		case <-m.done:
		}
	}()
	go func() {
		defer close(m.done)
		m.reconcile()
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-m.work.Done():
				return
			case <-ticker.C:
				m.reconcile()
			}
		}
	}()
	return nil
}
func stopMonitoring(ctx *sdk.AppCtx) {
	monitoringManagersMu.Lock()
	m := monitoringManagers[ctx.AppDB()]
	delete(monitoringManagers, ctx.AppDB())
	monitoringManagersMu.Unlock()
	if m == nil {
		return
	}
	m.cancel()
	m.mu.Lock()
	started := m.started
	m.mu.Unlock()
	if started {
		<-m.done
	}
	m.mu.Lock()
	workers := []*monitorWorker{}
	for _, w := range m.workers {
		w.cancel()
		workers = append(workers, w)
	}
	m.mu.Unlock()
	for _, w := range workers {
		<-w.done
	}
	monitoringSSHPool.closeAll()
	m.store.Close()
}
func (m *monitoringManager) reconcile() {
	if m.work.Err() != nil {
		return
	}
	instances, err := dbListInstances(m.ctx.AppDB(), "", "")
	if err != nil {
		m.ctx.Logger().Warn("monitoring inventory unavailable", "error", err)
		return
	}
	wanted := map[int64]bool{}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, inst := range instances {
		wanted[inst.ID] = true
		if m.workers[inst.ID] != nil {
			continue
		}
		m.store.EnsureHost(inst.ID)
		work, cancel := context.WithCancel(m.work)
		w := &monitorWorker{cancel: cancel, done: make(chan struct{})}
		m.workers[inst.ID] = w
		go func(id int64) { defer close(w.done); m.monitorHost(work, id) }(inst.ID)
	}
	for id, w := range m.workers {
		if !wanted[id] {
			w.cancel()
			delete(m.workers, id)
			monitoringSSHPool.evict(id)
			go func(host int64, worker *monitorWorker) { <-worker.done; m.store.RemoveHost(host) }(id, w)
		}
	}
}
func (m *monitoringManager) monitorHost(work context.Context, id int64) {
	var engine *monitor.Engine
	var localCancel context.CancelFunc
	var localDone chan struct{}
	stopLocal := func() {
		if localCancel != nil {
			localCancel()
			<-localDone
			localCancel = nil
			engine = nil
		}
	}
	defer stopLocal()
	installed := false
	stopped := false
	for {
		if work.Err() != nil {
			return
		}
		nextPoll := 2 * time.Second
		inst, err := dbGetInstance(m.ctx.AppDB(), id)
		if err != nil {
			return
		}
		inst.workContext = work
		status, err := m.store.Status(id)
		if err != nil {
			return
		}
		if !status.Enabled {
			stopLocal()
			if !inst.IsLocal() && !stopped {
				err = stopRemoteCollector(inst)
				if err != nil {
					m.store.SetState(id, "disabled", "", err.Error())
				} else {
					stopped = true
					m.store.SetState(id, "disabled", "", "")
					monitoringSSHPool.evict(id)
				}
			}
			installed = false
			if sleepContext(work, 5*time.Second) != nil {
				return
			}
			continue
		}
		stopped = false
		if inst.Status != "ready" {
			stopLocal()
			installed = false
			m.store.SetState(id, "waiting", "", "Instance is not ready")
			if sleepContext(work, 5*time.Second) != nil {
				return
			}
			continue
		}
		if inst.IsLocal() {
			if engine == nil {
				m.store.CloseOpenIncidents(id, time.Now().UnixMilli())
				engine = monitor.NewEngine()
				localWork, cancel := context.WithCancel(work)
				localCancel = cancel
				localDone = make(chan struct{})
				localEngine := engine
				localStopped := localDone
				go func() { defer close(localStopped); monitor.Run(localWork, localEngine) }()
			}
			batch := engine.Export(status.LastPoint)
			if batch.Latest != nil {
				err = m.store.Ingest(id, batch)
			}
		} else {
			select {
			case m.slots <- struct{}{}:
			case <-work.Done():
				return
			}
			if !installed {
				m.store.SetState(id, "installing", status.Version, "")
				err = ensureRemoteCollector(work, inst)
				if err == nil {
					installed = true
				}
			}
			if err == nil {
				var batch monitor.Batch
				batch, err = fetchRemoteBatch(inst, status.LastPoint)
				if err == nil && work.Err() == nil {
					if batch.Latest != nil {
						err = m.store.Ingest(id, batch)
					}
					if batch.More {
						nextPoll = 250 * time.Millisecond
					}
				}
			}
			<-m.slots
		}
		if err != nil {
			installed = false
			m.store.SetState(id, "error", status.Version, truncate(err.Error(), 700))
			if sleepContext(work, 30*time.Second) != nil {
				return
			}
		} else if sleepContext(work, nextPoll) != nil {
			return
		}
	}
}
func liveMonitoring(ctx *sdk.AppCtx, id int64) (map[string]any, error) {
	if _, err := dbGetInstance(ctx.AppDB(), id); err != nil {
		return nil, err
	}
	m, err := monitoringFor(ctx)
	if err != nil {
		return nil, err
	}
	status, err := m.store.Status(id)
	if err != nil {
		return nil, err
	}
	metrics, err := m.store.Latest(id)
	if err == sql.ErrNoRows {
		err = nil
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{"instance_id": id, "metrics": metrics, "monitoring": status}, nil
}
func configureMonitoring(ctx *sdk.AppCtx, id int64, enabled bool) (any, error) {
	if _, err := dbGetInstance(ctx.AppDB(), id); err != nil {
		return nil, err
	}
	m, err := monitoringFor(ctx)
	if err != nil {
		return nil, err
	}
	if err := m.store.SetEnabled(id, enabled); err != nil {
		return nil, err
	}
	return liveMonitoring(ctx, id)
}
func monitoringHistory(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64Arg(args, "id")
	if _, err := dbGetInstance(ctx.AppDB(), id); err != nil {
		return nil, err
	}
	now := time.Now()
	to := now.UnixMilli()
	from := now.Add(-time.Hour).UnixMilli()
	var err error
	if raw := strArg(args, "from"); raw != "" {
		var t time.Time
		t, err = time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			return nil, fmt.Errorf("from must be RFC3339: %w", err)
		}
		from = t.UnixMilli()
	}
	if raw := strArg(args, "to"); raw != "" {
		var t time.Time
		t, err = time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			return nil, fmt.Errorf("to must be RFC3339: %w", err)
		}
		to = t.UnixMilli()
	}
	max := int(int64Arg(args, "max_points"))
	if max == 0 {
		max = 600
	}
	m, err := monitoringFor(ctx)
	if err != nil {
		return nil, err
	}
	return m.store.History(id, from, to, strArg(args, "resolution"), max)
}
func monitoringIncidents(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64Arg(args, "id")
	if _, err := dbGetInstance(ctx.AppDB(), id); err != nil {
		return nil, err
	}
	m, err := monitoringFor(ctx)
	if err != nil {
		return nil, err
	}
	if event := strArg(args, "incident_id"); event != "" {
		ev, err := m.store.Incident(id, event)
		return map[string]any{"incident": ev}, err
	}
	limit := int(int64Arg(args, "limit"))
	if limit == 0 {
		limit = 50
	}
	events, err := m.store.ListIncidents(id, limit)
	return map[string]any{"incidents": events}, err
}
