package main

import (
	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
	"testing"
)

type financeBindingsPlatform struct {
	tk.BasePlatformClient
	bindings map[string]any
	conns    map[int64]sdk.PlatformConnection
	writes   int
}

func (p *financeBindingsPlatform) WhoAmI() (*sdk.InstallIdentity, error) {
	return &sdk.InstallIdentity{Bindings: p.bindings}, nil
}
func (p *financeBindingsPlatform) GetConnection(id int64) (*sdk.PlatformConnection, error) {
	c, ok := p.conns[id]
	if !ok {
		return nil, nil
	}
	return &c, nil
}
func (p *financeBindingsPlatform) ExecuteIntegrationTool(id int64, tool string, args map[string]any) (*sdk.ExecuteResult, error) {
	p.writes++
	return &sdk.ExecuteResult{Success: true, Status: 200, Data: []byte(`{}`)}, nil
}
func TestFinanceSingleMultipleConnectionRole(t *testing.T) {
	m := (&App{}).Manifest()
	if len(m.Requires.Integrations) != 1 {
		t.Fatal("finance must expose one integration dependency")
	}
	dep := m.Requires.Integrations[0]
	if dep.Role != financeConnectionRole || dep.Mode != "multiple" || dep.Required {
		t.Fatal(dep)
	}
	want := map[string]bool{"trading212": true}
	for _, slug := range bankingProviderSlugs {
		want[slug] = true
	}
	for _, slug := range dep.CompatibleSlugs {
		delete(want, slug)
	}
	if len(want) > 0 {
		t.Fatal("missing financial providers", want)
	}
}
func TestFinanceBindingsShareReadAndWriteSelection(t *testing.T) {
	pf := &financeBindingsPlatform{bindings: map[string]any{financeConnectionRole: map[string]any{"ids": []any{float64(1), float64(2), float64(3)}, "default_id": float64(2)}}, conns: map[int64]sdk.PlatformConnection{1: {ID: 1, AppSlug: "enable-banking", Status: "active"}, 2: {ID: 2, AppSlug: "plaid", Status: "active"}, 3: {ID: 3, AppSlug: "trading212", Status: "active"}, 4: {ID: 4, AppSlug: "teller", Status: "active"}}}
	ctx := newCtxWithPlatform(t, pf)
	list, err := listBankingConnections(ctx, "")
	if err != nil || len(list) != 2 || list[0].ID != 2 || !list[0].Default {
		t.Fatal(list, err)
	}
	for _, id := range []int64{1, 2} {
		read, _, err := bankingConnection(ctx, "", id)
		if err != nil {
			t.Fatal(err)
		}
		write, _, err := paymentConnection(ctx, id)
		if err != nil || read.ID != write.ID {
			t.Fatal(read, write, err)
		}
	}
	defaultBank, _, err := bankingConnection(ctx, "", 0)
	if err != nil || defaultBank.ID != 2 {
		t.Fatal(defaultBank, err)
	}
	broker, err := brokerageConnection(ctx, 0)
	if err != nil || broker.ID != 3 {
		t.Fatal(broker, err)
	}
	if _, _, err := paymentConnection(ctx, 4); err == nil {
		t.Fatal("unselected connection accepted")
	}
	var out map[string]any
	if err := executeIntegrationJSON(ctx, 4, "create_payment", nil, &out); err == nil || pf.writes != 0 {
		t.Fatal("unselected connection reached provider", err)
	}
	if err := executeIntegrationJSON(ctx, 1, "get_payment", nil, &out); err != nil || pf.writes != 1 {
		t.Fatal(err)
	}
}
func TestFinanceLegacySingleBindingAndProjectIsolation(t *testing.T) {
	pf := &financeBindingsPlatform{bindings: map[string]any{financeConnectionRole: float64(7)}, conns: map[int64]sdk.PlatformConnection{7: {ID: 7, AppSlug: "teller", Status: "active", ProjectID: "test-proj"}}}
	ctx := newCtxWithPlatform(t, pf)
	c, _, err := paymentConnection(ctx, 7)
	if err != nil || c.ID != 7 {
		t.Fatal(c, err)
	}
	if _, _, err := bankingConnection(ctx.WithProject("other-project"), "", 7); err == nil {
		t.Fatal("foreign connection accepted")
	}
}
