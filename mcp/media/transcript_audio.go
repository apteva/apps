package main

// Internal transcription audio proxies.
//
// Deepgram can accept remote URLs directly, but handing it a 4 GB MOV
// makes it fetch and demux a video container just to reach the audio
// stream. For video sources we first create normalized transcription audio
// under /.media/transcript-audio/, store it as an internal derivation, and
// pass that URL to Deepgram. MP3 remains the compact primary format; PCM WAV
// is the encoder-independent fallback for libmp3lame failures.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"

	sdk "github.com/apteva/app-sdk"
)

const (
	transcriptAudioFolder = "/.media/transcript-audio/"
	// Keep the existing recipe key so already-prepared MP3 derivations remain
	// reusable. New preparations still prefer MP3, but may store WAV when LAME
	// rejects an otherwise valid source.
	transcriptAudioRecipe      = "v1_loudnorm_16k_mono_mp3"
	transcriptAudioKindPrefix  = "transcript_audio:"
	transcriptAudioMP3Type     = "audio/mpeg"
	transcriptAudioWAVType     = "audio/wav"
	transcriptAudioTimeoutSec  = 600
	transcriptAudioLogMaxBytes = 64 << 10
	remoteAudioLogTailBytes    = 16 << 10
)

const transcriptAudioFilter = "loudnorm=I=-16:TP=-1.5:LRA=11,highpass=f=80,lowpass=f=8000"

func signedURLForDeepgram(ctx context.Context, app *sdk.AppCtx, sc *storageClient, media *MediaRow) (string, error) {
	fileID, err := strconv.ParseInt(media.FileID, 10, 64)
	if err != nil {
		return "", fmt.Errorf("file_id not numeric: %w", err)
	}
	if !media.HasVideo {
		return sc.GetSignedURL(ctx, media.ProjectID, fileID, 30*60)
	}
	audioID, err := ensureTranscriptAudio(ctx, app, sc, media, fileID)
	if err != nil {
		return "", err
	}
	return sc.GetSignedURL(ctx, media.ProjectID, audioID, 30*60)
}

func ensureTranscriptAudio(ctx context.Context, app *sdk.AppCtx, sc *storageClient, media *MediaRow, sourceFileID int64) (int64, error) {
	kind := transcriptAudioKind(media.SourceSHA256)
	if d, ok := findTranscriptAudioDerivation(app.AppDB(), media.ProjectID, media.FileID, kind); ok {
		if storageID, err := strconv.ParseInt(d.StorageFileID, 10, 64); err == nil && storageID > 0 {
			if transcriptAudioStorageExists(ctx, sc, media.ProjectID, d.StorageFileID) {
				return storageID, nil
			}
		}
	}

	sourceURL, err := sc.GetSignedURL(ctx, media.ProjectID, sourceFileID, 30*60)
	if err != nil {
		return 0, fmt.Errorf("source signed URL: %w", err)
	}

	var storageID int64
	hostID := int64(parseConfigIntFallback(app.Config().Get("render_host_id"), 0))
	if hostID > 0 {
		storageID, err = prepareTranscriptAudioRemote(ctx, app, media.ProjectID, hostID, sourceURL, media.FileID)
	} else {
		storageID, err = prepareTranscriptAudioLocal(ctx, app, sc, media.ProjectID, sourceURL, media.FileID)
	}
	if err != nil {
		return 0, err
	}
	if err := upsertDerivation(app.AppDB(), media.ProjectID, media.FileID, kind, storageID, 0, 0, 0); err != nil {
		return 0, fmt.Errorf("cache transcript audio derivation: %w", err)
	}
	cleanupStaleTranscriptAudioDerivations(ctx, app, sc, media.ProjectID, media.FileID, kind)
	return storageID, nil
}

func transcriptAudioKind(sourceSHA256 string) string {
	sha := strings.TrimSpace(sourceSHA256)
	if sha == "" {
		sha = "unknown"
	}
	if len(sha) > 12 {
		sha = sha[:12]
	}
	return transcriptAudioKindPrefix + sha + ":" + transcriptAudioRecipe
}

func isTranscriptAudioKind(kind string) bool {
	return strings.HasPrefix(kind, transcriptAudioKindPrefix)
}

func visibleDerivations(in []DerivationRow) []DerivationRow {
	if len(in) == 0 {
		return in
	}
	out := make([]DerivationRow, 0, len(in))
	for _, d := range in {
		if isTranscriptAudioKind(d.Kind) {
			continue
		}
		out = append(out, d)
	}
	return out
}

func findTranscriptAudioDerivation(db *sql.DB, projectID, fileID, kind string) (DerivationRow, bool) {
	for _, d := range mustListDerivations(db, projectID, fileID) {
		if d.Kind == kind && d.Status == "ok" && d.PositionMs == 0 {
			return d, true
		}
	}
	return DerivationRow{}, false
}

func mustListDerivations(db *sql.DB, projectID, fileID string) []DerivationRow {
	ds, err := listDerivations(db, projectID, fileID)
	if err != nil {
		return nil
	}
	return ds
}

func transcriptAudioStorageExists(ctx context.Context, sc *storageClient, projectID, storageFileID string) bool {
	resolved, err := sc.ResolveFiles(ctx, projectID, []string{storageFileID})
	if err != nil {
		return false
	}
	_, ok := resolved[storageFileID]
	return ok
}

func cleanupStaleTranscriptAudioDerivations(ctx context.Context, app *sdk.AppCtx, sc *storageClient, projectID, fileID, keepKind string) {
	for _, d := range mustListDerivations(app.AppDB(), projectID, fileID) {
		if !isTranscriptAudioKind(d.Kind) || d.Kind == keepKind {
			continue
		}
		if storageID, err := strconv.ParseInt(d.StorageFileID, 10, 64); err == nil && storageID > 0 {
			if err := sc.DeleteFile(ctx, projectID, storageID); err != nil {
				app.Logger().Warn("delete stale transcript audio failed",
					"file_id", fileID, "storage_file_id", storageID, "err", err)
			}
		}
		if _, err := app.AppDB().Exec(`DELETE FROM derivations WHERE id = ?`, d.ID); err != nil {
			app.Logger().Warn("delete stale transcript audio derivation row failed",
				"file_id", fileID, "derivation_id", d.ID, "err", err)
		}
	}
}

func prepareTranscriptAudioLocal(ctx context.Context, app *sdk.AppCtx, sc *storageClient, projectID, sourceURL, fileID string) (int64, error) {
	ffmpegPath := strings.TrimSpace(app.Config().Get("ffmpeg_path"))
	if ffmpegPath == "" {
		ffmpegPath = "ffmpeg"
	}
	scratchRoot := resolveScratchRoot(app, app.Config().Get("render_scratch_dir"))
	if err := os.MkdirAll(scratchRoot, 0o755); err != nil {
		scratchRoot = os.TempDir()
	}
	work, err := os.MkdirTemp(scratchRoot, "transcript-audio-*")
	if err != nil {
		return 0, fmt.Errorf("create transcript audio scratch: %w", err)
	}
	defer os.RemoveAll(work)

	mp3Path := filepath.Join(work, fileID+".mp3")
	usedPCMFallback, err := runTranscriptAudioFFmpeg(ctx, ffmpegPath, sourceURL, mp3Path)
	if err != nil {
		return 0, err
	}
	outPath := mp3Path
	outputName := fileID + ".mp3"
	contentType := transcriptAudioMP3Type
	if usedPCMFallback {
		outPath = transcriptAudioWAVPath(mp3Path)
		outputName = fileID + ".wav"
		contentType = transcriptAudioWAVType
		app.Logger().Warn("transcript audio CBR encode failed; PCM WAV retry succeeded",
			"file_id", fileID)
	}
	f, err := os.Open(outPath)
	if err != nil {
		return 0, fmt.Errorf("open transcript audio output: %w", err)
	}
	defer f.Close()
	storageID, err := sc.UploadInternalFile(ctx, projectID,
		transcriptAudioFolder, outputName, contentType, f,
		"media-transcript-audio", "internal,transcript-audio")
	if err != nil {
		return 0, fmt.Errorf("upload transcript audio: %w", err)
	}
	return storageID, nil
}

func transcriptAudioFFmpegArgs(sourceURL, outPath string) []string {
	return []string{
		"-y",
		"-loglevel", "error",
		"-nostdin",
		"-i", sourceURL,
		"-vn",
		"-map", "0:a:0",
		"-ac", "1",
		"-ar", "16000",
		"-af", transcriptAudioFilter,
		"-c:a", "libmp3lame",
		"-b:a", "64k",
		outPath,
	}
}

func transcriptAudioPCMFFmpegArgs(sourceURL, outPath string) []string {
	return []string{
		"-y",
		"-loglevel", "error",
		"-nostdin",
		"-i", sourceURL,
		"-vn",
		"-map", "0:a:0",
		"-ac", "1",
		"-ar", "16000",
		"-af", transcriptAudioFilter,
		"-c:a", "pcm_s16le",
		"-f", "wav",
		outPath,
	}
}

func transcriptAudioWAVPath(mp3Path string) string {
	ext := filepath.Ext(mp3Path)
	if ext == "" {
		return mp3Path + ".wav"
	}
	return strings.TrimSuffix(mp3Path, ext) + ".wav"
}

// runTranscriptAudioFFmpeg preserves the established 64 kbit/s CBR MP3
// output for the common path. libmp3lame's CBR and VBR paths both contain
// content-dependent fatal edge cases, so any primary failure removes the
// partial MP3 and retries with encoder-independent 16-bit PCM WAV. The caller
// uses the boolean to select the fallback path and correct Storage MIME type.
func runTranscriptAudioFFmpeg(ctx context.Context, ffmpegPath, sourceURL, outPath string) (bool, error) {
	primaryOut, primaryErr := executeTranscriptAudioFFmpeg(ctx, ffmpegPath, transcriptAudioFFmpegArgs(sourceURL, outPath), outPath)
	if primaryErr == nil {
		return false, nil
	}
	primaryFailure := transcriptAudioFFmpegFailure(primaryErr, primaryOut)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return false, fmt.Errorf("ffmpeg transcript audio CBR: %w: %s", ctxErr, primaryFailure)
	}
	if err := os.Remove(outPath); err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("remove partial transcript audio after CBR failure: %w (CBR: %s)", err, primaryFailure)
	}

	fallbackPath := transcriptAudioWAVPath(outPath)
	if err := os.Remove(fallbackPath); err != nil && !os.IsNotExist(err) {
		return true, fmt.Errorf("remove stale PCM transcript audio before retry: %w (CBR: %s)", err, primaryFailure)
	}
	fallbackOut, fallbackErr := executeTranscriptAudioFFmpeg(ctx, ffmpegPath, transcriptAudioPCMFFmpegArgs(sourceURL, fallbackPath), fallbackPath)
	if fallbackErr != nil {
		_ = os.Remove(fallbackPath)
		return true, fmt.Errorf("ffmpeg transcript audio failed (CBR: %s; PCM WAV retry: %s)",
			primaryFailure, transcriptAudioFFmpegFailure(fallbackErr, fallbackOut))
	}
	return true, nil
}

func executeTranscriptAudioFFmpeg(ctx context.Context, ffmpegPath string, args []string, outPath string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, ffmpegPath, args...)
	logs := &transcriptAudioLogBuffer{max: transcriptAudioLogMaxBytes}
	cmd.Stdout = logs
	cmd.Stderr = logs
	err := cmd.Run()
	out := logs.Bytes()
	if err != nil {
		return out, err
	}
	info, err := os.Stat(outPath)
	if err != nil {
		return out, fmt.Errorf("stat output: %w", err)
	}
	if info.Size() == 0 {
		return out, errors.New("ffmpeg produced an empty transcript audio output")
	}
	return out, nil
}

// transcriptAudioLogBuffer retains the tail of noisy FFmpeg output without
// allowing a broken encoder to consume unbounded memory. The useful LAME
// assertion and exit diagnostics are at the end of stderr.
type transcriptAudioLogBuffer struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func (b *transcriptAudioLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	if b.max <= 0 {
		return n, nil
	}
	if len(p) >= b.max {
		b.buf = append(b.buf[:0], p[len(p)-b.max:]...)
		return n, nil
	}
	b.buf = append(b.buf, p...)
	if extra := len(b.buf) - b.max; extra > 0 {
		copy(b.buf, b.buf[extra:])
		b.buf = b.buf[:b.max]
	}
	return n, nil
}

func (b *transcriptAudioLogBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.buf...)
}

func transcriptAudioFFmpegFailure(err error, out []byte) string {
	detail := strings.TrimSpace(string(out))
	if detail == "" {
		return err.Error()
	}
	return fmt.Sprintf("%v: %s", err, truncate(detail, 1200))
}

type remoteTranscriptAudioResult struct {
	StorageFileID int64 `json:"storage_file_id"`
}

func prepareTranscriptAudioRemote(ctx context.Context, app *sdk.AppCtx, projectID string, hostID int64, sourceURL, fileID string) (int64, error) {
	publicURL, err := resolvePublicURL(app)
	if err != nil {
		return 0, fmt.Errorf("remote transcript audio requires a public storage URL: %w", err)
	}
	storageToken := os.Getenv("APTEVA_OUTBOUND_TOKEN")
	if storageToken == "" {
		storageToken = os.Getenv("APTEVA_APP_TOKEN")
	}
	if storageToken == "" {
		return 0, errors.New("no outbound storage token (APTEVA_OUTBOUND_TOKEN/APP_TOKEN); remote transcript audio requires it")
	}
	paths, err := sharedRemoteInstaller().Ensure(ctx, app, hostID)
	if err != nil {
		return 0, fmt.Errorf("ffmpeg unavailable on host_id=%d: %w", hostID, err)
	}
	script := buildRemoteTranscriptAudioScript(remoteTranscriptAudioScriptInputs{
		FFmpeg:       paths.FFmpeg,
		SignedURL:    sourceURL,
		FileID:       fileID,
		PublicURL:    publicURL,
		StorageToken: storageToken,
		ProjectID:    projectID,
	})
	out, exit, err := runRemote(ctx, app, hostID, script, transcriptAudioTimeoutSec)
	if err != nil {
		if out != "" {
			return 0, fmt.Errorf("remote transcript audio ssh: %w (output: %s)", err, truncate(out, 600))
		}
		return 0, fmt.Errorf("remote transcript audio ssh: %w", err)
	}
	if exit != 0 {
		return 0, fmt.Errorf("remote transcript audio script exit=%d: %s", exit, truncate(out, 800))
	}
	res, err := parseRemoteTranscriptAudio(out)
	if err != nil {
		return 0, fmt.Errorf("parse remote transcript audio result after successful remote exit: %w (output may have been truncated; output=%s)", err, truncate(out, 400))
	}
	if res.StorageFileID <= 0 {
		return 0, errors.New("remote transcript audio returned empty storage_file_id")
	}
	return res.StorageFileID, nil
}

type remoteTranscriptAudioScriptInputs struct {
	FFmpeg       string
	SignedURL    string
	FileID       string
	PublicURL    string
	StorageToken string
	ProjectID    string
}

func buildRemoteTranscriptAudioScript(in remoteTranscriptAudioScriptInputs) string {
	var b strings.Builder
	b.WriteString("set -euo pipefail\n")
	fmt.Fprintf(&b, "WORK=%s\n", shellQuote(fmt.Sprintf("/tmp/apteva-media-transcript-audio-%s-$$", in.FileID)))
	b.WriteString(`mkdir -p "$WORK"; cd "$WORK"` + "\n")
	b.WriteString(`trap 'cd /tmp && rm -rf "$WORK"' EXIT` + "\n")
	fmt.Fprintf(&b, "export STORAGE_TOKEN=%s\n", shellQuote(in.StorageToken))
	fmt.Fprintf(&b, "export STORAGE_BASE=%s\n", shellQuote(in.PublicURL+boundStorageProxyPath))
	fmt.Fprintf(&b, "export PROJECT_ID=%s\n", shellQuote(in.ProjectID))
	fmt.Fprintf(&b, "export SRC_ID=%s\n", shellQuote(in.FileID))
	fmt.Fprintf(&b, "export SIGNED_URL=%s\n", shellQuote(in.SignedURL))
	fmt.Fprintf(&b, "export FFMPEG=%s\n", shellQuote(in.FFmpeg))
	fmt.Fprintf(&b, "export AUDIO_FILTER=%s\n", shellQuote(transcriptAudioFilter))
	fmt.Fprintf(&b, "export AUDIO_LOG_TAIL_BYTES=%d\n", remoteAudioLogTailBytes)
	b.WriteString(`CBR_STATUS=0` + "\n")
	b.WriteString(`"$FFMPEG" -y -loglevel error -nostdin -i "$SIGNED_URL" -vn -map 0:a:0 -ac 1 -ar 16000 -af "$AUDIO_FILTER" -c:a libmp3lame -b:a 64k audio.mp3 >cbr.log 2>&1 || CBR_STATUS=$?` + "\n")
	b.WriteString(`if [ "$CBR_STATUS" -eq 0 ] && [ -s audio.mp3 ]; then` + "\n")
	b.WriteString(`  AUDIO_PATH=audio.mp3` + "\n")
	b.WriteString(`  AUDIO_CONTENT_TYPE=audio/mpeg` + "\n")
	b.WriteString(`  AUDIO_FILENAME="$SRC_ID.mp3"` + "\n")
	b.WriteString(`else` + "\n")
	b.WriteString(`  if [ "$CBR_STATUS" -eq 0 ]; then CBR_STATUS=1; fi` + "\n")
	b.WriteString(`  echo "ffmpeg CBR transcript audio failed (exit=$CBR_STATUS); retrying PCM WAV" >&2` + "\n")
	b.WriteString(`  tail -c "$AUDIO_LOG_TAIL_BYTES" cbr.log >&2 2>/dev/null || true` + "\n")
	b.WriteString(`  rm -f audio.mp3 audio.wav` + "\n")
	b.WriteString(`  PCM_STATUS=0` + "\n")
	b.WriteString(`  "$FFMPEG" -y -loglevel error -nostdin -i "$SIGNED_URL" -vn -map 0:a:0 -ac 1 -ar 16000 -af "$AUDIO_FILTER" -c:a pcm_s16le -f wav audio.wav >pcm.log 2>&1 || PCM_STATUS=$?` + "\n")
	b.WriteString(`  if [ "$PCM_STATUS" -ne 0 ] || [ ! -s audio.wav ]; then` + "\n")
	b.WriteString(`    if [ "$PCM_STATUS" -eq 0 ]; then PCM_STATUS=1; fi` + "\n")
	b.WriteString(`    echo "ffmpeg PCM WAV transcript audio failed (exit=$PCM_STATUS)" >&2` + "\n")
	b.WriteString(`    tail -c "$AUDIO_LOG_TAIL_BYTES" pcm.log >&2 2>/dev/null || true` + "\n")
	b.WriteString(`    exit "$PCM_STATUS"` + "\n")
	b.WriteString(`  fi` + "\n")
	b.WriteString(`  AUDIO_PATH=audio.wav` + "\n")
	b.WriteString(`  AUDIO_CONTENT_TYPE=audio/wav` + "\n")
	b.WriteString(`  AUDIO_FILENAME="$SRC_ID.wav"` + "\n")
	b.WriteString(`fi` + "\n")
	b.WriteString(`RESP=$(curl -sS --fail -X POST \
  -H "Authorization: Bearer $STORAGE_TOKEN" \
  -F "folder=/.media/transcript-audio/" \
  -F "visibility=private" \
  -F "source=media-transcript-audio" \
  -F "tags=internal,transcript-audio" \
  -F "file=@$AUDIO_PATH;type=$AUDIO_CONTENT_TYPE;filename=$AUDIO_FILENAME" \
  "$STORAGE_BASE/files?project_id=$PROJECT_ID")` + "\n")
	b.WriteString(`AUDIO_FILE_ID=$(echo "$RESP" | sed -n 's/.*"id":[[:space:]]*\([0-9]*\).*/\1/p' | head -1)` + "\n")
	b.WriteString(`: "${AUDIO_FILE_ID:=0}"` + "\n")
	b.WriteString(`if [ "$AUDIO_FILE_ID" -le 0 ]; then echo "storage transcript audio upload returned no file id: $(printf '%.400s' "$RESP")" >&2; exit 1; fi` + "\n")
	b.WriteString(`printf 'APTEVA_TRANSCRIPT_AUDIO:{"storage_file_id":%s}\n' "$AUDIO_FILE_ID"` + "\n")
	return b.String()
}

var aptevaTranscriptAudioRE = regexp.MustCompile(`(?m)^APTEVA_TRANSCRIPT_AUDIO:(\{.*)$`)

func parseRemoteTranscriptAudio(stdout string) (*remoteTranscriptAudioResult, error) {
	m := aptevaTranscriptAudioRE.FindStringSubmatch(stdout)
	if len(m) < 2 {
		return nil, errors.New("no APTEVA_TRANSCRIPT_AUDIO marker in remote output")
	}
	var r remoteTranscriptAudioResult
	if err := json.Unmarshal([]byte(m[1]), &r); err != nil {
		return nil, fmt.Errorf("decode marker: %w (raw=%s)", err, truncate(m[1], 300))
	}
	return &r, nil
}
