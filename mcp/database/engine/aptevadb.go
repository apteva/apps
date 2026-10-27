package engine

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"sync"

	native "github.com/apteva/db"
)

const nativeIntentKey = "ddl-intent"

func nativeSchemaKey(name string) []byte      { return []byte("schema/" + name) }
func nativeCollectionName(name string) string { return "records_" + name }

type aptevaBackend struct {
	db      *native.DB
	mu      sync.Mutex
	handles map[string]*native.Collection
	fatal   error // Unpublished schema changes require recovery before any access.
	options AptevaOptions
}
type nativeRowKey struct{ collection, primary string }
type nativeUniqueKey struct{ collection, index, tuple string }
type aptevaTx struct {
	ctx       context.Context
	backend   *aptevaBackend
	snapshot  *native.Snapshot
	batch     *native.Batch
	pending   map[nativeRowKey][]byte // nil denotes a staged deletion
	unique    map[nativeUniqueKey]string
	rowUnique map[nativeRowKey][]nativeUniqueKey
	ddl       *nativeDDL
}

func openAptevaDB(path string) (backend, error) {
	return openAptevaDBWithOptions(path, DefaultAptevaOptions())
}

func openAptevaDBWithOptions(path string, options AptevaOptions) (backend, error) {
	options, e := options.normalized()
	if e != nil {
		return nil, e
	}
	durable := nativeAptevaOptions(options)
	db, e := native.Open(path, durable)
	if e != nil {
		return nil, e
	}
	b := &aptevaBackend{db: db, handles: map[string]*native.Collection{}, options: options}
	if e = b.recoverDDL(); e != nil {
		return nil, errors.Join(e, db.Close())
	}
	return b, nil
}

// Start with synchronous durable options: changing the flush primitive must
// never accidentally select the native Options{} asynchronous default.
func nativeAptevaOptions(options AptevaOptions) native.Options {
	durable := native.DurableOptions()
	switch options.SyncMode {
	case "barrier":
		durable.SyncMode = native.SyncBarrier
	case "os":
		durable.SyncMode = native.SyncOS
	}
	durable.CacheBytes = options.CacheBytes
	durable.MemtableBytes = options.MemtableBytes
	if durable.MemtableBytes == 0 {
		durable.MemtableBytes = -1
	}
	durable.Parallelism = options.AggregateWorkers
	if options.Realtime != nil {
		durable.ChangeFeed = nativeFeedOptions(*options.Realtime)
	}
	return durable
}
func (b *aptevaBackend) close() error { return b.db.Close() }
func (b *aptevaBackend) health() error {
	b.mu.Lock()
	e := b.fatal
	b.mu.Unlock()
	if e != nil {
		return e
	}
	return b.db.Health()
}
func (b *aptevaBackend) poison(e error) error {
	fatal := fail("storage_error", "native schema recovery required: %v", e)
	b.mu.Lock()
	b.fatal = fatal
	b.mu.Unlock()
	return fatal
}
func (b *aptevaBackend) invalidateSchema() {
	b.mu.Lock()
	b.handles = map[string]*native.Collection{}
	b.mu.Unlock()
}
func (b *aptevaBackend) handle(c Collection) (*native.Collection, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if h := b.handles[c.Name]; h != nil {
		return h, nil
	}
	h, e := b.db.OpenCollection(nativeCollectionName(c.Name))
	if e == nil {
		b.handles[c.Name] = h
	}
	return h, e
}
func nativeError(e error) error {
	if e == nil {
		return nil
	}
	var app *Error
	if errors.As(e, &app) {
		return e
	}
	switch {
	case errors.Is(e, native.ErrKeyExists), errors.Is(e, native.ErrDuplicateKey):
		return fail("unique_conflict", "primary or unique index constraint violated")
	case errors.Is(e, native.ErrKeySize), errors.Is(e, native.ErrValueSize), errors.Is(e, native.ErrTransactionTooLarge), errors.Is(e, native.ErrLimit):
		return fail("resource_limit", "native database resource limit: %v", e)
	case errors.Is(e, native.ErrNoSuchCollection), errors.Is(e, native.ErrNotFound):
		return fail("not_found", "native database object does not exist")
	case errors.Is(e, native.ErrConflict):
		return fail("storage_error", "unexpected concurrent native database writer")
	}
	return e
}
func (b *aptevaBackend) transaction(ctx context.Context, write bool, fn func(transaction) error) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	if e := b.health(); e != nil {
		return nativeError(e)
	}
	snap, e := b.db.Snapshot()
	if e != nil {
		return nativeError(e)
	}
	t := &aptevaTx{ctx: ctx, backend: b, snapshot: snap}
	if write {
		t.batch = b.db.NewBatch()
		t.pending = map[nativeRowKey][]byte{}
		t.unique = map[nativeUniqueKey]string{}
		t.rowUnique = map[nativeRowKey][]nativeUniqueKey{}
	}
	e = fn(t)
	snap.Close() // DDL and commit do not retain the operation's read view.
	if e != nil {
		return nativeError(e)
	}
	if e = ctx.Err(); e != nil {
		return e
	}
	if !write {
		return nil
	}
	if t.ddl != nil {
		return b.commitDDL(ctx, *t.ddl)
	}
	return nativeError(t.batch.CommitContext(ctx))
}
func (t *aptevaTx) collections() ([]Collection, error) {
	var out []Collection
	var scanErr error
	e := t.snapshot.Scan([]byte("schema/"), prefixEnd([]byte("schema/")), func(_, v []byte) bool {
		if scanErr = t.ctx.Err(); scanErr != nil {
			return false
		}
		var c Collection
		scanErr = decode(v, &c)
		if scanErr == nil {
			out = append(out, c)
		}
		return scanErr == nil
	})
	if e != nil {
		return nil, e
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	if out == nil {
		out = []Collection{}
	}
	return out, scanErr
}
func (t *aptevaTx) collection(name string) (Collection, error) {
	var c Collection
	v, found, e := t.snapshot.Find(nativeSchemaKey(name), nil)
	if e != nil {
		return c, e
	}
	if !found {
		return c, fail("not_found", "collection %s does not exist", name)
	}
	e = decode(v, &c)
	return c, e
}
func (t *aptevaTx) readCollection(c Collection) (*native.Collection, error) {
	return t.snapshot.Collection(nativeCollectionName(c.Name))
}
func (t *aptevaTx) get(c Collection, pk string) (Record, error) {
	if e := t.ctx.Err(); e != nil {
		return nil, e
	}
	key, e := nativeKeyFromCanonical(c, pk)
	if e != nil {
		return nil, e
	}
	return t.getPhysical(c, key)
}
func (t *aptevaTx) getPhysical(c Collection, key []byte) (Record, error) {
	if data, ok := t.pending[nativeRowKey{c.Name, string(key)}]; ok {
		if data == nil {
			return nil, nil
		}
		return decodeNativeRecord(c, data)
	}
	h, e := t.readCollection(c)
	if e != nil {
		return nil, e
	}
	data, found, e := h.Find(key, nil)
	if e != nil || !found {
		return nil, e
	}
	return decodeNativeRecord(c, data)
}
func (b *aptevaBackend) pointGet(ctx context.Context, c Collection, key Record, _ string) (Record, error) {
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	if e := b.health(); e != nil {
		return nil, e
	}
	physical, _, e := indexTuple(c, primaryIndex(c), key)
	if e != nil {
		return nil, e
	}
	h, e := b.handle(c)
	if e != nil {
		return nil, nativeError(e)
	}
	v, found, e := h.Find(physical, nil)
	if e != nil || !found {
		return nil, nativeError(e)
	}
	r, e := decodeNativeRecord(c, v)
	if e == nil {
		e = ctx.Err()
	}
	return r, e
}
func (t *aptevaTx) write(c Collection, pk string, r Record, insert bool) error {
	if t.batch == nil || t.ddl != nil {
		return Invalid("native schema and record writes cannot share a transaction")
	}
	if e := t.ctx.Err(); e != nil {
		return e
	}
	if e := validateSQLiteIndexKeys(c, r); e != nil {
		return e
	}
	physical, _, e := indexTuple(c, primaryIndex(c), r)
	if e != nil {
		return e
	}
	if insert {
		old, e := t.getPhysical(c, physical)
		if e != nil {
			return e
		}
		if old != nil {
			return fail("unique_conflict", "primary key already exists")
		}
	}
	unique, e := t.checkUnique(c, physical, r)
	if e != nil {
		return e
	}
	data, e := encodeNativeRecord(c, r)
	if e != nil {
		return e
	}
	h, e := t.backend.handle(c)
	if e != nil {
		return e
	}
	batch := t.batch.With(h)
	if insert {
		e = batch.Insert(physical, data)
	} else {
		e = batch.Upsert(physical, data)
	}
	if e == nil {
		key := nativeRowKey{c.Name, string(physical)}
		t.clearUnique(key)
		t.pending[key] = data
		t.rowUnique[key] = unique
		for _, u := range unique {
			t.unique[u] = key.primary
		}
	}
	return nativeError(e)
}
func (t *aptevaTx) insert(c Collection, pk string, r Record) error { return t.write(c, pk, r, true) }
func (t *aptevaTx) put(c Collection, pk string, r Record) error    { return t.write(c, pk, r, false) }
func (t *aptevaTx) remove(c Collection, pk string) error {
	if t.batch == nil || t.ddl != nil {
		return Invalid("native schema and record writes cannot share a transaction")
	}
	physical, e := nativeKeyFromCanonical(c, pk)
	if e != nil {
		return e
	}
	h, e := t.backend.handle(c)
	if e != nil {
		return e
	}
	e = t.batch.With(h).Delete(physical)
	if e == nil {
		key := nativeRowKey{c.Name, string(physical)}
		t.clearUnique(key)
		t.pending[key] = nil
	}
	return e
}

func (t *aptevaTx) clearUnique(key nativeRowKey) {
	for _, u := range t.rowUnique[key] {
		delete(t.unique, u)
	}
	delete(t.rowUnique, key)
}

// Native batches validate final uniqueness at commit. The portable app contract
// also rejects a temporary conflict inside a batch, so preflight each operation
// against the snapshot plus an indexed overlay of earlier writes.
func (t *aptevaTx) checkUnique(c Collection, physical []byte, r Record) ([]nativeUniqueKey, error) {
	var keys []nativeUniqueKey
	for _, idx := range c.Indexes {
		if !idx.Unique {
			continue
		}
		tuple, nullable, e := indexTuple(c, idx, r)
		if e != nil {
			return nil, e
		}
		if nullable {
			continue
		}
		key := nativeUniqueKey{c.Name, idx.Name, string(tuple)}
		owner := string(physical)
		if staged, exists := t.unique[key]; exists && staged != owner {
			return nil, fail("unique_conflict", "unique index %s constraint violated", idx.Name)
		}
		h, e := t.readCollection(c)
		if e != nil {
			return nil, e
		}
		values := make([]any, len(idx.Fields))
		for i, o := range idx.Fields {
			values[i] = r[o.Field]
			f, _ := c.field(o.Field)
			if f.Type == "integer" {
				values[i], e = strconv.ParseInt(values[i].(string), 10, 64)
				if e != nil {
					return nil, e
				}
			}
		}
		cur, e := h.NewIndexCursor(t.ctx, idx.Name, native.IndexCursorOptions{Prefix: values})
		if e != nil {
			return nil, e
		}
		conflict := false
		for ok := cur.First(); ok; ok = cur.Next() {
			other := string(cur.Key())
			if _, changed := t.pending[nativeRowKey{c.Name, other}]; !changed && other != owner {
				conflict = true
				break
			}
		}
		e = errors.Join(cur.Err(), cur.Close())
		if e != nil {
			return nil, e
		}
		if conflict {
			return nil, fail("unique_conflict", "unique index %s constraint violated", idx.Name)
		}
		keys = append(keys, key)
	}
	return keys, nil
}

// Build typed documents directly: integer wire strings remain int64 and a
// number field remains float64 even when its value has no fractional digits.
func encodeNativeRecord(c Collection, r Record) ([]byte, error) {
	var b native.DocBuilder
	for _, f := range allFields(c) {
		v := r[f.Name]
		if v == nil {
			b.Null(f.Name)
			continue
		}
		switch f.Type {
		case "integer":
			n, e := strconv.ParseInt(v.(string), 10, 64)
			if e != nil {
				return nil, e
			}
			b.Int(f.Name, n)
		case "number":
			b.Float(f.Name, number(v))
		case "boolean":
			b.Bool(f.Name, v.(bool))
		case "json":
			b.String(f.Name, string(marshal(v)))
		default:
			b.String(f.Name, v.(string))
		}
	}
	data := b.Bytes()
	if len(data) == 0 {
		return nil, fail("resource_limit", "native document encoding limit")
	}
	return data, nil
}
func decodeNativeRecord(c Collection, data []byte) (Record, error) {
	values, e := native.DecodeDocument(data)
	if e != nil {
		return nil, e
	}
	r := Record(values)
	for _, f := range allFields(c) {
		v, exists := values[f.Name]
		if !exists {
			return nil, fail("storage_error", "native record is missing field %s", f.Name)
		}
		if v != nil {
			switch f.Type {
			case "integer":
				n, ok := v.(int64)
				if !ok {
					return nil, fail("storage_error", "invalid stored integer")
				}
				v = strconv.FormatInt(n, 10)
			case "number":
				v = number(v)
			case "json":
				text, ok := v.(string)
				if !ok {
					return nil, fail("storage_error", "invalid stored JSON")
				}
				if e = decode([]byte(text), &v); e != nil {
					return nil, e
				}
			}
		}
		r[f.Name] = v
	}
	return r, nil
}

func nativeKeyFromCanonical(c Collection, pk string) ([]byte, error) {
	var values []any
	if e := decode([]byte(pk), &values); e != nil || len(values) != len(c.PrimaryKey) {
		return nil, fail("storage_error", "invalid primary key encoding")
	}
	r := make(Record, len(values))
	for i, p := range c.PrimaryKey {
		r[p] = values[i]
	}
	key, _, e := indexTuple(c, primaryIndex(c), r)
	return key, e
}
