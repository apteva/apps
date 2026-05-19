package main

import (
	"reflect"
	"strings"
	"testing"
)

// Pinned interpretation of the rotation→transpose direction. This
// is the bit most likely to be silently flipped by a future
// "cleanup" PR; the tests anchor it so a regression turns into a
// red CI run instead of upside-down reels.

func TestTransposeFilterFor(t *testing.T) {
	cases := []struct {
		in   int
		want string
	}{
		{0, ""},
		{90, "transpose=2"},                // 90° CCW
		{180, "transpose=1,transpose=1"},    // 180°
		{270, "transpose=1"},                // 90° CW
		{45, ""},                            // off-axis → nothing (caller should normalise first)
		{-90, ""},                           // negative → nothing (canonicalRotation handles normalisation)
	}
	for _, c := range cases {
		if got := transposeFilterFor(c.in); got != c.want {
			t.Errorf("transposeFilterFor(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestCanonicalRotation(t *testing.T) {
	cases := []struct{ in, want int }{
		{0, 0}, {90, 90}, {180, 180}, {270, 270},
		{-90, 270}, {-180, 180}, {-270, 90},
		{360, 0}, {450, 90},
		{45, 0},     // off-axis → 0
		{135, 0},    // off-axis → 0
	}
	for _, c := range cases {
		if got := canonicalRotation(c.in); got != c.want {
			t.Errorf("canonicalRotation(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

// applyRotation should: (a) inject -noautorotate before -i, (b)
// prepend the transpose filter to any -vf chain. Everything else
// is passed through unchanged.
func TestApplyRotation_InjectsNoautorotateAndPrependsTranspose(t *testing.T) {
	// Mirrors what planExtractReel emits for a 9:16 reel render.
	args := []string{
		"-y", "-loglevel", "error",
		"-progress", "pipe:1",
		"-ss", "0.000", "-to", "10.000",
		"-i", "{input}",
		"-vf", "crop=608:1080:656:0,scale=540:-2",
		"-c:a", "copy",
		"-avoid_negative_ts", "make_zero",
	}
	got := applyRotation(args, 90)
	want := []string{
		"-y", "-loglevel", "error",
		"-progress", "pipe:1",
		"-ss", "0.000", "-to", "10.000",
		"-noautorotate", "-i", "{input}",
		"-vf", "transpose=2,crop=608:1080:656:0,scale=540:-2",
		"-c:a", "copy",
		"-avoid_negative_ts", "make_zero",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("applyRotation(rotation=90) mismatch.\n got: %s\nwant: %s",
			strings.Join(got, " "), strings.Join(want, " "))
	}
}

func TestApplyRotation_ZeroIsNoOp(t *testing.T) {
	in := []string{"-y", "-i", "input.mov", "-vf", "scale=320:-2"}
	out := applyRotation(in, 0)
	if !reflect.DeepEqual(out, in) {
		t.Errorf("applyRotation(0) should be no-op; got %v want %v", out, in)
	}
}

func TestApplyRotation_180UsesDoubleTranspose(t *testing.T) {
	args := []string{"-i", "{input}", "-vf", "scale=640:-2"}
	got := applyRotation(args, 180)
	want := []string{"-noautorotate", "-i", "{input}", "-vf", "transpose=1,transpose=1,scale=640:-2"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("applyRotation(180) wrong.\n got: %v\nwant: %v", got, want)
	}
}

// shouldRotate gates the helper based on op + presence of -vf. Audio
// extract and concat are excluded.
func TestShouldRotate(t *testing.T) {
	withVF := []string{"-i", "x", "-vf", "scale=320:-2"}
	withoutVF := []string{"-i", "x", "-c:v", "copy"}
	cases := []struct {
		op   string
		args []string
		want bool
	}{
		{"extract_reel", withVF, true},
		{"extract_frame", withVF, true},
		{"resize", withVF, true},
		{"trim", withoutVF, false}, // stream-copy: rotation preserved via metadata
		{"audio_extract", withVF, false},
		{"concat", withVF, false},
	}
	for _, c := range cases {
		if got := shouldRotate(c.op, c.args); got != c.want {
			t.Errorf("shouldRotate(%q, vf=%v) = %v, want %v", c.op, hasVF(c.args), got, c.want)
		}
	}
}

// Probe side: rotation lifts cleanly out of ffprobe's side_data_list
// + the W↔H swap happens for 90/270 only.
func TestParseProbeBytes_RotationSwapsWidthHeight(t *testing.T) {
	// Synthetic ffprobe JSON with rotation=90 on a 1080×1920 codec
	// frame — the iPhone landscape-recording → portrait-display case.
	in := []byte(`{
		"format": {"format_name":"mov", "duration":"10.0", "bit_rate":"1000000"},
		"streams": [{
			"codec_type": "video",
			"codec_name": "hevc",
			"width": 1080,
			"height": 1920,
			"r_frame_rate": "30/1",
			"side_data_list": [{"side_data_type": "Display Matrix", "rotation": 90}]
		}]
	}`)
	p, err := parseProbeBytes(in)
	if err != nil {
		t.Fatal(err)
	}
	if p.Rotation != 90 {
		t.Errorf("Rotation = %d, want 90", p.Rotation)
	}
	if p.Width != 1920 || p.Height != 1080 {
		t.Errorf("post-swap dims = %d×%d, want 1920×1080 (display-space for a rotated landscape source)", p.Width, p.Height)
	}
}

func TestParseProbeBytes_NoRotationLeavesDimsAlone(t *testing.T) {
	in := []byte(`{
		"format": {"format_name":"mp4","duration":"5","bit_rate":"800000"},
		"streams": [{
			"codec_type":"video","codec_name":"h264","width":1920,"height":1080,
			"r_frame_rate":"30/1"
		}]
	}`)
	p, err := parseProbeBytes(in)
	if err != nil {
		t.Fatal(err)
	}
	if p.Rotation != 0 {
		t.Errorf("Rotation = %d, want 0", p.Rotation)
	}
	if p.Width != 1920 || p.Height != 1080 {
		t.Errorf("dims = %d×%d, want 1920×1080 (no rotation = no swap)", p.Width, p.Height)
	}
}

func TestParseProbeBytes_Rotation270AlsoSwaps(t *testing.T) {
	// rotation=-90 (= 270 after normalisation) — common for some
	// Android cameras + selfie mode on iPhone.
	in := []byte(`{
		"format": {"format_name":"mov","duration":"3","bit_rate":"600000"},
		"streams": [{
			"codec_type":"video","codec_name":"h264","width":720,"height":1280,
			"r_frame_rate":"30/1",
			"side_data_list":[{"side_data_type":"Display Matrix","rotation":-90}]
		}]
	}`)
	p, err := parseProbeBytes(in)
	if err != nil {
		t.Fatal(err)
	}
	if p.Rotation != 270 {
		t.Errorf("Rotation = %d, want 270 (canonicalised from -90)", p.Rotation)
	}
	if p.Width != 1280 || p.Height != 720 {
		t.Errorf("post-swap dims = %d×%d, want 1280×720", p.Width, p.Height)
	}
}
