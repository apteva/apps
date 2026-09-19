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
}
func (l *resolverLoader) batch(jobs []*resolverJob) {
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
