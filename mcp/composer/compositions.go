package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Canonical Edit JSON (Shotstack-shape subset). v0.1 supports:
//   - One video track of N clips
//   - Optional soundtrack (single audio file with volume)
//   - Per-clip text overlay (single line, position + font size)
//   - Per-clip transition: "none" | "fade" (cut otherwise)
//
// Validator rejects shapes that would silently misrender (multi-track,
// keyframes, unknown asset types, etc.) so the executor can assume a
// known shape.

type Edit struct {
	Timeline Timeline `json:"timeline"`
}

type Timeline struct {
	Soundtrack *Soundtrack `json:"soundtrack,omitempty"`
	Background string      `json:"background,omitempty"` // hex color, e.g. "#000000"
	Tracks     []Track     `json:"tracks"`
}

type Soundtrack struct {
	Src    string  `json:"src"`              // storage:N | https://… | mediastudio:N
	Volume float64 `json:"volume,omitempty"` // 0..1, default 1.0
}

type Track struct {
	Clips []Clip `json:"clips"`
}

type Clip struct {
	Asset      Asset       `json:"asset"`
	Start      float64     `json:"start"`             // seconds from composition start
	Length     float64     `json:"length"`            // seconds
	Transition *Transition `json:"transition,omitempty"`
	Text       *TextOver   `json:"text,omitempty"`
}

type Asset struct {
	Type string `json:"type"` // "video" | "image" | "audio"
	Src  string `json:"src"`  // storage:N | https://… | mediastudio:N
}

type Transition struct {
	In  string `json:"in,omitempty"`  // "none" | "fade"
	Out string `json:"out,omitempty"` // "none" | "fade"
}

type TextOver struct {
	Body     string `json:"body"`
	Position string `json:"position,omitempty"`  // "top" | "center" | "bottom" (default bottom)
	FontSize int    `json:"font_size,omitempty"` // default 32
	Color    string `json:"color,omitempty"`     // hex, default "#ffffff"
}

type Output struct {
	Format     string `json:"format"`     // "mp4" (only v0.1)
	Resolution string `json:"resolution"` // "sd" | "hd" | "fullhd" | "4k"
	Aspect     string `json:"aspect"`     // "16:9" | "9:16" | "1:1" | "4:3"
	FPS        int    `json:"fps"`        // 24 | 30 | 60
}

// validateEdit rejects shapes the local/remote executors can't render.
func validateEdit(e *Edit) error {
	if e == nil {
		return errors.New("edit is nil")
	}
	if len(e.Timeline.Tracks) == 0 {
		return errors.New("at least one track required")
	}
	if len(e.Timeline.Tracks) > 1 {
		return errors.New("v0.1 supports a single video track (got " + fmt.Sprint(len(e.Timeline.Tracks)) + ")")
	}
	track := e.Timeline.Tracks[0]
	if len(track.Clips) == 0 {
		return errors.New("track must have at least one clip")
	}
	for i, c := range track.Clips {
		if c.Asset.Src == "" {
			return fmt.Errorf("clip[%d]: asset.src required", i)
		}
		switch c.Asset.Type {
		case "video", "image", "":
			// "" defaults to "video"; image needs a length
		default:
			return fmt.Errorf("clip[%d]: unsupported asset.type %q (v0.1 accepts video|image)", i, c.Asset.Type)
		}
		if c.Length <= 0 {
			return fmt.Errorf("clip[%d]: length must be > 0", i)
		}
		if c.Transition != nil {
			if c.Transition.In != "" && c.Transition.In != "none" && c.Transition.In != "fade" {
				return fmt.Errorf("clip[%d]: transition.in must be 'none' or 'fade' (got %q)", i, c.Transition.In)
			}
			if c.Transition.Out != "" && c.Transition.Out != "none" && c.Transition.Out != "fade" {
				return fmt.Errorf("clip[%d]: transition.out must be 'none' or 'fade' (got %q)", i, c.Transition.Out)
			}
		}
		if c.Text != nil {
			switch c.Text.Position {
			case "", "top", "center", "bottom":
			default:
				return fmt.Errorf("clip[%d]: text.position must be top|center|bottom", i)
			}
		}
	}
	if s := e.Timeline.Soundtrack; s != nil {
		if s.Src == "" {
			return errors.New("soundtrack.src required when soundtrack is set")
		}
		if s.Volume < 0 || s.Volume > 1 {
			return errors.New("soundtrack.volume must be 0..1")
		}
	}
	return nil
}

func defaultOutput() Output {
	return Output{Format: "mp4", Resolution: "hd", Aspect: "16:9", FPS: 30}
}

// validateOutput allows the partial-fill case (UI may only set format).
// Unknown values are passed through — the executor handles fallbacks.
func validateOutput(o *Output) {
	if o.Format == "" {
		o.Format = "mp4"
	}
	if o.Resolution == "" {
		o.Resolution = "hd"
	}
	if o.Aspect == "" {
		o.Aspect = "16:9"
	}
	if o.FPS == 0 {
		o.FPS = 30
	}
}

// resolutionWH maps the canonical name to pixel dimensions for the
// chosen aspect ratio. Falls back to 1280×720 16:9 if either is unknown.
func resolutionWH(resolution, aspect string) (w, h int) {
	switch resolution {
	case "sd":
		w, h = 854, 480
	case "fullhd":
		w, h = 1920, 1080
	case "4k":
		w, h = 3840, 2160
	default: // "hd"
		w, h = 1280, 720
	}
	// Aspect adjustment — keep height for landscape, recompute width.
	switch aspect {
	case "9:16":
		w, h = h, w // portrait
	case "1:1":
		w = h
	case "4:3":
		w = (h * 4) / 3
	}
	// 16:9 stays as-is.
	return
}

// editDurationSeconds totals the clip lengths on the single track,
// ignoring overlap (we don't do overlapping clips in v0.1).
func editDurationSeconds(e *Edit) float64 {
	if e == nil || len(e.Timeline.Tracks) == 0 {
		return 0
	}
	var d float64
	for _, c := range e.Timeline.Tracks[0].Clips {
		d += c.Length
	}
	return d
}

// parseEditJSON unmarshals + validates. Returns the cleaned struct.
func parseEditJSON(s string) (*Edit, error) {
	if strings.TrimSpace(s) == "" {
		return nil, errors.New("empty edit_json")
	}
	var e Edit
	if err := json.Unmarshal([]byte(s), &e); err != nil {
		return nil, fmt.Errorf("edit_json parse: %w", err)
	}
	if err := validateEdit(&e); err != nil {
		return nil, err
	}
	return &e, nil
}

// editFromArgs builds an Edit out of MCP tool args (typed via JSON
// round-trip rather than reflection so the same validator applies).
func editFromArgs(args map[string]any) (*Edit, error) {
	timeline := map[string]any{}
	if v, ok := args["tracks"]; ok {
		timeline["tracks"] = v
	}
	if v, ok := args["soundtrack"]; ok {
		timeline["soundtrack"] = v
	}
	if v, ok := args["background"]; ok {
		timeline["background"] = v
	}
	wrapped := map[string]any{"timeline": timeline}
	b, err := json.Marshal(wrapped)
	if err != nil {
		return nil, err
	}
	return parseEditJSON(string(b))
}

func outputFromArgs(args map[string]any) Output {
	o := defaultOutput()
	if raw, ok := args["output"].(map[string]any); ok {
		if v := strArg(raw, "format", ""); v != "" {
			o.Format = v
		}
		if v := strArg(raw, "resolution", ""); v != "" {
			o.Resolution = v
		}
		if v := strArg(raw, "aspect", ""); v != "" {
			o.Aspect = v
		}
		if v := intArg(raw, "fps", 0); v > 0 {
			o.FPS = v
		}
	}
	validateOutput(&o)
	return o
}
