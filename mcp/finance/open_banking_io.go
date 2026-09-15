package main

import (
	"errors"
	"fmt"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// Uses the shared banking adapter contract. Credentials and decryption remain
// in the integration service; Finance receives only the SDK's normalized data.
type openBankingIOAdapter struct{}

func (openBankingIOAdapter) DiscoverAccounts(ctx *sdk.AppCtx, conn sdk.PlatformConnection, _ map[string]any) ([]bankingAccount, error) {
	var raw []map[string]any
	if err := executeIntegrationJSON(ctx, conn.ID, "list_accounts", nil, &raw); err != nil {
		return nil, err
	}
	out := []bankingAccount{}
	for _, item := range raw {
		id, currency := firstString(item, "id"), strings.ToUpper(firstString(item, "currency"))
		if id == "" || len(currency) != 3 {
			return nil, errors.New("open-banking.io returned an account without an ID or currency")
		}
		balance, err := openBankingIOBalance(item, currency)
		if err != nil {
			return nil, err
		}
		iban := firstString(item, "iban", "bban")
		mask := iban
		if len(mask) > 4 {
			mask = mask[len(mask)-4:]
		}
		name := firstString(item, "displayName", "accountName", "ownerName", "product")
		if name == "" {
			name = "Bank account"
		}
		out = append(out, bankingAccount{ExternalID: id, Name: name, Currency: currency, Kind: "cash", Institution: firstString(item, "aspspName"), Mask: mask, BalanceMinor: balance, NeedsReconnect: boolFromAny(item["needsReconnect"])})
	}
	return out, nil
}
func openBankingIOBalance(item map[string]any, currency string) (*int64, error) {
	// Reconcile booked funds, never an available balance including holds/overdraft.
	for _, kind := range []string{"ITBD", "CLBD"} {
		for _, b := range mapsFromArray(arrayAny(item["balances"])) {
			if firstString(b, "type") != kind {
				continue
			}
			if firstString(b, "currency") != currency {
				return nil, errors.New("bank balance currency mismatch")
			}
			n, err := enableMinor(firstString(b, "amount"), currency)
			if err != nil {
				return nil, err
			}
			return &n, nil
		}
	}
	return nil, nil
}
func (a openBankingIOAdapter) account(ctx *sdk.AppCtx, conn sdk.PlatformConnection, id string) (bankingAccount, error) {
	accounts, err := a.DiscoverAccounts(ctx, conn, nil)
	if err != nil {
		return bankingAccount{}, err
	}
	for _, account := range accounts {
		if account.ExternalID == id {
			if account.NeedsReconnect {
				return bankingAccount{}, errors.New("bank consent expired: reconnect this bank at open-banking.io, then discover accounts again")
			}
			return account, nil
		}
	}
	return bankingAccount{}, errors.New("linked account no longer exists at open-banking.io; discover and relink the account")
}
func (a openBankingIOAdapter) RefreshAccount(ctx *sdk.AppCtx, conn sdk.PlatformConnection, link bankingLink) error {
	if _, err := a.account(ctx, conn, link.ExternalID); err != nil {
		return err
	}
	var raw map[string]any
	return executeIntegrationJSON(ctx, conn.ID, "sync_account", map[string]any{"account_id": link.ExternalID}, &raw)
}
func (a openBankingIOAdapter) FetchBalance(ctx *sdk.AppCtx, conn sdk.PlatformConnection, link bankingLink) (*int64, error) {
	account, err := a.account(ctx, conn, link.ExternalID)
	if err != nil {
		return nil, err
	}
	if account.Currency != link.Account.Currency {
		return nil, errors.New("linked bank account currency changed")
	}
	return account.BalanceMinor, nil
}
func (a openBankingIOAdapter) FetchTransactions(ctx *sdk.AppCtx, conn sdk.PlatformConnection, link bankingLink, from, to string) ([]bankingTxn, error) {
	account, err := a.account(ctx, conn, link.ExternalID)
	if err != nil {
		return nil, err
	}
	if account.Currency != link.Account.Currency {
		return nil, errors.New("linked bank account currency changed")
	}
	out := []bankingTxn{}
	seen := map[string]bool{}
	offset := 0
	expectedTotal := -1
	for page := 0; page < 1000; page++ {
		var raw struct {
			Items []map[string]any `json:"items"`
			Total *int             `json:"total"`
		}
		if err := executeIntegrationJSON(ctx, conn.ID, "list_transactions", map[string]any{"account_id": link.ExternalID, "from": from, "to": to, "limit": 500, "offset": offset}, &raw); err != nil {
			return nil, err
		}
		if raw.Total == nil || *raw.Total < 0 {
			return nil, errors.New("open-banking.io transaction page has no valid total")
		}
		if expectedTotal < 0 {
			expectedTotal = *raw.Total
		}
		if *raw.Total != expectedTotal || offset+len(raw.Items) > *raw.Total {
			return nil, errors.New("transaction pages changed during sync; retry sync")
		}
		if len(raw.Items) == 0 && offset < *raw.Total {
			return nil, errors.New("open-banking.io returned an incomplete transaction page; retry sync")
		}
		for _, item := range raw.Items {
			id := firstString(item, "id")
			if id == "" || seen[id] {
				return nil, errors.New("open-banking.io returned missing or repeated transaction IDs; retry sync")
			}
			seen[id] = true
			currency := strings.ToUpper(firstString(item, "currency"))
			if currency != account.Currency {
				return nil, errors.New("bank transaction currency mismatch")
			}
			n, err := enableMinor(firstString(item, "amount"), currency)
			if err != nil {
				return nil, err
			}
			direction := strings.ToUpper(firstString(item, "creditDebitIndicator"))
			switch direction {
			case "DBIT":
				if n > 0 {
					n = -n
				}
			case "CRDT":
				if n < 0 {
					return nil, errors.New("negative credit transaction")
				}
			default:
				return nil, errors.New("bank transaction has no valid credit/debit indicator")
			}
			date := firstString(item, "bookingDate", "valueDate", "transactionDate")
			if _, err := parseFlexibleTime(date); err != nil {
				return nil, fmt.Errorf("bank transaction date: %w", err)
			}
			status := strings.ToUpper(firstString(item, "status"))
			pending := status == "PDNG" || status == "PENDING"
			if status != "" && status != "BOOK" && status != "BOOKED" && !pending {
				return nil, fmt.Errorf("unsupported bank transaction status %q", status)
			}
			payee := firstString(item, "creditorName")
			if direction == "CRDT" {
				payee = firstString(item, "debtorName")
			}
			out = append(out, bankingTxn{ExternalID: id, AccountExternalID: link.ExternalID, PostedAt: date, AmountMinor: n, Currency: currency, Payee: payee, Memo: firstString(item, "remittanceInformation", "note", "referenceNumber"), Pending: pending})
		}
		offset += len(raw.Items)
		if offset >= *raw.Total {
			return out, nil
		}
	}
	return nil, errors.New("transaction pagination limit exceeded; choose a smaller date range")
}
