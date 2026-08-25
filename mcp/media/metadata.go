package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const (
	maxMediaMetadataBytes      = 16 * 1024
	maxMediaMetadataDepth      = 16
	maxMediaMetadataConditions = 20
	maxMediaMetadataPathBytes  = 256
)

// MetadataCondition is one exact-match predicate over a nested metadata path.
// Path is the caller-facing form (metadata.patreon.status); JSONPath is the
// validated SQLite form ($.patreon.status). Values are restricted to JSON
// scalars so equality has predictable type-sensitive semantics.
type MetadataCondition struct {
	Path     string
	JSONPath string
	Value    any
}

type MetadataPatchResult struct {
	Found           bool            `json:"found"`
	Updated         bool            `json:"updated"`
	Reason          string          `json:"reason,omitempty"`
	FileID          string          `json:"file_id"`
	Metadata        json.RawMessage `json:"metadata"`
	MetadataVersion int64           `json:"metadata_version"`
}

func normalizeMetadataObject(v any, field string, requireNonEmpty bool) (map[string]any, []byte, error) {
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, nil, fmt.Errorf("%s must be a JSON object", field)
	}
	if requireNonEmpty && len(obj) == 0 {
		return nil, nil, fmt.Errorf("%s must contain at least one field", field)
	}
	b, err := json.Marshal(obj)
	if err != nil {
		return nil, nil, fmt.Errorf("%s is not valid JSON: %w", field, err)
	}
	if len(b) > maxMediaMetadataBytes {
		return nil, nil, fmt.Errorf("%s exceeds %d bytes", field, maxMediaMetadataBytes)
	}
	var normalized any
	if err := json.Unmarshal(b, &normalized); err != nil {
		return nil, nil, fmt.Errorf("%s is not valid JSON: %w", field, err)
	}
	if metadataValueDepth(normalized, 1) > maxMediaMetadataDepth {
		return nil, nil, fmt.Errorf("%s exceeds maximum nesting depth %d", field, maxMediaMetadataDepth)
	}
	return normalized.(map[string]any), b, nil
}

func metadataValueDepth(v any, depth int) int {
	maxDepth := depth
	switch x := v.(type) {
	case map[string]any:
		for _, child := range x {
			if d := metadataValueDepth(child, depth+1); d > maxDepth {
				maxDepth = d
			}
		}
	case []any:
		for _, child := range x {
			if d := metadataValueDepth(child, depth+1); d > maxDepth {
				maxDepth = d
			}
		}
	}
	return maxDepth
}

func parseMetadataConditions(v any, field string) ([]MetadataCondition, error) {
	if v == nil {
		return nil, nil
	}
	obj, _, err := normalizeMetadataObject(v, field, false)
	if err != nil {
		return nil, err
	}
	if len(obj) > maxMediaMetadataConditions {
		return nil, fmt.Errorf("%s supports at most %d fields", field, maxMediaMetadataConditions)
	}
	conditions := make([]MetadataCondition, 0, len(obj))
	for path, value := range obj {
		jsonPath, err := metadataJSONPath(path)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", field, err)
		}
		switch value.(type) {
		case nil, string, bool, float64:
			// Exact scalar comparisons are intentionally the complete v1
			// filter surface. Arrays/objects need explicit operators rather
			// than SQLite's surprising text-representation equality.
		default:
			return nil, fmt.Errorf("%s[%q] must be a JSON scalar", field, path)
		}
		conditions = append(conditions, MetadataCondition{Path: path, JSONPath: jsonPath, Value: value})
	}
	sort.Slice(conditions, func(i, j int) bool { return conditions[i].Path < conditions[j].Path })
	return conditions, nil
}

func metadataJSONPath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if len(path) > maxMediaMetadataPathBytes {
		return "", fmt.Errorf("metadata path exceeds %d bytes", maxMediaMetadataPathBytes)
	}
	if !strings.HasPrefix(path, "metadata.") {
		return "", fmt.Errorf("metadata path %q must start with metadata.", path)
	}
	relative := strings.TrimPrefix(path, "metadata.")
	parts := strings.Split(relative, ".")
	if relative == "" || len(parts) > maxMediaMetadataDepth {
		return "", fmt.Errorf("invalid metadata path %q", path)
	}
	for _, part := range parts {
		if part == "" {
			return "", fmt.Errorf("invalid metadata path %q", path)
		}
		for _, r := range part {
			if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
				(r >= '0' && r <= '9') || r == '_') {
				return "", fmt.Errorf("metadata path %q may contain only letters, numbers, underscores, and dot separators", path)
			}
		}
	}
	return "$." + relative, nil
}

func metadataVersionArg(v any) (*int64, error) {
	if v == nil {
		return nil, nil
	}
	var n int64
	switch x := v.(type) {
	case int:
		n = int64(x)
	case int64:
		n = x
	case float64:
		n = int64(x)
		if float64(n) != x {
			return nil, errors.New("expected_metadata_version must be an integer")
		}
	default:
		return nil, errors.New("expected_metadata_version must be an integer")
	}
	if n < 0 {
		return nil, errors.New("expected_metadata_version must be non-negative")
	}
	return &n, nil
}

// patchMediaMetadata applies an RFC 7396 JSON Merge Patch and every supplied
// condition in one UPDATE statement. RowsAffected/RETURNING is the compare-and-
// swap result: two callers racing on status=ready cannot both transition it.
func patchMediaMetadata(db *sql.DB, projectID, fileID string, patchJSON []byte, conditions []MetadataCondition, expectedVersion *int64) (MetadataPatchResult, error) {
	result := MetadataPatchResult{FileID: fileID, Metadata: json.RawMessage(`{}`)}
	now := time.Now().UTC().Format(time.RFC3339)
	clauses := []string{"project_id = ?", "file_id = ?", "length(CAST(json_patch(metadata, ?) AS BLOB)) <= ?"}
	args := []any{string(patchJSON), now, projectID, fileID, string(patchJSON), maxMediaMetadataBytes}
	if expectedVersion != nil {
		clauses = append(clauses, "metadata_version = ?")
		args = append(args, *expectedVersion)
	}
	for _, condition := range conditions {
		if condition.Value == nil {
			clauses = append(clauses, "json_type(metadata, ?) = 'null'")
			args = append(args, condition.JSONPath)
		} else {
			clauses = append(clauses, "json_extract(metadata, ?) = ?")
			args = append(args, condition.JSONPath, condition.Value)
		}
	}
	query := `UPDATE media
		SET metadata = json_patch(metadata, ?),
			metadata_version = metadata_version + 1,
			updated_at = ?
		WHERE ` + strings.Join(clauses, " AND ") + `
		RETURNING metadata, metadata_version`

	tx, err := db.Begin()
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	var raw string
	err = tx.QueryRow(query, args...).Scan(&raw, &result.MetadataVersion)
	if err == nil {
		result.Found = true
		result.Updated = true
		result.Metadata = json.RawMessage(raw)
		return result, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return result, err
	}

	var mergedBytes int
	err = tx.QueryRow(`SELECT metadata, metadata_version,
		length(CAST(json_patch(metadata, ?) AS BLOB))
		FROM media WHERE project_id = ? AND file_id = ?`, string(patchJSON), projectID, fileID).
		Scan(&raw, &result.MetadataVersion, &mergedBytes)
	if errors.Is(err, sql.ErrNoRows) {
		result.Reason = "not_found"
		return result, tx.Commit()
	}
	if err != nil {
		return result, err
	}
	result.Found = true
	result.Metadata = json.RawMessage(raw)
	switch {
	case mergedBytes > maxMediaMetadataBytes:
		result.Reason = "metadata_too_large"
	case expectedVersion != nil && result.MetadataVersion != *expectedVersion:
		result.Reason = "metadata_version_mismatch"
	default:
		result.Reason = "condition_failed"
	}
	return result, tx.Commit()
}

func (a *App) toolPatchMetadata(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	projectID, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	fileID, _ := args["file_id"].(string)
	if strings.TrimSpace(fileID) == "" {
		return nil, errors.New("file_id required")
	}
	_, patchJSON, err := normalizeMetadataObject(args["patch"], "patch", true)
	if err != nil {
		return nil, err
	}
	conditions, err := parseMetadataConditions(args["conditions"], "conditions")
	if err != nil {
		return nil, err
	}
	expectedVersion, err := metadataVersionArg(args["expected_metadata_version"])
	if err != nil {
		return nil, err
	}
	result, err := patchMediaMetadata(ctx.AppDB(), projectID, fileID, patchJSON, conditions, expectedVersion)
	if err != nil {
		return nil, err
	}
	if result.Updated {
		ctx.EmitWithProject("media.updated", projectID, map[string]any{
			"file_id":          fileID,
			"change":           "metadata_patched",
			"metadata_version": result.MetadataVersion,
		})
	}
	return result, nil
}

func metadataConditionsEcho(conditions []MetadataCondition) map[string]any {
	if len(conditions) == 0 {
		return nil
	}
	out := make(map[string]any, len(conditions))
	for _, condition := range conditions {
		out[condition.Path] = condition.Value
	}
	return out
}
