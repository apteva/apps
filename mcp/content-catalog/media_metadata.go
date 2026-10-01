package main

import (
	"errors"
	sdk "github.com/apteva/app-sdk"
	"strings"
)

type assetMediaMetadata struct {
	FileID               string `json:"file_id"`
	Description          string `json:"description"`
	DescriptionSource    string `json:"description_source"`
	DescriptionUpdatedAt string `json:"description_updated_at"`
	ProbeStatus          string `json:"probe_status"`
	AudienceRating       string `json:"audience_rating"`
	DurationMS           int64  `json:"duration_ms"`
}

func applyAssetMedia(asset *Asset, m assetMediaMetadata) {
	asset.Description, asset.DescriptionSource, asset.DescriptionUpdatedAt = m.Description, m.DescriptionSource, m.DescriptionUpdatedAt
	asset.MediaStatus = m.ProbeStatus
	if asset.MediaStatus == "ok" {
		asset.MediaStatus = "completed"
	}
	if asset.MediaStatus == "" {
		asset.MediaStatus = "unknown"
	}
	asset.MediaRating, asset.DurationMS = m.AudienceRating, m.DurationMS
	asset.MediaError = ""
}

// Live metadata is response-only. Catalog never stores a second description.
// Deduplicate explicit linked IDs and make one Media query per 100 files.
func (a *App) loadAssetMedia(ctx *sdk.AppCtx, assets []*Asset) {
	if len(assets) == 0 {
		return
	}
	byFile := map[string][]*Asset{}
	ids := []string{}
	for _, asset := range assets {
		asset.MediaStatus, asset.MediaRating = "unavailable", ""
		asset.Description, asset.DescriptionSource, asset.DescriptionUpdatedAt = "", "", ""
		asset.DurationMS = 0
		asset.MediaError = ""
		if _, ok := byFile[asset.StorageFileID]; !ok {
			ids = append(ids, asset.StorageFileID)
		}
		byFile[asset.StorageFileID] = append(byFile[asset.StorageFileID], asset)
	}
	if ctx.IntegrationFor("media") == nil {
		return
	}
	pid, err := project(ctx)
	if err != nil {
		return
	}
	for start := 0; start < len(ids); start += 100 {
		end := start + 100
		if end > len(ids) {
			end = len(ids)
		}
		chunk := ids[start:end]
		var result struct {
			Items   []assetMediaMetadata `json:"items"`
			Missing []string             `json:"missing_file_ids"`
		}
		err := ctx.PlatformAPI().CallAppResult("media", "media_get_batch", map[string]any{"_project_id": pid, "file_ids": chunk}, &result)
		received := map[string]assetMediaMetadata{}
		missing := map[string]bool{}
		if err == nil {
			for _, m := range result.Items {
				received[m.FileID] = m
			}
			for _, id := range result.Missing {
				missing[id] = true
			}
		}
		for _, id := range chunk {
			for _, asset := range byFile[id] {
				if err != nil {
					asset.MediaError = err.Error()
				} else if m, ok := received[id]; ok {
					applyAssetMedia(asset, m)
				} else if missing[id] {
					asset.MediaStatus = "missing"
				} else {
					asset.MediaError = "Media batch response omitted file"
				}
			}
		}
	}
}

// Search all eligible linked candidates in bounded pages before applying the
// final result limit. A description match can occur beyond the first SQL page.
func (a *App) searchAssetsLive(ctx *sdk.AppCtx, o searchOptions, cursor searchCursor) (searchPage[assetSearchHit], error) {
	out := searchPage[assetSearchHit]{Items: []assetSearchHit{}}
	candidates := o
	candidates.Query = ""
	if o.Query != "" {
		candidates.Limit = 100
	}
	for {
		page, err := a.searchAssets(ctx.AppDB(), candidates, cursor)
		if err != nil {
			return out, err
		}
		refs := make([]*Asset, len(page.Items))
		for i := range page.Items {
			refs[i] = &page.Items[i].Asset
		}
		a.loadAssetMedia(ctx, refs)
		for _, hit := range page.Items {
			if o.Query != "" && hit.MediaError != "" {
				return out, errors.New("Media descriptions unavailable; retry search: " + hit.MediaError)
			}
			q := strings.ToLower(o.Query)
			if q != "" && !strings.Contains(strings.ToLower(hit.Name), q) && !strings.Contains(strings.ToLower(hit.SessionTitle), q) && !strings.Contains(strings.ToLower(hit.SessionNotes), q) && hit.StorageFileID != o.Query && !strings.Contains(strings.ToLower(hit.Description), q) {
				continue
			}
			out.Items = append(out.Items, hit)
			if len(out.Items) > o.Limit {
				out.Items = out.Items[:o.Limit]
				last := out.Items[len(out.Items)-1]
				date := last.SessionDate
				if o.Sort == "asset_newest" {
					date = last.AttachedAt[:10]
				}
				out.NextCursor = encodeSearchCursor(date, last.AttachedAt, last.ID)
				return out, nil
			}
		}
		if page.NextCursor == "" {
			return out, nil
		}
		if o.Query == "" {
			out.NextCursor = page.NextCursor
			return out, nil
		}
		cursor, err = decodeSearchCursor(page.NextCursor)
		if err != nil {
			return out, err
		}
	}
}
