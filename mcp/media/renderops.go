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
func buildPlan(op string, sources []string, params json.RawMessage, outputName string) (*opPlan, error) {
	switch op {
	case "trim":
		return planTrim(sources, params, outputName)
	case "resize":
		return planResize(sources, params, outputName)
	case "transcode":
		return planTranscode(sources, params, outputName)
	case "concat":
		return planConcat(sources, params, outputName)
	case "crop":
		return planCrop(sources, params, outputName)
	case "extract_frame":
		return planExtractFrame(sources, params, outputName)
	case "extract_reel":
		return planExtractReel(sources, params, outputName)
	case "audio_extract":
		return planAudioExtract(sources, params, outputName)
	default:
		return nil, fmt.Errorf("unknown operation: %s", op)
	}
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

func planResize(sources []string, raw json.RawMessage, outputName string) (*opPlan, error) {
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
	args := []string{
		"-y",
		"-loglevel", "error",
		"-progress", "pipe:1",
		"-i", "{input}",
		"-vf", scale,
		"-c:a", "copy",
	}
	name, ct := defaultOutputName(outputName, sources[0], "resize", "")
	return &opPlan{Filename: name, ContentType: ct, Args: args}, nil
}

// ─── transcode ──────────────────────────────────────────────────────
//
// Format change with optional codec/bitrate overrides. Format drives
// the output extension; codecs are passed as -c:v / -c:a when set.

type transcodeParams struct {
	Format     string `json:"format"`              // mp4|mkv|webm|mov|m4a|mp3|wav|opus
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
	X      int `json:"x"`
	Y      int `json:"y"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

func planCrop(sources []string, raw json.RawMessage, outputName string) (*opPlan, error) {
	if len(sources) != 1 {
		return nil, errors.New("crop takes exactly one source file_id")
	}
	var p cropParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("crop params: %w", err)
	}
	if p.Width <= 0 || p.Height <= 0 {
		return nil, errors.New("crop: width and height must be > 0")
	}
	if p.X < 0 || p.Y < 0 {
		return nil, errors.New("crop: x and y must be >= 0")
	}
	vf := fmt.Sprintf("crop=%d:%d:%d:%d", p.Width, p.Height, p.X, p.Y)
	args := []string{
		"-y",
		"-loglevel", "error",
		"-progress", "pipe:1",
		"-i", "{input}",
		"-vf", vf,
		"-c:a", "copy",
	}
	name, ct := defaultOutputName(outputName, sources[0], "crop", "")
	return &opPlan{Filename: name, ContentType: ct, Args: args}, nil
}

// ─── extract_frame ──────────────────────────────────────────────────
//
// Single PNG at an arbitrary timestamp. Distinct from the canonical
// thumbnail derivation — agents call this when they want a specific
// frame at a specific time, possibly multiple per source.

type extractFrameParams struct {
	AtMs  int64 `json:"at_ms"`
	Width int   `json:"width,omitempty"`
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
	if p.Width > 0 {
		args = append(args, "-vf", fmt.Sprintf("scale=%d:-2", p.Width))
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
// Audio: copied through. The trim happens server-side via -ss / -to
// before re-encoding the video, so we save audio bandwidth too.

type extractReelParams struct {
	StartMs     int64   `json:"start_ms"`
	EndMs       int64   `json:"end_ms"`
	TargetRatio string  `json:"target_ratio"` // "9:16" (default), "1:1", "4:5", "16:9", …
	OutputWidth int     `json:"output_width"` // optional; default 1080
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
	// Filter chain:
	//   crop=W:H:X:Y where:
	//     W = if source-wider-than-target then ih*rw/rh else iw
	//     H = if source-wider-than-target then ih       else iw*rh/rw
	//   X,Y = center using out_w / out_h (auto-references)
	// Then scale=output_width:-2 (height auto-derived, even).
	cropExpr := fmt.Sprintf(
		"crop=w='if(gt(iw/ih,%d/%d),ih*%d/%d,iw)':h='if(gt(iw/ih,%d/%d),ih,iw*%d/%d)':x='(iw-out_w)/2':y='(ih-out_h)/2'",
		rw, rh, rw, rh, rw, rh, rh, rw,
	)
	scaleExpr := fmt.Sprintf("scale=%d:-2", p.OutputWidth)
	args := []string{
		"-y",
		"-loglevel", "error",
		"-progress", "pipe:1",
		// Demuxer-level seek before -i: fast + frame-accurate enough
		// for the typical reel use case. Same convention as planTrim.
		"-ss", msToSeconds(p.StartMs),
		"-to", msToSeconds(p.EndMs),
		"-i", "{input}",
		"-vf", cropExpr + "," + scaleExpr,
		"-c:a", "copy",                        // audio passthrough — no re-encode
		"-avoid_negative_ts", "make_zero",
	}
	name, ct := defaultOutputName(outputName, sources[0], "reel", ".mp4")
	return &opPlan{Filename: name, ContentType: ct, Args: args}, nil
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

// ─── helpers ────────────────────────────────────────────────────────

func msToSeconds(ms int64) string {
	// ffmpeg accepts decimal seconds. Use 3-digit precision so we can
	// trim at millisecond granularity without floating-point drift.
	return fmt.Sprintf("%d.%03d", ms/1000, ms%1000)
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
	default:
		return "application/octet-stream"
	}
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
