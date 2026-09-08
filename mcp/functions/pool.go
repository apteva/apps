package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"github.com/apteva/apps/mcp/functions/internal/admission"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	workersPerFunction = 8
	idleWorkerTTL      = 5 * time.Minute
	reaperEvery        = 30 * time.Second
)

var poolRef atomic.Pointer[pool]

func currentPool() *pool { return poolRef.Load() }

type poolContextKey struct{}

func poolFrom(ctx context.Context) *pool {
	if p, ok := ctx.Value(poolContextKey{}).(*pool); ok {
		return p
	}
	return currentPool()
}

var errFunctionBusy = errors.New("function capacity exhausted; retry later")

type pool struct {
	auto                 *admission.Observer
	autoDownstream       *admission.Observer
	protocolBudget       atomic.Pointer[protocolBudget]
	protocolWaiters      atomic.Int64
	startupSteps         []startupStep
	recoveredWork        int64
	legacySnapshotsReady chan struct{}
	owner                *runtimeOwner
	draining             bool
	activeWork           int
	workWG               sync.WaitGroup
	maintenanceWG        sync.WaitGroup
	// Admission measurements are cached for at most 100 ms under mu.
	memorySamples     map[*worker]admissionSample
	hostSample        admissionHostSample
	memoryReader      func(*worker) (*int64, string, int64)
	hostMemoryReader  func() int64
	protocolClasses   map[string]int64
	queueClasses      map[string]int
	capacity          CapacitySettings
	capacityWarning   string
	classReservations map[string][2]int
	downstreamClasses map[string]int
	liveCalls         map[int64]*callTrace
	rejections        map[string]int64

	initialPreparationScan                                   chan struct{}
	artifactBuilds                                           atomic.Uint64
	prepareMu                                                sync.Mutex
	prepareWG                                                sync.WaitGroup
	preparations                                             map[string]*preparation
	prepareHigh, prepareNormal                               chan *preparation
	versionRefs                                              map[string]int
	collecting                                               map[string]bool
	goCacheMu                                                sync.Mutex
	goCache                                                  string
	lastArtifactRetention                                    time.Time
	ctx                                                      *sdk.AppCtx
	stageDir, buildBase                                      string
	mu                                                       sync.Mutex
	byFn                                                     map[int64]*fnPool
	all                                                      map[*worker]*fnPool
	globalSem, globalQueue, buildSem, buildQueue, downstream chan struct{}
	liveMB                                                   int
	versions                                                 sync.Map
	artifacts                                                sync.Map
	functions                                                sync.Map
	stop                                                     chan struct{}
	wake                                                     chan struct{}
	cancel                                                   context.CancelFunc
	life                                                     context.Context
	closed                                                   bool
	deleted                                                  map[string]bool
	lastRetention                                            time.Time
}
type fnPool struct {
	sem                 chan struct{}
	idle                chan *worker
	queue               chan struct{}
	closed              bool
	signature, identity string
}

func configHash(fn *Function) string {
	b, _ := json.Marshal(struct {
		Env    map[string]string
		Memory int
		Access *FunctionAccess
		Limits RuntimePolicy
	}{fn.Env, fn.MaxMemoryMB, fn.Access, fn.Limits})
	return hashSource(b)
}
func newPool(ctx *sdk.AppCtx) (*pool, error) {
	stage, err := os.MkdirTemp("", "apteva-functions-")
	if err != nil {
		return nil, err
	}
	base := filepath.Join(stage, "build")
	if d := strings.TrimSpace(os.Getenv("APTEVA_DATA_DIR")); d != "" {
		base = filepath.Join(d, "functions-build")
	}
	if err = os.MkdirAll(base, 0700); err != nil {
		os.RemoveAll(stage)
		return nil, err
	}
	life, cancel := context.WithCancel(context.Background())
	p := &pool{auto: admission.NewObserver(), autoDownstream: admission.NewObserver(), ctx: ctx, stageDir: stage, buildBase: base, versionRefs: map[string]int{}, collecting: map[string]bool{}, deleted: map[string]bool{}, byFn: map[int64]*fnPool{}, all: map[*worker]*fnPool{}, globalSem: make(chan struct{}, 1024), globalQueue: make(chan struct{}, 10000), buildSem: make(chan struct{}, envInt("APTEVA_FUNCTIONS_MAX_BUILDS", 2, 1, 32)), buildQueue: make(chan struct{}, envInt("APTEVA_FUNCTIONS_MAX_BUILD_QUEUE", 16, 1, 256)), downstream: make(chan struct{}, 1024), stop: make(chan struct{}), wake: make(chan struct{}, 1), life: life, cancel: cancel}
	steps := []struct {
		name string
		run  func() error
	}{
		{"runtime-owner", func() error { p.owner, err = openRuntimeOwner(ctx.StartupContext(), ctx.AppDB(), base); return err }},
		{"recover-active-work", func() error {
			n, e := p.owner.recover(ctx.StartupContext(), ctx.AppDB())
			p.recoveredWork = n
			ctx.Logger().Info("recovered abandoned work", "rows", n)
			return e
		}},
		{"legacy-recovery-checkpoint", func() error { return p.initLegacyRecovery(ctx.StartupContext()) }},
		{"capacity", p.initCapacity},
	}
	for i, step := range steps {
		started := time.Now()
		ctx.ReportStartupProgress(step.name, int64(i), int64(len(steps)))
		err = step.run()
		p.startupSteps = append(p.startupSteps, startupStep{Name: step.name, DurationMS: time.Since(started).Milliseconds()})
		ctx.Logger().Info("startup step", "step", step.name, "duration_ms", time.Since(started).Milliseconds(), "err", err)
		if err != nil {
			cancel()
			p.owner.close()
			removeTree(stage)
			return nil, err
		}
	}
	p.maintenanceWG.Add(2)
	go p.legacyRecoveryLoop()
	p.startPreparation()
	go p.reapLoop()
	return p, nil
}
func (p *pool) poolFor(id int64) *fnPool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.poolForLocked(id)
}
func (p *pool) poolForLocked(id int64) *fnPool {
	fp := p.byFn[id]
	if fp == nil {
		fp = &fnPool{sem: make(chan struct{}, 1024), idle: make(chan *worker, 1024), queue: make(chan struct{}, 10000)}
		p.byFn[id] = fp
	}
	return fp
}
func (p *pool) signal() {
	select {
	case p.wake <- struct{}{}:
	default:
	}
}
func (p *pool) discard(w *worker) {
	p.mu.Lock()
	_, exists := p.all[w]
	if exists {
		delete(p.all, w)
		delete(p.memorySamples, w)
		p.hostSample.at = time.Time{}
		p.liveMB -= w.memoryMB
		u := p.classReservations[w.capacityClass]
		p.classReservations[w.capacityClass] = [2]int{u[0] - w.memoryMB, u[1] - 1}
	}
	p.mu.Unlock()
	w.shutdown()
	if exists {
		<-p.globalSem
	}
	p.signal()
}
func (p *pool) evictIdle() bool {
	p.mu.Lock()
	var victim *worker
	for _, fp := range p.byFn {
		select {
		case victim = <-fp.idle:
		default:
		}
		if victim != nil {
			break
		}
	}
	p.mu.Unlock()
	if victim != nil {
		p.discard(victim)
		return true
	}
	return false
}
func (p *pool) start(parent context.Context, fn *Function, v *FunctionVersion, spec runtimeSpec, dir string) (*worker, error) {
	memory := clampInt(fn.MaxMemoryMB, defaultMemoryMB, 16, maxMemoryMB)
	waitStart := time.Now()
	class, err := p.reserveWorker(parent, fn)
	if traceFrom(parent) != nil {
		timingsFrom(parent).queue += time.Since(waitStart)
	}
	if err != nil {
		return nil, err
	}
	processStart := time.Now()
	w, err := startWorkerContext(parent, spec, dir, fn, v.ID)
	if traceFrom(parent) != nil {
		timingsFrom(parent).cold += time.Since(processStart)
	}
	if err != nil {
		p.releaseReservation(class, memory)
		return nil, err
	}
	w.owner = p
	w.memoryMB = memory
	w.signature = configHash(fn)
	w.identity = fn.InstanceKey
	w.capacityClass = class
	w.projectID = fn.ProjectID
	w.capacityState.Store("starting")
	p.mu.Lock()
	fp := p.poolForLocked(fn.ID)
	p.all[w] = fp
	p.hostSample.at = time.Time{}
	closed := p.closed || fp.closed || p.deleted[fn.InstanceKey]
	p.mu.Unlock()
	if closed {
		p.discard(w)
		return nil, errors.New("function deleted or pool closed")
	}
	return w, nil
}

func (p *pool) put(fn *Function, fp *fnPool, w *worker) {
	p.mu.Lock()
	keep := (policy(fn).MaxIdle == nil || len(fp.idle) < *policy(fn).MaxIdle) && !p.closed && !fp.closed && fp.identity == fn.InstanceKey && fp.signature == w.signature
	if current := p.cachedFunction(fn.ProjectID, fn.ID, ""); current != nil {
		keep = keep && current.ActiveVersionID != nil && *current.ActiveVersionID == w.versionID && current.Status == "active"
	}
	if keep {
		select {
		case fp.idle <- w:
			w.capacityState.Store("idle")
			w.invocationID.Store(0)
		default:
			keep = false
		}
	}
	p.mu.Unlock()
	if !keep {
		p.discard(w)
	}
	p.signal()
}
func (p *pool) invoke(ctx *sdk.AppCtx, parent context.Context, fn *Function, v *FunctionVersion, spec runtimeSpec, dir string, event any, timeout time.Duration, stream invocationStream) (*invokeResult, error) {
	t := timingsFrom(parent)
	queueStart := time.Now()
	parent = context.WithValue(parent, queueDeadlineKey{}, queueStart.Add(time.Duration(policy(fn).QueueMS)*time.Millisecond))
	fp := p.poolFor(fn.ID)
	autoPermit, autoErr := p.acquireAutomatic(parent, fn)
	t.queue = time.Since(queueStart)
	if autoErr != nil {
		return nil, autoErr
	}
	autoResult := admission.Result{CPUSeconds: -1, Failed: true}
	defer func() {
		if autoPermit != nil {
			autoPermit.Finish(autoResult)
		}
	}()
	releaseAdmission, releaseQueue, err := p.admitInvocation(parent, fn, fp)
	t.queue = time.Since(queueStart)
	if err != nil {
		return nil, err
	}
	defer releaseAdmission()

	p.mu.Lock()
	if fp.identity == "" {
		fp.identity = fn.InstanceKey
		fp.signature = configHash(fn)
	}
	valid := !fp.closed && !p.closed && !p.deleted[fn.InstanceKey] && fp.identity == fn.InstanceKey && fp.signature == configHash(fn)
	p.mu.Unlock()
	if !valid {
		return nil, errors.New("function deleted or configuration changed; retry")
	}
	var w *worker
	for w == nil {
		select {
		case candidate := <-fp.idle:
			if candidate.alive() && !candidate.stale(v.ID) && candidate.signature == configHash(fn) && p.reclassify(candidate, requestClass(parent, fn)) {
				w = candidate
			} else {
				p.discard(candidate)
				continue
			}
		default:
		}
		break
	}
	if w == nil {
		var err error
		w, err = p.start(parent, fn, v, spec, dir)
		if err != nil {
			return nil, fmt.Errorf("cold start: %w", err)
		}
	}
	releaseQueue()
	w.capacityState.Store("running")
	trace := traceFrom(parent)
	if trace != nil {
		trace.state("running")
		w.invocationID.Store(trace.InvocationID)
		trace.sample(w)
	}
	sampleDone := make(chan struct{})
	sampleStopped := make(chan struct{})
	go func() {
		defer close(sampleStopped)
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-sampleDone:
				return
			case <-ticker.C:
				trace.sample(w)
			}
		}
	}()
	cpuStart := workerCPUSeconds(w)
	executionStart := time.Now()
	res, err := w.call(ctx, parent, event, timeout, stream)
	t.execution = time.Since(executionStart)
	autoResult.Duration = t.execution
	cpuEnd := workerCPUSeconds(w)
	if cpuStart >= 0 && cpuEnd >= cpuStart {
		autoResult.CPUSeconds = cpuEnd - cpuStart
	}
	autoResult.Canceled = parent.Err() != nil
	autoResult.Failed = err != nil || res == nil || res.Status != "ok"
	if trace != nil {
		trace.mu.Lock()
		trace.WorkerCPUSeconds = nil
		if autoResult.CPUSeconds >= 0 {
			n := autoResult.CPUSeconds
			trace.WorkerCPUSeconds = &n
		}
		trace.mu.Unlock()
	}
	close(sampleDone)
	<-sampleStopped
	trace.sample(w)
	_, _, oom := workerMemory(w)
	if oom > 0 || w.oomKills.Load() > 0 {
		if res == nil {
			res = &invokeResult{Status: "error", ExitCode: -1}
		}
		res.ErrorCode = "worker_oom"
		res.Error = "Worker exceeded its cgroup memory limit"
	}

	if err == nil && w.alive() {
		p.put(fn, fp, w)
		p.markBootValidated(fn, t.cold)
	} else {
		p.discard(w)
	}
	return res, err
}
func (p *pool) acquireBuild(ctx context.Context) error {
	select {
	case p.buildQueue <- struct{}{}:
	default:
		return errFunctionBusy
	}
	select {
	case p.buildSem <- struct{}{}:
		return nil
	case <-ctx.Done():
		<-p.buildQueue
		return ctx.Err()
	case <-p.stop:
		<-p.buildQueue
		return errors.New("pool closed")
	}
}
func (p *pool) releaseBuild() { <-p.buildSem; <-p.buildQueue }
func (p *pool) cachedVersion(id int64) *FunctionVersion {
	if v, ok := p.versions.Load(id); ok {
		c := *v.(*FunctionVersion)
		return &c
	}
	return nil
}
func (p *pool) cacheVersion(v *FunctionVersion) {
	if v != nil {
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.deleted[v.ArtifactKey] {
			return
		}
		c := *v
		p.versions.Store(v.ID, &c)
	}
}
func functionCacheKey(pid string, id int64, name string) string {
	if id != 0 {
		return fmt.Sprintf("%s/id/%d", pid, id)
	}
	return pid + "/name/" + name
}
func (p *pool) cachedFunction(pid string, id int64, name string) *Function {
	if v, ok := p.functions.Load(functionCacheKey(pid, id, name)); ok {
		c := *v.(*Function)
		return &c
	}
	return nil
}
func (p *pool) cacheFunction(fn *Function) {
	if fn == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.deleted[fn.InstanceKey] {
		return
	}
	c := *fn
	p.functions.Store(functionCacheKey(fn.ProjectID, fn.ID, ""), &c)
	p.functions.Store(functionCacheKey(fn.ProjectID, 0, fn.Name), &c)
}
func executionFunction(ctx *sdk.AppCtx, pid string, id int64, name string) (*Function, error) {
	p := currentPool()
	if p != nil {
		if f := p.cachedFunction(pid, id, name); f != nil {
			return f, nil
		}
	}
	f, err := dbGetFunction(ctx.AppDB(), pid, id, name)
	if err == nil && p != nil {
		p.cacheFunction(f)
	}
	return f, err
}
func (p *pool) refreshFunction(fn *Function) {
	if fn == nil {
		return
	}
	p.cacheFunction(fn)
	p.mu.Lock()
	if p.closed || p.deleted[fn.InstanceKey] {
		p.mu.Unlock()
		return
	}
	fp := p.poolForLocked(fn.ID)
	fp.identity = fn.InstanceKey
	fp.signature = configHash(fn)
	p.mu.Unlock()
	p.activateVersion(fn.ID, func() int64 {
		if fn.Status == "active" && fn.ActiveVersionID != nil {
			return *fn.ActiveVersionID
		}
		return -1
	}())
}
func (p *pool) activateVersion(fnID, versionID int64) {
	p.mu.Lock()
	fp := p.byFn[fnID]
	var stale []*worker
	if fp != nil {
		n := len(fp.idle)
		for i := 0; i < n; i++ {
			select {
			case w := <-fp.idle:
				if w.stale(versionID) || w.signature != fp.signature {
					stale = append(stale, w)
				} else {
					fp.idle <- w
				}
			default:
			}
		}
	}
	p.mu.Unlock()
	for _, w := range stale {
		p.discard(w)
	}
}
func (p *pool) removeFunction(fn *Function) {
	p.prepareMu.Lock()
	delete(p.preparations, fn.InstanceKey)
	p.prepareMu.Unlock()
	p.functions.Delete(functionCacheKey(fn.ProjectID, fn.ID, ""))
	p.functions.Delete(functionCacheKey(fn.ProjectID, 0, fn.Name))
	p.versions.Range(func(k, v any) bool {
		if v.(*FunctionVersion).ArtifactKey == fn.InstanceKey {
			p.versions.Delete(k)
		}
		return true
	})
	p.mu.Lock()
	p.deleted[fn.InstanceKey] = true
	p.artifacts.Range(func(key, value any) bool {
		if strings.HasPrefix(key.(string), filepath.Join(p.buildBase, fn.InstanceKey)+string(os.PathSeparator)) {
			p.artifacts.Delete(key)
		}
		return true
	})
	fp := p.byFn[fn.ID]
	if fp != nil && fp.identity == fn.InstanceKey {
		fp.closed = true
		delete(p.byFn, fn.ID)
	}
	var workers []*worker
	for w, owner := range p.all {
		if owner == fp {
			workers = append(workers, w)
		}
	}
	p.mu.Unlock()
	// Closing the socket interrupts active calls without waiting on their call mutex.
	for _, w := range workers {
		w.abort()
	}
	for _, w := range workers {
		p.discard(w)
	}
	if fn.InstanceKey != "" {
		_ = removeTree(filepath.Join(p.buildBase, fn.InstanceKey))
	}
	p.signal()
}
func (p *pool) reapLoop() {
	defer p.maintenanceWG.Done()
	ticker := time.NewTicker(reaperEvery)
	defer ticker.Stop()
	for {
		select {
		case <-p.stop:
			return
		case <-ticker.C:
			p.recoverAbandonedWork()
			p.reapIdle()
			p.retainInvocations()
			p.retainArtifacts()
		}
	}
}
func (p *pool) reapIdle() {
	p.mu.Lock()
	var victims []*worker
	for _, fp := range p.byFn {
		n := len(fp.idle)
		for i := 0; i < n; i++ {
			select {
			case w := <-fp.idle:
				if !w.alive() || time.Since(w.idleSince()) > p.workerIdleTTL(w) {
					victims = append(victims, w)
				} else {
					fp.idle <- w
				}
			default:
			}
		}
	}
	p.mu.Unlock()
	for _, w := range victims {
		p.discard(w)
	}
}
func (p *pool) shutdown() {
	if p.auto != nil {
		p.auto.Close()
	}
	if p.autoDownstream != nil {
		p.autoDownstream.Close()
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	close(p.stop)
	p.cancel()
	var workers []*worker
	for w := range p.all {
		workers = append(workers, w)
	}
	for _, fp := range p.byFn {
		fp.closed = true
	}
	p.mu.Unlock()
	for _, w := range workers {
		w.abort()
	}
	for _, w := range workers {
		p.discard(w)
	}
	p.prepareWG.Wait()
	p.workWG.Wait()
	p.maintenanceWG.Wait()
	p.owner.close()
	_ = removeTree(p.stageDir)
}
func (p *pool) retainInvocations() {
	days := envInt("APTEVA_FUNCTIONS_INVOCATION_RETENTION_DAYS", 30, 1, 3650)
	p.mu.Lock()
	if time.Since(p.lastRetention) < time.Hour {
		p.mu.Unlock()
		return
	}
	p.lastRetention = time.Now()
	p.mu.Unlock()
	cutoff := time.Now().UTC().Add(-time.Duration(days) * 24 * time.Hour).Format(time.RFC3339Nano)
	// One bounded batch per reaper tick; allow the next tick to continue a backlog.
	result, err := p.ctx.AppDB().ExecContext(p.life, `DELETE FROM function_invocations WHERE id IN (SELECT id FROM function_invocations WHERE started_at < ? ORDER BY started_at LIMIT 128)`, cutoff)
	if err != nil {
		p.ctx.Logger().Warn("prune function invocations", "err", err)
		return
	}
	n, _ := result.RowsAffected()
	if n == 128 {
		p.mu.Lock()
		p.lastRetention = time.Time{}
		p.mu.Unlock()
	}

}

func (p *pool) leaseVersion(dir string) (func(), error) {
	p.mu.Lock()
	if p.collecting[dir] {
		p.mu.Unlock()
		return nil, errors.New("version retired; retry with current function")
	}
	p.versionRefs[dir]++
	p.mu.Unlock()
	return func() {
		p.mu.Lock()
		p.versionRefs[dir]--
		if p.versionRefs[dir] == 0 {
			delete(p.versionRefs, dir)
		}
		p.mu.Unlock()
	}, nil
}

func (p *pool) workerIdleTTL(w *worker) time.Duration {
	if fn := p.cachedFunction(w.projectID, w.fnID, ""); fn != nil {
		return time.Duration(policy(fn).IdleMS) * time.Millisecond
	}
	return idleWorkerTTL
}
