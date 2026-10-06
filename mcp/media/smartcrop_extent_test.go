package main

import (
	"context"
	"image"
	"image/color"
	"image/draw"
	"testing"
)

func extentTestImage() *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, 320, 180))
	draw.Draw(img, img.Bounds(), &image.Uniform{color.RGBA{130, 130, 130, 255}}, image.Point{}, draw.Src)
	return img
}
func extentTestFill(img *image.RGBA, rect image.Rectangle, c color.RGBA) {
	draw.Draw(img, rect, &image.Uniform{c}, image.Point{}, draw.Src)
}
func TestSupportedForegroundHeadRejectsFootAndStaticDecor(t *testing.T) {
	bg := extentTestImage()
	refs := []image.Image{bg, bg, bg, bg}
	img := extentTestImage()
	extentTestFill(img, image.Rect(70, 85, 230, 165), color.RGBA{55, 60, 65, 255})
	skin := color.RGBA{205, 145, 125, 255}
	extentTestFill(img, image.Rect(74, 92, 98, 111), skin)
	extentTestFill(img, image.Rect(180, 130, 220, 163), skin) // larger foot/leg is lower
	sample := smartCropV2Sample{img: img, point: cropPathPoint{X: 600}}
	extent, ok := supportedSmartCropSubjectExtent(sample, refs, 1920, 1080, 606)
	if !ok || extent.Head == nil || extent.Head.CenterX > 600 || extent.Head.CenterY > 700 {
		t.Fatalf("upper head geometry lost to foot: %+v", extent)
	}
	x := containSmartCropSubjectExtentX(900, extent, 1920, 606)
	if x > extent.Head.MinX || x+606 < extent.Head.MaxX {
		t.Fatalf("head outside crop: x=%d head=%+v", x, extent.Head)
	}
	for _, c := range []struct {
		name string
		img  image.Image
		refs []image.Image
	}{
		{"unchanged furniture", img, []image.Image{img, img, img, img}},
		{"missing background", img, refs[:3]},
		{"camera or exposure", func() image.Image { im := extentTestImage(); extentTestFill(im, im.Bounds(), skin); return im }(), refs},
		{"two similar foreground subjects", func() image.Image {
			im := extentTestImage()
			for _, x := range []int{30, 170} {
				extentTestFill(im, image.Rect(x, 85, x+115, 165), color.RGBA{55, 60, 65, 255})
				extentTestFill(im, image.Rect(x+5, 92, x+28, 110), skin)
			}
			return im
		}(), refs},
		{"foot only", func() image.Image {
			im := extentTestImage()
			extentTestFill(im, image.Rect(70, 85, 230, 165), color.RGBA{55, 60, 65, 255})
			extentTestFill(im, image.Rect(180, 130, 220, 163), skin)
			return im
		}(), refs},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := sample
			s.img = c.img
			if e, ok := supportedSmartCropSubjectExtent(s, c.refs, 1920, 1080, 606); ok {
				t.Fatalf("unsupported head/extent: %+v", e)
			}
		})
	}
}
func TestSubjectExtentContainmentAndImpossibleWidth(t *testing.T) {
	head := &smartCropFace{MinX: 980, MaxX: 1080, CenterX: 1030, Scale: 100}
	fits := &smartCropSubjectExtent{Bounds: cropWindow{X: 810, W: 510}, Head: head, Evidence: "face_foreground"}
	got := containSmartCropSubjectExtentX(578, fits, 1920, 606)
	if got > 810-37 || got+606 < 1320+36 || containSmartCropFaceX(got, *head, 1920, 606) != got {
		t.Fatalf("available arms/margin lost: %d", got)
	}
	wide := &smartCropSubjectExtent{Bounds: cropWindow{X: 500, W: 850}, Head: head, Evidence: "face_foreground"}
	a := &smartCropAudit{Coverage: "unknown"}
	ctx := context.WithValue(context.Background(), smartCropAuditKey{}, a)
	recordSmartCropExtent(ctx, smartCropV2Sample{point: cropPathPoint{AtMs: 42}}, wide, 606)
	if a.Coverage != "exceeds_crop_width" || a.Recommendation == "" {
		t.Fatalf("impossible width not exposed: %+v", a)
	}
	if wide.Bounds.W <= 606 {
		t.Fatal("fixture fits")
	}
}
func TestRecliningHeadSurvivesSmoothing(t *testing.T) {
	h := &smartCropFace{MinX: 480, MaxX: 600, CenterX: 540, Scale: 120}
	samples := []smartCropV2Sample{{point: cropPathPoint{AtMs: 0, X: 600}, supportedHead: h}, {point: cropPathPoint{AtMs: 1000, X: 600}, supportedHead: h}}
	path := constrainSmartCropPathToFaceTracks([]cropPathPoint{{AtMs: 0, X: 600}, {AtMs: 1000, X: 600}}, samples, 1920, 606)
	for _, p := range path {
		if p.X > h.MinX || p.X+606 < h.MaxX {
			t.Fatalf("smoothed head clipped: %+v", p)
		}
	}
	if smartCropStaticRetainsSupportedHeads(600, samples, 1920, 606) {
		t.Fatal("static collapse accepted clipped head")
	}
}

func BenchmarkSupportedSmartCropExtent(b *testing.B) {
	bg := extentTestImage()
	refs := []image.Image{bg, bg, bg, bg}
	img := extentTestImage()
	extentTestFill(img, image.Rect(70, 85, 230, 165), color.RGBA{55, 60, 65, 255})
	extentTestFill(img, image.Rect(74, 92, 98, 111), color.RGBA{205, 145, 125, 255})
	extentTestFill(img, image.Rect(180, 130, 220, 163), color.RGBA{205, 145, 125, 255})
	sample := smartCropV2Sample{img: img, point: cropPathPoint{X: 600}}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		supportedSmartCropSubjectExtent(sample, refs, 1920, 1080, 606)
	}
}
