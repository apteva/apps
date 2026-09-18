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
	"sync"

	sqlite "modernc.org/sqlite"
)

type sqliteBackend struct {
	db     *sql.DB
	readDB *sql.DB
	stmtMu sync.Mutex
	stmts  map[string]*sql.Stmt
}
type sqliteTx struct {
	ctx     context.Context
	tx      *sql.Tx
	stmts   map[string]*sql.Stmt
	write   bool
	prepare func(context.Context, string) (*sql.Stmt, error)
}

func openSQL(path string) (*sql.DB, error) {
	return openSQLPool(path, 5, false, "durable")
}
func openSQLPool(path string, maxConns int, readOnly bool, durability string) (*sql.DB, error) {
	u := url.URL{Scheme: "file", Path: path}
	q := u.Query()
	if readOnly {
		q.Set("mode", "ro")
	}
	synchronous := "FULL"
	if durability == "balanced" {
		synchronous = "NORMAL"
	}
	pragmas := []string{"busy_timeout(5000)", "foreign_keys(ON)"}
	if readOnly {
		pragmas = append(pragmas, "query_only(ON)")
	} else {
		pragmas = append(pragmas, "journal_mode(WAL)", "synchronous("+synchronous+")", "wal_autocheckpoint(10000)")
	}
	for _, p := range pragmas {
		q.Add("_pragma", p)
	}
	u.RawQuery = q.Encode()
	db, e := sql.Open("sqlite", u.String())
	if e != nil {
		return nil, e
	}
	// Writes are serialized by database.mu. A bounded pool allows independent
	// read transactions to run concurrently while retaining one writer.
	db.SetMaxOpenConns(maxConns)
	db.SetMaxIdleConns(maxConns)
	if e = db.Ping(); e != nil {
		db.Close()
		return nil, e
	}
	return db, nil
}
func openSQLite(path string) (backend, error) {
	return openSQLiteWithDurability(path, "durable")
}
func openSQLiteWithDurability(path, durability string) (backend, error) {
	if durability != "durable" && durability != "balanced" {
		return nil, Invalid("durability must be durable or balanced")
	}
	// One connection executes the serialized writer transaction; the second is
	// reserved for database-level statement preparation. Manager-level locking
	// still guarantees only one active write transaction.
	db, e := openSQLPool(path, 2, false, durability)
	if e != nil {
		return nil, e
	}
	readDB, e := openSQLPool(path, 4, true, durability)
	if e != nil {
		db.Close()
		return nil, e
	}
	_, e = db.Exec(`CREATE TABLE IF NOT EXISTS __collections (name TEXT PRIMARY KEY, schema_json TEXT NOT NULL, storage_version INTEGER NOT NULL DEFAULT 1)`)
	if e != nil {
		db.Close()
		readDB.Close()
		return nil, e
	}
	// v1 databases predate the physical-layout marker. Existing tables remain
	// readable as v1; new collections use v2 and can be migrated explicitly.
	_, _ = db.Exec(`ALTER TABLE __collections ADD COLUMN storage_version INTEGER NOT NULL DEFAULT 1`)
	return &sqliteBackend{db: db, readDB: readDB, stmts: map[string]*sql.Stmt{}}, nil
}
func (b *sqliteBackend) close() error {
	b.stmtMu.Lock()
	for _, s := range b.stmts {
		_ = s.Close()
	}
	b.stmtMu.Unlock()
	return errors.Join(b.readDB.Close(), b.db.Close())
}
func (b *sqliteBackend) transaction(ctx context.Context, write bool, fn func(transaction) error) error {
	db := b.readDB
	if write {
		db = b.db
	}
	tx, e := db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	prepare := func(ctx context.Context, query string) (*sql.Stmt, error) {
		if !write {
			return nil, errors.New("prepared writes unavailable in read transaction")
		}
		b.stmtMu.Lock()
		defer b.stmtMu.Unlock()
		if s := b.stmts[query]; s != nil {
			return s, nil
		}
		s, err := b.db.PrepareContext(ctx, query)
		if err == nil {
			b.stmts[query] = s
		}
		return s, err
	}
	if e = fn(&sqliteTx{ctx: ctx, tx: tx, stmts: map[string]*sql.Stmt{}, write: write, prepare: prepare}); e == nil {
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
	rows, e := t.tx.QueryContext(t.ctx, `SELECT schema_json,storage_version FROM __collections ORDER BY name`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Collection{}
	for rows.Next() {
		var b []byte
		var storage int
		if e := rows.Scan(&b, &storage); e != nil {
			return nil, e
		}
		var c Collection
		if e := decode(b, &c); e != nil {
			return nil, e
		}
		c.StorageVersion = storage
		out = append(out, c)
	}
	return out, rows.Err()
}
func (t *sqliteTx) collection(name string) (Collection, error) {
	var c Collection
	var b []byte
	var storage int
	e := t.tx.QueryRowContext(t.ctx, `SELECT schema_json,storage_version FROM __collections WHERE name=?`, name).Scan(&b, &storage)
	if errors.Is(e, sql.ErrNoRows) {
		return c, fail("not_found", "collection %s does not exist", name)
	}
	if e == nil {
		e = decode(b, &c)
		c.StorageVersion = storage
		if c.StorageVersion == 1 && t.write {
			if e = t.migrateV1(c); e == nil {
				c.StorageVersion = 2
			}
		}
	}
	return c, e
}
func v2TableDefs(c Collection) []string {
	defs := []string{}
	for _, f := range allFields(c) {
		s := quote(f.Name) + " " + sqlType(f)
		if !f.Nullable {
			s += " NOT NULL"
		}
		defs = append(defs, s)
	}
	pk := make([]string, len(c.PrimaryKey))
	for i, p := range c.PrimaryKey {
		pk[i] = quote(p)
	}
	return append(defs, "PRIMARY KEY ("+strings.Join(pk, ",")+")")
}
func (t *sqliteTx) migrateV1(c Collection) error {
	tmp := quote("c_" + c.Name + "_v2_migration")
	if _, e := t.tx.ExecContext(t.ctx, "DROP TABLE IF EXISTS "+tmp); e != nil {
		return e
	}
	if _, e := t.tx.ExecContext(t.ctx, "CREATE TABLE "+tmp+" ("+strings.Join(v2TableDefs(c), ",")+") WITHOUT ROWID"); e != nil {
		return e
	}
	cols := sqlColumns(allFields(c))
	if _, e := t.tx.ExecContext(t.ctx, "INSERT INTO "+tmp+" ("+cols+") SELECT "+cols+" FROM "+table(c)); e != nil {
		return e
	}
	// Index names are database-global, so remove the v1 indexes before the
	// atomic table swap and rebuild them on the shadow table.
	for _, idx := range indexes(c) {
		if _, e := t.tx.ExecContext(t.ctx, "DROP INDEX IF EXISTS "+sqlIndex(c, idx.Name)); e != nil {
			return e
		}
	}
	if _, e := t.tx.ExecContext(t.ctx, "DROP TABLE "+table(c)); e != nil {
		return e
	}
	if _, e := t.tx.ExecContext(t.ctx, "ALTER TABLE "+tmp+" RENAME TO "+quote("c_"+c.Name)); e != nil {
		return e
	}
	for _, i := range c.Indexes {
		parts := []string{}
		for _, o := range i.Fields {
			parts = append(parts, quote(o.Field)+" "+strings.ToUpper(o.Direction))
		}
		if !i.Unique {
			for _, p := range c.PrimaryKey {
				parts = append(parts, quote(p)+" ASC")
			}
		}
		unique := ""
		if i.Unique {
			unique = "UNIQUE "
		}
		if _, e := t.tx.ExecContext(t.ctx, "CREATE "+unique+"INDEX "+sqlIndex(c, i.Name)+" ON "+table(c)+" ("+strings.Join(parts, ",")+")"); e != nil {
			return e
		}
	}
	c.StorageVersion = 2
	return t.save(c)
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
func v2(c Collection) bool { return c.StorageVersion >= 2 }
func allFields(c Collection) []Field {
	return append(append([]Field{}, c.Fields...), Field{Name: "_created_at", Type: "datetime"}, Field{Name: "_updated_at", Type: "datetime"}, Field{Name: "_version", Type: "integer"})
}
func (t *sqliteTx) save(c Collection) error {
	storage := c.StorageVersion
	if storage == 0 {
		storage = 2
	}
	_, e := t.tx.ExecContext(t.ctx, `INSERT INTO __collections(name,schema_json,storage_version) VALUES(?,?,?) ON CONFLICT(name) DO UPDATE SET schema_json=excluded.schema_json,storage_version=excluded.storage_version`, c.Name, string(marshal(c)), storage)
	return e
}
func (t *sqliteTx) createCollection(c Collection) error {
	c.StorageVersion = 2
	defs := v2TableDefs(c)
	_, e := t.tx.ExecContext(t.ctx, "CREATE TABLE "+table(c)+" ("+strings.Join(defs, ",")+") WITHOUT ROWID")
	if e != nil {
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
	if v2(c) {
		vals, e := t.pkArgs(c, pk)
		if e != nil {
			return nil, e
		}
		fields := allFields(c)
		args := make([]any, len(vals))
		copy(args, vals)
		where := make([]string, len(c.PrimaryKey))
		for i, p := range c.PrimaryKey {
			where[i] = quote(p) + "=?"
		}
		row := t.tx.QueryRowContext(t.ctx, "SELECT "+sqlColumns(fields)+" FROM "+table(c)+" WHERE "+strings.Join(where, " AND "), args...)
		r, found, e := scanRecord(row, fields)
		if e != nil || !found {
			return nil, e
		}
		return r, nil
	}
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
func (t *sqliteTx) pkArgs(c Collection, pk string) ([]any, error) {
	var raw []any
	if e := decode([]byte(pk), &raw); e != nil || len(raw) != len(c.PrimaryKey) {
		return nil, fail("storage_error", "invalid primary key encoding")
	}
	args := make([]any, len(raw))
	for i, name := range c.PrimaryKey {
		f, _ := c.field(name)
		v, e := normalize(f, raw[i])
		if e != nil {
			return nil, e
		}
		args[i] = sqlValue(f, v)
	}
	return args, nil
}
func sqlColumns(fields []Field) string {
	cols := make([]string, len(fields))
	for i, f := range fields {
		cols[i] = quote(f.Name)
	}
	return strings.Join(cols, ",")
}

type rowScanner interface{ Scan(...any) error }

func scanRecord(row rowScanner, fields []Field) (Record, bool, error) {
	vals := make([]any, len(fields))
	ptrs := make([]any, len(vals))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if e := row.Scan(ptrs...); e != nil {
		if errors.Is(e, sql.ErrNoRows) {
			return nil, false, nil
		}
		return nil, false, e
	}
	r := Record{}
	for i, f := range fields {
		r[f.Name] = fromSQL(f, vals[i])
	}
	return r, true, nil
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
func (t *sqliteTx) writeArgs(c Collection, pk string, r Record) ([]string, []string, []string, []any, error) {
	for _, idx := range indexes(c) {
		if sqliteIndexTupleSize(c, idx, r) > 4096 {
			return nil, nil, nil, nil, Invalid("encoded index key exceeds 4096 bytes")
		}
	}
	fields := []string{}
	qs := []string{}
	updates := []string{}
	args := []any{}
	if !v2(c) {
		fields = append(fields, "__pk", "__doc")
		qs = append(qs, "?", "?")
		updates = append(updates, "__doc=excluded.__doc")
		args = append(args, pk, string(marshal(r)))
	}
	for _, f := range allFields(c) {
		s := quote(f.Name)
		fields = append(fields, s)
		qs = append(qs, "?")
		if !c.isPK(f.Name) || !v2(c) {
			updates = append(updates, s+"=excluded."+s)
		}
		args = append(args, sqlValue(f, r[f.Name]))
	}
	return fields, qs, updates, args, nil
}

// SQLite maintains the native index itself, so it only needs the portable
// contract's encoded-key length check. Computing that length directly avoids
// allocating Pebble's order-preserving key bytes for every SQLite insert.
func sqliteIndexTupleSize(c Collection, idx Index, r Record) int {
	n := 0
	for _, o := range idx.Fields {
		f, _ := c.field(o.Field)
		v := r[o.Field]
		if v == nil {
			n++
			continue
		}
		switch f.Type {
		case "integer", "number":
			n += 9
		case "boolean":
			n += 2
		default:
			n += 3
			for _, b := range []byte(v.(string)) {
				n++
				if b == 0 {
					n++
				}
			}
		}
	}
	return n
}
func (t *sqliteTx) execPrepared(query string, args ...any) error {
	stmt := t.stmts[query]
	if stmt == nil {
		var e error
		base, e := t.prepare(t.ctx, query)
		if e == nil {
			stmt = t.tx.StmtContext(t.ctx, base)
		}
		if e != nil {
			return e
		}
		t.stmts[query] = stmt
	}
	_, e := stmt.ExecContext(t.ctx, args...)
	return e
}
func (t *sqliteTx) insert(c Collection, pk string, r Record) error {
	fields, qs, _, args, e := t.writeArgs(c, pk, r)
	if e != nil {
		return e
	}
	return t.execPrepared("INSERT INTO "+table(c)+" ("+strings.Join(fields, ",")+") VALUES ("+strings.Join(qs, ",")+")", args...)
}

func (t *sqliteTx) put(c Collection, pk string, r Record) error {
	fields, qs, updates, args, e := t.writeArgs(c, pk, r)
	if e != nil {
		return e
	}
	conflict := "__pk"
	if v2(c) {
		parts := make([]string, len(c.PrimaryKey))
		for i, p := range c.PrimaryKey {
			parts[i] = quote(p)
		}
		conflict = strings.Join(parts, ",")
	}
	e = t.execPrepared("INSERT INTO "+table(c)+" ("+strings.Join(fields, ",")+") VALUES ("+strings.Join(qs, ",")+") ON CONFLICT("+conflict+") DO UPDATE SET "+strings.Join(updates, ","), args...)
	return e
}
func (t *sqliteTx) remove(c Collection, pk string) error {
	if v2(c) {
		args, e := t.pkArgs(c, pk)
		if e != nil {
			return e
		}
		where := make([]string, len(c.PrimaryKey))
		for i, p := range c.PrimaryKey {
			where[i] = quote(p) + "=?"
		}
		_, e = t.tx.ExecContext(t.ctx, "DELETE FROM "+table(c)+" WHERE "+strings.Join(where, " AND "), args...)
		return e
	}
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
	selectSQL := "__doc"
	var fields []Field
	if v2(c) {
		wanted := map[string]bool{}
		for _, f := range q.Select {
			wanted[f] = true
		}
		if len(q.Select) == 0 {
			for _, f := range allFields(c) {
				wanted[f.Name] = true
			}
		}
		var visit func(*Filter)
		visit = func(f *Filter) {
			if f == nil {
				return
			}
			if f.Field != "" {
				wanted[f.Field] = true
			}
			for i := range f.And {
				visit(&f.And[i])
			}
			for i := range f.Or {
				visit(&f.Or[i])
			}
		}
		visit(q.Where)
		for _, o := range q.OrderBy {
			wanted[o.Field] = true
		}
		for _, f := range allFields(c) {
			if wanted[f.Name] {
				fields = append(fields, f)
			}
		}
		selectSQL = sqlColumns(fields)
	}
	rows, e := t.tx.QueryContext(t.ctx, "SELECT "+selectSQL+" FROM "+table(c)+" WHERE "+where+orderSQL(q.OrderBy)+" LIMIT ?", args...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Record{}
	size := 0
	for rows.Next() {
		var r Record
		if v2(c) {
			var ok bool
			var e error
			r, ok, e = scanRecord(rows, fields)
			if e != nil {
				return nil, e
			}
			if !ok {
				continue
			}
			size += len(marshal(r))
		} else {
			var b []byte
			if e := rows.Scan(&b); e != nil {
				return nil, e
			}
			size += len(b)
			if e := decode(b, &r); e != nil {
				return nil, e
			}
		}
		if size > MaxQueryWorkBytes {
			return nil, fail("resource_limit", "query working set exceeds 16 MiB")
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func (t *sqliteTx) explain(c Collection, q Query) (any, error) {
	where, args := compileFilter(c, q.Where)
	selectSQL := "__doc"
	if v2(c) {
		selectSQL = "1"
	}
	rows, e := t.tx.QueryContext(t.ctx, "EXPLAIN QUERY PLAN SELECT "+selectSQL+" FROM "+table(c)+" WHERE "+where+orderSQL(q.OrderBy), args...)
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
	case "json":
		var b []byte
		switch x := v.(type) {
		case []byte:
			b = x
		case string:
			b = []byte(x)
		}
		var out any
		if len(b) > 0 && decode(b, &out) == nil {
			return out
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
