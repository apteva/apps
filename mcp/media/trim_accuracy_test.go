package main

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Unlike the old all-intra fixture, this contains long GOPs and B frames.
// Each picture encodes its index into luma, so preroll and off-by-one end
// pictures are visible independently of container duration/seek metadata.
func TestTrimAccurateNonKeyframeInterval(t *testing.T) {
	skipIfNoFFmpeg(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "long-gop.mp4")
	cmd := exec.Command("ffmpeg", "-y", "-v", "error", "-f", "lavfi", "-i", "nullsrc=s=64x64:r=30:d=17,geq=lum='20+mod(N*37,210)':cb=128:cr=128", "-f", "lavfi", "-i", "aevalsrc=if(gte(t\\,14.5)\\,0.2*sin(2*PI*440*t)\\,0):s=48000:d=17", "-c:v", "libx264", "-crf", "1", "-bf", "3", "-g", "180", "-keyint_min", "180", "-sc_threshold", "0", "-c:a", "aac", src)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v %s", err, out)
	}
	for _, span := range []struct {
		name               string
		start, end         int64
		first, last, count int
	}{
		{"aligned", 14000, 15000, 420, 449, 30},
		{"fractional", 14017, 15233, 421, 456, 36},
	} {
		t.Run(span.name, func(t *testing.T) {
			out := runOpAgainstFile(t, "trim", []string{src}, map[string]any{"start_ms": span.start, "end_ms": span.end}, span.name+".mp4", dir)
			pixels, err := exec.Command("ffmpeg", "-v", "error", "-i", out, "-map", "0:v:0", "-f", "rawvideo", "-pix_fmt", "gray", "-").Output()
			if err != nil {
				t.Fatal(err)
			}
			if len(pixels) != span.count*64*64 {
				t.Fatalf("retained frames=%d want %d", len(pixels)/(64*64), span.count)
			}
			for _, edge := range []struct{ frame, index int }{{0, span.first}, {span.count - 1, span.last}} {
				expected := float64(20+(edge.index*37)%210-16) * 255 / 219
				var mean float64
				for _, p := range pixels[edge.frame*4096 : (edge.frame+1)*4096] {
					mean += float64(p)
				}
				mean /= 4096
				if math.Abs(mean-expected) > 2 {
					t.Fatalf("picture %d luma %.2f want %.2f (source frame %d)", edge.frame, mean, expected, edge.index)
				}
			}
			log, err := runCompactedFFmpeg(context.Background(), "ffmpeg", trimValidationArgs(out))
			if err != nil {
				t.Fatal(err)
			}
			v := parseTrimValidation(log)
			if err := checkTrimValidation(v); err != nil {
				t.Fatal(err)
			}
			if v.Video.FramesChecked != int64(span.count) || v.Video.FirstFrameMs != 0 || v.AudioFirstMs == nil || *v.AudioFirstMs != 0 {
				t.Fatalf("validation=%+v", v)
			}
			// Verify the audible event remains on the requested source timeline.
			audioLog, err := exec.Command("ffmpeg", "-v", "info", "-i", out, "-vn", "-af", "silencedetect=noise=-50dB:d=0.1", "-f", "null", "-").CombinedOutput()
			if err != nil {
				t.Fatal(err)
			}
			m := silenceEndRE.FindStringSubmatch(string(audioLog))
			if m == nil {
				t.Fatal("missing initial silence")
			}
			if delta := math.Abs(float64(secondsToMs(m[1]) - (14500 - span.start))); delta > 12 {
				t.Fatalf("audio event displaced by %.1f ms", delta)
			}
		})
	}
}

func TestEveryFrameBlackOpeningAndEnding(t *testing.T) {
	skipIfNoFFmpeg(t)
	for _, case_ := range []struct {
		name, enable    string
		opening, ending bool
		segments        int
	}{
		{"both", "eq(n,0)+eq(n,29)", true, true, 2},
		{"penultimate", "eq(n,28)", false, false, 1},
		{"clean", "0", false, false, 0},
	} {
		t.Run(case_.name, func(t *testing.T) {
			src := filepath.Join(t.TempDir(), "edges.mp4")
			filter := "color=blue:s=64x64:r=30:d=1,drawbox=color=black:t=fill:enable='" + case_.enable + "'"
			if out, err := exec.Command("ffmpeg", "-y", "-v", "error", "-f", "lavfi", "-i", filter, "-c:v", "libx264", "-crf", "0", src).CombinedOutput(); err != nil {
				t.Fatalf("fixture: %v %s", err, out)
			}
			opts := analysisOptions{EndMs: 1000}
			for _, remote := range []bool{false, true} {
				var log string
				if remote {
					out, err := exec.Command("bash", "-c", buildRemoteAnalysisScript("ffmpeg", src, &MediaRow{HasVideo: true}, opts)).CombinedOutput()
					if err != nil {
						t.Fatalf("remote: %v %s", err, out)
					}
					wire, err := parseRemoteAnalysisResult(string(out))
					if err != nil {
						t.Fatal(err)
					}
					log, err = decodeRemoteAnalysisLog(wire.VisualLog)
					if err != nil {
						t.Fatal(err)
					}
				} else {
					var err error
					log, err = runAnalysisFFmpeg(context.Background(), "ffmpeg", visualAnalysisArgs(src, &MediaRow{HasVideo: true}, opts))
					if err != nil {
						t.Fatal(err)
					}
				}
				v := parseVisualAnalysis(log, 0, 1000)
				fv := v.FrameValidation
				if fv == nil || fv.FramesChecked != 30 || fv.OpeningBlack != case_.opening || fv.EndingBlack != case_.ending || len(v.BlackSegments) != case_.segments {
					t.Fatalf("remote=%v validation=%+v segments=%+v log=%s", remote, fv, v.BlackSegments, log)
				}
				for _, s := range v.BlackSegments {
					if s.DurationMs < 32 || s.DurationMs > 34 {
						t.Fatalf("single black frame interval=%+v", s)
					}
				}
			}
		})
	}
}

func TestTrimVideoOnlyAudioOnlyAndDelayedAudio(t *testing.T) {
	skipIfNoFFmpeg(t)
	dir := t.TempDir()
	for _, kind := range []string{"video", "audio", "delayed"} {
		t.Run(kind, func(t *testing.T) {
			src := filepath.Join(dir, kind+".mp4")
			args := []string{"-y", "-v", "error"}
			if kind != "audio" {
				args = append(args, "-f", "lavfi", "-i", "color=blue:s=64x64:r=30:d=3")
			}
			if kind != "video" {
				if kind == "delayed" {
					args = append(args, "-itsoffset", "0.5")
				}
				args = append(args, "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=2")
			}
			args = append(args, "-c:v", "libx264", "-c:a", "aac", src)
			if out, err := exec.Command("ffmpeg", args...).CombinedOutput(); err != nil {
				t.Fatalf("fixture: %v %s", err, out)
			}
			name := kind + "-trim.mp4"
			if kind == "audio" {
				name = kind + "-trim.wav"
			}
			out := runOpAgainstFile(t, "trim", []string{src}, map[string]any{"start_ms": 0, "end_ms": 1800}, name, dir)
			log, err := runCompactedFFmpeg(context.Background(), "ffmpeg", trimValidationArgs(out))
			if err != nil {
				t.Fatal(err)
			}
			v := parseTrimValidation(log)
			if err := checkTrimValidation(v); err != nil {
				t.Fatal(err)
			}
			if kind == "delayed" {
				b, err := exec.Command("ffmpeg", "-v", "info", "-i", out, "-vn", "-af", "silencedetect=noise=-50dB:d=0.1", "-f", "null", "-").CombinedOutput()
				if err != nil {
					t.Fatal(err)
				}
				m := silenceEndRE.FindStringSubmatch(string(b))
				if m == nil || secondsToMs(m[1]) < 450 || secondsToMs(m[1]) > 510 {
					t.Fatalf("original audio delay lost: %s", b)
				}
			}
		})
	}
}

// Private production evidence stays outside the public repo. This replays the
// exact 14s opening cut against the captured first 20s of source 86052.
func TestTrimProduction86052Opening(t *testing.T) {
	root := os.Getenv("MEDIA_TRIM_FIXTURE_DIR")
	if root == "" {
		t.Skip("set MEDIA_TRIM_FIXTURE_DIR for private source86052")
	}
	skipIfNoFFmpeg(t)
	ctx := newTestCtx(t)
	metadata, err := os.ReadFile(filepath.Join(root, "86052-metadata.json"))
	if err != nil {
		t.Fatal(err)
	}
	var source struct {
		Media MediaRow `json:"media"`
	}
	if err := json.Unmarshal(metadata, &source); err != nil {
		t.Fatal(err)
	}
	source.Media.FileID = "86052"
	source.Media.ProjectID = testProj
	// Use the catalog's raw source metadata, including HDR precision/color.
	_, err = ctx.AppDB().Exec(`INSERT INTO media(project_id,file_id,source_sha256,raw_probe) VALUES(?,?,?,?)`, testProj, "86052", source.Media.SourceSHA256, string(source.Media.RawProbe))
	if err != nil {
		t.Fatal(err)
	}
	raw := prepareTrimParams(ctx.AppDB(), testProj, "trim", []string{"86052"}, json.RawMessage(`{"start_ms":14000,"end_ms":17000}`))
	var params map[string]any
	json.Unmarshal(raw, &params)
	out := runOpAgainstFile(t, "trim", []string{filepath.Join(root, "source-86052-opening.mov")}, params, "corrected-opening.mov", t.TempDir())
	log, err := runCompactedFFmpeg(context.Background(), "ffmpeg", trimValidationArgs(out))
	if err != nil {
		t.Fatal(err)
	}
	v := parseTrimValidation(log)
	if err := checkTrimValidation(v); err != nil {
		t.Fatal(err)
	}
	if v.Video == nil || v.Video.FramesChecked < 89 || v.Video.OpeningBlack || v.Video.EndingBlack {
		t.Fatalf("production opening=%+v", v)
	}
	// Compare the retained first picture to the first source picture at or
	// after 14.000s, independently of timestamps on the newly encoded file.
	firstPixels := func(file, filter string) []byte {
		args := []string{"-v", "error", "-i", file, "-vf", filter + ",scale=160:90", "-frames:v", "1", "-f", "rawvideo", "-pix_fmt", "gray", "-"}
		b, err := exec.Command("ffmpeg", args...).Output()
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	expected := firstPixels(filepath.Join(root, "source-86052-opening.mov"), `select=gte(t\,14)`)
	actual := firstPixels(out, "null")
	if len(actual) != len(expected) || len(actual) == 0 {
		t.Fatal("missing first picture comparison")
	}
	var errorSum float64
	for i, pixel := range actual {
		errorSum += math.Abs(float64(pixel) - float64(expected[i]))
	}
	if mae := errorSum / float64(len(actual)); mae > 3 {
		t.Fatalf("first picture differs from requested source frame: MAE %.3f", mae)
	}
	probeBytes, err := exec.Command("ffprobe", "-v", "error", "-show_streams", "-of", "json", out).Output()
	if err != nil {
		t.Fatal(err)
	}
	var probe struct {
		Streams []map[string]any `json:"streams"`
	}
	json.Unmarshal(probeBytes, &probe)
	for _, s := range probe.Streams {
		if s["codec_type"] == "video" {
			if s["pix_fmt"] != "yuv420p10le" || s["color_transfer"] != "arib-std-b67" {
				t.Fatalf("HDR changed: %+v", s)
			}
			n, _ := strconv.ParseFloat(s["start_time"].(string), 64)
			if math.Abs(n) > .002 {
				t.Fatalf("video start=%.6f", n)
			}
		}
	}
	// Save only sanitized diagnostic evidence for this private replay.
	b, _ := json.MarshalIndent(v, "", "  ")
	os.WriteFile(filepath.Join(root, "corrected-opening-validation.json"), b, 0600)
	if bytes, err := os.ReadFile(out); err == nil {
		os.WriteFile(filepath.Join(root, "corrected-opening.mov"), bytes, 0600)
	}
	if strings.Contains(string(b), "https://") {
		t.Fatal("validation leaked URL")
	}
}

func TestTrimVFRAndRemoteValidation(t *testing.T) {
	skipIfNoFFmpeg(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "vfr.mp4")
	args := []string{"-v", "error", "-y", "-f", "lavfi", "-i", "nullsrc=s=64x64:r=30:d=1,geq=lum='20+mod(N*37,210)':cb=128:cr=128", "-vf", "setpts='if(lt(N,15),N/30,0.5+(N-15)/15)/TB'", "-fps_mode", "vfr", "-c:v", "libx264", "-crf", "1", src}
	if out, err := exec.Command("ffmpeg", args...).CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v %s", err, out)
	}
	plan, err := buildPlan("trim", []string{"1"}, json.RawMessage(`{"start_ms":750,"end_ms":1250}`), "vfr-trim.mp4", "")
	if err != nil {
		t.Fatal(err)
	}
	remoteArgs, err := materialiseRemoteArgs(plan.Args, []string{src})
	if err != nil {
		t.Fatal(err)
	}
	script := shellCommand("ffmpeg", append(remoteArgs, filepath.Join(dir, plan.Filename))) + "\n" + trimValidationScript("ffmpeg", filepath.Join(dir, plan.Filename), 500)
	cmd := exec.Command("bash", "-c", script)
	cmd.Dir = dir
	logs, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("remote accurate trim: %v %s", err, logs)
	}
	v := parseTrimValidation(string(logs))
	if err := checkTrimValidation(v, 500); err != nil {
		t.Fatal(err)
	}
	// Original VFR frame timestamps are .7667,.8333,...,1.2333.
	if v.Video == nil || v.Video.FramesChecked != 8 || v.Video.FirstFrameMs != 0 {
		t.Fatalf("VFR interval=%+v", v)
	}
	// The exact remote pre-upload guard rejects nonzero openings and
	// truncated endings, rather than relying on an upload succeeding.
	for _, bad := range []string{
		"[Parsed_showinfo_0] n:0 pts_time:0.209 duration_time:0.033\n",
		"[Parsed_showinfo_0] n:0 pts_time:0 duration_time:0.033\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, "bad.log"), []byte(bad), 0600); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("bash", "-c", "awk -v strict=1 -v expected=0.5 "+shellQuote(compactFrameLogAWK)+" bad.log")
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(out), "trim_validation_failed:") {
			t.Fatalf("remote guard accepted bad output: %s %v", out, err)
		}
	}
}
