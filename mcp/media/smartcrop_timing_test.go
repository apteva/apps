package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tk "github.com/apteva/app-sdk/testkit"
)

func TestPoseBudgetScalesWithSamplesAndPreservesCap(t *testing.T) {
	app := newTestCtx(t)
	p := sampleVideoProbe()
	p.DurationMs = 580851
	p.Width = 3840
	p.Height = 2160
	if err := upsertMedia(app.AppDB(), testProj, "95679", p, "sha", "/", "video.mov"); err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"start_ms":105767,"end_ms":140500,"smart_crop_engine":"hybrid"}`)
	positions := posePositions(smartCropFocus("extract_reel", map[string]any{"start_ms": float64(105767), "end_ms": float64(140500)}), p.DurationMs, p.FPS)
	if len(positions) != 71 {
		t.Fatal(len(positions))
	}
	timeout := smartCropTimeout(app, testProj, "extract_reel", "95679", raw)
	if timeout <= 120*time.Second || timeout > 900*time.Second || poseAnalysisBudget(71, false) <= 120*time.Second {
		t.Fatal(timeout)
	}
	capped := tk.NewAppCtx(t, "apteva.yaml", tk.WithConfig(map[string]string{"smart_crop_timeout_seconds": "45"}))
	if got := smartCropTimeout(capped, testProj, "crop", "missing", nil); got != 45*time.Second {
		t.Fatal(got)
	}
	if poseAnalysisBudget(256, true) != poseAnalysisBudget(100000, true) {
		t.Fatal("unbounded sample budget")
	}
}

func TestEmptyPoseEvidenceCannotSupportGeometry(t *testing.T) {
	if got := summarizePoseFailures(nil).Classification; got != "no_pose_evidence" {
		t.Fatal(got)
	}
	if got := summarizePoseFailures([]poseSample{{Status: "insufficient_head_or_shoulder_evidence"}}).Classification; got != "unresolved_evidence" {
		t.Fatal(got)
	}
	req := poseRequest{Positions: []int64{0, 500}}
	r := &poseResult{Runtime: "mediapipe-0.10.21", ModelSHA: poseModelSHA256, Samples: []poseSample{{AtMs: 0}}}
	if verifiedPartialPose(r, req) == nil {
		t.Fatal("valid partial discarded")
	}
	r.Samples[0].AtMs = 1
	if verifiedPartialPose(r, req) != nil {
		t.Fatal("wrong timestamp accepted")
	}
	r.Samples[0].AtMs = 0
	r.ModelSHA = "wrong"
	if verifiedPartialPose(r, req) != nil {
		t.Fatal("wrong model accepted")
	}
}

func TestPoseTimeoutPreservesPartialEvidenceAndStopsFallback(t *testing.T) {
	python := filepath.Join(t.TempDir(), "python")
	writeExecutable(t, python, `python3 - "$@" <<'PY'
import sys,json
r=json.load(open(sys.argv[-1]))
samples=[{'at_ms':at,'status':'fits_detected_upper_pose','required_bounds':[900,100,1100,700],'head_bounds_estimate':[900,100,1100,300],'crop':{'x':800,'y':0,'width':606,'height':1080}} for at in r['positions'][:2]]
print('APTEVA_POSE_ERROR:'+json.dumps({'code':'pose_analysis_timeout','at_ms':r['positions'][2],'partial_result':{'samples':samples,'runtime':'mediapipe-0.10.21','model_sha256':r['model_sha256'],'timings':{'frame_extraction_ms':123,'source_kind':'local_disk','failure_stage':'frame_extraction'}}}))
sys.exit(1)
PY`)
	app := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj), tk.WithConfig(map[string]string{"smart_crop_python": python}))
	p := sampleVideoProbe()
	p.DurationMs = 35000
	p.Width = 1920
	p.Height = 1080
	if err := upsertMedia(app.AppDB(), testProj, "1", p, strings.Repeat("a", 64), "/", "v.mp4"); err != nil {
		t.Fatal(err)
	}
	ctx := context.WithValue(context.Background(), renderSourcesKey{}, map[string]string{"1": "unused-source"})
	out := preprocessSmartCrop(ctx, app, &storageClient{}, testProj, "extract_reel", []string{"1"}, []byte(`{"target_ratio":"9:16","start_ms":0,"end_ms":34000,"smart_crop_engine":"mediapipe_full"}`))
	var resolved struct {
		RuntimeError string         `json:"runtime_error"`
		W            int            `json:"crop_w"`
		Audit        smartCropAudit `json:"crop_diagnostics"`
	}
	if err := json.Unmarshal(out, &resolved); err != nil {
		t.Fatal(err)
	}
	a := &resolved.Audit
	if resolved.RuntimeError != "analysis_timeout" || resolved.W != 0 || a.EffectiveEngine != "none" || a.PoseAttempt.Analysed != 2 || a.PoseAttempt.Requested <= 2 || a.PoseFailures.Classification != "incomplete_analysis" || a.Coverage != "unknown" || a.PoseAttempt.Timings.ExtractionMs != 123 {
		t.Fatalf("lost failure: %s", out)
	}
	if len(a.Fallbacks) != 1 {
		t.Fatalf("fallback continued: %s", out)
	}
	err := cropProcessingError(out)
	if err == nil || !strings.Contains(err.Error(), "partial") || !strings.Contains(err.Error(), "crop_diagnostics") {
		t.Fatal(err)
	}
	if _, err := applyCropCompositionPolicy(out); err == nil {
		t.Fatal("failed analysis can render")
	}
	var count int
	_ = app.AppDB().QueryRow("SELECT COUNT(*) FROM smartcrop_cache").Scan(&count)
	if count != 0 {
		t.Fatal("timeout cached")
	}
}

func TestPoseRemoteSourceCacheReusesVerifiedBytes(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl unavailable")
	}
	root := t.TempDir()
	// The remote cache helper targets Linux df; provide its output shape on
	// macOS while exercising actual curl, checksums, stamps and hardlinks.
	bin := filepath.Join(root, "bin")
	_ = os.MkdirAll(bin, 0700)
	writeExecutable(t, filepath.Join(bin, "df"), `printf 'Filesystem 1B-blocks Used Available Use%% Mounted on\nfixture 100000000000 0 100000000000 0%% /\n'`)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	source := filepath.Join(root, "source.mov")
	data := []byte(strings.Repeat("pose source bytes", 1024))
	_ = os.WriteFile(source, data, 0600)
	digest := fmt.Sprintf("%x", sha256.Sum256(data))
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python unavailable")
	}
	runtimeRoot := filepath.Join(root, "runtime")
	_ = os.MkdirAll(filepath.Join(runtimeRoot, "venv/bin"), 0700)
	_ = os.Symlink(python, filepath.Join(runtimeRoot, "venv/bin/python"))
	f := &StorageFile{ID: 1, Name: "source.mov", SizeBytes: int64(len(data)), SHA256: digest}
	req := poseRequest{Source: "file://" + source, SourceFile: f, SourceCacheMaxBytes: 100000, Positions: []int64{0, 500}}
	fakeRuntime := `import json,sys,hashlib
r=json.load(open(sys.argv[1]))
assert hashlib.sha256(open(r['source'],'rb').read()).hexdigest()=='` + digest + `'
print('APTEVA_POSE:verified')`
	run := func(work string) (string, error) {
		script := buildPoseRemoteScript(req, runtimeRoot, work, "")
		script = strings.ReplaceAll(script, remoteSourceCacheRoot, filepath.Join(root, "cache"))
		script = strings.ReplaceAll(script, base64.StdEncoding.EncodeToString([]byte(poseSetup)), base64.StdEncoding.EncodeToString([]byte("# provisioned test runtime")))
		script = strings.ReplaceAll(script, base64.StdEncoding.EncodeToString([]byte(poseRuntime)), base64.StdEncoding.EncodeToString([]byte(fakeRuntime)))
		b, e := exec.Command("bash", "-c", script).CombinedOutput()
		return string(b), e
	}
	cold, err := run(filepath.Join(root, "cold"))
	if err != nil || !strings.Contains(cold, "REMOTE_SOURCE_CACHE_MISS") {
		t.Fatalf("cold: %v %s", err, cold)
	}
	_ = os.Remove(source) // Warm inference must succeed without source transport.
	warm, err := run(filepath.Join(root, "warm"))
	if err != nil || !strings.Contains(warm, "REMOTE_SOURCE_CACHE_HIT") || !strings.Contains(warm, "APTEVA_POSE:verified") {
		t.Fatalf("warm: %v %s", err, warm)
	}
	// Tampering invalidates the entry; unavailable transport must fail preparation.
	cache := filepath.Join(root, "cache", remoteSourceCacheName("1", f.Name, f.SHA256, f.SizeBytes))
	_ = os.WriteFile(cache, []byte("wrong bytes"), 0600)
	bad, err := run(filepath.Join(root, "invalid"))
	if err == nil || !strings.Contains(bad, "pose_source_preparation_failed") || strings.Contains(bad, "APTEVA_POSE:verified") {
		t.Fatalf("tampered: %v %s", err, bad)
	}
}

func TestPoseRemoteProductionProofScript(t *testing.T) {
	input, output := os.Getenv("MEDIA_POSE_PROOF_INPUT"), os.Getenv("MEDIA_POSE_PROOF_SCRIPT")
	if input == "" || output == "" {
		t.Skip("private remote proof input/script required")
	}
	var in struct {
		Source         string
		File           StorageFile
		Hybrid         bool
		Width, Height  int
		DurationMs     int64
		FPS            float64
		StartMs, EndMs int64
	}
	b, err := os.ReadFile(input)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(b, &in); err != nil {
		t.Fatal(err)
	}
	positions := posePositions(smartCropTarget{StartMs: in.StartMs, EndMs: in.EndMs}, in.DurationMs, in.FPS)
	root := poseRemoteRuntimeRoot(in.Hybrid)
	setupMode := ""
	if in.Hybrid {
		setupMode = " hybrid"
	}
	req := poseRequest{Hybrid: in.Hybrid, Source: in.Source, SourceFile: &in.File, SourceCacheMaxBytes: remoteSourceCacheDefaultMaxBytes, Framing: "upper_body", Positions: positions, Model: root + "/model.task", ModelSHA: poseModelSHA256, RecoveryRoot: root, FFmpeg: "/root/.apteva-render/ffmpeg-btbn-n7.1/bin/ffmpeg", Width: in.Width, Height: in.Height, RatioW: 9, RatioH: 16, Video: true, Remaining: poseAnalysisBudget(len(positions), in.Hybrid).Seconds(), ExpiresAt: float64(time.Now().Add(10 * time.Minute).Unix())}
	if err = os.WriteFile(output, []byte(buildPoseRemoteScript(req, root, uniqueRemoteWorkDir(0)+"-pose-proof", setupMode)), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestIdenticalCropPreviewsShareOneAnalysisAndReportCacheHits(t *testing.T) {
	root := t.TempDir()
	python := filepath.Join(root, "python")
	calls := filepath.Join(root, "calls")
	t.Setenv("POSE_CACHE_TEST_CALLS", calls)
	writeExecutable(t, python, `python3 - "$@" <<'PY'
import sys,json,os,time
r=json.load(open(sys.argv[-1]));open(os.environ['POSE_CACHE_TEST_CALLS'],'a').write('call\n');time.sleep(.1)
samples=[{'at_ms':at,'status':'fits_detected_upper_pose','required_bounds':[900,100,1100,700],'head_bounds_estimate':[900,100,1100,300],'crop':{'x':800,'y':0,'width':450,'height':800}} for at in r['positions']]
print('APTEVA_POSE:'+json.dumps({'samples':samples,'runtime':'mediapipe-0.10.21','model_sha256':r['model_sha256']}))
PY`)
	app := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj), tk.WithConfig(map[string]string{"smart_crop_python": python}))
	p := sampleVideoProbe()
	p.DurationMs = 35000
	p.Width = 1920
	p.Height = 1080
	_ = upsertMedia(app.AppDB(), testProj, "1", p, strings.Repeat("a", 64), "/", "v.mp4")
	params := []byte(`{"target_ratio":"9:16","start_ms":0,"end_ms":2000,"smart_crop_engine":"mediapipe_full"}`)
	type result struct {
		out []byte
		hit bool
	}
	results := make(chan result, 8)
	for i := 0; i < 8; i++ {
		go func() {
			state := &cropPlanCacheOutcome{}
			ctx := context.WithValue(context.Background(), renderSourcesKey{}, map[string]string{"1": "unused-source"})
			ctx = context.WithValue(ctx, cropPlanCacheKey{}, state)
			results <- result{preprocessSmartCrop(ctx, app, &storageClient{}, testProj, "extract_reel", []string{"1"}, params), state.Hit}
		}()
	}
	hits := 0
	for i := 0; i < 8; i++ {
		r := <-results
		if r.hit {
			hits++
		}
		var p map[string]any
		_ = json.Unmarshal(r.out, &p)
		if p["crop_version"] != "pose_full" || p["runtime_error"] != nil {
			t.Fatalf("preview failed: %s", r.out)
		}
	}
	b, _ := os.ReadFile(calls)
	if strings.Count(string(b), "call") != 1 || hits != 7 {
		t.Fatalf("calls=%s hits=%d", b, hits)
	}
	// A new index thumbnail does not change an exact-source ML plan.
	_ = upsertDerivation(app.AppDB(), testProj, "1", "thumbnail", 99, 320, 180, 0)
	ctx := context.WithValue(context.Background(), renderSourcesKey{}, map[string]string{"1": "unused-source"})
	state := &cropPlanCacheOutcome{}
	ctx = context.WithValue(ctx, cropPlanCacheKey{}, state)
	_ = preprocessSmartCrop(ctx, app, &storageClient{}, testProj, "extract_reel", []string{"1"}, params)
	if !state.Hit {
		t.Fatal("unrelated thumbnail invalidated native pose plan")
	}
	// A changed source identity must never reuse the earlier plan.
	_ = upsertMedia(app.AppDB(), testProj, "1", p, strings.Repeat("b", 64), "/", "v.mp4")
	ctx = context.WithValue(context.Background(), renderSourcesKey{}, map[string]string{"1": "unused-source"})
	_ = preprocessSmartCrop(ctx, app, &storageClient{}, testProj, "extract_reel", []string{"1"}, params)
	b, _ = os.ReadFile(calls)
	if strings.Count(string(b), "call") != 2 {
		t.Fatal("changed source reused cache")
	}
	// A render already holding both host units must not deadlock against an
	// identical preview queued for admission. They share the resulting plan.
	deadline, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	heavy, release, err := acquireMediaWork(deadline, app, 2)
	if err != nil {
		t.Fatal(err)
	}
	fresh := []byte(`{"target_ratio":"9:16","start_ms":2000,"end_ms":4000,"smart_crop_engine":"mediapipe_full"}`)
	queued := make(chan []byte, 1)
	go func() {
		queuedCtx := context.WithValue(deadline, renderSourcesKey{}, map[string]string{"1": "unused-source"})
		queued <- preprocessSmartCrop(queuedCtx, app, &storageClient{}, testProj, "extract_reel", []string{"1"}, fresh)
	}()
	time.Sleep(30 * time.Millisecond)
	heavy = context.WithValue(heavy, renderSourcesKey{}, map[string]string{"1": "unused-source"})
	nested := preprocessSmartCrop(heavy, app, &storageClient{}, testProj, "extract_reel", []string{"1"}, fresh)
	release()
	if strings.Contains(string(nested), "runtime_error") {
		t.Fatalf("admission/plan-gate deadlock: %s", nested)
	}
	if out := <-queued; strings.Contains(string(out), "runtime_error") {
		t.Fatalf("queued preview failed: %s", out)
	}
	b, _ = os.ReadFile(calls)
	if strings.Count(string(b), "call") != 3 {
		t.Fatalf("nested render and preview duplicated analysis: %s", b)
	}
}

func TestRemotePoseSetupKillRemainsResourceFailure(t *testing.T) {
	root := t.TempDir()
	script := buildPoseRemoteScript(poseRequest{Source: "source", Positions: []int64{0}}, root, filepath.Join(root, "work"), "")
	script = strings.ReplaceAll(script, base64.StdEncoding.EncodeToString([]byte(poseSetup)), base64.StdEncoding.EncodeToString([]byte("import os; os._exit(137)")))
	b, err := exec.Command("bash", "-c", script).CombinedOutput()
	if err == nil || !strings.Contains(string(b), `"code":"media_resource_exhausted"`) {
		t.Fatalf("kill hidden: %v %s", err, b)
	}
	if resourceFailure(err, string(b), 137) == nil {
		t.Fatal("resource failure lost")
	}
}

func TestPoseProductionIntervalEvidencePlans(t *testing.T) {
	root := os.Getenv("MEDIA_POSE_INTERVAL_PROOF_DIR")
	if root == "" {
		t.Skip("private interval evidence required")
	}
	for _, engine := range []string{"full", "hybrid"} {
		t.Run(engine, func(t *testing.T) {
			b, err := os.ReadFile(filepath.Join(root, "candidate-"+engine+"-native-pose.json"))
			if err != nil {
				t.Fatal(err)
			}
			var r poseResult
			if err = json.Unmarshal(b, &r); err != nil {
				t.Fatal(err)
			}
			if len(r.Samples) != 71 || r.ModelSHA != poseModelSHA256 || r.Runtime != "mediapipe-0.10.21" {
				t.Fatal("incomplete evidence")
			}
			win, path, err := planPoseSamples(r.Samples, 3840, 2160, 9, 16)
			if engine == "hybrid" {
				var gaps []posePathGap
				win, path, gaps, err = planHybridPoseSamples(r.Samples, 3840, 2160, 9, 16)
				if len(gaps) > 0 {
					t.Fatal(gaps)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			crop := auditCropWindow(*win)
			audit := &smartCropAudit{Effective: &crop, Path: path, Coverage: "sampled_extent_fits"}
			for _, s := range r.Samples {
				if s.Status != "fits_detected_upper_pose" {
					t.Fatal(s.Status)
				}
				audit.Extents = append(audit.Extents, smartCropExtentEvidence{AtMs: s.AtMs, Bounds: auditCropWindow(poseBoundsWindow(s.Bounds, 3840, 2160))})
			}
			if !cropRetainsSampledExtents(audit) {
				t.Fatal("planned crop loses sampled native extent")
			}
			out, _ := json.MarshalIndent(map[string]any{"crop": crop, "path": path, "retains_sampled_extents": true, "sample_count": 71}, "", "  ")
			if err = os.WriteFile(filepath.Join(root, "candidate-"+engine+"-crop-plan.json"), out, 0600); err != nil {
				t.Fatal(err)
			}
		})
	}
}
