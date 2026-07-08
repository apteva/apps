package main

import (
	"context"
	"fmt"
	"image"
	"image/color"
	stddraw "image/draw"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

type v2NativeRender struct {
	spec     *V2Composition
	output   Output
	width    int
	height   int
	designW  int
	designH  int
	scaleX   float64
	scaleY   float64
	scale    float64
	fps      int
	duration float64
	assets   map[string]V2Asset
	images   map[string]image.Image
	faces    map[string]font.Face
	regular  font.Face
	bold     font.Face
}

func renderV2Native(ctx context.Context, app *sdk.AppCtx, spec *V2Composition, projectID string) (Result, []string, error) {
	start := time.Now()
	if err := validateV2Composition(spec); err != nil {
		return Result{}, nil, err
	}
	out := v2OutputToOutput(spec.Output)
	if out.Format != "mp4" {
		return Result{}, nil, fmt.Errorf("native composer/v2 renderer supports mp4 output, got %q", out.Format)
	}
	if v2HasVideoElements(spec) {
		return Result{}, nil, errorsf("native composer/v2 renderer supports image/shape/text elements first; video elements still use the ffmpeg compatibility path")
	}
	w, h := spec.Output.Width, spec.Output.Height
	if w <= 0 || h <= 0 {
		w, h = resolutionWH(out.Resolution, out.Aspect)
	}
	designW, designH := spec.Output.DesignWidth, spec.Output.DesignHeight
	if designW <= 0 {
		designW = w
	}
	if designH <= 0 {
		designH = h
	}
	scaleX, scaleY := 1.0, 1.0
	if designW > 0 {
		scaleX = float64(w) / float64(designW)
	}
	if designH > 0 {
		scaleY = float64(h) / float64(designH)
	}
	styleScale := math.Min(scaleX, scaleY)
	if styleScale <= 0 {
		styleScale = 1
	}
	fps := spec.Output.FPS
	if fps <= 0 {
		fps = out.FPS
	}
	if fps <= 0 {
		fps = 30
	}
	duration := v2DurationSeconds(spec)
	if duration <= 0 {
		return Result{}, nil, fmt.Errorf("composer/v2 duration must be > 0")
	}

	scratch, err := os.MkdirTemp("", "composer-v2-render-")
	if err != nil {
		return Result{}, nil, fmt.Errorf("scratch dir: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(scratch) }
	framesDir := filepath.Join(scratch, "frames")
	if err := os.MkdirAll(framesDir, 0o755); err != nil {
		cleanup()
		return Result{}, nil, err
	}

	r := &v2NativeRender{
		spec:     spec,
		output:   out,
		width:    w,
		height:   h,
		designW:  designW,
		designH:  designH,
		scaleX:   scaleX,
		scaleY:   scaleY,
		scale:    styleScale,
		fps:      fps,
		duration: duration,
		assets:   map[string]V2Asset{},
		images:   map[string]image.Image{},
		faces:    map[string]font.Face{},
		regular:  loadFontFace(false, 36),
		bold:     loadFontFace(true, 36),
	}
	for _, asset := range spec.Assets {
		r.assets[asset.ID] = asset
	}
	if err := r.loadImages(app); err != nil {
		cleanup()
		return Result{}, nil, err
	}

	frameCount := int(math.Ceil(duration * float64(fps)))
	if frameCount < 1 {
		frameCount = 1
	}
	if app != nil {
		app.Logger().Info("native composer/v2 render", "scratch", scratch, "frames", frameCount, "width", w, "height", h, "fps", fps)
	}
	for i := 0; i < frameCount; i++ {
		t := float64(i) / float64(fps)
		img := r.renderFrame(t)
		framePath := filepath.Join(framesDir, fmt.Sprintf("frame_%06d.jpg", i+1))
		f, err := os.Create(framePath)
		if err != nil {
			cleanup()
			return Result{}, nil, err
		}
		if err := jpeg.Encode(f, img, &jpeg.Options{Quality: 92}); err != nil {
			_ = f.Close()
			cleanup()
			return Result{}, nil, err
		}
		if err := f.Close(); err != nil {
			cleanup()
			return Result{}, nil, err
		}
	}

	outFile := filepath.Join(scratch, "out."+out.Format)
	args, err := buildV2NativeFFmpegArgs(app, spec, out, projectID, filepath.Join(framesDir, "frame_%06d.jpg"), outFile, duration, fps)
	if err != nil {
		cleanup()
		return Result{}, nil, err
	}
	cmd := exec.CommandContext(ctx, ffmpegPath(), args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		app.Logger().Warn("kept native v2 scratch dir for post-mortem", "path", scratch, "err", err)
		return Result{FFmpegCommand: shellEcho(ffmpegPath(), args)}, nil, fmt.Errorf("ffmpeg failed: %w\nstderr (last 1KB):\n%s", err, truncTail(stderr.String(), 1024))
	}
	return Result{
		Sync:          true,
		LocalPath:     outFile,
		Cleanup:       cleanup,
		DurationMS:    time.Since(start).Milliseconds(),
		FFmpegCommand: shellEcho(ffmpegPath(), args),
	}, []string{"native composer/v2 renderer used for image/shape/text scene graph"}, nil
}

func v2HasVideoElements(spec *V2Composition) bool {
	for _, scene := range spec.Scenes {
		for _, el := range scene.Elements {
			if el.Type == "video" {
				return true
			}
		}
	}
	for _, track := range spec.Tracks {
		if track.Type == "video" {
			return true
		}
		for _, clip := range track.Clips {
			if clip.Type == "video" {
				return true
			}
		}
	}
	return false
}

func (r *v2NativeRender) loadImages(app *sdk.AppCtx) error {
	for _, asset := range r.assets {
		if asset.Type != "image" {
			continue
		}
		img, err := loadImageAsset(app, asset.Src)
		if err != nil {
			return fmt.Errorf("asset %q: %w", asset.ID, err)
		}
		r.images[asset.ID] = img
	}
	for _, scene := range r.spec.Scenes {
		for _, el := range scene.Elements {
			if el.Type != "image" || el.Asset != "" {
				continue
			}
			src := strings.TrimSpace(el.Src)
			if src == "" || r.images[src] != nil {
				continue
			}
			img, err := loadImageAsset(app, src)
			if err != nil {
				return fmt.Errorf("image element %q: %w", el.ID, err)
			}
			r.images[src] = img
		}
	}
	return nil
}

func loadImageAsset(app *sdk.AppCtx, src string) (image.Image, error) {
	resolved, err := resolveAssetLocal(app, src)
	if err != nil {
		return nil, err
	}
	if strings.HasPrefix(resolved, "http://") || strings.HasPrefix(resolved, "https://") {
		resp, err := http.Get(resolved)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, fmt.Errorf("download status %d", resp.StatusCode)
		}
		img, _, err := image.Decode(resp.Body)
		return img, err
	}
	f, err := os.Open(resolved)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	return img, err
}

func (r *v2NativeRender) renderFrame(t float64) *image.RGBA {
	bg := parseColor(firstNonEmpty(r.spec.Output.Background, r.spec.Background, "#000000"), color.RGBA{0, 0, 0, 255})
	canvas := image.NewRGBA(image.Rect(0, 0, r.width, r.height))
	stddraw.Draw(canvas, canvas.Bounds(), &image.Uniform{bg}, image.Point{}, stddraw.Src)
	for _, item := range r.activeScenes(t) {
		scene, local := item.scene, item.local
		if scene.Background != "" {
			fill := image.NewRGBA(canvas.Bounds())
			stddraw.Draw(fill, fill.Bounds(), &image.Uniform{parseColor(scene.Background, bg)}, image.Point{}, stddraw.Src)
			composite(canvas, fill, 1)
		}
		elements := map[string]V2Element{}
		for _, el := range scene.Elements {
			if el.ID != "" {
				elements[el.ID] = el
			}
		}
		for _, el := range scene.Elements {
			start := el.Start
			dur := el.Duration
			if dur <= 0 {
				dur = scene.Duration - start
			}
			if local+0.0001 < start || local > start+dur+0.0001 {
				continue
			}
			r.drawElement(canvas, el, elements, local, scene.Duration)
		}
	}
	return canvas
}

type activeV2Scene struct {
	scene V2Scene
	local float64
}

func (r *v2NativeRender) activeScenes(t float64) []activeV2Scene {
	var out []activeV2Scene
	cursor := 0.0
	for _, scene := range r.spec.Scenes {
		start := scene.Start
		if start <= 0 {
			start = cursor
		}
		end := start + scene.Duration
		if t+0.0001 >= start && t < end+0.0001 {
			out = append(out, activeV2Scene{scene: scene, local: t - start})
		}
		cursor = end
	}
	return out
}

func (r *v2NativeRender) drawElement(dst *image.RGBA, el V2Element, elements map[string]V2Element, sceneLocal, sceneDuration float64) {
	t := sceneLocal - el.Start
	duration := el.Duration
	if duration <= 0 {
		duration = sceneDuration - el.Start
	}
	box := r.elementBox(el)
	if el.Parent != "" {
		if parent, ok := elements[el.Parent]; ok {
			parentBox := r.elementBox(parent)
			parentDuration := parent.Duration
			if parentDuration <= 0 {
				parentDuration = sceneDuration - parent.Start
			}
			parentMotion := r.elementMotion(parent, sceneLocal-parent.Start, parentDuration)
			box = transformBoxAround(box, parentBox, parentMotion.xOff, parentMotion.yOff, parentMotion.scale)
		}
	}
	state := r.elementState(el, box, t, duration)
	if el.Parent != "" {
		if parent, ok := elements[el.Parent]; ok {
			parentDuration := parent.Duration
			if parentDuration <= 0 {
				parentDuration = sceneDuration - parent.Start
			}
			parentMotion := r.elementMotion(parent, sceneLocal-parent.Start, parentDuration)
			state.opacity *= parentMotion.opacity
		}
	}
	if state.opacity <= 0 {
		return
	}
	box = state.box
	switch el.Type {
	case "shape":
		r.drawShape(dst, el, box, state.opacity)
	case "image":
		r.drawImage(dst, el, box, state.opacity)
	case "text":
		r.drawText(dst, el, box, state.opacity, t)
	case "group":
		return
	}
}

type v2ElementState struct {
	box     image.Rectangle
	opacity float64
}

type v2ElementMotion struct {
	xOff    float64
	yOff    float64
	scale   float64
	opacity float64
}

func (r *v2NativeRender) elementState(el V2Element, box image.Rectangle, t, duration float64) v2ElementState {
	motion := r.elementMotion(el, t, duration)
	box = transformBox(box, motion.xOff, motion.yOff, motion.scale)
	return v2ElementState{box: box, opacity: clamp01(motion.opacity)}
}

func (r *v2NativeRender) elementMotion(el V2Element, t, duration float64) v2ElementMotion {
	opacity := styleFloat(el.Style, "opacity", 1)
	xOff, yOff := 0.0, 0.0
	scale := 1.0
	applyPreset := func(m map[string]any, entering bool) {
		if m == nil {
			return
		}
		kind := strings.ToLower(strings.TrimSpace(mapString(m, "type", mapString(m, "preset", ""))))
		d := mapFloat(m, "duration", 0.6)
		if d <= 0 {
			d = 0.6
		}
		delay := mapFloat(m, "delay", 0)
		var p float64
		if entering {
			p = clamp01((t - delay) / d)
		} else {
			p = clamp01((duration - t - delay) / d)
		}
		eased := easeOutCubic(p)
		switch kind {
		case "fade":
			opacity *= eased
		case "fade_up":
			opacity *= eased
			yOff += (1 - eased) * 48 * r.scaleY
		case "fade_down":
			opacity *= eased
			yOff -= (1 - eased) * 48 * r.scaleY
		case "slide_left":
			opacity *= eased
			xOff += (1 - eased) * 140 * r.scaleX
		case "slide_right":
			opacity *= eased
			xOff -= (1 - eased) * 140 * r.scaleX
		case "scale_pop":
			opacity *= eased
			scale *= 0.88 + 0.12*eased
		}
	}
	applyPreset(el.Enter, true)
	applyPreset(el.Exit, false)
	if el.Animate != nil {
		opacity = applyKeyframe(el.Animate, "opacity", t, opacity)
		xOff += applyKeyframe(el.Animate, "x", t, 0) * r.scaleX
		yOff += applyKeyframe(el.Animate, "y", t, 0) * r.scaleY
		scale *= applyKeyframe(el.Animate, "scale", t, 1)
	}
	return v2ElementMotion{xOff: xOff, yOff: yOff, scale: scale, opacity: clamp01(opacity)}
}

func (r *v2NativeRender) drawShape(dst *image.RGBA, el V2Element, box image.Rectangle, opacity float64) {
	fill := parseColor(styleString(el.Style, "fill", styleString(el.Style, "background", "#ffffff")), color.RGBA{255, 255, 255, 255})
	stroke := parseColor(styleString(el.Style, "stroke", ""), color.RGBA{})
	radius := int(styleFloat(el.Style, "radius", 0) * r.scale)
	strokeW := int(styleFloat(el.Style, "stroke_width", styleFloat(el.Style, "strokeWidth", 0)) * r.scale)
	layer := image.NewRGBA(dst.Bounds())
	if strokeW > 0 && stroke.A > 0 {
		fillRoundRect(layer, box, radius, stroke)
		inner := image.Rect(box.Min.X+strokeW, box.Min.Y+strokeW, box.Max.X-strokeW, box.Max.Y-strokeW)
		fillRoundRect(layer, inner, maxInt(0, radius-strokeW), fill)
	} else {
		fillRoundRect(layer, box, radius, fill)
	}
	compositeRect(dst, layer, box, opacity)
}

func (r *v2NativeRender) drawImage(dst *image.RGBA, el V2Element, box image.Rectangle, opacity float64) {
	var img image.Image
	if el.Asset != "" {
		img = r.images[el.Asset]
	} else {
		img = r.images[strings.TrimSpace(el.Src)]
	}
	if img == nil {
		return
	}
	fit := strings.ToLower(firstNonEmpty(el.Fit, styleString(el.Style, "fit", "cover")))
	src := img.Bounds()
	sw, sh := src.Dx(), src.Dy()
	dw, dh := box.Dx(), box.Dy()
	if sw <= 0 || sh <= 0 || dw <= 0 || dh <= 0 {
		return
	}
	scale := math.Max(float64(dw)/float64(sw), float64(dh)/float64(sh))
	if fit == "contain" {
		scale = math.Min(float64(dw)/float64(sw), float64(dh)/float64(sh))
	}
	tw, th := int(math.Round(float64(sw)*scale)), int(math.Round(float64(sh)*scale))
	target := image.Rect(box.Min.X+(dw-tw)/2, box.Min.Y+(dh-th)/2, box.Min.X+(dw-tw)/2+tw, box.Min.Y+(dh-th)/2+th)
	layer := image.NewRGBA(dst.Bounds())
	xdraw.CatmullRom.Scale(layer, target, img, src, stddraw.Over, nil)
	compositeRect(dst, layer, box, opacity)
}

func (r *v2NativeRender) drawText(dst *image.RGBA, el V2Element, box image.Rectangle, opacity float64, t float64) {
	size := styleFloat(el.Style, "font_size", styleFloat(el.Style, "fontSize", 48)) * r.scale
	if size < 1 {
		size = 1
	}
	face := r.fontFace(styleBool(el.Style, "bold", styleFloat(el.Style, "weight", 400) >= 700), size)
	col := parseColor(styleString(el.Style, "color", "#ffffff"), color.RGBA{255, 255, 255, 255})
	align := strings.ToLower(styleString(el.Style, "align", "left"))
	fullLines := wrapText(el.Text, face, box.Dx())
	lines := fullLines
	if reveal := strings.ToLower(mapString(el.Enter, "type", "")); reveal == "typewriter" || reveal == "word_by_word" {
		delay := mapFloat(el.Enter, "delay", 0)
		lines = revealTextLines(fullLines, t-delay, mapFloat(el.Enter, "duration", 1.2), reveal)
	}
	lineH := int(size * 1.22)
	totalH := lineH * len(fullLines)
	y := box.Min.Y + (box.Dy()-totalH)/2 + int(size)
	if strings.ToLower(styleString(el.Style, "vertical_align", "")) == "top" {
		y = box.Min.Y + int(size)
	}
	layer := image.NewRGBA(dst.Bounds())
	d := &font.Drawer{Dst: layer, Src: image.NewUniform(col), Face: face}
	for i, line := range lines {
		measureLine := line
		if i < len(fullLines) {
			measureLine = fullLines[i]
		}
		lineW := d.MeasureString(measureLine).Ceil()
		x := box.Min.X
		switch align {
		case "center":
			x = box.Min.X + (box.Dx()-lineW)/2
		case "right":
			x = box.Max.X - lineW
		}
		d.Dot = fixed.P(x, y)
		d.DrawString(line)
		y += lineH
	}
	compositeRect(dst, layer, box, opacity)
}

func (r *v2NativeRender) fontFace(bold bool, size float64) font.Face {
	key := fmt.Sprintf("%t:%0.2f", bold, size)
	if face := r.faces[key]; face != nil {
		return face
	}
	face := loadFontFace(bold, size)
	r.faces[key] = face
	return face
}

func buildV2NativeFFmpegArgs(app *sdk.AppCtx, spec *V2Composition, output Output, projectID, framePattern, outFile string, duration float64, fps int) ([]string, error) {
	assets := map[string]V2Asset{}
	for _, asset := range spec.Assets {
		assets[asset.ID] = asset
	}
	audioTrack, hasAudioTrack, err := v2AudioTrack(spec, assets)
	if err != nil {
		return nil, err
	}
	soundtrack, hasSoundtrack, err := v2Soundtrack(spec, assets)
	if err != nil {
		return nil, err
	}
	args := []string{"-y", "-loglevel", "error", "-framerate", strconv.Itoa(fps), "-i", framePattern}
	inputs := 1
	for _, c := range audioTrack.Clips {
		if clipAssetType(c, "audio") == "silence" {
			continue
		}
		url, err := resolveAssetLocal(app, c.Asset.Src)
		if err != nil {
			return nil, err
		}
		args = append(args, "-i", url)
		inputs++
	}
	soundtrackIdx := -1
	if hasSoundtrack {
		url, err := resolveAssetLocal(app, soundtrack.Src)
		if err != nil {
			return nil, err
		}
		if soundtrackLoops(soundtrack) {
			args = append(args, "-stream_loop", "-1")
		}
		soundtrackIdx = inputs
		args = append(args, "-i", url)
	}
	var filter strings.Builder
	mixLabels := []string{}
	inputCursor := 1
	if hasAudioTrack {
		for i, c := range audioTrack.Clips {
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
	}
	if hasSoundtrack {
		vol := soundtrack.Volume
		if vol <= 0 {
			vol = 1
		}
		fmt.Fprintf(&filter, "[%d:a]volume=%g,atrim=duration=%s[snd];", soundtrackIdx, vol, trimFloat(duration))
		mixLabels = append(mixLabels, "[snd]")
	}
	if len(mixLabels) == 0 {
		fmt.Fprintf(&filter, "anullsrc=channel_layout=stereo:sample_rate=44100,atrim=duration=%s[aout]", trimFloat(duration))
	} else if len(mixLabels) == 1 {
		fmt.Fprintf(&filter, "%sanull[aout]", mixLabels[0])
	} else {
		for _, label := range mixLabels {
			filter.WriteString(label)
		}
		fmt.Fprintf(&filter, "amix=inputs=%d:duration=longest:normalize=0,atrim=duration=%s[aout]", len(mixLabels), trimFloat(duration))
	}
	args = append(args,
		"-filter_complex", filter.String(),
		"-map", "0:v",
		"-map", "[aout]",
		"-t", trimFloat(duration),
		"-c:v", "libx264",
		"-preset", "veryfast",
		"-pix_fmt", "yuv420p",
		"-c:a", "aac",
		"-b:a", "128k",
		"-movflags", "+faststart",
		outFile,
	)
	return args, nil
}

func loadFontFace(bold bool, size float64) font.Face {
	paths := []string{}
	if bold {
		paths = append(paths,
			"/System/Library/Fonts/Supplemental/Arial Bold.ttf",
			"/usr/share/fonts/truetype/dejavu/DejaVuSans-Bold.ttf",
			"/usr/share/fonts/truetype/liberation2/LiberationSans-Bold.ttf",
		)
	} else {
		paths = append(paths,
			"/System/Library/Fonts/Supplemental/Arial.ttf",
			"/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf",
			"/usr/share/fonts/truetype/liberation2/LiberationSans-Regular.ttf",
		)
	}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		ft, err := opentype.Parse(b)
		if err != nil {
			continue
		}
		face, err := opentype.NewFace(ft, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingFull})
		if err == nil {
			return face
		}
	}
	return basicfont.Face7x13
}

func (r *v2NativeRender) elementBox(el V2Element) image.Rectangle {
	x := r.parseMeasureX(el.X, math.NaN())
	y := r.parseMeasureY(el.Y, math.NaN())
	w := r.parseMeasureX(el.Width, math.NaN())
	h := r.parseMeasureY(el.Height, math.NaN())
	if math.IsNaN(x) {
		x = r.parseMeasureX(styleAny(el.Style, "x"), 0)
	}
	if math.IsNaN(y) {
		y = r.parseMeasureY(styleAny(el.Style, "y"), 0)
	}
	if math.IsNaN(w) {
		w = r.parseMeasureX(styleAny(el.Style, "width"), float64(r.width))
	}
	if math.IsNaN(h) {
		h = r.parseMeasureY(styleAny(el.Style, "height"), float64(r.height))
	}
	pos := strings.ToLower(styleString(el.Style, "position", ""))
	if pos != "" && el.X == nil && el.Y == nil {
		switch pos {
		case "center":
			x, y = (float64(r.width)-w)/2, (float64(r.height)-h)/2
		case "top":
			x, y = (float64(r.width)-w)/2, 80*r.scaleY
		case "bottom":
			x, y = (float64(r.width)-w)/2, float64(r.height)-h-80*r.scaleY
		}
	}
	return image.Rect(int(math.Round(x)), int(math.Round(y)), int(math.Round(x+w)), int(math.Round(y+h)))
}

func (r *v2NativeRender) parseMeasureX(v any, fallback float64) float64 {
	return parseMeasureScaled(v, r.width, fallback, r.scaleX)
}

func (r *v2NativeRender) parseMeasureY(v any, fallback float64) float64 {
	return parseMeasureScaled(v, r.height, fallback, r.scaleY)
}

func parseMeasure(v any, base int, fallback float64) float64 {
	return parseMeasureScaled(v, base, fallback, 1)
}

func parseMeasureScaled(v any, base int, fallback, scale float64) float64 {
	switch x := v.(type) {
	case nil:
		return fallback
	case float64:
		return x * scale
	case int:
		return float64(x) * scale
	case string:
		s := strings.TrimSpace(x)
		if strings.HasSuffix(s, "%") {
			n, _ := strconv.ParseFloat(strings.TrimSuffix(s, "%"), 64)
			return float64(base) * n / 100
		}
		s = strings.TrimSuffix(s, "px")
		if n, err := strconv.ParseFloat(s, 64); err == nil {
			return n * scale
		}
	}
	return fallback
}

func fillRoundRect(img *image.RGBA, rect image.Rectangle, radius int, c color.RGBA) {
	if rect.Empty() {
		return
	}
	if radius <= 0 {
		stddraw.Draw(img, rect, &image.Uniform{c}, image.Point{}, stddraw.Over)
		return
	}
	radius = minInt(radius, minInt(rect.Dx(), rect.Dy())/2)
	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		for x := rect.Min.X; x < rect.Max.X; x++ {
			cx := clampInt(x, rect.Min.X+radius, rect.Max.X-radius-1)
			cy := clampInt(y, rect.Min.Y+radius, rect.Max.Y-radius-1)
			dx, dy := x-cx, y-cy
			if dx*dx+dy*dy <= radius*radius {
				img.SetRGBA(x, y, c)
			}
		}
	}
}

func composite(dst, src *image.RGBA, opacity float64) {
	if opacity >= 0.999 {
		stddraw.Draw(dst, dst.Bounds(), src, image.Point{}, stddraw.Over)
		return
	}
	op := uint8(clamp01(opacity) * 255)
	mask := image.NewUniform(color.Alpha{A: op})
	stddraw.DrawMask(dst, dst.Bounds(), src, image.Point{}, mask, image.Point{}, stddraw.Over)
}

func compositeRect(dst, src *image.RGBA, rect image.Rectangle, opacity float64) {
	rect = rect.Intersect(dst.Bounds())
	if rect.Empty() {
		return
	}
	if opacity >= 0.999 {
		stddraw.Draw(dst, rect, src, rect.Min, stddraw.Over)
		return
	}
	op := uint8(clamp01(opacity) * 255)
	mask := image.NewUniform(color.Alpha{A: op})
	stddraw.DrawMask(dst, rect, src, rect.Min, mask, image.Point{}, stddraw.Over)
}

func parseColor(s string, fallback color.RGBA) color.RGBA {
	s = strings.TrimSpace(s)
	if s == "" {
		return fallback
	}
	if strings.HasPrefix(s, "rgba(") && strings.HasSuffix(s, ")") {
		parts := strings.Split(strings.TrimSuffix(strings.TrimPrefix(s, "rgba("), ")"), ",")
		if len(parts) == 4 {
			r, _ := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
			g, _ := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
			b, _ := strconv.ParseFloat(strings.TrimSpace(parts[2]), 64)
			a, _ := strconv.ParseFloat(strings.TrimSpace(parts[3]), 64)
			return premultipliedRGBA(r, g, b, clamp01(a))
		}
	}
	if strings.HasPrefix(s, "#") {
		s = s[1:]
	}
	if len(s) == 6 {
		v, err := strconv.ParseUint(s, 16, 32)
		if err == nil {
			return color.RGBA{uint8(v >> 16), uint8(v >> 8), uint8(v), 255}
		}
	}
	if len(s) == 8 {
		v, err := strconv.ParseUint(s, 16, 32)
		if err == nil {
			a := float64(uint8(v)) / 255
			return premultipliedRGBA(float64(uint8(v>>24)), float64(uint8(v>>16)), float64(uint8(v>>8)), a)
		}
	}
	return fallback
}

func premultipliedRGBA(r, g, b, a float64) color.RGBA {
	a = clamp01(a)
	return color.RGBA{
		R: uint8(clampFloat(r, 0, 255) * a),
		G: uint8(clampFloat(g, 0, 255) * a),
		B: uint8(clampFloat(b, 0, 255) * a),
		A: uint8(a * 255),
	}
}

func wrapText(s string, face font.Face, maxWidth int) []string {
	if maxWidth <= 0 {
		return strings.Split(s, "\n")
	}
	var lines []string
	for _, raw := range strings.Split(s, "\n") {
		words := strings.Fields(raw)
		if len(words) == 0 {
			lines = append(lines, "")
			continue
		}
		line := words[0]
		d := &font.Drawer{Face: face}
		for _, word := range words[1:] {
			next := line + " " + word
			if d.MeasureString(next).Ceil() <= maxWidth {
				line = next
			} else {
				lines = append(lines, line)
				line = word
			}
		}
		lines = append(lines, line)
	}
	return lines
}

func revealText(s string, t, dur float64, mode string) string {
	if dur <= 0 || t >= dur {
		return s
	}
	p := clamp01(t / dur)
	if mode == "word_by_word" {
		words := strings.Fields(s)
		n := int(math.Ceil(p * float64(len(words))))
		if n < 1 {
			n = 1
		}
		return strings.Join(words[:minInt(n, len(words))], " ")
	}
	runes := []rune(s)
	n := int(math.Ceil(p * float64(len(runes))))
	if n < 1 {
		n = 1
	}
	return string(runes[:minInt(n, len(runes))])
}

func revealTextLines(lines []string, t, dur float64, mode string) []string {
	out := make([]string, len(lines))
	if t < 0 {
		return out
	}
	if dur <= 0 || t >= dur {
		copy(out, lines)
		return out
	}
	p := clamp01(t / dur)
	if mode == "word_by_word" {
		total := 0
		for _, line := range lines {
			total += len(strings.Fields(line))
		}
		remaining := int(math.Ceil(p * float64(total)))
		if remaining < 1 && total > 0 {
			remaining = 1
		}
		for i, line := range lines {
			words := strings.Fields(line)
			if remaining <= 0 {
				continue
			}
			take := minInt(remaining, len(words))
			out[i] = strings.Join(words[:take], " ")
			remaining -= take
		}
		return out
	}
	total := 0
	for _, line := range lines {
		total += len([]rune(line))
	}
	remaining := int(math.Ceil(p * float64(total)))
	if remaining < 1 && total > 0 {
		remaining = 1
	}
	for i, line := range lines {
		runes := []rune(line)
		if remaining <= 0 {
			continue
		}
		take := minInt(remaining, len(runes))
		out[i] = string(runes[:take])
		remaining -= take
	}
	return out
}

func applyKeyframe(anim map[string]any, key string, t, current float64) float64 {
	raw, ok := anim[key]
	if !ok {
		return current
	}
	items, ok := raw.([]any)
	if !ok {
		return current
	}
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		start := mapFloat(m, "start", 0)
		length := mapFloat(m, "length", mapFloat(m, "duration", 0))
		if length <= 0 || t < start || t > start+length {
			continue
		}
		from := mapFloat(m, "from", current)
		to := mapFloat(m, "to", current)
		return from + (to-from)*easeOutCubic(clamp01((t-start)/length))
	}
	return current
}

func transformBox(box image.Rectangle, xOff, yOff, scale float64) image.Rectangle {
	cx := float64(box.Min.X+box.Max.X) / 2
	cy := float64(box.Min.Y+box.Max.Y) / 2
	w := float64(box.Dx()) * scale
	h := float64(box.Dy()) * scale
	return image.Rect(
		int(math.Round(cx-w/2+xOff)),
		int(math.Round(cy-h/2+yOff)),
		int(math.Round(cx+w/2+xOff)),
		int(math.Round(cy+h/2+yOff)),
	)
}

func transformBoxAround(box, origin image.Rectangle, xOff, yOff, scale float64) image.Rectangle {
	cx := float64(origin.Min.X+origin.Max.X) / 2
	cy := float64(origin.Min.Y+origin.Max.Y) / 2
	minX := cx + (float64(box.Min.X)-cx)*scale + xOff
	minY := cy + (float64(box.Min.Y)-cy)*scale + yOff
	maxX := cx + (float64(box.Max.X)-cx)*scale + xOff
	maxY := cy + (float64(box.Max.Y)-cy)*scale + yOff
	return image.Rect(
		int(math.Round(minX)),
		int(math.Round(minY)),
		int(math.Round(maxX)),
		int(math.Round(maxY)),
	)
}

func easeOutCubic(t float64) float64 {
	t = clamp01(t)
	return 1 - math.Pow(1-t, 3)
}

func styleAny(m map[string]any, key string) any {
	if m == nil {
		return nil
	}
	return m[key]
}

func styleString(m map[string]any, key, fallback string) string {
	if m == nil {
		return fallback
	}
	if s, ok := m[key].(string); ok {
		return s
	}
	return fallback
}

func styleFloat(m map[string]any, key string, fallback float64) float64 {
	if m == nil {
		return fallback
	}
	return anyFloat(m[key], fallback)
}

func styleBool(m map[string]any, key string, fallback bool) bool {
	if m == nil {
		return fallback
	}
	if v, ok := m[key].(bool); ok {
		return v
	}
	return fallback
}

func mapString(m map[string]any, key, fallback string) string {
	if m == nil {
		return fallback
	}
	if s, ok := m[key].(string); ok {
		return s
	}
	return fallback
}

func mapFloat(m map[string]any, key string, fallback float64) float64 {
	if m == nil {
		return fallback
	}
	return anyFloat(m[key], fallback)
}

func anyFloat(v any, fallback float64) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case float32:
		return float64(x)
	case int:
		return float64(x)
	case int64:
		return float64(x)
	case string:
		if n, err := strconv.ParseFloat(strings.TrimSpace(x), 64); err == nil {
			return n
		}
	}
	return fallback
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func clampFloat(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func errorsf(msg string, args ...any) error {
	return fmt.Errorf(msg, args...)
}
