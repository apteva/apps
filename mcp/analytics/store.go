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
	UpsertKey string          `json:"upsert_key,omitempty"`
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
	UpsertKey string
	Props     string // JSON-encoded; "" → "{}"
}

func insertEvent(db *sql.DB, ev EventInsert) (int64, error) {
	return insertEventWithPolicy(db, ev)
}

func insertEventRaw(db *sql.DB, ev EventInsert, validate bool) (int64, error) {
	if ev.Props == "" {
		ev.Props = "{}"
	}
	var validation validationOutcome
	if validate {
		var err error
		validation, err = validateEventInsert(db, ev)
		if err != nil {
			return 0, err
		}
	}
	if ev.UpsertKey != "" {
		_, err := db.Exec(`
			INSERT INTO events (ts, app, topic, project_id, install_id, user_id, session_id, source, upsert_key, props)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(project_id, app, topic, upsert_key) WHERE upsert_key IS NOT NULL
			DO UPDATE SET
				ts = excluded.ts,
				install_id = excluded.install_id,
				user_id = excluded.user_id,
				session_id = excluded.session_id,
				source = excluded.source,
				props = excluded.props
		`,
			ev.TS, ev.App, ev.Topic,
			ev.ProjectID, nullInt(ev.InstallID),
			nullStr(ev.UserID), nullStr(ev.SessionID),
			ev.Source, ev.UpsertKey, ev.Props,
		)
		if err != nil {
			return 0, err
		}
		var id int64
		err = db.QueryRow(
			`SELECT id FROM events WHERE project_id=? AND app=? AND topic=? AND upsert_key=?`,
			ev.ProjectID, ev.App, ev.Topic, ev.UpsertKey,
		).Scan(&id)
		if err == nil {
			recordEventSpecViolations(db, id, validation.Violations)
		}
		return id, err
	}
	res, err := db.Exec(`
		INSERT INTO events (ts, app, topic, project_id, install_id, user_id, session_id, source, upsert_key, props)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		ev.TS, ev.App, ev.Topic,
		nullStr(ev.ProjectID), nullInt(ev.InstallID),
		nullStr(ev.UserID), nullStr(ev.SessionID),
		ev.Source, nullStr(ev.UpsertKey), ev.Props,
	)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err == nil {
		recordEventSpecViolations(db, id, validation.Violations)
	}
	return id, err
}

// Filter is the shared filter shape across query / count / top.
type Filter struct {
	App       string
	Topic     string
	ProjectID string
	Source    string
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
	if f.Source != "" {
		conds = append(conds, "source = ?")
		args = append(args, f.Source)
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
	q := `SELECT id, ts, app, topic, project_id, install_id, user_id, session_id, source, upsert_key, props
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
		var upsertKey sql.NullString
		var props string
		if err := rows.Scan(&r.ID, &r.TS, &r.App, &r.Topic, &pid, &iid, &uid, &sid, &r.Source, &upsertKey, &props); err != nil {
			return nil, err
		}
		r.ProjectID = pid.String
		r.UserID = uid.String
		r.SessionID = sid.String
		r.InstallID = iid.Int64
		r.UpsertKey = upsertKey.String
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

// sumByValue sums a numeric column/path over matching events. With
// groupBy keys, returns one bucket per dimension combination; otherwise
// returns a single bucket with only sum/count. Intended for aggregate-
// observation events such as "post_views_daily_observed" where the
// value lives in props.views.
func sumByValue(db *sql.DB, f Filter, valueKey string, groupBy []string, limit int) ([]map[string]any, error) {
	valueExpr, ok := valueExtract(valueKey)
	if !ok {
		return nil, fmt.Errorf("value must be a numeric column or props.X, got %q", valueKey)
	}
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
		switch gb {
		case "app", "topic", "project_id", "source", "user_id", "session_id", "upsert_key":
			selectExprs = append(selectExprs, gb)
			groupExprs = append(groupExprs, gb)
			labels = append(labels, gb)
		default:
			expr, ok := propsExtract(gb)
			if !ok {
				return nil, fmt.Errorf("group_by key %q must be a column or props.X", gb)
			}
			selectExprs = append(selectExprs, expr)
			groupExprs = append(groupExprs, expr)
			labels = append(labels, gb)
		}
	}

	where, args := f.buildWhere()
	selectSQL := ""
	if len(selectExprs) > 0 {
		selectSQL = strings.Join(selectExprs, ", ") + ", "
	}
	q := "SELECT " + selectSQL + "SUM(CAST(" + valueExpr + " AS REAL)) AS sum, COUNT(*) AS count FROM events"
	if where != "" {
		q += " WHERE " + where + " AND " + valueExpr + " IS NOT NULL"
	} else {
		q += " WHERE " + valueExpr + " IS NOT NULL"
	}
	if len(groupExprs) > 0 {
		q += " GROUP BY " + strings.Join(groupExprs, ", ") + " ORDER BY sum DESC LIMIT ?"
		args = append(args, limit)
	}

	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []map[string]any
	for rows.Next() {
		vals := make([]any, len(labels)+2)
		for i := range labels {
			var s sql.NullString
			vals[i] = &s
		}
		var sum sql.NullFloat64
		var count int64
		vals[len(labels)] = &sum
		vals[len(labels)+1] = &count
		if err := rows.Scan(vals...); err != nil {
			return nil, err
		}
		row := map[string]any{"sum": sum.Float64, "count": count}
		for i, label := range labels {
			ns := vals[i].(*sql.NullString)
			if ns.Valid {
				row[label] = ns.String
			} else {
				row[label] = nil
			}
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func valueExtract(key string) (string, bool) {
	switch key {
	case "ts", "install_id":
		return key, true
	default:
		return propsExtract(key)
	}
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

// overview returns headline counts within the filter window: total
// events, distinct apps, and distinct (app, topic) pairs. Backs the
// panel's stat tiles. char(31) is the unit-separator — a delimiter that
// can't appear in app/topic, so the concat distinct-count is exact.
func overview(db *sql.DB, f Filter) (map[string]any, error) {
	where, args := f.buildWhere()
	q := "SELECT COUNT(*), COUNT(DISTINCT app), COUNT(DISTINCT app || char(31) || topic) FROM events"
	if where != "" {
		q += " WHERE " + where
	}
	var total, apps, topics int64
	if err := db.QueryRow(q, args...).Scan(&total, &apps, &topics); err != nil {
		return nil, err
	}
	return map[string]any{"total": total, "apps": apps, "topics": topics}, nil
}

// dailySeries returns event counts bucketed by UTC day within the
// window, oldest first: [{day:"2026-05-21", count:N}]. Days with zero
// events are absent — the panel fills gaps for the chart if it wants.
func dailySeries(db *sql.DB, f Filter) ([]map[string]any, error) {
	where, args := f.buildWhere()
	q := "SELECT strftime('%Y-%m-%d', ts / 1000, 'unixepoch') AS day, COUNT(*) AS count FROM events"
	if where != "" {
		q += " WHERE " + where
	}
	q += " GROUP BY day ORDER BY day"

	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []map[string]any
	for rows.Next() {
		var day string
		var count int64
		if err := rows.Scan(&day, &count); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"day": day, "count": count})
	}
	return out, rows.Err()
}

// topicsWindowed is listTopics constrained to the filter window and
// ordered by volume — the panel's topics table. listTopics stays the
// MCP-tool path (all-time, ordered by name).
func topicsWindowed(db *sql.DB, f Filter, limit int) ([]map[string]any, error) {
	where, args := f.buildWhere()
	q := `SELECT app, topic, MAX(ts) AS last_ts, COUNT(*) AS count
	      FROM events`
	if where != "" {
		q += " WHERE " + where
	}
	q += " GROUP BY app, topic ORDER BY count DESC LIMIT ?"
	args = append(args, limit)

	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []map[string]any
	for rows.Next() {
		var app, topic string
		var lastTS, count int64
		if err := rows.Scan(&app, &topic, &lastTS, &count); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{
			"app":     app,
			"topic":   topic,
			"last_ts": lastTS,
			"count":   count,
		})
	}
	return out, rows.Err()
}

// distinctDimensions returns the set of app and topic values seen for a
// project (or all when projectID == ""), for the panel's filter dropdowns.
func distinctDimensions(db *sql.DB, projectID string) (apps, topics []string, err error) {
	if apps, err = distinctColumn(db, "app", projectID); err != nil {
		return nil, nil, err
	}
	topics, err = distinctColumn(db, "topic", projectID)
	return apps, topics, err
}

// distinctColumn lists distinct non-null values of one column, optionally
// scoped to a project. col is a fixed identifier from a closed set (never
// user input) — guarded here so it can never become an injection vector.
func distinctColumn(db *sql.DB, col, projectID string) ([]string, error) {
	switch col {
	case "app", "topic":
	default:
		return nil, fmt.Errorf("distinctColumn: unsupported column %q", col)
	}
	q := "SELECT DISTINCT " + col + " FROM events WHERE " + col + " IS NOT NULL"
	var args []any
	if projectID != "" {
		q += " AND project_id = ?"
		args = append(args, projectID)
	}
	q += " ORDER BY " + col
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
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
