package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
)

// OutputSettings are export choices, independent of the source master duration.
type OutputSettings struct {
	Output
	ExcerptStart float64 `json:"excerpt_start,omitempty"`
	ExcerptEnd   float64 `json:"excerpt_end,omitempty"`
}

type outputSnapshot struct {
	Edit     *Edit          `json:"edit,omitempty"`
	Spec     *V2Composition `json:"spec,omitempty"`
	Settings OutputSettings `json:"settings"`
	Executor string         `json:"executor,omitempty"`
}

func outputJSON(v any) string { b, _ := json.Marshal(v); return string(b) }
func outputHash(v any) string {
	sum := sha256.Sum256([]byte(outputJSON(v)))
	return hex.EncodeToString(sum[:])
}
func validOutputKind(kind string) bool {
	return kind == "song" || kind == "image_video" || kind == "full_clip"
}

func validateOutputSettings(kind string, s *OutputSettings) error {
	if !validOutputKind(kind) {
		return errors.New("unknown output kind")
	}
	if kind == "song" {
		if s.Format != "mp3" && s.Format != "wav" && s.Format != "m4a" && s.Format != "aac" {
			return errors.New("audio output requires mp3, wav, m4a or aac")
		}
	} else if s.Format != "mp4" {
		return errors.New("video output requires mp4")
	}
	if math.IsNaN(s.ExcerptStart) || math.IsInf(s.ExcerptStart, 0) || math.IsNaN(s.ExcerptEnd) || math.IsInf(s.ExcerptEnd, 0) || s.ExcerptStart < 0 || s.ExcerptEnd < 0 || (s.ExcerptEnd > 0 && s.ExcerptEnd <= s.ExcerptStart) {
		return errors.New("invalid excerpt range")
	}
	if err := validateOutputValues(s.Output); err != nil {
		return err
	}
	validateOutput(&s.Output)
	return nil
}

// An optional plan replaces visuals only. Audio always comes from shared inputs.
// The canonical edit is never materialized or shortened by an output render.
func buildOutputSnapshot(row map[string]any, kind string, settings OutputSettings, plan string, master *Clip) (outputSnapshot, error) {
	snap := outputSnapshot{Settings: settings}
	if err := validateOutputSettings(kind, &snap.Settings); err != nil {
		return snap, err
	}
	var edit *Edit
	raw := row["edit_json"].(string)
	if isV2EditJSON(raw) {
		spec, err := parseV2CompositionJSON(raw)
		if err != nil {
			return snap, err
		}
		if kind != "song" && plan == "{}" && !v2HasVideoElements(spec) {
			return buildNativeOutputSnapshot(spec, settings, master)
		}
		// Audio extraction must not first lower visual scenes into a timeline.
		if kind == "song" || plan != "{}" {
			spec.Scenes = nil
			tracks := []V2Track{}
			for _, t := range spec.Tracks {
				if t.Type == "audio" {
					tracks = append(tracks, t)
				}
			}
			spec.Tracks = tracks
			spec.Output.Format = "mp3"
		}
		if master != nil && (kind == "song" || plan != "{}") {
			edit = &Edit{Timeline: Timeline{Background: spec.Background}}
		} else {
			var err2 error
			edit, _, _, err2 = v2ToV1FFmpeg(spec)
			if err2 != nil {
				return snap, err2
			}
		}
	} else {
		var err error
		edit, err = parseEditJSON(raw)
		if err != nil {
			return snap, err
		}
	}
	audio := []Track{}
	visuals := []Track{}
	for _, t := range edit.Timeline.Tracks {
		if trackKind(t) == "audio" {
			audio = append(audio, t)
		} else {
			visuals = append(visuals, t)
		}
	}
	if master != nil {
		audio = []Track{{Type: "audio", Clips: []Clip{*master}}}
		edit.Timeline.Soundtrack = nil
	}
	if st := edit.Timeline.Soundtrack; st != nil {
		length := editDurationSeconds(edit)
		if st.AI != nil {
			if st.AI.ActualDurationSeconds > 0 {
				length = st.AI.ActualDurationSeconds
			} else if st.AI.Duration > 0 {
				length = float64(st.AI.Duration)
			}
		}
		audio = append(audio, Track{Type: "audio", Clips: []Clip{{UID: "shared-soundtrack", Asset: Asset{Type: "audio", Src: st.Src}, Length: length, Volume: st.Volume, AI: st.AI}}})
		edit.Timeline.Soundtrack = nil
	}
	if plan != "{}" && plan != "" {
		var p Edit
		if err := json.Unmarshal([]byte(plan), &p); err != nil {
			return snap, err
		}
		visuals = nil
		for _, t := range p.Timeline.Tracks {
			if trackKind(t) == "audio" {
				return snap, errors.New("output plans contain visual/overlay tracks only; set the shared master for audio")
			}
			visuals = append(visuals, t)
		}
	}
	edit.Timeline.Tracks = audio
	if kind != "song" {
		hasVisual := false
		for _, t := range visuals {
			if kind == "image_video" {
				for _, c := range t.Clips {
					if clipAssetType(c, trackKind(t)) == "video" || (c.AI != nil && assetTypeForAI(c.AI.MediaKind) == "video") {
						return snap, errors.New("image video needs a still-image plan; choose images instead of video clips")
					}
				}
			}
			if trackKind(t) == "visual" {
				hasVisual = true
			}
			edit.Timeline.Tracks = append(edit.Timeline.Tracks, t)
		}
		if !hasVisual {
			return snap, errors.New("add a visual plan for this output")
		}
	}
	if len(audio) == 0 && kind == "song" {
		return snap, errors.New("select an audio master or add an audio track")
	}
	// Validate dependency kinds before any provider can be called.
	for _, t := range edit.Timeline.Tracks {
		for _, c := range t.Clips {
			if c.AI != nil {
				at := assetTypeForAI(c.AI.MediaKind)
				if kind == "song" && at != "audio" {
					return snap, errors.New("audio output cannot generate visual assets")
				}
				if kind == "image_video" && at == "video" {
					return snap, errors.New("image video cannot generate video assets")
				}
			}
		}
	}
	effectiveRange := settings
	if kind != "song" && effectiveRange.ExcerptEnd == 0 {
		visualEnd := 0.0
		for _, t := range edit.Timeline.Tracks {
			if trackKind(t) == "visual" {
				for _, c := range t.Clips {
					visualEnd = math.Max(visualEnd, c.Start+clipDuration(c))
				}
			}
		}
		if visualEnd > 0 && visualEnd < editDurationSeconds(edit) {
			effectiveRange.ExcerptEnd = visualEnd
		}
	}
	if err := cropOutputEdit(edit, effectiveRange); err != nil {
		return snap, err
	}
	if err := validateEditOutput(edit, snap.Settings.Output); err != nil {
		return snap, err
	}
	snap.Edit = edit
	return snap, nil
}

func cropOutputEdit(edit *Edit, s OutputSettings) error {
	if s.ExcerptStart == 0 && s.ExcerptEnd == 0 {
		return nil
	}
	duration := editDurationSeconds(edit)
	end := s.ExcerptEnd
	if end == 0 {
		end = duration
	}
	if s.ExcerptStart >= duration || end > duration+0.001 {
		return fmt.Errorf("excerpt must fit the %.3fs source timeline", duration)
	}
	tracks := []Track{}
	for _, t := range edit.Timeline.Tracks {
		clips := []Clip{}
		for _, c := range t.Clips {
			stop := math.Min(c.Start+clipDuration(c), end)
			start := math.Max(c.Start, s.ExcerptStart)
			if stop <= start {
				continue
			}
			if clipAssetType(c, trackKind(t)) != "image" && trackKind(t) != "overlay" {
				c.SourceStart += (start - c.Start) * clipPlaybackRate(c)
			}
			c.Start = start - s.ExcerptStart
			c.Length = stop - start
			c.Duration = c.Length
			c.DurationMode = "fixed_trim_pad"
			c.Timing = nil
			c.AfterClipID = ""
			c.GapSeconds = 0
			clips = append(clips, c)
		}
		if len(clips) > 0 {
			t.Clips = clips
			tracks = append(tracks, t)
		}
	}
	edit.Timeline.Tracks = tracks
	return nil
}

// Native V2 keeps visual scene timing intact. Only frame sampling moves to the
// selected excerpt; shared audio is cropped independently for the encoder.
func buildNativeOutputSnapshot(spec *V2Composition, s OutputSettings, master *Clip) (outputSnapshot, error) {
	snap := outputSnapshot{Settings: s, Spec: spec}
	duration := v2DurationSeconds(spec)
	end := s.ExcerptEnd
	if end == 0 {
		end = duration
	}
	if s.ExcerptStart >= end || end > duration {
		return snap, errors.New("excerpt must fit the visual scene timeline")
	}
	assets := map[string]V2Asset{}
	for _, a := range spec.Assets {
		assets[a.ID] = a
	}
	track, ok, err := v2AudioTrack(spec, assets)
	if err != nil {
		return snap, err
	}
	if !ok {
		if st, has, e := v2Soundtrack(spec, assets); e != nil {
			return snap, e
		} else if has {
			track = Track{Type: "audio", Clips: []Clip{{Asset: Asset{Type: "audio", Src: st.Src}, Length: duration, Volume: st.Volume}}}
			ok = true
		}
	}
	if master != nil {
		track = Track{Type: "audio", Clips: []Clip{*master}}
		ok = true
	}
	if ok {
		edit := &Edit{Timeline: Timeline{Tracks: []Track{track}}}
		if err = cropOutputEdit(edit, OutputSettings{ExcerptStart: s.ExcerptStart, ExcerptEnd: end}); err != nil {
			return snap, err
		}
		snap.Edit = edit
	}
	spec.Audio = nil
	tracks := []V2Track{}
	for _, t := range spec.Tracks {
		if t.Type != "audio" {
			tracks = append(tracks, t)
		}
	}
	spec.Tracks = tracks
	// Keep only scenes that intersect the export, with explicit source times.
	scenes := []V2Scene{}
	cursor := 0.0
	used := map[string]bool{}
	for _, scene := range spec.Scenes {
		if scene.Start <= 0 {
			scene.Start = cursor
		}
		cursor = scene.Start + scene.Duration
		if cursor <= s.ExcerptStart || scene.Start >= end {
			continue
		}
		for _, el := range scene.Elements {
			used[el.Asset] = true
		}
		scenes = append(scenes, scene)
	}
	spec.Scenes = scenes
	for _, t := range spec.Tracks {
		for _, c := range t.Clips {
			used[c.Asset] = true
		}
	}
	kept := []V2Asset{}
	for _, a := range spec.Assets {
		if used[a.ID] {
			kept = append(kept, a)
		}
	}
	spec.Assets = kept
	designW, designH := spec.Output.DesignWidth, spec.Output.DesignHeight
	if designW <= 0 {
		designW = spec.Output.Width
	}
	if designH <= 0 {
		designH = spec.Output.Height
	}
	spec.Output = V2Output{DesignWidth: designW, DesignHeight: designH, Renderer: spec.Output.Renderer, Format: s.Format, Resolution: s.Resolution, Aspect: s.Aspect, FPS: s.FPS, Duration: duration, Background: spec.Output.Background}
	return snap, nil
}
