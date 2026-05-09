// Segments — saved filters over contacts.
//
// A segment definition is a JSON array of predicate entries that are
// AND-ed together. Each entry is one of:
//
//   - column-level filter: {field, op, value}
//     reuses CRM's existing buildFilterClause (same shape as
//     contacts_search filters).
//
//   - synthetic predicate: {predicate, ...args}
//     v0.5 supports tag_in / tag_not_in, attribute, last_activity_within,
//     channel_present, in_list / not_in_list, not_in_segment.
//
// The whole spec compiles to one SELECT with EXISTS / NOT EXISTS
// subqueries, returning matching contact_ids. Static segments freeze
// the result via segments_materialise → contact_segment_snapshots.
//
// What's deliberately NOT supported in v0.5:
//   - OR / NOT trees — a flat AND list covers ~95% of real use; the
//     not_in_* predicates handle inverse cases. Tree shape can come
//     when actual demand surfaces.
//   - cross-segment composition beyond not_in_segment (which is the
//     one that matters for "audience minus suppression list").
package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// ─── Domain type ──────────────────────────────────────────────────

type Segment struct {
	ID          int64           `json:"id"`
	ProjectID   string          `json:"project_id,omitempty"`
	ListID      *int64          `json:"list_id,omitempty"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Kind        string          `json:"kind"` // dynamic | static
	Definition  json.RawMessage `json:"definition,omitempty"`
	CachedCount *int64          `json:"cached_count,omitempty"`
	CachedAt    string          `json:"cached_at,omitempty"`
	ArchivedAt  string          `json:"archived_at,omitempty"`
	CreatedAt   string          `json:"created_at,omitempty"`
	UpdatedAt   string          `json:"updated_at,omitempty"`
}

// ─── Definition compiler ──────────────────────────────────────────

// compiledFilter is the SQL fragment + args produced by walking one
// segment definition. WHERE clause is contacts.* qualified so it can
// be embedded in larger queries (eval, count, materialise).
type compiledFilter struct {
	where []string
	args  []any
}

func (cf *compiledFilter) and(clause string, args ...any) {
	cf.where = append(cf.where, clause)
	cf.args = append(cf.args, args...)
}

// compileSegmentDefinition turns the JSON spec into SQL fragments.
// Walks each entry, dispatches by shape (field- vs predicate-style),
// and accumulates ANDed clauses + args. Always scopes contacts table
// as `c.*`.
func compileSegmentDefinition(pid string, listID *int64, def json.RawMessage) (*compiledFilter, error) {
	cf := &compiledFilter{
		where: []string{"c.project_id = ?", "c.deleted_at IS NULL", "(c.status IS NULL OR c.status = 'active')"},
		args:  []any{pid},
	}

	// Implicit list scope: a list_id-bound segment auto-ANDs the
	// in_list predicate.
	if listID != nil && *listID != 0 {
		cf.and(`EXISTS (SELECT 1 FROM contact_list_members m
				WHERE m.contact_id = c.id AND m.project_id = ? AND m.list_id = ?)`,
			pid, *listID)
	}

	if len(def) == 0 || string(def) == "null" {
		return cf, nil
	}

	var entries []map[string]any
	if err := json.Unmarshal(def, &entries); err != nil {
		// Allow a bare object too (single predicate convenience).
		var single map[string]any
		if err2 := json.Unmarshal(def, &single); err2 == nil && len(single) > 0 {
			entries = []map[string]any{single}
		} else {
			return nil, fmt.Errorf("definition must be a JSON array of predicates: %w", err)
		}
	}

	for i, e := range entries {
		if err := compilePredicate(cf, pid, e); err != nil {
			return nil, fmt.Errorf("predicate #%d: %w", i+1, err)
		}
	}
	return cf, nil
}

func compilePredicate(cf *compiledFilter, pid string, e map[string]any) error {
	if pred, _ := e["predicate"].(string); pred != "" {
		return compileSyntheticPredicate(cf, pid, pred, e)
	}
	// Field-level fallback: reuse CRM's existing column filter shape.
	field, _ := e["field"].(string)
	op, _ := e["op"].(string)
	if field == "" {
		return errors.New("predicate or field required")
	}
	clause, args, err := buildFilterClause(field, op, e["value"])
	if err != nil {
		return err
	}
	cf.and("c."+clause, args...)
	return nil
}

func compileSyntheticPredicate(cf *compiledFilter, pid, pred string, e map[string]any) error {
	switch pred {
	case "tag_in":
		tags := stringArrayArg(e, "tags")
		if len(tags) == 0 {
			return errors.New("tag_in: tags array required")
		}
		ph := strings.Repeat("?,", len(tags))
		ph = ph[:len(ph)-1]
		args := []any{pid}
		for _, t := range tags {
			args = append(args, t)
		}
		cf.and(`EXISTS (SELECT 1 FROM contact_tags t
				WHERE t.contact_id = c.id AND t.project_id = ?
				  AND t.tag_name IN (`+ph+`))`,
			args...)
		return nil

	case "tag_not_in":
		tags := stringArrayArg(e, "tags")
		if len(tags) == 0 {
			return errors.New("tag_not_in: tags array required")
		}
		ph := strings.Repeat("?,", len(tags))
		ph = ph[:len(ph)-1]
		args := []any{pid}
		for _, t := range tags {
			args = append(args, t)
		}
		cf.and(`NOT EXISTS (SELECT 1 FROM contact_tags t
				WHERE t.contact_id = c.id AND t.project_id = ?
				  AND t.tag_name IN (`+ph+`))`,
			args...)
		return nil

	case "attribute":
		// Value-typed lookup: join contact_attributes via def_id.
		key, _ := e["key"].(string)
		if key == "" {
			return errors.New("attribute: key required")
		}
		op, _ := e["op"].(string)
		if op == "" {
			op = "eq"
		}
		val := e["value"]
		// We don't know the type at definition compile time, so the
		// SQL handles all four value columns. The agent passes the
		// natural JSON shape (string, number, bool, date-string).
		col, casted, err := attrColumnForOp(op, val)
		if err != nil {
			return err
		}
		opSQL, opArgs, err := attrOpSQL(col, op, casted)
		if err != nil {
			return err
		}
		args := append([]any{pid, key}, opArgs...)
		cf.and(`EXISTS (SELECT 1 FROM contact_attributes ca
				JOIN contact_attribute_defs cad ON cad.id = ca.def_id
				WHERE ca.contact_id = c.id AND ca.project_id = ?
				  AND cad.key = ? AND `+opSQL+`)`,
			args...)
		return nil

	case "last_activity_within":
		days := intFromAny(e["days"])
		if days <= 0 {
			return errors.New("last_activity_within: days required (> 0)")
		}
		kind, _ := e["kind"].(string)
		args := []any{pid}
		extraKind := ""
		if kind != "" {
			extraKind = " AND a.kind = ?"
			args = append(args, kind)
		}
		// SQLite datetime comparison via ISO-8601 strings — works
		// because CRM persists occurred_at as RFC3339.
		cutoff := time.Now().UTC().Add(-time.Duration(days) * 24 * time.Hour).Format(time.RFC3339)
		args = append(args, cutoff)
		cf.and(`EXISTS (SELECT 1 FROM contact_activities a
				WHERE a.contact_id = c.id AND a.project_id = ?`+extraKind+`
				  AND a.occurred_at >= ?)`,
			args...)
		return nil

	case "channel_present":
		kind, _ := e["kind"].(string)
		switch kind {
		case "email":
			cf.and(`(c.primary_email IS NOT NULL AND c.primary_email <> '' OR EXISTS (
					SELECT 1 FROM contact_channels ch
					WHERE ch.contact_id = c.id AND ch.kind = 'email'))`)
		case "phone":
			cf.and(`(c.primary_phone IS NOT NULL AND c.primary_phone <> '' OR EXISTS (
					SELECT 1 FROM contact_channels ch
					WHERE ch.contact_id = c.id AND ch.kind = 'phone'))`)
		default:
			if kind == "" {
				return errors.New("channel_present: kind required")
			}
			cf.and(`EXISTS (SELECT 1 FROM contact_channels ch
					WHERE ch.contact_id = c.id AND ch.kind = ?)`,
				kind)
		}
		return nil

	case "in_list":
		listID := int64FromAny(e["list_id"])
		if listID == 0 {
			return errors.New("in_list: list_id required")
		}
		cf.and(`EXISTS (SELECT 1 FROM contact_list_members m
				WHERE m.contact_id = c.id AND m.project_id = ? AND m.list_id = ?)`,
			pid, listID)
		return nil

	case "not_in_list":
		listID := int64FromAny(e["list_id"])
		if listID == 0 {
			return errors.New("not_in_list: list_id required")
		}
		cf.and(`NOT EXISTS (SELECT 1 FROM contact_list_members m
				WHERE m.contact_id = c.id AND m.project_id = ? AND m.list_id = ?)`,
			pid, listID)
		return nil

	case "not_in_segment":
		segID := int64FromAny(e["segment_id"])
		if segID == 0 {
			return errors.New("not_in_segment: segment_id required")
		}
		// Two interpretations: dynamic segment (re-eval here would be
		// recursive — we cap recursion) or static (read snapshot).
		// v0.5 supports static-only: consult the snapshot table. If
		// the segment is dynamic with no snapshot, the predicate is a
		// no-op (empty exclusion set). Documented limitation.
		cf.and(`NOT EXISTS (SELECT 1 FROM contact_segment_snapshots s
				WHERE s.contact_id = c.id AND s.project_id = ? AND s.segment_id = ?)`,
			pid, segID)
		return nil
	}
	return fmt.Errorf("unknown predicate %q", pred)
}

// attrColumnForOp picks which value_* column to compare against
// based on the operator + the JSON value shape. CRM stores attribute
// values typed: value_text / value_number / value_date / value_bool.
func attrColumnForOp(op string, val any) (col string, casted any, err error) {
	switch v := val.(type) {
	case bool:
		return "value_bool", v, nil
	case float64:
		return "value_number", v, nil
	case int:
		return "value_number", float64(v), nil
	case int64:
		return "value_number", float64(v), nil
	case string:
		// is_null / contains / starts_with always go through text.
		// eq on a date-shaped string also goes through text since
		// we store dates as strings in v0.1.
		return "value_text", v, nil
	case nil:
		return "value_text", nil, nil
	}
	return "", nil, fmt.Errorf("attribute value: unsupported type %T", val)
}

func attrOpSQL(col, op string, val any) (string, []any, error) {
	prefix := "ca." + col
	switch op {
	case "eq", "":
		return prefix + " = ?", []any{val}, nil
	case "neq":
		return prefix + " != ?", []any{val}, nil
	case "gt":
		return prefix + " > ?", []any{val}, nil
	case "gte":
		return prefix + " >= ?", []any{val}, nil
	case "lt":
		return prefix + " < ?", []any{val}, nil
	case "lte":
		return prefix + " <= ?", []any{val}, nil
	case "contains":
		return prefix + " LIKE ?", []any{"%" + fmt.Sprint(val) + "%"}, nil
	case "starts_with":
		return prefix + " LIKE ?", []any{fmt.Sprint(val) + "%"}, nil
	case "is_null":
		return prefix + " IS NULL", nil, nil
	}
	return "", nil, fmt.Errorf("unknown attribute op %q", op)
}

// ─── DB helpers ───────────────────────────────────────────────────

func dbSegmentCreate(db *sql.DB, pid string, s *Segment) (*Segment, error) {
	if s.Name == "" {
		return nil, errors.New("name required")
	}
	if s.Kind == "" {
		s.Kind = "dynamic"
	}
	if s.Kind != "dynamic" && s.Kind != "static" {
		return nil, fmt.Errorf("kind must be dynamic or static, got %q", s.Kind)
	}
	if len(s.Definition) == 0 {
		s.Definition = json.RawMessage("[]")
	}
	// Compile-check the definition before persisting so the user
	// gets a clear error rather than a "evaluation failed" later.
	if _, err := compileSegmentDefinition(pid, s.ListID, s.Definition); err != nil {
		return nil, fmt.Errorf("invalid definition: %w", err)
	}
	var listIDArg any
	if s.ListID != nil && *s.ListID != 0 {
		listIDArg = *s.ListID
	}
	res, err := db.Exec(
		`INSERT INTO contact_segments
			(project_id, list_id, name, description, kind, definition_json,
			 created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
		pid, listIDArg, s.Name, nullStr(s.Description), s.Kind, string(s.Definition),
	)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, fmt.Errorf("segment name %q already in use", s.Name)
		}
		return nil, err
	}
	id, _ := res.LastInsertId()
	return dbSegmentGet(db, pid, id)
}

func dbSegmentGet(db *sql.DB, pid string, id int64) (*Segment, error) {
	row := db.QueryRow(
		`SELECT id, list_id, name, COALESCE(description,''), kind, definition_json,
				cached_count, COALESCE(cached_at,''),
				COALESCE(archived_at,''), created_at, updated_at
		 FROM contact_segments WHERE project_id = ? AND id = ?`,
		pid, id,
	)
	s := &Segment{}
	var listID sql.NullInt64
	var cached sql.NullInt64
	var defJSON string
	if err := row.Scan(&s.ID, &listID, &s.Name, &s.Description, &s.Kind, &defJSON,
		&cached, &s.CachedAt, &s.ArchivedAt, &s.CreatedAt, &s.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if listID.Valid {
		v := listID.Int64
		s.ListID = &v
	}
	if cached.Valid {
		v := cached.Int64
		s.CachedCount = &v
	}
	s.Definition = json.RawMessage(defJSON)
	return s, nil
}

func dbSegmentsAll(db *sql.DB, pid string, includeArchived bool) ([]*Segment, error) {
	where := "project_id = ?"
	if !includeArchived {
		where += " AND archived_at IS NULL"
	}
	rows, err := db.Query(
		`SELECT id, list_id, name, COALESCE(description,''), kind, definition_json,
				cached_count, COALESCE(cached_at,''),
				COALESCE(archived_at,''), created_at, updated_at
		 FROM contact_segments WHERE `+where+
			` ORDER BY name COLLATE NOCASE`,
		pid,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Segment{}
	for rows.Next() {
		s := &Segment{}
		var listID sql.NullInt64
		var cached sql.NullInt64
		var defJSON string
		if err := rows.Scan(&s.ID, &listID, &s.Name, &s.Description, &s.Kind, &defJSON,
			&cached, &s.CachedAt, &s.ArchivedAt, &s.CreatedAt, &s.UpdatedAt); err != nil {
			continue
		}
		if listID.Valid {
			v := listID.Int64
			s.ListID = &v
		}
		if cached.Valid {
			v := cached.Int64
			s.CachedCount = &v
		}
		s.Definition = json.RawMessage(defJSON)
		out = append(out, s)
	}
	return out, nil
}

func dbSegmentUpdate(db *sql.DB, pid string, id int64, patch map[string]any) (*Segment, error) {
	allowed := map[string]bool{
		"name": true, "description": true, "kind": true,
		"list_id": true, "definition": true,
	}
	sets := []string{}
	args := []any{}
	for k, v := range patch {
		if !allowed[k] {
			continue
		}
		switch k {
		case "definition":
			raw, err := json.Marshal(v)
			if err != nil {
				return nil, fmt.Errorf("definition: %w", err)
			}
			// Validate the new shape before persisting.
			if _, err := compileSegmentDefinition(pid, nil, raw); err != nil {
				return nil, fmt.Errorf("invalid definition: %w", err)
			}
			sets = append(sets, "definition_json = ?")
			args = append(args, string(raw))
			// Bust the cached count — definition changed.
			sets = append(sets, "cached_count = NULL", "cached_at = NULL")
		case "list_id":
			sets = append(sets, "list_id = ?")
			if v == nil {
				args = append(args, nil)
			} else {
				args = append(args, int64FromAny(v))
			}
		default:
			sets = append(sets, k+" = ?")
			if s, ok := v.(string); ok && s == "" {
				args = append(args, nil)
			} else {
				args = append(args, v)
			}
		}
	}
	if len(sets) == 0 {
		return dbSegmentGet(db, pid, id)
	}
	sets = append(sets, "updated_at = CURRENT_TIMESTAMP")
	args = append(args, pid, id)
	if _, err := db.Exec(
		`UPDATE contact_segments SET `+strings.Join(sets, ", ")+
			` WHERE project_id = ? AND id = ?`,
		args...,
	); err != nil {
		return nil, err
	}
	return dbSegmentGet(db, pid, id)
}

func dbSegmentArchive(db *sql.DB, pid string, id int64) error {
	_, err := db.Exec(
		`UPDATE contact_segments SET archived_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
		 WHERE project_id = ? AND id = ? AND archived_at IS NULL`,
		pid, id,
	)
	return err
}

// dbSegmentEval returns the contact ids matching a segment. Static
// segments read the snapshot; dynamic segments compile and run the
// definition.
func dbSegmentEval(db *sql.DB, pid string, s *Segment, limit int) ([]int64, int64, error) {
	if s == nil {
		return nil, 0, errors.New("segment required")
	}
	if s.Kind == "static" {
		return dbSegmentReadSnapshot(db, pid, s.ID, limit)
	}
	cf, err := compileSegmentDefinition(pid, s.ListID, s.Definition)
	if err != nil {
		return nil, 0, err
	}
	whereSQL := "WHERE " + strings.Join(cf.where, " AND ")

	// Total count first (no limit).
	var total int64
	row := db.QueryRow(`SELECT COUNT(*) FROM contacts c `+whereSQL, cf.args...)
	if err := row.Scan(&total); err != nil {
		return nil, 0, err
	}

	if limit <= 0 {
		limit = 200
	}
	if limit > 5000 {
		limit = 5000
	}
	rows, err := db.Query(
		`SELECT c.id FROM contacts c `+whereSQL+` ORDER BY c.id LIMIT ?`,
		append(cf.args, limit)...,
	)
	if err != nil {
		return nil, total, err
	}
	defer rows.Close()
	out := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err == nil {
			out = append(out, id)
		}
	}
	return out, total, nil
}

func dbSegmentReadSnapshot(db *sql.DB, pid string, segID int64, limit int) ([]int64, int64, error) {
	if limit <= 0 {
		limit = 200
	}
	var total int64
	row := db.QueryRow(
		`SELECT COUNT(*) FROM contact_segment_snapshots
		 WHERE project_id = ? AND segment_id = ?`,
		pid, segID,
	)
	if err := row.Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := db.Query(
		`SELECT contact_id FROM contact_segment_snapshots
		 WHERE project_id = ? AND segment_id = ?
		 ORDER BY contact_id LIMIT ?`,
		pid, segID, limit,
	)
	if err != nil {
		return nil, total, err
	}
	defer rows.Close()
	out := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err == nil {
			out = append(out, id)
		}
	}
	return out, total, nil
}

// dbSegmentMaterialise freezes a segment's current dynamic membership
// into the snapshot table. Used by campaigns at materialise-time so
// the audience doesn't shift mid-send. Idempotent: clears existing
// snapshot rows first, then re-inserts.
func dbSegmentMaterialise(db *sql.DB, pid string, s *Segment) (int64, error) {
	if s == nil {
		return 0, errors.New("segment required")
	}
	cf, err := compileSegmentDefinition(pid, s.ListID, s.Definition)
	if err != nil {
		return 0, err
	}
	whereSQL := "WHERE " + strings.Join(cf.where, " AND ")
	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(
		`DELETE FROM contact_segment_snapshots WHERE project_id = ? AND segment_id = ?`,
		pid, s.ID,
	); err != nil {
		return 0, err
	}
	res, err := tx.Exec(
		`INSERT INTO contact_segment_snapshots (segment_id, contact_id, project_id, snapshotted_at)
		 SELECT ?, c.id, ?, CURRENT_TIMESTAMP FROM contacts c `+whereSQL,
		append([]any{s.ID, pid}, cf.args...)...,
	)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	// Update the segment's cached_count to match.
	if _, err := tx.Exec(
		`UPDATE contact_segments SET cached_count = ?, cached_at = CURRENT_TIMESTAMP, kind = 'static', updated_at = CURRENT_TIMESTAMP
		 WHERE project_id = ? AND id = ?`,
		n, pid, s.ID,
	); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return n, nil
}

// dbSegmentCount is the lazy variant for the panel. Refreshes the
// cached count when older than 5 minutes; otherwise returns the cached
// value. Doesn't touch static segments — those carry an exact count
// from materialise time.
func dbSegmentCount(db *sql.DB, pid string, s *Segment) (int64, error) {
	if s == nil {
		return 0, errors.New("segment required")
	}
	if s.Kind == "static" {
		if s.CachedCount != nil {
			return *s.CachedCount, nil
		}
		// Fall through: count snapshot rows.
		var n int64
		row := db.QueryRow(
			`SELECT COUNT(*) FROM contact_segment_snapshots WHERE project_id = ? AND segment_id = ?`,
			pid, s.ID,
		)
		if err := row.Scan(&n); err != nil {
			return 0, err
		}
		return n, nil
	}

	const ttl = 5 * time.Minute
	if s.CachedCount != nil && s.CachedAt != "" {
		if t, err := time.Parse(time.RFC3339, s.CachedAt); err == nil {
			if time.Since(t) < ttl {
				return *s.CachedCount, nil
			}
		}
	}
	cf, err := compileSegmentDefinition(pid, s.ListID, s.Definition)
	if err != nil {
		return 0, err
	}
	whereSQL := "WHERE " + strings.Join(cf.where, " AND ")
	var n int64
	row := db.QueryRow(`SELECT COUNT(*) FROM contacts c `+whereSQL, cf.args...)
	if err := row.Scan(&n); err != nil {
		return 0, err
	}
	_, _ = db.Exec(
		`UPDATE contact_segments SET cached_count = ?, cached_at = CURRENT_TIMESTAMP
		 WHERE project_id = ? AND id = ?`,
		n, pid, s.ID,
	)
	return n, nil
}

// dbSegmentMembersFull is the list-of-contacts variant for the panel
// preview. Returns shaped Contact rows directly so the caller doesn't
// need a second round-trip per id.
func dbSegmentMembersFull(db *sql.DB, pid string, s *Segment, limit int) ([]*Contact, int64, error) {
	ids, total, err := dbSegmentEval(db, pid, s, limit)
	if err != nil {
		return nil, 0, err
	}
	if len(ids) == 0 {
		return []*Contact{}, total, nil
	}
	ph := strings.Repeat("?,", len(ids))
	ph = ph[:len(ph)-1]
	args := []any{pid}
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := db.Query(
		`SELECT id, COALESCE(first_name,''), COALESCE(last_name,''),
				COALESCE(display_name,''), COALESCE(primary_email,''),
				COALESCE(primary_phone,''), COALESCE(company,''),
				COALESCE(job_title,''), COALESCE(status,'active')
		 FROM contacts WHERE project_id = ? AND deleted_at IS NULL AND id IN (`+ph+`)
		 ORDER BY id`,
		args...,
	)
	if err != nil {
		return nil, total, err
	}
	defer rows.Close()
	out := []*Contact{}
	for rows.Next() {
		c := &Contact{}
		if err := rows.Scan(&c.ID, &c.FirstName, &c.LastName, &c.DisplayName,
			&c.PrimaryEmail, &c.PrimaryPhone, &c.Company, &c.JobTitle, &c.Status); err == nil {
			out = append(out, c)
		}
	}
	return out, total, nil
}

// ─── MCP tools ────────────────────────────────────────────────────

func (a *App) toolSegmentsCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	s := &Segment{
		Name:        strArg(args, "name"),
		Description: strArg(args, "description"),
		Kind:        strArg(args, "kind"),
	}
	if listID := int64Arg(args, "list_id"); listID != 0 {
		s.ListID = &listID
	}
	if def, ok := args["definition"]; ok {
		raw, _ := json.Marshal(def)
		s.Definition = raw
	}
	out, err := dbSegmentCreate(ctx.AppDB(), pid, s)
	if err != nil {
		return nil, err
	}
	ctx.Emit("segment.created", map[string]any{"id": out.ID, "name": out.Name, "kind": out.Kind})
	return map[string]any{"segment": out}, nil
}

func (a *App) toolSegmentsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	includeArchived, _ := args["include_archived"].(bool)
	out, err := dbSegmentsAll(ctx.AppDB(), pid, includeArchived)
	if err != nil {
		return nil, err
	}
	return map[string]any{"segments": out, "count": len(out)}, nil
}

func (a *App) toolSegmentsGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	s, err := dbSegmentGet(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, err
	}
	if s == nil {
		return map[string]any{"segment": nil, "found": false}, nil
	}
	return map[string]any{"segment": s, "found": true}, nil
}

func (a *App) toolSegmentsUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	patch, _ := args["patch"].(map[string]any)
	if patch == nil {
		return nil, errors.New("patch object required")
	}
	out, err := dbSegmentUpdate(ctx.AppDB(), pid, id, patch)
	if err != nil {
		return nil, err
	}
	ctx.Emit("segment.updated", map[string]any{"id": id})
	return map[string]any{"segment": out}, nil
}

func (a *App) toolSegmentsDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	if err := dbSegmentArchive(ctx.AppDB(), pid, id); err != nil {
		return nil, err
	}
	ctx.Emit("segment.archived", map[string]any{"id": id})
	return map[string]any{"archived": true, "id": id}, nil
}

func (a *App) toolSegmentsEval(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	s, err := dbSegmentGet(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, errors.New("segment not found")
	}
	limit := intArg(args, "limit", 200)
	ids, total, err := dbSegmentEval(ctx.AppDB(), pid, s, limit)
	if err != nil {
		return nil, err
	}
	return map[string]any{"contact_ids": ids, "count": total, "kind": s.Kind}, nil
}

func (a *App) toolSegmentsCount(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	s, err := dbSegmentGet(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, errors.New("segment not found")
	}
	n, err := dbSegmentCount(ctx.AppDB(), pid, s)
	if err != nil {
		return nil, err
	}
	return map[string]any{"count": n, "kind": s.Kind}, nil
}

func (a *App) toolSegmentsMaterialise(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	s, err := dbSegmentGet(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, errors.New("segment not found")
	}
	// Allow promoting dynamic → static via this tool.
	n, err := dbSegmentMaterialise(ctx.AppDB(), pid, s)
	if err != nil {
		return nil, err
	}
	ctx.Emit("segment.materialised", map[string]any{"id": id, "count": n})
	return map[string]any{"materialised": true, "id": id, "count": n, "kind": "static"}, nil
}

// ─── HTTP handlers ────────────────────────────────────────────────

func (a *App) handleHTTPSegments(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		a.handleHTTPSegmentsGet(w, r)
	case http.MethodPost:
		a.handleHTTPSegmentsCreate(w, r)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleHTTPSegmentItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/segments/")
	parts := strings.SplitN(rest, "/", 2)
	id, _ := strconv.ParseInt(parts[0], 10, 64)
	if id == 0 {
		httpErr(w, http.StatusBadRequest, "segment id required")
		return
	}
	if len(parts) >= 2 {
		switch parts[1] {
		case "eval":
			a.handleHTTPSegmentEval(w, r, id)
			return
		case "members":
			a.handleHTTPSegmentMembers(w, r, id)
			return
		case "materialise":
			a.handleHTTPSegmentMaterialise(w, r, id)
			return
		}
	}
	switch r.Method {
	case http.MethodGet:
		a.handleHTTPSegmentGet(w, r, id)
	case http.MethodPatch:
		a.handleHTTPSegmentUpdate(w, r, id)
	case http.MethodDelete:
		a.handleHTTPSegmentDelete(w, r, id)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleHTTPSegmentsGet(w http.ResponseWriter, r *http.Request) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	includeArchived := r.URL.Query().Get("include_archived") == "1" || r.URL.Query().Get("include_archived") == "true"
	out, err := dbSegmentsAll(globalCtx.AppDB(), pid, includeArchived)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"segments": out, "count": len(out)})
}

func (a *App) handleHTTPSegmentsCreate(w http.ResponseWriter, r *http.Request) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	s := &Segment{
		Name:        strArg(body, "name"),
		Description: strArg(body, "description"),
		Kind:        strArg(body, "kind"),
	}
	if listID := int64Arg(body, "list_id"); listID != 0 {
		s.ListID = &listID
	}
	if def, ok := body["definition"]; ok {
		raw, _ := json.Marshal(def)
		s.Definition = raw
	}
	out, err := dbSegmentCreate(globalCtx.AppDB(), pid, s)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	globalCtx.Emit("segment.created", map[string]any{"id": out.ID, "name": out.Name, "kind": out.Kind})
	httpJSON(w, map[string]any{"segment": out})
}

func (a *App) handleHTTPSegmentGet(w http.ResponseWriter, r *http.Request, id int64) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s, err := dbSegmentGet(globalCtx.AppDB(), pid, id)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if s == nil {
		httpErr(w, http.StatusNotFound, "segment not found")
		return
	}
	httpJSON(w, map[string]any{"segment": s})
}

func (a *App) handleHTTPSegmentUpdate(w http.ResponseWriter, r *http.Request, id int64) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var patch map[string]any
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	out, err := dbSegmentUpdate(globalCtx.AppDB(), pid, id, patch)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	globalCtx.Emit("segment.updated", map[string]any{"id": id})
	httpJSON(w, map[string]any{"segment": out})
}

func (a *App) handleHTTPSegmentDelete(w http.ResponseWriter, r *http.Request, id int64) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := dbSegmentArchive(globalCtx.AppDB(), pid, id); err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	globalCtx.Emit("segment.archived", map[string]any{"id": id})
	httpJSON(w, map[string]any{"archived": true, "id": id})
}

func (a *App) handleHTTPSegmentEval(w http.ResponseWriter, r *http.Request, id int64) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s, err := dbSegmentGet(globalCtx.AppDB(), pid, id)
	if err != nil || s == nil {
		httpErr(w, http.StatusNotFound, "segment not found")
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	ids, total, err := dbSegmentEval(globalCtx.AppDB(), pid, s, limit)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	httpJSON(w, map[string]any{"contact_ids": ids, "count": total, "kind": s.Kind})
}

func (a *App) handleHTTPSegmentMembers(w http.ResponseWriter, r *http.Request, id int64) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s, err := dbSegmentGet(globalCtx.AppDB(), pid, id)
	if err != nil || s == nil {
		httpErr(w, http.StatusNotFound, "segment not found")
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	out, total, err := dbSegmentMembersFull(globalCtx.AppDB(), pid, s, limit)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	httpJSON(w, map[string]any{"contacts": out, "count": total, "kind": s.Kind})
}

func (a *App) handleHTTPSegmentMaterialise(w http.ResponseWriter, r *http.Request, id int64) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s, err := dbSegmentGet(globalCtx.AppDB(), pid, id)
	if err != nil || s == nil {
		httpErr(w, http.StatusNotFound, "segment not found")
		return
	}
	n, err := dbSegmentMaterialise(globalCtx.AppDB(), pid, s)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	globalCtx.Emit("segment.materialised", map[string]any{"id": id, "count": n})
	httpJSON(w, map[string]any{"materialised": true, "id": id, "count": n, "kind": "static"})
}

// ─── Helpers (typed-arg pluckers) ─────────────────────────────────

func stringArrayArg(m map[string]any, key string) []string {
	v, ok := m[key]
	if !ok {
		return nil
	}
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, e := range arr {
		if s, ok := e.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}

func intFromAny(v any) int {
	switch x := v.(type) {
	case float64:
		return int(x)
	case int:
		return x
	case int64:
		return int(x)
	}
	return 0
}

func int64FromAny(v any) int64 {
	switch x := v.(type) {
	case float64:
		return int64(x)
	case int:
		return int64(x)
	case int64:
		return x
	case string:
		n, _ := strconv.ParseInt(x, 10, 64)
		return n
	}
	return 0
}
