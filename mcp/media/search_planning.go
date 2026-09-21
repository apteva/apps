package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const maxMediaSearchProjectionFields = 32

type MediaChannelStatus struct {
	Status string `json:"status,omitempty"`
}

type MediaHostingStatus struct {
	Status string `json:"status,omitempty"`
	Ready  bool   `json:"ready"`
}

type MediaLineageSummary struct {
	SessionID    string `json:"session_id,omitempty"`
	PackageID    string `json:"package_id,omitempty"`
	ParentFileID string `json:"parent_file_id,omitempty"`
	Role         string `json:"role,omitempty"`
	Exact        bool   `json:"exact"`
}

type MediaEssentialDerivatives struct {
	Thumbnail bool `json:"thumbnail"`
	Waveform  bool `json:"waveform"`
	Cover     bool `json:"cover"`
	Required  bool `json:"required_present"`
}

type MediaReleaseReadiness struct {
	MediaReady                 bool   `json:"media_ready"`
	ProbeReady                 bool   `json:"probe_ready"`
	PatreonPublicationStatus   string `json:"patreon_publication_status,omitempty"`
	SocialUsageStatus          string `json:"social_usage_status,omitempty"`
	HostingReady               bool   `json:"hosting_ready"`
	HostingStatus              string `json:"hosting_status,omitempty"`
	PublicAudienceSuitability  string `json:"public_audience_suitability"`
	RequiredDerivativesPresent bool   `json:"required_derivatives_present"`
	ExactLineage               bool   `json:"exact_lineage"`
}

type MediaPlanningSearchRow struct {
	MediaSearchRow
	Patreon              MediaChannelStatus        `json:"patreon"`
	Social               MediaChannelStatus        `json:"social"`
	Hosting              MediaHostingStatus        `json:"hosting"`
	Lineage              MediaLineageSummary       `json:"lineage"`
	PublicationState     string                    `json:"publication_state,omitempty"`
	EssentialDerivatives MediaEssentialDerivatives `json:"essential_derivatives"`
	Readiness            MediaReleaseReadiness     `json:"readiness"`
}

type mediaSearchContract struct {
	DetailLevel    string
	Fields         []string
	Expand         map[string]bool
	ResolveStorage bool
}

func parseMediaSearchContract(args map[string]any) (mediaSearchContract, error) {
	contract := mediaSearchContract{DetailLevel: "compact", Expand: map[string]bool{}}
	if raw, exists := args["detail_level"]; exists {
		level, ok := raw.(string)
		if !ok {
			return contract, errors.New("detail_level must be compact, planning, or full")
		}
		contract.DetailLevel = strings.ToLower(strings.TrimSpace(level))
	}
	if contract.DetailLevel != "compact" && contract.DetailLevel != "planning" && contract.DetailLevel != "full" {
		return contract, errors.New("detail_level must be compact, planning, or full")
	}
	if raw, exists := args["detail"]; exists {
		legacy, ok := boolArg(raw)
		if !ok {
			return contract, errors.New("detail must be a boolean")
		}
		legacyLevel := "compact"
		if legacy {
			legacyLevel = "full"
		}
		if _, hasLevel := args["detail_level"]; hasLevel && contract.DetailLevel != legacyLevel {
			return contract, errors.New("detail conflicts with detail_level")
		}
		contract.DetailLevel = legacyLevel
	}
	includeRawProbe, ok := boolArg(args["include_raw_probe"])
	if _, exists := args["include_raw_probe"]; exists && !ok {
		return contract, errors.New("include_raw_probe must be a boolean")
	}
	if includeRawProbe {
		contract.DetailLevel = "full"
		contract.Expand["raw_probe"] = true
	}
	expand, err := stringListArg(args["expand"], "expand", 4)
	if err != nil {
		return contract, err
	}
	for _, item := range expand {
		switch item {
		case "raw_probe", "derivations", "keyframes", "urls":
			contract.Expand[item] = true
		default:
			return contract, fmt.Errorf("unsupported media_search expansion %q", item)
		}
	}
	if contract.DetailLevel != "full" && (contract.Expand["raw_probe"] || contract.Expand["derivations"] || contract.Expand["keyframes"] || contract.Expand["urls"]) {
		return contract, errors.New("media_search expansions require detail_level=full")
	}
	contract.Fields, err = parseMediaSearchFields(args["fields"])
	if err != nil {
		return contract, err
	}
	fullOnly := map[string]bool{
		"url": true, "visibility": true, "size_bytes": true, "content_type": true,
		"metadata": true, "metadata_version": true, "description": true, "alt_text": true,
		"format_name": true, "bitrate": true, "video_codec": true, "audio_codec": true,
		"fps": true, "channels": true, "sample_rate": true, "has_video": true,
		"has_audio": true, "is_image": true, "derivations": true, "raw_probe": true,
	}
	for _, field := range contract.Fields {
		if fullOnly[field] && contract.DetailLevel != "full" {
			return contract, fmt.Errorf("media_search field %q requires detail_level=full", field)
		}
		if field == "raw_probe" && !contract.Expand["raw_probe"] {
			return contract, errors.New("raw_probe requires expand=['raw_probe']")
		}
	}
	contract.ResolveStorage = mediaSearchNeedsStorage(contract)
	return contract, nil
}

func stringListArg(value any, name string, max int) ([]string, error) {
	if value == nil {
		return nil, nil
	}
	var raw []any
	switch values := value.(type) {
	case []any:
		raw = values
	case []string:
		raw = make([]any, len(values))
		for i := range values {
			raw[i] = values[i]
		}
	default:
		return nil, fmt.Errorf("%s must be an array of strings", name)
	}
	if len(raw) > max {
		return nil, fmt.Errorf("%s supports at most %d values", name, max)
	}
	out := make([]string, 0, len(raw))
	seen := map[string]bool{}
	for _, value := range raw {
		item, ok := value.(string)
		if !ok || strings.TrimSpace(item) == "" {
			return nil, fmt.Errorf("%s must contain non-empty strings", name)
		}
		item = strings.TrimSpace(item)
		if !seen[item] {
			seen[item] = true
			out = append(out, item)
		}
	}
	return out, nil
}

var mediaSearchProjectionFields = map[string]bool{
	"id": true, "file_id": true, "filename": true, "name": true, "title": true, "type": true,
	"duration_ms": true, "width": true, "height": true, "folder": true, "thumbnail": true,
	"site": true, "channel": true, "recording_date": true, "rating": true, "audience_rating": true,
	"created_at": true, "updated_at": true, "probe_status": true, "basic_ready": true,
	"required_derivatives_present": true, "patreon.status": true, "social.status": true,
	"hosting": true, "lineage": true, "publication_state": true, "readiness": true,
	"essential_derivatives": true, "url": true, "visibility": true, "size_bytes": true,
	"content_type": true, "metadata": true, "metadata_version": true, "transcript_status": true,
	"description": true, "alt_text": true, "format_name": true, "bitrate": true,
	"video_codec": true, "audio_codec": true, "fps": true, "channels": true,
	"sample_rate": true, "has_video": true, "has_audio": true, "is_image": true,
	"derivations": true, "raw_probe": true,
}

func parseMediaSearchFields(value any) ([]string, error) {
	fields, err := stringListArg(value, "fields", maxMediaSearchProjectionFields)
	if err != nil {
		return nil, err
	}
	for _, field := range fields {
		if !mediaSearchProjectionFields[field] {
			return nil, fmt.Errorf("unsupported media_search field %q", field)
		}
	}
	return fields, nil
}

func mediaSearchNeedsStorage(contract mediaSearchContract) bool {
	if len(contract.Fields) == 0 {
		return contract.DetailLevel == "compact" || contract.DetailLevel == "full"
	}
	for _, field := range contract.Fields {
		switch field {
		case "filename", "name", "folder", "thumbnail", "url", "visibility", "size_bytes", "content_type", "derivations":
			return true
		}
	}
	return false
}

func mediaSearchNeedsDerivations(contract mediaSearchContract) string {
	if contract.Expand["derivations"] || contract.Expand["keyframes"] {
		return "all"
	}
	if len(contract.Fields) == 0 {
		return "essential"
	}
	for _, field := range contract.Fields {
		switch field {
		case "thumbnail", "basic_ready", "required_derivatives_present", "readiness", "essential_derivatives", "derivations":
			return "essential"
		}
	}
	return "none"
}

func mediaSearchNeedsMetadata(contract mediaSearchContract) bool {
	if len(contract.Fields) == 0 {
		return true
	}
	for _, field := range contract.Fields {
		switch field {
		case "site", "channel", "recording_date", "patreon.status", "social.status", "hosting", "lineage", "publication_state", "readiness", "metadata":
			return true
		}
	}
	return false
}

func mediaMetadataObject(raw json.RawMessage) map[string]any {
	if len(raw) == 0 {
		return map[string]any{}
	}
	var out map[string]any
	if json.Unmarshal(raw, &out) != nil || out == nil {
		return map[string]any{}
	}
	return out
}

func mediaMetadataValue(raw json.RawMessage, path ...string) any {
	var current any = mediaMetadataObject(raw)
	for _, segment := range path {
		object, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current = object[segment]
	}
	return current
}

func mediaMetadataString(raw json.RawMessage, path ...string) string {
	value := mediaMetadataValue(raw, path...)
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case bool, float64, json.Number:
		return fmt.Sprint(typed)
	default:
		return ""
	}
}

func mediaMetadataBool(raw json.RawMessage, path ...string) (bool, bool) {
	value := mediaMetadataValue(raw, path...)
	switch typed := value.(type) {
	case bool:
		return typed, true
	case float64:
		return typed != 0, true
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "true", "yes", "1", "ready", "hosted", "published", "complete", "completed", "available":
			return true, true
		case "false", "no", "0", "pending", "missing", "failed", "unavailable":
			return false, true
		}
	}
	return false, false
}

func mediaPatreonStatus(raw json.RawMessage) MediaChannelStatus {
	return MediaChannelStatus{Status: mediaMetadataString(raw, "patreon", "status")}
}

func mediaSocialStatus(raw json.RawMessage) MediaChannelStatus {
	status := mediaMetadataString(raw, "social", "status")
	if status == "" {
		status = mediaMetadataString(raw, "social", "usage_status")
	}
	return MediaChannelStatus{Status: status}
}

func mediaHostingSummary(raw json.RawMessage) MediaHostingStatus {
	status := mediaMetadataString(raw, "hosting", "status")
	ready, known := mediaMetadataBool(raw, "hosting", "ready")
	if !known {
		ready, known = mediaMetadataBool(raw, "hosting")
	}
	if !known {
		ready, _ = mediaMetadataBool(raw, "hosting", "status")
	}
	return MediaHostingStatus{Status: status, Ready: ready}
}

func mediaLineageSummary(raw json.RawMessage) MediaLineageSummary {
	sessionID := firstNonEmpty(mediaMetadataString(raw, "lineage", "session_id"), mediaMetadataString(raw, "session", "id"), mediaMetadataString(raw, "session_id"))
	packageID := firstNonEmpty(mediaMetadataString(raw, "lineage", "package_id"), mediaMetadataString(raw, "package", "id"), mediaMetadataString(raw, "package_id"))
	lineage := MediaLineageSummary{
		SessionID: sessionID, PackageID: packageID,
		ParentFileID: firstNonEmpty(mediaMetadataString(raw, "lineage", "parent_file_id"), mediaMetadataString(raw, "parent_file_id")),
		Role:         firstNonEmpty(mediaMetadataString(raw, "lineage", "role"), mediaMetadataString(raw, "release_role")),
	}
	lineage.Exact = lineage.SessionID != "" && lineage.PackageID != ""
	return lineage
}

func mediaEssentialDerivatives(row MediaRow) MediaEssentialDerivatives {
	var result MediaEssentialDerivatives
	for _, derivation := range row.Derivations {
		if derivation.Status != "ok" || derivation.StorageFileID == "" {
			continue
		}
		switch derivation.Kind {
		case "thumbnail":
			result.Thumbnail = true
		case "waveform":
			result.Waveform = true
		case "cover":
			result.Cover = true
		}
	}
	result.Required = mediaRequiredDerivativesPresent(row)
	return result
}

func mediaRequiredDerivativesPresent(row MediaRow) bool {
	derivatives := mediaEssentialDerivativesWithoutRequired(row)
	if row.HasVideo || row.IsImage {
		return derivatives.Thumbnail
	}
	if row.HasAudio {
		return derivatives.Waveform
	}
	return false
}

func mediaEssentialDerivativesWithoutRequired(row MediaRow) MediaEssentialDerivatives {
	var result MediaEssentialDerivatives
	for _, derivation := range row.Derivations {
		if derivation.Status != "ok" || derivation.StorageFileID == "" {
			continue
		}
		switch derivation.Kind {
		case "thumbnail":
			result.Thumbnail = true
		case "waveform":
			result.Waveform = true
		case "cover":
			result.Cover = true
		}
	}
	return result
}

func mediaAudienceSuitability(rating string) string {
	switch strings.ToLower(strings.TrimSpace(rating)) {
	case "general":
		return "public"
	case "mature", "adult":
		return "restricted"
	default:
		return "unknown"
	}
}

func mediaPatreonStatusPriority(status string) int64 {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "ready":
		return 5
	case "scheduled":
		return 4
	case "draft", "pending", "review":
		return 3
	case "published", "shared", "posted":
		return 2
	case "blocked", "failed":
		return 1
	default:
		return 0
	}
}

func mediaAudienceRatingPriority(rating string) int64 {
	switch strings.ToLower(strings.TrimSpace(rating)) {
	case "general":
		return 3
	case "", "unrated":
		return 2
	case "mature":
		return 1
	default:
		return 0
	}
}

func mediaReleaseReadiness(row MediaRow) MediaReleaseReadiness {
	patreon := mediaPatreonStatus(row.Metadata)
	social := mediaSocialStatus(row.Metadata)
	hosting := mediaHostingSummary(row.Metadata)
	lineage := mediaLineageSummary(row.Metadata)
	required := mediaRequiredDerivativesPresent(row)
	return MediaReleaseReadiness{
		MediaReady:                 row.ProbeStatus == "ok" && required,
		ProbeReady:                 row.ProbeStatus == "ok",
		PatreonPublicationStatus:   patreon.Status,
		SocialUsageStatus:          social.Status,
		HostingReady:               hosting.Ready,
		HostingStatus:              hosting.Status,
		PublicAudienceSuitability:  mediaAudienceSuitability(row.AudienceRating),
		RequiredDerivativesPresent: required,
		ExactLineage:               lineage.Exact,
	}
}

func planningMediaSearchRows(rows []MediaRow, compact []MediaSearchRow) []MediaPlanningSearchRow {
	out := make([]MediaPlanningSearchRow, 0, len(rows))
	for i, row := range rows {
		base := projectMediaSearchRows([]MediaRow{row}, nil)[0]
		if i < len(compact) {
			base = compact[i]
		}
		out = append(out, MediaPlanningSearchRow{
			MediaSearchRow:       base,
			Patreon:              mediaPatreonStatus(row.Metadata),
			Social:               mediaSocialStatus(row.Metadata),
			Hosting:              mediaHostingSummary(row.Metadata),
			Lineage:              mediaLineageSummary(row.Metadata),
			PublicationState:     firstNonEmpty(mediaMetadataString(row.Metadata, "publication_state"), mediaMetadataString(row.Metadata, "release_status")),
			EssentialDerivatives: mediaEssentialDerivatives(row),
			Readiness:            mediaReleaseReadiness(row),
		})
	}
	return out
}

func projectMediaSearchFields[T any](items []T, fields []string) ([]map[string]any, error) {
	if len(fields) == 0 {
		return nil, nil
	}
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		raw, err := json.Marshal(item)
		if err != nil {
			return nil, err
		}
		var source map[string]any
		if err := json.Unmarshal(raw, &source); err != nil {
			return nil, err
		}
		projected := map[string]any{}
		for _, field := range fields {
			sourcePath := field
			outputPath := field
			switch field {
			case "id":
				sourcePath = "file_id"
			case "filename":
				if _, ok := source["filename"]; !ok {
					sourcePath = "name"
				}
			case "rating":
				sourcePath = "audience_rating"
			}
			value, found := nestedMapValue(source, strings.Split(sourcePath, ".")...)
			if found {
				setNestedMapValue(projected, strings.Split(outputPath, "."), value)
			}
		}
		out = append(out, projected)
	}
	return out, nil
}

func nestedMapValue(source map[string]any, path ...string) (any, bool) {
	var current any = source
	for _, segment := range path {
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = object[segment]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

func setNestedMapValue(target map[string]any, path []string, value any) {
	current := target
	for _, segment := range path[:len(path)-1] {
		next, _ := current[segment].(map[string]any)
		if next == nil {
			next = map[string]any{}
			current[segment] = next
		}
		current = next
	}
	current[path[len(path)-1]] = value
}
