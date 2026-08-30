package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const (
	bunnyEmbedBaseURL       = "https://iframe.mediadelivery.net/embed"
	bunnyEmbedMaxBodyBytes  = 1024 * 1024
	bunnyEmbedRemoteTimeout = 15 * time.Second
)

var (
	bunnyVideoGUIDPattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	bunnySoft404Pattern   = regexp.MustCompile(`(?is)<(?:title|h1)>\s*404\s*</(?:title|h1)>`)
)

type bunnyDestination struct {
	Provider     string          `json:"provider"`
	SiteID       string          `json:"site_id"`
	LibraryID    json.RawMessage `json:"library_id"`
	VideoGUID    string          `json:"video_guid"`
	Status       string          `json:"status"`
	SourceSHA256 string          `json:"source_sha256"`
	VerifiedAt   string          `json:"verified_at"`
	Title        string          `json:"title"`
	CollectionID string          `json:"collection_id"`
}

type bunnyWorkflowMetadata struct {
	SiteID string `json:"site_id"`
	Bunny  struct {
		Destinations []bunnyDestination `json:"destinations"`
	} `json:"bunny"`
}

type bunnyEmbedRemoteResult struct {
	Verified   bool   `json:"verified"`
	Reason     string `json:"reason,omitempty"`
	HTTPStatus int    `json:"http_status,omitempty"`
}

func (a *App) toolResolveBunnyEmbed(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	fileID, _ := args["file_id"].(string)
	siteID, _ := args["site_id"].(string)
	fileID = strings.TrimSpace(fileID)
	siteID = strings.TrimSpace(siteID)
	if fileID == "" {
		return nil, fmt.Errorf("file_id required")
	}
	if siteID == "" {
		return nil, fmt.Errorf("site_id required")
	}

	row, err := getMedia(ctx.AppDB(), pid, fileID)
	if err != nil {
		if notFound(err) {
			return map[string]any{"found": false, "valid": false, "reason": "not_found", "file_id": fileID, "site_id": siteID}, nil
		}
		return nil, err
	}

	base := map[string]any{
		"found":            true,
		"valid":            false,
		"file_id":          fileID,
		"site_id":          siteID,
		"metadata_version": row.MetadataVersion,
	}
	var metadata bunnyWorkflowMetadata
	if len(bytes.TrimSpace(row.Metadata)) == 0 || bytes.Equal(bytes.TrimSpace(row.Metadata), []byte("null")) {
		base["reason"] = "metadata_missing"
		return base, nil
	}
	if err := json.Unmarshal(row.Metadata, &metadata); err != nil {
		base["reason"] = "metadata_invalid"
		return base, nil
	}
	if metadata.SiteID != siteID {
		base["reason"] = "site_id_mismatch"
		base["metadata_site_id"] = metadata.SiteID
		return base, nil
	}

	matches := make([]bunnyDestination, 0, 1)
	for _, destination := range metadata.Bunny.Destinations {
		if destination.Provider == "bunny_stream" && destination.SiteID == siteID && destination.Status == "ready" {
			matches = append(matches, destination)
		}
	}
	base["destination_count"] = len(matches)
	if len(matches) != 1 {
		if len(matches) == 0 {
			base["reason"] = "ready_destination_missing"
		} else {
			base["reason"] = "ready_destination_ambiguous"
		}
		return base, nil
	}

	destination := matches[0]
	libraryID, ok := positiveJSONInteger(destination.LibraryID)
	if !ok {
		base["reason"] = "library_id_invalid"
		return base, nil
	}
	videoGUID := strings.ToLower(strings.TrimSpace(destination.VideoGUID))
	if !bunnyVideoGUIDPattern.MatchString(videoGUID) {
		base["reason"] = "video_guid_invalid"
		return base, nil
	}
	sourceMatch := destination.SourceSHA256 != "" && row.SourceSHA256 != "" && strings.EqualFold(destination.SourceSHA256, row.SourceSHA256)
	if !sourceMatch {
		base["reason"] = "source_sha256_mismatch"
		base["source_match"] = false
		return base, nil
	}

	embedURL := fmt.Sprintf("%s/%d/%s", bunnyEmbedBaseURL, libraryID, videoGUID)
	base["provider"] = "bunny_stream"
	base["library_id"] = libraryID
	base["video_guid"] = videoGUID
	base["embed_url"] = embedURL
	base["source_match"] = true
	base["verified_at"] = destination.VerifiedAt
	base["title"] = destination.Title
	base["collection_id"] = destination.CollectionID

	remote := verifyBunnyEmbedRemote(context.Background(), embedURL, libraryID, videoGUID)
	base["remote_verified"] = remote.Verified
	base["remote_http_status"] = remote.HTTPStatus
	if !remote.Verified {
		base["reason"] = remote.Reason
		return base, nil
	}
	base["valid"] = true
	return base, nil
}

func positiveJSONInteger(raw json.RawMessage) (int64, bool) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" || strings.ContainsAny(trimmed, ".eE") {
		return 0, false
	}
	value, err := strconv.ParseInt(trimmed, 10, 64)
	if err != nil || value <= 0 {
		return 0, false
	}
	return value, true
}

func verifyBunnyEmbedRemote(ctx context.Context, embedURL string, libraryID int64, videoGUID string) bunnyEmbedRemoteResult {
	verifyCtx, cancel := context.WithTimeout(ctx, bunnyEmbedRemoteTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(verifyCtx, http.MethodGet, embedURL, nil)
	if err != nil {
		return bunnyEmbedRemoteResult{Reason: "remote_request_invalid"}
	}
	req.Header.Set("User-Agent", "Apteva-Media-Bunny-Resolver/1.0")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return bunnyEmbedRemoteResult{Reason: "remote_unavailable"}
	}
	defer resp.Body.Close()
	result := bunnyEmbedRemoteResult{HTTPStatus: resp.StatusCode}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		result.Reason = "remote_http_error"
		return result
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, bunnyEmbedMaxBodyBytes+1))
	if err != nil {
		result.Reason = "remote_read_failed"
		return result
	}
	if len(body) > bunnyEmbedMaxBodyBytes {
		result.Reason = "remote_response_too_large"
		return result
	}
	lower := strings.ToLower(string(body))
	if bunnySoft404Pattern.Match(body) {
		result.Reason = "remote_soft_404"
		return result
	}
	if headerLibrary := strings.TrimSpace(resp.Header.Get("cdn-videolibraryid")); headerLibrary != "" && headerLibrary != strconv.FormatInt(libraryID, 10) {
		result.Reason = "remote_library_id_mismatch"
		return result
	}
	if !strings.Contains(lower, strings.ToLower(videoGUID)) {
		result.Reason = "remote_video_guid_missing"
		return result
	}
	if !strings.Contains(lower, "og:video") && !strings.Contains(lower, "<video") {
		result.Reason = "remote_player_metadata_missing"
		return result
	}
	result.Verified = true
	return result
}
