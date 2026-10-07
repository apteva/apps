package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRenderBudgetHEVCAndLimits(t *testing.T) {
	b, err := computeRenderBudget(json.RawMessage(`{}`), 575000, true, 1800, 14400, .2)
	if err != nil || b.EstimatedEncodeSeconds != 2875 || b.EstimatedSeconds <= 2875 || b.EffectiveTimeoutSeconds < b.EstimatedSeconds {
		t.Fatalf("budget=%+v err=%v", b, err)
	}
	for _, raw := range []string{`{"timeout_seconds":1800}`, `{"timeout_seconds":29}`, `{"timeout_seconds":14401}`} {
		if _, err := computeRenderBudget(json.RawMessage(raw), 575000, true, 1800, 14400, .2); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	b, err = computeRenderBudget(json.RawMessage(`{"trim_mode":"auto","timeout_seconds":1800}`), 575000, true, 1800, 14400, .2)
	if err != nil || b.Warning == "" || b.EffectiveTimeoutSeconds != 1800 {
		t.Fatalf("auto=%+v %v", b, err)
	}
}

func TestTrimRejectsUnsafePublicOptions(t *testing.T) {
	for _, extra := range []string{`"trim_mode":"copy"`, `"max_start_drift_ms":-1`, `"max_end_drift_ms":2001`, `"require_dolby_vision":true`, `"hevc_profile":"turbo"`, `"hevc_profile":"fast","encoder_profile":"high"`} {
		if _, err := buildPlan("trim", []string{"1"}, json.RawMessage(`{"start_ms":0,"end_ms":1000,`+extra+`}`), "out.mov", ""); err == nil {
			t.Fatalf("accepted %s", extra)
		}
	}
}

func TestHEVCTrimSpeedProfilesRetainPrecision(t *testing.T) {
	for _, profile := range []string{"legacy", "fast", "balanced", "quality"} {
		params := map[string]any{"start_ms": 0, "end_ms": 1000, "hevc_profile": profile, "_trim_source_video": trimVideoEncoding{Codec: "hevc", PixelFormat: "yuv420p10le", ColorTransfer: "arib-std-b67", ColorPrimaries: "bt2020"}}
		plan, err := buildPlan("trim", []string{"1"}, raw(t, params), "out.mov", "")
		if err != nil {
			t.Fatal(err)
		}
		preset, crf, _ := hevcProfileSettings(profile)
		joined := strings.Join(plan.Args, " ")
		for _, want := range []string{"-c:v libx265", "-preset " + preset, "-crf " + crf, "-pix_fmt yuv420p10le", "-color_trc arib-std-b67", "-color_primaries bt2020"} {
			if !strings.Contains(joined, want) {
				t.Fatalf("%s missing %s: %s", profile, want, joined)
			}
		}
	}
}

func guardedFixture(t *testing.T, hevc bool) string {
	t.Helper()
	skipIfNoFFmpeg(t)
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("Python 3 unavailable")
	}
	dir := t.TempDir()
	source := filepath.Join(dir, "source.mov")
	args := []string{"-y", "-v", "error", "-f", "lavfi", "-i", "testsrc2=s=96x64:r=30:d=5", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=44100:duration=5", "-c:v", "libx264", "-g", "30", "-keyint_min", "30", "-sc_threshold", "0", "-bf", "3", "-c:a", "aac", source}
	if hevc {
		args = []string{"-y", "-v", "error", "-f", "lavfi", "-i", "testsrc2=s=96x64:r=30:d=5", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=5", "-c:v", "libx265", "-preset", "ultrafast", "-g", "30", "-pix_fmt", "yuv420p10le", "-color_trc", "arib-std-b67", "-color_primaries", "bt2020", "-colorspace", "bt2020nc", "-x265-params", "pools=1:frame-threads=1:open-gop=1:keyint=30:min-keyint=30:scenecut=0:log-level=error:colorprim=9:transfer=18:colormatrix=9", "-c:a", "aac", source}
	}
	if out, err := exec.Command("ffmpeg", args...).CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v %s", err, out)
	}
	return source
}

func runGuardedFixture(t *testing.T, source, op string, params map[string]any, remote bool, names ...string) (string, map[string]any, error) {
	t.Helper()
	dir := t.TempDir()
	name := "out.mov"
	if len(names) > 0 {
		name = names[0]
	}
	output := filepath.Join(dir, name)
	row := &RenderRow{Operation: op, SourceFileIDs: []string{"1"}, Params: raw(t, params)}
	plan, err := buildPlan(op, row.SourceFileIDs, row.Params, name, ".mov")
	if err != nil {
		t.Fatal(err)
	}
	// buildPlan treats names as filenames; materialize a safe explicit output.
	args, err := materialiseArgs(plan.Args, []string{source}, dir)
	if err != nil {
		t.Fatal(err)
	}
	args = append(args, output)
	binary, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var cmd *exec.Cmd
	if remote {
		cmd = exec.CommandContext(ctx, "bash", "-c", remoteRuntimeScript(row, binary, source, output, args))
		cmd.Dir = dir
	} else {
		cmd, err = localRuntimeCommand(ctx, row, dir, binary, source, output, args)
		if err != nil {
			t.Fatal(err)
		}
	}
	log, runErr := cmd.CombinedOutput()
	var result map[string]any
	rawResult := runtimeResultFromLog(string(log))
	if len(rawResult) == 0 {
		t.Fatalf("missing result %v %s", runErr, log)
	}
	if err := json.Unmarshal(rawResult, &result); err != nil {
		t.Fatal(err)
	}
	return output, result, runErr
}

func TestAutoTrimCopyHEVCOpenGOPAndH264Parity(t *testing.T) {
	for _, hevc := range []bool{false, true} {
		source := guardedFixture(t, hevc)
		for _, remote := range []bool{false, true} {
			output, result, err := runGuardedFixture(t, source, "trim", map[string]any{"start_ms": 1005, "end_ms": 2990, "trim_mode": "auto", "trim_diagnostics": map[string]any{"actual_start_ms": 1005, "actual_end_ms": 2990, "mode": "accurate", "reencoded": true}}, remote)
			if err != nil {
				t.Fatalf("hevc=%v remote=%v %v: %v", hevc, remote, err, result)
			}
			d := result["trim_diagnostics"].(map[string]any)
			if d["mode"] != "keyframe_copy" || d["actual_start_ms"] != float64(1000) || d["actual_end_ms"] != float64(3000) || d["source_endpoint_hashes_match"] != true {
				t.Fatalf("hevc=%v remote=%v: %v", hevc, remote, d)
			}
			binary, _ := exec.LookPath("ffmpeg")
			log, err := runCompactedFFmpeg(context.Background(), binary, trimValidationArgs(output))
			if err != nil {
				t.Fatal(err)
			}
			v := parseTrimValidation(log)
			if err := checkTrimValidation(v, 2000); err != nil {
				t.Fatal(err)
			}
			if v.Video == nil || v.Video.FramesChecked != 60 || v.Video.FirstFrameMs != 0 || *v.AudioFirstMs != 0 {
				t.Fatalf("hevc=%v remote=%v %s", hevc, remote, log)
			}
		}
	}
}

func TestAutoTrimFallbackAndBudgetPreventsEncoding(t *testing.T) {
	source := guardedFixture(t, false)
	params := map[string]any{"start_ms": 1017, "end_ms": 1520, "trim_mode": "auto", "max_start_drift_ms": 0, "trim_diagnostics": map[string]any{"actual_start_ms": 1017, "actual_end_ms": 1520, "mode": "accurate"}}
	for _, remote := range []bool{false, true} {
		output, result, err := runGuardedFixture(t, source, "trim", params, remote)
		if err != nil {
			t.Fatalf("%v: %v", err, result)
		}
		d := result["trim_diagnostics"].(map[string]any)
		if d["mode"] != "accurate" || d["fallback_reason"] != "no_start_keyframe_within_drift_limit" {
			t.Fatal(d)
		}
		binary, _ := exec.LookPath("ffmpeg")
		log, err := runCompactedFFmpeg(context.Background(), binary, trimValidationArgs(output))
		if err != nil {
			t.Fatal(err)
		}
		if err := checkTrimValidation(parseTrimValidation(log), 503); err != nil {
			t.Fatal(err)
		}
	}
	params["render_budget"] = map[string]any{"estimated_seconds": 20000}
	output, result, err := runGuardedFixture(t, source, "trim", params, false)
	if err == nil || !strings.Contains(result["runtime_error"].(string), "render_budget_exceeded") {
		t.Fatalf("%v %v", err, result)
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatalf("encoded despite budget: %v", err)
	}
}

func TestTwoPassNormalizationLocalRemoteAndFinishedValidation(t *testing.T) {
	source := guardedFixture(t, false)
	for _, remote := range []bool{false, true} {
		output, result, err := runGuardedFixture(t, source, "audio_filter", map[string]any{"mode": "normalize", "target_lufs": -20, "target_peak_dbtp": -3}, remote)
		if err != nil {
			t.Fatalf("%v %v", err, result)
		}
		d := result["audio_normalization"].(map[string]any)
		measured, peak := measureLoudness(t, output)
		if math.Abs(measured+20) > .5 || peak > -2.9 || d["validated"] != true || d["sample_rate"] != float64(44100) || d["timeline_validated"] != true {
			t.Fatalf("remote=%v %v / %.2f %.2f", remote, d, measured, peak)
		}
	}
}

func TestSource86052GuardedCopyAnd90831Loudness(t *testing.T) {
	root := os.Getenv("MEDIA_TRIM_FIXTURE_DIR")
	if root == "" {
		t.Skip("private source replay opt-in")
	}
	source := filepath.Join(root, "source-86052-opening.mov")
	output, result, err := runGuardedFixture(t, source, "trim", map[string]any{"start_ms": 14000, "end_ms": 15870, "trim_mode": "auto", "trim_diagnostics": map[string]any{"actual_start_ms": 14000, "actual_end_ms": 15870}}, true)
	if err != nil {
		t.Fatalf("%v %v", err, result)
	}
	d := result["trim_diagnostics"].(map[string]any)
	if d["mode"] != "keyframe_copy" || d["actual_start_ms"] != float64(14005) || d["copy_frames_checked"] != float64(56) {
		t.Fatal(d)
	}
	binary, _ := exec.LookPath("ffmpeg")
	log, err := runCompactedFFmpeg(context.Background(), binary, trimValidationArgs(output))
	if err != nil {
		t.Fatal(err)
	}
	if err := checkTrimValidation(parseTrimValidation(log), 1867); err != nil {
		t.Fatal(err)
	}
	source = filepath.Join(root, "feedback-0.14.12", "90831.mov")
	output, result, err = runGuardedFixture(t, source, "audio_filter", map[string]any{"mode": "normalize", "target_lufs": -20, "target_peak_dbtp": -3}, true)
	if err != nil {
		t.Fatalf("%v %v", err, result)
	}
	measured, peak := measureLoudness(t, output)
	if math.Abs(measured+20) > .5 || peak > -2.9 {
		t.Fatalf("%.2f %.2f", measured, peak)
	}
}

func TestNormalizationRejectsUndeliverableTarget(t *testing.T) {
	source := guardedFixture(t, false)
	for _, remote := range []bool{false, true} {
		_, result, err := runGuardedFixture(t, source, "audio_filter", map[string]any{"target_lufs": -5, "target_peak_dbtp": -9}, remote)
		if err == nil || !strings.Contains(result["runtime_error"].(string), "audio_normalization_failed") {
			t.Fatalf("remote=%v err=%v result=%v", remote, err, result)
		}
		if d, ok := result["audio_normalization"].(map[string]any); ok && d["validated"] == true {
			t.Fatal(d)
		}
	}
}

func TestInterleavedFrameLogsKeepEveryPicture(t *testing.T) {
	// FFmpeg's concurrent audio/video log calls can split off the showinfo
	// prefix and append the picture record inside an audio checksum line.
	log := "[Parsed_showinfo_0] n:0 pts:0 pts_time:0 duration:1 duration_time:0.033333 fmt:yuv420p\n" +
		"[Parsed_ashowinfo_0] n:1 pts:1024 pts_time:0.023 fmt:fltp channels:1 chlayout:mono rate:44100 nb_samples:1024 checksum:abc [ n:1 pts:1 pts_time:0.033333 duration:1 duration_time:0.033333 fmt:yuv420p\n"
	var collector analysisLogCollector
	for _, line := range strings.Split(log, "\n") {
		collector.add(line)
	}
	v := parseTrimValidation(collector.result())
	if v.Video == nil || v.Video.FramesChecked != 2 || v.Video.LastFrameMs != 33 || v.AudioFirstMs == nil || *v.AudioFirstMs != 23 {
		t.Fatalf("%s", collector.result())
	}
	cmd := exec.Command("awk", compactFrameLogAWK)
	cmd.Stdin = strings.NewReader(log)
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	remote := parseTrimValidation(string(out))
	if remote.Video.FramesChecked != 2 || *remote.AudioFirstMs != 23 {
		t.Fatalf("%s", out)
	}
}

func TestNormalizationPreservesCopiedPresentation(t *testing.T) {
	source := guardedFixture(t, true)
	for _, remote := range []bool{false, true} {
		copy, result, err := runGuardedFixture(t, source, "trim", map[string]any{"start_ms": 1005, "end_ms": 2990, "trim_mode": "auto", "trim_diagnostics": map[string]any{"actual_start_ms": 1005, "actual_end_ms": 2990}}, remote)
		if err != nil || result["trim_diagnostics"].(map[string]any)["mode"] != "keyframe_copy" {
			t.Fatalf("%v %v", err, result)
		}
		output, result, err := runGuardedFixture(t, copy, "audio_filter", map[string]any{"target_lufs": -20, "target_peak_dbtp": -3}, remote)
		if err != nil {
			t.Fatalf("%v %v", err, result)
		}
		binary, _ := exec.LookPath("ffmpeg")
		log, err := runCompactedFFmpeg(context.Background(), binary, trimValidationArgs(output))
		if err != nil {
			t.Fatal(err)
		}
		v := parseTrimValidation(log)
		if err := checkTrimValidation(v, 2000); err != nil {
			t.Fatal(err)
		}
		if v.Video == nil || v.Video.FramesChecked != 60 || v.Video.LastFrameMs != 1967 {
			t.Fatalf("%s", log)
		}
		// Copied 10-bit video must remain 10-bit after audio-only normalization.
		b, err := exec.Command("ffprobe", "-v", "error", "-select_streams", "v:0", "-show_entries", "stream=pix_fmt,color_transfer,color_primaries", "-of", "json", output).Output()
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"yuv420p10le", "arib-std-b67", "bt2020"} {
			if !strings.Contains(string(b), want) {
				t.Fatalf("%s", b)
			}
		}
	}
}

func TestNormalizationAudioOnlyExports(t *testing.T) {
	source := guardedFixture(t, true) // 48 kHz, also supported by Opus.
	for _, name := range []string{"out.wav", "out.mp3", "out.opus"} {
		_, result, err := runGuardedFixture(t, source, "audio_filter", map[string]any{"target_lufs": -20, "target_peak_dbtp": -3}, true, name)
		if err != nil || result["audio_normalization"].(map[string]any)["validated"] != true {
			t.Fatalf("%s: %v %v", name, err, result)
		}
	}
}

// Some valid MOV sources report media duration at the last picture's PTS,
// while their presentation edit still includes that picture (production 90825).
func normalizationBoundaryFixture(t *testing.T) string {
	t.Helper()
	source := guardedFixture(t, true)
	dir := filepath.Dir(source)
	helper := filepath.Join(dir, "helper.py")
	if err := os.WriteFile(helper, []byte(renderRuntimePython), 0600); err != nil {
		t.Fatal(err)
	}
	script := `import sys,struct
sys.path.insert(0, sys.argv[1])
from helper import movie_metadata,movie_boxes
with open(sys.argv[2], 'r+b') as f:
 offset,header,data=movie_metadata(f)
 for a,b,k in movie_boxes(data,0,len(data)):
  if k != b'trak': continue
  for m,n,t in movie_boxes(data,a,b):
   if t != b'mdia': continue
   children=list(movie_boxes(data,m,n))
   if not any(k==b'hdlr' and data[h+8:h+12]==b'vide' for h,_,k in children): continue
   for o,_,kind in children:
    if kind==b'mdhd':
     pos=o+(16 if data[o]==0 else 24);fmt='>I' if data[o]==0 else '>Q'
     duration=struct.unpack_from(fmt,data,pos)[0]
     struct.pack_into(fmt,data,pos,duration-duration//150)
 f.seek(offset+header);f.write(data)
`
	if out, err := exec.Command("python3", "-c", script, dir, source).CombinedOutput(); err != nil {
		t.Fatalf("boundary fixture: %v %s", err, out)
	}
	return source
}

func TestNormalizationRetainsLastPictureAtMediaDuration(t *testing.T) {
	source := normalizationBoundaryFixture(t)
	for _, remote := range []bool{false, true} {
		_, result, err := runGuardedFixture(t, source, "audio_filter", map[string]any{"target_lufs": -20, "target_peak_dbtp": -1.5}, remote)
		if err != nil {
			t.Fatalf("remote=%v %v %v", remote, err, result)
		}
		d := result["audio_normalization"].(map[string]any)
		if d["video_frames_expected"] != float64(150) || d["video_frames_checked"] != float64(150) || d["video_pictures_match"] != true {
			t.Fatalf("remote=%v %v", remote, d)
		}
	}
}

func TestSource90825NormalizationRetainsAll150Pictures(t *testing.T) {
	root := os.Getenv("MEDIA_TRIM_FIXTURE_DIR")
	if root == "" {
		t.Skip("private production regression opt-in")
	}
	source := filepath.Join(root, "feedback-0.14.14", "90825.mov")
	for _, remote := range []bool{false, true} {
		_, result, err := runGuardedFixture(t, source, "audio_filter", map[string]any{"target_lufs": -20, "target_peak_dbtp": -1.5}, remote)
		if err != nil {
			t.Fatalf("remote=%v %v %v", remote, err, result)
		}
		d := result["audio_normalization"].(map[string]any)
		if d["video_frames_expected"] != float64(150) || d["video_frames_checked"] != float64(150) || d["video_pictures_match"] != true || d["validated"] != true {
			t.Fatalf("remote=%v %v", remote, d)
		}
	}
}

func TestNormalizationRejectsMissingLastPicture(t *testing.T) {
	source := guardedFixture(t, false)
	dir := t.TempDir()
	output := filepath.Join(dir, "out.mov")
	row := &RenderRow{Operation: "audio_filter", Params: raw(t, map[string]any{"target_lufs": -20, "target_peak_dbtp": -1.5})}
	binary, _ := exec.LookPath("ffmpeg")
	args := []string{"-y", "-v", "error", "-i", source, "-map", "0:v:0", "-map", "0:a:0", "-c:v", "copy", "-c:a", "aac", "-af", "volume=0", "-ar", "44100", "-frames:v", "149", output}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd, err := localRuntimeCommand(ctx, row, dir, binary, source, output, args)
	if err != nil {
		t.Fatal(err)
	}
	log, err := cmd.CombinedOutput()
	var result map[string]any
	if jsonErr := json.Unmarshal(runtimeResultFromLog(string(log)), &result); jsonErr != nil {
		t.Fatal(jsonErr)
	}
	if err == nil || !strings.Contains(fmt.Sprint(result["runtime_error"]), "retained video pictures or timestamps changed") {
		t.Fatalf("lost picture accepted: %v %s", err, log)
	}
	d := result["audio_normalization"].(map[string]any)
	if d["validated"] != false || d["video_frames_expected"] != float64(150) || d["video_frames_checked"] != float64(149) {
		t.Fatal(d)
	}
}

func TestSharedTrimValidationAndNormalizationEvidence(t *testing.T) {
	source := guardedFixture(t, true)
	for _, remote := range []bool{false, true} {
		copy, result, err := runGuardedFixture(t, source, "trim", map[string]any{"start_ms": 1005, "end_ms": 2990, "trim_mode": "auto", "trim_diagnostics": map[string]any{}}, remote)
		if err != nil {
			t.Fatalf("%v %v", err, result)
		}
		rawStatus, err := os.ReadFile(filepath.Join(filepath.Dir(copy), "runtime-status.json"))
		if err != nil {
			t.Fatal(err)
		}
		var status map[string]any
		if json.Unmarshal(rawStatus, &status) != nil || status["diagnostics"].(map[string]any)["trim_diagnostics"].(map[string]any)["mode"] != "keyframe_copy" {
			t.Fatalf("lost live copy mode: %s", rawStatus)
		}
		evidence := result["video_evidence"].(map[string]any)
		validation := parseTrimValidation(result["trim_validation_log"].(string))
		if validation.Video.FramesChecked != 60 || evidence["frames_checked"] != float64(60) || checkTrimValidation(validation, 2000) != nil {
			t.Fatalf("%+v %v", validation, evidence)
		}
		_, normalized, err := runGuardedFixture(t, copy, "audio_filter", map[string]any{"target_lufs": -20, "target_peak_dbtp": -3, "_validated_video_evidence": evidence}, remote)
		if err != nil {
			t.Fatalf("%v %v", err, normalized)
		}
		d := normalized["audio_normalization"].(map[string]any)
		if d["video_validation_reused"] != true || d["video_frames_checked"] != float64(60) || d["validated"] != true {
			t.Fatalf("remote=%v %v", remote, d)
		}
		// A stale/altered packet proof falls back to full picture comparison.
		bad := map[string]any{"algorithm_version": "media-shared-validation-1", "decode_ok": true, "frames_checked": 60, "packet_signature": "wrong"}
		_, normalized, err = runGuardedFixture(t, copy, "audio_filter", map[string]any{"target_lufs": -20, "target_peak_dbtp": -3, "_validated_video_evidence": bad}, remote)
		if err != nil || normalized["audio_normalization"].(map[string]any)["video_validation_reused"] != false {
			t.Fatalf("%v %v", err, normalized)
		}
	}
}

func TestSharedTrimHasOneFullOutputDecodeAndReuseHasNone(t *testing.T) {
	source := guardedFixture(t, true)
	realFFmpeg, _ := exec.LookPath("ffmpeg")
	realProbe, _ := exec.LookPath("ffprobe")
	dir := t.TempDir()
	trace := filepath.Join(dir, "commands.log")
	wrapper := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + shellQuote(trace) + "\nexec " + shellQuote(realFFmpeg) + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "ffmpeg"), []byte(wrapper), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realProbe, filepath.Join(dir, "ffprobe")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	copy, result, err := runGuardedFixture(t, source, "trim", map[string]any{"start_ms": 1005, "end_ms": 2990, "trim_mode": "auto", "trim_diagnostics": map[string]any{}}, false)
	if err != nil {
		t.Fatalf("%v %v", err, result)
	}
	raw, _ := os.ReadFile(trace)
	scans := 0
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.Contains(line, "-i "+copy) && strings.Contains(line, "-f framemd5") {
			scans++
		}
	}
	if scans != 1 {
		t.Fatalf("full output decodes=%d: %s", scans, raw)
	}
	if err := os.WriteFile(trace, nil, 0600); err != nil {
		t.Fatal(err)
	}
	_, result, err = runGuardedFixture(t, copy, "audio_filter", map[string]any{"target_lufs": -20, "target_peak_dbtp": -3, "_validated_video_evidence": result["video_evidence"]}, false)
	if err != nil {
		t.Fatalf("%v %v", err, result)
	}
	raw, _ = os.ReadFile(trace)
	if strings.Contains(string(raw), "-f framemd5") || result["audio_normalization"].(map[string]any)["video_validation_reused"] != true {
		t.Fatalf("video unnecessarily decoded: %s %v", raw, result)
	}
}
