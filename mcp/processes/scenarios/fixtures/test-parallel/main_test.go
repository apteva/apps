package main

import (
	"context"
	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
	"sync"
	"testing"
	"time"
)

func TestTransferableParallelWorkKeepsOwnerSession(t *testing.T) {
	a := &App{workDelay: 100 * time.Millisecond}
	appctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("test-project"))
	if e := a.OnMount(appctx); e != nil {
		t.Fatal(e)
	}
	caller := &sdk.Caller{AgentID: 7, ProjectID: "test-project", ThreadID: "owner", ToolCallID: "prepare-call"}
	ctx := sdk.WithCaller(context.Background(), caller)
	prepared, e := a.call(ctx, "prepare", nil)
	if e != nil {
		t.Fatal(e)
	}
	id := prepared.(map[string]any)["context_id"]
	if _, e = a.call(ctx, "prepare", nil); e == nil {
		t.Fatal("duplicate prepare accepted")
	}
	if _, e = a.call(ctx, "session", map[string]any{"context_id": id, "phase": "b"}); e == nil {
		t.Fatal("phase b before a")
	}
	child := *caller
	child.ThreadID = "child"
	child.ToolCallID = "child-call"
	if _, e = a.call(sdk.WithCaller(context.Background(), &child), "session", map[string]any{"context_id": id, "phase": "a"}); e == nil {
		t.Fatal("child took owner session")
	}
	other := child
	other.ProjectID = "other-project"
	if _, e = a.call(sdk.WithCaller(context.Background(), &other), "work", map[string]any{"context_id": id, "artifact_id": "alpha.png"}); e == nil {
		t.Fatal("wrong project used source")
	}
	if _, e = a.call(ctx, "validate", map[string]any{"context_id": id}); e == nil {
		t.Fatal("validated missing outputs")
	}
	var wg sync.WaitGroup
	for _, artifact := range []string{"alpha.png", "beta.png"} {
		wg.Add(1)
		go func(artifact string) {
			defer wg.Done()
			c := child
			c.ThreadID = artifact
			c.ToolCallID = artifact
			_, e := a.call(sdk.WithCaller(context.Background(), &c), "work", map[string]any{"context_id": id, "artifact_id": artifact})
			if e != nil {
				t.Error(e)
			}
		}(artifact)
	}
	wg.Wait()
	var overlap int
	if e := a.db.QueryRow(`SELECT count(*) FROM operations a JOIN operations b ON a.artifact_id='alpha.png' AND b.artifact_id='beta.png' WHERE a.name='work' AND b.name='work' AND max(a.started_at,b.started_at)<min(a.completed_at,b.completed_at) AND a.thread_id<>b.thread_id`).Scan(&overlap); e != nil || overlap != 1 {
		t.Fatal("lost real overlap", overlap, e)
	}
	for _, phase := range []string{"a", "b"} {
		if _, e = a.call(ctx, "session", map[string]any{"context_id": id, "phase": phase}); e != nil {
			t.Fatal(e)
		}
	}
	if _, e = a.call(ctx, "publish", map[string]any{"context_id": id, "approval_receipt": approval}); e == nil {
		t.Fatal("published before validation")
	}
	if _, e = a.call(ctx, "validate", map[string]any{"context_id": id}); e != nil {
		t.Fatal(e)
	}
	if _, e = a.call(ctx, "publish", map[string]any{"context_id": id, "approval_receipt": "self"}); e == nil {
		t.Fatal("published without operator evidence")
	}
	if _, e = a.call(ctx, "publish", map[string]any{"context_id": id, "approval_receipt": approval}); e != nil {
		t.Fatal(e)
	}
}
