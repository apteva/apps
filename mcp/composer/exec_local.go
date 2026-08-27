package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// localFFmpegExecutor shells out to ffmpeg on the sidecar's host.
// Each Render call:
//  1. resolves every asset.src to a URL ffmpeg can fetch over HTTPS
//     (signed if storage gates it).
//  2. assembles a filter_complex from the canonical Edit.
//  3. spawns ffmpeg in a per-render scratch dir.
//  4. returns the absolute path to the output file; caller stores it.
//
// Cancellation: uses the passed context — `exec.CommandContext`
// SIGKILLs ffmpeg on ctx-cancel.
type localFFmpegExecutor struct{}

func (e *localFFmpegExecutor) Name() string { return "local" }

func (e *localFFmpegExecutor) Render(
	ctx context.Context,
	app *sdk.AppCtx,
	edit *Edit,
	output Output,
	projectID string,
) (Result, error) {
	start := time.Now()

	scratch, err := os.MkdirTemp("", "composer-render-")
	if err != nil {
		return Result{}, fmt.Errorf("scratch dir: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(scratch) }

	visual := primaryVisualTrack(edit)
	audioClips := audioTimelineClips(edit)
	if visual == nil {
		inputs := make([]string, 0, len(audioClips)+1)
		for i, c := range audioClips {
			if clipAssetType(c, "audio") == "silence" {
				continue
			}
			url, err := resolveAssetLocal(app, c.Asset.Src)
			if err != nil {
				cleanup()
				return Result{}, fmt.Errorf("audio clip[%d]: resolve %q: %w", i, c.Asset.Src, err)
			}
			inputs = append(inputs, url)
		}
		soundtrackIdx := -1
		if s := edit.Timeline.Soundtrack; s != nil {
			url, err := resolveAssetLocal(app, s.Src)
			if err != nil {
				cleanup()
				return Result{}, fmt.Errorf("soundtrack resolve %q: %w", s.Src, err)
			}
			soundtrackIdx = len(inputs)
			inputs = append(inputs, url)
		}
		outFile := filepath.Join(scratch, "out."+output.Format)
		args := buildLocalAudioFFmpegArgs(edit, output, inputs, soundtrackIdx, outFile)
		result, runErr := runLocalFFmpeg(ctx, app, start, scratch, len(inputs), outFile, args)
		if runErr != nil {
			app.Logger().Warn("kept scratch dir for post-mortem", "path", scratch, "err", runErr)
		} else {
			result.Cleanup = cleanup
		}
		return result, runErr
	}

	// Resolve every clip's asset to a URL. ffmpeg accepts https:// inputs
	// natively (movflags+frag work); no need to download first.
	visualRefs := visualClipRefs(edit)
	inputs := make([]string, 0, len(visualRefs)+len(audioClips)+1)
	visualHasAudio := make([]bool, len(visualRefs))
	for i, ref := range visualRefs {
		c := ref.clip
		url, err := resolveAssetLocal(app, c.Asset.Src)
		if err != nil {
			cleanup()
			return Result{}, fmt.Errorf("visual clip[%d]: resolve %q: %w", i, c.Asset.Src, err)
		}
		visualHasAudio[i] = visualClipMayUseSourceAudioForLayer(c, ref.base) && probeMediaHasAudio(url)
		inputs = append(inputs, url)
	}
	for i, c := range audioClips {
		if clipAssetType(c, "audio") == "silence" {
			continue
		}
		url, err := resolveAssetLocal(app, c.Asset.Src)
		if err != nil {
			cleanup()
			return Result{}, fmt.Errorf("audio clip[%d]: resolve %q: %w", i, c.Asset.Src, err)
		}
		inputs = append(inputs, url)
	}
	var soundtrackIdx int = -1
	if s := edit.Timeline.Soundtrack; s != nil {
		url, err := resolveAssetLocal(app, s.Src)
		if err != nil {
			cleanup()
			return Result{}, fmt.Errorf("soundtrack resolve %q: %w", s.Src, err)
		}
		soundtrackIdx = len(inputs)
		inputs = append(inputs, url)
	}

	outFile := filepath.Join(scratch, "out."+output.Format)
	args := buildLocalFFmpegArgsWithAudioInfo(edit, output, inputs, soundtrackIdx, outFile, visualHasAudio)
	if argsUseComposerFont(args) {
		fontPaths, fontErr := writeComposerFonts(scratch, composerFontFacesInArgs(args))
		if fontErr != nil {
			cleanup()
			return Result{}, fontErr
		}
		args = materializeComposerFontArgs(args, fontPaths)
	}

	result, runErr := runLocalFFmpeg(ctx, app, start, scratch, len(inputs), outFile, args)
	if runErr != nil {
		app.Logger().Warn("kept scratch dir for post-mortem", "path", scratch, "err", runErr)
	} else {
		result.Cleanup = cleanup
	}
	return result, runErr
}

func runLocalFFmpeg(ctx context.Context, app *sdk.AppCtx, start time.Time, scratch string, inputCount int, outFile string, args []string) (Result, error) {
	app.Logger().Info("local ffmpeg render", "scratch", scratch, "inputs", inputCount, "out", outFile)

	cmd := exec.CommandContext(ctx, ffmpegPath(), args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return Result{FFmpegCommand: shellEcho(ffmpegPath(), args)}, fmt.Errorf("ffmpeg failed: %w\nstderr (last 1KB):\n%s",
			err, truncTail(stderr.String(), 1024))
	}

	return Result{
		Sync:          true,
		LocalPath:     outFile,
		DurationMS:    time.Since(start).Milliseconds(),
		FFmpegCommand: shellEcho(ffmpegPath(), args),
	}, nil
}

// buildLocalFFmpegArgs assembles the ffmpeg argv for the canonical Edit.
//
// v0.1 strategy (intentionally simple — no xfade, no transitions yet):
//   - Each clip is opened as a separate -i input.
//   - Per-clip filter chain: scale+pad to the output dims, set fps,
//     optional drawtext overlay, optional fade-in/out at clip edges.
//   - All clips' v + a streams concatenated via the concat filter.
//   - Soundtrack (optional) mixed in on top of the concat'd audio
//     with amix=normalize=0 so the soundtrack's volume override is
//     honoured directly.
//
// Returns the args list; the caller logs the assembled command for
// debugging via shellEcho.
func buildLocalFFmpegArgs(edit *Edit, output Output, inputs []string, soundtrackIdx int, outFile string) []string {
	return buildLocalFFmpegArgsWithAudioInfo(edit, output, inputs, soundtrackIdx, outFile, nil)
}

func buildLocalFFmpegArgsWithAudioInfo(edit *Edit, output Output, inputs []string, soundtrackIdx int, outFile string, visualHasAudio []bool) []string {
	w, h := resolutionWH(output.Resolution, output.Aspect)
	track := primaryVisualTrack(edit)
	if track == nil {
		track = &Track{}
	}
	audioClips := audioTimelineClips(edit)
	visualCount := len(track.Clips)
	visualRefs := visualClipRefs(edit)
	totalVisuals := len(visualRefs)

	args := []string{"-y", "-loglevel", "error"}

	// One -i per input.
	for i, src := range inputs {
		if i < totalVisuals {
			ref := visualRefs[i]
			if clipAssetType(ref.clip, "visual") == "image" {
				args = append(args,
					"-loop", "1",
					"-t", trimFloat(clipDuration(ref.clip)),
					"-i", src,
				)
				continue
			}
			if visualClipLoopsForSlot(ref.clip) {
				args = append(args, "-stream_loop", "-1")
			}
			args = append(args, "-i", src)
			continue
		}
		// Images need -loop 1 + -t to behave as fixed-length stills.
		if i < visualCount && clipAssetType(track.Clips[i], "visual") == "image" {
			args = append(args,
				"-loop", "1",
				"-t", trimFloat(clipDuration(track.Clips[i])),
				"-i", src,
			)
			continue
		}
		if i < visualCount && visualClipLoopsForSlot(track.Clips[i]) {
			args = append(args, "-stream_loop", "-1")
		}
		if i == soundtrackIdx && soundtrackLoops(edit.Timeline.Soundtrack) {
			args = append(args, "-stream_loop", "-1")
		}
		args = append(args, "-i", src)
	}

	// Build the filter graph.
	var filter strings.Builder
	for i, c := range track.Clips {
		// Select the source range/crop, fit it to the canvas, then apply
		// source-space camera keyframes. The clip is still trimmed to its
		// timeline length below, so one source can back many timeline clips.
		fmt.Fprintf(&filter, "[%d:v]%s", i, buildBaseVisualFilter(c, w, h, output.FPS, edit.Timeline.Background))
		// Trim length — important for video clips that are longer than
		// the requested clip length. Image clips are already length-pinned
		// via -t on input.
		if clipAssetType(c, "visual") != "image" {
			d := trimFloat(clipDuration(c))
			if visualClipPadsForSlot(c) {
				fmt.Fprintf(&filter, ",tpad=stop_mode=clone:stop_duration=%s", d)
			}
			fmt.Fprintf(&filter, ",trim=duration=%s,setpts=PTS-STARTPTS", d)
		}
		// Optional fade in/out within the clip.
		if c.Transition != nil {
			if c.Transition.In == "fade" {
				filter.WriteString(",fade=t=in:st=0:d=0.3")
			}
			if c.Transition.Out == "fade" {
				fmt.Fprintf(&filter, ",fade=t=out:st=%s:d=0.3", trimFloat(clipDuration(c)-0.3))
			}
		}
		// Optional text overlay (drawtext).
		if c.Text != nil && strings.TrimSpace(c.Text.Body) != "" {
			filter.WriteString(",")
			filter.WriteString(buildDrawText(c.Text, w, h))
		}
		fmt.Fprintf(&filter, "[v%d];", i)

		// Per-clip audio: trim or silence-pad to clip length.
		if !visualClipUsesSourceAudio(c, visualHasAudioAt(visualHasAudio, i, c)) {
			// Synthesize silent audio for image clips, muted clips, and
			// no-audio videos so concat audio stream count matches.
			fmt.Fprintf(&filter, "anullsrc=channel_layout=stereo:sample_rate=44100,atrim=duration=%s[a%d];", trimFloat(clipDuration(c)), i)
		} else {
			fmt.Fprintf(&filter, "[%d:a]%sapad,atrim=duration=%s,asetpts=PTS-STARTPTS[a%d];", i, sourceAudioFilterPrefix(c), trimFloat(clipDuration(c)), i)
		}
	}

	if baseTrackNeedsTimedComposition(track) {
		writeTimedBaseTrack(&filter, edit, track, w, h, output.FPS)
	} else {
		// Legacy and contiguous base tracks stay on the efficient concat path.
		n := len(track.Clips)
		for i := 0; i < n; i++ {
			fmt.Fprintf(&filter, "[v%d][a%d]", i, i)
		}
		fmt.Fprintf(&filter, "concat=n=%d:v=1:a=1[vcat][acat];", n)
		baseDuration := baseVisualDuration(track)
		totalDuration := editDurationSeconds(edit)
		if totalDuration > baseDuration+0.001 {
			fmt.Fprintf(&filter, "[vcat]tpad=stop_mode=clone:stop_duration=%s[vbase];", trimFloat(totalDuration-baseDuration))
		} else {
			filter.WriteString("[vcat]null[vbase];")
		}
	}

	mixLabels := []string{"[acat]"}
	audioInputCursor := totalVisuals
	for oi, ref := range visualOverlayClipRefs(edit) {
		idx := ref.inputIndex
		c := ref.clip
		if !visualClipUsesSourceAudioForLayer(c, visualHasAudioAt(visualHasAudio, idx, c), false) {
			continue
		}
		delayMS := int(c.Start * 1000)
		if delayMS < 0 {
			delayMS = 0
		}
		writeTimedAudioFilter(&filter, idx, c, delayMS, fmt.Sprintf("ova%d", oi))
		mixLabels = append(mixLabels, fmt.Sprintf("[ova%d]", oi))
	}
	for i, c := range audioClips {
		delayMS := int(c.Start * 1000)
		if delayMS < 0 {
			delayMS = 0
		}
		if clipAssetType(c, "audio") == "silence" {
			fmt.Fprintf(&filter, "anullsrc=channel_layout=stereo:sample_rate=44100,atrim=duration=%s,adelay=%d|%d[ta%d];",
				trimFloat(clipDuration(c)), delayMS, delayMS, i)
		} else {
			writeTimedAudioFilter(&filter, audioInputCursor, c, delayMS, fmt.Sprintf("ta%d", i))
			audioInputCursor++
		}
		mixLabels = append(mixLabels, fmt.Sprintf("[ta%d]", i))
	}

	if soundtrackIdx >= 0 {
		vol := 1.0
		if v := edit.Timeline.Soundtrack.Volume; v > 0 {
			vol = v
		}
		fmt.Fprintf(&filter,
			"[%d:a]volume=%g,atrim=duration=%s[snd];",
			soundtrackIdx, vol, trimFloat(editDurationSeconds(edit)),
		)
		mixLabels = append(mixLabels, "[snd]")
	}
	if len(mixLabels) > 1 {
		for _, label := range mixLabels {
			filter.WriteString(label)
		}
		fmt.Fprintf(&filter, "amix=inputs=%d:duration=longest:normalize=0[aout];", len(mixLabels))
	} else {
		filter.WriteString("[acat]anull[aout];")
	}
	videoLabel := "vbase"
	for i, ref := range visualOverlayClipRefs(edit) {
		outLabel := fmt.Sprintf("vov%d", i)
		filter.WriteString(buildVisualOverlayChain(ref.inputIndex, videoLabel, outLabel, ref.clip, w, h, output.FPS))
		videoLabel = outLabel
	}
	for i, c := range textOverlayClips(edit) {
		outLabel := fmt.Sprintf("vtxt%d", i)
		filter.WriteString(buildTimedDrawTextChain(videoLabel, outLabel, c, w, h))
		videoLabel = outLabel
	}
	fmt.Fprintf(&filter, "[%s]null[vout]", videoLabel)

	args = append(args,
		"-filter_complex", filter.String(),
		"-map", "[vout]",
		"-map", "[aout]",
		"-c:v", "libx264",
		"-preset", "veryfast",
		"-pix_fmt", "yuv420p",
		"-c:a", "aac",
		"-b:a", "128k",
		"-movflags", "+faststart",
		outFile,
	)
	return args
}

func visualHasAudioAt(values []bool, i int, c Clip) bool {
	if i >= 0 && i < len(values) {
		return values[i]
	}
	return clipAssetType(c, "visual") == "video"
}

func visualClipUsesSourceAudio(c Clip, hasAudio bool) bool {
	if !hasAudio || clipAssetType(c, "visual") != "video" {
		return false
	}
	return visualClipMayUseSourceAudio(c)
}

func visualClipUsesSourceAudioForLayer(c Clip, hasAudio bool, base bool) bool {
	if !hasAudio || clipAssetType(c, "visual") != "video" {
		return false
	}
	return visualClipMayUseSourceAudioForLayer(c, base)
}

func visualClipMayUseSourceAudioForLayer(c Clip, base bool) bool {
	if c.AI != nil && boolOption(c.AI.Options, "no_sound") {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(c.SourceAudio)) {
	case "mute":
		return false
	case "keep":
		return true
	case "auto", "":
		return base
	default:
		return false
	}
}

func visualClipMayUseSourceAudio(c Clip) bool {
	switch strings.ToLower(strings.TrimSpace(c.SourceAudio)) {
	case "mute":
		return false
	case "keep":
		return true
	}
	if c.AI != nil && boolOption(c.AI.Options, "no_sound") {
		return false
	}
	return true
}

func baseVisualDuration(track *Track) float64 {
	if track == nil {
		return 0
	}
	if baseTrackNeedsTimedComposition(track) {
		var out float64
		for _, c := range track.Clips {
			if end := maxFloat(0, c.Start) + clipDuration(c); end > out {
				out = end
			}
		}
		return out
	}
	var out float64
	for _, c := range track.Clips {
		out += clipDuration(c)
	}
	return out
}

// baseTrackNeedsTimedComposition preserves the historical implicit-concat
// shape (all starts omitted/zero) and the fast path for explicitly contiguous
// clips. Any gap, overlap, or out-of-order clip requires timestamp-aware
// composition so each declared start remains authoritative.
func baseTrackNeedsTimedComposition(track *Track) bool {
	if track == nil || len(track.Clips) < 2 {
		return track != nil && len(track.Clips) == 1 && track.Clips[0].Start > 0.001
	}
	allZero := true
	for _, c := range track.Clips {
		if c.Start > 0.001 {
			allZero = false
			break
		}
	}
	if allZero {
		return false
	}
	cursor := 0.0
	for _, c := range track.Clips {
		if !sameTime(maxFloat(0, c.Start), cursor) {
			return true
		}
		cursor += clipDuration(c)
	}
	return false
}

func writeTimedBaseTrack(filter *strings.Builder, edit *Edit, track *Track, w, h, fps int) {
	totalDuration := editDurationSeconds(edit)
	if totalDuration <= 0 {
		totalDuration = baseVisualDuration(track)
	}
	if totalDuration <= 0 {
		totalDuration = 0.1
	}
	fmt.Fprintf(filter, "color=c=%s:s=%dx%d:r=%d:d=%s[vbg];",
		escFFmpegColor(edit.Timeline.Background), w, h, fps, trimFloat(totalDuration))

	baseLabel := "vbg"
	ordered := timelineOrderedClipIndices(track)
	for position, index := range ordered {
		c := track.Clips[index]
		start := maxFloat(0, c.Start)
		end := start + clipDuration(c)
		shifted := fmt.Sprintf("vbt%d", index)
		out := fmt.Sprintf("vbase%d", position)
		fmt.Fprintf(filter, "[v%d]setpts=PTS-STARTPTS+%s/TB[%s];[%s][%s]overlay=x=0:y=0:enable='between(t\\,%s\\,%s)':eof_action=pass[%s];",
			index, trimFloat(start), shifted, baseLabel, shifted, trimFloat(start), trimFloat(end), out)
		baseLabel = out
	}
	fmt.Fprintf(filter, "[%s]null[vbase];", baseLabel)

	// Delay and mix base-clip audio on the same absolute timeline. A silent
	// canvas track keeps audio duration aligned through visual gaps.
	fmt.Fprintf(filter, "anullsrc=channel_layout=stereo:sample_rate=44100,atrim=duration=%s[abase];", trimFloat(totalDuration))
	for _, index := range ordered {
		delayMS := int(maxFloat(0, track.Clips[index].Start) * 1000)
		fmt.Fprintf(filter, "[a%d]adelay=%d|%d[abt%d];", index, delayMS, delayMS, index)
	}
	filter.WriteString("[abase]")
	for _, index := range ordered {
		fmt.Fprintf(filter, "[abt%d]", index)
	}
	fmt.Fprintf(filter, "amix=inputs=%d:duration=longest:normalize=0[acat];", len(ordered)+1)
}

type resolvedClipLayout struct {
	fit     string
	x       int
	y       int
	width   int
	height  int
	opacity float64
}

func buildVisualOverlayChain(inputIdx int, baseLabel, outLabel string, c Clip, canvasW, canvasH, fps int) string {
	layout := resolveClipLayout(c, canvasW, canvasH)
	d := trimFloat(clipDuration(c))
	start := trimFloat(c.Start)
	end := trimFloat(c.Start + clipDuration(c))
	chain := buildLayerVisualFilter(c, layout, fps)
	if layout.opacity < 1 {
		chain += ",colorchannelmixer=aa=" + trimFloat(layout.opacity)
	}
	return fmt.Sprintf("[%d:v]%s,trim=duration=%s,setpts=PTS-STARTPTS+%s/TB[ov%d];[%s][ov%d]overlay=x=%d:y=%d:enable='between(t\\,%s\\,%s)':eof_action=pass[%s];",
		inputIdx, chain, d, start, inputIdx, baseLabel, inputIdx, layout.x, layout.y, start, end, outLabel)
}

func visualFitFilter(layout resolvedClipLayout, fps int) string {
	w := layout.width
	h := layout.height
	switch layout.fit {
	case "contain":
		return fmt.Sprintf("format=rgba,scale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:(ow-iw)/2:(oh-ih)/2:color=black@0,setsar=1,fps=%d", w, h, w, h, fps)
	case "cover", "stretch":
		return fmt.Sprintf("format=rgba,scale=%d:%d,setsar=1,fps=%d", w, h, fps)
	case "none":
		return fmt.Sprintf("format=rgba,scale=%d:%d:force_original_aspect_ratio=decrease,setsar=1,fps=%d", w, h, fps)
	default: // crop
		return fmt.Sprintf("format=rgba,scale=%d:%d:force_original_aspect_ratio=increase,crop=%d:%d,setsar=1,fps=%d", w, h, w, h, fps)
	}
}

func buildBaseVisualFilter(c Clip, w, h, fps int, background string) string {
	parts := sourceVisualFilters(c)
	fit := strings.ToLower(strings.TrimSpace(c.Fit))
	if c.Layout != nil && strings.TrimSpace(c.Layout.Fit) != "" {
		fit = strings.ToLower(strings.TrimSpace(c.Layout.Fit))
	}
	switch fit {
	case "crop", "cover":
		parts = append(parts, fmt.Sprintf("format=rgba,scale=%d:%d:force_original_aspect_ratio=increase,crop=%d:%d,setsar=1,fps=%d", w, h, w, h, fps))
	case "stretch":
		parts = append(parts, fmt.Sprintf("format=rgba,scale=%d:%d,setsar=1,fps=%d", w, h, fps))
	default:
		parts = append(parts, fmt.Sprintf("format=rgba,scale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:(ow-iw)/2:(oh-ih)/2:color=%s,setsar=1,fps=%d", w, h, w, h, escFFmpegColor(background), fps))
	}
	if camera := cameraZoomPanFilter(c, w, h, fps); camera != "" {
		parts = append(parts, camera)
	}
	return strings.Join(parts, ",")
}

func buildLayerVisualFilter(c Clip, layout resolvedClipLayout, fps int) string {
	parts := sourceVisualFilters(c)
	parts = append(parts, visualFitFilter(layout, fps))
	if camera := cameraZoomPanFilter(c, layout.width, layout.height, fps); camera != "" {
		parts = append(parts, camera)
	}
	return strings.Join(parts, ",")
}

func sourceVisualFilters(c Clip) []string {
	parts := []string{}
	if trim := sourceTrimFilter("trim", c); trim != "" {
		parts = append(parts, trim, "setpts=PTS-STARTPTS")
	}
	if crop := c.Crop; crop != nil {
		parts = append(parts, fmt.Sprintf(
			"crop=iw*%s:ih*%s:iw*%s:ih*%s",
			trimFloat(crop.Width), trimFloat(crop.Height), trimFloat(crop.X), trimFloat(crop.Y),
		))
	}
	return parts
}

func sourceAudioFilterPrefix(c Clip) string {
	if trim := sourceTrimFilter("atrim", c); trim != "" {
		return trim + ",asetpts=PTS-STARTPTS,"
	}
	return ""
}

func sourceTrimFilter(name string, c Clip) string {
	if c.SourceStart <= 0 && c.SourceEnd <= 0 {
		return ""
	}
	parts := []string{}
	if c.SourceStart > 0 {
		parts = append(parts, "start="+trimFloat(c.SourceStart))
	}
	if c.SourceEnd > 0 {
		parts = append(parts, "end="+trimFloat(c.SourceEnd))
	}
	return name + "=" + strings.Join(parts, ":")
}

type cameraPoint struct {
	time   float64
	x      float64
	y      float64
	scale  float64
	easing string
}

// cameraZoomPanFilter converts source-space transform keyframes into a
// frame-evaluated FFmpeg zoompan expression. The output size is fixed, so it
// composes safely with both fullscreen base clips and timed overlay layers.
func cameraZoomPanFilter(c Clip, w, h, fps int) string {
	if c.Transform == nil || w <= 0 || h <= 0 || fps <= 0 {
		return ""
	}
	points := cameraPoints(c.Transform)
	if len(points) == 0 {
		return ""
	}
	timeExpr := fmt.Sprintf("on/%d", fps)
	z := cameraPropertyExpr(points, timeExpr, func(p cameraPoint) float64 { return p.scale })
	x := cameraPropertyExpr(points, timeExpr, func(p cameraPoint) float64 { return p.x })
	y := cameraPropertyExpr(points, timeExpr, func(p cameraPoint) float64 { return p.y })
	return fmt.Sprintf(
		"zoompan=z='%s':x='max(0,min(iw-iw/zoom,iw*(%s)-iw/(2*zoom)))':y='max(0,min(ih-ih/zoom,ih*(%s)-ih/(2*zoom)))':d=1:s=%dx%d:fps=%d,format=rgba",
		z, x, y, w, h, fps,
	)
}

func cameraPoints(t *Transform) []cameraPoint {
	if t == nil {
		return nil
	}
	x, y, scale := 0.5, 0.5, 1.0
	if t.X != nil {
		x = *t.X
	}
	if t.Y != nil {
		y = *t.Y
	}
	if t.Scale > 0 {
		scale = t.Scale
	}
	points := []cameraPoint{{time: 0, x: x, y: y, scale: scale, easing: "linear"}}
	for _, keyframe := range t.Keyframes {
		if keyframe.X != nil {
			x = *keyframe.X
		}
		if keyframe.Y != nil {
			y = *keyframe.Y
		}
		if keyframe.Scale > 0 {
			scale = keyframe.Scale
		}
		point := cameraPoint{time: keyframe.Time, x: x, y: y, scale: scale, easing: strings.ToLower(strings.TrimSpace(keyframe.Easing))}
		if point.easing == "" {
			point.easing = "linear"
		}
		if point.time == 0 {
			points[0] = point
			continue
		}
		points = append(points, point)
	}
	return points
}

func cameraPropertyExpr(points []cameraPoint, timeExpr string, value func(cameraPoint) float64) string {
	if len(points) == 0 {
		return "0"
	}
	if len(points) == 1 {
		return trimFloat(value(points[0]))
	}
	expr := trimFloat(value(points[len(points)-1]))
	for i := len(points) - 2; i >= 0; i-- {
		from, to := points[i], points[i+1]
		duration := to.time - from.time
		if duration <= 0 {
			continue
		}
		progress := fmt.Sprintf("max(0,min(1,((%s)-%s)/%s))", timeExpr, trimFloat(from.time), trimFloat(duration))
		progress = cameraEaseExpr(progress, to.easing)
		segment := fmt.Sprintf("%s+(%s-%s)*(%s)", trimFloat(value(from)), trimFloat(value(to)), trimFloat(value(from)), progress)
		expr = fmt.Sprintf("if(lt(%s\\,%s)\\,%s\\,%s)", timeExpr, trimFloat(to.time), segment, expr)
	}
	return expr
}

func cameraEaseExpr(progress, easing string) string {
	switch strings.ToLower(strings.TrimSpace(easing)) {
	case "ease_in":
		return "(" + progress + ")*(" + progress + ")"
	case "ease_out":
		return "1-(1-(" + progress + "))*(1-(" + progress + "))"
	case "ease_in_out":
		return fmt.Sprintf("if(lt((%s)\\,0.5)\\,2*(%s)*(%s)\\,1-pow(-2*(%s)+2\\,2)/2)", progress, progress, progress, progress)
	default:
		return progress
	}
}

func resolveClipLayout(c Clip, canvasW, canvasH int) resolvedClipLayout {
	width := measureClipLayoutValue(c.Width, canvasW)
	height := measureClipLayoutValue(c.Height, canvasH)
	fit := strings.ToLower(strings.TrimSpace(c.Fit))
	opacity := c.Opacity
	scale := c.Scale
	position := clipPositionName(c.Position)
	marginX, marginY := 0, 0
	var explicitX, explicitY *float64
	if l := c.Layout; l != nil {
		if l.Fit != "" {
			fit = strings.ToLower(strings.TrimSpace(l.Fit))
		}
		if l.Width > 0 {
			width = measureClipLayoutValue(l.Width, canvasW)
		}
		if l.Height > 0 {
			height = measureClipLayoutValue(l.Height, canvasH)
		}
		if l.Scale > 0 {
			scale = l.Scale
		}
		if l.Opacity > 0 {
			opacity = l.Opacity
		}
		if l.Anchor != "" {
			position = l.Anchor
		}
		if l.Position != "" {
			position = l.Position
		}
		margin := int(l.Margin + 0.5)
		marginX, marginY = margin, margin
		if l.MarginX > 0 {
			marginX = int(l.MarginX + 0.5)
		}
		if l.MarginY > 0 {
			marginY = int(l.MarginY + 0.5)
		}
		explicitX, explicitY = l.X, l.Y
	}
	if fit == "" {
		fit = "crop"
	}
	if opacity <= 0 {
		opacity = 1
	}
	if scale <= 0 {
		scale = 1
	}
	if width <= 0 && height <= 0 {
		width, height = canvasW, canvasH
	} else if width <= 0 {
		width = int(float64(height) * 16.0 / 9.0)
	} else if height <= 0 {
		height = int(float64(width) * 9.0 / 16.0)
	}
	width = int(float64(width)*scale + 0.5)
	height = int(float64(height)*scale + 0.5)
	if width < 2 {
		width = 2
	}
	if height < 2 {
		height = 2
	}
	x, y := anchorPosition(position, canvasW, canvasH, width, height, marginX, marginY)
	if c.Offset != nil {
		x += int(c.Offset.X * float64(canvasW))
		y -= int(c.Offset.Y * float64(canvasH))
	}
	if explicitX != nil {
		x = measureClipLayoutValue(*explicitX, canvasW)
	}
	if explicitY != nil {
		y = measureClipLayoutValue(*explicitY, canvasH)
	}
	return resolvedClipLayout{fit: fit, x: x, y: y, width: width, height: height, opacity: opacity}
}

func measureClipLayoutValue(v float64, viewport int) int {
	if v <= 0 {
		return 0
	}
	if v <= 1 {
		return int(v*float64(viewport) + 0.5)
	}
	return int(v + 0.5)
}

func clipPositionName(pos *Position) string {
	if pos == nil {
		return ""
	}
	if strings.TrimSpace(pos.Name) != "" {
		return pos.Name
	}
	return pos.Anchor
}

func normalizePositionName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "-", "")
	s = strings.ReplaceAll(s, "_", "")
	return s
}

func anchorPosition(position string, canvasW, canvasH, boxW, boxH, marginX, marginY int) (int, int) {
	switch normalizePositionName(position) {
	case "topleft":
		return marginX, marginY
	case "top":
		return (canvasW - boxW) / 2, marginY
	case "topright":
		return canvasW - boxW - marginX, marginY
	case "left":
		return marginX, (canvasH - boxH) / 2
	case "right":
		return canvasW - boxW - marginX, (canvasH - boxH) / 2
	case "bottomleft":
		return marginX, canvasH - boxH - marginY
	case "bottom":
		return (canvasW - boxW) / 2, canvasH - boxH - marginY
	case "bottomright":
		return canvasW - boxW - marginX, canvasH - boxH - marginY
	default:
		return (canvasW - boxW) / 2, (canvasH - boxH) / 2
	}
}

func boolOption(options map[string]any, key string) bool {
	if options == nil {
		return false
	}
	v, ok := options[key]
	if !ok {
		return false
	}
	switch x := v.(type) {
	case bool:
		return x
	case string:
		switch strings.ToLower(strings.TrimSpace(x)) {
		case "1", "true", "yes", "on":
			return true
		default:
			return false
		}
	case float64:
		return x != 0
	case int:
		return x != 0
	default:
		return false
	}
}

func buildLocalAudioFFmpegArgs(edit *Edit, output Output, inputs []string, soundtrackIdx int, outFile string) []string {
	audioClips := audioTimelineClips(edit)
	args := []string{"-y", "-loglevel", "error"}
	for i, src := range inputs {
		if i == soundtrackIdx && soundtrackLoops(edit.Timeline.Soundtrack) {
			args = append(args, "-stream_loop", "-1")
		}
		args = append(args, "-i", src)
	}

	var filter strings.Builder
	mixLabels := make([]string, 0, len(audioClips)+1)
	inputCursor := 0
	for i, c := range audioClips {
		delayMS := int(c.Start * 1000)
		if delayMS < 0 {
			delayMS = 0
		}
		if clipAssetType(c, "audio") == "silence" {
			fmt.Fprintf(&filter, "anullsrc=channel_layout=stereo:sample_rate=44100,atrim=duration=%s,adelay=%d|%d[ta%d];",
				trimFloat(clipDuration(c)), delayMS, delayMS, i)
		} else {
			writeTimedAudioFilter(&filter, inputCursor, c, delayMS, fmt.Sprintf("ta%d", i))
			inputCursor++
		}
		mixLabels = append(mixLabels, fmt.Sprintf("[ta%d]", i))
	}
	if soundtrackIdx >= 0 {
		vol := 1.0
		if v := edit.Timeline.Soundtrack.Volume; v > 0 {
			vol = v
		}
		fmt.Fprintf(&filter,
			"[%d:a]volume=%g,atrim=duration=%s[snd];",
			soundtrackIdx, vol, trimFloat(editDurationSeconds(edit)),
		)
		mixLabels = append(mixLabels, "[snd]")
	}
	if len(mixLabels) == 0 {
		filter.WriteString("anullsrc=channel_layout=stereo:sample_rate=44100,atrim=duration=0.1[aout]")
	} else if len(mixLabels) == 1 {
		fmt.Fprintf(&filter, "%sanull[aout]", mixLabels[0])
	} else {
		for _, label := range mixLabels {
			filter.WriteString(label)
		}
		fmt.Fprintf(&filter, "amix=inputs=%d:duration=longest:normalize=0[aout]", len(mixLabels))
	}

	args = append(args,
		"-filter_complex", filter.String(),
		"-map", "[aout]",
		"-vn",
	)
	args = append(args, audioCodecArgs(output)...)
	args = append(args, outFile)
	return args
}

func writeTimedAudioFilter(filter *strings.Builder, inputIdx int, c Clip, delayMS int, label string) {
	chain := []string{}
	if trim := sourceTrimFilter("atrim", c); trim != "" {
		chain = append(chain, trim, "asetpts=PTS-STARTPTS")
	}
	if c.Audio != nil && c.Audio.TrimSilence {
		chain = append(chain, "silenceremove=start_periods=1:start_threshold=-50dB:start_silence=0.05")
	}
	chain = append(chain, "apad", "atrim=duration="+trimFloat(clipDuration(c)), "asetpts=PTS-STARTPTS")
	if c.Audio != nil {
		if c.Audio.GainDB != 0 {
			chain = append(chain, "volume="+trimFloat(c.Audio.GainDB)+"dB")
		}
		if c.Audio.Normalize {
			i := c.Audio.LoudnessTarget
			if i == 0 {
				i = -18
			}
			tp := c.Audio.PeakLimitDB
			if tp == 0 {
				tp = -3
			}
			chain = append(chain, fmt.Sprintf("loudnorm=I=%s:TP=%s:LRA=11", trimFloat(i), trimFloat(tp)))
		}
		if c.Audio.FadeInSeconds > 0 {
			chain = append(chain, "afade=t=in:st=0:d="+trimFloat(c.Audio.FadeInSeconds))
		}
		if c.Audio.FadeOutSeconds > 0 {
			st := clipDuration(c) - c.Audio.FadeOutSeconds
			if st < 0 {
				st = 0
			}
			chain = append(chain, "afade=t=out:st="+trimFloat(st)+":d="+trimFloat(c.Audio.FadeOutSeconds))
		}
	}
	chain = append(chain, fmt.Sprintf("adelay=%d|%d", delayMS, delayMS), "volume="+trimFloat(clipVolume(c)))
	fmt.Fprintf(filter, "[%d:a]%s[%s];", inputIdx, strings.Join(chain, ","), label)
}

func soundtrackLoops(s *Soundtrack) bool {
	if s == nil {
		return false
	}
	if s.Timing == nil {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(s.Timing.Behavior)) {
	case "", "trim_or_loop", "loop":
		return true
	default:
		return false
	}
}

func visualClipPadsForSlot(c Clip) bool {
	behavior, mode := visualClipTiming(c)
	switch behavior {
	case "trim", "trim_or_loop", "loop", "stretch", "regenerate":
		return false
	case "pad":
		return true
	}
	switch mode {
	case "fit_source", "fit_group", "fit_timeline":
		source := visualClipSourceDuration(c)
		return source <= 0 || source+0.01 >= clipDuration(c)
	default:
		return true
	}
}

func visualClipLoopsForSlot(c Clip) bool {
	if clipAssetType(c, "visual") != "video" {
		return false
	}
	behavior, _ := visualClipTiming(c)
	return behavior == "loop" || behavior == "trim_or_loop"
}

func visualClipTiming(c Clip) (behavior, mode string) {
	if c.Timing == nil {
		return "", ""
	}
	return strings.ToLower(strings.TrimSpace(c.Timing.Behavior)), strings.ToLower(strings.TrimSpace(c.Timing.Mode))
}

func visualClipSourceDuration(c Clip) float64 {
	if c.ActualLength > 0 {
		return c.ActualLength
	}
	if c.AI == nil {
		return 0
	}
	if c.AI.ActualDurationSeconds > 0 {
		return c.AI.ActualDurationSeconds
	}
	if c.AI.EstimatedDurationSeconds > 0 {
		return c.AI.EstimatedDurationSeconds
	}
	if c.AI.Duration > 0 {
		return float64(c.AI.Duration)
	}
	return 0
}

func audioCodecArgs(output Output) []string {
	switch strings.ToLower(strings.TrimSpace(output.Format)) {
	case "mp3":
		return []string{"-c:a", "libmp3lame", "-b:a", "192k"}
	case "wav":
		return []string{"-c:a", "pcm_s16le"}
	case "m4a", "aac":
		return []string{"-c:a", "aac", "-b:a", "192k"}
	default:
		return []string{"-c:a", "aac", "-b:a", "192k"}
	}
}

// buildDrawText returns a drawtext filter string for a single text
// overlay. Position is mapped to coordinates relative to (w, h).
// Body is escaped per ffmpeg's drawtext expression syntax — colon,
// backslash, and single-quote are the dangerous chars.
func buildDrawText(t *TextOver, w, h int) string {
	fs := t.FontSize
	if fs == 0 {
		fs = 32
	}
	color := t.Color
	if color == "" {
		color = "white"
	}
	var x, y string
	switch t.Position {
	case "top":
		x, y = "(w-text_w)/2", strconv.Itoa(h/24)
	case "center":
		x, y = "(w-text_w)/2", "(h-text_h)/2"
	default: // "bottom"
		x, y = "(w-text_w)/2", strconv.Itoa(h-h/8-fs)
	}
	return fmt.Sprintf(
		"drawtext=text='%s':fontfile='%s':fontsize=%d:fontcolor=%s:borderw=2:bordercolor=black@0.6:x=%s:y=%s",
		escDrawText(t.Body), composerFontFor(nil).Token, fs, color, x, y,
	)
}

func buildTimedDrawText(c Clip, w, h int) string {
	return buildTimedDrawTextWithBody(c, w, h, styledTextClipBody(c, true))
}

func buildTimedDrawTextChain(inputLabel, outLabel string, c Clip, w, h int) string {
	if !usesRevealText(c) {
		return fmt.Sprintf("[%s]%s[%s];", inputLabel, buildTimedDrawText(c, w, h), outLabel)
	}
	body := styledTextClipBody(c, true)
	steps := revealTextBodies(body, animationPreset(c.Animation.In))
	if len(steps) <= 1 {
		return fmt.Sprintf("[%s]%s[%s];", inputLabel, buildTimedDrawText(c, w, h), outLabel)
	}
	start := c.Start
	end := c.Start + clipDuration(c)
	revealDur := animationDuration(c.Animation.In, 1.2)
	if revealDur <= 0 {
		revealDur = 1.2
	}
	if revealDur > clipDuration(c) {
		revealDur = clipDuration(c)
	}
	stepDur := revealDur / float64(len(steps))
	var b strings.Builder
	prev := inputLabel
	for i, body := range steps {
		stepStart := start + float64(i)*stepDur
		stepEnd := start + float64(i+1)*stepDur
		if i == len(steps)-1 || stepEnd > end {
			stepEnd = end
		}
		if stepEnd <= stepStart {
			continue
		}
		next := outLabel
		if i < len(steps)-1 {
			next = fmt.Sprintf("%s_%d", outLabel, i)
		}
		cc := c
		cc.Start = stepStart
		cc.Length = stepEnd - stepStart
		cc.Animation = nil
		if i == len(steps)-1 && c.Animation != nil && c.Animation.Out != nil {
			cc.Animation = &Animation{Out: c.Animation.Out}
		}
		fmt.Fprintf(&b, "[%s]%s[%s];", prev, buildTimedDrawTextWithBody(cc, w, h, body), next)
		prev = next
	}
	if prev != outLabel {
		fmt.Fprintf(&b, "[%s]null[%s];", prev, outLabel)
	}
	return b.String()
}

func buildTimedDrawTextWithBody(c Clip, w, h int, body string) string {
	if c.Asset.Style != nil {
		switch strings.ToLower(strings.TrimSpace(c.Asset.Style.Transform)) {
		case "uppercase":
			body = strings.ToUpper(body)
		case "lowercase":
			body = strings.ToLower(body)
		}
	}
	fs := 48
	if c.Text != nil && c.Text.FontSize > 0 {
		fs = c.Text.FontSize
	}
	if c.Asset.Font != nil && c.Asset.Font.Size > 0 {
		fs = c.Asset.Font.Size
	}
	color := "#ffffff"
	if c.Text != nil && strings.TrimSpace(c.Text.Color) != "" {
		color = c.Text.Color
	}
	if c.Asset.Font != nil && strings.TrimSpace(c.Asset.Font.Color) != "" {
		color = c.Asset.Font.Color
	}
	color = ffmpegColorWithAlpha(color, textOpacity(c))
	borderW := 2
	borderColor := "black@0.7"
	if c.Asset.Stroke != nil {
		borderW = c.Asset.Stroke.Width
		if strings.TrimSpace(c.Asset.Stroke.Color) != "" {
			borderColor = ffmpegColorWithAlpha(c.Asset.Stroke.Color, c.Asset.Stroke.Opacity)
		}
	}
	x, y := drawTextXY(c, w, h, fs)
	if c.Animation != nil && c.Animation.In != nil {
		switch strings.ToLower(strings.TrimSpace(c.Animation.In.Preset)) {
		case "fade_up":
			y = timedMoveExpr(c.Start, animationDuration(c.Animation.In, 0.6), y, "30", "-")
		case "fade_down":
			y = timedMoveExpr(c.Start, animationDuration(c.Animation.In, 0.6), y, "30", "+")
		case "slide_left":
			x = timedMoveExpr(c.Start, animationDuration(c.Animation.In, 0.6), x, "120", "-")
		case "slide_right":
			x = timedMoveExpr(c.Start, animationDuration(c.Animation.In, 0.6), x, "120", "+")
		case "scale_pop":
			fs = int(float64(fs) * 1.04)
		}
	}
	parts := []string{
		fmt.Sprintf("drawtext=text='%s'", escDrawText(body)),
		fmt.Sprintf("fontfile='%s'", composerFontFor(c.Asset.Font).Token),
		fmt.Sprintf("fontsize=%d", fs),
		fmt.Sprintf("fontcolor=%s", color),
		fmt.Sprintf("borderw=%d", borderW),
		fmt.Sprintf("bordercolor=%s", borderColor),
		fmt.Sprintf("x=%s", escapeDrawTextExpr(x)),
		fmt.Sprintf("y=%s", escapeDrawTextExpr(y)),
		fmt.Sprintf("enable='%s'", escapeDrawTextExpr(fmt.Sprintf("between(t,%s,%s)", trimFloat(c.Start), trimFloat(c.Start+clipDuration(c))))),
	}
	if shadow := drawTextShadow(c.Asset.Shadow); shadow != "" {
		parts = append(parts, shadow)
	}
	if alpha := drawTextAlpha(c); alpha != "" {
		parts = append(parts, "alpha='"+escapeDrawTextExpr(alpha)+"'")
	}
	return strings.Join(parts, ":")
}

func styledTextClipBody(c Clip, trim bool) string {
	body := textClipBody(c)
	if trim {
		return strings.TrimSpace(body)
	}
	return strings.TrimRight(body, "\r\n")
}

func usesRevealText(c Clip) bool {
	if c.Animation == nil || c.Animation.In == nil {
		return false
	}
	switch animationPreset(c.Animation.In) {
	case "typewriter", "word_by_word":
		return animationDuration(c.Animation.In, 1.2) > 0
	default:
		return false
	}
}

func revealTextBodies(body, preset string) []string {
	switch preset {
	case "word_by_word":
		return revealTextBodiesByWord(body)
	default:
		return revealTextBodiesByRune(body)
	}
}

func revealTextBodiesByRune(body string) []string {
	total := visibleRuneCount(body)
	if total == 0 {
		return nil
	}
	step := 1
	if total > 120 {
		step = (total + 119) / 120
	}
	out := make([]string, 0, (total+step-1)/step)
	for n := step; n < total; n += step {
		out = append(out, revealBody(body, n))
	}
	out = append(out, revealBody(body, total))
	return out
}

func revealTextBodiesByWord(body string) []string {
	total := visibleRuneCount(body)
	if total == 0 {
		return nil
	}
	var stops []int
	visible := 0
	inWord := false
	for _, r := range body {
		if r == '\n' || r == '\r' {
			continue
		}
		visible++
		if r == ' ' || r == '\t' {
			if inWord {
				stops = append(stops, visible-1)
			}
			inWord = false
			continue
		}
		inWord = true
	}
	if inWord {
		stops = append(stops, total)
	}
	if len(stops) == 0 || stops[len(stops)-1] != total {
		stops = append(stops, total)
	}
	out := make([]string, 0, len(stops))
	for _, n := range stops {
		if n > 0 {
			out = append(out, revealBody(body, n))
		}
	}
	return out
}

func visibleRuneCount(body string) int {
	n := 0
	for _, r := range body {
		if r != '\n' && r != '\r' {
			n++
		}
	}
	return n
}

func revealBody(body string, revealCount int) string {
	var b strings.Builder
	visible := 0
	for _, r := range body {
		switch r {
		case '\n', '\r':
			b.WriteRune(r)
		default:
			visible++
			if visible <= revealCount {
				b.WriteRune(r)
			} else {
				b.WriteByte(' ')
			}
		}
	}
	return b.String()
}

func textOpacity(c Clip) float64 {
	if c.Asset.Font == nil || c.Asset.Font.Opacity <= 0 {
		return 1
	}
	return c.Asset.Font.Opacity
}

func drawTextShadow(s *TextShadow) string {
	if s == nil {
		return ""
	}
	color := s.Color
	if color == "" {
		color = "black"
	}
	opacity := s.Opacity
	if opacity <= 0 {
		opacity = 0.65
	}
	x := s.OffsetX
	y := s.OffsetY
	if x == 0 && y == 0 {
		x, y = 2, 2
	}
	return fmt.Sprintf("shadowcolor=%s:shadowx=%d:shadowy=%d", ffmpegColorWithAlpha(color, opacity), x, y)
}

func drawTextXY(c Clip, w, h, fs int) (string, string) {
	if c.Position != nil && (strings.TrimSpace(c.Position.X) != "" || strings.TrimSpace(c.Position.Y) != "") {
		x := positionExpr(c.Position.X, "50%", "w", "text_w")
		y := positionExpr(c.Position.Y, "50%", "h", "text_h")
		switch strings.ToLower(strings.TrimSpace(c.Position.Anchor)) {
		case "top-left":
		case "top":
			x = "(" + x + ")-text_w/2"
		case "top-right":
			x = "(" + x + ")-text_w"
		case "left":
			y = "(" + y + ")-text_h/2"
		case "right":
			x = "(" + x + ")-text_w"
			y = "(" + y + ")-text_h/2"
		case "bottom-left":
			y = "(" + y + ")-text_h"
		case "bottom":
			x = "(" + x + ")-text_w/2"
			y = "(" + y + ")-text_h"
		case "bottom-right":
			x = "(" + x + ")-text_w"
			y = "(" + y + ")-text_h"
		default:
			x = "(" + x + ")-text_w/2"
			y = "(" + y + ")-text_h/2"
		}
		return x, y
	}
	hAlign, vAlign := "center", "bottom"
	if c.Text != nil && c.Text.Position != "" {
		vAlign = c.Text.Position
	}
	if c.Asset.Align != nil {
		if c.Asset.Align.Horizontal != "" {
			hAlign = c.Asset.Align.Horizontal
		}
		if c.Asset.Align.Vertical != "" {
			vAlign = c.Asset.Align.Vertical
		}
	}
	var x string
	switch hAlign {
	case "left":
		x = strconv.Itoa(w / 14)
	case "right":
		x = "w-text_w-" + strconv.Itoa(w/14)
	default:
		x = "(w-text_w)/2"
	}
	var y string
	switch vAlign {
	case "top":
		y = strconv.Itoa(h / 16)
	case "center":
		y = "(h-text_h)/2"
	default:
		y = strconv.Itoa(h - h/8 - fs)
	}
	return x, y
}

func positionExpr(value, fallback, axis, size string) string {
	v := strings.TrimSpace(value)
	if v == "" {
		v = fallback
	}
	if strings.HasSuffix(v, "%") {
		n := strings.TrimSuffix(v, "%")
		if n == "" {
			n = "0"
		}
		return axis + "*" + n + "/100"
	}
	return v
}

func drawTextAlpha(c Clip) string {
	if c.Animation == nil {
		return ""
	}
	start := c.Start
	end := c.Start + clipDuration(c)
	inDur := animationDuration(c.Animation.In, 0)
	outDur := animationDuration(c.Animation.Out, 0)
	inPreset := animationPreset(c.Animation.In)
	outPreset := animationPreset(c.Animation.Out)
	if inDur <= 0 && outDur <= 0 {
		return ""
	}
	base := "1"
	if inDur > 0 && (inPreset == "fade" || inPreset == "fade_up" || inPreset == "fade_down" || inPreset == "slide_left" || inPreset == "slide_right" || inPreset == "scale_pop" || inPreset == "typewriter" || inPreset == "word_by_word") {
		base = fmt.Sprintf("if(lt(t,%s),0,if(lt(t,%s),(t-%s)/%s,1))", trimFloat(start), trimFloat(start+inDur), trimFloat(start), trimFloat(inDur))
	}
	if outDur > 0 && (outPreset == "fade" || outPreset == "fade_up" || outPreset == "fade_down" || outPreset == "slide_left" || outPreset == "slide_right") {
		outStart := end - outDur
		base = fmt.Sprintf("(%s)*if(lt(t,%s),1,if(lt(t,%s),(%s-t)/%s,0))", base, trimFloat(outStart), trimFloat(end), trimFloat(end), trimFloat(outDur))
	}
	return base
}

func animationPreset(p *AnimationPreset) string {
	if p == nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(p.Preset))
}

func animationDuration(p *AnimationPreset, fallback float64) float64 {
	if p == nil {
		return 0
	}
	if p.Duration > 0 {
		return p.Duration
	}
	if animationPreset(p) != "" && animationPreset(p) != "none" {
		return fallback
	}
	return 0
}

func timedMoveExpr(start, dur float64, base, distance, direction string) string {
	if dur <= 0 {
		return base
	}
	if direction == "+" {
		return fmt.Sprintf("if(lt(t,%s),(%s)+%s,if(lt(t,%s),(%s)+%s*(1-(t-%s)/%s),%s))",
			trimFloat(start), base, distance, trimFloat(start+dur), base, distance, trimFloat(start), trimFloat(dur), base)
	}
	return fmt.Sprintf("if(lt(t,%s),(%s)-%s,if(lt(t,%s),(%s)-%s*(1-(t-%s)/%s),%s))",
		trimFloat(start), base, distance, trimFloat(start+dur), base, distance, trimFloat(start), trimFloat(dur), base)
}

func ffmpegColorWithAlpha(color string, alpha float64) string {
	color = strings.TrimSpace(color)
	if color == "" {
		color = "white"
	}
	if alpha <= 0 || alpha > 1 {
		alpha = 1
	}
	if strings.HasPrefix(color, "#") {
		color = "0x" + color[1:]
	}
	if alpha < 1 {
		return fmt.Sprintf("%s@%s", color, trimFloat(alpha))
	}
	return color
}

func escapeDrawTextExpr(s string) string {
	return strings.NewReplacer(",", `\,`, ":", `\:`, "'", `\'`).Replace(s)
}

// escDrawText escapes the drawtext expression body. ffmpeg's drawtext
// uses colons + single quotes + backslash with special meanings —
// reject them via simple escaping.
func escDrawText(s string) string {
	r := strings.NewReplacer(
		`\`, `\\`,
		`:`, `\:`,
		`'`, `\'`,
		"\n", " ",
	)
	return r.Replace(s)
}

// escFFmpegColor returns a color value the pad filter accepts. Empty
// or invalid → "black".
func escFFmpegColor(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "black"
	}
	// hex without leading 0x; allow either #rrggbb or rrggbb.
	if strings.HasPrefix(s, "#") {
		return "0x" + s[1:]
	}
	return s
}

// trimFloat formats a float with up to 3 decimals, no trailing zeros.
func trimFloat(f float64) string {
	s := strconv.FormatFloat(f, 'f', 3, 64)
	s = strings.TrimRight(s, "0")
	s = strings.TrimRight(s, ".")
	if s == "" {
		s = "0"
	}
	return s
}

// shellEcho returns a printable, single-quoted representation of the
// command for logging — NOT for re-execution (we don't quote-escape
// embedded single quotes the bash-strict way).
func shellEcho(bin string, args []string) string {
	var b strings.Builder
	b.WriteString(bin)
	for _, a := range args {
		b.WriteByte(' ')
		if strings.ContainsAny(a, " \t\"'$&|<>;()`\\") {
			b.WriteByte('\'')
			b.WriteString(strings.ReplaceAll(a, "'", `'\''`))
			b.WriteByte('\'')
		} else {
			b.WriteString(a)
		}
	}
	return b.String()
}

func truncTail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}
