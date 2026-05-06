package main

// DB layer for the events table. Generic CRUD + a few aggregate
// helpers — query / count / top-N / topics. Filter is the shared
// shape; each function builds a WHERE clause from it.

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

// EventRow mirrors one events row, with nullable columns flattened
// to zero values for ergonomics on the wire. JSON-encodable directly.
type EventRow struct {
	ID        int64           `json:"id"`
	TS        int64           `json:"ts"`
	App       string          `json:"app"`
	Topic     string          `json:"topic"`
	ProjectID string          `json:"project_id,omitempty"`
	InstallID int64           `json:"install_id,omitempty"`
	UserID    string          `json:"user_id,omitempty"`
	SessionID string          `json:"session_id,omitempty"`
	Source    string          `json:"source"`
	Props     json.RawMessage `json:"props"`
}

// EventInsert is what handlers hand to insertEvent. Empty-string fields
// become NULL in the DB; zero InstallID becomes NULL.
type EventInsert struct {
	TS        int64
	App       string
	Topic     string
	ProjectID string
	InstallID int64
	UserID    string
	SessionID string
	Source    string // "auto" | "track"
	Props     string // JSON-encoded; "" → "{}"
}

func insertEvent(db *sql.DB, ev EventInsert) (int64, error) {
	if ev.Props == "" {
		ev.Props = "{}"
	}
	res, err := db.Exec(`
		INSERT INTO events (ts, app, topic, project_id, install_id, user_id, session_id, source, props)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		ev.TS, ev.App, ev.Topic,
		nullStr(ev.ProjectID), nullInt(ev.InstallID),
		nullStr(ev.UserID), nullStr(ev.SessionID),
		ev.Source, ev.Props,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// Filter is the shared filter shape across query / count / top.
type Filter struct {
	App       string
	Topic     string
	ProjectID string
	Since     int64 // unix ms; 0 = no lower bound
	Until     int64 // unix ms; 0 = no upper bound

	// Where keys must be of the form "props.<jsonkey>" — equality only.
	// Other keys are silently ignored to keep the surface small.
	Where map[string]any
}

// buildWhere returns the WHERE clause (no leading WHERE) and the
// arg list. Empty conds → empty string.
func (f Filter) buildWhere() (string, []any) {
	var conds []string
	var args []any
	if f.App != "" {
		conds = append(conds, "app = ?")
		args = append(args, f.App)
	}
	if f.Topic != "" {
		conds = append(conds, "topic = ?")
		args = append(args, f.Topic)
	}
	if f.ProjectID != "" {
		conds = append(conds, "project_id = ?")
		args = append(args, f.ProjectID)
	}
	if f.Since > 0 {
		conds = append(conds, "ts >= ?")
		args = append(args, f.Since)
	}
	if f.Until > 0 {
		conds = append(conds, "ts < ?")
		args = append(args, f.Until)
	}
	for k, v := range f.Where {
		expr, ok := propsExtract(k)
		if !ok {
			continue
		}
		conds = append(conds, expr+" = ?")
		args = append(args, fmt.Sprint(v))
	}
	if len(conds) == 0 {
		return "", nil
	}
	return strings.Join(conds, " AND "), args
}

// propsExtract returns a json_extract expression for a "props.<key>"
// reference, or false when key isn't safe to interpolate. Only
// alphanumerics, underscore, and dot-segments are accepted — that's
// enough for nested JSON paths like "props.user.id" without opening
// up SQL injection.
func propsExtract(key string) (string, bool) {
	if !strings.HasPrefix(key, "props.") {
		return "", false
	}
	path := strings.TrimPrefix(key, "props.")
	if path == "" || len(path) > 128 {
		return "", false
	}
	for _, r := range path {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '_' || r == '.') {
			return "", false
		}
	}
	// "$.user.id" — JSON path expression sqlite understands.
	return "json_extract(props, '$." + path + "')", true
}

func queryRows(db *sql.DB, f Filter, limit int) ([]EventRow, error) {
	where, args := f.buildWhere()
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	q := `SELECT id, ts, app, topic, project_id, install_id, user_id, session_id, source, props
	      FROM events`
	if where != "" {
		q += " WHERE " + where
	}
	q += " ORDER BY ts DESC LIMIT ?"
	args = append(args, limit)

	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []EventRow
	for rows.Next() {
		var r EventRow
		var pid, uid, sid sql.NullString
		var iid sql.NullInt64
		var props string
		if err := rows.Scan(&r.ID, &r.TS, &r.App, &r.Topic, &pid, &iid, &uid, &sid, &r.Source, &props); err != nil {
			return nil, err
		}
		r.ProjectID = pid.String
		r.UserID = uid.String
		r.SessionID = sid.String
		r.InstallID = iid.Int64
		r.Props = json.RawMessage(props)
		out = append(out, r)
	}
	return out, rows.Err()
}

// queryGrouped runs a GROUP BY over one or more "props.X" keys, plus
// optionally app/topic. Returns one row per bucket: {<key>: value, ...,
// count: N}. Limited to 1000 buckets.
func queryGrouped(db *sql.DB, f Filter, groupBy []string, limit int) ([]map[string]any, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}

	var selectExprs []string
	var groupExprs []string
	var labels []string
	for _, gb := range groupBy {
		switch {
		case gb == "app" || gb == "topic" || gb == "project_id" || gb == "source":
			selectExprs = append(selectExprs, gb)
			groupExprs = append(groupExprs, gb)
			labels = append(labels, gb)
		default:
			expr, ok := propsExtract(gb)
			if !ok {
				return nil, fmt.Errorf("group_by key %q must be a column or props.X (alnum/underscore/dot)", gb)
			}
			selectExprs = append(selectExprs, expr)
			groupExprs = append(groupExprs, expr)
			labels = append(labels, gb)
		}
	}
	if len(selectExprs) == 0 {
		return nil, fmt.Errorf("group_by required for grouped query")
	}

	where, args := f.buildWhere()
	q := "SELECT " + strings.Join(selectExprs, ", ") + ", COUNT(*) AS count FROM events"
	if where != "" {
		q += " WHERE " + where
	}
	q += " GROUP BY " + strings.Join(groupExprs, ", ") + " ORDER BY count DESC LIMIT ?"
	args = append(args, limit)

	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []map[string]any
	for rows.Next() {
		vals := make([]any, len(labels)+1)
		for i := range vals {
			var s sql.NullString
			vals[i] = &s
		}
		// Last col is count — read into int64 instead.
		var count int64
		vals[len(labels)] = &count
		if err := rows.Scan(vals...); err != nil {
			return nil, err
		}
		bucket := make(map[string]any, len(labels)+1)
		for i, label := range labels {
			ns := vals[i].(*sql.NullString)
			if ns.Valid {
				bucket[label] = ns.String
			} else {
				bucket[label] = nil
			}
		}
		bucket["count"] = count
		out = append(out, bucket)
	}
	return out, rows.Err()
}

func countEvents(db *sql.DB, f Filter) (int64, error) {
	where, args := f.buildWhere()
	q := "SELECT COUNT(*) FROM events"
	if where != "" {
		q += " WHERE " + where
	}
	var n int64
	if err := db.QueryRow(q, args...).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// topByPropsKey returns top-N values for a single "props.<key>" path,
// optionally filtered. NULL extraction (key absent in the JSON) is
// dropped from results.
func topByPropsKey(db *sql.DB, f Filter, by string, limit int) ([]map[string]any, error) {
	expr, ok := propsExtract(by)
	if !ok {
		return nil, fmt.Errorf("by must be props.X with alnum/underscore/dot, got %q", by)
	}
	if limit <= 0 {
		limit = 10
	}
	if limit > 200 {
		limit = 200
	}
	where, args := f.buildWhere()
	q := "SELECT " + expr + " AS value, COUNT(*) AS count FROM events"
	if where != "" {
		q += " WHERE " + where + " AND " + expr + " IS NOT NULL"
	} else {
		q += " WHERE " + expr + " IS NOT NULL"
	}
	q += " GROUP BY value ORDER BY count DESC LIMIT ?"
	args = append(args, limit)

	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []map[string]any
	for rows.Next() {
		var v sql.NullString
		var c int64
		if err := rows.Scan(&v, &c); err != nil {
			return nil, err
		}
		row := map[string]any{"count": c}
		if v.Valid {
			row["value"] = v.String
		} else {
			row["value"] = nil
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// listTopics returns one row per (app, topic) seen, with last_ts and
// count. Optionally filtered by app. Useful for dashboard pickers.
func listTopics(db *sql.DB, app string) ([]map[string]any, error) {
	q := `SELECT app, topic, MAX(ts) AS last_ts, COUNT(*) AS count
	      FROM events`
	var args []any
	if app != "" {
		q += " WHERE app = ?"
		args = append(args, app)
	}
	q += " GROUP BY app, topic ORDER BY app, topic"

	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []map[string]any
	for rows.Next() {
		var a, t string
		var lastTS, count int64
		if err := rows.Scan(&a, &t, &lastTS, &count); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{
			"app":     a,
			"topic":   t,
			"last_ts": lastTS,
			"count":   count,
		})
	}
	return out, rows.Err()
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullInt(n int64) any {
	if n == 0 {
		return nil
	}
	return n
}
