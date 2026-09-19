package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	sdk "github.com/apteva/app-sdk"
)

// A request-local loader schedules source reads while the standard engine
// collects fields. Its deferred resolvers preserve standard completion/errors
// while coalescing sibling reads, including relationships across list items.
type resolverJob struct {
	tool, operation string
	input           map[string]any
	call            func() (any, error)
	value           any
	err             error
	done            chan struct{}
}
type resolverLoader struct {
	app     *App
	ctx     context.Context
	project string
	mu      sync.Mutex
	pending []*resolverJob
	cache   map[string]*resolverJob
	count   int
}

type countAggregateFusion struct {
	search, aggregate *resolverJob
	metric            string
}

func newResolverLoader(a *App, ctx context.Context, project string) *resolverLoader {
	return &resolverLoader{app: a, ctx: ctx, project: project, cache: map[string]*resolverJob{}}
}
func (l *resolverLoader) enqueue(j *resolverJob, key string) func() (any, error) {
	l.mu.Lock()
	if old := l.cache[key]; key != "" && old != nil {
		j = old
	} else {
		j.done = make(chan struct{})
		l.count++
		if l.count > 1000 {
			j.err = invalid("source read budget exceeded")
			close(j.done)
		} else {
			l.pending = append(l.pending, j)
			if key != "" {
				l.cache[key] = j
			}
		}
	}
	l.mu.Unlock()
	return func() (any, error) {
		l.flush()
		select {
		case <-j.done:
			if j.err != nil {
				return nil, resolverError{j.err}
			}
			return unwrapSourceResult(j.operation, j.value), nil
		case <-l.ctx.Done():
			return nil, l.ctx.Err()
		}
	}
}
func (l *resolverLoader) load(tool string, input map[string]any, operation string) func() (any, error) {
	encoded, _ := json.Marshal([]any{tool, input, operation})
	return l.enqueue(&resolverJob{tool: tool, input: input, operation: operation}, string(encoded))
}
func (l *resolverLoader) deferCall(key string, call func() (any, error)) func() (any, error) {
	return l.enqueue(&resolverJob{call: call}, key)
}
func (l *resolverLoader) flush() {
	l.mu.Lock()
	jobs := l.pending
	l.pending = nil
	l.mu.Unlock()
	if len(jobs) == 0 {
		return
	}
	fusions := fuseCountAggregates(jobs)
	var small, singles []*resolverJob
	for _, j := range jobs {
		limit := 100
		if n, ok := j.input["limit"]; ok {
			switch v := n.(type) {
			case int:
				limit = v
			case int64:
				limit = int(v)
			case float64:
				limit = int(v)
			}
		}
		if j.call == nil && limit > 0 && limit <= 100 {
			small = append(small, j)
		} else {
			singles = append(singles, j)
		}
	}
	var tasks []func()
	// Small chunks keep the Tables batch envelope bounded. Large reads stay
	// independent so their transport/decode can run concurrently.
	for len(small) > 0 {
		n := min(5, len(small))
		group := small[:n]
		small = small[n:]
		if n == 1 {
			singles = append(singles, group[0])
			continue
		}
		tasks = append(tasks, func() { l.batch(group) })
	}
	for _, j := range singles {
		tasks = append(tasks, func() {
			defer func() {
				if recover() != nil {
					j.err = internal("source resolver failed")
				}
				close(j.done)
			}()
			if err := l.ctx.Err(); err != nil {
				j.err = err
				return
			}
			if j.call != nil {
				j.value, j.err = j.call()
				return
			}
			j.err = sdk.CallAppResultContext(l.ctx, l.app.ctx.WithProject(l.project).PlatformAPI(), "tables", j.tool, j.input, &j.value)
		})
	}
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for _, task := range tasks {
		sem <- struct{}{}
		wg.Add(1)
		go func() { defer wg.Done(); defer func() { <-sem }(); task() }()
	}
	wg.Wait()
	for _, fusion := range fusions {
		fusion.finish()
	}
}

// fuseCountAggregates combines an exact page total and an ungrouped aggregate
// over the same Tables source/filter. The page read skips its count and the
// aggregate computes COUNT together with its existing metrics, so SQLite scans
// the filtered rows once instead of twice. GraphQL fields and result envelopes
// are unchanged.
func fuseCountAggregates(jobs []*resolverJob) []countAggregateFusion {
	aggregates := map[string]*resolverJob{}
	for _, job := range jobs {
		if job.call != nil || job.tool != "rows_aggregate" || hasAggregateGroups(job.input) {
			continue
		}
		if key := fusionKey(job.input); key != "" {
			aggregates[key] = job
		}
	}
	used := map[*resolverJob]bool{}
	out := []countAggregateFusion{}
	for _, search := range jobs {
		if search.call != nil || search.tool != "rows_search" || search.input["include_total"] != true {
			continue
		}
		aggregate := aggregates[fusionKey(search.input)]
		if aggregate == nil || used[aggregate] {
			continue
		}
		metric, ok := appendAggregateCount(aggregate.input)
		if !ok {
			continue
		}
		search.input["include_total"] = false
		used[aggregate] = true
		out = append(out, countAggregateFusion{search: search, aggregate: aggregate, metric: metric})
	}
	return out
}

func fusionKey(input map[string]any) string {
	table, _ := input["table"].(string)
	if table == "" {
		return ""
	}
	encoded, err := json.Marshal([]any{input["_project_id"], table, input["where"]})
	if err != nil {
		return ""
	}
	return string(encoded)
}

func hasAggregateGroups(input map[string]any) bool {
	switch groups := input["group_by"].(type) {
	case []any:
		return len(groups) > 0
	case []string:
		return len(groups) > 0
	default:
		return groups != nil
	}
}

func appendAggregateCount(input map[string]any) (string, bool) {
	raw, ok := input["metrics"]
	if !ok {
		return "", false
	}
	metrics := []any{}
	switch values := raw.(type) {
	case []any:
		metrics = append(metrics, values...)
	case []map[string]any:
		for _, value := range values {
			metrics = append(metrics, value)
		}
	default:
		return "", false
	}
	used := map[string]bool{}
	for _, rawMetric := range metrics {
		metric, _ := rawMetric.(map[string]any)
		name, _ := metric["name"].(string)
		if name != "" {
			used[name] = true
		}
		if metric["op"] == "count" && (metric["col"] == nil || metric["col"] == "") && name != "" {
			input["metrics"] = metrics
			return name, true
		}
	}
	name := "graphql_total_count"
	for suffix := 2; used[name]; suffix++ {
		name = fmt.Sprintf("graphql_total_count_%d", suffix)
	}
	metrics = append(metrics, map[string]any{"name": name, "op": "count"})
	input["metrics"] = metrics
	return name, true
}

func (f countAggregateFusion) finish() {
	if f.search.err != nil || f.aggregate.err != nil {
		return
	}
	aggregate, ok := f.aggregate.value.(map[string]any)
	if !ok {
		return
	}
	rows, ok := aggregate["rows"].([]any)
	if !ok || len(rows) == 0 {
		return
	}
	row, ok := rows[0].(map[string]any)
	if !ok {
		return
	}
	total, exists := row[f.metric]
	if !exists {
		return
	}
	search, ok := f.search.value.(map[string]any)
	if !ok {
		return
	}
	search["total"] = total
}

// batch prefers the server/SDK batch transport added in SDK v0.82. It makes
// one authorization decision, negotiates direct JSON results, and dispatches
// explicitly independent Tables reads with bounded parallelism. The Tables
// native batch remains a safe compatibility fallback: query loaders contain
// reads only, so an outer transport/capability failure can be retried without
// duplicating mutations.
func (l *resolverLoader) batch(jobs []*resolverJob) {
	if err := l.serverBatch(jobs); err == nil {
		return
	}
	l.tablesBatch(jobs)
}

func (l *resolverLoader) serverBatch(jobs []*resolverJob) (err error) {
	calls := make([]sdk.AppCall, len(jobs))
	for i, job := range jobs {
		calls[i] = sdk.AppCall{ID: fmt.Sprintf("op%d", i), Tool: job.tool, Input: job.input}
	}
	// Some older test doubles embed the optional context interface without an
	// implementation. Treat that promoted-nil panic like an unavailable batch
	// capability and exercise the native Tables fallback.
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("server app batch unavailable: %v", recovered)
		}
	}()
	results, err := sdk.CallAppBatchContext(
		l.ctx,
		l.app.ctx.WithProject(l.project).PlatformAPI(),
		"tables",
		calls,
		sdk.AppBatchOptions{
			Execution:   sdk.ParallelIndependent,
			Concurrency: min(4, len(calls)),
			ResultMode:  "json",
		},
	)
	if err != nil {
		return err
	}
	byID := make(map[string]sdk.AppCallResult, len(results))
	for _, result := range results {
		byID[result.ID] = result
	}
	for i, job := range jobs {
		result, ok := byID[fmt.Sprintf("op%d", i)]
		if !ok {
			job.err = fmt.Errorf("server app batch omitted operation op%d", i)
		} else {
			job.err = result.Decode(&job.value)
		}
		close(job.done)
	}
	return nil
}

func (l *resolverLoader) tablesBatch(jobs []*resolverJob) {
	defer func() {
		if recover() != nil {
			for _, j := range jobs {
				select {
				case <-j.done:
				default:
					j.err = internal("source batch failed")
					close(j.done)
				}
			}
		}
	}()
	ops := make([]map[string]any, len(jobs))
	for i, j := range jobs {
		ops[i] = map[string]any{"id": fmt.Sprint("op", i), "operation": j.tool, "args": j.input}
	}
	var out struct {
		Results map[string]struct {
			Status string `json:"status"`
			Result any    `json:"result"`
			Error  any    `json:"error"`
		} `json:"results"`
	}
	err := sdk.CallAppResultContext(l.ctx, l.app.ctx.WithProject(l.project).PlatformAPI(), "tables", "tables_batch", map[string]any{"mode": "best_effort", "operations": ops, "_project_id": l.project}, &out)
	for i, j := range jobs {
		j.err = err
		if err == nil {
			entry := out.Results[fmt.Sprint("op", i)]
			if entry.Status != "ok" {
				j.err = fmt.Errorf("tables read failed: %v", entry.Error)
			} else {
				j.value = entry.Result
			}
		}
		close(j.done)
	}
}
