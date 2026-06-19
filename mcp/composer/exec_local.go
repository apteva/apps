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
			url, err := resolveAssetURL(app, c.Asset.Src)
			if err != nil {
				cleanup()
				return Result{}, fmt.Errorf("audio clip[%d]: resolve %q: %w", i, c.Asset.Src, err)
			}
			inputs = append(inputs, url)
		}
		soundtrackIdx := -1
		if s := edit.Timeline.Soundtrack; s != nil {
			url, err := resolveAssetURL(app, s.Src)
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
	for i, c := range visual.Clips {
		url, err := resolveAssetURL(app, c.Asset.Src)
		if err != nil {
			cleanup()
			return Result{}, fmt.Errorf("visual clip[%d]: resolve %q: %w", i, c.Asset.Src, err)
		}
		inputs = append(inputs, url)
	}
	for i, c := range audioClips {
		if clipAssetType(c, "audio") == "silence" {
			continue
		}
		url, err := resolveAssetURL(app, c.Asset.Src)
		if err != nil {
			cleanup()
			return Result{}, fmt.Errorf("audio clip[%d]: resolve %q: %w", i, c.Asset.Src, err)
		}
		inputs = append(inputs, url)
	}
	var soundtrackIdx int = -1
	if s := edit.Timeline.Soundtrack; s != nil {
		url, err := resolveAssetURL(app, s.Src)
		if err != nil {
			cleanup()
			return Result{}, fmt.Errorf("soundtrack resolve %q: %w", s.Src, err)
		}
		soundtrackIdx = len(inputs)
		inputs = append(inputs, url)
	}

	outFile := filepath.Join(scratch, "out."+output.Format)
	args := buildLocalFFmpegArgs(edit, output, inputs, soundtrackIdx, outFile)

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
			fmt.Fprintf(&filter, ",tpad=stop_mode=clone:stop_duration=%s,trim=duration=%s,setpts=PTS-STARTPTS", d, d)
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
		if clipAssetType(c, "visual") == "image" {
			// Synthesize silent audio for image clips so concat audio
			// stream count matches.
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
	filter.WriteString("[vcat]null[vout]")

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

func buildLocalAudioFFmpegArgs(edit *Edit, output Output, inputs []string, soundtrackIdx int, outFile string) []string {
	audioClips := audioTimelineClips(edit)
	args := []string{"-y", "-loglevel", "error"}
	for _, src := range inputs {
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
