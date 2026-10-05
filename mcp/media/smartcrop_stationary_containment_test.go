package main

import (
	"fmt"
	"image"
	"image/color"
	"testing"
)

// The same repeated warm patch can describe a subject at the crop edge or an
// unrelated feature across the room. Stationary sub-runs must obey the existing
// still/scene containment policy before declaring that patch a temporal track.
func TestSmartCropStationaryStaticContainment(t *testing.T) {
	for _, count := range []int{3, 4, 9} {
		for _, anchored := range []bool{false, true} {
			for _, edgeRecovery := range []bool{false, true} {
				t.Run(fmt.Sprintf("frames=%d/motion-anchor=%v/edge-recovery=%v", count, anchored, edgeRecovery), func(t *testing.T) {
					const srcW, cropW = 1920, 606
					x := 750
					if edgeRecovery {
						x = 1000
					}
					samples := make([]smartCropV2Sample, count)
					for i := range samples {
						img := image.NewRGBA(image.Rect(0, 0, 320, 180))
						fillSmartCropTestRect(img, img.Bounds(), color.RGBA{R: 90, G: 95, B: 100, A: 255})
						fillSmartCropTestRect(img, image.Rect(245, 50, 255, 80), color.RGBA{R: 205, G: 160, B: 140, A: 255})
						samples[i] = smartCropV2Sample{point: cropPathPoint{AtMs: int64(i) * 1000, X: x}, img: img}
					}
					candidate, ok := bestSmartCropTemporalConsensus(samples, srcW, cropW)
					if !ok || !candidate.StaticAnchored || !smartCropTemporalResultConfident(candidate) {
						t.Fatalf("fixture needs confident static evidence: %+v ok=%v", candidate, ok)
					}
					if anchored {
						samples = append(samples, smartCropV2Sample{point: cropPathPoint{AtMs: int64(count) * 1000, X: x}, motionTracked: true})
					}
					correctSmartCropStationaryRuns(samples, srcW, cropW)
					for i, sample := range samples[:count] {
						if edgeRecovery {
							if sample.point.X <= x || sample.point.X > candidate.X || !sample.temporalTrack {
								t.Fatalf("edge subject was not recovered at %d: %+v", i, sample)
							}
						} else if sample.point.X != x || sample.temporalTrack {
							t.Fatalf("disconnected room feature stole crop at %d: %+v", i, sample)
						}
					}
				})
			}
		}
	}
}
