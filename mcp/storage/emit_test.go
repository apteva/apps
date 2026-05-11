package main

// emit_test.go — assert that storage's mutation paths (HTTP + MCP)
// publish to the platform's app-event bus via ctx.Emit. The bus
// itself is exercised in apteva-server's appbus_test.go; here we
// only verify that storage CALLS Emit with the right topic + payload.

import (
	"encoding/base64"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

// newRecordedCtx builds a fresh AppCtx wired to an EmitRecorder so
// tests can assert what the handler published.
func newRecordedCtx(t *testing.T) (*sdk.AppCtx, *tk.EmitRecorder) {
	t.Helper()
	dir := t.TempDir()
	rec := tk.NewEmitRecorder()
	ctx := tk.NewAppCtx(t, "apteva.yaml",
		tk.WithProjectID("test-proj"),
		tk.WithEnv("STORAGE_BLOBS_DIR", dir),
		tk.WithEmitter(rec),
	)
	globalCtx = ctx
	return ctx, rec
}

func TestEmit_ToolUploadFiresFileAdded(t *testing.T) {
	ctx, rec := newRecordedCtx(t)
	app := &App{}

	out, err := app.toolUpload(ctx, map[string]any{
		"name":           "doc.txt",
		"folder":         "/letters/",
		"content_base64": base64.StdEncoding.EncodeToString([]byte("hello")),
	})
	if err != nil {
		t.Fatalf("toolUpload: %v", err)
	}
	id := out.(map[string]any)["id"].(int64)

	got := rec.EventsByTopic("file.added")
	if len(got) != 1 {
		t.Fatalf("expected 1 file.added emit, got %d (events=%+v)", len(got), rec.Events())
	}
	data, ok := got[0].Data.(map[string]any)
	if !ok {
		t.Fatalf("emit data not a map: %T %+v", got[0].Data, got[0].Data)
	}
	if data["id"] != id {
		t.Errorf("emit data.id = %v, want %v", data["id"], id)
	}
	if data["name"] != "doc.txt" {
		t.Errorf("emit data.name = %v", data["name"])
	}
	if data["folder"] != "/letters/" {
		t.Errorf("emit data.folder = %v", data["folder"])
	}
	if data["sha256"] == nil || data["sha256"] == "" {
		t.Error("emit data.sha256 missing")
	}
	if data["was_existing"] != false {
		t.Errorf("emit data.was_existing = %v, want false on first upload", data["was_existing"])
	}
}

func TestEmit_ToolUploadDedupedFileMarksWasExisting(t *testing.T) {
	ctx, rec := newRecordedCtx(t)
	app := &App{}
	args := map[string]any{
		"name":           "dup.txt",
		"folder":         "/",
		"content_base64": base64.StdEncoding.EncodeToString([]byte("same bytes")),
	}
	if _, err := app.toolUpload(ctx, args); err != nil {
		t.Fatal(err)
	}
	rec.Reset()
	// Second upload with the same bytes — saveBytes returns the
	// existing row, but the handler still emits so subscribers can
	// re-render (was_existing tells them to skip the row insert).
	if _, err := app.toolUpload(ctx, args); err != nil {
		t.Fatal(err)
	}
	got := rec.EventsByTopic("file.added")
	if len(got) != 1 {
		t.Fatalf("expected 1 file.added on dup, got %d", len(got))
	}
	data := got[0].Data.(map[string]any)
	if data["was_existing"] != true {
		t.Errorf("was_existing = %v, want true on dedup", data["was_existing"])
	}
}

func TestEmit_ToolDeleteFiresFileDeleted(t *testing.T) {
	ctx, rec := newRecordedCtx(t)
	app := &App{}
	out, _ := app.toolUpload(ctx, map[string]any{
		"name":           "todelete.txt",
		"content_base64": base64.StdEncoding.EncodeToString([]byte("bye")),
	})
	id := out.(map[string]any)["id"].(int64)
	rec.Reset()

	if _, err := app.toolDelete(ctx, map[string]any{"id": id}); err != nil {
		t.Fatalf("toolDelete: %v", err)
	}
	got := rec.EventsByTopic("file.deleted")
	if len(got) != 1 {
		t.Fatalf("expected 1 file.deleted, got %d", len(got))
	}
	data, ok := got[0].Data.(map[string]any)
	if !ok {
		t.Fatalf("data not a map: %T", got[0].Data)
	}
	if data["id"] != id {
		t.Errorf("emit data.id = %v, want %v", data["id"], id)
	}
	if data["name"] != "todelete.txt" {
		t.Errorf("emit data.name = %v", data["name"])
	}
}

func TestEmit_DeleteUnknownIDDoesNotEmit(t *testing.T) {
	ctx, rec := newRecordedCtx(t)
	app := &App{}
	// dbGetByID will return nil for an id that doesn't exist;
	// emitFileEvent silently no-ops on nil File.
	_, err := app.toolDelete(ctx, map[string]any{"id": int64(9999)})
	// dbSoftDelete tolerates "no rows affected" — no error.
	_ = err
	if got := rec.EventsByTopic("file.deleted"); len(got) != 0 {
		t.Fatalf("expected zero emits on unknown-id delete, got %d", len(got))
	}
}

func TestEmit_DataPayloadShape(t *testing.T) {
	// Snapshot the keys the dashboard relies on so a refactor that
	// drops a field surfaces here instead of breaking the panel
	// silently.
	ctx, rec := newRecordedCtx(t)
	app := &App{}
	if _, err := app.toolUpload(ctx, map[string]any{
		"name":           "s.txt",
		"content_base64": base64.StdEncoding.EncodeToString([]byte("x")),
	}); err != nil {
		t.Fatal(err)
	}
	got := rec.EventsByTopic("file.added")
	if len(got) != 1 {
		t.Fatal("missing emit")
	}
	data := got[0].Data.(map[string]any)
	wantKeys := []string{"id", "name", "folder", "size_bytes", "content_type", "sha256", "visibility", "was_existing"}
	for _, k := range wantKeys {
		if _, ok := data[k]; !ok {
			t.Errorf("emit payload missing key %q (have: %s)", k, mapKeys(data))
		}
	}
}

// Storage's file events must carry the file row's project_id on the
// emit envelope (not just inside the data payload). This is what
// lets a global storage install's events route to the right per-
// project lane on the bus and reach a global media install's
// per-project subscriber. Before EmitWithProject was wired through,
// the wire-level project_id came from the install row — empty for a
// global install — and media's indexer silently dropped every event.
func TestEmit_FileEventsCarryFilesProjectID(t *testing.T) {
	ctx, rec := newRecordedCtx(t)
	app := &App{}
	out, err := app.toolUpload(ctx, map[string]any{
		"name":           "tagged.txt",
		"content_base64": base64.StdEncoding.EncodeToString([]byte("x")),
	})
	if err != nil {
		t.Fatalf("toolUpload: %v", err)
	}
	_ = out
	got := rec.EventsByTopic("file.added")
	if len(got) != 1 {
		t.Fatalf("expected 1 file.added, got %d", len(got))
	}
	if got[0].ProjectID != "test-proj" {
		t.Errorf("emit envelope ProjectID = %q, want test-proj (from file row, via EmitWithProject)", got[0].ProjectID)
	}
}

func mapKeys(m map[string]any) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return strings.Join(keys, ",")
}
