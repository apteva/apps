package main

// Tier 1 — every MCP tool handler exercised against an in-memory
// SQLite. Three groups: UNIT (handler logic), HTTP (in-process
// route dispatch), MANIFEST (contract checks).
//
// Tier 2 (real binary via tk.SpawnSidecar) lives in integration_test.go.

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

// ─── Accounts ────────────────────────────────────────────────────

func TestUnit_AccountsCreateAndList(t *testing.T) {
	ctx := newCtx(t)
	app := &App{}
	res, err := app.toolAccountsCreate(ctx, map[string]any{
		"name": "Main Checking", "kind": "cash", "currency": "EUR", "opening_balance": float64(100000),
	})
	if err != nil {
		t.Fatal(err)
	}
	acc := res.(Account)
	if acc.Name != "Main Checking" || acc.Currency != "EUR" || acc.OpeningBalance != 100000 {
		t.Fatalf("created malformed: %+v", acc)
	}
	out, _ := app.toolAccountsList(ctx, nil)
	accs := out.(map[string]any)["accounts"].([]Account)
	if len(accs) != 1 || accs[0].CashBalance != 100000 {
		t.Fatalf("expected one account with cash=100000, got %+v", accs)
	}
}

func TestUnit_AccountsCreate_RequiresValidKind(t *testing.T) {
	ctx := newCtx(t)
	app := &App{}
	if _, err := app.toolAccountsCreate(ctx, map[string]any{"name": "x", "kind": "imaginary"}); err == nil {
		t.Error("expected error for bad kind")
	}
}

func TestUnit_AccountsUpdate_RefusesNoOp(t *testing.T) {
	ctx := newCtx(t)
	app := &App{}
	c, _ := app.toolAccountsCreate(ctx, map[string]any{"name": "a", "kind": "cash"})
	id := c.(Account).ID
	if _, err := app.toolAccountsUpdate(ctx, map[string]any{"id": float64(id)}); err == nil {
		t.Error("expected no-op refusal")
	}
}

// ─── Instruments ─────────────────────────────────────────────────

func TestUnit_InstrumentsCreateSharedVsPrivate(t *testing.T) {
	ctx := newCtx(t)
	app := &App{}
	// Stock with project_only=false → shared (project_id NULL).
	stock, err := app.toolInstrumentsCreate(ctx, map[string]any{
		"kind": "stock", "symbol": "AAPL", "name": "Apple", "quote_currency": "USD",
	})
	if err != nil {
		t.Fatal(err)
	}
	if stock.(Instrument).ProjectID != nil {
		t.Errorf("expected shared catalog row (project_id NULL) for stock, got %v", *stock.(Instrument).ProjectID)
	}
	// Real estate is always project-scoped, even without project_only.
	house, err := app.toolInstrumentsCreate(ctx, map[string]any{
		"kind": "real_estate", "symbol": "house-1", "name": "Paris 15e", "quote_currency": "EUR",
	})
	if err != nil {
		t.Fatal(err)
	}
	if house.(Instrument).ProjectID == nil {
		t.Errorf("expected project-scoped real_estate row")
	}
}

func TestUnit_InstrumentsSearch(t *testing.T) {
	ctx := newCtx(t)
	app := &App{}
	_, _ = app.toolInstrumentsCreate(ctx, map[string]any{
		"kind": "stock", "symbol": "AAPL", "name": "Apple", "quote_currency": "USD",
	})
	_, _ = app.toolInstrumentsCreate(ctx, map[string]any{
		"kind": "stock", "symbol": "MSFT", "name": "Microsoft", "quote_currency": "USD",
	})
	out, err := app.toolInstrumentsSearch(ctx, map[string]any{"query": "appl"})
	if err != nil {
		t.Fatal(err)
	}
	ins := out.(map[string]any)["instruments"].([]Instrument)
	if len(ins) != 1 || ins[0].Symbol != "AAPL" {
		t.Errorf("expected AAPL match, got %+v", ins)
	}
}

// ─── Buy / Sell / P&L ────────────────────────────────────────────

func TestUnit_BuyThenSell_HoldingMath(t *testing.T) {
	ctx := newCtx(t)
	app := &App{}
	acc := mustCreateAccount(t, app, ctx, "Broker", "brokerage", "USD", 1000000)
	inst := mustCreateInstrument(t, app, ctx, "stock", "AAPL", "Apple", "USD")

	// Buy 10 shares for $1,000.00 (100000 minor units).
	_, err := app.toolTxnsBuy(ctx, map[string]any{
		"account_id": float64(acc.ID), "instrument_id": float64(inst.ID),
		"quantity": float64(10), "amount": float64(100000),
		"posted_at": "2026-01-15T10:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Cash should have dropped by 1000.00 → 9000.00.
	bal := mustCashBalance(ctx, acc.ID, acc.OpeningBalance)
	if bal != 900000 {
		t.Errorf("after buy cash=%d, want 900000", bal)
	}
	// Holding should be qty=10 cost_basis=100000.
	hs, _ := listHoldingsRich(ctx, acc.ID, 0, false)
	if len(hs) != 1 || hs[0].Quantity != 10 || hs[0].CostBasis != 100000 {
		t.Fatalf("unexpected holding: %+v", hs)
	}

	// Sell 4 shares for $500. Realised P&L = 500 − (4/10 × 1000) = +100.
	r, err := app.toolTxnsSell(ctx, map[string]any{
		"account_id": float64(acc.ID), "instrument_id": float64(inst.ID),
		"quantity": float64(4), "amount": float64(50000),
		"posted_at": "2026-02-15T10:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	sellTxn := r.(Transaction)
	// cost_basis_delta should be −40000 (4/10 of 100000).
	if sellTxn.CostBasisDelta != -40000 {
		t.Errorf("sell cost_basis_delta=%d, want -40000", sellTxn.CostBasisDelta)
	}
	// Cash should be 9000 + 500 = 9500.
	bal = mustCashBalance(ctx, acc.ID, acc.OpeningBalance)
	if bal != 950000 {
		t.Errorf("after sell cash=%d, want 950000", bal)
	}
	// Holding should be qty=6 cost_basis=60000.
	hs, _ = listHoldingsRich(ctx, acc.ID, 0, false)
	if len(hs) != 1 || hs[0].Quantity != 6 || hs[0].CostBasis != 60000 {
		t.Errorf("after sell holding: %+v", hs)
	}
}

func TestUnit_SellMoreThanOwned_Fails(t *testing.T) {
	ctx := newCtx(t)
	app := &App{}
	acc := mustCreateAccount(t, app, ctx, "Broker", "brokerage", "USD", 0)
	inst := mustCreateInstrument(t, app, ctx, "stock", "AAPL", "Apple", "USD")
	_, _ = app.toolTxnsBuy(ctx, map[string]any{
		"account_id": float64(acc.ID), "instrument_id": float64(inst.ID),
		"quantity": float64(3), "amount": float64(30000),
		"posted_at": "2026-01-15T10:00:00Z",
	})
	_, err := app.toolTxnsSell(ctx, map[string]any{
		"account_id": float64(acc.ID), "instrument_id": float64(inst.ID),
		"quantity": float64(10), "amount": float64(100000),
		"posted_at": "2026-02-15T10:00:00Z",
	})
	if err == nil {
		t.Error("expected error on overdraft sell")
	}
}

func TestUnit_SellAll_ClosesHolding(t *testing.T) {
	ctx := newCtx(t)
	app := &App{}
	acc := mustCreateAccount(t, app, ctx, "Broker", "brokerage", "USD", 0)
	inst := mustCreateInstrument(t, app, ctx, "stock", "AAPL", "Apple", "USD")
	_, _ = app.toolTxnsBuy(ctx, map[string]any{
		"account_id": float64(acc.ID), "instrument_id": float64(inst.ID),
		"quantity": float64(2), "amount": float64(20000),
		"posted_at": "2026-01-15T10:00:00Z",
	})
	_, _ = app.toolTxnsSell(ctx, map[string]any{
		"account_id": float64(acc.ID), "instrument_id": float64(inst.ID),
		"quantity": float64(2), "amount": float64(25000),
		"posted_at": "2026-02-15T10:00:00Z",
	})
	hsOpen, _ := listHoldingsRich(ctx, acc.ID, 0, false)
	if len(hsOpen) != 0 {
		t.Errorf("expected zero open holdings, got %+v", hsOpen)
	}
	hsAll, _ := listHoldingsRich(ctx, acc.ID, 0, true)
	if len(hsAll) != 1 || hsAll[0].Quantity != 0 {
		t.Errorf("expected one closed holding qty=0, got %+v", hsAll)
	}
}

// ─── Transfers ───────────────────────────────────────────────────

func TestUnit_Transfer_HappyPath(t *testing.T) {
	ctx := newCtx(t)
	app := &App{}
	src := mustCreateAccount(t, app, ctx, "Checking", "cash", "EUR", 500000)
	dst := mustCreateAccount(t, app, ctx, "Savings", "cash", "EUR", 0)
	_, err := app.toolTxnsTransfer(ctx, map[string]any{
		"from_account_id": float64(src.ID), "to_account_id": float64(dst.ID),
		"amount": float64(200000), "posted_at": "2026-03-01T10:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	srcBal := mustCashBalance(ctx, src.ID, src.OpeningBalance)
	dstBal := mustCashBalance(ctx, dst.ID, dst.OpeningBalance)
	if srcBal != 300000 || dstBal != 200000 {
		t.Errorf("transfer balances wrong: src=%d dst=%d", srcBal, dstBal)
	}
}

func TestUnit_Transfer_RefusesCrossCurrency(t *testing.T) {
	ctx := newCtx(t)
	app := &App{}
	src := mustCreateAccount(t, app, ctx, "EUR cash", "cash", "EUR", 100000)
	dst := mustCreateAccount(t, app, ctx, "USD cash", "cash", "USD", 0)
	if _, err := app.toolTxnsTransfer(ctx, map[string]any{
		"from_account_id": float64(src.ID), "to_account_id": float64(dst.ID),
		"amount": float64(10000), "posted_at": "2026-03-01T10:00:00Z",
	}); err == nil {
		t.Error("expected cross-currency rejection in v0.1")
	}
}

// ─── Valuation ───────────────────────────────────────────────────

func TestUnit_ValuationFlowsThroughNetWorth(t *testing.T) {
	ctx := newCtx(t)
	app := &App{}
	acc := mustCreateAccount(t, app, ctx, "House", "real_estate", "EUR", 0)
	house := mustCreateInstrument(t, app, ctx, "real_estate", "paris-15e", "Apartment", "EUR")
	// Set holding to 1.0 with cost_basis (purchase price) of €300,000.
	if _, err := app.toolHoldingsSet(ctx, map[string]any{
		"account_id": float64(acc.ID), "instrument_id": float64(house.ID),
		"quantity": float64(1), "cost_basis": float64(30000000),
	}); err != nil {
		t.Fatal(err)
	}
	// Revalue to €350,000.
	if _, err := app.toolValuationSet(ctx, map[string]any{
		"instrument_id": float64(house.ID),
		"value":         float64(35000000),
		"account_id":    float64(acc.ID),
	}); err != nil {
		t.Fatal(err)
	}
	out, err := app.toolReportsNetWorth(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	res := out.(map[string]any)
	if res["total"].(int64) != 35000000 {
		t.Errorf("net worth=%v, want 35000000", res["total"])
	}
}

// ─── Reports ─────────────────────────────────────────────────────

func TestUnit_Cashflow_BucketsByMonth(t *testing.T) {
	ctx := newCtx(t)
	app := &App{}
	acc := mustCreateAccount(t, app, ctx, "Wallet", "cash", "EUR", 0)
	// Two income rows in Jan, one expense in Feb.
	mustCreate(t, app, ctx, "income", acc.ID, 100000, "2026-01-05T10:00:00Z")
	mustCreate(t, app, ctx, "income", acc.ID, 50000, "2026-01-25T10:00:00Z")
	mustCreate(t, app, ctx, "expense", acc.ID, -30000, "2026-02-10T10:00:00Z")
	out, err := app.toolReportsCashflow(ctx, map[string]any{
		"from": "2026-01-01T00:00:00Z", "to": "2026-03-01T00:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	res := out.(map[string]any)
	points := res["points"].([]map[string]any)
	if len(points) != 2 {
		t.Fatalf("expected 2 monthly buckets, got %d", len(points))
	}
	if points[0]["income"].(int64) != 150000 {
		t.Errorf("Jan income=%v, want 150000", points[0]["income"])
	}
	if points[1]["expense"].(int64) != -30000 {
		t.Errorf("Feb expense=%v, want -30000", points[1]["expense"])
	}
}

func TestUnit_Allocation_GroupsAndTops(t *testing.T) {
	ctx := newCtx(t)
	app := &App{}
	broker := mustCreateAccount(t, app, ctx, "Broker", "brokerage", "EUR", 0)
	aapl := mustCreateInstrument(t, app, ctx, "stock", "AAPL", "Apple", "EUR")
	msft := mustCreateInstrument(t, app, ctx, "stock", "MSFT", "Microsoft", "EUR")
	_, _ = app.toolTxnsBuy(ctx, map[string]any{
		"account_id": float64(broker.ID), "instrument_id": float64(aapl.ID),
		"quantity": float64(10), "amount": float64(100000),
		"posted_at": "2026-01-15T10:00:00Z",
	})
	_, _ = app.toolTxnsBuy(ctx, map[string]any{
		"account_id": float64(broker.ID), "instrument_id": float64(msft.ID),
		"quantity": float64(5), "amount": float64(50000),
		"posted_at": "2026-01-15T10:00:00Z",
	})
	// Set prices so current value matches cost.
	_, _ = app.toolPricesSet(ctx, map[string]any{"instrument_id": float64(aapl.ID), "price": float64(10000)})
	_, _ = app.toolPricesSet(ctx, map[string]any{"instrument_id": float64(msft.ID), "price": float64(10000)})

	out, err := app.toolReportsAllocation(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	res := out.(map[string]any)
	tops := res["top_instruments"].([]instrumentTotal)
	if len(tops) < 1 || tops[0].Symbol != "AAPL" {
		t.Errorf("expected AAPL as top instrument, got %+v", tops)
	}
}

// ─── CSV import ──────────────────────────────────────────────────

func TestUnit_ImportCSV_HappyPath(t *testing.T) {
	ctx := newCtx(t)
	app := &App{}
	acc := mustCreateAccount(t, app, ctx, "Wallet", "cash", "EUR", 0)
	csv := "Date,Amount,Memo\n2026-01-05,100.00,Salary\n2026-01-15,-12.50,Groceries\n"
	out, err := app.toolImportCSV(ctx, map[string]any{
		"account_id": float64(acc.ID),
		"csv":        csv,
		"mapping":    map[string]any{"date": "Date", "amount": "Amount", "memo": "Memo"},
	})
	if err != nil {
		t.Fatal(err)
	}
	res := out.(map[string]any)
	if res["imported"].(int) != 2 || res["skipped"].(int) != 0 {
		t.Errorf("import: %+v", res)
	}
	bal := mustCashBalance(ctx, acc.ID, 0)
	if bal != 8750 { // 10000 − 1250
		t.Errorf("balance after import=%d, want 8750", bal)
	}
}

func TestUnit_ParseMoneyToMinor(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"12.34", 1234},
		{"12,34", 1234},
		{"1,234.56", 123456},
		{"-5.00", -500},
		{"(5.00)", -500},
		{"1.234,56", 123456}, // EU style
		{"0", 0},
		{"100", 10000},
	}
	for _, c := range cases {
		got, err := parseMoneyToMinor(c.in)
		if err != nil {
			t.Errorf("parseMoneyToMinor(%q) error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("parseMoneyToMinor(%q)=%d, want %d", c.in, got, c.want)
		}
	}
}

// ─── Categories ──────────────────────────────────────────────────

func TestUnit_CategoriesSeed_Idempotent(t *testing.T) {
	ctx := newCtx(t)
	app := &App{}
	r1, err := app.toolCategoriesSeed(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	first := r1.(map[string]any)["created"].(int)
	if first == 0 {
		t.Error("first seed created nothing")
	}
	r2, _ := app.toolCategoriesSeed(ctx, nil)
	if r2.(map[string]any)["created"].(int) != 0 {
		t.Errorf("second seed should be no-op, created %v", r2.(map[string]any)["created"])
	}
}

// ─── Budgets ─────────────────────────────────────────────────────

func TestUnit_BudgetsSet_RequiresAmount(t *testing.T) {
	ctx := newCtx(t)
	app := &App{}
	if _, err := app.toolBudgetsSet(ctx, map[string]any{}); err == nil {
		t.Error("expected error for missing amount")
	}
}

func TestUnit_BudgetsSet_IsUpsert(t *testing.T) {
	ctx := newCtx(t)
	app := &App{}
	cat := mustCategory(t, app, ctx, "Food", "expense", 0)
	r1, err := app.toolBudgetsSet(ctx, map[string]any{
		"category_id": float64(cat.ID), "amount": float64(50000),
	})
	if err != nil {
		t.Fatal(err)
	}
	r2, err := app.toolBudgetsSet(ctx, map[string]any{
		"category_id": float64(cat.ID), "amount": float64(75000),
	})
	if err != nil {
		t.Fatal(err)
	}
	if r1.(Budget).ID != r2.(Budget).ID {
		t.Errorf("expected same row, got %d then %d", r1.(Budget).ID, r2.(Budget).ID)
	}
	if r2.(Budget).Amount != 75000 {
		t.Errorf("amount=%d, want 75000", r2.(Budget).Amount)
	}
	// Only one budget in the list.
	list, _ := app.toolBudgetsList(ctx, nil)
	bs := list.(map[string]any)["budgets"].([]Budget)
	if len(bs) != 1 {
		t.Errorf("expected 1 budget, got %d", len(bs))
	}
}

func TestUnit_BudgetsStatus_RollsUpDescendants(t *testing.T) {
	ctx := newCtx(t)
	app := &App{}
	// Food → Groceries, Food → Restaurants.
	food := mustCategory(t, app, ctx, "Food", "expense", 0)
	gro := mustCategory(t, app, ctx, "Groceries", "expense", food.ID)
	rst := mustCategory(t, app, ctx, "Restaurants", "expense", food.ID)
	other := mustCategory(t, app, ctx, "Transport", "expense", 0)

	acc := mustCreateAccount(t, app, ctx, "Wallet", "cash", "EUR", 0)
	// Spend during May 2026.
	mustCreateCat(t, app, ctx, "expense", acc.ID, -20000, "2026-05-05T10:00:00Z", gro.ID)
	mustCreateCat(t, app, ctx, "expense", acc.ID, -15000, "2026-05-12T10:00:00Z", rst.ID)
	mustCreateCat(t, app, ctx, "expense", acc.ID, -5000, "2026-05-20T10:00:00Z", other.ID) // not Food

	// Spend OUTSIDE the period — must not count.
	mustCreateCat(t, app, ctx, "expense", acc.ID, -99999, "2026-04-15T10:00:00Z", gro.ID)

	if _, err := app.toolBudgetsSet(ctx, map[string]any{
		"category_id": float64(food.ID), "amount": float64(50000),
	}); err != nil {
		t.Fatal(err)
	}
	res, err := app.toolBudgetsStatus(ctx, map[string]any{"as_of": "2026-05-15T00:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	statuses := res.(map[string]any)["budgets"].([]BudgetStatus)
	if len(statuses) != 1 {
		t.Fatalf("expected 1 status row, got %+v", statuses)
	}
	s := statuses[0]
	// Should sum Groceries + Restaurants = 35000, not Transport.
	if s.Spent != 35000 {
		t.Errorf("spent=%d, want 35000 (Groceries+Restaurants in May only)", s.Spent)
	}
	if s.Remaining != 15000 {
		t.Errorf("remaining=%d, want 15000", s.Remaining)
	}
	if s.Over {
		t.Error("not over budget yet")
	}
}

func TestUnit_BudgetsStatus_OverBudget(t *testing.T) {
	ctx := newCtx(t)
	app := &App{}
	cat := mustCategory(t, app, ctx, "Subscriptions", "expense", 0)
	acc := mustCreateAccount(t, app, ctx, "Card", "cash", "EUR", 0)
	mustCreateCat(t, app, ctx, "expense", acc.ID, -12000, "2026-05-05T10:00:00Z", cat.ID)
	_, _ = app.toolBudgetsSet(ctx, map[string]any{
		"category_id": float64(cat.ID), "amount": float64(10000),
	})
	res, _ := app.toolBudgetsStatus(ctx, map[string]any{"as_of": "2026-05-15T00:00:00Z"})
	s := res.(map[string]any)["budgets"].([]BudgetStatus)[0]
	if !s.Over {
		t.Error("expected over=true")
	}
	if s.Remaining != -2000 {
		t.Errorf("remaining=%d, want -2000", s.Remaining)
	}
}

func TestUnit_BudgetsStatus_TotalBudgetCountsUncategorised(t *testing.T) {
	ctx := newCtx(t)
	app := &App{}
	acc := mustCreateAccount(t, app, ctx, "Card", "cash", "EUR", 0)
	// One categorised, one uncategorised expense.
	cat := mustCategory(t, app, ctx, "Food", "expense", 0)
	mustCreateCat(t, app, ctx, "expense", acc.ID, -3000, "2026-05-05T10:00:00Z", cat.ID)
	mustCreate(t, app, ctx, "expense", acc.ID, -2000, "2026-05-10T10:00:00Z") // no category
	// Total budget (NULL category_id) — should pick up BOTH.
	_, _ = app.toolBudgetsSet(ctx, map[string]any{"amount": float64(50000)})
	res, _ := app.toolBudgetsStatus(ctx, map[string]any{"as_of": "2026-05-15T00:00:00Z"})
	s := res.(map[string]any)["budgets"].([]BudgetStatus)[0]
	if s.CategoryID != 0 {
		t.Errorf("expected total-budget (cat=0), got %d", s.CategoryID)
	}
	if s.Spent != 5000 {
		t.Errorf("spent=%d, want 5000 (both cat'd + uncat'd)", s.Spent)
	}
}

func TestUnit_BudgetsStatus_IgnoresNonExpenseKinds(t *testing.T) {
	ctx := newCtx(t)
	app := &App{}
	cat := mustCategory(t, app, ctx, "Travel", "expense", 0)
	acc := mustCreateAccount(t, app, ctx, "Card", "cash", "EUR", 0)
	mustCreateCat(t, app, ctx, "expense", acc.ID, -2000, "2026-05-05T10:00:00Z", cat.ID)
	// A withdraw of equal magnitude should NOT count — cash leaving
	// the account isn't consumption.
	mustCreateCat(t, app, ctx, "withdraw", acc.ID, -10000, "2026-05-06T10:00:00Z", cat.ID)
	_, _ = app.toolBudgetsSet(ctx, map[string]any{
		"category_id": float64(cat.ID), "amount": float64(20000),
	})
	res, _ := app.toolBudgetsStatus(ctx, map[string]any{"as_of": "2026-05-15T00:00:00Z"})
	s := res.(map[string]any)["budgets"].([]BudgetStatus)[0]
	if s.Spent != 2000 {
		t.Errorf("spent=%d, want 2000 (withdraw excluded)", s.Spent)
	}
}

func TestUnit_PeriodBounds(t *testing.T) {
	// Monthly: May 2026 → [May 1, Jun 1).
	at := time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC)
	s, e := periodBounds(at, "monthly")
	if !s.Equal(time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)) || !e.Equal(time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("monthly: got [%v, %v)", s, e)
	}
	// Quarterly: a date in May lands in Q2 (Apr–Jul).
	s, e = periodBounds(at, "quarterly")
	if !s.Equal(time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)) || !e.Equal(time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("quarterly: got [%v, %v)", s, e)
	}
	// Weekly: 2026-05-15 is a Friday → Monday is 2026-05-11.
	s, e = periodBounds(at, "weekly")
	if !s.Equal(time.Date(2026, 5, 11, 0, 0, 0, 0, time.UTC)) || !e.Equal(time.Date(2026, 5, 18, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("weekly: got [%v, %v)", s, e)
	}
}

// ─── HTTP ─────────────────────────────────────────────────────────

func TestHTTP_AccountCreateGetDelete(t *testing.T) {
	srv := newHTTPServer(t)
	defer srv.Close()
	// Create.
	resp, err := http.Post(srv.URL+"/accounts", "application/json", bytes.NewBufferString(
		`{"name":"Test","kind":"cash","currency":"EUR","opening_balance":50000}`))
	must200(t, resp, err)
	var a Account
	_ = json.NewDecoder(resp.Body).Decode(&a)
	resp.Body.Close()
	if a.ID == 0 {
		t.Fatal("no id")
	}
	// Get.
	r2, err := http.Get(srv.URL + "/accounts/" + itoa(a.ID))
	must200(t, r2, err)
	r2.Body.Close()
	// Delete.
	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/accounts/"+itoa(a.ID), nil)
	r3, err := http.DefaultClient.Do(req)
	if err != nil || r3.StatusCode != 204 {
		t.Fatalf("delete failed: %v %v", err, r3)
	}
}

// ─── MANIFEST ─────────────────────────────────────────────────────

func TestManifest_Valid(t *testing.T) {
	app := &App{}
	m := app.Manifest()
	if m.Name != "finance" {
		t.Errorf("manifest name=%q", m.Name)
	}
	tools := app.MCPTools()
	if len(tools) < 20 {
		t.Errorf("expected ≥20 mcp tools, got %d", len(tools))
	}
}

// ─── helpers ─────────────────────────────────────────────────────

func newCtx(t *testing.T) *sdk.AppCtx {
	t.Helper()
	rec := tk.NewEmitRecorder()
	ctx := tk.NewAppCtx(t, "apteva.yaml",
		tk.WithProjectID("test-proj"),
		tk.WithEmitter(rec),
	)
	globalCtx = ctx
	return ctx
}

func newHTTPServer(t *testing.T) *httptest.Server {
	t.Helper()
	newCtx(t)
	app := &App{}
	mux := http.NewServeMux()
	for _, r := range app.HTTPRoutes() {
		method, pattern, handler := r.Method, r.Pattern, r.Handler
		mux.HandleFunc(pattern, func(w http.ResponseWriter, req *http.Request) {
			if method != "" && req.Method != method {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			handler(w, req)
		})
	}
	return httptest.NewServer(mux)
}

func must200(t *testing.T, resp *http.Response, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("HTTP error: %v", err)
	}
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("HTTP %d: %s", resp.StatusCode, body)
	}
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var buf bytes.Buffer
	for n > 0 {
		buf.WriteByte(byte('0' + n%10))
		n /= 10
	}
	b := buf.Bytes()
	for i, j := 0, len(b)-1; i < j; i, j = i+1, j-1 {
		b[i], b[j] = b[j], b[i]
	}
	return string(b)
}

func mustCreateAccount(t *testing.T, app *App, ctx *sdk.AppCtx, name, kind, ccy string, opening int64) Account {
	t.Helper()
	r, err := app.toolAccountsCreate(ctx, map[string]any{
		"name": name, "kind": kind, "currency": ccy, "opening_balance": float64(opening),
	})
	if err != nil {
		t.Fatal(err)
	}
	return r.(Account)
}

func mustCreateInstrument(t *testing.T, app *App, ctx *sdk.AppCtx, kind, symbol, name, quote string) Instrument {
	t.Helper()
	r, err := app.toolInstrumentsCreate(ctx, map[string]any{
		"kind": kind, "symbol": symbol, "name": name, "quote_currency": quote,
	})
	if err != nil {
		t.Fatal(err)
	}
	return r.(Instrument)
}

func mustCreate(t *testing.T, app *App, ctx *sdk.AppCtx, kind string, accID, amount int64, postedAt string) Transaction {
	t.Helper()
	r, err := app.toolTxnsCreate(ctx, map[string]any{
		"account_id": float64(accID), "kind": kind, "amount": float64(amount), "posted_at": postedAt,
	})
	if err != nil {
		t.Fatalf("txn create %s: %v", kind, err)
	}
	return r.(Transaction)
}

func mustCreateCat(t *testing.T, app *App, ctx *sdk.AppCtx, kind string, accID, amount int64, postedAt string, catID int64) Transaction {
	t.Helper()
	args := map[string]any{
		"account_id": float64(accID), "kind": kind, "amount": float64(amount), "posted_at": postedAt,
	}
	if catID > 0 {
		args["category_id"] = float64(catID)
	}
	r, err := app.toolTxnsCreate(ctx, args)
	if err != nil {
		t.Fatalf("txn create %s (cat=%d): %v", kind, catID, err)
	}
	return r.(Transaction)
}

func mustCategory(t *testing.T, app *App, ctx *sdk.AppCtx, name, kind string, parentID int64) Category {
	t.Helper()
	args := map[string]any{"name": name, "kind": kind}
	if parentID > 0 {
		args["parent_id"] = float64(parentID)
	}
	r, err := app.toolCategoriesCreate(ctx, args)
	if err != nil {
		t.Fatalf("category create %s: %v", name, err)
	}
	return r.(Category)
}

// Silence "imported and not used" if a test gets commented out.
var _ = time.RFC3339
