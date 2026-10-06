package main

import (
	"encoding/json"
	"errors"
	sdk "github.com/apteva/app-sdk"
	"testing"
)

type openIOPlatform struct {
	financeBindingsPlatform
	refreshes                    int
	offsets                      []int
	expired, repeat, failRefresh bool
	amount                       string
}

func (p *openIOPlatform) ExecuteIntegrationTool(id int64, tool string, args map[string]any) (*sdk.ExecuteResult, error) {
	var out any
	switch tool {
	case "list_accounts":
		out = []any{map[string]any{"id": "cash-1", "displayName": "Current", "currency": "EUR", "aspspName": "Bank", "iban": "ES123456", "needsReconnect": p.expired, "balances": []any{map[string]any{"type": "ITAV", "amount": "999.00", "currency": "EUR"}, map[string]any{"type": "ITBD", "amount": "100.00", "currency": "EUR"}}}}
	case "sync_account":
		p.refreshes++
		if p.failRefresh {
			return nil, errors.New("reconnect_needed")
		}
		out = map[string]any{"newTransactions": 2}
	case "list_transactions":
		offset := intArg(args, "offset", 0)
		p.offsets = append(p.offsets, offset)
		id, direction := "expense", "DBIT"
		if offset == 1 && !p.repeat {
			id, direction = "income", "CRDT"
		}
		out = map[string]any{"total": 2, "items": []any{map[string]any{"id": id, "amount": p.amount, "currency": "EUR", "creditDebitIndicator": direction, "bookingDate": "2026-09-01", "status": "BOOK", "creditorName": "Shop", "debtorName": "Employer"}}}
	default:
		return nil, errors.New("unexpected tool " + tool)
	}
	b, _ := json.Marshal(out)
	return &sdk.ExecuteResult{Success: true, Status: 200, Data: b}, nil
}
func openIOFixture(t *testing.T) (*App, *sdk.AppCtx, *openIOPlatform) {
	p := &openIOPlatform{financeBindingsPlatform: financeBindingsPlatform{bindings: map[string]any{financeConnectionRole: float64(7)}, conns: map[int64]sdk.PlatformConnection{7: {ID: 7, AppSlug: "open-banking-io", Status: "active", ProjectID: "test-proj"}}}, amount: "12.34"}
	return &App{}, newCtxWithPlatform(t, p), p
}
func TestOpenIOSharedDiscoveryImportDryRunAndRefresh(t *testing.T) {
	app, ctx, p := openIOFixture(t)
	accounts, err := bankingAdapterFor("open-banking-io").DiscoverAccounts(ctx, p.conns[7], nil)
	if err != nil || len(accounts) != 1 {
		t.Fatal(accounts, err)
	}
	if accounts[0].BalanceMinor == nil || *accounts[0].BalanceMinor != 10000 || accounts[0].Mask != "3456" {
		t.Fatal(accounts)
	}
	_, err = app.toolBankingLinkAccount(ctx, map[string]any{"connection_id": 7, "external_account_id": "cash-1"})
	if err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"connection_id": 7, "from": "2026-09-01", "to": "2026-09-15", "dry_run": true}
	if _, err = app.toolBankingSync(ctx, args); err != nil {
		t.Fatal(err)
	}
	if p.refreshes != 0 {
		t.Fatal("dry run refreshed upstream")
	}
	args["dry_run"] = false
	out, err := app.toolBankingSync(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	stats := out.(bankingSyncStats)
	if stats.Imported != 2 || len(stats.Errors) != 0 || p.refreshes != 1 {
		t.Fatal(stats, p.refreshes)
	}
	out, err = app.toolBankingSync(ctx, args)
	if err != nil || out.(bankingSyncStats).Imported != 0 {
		t.Fatal("duplicate import", out, err)
	}
	var debit, credit int64
	ctx.AppDB().QueryRow(`SELECT amount FROM transactions WHERE external_id='open-banking-io:bank:expense'`).Scan(&debit)
	ctx.AppDB().QueryRow(`SELECT amount FROM transactions WHERE external_id='open-banking-io:bank:income'`).Scan(&credit)
	if debit != -1234 || credit != 1234 {
		t.Fatal(debit, credit)
	}
	if len(p.offsets) < 2 || p.offsets[0] != 0 || p.offsets[1] != 1 {
		t.Fatal(p.offsets)
	}
	if _, err = app.toolBankingPaymentPrepare(ctx, map[string]any{"connection_id": 7, "request_key": "no-payment", "amount": 100}); err == nil {
		t.Fatal("data-only provider accepted payment")
	}
}
func TestOpenIORejectsExpiredConsentAndInvalidPages(t *testing.T) {
	app, ctx, p := openIOFixture(t)
	p.expired = true
	if _, err := app.toolBankingDiscover(ctx, map[string]any{"connection_id": 7, "import_accounts": true}); err == nil {
		t.Fatal("imported expired consent")
	}
	p.expired = false
	link, err := linkBankingAccount(ctx, "open-banking-io", p.conns[7], bankingAccount{ExternalID: "cash-1", Name: "Current", Currency: "EUR", Kind: "cash"}, 0, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	p.repeat = true
	if _, err = (openBankingIOAdapter{}).FetchTransactions(ctx, p.conns[7], link, "2026-09-01", "2026-09-15"); err == nil {
		t.Fatal("repeated page accepted")
	}
	p.repeat = false
	p.amount = "0.001"
	if _, err = (openBankingIOAdapter{}).FetchTransactions(ctx, p.conns[7], link, "2026-09-01", "2026-09-15"); err == nil {
		t.Fatal("rounded fractional cents")
	}
	p.failRefresh = true
	p.offsets = nil
	out, err := app.toolBankingSync(ctx, map[string]any{"connection_id": 7})
	if err == nil || len(p.offsets) != 0 {
		t.Fatal("imported after failed upstream refresh", out)
	}
}
