package main

import (
	"context"
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

const parakeetFixture = `APTEVA_PARAKEET:{"runtime":"sherpa-onnx-2.0.2","model":"parakeet-tdt-0.6b-v3-int8","duration_ms":5000,"threads":4,"chunks":1,"load_ms":900,"decode_ms":250,"text":"Bonjour madame. Comment allez-vous?","words":[[100,400,"Bonjour",0.99,false],[400,900,"madame.",0.97,false],[2500,2800,"Comment",0.95,false],[2800,2880,"allez-vous?",0.41,true]]}`

func TestParseParakeetOutput(t *testing.T) {
	res, err := parseParakeetOutput("setup noise\n" + parakeetFixture + "\n")
	if err != nil || len(res.Words) != 4 || res.DurationMs != 5000 {
		t.Fatalf("result: %+v %v", res, err)
	}
	if _, err := parseParakeetOutput(`APTEVA_PARAKEET_ERROR:{"code":"parakeet_model_hash_mismatch"}`); err == nil || err.Error() != "parakeet_model_hash_mismatch" {
		t.Fatalf("error line: %v", err)
	}
	if _, err := parseParakeetOutput("nothing"); err == nil || err.Error() != "parakeet_result_missing" {
		t.Fatalf("missing: %v", err)
	}
	wrong := strings.Replace(parakeetFixture, "sherpa-onnx-2.0.2", "sherpa-onnx-9", 1)
	if _, err := parseParakeetOutput(wrong); err == nil || err.Error() != "parakeet_result_identity_mismatch" {
		t.Fatalf("identity: %v", err)
	}
}

func TestParakeetToParsedSegmentsAndWords(t *testing.T) {
	res, _ := parseParakeetOutput(parakeetFixture)
	p, err := parakeetToParsed(res, "fr", "local")
	if err != nil {
		t.Fatal(err)
	}
	if p.WordCount != 4 || p.Language != "fr" || p.LastWordEndMs != 2880 || p.ProviderDurationMs != 5000 || p.WordSource != "parakeet_tdt" {
		t.Fatalf("parsed: %+v", p)
	}
	// "madame." closes the first sentence; the second starts at "Comment".
	if len(p.Segments) != 2 || p.Segments[0].Text != "Bonjour madame." || p.Segments[1].StartMs != 2500 || len(p.Segments[1].Words) != 2 {
		t.Fatalf("segments: %+v", p.Segments)
	}
	var meta map[string]any
	_ = json.Unmarshal(p.Metadata, &meta)
	if meta["estimated_words"] != float64(1) || meta["host"] != "local" || meta["duration"] != float64(5) {
		t.Fatalf("metadata: %v", meta)
	}
	if auto, _ := parakeetToParsed(res, "auto", "executor"); auto.Language != "" {
		t.Fatalf("auto language should stay unset, got %q", auto.Language)
	}
	bad := strings.Replace(parakeetFixture, `[400,900,"madame."`, `[400,400,"madame."`, 1)
	res, _ = parseParakeetOutput(bad)
	if _, err := parakeetToParsed(res, "fr", "local"); err == nil {
		t.Fatal("zero-length word accepted")
	}
}

func TestParakeetPauseSplitsSegment(t *testing.T) {
	words := []TranscriptWord{{StartMs: 0, EndMs: 300, Text: "one"}, {StartMs: 1500, EndMs: 1800, Text: "two"}, {StartMs: 1900, EndMs: 2100, Text: "three"}}
	if !segmentClosed(words, 1) || segmentClosed(words, 2) {
		t.Fatal("a pause over one second should start a segment; a short gap should not")
	}
}

func TestTranscriptionEngineDefaults(t *testing.T) {
	cases := []struct {
		engine, language, diarize, want string
		args                            map[string]any
	}{
		{"", "auto", "false", "nova-3", nil},
		{"parakeet", "auto", "false", parakeetModel, nil},
		{"parakeet", "fr", "false", parakeetModel, nil},
		{"auto", "fr", "false", parakeetModel, nil},
		{"auto", "en-US", "false", parakeetModel, nil},
		{"auto", "ja", "false", "nova-3", nil},
		{"auto", "auto", "true", "nova-3", nil},
		{"auto", "auto", "false", "nova-2", map[string]any{"model": "nova-2"}},
		{"", "auto", "false", parakeetModel, map[string]any{"model": parakeetModel}},
		{"auto", "auto", "false", "nova-3", map[string]any{"language": "ja"}},
	}
	for _, c := range cases {
		ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj), tk.WithConfig(map[string]string{"transcribe_engine": c.engine, "transcribe_language": c.language, "transcribe_diarize": c.diarize}))
		args := c.args
		if args == nil {
			args = map[string]any{}
		}
		o, err := resolveTranscriptionOptions(ctx, args)
		if err != nil || o.Model != c.want {
			t.Fatalf("%+v: model %q err %v", c, o.Model, err)
		}
		if c.args == nil {
			if d := transcriptionDefaults(ctx); d.Model != c.want {
				t.Fatalf("%+v: defaults model %q", c, d.Model)
			}
		}
	}
}

func TestTranscriptionConfiguredWithoutIntegration(t *testing.T) {
	ctx := newTestCtxWithPlatform(t, noBindings())
	if transcriptionConfigured(ctx) {
		t.Fatal("deepgram engine without integration must not be configured")
	}
	ctx = newTestCtxWithPlatform(t, noBindings(), tk.WithConfig(map[string]string{"transcribe_engine": "parakeet"}))
	if !transcriptionConfigured(ctx) {
		t.Fatal("parakeet engine needs no integration")
	}
}

func TestParakeetRemoteScriptShape(t *testing.T) {
	script := buildParakeetRemoteScript("/opt/ffmpeg", "https://storage/a.mp3?sig=x", parakeetRequest{Audio: "audio.wav", Threads: 4, ChunkS: 30})
	for _, want := range []string{parakeetRemoteRoot, "/var/tmp/apteva-media-asr-work/job-XXXXXX", "python3 \"$WORK/setup.py\" \"$ROOT\"", "'/opt/ffmpeg' -nostdin", "'https://storage/a.mp3?sig=x'", "\"$ROOT/model\"", "trap "} {
		if !strings.Contains(script, want) {
			t.Fatalf("script missing %q:\n%s", want, script)
		}
	}
	if weight, control := classifyComponentPlan(script); weight != 2 || control {
		t.Fatalf("parakeet plans weigh two units, got %d control=%v", weight, control)
	}
}

func TestParakeetModelHashesMatchSetup(t *testing.T) {
	for _, digest := range []string{"1afbe69c1ffdabee94755f42e4daf1d93549131a3da11356bda84fcef0f2c479", "0222a86147299d4dee90edf95b6d13827edb42ad8b7c9cf0b2582f030937c0b5", "cbd02f3696e9944970111fb439d9c3fd365a80374565f13f2b9c7007d253fc98", "d58544679ea4bc6ac563d1f545eb7d474bd6cfa467f0a6e2c1dc1c7d37e3c35d"} {
		if !strings.Contains(parakeetSetup, digest) {
			t.Fatalf("setup lost pinned digest %s", digest)
		}
	}
	if !strings.Contains(parakeetRuntime, `RUNTIME = "`+parakeetRuntimeID+`"`) || !strings.Contains(parakeetRuntime, `MODEL = "`+parakeetModelID+`"`) {
		t.Fatal("runtime identity drifted from Go constants")
	}
}

// stageParakeetRoot builds a runtime root that the real setup.py accepts
// without network: a fake venv python and placeholder model files whose
// verified.json marker matches their size and mtime.
func stageParakeetRoot(t *testing.T, output string) string {
	t.Helper()
	root := t.TempDir()
	bin := filepath.Join(root, "venv", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(root, "output.txt")
	if err := os.WriteFile(out, []byte(output+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	python := "#!/bin/sh\n[ \"$1\" = \"-c\" ] && exit 0\ncat " + out + "\n"
	if err := os.WriteFile(filepath.Join(bin, "python"), []byte(python), 0o755); err != nil {
		t.Fatal(err)
	}
	model := filepath.Join(root, "model")
	_ = os.MkdirAll(model, 0o755)
	marker := map[string][]any{}
	for name, digest := range map[string]string{"encoder.int8.onnx": "1afbe69c1ffdabee94755f42e4daf1d93549131a3da11356bda84fcef0f2c479", "decoder.int8.onnx": "0222a86147299d4dee90edf95b6d13827edb42ad8b7c9cf0b2582f030937c0b5", "joiner.int8.onnx": "cbd02f3696e9944970111fb439d9c3fd365a80374565f13f2b9c7007d253fc98", "tokens.txt": "d58544679ea4bc6ac563d1f545eb7d474bd6cfa467f0a6e2c1dc1c7d37e3c35d"} {
		p := filepath.Join(model, name)
		if err := os.WriteFile(p, []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
		st, _ := os.Stat(p)
		marker[name] = []any{digest, st.Size(), st.ModTime().Unix()}
	}
	raw, _ := json.Marshal(marker)
	if err := os.WriteFile(filepath.Join(model, "verified.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func fakeFFmpeg(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ffmpeg")
	// Write a placeholder at the last argument (the output path).
	script := "#!/bin/sh\nfor last; do :; done\n: > \"$last\"\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestParakeetWorkerTranscribesWithoutIntegration(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
	root := stageParakeetRoot(t, parakeetFixture)
	ctx := newTestCtxWithPlatform(t, noBindings(), tk.WithConfig(map[string]string{"transcribe_engine": "parakeet", "transcribe_parakeet_root": root, "ffmpeg_path": fakeFFmpeg(t), "transcribe_language": "fr"}))
	if err := upsertMedia(ctx.AppDB(), testProj, "1", sampleAudioProbe(), "sha", "", "source.wav"); err != nil {
		t.Fatal(err)
	}
	seedTranscriptAudio(t, ctx, "1", "sha", 9001, 5000)
	if err := insertPendingTranscript(ctx.AppDB(), testProj, "1", "auto", transcriptionDefaults(ctx)); err != nil {
		t.Fatal(err)
	}
	row, err := claimNextPendingTranscript(ctx.AppDB(), testProj)
	if err != nil {
		t.Fatal(err)
	}
	runOneTranscription(ctx, nil, row)
	got, err := getTranscript(ctx.AppDB(), testProj, "1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "ok" || got.Provider != "parakeet" || got.Model != parakeetModel || got.Language != "fr" || !strings.HasPrefix(got.Text, "Bonjour madame.") {
		t.Fatalf("transcript: status=%s provider=%s model=%s lang=%s text=%q diag=%s", got.Status, got.Provider, got.Model, got.Language, got.Text, got.Diagnostics)
	}
	summary := readTranscriptMap(t, ctx, map[string]any{"file_id": "1", "view": "summary"})
	if summary["word_count"] != 4 || summary["redaction_ready"] != true {
		t.Fatalf("summary: %+v", summary)
	}
	if timing := summary["timing"].(*transcriptTiming); timing.AudioFileID != 9001 || timing.WordSource != "parakeet_tdt" {
		t.Fatalf("timing: %+v", timing)
	}
}

func TestParakeetRejectsDiarizationAndUnsupportedLanguage(t *testing.T) {
	root := stageParakeetRoot(t, parakeetFixture)
	for _, opts := range []map[string]string{{"transcribe_diarize": "true"}, {"transcribe_language": "ja"}} {
		cfg := map[string]string{"transcribe_parakeet_root": root, "ffmpeg_path": fakeFFmpeg(t)}
		for k, v := range opts {
			cfg[k] = v
		}
		ctx := newTestCtxWithPlatform(t, noBindings(), tk.WithConfig(cfg))
		if err := upsertMedia(ctx.AppDB(), testProj, "1", sampleAudioProbe(), "sha", "", "source.wav"); err != nil {
			t.Fatal(err)
		}
		seedTranscriptAudio(t, ctx, "1", "sha", 9001, 5000)
		o := transcriptionDefaults(ctx)
		o.Model = parakeetModel
		if err := insertPendingTranscript(ctx.AppDB(), testProj, "1", "manual", o); err != nil {
			t.Fatal(err)
		}
		row, err := claimNextPendingTranscript(ctx.AppDB(), testProj)
		if err != nil {
			t.Fatal(err)
		}
		runOneTranscription(ctx, nil, row)
		got, _ := getTranscript(ctx.AppDB(), testProj, "1")
		msg := fmt.Sprint(got.Error)
		if got.Status != "failed" || !strings.Contains(msg, "parakeet_") {
			t.Fatalf("%v: status=%s error=%s", opts, got.Status, msg)
		}
	}
}

// Live: provisions the real runtime and model (≈490 MB download on first run)
// and transcribes MEDIA_PARAKEET_LIVE_AUDIO, a local audio file or URL.
func TestParakeetLiveLocal(t *testing.T) {
	audio := os.Getenv("MEDIA_PARAKEET_LIVE_AUDIO")
	if audio == "" {
		t.Skip("MEDIA_PARAKEET_LIVE_AUDIO not set")
	}
	cfg := map[string]string{}
	if root := os.Getenv("MEDIA_PARAKEET_LIVE_ROOT"); root != "" {
		cfg["transcribe_parakeet_root"] = root
	}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj), tk.WithConfig(cfg))
	start := time.Now()
	res, err := runParakeetLocal(context.Background(), ctx, audio, parakeetRequest{Audio: "audio.wav", Threads: 4, ChunkS: 30})
	if err != nil {
		t.Fatal(err)
	}
	p, err := parakeetToParsed(res, "auto", "local")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("audio %.1fs, wall %.1fs (decode %.1fs, load %.1fs), %d words, %d segments, metadata %s", float64(res.DurationMs)/1000, time.Since(start).Seconds(), float64(res.DecodeMs)/1000, float64(res.LoadMs)/1000, p.WordCount, len(p.Segments), p.Metadata)
	t.Logf("text: %.300s", p.Text)
	if p.WordCount == 0 {
		t.Fatal("no words")
	}
}

func TestExplicitParakeetRunsWithoutEngineOrIntegration(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
	root := stageParakeetRoot(t, parakeetFixture)
	ctx := newTestCtxWithPlatform(t, noBindings(), tk.WithConfig(map[string]string{"transcribe_parakeet_root": root, "ffmpeg_path": fakeFFmpeg(t)}))
	for _, fid := range []string{"1", "2"} {
		if err := upsertMedia(ctx.AppDB(), testProj, fid, sampleAudioProbe(), "sha"+fid, "", "source.wav"); err != nil {
			t.Fatal(err)
		}
		seedTranscriptAudio(t, ctx, fid, "sha"+fid, 9000+int64(len(fid)), 5000)
	}
	deepgram := transcriptionDefaults(ctx)
	local := deepgram
	local.Model = parakeetModel
	if err := insertPendingTranscript(ctx.AppDB(), testProj, "1", "manual", deepgram); err != nil {
		t.Fatal(err)
	}
	if err := insertPendingTranscript(ctx.AppDB(), testProj, "2", "manual", local); err != nil {
		t.Fatal(err)
	}
	transcriberSweepOne(ctx)
	first, _ := getTranscript(ctx.AppDB(), testProj, "1")
	second, _ := getTranscript(ctx.AppDB(), testProj, "2")
	if first.Status != "pending" {
		t.Fatalf("deepgram row must keep waiting for an integration, got %s", first.Status)
	}
	if second.Status != "ok" || second.Provider != "parakeet" {
		t.Fatalf("explicit parakeet row: status=%s provider=%s error=%s", second.Status, second.Provider, second.Error)
	}
}
