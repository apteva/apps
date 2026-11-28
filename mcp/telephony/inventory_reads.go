package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const inventoryCacheTTL = 25 * time.Second
const inventoryReadConcurrency = 32
const inventoryApplicationConcurrency = 4
const inventoryCacheBytes = 16 << 20

// One list page normally holds every Call Control application of an account.
const inventoryApplicationPageSize = 100
const maxApplicationListPages = 10

// A carrier 429 pauses the same inventory read for its Retry-After, capped,
// so repeated views do not keep the account rate-limited.
const inventoryRateLimitPause = 5 * time.Second
const maxInventoryRateLimitPause = time.Minute

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
	// limited holds rate-limited reads; at is when the pause ends.
	limited map[string]inventoryReadResult
	slots   chan struct{}
	bytes   int
}

func inventoryRateLimitUntil(err error, now time.Time) (time.Time, bool) {
	var carrier *carrierHTTPError
	if !errors.As(err, &carrier) || carrier.status != 429 {
		return time.Time{}, false
	}
	pause := carrier.retryAfter
	if pause <= 0 {
		pause = inventoryRateLimitPause
	}
	return now.Add(min(pause, maxInventoryRateLimitPause)), true
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
	if limited, ok := c.limited[key]; ok {
		if now.Before(limited.at) {
			c.mu.Unlock()
			return inventoryReadResult{err: limited.err}
		}
		delete(c.limited, key)
	}
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
			// Telnyx limits the account, so a pause outlives cache invalidation.
			if until, ok := inventoryRateLimitUntil(err, time.Now()); ok {
				if c.limited == nil {
					c.limited = map[string]inventoryReadResult{}
				}
				for k, old := range c.limited {
					if !old.at.After(v.at) {
						delete(c.limited, k)
					}
				}
				if len(c.limited) < 1024 {
					c.limited[key] = inventoryReadResult{err: err, at: until}
				}
			}
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
	v := s.readResult(tool, input)
	return v.raw, v.err
}

func (s *inventoryReadSession) readResult(tool string, input map[string]any) inventoryReadResult {
	encoded, err := json.Marshal(input)
	if err != nil {
		return inventoryReadResult{err: err}
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
	return v
}

func applicationReadKey(id string) string {
	encoded, _ := json.Marshal(map[string]any{"id": id})
	return "get_call_control_application:" + string(encoded)
}

// seed answers a later read of key in this session without a carrier request.
func (s *inventoryReadSession) seed(key string, v inventoryReadResult) {
	s.mu.Lock()
	if _, ok := s.results[key]; !ok {
		s.results[key] = v
	}
	s.mu.Unlock()
}

func telnyxApplicationNotFound(id string) error {
	return &carrierHTTPError{status: 404, message: fmt.Sprintf("Telnyx Call Control application %s was not found", id)}
}

// prefetchApplications answers this session's Call Control application reads
// from the account's application list: one request for most accounts, where
// reading each application separately gets rate-limited. An application
// missing from the complete list does not exist. Only a cut-short or unusable
// list falls back to reading the missing applications one by one.
func (s *inventoryReadSession) prefetchApplications(ids []string) {
	missing := make(map[string]bool, len(ids))
	for _, id := range ids {
		missing[id] = true
	}
	if len(missing) == 0 {
		return
	}
	complete := false
	var failure error
	var listedAt time.Time
	for page := 1; page <= maxApplicationListPages && len(missing) > 0; page++ {
		v := s.readResult("list_call_control_applications", map[string]any{"page[number]": page, "page[size]": inventoryApplicationPageSize})
		if v.err != nil {
			failure = v.err
			break
		}
		var body struct {
			Data *[]json.RawMessage `json:"data"`
			Meta struct {
				TotalPages int `json:"total_pages"`
			} `json:"meta"`
		}
		if json.Unmarshal(v.raw, &body) != nil || body.Data == nil {
			failure = errors.New("list_call_control_applications returned no application list")
			break
		}
		listedAt = v.at
		for _, item := range *body.Data {
			var application map[string]any
			if json.Unmarshal(item, &application) != nil {
				continue
			}
			if id := stringValue(application["id"]); missing[id] {
				delete(missing, id)
				raw := make([]byte, 0, len(item)+9)
				raw = append(append(append(raw, `{"data":`...), item...), '}')
				s.seed(applicationReadKey(id), inventoryReadResult{raw: raw, at: v.at})
			}
		}
		if len(*body.Data) < inventoryApplicationPageSize || (body.Meta.TotalPages > 0 && page >= body.Meta.TotalPages) {
			complete = true
			break
		}
	}
	if len(missing) == 0 || s.request.Err() != nil {
		return
	}
	pending := make([]string, 0, len(missing))
	for _, id := range ids {
		if missing[id] {
			pending = append(pending, id)
			delete(missing, id)
		}
	}
	switch {
	case complete:
		for _, id := range pending {
			s.seed(applicationReadKey(id), inventoryReadResult{err: telnyxApplicationNotFound(id), at: listedAt})
		}
	case temporaryCarrierVerificationError(failure):
		// Separate reads would meet the same limit or outage; previously
		// verified results cover it.
		for _, id := range pending {
			s.seed(applicationReadKey(id), inventoryReadResult{err: failure})
		}
	default:
		s.readApplications(pending)
	}
}

func (s *inventoryReadSession) readApplications(ids []string) {
	// Custom non-context clients historically execute sequentially. Keep that
	// compatibility; the production SDK supports cancellation/concurrent reads.
	workers := inventoryApplicationConcurrency
	if !inventoryRequestsCancelable(s.ctx) {
		workers = 1
	}
	// After a 429, the remaining applications report it instead of adding
	// requests to a rate-limited account.
	var limitedMu sync.Mutex
	var limited error
	jobs := make(chan string)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for id := range jobs {
				limitedMu.Lock()
				err := limited
				limitedMu.Unlock()
				if err != nil {
					s.seed(applicationReadKey(id), inventoryReadResult{err: err})
					continue
				}
				if _, err = s.read("get_call_control_application", map[string]any{"id": id}); carrierErrorStatus(err) == 429 {
					limitedMu.Lock()
					if limited == nil {
						limited = err
					}
					limitedMu.Unlock()
				}
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
