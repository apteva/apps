package main

import (
	"fmt"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

type partialFillPlatform struct {
	financeBindingsPlatform
}

type positionCurrencyPlatform struct {
	financeBindingsPlatform
}

func (p *positionCurrencyPlatform) ExecuteIntegrationTool(_ int64, tool string, _ map[string]any) (*sdk.ExecuteResult, error) {
	if tool != "get_positions" {
		return nil, fmt.Errorf("unexpected integration tool %s", tool)
	}
	return &sdk.ExecuteResult{Success: true, Status: 200, Data: []byte(`[
		{"instrument":{"ticker":"TST_US_EQ","name":"Test Stock","currency":"USD"},
		 "quantity":2,"averagePricePaid":60,"currentPrice":70,
		 "walletImpact":{"currency":"EUR","totalCost":90,"currentValue":100}}
	]`)}, nil
}

func TestTrading212PositionUsesWalletCurrency(t *testing.T) {
	platform := &positionCurrencyPlatform{financeBindingsPlatform: financeBindingsPlatform{
		bindings: map[string]any{financeConnectionRole: float64(7)},
		conns:    map[int64]sdk.PlatformConnection{7: {ID: 7, AppSlug: "trading212", Status: "active"}},
	}}
	ctx := newCtxWithPlatform(t, platform)
	app := &App{}
	account := mustCreateAccount(t, app, ctx, "Broker", "brokerage", "EUR", 0)
	usd := mustCreateInstrument(t, app, ctx, "stock", "TST", "Test Stock", "USD")
	imported, _, err := syncTrading212Positions(ctx, app, account, 7, false)
	if err != nil || imported != 1 {
		t.Fatalf("positions imported=%d: %v", imported, err)
	}
	var quoteCurrency string
	var instrumentID, priceMinor int64
	if err := ctx.AppDB().QueryRow(`SELECT h.instrument_id, i.quote_currency, p.price FROM holdings h JOIN instruments i ON i.id=h.instrument_id JOIN prices p ON p.instrument_id=h.instrument_id WHERE h.account_id=?`, account.ID).Scan(&instrumentID, &quoteCurrency, &priceMinor); err != nil {
		t.Fatal(err)
	}
	if instrumentID == usd.ID || quoteCurrency != "EUR" || priceMinor != 5000 || mustHoldingsValue(ctx, account.ID, "EUR") != 10000 {
		t.Fatalf("instrument=%d currency=%s price=%d value=%d", instrumentID, quoteCurrency, priceMinor, mustHoldingsValue(ctx, account.ID, "EUR"))
	}
}

func (p *partialFillPlatform) ExecuteIntegrationTool(_ int64, tool string, _ map[string]any) (*sdk.ExecuteResult, error) {
	if tool != "get_order_history" {
		return nil, fmt.Errorf("unexpected integration tool %s", tool)
	}
	return &sdk.ExecuteResult{Success: true, Status: 200, Data: []byte(`{
		"items": [
			{"order":{"id":42,"createdAt":"2026-09-27T09:59:00Z","status":"FILLED","side":"BUY","currency":"EUR","instrument":{"ticker":"TST_US_EQ","name":"Test Stock","currency":"USD"}},"fill":{"id":1002,"filledAt":"2026-09-27T10:01:00Z","quantity":2,"price":10,"walletImpact":{"currency":"EUR","netValue":20}}},
			{"order":{"id":42,"createdAt":"2026-09-27T09:59:00Z","status":"FILLED","side":"BUY","currency":"EUR","instrument":{"ticker":"TST_US_EQ","name":"Test Stock","currency":"USD"}},"fill":{"id":1001,"filledAt":"2026-09-27T10:00:00Z","quantity":1,"price":10,"walletImpact":{"currency":"EUR","netValue":10}}}
		]
	}`)}, nil
}

func TestTrading212PartialFillsImportAndLegacyMigration(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprintf("legacy=%t", legacy), func(t *testing.T) {
			platform := &partialFillPlatform{financeBindingsPlatform: financeBindingsPlatform{
				bindings: map[string]any{financeConnectionRole: float64(7)},
				conns:    map[int64]sdk.PlatformConnection{7: {ID: 7, AppSlug: "trading212", Status: "active"}},
			}}
			ctx := newCtxWithPlatform(t, platform)
			app := &App{}
			account := mustCreateAccount(t, app, ctx, "Broker", "brokerage", "EUR", 0)
			if legacy {
				instrument := mustCreateInstrument(t, app, ctx, "stock", "TST", "Test Stock", "EUR")
				_, err := app.toolTxnsBuy(ctx, map[string]any{
					"account_id": float64(account.ID), "instrument_id": float64(instrument.ID),
					"quantity": float64(1), "amount": float64(1000),
					"posted_at": "2026-09-27T09:59:00Z", "external_id": "trading212:order:42",
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			wantImported := 2
			if legacy {
				wantImported = 1
			}
			imported, _, err := syncTrading212Orders(ctx, app, account, 7, false)
			if err != nil || imported != wantImported {
				t.Fatalf("imported=%d, want %d: %v", imported, wantImported, err)
			}
			var count, amount int64
			if err := ctx.AppDB().QueryRow(`SELECT COUNT(*), COALESCE(SUM(amount),0) FROM transactions WHERE account_id=? AND kind='buy'`, account.ID).Scan(&count, &amount); err != nil {
				t.Fatal(err)
			}
			if count != 2 || amount != -3000 {
				t.Fatalf("buys count=%d amount=%d, want 2 and -3000", count, amount)
			}
			var newIDs int
			if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM transactions WHERE account_id=? AND external_id IN ('trading212:fill:1001','trading212:fill:1002')`, account.ID).Scan(&newIDs); err != nil {
				t.Fatal(err)
			}
			if newIDs != 2 {
				t.Fatalf("fill identity count=%d, want 2", newIDs)
			}
			imported, _, err = syncTrading212Orders(ctx, app, account, 7, false)
			if err != nil || imported != 0 {
				t.Fatalf("repeat sync imported=%d: %v", imported, err)
			}
		})
	}
}

func TestDividendImportDoesNotFetchHoldingQuotes(t *testing.T) {
	platform := &stockQuotePlatform{quote: map[string]any{"price": 100, "currency": "EUR"}}
	ctx := newCtxWithPlatform(t, platform)
	app := &App{}
	account := mustCreateAccount(t, app, ctx, "Broker", "brokerage", "EUR", 0)
	instrument := mustCreateInstrument(t, app, ctx, "stock", "TST", "Test Stock", "EUR")
	result, err := ctx.AppDB().Exec(`INSERT INTO holdings (account_id, instrument_id, quantity) VALUES (?, ?, ?)`, account.ID, instrument.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	holdingID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := insertTxn(ctx, txnIn{
		AccountID: account.ID,
		HoldingID: holdingID,
		PostedAt:  "2026-09-28T00:00:00Z",
		Kind:      "dividend",
		Amount:    100,
		Currency:  "EUR",
	}); err != nil {
		t.Fatal(err)
	}
	if platform.calls != 0 {
		t.Fatalf("dividend insertion fetched %d stock quotes", platform.calls)
	}
	other := mustCreateAccount(t, app, ctx, "Other", "brokerage", "EUR", 0)
	_, err = insertTxn(ctx, txnIn{AccountID: other.ID, HoldingID: holdingID, PostedAt: "2026-09-28T00:00:00Z", Kind: "dividend", Amount: 100, Currency: "EUR"})
	if err == nil || err.Error() != "holding belongs to a different account" {
		t.Fatalf("ownership validation failed: %v", err)
	}
}
