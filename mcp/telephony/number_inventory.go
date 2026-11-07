package main

import (
	"context"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const numberInventoryTimeout = 8 * time.Second

type accountNumberInventory struct {
	provider *numberProvider
	result   map[string]any
	err      error
}

// Carrier reads have one bounded request budget. Each authorized account starts
// independently so stalled accounts cannot monopolize a shared worker pool.
// Result order stays binding order so defaults and duplicate-number ordering
// are stable. Late results never mutate the returned slice.
func (a *App) accountNumberInventories(ctx *sdk.AppCtx, bindings []*sdk.BoundIntegration, request context.Context) []accountNumberInventory {
	results := make([]accountNumberInventory, len(bindings))
	fetch := func(i int) accountNumberInventory {
		if err := request.Err(); err != nil {
			return accountNumberInventory{err: err}
		}
		provider, err := a.numberProviderForBinding(ctx, bindings[i])
		if err != nil {
			return accountNumberInventory{provider: provider, err: err}
		}
		// Credential reads do not expose cancellation in the current SDK. If
		// one finishes after the budget, do not start further carrier reads.
		if err := request.Err(); err != nil {
			return accountNumberInventory{provider: provider, err: err}
		}
		provider.inventoryContext = request
		result, err := a.connectedNumbersForProvider(ctx, provider)
		return accountNumberInventory{provider: provider, result: result, err: err}
	}
	// Preserve compatibility with simple/custom PlatformClient implementations
	// that cannot cancel requests; the production SDK supports deadlines.
	if !inventoryRequestsCancelable(ctx) {
		for i, b := range bindings {
			if b != nil {
				results[i] = fetch(i)
			}
		}
		return results
	}
	type completedInventory struct {
		index     int
		inventory accountNumberInventory
	}
	completed := make(chan completedInventory, len(bindings))
	pending := make(map[int]bool, len(bindings))
	for i, b := range bindings {
		if b == nil {
			continue
		}
		pending[i] = true
		go func(i int) {
			completed <- completedInventory{i, fetch(i)}
		}(i)
	}
	accept := func(done completedInventory) {
		results[done.index] = done.inventory
		delete(pending, done.index)
	}
	for len(pending) > 0 {
		select {
		case done := <-completed:
			accept(done)
		case <-request.Done():
			// Preserve results already completed when the deadline raced with
			// collection. Non-cancellable credential requests can finish later;
			// the buffered channel lets them exit without delaying this caller.
			for {
				select {
				case done := <-completed:
					accept(done)
				default:
					for i := range pending {
						results[i].err = request.Err()
					}
					return results
				}
			}
		}
	}
	return results
}

func executeNumberInventoryTool(ctx *sdk.AppCtx, p *numberProvider, tool string, input map[string]any) ([]byte, error) {
	if p.inventoryReads != nil {
		return p.inventoryReads.read(tool, input)
	}
	return executeCarrierTool(ctx, p.ConnID, tool, input, p.inventoryContext)
}

// Successful inventory is only a selection hint. Always recheck binding,
// credentials, current ownership, readiness and admission before placing a call.
// Hints let a selected healthy caller ID avoid querying an unrelated stalled
// account first. They never grant ownership or authorize a call.
type outboundInventoryHints struct {
	mu       sync.Mutex
	projects map[string]outboundProjectHints
}
type outboundProjectHints struct {
	expires time.Time
	owners  map[string]map[int64]bool
}

func (h *outboundInventoryHints) remember(project string, numbers []connectedNumberView) {
	owners := map[string]map[int64]bool{}
	for _, n := range numbers {
		if !validE164(n.PhoneNumber) || n.CarrierStatus == "not_found" {
			continue
		}
		if owners[n.PhoneNumber] == nil {
			owners[n.PhoneNumber] = map[int64]bool{}
		}
		owners[n.PhoneNumber][n.CarrierConnectionID] = true
		if len(owners) >= 8192 {
			break
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.projects == nil || len(h.projects) >= 128 {
		h.projects = map[string]outboundProjectHints{}
	}
	if len(owners) > 0 {
		h.projects[project] = outboundProjectHints{time.Now().Add(time.Minute), owners}
	}
}
func (h *outboundInventoryHints) candidates(project, number string, bindings []*sdk.BoundIntegration) []*sdk.BoundIntegration {
	h.mu.Lock()
	defer h.mu.Unlock()
	hints := h.projects[project]
	if time.Now().After(hints.expires) {
		return bindings
	}
	owners := hints.owners[number]
	if len(owners) == 0 {
		return bindings
	}
	out := make([]*sdk.BoundIntegration, 0, len(bindings))
	for _, b := range bindings {
		if b != nil && owners[b.ConnectionID] {
			out = append(out, b)
		}
	}
	for _, b := range bindings {
		if b != nil && !owners[b.ConnectionID] {
			out = append(out, b)
		}
	}
	return out
}

// A project wrapper exposes optional methods even when its underlying custom
// client does not. Check the mounted client for actual cancellation support.
func inventoryRequestsCancelable(ctx *sdk.AppCtx) bool {
	client := ctx.PlatformAPI()
	if globalCtx != nil {
		client = globalCtx.PlatformAPI()
	}
	_, ok := client.(sdk.IntegrationContextClient)
	return ok
}
