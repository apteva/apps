package engine

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	native "github.com/apteva/db"
)

type nativeDDL struct {
	Before *Collection `json:"before"`
	After  *Collection `json:"after"`
}
type nativeIndexSpec struct {
	fields  []native.IndexField
	options native.IndexOptions
}

func nativeIndexes(c Collection) map[string]nativeIndexSpec {
	out := map[string]nativeIndexSpec{}
	fields := func(orders []Order) []native.IndexField {
		out := make([]native.IndexField, len(orders))
		for i, o := range orders {
			out[i] = native.IndexField{Field: o.Field, Desc: o.Direction == "desc"}
		}
		return out
	}
	out["primary"] = nativeIndexSpec{fields(primaryIndex(c).Fields), native.IndexOptions{Unique: true}}
	for _, i := range c.Indexes {
		out[i.Name] = nativeIndexSpec{fields(i.Fields), native.IndexOptions{Unique: i.Unique, NullsDistinct: i.Unique}}
	}
	return out
}
func (t *aptevaTx) stageDDL(before, after *Collection) error {
	if t.batch == nil || len(t.pending) > 0 || t.ddl != nil {
		return Invalid("native DDL requires its own transaction")
	}
	t.ddl = &nativeDDL{Before: before, After: after}
	return nil
}
func (t *aptevaTx) createCollection(c Collection) error { return t.stageDDL(nil, &c) }
func (t *aptevaTx) dropCollection(c Collection) error   { return t.stageDDL(&c, nil) }
func (t *aptevaTx) createIndex(c Collection, i Index) error {
	rows, e := t.find(c, Query{}, MaxIndexBuildRecords+1)
	if e != nil {
		return e
	}
	if len(rows) > MaxIndexBuildRecords {
		return fail("resource_limit", "synchronous index build exceeds 10000 records")
	}
	seen := map[string]bool{}
	for _, r := range rows {
		tuple, nullable, e := indexTuple(c, i, r)
		if e != nil {
			return e
		}
		if i.Unique && !nullable {
			key := string(tuple)
			if seen[key] {
				return fail("unique_conflict", "index contains duplicate values")
			}
			seen[key] = true
		}
	}
	before := cloneCollection(c)
	c.Indexes = append(append([]Index(nil), c.Indexes...), i)
	c.Version++
	return t.stageDDL(&before, &c)
}
func (t *aptevaTx) dropIndex(c Collection, name string) error {
	before := cloneCollection(c)
	out := make([]Index, 0, len(c.Indexes))
	found := false
	for _, i := range c.Indexes {
		if i.Name == name {
			found = true
		} else {
			out = append(out, i)
		}
	}
	if !found {
		return fail("not_found", "index does not exist")
	}
	c.Indexes = out
	c.Version++
	return t.stageDDL(&before, &c)
}
func (b *aptevaBackend) recoverDDL() error {
	data, found, e := b.db.Find([]byte(nativeIntentKey), nil)
	if e != nil || !found {
		return e
	}
	var intent nativeDDL
	if e = decode(data, &intent); e != nil {
		return fmt.Errorf("native DDL journal: %w", e)
	}
	if intent.Before == nil && intent.After == nil {
		return errors.New("native DDL journal has no schema")
	}
	for _, c := range []*Collection{intent.Before, intent.After} {
		if c != nil {
			if e := nameOK(c.Name); e != nil {
				return fmt.Errorf("native DDL journal: %w", e)
			}
		}
	}
	if intent.Before != nil && intent.After != nil && intent.Before.Name != intent.After.Name {
		return errors.New("native DDL journal changes collection identity")
	}
	return b.finishDDL(context.Background(), intent)
}
func (b *aptevaBackend) commitDDL(ctx context.Context, intent nativeDDL) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	if e := b.db.Upsert([]byte(nativeIntentKey), marshal(intent)); e != nil {
		return nativeError(e)
	}
	e := b.reconcileSchema(ctx, intent.Before, intent.After)
	if e != nil {
		if health := b.db.Health(); health != nil {
			return nativeError(health)
		}
		// Native operations reject cancellation before mutation. Compensate reversible
		// index/creation steps if a later step fails. A completed drop is finalized
		// below with an uncancelled catalog commit rather than abandoned halfway.
		if rollback := b.reconcileSchema(context.Background(), intent.After, intent.Before); rollback != nil {
			return b.poison(errors.Join(nativeError(e), fmt.Errorf("native schema rollback: %w", rollback)))
		}
		if clear := b.db.Delete([]byte(nativeIntentKey)); clear != nil {
			return b.poison(errors.Join(nativeError(e), clear))
		}
		b.invalidateSchema()
		return nativeError(e)
	}
	return b.publishSchema(intent)
}
func (b *aptevaBackend) finishDDL(ctx context.Context, intent nativeDDL) error {
	if e := b.reconcileSchema(ctx, intent.Before, intent.After); e != nil {
		return nativeError(e)
	}
	return b.publishSchema(intent)
}
func (b *aptevaBackend) publishSchema(intent nativeDDL) error {
	batch := b.db.NewBatch()
	var e error
	if intent.After == nil {
		e = batch.Delete(nativeSchemaKey(intent.Before.Name))
	} else {
		e = batch.Upsert(nativeSchemaKey(intent.After.Name), marshal(intent.After))
	}
	if e == nil {
		e = batch.Delete([]byte(nativeIntentKey))
	}
	// Once physical DDL succeeds, finish publication even if the request deadline
	// expires: the two catalog records become visible in one durable WAL group.
	if e == nil {
		e = batch.Commit()
	}
	b.invalidateSchema()
	if e != nil {
		return b.poison(e)
	}
	return nativeError(e)
}
func (b *aptevaBackend) reconcileSchema(ctx context.Context, previous, target *Collection) error {
	if target == nil {
		if previous == nil {
			return errors.New("native schema transition has no collection")
		}
		e := b.db.DropCollection(ctx, nativeCollectionName(previous.Name))
		if errors.Is(e, native.ErrNoSuchCollection) {
			return nil
		}
		return e
	}
	c, e := b.db.Collection(nativeCollectionName(target.Name))
	if e != nil {
		return e
	}
	desired := nativeIndexes(*target)
	for _, existing := range c.ListIndexes() {
		spec, wanted := desired[existing.Name]
		if wanted && reflect.DeepEqual(existing.Fields, spec.fields) && existing.Unique == spec.options.Unique && existing.NullsDistinct == spec.options.NullsDistinct {
			continue
		}
		if e = c.DropIndex(ctx, existing.Name); e != nil {
			return e
		}
	}
	existing := c.Indexes()
	for name, spec := range desired {
		if _, found := existing[name]; found {
			continue
		}
		if e = c.CreateCompoundIndex(ctx, name, spec.fields, spec.options); e != nil {
			return e
		}
	}
	return nil
}
