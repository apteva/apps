package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

type materializeResult struct {
	Changed bool
	Pending []string
}

func materializeAIAssets(ctx *sdk.AppCtx, edit *Edit, compositionID int64, projectID string, persist bool) (materializeResult, error) {
	var out materializeResult
	if edit == nil {
		return out, nil
	}
	for ti := range edit.Timeline.Tracks {
		for i := range edit.Timeline.Tracks[ti].Clips {
			track := &edit.Timeline.Tracks[ti]
			clip := &edit.Timeline.Tracks[ti].Clips[i]
			normalizeGeneratedAsset(clip)
			if clip.UID == "" {
				clip.UID = fmt.Sprintf("clip-%d", i+1)
				out.Changed = true
			}
			if clip.AI == nil {
				continue
			}
			if prepareAIVideoDurationForTiming(edit, clip) {
				out.Changed = true
			}
			continuityOptions := ttsContinuityOptions(track, i)
			changed, pending, err := materializeOneAIAsset(ctx, clip.AI, "clip "+clip.UID, projectID, continuityOptions)
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
			if refreshClipActualDuration(ctx, clip) {
				out.Changed = true
			}
			if syncClipDurationFromAI(clip) {
				out.Changed = true
			}
			if applyClipTiming(edit, clip) {
				out.Changed = true
			}
		}
	}
	if s := edit.Timeline.Soundtrack; s != nil && s.AI != nil {
		changed, pending, err := materializeOneAIAsset(ctx, s.AI, "soundtrack", projectID, nil)
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
			if refreshSoundtrackActualDuration(ctx, s) {
				out.Changed = true
			}
		}
	}
	if applyTimelineTiming(edit) {
		out.Changed = true
	}
	if resolveRelativeClipStarts(edit) {
		out.Changed = true
	}
	if out.Changed && persist {
		b, _ := json.Marshal(edit)
		_, _ = ctx.AppDB().Exec(
			`UPDATE compositions SET edit_json=?, duration_seconds=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`,
			string(b), editDurationSeconds(edit), compositionID,
		)
	}
	return out, nil
}

func applyTimelineTiming(edit *Edit) bool {
	if edit == nil {
		return false
	}
	changed := false
	for ti := range edit.Timeline.Tracks {
		for i := range edit.Timeline.Tracks[ti].Clips {
			if applyClipTiming(edit, &edit.Timeline.Tracks[ti].Clips[i]) {
				changed = true
			}
		}
	}
	return changed
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
			if target != nil && (clip.AfterClipID != "" || clip.GapSeconds > 0 || durationModeReflows(target.DurationMode) || timingReflows(target.Timing)) {
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

func materializeOneAIAsset(ctx *sdk.AppCtx, ai *AIAsset, label, projectID string, contextualOptions map[string]any) (bool, string, error) {
	if ai == nil {
		return false, "", nil
	}
	changed := false
	if ai.CachePolicy == "" {
		ai.CachePolicy = "reuse"
		changed = true
	}
	expectedCacheKey := aiCacheKeyWithOptions(ai, contextualOptions)
	if ai.CacheKey == "" {
		ai.CacheKey = expectedCacheKey
		changed = true
	} else if strings.HasPrefix(ai.CacheKey, "composer:") && ai.CacheKey != expectedCacheKey {
		ai.CacheKey = expectedCacheKey
		ai.StorageID = 0
		ai.GenerationID = 0
		ai.JobID = 0
		ai.Status = "draft"
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
	if ai.Size != "" {
		args["size"] = ai.Size
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
	if refs := aiSourceImages(ai); len(refs) > 0 {
		args["source_images"] = refs
		args["source_image"] = refs[0]
	}
	if opts := mediaGenerateOptions(ai, contextualOptions); len(opts) > 0 {
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
		if msg := mcpErrorText(got); msg != "" {
			ai.Error = msg
			return true, "", fmt.Errorf("%s: media-studio generate failed: %s", label, msg)
		}
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
	if requestID := strings.TrimSpace(strFromAny(meta["provider_request_id"])); requestID != "" {
		ai.ProviderRequestID = requestID
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

func ttsContinuityOptions(track *Track, clipIndex int) map[string]any {
	if track == nil || clipIndex < 0 || clipIndex >= len(track.Clips) {
		return nil
	}
	current := &track.Clips[clipIndex]
	if !isAudioTTS(current.AI) {
		return nil
	}
	if !ttsModelSupportsContinuity(current.AI) {
		return nil
	}
	out := map[string]any{}
	if !hasAIOption(current.AI, "previous_request_ids") {
		if ids := neighboringCompatibleTTSRequestIDs(track, clipIndex, -1); len(ids) > 0 {
			out["previous_request_ids"] = ids
		}
	}
	if _, hasRequestIDs := out["previous_request_ids"]; !hasRequestIDs && !hasAIOption(current.AI, "previous_text") {
		if prev := neighboringCompatibleTTS(track, clipIndex, -1); prev != nil {
			if text := strings.TrimSpace(prev.AI.Prompt); text != "" {
				out["previous_text"] = text
			}
		}
	}
	if !hasAIOption(current.AI, "next_request_ids") {
		if ids := neighboringCompatibleTTSRequestIDs(track, clipIndex, 1); len(ids) > 0 {
			out["next_request_ids"] = ids
		}
	}
	if _, hasRequestIDs := out["next_request_ids"]; !hasRequestIDs && !hasAIOption(current.AI, "next_text") {
		if next := neighboringCompatibleTTS(track, clipIndex, 1); next != nil {
			if text := strings.TrimSpace(next.AI.Prompt); text != "" {
				out["next_text"] = text
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func neighboringCompatibleTTSRequestIDs(track *Track, clipIndex, direction int) []string {
	if track == nil || direction == 0 {
		return nil
	}
	current := &track.Clips[clipIndex]
	ids := []string{}
	for i := clipIndex + direction; i >= 0 && i < len(track.Clips); i += direction {
		candidate := &track.Clips[i]
		if !isAudioTTS(candidate.AI) {
			continue
		}
		if !compatibleTTSContext(current.AI, candidate.AI) {
			break
		}
		if id := strings.TrimSpace(candidate.AI.ProviderRequestID); id != "" {
			ids = append(ids, id)
			if len(ids) == 3 {
				break
			}
		}
	}
	if direction < 0 {
		for i, j := 0, len(ids)-1; i < j; i, j = i+1, j-1 {
			ids[i], ids[j] = ids[j], ids[i]
		}
	}
	return ids
}

func neighboringCompatibleTTS(track *Track, clipIndex, direction int) *Clip {
	if track == nil || direction == 0 {
		return nil
	}
	current := &track.Clips[clipIndex]
	for i := clipIndex + direction; i >= 0 && i < len(track.Clips); i += direction {
		candidate := &track.Clips[i]
		if !isAudioTTS(candidate.AI) {
			continue
		}
		if compatibleTTSContext(current.AI, candidate.AI) {
			return candidate
		}
		return nil
	}
	return nil
}

func isAudioTTS(ai *AIAsset) bool {
	return ai != nil && strings.ToLower(strings.TrimSpace(ai.MediaKind)) == "audio_tts"
}

func compatibleTTSContext(a, b *AIAsset) bool {
	if !isAudioTTS(a) || !isAudioTTS(b) {
		return false
	}
	if !ttsModelSupportsContinuity(a) || !ttsModelSupportsContinuity(b) {
		return false
	}
	if strings.TrimSpace(a.Voice) != strings.TrimSpace(b.Voice) {
		return false
	}
	if effectiveTTSModelID(a) != effectiveTTSModelID(b) {
		return false
	}
	if optionJSONKey(a.Options, "voice_settings") != optionJSONKey(b.Options, "voice_settings") {
		return false
	}
	return true
}

func effectiveTTSModelID(ai *AIAsset) string {
	if ai == nil {
		return ""
	}
	if modelID := strings.TrimSpace(optionString(ai.Options, "model_id")); modelID != "" {
		return modelID
	}
	return strings.TrimSpace(ai.Model)
}

func ttsModelSupportsContinuity(ai *AIAsset) bool {
	model := strings.ToLower(strings.TrimSpace(effectiveTTSModelID(ai)))
	switch model {
	case "eleven_v3":
		return false
	}
	return true
}

func mcpErrorText(got map[string]any) string {
	if len(got) == 0 {
		return ""
	}
	if isErr, _ := got["isError"].(bool); !isErr {
		return ""
	}
	items, _ := got["content"].([]any)
	parts := make([]string, 0, len(items))
	for _, item := range items {
		m, _ := item.(map[string]any)
		if strings.TrimSpace(strFromAny(m["type"])) != "text" {
			continue
		}
		if text := strings.TrimSpace(strFromAny(m["text"])); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n")
}

func strFromAny(v any) string {
	s, _ := v.(string)
	return s
}

func optionString(opts map[string]any, key string) string {
	if len(opts) == 0 {
		return ""
	}
	v, _ := opts[key].(string)
	return strings.TrimSpace(v)
}

func hasAIOption(ai *AIAsset, key string) bool {
	if ai == nil || len(ai.Options) == 0 {
		return false
	}
	_, ok := ai.Options[key]
	return ok
}

func optionJSONKey(opts map[string]any, key string) string {
	if len(opts) == 0 {
		return ""
	}
	v, ok := opts[key]
	if !ok {
		return ""
	}
	b, _ := json.Marshal(v)
	return string(b)
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
	if c.AI.ActualDurationSeconds > 0 && !sameTime(c.ActualLength, c.AI.ActualDurationSeconds) {
		c.ActualLength = c.AI.ActualDurationSeconds
		changed = true
	}
	if c.DurationMode == "" {
		c.DurationMode = defaultDurationMode(c.AI.MediaKind)
		changed = true
	}
	if durationModeFitsGenerated(c.DurationMode) && c.AI.ActualDurationSeconds > 0 && !sameTime(c.Length, c.AI.ActualDurationSeconds) {
		c.Length = c.AI.ActualDurationSeconds
		changed = true
	}
	if c.Length <= 0 && c.EstimatedLength > 0 {
		c.Length = c.EstimatedLength
		changed = true
	}
	return changed
}

func refreshClipActualDuration(ctx *sdk.AppCtx, c *Clip) bool {
	if c == nil || !clipHasProbeableDuration(*c) || strings.TrimSpace(c.Asset.Src) == "" {
		return false
	}
	d := probeAssetDurationSeconds(ctx, c.Asset.Src)
	if d <= 0 {
		return false
	}
	changed := false
	if !sameTime(c.ActualLength, d) {
		c.ActualLength = d
		changed = true
	}
	if c.AI != nil && aiKindHasMediaDuration(c.AI.MediaKind) {
		if !sameTime(c.AI.ActualDurationSeconds, d) {
			c.AI.ActualDurationSeconds = d
			changed = true
		}
		if c.AI.AudioAnalysis == nil {
			c.AI.AudioAnalysis = &AudioAnalysis{}
			changed = true
		}
		if !sameTime(c.AI.AudioAnalysis.DurationSeconds, d) {
			c.AI.AudioAnalysis.DurationSeconds = d
			changed = true
		}
	}
	return changed
}

func refreshSoundtrackActualDuration(ctx *sdk.AppCtx, s *Soundtrack) bool {
	if s == nil || strings.TrimSpace(s.Src) == "" || s.AI == nil || !aiKindHasMediaDuration(s.AI.MediaKind) {
		return false
	}
	d := probeAssetDurationSeconds(ctx, s.Src)
	if d <= 0 {
		return false
	}
	changed := false
	if !sameTime(s.AI.ActualDurationSeconds, d) {
		s.AI.ActualDurationSeconds = d
		changed = true
	}
	if s.AI.AudioAnalysis == nil {
		s.AI.AudioAnalysis = &AudioAnalysis{}
		changed = true
	}
	if !sameTime(s.AI.AudioAnalysis.DurationSeconds, d) {
		s.AI.AudioAnalysis.DurationSeconds = d
		changed = true
	}
	return changed
}

func prepareAIVideoDurationForTiming(edit *Edit, c *Clip) bool {
	if edit == nil || c == nil || c.AI == nil {
		return false
	}
	if strings.ToLower(strings.TrimSpace(c.AI.MediaKind)) != "video" || c.Timing == nil {
		return false
	}
	behavior := strings.ToLower(strings.TrimSpace(c.Timing.Behavior))
	if behavior == "loop" || behavior == "trim_or_loop" {
		return false
	}
	mode := strings.ToLower(strings.TrimSpace(c.Timing.Mode))
	if mode != "fit_source" && mode != "fit_group" && mode != "fit_timeline" {
		return false
	}
	target := targetDurationForTiming(edit, c)
	if target <= 0 {
		return false
	}
	if c.ActualLength >= target || c.AI.ActualDurationSeconds >= target {
		return false
	}
	desired := desiredVideoGenerationSeconds(target)
	if desired <= 0 || c.AI.Duration >= desired {
		return false
	}
	c.AI.Duration = desired
	c.AI.EstimatedDurationSeconds = float64(desired)
	c.AI.CacheKey = ""
	c.AI.StorageID = 0
	c.AI.GenerationID = 0
	c.AI.JobID = 0
	c.AI.Status = "draft"
	c.AI.ActualDurationSeconds = 0
	c.AI.AudioAnalysis = nil
	c.AI.Error = ""
	return true
}

func targetDurationForTiming(edit *Edit, c *Clip) float64 {
	if c == nil || c.Timing == nil {
		return 0
	}
	timing := c.Timing
	mode := strings.ToLower(strings.TrimSpace(timing.Mode))
	var base float64
	switch mode {
	case "fit_source":
		base = sourceDuration(edit, c, timing.Source)
	case "fit_group":
		base = groupDuration(edit, c)
	case "fit_timeline":
		base = editDurationSeconds(edit)
	default:
		return 0
	}
	if base <= 0 {
		return 0
	}
	next := base + timing.PaddingAfter
	if timing.MinLength > 0 && next < timing.MinLength {
		next = timing.MinLength
	}
	if timing.MaxLength > 0 && next > timing.MaxLength {
		next = timing.MaxLength
	}
	return next
}

func desiredVideoGenerationSeconds(target float64) int {
	if target <= 0 {
		return 0
	}
	handle := math.Max(1, target*0.15)
	return int(math.Ceil(target + handle))
}

func clipHasProbeableDuration(c Clip) bool {
	at := strings.ToLower(strings.TrimSpace(c.Asset.Type))
	if at == "audio" || at == "video" {
		return true
	}
	if c.AI != nil && aiKindHasMediaDuration(c.AI.MediaKind) {
		return true
	}
	if c.Timing != nil && strings.TrimSpace(c.Timing.Mode) != "" {
		return true
	}
	return false
}

func applyClipTiming(edit *Edit, c *Clip) bool {
	if c == nil || c.Timing == nil {
		return false
	}
	timing := c.Timing
	mode := strings.ToLower(strings.TrimSpace(timing.Mode))
	if mode == "" || mode == "fixed" {
		return false
	}
	var base float64
	switch mode {
	case "fit_generated":
		base = c.ActualLength
		if base <= 0 && c.AI != nil {
			base = c.AI.ActualDurationSeconds
		}
		if base <= 0 {
			base = c.EstimatedLength
		}
	case "fit_source":
		base = sourceDuration(edit, c, timing.Source)
	case "fit_group":
		base = groupDuration(edit, c)
	case "fit_timeline":
		base = editDurationSeconds(edit)
	default:
		return false
	}
	if base <= 0 {
		return false
	}
	next := base + timing.PaddingAfter
	if timing.MinLength > 0 && next < timing.MinLength {
		next = timing.MinLength
	}
	if timing.MaxLength > 0 && next > timing.MaxLength {
		next = timing.MaxLength
	}
	if next <= 0 || sameTime(c.Length, next) {
		return false
	}
	c.Length = next
	return true
}

func sourceDuration(edit *Edit, current *Clip, source string) float64 {
	source = strings.TrimSpace(source)
	sourceKind := strings.ToLower(source)
	if edit == nil || current == nil {
		return 0
	}
	if sourceKind == "" || sourceKind == "self" {
		return current.ActualLength
	}
	if sourceKind == "section" || sourceKind == "group" {
		return groupDuration(edit, current)
	}
	if strings.HasPrefix(sourceKind, "clip:") || strings.HasPrefix(sourceKind, "audio:") {
		uid := strings.TrimSpace(source[strings.IndexByte(source, ':')+1:])
		if c := findClipByUID(edit, uid); c != nil {
			return clipDuration(*c)
		}
	}
	if sourceKind == "track:audio" {
		for ti := range edit.Timeline.Tracks {
			track := &edit.Timeline.Tracks[ti]
			if trackKind(*track) != "audio" {
				continue
			}
			for i := range track.Clips {
				if track.Clips[i].SectionID != "" && track.Clips[i].SectionID == current.SectionID {
					return clipDuration(track.Clips[i])
				}
				if track.Clips[i].GroupID != "" && track.Clips[i].GroupID == current.GroupID {
					return clipDuration(track.Clips[i])
				}
			}
		}
	}
	return 0
}

func groupDuration(edit *Edit, current *Clip) float64 {
	if edit == nil || current == nil {
		return 0
	}
	sectionID := strings.TrimSpace(current.SectionID)
	groupID := strings.TrimSpace(current.GroupID)
	if sectionID == "" && groupID == "" {
		return 0
	}
	var max float64
	for ti := range edit.Timeline.Tracks {
		for i := range edit.Timeline.Tracks[ti].Clips {
			c := edit.Timeline.Tracks[ti].Clips[i]
			if sectionID != "" && c.SectionID == sectionID {
				max = maxFloat(max, clipDuration(c))
			} else if groupID != "" && c.GroupID == groupID {
				max = maxFloat(max, clipDuration(c))
			}
		}
	}
	return max
}

func findClipByUID(edit *Edit, uid string) *Clip {
	if edit == nil || uid == "" {
		return nil
	}
	for ti := range edit.Timeline.Tracks {
		for i := range edit.Timeline.Tracks[ti].Clips {
			if edit.Timeline.Tracks[ti].Clips[i].UID == uid {
				return &edit.Timeline.Tracks[ti].Clips[i]
			}
		}
	}
	return nil
}

func sameTime(a, b float64) bool {
	if a > b {
		return a-b < 0.01
	}
	return b-a < 0.01
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
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

func timingReflows(t *Timing) bool {
	if t == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(t.Reflow)) {
	case "following", "track", "linked_group", "composition":
		return true
	default:
		return false
	}
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
	return aiCacheKeyWithOptions(ai, nil)
}

func aiCacheKeyWithOptions(ai *AIAsset, contextualOptions map[string]any) string {
	stable := map[string]any{
		"media_kind":    ai.MediaKind,
		"prompt":        ai.Prompt,
		"model":         ai.Model,
		"size":          ai.Size,
		"duration":      ai.Duration,
		"aspect":        ai.Aspect,
		"voice":         ai.Voice,
		"avatar":        ai.Avatar,
		"source_images": aiSourceImages(ai),
		"options":       sortedStableOptions(mergedAIOptions(ai.Options, contextualOptions)),
	}
	b, _ := json.Marshal(stable)
	sum := sha256.Sum256(b)
	return "composer:" + hex.EncodeToString(sum[:16])
}

func aiSourceImages(ai *AIAsset) []string {
	if ai == nil {
		return nil
	}
	out := []string{}
	seen := map[string]bool{}
	add := func(ref string) {
		ref = strings.TrimSpace(ref)
		if ref == "" || seen[ref] {
			return
		}
		seen[ref] = true
		out = append(out, ref)
	}
	for _, ref := range ai.SourceImages {
		add(ref)
	}
	add(ai.SourceImage)
	return out
}

func mediaGenerateOptions(ai *AIAsset, contextualOptions ...map[string]any) map[string]any {
	if ai == nil {
		return nil
	}
	var contextual map[string]any
	if len(contextualOptions) > 0 {
		contextual = contextualOptions[0]
	}
	opts := mergedAIOptions(ai.Options, contextual)
	if ai.EstimatedDurationSeconds > 0 {
		if opts == nil {
			opts = map[string]any{}
		}
		opts["estimated_duration_seconds"] = ai.EstimatedDurationSeconds
	}
	return opts
}

func mergedAIOptions(base, contextual map[string]any) map[string]any {
	out := cloneOptions(base)
	for k, v := range contextual {
		if out == nil {
			out = map[string]any{}
		}
		if _, exists := out[k]; exists {
			continue
		}
		out[k] = v
	}
	return out
}

func sortedStableOptions(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	keys := make([]string, 0, len(in))
	for k := range in {
		if isInternalAIOption(k) {
			continue
		}
		keys = append(keys, k)
	}
	if len(keys) == 0 {
		return nil
	}
	sort.Strings(keys)
	out := make(map[string]any, len(keys))
	for _, k := range keys {
		out[k] = in[k]
	}
	return out
}

func cloneOptions(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func isInternalAIOption(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "estimated_duration_seconds":
		return true
	default:
		return false
	}
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
