package main

import (
	"fmt"
	"testing"
)

func TestCustomerSearchProductionLookupWithContext(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	circle := mustCustomer(t, ctx, "circle@example.com", "Circlewise")
	google := mustCustomer(t, ctx, "google@example.com", "Google Ireland Limited")
	invoice := mustFinalize(t, ctx, mustDraft(t, ctx, circle.ID, []any{line("Consulting", 1, 10000, 0)}).ID)
	if _, err := app.toolPaymentsRecord(ctx, map[string]any{"invoice_id": invoice.ID, "amount_cents": 10000, "method": "wire", "received_at": "2026-04-29T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	mustFinalize(t, ctx, mustDraft(t, ctx, google.ID, []any{line("Services", 1, 25000, 0)}).ID)
	for _, tc := range []struct {
		q, mode string
		id      int64
		due     int64
		confirm bool
	}{
		{"  cIrClEwIsE  ", "phrase", circle.ID, 0, false},
		{"Google Ads", "token_candidates", google.ID, 25000, true},
	} {
		t.Run(tc.q, func(t *testing.T) {
			raw, err := app.toolCustomersSearch(ctx, map[string]any{"q": tc.q, "include_context": true})
			if err != nil {
				t.Fatal(err)
			}
			out := raw.(map[string]any)
			customers := out["customers"].([]*Customer)
			if len(customers) != 1 || customers[0].ID != tc.id || out["match_mode"] != tc.mode || out["requires_confirmation"] != tc.confirm {
				t.Fatalf("wrong search result: %#v", out)
			}
			contexts := out["contexts"].([]map[string]any)
			if len(contexts) != 1 || contexts[0]["customer"].(*Customer).ID != tc.id {
				t.Fatalf("context not aligned with match: %#v", contexts)
			}
			if got := contexts[0]["lifetime"].(map[string]any)["outstanding_cents"]; got != tc.due {
				t.Fatalf("outstanding=%v, want %d", got, tc.due)
			}
			counts := contexts[0]["lifetime"].(map[string]any)["invoice_status_counts"].(map[string]int)
			paid, open := 1, 0
			if tc.id == google.ID {
				paid, open = 0, 1
			}
			if counts["paid"] != paid || counts["open"] != open || counts["uncollectible"] != 0 {
				t.Fatalf("wrong invoice status counts: %#v", counts)
			}
			if _, ok := contexts[0]["open_invoices"]; ok {
				t.Fatal("search context should return counts rather than full invoice documents")
			}
			if tc.id == circle.ID {
				payments := contexts[0]["recent_payments"].([]*Payment)
				if len(payments) != 1 || payments[0].AmountCents != 10000 || payments[0].Method != "wire" {
					t.Fatalf("missing latest payment: %#v", payments)
				}
			}
		})
	}
}

func TestCustomerSearchFallbackDoesNotLeakOrSuppressAcrossProjects(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	local := mustCustomer(t, ctx, "local@example.com", "Google Ireland Limited")
	foreign := mustCustomer(t, ctx, "foreign@example.com", "Google Ads")
	deleted := mustCustomer(t, ctx, "deleted@example.com", "Google Ads")
	if _, err := ctx.AppDB().Exec(`UPDATE customers SET project_id='another-project' WHERE id=?`, foreign.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := ctx.AppDB().Exec(`UPDATE customers SET deleted_at='2026-01-01' WHERE id=?`, deleted.ID); err != nil {
		t.Fatal(err)
	}
	raw, err := app.toolCustomersSearch(ctx, map[string]any{"q": "Google Ads", "include_context": true})
	if err != nil {
		t.Fatal(err)
	}
	out := raw.(map[string]any)
	rows := out["customers"].([]*Customer)
	if len(rows) != 1 || rows[0].ID != local.ID || out["match_mode"] != "token_candidates" {
		t.Fatalf("foreign/deleted matches changed fallback: %#v", out)
	}
	counts := out["contexts"].([]map[string]any)[0]["lifetime"].(map[string]any)["invoice_status_counts"].(map[string]int)
	if counts["open"] != 0 || counts["paid"] != 0 || counts["uncollectible"] != 0 {
		t.Fatalf("customer with no invoices: %#v", counts)
	}
}

func TestCustomerSearchRankingPaginationAndIsolation(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	direct := mustCustomer(t, ctx, "exact@example.com", "Google Ads")
	substring := mustCustomer(t, ctx, "extended@example.com", "Google Ads Europe")
	candidate := mustCustomer(t, ctx, "legal@example.com", "Google Ireland Limited")
	other := mustCustomer(t, ctx, "other@example.com", "Google Ads")
	deleted := mustCustomer(t, ctx, "deleted@example.com", "Google Ads")
	if _, err := ctx.AppDB().Exec(`UPDATE customers SET project_id='another-project' WHERE id=?`, other.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := ctx.AppDB().Exec(`UPDATE customers SET deleted_at='2026-01-01' WHERE id=?`, deleted.ID); err != nil {
		t.Fatal(err)
	}
	for offset, want := range []int64{direct.ID, substring.ID, 0} {
		raw, err := app.toolCustomersSearch(ctx, map[string]any{"q": "Google Ads", "limit": 1, "offset": offset})
		if err != nil {
			t.Fatal(err)
		}
		out := raw.(map[string]any)
		rows := out["customers"].([]*Customer)
		if out["match_mode"] != "phrase" || (want == 0 && len(rows) != 0) || (want != 0 && (len(rows) != 1 || rows[0].ID != want)) {
			t.Fatalf("offset %d: %#v (candidate %d must never fill a later phrase page)", offset, out, candidate.ID)
		}
		if out["has_more"] != (offset == 0) {
			t.Fatalf("wrong has_more on page %d: %#v", offset, out)
		}
	}
	// Without an exact email match, never broaden across unrelated customers.
	raw, err := app.toolCustomersSearch(ctx, map[string]any{"q": "Google Ads", "email": "absent@example.com"})
	if err != nil || raw.(map[string]any)["count"] != 0 {
		t.Fatalf("exact email filter broadened: %v, %v", raw, err)
	}
}

func TestCustomerSearchCandidatesRemainAmbiguousAndPaginated(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	first := mustCustomer(t, ctx, "one@example.com", "Google Ads Ireland")
	second := mustCustomer(t, ctx, "two@example.com", "Google Ireland Limited")
	for offset, want := range []int64{first.ID, second.ID} {
		raw, err := app.toolCustomersSearch(ctx, map[string]any{"q": "Google Ireland Ads Team", "limit": 1, "offset": offset})
		if err != nil {
			t.Fatal(err)
		}
		out := raw.(map[string]any)
		rows := out["customers"].([]*Customer)
		if len(rows) != 1 || rows[0].ID != want || out["match_mode"] != "token_candidates" || out["requires_confirmation"] != true || out["has_more"] != (offset == 0) {
			t.Fatalf("wrong candidate page: %#v", out)
		}
	}
}

func TestCustomerSearchLiteralWildcardsAndBoundedContext(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	wildcard := mustCustomer(t, ctx, "literal@example.com", `100%_\ Company`)
	for i := range 21 {
		mustCustomer(t, ctx, fmt.Sprintf("other%d@example.com", i), fmt.Sprintf("Other %d", i))
	}
	for _, q := range []string{"%", "_", `\`} {
		raw, err := app.toolCustomersSearch(ctx, map[string]any{"q": q})
		if err != nil {
			t.Fatal(err)
		}
		rows := raw.(map[string]any)["customers"].([]*Customer)
		if len(rows) != 1 || rows[0].ID != wildcard.ID {
			t.Fatalf("%q interpreted as wildcard: %#v", q, raw)
		}
		if _, ok := raw.(map[string]any)["contexts"]; ok {
			t.Fatal("search without include_context unexpectedly loaded context")
		}
	}
	raw, err := app.toolCustomersSearch(ctx, map[string]any{"include_context": true, "limit": 200})
	if err != nil {
		t.Fatal(err)
	}
	out := raw.(map[string]any)
	if out["count"] != 20 || len(out["contexts"].([]map[string]any)) != 20 || out["has_more"] != true {
		t.Fatalf("context page was not bounded: %#v", out)
	}
	raw, err = app.toolCustomersSearch(ctx, map[string]any{"q": "Missing Customer", "include_context": true})
	if err != nil || raw.(map[string]any)["count"] != 0 || len(raw.(map[string]any)["contexts"].([]map[string]any)) != 0 {
		t.Fatalf("missing customer: %v, %v", raw, err)
	}
}
