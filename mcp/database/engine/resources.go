package engine

import (
	"context"
	"time"
)

// These limits control configured cache targets and concurrent expensive reads,
// not file-backed RSS, WAL retention or the native engine's private allocations.
func (m *Manager) acquireDatabase(ctx context.Context, scope, name, adapter string, create bool) (*database, func(), error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, err := m.resolveLocked(ctx, scope, name, adapter, create)
	if err != nil {
		return nil, nil, err
	}
	d.users++
	m.cacheClock++
	d.lastUse = m.cacheClock
	return d, func() { m.mu.Lock(); d.users--; m.cacheClock++; d.lastUse = m.cacheClock; m.mu.Unlock() }, nil
}

// Caller holds m.mu. Active operations and registered watches keep their handle.
// Eviction closes only the backend; registry entries and stored data remain.
func (m *Manager) makeNativeCacheRoom() error {
	if m.apteva.CacheBytes == 0 {
		return nil
	} // Explicit legacy uncapped-cache opt-out.
	for {
		var used int64
		var oldest *database
		for _, d := range m.databases {
			if d.info.Adapter != "aptevadb" {
				continue
			}
			used += m.apteva.CacheBytes
			if d.users != 0 {
				continue
			}
			d.watchesMu.Lock()
			watched := len(d.watches) > 0
			d.watchesMu.Unlock()
			if !watched && (oldest == nil || d.lastUse < oldest.lastUse) {
				oldest = d
			}
		}
		if used+m.apteva.CacheBytes <= m.nativeCacheBudget {
			return nil
		}
		if oldest == nil {
			return fail("resource_limit", "native cache targets are in use; retry after active queries or watches finish")
		}
		oldest.mu.Lock()
		oldest.closed = true
		err := oldest.backend.close()
		oldest.mu.Unlock()
		delete(m.databases, oldest.info.ID)
		for name, d := range m.names {
			if d == oldest {
				delete(m.names, name)
			}
		}
		if err != nil {
			return err
		}
	}
}

const (
	maxQueuedNativeQueries = 64
	maxQueuedShortQueries  = 16
)

func (m *Manager) acquireNativeQuery(ctx context.Context) (func(), error) {
	return acquireQuerySlot(ctx, m.nativeQueries, m.nativeQueryAdmissions)
}

func acquireQuerySlot(ctx context.Context, slots, admissions chan struct{}) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case admissions <- struct{}{}:
	default:
		return nil, fail("resource_limit", "native query queue is full")
	}
	select {
	case slots <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-slots
			<-admissions
			return nil, err
		}
		return func() { <-slots; <-admissions }, nil
	case <-ctx.Done():
		<-admissions
		return nil, ctx.Err()
	}
}

// Classify from cached schema before waiting. Recheck under the database read
// lock after admission so DDL cannot turn a point lookup into a scan.
func (m *Manager) admitNativeRead(ctx context.Context, d *database, op string, r Request) (func(), bool, error) {
	short := m.nativeShortQueries != nil && boundedNativeRead(d, op, r)
	var wait time.Duration
	if short {
		start := time.Now()
		done, err := acquireQuerySlot(ctx, m.nativeShortQueries, m.nativeShortAdmissions)
		wait += time.Since(start)
		if err != nil {
			return nil, false, err
		}
		if d.mu.TryRLock() {
			if boundedNativeRead(d, op, r) {
				r.diag.admission("interactive", wait)
				return done, true, nil
			}
			d.mu.RUnlock()
		}
		done()
	}
	start := time.Now()
	done, err := m.acquireNativeQuery(ctx)
	r.diag.admission("bulk", wait+time.Since(start))
	return done, false, err
}

// A complete non-null primary or unique key bounds candidates to one. Limit
// and index presence alone give no such guarantee. Keep complex reads in the
// bulk lane and cap the total point work in an interactive read batch.
func boundedNativeRead(d *database, op string, r Request) bool {
	if op == "batch" {
		if len(r.Operations) == 0 || len(r.Operations) > 8 {
			return false
		}
		for _, child := range r.Operations {
			if child.Op == "batch" || child.Request.TimeoutMS != 0 ||
				child.Request.Database != "" && child.Request.Database != r.Database ||
				!boundedNativeRead(d, child.Op, child.Request) {
				return false
			}
		}
		return true
	}
	if op != "find" && op != "count" && op != "get" {
		return false
	}
	c, ok := d.schemas.get(r.Collection)
	if !ok {
		return false
	}
	if op == "get" {
		return len(r.Key) == len(c.PrimaryKey)
	}
	if QueryKind(op, r) != op || len(r.Lookups) != 0 || len(r.Buckets) != 0 || r.Cursor != "" || r.Offset != 0 {
		return false
	}
	if r.Where == nil {
		return false
	}
	terms := r.Where.And
	if terms == nil {
		terms = []Filter{*r.Where}
	}
	if len(terms) == 0 || len(terms) > 16 {
		return false
	}
	equalities := make(map[string]bool, len(terms))
	for _, term := range terms {
		if term.And != nil || term.Or != nil || term.Field == "" || term.Op != "eq" || term.Value == nil || term.Exists != nil || term.NotExists != nil {
			return false
		}
		equalities[term.Field] = true
	}
	for _, index := range indexes(c) {
		if !index.Unique || len(index.Fields) == 0 {
			continue
		}
		full := true
		for _, field := range index.Fields {
			full = full && equalities[field.Field]
		}
		if full {
			return true
		}
	}
	return false
}
