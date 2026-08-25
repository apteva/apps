package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type EventSpec struct {
	ID             int64               `json:"id"`
	ProjectID      string              `json:"project_id"`
	App            string              `json:"app"`
	Topic          string              `json:"topic"`
	Kind           string              `json:"kind"`
	DisplayName    string              `json:"display_name"`
	Description    string              `json:"description"`
	Category       string              `json:"category"`
	Status         string              `json:"status"`
	ValidationMode string              `json:"validation_mode"`
	IngestMode     string              `json:"ingest_mode"`
	UpsertPolicy   *EventIngestPolicy  `json:"upsert_policy,omitempty"`
	RollupPolicy   *EventIngestPolicy  `json:"rollup_policy,omitempty"`
	CreatedBy      string              `json:"created_by,omitempty"`
	CreatedAt      int64               `json:"created_at,omitempty"`
	UpdatedAt      int64               `json:"updated_at,omitempty"`
	Properties     []EventPropertySpec `json:"properties,omitempty"`
}

type EventIngestPolicy struct {
	TargetTopic       string   `json:"target_topic,omitempty"`
	Bucket            string   `json:"bucket,omitempty"`
	Timezone          string   `json:"timezone,omitempty"`
	TimestampProperty string   `json:"timestamp_property,omitempty"`
	Operation         string   `json:"operation,omitempty"`
	Value             any      `json:"value,omitempty"`
	ValueKey          string   `json:"value_key,omitempty"`
	OutputProperty    string   `json:"output_property,omitempty"`
	Dimensions        []string `json:"dimensions,omitempty"`
}

type EventPropertySpec struct {
	ID                int64             `json:"id,omitempty"`
	EventSpecID       int64             `json:"event_spec_id,omitempty"`
	Key               string            `json:"key"`
	Type              string            `json:"type"`
	Required          bool              `json:"required"`
	Description       string            `json:"description,omitempty"`
	EnumValues        []string          `json:"enum_values,omitempty"`
	ReferenceSet      string            `json:"reference_set,omitempty"`
	AllowedValues     []ReferenceOption `json:"allowed_values,omitempty"`
	PIIClassification string            `json:"pii_classification"`
	ExampleValue      string            `json:"example_value,omitempty"`
	CreatedAt         int64             `json:"created_at,omitempty"`
	UpdatedAt         int64             `json:"updated_at,omitempty"`
}

type EventSpecViolation struct {
	ID            int64  `json:"id,omitempty"`
	ProjectID     string `json:"project_id"`
	App           string `json:"app"`
	Topic         string `json:"topic"`
	EventID       int64  `json:"event_id,omitempty"`
	ViolationType string `json:"violation_type"`
	Message       string `json:"message"`
	PropertyKey   string `json:"property_key,omitempty"`
	SeenAt        int64  `json:"seen_at"`
}

type validationOutcome struct {
	Reject     bool
	Violations []EventSpecViolation
}

func (a *App) handleEventSpecs(w http.ResponseWriter, r *http.Request) {
	if !requireUser(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	projectID, ok := requireRequestProject(w, r)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		specs, err := listEventSpecs(globalCtx.AppDB(), specFilter{
			ProjectID: projectID,
			App:       r.URL.Query().Get("app"),
			Status:    r.URL.Query().Get("status"),
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"specs": specs})
	case http.MethodPost:
		var spec EventSpec
		if err := json.NewDecoder(r.Body).Decode(&spec); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		if _, err := assignRequestProject(spec.ProjectID, projectID); err != nil {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
		spec.ID = 0
		spec.ProjectID = projectID
		saved, err := upsertEventSpec(globalCtx.AppDB(), spec, true)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, saved)
	default:
		http.Error(w, "GET or POST only", http.StatusMethodNotAllowed)
	}
}

func (a *App) handleEventSpecItem(w http.ResponseWriter, r *http.Request) {
	if !requireUser(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	projectID, ok := requireRequestProject(w, r)
	if !ok {
		return
	}
	parts := splitPath(strings.TrimPrefix(r.URL.Path, "/event-specs/"))
	if len(parts) == 0 {
		http.NotFound(w, r)
		return
	}
	if parts[0] == "validate" {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		a.handleEventSpecValidate(w, r, projectID)
		return
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "invalid spec id", http.StatusBadRequest)
		return
	}
	if len(parts) >= 2 && parts[1] == "properties" {
		if r.Method != http.MethodPost && r.Method != http.MethodPut && r.Method != http.MethodDelete {
			http.Error(w, "POST, PUT or DELETE only", http.StatusMethodNotAllowed)
			return
		}
		if r.Method == http.MethodDelete {
			if len(parts) != 3 {
				http.Error(w, "property key required", http.StatusBadRequest)
				return
			}
			a.handleEventPropertyDelete(w, r, projectID, id, parts[2])
			return
		}
		a.handleEventPropertyUpsert(w, r, projectID, id)
		return
	}
	if len(parts) != 1 {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet:
		spec, err := getEventSpecForProject(globalCtx.AppDB(), id, projectID)
		if errors.Is(err, sql.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, spec)
	case http.MethodPut:
		existing, err := getEventSpecForProject(globalCtx.AppDB(), id, projectID)
		if errors.Is(err, sql.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		var spec EventSpec
		if err := json.NewDecoder(r.Body).Decode(&spec); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		if _, err := assignRequestProject(spec.ProjectID, projectID); err != nil {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
		if strings.TrimSpace(spec.App) != existing.App || strings.TrimSpace(spec.Topic) != existing.Topic {
			http.Error(w, "app and topic cannot be changed; create a new spec", http.StatusBadRequest)
			return
		}
		spec.ID = id
		spec.ProjectID = projectID
		saved, err := upsertEventSpec(globalCtx.AppDB(), spec, true)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, saved)
	case http.MethodDelete:
		result, err := globalCtx.AppDB().Exec(`DELETE FROM event_specs WHERE id=? AND project_id=?`, id, projectID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if changed, _ := result.RowsAffected(); changed == 0 {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	default:
		http.Error(w, "GET, PUT or DELETE only", http.StatusMethodNotAllowed)
	}
}

func (a *App) handleEventSpecViolations(w http.ResponseWriter, r *http.Request) {
	if !requireUser(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	projectID, ok := requireRequestProject(w, r)
	if !ok {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	rows, err := listEventSpecViolations(globalCtx.AppDB(), Filter{
		ProjectID: projectID,
		App:       r.URL.Query().Get("app"),
		Topic:     r.URL.Query().Get("topic"),
		Since:     parseInt64(r.URL.Query().Get("since")),
	}, queryLimit(r, 100, 1000))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"violations": rows})
}

func (a *App) handleEventSpecValidate(w http.ResponseWriter, r *http.Request, projectID string) {
	var body struct {
		App       string         `json:"app"`
		Topic     string         `json:"topic"`
		Event     string         `json:"event"`
		ProjectID string         `json:"project_id"`
		UserID    string         `json:"user_id"`
		SessionID string         `json:"session_id"`
		UpsertKey string         `json:"upsert_key"`
		Props     map[string]any `json:"props"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if body.Topic == "" {
		body.Topic = body.Event
	}
	if body.App == "" {
		body.App = "_explicit"
	}
	if _, err := assignRequestProject(body.ProjectID, projectID); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	body.ProjectID = projectID
	propsJSON := "{}"
	if body.Props != nil {
		b, _ := json.Marshal(body.Props)
		propsJSON = string(b)
	}
	out, err := validateEventAgainstSpecs(globalCtx.AppDB(), EventInsert{
		App:       body.App,
		Topic:     body.Topic,
		ProjectID: body.ProjectID,
		UserID:    body.UserID,
		SessionID: body.SessionID,
		UpsertKey: body.UpsertKey,
		Props:     propsJSON,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	ev := EventInsert{
		App:       body.App,
		Topic:     body.Topic,
		ProjectID: body.ProjectID,
		UserID:    body.UserID,
		SessionID: body.SessionID,
		UpsertKey: body.UpsertKey,
		Props:     propsJSON,
	}
	writeJSON(w, map[string]any{"valid": len(out.Violations) == 0, "reject": out.Reject, "violations": out.Violations, "ingest": previewEventIngest(globalCtx.AppDB(), ev)})
}

func (a *App) handleEventPropertyUpsert(w http.ResponseWriter, r *http.Request, projectID string, specID int64) {
	if _, err := getEventSpecForProject(globalCtx.AppDB(), specID, projectID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.NotFound(w, r)
		} else {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}
	var prop EventPropertySpec
	if err := json.NewDecoder(r.Body).Decode(&prop); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	prop.EventSpecID = specID
	if err := upsertEventPropertySpec(globalCtx.AppDB(), prop); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	spec, err := getEventSpecByID(globalCtx.AppDB(), specID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, spec)
}

func (a *App) handleEventPropertyDelete(w http.ResponseWriter, r *http.Request, projectID string, specID int64, key string) {
	if _, err := globalCtx.AppDB().Exec(
		`DELETE FROM event_property_specs WHERE event_spec_id=? AND key=?
		 AND event_spec_id IN (SELECT id FROM event_specs WHERE project_id=?)`,
		specID, key, projectID,
	); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func getEventSpecForProject(db *sql.DB, id int64, projectID string) (*EventSpec, error) {
	var found int
	if err := db.QueryRow(`SELECT 1 FROM event_specs WHERE id=? AND project_id=?`, id, projectID).Scan(&found); err != nil {
		return nil, err
	}
	return getEventSpecByID(db, id)
}

type specFilter struct {
	ProjectID string
	App       string
	Status    string
}

func listEventSpecs(db *sql.DB, f specFilter) ([]EventSpec, error) {
	q := `SELECT id, project_id, app, topic, kind, display_name, description, category, status, validation_mode, ingest_mode, upsert_policy, rollup_policy, created_by, created_at, updated_at FROM event_specs`
	var conds []string
	var args []any
	if f.ProjectID != "" {
		conds = append(conds, "project_id = ?")
		args = append(args, f.ProjectID)
	}
	if f.App != "" {
		conds = append(conds, "app = ?")
		args = append(args, f.App)
	}
	if f.Status != "" {
		conds = append(conds, "status = ?")
		args = append(args, f.Status)
	}
	if len(conds) > 0 {
		q += " WHERE " + strings.Join(conds, " AND ")
	}
	q += " ORDER BY app, category, topic"
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	var out []EventSpec
	for rows.Next() {
		var spec EventSpec
		if err := scanEventSpec(rows, &spec); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, spec)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for i := range out {
		props, err := listEventPropertySpecs(db, out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].Properties = props
		if err := hydrateEventSpecReferences(db, &out[i]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func getEventSpecByID(db *sql.DB, id int64) (*EventSpec, error) {
	var spec EventSpec
	row := db.QueryRow(`SELECT id, project_id, app, topic, kind, display_name, description, category, status, validation_mode, ingest_mode, upsert_policy, rollup_policy, created_by, created_at, updated_at FROM event_specs WHERE id=?`, id)
	if err := scanEventSpec(row, &spec); err != nil {
		return nil, err
	}
	props, err := listEventPropertySpecs(db, spec.ID)
	if err != nil {
		return nil, err
	}
	spec.Properties = props
	if err := hydrateEventSpecReferences(db, &spec); err != nil {
		return nil, err
	}
	return &spec, nil
}

func getEventSpec(db *sql.DB, projectID, app, topic string) (*EventSpec, error) {
	var spec EventSpec
	row := db.QueryRow(`SELECT id, project_id, app, topic, kind, display_name, description, category, status, validation_mode, ingest_mode, upsert_policy, rollup_policy, created_by, created_at, updated_at FROM event_specs WHERE project_id=? AND app=? AND topic=?`, projectID, app, topic)
	if err := scanEventSpec(row, &spec); err != nil {
		return nil, err
	}
	props, err := listEventPropertySpecs(db, spec.ID)
	if err != nil {
		return nil, err
	}
	spec.Properties = props
	if err := hydrateEventSpecReferences(db, &spec); err != nil {
		return nil, err
	}
	return &spec, nil
}

func scanEventSpec(row interface{ Scan(...any) error }, spec *EventSpec) error {
	var upsertPolicy, rollupPolicy sql.NullString
	if err := row.Scan(&spec.ID, &spec.ProjectID, &spec.App, &spec.Topic, &spec.Kind, &spec.DisplayName, &spec.Description, &spec.Category, &spec.Status, &spec.ValidationMode, &spec.IngestMode, &upsertPolicy, &rollupPolicy, &spec.CreatedBy, &spec.CreatedAt, &spec.UpdatedAt); err != nil {
		return err
	}
	spec.UpsertPolicy = decodeIngestPolicy(upsertPolicy)
	spec.RollupPolicy = decodeIngestPolicy(rollupPolicy)
	return nil
}

func upsertEventSpec(db *sql.DB, spec EventSpec, replaceProperties bool) (*EventSpec, error) {
	spec.App = strings.TrimSpace(spec.App)
	spec.Topic = strings.TrimSpace(spec.Topic)
	if spec.App == "" {
		spec.App = "_explicit"
	}
	if spec.Topic == "" {
		return nil, errors.New("topic required")
	}
	spec.Kind = normalizeChoice(spec.Kind, "occurrence", map[string]bool{"occurrence": true, "aggregate_observation": true})
	spec.Status = normalizeChoice(spec.Status, "active", map[string]bool{"draft": true, "active": true, "deprecated": true, "blocked": true})
	spec.ValidationMode = normalizeChoice(spec.ValidationMode, "observe", map[string]bool{"observe": true, "warn": true, "reject": true})
	spec.IngestMode = normalizeChoice(spec.IngestMode, "raw", map[string]bool{"raw": true, "upsert": true, "raw_plus_rollup": true})
	defaultTimestampProperty(spec.UpsertPolicy, spec.Properties)
	defaultTimestampProperty(spec.RollupPolicy, spec.Properties)
	normalizeIngestPolicy(spec.UpsertPolicy)
	normalizeIngestPolicy(spec.RollupPolicy)
	upsertPolicy, err := encodeIngestPolicy(spec.UpsertPolicy)
	if err != nil {
		return nil, err
	}
	rollupPolicy, err := encodeIngestPolicy(spec.RollupPolicy)
	if err != nil {
		return nil, err
	}
	now := time.Now().UnixMilli()
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(
		`INSERT INTO event_specs (project_id, app, topic, kind, display_name, description, category, status, validation_mode, ingest_mode, upsert_policy, rollup_policy, created_by, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(project_id, app, topic) DO UPDATE SET
			kind=excluded.kind,
			display_name=excluded.display_name,
			description=excluded.description,
			category=excluded.category,
			status=excluded.status,
			validation_mode=excluded.validation_mode,
			ingest_mode=excluded.ingest_mode,
			upsert_policy=excluded.upsert_policy,
			rollup_policy=excluded.rollup_policy,
			updated_at=excluded.updated_at`,
		spec.ProjectID, spec.App, spec.Topic, spec.Kind, spec.DisplayName, spec.Description, spec.Category, spec.Status, spec.ValidationMode, spec.IngestMode, upsertPolicy, rollupPolicy, spec.CreatedBy, now, now,
	)
	if err != nil {
		return nil, err
	}
	id := spec.ID
	if id == 0 {
		_ = tx.QueryRow(`SELECT id FROM event_specs WHERE project_id=? AND app=? AND topic=?`, spec.ProjectID, spec.App, spec.Topic).Scan(&id)
	}
	if id == 0 {
		id, _ = res.LastInsertId()
	}
	if replaceProperties {
		if _, err := tx.Exec(`DELETE FROM event_property_specs WHERE event_spec_id=?`, id); err != nil {
			return nil, err
		}
		for _, prop := range spec.Properties {
			prop.EventSpecID = id
			if err := upsertEventPropertySpec(tx, prop); err != nil {
				return nil, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return getEventSpecByID(db, id)
}

func defaultTimestampProperty(policy *EventIngestPolicy, properties []EventPropertySpec) {
	if policy == nil || strings.TrimSpace(policy.TimestampProperty) != "" || policy.Bucket == "" || policy.Bucket == "none" {
		return
	}
	for _, property := range properties {
		if property.Key == "props.date" {
			policy.TimestampProperty = "props.date"
			return
		}
	}
}

func decodeIngestPolicy(raw sql.NullString) *EventIngestPolicy {
	if !raw.Valid || strings.TrimSpace(raw.String) == "" {
		return nil
	}
	var policy EventIngestPolicy
	if err := json.Unmarshal([]byte(raw.String), &policy); err != nil {
		return nil
	}
	normalizeIngestPolicy(&policy)
	return &policy
}

func encodeIngestPolicy(policy *EventIngestPolicy) (any, error) {
	if policy == nil || policyEmpty(*policy) {
		return nil, nil
	}
	b, err := json.Marshal(policy)
	if err != nil {
		return nil, err
	}
	return string(b), nil
}

func policyEmpty(policy EventIngestPolicy) bool {
	return policy.TargetTopic == "" && policy.Bucket == "" && policy.Timezone == "" && policy.TimestampProperty == "" &&
		policy.Operation == "" && policy.Value == nil && policy.ValueKey == "" &&
		policy.OutputProperty == "" && len(policy.Dimensions) == 0
}

func normalizeIngestPolicy(policy *EventIngestPolicy) {
	if policy == nil {
		return
	}
	policy.Bucket = normalizeChoice(policy.Bucket, "none", map[string]bool{
		"none": true, "hour": true, "day": true, "week": true, "month": true,
	})
	policy.Operation = normalizeChoice(policy.Operation, "replace", map[string]bool{
		"replace": true, "increment": true, "sum": true, "min": true, "max": true,
	})
	if strings.TrimSpace(policy.Timezone) == "" {
		policy.Timezone = "UTC"
	}
	policy.TimestampProperty = strings.TrimSpace(policy.TimestampProperty)
	if strings.TrimSpace(policy.OutputProperty) == "" {
		if policy.Operation == "increment" || policy.Operation == "sum" {
			policy.OutputProperty = "count"
		} else if key := strings.TrimSpace(policy.ValueKey); strings.HasPrefix(key, "props.") {
			policy.OutputProperty = lastPathSegment(key)
		} else if s, ok := policy.Value.(string); ok && strings.HasPrefix(s, "props.") {
			policy.OutputProperty = lastPathSegment(s)
		} else {
			policy.OutputProperty = "value"
		}
	}
	for i, dim := range policy.Dimensions {
		policy.Dimensions[i] = strings.TrimSpace(dim)
	}
}

func listEventPropertySpecs(db *sql.DB, specID int64) ([]EventPropertySpec, error) {
	rows, err := db.Query(
		`SELECT id, event_spec_id, key, type, required, description, enum_values, reference_set, pii_classification, example_value, created_at, updated_at
		 FROM event_property_specs WHERE event_spec_id=? ORDER BY required DESC, key`,
		specID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EventPropertySpec
	for rows.Next() {
		var prop EventPropertySpec
		var required int
		var enumJSON, referenceSet sql.NullString
		if err := rows.Scan(&prop.ID, &prop.EventSpecID, &prop.Key, &prop.Type, &required, &prop.Description, &enumJSON, &referenceSet, &prop.PIIClassification, &prop.ExampleValue, &prop.CreatedAt, &prop.UpdatedAt); err != nil {
			return nil, err
		}
		prop.Required = required == 1
		prop.ReferenceSet = referenceSet.String
		if enumJSON.Valid && enumJSON.String != "" {
			_ = json.Unmarshal([]byte(enumJSON.String), &prop.EnumValues)
		}
		out = append(out, prop)
	}
	return out, rows.Err()
}

func upsertEventPropertySpec(db sqlRunner, prop EventPropertySpec) error {
	prop.Key = strings.TrimSpace(prop.Key)
	if prop.EventSpecID <= 0 || prop.Key == "" {
		return errors.New("event_spec_id and key required")
	}
	if !validEventPropertyKey(prop.Key) {
		return fmt.Errorf("unsupported property key %q", prop.Key)
	}
	prop.Type = normalizeChoice(prop.Type, "string", map[string]bool{
		"string": true, "number": true, "boolean": true, "object": true, "array": true, "timestamp": true,
	})
	prop.PIIClassification = normalizeChoice(prop.PIIClassification, "none", map[string]bool{
		"none": true, "contact": true, "identifier": true, "sensitive": true, "secret": true,
	})
	prop.ReferenceSet = strings.TrimSpace(prop.ReferenceSet)
	if prop.ReferenceSet != "" && !validReferenceSetKey(prop.ReferenceSet) {
		return fmt.Errorf("invalid reference_set %q", prop.ReferenceSet)
	}
	if prop.ReferenceSet != "" && prop.Type != "string" {
		return errors.New("reference_set is only supported for string properties")
	}
	if prop.ReferenceSet != "" {
		var found int
		if err := db.QueryRow(`
			SELECT 1 FROM reference_sets
			WHERE project_id=(SELECT project_id FROM event_specs WHERE id=?) AND key=?
		`, prop.EventSpecID, prop.ReferenceSet).Scan(&found); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("reference set %q not found in the event spec project", prop.ReferenceSet)
			}
			return err
		}
	}
	required := 0
	if prop.Required {
		required = 1
	}
	var enumJSON any
	if len(prop.EnumValues) > 0 {
		b, _ := json.Marshal(prop.EnumValues)
		enumJSON = string(b)
	}
	now := time.Now().UnixMilli()
	_, err := db.Exec(
		`INSERT INTO event_property_specs (event_spec_id, key, type, required, description, enum_values, reference_set, pii_classification, example_value, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(event_spec_id, key) DO UPDATE SET
			type=excluded.type,
			required=excluded.required,
			description=excluded.description,
			enum_values=excluded.enum_values,
			reference_set=excluded.reference_set,
			pii_classification=excluded.pii_classification,
			example_value=excluded.example_value,
			updated_at=excluded.updated_at`,
		prop.EventSpecID, prop.Key, prop.Type, required, prop.Description, enumJSON, nullStr(prop.ReferenceSet), prop.PIIClassification, prop.ExampleValue, now, now,
	)
	return err
}

func validateEventInsert(db *sql.DB, ev EventInsert) (validationOutcome, error) {
	out, err := validateEventAgainstSpecs(db, ev)
	if err != nil {
		return validationOutcome{}, err
	}
	if out.Reject {
		return out, fmt.Errorf("event rejected by spec: %s", joinViolationMessages(out.Violations))
	}
	return out, nil
}

func validateEventAgainstSpecs(db *sql.DB, ev EventInsert) (validationOutcome, error) {
	now := time.Now().UnixMilli()
	spec, err := getEventSpec(db, ev.ProjectID, ev.App, ev.Topic)
	if errors.Is(err, sql.ErrNoRows) {
		return validationOutcome{Violations: []EventSpecViolation{{
			ProjectID: ev.ProjectID, App: ev.App, Topic: ev.Topic,
			ViolationType: "unknown_event",
			Message:       "event has no spec",
			SeenAt:        now,
		}}}, nil
	}
	if err != nil {
		return validationOutcome{}, err
	}
	if spec.IngestMode == "upsert" && spec.UpsertPolicy != nil && ev.UpsertKey == "" {
		if next, err := eventFromPolicy(db, ev, spec.Topic, spec.UpsertPolicy, false); err == nil {
			ev = next
		}
	}
	var violations []EventSpecViolation
	add := func(kind, key, msg string) {
		violations = append(violations, EventSpecViolation{
			ProjectID: ev.ProjectID, App: ev.App, Topic: ev.Topic,
			ViolationType: kind, PropertyKey: key, Message: msg, SeenAt: now,
		})
	}
	switch spec.Status {
	case "blocked":
		add("blocked_event", "", "event spec is blocked")
	case "deprecated":
		add("deprecated_event", "", "event spec is deprecated")
	}
	values := eventValueMap(ev)
	for _, prop := range spec.Properties {
		v, ok := values[prop.Key]
		if prop.Required && (!ok || v == nil || v == "") {
			add("missing_required", prop.Key, "required property missing")
			continue
		}
		if !ok || v == nil || v == "" {
			continue
		}
		if !valueMatchesType(v, prop.Type) {
			add("type_mismatch", prop.Key, "property has wrong type")
			continue
		}
		if len(prop.EnumValues) > 0 && !stringIn(fmt.Sprint(v), prop.EnumValues) {
			add("enum_mismatch", prop.Key, "property value is not allowed")
		}
		if prop.ReferenceSet != "" {
			found, err := activeReferenceValueExists(db, ev.ProjectID, prop.ReferenceSet, fmt.Sprint(v))
			if err != nil {
				return validationOutcome{}, err
			}
			if !found {
				add("reference_not_found", prop.Key, fmt.Sprintf("value %q is not active in reference set %q", fmt.Sprint(v), prop.ReferenceSet))
			}
		}
	}
	reject := spec.Status == "blocked" || (spec.ValidationMode == "reject" && len(violations) > 0)
	return validationOutcome{Reject: reject, Violations: violations}, nil
}

func recordEventSpecViolations(db sqlRunner, eventID int64, violations []EventSpecViolation) {
	for _, v := range violations {
		_, _ = db.Exec(
			`INSERT INTO event_spec_violations (project_id, app, topic, event_id, violation_type, message, property_key, seen_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			v.ProjectID, v.App, v.Topic, eventID, v.ViolationType, v.Message, v.PropertyKey, v.SeenAt,
		)
	}
}

func listEventSpecViolations(db *sql.DB, f Filter, limit int) ([]EventSpecViolation, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	var conds []string
	var args []any
	if f.ProjectID != "" {
		conds = append(conds, "project_id = ?")
		args = append(args, f.ProjectID)
	}
	if f.App != "" {
		conds = append(conds, "app = ?")
		args = append(args, f.App)
	}
	if f.Topic != "" {
		conds = append(conds, "topic = ?")
		args = append(args, f.Topic)
	}
	if f.Since > 0 {
		conds = append(conds, "seen_at >= ?")
		args = append(args, f.Since)
	}
	q := `SELECT id, project_id, app, topic, event_id, violation_type, message, property_key, seen_at FROM event_spec_violations`
	if len(conds) > 0 {
		q += " WHERE " + strings.Join(conds, " AND ")
	}
	q += " ORDER BY seen_at DESC LIMIT ?"
	args = append(args, limit)
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EventSpecViolation
	for rows.Next() {
		var v EventSpecViolation
		var eventID sql.NullInt64
		if err := rows.Scan(&v.ID, &v.ProjectID, &v.App, &v.Topic, &eventID, &v.ViolationType, &v.Message, &v.PropertyKey, &v.SeenAt); err != nil {
			return nil, err
		}
		v.EventID = eventID.Int64
		out = append(out, v)
	}
	return out, rows.Err()
}

func eventValueMap(ev EventInsert) map[string]any {
	out := map[string]any{
		"app":        ev.App,
		"topic":      ev.Topic,
		"project_id": ev.ProjectID,
		"user_id":    ev.UserID,
		"session_id": ev.SessionID,
		"source":     ev.Source,
		"upsert_key": ev.UpsertKey,
		"ts":         ev.TS,
	}
	var props map[string]any
	_ = json.Unmarshal([]byte(ev.Props), &props)
	flattenEventProps(out, "props", props)
	return out
}

func flattenEventProps(out map[string]any, prefix string, value any) {
	obj, ok := value.(map[string]any)
	if !ok {
		out[prefix] = value
		return
	}
	for k, v := range obj {
		key := prefix + "." + k
		out[key] = v
		flattenEventProps(out, key, v)
	}
}

func validEventPropertyKey(key string) bool {
	switch key {
	case "app", "topic", "project_id", "user_id", "session_id", "source", "upsert_key", "ts":
		return true
	default:
		_, ok := propsExtract(key)
		return ok
	}
}

func valueMatchesType(v any, typ string) bool {
	switch typ {
	case "number", "timestamp":
		switch n := v.(type) {
		case float64:
			return !math.IsNaN(n)
		case int, int64, json.Number:
			return true
		default:
			return false
		}
	case "boolean":
		_, ok := v.(bool)
		return ok
	case "object":
		_, ok := v.(map[string]any)
		return ok
	case "array":
		_, ok := v.([]any)
		return ok
	default:
		_, ok := v.(string)
		return ok
	}
}

func normalizeChoice(v, def string, allowed map[string]bool) string {
	v = strings.TrimSpace(v)
	if v == "" || !allowed[v] {
		return def
	}
	return v
}

func stringIn(v string, choices []string) bool {
	for _, c := range choices {
		if c == v {
			return true
		}
	}
	return false
}

func joinViolationMessages(violations []EventSpecViolation) string {
	if len(violations) == 0 {
		return "invalid event"
	}
	parts := make([]string, 0, len(violations))
	for _, v := range violations {
		if v.PropertyKey != "" {
			parts = append(parts, v.PropertyKey+": "+v.Message)
		} else {
			parts = append(parts, v.Message)
		}
	}
	return strings.Join(parts, "; ")
}
