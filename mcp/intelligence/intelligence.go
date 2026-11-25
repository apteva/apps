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
	IngestedAt                                               *time.Time
	OriginKey                                                string `json:"origin_key"`
	// Basis is observed, inferred or synthetic; Access is open, internal or
	// restricted. Empty values take the defaults at insert.
	Basis, Access    string
	ContentExpiredAt *time.Time
}

func (e Evidence) Map() map[string]any {
	locations, _ := evidenceLocations(e.Payload)
	return map[string]any{"id": e.ID, "kind": e.Kind, "title": e.Title, "body": e.Body, "locations": locations,
		"payload": e.Payload, "entity_refs": e.EntityRefs, "source": e.Source, "source_ref": e.SourceRef,
		"event_time": timeString(e.EventTime), "published_time": timeString(e.PublishedTime),
		"observed_at": timeString(e.ObservedAt), "valid_from": timeString(e.ValidFrom), "valid_to": timeString(e.ValidTo),
		"supersedes_id": e.SupersedesID, "content_hash": e.ContentHash,
		"ingested_at": timeString(e.IngestedAt), "origin_key": e.OriginKey,
		"basis": fallback(e.Basis, basisObserved), "access": fallback(e.Access, accessOpen), "content_expired_at": timeString(e.ContentExpiredAt)}
}

type EvidenceQuery struct {
	ProjectID, Text, Kind, Source, Entity                string
	EventFrom, EventTo, PublishedFrom, PublishedTo, AsOf *time.Time
	Limit, Offset                                        int
	// Knowledge selects which clock as_of filters on. "observed" (default)
	// trusts the source-supplied observation time, which historical imports
	// may set; "ingested" uses the server's own arrival time and is what
	// forecasts use, so they can never see evidence that arrived later.
	Knowledge string
	IDs       []int64
	// Basis filters by basis; ExcludeSynthetic drops synthetic rows.
	Basis            string
	ExcludeSynthetic bool
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

// evidenceHash identifies content, not the moment it was seen: the same
// report fetched twice is one record. Observation and ingestion times are
// deliberately excluded (including them made every re-fetch a new row).
func evidenceHash(projectID string, e Evidence) string {
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%v\x00%v\x00%v\x00%v\x00%v",
		projectID, fallback(e.Kind, "document"), e.Title, e.Body, e.Source, e.SourceRef, e.Payload,
		timeString(e.EventTime), timeString(e.PublishedTime), timeString(e.ValidFrom), timeString(e.ValidTo), ptrInt64(e.SupersedesID))
	return hex.EncodeToString(h.Sum(nil))
}

func ptrInt64(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}

// originKey groups syndicated copies of one story: same normalized title
// regardless of outlet. Empty titles get no origin.
func originKey(title string) string {
	n := normTitle(title)
	if n == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(n))
	return hex.EncodeToString(sum[:8])
}

func recordEvidence(db *sql.DB, e Evidence, projectID string) (map[string]any, error) {
	out, _, err := recordEvidenceDetailed(db, e, projectID)
	return out, err
}

// recordEvidenceDetailed also reports whether the row was new. Existing
// rows are never rewritten: evidence is append-only, and a correction is a
// new row with supersedes_id.
func recordEvidenceDetailed(db *sql.DB, e Evidence, projectID string) (map[string]any, bool, error) {
	if db == nil {
		return nil, false, errors.New("database unavailable")
	}
	if strings.TrimSpace(projectID) == "" || strings.TrimSpace(e.Source) == "" {
		return nil, false, errors.New("project_id and source are required")
	}
	if e.ObservedAt == nil {
		now := nowUTC()
		e.ObservedAt = &now
	}
	if len(e.Payload) == 0 {
		e.Payload = json.RawMessage(`{}`)
	}
	if !json.Valid(e.Payload) {
		return nil, false, errors.New("payload must be valid JSON")
	}
	if _, err := evidenceLocations(e.Payload); err != nil {
		return nil, false, err
	}
	if e.ContentHash == "" {
		e.ContentHash = evidenceHash(projectID, e)
	}
	if e.OriginKey == "" {
		e.OriginKey = originKey(e.Title)
	}
	e.Basis = fallback(e.Basis, basisObserved)
	if !validBasis(e.Basis) {
		return nil, false, fmt.Errorf("basis must be observed, inferred or synthetic, got %q", e.Basis)
	}
	e.Access = fallback(e.Access, accessOpen)
	if !validAccess(e.Access) {
		return nil, false, fmt.Errorf("access must be open, internal or restricted, got %q", e.Access)
	}
	var existing int64
	if err := db.QueryRow(`SELECT id FROM intelligence_evidence WHERE project_id = ? AND content_hash = ?`, projectID, e.ContentHash).Scan(&existing); err == nil {
		rows, err := searchEvidence(db, EvidenceQuery{ProjectID: projectID, IDs: []int64{existing}, Limit: 1})
		if err != nil || len(rows) == 0 {
			return map[string]any{"id": existing, "content_hash": e.ContentHash, "duplicate": true}, false, err
		}
		rows[0]["duplicate"] = true
		return rows[0], false, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, false, err
	}
	now := nowUTC()
	e.IngestedAt = &now
	refs, _ := json.Marshal(e.EntityRefs)
	if e.EntityRefs == nil {
		refs = []byte("[]")
	}
	var id int64
	err := db.QueryRow(`INSERT INTO intelligence_evidence
 (project_id,kind,title,body,payload,entity_refs,source,source_ref,event_time,published_time,observed_at,valid_from,valid_to,supersedes_id,content_hash,ingested_at,origin_key,basis,access)
 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) RETURNING id`,
		projectID, fallback(e.Kind, "document"), e.Title, e.Body, string(e.Payload), string(refs), e.Source, e.SourceRef,
		timeString(e.EventTime), timeString(e.PublishedTime), timeString(e.ObservedAt), timeString(e.ValidFrom), timeString(e.ValidTo), e.SupersedesID, e.ContentHash,
		timeString(e.IngestedAt), e.OriginKey, e.Basis, e.Access).Scan(&id)
	if err != nil {
		return nil, false, err
	}
	e.ID = id
	e.Kind = fallback(e.Kind, "document")
	return e.Map(), true, nil
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
	if len(q.IDs) > 0 {
		marks := make([]string, len(q.IDs))
		for i, id := range q.IDs {
			marks[i] = "?"
			args = append(args, id)
		}
		where = append(where, "id IN ("+strings.Join(marks, ",")+")")
	}
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
	if q.Basis != "" {
		where = append(where, "basis = ?")
		args = append(args, q.Basis)
	}
	if q.ExcludeSynthetic {
		where = append(where, "basis <> 'synthetic'")
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
		args = append(args, timeString(q.EventFrom))
	}
	if q.EventTo != nil {
		where = append(where, "event_time < ?")
		args = append(args, timeString(q.EventTo))
	}
	if q.PublishedFrom != nil {
		where = append(where, "published_time >= ?")
		args = append(args, timeString(q.PublishedFrom))
	}
	if q.PublishedTo != nil {
		where = append(where, "published_time < ?")
		args = append(args, timeString(q.PublishedTo))
	}
	if q.AsOf != nil {
		clock := "observed_at"
		if q.Knowledge == "ingested" {
			clock = "COALESCE(ingested_at, observed_at)"
		}
		asOf := timeString(q.AsOf)
		where = append(where, clock+" <= ? AND (valid_from IS NULL OR valid_from <= ?) AND (valid_to IS NULL OR valid_to > ?)")
		args = append(args, asOf, asOf, asOf)
	}
	rows, err := db.Query(`SELECT id,kind,title,body,payload,entity_refs,source,source_ref,event_time,published_time,observed_at,valid_from,valid_to,supersedes_id,content_hash,ingested_at,origin_key,basis,access,content_expired_at FROM intelligence_evidence WHERE `+strings.Join(where, " AND ")+` ORDER BY COALESCE(event_time,published_time,observed_at) DESC, id DESC LIMIT ? OFFSET ?`, append(args, q.Limit, q.Offset)...)
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
		var event, published, observed, from, to, ingested, expired flexibleTime
		var supersedes sql.NullInt64
		if err := rows.Scan(&e.ID, &e.Kind, &e.Title, &e.Body, &payload, &refs, &e.Source, &e.SourceRef, &event, &published, &observed, &from, &to, &supersedes, &e.ContentHash, &ingested, &e.OriginKey, &e.Basis, &e.Access, &expired); err != nil {
			return nil, err
		}
		e.Payload = json.RawMessage(payload)
		_ = json.Unmarshal([]byte(refs), &e.EntityRefs)
		e.EventTime = event.Time()
		e.PublishedTime = published.Time()
		e.ObservedAt = observed.Time()
		e.ValidFrom = from.Time()
		e.ValidTo = to.Time()
		e.IngestedAt = ingested.Time()
		e.ContentExpiredAt = expired.Time()
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
