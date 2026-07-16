package main

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	stddraw "image/draw"
	_ "image/gif"
	"image/jpeg"
	"image/png"
	"strings"

	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/webp"
)

const (
	enforcedImageQuality     = 90
	minimumJPEGQuality       = 70
	maxGeneratedImageBytes   = 2_000_000
	generatedImageScaleRatio = 0.85
)

// requestedImageOutputFormat returns the canonical final format requested by
// the caller. options.format remains an input alias for older Venice callers.
func requestedImageOutputFormat(args map[string]any) (string, error) {
	format := strArg(args, "output_format", "")
	if opts, ok := args["options"].(map[string]any); ok {
		if v := strArg(opts, "output_format", ""); v != "" {
			format = v
		} else if v := strArg(opts, "format", ""); v != "" {
			format = v
		}
	}
	format = strings.ToLower(strings.TrimSpace(format))
	switch format {
	case "":
		return "jpeg", nil
	case "jpg", "jpeg":
		return "jpeg", nil
	case "png", "webp":
		return format, nil
	default:
		return "", fmt.Errorf("unsupported output_format %q (png|jpeg|webp)", format)
	}
}

func canonicalizeImageOutputFormat(args map[string]any, format string) {
	if format == "" {
		return
	}
	opts, ok := args["options"].(map[string]any)
	if !ok {
		opts = map[string]any{}
		args["options"] = opts
	}
	opts["output_format"] = format
	delete(opts, "format")
}

// enforceImageOutputFormat makes output_format an application-level
// guarantee rather than only a provider hint. The provider still receives the
// preference, but mismatched bytes are re-encoded before thumbnailing, cache,
// or Storage. JPEG has no alpha channel, so transparent pixels are flattened
// onto white. Default JPEGs are also kept strictly below 2 MB. The encoder
// starts at quality 90, lowers quality no further than 70, then progressively
// scales dimensions only when compression alone cannot satisfy the limit.
func enforceImageOutputFormat(m generatedMedia, data []byte, format string) (generatedMedia, []byte, error) {
	m = withSniffedImageMediaType(m, data)
	if format == "" || (imageMediaMatchesFormat(m.MimeType, format) && (format != "jpeg" || len(data) < maxGeneratedImageBytes)) {
		return m, data, nil
	}

	img, err := decodeGeneratedImage(data, m.MimeType)
	if err != nil {
		return m, nil, fmt.Errorf("decode provider image for %s conversion: %w", format, err)
	}

	var converted []byte
	switch format {
	case "jpeg":
		converted, err = encodeJPEGUnderLimit(img, maxGeneratedImageBytes)
		if err != nil {
			return m, nil, fmt.Errorf("encode jpeg: %w", err)
		}
		m.MimeType, m.Ext = "image/jpeg", "jpg"
	case "png":
		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err != nil {
			return m, nil, fmt.Errorf("encode png: %w", err)
		}
		converted = buf.Bytes()
		m.MimeType, m.Ext = "image/png", "png"
	case "webp":
		return m, nil, fmt.Errorf("provider returned %s; local webp encoding is unavailable", m.MimeType)
	default:
		return m, nil, fmt.Errorf("unsupported output format %q", format)
	}

	m.B64 = base64.StdEncoding.EncodeToString(converted)
	m.UpstreamURL = ""
	return m, converted, nil
}

func encodeJPEGUnderLimit(src image.Image, maxBytes int) ([]byte, error) {
	if maxBytes <= 0 {
		return nil, fmt.Errorf("invalid maximum size %d", maxBytes)
	}
	current := flattenImageOnWhite(src)
	for {
		atDefault, err := encodeJPEG(current, enforcedImageQuality)
		if err != nil {
			return nil, err
		}
		if len(atDefault) < maxBytes {
			return atDefault, nil
		}

		atMinimum, err := encodeJPEG(current, minimumJPEGQuality)
		if err != nil {
			return nil, err
		}
		if len(atMinimum) < maxBytes {
			best := atMinimum
			low, high := minimumJPEGQuality+1, enforcedImageQuality-1
			for low <= high {
				quality := low + (high-low)/2
				candidate, err := encodeJPEG(current, quality)
				if err != nil {
					return nil, err
				}
				if len(candidate) < maxBytes {
					best = candidate
					low = quality + 1
				} else {
					high = quality - 1
				}
			}
			return best, nil
		}

		bounds := current.Bounds()
		if bounds.Dx() <= 1 && bounds.Dy() <= 1 {
			return nil, fmt.Errorf("cannot encode image below %d bytes", maxBytes)
		}
		width := max(1, int(float64(bounds.Dx())*generatedImageScaleRatio))
		height := max(1, int(float64(bounds.Dy())*generatedImageScaleRatio))
		resized := image.NewRGBA(image.Rect(0, 0, width, height))
		xdraw.CatmullRom.Scale(resized, resized.Bounds(), current, bounds, stddraw.Src, nil)
		current = resized
	}
}

func encodeJPEG(src image.Image, quality int) ([]byte, error) {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, src, &jpeg.Options{Quality: quality}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func imageMediaMatchesFormat(mimeType, format string) bool {
	mimeType = strings.ToLower(strings.TrimSpace(mimeType))
	switch format {
	case "jpeg":
		return mimeType == "image/jpeg" || mimeType == "image/jpg"
	case "png":
		return mimeType == "image/png"
	case "webp":
		return mimeType == "image/webp"
	default:
		return false
	}
}

func decodeGeneratedImage(data []byte, mimeType string) (image.Image, error) {
	if strings.EqualFold(strings.TrimSpace(mimeType), "image/webp") {
		return webp.Decode(bytes.NewReader(data))
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	return img, err
}

func flattenImageOnWhite(src image.Image) image.Image {
	bounds := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	stddraw.Draw(dst, dst.Bounds(), &image.Uniform{C: color.White}, image.Point{}, stddraw.Src)
	stddraw.Draw(dst, dst.Bounds(), src, bounds.Min, stddraw.Over)
	return dst
}
