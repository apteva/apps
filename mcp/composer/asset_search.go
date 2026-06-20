package main

import (
	"encoding/json"
	"fmt"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

type storageListFile struct {
	ID          int64    `json:"id"`
	Name        string   `json:"name"`
	Folder      string   `json:"folder"`
	ContentType string   `json:"content_type"`
	SizeBytes   int64    `json:"size_bytes"`
	Source      string   `json:"source"`
	Tags        []string `json:"tags"`
	CreatedAt   string   `json:"created_at"`
	UpdatedAt   string   `json:"updated_at"`
}

func (a *App) toolAssetSearch(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	folder := strArg(args, "folder", "/")
	recursive := boolArg(args, "recursive", true)
	limit := intArg(args, "limit", 50)
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	kind := strings.ToLower(strings.TrimSpace(strArg(args, "kind", "")))
	q := strings.ToLower(strings.TrimSpace(strArg(args, "q", "")))
	tags := cleanStringList(strArrayArg(args, "tags"))
	inspect := boolArg(args, "inspect", false)

	var got struct {
		Files []storageListFile `json:"files"`
	}
	callLimit := limit
	if q != "" || kind != "" || len(tags) > 0 {
		callLimit = 500
	}
	err := ctx.PlatformAPI().CallAppResult("storage", "files_list", map[string]any{
		"folder":      folder,
		"recursive":   recursive,
		"limit":       callLimit,
		"_project_id": projectScopeFromArgs(ctx, args),
	}, &got)
	if err != nil {
		return nil, fmt.Errorf("storage files_list: %w", err)
	}

	assets := []map[string]any{}
	for _, file := range got.Files {
		if !assetMatchesSearch(file, q, kind, tags) {
			continue
		}
		row := map[string]any{
			"id":           file.ID,
			"src":          fmt.Sprintf("storage:%d", file.ID),
			"name":         file.Name,
			"folder":       file.Folder,
			"kind":         kindFromStorageFile(file),
			"content_type": file.ContentType,
			"size_bytes":   file.SizeBytes,
			"source":       file.Source,
			"tags":         file.Tags,
			"created_at":   file.CreatedAt,
			"updated_at":   file.UpdatedAt,
		}
		if inspect {
			if meta, err := a.toolAssetInspect(ctx, map[string]any{"src": row["src"]}); err == nil {
				if d := probeDurationSeconds(meta); d > 0 {
					row["duration_seconds"] = d
				}
				row["probe"] = meta
			}
		}
		assets = append(assets, row)
		if len(assets) >= limit {
			break
		}
	}
	return map[string]any{"assets": assets}, nil
}

func assetMatchesSearch(file storageListFile, q, kind string, tags []string) bool {
	if kind != "" && kindFromStorageFile(file) != kind {
		return false
	}
	if q != "" {
		haystack := strings.ToLower(file.Name + " " + file.Folder + " " + file.ContentType + " " + file.Source + " " + strings.Join(file.Tags, " "))
		if !strings.Contains(haystack, q) {
			return false
		}
	}
	if len(tags) > 0 {
		have := map[string]bool{}
		for _, tag := range file.Tags {
			have[strings.ToLower(strings.TrimSpace(tag))] = true
		}
		for _, tag := range tags {
			if !have[tag] {
				return false
			}
		}
	}
	return true
}

func kindFromStorageFile(file storageListFile) string {
	ct := strings.ToLower(file.ContentType)
	name := strings.ToLower(file.Name)
	switch {
	case strings.HasPrefix(ct, "audio/") || strings.HasSuffix(name, ".mp3") || strings.HasSuffix(name, ".wav") || strings.HasSuffix(name, ".m4a") || strings.HasSuffix(name, ".aac") || strings.HasSuffix(name, ".flac"):
		return "audio"
	case strings.HasPrefix(ct, "image/") || strings.HasSuffix(name, ".png") || strings.HasSuffix(name, ".jpg") || strings.HasSuffix(name, ".jpeg") || strings.HasSuffix(name, ".webp") || strings.HasSuffix(name, ".gif"):
		return "image"
	default:
		return "video"
	}
}

func probeDurationSeconds(v any) float64 {
	b, _ := json.Marshal(v)
	var probe struct {
		Format struct {
			Duration json.Number `json:"duration"`
		} `json:"format"`
	}
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.UseNumber()
	if err := dec.Decode(&probe); err != nil {
		return 0
	}
	f, _ := probe.Format.Duration.Float64()
	return f
}

func cleanStringList(in []string) []string {
	out := []string{}
	for _, item := range in {
		s := strings.ToLower(strings.TrimSpace(item))
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}
