package main

import (
	"strings"
	"testing"
)

// Regression test for the v0.12.5 thumbnail-extraction fix on the
// remote path. Previously buildRemoteIndexScript did a single fixed
// seek with no smart filter and no luma check, so the produced
// thumbnail was effectively "the frame at thumbnail_seek_seconds" —
// usually a black opening, studio logo, or title card. This test
// pins the new multi-attempt + signalstats-luma-check strategy in
// the generated shell so a future refactor doesn't silently regress
// it back to single-seek.
func TestBuildRemoteIndexScript_HasSmartThumbnailLoop(t *testing.T) {
	s := buildRemoteIndexScript(remoteIndexScriptInputs{
		FFmpeg: "/u/bin/ffmpeg", FFprobe: "/u/bin/ffprobe",
		SignedURL: "https://example/x.mkv", FileID: "99",
		ThumbSeek: 1.0, ThumbWidth: 320,
		WaveW: 800, WaveH: 80,
		PublicURL: "https://example", StorageToken: "t", ProjectID: "p",
	})
	for _, marker := range []string{
		"luma_of()",                           // helper defined
		`signalstats\.YAVG`,                   // ffmpeg luma probe (sed-escaped period in source)
		"thumbnail=30,scale=$THUMB_WIDTH:-2",  // smart-frame filter, not just scale
		"0.05 0.15 0.30 0.50 0.75",            // percentage-based seek schedule
		"export THUMB_SEEK=",                  // first attempt = user-configured seek
		`'BEGIN{exit !(l >= 25)}'`,            // 25/255 luma threshold (matches local path)
		`|| continue`,                         // failure on one seek doesn't abort
	} {
		if !strings.Contains(s, marker) {
			t.Errorf("generated script missing marker %q\nfull script:\n%s", marker, s)
		}
	}
}
