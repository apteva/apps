package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

func TestHybridPositionGapsStayUnverified(t *testing.T) {
	samples := make([]poseSample, 8)
	for i := range samples {
		samples[i] = poseSample{AtMs: int64(i) * 500, Status: "fits_detected_upper_pose", Bounds: []float64{800 + float64(i)*10, 300, 1100 + float64(i)*10, 950}}
		samples[i].Crop.Width = 450
		samples[i].Crop.Height = 800
		samples[i].Crop.X = 750 + i*10
	}
	samples[3] = poseSample{AtMs: 1500, Status: "no_pose_detected"}
	win, path, gaps, err := planHybridPoseSamples(samples, 1920, 1080, 9, 16)
	if err != nil || len(path) != 8 || len(gaps) != 1 || gaps[0].AtMs != 1500 || gaps[0].Method != "interpolated_position_only" {
		t.Fatalf("%+v %+v %v", path, gaps, err)
	}
	if samples[3].Status != "no_pose_detected" || len(samples[3].Bounds) != 0 {
		t.Fatal("invented pose evidence")
	}
	for i, s := range samples {
		if poseSampleHasGeometry(s, 1920, 1080) {
			b := poseBoundsWindow(s.Bounds, 1920, 1080)
			if path[i].X > b.X || path[i].X+win.W < b.X+b.W {
				t.Fatal("lost supported subject")
			}
		}
	}
	// A long hole and sparse evidence must still fall back.
	sparse := append([]poseSample(nil), samples...)
	for i := 2; i < 6; i++ {
		sparse[i] = poseSample{AtMs: int64(i) * 500, Status: "no_pose_detected"}
	}
	if _, _, _, err := planHybridPoseSamples(sparse, 1920, 1080, 9, 16); err == nil {
		t.Fatal("sparse pose accepted")
	}
	distant := append([]poseSample(nil), samples...)
	for i := 4; i < len(distant); i++ {
		distant[i].AtMs += 3000
	}
	if _, _, _, err := planHybridPoseSamples(distant, 1920, 1080, 9, 16); err == nil {
		t.Fatal("long gap accepted")
	}
	// Strict composition checks reject any unresolved coverage.
	raw, _ := json.Marshal(map[string]any{"require_action_preservation": true, "fit_mode": "crop", "crop_diagnostics": map[string]any{"action_coverage": "unknown", "pose_position_gaps": gaps}})
	if _, err := applyCropCompositionPolicy(raw); err == nil {
		t.Fatal("unknown coverage approved")
	}
	// Rounding an exact 9:16 unit to 594 must not reject a 600px extent.
	narrow := samples[0]
	narrow.Status = "upper_pose_exceeds_crop"
	narrow.Bounds = []float64{900, 200, 1500, 1050}
	narrow.Crop.Width = 594
	narrow.Crop.Height = 1056
	widened, _, _, err := planHybridPoseSamples([]poseSample{narrow}, 1920, 1080, 9, 16)
	if err != nil || widened.W < 600 || widened.W > 606 || widened.H > 1080 {
		t.Fatalf("feasible strip lost: %+v %v", widened, err)
	}
	if _, _, err := planPoseSamples(samples, 1920, 1080, 9, 16); err == nil {
		t.Fatal("pure Full semantics changed")
	}
}

func TestHybridRecoveryAgreementAndSourceEdges(t *testing.T) {
	python := os.Getenv("MEDIAPIPE_TEST_PYTHON")
	if python == "" {
		python = "python3"
	}
	if err := exec.Command(python, "-c", "import numpy,cv2").Run(); err != nil {
		t.Skip("numpy/OpenCV unavailable; set MEDIAPIPE_TEST_PYTHON")
	}
	script := `import numpy as np, tempfile, pathlib, sys
sys.dont_write_bytecode=True
from smartcrop_pose_recovery import Recovery, box_plan
im=np.zeros((1080,1920,3),np.uint8)
r=Recovery.__new__(Recovery)
r.person_box=lambda _:([710,300,1290,860,.9],'single_person_detected')
p=np.zeros((26,3));p[:]=[1000,400,.9]
p[5,:2]=[900,500];p[6,:2]=[1100,500];p[7,:2]=[800,650];p[8,:2]=[1200,650];p[9,:2]=[750,800];p[10,:2]=[1250,800]
ev=[{'index':i,'x':1000,'y':400,'visibility':.9,'presence':.9} for i in range(23)]
for i,j in [(11,5),(12,6),(13,7),(14,8),(15,9),(16,10),(17,9),(19,9),(21,9),(18,10),(20,10),(22,10)]:ev[i]['x'],ev[i]['y']=p[j,:2]
ev[15]['visibility']=.2
s={'status':'uncertain_hand_evidence','required_bounds':[710,300,1290,860],'head_bounds_estimate':[900,300,1100,450],'landmark_evidence':ev,'low_confidence_wrist_indices':[15]}
r.infer=lambda *_:p.copy()
fixed=r.recover(im,s)
assert fixed['recovery']['recovered_wrist_indices']==[15] and fixed['low_confidence_wrist_indices']==[]
assert fixed['landmark_evidence']==s['landmark_evidence'],'secondary scores replaced primary probabilities'
assert fixed['required_bounds'][0]<=710 and fixed['required_bounds'][2]>=1290,'lost existing hand extent'
for kind in ['torso','head','elbow','finger']:
    candidate=p.copy()
    if kind=='torso':candidate[5,0]+=300
    if kind=='head':candidate[:5,0]+=300
    if kind=='elbow':candidate[7,0]+=300
    if kind=='finger':candidate[9,0]+=300
    r.infer=lambda *_,v=candidate:v.copy()
    guarded=r.recover(im,s)
    assert not guarded['recovery'].get('recovered_wrist_indices'),kind
    assert guarded['status']==s['status'] and guarded['required_bounds']==s['required_bounds'],kind
r.infer=lambda *_:p.copy()
coarse=r.recover(im,{'status':'no_pose_detected'})
assert coarse['status']=='uncertain_person_extent','person box established head/hand coverage'
for head in [[-100,100,80,250],[1850,100,2050,250],[-100,100,-20,250]]:
    plan=box_plan([-100,-100,2050,1200],head,1920,1080,9/16,'widest_valid')
    c=plan['crop'];assert 0<=c['x']<=1920-c['width'] and 0<=c['y']<=1080-c['height'],plan
    assert not plan['fits_bounds'],'broad pose falsely fitted'
with tempfile.TemporaryDirectory() as root:
    pathlib.Path(root,'yolo11n-pose.onnx').write_bytes(b'invalid')
    try:Recovery(root)
    except RuntimeError as e:assert str(e)=='recovery_model_hash_mismatch'
    else:raise AssertionError('invalid weights accepted')
print('independent-model conflict, source-edge and unknown-coverage guards verified')
`
	if out, err := exec.Command(python, "-c", script).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
}

func TestHybridPrivateChickenReels(t *testing.T) {
	root := os.Getenv("MEDIAPIPE_REPORT_FIXTURE_DIR")
	python := os.Getenv("MEDIAPIPE_TEST_PYTHON")
	if root == "" || python == "" {
		t.Skip("set private .26 report fixture and isolated Python")
	}
	if _, err := os.Stat(filepath.Join(root, "rtmpose-m-halpe26.onnx")); err != nil {
		t.Skip("private recovery models unavailable")
	}
	app := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj), tk.WithConfig(map[string]string{"smart_crop_python": python}))
	sc := &storageClient{}
	for _, c := range []struct {
		name       string
		start, end int64
	}{{"peck", 127385, 150000}, {"proud", 160639, 186000}, {"egg", 197895, 220000}, {"reset", 234450, 254450}, {"resting-R2", 143550, 183000}, {"resting-R3", 190615, 224285}} {
		t.Run(c.name, func(t *testing.T) {
			source := filepath.Join(filepath.Dir(root), "feedback-0.14.25", "chicken-92078.mov")
			id, digest, duration, fps := "92078", "499375e332c61be57543ebd29db931b8d1eff9f2cf7c2c9c3410c864dfb660e4", int64(406156), 30000.0/1001
			if strings.HasPrefix(c.name, "resting-") {
				source = filepath.Join(root, "resting-91572.mov")
				id, digest, duration, fps = "91572", "1c8d0c4653803fff4bd410d5fad5f4e75a39f661a1a106a61ee7c4b556fe219b", 799215, 30
			}
			if _, err := os.Stat(source); err != nil {
				t.Skip("private source unavailable")
			}
			p := sampleImageProbe()
			p.IsImage = false
			p.HasVideo = true
			p.Width = 1920
			p.Height = 1080
			p.DurationMs = duration
			p.FPS = fps
			upsertMedia(app.AppDB(), testProj, id, p, digest, "/", filepath.Base(source))
			ctx := context.WithValue(context.Background(), renderSourcesKey{}, map[string]string{id: source})
			ctx = context.WithValue(ctx, poseRecoveryModelsKey{}, root)
			raw, _ := json.Marshal(map[string]any{"start_ms": c.start, "end_ms": c.end, "target_ratio": "9:16", "crop_mode": "smart", "fit_mode": "crop", "smart_crop_engine": "hybrid", "output_width": 540})
			out := preprocessSmartCrop(ctx, app, sc, testProj, "extract_reel", []string{id}, raw)
			var r struct {
				Audit   smartCropAudit `json:"crop_diagnostics"`
				Version string         `json:"crop_version"`
			}
			if err := json.Unmarshal(out, &r); err != nil {
				t.Fatal(err)
			}
			if r.Version != "pose_hybrid" || r.Audit.EffectiveEngine != "hybrid" || r.Audit.PoseAttempt == nil || len(r.Audit.PoseSamples) < 40 {
				t.Fatalf("unexpected fallback: engine=%s version=%s pose_attempt=%+v", r.Audit.EffectiveEngine, r.Version, r.Audit.PoseAttempt)
			}
			// New independent recovery may support formerly unresolved samples;
			// incomplete evidence must still never become a preservation pass.
			for _, sample := range r.Audit.PoseSamples {
				if r.Audit.Coverage == "sampled_extent_fits" && (sample.Status != "fits_detected_upper_pose" && sample.EffectiveStatus != "fits_supported_extent_after_reposition" || len(sample.WeakWrists) > 0 || sample.GeometryTrust != "independently_grounded") {
					t.Fatal("unresolved evidence falsely cleared")
				}
			}
			if r.Audit.Coverage == "sampled_extent_fits" && !cropRetainsSampledExtents(&r.Audit) {
				t.Fatal("sampled extents lost")
			}
			for _, s := range r.Audit.PoseSamples {
				if s.Recovery != nil && s.Recovery.Status == "runtime_unavailable" {
					t.Fatal("recovery runtime missing")
				}
			}
			outputRoot := root
			if override := os.Getenv("MEDIAPIPE_REPORT_OUTPUT_DIR"); override != "" {
				outputRoot = override
				if err := os.MkdirAll(outputRoot, 0700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(outputRoot, "local-"+c.name+"-resolved.json"), out, 0600); err != nil {
				t.Fatal(err)
			}
			if os.Getenv("MEDIAPIPE_PREVIEW_ONLY") == "1" {
				t.Logf("%s: engine=%s coverage=%s valid=%d invalid=%d", c.name, r.Audit.EffectiveEngine, r.Audit.Coverage, r.Audit.PoseAttempt.Valid, r.Audit.PoseAttempt.Invalid)
				return
			}
			plan, err := buildPlan("extract_reel", []string{id}, out, c.name+".mp4", ".mp4")
			if err != nil {
				t.Fatal(err)
			}
			args := append([]string(nil), plan.Args...)
			for i, a := range args {
				if a == "{input}" {
					args[i] = source
				}
			}
			dest := filepath.Join(root, "local-"+c.name+"-hybrid.mp4")
			if out, err := exec.Command("ffmpeg", append(args, dest)...).CombinedOutput(); err != nil {
				t.Fatalf("render: %v %s", err, out)
			}
			t.Logf("%s: engine=%s coverage=%s valid=%d invalid=%d gaps=%d", c.name, r.Audit.EffectiveEngine, r.Audit.Coverage, r.Audit.PoseAttempt.Valid, r.Audit.PoseAttempt.Invalid, len(r.Audit.PosePathGaps))
		})
	}
}
