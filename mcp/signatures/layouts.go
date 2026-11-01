package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Layout is a reusable set of signer roles and field positions. Fields
// point at roles by index, so a layout applies to any envelope whose
// PDF shares the page geometry it was measured on.
type Layout struct {
	ID          int64             `json:"id"`
	Slug        string            `json:"slug"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Recipients  []LayoutRecipient `json:"recipients"`
	Fields      []LayoutField     `json:"fields"`
	CreatedAt   string            `json:"created_at"`
	UpdatedAt   string            `json:"updated_at"`
}

type LayoutRecipient struct {
	Label        string `json:"label"`
	Role         string `json:"role"`
	SigningOrder int    `json:"signing_order"`
}

type LayoutField struct {
	Recipient int     `json:"recipient"`
	FieldType string  `json:"field_type"`
	Page      int     `json:"page"`
	X         float64 `json:"x"`
	Y         float64 `json:"y"`
	Width     float64 `json:"width"`
	Height    float64 `json:"height"`
	Label     string  `json:"label"`
	Required  bool    `json:"required"`
}

var layoutSlugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

var layoutFieldTypes = map[string]bool{"signature": true, "initials": true, "date_signed": true, "text": true, "checkbox": true}

func parseLayoutRecipients(specs []map[string]any) ([]LayoutRecipient, error) {
	if len(specs) == 0 || len(specs) > 50 {
		return nil, errors.New("recipients must contain 1 to 50 roles")
	}
	out := make([]LayoutRecipient, 0, len(specs))
	orders := map[int]bool{}
	for i, spec := range specs {
		r := LayoutRecipient{
			Label:        strings.TrimSpace(stringArg(spec, "label")),
			Role:         strings.ToLower(stringArg(spec, "role")),
			SigningOrder: intArg(spec, "signing_order", 1),
		}
		if r.Label == "" {
			r.Label = strings.TrimSpace(stringArg(spec, "name"))
		}
		if r.Label == "" {
			return nil, fmt.Errorf("recipients[%d].label required", i)
		}
		if r.Role == "" {
			r.Role = "signer"
		}
		if r.Role != "signer" && r.Role != "approver" {
			return nil, fmt.Errorf("recipients[%d].role must be signer or approver", i)
		}
		if r.SigningOrder < 1 {
			return nil, fmt.Errorf("recipients[%d].signing_order must be positive", i)
		}
		orders[r.SigningOrder] = true
		out = append(out, r)
	}
	for order := 1; order <= len(orders); order++ {
		if !orders[order] {
			return nil, errors.New("signing_order values must form a contiguous sequence starting at 1")
		}
	}
	return out, nil
}

func parseLayoutFields(specs []map[string]any, recipients []LayoutRecipient) ([]LayoutField, error) {
	if len(specs) == 0 || len(specs) > 500 {
		return nil, errors.New("fields must contain 1 to 500 entries")
	}
	out := make([]LayoutField, 0, len(specs))
	hasSignature := map[int]bool{}
	for i, spec := range specs {
		f := LayoutField{
			Recipient: -1,
			FieldType: strings.ToLower(stringArg(spec, "field_type")),
			Page:      intArg(spec, "page", 0),
			Label:     stringArg(spec, "label"),
			Required:  boolArg(spec, "required", true),
		}
		// intArg treats 0 as missing, but 0 is the first role.
		if _, ok := spec["recipient"]; ok {
			f.Recipient = int(int64Arg(spec, "recipient"))
		}
		var xok, yok, wok, hok bool
		f.X, xok = numberArg(spec, "x")
		f.Y, yok = numberArg(spec, "y")
		f.Width, wok = numberArg(spec, "width")
		f.Height, hok = numberArg(spec, "height")
		if f.Recipient < 0 || f.Recipient >= len(recipients) {
			return nil, fmt.Errorf("fields[%d].recipient must be a role index from 0 to %d", i, len(recipients)-1)
		}
		if !layoutFieldTypes[f.FieldType] {
			return nil, fmt.Errorf("fields[%d].field_type is invalid", i)
		}
		if f.Page < 1 {
			return nil, fmt.Errorf("fields[%d].page must be positive", i)
		}
		if !xok || !yok || !wok || !hok || f.X < 0 || f.Y < 0 || f.Width <= 0 || f.Height <= 0 || f.X+f.Width > 1 || f.Y+f.Height > 1 {
			return nil, fmt.Errorf("fields[%d] coordinates must be normalized inside the page", i)
		}
		if f.FieldType == "signature" {
			hasSignature[f.Recipient] = true
		}
		out = append(out, f)
	}
	for i, r := range recipients {
		if r.Role == "signer" && !hasSignature[i] {
			return nil, fmt.Errorf("signer role %q needs a signature field", r.Label)
		}
	}
	return out, nil
}

func normalizeLayoutSlug(slug, name string) (string, error) {
	slug = strings.ToLower(strings.TrimSpace(slug))
	if slug == "" {
		slug = strings.ToLower(safeFilename(name))
	}
	if !layoutSlugPattern.MatchString(slug) {
		return "", errors.New("slug must be 1-64 lowercase letters, digits or dashes")
	}
	return slug, nil
}

// saveLayout creates or replaces the project's layout with this slug.
func saveLayout(db *sql.DB, projectID, slug, name, description string, recipients []LayoutRecipient, fields []LayoutField) (*Layout, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("name required")
	}
	slug, err := normalizeLayoutSlug(slug, name)
	if err != nil {
		return nil, err
	}
	recipientsJSON, err := json.Marshal(recipients)
	if err != nil {
		return nil, err
	}
	fieldsJSON, err := json.Marshal(fields)
	if err != nil {
		return nil, err
	}
	now := nowUTC()
	if _, err := db.Exec(`
		INSERT INTO layouts (project_id, slug, name, description, recipients, fields, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(project_id, slug) DO UPDATE SET name = excluded.name, description = excluded.description,
			recipients = excluded.recipients, fields = excluded.fields, updated_at = excluded.updated_at`,
		projectID, slug, name, strings.TrimSpace(description), string(recipientsJSON), string(fieldsJSON), now, now); err != nil {
		return nil, err
	}
	return getLayout(db, projectID, 0, slug)
}

func getLayout(db *sql.DB, projectID string, id int64, slug string) (*Layout, error) {
	query := `SELECT id, slug, name, description, recipients, fields, created_at, updated_at FROM layouts WHERE project_id = ? AND `
	var row *sql.Row
	if id != 0 {
		row = db.QueryRow(query+`id = ?`, projectID, id)
	} else {
		row = db.QueryRow(query+`slug = ?`, projectID, strings.ToLower(strings.TrimSpace(slug)))
	}
	layout, err := scanLayout(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errors.New("layout not found")
	}
	return layout, err
}

func scanLayout(s scanner) (*Layout, error) {
	var l Layout
	var recipientsJSON, fieldsJSON string
	if err := s.Scan(&l.ID, &l.Slug, &l.Name, &l.Description, &recipientsJSON, &fieldsJSON, &l.CreatedAt, &l.UpdatedAt); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(recipientsJSON), &l.Recipients); err != nil {
		return nil, fmt.Errorf("layout %d recipients: %w", l.ID, err)
	}
	if err := json.Unmarshal([]byte(fieldsJSON), &l.Fields); err != nil {
		return nil, fmt.Errorf("layout %d fields: %w", l.ID, err)
	}
	return &l, nil
}

func listLayouts(db *sql.DB, projectID string) ([]Layout, error) {
	rows, err := db.Query(`SELECT id, slug, name, description, recipients, fields, created_at, updated_at FROM layouts WHERE project_id = ? ORDER BY name`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Layout{}
	for rows.Next() {
		l, err := scanLayout(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *l)
	}
	return out, rows.Err()
}

func deleteLayout(db *sql.DB, projectID string, id int64) error {
	res, err := db.Exec(`DELETE FROM layouts WHERE project_id = ? AND id = ?`, projectID, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return errors.New("layout not found")
	}
	return nil
}

// layoutFromEnvelope snapshots a draft's recipients (names become role
// labels) and field positions.
func layoutFromEnvelope(db *sql.DB, projectID string, envelopeID int64) ([]LayoutRecipient, []LayoutField, error) {
	recipients, err := listRecipients(db, projectID, envelopeID)
	if err != nil {
		return nil, nil, err
	}
	if len(recipients) == 0 {
		return nil, nil, errors.New("envelope has no recipients")
	}
	fields, err := listFields(db, projectID, envelopeID, 0)
	if err != nil {
		return nil, nil, err
	}
	if len(fields) == 0 {
		return nil, nil, errors.New("envelope has no fields")
	}
	index := map[int64]int{}
	roles := make([]LayoutRecipient, len(recipients))
	for i, r := range recipients {
		index[r.ID] = i
		roles[i] = LayoutRecipient{Label: r.Name, Role: r.Role, SigningOrder: r.SigningOrder}
	}
	out := make([]LayoutField, 0, len(fields))
	for _, f := range fields {
		out = append(out, LayoutField{Recipient: index[f.RecipientID], FieldType: f.FieldType, Page: f.Page, X: f.X, Y: f.Y, Width: f.Width, Height: f.Height, Label: f.Label, Required: f.Required})
	}
	return roles, out, nil
}

// applyLayout sets a draft envelope's recipients and fields from a
// layout. people[i] names role i; a missing name falls back to the
// role label.
func applyLayout(db *sql.DB, projectID string, envelopeID int64, layout *Layout, people []map[string]any) ([]Recipient, []Field, error) {
	if len(people) > len(layout.Recipients) {
		return nil, nil, fmt.Errorf("layout %q has %d roles but %d recipients were given", layout.Slug, len(layout.Recipients), len(people))
	}
	specs := make([]map[string]any, len(layout.Recipients))
	for i, role := range layout.Recipients {
		spec := map[string]any{"name": role.Label, "role": role.Role, "signing_order": role.SigningOrder}
		if i < len(people) {
			if name := strings.TrimSpace(stringArg(people[i], "name")); name != "" {
				spec["name"] = name
			}
			spec["email"] = stringArg(people[i], "email")
		}
		specs[i] = spec
	}
	recipients, err := setRecipients(db, projectID, envelopeID, specs)
	if err != nil {
		return nil, nil, err
	}
	// setRecipients stores roles stable-sorted by signing_order and
	// lists them in that position order; replay the sort to map each
	// role index to its recipient row.
	if len(recipients) != len(layout.Recipients) {
		return nil, nil, errors.New("recipient count does not match the layout")
	}
	positions := make([]int, len(layout.Recipients))
	for i := range positions {
		positions[i] = i
	}
	sort.SliceStable(positions, func(a, b int) bool {
		return layout.Recipients[positions[a]].SigningOrder < layout.Recipients[positions[b]].SigningOrder
	})
	roleIDs := make([]int64, len(layout.Recipients))
	for position, role := range positions {
		roleIDs[role] = recipients[position].ID
	}
	fieldSpecs := make([]map[string]any, len(layout.Fields))
	for i, f := range layout.Fields {
		fieldSpecs[i] = map[string]any{
			"recipient_id": roleIDs[f.Recipient], "field_type": f.FieldType, "page": f.Page,
			"x": f.X, "y": f.Y, "width": f.Width, "height": f.Height, "label": f.Label, "required": f.Required,
		}
	}
	fields, err := setFields(db, projectID, envelopeID, fieldSpecs)
	if err != nil {
		return nil, nil, err
	}
	_ = addAudit(db, envelopeID, projectID, 0, "layout.applied", map[string]any{"layout": layout.Slug})
	return recipients, fields, nil
}
