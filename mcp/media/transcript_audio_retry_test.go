package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTranscriptAudioFFmpegArgsKeepCBRPrimaryAndUsePCMFallback(t *testing.T) {
	primary := transcriptAudioFFmpegArgs("source.mp4", "audio.mp3")
	fallback := transcriptAudioPCMFFmpegArgs("source.mp4", "audio.wav")

	if !hasAdjacentArgs(primary, "-b:a", "64k") {
		t.Fatalf("primary args must retain 64k CBR: %q", primary)
	}
	if hasAdjacentArgs(primary, "-q:a", "5") {
		t.Fatalf("primary args unexpectedly use VBR: %q", primary)
	}
	if !hasAdjacentArgs(fallback, "-c:a", "pcm_s16le") || !hasAdjacentArgs(fallback, "-f", "wav") {
		t.Fatalf("fallback args must use PCM WAV: %q", fallback)
	}
	if hasAdjacentArgs(fallback, "-c:a", "libmp3lame") || hasAdjacentArgs(fallback, "-q:a", "5") {
		t.Fatalf("fallback must not use a LAME code path: %q", fallback)
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

func TestRunTranscriptAudioFFmpegRetriesPCMWAVAndRemovesPartialCBR(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "calls.log")
	t.Setenv("FAKE_FFMPEG_LOG", logPath)
	ffmpeg := writeFakeFFmpeg(t, `
out=""
for arg in "$@"; do out="$arg"; done
printf '%s\n' "$*" >> "$FAKE_FFMPEG_LOG"
case " $* " in
  *" -b:a 64k "*) printf partial > "$out"; echo cbr-abort >&2; exit 134 ;;
  *" -c:a pcm_s16le "*)
    if [ -e "${out%.wav}.mp3" ]; then echo partial-was-not-removed >&2; exit 9; fi
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
		t.Fatal("expected PCM WAV fallback")
	}
	if _, err := os.Stat(outPath); !os.IsNotExist(err) {
		t.Fatalf("partial MP3 still exists: %v", err)
	}
	got, err := os.ReadFile(transcriptAudioWAVPath(outPath))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "complete" {
		t.Fatalf("output = %q, want complete PCM WAV output", got)
	}
	calls, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(calls)), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], "-b:a 64k") || !strings.Contains(lines[1], "-c:a pcm_s16le") {
		t.Fatalf("unexpected calls:\n%s", calls)
	}
}

func TestRunTranscriptAudioFFmpegRetriesEmptySuccessfulOutput(t *testing.T) {
	ffmpeg := writeFakeFFmpeg(t, `
out=""
for arg in "$@"; do out="$arg"; done
case " $* " in
  *" -b:a 64k "*) : > "$out"; exit 0 ;;
  *" -c:a pcm_s16le "*) printf complete > "$out"; exit 0 ;;
esac
exit 2
`)
	outPath := filepath.Join(t.TempDir(), "audio.mp3")
	usedFallback, err := runTranscriptAudioFFmpeg(context.Background(), ffmpeg, "source.mp4", outPath)
	if err != nil {
		t.Fatal(err)
	}
	if !usedFallback {
		t.Fatal("expected empty CBR output to trigger PCM WAV fallback")
	}
}

func TestRunTranscriptAudioFFmpegReportsBothFailures(t *testing.T) {
	ffmpeg := writeFakeFFmpeg(t, `
out=""
for arg in "$@"; do out="$arg"; done
printf partial > "$out"
case " $* " in
  *" -b:a 64k "*) echo cbr-failed >&2; exit 134 ;;
  *" -c:a pcm_s16le "*) echo pcm-failed >&2; exit 7 ;;
esac
exit 2
`)
	outPath := filepath.Join(t.TempDir(), "audio.mp3")
	usedFallback, err := runTranscriptAudioFFmpeg(context.Background(), ffmpeg, "source.mp4", outPath)
	if !usedFallback {
		t.Fatal("expected attempted PCM WAV fallback")
	}
	if err == nil || !strings.Contains(err.Error(), "cbr-failed") || !strings.Contains(err.Error(), "pcm-failed") {
		t.Fatalf("error must report both attempts, got %v", err)
	}
	if _, statErr := os.Stat(transcriptAudioWAVPath(outPath)); !os.IsNotExist(statErr) {
		t.Fatalf("partial WAV still exists after failed retry: %v", statErr)
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

func TestRemoteTranscriptAudioScriptUsesBoundedLogsAndPCMWAVFallback(t *testing.T) {
	script := buildRemoteTranscriptAudioScript(remoteTranscriptAudioScriptInputs{
		FFmpeg:       "/opt/ffmpeg",
		SignedURL:    "https://storage.example/source.mp4",
		FileID:       "1738",
		PublicURL:    "https://agents.example",
		StorageToken: "secret",
		ProjectID:    "project",
	})

	cbr := `-c:a libmp3lame -b:a 64k audio.mp3`
	remove := `rm -f audio.mp3 audio.wav`
	pcm := `-c:a pcm_s16le -f wav audio.wav`
	for _, want := range []string{cbr, remove, pcm, `>cbr.log 2>&1`, `tail -c "$AUDIO_LOG_TAIL_BYTES" cbr.log`} {
		if !strings.Contains(script, want) {
			t.Fatalf("remote script missing %q:\n%s", want, script)
		}
	}
	if strings.Contains(script, `-q:a 5`) {
		t.Fatalf("remote fallback must not use LAME VBR:\n%s", script)
	}
	if !(strings.Index(script, cbr) < strings.Index(script, remove) && strings.Index(script, remove) < strings.Index(script, pcm)) {
		t.Fatalf("remote retry order is unsafe:\n%s", script)
	}
}

func TestRemoteTranscriptAudioScriptPreservesMarkerAfterNoisyLAMEFailure(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "calls.log")
	curlLogPath := filepath.Join(t.TempDir(), "curl.log")
	ffmpeg := writeFakeFFmpeg(t, `
out=""
for arg in "$@"; do out="$arg"; done
printf '%s\n' "$*" >> "$FAKE_FFMPEG_LOG"
case " $* " in
  *" -b:a 64k "*)
    printf partial > "$out"
    head -c 1100000 /dev/zero | tr '\000' X >&2
    echo cbr-fatal-tail >&2
    exit 134
    ;;
  *" -c:a pcm_s16le "*)
    if [ -e "${out%.wav}.mp3" ]; then echo partial-was-not-removed >&2; exit 9; fi
    printf complete > "$out"
    exit 0
    ;;
esac
exit 2
`)
	binDir := t.TempDir()
	writeExecutable(t, filepath.Join(binDir, "curl"), `printf '%s\n' "$*" > "$FAKE_CURL_LOG"; printf '{"id":321}\n'`)
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
		"FAKE_CURL_LOG="+curlLogPath,
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
	if len(out) >= 64<<10 {
		t.Fatalf("remote output must stay safely below Instances' 1 MiB cap, got %d bytes", len(out))
	}
	if !strings.Contains(string(out), "cbr-fatal-tail") {
		t.Fatalf("bounded output lost the useful tail of the CBR failure: %q", out)
	}
	calls, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(calls)), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], "-b:a 64k") || !strings.Contains(lines[1], "-c:a pcm_s16le") {
		t.Fatalf("unexpected remote ffmpeg calls:\n%s", calls)
	}
	curlArgs, err := os.ReadFile(curlLogPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(curlArgs), "file=@audio.wav;type=audio/wav;filename=1738.wav") {
		t.Fatalf("fallback upload did not carry the WAV path and MIME type:\n%s", curlArgs)
	}
}

func TestRemoteTranscriptAudioScriptPreservesBothEncoderFailures(t *testing.T) {
	ffmpeg := writeFakeFFmpeg(t, `
out=""
for arg in "$@"; do out="$arg"; done
printf partial > "$out"
case " $* " in
  *" -b:a 64k "*) echo cbr-reservoir-fatal >&2; exit 134 ;;
  *" -c:a pcm_s16le "*) echo pcm-fallback-fatal >&2; exit 7 ;;
esac
exit 2
`)
	binDir := t.TempDir()
	writeExecutable(t, filepath.Join(binDir, "curl"), `echo curl-must-not-run >&2; exit 99`)
	script := buildRemoteTranscriptAudioScript(remoteTranscriptAudioScriptInputs{
		FFmpeg:       ffmpeg,
		SignedURL:    "https://storage.example/source.mp4",
		FileID:       "1738",
		PublicURL:    "https://agents.example",
		StorageToken: "secret",
		ProjectID:    "project",
	})
	cmd := exec.Command("bash", "-c", script)
	cmd.Env = append(os.Environ(), "PATH="+binDir+":"+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 7 {
		t.Fatalf("exit = %v, want PCM failure exit 7\n%s", err, out)
	}
	for _, want := range []string{"cbr-reservoir-fatal", "pcm-fallback-fatal"} {
		if !strings.Contains(string(out), want) {
			t.Fatalf("remote error omitted %q:\n%s", want, out)
		}
	}
	if strings.Contains(string(out), "APTEVA_TRANSCRIPT_AUDIO:") || strings.Contains(string(out), "curl-must-not-run") {
		t.Fatalf("failed preparation must not upload or emit a success marker:\n%s", out)
	}
}

func TestTranscriptAudioLogBufferRetainsBoundedTail(t *testing.T) {
	b := &transcriptAudioLogBuffer{max: 8}
	_, _ = b.Write([]byte("012345"))
	_, _ = b.Write([]byte("6789abcdef"))
	if got := string(b.Bytes()); got != "89abcdef" {
		t.Fatalf("tail = %q, want %q", got, "89abcdef")
	}
}

// TestTranscriptAudioSleepRegression runs the production sleep.mp4 through
// the complete preparation helper. The exact 35.9 MB fixture stays external
// to avoid bloating the repository; its SHA is pinned so a different input
// cannot silently make this regression pass.
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
	f, err := os.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		t.Fatal(err)
	}
	if got, want := hex.EncodeToString(h.Sum(nil)), "fec3d363fd57d707b2158698c46ab3ebd0acc978020505e5cb15b61e8a614277"; got != want {
		t.Fatalf("fixture SHA-256 = %s, want production sleep.mp4 SHA-256 %s", got, want)
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
	preparedPath := outPath
	if usedFallback {
		preparedPath = transcriptAudioWAVPath(outPath)
	}
	if err := exec.Command(ffmpeg, "-v", "error", "-i", preparedPath, "-f", "null", "-").Run(); err != nil {
		t.Fatalf("prepared transcript audio does not fully decode: %v", err)
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
