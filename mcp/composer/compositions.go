package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Canonical Edit JSON. The renderer supports a primary visual track, optional
// additional visual tracks as timed layers, and any number of timed audio
// tracks. The schema intentionally stays close to SaaS render APIs
// (Shotstack/Creatomate-style tracks and clips) while keeping unsupported
// features explicit in validation.

type Edit struct {
	Timeline Timeline `json:"timeline"`
}

type Timeline struct {
	Soundtrack *Soundtrack `json:"soundtrack,omitempty"`
	Background string      `json:"background,omitempty"` // hex color, e.g. "#000000"
	Tracks     []Track     `json:"tracks"`
	Markers    []Marker    `json:"markers,omitempty"`
}

// Marker is an editable timeline event supplied by a recorder or an agent.
// Composer stores markers with the composition and exposes them in the panel;
// they do not change the render unless a caller uses them to author clips or
// camera keyframes.
type Marker struct {
	ID       string         `json:"id,omitempty"`
	Time     float64        `json:"time"`
	Type     string         `json:"type"`
	Label    string         `json:"label,omitempty"`
	Value    any            `json:"value,omitempty"`
	Duration float64        `json:"duration,omitempty"`
	Region   *SourceCrop    `json:"region,omitempty"`
	Meta     map[string]any `json:"meta,omitempty"`
}

type Soundtrack struct {
	Src    string   `json:"src"`              // storage:N | https://… | mediastudio:N
	Volume float64  `json:"volume,omitempty"` // 0..1, default 1.0
	Timing *Timing  `json:"timing,omitempty"`
	AI     *AIAsset `json:"ai,omitempty"`
}

type Track struct {
	ID    string `json:"id,omitempty"`
	Type  string `json:"type,omitempty"` // visual|video|audio|overlay|text
	Clips []Clip `json:"clips"`
}

type Clip struct {
	UID             string      `json:"uid,omitempty"`
	SectionID       string      `json:"section_id,omitempty"`
	GroupID         string      `json:"group_id,omitempty"`
	Asset           Asset       `json:"asset"`
	Start           float64     `json:"start"`                   // seconds from composition start
	Length          float64     `json:"length"`                  // seconds
	SourceStart     float64     `json:"source_start,omitempty"`  // seconds from source start
	SourceEnd       float64     `json:"source_end,omitempty"`    // absolute source time; 0 means unbounded
	PlaybackRate    float64     `json:"playback_rate,omitempty"` // source speed multiplier; default 1
	Duration        float64     `json:"duration,omitempty"`
	Crop            *SourceCrop `json:"crop,omitempty"`      // normalized source-space rectangle
	Transform       *Transform  `json:"transform,omitempty"` // source-space focus and zoom
	Fit             string      `json:"fit,omitempty"`       // crop|contain|cover|stretch|none
	Width           float64     `json:"width,omitempty"`     // pixels; values 0..1 are treated as viewport-relative
	Height          float64     `json:"height,omitempty"`    // pixels; values 0..1 are treated as viewport-relative
	Scale           float64     `json:"scale,omitempty"`     // multiplier after fit sizing
	Opacity         float64     `json:"opacity,omitempty"`   // 0..1, default 1
	Offset          *Offset     `json:"offset,omitempty"`    // Shotstack-style viewport-relative offset
	Layout          *ClipLayout `json:"layout,omitempty"`    // Composer convenience alias, normalized at render time
	ZIndex          int         `json:"z_index,omitempty"`   // optional per-track ordering hint
	BorderRadius    float64     `json:"border_radius,omitempty"`
	Shadow          bool        `json:"shadow,omitempty"`
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
	Position        *Position   `json:"position,omitempty"`
	Animation       *Animation  `json:"animation,omitempty"`
	AI              *AIAsset    `json:"ai,omitempty"`
}

type Timing struct {
	Mode         string  `json:"mode,omitempty"`          // fixed|fit_generated|fit_source|fit_group|fit_timeline
	Source       string  `json:"source,omitempty"`        // clip:<uid>|audio:<uid>|track:audio|section|group
	PaddingAfter float64 `json:"padding_after,omitempty"` // seconds added after the fitted source
	MinLength    float64 `json:"min_length,omitempty"`
	MaxLength    float64 `json:"max_length,omitempty"`
	Reflow       string  `json:"reflow,omitempty"`   // none|following|track|linked_group|composition; composition reflows section_id/group_id spans across tracks
	Behavior     string  `json:"behavior,omitempty"` // trim|pad|trim_or_loop|loop|stretch|regenerate
	FadeOut      float64 `json:"fade_out,omitempty"`
}

type Asset struct {
	Type     string         `json:"type"` // video|image|audio|generated
	Src      string         `json:"src"`  // storage:N | https://… | mediastudio:N
	Provider string         `json:"provider,omitempty"`
	Kind     string         `json:"kind,omitempty"`
	Request  map[string]any `json:"request,omitempty"`
	Text     string         `json:"text,omitempty"`
	Font     *TextFont      `json:"font,omitempty"`
	Style    *TextStyle     `json:"style,omitempty"`
	Stroke   *TextStroke    `json:"stroke,omitempty"`
	Shadow   *TextShadow    `json:"shadow,omitempty"`
	Align    *TextAlign     `json:"align,omitempty"`
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
	SourceImages             []string       `json:"source_images,omitempty"`
	Options                  map[string]any `json:"options,omitempty"`
	CacheKey                 string         `json:"cache_key,omitempty"`
	InputFingerprint         string         `json:"input_fingerprint,omitempty"`
	ContinuityFingerprint    string         `json:"continuity_fingerprint,omitempty"`
	CachePolicy              string         `json:"cache_policy,omitempty"`
	Status                   string         `json:"status,omitempty"` // draft | generating | ready | failed
	GenerationID             int64          `json:"generation_id,omitempty"`
	ProviderRequestID        string         `json:"provider_request_id,omitempty"`
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

type TextFont struct {
	Family  string  `json:"family,omitempty"`
	Size    int     `json:"size,omitempty"`
	Weight  int     `json:"weight,omitempty"`
	Color   string  `json:"color,omitempty"`
	Opacity float64 `json:"opacity,omitempty"`
}

type TextStyle struct {
	LetterSpacing int     `json:"letter_spacing,omitempty"`
	LineHeight    float64 `json:"line_height,omitempty"`
	Transform     string  `json:"transform,omitempty"` // none|uppercase|lowercase
	Wrap          bool    `json:"wrap,omitempty"`
	AutoSize      bool    `json:"auto_size,omitempty"`
	MaxWidth      float64 `json:"max_width,omitempty"`  // pixels, or viewport fraction when <= 1
	MaxHeight     float64 `json:"max_height,omitempty"` // pixels, or viewport fraction when <= 1
	MinFontSize   int     `json:"min_font_size,omitempty"`
	Padding       int     `json:"padding,omitempty"`
	SafeArea      float64 `json:"safe_area,omitempty"` // viewport fraction, e.g. 0.05
}

type TextStroke struct {
	Color   string  `json:"color,omitempty"`
	Width   int     `json:"width,omitempty"`
	Opacity float64 `json:"opacity,omitempty"`
}

type TextShadow struct {
	Color   string  `json:"color,omitempty"`
	OffsetX int     `json:"offset_x,omitempty"`
	OffsetY int     `json:"offset_y,omitempty"`
	Blur    int     `json:"blur,omitempty"`
	Opacity float64 `json:"opacity,omitempty"`
}

type TextAlign struct {
	Horizontal string `json:"horizontal,omitempty"` // left|center|right
	Vertical   string `json:"vertical,omitempty"`   // top|center|bottom
}

type Position struct {
	Name   string `json:"-"`
	X      string `json:"x,omitempty"`      // percent or pixels, e.g. 50%
	Y      string `json:"y,omitempty"`      // percent or pixels, e.g. 50%
	Anchor string `json:"anchor,omitempty"` // top-left|top|top-right|left|center|right|bottom-left|bottom|bottom-right
}

func (p *Position) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		p.Name = s
		p.Anchor = s
		return nil
	}
	type rawPosition Position
	var raw rawPosition
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	*p = Position(raw)
	return nil
}

type Offset struct {
	X float64 `json:"x,omitempty"`
	Y float64 `json:"y,omitempty"`
}

// SourceCrop selects a normalized rectangle from the source before fitting it
// into the output layout. Values are fractions in the inclusive 0..1 range.
type SourceCrop struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

// Transform controls source-space camera motion. X and Y are normalized focus
// points (0..1); Scale is zoom (1 is unchanged). Keyframe times are relative
// to the clip, which keeps the motion intact when the clip moves on timeline.
type Transform struct {
	X         *float64            `json:"x,omitempty"`
	Y         *float64            `json:"y,omitempty"`
	Scale     float64             `json:"scale,omitempty"`
	Keyframes []TransformKeyframe `json:"keyframes,omitempty"`
}

type TransformKeyframe struct {
	Time   float64  `json:"time"`
	X      *float64 `json:"x,omitempty"`
	Y      *float64 `json:"y,omitempty"`
	Scale  float64  `json:"scale,omitempty"`
	Easing string   `json:"easing,omitempty"` // linear|ease_in|ease_out|ease_in_out
}

type ClipLayout struct {
	Fit          string   `json:"fit,omitempty"`
	X            *float64 `json:"x,omitempty"`
	Y            *float64 `json:"y,omitempty"`
	Width        float64  `json:"width,omitempty"`
	Height       float64  `json:"height,omitempty"`
	Anchor       string   `json:"anchor,omitempty"`
	Position     string   `json:"position,omitempty"`
	Margin       float64  `json:"margin,omitempty"`
	MarginX      float64  `json:"margin_x,omitempty"`
	MarginY      float64  `json:"margin_y,omitempty"`
	Opacity      float64  `json:"opacity,omitempty"`
	Scale        float64  `json:"scale,omitempty"`
	BorderRadius float64  `json:"border_radius,omitempty"`
	Shadow       bool     `json:"shadow,omitempty"`
	ZIndex       int      `json:"z_index,omitempty"`
}

type Animation struct {
	In        *AnimationPreset `json:"in,omitempty"`
	Out       *AnimationPreset `json:"out,omitempty"`
	Keyframes *Keyframes       `json:"keyframes,omitempty"`
}

type AnimationPreset struct {
	Preset    string  `json:"preset,omitempty"` // none|fade|fade_up|fade_down|slide_left|slide_right|scale_pop|typewriter|word_by_word
	Duration  float64 `json:"duration,omitempty"`
	Easing    string  `json:"easing,omitempty"` // linear|ease_in|ease_out|ease_in_out
	Direction string  `json:"direction,omitempty"`
	Style     string  `json:"style,omitempty"` // element|word|character
}

type Keyframes struct {
	Opacity []Tween `json:"opacity,omitempty"`
	X       []Tween `json:"x,omitempty"`
	Y       []Tween `json:"y,omitempty"`
	Scale   []Tween `json:"scale,omitempty"`
	Rotate  []Tween `json:"rotate,omitempty"`
}

type Tween struct {
	From   any     `json:"from,omitempty"`
	To     any     `json:"to,omitempty"`
	Start  float64 `json:"start,omitempty"`
	Length float64 `json:"length,omitempty"`
	Easing string  `json:"easing,omitempty"`
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
		} else if tt != "audio" && tt != "overlay" {
			return fmt.Errorf("track[%d]: unsupported track.type %q (use visual, audio, or overlay/text)", ti, track.Type)
		}
		for i := range track.Clips {
			c := &track.Clips[i]
			at := clipAssetType(*c, tt)
			if c.Asset.Src == "" && c.AI == nil && at != "silence" && at != "text" {
				return fmt.Errorf("track[%d].clip[%d]: asset.src required", ti, i)
			}
			if at == "" {
				at = "video"
			}
			if at != "video" && at != "image" && at != "audio" && at != "silence" && at != "text" {
				return fmt.Errorf("track[%d].clip[%d]: unsupported asset.type %q", ti, i, c.Asset.Type)
			}
			if tt == "overlay" && at != "text" {
				return fmt.Errorf("track[%d].clip[%d]: overlay/text tracks require text assets", ti, i)
			}
			if tt != "overlay" && at == "text" {
				return fmt.Errorf("track[%d].clip[%d]: text assets belong on overlay/text tracks", ti, i)
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
			if tt == "overlay" && strings.TrimSpace(textClipBody(*c)) == "" {
				return fmt.Errorf("track[%d].clip[%d]: text asset requires asset.text or text.body", ti, i)
			}
			if clipDuration(*c) <= 0 {
				return fmt.Errorf("track[%d].clip[%d]: length must be > 0", ti, i)
			}
			if c.Volume < 0 || c.Volume > 1 {
				return fmt.Errorf("track[%d].clip[%d]: volume must be 0..1", ti, i)
			}
			if err := validateSourceRange(c, at); err != nil {
				return fmt.Errorf("track[%d].clip[%d]: %w", ti, i, err)
			}
			if err := validatePlaybackRate(c.PlaybackRate, at); err != nil {
				return fmt.Errorf("track[%d].clip[%d]: %w", ti, i, err)
			}
			if (c.Crop != nil || c.Transform != nil) && at != "video" && at != "image" {
				return fmt.Errorf("track[%d].clip[%d]: crop/transform require a video or image asset", ti, i)
			}
			if err := validateSourceCrop(c.Crop); err != nil {
				return fmt.Errorf("track[%d].clip[%d]: crop: %w", ti, i, err)
			}
			if err := validateTransform(c.Transform, clipDuration(*c)); err != nil {
				return fmt.Errorf("track[%d].clip[%d]: transform: %w", ti, i, err)
			}
			if c.Opacity < 0 || c.Opacity > 1 {
				return fmt.Errorf("track[%d].clip[%d]: opacity must be 0..1", ti, i)
			}
			if err := validateClipLayout(c); err != nil {
				return fmt.Errorf("track[%d].clip[%d]: %w", ti, i, err)
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
			if err := validateTextStyle(c); err != nil {
				return fmt.Errorf("track[%d].clip[%d]: %w", ti, i, err)
			}
			if tt == "audio" && c.Text != nil {
				return fmt.Errorf("track[%d].clip[%d]: text overlays are only supported on visual clips", ti, i)
			}
			if c.Transition != nil {
				if tt != "visual" {
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
	for i := range e.Timeline.Markers {
		m := &e.Timeline.Markers[i]
		if m.Time < 0 {
			return fmt.Errorf("marker[%d]: time must be >= 0", i)
		}
		if m.Duration < 0 {
			return fmt.Errorf("marker[%d]: duration must be >= 0", i)
		}
		if strings.TrimSpace(m.Type) == "" {
			return fmt.Errorf("marker[%d]: type required", i)
		}
		if err := validateSourceCrop(m.Region); err != nil {
			return fmt.Errorf("marker[%d].region: %w", i, err)
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

func validateSourceRange(c *Clip, assetType string) error {
	if c == nil {
		return nil
	}
	if c.SourceStart < 0 || c.SourceEnd < 0 {
		return errors.New("source_start/source_end must be >= 0")
	}
	if c.SourceEnd > 0 && c.SourceEnd <= c.SourceStart {
		return errors.New("source_end must be greater than source_start")
	}
	if (c.SourceStart > 0 || c.SourceEnd > 0) && assetType != "video" && assetType != "audio" {
		return fmt.Errorf("source ranges require a video or audio asset (got %s)", assetType)
	}
	return nil
}

func validatePlaybackRate(rate float64, assetType string) error {
	if rate == 0 || rate == 1 {
		return nil
	}
	if assetType != "video" && assetType != "audio" {
		return fmt.Errorf("playback_rate requires a video or audio asset (got %s)", assetType)
	}
	if rate < 0.25 || rate > 16 {
		return errors.New("playback_rate must be between 0.25 and 16")
	}
	return nil
}

func validateSourceCrop(c *SourceCrop) error {
	if c == nil {
		return nil
	}
	if c.X < 0 || c.Y < 0 || c.X > 1 || c.Y > 1 {
		return errors.New("x/y must be between 0 and 1")
	}
	if c.Width <= 0 || c.Height <= 0 || c.Width > 1 || c.Height > 1 {
		return errors.New("width/height must be greater than 0 and at most 1")
	}
	if c.X+c.Width > 1.000001 || c.Y+c.Height > 1.000001 {
		return errors.New("rectangle must stay inside the source")
	}
	return nil
}

func validateTransform(t *Transform, duration float64) error {
	if t == nil {
		return nil
	}
	if err := validateFocus(t.X, "x"); err != nil {
		return err
	}
	if err := validateFocus(t.Y, "y"); err != nil {
		return err
	}
	if t.Scale != 0 && (t.Scale < 1 || t.Scale > 8) {
		return errors.New("scale must be between 1 and 8")
	}
	previous := -1.0
	for i, k := range t.Keyframes {
		if k.Time < 0 || k.Time > duration+0.001 {
			return fmt.Errorf("keyframes[%d].time must be within the clip", i)
		}
		if k.Time < previous {
			return errors.New("keyframes must be ordered by time")
		}
		previous = k.Time
		if err := validateFocus(k.X, fmt.Sprintf("keyframes[%d].x", i)); err != nil {
			return err
		}
		if err := validateFocus(k.Y, fmt.Sprintf("keyframes[%d].y", i)); err != nil {
			return err
		}
		if k.Scale != 0 && (k.Scale < 1 || k.Scale > 8) {
			return fmt.Errorf("keyframes[%d].scale must be between 1 and 8", i)
		}
		switch strings.ToLower(strings.TrimSpace(k.Easing)) {
		case "", "linear", "ease_in", "ease_out", "ease_in_out":
		default:
			return fmt.Errorf("keyframes[%d].easing must be linear|ease_in|ease_out|ease_in_out", i)
		}
	}
	return nil
}

func validateFocus(value *float64, name string) error {
	if value != nil && (*value < 0 || *value > 1) {
		return fmt.Errorf("%s must be between 0 and 1", name)
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

func validateTextStyle(c *Clip) error {
	if c == nil {
		return nil
	}
	if c.Asset.Font != nil {
		if c.Asset.Font.Size < 0 || c.Asset.Font.Size > 512 {
			return errors.New("text font.size must be 0..512")
		}
		if c.Asset.Font.Opacity < 0 || c.Asset.Font.Opacity > 1 {
			return errors.New("text font.opacity must be 0..1")
		}
	}
	if c.Asset.Style != nil {
		switch strings.ToLower(strings.TrimSpace(c.Asset.Style.Transform)) {
		case "", "none", "uppercase", "lowercase", "capitalize":
		default:
			return fmt.Errorf("text style.transform must be none|uppercase|lowercase|capitalize (got %q)", c.Asset.Style.Transform)
		}
		if c.Asset.Style.LineHeight < 0 || c.Asset.Style.LineHeight > 4 {
			return errors.New("text style.line_height must be 0..4")
		}
		if c.Asset.Style.MaxWidth < 0 || c.Asset.Style.MaxHeight < 0 || c.Asset.Style.Padding < 0 || c.Asset.Style.MinFontSize < 0 {
			return errors.New("text style max_width/max_height/padding/min_font_size must be >= 0")
		}
		if c.Asset.Style.SafeArea < 0 || c.Asset.Style.SafeArea >= 0.5 {
			return errors.New("text style.safe_area must be >= 0 and < 0.5")
		}
	}
	if c.Asset.Stroke != nil {
		if c.Asset.Stroke.Width < 0 || c.Asset.Stroke.Width > 64 {
			return errors.New("text stroke.width must be 0..64")
		}
		if c.Asset.Stroke.Opacity < 0 || c.Asset.Stroke.Opacity > 1 {
			return errors.New("text stroke.opacity must be 0..1")
		}
	}
	if c.Asset.Shadow != nil {
		if c.Asset.Shadow.Opacity < 0 || c.Asset.Shadow.Opacity > 1 {
			return errors.New("text shadow.opacity must be 0..1")
		}
	}
	if c.Asset.Align != nil {
		switch strings.ToLower(strings.TrimSpace(c.Asset.Align.Horizontal)) {
		case "", "left", "center", "right":
		default:
			return fmt.Errorf("text align.horizontal must be left|center|right (got %q)", c.Asset.Align.Horizontal)
		}
		switch strings.ToLower(strings.TrimSpace(c.Asset.Align.Vertical)) {
		case "", "top", "center", "bottom":
		default:
			return fmt.Errorf("text align.vertical must be top|center|bottom (got %q)", c.Asset.Align.Vertical)
		}
	}
	if c.Position != nil {
		switch normalizePositionName(c.Position.Anchor) {
		case "", "topleft", "top", "topright", "left", "center", "right", "bottomleft", "bottom", "bottomright":
		default:
			return fmt.Errorf("position.anchor must be one of top-left|top|top-right|left|center|right|bottom-left|bottom|bottom-right (got %q)", c.Position.Anchor)
		}
	}
	return validateAnimation(c.Animation)
}

func validateAnimation(a *Animation) error {
	if a == nil {
		return nil
	}
	if err := validateAnimationPreset(a.In); err != nil {
		return fmt.Errorf("animation.in: %w", err)
	}
	if err := validateAnimationPreset(a.Out); err != nil {
		return fmt.Errorf("animation.out: %w", err)
	}
	return nil
}

func validateAnimationPreset(p *AnimationPreset) error {
	if p == nil {
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(p.Preset)) {
	case "", "none", "fade", "fade_up", "fade_down", "slide_left", "slide_right", "scale_pop", "typewriter", "word_by_word":
	default:
		return fmt.Errorf("preset must be none|fade|fade_up|fade_down|slide_left|slide_right|scale_pop|typewriter|word_by_word (got %q)", p.Preset)
	}
	if p.Duration < 0 {
		return errors.New("duration must be >= 0")
	}
	switch strings.ToLower(strings.TrimSpace(p.Easing)) {
	case "", "linear", "ease", "ease_in", "ease_out", "ease_in_out":
	default:
		return fmt.Errorf("easing must be linear|ease|ease_in|ease_out|ease_in_out (got %q)", p.Easing)
	}
	switch strings.ToLower(strings.TrimSpace(p.Style)) {
	case "", "element", "word", "character":
	default:
		return fmt.Errorf("style must be element|word|character (got %q)", p.Style)
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
	if len(textOverlayClips(e)) > 0 && !hasVisual {
		return errors.New("text overlay tracks require a visual composition")
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

// editDurationSeconds returns the visible composition duration. The primary
// visual track duration is concatenated; timed visual overlays, timed audio
// tracks, and text overlays can extend it.
func editDurationSeconds(e *Edit) float64 {
	if e == nil || len(e.Timeline.Tracks) == 0 {
		return 0
	}
	var d float64
	if vt := primaryVisualTrack(e); vt != nil {
		d = baseVisualDuration(vt)
	}
	for _, ref := range visualOverlayClipRefs(e) {
		if end := ref.clip.Start + clipDuration(ref.clip); end > d {
			d = end
		}
	}
	for _, c := range audioTimelineClips(e) {
		if end := c.Start + clipDuration(c); end > d {
			d = end
		}
	}
	for _, c := range textOverlayClips(e) {
		if end := c.Start + clipDuration(c); end > d {
			d = end
		}
	}
	return d
}

func visualTracks(e *Edit) []*Track {
	if e == nil {
		return nil
	}
	out := []*Track{}
	for i := range e.Timeline.Tracks {
		if trackKind(e.Timeline.Tracks[i]) == "visual" {
			out = append(out, &e.Timeline.Tracks[i])
		}
	}
	return out
}

func primaryVisualTrack(e *Edit) *Track {
	tracks := visualTracks(e)
	if len(tracks) == 0 {
		return nil
	}
	return tracks[0]
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

func textOverlayClips(e *Edit) []Clip {
	if e == nil {
		return nil
	}
	var out []Clip
	for _, t := range e.Timeline.Tracks {
		if trackKind(t) != "overlay" {
			continue
		}
		out = append(out, t.Clips...)
	}
	return out
}

type visualClipRef struct {
	trackIndex int
	clipIndex  int
	inputIndex int
	base       bool
	clip       Clip
}

func visualClipRefs(e *Edit) []visualClipRef {
	tracks := visualTracks(e)
	refs := []visualClipRef{}
	inputIdx := 0
	for ti, t := range tracks {
		for ci, c := range t.Clips {
			refs = append(refs, visualClipRef{
				trackIndex: ti,
				clipIndex:  ci,
				inputIndex: inputIdx,
				base:       ti == 0,
				clip:       c,
			})
			inputIdx++
		}
	}
	return refs
}

func visualOverlayClipRefs(e *Edit) []visualClipRef {
	refs := visualClipRefs(e)
	out := []visualClipRef{}
	for _, ref := range refs {
		if !ref.base {
			out = append(out, ref)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		zi := clipZIndex(out[i].clip)
		zj := clipZIndex(out[j].clip)
		if zi != zj {
			return zi < zj
		}
		if out[i].trackIndex != out[j].trackIndex {
			return out[i].trackIndex < out[j].trackIndex
		}
		return out[i].clipIndex < out[j].clipIndex
	})
	return out
}

func totalVisualClipCount(e *Edit) int {
	return len(visualClipRefs(e))
}

func clipZIndex(c Clip) int {
	if c.Layout != nil && c.Layout.ZIndex != 0 {
		return c.Layout.ZIndex
	}
	return c.ZIndex
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
	case "overlay", "text", "title", "titles", "subtitle", "subtitles":
		return "overlay"
	default:
		return strings.ToLower(strings.TrimSpace(t.Type))
	}
}

func validateClipLayout(c *Clip) error {
	if c == nil {
		return nil
	}
	if err := validateFit(c.Fit); err != nil {
		return err
	}
	if c.Width < 0 || c.Height < 0 || c.Scale < 0 || c.BorderRadius < 0 {
		return errors.New("layout width/height/scale/border_radius must be >= 0")
	}
	if c.Layout != nil {
		if err := validateFit(c.Layout.Fit); err != nil {
			return err
		}
		if c.Layout.Width < 0 || c.Layout.Height < 0 || c.Layout.Margin < 0 || c.Layout.MarginX < 0 || c.Layout.MarginY < 0 || c.Layout.Scale < 0 || c.Layout.BorderRadius < 0 {
			return errors.New("layout width/height/margin/scale/border_radius must be >= 0")
		}
		if c.Layout.Opacity < 0 || c.Layout.Opacity > 1 {
			return errors.New("layout.opacity must be 0..1")
		}
	}
	return nil
}

func validateFit(fit string) error {
	switch strings.ToLower(strings.TrimSpace(fit)) {
	case "", "crop", "contain", "cover", "stretch", "none":
		return nil
	default:
		return fmt.Errorf("fit must be crop|contain|cover|stretch|none (got %q)", fit)
	}
}

func clipAssetType(c Clip, trackType string) string {
	switch strings.ToLower(strings.TrimSpace(c.Asset.Type)) {
	case "", "generated":
		if trackType == "overlay" {
			return "text"
		}
		if c.AI != nil {
			return assetTypeForAI(c.AI.MediaKind)
		}
		if trackType == "audio" {
			return "audio"
		}
		return "video"
	case "silence", "blank", "gap":
		return "silence"
	case "text", "rich-text", "rich_text", "title", "caption":
		return "text"
	default:
		return strings.ToLower(strings.TrimSpace(c.Asset.Type))
	}
}

func textClipBody(c Clip) string {
	if strings.TrimSpace(c.Asset.Text) != "" {
		return c.Asset.Text
	}
	if c.Text != nil {
		return c.Text.Body
	}
	return ""
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
	switch strings.ToLower(strings.TrimSpace(c.AI.MediaKind)) {
	case "audio_tts":
		c.Audio = &AudioFX{
			Normalize:      true,
			LoudnessTarget: -16,
			PeakLimitDB:    -2,
		}
	case "audio_sfx":
		c.Audio = &AudioFX{
			Normalize:      true,
			LoudnessTarget: -16,
			PeakLimitDB:    -2,
			TrimSilence:    true,
		}
	default:
		return
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
	defaults := defaultTTSVoiceSettings()
	settings, _ := ai.Options["voice_settings"].(map[string]any)
	if settings == nil {
		settings = map[string]any{}
	}
	for key, value := range defaults {
		if _, exists := settings[key]; !exists {
			settings[key] = value
		}
	}
	ai.Options["voice_settings"] = settings
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
	if v, ok := args["markers"]; ok {
		timeline["markers"] = v
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
