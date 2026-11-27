package main

// World-model storage. The app holds one SQLite connection
// (SetMaxOpenConns(1)), so every query here drains and closes its rows
// before the next statement runs — never nest a query inside rows.Next().

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// nowUTC is a variable so the reproducible ingestion sample can pin the clock.
var nowUTC = func() time.Time { return time.Now().UTC() }

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func parseTS(v any) *time.Time { return flexibleTime{raw: v}.Time() }

// ─── Sources ───────────────────────────────────────────────────────

type IntelSource struct {
	ID            int64          `json:"id"`
	Slug          string         `json:"slug"`
	Connector     string         `json:"connector"`
	Enabled       bool           `json:"enabled"`
	IntervalSec   int            `json:"interval_sec"`
	Config        map[string]any `json:"config"`
	PermittedUse  string         `json:"permitted_use"`
	Licence       string         `json:"licence"`
	RetentionDays int            `json:"retention_days"`
	Access        string         `json:"access"`
	Cursor        string         `json:"cursor"`
	LastRunAt     *time.Time     `json:"last_run_at"`
	LastSuccessAt *time.Time     `json:"last_success_at"`
	LastError     string         `json:"last_error"`
	ItemsTotal    int            `json:"items_total"`
	CallsTotal    int            `json:"calls_total"`
}

// FreshnessSec is how long since the last successful run; -1 = never.
func (s IntelSource) FreshnessSec(now time.Time) int {
	if s.LastSuccessAt == nil {
		return -1
	}
	return int(now.Sub(*s.LastSuccessAt).Seconds())
}

// ensureSource inserts a source with defaults if missing; an existing
// source keeps its operator-edited settings.
func ensureSource(db *sql.DB, pid string, s IntelSource) error {
	cfg := mustJSON(s.Config)
	if s.Config == nil {
		cfg = "{}"
	}
	_, err := db.Exec(`INSERT INTO intel_sources (project_id, slug, connector, enabled, interval_sec, config, permitted_use, licence, retention_days, access)
		VALUES (?,?,?,?,?,?,?,?,?,?) ON CONFLICT(project_id, slug) DO NOTHING`,
		pid, s.Slug, s.Connector, boolInt(s.Enabled), s.IntervalSec, cfg, s.PermittedUse, s.Licence, s.RetentionDays, fallback(s.Access, accessOpen))
	return err
}

func updateSourceSettings(db *sql.DB, pid, slug string, enabled *bool, interval *int, config map[string]any, access string, retentionDays *int) error {
	sets, args := []string{}, []any{}
	if access != "" {
		if !validAccess(access) {
			return errors.New("access must be open, internal or restricted")
		}
		sets, args = append(sets, "access = ?"), append(args, access)
	}
	if retentionDays != nil {
		if *retentionDays < 0 {
			return errors.New("retention_days cannot be negative (0 keeps content)")
		}
		sets, args = append(sets, "retention_days = ?"), append(args, *retentionDays)
	}
	if enabled != nil {
		sets, args = append(sets, "enabled = ?"), append(args, boolInt(*enabled))
	}
	if interval != nil {
		if *interval < 60 {
			return errors.New("interval_sec must be at least 60")
		}
		sets, args = append(sets, "interval_sec = ?"), append(args, *interval)
	}
	if config != nil {
		sets, args = append(sets, "config = ?"), append(args, mustJSON(config))
	}
	if len(sets) == 0 {
		return errors.New("nothing to update")
	}
	res, err := db.Exec(`UPDATE intel_sources SET `+strings.Join(sets, ", ")+` WHERE project_id = ? AND slug = ?`, append(args, pid, slug)...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("unknown source %q", slug)
	}
	return nil
}

func listSources(db *sql.DB, pid string) ([]IntelSource, error) {
	rows, err := db.Query(`SELECT id, slug, connector, enabled, interval_sec, config, permitted_use, licence, retention_days, cursor,
		last_run_at, last_success_at, last_error, items_total, calls_total, access FROM intel_sources WHERE project_id = ? ORDER BY slug`, pid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []IntelSource{}
	for rows.Next() {
		var s IntelSource
		var cfg string
		var enabled int
		var lastRun, lastOK flexibleTime
		if err := rows.Scan(&s.ID, &s.Slug, &s.Connector, &enabled, &s.IntervalSec, &cfg, &s.PermittedUse, &s.Licence, &s.RetentionDays, &s.Cursor,
			&lastRun, &lastOK, &s.LastError, &s.ItemsTotal, &s.CallsTotal, &s.Access); err != nil {
			return nil, err
		}
		s.Enabled = enabled != 0
		_ = json.Unmarshal([]byte(cfg), &s.Config)
		if s.Config == nil {
			s.Config = map[string]any{}
		}
		s.LastRunAt, s.LastSuccessAt = lastRun.Time(), lastOK.Time()
		out = append(out, s)
	}
	return out, rows.Err()
}

func startIngestRun(db *sql.DB, pid, slug string) (int64, error) {
	var id int64
	err := db.QueryRow(`INSERT INTO intel_ingest_runs (project_id, source_slug, started_at) VALUES (?,?,?) RETURNING id`, pid, slug, ts(nowUTC())).Scan(&id)
	return id, err
}

type ingestStats struct {
	Fetched, Inserted, Duplicates, Calls int
	Cursor                               string
}

func finishIngestRun(db *sql.DB, pid, slug string, runID int64, st ingestStats, runErr error) {
	now := ts(nowUTC())
	status, msg := "ok", ""
	if runErr != nil {
		status, msg = "error", runErr.Error()
	}
	_, _ = db.Exec(`UPDATE intel_ingest_runs SET finished_at = ?, status = ?, fetched = ?, inserted = ?, duplicates = ?, calls = ?, error = ? WHERE id = ?`,
		now, status, st.Fetched, st.Inserted, st.Duplicates, st.Calls, msg, runID)
	if runErr != nil {
		_, _ = db.Exec(`UPDATE intel_sources SET last_run_at = ?, last_error = ?, calls_total = calls_total + ? WHERE project_id = ? AND slug = ?`,
			now, msg, st.Calls, pid, slug)
		return
	}
	_, _ = db.Exec(`UPDATE intel_sources SET last_run_at = ?, last_success_at = ?, last_error = '', items_total = items_total + ?,
		calls_total = calls_total + ?, cursor = CASE WHEN ? <> '' THEN ? ELSE cursor END WHERE project_id = ? AND slug = ?`,
		now, now, st.Inserted, st.Calls, st.Cursor, st.Cursor, pid, slug)
}

func recentIngestRuns(db *sql.DB, pid string, limit int) ([]map[string]any, error) {
	rows, err := db.Query(`SELECT id, source_slug, started_at, finished_at, status, fetched, inserted, duplicates, calls, error
		FROM intel_ingest_runs WHERE project_id = ? ORDER BY id DESC LIMIT ?`, pid, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id int64
		var slug, status, msg string
		var started, finished flexibleTime
		var fetched, inserted, dups, calls int
		if err := rows.Scan(&id, &slug, &started, &finished, &status, &fetched, &inserted, &dups, &calls, &msg); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"id": id, "source": slug, "started_at": timeString(started.Time()), "finished_at": timeString(finished.Time()),
			"status": status, "fetched": fetched, "inserted": inserted, "duplicates": dups, "calls": calls, "error": msg})
	}
	return out, rows.Err()
}

// ─── Entities ──────────────────────────────────────────────────────

// Entity kinds the world model accepts. "person" is deliberately absent:
// people are represented only as public roles attached to institutions or
// events, never as individuals to be tracked or scored.
var entityKinds = map[string]bool{
	"organization": true, "facility": true, "place": true, "region": true, "commodity": true,
	"product": true, "technology": true, "market": true, "institution": true, "infrastructure": true,
	"event": true, "indicator": true, "public_role": true, "concept": true,
}

func validEntityKind(kind string) error {
	kind = strings.ToLower(strings.TrimSpace(kind))
	if kind == "person" || kind == "individual" || kind == "people" {
		return errors.New("person entities are out of scope: model people only as kind public_role (e.g. \"central bank governor\"), never as tracked individuals")
	}
	if !entityKinds[kind] {
		return fmt.Errorf("unknown entity kind %q", kind)
	}
	return nil
}

type WorldEntity struct {
	ID          int64             `json:"id"`
	Name        string            `json:"name"`
	Kind        string            `json:"kind"`
	Domain      string            `json:"domain"`
	Description string            `json:"description"`
	Aliases     []string          `json:"aliases"`
	ExternalIDs map[string]string `json:"external_ids"`
	Basis       string            `json:"basis"`
}

// upsertWorldEntity finds an entity by external id, exact name, or alias
// (case-insensitive), merging new aliases/ids; otherwise creates it.
func upsertWorldEntity(db *sql.DB, pid string, e WorldEntity) (*WorldEntity, bool, error) {
	e.Name = strings.TrimSpace(e.Name)
	if e.Name == "" {
		return nil, false, errors.New("entity name required")
	}
	e.Kind = strings.ToLower(fallback(e.Kind, "concept"))
	if err := validEntityKind(e.Kind); err != nil {
		return nil, false, err
	}
	e.Domain = fallback(e.Domain, "world")
	e.Basis = fallback(e.Basis, basisObserved)
	if !validBasis(e.Basis) {
		return nil, false, fmt.Errorf("invalid basis %q", e.Basis)
	}
	existing, err := findWorldEntity(db, pid, e.Name, e.ExternalIDs)
	if err != nil {
		return nil, false, err
	}
	if existing != nil {
		changed := false
		for _, a := range append([]string{e.Name}, e.Aliases...) {
			if a = strings.TrimSpace(a); a != "" && !strings.EqualFold(a, existing.Name) && !containsFold(existing.Aliases, a) {
				existing.Aliases = append(existing.Aliases, a)
				changed = true
			}
		}
		for k, v := range e.ExternalIDs {
			if existing.ExternalIDs[k] == "" && v != "" {
				existing.ExternalIDs[k] = v
				changed = true
			}
		}
		if existing.Description == "" && e.Description != "" {
			existing.Description = e.Description
			changed = true
		}
		if existing.Kind == "concept" && e.Kind != "concept" {
			existing.Kind = e.Kind
			changed = true
		}
		// A source confirming an inferred or synthetic entity upgrades it to
		// observed; an inference never downgrades an observed entity.
		if basisRank(e.Basis) > basisRank(existing.Basis) {
			existing.Basis = e.Basis
			changed = true
		}
		if changed {
			_, err = db.Exec(`UPDATE entities SET aliases = ?, external_ids = ?, description = ?, kind = ?, basis = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
				mustJSON(existing.Aliases), mustJSON(existing.ExternalIDs), existing.Description, existing.Kind, existing.Basis, existing.ID)
		}
		return existing, false, err
	}
	if e.Aliases == nil {
		e.Aliases = []string{}
	}
	if e.ExternalIDs == nil {
		e.ExternalIDs = map[string]string{}
	}
	err = db.QueryRow(`INSERT INTO entities (project_id, canonical, domain, external_ids, aliases, kind, description, basis) VALUES (?,?,?,?,?,?,?,?) RETURNING id`,
		pid, e.Name, e.Domain, mustJSON(e.ExternalIDs), mustJSON(e.Aliases), e.Kind, e.Description, e.Basis).Scan(&e.ID)
	if err != nil {
		return nil, false, err
	}
	return &e, true, nil
}

func containsFold(list []string, v string) bool {
	for _, x := range list {
		if strings.EqualFold(x, v) {
			return true
		}
	}
	return false
}

func findWorldEntity(db *sql.DB, pid, name string, ext map[string]string) (*WorldEntity, error) {
	for k, v := range ext {
		if v == "" {
			continue
		}
		e, err := scanWorldEntity(db.QueryRow(`SELECT id, canonical, kind, domain, description, aliases, external_ids, basis FROM entities
			WHERE project_id = ? AND json_extract(external_ids, '$.' || ?) = ? LIMIT 1`, pid, k, v))
		if err != nil || e != nil {
			return e, err
		}
	}
	e, err := scanWorldEntity(db.QueryRow(`SELECT id, canonical, kind, domain, description, aliases, external_ids, basis FROM entities
		WHERE project_id = ? AND lower(canonical) = lower(?) LIMIT 1`, pid, name))
	if err != nil || e != nil {
		return e, err
	}
	return scanWorldEntity(db.QueryRow(`SELECT e.id, e.canonical, e.kind, e.domain, e.description, e.aliases, e.external_ids, e.basis FROM entities e, json_each(e.aliases) a
		WHERE e.project_id = ? AND lower(a.value) = lower(?) LIMIT 1`, pid, name))
}

func scanWorldEntity(row *sql.Row) (*WorldEntity, error) {
	var e WorldEntity
	var aliases, ext string
	if err := row.Scan(&e.ID, &e.Name, &e.Kind, &e.Domain, &e.Description, &aliases, &ext, &e.Basis); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	_ = json.Unmarshal([]byte(aliases), &e.Aliases)
	_ = json.Unmarshal([]byte(ext), &e.ExternalIDs)
	if e.Aliases == nil {
		e.Aliases = []string{}
	}
	if e.ExternalIDs == nil {
		e.ExternalIDs = map[string]string{}
	}
	return &e, nil
}

func getWorldEntity(db *sql.DB, pid string, id int64) (*WorldEntity, error) {
	return scanWorldEntity(db.QueryRow(`SELECT id, canonical, kind, domain, description, aliases, external_ids, basis FROM entities WHERE project_id = ? AND id = ?`, pid, id))
}

func searchWorldEntities(db *sql.DB, pid, query, kind string, limit int) ([]WorldEntity, error) {
	where, args := []string{"project_id = ?"}, []any{pid}
	if q := strings.ToLower(strings.TrimSpace(query)); q != "" {
		where, args = append(where, "(lower(canonical) LIKE ? OR lower(aliases) LIKE ?)"), append(args, "%"+q+"%", "%"+q+"%")
	}
	if kind != "" {
		where, args = append(where, "kind = ?"), append(args, kind)
	}
	rows, err := db.Query(`SELECT id, canonical, kind, domain, description, aliases, external_ids, basis FROM entities WHERE `+strings.Join(where, " AND ")+` ORDER BY canonical LIMIT ?`, append(args, clampLimit(limit, 50, 500))...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []WorldEntity{}
	for rows.Next() {
		var e WorldEntity
		var aliases, ext string
		if err := rows.Scan(&e.ID, &e.Name, &e.Kind, &e.Domain, &e.Description, &aliases, &ext, &e.Basis); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(aliases), &e.Aliases)
		_ = json.Unmarshal([]byte(ext), &e.ExternalIDs)
		out = append(out, e)
	}
	return out, rows.Err()
}

func clampLimit(n, def, max int) int {
	if n <= 0 {
		return def
	}
	if n > max {
		return max
	}
	return n
}

// ─── Relationships ─────────────────────────────────────────────────

var relationshipStatuses = map[string]bool{"observed": true, "reported": true, "inferred": true, "assumed": true}

// relationshipBasis maps a status onto the observed/inferred split; a
// synthetic relationship stays synthetic whatever its status.
func relationshipBasis(status, basis string) string {
	if basis == basisSynthetic {
		return basisSynthetic
	}
	if status == "observed" || status == "reported" {
		return basisObserved
	}
	return basisInferred
}

type Relationship struct {
	ID          int64      `json:"id"`
	From        int64      `json:"from_entity_id"`
	FromName    string     `json:"from"`
	Type        string     `json:"type"`
	To          int64      `json:"to_entity_id"`
	ToName      string     `json:"to"`
	Status      string     `json:"status"`
	Confidence  *float64   `json:"confidence,omitempty"`
	ValidFrom   *time.Time `json:"valid_from,omitempty"`
	ValidTo     *time.Time `json:"valid_to,omitempty"`
	EvidenceIDs []int64    `json:"evidence_ids"`
	ClaimIDs    []int64    `json:"claim_ids"`
	Note        string     `json:"note,omitempty"`
	Basis       string     `json:"basis"`
	// CausalBasis grades what the sources support (see migration 007):
	// mechanism | asserted | co_occurrence | unsupported | assumed | "".
	CausalBasis string `json:"causal_basis"`
	CausalNote  string `json:"causal_note,omitempty"`
	GradedBy    string `json:"graded_by,omitempty"`
}

func assertRelationship(db *sql.DB, pid string, r Relationship) (*Relationship, error) {
	r.Type = strings.ToLower(strings.TrimSpace(r.Type))
	if r.From == 0 || r.To == 0 || r.Type == "" {
		return nil, errors.New("from, to and type are required")
	}
	if !relationshipStatuses[r.Status] {
		return nil, errors.New("status must be observed, reported, inferred or assumed")
	}
	if (r.Status == "observed" || r.Status == "reported") && len(r.EvidenceIDs) == 0 && len(r.ClaimIDs) == 0 {
		return nil, errors.New("observed and reported relationships need supporting evidence_ids or claim_ids; use status assumed for operator assumptions")
	}
	for _, id := range []int64{r.From, r.To} {
		if e, err := getWorldEntity(db, pid, id); err != nil || e == nil {
			return nil, fmt.Errorf("entity %d not found", id)
		}
	}
	if r.EvidenceIDs == nil {
		r.EvidenceIDs = []int64{}
	}
	if r.ClaimIDs == nil {
		r.ClaimIDs = []int64{}
	}
	r.Basis = relationshipBasis(r.Status, r.Basis)
	r.CausalBasis = strings.ToLower(strings.TrimSpace(r.CausalBasis))
	switch {
	case r.Status == "assumed":
		r.CausalBasis, r.GradedBy = cbAssumed, "status"
	case r.CausalBasis == cbAssumed:
		return nil, errors.New("causal_basis assumed is only for status assumed")
	case r.CausalBasis != "" && !causalBases[r.CausalBasis]:
		return nil, errors.New("causal_basis must be mechanism, asserted, co_occurrence or unsupported")
	case r.CausalBasis == "":
		r.GradedBy = ""
	}
	var gradedAt any
	if r.GradedBy != "" {
		gradedAt = ts(nowUTC())
	}
	err := db.QueryRow(`INSERT INTO intel_relationships (project_id, from_entity_id, rel_type, to_entity_id, status, confidence, valid_from, valid_to, evidence_ids, claim_ids, note, basis, causal_basis, causal_note, graded_by, graded_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) RETURNING id`, pid, r.From, r.Type, r.To, r.Status, fptr(r.Confidence), timeString(r.ValidFrom), timeString(r.ValidTo),
		mustJSON(r.EvidenceIDs), mustJSON(r.ClaimIDs), r.Note, r.Basis, r.CausalBasis, r.CausalNote, r.GradedBy, gradedAt).Scan(&r.ID)
	return &r, err
}

// relationshipsAt returns relationships touching any of the entity ids that
// are valid at t (nil = now) and not retracted.
func relationshipsAt(db *sql.DB, pid string, entityIDs []int64, at *time.Time) ([]Relationship, error) {
	if len(entityIDs) == 0 {
		return []Relationship{}, nil
	}
	marks := strings.TrimSuffix(strings.Repeat("?,", len(entityIDs)), ",")
	args := []any{pid}
	for _, id := range entityIDs {
		args = append(args, id)
	}
	for _, id := range entityIDs {
		args = append(args, id)
	}
	when := ts(nowUTC())
	if at != nil {
		when = ts(*at)
	}
	args = append(args, when, when, when)
	rows, err := db.Query(`SELECT r.id, r.from_entity_id, f.canonical, r.rel_type, r.to_entity_id, t.canonical, r.status, r.confidence, r.valid_from, r.valid_to, r.evidence_ids, r.claim_ids, r.note, r.basis, r.causal_basis, r.causal_note, r.graded_by
		FROM intel_relationships r JOIN entities f ON f.id = r.from_entity_id JOIN entities t ON t.id = r.to_entity_id
		WHERE r.project_id = ? AND (r.from_entity_id IN (`+marks+`) OR r.to_entity_id IN (`+marks+`))
		AND (r.valid_from IS NULL OR r.valid_from <= ?) AND (r.valid_to IS NULL OR r.valid_to > ?)
		AND (r.retracted_at IS NULL OR r.retracted_at > ?) ORDER BY r.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Relationship{}
	for rows.Next() {
		var r Relationship
		var conf sql.NullFloat64
		var from, to flexibleTime
		var ev, cl string
		if err := rows.Scan(&r.ID, &r.From, &r.FromName, &r.Type, &r.To, &r.ToName, &r.Status, &conf, &from, &to, &ev, &cl, &r.Note, &r.Basis, &r.CausalBasis, &r.CausalNote, &r.GradedBy); err != nil {
			return nil, err
		}
		if conf.Valid {
			v := conf.Float64
			r.Confidence = &v
		}
		r.ValidFrom, r.ValidTo = from.Time(), to.Time()
		_ = json.Unmarshal([]byte(ev), &r.EvidenceIDs)
		_ = json.Unmarshal([]byte(cl), &r.ClaimIDs)
		out = append(out, r)
	}
	return out, rows.Err()
}

func retractRelationship(db *sql.DB, pid string, id int64) error {
	res, err := db.Exec(`UPDATE intel_relationships SET retracted_at = ? WHERE project_id = ? AND id = ? AND retracted_at IS NULL`, ts(nowUTC()), pid, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("relationship %d not found or already retracted", id)
	}
	return nil
}

// ─── Claims ────────────────────────────────────────────────────────

type Claim struct {
	ID         int64      `json:"id"`
	EvidenceID int64      `json:"evidence_id"`
	SubjectID  *int64     `json:"subject_entity_id,omitempty"`
	Subject    string     `json:"subject"`
	Predicate  string     `json:"predicate"`
	ObjectID   *int64     `json:"object_entity_id,omitempty"`
	Object     string     `json:"object"`
	Value      *float64   `json:"value,omitempty"`
	Unit       string     `json:"unit,omitempty"`
	ClaimTime  *time.Time `json:"claim_time,omitempty"`
	Confidence *float64   `json:"confidence,omitempty"`
	Extractor  string     `json:"extractor"`
	Status     string     `json:"status"`
	Basis      string     `json:"basis"`
}

func insertClaim(db *sql.DB, pid string, c Claim) (int64, error) {
	if c.EvidenceID == 0 || strings.TrimSpace(c.Subject) == "" || strings.TrimSpace(c.Predicate) == "" {
		return 0, errors.New("claim needs evidence_id, subject and predicate")
	}
	var id int64
	err := db.QueryRow(`INSERT INTO intel_claims (project_id, evidence_id, subject_entity_id, subject, predicate, object_entity_id, object, value, unit, claim_time, confidence, extractor, status, basis)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?) RETURNING id`, pid, c.EvidenceID, ptrInt64(c.SubjectID), c.Subject, c.Predicate, ptrInt64(c.ObjectID), c.Object,
		fptr(c.Value), c.Unit, timeString(c.ClaimTime), fptr(c.Confidence), c.Extractor, fallback(c.Status, "proposed"), fallback(c.Basis, basisInferred)).Scan(&id)
	return id, err
}

func listClaims(db *sql.DB, pid string, evidenceID, entityID int64, status string, limit int) ([]Claim, error) {
	where, args := []string{"project_id = ?"}, []any{pid}
	if evidenceID > 0 {
		where, args = append(where, "evidence_id = ?"), append(args, evidenceID)
	}
	if entityID > 0 {
		where, args = append(where, "(subject_entity_id = ? OR object_entity_id = ?)"), append(args, entityID, entityID)
	}
	if status != "" {
		where, args = append(where, "status = ?"), append(args, status)
	}
	rows, err := db.Query(`SELECT id, evidence_id, subject_entity_id, subject, predicate, object_entity_id, object, value, unit, claim_time, confidence, extractor, status, basis
		FROM intel_claims WHERE `+strings.Join(where, " AND ")+` ORDER BY id DESC LIMIT ?`, append(args, clampLimit(limit, 100, 500))...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Claim{}
	for rows.Next() {
		var c Claim
		var subj, obj sql.NullInt64
		var val, conf sql.NullFloat64
		var at flexibleTime
		if err := rows.Scan(&c.ID, &c.EvidenceID, &subj, &c.Subject, &c.Predicate, &obj, &c.Object, &val, &c.Unit, &at, &conf, &c.Extractor, &c.Status, &c.Basis); err != nil {
			return nil, err
		}
		if subj.Valid {
			v := subj.Int64
			c.SubjectID = &v
		}
		if obj.Valid {
			v := obj.Int64
			c.ObjectID = &v
		}
		if val.Valid {
			v := val.Float64
			c.Value = &v
		}
		if conf.Valid {
			v := conf.Float64
			c.Confidence = &v
		}
		c.ClaimTime = at.Time()
		out = append(out, c)
	}
	return out, rows.Err()
}

func setClaimStatus(db *sql.DB, pid string, id int64, status string) error {
	if status != "accepted" && status != "rejected" && status != "proposed" {
		return errors.New("status must be accepted, rejected or proposed")
	}
	res, err := db.Exec(`UPDATE intel_claims SET status = ? WHERE project_id = ? AND id = ?`, status, pid, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("claim %d not found", id)
	}
	return nil
}

// ─── State values ──────────────────────────────────────────────────

type StateValue struct {
	EntityID   int64     `json:"entity_id"`
	Variable   string    `json:"variable"`
	Value      float64   `json:"value"`
	Unit       string    `json:"unit"`
	ValidAt    time.Time `json:"valid_at"`
	EvidenceID *int64    `json:"evidence_id,omitempty"`
	Source     string    `json:"source"`
	Basis      string    `json:"basis"`
}

// putStateValue is idempotent per (entity, variable, valid_at, source).
func putStateValue(db *sql.DB, pid string, v StateValue) (bool, error) {
	res, err := db.Exec(`INSERT INTO intel_state_values (project_id, entity_id, variable, value, unit, valid_at, evidence_id, source, ingested_at, basis)
		VALUES (?,?,?,?,?,?,?,?,?,?) ON CONFLICT(project_id, entity_id, variable, valid_at, source) DO NOTHING`,
		pid, v.EntityID, v.Variable, v.Value, v.Unit, ts(v.ValidAt), ptrInt64(v.EvidenceID), v.Source, ts(nowUTC()), fallback(v.Basis, basisObserved))
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func stateSeries(db *sql.DB, pid string, entityID int64, variable string, limit int) ([]map[string]any, error) {
	where, args := []string{"project_id = ?", "entity_id = ?"}, []any{pid, entityID}
	if variable != "" {
		where, args = append(where, "variable = ?"), append(args, variable)
	}
	rows, err := db.Query(`SELECT variable, value, unit, valid_at, source, evidence_id, basis FROM intel_state_values WHERE `+strings.Join(where, " AND ")+
		` ORDER BY valid_at DESC LIMIT ?`, append(args, clampLimit(limit, 60, 1000))...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var variable, unit, source, basis string
		var value float64
		var at flexibleTime
		var ev sql.NullInt64
		if err := rows.Scan(&variable, &value, &unit, &at, &source, &ev, &basis); err != nil {
			return nil, err
		}
		row := map[string]any{"variable": variable, "value": value, "unit": unit, "valid_at": timeString(at.Time()), "source": source, "basis": basis}
		if ev.Valid {
			row["evidence_id"] = ev.Int64
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// ─── Questions ─────────────────────────────────────────────────────

type Question struct {
	ID               int64      `json:"id"`
	Origin           string     `json:"origin"`
	Venue            string     `json:"venue,omitempty"`
	ExternalID       string     `json:"external_id,omitempty"`
	URL              string     `json:"url,omitempty"`
	Text             string     `json:"text"`
	ResolutionRule   string     `json:"resolution_rule"`
	ResolutionSource string     `json:"resolution_source"`
	Category         string     `json:"category,omitempty"`
	EventKey         string     `json:"event_key,omitempty"`
	Template         string     `json:"-"`
	Topics           []string   `json:"topics"`
	EntityIDs        []int64    `json:"entity_ids"`
	Deadline         time.Time  `json:"deadline"`
	Status           string     `json:"status"`
	Outcome          *int       `json:"outcome,omitempty"`
	ResolvedAt       *time.Time `json:"resolved_at,omitempty"`
	ResolutionNote   string     `json:"resolution_note,omitempty"`
	LastEvidenceAt   *time.Time `json:"last_evidence_at,omitempty"`
	CreatedAt        *time.Time `json:"created_at,omitempty"`
}

const questionCols = `id, origin, venue, external_id, url, text, resolution_rule, resolution_source, category, event_key, template, topics, entity_ids, deadline, status, outcome, resolved_at, resolution_note, last_evidence_at, created_at`

type rowScanner interface{ Scan(dest ...any) error }

func scanQuestion(r rowScanner) (*Question, error) {
	var q Question
	var topics, ents string
	var deadline, resolvedAt, lastEv, created flexibleTime
	var outcome sql.NullInt64
	if err := r.Scan(&q.ID, &q.Origin, &q.Venue, &q.ExternalID, &q.URL, &q.Text, &q.ResolutionRule, &q.ResolutionSource, &q.Category, &q.EventKey, &q.Template,
		&topics, &ents, &deadline, &q.Status, &outcome, &resolvedAt, &q.ResolutionNote, &lastEv, &created); err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(topics), &q.Topics)
	_ = json.Unmarshal([]byte(ents), &q.EntityIDs)
	if q.Topics == nil {
		q.Topics = []string{}
	}
	if q.EntityIDs == nil {
		q.EntityIDs = []int64{}
	}
	if d := deadline.Time(); d != nil {
		q.Deadline = *d
	}
	if outcome.Valid {
		v := int(outcome.Int64)
		q.Outcome = &v
	}
	q.ResolvedAt, q.LastEvidenceAt, q.CreatedAt = resolvedAt.Time(), lastEv.Time(), created.Time()
	return &q, nil
}

// insertQuestion creates a question; for market questions an existing
// (venue, external_id) row is returned unchanged — rules are frozen once
// recorded.
func insertQuestion(db *sql.DB, pid string, q Question) (*Question, bool, error) {
	q.Text = strings.TrimSpace(q.Text)
	if q.Text == "" {
		return nil, false, errors.New("question text required")
	}
	if q.Deadline.IsZero() {
		return nil, false, errors.New("deadline required")
	}
	if q.Origin == "manual" && strings.TrimSpace(q.ResolutionRule) == "" {
		return nil, false, errors.New("manual questions need a resolution_rule stating exactly what counts as YES")
	}
	if q.ExternalID != "" {
		existing, err := scanQuestion(db.QueryRow(`SELECT `+questionCols+` FROM intel_questions WHERE project_id = ? AND venue = ? AND external_id = ?`, pid, q.Venue, q.ExternalID))
		if err == nil {
			return existing, false, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, false, err
		}
	}
	if q.Topics == nil {
		q.Topics = questionTopics(q.Text)
	}
	if q.EntityIDs == nil {
		q.EntityIDs = []int64{}
	}
	err := db.QueryRow(`INSERT INTO intel_questions (project_id, origin, venue, external_id, url, text, resolution_rule, resolution_source, category, event_key, template, topics, entity_ids, deadline, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) RETURNING id`, pid, q.Origin, q.Venue, q.ExternalID, q.URL, q.Text, q.ResolutionRule, q.ResolutionSource, q.Category, q.EventKey, q.Template,
		mustJSON(q.Topics), mustJSON(q.EntityIDs), ts(q.Deadline), ts(nowUTC())).Scan(&q.ID)
	if err != nil {
		return nil, false, err
	}
	q.Status = "open"
	return &q, true, nil
}

func getQuestion(db *sql.DB, pid string, id int64) (*Question, error) {
	q, err := scanQuestion(db.QueryRow(`SELECT `+questionCols+` FROM intel_questions WHERE project_id = ? AND id = ?`, pid, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("question %d not found", id)
	}
	return q, err
}

func listQuestions(db *sql.DB, pid, status string, limit int) ([]Question, error) {
	where, args := []string{"project_id = ?"}, []any{pid}
	order := "deadline ASC"
	if status != "" && status != "all" {
		where, args = append(where, "status = ?"), append(args, status)
		if status != "open" {
			order = "COALESCE(resolved_at, deadline) DESC"
		}
	}
	rows, err := db.Query(`SELECT `+questionCols+` FROM intel_questions WHERE `+strings.Join(where, " AND ")+` ORDER BY `+order+` LIMIT ?`, append(args, clampLimit(limit, 100, 1000))...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Question{}
	for rows.Next() {
		q, err := scanQuestion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *q)
	}
	return out, rows.Err()
}

func setQuestionEntities(db *sql.DB, pid string, qid int64, ids []int64) error {
	_, err := db.Exec(`UPDATE intel_questions SET entity_ids = ? WHERE project_id = ? AND id = ?`, mustJSON(ids), pid, qid)
	return err
}

func resolveQuestionRow(db *sql.DB, pid string, qid int64, status string, outcome *int, note string, at time.Time) error {
	if status != "resolved" && status != "void" {
		return errors.New("status must be resolved or void")
	}
	if status == "resolved" && (outcome == nil || (*outcome != 0 && *outcome != 1)) {
		return errors.New("resolved questions need outcome 0 or 1")
	}
	var out any
	if outcome != nil {
		out = *outcome
	}
	res, err := db.Exec(`UPDATE intel_questions SET status = ?, outcome = ?, resolved_at = ?, resolution_note = ? WHERE project_id = ? AND id = ? AND status = 'open'`,
		status, out, ts(at), note, pid, qid)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("question %d is not open", qid)
	}
	return nil
}

func addQuestionPrice(db *sql.DB, qid int64, price float64, at time.Time, source string) error {
	if price < 0 || price > 1 {
		return fmt.Errorf("price %v outside [0,1]", price)
	}
	// Skip unchanged consecutive prices so the series stays compact.
	var last sql.NullFloat64
	_ = db.QueryRow(`SELECT price FROM intel_question_prices WHERE question_id = ? ORDER BY observed_at DESC, id DESC LIMIT 1`, qid).Scan(&last)
	if last.Valid && last.Float64 == price {
		return nil
	}
	_, err := db.Exec(`INSERT INTO intel_question_prices (question_id, price, observed_at, source) VALUES (?,?,?,?)`, qid, price, ts(at), source)
	return err
}

// priceAt returns the latest market price observed at or before t.
func priceAt(db *sql.DB, qid int64, t time.Time) (*float64, error) {
	var p sql.NullFloat64
	err := db.QueryRow(`SELECT price FROM intel_question_prices WHERE question_id = ? AND observed_at <= ? ORDER BY observed_at DESC, id DESC LIMIT 1`, qid, ts(t)).Scan(&p)
	if errors.Is(err, sql.ErrNoRows) || !p.Valid {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	v := p.Float64
	return &v, nil
}

func priceSeries(db *sql.DB, qid int64) ([]map[string]any, error) {
	rows, err := db.Query(`SELECT price, observed_at, source FROM intel_question_prices WHERE question_id = ? ORDER BY observed_at`, qid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var p float64
		var at flexibleTime
		var src string
		if err := rows.Scan(&p, &at, &src); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"price": p, "observed_at": timeString(at.Time()), "source": src})
	}
	return out, rows.Err()
}

func linkQuestionEvidence(db *sql.DB, qid, evidenceID int64, relevance float64) (bool, error) {
	res, err := db.Exec(`INSERT INTO intel_question_evidence (question_id, evidence_id, relevance, linked_at) VALUES (?,?,?,?) ON CONFLICT DO NOTHING`,
		qid, evidenceID, relevance, ts(nowUTC()))
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		_, _ = db.Exec(`UPDATE intel_questions SET last_evidence_at = ? WHERE id = ?`, ts(nowUTC()), qid)
	}
	return n > 0, nil
}

// questionEvidenceIDs returns linked evidence that had been ingested by
// cutoff, most relevant first. Ingestion time, not source time, decides
// what a forecast could have known.
func questionEvidenceIDs(db *sql.DB, pid string, qid int64, cutoff time.Time, limit int) ([]int64, error) {
	rows, err := db.Query(`SELECT e.id FROM intel_question_evidence qe JOIN intelligence_evidence e ON e.id = qe.evidence_id
		WHERE qe.question_id = ? AND e.project_id = ? AND COALESCE(e.ingested_at, e.observed_at) <= ? AND qe.linked_at <= ?
		  AND e.basis <> 'synthetic' AND e.access <> 'restricted'
		ORDER BY qe.relevance DESC, COALESCE(e.published_time, e.event_time, e.ingested_at) DESC LIMIT ?`, qid, pid, ts(cutoff), ts(cutoff), clampLimit(limit, 20, 200))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ─── Forecasts ─────────────────────────────────────────────────────

type Forecast struct {
	ID             int64     `json:"id"`
	QuestionID     int64     `json:"question_id"`
	Revision       int       `json:"revision"`
	Probability    *float64  `json:"probability"`
	Abstained      bool      `json:"abstained"`
	AbstainReason  string    `json:"abstain_reason,omitempty"`
	Prior          *float64  `json:"prior"`
	MarketPrice    *float64  `json:"market_price"`
	Adjustment     float64   `json:"adjustment"`
	EvidenceCutoff time.Time `json:"evidence_cutoff"`
	SnapshotHash   string    `json:"snapshot_hash"`
	EvidenceIDs    []int64   `json:"evidence_ids"`
	Model          string    `json:"model"`
	ModelVersion   string    `json:"model_version"`
	Assumptions    []string  `json:"assumptions"`
	Explanation    string    `json:"explanation"`
	Trigger        string    `json:"trigger"`
	CreatedAt      time.Time `json:"created_at"`
}

func insertForecast(db *sql.DB, pid string, f *Forecast) error {
	var last sql.NullInt64
	if err := db.QueryRow(`SELECT MAX(revision) FROM intel_forecasts WHERE question_id = ?`, f.QuestionID).Scan(&last); err != nil {
		return err
	}
	f.Revision = int(last.Int64) + 1
	if f.EvidenceIDs == nil {
		f.EvidenceIDs = []int64{}
	}
	if f.Assumptions == nil {
		f.Assumptions = []string{}
	}
	return db.QueryRow(`INSERT INTO intel_forecasts (project_id, question_id, revision, probability, abstained, abstain_reason, prior, market_price, adjustment,
		evidence_cutoff, snapshot_hash, evidence_ids, model, model_version, assumptions, explanation, trigger, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) RETURNING id`, pid, f.QuestionID, f.Revision, fptr(f.Probability), boolInt(f.Abstained), f.AbstainReason,
		fptr(f.Prior), fptr(f.MarketPrice), f.Adjustment, ts(f.EvidenceCutoff), f.SnapshotHash, mustJSON(f.EvidenceIDs), f.Model, f.ModelVersion,
		mustJSON(f.Assumptions), f.Explanation, f.Trigger, ts(f.CreatedAt)).Scan(&f.ID)
}

const forecastCols = `id, question_id, revision, probability, abstained, abstain_reason, prior, market_price, adjustment, evidence_cutoff, snapshot_hash, evidence_ids, model, model_version, assumptions, explanation, trigger, created_at`

func scanForecast(r rowScanner) (*Forecast, error) {
	var f Forecast
	var prob, prior, mkt sql.NullFloat64
	var abst int
	var cutoff, created flexibleTime
	var ev, as string
	if err := r.Scan(&f.ID, &f.QuestionID, &f.Revision, &prob, &abst, &f.AbstainReason, &prior, &mkt, &f.Adjustment, &cutoff, &f.SnapshotHash, &ev,
		&f.Model, &f.ModelVersion, &as, &f.Explanation, &f.Trigger, &created); err != nil {
		return nil, err
	}
	for _, p := range []struct {
		src sql.NullFloat64
		dst **float64
	}{{prob, &f.Probability}, {prior, &f.Prior}, {mkt, &f.MarketPrice}} {
		if p.src.Valid {
			v := p.src.Float64
			*p.dst = &v
		}
	}
	f.Abstained = abst != 0
	if t := cutoff.Time(); t != nil {
		f.EvidenceCutoff = *t
	}
	if t := created.Time(); t != nil {
		f.CreatedAt = *t
	}
	_ = json.Unmarshal([]byte(ev), &f.EvidenceIDs)
	_ = json.Unmarshal([]byte(as), &f.Assumptions)
	return &f, nil
}

func forecastsFor(db *sql.DB, qid int64) ([]Forecast, error) {
	rows, err := db.Query(`SELECT `+forecastCols+` FROM intel_forecasts WHERE question_id = ? ORDER BY revision`, qid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Forecast{}
	for rows.Next() {
		f, err := scanForecast(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *f)
	}
	return out, rows.Err()
}

func latestForecast(db *sql.DB, qid int64) (*Forecast, error) {
	f, err := scanForecast(db.QueryRow(`SELECT `+forecastCols+` FROM intel_forecasts WHERE question_id = ? ORDER BY revision DESC LIMIT 1`, qid))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return f, err
}

func recentForecasts(db *sql.DB, pid string, since time.Time, limit int) ([]Forecast, error) {
	rows, err := db.Query(`SELECT `+forecastCols+` FROM intel_forecasts WHERE project_id = ? AND created_at >= ? ORDER BY created_at DESC, id DESC LIMIT ?`,
		pid, ts(since), clampLimit(limit, 50, 500))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Forecast{}
	for rows.Next() {
		f, err := scanForecast(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *f)
	}
	return out, rows.Err()
}

// ─── Topics ────────────────────────────────────────────────────────

var topicStopwords = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`a an the and or of in on at to for by with from as is are be will would can could should was were been being
		this that these those it its than then there their what which who whom whose when where why how before after during above below
		over under more less most least any each every all both either neither not no yes than into onto upon between through about
		against within without per vs versus win wins winner reach reaches hit hits above end ends ending close closes market price
		before by december january february march april may june july august september october november 2025 2026 2027
		do does did have has had get gets out up down new next first last least most higher lower`) {
		topicStopwords[w] = true
	}
}

// questionTopics extracts search terms from question text: quoted phrases
// and capitalized runs first (names), then distinctive words. It is a
// deterministic starting point; operators and the LLM extractor refine it.
func questionTopics(text string) []string {
	seen := map[string]bool{}
	out := []string{}
	add := func(t string) {
		t = strings.Trim(t, " ?.,:;!\"'()")
		k := strings.ToLower(t)
		if len(t) < 3 || seen[k] || topicStopwords[k] {
			return
		}
		seen[k] = true
		out = append(out, t)
	}
	words := strings.Fields(text)
	run := []string{}
	flush := func() {
		if len(run) > 0 {
			add(strings.Join(run, " "))
			run = run[:0]
		}
	}
	for i, w := range words {
		clean := strings.Trim(w, " ?.,:;!\"'()")
		if clean == "" {
			flush()
			continue
		}
		isCap := clean[0] >= 'A' && clean[0] <= 'Z'
		if isCap && !(i == 0 && topicStopwords[strings.ToLower(clean)]) && !topicStopwords[strings.ToLower(clean)] {
			run = append(run, clean)
		} else {
			flush()
		}
	}
	flush()
	if len(out) == 0 {
		cands := []string{}
		for _, w := range words {
			w = strings.ToLower(strings.Trim(w, " ?.,:;!\"'()"))
			if len(w) >= 4 && !topicStopwords[w] {
				cands = append(cands, w)
			}
		}
		sort.SliceStable(cands, func(i, j int) bool { return len(cands[i]) > len(cands[j]) })
		for _, c := range cands {
			add(c)
		}
	}
	if len(out) > 4 {
		out = out[:4]
	}
	return out
}
