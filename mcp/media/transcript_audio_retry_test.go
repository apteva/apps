package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTranscriptAudioFFmpegArgsKeepCBRPrimaryAndVBRFallback(t *testing.T) {
	primary := transcriptAudioFFmpegArgs("source.mp4", "audio.mp3")
	fallback := transcriptAudioVBRFFmpegArgs("source.mp4", "audio.mp3")

	if !hasAdjacentArgs(primary, "-b:a", "64k") {
		t.Fatalf("primary args must retain 64k CBR: %q", primary)
	}
	if hasAdjacentArgs(primary, "-q:a", "5") {
		t.Fatalf("primary args unexpectedly use VBR: %q", primary)
	}
	if !hasAdjacentArgs(fallback, "-q:a", "5") {
		t.Fatalf("fallback args must use LAME VBR quality 5: %q", fallback)
	}
	if hasAdjacentArgs(fallback, "-b:a", "64k") {
		t.Fatalf("fallback args unexpectedly use CBR: %q", fallback)
	}
	for _, args := range [][]string{primary, fallback} {
		if !hasAdjacentArgs(args, "-ac", "1") || !hasAdjacentArgs(args, "-ar", "16000") {
			t.Fatalf("speech normalization changed: %q", args)
		}
		if !hasAdjacentArgs(args, "-af", transcriptAudioFilter) {
			t.Fatalf("audio filter changed: %q", args)
		}
	}
}

func TestRunTranscriptAudioFFmpegRetriesVBRAndRemovesPartialCBR(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "calls.log")
	t.Setenv("FAKE_FFMPEG_LOG", logPath)
	ffmpeg := writeFakeFFmpeg(t, `
out=""
for arg in "$@"; do out="$arg"; done
printf '%s\n' "$*" >> "$FAKE_FFMPEG_LOG"
case " $* " in
  *" -b:a 64k "*) printf partial > "$out"; echo cbr-abort >&2; exit 134 ;;
  *" -q:a 5 "*)
    if [ -e "$out" ]; then echo partial-was-not-removed >&2; exit 9; fi
    printf complete > "$out"
    exit 0
    ;;
esac
echo unknown-arguments >&2
exit 2
`)
	outPath := filepath.Join(t.TempDir(), "audio.mp3")

	usedFallback, err := runTranscriptAudioFFmpeg(context.Background(), ffmpeg, "source.mp4", outPath)
	if err != nil {
		t.Fatal(err)
	}
	if !usedFallback {
		t.Fatal("expected VBR fallback")
	}
	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "complete" {
		t.Fatalf("output = %q, want complete VBR output", got)
	}
	calls, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(calls)), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], "-b:a 64k") || !strings.Contains(lines[1], "-q:a 5") {
		t.Fatalf("unexpected calls:\n%s", calls)
	}
}

func TestRunTranscriptAudioFFmpegRetriesEmptySuccessfulOutput(t *testing.T) {
	ffmpeg := writeFakeFFmpeg(t, `
out=""
for arg in "$@"; do out="$arg"; done
case " $* " in
  *" -b:a 64k "*) : > "$out"; exit 0 ;;
  *" -q:a 5 "*) printf complete > "$out"; exit 0 ;;
esac
exit 2
`)
	outPath := filepath.Join(t.TempDir(), "audio.mp3")
	usedFallback, err := runTranscriptAudioFFmpeg(context.Background(), ffmpeg, "source.mp4", outPath)
	if err != nil {
		t.Fatal(err)
	}
	if !usedFallback {
		t.Fatal("expected empty CBR output to trigger VBR fallback")
	}
}

func TestRunTranscriptAudioFFmpegReportsBothFailures(t *testing.T) {
	ffmpeg := writeFakeFFmpeg(t, `
out=""
for arg in "$@"; do out="$arg"; done
printf partial > "$out"
case " $* " in
  *" -b:a 64k "*) echo cbr-failed >&2; exit 134 ;;
  *" -q:a 5 "*) echo vbr-failed >&2; exit 7 ;;
esac
exit 2
`)
	outPath := filepath.Join(t.TempDir(), "audio.mp3")
	usedFallback, err := runTranscriptAudioFFmpeg(context.Background(), ffmpeg, "source.mp4", outPath)
	if !usedFallback {
		t.Fatal("expected attempted VBR fallback")
	}
	if err == nil || !strings.Contains(err.Error(), "cbr-failed") || !strings.Contains(err.Error(), "vbr-failed") {
		t.Fatalf("error must report both attempts, got %v", err)
	}
}

func TestRunTranscriptAudioFFmpegDoesNotRetryCancelledContext(t *testing.T) {
	ffmpeg := writeFakeFFmpeg(t, `
echo should-not-run >&2
exit 99
`)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	outPath := filepath.Join(t.TempDir(), "audio.mp3")
	usedFallback, err := runTranscriptAudioFFmpeg(ctx, ffmpeg, "source.mp4", outPath)
	if usedFallback {
		t.Fatal("cancelled job must not retry")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestRemoteTranscriptAudioScriptRetriesVBRAndDeletesPartial(t *testing.T) {
	script := buildRemoteTranscriptAudioScript(remoteTranscriptAudioScriptInputs{
		FFmpeg:       "/opt/ffmpeg",
		SignedURL:    "https://storage.example/source.mp4",
		FileID:       "1738",
		PublicURL:    "https://agents.example",
		StorageToken: "secret",
		ProjectID:    "project",
	})

	cbr := `-c:a libmp3lame -b:a 64k audio.mp3`
	remove := `rm -f audio.mp3`
	vbr := `-c:a libmp3lame -q:a 5 audio.mp3`
	for _, want := range []string{cbr, remove, vbr, `[ ! -s audio.mp3 ]`} {
		if !strings.Contains(script, want) {
			t.Fatalf("remote script missing %q:\n%s", want, script)
		}
	}
	if !(strings.Index(script, cbr) < strings.Index(script, remove) && strings.Index(script, remove) < strings.Index(script, vbr)) {
		t.Fatalf("remote retry order is unsafe:\n%s", script)
	}
}

func TestRemoteTranscriptAudioScriptExecutesVBRFallback(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "calls.log")
	ffmpeg := writeFakeFFmpeg(t, `
out=""
for arg in "$@"; do out="$arg"; done
printf '%s\n' "$*" >> "$FAKE_FFMPEG_LOG"
case " $* " in
  *" -b:a 64k "*) printf partial > "$out"; exit 134 ;;
  *" -q:a 5 "*)
    if [ -e "$out" ]; then echo partial-was-not-removed >&2; exit 9; fi
    printf complete > "$out"
    exit 0
    ;;
esac
exit 2
`)
	binDir := t.TempDir()
	writeExecutable(t, filepath.Join(binDir, "curl"), `printf '{"id":321}\n'`)
	script := buildRemoteTranscriptAudioScript(remoteTranscriptAudioScriptInputs{
		FFmpeg:       ffmpeg,
		SignedURL:    "https://storage.example/source.mp4",
		FileID:       "1738",
		PublicURL:    "https://agents.example",
		StorageToken: "secret",
		ProjectID:    "project",
	})
	cmd := exec.Command("bash", "-c", script)
	cmd.Env = append(os.Environ(),
		"FAKE_FFMPEG_LOG="+logPath,
		"PATH="+binDir+":"+os.Getenv("PATH"),
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("remote script: %v\n%s", err, out)
	}
	parsed, err := parseRemoteTranscriptAudio(string(out))
	if err != nil {
		t.Fatalf("parse result: %v\n%s", err, out)
	}
	if parsed.StorageFileID != 321 {
		t.Fatalf("storage file id = %d, want 321", parsed.StorageFileID)
	}
	calls, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(calls)), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], "-b:a 64k") || !strings.Contains(lines[1], "-q:a 5") {
		t.Fatalf("unexpected remote ffmpeg calls:\n%s", calls)
	}
}

// TestTranscriptAudioSleepRegression runs the actual libmp3lame failure and
// retry against the audio-only sleep.mp4 fixture. The source is intentionally
// supplied externally because the smallest faithful reproduction is 7.7 MB;
// shorter tail clips no longer trigger LAME's content-dependent CBR assertion.
//
// Run with:
//
//	MEDIA_TRANSCRIPT_AUDIO_REGRESSION_FIXTURE=/path/to/sleep-audio-only.m4a \
//	  go test -run TestTranscriptAudioSleepRegression
func TestTranscriptAudioSleepRegression(t *testing.T) {
	source := strings.TrimSpace(os.Getenv("MEDIA_TRANSCRIPT_AUDIO_REGRESSION_FIXTURE"))
	if source == "" {
		t.Skip("MEDIA_TRANSCRIPT_AUDIO_REGRESSION_FIXTURE is not set")
	}
	ffmpeg := strings.TrimSpace(os.Getenv("MEDIA_TRANSCRIPT_AUDIO_REGRESSION_FFMPEG"))
	if ffmpeg == "" {
		var err error
		ffmpeg, err = exec.LookPath("ffmpeg")
		if err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	outPath := filepath.Join(t.TempDir(), "sleep.mp3")
	usedFallback, err := runTranscriptAudioFFmpeg(ctx, ffmpeg, source, outPath)
	if err != nil {
		t.Fatal(err)
	}
	if !usedFallback {
		t.Fatal("fixture no longer reproduces the CBR failure; refresh the regression fixture or expectation")
	}
	if err := exec.Command(ffmpeg, "-v", "error", "-i", outPath, "-f", "null", "-").Run(); err != nil {
		t.Fatalf("VBR retry output does not fully decode: %v", err)
	}
}

func hasAdjacentArgs(args []string, key, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == key && args[i+1] == value {
			return true
		}
	}
	return false
}

func writeFakeFFmpeg(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ffmpeg")
	writeExecutable(t, path, body)
	return path
}

func writeExecutable(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\nset -u\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
}
