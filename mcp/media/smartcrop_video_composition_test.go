package main

import (
	"context"
	"image"
	"image/color"
	"testing"
)

func seatedCompositionFixture() ([]smartCropV2Sample, []image.Image) {
	bg := extentTestImage()
	refs := []image.Image{bg, bg, bg, bg}
	var ss []smartCropV2Sample
	for i := 0; i < 4; i++ {
		im := extentTestImage()
		extentTestFill(im, image.Rect(145, 65, 205, 180), color.RGBA{55, 60, 65, 255})
		skin := color.RGBA{205, 145, 125, 255}
		extentTestFill(im, image.Rect(162, 70, 182, 88), skin)
		extentTestFill(im, image.Rect(148+i, 105, 158+i, 117), skin)
		extentTestFill(im, image.Rect(192-i, 105, 202-i, 117), skin)
		extentTestFill(im, image.Rect(157, 133, 175, 170), skin)
		ss = append(ss, smartCropV2Sample{img: im, point: cropPathPoint{AtMs: int64(i) * 1000, X: 744}})
	}
	return ss, refs
}

func TestStableReelCompositionRetainsMovingPoseAndBottom(t *testing.T) {
	ss, refs := seatedCompositionFixture()
	a := &smartCropAudit{Coverage: "unknown"}
	ctx := context.WithValue(context.Background(), smartCropAuditKey{}, a)
	original := cropWindow{X: 744, W: 606, H: 1080}
	win, ok := composeSmartCropReel(ctx, original, ss, refs, 1920, 1080, smartCropTarget{StartMs: 0, EndMs: 3000})
	if !ok || win.H >= 1080 || win.H < 720 || win.Y+win.H != 1080 || win.X > 888 || win.X+win.W < 1212 {
		t.Fatalf("moving pose not preserved: %+v, %t", win, ok)
	}
	a.Effective = &smartCropAuditWindow{X: win.X, Y: win.Y, W: win.W, H: win.H}
	if !cropRetainsSampledExtents(a) || len(a.Extents) != 4 {
		t.Fatal("sample evidence not retained")
	}
	for _, e := range a.Extents {
		if e.Head == nil || e.Head.Bounds.Y < win.Y || e.Head.Bounds.X < win.X || e.Head.Bounds.X+e.Head.Bounds.W > win.X+win.W {
			t.Fatal("head geometry clipped")
		}
	}
	for _, s := range ss {
		if s.sceneForeground || s.scenePoseGroup || s.motionTracked {
			t.Fatal("composition mutated tracking samples")
		}
	}
}

func TestStableReelCompositionRejectsUncertaintyAndWideMovement(t *testing.T) {
	for _, name := range []string{"cut", "sparse", "missing opening", "missing ending", "no backgrounds", "no head", "wide hands", "camera change", "large movement"} {
		t.Run(name, func(t *testing.T) {
			ss, refs := seatedCompositionFixture()
			target := smartCropTarget{StartMs: 0, EndMs: 3000}
			switch name {
			case "cut":
				ss[2].point.Cut = true
			case "sparse":
				ss[2].point.AtMs = 6000
				ss[3].point.AtMs = 7000
				target.EndMs = 7000
			case "missing opening":
				target.StartMs = -1000
			case "missing ending":
				target.EndMs = 4000
			case "no backgrounds":
				refs = refs[:3]
			case "no head":
				for _, s := range ss {
					extentTestFill(s.img.(*image.RGBA), image.Rect(162, 70, 182, 88), color.RGBA{55, 60, 65, 255})
				}
			case "wide hands":
				for _, s := range ss {
					im := s.img.(*image.RGBA)
					extentTestFill(im, image.Rect(105, 105, 245, 117), color.RGBA{205, 145, 125, 255})
				}
			case "camera change":
				im := ss[2].img.(*image.RGBA)
				extentTestFill(im, im.Bounds(), color.RGBA{205, 145, 125, 255})
			case "large movement":
				for i := range ss {
					if i > 1 {
						im := extentTestImage()
						extentTestFill(im, image.Rect(65, 65, 125, 180), color.RGBA{55, 60, 65, 255})
						extentTestFill(im, image.Rect(82, 70, 102, 88), color.RGBA{205, 145, 125, 255})
						ss[i].img = im
						ss[i].point.X = 260
					}
				}
			}
			original := cropWindow{X: 744, W: 606, H: 1080}
			win, ok := composeSmartCropReel(context.Background(), original, ss, refs, 1920, 1080, target)
			if ok || win != original {
				t.Fatalf("unsafe tightening: %+v", win)
			}
		})
	}
}

func TestVideoPoseGroupingRejectsAdjacentPeople(t *testing.T) {
	bg := extentTestImage()
	im := extentTestImage()
	skin := color.RGBA{205, 145, 125, 255}
	for _, x := range []int{110, 170} {
		extentTestFill(im, image.Rect(x, 65, x+50, 170), color.RGBA{55, 60, 65, 255})
		extentTestFill(im, image.Rect(x+10, 70, x+30, 88), skin)
	}
	s := smartCropV2Sample{img: im, sceneForeground: true, scenePoseGroup: true, motionTracked: true, point: cropPathPoint{X: 744}}
	if e, ok := supportedSmartCropSubjectExtent(s, []image.Image{bg, bg, bg, bg}, 1920, 1080, 606); ok {
		t.Fatalf("two people merged: %+v", e)
	}
}

func BenchmarkComposeStableReel4Samples(b *testing.B) {
	ss, refs := seatedCompositionFixture()
	original := cropWindow{X: 744, W: 606, H: 1080}
	target := smartCropTarget{EndMs: 3000}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		composeSmartCropReel(context.Background(), original, ss, refs, 1920, 1080, target)
	}
}

func TestVideoPoseGroupingReconnectsSeparatedHeadAndBody(t *testing.T) {
	bg := extentTestImage()
	im := extentTestImage()
	clothes := color.RGBA{55, 60, 65, 255}
	skin := color.RGBA{205, 145, 125, 255}
	extentTestFill(im, image.Rect(165, 65, 185, 130), clothes)
	extentTestFill(im, image.Rect(165, 70, 185, 88), skin)
	for _, x := range []int{145, 195} {
		extentTestFill(im, image.Rect(x, 100, x+10, 170), clothes)
		extentTestFill(im, image.Rect(x, 135, x+10, 165), skin)
	}
	extentTestFill(im, image.Rect(145, 160, 205, 170), clothes)
	s := smartCropV2Sample{img: im, sceneForeground: true, motionTracked: true, point: cropPathPoint{X: 744}}
	refs := []image.Image{bg, bg, bg, bg}
	if _, ok := supportedSmartCropSubjectExtent(s, refs, 1920, 1080, 606); ok {
		t.Fatal("fixture must reproduce disconnected pose ambiguity")
	}
	s.scenePoseGroup = true
	e, ok := supportedSmartCropSubjectExtent(s, refs, 1920, 1080, 606)
	if !ok || e.Head == nil || e.Bounds.X > 870 || e.Bounds.X+e.Bounds.W < 1230 {
		t.Fatalf("disconnected pose lost: %+v", e)
	}
}
