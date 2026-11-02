package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// web-canvas-1 procedures are an HTML page that draws one frame for any time
// t. The page defines setTime(t) (or calls composer.onTime(fn)); Composer
// renders it with the same headless-Chrome capture loop as the browser V2
// renderer, and the panel previews the identical document in a sandboxed
// iframe that it seeks by postMessage.
const webCanvasRuntime = "web-canvas-1"

type webCanvasAsset struct {
	URL  string `json:"url"`
	Kind string `json:"kind,omitempty"`
}

type webCanvasEnv struct {
	Mode       string                    `json:"mode"` // render | preview
	Canvas     map[string]any            `json:"canvas"`
	Timeline   procedureTimeline         `json:"timeline"`
	Parameters map[string]any            `json:"parameters"`
	Assets     map[string]webCanvasAsset `json:"assets"`
	Files      map[string]string         `json:"files"`
}

func validateWebCanvasSource(target, outputKind, entrypoint string) error {
	if target != "clip" && target != "still" {
		return errors.New("web-canvas-1 procedures support target clip|still")
	}
	if outputKind != "video" && outputKind != "image" {
		return errors.New("web-canvas-1 procedures support output_kind video|image")
	}
	if ext := strings.ToLower(path.Ext(entrypoint)); ext != ".html" && ext != ".htm" {
		return errors.New("web-canvas-1 entrypoint must be an .html file")
	}
	return nil
}

var (
	webCanvasScriptTag = regexp.MustCompile(`(?is)<script\b([^>]*?)\bsrc\s*=\s*["']([^"']+)["']([^>]*)>\s*</script>`)
	webCanvasLinkTag   = regexp.MustCompile(`(?is)<link\b[^>]*>`)
	webCanvasHrefAttr  = regexp.MustCompile(`(?is)\bhref\s*=\s*["']([^"']+)["']`)
	webCanvasHeadOpen  = regexp.MustCompile(`(?is)<head\b[^>]*>`)
)

// buildWebCanvasDocument inlines the procedure's local scripts and
// stylesheets into the entry page and injects the composer bridge, producing
// one self-contained document. Render and preview use the same document so
// what the panel shows is what the renderer captures.
func buildWebCanvasDocument(revision *ProcedureRevisionRecord, env webCanvasEnv) (string, error) {
	entry, ok := revision.Files[revision.Entrypoint]
	if !ok {
		return "", fmt.Errorf("entrypoint %q is not present in files", revision.Entrypoint)
	}
	base := path.Dir(revision.Entrypoint)
	local := func(ref string) (string, string, bool) {
		if strings.Contains(ref, ":") || strings.HasPrefix(ref, "//") || strings.HasPrefix(ref, "/") {
			return "", "", false
		}
		name := path.Clean(path.Join(base, strings.SplitN(strings.SplitN(ref, "?", 2)[0], "#", 2)[0]))
		content, ok := revision.Files[name]
		return name, content, ok
	}
	inlined := map[string]bool{}
	doc := webCanvasScriptTag.ReplaceAllStringFunc(entry, func(tag string) string {
		m := webCanvasScriptTag.FindStringSubmatch(tag)
		name, content, ok := local(m[2])
		if !ok {
			return tag
		}
		inlined[name] = true
		return "<script" + m[1] + m[3] + ">" + strings.ReplaceAll(content, "</script", `<\/script`) + "</script>"
	})
	doc = webCanvasLinkTag.ReplaceAllStringFunc(doc, func(tag string) string {
		if !strings.Contains(strings.ToLower(tag), "stylesheet") {
			return tag
		}
		m := webCanvasHrefAttr.FindStringSubmatch(tag)
		if m == nil {
			return tag
		}
		name, content, ok := local(m[1])
		if !ok {
			return tag
		}
		inlined[name] = true
		return "<style>" + strings.ReplaceAll(content, "</style", `<\/style`) + "</style>"
	})
	env.Files = map[string]string{}
	for name, content := range revision.Files {
		if name != revision.Entrypoint && !inlined[name] {
			env.Files[name] = content
		}
	}
	if env.Parameters == nil {
		env.Parameters = map[string]any{}
	}
	if env.Assets == nil {
		env.Assets = map[string]webCanvasAsset{}
	}
	payload, err := json.Marshal(env)
	if err != nil {
		return "", err
	}
	bridge := "<script>window.composer = " + string(payload) + ";\n" + webCanvasBridgeJS + "</script>"
	if loc := webCanvasHeadOpen.FindStringIndex(doc); loc != nil {
		return doc[:loc[1]] + bridge + doc[loc[1]:], nil
	}
	return bridge + doc, nil
}

const webCanvasBridgeJS = `(function () {
  var C = window.composer, handler = null;
  C.onTime = function (fn) { handler = fn; };
  function draw(t) {
    var fn = handler || window.setTime;
    if (typeof fn !== "function") throw new Error("web-canvas page must define setTime(t) or call composer.onTime(fn)");
    return fn(t);
  }
  function settle() {
    if (C.mode !== "render") return Promise.resolve();
    return new Promise(function (r) { requestAnimationFrame(function () { requestAnimationFrame(r); }); });
  }
  window.__setComposerTime = function (t) { return Promise.resolve(draw(t)).then(settle); };
  var loaded = new Promise(function (r) { if (document.readyState === "complete") r(); else addEventListener("load", r, { once: true }); });
  window.__composerReady = loaded
    .then(function () { return document.fonts ? document.fonts.ready : null; })
    .then(function () { return window.composerReady; })
    .then(function () { return Promise.all(Array.prototype.map.call(document.images, function (img) { return img.decode().catch(function () {}); })); })
    .then(function () { return window.__setComposerTime(C.timeline.source_start || 0); });
  if (C.mode !== "preview" || parent === window) return;
  function report(err) { parent.postMessage({ type: "composer:error", message: String((err && err.message) || err) }, "*"); }
  window.__composerReady.then(function () { parent.postMessage({ type: "composer:ready" }, "*"); }, report);
  addEventListener("error", function (e) { report(e.error || e.message); });
  addEventListener("message", function (e) {
    var m = e.data;
    if (!m || m.type !== "composer:seek") return;
    window.__composerReady.then(function () { return window.__setComposerTime(Number(m.t) || 0); }).catch(report);
  });
})();
`

// runWebCanvasProcedure captures the page frame by frame and writes the
// artifact plus result.json into outputDir, so the regular procedure result
// validation applies unchanged.
func runWebCanvasProcedure(ctx context.Context, appCtx *sdk.AppCtx, revision *ProcedureRevisionRecord, job procedureJob, outputDir, tmpDir string, logs *boundedProcedureLog) error {
	chromePath, err := chromeExecutable()
	if err != nil {
		return err
	}
	nodePath, err := exec.LookPath(browserNodePath())
	if err != nil {
		return fmt.Errorf("node is required for web-canvas-1 procedures: %w", err)
	}
	assets := map[string]webCanvasAsset{}
	for name, asset := range job.Assets {
		assets[name] = webCanvasAsset{URL: fileURL(asset.Path), Kind: asset.Kind}
	}
	width, _ := job.Canvas["width"].(int)
	height, _ := job.Canvas["height"].(int)
	if width <= 0 || height <= 0 {
		return errors.New("web-canvas-1 needs a canvas size")
	}
	fps := job.Timeline.FPS
	if fps <= 0 {
		fps = 30
	}
	job.Timeline.FPS = fps
	doc, err := buildWebCanvasDocument(revision, webCanvasEnv{Mode: "render", Canvas: job.Canvas, Timeline: job.Timeline, Parameters: job.Parameters, Assets: assets})
	if err != nil {
		return err
	}
	htmlPath := filepath.Join(tmpDir, "scene.html")
	if err := os.WriteFile(htmlPath, []byte(doc), 0o600); err != nil {
		return err
	}
	scriptPath := filepath.Join(tmpDir, "capture.js")
	if err := os.WriteFile(scriptPath, []byte(browserCaptureScript()), 0o600); err != nil {
		return err
	}
	framesDir := filepath.Join(tmpDir, "frames")
	frames := 1
	if job.Target != "still" {
		frames = int(job.Timeline.Duration*float64(fps) + 0.5)
		if frames < 1 {
			return errors.New("web-canvas-1 clip duration must be > 0")
		}
	}
	port, err := freeTCPPort()
	if err != nil {
		return err
	}
	capture := exec.CommandContext(ctx, nodePath, scriptPath, chromePath, fileURL(htmlPath), framesDir,
		strconv.Itoa(width), strconv.Itoa(height), strconv.Itoa(fps), strconv.Itoa(frames), strconv.Itoa(port), filepath.Join(tmpDir, "chrome-profile"))
	capture.Env = append(os.Environ(), fmt.Sprintf("COMPOSER_EXCERPT_START=%g", job.Timeline.SourceStart))
	capture.Cancel = func() error { return capture.Process.Signal(os.Interrupt) }
	capture.WaitDelay = 5 * time.Second
	capture.Stdout, capture.Stderr = logs, logs
	if err := capture.Run(); err != nil {
		return fmt.Errorf("web-canvas capture: %w", err)
	}
	artifact, kind, mime := "main.mp4", "video", "video/mp4"
	args := []string{"-y", "-loglevel", "error", "-framerate", strconv.Itoa(fps), "-i", filepath.Join(framesDir, "frame_%06d.jpg")}
	if revision.OutputKind == "image" {
		artifact, kind, mime = "main.png", "image", "image/png"
		args = append(args, "-frames:v", "1", filepath.Join(outputDir, artifact))
	} else {
		args = append(args, "-c:v", "libx264", "-pix_fmt", "yuv420p", "-preset", "medium", "-crf", "18", "-r", strconv.Itoa(fps), "-movflags", "+faststart", filepath.Join(outputDir, artifact))
	}
	encode := exec.CommandContext(ctx, ffmpegPath(), args...)
	encode.Stdout, encode.Stderr = logs, logs
	if err := encode.Run(); err != nil {
		return fmt.Errorf("web-canvas encode: %w", err)
	}
	manifest := map[string]any{"api_version": procedureJobVersion, "artifacts": []map[string]any{{"name": "main", "kind": kind, "path": artifact, "mime_type": mime}}}
	if kind == "video" {
		manifest["artifacts"].([]map[string]any)[0]["duration"] = float64(frames) / float64(fps)
	}
	encoded, _ := json.Marshal(manifest)
	return os.WriteFile(filepath.Join(outputDir, "result.json"), encoded, 0o600)
}

// handleProcedurePreview serves the web-canvas document for the panel's live
// preview. Inputs resolve to URLs the viewer's browser can load directly.
func (a *App) handleProcedurePreview(w http.ResponseWriter, r *http.Request, id int64) {
	ctx := requestAppCtx(r)
	var body struct {
		Revision    int                        `json:"revision"`
		Parameters  map[string]any             `json:"parameters"`
		Inputs      map[string]*ProcedureInput `json:"inputs"`
		Width       int                        `json:"width"`
		Height      int                        `json:"height"`
		FPS         int                        `json:"fps"`
		Duration    float64                    `json:"duration"`
		SourceStart float64                    `json:"source_start"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	_, revision, err := getProcedure(ctx.AppDB(), projectScope(ctx), id, body.Revision)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if revision.Runtime != webCanvasRuntime {
		http.Error(w, fmt.Sprintf("live preview needs a %s procedure; %s procedures preview after rendering", webCanvasRuntime, revision.Runtime), http.StatusConflict)
		return
	}
	if body.Width <= 0 || body.Height <= 0 {
		body.Width, body.Height = 1920, 1080
	}
	if body.FPS <= 0 {
		body.FPS = 30
	}
	assets := map[string]webCanvasAsset{}
	for name, input := range body.Inputs {
		if input == nil || strings.TrimSpace(input.Src) == "" {
			continue
		}
		url, resolveErr := resolveAssetURL(ctx, input.Src)
		if resolveErr != nil {
			http.Error(w, fmt.Sprintf("resolve input %q: %v", name, resolveErr), http.StatusBadRequest)
			return
		}
		assets[name] = webCanvasAsset{URL: url, Kind: input.Kind}
	}
	doc, err := buildWebCanvasDocument(revision, webCanvasEnv{
		Mode:       "preview",
		Canvas:     map[string]any{"width": body.Width, "height": body.Height, "pixel_aspect": 1, "color_space": "srgb"},
		Timeline:   procedureTimeline{Duration: body.Duration, SourceStart: body.SourceStart, FPS: body.FPS},
		Parameters: body.Parameters,
		Assets:     assets,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(doc))
}
