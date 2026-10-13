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
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const remoteAnalysisMarker = "APTEVA_ANALYZE:"

type remoteAnalysisSample struct {
	StartMs   int64  `json:"start_ms"`
	EndMs     int64  `json:"end_ms"`
	Exit      int    `json:"exit"`
	Log       string `json:"log_b64"`
	ElapsedMs int64  `json:"elapsed_ms"`
}
type remoteAnalysisWireResult struct {
	Samples        []remoteAnalysisSample `json:"samples,omitempty"`
	SourceCacheHit bool                   `json:"source_cache_hit"`
	DecodeThreads  int                    `json:"decoder_threads"`
	VisualRan      bool                   `json:"visual_ran"`
	VisualExit     int                    `json:"visual_exit"`
	VisualLog      string                 `json:"visual_log_b64"`
	AudioRan       bool                   `json:"audio_ran"`
	AudioExit      int                    `json:"audio_exit"`
	AudioLog       string                 `json:"audio_log_b64"`
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
	cctx, cancel, release, admission, err := beginAnalysis(app, opts)
	if err != nil {
		return result, err
	}
	defer cancel()
	defer release()
	if opts.WorkDir == "" {
		opts.WorkDir = uniqueRemoteWorkDir(-time.Now().UnixNano())
	}
	started := time.Now()
	paths, err := ensureRemoteAnalysisFFmpeg(cctx, app, hostID)
	if err != nil {
		return result, fmt.Errorf("ANALYSIS_EXECUTOR_UNAVAILABLE: ffmpeg unavailable on host_id=%d: %w", hostID, err)
	}

	script := buildRemoteAnalysisScript(paths.FFmpeg, sourceURL, row, opts)
	timeoutS := int(opts.Timeout.Seconds())
	if timeoutS < 1 {
		timeoutS = 1
	}
	finish := watchAnalysisRemoteCancellation(cctx, app, hostID, opts.WorkDir)
	out, exit, runErr := runRemoteAnalysisCommand(cctx, app, hostID, "setsid bash -c "+shellQuote(script), timeoutS)
	if cancelErr := finish(); cancelErr != nil {
		return result, cancelErr
	}
	if failure := mediaWorkFailure(cctx); failure != nil {
		return result, failure
	}
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

	d := analysisDiagnostics{Algorithm: analysisRecipe, ElapsedMs: time.Since(started).Milliseconds(), AdmissionMs: admission.Milliseconds(), DecodeThreads: wire.DecodeThreads, FilterThreads: 1, SourceCacheHit: wire.SourceCacheHit, InputPasses: 1, SourceAccess: "http_range_stream", TimeoutSeconds: timeoutS}
	if opts.Depth != "quick" {
		d.SourceAccess = "http_stream"
	}
	if wire.SourceCacheHit {
		d.SourceAccess = "verified_local_cache"
	}
	if len(wire.Samples) > 0 {
		results := []qualityAnalysis{}
		windows := []analysisWindow{}
		for _, sample := range wire.Samples {
			log, e := decodeRemoteAnalysisLog(sample.Log)
			if e != nil {
				return result, errors.New("analysis_result_unavailable: invalid sample log")
			}
			sampleOpts := opts
			sampleOpts.StartMs = sample.StartMs
			sampleOpts.EndMs = sample.EndMs
			var failure error
			if sample.Exit != 0 {
				failure = remoteFFmpegExitError("combined", sample.Exit)
			}
			q := qualityFromLog(log, failure, row, sampleOpts)
			q.Diagnostics.ElapsedMs = sample.ElapsedMs
			results = append(results, q)
			windows = append(windows, analysisWindow{sample.StartMs, sample.EndMs})
		}
		d.InputPasses = len(windows)
		if opts.Depth == "quick" {
			d.ContinuityWidth = 640
			q := mergeQuickQuality(results, windows, d)
			q.Executor = "remote-instance"
			q.HostID = hostID
			return q, nil
		}
		q := results[0]
		q.Diagnostics = d
		q.Executor = "remote-instance"
		q.HostID = hostID
		return q, nil
	}
	result.Diagnostics = d
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
	if opts.WorkDir != "" {
		fmt.Fprintf(&b, "WORK=%s\nmkdir -p \"$WORK\"\n", shellQuote(opts.WorkDir))
	} else {
		b.WriteString("WORK=$(mktemp -d /var/tmp/apteva-media-analyze.XXXXXX)\n")
	}
	b.WriteString("trap 'rm -rf \"$WORK\"' EXIT\ncd \"$WORK\"\necho $$ > pid\n[ ! -f cancel.requested ] || exit 1\n")
	fmt.Fprintf(&b, "SOURCE=%s\nSOURCE_CACHE_HIT=false\n", shellQuote(sourceURL))
	if meta := opts.SourceMeta; meta != nil && len(meta.SHA256) == 64 {
		fmt.Fprintf(&b, "SOURCE_CACHE_ROOT=%s\nSOURCE_CACHE_MAX_BYTES=%d\n", shellQuote(remoteSourceCacheRoot), remoteSourceCacheDefaultMaxBytes)
		b.WriteString(remoteSourceCacheScriptFragment)
		cache := remoteSourceCacheRoot + "/" + remoteSourceCacheName(fmt.Sprint(meta.ID), meta.Name, meta.SHA256, meta.SizeBytes)
		fmt.Fprintf(&b, "if cache_valid %s %d %s; then\n link_cached_source %s source-input\n SOURCE=source-input\n SOURCE_CACHE_HIT=true\nfi\n", shellQuote(cache), meta.SizeBytes, shellQuote(meta.SHA256), shellQuote(cache))
	}
	threads := max(1, min(4, opts.DecoderThreads))
	fmt.Fprintf(&b, "CPUS=$(getconf _NPROCESSORS_ONLN 2>/dev/null || echo 1)\nDECODER_THREADS=%d\nif [ \"$DECODER_THREADS\" -gt \"$CPUS\" ]; then DECODER_THREADS=$CPUS; fi\nSAMPLES=\"\"\n", threads)
	windows := []analysisWindow{{opts.StartMs, opts.EndMs}}
	if opts.Depth == "quick" {
		windows = quickWindows(opts, row)
	}
	for _, w := range windows {
		sample := opts
		sample.StartMs = w.StartMs
		sample.EndMs = w.EndMs
		args := analysisBoundedArgs(analysisFFmpegArgs("{analysis_source}", row, sample), opts)
		// Shell substitution is limited to controlled placeholders, never raw URLs.
		command := shellCommand(ffmpegPath, args)
		command = strings.ReplaceAll(command, shellQuote("{analysis_source}"), "\"$SOURCE\"")
		command = strings.ReplaceAll(command, "'-threads' '"+fmt.Sprint(min(threads, runtimeCPUCount()))+"'", "'-threads' \"$DECODER_THREADS\"")
		b.WriteString("START=$(date +%s)\n" + command + " > combined.log 2>&1\nEXIT_CODE=$?\n")
		b.WriteString("if [ \"$EXIT_CODE\" -eq 137 ] || grep -qiE 'out of memory|cannot allocate memory' combined.log; then echo media_resource_exhausted; exit 137; fi\n")
		b.WriteString("awk " + shellQuote(compactFrameLogAWK) + " combined.log > combined.safe\nLOG=$(base64 -w0 combined.safe 2>/dev/null || base64 < combined.safe | tr -d '\\n')\nELAPSED=$(( ($(date +%s) - START) * 1000 ))\n")
		fmt.Fprintf(&b, "ENTRY=$(printf '{\"start_ms\":%d,\"end_ms\":%d,\"exit\":%%s,\"log_b64\":\"%%s\",\"elapsed_ms\":%%s}' \"$EXIT_CODE\" \"$LOG\" \"$ELAPSED\")\n", w.StartMs, w.EndMs)
		b.WriteString("if [ -n \"$SAMPLES\" ]; then SAMPLES=\"$SAMPLES,$ENTRY\"; else SAMPLES=$ENTRY; fi\n")
	}
	b.WriteString("printf 'APTEVA_ANALYZE:{\"samples\":[%s],\"source_cache_hit\":%s,\"decoder_threads\":%s,\"visual_ran\":%s,\"visual_exit\":%s,\"visual_log_b64\":\"%s\",\"audio_ran\":%s,\"audio_exit\":%s,\"audio_log_b64\":\"%s\"}\\n' \"$SAMPLES\" \"$SOURCE_CACHE_HIT\" \"$DECODER_THREADS\" ")
	fmt.Fprintf(&b, "%t \"$EXIT_CODE\" \"$LOG\" %t \"$EXIT_CODE\" \"$LOG\"\n", row.HasVideo || row.IsImage, row.HasAudio)
	return b.String()
}

func runtimeCPUCount() int { return runtime.NumCPU() }

// Join cancellation before returning. HTTP expiry, app shutdown and timeouts
// terminate only this guarded process group; a failed stop remains an error.
func watchAnalysisRemoteCancellation(ctx context.Context, app *sdk.AppCtx, hostID int64, work string) func() error {
	done := make(chan struct{})
	finished := make(chan struct{})
	var once sync.Once
	var result error
	kill := func() {
		once.Do(func() {
			out, exit, err := runRemoteCancellation(context.Background(), app, hostID, remoteCancellationScript(work), 20)
			if err != nil || exit != 0 || !strings.Contains(out, "REMOTE_CANCEL_CONFIRMED") {
				result = errors.New("analysis_cancellation_failed: remote worker termination could not be verified")
			}
		})
	}
	go func() {
		defer close(finished)
		select {
		case <-ctx.Done():
			kill()
		case <-done:
		}
	}()
	return func() error {
		close(done)
		if ctx.Err() != nil {
			kill()
		}
		<-finished
		return result
	}
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
