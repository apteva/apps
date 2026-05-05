// Tests pinning v0.1.0 contracts:
//
//   * formatToMuxer table — adding a third format must update the
//     map AND the description, fail loudly if not
//   * boundedTimeout clamps correctly
//   * apteva.yaml + embedded const both parse against the SDK
//   * grab_frame against an MJPEG file:// URL works end-to-end via
//     real ffmpeg (skipped if ffmpeg isn't on PATH so CI doesn't
//     break on hosts without it)
//   * probe against the same MJPEG returns sensible JSON

package main

import (
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func TestFormatToMuxer(t *testing.T) {
	cases := []struct {
		in       string
		muxerOK  bool
		ctOK     string
	}{
		{"jpeg", true, "image/jpeg"},
		{"jpg", true, "image/jpeg"},
		{"png", true, "image/png"},
		{"gif", false, ""},
		{"", false, ""},
	}
	for _, c := range cases {
		_, ct, err := formatToMuxer(c.in)
		if c.muxerOK && err != nil {
			t.Errorf("formatToMuxer(%q) errored: %v", c.in, err)
		}
		if !c.muxerOK && err == nil {
			t.Errorf("formatToMuxer(%q) accepted unsupported format", c.in)
		}
		if ct != c.ctOK {
			t.Errorf("formatToMuxer(%q) ct=%q want %q", c.in, ct, c.ctOK)
		}
	}
}

func TestBoundedTimeout(t *testing.T) {
	cases := []struct {
		given int
		want  int
	}{
		{0, 8},   // zero → default (passing 0 is the same as omitting the field)
		{-5, 8},  // negative → default
		{1, 1},
		{8, 8},
		{30, 30},
		{60, 30}, // over → clamped to 30
		{1000, 30},
	}
	for _, c := range cases {
		got := boundedTimeout(map[string]any{"timeout_seconds": c.given}, 8)
		if got != c.want {
			t.Errorf("boundedTimeout(%d, 8) = %d, want %d", c.given, got, c.want)
		}
	}
	// Default branch when key absent.
	if got := boundedTimeout(nil, 8); got != 8 {
		t.Errorf("boundedTimeout(nil, 8) = %d, want 8", got)
	}
}

func TestManifestValidates(t *testing.T) {
	if _, err := sdk.ParseManifest([]byte(manifestYAML)); err != nil {
		t.Fatalf("embedded manifest: %v", err)
	}
	body, err := os.ReadFile("apteva.yaml")
	if err != nil {
		t.Fatalf("read apteva.yaml: %v", err)
	}
	if _, err := sdk.ParseManifest(body); err != nil {
		t.Fatalf("apteva.yaml: %v", err)
	}
}

// TestGrabFrame_Local exercises grabFrame against a tiny MJPEG file
// produced by ffmpeg itself — single 64x64 black frame, ~1 KB. The
// test is skipped when ffmpeg isn't installed so the rest of the
// suite still runs on CI hosts without it.
func TestGrabFrame_Local(t *testing.T) {
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not on PATH")
	}
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src.mjpeg")
	// Build a 1-frame mjpeg using ffmpeg's `color` filter source.
	if out, err := exec.Command(ffmpegPath,
		"-y", "-loglevel", "error",
		"-f", "lavfi", "-i", "color=c=black:s=64x64:d=0.1",
		"-frames:v", "1", "-f", "mjpeg", src,
	).CombinedOutput(); err != nil {
		t.Fatalf("build mjpeg fixture: %v: %s", err, out)
	}

	a := &App{ffmpegPath: ffmpegPath}
	out, err := a.toolGrabFrame(nil, map[string]any{
		"url":    "file://" + src,
		"format": "jpeg",
	})
	if err != nil {
		t.Fatalf("grab: %v", err)
	}
	m := out.(map[string]any)
	if m["content_type"] != "image/jpeg" {
		t.Errorf("ct = %v", m["content_type"])
	}
	jpg, _ := base64.StdEncoding.DecodeString(m["bytes_base64"].(string))
	if len(jpg) < 100 {
		t.Errorf("frame too small: %d bytes", len(jpg))
	}
	// JPEG magic bytes — paranoid check that we actually got one.
	if len(jpg) < 3 || jpg[0] != 0xFF || jpg[1] != 0xD8 || jpg[2] != 0xFF {
		t.Errorf("not a JPEG: %x", jpg[:min(3, len(jpg))])
	}
}

// TestProbe_Local — same fixture, run probe, assert the parsed JSON
// contains the expected stream/format keys.
func TestProbe_Local(t *testing.T) {
	ffmpegPath, errF := exec.LookPath("ffmpeg")
	ffprobePath, errP := exec.LookPath("ffprobe")
	if errF != nil || errP != nil {
		t.Skip("ffmpeg/ffprobe not on PATH")
	}
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src.mjpeg")
	if out, err := exec.Command(ffmpegPath,
		"-y", "-loglevel", "error",
		"-f", "lavfi", "-i", "color=c=black:s=64x64:d=0.5",
		"-f", "mjpeg", src,
	).CombinedOutput(); err != nil {
		t.Fatalf("build mjpeg: %v: %s", err, out)
	}
	a := &App{ffprobePath: ffprobePath}
	out, err := a.toolProbe(nil, map[string]any{"url": "file://" + src})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	m := out.(map[string]any)
	if _, ok := m["streams"]; !ok {
		t.Errorf("probe output missing `streams` key: keys=%v", keysOf(m))
	}
	if _, ok := m["format"]; !ok {
		t.Errorf("probe output missing `format` key: keys=%v", keysOf(m))
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
