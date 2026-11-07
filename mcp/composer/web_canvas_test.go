package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

const webCanvasTestPage = `<!doctype html>
<html><head><link rel="stylesheet" href="style.css"></head>
<body><div id="box"></div>
<script src="lib/scene.js"></script>
</body></html>`

func webCanvasTestRevision() *ProcedureRevisionRecord {
	return &ProcedureRevisionRecord{ProcedureID: 1, Revision: 1, Runtime: webCanvasRuntime, Entrypoint: "index.html", Target: "clip", OutputKind: "video", Files: map[string]string{
		"index.html":   webCanvasTestPage,
		"style.css":    "html,body{margin:0;background:#000}#box{position:absolute;width:200px;height:200px;background:#0f0}",
		"lib/scene.js": "window.setTime = t => { document.getElementById('box').style.left = (t * 100) + 'px'; }; // </script> stays inert",
		"data.json":    `{"rows":[1,2,3]}`,
	}}
}

func TestBuildWebCanvasDocumentInlinesLocalFilesAndInjectsBridge(t *testing.T) {
	doc, err := buildWebCanvasDocument(webCanvasTestRevision(), webCanvasEnv{
		Mode: "render", Canvas: map[string]any{"width": 640, "height": 360},
		Timeline: procedureTimeline{Duration: 2, FPS: 30}, Parameters: map[string]any{"title": "</script><b>x"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"<style>html,body", "window.setTime = t =>", `<\/script> stays inert`, "window.__setComposerTime", `"data.json"`} {
		if !strings.Contains(doc, want) {
			t.Fatalf("document missing %q:\n%s", want, doc)
		}
	}
	for _, unwanted := range []string{`href="style.css"`, `src="lib/scene.js"`, "</script><b>x"} {
		if strings.Contains(doc, unwanted) {
			t.Fatalf("document still contains %q", unwanted)
		}
	}
	if strings.Index(doc, "window.composer =") > strings.Index(doc, "window.setTime") {
		t.Fatal("bridge must run before page scripts")
	}
}

func TestWebCanvasSourceValidation(t *testing.T) {
	ok := map[string]any{"runtime": webCanvasRuntime, "entrypoint": "index.html", "files": map[string]any{"index.html": "<html></html>"}}
	if _, _, _, _, _, _, _, err := normalizeProcedureSource(ok); err != nil {
		t.Fatalf("valid web-canvas source rejected: %v", err)
	}
	for name, patch := range map[string]map[string]any{
		"audio output": {"target": "audio", "output_kind": "audio"},
		"script entry": {"entrypoint": "render.ts", "files": map[string]any{"render.ts": "x"}},
	} {
		args := map[string]any{}
		for k, v := range ok {
			args[k] = v
		}
		for k, v := range patch {
			args[k] = v
		}
		if _, _, _, _, _, _, _, err := normalizeProcedureSource(args); err == nil {
			t.Fatalf("%s: expected rejection", name)
		}
	}
}

func TestProcedurePreviewServesWebCanvasDocument(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-a")).WithProject("project-a")
	globalCtx = ctx
	defer func() { globalCtx = nil }()
	app := &App{}
	created, err := app.toolProcedureCreate(ctx, map[string]any{
		"name": "Box", "runtime": webCanvasRuntime, "entrypoint": "index.html", "files": map[string]any(func() map[string]any {
			files := map[string]any{}
			for k, v := range webCanvasTestRevision().Files {
				files[k] = v
			}
			return files
		}()),
	})
	if err != nil {
		t.Fatal(err)
	}
	id := created.(map[string]any)["procedure"].(*ProcedureRecord).ID
	body, _ := json.Marshal(map[string]any{"parameters": map[string]any{"title": "Hi"}, "width": 640, "height": 360, "duration": 2})
	req := httptest.NewRequest(http.MethodPost, "/procedure/"+itoa64(id)+"/preview", strings.NewReader(string(body)))
	req.Header.Set("X-Apteva-Project-ID", "project-a")
	rec := httptest.NewRecorder()
	app.handleProcedureByID(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"mode":"preview"`) || !strings.Contains(rec.Body.String(), `"title":"Hi"`) {
		t.Fatalf("preview status=%d body=%s", rec.Code, rec.Body.String())
	}

	other, err := app.toolProcedureCreate(ctx, map[string]any{"name": "Py", "runtime": "python-3.13-media", "entrypoint": "render.py", "source": "pass"})
	if err != nil {
		t.Fatal(err)
	}
	otherID := other.(map[string]any)["procedure"].(*ProcedureRecord).ID
	req = httptest.NewRequest(http.MethodPost, "/procedure/"+itoa64(otherID)+"/preview", strings.NewReader("{}"))
	req.Header.Set("X-Apteva-Project-ID", "project-a")
	rec = httptest.NewRecorder()
	app.handleProcedureByID(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("non web-canvas preview status=%d", rec.Code)
	}
}

func TestRunProcedureLocalWebCanvas(t *testing.T) {
	if _, err := chromeExecutable(); err != nil {
		t.Skip("Chrome not available")
	}
	if _, err := exec.LookPath(browserNodePath()); err != nil {
		t.Skip("node not available")
	}
	if _, err := exec.LookPath(ffprobePath()); err != nil {
		t.Skip("ffprobe not available")
	}
	ctx := tk.NewAppCtx(t, "apteva.yaml").WithProject("project-a")
	output := defaultOutput()
	output.Resolution, output.Aspect = "sd", "16:9"
	result, err := runProcedureLocal(context.Background(), ctx, webCanvasTestRevision(), &ProceduralAsset{}, output, procedureTimeline{JobID: "test:web-canvas", Duration: 1, FPS: 12})
	if result.Cleanup != nil {
		defer result.Cleanup()
	}
	if err != nil {
		t.Fatalf("web-canvas procedure: %v; logs=%s", err, result.Logs)
	}
	if result.Kind != "video" || result.DurationSeconds < 0.9 || result.DurationSeconds > 1.1 {
		t.Fatalf("unexpected artifact kind=%s duration=%v", result.Kind, result.DurationSeconds)
	}
}

func itoa64(v int64) string {
	b, _ := json.Marshal(v)
	return string(b)
}
