package engine

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"sort"
	"strconv"

	"github.com/cockroachdb/pebble/v2"
)

type pebbleBackend struct {
	db       *pebble.DB
	writeErr error
}
type kvReader interface {
	Get([]byte) ([]byte, io.Closer, error)
	NewIter(*pebble.IterOptions) (*pebble.Iterator, error)
}
type pebbleTx struct {
	ctx    context.Context
	reader kvReader
	batch  *pebble.Batch
}

func openPebble(path string) (backend, error) {
	db, e := pebble.Open(path, &pebble.Options{})
	if e != nil {
		return nil, e
	}
	return &pebbleBackend{db: db}, nil
}
func (b *pebbleBackend) close() error { return b.db.Close() }
func (b *pebbleBackend) transaction(ctx context.Context, write bool, fn func(transaction) error) error {
	if b.writeErr != nil {
		return b.writeErr
	}
	if write {
		batch := b.db.NewIndexedBatch()
		defer batch.Close()
		if e := fn(&pebbleTx{ctx, batch, batch}); e != nil {
			return e
		}
		if e := ctx.Err(); e != nil {
			return e
		}
		return b.commit(batch)
	}
	snap := b.db.NewSnapshot()
	defer snap.Close()
	return fn(&pebbleTx{ctx, snap, nil})
}

func (b *pebbleBackend) commit(batch *pebble.Batch) error {
	if err := batch.Commit(pebble.Sync); err != nil {
		// An unsuccessful sync has an uncertain durability outcome. Do not allow
		// later requests to rely on potentially unacknowledged data in memory.
		b.writeErr = fail("storage_error", "durable commit failed: %v; reopen database before retrying", err)
		return b.writeErr
	}
	return nil
}

// No asynchronous acknowledgements: every successful member returns only after
// Commit(Sync). Requests see earlier successful members but never failed ones.
func (b *pebbleBackend) transactions(calls []transactionCall) []error {
	return b.combine(calls, b.commit)
}

func (b *pebbleBackend) combine(calls []transactionCall, commit func(*pebble.Batch) error) []error {
	errs := make([]error, len(calls))
	if b.writeErr != nil {
		for i := range errs {
			errs[i] = b.writeErr
		}
		return errs
	}
	var staged *pebble.Batch
	var accepted []int
	flush := func() error {
		if staged == nil {
			return nil
		}
		err := commit(staged)
		staged.Close()
		staged = nil
		if err != nil {
			for _, i := range accepted {
				errs[i] = err
			}
		}
		accepted = nil
		return err
	}
	for i, call := range calls {
		if err := call.ctx.Err(); err != nil {
			errs[i] = err
			continue
		}
		next := b.db.NewIndexedBatch()
		// Copy the bounded staged group to provide a request-level savepoint.
		// Discarding next rolls back every change made by a rejected request.
		if staged != nil {
			errs[i] = next.Apply(staged, nil)
		}
		if errs[i] == nil {
			errs[i] = call.run(&pebbleTx{call.ctx, next, next})
		}
		if errs[i] == nil {
			errs[i] = call.ctx.Err()
		}
		if errs[i] != nil {
			next.Close()
			continue
		}
		if staged != nil {
			staged.Close()
		}
		staged = next
		accepted = append(accepted, i)
		// Limit copy overhead; a single existing API transaction can exceed this
		// threshold and is committed on its own without splitting its atomicity.
		if staged.Len() >= 1<<20 {
			if err := flush(); err != nil {
				for j := i + 1; j < len(errs); j++ {
					errs[j] = err
				}
				return errs
			}
		}
	}
	flush()
	return errs
}
func prefixEnd(b []byte) []byte {
	out := append([]byte{}, b...)
	for i := len(out) - 1; i >= 0; i-- {
		if out[i] != 255 {
			out[i]++
			return out[:i+1]
		}
	}
	return nil
}
func (t *pebbleTx) read(key []byte) ([]byte, error) {
	b, closer, e := t.reader.Get(key)
	if errors.Is(e, pebble.ErrNotFound) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	defer closer.Close()
	return append([]byte{}, b...), nil
}
func (t *pebbleTx) iter(prefix []byte) (*pebble.Iterator, error) {
	return t.reader.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: prefixEnd(prefix)})
}
func (t *pebbleTx) collections() ([]Collection, error) {
	it, e := t.iter([]byte("s/"))
	if e != nil {
		return nil, e
	}
	defer it.Close()
	out := []Collection{}
	for it.First(); it.Valid(); it.Next() {
		if e := t.ctx.Err(); e != nil {
			return nil, e
		}
		var c Collection
		if e := decode(it.Value(), &c); e != nil {
			return nil, e
		}
		out = append(out, c)
	}
	return out, it.Error()
}
func (t *pebbleTx) collection(name string) (Collection, error) {
	var c Collection
	b, e := t.read([]byte("s/" + name))
	if e != nil {
		return c, e
	}
	if b == nil {
		return c, fail("not_found", "collection %s does not exist", name)
	}
	e = decode(b, &c)
	return c, e
}
func (t *pebbleTx) save(c Collection) error             { return t.batch.Set([]byte("s/"+c.Name), marshal(c), nil) }
func (t *pebbleTx) createCollection(c Collection) error { return t.save(c) }
func (t *pebbleTx) dropCollection(c Collection) error {
	for _, p := range []string{"r/" + c.Name + "/", "i/" + c.Name + "/", "u/" + c.Name + "/"} {
		if e := t.batch.DeleteRange([]byte(p), prefixEnd([]byte(p)), nil); e != nil {
			return e
		}
	}
	return t.batch.Delete([]byte("s/"+c.Name), nil)
}
func primaryIndex(c Collection) Index {
	idx := Index{Name: "primary", Unique: true}
	for _, p := range c.PrimaryKey {
		idx.Fields = append(idx.Fields, Order{p, "asc"})
	}
	return idx
}
func indexes(c Collection) []Index { return append([]Index{primaryIndex(c)}, c.Indexes...) }
func indexPrefix(c Collection, i Index, unique bool) []byte {
	p := "i/"
	if unique {
		p = "u/"
	}
	return []byte(p + c.Name + "/" + i.Name + "/")
}

// Each scalar encoding is prefix-free and order-preserving. Direction is
// applied to the complete scalar, including its terminator and null marker.
func scalar(f Field, v any, desc bool) []byte {
	out := []byte{0}
	if v != nil {
		out = []byte{1}
		switch f.Type {
		case "integer":
			n, _ := strconv.ParseInt(v.(string), 10, 64)
			out = binary.BigEndian.AppendUint64(out, uint64(n)^(1<<63))
		case "number":
			n := number(v)
			bits := math.Float64bits(n)
			if bits&(1<<63) != 0 {
				bits = ^bits
			} else {
				bits ^= 1 << 63
			}
			out = binary.BigEndian.AppendUint64(out, bits)
		case "boolean":
			if v.(bool) {
				out = append(out, 1)
			} else {
				out = append(out, 0)
			}
		default:
			for _, b := range []byte(v.(string)) {
				if b == 0 {
					out = append(out, 0, 255)
				} else {
					out = append(out, b)
				}
			}
			out = append(out, 0, 0)
		}
	}
	if desc {
		for i := range out {
			out[i] = ^out[i]
		}
	}
	return out
}
func indexTuple(c Collection, i Index, r Record) ([]byte, bool, error) {
	out := []byte{}
	hasNull := false
	for _, o := range i.Fields {
		f, _ := c.field(o.Field)
		if r[o.Field] == nil {
			hasNull = true
		}
		out = append(out, scalar(f, r[o.Field], o.Direction == "desc")...)
	}
	if len(out) > 4096 {
		return nil, false, Invalid("encoded index key exceeds 4096 bytes")
	}
	return out, hasNull, nil
}
func (t *pebbleTx) get(c Collection, pk string) (Record, error) {
	b, e := t.read([]byte("r/" + c.Name + "/" + pk))
	if e != nil || b == nil {
		return nil, e
	}
	var r Record
	e = decode(b, &r)
	return r, e
}
func (t *pebbleTx) entry(c Collection, i Index, pk string, r Record, remove bool) error {
	tuple, null, e := indexTuple(c, i, r)
	if e != nil {
		return e
	}
	key := append(indexPrefix(c, i, false), tuple...)
	primaryTuple, _, e := indexTuple(c, primaryIndex(c), r)
	if e != nil {
		return e
	}
	key = append(key, primaryTuple...)
	if remove {
		if e := t.batch.Delete(key, nil); e != nil {
			return e
		}
	} else {
		if e := t.batch.Set(key, []byte(pk), nil); e != nil {
			return e
		}
	}
	if i.Unique && !null {
		u := append(indexPrefix(c, i, true), tuple...)
		if remove {
			return t.batch.Delete(u, nil)
		}
		owner, e := t.read(u)
		if e != nil {
			return e
		}
		if owner != nil && string(owner) != pk {
			return fail("unique_conflict", "index %s already contains this value", i.Name)
		}
		return t.batch.Set(u, []byte(pk), nil)
	}
	return nil
}
func (t *pebbleTx) put(c Collection, pk string, r Record) error {
	old, e := t.get(c, pk)
	if e != nil {
		return e
	}
	for _, i := range indexes(c) {
		if old != nil {
			if e := t.entry(c, i, pk, old, true); e != nil {
				return e
			}
		}
		if e := t.entry(c, i, pk, r, false); e != nil {
			return e
		}
	}
	return t.batch.Set([]byte("r/"+c.Name+"/"+pk), marshal(r), nil)
}
func (t *pebbleTx) insert(c Collection, pk string, r Record) error {
	old, e := t.get(c, pk)
	if e != nil {
		return e
	}
	if old != nil {
		return fail("unique_conflict", "primary key already exists")
	}
	for _, i := range indexes(c) {
		if e := t.entry(c, i, pk, r, false); e != nil {
			return e
		}
	}
	return t.batch.Set([]byte("r/"+c.Name+"/"+pk), marshal(r), nil)
}
func (t *pebbleTx) remove(c Collection, pk string) error {
	old, e := t.get(c, pk)
	if e != nil || old == nil {
		return e
	}
	for _, i := range indexes(c) {
		if e := t.entry(c, i, pk, old, true); e != nil {
			return e
		}
	}
	return t.batch.Delete([]byte("r/"+c.Name+"/"+pk), nil)
}
func (t *pebbleTx) createIndex(c Collection, i Index) error {
	it, e := t.iter([]byte("r/" + c.Name + "/"))
	if e != nil {
		return e
	}
	defer it.Close()
	n := 0
	for it.First(); it.Valid(); it.Next() {
		n++
		if n > MaxIndexBuildRecords {
			return fail("resource_limit", "synchronous index build exceeds 10000 records")
		}
		if e := t.ctx.Err(); e != nil {
			return e
		}
		var r Record
		if e := decode(it.Value(), &r); e != nil {
			return e
		}
		pk, e := primary(c, r)
		if e != nil {
			return e
		}
		if e := t.entry(c, i, pk, r, false); e != nil {
			return e
		}
	}
	if e := it.Error(); e != nil {
		return e
	}
	c.Indexes = append(c.Indexes, i)
	c.Version++
	return t.save(c)
}
func (t *pebbleTx) dropIndex(c Collection, name string) error {
	found := false
	out := []Index{}
	for _, i := range c.Indexes {
		if i.Name == name {
			found = true
			for _, u := range []bool{false, true} {
				p := indexPrefix(c, i, u)
				if e := t.batch.DeleteRange(p, prefixEnd(p), nil); e != nil {
					return e
				}
			}
		} else {
			out = append(out, i)
		}
	}
	if !found {
		return fail("not_found", "index does not exist")
	}
	c.Indexes = out
	c.Version++
	return t.save(c)
}

type accessPlan struct {
	Index         string `json:"index"`
	IndexedLookup bool   `json:"indexedLookup"`
	Sort          bool   `json:"sort"`
	Ranges        int    `json:"ranges,omitempty"`
	lower, upper  []byte
	ranges        []keyRange  // set for a union; lower and upper are then unused
	union         *accessPlan // a union an ordered plan could give way to
}

type keyRange struct{ lower, upper []byte }

func conjuncts(f *Filter) []Filter {
	if f == nil {
		return nil
	}
	if f.And != nil {
		out := []Filter{}
		for i := range f.And {
			out = append(out, conjuncts(&f.And[i])...)
		}
		return out
	}
	if f.Or != nil {
		return nil
	}
	return []Filter{*f}
}

// fieldBounds narrows prefix by the range predicates on the next index field.
func fieldBounds(f Field, o Order, prefix []byte, terms []Filter) (lower, upper []byte, n int) {
	lower = append([]byte{}, prefix...)
	upper = prefixEnd(prefix)
	for _, p := range terms {
		if p.Field != o.Field {
			continue
		}
		op := p.Op
		if op == "starts_with" {
			encoded := scalar(f, p.Value, o.Direction == "desc")
			v := append(append([]byte{}, prefix...), encoded[:len(encoded)-2]...)
			if bytes.Compare(v, lower) > 0 {
				lower = v
			}
			hi := prefixEnd(v)
			if upper == nil || bytes.Compare(hi, upper) < 0 {
				upper = hi
			}
			n++
			continue
		}
		if op != "gt" && op != "gte" && op != "lt" && op != "lte" {
			continue
		}
		if o.Direction == "desc" {
			op = map[string]string{"gt": "lt", "gte": "lte", "lt": "gt", "lte": "gte"}[op]
		}
		v := append(append([]byte{}, prefix...), scalar(f, p.Value, o.Direction == "desc")...)
		switch op {
		case "gt":
			v = prefixEnd(v)
			if bytes.Compare(v, lower) > 0 {
				lower = v
			}
			n++
		case "gte":
			if bytes.Compare(v, lower) > 0 {
				lower = v
			}
			n++
		case "lt":
			if upper == nil || bytes.Compare(v, upper) < 0 {
				upper = v
			}
			n++
		case "lte":
			v = prefixEnd(v)
			if upper == nil || bytes.Compare(v, upper) < 0 {
				upper = v
			}
			n++
		}
	}
	return lower, upper, n
}

// maxPlanRanges and maxPlanRangeBytes bound the ranges a multi-value in expands
// to. Past them the plan keeps its single range and the filter still applies.
const (
	maxPlanRanges     = 64
	maxPlanRangeBytes = 1 << 20
)

// unionRanges expands a multi-value in on leading index fields into one range
// per listed tuple, each bounded by a range on the next field. Scalar encodings
// are prefix-free, so distinct tuples give disjoint ranges and no record is read
// twice. The caller's matches still checks the complete predicate.
func unionRanges(c Collection, idx Index, terms []Filter) (out []keyRange, n int, ok bool) {
	prefixes := [][]byte{indexPrefix(c, idx, false)}
	multi := false
	var field *Order
	for k := range idx.Fields {
		o := idx.Fields[k]
		f, _ := c.field(o.Field)
		var equal, in *Filter
		for j := range terms {
			p := &terms[j]
			if p.Field != o.Field {
				continue
			}
			if p.Op == "eq" || p.Op == "is_null" {
				equal = p
				break
			}
			if p.Op == "in" && in == nil {
				in = p
			}
		}
		values := []any{}
		switch {
		case equal != nil:
			values = append(values, equal.Value)
		case in != nil:
			values = in.Value.([]any)
			multi = true
		default:
			field = &idx.Fields[k]
		}
		if field != nil {
			break
		}
		seen := map[string]bool{}
		grown := [][]byte{}
		for _, v := range values {
			encoded := scalar(f, v, o.Direction == "desc")
			if seen[string(encoded)] {
				continue
			}
			seen[string(encoded)] = true
			if len(prefixes)*len(seen) > maxPlanRanges {
				return nil, 0, false
			}
			for _, prefix := range prefixes {
				grown = append(grown, append(prefix[:len(prefix):len(prefix)], encoded...))
			}
		}
		prefixes = grown
		n += 10
	}
	if !multi {
		return nil, 0, false
	}
	out = make([]keyRange, 0, len(prefixes))
	size := 0
	for _, prefix := range prefixes {
		lower, upper := prefix, prefixEnd(prefix)
		if field != nil {
			f, _ := c.field(field.Field)
			var m int
			lower, upper, m = fieldBounds(f, *field, prefix, terms)
			if len(out) == 0 {
				n += m
			}
		}
		if size += len(lower) + len(upper); size > maxPlanRangeBytes {
			return nil, 0, false
		}
		if bytes.Compare(lower, upper) < 0 {
			out = append(out, keyRange{lower, upper})
		}
	}
	sort.Slice(out, func(i, j int) bool { return bytes.Compare(out[i].lower, out[j].lower) < 0 })
	return out, n, true
}

func plan(c Collection, q Query) accessPlan {
	best := accessPlan{Sort: true}
	score := -1
	var union *accessPlan
	unionScore := -1
	for _, idx := range indexes(c) {
		if q.hint != "" && idx.Name != q.hint {
			continue
		}
		prefix := indexPrefix(c, idx, false)
		eq := map[string]bool{}
		n := 0
		terms := conjuncts(q.Where)
		var lower, upper []byte
		for _, o := range idx.Fields {
			f, _ := c.field(o.Field)
			var equal *Filter
			for j := range terms {
				p := &terms[j]
				if p.Field == o.Field && (p.Op == "eq" || p.Op == "is_null") {
					equal = p
					break
				}
			}
			if equal != nil {
				prefix = append(prefix, scalar(f, equal.Value, o.Direction == "desc")...)
				eq[o.Field] = true
				n += 10
				continue
			}
			var m int
			lower, upper, m = fieldBounds(f, o, prefix, terms)
			n += m
			break
		}
		if lower == nil {
			lower = prefix
			upper = prefixEnd(prefix)
		}
		actual := []Order{}
		seen := map[string]bool{}
		for _, o := range idx.Fields {
			seen[o.Field] = true
			if !eq[o.Field] {
				actual = append(actual, o)
			}
		}
		for _, p := range c.PrimaryKey {
			if !seen[p] && !eq[p] {
				actual = append(actual, Order{p, "asc"})
			}
		}
		desired := []Order{}
		for _, o := range q.OrderBy {
			if !eq[o.Field] {
				desired = append(desired, o)
			}
		}
		ordered := len(desired) <= len(actual)
		if ordered {
			for j, o := range desired {
				if o != actual[j] {
					ordered = false
					break
				}
			}
		}
		if ordered && q.after != nil {
			values := Record{}
			for _, term := range terms {
				if term.Op == "eq" || term.Op == "is_null" {
					values[term.Field] = term.Value
				}
			}
			for field, value := range q.after {
				values[field] = value
			}
			complete := true
			for _, field := range idx.Fields {
				if _, ok := values[field.Field]; !ok {
					complete = false
				}
			}
			if complete {
				tuple, _, err := indexTuple(c, idx, values)
				pk, _, pkErr := indexTuple(c, primaryIndex(c), values)
				if err == nil && pkErr == nil {
					seek := append(indexPrefix(c, idx, false), tuple...)
					seek = prefixEnd(append(seek, pk...))
					if bytes.Compare(seek, lower) > 0 {
						lower = seek
					}
					n++
				}
			}
		}
		s := n * 10
		if ordered {
			s++
		}
		if s > score {
			score = s
			best = accessPlan{Index: idx.Name, IndexedLookup: n > 0, Sort: !ordered, lower: lower, upper: upper}
		}
		// Exists probes name their index and bind it by equality, so they stay
		// on one range.
		if q.hint == "" {
			if ranges, m, ok := unionRanges(c, idx, terms); ok && m*10 > unionScore {
				unionScore = m * 10
				u := accessPlan{Index: idx.Name, IndexedLookup: true, Sort: len(q.OrderBy) > 0, Ranges: len(ranges), ranges: ranges}
				union = &u
			}
		}
	}
	if union == nil {
		return best
	}
	// A union is in key order only within each range, so it reads every match to
	// sort a page, where an ordered stream can stop early. Only the caller can
	// weigh those, knowing how many records match.
	if len(q.OrderBy) > 0 && !best.Sort && (best.IndexedLookup || !q.RequireIndex) {
		best.union = union
		return best
	}
	if unionScore > score {
		return *union
	}
	return best
}
func (t *pebbleTx) scan(c Collection, q Query, unordered bool, consume func(Record) bool) error {
	return t.scanFields(c, q, unordered, nil, func(r Record, _ []byte) bool {
		return consume(retainSelected(r, Query{}))
	})
}

// Both r and raw are borrowed until the callback returns. Selected values own
// their storage, but the row map itself is reused for the next candidate.
func (t *pebbleTx) scanFields(c Collection, q Query, unordered bool, fields []string, consume func(Record, []byte) bool) error {
	p := plan(c, q)
	if q.RequireIndex && !p.IndexedLookup {
		return fail("query_too_expensive", "query requires a selective index")
	}
	// A broad unordered scan can stream the record values directly instead of
	// traversing a secondary key and doing a second lookup for every record.
	direct := unordered && !p.IndexedLookup
	if direct {
		p.lower = []byte("r/" + c.Name + "/")
		p.upper = prefixEnd(p.lower)
	}
	spans := p.ranges
	if spans == nil {
		if p.upper != nil && bytes.Compare(p.lower, p.upper) >= 0 {
			return nil
		}
		spans = []keyRange{{p.lower, p.upper}}
	}
	var records *pebble.Iterator
	recordPrefix := []byte("r/" + c.Name + "/")
	key := append([]byte{}, recordPrefix...)
	if !direct {
		var e error
		records, e = t.iter(recordPrefix)
		if e != nil {
			return e
		}
		defer records.Close()
	}
	decoder := newScanDecoder(c, fields, q.Where)
	stopped := false
	scan := func(span keyRange) error {
		it, e := t.reader.NewIter(&pebble.IterOptions{LowerBound: span.lower, UpperBound: span.upper})
		if e != nil {
			return e
		}
		defer it.Close()
		for it.First(); it.Valid(); it.Next() {
			if e := t.ctx.Err(); e != nil {
				return e
			}
			raw := it.Value()
			if !direct {
				key = append(key[:len(recordPrefix)], raw...)
				// Preserve index order while reusing a record iterator. Adjacent primary
				// keys usually need just Next; non-adjacent or differently encoded keys
				// seek exactly, including compound/numeric keys and secondary indexes.
				if records.Valid() && bytes.Compare(records.Key(), key) < 0 {
					if !records.Next() && records.Error() != nil {
						return records.Error()
					}
				}
				if !records.Valid() || !bytes.Equal(records.Key(), key) {
					records.SeekGE(key)
				}
				if e := records.Error(); e != nil {
					return e
				}
				if !records.Valid() || !bytes.Equal(records.Key(), key) {
					return fail("storage_error", "index references a missing record")
				}
				raw = records.Value()
			}
			r, e := decoder.decode(raw)
			if e != nil {
				return e
			}
			if matches(c, r, q.Where) && !consume(r, raw) {
				stopped = true
				break
			}
		}
		return it.Error()
	}
	for _, span := range spans {
		if e := scan(span); e != nil {
			return e
		}
		if stopped {
			break
		}
	}
	return t.ctx.Err()
}
func (t *pebbleTx) find(c Collection, q Query, limit int) ([]Record, error) {
	out := []Record{}
	p := plan(c, q)
	top := &topRecords{collection: c, query: q, limit: limit}
	var workErr error
	size := 0
	fields := append([]string{}, q.Select...)
	for _, o := range q.OrderBy {
		fields = append(fields, o.Field)
	}
	e := t.scanFields(c, q, p.Sort, fields, func(r Record, raw []byte) bool {
		if p.Sort && !top.accepts(r) {
			return true
		}
		if len(q.Select) == 0 {
			// Hydrate full documents only after filtering and top-page rejection.
			var full Record
			workErr = decode(raw, &full)
			if workErr != nil {
				return false
			}
			r = full
		}
		if p.Sort {
			workErr = top.offer(r)
			return workErr == nil
		}
		r = retainSelected(r, q)
		size += retainedBytes(r)
		if size > MaxQueryWorkBytes {
			workErr = fail("resource_limit", "retained query results exceed 16 MiB")
			return false
		}
		out = append(out, r)
		return len(out) < limit
	})
	if e != nil {
		return nil, e
	}
	if workErr != nil {
		return nil, workErr
	}
	if p.Sort {
		for _, r := range top.rows {
			out = append(out, r.row)
		}
		sorted(c, out, q.OrderBy)
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, t.ctx.Err()
}
func (t *pebbleTx) explain(c Collection, q Query) (any, error) {
	return map[string]any{"adapter": "pebble", "plan": plan(c, q), "candidateLimit": nil, "maxRetainedBytes": MaxQueryWorkBytes, "executionBound": "deadline"}, nil
}
func (t *pebbleTx) aggregate(c Collection, r Request, outSchema Collection) ([]Record, error) {
	if r.analytics == nil && r.Where == nil && !r.RequireIndex && len(r.GroupBy) == 0 && len(r.Metrics) == 1 && r.Metrics[0].Op == "count" && r.Metrics[0].Field == "" {
		it, e := t.iter([]byte("r/" + c.Name + "/"))
		if e != nil {
			return nil, e
		}
		defer it.Close()
		var count int64
		for it.First(); it.Valid(); it.Next() {
			if e := t.ctx.Err(); e != nil {
				return nil, e
			}
			count++
		}
		if e := it.Error(); e != nil {
			return nil, e
		}
		if e := t.ctx.Err(); e != nil {
			return nil, e
		}
		return []Record{{r.Metrics[0].Name: strconv.FormatInt(count, 10)}}, nil
	}
	return aggregateRecords(t.ctx, c, r, outSchema, func(q Query, fields []string, consume func(Record, []byte) bool) error {
		return t.scanFields(c, q, true, fields, consume)
	})
}

func (t *pebbleTx) scanRecords(c Collection, q Query, fields []string, consume func(Record) bool) error {
	return t.scanFields(c, q, true, fields, func(r Record, _ []byte) bool { return consume(r) })
}

func (b *pebbleBackend) pointGet(ctx context.Context, c Collection, key Record, pk string) (Record, error) {
	if b.writeErr != nil {
		return nil, b.writeErr
	}
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	value, closer, e := b.db.Get([]byte("r/" + c.Name + "/" + pk))
	if errors.Is(e, pebble.ErrNotFound) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	defer closer.Close()
	var row Record
	if e = decode(value, &row); e != nil {
		return nil, e
	}
	if e = ctx.Err(); e != nil {
		return nil, e
	}
	return row, nil
}
