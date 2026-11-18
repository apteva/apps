package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// boundProvider is one connection in the fabrication_provider role with
// the adapter that drives it.
type boundProvider struct {
	ConnectionID int64               `json:"connection_id"`
	Slug         string              `json:"slug"`
	IsDefault    bool                `json:"is_default"`
	Supported    bool                `json:"supported"`
	Caps         ProviderCaps        `json:"capabilities"`
	adapter      FabricationProvider `json:"-"`
}

// connectionsFor is replaced in tests.
var connectionsFor = func(ctx *sdk.AppCtx) []*sdk.BoundIntegration {
	if ctx == nil {
		return nil
	}
	return ctx.IntegrationsFor(fabricationRole)
}

func (s *Service) fabricationProviders() []boundProvider {
	out := []boundProvider{}
	for _, b := range connectionsFor(s.ctx) {
		if b == nil || b.ConnectionID <= 0 {
			continue
		}
		bp := boundProvider{ConnectionID: b.ConnectionID, Slug: b.AppSlug, IsDefault: b.IsDefault}
		if adapter, ok := providerFor(b.AppSlug); ok {
			bp.Supported, bp.adapter, bp.Caps = true, adapter, adapter.Caps()
		}
		out = append(out, bp)
	}
	return out
}

func (s *Service) providerByConnection(connID int64) (boundProvider, error) {
	for _, p := range s.fabricationProviders() {
		if p.ConnectionID == connID {
			if !p.Supported {
				return p, fmt.Errorf("connection %d (%s) has no fabrication adapter", connID, p.Slug)
			}
			return p, nil
		}
	}
	return boundProvider{}, fmt.Errorf("connection %d is not bound to Design Studio's fabrication_provider role", connID)
}

// executeTool is replaced in tests.
var executeTool = func(ctx context.Context, app *sdk.AppCtx, connID int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
	if app == nil || app.PlatformAPI() == nil {
		return nil, errors.New("platform API unavailable")
	}
	return sdk.ExecuteIntegrationToolContext(ctx, app.PlatformAPI(), connID, tool, input)
}

func (s *Service) caller(connID int64) ToolCaller {
	return func(ctx context.Context, tool string, input map[string]any) (json.RawMessage, int, error) {
		res, err := executeTool(ctx, s.ctx, connID, tool, input)
		if err != nil {
			return nil, 0, fmt.Errorf("%s: %w", tool, err)
		}
		if res == nil {
			return nil, 0, fmt.Errorf("%s: empty response", tool)
		}
		if !res.Success || res.Status >= 300 {
			detail := strings.TrimSpace(string(res.Data))
			if len(detail) > 300 {
				detail = detail[:300] + "…"
			}
			return res.Data, res.Status, fmt.Errorf("%s returned HTTP %d: %s", tool, res.Status, detail)
		}
		return res.Data, res.Status, nil
	}
}

type QuoteArgs struct {
	DesignID      int64
	RevisionID    int64
	PartIDs       []string
	Quantity      int
	Quantities    map[string]int
	ConnectionIDs []int64
	Currency      string
	Country       string
	Material      string
	MaxOptions    int
}

// printableParts builds (or reuses) the revision's STL exports and returns
// the printed parts. Quoting refuses a revision whose build failed.
func (s *Service) printableParts(ctx context.Context, designID, revisionID int64, partIDs []string, qty int, quantities map[string]int) (int64, []PartFile, error) {
	result, err := s.Build(ctx, designID, revisionID, []string{"stl"})
	if err != nil {
		return 0, nil, fmt.Errorf("build before quoting: %w", err)
	}
	if result.Run.Status == "failed" {
		return 0, nil, errors.New("the revision failed validation; fix it before quoting")
	}
	want := map[string]bool{}
	for _, id := range partIDs {
		want[id] = true
	}
	var parts []PartFile
	var whole *Artifact
	for i := range result.Artifacts {
		a := result.Artifacts[i]
		if a.Format != "stl" {
			continue
		}
		if a.Kind == "export" {
			whole = &a
			continue
		}
		if a.Kind != "part-export" {
			continue
		}
		var meta struct {
			PartID        string `json:"part_id"`
			PartName      string `json:"part_name"`
			Manufacturing *struct {
				Classification string `json:"classification"`
			} `json:"manufacturing"`
		}
		_ = json.Unmarshal(a.Metadata, &meta)
		if meta.Manufacturing != nil && meta.Manufacturing.Classification != "" && meta.Manufacturing.Classification != "printed" {
			continue
		}
		if len(want) > 0 && !want[meta.PartID] {
			continue
		}
		parts = append(parts, PartFile{PartID: meta.PartID, Name: ternary(meta.PartName == "", meta.PartID, meta.PartName), Filename: a.Name, SHA256: a.SHA256, Quantity: quantityFor(meta.PartID, qty, quantities), Body: nil})
		parts[len(parts)-1].Body, err = os.ReadFile(a.LocalPath)
		if err != nil {
			return 0, nil, fmt.Errorf("read %s: %w", a.Name, err)
		}
	}
	if len(parts) == 0 && whole != nil && len(want) == 0 {
		body, err := os.ReadFile(whole.LocalPath)
		if err != nil {
			return 0, nil, fmt.Errorf("read %s: %w", whole.Name, err)
		}
		parts = append(parts, PartFile{PartID: "design", Name: "Design", Filename: whole.Name, SHA256: whole.SHA256, Quantity: quantityFor("design", qty, quantities), Body: body})
	}
	if len(parts) == 0 {
		return 0, nil, errors.New("no printable parts to quote; check part_ids and that parts are classified as printed")
	}
	for _, p := range parts {
		if len(p.Body) > 30<<20 {
			return 0, nil, fmt.Errorf("%s is larger than 30 MB", p.Filename)
		}
	}
	return result.Run.RevisionID, parts, nil
}

func quantityFor(partID string, qty int, quantities map[string]int) int {
	if n, ok := quantities[partID]; ok && n > 0 {
		return n
	}
	if qty > 0 {
		return qty
	}
	return 1
}

func (s *Service) CreateQuote(ctx context.Context, args QuoteArgs) (*FabricationQuote, error) {
	settings, err := s.store.GetFabricationSettings(s.project)
	if err != nil {
		return nil, err
	}
	providers := s.fabricationProviders()
	if len(providers) == 0 {
		return nil, errors.New("no print service is bound; bind Craftcloud, Slant 3D, Shapeways, Treatstock or Sculpteo to Design Studio's fabrication_provider role")
	}
	selected := []boundProvider{}
	for _, p := range providers {
		if len(args.ConnectionIDs) > 0 && !containsInt64(args.ConnectionIDs, p.ConnectionID) {
			continue
		}
		selected = append(selected, p)
	}
	if len(selected) == 0 {
		return nil, errors.New("none of the requested connections is bound")
	}
	revisionID, parts, err := s.printableParts(ctx, args.DesignID, args.RevisionID, args.PartIDs, args.Quantity, args.Quantities)
	if err != nil {
		return nil, err
	}
	currency := strings.ToUpper(strings.TrimSpace(args.Currency))
	if currency == "" {
		currency = settings.Currency
	}
	country := strings.ToUpper(strings.TrimSpace(args.Country))
	if country == "" {
		country = settings.Country
	}
	if args.MaxOptions <= 0 || args.MaxOptions > 50 {
		args.MaxOptions = 12
	}
	type result struct {
		p       boundProvider
		options []QuoteOption
		err     error
	}
	results := make([]result, len(selected))
	var wg sync.WaitGroup
	quoteCtx, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()
	for i, p := range selected {
		wg.Add(1)
		go func(i int, p boundProvider) {
			defer wg.Done()
			if !p.Supported {
				results[i] = result{p: p, err: fmt.Errorf("no adapter for %q", p.Slug)}
				return
			}
			req := QuoteRequest{DesignID: args.DesignID, RevisionID: revisionID, Parts: parts, Currency: currency, Country: country,
				Material: args.Material, MaxOptions: args.MaxOptions, Settings: settings.ProviderSettings[p.Slug]}
			options, err := p.adapter.Quote(quoteCtx, s.caller(p.ConnectionID), req)
			results[i] = result{p: p, options: options, err: err}
		}(i, p)
	}
	wg.Wait()
	var all []QuoteOption
	var errs []ProviderError
	for _, r := range results {
		if r.err != nil {
			errs = append(errs, ProviderError{Provider: r.p.Slug, ConnectionID: r.p.ConnectionID, Error: r.err.Error()})
			continue
		}
		for i := range r.options {
			r.options[i].ConnectionID = r.p.ConnectionID
			r.options[i].Provider = r.p.Slug
		}
		all = append(all, rankOptions(r.options, args.MaxOptions)...)
	}
	all = rankOptions(all, args.MaxOptions*len(selected))
	expires := ""
	for i := range all {
		all[i].ID = fmt.Sprintf("o%d", i+1)
		if all[i].ExpiresAt != "" && (expires == "" || all[i].ExpiresAt < expires) {
			expires = all[i].ExpiresAt
		}
	}
	partSummary := make([]map[string]any, 0, len(parts))
	for _, p := range parts {
		partSummary = append(partSummary, map[string]any{"part_id": p.PartID, "filename": p.Filename, "sha256": p.SHA256, "quantity": p.Quantity, "bytes": len(p.Body)})
	}
	request, _ := json.Marshal(map[string]any{"currency": currency, "country": country, "material": args.Material, "parts": partSummary})
	quote, err := s.store.SaveFabricationQuote(s.project, FabricationQuote{DesignID: args.DesignID, RevisionID: revisionID, Request: request, Options: all, Errors: errs, ExpiresAt: expires})
	if err != nil {
		return nil, err
	}
	if s.ctx != nil {
		s.ctx.Emit("design.fabrication.quoted", map[string]any{"design_id": args.DesignID, "revision_id": revisionID, "quote_id": quote.ID, "options": len(all), "errors": len(errs)})
	}
	return quote, nil
}

func containsInt64(list []int64, v int64) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func newIdempotencyKey() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// PrepareOrder turns one quote option into a draft. Nothing is charged.
func (s *Service) PrepareOrder(ctx context.Context, quoteID int64, optionID, addressID, createdBy string) (*FabricationOrder, error) {
	quote, err := s.store.GetFabricationQuote(s.project, quoteID)
	if err != nil {
		return nil, err
	}
	var opt *QuoteOption
	for i := range quote.Options {
		if quote.Options[i].ID == optionID {
			opt = &quote.Options[i]
		}
	}
	if opt == nil {
		return nil, fmt.Errorf("quote %d has no option %q", quoteID, optionID)
	}
	if opt.ExpiresAt != "" {
		if t, err := time.Parse(time.RFC3339, opt.ExpiresAt); err == nil && time.Now().After(t) {
			return nil, errors.New("this quote has expired; request a new quote")
		}
	}
	design, err := s.store.GetDesign(s.project, quote.DesignID)
	if err != nil {
		return nil, err
	}
	if design.CurrentRevisionID != quote.RevisionID {
		return nil, fmt.Errorf("the design moved to revision %d after this quote (revision %d); quote again", design.CurrentRevisionID, quote.RevisionID)
	}
	settings, err := s.store.GetFabricationSettings(s.project)
	if err != nil {
		return nil, err
	}
	addr, ok := settings.address(addressID)
	if !ok {
		return nil, errors.New("no saved ship-to address; add one in the Fabrication settings of the Design panel")
	}
	p, err := s.providerByConnection(opt.ConnectionID)
	if err != nil {
		return nil, err
	}
	draft, err := p.adapter.PrepareOrder(ctx, s.caller(opt.ConnectionID), *opt, addr, settings.ProviderSettings[p.Slug])
	if err != nil {
		return nil, err
	}
	if draft.Currency == "" {
		draft.Currency = opt.Currency
	}
	spent, err := s.store.MonthSpend(s.project, draft.Currency, monthStart(time.Now()))
	if err != nil {
		return nil, err
	}
	verdict := evaluatePolicy(settings, p.Slug, draft.Currency, opt.Total, draft.Total, spent)
	status := ternary(verdict.AutoAllowed, orderApproved, orderAwaitingApproval)
	order, err := s.store.CreateFabricationOrder(s.project, FabricationOrder{DesignID: quote.DesignID, RevisionID: quote.RevisionID, QuoteID: quote.ID,
		OptionID: opt.ID, Provider: p.Slug, ConnectionID: opt.ConnectionID, Status: status, IdempotencyKey: newIdempotencyKey(),
		Currency: draft.Currency, QuotedTotal: opt.Total, DraftTotal: draft.Total, AddressID: addr.ID, Option: *opt, Draft: draft, Policy: verdict, CreatedBy: createdBy})
	if err != nil {
		return nil, err
	}
	if verdict.AutoAllowed {
		_ = s.store.RecordApproval(s.project, order.ID, map[string]any{"by": "policy", "at": time.Now().UTC().Format(time.RFC3339), "total": draft.Total, "currency": draft.Currency}, orderApproved)
	}
	if s.ctx != nil {
		s.ctx.Emit("design.fabrication.order_prepared", map[string]any{"order_id": order.ID, "design_id": order.DesignID, "status": status, "total": draft.Total, "currency": draft.Currency})
	}
	return s.store.GetFabricationOrder(s.project, order.ID)
}

// Approve records a person's approval. confirmTotal must equal the draft
// total the person saw, so an approval can never cover a different price.
func (s *Service) Approve(orderID int64, confirmTotal float64, approver map[string]any) (*FabricationOrder, error) {
	order, err := s.store.GetFabricationOrder(s.project, orderID)
	if err != nil {
		return nil, err
	}
	if order.Status != orderAwaitingApproval {
		return nil, fmt.Errorf("order %d is %s, not awaiting approval", orderID, order.Status)
	}
	if round2(confirmTotal) != round2(order.DraftTotal) {
		return nil, fmt.Errorf("confirm_total %.2f does not match the draft total %.2f", confirmTotal, order.DraftTotal)
	}
	approval := map[string]any{"at": time.Now().UTC().Format(time.RFC3339), "total": order.DraftTotal, "currency": order.Currency, "via": "panel"}
	for k, v := range approver {
		approval[k] = v
	}
	ok, err := s.store.TransitionOrder(s.project, orderID, []string{orderAwaitingApproval}, orderApproved)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errors.New("order changed while approving; reload")
	}
	if err := s.store.RecordApproval(s.project, orderID, approval, orderApproved); err != nil {
		return nil, err
	}
	return s.store.GetFabricationOrder(s.project, orderID)
}

func (s *Service) Decline(orderID int64, by map[string]any) (*FabricationOrder, error) {
	ok, err := s.store.TransitionOrder(s.project, orderID, []string{orderAwaitingApproval, orderApproved, orderDraft}, orderDeclined)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errors.New("only drafts that were not submitted can be declined")
	}
	approval := map[string]any{"declined_at": time.Now().UTC().Format(time.RFC3339)}
	for k, v := range by {
		approval[k] = v
	}
	if err := s.store.RecordApproval(s.project, orderID, approval, orderDeclined); err != nil {
		return nil, err
	}
	return s.store.GetFabricationOrder(s.project, orderID)
}

// Submit is the only billable step. It runs at most once per order: the
// approved → submitting transition is atomic, and an order whose provider
// call failed in an unknown way is parked for a person to check.
func (s *Service) Submit(ctx context.Context, orderID int64) (*FabricationOrder, error) {
	order, err := s.store.GetFabricationOrder(s.project, orderID)
	if err != nil {
		return nil, err
	}
	switch order.Status {
	case orderApproved:
	case orderAwaitingApproval:
		return nil, errors.New("this order is waiting for a person to approve it in the Design panel")
	default:
		return order, fmt.Errorf("order %d is already %s", orderID, order.Status)
	}
	// Re-check the policy for orders approved by policy: spending may have
	// moved since the draft was prepared.
	settings, err := s.store.GetFabricationSettings(s.project)
	if err != nil {
		return nil, err
	}
	if pickString(decodeAny(order.Approval), "by") == "policy" {
		spent, err := s.store.MonthSpend(s.project, order.Currency, monthStart(time.Now()))
		if err != nil {
			return nil, err
		}
		verdict := evaluatePolicy(settings, order.Provider, order.Currency, order.QuotedTotal, order.DraftTotal, spent)
		if !verdict.AutoAllowed {
			_ = s.store.UpdateOrderDraft(s.project, orderID, orderAwaitingApproval, order.Draft, verdict)
			return s.store.GetFabricationOrder(s.project, orderID)
		}
	}
	addr, ok := settings.address(order.AddressID)
	if !ok {
		return nil, errors.New("the ship-to address of this order was removed from settings")
	}
	p, err := s.providerByConnection(order.ConnectionID)
	if err != nil {
		return nil, err
	}
	moved, err := s.store.TransitionOrder(s.project, orderID, []string{orderApproved}, orderSubmitting)
	if err != nil {
		return nil, err
	}
	if !moved {
		return s.store.GetFabricationOrder(s.project, orderID)
	}
	ref, submitErr := p.adapter.Submit(ctx, s.caller(order.ConnectionID), order.Option, order.Draft, addr, settings.ProviderSettings[p.Slug], order.IdempotencyKey)
	if submitErr != nil {
		// The provider may or may not have accepted the order. Never retry
		// automatically; a person checks the provider account.
		_ = s.store.RecordSubmit(s.project, orderID, orderUnknown, OrderRef{}, submitErr.Error())
		return s.store.GetFabricationOrder(s.project, orderID)
	}
	if ref.Status == "" {
		ref.Status = orderSubmitted
	}
	if err := s.store.RecordSubmit(s.project, orderID, ref.Status, ref, ""); err != nil {
		return nil, err
	}
	if s.ctx != nil {
		s.ctx.Emit("design.fabrication.order_submitted", map[string]any{"order_id": orderID, "provider": p.Slug, "provider_order_id": ref.ProviderOrderID, "status": ref.Status, "total": order.DraftTotal, "currency": order.Currency})
	}
	return s.store.GetFabricationOrder(s.project, orderID)
}

func (s *Service) RefreshOrder(ctx context.Context, orderID int64) (*FabricationOrder, error) {
	order, err := s.store.GetFabricationOrder(s.project, orderID)
	if err != nil {
		return nil, err
	}
	if order.ProviderOrderID == "" || order.Status == orderDelivered || order.Status == orderCancelled {
		return order, nil
	}
	p, err := s.providerByConnection(order.ConnectionID)
	if err != nil {
		return order, err
	}
	st, err := p.adapter.Status(ctx, s.caller(order.ConnectionID), order.ProviderOrderID)
	if errors.Is(err, errNotSupported) {
		return order, nil
	}
	if err != nil {
		return order, err
	}
	if st.Status == "" {
		st.Status = order.Status
	}
	if err := s.store.RecordStatus(s.project, orderID, st); err != nil {
		return nil, err
	}
	return s.store.GetFabricationOrder(s.project, orderID)
}

func (s *Service) CancelOrder(ctx context.Context, orderID int64) (*FabricationOrder, error) {
	order, err := s.store.GetFabricationOrder(s.project, orderID)
	if err != nil {
		return nil, err
	}
	if order.ProviderOrderID == "" {
		return s.Decline(orderID, map[string]any{"via": "cancel"})
	}
	p, err := s.providerByConnection(order.ConnectionID)
	if err != nil {
		return nil, err
	}
	if err := p.adapter.Cancel(ctx, s.caller(order.ConnectionID), order.ProviderOrderID); err != nil {
		if errors.Is(err, errNotSupported) {
			return nil, fmt.Errorf("%s orders cannot be cancelled through its API; contact the provider", p.Slug)
		}
		return nil, err
	}
	if err := s.store.RecordStatus(s.project, orderID, OrderStatus{Status: orderCancelled, ProviderStatus: "cancelled"}); err != nil {
		return nil, err
	}
	return s.store.GetFabricationOrder(s.project, orderID)
}

// refreshActiveOrders is the worker body: poll every open order once.
func (a *App) refreshActiveOrders(ctx context.Context, app *sdk.AppCtx) error {
	if a.store == nil {
		return nil
	}
	projects, err := a.store.ActiveOrderProjects()
	if err != nil {
		return err
	}
	for _, project := range projects {
		service, err := a.service(app.WithProject(project))
		if err != nil {
			continue
		}
		orders, err := service.store.ListFabricationOrders(project, 0, []string{orderAwaitingPayment, orderSubmitted, orderInProduction, orderShipped}, 100)
		if err != nil {
			continue
		}
		for _, o := range orders {
			if _, err := service.RefreshOrder(ctx, o.ID); err != nil {
				app.Logger().Warn("fabrication status refresh failed", "order", o.ID, "provider", o.Provider, "err", err)
			}
		}
	}
	return nil
}
