package main

import (
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"math/big"
	"regexp"
	"strings"
	"time"
)

func enableSession(ctx *sdk.AppCtx, conn sdk.PlatformConnection, id string) (map[string]any, error) {
	if id == "" {
		return nil, errors.New("session_id required: complete Enable Banking authorization and exchange the verified callback code using authorize_session first")
	}
	var session map[string]any
	if err := executeIntegrationJSON(ctx, conn.ID, "get_session", map[string]any{"session_id": id}, &session); err != nil {
		return nil, err
	}
	if firstString(session, "status") != "AUTHORIZED" {
		return nil, errors.New("Enable Banking session is not authorized; renew bank consent")
	}
	expiry, err := time.Parse(time.RFC3339, firstString(session, "access.valid_until"))
	if err != nil || !expiry.After(time.Now()) {
		return nil, errors.New("Enable Banking session expired or has no valid expiry; renew bank consent")
	}
	return session, nil
}

func enableAccountIDs(session map[string]any) []string {
	ids := []string{}
	seen := map[string]bool{}
	for _, v := range arrayAny(session["accounts"]) {
		id := stringAny(v)
		if m := asMap(v); m != nil {
			id = firstString(m, "uid")
		}
		if id != "" && !seen[id] {
			ids = append(ids, id)
			seen[id] = true
		}
	}
	for _, m := range mapsFromArray(arrayAny(session["accounts_data"])) {
		id := firstString(m, "uid")
		if id != "" && !seen[id] {
			ids = append(ids, id)
			seen[id] = true
		}
	}
	return ids
}

func discoverEnableBanking(ctx *sdk.AppCtx, conn sdk.PlatformConnection, args map[string]any) ([]bankingAccount, error) {
	session, err := enableSession(ctx, conn, strArg(args, "session_id", ""))
	if err != nil {
		return nil, err
	}
	out := []bankingAccount{}
	for _, id := range enableAccountIDs(session) {
		var details map[string]any
		if err = executeIntegrationJSON(ctx, conn.ID, "get_account_details", map[string]any{"account_id": id}, &details); err != nil {
			return nil, err
		}
		currency := strings.ToUpper(firstString(details, "currency"))
		if currency == "" {
			return nil, fmt.Errorf("Enable Banking account %s has no currency", id)
		}
		name := firstString(details, "name", "details", "account_id.iban")
		if name == "" {
			name = "Bank account"
		}
		var raw map[string]any
		if err = executeIntegrationJSON(ctx, conn.ID, "get_account_balances", map[string]any{"account_id": id}, &raw); err != nil {
			return nil, err
		}
		balance, err := enableBankingBalance(raw, currency)
		if err != nil {
			return nil, err
		}
		identification := firstString(details, "identification_hash")
		for _, m := range mapsFromArray(arrayAny(session["accounts_data"])) {
			if firstString(m, "uid") == id && identification == "" {
				identification = firstString(m, "identification_hash")
			}
		}
		details["identification_hash"] = identification
		out = append(out, bankingAccount{ExternalID: id, Name: name, Currency: currency, Kind: "cash", Institution: firstString(session, "aspsp.name"), BalanceMinor: balance, Raw: details})
	}
	return out, nil
}

// The ledger currently uses hundredths throughout its UI and FX model. Reject
// other exponents here rather than silently misrepresent an imported balance.
var bankDecimalPattern = regexp.MustCompile(`^-?[0-9]+(?:\.[0-9]+)?$`)

func enableMinor(amount, currency string) (int64, error) {
	if len(amount) > 40 || !bankDecimalPattern.MatchString(amount) {
		return 0, errors.New("invalid bank decimal amount")
	}
	switch currency {
	case "BHD", "IQD", "JOD", "KWD", "LYD", "OMR", "TND", "CLF", "BIF", "CLP", "DJF", "GNF", "ISK", "JPY", "KMF", "KRW", "PYG", "RWF", "UGX", "UYI", "VND", "VUV", "XAF", "XOF", "XPF":
		return 0, fmt.Errorf("Finance banking imports do not yet support the minor-unit scale of %s", currency)
	}
	r, ok := new(big.Rat).SetString(amount)
	if !ok {
		return 0, errors.New("invalid Enable Banking decimal amount")
	}
	r.Mul(r, big.NewRat(100, 1))
	if !r.IsInt() || !r.Num().IsInt64() {
		return 0, errors.New("bank amount cannot be represented in exact minor units")
	}
	n := r.Num().Int64()
	if n > 9007199254740991 || n < -9007199254740991 {
		return 0, errors.New("bank amount exceeds exact display range")
	}
	return n, nil
}

func enableBankingBalance(raw map[string]any, currency string) (*int64, error) {
	// Reconciliation uses booked cash, not available funds including pending holds.
	for _, kind := range []string{"CLBD", "ITBD"} {
		for _, b := range mapsFromArray(arrayAny(raw["balances"])) {
			if firstString(b, "balance_type") != kind || firstString(b, "balance_amount.currency") != currency {
				continue
			}
			n, err := enableMinor(firstString(b, "balance_amount.amount"), currency)
			if err != nil {
				return nil, err
			}
			return &n, nil
		}
	}
	// No booked balance is not permission to reconcile against an available balance.
	return nil, nil
}

func fetchEnableBankingTransactions(ctx *sdk.AppCtx, conn sdk.PlatformConnection, link bankingLink, from, to string) ([]bankingTxn, error) {
	session, err := enableSession(ctx, conn, firstString(link.Metadata, "session_id"))
	if err != nil {
		return nil, err
	}
	found := false
	for _, id := range enableAccountIDs(session) {
		if id == link.ExternalID {
			found = true
		}
	}
	if !found {
		return nil, errors.New("linked account is absent from the Enable Banking session; relink after renewing consent")
	}
	out := []bankingTxn{}
	cursor := ""
	seen := map[string]bool{}
	for page := 0; page < 1000; page++ {
		input := map[string]any{"account_id": link.ExternalID, "date_from": from, "date_to": to, "transaction_status": "BOOK"}
		if cursor != "" {
			input["continuation_key"] = cursor
		}
		var raw map[string]any
		if err = executeIntegrationJSON(ctx, conn.ID, "get_account_transactions", input, &raw); err != nil {
			return nil, err
		}
		for _, m := range mapsFromArray(arrayAny(raw["transactions"])) {
			if status := firstString(m, "status"); status != "" && status != "BOOK" {
				continue
			}
			currency := strings.ToUpper(firstString(m, "transaction_amount.currency"))
			if currency != link.Account.Currency {
				return nil, errors.New("Enable Banking transaction currency differs from account")
			}
			amount, err := enableMinor(firstString(m, "transaction_amount.amount"), currency)
			if err != nil {
				return nil, err
			}
			switch firstString(m, "credit_debit_indicator") {
			case "DBIT":
				if amount > 0 {
					amount = -amount
				}
			case "CRDT":
				if amount < 0 {
					return nil, errors.New("negative credit amount from Enable Banking")
				}
			default:
				return nil, errors.New("Enable Banking transaction direction missing")
			}
			at := firstString(m, "booking_date", "value_date", "transaction_date")
			parsed, err := parseFlexibleTime(at)
			if err != nil {
				return nil, fmt.Errorf("Enable Banking transaction date: %w", err)
			}
			payee := firstString(m, "creditor.name")
			if amount >= 0 {
				payee = firstString(m, "debtor.name")
			}
			memo := firstString(m, "remittance_information")
			if parts := arrayAny(m["remittance_information"]); len(parts) > 0 {
				ss := []string{}
				for _, v := range parts {
					ss = append(ss, stringAny(v))
				}
				memo = strings.Join(ss, " ")
			}
			bt := bankingTxn{ExternalID: firstString(m, "entry_reference"), AccountExternalID: link.ExternalID, PostedAt: parsed.UTC().Format(time.RFC3339), AmountMinor: amount, Currency: currency, Payee: payee, Memo: memo}
			// transaction_id is a detail lookup identifier, not a stable dedup key.
			if bt.ExternalID == "" {
				return nil, errors.New("Enable Banking transaction is missing entry_reference; cannot safely deduplicate it")
			}
			out = append(out, bt)
		}
		next := firstString(raw, "continuation_key")
		if next == "" {
			return out, nil
		}
		if seen[next] {
			return nil, errors.New("Enable Banking repeated pagination cursor")
		}
		seen[next] = true
		cursor = next
	}
	return nil, errors.New("Enable Banking pagination limit reached; narrow the date range")
}

// Renewed consent changes account uids. identification_hash reconnects the same
// physical account to its existing ledger rather than creating another balance.
func linkEnableBankingAccount(ctx *sdk.AppCtx, conn sdk.PlatformConnection, ba bankingAccount, financeID int64, create bool, extra map[string]any) (bankingLink, error) {
	hash := firstString(ba.Raw, "identification_hash")
	if firstString(extra, "session_id") == "" {
		return bankingLink{}, errors.New("session_id required")
	}
	if financeID == 0 {
		var matched int64
		query := `SELECT finance_id FROM external_links WHERE project_id=? AND provider='enable-banking' AND connection_id=? AND external_type='account' AND external_id=?`
		args := []any{projectID(ctx), fmt.Sprint(conn.ID), ba.ExternalID}
		if hash != "" {
			query = `SELECT finance_id FROM external_links WHERE project_id=? AND provider='enable-banking' AND connection_id=? AND external_type='account' AND (external_id=? OR json_extract(metadata_json,'$.identification_hash')=?)`
			args = append(args, hash)
		}
		rows, err := ctx.AppDB().Query(query, args...)
		if err != nil {
			return bankingLink{}, err
		}
		for rows.Next() {
			var id int64
			if err = rows.Scan(&id); err != nil {
				rows.Close()
				return bankingLink{}, err
			}
			if matched != 0 && matched != id {
				rows.Close()
				return bankingLink{}, errors.New("multiple ledgers match this bank account; resolve duplicate links first")
			}
			matched = id
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return bankingLink{}, err
		}
		financeID = matched
	}
	if financeID == 0 && !create {
		return bankingLink{}, errors.New("finance_account_id required")
	}
	if financeID != 0 {
		acc, err := readAccount(ctx, financeID)
		if err != nil {
			return bankingLink{}, err
		}
		if acc.Kind != "cash" || acc.Archived || acc.Currency != ba.Currency {
			return bankingLink{}, errors.New("bank link requires active cash account with matching currency")
		}
		if acc.Source != "manual" {
			if acc.Source != "integration:enable-banking" || acc.ConnectionID != fmt.Sprint(conn.ID) {
				return bankingLink{}, errors.New("account linked to a different source")
			}
			if acc.ExternalID != ba.ExternalID {
				var oldHash string
				err = ctx.AppDB().QueryRow(`SELECT COALESCE(json_extract(metadata_json,'$.identification_hash'),'') FROM external_links WHERE project_id=? AND provider='enable-banking' AND connection_id=? AND finance_type='account' AND finance_id=?`, projectID(ctx), fmt.Sprint(conn.ID), financeID).Scan(&oldHash)
				if err != nil || hash == "" || oldHash != hash {
					return bankingLink{}, errors.New("renewed session account identity does not match the existing ledger")
				}
			}
		}
	}
	meta := map[string]any{"session_id": extra["session_id"], "identification_hash": hash, "name": ba.Name, "currency": ba.Currency, "institution": ba.Institution}
	encoded, err := json.Marshal(meta)
	if err != nil {
		return bankingLink{}, err
	}
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return bankingLink{}, err
	}
	defer tx.Rollback()
	if financeID == 0 {
		res, e := tx.Exec(`INSERT INTO accounts(project_id,name,kind,source,connection_id,external_id,currency,opening_balance,color) VALUES(?,?,'cash','integration:enable-banking',?,?,?,0,'#22c55e')`, projectID(ctx), ba.Name, fmt.Sprint(conn.ID), ba.ExternalID, ba.Currency)
		if e != nil {
			return bankingLink{}, e
		}
		financeID, err = res.LastInsertId()
		if err != nil {
			return bankingLink{}, err
		}
	} else {
		_, err = tx.Exec(`UPDATE accounts SET source='integration:enable-banking',connection_id=?,external_id=?,sync_error=NULL WHERE id=? AND project_id=?`, fmt.Sprint(conn.ID), ba.ExternalID, financeID, projectID(ctx))
		if err != nil {
			return bankingLink{}, err
		}
	}
	_, err = tx.Exec(`DELETE FROM external_links WHERE project_id=? AND provider='enable-banking' AND connection_id=? AND external_type='account' AND finance_id=?`, projectID(ctx), fmt.Sprint(conn.ID), financeID)
	if err != nil {
		return bankingLink{}, err
	}
	_, err = tx.Exec(`INSERT INTO external_links(project_id,provider,connection_id,external_type,external_id,finance_type,finance_id,metadata_json) VALUES(?,'enable-banking',?,'account',?,'account',?,?)`, projectID(ctx), fmt.Sprint(conn.ID), ba.ExternalID, financeID, string(encoded))
	if err != nil {
		return bankingLink{}, err
	}
	if err = tx.Commit(); err != nil {
		return bankingLink{}, err
	}
	acc, err := readAccount(ctx, financeID)
	return bankingLink{Account: acc, Provider: "enable-banking", Connection: conn.ID, ExternalID: ba.ExternalID, Metadata: meta}, err
}
