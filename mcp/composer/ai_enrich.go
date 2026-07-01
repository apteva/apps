package main

import (
	"encoding/json"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

type mediaHistoryRow struct {
	ID                       int64    `json:"id"`
	Kind                     string   `json:"kind"`
	Prompt                   string   `json:"prompt"`
	Model                    string   `json:"model"`
	Size                     string   `json:"size"`
	DurationMS               int64    `json:"duration_ms"`
	StorageIDs               []int64  `json:"storage_ids"`
	CacheKey                 string   `json:"cache_key"`
	Status                   string   `json:"status"`
	ExtraJSON                string   `json:"extra_json"`
	RequestJSON              string   `json:"request_json"`
	EstimatedDurationSeconds float64  `json:"estimated_duration_seconds"`
	ActualDurationSeconds    float64  `json:"actual_duration_seconds"`
	StorageURLs              []string `json:"storage_urls"`
}

// enrichEditJSONFromMediaStudio restores editable AI metadata for clips that
// were inserted as plain storage:N assets after being generated in Media Studio.
// It is intentionally best-effort: missing bindings/history must not make a
// composition unreadable.
func enrichEditJSONFromMediaStudio(ctx *sdk.AppCtx, editJSON, projectID string) string {
	if strings.TrimSpace(editJSON) == "" {
		return editJSON
	}
	var edit Edit
	if err := json.Unmarshal([]byte(editJSON), &edit); err != nil {
		return editJSON
	}
	needed := storageIDsNeedingAI(&edit)
	if len(needed) == 0 {
		return editJSON
	}
	history, err := mediaHistoryByStorageID(ctx, projectID)
	if err != nil || len(history) == 0 {
		return editJSON
	}
	return enrichEditJSONWithMediaHistory(editJSON, history)
}

func enrichEditJSONWithMediaHistory(editJSON string, history map[int64]*mediaHistoryRow) string {
	if strings.TrimSpace(editJSON) == "" || len(history) == 0 {
		return editJSON
	}
	var edit Edit
	if err := json.Unmarshal([]byte(editJSON), &edit); err != nil {
		return editJSON
	}
	changed := false
	for ti := range edit.Timeline.Tracks {
		for ci := range edit.Timeline.Tracks[ti].Clips {
			clip := &edit.Timeline.Tracks[ti].Clips[ci]
			if clip.AI != nil {
				continue
			}
			storageID := parseStorageID(clip.Asset.Src)
			if storageID <= 0 {
				continue
			}
			row := history[storageID]
			if row == nil {
				continue
			}
			clip.AI = aiFromMediaHistory(*row, storageID)
			if clip.DurationMode == "" {
				clip.DurationMode = defaultDurationModeForKind(clip.AI.MediaKind)
			}
			if clip.EstimatedLength <= 0 && clip.AI.EstimatedDurationSeconds > 0 {
				clip.EstimatedLength = clip.AI.EstimatedDurationSeconds
			}
			if clip.ActualLength <= 0 && clip.AI.ActualDurationSeconds > 0 {
				clip.ActualLength = clip.AI.ActualDurationSeconds
			}
			changed = true
		}
	}
	if edit.Timeline.Soundtrack != nil && edit.Timeline.Soundtrack.AI == nil {
		storageID := parseStorageID(edit.Timeline.Soundtrack.Src)
		if storageID > 0 {
			if row := history[storageID]; row != nil {
				edit.Timeline.Soundtrack.AI = aiFromMediaHistory(*row, storageID)
				changed = true
			}
		}
	}
	if !changed {
		return editJSON
	}
	b, err := json.Marshal(edit)
	if err != nil {
		return editJSON
	}
	return string(b)
}

func editJSONNeedsAIEnrichment(editJSON string) bool {
	if strings.TrimSpace(editJSON) == "" {
		return false
	}
	var edit Edit
	if err := json.Unmarshal([]byte(editJSON), &edit); err != nil {
		return false
	}
	return len(storageIDsNeedingAI(&edit)) > 0
}

func storageIDsNeedingAI(edit *Edit) map[int64]bool {
	out := map[int64]bool{}
	if edit == nil {
		return out
	}
	for _, track := range edit.Timeline.Tracks {
		for _, clip := range track.Clips {
			if clip.AI != nil {
				continue
			}
			if id := parseStorageID(clip.Asset.Src); id > 0 {
				out[id] = true
			}
		}
	}
	if s := edit.Timeline.Soundtrack; s != nil && s.AI == nil {
		if id := parseStorageID(s.Src); id > 0 {
			out[id] = true
		}
	}
	return out
}

func mediaHistoryByStorageID(ctx *sdk.AppCtx, projectID string) (map[int64]*mediaHistoryRow, error) {
	var got struct {
		Generations []mediaHistoryRow `json:"generations"`
	}
	args := map[string]any{"limit": 200, "_project_id": projectID}
	if err := ctx.PlatformAPI().CallAppResult("media-studio", "media_history", args, &got); err != nil {
		return nil, err
	}
	out := map[int64]*mediaHistoryRow{}
	for i := range got.Generations {
		row := &got.Generations[i]
		for _, id := range row.StorageIDs {
			if id > 0 {
				out[id] = row
			}
		}
	}
	return out, nil
}

func aiFromMediaHistory(row mediaHistoryRow, storageID int64) *AIAsset {
	extra := decodeJSONObject(row.ExtraJSON)
	req := decodeJSONObject(row.RequestJSON)
	status := row.Status
	if status == "" {
		status = "ready"
	}
	ai := &AIAsset{
		MediaKind:                row.Kind,
		Prompt:                   row.Prompt,
		Model:                    row.Model,
		CacheKey:                 row.CacheKey,
		CachePolicy:              "reuse",
		Status:                   status,
		GenerationID:             row.ID,
		StorageID:                storageID,
		EstimatedDurationSeconds: row.EstimatedDurationSeconds,
		ActualDurationSeconds:    row.ActualDurationSeconds,
	}
	if ai.Prompt == "" {
		if prompt, _ := req["prompt"].(string); prompt != "" {
			ai.Prompt = prompt
		}
	}
	if ai.Model == "" {
		if model, _ := req["model"].(string); model != "" {
			ai.Model = model
		}
	}
	if size, _ := req["size"].(string); size != "" {
		ai.Size = size
	} else if row.Size != "" {
		ai.Size = row.Size
	}
	if voice, _ := extra["voice"].(string); voice != "" {
		ai.Voice = voice
	} else if voice, _ := req["voice"].(string); voice != "" {
		ai.Voice = voice
	}
	if avatar, _ := extra["avatar"].(string); avatar != "" {
		ai.Avatar = avatar
	} else if avatar, _ := req["avatar"].(string); avatar != "" {
		ai.Avatar = avatar
	}
	if refs := stringSliceFromAny(extra["source_image_refs"]); len(refs) > 0 {
		ai.SourceImages = refs
		ai.SourceImage = refs[0]
	} else if refs := stringSliceFromAny(req["source_images"]); len(refs) > 0 {
		ai.SourceImages = refs
		ai.SourceImage = refs[0]
	} else if source, _ := extra["source_image_ref"].(string); source != "" {
		ai.SourceImage = source
		ai.SourceImages = []string{source}
	} else if source, _ := req["source_image"].(string); source != "" {
		ai.SourceImage = source
		ai.SourceImages = []string{source}
	}
	if aspect, _ := extra["aspect"].(string); aspect != "" {
		ai.Aspect = aspect
	} else if aspect, _ := req["aspect"].(string); aspect != "" {
		ai.Aspect = aspect
	}
	if duration := int(number(req["duration"])); duration > 0 {
		ai.Duration = duration
	} else if row.DurationMS > 0 {
		ai.Duration = int(row.DurationMS / 1000)
	}
	if opts, _ := req["options"].(map[string]any); len(opts) > 0 {
		ai.Options = opts
	}
	return ai
}

func stringSliceFromAny(v any) []string {
	switch t := v.(type) {
	case []string:
		out := make([]string, 0, len(t))
		for _, s := range t {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
		return out
	case []any:
		out := make([]string, 0, len(t))
		for _, item := range t {
			if s := strings.TrimSpace(strFromAny(item)); s != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func decodeJSONObject(raw string) map[string]any {
	var out map[string]any
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&out); err != nil {
		return map[string]any{}
	}
	return out
}

func parseStorageID(src string) int64 {
	s := strings.TrimSpace(src)
	if !strings.HasPrefix(s, "storage:") {
		return 0
	}
	id, err := strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(s, "storage:")), 10, 64)
	if err != nil {
		return 0
	}
	return id
}

func defaultDurationModeForKind(kind string) string {
	switch kind {
	case "audio_tts":
		return "fit_generated_reflow"
	case "avatar":
		return "fit_generated_keep_start"
	default:
		return "fixed_trim_pad"
	}
}
