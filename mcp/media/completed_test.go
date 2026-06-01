package main

import (
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

func TestMaybeEmitMediaCompleted_StorageFileOrigin(t *testing.T) {
	rec := tk.NewEmitRecorder()
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj), tk.WithEmitter(rec))
	globalCtx = ctx

	if err := upsertMedia(ctx.AppDB(), testProj, "42", sampleVideoProbe(), "sha", "/uploads/", "source.mov"); err != nil {
		t.Fatal(err)
	}
	if err := upsertDerivation(ctx.AppDB(), testProj, "42", "thumbnail", 1001, 320, 180, 0); err != nil {
		t.Fatal(err)
	}

	maybeEmitMediaCompleted(ctx, testProj, "42")

	events := rec.EventsByTopic("media.completed")
	if len(events) != 1 {
		t.Fatalf("expected one media.completed event, got %d", len(events))
	}
	if events[0].ProjectID != testProj {
		t.Fatalf("project_id=%q want %q", events[0].ProjectID, testProj)
	}
	data := events[0].Data.(map[string]any)
	if data["origin"] != "storage_file" {
		t.Fatalf("origin=%v want storage_file", data["origin"])
	}
	if _, ok := data["output_of_render_id"]; ok {
		t.Fatalf("storage file should not carry render provenance: %+v", data)
	}
}

func TestMaybeEmitMediaCompleted_RenderOutputOrigin(t *testing.T) {
	rec := tk.NewEmitRecorder()
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj), tk.WithEmitter(rec))
	globalCtx = ctx

	renderID, err := insertRender(ctx.AppDB(), testProj, "extract_reel", []string{"42"}, nil, "reel.mp4", "/renders/", "agent:test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := claimNextPending(ctx.AppDB()); err != nil {
		t.Fatal(err)
	}
	if err := renderMarkOk(ctx.AppDB(), renderID, "66"); err != nil {
		t.Fatal(err)
	}
	if err := upsertMedia(ctx.AppDB(), testProj, "66", sampleVideoProbe(), "sha", "/renders/", "reel.mp4"); err != nil {
		t.Fatal(err)
	}
	if err := upsertDerivation(ctx.AppDB(), testProj, "66", "thumbnail", 1002, 320, 180, 0); err != nil {
		t.Fatal(err)
	}

	maybeEmitMediaCompleted(ctx, testProj, "66")

	events := rec.EventsByTopic("media.completed")
	if len(events) != 1 {
		t.Fatalf("expected one media.completed event, got %d", len(events))
	}
	data := events[0].Data.(map[string]any)
	if data["origin"] != "render_output" {
		t.Fatalf("origin=%v want render_output", data["origin"])
	}
	if data["output_of_render_id"] != renderID {
		t.Fatalf("output_of_render_id=%v want %d", data["output_of_render_id"], renderID)
	}
	if data["render_operation"] != "extract_reel" {
		t.Fatalf("render_operation=%v want extract_reel", data["render_operation"])
	}
	gotSources, ok := data["render_source_file_ids"].([]string)
	if !ok || len(gotSources) != 1 || gotSources[0] != "42" {
		t.Fatalf("render_source_file_ids=%#v want [42]", data["render_source_file_ids"])
	}
}
