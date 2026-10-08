package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	tk "github.com/apteva/app-sdk/testkit"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPoseEngineSelectionAndModelIdentity(t *testing.T) {
	app := tk.NewAppCtx(t, "apteva.yaml")
	for _, v := range []struct{ input, want string }{{"", "mediapipe_full"}, {"legacy", "legacy"}, {"mediapipe_full", "mediapipe_full"}} {
		got, e := resolveSmartCropEngine(app, v.input)
		if e != nil || got != v.want {
			t.Fatalf("%q: %s %v", v.input, got, e)
		}
	}
	if _, e := resolveSmartCropEngine(app, "unknown"); e == nil {
		t.Fatal("unknown accepted")
	}
	for _, raw := range []string{`{"smart_crop_engine":1}`, `{"smart_crop_engine":"unknown"}`} {
		if validateSmartCropEngine([]byte(raw)) == nil {
			t.Fatal(raw)
		}
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(poseModel)); got != poseModelSHA256 {
		t.Fatal(got)
	}
	configured := tk.NewAppCtx(t, "apteva.yaml", tk.WithConfig(map[string]string{"smart_crop_engine": "legacy"}))
	if got, e := resolveSmartCropEngine(configured, ""); e != nil || got != "legacy" {
		t.Fatal(got, e)
	}
}
func TestPoseSamplePlannerCoverageAndFixedGeometry(t *testing.T) {
	samples := []poseSample{{AtMs: 0, Status: "fits_detected_upper_pose", Bounds: []float64{900, 350, 1280, 950}}, {AtMs: 500, Status: "fits_detected_upper_pose", Bounds: []float64{960, 300, 1330, 970}}}
	for i := range samples {
		samples[i].Crop.Width = 450
		samples[i].Crop.Height = 800
		samples[i].Crop.X = 850 + i*50
	}
	win, path, e := planPoseSamples(samples, 1920, 1080, 9, 16)
	if e != nil {
		t.Fatal(e)
	}
	a := &smartCropAudit{Effective: func() *smartCropAuditWindow { v := auditCropWindow(*win); return &v }(), Path: path, Coverage: "sampled_extent_fits"}
	for _, s := range samples {
		a.Extents = append(a.Extents, smartCropExtentEvidence{AtMs: s.AtMs, Bounds: auditCropWindow(poseBoundsWindow(s.Bounds, 1920, 1080))})
	}
	if !cropRetainsSampledExtents(a) {
		t.Fatalf("lost geometry: %+v %+v", win, path)
	}
	if path[0].Y != path[1].Y {
		t.Fatal("vertical jitter")
	}
	p := posePositions(smartCropTarget{StartMs: 0, EndMs: 600000}, 600000)
	if len(p) > 256 || p[0] != 0 || p[len(p)-1] >= 600000 {
		t.Fatal(p)
	}
	if _, _, e := planPoseSamples([]poseSample{{Status: "no_pose_detected"}}, 1920, 1080, 9, 16); e == nil {
		t.Fatal("unsupported pose accepted")
	}
}

// Private benchmark pixels remain outside the release. This invokes the real
// embedded runtime and production preprocessing/FFmpeg plan, never a mock pose.
func TestMediaPipeFullLocalIntegration(t *testing.T) {
	root := os.Getenv("MEDIAPIPE_FIXTURE_DIR")
	python := os.Getenv("MEDIAPIPE_TEST_PYTHON")
	if root == "" || python == "" {
		t.Skip("set MEDIAPIPE_FIXTURE_DIR and MEDIAPIPE_TEST_PYTHON")
	}
	raw, e := os.ReadFile(filepath.Join(root, "sources.json"))
	if e != nil {
		t.Fatal(e)
	}
	var images []struct{ Name, Path string }
	json.Unmarshal(raw, &images)
	raw, e = os.ReadFile(filepath.Join(root, "reels/manifest.json"))
	if e != nil {
		t.Fatal(e)
	}
	var reels []struct {
		Name string
		Path string `json:"clip_path"`
	}
	json.Unmarshal(raw, &reels)
	output := filepath.Join(root, "integrated-full")
	os.MkdirAll(output, 0700)
	app := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj), tk.WithConfig(map[string]string{"smart_crop_python": python}))
	sc := &storageClient{}
	results := []json.RawMessage{}
	run := func(name, source string, video bool) {
		t.Helper()
		probe, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "stream=width,height:format=duration", "-of", "json", source).Output()
		if err != nil {
			t.Fatal(err)
		}
		var meta struct {
			Streams []struct{ Width, Height int }
			Format  struct{ Duration string }
		}
		json.Unmarshal(probe, &meta)
		p := sampleImageProbe()
		p.Width = meta.Streams[0].Width
		p.Height = meta.Streams[0].Height
		p.IsImage = !video
		p.HasVideo = video
		op := "crop"
		params := map[string]any{"target_ratio": "9:16", "crop_mode": "smart", "output_width": 540}
		if video {
			var seconds float64
			fmt.Sscan(meta.Format.Duration, &seconds)
			p.DurationMs = int64(seconds * 1000)
			op = "extract_reel"
			params["start_ms"] = 0
			params["end_ms"] = p.DurationMs
		}
		upsertMedia(app.AppDB(), testProj, name, p, strings.Repeat("a", 64), "/", name)
		ctx := context.WithValue(context.Background(), renderSourcesKey{}, map[string]string{name: source})
		raw, _ := json.Marshal(params)
		resolved := preprocessSmartCrop(ctx, app, sc, testProj, op, []string{name}, raw)
		var parsed struct {
			Version string         `json:"crop_version"`
			Audit   smartCropAudit `json:"crop_diagnostics"`
		}
		json.Unmarshal(resolved, &parsed)
		if parsed.Version != "pose_full" || parsed.Audit.EffectiveEngine != "mediapipe_full" {
			t.Fatalf("%s fallback: %s", name, resolved)
		}
		if parsed.Audit.Coverage == "sampled_extent_fits" && !cropRetainsSampledExtents(&parsed.Audit) {
			t.Fatalf("%s false fit", name)
		}
		ext := ".png"
		if video {
			ext = ".mp4"
		}
		plan, err := buildPlan(op, []string{name}, resolved, name+ext, ext)
		if err != nil {
			t.Fatal(err)
		}
		args := append([]string{}, plan.Args...)
		for i, a := range args {
			if a == "{input}" {
				args[i] = source
			}
		}
		args = append(args, filepath.Join(output, name+ext))
		if out, err := exec.Command("ffmpeg", args...).CombinedOutput(); err != nil {
			t.Fatalf("render %s: %v %s", name, err, out)
		}
		os.WriteFile(filepath.Join(output, name+".json"), resolved, 0600)
		results = append(results, resolved)
		t.Logf("%s coverage=%s crop=%+v samples=%d", name, parsed.Audit.Coverage, parsed.Audit.Effective, len(parsed.Audit.Evidence))
	}
	for _, i := range images {
		run(i.Name, i.Path, false)
	}
	for _, r := range reels {
		run(r.Name, r.Path, true)
	}
	encoded, _ := json.MarshalIndent(results, "", "  ")
	os.WriteFile(filepath.Join(output, "results.json"), encoded, 0600)
}

func TestPoseSchemasAndFallbackCacheIsolation(t *testing.T) {
	for _, tool := range (&App{}).MCPTools() {
		if tool.Name == "media_crop" || tool.Name == "media_extract_frame" || tool.Name == "media_extract_reel" || tool.Name == "media_preview_crop" {
			props := tool.InputSchema["properties"].(map[string]any)
			if props["smart_crop_engine"] == nil {
				t.Fatal(tool.Name)
			}
		}
	}
	app := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	p := sampleImageProbe()
	upsertMedia(app.AppDB(), testProj, "1", p, strings.Repeat("a", 64), "/", "1.png")
	sc := &storageClient{}
	out := preprocessSmartCrop(context.Background(), app, sc, testProj, "crop", []string{"1"}, []byte(`{"target_ratio":"9:16"}`))
	var parsed struct {
		Audit smartCropAudit `json:"crop_diagnostics"`
	}
	json.Unmarshal(out, &parsed)
	if parsed.Audit.RequestedEngine != "mediapipe_full" || parsed.Audit.EffectiveEngine != "legacy" || len(parsed.Audit.Fallbacks) == 0 {
		t.Fatalf("hidden fallback: %s", out)
	}
	if !smartCropUsedEngineFallback(out) {
		t.Fatal("fallback result would be cached")
	}
	var count int
	app.AppDB().QueryRow("SELECT COUNT(*) FROM smartcrop_cache").Scan(&count)
	if count != 0 {
		t.Fatal("transient fallback cached")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, e := computeSmartCropPose(ctx, app, sc, testProj, "1", 9, 16, smartCropTarget{}); e == nil {
		t.Fatal("cancelled inference succeeded")
	}
}

func TestPoseFailureDiagnosticsDoNotExposePaths(t *testing.T) {
	if got := poseFailureReason(errors.New("open /private/customer/source: denied")); got != "pose_analysis_unavailable" {
		t.Fatal(got)
	}
	if got := poseFailureReason(context.DeadlineExceeded); got != "analysis_timeout" {
		t.Fatal(got)
	}
}
