package main

import (
	"context"
	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
	"testing"
)

func TestFixtureRequiresSameWorkerAndSavedCheckpoints(t *testing.T) {
	app := &App{}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("test-project"))
	if err := app.OnMount(ctx); err != nil {
		t.Fatal(err)
	}
	caller := &sdk.Caller{AgentID: 7, ProjectID: "test-project", ThreadID: "worker", ToolCallID: "call-1"}
	callctx := sdk.WithCaller(context.Background(), caller)
	raw, err := app.call(callctx, "prepare", nil)
	if err != nil {
		t.Fatal(err)
	}
	id := raw.(map[string]any)["context_id"].(string)
	if _, err = app.call(callctx, "prepare", nil); err == nil {
		t.Fatal("prepared twice")
	}
	if _, err = app.call(callctx, "render", map[string]any{"context_id": id, "artifact_id": "portrait-4.png"}); err == nil {
		t.Fatal("rendered before retained checkpoint")
	}
	if _, err = app.call(callctx, "validate", map[string]any{"context_id": id}); err == nil {
		t.Fatal("validated missing artifact")
	}
	wrong := *caller
	wrong.ThreadID = "another-worker"
	if _, err = app.call(sdk.WithCaller(context.Background(), &wrong), "render", map[string]any{"context_id": id, "artifact_id": "portrait-3.png"}); err == nil {
		t.Fatal("context crossed worker threads")
	}
	for _, artifact := range []string{"portrait-3.png", "portrait-4.png"} {
		if _, err = app.call(callctx, "render", map[string]any{"context_id": id, "artifact_id": artifact}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = app.call(callctx, "validate", map[string]any{"context_id": id}); err != nil {
		t.Fatal(err)
	}
	if _, err = app.call(callctx, "publish", map[string]any{"context_id": id, "approval_receipt": "self-approved"}); err == nil {
		t.Fatal("accepted missing operator receipt")
	}
	if _, err = app.call(callctx, "publish", map[string]any{"context_id": id, "approval_receipt": "Operator approved: portrait-3.png and portrait-4.png after validation."}); err != nil {
		t.Fatal(err)
	}
	var n int
	if err = app.db.QueryRow(`SELECT count(*) FROM operations`).Scan(&n); err != nil || n != 5 {
		t.Fatal(n, err)
	}
}
