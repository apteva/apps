package engine

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"
)

type backend interface {
	transaction(context.Context, bool, func(transaction) error) error
	close() error
}
type transaction interface {
	collections() ([]Collection, error)
	collection(string) (Collection, error)
	createCollection(Collection) error
	dropCollection(Collection) error
	createIndex(Collection, Index) error
	dropIndex(Collection, string) error
	get(Collection, string) (Record, error)
	insert(Collection, string, Record) error
	put(Collection, string, Record) error
	remove(Collection, string) error
	find(Collection, Query, int) ([]Record, error)
	aggregate(Collection, Request, Collection) ([]Record, error)
	explain(Collection, Query) (any, error)
}

// batchInserter is an optional SQLite fast path. Pebble already accumulates
// record writes in one batch, while SQLite can reduce cgo/SQL statement
// overhead by binding several rows to one INSERT statement. The transaction
// contract remains unchanged for adapters that do not implement it.
type batchInserter interface {
	insertBatch(Collection, []Record) error
}
type DatabaseInfo struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Adapter string `json:"adapter"`
}
type database struct {
	mu      sync.RWMutex
	writes  writeQueue
	backend backend
	info    DatabaseInfo
	key     []byte
	closed  bool
}
type Manager struct {
	mu         sync.Mutex
	registry   *sql.DB
	root       string
	databases  map[string]*database
	secret     string
	durability string
	closed     bool
}

func Open(root string) (*Manager, error) {
	return OpenWithOptions(root, "durable")
}

// OpenWithOptions opens the manager with an explicit SQLite durability profile.
// durable is the default and uses synchronous=FULL; balanced uses NORMAL and
// is intended only when the operator accepts the weaker crash-commit window.
func OpenWithOptions(root, durability string) (*Manager, error) {
	if durability != "durable" && durability != "balanced" {
		return nil, Invalid("durability must be durable or balanced")
	}
	if e := os.MkdirAll(root, 0700); e != nil {
		return nil, e
	}
	root, e := filepath.Abs(root)
	if e != nil {
		return nil, e
	}
	db, e := openSQL(filepath.Join(root, "control.db"))
	if e != nil {
		return nil, e
	}
	_, e = db.Exec(`CREATE TABLE IF NOT EXISTS databases (id TEXT PRIMARY KEY, scope TEXT NOT NULL, name TEXT NOT NULL, adapter TEXT NOT NULL, UNIQUE(scope,name)); CREATE TABLE IF NOT EXISTS settings (name TEXT PRIMARY KEY,value TEXT NOT NULL)`)
	if e != nil {
		db.Close()
		return nil, e
	}
	_, e = db.Exec(`INSERT OR IGNORE INTO settings VALUES ('secret',?)`, uuid.NewString())
	if e != nil {
		db.Close()
		return nil, e
	}
	m := &Manager{registry: db, root: root, databases: map[string]*database{}, durability: durability}
	if e = db.QueryRow(`SELECT value FROM settings WHERE name='secret'`).Scan(&m.secret); e != nil {
		db.Close()
		return nil, e
	}
	return m, nil
}
func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil
	}
	m.closed = true
	var errs []error
	for _, d := range m.databases {
		d.mu.Lock()
		d.closed = true
		errs = append(errs, d.backend.close())
		d.mu.Unlock()
	}
	errs = append(errs, m.registry.Close())
	return errors.Join(errs...)
}
func (m *Manager) list(ctx context.Context, scope string) ([]DatabaseInfo, error) {
	rows, e := m.registry.QueryContext(ctx, `SELECT id,name,adapter FROM databases WHERE scope=? ORDER BY name`, scope)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []DatabaseInfo{}
	for rows.Next() {
		var d DatabaseInfo
		if e := rows.Scan(&d.ID, &d.Name, &d.Adapter); e != nil {
			return nil, e
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
func (m *Manager) resolve(ctx context.Context, scope, name, adapter string, create bool) (*database, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, fail("storage_error", "database app is closed")
	}
	if e := nameOK(name); e != nil {
		return nil, e
	}
	var info DatabaseInfo
	e := m.registry.QueryRowContext(ctx, `SELECT id,name,adapter FROM databases WHERE scope=? AND name=?`, scope, name).Scan(&info.ID, &info.Name, &info.Adapter)
	fresh := errors.Is(e, sql.ErrNoRows)
	if e != nil && !fresh {
		return nil, e
	}
	if fresh {
		if !create {
			return nil, fail("not_found", "database %s does not exist", name)
		}
		if adapter == "" {
			adapter = "sqlite"
		}
		if adapter != "sqlite" && adapter != "pebble" {
			return nil, Invalid("adapter must be sqlite or pebble")
		}
		var n int
		if e := m.registry.QueryRowContext(ctx, `SELECT count(*) FROM databases WHERE scope=?`, scope).Scan(&n); e != nil {
			return nil, e
		}
		if n >= 128 {
			return nil, fail("resource_limit", "at most 128 databases per scope")
		}
		info = DatabaseInfo{uuid.NewString(), name, adapter}
	} else if create && adapter != "" && adapter != info.Adapter {
		return nil, fail("schema_conflict", "database already uses %s", info.Adapter)
	}
	if d := m.databases[info.ID]; d != nil {
		return d, nil
	}
	if len(m.databases) >= 128 {
		return nil, fail("resource_limit", "at most 128 open databases per app")
	}
	dir := filepath.Join(m.root, "databases", info.ID)
	if e := os.MkdirAll(dir, 0700); e != nil {
		return nil, e
	}
	var b backend
	if info.Adapter == "sqlite" {
		b, e = openSQLiteWithDurability(filepath.Join(dir, "data.sqlite"), m.durability)
	} else {
		b, e = openPebble(filepath.Join(dir, "pebble"))
	}
	if e != nil {
		return nil, e
	}
	if fresh {
		_, e = m.registry.ExecContext(ctx, `INSERT INTO databases(id,scope,name,adapter) VALUES(?,?,?,?)`, info.ID, scope, name, info.Adapter)
		if e != nil {
			b.close()
			os.RemoveAll(dir)
			return nil, e
		}
	}
	key := sha256.Sum256([]byte(m.secret + "/" + info.ID))
	d := &database{backend: b, info: info, key: key[:]}
	m.databases[info.ID] = d
	return d, nil
}
func (m *Manager) Execute(ctx context.Context, scope, op string, r Request) (any, error) {
	timeout, err := OperationTimeout(op, r.TimeoutMS)
	if err != nil {
		return nil, err
	}
	if op == "find" || op == "count" || op == "aggregate" {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	if scope == "" {
		return nil, fail("permission_denied", "authenticated project scope required")
	}
	if r.Database == "" {
		r.Database = "default"
	}
	if e := nameOK(r.Database); e != nil {
		return nil, e
	}
	if op == "databases_list" {
		return m.list(ctx, scope)
	}
	d, e := m.resolve(ctx, scope, r.Database, r.Adapter, op == "database_create" || r.Database == "default")
	if e != nil {
		return nil, e
	}
	if op == "database_create" || op == "database_describe" {
		return d.info, nil
	}
	if op == "database_drop" {
		if !r.Confirm {
			return nil, Invalid("confirm=true is required")
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		d.mu.Lock()
		defer d.mu.Unlock()
		if d.closed {
			return nil, fail("not_found", "database is closed")
		}
		if _, e = m.registry.ExecContext(ctx, `DELETE FROM databases WHERE id=?`, d.info.ID); e != nil {
			return nil, e
		}
		d.closed = true
		delete(m.databases, d.info.ID)
		if e = d.backend.close(); e != nil {
			return nil, e
		}
		e = os.RemoveAll(filepath.Join(m.root, "databases", d.info.ID))
		return map[string]any{"deleted": e == nil}, e
	}
	write := isWrite(op)
	if isRecordWrite(op) {
		if grouped, ok := d.backend.(groupedBackend); ok {
			return d.enqueueWrite(ctx, grouped, func(tx transaction) (any, error) {
				return execute(tx, op, r, d.key)
			})
		}
	}
	if write {
		d.mu.Lock()
		defer d.mu.Unlock()
	} else {
		d.mu.RLock()
		defer d.mu.RUnlock()
	}
	if d.closed {
		return nil, fail("not_found", "database is closed")
	}
	var out any
	e = d.backend.transaction(ctx, write, func(tx transaction) error { var err error; out, err = execute(tx, op, r, d.key); return err })
	return out, e
}
func isRecordWrite(op string) bool {
	switch op {
	case "insert", "update", "delete", "upsert", "batch":
		return true
	}
	return false
}
func isWrite(op string) bool {
	switch op {
	case "collections_list", "collection_describe", "indexes_list", "get", "find", "count", "aggregate", "explain":
		return false
	}
	return true
}
func execute(tx transaction, op string, r Request, cursorKey []byte) (any, error) {
	if op == "collections_list" {
		return tx.collections()
	}
	if op == "batch" {
		if len(r.Operations) == 0 || len(r.Operations) > 100 {
			return nil, Invalid("batch requires 1–100 operations")
		}
		out := []any{}
		total := 0
		for _, o := range r.Operations {
			if o.Request.TimeoutMS != 0 {
				return nil, Invalid("batch record operations do not accept timeoutMs")
			}
			switch o.Op {
			case "insert", "update", "delete", "upsert":
			default:
				return nil, Invalid("batch only accepts record writes")
			}
			if o.Request.Database != "" && o.Request.Database != r.Database {
				return nil, Invalid("batch cannot span databases")
			}
			v, e := execute(tx, o.Op, o.Request, cursorKey)
			if e != nil {
				return nil, e
			}
			result := v.(map[string]any)
			total += result["affected"].(int)
			if total > MaxRecords {
				return nil, fail("resource_limit", "batch affects more than 1000 records")
			}
			out = append(out, v)
		}
		return map[string]any{"results": out, "affected": total}, nil
	}
	if op == "collection_create" {
		c, e := newCollection(r)
		if e != nil {
			return nil, e
		}
		if old, e := tx.collection(c.Name); e == nil {
			c.Version = old.Version
			c.Indexes = old.Indexes
			if equalJSON(c, old) {
				return old, nil
			}
			return nil, fail("schema_conflict", "collection already exists with a different schema")
		} else {
			var ee *Error
			if !errors.As(e, &ee) || ee.Code != "not_found" {
				return nil, e
			}
		}
		return c, tx.createCollection(c)
	}
	if e := nameOK(r.Collection); e != nil {
		return nil, e
	}
	c, e := tx.collection(r.Collection)
	if e != nil {
		return nil, e
	}
	switch op {
	case "collection_describe":
		return c, nil
	case "collection_drop":
		if !r.Confirm {
			return nil, Invalid("confirm=true is required")
		}
		return map[string]any{"deleted": true}, tx.dropCollection(c)
	case "indexes_list":
		return c.Indexes, nil
	case "index_create":
		if e := validateIndex(c, r.Index); e != nil {
			return nil, e
		}
		for _, i := range c.Indexes {
			if i.Name == r.Index.Name {
				if equalJSON(i, *r.Index) {
					return i, nil
				}
				return nil, fail("schema_conflict", "index name already exists")
			}
		}
		if len(c.Indexes) >= 16 {
			return nil, fail("resource_limit", "at most 16 secondary indexes")
		}
		return r.Index, tx.createIndex(c, *r.Index)
	case "index_drop":
		if !r.Confirm {
			return nil, Invalid("confirm=true is required")
		}
		if e := nameOK(r.Name); e != nil {
			return nil, e
		}
		if r.Name == "primary" {
			return nil, Invalid("primary index cannot be dropped")
		}
		return map[string]any{"deleted": true}, tx.dropIndex(c, r.Name)
	case "get":
		if len(r.Key) != len(c.PrimaryKey) {
			return nil, Invalid("key must contain exactly the primary key fields")
		}
		pk, e := primary(c, r.Key)
		if e != nil {
			return nil, e
		}
		v, e := tx.get(c, pk)
		return map[string]any{"found": v != nil, "record": v}, e
	case "find", "explain":
		if e := validateQuery(c, &r.Query); e != nil {
			return nil, e
		}
		if op == "explain" {
			return tx.explain(c, r.Query)
		}
		q := r.Query
		if q.Cursor != "" {
			after, e := afterCursor(c, &q, cursorKey)
			if e != nil {
				return nil, e
			}
			q.Where = and(q.Where, after)
		}
		rows, e := tx.find(c, q, q.Limit+1)
		if e != nil {
			return nil, e
		}
		return page(c, r.Query, rows, cursorKey)
	case "count":
		r.Metrics = []Metric{{Name: "count", Op: "count"}}
		r.GroupBy = nil
		r.OrderBy = nil
		r.Limit = 1
		fallthrough
	case "aggregate":
		outSchema, e := validateAggregate(c, &r)
		if e != nil {
			return nil, e
		}
		rows, e := tx.aggregate(c, r, outSchema)
		if e != nil {
			return nil, e
		}
		if op == "count" {
			return map[string]any{"count": rows[0]["count"]}, nil
		}
		if len(marshal(rows)) > MaxBytes {
			return nil, fail("resource_limit", "aggregate response exceeds 4 MiB")
		}
		more := len(rows) > r.Limit
		if more {
			rows = rows[:r.Limit]
		}
		return map[string]any{"rows": rows, "truncated": more}, nil
	case "insert", "upsert", "update", "delete":
		return mutate(tx, c, op, r)
	default:
		return nil, Invalid("unknown operation %s", op)
	}
}
func mutate(tx transaction, c Collection, op string, r Request) (any, error) {
	if r.IfVersion < 0 {
		return nil, Invalid("ifVersion must be positive")
	}
	keys := []Record{}
	now := time.Now().UTC().Truncate(time.Microsecond).Format("2006-01-02T15:04:05.000000Z")
	if op == "insert" || op == "upsert" {
		if len(r.Records) == 0 || len(r.Records) > MaxRecords {
			return nil, Invalid("records requires 1–1000 records")
		}
		if op == "insert" {
			if bulk, ok := tx.(batchInserter); ok {
				return bulkInsert(c, r.Records, now, bulk)
			}
		}
		for _, input := range r.Records {
			row := Record{}
			for k, v := range input {
				row[k] = v
			}
			var old Record
			if op == "upsert" && r.ConflictIndex != "" {
				var ix *Index
				for _, idx := range c.Indexes {
					if idx.Name == r.ConflictIndex {
						x := idx
						ix = &x
					}
				}
				if ix == nil || !ix.Unique {
					return nil, Invalid("conflictIndex must name a unique index")
				}
				fs := []Filter{}
				for _, o := range ix.Fields {
					f, _ := c.field(o.Field)
					f.Nullable = false
					v, e := normalize(f, row[o.Field])
					if e != nil {
						return nil, e
					}
					fs = append(fs, Filter{Field: o.Field, Op: "eq", Value: v})
				}
				found, e := tx.find(c, Query{Where: &Filter{And: fs}}, 2)
				if e != nil {
					return nil, e
				}
				if len(found) > 0 {
					old = found[0]
				}
			} else if op == "upsert" {
				if _, e := primary(c, row); e == nil {
					pk, _ := primary(c, row)
					old, e = tx.get(c, pk)
					if e != nil {
						return nil, e
					}
				}
			}
			if old != nil {
				merged := Record{}
				for _, f := range c.Fields {
					merged[f.Name] = old[f.Name]
				}
				for k, v := range row {
					if c.isPK(k) && !equalJSON(v, old[k]) {
						return nil, Invalid("primary key cannot change")
					}
					merged[k] = v
				}
				row = merged
			} else if _, supplied := row["id"]; len(c.PrimaryKey) == 1 && c.PrimaryKey[0] == "id" && !supplied {
				f, _ := c.field("id")
				if f.Type == "text" {
					row["id"] = uuid.NewString()
				}
			}
			row, e := normalizeRecord(c, row)
			if e != nil {
				return nil, e
			}
			pk, e := primary(c, row)
			if e != nil {
				return nil, e
			}
			if op == "upsert" && old == nil {
				existing, e := tx.get(c, pk)
				if e != nil {
					return nil, e
				}
				if existing != nil {
					return nil, fail("unique_conflict", "primary key already exists")
				}
			}
			stamp(row, old, now)
			if op == "insert" {
				e = tx.insert(c, pk, row)
			} else {
				e = tx.put(c, pk, row)
			}
			if e != nil {
				return nil, e
			}
			keys = append(keys, keyOf(c, row))
		}
	} else {
		if r.Cursor != "" || len(r.OrderBy) > 0 || r.Limit != 0 || len(r.Select) > 0 {
			return nil, Invalid("mutations do not accept pagination, ordering or projection")
		}
		if len(r.Key) > 0 {
			if r.Where != nil || r.All || len(r.Key) != len(c.PrimaryKey) {
				return nil, Invalid("use an exact key or a filter")
			}
			filters := []Filter{}
			for _, p := range c.PrimaryKey {
				filters = append(filters, Filter{Field: p, Op: "eq", Value: r.Key[p]})
			}
			r.Where = &Filter{And: filters}
		}
		if r.Where == nil && !r.All {
			return nil, Invalid("where, key or all=true is required")
		}
		if r.Where != nil && r.All {
			return nil, Invalid("all and where cannot be combined")
		}
		n := 0
		if e := validateFilter(c, r.Where, 0, &n); e != nil {
			return nil, e
		}
		if r.MaxAffected == 0 {
			r.MaxAffected = 1
		}
		if r.MaxAffected < 1 || r.MaxAffected > MaxRecords {
			return nil, Invalid("maxAffected must be 1–1000")
		}
		if r.IfVersion != 0 && len(r.Key) == 0 {
			return nil, Invalid("ifVersion requires an exact key")
		}
		if op == "update" && len(r.Set)+len(r.Increment) == 0 {
			return nil, Invalid("set or increment is required")
		}
		for k := range r.Set {
			if c.isPK(k) || len(k) == 0 || k[0] == '_' {
				return nil, Invalid("cannot modify primary key or metadata")
			}
			if _, e := c.field(k); e != nil {
				return nil, e
			}
			if _, ok := r.Increment[k]; ok {
				return nil, Invalid("set and increment overlap")
			}
		}
		for k, v := range r.Increment {
			f, e := c.field(k)
			if e != nil {
				return nil, e
			}
			if c.isPK(k) || k[0] == '_' || (f.Type != "integer" && f.Type != "number") {
				return nil, Invalid("increment requires a mutable numeric field")
			}
			f.Nullable = false
			nv, e := normalize(f, v)
			if e != nil {
				return nil, e
			}
			r.Increment[k] = nv
		}
		rows, e := tx.find(c, r.Query, r.MaxAffected+1)
		if e != nil {
			return nil, e
		}
		if len(rows) > r.MaxAffected {
			return nil, fail("resource_limit", "mutation exceeds maxAffected; no records changed")
		}
		if r.IfVersion > 0 && len(rows) == 0 {
			return nil, fail("version_conflict", "record is absent")
		}
		for _, old := range rows {
			pk, _ := primary(c, old)
			if r.IfVersion > 0 && old["_version"] != strconv.FormatInt(r.IfVersion, 10) {
				return nil, fail("version_conflict", "record version changed")
			}
			if op == "delete" {
				if e := tx.remove(c, pk); e != nil {
					return nil, e
				}
			} else {
				v := Record{}
				for _, f := range c.Fields {
					v[f.Name] = old[f.Name]
				}
				for k, x := range r.Set {
					v[k] = x
				}
				for k, x := range r.Increment {
					f, _ := c.field(k)
					if v[k] == nil {
						return nil, Invalid("cannot increment null")
					}
					if f.Type == "integer" {
						a, _ := strconv.ParseInt(v[k].(string), 10, 64)
						b, _ := strconv.ParseInt(x.(string), 10, 64)
						if (b > 0 && a > math.MaxInt64-b) || (b < 0 && a < math.MinInt64-b) {
							return nil, Invalid("integer overflow")
						}
						v[k] = strconv.FormatInt(a+b, 10)
					} else {
						v[k] = number(v[k]) + number(x)
					}
				}
				v, e := normalizeRecord(c, v)
				if e != nil {
					return nil, e
				}
				stamp(v, old, now)
				if e = tx.put(c, pk, v); e != nil {
					return nil, e
				}
			}
			keys = append(keys, keyOf(c, old))
		}
	}
	return map[string]any{"affected": len(keys), "keys": keys}, nil
}

func bulkInsert(c Collection, inputs []Record, now string, bulk batchInserter) (any, error) {
	rows := make([]Record, 0, len(inputs))
	keys := make([]Record, 0, len(inputs))
	for _, input := range inputs {
		row := Record{}
		for k, v := range input {
			row[k] = v
		}
		if _, supplied := row["id"]; len(c.PrimaryKey) == 1 && c.PrimaryKey[0] == "id" && !supplied {
			f, _ := c.field("id")
			if f.Type == "text" {
				row["id"] = uuid.NewString()
			}
		}
		var e error
		row, e = normalizeRecord(c, row)
		if e != nil {
			return nil, e
		}
		if _, e := primary(c, row); e != nil {
			return nil, e
		}
		stamp(row, nil, now)
		rows = append(rows, row)
		keys = append(keys, keyOf(c, row))
	}
	if e := bulk.insertBatch(c, rows); e != nil {
		return nil, e
	}
	return map[string]any{"keys": keys, "affected": len(keys)}, nil
}
func stamp(r, old Record, now string) {
	r["_created_at"] = now
	r["_updated_at"] = now
	r["_version"] = "1"
	if old != nil {
		r["_created_at"] = old["_created_at"]
		v, _ := strconv.ParseInt(old["_version"].(string), 10, 64)
		r["_version"] = fmt.Sprint(v + 1)
	}
}
