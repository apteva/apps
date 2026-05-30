package main

import (
	"bytes"
	"image/jpeg"
	"testing"
)

func TestBgraFrameToJPEG(t *testing.T) {
	const (
		width     = 2
		height    = 1
		rowStride = 64
	)
	raw := make([]byte, rowStride*height)
	// BGRA red, then green.
	raw[0], raw[1], raw[2], raw[3] = 0, 0, 255, 255
	raw[4], raw[5], raw[6], raw[7] = 0, 255, 0, 255

	out, err := bgraFrameToJPEG(raw, width, height, rowStride)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) < 3 || out[0] != 0xff || out[1] != 0xd8 || out[2] != 0xff {
		n := len(out)
		if n > 8 {
			n = 8
		}
		t.Fatalf("not a jpeg: %x", out[:n])
	}
	img, err := jpeg.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	if got := img.Bounds().Dx(); got != width {
		t.Fatalf("width=%d, want %d", got, width)
	}
	if got := img.Bounds().Dy(); got != height {
		t.Fatalf("height=%d, want %d", got, height)
	}
}

func TestAlignUp(t *testing.T) {
	if got := alignUp(1176, 64); got != 1216 {
		t.Fatalf("alignUp(1176,64)=%d, want 1216", got)
	}
	if got := alignUp(4716, 64); got != 4736 {
		t.Fatalf("alignUp(4716,64)=%d, want 4736", got)
	}
}
