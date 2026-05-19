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
//   1. resolves every asset.src to a URL ffmpeg can fetch over HTTPS
//      (signed if storage gates it).
//   2. assembles a filter_complex from the canonical Edit.
//   3. spawns ffmpeg in a per-render scratch dir.
//   4. returns the absolute path to the output file; caller stores it.
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
	// Keep on failure for post-mortem; clean on success below.
	defer func() {
		if err == nil {
			_ = os.RemoveAll(scratch)
		} else {
			app.Logger().Warn("kept scratch dir for post-mortem", "path", scratch, "err", err)
		}
	}()

	track := edit.Timeline.Tracks[0]

	// Resolve every clip's asset to a URL. ffmpeg accepts https:// inputs
	// natively (movflags+frag work); no need to download first.
	inputs := make([]string, 0, len(track.Clips)+1)
	for i, c := range track.Clips {
		url, err := resolveAssetURL(app, c.Asset.Src)
		if err != nil {
			return Result{}, fmt.Errorf("clip[%d]: resolve %q: %w", i, c.Asset.Src, err)
		}
		inputs = append(inputs, url)
	}
	var soundtrackIdx int = -1
	if s := edit.Timeline.Soundtrack; s != nil {
		url, err := resolveAssetURL(app, s.Src)
		if err != nil {
			return Result{}, fmt.Errorf("soundtrack resolve %q: %w", s.Src, err)
		}
		soundtrackIdx = len(inputs)
		inputs = append(inputs, url)
	}

	outFile := filepath.Join(scratch, "out."+output.Format)
	args := buildLocalFFmpegArgs(edit, output, inputs, soundtrackIdx, outFile)

	app.Logger().Info("local ffmpeg render", "scratch", scratch, "inputs", len(inputs), "out", outFile)

	cmd := exec.CommandContext(ctx, ffmpegPath(), args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err = cmd.Run(); err != nil {
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
	track := edit.Timeline.Tracks[0]

	args := []string{"-y", "-loglevel", "error"}

	// One -i per input.
	for i, src := range inputs {
		// Images need -loop 1 + -t to behave as fixed-length stills.
		if i < len(track.Clips) && track.Clips[i].Asset.Type == "image" {
			args = append(args,
				"-loop", "1",
				"-t", trimFloat(track.Clips[i].Length),
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
		if c.Asset.Type != "image" {
			fmt.Fprintf(&filter, ",trim=duration=%s,setpts=PTS-STARTPTS", trimFloat(c.Length))
		}
		// Optional fade in/out within the clip.
		if c.Transition != nil {
			if c.Transition.In == "fade" {
				filter.WriteString(",fade=t=in:st=0:d=0.3")
			}
			if c.Transition.Out == "fade" {
				fmt.Fprintf(&filter, ",fade=t=out:st=%s:d=0.3", trimFloat(c.Length-0.3))
			}
		}
		// Optional text overlay (drawtext).
		if c.Text != nil && strings.TrimSpace(c.Text.Body) != "" {
			filter.WriteString(",")
			filter.WriteString(buildDrawText(c.Text, w, h))
		}
		fmt.Fprintf(&filter, "[v%d];", i)

		// Per-clip audio: trim or silence-pad to clip length.
		if c.Asset.Type == "image" {
			// Synthesize silent audio for image clips so concat audio
			// stream count matches.
			fmt.Fprintf(&filter, "anullsrc=channel_layout=stereo:sample_rate=44100,atrim=duration=%s[a%d];", trimFloat(c.Length), i)
		} else {
			fmt.Fprintf(&filter, "[%d:a]atrim=duration=%s,asetpts=PTS-STARTPTS[a%d];", i, trimFloat(c.Length), i)
		}
	}

	// concat all clips
	n := len(track.Clips)
	for i := 0; i < n; i++ {
		fmt.Fprintf(&filter, "[v%d][a%d]", i, i)
	}
	fmt.Fprintf(&filter, "concat=n=%d:v=1:a=1[vcat][acat];", n)

	// Soundtrack overlay
	if soundtrackIdx >= 0 {
		vol := 1.0
		if v := edit.Timeline.Soundtrack.Volume; v > 0 {
			vol = v
		}
		fmt.Fprintf(&filter,
			"[%d:a]volume=%g,atrim=duration=%s[snd];[acat][snd]amix=inputs=2:duration=longest:normalize=0[aout]",
			soundtrackIdx, vol, trimFloat(editDurationSeconds(edit)),
		)
		filter.WriteString(";[vcat]null[vout]")
	} else {
		filter.WriteString("[vcat]null[vout];[acat]anull[aout]")
	}

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
