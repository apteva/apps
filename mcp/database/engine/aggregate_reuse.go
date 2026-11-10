package engine

import (
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/json"
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
	done    chan struct{}
	cancel  context.CancelFunc
	waiters int
	result  map[string]any
	trace   map[string]any
	err     error
}

type aggregateEntry struct {
	key     aggregateKey
	result  map[string]any
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
func reusableAggregateBatch(r Request) []byte {
	if r.ComputedCollection != nil {
		return nil
	}
	for _, op := range r.Operations {
		child := op.Request
		if op.Op != "aggregate" || child.Diagnostics || child.TimeoutMS != 0 || child.ComputedCollection != nil ||
			(child.Database != "" && child.Database != r.Database) ||
			QueryKind(op.Op, child) != "aggregate" || len(child.Lookups) != 0 || len(child.Buckets) != 0 {
			return nil
		}
		if _, err := planCoreRead(op.Op, child); err != nil || !reusableFilter(child.Where, 0) {
			return nil
		}
		for _, metric := range child.Metrics {
			if !reusableFilter(metric.Where, 0) {
				return nil
			}
		}
	}
	r.TimeoutMS, r.Diagnostics = 0, false
	data, err := json.Marshal(r)
	if err != nil || len(data) > aggregateKeyBytes {
		return nil
	}
	return data
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
			return aggregateResponse(ctx, r.Diagnostics, entry.result, nil, "cache", 0)
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
			return m.executeDatabase(ctx, d, "", "batch", "read_batch", r)
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
		return aggregateResponse(ctx, r.Diagnostics, flight.result, flight.trace, path, time.Since(started))
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
	reuse.mu.Lock()
	defer reuse.mu.Unlock()
	if result != nil {
		flight.trace, _ = result["diagnostics"].(map[string]any)
		delete(result, "diagnostics")
	}
	if reuse.flights[lookup] == flight {
		delete(reuse.flights, lookup)
		if err == nil && ctx.Err() == nil && reuse.cache {
			reuse.storeEntry(actual, result)
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
	done, err := m.acquireNativeQuery(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	r.diag.admission("bulk", time.Since(r.diag.started))
	r.diag.queued()
	d.mu.RLock()
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

func (s *aggregateReuse) storeEntry(key aggregateKey, result map[string]any) {
	n := 256 + retainedBytes(result)
	if n > aggregateEntryBytes {
		return
	}
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

func aggregateResponse(ctx context.Context, diagnostics bool, result, trace map[string]any, path string, wait time.Duration) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := cloneAggregateValue(result).(map[string]any)
	if diagnostics {
		diag := map[string]any{"executed": false, "reuse": path, "aggregateExecutions": 0}
		if path == "executed" {
			diag = cloneAggregateValue(trace).(map[string]any)
			diag["reuse"], diag["aggregateExecutions"] = path, 1
		} else if path == "inflight" {
			diag["coalescedWaitMs"] = milliseconds(wait)
		}
		out["diagnostics"] = diag
	}
	return out, nil
}

func cloneAggregateValue(value any) any {
	switch v := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(v)+1)
		for k, item := range v {
			out[k] = cloneAggregateValue(item)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = cloneAggregateValue(item)
		}
		return out
	case []Record:
		if v == nil {
			return v
		}
		out := make([]Record, len(v))
		for i, record := range v {
			out[i] = Record(cloneAggregateValue(map[string]any(record)).(map[string]any))
		}
		return out
	default:
		return value
	}
}
