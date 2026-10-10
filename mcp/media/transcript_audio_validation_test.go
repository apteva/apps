package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const goodTranscriptStats = `[Parsed_astats_1 @ fake] Peak level dB: -1.0
[Parsed_astats_1 @ fake] RMS level dB: -19.0
[Parsed_astats_1 @ fake] Peak count: 2
[Parsed_astats_1 @ fake] Number of samples: 9600000
[Parsed_astats_1 @ fake] Number of NaNs: 0
[Parsed_astats_1 @ fake] Number of Infs: 0
`

func TestTranscriptAudioValidationRejectsSuccessfulCorruptSignals(t *testing.T) {
	for _, tc := range []struct {
		name, log string
		valid     bool
	}{
		{"stable", goodTranscriptStats, true},
		{"silence", strings.ReplaceAll(strings.ReplaceAll(goodTranscriptStats, "-1.0", "-inf"), "-19.0", "-inf"), true},
		{"runaway", strings.ReplaceAll(goodTranscriptStats, "-1.0", "124.8"), false},
		{"clipped_pcm", strings.ReplaceAll(strings.ReplaceAll(goodTranscriptStats, "-1.0", "-0.000265"), "Peak count: 2", "Peak count: 3200000"), false},
		{"nan_samples", strings.ReplaceAll(goodTranscriptStats, "Number of NaNs: 0", "Number of NaNs: 3"), false},
		{"infinite_samples", strings.ReplaceAll(goodTranscriptStats, "Number of Infs: 0", "Number of Infs: 3"), false},
		{"nan_level", strings.ReplaceAll(goodTranscriptStats, "-1.0", "nan"), false},
		{"missing_evidence", "ffmpeg exit zero without statistics", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v, err := parseTranscriptAudioValidation(tc.log)
			if err == nil {
				err = v.check(600000)
			}
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, error=%v", tc.valid, err)
			}
			// Execute exactly the remote pre-upload gate with the same signal.
			ff := filepath.Join(t.TempDir(), "ffmpeg")
			writeExecutable(t, ff, "cat >&2 <<'STATS'\n"+tc.log+"STATS\nexit 0\n")
			cmd := exec.Command("bash", "-c", remoteTranscriptAudioValidator()+"\nvalidate_audio source.mp3\n")
			cmd.Dir = t.TempDir()
			cmd.Env = append(os.Environ(), "FFMPEG="+ff, "AUDIO_LOG_TAIL_BYTES=16384", "EXPECTED_DURATION_MS=600000")
			out, remoteErr := cmd.CombinedOutput()
			if (remoteErr == nil) != tc.valid {
				t.Fatalf("remote valid=%v err=%v: %s", tc.valid, remoteErr, out)
			}
		})
	}
	v, err := parseTranscriptAudioValidation(goodTranscriptStats)
	if err != nil {
		t.Fatal(err)
	}
	if v.check(603000) == nil {
		t.Fatal("short audio accepted")
	}
}

func TestTranscriptAudioBadRecipeAndUnvalidatedCacheAreNotReused(t *testing.T) {
	ctx := newTestCtx(t)
	old := "transcript_audio:abc:v1_loudnorm_16k_mono_mp3"
	if err := upsertDerivation(ctx.AppDB(), testProj, "1", old, 42, 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	if _, ok := findTranscriptAudioDerivation(ctx.AppDB(), testProj, "1", transcriptAudioKind("abc")); ok {
		t.Fatal("poisoned old recipe was reused")
	}
	if transcriptAudioKind("abc") == old {
		t.Fatal("cache recipe was not changed")
	}
}

func TestTranscriptAudioInvalidPrimaryAndFallbackNeverUpload(t *testing.T) {
	bad := strings.ReplaceAll(goodTranscriptStats, "-1.0", "120.0")
	ff := filepath.Join(t.TempDir(), "ffmpeg")
	writeExecutable(t, ff, `case " $* " in
 *" -af aformat=sample_fmts=dbl,astats="*) cat >&2 <<'STATS'
`+bad+`STATS
 exit 0;;
esac
for arg in "$@"; do output="$arg"; done
printf invalid_audio > "$output"
`)
	_, _, err := runValidatedTranscriptAudioFFmpeg(context.Background(), ff, "source.mp4", filepath.Join(t.TempDir(), "audio.mp3"), 600000)
	if err == nil || !strings.Contains(err.Error(), "runaway") {
		t.Fatalf("invalid local audio accepted: %v", err)
	}
	bin := t.TempDir()
	writeExecutable(t, filepath.Join(bin, "curl"), "echo upload-must-not-run >&2; exit 99")
	script := buildRemoteTranscriptAudioScript(remoteTranscriptAudioScriptInputs{FFmpeg: ff, SignedURL: "source.mp4", FileID: "test", ExpectedDurationMs: 600000, PublicURL: "https://example", StorageToken: "fake", ProjectID: testProj})
	cmd := exec.Command("bash", "-c", script)
	cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	if err == nil || strings.Contains(string(out), "upload-must-not-run") || strings.Contains(string(out), "APTEVA_TRANSCRIPT_AUDIO:") {
		t.Fatalf("invalid remote audio uploaded: %v %s", err, out)
	}
	if !strings.Contains(string(out), "transcript_audio_invalid") {
		t.Fatalf("primary cause lost: %s", out)
	}
}

// Export the exact generated production script for pre-release host QA. Runtime
// URLs/tokens are substituted only in memory by the private QA runner.
func TestExportTranscriptAudioHostQA(t *testing.T) {
	path := os.Getenv("MEDIA_TRANSCRIPT_AUDIO_EXPORT_SCRIPT")
	if path == "" {
		t.Skip("host QA export not requested")
	}
	script := buildRemoteTranscriptAudioScript(remoteTranscriptAudioScriptInputs{FFmpeg: "QA_FFMPEG", SignedURL: "QA_SOURCE", FileID: "QA", ExpectedDurationMs: 599626, PublicURL: "https://unused.example", StorageToken: "unused", ProjectID: testProj})
	if err := os.WriteFile(path, []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
}

// Pin the complete failing production fixture outside Git. Also validate the
// encoder-independent fallback with the exact same corrected filter.
func TestTranscriptAudioFullLengthProductionSignal(t *testing.T) {
	source := os.Getenv("MEDIA_TRANSCRIPT_AUDIO_LONG_FIXTURE")
	if source == "" {
		t.Skip("MEDIA_TRANSCRIPT_AUDIO_LONG_FIXTURE not set")
	}
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	path := filepath.Join(t.TempDir(), "full.mp3")
	_, v, err := runValidatedTranscriptAudioFFmpeg(ctx, ff, source, path, 599573)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("validated primary: %s", transcriptAudioEvidenceJSON(&transcriptAudioEvidence{Validation: v}))
	wav := filepath.Join(t.TempDir(), "full.wav")
	if _, err = executeTranscriptAudioFFmpeg(ctx, ff, transcriptAudioPCMFFmpegArgs(source, wav), wav); err != nil {
		t.Fatal(err)
	}
	v, err = validateTranscriptAudio(ctx, ff, wav, 599573)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("validated PCM: %s", transcriptAudioEvidenceJSON(&transcriptAudioEvidence{Validation: v}))
	if bad := os.Getenv("MEDIA_TRANSCRIPT_AUDIO_BAD_FIXTURE"); bad != "" {
		if _, err = validateTranscriptAudio(ctx, ff, bad, 599573); err == nil || !strings.Contains(err.Error(), "runaway") {
			t.Fatalf("confirmed corrupt proxy accepted: %v", err)
		}
	}
}

func TestDeepgramDiagnosticsPersistActualMetadataWithoutClaimingCoverage(t *testing.T) {
	raw := json.RawMessage(`{"metadata":{"duration":599.652,"request_id":"request-1","sha256":"proxy-sha","model_info":{"m":{"name":"general-nova-3","version":"2025-07-31.0"}}},"results":{"channels":[{"alternatives":[{"transcript":"late speech","words":[{"end":5},{"end":575.95}]}]}]}}`)
	p, err := parseDeepgramResponse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if p.ProviderDurationMs != 599652 || p.RequestID != "request-1" || p.LastWordEndMs != 575950 || p.WordCount != 2 || !strings.Contains(string(p.Metadata), "2025-07-31.0") {
		t.Fatalf("metadata lost: %+v", p)
	}
	ctx := newTestCtx(t)
	if err = insertPendingTranscript(ctx.AppDB(), testProj, "1", "manual"); err != nil {
		t.Fatal(err)
	}
	row, err := claimNextPendingTranscript(ctx.AppDB(), testProj)
	if err != nil {
		t.Fatal(err)
	}
	row.Text = p.Text
	row.Diagnostics = json.RawMessage(`{"speech_coverage":"unverified","provider_duration_ms":599652,"request_id":"request-1"}`)
	if err = transcriptMarkOk(ctx.AppDB(), row); err != nil {
		t.Fatal(err)
	}
	got, err := getTranscript(ctx.AppDB(), testProj, "1")
	if err != nil {
		t.Fatal(err)
	}
	if string(got.Diagnostics) != string(row.Diagnostics) {
		t.Fatalf("diagnostics lost: %s", got.Diagnostics)
	}
}

func TestPrepareTranscriptAudioOnlyPreservesImportedTranscript(t *testing.T) {
	stub := boundDeepgram()
	ctx := newTestCtxWithPlatform(t, stub)
	if err := upsertMedia(ctx.AppDB(), testProj, "1", sampleAVProbe(3000), "abc", "", "source.mov"); err != nil {
		t.Fatal(err)
	}
	if err := upsertTranscript(ctx.AppDB(), &TranscriptRow{FileID: "1", ProjectID: testProj, Provider: "whisper", Text: "preserve imported speech", SourceKind: "imported"}); err != nil {
		t.Fatal(err)
	}
	if err := upsertDerivation(ctx.AppDB(), testProj, "1", transcriptAudioKind("abc"), 9001, 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	e := &transcriptAudioEvidence{StorageFileID: 9001, Validation: &transcriptAudioValidation{Method: "full_decode_float_astats", Recipe: transcriptAudioRecipe, DecodeOK: true, Samples: 48000, DurationMs: 3000}}
	if _, err := ctx.AppDB().Exec(`INSERT INTO transcript_audio_checks(project_id,file_id,kind,storage_file_id,evidence) VALUES(?,?,?,?,?)`, testProj, "1", transcriptAudioKind("abc"), 9001, transcriptAudioEvidenceJSON(e)); err != nil {
		t.Fatal(err)
	}
	a := &App{}
	r, err := a.toolTranscribe(ctx, map[string]any{"_project_id": testProj, "file_id": "1", "prepare_only": true})
	if err != nil {
		t.Fatal(err)
	}
	if !r.(map[string]any)["prepared"].(bool) {
		t.Fatalf("did not prepare: %v", r)
	}
	if _, err = a.toolTranscribe(ctx, map[string]any{"_project_id": testProj, "file_id": "1", "prepare_only": true, "force": true}); err == nil {
		t.Fatal("conflicting force accepted")
	}
	tr, err := getTranscript(ctx.AppDB(), testProj, "1")
	if err != nil {
		t.Fatal(err)
	}
	if tr.Provider != "whisper" || tr.Text != "preserve imported speech" || len(stub.ExecuteCalls) != 0 {
		t.Fatalf("prepare-only changed transcript/called provider: %+v", tr)
	}
	// A derivation alone is insufficient; missing checks must force new
	// preparation rather than feeding an unvalidated cached file to Deepgram.
	if _, err = ctx.AppDB().Exec(`DELETE FROM transcript_audio_checks`); err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = ensureTranscriptAudioEvidence(cancelled, ctx, newStorageClient(), &MediaRow{ProjectID: testProj, FileID: "1", HasVideo: true, DurationMs: 3000, SourceSHA256: "abc"}, 1); err == nil {
		t.Fatal("unvalidated cache accepted")
	}
}
