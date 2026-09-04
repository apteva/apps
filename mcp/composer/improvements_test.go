package main

import (
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tk "github.com/apteva/app-sdk/testkit"
	"golang.org/x/image/font"
)

func TestV1BaseImageOpacityIsAppliedByFFmpeg(t *testing.T) {
	ffmpeg, err := exec.LookPath(ffmpegPath())
	if err != nil {
		t.Skip("ffmpeg not available")
	}
	dir := t.TempDir()
	source := filepath.Join(dir, "white.png")
	writeSolidPNG(t, source, 64, 64, color.RGBA{255, 255, 255, 255})

	edit, err := parseEditJSON(`{"timeline":{"background":"#000000","tracks":[{"type":"visual","clips":[{"asset":{"type":"image","src":"fixture"},"start":0,"length":0.4,"fit":"cover","opacity":0.1}]}]}}`)
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "opacity.mp4")
	args := buildLocalFFmpegArgsWithAudioInfo(edit, Output{Format: "mp4", Resolution: "sd", Aspect: "16:9", FPS: 24}, []string{source}, -1, out, []bool{false})
	if output, err := exec.Command(ffmpeg, args...).CombinedOutput(); err != nil {
		t.Fatalf("render opacity fixture: %v\n%s\n%s", err, output, strings.Join(args, " "))
	}
	rgb := readRenderedPixel(t, ffmpeg, out, 427, 240)
	for channel, value := range map[string]byte{"red": rgb[0], "green": rgb[1], "blue": rgb[2]} {
		if value < 18 || value > 38 {
			t.Fatalf("%s channel = %d, want about 10%% white over black; pixel=%v", channel, value, rgb)
		}
	}
}

func TestV1OverlayRendersRoundedCornersAndShadow(t *testing.T) {
	ffmpeg, err := exec.LookPath(ffmpegPath())
	if err != nil {
		t.Skip("ffmpeg not available")
	}
	dir := t.TempDir()
	base, overlay := filepath.Join(dir, "base.png"), filepath.Join(dir, "overlay.png")
	writeSolidPNG(t, base, 64, 64, color.RGBA{128, 128, 128, 255})
	writeSolidPNG(t, overlay, 64, 64, color.RGBA{255, 255, 255, 255})
	edit, err := parseEditJSON(`{"timeline":{"background":"#808080","tracks":[
		{"type":"visual","clips":[{"asset":{"type":"image","src":"base"},"start":0,"length":0.4,"fit":"cover"}]},
		{"type":"visual","clips":[{"asset":{"type":"image","src":"overlay"},"start":0,"length":0.4,"layout":{"x":100,"y":100,"width":120,"height":120,"fit":"stretch","border_radius":30,"shadow":true}}]}
	]}}`)
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "rounded-shadow.mp4")
	args := buildLocalFFmpegArgsWithAudioInfo(edit, Output{Format: "mp4", Resolution: "sd", Aspect: "16:9", FPS: 24}, []string{base, overlay}, -1, out, []bool{false, false})
	if output, err := exec.Command(ffmpeg, args...).CombinedOutput(); err != nil {
		t.Fatalf("render rounded overlay: %v\n%s\n%s", err, output, strings.Join(args, " "))
	}
	corner := readRenderedPixel(t, ffmpeg, out, 100, 100)
	center := readRenderedPixel(t, ffmpeg, out, 160, 160)
	shadow := readRenderedPixel(t, ffmpeg, out, 160, 225)
	if corner[0] < 105 || corner[0] > 150 || center[0] < 235 || shadow[0] > 100 {
		t.Fatalf("rounded/shadow pixels corner=%v center=%v shadow=%v", corner, center, shadow)
	}
}

func TestV2NativeShapesRenderGradientBorderRadiusEllipseAndShadow(t *testing.T) {
	spec := &V2Composition{
		Version: composerV2Version,
		Output:  V2Output{Width: 140, Height: 100, FPS: 24, Background: "#000000"},
		Scenes: []V2Scene{{Duration: 1, Elements: []V2Element{
			{Type: "shape", X: 20.0, Y: 15.0, Width: 90.0, Height: 55.0, Style: map[string]any{
				"radius": 12.0, "stroke": "#ffffff", "stroke_width": 4.0,
				"gradient": map[string]any{"angle": 90.0, "from": "#ff0000", "to": "#0000ff"},
				"shadow":   map[string]any{"color": "#00ff00", "offset_x": 0.0, "offset_y": 12.0, "blur": 2.0, "opacity": 1.0},
			}},
			{Type: "shape", X: 116.0, Y: 10.0, Width: 20.0, Height: 20.0, Style: map[string]any{"kind": "ellipse", "fill": "#ffff00"}},
		}}},
	}
	r := &v2NativeRender{spec: spec, width: 140, height: 100, designW: 140, designH: 100, scaleX: 1, scaleY: 1, scale: 1, fps: 24, duration: 1, assets: map[string]V2Asset{}, images: map[string]image.Image{}, faces: map[string]font.Face{}}
	frame := r.renderFrame(0)

	assertNearColor(t, frame.RGBAAt(20, 15), color.RGBA{0, 0, 0, 255}, 3, "rounded corner")
	assertNearColor(t, frame.RGBAAt(22, 42), color.RGBA{255, 255, 255, 255}, 8, "border")
	left, right := frame.RGBAAt(30, 42), frame.RGBAAt(100, 42)
	if left.R <= left.B || right.B <= right.R {
		t.Fatalf("gradient direction not visible: left=%v right=%v", left, right)
	}
	shadow := frame.RGBAAt(65, 78)
	if shadow.G < 80 || shadow.R > 25 || shadow.B > 25 {
		t.Fatalf("shadow pixel = %v, want visible green shadow", shadow)
	}
	assertNearColor(t, frame.RGBAAt(126, 20), color.RGBA{255, 255, 0, 255}, 3, "ellipse center")
	assertNearColor(t, frame.RGBAAt(116, 10), color.RGBA{0, 0, 0, 255}, 3, "ellipse corner")
}

func TestTextLayoutWrapsAutoSizesPadsAndWarnsOnUnsafeAnchor(t *testing.T) {
	clip := Clip{UID: "headline", Asset: Asset{Type: "text", Text: "A professional comparison headline that must wrap", Font: &TextFont{Size: 42}, Style: &TextStyle{
		Wrap: true, AutoSize: true, MaxWidth: 120, MaxHeight: 64, MinFontSize: 10, Padding: 8, LineHeight: 1,
	}}}
	layout := layoutV1Text(clip, clip.Asset.Text, 42, 320, 180)
	if layout.fontSize >= 42 || len(layout.lines) < 2 {
		t.Fatalf("layout did not wrap and shrink: %+v", layout)
	}
	face := v1TextFace(clip.Asset.Font, layout.fontSize)
	drawer := &font.Drawer{Face: face}
	for _, line := range layout.lines {
		if width := drawer.MeasureString(line).Ceil(); width > 104 {
			t.Fatalf("wrapped line %q is %dpx, exceeds padded 104px content box", line, width)
		}
	}
	if layout.height > 48 {
		t.Fatalf("text height = %d, exceeds padded 48px content box", layout.height)
	}

	edit, err := parseEditJSON(`{"timeline":{"tracks":[{"type":"visual","clips":[{"asset":{"type":"image","src":"x"},"length":1}]},{"type":"text","clips":[{"uid":"unsafe","asset":{"type":"text","text":"Title","style":{"safe_area":0.1}},"position":{"x":"4%","y":"50%"},"length":1}]}]}}`)
	if err != nil {
		t.Fatal(err)
	}
	if warnings := strings.Join(v1TextSafeAreaWarnings(edit), "\n"); !strings.Contains(warnings, "outside the 10% safe area") {
		t.Fatalf("missing safe-area warning: %q", warnings)
	}
}

func TestFFmpegReportsMeasuredProgress(t *testing.T) {
	_, err := exec.LookPath(ffmpegPath())
	if err != nil {
		t.Skip("ffmpeg not available")
	}
	dir := t.TempDir()
	out := filepath.Join(dir, "progress.mp4")
	app := tk.NewAppCtx(t, "apteva.yaml")
	var progress []RenderProgress
	ctx := withRenderProgress(context.Background(), func(value RenderProgress) { progress = append(progress, value) })
	args := []string{"-y", "-v", "error", "-re", "-f", "lavfi", "-i", "testsrc=size=64x64:rate=24:duration=2.5", "-c:v", "libx264", "-tune", "zerolatency", "-pix_fmt", "yuv420p", out}
	result, err := runLocalFFmpeg(ctx, app, time.Now(), dir, 1, out, args, 2.5)
	if err != nil {
		t.Fatal(err)
	}
	if result.LocalPath != out || len(progress) < 2 {
		t.Fatalf("path=%q progress=%+v", result.LocalPath, progress)
	}
	hasIntermediate := false
	for _, value := range progress {
		if value.Fraction > 0 && value.Fraction < 1 && value.OutTimeSeconds > 0 {
			hasIntermediate = true
		}
	}
	if !hasIntermediate || progress[len(progress)-1].Fraction != 1 {
		t.Fatalf("expected measured intermediate and terminal progress, got %+v", progress)
	}
}

func TestRemoteShellAndDrawTextPreserveSpecialCharacters(t *testing.T) {
	raw := "https://example.test/über?q=$HOME&tick=`uname`&author=O'Reilly"
	quoted, err := exec.Command("bash", "-c", "printf '%s' "+shellQuote(raw)).Output()
	if err != nil {
		t.Fatal(err)
	}
	if string(quoted) != raw {
		t.Fatalf("shell round trip = %q, want %q", quoted, raw)
	}

	ffmpeg, err := exec.LookPath(ffmpegPath())
	if err != nil {
		t.Skip("ffmpeg not available")
	}
	dir := t.TempDir()
	out := filepath.Join(dir, "O'Reilly-$HOME-über.mp4")
	face := composerFontFor(nil)
	fontPaths, err := writeComposerFonts(dir, []composerFontFace{face})
	if err != nil {
		t.Fatal(err)
	}
	filter := materializeComposerFontArgs([]string{buildDrawText(&TextOver{Body: "O'Reilly $HOME `uname` 100% café", FontSize: 24, Color: "white", Position: "center"}, 320, 180)}, fontPaths)[0]
	args := []string{"-y", "-v", "error", "-f", "lavfi", "-i", "color=c=black:s=320x180:d=0.2", "-vf", filter, "-frames:v", "1", out}
	if output, err := exec.Command("bash", "-c", shellEcho(ffmpeg, args)).CombinedOutput(); err != nil {
		t.Fatalf("shell-executed special-character render: %v\n%s\n%s", err, output, shellEcho(ffmpeg, args))
	}
	if info, err := os.Stat(out); err != nil || info.Size() == 0 {
		t.Fatalf("special-character output missing: info=%v err=%v", info, err)
	}
}

func TestRenderDiagnosticsRedactSecretsInDatabase(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml").WithProject("redaction")
	res, err := ctx.AppDB().Exec(`INSERT INTO compositions(project_id,name,edit_json,output_json,duration_seconds) VALUES(?,?,?,?,?)`, "redaction", "Failure", `{}`, `{}`, 1)
	if err != nil {
		t.Fatal(err)
	}
	compositionID, _ := res.LastInsertId()
	renderID, err := createRenderRow(ctx, compositionID, "redaction", "remote", `{}`, `{}`, "rendering", "rendering")
	if err != nil {
		t.Fatal(err)
	}
	failRender(ctx, renderID, compositionID, "redaction", errors.New("provider rejected Authorization: Bearer top-secret\nhttps://host/render?api_key=query-secret"), "API_KEY=command-secret ffmpeg")
	var status, message, command, diagnostic string
	if err := ctx.AppDB().QueryRow(`SELECT status,error,ffmpeg_command,progress_json FROM renders WHERE id=?`, renderID).Scan(&status, &message, &command, &diagnostic); err != nil {
		t.Fatal(err)
	}
	combined := strings.Join([]string{message, command, diagnostic}, "\n")
	for _, secret := range []string{"top-secret", "query-secret", "command-secret"} {
		if strings.Contains(combined, secret) {
			t.Fatalf("persisted diagnostics leaked %q: %s", secret, combined)
		}
	}
	if status != "failed" || strings.Count(combined, redactedValue) < 3 {
		t.Fatalf("unexpected redacted failure row: status=%s data=%s", status, combined)
	}
	var detail map[string]any
	if err := json.Unmarshal([]byte(diagnostic), &detail); err != nil || detail["stage"] != "rendering" || detail["executor"] != "remote" {
		t.Fatalf("diagnostic=%q parsed=%v err=%v", diagnostic, detail, err)
	}
}

func writeSolidPNG(t *testing.T, path string, width, height int, fill color.RGBA) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.SetRGBA(x, y, fill)
		}
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, img); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func readRenderedPixel(t *testing.T, ffmpeg, path string, x, y int) []byte {
	t.Helper()
	filter := "crop=1:1:" + trimFloat(float64(x)) + ":" + trimFloat(float64(y)) + ",format=rgb24"
	output, err := exec.Command(ffmpeg, "-v", "error", "-ss", "0.1", "-i", path, "-vf", filter, "-frames:v", "1", "-f", "rawvideo", "pipe:1").Output()
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			t.Fatalf("read rendered pixel: %v\n%s", err, exit.Stderr)
		}
		t.Fatal(err)
	}
	if len(output) < 3 {
		t.Fatalf("pixel output has %d bytes", len(output))
	}
	return output[:3]
}

func assertNearColor(t *testing.T, got, want color.RGBA, tolerance uint8, label string) {
	t.Helper()
	delta := func(a, b uint8) uint8 {
		if a > b {
			return a - b
		}
		return b - a
	}
	if delta(got.R, want.R) > tolerance || delta(got.G, want.G) > tolerance || delta(got.B, want.B) > tolerance || delta(got.A, want.A) > tolerance {
		t.Fatalf("%s color = %v, want %v ±%d", label, got, want, tolerance)
	}
}
