package main

import (
	"encoding/base64"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

// newTestCtx builds a fresh AppCtx + per-test blob dir. The blob
// dir lives in the test's temp dir so the in-memory DB and the
// on-disk bytes get torn down together at t.Cleanup.
func newTestCtx(t *testing.T, opts ...tk.Option) *sdk.AppCtx {
	t.Helper()
	dir := t.TempDir()
	full := append([]tk.Option{
		tk.WithProjectID("test-proj"),
		tk.WithEnv("STORAGE_BLOBS_DIR", dir),
	}, opts...)
	ctx := tk.NewAppCtx(t, "apteva.yaml", full...)
	globalCtx = ctx
	return ctx
}

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func mustUpload(t *testing.T, ctx *sdk.AppCtx, name, folder, body string) *File {
	t.Helper()
	app := &App{}
	out, err := app.toolUpload(ctx, map[string]any{
		"name":           name,
		"folder":         folder,
		"content_base64": b64(body),
	})
	if err != nil {
		t.Fatalf("upload %s: %v", name, err)
	}
	id := int64(out.(map[string]any)["id"].(int64))
	gotOut, err := app.toolGet(ctx, map[string]any{"id": id})
	if err != nil {
		t.Fatal(err)
	}
	return gotOut.(map[string]any)["file"].(*File)
}

func TestUpload_StoresAndReturnsHash(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	out, err := app.toolUpload(ctx, map[string]any{
		"name":           "hello.txt",
		"content_base64": b64("hello world"),
		"folder":         "/notes/",
	})
	if err != nil {
		t.Fatal(err)
	}
	r := out.(map[string]any)
	if r["sha256"].(string) != "b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9" {
		t.Errorf("unexpected sha256: %v", r["sha256"])
	}
	if r["size_bytes"].(int64) != 11 {
		t.Errorf("size=%v, want 11", r["size_bytes"])
	}
	if r["was_existing"] != false {
		t.Errorf("expected was_existing=false on first upload")
	}
}

func TestUpload_DedupsExactMatch(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	args := map[string]any{
		"name":           "hello.txt",
		"folder":         "/notes/",
		"content_base64": b64("hello world"),
	}
	out1, _ := app.toolUpload(ctx, args)
	out2, _ := app.toolUpload(ctx, args)
	r1 := out1.(map[string]any)
	r2 := out2.(map[string]any)
	if r1["id"] != r2["id"] {
		t.Errorf("same content+name+folder should dedupe to same id")
	}
	if r2["was_existing"] != true {
		t.Errorf("second upload should be was_existing=true")
	}
}

func TestUpload_RejectsBadBase64(t *testing.T) {
	ctx := newTestCtx(t)
	_, err := (&App{}).toolUpload(ctx, map[string]any{
		"name":           "x.txt",
		"content_base64": "not-base64-!@#$",
	})
	if err == nil {
		t.Fatal("expected base64 error")
	}
}

func TestList_FolderIsDefault(t *testing.T) {
	ctx := newTestCtx(t)
	mustUpload(t, ctx, "a.txt", "/", "A")
	mustUpload(t, ctx, "b.txt", "/", "B")
	mustUpload(t, ctx, "c.txt", "/sub/", "C")

	app := &App{}
	out, _ := app.toolList(ctx, map[string]any{})
	r := out.(map[string]any)
	if r["count"].(int) != 2 {
		t.Errorf("root list count=%v, want 2", r["count"])
	}
}

func TestList_RecursiveDescends(t *testing.T) {
	ctx := newTestCtx(t)
	mustUpload(t, ctx, "a.txt", "/", "A")
	mustUpload(t, ctx, "b.txt", "/sub/", "B")
	mustUpload(t, ctx, "c.txt", "/sub/deep/", "C")

	app := &App{}
	out, _ := app.toolList(ctx, map[string]any{
		"folder": "/", "recursive": true,
	})
	if out.(map[string]any)["count"].(int) != 3 {
		t.Errorf("recursive count=%v, want 3", out.(map[string]any)["count"])
	}
}

func TestListFolders_OneLevel(t *testing.T) {
	ctx := newTestCtx(t)
	mustUpload(t, ctx, "x", "/reports/2025/", "x")
	mustUpload(t, ctx, "x", "/reports/2026/", "x")
	mustUpload(t, ctx, "x", "/reports/2026/q1/", "x")
	mustUpload(t, ctx, "x", "/notes/", "x")

	app := &App{}
	// Root should yield reports + notes.
	out, _ := app.toolListFolders(ctx, map[string]any{"parent": "/"})
	got := out.(map[string]any)["folders"].([]string)
	if len(got) != 2 {
		t.Errorf("root child folders=%v, want 2", got)
	}

	// /reports/ should yield 2025 + 2026 (NOT 2026/q1).
	out, _ = app.toolListFolders(ctx, map[string]any{"parent": "/reports/"})
	got = out.(map[string]any)["folders"].([]string)
	if len(got) != 2 {
		t.Errorf("/reports/ children=%v, want 2", got)
	}
}

func TestMove_ChangesFolderAndName(t *testing.T) {
	ctx := newTestCtx(t)
	f := mustUpload(t, ctx, "draft.txt", "/", "x")

	app := &App{}
	_, err := app.toolMove(ctx, map[string]any{
		"id": f.ID, "folder": "/archive/2025/", "name": "final.txt",
	})
	if err != nil {
		t.Fatal(err)
	}
	out, _ := app.toolGet(ctx, map[string]any{"id": f.ID})
	moved := out.(map[string]any)["file"].(*File)
	if moved.Folder != "/archive/2025/" || moved.Name != "final.txt" {
		t.Errorf("after move: folder=%q name=%q", moved.Folder, moved.Name)
	}
}

func TestSetVisibility_Cycles(t *testing.T) {
	ctx := newTestCtx(t)
	f := mustUpload(t, ctx, "x.txt", "/", "x")
	app := &App{}
	for _, v := range []string{"public", "signed", "private"} {
		_, err := app.toolSetVisibility(ctx, map[string]any{"id": f.ID, "visibility": v})
		if err != nil {
			t.Fatal(err)
		}
		out, _ := app.toolGet(ctx, map[string]any{"id": f.ID})
		if out.(map[string]any)["file"].(*File).Visibility != v {
			t.Errorf("vis=%q", out.(map[string]any)["file"].(*File).Visibility)
		}
	}
}

func TestDedupeCheck_FindsExisting(t *testing.T) {
	ctx := newTestCtx(t)
	f := mustUpload(t, ctx, "a.txt", "/", "hello world")
	app := &App{}
	out, _ := app.toolDedupe(ctx, map[string]any{"sha256": f.SHA256})
	if out.(map[string]any)["found"] != true {
		t.Errorf("expected found=true")
	}
	out, _ = app.toolDedupe(ctx, map[string]any{
		"sha256": "0000000000000000000000000000000000000000000000000000000000000000",
	})
	if out.(map[string]any)["found"] != false {
		t.Errorf("expected found=false for unknown sha")
	}
}

func TestSearch_FilterByContentTypePrefix(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	app.toolUpload(ctx, map[string]any{
		"name": "a.png", "content_base64": b64("png"), "content_type": "image/png",
	})
	app.toolUpload(ctx, map[string]any{
		"name": "b.txt", "content_base64": b64("txt"), "content_type": "text/plain",
	})
	out, _ := app.toolSearch(ctx, map[string]any{"content_type": "image"})
	if out.(map[string]any)["count"].(int) != 1 {
		t.Errorf("expected 1 image, got %v", out.(map[string]any)["count"])
	}
}

func TestSoftDelete_HidesFromList(t *testing.T) {
	ctx := newTestCtx(t)
	f := mustUpload(t, ctx, "x.txt", "/", "x")
	app := &App{}
	app.toolDelete(ctx, map[string]any{"id": f.ID})
	out, _ := app.toolList(ctx, map[string]any{})
	if out.(map[string]any)["count"].(int) != 0 {
		t.Errorf("soft-deleted file should not list")
	}
	got, _ := app.toolGet(ctx, map[string]any{"id": f.ID})
	if got.(map[string]any)["found"] != false {
		t.Errorf("soft-deleted file should not be retrievable by get")
	}
}

func TestProjectScope_Isolation(t *testing.T) {
	// Same blob dir, two project ids — files don't leak across.
	dir := t.TempDir()
	ctxA := tk.NewAppCtx(t, "apteva.yaml",
		tk.WithProjectID("proj-A"),
		tk.WithEnv("STORAGE_BLOBS_DIR", dir),
	)
	ctxB := tk.NewAppCtx(t, "apteva.yaml",
		tk.WithProjectID("proj-B"),
		tk.WithEnv("STORAGE_BLOBS_DIR", dir),
	)
	// Different DBs — testkit gives each call its own in-memory pool —
	// so the scope check here is really "the API requires the right
	// project_id". Validates the env-driven resolveProject path.
	globalCtx = ctxA
	(&App{}).toolUpload(ctxA, map[string]any{"name": "a.txt", "content_base64": b64("a")})
	globalCtx = ctxB
	out, _ := (&App{}).toolList(ctxB, map[string]any{})
	if out.(map[string]any)["count"].(int) != 0 {
		t.Errorf("project B should see 0 files; saw %v", out.(map[string]any)["count"])
	}
}

func TestResolveProject_GlobalScopeRequiresArg(t *testing.T) {
	t.Setenv("APTEVA_PROJECT_ID", "")
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	globalCtx = ctx
	_, err := (&App{}).toolUpload(ctx, map[string]any{
		"name": "x.txt", "content_base64": b64("x"),
	})
	if err == nil {
		t.Fatal("expected error in global scope without _project_id")
	}
	if !strings.Contains(err.Error(), "project_id") {
		t.Errorf("wrong error: %v", err)
	}
}
