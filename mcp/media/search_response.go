package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

const (
	mediaSearchDefaultLimit     = 20
	mediaSearchMaxLimit         = 100
	mediaSearchMaxResponseBytes = 64 * 1024
	mediaSearchCursorVersion    = "v1"
)

// MediaSearchRow is deliberately much smaller than MediaResponseRow. Search is
// the discovery step; media_get is the detail step. Keeping source URLs, probe
// internals, descriptions, and every keyframe out of this shape prevents broad
// catalog searches from consuming an agent's context window.
type MediaSearchRow struct {
	FileID     string                `json:"file_id"`
	Filename   string                `json:"filename,omitempty"`
	Title      string                `json:"title,omitempty"`
	MediaType  string                `json:"type"`
	DurationMs int64                 `json:"duration_ms,omitempty"`
	Width      int                   `json:"width,omitempty"`
	Height     int                   `json:"height,omitempty"`
	Folder     string                `json:"folder,omitempty"`
	Thumbnail  *MediaSearchThumbnail `json:"thumbnail,omitempty"`
}

type MediaSearchThumbnail struct {
	StorageFileID string `json:"storage_file_id"`
	URL           string `json:"url,omitempty"`
	Width         int    `json:"width,omitempty"`
	Height        int    `json:"height,omitempty"`
}

func mediaSearchLimit(v any) int {
	limit := int(int64Arg(v))
	if limit <= 0 {
		return mediaSearchDefaultLimit
	}
	if limit > mediaSearchMaxLimit {
		return mediaSearchMaxLimit
	}
	return limit
}

func encodeMediaSearchCursor(offset int) string {
	raw := mediaSearchCursorVersion + ":" + strconv.Itoa(offset)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeMediaSearchCursor(cursor string) (int, error) {
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(cursor))
	if err != nil {
		return 0, errors.New("invalid media_search cursor")
	}
	parts := strings.SplitN(string(raw), ":", 2)
	if len(parts) != 2 || parts[0] != mediaSearchCursorVersion {
		return 0, errors.New("invalid media_search cursor")
	}
	offset, err := strconv.Atoi(parts[1])
	if err != nil || offset < 0 {
		return 0, errors.New("invalid media_search cursor")
	}
	return offset, nil
}

func mediaSearchOffset(args map[string]any) (int, error) {
	offset := int(int64Arg(args["offset"]))
	if offset < 0 {
		return 0, errors.New("offset must be non-negative")
	}
	cursor, _ := args["cursor"].(string)
	if strings.TrimSpace(cursor) == "" {
		return offset, nil
	}
	if offset > 0 {
		return 0, errors.New("provide cursor or offset, not both")
	}
	return decodeMediaSearchCursor(cursor)
}

func mediaSearchType(row MediaRow) string {
	switch {
	case row.IsImage:
		return "image"
	case row.HasVideo:
		return "video"
	case row.HasAudio:
		return "audio"
	default:
		return "unknown"
	}
}

func searchThumbnail(row MediaRow) *DerivationRow {
	for i := range row.Derivations {
		d := &row.Derivations[i]
		if d.Kind == "thumbnail" && d.Status == "ok" && d.StorageFileID != "" {
			return d
		}
	}
	return nil
}

// compactMediaSearchRows resolves only the source files and canonical
// thumbnails. It intentionally does not resolve keyframes or waveforms, which
// are useful in media_get but make broad searches needlessly expensive.
func compactMediaSearchRows(ctx context.Context, projectID string, rows []MediaRow) ([]MediaSearchRow, error) {
	ids := make([]string, 0, len(rows)*2)
	seen := make(map[string]struct{}, len(rows)*2)
	add := func(id string) {
		if id == "" {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	for i := range rows {
		add(rows[i].FileID)
		if d := searchThumbnail(rows[i]); d != nil {
			add(d.StorageFileID)
		}
	}
	files, err := newStorageClient().ResolveFiles(ctx, projectID, ids)
	if err != nil {
		return nil, err
	}
	return projectMediaSearchRows(rows, files), nil
}

func projectMediaSearchRows(rows []MediaRow, files map[string]*StorageFile) []MediaSearchRow {
	out := make([]MediaSearchRow, 0, len(rows))
	for i := range rows {
		row := rows[i]
		item := MediaSearchRow{
			FileID:     row.FileID,
			Filename:   row.Name,
			Title:      row.Title,
			MediaType:  mediaSearchType(row),
			DurationMs: row.DurationMs,
			Width:      row.Width,
			Height:     row.Height,
			Folder:     row.Folder,
		}
		if source := files[row.FileID]; source != nil {
			if source.Name != "" {
				item.Filename = source.Name
			}
			if source.Folder != "" {
				item.Folder = source.Folder
			}
		}
		if d := searchThumbnail(row); d != nil && validateDerivationStorageFile(*d, files[d.StorageFileID]) == nil {
			thumb := &MediaSearchThumbnail{
				StorageFileID: d.StorageFileID,
				Width:         d.Width,
				Height:        d.Height,
			}
			if f := files[d.StorageFileID]; f != nil {
				thumb.URL = f.URL
			}
			item.Thumbnail = thumb
		}
		out = append(out, item)
	}
	return out
}

// fitMediaSearchPage applies the final serialized-size budget. The database
// page limit bounds row count; this second guard bounds unusually large titles,
// filenames, URLs, or detail rows. The cursor advances only by rows actually
// returned, so size truncation cannot silently skip candidates.
func fitMediaSearchPage[T any](items []T, offset int, moreFromDB, storageUnavailable bool) (map[string]any, error) {
	if items == nil {
		items = []T{}
	}
	for n := len(items); n >= 0; n-- {
		if n == 0 && len(items) > 0 {
			break
		}
		hasMore := moreFromDB || n < len(items)
		response := map[string]any{
			"media":    items[:n],
			"returned": n,
			"has_more": hasMore,
		}
		if hasMore {
			response["next_cursor"] = encodeMediaSearchCursor(offset + n)
		}
		if n < len(items) {
			response["response_truncated"] = true
		}
		if storageUnavailable {
			response["storage_unavailable"] = true
		}
		encoded, err := json.Marshal(response)
		if err != nil {
			return nil, err
		}
		if len(encoded) <= mediaSearchMaxResponseBytes {
			return response, nil
		}
	}
	return nil, fmt.Errorf("media_search row exceeds %d-byte response limit; use media_get for that file", mediaSearchMaxResponseBytes)
}
