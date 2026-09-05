package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type ReferenceSet struct {
	ID          int64  `json:"id"`
	ProjectID   string `json:"project_id"`
	Key         string `json:"key"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	CreatedAt   int64  `json:"created_at"`
	UpdatedAt   int64  `json:"updated_at"`
}

type ReferenceValue struct {
	ID             int64           `json:"id"`
	ReferenceSetID int64           `json:"reference_set_id"`
	Value          string          `json:"value"`
	Label          string          `json:"label"`
	Status         string          `json:"status"`
	Metadata       json.RawMessage `json:"metadata"`
	CreatedAt      int64           `json:"created_at"`
	UpdatedAt      int64           `json:"updated_at"`
}

type ReferenceOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

func validReferenceSetKey(key string) bool {
	key = strings.TrimSpace(key)
	if key == "" || len(key) > 128 {
		return false
	}
	for _, r := range key {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '_' || r == '-' || r == '.') {
			return false
		}
	}
	return true
}

func upsertReferenceSet(db sqlRunner, set ReferenceSet) (*ReferenceSet, error) {
	set.ProjectID = strings.TrimSpace(set.ProjectID)
	set.Key = strings.TrimSpace(set.Key)
	set.Label = strings.TrimSpace(set.Label)
	if set.ProjectID == "" || !validReferenceSetKey(set.Key) {
		return nil, errors.New("project_id and valid reference set key required")
	}
	if set.Label == "" {
		set.Label = set.Key
	}
	now := time.Now().UnixMilli()
	if _, err := db.Exec(`
		INSERT INTO reference_sets (project_id, key, label, description, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(project_id, key) DO UPDATE SET
			label=excluded.label,
			description=excluded.description,
			updated_at=excluded.updated_at
	`, set.ProjectID, set.Key, set.Label, set.Description, now, now); err != nil {
		return nil, err
	}
	return getReferenceSet(db, set.ProjectID, set.Key)
}

func getReferenceSet(db sqlRunner, projectID, key string) (*ReferenceSet, error) {
	var set ReferenceSet
	err := db.QueryRow(`
		SELECT id, project_id, key, label, description, created_at, updated_at
		FROM reference_sets WHERE project_id=? AND key=?
	`, projectID, strings.TrimSpace(key)).Scan(
		&set.ID, &set.ProjectID, &set.Key, &set.Label, &set.Description, &set.CreatedAt, &set.UpdatedAt,
	)
	return &set, err
}

func upsertReferenceValue(db sqlRunner, projectID, setKey string, value ReferenceValue) (*ReferenceValue, error) {
	set, err := getReferenceSet(db, projectID, setKey)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("reference set %q not found", setKey)
		}
		return nil, err
	}
	value.Value = strings.TrimSpace(value.Value)
	value.Label = strings.TrimSpace(value.Label)
	if value.Value == "" {
		return nil, errors.New("reference value required")
	}
	hasLabel, hasStatus, hasMetadata := value.Label != "", value.Status != "", len(value.Metadata) != 0
	if value.Label == "" {
		value.Label = value.Value
	}
	value.Status = strings.TrimSpace(value.Status)
	if value.Status == "" {
		value.Status = "active"
	}
	if value.Status != "active" && value.Status != "inactive" {
		return nil, fmt.Errorf("invalid reference value status %q", value.Status)
	}
	if len(value.Metadata) == 0 {
		value.Metadata = json.RawMessage(`{}`)
	}
	var metadataObject map[string]any
	if json.Unmarshal(value.Metadata, &metadataObject) != nil || metadataObject == nil {
		return nil, errors.New("metadata must be a JSON object")
	}
	now := time.Now().UnixMilli()
	if _, err := db.Exec(`
		INSERT INTO reference_values (reference_set_id, value, label, status, metadata_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(reference_set_id, value) DO UPDATE SET
			label=CASE WHEN ? THEN excluded.label ELSE reference_values.label END,
 status=CASE WHEN ? THEN excluded.status ELSE reference_values.status END,
 metadata_json=CASE WHEN ? THEN excluded.metadata_json ELSE reference_values.metadata_json END,
			updated_at=excluded.updated_at
	`, set.ID, value.Value, value.Label, value.Status, string(value.Metadata), now, now, hasLabel, hasStatus, hasMetadata); err != nil {
		return nil, err
	}
	var saved ReferenceValue
	var metadata string
	err = db.QueryRow(`
		SELECT id, reference_set_id, value, label, status, metadata_json, created_at, updated_at
		FROM reference_values WHERE reference_set_id=? AND value=?
	`, set.ID, value.Value).Scan(
		&saved.ID, &saved.ReferenceSetID, &saved.Value, &saved.Label, &saved.Status, &metadata, &saved.CreatedAt, &saved.UpdatedAt,
	)
	saved.Metadata = json.RawMessage(metadata)
	return &saved, err
}

func listReferenceValues(db sqlRunner, projectID, setKey, status string, limit int) (*ReferenceSet, []ReferenceValue, error) {
	set, err := getReferenceSet(db, projectID, setKey)
	if err != nil {
		return nil, nil, err
	}
	if limit <= 0 {
		limit = 200
	}
	if limit > 1000 {
		limit = 1000
	}
	query := `SELECT id, reference_set_id, value, label, status, metadata_json, created_at, updated_at
		FROM reference_values WHERE reference_set_id=?`
	args := []any{set.ID}
	if status != "" {
		query += ` AND status=?`
		args = append(args, status)
	}
	query += ` ORDER BY label, value LIMIT ?`
	args = append(args, limit)
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	values := make([]ReferenceValue, 0)
	for rows.Next() {
		var value ReferenceValue
		var metadata string
		if err := rows.Scan(&value.ID, &value.ReferenceSetID, &value.Value, &value.Label, &value.Status, &metadata, &value.CreatedAt, &value.UpdatedAt); err != nil {
			return nil, nil, err
		}
		value.Metadata = json.RawMessage(metadata)
		values = append(values, value)
	}
	return set, values, rows.Err()
}

func activeReferenceOptions(db sqlRunner, projectID, setKey string, limit int) ([]ReferenceOption, error) {
	_, values, err := listReferenceValues(db, projectID, setKey, "active", limit)
	if err != nil {
		return nil, err
	}
	options := make([]ReferenceOption, 0, len(values))
	for _, value := range values {
		options = append(options, ReferenceOption{Value: value.Value, Label: value.Label})
	}
	return options, nil
}

func activeReferenceValueExists(db sqlRunner, projectID, setKey, value string) (bool, error) {
	var found int
	err := db.QueryRow(`
		SELECT 1
		FROM reference_sets rs
		JOIN reference_values rv ON rv.reference_set_id=rs.id
		WHERE rs.project_id=? AND rs.key=? AND rv.value=? AND rv.status='active'
	`, projectID, setKey, value).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func hydrateEventSpecReferences(db sqlRunner, spec *EventSpec) error {
	for i := range spec.Properties {
		setKey := spec.Properties[i].ReferenceSet
		if setKey == "" {
			continue
		}
		options, err := activeReferenceOptions(db, spec.ProjectID, setKey, 200)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		spec.Properties[i].AllowedValues = options
		var total int64
		if err := db.QueryRow(`SELECT COUNT(*) FROM reference_values v JOIN reference_sets s ON s.id=v.reference_set_id WHERE s.project_id=? AND s.key=? AND v.status='active'`, spec.ProjectID, setKey).Scan(&total); err != nil {
			return err
		}
		spec.Properties[i].AllowedValuesTotal = total
		spec.Properties[i].AllowedValuesHasMore = total > int64(len(options))
	}
	return nil
}

func (a *App) toolReferenceSetUpsert(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	projectID, err := scopedProject(ctx, args)
	if err != nil {
		return nil, err
	}
	set, err := upsertReferenceSet(toolWriter(ctx), ReferenceSet{
		ProjectID: projectID,
		Key:       stringArg(args, "key"), Label: stringArg(args, "label"), Description: stringArg(args, "description"),
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"reference_set": set}, nil
}

func (a *App) toolReferenceValueUpsert(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	projectID, err := scopedProject(ctx, args)
	if err != nil {
		return nil, err
	}
	var metadata json.RawMessage
	if raw, ok := args["metadata"]; ok && raw != nil {
		encoded, err := json.Marshal(raw)
		if err != nil {
			return nil, err
		}
		metadata = encoded
	}
	value, err := upsertReferenceValue(toolWriter(ctx), projectID, stringArg(args, "reference_set"), ReferenceValue{
		Value: stringArg(args, "value"), Label: stringArg(args, "label"), Status: stringArg(args, "status"), Metadata: metadata,
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"reference_value": value}, nil
}

func (a *App) toolReferenceValuesList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	projectID, err := scopedProject(ctx, args)
	if err != nil {
		return nil, err
	}
	setKey := stringArg(args, "reference_set")
	if setKey == "" {
		return nil, errors.New("reference_set required")
	}
	status := stringArg(args, "status")
	if status == "" {
		status = "active"
	}
	if status != "active" && status != "inactive" {
		return nil, fmt.Errorf("invalid reference value status %q", status)
	}
	return referencePage(toolReader(ctx), projectID, setKey, status, stringArg(args, "search"), int64Arg(args, "after"), intArg(args, "limit"))
}
