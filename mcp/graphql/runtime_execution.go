package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
	"github.com/google/uuid"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/formatter"
)

type resolverMetric struct {
	Calls   int     `json:"calls"`
	TotalMS float64 `json:"total_ms"`
	MaxMS   float64 `json:"max_ms"`
	Errors  int     `json:"errors"`
}
type runtimeMetrics struct {
	QueueMS         float64                   `json:"queue_ms"`
	Coalesced       bool                      `json:"coalesced"`
	ExecutionID     string                    `json:"execution_id,omitempty"`
	Waiters         int                       `json:"waiters"`
	LoaderHits      int                       `json:"loader_hits"`
	BackendCalls    int                       `json:"backend_calls"`
	BackendReads    int                       `json:"backend_reads"`
	BatchSizes      []int                     `json:"batch_sizes,omitempty"`
	SnapshotMS      float64                   `json:"snapshot_ms"`
	Consistency     string                    `json:"consistency"`
	ResolverTimings map[string]resolverMetric `json:"resolver_timings,omitempty"`
	Sources         []any                     `json:"sources,omitempty"`
}
type admissionLane struct {
	running, queued int
	changed         chan struct{}
}
type sharedExecution struct {
	done          chan struct{}
	cancel        context.CancelFunc
	waiters, peak int
	id            string
	result        executeResult
	err           error
}
type executionRuntime struct {
	mu      sync.Mutex
	lanes   map[string]*admissionLane
	flights map[string]*sharedExecution
	stopped bool
}

func (r *executionRuntime) stop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stopped = true
	for _, f := range r.flights {
		f.cancel()
	}
	for _, l := range r.lanes {
		close(l.changed)
		l.changed = make(chan struct{})
	}
}
func runtimeError(code, message string) error { return &graphqlError{Code: code, Message: message} }

// Lanes are removed when idle, so unique client documents cannot grow an
// unbounded semaphore registry. The API lane also bounds aggregate admission.
func (r *executionRuntime) admit(ctx context.Context, scope, operation string, limits releaseLimits) (func(), error) {
	r.mu.Lock()
	if r.lanes == nil {
		r.lanes = map[string]*admissionLane{}
	}
	keys := []string{"api:" + scope, "op:" + scope + ":" + operation}
	lanes := make([]*admissionLane, 2)
	for i, k := range keys {
		lanes[i] = r.lanes[k]
		if lanes[i] == nil {
			lanes[i] = &admissionLane{changed: make(chan struct{})}
			r.lanes[k] = lanes[i]
		}
	}
	cleanup := func() {
		for i, l := range lanes {
			if l.running == 0 && l.queued == 0 && r.lanes[keys[i]] == l {
				delete(r.lanes, keys[i])
			}
		}
	}
	capacity := func() bool {
		return lanes[0].running < limits.MaxConcurrentRequests && lanes[1].running < limits.MaxConcurrentOperations
	}
	queued := false
	timer := time.NewTimer(time.Duration(limits.MaxQueueMS) * time.Millisecond)
	defer timer.Stop()
	for {
		if ctx.Err() != nil || r.stopped {
			if queued {
				for _, l := range lanes {
					l.queued--
				}
			}
			cleanup()
			r.mu.Unlock()
			return nil, runtimeError("execution_cancelled", "GraphQL execution cancelled before admission")
		}
		if capacity() {
			for _, l := range lanes {
				if queued {
					l.queued--
				}
				l.running++
			}
			r.mu.Unlock()
			var once sync.Once
			return func() {
				once.Do(func() {
					r.mu.Lock()
					defer r.mu.Unlock()
					for _, l := range lanes {
						l.running--
						close(l.changed)
						l.changed = make(chan struct{})
					}
					cleanup()
				})
			}, nil
		}
		if !queued {
			if lanes[0].queued >= limits.MaxQueuedOperations || lanes[1].queued >= limits.MaxQueuedOperations {
				cleanup()
				r.mu.Unlock()
				return nil, runtimeError("queue_full", "GraphQL execution queue is full")
			}
			for _, l := range lanes {
				l.queued++
			}
			queued = true
		}
		a, b := lanes[0].changed, lanes[1].changed
		r.mu.Unlock()
		timedOut := false
		select {
		case <-a:
		case <-b:
		case <-ctx.Done():
		case <-timer.C:
			timedOut = true
		}
		r.mu.Lock()
		if timedOut {
			for _, l := range lanes {
				l.queued--
			}
			cleanup()
			r.mu.Unlock()
			return nil, runtimeError("queue_timeout", "GraphQL execution queue wait exceeded its limit")
		}
	}
}

// Only in-flight work is retained. Completion removes the flight before waking
// callers, including on partial GraphQL errors; no result cache exists here.
func (r *executionRuntime) share(ctx context.Context, key string, limits releaseLimits, run func(context.Context) (executeResult, error)) (executeResult, error) {
	if ctx.Err() != nil {
		return executeResult{}, runtimeError("execution_cancelled", "GraphQL caller cancelled")
	}
	r.mu.Lock()
	if r.stopped {
		r.mu.Unlock()
		return executeResult{}, runtimeError("execution_cancelled", "GraphQL runtime stopped")
	}
	if r.flights == nil {
		r.flights = map[string]*sharedExecution{}
	}
	f, joined := r.flights[key]
	if joined && f.waiters >= limits.MaxCoalescedWaiters {
		r.mu.Unlock()
		return executeResult{}, runtimeError("coalescing_limit", "GraphQL shared execution waiter limit exceeded")
	}
	if !joined {
		if len(r.flights) >= 4096 {
			r.mu.Unlock()
			return executeResult{}, runtimeError("queue_full", "GraphQL shared execution capacity exceeded")
		}
		base := context.WithoutCancel(ctx)
		base = context.WithValue(base, requestTelemetryKey{}, (*requestTelemetry)(nil))
		deadline := time.Now().Add(time.Duration(limits.MaxQueueMS+limits.MaxExecutionMS) * time.Millisecond)
		if identity := securityIdentity(ctx); identity != nil && identity.Expires.Before(deadline) {
			deadline = identity.Expires
		}
		sharedCtx, cancel := context.WithDeadline(base, deadline)
		f = &sharedExecution{done: make(chan struct{}), cancel: cancel, id: uuid.NewString()}
		if identity := securityIdentity(ctx); identity != nil {
			copy := *identity
			copy.RequestID = f.id
			sharedCtx = context.WithValue(sharedCtx, identityKey{}, &copy)
		}
		r.flights[key] = f
		go func() {
			var out executeResult
			var err error
			func() {
				defer func() {
					if recover() != nil {
						err = internal("shared GraphQL execution failed")
					}
				}()
				out, err = run(sharedCtx)
			}()
			r.mu.Lock()
			f.result, f.err = out, err
			if r.flights[key] == f {
				delete(r.flights, key)
			}
			f.cancel()
			close(f.done)
			r.mu.Unlock()
		}()
	}
	f.waiters++
	f.peak = max(f.peak, f.waiters)
	r.mu.Unlock()
	release := func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		f.waiters--
		if f.waiters == 0 {
			if r.flights[key] == f {
				delete(r.flights, key)
			}
			f.cancel()
		}
	}
	defer release()
	select {
	case <-ctx.Done():
		return executeResult{Runtime: runtimeMetrics{Coalesced: joined, ExecutionID: f.id}}, runtimeError("execution_cancelled", "GraphQL caller cancelled")
	case <-f.done:
		out := cloneExecuteResult(f.result)
		r.mu.Lock()
		out.Runtime.Waiters = f.peak
		r.mu.Unlock()
		out.Runtime.Coalesced = joined
		out.Runtime.ExecutionID = f.id
		return out, f.err
	}
}
func cloneExecuteResult(in executeResult) executeResult {
	// Maps and slices are caller-owned, including errors, data and extensions.
	raw, _ := json.Marshal(in)
	var out executeResult
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	_ = d.Decode(&out)
	return out
}
func runtimeDigest(value any) string {
	raw, _ := json.Marshal(value)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func canonicalDocument(doc *ast.QueryDocument) string {
	var out bytes.Buffer
	formatter.NewFormatter(&out).FormatQueryDocument(doc)
	return out.String()
}
func coalescingKey(ctx context.Context, project, api, environment string, release *apiRelease, document, operation string, variables map[string]any) (string, error) {
	var identity any
	if i := securityIdentity(ctx); i != nil {
		identity = []any{i.Issuer, i.Subject, i.Project, i.API, i.Tenant, i.Claims, i.Permissions, i.AuthorizationVersion, i.Expires}
	} else if c := sdk.CallerFrom(ctx); c != nil {
		identity = c
	} else {
		identity = []any{"platform", project}
	}
	raw, err := json.Marshal([]any{project, api, normalizeEnvironment(environment), release.ID, release.Checksum, release.Security, release.Limits, document, operation, variables, identity})
	if err != nil {
		return "", invalid("cannot form authorized execution identity")
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
func pureReadOperation(p *preparedOperation, b *executionBindings, schema *ast.Schema) bool {
	if p.op.Operation != ast.Query {
		return false
	}
	fields := map[string]bool{}
	for _, key := range p.permissionFields {
		fields[key] = true
		parts := strings.SplitN(key, ".", 2)
		if len(parts) == 2 {
			for _, def := range schema.PossibleTypes[parts[0]] {
				fields[def.Name+"."+parts[1]] = true
			}
		}
	}
	for key := range fields {
		r, ok := b.resolvers[key]
		if !ok {
			continue
		}
		s, ok := b.sources[r.SourceID]
		if !ok {
			return false
		}
		switch s.Kind {
		case "tables":
			if !tablesReadOperation(r.Operation) {
				return false
			}
		case "module":
			module, _, ok := configuredResolverModule(s.Config, r.Config, b)
			if !ok || !module.Deterministic {
				return false
			}
		case "upstream", "http", "function":
			if s.Config["read_only"] != true || r.Config["read_only"] == false {
				return false
			}
		default:
			return false // unknown/module/database purity is not assumed
		}
	}
	return true
}
func tablesReadOperation(op string) bool {
	switch op {
	case "find", "list", "search", "get", "count", "aggregate", aggregatePipelineOperation:
		return true
	}
	return false
}

// Shared failures are copied and their field locations are mapped to this
// caller's document. Whitespace canonicalization must not borrow locations.
func remapErrorLocations(errors []map[string]any, selection ast.SelectionSet) {
	var find func(ast.SelectionSet, []string) *ast.Field
	find = func(set ast.SelectionSet, path []string) *ast.Field {
		if len(path) == 0 {
			return nil
		}
		for _, item := range set {
			switch f := item.(type) {
			case *ast.Field:
				alias := f.Alias
				if alias == "" {
					alias = f.Name
				}
				if alias == path[0] {
					if len(path) == 1 {
						return f
					}
					if child := find(f.SelectionSet, path[1:]); child != nil {
						return child
					}
					return f
				}
			case *ast.InlineFragment:
				if child := find(f.SelectionSet, path); child != nil {
					return child
				}
			case *ast.FragmentSpread:
				if f.Definition != nil {
					if child := find(f.Definition.SelectionSet, path); child != nil {
						return child
					}
				}
			}
		}
		return nil
	}
	for _, e := range errors {
		path, ok := e["path"].([]any)
		if !ok {
			continue
		}
		names := []string{}
		for _, part := range path {
			if s, ok := part.(string); ok {
				names = append(names, s)
			}
		}
		f := find(selection, names)
		if f != nil && f.Position != nil {
			e["locations"] = []any{map[string]any{"line": f.Position.Line, "column": f.Position.Column}}
		}
	}
}
func (m *runtimeMetrics) recordResolver(field string, elapsed time.Duration, failed bool) {
	if m.ResolverTimings == nil {
		m.ResolverTimings = map[string]resolverMetric{}
	}
	if len(field) > 512 {
		field = "[long field name]"
	}
	if _, known := m.ResolverTimings[field]; !known && len(m.ResolverTimings) >= 256 {
		field = "[other fields]"
	}
	entry := m.ResolverTimings[field]
	entry.Calls++
	entry.TotalMS += milliseconds(elapsed)
	entry.MaxMS = max(entry.MaxMS, milliseconds(elapsed))
	if failed {
		entry.Errors++
	}
	m.ResolverTimings[field] = entry
}
