package engine

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"

	sqlite "modernc.org/sqlite"
)

type sqliteBackend struct{ db *sql.DB }
type sqliteTx struct {
	ctx context.Context
	tx  *sql.Tx
}

func openSQL(path string) (*sql.DB, error) {
	u := url.URL{Scheme: "file", Path: path}
	q := u.Query()
	for _, p := range []string{"journal_mode(WAL)", "synchronous(FULL)", "busy_timeout(5000)", "foreign_keys(ON)"} {
		q.Add("_pragma", p)
	}
	u.RawQuery = q.Encode()
	db, e := sql.Open("sqlite", u.String())
	if e != nil {
		return nil, e
	}
	db.SetMaxOpenConns(1)
	if e = db.Ping(); e != nil {
		db.Close()
		return nil, e
	}
	return db, nil
}
func openSQLite(path string) (backend, error) {
	db, e := openSQL(path)
	if e != nil {
		return nil, e
	}
	_, e = db.Exec(`CREATE TABLE IF NOT EXISTS __collections (name TEXT PRIMARY KEY, schema_json TEXT NOT NULL)`)
	if e != nil {
		db.Close()
		return nil, e
	}
	return &sqliteBackend{db}, nil
}
func (b *sqliteBackend) close() error { return b.db.Close() }
func (b *sqliteBackend) transaction(ctx context.Context, write bool, fn func(transaction) error) error {
	tx, e := b.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if e = fn(&sqliteTx{ctx, tx}); e == nil {
		if write {
			e = tx.Commit()
		} else {
			e = tx.Rollback()
		}
	}
	var se *sqlite.Error
	if errors.As(e, &se) && se.Code()&255 == 19 {
		return fail("unique_conflict", "primary or unique index constraint violated")
	}
	if e != nil && strings.Contains(e.Error(), "integer overflow") {
		return fail("resource_limit", "aggregate integer overflow")
	}
	return e
}
func quote(s string) string     { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }
func table(c Collection) string { return quote("c_" + c.Name) }
func sqlIndex(c Collection, n string) string {
	sum := sha256.Sum256([]byte(c.Name + "/" + n))
	return quote(fmt.Sprintf("i_%x", sum[:16]))
}
func (t *sqliteTx) collections() ([]Collection, error) {
	rows, e := t.tx.QueryContext(t.ctx, `SELECT schema_json FROM __collections ORDER BY name`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Collection{}
	for rows.Next() {
		var b []byte
		if e := rows.Scan(&b); e != nil {
			return nil, e
		}
		var c Collection
		if e := decode(b, &c); e != nil {
			return nil, e
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
func (t *sqliteTx) collection(name string) (Collection, error) {
	var c Collection
	var b []byte
	e := t.tx.QueryRowContext(t.ctx, `SELECT schema_json FROM __collections WHERE name=?`, name).Scan(&b)
	if errors.Is(e, sql.ErrNoRows) {
		return c, fail("not_found", "collection %s does not exist", name)
	}
	if e == nil {
		e = decode(b, &c)
	}
	return c, e
}
func sqlType(f Field) string {
	switch f.Type {
	case "integer", "boolean":
		return "INTEGER"
	case "number":
		return "REAL"
	}
	return "TEXT COLLATE BINARY"
}
func allFields(c Collection) []Field {
	return append(append([]Field{}, c.Fields...), Field{Name: "_created_at", Type: "datetime"}, Field{Name: "_updated_at", Type: "datetime"}, Field{Name: "_version", Type: "integer"})
}
func (t *sqliteTx) save(c Collection) error {
	_, e := t.tx.ExecContext(t.ctx, `INSERT INTO __collections VALUES(?,?) ON CONFLICT(name) DO UPDATE SET schema_json=excluded.schema_json`, c.Name, string(marshal(c)))
	return e
}
func (t *sqliteTx) createCollection(c Collection) error {
	defs := []string{`"__pk" TEXT PRIMARY KEY NOT NULL`, `"__doc" TEXT NOT NULL`}
	for _, f := range allFields(c) {
		s := quote(f.Name) + " " + sqlType(f)
		if !f.Nullable {
			s += " NOT NULL"
		}
		defs = append(defs, s)
	}
	_, e := t.tx.ExecContext(t.ctx, "CREATE TABLE "+table(c)+" ("+strings.Join(defs, ",")+")")
	if e != nil {
		return e
	}
	pk := []string{}
	for _, p := range c.PrimaryKey {
		pk = append(pk, quote(p))
	}
	if _, e = t.tx.ExecContext(t.ctx, "CREATE UNIQUE INDEX "+sqlIndex(c, "primary")+" ON "+table(c)+" ("+strings.Join(pk, ",")+")"); e != nil {
		return e
	}
	return t.save(c)
}
func (t *sqliteTx) dropCollection(c Collection) error {
	if _, e := t.tx.ExecContext(t.ctx, "DROP TABLE "+table(c)); e != nil {
		return e
	}
	_, e := t.tx.ExecContext(t.ctx, `DELETE FROM __collections WHERE name=?`, c.Name)
	return e
}
func (t *sqliteTx) createIndex(c Collection, i Index) error {
	// Validate portable key sizes before native DDL. Keep the initial build
	// bound aligned with Pebble's synchronous, atomic index builder.
	rows, err := t.find(c, Query{}, MaxIndexBuildRecords+1)
	if err != nil {
		return err
	}
	if len(rows) > MaxIndexBuildRecords {
		return fail("resource_limit", "synchronous index build exceeds 10000 records")
	}
	for _, row := range rows {
		if _, _, err := indexTuple(c, i, row); err != nil {
			return err
		}
	}
	fields := []string{}
	for _, o := range i.Fields {
		fields = append(fields, quote(o.Field)+" "+strings.ToUpper(o.Direction))
	}
	unique := ""
	if i.Unique {
		unique = "UNIQUE "
	} else {
		for _, p := range c.PrimaryKey {
			fields = append(fields, quote(p)+" ASC")
		}
	}
	_, e := t.tx.ExecContext(t.ctx, "CREATE "+unique+"INDEX "+sqlIndex(c, i.Name)+" ON "+table(c)+" ("+strings.Join(fields, ",")+")")
	if e != nil {
		return e
	}
	c.Indexes = append(c.Indexes, i)
	c.Version++
	return t.save(c)
}
func (t *sqliteTx) dropIndex(c Collection, name string) error {
	found := false
	idx := []Index{}
	for _, i := range c.Indexes {
		if i.Name == name {
			found = true
		} else {
			idx = append(idx, i)
		}
	}
	if !found {
		return fail("not_found", "index does not exist")
	}
	if _, e := t.tx.ExecContext(t.ctx, "DROP INDEX "+sqlIndex(c, name)); e != nil {
		return e
	}
	c.Indexes = idx
	c.Version++
	return t.save(c)
}
func (t *sqliteTx) get(c Collection, pk string) (Record, error) {
	var b []byte
	e := t.tx.QueryRowContext(t.ctx, "SELECT __doc FROM "+table(c)+" WHERE __pk=?", pk).Scan(&b)
	if errors.Is(e, sql.ErrNoRows) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	var r Record
	e = decode(b, &r)
	return r, e
}
func sqlValue(f Field, v any) any {
	if v == nil {
		return nil
	}
	switch f.Type {
	case "integer":
		n, _ := strconv.ParseInt(v.(string), 10, 64)
		return n
	case "number":
		return number(v)
	case "json":
		return string(marshal(v))
	}
	return v
}
func (t *sqliteTx) put(c Collection, pk string, r Record) error {
	for _, idx := range indexes(c) {
		if _, _, err := indexTuple(c, idx, r); err != nil {
			return err
		}
	}
	fields := []string{"__pk", "__doc"}
	qs := []string{"?", "?"}
	updates := []string{"__doc=excluded.__doc"}
	args := []any{pk, string(marshal(r))}
	for _, f := range allFields(c) {
		s := quote(f.Name)
		fields = append(fields, s)
		qs = append(qs, "?")
		updates = append(updates, s+"=excluded."+s)
		args = append(args, sqlValue(f, r[f.Name]))
	}
	_, e := t.tx.ExecContext(t.ctx, "INSERT INTO "+table(c)+" ("+strings.Join(fields, ",")+") VALUES ("+strings.Join(qs, ",")+") ON CONFLICT(__pk) DO UPDATE SET "+strings.Join(updates, ","), args...)
	return e
}
func (t *sqliteTx) remove(c Collection, pk string) error {
	_, e := t.tx.ExecContext(t.ctx, "DELETE FROM "+table(c)+" WHERE __pk=?", pk)
	return e
}
func compileFilter(c Collection, f *Filter) (string, []any) {
	if f == nil {
		return "1", nil
	}
	if f.And != nil || f.Or != nil {
		children := f.And
		join := " AND "
		if children == nil {
			children = f.Or
			join = " OR "
		}
		parts := []string{}
		args := []any{}
		for i := range children {
			s, a := compileFilter(c, &children[i])
			parts = append(parts, "("+s+")")
			args = append(args, a...)
		}
		return strings.Join(parts, join), args
	}
	col := quote(f.Field)
	fld, _ := c.field(f.Field)
	switch f.Op {
	case "is_null":
		return col + " IS NULL", nil
	case "is_not_null":
		return col + " IS NOT NULL", nil
	case "in":
		args := []any{}
		qs := []string{}
		for _, v := range f.Value.([]any) {
			qs = append(qs, "?")
			args = append(args, sqlValue(fld, v))
		}
		return col + " IN (" + strings.Join(qs, ",") + ")", args
	case "starts_with":
		s := f.Value.(string)
		hi := prefixEnd([]byte(s))
		if len(hi) > 0 {
			return col + ">=? AND " + col + "<?", []any{s, string(hi)}
		}
		return col + ">=?", []any{s}
	}
	op := map[string]string{"eq": "=", "ne": "!=", "gt": ">", "gte": ">=", "lt": "<", "lte": "<="}[f.Op]
	return col + op + "?", []any{sqlValue(fld, f.Value)}
}
func orderSQL(order []Order) string {
	if len(order) == 0 {
		return ""
	}
	p := []string{}
	for _, o := range order {
		dir := "ASC"
		if o.Direction == "desc" {
			dir = "DESC"
		}
		p = append(p, quote(o.Field)+" "+dir)
	}
	return " ORDER BY " + strings.Join(p, ",")
}
func (t *sqliteTx) checkIndex(c Collection, q Query) error {
	if !q.RequireIndex {
		return nil
	}
	p, e := t.explain(c, q)
	if e != nil {
		return e
	}
	if !p.(map[string]any)["indexedLookup"].(bool) {
		return fail("query_too_expensive", "query requires a selective index")
	}
	return nil
}
func (t *sqliteTx) find(c Collection, q Query, limit int) ([]Record, error) {
	if e := t.checkIndex(c, q); e != nil {
		return nil, e
	}
	where, args := compileFilter(c, q.Where)
	args = append(args, limit)
	rows, e := t.tx.QueryContext(t.ctx, "SELECT __doc FROM "+table(c)+" WHERE "+where+orderSQL(q.OrderBy)+" LIMIT ?", args...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Record{}
	size := 0
	for rows.Next() {
		var b []byte
		if e := rows.Scan(&b); e != nil {
			return nil, e
		}
		size += len(b)
		if size > MaxQueryWorkBytes {
			return nil, fail("resource_limit", "query working set exceeds 16 MiB")
		}
		var r Record
		if e := decode(b, &r); e != nil {
			return nil, e
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func (t *sqliteTx) explain(c Collection, q Query) (any, error) {
	where, args := compileFilter(c, q.Where)
	rows, e := t.tx.QueryContext(t.ctx, "EXPLAIN QUERY PLAN SELECT __doc FROM "+table(c)+" WHERE "+where+orderSQL(q.OrderBy), args...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	details := []string{}
	indexed := false
	for rows.Next() {
		var a, b, d int
		var s string
		if e := rows.Scan(&a, &b, &d, &s); e != nil {
			return nil, e
		}
		details = append(details, s)
		if strings.Contains(s, "SEARCH ") {
			indexed = true
		}
	}
	return map[string]any{"adapter": "sqlite", "indexedLookup": indexed, "steps": details}, rows.Err()
}
func fromSQL(f Field, v any) any {
	if v == nil {
		return nil
	}
	switch f.Type {
	case "integer":
		return fmt.Sprint(v)
	case "boolean":
		return v.(int64) != 0
	case "number":
		switch n := v.(type) {
		case int64:
			return float64(n)
		case float64:
			return n
		}
	case "text", "datetime":
		if b, ok := v.([]byte); ok {
			return string(b)
		}
	}
	return v
}
func (t *sqliteTx) aggregate(c Collection, r Request, out Collection) ([]Record, error) {
	if e := t.checkIndex(c, Query{Where: r.Where, RequireIndex: r.RequireIndex}); e != nil {
		return nil, e
	}
	selects := []string{}
	for _, g := range r.GroupBy {
		selects = append(selects, quote(g))
	}
	for _, m := range r.Metrics {
		field := "*"
		if m.Field != "" {
			field = quote(m.Field)
		}
		selects = append(selects, strings.ToUpper(m.Op)+"("+field+") AS "+quote(m.Name))
	}
	where, args := compileFilter(c, r.Where)
	s := "SELECT " + strings.Join(selects, ",") + " FROM " + table(c) + " WHERE " + where
	if len(r.GroupBy) > 0 {
		groups := []string{}
		for _, g := range r.GroupBy {
			groups = append(groups, quote(g))
		}
		s += " GROUP BY " + strings.Join(groups, ",")
	}
	s += orderSQL(r.OrderBy) + " LIMIT ?"
	args = append(args, r.Limit+1)
	rows, e := t.tx.QueryContext(t.ctx, s, args...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	result := []Record{}
	workBytes := 0
	for rows.Next() {
		vals := make([]any, len(out.Fields))
		ptrs := make([]any, len(vals))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if e := rows.Scan(ptrs...); e != nil {
			return nil, e
		}
		rec := Record{}
		for i, f := range out.Fields {
			v := fromSQL(f, vals[i])
			if n, ok := v.(float64); ok && (math.IsNaN(n) || math.IsInf(n, 0)) {
				return nil, fail("resource_limit", "aggregate numeric overflow")
			}
			rec[f.Name] = v
		}
		workBytes += retainedBytes(rec)
		if workBytes > MaxQueryWorkBytes {
			return nil, fail("resource_limit", "retained aggregate results exceed 16 MiB")
		}
		result = append(result, rec)
	}
	return result, rows.Err()
}
