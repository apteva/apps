package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestComponentCachedVideoPoseSampling(t *testing.T) {
	app, platform := componentFixture(t)
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("FFmpeg unavailable")
	}
	source := filepath.Join(t.TempDir(), "source.mp4")
	if out, err := exec.Command(ffmpeg, "-v", "error", "-f", "lavfi", "-i", "testsrc2=size=160x90:rate=10", "-t", "2", "-c:v", "libx264", "-threads", "1", source).CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v %s", err, out)
	}
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	var available atomic.Bool
	available.Store(true)
	var downloads atomic.Int64
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !available.Load() {
			http.NotFound(w, r)
			return
		}
		downloads.Add(1)
		_, _ = w.Write(data)
	}))
	defer gateway.Close()
	f := &StorageFile{ID: 123, Name: "source.mp4", SizeBytes: int64(len(data)), SHA256: fmt.Sprintf("%x", sha256.Sum256(data))}
	req := poseRequest{Source: gateway.URL + "/private-video?token=secret", SourceFile: f, Positions: []int64{0, 500, 1500}, FFmpeg: ffmpeg, Video: true}
	// Run the production request resolver and frame sampler with real FFmpeg
	// through the real worker SDK/process. Only ML provisioning/inference is
	// replaced; it is unrelated to the encoded-path and cache regression.
	runtime := `import ast,base64,json,os,sys,time,subprocess,tempfile
source=ast.parse(base64.b64decode('` + base64.StdEncoding.EncodeToString([]byte(poseRuntime)) + `'))
names={'resolve_workspace_paths','ffmpeg_diagnostic','PoseRuntimeFailure','extract_frame'}
selected=[n for n in source.body if isinstance(n,(ast.FunctionDef,ast.ClassDef)) and n.name in names]
exec(compile(ast.Module(body=selected,type_ignores=[]),'embedded-pose-runtime','exec'))
r=json.load(open(sys.argv[1]))
if len(sys.argv)>2:resolve_workspace_paths(r,sys.argv[1],sys.argv[2])
samples=[]
try:
 with tempfile.TemporaryDirectory() as work:
  for at in r['positions']:
   path=os.path.join(work,'frame.png')
   attempts=extract_frame(r,at,path,time.monotonic()+15)
   assert open(path,'rb').read(8)==b'\x89PNG\r\n\x1a\n'
   samples.append({'at_ms':at,'extraction_attempts':attempts})
 assert open(r['model'],'rb').read()==b'fixture-model'
 assert os.path.samefile(r['recovery_root'],os.path.dirname(r['model']))
 print('APTEVA_POSE:'+json.dumps({'samples':samples}))
except PoseRuntimeFailure as error:
 failure={'code':error.code,'at_ms':error.at_ms,'attempts':error.attempts}
 if getattr(error,'ffmpeg',None):failure['ffmpeg']=error.ffmpeg
 print('APTEVA_POSE_ERROR:'+json.dumps(failure));sys.exit(1)
`
	setup := `import os,pathlib,sys
root=pathlib.Path(sys.argv[1]);(root/'venv/bin').mkdir(parents=True,exist_ok=True)
python=root/'venv/bin/python'
if not python.exists():python.symlink_to(sys.executable)
(root/'model.task').write_bytes(b'fixture-model')
`
	run := func(name string) (string, int, error) {
		t.Helper()
		script := buildPoseRemoteScript(req, poseRemoteRuntimeRoot(false), uniqueRemoteWorkDir(0)+"-pose-"+name, "")
		script = strings.ReplaceAll(script, base64.StdEncoding.EncodeToString([]byte(poseSetup)), base64.StdEncoding.EncodeToString([]byte(setup)))
		script = strings.ReplaceAll(script, base64.StdEncoding.EncodeToString([]byte(poseRuntime)), base64.StdEncoding.EncodeToString([]byte(runtime)))
		return runComponentPlan(context.Background(), app, "setsid bash -c "+shellQuote(script), 30)
	}
	for _, name := range []string{"cold", "warm"} {
		out, code, err := run(name)
		if err != nil || code != 0 {
			t.Fatalf("%s: exit=%d err=%v %s", name, code, err, out)
		}
		wantCache := "MISS"
		if name == "warm" {
			wantCache = "HIT"
		}
		if !strings.Contains(out, "REMOTE_SOURCE_CACHE_"+wantCache) {
			t.Fatalf("%s cache: %s", name, out)
		}
		var result struct {
			Samples []poseSample `json:"samples"`
		}
		for _, line := range strings.Split(out, "\n") {
			if strings.HasPrefix(line, "APTEVA_POSE:") {
				if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "APTEVA_POSE:")), &result); err != nil {
					t.Fatal(err)
				}
			}
		}
		if len(result.Samples) != len(req.Positions) {
			t.Fatalf("%s samples: %+v", name, result)
		}
		for i, sample := range result.Samples {
			if sample.AtMs != req.Positions[i] || sample.ExtractionAttempts != 1 {
				t.Fatalf("%s sample: %+v", name, sample)
			}
		}
		available.Store(false) // Warm sampling must use verified cached bytes.
	}
	if downloads.Load() != 1 || platform.submits.Load() != 2 {
		t.Fatalf("downloads=%d submits=%d", downloads.Load(), platform.submits.Load())
	}
	bad := []byte("not a video")
	badGateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(bad) }))
	defer badGateway.Close()
	req.Source = badGateway.URL + "/private?token=secret"
	req.SourceFile = &StorageFile{ID: 124, Name: "bad.mp4", SizeBytes: int64(len(bad)), SHA256: fmt.Sprintf("%x", sha256.Sum256(bad))}
	out, code, err := run("invalid-video")
	failure := parsePoseRuntimeFailure(out)
	if err == nil || code == 0 || failure == nil || failure.Code != "pose_source_frame_unavailable" || failure.Attempts != 3 || failure.FFmpeg == nil || failure.FFmpeg.ExitCode == nil || *failure.FFmpeg.ExitCode == 0 || len(failure.FFmpeg.Stderr) == 0 {
		t.Fatalf("decoder diagnostics: exit=%d err=%v failure=%+v %s", code, err, failure, out)
	}
	raw, _ := json.Marshal(failure)
	if strings.Contains(string(raw), "private") || strings.Contains(string(raw), "secret") || strings.Contains(string(raw), "/var/") {
		t.Fatalf("unsafe decoder diagnostics: %s", raw)
	}
}

func TestComponentCachedVideoPoseInference(t *testing.T) {
	if os.Getenv("MEDIA_POSE_COMPONENT_REAL_RUNTIME") != "1" {
		t.Skip("set MEDIA_POSE_COMPONENT_REAL_RUNTIME=1 to provision the pinned ML runtime")
	}
	app, _ := componentFixture(t)
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("FFmpeg unavailable")
	}
	source := filepath.Join(t.TempDir(), "synthetic-person.mp4")
	if out, err := exec.Command(ffmpeg, "-v", "error", "-loop", "1", "-i", "testdata/smartcrop/synthetic_seated_person.jpg", "-t", "2", "-r", "10", "-c:v", "libx264", "-pix_fmt", "yuv420p", "-threads", "1", source).CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v %s", err, out)
	}
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	var available atomic.Bool
	available.Store(true)
	var downloads atomic.Int64
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !available.Load() {
			http.NotFound(w, r)
			return
		}
		downloads.Add(1)
		_, _ = w.Write(data)
	}))
	defer gateway.Close()
	req := poseRequest{Source: gateway.URL + "/synthetic.mp4", SourceFile: &StorageFile{ID: 125, Name: "synthetic.mp4", SizeBytes: int64(len(data)), SHA256: fmt.Sprintf("%x", sha256.Sum256(data))}, Positions: []int64{0, 500, 1500}, FFmpeg: ffmpeg, Video: true, Width: 640, Height: 360, RatioW: 9, RatioH: 16, ModelSHA: poseModelSHA256, Framing: "upper_body"}
	for _, name := range []string{"cold", "warm"} {
		audit := &smartCropAudit{}
		ctx := context.WithValue(t.Context(), smartCropAuditKey{}, audit)
		result, err := runPose(ctx, app, 1, req)
		if err != nil {
			t.Fatalf("%s inference: %v", name, err)
		}
		wantCache := "miss"
		if name == "warm" {
			wantCache = "hit"
		}
		if len(result.Samples) != len(req.Positions) || audit.SourceCache != wantCache {
			t.Fatalf("%s inference: samples=%d cache=%s", name, len(result.Samples), audit.SourceCache)
		}
		for i, sample := range result.Samples {
			if sample.AtMs != req.Positions[i] || sample.ExtractionAttempts != 1 || len(sample.Landmarks) == 0 {
				t.Fatalf("%s sample: %+v", name, sample)
			}
		}
		t.Logf("%s: %d frames extracted and pose inference completed; cache=%s", name, len(result.Samples), audit.SourceCache)
		available.Store(false)
	}
	if downloads.Load() != 1 {
		t.Fatalf("source downloaded %d times", downloads.Load())
	}
}
