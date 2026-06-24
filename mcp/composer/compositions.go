package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Canonical Edit JSON. The renderer supports one visual track and any
// number of timed audio tracks. Visual clips are concatenated in track
// order; audio clips use their start offsets and are mixed over the
// visual track audio. The schema intentionally stays close to SaaS
// render APIs (Shotstack/Creatomate-style tracks and clips) while
// keeping unsupported features explicit in validation.

type Edit struct {
	Timeline Timeline `json:"timeline"`
}

type Timeline struct {
	Soundtrack *Soundtrack `json:"soundtrack,omitempty"`
	Background string      `json:"background,omitempty"` // hex color, e.g. "#000000"
	Tracks     []Track     `json:"tracks"`
}

type Soundtrack struct {
	Src    string   `json:"src"`              // storage:N | https://… | mediastudio:N
	Volume float64  `json:"volume,omitempty"` // 0..1, default 1.0
	Timing *Timing  `json:"timing,omitempty"`
	AI     *AIAsset `json:"ai,omitempty"`
}

type Track struct {
	ID    string `json:"id,omitempty"`
	Type  string `json:"type,omitempty"` // visual|video|audio
	Clips []Clip `json:"clips"`
}

type Clip struct {
	UID             string      `json:"uid,omitempty"`
	SectionID       string      `json:"section_id,omitempty"`
	GroupID         string      `json:"group_id,omitempty"`
	Asset           Asset       `json:"asset"`
	Start           float64     `json:"start"`  // seconds from composition start
	Length          float64     `json:"length"` // seconds
	Duration        float64     `json:"duration,omitempty"`
	DurationMode    string      `json:"duration_mode,omitempty"` // fixed_trim_pad | fit_generated | fit_generated_keep_start | fit_generated_reflow
	EstimatedLength float64     `json:"estimated_length,omitempty"`
	ActualLength    float64     `json:"actual_length,omitempty"`
	Volume          float64     `json:"volume,omitempty"`
	SourceAudio     string      `json:"source_audio,omitempty"` // auto|keep|mute
	AfterClipID     string      `json:"after_clip_id,omitempty"`
	GapSeconds      float64     `json:"gap_seconds,omitempty"`
	Timing          *Timing     `json:"timing,omitempty"`
	Audio           *AudioFX    `json:"audio,omitempty"`
	Transition      *Transition `json:"transition,omitempty"`
	Text            *TextOver   `json:"text,omitempty"`
	AI              *AIAsset    `json:"ai,omitempty"`
}

type Timing struct {
	Mode         string  `json:"mode,omitempty"`          // fixed|fit_generated|fit_source|fit_group|fit_timeline
	Source       string  `json:"source,omitempty"`        // clip:<uid>|audio:<uid>|track:audio|section|group
	PaddingAfter float64 `json:"padding_after,omitempty"` // seconds added after the fitted source
	MinLength    float64 `json:"min_length,omitempty"`
	MaxLength    float64 `json:"max_length,omitempty"`
	Reflow       string  `json:"reflow,omitempty"`   // none|following|track|linked_group|composition
	Behavior     string  `json:"behavior,omitempty"` // trim|pad|trim_or_loop|loop|stretch|regenerate
	FadeOut      float64 `json:"fade_out,omitempty"`
}

type Asset struct {
	Type     string         `json:"type"` // video|image|audio|generated
	Src      string         `json:"src"`  // storage:N | https://… | mediastudio:N
	Provider string         `json:"provider,omitempty"`
	Kind     string         `json:"kind,omitempty"`
	Request  map[string]any `json:"request,omitempty"`
}

type AIAsset struct {
	MediaKind                string         `json:"media_kind"` // image | video | audio_tts | audio_sfx | music | avatar
	Prompt                   string         `json:"prompt"`
	Model                    string         `json:"model,omitempty"`
	Size                     string         `json:"size,omitempty"`
	Duration                 int            `json:"duration,omitempty"`
	Aspect                   string         `json:"aspect,omitempty"`
	Voice                    string         `json:"voice,omitempty"`
	Avatar                   string         `json:"avatar,omitempty"`
	SourceImage              string         `json:"source_image,omitempty"`
	Options                  map[string]any `json:"options,omitempty"`
	CacheKey                 string         `json:"cache_key,omitempty"`
	CachePolicy              string         `json:"cache_policy,omitempty"`
	Status                   string         `json:"status,omitempty"` // draft | generating | ready | failed
	GenerationID             int64          `json:"generation_id,omitempty"`
	StorageID                int64          `json:"storage_id,omitempty"`
	JobID                    int64          `json:"job_id,omitempty"`
	EstimatedDurationSeconds float64        `json:"estimated_duration_seconds,omitempty"`
	ActualDurationSeconds    float64        `json:"actual_duration_seconds,omitempty"`
	AudioAnalysis            *AudioAnalysis `json:"audio_analysis,omitempty"`
	PeakDB                   float64        `json:"peak_db,omitempty"`
	RMSDB                    float64        `json:"rms_db,omitempty"`
	Error                    string         `json:"error,omitempty"`
}

type AudioFX struct {
	GainDB         float64 `json:"gain_db,omitempty"`
	Normalize      bool    `json:"normalize,omitempty"`
	LoudnessTarget float64 `json:"loudness_target,omitempty"`
	PeakLimitDB    float64 `json:"peak_limit_db,omitempty"`
	FadeInSeconds  float64 `json:"fade_in_seconds,omitempty"`
	FadeOutSeconds float64 `json:"fade_out_seconds,omitempty"`
	TrimSilence    bool    `json:"trim_silence,omitempty"`
}

type AudioAnalysis struct {
	DurationSeconds float64 `json:"duration_seconds,omitempty"`
	PeakDB          float64 `json:"peak_db,omitempty"`
	RMSDB           float64 `json:"rms_db,omitempty"`
	SampleRate      int     `json:"sample_rate,omitempty"`
	Channels        int     `json:"channels,omitempty"`
	Codec           string  `json:"codec,omitempty"`
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
	Format     string `json:"format"`     // mp4|mp3|wav|m4a|aac
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
	visualTracks := 0
	for ti := range e.Timeline.Tracks {
		track := &e.Timeline.Tracks[ti]
		if len(track.Clips) == 0 {
			return fmt.Errorf("track[%d]: must have at least one clip", ti)
		}
		for i := range track.Clips {
			c := &track.Clips[i]
			normalizeGeneratedAsset(c)
			if c.Length <= 0 && c.Duration > 0 {
				c.Length = c.Duration
			}
			normalizeClipDurationMetadata(c)
			defaultGeneratedAudioFX(c)
		}
		tt := trackKind(*track)
		if tt == "visual" {
			visualTracks++
			if visualTracks > 1 {
				return errors.New("composer currently renders one visual track plus optional audio tracks")
			}
		} else if tt != "audio" {
			return fmt.Errorf("track[%d]: unsupported track.type %q (use visual or audio)", ti, track.Type)
		}
		for i := range track.Clips {
			c := &track.Clips[i]
			at := clipAssetType(*c, tt)
			if c.Asset.Src == "" && c.AI == nil && at != "silence" {
				return fmt.Errorf("track[%d].clip[%d]: asset.src required", ti, i)
			}
			if at == "" {
				at = "video"
			}
			if at != "video" && at != "image" && at != "audio" && at != "silence" {
				return fmt.Errorf("track[%d].clip[%d]: unsupported asset.type %q", ti, i, c.Asset.Type)
			}
			if tt == "visual" && at == "audio" {
				return fmt.Errorf("track[%d].clip[%d]: audio assets belong on audio tracks", ti, i)
			}
			if tt == "visual" && at == "silence" {
				return fmt.Errorf("track[%d].clip[%d]: silence clips belong on audio tracks", ti, i)
			}
			if tt == "audio" && at != "audio" && at != "silence" {
				return fmt.Errorf("track[%d].clip[%d]: audio tracks require audio assets", ti, i)
			}
			if clipDuration(*c) <= 0 {
				return fmt.Errorf("track[%d].clip[%d]: length must be > 0", ti, i)
			}
			if c.Volume < 0 || c.Volume > 1 {
				return fmt.Errorf("track[%d].clip[%d]: volume must be 0..1", ti, i)
			}
			switch strings.ToLower(strings.TrimSpace(c.SourceAudio)) {
			case "", "auto", "keep", "mute":
			default:
				return fmt.Errorf("track[%d].clip[%d]: source_audio must be auto|keep|mute", ti, i)
			}
			if c.GapSeconds < 0 {
				return fmt.Errorf("track[%d].clip[%d]: gap_seconds must be >= 0", ti, i)
			}
			if err := validateTiming(c.Timing); err != nil {
				return fmt.Errorf("track[%d].clip[%d]: %w", ti, i, err)
			}
			if err := validateAudioFX(c.Audio); err != nil {
				return fmt.Errorf("track[%d].clip[%d]: %w", ti, i, err)
			}
			if tt == "audio" && c.Text != nil {
				return fmt.Errorf("track[%d].clip[%d]: text overlays are only supported on visual clips", ti, i)
			}
			if c.Transition != nil {
				if tt == "audio" {
					return fmt.Errorf("track[%d].clip[%d]: transitions are only supported on visual clips", ti, i)
				}
				if c.Transition.In != "" && c.Transition.In != "none" && c.Transition.In != "fade" {
					return fmt.Errorf("track[%d].clip[%d]: transition.in must be 'none' or 'fade' (got %q)", ti, i, c.Transition.In)
				}
				if c.Transition.Out != "" && c.Transition.Out != "none" && c.Transition.Out != "fade" {
					return fmt.Errorf("track[%d].clip[%d]: transition.out must be 'none' or 'fade' (got %q)", ti, i, c.Transition.Out)
				}
			}
			if c.Text != nil {
				switch c.Text.Position {
				case "", "top", "center", "bottom":
				default:
					return fmt.Errorf("track[%d].clip[%d]: text.position must be top|center|bottom", ti, i)
				}
			}
		}
	}
	if visualTracks == 0 && len(audioTimelineClips(e)) == 0 {
		return errors.New("at least one visual or audio track required")
	}
	if s := e.Timeline.Soundtrack; s != nil {
		if s.Src == "" && s.AI == nil {
			return errors.New("soundtrack.src required when soundtrack is set")
		}
		if s.Volume < 0 || s.Volume > 1 {
			return errors.New("soundtrack.volume must be 0..1")
		}
		if err := validateTiming(s.Timing); err != nil {
			return fmt.Errorf("soundtrack: %w", err)
		}
	}
	return nil
}

func validateTiming(t *Timing) error {
	if t == nil {
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(t.Mode)) {
	case "", "fixed", "fit_generated", "fit_source", "fit_group", "fit_timeline":
	default:
		return fmt.Errorf("timing.mode must be fixed|fit_generated|fit_source|fit_group|fit_timeline (got %q)", t.Mode)
	}
	if t.PaddingAfter < 0 {
		return errors.New("timing.padding_after must be >= 0")
	}
	if t.MinLength < 0 || t.MaxLength < 0 {
		return errors.New("timing min/max length must be >= 0")
	}
	if t.MaxLength > 0 && t.MinLength > t.MaxLength {
		return errors.New("timing.min_length must be <= timing.max_length")
	}
	switch strings.ToLower(strings.TrimSpace(t.Reflow)) {
	case "", "none", "following", "track", "linked_group", "composition":
	default:
		return fmt.Errorf("timing.reflow must be none|following|track|linked_group|composition (got %q)", t.Reflow)
	}
	switch strings.ToLower(strings.TrimSpace(t.Behavior)) {
	case "", "trim", "pad", "trim_or_loop", "loop", "stretch", "regenerate":
	default:
		return fmt.Errorf("timing.behavior must be trim|pad|trim_or_loop|loop|stretch|regenerate (got %q)", t.Behavior)
	}
	if t.FadeOut < 0 {
		return errors.New("timing.fade_out must be >= 0")
	}
	return nil
}

func validateAudioFX(fx *AudioFX) error {
	if fx == nil {
		return nil
	}
	if fx.FadeInSeconds < 0 || fx.FadeOutSeconds < 0 {
		return errors.New("audio fade durations must be >= 0")
	}
	if fx.LoudnessTarget != 0 && (fx.LoudnessTarget > -5 || fx.LoudnessTarget < -40) {
		return errors.New("audio loudness_target must be between -40 and -5 LUFS")
	}
	if fx.PeakLimitDB != 0 && (fx.PeakLimitDB > 0 || fx.PeakLimitDB < -20) {
		return errors.New("audio peak_limit_db must be between -20 and 0 dB")
	}
	if fx.GainDB > 24 || fx.GainDB < -60 {
		return errors.New("audio gain_db must be between -60 and 24")
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

func isAudioOutput(o Output) bool {
	switch strings.ToLower(strings.TrimSpace(o.Format)) {
	case "mp3", "wav", "m4a", "aac":
		return true
	default:
		return false
	}
}

func hasVisualTrack(e *Edit) bool {
	return primaryVisualTrack(e) != nil
}

func validateEditOutput(e *Edit, o Output) error {
	if err := validateEdit(e); err != nil {
		return err
	}
	hasVisual := hasVisualTrack(e)
	if !hasVisual && !isAudioOutput(o) {
		return errors.New("audio-only compositions require output.format mp3, wav, m4a, or aac")
	}
	if hasVisual && isAudioOutput(o) {
		return errors.New("audio output currently requires an audio-only composition")
	}
	return nil
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

// editDurationSeconds returns the visible composition duration. The
// visual track duration is concatenated; timed audio tracks can extend it.
func editDurationSeconds(e *Edit) float64 {
	if e == nil || len(e.Timeline.Tracks) == 0 {
		return 0
	}
	var d float64
	if vt := primaryVisualTrack(e); vt != nil {
		for _, c := range vt.Clips {
			d += clipDuration(c)
		}
	}
	for _, c := range audioTimelineClips(e) {
		if end := c.Start + clipDuration(c); end > d {
			d = end
		}
	}
	return d
}

func primaryVisualTrack(e *Edit) *Track {
	if e == nil {
		return nil
	}
	for i := range e.Timeline.Tracks {
		if trackKind(e.Timeline.Tracks[i]) == "visual" {
			return &e.Timeline.Tracks[i]
		}
	}
	return nil
}

func audioTimelineClips(e *Edit) []Clip {
	if e == nil {
		return nil
	}
	var out []Clip
	for _, t := range e.Timeline.Tracks {
		if trackKind(t) != "audio" {
			continue
		}
		out = append(out, t.Clips...)
	}
	return out
}

func trackKind(t Track) string {
	switch strings.ToLower(strings.TrimSpace(t.Type)) {
	case "", "visual", "video":
		if t.Type == "" {
			allAudio := len(t.Clips) > 0
			for _, c := range t.Clips {
				at := clipAssetType(c, "")
				if at != "audio" && at != "silence" {
					allAudio = false
					break
				}
			}
			if allAudio {
				return "audio"
			}
		}
		return "visual"
	case "audio", "sound", "music", "voice", "sfx":
		return "audio"
	default:
		return strings.ToLower(strings.TrimSpace(t.Type))
	}
}

func clipAssetType(c Clip, trackType string) string {
	switch strings.ToLower(strings.TrimSpace(c.Asset.Type)) {
	case "", "generated":
		if c.AI != nil {
			return assetTypeForAI(c.AI.MediaKind)
		}
		if trackType == "audio" {
			return "audio"
		}
		return "video"
	case "silence", "blank", "gap":
		return "silence"
	default:
		return strings.ToLower(strings.TrimSpace(c.Asset.Type))
	}
}

func clipDuration(c Clip) float64 {
	if c.Length > 0 {
		return c.Length
	}
	if c.ActualLength > 0 {
		return c.ActualLength
	}
	if c.EstimatedLength > 0 {
		return c.EstimatedLength
	}
	if c.AI != nil {
		if c.AI.ActualDurationSeconds > 0 {
			return c.AI.ActualDurationSeconds
		}
		if c.AI.EstimatedDurationSeconds > 0 {
			return c.AI.EstimatedDurationSeconds
		}
	}
	return c.Duration
}

func clipVolume(c Clip) float64 {
	if c.Volume > 0 {
		return c.Volume
	}
	return 1
}

func normalizeGeneratedAsset(c *Clip) {
	if c == nil || strings.ToLower(strings.TrimSpace(c.Asset.Type)) != "generated" || c.AI != nil {
		return
	}
	c.AI = generatedAssetAI(c.Asset)
}

func normalizeClipDurationMetadata(c *Clip) {
	if c == nil {
		return
	}
	if c.AI == nil {
		return
	}
	applyDefaultAIOptions(c.AI)
	if c.DurationMode == "" {
		c.DurationMode = defaultDurationMode(c.AI.MediaKind)
	}
	if c.EstimatedLength <= 0 && c.AI.EstimatedDurationSeconds > 0 {
		c.EstimatedLength = c.AI.EstimatedDurationSeconds
	}
	if c.ActualLength <= 0 && c.AI.ActualDurationSeconds > 0 {
		c.ActualLength = c.AI.ActualDurationSeconds
	}
	if c.Length <= 0 && c.EstimatedLength > 0 {
		c.Length = c.EstimatedLength
	}
	if c.Length <= 0 && c.AI.Duration > 0 {
		c.Length = float64(c.AI.Duration)
	}
}

func defaultGeneratedAudioFX(c *Clip) {
	if c == nil || c.AI == nil || c.Audio != nil {
		return
	}
	if strings.ToLower(strings.TrimSpace(c.AI.MediaKind)) != "audio_sfx" {
		return
	}
	c.Audio = &AudioFX{
		Normalize:      true,
		LoudnessTarget: -16,
		PeakLimitDB:    -2,
		TrimSilence:    true,
	}
}

func defaultDurationMode(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "audio_tts":
		return "fit_generated_reflow"
	case "avatar":
		return "fit_generated_keep_start"
	default:
		return "fixed_trim_pad"
	}
}

func applyDefaultAIOptions(ai *AIAsset) {
	if ai == nil {
		return
	}
	if strings.ToLower(strings.TrimSpace(ai.MediaKind)) != "audio_tts" {
		return
	}
	if ai.Options == nil {
		ai.Options = map[string]any{}
	}
	if _, ok := ai.Options["voice_settings"]; !ok {
		ai.Options["voice_settings"] = defaultTTSVoiceSettings()
	}
}

func defaultTTSVoiceSettings() map[string]any {
	return map[string]any{
		"stability":         0.85,
		"similarity_boost":  0.95,
		"style":             0,
		"use_speaker_boost": true,
	}
}

func generatedAssetAI(a Asset) *AIAsset {
	if strings.TrimSpace(a.Provider) != "" && strings.ToLower(strings.TrimSpace(a.Provider)) != "media-studio" {
		return nil
	}
	req := map[string]any{}
	for k, v := range a.Request {
		req[k] = v
	}
	if req["media_kind"] == nil {
		if kind, _ := req["kind"].(string); kind != "" {
			req["media_kind"] = kind
		} else if a.Kind != "" {
			req["media_kind"] = a.Kind
		}
	}
	b, _ := json.Marshal(req)
	var ai AIAsset
	if err := json.Unmarshal(b, &ai); err != nil {
		return nil
	}
	if ai.MediaKind == "" {
		return nil
	}
	applyDefaultAIOptions(&ai)
	return &ai
}

func assetTypeForAI(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "image":
		return "image"
	case "audio_tts", "audio_sfx", "music", "audio":
		return "audio"
	default:
		return "video"
	}
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
