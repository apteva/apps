package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func TestAudit_ProjectIsolation(t *testing.T) {
	ctx := newCtx(t)
	other := ctx.WithProject("other-project")
	app := &App{}
	own := mustCreateAccount(t, app, ctx, "Mine", "cash", "EUR", 10000)
	foreign := mustCreateAccount(t, app, other, "Private", "cash", "EUR", 50000)
	inst := mustCreateInstrument(t, app, other, "real_estate", "HOME", "Private house", "EUR")
	txOut, err := app.toolTxnsCreate(other, map[string]any{"account_id": float64(foreign.ID), "kind": "income", "amount": float64(2000), "posted_at": "2026-09-01"})
	if err != nil {
		t.Fatal(err)
	}
	txn := txOut.(Transaction)
	catOut, err := app.toolCategoriesCreate(other, map[string]any{"name": "Private", "kind": "expense"})
	if err != nil {
		t.Fatal(err)
	}
	cat := catOut.(Category)
	cases := []struct {
		name string
		fn   func(*sdk.AppCtx, map[string]any) (any, error)
		args map[string]any
	}{
		{"read account", app.toolAccountsGet, map[string]any{"id": float64(foreign.ID)}},
		{"update account", app.toolAccountsUpdate, map[string]any{"id": float64(foreign.ID), "name": "Hacked"}},
		{"delete account", app.toolAccountsDelete, map[string]any{"id": float64(foreign.ID)}},
		{"read transaction", app.toolTxnsGet, map[string]any{"id": float64(txn.ID)}},
		{"update transaction", app.toolTxnsUpdate, map[string]any{"id": float64(txn.ID), "memo": "Hacked"}},
		{"delete transaction", app.toolTxnsDelete, map[string]any{"id": float64(txn.ID)}},
		{"private instrument", app.toolInstrumentsGet, map[string]any{"id": float64(inst.ID)}},
		{"private instrument write", app.toolPricesSet, map[string]any{"instrument_id": float64(inst.ID), "price": float64(500)}},
		{"private holding", app.toolHoldingsSet, map[string]any{"account_id": float64(own.ID), "instrument_id": float64(inst.ID), "quantity": float64(1)}},
		{"category parent", app.toolCategoriesCreate, map[string]any{"name": "Child", "kind": "expense", "parent_id": float64(cat.ID)}},
		{"budget category", app.toolBudgetsSet, map[string]any{"category_id": float64(cat.ID), "amount": float64(100)}},
		{"transfer", app.toolTxnsTransfer, map[string]any{"from_account_id": float64(own.ID), "to_account_id": float64(foreign.ID), "amount": float64(100), "posted_at": "2026-09-01"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := c.fn(ctx, c.args); err == nil {
				t.Fatal("cross-project operation accepted")
			}
		})
	}
	got, err := readAccount(other, foreign.ID)
	if err != nil || got.Name != "Private" {
		t.Fatalf("foreign account changed: %+v %v", got, err)
	}
	out, err := app.toolTxnsList(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.(map[string]any)["transactions"].([]Transaction)) != 0 {
		t.Fatal("foreign transactions leaked")
	}
	if _, err = app.toolHoldingsSet(other, map[string]any{"account_id": float64(foreign.ID), "instrument_id": float64(inst.ID), "quantity": float64(1), "cost_basis": float64(10000)}); err != nil {
		t.Fatal(err)
	}
	hs, err := listHoldingsRich(ctx, 0, 0, true)
	if err != nil || len(hs) != 0 {
		t.Fatalf("foreign holdings leaked: %+v %v", hs, err)
	}
	perf, err := app.toolReportsPerformance(ctx, map[string]any{"from": "2026-01-01", "to": "2027-01-01"})
	if err != nil {
		t.Fatal(err)
	}
	if perf.(map[string]any)["cost_basis_total"].(int64) != 0 {
		t.Fatal("foreign cost basis included")
	}
	cf, err := app.toolReportsCashflow(ctx, map[string]any{"from": "2026-01-01", "to": "2027-01-01"})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(cf)
	if strings.Contains(string(b), "2000") {
		t.Fatalf("foreign income leaked: %s", b)
	}
}

func TestAudit_CategoryCycleAndClear(t *testing.T) {
	ctx := newCtx(t)
	app := &App{}
	a, e := app.toolCategoriesCreate(ctx, map[string]any{"name": "A", "kind": "expense"})
	if e != nil {
		t.Fatal(e)
	}
	aid := a.(Category).ID
	b, e := app.toolCategoriesCreate(ctx, map[string]any{"name": "B", "kind": "expense", "parent_id": float64(aid)})
	if e != nil {
		t.Fatal(e)
	}
	bid := b.(Category).ID
	if _, e = app.toolCategoriesUpdate(ctx, map[string]any{"id": float64(aid), "parent_id": float64(bid)}); e == nil {
		t.Fatal("cycle accepted")
	}
	result, e := app.toolCategoriesUpdate(ctx, map[string]any{"id": float64(bid), "parent_id": float64(0)})
	if e != nil || result.(Category).ParentID != 0 {
		t.Fatalf("cannot clear parent: %v %v", result, e)
	}
}

func TestAudit_BankingPendingAndAtomicImport(t *testing.T) {
	ctx := newCtx(t)
	app := &App{}
	acc := mustCreateAccount(t, app, ctx, "Bank", "cash", "EUR", 0)
	link := bankingLink{Account: acc, ExternalID: "bank1"}
	bt := bankingTxn{ExternalID: "pending1", AccountExternalID: "bank1", PostedAt: "2026-09-01", AmountMinor: -1200, Currency: "EUR", Pending: true}
	n, _, err := importBankingTxn(ctx, "teller", 1, link, bt, false)
	if err != nil || n != 0 {
		t.Fatal(n, err)
	}
	if mustCashBalance(ctx, acc.ID, 0) != 0 {
		t.Fatal("pending changed booked cash")
	}
	bt.Pending = false
	bt.ExternalID = "posted1"
	_, err = ctx.AppDB().Exec(`CREATE TRIGGER reject_link BEFORE INSERT ON external_links BEGIN SELECT RAISE(ABORT,'mapping failed'); END`)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = importBankingTxn(ctx, "teller", 1, link, bt, false); err == nil {
		t.Fatal("expected failed mapping")
	}
	if mustCashBalance(ctx, acc.ID, 0) != 0 {
		t.Fatal("partial import committed")
	}
	if _, err = ctx.AppDB().Exec(`DROP TRIGGER reject_link`); err != nil {
		t.Fatal(err)
	}
	n, _, err = importBankingTxn(ctx, "teller", 1, link, bt, false)
	if err != nil || n != 1 {
		t.Fatal(n, err)
	}
	second := mustCreateAccount(t, app, ctx, "Bank2", "cash", "EUR", 0)
	link.Account = second
	link.ExternalID = "bank2"
	bt.AccountExternalID = "bank2"
	n, _, err = importBankingTxn(ctx, "teller", 1, link, bt, false)
	if err != nil || n != 1 {
		t.Fatalf("account-local transaction ID lost: %d %v", n, err)
	}
	bt.ExternalID = "wrong-currency"
	bt.Currency = "USD"
	if _, _, err = importBankingTxn(ctx, "teller", 1, link, bt, false); err == nil {
		t.Fatal("currency mismatch accepted")
	}
}

func TestAudit_ReconcileRepeatedSameDay(t *testing.T) {
	ctx := newCtx(t)
	app := &App{}
	acc := mustCreateAccount(t, app, ctx, "Bank", "cash", "EUR", 0)
	for _, balance := range []int64{10000, 12000, 12000, 8000} {
		if _, _, err := reconcileBankingBalance(ctx, "saltedge", 1, acc, balance, false); err != nil {
			t.Fatal(err)
		}
		if got := mustCashBalance(ctx, acc.ID, 0); got != balance {
			t.Fatalf("balance=%d want %d", got, balance)
		}
	}
	if _, _, err := reconcileBankingBalance(ctx, "saltedge", 1, acc, 99999, true); err != nil {
		t.Fatal(err)
	}
	if got := mustCashBalance(ctx, acc.ID, 0); got != 8000 {
		t.Fatal("dry run mutated balance")
	}
}

type auditBankPlatform struct {
	bankingPlatform
	failTransactions, failBalance bool
	balance                       float64
	called                        string
}

func (p *auditBankPlatform) ExecuteIntegrationTool(id int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
	p.called = tool
	if tool == "list_transactions" && p.failTransactions {
		return nil, errors.New("transactions unavailable")
	}
	if tool == "get_account_balances" && p.failBalance {
		return nil, errors.New("balance unavailable")
	}
	if tool == "get_account" {
		b, _ := json.Marshal(map[string]any{"data": map[string]any{"id": "bank1", "balance": p.balance, "currency": "EUR"}})
		return &sdk.ExecuteResult{Success: true, Data: b}, nil
	}
	return p.bankingPlatform.ExecuteIntegrationTool(id, tool, input)
}
func TestAudit_DrySyncDoesNotWriteErrors(t *testing.T) {
	pf := &auditBankPlatform{bankingPlatform: bankingPlatform{conn: sdk.PlatformConnection{ID: 44, AppSlug: "teller", Status: "active"}}, failTransactions: true}
	ctx := newCtxWithPlatform(t, pf)
	app := &App{}
	out, e := app.toolBankingLinkAccount(ctx, map[string]any{"connection_id": float64(44), "external_account_id": "acc_1"})
	if e != nil {
		t.Fatal(e)
	}
	acc := out.(bankingLink).Account
	if _, e = ctx.AppDB().Exec(`UPDATE accounts SET sync_error='old error' WHERE id=?`, acc.ID); e != nil {
		t.Fatal(e)
	}
	_, _ = app.toolBankingSync(ctx, map[string]any{"connection_id": float64(44), "dry_run": true})
	var msg string
	if e = ctx.AppDB().QueryRow(`SELECT sync_error FROM accounts WHERE id=?`, acc.ID).Scan(&msg); e != nil {
		t.Fatal(e)
	}
	if msg != "old error" {
		t.Fatalf("dry run wrote error: %s", msg)
	}
	pf.failTransactions = false
	pf.failBalance = true
	_, _ = app.toolBankingSync(ctx, map[string]any{"connection_id": float64(44)})
	if e = ctx.AppDB().QueryRow(`SELECT sync_error FROM accounts WHERE id=?`, acc.ID).Scan(&msg); e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(msg, "balance unavailable") {
		t.Fatalf("sync cleared balance failure: %s", msg)
	}
}
func TestAudit_FreshSaltEdgeBalanceAndEmptyResults(t *testing.T) {
	pf := &auditBankPlatform{balance: 123.45}
	ctx := newCtxWithPlatform(t, pf)
	bal, err := (genericBankingAdapter{provider: "saltedge"}).FetchBalance(ctx, sdk.PlatformConnection{ID: 1}, bankingLink{Account: Account{Currency: "EUR"}, ExternalID: "bank1", Metadata: map[string]any{"balance_minor": float64(1)}})
	if err != nil || bal == nil || *bal != 12345 || pf.called != "get_account" {
		t.Fatalf("stale balance: %v %v %s", bal, err, pf.called)
	}
	for _, raw := range []any{map[string]any{"transactions": []any{}}, map[string]any{"results": []any{}}, map[string]any{"transactions": map[string]any{"booked": []any{}, "pending": []any{}}}} {
		if got := normalizeBankingTxns("plaid", "bank1", raw); len(got) != 0 {
			t.Fatalf("phantom transaction: %+v", got)
		}
	}
	link := bankingLink{Metadata: map[string]any{"access_token": "secret-token"}}
	b, _ := json.Marshal(link)
	if strings.Contains(string(b), "secret-token") {
		t.Fatal("bank credential leaked into API response")
	}
}

func TestAudit_ProviderDatesAndDebitDirection(t *testing.T) {
	for _, tc := range []struct {
		provider string
		raw      map[string]any
		amount   int64
	}{
		{"nordigen", map[string]any{"transactionId": "1", "bookingDate": "2026-09-01", "transactionAmount": map[string]any{"amount": "-12.34", "currency": "EUR"}}, -1234},
		{"saltedge", map[string]any{"id": "1", "made_on": "2026-09-01", "amount": -12.34, "currency_code": "EUR"}, -1234},
		{"truelayer", map[string]any{"transaction_id": "1", "timestamp": "2026-09-01T01:00:00Z", "amount": 12.34, "transaction_type": "DEBIT"}, -1234},
	} {
		txs := normalizeBankingTxns(tc.provider, "bank1", []any{tc.raw})
		if len(txs) != 1 || txs[0].PostedAt == "" || txs[0].AmountMinor != tc.amount {
			t.Fatalf("%s normalization: %+v", tc.provider, txs)
		}
	}
	if got := bankingTxnKind(bankingTxn{Payee: "Coffee shop", AmountMinor: -100}); got != "expense" {
		t.Fatalf("coffee classified as %s", got)
	}
	if got := bankingTxnKind(bankingTxn{Payee: "Tax refund", AmountMinor: 100}); got != "income" {
		t.Fatalf("refund classified as %s", got)
	}
}

func TestAudit_MissingFXCannotProduceOneToOneTotals(t *testing.T) {
	ctx := newCtx(t)
	app := &App{}
	mustCreateAccount(t, app, ctx, "USD cash", "cash", "USD", 10000)
	if _, err := app.toolReportsNetWorth(ctx, nil); err == nil || !strings.Contains(err.Error(), "exchange rate") {
		t.Fatalf("missing FX silently accepted: %v", err)
	}
	if _, err := app.toolFXSet(ctx, map[string]any{"base": "USD", "quote": "EUR", "rate": float64(0.9)}); err != nil {
		t.Fatal(err)
	}
	out, err := app.toolReportsNetWorth(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := out.(map[string]any)["total"].(int64); got != 9000 {
		t.Fatalf("converted total=%d", got)
	}
}
