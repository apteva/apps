package main

// Remote read-only media analysis. When render_host_id is configured,
// media_analyze follows the same Instances host used by rendering, indexing,
// smart-crop sampling, and transcript audio preparation. It intentionally does
// not fall back to local execution: selecting a host is an operator decision to
// keep ffmpeg CPU and memory away from the Media sidecar.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

const remoteAnalysisMarker = "APTEVA_ANALYZE:"

type remoteAnalysisWireResult struct {
	VisualRan  bool   `json:"visual_ran"`
	VisualExit int    `json:"visual_exit"`
	VisualLog  string `json:"visual_log_b64"`
	AudioRan   bool   `json:"audio_ran"`
	AudioExit  int    `json:"audio_exit"`
	AudioLog   string `json:"audio_log_b64"`
}

var ensureRemoteAnalysisFFmpeg = func(ctx context.Context, app *sdk.AppCtx, hostID int64) (installedPaths, error) {
	return sharedRemoteInstaller().Ensure(ctx, app, hostID)
}

var runRemoteAnalysisCommand = runRemote

func analyzeExistingSourceRemote(app *sdk.AppCtx, sourceURL string, row *MediaRow, opts analysisOptions, hostID int64) (qualityAnalysis, error) {
	result := qualityAnalysis{
		DecodeOK: true,
		Issues:   make([]analysisIssue, 0),
		Executor: "remote-instance",
		HostID:   hostID,
	}
	cctx, cancel := context.WithTimeout(context.Background(), opts.Timeout)
	defer cancel()

	paths, err := ensureRemoteAnalysisFFmpeg(cctx, app, hostID)
	if err != nil {
		return result, fmt.Errorf("ANALYSIS_EXECUTOR_UNAVAILABLE: ffmpeg unavailable on host_id=%d: %w", hostID, err)
	}

	script := buildRemoteAnalysisScript(paths.FFmpeg, sourceURL, row, opts)
	timeoutS := int(opts.Timeout.Seconds())
	if timeoutS < 1 {
		timeoutS = 1
	}
	out, exit, runErr := runRemoteAnalysisCommand(cctx, app, hostID, script, timeoutS)
	if runErr != nil {
		if cctx.Err() != nil {
			return result, fmt.Errorf("ANALYSIS_EXECUTOR_UNAVAILABLE: remote analysis host_id=%d timed out: %w", hostID, cctx.Err())
		}
		return result, fmt.Errorf("ANALYSIS_EXECUTOR_UNAVAILABLE: remote analysis host_id=%d: %w", hostID, runErr)
	}
	if exit != 0 {
		return result, fmt.Errorf("ANALYSIS_EXECUTOR_UNAVAILABLE: remote analysis host_id=%d script exit=%d", hostID, exit)
	}

	wire, err := parseRemoteAnalysisResult(out)
	if err != nil {
		// Do not include raw remote output: even though the script compacts
		// ffmpeg logs, a shell-level error could contain the signed source URL.
		return result, fmt.Errorf("ANALYSIS_EXECUTOR_UNAVAILABLE: parse remote analysis host_id=%d result: %w", hostID, err)
	}

	if wire.VisualRan {
		log, decodeErr := decodeRemoteAnalysisLog(wire.VisualLog)
		if decodeErr != nil {
			return result, fmt.Errorf("ANALYSIS_EXECUTOR_UNAVAILABLE: decode remote visual analysis: %w", decodeErr)
		}
		result.Visual = parseVisualAnalysis(log, opts.StartMs, opts.EndMs)
		if wire.VisualExit != 0 {
			result.DecodeOK = false
			result.Issues = append(result.Issues, commandFailureIssue(
				"VISUAL_ANALYSIS_FAILED", remoteFFmpegExitError("visual", wire.VisualExit), log))
		}
		result.Issues = append(result.Issues, visualIssues(result.Visual)...)
	}
	if wire.AudioRan {
		log, decodeErr := decodeRemoteAnalysisLog(wire.AudioLog)
		if decodeErr != nil {
			return result, fmt.Errorf("ANALYSIS_EXECUTOR_UNAVAILABLE: decode remote audio analysis: %w", decodeErr)
		}
		result.Audio = parseAudioAnalysis(log, opts)
		if wire.AudioExit != 0 {
			result.DecodeOK = false
			result.Issues = append(result.Issues, commandFailureIssue(
				"AUDIO_ANALYSIS_FAILED", remoteFFmpegExitError("audio", wire.AudioExit), log))
		}
		result.Issues = append(result.Issues, audioIssues(result.Audio)...)
	}
	if !wire.VisualRan && !wire.AudioRan {
		return result, errors.New("media has no analyzable image, video, or audio stream")
	}
	return result, nil
}

func buildRemoteAnalysisScript(ffmpegPath, sourceURL string, row *MediaRow, opts analysisOptions) string {
	var b strings.Builder
	b.WriteString("set -u\n")
	b.WriteString(`WORK=$(mktemp -d /tmp/apteva-media-analyze.XXXXXX)` + "\n")
	b.WriteString(`trap 'rm -rf "$WORK"' EXIT` + "\n")
	b.WriteString(`cd "$WORK"` + "\n")
	b.WriteString("VISUAL_RAN=false\nVISUAL_EXIT=0\nAUDIO_RAN=false\nAUDIO_EXIT=0\n")

	if row.HasVideo || row.IsImage {
		b.WriteString("VISUAL_RAN=true\n")
		b.WriteString(shellCommand(ffmpegPath, visualAnalysisArgs(sourceURL, row, opts)))
		b.WriteString(" > visual.log 2>&1\nVISUAL_EXIT=$?\n")
	}
	if row.HasAudio {
		b.WriteString("AUDIO_RAN=true\n")
		b.WriteString(shellCommand(ffmpegPath, audioAnalysisArgs(sourceURL, opts)))
		b.WriteString(" > audio.log 2>&1\nAUDIO_EXIT=$?\n")
	}

	// Return only lines consumed by the Go parsers. Besides keeping signed URLs
	// out of the Instances response, this bounds output for depth=full on long
	// media: ebur128's verbose per-frame log can otherwise exceed command-output
	// limits before the result marker arrives.
	b.WriteString(`if [ -f visual.log ]; then grep -E 'lavfi\.(signalstats\.[A-Z]+|blur|block|freezedetect\.)|black_start:' visual.log > visual.safe || true; else : > visual.safe; fi` + "\n")
	b.WriteString(`if [ -f audio.log ]; then grep -E '^[[:space:]]*(I:|LRA:|Peak:)|Peak level dB:|RMS level dB:|Dynamic range:|DC offset:|silence_start:|silence_end:' audio.log > audio.safe || true; else : > audio.safe; fi` + "\n")
	b.WriteString(`VISUAL_B64=$(base64 -w0 visual.safe 2>/dev/null || base64 < visual.safe | tr -d '\n')` + "\n")
	b.WriteString(`AUDIO_B64=$(base64 -w0 audio.safe 2>/dev/null || base64 < audio.safe | tr -d '\n')` + "\n")
	b.WriteString(`printf 'APTEVA_ANALYZE:{"visual_ran":%s,"visual_exit":%s,"visual_log_b64":"%s","audio_ran":%s,"audio_exit":%s,"audio_log_b64":"%s"}\n' "$VISUAL_RAN" "$VISUAL_EXIT" "$VISUAL_B64" "$AUDIO_RAN" "$AUDIO_EXIT" "$AUDIO_B64"` + "\n")
	return b.String()
}

func shellCommand(path string, args []string) string {
	var b strings.Builder
	b.WriteString(shellQuote(path))
	for _, arg := range args {
		b.WriteByte(' ')
		b.WriteString(shellQuote(arg))
	}
	return b.String()
}

func parseRemoteAnalysisResult(out string) (remoteAnalysisWireResult, error) {
	var raw string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.HasPrefix(line, remoteAnalysisMarker) {
			raw = strings.TrimPrefix(line, remoteAnalysisMarker)
		}
	}
	if raw == "" {
		return remoteAnalysisWireResult{}, errors.New("result marker missing")
	}
	var result remoteAnalysisWireResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return remoteAnalysisWireResult{}, err
	}
	return result, nil
}

func decodeRemoteAnalysisLog(encoded string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func remoteFFmpegExitError(kind string, exit int) error {
	if exit > 128 {
		return fmt.Errorf("remote ffmpeg %s terminated by signal %d (exit=%d)", kind, exit-128, exit)
	}
	return fmt.Errorf("remote ffmpeg %s exit=%s", kind, strconv.Itoa(exit))
}
