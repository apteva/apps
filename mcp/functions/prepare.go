package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type RuntimeReadiness struct {
	State         string `json:"state"`
	VersionID     int64  `json:"version_id,omitempty"`
	Fingerprint   string `json:"fingerprint,omitempty"`
	Error         string `json:"error,omitempty"`
	BuildMS       int64  `json:"build_ms"`
	WorkerStartMS int64  `json:"worker_start_ms"`
	BootValidated bool   `json:"boot_validated"`
	WarmWorkers   int    `json:"warm_workers"`
}
type preparation struct {
	fn        *Function
	signature string
	result    RuntimeReadiness
	warm      bool
	done      chan struct{}
}

func preparationSignature(fn *Function) string {
	return fmt.Sprintf("%s/%v/%s", fn.InstanceKey, versionID(fn), configHash(fn))
}
func versionID(fn *Function) int64 {
	if fn.ActiveVersionID == nil {
		return 0
	}
	return *fn.ActiveVersionID
}

// Two bounded priority queues and a fixed worker count; no goroutine per request.
func (p *pool) startPreparation() {
	p.preparations = make(map[string]*preparation)
	p.initialPreparationScan = make(chan struct{})
	p.prepareHigh = make(chan *preparation, 32)
	p.prepareNormal = make(chan *preparation, 64)
	for i := 0; i < envInt("APTEVA_FUNCTIONS_PREPARE_WORKERS", 2, 1, 8); i++ {
		p.prepareWG.Add(1)
		go func() {
			defer p.prepareWG.Done()
			for {
				var job *preparation
				select {
				case <-p.life.Done():
					return
				case job = <-p.prepareHigh:
				default:
					select {
					case <-p.life.Done():
						return
					case job = <-p.prepareHigh:
					case job = <-p.prepareNormal:
					}
				}
				p.runPreparation(job)
			}
		}()
	}
	p.prepareWG.Add(1)
	go func() {
		defer p.prepareWG.Done()
		p.scanPreparation()
		close(p.initialPreparationScan)
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-p.life.Done():
				return
			case <-ticker.C:
				p.scanPreparation()
			}
		}
	}()
}
func priorityFunction(fn *Function) bool {
	if fn.FunctionURL != nil && fn.FunctionURL.Enabled {
		return true
	}
	for _, target := range strings.Split(os.Getenv("APTEVA_FUNCTIONS_PREPARE_FIRST"), ",") {
		if strings.TrimSpace(target) == fn.ProjectID+"/"+fn.Name {
			return true
		}
	}
	return false
}
func (p *pool) scanPreparation() {
	// First explicitly designated entry points, then public URLs, then all others.
	for _, target := range strings.Split(os.Getenv("APTEVA_FUNCTIONS_PREPARE_FIRST"), ",") {
		parts := strings.SplitN(strings.TrimSpace(target), "/", 2)
		if len(parts) != 2 {
			continue
		}
		fn, err := dbGetFunction(p.ctx.AppDB(), parts[0], 0, parts[1])
		if err == nil && fn != nil {
			p.enqueueBackground(fn, true)
		}
	}
	for _, public := range []bool{true, false} {
		var cursor int64
		for p.life.Err() == nil {
			rows, err := p.ctx.AppDB().QueryContext(p.life, `SELECT id,project_id FROM functions WHERE status='active' AND active_version_id IS NOT NULL AND id>? AND COALESCE(json_extract(function_url_json,'$.enabled'),0)=? ORDER BY id LIMIT 100`, cursor, public)
			if err != nil {
				return
			}
			type ref struct {
				id  int64
				pid string
			}
			var refs []ref
			for rows.Next() {
				var r ref
				if err = rows.Scan(&r.id, &r.pid); err != nil {
					break
				}
				refs = append(refs, r)
			}
			if rows.Err() != nil {
				err = rows.Err()
			}
			rows.Close()
			if err != nil {
				return
			}
			if len(refs) == 0 {
				break
			}
			for _, r := range refs {
				cursor = r.id
				fn, err := dbGetFunction(p.ctx.AppDB(), r.pid, r.id, "")
				if err == nil && fn != nil {
					p.enqueueBackground(fn, public)
				}
			}
		}
	}
}
func (p *pool) enqueueBackground(fn *Function, high bool) {
	for p.life.Err() == nil {
		_, err := p.requestPreparation(fn, true, false, high)
		if !errors.Is(err, errFunctionBusy) {
			return
		}
		select {
		case <-p.life.Done():
			return
		case <-time.After(25 * time.Millisecond):
		}
	}
}
func (p *pool) requestPreparation(fn *Function, warm, retry, high bool) (*preparation, error) {
	if fn.Status != "active" || fn.ActiveVersionID == nil {
		return nil, errors.New("function has no active deployment")
	}
	p.prepareMu.Lock()
	defer p.prepareMu.Unlock()
	if p.life.Err() != nil {
		return nil, errors.New("runtime stopped")
	}
	signature := preparationSignature(fn)
	if entry := p.preparations[fn.InstanceKey]; entry != nil && entry.signature == signature {
		switch entry.result.State {
		case "preparing":
			entry.warm = entry.warm || warm
			return entry, nil
		case "ready", "prepared":
			v := p.cachedVersion(versionID(fn))
			needWarm := false
			if warm && retry {
				p.mu.Lock()
				fp := p.byFn[fn.ID]
				needWarm = fp == nil || len(fp.idle) == 0
				p.mu.Unlock()
			}
			if !needWarm && v != nil && artifactAvailable(versionDir(p.buildBase, v), v) && (!warm || entry.result.BootValidated) {
				return entry, nil
			}
		case "failed":
			if !retry {
				return entry, nil
			}
		}
	}
	copyFn := *fn
	entry := &preparation{fn: &copyFn, signature: signature, warm: warm, done: make(chan struct{}), result: RuntimeReadiness{State: "preparing", VersionID: versionID(fn)}}
	queue := p.prepareNormal
	if high {
		queue = p.prepareHigh
	}
	select {
	case queue <- entry:
		p.preparations[fn.InstanceKey] = entry
		return entry, nil
	default:
		return nil, errFunctionBusy
	}
}
func (p *pool) runPreparation(job *preparation) {
	ctx, cancel := context.WithTimeout(context.WithValue(p.life, poolContextKey{}, p), buildTimeout)
	defer cancel()
	result := RuntimeReadiness{State: "failed", VersionID: versionID(job.fn)}
	defer func() {
		result.Error = redactSecrets(result.Error, job.fn.Env)
		p.prepareMu.Lock()
		job.result = result
		followWarm := job.warm && result.State == "prepared" && p.preparations[job.fn.InstanceKey] == job
		close(job.done)
		p.prepareMu.Unlock()
		if followWarm {
			_, _ = p.requestPreparation(job.fn, true, true, priorityFunction(job.fn))
		}
	}()
	fn, err := dbGetFunction(p.ctx.AppDB(), job.fn.ProjectID, job.fn.ID, "")
	if err != nil || fn == nil || preparationSignature(fn) != job.signature || fn.Status != "active" {
		result.Error = "Deployment changed during preparation; prepare the current version"
		return
	}
	v, err := dbGetVersion(p.ctx.AppDB(), fn.ProjectID, versionID(fn))
	if err != nil || v == nil || v.ArtifactKey != fn.InstanceKey || v.BuildStatus != "ready" {
		result.Error = "Active deployment unavailable; deploy a valid version"
		return
	}
	result.Fingerprint = artifactHash(v)
	dir := versionDir(p.buildBase, v)
	lease, err := p.leaseVersion(dir)
	if err != nil {
		result.Error = err.Error()
		return
	}
	defer lease()
	spec, err := resolveRuntime(fn.Runtime)
	if err != nil {
		result.Error = err.Error()
		return
	}
	started := time.Now()
	if !artifactAvailable(dir, v) {
		p.artifacts.Delete(dir)
		src, sourceErr := resolveVersionSourceContext(ctx, p.ctx.WithProject(fn.ProjectID), v)
		if sourceErr != nil {
			result.Error = sourceErr.Error()
			return
		}
		dir, err = ensureBuiltContext(ctx, p.buildBase, v, spec, src)
		if err != nil {
			result.Error = redactSecrets(err.Error(), fn.Env)
			return
		}
		result.BuildMS = time.Since(started).Milliseconds()
	}
	// Serialize publication with deploy/rollback; re-check immutable identity.
	unlock, err := lockBuild(ctx, fmt.Sprintf("activation-%d", fn.ID))
	if err != nil {
		result.Error = err.Error()
		return
	}
	defer unlock()
	current, err := dbGetFunction(p.ctx.AppDB(), fn.ProjectID, fn.ID, "")
	if err != nil || current == nil || preparationSignature(current) != job.signature || current.Status != "active" {
		result.Error = "Deployment changed during preparation; prepare the current version"
		return
	}
	var candidate *worker
	p.prepareMu.Lock()
	warm := job.warm
	p.prepareMu.Unlock()
	if warm {
		started = time.Now()
		candidate, err = p.start(ctx, fn, v, spec, dir)
		result.WorkerStartMS = time.Since(started).Milliseconds()
		if err != nil {
			result.Error = redactSecrets(err.Error(), fn.Env)
			return
		}
	}
	if err = dbUpdateVersionBuild(p.ctx.AppDB(), fn.ProjectID, v.ID, "ready", v.BuildLog, dir, fn.InstanceKey); err != nil {
		if candidate != nil {
			p.discard(candidate)
		}
		result.Error = err.Error()
		return
	}
	v.BuildDir = dir
	p.cacheVersion(v)
	p.cacheFunction(current)
	p.artifacts.Store(dir, true)
	if candidate != nil {
		p.refreshFunction(current)
		p.put(current, p.poolFor(fn.ID), candidate)
		result.BootValidated = true
	}
	result.State = "prepared"
	if result.BootValidated {
		result.State = "ready"
	}
}
func (p *pool) markPrepared(fn *Function, v *FunctionVersion) {
	if fn == nil || v == nil {
		return
	}
	done := make(chan struct{})
	close(done)
	p.prepareMu.Lock()
	p.preparations[fn.InstanceKey] = &preparation{fn: fn, signature: preparationSignature(fn), done: done, result: RuntimeReadiness{State: "ready", VersionID: v.ID, Fingerprint: artifactHash(v), BootValidated: true}}
	p.prepareMu.Unlock()
}
func (p *pool) runtimeReadiness(fn *Function) *RuntimeReadiness {
	result := &RuntimeReadiness{State: "preparing", VersionID: versionID(fn)}
	if fn.Status != "active" || fn.ActiveVersionID == nil {
		result.State = "inactive"
		return result
	}
	p.prepareMu.Lock()
	entry := p.preparations[fn.InstanceKey]
	if entry != nil && entry.signature == preparationSignature(fn) {
		*result = entry.result
	}
	p.prepareMu.Unlock()
	if result.State == "ready" || result.State == "prepared" {
		v := p.cachedVersion(versionID(fn))
		if v == nil || !artifactAvailable(versionDir(p.buildBase, v), v) {
			result.State = "preparing"
			result.BootValidated = false
		}
	}
	p.mu.Lock()
	if fp := p.byFn[fn.ID]; fp != nil && fp.signature == configHash(fn) {
		result.WarmWorkers = len(fp.idle)
	}
	p.mu.Unlock()
	return result
}
func withReadiness(fn *Function) *Function {
	if fn == nil {
		return nil
	}
	if fn.RuntimeReadiness != nil {
		return fn
	}
	copyFn := *fn
	if p := currentPool(); p != nil {
		copyFn.RuntimeReadiness = p.runtimeReadiness(fn)
	}
	return &copyFn
}
func (p *pool) awaitPreparation(ctx context.Context, fn *Function) error {
	job, err := p.requestPreparation(fn, false, false, priorityFunction(fn))
	if err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-p.life.Done():
		return p.life.Err()
	case <-job.done:
	}
	p.prepareMu.Lock()
	state := job.result
	p.prepareMu.Unlock()
	if state.State != "ready" && state.State != "prepared" {
		return fmt.Errorf("runtime preparation failed: %s; retry with functions_prepare", state.Error)
	}
	return nil
}
func (a *App) toolPrepare(parent context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	fn, err := dbGetFunction(ctx.AppDB(), pid, int64Arg(args, "id"), strArg(args, "name"))
	if err != nil {
		return nil, err
	}
	if fn == nil {
		return nil, errors.New("function not found")
	}
	p := currentPool()
	if p == nil {
		return nil, errors.New("runtime unavailable")
	}
	warm := args["warm"] != false
	job, err := p.requestPreparation(fn, warm, true, true)
	if err != nil {
		return nil, err
	}
	if args["wait"] == true {
		select {
		case <-parent.Done():
			return nil, parent.Err()
		case <-p.life.Done():
			return nil, p.life.Err()
		case <-job.done:
		}
		if warm && p.runtimeReadiness(fn).State == "prepared" {
			return a.toolPrepare(parent, ctx, args)
		}
	}
	return map[string]any{"runtime_readiness": p.runtimeReadiness(fn)}, nil
}
func (a *App) handleHTTPPrepare(w http.ResponseWriter, r *http.Request, id int64) {
	if r.Method != "POST" {
		httpErr(w, 405, "POST required")
		return
	}
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	args := map[string]any{"id": id, "_project_id": pid, "warm": r.URL.Query().Get("warm") != "false", "wait": r.URL.Query().Get("wait") == "true"}
	out, err := a.toolPrepare(r.Context(), globalCtx, args)
	if err != nil {
		httpErr(w, 503, err.Error())
		return
	}
	httpJSON(w, out)
}

func (p *pool) markBootValidated(fn *Function, cold time.Duration) {
	p.prepareMu.Lock()
	defer p.prepareMu.Unlock()
	if entry := p.preparations[fn.InstanceKey]; entry != nil && entry.signature == preparationSignature(fn) && entry.result.State == "prepared" {
		entry.result.State = "ready"
		entry.result.BootValidated = true
		entry.result.WorkerStartMS = cold.Milliseconds()
	}
}
