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
	calls   []sdk.AppCall
	options sdk.AppBatchOptions
}

func (p *serverBatchPlatform) CallAppContext(context.Context, string, string, map[string]any) (json.RawMessage, error) {
	return nil, fmt.Errorf("unexpected raw app call")
}

func (p *serverBatchPlatform) CallAppResultContext(context.Context, string, string, map[string]any, any) error {
	return fmt.Errorf("unexpected single app call")
}

func (p *serverBatchPlatform) CallAppBatchContext(_ context.Context, app string, calls []sdk.AppCall, options sdk.AppBatchOptions) ([]sdk.AppCallResult, error) {
	if app != "tables" {
		return nil, fmt.Errorf("unexpected app %s", app)
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

func TestResolverLoaderUsesServerParallelJSONBatch(t *testing.T) {
	platform := &serverBatchPlatform{}
	app := &App{ctx: tk.NewAppCtx(t, "apteva.yaml", tk.WithPlatform(platform))}
	loader := newResolverLoader(app, context.Background(), "p1")
	jobs := []*resolverJob{
		{tool: "rows_search", operation: "search", input: map[string]any{"table": "prospects"}, done: make(chan struct{})},
		{tool: "rows_search", operation: "search", input: map[string]any{"table": "appels"}, done: make(chan struct{})},
		{tool: "rows_search", operation: "search", input: map[string]any{"table": "ventes"}, done: make(chan struct{})},
	}
	loader.batch(jobs)
	if len(platform.calls) != 3 {
		t.Fatalf("server batch calls = %d, want 3", len(platform.calls))
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
