package main

// Per-operation ffmpeg argv builders. Each op turns the JSON params
// into:
//   - the ffmpeg command-line arguments
//   - the output filename (basename + extension)
//   - the output content-type for the storage upload
//
// Keep the builders small and side-effect-free: they read params,
// produce strings. The render pool calls them, runs ffmpeg, then
// uploads the produced file.
//
// Idempotent on inputs — calling buildArgs twice with the same
// params returns the same argv. Concurrency safety follows for free.

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

// opPlan is the contract between the dispatch table below and the
// render pool. Outpath is filled in by the caller (it's the scratch
// dir + Filename); the builder only owns Filename + Args.
type opPlan struct {
	Filename    string   // basename, e.g. "trim-12.mp4"
	ContentType string   // e.g. "video/mp4"
	Args        []string // ffmpeg args excluding the binary name and the final output path
}

// buildPlan dispatches to the per-op builder. Returns ErrNotImplemented
// for ops scaffolded but not yet wired (resize/concat/etc. as of v0.2).
//
// sourceExt is the first source file's extension (e.g. ".jpg"), or ""
// when unknown. The shape-preserving ops (resize, crop) use it to keep
// the output the same media type as the input — without it an image
// source would silently default to a ".mp4" output. Other ops own their
// extension (transcode via `format`, audio_extract via `format`,
// extract_frame/reel are always image/mp4) and ignore the hint.
func buildPlan(op string, sources []string, params json.RawMessage, outputName, sourceExt string) (*opPlan, error) {
	if err := validateOutputName(outputName); err != nil {
		return nil, err
	}
	switch op {
	case "trim":
		return planTrim(sources, params, outputName)
	case "resize":
		return planResize(sources, params, outputName, sourceExt)
	case "transcode":
		return planTranscode(sources, params, outputName)
	case "concat":
		return planConcat(sources, params, outputName)
	case "crop":
		return planCrop(sources, params, outputName, sourceExt)
	case "extract_frame":
		return planExtractFrame(sources, params, outputName)
	case "extract_reel":
		return planExtractReel(sources, params, outputName)
	case "audio_extract":
		return planAudioExtract(sources, params, outputName)
	case "audio_filter":
		return planAudioFilter(sources, params, outputName, sourceExt)
	default:
		return nil, fmt.Errorf("unknown operation: %s", op)
	}
}

func validateOutputName(name string) error {
	if name == "" {
		return nil
	}
	if name == "." || name == ".." || strings.ContainsRune(name, '\x00') ||
		strings.ContainsAny(name, `/\\`) || filepath.IsAbs(name) || filepath.Base(name) != name {
		return errors.New("output_name must be a filename, not a path")
	}
	return nil
}

// ErrNotImplemented marks ops that ship in v0.2's manifest but whose
// argv builders are scaffolded for v0.3+. The pool catches it and
// fails the render with a clear message.
var ErrNotImplemented = errors.New("operation not implemented in this media version")

// ─── trim ───────────────────────────────────────────────────────────
//
// `-ss <start> -to <end> -i <input> -c copy` does a stream copy when
// possible (no re-encode → fast + lossless). For mid-frame cuts on
// formats that don't tolerate that we'd fall back to re-encode; v0.2
// keeps it simple — copy mode only, callers must align to keyframes
// for accurate cuts.

type trimParams struct {
	StartMs int64 `json:"start_ms"`
	EndMs   int64 `json:"end_ms"`
}

func planTrim(sources []string, raw json.RawMessage, outputName string) (*opPlan, error) {
	if len(sources) != 1 {
		return nil, errors.New("trim takes exactly one source file_id")
	}
	var p trimParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("trim params: %w", err)
	}
	if p.EndMs <= p.StartMs {
		return nil, errors.New("trim: end_ms must be > start_ms")
	}
	if p.StartMs < 0 {
		return nil, errors.New("trim: start_ms must be >= 0")
	}

	// Place -ss BEFORE -i so ffmpeg seeks via the demuxer (fast). We
	// pass start/end as fractional seconds — ffmpeg accepts this
	// portably; some old versions choke on hh:mm:ss.fff.
	args := []string{
		"-y",
		"-loglevel", "error",
		"-progress", "pipe:1",
		"-ss", msToSeconds(p.StartMs),
		"-to", msToSeconds(p.EndMs),
		"-i", "{input}",
		"-c", "copy",
		"-avoid_negative_ts", "make_zero",
	}
	name, ct := defaultOutputName(outputName, sources[0], "trim", "")
	return &opPlan{Filename: name, ContentType: ct, Args: args}, nil
}

// ─── resize ─────────────────────────────────────────────────────────
//
// scale=W:H. When keep_aspect=true, height becomes -2 (auto, even
// number) so we preserve aspect ratio without callers doing math.

type resizeParams struct {
	Width      int  `json:"width"`
	Height     int  `json:"height"`
	KeepAspect bool `json:"keep_aspect"`
}

func planResize(sources []string, raw json.RawMessage, outputName, sourceExt string) (*opPlan, error) {
	if len(sources) != 1 {
		return nil, errors.New("resize takes exactly one source file_id")
	}
	var p resizeParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("resize params: %w", err)
	}
	if p.Width <= 0 {
		return nil, errors.New("resize: width must be > 0")
	}
	if !p.KeepAspect && p.Height <= 0 {
		return nil, errors.New("resize: height must be > 0 unless keep_aspect=true")
	}
	height := fmt.Sprint(p.Height)
	if p.KeepAspect {
		height = "-2"
	}
	scale := fmt.Sprintf("scale=%d:%s", p.Width, height)
	name, ct := defaultOutputName(outputName, sources[0], "resize", sourceExt)
	args := imageAwareVideoFilterArgs(scale, name)
	return &opPlan{Filename: name, ContentType: ct, Args: args}, nil
}

// ─── transcode ──────────────────────────────────────────────────────
//
// Format change with optional codec/bitrate overrides. Format drives
// the output extension; codecs are passed as -c:v / -c:a when set.

type transcodeParams struct {
	Format     string `json:"format"`                // mp4|mkv|webm|mov|m4a|mp3|wav|opus
	VideoCodec string `json:"video_codec,omitempty"` // libx264|libx265|libvpx-vp9|...
	AudioCodec string `json:"audio_codec,omitempty"` // aac|libmp3lame|libopus|...
	Bitrate    string `json:"bitrate,omitempty"`     // e.g. "2M", "192k"
}

func planTranscode(sources []string, raw json.RawMessage, outputName string) (*opPlan, error) {
	if len(sources) != 1 {
		return nil, errors.New("transcode takes exactly one source file_id")
	}
	var p transcodeParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("transcode params: %w", err)
	}
	if p.Format == "" {
		return nil, errors.New("transcode: format required (e.g. mp4, webm, mp3)")
	}
	args := []string{
		"-y",
		"-loglevel", "error",
		"-progress", "pipe:1",
		"-i", "{input}",
	}
	if p.VideoCodec != "" {
		args = append(args, "-c:v", p.VideoCodec)
	}
	if p.AudioCodec != "" {
		args = append(args, "-c:a", p.AudioCodec)
	}
	if p.Bitrate != "" {
		args = append(args, "-b:v", p.Bitrate)
	}
	name, ct := defaultOutputName(outputName, sources[0], "transcode", "."+strings.ToLower(p.Format))
	return &opPlan{Filename: name, ContentType: ct, Args: args}, nil
}

// ─── concat ─────────────────────────────────────────────────────────
//
// Concat demuxer: writes a temporary list-file, ffmpeg reads it.
// All inputs must share container + codec for stream-copy concat.
// The {input} placeholder here is special: the pool writes the
// list-file and substitutes its path.

type concatParams struct {
	// no extra params; sources carry the inputs
}

func planConcat(sources []string, _ json.RawMessage, outputName string) (*opPlan, error) {
	if len(sources) < 2 {
		return nil, errors.New("concat takes 2+ source file_ids")
	}
	if outputName == "" {
		return nil, errors.New("concat: output_name required")
	}
	args := []string{
		"-y",
		"-loglevel", "error",
		"-progress", "pipe:1",
		"-f", "concat",
		"-safe", "0",
		"-i", "{concat_list}",
		"-c", "copy",
	}
	ext := path.Ext(outputName)
	if ext == "" {
		ext = ".mp4"
		outputName = outputName + ext
	}
	name, ct := defaultOutputName(outputName, sources[0], "concat", "")
	return &opPlan{Filename: name, ContentType: ct, Args: args}, nil
}

// ─── crop ───────────────────────────────────────────────────────────

type cropParams struct {
	X           int    `json:"x"`
	Y           int    `json:"y"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
	TargetRatio string `json:"target_ratio,omitempty"` // "1:1", "9:16", "4:5", … — if set, output is cropped/reframed to that ratio
	OutputWidth int    `json:"output_width,omitempty"` // optional scale width after target_ratio crop; omitted preserves crop size
	CropMode    string `json:"crop_mode,omitempty"`    // "smart" (default) | "center"; actual coords supplied by preprocessSmartCrop
	FitMode     string `json:"fit_mode,omitempty"`     // "crop" (default) | "contain"; contain preserves the complete source frame

	// crop_w/h/x/y are injected by preprocessSmartCrop at execute
	// time. Agents and the UI shouldn't pass these directly.
	CropW int `json:"crop_w,omitempty"`
	CropH int `json:"crop_h,omitempty"`
	CropX int `json:"crop_x,omitempty"`
	CropY int `json:"crop_y,omitempty"`
}

func planCrop(sources []string, raw json.RawMessage, outputName, sourceExt string) (*opPlan, error) {
	if len(sources) != 1 {
		return nil, errors.New("crop takes exactly one source file_id")
	}
	var p cropParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("crop params: %w", err)
	}
	if p.X < 0 || p.Y < 0 {
		return nil, errors.New("crop: x and y must be >= 0")
	}
	var vf string
	if strings.TrimSpace(p.TargetRatio) != "" {
		rw, rh, err := parseAspectRatio(p.TargetRatio)
		if err != nil {
			return nil, fmt.Errorf("crop: %w", err)
		}
		fitMode, err := normalizeFitMode(p.FitMode)
		if err != nil {
			return nil, fmt.Errorf("crop: %w", err)
		}
		if fitMode == "contain" {
			outputWidth, outputHeight := ratioOutputDimensions(p.OutputWidth, rw, rh)
			vf = containFilter(outputWidth, outputHeight)
		} else {
			if p.CropW > 0 && p.CropH > 0 {
				vf = explicitCropFilter(p.CropW, p.CropH, p.CropX, p.CropY)
			} else if p.Width > 0 && p.Height > 0 {
				vf = explicitCropFilter(p.Width, p.Height, p.X, p.Y)
			} else {
				vf = symbolicCenterCropFilter(rw, rh)
			}
			if p.OutputWidth > 0 {
				outputWidth, outputHeight := ratioOutputDimensions(p.OutputWidth, rw, rh)
				vf += "," + fmt.Sprintf("scale=%d:%d,setsar=1", outputWidth, outputHeight)
			}
		}
	} else {
		if strings.TrimSpace(p.FitMode) != "" && !strings.EqualFold(strings.TrimSpace(p.FitMode), "crop") {
			return nil, errors.New("crop: fit_mode requires target_ratio")
		}
		if p.Width <= 0 || p.Height <= 0 {
			return nil, errors.New("crop: width and height must be > 0 unless target_ratio is set")
		}
		vf = explicitCropFilter(p.Width, p.Height, p.X, p.Y)
	}
	name, ct := defaultOutputName(outputName, sources[0], "crop", sourceExt)
	args := imageAwareVideoFilterArgs(vf, name)
	return &opPlan{Filename: name, ContentType: ct, Args: args}, nil
}

// ─── extract_frame ──────────────────────────────────────────────────
//
// Single PNG at an arbitrary timestamp. Distinct from the canonical
// thumbnail derivation — agents call this when they want a specific
// frame at a specific time, possibly multiple per source.

type extractFrameParams struct {
	AtMs        int64  `json:"at_ms"`
	Width       int    `json:"width,omitempty"`
	TargetRatio string `json:"target_ratio,omitempty"` // "1:1", "9:16", "4:5", … — if set, output is cropped + scaled to that ratio
	OutputWidth int    `json:"output_width,omitempty"` // width when target_ratio is set; defaults to Width or 1080
	CropMode    string `json:"crop_mode,omitempty"`    // informational — actual crop_w/h/x/y is supplied by preprocessSmartCrop
	FitMode     string `json:"fit_mode,omitempty"`     // "crop" (default) | "contain"

	// crop_w/h/x/y are injected by preprocessSmartCrop at execute
	// time. When set, the filter chain uses explicit coords and
	// skips the symbolic iw/ih path. Agents and the UI shouldn't
	// pass these directly — set crop_mode instead.
	CropW int `json:"crop_w,omitempty"`
	CropH int `json:"crop_h,omitempty"`
	CropX int `json:"crop_x,omitempty"`
	CropY int `json:"crop_y,omitempty"`
}

func planExtractFrame(sources []string, raw json.RawMessage, outputName string) (*opPlan, error) {
	if len(sources) != 1 {
		return nil, errors.New("extract_frame takes exactly one source file_id")
	}
	var p extractFrameParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("extract_frame params: %w", err)
	}
	if p.AtMs < 0 {
		return nil, errors.New("extract_frame: at_ms must be >= 0")
	}
	args := []string{
		"-y",
		"-loglevel", "error",
		"-ss", msToSeconds(p.AtMs),
		"-i", "{input}",
		"-frames:v", "1",
	}
	// Filter chain: build crop + scale, depending on which combo of
	// params was supplied. Three modes:
	//   1. No target_ratio + no width    → unchanged frame
	//   2. No target_ratio + width       → scale only (back-compat)
	//   3. target_ratio (any crop_mode)  → crop then scale to OutputWidth
	//      The crop coords come from preprocessSmartCrop (smart/center
	//      mode resolved upstream into crop_w/h/x/y) when present;
	//      otherwise we fall back to a symbolic iw/ih center expression
	//      so the render still proceeds even if smartcrop bailed.
	if strings.TrimSpace(p.TargetRatio) != "" {
		rw, rh, err := parseAspectRatio(p.TargetRatio)
		if err != nil {
			return nil, fmt.Errorf("extract_frame: %w", err)
		}
		outW := p.OutputWidth
		if outW <= 0 {
			outW = p.Width
		}
		if outW <= 0 {
			outW = 1080
		}
		outputWidth, outputHeight := ratioOutputDimensions(outW, rw, rh)
		fitMode, err := normalizeFitMode(p.FitMode)
		if err != nil {
			return nil, fmt.Errorf("extract_frame: %w", err)
		}
		if fitMode == "contain" {
			args = append(args, "-vf", containFilter(outputWidth, outputHeight))
		} else {
			cropExpr := cropFilterForRatio(rw, rh, p.CropW, p.CropH, p.CropX, p.CropY)
			args = append(args, "-vf", cropExpr+","+fmt.Sprintf("scale=%d:%d,setsar=1", outputWidth, outputHeight))
		}
	} else if p.Width > 0 {
		if strings.TrimSpace(p.FitMode) != "" && !strings.EqualFold(strings.TrimSpace(p.FitMode), "crop") {
			return nil, errors.New("extract_frame: fit_mode requires target_ratio")
		}
		args = append(args, "-vf", fmt.Sprintf("scale=%d:-2", p.Width))
	} else if strings.TrimSpace(p.FitMode) != "" && !strings.EqualFold(strings.TrimSpace(p.FitMode), "crop") {
		return nil, errors.New("extract_frame: fit_mode requires target_ratio")
	}
	if outputName == "" {
		outputName = fmt.Sprintf("frame-%dms.png", p.AtMs)
	} else if path.Ext(outputName) == "" {
		outputName += ".png"
	}
	return &opPlan{Filename: outputName, ContentType: "image/png", Args: args}, nil
}

// ─── extract_reel ───────────────────────────────────────────────────
//
// One-pass trim + center-crop + scale to a target aspect ratio.
// Designed for the common "make a 9:16 reel from a 16:9 source"
// workflow without forcing the agent to chain media_trim →
// media_crop → media_resize (3 tool calls, 3 download/upload pairs,
// 3 re-encodes — vs one).
//
// Time fields use the same names + unit as media_trim (start_ms,
// end_ms, integer milliseconds). Aspect ratio is parsed at submit
// time but the actual crop math runs INSIDE ffmpeg via filter
// expression variables (iw, ih, out_w, out_h) — the planner never
// touches source dimensions, so this stays a pure function like
// every other planner here.
//
// Both source orientations are handled with one filter expression:
// when source is wider than target (16:9 → 9:16), the height is
// preserved and width crops to ih*9/16. When source is taller than
// target (9:16 → 16:9), width is preserved and height crops. The
// `gt(iw/ih, target_aspect)` test inside the expression picks the
// branch at render time.
//
// Audio: copied through. The trim uses a short demuxer-level preroll
// and then an output seek. That avoids the black leading frames some
// phone MOVs produce when a reframe starts exactly at a non-keyframe,
// while still avoiding a decode from the beginning of long sources.

const extractReelSeekPrerollMs int64 = 2000

type extractReelParams struct {
	StartMs     int64  `json:"start_ms"`
	EndMs       int64  `json:"end_ms"`
	TargetRatio string `json:"target_ratio"`        // "9:16" (default), "1:1", "4:5", "16:9", …
	OutputWidth int    `json:"output_width"`        // optional; default 1080
	CropMode    string `json:"crop_mode,omitempty"` // "smart" (default) | "center" — passed through for logging; the actual crop_w/h/x/y is injected by preprocessSmartCrop
	FitMode     string `json:"fit_mode,omitempty"`  // "crop" (default) | "contain"

	// crop_w/h/x/y are injected by preprocessSmartCrop at execute
	// time. When set, the filter chain uses explicit coords and
	// skips the symbolic iw/ih path. Agents and the UI shouldn't
	// pass these directly.
	CropW int `json:"crop_w,omitempty"`
	CropH int `json:"crop_h,omitempty"`
	CropX int `json:"crop_x,omitempty"`
	CropY int `json:"crop_y,omitempty"`
	// CropPath is injected by Smart Crop v2 for reels whose subject moves.
	// Times are source-timeline milliseconds; callers should not set it.
	CropPath []cropPathPoint `json:"crop_path,omitempty"`
}

func planExtractReel(sources []string, raw json.RawMessage, outputName string) (*opPlan, error) {
	if len(sources) != 1 {
		return nil, errors.New("extract_reel takes exactly one source file_id")
	}
	var p extractReelParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("extract_reel params: %w", err)
	}
	if p.EndMs <= p.StartMs {
		return nil, errors.New("extract_reel: end_ms must be > start_ms")
	}
	if p.StartMs < 0 {
		return nil, errors.New("extract_reel: start_ms must be >= 0")
	}
	if p.TargetRatio == "" {
		p.TargetRatio = "9:16"
	}
	if p.OutputWidth <= 0 {
		p.OutputWidth = 1080
	}
	rw, rh, err := parseAspectRatio(p.TargetRatio)
	if err != nil {
		return nil, fmt.Errorf("extract_reel: %w", err)
	}
	fitMode, err := normalizeFitMode(p.FitMode)
	if err != nil {
		return nil, fmt.Errorf("extract_reel: %w", err)
	}
	// Filter chain: explicit crop (when preprocessSmartCrop has
	// already resolved smart/center coords into crop_w/h/x/y on the
	// params) takes priority; otherwise fall back to the original
	// symbolic iw/ih center expression so renders still proceed when
	// the pre-pass bails (no probed dimensions, missing thumbnail,
	// older media row, smartcrop analyzer error, …).
	outputWidth, outputHeight := ratioOutputDimensions(p.OutputWidth, rw, rh)
	videoFilter := containFilter(outputWidth, outputHeight)
	if fitMode == "crop" {
		cropExpr := cropFilterForRatio(rw, rh, p.CropW, p.CropH, p.CropX, p.CropY)
		scaleExpr := fmt.Sprintf("scale=%d:%d,setsar=1", outputWidth, outputHeight)
		videoFilter = cropExpr + "," + scaleExpr
	}
	seekStartMs := p.StartMs - extractReelSeekPrerollMs
	if seekStartMs < 0 {
		seekStartMs = 0
	}
	outputSeekMs := p.StartMs - seekStartMs
	durationMs := p.EndMs - p.StartMs
	if fitMode == "crop" && p.CropW > 0 && p.CropH > 0 && len(p.CropPath) > 1 {
		// The input clock starts at seekStartMs because of the preroll. Shift
		// the source-timeline path to that clock before building x(t).
		cropExpr := cropFilterForPath(p.CropW, p.CropH, p.CropY, seekStartMs, p.CropPath)
		videoFilter = cropExpr + "," + fmt.Sprintf("scale=%d:%d,setsar=1", outputWidth, outputHeight)
	}
	args := []string{
		"-y",
		"-loglevel", "error",
		"-progress", "pipe:1",
		"-ss", msToSeconds(seekStartMs),
		"-i", "{input}",
		"-ss", msToSeconds(outputSeekMs),
		"-t", msToSeconds(durationMs),
		"-vf", videoFilter,
		"-c:a", "copy", // audio passthrough — no re-encode
		"-avoid_negative_ts", "make_zero",
	}
	name, ct := defaultOutputName(outputName, sources[0], "reel", ".mp4")
	return &opPlan{Filename: name, ContentType: ct, Args: args}, nil
}

func ratioOutputDimensions(width, ratioW, ratioH int) (int, int) {
	w := roundEven(width)
	if w <= 0 {
		w = 1080
	}
	h := roundEven(w * ratioH / ratioW)
	if h <= 0 {
		h = 2
	}
	return w, h
}

func normalizeFitMode(mode string) (string, error) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		return "crop", nil
	}
	switch mode {
	case "crop", "contain":
		return mode, nil
	default:
		return "", fmt.Errorf("fit_mode must be crop or contain, got %q", mode)
	}
}

// containFilter preserves every source pixel and letterboxes/pillarboxes only
// the unused canvas area. It is the explicit alternative for compositions or
// movement that cannot physically fit inside a narrow destructive crop.
func containFilter(width, height int) string {
	return fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:(ow-iw)/2:(oh-ih)/2:color=black,setsar=1",
		width, height, width, height)
}

// parseAspectRatio splits "9:16" / "1:1" / "16:9" into integer (w, h)
// pairs. Rejects values < 1 and non-integer tokens — keeps the filter
// expression algebra clean.
func parseAspectRatio(s string) (int, int, error) {
	parts := strings.Split(strings.TrimSpace(s), ":")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("target_ratio %q must be \"W:H\" (e.g. \"9:16\")", s)
	}
	w, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil || w < 1 {
		return 0, 0, fmt.Errorf("target_ratio %q: width must be a positive integer", s)
	}
	h, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil || h < 1 {
		return 0, 0, fmt.Errorf("target_ratio %q: height must be a positive integer", s)
	}
	return w, h, nil
}

func cropFilterForRatio(rw, rh, cropW, cropH, cropX, cropY int) string {
	if cropW > 0 && cropH > 0 {
		return explicitCropFilter(cropW, cropH, cropX, cropY)
	}
	return symbolicCenterCropFilter(rw, rh)
}

func explicitCropFilter(w, h, x, y int) string {
	return fmt.Sprintf("crop=%d:%d:%d:%d", w, h, x, y)
}

func symbolicCenterCropFilter(rw, rh int) string {
	return fmt.Sprintf(
		"crop=w='if(gt(iw/ih,%d/%d),ih*%d/%d,iw)':h='if(gt(iw/ih,%d/%d),ih,iw*%d/%d)':x='(iw-out_w)/2':y='(ih-out_h)/2'",
		rw, rh, rw, rh, rw, rh, rh, rw,
	)
}

// ─── audio_extract ──────────────────────────────────────────────────
//
// Pulls the audio track out of a video into a standalone file. -vn
// drops video; codec/format are chosen by the requested target.

type audioExtractParams struct {
	Format string `json:"format"` // mp3|wav|m4a|opus|flac
}

func planAudioExtract(sources []string, raw json.RawMessage, outputName string) (*opPlan, error) {
	if len(sources) != 1 {
		return nil, errors.New("audio_extract takes exactly one source file_id")
	}
	var p audioExtractParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("audio_extract params: %w", err)
	}
	codec, ext, ct, err := audioFormatToCodec(p.Format)
	if err != nil {
		return nil, err
	}
	args := []string{
		"-y",
		"-loglevel", "error",
		"-progress", "pipe:1",
		"-i", "{input}",
		"-vn",
		"-c:a", codec,
	}
	name, _ := defaultOutputName(outputName, sources[0], "audio", ext)
	return &opPlan{Filename: name, ContentType: ct, Args: args}, nil
}

// ─── audio_filter ──────────────────────────────────────────────────
//
// Applies audio-only filters to either audio files or videos with
// audio. Video outputs copy the video stream untouched and re-encode
// only the filtered audio stream.

type audioFilterParams struct {
	Mode             string  `json:"mode"`                          // normalize|speech_clean|volume|mute
	TargetLUFS       float64 `json:"target_lufs,omitempty"`         // default -16 for normalize/speech_clean
	GainDB           float64 `json:"gain_db,omitempty"`             // used by volume mode
	SourceSampleRate int     `json:"_source_sample_rate,omitempty"` // executor-injected; never agent-controlled
}

func planAudioFilter(sources []string, raw json.RawMessage, outputName, sourceExt string) (*opPlan, error) {
	if len(sources) != 1 {
		return nil, errors.New("audio_filter takes exactly one source file_id")
	}
	var p audioFilterParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("audio_filter params: %w", err)
	}
	name, ct, audioOnly := audioFilterOutput(outputName, sources[0], sourceExt)
	codec, err := audioFilterCodecForOutput(path.Ext(name), audioOnly)
	if err != nil {
		return nil, err
	}
	if p.SourceSampleRate == 0 {
		// Submit-time validation runs before the executor can consult the
		// indexed source row. 48 kHz is the safe media default; execution
		// replaces it with the actual source rate when one is known.
		p.SourceSampleRate = 48_000
	}
	if p.SourceSampleRate < 8_000 || p.SourceSampleRate > 192_000 {
		return nil, fmt.Errorf("audio_filter: invalid source sample rate %d", p.SourceSampleRate)
	}
	af, err := audioFilterChain(p, isLossyAudioCodec(codec))
	if err != nil {
		return nil, err
	}
	sampleRate := strconv.Itoa(p.SourceSampleRate)
	if audioOnly {
		args := []string{
			"-y",
			"-loglevel", "error",
			"-progress", "pipe:1",
			"-i", "{input}",
			"-vn",
			"-map", "0:a:0",
			"-af", af,
			"-c:a", codec,
			"-ar", sampleRate,
		}
		return &opPlan{Filename: name, ContentType: ct, Args: args}, nil
	} else {
		args := []string{
			"-y",
			"-loglevel", "error",
			"-progress", "pipe:1",
			"-i", "{input}",
			"-map", "0:v?",
			"-map", "0:a:0",
			"-c:v", "copy",
			"-af", af,
			"-c:a", codec,
			"-ar", sampleRate,
		}
		return &opPlan{Filename: name, ContentType: ct, Args: args}, nil
	}
}

func audioFilterChain(p audioFilterParams, lossyOutput bool) (string, error) {
	mode := strings.ToLower(strings.TrimSpace(p.Mode))
	if mode == "" {
		mode = "normalize"
	}
	target := p.TargetLUFS
	if target == 0 {
		target = -16
	}
	// loudnorm's dynamic mode internally upsamples to 192 kHz. Leaving
	// that rate at the encoder boundary made AAC/MOV outputs land at
	// 96 kHz and produced inter-sample overs above 0 dBTP despite TP=-1.5.
	// Explicitly return to the source rate before encoding. Lossy codecs
	// get 2 dB of encode headroom and a post-resample limiter (AAC on
	// real phone recordings produced up to +1.6 dB of post-encode true-
	// peak overshoot in regression testing);
	// lossless codecs can use the requested delivery ceiling directly.
	peakDB := "-1.5"
	peakLinear := "0.841395" // 10^(-1.5/20)
	if lossyOutput {
		peakDB = "-3.5"
		peakLinear = "0.668344" // 10^(-3.5/20)
	}
	rate := strconv.Itoa(p.SourceSampleRate)
	loudnorm := fmt.Sprintf("loudnorm=I=%s:TP=%s:LRA=11", ffmpegFloat(target), peakDB)
	safeNormalize := loudnorm + ",aresample=" + rate +
		",alimiter=limit=" + peakLinear + ":attack=5:release=50:level=false"
	switch mode {
	case "normalize":
		return safeNormalize, nil
	case "speech_clean":
		return "highpass=f=80,lowpass=f=8000,acompressor=threshold=-18dB:ratio=3:attack=5:release=100," + safeNormalize, nil
	case "volume":
		return fmt.Sprintf("volume=%sdB", ffmpegFloat(p.GainDB)), nil
	case "mute":
		return "volume=0", nil
	default:
		return "", fmt.Errorf("audio_filter: unsupported mode %q (normalize|speech_clean|volume|mute)", p.Mode)
	}
}

func isLossyAudioCodec(codec string) bool {
	switch codec {
	case "aac", "libmp3lame", "libopus":
		return true
	default:
		return false
	}
}

func audioFilterOutput(outputName, sourceFileID, sourceExt string) (name, contentType string, audioOnly bool) {
	ext := strings.ToLower(path.Ext(outputName))
	if ext == "" {
		ext = strings.ToLower(sourceExt)
	}
	if ext == "" {
		ext = ".mp4"
	}
	audioOnly = isAudioExt(ext)
	if strings.TrimSpace(outputName) != "" {
		if path.Ext(outputName) == "" {
			outputName += ext
		}
		return outputName, contentTypeForName(outputName), audioOnly
	}
	name, ct := defaultOutputName("", sourceFileID, "audio-filter", ext)
	return name, ct, audioOnly
}

func audioFilterCodecForOutput(ext string, audioOnly bool) (string, error) {
	ext = strings.ToLower(ext)
	if audioOnly {
		switch ext {
		case ".mp3":
			return "libmp3lame", nil
		case ".wav":
			return "pcm_s16le", nil
		case ".m4a", ".mp4":
			return "aac", nil
		case ".opus":
			return "libopus", nil
		case ".flac":
			return "flac", nil
		default:
			return "", fmt.Errorf("audio_filter: unsupported audio output extension %q (mp3|wav|m4a|opus|flac)", ext)
		}
	}
	if ext == ".webm" {
		return "libopus", nil
	}
	return "aac", nil
}

// ─── helpers ────────────────────────────────────────────────────────

func msToSeconds(ms int64) string {
	// ffmpeg accepts decimal seconds. Use 3-digit precision so we can
	// trim at millisecond granularity without floating-point drift.
	return fmt.Sprintf("%d.%03d", ms/1000, ms%1000)
}

func ffmpegFloat(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// defaultOutputName picks an output basename. Priority: explicit
// outputName from the caller, otherwise <op>-<sourceFileID><ext>
// where ext is forceExt if set, else the source's ext if known.
func defaultOutputName(explicit, sourceFileID, op, forceExt string) (string, string) {
	if explicit != "" {
		return explicit, contentTypeForName(explicit)
	}
	ext := forceExt
	if ext == "" {
		ext = ".mp4" // safe default for video; transcode/audio_extract override via forceExt
	}
	return fmt.Sprintf("%s-%s%s", op, sourceFileID, ext), contentTypeForName("x" + ext)
}

func contentTypeForName(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".mp4":
		return "video/mp4"
	case ".mov":
		return "video/quicktime"
	case ".mkv":
		return "video/x-matroska"
	case ".webm":
		return "video/webm"
	case ".m4a":
		return "audio/mp4"
	case ".mp3":
		return "audio/mpeg"
	case ".wav":
		return "audio/wav"
	case ".opus":
		return "audio/opus"
	case ".flac":
		return "audio/flac"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".bmp":
		return "image/bmp"
	case ".tif", ".tiff":
		return "image/tiff"
	case ".heic", ".heif":
		return "image/heic"
	default:
		return "application/octet-stream"
	}
}

// extFromContentType maps a storage content-type to a canonical file
// extension. The inverse of contentTypeForName, used when a source
// file's name carries no extension (e.g. a UUID name) so crop/resize
// can still pick the right output container. Covers the image/audio/
// video types media handles; "" for anything unrecognised (caller
// falls back to the legacy default).
func extFromContentType(ct string) string {
	ct = strings.ToLower(strings.TrimSpace(ct))
	if i := strings.IndexByte(ct, ';'); i >= 0 { // strip "; charset=..."
		ct = strings.TrimSpace(ct[:i])
	}
	switch ct {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "image/bmp":
		return ".bmp"
	case "image/tiff":
		return ".tiff"
	case "image/heic", "image/heif":
		return ".heic"
	case "video/mp4":
		return ".mp4"
	case "video/quicktime":
		return ".mov"
	case "video/webm":
		return ".webm"
	case "video/x-matroska":
		return ".mkv"
	case "audio/mpeg":
		return ".mp3"
	case "audio/wav", "audio/x-wav":
		return ".wav"
	case "audio/mp4":
		return ".m4a"
	case "audio/opus":
		return ".opus"
	case "audio/flac":
		return ".flac"
	}
	return ""
}

// isImageExt reports whether ext (with leading dot, any case) is one of
// the still-image extensions media indexes. Mirrors isMediaByExt's image
// arm so resize/crop classify their output the same way the indexer
// classified the input.
func isImageExt(ext string) bool {
	switch strings.ToLower(ext) {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".bmp", ".tif", ".tiff", ".heic", ".heif":
		return true
	}
	return false
}

func isAudioExt(ext string) bool {
	switch strings.ToLower(ext) {
	case ".mp3", ".wav", ".m4a", ".opus", ".flac":
		return true
	}
	return false
}

// imageAwareVideoFilterArgs assembles the ffmpeg argv for a single
// -vf filter (scale/crop) so the same op produces a valid still image
// when the output is an image and a valid clip when it's video.
//
//	image out → "-frames:v 1" (single frame; required so animated
//	            gif/webp sources don't try to write a sequence to one
//	            filename) + "-q:v 3" for jpeg quality; no audio stream
//	            to carry, so no "-c:a copy". Mirrors extractImageThumbnail.
//	video out → "-c:a copy" to pass the audio track through untouched.
func imageAwareVideoFilterArgs(vf, outputName string) []string {
	args := []string{
		"-y",
		"-loglevel", "error",
		"-progress", "pipe:1",
		"-i", "{input}",
		"-vf", vf,
	}
	ext := strings.ToLower(path.Ext(outputName))
	if isImageExt(ext) {
		args = append(args, "-frames:v", "1")
		if ext == ".jpg" || ext == ".jpeg" {
			args = append(args, "-q:v", "3")
		}
		return args
	}
	return append(args, "-c:a", "copy")
}

func audioFormatToCodec(format string) (codec, ext, contentType string, err error) {
	switch strings.ToLower(format) {
	case "mp3":
		return "libmp3lame", ".mp3", "audio/mpeg", nil
	case "wav":
		return "pcm_s16le", ".wav", "audio/wav", nil
	case "m4a":
		return "aac", ".m4a", "audio/mp4", nil
	case "opus":
		return "libopus", ".opus", "audio/opus", nil
	case "flac":
		return "flac", ".flac", "audio/flac", nil
	default:
		return "", "", "", fmt.Errorf("audio_extract: unsupported format %q (mp3|wav|m4a|opus|flac)", format)
	}
}
