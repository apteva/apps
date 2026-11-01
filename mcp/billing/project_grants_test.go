package main

import (
	"errors"
	"sort"
	"strings"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

// The platform fans a project_id "*" call out only to tools carrying
// readOnlyHint, so this list must stay exactly the tools that never write.
func TestMCPTools_ReadOnlyHints(t *testing.T) {
	want := []string{
		"billing_reconciliation_status", "customers_get", "customers_get_context",
		"customers_search", "invoices_get", "invoices_history", "invoices_search",
		"issuer_get", "payment_methods_list", "payment_processor_public_config",
		"payments_list",
	}
	var got []string
	for _, tool := range (&App{}).MCPTools() {
		if v, ok := tool.Annotations["readOnlyHint"]; ok {
			if v != true {
				t.Errorf("%s readOnlyHint = %#v, want true", tool.Name, v)
			}
			got = append(got, tool.Name)
		}
		if _, ok := tool.InputSchema["properties"].(map[string]any)["project_id"]; ok {
			t.Errorf("%s declares project_id, which the platform reserves for grant routing", tool.Name)
		}
	}
	sort.Strings(got)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("read-only tools = %v, want %v", got, want)
	}
}

// A global Billing install must name the project on catalog lookups;
// catalog refuses calls without one.
func TestCatalogLookupsCarryProject(t *testing.T) {
	var calls []map[string]any
	platform := &invoiceStoragePlatform{call: func(app, tool string, input map[string]any) (any, error) {
		if app != "catalog" {
			return nil, errors.New("unexpected app " + app)
		}
		calls = append(calls, input)
		if input["_project_id"] != "invoice-project" {
			return nil, errors.New("project_id required")
		}
		switch tool {
		case "catalog_prices_get":
			return map[string]any{"price": map[string]any{"id": 7, "product_id": 3, "unit_amount_cents": 2500, "currency": "eur", "nickname": "Setup"}}, nil
		case "catalog_products_get":
			return map[string]any{"product": map[string]any{"id": 3, "name": "Telephony"}}, nil
		}
		return nil, errors.New("unexpected tool " + tool)
	}}
	ctx := newGlobalTestCtx(t, tk.WithPlatform(platform))
	app := &App{}
	out, err := app.toolCustomersUpsertByEmail(ctx, map[string]any{"_project_id": "invoice-project", "email": "catalog@example.test"})
	if err != nil {
		t.Fatal(err)
	}
	customer := out.(map[string]any)["customer"].(*Customer)
	out, err = app.toolInvoicesCreate(ctx, map[string]any{"_project_id": "invoice-project", "customer_id": customer.ID,
		"line_items": []any{map[string]any{"price_id": 7, "quantity": 1}}})
	if err != nil {
		t.Fatal(err)
	}
	inv := out.(map[string]any)["invoice"].(*Invoice)
	if inv.TotalCents != 2500 || len(calls) != 2 {
		t.Fatalf("total=%d calls=%v", inv.TotalCents, calls)
	}
}
