package engine

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"math"
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
	lower, upper  []byte
}

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
func plan(c Collection, q Query) accessPlan {
	best := accessPlan{Sort: true}
	score := -1
	for _, idx := range indexes(c) {
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
			best = accessPlan{idx.Name, n > 0, !ordered, lower, upper}
		}
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
	if p.upper != nil && bytes.Compare(p.lower, p.upper) >= 0 {
		return nil
	}
	// A broad unordered scan can stream the record values directly instead of
	// traversing a secondary key and doing a second lookup for every record.
	direct := unordered && !p.IndexedLookup
	if direct {
		p.lower = []byte("r/" + c.Name + "/")
		p.upper = prefixEnd(p.lower)
	}
	it, e := t.reader.NewIter(&pebble.IterOptions{LowerBound: p.lower, UpperBound: p.upper})
	if e != nil {
		return e
	}
	defer it.Close()
	var records *pebble.Iterator
	recordPrefix := []byte("r/" + c.Name + "/")
	key := append([]byte{}, recordPrefix...)
	if !direct {
		records, e = t.iter(recordPrefix)
		if e != nil {
			return e
		}
		defer records.Close()
	}
	decoder := newScanDecoder(c, fields, q.Where)
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
			break
		}
	}
	if e := it.Error(); e != nil {
		return e
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
	if r.Where == nil && !r.RequireIndex && len(r.GroupBy) == 0 && len(r.Metrics) == 1 && r.Metrics[0].Op == "count" && r.Metrics[0].Field == "" {
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
	// Keep typed accumulators while scanning; format portable decimal strings
	// once per output group, instead of allocating them for every input row.
	type metricState struct {
		count   int64
		sum     float64
		integer int64
		value   any
	}
	type groupState struct {
		row     Record
		metrics []metricState
	}
	groups := map[any]*groupState{}
	metricFields := make([]Field, len(r.Metrics))
	for i, m := range r.Metrics {
		if m.Field != "" {
			metricFields[i], _ = c.field(m.Field)
		}
	}
	groupFields := make([]Field, len(r.GroupBy))
	for i, g := range r.GroupBy {
		groupFields[i], _ = c.field(g)
	}
	workBytes := 0
	init := func(key any, row Record) error {
		out := Record{}
		for _, g := range r.GroupBy {
			out[g] = row[g]
		}
		for _, m := range r.Metrics {
			out[m.Name] = nil
		}
		// Include maps, keys, typed accumulator storage and final scalar growth.
		n := 256 + retainedBytes(key) + retainedBytes(out) + len(r.Metrics)*96
		if workBytes+n > MaxQueryWorkBytes {
			return fail("resource_limit", "aggregate group state exceeds 16 MiB")
		}
		workBytes += n
		groups[key] = &groupState{row: out, metrics: make([]metricState, len(r.Metrics))}
		return nil
	}
	if len(r.GroupBy) == 0 {
		if e := init(nil, nil); e != nil {
			return nil, e
		}
	}
	var aggErr error
	input := r.Query
	input.OrderBy = nil
	fields := append([]string{}, r.GroupBy...)
	for _, m := range r.Metrics {
		if m.Field != "" {
			fields = append(fields, m.Field)
		}
	}
	vals := make([]any, len(r.GroupBy))
	e := t.scanFields(c, input, true, fields, func(row Record, _ []byte) bool {
		var key any
		if len(r.GroupBy) > 0 {
			for i, g := range r.GroupBy {
				vals[i] = row[g]
				if vals[i] != nil && groupFields[i].Type == "number" {
					vals[i] = number(vals[i])
				}
			}
			// Scalar keys are already comparable and unambiguous within a query.
			// Only compound groups need a serialized tuple.
			if len(vals) == 1 {
				key = vals[0]
			} else {
				key = string(marshal(vals))
			}
		}
		group := groups[key]
		if group == nil {
			if aggErr = init(key, row); aggErr != nil {
				return false
			}
			group = groups[key]
		}
		for i, m := range r.Metrics {
			v := row[m.Field]
			state := &group.metrics[i]
			if v == nil && !(m.Op == "count" && m.Field == "") {
				continue
			}
			state.count++
			if m.Op == "count" {
				continue
			}
			f := metricFields[i]
			switch m.Op {
			case "min", "max":
				if state.value == nil || (m.Op == "min" && compare(f, v, state.value) < 0) || (m.Op == "max" && compare(f, v, state.value) > 0) {
					workBytes += retainedBytes(v) - retainedBytes(state.value)
					if workBytes > MaxQueryWorkBytes {
						aggErr = fail("resource_limit", "aggregate group state exceeds 16 MiB")
						return false
					}
					state.value = v
				}
			case "sum", "avg":
				if f.Type == "integer" && m.Op == "sum" {
					b, _ := strconv.ParseInt(v.(string), 10, 64)
					a := state.integer
					if (b > 0 && a > math.MaxInt64-b) || (b < 0 && a < math.MinInt64-b) {
						aggErr = fail("resource_limit", "aggregate integer overflow")
						return false
					}
					state.integer = a + b
				} else {
					b := number(v)
					if f.Type == "integer" {
						b, _ = strconv.ParseFloat(v.(string), 64)
					}
					sum := state.sum + b
					if math.IsInf(sum, 0) || math.IsNaN(sum) {
						aggErr = fail("resource_limit", "aggregate numeric overflow")
						return false
					}
					state.sum = sum
				}
			}
		}
		return true
	})
	if e != nil {
		return nil, e
	}
	if aggErr != nil {
		return nil, aggErr
	}
	out := []Record{}
	for _, group := range groups {
		if e := t.ctx.Err(); e != nil {
			return nil, e
		}
		for i, m := range r.Metrics {
			state := group.metrics[i]
			var v any
			if m.Op == "count" {
				v = strconv.FormatInt(state.count, 10)
			} else if state.count > 0 {
				switch m.Op {
				case "sum":
					if metricFields[i].Type == "integer" {
						v = strconv.FormatInt(state.integer, 10)
					} else {
						v = state.sum
					}
				case "avg":
					v = state.sum / float64(state.count)
				case "min", "max":
					v = state.value
				}
			}
			group.row[m.Name] = v
		}
		out = append(out, group.row)
	}
	sorted(outSchema, out, r.OrderBy)
	if len(out) > r.Limit+1 {
		out = out[:r.Limit+1]
	}
	return out, t.ctx.Err()
}
