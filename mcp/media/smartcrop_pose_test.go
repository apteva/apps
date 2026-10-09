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
	for _, v := range []struct{ input, want string }{{"", "hybrid"}, {"hybrid", "hybrid"}, {"legacy", "legacy"}, {"mediapipe_full", "mediapipe_full"}} {
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
	p := posePositions(smartCropTarget{StartMs: 0, EndMs: 600000}, 600000, 30)
	if len(p) > 256 || p[0] != 0 || p[len(p)-1] >= 600000 {
		t.Fatal(p)
	}
	for _, v := range []struct {
		fps  float64
		last int64
	}{{60, 33549}, {30, 33516}, {0, 33503}} {
		end := posePositions(smartCropTarget{StartMs: 0, EndMs: 33583}, 33583, v.fps)
		if end[len(end)-1] != v.last {
			t.Fatalf("end-of-source sampling: fps=%v positions=%v", v.fps, end)
		}
	}
	mid := posePositions(smartCropTarget{StartMs: 1000, EndMs: 2000}, 33583, 30)
	if mid[len(mid)-1] != 1999 {
		t.Fatal("mid-source endpoint moved", mid)
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
	engine := os.Getenv("MEDIAPIPE_TEST_ENGINE")
	if engine == "" {
		engine = "mediapipe_full"
	}
	outputName := "integrated-full"
	if engine == "hybrid" {
		outputName = "integrated-hybrid"
	}
	output := filepath.Join(root, outputName)
	os.MkdirAll(output, 0700)
	app := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj), tk.WithConfig(map[string]string{"smart_crop_python": python}))
	sc := &storageClient{}
	results := []json.RawMessage{}
	run := func(t *testing.T, name, source string, video bool) {
		t.Helper()
		probe, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "stream=width,height,r_frame_rate:format=duration", "-of", "json", source).Output()
		if err != nil {
			t.Fatal(err)
		}
		var meta struct {
			Streams []struct {
				Width, Height int
				FrameRate     string `json:"r_frame_rate"`
			}
			Format struct{ Duration string }
		}
		json.Unmarshal(probe, &meta)
		p := sampleImageProbe()
		p.Width = meta.Streams[0].Width
		p.Height = meta.Streams[0].Height
		p.IsImage = !video
		p.HasVideo = video
		op := "crop"
		params := map[string]any{"target_ratio": "9:16", "crop_mode": "smart", "output_width": 540, "smart_crop_engine": engine}
		if video {
			var seconds float64
			fmt.Sscan(meta.Format.Duration, &seconds)
			p.DurationMs = int64(seconds * 1000)
			p.FPS = parseRational(meta.Streams[0].FrameRate)
			op = "extract_reel"
			params["start_ms"] = 0
			params["end_ms"] = p.DurationMs
		}
		upsertMedia(app.AppDB(), testProj, name, p, strings.Repeat("a", 64), "/", name)
		ctx := context.WithValue(context.Background(), renderSourcesKey{}, map[string]string{name: source})
		ctx = context.WithValue(ctx, poseRecoveryModelsKey{}, os.Getenv("MEDIAPIPE_RECOVERY_MODEL_DIR"))
		raw, _ := json.Marshal(params)
		resolved := preprocessSmartCrop(ctx, app, sc, testProj, op, []string{name}, raw)
		var parsed struct {
			Version string         `json:"crop_version"`
			Audit   smartCropAudit `json:"crop_diagnostics"`
		}
		json.Unmarshal(resolved, &parsed)
		version := "pose_full"
		if engine == "hybrid" {
			version = "pose_hybrid"
		}
		if parsed.Version != version || parsed.Audit.EffectiveEngine != engine {
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
		t.Run(i.Name, func(t *testing.T) { run(t, i.Name, i.Path, false) })
	}
	for _, r := range reels {
		t.Run(r.Name, func(t *testing.T) { run(t, r.Name, r.Path, true) })
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
	if parsed.Audit.RequestedEngine != "hybrid" || parsed.Audit.EffectiveEngine != "legacy" || len(parsed.Audit.Fallbacks) == 0 {
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
	if _, _, e := computeSmartCropPose(ctx, app, sc, testProj, "1", 9, 16, smartCropTarget{}, "upper_body"); e == nil {
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

func TestPoseRuntimeFailureProtocol(t *testing.T) {
	failure := parsePoseRuntimeFailure("decoder log\nAPTEVA_POSE_ERROR:{\"code\":\"pose_source_frame_unavailable\",\"at_ms\":343000,\"attempts\":3,\"source\":\"https://private.invalid/token\"}\n")
	if failure == nil || failure.AtMs == nil || *failure.AtMs != 343000 || failure.Attempts != 3 || poseFailureReason(failure) != "pose_source_frame_unavailable" {
		t.Fatalf("%+v", failure)
	}
	raw, _ := json.Marshal(failure)
	if strings.Contains(string(raw), "private") {
		t.Fatal(string(raw))
	}
	for _, output := range []string{
		`APTEVA_POSE_ERROR:{"code":"private/customer/path"}`,
		`APTEVA_POSE_ERROR:{"code":"pose_source_frame_unavailable","attempts":4}`,
		`APTEVA_POSE_ERROR:{"code":"pose_source_frame_unavailable","at_ms":-1}`,
		`APTEVA_POSE_ERROR:invalid`,
	} {
		if parsePoseRuntimeFailure(output) != nil {
			t.Fatal(output)
		}
	}
}

func TestPoseFrameReadRetryRuntime(t *testing.T) {
	// Execute the embedded extraction code with deterministic decoder failures.
	// AST selection avoids requiring MediaPipe just to exercise transport retries.
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	script := `import ast,os,time,subprocess,tempfile
from unittest.mock import patch
source=ast.parse(open('smartcrop_pose_runtime.py').read())
selected=[n for n in source.body if isinstance(n,(ast.FunctionDef,ast.ClassDef)) and n.name in ('extract_frame','PoseRuntimeFailure')]
exec(compile(ast.Module(body=selected,type_ignores=[]),'embedded-runtime','exec'))
req={'ffmpeg':'ffmpeg','source':'private-source'}
with tempfile.TemporaryDirectory() as work:
 path=os.path.join(work,'frame.png');open(path,'wb').write(b'old-frame')
 calls=[]
 def decoder(args,**kw):
  assert not os.path.exists(path),'stale frame reused'
  assert 0<kw['timeout']<=15
  calls.append(args)
  if len(calls)==1:
   open(path,'wb').write(b'partial-frame')
   return subprocess.CompletedProcess(args,1)
  if len(calls)==2:return subprocess.CompletedProcess(args,0) # Empty output is failure.
  open(path,'wb').write(b'fresh-frame')
  return subprocess.CompletedProcess(args,0)
 with patch('subprocess.run',decoder),patch('time.sleep'):
  assert extract_frame(req,343000,path,time.monotonic()+60)==3
  assert open(path,'rb').read()==b'fresh-frame'
 with patch('subprocess.run',return_value=subprocess.CompletedProcess([],1)) as run,patch('time.sleep'):
  try:extract_frame(req,343500,path,time.monotonic()+60)
  except PoseRuntimeFailure as e:assert e.code=='pose_source_frame_unavailable' and e.at_ms==343500 and e.attempts==3
  else:raise AssertionError('exhaustion accepted')
  assert run.call_count==3
 with patch('subprocess.run') as run:
  try:extract_frame(req,344000,path,time.monotonic()-1)
  except PoseRuntimeFailure as e:assert e.code=='pose_source_read_timeout' and e.attempts==0
  else:raise AssertionError('deadline ignored')
  run.assert_not_called()
 with patch('subprocess.run',side_effect=subprocess.TimeoutExpired('ffmpeg',15)) as run,patch('time.sleep'):
  try:extract_frame(req,344500,path,time.monotonic()+60)
  except PoseRuntimeFailure as e:assert e.attempts==3
  else:raise AssertionError('decoder timeout ignored')
  assert run.call_count==3
print('bounded retries, fresh-frame identity, empty outputs, exhaustion and deadlines verified')
`
	if out, err := exec.Command(python, "-c", script).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
}

func TestPoseAttemptDiagnosticsSurvivePlanningFailure(t *testing.T) {
	a := &smartCropAudit{SourceWidth: 1920, SourceHeight: 1080, EffectiveEngine: "legacy", RequestedEngine: "hybrid"}
	ctx := context.WithValue(context.Background(), smartCropAuditKey{}, a)
	valid := poseSample{AtMs: 500, Status: "uncertain_hand_evidence", Bounds: []float64{900, 300, 1200, 900}, Landmarks: []poseLandmarkEvidence{{Index: 15, Visibility: .2, Presence: .9}}}
	valid.Crop.Width = 378
	valid.Crop.Height = 672
	r := &poseResult{Samples: []poseSample{{AtMs: 0, Status: "no_pose_detected"}, valid}}
	recordPoseAttempt(ctx, r, "upper_body")
	if _, _, err := planPoseSamples(r.Samples, 1920, 1080, 9, 16); err == nil {
		t.Fatal("incomplete pose accepted")
	}
	a.AlgorithmVersion = legacySmartCropAlgorithmVersion
	out := attachSmartCropAudit([]byte(`{"crop_w":606,"crop_h":1080,"crop_x":656,"crop_y":0}`), a)
	var decoded struct {
		Audit smartCropAudit `json:"crop_diagnostics"`
	}
	json.Unmarshal(out, &decoded)
	got := &decoded.Audit
	if got.PoseAttempt == nil || got.PoseAttempt.Valid != 1 || got.PoseAttempt.Invalid != 1 || got.PoseAttempt.Uncertain != 1 || len(got.PoseAttempt.Failed) != 2 || got.PoseAttempt.Failed[0].AtMs != 0 || len(got.PoseSamples) != 2 || got.PoseSamples[1].Landmarks[0].Visibility != .2 || got.AlgorithmVersion != legacySmartCropAlgorithmVersion || got.PoseAttempt.Algorithm != smartCropAlgorithmVersion {
		t.Fatalf("lost attempted evidence: %s", out)
	}
}

func TestPoseFramingSelectionAndExpansion(t *testing.T) {
	app := tk.NewAppCtx(t, "apteva.yaml", tk.WithConfig(map[string]string{"smart_crop_framing": "widest_valid"}))
	if got, err := resolveSmartCropFraming(app, ""); err != nil || got != "widest_valid" {
		t.Fatal(got, err)
	}
	if got, err := resolveSmartCropFraming(nil, ""); err != nil || got != "upper_body" {
		t.Fatal(got, err)
	}
	for _, raw := range []string{`{"smart_crop_framing":123}`, `{"smart_crop_framing":"bad"}`} {
		if validateSmartCropEngine([]byte(raw)) == nil {
			t.Fatal(raw)
		}
	}
	a := &smartCropAudit{SourceWidth: 1920, SourceHeight: 1080, Coverage: "unknown"}
	raw := []byte(`{"crop_x":795,"crop_y":378,"crop_w":378,"crop_h":672,"crop_path":[{"at_ms":0,"x":795,"y":378},{"at_ms":500,"x":1000,"y":378}]}`)
	out := expandSmartCropFraming(raw, a, 9, 16)
	var p struct {
		X    int             `json:"crop_x"`
		Y    int             `json:"crop_y"`
		W    int             `json:"crop_w"`
		H    int             `json:"crop_h"`
		Path []cropPathPoint `json:"crop_path"`
	}
	json.Unmarshal(out, &p)
	if p.W != 606 || p.H != 1080 || p.Y != 0 || p.X > 795 || p.X+p.W < 795+378 || p.Path[1].X > 1000 || p.Path[1].X+p.W < 1378 || a.Coverage != "unknown" {
		t.Fatalf("expansion lost pixels or relaxed guard: %s", out)
	}
	for _, tool := range (&App{}).MCPTools() {
		if tool.Name == "media_crop" || tool.Name == "media_extract_frame" || tool.Name == "media_extract_reel" || tool.Name == "media_preview_crop" {
			if tool.InputSchema["properties"].(map[string]any)["smart_crop_framing"] == nil {
				t.Fatal(tool.Name)
			}
		}
	}
}

func TestPoseSameFrameHandRefreshGuards(t *testing.T) {
	python := os.Getenv("MEDIAPIPE_TEST_PYTHON")
	if python == "" {
		python = "python3"
	}
	if err := exec.Command(python, "-c", "import numpy").Run(); err != nil {
		t.Skip("numpy unavailable; set MEDIAPIPE_TEST_PYTHON")
	}
	script := `import ast,numpy as np
source=ast.parse(open('smartcrop_pose_runtime.py').read())
selected=[n for n in source.body if isinstance(n,ast.FunctionDef) and n.name in ('hand_refresh','plan_pose','bounds','midpoint')]
THRESHOLD=.5;RATIO=9/16;req={'ratio_w':9,'ratio_h':16};import math
exec(compile(ast.Module(body=selected,type_ignores=[]),'embedded-runtime','exec'))
p=np.ones((33,4));p[:,:2]=[1000,400];p[:,2:]=.9
p[11,:2]=[900,500];p[12,:2]=[1100,500];p[13,:2]=[850,650];p[14,:2]=[1150,650]
for i in [15,17,19,21]:p[i,:2]=[800,800]
for i in [16,18,20,22]:p[i,:2]=[1200,800]
p[15,2]=.2
fresh=p.copy();fresh[15,2]=.95;fresh[16,:2]=[1300,820]
merged,hands,status=hand_refresh(p,fresh)
assert hands==[15] and status=='same_frame_hand_recovered'
assert merged[15,2]==.95 and np.array_equal(merged[16],p[16]),'changed supported gesture'
for mutation in ['shoulder','face','elbow','finger','low_confidence']:
 candidate=fresh.copy()
 if mutation=='shoulder':candidate[11,0]+=300
 if mutation=='face':candidate[:11,0]+=300
 if mutation=='elbow':candidate[13,0]+=200
 if mutation=='finger':candidate[17,0]+=200
 if mutation=='low_confidence':candidate[15,2]=.4
 guarded,accepted,_=hand_refresh(p,candidate)
 assert not accepted and np.array_equal(guarded,p),mutation
plan=lambda v:plan_pose(np.column_stack((v[:,:2],np.min(v[:,2:],axis=1))),1920,1080)
req['framing']='widest_valid'
wide=plan(merged);assert wide['crop']['width']==606 and wide['crop']['height']==1080
broad=merged.copy()
for i in [15,17,19,21]:broad[i,0]=650
for i in [16,18,20,22]:broad[i,0]=1480
assert plan(broad)['status']=='upper_pose_exceeds_crop','wide-hand guard relaxed'
assert plan(p)['status']=='uncertain_hand_evidence','occlusion guard relaxed'
print('same-frame confidence, identity/gesture conflicts, unresolved occlusion and wide-pose guards verified')
`
	if out, err := exec.Command(python, "-c", script).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
}

func TestMediaPipeReportNativeRegression(t *testing.T) {
	root := os.Getenv("MEDIAPIPE_REPORT_FIXTURE_DIR")
	python := os.Getenv("MEDIAPIPE_TEST_PYTHON")
	if root == "" || python == "" {
		t.Skip("set private report fixture directory and isolated Python")
	}
	app := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj), tk.WithConfig(map[string]string{"smart_crop_python": python}))
	sc := &storageClient{}
	engine := os.Getenv("MEDIAPIPE_TEST_ENGINE")
	if engine == "" {
		engine = "mediapipe_full"
	}
	for _, c := range []struct {
		name, file, framing, coverage string
		w, h                          int
		fallback                      bool
	}{
		{"native-tight", "94532.png", "upper_body", "sampled_extent_fits", 378, 672, false},
		{"native-widest", "94532.png", "widest_valid", "sampled_extent_fits", 606, 1080, false},
		{"wide-hand-control", "94687.png", "widest_valid", "exceeds_crop_width", 606, 1080, false},
		{"no-pose-fallback", "92078-127385.png", "upper_body", "unknown", 606, 1080, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			if engine == "hybrid" && c.fallback {
				t.Skip("Full-only fallback baseline; hybrid has independent same-frame recovery")
			}
			source := filepath.Join(root, "native", c.file)
			p := sampleImageProbe()
			p.Width = 1920
			p.Height = 1080
			upsertMedia(app.AppDB(), testProj, c.name, p, strings.Repeat("a", 64), "/", c.file)
			ctx := context.WithValue(context.Background(), renderSourcesKey{}, map[string]string{c.name: source})
			ctx = context.WithValue(ctx, poseRecoveryModelsKey{}, os.Getenv("MEDIAPIPE_RECOVERY_MODEL_DIR"))
			params, _ := json.Marshal(map[string]any{"target_ratio": "9:16", "crop_mode": "smart", "fit_mode": "crop", "smart_crop_framing": c.framing, "smart_crop_engine": engine})
			resolved := preprocessSmartCrop(ctx, app, sc, testProj, "crop", []string{c.name}, params)
			var r struct {
				W     int            `json:"crop_w"`
				H     int            `json:"crop_h"`
				Audit smartCropAudit `json:"crop_diagnostics"`
			}
			json.Unmarshal(resolved, &r)
			if r.W != c.w || r.H != c.h || r.Audit.Coverage != c.coverage || r.Audit.Framing != c.framing || r.Audit.PoseAttempt == nil {
				t.Fatalf("unexpected crop: %s", resolved)
			}
			if c.fallback {
				if r.Audit.EffectiveEngine != "legacy" || r.Audit.PoseAttempt.Invalid != 1 || r.Audit.PoseAttempt.Status != "planning_failed" || len(r.Audit.PoseSamples) != 1 || len(r.Audit.PoseAttempt.Failed) != 1 || r.Audit.PoseAttempt.Failed[0].Category != "no_pose_detected" {
					t.Fatalf("fallback lost evidence: %s", resolved)
				}
			} else if r.Audit.EffectiveEngine != engine || len(r.Audit.PoseSamples[0].Landmarks) != 23 {
				t.Fatalf("missing confidence: %s", resolved)
			}
			if c.coverage == "sampled_extent_fits" && !cropRetainsSampledExtents(&r.Audit) {
				t.Fatal("false fit")
			}
			strict := map[string]any{}
			json.Unmarshal(resolved, &strict)
			strict["require_action_preservation"] = true
			raw, _ := json.Marshal(strict)
			_, err := applyCropCompositionPolicy(raw)
			if (c.coverage == "sampled_extent_fits") != (err == nil) {
				t.Fatalf("guard changed: %v", err)
			}
			if c.name == "native-widest" {
				plan, err := buildPlan("crop", []string{c.name}, resolved, "native-widest.png", ".png")
				if err != nil {
					t.Fatal(err)
				}
				output := filepath.Join(root, "native-widest-output.png")
				args := append([]string{}, plan.Args...)
				for i, a := range args {
					if a == "{input}" {
						args[i] = source
					}
				}
				if out, err := exec.Command("ffmpeg", append(args, output)...).CombinedOutput(); err != nil {
					t.Fatalf("render %v %s", err, out)
				}
				probe, err := runProbe(context.Background(), "ffprobe", output)
				if err != nil {
					t.Fatal(err)
				}
				if probe.Width != 606 || probe.Height != 1080 {
					t.Fatalf("unrequested scaling: %+v", probe)
				}
			}
			if err := os.WriteFile(filepath.Join(root, c.name+".json"), resolved, 0600); err != nil {
				t.Fatal(err)
			}
			t.Logf("%s: engine=%s coverage=%s native=%dx%d", c.name, r.Audit.EffectiveEngine, r.Audit.Coverage, r.W, r.H)
		})
	}
}
