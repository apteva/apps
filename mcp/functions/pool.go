package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
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
	}{fn.Env, fn.MaxMemoryMB, fn.Access})
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
	p := &pool{ctx: ctx, stageDir: stage, buildBase: base, versionRefs: map[string]int{}, collecting: map[string]bool{}, deleted: map[string]bool{}, byFn: map[int64]*fnPool{}, all: map[*worker]*fnPool{}, globalSem: make(chan struct{}, envInt("APTEVA_FUNCTIONS_MAX_WORKERS", 32, 1, 1024)), globalQueue: make(chan struct{}, envInt("APTEVA_FUNCTIONS_MAX_QUEUE", 256, 1, 10000)), buildSem: make(chan struct{}, envInt("APTEVA_FUNCTIONS_MAX_BUILDS", 2, 1, 32)), buildQueue: make(chan struct{}, envInt("APTEVA_FUNCTIONS_MAX_BUILD_QUEUE", 16, 1, 256)), downstream: make(chan struct{}, envInt("APTEVA_FUNCTIONS_MAX_DOWNSTREAM_TOTAL", 64, 1, 1024)), stop: make(chan struct{}), wake: make(chan struct{}, 1), life: life, cancel: cancel}
	_, err = ctx.AppDB().Exec(`UPDATE function_versions SET build_status='failed',build_log='Build interrupted by restart' WHERE build_status IN ('pending','building')`)
	if err != nil {
		cancel()
		removeTree(stage)
		return nil, err
	}
	_, _ = ctx.AppDB().Exec(`UPDATE function_invocations SET status='error',error='Invocation interrupted by restart' WHERE status='running'`)
	if err := p.recoverLegacySnapshots(); err != nil {
		cancel()
		removeTree(stage)
		return nil, err
	}
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
		fp = &fnPool{sem: make(chan struct{}, workersPerFunction), idle: make(chan *worker, workersPerFunction), queue: make(chan struct{}, envInt("APTEVA_FUNCTIONS_MAX_QUEUE_PER_FUNCTION", 64, 1, 10000))}
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
		p.liveMB -= w.memoryMB
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
	for {
		p.mu.Lock()
		closed := p.closed || p.deleted[fn.InstanceKey]
		p.mu.Unlock()
		if closed {
			return nil, errors.New("worker pool closed")
		}
		select {
		case p.globalSem <- struct{}{}:
			p.mu.Lock()
			fits := p.liveMB+memory <= envInt("APTEVA_FUNCTIONS_TOTAL_MEMORY_MB", 4096, 16, 1048576)
			if fits {
				p.liveMB += memory
			}
			p.mu.Unlock()
			if !fits {
				<-p.globalSem
				if p.evictIdle() {
					continue
				}
				return nil, errFunctionBusy
			}
			w, err := startWorkerContext(parent, spec, dir, fn, v.ID)
			if err != nil {
				p.mu.Lock()
				p.liveMB -= memory
				p.mu.Unlock()
				<-p.globalSem
				p.signal()
				return nil, err
			}
			w.owner = p
			w.memoryMB = memory
			w.signature = configHash(fn)
			w.identity = fn.InstanceKey
			p.mu.Lock()
			fp := p.poolForLocked(fn.ID)
			p.all[w] = fp
			closed = p.closed || fp.closed || p.deleted[fn.InstanceKey]
			p.mu.Unlock()
			if closed {
				p.discard(w)
				return nil, errors.New("function deleted or pool closed")
			}
			return w, nil
		default:
		}
		if p.evictIdle() {
			continue
		}
		select {
		case <-parent.Done():
			return nil, parent.Err()
		case <-p.stop:
			return nil, errors.New("worker pool closed")
		case <-p.wake:
		}
	}
}
func (p *pool) put(fn *Function, fp *fnPool, w *worker) {
	p.mu.Lock()
	keep := !p.closed && !fp.closed && fp.identity == fn.InstanceKey && fp.signature == w.signature
	if current := p.cachedFunction(fn.ProjectID, fn.ID, ""); current != nil {
		keep = keep && current.ActiveVersionID != nil && *current.ActiveVersionID == w.versionID && current.Status == "active"
	}
	if keep {
		select {
		case fp.idle <- w:
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
	fp := p.poolFor(fn.ID)
	select {
	case p.globalQueue <- struct{}{}:
		defer func() { <-p.globalQueue }()
	default:
		return nil, errFunctionBusy
	}
	select {
	case fp.queue <- struct{}{}:
		defer func() { <-fp.queue }()
	default:
		return nil, errFunctionBusy
	}
	select {
	case fp.sem <- struct{}{}:
		defer func() { <-fp.sem }()
	case <-parent.Done():
		return nil, parent.Err()
	case <-p.stop:
		return nil, errors.New("worker pool closed")
	}
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
	t.queue = time.Since(queueStart)
	var w *worker
	for w == nil {
		select {
		case candidate := <-fp.idle:
			if candidate.alive() && !candidate.stale(v.ID) && candidate.signature == configHash(fn) {
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
		coldStart := time.Now()
		w, err = p.start(parent, fn, v, spec, dir)
		t.cold = time.Since(coldStart)
		if err != nil {
			return nil, fmt.Errorf("cold start: %w", err)
		}
	}
	executionStart := time.Now()
	res, err := w.call(ctx, parent, event, timeout, stream)
	t.execution = time.Since(executionStart)
	if err == nil && w.alive() {
		p.put(fn, fp, w)
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
	ticker := time.NewTicker(reaperEvery)
	defer ticker.Stop()
	for {
		select {
		case <-p.stop:
			return
		case <-ticker.C:
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
				if !w.alive() || time.Since(w.idleSince()) > idleWorkerTTL {
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
	if _, err := p.ctx.AppDB().Exec(`DELETE FROM function_invocations WHERE started_at < ?`, cutoff); err != nil {
		p.ctx.Logger().Warn("prune function invocations", "err", err)
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
