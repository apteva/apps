package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

type materializeResult struct {
	Changed bool
	Pending []string
}

func materializeAIAssets(ctx *sdk.AppCtx, edit *Edit, compositionID int64, projectID string) (materializeResult, error) {
	var out materializeResult
	if edit == nil {
		return out, nil
	}
	for ti := range edit.Timeline.Tracks {
		for i := range edit.Timeline.Tracks[ti].Clips {
			clip := &edit.Timeline.Tracks[ti].Clips[i]
			normalizeGeneratedAsset(clip)
			if clip.UID == "" {
				clip.UID = fmt.Sprintf("clip-%d", i+1)
				out.Changed = true
			}
			if clip.AI == nil {
				continue
			}
			changed, pending, err := materializeOneAIAsset(ctx, clip.AI, "clip "+clip.UID, projectID)
			if err != nil {
				return out, err
			}
			if changed {
				out.Changed = true
			}
			if pending != "" {
				out.Pending = append(out.Pending, pending)
				continue
			}
			if clip.AI.StorageID > 0 {
				nextSrc := fmt.Sprintf("storage:%d", clip.AI.StorageID)
				if clip.Asset.Src != nextSrc {
					clip.Asset.Src = nextSrc
					out.Changed = true
				}
				nextType := assetTypeForAI(clip.AI.MediaKind)
				if nextType != "" && clip.Asset.Type != nextType {
					clip.Asset.Type = nextType
					out.Changed = true
				}
			}
			if clip.AI.StorageID > 0 && clip.AI.ActualDurationSeconds <= 0 && aiKindHasMediaDuration(clip.AI.MediaKind) {
				if d := probeAssetDurationSeconds(ctx, clip.Asset.Src); d > 0 {
					clip.AI.ActualDurationSeconds = d
					if clip.AI.AudioAnalysis == nil {
						clip.AI.AudioAnalysis = &AudioAnalysis{}
					}
					clip.AI.AudioAnalysis.DurationSeconds = d
					out.Changed = true
				}
			}
			if syncClipDurationFromAI(clip) {
				out.Changed = true
			}
		}
	}
	if s := edit.Timeline.Soundtrack; s != nil && s.AI != nil {
		changed, pending, err := materializeOneAIAsset(ctx, s.AI, "soundtrack", projectID)
		if err != nil {
			return out, err
		}
		if changed {
			out.Changed = true
		}
		if pending != "" {
			out.Pending = append(out.Pending, pending)
		} else if s.AI.StorageID > 0 {
			nextSrc := fmt.Sprintf("storage:%d", s.AI.StorageID)
			if s.Src != nextSrc {
				s.Src = nextSrc
				out.Changed = true
			}
			if s.AI.ActualDurationSeconds <= 0 && aiKindHasMediaDuration(s.AI.MediaKind) {
				if d := probeAssetDurationSeconds(ctx, s.Src); d > 0 {
					s.AI.ActualDurationSeconds = d
					if s.AI.AudioAnalysis == nil {
						s.AI.AudioAnalysis = &AudioAnalysis{}
					}
					s.AI.AudioAnalysis.DurationSeconds = d
					out.Changed = true
				}
			}
		}
	}
	if resolveRelativeClipStarts(edit) {
		out.Changed = true
	}
	if out.Changed {
		b, _ := json.Marshal(edit)
		_, _ = ctx.AppDB().Exec(
			`UPDATE compositions SET edit_json=?, duration_seconds=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`,
			string(b), editDurationSeconds(edit), compositionID,
		)
	}
	return out, nil
}

func resolveRelativeClipStarts(edit *Edit) bool {
	if edit == nil {
		return false
	}
	changed := false
	for ti := range edit.Timeline.Tracks {
		track := &edit.Timeline.Tracks[ti]
		byID := map[string]*Clip{}
		for i := range track.Clips {
			if track.Clips[i].UID != "" {
				byID[track.Clips[i].UID] = &track.Clips[i]
			}
		}
		var prev *Clip
		for i := range track.Clips {
			clip := &track.Clips[i]
			target := prev
			if clip.AfterClipID != "" {
				target = byID[clip.AfterClipID]
			}
			if target != nil && (clip.AfterClipID != "" || clip.GapSeconds > 0 || durationModeReflows(target.DurationMode)) {
				nextStart := target.Start + clipDuration(*target) + clip.GapSeconds
				if trimFloat(nextStart) != trimFloat(clip.Start) {
					clip.Start = nextStart
					changed = true
				}
			}
			prev = clip
		}
	}
	return changed
}

func materializeOneAIAsset(ctx *sdk.AppCtx, ai *AIAsset, label, projectID string) (bool, string, error) {
	if ai == nil {
		return false, "", nil
	}
	changed := false
	if ai.CachePolicy == "" {
		ai.CachePolicy = "reuse"
		changed = true
	}
	if ai.CacheKey == "" {
		ai.CacheKey = aiCacheKey(ai)
		changed = true
	}
	if ai.StorageID > 0 && ai.Status == "ready" {
		return changed, "", nil
	}
	if strings.TrimSpace(ai.Prompt) == "" {
		ai.Status = "failed"
		ai.Error = "prompt required"
		return true, "", errors.New(label + ": AI prompt required")
	}
	args := map[string]any{
		"kind":         ai.MediaKind,
		"prompt":       ai.Prompt,
		"cache_key":    ai.CacheKey,
		"cache_policy": ai.CachePolicy,
		"_project_id":  projectID,
	}
	if ai.Model != "" {
		args["model"] = ai.Model
	}
	if ai.Duration > 0 {
		args["duration"] = ai.Duration
	}
	if ai.Aspect != "" {
		args["aspect"] = ai.Aspect
	}
	if ai.Voice != "" {
		args["voice"] = ai.Voice
	}
	if ai.Avatar != "" {
		args["avatar"] = ai.Avatar
	}
	if ai.SourceImage != "" {
		args["source_image"] = ai.SourceImage
	}
	if len(ai.Options) > 0 {
		args["options"] = ai.Options
	}
	if ai.EstimatedDurationSeconds > 0 {
		opts, _ := args["options"].(map[string]any)
		if opts == nil {
			opts = map[string]any{}
		}
		if _, ok := opts["estimated_duration_seconds"]; !ok {
			opts["estimated_duration_seconds"] = ai.EstimatedDurationSeconds
		}
		args["options"] = opts
	}
	var got map[string]any
	if err := ctx.PlatformAPI().CallAppResult("media-studio", "media_generate", args, &got); err != nil {
		ai.Status = "failed"
		ai.Error = err.Error()
		return true, "", fmt.Errorf("%s: media-studio generate failed: %w", label, err)
	}
	meta, _ := got["_meta"].(map[string]any)
	if meta == nil {
		ai.Status = "failed"
		ai.Error = "media-studio returned no _meta"
		return true, "", errors.New(label + ": media-studio returned no _meta")
	}
	if status, _ := meta["status"].(string); status == "queued" || status == "polling" {
		ai.Status = "generating"
		ai.JobID = number(meta["job_id"])
		if v := floatNumber(meta["estimated_duration_seconds"]); v > 0 {
			ai.EstimatedDurationSeconds = v
		}
		ai.Error = ""
		return true, fmt.Sprintf("%s queued as media-studio job #%d", label, ai.JobID), nil
	}
	if id := number(meta["generation_id"]); id > 0 {
		ai.GenerationID = id
	}
	if v := floatNumber(meta["estimated_duration_seconds"]); v > 0 {
		ai.EstimatedDurationSeconds = v
	}
	if v := floatNumber(meta["actual_duration_seconds"]); v > 0 {
		ai.ActualDurationSeconds = v
	}
	if a := audioAnalysisFromMeta(meta["audio_analysis"]); a != nil {
		ai.AudioAnalysis = a
	}
	if v := floatNumber(meta["peak_db"]); v != 0 {
		ai.PeakDB = v
		if ai.AudioAnalysis != nil {
			ai.AudioAnalysis.PeakDB = v
		}
	}
	if v := floatNumber(meta["rms_db"]); v != 0 {
		ai.RMSDB = v
		if ai.AudioAnalysis != nil {
			ai.AudioAnalysis.RMSDB = v
		}
	}
	if ids := numberSlice(meta["storage_ids"]); len(ids) > 0 {
		ai.StorageID = ids[0]
		ai.Status = "ready"
		ai.Error = ""
		return true, "", nil
	}
	ai.Status = "failed"
	ai.Error = "Media Studio returned no storage id; bind Storage to Media Studio for Composer AI assets"
	return true, "", errors.New(label + ": Media Studio returned no storage id")
}

func syncClipDurationFromAI(c *Clip) bool {
	if c == nil || c.AI == nil {
		return false
	}
	changed := false
	if c.EstimatedLength <= 0 && c.AI.EstimatedDurationSeconds > 0 {
		c.EstimatedLength = c.AI.EstimatedDurationSeconds
		changed = true
	}
	if c.ActualLength <= 0 && c.AI.ActualDurationSeconds > 0 {
		c.ActualLength = c.AI.ActualDurationSeconds
		changed = true
	}
	if c.DurationMode == "" {
		c.DurationMode = defaultDurationMode(c.AI.MediaKind)
		changed = true
	}
	if durationModeFitsGenerated(c.DurationMode) && c.AI.ActualDurationSeconds > 0 && c.Length != c.AI.ActualDurationSeconds {
		c.Length = c.AI.ActualDurationSeconds
		changed = true
	}
	if c.Length <= 0 && c.EstimatedLength > 0 {
		c.Length = c.EstimatedLength
		changed = true
	}
	return changed
}

func durationModeFitsGenerated(mode string) bool {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "fit_generated", "fit_generated_keep_start", "fit_generated_reflow":
		return true
	default:
		return false
	}
}

func durationModeReflows(mode string) bool {
	return strings.ToLower(strings.TrimSpace(mode)) == "fit_generated_reflow"
}

func aiKindHasMediaDuration(kind string) bool {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "audio_tts", "audio_sfx", "music", "video", "avatar":
		return true
	default:
		return false
	}
}

func aiCacheKey(ai *AIAsset) string {
	stable := map[string]any{
		"media_kind":   ai.MediaKind,
		"prompt":       ai.Prompt,
		"model":        ai.Model,
		"duration":     ai.Duration,
		"aspect":       ai.Aspect,
		"voice":        ai.Voice,
		"avatar":       ai.Avatar,
		"source_image": ai.SourceImage,
		"options":      sortedMap(ai.Options),
	}
	b, _ := json.Marshal(stable)
	sum := sha256.Sum256(b)
	return "composer:" + hex.EncodeToString(sum[:16])
}

func sortedMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	keys := make([]string, 0, len(in))
	for k := range in {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make(map[string]any, len(in))
	for _, k := range keys {
		out[k] = in[k]
	}
	return out
}

func number(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case float64:
		return int64(n)
	case json.Number:
		i, _ := n.Int64()
		return i
	}
	return 0
}

func floatNumber(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case float32:
		return float64(n)
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case json.Number:
		f, _ := n.Float64()
		return f
	}
	return 0
}

func numberSlice(v any) []int64 {
	switch ids := v.(type) {
	case []int64:
		return ids
	case []any:
		out := make([]int64, 0, len(ids))
		for _, id := range ids {
			if n := number(id); n > 0 {
				out = append(out, n)
			}
		}
		return out
	}
	return nil
}

func audioAnalysisFromMeta(v any) *AudioAnalysis {
	m, _ := v.(map[string]any)
	if m == nil {
		return nil
	}
	a := &AudioAnalysis{
		DurationSeconds: floatNumber(m["duration_seconds"]),
		PeakDB:          floatNumber(m["peak_db"]),
		RMSDB:           floatNumber(m["rms_db"]),
		SampleRate:      int(floatNumber(m["sample_rate"])),
		Channels:        int(floatNumber(m["channels"])),
	}
	if codec, _ := m["codec"].(string); codec != "" {
		a.Codec = codec
	}
	return a
}
