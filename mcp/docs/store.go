package main

// SQLite reads/writes for templates + renders. Plain-rectangle CRUD
// over both tables; no business logic here. Render-time enrichment
// (fetching storage URL etc.) lives in tools.go / handlers.go.

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Template — operator-authored row in the templates table.
type Template struct {
	ID            int64           `json:"id"`
	Slug          string          `json:"slug"`
	Name          string          `json:"name"`
	Description   string          `json:"description,omitempty"`
	Body          string          `json:"body"`
	Stylesheet    string          `json:"stylesheet,omitempty"`
	SettingsJSON  json.RawMessage `json:"settings,omitempty"`
	SourceFormat  string          `json:"source_format"`
	OutputFormat  string          `json:"output_format"`
	VariablesJSON json.RawMessage `json:"variables,omitempty"`
	DefaultFolder string          `json:"default_folder,omitempty"`
	CreatedAt     string          `json:"created_at,omitempty"`
	UpdatedAt     string          `json:"updated_at,omitempty"`
}

var templateSlugRE = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

// Render — one audit row.
type Render struct {
	ID                 int64             `json:"id"`
	TemplateID         int64             `json:"template_id"`
	TemplateSlug       string            `json:"template_slug"`
	OutputFileID       string            `json:"output_file_id"`
	OutputName         string            `json:"output_name,omitempty"`
	OutputFolder       string            `json:"output_folder,omitempty"`
	DataSnapshot       json.RawMessage   `json:"data"`
	RenderedBy         string            `json:"rendered_by,omitempty"`
	RenderedAt         string            `json:"rendered_at"`
	Bytes              int64             `json:"bytes,omitempty"`
	TemplateRevisionID int64             `json:"template_revision_id,omitempty"`
	SourceHash         string            `json:"source_hash,omitempty"`
	RendererVersion    string            `json:"renderer_version,omitempty"`
	TemplateRevision   *TemplateRevision `json:"template_revision,omitempty"`
}

type TemplateRevision struct {
	ID             int64           `json:"id"`
	TemplateID     int64           `json:"template_id"`
	RevisionNumber int64           `json:"revision_number"`
	SourceFormat   string          `json:"source_format"`
	Body           string          `json:"body"`
	Stylesheet     string          `json:"stylesheet,omitempty"`
	SettingsJSON   json.RawMessage `json:"settings,omitempty"`
	SourceHash     string          `json:"source_hash"`
	CreatedAt      string          `json:"created_at,omitempty"`
}

// ─── templates ────────────────────────────────────────────────────────

func listTemplates(db *sql.DB) ([]Template, error) {
	rows, err := db.Query(`
		SELECT id, slug, name, description, body, stylesheet, settings_json,
		       source_format, output_format, variables_json, default_folder, created_at, updated_at
		FROM templates ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Template{}
	for rows.Next() {
		t, err := scanTemplate(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

// listTemplateSummaries intentionally excludes body and variables_json.
// Lists stay cheap even when operators keep long-form lead magnets in the
// same install; callers fetch one full template on selection.
func listTemplateSummaries(db *sql.DB) ([]Template, error) {
	rows, err := db.Query(`
		SELECT id, slug, name, description, source_format, output_format,
		       default_folder, created_at, updated_at
		FROM templates ORDER BY name, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Template{}
	for rows.Next() {
		var t Template
		if err := rows.Scan(
			&t.ID, &t.Slug, &t.Name, &t.Description, &t.SourceFormat,
			&t.OutputFormat, &t.DefaultFolder, &t.CreatedAt, &t.UpdatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// getTemplate accepts either id (>0) or slug (non-empty). Returns
// nil + nil when neither matches anything (caller decides whether
// that's a 404 or a found=false response).
func getTemplate(db *sql.DB, id int64, slug string) (*Template, error) {
	if id <= 0 && slug == "" {
		return nil, errors.New("id or slug required")
	}
	q := `SELECT id, slug, name, description, body, stylesheet, settings_json,
		         source_format, output_format, variables_json, default_folder, created_at, updated_at
	      FROM templates WHERE `
	var args []any
	if id > 0 {
		q += "id = ?"
		args = []any{id}
	} else {
		q += "slug = ?"
		args = []any{slug}
	}
	row := db.QueryRow(q, args...)
	t, err := scanTemplate(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return t, err
}

type scanner interface{ Scan(...any) error }

func scanTemplate(s scanner) (*Template, error) {
	var t Template
	var vars sql.NullString
	var settings sql.NullString
	if err := s.Scan(
		&t.ID, &t.Slug, &t.Name, &t.Description, &t.Body, &t.Stylesheet, &settings,
		&t.SourceFormat, &t.OutputFormat, &vars, &t.DefaultFolder,
		&t.CreatedAt, &t.UpdatedAt,
	); err != nil {
		return nil, err
	}
	if vars.Valid && vars.String != "" {
		t.VariablesJSON = json.RawMessage(vars.String)
	}
	if settings.Valid && settings.String != "" {
		t.SettingsJSON = json.RawMessage(settings.String)
	}
	return &t, nil
}

func createTemplate(db *sql.DB, t *Template) (int64, error) {
	if err := validateTemplate(t); err != nil {
		return 0, err
	}
	if t.SourceFormat == "" {
		t.SourceFormat = "markdown"
	}
	if t.OutputFormat == "" {
		t.OutputFormat = "pdf"
	}
	vars := string(t.VariablesJSON)
	if vars == "" {
		vars = "[]"
	}
	settings := string(t.SettingsJSON)
	if settings == "" {
		settings = "{}"
	}
	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`
		INSERT INTO templates (slug, name, description, body, stylesheet, settings_json,
		                       source_format, output_format, variables_json, default_folder)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.Slug, t.Name, t.Description, t.Body, t.Stylesheet, settings,
		t.SourceFormat, t.OutputFormat, vars, t.DefaultFolder,
	)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	t.ID = id
	if _, err := insertTemplateRevisionTx(tx, t); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return id, nil
}

func validateTemplate(t *Template) error {
	if t == nil || strings.TrimSpace(t.Slug) == "" || strings.TrimSpace(t.Name) == "" || strings.TrimSpace(t.Body) == "" {
		return errors.New("slug, name, body required")
	}
	if !templateSlugRE.MatchString(t.Slug) {
		return errors.New("slug must contain lowercase letters, numbers, and single hyphens only")
	}
	if len(t.Slug) > 80 || len(t.Name) > 200 || len(t.Description) > 2000 || len(t.DefaultFolder) > 500 {
		return errors.New("template metadata exceeds size limits")
	}
	if t.SourceFormat != "" && t.SourceFormat != "markdown" && t.SourceFormat != "html" {
		return errors.New("source_format must be markdown or html")
	}
	if t.OutputFormat != "" && t.OutputFormat != "pdf" {
		return errors.New("output_format must be pdf")
	}
	if len(t.SettingsJSON) > 0 {
		if _, err := parseDocumentSettings(t.SettingsJSON); err != nil {
			return err
		}
	}
	return nil
}

// updateTemplate applies a partial update. fields keys are the Go
// struct field names mapped from input ("name", "description", "body",
// "default_folder") — anything else is silently ignored. Returns
// sql.ErrNoRows when the id doesn't exist so callers can 404.
func updateTemplate(db *sql.DB, id int64, fields map[string]any) error {
	if id <= 0 {
		return errors.New("id required")
	}
	allowed := map[string]bool{
		"name": true, "description": true, "body": true, "stylesheet": true,
		"settings_json": true, "source_format": true, "default_folder": true,
	}
	sets := []string{}
	args := []any{}
	for k, v := range fields {
		if !allowed[k] {
			return fmt.Errorf("field %q cannot be updated", k)
		}
		s, ok := v.(string)
		if !ok {
			return fmt.Errorf("%s must be a string", k)
		}
		if k == "name" && strings.TrimSpace(s) == "" {
			return errors.New("name cannot be empty")
		}
		if k == "body" && strings.TrimSpace(s) == "" {
			return errors.New("body cannot be empty")
		}
		if k == "source_format" && s != "markdown" && s != "html" {
			return errors.New("source_format must be markdown or html")
		}
		if k == "settings_json" && !json.Valid([]byte(s)) {
			return errors.New("settings must be valid JSON")
		}
		if k == "settings_json" {
			if _, err := parseDocumentSettings(json.RawMessage(s)); err != nil {
				return err
			}
		}
		sets = append(sets, k+" = ?")
		args = append(args, s)
	}
	if len(sets) == 0 {
		return errors.New("no updatable fields provided")
	}
	sets = append(sets, "updated_at = ?")
	args = append(args, time.Now().UTC().Format(time.RFC3339))
	args = append(args, id)
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE templates SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	if fieldsAffectRevision(fields) {
		t, err := getTemplateTx(tx, id)
		if err != nil {
			return err
		}
		if _, err := insertTemplateRevisionTx(tx, t); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func fieldsAffectRevision(fields map[string]any) bool {
	for _, key := range []string{"body", "stylesheet", "settings_json", "source_format"} {
		if _, ok := fields[key]; ok {
			return true
		}
	}
	return false
}

func getTemplateTx(tx *sql.Tx, id int64) (*Template, error) {
	row := tx.QueryRow(`SELECT id, slug, name, description, body, stylesheet, settings_json,
		source_format, output_format, variables_json, default_folder, created_at, updated_at
		FROM templates WHERE id = ?`, id)
	return scanTemplate(row)
}

func templateSourceHash(t *Template) string {
	settings := string(t.SettingsJSON)
	if settings == "" {
		settings = "{}"
	}
	sum := sha256.Sum256([]byte(t.SourceFormat + "\x00" + t.Body + "\x00" + t.Stylesheet + "\x00" + settings))
	return fmt.Sprintf("%x", sum[:])
}

func insertTemplateRevisionTx(tx *sql.Tx, t *Template) (*TemplateRevision, error) {
	if t == nil || t.ID <= 0 {
		return nil, errors.New("template revision requires template id")
	}
	hash := templateSourceHash(t)
	var current TemplateRevision
	var settings string
	err := tx.QueryRow(`SELECT id, template_id, revision_number, source_format, body,
		stylesheet, settings_json, source_hash, created_at
		FROM template_revisions WHERE template_id = ? ORDER BY revision_number DESC LIMIT 1`, t.ID).
		Scan(&current.ID, &current.TemplateID, &current.RevisionNumber, &current.SourceFormat,
			&current.Body, &current.Stylesheet, &settings, &current.SourceHash, &current.CreatedAt)
	if err == nil && current.SourceHash == hash {
		current.SettingsJSON = json.RawMessage(settings)
		return &current, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	next := current.RevisionNumber + 1
	settings = string(t.SettingsJSON)
	if settings == "" {
		settings = "{}"
	}
	res, err := tx.Exec(`INSERT INTO template_revisions
		(template_id, revision_number, source_format, body, stylesheet, settings_json, source_hash)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		t.ID, next, t.SourceFormat, t.Body, t.Stylesheet, settings, hash)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return &TemplateRevision{ID: id, TemplateID: t.ID, RevisionNumber: next,
		SourceFormat: t.SourceFormat, Body: t.Body, Stylesheet: t.Stylesheet,
		SettingsJSON: json.RawMessage(settings), SourceHash: hash}, nil
}

func ensureTemplateRevision(db *sql.DB, t *Template) (*TemplateRevision, error) {
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rev, err := insertTemplateRevisionTx(tx, t)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return rev, nil
}

func deleteTemplate(db *sql.DB, id int64) error {
	res, err := db.Exec(`DELETE FROM templates WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// ─── renders ──────────────────────────────────────────────────────────

func insertRender(db *sql.DB, r *Render) (int64, error) {
	if r.TemplateID <= 0 || r.OutputFileID == "" {
		return 0, errors.New("template_id + output_file_id required")
	}
	if len(r.DataSnapshot) == 0 {
		r.DataSnapshot = json.RawMessage("{}")
	}
	res, err := db.Exec(`
		INSERT INTO renders (template_id, template_slug, output_file_id, output_name,
		                     output_folder, data_snapshot, rendered_by, bytes,
		                     template_revision_id, source_hash, renderer_version)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.TemplateID, r.TemplateSlug, r.OutputFileID, r.OutputName,
		r.OutputFolder, string(r.DataSnapshot), r.RenderedBy, r.Bytes,
		r.TemplateRevisionID, r.SourceHash, r.RendererVersion,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

type RenderFilters struct {
	TemplateID int64
	Since      string // RFC3339; empty = no since filter
	Limit      int
	Offset     int
}

func listRenders(db *sql.DB, f RenderFilters) ([]Render, error) {
	clauses := []string{"1 = 1"}
	args := []any{}
	if f.TemplateID > 0 {
		clauses = append(clauses, "template_id = ?")
		args = append(args, f.TemplateID)
	}
	if f.Since != "" {
		parsed, err := time.Parse(time.RFC3339, f.Since)
		if err != nil {
			return nil, fmt.Errorf("since must be RFC3339: %w", err)
		}
		clauses = append(clauses, "datetime(rendered_at) >= datetime(?)")
		args = append(args, parsed.UTC().Format(time.RFC3339))
	}
	limit := 50
	if f.Limit > 0 && f.Limit <= 500 {
		limit = f.Limit
	}
	offset := f.Offset
	if offset < 0 {
		offset = 0
	}
	args = append(args, limit, offset)
	rows, err := db.Query(
		`SELECT id, template_id, template_slug, output_file_id, output_name,
		        output_folder, rendered_by, rendered_at, bytes,
		        template_revision_id, source_hash, renderer_version
		 FROM renders WHERE `+strings.Join(clauses, " AND ")+
			` ORDER BY rendered_at DESC, id DESC LIMIT ? OFFSET ?`,
		args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Render{}
	for rows.Next() {
		r, err := scanRenderSummary(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

func scanRenderSummary(s scanner) (*Render, error) {
	var r Render
	if err := s.Scan(
		&r.ID, &r.TemplateID, &r.TemplateSlug, &r.OutputFileID, &r.OutputName,
		&r.OutputFolder, &r.RenderedBy, &r.RenderedAt, &r.Bytes,
		&r.TemplateRevisionID, &r.SourceHash, &r.RendererVersion,
	); err != nil {
		return nil, err
	}
	return &r, nil
}

func getRender(db *sql.DB, id int64) (*Render, error) {
	row := db.QueryRow(
		`SELECT id, template_id, template_slug, output_file_id, output_name,
		        output_folder, data_snapshot, rendered_by, rendered_at, bytes,
		        template_revision_id, source_hash, renderer_version
		 FROM renders WHERE id = ?`, id)
	r, err := scanRender(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err == nil && r.TemplateRevisionID > 0 {
		r.TemplateRevision, err = getTemplateRevision(db, r.TemplateRevisionID)
	}
	return r, err
}

func getTemplateRevision(db *sql.DB, id int64) (*TemplateRevision, error) {
	var revision TemplateRevision
	var settings string
	err := db.QueryRow(`SELECT id, template_id, revision_number, source_format, body,
		stylesheet, settings_json, source_hash, created_at
		FROM template_revisions WHERE id = ?`, id).
		Scan(&revision.ID, &revision.TemplateID, &revision.RevisionNumber, &revision.SourceFormat,
			&revision.Body, &revision.Stylesheet, &settings, &revision.SourceHash, &revision.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	revision.SettingsJSON = json.RawMessage(settings)
	return &revision, nil
}

func scanRender(s scanner) (*Render, error) {
	var r Render
	var data string
	if err := s.Scan(
		&r.ID, &r.TemplateID, &r.TemplateSlug, &r.OutputFileID, &r.OutputName,
		&r.OutputFolder, &data, &r.RenderedBy, &r.RenderedAt, &r.Bytes,
		&r.TemplateRevisionID, &r.SourceHash, &r.RendererVersion,
	); err != nil {
		return nil, err
	}
	r.DataSnapshot = json.RawMessage(data)
	return &r, nil
}

// errSqlNoRows is the alias handlers.go uses to detect "no row
// matched" without dragging database/sql into its imports.
var errSqlNoRows = sql.ErrNoRows

// configIntDefault parses a numeric config value, falling back to
// def when blank or unparseable. Used by the audit-prune worker
// schedule and a few render-time knobs.
func configIntDefault(raw string, def int) int {
	if raw == "" {
		return def
	}
	var n int
	if _, err := fmt.Sscanf(raw, "%d", &n); err != nil {
		return def
	}
	return n
}
