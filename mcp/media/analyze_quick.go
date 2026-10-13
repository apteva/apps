package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"runtime"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const analysisRecipe = "media-quick-checks-1"
const quickWindowMs int64 = 2000

type analysisWindow struct {
	StartMs int64 `json:"start_ms"`
	EndMs   int64 `json:"end_ms"`
}
type analysisSample struct {
	analysisWindow
	DecodeOK  bool            `json:"decode_ok"`
	Visual    *visualAnalysis `json:"visual,omitempty"`
	Audio     *audioAnalysis  `json:"audio,omitempty"`
	ElapsedMs int64           `json:"elapsed_ms"`
}
type analysisDiagnostics struct {
	Algorithm            string `json:"algorithm_version"`
	ElapsedMs            int64  `json:"elapsed_ms"`
	AdmissionMs          int64  `json:"admission_ms"`
	DecodeThreads        int    `json:"decoder_threads"`
	FilterThreads        int    `json:"filter_threads"`
	ContinuityWidth      int    `json:"continuity_width,omitempty"`
	InputPasses          int    `json:"input_passes"`
	SourceCacheHit       bool   `json:"source_cache_hit"`
	SourceAccess         string `json:"source_access"`
	FullSourceDownloaded bool   `json:"full_source_downloaded"`
	TimeoutSeconds       int    `json:"timeout_seconds"`
}

func quickWindows(opts analysisOptions, row *MediaRow) []analysisWindow {
	if row.IsImage {
		return []analysisWindow{{0, 0}}
	}
	start, end := opts.StartMs, opts.EndMs
	if end <= start {
		end = start + quickWindowMs
	}
	if end-start <= 3*quickWindowMs {
		return []analysisWindow{{start, end}}
	}
	mid := (start + end) / 2
	return []analysisWindow{{start, start + quickWindowMs}, {mid - quickWindowMs/2, mid + quickWindowMs/2}, {end - quickWindowMs, end}}
}

func analysisParent(opts analysisOptions) context.Context {
	if opts.Parent != nil {
		return opts.Parent
	}
	return context.Background()
}

func analysisFFmpegArgs(source string, row *MediaRow, opts analysisOptions) []string {
	args := commonRangeArgs(source, opts)
	if opts.StartMs > 0 && (row.HasVideo && !row.IsImage) {
		args = append([]string{"-noaccurate_seek"}, args...)
	}
	graph := []string{}
	maps := []string{}
	if row.HasVideo || row.IsImage {
		v := visualAnalysisArgs(source, row, opts)
		for i := 0; i+1 < len(v); i++ {
			if v[i] == "-vf" {
				graph = append(graph, "[0:v:0]"+v[i+1]+"[v]")
				break
			}
		}
		maps = append(maps, "-map", "[v]", "-fps_mode:v", "passthrough")
	}
	if row.HasAudio {
		a := audioAnalysisArgs(source, opts)
		for i := 0; i+1 < len(a); i++ {
			if a[i] == "-af" {
				filter := a[i+1]
				if opts.StartMs > 0 {
					filter = "atrim=start=0," + filter
				}
				graph = append(graph, "[0:a:0]"+filter+"[a]")
				break
			}
		}
		maps = append(maps, "-map", "[a]")
	}
	args = append(args, "-filter_complex", strings.Join(graph, ";"))
	args = append(args, maps...)
	if row.IsImage {
		args = append(args, "-frames:v", "1")
	}
	return append(args, "-sn", "-dn", "-f", "null", "-")
}

func analysisBoundedArgs(args []string, opts analysisOptions) []string {
	dec := opts.DecoderThreads
	if dec <= 0 {
		dec = 1
	}
	dec = min(dec, 4, runtime.NumCPU())
	out := []string{"-filter_threads", "1", "-filter_complex_threads", "1"}
	for _, a := range args {
		if a == "-i" {
			out = append(out, "-threads", fmt.Sprint(dec))
		}
		out = append(out, a)
	}
	return out
}

func qualityFromLog(log string, exitErr error, row *MediaRow, opts analysisOptions) qualityAnalysis {
	q := qualityAnalysis{DecodeOK: exitErr == nil, Issues: []analysisIssue{}}
	if row.HasVideo || row.IsImage {
		q.Visual = parseVisualAnalysis(log, opts.StartMs, opts.EndMs)
		if opts.Depth == "quick" && q.Visual.FrameValidation != nil {
			q.Visual.FrameValidation.BlackDetection = "every_decoded_frame_in_sample_window_at_640px"
		}
		q.Issues = append(q.Issues, visualIssues(q.Visual)...)
		if !row.IsImage && (q.Visual.FrameValidation == nil || q.Visual.FrameValidation.FramesChecked == 0) {
			q.DecodeOK = false
			q.Issues = append(q.Issues, analysisIssue{Code: "NO_VIDEO_PICTURES", Severity: "error", Message: "No video pictures were decoded in the selected window.", StartMs: opts.StartMs, EndMs: opts.EndMs})
		}
	}
	if row.HasAudio {
		q.Audio = parseAudioAnalysis(log, opts)
		if opts.Depth == "quick" {
			q.Audio.MeasurementScope = "sample_window"
		}
		q.Issues = append(q.Issues, audioIssues(q.Audio)...)
	}
	if exitErr != nil {
		q.Issues = append(q.Issues, commandFailureIssue("ANALYSIS_DECODE_FAILED", exitErr, log))
	}
	return q
}

func mergeQuickQuality(results []qualityAnalysis, windows []analysisWindow, diagnostics analysisDiagnostics) qualityAnalysis {
	q := qualityAnalysis{DecodeOK: true, Issues: []analysisIssue{}, Diagnostics: diagnostics}
	var luma, minLuma, maxLuma, sat, blur, block []float64
	audio := &audioAnalysis{MeasurementScope: "sampled_window_peaks_and_silence", ClippingThresholdDB: clippingRiskDB, SilenceThresholdDB: defaultSilenceDB, SilenceMinMs: defaultSilenceMinMs, SilenceSegments: []timedSegment{}}
	visual := &visualAnalysis{BlackSegments: []timedSegment{}, FrozenSegments: []timedSegment{}}
	haveV, haveA := false, false
	duration := int64(0)
	take := func(xs *[]float64, v *float64) {
		if v != nil {
			*xs = append(*xs, *v)
		}
	}
	maximum := func(dst **float64, src *float64) {
		if src != nil && (*dst == nil || **dst < *src) {
			x := *src
			*dst = &x
		}
	}
	for i, r := range results {
		q.DecodeOK = q.DecodeOK && r.DecodeOK
		q.Issues = append(q.Issues, r.Issues...)
		q.Samples = append(q.Samples, analysisSample{windows[i], r.DecodeOK, r.Visual, r.Audio, r.Diagnostics.ElapsedMs})
		duration += windows[i].EndMs - windows[i].StartMs
		if v := r.Visual; v != nil {
			haveV = true
			visual.SampledFrames += v.SampledFrames
			visual.BlackSegments = append(visual.BlackSegments, v.BlackSegments...)
			visual.FrozenSegments = append(visual.FrozenSegments, v.FrozenSegments...)
			take(&luma, v.MeanLuma)
			take(&minLuma, v.MinLuma)
			take(&maxLuma, v.MaxLuma)
			take(&sat, v.MeanSaturation)
			take(&blur, v.MeanBlurScore)
			take(&block, v.MeanBlockinessScore)
		}
		if a := r.Audio; a != nil {
			haveA = true
			maximum(&audio.MaxTruePeakDBTP, a.MaxTruePeakDBTP)
			maximum(&audio.MaxSamplePeakDBFS, a.MaxSamplePeakDBFS)
			audio.ClippingDetected = audio.ClippingDetected || a.ClippingDetected
			audio.SilenceTotalMs += a.SilenceTotalMs
			audio.LongestSilenceMs = max(audio.LongestSilenceMs, a.LongestSilenceMs)
			audio.SilenceSegments = append(audio.SilenceSegments, a.SilenceSegments...)
			audio.SilenceMinMs = a.SilenceMinMs
			audio.SilenceThresholdDB = a.SilenceThresholdDB
		}
	}
	if haveV {
		visual.MeanLuma = meanPtr(luma)
		visual.MinLuma = minPtr(minLuma)
		visual.MaxLuma = maxPtr(maxLuma)
		visual.MeanSaturation = meanPtr(sat)
		visual.MeanBlurScore = meanPtr(blur)
		visual.MeanBlockinessScore = meanPtr(block)
		q.Visual = visual
	}
	if haveA {
		if duration > 0 {
			audio.SilenceRatio = math.Min(1, float64(audio.SilenceTotalMs)/float64(duration))
		}
		q.Audio = audio
	}
	return q
}

func analysisCacheKey(project string, row *MediaRow, opts analysisOptions, source *StorageFile) string {
	if len(source.SHA256) != 64 {
		return ""
	}
	b, _ := json.Marshal([]any{analysisRecipe, project, source.ID, source.SHA256, source.SizeBytes, opts.Depth, opts.StartMs, opts.EndMs, opts.SilenceThresholdDB, opts.SilenceMinMs, quickWindowMs, 640})
	return fmt.Sprintf("%x", sha256.Sum256(b))
}
func loadAnalysisCache(db *sql.DB, project, key string) (mediaAnalysisResult, bool) {
	var raw []byte
	var r mediaAnalysisResult
	if key == "" || db.QueryRow(`SELECT result_json FROM analysis_cache WHERE project_id=? AND cache_key=?`, project, key).Scan(&raw) != nil || json.Unmarshal(raw, &r) != nil {
		return r, false
	}
	return r, true
}
func saveAnalysisCache(db *sql.DB, project, key string, row *MediaRow, result mediaAnalysisResult) {
	if key == "" {
		return
	}
	raw, _ := json.Marshal(result)
	_, _ = db.Exec(`INSERT INTO analysis_cache(cache_key,project_id,file_id,source_sha256,algorithm_version,result_json) VALUES(?,?,?,?,?,?) ON CONFLICT(cache_key) DO NOTHING`, key, project, row.FileID, row.SourceSHA256, analysisRecipe, string(raw))
	_, _ = db.Exec(`DELETE FROM analysis_cache WHERE created_at<datetime('now','-30 days')`)
}

func analysisToolReceipt(app *sdk.AppCtx, project string, r mediaAnalysisResult, hit bool) (any, error) {
	out := map[string]any{"found": true, "result": r, "result_cache_hit": hit}
	if renderWireBytes(out) <= maxRenderStatusBytes-256 {
		return out, nil
	}
	raw, _ := json.Marshal(r)
	ref, err := storeCropEvidence(app.AppDB(), project, raw)
	if err != nil {
		return nil, fmt.Errorf("analysis_result_too_large: full evidence unavailable")
	}
	// Complete measurements stay behind the immutable reference; a large result
	// must not spill into the caller's model context.
	return map[string]any{"found": true, "file_id": r.FileID, "analysis": r.Analysis, "result_cache_hit": hit, "result_ref": ref, "download_required": true, "limitation": "Complete analysis measurements exceed the bounded receipt; download the saved evidence."}, nil
}

func beginAnalysis(app *sdk.AppCtx, opts analysisOptions) (context.Context, context.CancelFunc, func(), time.Duration, error) {
	parent, stop := mediaContext(analysisParent(opts), app)
	wait := 30 * time.Second
	if opts.Depth == "quick" {
		wait = 10 * time.Second
	}
	admission, cancel := context.WithTimeout(parent, wait)
	start := time.Now()
	leased, release, err := acquireMediaWorkWithLeaseContext(admission, parent, app, 2)
	cancel()
	if err != nil {
		stop()
		return nil, func() {}, func() {}, time.Since(start), fmt.Errorf("analysis_admission_timeout: %w", err)
	}
	duration := opts.Timeout
	if duration <= 0 {
		duration = 300 * time.Second
	}
	ctx, timeoutCancel := context.WithTimeout(leased, duration)
	return ctx, func() { timeoutCancel(); stop() }, release, time.Since(start), nil
}

// Core already honors this schema extension. It covers the longest explicit
// synchronous path (source lookup, admission and 300s execution), with its
// own dispatch overhead added by Core. The quick path has a much shorter cap.
func analysisToolSchema(properties map[string]any, required []string) map[string]any {
	s := schemaObject(properties, required)
	s["x-apteva-timeout-ms"] = 340000
	return s
}
