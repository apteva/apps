package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
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
	Status          string
	Kind            string
	Source          string
	Tag             string
	Q               string
	PublishedOnly   bool
	IncludeArchived bool
	Limit           int
	Offset          int
}

type PublicTestimonial struct {
	ID              int64    `json:"id"`
	Status          string   `json:"status"`
	Kind            string   `json:"kind"`
	Source          string   `json:"source"`
	Title           string   `json:"title"`
	Quote           string   `json:"quote"`
	Body            string   `json:"body"`
	Rating          *int     `json:"rating,omitempty"`
	AuthorName      string   `json:"author_name"`
	AuthorRole      string   `json:"author_role"`
	AuthorCompany   string   `json:"author_company"`
	MediaURL        string   `json:"media_url"`
	PermissionScope string   `json:"permission_scope"`
	Tags            []string `json:"tags"`
	PublishedAt     string   `json:"published_at,omitempty"`
	UpdatedAt       string   `json:"updated_at,omitempty"`
}

func createTestimonial(db *sql.DB, projectID string, t *Testimonial) (*Testimonial, error) {
	if err := normalizeTestimonial(t); err != nil {
		return nil, err
	}
	tagsJSON, metadataJSON, err := encodeJSONFields(t.Tags, t.Metadata)
	if err != nil {
		return nil, err
	}
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
	items, _, err := listTestimonialsPage(db, projectID, f)
	return items, err
}

func listTestimonialsPage(db *sql.DB, projectID string, f TestimonialFilter) ([]Testimonial, int, error) {
	clauses, args, f, err := testimonialFilterSQL(projectID, f)
	if err != nil {
		return nil, 0, err
	}
	var total int
	if err := db.QueryRow(`SELECT COUNT(*) FROM testimonials WHERE `+strings.Join(clauses, " AND "), args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	queryArgs := append(append([]any{}, args...), f.Limit, f.Offset)
	rows, err := db.Query(`
		SELECT id, project_id, status, kind, source, title, quote, body, rating,
		       author_name, author_role, author_company, author_email,
		       media_file_id, media_url, consent_status, permission_scope,
		       tags_json, metadata_json, COALESCE(submitted_at, ''), COALESCE(approved_at, ''),
		       COALESCE(published_at, ''), created_at, updated_at
		FROM testimonials
		WHERE `+strings.Join(clauses, " AND ")+`
		ORDER BY updated_at DESC, id DESC
		LIMIT ? OFFSET ?`, queryArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []Testimonial{}
	for rows.Next() {
		t, err := scanTestimonial(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *t)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

func testimonialFilterSQL(projectID string, f TestimonialFilter) ([]string, []any, TestimonialFilter, error) {
	if f.PublishedOnly {
		f.Status = "published"
	}
	if f.Status != "" && !validStatus(f.Status) {
		return nil, nil, f, fmt.Errorf("invalid status %q", f.Status)
	}
	if f.Kind != "" && !validKind(f.Kind) {
		return nil, nil, f, fmt.Errorf("invalid kind %q", f.Kind)
	}
	clauses := []string{"project_id = ?"}
	args := []any{projectID}
	if f.Status != "" {
		clauses = append(clauses, "status = ?")
		args = append(args, f.Status)
	} else if !f.IncludeArchived {
		clauses = append(clauses, "status <> 'archived'")
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
		clauses = append(clauses, "EXISTS (SELECT 1 FROM json_each(testimonials.tags_json) AS tag_item WHERE tag_item.value = ?)")
		args = append(args, strings.TrimSpace(f.Tag))
	}
	if strings.TrimSpace(f.Q) != "" {
		q := literalLikePattern(strings.TrimSpace(f.Q))
		clauses = append(clauses, `(title LIKE ? ESCAPE '\' OR quote LIKE ? ESCAPE '\' OR body LIKE ? ESCAPE '\' OR author_name LIKE ? ESCAPE '\' OR author_company LIKE ? ESCAPE '\')`)
		args = append(args, q, q, q, q, q)
	}
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 50
	}
	if f.Offset < 0 {
		return nil, nil, f, errors.New("offset must be zero or greater")
	}
	return clauses, args, f, nil
}

func literalLikePattern(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `%`, `\%`)
	value = strings.ReplaceAll(value, `_`, `\_`)
	return "%" + value + "%"
}

func publicTestimonials(items []Testimonial) []PublicTestimonial {
	out := make([]PublicTestimonial, 0, len(items))
	for _, t := range items {
		out = append(out, PublicTestimonial{
			ID: t.ID, Status: t.Status, Kind: t.Kind, Source: t.Source,
			Title: t.Title, Quote: t.Quote, Body: t.Body, Rating: t.Rating,
			AuthorName: t.AuthorName, AuthorRole: t.AuthorRole, AuthorCompany: t.AuthorCompany,
			MediaURL: t.MediaURL, PermissionScope: t.PermissionScope,
			Tags: append([]string{}, t.Tags...), PublishedAt: t.PublishedAt, UpdatedAt: t.UpdatedAt,
		})
	}
	return out
}

func getTestimonial(db *sql.DB, projectID string, id int64) (*Testimonial, error) {
	return getTestimonialFrom(db, projectID, id)
}

type testimonialQuerier interface {
	QueryRow(query string, args ...any) *sql.Row
}

func getTestimonialFrom(q testimonialQuerier, projectID string, id int64) (*Testimonial, error) {
	t, err := scanTestimonial(q.QueryRow(`
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
	if id <= 0 {
		return nil, errors.New("id required")
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	current, err := getTestimonialFrom(tx, projectID, id)
	if err != nil {
		return nil, err
	}
	if current == nil {
		return nil, errNotFound
	}
	candidate := cloneTestimonial(*current)
	changed, err := applyTestimonialPatch(&candidate, fields)
	if err != nil {
		return nil, err
	}
	if !changed {
		return nil, errors.New("no updatable fields provided")
	}
	if err := normalizeTestimonial(&candidate); err != nil {
		return nil, err
	}
	tagsJSON, metadataJSON, err := encodeJSONFields(candidate.Tags, candidate.Metadata)
	if err != nil {
		return nil, err
	}
	submittedAt, approvedAt, publishedAt := current.SubmittedAt, current.ApprovedAt, current.PublishedAt
	updatedAt := now()
	if current.Status != candidate.Status {
		switch candidate.Status {
		case "submitted":
			if submittedAt == "" {
				submittedAt = updatedAt
			}
		case "approved":
			if submittedAt == "" {
				submittedAt = updatedAt
			}
			if approvedAt == "" {
				approvedAt = updatedAt
			}
		case "published":
			if submittedAt == "" {
				submittedAt = updatedAt
			}
			if approvedAt == "" {
				approvedAt = updatedAt
			}
			if publishedAt == "" {
				publishedAt = updatedAt
			}
		}
	}
	res, err := tx.Exec(`UPDATE testimonials SET
		status = ?, kind = ?, source = ?, title = ?, quote = ?, body = ?, rating = ?,
		author_name = ?, author_role = ?, author_company = ?, author_email = ?,
		media_file_id = ?, media_url = ?, consent_status = ?, permission_scope = ?,
		tags_json = ?, metadata_json = ?, submitted_at = ?, approved_at = ?, published_at = ?, updated_at = ?
		WHERE id = ? AND project_id = ?`,
		candidate.Status, candidate.Kind, candidate.Source, candidate.Title, candidate.Quote, candidate.Body, nullableRating(candidate.Rating),
		candidate.AuthorName, candidate.AuthorRole, candidate.AuthorCompany, candidate.AuthorEmail,
		candidate.MediaFileID, candidate.MediaURL, candidate.ConsentStatus, candidate.PermissionScope,
		tagsJSON, metadataJSON, nullableString(submittedAt), nullableString(approvedAt), nullableString(publishedAt), updatedAt,
		id, projectID)
	if err != nil {
		return nil, err
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		return nil, errNotFound
	}
	updated, err := getTestimonialFrom(tx, projectID, id)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return updated, nil
}

func applyTestimonialPatch(t *Testimonial, fields map[string]any) (bool, error) {
	changed := false
	for key, value := range fields {
		if key == "id" {
			continue
		}
		changed = true
		switch key {
		case "status", "kind", "source", "title", "quote", "body", "author_name", "author_role", "author_company", "author_email", "media_file_id", "media_url", "consent_status", "permission_scope":
			s, ok := value.(string)
			if !ok {
				return false, fmt.Errorf("%s must be a string", key)
			}
			s = strings.TrimSpace(s)
			switch key {
			case "status":
				t.Status = s
			case "kind":
				t.Kind = s
			case "source":
				t.Source = s
			case "title":
				t.Title = s
			case "quote":
				t.Quote = s
			case "body":
				t.Body = s
			case "author_name":
				t.AuthorName = s
			case "author_role":
				t.AuthorRole = s
			case "author_company":
				t.AuthorCompany = s
			case "author_email":
				t.AuthorEmail = s
			case "media_file_id":
				t.MediaFileID = s
			case "media_url":
				t.MediaURL = s
			case "consent_status":
				t.ConsentStatus = s
			case "permission_scope":
				t.PermissionScope = s
			}
		case "rating":
			if value == nil {
				t.Rating = nil
				continue
			}
			n, ok := intFromAny(value)
			if !ok {
				return false, errors.New("rating must be an integer")
			}
			t.Rating = &n
		case "tags":
			tags, err := strictStringSlice(value)
			if err != nil {
				return false, err
			}
			t.Tags = tags
		case "metadata":
			if value == nil {
				t.Metadata = map[string]any{}
				continue
			}
			meta, ok := value.(map[string]any)
			if !ok {
				return false, errors.New("metadata must be an object")
			}
			t.Metadata = meta
		default:
			return false, fmt.Errorf("unknown field %q", key)
		}
	}
	return changed, nil
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
	if t.Title == "" && t.Quote == "" && t.Body == "" && t.MediaFileID == "" && t.MediaURL == "" {
		return errors.New("title, quote, body, media_file_id, or media_url required")
	}
	if t.MediaURL != "" {
		u, err := url.ParseRequestURI(t.MediaURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
			return errors.New("media_url must be an http or https URL without credentials")
		}
	}
	t.Tags = cleanStrings(t.Tags)
	if t.Metadata == nil {
		t.Metadata = map[string]any{}
	}
	for field, value := range map[string]string{
		"status": t.Status, "kind": t.Kind, "source": t.Source, "title": t.Title,
		"quote": t.Quote, "author_name": t.AuthorName, "author_role": t.AuthorRole,
		"author_company": t.AuthorCompany, "author_email": t.AuthorEmail,
		"media_file_id": t.MediaFileID, "media_url": t.MediaURL,
	} {
		limit := 500
		switch field {
		case "status", "kind":
			limit = 32
		case "source":
			limit = 100
		case "quote":
			limit = 5000
		case "media_url":
			limit = 4096
		}
		if utf8.RuneCountInString(value) > limit {
			return fmt.Errorf("%s exceeds %d characters", field, limit)
		}
	}
	if utf8.RuneCountInString(t.Body) > 100000 {
		return errors.New("body exceeds 100000 characters")
	}
	if len(t.Tags) > 50 {
		return errors.New("tags cannot contain more than 50 values")
	}
	for _, tag := range t.Tags {
		if utf8.RuneCountInString(tag) > 100 {
			return errors.New("each tag must be 100 characters or fewer")
		}
	}
	_, metadataJSON, err := encodeJSONFields(t.Tags, t.Metadata)
	if err != nil {
		return err
	}
	if len(metadataJSON) > 64<<10 {
		return errors.New("metadata exceeds 64 KiB")
	}
	if t.Status == "published" {
		if t.ConsentStatus != "granted" {
			return errors.New("published testimonials require granted consent")
		}
		if t.PermissionScope != "public" && t.PermissionScope != "marketing" {
			return errors.New("published testimonials require public or marketing permission")
		}
		if t.Title == "" && t.Quote == "" && t.Body == "" && t.MediaURL == "" {
			return errors.New("media-only published testimonials require media_url")
		}
	}
	return nil
}

func encodeJSONFields(tags []string, metadata map[string]any) (string, string, error) {
	if tags == nil {
		tags = []string{}
	}
	if metadata == nil {
		metadata = map[string]any{}
	}
	tb, err := json.Marshal(tags)
	if err != nil {
		return "", "", fmt.Errorf("encode tags: %w", err)
	}
	mb, err := json.Marshal(metadata)
	if err != nil {
		return "", "", fmt.Errorf("encode metadata: %w", err)
	}
	return string(tb), string(mb), nil
}

func cloneTestimonial(t Testimonial) Testimonial {
	t.Tags = append([]string{}, t.Tags...)
	t.Metadata = make(map[string]any, len(t.Metadata))
	for key, value := range t.Metadata {
		t.Metadata[key] = value
	}
	if t.Rating != nil {
		rating := *t.Rating
		t.Rating = &rating
	}
	return t
}

func strictStringSlice(value any) ([]string, error) {
	if value == nil {
		return []string{}, nil
	}
	var values []string
	switch typed := value.(type) {
	case []string:
		values = typed
	case []any:
		values = make([]string, 0, len(typed))
		for _, item := range typed {
			s, ok := item.(string)
			if !ok {
				return nil, errors.New("tags must contain strings")
			}
			values = append(values, s)
		}
	default:
		return nil, errors.New("tags must be an array of strings")
	}
	return cleanStrings(values), nil
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
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
