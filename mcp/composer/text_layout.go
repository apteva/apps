package main

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/font/opentype"
)

type v1TextLayout struct {
	body     string
	fontSize int
	lines    []string
	width    int
	height   int
}

func layoutV1Text(c Clip, body string, fontSize, canvasW, canvasH int) v1TextLayout {
	result := v1TextLayout{body: body, fontSize: fontSize, lines: strings.Split(body, "\n")}
	style := c.Asset.Style
	if style == nil || (!style.Wrap && !style.AutoSize && style.MaxWidth <= 0 && style.MaxHeight <= 0) {
		return result
	}
	maxWidth := textMeasure(style.MaxWidth, canvasW, int(float64(canvasW)*0.84))
	maxHeight := textMeasure(style.MaxHeight, canvasH, int(float64(canvasH)*0.8))
	maxWidth -= style.Padding * 2
	maxHeight -= style.Padding * 2
	maxWidth = maxInt(1, maxWidth)
	maxHeight = maxInt(1, maxHeight)
	minSize := style.MinFontSize
	if minSize <= 0 {
		minSize = 12
	}
	if minSize > fontSize {
		minSize = fontSize
	}
	for {
		face := v1TextFace(c.Asset.Font, fontSize)
		lines := strings.Split(body, "\n")
		if style.Wrap || style.MaxWidth > 0 {
			lines = wrapText(body, face, maxWidth)
		}
		lineHeight := float64(fontSize) * 1.22
		if style.LineHeight > 0 {
			lineHeight = float64(fontSize) * style.LineHeight
		}
		drawer := &font.Drawer{Face: face}
		width := 0
		for _, line := range lines {
			width = maxInt(width, drawer.MeasureString(line).Ceil())
		}
		height := int(math.Ceil(lineHeight * float64(len(lines))))
		result = v1TextLayout{body: strings.Join(lines, "\n"), fontSize: fontSize, lines: lines, width: width, height: height}
		if !style.AutoSize || height <= maxHeight || fontSize <= minSize {
			return result
		}
		fontSize = maxInt(minSize, int(math.Floor(float64(fontSize)*0.92)))
	}
}

func v1TextFace(spec *TextFont, size int) font.Face {
	faceDef := composerFontFor(spec)
	parsed, err := opentype.Parse(faceDef.Data)
	if err != nil {
		return basicfont.Face7x13
	}
	face, err := opentype.NewFace(parsed, &opentype.FaceOptions{Size: float64(size), DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		return basicfont.Face7x13
	}
	return face
}

func textMeasure(value float64, canvas, fallback int) int {
	if value <= 0 {
		return fallback
	}
	if value <= 1 {
		return int(math.Round(value * float64(canvas)))
	}
	return int(math.Round(value))
}

func v1TextSafeAreaWarnings(edit *Edit) []string {
	var warnings []string
	for _, c := range textOverlayClips(edit) {
		style := c.Asset.Style
		if style == nil || style.SafeArea <= 0 || c.Position == nil {
			continue
		}
		x, xOK := normalizedTextPosition(c.Position.X)
		y, yOK := normalizedTextPosition(c.Position.Y)
		if (xOK && (x < style.SafeArea || x > 1-style.SafeArea)) || (yOK && (y < style.SafeArea || y > 1-style.SafeArea)) {
			warnings = append(warnings, fmt.Sprintf("text clip %s anchor is outside the %.0f%% safe area", clipLabel(c), style.SafeArea*100))
		}
	}
	return warnings
}

func normalizedTextPosition(raw string) (float64, bool) {
	raw = strings.TrimSpace(raw)
	if !strings.HasSuffix(raw, "%") {
		return 0, false
	}
	value, err := strconv.ParseFloat(strings.TrimSuffix(raw, "%"), 64)
	return value / 100, err == nil
}
