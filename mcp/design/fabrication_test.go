package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// fakeProvider records Submit calls so tests can prove orders are placed once.
type fakeProvider struct {
	draftTotal float64
	submits    *int32
	submitErr  error
}

func (f fakeProvider) Caps() ProviderCaps { return ProviderCaps{Slug: "fake", Name: "Fake"} }
func (f fakeProvider) Quote(context.Context, ToolCaller, QuoteRequest) ([]QuoteOption, error) {
	return nil, nil
}
func (f fakeProvider) PrepareOrder(context.Context, ToolCaller, QuoteOption, Address, map[string]any) (Draft, error) {
	return Draft{Total: f.draftTotal, Currency: "EUR"}, nil
}
func (f fakeProvider) Submit(context.Context, ToolCaller, QuoteOption, Draft, Address, map[string]any, string) (OrderRef, error) {
	atomic.AddInt32(f.submits, 1)
	if f.submitErr != nil {
		return OrderRef{}, f.submitErr
	}
	return OrderRef{ProviderOrderID: "P-1", Status: orderSubmitted}, nil
}
func (f fakeProvider) Status(context.Context, ToolCaller, string) (OrderStatus, error) {
	return OrderStatus{}, errNotSupported
}
func (f fakeProvider) Cancel(context.Context, ToolCaller, string) error { return errNotSupported }

type fabricationFixture struct {
	service *Service
	quote   *FabricationQuote
	submits *int32
}

func newFabricationFixture(t *testing.T, settings FabricationSettings, quoted, draft float64, submitErr error) fabricationFixture {
	t.Helper()
	store := testStore(t)
	canonical, definition, err := normalizeDefinition(testDefinition(), 256)
	if err != nil {
		t.Fatal(err)
	}
	parameters, err := normalizeParameters([]byte(`{}`), definition)
	if err != nil {
		t.Fatal(err)
	}
	design, err := store.CreateDesign("p", CreateDesignInput{Name: "Dock", Definition: canonical, Parameters: parameters})
	if err != nil {
		t.Fatal(err)
	}
	if err := settings.normalize(); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveFabricationSettings("p", settings, "test"); err != nil {
		t.Fatal(err)
	}
	quote, err := store.SaveFabricationQuote("p", FabricationQuote{DesignID: design.ID, RevisionID: design.CurrentRevisionID,
		Request: json.RawMessage(`{}`), Options: []QuoteOption{{ID: "o1", Provider: "fake", ConnectionID: 7, Material: "PLA", Total: quoted, Currency: "EUR",
			ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}}})
	if err != nil {
		t.Fatal(err)
	}
	var submits int32
	prevProviders, prevConns := fabricationProviders["fake"], connectionsFor
	fabricationProviders["fake"] = fakeProvider{draftTotal: draft, submits: &submits, submitErr: submitErr}
	connectionsFor = func(*sdk.AppCtx) []*sdk.BoundIntegration {
		return []*sdk.BoundIntegration{{ConnectionID: 7, AppSlug: "fake"}}
	}
	t.Cleanup(func() {
		connectionsFor = prevConns
		if prevProviders == nil {
			delete(fabricationProviders, "fake")
		} else {
			fabricationProviders["fake"] = prevProviders
		}
	})
	return fabricationFixture{service: &Service{store: store, project: "p"}, quote: quote, submits: &submits}
}

func homeSettings(auto bool) FabricationSettings {
	return FabricationSettings{AutoOrder: auto, MaxOrderTotal: ternary(auto, 100.0, 0), MonthlyBudget: ternary(auto, 300.0, 0), Currency: "EUR", Country: "FR",
		Addresses: []Address{{ID: "lab", Label: "Lab", FirstName: "A", LastName: "B", Line1: "1 rue", City: "Paris", Zip: "75001", Country: "FR", Phone: "+33100000000", Email: "lab@example.com"}}}
}

func TestEvaluatePolicy(t *testing.T) {
	s := homeSettings(true)
	if v := evaluatePolicy(s, "craftcloud", "EUR", 40, 41, 100); !v.AutoAllowed {
		t.Fatalf("expected within policy, got %v", v.Reasons)
	}
	cases := map[string]PolicyVerdict{
		"per-order limit":    evaluatePolicy(s, "craftcloud", "EUR", 120, 120, 0),
		"monthly budget":     evaluatePolicy(s, "craftcloud", "EUR", 90, 90, 250),
		"currency":           evaluatePolicy(s, "craftcloud", "USD", 40, 40, 0),
		"price drift":        evaluatePolicy(s, "craftcloud", "EUR", 40, 43, 0),
		"auto-order off":     evaluatePolicy(homeSettings(false), "craftcloud", "EUR", 1, 1, 0),
		"provider not allow": evaluatePolicy(FabricationSettings{AutoOrder: true, MaxOrderTotal: 100, MonthlyBudget: 300, Currency: "EUR", AllowedProviders: []string{"slant3d"}}, "craftcloud", "EUR", 1, 1, 0),
	}
	for name, v := range cases {
		if v.AutoAllowed || len(v.Reasons) == 0 {
			t.Errorf("%s: expected approval to be required, got %+v", name, v)
		}
	}
}

func TestSettingsRequireLimitsForAutoOrder(t *testing.T) {
	s := FabricationSettings{AutoOrder: true}
	if err := s.normalize(); err == nil {
		t.Fatal("auto_order without limits must be rejected")
	}
}

func TestCraftcloudOptionsRequireEveryPartAndAddShipping(t *testing.T) {
	prices := decodeAny(json.RawMessage(`{
		"expiresAt": 1893456000000, "allComplete": true,
		"quotes": [
			{"quoteId":"q1","vendorId":"shopA","modelId":"m1","materialConfigId":"pla","priceInclVat":10,"productionTimeSlow":4},
			{"quoteId":"q2","vendorId":"shopA","modelId":"m2","materialConfigId":"pla","priceInclVat":6,"productionTimeSlow":5},
			{"quoteId":"q3","vendorId":"shopA","modelId":"m2","materialConfigId":"pla","priceInclVat":9},
			{"quoteId":"q4","vendorId":"shopB","modelId":"m1","materialConfigId":"pla","priceInclVat":3}
		],
		"shippings": [
			{"shippingId":"s1","vendorId":"shopA","name":"Standard","priceInclVat":5,"deliveryTime":"2-3","carrier":"DHL"},
			{"shippingId":"s2","vendorId":"shopA","name":"Express","priceInclVat":15,"deliveryTime":"1"},
			{"shippingId":"s3","vendorId":"shopB","name":"Standard","priceInclVat":1}
		]}`))
	parts := map[string]PartFile{"m1": {PartID: "dock", Quantity: 2}, "m2": {PartID: "foot", Quantity: 1}}
	options := craftcloudOptions(prices, QuoteRequest{Currency: "EUR"}, parts, "price-1")
	if len(options) != 1 {
		t.Fatalf("shopB cannot print every part; want 1 option, got %d", len(options))
	}
	o := options[0]
	if o.Vendor != "shopA" || o.Subtotal != 16 || o.Shipping != 5 || o.Total != 21 {
		t.Fatalf("unexpected option: %+v", o)
	}
	var ref map[string]any
	_ = json.Unmarshal(o.Ref, &ref)
	if ref["shipping_id"] != "s1" || ref["price_id"] != "price-1" {
		t.Fatalf("unexpected provider ref: %v", ref)
	}
	if o.ExpiresAt == "" || o.LeadDays < 5 {
		t.Fatalf("expiry/lead time not derived: %+v", o)
	}
}

func TestOrderOutsidePolicyNeedsExactApprovalAndSubmitsOnce(t *testing.T) {
	fx := newFabricationFixture(t, homeSettings(false), 40, 42, nil)
	ctx := context.Background()
	order, err := fx.service.PrepareOrder(ctx, fx.quote.ID, "o1", "lab", "agent")
	if err != nil {
		t.Fatal(err)
	}
	if order.Status != orderAwaitingApproval {
		t.Fatalf("status %s, want awaiting_approval", order.Status)
	}
	if _, err := fx.service.Submit(ctx, order.ID); err == nil {
		t.Fatal("submit before approval must fail")
	}
	if _, err := fx.service.Approve(order.ID, 40, map[string]any{"email": "owner@example.com"}); err == nil || !strings.Contains(err.Error(), "confirm_total") {
		t.Fatalf("approval at the quoted price must be refused once the draft price differs, got %v", err)
	}
	approved, err := fx.service.Approve(order.ID, 42, map[string]any{"email": "owner@example.com"})
	if err != nil || approved.Status != orderApproved {
		t.Fatalf("approve: %v %+v", err, approved)
	}
	submitted, err := fx.service.Submit(ctx, order.ID)
	if err != nil || submitted.Status != orderSubmitted || submitted.ProviderOrderID != "P-1" {
		t.Fatalf("submit: %v %+v", err, submitted)
	}
	if _, err := fx.service.Submit(ctx, order.ID); err == nil {
		t.Fatal("second submit must be refused")
	}
	if n := atomic.LoadInt32(fx.submits); n != 1 {
		t.Fatalf("provider Submit called %d times, want 1", n)
	}
}

func TestOrderWithinPolicyIsApprovedByPolicy(t *testing.T) {
	fx := newFabricationFixture(t, homeSettings(true), 40, 41, nil)
	order, err := fx.service.PrepareOrder(context.Background(), fx.quote.ID, "o1", "lab", "agent")
	if err != nil {
		t.Fatal(err)
	}
	if order.Status != orderApproved || pickString(decodeAny(order.Approval), "by") != "policy" {
		t.Fatalf("expected policy approval, got %s %s", order.Status, order.Approval)
	}
	// Tighten the policy before submit: the order must fall back to a person.
	tight := homeSettings(true)
	tight.MaxOrderTotal = 10
	if err := fx.service.store.SaveFabricationSettings("p", tight, "test"); err != nil {
		t.Fatal(err)
	}
	after, err := fx.service.Submit(context.Background(), order.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != orderAwaitingApproval || atomic.LoadInt32(fx.submits) != 0 {
		t.Fatalf("policy change must block submit; status %s, submits %d", after.Status, *fx.submits)
	}
}

func TestSubmitFailureIsParkedNotRetried(t *testing.T) {
	fx := newFabricationFixture(t, homeSettings(true), 40, 40, errors.New("timeout"))
	ctx := context.Background()
	order, err := fx.service.PrepareOrder(ctx, fx.quote.ID, "o1", "lab", "agent")
	if err != nil {
		t.Fatal(err)
	}
	after, err := fx.service.Submit(ctx, order.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != orderUnknown {
		t.Fatalf("status %s, want %s", after.Status, orderUnknown)
	}
	if _, err := fx.service.Submit(ctx, order.ID); err == nil {
		t.Fatal("an order with an unknown submit outcome must not be resubmitted")
	}
	if n := atomic.LoadInt32(fx.submits); n != 1 {
		t.Fatalf("provider Submit called %d times, want 1", n)
	}
}

func TestPrepareRefusesStaleRevisionAndMissingAddress(t *testing.T) {
	fx := newFabricationFixture(t, homeSettings(false), 40, 40, nil)
	if _, err := fx.service.PrepareOrder(context.Background(), fx.quote.ID, "o1", "nowhere", "agent"); err == nil {
		t.Fatal("unknown address must be refused")
	}
	if _, err := fx.service.PrepareOrder(context.Background(), fx.quote.ID, "o9", "lab", "agent"); err == nil {
		t.Fatal("unknown option must be refused")
	}
}

func TestCallerReportsProviderStatus(t *testing.T) {
	prev := executeTool
	t.Cleanup(func() { executeTool = prev })
	executeTool = func(context.Context, *sdk.AppCtx, int64, string, map[string]any) (*sdk.ExecuteResult, error) {
		return &sdk.ExecuteResult{Success: false, Status: 404, Data: json.RawMessage(`{"error":"not found"}`)}, nil
	}
	_, status, err := (&Service{}).caller(1)(context.Background(), "upload_model", nil)
	if err == nil || status != 404 || !strings.Contains(err.Error(), "upload_model returned HTTP 404") {
		t.Fatalf("got status %d err %v", status, err)
	}
}
