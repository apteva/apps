package main

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"os"
	"strconv"
	"testing"
)

func portraitCompositionFixture(center int, gesture bool) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, 320, 180))
	draw.Draw(img, img.Bounds(), &image.Uniform{color.RGBA{225, 225, 220, 255}}, image.Point{}, draw.Src)
	fill := func(r image.Rectangle, c color.RGBA) { draw.Draw(img, r, &image.Uniform{c}, image.Point{}, draw.Src) }
	fill(image.Rect(center-12, 66, center+12, 94), color.RGBA{200, 140, 95, 255})
	fill(image.Rect(center-24, 92, center+24, 180), color.RGBA{45, 45, 50, 255})
	if gesture {
		fill(image.Rect(center+10, 86, center+105, 99), color.RGBA{200, 140, 95, 255})
	}
	// A large lower-frame piece of furniture is not an upright head/torso.
	fill(image.Rect(10, 145, 95, 180), color.RGBA{55, 55, 60, 255})
	return img
}

func TestSmartCropPortraitComposition(t *testing.T) {
	for _, center := range []int{70, 160, 205} {
		t.Run(strconv.Itoa(center), func(t *testing.T) {
			current := cropWindow{W: 606, H: 1080, X: 1000}
			got, ok := composeSmartCropPortrait(portraitCompositionFixture(center, false), current, 1920, 1080)
			if !ok || got.H >= 1080 || got.Y <= 0 {
				t.Fatalf("headroom not removed: %+v changed=%v", got, ok)
			}
			if got.X > 6*(center-24) || got.X+got.W < 6*(center+24) || got.Y > 66*6 || got.Y+got.H < 1080 {
				t.Fatalf("subject clipped: %+v", got)
			}
			if got.W%2 != 0 || got.H%2 != 0 || got.X%2 != 0 || got.Y%2 != 0 {
				t.Fatalf("odd crop: %+v", got)
			}
		})
	}
	t.Run("wide-gesture-keeps-scale-and-body", func(t *testing.T) {
		got, ok := composeSmartCropPortrait(portraitCompositionFixture(160, true), cropWindow{W: 606, H: 1080, X: 1000}, 1920, 1080)
		if !ok || got.W != 606 || got.H != 1080 || got.Y != 0 || got.X > 136*6 || got.X+got.W < 184*6 {
			t.Fatalf("gesture zoom or body clipping: %+v changed=%v", got, ok)
		}
	})
	t.Run("two-subjects-remain-ambiguous", func(t *testing.T) {
		img := portraitCompositionFixture(70, false)
		other := portraitCompositionFixture(240, false)
		draw.Draw(img, image.Rect(150, 0, 320, 180), other, image.Pt(150, 0), draw.Src)
		current := cropWindow{W: 606, H: 1080, X: 658}
		got, ok := composeSmartCropPortrait(img, current, 1920, 1080)
		if ok || got != current {
			t.Fatalf("ambiguous scene changed: %+v", got)
		}
	})
	t.Run("warm-wall-is-not-a-person", func(t *testing.T) {
		img := image.NewRGBA(image.Rect(0, 0, 320, 180))
		draw.Draw(img, img.Bounds(), &image.Uniform{color.RGBA{200, 140, 95, 255}}, image.Point{}, draw.Src)
		current := cropWindow{W: 606, H: 1080, X: 658}
		got, ok := composeSmartCropPortrait(img, current, 1920, 1080)
		if ok || got != current {
			t.Fatalf("warm wall changed: %+v", got)
		}
	})
	t.Run("dark-furniture-without-head-evidence", func(t *testing.T) {
		img := portraitCompositionFixture(160, false)
		draw.Draw(img, image.Rect(148, 66, 172, 94), &image.Uniform{color.RGBA{55, 55, 60, 255}}, image.Point{}, draw.Src)
		current := cropWindow{W: 606, H: 1080, X: 658}
		got, ok := composeSmartCropPortrait(img, current, 1920, 1080)
		if ok || got != current {
			t.Fatalf("furniture changed: %+v", got)
		}
	})
	t.Run("occupied-headroom-is-not-discarded", func(t *testing.T) {
		img := portraitCompositionFixture(160, false)
		draw.Draw(img, image.Rect(130, 0, 190, 55), &image.Uniform{color.RGBA{55, 55, 60, 255}}, image.Point{}, draw.Src)
		current := cropWindow{W: 606, H: 1080, X: 658}
		got, ok := composeSmartCropPortrait(img, current, 1920, 1080)
		if ok || got != current {
			t.Fatalf("occupied headroom changed: %+v", got)
		}
	})
	for _, img := range []image.Image{nil, image.NewRGBA(image.Rect(0, 0, 0, 0))} {
		if _, ok := composeSmartCropPortrait(img, cropWindow{W: 606, H: 1080}, 1920, 1080); ok {
			t.Fatal("invalid image accepted")
		}
	}
}

func BenchmarkComposeSmartCropPortrait320(b *testing.B) {
	img := portraitCompositionFixture(160, false)
	win := cropWindow{W: 606, H: 1080, X: 1000}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		composeSmartCropPortrait(img, win, 1920, 1080)
	}
}

func TestSmartCropPortraitExistingPhotographicScenes(t *testing.T) {
	for _, name := range []string{"synthetic_seated_person.jpg", "synthetic_reclined_person.jpg"} {
		f, err := os.Open("testdata/smartcrop/" + name)
		if err != nil {
			t.Fatal(err)
		}
		base, _, err := image.Decode(f)
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
		for _, shift := range []int{-220, 0, 220} {
			for _, exposure := range []int{-20, 0, 20} {
				t.Run(fmt.Sprintf("%s/%d/%d", name, shift, exposure), func(t *testing.T) {
					img := adjustSyntheticSmartCropExposure(shiftSyntheticSmartCropImage(base, shift), exposure)
					win, _, err := analyzeSmartCropV2FrameDetailed(1920, 1080, 9, 16, img)
					if err != nil {
						t.Fatal(err)
					}
					composed, changed := composeSmartCropPortrait(img, *win, 1920, 1080)
					if changed || composed != *win {
						t.Fatalf("established seated/reclining crop changed: %+v -> %+v", win, composed)
					}
				})
			}
		}
	}
}

func TestSmartCropPortraitPreservesSupportedFace(t *testing.T) {
	face := &smartCropFace{MinX: 1000, MaxX: 1150, MinY: 420, MaxY: 570, Scale: 150}
	if !smartCropPortraitPreservesFace(cropWindow{X: 850, Y: 260, W: 460, H: 818}, face) {
		t.Fatal("safe composition rejected")
	}
	for _, win := range []cropWindow{{X: 1000, Y: 260, W: 460, H: 818}, {X: 850, Y: 450, W: 460, H: 620}} {
		if smartCropPortraitPreservesFace(win, face) {
			t.Fatalf("face margin lost: %+v", win)
		}
	}
}
