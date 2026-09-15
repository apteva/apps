package main

import (
	"encoding/json"
	"errors"
	sdk "github.com/apteva/app-sdk"
	"testing"
	"time"
)

type enablePlatform struct {
	bankingPlatform
	uid      string
	expired  bool
	repeat   bool
	requests []map[string]any
}

func (p *enablePlatform) ExecuteIntegrationTool(id int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
	var body any
	expiry := time.Now().Add(time.Hour)
	if p.expired {
		expiry = time.Now().Add(-time.Hour)
	}
	switch tool {
	case "get_session":
		body = map[string]any{"status": "AUTHORIZED", "access": map[string]any{"valid_until": expiry.Format(time.RFC3339)}, "accounts": []string{p.uid}, "accounts_data": []any{map[string]any{"uid": p.uid, "identification_hash": "stable-account"}}, "aspsp": map[string]any{"name": "Spanish bank", "country": "ES"}}
	case "get_account_details":
		body = map[string]any{"currency": "EUR", "name": "Current account", "account_id": map[string]any{"iban": "ES0000"}}
	case "get_account_balances":
		body = map[string]any{"balances": []any{map[string]any{"balance_type": "CLAV", "balance_amount": map[string]any{"currency": "EUR", "amount": "200.00"}}, map[string]any{"balance_type": "CLBD", "balance_amount": map[string]any{"currency": "EUR", "amount": "100.00"}}}}
	case "get_account_transactions":
		p.requests = append(p.requests, input)
		if input["continuation_key"] == nil {
			body = map[string]any{"transactions": []any{}, "continuation_key": "next"}
		} else {
			body = map[string]any{"transactions": []any{map[string]any{"entry_reference": "stable-txn", "transaction_id": "unstable-details-id", "transaction_amount": map[string]any{"currency": "EUR", "amount": "12.34"}, "credit_debit_indicator": "DBIT", "status": "BOOK", "booking_date": "2026-09-01", "creditor": map[string]any{"name": "Coffee"}, "remittance_information": []string{"Morning coffee"}}}, "continuation_key": nil}
			if p.repeat {
				body.(map[string]any)["continuation_key"] = "next"
			}
		}
	default:
		return nil, errors.New("unexpected tool " + tool)
	}
	b, _ := json.Marshal(body)
	return &sdk.ExecuteResult{Success: true, Status: 200, Data: b}, nil
}
func TestEnableBanking_DiscoverPaginationSyncAndRenewal(t *testing.T) {
	pf := &enablePlatform{bankingPlatform: bankingPlatform{conn: sdk.PlatformConnection{ID: 44, AppSlug: "enable-banking", Status: "active"}}, uid: "uid-first"}
	ctx := newCtxWithPlatform(t, pf)
	app := &App{}
	args := map[string]any{"connection_id": float64(44), "session_id": "session-1", "import_accounts": true}
	result, err := app.toolBankingDiscover(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	data := result.(map[string]any)
	accs := data["accounts"].([]bankingAccount)
	if len(accs) != 1 || accs[0].BalanceMinor == nil || *accs[0].BalanceMinor != 10000 {
		t.Fatalf("wrong discovery: %+v", accs)
	}
	link := data["linked"].([]bankingLink)[0]
	txns, err := fetchEnableBankingTransactions(ctx, pf.conn, link, "2026-09-01", "2026-09-15")
	if err != nil {
		t.Fatal(err)
	}
	if len(pf.requests) != 2 || len(txns) != 1 || txns[0].ExternalID != "stable-txn" || txns[0].AmountMinor != -1234 {
		t.Fatalf("wrong paginated result: %+v", txns)
	}
	for _, input := range pf.requests {
		if _, ok := input["psu_ip_address"]; ok {
			t.Fatal("background request fabricated a PSU IP")
		}
		if input["transaction_status"] != "BOOK" {
			t.Fatal("requested pending data")
		}
	}
	if _, err = app.toolBankingSync(ctx, map[string]any{"connection_id": float64(44)}); err != nil {
		t.Fatal(err)
	}
	if got := mustCashBalance(ctx, link.Account.ID, 0); got != 10000 {
		t.Fatalf("reconciled against available balance: %d", got)
	}
	// A newly authorized session changes uid, but must keep the account and history.
	pf.uid = "uid-renewed"
	args["session_id"] = "session-2"
	result, err = app.toolBankingDiscover(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	renewed := result.(map[string]any)["linked"].([]bankingLink)[0]
	if renewed.Account.ID != link.Account.ID || renewed.ExternalID != "uid-renewed" {
		t.Fatalf("renewal duplicated account: %+v", renewed)
	}
	out, err := app.toolBankingSync(ctx, map[string]any{"connection_id": float64(44)})
	if err != nil {
		t.Fatal(err)
	}
	if out.(bankingSyncStats).Imported != 0 {
		t.Fatal("renewal duplicated the existing transaction")
	}
}
func TestEnableBanking_ConsentAndPaginationFailures(t *testing.T) {
	pf := &enablePlatform{bankingPlatform: bankingPlatform{conn: sdk.PlatformConnection{ID: 44, AppSlug: "enable-banking", Status: "active"}}, uid: "uid", expired: true}
	ctx := newCtxWithPlatform(t, pf)
	if _, err := discoverEnableBanking(ctx, pf.conn, map[string]any{"session_id": "expired"}); err == nil {
		t.Fatal("expired consent accepted")
	}
	pf.expired = false
	pf.repeat = true
	_, err := fetchEnableBankingTransactions(ctx, pf.conn, bankingLink{ExternalID: "uid", Account: Account{Currency: "EUR"}, Metadata: map[string]any{"session_id": "s"}}, "2026-09-01", "2026-09-15")
	if err == nil {
		t.Fatal("repeated continuation key accepted")
	}
	for _, bad := range []string{"NaN", "1.234", "Inf", "92233720368547758.08"} {
		if _, err := enableMinor(bad, "EUR"); err == nil {
			t.Fatalf("invalid decimal accepted: %s", bad)
		}
	}
	raw := map[string]any{"balances": []any{map[string]any{"balance_type": "CLAV", "balance_amount": map[string]any{"currency": "EUR", "amount": "100"}}}}
	if bal, err := enableBankingBalance(raw, "EUR"); err != nil || bal != nil {
		t.Fatal("available balance used as booked balance")
	}
}
