package engine

import (
	"bytes"
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
func (t *aptevaTx) dropCollection(c Collection) error {
	if c.ComputedCollection == nil {
		collections, err := t.collections()
		if err != nil {
			return err
		}
		for _, target := range collections {
			if target.ComputedCollection == nil {
				continue
			}
			if target.ComputedCollection.Source == c.Name {
				return fail("schema_conflict", "drop dependent computed collections first")
			}
			for _, input := range target.ComputedCollection.Sources {
				if input.Collection == c.Name {
					return fail("schema_conflict", "drop dependent computed collections first")
				}
			}
		}
	}
	return t.stageDDL(&c, nil)
}

// nativeIndexBuildLimit is MaxNativeIndexBuildRecords; tests lower it.
var nativeIndexBuildLimit int64 = MaxNativeIndexBuildRecords

// createIndex only counts the records: AptevaDB builds the index in one
// pass over the collection, validates uniqueness and key sizes before
// publishing it, and commitDDL rolls the schema back if the build fails.
func (t *aptevaTx) createIndex(c Collection, i Index) error {
	h, e := t.readCollection(c)
	if e != nil {
		return nativeError(e)
	}
	n, e := h.CountDocuments(t.ctx, []byte(`{}`), native.CountOptions{MaxMemoryBytes: int64(t.backend.options.QueryWorkBytes)})
	if e != nil {
		return nativeError(e)
	}
	if n > nativeIndexBuildLimit {
		return fail("resource_limit", "synchronous index build exceeds %d records (collection has %d)", nativeIndexBuildLimit, n)
	}
	before := cloneCollection(c)
	c.Indexes = append(append([]Index(nil), c.Indexes...), i)
	c.Version++
	return t.stageDDL(&before, &c)
}
func (t *aptevaTx) dropIndex(c Collection, name string) error {
	if c.onlineComputed() {
		if _, err := t.readCollection(c); err != nil {
			return err
		}
	}
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
	b.builds.step.Lock()
	defer b.builds.step.Unlock()
	if e := ctx.Err(); e != nil {
		return e
	}
	if e := b.db.Upsert([]byte(nativeIntentKey), marshal(intent)); e != nil {
		return nativeError(e)
	}
	e := b.reconcileSchema(ctx, intent.Before, intent.After)
	if e != nil {
		// Native operations reject cancellation before mutation. Compensate reversible
		// index/creation steps if a later step fails. A completed drop is finalized
		// below with an uncancelled catalog commit rather than abandoned halfway.
		if rollback := b.rollbackDDL(intent, e); rollback != nil {
			return rollback
		}
		return nativeError(e)
	}
	if err := b.publishSchema(intent); err != nil {
		return err
	}
	if intent.After != nil && intent.After.onlineComputed() {
		b.builds.failures.Delete(intent.After.Name)
		b.wakeComputedBuilds()
	}
	if intent.After == nil && intent.Before.onlineComputed() {
		b.builds.failures.Delete(intent.Before.Name)
	}
	return nil
}
func (b *aptevaBackend) finishDDL(ctx context.Context, intent nativeDDL) error {
	if e := b.reconcileSchema(ctx, intent.Before, intent.After); e != nil {
		if intent.Before == nil && intent.After != nil && intent.After.ComputedCollection != nil &&
			(errors.Is(e, native.ErrComputedCollection) || errors.Is(e, native.ErrComputedCollectionLimit) ||
				errors.Is(e, native.ErrKeySize) || errors.Is(e, native.ErrIndexValue)) {
			// An interrupted backfill can fail again after restart. Roll
			// back its intent instead of blocking access to the source.
			return b.rollbackDDL(intent, e)
		}
		return nativeError(e)
	}
	return b.publishSchema(intent)
}

func (b *aptevaBackend) rollbackDDL(intent nativeDDL, cause error) error {
	if health := b.db.Health(); health != nil {
		return nativeError(health)
	}
	if rollback := b.reconcileSchema(context.Background(), intent.After, intent.Before); rollback != nil {
		return b.poison(errors.Join(nativeError(cause), fmt.Errorf("native schema rollback: %w", rollback)))
	}
	if clear := b.db.Delete([]byte(nativeIntentKey)); clear != nil {
		return b.poison(errors.Join(nativeError(cause), clear))
	}
	b.invalidateSchema()
	return nil
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
	var c *native.Collection
	var e error
	if target.onlineComputed() {
		status, err := b.beginComputedMetrics(ctx, *target)
		if err != nil {
			return err
		}
		if !status.Ready {
			if len(target.Indexes) != 0 {
				return native.ErrComputedCollectionBuilding
			}
			return nil
		}
		c, e = b.db.OpenCollection(nativeCollectionName(target.Name))
	} else if target.ComputedCollection != nil {
		c, e = b.openComputedCollection(ctx, *target)
	} else {
		c, e = b.db.Collection(nativeCollectionName(target.Name))
	}
	if e != nil {
		return e
	}
	return reconcileNativeIndexes(ctx, c, *target)
}

func (s nativeIndexSpec) matches(index native.IndexInfo) bool {
	return reflect.DeepEqual(index.Fields, s.fields) && index.Unique == s.options.Unique &&
		index.NullsDistinct == s.options.NullsDistinct && index.Sparse == s.options.Sparse
}

func reconcileNativeIndexes(ctx context.Context, c *native.Collection, target Collection) error {
	missing := nativeIndexes(target)
	for _, existing := range c.ListIndexes() {
		spec, wanted := missing[existing.Name]
		if wanted && spec.matches(existing) {
			delete(missing, existing.Name)
			continue
		}
		if err := c.DropIndex(ctx, existing.Name); err != nil {
			return err
		}
	}
	for name, spec := range missing {
		if err := c.CreateCompoundIndex(ctx, name, spec.fields, spec.options); err != nil {
			return err
		}
	}
	return nil
}

// A snapshot can contain a completed native rollup while its app indexes
// are still being built. Check the indexes in that same snapshot, so even
// an already-running batch cannot mix pre- and post-publication reads.
func requireComputedIndexes(h *native.Collection, c Collection) error {
	missing := nativeIndexes(c)
	for _, index := range h.ListIndexes() {
		if spec, wanted := missing[index.Name]; wanted && spec.matches(index) {
			delete(missing, index.Name)
		}
	}
	if len(missing) != 0 {
		return native.ErrComputedCollectionBuilding
	}
	return nil
}

func (b *aptevaBackend) openComputedCollection(ctx context.Context, target Collection) (*native.Collection, error) {
	source := target.ComputedCollection.Source
	data, found, err := b.db.Find(nativeSchemaKey(source), nil)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fail("not_found", "computed collection source does not exist")
	}
	var schema Collection
	if err = decode(data, &schema); err != nil {
		return nil, err
	}
	pipeline, err := computedNativePipeline(schema, target)
	if err != nil {
		return nil, err
	}
	name, sourceName := nativeCollectionName(target.Name), nativeCollectionName(source)
	c, err := b.db.OpenCollection(name)
	if errors.Is(err, native.ErrNoSuchCollection) {
		return b.db.CreateComputedCollection(ctx, name, sourceName, pipeline)
	}
	if err != nil {
		return nil, err
	}
	info, err := c.ComputedCollection()
	if err != nil {
		return nil, err
	}
	if info == nil || info.Source != sourceName || !bytes.Equal(info.Pipeline, pipeline) {
		return nil, fail("schema_conflict", "physical computed collection differs from its catalog")
	}
	return c, nil
}
