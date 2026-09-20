package main

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type serverBatchPlatform struct {
	sdk.PlatformClient
	calls       []sdk.AppCall
	options     sdk.AppBatchOptions
	nativeCalls int
	nativeErr   error
	serverErr   error
	batchCalls  [][]sdk.AppCall
}

func (p *serverBatchPlatform) CallAppContext(context.Context, string, string, map[string]any) (json.RawMessage, error) {
	return nil, fmt.Errorf("unexpected raw app call")
}

func (p *serverBatchPlatform) CallAppResultContext(_ context.Context, app, tool string, input map[string]any, out any) error {
	p.nativeCalls++
	if p.nativeErr != nil {
		return p.nativeErr
	}
	if app != "tables" || tool != "tables_batch" {
		return fmt.Errorf("unexpected single app call %s.%s", app, tool)
	}
	results := map[string]any{}
	for index := range input["operations"].([]map[string]any) {
		results[fmt.Sprintf("op%d", index)] = map[string]any{"status": "ok", "result": map[string]any{"rows": []any{map[string]any{"id": index + 1}}}}
	}
	body, _ := json.Marshal(map[string]any{"results": results})
	return json.Unmarshal(body, out)
}

func (p *serverBatchPlatform) CallAppBatchContext(_ context.Context, app string, calls []sdk.AppCall, options sdk.AppBatchOptions) ([]sdk.AppCallResult, error) {
	if app != "tables" {
		return nil, fmt.Errorf("unexpected app %s", app)
	}
	p.batchCalls = append(p.batchCalls, calls)
	if p.serverErr != nil {
		return nil, p.serverErr
	}
	p.calls = calls
	p.options = options
	results := make([]sdk.AppCallResult, len(calls))
	for i, call := range calls {
		body, _ := json.Marshal(map[string]any{"rows": []any{map[string]any{"id": i + 1}}})
		results[i] = sdk.AppCallResult{ID: call.ID, Format: "json", Result: body}
	}
	return results, nil
}

func TestResolverLoaderPrefersServerParallelJSONBatch(t *testing.T) {
	platform := &serverBatchPlatform{}
	app := &App{ctx: tk.NewAppCtx(t, "apteva.yaml", tk.WithPlatform(platform))}
	loader := newResolverLoader(app, context.Background(), "p1")
	jobs := []*resolverJob{
		{tool: "rows_search", operation: "search", input: map[string]any{"table": "prospects"}, done: make(chan struct{})},
		{tool: "rows_search", operation: "search", input: map[string]any{"table": "appels"}, done: make(chan struct{})},
		{tool: "rows_search", operation: "search", input: map[string]any{"table": "ventes"}, done: make(chan struct{})},
	}
	loader.batch(jobs)
	if platform.nativeCalls != 0 || len(platform.calls) != 3 || len(platform.batchCalls) != 1 {
		t.Fatalf("native calls = %d, server batch calls = %d, transports = %#v", platform.nativeCalls, len(platform.calls), platform.batchCalls)
	}
	if platform.options.Execution != sdk.ParallelIndependent || platform.options.Concurrency != 3 || platform.options.ResultMode != "json" {
		t.Fatalf("server batch options = %#v", platform.options)
	}
	for i, job := range jobs {
		if job.err != nil {
			t.Fatalf("job %d: %v", i, job.err)
		}
		value, ok := job.value.(map[string]any)
		if !ok || len(value["rows"].([]any)) != 1 {
			t.Fatalf("job %d value = %#v", i, job.value)
		}
	}
}

func TestResolverLoaderFallsBackToTablesNativeBatch(t *testing.T) {
	platform := &serverBatchPlatform{serverErr: fmt.Errorf("server batch unavailable")}
	app := &App{ctx: tk.NewAppCtx(t, "apteva.yaml", tk.WithPlatform(platform))}
	loader := newResolverLoader(app, context.Background(), "p1")
	jobs := []*resolverJob{
		{tool: "rows_search", operation: "search", input: map[string]any{"table": "prospects"}, done: make(chan struct{})},
		{tool: "rows_search", operation: "search", input: map[string]any{"table": "appels"}, done: make(chan struct{})},
		{tool: "rows_search", operation: "search", input: map[string]any{"table": "ventes"}, done: make(chan struct{})},
	}
	loader.batch(jobs)
	if platform.nativeCalls != 1 || len(platform.calls) != 0 || len(platform.batchCalls) != 1 {
		t.Fatalf("native calls = %d, server batch calls = %d, transports = %d", platform.nativeCalls, len(platform.calls), len(platform.batchCalls))
	}
	for i, job := range jobs {
		if job.err != nil {
			t.Fatalf("job %d: %v", i, job.err)
		}
		value, ok := job.value.(map[string]any)
		if !ok || len(value["rows"].([]any)) != 1 {
			t.Fatalf("job %d value = %#v", i, job.value)
		}
	}
}
