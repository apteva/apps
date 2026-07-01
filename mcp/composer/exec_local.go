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
	inputs := make([]string, 0, len(visual.Clips)+len(audioClips)+1)
	visualHasAudio := make([]bool, len(visual.Clips))
	for i, c := range visual.Clips {
		url, err := resolveAssetLocal(app, c.Asset.Src)
		if err != nil {
			cleanup()
			return Result{}, fmt.Errorf("visual clip[%d]: resolve %q: %w", i, c.Asset.Src, err)
		}
		visualHasAudio[i] = visualClipMayUseSourceAudio(c) && probeMediaHasAudio(url)
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

	args := []string{"-y", "-loglevel", "error"}

	// One -i per input.
	for i, src := range inputs {
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
		// Scale + pad to output dims, set fps, set SAR=1.
		fmt.Fprintf(&filter,
			"[%d:v]scale=%d:%d:force_original_aspect_ratio=decrease,"+
				"pad=%d:%d:(ow-iw)/2:(oh-ih)/2:color=%s,"+
				"setsar=1,fps=%d",
			i, w, h, w, h, escFFmpegColor(edit.Timeline.Background), output.FPS,
		)
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
			fmt.Fprintf(&filter, "[%d:a]apad,atrim=duration=%s,asetpts=PTS-STARTPTS[a%d];", i, trimFloat(clipDuration(c)), i)
		}
	}

	// concat all clips
	n := len(track.Clips)
	for i := 0; i < n; i++ {
		fmt.Fprintf(&filter, "[v%d][a%d]", i, i)
	}
	fmt.Fprintf(&filter, "concat=n=%d:v=1:a=1[vcat][acat];", n)

	mixLabels := []string{"[acat]"}
	audioInputCursor := visualCount
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
	videoLabel := "vcat"
	for i, c := range textOverlayClips(edit) {
		outLabel := fmt.Sprintf("vtxt%d", i)
		fmt.Fprintf(&filter, "[%s]%s[%s];", videoLabel, buildTimedDrawText(c, w, h), outLabel)
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
		"drawtext=text='%s':fontsize=%d:fontcolor=%s:borderw=2:bordercolor=black@0.6:x=%s:y=%s",
		escDrawText(t.Body), fs, color, x, y,
	)
}

func buildTimedDrawText(c Clip, w, h int) string {
	body := strings.TrimSpace(textClipBody(c))
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
	if c.Asset.Font != nil && strings.TrimSpace(c.Asset.Font.Family) != "" {
		parts = append(parts, "font='"+escDrawText(c.Asset.Font.Family)+"'")
	}
	return strings.Join(parts, ":")
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
