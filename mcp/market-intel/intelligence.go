package main

// Generic, bitemporal evidence primitives. Domain packs may add richer
// projections, but search and replay always operate on this stable record.
import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type Evidence struct {
	ID                                                       int64 `json:"id"`
	Kind, Title, Body, Source, SourceRef                     string
	Payload                                                  json.RawMessage `json:"payload"`
	EntityRefs                                               []string        `json:"entity_refs"`
	EventTime, PublishedTime, ObservedAt, ValidFrom, ValidTo *time.Time
	SupersedesID                                             *int64 `json:"supersedes_id,omitempty"`
	ContentHash                                              string `json:"content_hash"`
}

func (e Evidence) Map() map[string]any {
	locations, _ := evidenceLocations(e.Payload)
	return map[string]any{"id": e.ID, "kind": e.Kind, "title": e.Title, "body": e.Body, "locations": locations,
		"payload": e.Payload, "entity_refs": e.EntityRefs, "source": e.Source, "source_ref": e.SourceRef,
		"event_time": timeString(e.EventTime), "published_time": timeString(e.PublishedTime),
		"observed_at": timeString(e.ObservedAt), "valid_from": timeString(e.ValidFrom), "valid_to": timeString(e.ValidTo),
		"supersedes_id": e.SupersedesID, "content_hash": e.ContentHash}
}

type EvidenceQuery struct {
	ProjectID, Text, Kind, Source, Entity                string
	EventFrom, EventTo, PublishedFrom, PublishedTo, AsOf *time.Time
	Limit, Offset                                        int
}

func parseOptionalTime(value string) (*time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		if d, e := time.Parse("2006-01-02", value); e == nil {
			t = d.UTC()
		} else {
			return nil, fmt.Errorf("invalid timestamp %q: use RFC3339 or YYYY-MM-DD", value)
		}
	}
	return &t, nil
}

func timeString(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Format(time.RFC3339)
}

func evidenceHash(projectID, kind, title, body, source, sourceRef string, payload json.RawMessage, observed *time.Time) string {
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s", projectID, kind, title, body, source, sourceRef, payload, observed.UTC().Format(time.RFC3339Nano))
	return hex.EncodeToString(h.Sum(nil))
}

func recordEvidence(db *sql.DB, e Evidence, projectID string) (map[string]any, error) {
	if db == nil {
		return nil, errors.New("database unavailable")
	}
	if strings.TrimSpace(projectID) == "" || strings.TrimSpace(e.Source) == "" {
		return nil, errors.New("project_id and source are required")
	}
	if e.ObservedAt == nil {
		now := time.Now().UTC()
		e.ObservedAt = &now
	}
	if len(e.Payload) == 0 {
		e.Payload = json.RawMessage(`{}`)
	}
	if !json.Valid(e.Payload) {
		return nil, errors.New("payload must be valid JSON")
	}
	if _, err := evidenceLocations(e.Payload); err != nil {
		return nil, err
	}
	if e.ContentHash == "" {
		e.ContentHash = evidenceHash(projectID, e.Kind, e.Title, e.Body, e.Source, e.SourceRef, e.Payload, e.ObservedAt)
	}
	refs, _ := json.Marshal(e.EntityRefs)
	var id int64
	err := db.QueryRow(`INSERT INTO intelligence_evidence
 (project_id,kind,title,body,payload,entity_refs,source,source_ref,event_time,published_time,observed_at,valid_from,valid_to,supersedes_id,content_hash)
 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(project_id,content_hash) DO UPDATE SET title=excluded.title, body=excluded.body, payload=excluded.payload RETURNING id`,
		projectID, fallback(e.Kind, "document"), e.Title, e.Body, string(e.Payload), string(refs), e.Source, e.SourceRef,
		timeString(e.EventTime), timeString(e.PublishedTime), e.ObservedAt.UTC(), timeString(e.ValidFrom), timeString(e.ValidTo), e.SupersedesID, e.ContentHash).Scan(&id)
	if err != nil {
		return nil, err
	}
	e.ID = id
	return e.Map(), nil
}

func searchEvidence(db *sql.DB, q EvidenceQuery) ([]map[string]any, error) {
	if db == nil {
		return nil, errors.New("database unavailable")
	}
	if q.Limit <= 0 {
		q.Limit = 50
	}
	if q.Limit > 200 {
		q.Limit = 200
	}
	if q.Offset < 0 {
		return nil, errors.New("offset cannot be negative")
	}
	where := []string{"project_id = ?"}
	args := []any{q.ProjectID}
	if q.Text != "" {
		// Treat a query as a small AND-of-terms search. This is more useful for
		// research than requiring the exact phrase, while keeping the storage
		// layer portable across SQLite and future analytical backends.
		for _, term := range strings.Fields(strings.ToLower(q.Text)) {
			like := "%" + term + "%"
			where = append(where, "(lower(title) LIKE ? OR lower(body) LIKE ? OR lower(payload) LIKE ?)")
			args = append(args, like, like, like)
		}
	}
	if q.Kind != "" {
		where = append(where, "kind = ?")
		args = append(args, q.Kind)
	}
	if q.Source != "" {
		where = append(where, "source = ?")
		args = append(args, q.Source)
	}
	if q.Entity != "" {
		where = append(where, "lower(entity_refs) LIKE ?")
		args = append(args, "%"+strings.ToLower(q.Entity)+"%")
	}
	if q.EventFrom != nil {
		where = append(where, "event_time >= ?")
		args = append(args, q.EventFrom.UTC())
	}
	if q.EventTo != nil {
		where = append(where, "event_time < ?")
		args = append(args, q.EventTo.UTC())
	}
	if q.PublishedFrom != nil {
		where = append(where, "published_time >= ?")
		args = append(args, q.PublishedFrom.UTC())
	}
	if q.PublishedTo != nil {
		where = append(where, "published_time < ?")
		args = append(args, q.PublishedTo.UTC())
	}
	if q.AsOf != nil {
		where = append(where, "observed_at <= ? AND (valid_from IS NULL OR valid_from <= ?) AND (valid_to IS NULL OR valid_to > ?)")
		args = append(args, q.AsOf.UTC(), q.AsOf.UTC(), q.AsOf.UTC())
	}
	rows, err := db.Query(`SELECT id,kind,title,body,payload,entity_refs,source,source_ref,event_time,published_time,observed_at,valid_from,valid_to,supersedes_id,content_hash FROM intelligence_evidence WHERE `+strings.Join(where, " AND ")+` ORDER BY COALESCE(event_time,published_time,observed_at) DESC, id DESC LIMIT ? OFFSET ?`, append(args, q.Limit, q.Offset)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var e Evidence
		var payload, refs string
		// SQLite may return DATETIME values as either time.Time or strings
		// depending on the driver/schema affinity. Scan as flexible values so
		// historical databases created by older releases remain readable.
		var event, published, observed, from, to flexibleTime
		var supersedes sql.NullInt64
		if err := rows.Scan(&e.ID, &e.Kind, &e.Title, &e.Body, &payload, &refs, &e.Source, &e.SourceRef, &event, &published, &observed, &from, &to, &supersedes, &e.ContentHash); err != nil {
			return nil, err
		}
		e.Payload = json.RawMessage(payload)
		_ = json.Unmarshal([]byte(refs), &e.EntityRefs)
		e.EventTime = event.Time()
		e.PublishedTime = published.Time()
		e.ObservedAt = observed.Time()
		e.ValidFrom = from.Time()
		e.ValidTo = to.Time()
		if supersedes.Valid {
			v := supersedes.Int64
			e.SupersedesID = &v
		}
		out = append(out, e.Map())
	}
	return out, rows.Err()
}

type flexibleTime struct{ raw any }

func (v *flexibleTime) Scan(src any) error { v.raw = src; return nil }

func (v flexibleTime) Time() *time.Time {
	var t time.Time
	switch x := v.raw.(type) {
	case time.Time:
		t = x
	case string:
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05.999999999-07:00", "2006-01-02 15:04:05"} {
			if parsed, err := time.Parse(layout, x); err == nil {
				t = parsed
				break
			}
		}
	case []byte:
		return flexibleTime{raw: string(x)}.Time()
	}
	if t.IsZero() {
		return nil
	}
	t = t.UTC()
	return &t
}
func fallback(v, d string) string {
	if strings.TrimSpace(v) == "" {
		return d
	}
	return v
}
