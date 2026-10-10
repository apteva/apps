package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	tk "github.com/apteva/app-sdk/testkit"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSharedAdmissionAcrossMediaOperations(t *testing.T) {
	app := newTestCtx(t)
	var active, peak atomic.Int32
	var wg sync.WaitGroup
	for _, op := range []string{"preview_full", "preview_legacy", "sampling", "indexing", "render", "transcript_audio"} {
		wg.Add(1)
		go func(op string) {
			defer wg.Done()
			ctx, release, err := acquireMediaWork(context.Background(), app, 2)
			if err != nil {
				t.Error(err)
				return
			}
			defer release()
			_, nested, err := acquireMediaWork(ctx, app, 1)
			if err != nil {
				t.Error(err)
				return
			}
			nested()
			n := active.Add(1)
			for previous := peak.Load(); n > previous && !peak.CompareAndSwap(previous, n); previous = peak.Load() {
			}
			time.Sleep(time.Millisecond * 5)
			active.Add(-1)
		}(op)
	}
	wg.Wait()
	if peak.Load() != 1 {
		t.Fatalf("concurrent heavy work=%d", peak.Load())
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, release, err := acquireMediaWork(canceled, app, 1); err == nil {
		release()
		t.Fatal("canceled request admitted")
	}
}
func TestResourceFailureStopsNestedFallbackAndReleasesAdmission(t *testing.T) {
	app := newTestCtx(t)
	ctx, release, err := acquireMediaWork(context.Background(), app, 2)
	if err != nil {
		t.Fatal(err)
	}
	if markMediaResourceFailure(ctx, errors.New("exit status 1"), "codec unsupported", 1) != nil {
		t.Fatal("ordinary errors poisoned work")
	}
	failure := markMediaResourceFailure(ctx, nil, "", 137)
	if failure == nil || ctx.Err() == nil {
		t.Fatal("kill did not stop work")
	}
	if _, _, err := acquireMediaWork(ctx, app, 1); err == nil {
		t.Fatal("nested fallback admitted")
	}
	raw := cropWorkError([]byte(`{"crop_w":606,"require_action_preservation":false,"crop_fallback":"contain"}`), failure)
	if _, err := applyCropCompositionPolicy(raw); err == nil || !strings.Contains(err.Error(), "media_resource_exhausted") {
		t.Fatalf("policy allowed render: %s %v", raw, err)
	}
	release()
	release() // release must be idempotent
	next, done, err := acquireMediaWork(context.Background(), app, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	if mediaWorkFailure(next) != nil {
		t.Fatal("next request inherited failure")
	}
}
func TestTranscriptAudioOOMDoesNotTryPCM(t *testing.T) {
	log := filepath.Join(t.TempDir(), "calls")
	t.Setenv("FAKE_FFMPEG_LOG", log)
	binary := writeFakeFFmpeg(t, `printf 'call\n' >> "$FAKE_FFMPEG_LOG"; echo 'Cannot allocate memory' >&2; exit 137`)
	fallback, err := runTranscriptAudioFFmpeg(context.Background(), binary, "source", filepath.Join(t.TempDir(), "audio.mp3"))
	if err == nil || fallback || !strings.Contains(err.Error(), "media_resource_exhausted") {
		t.Fatalf("%v %v", fallback, err)
	}
	b, _ := os.ReadFile(log)
	if strings.Count(string(b), "call") != 1 {
		t.Fatalf("fallback ran: %s", b)
	}
}
func TestRemoteOOMStopsCandidateAndCodecFallbacks(t *testing.T) {
	for _, kind := range []string{"samples", "index", "audio"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			log := filepath.Join(root, "calls")
			t.Setenv("FAKE_FFMPEG_LOG", log)
			ff := writeFakeFFmpeg(t, `printf 'call\n' >> "$FAKE_FFMPEG_LOG"; echo 'Cannot allocate memory' >&2; exit 137`)
			script := ""
			switch kind {
			case "samples":
				script, _ = buildRemoteSmartCropSampleScript(ff, "source", []int64{0, 1000, 2000}, 1920)
			case "index":
				probe := filepath.Join(root, "ffprobe")
				writeExecutable(t, probe, `echo '{"streams":[{"codec_type":"video","width":1920,"height":1080}],"format":{"duration":"10","format_name":"mov"}}'`)
				script = buildRemoteIndexScript(remoteIndexScriptInputs{FFmpeg: ff, FFprobe: probe, SignedURL: "source", FileID: "1", ThumbWidth: 320, ThumbSeek: 1})
			case "audio":
				script = buildRemoteTranscriptAudioScript(remoteTranscriptAudioScriptInputs{FFmpeg: ff, SignedURL: "source", FileID: "1"})
			}
			out, err := exec.Command("bash", "-c", script).CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 137 {
				t.Fatalf("OOM suppressed: %v %s", err, out)
			}
			b, _ := os.ReadFile(log)
			if strings.Count(string(b), "call") != 1 {
				t.Fatalf("fallback ran: %s", b)
			}
		})
	}
}
func TestNativeThumbnailMemoryBound(t *testing.T) {
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg unavailable")
	}
	source := filepath.Join(t.TempDir(), "uhd.mp4")
	cmd := mediaFFmpegCommand(context.Background(), ff, []string{"-v", "error", "-f", "lavfi", "-i", "testsrc2=size=3840x2160:rate=30:duration=1", "-c:v", "libx264", "-preset", "ultrafast", source})
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v %s", err, out)
	}
	output := filepath.Join(t.TempDir(), "thumb.jpg")
	if err := extractVideoFrame(context.Background(), ff, source, output, 0, 320); err != nil {
		t.Fatal(err)
	}
	img, err := decodeSmartCropFrame(output)
	if err != nil {
		t.Fatal(err)
	}
	if img.Bounds().Dx() != 320 {
		t.Fatal(img.Bounds())
	}
}
func TestPoseCacheOnDiskAndResourceErrorIdentity(t *testing.T) {
	for _, hybrid := range []bool{true, false} {
		if !strings.HasPrefix(poseRemoteRuntimeRoot(hybrid), "/var/tmp/") {
			t.Fatal(poseRemoteRuntimeRoot(hybrid))
		}
	}
	f := parsePoseRuntimeFailure(`APTEVA_POSE_ERROR:{"code":"media_resource_exhausted","at_ms":1234,"attempts":1}`)
	if f == nil || f.AtMs == nil || *f.AtMs != 1234 {
		t.Fatalf("lost failure: %+v", f)
	}
	var p map[string]any
	json.Unmarshal(cropWorkError([]byte(`{}`), errors.New("media_resource_exhausted")), &p)
	if p["runtime_error"] != "media_resource_exhausted" {
		t.Fatal(p)
	}
}

func TestLegacyCropPlanningWaitsForRenderAdmission(t *testing.T) {
	app := newTestCtx(t)
	_, release, err := acquireMediaWork(context.Background(), app, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	deadline, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	raw := preprocessSmartCropUncached(deadline, app, &storageClient{}, testProj, "crop", []string{"1"}, []byte(`{"target_ratio":"9:16","smart_crop_engine":"legacy"}`))
	if err := cropProcessingError(raw); err == nil {
		t.Fatalf("legacy bypassed admission: %s", raw)
	}
}
func TestPythonTrimOOMCannotReencode(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python unavailable")
	}
	script := renderRuntimePython + `
probe=lambda *args: {'streams':[]}
copy_trim=lambda *args: check_resources(-9)
encode=lambda *args: (_ for _ in ()).throw(AssertionError('encoded after OOM'))
try:
    main({'operation':'trim','params':{},'ffprobe':'ffprobe','source':'source'}, {})
    raise AssertionError('accepted OOM')
except MediaResourceFailure:
    pass
`
	// Import namespace prevents the standalone entry point from executing.
	runner := "ns={'__name__':'regression'};exec(" + strconv.Quote(script) + ",ns)"
	if out, err := exec.Command(python, "-c", runner).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
}

func TestSharedBudgetPermitsTwoLightOperationsButBoundsThird(t *testing.T) {
	app := newTestCtx(t)
	first, release1, err := acquireMediaWork(context.Background(), app, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer release1()
	_, release2, err := acquireMediaWork(context.Background(), app, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer release2()
	if first.Err() != nil {
		t.Fatal(first.Err())
	}
	deadline, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, done, err := acquireMediaWork(deadline, app, 1); err == nil {
		done()
		t.Fatal("third operation bypassed host budget")
	}
	release1()
	ctx, done, err := acquireMediaWork(context.Background(), app, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	if ctx.Err() != nil {
		t.Fatal(ctx.Err())
	}
}

func TestOOMRenderFailsRatherThanReportingUserCancellation(t *testing.T) {
	uploads := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/files/1/content"):
			fmt.Fprint(w, "source")
		case strings.HasSuffix(r.URL.Path, "/files/1"):
			fmt.Fprint(w, `{"file":{"id":1,"name":"source.mp4","folder":"/","content_type":"video/mp4"}}`)
		case strings.HasSuffix(r.URL.Path, "/uploads"):
			uploads = true
			http.Error(w, "unexpected upload", 500)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	app := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj), tk.WithEnv("APTEVA_GATEWAY_URL", srv.URL), tk.WithEnv("APTEVA_OUTBOUND_TOKEN", "test"))
	old := globalCtx
	globalCtx = app
	t.Cleanup(func() { globalCtx = old })
	_, err := insertRender(app.AppDB(), testProj, "transcode", []string{"1"}, map[string]any{"format": "mp4"}, "out.mp4", "/renders/", "")
	if err != nil {
		t.Fatal(err)
	}
	row, err := claimNextPending(app.AppDB())
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	binary := writeFakeFFmpeg(t, `echo 'Cannot allocate memory' >&2; exit 137`)
	runOneRender(app, row, &localExecutor{ffmpegPath: binary, scratchRoot: root, outputFolder: "/renders/"}, nil, 30)
	got, err := getRender(app.AppDB(), testProj, row.ID)
	if err != nil || got.Status != "failed" || !strings.Contains(got.Error, "media_resource_exhausted") || uploads {
		t.Fatalf("%+v error=%v uploads=%v", got, err, uploads)
	}
}
