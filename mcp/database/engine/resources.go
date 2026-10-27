package engine

import "context"

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

const maxQueuedNativeQueries = 64

func (m *Manager) acquireNativeQuery(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case m.nativeQueryAdmissions <- struct{}{}:
	default:
		return nil, fail("resource_limit", "native query queue is full")
	}
	select {
	case m.nativeQueries <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-m.nativeQueries
			<-m.nativeQueryAdmissions
			return nil, err
		}
		return func() { <-m.nativeQueries; <-m.nativeQueryAdmissions }, nil
	case <-ctx.Done():
		<-m.nativeQueryAdmissions
		return nil, ctx.Err()
	}
}
