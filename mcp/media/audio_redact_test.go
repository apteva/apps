package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func redactPtr(v int64) *int64 { return &v }
func redactionParamsForTest(t *testing.T, method string, preview bool) map[string]any {
	t.Helper()
	p := audioRedactParams{Method: method, Preview: preview, Selections: []audioSelection{{StartMs: redactPtr(2000), EndMs: redactPtr(3000)}}}
	if method == "room_tone" {
		p.RoomTone = &audioInterval{0, 500}
	}
	plan, err := makeAudioRedactionPlan(p, nil, 5000, "source-hash")
	if err != nil {
		t.Fatal(err)
	}
	return map[string]any{"method": method, "preview": preview, "_redaction_plan": plan, "_source_sample_rate": 44100}
}

func TestAudioRedactionSelectionsAndGuards(t *testing.T) {
	words := []TranscriptWord{{100, 300, "Hello"}, {350, 600, "world!"}, {700, 900, "hello"}, {950, 1200, "world"}}
	p := audioRedactParams{Method: "mute", Selections: []audioSelection{{Text: "HELLO world", Occurrence: 2}}}
	plan, err := makeAudioRedactionPlan(p, words, 5000, "hash")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Ranges) != 1 || plan.Ranges[0] != (audioInterval{660, 1240}) || plan.FadeMs != 10 {
		t.Fatalf("plan=%+v", plan)
	}
	cases := []struct {
		name  string
		p     audioRedactParams
		words []TranscriptWord
		code  string
	}{
		{"ambiguous", audioRedactParams{Method: "mute", Selections: []audioSelection{{Text: "hello"}}}, words, "ambiguous_audio_selection"},
		{"no_word_alignment", p, nil, "word_timing_unavailable"},
		{"unmatched", audioRedactParams{Method: "mute", Selections: []audioSelection{{Text: "absent"}}}, words, "audio_selection_not_found"},
		{"mixed", audioRedactParams{Method: "mute", Selections: []audioSelection{{Text: "hello", StartMs: redactPtr(0)}}}, words, "invalid_audio_selection"},
		{"range", audioRedactParams{Method: "mute", Selections: []audioSelection{{StartMs: redactPtr(4000), EndMs: redactPtr(5001)}}}, nil, "timestamp_out_of_range"},
		{"donor_required", audioRedactParams{Selections: p.Selections}, words, "room_tone_required"},
		{"donor_overlaps", audioRedactParams{Method: "room_tone", Selections: p.Selections, RoomTone: &audioInterval{700, 1000}}, words, "invalid_room_tone"},
		{"invalid_fade", audioRedactParams{Method: "mute", Selections: p.Selections, FadeMs: redactPtr(100)}, words, "invalid_audio_selection"},
		{"invalid_method", audioRedactParams{Method: "bogus", Selections: p.Selections}, words, "invalid_audio_method"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := makeAudioRedactionPlan(tc.p, tc.words, 5000, "hash")
			if err == nil || !strings.Contains(err.Error(), tc.code) {
				t.Fatalf("err=%v want %s", err, tc.code)
			}
		})
	}
	p = audioRedactParams{Method: "bleep", Selections: []audioSelection{{StartMs: redactPtr(500), EndMs: redactPtr(1000)}, {StartMs: redactPtr(1000), EndMs: redactPtr(1300)}}}
	plan, err = makeAudioRedactionPlan(p, nil, 5000, "hash")
	if err != nil || len(plan.Ranges) != 1 || plan.Ranges[0] != (audioInterval{460, 1340}) {
		t.Fatalf("merge=%+v err=%v", plan, err)
	}
	p.Preview = true
	p.Selections = append(p.Selections, audioSelection{StartMs: redactPtr(35000), EndMs: redactPtr(36000)})
	if _, err := makeAudioRedactionPlan(p, nil, 40000, "hash"); err == nil || !strings.Contains(err.Error(), "preview_too_wide") {
		t.Fatalf("preview err=%v", err)
	}
}

func TestAudioRedactionToolQueuesSourceBoundPlan(t *testing.T) {
	ctx := newTestCtx(t)
	if err := upsertMedia(ctx.AppDB(), testProj, "42", sampleAudioProbe(), "source-hash", "", "source.wav"); err != nil {
		t.Fatal(err)
	}
	segs, _ := json.Marshal([]TranscriptSegment{{StartMs: 0, EndMs: 5000, Text: "hide this", Words: []TranscriptWord{{2000, 2400, "hide"}, {2500, 3000, "this"}}}})
	if err := upsertTranscript(ctx.AppDB(), &TranscriptRow{FileID: "42", ProjectID: testProj, SourceSHA256: "source-hash", Text: "hide this", Segments: segs}); err != nil {
		t.Fatal(err)
	}
	app := &App{}
	args := map[string]any{"_project_id": testProj, "file_id": "42", "selections": []any{map[string]any{"text": "hide this"}}, "method": "mute", "output_name": "redacted.wav"}
	result, err := app.audioRedactTool().Handler(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	receipt := result.(map[string]any)
	row, err := getRender(ctx.AppDB(), testProj, receipt["render_id"].(int64))
	if err != nil {
		t.Fatal(err)
	}
	var p audioRedactParams
	if err := json.Unmarshal(row.ResolvedParams, &p); err != nil {
		t.Fatal(err)
	}
	if row.Status != "pending" || row.Operation != "redact_audio" || p.Plan == nil || p.Plan.Ranges[0] != (audioInterval{1960, 3040}) {
		t.Fatalf("row=%+v params=%s", row, row.ResolvedParams)
	}
	if err := verifyRedactionSource(row.ResolvedParams, "changed"); err == nil {
		t.Fatal("changed source accepted")
	}
	if err := verifyRedactionSource(row.ResolvedParams, "source-hash"); err != nil {
		t.Fatal(err)
	}
	if _, err := ctx.AppDB().Exec(`UPDATE transcripts SET source_sha256='old' WHERE file_id='42'`); err != nil {
		t.Fatal(err)
	}
	if _, err := app.audioRedactTool().Handler(ctx, args); err == nil || !strings.Contains(err.Error(), "transcript_source_mismatch") {
		t.Fatalf("stale transcript err=%v", err)
	}
	if _, err := ctx.AppDB().Exec(`UPDATE transcripts SET source_sha256='source-hash',segments='[]' WHERE file_id='42'`); err != nil {
		t.Fatal(err)
	}
	if _, err := app.audioRedactTool().Handler(ctx, args); err == nil || !strings.Contains(err.Error(), "word_timing_unavailable") {
		t.Fatalf("untimed transcript err=%v", err)
	}
	var count int
	if err := ctx.AppDB().QueryRow(`SELECT count(*) FROM renders`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("invalid selections queued: count=%d err=%v", count, err)
	}
}

func TestDeepgramWordTimingPersistsForRedaction(t *testing.T) {
	parsed, err := parseDeepgramResponse(json.RawMessage(`{"results":{"channels":[{"alternatives":[{"transcript":"hello there","words":[{"word":"hello","start":0.25,"end":0.5},{"word":"there","start":0.6,"end":1.0}],"paragraphs":{"paragraphs":[{"sentences":[{"text":"hello there","start":0.25,"end":1.0}]}]}}]}]}}`))
	if err != nil || len(parsed.Segments) != 1 || len(parsed.Segments[0].Words) != 2 {
		t.Fatalf("parsed=%+v err=%v", parsed, err)
	}
	if parsed.Segments[0].Words[0] != (TranscriptWord{250, 500, "hello"}) {
		t.Fatalf("words=%+v", parsed.Segments[0].Words)
	}
	if err := validateTranscriptWords([]TranscriptWord{{20, 10, "bad"}}, 5000); err == nil {
		t.Fatal("invalid alignment accepted")
	}
}

func decodedRedactionAudio(t *testing.T, file string) []float64 {
	t.Helper()
	data, err := exec.Command("ffmpeg", "-v", "error", "-i", file, "-map", "0:a:0", "-f", "f32le", "-acodec", "pcm_f32le", "-").Output()
	if err != nil {
		t.Fatal(err)
	}
	values := make([]float64, len(data)/4)
	for i := range values {
		values[i] = float64(math.Float32frombits(binary.LittleEndian.Uint32(data[4*i:])))
	}
	return values
}
func audioRMS(v []float64, start, end float64, rate, channels int) float64 {
	a, b := int(start*float64(rate))*channels, min(len(v), int(end*float64(rate))*channels)
	sum := 0.0
	for _, n := range v[a:b] {
		sum += n * n
	}
	return math.Sqrt(sum / float64(b-a))
}

func TestAudioRedactionFFmpegEffectsAndUnchangedOutsideSelection(t *testing.T) {
	skipIfNoFFmpeg(t)
	source := filepath.Join(t.TempDir(), "source.wav")
	// Two channels, actual non-silent background at the donor interval, speech-like
	// tone elsewhere. This detects accidental channel loss and speech leakage.
	expr := "aevalsrc=exprs='if(lt(t,0.5),0.004*sin(2*PI*80*t),0.2*sin(2*PI*440*t))|if(lt(t,0.5),0.003*sin(2*PI*90*t),0.15*sin(2*PI*660*t))':s=44100:d=5"
	if log, err := exec.Command("ffmpeg", "-y", "-v", "error", "-f", "lavfi", "-i", expr, "-c:a", "pcm_s16le", source).CombinedOutput(); err != nil {
		t.Fatalf("fixture %v %s", err, log)
	}
	original := decodedRedactionAudio(t, source)
	for _, method := range []string{"mute", "bleep", "room_tone", "muffle"} {
		for _, remote := range []bool{false, true} {
			t.Run(method+map[bool]string{false: "/local", true: "/remote-script"}[remote], func(t *testing.T) {
				params := redactionParamsForTest(t, method, false)
				output, result, err := runGuardedFixture(t, source, "redact_audio", params, remote, "redacted.wav")
				if err != nil {
					t.Fatalf("%v diagnostics=%v", err, result)
				}
				d := result["audio_redaction"].(map[string]any)
				if d["validated"] != true || d["timeline_validated"] != true {
					t.Fatalf("unvalidated %v", d)
				}
				samples := decodedRedactionAudio(t, output)
				if len(samples) != len(original) {
					t.Fatalf("sample count changed %d -> %d", len(original), len(samples))
				}
				for _, r := range []audioInterval{{600, 1900}, {3200, 4900}} {
					for i := int(r.StartMs) * 44100 * 2 / 1000; i < int(r.EndMs)*44100*2/1000; i++ {
						if math.Abs(samples[i]-original[i]) > 1.0/32768 {
							t.Fatalf("outside selection changed at sample %d", i)
						}
					}
				}
				rms := audioRMS(samples, 2.2, 2.8, 44100, 2)
				switch method {
				case "mute":
					if rms != 0 {
						t.Fatalf("speech leaked: rms %g", rms)
					}
				case "room_tone":
					if rms < .001 || rms > .004 {
						t.Fatalf("donor rms %g", rms)
					}
				case "bleep":
					if rms < .06 || rms > .10 {
						t.Fatalf("bleep rms %g", rms)
					}
				case "muffle":
					if rms >= .02 || rms == 0 {
						t.Fatalf("muffle rms %g", rms)
					}
				}
			})
		}
	}
}

func TestAudioRedactionVideoFramesAndPreview(t *testing.T) {
	source := guardedFixture(t, false)
	for _, remote := range []bool{false, true} {
		params := redactionParamsForTest(t, "mute", false)
		output, result, err := runGuardedFixture(t, source, "redact_audio", params, remote, "redacted.mov")
		if err != nil {
			t.Fatalf("remote=%v %v %v", remote, err, result)
		}
		d := result["audio_redaction"].(map[string]any)
		if d["video_pictures_match"] != true || d["video_frames_checked"] != float64(150) {
			t.Fatalf("last frame lost: %v output=%s", d, output)
		}
		previewParams := redactionParamsForTest(t, "bleep", true)
		output, result, err = runGuardedFixture(t, source, "redact_audio", previewParams, remote, "preview.wav")
		if err != nil {
			t.Fatalf("preview %v %v", err, result)
		}
		d = result["audio_redaction"].(map[string]any)
		if d["source_start_ms"] != float64(460) || d["source_end_ms"] != float64(4540) || d["video_copied"] != false {
			t.Fatalf("preview mapping=%v", d)
		}
		samples := decodedRedactionAudio(t, output)
		if math.Abs(float64(len(samples))/44100-4.08) > 1.0/44100 {
			t.Fatalf("preview samples=%d", len(samples))
		}
	}
}

func TestAudioRedactionPreviewsAndOutputFormats(t *testing.T) {
	ctx := newTestCtx(t)
	if err := upsertMedia(ctx.AppDB(), testProj, "42", sampleAudioProbe(), "source-hash", "", "source.wav"); err != nil {
		t.Fatal(err)
	}
	params := json.RawMessage(`{"method":"mute","preview":true,"selections":[{"start_ms":2000,"end_ms":3000}]}`)
	for _, name := range []string{"", "preview", "preview.wav"} {
		plan, err := prepareRenderSubmission(ctx, testProj, "redact_audio", []string{"42"}, params, name)
		if err != nil {
			t.Fatalf("name=%q err=%v", name, err)
		}
		if !isAudioExt(filepath.Ext(plan.Filename)) || len(plan.SubmissionParams) == 0 {
			t.Fatalf("plan=%+v", plan)
		}
	}
	if _, err := prepareRenderSubmission(ctx, testProj, "redact_audio", []string{"42"}, params, "preview.mov"); err == nil {
		t.Fatal("video preview accepted")
	}
	params = redactionRaw(t, redactionParamsForTest(t, "mute", false))
	if _, err := buildPlan("redact_audio", []string{"42"}, params, "out.png", ".wav"); err == nil {
		t.Fatal("image redaction accepted")
	}
	got := compactRenderParams(params)
	summary, ok := got["audio_redaction_plan"].(map[string]any)
	if !ok || summary["range_count"] != 1 {
		t.Fatalf("missing summary %v", got)
	}
	if renderWireBytes(got) > 4096 {
		t.Fatal("oversized summary")
	}
}
func redactionRaw(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestAudioRedactionHEVCAndAACPreservePicturesAndTiming(t *testing.T) {
	source := guardedFixture(t, true)
	p := redactionParamsForTest(t, "bleep", false)
	p["_source_sample_rate"] = 48000
	_, result, err := runGuardedFixture(t, source, "redact_audio", p, true, "redacted.mov")
	if err != nil {
		t.Fatalf("%v %v", err, result)
	}
	d := result["audio_redaction"].(map[string]any)
	if d["video_frames_checked"] != float64(150) || d["video_pictures_match"] != true || d["timeline_validated"] != true {
		t.Fatalf("HEVC validation=%v", d)
	}
	// A real AAC preview also exercises donor materialization from a separate
	// input, outside the preview seek. Both paths must map the selection back.
	p = redactionParamsForTest(t, "room_tone", true)
	p["_source_sample_rate"] = 48000
	_, result, err = runGuardedFixture(t, source, "redact_audio", p, false, "preview.m4a")
	if err != nil {
		t.Fatalf("AAC room-tone preview %v %v", err, result)
	}
	d = result["audio_redaction"].(map[string]any)
	if d["sample_rate"] != float64(48000) || d["validated"] != true || d["video_copied"] != false {
		t.Fatalf("AAC preview=%v", d)
	}
}

func TestAudioRedactionCacheBindsResolvedWordTiming(t *testing.T) {
	app := newTestCtx(t)
	hash := strings.Repeat("a", 64)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"file": StorageFile{ID: 1, Name: "source.wav", SHA256: hash, SizeBytes: 100}})
	}))
	defer server.Close()
	sc := &storageClient{base: server.URL, httpClient: server.Client()}
	executor := &localExecutor{ffmpegPath: "/bin/sh"}
	p := redactionParamsForTest(t, "mute", false)
	plan := p["_redaction_plan"].(*audioRedactionPlan)
	plan.SourceSHA256 = hash
	row := &RenderRow{ProjectID: testProj, Operation: "redact_audio", SourceFileIDs: []string{"1"}, Params: json.RawMessage(`{"method":"mute","selections":[{"text":"same phrase"}]}`), ResolvedParams: redactionRaw(t, p), OutputName: "redacted.wav"}
	first, _, _ := requestRenderCacheKey(context.Background(), app, sc, row, executor)
	if first == "" {
		t.Fatal("no cache identity for prepared redaction")
	}
	plan.Ranges[0].StartMs += 100
	row.ResolvedParams = redactionRaw(t, p)
	second, _, _ := requestRenderCacheKey(context.Background(), app, sc, row, executor)
	if second == "" || first == second {
		t.Fatal("corrected word timing reused old redaction")
	}
	plan.SourceSHA256 = "changed"
	row.ResolvedParams = redactionRaw(t, p)
	if key, _, _ := requestRenderCacheKey(context.Background(), app, sc, row, executor); key != "" {
		t.Fatal("source-mismatched plan hit cache")
	}
}

func TestAudioRedactionRejectsActualStreamMismatch(t *testing.T) {
	source := guardedFixture(t, false)
	params := redactionParamsForTest(t, "mute", false)
	params["_source_sample_rate"] = 48000
	_, result, err := runGuardedFixture(t, source, "redact_audio", params, true, "out.mov")
	if err == nil || !strings.Contains(result["runtime_error"].(string), "audio_redaction_failed") {
		t.Fatalf("mismatched source accepted: %v %v", err, result)
	}
	if d, ok := result["audio_redaction"].(map[string]any); ok && d["validated"] == true {
		t.Fatal("failed output marked validated")
	}
}

func TestAudioRedactionDelayedAudioUsesSourceTimeline(t *testing.T) {
	skipIfNoFFmpeg(t)
	source := filepath.Join(t.TempDir(), "delayed.mov")
	args := []string{"-y", "-v", "error", "-f", "lavfi", "-i", "testsrc2=s=96x64:r=30:d=5", "-itsoffset", "0.5", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=44100:duration=4.5", "-c:v", "libx264", "-c:a", "pcm_s16le", source}
	if log, err := exec.Command("ffmpeg", args...).CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v %s", err, log)
	}
	// The source really starts audio at .5s, while the transcription proxy
	// contains only 4.5s of audio. Word boundaries therefore need a .5s offset.
	proxy := filepath.Join(t.TempDir(), "proxy.wav")
	if log, err := exec.Command("ffmpeg", "-v", "error", "-i", source, "-vn", "-map", "0:a:0", proxy).CombinedOutput(); err != nil {
		t.Fatalf("proxy: %v %s", err, log)
	}
	if samples := decodedRedactionAudio(t, proxy); len(samples) != 198450 {
		t.Fatalf("unexpected proxy sample origin: %d", len(samples))
	}
	ctx := newTestCtx(t)
	probe, err := runProbe(context.Background(), "ffprobe", source)
	if err != nil {
		t.Fatal(err)
	}
	if err := upsertMedia(ctx.AppDB(), testProj, "42", probe, "source-hash", "", "delayed.mov"); err != nil {
		t.Fatal(err)
	}
	segs := redactionRaw(t, []TranscriptSegment{{StartMs: 0, EndMs: 4500, Text: "hide", Words: []TranscriptWord{{1500, 2500, "hide"}}}})
	if err := upsertTranscript(ctx.AppDB(), &TranscriptRow{FileID: "42", ProjectID: testProj, SourceSHA256: "source-hash", Provider: "deepgram", Text: "hide", Segments: segs}); err != nil {
		t.Fatal(err)
	}
	prepared, err := prepareAudioRedactionSubmission(ctx, testProj, "redact_audio", []string{"42"}, json.RawMessage(`{"method":"mute","selections":[{"text":"hide"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	var p audioRedactParams
	_ = json.Unmarshal(prepared, &p)
	if p.Plan.AudioStartMs != 500 || p.Plan.Ranges[0] != (audioInterval{1960, 3040}) {
		t.Fatalf("wrong word origin: %+v", p.Plan)
	}
	var params map[string]any
	_ = json.Unmarshal(prepared, &params)
	output, result, err := runGuardedFixture(t, source, "redact_audio", params, false, "redacted.mov")
	if err != nil {
		t.Fatalf("delayed audio %v %v", err, result)
	}
	samples := decodedRedactionAudio(t, output)
	if audioRMS(samples, .05, .4, 44100, 1) > 1e-5 || audioRMS(samples, .8, 1.2, 44100, 1) < .05 || audioRMS(samples, 2.2, 2.8, 44100, 1) > 1e-4 {
		t.Fatal("delayed speech or masked interval moved on the output timeline")
	}
	p.Plan.Preview = true
	p.Plan.StartMs = 460
	p.Plan.EndMs = 4540
	params["preview"] = true
	params["_redaction_plan"] = p.Plan
	_, result, err = runGuardedFixture(t, source, "redact_audio", params, true, "preview.wav")
	if err != nil {
		t.Fatalf("preview leading silence %v %v", err, result)
	}
}

func TestAudioRedactionMP3PrimingUsesDecodedAudioOrigin(t *testing.T) {
	skipIfNoFFmpeg(t)
	source := filepath.Join(t.TempDir(), "source.mp3")
	if log, err := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=44100:duration=5", "-c:a", "libmp3lame", source).CombinedOutput(); err != nil {
		t.Fatalf("fixture %v %s", err, log)
	}
	ctx := newTestCtx(t)
	probe, err := runProbe(context.Background(), "ffprobe", source)
	if err != nil {
		t.Fatal(err)
	}
	if err := upsertMedia(ctx.AppDB(), testProj, "42", probe, "source-hash", "", "source.mp3"); err != nil {
		t.Fatal(err)
	}
	prepared, err := prepareAudioRedactionSubmission(ctx, testProj, "redact_audio", []string{"42"}, json.RawMessage(`{"method":"mute","selections":[{"start_ms":2000,"end_ms":3000}]}`))
	if err != nil {
		t.Fatal(err)
	}
	var params map[string]any
	_ = json.Unmarshal(prepared, &params)
	output, result, err := runGuardedFixture(t, source, "redact_audio", params, false, "redacted.wav")
	if err != nil {
		t.Fatalf("MP3 priming %v %v", err, result)
	}
	samples := decodedRedactionAudio(t, output)
	if len(samples) != 220500 || audioRMS(samples, 2.2, 2.8, 44100, 1) != 0 {
		t.Fatalf("MP3 priming or masked samples changed: %d", len(samples))
	}
}
