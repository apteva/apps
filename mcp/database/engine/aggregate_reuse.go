package engine

import (
	"bytes"
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/gob"
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	aggregateCacheBytes = 32 << 20
	aggregateEntryBytes = 512 << 10
	aggregateKeyBytes   = 64 << 10
	maxAggregateFlights = 16
	maxAggregateWaiters = 64
	aggregateCacheTTL   = 30 * time.Second
	aggregateWorkLimit  = 60 * time.Second
)

// A backend incarnation has its own read-revision domain, including after
// cache eviction or a realtime policy change. Keys retain only its token.
type aggregateKey struct {
	instance *byte
	revision uint64
	query    [32]byte
}

type aggregateFlight struct {
	done             chan struct{}
	cancel           context.CancelFunc
	waiters          int
	result           map[string]any
	trace            map[string]any
	cacheStore       string
	cacheResultBytes int
	err              error
}

type aggregateEntry struct {
	key     aggregateKey
	result  string
	bytes   int
	expires time.Time
}

type aggregateReuse struct {
	mu      sync.Mutex
	ctx     context.Context
	cancel  context.CancelFunc
	cache   bool
	active  int
	flights map[aggregateKey]*aggregateFlight
	entries map[aggregateKey]*list.Element
	lru     list.List
	bytes   int
}

func newAggregateReuse(cache bool) *aggregateReuse {
	ctx, cancel := context.WithCancel(context.Background())
	return &aggregateReuse{ctx: ctx, cancel: cancel, cache: cache,
		flights: make(map[aggregateKey]*aggregateFlight), entries: make(map[aggregateKey]*list.Element)}
}

// Hash the parsed wire shape, conservatively preserving order and every
// result-affecting field. The encoded copy also detaches worker input from
// caller-owned filters, which the executor normalizes in place.
func reusableAggregateBatch(r Request, cursorKey []byte) ([]byte, string) {
	if r.ComputedCollection != nil {
		return nil, "unsupported_query"
	}
	for _, op := range r.Operations {
		child := op.Request
		switch op.Op {
		case "get", "find", "aggregate":
		default:
			return nil, "unsupported_operation"
		}
		if child.Diagnostics || child.TimeoutMS != 0 || child.ComputedCollection != nil ||
			(child.Database != "" && child.Database != r.Database) ||
			(child.Pipeline == nil && QueryKind(op.Op, child) != op.Op) || len(child.Lookups) != 0 || len(child.Buckets) != 0 {
			return nil, "unsupported_query"
		}
		if child.Pipeline != nil {
			if (op.Op != "find" && op.Op != "aggregate") || !reusablePipeline(child.Pipeline) {
				return nil, "unsupported_query"
			}
		}
		if op.Op == "get" {
			for _, value := range child.Key {
				if !reusableScalar(value) {
					return nil, "unsupported_query"
				}
			}
		} else if _, err := planCoreRead(op.Op, child); err != nil || !reusableFilter(child.Where, 0) {
			return nil, "unsupported_query"
		}
		for _, metric := range child.Metrics {
			if !reusableFilter(metric.Where, 0) {
				return nil, "unsupported_query"
			}
		}
		if op.Op == "find" && child.Cursor != "" {
			cursor, err := readCursor(child.Cursor, cursorKey)
			// A caller can wait up to the worker deadline. Let the normal path
			// validate cursors that could expire before the shared work finishes.
			if err != nil || cursor.Expires < time.Now().Add(aggregateWorkLimit).Unix() {
				return nil, "cursor_validation"
			}
		}
	}
	r.TimeoutMS, r.Diagnostics = 0, false
	data, err := json.Marshal(r)
	if err != nil {
		return nil, "request_encoding"
	}
	if len(data) > aggregateKeyBytes {
		return nil, "request_too_large"
	}
	return data, ""
}

// Only these deterministic node operations participate in reuse. Schema,
// expression and cardinality validation still runs on the execution snapshot.
func reusablePipeline(p *ReadPipeline) bool {
	if p.Version != 1 || len(p.Nodes) == 0 || len(p.Nodes) > maxQueryNodes {
		return false
	}
	for _, node := range p.Nodes {
		switch node.Op {
		case "scan", "compute", "join", "aggregate":
		default:
			return false
		}
		if !reusableFilter(node.Where, 0) {
			return false
		}
		for _, metric := range node.Metrics {
			if !reusableFilter(metric.Where, 0) {
				return false
			}
		}
		for _, join := range node.Joins {
			if !reusableFilter(join.Where, 0) {
				return false
			}
		}
	}
	return reusablePipelineValue(reflect.ValueOf(p), 0)
}

// omitempty erases non-nil empty expression/filter containers that validation
// can distinguish from omitted inputs. Keep these requests on the normal path.
// Inspect the entire pipeline, including nested constants, before serializing.
func reusablePipelineValue(v reflect.Value, depth int) bool {
	if depth > 64 {
		return false
	}
	switch v.Kind() {
	case reflect.Interface, reflect.Pointer:
		return v.IsNil() || reusablePipelineValue(v.Elem(), depth+1)
	case reflect.Slice, reflect.Map:
		if v.IsNil() {
			return true
		}
		if v.Len() == 0 {
			return false
		}
		if v.Kind() == reflect.Map {
			iter := v.MapRange()
			for iter.Next() {
				if !reusablePipelineValue(iter.Key(), depth+1) || !reusablePipelineValue(iter.Value(), depth+1) {
					return false
				}
			}
			return true
		}
		for i := 0; i < v.Len(); i++ {
			if !reusablePipelineValue(v.Index(i), depth+1) {
				return false
			}
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if !reusablePipelineValue(v.Field(i), depth+1) {
				return false
			}
		}
	case reflect.String:
		return utf8.ValidString(v.String())
	}
	return true
}

func (m *Manager) executeUnsharedReadBatch(ctx context.Context, d *database, r Request, reason string) (any, error) {
	result, err := m.executeDatabase(ctx, d, "", "batch", "read_batch", r)
	if err == nil && r.Diagnostics {
		diag := result.(map[string]any)["diagnostics"].(map[string]any)
		diag["reuse"], diag["reuseReason"], diag["aggregateExecutions"] = "skipped", reason, 1
	}
	return result, err
}

func reusableFilter(f *Filter, depth int) bool {
	if f == nil {
		return true
	}
	// An empty non-nil slice participates in validation but omitempty would
	// erase it. Keep these and relation-based predicates on the normal path.
	if depth > 8 || f.Exists != nil || f.NotExists != nil ||
		(f.And != nil && len(f.And) == 0) || (f.Or != nil && len(f.Or) == 0) ||
		(f.Fields != nil && len(f.Fields) == 0) {
		return false
	}
	for i := range f.And {
		if !reusableFilter(&f.And[i], depth+1) {
			return false
		}
	}
	for i := range f.Or {
		if !reusableFilter(&f.Or[i], depth+1) {
			return false
		}
	}
	if values, ok := f.Value.([]any); ok {
		for _, value := range values {
			if !reusableScalar(value) {
				return false
			}
		}
		return true
	}
	return reusableScalar(f.Value)
}

func reusableScalar(v any) bool {
	switch v := v.(type) {
	case nil, bool, json.Number, float64, int:
		return true
	case string:
		return utf8.ValidString(v)
	default:
		return false
	}
}

// Owns the request's database lease. A newly registered worker takes it
// over so backend eviction cannot follow the leader's disconnect.
func (m *Manager) reuseAggregate(ctx context.Context, d *database, r Request, data []byte, release func()) (any, error) {
	ownsLease := true
	defer func() {
		if ownsLease {
			release()
		}
	}()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key := aggregateKey{query: sha256.Sum256(data)}
	d.mu.RLock()
	if d.closed {
		d.mu.RUnlock()
		return nil, fail("not_found", "database is closed")
	}
	backend := d.backend.(*aptevaBackend)
	err := backend.health()
	if err == nil {
		key.instance = backend.readInstance
		key.revision, err = backend.db.ReadRevision()
	}
	d.mu.RUnlock()
	if err != nil {
		return nil, nativeError(err)
	}
	reuse := m.aggregateReads
	reuse.mu.Lock()
	if err = reuse.ctx.Err(); err != nil {
		reuse.mu.Unlock()
		return nil, err
	}
	if element := reuse.entries[key]; element != nil {
		entry := element.Value.(*aggregateEntry)
		if time.Now().Before(entry.expires) {
			reuse.lru.MoveToFront(element)
			reuse.mu.Unlock()
			var result map[string]any
			if err := gob.NewDecoder(strings.NewReader(entry.result)).Decode(&result); err != nil {
				reuse.mu.Lock()
				if reuse.entries[key] == element {
					reuse.removeEntry(element)
				}
				reuse.mu.Unlock()
				return m.executeUnsharedReadBatch(ctx, d, r, "cache_decoding")
			}
			return aggregateResponse(ctx, r.Diagnostics, result, nil, "cache", 0)
		}
		reuse.removeEntry(element)
	}
	flight := reuse.flights[key]
	path := "inflight"
	if flight != nil {
		if flight.waiters >= maxAggregateWaiters {
			reuse.mu.Unlock()
			return nil, fail("resource_limit", "aggregate has too many waiting readers")
		}
		flight.waiters++
	} else {
		if reuse.active >= maxAggregateFlights {
			reuse.mu.Unlock()
			return m.executeUnsharedReadBatch(ctx, d, r, "inflight_limit")
		}
		work, cancel := context.WithTimeout(reuse.ctx, aggregateWorkLimit)
		flight = &aggregateFlight{done: make(chan struct{}), cancel: cancel, waiters: 1}
		reuse.flights[key] = flight
		reuse.active++
		ownsLease = false
		path = "executed"
		go m.runAggregateFlight(work, d, data, key, flight, release)
	}
	reuse.mu.Unlock()

	started := time.Now()
	select {
	case <-flight.done:
		if flight.err != nil {
			return nil, flight.err
		}
		return aggregateResponse(ctx, r.Diagnostics, flight.result, flight, path, time.Since(started))
	case <-ctx.Done():
		reuse.mu.Lock()
		if reuse.flights[key] == flight {
			flight.waiters--
			if flight.waiters == 0 {
				delete(reuse.flights, key)
				flight.cancel()
			}
		}
		reuse.mu.Unlock()
		return nil, ctx.Err()
	}
}

func (m *Manager) runAggregateFlight(ctx context.Context, d *database, data []byte, lookup aggregateKey, flight *aggregateFlight, release func()) {
	defer release()
	defer flight.cancel()
	actual := lookup
	result, err := m.executeAggregateFlight(ctx, d, data, &actual)
	reuse := m.aggregateReads
	if result != nil {
		flight.trace, _ = result["diagnostics"].(map[string]any)
		delete(result, "diagnostics")
	}
	var packed string
	if err == nil && ctx.Err() == nil {
		flight.cacheStore = "disabled"
		if reuse.cache {
			packed, flight.cacheResultBytes, flight.cacheStore = encodeAggregateResult(result)
		}
	}
	reuse.mu.Lock()
	defer reuse.mu.Unlock()
	if reuse.flights[lookup] == flight {
		delete(reuse.flights, lookup)
		if packed != "" && ctx.Err() == nil {
			reuse.storeEntry(actual, packed)
		}
	}
	flight.result, flight.err = result, err
	reuse.active--
	close(flight.done)
}

func (m *Manager) executeAggregateFlight(ctx context.Context, d *database, data []byte, actual *aggregateKey) (map[string]any, error) {
	var r Request
	if err := decode(data, &r); err != nil {
		return nil, err
	}
	r.diag = &countDiagnostics{started: time.Now()}
	done, locked, err := m.admitNativeRead(ctx, d, "batch", r)
	if err != nil {
		return nil, err
	}
	defer done()
	r.diag.queued()
	if !locked {
		d.mu.RLock()
	}
	defer d.mu.RUnlock()
	if d.closed {
		return nil, fail("not_found", "database is closed")
	}
	backend := d.backend.(*aptevaBackend)
	var result any
	err = backend.transaction(ctx, false, func(tx transaction) error {
		actual.instance = backend.readInstance
		actual.revision = tx.(*aptevaTx).snapshot.Revision()
		var err error
		result, err = execute(d.cachedTransaction(tx, true), "batch", r, d.key)
		return err
	})
	if err != nil {
		return nil, err
	}
	return result.(map[string]any), nil
}

func (s *aggregateReuse) close() {
	s.cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	clear(s.entries)
	s.lru.Init()
	s.bytes = 0
}

func (s *aggregateReuse) removeEntry(e *list.Element) {
	entry := e.Value.(*aggregateEntry)
	s.bytes -= entry.bytes
	delete(s.entries, entry.key)
	s.lru.Remove(e)
}

func (s *aggregateReuse) storeEntry(key aggregateKey, result string) {
	n := aggregateEntryOverhead + len(result)
	if old := s.entries[key]; old != nil {
		s.removeEntry(old)
	}
	for s.bytes+n > aggregateCacheBytes {
		s.removeEntry(s.lru.Back())
	}
	entry := &aggregateEntry{key: key, result: result, bytes: n, expires: time.Now().Add(aggregateCacheTTL)}
	s.entries[key] = s.lru.PushFront(entry)
	s.bytes += n
}

func aggregateResponse(ctx context.Context, diagnostics bool, result map[string]any, flight *aggregateFlight, path string, wait time.Duration) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := cloneAggregateValue(result, false).(map[string]any)
	if diagnostics {
		diag := map[string]any{"executed": false, "reuse": path, "aggregateExecutions": 0}
		if path == "executed" {
			diag = cloneAggregateValue(flight.trace, false).(map[string]any)
			diag["reuse"], diag["aggregateExecutions"] = path, 1
		} else if path == "inflight" {
			diag["coalescedWaitMs"] = milliseconds(wait)
		}
		if flight != nil {
			diag["cacheStore"] = flight.cacheStore
			if flight.cacheResultBytes != 0 {
				diag["cacheResultBytes"] = flight.cacheResultBytes
			}
		}
		out["diagnostics"] = diag
	}
	return out, nil
}

const aggregateEntryOverhead = 256 // entry, LRU element and amortized map overhead

type aggregateContainer uint8

const (
	nilAggregateRecord aggregateContainer = iota
	nilAggregateMap
	nilAggregateRows
	emptyAggregateRows
	nilAggregateArray
	emptyAggregateArray
)

var aggregateCodec sync.Once

func encodeAggregateResult(result map[string]any) (string, int, string) {
	aggregateCodec.Do(func() {
		gob.RegisterName("database.record", Record{})
		gob.RegisterName("database.rows", []Record{})
		gob.RegisterName("database.object", map[string]any{})
		gob.RegisterName("database.array", []any{})
		gob.RegisterName("database.number", json.Number(""))
		gob.RegisterName("database.container", aggregateContainer(0))
	})
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(cloneAggregateValue(result, true)); err != nil {
		return "", 0, "encode_failed"
	}
	size := buf.Len()
	if aggregateEntryOverhead+size > aggregateEntryBytes {
		return "", size, "result_too_large"
	}
	return buf.String(), size, "stored"
}

// The binary encoding preserves Go scalar types, including exact JSON numbers.
// Mark nil maps and empty slices explicitly because gob conflates those shapes.
func cloneAggregateValue(value any, packing bool) any {
	switch v := value.(type) {
	case map[string]any:
		if v == nil {
			if packing {
				return nilAggregateMap
			}
			return v
		}
		out := make(map[string]any, len(v)+1)
		for k, item := range v {
			out[k] = cloneAggregateValue(item, packing)
		}
		return out
	case Record:
		if v == nil && packing {
			return nilAggregateRecord
		}
		return Record(cloneAggregateValue(map[string]any(v), packing).(map[string]any))
	case []any:
		if v == nil {
			if packing {
				return nilAggregateArray
			}
			return v
		}
		if packing && len(v) == 0 {
			return emptyAggregateArray
		}
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = cloneAggregateValue(item, packing)
		}
		return out
	case []Record:
		if v == nil {
			if packing {
				return nilAggregateRows
			}
			return v
		}
		if packing && len(v) == 0 {
			return emptyAggregateRows
		}
		out := make([]Record, len(v))
		for i, record := range v {
			// Result rows are never nil.
			out[i] = cloneAggregateValue(record, packing).(Record)
		}
		return out
	case aggregateContainer:
		switch v {
		case nilAggregateRecord:
			return Record(nil)
		case nilAggregateMap:
			return map[string]any(nil)
		case nilAggregateRows:
			return []Record(nil)
		case emptyAggregateRows:
			return []Record{}
		case nilAggregateArray:
			return []any(nil)
		case emptyAggregateArray:
			return []any{}
		}
	}
	return value
}
