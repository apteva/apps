package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type Testimonial struct {
	ID              int64          `json:"id"`
	ProjectID       string         `json:"project_id,omitempty"`
	Status          string         `json:"status"`
	Kind            string         `json:"kind"`
	Source          string         `json:"source"`
	Title           string         `json:"title"`
	Quote           string         `json:"quote"`
	Body            string         `json:"body"`
	Rating          *int           `json:"rating,omitempty"`
	AuthorName      string         `json:"author_name"`
	AuthorRole      string         `json:"author_role"`
	AuthorCompany   string         `json:"author_company"`
	AuthorEmail     string         `json:"author_email"`
	MediaFileID     string         `json:"media_file_id"`
	MediaURL        string         `json:"media_url"`
	ConsentStatus   string         `json:"consent_status"`
	PermissionScope string         `json:"permission_scope"`
	Tags            []string       `json:"tags"`
	Metadata        map[string]any `json:"metadata"`
	SubmittedAt     string         `json:"submitted_at,omitempty"`
	ApprovedAt      string         `json:"approved_at,omitempty"`
	PublishedAt     string         `json:"published_at,omitempty"`
	CreatedAt       string         `json:"created_at,omitempty"`
	UpdatedAt       string         `json:"updated_at,omitempty"`
}

type TestimonialFilter struct {
	Status        string
	Kind          string
	Source        string
	Tag           string
	Q             string
	PublishedOnly bool
	Limit         int
}

func createTestimonial(db *sql.DB, projectID string, t *Testimonial) (*Testimonial, error) {
	if err := normalizeTestimonial(t); err != nil {
		return nil, err
	}
	tagsJSON, metadataJSON := encodeJSONFields(t.Tags, t.Metadata)
	ts := statusTimestampValues(t.Status)
	res, err := db.Exec(`
		INSERT INTO testimonials (
			project_id, status, kind, source, title, quote, body, rating,
			author_name, author_role, author_company, author_email,
			media_file_id, media_url, consent_status, permission_scope,
			tags_json, metadata_json, submitted_at, approved_at, published_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		projectID, t.Status, t.Kind, t.Source, t.Title, t.Quote, t.Body, nullableRating(t.Rating),
		t.AuthorName, t.AuthorRole, t.AuthorCompany, t.AuthorEmail,
		t.MediaFileID, t.MediaURL, t.ConsentStatus, t.PermissionScope,
		tagsJSON, metadataJSON, ts.submittedAt, ts.approvedAt, ts.publishedAt)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return getTestimonial(db, projectID, id)
}

func listTestimonials(db *sql.DB, projectID string, f TestimonialFilter) ([]Testimonial, error) {
	if f.PublishedOnly {
		f.Status = "published"
	}
	if f.Status != "" && !validStatus(f.Status) {
		return nil, fmt.Errorf("invalid status %q", f.Status)
	}
	if f.Kind != "" && !validKind(f.Kind) {
		return nil, fmt.Errorf("invalid kind %q", f.Kind)
	}
	clauses := []string{"project_id = ?"}
	args := []any{projectID}
	if f.Status != "" {
		clauses = append(clauses, "status = ?")
		args = append(args, f.Status)
	}
	if f.Kind != "" {
		clauses = append(clauses, "kind = ?")
		args = append(args, f.Kind)
	}
	if f.Source != "" {
		clauses = append(clauses, "source = ?")
		args = append(args, strings.TrimSpace(f.Source))
	}
	if f.Tag != "" {
		clauses = append(clauses, "tags_json LIKE ?")
		args = append(args, "%"+strings.TrimSpace(f.Tag)+"%")
	}
	if strings.TrimSpace(f.Q) != "" {
		q := "%" + strings.TrimSpace(f.Q) + "%"
		clauses = append(clauses, "(title LIKE ? OR quote LIKE ? OR body LIKE ? OR author_name LIKE ? OR author_company LIKE ?)")
		args = append(args, q, q, q, q, q)
	}
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 100
	}
	args = append(args, f.Limit)
	rows, err := db.Query(`
		SELECT id, project_id, status, kind, source, title, quote, body, rating,
		       author_name, author_role, author_company, author_email,
		       media_file_id, media_url, consent_status, permission_scope,
		       tags_json, metadata_json, COALESCE(submitted_at, ''), COALESCE(approved_at, ''),
		       COALESCE(published_at, ''), created_at, updated_at
		FROM testimonials
		WHERE `+strings.Join(clauses, " AND ")+`
		ORDER BY updated_at DESC, id DESC
		LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Testimonial{}
	for rows.Next() {
		t, err := scanTestimonial(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

func getTestimonial(db *sql.DB, projectID string, id int64) (*Testimonial, error) {
	t, err := scanTestimonial(db.QueryRow(`
		SELECT id, project_id, status, kind, source, title, quote, body, rating,
		       author_name, author_role, author_company, author_email,
		       media_file_id, media_url, consent_status, permission_scope,
		       tags_json, metadata_json, COALESCE(submitted_at, ''), COALESCE(approved_at, ''),
		       COALESCE(published_at, ''), created_at, updated_at
		FROM testimonials
		WHERE id = ? AND project_id = ?`, id, projectID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return t, err
}

func updateTestimonial(db *sql.DB, projectID string, id int64, fields map[string]any) (*Testimonial, error) {
	sets, args := []string{}, []any{}
	for _, key := range []string{
		"status", "kind", "source", "title", "quote", "body", "rating",
		"author_name", "author_role", "author_company", "author_email",
		"media_file_id", "media_url", "consent_status", "permission_scope",
		"tags", "metadata",
	} {
		v, ok := fields[key]
		if !ok {
			continue
		}
		switch key {
		case "status":
			s := strings.TrimSpace(fmt.Sprint(v))
			if !validStatus(s) {
				return nil, fmt.Errorf("invalid status %q", s)
			}
			sets = append(sets, "status = ?")
			args = append(args, s)
			sets, args = appendStatusTimestampSets(sets, args, s)
		case "kind":
			s := strings.TrimSpace(fmt.Sprint(v))
			if !validKind(s) {
				return nil, fmt.Errorf("invalid kind %q", s)
			}
			sets = append(sets, "kind = ?")
			args = append(args, s)
		case "rating":
			if v == nil {
				sets = append(sets, "rating = NULL")
				continue
			}
			n, ok := intFromAny(v)
			if !ok {
				return nil, errors.New("rating must be an integer")
			}
			if n < 1 || n > 5 {
				return nil, errors.New("rating must be between 1 and 5")
			}
			sets = append(sets, "rating = ?")
			args = append(args, n)
		case "consent_status":
			s := strings.TrimSpace(fmt.Sprint(v))
			if !validConsent(s) {
				return nil, fmt.Errorf("invalid consent_status %q", s)
			}
			sets = append(sets, "consent_status = ?")
			args = append(args, s)
		case "permission_scope":
			s := strings.TrimSpace(fmt.Sprint(v))
			if !validPermission(s) {
				return nil, fmt.Errorf("invalid permission_scope %q", s)
			}
			sets = append(sets, "permission_scope = ?")
			args = append(args, s)
		case "tags":
			tags := stringSliceArg(fields, "tags")
			b, _ := json.Marshal(tags)
			sets = append(sets, "tags_json = ?")
			args = append(args, string(b))
		case "metadata":
			meta, ok := mapArg(fields, "metadata")
			if !ok {
				meta = map[string]any{}
			}
			b, _ := json.Marshal(meta)
			sets = append(sets, "metadata_json = ?")
			args = append(args, string(b))
		default:
			sets = append(sets, key+" = ?")
			args = append(args, strings.TrimSpace(fmt.Sprint(v)))
		}
	}
	if len(sets) == 0 {
		return nil, errors.New("no updatable fields provided")
	}
	sets = append(sets, "updated_at = ?")
	args = append(args, now(), id, projectID)
	res, err := db.Exec(`UPDATE testimonials SET `+strings.Join(sets, ", ")+` WHERE id = ? AND project_id = ?`, args...)
	if err != nil {
		return nil, err
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		return nil, errNotFound
	}
	return getTestimonial(db, projectID, id)
}

func setTestimonialStatus(db *sql.DB, projectID string, id int64, status string) (*Testimonial, error) {
	return updateTestimonial(db, projectID, id, map[string]any{"status": status})
}

func deleteTestimonial(db *sql.DB, projectID string, id int64, hard bool) error {
	if hard {
		res, err := db.Exec(`DELETE FROM testimonials WHERE id = ? AND project_id = ?`, id, projectID)
		if err != nil {
			return err
		}
		if rows, _ := res.RowsAffected(); rows == 0 {
			return errNotFound
		}
		return nil
	}
	_, err := setTestimonialStatus(db, projectID, id, "archived")
	return err
}

type rowScanner interface{ Scan(...any) error }

func scanTestimonial(s rowScanner) (*Testimonial, error) {
	var t Testimonial
	var rating sql.NullInt64
	var tagsJSON, metadataJSON string
	if err := s.Scan(
		&t.ID, &t.ProjectID, &t.Status, &t.Kind, &t.Source, &t.Title, &t.Quote, &t.Body, &rating,
		&t.AuthorName, &t.AuthorRole, &t.AuthorCompany, &t.AuthorEmail,
		&t.MediaFileID, &t.MediaURL, &t.ConsentStatus, &t.PermissionScope,
		&tagsJSON, &metadataJSON, &t.SubmittedAt, &t.ApprovedAt, &t.PublishedAt,
		&t.CreatedAt, &t.UpdatedAt,
	); err != nil {
		return nil, err
	}
	if rating.Valid {
		n := int(rating.Int64)
		t.Rating = &n
	}
	_ = json.Unmarshal([]byte(tagsJSON), &t.Tags)
	if t.Tags == nil {
		t.Tags = []string{}
	}
	_ = json.Unmarshal([]byte(metadataJSON), &t.Metadata)
	if t.Metadata == nil {
		t.Metadata = map[string]any{}
	}
	return &t, nil
}

func normalizeTestimonial(t *Testimonial) error {
	t.Status = defaultString(t.Status, "draft")
	t.Kind = defaultString(t.Kind, "text")
	t.Source = defaultString(t.Source, "manual")
	t.ConsentStatus = defaultString(t.ConsentStatus, "unknown")
	t.PermissionScope = defaultString(t.PermissionScope, "internal")
	if !validStatus(t.Status) {
		return fmt.Errorf("invalid status %q", t.Status)
	}
	if !validKind(t.Kind) {
		return fmt.Errorf("invalid kind %q", t.Kind)
	}
	if !validConsent(t.ConsentStatus) {
		return fmt.Errorf("invalid consent_status %q", t.ConsentStatus)
	}
	if !validPermission(t.PermissionScope) {
		return fmt.Errorf("invalid permission_scope %q", t.PermissionScope)
	}
	if t.Rating != nil && (*t.Rating < 1 || *t.Rating > 5) {
		return errors.New("rating must be between 1 and 5")
	}
	t.Title = strings.TrimSpace(t.Title)
	t.Quote = strings.TrimSpace(t.Quote)
	t.Body = strings.TrimSpace(t.Body)
	t.AuthorName = strings.TrimSpace(t.AuthorName)
	t.AuthorRole = strings.TrimSpace(t.AuthorRole)
	t.AuthorCompany = strings.TrimSpace(t.AuthorCompany)
	t.AuthorEmail = strings.TrimSpace(t.AuthorEmail)
	t.MediaFileID = strings.TrimSpace(t.MediaFileID)
	t.MediaURL = strings.TrimSpace(t.MediaURL)
	if t.Title == "" && t.Quote == "" && t.Body == "" {
		return errors.New("title, quote, or body required")
	}
	if t.Tags == nil {
		t.Tags = []string{}
	}
	if t.Metadata == nil {
		t.Metadata = map[string]any{}
	}
	return nil
}

func encodeJSONFields(tags []string, metadata map[string]any) (string, string) {
	if tags == nil {
		tags = []string{}
	}
	if metadata == nil {
		metadata = map[string]any{}
	}
	tb, _ := json.Marshal(tags)
	mb, _ := json.Marshal(metadata)
	return string(tb), string(mb)
}

type statusTimestamps struct {
	submittedAt any
	approvedAt  any
	publishedAt any
}

func statusTimestampValues(status string) statusTimestamps {
	ts := now()
	out := statusTimestamps{}
	switch status {
	case "submitted":
		out.submittedAt = ts
	case "approved":
		out.submittedAt = ts
		out.approvedAt = ts
	case "published":
		out.submittedAt = ts
		out.approvedAt = ts
		out.publishedAt = ts
	}
	return out
}

func appendStatusTimestampSets(sets []string, args []any, status string) ([]string, []any) {
	ts := now()
	switch status {
	case "submitted":
		sets = append(sets, "submitted_at = COALESCE(submitted_at, ?)")
		args = append(args, ts)
	case "approved":
		sets = append(sets, "submitted_at = COALESCE(submitted_at, ?)", "approved_at = COALESCE(approved_at, ?)")
		args = append(args, ts, ts)
	case "published":
		sets = append(sets, "submitted_at = COALESCE(submitted_at, ?)", "approved_at = COALESCE(approved_at, ?)", "published_at = COALESCE(published_at, ?)")
		args = append(args, ts, ts, ts)
	}
	return sets, args
}

func statusValues() []string {
	return []string{"draft", "submitted", "approved", "rejected", "published", "archived"}
}
func kindValues() []string { return []string{"text", "review", "video", "audio", "image"} }
func consentValues() []string {
	return []string{"unknown", "granted", "denied", "revoked"}
}
func permissionValues() []string {
	return []string{"private", "internal", "public", "marketing"}
}

func validStatus(v string) bool     { return in(v, statusValues()) }
func validKind(v string) bool       { return in(v, kindValues()) }
func validConsent(v string) bool    { return in(v, consentValues()) }
func validPermission(v string) bool { return in(v, permissionValues()) }

func in(v string, values []string) bool {
	for _, candidate := range values {
		if v == candidate {
			return true
		}
	}
	return false
}

func defaultString(v, fallback string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return fallback
	}
	return v
}

func nullableRating(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}

func now() string {
	return time.Now().UTC().Format(time.RFC3339)
}
