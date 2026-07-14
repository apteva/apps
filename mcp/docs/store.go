package main

// SQLite reads/writes for templates + renders. Plain-rectangle CRUD
// over both tables; no business logic here. Render-time enrichment
// (fetching storage URL etc.) lives in tools.go / handlers.go.

import (
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
	ID           int64           `json:"id"`
	TemplateID   int64           `json:"template_id"`
	TemplateSlug string          `json:"template_slug"`
	OutputFileID string          `json:"output_file_id"`
	OutputName   string          `json:"output_name,omitempty"`
	OutputFolder string          `json:"output_folder,omitempty"`
	DataSnapshot json.RawMessage `json:"data"`
	RenderedBy   string          `json:"rendered_by,omitempty"`
	RenderedAt   string          `json:"rendered_at"`
	Bytes        int64           `json:"bytes,omitempty"`
}

// ─── templates ────────────────────────────────────────────────────────

func listTemplates(db *sql.DB) ([]Template, error) {
	rows, err := db.Query(`
		SELECT id, slug, name, description, body, source_format, output_format,
		       variables_json, default_folder, created_at, updated_at
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
	q := `SELECT id, slug, name, description, body, source_format, output_format,
		         variables_json, default_folder, created_at, updated_at
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
	if err := s.Scan(
		&t.ID, &t.Slug, &t.Name, &t.Description, &t.Body,
		&t.SourceFormat, &t.OutputFormat, &vars, &t.DefaultFolder,
		&t.CreatedAt, &t.UpdatedAt,
	); err != nil {
		return nil, err
	}
	if vars.Valid && vars.String != "" {
		t.VariablesJSON = json.RawMessage(vars.String)
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
	res, err := db.Exec(`
		INSERT INTO templates (slug, name, description, body, source_format, output_format, variables_json, default_folder)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		t.Slug, t.Name, t.Description, t.Body, t.SourceFormat, t.OutputFormat, vars, t.DefaultFolder,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
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
	if t.SourceFormat != "" && t.SourceFormat != "markdown" {
		return errors.New("source_format must be markdown")
	}
	if t.OutputFormat != "" && t.OutputFormat != "pdf" {
		return errors.New("output_format must be pdf")
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
		"name": true, "description": true, "body": true, "default_folder": true,
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
		sets = append(sets, k+" = ?")
		args = append(args, s)
	}
	if len(sets) == 0 {
		return errors.New("no updatable fields provided")
	}
	sets = append(sets, "updated_at = ?")
	args = append(args, time.Now().UTC().Format(time.RFC3339))
	args = append(args, id)
	res, err := db.Exec(`UPDATE templates SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
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
		                     output_folder, data_snapshot, rendered_by, bytes)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		r.TemplateID, r.TemplateSlug, r.OutputFileID, r.OutputName,
		r.OutputFolder, string(r.DataSnapshot), r.RenderedBy, r.Bytes,
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
		        output_folder, rendered_by, rendered_at, bytes
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
	); err != nil {
		return nil, err
	}
	return &r, nil
}

func getRender(db *sql.DB, id int64) (*Render, error) {
	row := db.QueryRow(
		`SELECT id, template_id, template_slug, output_file_id, output_name,
		        output_folder, data_snapshot, rendered_by, rendered_at, bytes
		 FROM renders WHERE id = ?`, id)
	r, err := scanRender(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return r, err
}

func scanRender(s scanner) (*Render, error) {
	var r Render
	var data string
	if err := s.Scan(
		&r.ID, &r.TemplateID, &r.TemplateSlug, &r.OutputFileID, &r.OutputName,
		&r.OutputFolder, &data, &r.RenderedBy, &r.RenderedAt, &r.Bytes,
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
