package main

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type recordingPlatform struct {
	tk.BasePlatformClient
	mu        sync.Mutex
	calls     []callAppCall
	responses map[string]json.RawMessage
	bindings  map[string]int64
}

type callAppCall struct {
	App   string
	Tool  string
	Input map[string]any
}

func newRecordingPlatform() *recordingPlatform {
	return &recordingPlatform{
		responses: map[string]json.RawMessage{},
		bindings: map[string]int64{
			"target-circle":     101,
			"impact":            102,
			"awin":              103,
			"cj-affiliate":      104,
			"amazon-associates": 105,
			"skimlinks":         106,
			"sovrn":             107,
			"partnerstack":      108,
			"shareasale":        109,
		},
	}
}

func (p *recordingPlatform) WhoAmI() (*sdk.InstallIdentity, error) {
	bindings := map[string]any{}
	for role, id := range p.bindings {
		bindings[role] = float64(id)
	}
	return &sdk.InstallIdentity{
		AppName:   "affiliate",
		ProjectID: "test-project",
		Bindings:  bindings,
	}, nil
}

func (p *recordingPlatform) GetConnection(id int64) (*sdk.PlatformConnection, error) {
	for role, connID := range p.bindings {
		if connID == id {
			return &sdk.PlatformConnection{ID: id, AppSlug: role, Status: "active"}, nil
		}
	}
	return nil, fmt.Errorf("unknown connection %d", id)
}

func (p *recordingPlatform) ExecuteIntegrationTool(connID int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
	role := ""
	for r, id := range p.bindings {
		if id == connID {
			role = r
			break
		}
	}
	if role == "" {
		return nil, fmt.Errorf("unknown connection %d", connID)
	}
	p.mu.Lock()
	p.calls = append(p.calls, callAppCall{App: role, Tool: tool, Input: input})
	p.mu.Unlock()
	if raw, ok := p.responses[role+":"+tool]; ok {
		return &sdk.ExecuteResult{Success: true, Status: 200, Data: raw}, nil
	}
	return nil, fmt.Errorf("no stub response for %s:%s", role, tool)
}

func (p *recordingPlatform) CallApp(app, tool string, input map[string]any) (json.RawMessage, error) {
	p.mu.Lock()
	p.calls = append(p.calls, callAppCall{App: app, Tool: tool, Input: input})
	p.mu.Unlock()
	if raw, ok := p.responses[app+":"+tool]; ok {
		return raw, nil
	}
	return nil, fmt.Errorf("no stub response for %s:%s", app, tool)
}

func (p *recordingPlatform) CallAppResult(app, tool string, input map[string]any, out any) error {
	raw, err := p.CallApp(app, tool, input)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

func newTestCtx(t *testing.T, platform sdk.PlatformClient, cfg map[string]string) *sdk.AppCtx {
	t.Helper()
	opts := []tk.Option{tk.WithProjectID("test-project")}
	if platform != nil {
		opts = append(opts, tk.WithPlatform(platform))
	}
	if cfg != nil {
		opts = append(opts, tk.WithConfig(cfg))
	}
	ctx := tk.NewAppCtx(t, "apteva.yaml", opts...)
	globalCtx = ctx
	return ctx
}

func TestManualLinkCreate(t *testing.T) {
	ctx := newTestCtx(t, nil, nil)
	app := &App{}

	out, err := app.toolLinkCreate(ctx, map[string]any{
		"url":           "https://example.com/product",
		"network":       "target-circle",
		"affiliate_url": "https://track.example/click",
		"campaign":      "guide",
	})
	if err != nil {
		t.Fatal(err)
	}
	link := out.(map[string]any)["link"].(*Link)
	if link.NetworkKey != "target-circle" || link.Campaign != "guide" {
		t.Fatalf("unexpected link: %+v", link)
	}
	if link.ShortURL != "" {
		t.Fatalf("manual link should not be shortened by default: %+v", link)
	}
}

func TestRefreshOffersAndStatsFromProvider(t *testing.T) {
	platform := newRecordingPlatform()
	platform.responses["target-circle:offers_list"] = json.RawMessage(`{
		"offers": [{
			"id": "mintos",
			"advertiser": "Mintos",
			"offerName": "Mintos Investment Marketplace",
			"status": "accepted",
			"category": "Fintech",
			"commission": "CPL 5 EUR",
			"deeplinking": true
		}]
	}`)
	platform.responses["target-circle:transactions_list"] = json.RawMessage(`{
		"transactions": [{
			"date": "2026-05-25",
			"offer_external_id": "mintos",
			"clicks": 10,
			"conversions": 2,
			"commission_cents": 1000,
			"currency": "EUR"
		}]
	}`)
	ctx := newTestCtx(t, platform, nil)
	app := &App{}

	out, err := app.toolRefresh(ctx, map[string]any{"network": "target-circle", "kind": "all"})
	if err != nil {
		t.Fatal(err)
	}
	summary := out.(*RefreshSummary)
	if summary.OffersUpserted != 1 || summary.StatsDaysUpserted != 1 {
		t.Fatalf("bad summary: %+v", summary)
	}
	offers, err := dbListOffers(ctx.AppDB(), "mintos", "", "", 10)
	if err != nil || len(offers) != 1 {
		t.Fatalf("offers len=%d err=%v", len(offers), err)
	}
	stats, err := dbStats(ctx.AppDB(), "", "", "target-circle", offers[0].ID, 0, "day")
	if err != nil || len(stats) != 1 || stats[0].CommissionCents != 1000 {
		t.Fatalf("stats=%+v err=%v", stats, err)
	}
}

func TestLinkCreateCallsProviderAndRedirects(t *testing.T) {
	platform := newRecordingPlatform()
	platform.responses["target-circle:codes_list"] = json.RawMessage(`{
		"clickUrl": "https://c.trackmytarget.com/abc123"
	}`)
	platform.responses["redirects:redirect_add"] = json.RawMessage(`{
		"redirect": {"id": 318}
	}`)
	ctx := newTestCtx(t, platform, map[string]string{"default_short_hostname": "go.example.com"})
	app := &App{}

	offer, err := dbUpsertOffer(ctx.AppDB(), OfferInput{
		NetworkKey:   "target-circle",
		ExternalID:   "mintos",
		MerchantName: "Mintos",
		OfferName:    "Mintos Investment Marketplace",
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err := app.toolLinkCreate(ctx, map[string]any{
		"url":      "https://www.mintos.com/en/",
		"offer_id": offer.ID,
		"campaign": "p2p-guide",
		"shorten":  true,
	})
	if err != nil {
		t.Fatal(err)
	}
	link := out.(map[string]any)["link"].(*Link)
	if link.AffiliateURL != "https://c.trackmytarget.com/abc123" {
		t.Fatalf("affiliate_url = %q", link.AffiliateURL)
	}
	if link.ShortURL != "https://go.example.com/p2p-guide" || link.RedirectRuleID != 318 {
		t.Fatalf("short link not recorded: %+v", link)
	}
	if len(platform.calls) != 2 {
		t.Fatalf("calls=%+v", platform.calls)
	}
	if platform.calls[0].App != "target-circle" || platform.calls[0].Tool != "codes_list" {
		t.Fatalf("provider call wrong: %+v", platform.calls[0])
	}
	if platform.calls[1].App != "redirects" || platform.calls[1].Tool != "redirect_add" {
		t.Fatalf("redirect call wrong: %+v", platform.calls[1])
	}
}
