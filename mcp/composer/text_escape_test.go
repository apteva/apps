package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestV1DrawTextMatchesLiteralTextFile(t *testing.T) {
	ffmpeg, err := exec.LookPath(ffmpegPath())
	if err != nil {
		t.Skip("ffmpeg not available")
	}
	dir := t.TempDir()
	fontDir := filepath.Join(dir, "O'Reilly fonts")
	if err := os.Mkdir(fontDir, 0o700); err != nil {
		t.Fatal(err)
	}
	fontPaths, err := writeComposerFonts(fontDir, []composerFontFace{composerFontFor(nil)})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, body string }{
		{"apostrophe", "It's ready"},
		{"apostrophe and filter delimiters", "It's: ready, [yes]; go"},
		{"multiline", "First line\nSecond line"},
		{"CRLF multiline", "First line\r\nSecond line"},
		{"blank line", "First\n\nThird"},
		{"literal backslash n", `First\nSecond`},
		{"literal symbols", "100% %{pts} O'Reilly \\path\ncafé $HOME `uname`"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			textFile := filepath.Join(dir, "text.txt")
			if err := os.WriteFile(textFile, []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			for _, kind := range []string{"legacy clip text", "timed text track"} {
				t.Run(kind, func(t *testing.T) {
					filter := buildDrawText(&TextOver{Body: tc.body, FontSize: 24, Position: "center"}, 640, 360)
					if kind == "timed text track" {
						filter = buildTimedDrawText(Clip{Length: 1, Asset: Asset{Type: "text", Text: tc.body, Font: &TextFont{Size: 24}}}, 640, 360)
					}
					filter = materializeComposerFontArgs([]string{filter}, fontPaths)[0]
					// textfile bypasses inline text escaping and is the independent
					// oracle for glyphs, punctuation, and actual line breaks.
					reference := "drawtext=textfile='" + textFile + "'" + filter[strings.Index(filter, ":fontfile="):] + ":expansion=none"
					want := renderDrawTextFrame(t, ffmpeg, reference, false)
					for _, remote := range []bool{false, true} {
						got := renderDrawTextFrame(t, ffmpeg, filter, remote)
						if !bytes.Equal(got, want) {
							t.Errorf("rendered text differs from literal textfile (remote shell=%v): %q", remote, tc.body)
						}
					}
				})
			}
		})
	}
}

func renderDrawTextFrame(t *testing.T, ffmpeg, filter string, remote bool) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	args := []string{"-v", "error", "-f", "lavfi", "-i", "color=black:s=640x360:d=0.1",
		"-filter_complex", "[0:v]" + filter + "[out]", "-map", "[out]", "-frames:v", "1", "-pix_fmt", "gray", "-f", "rawvideo", "pipe:1"}
	cmd := exec.CommandContext(ctx, ffmpeg, args...)
	if remote {
		// Match Composer's Bash invocation through the Instances sh boundary.
		cmd = exec.CommandContext(ctx, "sh", "-c", "bash -c "+shellQuote(shellEcho(ffmpeg, args)))
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	frame, err := cmd.Output()
	if err != nil {
		t.Fatalf("render text (remote shell=%v): %v\n%s\n%s", remote, err, stderr.String(), filter)
	}
	if len(frame) != 640*360 {
		t.Fatalf("unexpected raw frame size: %d", len(frame))
	}
	if stderr.Len() != 0 {
		t.Errorf("FFmpeg text errors: %s", stderr.String())
	}
	return frame
}
