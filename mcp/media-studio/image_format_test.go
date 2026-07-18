package main

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"

	"golang.org/x/image/webp"
)

func TestRequestedImageOutputFormat(t *testing.T) {
	tests := []struct {
		name string
		args map[string]any
		want string
	}{
		{name: "unset defaults to jpeg", args: map[string]any{}, want: "jpeg"},
		{name: "jpeg", args: map[string]any{"options": map[string]any{"output_format": "jpeg"}}, want: "jpeg"},
		{name: "jpg alias", args: map[string]any{"options": map[string]any{"output_format": "JPG"}}, want: "jpeg"},
		{name: "legacy Venice format", args: map[string]any{"options": map[string]any{"format": "webp"}}, want: "webp"},
		{name: "provider neutral wins", args: map[string]any{"options": map[string]any{"format": "png", "output_format": "jpeg"}}, want: "jpeg"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := requestedImageOutputFormat(tt.args)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("format = %q, want %q", got, tt.want)
			}
		})
	}
	if _, err := requestedImageOutputFormat(map[string]any{"options": map[string]any{"output_format": "tiff"}}); err == nil {
		t.Fatal("expected unsupported format error")
	}
}

func TestCanonicalizeImageOutputFormat(t *testing.T) {
	args := map[string]any{"options": map[string]any{"format": "JPG", "safe_mode": false}}
	canonicalizeImageOutputFormat(args, "jpeg")
	opts := args["options"].(map[string]any)
	if opts["output_format"] != "jpeg" {
		t.Fatalf("output_format = %v", opts["output_format"])
	}
	if _, ok := opts["format"]; ok {
		t.Fatalf("legacy format alias remained: %+v", opts)
	}
	if opts["safe_mode"] != false {
		t.Fatalf("unrelated options changed: %+v", opts)
	}
}

func TestEnforceImageOutputFormat_PNGToJPEGFlattensTransparency(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 16, 16))
	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			src.SetNRGBA(x, y, color.NRGBA{R: 255, A: 0})
		}
	}
	var input bytes.Buffer
	if err := png.Encode(&input, src); err != nil {
		t.Fatal(err)
	}

	media, converted, err := enforceImageOutputFormat(generatedMedia{
		B64: base64.StdEncoding.EncodeToString(input.Bytes()), MimeType: "image/png", Ext: "png",
	}, input.Bytes(), "jpeg")
	if err != nil {
		t.Fatal(err)
	}
	if media.MimeType != "image/jpeg" || media.Ext != "jpg" {
		t.Fatalf("media = %+v", media)
	}
	if mime, ext, ok := sniffImageMediaType(converted); !ok || mime != "image/jpeg" || ext != "jpg" {
		t.Fatalf("converted bytes = %q/%q ok=%v", mime, ext, ok)
	}
	stored, err := base64.StdEncoding.DecodeString(media.B64)
	if err != nil || !bytes.Equal(stored, converted) {
		t.Fatalf("converted base64 mismatch: err=%v", err)
	}
	decoded, err := jpeg.Decode(bytes.NewReader(converted))
	if err != nil {
		t.Fatal(err)
	}
	r, g, b, _ := decoded.At(8, 8).RGBA()
	if r < 0xf000 || g < 0xf000 || b < 0xf000 {
		t.Fatalf("transparent pixel was not flattened onto white: r=%x g=%x b=%x", r, g, b)
	}
}

func TestEnforceImageOutputFormat_WebPToJPEG(t *testing.T) {
	input := mustDecodeBase64(t, "UklGRjgAAABXRUJQVlA4ICwAAACQAQCdASoIAAgAAgA0JaACdLoAA5gA/vmTb/+QH/+QH/+QH/8gP+IXeyAwAA==")
	if _, err := webp.Decode(bytes.NewReader(input)); err != nil {
		t.Fatalf("invalid WebP fixture: %v", err)
	}
	media, converted, err := enforceImageOutputFormat(
		generatedMedia{UpstreamURL: "https://provider.test/image.webp", MimeType: "image/webp", Ext: "webp"},
		input, "jpeg",
	)
	if err != nil {
		t.Fatal(err)
	}
	if media.UpstreamURL != "" || media.B64 == "" || media.MimeType != "image/jpeg" || media.Ext != "jpg" {
		t.Fatalf("converted media = %+v", media)
	}
	if _, err := jpeg.Decode(bytes.NewReader(converted)); err != nil {
		t.Fatalf("converted JPEG: %v", err)
	}
}

func TestEnforceImageOutputFormat_AlreadyJPEGUnchanged(t *testing.T) {
	data := fakeJPEG()
	media := generatedMedia{B64: base64.StdEncoding.EncodeToString(data), MimeType: "image/png", Ext: "png"}
	gotMedia, gotData, err := enforceImageOutputFormat(media, data, "jpeg")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotData, data) || gotMedia.B64 != media.B64 {
		t.Fatal("matching JPEG bytes should not be re-encoded")
	}
	if gotMedia.MimeType != "image/jpeg" || gotMedia.Ext != "jpg" {
		t.Fatalf("sniffed metadata = %+v", gotMedia)
	}
}

func TestEnforceImageOutputFormat_OversizedJPEGReencodedBelowLimit(t *testing.T) {
	input := append(append([]byte(nil), fakeJPEG()...), make([]byte, maxGeneratedImageBytes)...)
	media, converted, err := enforceImageOutputFormat(generatedMedia{
		B64: base64.StdEncoding.EncodeToString(input), MimeType: "image/jpeg", Ext: "jpg",
	}, input, "jpeg")
	if err != nil {
		t.Fatal(err)
	}
	if len(converted) >= maxGeneratedImageBytes {
		t.Fatalf("converted JPEG is %d bytes, want less than %d", len(converted), maxGeneratedImageBytes)
	}
	if bytes.Equal(converted, input) || media.MimeType != "image/jpeg" || media.Ext != "jpg" {
		t.Fatalf("oversized JPEG was not normalized: media=%+v bytes=%d", media, len(converted))
	}
	if _, err := jpeg.Decode(bytes.NewReader(converted)); err != nil {
		t.Fatalf("converted JPEG: %v", err)
	}
}

func TestEncodeJPEGUnderLimit_DownscalesWhenQualityFloorIsInsufficient(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 1024, 1024))
	seed := uint32(1)
	for i := 0; i < len(src.Pix); i += 4 {
		seed = seed*1664525 + 1013904223
		src.Pix[i] = byte(seed >> 24)
		seed = seed*1664525 + 1013904223
		src.Pix[i+1] = byte(seed >> 24)
		seed = seed*1664525 + 1013904223
		src.Pix[i+2] = byte(seed >> 24)
		src.Pix[i+3] = 255
	}

	const testLimit = 100_000
	converted, err := encodeJPEGUnderLimit(src, testLimit)
	if err != nil {
		t.Fatal(err)
	}
	if len(converted) >= testLimit {
		t.Fatalf("converted JPEG is %d bytes, want less than %d", len(converted), testLimit)
	}
	decoded, err := jpeg.Decode(bytes.NewReader(converted))
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Bounds().Dx() >= src.Bounds().Dx() || decoded.Bounds().Dy() >= src.Bounds().Dy() {
		t.Fatalf("expected downscale from %v, got %v", src.Bounds(), decoded.Bounds())
	}
}

func TestEnforceImageOutputFormat_PNG(t *testing.T) {
	jpegBytes := fakeJPEG()
	media, converted, err := enforceImageOutputFormat(
		generatedMedia{B64: base64.StdEncoding.EncodeToString(jpegBytes), MimeType: "image/jpeg", Ext: "jpg"},
		jpegBytes, "png",
	)
	if err != nil {
		t.Fatal(err)
	}
	mime, ext, ok := sniffImageMediaType(converted)
	if !ok || mime != "image/png" || ext != "png" || media.MimeType != mime || media.Ext != ext {
		t.Fatalf("media=%+v sniff=%q/%q ok=%v", media, mime, ext, ok)
	}
}

func TestEnforceImageOutputFormat_WebPMismatchFails(t *testing.T) {
	jpegBytes := fakeJPEG()
	_, _, err := enforceImageOutputFormat(
		generatedMedia{B64: base64.StdEncoding.EncodeToString(jpegBytes), MimeType: "image/jpeg", Ext: "jpg"},
		jpegBytes, "webp",
	)
	if err == nil {
		t.Fatal("expected mismatched WebP output to fail")
	}
}

func mustDecodeBase64(t *testing.T, value string) []byte {
	t.Helper()
	data, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
