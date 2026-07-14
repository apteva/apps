package main

// Internal transcription audio proxies.
//
// Deepgram can accept remote URLs directly, but handing it a 4 GB MOV
// makes it fetch and demux a video container just to reach the audio
// stream. For video sources we first create a small, normalized MP3
// under /.media/transcript-audio/, store it as an internal derivation,
// and pass that URL to Deepgram instead.

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

	sdk "github.com/apteva/app-sdk"
)

const (
	transcriptAudioFolder      = "/.media/transcript-audio/"
	transcriptAudioRecipe      = "v1_loudnorm_16k_mono_mp3"
	transcriptAudioKindPrefix  = "transcript_audio:"
	transcriptAudioContentType = "audio/mpeg"
	transcriptAudioTimeoutSec  = 600
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

	outPath := filepath.Join(work, fileID+".mp3")
	args := transcriptAudioFFmpegArgs(sourceURL, outPath)
	cmd := exec.CommandContext(ctx, ffmpegPath, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("ffmpeg transcript audio: %w: %s", err, strings.TrimSpace(string(out)))
	}
	f, err := os.Open(outPath)
	if err != nil {
		return 0, fmt.Errorf("open transcript audio output: %w", err)
	}
	defer f.Close()
	storageID, err := sc.UploadInternalFile(ctx, projectID,
		transcriptAudioFolder, fileID+".mp3", transcriptAudioContentType, f,
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
		return 0, fmt.Errorf("parse remote transcript audio result: %w (output=%s)", err, truncate(out, 400))
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
	b.WriteString(`"$FFMPEG" -y -loglevel error -nostdin -i "$SIGNED_URL" -vn -map 0:a:0 -ac 1 -ar 16000 -af "$AUDIO_FILTER" -c:a libmp3lame -b:a 64k audio.mp3` + "\n")
	b.WriteString(`if [ ! -s audio.mp3 ]; then echo "ffmpeg produced no transcript audio output" >&2; exit 1; fi` + "\n")
	b.WriteString(`RESP=$(curl -sS --fail -X POST \
  -H "Authorization: Bearer $STORAGE_TOKEN" \
  -F "folder=/.media/transcript-audio/" \
  -F "visibility=private" \
  -F "source=media-transcript-audio" \
  -F "tags=internal,transcript-audio" \
  -F "file=@audio.mp3;type=audio/mpeg;filename=$SRC_ID.mp3" \
  "$STORAGE_BASE/files?project_id=$PROJECT_ID")` + "\n")
	b.WriteString(`AUDIO_FILE_ID=$(echo "$RESP" | sed -n 's/.*"id":[[:space:]]*\([0-9]*\).*/\1/p' | head -1)` + "\n")
	b.WriteString(`: "${AUDIO_FILE_ID:=0}"` + "\n")
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
