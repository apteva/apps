package main

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type resourceKey struct {
	db   *sql.DB
	kind string
	id   int64
}

var resourceMutex sync.Mutex
var resourceBusy = map[resourceKey]bool{}

func lockResource(db *sql.DB, kind string, id int64) (func(), error) {
	key := resourceKey{db, kind, id}
	resourceMutex.Lock()
	defer resourceMutex.Unlock()
	if resourceBusy[key] {
		return nil, errors.New("resource operation already in progress")
	}
	resourceBusy[key] = true
	return func() { resourceMutex.Lock(); delete(resourceBusy, key); resourceMutex.Unlock() }, nil
}

type instanceWorker struct {
	cancel context.CancelFunc
	done   chan struct{}
	ctx    context.Context
}

var workerMutex sync.Mutex
var stoppedWorkers = map[*sql.DB]bool{}
var instanceWorkers = map[resourceKey]*instanceWorker{}

func startInstanceWorker(ctx *sdk.AppCtx, id int64, run func(context.Context)) {
	key := resourceKey{ctx.AppDB(), "instance", id}
	workerMutex.Lock()
	if stoppedWorkers[ctx.AppDB()] || instanceWorkers[key] != nil {
		workerMutex.Unlock()
		return
	}
	work, cancel := context.WithCancel(context.Background())
	w := &instanceWorker{cancel: cancel, done: make(chan struct{}), ctx: work}
	instanceWorkers[key] = w
	workerMutex.Unlock()
	go func() {
		select {
		case <-ctx.Done():
			cancel()
		case <-w.done:
		}
	}()
	go func() {
		defer func() {
			cancel()
			workerMutex.Lock()
			if instanceWorkers[key] == w {
				delete(instanceWorkers, key)
			}
			close(w.done)
			workerMutex.Unlock()
		}()
		run(work)
	}()
}

func cancelInstanceWorker(ctx *sdk.AppCtx, id int64) {
	workerMutex.Lock()
	defer workerMutex.Unlock()
	if w := instanceWorkers[resourceKey{ctx.AppDB(), "instance", id}]; w != nil {
		w.cancel()
	}
}

func instanceWorkerContext(ctx *sdk.AppCtx, id int64) context.Context {
	workerMutex.Lock()
	defer workerMutex.Unlock()
	if w := instanceWorkers[resourceKey{ctx.AppDB(), "instance", id}]; w != nil {
		return w.ctx
	}
	return context.Background()
}

func stopInstanceWorkers(ctx *sdk.AppCtx) {
	workerMutex.Lock()
	stoppedWorkers[ctx.AppDB()] = true
	pending := []*instanceWorker{}
	for key, w := range instanceWorkers {
		if key.db == ctx.AppDB() {
			w.cancel()
			pending = append(pending, w)
		}
	}
	workerMutex.Unlock()
	globalSSHPool.closeAll()
	deadline := time.After(12 * time.Second)
	for _, w := range pending {
		select {
		case <-w.done:
		case <-deadline:
			return
		}
	}
}

func waitInstanceReady(ctx context.Context, app *sdk.AppCtx, id int64, timeout time.Duration) (*Instance, error) {
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	wait, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		inst, err := dbGetInstance(app.AppDB(), id)
		if err != nil {
			return nil, err
		}
		if inst.Status == "ready" {
			return inst, nil
		}
		if inst.Status != "provisioning" && inst.Status != "pending" {
			return nil, ErrOperationSuperseded
		}
		select {
		case <-wait.Done():
			return nil, wait.Err()
		case <-ticker.C:
		}
	}
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	if ctx == nil {
		ctx = context.Background()
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

var activeCreates = map[resourceKey]bool{}

func trackInstanceCreation(ctx *sdk.AppCtx, id int64) func() {
	key := resourceKey{ctx.AppDB(), "create", id}
	workerMutex.Lock()
	activeCreates[key] = true
	workerMutex.Unlock()
	return func() {
		workerMutex.Lock()
		delete(activeCreates, key)
		workerMutex.Unlock()
		inst, err := dbGetInstance(ctx.AppDB(), id)
		if err == nil && inst.CreatePending && inst.ProviderID != "" {
			finishInstanceCreation(ctx, id)
		}
	}
}
func instanceCreateActive(ctx *sdk.AppCtx, id int64) bool {
	workerMutex.Lock()
	defer workerMutex.Unlock()
	return activeCreates[resourceKey{ctx.AppDB(), "create", id}]
}
func sleepApp(ctx *sdk.AppCtx, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return context.Canceled
	case <-timer.C:
		return nil
	}
}

func reconcileRollbacks(ctx *sdk.AppCtx) {
	instances, err := dbListInstances(ctx.AppDB(), "", "rolling_back")
	if err != nil {
		return
	}
	for _, inst := range instances {
		if instanceCreateActive(ctx, inst.ID) {
			continue
		}
		unlock, e := lockResource(ctx.AppDB(), "instance", inst.ID)
		if e != nil {
			continue
		}
		e = prepareVolumesForInstanceDestroy(ctx, inst.ID, true)
		if e == nil {
			e = destroyProviderInstance(ctx, inst)
		}
		fields := map[string]any{"lifecycle_stage": "Rollback", "create_pending": false}
		if e != nil {
			fields["cleanup_error"] = e.Error()
		} else {
			fields["provider_id"] = ""
			fields["public_ipv4"] = ""
			fields["public_ipv6"] = ""
			fields["cleanup_error"] = ""
		}
		_, _, _ = transitionInstanceAndEmit(ctx, inst.ID, []string{"rolling_back"}, "error", fields)
		unlock()
	}
}

func sleepOperation(ctx *sdk.AppCtx, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return context.Canceled
	case <-instanceWorkerContext(ctx, -1).Done():
		return context.Canceled
	case <-timer.C:
		return nil
	}
}
