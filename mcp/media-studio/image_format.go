package main

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/gif"
	"image/jpeg"
	"image/png"
	"strings"

	"golang.org/x/image/webp"
)

const enforcedImageQuality = 90

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
		return "", nil
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
// onto white. A requested WebP output is accepted unchanged when the provider
// returns WebP; a mismatch fails instead of storing mislabeled bytes.
func enforceImageOutputFormat(m generatedMedia, data []byte, format string) (generatedMedia, []byte, error) {
	m = withSniffedImageMediaType(m, data)
	if format == "" || imageMediaMatchesFormat(m.MimeType, format) {
		return m, data, nil
	}

	img, err := decodeGeneratedImage(data, m.MimeType)
	if err != nil {
		return m, nil, fmt.Errorf("decode provider image for %s conversion: %w", format, err)
	}

	var buf bytes.Buffer
	switch format {
	case "jpeg":
		if err := jpeg.Encode(&buf, flattenImageOnWhite(img), &jpeg.Options{Quality: enforcedImageQuality}); err != nil {
			return m, nil, fmt.Errorf("encode jpeg: %w", err)
		}
		m.MimeType, m.Ext = "image/jpeg", "jpg"
	case "png":
		if err := png.Encode(&buf, img); err != nil {
			return m, nil, fmt.Errorf("encode png: %w", err)
		}
		m.MimeType, m.Ext = "image/png", "png"
	case "webp":
		return m, nil, fmt.Errorf("provider returned %s; local webp encoding is unavailable", m.MimeType)
	default:
		return m, nil, fmt.Errorf("unsupported output format %q", format)
	}

	converted := buf.Bytes()
	m.B64 = base64.StdEncoding.EncodeToString(converted)
	m.UpstreamURL = ""
	return m, converted, nil
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
	draw.Draw(dst, dst.Bounds(), &image.Uniform{C: color.White}, image.Point{}, draw.Src)
	draw.Draw(dst, dst.Bounds(), src, bounds.Min, draw.Over)
	return dst
}
