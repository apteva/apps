package main

import (
	"context"
	"encoding/json"
	tk "github.com/apteva/app-sdk/testkit"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestHybridGroundingArbitration(t *testing.T) {
	python := os.Getenv("MEDIAPIPE_TEST_PYTHON")
	if python == "" {
		python = "python3"
	}
	if exec.Command(python, "-c", "import numpy,cv2").Run() != nil {
		t.Skip("set MEDIAPIPE_TEST_PYTHON with numpy/OpenCV")
	}
	script := `import numpy as np,sys
sys.dont_write_bytecode=True
from smartcrop_pose_recovery import Recovery,box_plan
r=Recovery.__new__(Recovery);im=np.zeros((1080,1920,3),np.uint8)
y=np.zeros((17,3));y[:]=[1000,250,.9];y[3,:2]=[950,200];y[4,:2]=[1050,200]
y[5,:2]=[900,400];y[6,:2]=[1100,400];y[7,:2]=[850,550];y[8,:2]=[1150,550];y[9,:2]=[850,700];y[10,:2]=[1150,700]
p=np.zeros((26,3));p[:17]=y;p[17]=[1000,140,.9]
r.person_box=lambda _:([800,100,1200,1000,.9]+y.flatten().tolist(),'single_person_detected')
r.infer=lambda *_:p.copy()
mapping=[(0,0),(2,1),(5,2),(7,3),(8,4),(11,5),(12,6),(13,7),(14,8),(15,9),(16,10)]
full=np.zeros((33,4));full[:]=[1000,250,.9,.9]
for a,b in mapping:full[a]=[*y[b,:2],.9,.9]
plan=lambda v:dict(box_plan([800,100,1200,750],[900,100,1100,300],1920,1080,9/16,'widest_valid'),status='fits_detected_upper_pose')
ev=lambda v:[dict(index=i,x=float(q[0]),y=float(q[1]),visibility=float(q[2]),presence=float(q[3])) for i,q in enumerate(v[:23])]
bad=full.copy();bad[:13,1]+=550
s=dict(plan(full),at_ms=1,landmark_evidence=ev(bad))
fixed=r.arbitrate(im,s,lambda:full,plan,'widest_valid')
assert fixed['recovery']['status']=='whole_pose_reacquired_full' and fixed['status']=='fits_detected_upper_pose'
assert fixed['landmark_evidence']==ev(full) and fixed['recovery']['original_pose']['landmark_evidence']==ev(bad)
assert fixed['bounds_model']=='mediapipe_full_fresh','wrong primary vetoed complete recovery'
# Preserve confidence scopes: whole secondary recovery never invents MP probabilities.
secondary=r.arbitrate(im,s,lambda:None,plan,'widest_valid')
assert secondary['bounds_model']=='rtmpose_yolo_whole' and 'landmark_evidence' not in secondary
assert secondary['status']=='fits_detected_upper_pose'
# Confidence alone cannot certify an occluded hand.
p[9,2]=.1
occluded=r.arbitrate(im,s,lambda:None,plan,'widest_valid')
assert occluded['status']=='uncertain_hand_evidence' and 15 in occluded['low_confidence_wrist_indices']
p[9,2]=.9
# Conflicting independent identities and multiple people never become fit.
p[5,0]+=500
assert r.arbitrate(im,s,lambda:full,plan)['status']=='uncertain_person_extent'
p[5,0]-=500
r.person_box=lambda _:(None,'multiple_people_unresolved')
assert r.arbitrate(im,s,lambda:full,plan)['geometry_trust']=='unverified'
# Orientation does not determine validity: reclining/inverted head/torso are allowed.
for axis in (0,1):
 a={i:v.tolist() for i,v in enumerate(y)};rot=y.copy();rot[:,axis]=1920-rot[:,axis] if axis==0 else 1080-rot[:,axis]
 a={i:v.tolist() for i,v in enumerate(rot)}
 used,conflicts=r.agreement(a,rot,[(i,i) for i in range(11)],200)
 assert len(used)==11 and not conflicts
# Back-facing/closed-eye input can be grounded with ears; orientation is not a gate.
for axis in (0,1):
    rotated=full.copy();rotated[:,axis]=(1920 if axis==0 else 1080)-rotated[:,axis]
    independent=y.copy();independent[:,axis]=(1920 if axis==0 else 1080)-independent[:,axis]
    r.person_box=lambda _,v=independent:([0,0,1920,1080,.9]+v.flatten().tolist(),'single_person_detected')
    assert r.arbitrate(im,dict(plan(rotated),landmark_evidence=ev(rotated)),lambda:None,plan)['recovery']['status']=='primary_pose_grounded'
# Never cherry-pick a narrow skeleton when independently supported hands are wide.
wide=y.copy();wide[9,0]=600;wide[10,0]=1500
p[:17]=wide
r.person_box=lambda _:([500,100,1600,1000,.9]+wide.flatten().tolist(),'single_person_detected')
wide_result=r.arbitrate(im,s,lambda:full,plan,'widest_valid')
assert wide_result['status']=='upper_pose_exceeds_crop' and wide_result['required_upper_pose_width']>606
p[:17]=y
# Even if torso correspondence fails, a supported reclining head stays in the
# unknown-position crop instead of being displaced by the full person rectangle.
p[5,0]+=500
r.person_box=lambda _:([400,100,1600,1000,.9]+y.flatten().tolist(),'single_person_detected')
head_s=dict(s,status="uncertain_hand_evidence",landmark_evidence=ev(full),head_bounds_estimate=[880,100,1120,300])
unknown=r.arbitrate(im,head_s,lambda:None,plan,'widest_valid')
c=unknown['crop'];assert unknown['status']=='uncertain_person_extent' and c['x']<=880 and c['x']+c['width']>=1120
p[5,0]-=500
# Two overlapping detections remain two identities; tiled grouping cannot hide them.
r.detect=lambda _:[[800,100,1200,1000,.9]+y.flatten().tolist(),[850,100,1250,1000,.85]+y.flatten().tolist()]
del r.person_box
assert r.person_box(im)[1]=='multiple_people_unresolved'
# Coordinate conversion including tiles and flips keeps native pose geometry.
values=np.array([[100,200,.9]]*17);native=Recovery.native_keypoints(values,.5,10,20)
box=[0,0,0,0,.9]+native;Recovery.transform_keypoints(box,offset=100,flip_width=1920)
assert box[5]==1640 and box[6]==360 and box[7]==.9
for angle,box,expected in [(90,[20,100,40,200,.9], [100,1040,200,1060]),(180,[20,100,40,200,.9],[1880,880,1900,980]),(270,[20,100,40,200,.9],[1720,20,1820,40])]:
    box=box+[20,100,.9]*17
    Recovery.unrotate(box,angle,1920,1080)
    assert box[:4]==expected,(angle,box[:4])
print('whole-pose grounding/replacement, occlusion, conflicting identity and coordinate transforms verified')`
	if out, err := exec.Command(python, "-c", script).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
}

func TestPoseMixedFailureSummary(t *testing.T) {
	samples := []poseSample{
		{AtMs: 10, Status: "upper_pose_exceeds_crop", GeometryTrust: "independently_grounded"},
		{AtMs: 20, Status: "upper_pose_exceeds_crop", GeometryTrust: "independently_grounded", WeakWrists: []int{15}},
		{AtMs: 30, Status: "uncertain_person_extent", GeometryTrust: "unverified", Recovery: &poseRecoveryEvidence{ConflictingPrimary: []int{11}, Status: "whole_pose_identity_unresolved"}},
	}
	f := summarizePoseFailures(samples)
	if f.Classification != "mixed_trusted_overflow_and_unresolved_evidence" || len(f.TrustedOverflow) != 1 || f.TrustedOverflow[0] != 10 || len(f.UncertainHands) != 1 || len(f.RejectedPrimary) != 1 || len(f.UnresolvedIdentity) != 1 {
		t.Fatalf("%+v", f)
	}
	raw, _ := json.Marshal(poseSample{HeadMargin: 25, HandMargin: 40, RequiredWidth: 670, MaxWidth: 607.5, OutsideIndices: []int{16}, BoundsModel: "rtmpose_yolo_whole", Recovery: &poseRecoveryEvidence{OriginalPose: json.RawMessage(`{"status":"wrong_pose"}`)}})
	var s poseSample
	if err := json.Unmarshal(raw, &s); err != nil || s.HeadMargin != 25 || s.HandMargin != 40 || len(s.Recovery.OriginalPose) == 0 {
		t.Fatalf("diagnostics lost: %s %v", raw, err)
	}
}

func TestHybridChickenTrackingRegression(t *testing.T) {
	root := os.Getenv("MEDIAPIPE_CHICKEN_REGRESSION_DIR")
	python := os.Getenv("MEDIAPIPE_TEST_PYTHON")
	if root == "" || python == "" {
		t.Skip("set private .28 regression fixture directory and Python")
	}
	models := filepath.Join(filepath.Dir(root), "feedback-0.14.26")
	source := filepath.Join(filepath.Dir(root), "feedback-0.14.25", "chicken-92078.mov")
	app := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj), tk.WithConfig(map[string]string{"smart_crop_python": python}))
	p := sampleImageProbe()
	p.IsImage = false
	p.HasVideo = true
	p.Width = 1920
	p.Height = 1080
	p.DurationMs = 406156
	p.FPS = 30000.0 / 1001
	upsertMedia(app.AppDB(), testProj, "92078", p, "499375e332c61be57543ebd29db931b8d1eff9f2cf7c2c9c3410c864dfb660e4", "/", filepath.Base(source))
	ctx := context.WithValue(context.Background(), renderSourcesKey{}, map[string]string{"92078": source})
	ctx = context.WithValue(ctx, poseRecoveryModelsKey{}, models)
	for _, c := range []struct {
		name       string
		start, end int64
	}{{"R1", 57010, 91700}, {"R2", 349570, 374840}} {
		t.Run(c.name, func(t *testing.T) {
			raw, _ := json.Marshal(map[string]any{"start_ms": c.start, "end_ms": c.end, "target_ratio": "9:16", "crop_mode": "smart", "fit_mode": "crop", "smart_crop_engine": "hybrid", "smart_crop_framing": "widest_valid"})
			out := preprocessSmartCrop(ctx, app, &storageClient{}, testProj, "extract_reel", []string{"92078"}, raw)
			var r struct {
				Audit   smartCropAudit `json:"crop_diagnostics"`
				Version string         `json:"crop_version"`
			}
			if err := json.Unmarshal(out, &r); err != nil {
				t.Fatal(err)
			}
			if r.Audit.EffectiveEngine != "hybrid" || len(r.Audit.PoseSamples) < 50 {
				t.Fatalf("fallback: %s", out)
			}
			for _, s := range r.Audit.PoseSamples {
				if s.Recovery == nil || s.Recovery.Status == "runtime_unavailable" || s.Recovery.Status == "budget_exhausted" {
					t.Fatalf("grounding missing at %d: %+v", s.AtMs, s.Recovery)
				}
				if s.AtMs == 349570 && (len(s.Recovery.ConflictingPrimary) == 0 || s.BoundsModel == "mediapipe_full_tracked" || len(s.Head) != 4 || s.Head[1] > 400) {
					t.Fatalf("skirt head retained: %+v", s)
				}
				if s.AtMs == 351056 || s.AtMs == 368397 {
					if s.Status != "fits_detected_upper_pose" || s.GeometryTrust != "independently_grounded" {
						t.Fatalf("positive recovery failed: %+v", s)
					}
				}
				if s.AtMs == 88230 && s.Status == "fits_detected_upper_pose" {
					t.Fatal("genuine wing pose cleared")
				}
			}
			if r.Audit.Coverage == "sampled_extent_fits" {
				t.Fatal("complete interval lacks verified preservation")
			}
			if err := os.WriteFile(filepath.Join(root, "local-"+c.name+"-resolved.json"), out, 0600); err != nil {
				t.Fatal(err)
			}
			plan, err := buildPlan("extract_reel", []string{"92078"}, out, c.name+".mp4", ".mp4")
			if err != nil {
				t.Fatal(err)
			}
			args := append([]string(nil), plan.Args...)
			for i, a := range args {
				if a == "{input}" {
					args[i] = source
				}
			}
			dest := filepath.Join(root, "local-"+c.name+"-diagnostic.mp4")
			if b, err := exec.Command("ffmpeg", append(args, dest)...).CombinedOutput(); err != nil {
				t.Fatalf("render: %v %s", err, b)
			}
			t.Logf("%s: coverage=%s summary=%+v", c.name, r.Audit.Coverage, r.Audit.PoseFailures)
		})
	}
}
