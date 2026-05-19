package main

// Subject-aware crop pre-pass for extract_reel / extract_frame.
//
// Computes the crop window in source-pixel space at the per-render
// preprocess step (before buildPlan). Two modes:
//
//   "center" — geometric center of the source. Same as the
//              filter-expression-based crop the planners used pre-v0.12.7.
//
//   "smart"  — runs muesli/smartcrop against the source's cached
//              thumbnail derivation. Saliency-based (edge density,
//              saturation, skin-tone proxy), no ML model, no GPU,
//              <100 ms per call. Falls back to center if the thumbnail
//              isn't available yet, has zero dimensions, or smartcrop
//              errors — so the render always proceeds, even on a
//              freshly-uploaded file the indexer hasn't derived from
//              yet.
//
// Why the cached thumbnail and not a fresh ffmpeg pass: we already
// have a representative frame (the local + remote indexers run their
// own seek-and-luma-check pipeline to pick one). Reusing it avoids a
// second download + ffmpeg invocation for what's just a hint to the
// saliency analyzer. The crop result is mapped from thumbnail-pixel
// space back to source-pixel space using the known
// thumbnail-vs-source dimensions.

import (
	"context"
	"encoding/json"
	"fmt"
	"image"
	_ "image/gif" // accept GIFs in case a future thumbnail derivation switches format
	"image/jpeg"
	_ "image/png" // accept PNGs (waveform falls back here for audio-only sources)
	"io"
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
//   targetW, targetH — ratio numerator/denominator (e.g. 9, 16). The
//                      returned W:H is normalised so cropW/cropH ==
//                      targetW/targetH within rounding.
//   mode             — "smart" (recommended default) | "center".
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

	// Smart mode — needs the source's cached thumbnail derivation. If
	// it's missing, fall back to center rather than failing the
	// render (the next re-render after the indexer catches up will
	// re-evaluate).
	thumbID := pickThumbnailDerivation(row.Derivations)
	if thumbID == "" {
		app.Logger().Info("smartcrop fallback to center: no thumbnail derivation yet",
			"file_id", sourceFileID)
		return center, nil
	}
	thumb, err := downloadAndDecodeImage(ctx, sc, projectID, thumbID)
	if err != nil {
		app.Logger().Warn("smartcrop fallback to center: thumbnail download/decode failed",
			"file_id", sourceFileID, "thumb_id", thumbID, "err", err.Error())
		return center, nil
	}
	tBounds := thumb.Bounds()
	tW := tBounds.Dx()
	tH := tBounds.Dy()
	if tW <= 0 || tH <= 0 {
		app.Logger().Warn("smartcrop fallback to center: zero-sized thumbnail",
			"file_id", sourceFileID, "thumb_id", thumbID)
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

// pickThumbnailDerivation returns the storage_file_id of the most
// recent ok-status thumbnail derivation, or "" if none exist. Audio-
// only sources fall back to the waveform derivation as a second-best
// saliency input (waveforms still have edges + saturation that
// smartcrop can score).
func pickThumbnailDerivation(derivs []DerivationRow) string {
	for _, d := range derivs {
		if d.Kind == "thumbnail" && d.Status == "ok" && d.StorageFileID != "" {
			return d.StorageFileID
		}
	}
	for _, d := range derivs {
		if d.Kind == "waveform" && d.Status == "ok" && d.StorageFileID != "" {
			return d.StorageFileID
		}
	}
	return ""
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
// No-op for operations that don't support cropping (trim, concat,
// audio_extract, …) or for params that explicitly opt into
// "crop_mode: center" (which the symbolic filter already handles).

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
	if op != "extract_reel" && op != "extract_frame" {
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
		// extract_frame defaults to no crop; extract_reel defaults to 9:16.
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
	win, err := computeSmartCrop(ctx, app, sc, projectID, sources[0], rw, rh, mode)
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
