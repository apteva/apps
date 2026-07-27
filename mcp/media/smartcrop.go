package main

// Subject-aware crop pre-pass for extract_reel / extract_frame.
//
// Computes the crop window in source-pixel space at the per-render
// preprocess step (before buildPlan). Two modes:
//
//   "center" — geometric center of the source. Same as the
//              filter-expression-based crop the planners used pre-v0.12.7.
//
//   "smart"  — runs muesli/smartcrop against the nearest cached
//              keyframe for timed operations, then falls back to the
//              canonical thumbnail. Saliency-based (edge density,
//              saturation, skin-tone proxy), no ML model, no GPU,
//              <100 ms per call. Falls back to center if no usable
//              derivation exists, decode fails, or smartcrop errors —
//              so the render always proceeds, even on a freshly-
//              uploaded file the indexer hasn't derived from yet.
//
// Why cached keyframes/thumbnails and not a fresh ffmpeg pass: we
// already have representative frames (the local + remote indexers run
// their own seek-and-luma-check pipeline). Reusing them avoids a
// second download + ffmpeg invocation for what's just a hint to the
// saliency analyzer. The crop result is mapped from derivation-pixel
// space back to source-pixel space using the known derivation-vs-
// source dimensions.

import (
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	_ "image/gif" // accept GIFs in case a future thumbnail derivation switches format
	"image/jpeg"
	_ "image/png" // accept PNGs (waveform falls back here for audio-only sources)
	"io"
	"math"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
	"github.com/muesli/smartcrop"
	"github.com/muesli/smartcrop/nfnt"
)

// cropWindow holds the result of computeSmartCrop. All values are in
// SOURCE-pixel space and even (encoders prefer even dimensions).
type cropWindow struct {
	W, H int
	X, Y int
}

// computeSmartCrop returns the best crop rectangle for the given
// source file at the target aspect ratio.
//
//	targetW, targetH — ratio numerator/denominator (e.g. 9, 16). The
//	                   returned W:H is normalised so cropW/cropH ==
//	                   targetW/targetH within rounding.
//	mode             — "smart" (recommended default) | "center".
//
// Falls back to a centered crop on any failure of the smart path
// (missing thumbnail, decode error, smartcrop analyzer error). The
// caller can therefore treat the returned window as authoritative —
// no nil checks needed.
//
// Returns an error only when the source itself doesn't have a usable
// width/height (probe pending or failed). In that case the caller
// should leave the symbolic filter-expression crop in place so the
// render still runs.
func computeSmartCrop(
	ctx context.Context,
	app *sdk.AppCtx,
	sc *storageClient,
	projectID, sourceFileID string,
	targetW, targetH int,
	mode string,
	target smartCropTarget,
) (*cropWindow, error) {
	if targetW <= 0 || targetH <= 0 {
		return nil, fmt.Errorf("invalid target ratio %d:%d", targetW, targetH)
	}
	row, err := getMedia(app.AppDB(), projectID, sourceFileID)
	if err != nil {
		return nil, fmt.Errorf("get source media: %w", err)
	}
	if row == nil {
		return nil, fmt.Errorf("source media row %q not found", sourceFileID)
	}
	if row.Width <= 0 || row.Height <= 0 {
		// Indexer hasn't probed yet (or probe failed). Let the planner
		// keep the symbolic filter expression; ffmpeg will read iw/ih
		// itself at render time.
		return nil, fmt.Errorf("source %q has no probed dimensions yet — skip pre-crop", sourceFileID)
	}

	cw, ch := cropDimsForRatio(row.Width, row.Height, targetW, targetH)
	if cw == row.Width && ch == row.Height {
		// Source already matches target ratio — no crop needed.
		return &cropWindow{W: cw, H: ch, X: 0, Y: 0}, nil
	}

	// Center crop is the fallback; compute it up front so every smart-
	// mode failure has a sensible window to return.
	center := &cropWindow{
		W: cw, H: ch,
		X: roundEven((row.Width - cw) / 2),
		Y: roundEven((row.Height - ch) / 2),
	}

	if strings.EqualFold(mode, "center") {
		return center, nil
	}
	validDerivations, validateErr := resolveValidDerivations(ctx, sc, projectID, row.Derivations)
	if validateErr != nil {
		app.Logger().Warn("smartcrop fallback to center: derivative identity lookup failed",
			"file_id", sourceFileID, "err", validateErr.Error())
		return center, nil
	}
	row.Derivations = validDerivations

	// Smart mode — prefer the nearest cached keyframe for timed
	// renders. If keyframes are missing, fall back to the canonical
	// thumbnail rather than failing the render (the next re-render
	// after the indexer catches up will re-evaluate).
	cropSource := pickSmartCropDerivation(row.Derivations, target)
	if cropSource.StorageFileID == "" {
		app.Logger().Info("smartcrop fallback to center: no usable frame derivation yet",
			"file_id", sourceFileID)
		return center, nil
	}
	thumb, err := downloadAndDecodeImage(ctx, sc, projectID, cropSource.StorageFileID)
	if err != nil {
		app.Logger().Warn("smartcrop fallback to center: frame derivation download/decode failed",
			"file_id", sourceFileID, "derivation_kind", cropSource.Kind,
			"derivation_file_id", cropSource.StorageFileID, "err", err.Error())
		return center, nil
	}
	tBounds := thumb.Bounds()
	tW := tBounds.Dx()
	tH := tBounds.Dy()
	if tW <= 0 || tH <= 0 {
		app.Logger().Warn("smartcrop fallback to center: zero-sized frame derivation",
			"file_id", sourceFileID, "derivation_kind", cropSource.Kind,
			"derivation_file_id", cropSource.StorageFileID)
		return center, nil
	}

	// Translate the source-space crop window into thumbnail-pixel
	// space so we ask smartcrop for a rectangle proportional to what
	// we'll actually crop on the source.
	tCropW, tCropH := cropDimsForRatio(tW, tH, targetW, targetH)
	if tCropW <= 0 || tCropH <= 0 {
		return center, nil
	}

	analyzer := smartcrop.NewAnalyzer(nfnt.NewDefaultResizer())
	rect, err := analyzer.FindBestCrop(thumb, tCropW, tCropH)
	if err != nil {
		app.Logger().Warn("smartcrop fallback to center: analyzer error",
			"file_id", sourceFileID, "err", err.Error())
		return center, nil
	}

	// Map thumbnail-space (X, Y) → source-space.
	srcX := int(float64(rect.Min.X) * float64(row.Width) / float64(tW))
	srcY := int(float64(rect.Min.Y) * float64(row.Height) / float64(tH))
	// Clamp so the crop stays inside the source frame (rounding can
	// nudge us a pixel past the edge for sources with odd dims).
	if srcX < 0 {
		srcX = 0
	}
	if srcY < 0 {
		srcY = 0
	}
	if srcX+cw > row.Width {
		srcX = row.Width - cw
	}
	if srcY+ch > row.Height {
		srcY = row.Height - ch
	}
	rawSrcX := srcX
	srcX, srcY = stabilizeNarrowSmartCrop(srcX, srcY, row.Width, row.Height, cw, ch)
	if subjectX, ok := subjectAwareNarrowSmartCropX(thumb, rawSrcX, srcX, row.Width, row.Height, cw, ch, tCropW); ok {
		app.Logger().Info("smartcrop subject correction applied",
			"file_id", sourceFileID,
			"derivation_kind", cropSource.Kind,
			"derivation_file_id", cropSource.StorageFileID,
			"derivation_position_ms", cropSource.PositionMs,
			"crop_x_raw", rawSrcX,
			"crop_x_before", srcX,
			"crop_x_after", subjectX)
		srcX = subjectX
	}
	app.Logger().Info("smartcrop resolved",
		"file_id", sourceFileID,
		"derivation_kind", cropSource.Kind,
		"derivation_file_id", cropSource.StorageFileID,
		"derivation_position_ms", cropSource.PositionMs,
		"crop_w", cw,
		"crop_h", ch,
		"crop_x", roundEven(srcX),
		"crop_y", roundEven(srcY))
	return &cropWindow{
		W: cw, H: ch,
		X: roundEven(srcX),
		Y: roundEven(srcY),
	}, nil
}

// cropDimsForRatio returns the largest (w, h) inscribed in (srcW, srcH)
// whose aspect ratio equals tW:tH. Always returns even integers so the
// chosen crop is encoder-friendly. The returned (w, h) equals (srcW,
// srcH) when the source is already at the target ratio.
func cropDimsForRatio(srcW, srcH, tW, tH int) (int, int) {
	// Compare src ratio vs target. Avoid float division by cross-
	// multiplying: srcW/srcH > tW/tH  ⇔  srcW*tH > srcH*tW.
	srcWiderThanTarget := srcW*tH > srcH*tW
	if srcWiderThanTarget {
		// Width crops; keep height.
		w := srcH * tW / tH
		return roundEven(w), roundEven(srcH)
	}
	// Height crops; keep width.
	h := srcW * tH / tW
	return roundEven(srcW), roundEven(h)
}

func roundEven(n int) int {
	if n < 0 {
		return 0
	}
	return n - (n % 2)
}

// stabilizeNarrowSmartCrop keeps very narrow social crops from being
// hijacked by textured backgrounds. muesli/smartcrop is good at
// finding saliency, but on 16:9 → 9:16 reels it can overvalue brick,
// cushions, shelves, etc. and park the crop too far from the natural
// phone-frame center. For those narrow crops, blend the saliency
// answer back toward the geometric center. Wider crops are left alone.
func stabilizeNarrowSmartCrop(srcX, srcY, srcW, srcH, cropW, cropH int) (int, int) {
	if srcW <= 0 || srcH <= 0 || cropW <= 0 || cropH <= 0 || srcW <= cropW {
		return srcX, srcY
	}
	widthRatio := float64(srcW) / float64(cropW)
	if widthRatio < 1.8 {
		return srcX, srcY
	}
	maxX := srcW - cropW
	centerX := maxX / 2
	if absInt(srcX-centerX) < cropW/5 {
		if srcX < centerX {
			return clampInt(roundEven(centerX), 0, maxX), srcY
		}
		return srcX, srcY
	}
	weight := (widthRatio - 1.6) / 3.0
	if weight < 0.25 {
		weight = 0.25
	}
	if weight > 0.60 {
		weight = 0.60
	}
	x := int(math.Round(float64(srcX)*(1-weight) + float64(centerX)*weight))
	return clampInt(roundEven(x), 0, maxX), clampInt(roundEven(srcY), 0, srcH-cropH)
}

// subjectAwareNarrowSmartCropX corrects very narrow portrait crops when
// generic edge saliency picks room texture/artwork over a visible human
// subject. It uses a cheap skin/warm-subject column score on the same
// cached frame smartcrop already decoded. This is intentionally only a
// second-stage correction: it moves the crop when the best subject window
// is materially better than the current saliency window, otherwise the
// original smartcrop result stands.
func subjectAwareNarrowSmartCropX(img image.Image, rawSrcX, srcX, srcW, srcH, cropW, cropH, thumbCropW int) (int, bool) {
	if img == nil || srcW <= 0 || srcH <= 0 || cropW <= 0 || cropH <= 0 || thumbCropW <= 0 || srcW <= cropW {
		return srcX, false
	}
	if float64(srcW)/float64(cropW) < 1.8 {
		return srcX, false
	}
	bounds := img.Bounds()
	tW := bounds.Dx()
	tH := bounds.Dy()
	if tW <= 0 || tH <= 0 || thumbCropW >= tW {
		return srcX, false
	}

	cols := subjectColumnWeights(img)
	if len(cols) != tW {
		return srcX, false
	}
	smooth := make([]float64, len(cols))
	for i := range cols {
		var sum float64
		var n int
		for j := i - 4; j <= i+4; j++ {
			if j < 0 || j >= len(cols) {
				continue
			}
			sum += cols[j]
			n++
		}
		if n > 0 {
			smooth[i] = sum / float64(n)
		}
	}

	windowN := tW - thumbCropW + 1
	if windowN <= 0 {
		return srcX, false
	}
	scores := make([]float64, windowN)
	var running float64
	for i := 0; i < thumbCropW; i++ {
		running += smooth[i]
	}
	scores[0] = running
	bestX := 0
	bestScore := running
	for x := 1; x < windowN; x++ {
		running += smooth[x+thumbCropW-1] - smooth[x-1]
		scores[x] = running
		if running > bestScore {
			bestScore = running
			bestX = x
		}
	}

	currentThumbX := int(math.Round(float64(srcX) * float64(tW) / float64(srcW)))
	currentThumbX = clampInt(currentThumbX, 0, windowN-1)
	currentScore := scores[currentThumbX]
	rawThumbX := int(math.Round(float64(rawSrcX) * float64(tW) / float64(srcW)))
	rawThumbX = clampInt(rawThumbX, 0, windowN-1)
	rawScore := scores[rawThumbX]
	total := 0.0
	for _, v := range smooth {
		total += v
	}
	if total < float64(tW*tH)*0.015 {
		return srcX, false
	}
	if bestScore < currentScore*1.08 || bestScore < total*0.18 {
		if rawX, ok := concentratedRawSubjectCropX(rawSrcX, srcX, srcW, cropW, rawScore, bestScore, total); ok {
			return rawX, true
		}
		return srcX, false
	}

	x := int(math.Round(float64(bestX) * float64(srcW) / float64(tW)))
	x = clampInt(roundEven(x), 0, srcW-cropW)
	if absInt(x-rawSrcX) > cropW/4 {
		maxX := srcW - cropW
		edgeGuard := cropW / 4
		rawNearEdge := rawSrcX <= edgeGuard || rawSrcX >= maxX-edgeGuard
		if rawNearEdge {
			// A strong, concentrated subject window is a better recovery
			// than geometric center when generic saliency has parked at a
			// frame edge.
			return x, true
		}
		// A concentrated foreground subject may legitimately be far from
		// a center-ish generic crop. Broad warm backgrounds remain guarded.
		if bestScore >= total*0.55 {
			return x, true
		}
		return srcX, false
	}
	return x, true
}

// headAwareNarrowSmartCropX is a conservative edge guard for reclining or
// low-positioned people. Generic saliency and whole-body motion naturally
// favour the torso; on a narrow portrait crop that can leave a clearly visible
// face cut by the left or right edge even though the current crop otherwise
// contains most of the person.
//
// This is intentionally not a general face detector. It only considers a
// compact, face-sized warm component in the middle/lower part of the frame.
// The component may sit just outside the chosen crop, but must overlap a
// tightly bounded edge halo; the crop then moves only far enough to restore a
// small safety margin. Upright people, products, animals, animation, broad
// warm furniture, posters near the top of the frame, lower-body regions, and
// already-safe crops stay on the released path.
func headAwareNarrowSmartCropX(img image.Image, srcX, srcW, cropW int) (int, bool) {
	if img == nil || srcW <= 0 || cropW <= 0 || srcW <= cropW ||
		float64(srcW)/float64(cropW) < 1.8 {
		return srcX, false
	}
	b := img.Bounds()
	if b.Dx() <= 0 || b.Dy() <= 0 {
		return srcX, false
	}
	w := minInt(320, b.Dx())
	h := maxInt(1, int(math.Round(float64(w)*float64(b.Dy())/float64(b.Dx()))))
	thumbCropW := clampInt(int(math.Round(float64(cropW)*float64(w)/float64(srcW))), 1, w)
	if thumbCropW >= w {
		return srcX, false
	}
	currentStart := clampInt(int(math.Round(float64(srcX)*float64(w)/float64(srcW))), 0, w-thumbCropW)
	currentEnd := currentStart + thumbCropW - 1
	// A face immediately outside a torso-centred crop is the failure mode this
	// guard exists to repair. Keep the halo small enough that an unrelated
	// person or warm object elsewhere in the scene cannot pull the crop across
	// the frame. At 1920 -> 9:16 this is about 150 source pixels.
	edgeHalo := maxInt(6, thumbCropW/4)

	pixels := normalizedSmartCropRGB(img, w, h)
	components := warmSubjectComponents(pixels, w, h, thumbCropW, 0, w-1)
	minScore := 350.0 * float64(w*h) / (320.0 * 180.0)
	var best warmSubjectComponent
	for _, component := range components {
		componentW := component.maxX - component.minX + 1
		componentH := component.maxY - component.minY + 1
		// Hands and forearms can be just as warm and connected as a face. The
		// failure is especially damaging beside a saturated cushion: protecting
		// the hand at the crop edge can push the actual face out of frame. A
		// useful reclining-head component is closer to square at this scale;
		// reject short hand blobs and broad furniture/hair regions here. The
		// bounds intentionally retain the exact-timestamp production reclining
		// face fixture (32x37 at 320x180) and both synthetic regressions.
		smallTallFace := componentW >= maxInt(6, w*5/100) &&
			componentH >= maxInt(10, h*12/100) &&
			componentH*10 >= componentW*11
		originalFace := componentW >= maxInt(6, w*10/100) &&
			componentH >= maxInt(10, h*15/100)
		if (!smallTallFace && !originalFace) || componentW > w*18/100 ||
			componentH > h*35/100 ||
			component.minY < h*30/100 || component.maxY > h*88/100 ||
			component.maxX < currentStart-edgeHalo || component.minX > currentEnd+edgeHalo ||
			component.score < minScore {
			continue
		}
		boxArea := maxInt(1, componentW*componentH)
		fill := float64(component.area) / float64(boxArea)
		if fill < 0.15 || fill > 0.78 {
			continue
		}
		if component.score > best.score {
			best = component
		}
	}
	if best.score == 0 {
		return srcX, false
	}

	margin := maxInt(4, thumbCropW/5)
	newStart := currentStart
	if best.minX < currentStart+margin {
		newStart = best.minX - margin
	} else if best.maxX > currentEnd-margin {
		newStart = best.maxX + margin - thumbCropW + 1
	} else {
		return srcX, false
	}
	newStart = clampInt(newStart, 0, w-thumbCropW)
	x := clampInt(roundEven(int(math.Round(float64(newStart)*float64(srcW)/float64(w)))), 0, srcW-cropW)
	if x == srcX {
		return srcX, false
	}
	return x, true
}

// tallSubjectExtentAwareNarrowSmartCropX recovers the upper end of a tall,
// connected skin component when a leaning head, neck, shoulder, and arm merge
// into one blob. A compact face detector cannot separate that shape, while
// torso-centred saliency can strand a substantial part of it outside the crop.
//
// Callers gate this with confident, subject-anchored temporal motion. The
// geometry below is therefore only an edge-containment refinement; it cannot
// make a static wall, cushion, or warm piece of furniture become the subject.
func tallSubjectExtentAwareNarrowSmartCropX(img image.Image, srcX, srcW, cropW int) (int, bool) {
	if img == nil || srcW <= cropW || cropW <= 0 || float64(srcW)/float64(cropW) < 1.8 {
		return srcX, false
	}
	b := img.Bounds()
	if b.Dx() <= 0 || b.Dy() <= 0 {
		return srcX, false
	}
	w := minInt(320, b.Dx())
	h := maxInt(1, int(math.Round(float64(w)*float64(b.Dy())/float64(b.Dx()))))
	thumbCropW := clampInt(int(math.Round(float64(cropW)*float64(w)/float64(srcW))), 1, w)
	if thumbCropW >= w {
		return srcX, false
	}
	currentStart := clampInt(int(math.Round(float64(srcX)*float64(w)/float64(srcW))), 0, w-thumbCropW)
	currentEnd := currentStart + thumbCropW - 1
	components := warmSubjectComponents(normalizedSmartCropRGB(img, w, h), w, h, thumbCropW, 0, w-1)
	var best warmSubjectComponent
	for _, component := range components {
		componentW := component.maxX - component.minX + 1
		componentH := component.maxY - component.minY + 1
		boxArea := maxInt(1, componentW*componentH)
		fill := float64(component.area) / float64(boxArea)
		if componentW < w*10/100 || componentW > w*25/100 ||
			componentH < h*50/100 || component.minY > h*45/100 || component.maxY < h*80/100 ||
			fill < 0.16 || fill > 0.75 {
			continue
		}
		leftClipped := maxInt(0, currentStart-component.minX)
		rightClipped := maxInt(0, component.maxX-currentEnd)
		if maxInt(leftClipped, rightClipped) < componentW/4 || leftClipped == rightClipped {
			continue
		}
		if component.score > best.score {
			best = component
		}
	}
	if best.score == 0 {
		return srcX, false
	}
	margin := maxInt(4, thumbCropW/5)
	newStart := currentStart
	if best.minX < currentStart {
		newStart = best.minX - margin
	} else if best.maxX > currentEnd {
		newStart = best.maxX + margin - thumbCropW + 1
	}
	newStart = clampInt(newStart, 0, w-thumbCropW)
	x := clampInt(roundEven(int(math.Round(float64(newStart)*float64(srcW)/float64(w)))), 0, srcW-cropW)
	if x == srcX {
		return srcX, false
	}
	return x, true
}

// recliningSubjectAwareNarrowSmartCropX protects the full horizontal extent
// of a reclining person when no isolated face-shaped component is available.
// A face can merge with a hand, hair, or patterned cushion and evade the
// compact head guard above, while the torso and head-side skin regions still
// form two strong, tall components along the lower edge of the frame.
//
// The rule is deliberately narrow: both components must be lower-frame,
// person-sized, similarly substantial, close to each other, and already touch
// the current crop or its small halo. It therefore does not turn generic warm
// furniture elsewhere in the room into a new subject. The correction moves
// only far enough to give the outer component breathing room.
func recliningSubjectAwareNarrowSmartCropX(img image.Image, srcX, srcW, cropW int) (int, bool) {
	if img == nil || srcW <= cropW || cropW <= 0 ||
		float64(srcW)/float64(cropW) < 1.8 {
		return srcX, false
	}
	b := img.Bounds()
	if b.Dx() <= 0 || b.Dy() <= 0 {
		return srcX, false
	}
	w := minInt(320, b.Dx())
	h := maxInt(1, int(math.Round(float64(w)*float64(b.Dy())/float64(b.Dx()))))
	thumbCropW := clampInt(int(math.Round(float64(cropW)*float64(w)/float64(srcW))), 1, w)
	if thumbCropW >= w {
		return srcX, false
	}
	currentStart := clampInt(int(math.Round(float64(srcX)*float64(w)/float64(srcW))), 0, w-thumbCropW)
	currentEnd := currentStart + thumbCropW - 1
	components := warmSubjectComponents(normalizedSmartCropRGB(img, w, h), w, h, thumbCropW, 0, w-1)
	minScore := 260.0 * float64(w*h) / (320.0 * 180.0)
	eligible := make([]warmSubjectComponent, 0, len(components))
	for _, component := range components {
		componentW := component.maxX - component.minX + 1
		componentH := component.maxY - component.minY + 1
		boxArea := maxInt(1, componentW*componentH)
		fill := float64(component.area) / float64(boxArea)
		if componentW < maxInt(6, w*6/100) || componentW > w*20/100 ||
			componentH < maxInt(10, h*18/100) || componentH > h*48/100 ||
			component.minY < h*55/100 || component.maxY < h*88/100 ||
			component.score < minScore || fill < 0.14 || fill > 0.82 {
			continue
		}
		eligible = append(eligible, component)
	}
	if len(eligible) < 2 {
		return srcX, false
	}

	halo := maxInt(6, thumbCropW/4)
	var left, right warmSubjectComponent
	bestScore := 0.0
	for i := 0; i < len(eligible); i++ {
		for j := i + 1; j < len(eligible); j++ {
			a, c := eligible[i], eligible[j]
			if a.centerX > c.centerX {
				a, c = c, a
			}
			gap := c.centerX - a.centerX
			span := c.maxX - a.minX + 1
			if gap < float64(thumbCropW)/3.0 || gap > float64(thumbCropW) ||
				span > thumbCropW*3/2 ||
				c.maxX < currentStart-halo || a.minX > currentEnd+halo {
				continue
			}
			ratio := a.score / c.score
			if ratio < 0.35 || ratio > 2.85 {
				continue
			}
			if score := a.score + c.score; score > bestScore {
				left, right, bestScore = a, c, score
			}
		}
	}
	if bestScore == 0 {
		return srcX, false
	}

	margin := maxInt(4, thumbCropW/5)
	newStart := currentStart
	if left.minX < currentStart+margin {
		newStart = left.minX - margin
	} else if right.maxX > currentEnd-margin {
		newStart = right.maxX + margin - thumbCropW + 1
	} else {
		return srcX, false
	}
	newStart = clampInt(newStart, 0, w-thumbCropW)
	x := clampInt(roundEven(int(math.Round(float64(newStart)*float64(srcW)/float64(w)))), 0, srcW-cropW)
	if x == srcX {
		return srcX, false
	}
	return x, true
}

func subjectColumnWeights(img image.Image) []float64 {
	bounds := img.Bounds()
	w := bounds.Dx()
	h := bounds.Dy()
	cols := make([]float64, w)
	for y := 0; y < h; y++ {
		yWeight := 1.0
		if y < h*8/100 {
			yWeight = 0.4
		} else if y > h*98/100 {
			yWeight = 0.5
		}
		for x := 0; x < w; x++ {
			r, g, b := rgb8(img.At(bounds.Min.X+x, bounds.Min.Y+y))
			weight := warmSubjectPixelWeight(r, g, b)
			if weight == 0 {
				continue
			}
			gray := gray8(r, g, b)
			var grad int
			if x > 0 {
				lr, lg, lb := rgb8(img.At(bounds.Min.X+x-1, bounds.Min.Y+y))
				grad += absInt(gray - gray8(lr, lg, lb))
			}
			if y > 0 {
				ur, ug, ub := rgb8(img.At(bounds.Min.X+x, bounds.Min.Y+y-1))
				grad += absInt(gray - gray8(ur, ug, ub))
			}
			edge := float64(grad) / 32.0
			if edge > 2 {
				edge = 2
			}
			cols[x] += weight * (0.25 + edge) * yWeight
		}
	}
	return cols
}

type smartCropSilhouetteWindow struct {
	Start        int
	Score        float64
	CurrentScore float64
	Total        float64
	RowCoverage  float64
}

// smartCropSilhouetteWindow finds a tall dark/neutral foreground region. It
// complements the warm-pixel detector for people wearing dark clothes, while
// its row-coverage and concentration metrics let callers reject ordinary dark
// furniture or isolated edges.
func findSmartCropSilhouetteWindow(img image.Image, currentStart, cropW int) (smartCropSilhouetteWindow, bool) {
	if img == nil {
		return smartCropSilhouetteWindow{}, false
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 || cropW <= 0 || cropW >= w {
		return smartCropSilhouetteWindow{}, false
	}
	cols := make([]float64, w)
	active := make([]bool, w*h)
	pixels := normalizedSmartCropRGB(img, w, h)
	for y := 0; y < h; y++ {
		yWeight := 1.0
		if y < h*8/100 {
			yWeight = 0.25
		}
		for x := 0; x < w; x++ {
			idx := (y*w + x) * 3
			r, g, bl := int(pixels[idx]), int(pixels[idx+1]), int(pixels[idx+2])
			gray := gray8(r, g, bl)
			spread := maxInt(r, maxInt(g, bl)) - minInt(r, minInt(g, bl))
			if gray < 12 || gray > 135 || spread > 90 {
				continue
			}
			var grad int
			if x > 0 {
				left := idx - 3
				lr, lg, lb := int(pixels[left]), int(pixels[left+1]), int(pixels[left+2])
				grad += absInt(gray - gray8(lr, lg, lb))
			}
			if y > 0 {
				up := idx - w*3
				ur, ug, ub := int(pixels[up]), int(pixels[up+1]), int(pixels[up+2])
				grad += absInt(gray - gray8(ur, ug, ub))
			}
			edge := math.Min(float64(grad)/32.0, 2.0)
			cols[x] += (0.35 + edge) * (float64(136-gray) / 124.0) * yWeight
			active[y*w+x] = true
		}
	}
	smooth := smoothColumns(cols, 7)
	windowN := w - cropW + 1
	scores := make([]float64, windowN)
	var running, total float64
	for _, score := range smooth {
		total += score
	}
	for x := 0; x < cropW; x++ {
		running += smooth[x]
	}
	scores[0] = running
	bestStart, bestScore := 0, running
	for start := 1; start < windowN; start++ {
		running += smooth[start+cropW-1] - smooth[start-1]
		scores[start] = running
		if running > bestScore {
			bestStart, bestScore = start, running
		}
	}
	coveredRows := 0
	for y := 0; y < h; y++ {
		pixels := 0
		for x := bestStart; x < bestStart+cropW; x++ {
			if active[y*w+x] {
				pixels++
			}
		}
		if pixels >= maxInt(3, cropW/25) {
			coveredRows++
		}
	}
	currentStart = clampInt(currentStart, 0, windowN-1)
	return smartCropSilhouetteWindow{
		Start:        bestStart,
		Score:        bestScore,
		CurrentScore: scores[currentStart],
		Total:        total,
		RowCoverage:  float64(coveredRows) / float64(h),
	}, total > 0
}

func silhouetteAwareNarrowSmartCropX(img image.Image, srcX, srcW, cropW, thumbCropW int) (int, bool) {
	if img == nil || srcW <= cropW || cropW <= 0 || thumbCropW <= 0 ||
		float64(srcW)/float64(cropW) < 1.8 {
		return srcX, false
	}
	b := img.Bounds()
	if b.Dx() <= thumbCropW || b.Dy() <= 0 {
		return srcX, false
	}
	windowN := b.Dx() - thumbCropW + 1
	currentStart := clampInt(int(math.Round(float64(srcX)*float64(b.Dx())/float64(srcW))), 0, windowN-1)
	x, candidate, ok := strongSmartCropSilhouetteX(img, srcW, cropW, thumbCropW, currentStart)
	if !ok || candidate.Score < candidate.CurrentScore*1.20 {
		return srcX, false
	}
	if candidate.Start == 0 && candidate.Score < candidate.CurrentScore*1.50 {
		return srcX, false
	}
	if absInt(x-srcX) <= maxInt(24, cropW/10) {
		return srcX, false
	}
	return x, true
}

func strongSmartCropSilhouetteX(img image.Image, srcW, cropW, thumbCropW, currentStart int) (int, smartCropSilhouetteWindow, bool) {
	if img == nil || srcW <= cropW || cropW <= 0 || thumbCropW <= 0 {
		return 0, smartCropSilhouetteWindow{}, false
	}
	b := img.Bounds()
	candidate, ok := findSmartCropSilhouetteWindow(img, currentStart, thumbCropW)
	if !ok || candidate.Total < float64(b.Dx()*b.Dy())*0.04 ||
		candidate.Score < candidate.Total*0.55 || candidate.RowCoverage < 0.65 ||
		(candidate.Start == 0 && candidate.RowCoverage < 0.85) {
		return 0, candidate, false
	}
	x := clampInt(roundEven(int(math.Round(float64(candidate.Start)*float64(srcW)/float64(b.Dx())))), 0, srcW-cropW)
	return x, candidate, true
}

func motionAwareNarrowSmartCropX(
	ctx context.Context,
	app *sdk.AppCtx,
	sc *storageClient,
	projectID, sourceFileID string,
	derivs []DerivationRow,
	cropSource DerivationRow,
	img image.Image,
	srcX, srcW, srcH, cropW, cropH, thumbCropW int,
) (int, bool) {
	if img == nil || cropSource.Kind != "keyframe" ||
		srcW <= 0 || srcH <= 0 || cropW <= 0 || cropH <= 0 ||
		thumbCropW <= 0 || srcW <= cropW ||
		float64(srcW)/float64(cropW) < 1.8 {
		return srcX, false
	}
	prev, next := neighboringKeyframes(derivs, cropSource.PositionMs)
	neighbors := make([]image.Image, 0, 2)
	for _, d := range []DerivationRow{prev, next} {
		if d.StorageFileID == "" {
			continue
		}
		neighbor, err := downloadAndDecodeImage(ctx, sc, projectID, d.StorageFileID)
		if err != nil {
			if app != nil {
				app.Logger().Info("smartcrop motion neighbor unavailable",
					"file_id", sourceFileID,
					"neighbor_file_id", d.StorageFileID,
					"neighbor_position_ms", d.PositionMs,
					"err", err.Error())
			}
			continue
		}
		neighbors = append(neighbors, neighbor)
	}
	if len(neighbors) == 0 {
		return srcX, false
	}
	return motionAwareNarrowSmartCropXFromImages(img, neighbors, srcX, srcW, cropW, thumbCropW)
}

func motionAwareNarrowSmartCropXFromImages(img image.Image, neighbors []image.Image, srcX, srcW, cropW, thumbCropW int) (int, bool) {
	bounds := img.Bounds()
	tW := bounds.Dx()
	tH := bounds.Dy()
	if tW <= 0 || tH <= 0 || thumbCropW <= 0 || thumbCropW >= tW || srcW <= cropW {
		return srcX, false
	}
	usable := make([]image.Image, 0, len(neighbors))
	for _, n := range neighbors {
		if n == nil {
			continue
		}
		nb := n.Bounds()
		if nb.Dx() == tW && nb.Dy() == tH {
			usable = append(usable, n)
		}
	}
	if len(usable) == 0 {
		return srcX, false
	}

	cols := make([]float64, tW)
	for y := 0; y < tH; y++ {
		yWeight := 1.0
		if y < tH*10/100 {
			yWeight = 0.2
		} else if y > tH*95/100 {
			yWeight = 0.7
		}
		for x := 0; x < tW; x++ {
			r, g, b := rgb8(img.At(bounds.Min.X+x, bounds.Min.Y+y))
			var maxDiff float64
			minDiff := math.Inf(1)
			for _, n := range usable {
				nb := n.Bounds()
				nr, ng, nbv := rgb8(n.At(nb.Min.X+x, nb.Min.Y+y))
				diff := float64(absInt(r-nr)+absInt(g-ng)+absInt(b-nbv)) / 3.0
				if diff > maxDiff {
					maxDiff = diff
				}
				if diff < minDiff {
					minDiff = diff
				}
			}
			// With frames on both sides, require activity against both. Using
			// the maximum creates motion ghosts at the subject's previous and
			// next positions and can make the crop chase an empty room region.
			motionDiff := maxDiff
			if len(usable) >= 2 {
				motionDiff = minDiff
			}
			weight := (motionDiff - 12.0) / 40.0
			if weight <= 0 {
				continue
			}
			if weight > 1 {
				weight = 1
			}
			cols[x] += weight * yWeight
		}
	}
	smooth := smoothColumns(cols, 9)
	bestX, bestScore, currentScore, total, ok := bestColumnWindow(smooth, thumbCropW, srcX, srcW)
	if !ok {
		return srcX, false
	}
	// Use motion only when it is strong and concentrated. Uniform
	// whole-frame motion usually means camera movement or exposure
	// flicker, not a subject we should chase.
	if total < float64(tW*tH)*0.02 || bestScore < total*0.38 || bestScore < currentScore*1.03 {
		return srcX, false
	}
	x := int(math.Round(float64(bestX) * float64(srcW) / float64(tW)))
	return clampInt(roundEven(x), 0, srcW-cropW), true
}

func neighboringKeyframes(derivs []DerivationRow, positionMs int64) (DerivationRow, DerivationRow) {
	var prev DerivationRow
	var next DerivationRow
	for _, d := range derivs {
		if d.Kind != "keyframe" || d.Status != "ok" || d.StorageFileID == "" || d.PositionMs == positionMs {
			continue
		}
		if d.PositionMs < positionMs && (prev.StorageFileID == "" || d.PositionMs > prev.PositionMs) {
			prev = d
		}
		if d.PositionMs > positionMs && (next.StorageFileID == "" || d.PositionMs < next.PositionMs) {
			next = d
		}
	}
	return prev, next
}

func smoothColumns(cols []float64, width int) []float64 {
	if width <= 1 {
		out := make([]float64, len(cols))
		copy(out, cols)
		return out
	}
	radius := width / 2
	out := make([]float64, len(cols))
	for i := range cols {
		var sum float64
		var n int
		for j := i - radius; j <= i+radius; j++ {
			if j < 0 || j >= len(cols) {
				continue
			}
			sum += cols[j]
			n++
		}
		if n > 0 {
			out[i] = sum / float64(n)
		}
	}
	return out
}

func bestColumnWindow(cols []float64, windowW, srcX, srcW int) (bestX int, bestScore, currentScore, total float64, ok bool) {
	windowN := len(cols) - windowW + 1
	if windowN <= 0 || srcW <= 0 {
		return 0, 0, 0, 0, false
	}
	for _, v := range cols {
		total += v
	}
	var running float64
	for i := 0; i < windowW; i++ {
		running += cols[i]
	}
	bestScore = running
	scores := make([]float64, windowN)
	scores[0] = running
	for x := 1; x < windowN; x++ {
		running += cols[x+windowW-1] - cols[x-1]
		scores[x] = running
		if running > bestScore {
			bestScore = running
			bestX = x
		}
	}
	currentX := int(math.Round(float64(srcX) * float64(len(cols)) / float64(srcW)))
	currentX = clampInt(currentX, 0, windowN-1)
	return bestX, bestScore, scores[currentX], total, true
}

func rgb8(c color.Color) (int, int, int) {
	r, g, b, _ := c.RGBA()
	return int(r >> 8), int(g >> 8), int(b >> 8)
}

func gray8(r, g, b int) int {
	return (299*r + 587*g + 114*b) / 1000
}

func warmSubjectPixelWeight(r, g, b int) float64 {
	maxC := maxInt(r, maxInt(g, b))
	minC := minInt(r, minInt(g, b))
	if maxC <= 0 {
		return 0
	}
	sat := float64(maxC-minC) / float64(maxC)
	cb := 128.0 - 0.168736*float64(r) - 0.331264*float64(g) + 0.5*float64(b)
	cr := 128.0 + 0.5*float64(r) - 0.418688*float64(g) - 0.081312*float64(b)
	if r <= 55 || g <= 30 || b <= 20 ||
		float64(r) <= float64(g)*1.03 ||
		float64(r) <= float64(b)*1.12 ||
		maxC-minC <= 12 ||
		cb < 75 || cb > 135 ||
		cr < 128 || cr > 185 ||
		sat <= 0.12 {
		return 0
	}
	return (0.2 + sat*2.0)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func clampInt(v, minV, maxV int) int {
	if maxV < minV {
		return minV
	}
	if v < minV {
		return minV
	}
	if v > maxV {
		return maxV
	}
	return v
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// pickSmartCropDerivation returns the best cached image to feed to
// the saliency analyzer. Timed operations prefer an ok keyframe in
// the requested clip range, then the nearest ok keyframe to the
// focus timestamp, then fall back to thumbnail. Audio-only sources
// fall back to waveform as a second-best saliency input (waveforms
// still have edges + saturation that smartcrop can score).
func pickSmartCropDerivation(derivs []DerivationRow, target smartCropTarget) DerivationRow {
	if target.PreferKeyframe {
		if best := bestKeyframeDerivation(derivs, target, true); best.StorageFileID != "" {
			return best
		}
		if best := bestKeyframeDerivation(derivs, target, false); best.StorageFileID != "" {
			return best
		}
	}
	for _, d := range derivs {
		if d.Kind == "thumbnail" && d.Status == "ok" && d.StorageFileID != "" {
			return d
		}
	}
	for _, d := range derivs {
		if d.Kind == "waveform" && d.Status == "ok" && d.StorageFileID != "" {
			return d
		}
	}
	return DerivationRow{}
}

func bestKeyframeDerivation(derivs []DerivationRow, target smartCropTarget, requireInRange bool) DerivationRow {
	var best DerivationRow
	var bestDist int64
	for _, d := range derivs {
		if d.Kind != "keyframe" || d.Status != "ok" || d.StorageFileID == "" {
			continue
		}
		if requireInRange && target.HasRange() && (d.PositionMs < target.StartMs || d.PositionMs > target.EndMs) {
			continue
		}
		dist := absInt64(d.PositionMs - target.FocusMs)
		if best.StorageFileID == "" || dist < bestDist || (dist == bestDist && d.PositionMs < best.PositionMs) {
			best = d
			bestDist = dist
		}
	}
	return best
}

type smartCropTarget struct {
	FocusMs        int64
	StartMs        int64
	EndMs          int64
	PreferKeyframe bool
}

func (t smartCropTarget) HasRange() bool {
	return t.EndMs > t.StartMs
}

func (t smartCropTarget) Normalized() smartCropTarget {
	if t.FocusMs < 0 {
		t.FocusMs = 0
	}
	if t.StartMs < 0 {
		t.StartMs = 0
	}
	if t.EndMs < 0 {
		t.EndMs = 0
	}
	return t
}

func absInt64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// downloadAndDecodeImage pulls a storage file's bytes via the
// cross-app HTTP client and decodes them as an image. JPEG, PNG and
// GIF are accepted (the underscore imports above register the
// decoders).
func downloadAndDecodeImage(ctx context.Context, sc *storageClient, projectID, fileIDStr string) (image.Image, error) {
	fid, err := strconv.ParseInt(fileIDStr, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("thumbnail storage_file_id %q: %w", fileIDStr, err)
	}
	var buf imageBuffer
	if err := sc.DownloadContent(ctx, projectID, fid, &buf); err != nil {
		return nil, fmt.Errorf("download thumbnail bytes: %w", err)
	}
	img, _, err := image.Decode(&buf)
	if err != nil {
		// Try JPEG explicitly as a defence-in-depth — some camera
		// JPEGs use APP-segment dialects image.Decode rejects via the
		// generic dispatcher.
		buf.reset()
		img, err = jpeg.Decode(&buf)
		if err != nil {
			return nil, fmt.Errorf("decode thumbnail: %w", err)
		}
	}
	return img, nil
}

// imageBuffer is a tiny io.Writer + io.Reader bridge backed by a
// growing byte slice. We can't reuse bytes.Buffer for the post-write
// re-read path because storageclient's DownloadContent only takes
// io.Writer, and image.Decode needs io.Reader on the same bytes.
// Standard library bytes.Buffer does support both, but we wrap it so
// the reset() call after a failed Decode is explicit + harder to
// forget if the file gains another fallback decode pass.
type imageBuffer struct {
	buf []byte
	pos int
}

func (b *imageBuffer) Write(p []byte) (int, error) {
	b.buf = append(b.buf, p...)
	return len(p), nil
}

func (b *imageBuffer) Read(p []byte) (int, error) {
	if b.pos >= len(b.buf) {
		return 0, io.EOF
	}
	n := copy(p, b.buf[b.pos:])
	b.pos += n
	return n, nil
}

func (b *imageBuffer) reset() { b.pos = 0 }

// ─── parameter mutation helpers ───────────────────────────────────────
//
// preprocessSmartCrop inspects a render's params, resolves a smart-
// crop window if the operation supports it, and rewrites params with
// concrete crop_w/crop_h/crop_x/crop_y fields. The per-op planner
// then sees explicit numbers and emits a literal `crop=W:H:X:Y`
// filter instead of the symbolic `iw/ih`-based expression.
//
// No-op for operations that don't support target-ratio cropping
// (trim, concat, audio_extract, …) or malformed params. "crop_mode:
// center" still runs this pre-pass so the planner receives explicit,
// display-space crop coordinates.

// preprocessSmartCrop returns the (possibly rewritten) params bytes.
// Original bytes are returned unchanged on any error or no-op path —
// callers can safely overwrite row.Params with the result.
func preprocessSmartCrop(
	ctx context.Context,
	app *sdk.AppCtx,
	sc *storageClient,
	projectID, op string,
	sources []string,
	params []byte,
) []byte {
	if op != "extract_reel" && op != "extract_frame" && op != "crop" {
		return params
	}
	if len(sources) != 1 {
		return params
	}
	parsed := map[string]any{}
	if len(params) > 0 {
		_ = json.Unmarshal(params, &parsed)
	}
	// Already explicit — caller pre-supplied coords or a previous
	// pre-pass already mutated; skip.
	if _, ok := parsed["crop_w"]; ok {
		return params
	}
	tr, _ := parsed["target_ratio"].(string)
	if strings.TrimSpace(tr) == "" {
		// extract_frame and crop default to no crop; extract_reel defaults to 9:16.
		if op == "extract_reel" {
			tr = "9:16"
		} else {
			return params
		}
	}
	rw, rh, err := parseAspectRatio(tr)
	if err != nil {
		return params
	}
	mode, _ := parsed["crop_mode"].(string)
	mode = strings.TrimSpace(strings.ToLower(mode))
	if mode == "" {
		mode = "smart" // default — the whole point of v0.12.7
	}
	if mode != "smart" && mode != "center" {
		return params
	}
	target := smartCropFocus(op, parsed)
	// V2 uses a bounded sample set. Dense storyboards stay on the cached fast
	// path; sparse indexes are supplemented with temporary source screenshots.
	// Source-sampling failures still fall through safely to v1.
	if op == "extract_reel" && mode == "smart" {
		if win, path, v2Err := computeSmartCropReelV2(ctx, app, sc, projectID, sources[0], rw, rh, target); v2Err == nil {
			parsed["crop_w"] = win.W
			parsed["crop_h"] = win.H
			parsed["crop_x"] = win.X
			parsed["crop_y"] = win.Y
			parsed["crop_mode"] = mode
			parsed["crop_version"] = "v2"
			if len(path) > 1 {
				parsed["crop_path"] = path
			}
			if out, marshalErr := json.Marshal(parsed); marshalErr == nil {
				return out
			}
		} else {
			app.Logger().Info("smartcrop v2 fallback to v1",
				"op", op, "file_id", sources[0], "reason", v2Err.Error())
		}
	}
	if (op == "extract_frame" || op == "crop") && mode == "smart" {
		if win, v2Err := computeSmartCropStillV2(ctx, app, sc, projectID, sources[0], rw, rh, target); v2Err == nil {
			parsed["crop_w"] = win.W
			parsed["crop_h"] = win.H
			parsed["crop_x"] = win.X
			parsed["crop_y"] = win.Y
			parsed["crop_mode"] = mode
			parsed["crop_version"] = "v2"
			if out, marshalErr := json.Marshal(parsed); marshalErr == nil {
				return out
			}
		} else {
			app.Logger().Info("smartcrop v2 fallback to v1",
				"op", op, "file_id", sources[0], "reason", v2Err.Error())
		}
	}
	win, err := computeSmartCrop(ctx, app, sc, projectID, sources[0], rw, rh, mode, target)
	if err != nil {
		// Symbolic filter is fine — log + skip.
		app.Logger().Info("smartcrop preprocess skipped",
			"op", op, "file_id", sources[0], "reason", err.Error())
		return params
	}
	parsed["crop_w"] = win.W
	parsed["crop_h"] = win.H
	parsed["crop_x"] = win.X
	parsed["crop_y"] = win.Y
	// Keep crop_mode for downstream logging (panel can show "smart" vs
	// "center"), but the planner reads only crop_w/h/x/y.
	parsed["crop_mode"] = mode
	out, err := json.Marshal(parsed)
	if err != nil {
		return params
	}
	return out
}

func smartCropFocus(op string, parsed map[string]any) smartCropTarget {
	switch op {
	case "extract_reel":
		startMs := int64FromJSONValue(parsed["start_ms"])
		endMs := int64FromJSONValue(parsed["end_ms"])
		focusMs := startMs
		if endMs > startMs {
			focusMs = startMs + (endMs-startMs)/2
		}
		return smartCropTarget{
			FocusMs:        focusMs,
			StartMs:        startMs,
			EndMs:          endMs,
			PreferKeyframe: true,
		}.Normalized()
	case "extract_frame":
		atMs := int64FromJSONValue(parsed["at_ms"])
		return smartCropTarget{
			FocusMs:        atMs,
			StartMs:        atMs,
			EndMs:          atMs,
			PreferKeyframe: true,
		}.Normalized()
	default:
		return smartCropTarget{}
	}
}

func int64FromJSONValue(v any) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case int:
		return int64(x)
	case float64:
		return int64(x)
	case json.Number:
		n, _ := x.Int64()
		return n
	default:
		return 0
	}
}
