package main

import (
	"context"
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

	mu   sync.Mutex
	byFn map[int64]*fnPool

	stop chan struct{}
}

// fnPool is the per-function concurrency gate + warm-worker freelist.
type fnPool struct {
	sem  chan struct{} // cap = workersPerFunction
	idle chan *worker  // cap = workersPerFunction
}

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
		ctx:       ctx,
		stageDir:  stageDir,
		buildBase: buildBase,
		byFn:      map[int64]*fnPool{},
		stop:      make(chan struct{}),
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
func (p *pool) invoke(ctx *sdk.AppCtx, parent context.Context, fn *Function, ver *FunctionVersion, spec runtimeSpec, buildDir string, event any, timeout time.Duration) (*invokeResult, error) {
	fp := p.poolFor(fn.ID)

	// Acquire a concurrency slot (blocks at the cap).
	select {
	case fp.sem <- struct{}{}:
	case <-parent.Done():
		return nil, parent.Err()
	}
	defer func() { <-fp.sem }()

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
	}

	res, err := w.call(ctx, parent, event, timeout)

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
			sem:  make(chan struct{}, workersPerFunction),
			idle: make(chan *worker, workersPerFunction),
		}
		p.byFn[fnID] = fp
	}
	return fp
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
		}
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
	_ = os.RemoveAll(p.stageDir)
}
