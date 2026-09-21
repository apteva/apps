package main

import (
	"context"
	"crypto/sha256"
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
	mediaSearchFullDefaultLimit = 5
	mediaSearchFullMaxLimit     = 10
	mediaSearchMaxResponseBytes = 64 * 1024
	mediaSearchCursorVersion    = "v2"
)

// MediaSearchRow is deliberately much smaller than MediaResponseRow. Search is
// the discovery step; media_get is the detail step. Keeping source URLs, probe
// internals, descriptions, and every keyframe out of this shape prevents broad
// catalog searches from consuming an agent's context window.
type MediaSearchRow struct {
	FileID                     string                `json:"file_id"`
	Filename                   string                `json:"filename,omitempty"`
	Title                      string                `json:"title,omitempty"`
	MediaType                  string                `json:"type"`
	DurationMs                 int64                 `json:"duration_ms,omitempty"`
	Width                      int                   `json:"width,omitempty"`
	Height                     int                   `json:"height,omitempty"`
	Folder                     string                `json:"folder,omitempty"`
	Thumbnail                  *MediaSearchThumbnail `json:"thumbnail,omitempty"`
	Site                       string                `json:"site,omitempty"`
	Channel                    string                `json:"channel,omitempty"`
	RecordingDate              string                `json:"recording_date,omitempty"`
	AudienceRating             string                `json:"audience_rating,omitempty"`
	CreatedAt                  string                `json:"created_at,omitempty"`
	UpdatedAt                  string                `json:"updated_at,omitempty"`
	ProbeStatus                string                `json:"probe_status,omitempty"`
	TranscriptStatus           string                `json:"transcript_status,omitempty"`
	BasicReady                 bool                  `json:"basic_ready"`
	RequiredDerivativesPresent bool                  `json:"required_derivatives_present"`
}

type MediaSearchThumbnail struct {
	StorageFileID string `json:"storage_file_id"`
	URL           string `json:"url,omitempty"`
	Width         int    `json:"width,omitempty"`
	Height        int    `json:"height,omitempty"`
}

func resolveMediaSearchFolderScope(args map[string]any, folder string) (string, error) {
	var scope string
	scopeProvided := false
	if raw, exists := args["folder_scope"]; exists {
		var ok bool
		scope, ok = raw.(string)
		if !ok {
			return "", errors.New("folder_scope must be a string")
		}
		scopeProvided = true
	}
	scope = strings.ToLower(strings.TrimSpace(scope))
	if scopeProvided && scope != folderScopeExact && scope != folderScopeSubtree {
		return "", errors.New("folder_scope must be exact or subtree")
	}

	var recursive bool
	recursiveProvided := false
	if raw, exists := args["recursive"]; exists {
		var ok bool
		recursive, ok = boolArg(raw)
		if !ok {
			return "", errors.New("recursive must be a boolean")
		}
		recursiveProvided = true
	}

	if scopeProvided && recursiveProvided && (scope == folderScopeSubtree) != recursive {
		return "", errors.New("folder_scope conflicts with recursive")
	}
	if scopeProvided {
		if folder == "" {
			return "", errors.New("folder_scope requires folder")
		}
		return scope, nil
	}
	if recursiveProvided && recursive {
		return folderScopeSubtree, nil
	}
	return folderScopeExact, nil
}

func mediaSearchQueryEcho(f SearchFilters) map[string]any {
	query := map[string]any{}
	putString := func(name, value string) {
		if value = strings.TrimSpace(value); value != "" {
			query[name] = value
		}
	}
	putString("q", f.Q)
	putString("filename", f.Filename)
	putString("title", f.Title)
	if f.Folder != "" {
		query["folder"] = f.Folder
		query["folder_scope"] = f.effectiveFolderScope()
	}
	putString("media_type", f.MediaType)
	putString("aspect", f.Aspect)
	if f.DurationMinMs > 0 {
		query["duration_min_ms"] = f.DurationMinMs
	}
	if f.DurationMaxMs > 0 {
		query["duration_max_ms"] = f.DurationMaxMs
	}
	if f.HasVideo != nil {
		query["has_video"] = *f.HasVideo
	}
	if f.HasAudio != nil {
		query["has_audio"] = *f.HasAudio
	}
	if f.IsImage != nil {
		query["is_image"] = *f.IsImage
	}
	if f.WidthMin > 0 {
		query["width_min"] = f.WidthMin
	}
	if f.WidthMax > 0 {
		query["width_max"] = f.WidthMax
	}
	putString("video_codec", f.VideoCodec)
	putString("audio_codec", f.AudioCodec)
	if len(f.AudienceRatingIn) > 0 {
		query["audience_rating"] = append([]string(nil), f.AudienceRatingIn...)
	}
	if len(f.AudienceRatingNotIn) > 0 {
		query["exclude_audience_rating"] = append([]string(nil), f.AudienceRatingNotIn...)
	}
	if filters := metadataConditionsEcho(f.MetadataFilters); len(filters) > 0 {
		query["metadata_filters"] = filters
	}
	putString("order_by", f.OrderBy)
	if normalizedMediaSearchDirection(f.SortDirection) == "ASC" {
		query["sort_direction"] = "asc"
	}
	return query
}

func mediaSearchResponseMetadata(f SearchFilters, diagnostic *MediaSearchEmptyDiagnostic) map[string]any {
	metadata := map[string]any{"query": mediaSearchQueryEcho(f)}
	if f.Folder != "" {
		metadata["folder_scope"] = f.effectiveFolderScope()
	}
	if diagnostic == nil {
		return metadata
	}
	metadata["empty_reason"] = diagnostic.EmptyReason
	metadata["has_matching_descendants"] = diagnostic.HasMatchingDescendants
	metadata["descendant_match_count"] = diagnostic.DescendantMatchCount
	metadata["sample_matching_folders"] = diagnostic.SampleMatchingFolders
	if diagnostic.HasMatchingDescendants {
		metadata["retry_recommended"] = map[string]any{
			"folder":       f.Folder,
			"folder_scope": folderScopeSubtree,
		}
	}
	return metadata
}

func mediaSearchLimit(v any, detailLevel ...string) (int, error) {
	full := len(detailLevel) > 0 && detailLevel[0] == "full"
	limit := int(int64Arg(v))
	if limit <= 0 {
		if full {
			return mediaSearchFullDefaultLimit, nil
		}
		return mediaSearchDefaultLimit, nil
	}
	if full && limit > mediaSearchFullMaxLimit {
		return 0, fmt.Errorf("full-detail media_search supports at most %d records; use compact/planning search and media_get for selected file IDs", mediaSearchFullMaxLimit)
	}
	if limit > mediaSearchMaxLimit {
		return mediaSearchMaxLimit, nil
	}
	return limit, nil
}

type MediaSearchBoundary struct {
	TextValue   string `json:"t,omitempty"`
	NumberValue int64  `json:"n,omitempty"`
	FileID      string `json:"id"`
}

type MediaSearchCursor struct {
	Version     string `json:"v"`
	Fingerprint string `json:"f"`
	OrderBy     string `json:"o"`
	Direction   string `json:"d"`
	MediaSearchBoundary
	Snapshot MediaSearchBoundary `json:"snapshot"`
	Seen     int                 `json:"s"`
	Total    int                 `json:"total"`
}

func mediaSearchFingerprint(f SearchFilters) string {
	payload, _ := json.Marshal(mediaSearchQueryEcho(f))
	digest := sha256.Sum256(payload)
	return base64.RawURLEncoding.EncodeToString(digest[:12])
}

func mediaSearchSortValue(row MediaRow, f SearchFilters) (string, int64) {
	switch normalizedMediaSearchOrder(f).name {
	case "duration_ms":
		return "", row.DurationMs
	case "updated_at":
		return row.UpdatedAt, 0
	case "recording_date":
		return mediaMetadataString(row.Metadata, "recording_date"), 0
	case "session_date":
		value := mediaMetadataString(row.Metadata, "session", "date")
		if value == "" {
			value = mediaMetadataString(row.Metadata, "session_date")
		}
		return value, 0
	case "hosting_readiness":
		if mediaHostingSummary(row.Metadata).Ready {
			return "", 1
		}
		return "", 0
	case "patreon_status":
		return "", mediaPatreonStatusPriority(mediaMetadataString(row.Metadata, "patreon", "status"))
	case "audience_rating":
		return "", mediaAudienceRatingPriority(row.AudienceRating)
	default:
		return row.CreatedAt, 0
	}
}

func mediaSearchBoundary(row MediaRow, f SearchFilters) MediaSearchBoundary {
	textValue, numberValue := mediaSearchSortValue(row, f)
	return MediaSearchBoundary{TextValue: textValue, NumberValue: numberValue, FileID: row.FileID}
}

func encodeMediaSearchCursor(row MediaRow, f SearchFilters, snapshot MediaSearchBoundary, seen, total int) string {
	order := normalizedMediaSearchOrder(f)
	cursor := MediaSearchCursor{
		Version: mediaSearchCursorVersion, Fingerprint: mediaSearchFingerprint(f),
		OrderBy: order.name, Direction: strings.ToLower(normalizedMediaSearchDirection(f.SortDirection)),
		MediaSearchBoundary: mediaSearchBoundary(row, f), Snapshot: snapshot, Seen: seen, Total: total,
	}
	raw, _ := json.Marshal(cursor)
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeMediaSearchCursor(cursor string, f SearchFilters) (*MediaSearchCursor, int, error) {
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(cursor))
	if err != nil {
		return nil, 0, errors.New("invalid media_search cursor")
	}
	// v1 was an encoded offset. Accept it for one release so callers can
	// finish an in-flight traversal, while all newly emitted cursors are stable
	// filter-bound keysets.
	if strings.HasPrefix(string(raw), "v1:") {
		parts := strings.SplitN(string(raw), ":", 2)
		offset, convErr := strconv.Atoi(parts[1])
		if convErr != nil || offset < 0 {
			return nil, 0, errors.New("invalid media_search cursor")
		}
		return nil, offset, nil
	}
	var decoded MediaSearchCursor
	if err := json.Unmarshal(raw, &decoded); err != nil || decoded.Version != mediaSearchCursorVersion || decoded.FileID == "" || decoded.Snapshot.FileID == "" || decoded.Seen < 0 || decoded.Total < decoded.Seen {
		return nil, 0, errors.New("invalid media_search cursor")
	}
	order := normalizedMediaSearchOrder(f)
	direction := strings.ToLower(normalizedMediaSearchDirection(f.SortDirection))
	if decoded.Fingerprint != mediaSearchFingerprint(f) || decoded.OrderBy != order.name || decoded.Direction != direction {
		return nil, 0, errors.New("media_search cursor does not match the current filters or sorting")
	}
	return &decoded, 0, nil
}

func mediaSearchPagination(args map[string]any, f SearchFilters) (*MediaSearchCursor, int, int, error) {
	offset := int(int64Arg(args["offset"]))
	if offset < 0 {
		return nil, 0, 0, errors.New("offset must be non-negative")
	}
	cursor, _ := args["cursor"].(string)
	if strings.TrimSpace(cursor) == "" {
		return nil, offset, offset, nil
	}
	if offset > 0 {
		return nil, 0, 0, errors.New("provide cursor or offset, not both")
	}
	decoded, legacyOffset, err := decodeMediaSearchCursor(cursor, f)
	if err != nil {
		return nil, 0, 0, err
	}
	if decoded == nil {
		return nil, legacyOffset, legacyOffset, nil
	}
	return decoded, 0, decoded.Seen, nil
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
			FileID:           row.FileID,
			Filename:         row.Name,
			Title:            row.Title,
			MediaType:        mediaSearchType(row),
			DurationMs:       row.DurationMs,
			Width:            row.Width,
			Height:           row.Height,
			Folder:           row.Folder,
			Site:             firstNonEmpty(mediaMetadataString(row.Metadata, "site"), mediaMetadataString(row.Metadata, "site_id")),
			Channel:          firstNonEmpty(mediaMetadataString(row.Metadata, "channel"), mediaMetadataString(row.Metadata, "channel_id")),
			RecordingDate:    mediaMetadataString(row.Metadata, "recording_date"),
			AudienceRating:   row.AudienceRating,
			CreatedAt:        row.CreatedAt,
			UpdatedAt:        row.UpdatedAt,
			ProbeStatus:      row.ProbeStatus,
			TranscriptStatus: row.TranscriptStatus,
		}
		item.RequiredDerivativesPresent = mediaRequiredDerivativesPresent(row)
		item.BasicReady = row.ProbeStatus == "ok" && item.RequiredDerivativesPresent
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
func fitMediaSearchPage[T any](items []T, cursors []string, seenBefore, total int, moreFromDB, storageUnavailable bool, metadata ...map[string]any) (map[string]any, error) {
	if items == nil {
		items = []T{}
	}
	for n := len(items); n >= 0; n-- {
		if n == 0 && len(items) > 0 {
			break
		}
		hasMore := moreFromDB || n < len(items)
		seen := seenBefore + n
		remaining := total - seen
		if remaining < 0 {
			remaining = 0
		}
		response := map[string]any{
			"media":               items[:n],
			"returned":            n,
			"has_more":            hasMore,
			"incomplete":          hasMore,
			"must_continue":       hasMore,
			"complete":            !hasMore,
			"estimated_remaining": remaining,
			"estimated_total":     total,
		}
		if len(metadata) > 0 {
			for key, value := range metadata[0] {
				response[key] = value
			}
		}
		if hasMore {
			if n == 0 || n > len(cursors) {
				return nil, errors.New("media_search cannot continue without a row cursor")
			}
			response["next_cursor"] = cursors[n-1]
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
