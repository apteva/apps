package main

// Tier 1 tests for the Cloudinary render backend. We exercise:
//
//   - buildCloudinaryChain per supported op (string-equality on the
//     emitted eager chain — the Cloudinary side of the contract)
//   - selectExecutor dispatch (no binding → local; cloudinary binding
//     + supported op → cloudinary; cloudinary + unsupported op →
//     local; unknown slug → local)
//   - parseCloudinaryEagerURL response parsing (eager array wins over
//     top-level secure_url; falls back when eager is empty)
//
// The full Execute() round-trip (signed URL + integration call +
// download + re-upload) lives behind a stubPlatform pattern; the
// real Cloudinary HTTP path is covered by a `-tags live` integration
// test, gated on CLOUDINARY_* env vars.

import (
	"encoding/json"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

// ─── chain builders ─────────────────────────────────────────────────

func TestBuildCloudinaryChain_Trim(t *testing.T) {
	chain, err := buildCloudinaryChain("trim",
		json.RawMessage(`{"start_ms":1000,"end_ms":4500}`), "")
	if err != nil {
		t.Fatal(err)
	}
	want := "so_1,du_3.500,f_mp4"
	if chain != want {
		t.Errorf("chain=%q want %q", chain, want)
	}
}

func TestBuildCloudinaryChain_Trim_HonoursOutputExt(t *testing.T) {
	chain, err := buildCloudinaryChain("trim",
		json.RawMessage(`{"start_ms":0,"end_ms":2000}`), "clip.webm")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(chain, ",f_webm") {
		t.Errorf("expected f_webm suffix, got %q", chain)
	}
}

func TestBuildCloudinaryChain_Trim_Validates(t *testing.T) {
	cases := []string{
		`{"start_ms":-1,"end_ms":1000}`,
		`{"start_ms":1000,"end_ms":1000}`,
		`{"start_ms":2000,"end_ms":1000}`,
	}
	for _, c := range cases {
		if _, err := buildCloudinaryChain("trim", json.RawMessage(c), ""); err == nil {
			t.Errorf("expected error for %s", c)
		}
	}
}

func TestBuildCloudinaryChain_Resize_Fixed(t *testing.T) {
	chain, err := buildCloudinaryChain("resize",
		json.RawMessage(`{"width":320,"height":240}`), "")
	if err != nil {
		t.Fatal(err)
	}
	if chain != "c_scale,w_320,h_240,f_mp4" {
		t.Errorf("got %q", chain)
	}
}

func TestBuildCloudinaryChain_Resize_KeepAspect(t *testing.T) {
	chain, err := buildCloudinaryChain("resize",
		json.RawMessage(`{"width":640,"keep_aspect":true}`), "")
	if err != nil {
		t.Fatal(err)
	}
	// keep_aspect → c_limit (Cloudinary's "scale within W, preserve
	// aspect ratio") rather than c_scale.
	if !strings.Contains(chain, "c_limit,w_640") {
		t.Errorf("expected c_limit step in keep_aspect chain: %q", chain)
	}
	if strings.Contains(chain, "h_") {
		t.Errorf("keep_aspect chain shouldn't pin height: %q", chain)
	}
}

func TestBuildCloudinaryChain_Transcode_FormatRequired(t *testing.T) {
	if _, err := buildCloudinaryChain("transcode",
		json.RawMessage(`{}`), ""); err == nil {
		t.Error("expected error when format missing")
	}
}

func TestBuildCloudinaryChain_Transcode_WithCodecAndBitrate(t *testing.T) {
	chain, err := buildCloudinaryChain("transcode",
		json.RawMessage(`{"format":"webm","video_codec":"libvpx-vp9","bitrate":"2M"}`), "")
	if err != nil {
		t.Fatal(err)
	}
	// Codec must be normalised (libvpx-vp9 → vp9), bitrate prefixed
	// br_, format pinned with f_webm. Order: codec, bitrate, format.
	want := "vc_vp9,br_2M,f_webm"
	if chain != want {
		t.Errorf("chain=%q want %q", chain, want)
	}
}

func TestNormaliseCldVideoCodec_Mappings(t *testing.T) {
	cases := map[string]string{
		"libx264":    "h264",
		"libx265":    "h265",
		"hevc":       "h265",
		"libvpx-vp9": "vp9",
		"libaom-av1": "av1",
		"prores":     "prores", // unknown passes through
	}
	for in, want := range cases {
		if got := normaliseCldVideoCodec(in); got != want {
			t.Errorf("normaliseCldVideoCodec(%q)=%q want %q", in, got, want)
		}
	}
}

func TestBuildCloudinaryChain_Crop(t *testing.T) {
	chain, err := buildCloudinaryChain("crop",
		json.RawMessage(`{"x":10,"y":20,"width":300,"height":200}`), "out.mp4")
	if err != nil {
		t.Fatal(err)
	}
	if chain != "c_crop,w_300,h_200,x_10,y_20,f_mp4" {
		t.Errorf("got %q", chain)
	}
}

func TestBuildCloudinaryChain_Crop_RejectsNegative(t *testing.T) {
	if _, err := buildCloudinaryChain("crop",
		json.RawMessage(`{"x":-1,"y":0,"width":10,"height":10}`), ""); err == nil {
		t.Error("expected error for negative x")
	}
}

func TestBuildCloudinaryChain_ExtractFrame_MinimalChain(t *testing.T) {
	chain, err := buildCloudinaryChain("extract_frame",
		json.RawMessage(`{"at_ms":3000}`), "")
	if err != nil {
		t.Fatal(err)
	}
	// at_ms=3000 → so_3 (clean integer); always png; no scale step
	// without an explicit width.
	if chain != "so_3,f_png" {
		t.Errorf("got %q", chain)
	}
}

func TestBuildCloudinaryChain_ExtractFrame_WithWidth(t *testing.T) {
	chain, err := buildCloudinaryChain("extract_frame",
		json.RawMessage(`{"at_ms":1500,"width":320}`), "")
	if err != nil {
		t.Fatal(err)
	}
	if chain != "so_1.500,c_scale,w_320,f_png" {
		t.Errorf("got %q", chain)
	}
}

func TestBuildCloudinaryChain_RejectsUnsupported(t *testing.T) {
	for _, op := range []string{"concat", "audio_extract", "weird"} {
		if _, err := buildCloudinaryChain(op, json.RawMessage(`{}`), ""); err == nil {
			t.Errorf("expected error for op=%q", op)
		}
	}
}

// ─── selectExecutor ────────────────────────────────────────────────

// boundCloudinary returns a stub install with a cloudinary connection
// (id 11) bound to render_executor.
func boundCloudinary() *stubPlatform {
	return &stubPlatform{
		whoami: &sdk.InstallIdentity{
			Bindings: map[string]any{"render_executor": float64(11)},
		},
		connections: map[int64]*sdk.PlatformConnection{
			11: {ID: 11, AppSlug: "cloudinary", Status: "active"},
		},
	}
}

func boundUnknownSlug() *stubPlatform {
	return &stubPlatform{
		whoami: &sdk.InstallIdentity{
			Bindings: map[string]any{"render_executor": float64(11)},
		},
		connections: map[int64]*sdk.PlatformConnection{
			11: {ID: 11, AppSlug: "transloadit", Status: "active"},
		},
	}
}

func TestSelectExecutor_NoBinding_ReturnsLocal(t *testing.T) {
	ctx := newTestCtxWithPlatform(t, noBindings())
	local := &localExecutor{ffmpegPath: "ffmpeg", scratchRoot: "/tmp", outputFolder: "/r/"}
	row := &RenderRow{Operation: "trim"}
	got := selectExecutor(ctx, local, row)
	if got.Name() != "local" {
		t.Errorf("expected local executor, got %q", got.Name())
	}
}

func TestSelectExecutor_CloudinaryBound_SupportedOp_ReturnsCloudinary(t *testing.T) {
	ctx := newTestCtxWithPlatform(t, boundCloudinary())
	local := &localExecutor{ffmpegPath: "ffmpeg", scratchRoot: "/tmp", outputFolder: "/r/"}
	for _, op := range []string{"trim", "resize", "transcode", "crop", "extract_frame"} {
		row := &RenderRow{Operation: op}
		got := selectExecutor(ctx, local, row)
		if got.Name() != "cloudinary" {
			t.Errorf("op=%s expected cloudinary, got %q", op, got.Name())
		}
	}
}

func TestSelectExecutor_CloudinaryBound_UnsupportedOp_FallsBackLocal(t *testing.T) {
	// concat + audio_extract aren't modelled by the cloud backend;
	// selectExecutor must still hand them to local ffmpeg silently.
	ctx := newTestCtxWithPlatform(t, boundCloudinary())
	local := &localExecutor{ffmpegPath: "ffmpeg", scratchRoot: "/tmp", outputFolder: "/r/"}
	for _, op := range []string{"concat", "audio_extract"} {
		row := &RenderRow{Operation: op}
		got := selectExecutor(ctx, local, row)
		if got.Name() != "local" {
			t.Errorf("op=%s expected local fallback, got %q", op, got.Name())
		}
	}
}

func TestSelectExecutor_UnknownSlug_FallsBackLocal(t *testing.T) {
	// Operator bound an integration we don't have an executor for.
	// Must NOT crash + must NOT silently forward to cloudinary; fall
	// back to local instead.
	ctx := newTestCtxWithPlatform(t, boundUnknownSlug())
	local := &localExecutor{ffmpegPath: "ffmpeg", scratchRoot: "/tmp", outputFolder: "/r/"}
	row := &RenderRow{Operation: "trim"}
	got := selectExecutor(ctx, local, row)
	if got.Name() != "local" {
		t.Errorf("expected local fallback for unknown slug, got %q", got.Name())
	}
}

// ─── response parser ───────────────────────────────────────────────

func TestParseCloudinaryEagerURL_PrefersEagerSecureURL(t *testing.T) {
	body := []byte(`{
		"public_id": "abc",
		"secure_url": "https://res.cloudinary.com/me/video/upload/abc.mp4",
		"eager": [
			{"secure_url": "https://res.cloudinary.com/me/video/upload/so_1,du_2/abc.mp4"}
		]
	}`)
	got, err := parseCloudinaryEagerURL(body)
	if err != nil {
		t.Fatal(err)
	}
	want := "https://res.cloudinary.com/me/video/upload/so_1,du_2/abc.mp4"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestParseCloudinaryEagerURL_FallsBackToTopLevel(t *testing.T) {
	// No eager block → fall back to the asset's own secure_url. Some
	// Cloudinary plans inline a single transformation rather than
	// returning an eager array.
	body := []byte(`{"secure_url":"https://res.cloudinary.com/me/video/upload/x.mp4"}`)
	got, err := parseCloudinaryEagerURL(body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(got, "/x.mp4") {
		t.Errorf("got %q", got)
	}
}

func TestParseCloudinaryEagerURL_RejectsEmpty(t *testing.T) {
	if _, err := parseCloudinaryEagerURL([]byte(`{}`)); err == nil {
		t.Error("expected error on empty response")
	}
}

func TestParseCloudinaryEagerURL_PrefersHTTPSWhenEagerHasUrlOnly(t *testing.T) {
	// Some legacy responses use `url` instead of `secure_url`. Make
	// sure we still find it.
	body := []byte(`{"eager":[{"url":"http://res.cloudinary.com/me/video/upload/abc.mp4"}]}`)
	got, err := parseCloudinaryEagerURL(body)
	if err != nil {
		t.Fatal(err)
	}
	if got != "http://res.cloudinary.com/me/video/upload/abc.mp4" {
		t.Errorf("got %q", got)
	}
}

// ─── ms→cld float ───────────────────────────────────────────────────

func TestMsToCldFloat_DropsTrailingZeros(t *testing.T) {
	cases := map[int64]string{
		0:    "0",
		1000: "1",
		1500: "1.500",
		90:   "0.090",
		3001: "3.001",
	}
	for ms, want := range cases {
		if got := msToCldFloat(ms); got != want {
			t.Errorf("msToCldFloat(%d)=%q want %q", ms, got, want)
		}
	}
}
