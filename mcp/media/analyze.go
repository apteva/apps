package main

// Read-only technical and quality analysis for image, video, and audio
// sources. The analyzer streams the existing Storage object through
// ffmpeg filters and writes to the null muxer: no render row, derivation,
// temporary media file, or Storage object is created.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const (
	standardAnalysisMaxMs = int64(60_000)
	defaultSilenceDB      = -50.0
	defaultSilenceMinMs   = int64(1_000)
	clippingRiskDB        = -0.1
)

type analysisIssue struct {
	Code     string  `json:"code"`
	Severity string  `json:"severity"`
	Message  string  `json:"message"`
	StartMs  int64   `json:"start_ms,omitempty"`
	EndMs    int64   `json:"end_ms,omitempty"`
	Value    float64 `json:"value,omitempty"`
	Unit     string  `json:"unit,omitempty"`
}

type timedSegment struct {
	StartMs    int64 `json:"start_ms"`
	EndMs      int64 `json:"end_ms"`
	DurationMs int64 `json:"duration_ms"`
}

type visualAnalysis struct {
	SampledFrames       int            `json:"sampled_frames"`
	MeanLuma            *float64       `json:"mean_luma,omitempty"`
	MinLuma             *float64       `json:"min_luma,omitempty"`
	MaxLuma             *float64       `json:"max_luma,omitempty"`
	MeanSaturation      *float64       `json:"mean_saturation,omitempty"`
	MeanBlurScore       *float64       `json:"mean_blur_score,omitempty"`
	MeanBlockinessScore *float64       `json:"mean_blockiness_score,omitempty"`
	BlackSegments       []timedSegment `json:"black_segments"`
	FrozenSegments      []timedSegment `json:"frozen_segments"`
}

type audioAnalysis struct {
	IntegratedLUFS      *float64       `json:"integrated_lufs,omitempty"`
	LoudnessRangeLU     *float64       `json:"loudness_range_lu,omitempty"`
	MaxTruePeakDBTP     *float64       `json:"max_true_peak_dbtp,omitempty"`
	MaxSamplePeakDBFS   *float64       `json:"max_sample_peak_dbfs,omitempty"`
	RMSDBFS             *float64       `json:"rms_dbfs,omitempty"`
	DynamicRangeDB      *float64       `json:"dynamic_range_db,omitempty"`
	DCOffset            *float64       `json:"dc_offset,omitempty"`
	ClippingDetected    bool           `json:"clipping_detected"`
	ClippingThresholdDB float64        `json:"clipping_threshold_dbfs"`
	SilenceThresholdDB  float64        `json:"silence_threshold_dbfs"`
	SilenceMinMs        int64          `json:"silence_min_ms"`
	SilenceTotalMs      int64          `json:"silence_total_ms"`
	SilenceRatio        float64        `json:"silence_ratio"`
	LongestSilenceMs    int64          `json:"longest_silence_ms"`
	SilenceSegments     []timedSegment `json:"silence_segments"`
}

type analysisCoverage struct {
	Depth              string  `json:"depth"`
	StartMs            int64   `json:"start_ms"`
	EndMs              int64   `json:"end_ms,omitempty"`
	AnalyzedDurationMs int64   `json:"analyzed_duration_ms,omitempty"`
	SourceDurationMs   int64   `json:"source_duration_ms,omitempty"`
	Ratio              float64 `json:"ratio"`
	Complete           bool    `json:"complete"`
	ArtifactsCreated   bool    `json:"artifacts_created"`
}

type mediaAnalysisResult struct {
	FileID    string           `json:"file_id"`
	MediaType string           `json:"media_type"`
	Analysis  analysisCoverage `json:"analysis"`
	Technical map[string]any   `json:"technical"`
	Visual    *visualAnalysis  `json:"visual,omitempty"`
	Audio     *audioAnalysis   `json:"audio,omitempty"`
	Issues    []analysisIssue  `json:"issues"`
}

type analysisOptions struct {
	Depth              string
	StartMs            int64
	EndMs              int64
	SilenceThresholdDB float64
	SilenceMinMs       int64
	Timeout            time.Duration
}

// mediaAnalysisRunner is replaceable in unit tests so the MCP handler can be
// exercised without making a real network request or invoking ffmpeg.
var mediaAnalysisRunner = analyzeExistingSource

func (a *App) toolAnalyze(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	fid, _ := args["file_id"].(string)
	if strings.TrimSpace(fid) == "" {
		return nil, errors.New("file_id required")
	}
	row, err := getMedia(ctx.AppDB(), pid, fid)
	if err != nil {
		if notFound(err) {
			return map[string]any{"found": false}, nil
		}
		return nil, err
	}
	if row.ProbeStatus != "ok" {
		return nil, fmt.Errorf("media %s is not ready for analysis (probe_status=%s)", fid, row.ProbeStatus)
	}

	opts, err := parseAnalysisOptions(ctx, row, args)
	if err != nil {
		return nil, err
	}
	source, err := signedFetchURLForMedia(ctx, pid, fid, storageDeliveryProxy, storageDispositionInline)
	if err != nil || source.URL == "" {
		if err == nil {
			err = errors.New("empty signed URL")
		}
		return nil, fmt.Errorf("read source for analysis: %w", err)
	}
	result := baseAnalysisResult(row, opts)
	quality, err := mediaAnalysisRunner(ctx, source.URL, row, opts)
	if err != nil {
		return nil, err
	}
	result.Visual = quality.Visual
	result.Audio = quality.Audio
	result.Issues = append(result.Issues, quality.Issues...)
	result.Technical["decode_ok"] = quality.DecodeOK
	sort.SliceStable(result.Issues, func(i, j int) bool {
		if result.Issues[i].StartMs == result.Issues[j].StartMs {
			return result.Issues[i].Code < result.Issues[j].Code
		}
		return result.Issues[i].StartMs < result.Issues[j].StartMs
	})
	return map[string]any{"found": true, "result": result}, nil
}

func parseAnalysisOptions(ctx *sdk.AppCtx, row *MediaRow, args map[string]any) (analysisOptions, error) {
	depth, _ := args["depth"].(string)
	depth = strings.ToLower(strings.TrimSpace(depth))
	if depth == "" {
		depth = "standard"
	}
	if depth != "standard" && depth != "full" {
		return analysisOptions{}, errors.New("depth must be standard or full")
	}
	start := int64Arg(args["start_ms"])
	end := int64Arg(args["end_ms"])
	if start < 0 || end < 0 {
		return analysisOptions{}, errors.New("start_ms and end_ms must be non-negative")
	}
	if row.DurationMs > 0 && start >= row.DurationMs {
		return analysisOptions{}, errors.New("start_ms must be before the end of the source")
	}
	if end > 0 && end <= start {
		return analysisOptions{}, errors.New("end_ms must be greater than start_ms")
	}
	if row.IsImage {
		start, end = 0, 0
	} else if end == 0 {
		end = row.DurationMs
		if depth == "standard" && (end == 0 || end-start > standardAnalysisMaxMs) {
			end = start + standardAnalysisMaxMs
		}
	}
	if row.DurationMs > 0 && end > row.DurationMs {
		end = row.DurationMs
	}

	silenceDB := numberArg(args["silence_threshold_db"], defaultSilenceDB)
	silenceMin := int64Arg(args["silence_min_ms"])
	if silenceMin == 0 {
		silenceMin = defaultSilenceMinMs
	}
	if silenceMin < 100 {
		return analysisOptions{}, errors.New("silence_min_ms must be at least 100")
	}
	timeoutSec := parseConfigIntFallback(ctx.Config().Get("analyze_timeout_seconds"), 300)
	return analysisOptions{
		Depth: depth, StartMs: start, EndMs: end,
		SilenceThresholdDB: silenceDB, SilenceMinMs: silenceMin,
		Timeout: time.Duration(timeoutSec) * time.Second,
	}, nil
}

func numberArg(v any, fallback float64) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case float32:
		return float64(n)
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case json.Number:
		if f, err := n.Float64(); err == nil {
			return f
		}
	case string:
		if f, err := strconv.ParseFloat(strings.TrimSpace(n), 64); err == nil {
			return f
		}
	}
	return fallback
}

func mediaTypeFor(row *MediaRow) string {
	switch {
	case row.IsImage:
		return "image"
	case row.HasVideo:
		return "video"
	case row.HasAudio:
		return "audio"
	default:
		return "unknown"
	}
}

func baseAnalysisResult(row *MediaRow, opts analysisOptions) mediaAnalysisResult {
	analyzed := opts.EndMs - opts.StartMs
	if row.IsImage {
		analyzed = 0
	}
	complete := row.IsImage || row.DurationMs == 0 || (opts.StartMs == 0 && opts.EndMs >= row.DurationMs)
	ratio := 1.0
	if row.DurationMs > 0 {
		ratio = math.Min(1, math.Max(0, float64(analyzed)/float64(row.DurationMs)))
	}
	technical := map[string]any{
		"format_name":    row.FormatName,
		"duration_ms":    row.DurationMs,
		"bitrate":        row.Bitrate,
		"has_video":      row.HasVideo,
		"has_audio":      row.HasAudio,
		"width":          row.Width,
		"height":         row.Height,
		"rotation":       row.Rotation,
		"fps":            row.FPS,
		"video_codec":    row.VideoCodec,
		"audio_codec":    row.AudioCodec,
		"channels":       row.Channels,
		"sample_rate_hz": row.SampleRate,
	}
	if streams := technicalStreams(row.RawProbe); len(streams) > 0 {
		technical["streams"] = streams
	}
	issues := make([]analysisIssue, 0)
	if !complete {
		issues = append(issues, analysisIssue{
			Code: "PARTIAL_ANALYSIS", Severity: "info",
			Message: fmt.Sprintf("Analysis covers %.1f%% of the source; use depth=full or an explicit range for broader coverage.", ratio*100),
			StartMs: opts.StartMs, EndMs: opts.EndMs,
		})
	}
	return mediaAnalysisResult{
		FileID: row.FileID, MediaType: mediaTypeFor(row), Technical: technical,
		Analysis: analysisCoverage{
			Depth: opts.Depth, StartMs: opts.StartMs, EndMs: opts.EndMs,
			AnalyzedDurationMs: analyzed, SourceDurationMs: row.DurationMs,
			Ratio: ratio, Complete: complete, ArtifactsCreated: false,
		},
		Issues: issues,
	}
}

type rawProbeTechnical struct {
	Streams []struct {
		Index            int    `json:"index"`
		CodecName        string `json:"codec_name"`
		CodecLongName    string `json:"codec_long_name"`
		CodecType        string `json:"codec_type"`
		Profile          string `json:"profile"`
		PixelFormat      string `json:"pix_fmt"`
		Width            int    `json:"width"`
		Height           int    `json:"height"`
		ColorRange       string `json:"color_range"`
		ColorSpace       string `json:"color_space"`
		ColorTransfer    string `json:"color_transfer"`
		ColorPrimaries   string `json:"color_primaries"`
		BitsPerRawSample string `json:"bits_per_raw_sample"`
		SampleFormat     string `json:"sample_fmt"`
		SampleRate       string `json:"sample_rate"`
		Channels         int    `json:"channels"`
		ChannelLayout    string `json:"channel_layout"`
		BitRate          string `json:"bit_rate"`
		RFrameRate       string `json:"r_frame_rate"`
		AvgFrameRate     string `json:"avg_frame_rate"`
	} `json:"streams"`
}

func technicalStreams(raw json.RawMessage) []map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var p rawProbeTechnical
	if json.Unmarshal(raw, &p) != nil {
		return nil
	}
	out := make([]map[string]any, 0, len(p.Streams))
	for _, s := range p.Streams {
		m := map[string]any{"index": s.Index, "type": s.CodecType, "codec": s.CodecName}
		putString := func(k, v string) {
			if strings.TrimSpace(v) != "" {
				m[k] = v
			}
		}
		putString("codec_long_name", s.CodecLongName)
		putString("profile", s.Profile)
		putString("pixel_format", s.PixelFormat)
		putString("color_range", s.ColorRange)
		putString("color_space", s.ColorSpace)
		putString("color_transfer", s.ColorTransfer)
		putString("color_primaries", s.ColorPrimaries)
		putString("bits_per_raw_sample", s.BitsPerRawSample)
		putString("sample_format", s.SampleFormat)
		putString("sample_rate_hz", s.SampleRate)
		putString("channel_layout", s.ChannelLayout)
		putString("bitrate", s.BitRate)
		putString("r_frame_rate", s.RFrameRate)
		putString("avg_frame_rate", s.AvgFrameRate)
		if s.Width > 0 {
			m["width"] = s.Width
		}
		if s.Height > 0 {
			m["height"] = s.Height
		}
		if s.Channels > 0 {
			m["channels"] = s.Channels
		}
		out = append(out, m)
	}
	return out
}

type qualityAnalysis struct {
	Visual   *visualAnalysis
	Audio    *audioAnalysis
	Issues   []analysisIssue
	DecodeOK bool
}

func analyzeExistingSource(app *sdk.AppCtx, sourceURL string, row *MediaRow, opts analysisOptions) (qualityAnalysis, error) {
	cctx, cancel := context.WithTimeout(context.Background(), opts.Timeout)
	defer cancel()
	ffmpegPath := strings.TrimSpace(app.Config().Get("ffmpeg_path"))
	if ffmpegPath == "" {
		ffmpegPath = "ffmpeg"
	}
	result := qualityAnalysis{DecodeOK: true, Issues: make([]analysisIssue, 0)}
	commandsRun := 0
	if row.HasVideo || row.IsImage {
		commandsRun++
		log, runErr := runAnalysisFFmpeg(cctx, ffmpegPath, visualAnalysisArgs(sourceURL, row, opts))
		result.Visual = parseVisualAnalysis(log, opts.StartMs, opts.EndMs)
		if runErr != nil {
			result.DecodeOK = false
			result.Issues = append(result.Issues, commandFailureIssue("VISUAL_ANALYSIS_FAILED", runErr, log))
		}
		result.Issues = append(result.Issues, visualIssues(result.Visual)...)
	}
	if row.HasAudio {
		commandsRun++
		log, runErr := runAnalysisFFmpeg(cctx, ffmpegPath, audioAnalysisArgs(sourceURL, opts))
		result.Audio = parseAudioAnalysis(log, opts)
		if runErr != nil {
			result.DecodeOK = false
			result.Issues = append(result.Issues, commandFailureIssue("AUDIO_ANALYSIS_FAILED", runErr, log))
		}
		result.Issues = append(result.Issues, audioIssues(result.Audio)...)
	}
	if commandsRun == 0 {
		return result, errors.New("media has no analyzable image, video, or audio stream")
	}
	// A corrupt or partially decodable source is still a successful
	// inspection result: callers need the technical metadata plus the
	// explicit failure issue to diagnose it. Only a row with no media
	// streams at all is a hard tool error.
	return result, nil
}

func runAnalysisFFmpeg(ctx context.Context, path string, args []string) (string, error) {
	cmd := exec.CommandContext(ctx, path, args...)
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return string(out), fmt.Errorf("ffmpeg analysis timed out: %w", ctx.Err())
	}
	return string(out), err
}

func commonRangeArgs(sourceURL string, opts analysisOptions) []string {
	args := []string{"-hide_banner", "-nostats", "-v", "info"}
	if opts.StartMs > 0 {
		args = append(args, "-ss", formatSeconds(opts.StartMs))
	}
	args = append(args, "-i", sourceURL)
	if opts.EndMs > opts.StartMs {
		args = append(args, "-t", formatSeconds(opts.EndMs-opts.StartMs))
	}
	return args
}

func visualAnalysisArgs(sourceURL string, row *MediaRow, opts analysisOptions) []string {
	args := commonRangeArgs(sourceURL, opts)
	filter := "scale=w='min(1280,iw)':h=-2,signalstats,blurdetect,blockdetect,metadata=print"
	if !row.IsImage {
		// Continuity checks see every decoded frame. Expensive per-frame
		// visual metrics run only on one representative sample every 5s.
		filter = "blackdetect=d=0.5:pix_th=0.10,freezedetect=n=-60dB:d=2,fps=1/5," + filter
	}
	args = append(args, "-map", "0:v:0", "-vf", filter, "-an", "-sn", "-dn")
	if row.IsImage {
		args = append(args, "-frames:v", "1")
	}
	return append(args, "-f", "null", "-")
}

func audioAnalysisArgs(sourceURL string, opts analysisOptions) []string {
	args := commonRangeArgs(sourceURL, opts)
	filter := fmt.Sprintf(
		"ebur128=peak=true:framelog=verbose,astats=metadata=0:reset=0:measure_perchannel=none:measure_overall=all,silencedetect=noise=%sdB:d=%s",
		strconv.FormatFloat(opts.SilenceThresholdDB, 'f', -1, 64),
		strconv.FormatFloat(float64(opts.SilenceMinMs)/1000, 'f', 3, 64),
	)
	return append(args, "-map", "0:a:0", "-af", filter, "-vn", "-sn", "-dn", "-f", "null", "-")
}

func formatSeconds(ms int64) string {
	return strconv.FormatFloat(float64(ms)/1000, 'f', 3, 64)
}

func commandFailureIssue(code string, err error, _ string) analysisIssue {
	// Do not echo ffmpeg's input banner: it can contain a signed URL.
	// The exit/timeout reason is safe and enough for the caller.
	return analysisIssue{Code: code, Severity: "error", Message: err.Error()}
}

var (
	metadataNumberRE = regexp.MustCompile(`(?m)^.*?(lavfi\.(?:signalstats\.[A-Z]+|blur|block))=(-?(?:\d+(?:\.\d+)?|\.\d+))\s*$`)
	blackRE          = regexp.MustCompile(`black_start:([0-9.]+)\s+black_end:([0-9.]+)\s+black_duration:([0-9.]+)`)
	freezeStartRE    = regexp.MustCompile(`lavfi\.freezedetect\.freeze_start:\s*([0-9.]+)`)
	freezeEndRE      = regexp.MustCompile(`lavfi\.freezedetect\.freeze_end:\s*([0-9.]+)`)
	freezeDurationRE = regexp.MustCompile(`lavfi\.freezedetect\.freeze_duration:\s*([0-9.]+)`)
)

func parseVisualAnalysis(log string, offsetMs, rangeEndMs int64) *visualAnalysis {
	values := map[string][]float64{}
	for _, m := range metadataNumberRE.FindAllStringSubmatch(log, -1) {
		v, err := strconv.ParseFloat(m[2], 64)
		if err == nil && !math.IsNaN(v) && !math.IsInf(v, 0) {
			values[m[1]] = append(values[m[1]], v)
		}
	}
	v := &visualAnalysis{
		SampledFrames: len(values["lavfi.signalstats.YAVG"]),
		BlackSegments: make([]timedSegment, 0), FrozenSegments: make([]timedSegment, 0),
	}
	v.MeanLuma = meanPtr(values["lavfi.signalstats.YAVG"])
	v.MinLuma = minPtr(values["lavfi.signalstats.YMIN"])
	v.MaxLuma = maxPtr(values["lavfi.signalstats.YMAX"])
	v.MeanSaturation = meanPtr(values["lavfi.signalstats.SATAVG"])
	v.MeanBlurScore = meanPtr(values["lavfi.blur"])
	v.MeanBlockinessScore = meanPtr(values["lavfi.block"])
	for _, m := range blackRE.FindAllStringSubmatch(log, -1) {
		v.BlackSegments = append(v.BlackSegments, secondsSegment(m[1], m[2], m[3], offsetMs))
	}
	v.FrozenSegments = parseFreezeSegments(log, offsetMs, rangeEndMs)
	return v
}

func parseFreezeSegments(log string, offsetMs, rangeEndMs int64) []timedSegment {
	starts := freezeStartRE.FindAllStringSubmatch(log, -1)
	ends := freezeEndRE.FindAllStringSubmatch(log, -1)
	durations := freezeDurationRE.FindAllStringSubmatch(log, -1)
	n := len(starts)
	if len(ends) < n {
		n = len(ends)
	}
	out := make([]timedSegment, 0, n)
	for i := 0; i < n; i++ {
		start := secondsToMs(starts[i][1]) + offsetMs
		end := secondsToMs(ends[i][1]) + offsetMs
		duration := end - start
		if i < len(durations) {
			duration = secondsToMs(durations[i][1])
		}
		out = append(out, timedSegment{StartMs: start, EndMs: end, DurationMs: duration})
	}
	// freezedetect reports freeze_start immediately but has no later
	// changed frame from which to emit freeze_end when the source ends
	// frozen. Close those terminal segments at the analyzed range end.
	for i := n; i < len(starts); i++ {
		start := secondsToMs(starts[i][1]) + offsetMs
		if rangeEndMs > start {
			out = append(out, timedSegment{StartMs: start, EndMs: rangeEndMs, DurationMs: rangeEndMs - start})
		}
	}
	return out
}

func secondsSegment(start, end, duration string, offsetMs int64) timedSegment {
	return timedSegment{
		StartMs:    secondsToMs(start) + offsetMs,
		EndMs:      secondsToMs(end) + offsetMs,
		DurationMs: secondsToMs(duration),
	}
}

func secondsToMs(s string) int64 {
	f, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return int64(math.Round(f * 1000))
}

func meanPtr(v []float64) *float64 {
	if len(v) == 0 {
		return nil
	}
	var sum float64
	for _, n := range v {
		sum += n
	}
	x := sum / float64(len(v))
	return &x
}

func minPtr(v []float64) *float64 {
	if len(v) == 0 {
		return nil
	}
	x := v[0]
	for _, n := range v[1:] {
		if n < x {
			x = n
		}
	}
	return &x
}

func maxPtr(v []float64) *float64 {
	if len(v) == 0 {
		return nil
	}
	x := v[0]
	for _, n := range v[1:] {
		if n > x {
			x = n
		}
	}
	return &x
}

func visualIssues(v *visualAnalysis) []analysisIssue {
	if v == nil {
		return nil
	}
	out := make([]analysisIssue, 0, len(v.BlackSegments)+len(v.FrozenSegments))
	for _, s := range v.BlackSegments {
		out = append(out, analysisIssue{Code: "BLACK_SEGMENT", Severity: "warning", Message: "Video contains a black or nearly black segment.", StartMs: s.StartMs, EndMs: s.EndMs})
	}
	for _, s := range v.FrozenSegments {
		out = append(out, analysisIssue{Code: "FROZEN_SEGMENT", Severity: "warning", Message: "Video contains a frozen section lasting at least two seconds.", StartMs: s.StartMs, EndMs: s.EndMs})
	}
	return out
}

var (
	integratedLUFSRE = regexp.MustCompile(`(?m)^\s*I:\s*(-?(?:\d+(?:\.\d+)?|inf))\s+LUFS\s*$`)
	loudnessRangeRE  = regexp.MustCompile(`(?m)^\s*LRA:\s*(-?(?:\d+(?:\.\d+)?|inf))\s+LU\s*$`)
	truePeakRE       = regexp.MustCompile(`(?m)^\s*Peak:\s*(-?(?:\d+(?:\.\d+)?|inf))\s+dBFS\s*$`)
	samplePeakRE     = regexp.MustCompile(`Peak level dB:\s*(-?(?:\d+(?:\.\d+)?|inf))`)
	rmsRE            = regexp.MustCompile(`RMS level dB:\s*(-?(?:\d+(?:\.\d+)?|inf))`)
	dynamicRangeRE   = regexp.MustCompile(`Dynamic range:\s*(-?(?:\d+(?:\.\d+)?|inf))`)
	dcOffsetRE       = regexp.MustCompile(`DC offset:\s*(-?(?:\d+(?:\.\d+)?|inf))`)
	silenceStartRE   = regexp.MustCompile(`silence_start:\s*([0-9.]+)`)
	silenceEndRE     = regexp.MustCompile(`silence_end:\s*([0-9.]+)\s*\|\s*silence_duration:\s*([0-9.]+)`)
)

func lastMetric(re *regexp.Regexp, log string) *float64 {
	matches := re.FindAllStringSubmatch(log, -1)
	if len(matches) == 0 {
		return nil
	}
	s := matches[len(matches)-1][1]
	if strings.Contains(strings.ToLower(s), "inf") {
		return nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil
	}
	return &v
}

func parseAudioAnalysis(log string, opts analysisOptions) *audioAnalysis {
	a := &audioAnalysis{
		IntegratedLUFS:      lastMetric(integratedLUFSRE, log),
		LoudnessRangeLU:     lastMetric(loudnessRangeRE, log),
		MaxTruePeakDBTP:     lastMetric(truePeakRE, log),
		MaxSamplePeakDBFS:   lastMetric(samplePeakRE, log),
		RMSDBFS:             lastMetric(rmsRE, log),
		DynamicRangeDB:      lastMetric(dynamicRangeRE, log),
		DCOffset:            lastMetric(dcOffsetRE, log),
		ClippingThresholdDB: clippingRiskDB,
		SilenceThresholdDB:  opts.SilenceThresholdDB,
		SilenceMinMs:        opts.SilenceMinMs,
		SilenceSegments:     make([]timedSegment, 0),
	}
	starts := silenceStartRE.FindAllStringSubmatch(log, -1)
	ends := silenceEndRE.FindAllStringSubmatch(log, -1)
	n := len(starts)
	if len(ends) < n {
		n = len(ends)
	}
	for i := 0; i < n; i++ {
		start := secondsToMs(starts[i][1]) + opts.StartMs
		end := secondsToMs(ends[i][1]) + opts.StartMs
		dur := secondsToMs(ends[i][2])
		s := timedSegment{StartMs: start, EndMs: end, DurationMs: dur}
		a.SilenceSegments = append(a.SilenceSegments, s)
		a.SilenceTotalMs += dur
		if dur > a.LongestSilenceMs {
			a.LongestSilenceMs = dur
		}
	}
	duration := opts.EndMs - opts.StartMs
	if duration > 0 {
		a.SilenceRatio = math.Min(1, float64(a.SilenceTotalMs)/float64(duration))
	}
	if a.MaxSamplePeakDBFS != nil && *a.MaxSamplePeakDBFS >= clippingRiskDB {
		a.ClippingDetected = true
	}
	if a.MaxTruePeakDBTP != nil && *a.MaxTruePeakDBTP >= clippingRiskDB {
		a.ClippingDetected = true
	}
	return a
}

func audioIssues(a *audioAnalysis) []analysisIssue {
	if a == nil {
		return nil
	}
	out := make([]analysisIssue, 0)
	if a.ClippingDetected {
		peak := 0.0
		unit := "dBFS"
		if a.MaxTruePeakDBTP != nil {
			peak, unit = *a.MaxTruePeakDBTP, "dBTP"
		} else if a.MaxSamplePeakDBFS != nil {
			peak = *a.MaxSamplePeakDBFS
		}
		out = append(out, analysisIssue{Code: "CLIPPING_RISK", Severity: "warning", Message: "Audio peak reaches the configured clipping-risk threshold.", Value: peak, Unit: unit})
	}
	for _, s := range a.SilenceSegments {
		out = append(out, analysisIssue{Code: "SILENCE_SEGMENT", Severity: "info", Message: "Audio contains a detected silence segment.", StartMs: s.StartMs, EndMs: s.EndMs})
	}
	return out
}
