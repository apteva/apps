package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
)

const composerV2Version = "composer/v2"

// V2Composition is the forward-compatible composition model. It is
// intentionally renderer-neutral: scenes/elements map cleanly to a future
// browser renderer, while simple image/video/text/soundtrack timelines can be
// lowered to the current ffmpeg renderer.
type V2Composition struct {
	Version    string            `json:"version"`
	Name       string            `json:"name,omitempty"`
	Output     V2Output          `json:"output,omitempty"`
	Variables  map[string]string `json:"variables,omitempty"`
	Assets     []V2Asset         `json:"assets,omitempty"`
	Scenes     []V2Scene         `json:"scenes,omitempty"`
	Tracks     []V2Track         `json:"tracks,omitempty"`
	Audio      []V2Audio         `json:"audio,omitempty"`
	Background string            `json:"background,omitempty"`
}

type V2Output struct {
	Format       string  `json:"format,omitempty"`
	Width        int     `json:"width,omitempty"`
	Height       int     `json:"height,omitempty"`
	DesignWidth  int     `json:"design_width,omitempty"`
	DesignHeight int     `json:"design_height,omitempty"`
	FPS          int     `json:"fps,omitempty"`
	Duration     float64 `json:"duration,omitempty"`
	Resolution   string  `json:"resolution,omitempty"`
	Aspect       string  `json:"aspect,omitempty"`
	Background   string  `json:"background,omitempty"`
}

type V2Asset struct {
	ID   string `json:"id"`
	Type string `json:"type"` // image | video | audio
	Src  string `json:"src"`
}

type V2Scene struct {
	ID         string         `json:"id,omitempty"`
	Start      float64        `json:"start,omitempty"`
	Duration   float64        `json:"duration"`
	Background string         `json:"background,omitempty"`
	Elements   []V2Element    `json:"elements,omitempty"`
	Transition *Transition    `json:"transition,omitempty"`
	Meta       map[string]any `json:"meta,omitempty"`
}

type V2Track struct {
	ID    string   `json:"id,omitempty"`
	Type  string   `json:"type"` // video | image | audio | text | overlay
	Clips []V2Clip `json:"clips"`
}

type V2Clip struct {
	UID        string         `json:"uid,omitempty"`
	Type       string         `json:"type,omitempty"`
	Asset      string         `json:"asset,omitempty"`
	Src        string         `json:"src,omitempty"`
	Start      float64        `json:"start,omitempty"`
	Duration   float64        `json:"duration,omitempty"`
	Length     float64        `json:"length,omitempty"`
	Volume     float64        `json:"volume,omitempty"`
	Fit        string         `json:"fit,omitempty"`
	Text       string         `json:"text,omitempty"`
	Style      map[string]any `json:"style,omitempty"`
	Transition *Transition    `json:"transition,omitempty"`
	Meta       map[string]any `json:"meta,omitempty"`
}

type V2Element struct {
	ID       string         `json:"id,omitempty"`
	Type     string         `json:"type"`
	Parent   string         `json:"parent,omitempty"`
	Asset    string         `json:"asset,omitempty"`
	Src      string         `json:"src,omitempty"`
	Text     string         `json:"text,omitempty"`
	Start    float64        `json:"start,omitempty"`
	Duration float64        `json:"duration,omitempty"`
	X        any            `json:"x,omitempty"`
	Y        any            `json:"y,omitempty"`
	Width    any            `json:"width,omitempty"`
	Height   any            `json:"height,omitempty"`
	Fit      string         `json:"fit,omitempty"`
	Style    map[string]any `json:"style,omitempty"`
	Enter    map[string]any `json:"enter,omitempty"`
	Exit     map[string]any `json:"exit,omitempty"`
	Animate  map[string]any `json:"animate,omitempty"`
	Meta     map[string]any `json:"meta,omitempty"`
}

type V2Audio struct {
	ID       string  `json:"id,omitempty"`
	Asset    string  `json:"asset,omitempty"`
	Src      string  `json:"src,omitempty"`
	Start    float64 `json:"start,omitempty"`
	Duration float64 `json:"duration,omitempty"`
	Length   float64 `json:"length,omitempty"`
	Volume   float64 `json:"volume,omitempty"`
}

type CompositionValidation struct {
	Valid           bool     `json:"valid"`
	Version         string   `json:"version"`
	DurationSeconds float64  `json:"duration_seconds"`
	Renderer        string   `json:"renderer"`
	Errors          []string `json:"errors,omitempty"`
	Warnings        []string `json:"warnings,omitempty"`
}

func isV2EditJSON(s string) bool {
	var raw map[string]any
	if err := json.Unmarshal([]byte(s), &raw); err != nil {
		return false
	}
	return isV2Map(raw)
}

func isV2Map(raw map[string]any) bool {
	if strArg(raw, "version", "") == composerV2Version {
		return true
	}
	if _, ok := raw["scenes"]; ok {
		return true
	}
	if _, ok := raw["assets"]; ok {
		return true
	}
	if _, ok := raw["audio"]; ok {
		return true
	}
	return false
}

func v2SpecFromArgs(args map[string]any) (*V2Composition, bool, error) {
	if args == nil {
		return nil, false, nil
	}
	if raw, ok := args["spec"]; ok {
		b, err := json.Marshal(raw)
		if err != nil {
			return nil, true, err
		}
		spec, err := parseV2CompositionJSON(string(b))
		return spec, true, err
	}
	if !isV2Map(args) {
		return nil, false, nil
	}
	b, err := json.Marshal(args)
	if err != nil {
		return nil, true, err
	}
	spec, err := parseV2CompositionJSON(string(b))
	return spec, true, err
}

func parseV2CompositionJSON(s string) (*V2Composition, error) {
	if strings.TrimSpace(s) == "" {
		return nil, errors.New("empty composer/v2 spec")
	}
	var spec V2Composition
	if err := json.Unmarshal([]byte(s), &spec); err != nil {
		return nil, fmt.Errorf("composer/v2 parse: %w", err)
	}
	if spec.Version == "" {
		spec.Version = composerV2Version
	}
	if err := validateV2Composition(&spec); err != nil {
		return nil, err
	}
	return &spec, nil
}

func validateV2Composition(spec *V2Composition) error {
	if spec == nil {
		return errors.New("composer/v2 spec is nil")
	}
	if spec.Version != composerV2Version {
		return fmt.Errorf("version must be %q", composerV2Version)
	}
	if spec.Output.Format == "" {
		spec.Output.Format = "mp4"
	}
	if spec.Output.FPS == 0 {
		spec.Output.FPS = 30
	}
	if spec.Output.Background == "" {
		spec.Output.Background = spec.Background
	}
	if spec.Output.Background == "" {
		spec.Output.Background = "#000000"
	}
	if spec.Output.Width <= 0 || spec.Output.Height <= 0 {
		out := v2OutputToOutput(spec.Output)
		w, h := resolutionWH(out.Resolution, out.Aspect)
		spec.Output.Width = w
		spec.Output.Height = h
		spec.Output.Resolution = out.Resolution
		spec.Output.Aspect = out.Aspect
	}
	if spec.Output.Format != "mp4" && spec.Output.Format != "mp3" && spec.Output.Format != "wav" {
		return fmt.Errorf("output.format %q is not supported", spec.Output.Format)
	}
	if spec.Output.Width <= 0 || spec.Output.Height <= 0 {
		return errors.New("output width/height must be > 0")
	}
	if spec.Output.FPS != 24 && spec.Output.FPS != 25 && spec.Output.FPS != 30 && spec.Output.FPS != 60 {
		return fmt.Errorf("output.fps must be 24, 25, 30, or 60 (got %d)", spec.Output.FPS)
	}
	ids := map[string]V2Asset{}
	for i, asset := range spec.Assets {
		if strings.TrimSpace(asset.ID) == "" {
			return fmt.Errorf("assets[%d].id required", i)
		}
		if _, exists := ids[asset.ID]; exists {
			return fmt.Errorf("asset id %q is duplicated", asset.ID)
		}
		switch asset.Type {
		case "image", "video", "audio":
		default:
			return fmt.Errorf("asset %q has unsupported type %q", asset.ID, asset.Type)
		}
		if strings.TrimSpace(asset.Src) == "" {
			return fmt.Errorf("asset %q src required", asset.ID)
		}
		ids[asset.ID] = asset
	}
	for i := range spec.Scenes {
		if spec.Scenes[i].Duration <= 0 {
			return fmt.Errorf("scenes[%d].duration must be > 0", i)
		}
		for j, el := range spec.Scenes[i].Elements {
			if err := validateV2Element(el, ids); err != nil {
				return fmt.Errorf("scenes[%d].elements[%d]: %w", i, j, err)
			}
		}
	}
	for i, track := range spec.Tracks {
		switch track.Type {
		case "video", "image", "audio", "text", "overlay", "":
		default:
			return fmt.Errorf("tracks[%d].type %q is unsupported", i, track.Type)
		}
		for j, clip := range track.Clips {
			if err := validateV2Clip(clip, ids); err != nil {
				return fmt.Errorf("tracks[%d].clips[%d]: %w", i, j, err)
			}
		}
	}
	for i, audio := range spec.Audio {
		if _, _, err := v2AudioSource(audio, ids); err != nil {
			return fmt.Errorf("audio[%d]: %w", i, err)
		}
		if audio.Duration < 0 || audio.Length < 0 {
			return fmt.Errorf("audio[%d]: duration/length cannot be negative", i)
		}
	}
	if len(spec.Scenes) == 0 && len(spec.Tracks) == 0 && len(spec.Audio) == 0 {
		return errors.New("composer/v2 requires at least one scene, track, or audio item")
	}
	return nil
}

func validateV2Element(el V2Element, assets map[string]V2Asset) error {
	switch el.Type {
	case "image", "video", "text", "shape":
	default:
		return fmt.Errorf("unsupported element type %q", el.Type)
	}
	if el.Type == "image" || el.Type == "video" {
		src, typ, err := v2ResolveAsset(el.Asset, el.Src, assets)
		if err != nil {
			return err
		}
		if typ != el.Type {
			return fmt.Errorf("asset type mismatch: element is %s but asset is %s", el.Type, typ)
		}
		if src == "" {
			return errors.New("asset/src required")
		}
	}
	if el.Type == "text" && strings.TrimSpace(el.Text) == "" {
		return errors.New("text element requires text")
	}
	if el.Duration < 0 {
		return errors.New("duration cannot be negative")
	}
	return nil
}

func validateV2Clip(clip V2Clip, assets map[string]V2Asset) error {
	typ := clip.Type
	src, assetTyp, err := v2ResolveAsset(clip.Asset, clip.Src, assets)
	if err != nil {
		return err
	}
	if typ == "" {
		typ = assetTyp
	}
	switch typ {
	case "image", "video", "audio", "text":
	default:
		return fmt.Errorf("unsupported clip type %q", typ)
	}
	if typ == "text" {
		if strings.TrimSpace(clip.Text) == "" {
			return errors.New("text clip requires text")
		}
	} else if src == "" {
		return errors.New("asset/src required")
	}
	if clip.Duration < 0 || clip.Length < 0 {
		return errors.New("duration/length cannot be negative")
	}
	return nil
}

func v2ResolveAsset(id, src string, assets map[string]V2Asset) (string, string, error) {
	if id != "" {
		asset, ok := assets[id]
		if !ok {
			return "", "", fmt.Errorf("asset %q not found", id)
		}
		if src == "" {
			src = asset.Src
		}
		return src, asset.Type, nil
	}
	if src == "" {
		return "", "", nil
	}
	return src, assetKindHint(src), nil
}

func v2AudioSource(audio V2Audio, assets map[string]V2Asset) (string, string, error) {
	src, typ, err := v2ResolveAsset(audio.Asset, audio.Src, assets)
	if err != nil {
		return "", "", err
	}
	if src == "" {
		return "", "", errors.New("asset/src required")
	}
	if typ != "audio" {
		return "", "", fmt.Errorf("audio source must resolve to audio, got %q", typ)
	}
	return src, typ, nil
}

func v2OutputToOutput(v V2Output) Output {
	out := defaultOutput()
	if v.Format != "" {
		out.Format = v.Format
	}
	if v.Resolution != "" {
		out.Resolution = v.Resolution
	}
	if v.Aspect != "" {
		out.Aspect = v.Aspect
	}
	if v.FPS > 0 {
		out.FPS = v.FPS
	}
	if v.Width > 0 && v.Height > 0 {
		switch {
		case v.Width == 3840 || v.Height == 3840 || v.Width >= 3000 || v.Height >= 3000:
			out.Resolution = "4k"
		case v.Width >= 1900 || v.Height >= 1900:
			out.Resolution = "fullhd"
		case v.Width <= 900 && v.Height <= 900:
			out.Resolution = "sd"
		default:
			out.Resolution = "hd"
		}
		ratio := float64(v.Width) / float64(v.Height)
		switch {
		case math.Abs(ratio-(9.0/16.0)) < 0.03:
			out.Aspect = "9:16"
		case math.Abs(ratio-1) < 0.03:
			out.Aspect = "1:1"
		case math.Abs(ratio-(4.0/3.0)) < 0.04:
			out.Aspect = "4:3"
		default:
			out.Aspect = "16:9"
		}
	}
	validateOutput(&out)
	return out
}

func v2DurationSeconds(spec *V2Composition) float64 {
	if spec == nil {
		return 0
	}
	if spec.Output.Duration > 0 {
		return spec.Output.Duration
	}
	var maxEnd float64
	var sceneCursor float64
	for _, scene := range spec.Scenes {
		start := scene.Start
		if start <= 0 {
			start = sceneCursor
		}
		end := start + scene.Duration
		if end > maxEnd {
			maxEnd = end
		}
		sceneCursor = end
	}
	for _, track := range spec.Tracks {
		for _, clip := range track.Clips {
			d := clip.Duration
			if d <= 0 {
				d = clip.Length
			}
			if d <= 0 {
				d = 1
			}
			if end := clip.Start + d; end > maxEnd {
				maxEnd = end
			}
		}
	}
	for _, audio := range spec.Audio {
		d := audio.Duration
		if d <= 0 {
			d = audio.Length
		}
		if end := audio.Start + d; end > maxEnd {
			maxEnd = end
		}
	}
	return maxEnd
}

func v2ToV1FFmpeg(spec *V2Composition) (*Edit, Output, []string, error) {
	if err := validateV2Composition(spec); err != nil {
		return nil, Output{}, nil, err
	}
	out := v2OutputToOutput(spec.Output)
	if out.Format != "mp4" && !v2OutputIsAudio(out.Format) {
		return nil, out, nil, fmt.Errorf("composer/v2 output.format=%q is not supported", out.Format)
	}
	assets := map[string]V2Asset{}
	for _, asset := range spec.Assets {
		assets[asset.ID] = asset
	}
	edit := &Edit{Timeline: Timeline{Background: spec.Output.Background}}
	if edit.Timeline.Background == "" {
		edit.Timeline.Background = spec.Background
	}
	if edit.Timeline.Background == "" {
		edit.Timeline.Background = "#000000"
	}
	var warnings []string
	clips, visualWarnings, visualErr := v2VisualClips(spec, assets)
	warnings = append(warnings, visualWarnings...)
	if visualErr != nil && !v2OutputIsAudio(out.Format) {
		return nil, out, warnings, visualErr
	}
	if len(clips) == 0 && !v2OutputIsAudio(out.Format) {
		return nil, out, warnings, errors.New("composer/v2 has no ffmpeg-compatible visual clips")
	}
	if len(clips) > 0 {
		edit.Timeline.Tracks = append(edit.Timeline.Tracks, Track{Type: "visual", Clips: clips})
	}
	if audioTrack, ok, err := v2AudioTrack(spec, assets); err != nil {
		return nil, out, warnings, err
	} else if ok {
		edit.Timeline.Tracks = append(edit.Timeline.Tracks, audioTrack)
	}
	if st, ok, err := v2Soundtrack(spec, assets); err != nil {
		return nil, out, warnings, err
	} else if ok {
		edit.Timeline.Soundtrack = st
	}
	if err := validateEdit(edit); err != nil {
		return nil, out, warnings, err
	}
	return edit, out, warnings, nil
}

func v2OutputIsAudio(format string) bool {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "mp3", "wav", "m4a", "aac":
		return true
	default:
		return false
	}
}

func v2VisualClips(spec *V2Composition, assets map[string]V2Asset) ([]Clip, []string, error) {
	var warnings []string
	if len(spec.Scenes) > 0 {
		return v2SceneClips(spec.Scenes, assets)
	}
	var visualTrack *V2Track
	for i := range spec.Tracks {
		switch spec.Tracks[i].Type {
		case "video", "image", "":
			visualTrack = &spec.Tracks[i]
			goto found
		}
	}
found:
	if visualTrack == nil {
		return nil, warnings, errors.New("ffmpeg renderer needs at least one image/video track")
	}
	sort.SliceStable(visualTrack.Clips, func(i, j int) bool {
		return visualTrack.Clips[i].Start < visualTrack.Clips[j].Start
	})
	clips := make([]Clip, 0, len(visualTrack.Clips))
	for i, vc := range visualTrack.Clips {
		src, typ, err := v2ResolveAsset(vc.Asset, vc.Src, assets)
		if err != nil {
			return nil, warnings, fmt.Errorf("visual clip[%d]: %w", i, err)
		}
		if typ != "image" && typ != "video" {
			return nil, warnings, fmt.Errorf("visual clip[%d]: asset must be image/video, got %s", i, typ)
		}
		length := vc.Duration
		if length <= 0 {
			length = vc.Length
		}
		if length <= 0 {
			return nil, warnings, fmt.Errorf("visual clip[%d]: duration or length required for ffmpeg renderer", i)
		}
		clip := Clip{
			Asset:      Asset{Type: typ, Src: src},
			Start:      vc.Start,
			Length:     length,
			Transition: vc.Transition,
		}
		if strings.TrimSpace(vc.Text) != "" {
			clip.Text = textOverFromV2(vc.Text, vc.Style)
		}
		clips = append(clips, clip)
	}
	if len(spec.Tracks) > 1 {
		warnings = append(warnings, "ffmpeg renderer uses one visual track plus optional audio; overlay/text tracks require web renderer")
	}
	return clips, warnings, nil
}

func v2SceneClips(scenes []V2Scene, assets map[string]V2Asset) ([]Clip, []string, error) {
	warnings := []string{}
	clips := make([]Clip, 0, len(scenes))
	cursor := 0.0
	for i, scene := range scenes {
		start := scene.Start
		if start <= 0 {
			start = cursor
		}
		var visual *V2Element
		var text *V2Element
		for j := range scene.Elements {
			el := &scene.Elements[j]
			switch el.Type {
			case "image", "video":
				if visual == nil {
					visual = el
				} else {
					warnings = append(warnings, fmt.Sprintf("scene[%d] has multiple visual elements; ffmpeg renderer uses the first", i))
				}
			case "text":
				if text == nil {
					text = el
				} else {
					warnings = append(warnings, fmt.Sprintf("scene[%d] has multiple text elements; ffmpeg renderer uses the first", i))
				}
			case "shape":
				warnings = append(warnings, fmt.Sprintf("scene[%d] shape elements require web renderer", i))
			}
		}
		if visual == nil {
			return nil, warnings, fmt.Errorf("scene[%d] needs an image/video element for ffmpeg rendering", i)
		}
		src, typ, err := v2ResolveAsset(visual.Asset, visual.Src, assets)
		if err != nil {
			return nil, warnings, fmt.Errorf("scene[%d]: %w", i, err)
		}
		clip := Clip{
			Asset:      Asset{Type: typ, Src: src},
			Start:      start,
			Length:     scene.Duration,
			Transition: scene.Transition,
		}
		if text != nil {
			clip.Text = textOverFromV2(text.Text, text.Style)
		}
		clips = append(clips, clip)
		cursor = start + scene.Duration
	}
	return clips, warnings, nil
}

func textOverFromV2(body string, style map[string]any) *TextOver {
	text := &TextOver{Body: body, Position: "bottom", FontSize: 36, Color: "#ffffff"}
	if style == nil {
		return text
	}
	if v, ok := style["position"].(string); ok {
		text.Position = v
	}
	if v, ok := style["color"].(string); ok {
		text.Color = v
	}
	if v, ok := style["font_size"].(float64); ok && v > 0 {
		text.FontSize = int(v)
	}
	if v, ok := style["fontSize"].(float64); ok && v > 0 {
		text.FontSize = int(v)
	}
	return text
}

func v2Soundtrack(spec *V2Composition, assets map[string]V2Asset) (*Soundtrack, bool, error) {
	if len(spec.Audio) > 1 || (len(spec.Audio) == 1 && (spec.Audio[0].Start != 0 || spec.Audio[0].Duration > 0 || spec.Audio[0].Length > 0)) {
		return nil, false, nil
	}
	if len(spec.Audio) == 0 {
		for _, track := range spec.Tracks {
			if track.Type == "audio" && len(track.Clips) > 0 {
				return nil, false, nil
			}
		}
		return nil, false, nil
	}
	audio := spec.Audio[0]
	src, _, err := v2AudioSource(audio, assets)
	if err != nil {
		return nil, false, err
	}
	volume := audio.Volume
	if volume <= 0 {
		volume = 1
	}
	return &Soundtrack{Src: src, Volume: volume}, true, nil
}

func v2AudioTrack(spec *V2Composition, assets map[string]V2Asset) (Track, bool, error) {
	track := Track{Type: "audio"}
	for i, audio := range spec.Audio {
		length := audio.Duration
		if length <= 0 {
			length = audio.Length
		}
		if length <= 0 {
			continue
		}
		src, _, err := v2AudioSource(audio, assets)
		if err != nil {
			return Track{}, false, fmt.Errorf("audio[%d]: %w", i, err)
		}
		volume := audio.Volume
		if volume <= 0 {
			volume = 1
		}
		track.Clips = append(track.Clips, Clip{
			UID:    audio.ID,
			Asset:  Asset{Type: "audio", Src: src},
			Start:  audio.Start,
			Length: length,
			Volume: volume,
		})
	}
	for ti, srcTrack := range spec.Tracks {
		if srcTrack.Type != "audio" {
			continue
		}
		for ci, clip := range srcTrack.Clips {
			length := clip.Duration
			if length <= 0 {
				length = clip.Length
			}
			if length <= 0 {
				return Track{}, false, fmt.Errorf("tracks[%d].clips[%d]: duration or length required for audio rendering", ti, ci)
			}
			src, typ, err := v2ResolveAsset(clip.Asset, clip.Src, assets)
			if err != nil {
				return Track{}, false, fmt.Errorf("tracks[%d].clips[%d]: %w", ti, ci, err)
			}
			if typ != "audio" {
				return Track{}, false, fmt.Errorf("tracks[%d].clips[%d]: audio source must resolve to audio, got %s", ti, ci, typ)
			}
			volume := clip.Volume
			if volume <= 0 {
				volume = 1
			}
			track.Clips = append(track.Clips, Clip{
				UID:    clip.UID,
				Asset:  Asset{Type: "audio", Src: src},
				Start:  clip.Start,
				Length: length,
				Volume: volume,
			})
		}
	}
	return track, len(track.Clips) > 0, nil
}

func validateCompositionJSON(s string) CompositionValidation {
	if isV2EditJSON(s) {
		spec, err := parseV2CompositionJSON(s)
		if err != nil {
			return CompositionValidation{Valid: false, Version: composerV2Version, Renderer: "none", Errors: []string{err.Error()}}
		}
		if spec.Output.Format == "mp4" && len(spec.Scenes) > 0 && !v2HasVideoElements(spec) {
			return CompositionValidation{
				Valid:           true,
				Version:         composerV2Version,
				DurationSeconds: v2DurationSeconds(spec),
				Renderer:        "native-v2",
				Warnings:        []string{"native composer/v2 renderer supports image, shape, text, parented component motion, design-size scaling, opacity, enter/exit presets, and x/y/scale/opacity keyframes"},
			}
		}
		_, _, warnings, convErr := v2ToV1FFmpeg(spec)
		renderer := "ffmpeg"
		if convErr != nil {
			renderer = "web_required"
			warnings = append(warnings, convErr.Error())
		}
		return CompositionValidation{Valid: true, Version: composerV2Version, DurationSeconds: v2DurationSeconds(spec), Renderer: renderer, Warnings: warnings}
	}
	edit, err := parseEditJSON(s)
	if err != nil {
		return CompositionValidation{Valid: false, Version: "composer/v1", Renderer: "none", Errors: []string{err.Error()}}
	}
	return CompositionValidation{Valid: true, Version: "composer/v1", DurationSeconds: editDurationSeconds(edit), Renderer: "ffmpeg"}
}

func composerV2Examples() []map[string]any {
	raw := `[
	  {
	    "id": "v2-scenes-with-text",
	    "title": "Native scene graph with grouped motion",
	    "description": "Native V2 scene graph with design-size scaling, parented text labels, shape cards, fast enter presets, and a soundtrack.",
	    "spec": {
	      "version": "composer/v2",
	      "output": {"format": "mp4", "width": 1920, "height": 1080, "design_width": 1920, "design_height": 1080, "fps": 30, "background": "#05070c"},
	      "assets": [
	        {"id": "music", "type": "audio", "src": "storage:201"}
	      ],
	      "scenes": [
	        {"id": "open", "duration": 4, "elements": [
	          {"id": "bg", "type": "shape", "x": "0%", "y": "0%", "width": "100%", "height": "100%", "style": {"fill": "#05070c"}},
	          {"id": "glow", "type": "shape", "x": "60%", "y": "-8%", "width": "46%", "height": "38%", "style": {"fill": "rgba(255,122,26,0.14)", "radius": 260}, "animate": {"scale": [{"start": 0, "duration": 4, "from": 1, "to": 1.06}]}},
	          {"id": "h", "type": "text", "text": "Launch a sharper\\nAI service offer.", "x": "7%", "y": "22%", "width": "44%", "height": "24%", "style": {"font_size": 76, "weight": 800, "color": "#f7f8fb", "align": "left"}, "enter": {"type": "rise", "duration": 0.36}},
	          {"id": "sub", "type": "text", "text": "Package outcomes, proof, and delivery into a pitch clients understand.", "x": "7%", "y": "49%", "width": "40%", "height": "10%", "style": {"font_size": 30, "color": "#a8b0bd", "align": "left"}, "enter": {"type": "fade", "delay": 0.12, "duration": 0.32}},
	          {"id": "card", "type": "shape", "x": "57%", "y": "20%", "width": "31%", "height": "45%", "style": {"fill": "#111827", "stroke": "#2d3a4f", "stroke_width": 2, "radius": 28}, "enter": {"type": "zoom_in", "delay": 0.12, "duration": 0.34}, "animate": {"scale": [{"start": 0.8, "duration": 3.2, "from": 1, "to": 1.018}]}},
	          {"id": "card-title", "parent": "card", "type": "text", "text": "Offer Engine", "x": "60%", "y": "25%", "width": "18%", "height": "5%", "style": {"font_size": 30, "weight": 800, "color": "#ffffff", "align": "left"}},
	          {"id": "metric", "parent": "card", "type": "text", "text": "+31% qualified calls", "x": "60%", "y": "43%", "width": "24%", "height": "7%", "style": {"font_size": 42, "weight": 800, "color": "#37e6ad", "align": "left"}}
	        ]},
	        {"id": "proof", "duration": 5, "elements": [
	          {"id": "bg", "type": "shape", "x": "0%", "y": "0%", "width": "100%", "height": "100%", "style": {"fill": "#05070c"}},
	          {"id": "h", "type": "text", "text": "Proof beats promises.", "x": "7%", "y": "18%", "width": "48%", "height": "12%", "style": {"font_size": 72, "weight": 800, "color": "#f7f8fb", "align": "left"}, "enter": {"type": "rise", "duration": 0.34}},
	          {"id": "proof-card", "type": "shape", "x": "7%", "y": "42%", "width": "22%", "height": "18%", "style": {"fill": "#121927", "stroke": "#ff7a1a", "stroke_width": 2, "radius": 22}, "enter": {"type": "slide_up", "delay": 0.1, "duration": 0.32}},
	          {"id": "proof-v", "parent": "proof-card", "type": "text", "text": "18%", "x": "9%", "y": "45%", "width": "12%", "height": "7%", "style": {"font_size": 54, "weight": 800, "color": "#37e6ad", "align": "left"}},
	          {"id": "proof-l", "parent": "proof-card", "type": "text", "text": "faster onboarding", "x": "9%", "y": "54%", "width": "17%", "height": "4%", "style": {"font_size": 24, "color": "#a8b0bd", "align": "left"}}
	        ]}
	      ],
	      "audio": [{"asset": "music", "volume": 0.25}]
	    }
	  },
	  {
	    "id": "v2-timeline-video",
	    "title": "Timeline clips with a soundtrack",
	    "description": "Explicit tracks form for video/image stitching.",
	    "spec": {
	      "version": "composer/v2",
	      "output": {"format": "mp4", "width": 1080, "height": 1920, "fps": 30},
	      "assets": [
	        {"id": "clip-a", "type": "video", "src": "storage:301"},
	        {"id": "clip-b", "type": "video", "src": "storage:302"},
	        {"id": "bed", "type": "audio", "src": "storage:401"}
	      ],
	      "tracks": [{"id": "main", "type": "video", "clips": [
	        {"asset": "clip-a", "start": 0, "duration": 6, "text": "First beat", "style": {"position": "bottom"}},
	        {"asset": "clip-b", "start": 6, "duration": 6, "text": "Second beat", "style": {"position": "bottom"}}
	      ]}],
	      "audio": [{"asset": "bed", "volume": 0.18}]
	    }
	  },
	  {
	    "id": "v2-web-renderer-required",
	    "title": "Advanced layout placeholder",
	    "description": "Text-only and shape elements validate but require the upcoming web renderer to render faithfully.",
	    "spec": {
	      "version": "composer/v2",
	      "output": {"format": "mp4", "width": 1920, "height": 1080, "fps": 30, "background": "#08080c"},
	      "scenes": [{"id": "title", "duration": 5, "elements": [
	        {"type": "shape", "style": {"fill": "#15151c"}},
	        {"type": "text", "text": "Animated title", "style": {"position": "center", "font_size": 72}, "enter": {"type": "fade", "duration": 0.4}}
	      ]}]
	    }
	  }
	]`
	var examples []map[string]any
	_ = json.Unmarshal([]byte(raw), &examples)
	return examples
}
