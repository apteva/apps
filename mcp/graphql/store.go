package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

type schemaRecord struct {
	ID              int64
	ProjectID       string
	Environment     string
	Version         int
	SDL             string
	Status          string
	Hash            string
	ValidationError []string
	CreatedAt       string
	PublishedAt     string
}

type sourceRecord struct {
	ID        int64
	ProjectID string
	Name      string
	Kind      string
	Config    map[string]any
	Status    string
}

type resolverRecord struct {
	ID         int64
	ProjectID  string
	ParentType string
	FieldName  string
	SourceID   int64
	Operation  string
	Config     map[string]any
}

func nowUTC() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func jsonObject(v any) (map[string]any, error) {
	if v == nil {
		return map[string]any{}, nil
	}
	if obj, ok := v.(map[string]any); ok {
		return obj, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil || out == nil {
		return nil, invalid("expected a JSON object")
	}
	return out, nil
}

func encodeJSON(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func decodeJSON(s string) map[string]any {
	var out map[string]any
	if json.Unmarshal([]byte(s), &out) != nil || out == nil {
		return map[string]any{}
	}
	return out
}

func schemaHash(sdl string) string {
	h := sha256.Sum256([]byte(sdl))
	return hex.EncodeToString(h[:])
}

func normalizeEnvironment(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "" {
		return "development"
	}
	return v
}

func getSchema(db *sql.DB, project, environment string, version int, publishedOnly bool) (*schemaRecord, error) {
	query := `SELECT id, project_id, environment, version, sdl, status, hash,
        validation_errors, created_at, COALESCE(published_at,'')
        FROM graphql_schemas WHERE project_id=? AND environment=?`
	args := []any{project, normalizeEnvironment(environment)}
	if version > 0 {
		query += " AND version=?"
		args = append(args, version)
	}
	if publishedOnly {
		query += " AND status='published'"
	}
	query += " ORDER BY version DESC LIMIT 1"
	var out schemaRecord
	var errorsJSON string
	if err := db.QueryRow(query, args...).Scan(&out.ID, &out.ProjectID, &out.Environment, &out.Version,
		&out.SDL, &out.Status, &out.Hash, &errorsJSON, &out.CreatedAt, &out.PublishedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	_ = json.Unmarshal([]byte(errorsJSON), &out.ValidationError)
	return &out, nil
}

func listSchemas(db *sql.DB, project, environment string) ([]schemaRecord, error) {
	rows, err := db.Query(`SELECT id, project_id, environment, version, sdl, status, hash,
        validation_errors, created_at, COALESCE(published_at,'')
        FROM graphql_schemas WHERE project_id=? AND environment=? ORDER BY version DESC`, project, normalizeEnvironment(environment))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []schemaRecord
	for rows.Next() {
		var row schemaRecord
		var errorsJSON string
		if err := rows.Scan(&row.ID, &row.ProjectID, &row.Environment, &row.Version, &row.SDL, &row.Status, &row.Hash, &errorsJSON, &row.CreatedAt, &row.PublishedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(errorsJSON), &row.ValidationError)
		out = append(out, row)
	}
	return out, rows.Err()
}

func createSchema(db *sql.DB, project, environment, sdl string, version int) (*schemaRecord, []string, error) {
	sdl = strings.TrimSpace(sdl)
	if sdl == "" {
		return nil, []string{"sdl is required"}, invalid("sdl is required")
	}
	_, validationErrors := validateSDL(sdl)
	if version <= 0 {
		_ = db.QueryRow(`SELECT COALESCE(MAX(version),0)+1 FROM graphql_schemas WHERE project_id=? AND environment=?`, project, normalizeEnvironment(environment)).Scan(&version)
	}
	errorsJSON, _ := encodeJSON(validationErrors)
	status := "draft"
	if len(validationErrors) > 0 {
		status = "invalid"
	}
	now := nowUTC()
	result, err := db.Exec(`INSERT INTO graphql_schemas
        (project_id, environment, version, sdl, status, hash, validation_errors, created_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT(project_id, environment, version) DO UPDATE SET
          sdl=excluded.sdl, status=excluded.status, hash=excluded.hash,
          validation_errors=excluded.validation_errors`, project, normalizeEnvironment(environment), version,
		sdl, status, schemaHash(sdl), errorsJSON, now)
	if err != nil {
		return nil, validationErrors, err
	}
	id, _ := result.LastInsertId()
	row, err := getSchema(db, project, environment, version, false)
	if err != nil {
		return nil, validationErrors, err
	}
	if row != nil && row.ID == 0 {
		row.ID = id
	}
	return row, validationErrors, nil
}

func publishSchema(db *sql.DB, project, environment string, version int) (*schemaRecord, error) {
	row, err := getSchema(db, project, environment, version, false)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, invalid("schema version not found")
	}
	if len(row.ValidationError) > 0 || row.Status == "invalid" {
		return nil, invalid("schema has validation errors")
	}
	parsed, problems := validateSDL(row.SDL)
	if len(problems) > 0 {
		return nil, invalid("schema has validation errors")
	}
	if _, err := buildStandardSchema(parsed, nil); err != nil {
		return nil, invalid("schema is not executable: %s", err)
	}
	now := nowUTC()
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`UPDATE graphql_schemas SET status='archived' WHERE project_id=? AND environment=? AND status='published'`, project, normalizeEnvironment(environment)); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(`UPDATE graphql_schemas SET status='published', published_at=? WHERE id=?`, now, row.ID); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return getSchema(db, project, environment, version, false)
}

func validateSourceKind(kind string) error {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "database", "tables", "function", "http", "module":
		return nil
	default:
		return invalid("source kind must be database, tables, function, HTTP, or module")
	}
}

func createSource(db *sql.DB, project, name, kind string, config map[string]any) (*sourceRecord, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, invalid("source name is required")
	}
	if err := validateSourceKind(kind); err != nil {
		return nil, err
	}
	if strings.EqualFold(kind, "function") {
		if _, err := functionSecurity(config); err != nil {
			return nil, err
		}
	}
	if strings.EqualFold(kind, "http") {
		if err := validateHTTPSource(config); err != nil {
			return nil, err
		}
	}
	if strings.EqualFold(kind, "module") {
		moduleName, _ := config["module"].(string)
		version := moduleInt(config["version"])
		if !validModuleName(moduleName) || version <= 0 {
			return nil, invalid("module source requires module and a pinned positive version")
		}
		module, findErr := getResolverModule(db, project, moduleName, version, true)
		if findErr != nil {
			return nil, findErr
		}
		if module == nil {
			return nil, invalid("published resolver module %s@%d not found", moduleName, version)
		}
		if _, err := moduleInputMapping(config); err != nil {
			return nil, err
		}
	}
	encoded, err := encodeJSON(config)
	if err != nil {
		return nil, err
	}
	now := nowUTC()
	_, err = db.Exec(`INSERT INTO graphql_sources(project_id,name,kind,config_json,status,created_at,updated_at)
        VALUES(?,?,?,?,?,?,?) ON CONFLICT(project_id,name) DO UPDATE SET kind=excluded.kind, config_json=excluded.config_json, updated_at=excluded.updated_at`,
		project, name, strings.ToLower(kind), encoded, "active", now, now)
	if err != nil {
		return nil, err
	}
	// LastInsertId is not reliable for the UPDATE arm of an SQLite upsert: it
	// may be zero or refer to an earlier insert on the connection. Name is the
	// conflict key and identifies inserts and updates deterministically.
	return getSource(db, project, 0, name)
}

func getSource(db *sql.DB, project string, id int64, name string) (*sourceRecord, error) {
	query := `SELECT id, project_id, name, kind, config_json, status FROM graphql_sources WHERE project_id=?`
	args := []any{project}
	if id > 0 {
		query += " AND id=?"
		args = append(args, id)
	} else {
		query += " AND name=?"
		args = append(args, name)
	}
	var row sourceRecord
	var config string
	if err := db.QueryRow(query, args...).Scan(&row.ID, &row.ProjectID, &row.Name, &row.Kind, &config, &row.Status); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	row.Config = decodeJSON(config)
	return &row, nil
}

func listSources(db *sql.DB, project string) ([]sourceRecord, error) {
	rows, err := db.Query(`SELECT id, project_id, name, kind, config_json, status FROM graphql_sources WHERE project_id=? ORDER BY name`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []sourceRecord
	for rows.Next() {
		var row sourceRecord
		var config string
		if err := rows.Scan(&row.ID, &row.ProjectID, &row.Name, &row.Kind, &config, &row.Status); err != nil {
			return nil, err
		}
		row.Config = decodeJSON(config)
		out = append(out, row)
	}
	return out, rows.Err()
}

func upsertResolver(db *sql.DB, project, parentType, fieldName, operation string, sourceID int64, config map[string]any) (*resolverRecord, error) {
	if strings.TrimSpace(parentType) == "" || strings.TrimSpace(fieldName) == "" {
		return nil, invalid("parent_type and field_name are required")
	}
	if sourceID <= 0 {
		return nil, invalid("source_id is required")
	}
	source, err := getSource(db, project, sourceID, "")
	if err != nil {
		return nil, err
	}
	if source == nil {
		return nil, invalid("source not found in this API")
	}
	if source.Kind == "function" {
		if _, err := functionSecurity(mergeMaps(source.Config, config)); err != nil {
			return nil, err
		}
	}
	if strings.TrimSpace(operation) == "" {
		return nil, invalid("operation is required")
	}
	if source.Kind == "tables" {
		merged := mergeMaps(source.Config, config)
		if err := validateTableRelation(operation, merged); err != nil {
			return nil, err
		}
		if _, err := tablesDistinct(operation, merged, map[string]any{}); err != nil {
			return nil, err
		}
	}
	if source.Kind == "module" {
		if operation != "resolve" && operation != "computed" {
			return nil, invalid("module resolver operation must be resolve")
		}
		if _, err := moduleInputMapping(mergeMaps(source.Config, config)); err != nil {
			return nil, err
		}
	}
	encoded, err := encodeJSON(config)
	if err != nil {
		return nil, err
	}
	now := nowUTC()
	_, err = db.Exec(`INSERT INTO graphql_resolvers(project_id,parent_type,field_name,source_id,operation,config_json,created_at,updated_at)
        VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(project_id,parent_type,field_name) DO UPDATE SET source_id=excluded.source_id, operation=excluded.operation, config_json=excluded.config_json, updated_at=excluded.updated_at`,
		project, parentType, fieldName, sourceID, operation, encoded, now, now)
	if err != nil {
		return nil, err
	}
	return getResolver(db, project, parentType, fieldName)
}

func getResolver(db *sql.DB, project, parentType, fieldName string) (*resolverRecord, error) {
	var row resolverRecord
	var config string
	err := db.QueryRow(`SELECT id, project_id, parent_type, field_name, source_id, operation, config_json
        FROM graphql_resolvers WHERE project_id=? AND parent_type=? AND field_name=?`, project, parentType, fieldName).
		Scan(&row.ID, &row.ProjectID, &row.ParentType, &row.FieldName, &row.SourceID, &row.Operation, &config)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	row.Config = decodeJSON(config)
	return &row, nil
}

func listResolvers(db *sql.DB, project string) ([]resolverRecord, error) {
	rows, err := db.Query(`SELECT id, project_id, parent_type, field_name, source_id, operation, config_json
        FROM graphql_resolvers WHERE project_id=? ORDER BY parent_type, field_name`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []resolverRecord
	for rows.Next() {
		var row resolverRecord
		var config string
		if err := rows.Scan(&row.ID, &row.ProjectID, &row.ParentType, &row.FieldName, &row.SourceID, &row.Operation, &config); err != nil {
			return nil, err
		}
		row.Config = decodeJSON(config)
		out = append(out, row)
	}
	return out, rows.Err()
}

func validateHTTPSource(config map[string]any) error {
	urlValue, _ := config["url"].(string)
	urlValue = strings.TrimSpace(urlValue)
	if urlValue == "" {
		return invalid("HTTP source requires url")
	}
	if !strings.HasPrefix(urlValue, "https://") && !strings.HasPrefix(urlValue, "http://") {
		return invalid("HTTP source url must use http or https")
	}
	return nil
}

func publicSchema(row *schemaRecord) map[string]any {
	if row == nil {
		return nil
	}
	return map[string]any{"id": row.ID, "project_id": publicProjectID(row.ProjectID), "environment": row.Environment, "version": row.Version, "sdl": row.SDL, "status": row.Status, "hash": row.Hash, "validation_errors": row.ValidationError, "created_at": row.CreatedAt, "published_at": row.PublishedAt}
}

func publicSource(row sourceRecord) map[string]any {
	return map[string]any{"id": row.ID, "project_id": publicProjectID(row.ProjectID), "name": row.Name, "kind": row.Kind, "config": row.Config, "status": row.Status}
}

func publicResolver(row resolverRecord, source *sourceRecord) map[string]any {
	result := map[string]any{"id": row.ID, "project_id": publicProjectID(row.ProjectID), "parent_type": row.ParentType, "field_name": row.FieldName, "source_id": row.SourceID, "operation": row.Operation, "config": row.Config}
	if source != nil {
		result["source"] = source.Name
	}
	return result
}

func publicLogs(db *sql.DB, project string, limit int) ([]map[string]any, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := db.Query(`SELECT id, operation_name, operation_type, status_code, duration_ms, error, created_at
        FROM graphql_request_logs WHERE project_id=? ORDER BY id DESC LIMIT ?`, project, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var id, duration int64
		var operationName, operationType, message, created string
		var status int
		if err := rows.Scan(&id, &operationName, &operationType, &status, &duration, &message, &created); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"id": id, "operation_name": operationName, "operation_type": operationType, "status_code": status, "duration_ms": duration, "error": message, "created_at": created})
	}
	return out, rows.Err()
}

func mergeMaps(base, extra map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(extra))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sourceSummary(row sourceRecord) string {
	return fmt.Sprintf("%s:%s", row.Kind, row.Name)
}
