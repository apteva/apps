package engine

import (
	"context"
	"sync"
)

type databaseName struct{ scope, name string }

// Schema reads are protected from DDL by database.mu. Store independent slices
// so neither caller-owned definitions nor returned descriptions can mutate them.
const maxCachedCollections = 256

type schemaCache struct {
	mu          sync.RWMutex
	collections map[string]Collection
}

func (s *schemaCache) get(name string) (Collection, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c, ok := s.collections[name]
	return c, ok
}
func (s *schemaCache) put(c Collection) {
	// v1 SQLite metadata must be read inside the first write's migration transaction.
	if c.StorageVersion == 1 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.collections == nil {
		s.collections = map[string]Collection{}
	}
	if _, exists := s.collections[c.Name]; exists || len(s.collections) < maxCachedCollections {
		s.collections[c.Name] = cloneCollection(c)
	}
}
func (s *schemaCache) clear() { s.mu.Lock(); s.collections = nil; s.mu.Unlock() }
func cloneCollection(c Collection) Collection {
	c.Fields = append([]Field(nil), c.Fields...)
	c.PrimaryKey = append([]string(nil), c.PrimaryKey...)
	if c.Indexes != nil {
		indexes := make([]Index, len(c.Indexes))
		copy(indexes, c.Indexes)
		for i := range indexes {
			indexes[i].Fields = append([]Order(nil), indexes[i].Fields...)
		}
		c.Indexes = indexes
	}
	return c
}

type cachedSchemaTx struct {
	transaction
	cache    *schemaCache
	readOnly bool
}

func (t cachedSchemaTx) collection(name string) (Collection, error) {
	if c, ok := t.cache.get(name); ok {
		return c, nil
	}
	c, e := t.transaction.collection(name)
	if e == nil && t.readOnly {
		t.cache.put(c)
	}
	return c, e
}
func (d *database) cachedTransaction(tx transaction, readOnly bool) transaction {
	return cachedSchemaTx{transaction: tx, cache: &d.schemas, readOnly: readOnly}
}
func isSchemaWrite(op string) bool {
	switch op {
	case "collection_create", "collection_drop", "index_create", "index_drop":
		return true
	}
	return false
}

type schemaInvalidator interface{ invalidateSchema() }

// A single point read needs one atomic backend lookup. The database read lock
// keeps the cached schema stable, without a separate backend transaction.
type pointReader interface {
	pointGet(context.Context, Collection, Record, string) (Record, error)
}

func (d *database) pointRead(ctx context.Context, b pointReader, r Request) (any, error) {
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	if e := nameOK(r.Collection); e != nil {
		return nil, e
	}
	c, ok := d.schemas.get(r.Collection)
	if !ok {
		e := d.backend.transaction(ctx, false, func(tx transaction) error {
			var e error
			c, e = d.cachedTransaction(tx, true).collection(r.Collection)
			return e
		})
		if e != nil {
			return nil, e
		}
	}
	if len(r.Key) != len(c.PrimaryKey) {
		return nil, Invalid("key must contain exactly the primary key fields")
	}
	key := make(Record, len(c.PrimaryKey))
	for _, p := range c.PrimaryKey {
		f, _ := c.field(p)
		v, e := normalize(f, r.Key[p])
		if e != nil {
			return nil, e
		}
		key[p] = v
	}
	// Preserve the portable primary-key byte limit on both backends.
	pk, e := primaryNormalized(c, key)
	if e != nil {
		return nil, e
	}
	row, e := b.pointGet(ctx, c, key, pk)
	return map[string]any{"found": row != nil, "record": row}, e
}
