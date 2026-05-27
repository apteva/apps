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
	if len(edit.Timeline.Tracks) > 0 {
		for i := range edit.Timeline.Tracks[0].Clips {
			clip := &edit.Timeline.Tracks[0].Clips[i]
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
		}
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
		ai.Error = ""
		return true, fmt.Sprintf("%s queued as media-studio job #%d", label, ai.JobID), nil
	}
	if id := number(meta["generation_id"]); id > 0 {
		ai.GenerationID = id
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
