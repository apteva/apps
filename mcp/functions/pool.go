package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const (
	// workersPerFunction caps concurrent invocations — and therefore
	// live worker processes — for a single function.
	workersPerFunction = 8
	// idleWorkerTTL: a warm worker unused for this long is reaped to
	// give its memory back.
	idleWorkerTTL = 5 * time.Minute
	reaperEvery   = 30 * time.Second
)

// globalPool is the process-wide worker pool, created in OnMount.
var globalPool *pool

// pool owns every warm worker. One fnPool per function id; within it
// a counting semaphore caps concurrency and an idle freelist hands
// warm workers back out. Workers are keyed by version id — a deploy
// makes the previous version's idle workers stale, and the next
// acquire drains them.
type pool struct {
	ctx *sdk.AppCtx

	stageDir  string // <tmp>/apteva-functions-XXXX — the build-base fallback root
	buildBase string // root for version artifact dirs

	mu          sync.Mutex
	byFn        map[int64]*fnPool
	globalSem   chan struct{}
	globalQueue chan struct{}
	buildSem    chan struct{}
	versions    sync.Map // version id -> *FunctionVersion

	stop          chan struct{}
	lastRetention time.Time
}

// fnPool is the per-function concurrency gate + warm-worker freelist.
type fnPool struct {
	sem   chan struct{} // cap = workersPerFunction
	idle  chan *worker  // cap = workersPerFunction
	queue chan struct{}
}

var errFunctionBusy = errors.New("function capacity exhausted; retry later")

// newPool picks the build-artifact root and starts the idle reaper.
// Harnesses aren't staged here — ensureBuilt writes the right one
// into each version's build dir at build time.
func newPool(ctx *sdk.AppCtx) (*pool, error) {
	stageDir, err := os.MkdirTemp("", "apteva-functions-")
	if err != nil {
		return nil, err
	}

	// Build artifacts: persistent under APTEVA_DATA_DIR when set (so
	// built dependency trees / compiled workers survive a restart),
	// otherwise under the per-boot stage dir — ensureBuilt rebuilds
	// lazily either way.
	buildBase := filepath.Join(stageDir, "build")
	if d := strings.TrimSpace(os.Getenv("APTEVA_DATA_DIR")); d != "" {
		buildBase = filepath.Join(d, "functions-build")
	}
	if err := os.MkdirAll(buildBase, 0o700); err != nil {
		return nil, err
	}

	p := &pool{
		ctx:         ctx,
		stageDir:    stageDir,
		buildBase:   buildBase,
		byFn:        map[int64]*fnPool{},
		globalSem:   make(chan struct{}, envInt("APTEVA_FUNCTIONS_MAX_WORKERS", 32, 1, 1024)),
		globalQueue: make(chan struct{}, envInt("APTEVA_FUNCTIONS_MAX_QUEUE", 256, 1, 10000)),
		buildSem:    make(chan struct{}, envInt("APTEVA_FUNCTIONS_MAX_BUILDS", 2, 1, 32)),
		stop:        make(chan struct{}),
	}
	go p.reapLoop()
	return p, nil
}

// invoke runs one event against ver through the warm pool: reuse a
// current idle worker if there is one, otherwise cold-start one
// against the version's already-built artifact dir. ctx is threaded
// to the worker so cross-app context.call frames can be serviced via
// its PlatformAPI. The worker goes back to the freelist afterwards
// unless it died or its version is no longer active.
func (p *pool) invoke(ctx *sdk.AppCtx, parent context.Context, fn *Function, ver *FunctionVersion, spec runtimeSpec, buildDir string, event any, timeout time.Duration, stream invocationStream) (*invokeResult, error) {
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

	// Acquire a concurrency slot (blocks at the cap).
	select {
	case fp.sem <- struct{}{}:
	case <-parent.Done():
		return nil, parent.Err()
	}
	defer func() { <-fp.sem }()
	select {
	case p.globalSem <- struct{}{}:
	case <-parent.Done():
		return nil, parent.Err()
	}
	defer func() { <-p.globalSem }()

	// Reuse a warm worker on the current version; drain stale/dead.
	var w *worker
	for {
		select {
		case cand := <-fp.idle:
			if cand.alive() && !cand.stale(ver.ID) {
				w = cand
			} else {
				cand.shutdown()
				continue
			}
		default:
			// freelist empty — fall through to cold start
		}
		break
	}

	if w == nil {
		started, err := startWorker(spec, buildDir, fn, ver.ID)
		if err != nil {
			return nil, fmt.Errorf("cold start: %w", err)
		}
		w = started
		select {
		case <-parent.Done():
			w.shutdown()
			return &invokeResult{Status: "timeout", ExitCode: -1, DurationMS: timeout.Milliseconds(), Error: "deadline exceeded"}, nil
		default:
		}
	}

	res, err := w.call(ctx, parent, event, timeout, stream)

	// Return the worker to the freelist if it's still healthy and on
	// the active version; otherwise let it go.
	if err == nil && w.alive() && !w.stale(ver.ID) {
		select {
		case fp.idle <- w:
		default:
			w.shutdown() // freelist full — shouldn't happen under the sem cap
		}
	} else {
		w.shutdown()
	}
	return res, err
}

// poolFor returns (creating on first use) the per-function gate.
func (p *pool) poolFor(fnID int64) *fnPool {
	p.mu.Lock()
	defer p.mu.Unlock()
	fp, ok := p.byFn[fnID]
	if !ok {
		fp = &fnPool{
			sem:   make(chan struct{}, workersPerFunction),
			idle:  make(chan *worker, workersPerFunction),
			queue: make(chan struct{}, envInt("APTEVA_FUNCTIONS_MAX_QUEUE_PER_FUNCTION", 64, 1, 10000)),
		}
		p.byFn[fnID] = fp
	}
	return fp
}

func (p *pool) acquireBuild(ctx context.Context) error {
	select {
	case p.buildSem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *pool) releaseBuild() { <-p.buildSem }

func (p *pool) cachedVersion(id int64) *FunctionVersion {
	if raw, ok := p.versions.Load(id); ok {
		copy := *(raw.(*FunctionVersion))
		return &copy
	}
	return nil
}

func (p *pool) cacheVersion(v *FunctionVersion) {
	if v == nil {
		return
	}
	copy := *v
	p.versions.Store(v.ID, &copy)
}

// activateVersion eagerly drops idle workers from older versions instead of
// retaining them until the next invocation or the five-minute reaper pass.
func (p *pool) activateVersion(fnID, versionID int64) {
	p.mu.Lock()
	fp := p.byFn[fnID]
	p.mu.Unlock()
	if fp == nil {
		return
	}
	n := len(fp.idle)
	for i := 0; i < n; i++ {
		select {
		case w := <-fp.idle:
			if w.stale(versionID) {
				w.shutdown()
			} else {
				select {
				case fp.idle <- w:
				default:
					w.shutdown()
				}
			}
		default:
			return
		}
	}
}

// removeFunction detaches its pool immediately, kills idle workers, and waits
// for in-flight calls before deleting persistent artifacts.
func (p *pool) removeFunction(fnID int64) {
	p.versions.Range(func(key, value any) bool {
		if v, ok := value.(*FunctionVersion); ok && v.FunctionID == fnID {
			p.versions.Delete(key)
		}
		return true
	})
	sourceCache.clear()
	p.mu.Lock()
	fp := p.byFn[fnID]
	delete(p.byFn, fnID)
	p.mu.Unlock()
	if fp == nil {
		_ = removeTree(filepath.Join(p.buildBase, fmt.Sprintf("fn-%d", fnID)))
		return
	}
	for {
		select {
		case w := <-fp.idle:
			w.shutdown()
		default:
			go func() {
				for i := 0; i < cap(fp.sem); i++ {
					fp.sem <- struct{}{}
				}
				_ = removeTree(filepath.Join(p.buildBase, fmt.Sprintf("fn-%d", fnID)))
				for i := 0; i < cap(fp.sem); i++ {
					<-fp.sem
				}
			}()
			return
		}
	}
}

func (p *pool) reapLoop() {
	t := time.NewTicker(reaperEvery)
	defer t.Stop()
	for {
		select {
		case <-p.stop:
			return
		case <-t.C:
			p.reapIdle()
			p.retainInvocations()
		}
	}
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

// reapIdle culls workers idle past idleWorkerTTL. It opportunistically
// drains each freelist and re-pushes the keepers — a concurrent
// invoke may grab a worker mid-pass, which is harmless.
func (p *pool) reapIdle() {
	p.mu.Lock()
	pools := make([]*fnPool, 0, len(p.byFn))
	for _, fp := range p.byFn {
		pools = append(pools, fp)
	}
	p.mu.Unlock()

	cutoff := time.Now().Add(-idleWorkerTTL)
	for _, fp := range pools {
		n := len(fp.idle)
		for i := 0; i < n; i++ {
			select {
			case w := <-fp.idle:
				if !w.alive() || w.idleSince().Before(cutoff) {
					w.shutdown()
				} else {
					select {
					case fp.idle <- w:
					default:
						w.shutdown()
					}
				}
			default:
				i = n // freelist drained
			}
		}
	}
}

// shutdown stops the reaper and kills every warm worker. Called from
// OnUnmount.
func (p *pool) shutdown() {
	close(p.stop)
	p.mu.Lock()
	pools := make([]*fnPool, 0, len(p.byFn))
	for _, fp := range p.byFn {
		pools = append(pools, fp)
	}
	p.byFn = map[int64]*fnPool{}
	p.mu.Unlock()

	for _, fp := range pools {
		draining := true
		for draining {
			select {
			case w := <-fp.idle:
				w.shutdown()
			default:
				draining = false
			}
		}
	}
	_ = removeTree(p.stageDir)
}
