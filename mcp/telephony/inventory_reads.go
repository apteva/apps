package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const inventoryCacheTTL = 25 * time.Second
const inventoryReadConcurrency = 32
const inventoryApplicationConcurrency = 4
const inventoryCacheBytes = 16 << 20

type inventoryReadResult struct {
	raw []byte
	err error
	at  time.Time
}
type inventoryReadFlight struct {
	done    chan struct{}
	cancel  context.CancelFunc
	waiters int
	result  inventoryReadResult
}

// Only carrier responses are cached. Credentials, bindings, local routes and
// admission/permission checks are resolved anew for every response. A changed
// route/binding/credential fingerprint cannot reuse a previous scope.
type inventoryReadCache struct {
	mu      sync.Mutex
	epoch   uint64
	entries map[string]inventoryReadResult
	flights map[string]*inventoryReadFlight
	slots   chan struct{}
	bytes   int
}

func (c *inventoryReadCache) invalidate() {
	c.mu.Lock()
	c.epoch++
	c.entries = nil
	c.bytes = 0
	// Detach flights, without interrupting their existing readers. A completion
	// from the previous epoch cannot refill the cache after a mutation.
	c.flights = nil
	c.mu.Unlock()
}

func (c *inventoryReadCache) read(request context.Context, key string, fresh bool, fetch func(context.Context) ([]byte, error), refreshAfter ...time.Time) inventoryReadResult {
	if err := request.Err(); err != nil {
		return inventoryReadResult{err: err}
	}
	c.mu.Lock()
	if c.slots == nil {
		c.slots = make(chan struct{}, inventoryReadConcurrency)
	}
	if c.entries == nil {
		c.entries = map[string]inventoryReadResult{}
	}
	if c.flights == nil {
		c.flights = map[string]*inventoryReadFlight{}
	}
	now := time.Now()
	v, ok := c.entries[key]
	verifiedDuringRequest := len(refreshAfter) > 0 && !v.at.Before(refreshAfter[0])
	if ok && (!fresh || verifiedDuringRequest) && now.Sub(v.at) < inventoryCacheTTL {
		c.mu.Unlock()
		v.raw = append([]byte(nil), v.raw...)
		return v
	}
	f := c.flights[key]
	if f == nil {
		// A shared fetch retains the initiating request's deadline; canceling one
		// reader does not cancel other readers, but the last reader cancels it.
		deadline, ok := request.Deadline()
		if !ok {
			deadline = now.Add(numberInventoryTimeout)
		}
		work, cancel := context.WithDeadline(context.WithoutCancel(request), deadline)
		f = &inventoryReadFlight{done: make(chan struct{}), cancel: cancel}
		c.flights[key] = f
		epoch, slots := c.epoch, c.slots
		go func() {
			defer cancel()
			var raw []byte
			var err error
			select {
			case slots <- struct{}{}:
				if err = work.Err(); err == nil {
					raw, err = fetch(work)
				}
				<-slots
			case <-work.Done():
				err = work.Err()
			}
			if err == nil {
				err = work.Err()
			}
			v := inventoryReadResult{raw: append([]byte(nil), raw...), err: err, at: time.Now().UTC()}
			c.mu.Lock()
			f.result = v
			if c.epoch == epoch && c.flights[key] == f {
				delete(c.flights, key)
				// A failed fresh verification must not expose a prior healthy result.
				if old, ok := c.entries[key]; ok {
					delete(c.entries, key)
					c.bytes -= len(old.raw)
				}
				if err == nil && len(raw) <= inventoryCacheBytes {
					for k, old := range c.entries {
						if v.at.Sub(old.at) >= inventoryCacheTTL {
							delete(c.entries, k)
							c.bytes -= len(old.raw)
						}
					}
					if len(c.entries) >= 1024 || c.bytes+len(raw) > inventoryCacheBytes {
						c.entries = map[string]inventoryReadResult{}
						c.bytes = 0
					}
					c.bytes -= len(c.entries[key].raw)
					c.entries[key] = v
					c.bytes += len(raw)
				}
			}
			close(f.done)
			c.mu.Unlock()
		}()
	}
	f.waiters++
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		f.waiters--
		if f.waiters == 0 {
			select {
			case <-f.done:
			default:
				if c.flights[key] == f {
					delete(c.flights, key)
				}
				f.cancel()
			}
		}
		c.mu.Unlock()
	}()
	select {
	case <-request.Done():
		return inventoryReadResult{err: request.Err()}
	case <-f.done:
		v := f.result
		v.raw = append([]byte(nil), v.raw...)
		return v
	}
}

type inventoryReadSession struct {
	app            *App
	ctx            *sdk.AppCtx
	request        context.Context
	provider       *numberProvider
	scope          string
	fresh          bool
	freshAfter     time.Time
	mu             sync.Mutex
	results        map[string]inventoryReadResult
	verifiedAt     time.Time
	publicKeyValid bool
}

func (s *inventoryReadSession) read(tool string, input map[string]any) ([]byte, error) {
	encoded, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	key := tool + ":" + string(encoded)
	s.mu.Lock()
	v, ok := s.results[key]
	s.mu.Unlock()
	if !ok {
		v = s.app.inventoryReads.read(s.request, s.scope+":"+key, s.fresh, func(work context.Context) ([]byte, error) {
			return executeCarrierTool(s.ctx, s.provider.ConnID, tool, input, work)
		}, s.freshAfter)
		s.mu.Lock()
		s.results[key] = v
		if v.err == nil && (s.verifiedAt.IsZero() || v.at.Before(s.verifiedAt)) {
			s.verifiedAt = v.at
		}
		s.mu.Unlock()
	}
	return v.raw, v.err
}

func (s *inventoryReadSession) prefetchApplications(ids []string) {
	// Custom non-context clients historically execute sequentially. Keep that
	// compatibility; the production SDK supports cancellation/concurrent reads.
	workers := inventoryApplicationConcurrency
	if !inventoryRequestsCancelable(s.ctx) {
		workers = 1
	}
	jobs := make(chan string)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for id := range jobs {
				_, _ = s.read("get_call_control_application", map[string]any{"id": id})
			}
		}()
	}
	for _, id := range ids {
		select {
		case jobs <- id:
		case <-s.request.Done():
			close(jobs)
			wg.Wait()
			return
		}
	}
	close(jobs)
	wg.Wait()
}

type inventoryFreshKey struct{}

func inventoryFreshContext(request context.Context, fresh bool) context.Context {
	if fresh {
		return context.WithValue(request, inventoryFreshKey{}, time.Now())
	}
	return request
}

func inventoryScope(install int64, project string, provider *numberProvider, routes []routeRow, bindings []*sdk.BoundIntegration) (string, error) {
	// SDK BoundIntegration contains a function, so fingerprint only its data.
	type bindingIdentity struct {
		ConnectionID int64
		InstallID    int64
		Slug         string
		Default      bool
	}
	identities := make([]bindingIdentity, 0, len(bindings))
	for _, b := range bindings {
		if b != nil {
			identities = append(identities, bindingIdentity{b.ConnectionID, b.InstallID, b.AppSlug, b.IsDefault})
		}
	}
	encoded, err := json.Marshal([]any{install, project, provider.ConnID, provider.Slug, provider.Fields, routes, identities})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(encoded)), nil
}
