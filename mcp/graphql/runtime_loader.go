package main

import (
	"context"
	"encoding/json"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"strconv"
	"time"
)

func (l *resolverLoader) consistency() string {
	if s, ok := l.ctx.Value(standardRequestKey{}).(*standardRequest); ok {
		return runtimeLimits(s.limits).ReadConsistency
	}
	return "none"
}
func (l *resolverLoader) backendMetrics(size, reads int) {
	if s, ok := l.ctx.Value(standardRequestKey{}).(*standardRequest); ok {
		s.timingMu.Lock()
		defer s.timingMu.Unlock()
		s.metrics.BackendCalls++
		s.metrics.BackendReads += reads
		if len(s.metrics.BatchSizes) < 256 {
			s.metrics.BatchSizes = append(s.metrics.BatchSizes, size)
		}
	}
}
func (l *resolverLoader) tablesMetadata(value any) {
	if s, ok := l.ctx.Value(standardRequestKey{}).(*standardRequest); ok {
		if m, ok := value.(map[string]any); ok {
			s.sourceMetadata("tables", l.consistency(), projectionMetadata(m["projections"]))
		}
	}
}
func (l *resolverLoader) snapshotBatch(jobs []*resolverJob) {
	defer func() {
		if recover() != nil {
			for _, j := range jobs {
				j.err = internal("Tables snapshot read failed")
				close(j.done)
			}
		}
	}()
	adapter := &tablesUpstream{app: l.app, project: l.project, consistency: "batch"}
	reads := make([]upstreamRead, len(jobs))
	for i, j := range jobs {
		reads[i] = upstreamRead{ID: fmt.Sprint("op", i), Operation: j.tool, Arguments: j.input}
	}
	l.backendMetrics(len(jobs), len(jobs))
	out, err := adapter.Read(l.ctx, nil, reads)
	for i, j := range jobs {
		j.err = err
		if err == nil {
			result := out[reads[i].ID]
			j.value = result.Value
			l.tablesMetadata(j.value)
		}
		close(j.done)
	}
}
func (l *resolverLoader) loadUpstream(source sourceRecord, operation string, args map[string]any, parent any) func() (any, error) {
	input := map[string]any{"arguments": args, "parent": parent}
	key := runtimeDigest([]any{"upstream", source.ID, operation, input})
	return l.enqueue(&resolverJob{source: &source, operation: operation, input: input}, key)
}
func (l *resolverLoader) upstreamBatch(jobs []*resolverJob) {
	var batchErr error
	defer func() {
		if recover() != nil {
			batchErr = internal("upstream read failed")
		}
		for _, j := range jobs {
			if batchErr != nil {
				j.err = batchErr
			}
			close(j.done)
		}
	}()
	state, ok := l.ctx.Value(standardRequestKey{}).(*standardRequest)
	if !ok {
		batchErr = internal("missing request read context")
		return
	}
	source := *jobs[0].source
	adapter := &appUpstream{app: l.app, project: l.project, source: source}
	started := time.Now()
	c, err := state.reads.get(l.ctx, source, adapter, state.limits)
	state.timingMu.Lock()
	state.metrics.SnapshotMS += milliseconds(time.Since(started))
	state.timingMu.Unlock()
	if err != nil {
		batchErr = err
		return
	}
	if state.limits.ReadConsistency == "batch" && c.snapshot == nil {
		// A reusable snapshot is also suitable for a single batch. A backend with
		// no advertised snapshot cannot silently satisfy the requested contract.
		if !adapter.Capabilities().RequestSnapshot {
			batchErr = runtimeError("snapshot_unsupported", "upstream has no bounded read snapshot capability")
			return
		}
		deadline := time.Now().Add(time.Duration(state.limits.MaxSnapshotMS) * time.Millisecond)
		if d, ok := l.ctx.Deadline(); ok && d.Before(deadline) {
			deadline = d
		}
		cSnapshot, err := adapter.Open(l.ctx, deadline)
		if err != nil {
			batchErr = err
			return
		}
		local := *c
		local.snapshot = cSnapshot
		c = &local
		defer func() {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(l.ctx), 2*time.Second)
			err := adapter.Close(cleanup, cSnapshot)
			cancel()
			if err != nil {
				for _, j := range jobs {
					j.err = runtimeError("snapshot_cleanup_failed", "upstream snapshot cleanup failed")
				}
			}
		}()
	}
	reads := make([]upstreamRead, len(jobs))
	for i, j := range jobs {
		reads[i] = upstreamRead{ID: fmt.Sprint("op", i), Operation: j.operation, Arguments: j.input["arguments"].(map[string]any), Parent: j.input["parent"]}
	}
	if len(jobs) > 1 && adapter.Capabilities().KeyBatching {
		l.backendMetrics(len(jobs), len(jobs))
	} else {
		for range jobs {
			l.backendMetrics(1, 1)
		}
	}
	startedRead := time.Now()
	out, err := adapter.Read(l.ctx, c.snapshot, reads)
	state.timingMu.Lock()
	state.upstreamNanos.Add(time.Since(startedRead).Nanoseconds())
	state.timingMu.Unlock()
	if err != nil {
		batchErr = err
		return
	}
	for i, j := range jobs {
		result, found := out[reads[i].ID]
		if !found {
			j.err = internal("upstream batch omitted a read")
		} else if result.Error != nil {
			j.err = runtimeError("upstream_read_failed", "upstream read failed")
		} else {
			j.value = result.Value
			state.sourceMetadata(source.Name, state.limits.ReadConsistency, projectionMetadata(result.Metadata))
		}
	}
}

// Fuse compatible point reads into one indexed IN query. This reduces actual
// backend queries, rather than only putting N operations in one transport.
// Pagination, hydration, predicates and arbitrary operations are never fused.
func (l *resolverLoader) bulkRecordJobs(jobs []*resolverJob) []*resolverJob {
	groups := map[string][]*resolverJob{}
	remaining := []*resolverJob{}
	for _, j := range jobs {
		if j.source != nil || j.call != nil || j.tool != "rows_get" || j.operation != "get" {
			remaining = append(remaining, j)
			continue
		}
		id, err := strconv.ParseInt(fmt.Sprint(j.input["id"]), 10, 64)
		eligible := err == nil && id > 0
		for key := range j.input {
			if key != "table" && key != "id" && key != "select" && key != "_project_id" && key != "include_total" {
				eligible = false
			}
		}
		if !eligible {
			remaining = append(remaining, j)
			continue
		}
		config := mergeMaps(j.input, map[string]any{})
		delete(config, "id")
		key := runtimeDigest(config)
		groups[key] = append(groups[key], j)
	}
	for _, group := range groups {
		for len(group) > 0 {
			n := min(100, len(group))
			chunk := group[:n]
			group = group[n:]
			if n == 1 {
				remaining = append(remaining, chunk[0])
				continue
			}
			bulk := &resolverJob{done: make(chan struct{}), call: func() (any, error) { l.bulkGet(chunk); return nil, nil }}
			remaining = append(remaining, bulk)
		}
	}
	return remaining
}
func (l *resolverLoader) bulkGet(jobs []*resolverJob) {
	var sharedErr error
	defer func() {
		if recover() != nil {
			sharedErr = internal("bulk record read failed")
		}
		if sharedErr != nil {
			for _, j := range jobs {
				j.err = sharedErr
				close(j.done)
			}
		}
	}()
	input := mergeMaps(jobs[0].input, map[string]any{})
	delete(input, "id")
	ids := []any{}
	for _, j := range jobs {
		id, _ := strconv.ParseInt(fmt.Sprint(j.input["id"]), 10, 64)
		ids = append(ids, id)
	}
	input["where"] = []any{map[string]any{"col": "id", "op": "in", "value": ids}}
	input["limit"] = len(jobs)
	input["include_total"] = false
	// ID is needed for distribution even if it was not requested by GraphQL.
	picks := []any{}
	raw, _ := json.Marshal(input["select"])
	_ = json.Unmarshal(raw, &picks)
	stripID := false
	if len(picks) > 0 {
		found := false
		for _, p := range picks {
			if p == "id" {
				found = true
			}
		}
		if !found {
			picks = append(picks, "id")
			stripID = true
		}
		input["select"] = picks
	}
	var result any
	if l.consistency() == "batch" {
		adapter := &tablesUpstream{app: l.app, project: l.project, consistency: "batch"}
		l.backendMetrics(len(jobs), 1)
		out, err := adapter.Read(l.ctx, nil, []upstreamRead{{ID: "bulk", Operation: "rows_search", Arguments: input}})
		if err != nil {
			sharedErr = err
			return
		}
		result = out["bulk"].Value
	} else {
		l.backendMetrics(len(jobs), 1)
		var err error
		result, err = l.app.callBulkTables(l.ctx, l.project, input)
		if err != nil {
			sharedErr = err
			return
		}
	}
	envelope, ok := result.(map[string]any)
	if !ok {
		sharedErr = internal("invalid bulk record response")
		return
	}
	rows, ok := envelope["rows"].([]any)
	if !ok {
		sharedErr = internal("invalid bulk record rows")
		return
	}
	byID := map[string]any{}
	for _, raw := range rows {
		row, ok := raw.(map[string]any)
		if !ok {
			sharedErr = internal("invalid bulk record row")
			return
		}
		id := fmt.Sprint(row["id"])
		copy := mergeMaps(row, map[string]any{})
		if stripID {
			delete(copy, "id")
		}
		byID[id] = copy
	}
	l.tablesMetadata(result)
	for _, j := range jobs {
		id := fmt.Sprint(j.input["id"])
		row, found := byID[id]
		j.value = map[string]any{"row": row, "found": found}
		close(j.done)
	}
}

func (a *App) callBulkTables(ctx context.Context, project string, input map[string]any) (any, error) {
	var out any
	err := sdk.CallAppResultContext(ctx, a.ctx.WithProject(project).PlatformAPI(), "tables", "rows_search", input, &out)
	return out, err
}
