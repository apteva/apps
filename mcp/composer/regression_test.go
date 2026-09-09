package main

import (
	"context"
	"encoding/json"
	"image"
	"image/color"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
	"golang.org/x/image/font"
)

const auditEdit = `{"timeline":{"background":"#000000","tracks":[{"type":"visual","clips":[{"asset":{"type":"image","src":"https://example.com/image.png"},"start":0,"length":2}]}]}}`
const auditOutput = `{"format":"mp4","resolution":"sd","aspect":"16:9","fps":24}`

func auditInsert(t *testing.T, ctx *sdk.AppCtx) int64 {
	t.Helper()
	result, err := ctx.AppDB().Exec(`INSERT INTO compositions(project_id,name,edit_json,output_json,duration_seconds) VALUES(?,?,?,?,?)`, "owner", "Audit", auditEdit, auditOutput, 2)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := result.LastInsertId()
	return id
}
func TestAuditProjectIsolation(t *testing.T) {
	for _, operation := range []string{"get", "update", "delete", "render", "render_status"} {
		t.Run(operation, func(t *testing.T) {
			ctx := tk.NewAppCtx(t, "apteva.yaml").WithProject("other")
			id := auditInsert(t, ctx)
			app := &App{}
			var err error
			switch operation {
			case "get":
				_, err = app.toolCompositionGet(ctx, map[string]any{"id": id})
			case "update":
				_, err = app.toolCompositionUpdate(ctx, map[string]any{"id": id, "patch": map[string]any{"name": "Changed by another project"}})
			case "delete":
				_, err = app.toolCompositionDelete(ctx, map[string]any{"id": id})
			case "render":
				_, err = app.toolCompositionRender(ctx, map[string]any{"id": id, "wait": false})
			case "render_status":
				rid, e := createRenderRow(ctx, id, "owner", "auto", auditEdit, auditOutput, "queued", "queued")
				if e != nil {
					t.Fatal(e)
				}
				_, err = app.toolRenderStatus(ctx, map[string]any{"render_id": rid})
			}
			if err == nil {
				t.Fatalf("%s accepted another project's ID", operation)
			}
		})
	}
}
func TestAuditMaterializationPreservesNewerEdits(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml").WithProject("owner")
	id := auditInsert(t, ctx)
	snapshot, err := parseEditJSON(auditEdit)
	if err != nil {
		t.Fatal(err)
	}
	newer := strings.Replace(auditEdit, "#000000", "#ff0000", 1)
	if _, err = ctx.AppDB().Exec(`UPDATE compositions SET edit_json=? WHERE id=?`, newer, id); err != nil {
		t.Fatal(err)
	}
	if _, err = materializeAIAssets(ctx, snapshot, id, "owner", true); err != nil {
		t.Fatal(err)
	}
	var stored string
	if err = ctx.AppDB().QueryRow(`SELECT edit_json FROM compositions WHERE id=?`, id).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stored, "#ff0000") {
		t.Fatalf("queued snapshot overwrote newer edit: %s", stored)
	}
}
func TestAuditBrowserPreservesHTTPAssets(t *testing.T) {
	url := "https://example.com/image.png"
	spec := &V2Composition{Assets: []V2Asset{{ID: "photo", Type: "image", Src: url}}}
	got, err := browserResolvedSpec(nil, spec)
	if err != nil {
		t.Fatal(err)
	}
	if got.Assets[0].Src != url {
		t.Fatalf("remote asset rewritten to %q", got.Assets[0].Src)
	}
}
func auditShapeSpec() *V2Composition {
	return &V2Composition{Version: composerV2Version, Output: V2Output{Width: 64, Height: 64, FPS: 24, Background: "#000000"}, Scenes: []V2Scene{{Duration: 1, Elements: []V2Element{{Type: "shape", Width: 64.0, Height: 64.0, Style: map[string]any{"fill": "#000000"}}}}}}
}
func TestAuditNativePreservesVisualTracks(t *testing.T) {
	spec := auditShapeSpec()
	spec.Tracks = []V2Track{{Type: "image", Clips: []V2Clip{{Type: "image", Asset: "photo", Duration: 1}}}}
	spec.Assets = []V2Asset{{ID: "photo", Type: "image", Src: "https://example.com/image.png"}}
	if err := validateV2Composition(spec); err != nil {
		return // Explicit rejection prevents silent content loss.
	}
	if !v2UseDirectRenderer(spec) {
		return
	}
	white := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			white.Set(x, y, color.White)
		}
	}
	r := &v2NativeRender{spec: spec, width: 64, height: 64, designW: 64, designH: 64, scaleX: 1, scaleY: 1, scale: 1, images: map[string]image.Image{"photo": white}, assets: map[string]V2Asset{"photo": spec.Assets[0]}, faces: map[string]font.Face{}}
	red, _, _, _ := r.renderFrame(.5).At(32, 32).RGBA()
	if red == 0 {
		t.Fatal("accepted image track is absent from native output")
	}
}
func TestAuditNativeRejectsUnsupportedComponent(t *testing.T) {
	spec := auditShapeSpec()
	spec.Scenes[0].Elements = []V2Element{{Type: "component", Component: "pill", Text: "Visible label"}}
	raw, _ := json.Marshal(spec)
	validation := validateCompositionJSON(string(raw))
	if validation.Valid && validation.Renderer == "native-v2" {
		t.Fatal("component validates for native-v2, whose drawElement has no component case")
	}
}
func TestAuditKeyframesHoldFinalValue(t *testing.T) {
	anim := map[string]any{"x": []any{map[string]any{"start": 0.0, "duration": 1.0, "from": 0.0, "to": 100.0}}}
	if x := applyKeyframe(anim, "x", 1.1, 0); x != 100 {
		t.Fatalf("animation snapped back to %g after ending, want 100", x)
	}
}
func TestAuditNativeChecksCancelledContextBeforeFrames(t *testing.T) {
	app := tk.NewAppCtx(t, "apteva.yaml")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	progress := 0
	ctx = withRenderProgress(ctx, func(RenderProgress) { progress++ })
	result, _, err := renderV2Native(ctx, app, auditShapeSpec(), "owner")
	if result.Cleanup != nil {
		defer result.Cleanup()
	}
	if err == nil {
		t.Fatal("cancelled render succeeded")
	}
	if progress > 0 {
		t.Fatalf("cancelled render still generated all frames (%d progress callbacks)", progress)
	}
}

func BenchmarkAuditSmallShapeAt1080p(b *testing.B) {
	r := &v2NativeRender{scale: 1}
	dst := image.NewRGBA(image.Rect(0, 0, 1920, 1080))
	el := V2Element{Type: "shape", Style: map[string]any{"fill": "#ff0000"}}
	box := image.Rect(20, 20, 52, 52)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r.drawShape(dst, el, box, 1)
	}
}
func TestAuditV2ExplicitZeroVolume(t *testing.T) {
	spec := auditShapeSpec()
	spec.Audio = []V2Audio{{Src: "https://example.com/audio.mp3", Duration: 1, Volume: 0}}
	track, _, err := v2AudioTrack(spec, map[string]V2Asset{})
	if err != nil {
		t.Fatal(err)
	}
	if track.Clips[0].Volume != 0 {
		t.Fatalf("explicit silence becomes volume %g", track.Clips[0].Volume)
	}
}

func TestAuditRemoteSilentVideo(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "silent.mp4")
	if out, err := exec.Command(ffmpegPath(), "-y", "-v", "error", "-f", "lavfi", "-i", "color=c=red:s=64x64:d=0.5:r=24", "-an", source).CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v %s", err, out)
	}
	edit, err := parseEditJSON(`{"timeline":{"tracks":[{"type":"visual","clips":[{"asset":{"type":"video","src":"https://example.com/silent.mp4"},"length":0.5}]}]}}`)
	if err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("bash", "-c", remoteEnsureAudioScript(source)).CombinedOutput(); err != nil {
		t.Fatalf("remote audio preparation: %v %s", err, out)
	}
	args := buildLocalFFmpegArgsWithAudioInfo(edit, Output{Format: "mp4", Resolution: "sd", Aspect: "16:9", FPS: 24}, []string{source}, -1, filepath.Join(dir, "out.mp4"), remoteVisualAudioDefaults(edit))
	if out, err := exec.Command(ffmpegPath(), args...).CombinedOutput(); err != nil {
		t.Fatalf("remote command fails for video without audio: %v %s", err, out)
	}
}
func TestAuditNoSuccessWhenOutputCannotBePersisted(t *testing.T) {
	app := tk.NewAppCtx(t, "apteva.yaml").WithProject("owner")
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DB_PATH", filepath.Join(blocked, "db.sqlite"))
	raw, _ := json.Marshal(auditShapeSpec())
	res, err := app.AppDB().Exec(`INSERT INTO compositions(project_id,name,edit_json,output_json,duration_seconds) VALUES(?,?,?,?,?)`, "owner", "Persistence audit", string(raw), auditOutput, 1)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	got, err := (&App{}).toolCompositionRender(app, map[string]any{"id": id, "wait": true})
	if err == nil && got.(map[string]any)["status"] == "complete" {
		t.Fatalf("reported complete after failed Storage upload AND failed cache write: %#v", got)
	}
}

type auditAsyncPlatform struct {
	tk.BasePlatformClient
	freshJobs int
}

func (p *auditAsyncPlatform) CallAppResult(app, tool string, input map[string]any, out any) error {
	// Media Studio skips both completed and pending cache lookups for refresh.
	if input["cache_policy"] == "refresh" {
		p.freshJobs++
	}
	raw, _ := json.Marshal(map[string]any{"_meta": map[string]any{"status": "queued", "job_id": p.freshJobs}})
	return json.Unmarshal(raw, out)
}
func TestAuditAsyncRefreshDoesNotStartNewJobOnRetry(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/apps/callback/apps/media-studio/proxy/video-jobs/1" || r.URL.Query().Get("project_id") != "owner" || r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("wrong poll scope or authentication")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"job":{"id":1,"status":"polling"}}`))
	}))
	defer server.Close()
	t.Setenv("APTEVA_GATEWAY_URL", server.URL)
	t.Setenv("APTEVA_OUTBOUND_TOKEN", "test-token")
	platform := &auditAsyncPlatform{}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithPlatform(platform))
	ai := &AIAsset{MediaKind: "video", Prompt: "Audit fixture", CachePolicy: "refresh"}
	for i := 0; i < 3; i++ {
		if _, _, err := materializeOneAIAsset(ctx, ai, "clip", "owner", nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	if platform.freshJobs != 1 {
		t.Fatalf("three retries requested %d fresh paid jobs; job_id is stored but not used to poll", platform.freshJobs)
	}
}

func TestAuditRejectsExecutableOutputFormat(t *testing.T) {
	edit, err := parseEditJSON(auditEdit)
	if err != nil {
		t.Fatal(err)
	}
	// Only creates a marker inside this test's temporary directory.
	dir := t.TempDir()
	marker := filepath.Join(dir, "audit-marker")
	format := "mp4$(touch audit-marker).mp4"
	if err := validateEditOutput(edit, Output{Format: format, Resolution: "sd", Aspect: "16:9", FPS: 24}); err != nil {
		return
	}
	// Verify FFmpeg can produce the quoted filename before the vulnerable statement runs.
	render := exec.Command(ffmpegPath(), "-y", "-v", "error", "-f", "lavfi", "-i", "color=c=red:s=64x64:d=0.1", "./out."+format)
	render.Dir = dir
	if output, err := render.CombinedOutput(); err != nil {
		t.Fatalf("fixture render: %v %s", err, output)
	}
	// Isolate the first post-render shell statement from the exact generated script.
	script := remoteRenderScript(nil, "true", format, "owner", "https://example.com", "fake-audit-token", "out.mp4", "video/mp4", nil)
	start := strings.Index(script, "BYTES=$(")
	end := strings.Index(script[start:], "\n")
	line := script[start : start+end]
	command := exec.Command("bash", "-c", line)
	command.Dir = dir
	_ = command.Run()
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("accepted output.format executed shell substitution in generated remote script")
	}
}
