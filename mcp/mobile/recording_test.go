package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

// testVideo renders a real H.264 clip, or skips when ffmpeg is unavailable.
func testVideo(t *testing.T, seconds int, audio bool) string {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is not installed")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe is not installed")
	}
	out := filepath.Join(t.TempDir(), "source.mp4")
	args := []string{"-v", "error", "-y", "-f", "lavfi", "-i", fmt.Sprintf("testsrc=size=360x780:rate=60:duration=%d", seconds)}
	if audio {
		args = append(args, "-f", "lavfi", "-i", fmt.Sprintf("sine=frequency=440:duration=%d", seconds), "-c:a", "aac")
	}
	args = append(args, "-c:v", "libx264", "-pix_fmt", "yuv420p", out)
	if b, err := exec.Command("ffmpeg", args...).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v %s", err, b)
	}
	return out
}

type fakeRecorder struct {
	src     string
	path    string
	done    chan struct{}
	closes  func() int
	stopped chan int
	stopErr error
}

func (r *fakeRecorder) Done() <-chan struct{} { return r.done }
func (r *fakeRecorder) Stop(context.Context) error {
	r.stopped <- r.closes()
	if r.src == "" {
		return os.WriteFile(r.path, []byte("not a video"), 0600)
	}
	b, err := os.ReadFile(r.src)
	if err != nil {
		return err
	}
	if err := os.WriteFile(r.path, b, 0600); err != nil {
		return err
	}
	return r.stopErr
}

type fakeRecordBackend struct {
	mu         sync.Mutex
	src        string
	audio      bool
	starts     int
	last       *fakeRecorder
	backend    *fakeBackend
	stopped    chan int
	readyDelay time.Duration
	stopErr    error
}

func (f *fakeRecordBackend) RecordPlan(_ Device, o recordOptions) (recordPlan, error) {
	if o.Quality == "high" {
		return recordPlan{}, problem("setup_required", "screen recording is unavailable for this device: install scrcpy")
	}
	return recordPlan{Method: "fake", Audio: f.audio && o.Audio, Ext: ".mp4"}, nil
}
func (f *fakeRecordBackend) StartRecording(_ context.Context, _ Session, _ recordPlan, _ recordOptions, path string) (recorder, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.readyDelay > 0 {
		time.Sleep(f.readyDelay)
	}
	f.starts++
	f.last = &fakeRecorder{src: f.src, path: path, done: make(chan struct{}), stopped: f.stopped, stopErr: f.stopErr, closes: func() int {
		f.backend.mu.Lock()
		defer f.backend.mu.Unlock()
		return f.backend.closes
	}}
	return f.last, nil
}

func recordFixture(t *testing.T, src string) (*engine, *fakeBackend, *fakeRecordBackend) {
	t.Helper()
	e, fake := fixture(t)
	rb := &fakeRecordBackend{src: src, backend: fake, stopped: make(chan int, 8)}
	e.recordBackend = rb
	return e, fake, rb
}

func recordingOf(t *testing.T, v any) Recording {
	t.Helper()
	var out struct {
		Recording Recording `json:"recording"`
	}
	if err := decodeMap(v, &out); err != nil {
		t.Fatal(err)
	}
	return out.Recording
}

func waitRecording(t *testing.T, e *engine, pid, rid string, want ...string) Recording {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		r, err := e.recording(pid, rid)
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range want {
			if r.Status == w {
				return r
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("recording %s did not reach %v", rid, want)
	return Recording{}
}

func TestRecordingLifecycleMarkersAndMetadata(t *testing.T) {
	src := testVideo(t, 3, true)
	e, _, _ := recordFixture(t, src)
	s := openFixture(t, e, "p")
	rec := recordingOf(t, execute(t, e, "p", "mobile_record", map[string]any{"action": "start", "session_id": s.ID}))
	if rec.Status != "recording" || rec.Kind != "capture" || !strings.HasSuffix(rec.FileName, ".mp4") {
		t.Fatalf("start: %+v", rec)
	}
	if _, err := e.Execute(context.Background(), "p", "mobile_record", map[string]any{"action": "start", "session_id": s.ID}); err == nil {
		t.Fatal("second concurrent capture on one session must be rejected")
	}
	execute(t, e, "p", "mobile_record", map[string]any{"action": "mark", "recording_id": rec.ID, "name": "gameplay"})
	if _, err := e.Execute(context.Background(), "other", "mobile_record", map[string]any{"action": "mark", "recording_id": rec.ID, "name": "x"}); err == nil {
		t.Fatal("another project marked the recording")
	}
	got := recordingOf(t, execute(t, e, "p", "mobile_record", map[string]any{"action": "stop", "session_id": s.ID}))
	if got.Status != "ready" {
		t.Fatalf("stop: %+v", got)
	}
	if got.Width != 360 || got.Height != 780 || got.VideoCodec != "h264" || !got.Audio || got.DurationMS < 2900 || len(got.Markers) != 1 || got.Markers[0].Name != "gameplay" {
		t.Fatalf("metadata: %+v", got)
	}
	if _, _, err := e.RecordingFile("other", rec.ID); err == nil {
		t.Fatal("recording file leaked across projects")
	}
	if _, err := e.Execute(context.Background(), "p", "mobile_record", map[string]any{"action": "delete", "recording_id": rec.ID}); err == nil {
		t.Fatal("delete without confirm")
	}
	execute(t, e, "p", "mobile_record", map[string]any{"action": "delete", "recording_id": rec.ID, "confirm": true})
	if _, err := os.Stat(e.recordingPath(got)); !os.IsNotExist(err) {
		t.Fatal("delete kept the runner file")
	}
}

func TestRecordingStartTimeExcludesPreparation(t *testing.T) {
	e, _, backend := recordFixture(t, testVideo(t, 1, false))
	backend.readyDelay = 1100 * time.Millisecond
	s := openFixture(t, e, "p")
	r := recordingOf(t, execute(t, e, "p", "mobile_record", map[string]any{"action": "start", "session_id": s.ID}))
	if r.StartedAt <= r.CreatedAt {
		t.Fatalf("preparation counted as recording time: created=%d started=%d", r.CreatedAt, r.StartedAt)
	}
	persisted, err := e.recording("p", r.ID)
	if err != nil || persisted.StartedAt != r.StartedAt {
		t.Fatalf("panel timer differs from ready boundary: %+v %v", persisted, err)
	}
	execute(t, e, "p", "mobile_record", map[string]any{"action": "stop", "recording_id": r.ID})
}

func TestSessionCloseFinalisesRecordingBeforeDriverClose(t *testing.T) {
	src := testVideo(t, 2, false)
	e, fake, rb := recordFixture(t, src)
	s := openFixture(t, e, "p")
	rec := recordingOf(t, execute(t, e, "p", "mobile_record", map[string]any{"action": "start", "session_id": s.ID}))
	execute(t, e, "p", "mobile_session", map[string]any{"action": "close", "session_id": s.ID})
	if closesAtStop := <-rb.stopped; closesAtStop != 0 {
		t.Fatal("driver session closed before the capture stopped")
	}
	if fake.closes != 1 {
		t.Fatalf("closes = %d", fake.closes)
	}
	if got := waitRecording(t, e, "p", rec.ID, "ready", "failed"); got.Status != "ready" {
		t.Fatalf("closed session recording: %+v", got)
	}
}

func TestRecordingAudioAndQualityErrors(t *testing.T) {
	e, _, _ := recordFixture(t, "")
	s := openFixture(t, e, "p")
	_, err := e.Execute(context.Background(), "p", "mobile_record", map[string]any{"action": "start", "session_id": s.ID, "audio": true})
	if errorCode(err) != "unsupported" {
		t.Fatalf("explicit audio without support: %v", err)
	}
	_, err = e.Execute(context.Background(), "p", "mobile_record", map[string]any{"action": "start", "session_id": s.ID, "quality": "high"})
	if errorCode(err) != "setup_required" {
		t.Fatalf("missing high-quality tool: %v", err)
	}
	_, err = e.Execute(context.Background(), "p", "mobile_record", map[string]any{"action": "start", "session_id": s.ID, "max_seconds": 99999})
	if errorCode(err) != "invalid_argument" {
		t.Fatalf("max_seconds bound: %v", err)
	}
}

func TestUnplayableCaptureIsReportedAsFailed(t *testing.T) {
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe is not installed")
	}
	e, _, _ := recordFixture(t, "")
	s := openFixture(t, e, "p")
	rec := recordingOf(t, execute(t, e, "p", "mobile_record", map[string]any{"action": "start", "session_id": s.ID}))
	got := recordingOf(t, execute(t, e, "p", "mobile_record", map[string]any{"action": "stop", "recording_id": rec.ID}))
	if got.Status != "failed" || !strings.Contains(got.Error, "not playable") {
		t.Fatalf("garbage capture: %+v", got)
	}
}

func TestMaxSecondsStopsCapture(t *testing.T) {
	src := testVideo(t, 1, false)
	e, _, _ := recordFixture(t, src)
	s := openFixture(t, e, "p")
	rec := recordingOf(t, execute(t, e, "p", "mobile_record", map[string]any{"action": "start", "session_id": s.ID, "max_seconds": 5}))
	start := time.Now()
	got := waitRecording(t, e, "p", rec.ID, "ready", "failed")
	if got.Status != "ready" || time.Since(start) < 4*time.Second {
		t.Fatalf("auto stop: %+v after %s", got, time.Since(start))
	}
}

func TestRecordedTestRunMarksEverySteps(t *testing.T) {
	src := testVideo(t, 2, false)
	e, _, _ := recordFixture(t, src)
	rid := execute(t, e, "p", "mobile_test_run", map[string]any{"device_id": "android:phone", "record": map[string]any{"quality": "auto"}, "steps": []any{
		map[string]any{"action": "tap", "label": "Start game", "params": map[string]any{"x": 0.5, "y": 0.5}},
		map[string]any{"action": "record_mark", "params": map[string]any{"name": "boss fight"}},
		map[string]any{"action": "wait", "params": map[string]any{"ms": 50}},
	}}).(map[string]any)["run_id"].(string)
	waitRun(t, e, "p", rid)
	var report struct {
		Run TestRun `json:"run"`
	}
	if err := decodeMap(execute(t, e, "p", "mobile_test_get", map[string]any{"run_id": rid}), &report); err != nil {
		t.Fatal(err)
	}
	run := report.Run
	if run.Status != "passed" || run.RecordingID == "" {
		t.Fatalf("run: %+v", run)
	}
	rec := waitRecording(t, e, "p", run.RecordingID, "ready", "failed")
	names := []string{}
	for _, m := range rec.Markers {
		names = append(names, m.Name)
	}
	if rec.Status != "ready" || rec.RunID != rid || strings.Join(names, "|") != "Start game|boss fight|step 3: wait" {
		t.Fatalf("recording: %+v", rec)
	}
}

func TestRunValidationOfNewSteps(t *testing.T) {
	e, _, _ := recordFixture(t, "")
	for _, steps := range [][]any{
		{map[string]any{"action": "wait", "params": map[string]any{"ms": 60000}}},
		{map[string]any{"action": "record_mark"}},
		{map[string]any{"action": "wait_for_app", "params": map[string]any{"app_id": "bad id"}}},
	} {
		if _, err := e.Execute(context.Background(), "p", "mobile_test_run", map[string]any{"device_id": "android:phone", "steps": steps}); errorCode(err) != "invalid_argument" {
			t.Fatalf("accepted %v: %v", steps, err)
		}
	}
	if _, err := e.Execute(context.Background(), "p", "mobile_test_run", map[string]any{"device_id": "android:phone", "record": "yes", "steps": []any{map[string]any{"action": "screenshot"}}}); errorCode(err) != "invalid_argument" {
		t.Fatalf("record type: %v", err)
	}
}

func TestRestartFailsUnfinishedRecordings(t *testing.T) {
	dir := t.TempDir()
	db := testDB(t, filepath.Join(dir, "mobile.db"))
	_, err := db.Exec(`INSERT INTO mobile_recordings(id,project_id,kind,status,created_at) VALUES ('rec_x','p','capture','recording',1)`)
	if err != nil {
		t.Fatal(err)
	}
	d := newDrivers(runnerConfig{LockDir: filepath.Join(dir, "locks")})
	d.command = func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("offline") }
	e, err := newEngine(db, dir, d)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Shutdown()
	r, _ := e.recording("p", "rec_x")
	if r.Status != "failed" || !strings.Contains(r.Error, "restarted") {
		t.Fatalf("restart: %+v", r)
	}
}

func TestExportAppStorePreviewAndChecks(t *testing.T) {
	src := testVideo(t, 40, false)
	e, _, _ := recordFixture(t, src)
	s := openFixture(t, e, "p")
	rec := recordingOf(t, execute(t, e, "p", "mobile_record", map[string]any{"action": "start", "session_id": s.ID}))
	if _, err := e.recordingMark("p", rec.ID, "intro"); err != nil {
		t.Fatal(err)
	}
	rec = recordingOf(t, execute(t, e, "p", "mobile_record", map[string]any{"action": "stop", "recording_id": rec.ID}))
	if rec.Status != "ready" {
		t.Fatalf("source: %+v", rec)
	}
	_, err := e.Execute(context.Background(), "p", "mobile_record_export", map[string]any{"recording_id": rec.ID, "preset": "app_store_preview"})
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("40 s source must need a trim: %v", err)
	}
	_, err = e.Execute(context.Background(), "p", "mobile_record_export", map[string]any{"recording_id": rec.ID, "preset": "app_store_preview", "variant": "mac", "orientation": "portrait", "to": 20})
	if err == nil || !strings.Contains(err.Error(), "landscape") {
		t.Fatalf("mac previews are landscape only: %v", err)
	}
	out := recordingOf(t, execute(t, e, "p", "mobile_record_export", map[string]any{"recording_id": rec.ID, "preset": "app_store_preview", "from": 5, "to": 25}))
	got := waitRecording(t, e, "p", out.ID, "ready", "failed")
	if got.Status != "ready" || got.Valid == nil || !*got.Valid || got.Width != 886 || got.Height != 1920 || got.FPS != 30 || got.AudioCodec != "aac" {
		t.Fatalf("export: %+v", got)
	}
	if got.ParentID != rec.ID || got.Preset != "app_store_preview-iphone" || !strings.HasSuffix(got.FileName, "-app_store_preview-iphone.mp4") {
		t.Fatalf("export identity: %+v", got)
	}
	raw := recordingOf(t, execute(t, e, "p", "mobile_record_export", map[string]any{"recording_id": rec.ID, "preset": "raw", "to": 3}))
	if got := waitRecording(t, e, "p", raw.ID, "ready", "failed"); got.Status != "ready" || got.Width != 360 {
		t.Fatalf("raw export: %+v", got)
	}
}

func TestPresetValidationRejectsUnsafeDefinitions(t *testing.T) {
	for _, p := range builtinPresets() {
		if err := validatePreset(p); err != nil {
			t.Fatalf("builtin %s: %v", p.Name, err)
		}
	}
	bad := []Preset{
		{Name: "x;rm", Container: "mp4", VideoCodec: "h264", Audio: "aac", Sizes: map[string]Size{"a": {100, 100}}},
		{Name: "odd", Container: "mp4", VideoCodec: "h264", Audio: "aac", Sizes: map[string]Size{"a": {101, 100}}},
		{Name: "codec", Container: "mp4", VideoCodec: "-vf", Audio: "aac", Sizes: map[string]Size{"a": {100, 100}}},
		{Name: "copyscale", Container: "mp4", VideoCodec: "copy", Audio: "copy", Sizes: map[string]Size{"a": {100, 100}}},
		{Name: "range", Container: "mp4", VideoCodec: "h264", Audio: "aac", Sizes: map[string]Size{"a": {100, 100}}, MinSeconds: 40, MaxSeconds: 30},
	}
	for _, p := range bad {
		if err := validatePreset(p); err == nil {
			t.Errorf("accepted %+v", p)
		}
	}
}

func TestFFmpegArgsForSilentPadExport(t *testing.T) {
	p, _ := findPreset("app_store_preview", nil)
	x, err := planExport(Recording{DurationMS: 60000, Width: 1206, Height: 2622, FPS: 60, Markers: []Marker{{Name: "a", AtMS: 10000}}}, p, map[string]any{"from_mark": "a", "to": 35.0, "variant": "ipad"})
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Join(ffmpegArgs("in.mov", "out.mp4", x), " ")
	for _, want := range []string{"-ss 10.000 -i in.mov", "anullsrc=r=48000:cl=stereo", "-map 1:a:0", "-t 25.000", "scale=1200:1600:force_original_aspect_ratio=decrease,pad=1200:1600", "fps=30", "-level:v 4.0", "-b:a 256k", "-shortest", "+faststart out.mp4"} {
		if !strings.Contains(args, want) {
			t.Errorf("missing %q in %s", want, args)
		}
	}
	land, err := planExport(Recording{DurationMS: 20000, Width: 2622, Height: 1206, FPS: 30, Audio: true}, p, map[string]any{})
	if err != nil || land.Size != (Size{1920, 886}) || land.Orientation != "landscape" {
		t.Fatalf("landscape plan: %+v %v", land, err)
	}
	if args := strings.Join(ffmpegArgs("a", "b", land), " "); strings.Contains(args, "anullsrc") || !strings.Contains(args, "-map 0:a:0?") {
		t.Fatalf("audio passthrough: %s", args)
	}
}

func TestParseProbeHandlesRotation(t *testing.T) {
	p, err := parseProbe([]byte(`{"streams":[{"codec_type":"video","codec_name":"h264","width":2622,"height":1206,"avg_frame_rate":"60/1","side_data_list":[{"rotation":-90}]},{"codec_type":"audio","codec_name":"aac"}],"format":{"duration":"12.345","size":"1000"}}`))
	if err != nil || p.Width != 1206 || p.Height != 2622 || p.FPS != 60 || !p.HasAudio || p.DurationMS != 12345 {
		t.Fatalf("probe: %+v %v", p, err)
	}
	if _, err := parseProbe([]byte(`{"streams":[{"codec_type":"audio"}]}`)); err == nil {
		t.Fatal("audio-only file accepted as video")
	}
}

func TestAppiumRecordingStreamsBase64(t *testing.T) {
	payload := []byte(strings.Repeat("video-bytes/", 5000))
	encoded := strings.ReplaceAll(base64.StdEncoding.EncodeToString(payload), "/", `\/`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/appium/stop_recording_screen") {
			w.Write([]byte(`{"value":null}`))
			return
		}
		w.Write([]byte(`{"sessionId":"s1", "value" : "` + encoded + `"}`))
	}))
	defer srv.Close()
	d := newDrivers(runnerConfig{AppiumURL: srv.URL})
	path := filepath.Join(t.TempDir(), "rec.mp4")
	r, err := d.startAppiumRecording(context.Background(), Session{ProviderID: "s1"}, recordOptions{MaxSeconds: 60}, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != string(payload) {
		t.Fatalf("decoded %d bytes, want %d", len(got), len(payload))
	}
}

func TestRecordPlanChoosesAvailableMethods(t *testing.T) {
	old := lookTool
	defer func() { lookTool = old }()
	lookTool = func(name string) (string, error) {
		if name == "adb" || name == "ffmpeg" || name == "ffprobe" {
			return "/usr/bin/" + name, nil
		}
		return "", errors.New("missing")
	}
	d := newDrivers(runnerConfig{})
	android := Device{Platform: "android", Physical: true}
	plan, err := d.RecordPlan(android, recordOptions{Quality: "auto", Audio: true})
	if err != nil || plan.Method != methodScreenrecord || plan.Audio {
		t.Fatalf("android auto: %+v %v", plan, err)
	}
	if _, err := d.RecordPlan(android, recordOptions{Quality: "high"}); errorCode(err) != "setup_required" || !strings.Contains(err.Error(), "scrcpy") {
		t.Fatalf("android high: %v", err)
	}
	if caps := strings.Join(d.recordCapabilities(android), ","); caps != "record" {
		t.Fatalf("caps: %s", caps)
	}
	if _, err := d.RecordPlan(Device{Platform: "ios", Physical: true}, recordOptions{Quality: "standard"}); errorCode(err) != "setup_required" {
		t.Fatalf("ios without appium: %v", err)
	}
	if args := strings.Join(scrcpyArgs("SER", "/x.mp4", false, 5), " "); !strings.Contains(args, "--no-playback --record /x.mp4") || !strings.Contains(args, "--no-audio") || !strings.Contains(args, "--no-window") || !strings.Contains(args, "--no-control") {
		t.Fatalf("scrcpy args: %s", args)
	}
	if args := strings.Join(scrcpyArgs("SER", "/x.mp4", true, 2), " "); strings.Contains(args, "--no-window") || !strings.Contains(args, "--require-audio") {
		t.Fatalf("scrcpy 2 compatibility/audio requirement: %s", args)
	}
}

func TestRecordingHTTPServesRangesWithinProject(t *testing.T) {
	src := testVideo(t, 1, false)
	e, _, _ := recordFixture(t, src)
	s := openFixture(t, e, "p")
	rec := recordingOf(t, execute(t, e, "p", "mobile_record", map[string]any{"action": "start", "session_id": s.ID}))
	rec = recordingOf(t, execute(t, e, "p", "mobile_record", map[string]any{"action": "stop", "recording_id": rec.ID}))
	app := &App{executor: e, local: e, project: "p"}
	req := httptest.NewRequest("GET", "/api/recordings/"+rec.ID+"/file?project_id=p", nil)
	req.Header.Set("Range", "bytes=0-9")
	w := httptest.NewRecorder()
	app.handleRecording(w, req)
	if w.Code != 206 || w.Body.Len() != 10 || w.Header().Get("Content-Type") != "video/mp4" {
		t.Fatalf("range: %d %d %q", w.Code, w.Body.Len(), w.Header().Get("Content-Type"))
	}
	w = httptest.NewRecorder()
	app.handleRecording(w, httptest.NewRequest("GET", "/api/recordings/"+rec.ID+"/file?project_id=other", nil))
	if w.Code != 403 {
		t.Fatalf("cross-project: %d", w.Code)
	}
}

func TestRemoteRunnerStreamsRecordings(t *testing.T) {
	src := testVideo(t, 1, false)
	e, _, _ := recordFixture(t, src)
	s := openFixture(t, e, "p")
	rec := recordingOf(t, execute(t, e, "p", "mobile_record", map[string]any{"action": "start", "session_id": s.ID}))
	rec = recordingOf(t, execute(t, e, "p", "mobile_record", map[string]any{"action": "stop", "recording_id": rec.ID}))
	token := strings.Repeat("t", 40)
	srv := httptest.NewServer(runnerHandler(e, token))
	defer srv.Close()
	client, err := newRemote(srv.URL, token)
	if err != nil {
		t.Fatal(err)
	}
	res, err := client.stream(context.Background(), "p", rec.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.ContentLength != rec.SizeBytes || res.Header.Get("X-Recording-Name") != rec.FileName {
		t.Fatalf("stream: %d %q", res.ContentLength, res.Header.Get("X-Recording-Name"))
	}
	if _, err := client.stream(context.Background(), "other", rec.ID, ""); errorCode(err) != "not_found" {
		t.Fatalf("remote project isolation: %v", err)
	}
}

// storagePlatform implements the Storage multipart tools in memory.
type storagePlatform struct {
	tk.BasePlatformClient
	mu      sync.Mutex
	parts   [][]byte
	init    map[string]any
	project string
}

func (s *storagePlatform) CallAppContext(context.Context, string, string, map[string]any) (json.RawMessage, error) {
	return nil, errors.New("unused")
}
func (s *storagePlatform) CallAppBatchContext(context.Context, string, []sdk.AppCall, sdk.AppBatchOptions) ([]sdk.AppCallResult, error) {
	return nil, errors.New("unused")
}
func (s *storagePlatform) CallAppResultContext(_ context.Context, app, tool string, in map[string]any, out any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if app != "storage" {
		return errors.New("unexpected app")
	}
	s.project = str(in, "_project_id")
	var result any
	switch tool {
	case "storage_upload_init":
		s.init = in
		result = map[string]any{"upload_id": "up1", "part_size": 1 << 20}
	case "storage_upload_part":
		b, err := base64.StdEncoding.DecodeString(str(in, "content_base64"))
		if err != nil {
			return err
		}
		s.parts = append(s.parts, b)
		result = map[string]any{"ok": true}
	case "storage_upload_complete":
		result = map[string]any{"file": map[string]any{"id": 77}}
	default:
		return errors.New("unexpected tool " + tool)
	}
	return decodeMap(result, out)
}

func TestFinishedRecordingsUploadToStorage(t *testing.T) {
	src := testVideo(t, 2, false)
	e, _, _ := recordFixture(t, src)
	s := openFixture(t, e, "p")
	rec := recordingOf(t, execute(t, e, "p", "mobile_record", map[string]any{"action": "start", "session_id": s.ID}))
	rec = recordingOf(t, execute(t, e, "p", "mobile_record", map[string]any{"action": "stop", "recording_id": rec.ID}))
	pf := &storagePlatform{}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p"), tk.WithPlatform(pf))
	app := &App{executor: e, local: e, project: "p", ctx: ctx}
	app.uploadPending(context.Background(), "")
	st, ok := app.uploadState(rec.ID)
	if !ok || st.Status != "uploaded" || st.StorageFileID != 77 {
		t.Fatalf("upload state: %+v %v", st, ok)
	}
	var total int
	for _, p := range pf.parts {
		total += len(p)
	}
	if int64(total) != rec.SizeBytes || pf.init["folder"] != "/.mobile/recordings/" || pf.project != "p" {
		t.Fatalf("uploaded %d/%d to %v (%s)", total, rec.SizeBytes, pf.init["folder"], pf.project)
	}
	decorated := app.decorateRecordings(map[string]any{"recording": rec}).(map[string]any)["recording"].(map[string]any)
	if decorated["file_url"] == nil || decorated["upload"].(uploadState).StorageFileID != 77 {
		t.Fatalf("decorated: %+v", decorated)
	}
	// A second pass must not upload again.
	pf.parts = nil
	app.uploadPending(context.Background(), "")
	if len(pf.parts) != 0 {
		t.Fatal("recording uploaded twice")
	}
}

func TestConfigPresetsAndCustomExports(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p"), tk.WithConfig(map[string]string{"export_presets_json": `[{"name":"tv_landscape","container":"mp4","video_codec":"h264","sizes":{"tv":{"w":1080,"h":1920}},"orientations":["landscape"],"max_fps":30,"audio":"aac"}]`}))
	app := &App{project: "p", ctx: ctx}
	args, early, err := app.prepareRecordCall("mobile_record_export", map[string]any{"recording_id": "r", "preset": "tv_landscape", "preset_definition": map[string]any{"name": "injected"}})
	if err != nil || early != nil || args["preset_definition"].(Preset).Name != "tv_landscape" {
		t.Fatalf("config preset: %+v %v", args, err)
	}
	_, list, err := app.prepareRecordCall("mobile_record", map[string]any{"action": "presets"})
	if err != nil || len(list.(map[string]any)["presets"].([]Preset)) != len(builtinPresets())+1 {
		t.Fatalf("presets: %v %v", list, err)
	}
	if _, _, err := app.prepareRecordCall("mobile_record_export", map[string]any{"recording_id": "r", "custom": map[string]any{"name": "bad", "container": "avi"}}); errorCode(err) != "invalid_argument" {
		t.Fatalf("custom preset validation: %v", err)
	}
}
