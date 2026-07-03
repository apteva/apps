package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

var bankingProviderSlugs = []string{"plaid", "teller", "nordigen", "truelayer", "saltedge"}

type bankingConnectionView struct {
	ID       int64  `json:"id"`
	Provider string `json:"provider"`
	AppSlug  string `json:"app_slug"`
	Name     string `json:"name"`
	Status   string `json:"status"`
	Project  string `json:"project_id,omitempty"`
}

type bankingAccount struct {
	ExternalID   string         `json:"external_id"`
	Name         string         `json:"name"`
	Currency     string         `json:"currency"`
	Kind         string         `json:"kind"`
	Institution  string         `json:"institution,omitempty"`
	Mask         string         `json:"mask,omitempty"`
	BalanceMinor *int64         `json:"balance_minor,omitempty"`
	Raw          map[string]any `json:"raw,omitempty"`
}

type bankingTxn struct {
	ExternalID        string         `json:"external_id"`
	AccountExternalID string         `json:"account_external_id"`
	PostedAt          string         `json:"posted_at"`
	AmountMinor       int64          `json:"amount_minor"`
	Currency          string         `json:"currency"`
	Payee             string         `json:"payee,omitempty"`
	Memo              string         `json:"memo,omitempty"`
	Pending           bool           `json:"pending"`
	Raw               map[string]any `json:"raw,omitempty"`
}

type bankingLink struct {
	Account    Account        `json:"account"`
	Provider   string         `json:"provider"`
	Connection int64          `json:"connection_id"`
	ExternalID string         `json:"external_account_id"`
	Metadata   map[string]any `json:"metadata,omitempty"`
}

type bankingSyncStats struct {
	Provider     string   `json:"provider"`
	ConnectionID int64    `json:"connection_id"`
	AccountID    int64    `json:"account_id,omitempty"`
	DryRun       bool     `json:"dry_run"`
	Accounts     int      `json:"accounts"`
	Imported     int      `json:"imported"`
	Reconciled   int      `json:"reconciled"`
	Skipped      int      `json:"skipped"`
	Errors       []string `json:"errors,omitempty"`
}

type bankingAdapter interface {
	DiscoverAccounts(ctx *sdk.AppCtx, conn sdk.PlatformConnection, args map[string]any) ([]bankingAccount, error)
	FetchTransactions(ctx *sdk.AppCtx, conn sdk.PlatformConnection, link bankingLink, from, to string) ([]bankingTxn, error)
	FetchBalance(ctx *sdk.AppCtx, conn sdk.PlatformConnection, link bankingLink) (*int64, error)
}

func (a *App) toolBankingConnections(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	conns, err := listBankingConnections(ctx, strArg(args, "provider", ""))
	if err != nil {
		return nil, err
	}
	return map[string]any{"providers": bankingProviderSlugs, "connections": conns}, nil
}

func (a *App) toolBankingDiscover(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	conn, provider, err := bankingConnection(ctx, strArg(args, "provider", ""), int64(intArg(args, "connection_id", 0)))
	if err != nil {
		return nil, err
	}
	accounts, err := bankingAdapterFor(provider).DiscoverAccounts(ctx, conn, args)
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"provider":      provider,
		"connection_id": conn.ID,
		"accounts":      accounts,
	}
	if boolArg(args, "import_accounts", false) {
		linked := []bankingLink{}
		for _, ba := range accounts {
			link, err := linkBankingAccount(ctx, provider, conn, ba, 0, true, bankingProviderMetadata(args))
			if err != nil {
				return nil, err
			}
			linked = append(linked, link)
		}
		out["linked"] = linked
	}
	return out, nil
}

func (a *App) toolBankingLinkAccount(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	conn, provider, err := bankingConnection(ctx, strArg(args, "provider", ""), int64(intArg(args, "connection_id", 0)))
	if err != nil {
		return nil, err
	}
	externalID := strArg(args, "external_account_id", "")
	if externalID == "" {
		return nil, errors.New("external_account_id required")
	}
	accounts, err := bankingAdapterFor(provider).DiscoverAccounts(ctx, conn, args)
	if err != nil {
		return nil, err
	}
	var found *bankingAccount
	for i := range accounts {
		if accounts[i].ExternalID == externalID {
			found = &accounts[i]
			break
		}
	}
	if found == nil {
		return nil, fmt.Errorf("external account %q not found for %s connection %d", externalID, provider, conn.ID)
	}
	financeID := int64(intArg(args, "finance_account_id", 0))
	create := boolArg(args, "create_account", financeID == 0)
	link, err := linkBankingAccount(ctx, provider, conn, *found, financeID, create, bankingProviderMetadata(args))
	if err != nil {
		return nil, err
	}
	return link, nil
}

func (a *App) toolBankingSync(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	dry := boolArg(args, "dry_run", false)
	provider := strArg(args, "provider", "")
	requestedConn := int64(intArg(args, "connection_id", 0))
	accountID := int64(intArg(args, "account_id", 0))
	from := strArg(args, "from", time.Now().UTC().AddDate(0, -3, 0).Format("2006-01-02"))
	to := strArg(args, "to", time.Now().UTC().Format("2006-01-02"))

	links, err := bankingLinks(ctx, provider, requestedConn, accountID)
	if err != nil {
		return nil, err
	}
	if len(links) == 0 {
		return nil, errors.New("no linked banking accounts; call banking_discover then banking_link_account first")
	}
	stats := bankingSyncStats{Provider: provider, ConnectionID: requestedConn, AccountID: accountID, DryRun: dry}
	for _, link := range links {
		conn, gotProvider, err := bankingConnection(ctx, link.Provider, link.Connection)
		if err != nil {
			stats.Errors = append(stats.Errors, err.Error())
			continue
		}
		adapter := bankingAdapterFor(gotProvider)
		txns, err := adapter.FetchTransactions(ctx, conn, link, from, to)
		if err != nil {
			stats.Errors = append(stats.Errors, fmt.Sprintf("%s account %d: %v", gotProvider, link.Account.ID, err))
			_, _ = ctx.AppDB().Exec(`UPDATE accounts SET sync_error=? WHERE id=?`, err.Error(), link.Account.ID)
			continue
		}
		stats.Accounts++
		for _, bt := range txns {
			if bt.Currency == "" {
				bt.Currency = link.Account.Currency
			}
			if bt.AccountExternalID == "" {
				bt.AccountExternalID = link.ExternalID
			}
			if bt.ExternalID == "" {
				bt.ExternalID = fallbackBankingTxnID(bt)
			}
			imported, skipped, err := importBankingTxn(ctx, gotProvider, conn.ID, link, bt, dry)
			if err != nil {
				stats.Errors = append(stats.Errors, err.Error())
				continue
			}
			stats.Imported += imported
			stats.Skipped += skipped
		}
		if balance, err := adapter.FetchBalance(ctx, conn, link); err == nil && balance != nil {
			n, skipped, err := reconcileBankingBalance(ctx, gotProvider, conn.ID, link.Account, *balance, dry)
			if err != nil {
				stats.Errors = append(stats.Errors, err.Error())
			} else {
				stats.Reconciled += n
				stats.Skipped += skipped
			}
		}
		if !dry {
			now := time.Now().UTC().Format(time.RFC3339)
			meta := link.Metadata
			if meta == nil {
				meta = map[string]any{}
			}
			meta["last_sync_at"] = now
			meta["last_sync_imported"] = stats.Imported
			meta["last_sync_skipped"] = stats.Skipped
			metaJSON, _ := json.Marshal(meta)
			_, _ = ctx.AppDB().Exec(
				`UPDATE external_links SET metadata_json=?, last_seen_at=?, updated_at=CURRENT_TIMESTAMP
				 WHERE project_id=? AND provider=? AND connection_id=? AND external_type='account' AND external_id=?`,
				string(metaJSON), now, projectID(ctx), gotProvider, strconv.FormatInt(conn.ID, 10), link.ExternalID,
			)
			_, _ = ctx.AppDB().Exec(
				`UPDATE accounts SET last_sync_at=?, sync_error=NULL WHERE id=?`,
				now, link.Account.ID,
			)
		}
	}
	if !dry {
		ctx.Emit("banking.synced", stats)
	}
	if len(stats.Errors) > 0 && stats.Imported == 0 && stats.Reconciled == 0 {
		return stats, errors.New(strings.Join(stats.Errors, "; "))
	}
	return stats, nil
}

func (a *App) toolBankingUnlink(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	accountID := int64(intArg(args, "account_id", 0))
	if accountID == 0 {
		return nil, errors.New("account_id required")
	}
	pid := projectID(ctx)
	res, err := ctx.AppDB().Exec(
		`DELETE FROM external_links WHERE project_id=? AND finance_type='account' AND finance_id=?`,
		pid, accountID,
	)
	if err != nil {
		return nil, err
	}
	_, _ = ctx.AppDB().Exec(
		`UPDATE accounts SET source='manual', connection_id=NULL, sync_error=NULL WHERE project_id=? AND id=?`,
		pid, accountID,
	)
	n, _ := res.RowsAffected()
	return map[string]any{"account_id": accountID, "unlinked": n}, nil
}

func listBankingConnections(ctx *sdk.AppCtx, provider string) ([]bankingConnectionView, error) {
	if ctx == nil || ctx.PlatformAPI() == nil {
		return nil, errors.New("platform connections are not available")
	}
	slugs := bankingProviderSlugs
	if provider != "" {
		if !isBankingProvider(provider) {
			return nil, fmt.Errorf("unsupported banking provider %q", provider)
		}
		slugs = []string{provider}
	}
	out := []bankingConnectionView{}
	for _, slug := range slugs {
		conns, err := ctx.PlatformAPI().ListConnections(sdk.ConnectionFilter{AppSlug: slug})
		if err != nil {
			return nil, err
		}
		for _, c := range conns {
			out = append(out, bankingConnectionView{
				ID: c.ID, Provider: slug, AppSlug: c.AppSlug, Name: c.Name, Status: c.Status, Project: c.ProjectID,
			})
		}
	}
	sortBankingConnections(out)
	return out, nil
}

func sortBankingConnections(conns []bankingConnectionView) {
	sort.SliceStable(conns, func(i, j int) bool {
		if conns[i].Provider != conns[j].Provider {
			return conns[i].Provider < conns[j].Provider
		}
		return conns[i].ID < conns[j].ID
	})
}

func bankingConnection(ctx *sdk.AppCtx, provider string, requested int64) (sdk.PlatformConnection, string, error) {
	if ctx == nil || ctx.PlatformAPI() == nil {
		return sdk.PlatformConnection{}, "", errors.New("platform connections are not available")
	}
	if requested != 0 {
		c, err := ctx.PlatformAPI().GetConnection(requested)
		if err != nil {
			return sdk.PlatformConnection{}, "", err
		}
		if c == nil {
			return sdk.PlatformConnection{}, "", fmt.Errorf("connection %d not found", requested)
		}
		got := c.AppSlug
		if provider != "" && provider != got {
			return sdk.PlatformConnection{}, "", fmt.Errorf("connection %d is %s, not %s", requested, got, provider)
		}
		if !isBankingProvider(got) {
			return sdk.PlatformConnection{}, "", fmt.Errorf("connection %d uses unsupported banking provider %q", requested, got)
		}
		return *c, got, nil
	}
	if provider == "" {
		return sdk.PlatformConnection{}, "", errors.New("provider or connection_id required")
	}
	if !isBankingProvider(provider) {
		return sdk.PlatformConnection{}, "", fmt.Errorf("unsupported banking provider %q", provider)
	}
	conns, err := ctx.PlatformAPI().ListConnections(sdk.ConnectionFilter{AppSlug: provider})
	if err != nil {
		return sdk.PlatformConnection{}, "", err
	}
	for _, c := range conns {
		if c.Status == "" || c.Status == "active" || c.Status == "connected" {
			return c, provider, nil
		}
	}
	return sdk.PlatformConnection{}, "", fmt.Errorf("no active %s connection bound", provider)
}

func isBankingProvider(slug string) bool {
	for _, s := range bankingProviderSlugs {
		if s == slug {
			return true
		}
	}
	return false
}

func bankingAdapterFor(provider string) bankingAdapter {
	return genericBankingAdapter{provider: provider}
}

type genericBankingAdapter struct{ provider string }

func (a genericBankingAdapter) DiscoverAccounts(ctx *sdk.AppCtx, conn sdk.PlatformConnection, args map[string]any) ([]bankingAccount, error) {
	switch a.provider {
	case "plaid":
		var raw any
		accessToken := strArg(args, "access_token", "")
		if accessToken == "" {
			return nil, errors.New("access_token required for plaid banking_discover")
		}
		if err := executeIntegrationJSON(ctx, conn.ID, "get_accounts", map[string]any{"access_token": accessToken}, &raw); err != nil {
			return nil, err
		}
		return normalizePlaidAccounts(raw), nil
	case "teller":
		var raw any
		if err := executeIntegrationJSON(ctx, conn.ID, "list_accounts", nil, &raw); err != nil {
			return nil, err
		}
		return normalizeBankingAccounts(a.provider, raw), nil
	case "nordigen":
		return discoverNordigenAccounts(ctx, conn)
	case "truelayer":
		var raw any
		if err := executeIntegrationJSON(ctx, conn.ID, "list_accounts", nil, &raw); err != nil {
			return nil, err
		}
		return normalizeBankingAccounts(a.provider, raw), nil
	case "saltedge":
		var raw any
		providerConnID := strArg(args, "provider_connection_id", "")
		if providerConnID == "" {
			return nil, errors.New("provider_connection_id required for saltedge banking_discover")
		}
		if err := executeIntegrationJSON(ctx, conn.ID, "list_accounts", map[string]any{"connection_id": providerConnID}, &raw); err != nil {
			return nil, err
		}
		return normalizeBankingAccounts(a.provider, raw), nil
	default:
		return nil, fmt.Errorf("unsupported banking provider %q", a.provider)
	}
}

func (a genericBankingAdapter) FetchTransactions(ctx *sdk.AppCtx, conn sdk.PlatformConnection, link bankingLink, from, to string) ([]bankingTxn, error) {
	input := map[string]any{}
	switch a.provider {
	case "plaid":
		accessToken, err := bankingLinkAccessToken(link)
		if err != nil {
			return nil, err
		}
		input = map[string]any{"access_token": accessToken, "start_date": from, "end_date": to, "account_ids": []any{link.ExternalID}}
		var raw any
		if err := executeIntegrationJSON(ctx, conn.ID, "get_transactions", input, &raw); err != nil {
			return nil, err
		}
		return normalizeBankingTxns(a.provider, link.ExternalID, raw), nil
	case "teller":
		input = map[string]any{"account_id": link.ExternalID, "start_date": from, "end_date": to}
		var raw any
		if err := executeIntegrationJSON(ctx, conn.ID, "list_transactions", input, &raw); err != nil {
			return nil, err
		}
		return normalizeBankingTxns(a.provider, link.ExternalID, raw), nil
	case "nordigen":
		input = map[string]any{"account_id": link.ExternalID, "date_from": from, "date_to": to}
		var raw any
		if err := executeIntegrationJSON(ctx, conn.ID, "get_account_transactions", input, &raw); err != nil {
			return nil, err
		}
		return normalizeBankingTxns(a.provider, link.ExternalID, raw), nil
	case "truelayer":
		input = map[string]any{"account_id": link.ExternalID, "from": from, "to": to}
		var raw any
		if err := executeIntegrationJSON(ctx, conn.ID, "get_account_transactions", input, &raw); err != nil {
			return nil, err
		}
		return normalizeBankingTxns(a.provider, link.ExternalID, raw), nil
	case "saltedge":
		input = map[string]any{"connection_id": link.Metadata["provider_connection_id"], "account_id": link.ExternalID, "made_on_from": from, "made_on_to": to}
		if input["connection_id"] == nil || input["connection_id"] == "" {
			delete(input, "connection_id")
		}
		var raw any
		if err := executeIntegrationJSON(ctx, conn.ID, "list_transactions", input, &raw); err != nil {
			return nil, err
		}
		return normalizeBankingTxns(a.provider, link.ExternalID, raw), nil
	default:
		return nil, fmt.Errorf("unsupported banking provider %q", a.provider)
	}
}

func (a genericBankingAdapter) FetchBalance(ctx *sdk.AppCtx, conn sdk.PlatformConnection, link bankingLink) (*int64, error) {
	var tool string
	input := map[string]any{"account_id": link.ExternalID}
	switch a.provider {
	case "plaid":
		tool = "get_balances"
		accessToken, err := bankingLinkAccessToken(link)
		if err != nil {
			return nil, err
		}
		input = map[string]any{"access_token": accessToken, "account_ids": []any{link.ExternalID}}
	case "teller":
		tool = "get_account_balances"
	case "nordigen":
		tool = "get_account_balances"
	case "truelayer":
		tool = "get_account_balance"
	case "saltedge":
		return link.AccountBalance(), nil
	default:
		return nil, fmt.Errorf("unsupported banking provider %q", a.provider)
	}
	var raw any
	if err := executeIntegrationJSON(ctx, conn.ID, tool, input, &raw); err != nil {
		return nil, err
	}
	return extractBalanceMinor(raw, link.Account.Currency), nil
}

func (l bankingLink) AccountBalance() *int64 {
	if v, ok := l.Metadata["balance_minor"]; ok {
		if f, ok := floatAny(v); ok {
			n := int64(f)
			return &n
		}
	}
	return nil
}

func bankingLinkAccessToken(link bankingLink) (string, error) {
	if token := firstString(link.Metadata, "access_token"); token != "" {
		return token, nil
	}
	return "", errors.New("plaid linked account is missing access_token metadata; relink with access_token")
}

func discoverNordigenAccounts(ctx *sdk.AppCtx, conn sdk.PlatformConnection) ([]bankingAccount, error) {
	var req any
	if err := executeIntegrationJSON(ctx, conn.ID, "list_requisitions", nil, &req); err != nil {
		return nil, err
	}
	ids := []string{}
	for _, item := range flattenItems(req) {
		for _, v := range arrayAny(item["accounts"]) {
			if id := stringAny(v); id != "" {
				ids = append(ids, id)
			}
		}
	}
	out := []bankingAccount{}
	for _, id := range ids {
		var raw any
		if err := executeIntegrationJSON(ctx, conn.ID, "get_account_metadata", map[string]any{"account_id": id}, &raw); err != nil {
			continue
		}
		accs := normalizeBankingAccounts("nordigen", raw)
		if len(accs) == 0 {
			accs = []bankingAccount{{ExternalID: id, Name: "Bank account " + id, Kind: "cash", Currency: "EUR", Raw: asMap(raw)}}
		}
		out = append(out, accs...)
	}
	return out, nil
}

func normalizePlaidAccounts(raw any) []bankingAccount {
	accounts := normalizeBankingAccounts("plaid", raw)
	for i := range accounts {
		if accounts[i].ExternalID == "" {
			continue
		}
		if accounts[i].Raw == nil {
			accounts[i].Raw = map[string]any{}
		}
		accounts[i].Raw["access_token_note"] = "Plaid tools need the Item access_token; Finance stores account_id as external_id for imported accounts."
	}
	return accounts
}

func normalizeBankingAccounts(provider string, raw any) []bankingAccount {
	items := flattenItems(raw)
	out := []bankingAccount{}
	for _, item := range items {
		id := firstString(item, "id", "account_id", "accountId", "account_uid", "uid", "resourceId")
		if id == "" {
			continue
		}
		ccy := strings.ToUpper(firstString(item, "currency", "currency_code", "currencyCode", "balances.iso_currency_code", "iso_currency_code"))
		if ccy == "" {
			ccy = "EUR"
		}
		name := firstString(item, "name", "display_name", "displayName", "official_name", "account.name", "institution.name")
		if name == "" {
			name = "Bank account " + id
		}
		mask := firstString(item, "mask", "last_four", "lastFour", "account_number.mask")
		inst := firstString(item, "institution.name", "institution_name", "provider_name", "bank_name")
		bal := extractBalanceMinor(item, ccy)
		out = append(out, bankingAccount{
			ExternalID:   id,
			Name:         name,
			Currency:     ccy,
			Kind:         "cash",
			Institution:  inst,
			Mask:         mask,
			BalanceMinor: bal,
			Raw:          item,
		})
	}
	return out
}

func normalizeBankingTxns(provider, accountExternalID string, raw any) []bankingTxn {
	items := flattenTransactionItems(raw)
	out := []bankingTxn{}
	for _, item := range items {
		id := firstString(item, "id", "transaction_id", "transactionId", "entry_reference", "internal_transaction_id", "provider_transaction_id")
		accountID := firstString(item, "account_id", "accountId", "account_uid")
		if accountID == "" {
			accountID = accountExternalID
		}
		ccy := strings.ToUpper(firstString(item, "currency", "iso_currency_code", "currency_code", "transactionAmount.currency", "amount.currency"))
		amount := amountMinorFromTxn(item)
		if provider == "plaid" {
			amount = -amount
		}
		postedAt := firstTime(item)
		if postedAt == "" {
			postedAt = time.Now().UTC().Format(time.RFC3339)
		}
		payee := firstString(item, "merchant_name", "merchantName", "counterparty", "description", "payee", "name", "remittanceInformationUnstructured")
		memo := firstString(item, "description", "details", "reference", "memo", "remittanceInformationUnstructured")
		pending := boolFromAny(item["pending"]) || strings.EqualFold(firstString(item, "status"), "pending")
		bt := bankingTxn{
			ExternalID:        id,
			AccountExternalID: accountID,
			PostedAt:          postedAt,
			AmountMinor:       amount,
			Currency:          ccy,
			Payee:             payee,
			Memo:              memo,
			Pending:           pending,
			Raw:               item,
		}
		if bt.ExternalID == "" {
			bt.ExternalID = fallbackBankingTxnID(bt)
		}
		out = append(out, bt)
	}
	return out
}

func flattenItems(raw any) []map[string]any {
	switch v := raw.(type) {
	case []any:
		return mapsFromArray(v)
	case []map[string]any:
		return v
	case map[string]any:
		for _, key := range []string{"accounts", "results", "data", "items", "resources"} {
			if arr := arrayAny(v[key]); len(arr) > 0 {
				return mapsFromArray(arr)
			}
			if m, ok := v[key].(map[string]any); ok {
				if nested := flattenItems(m); len(nested) > 0 {
					return nested
				}
			}
		}
		return []map[string]any{v}
	default:
		return nil
	}
}

func flattenTransactionItems(raw any) []map[string]any {
	if m := asMap(raw); m != nil {
		if arr := mapsFromArray(arrayAny(m["transactions"])); len(arr) > 0 {
			return arr
		}
		if txs := childMap(m, "transactions"); len(txs) > 0 {
			out := []map[string]any{}
			for _, key := range []string{"booked", "pending"} {
				for _, item := range mapsFromArray(arrayAny(txs[key])) {
					if key == "pending" {
						item["pending"] = true
					}
					out = append(out, item)
				}
			}
			if len(out) > 0 {
				return out
			}
		}
	}
	return flattenItems(raw)
}

func mapsFromArray(arr []any) []map[string]any {
	out := []map[string]any{}
	for _, v := range arr {
		if m := asMap(v); m != nil {
			out = append(out, m)
		}
	}
	return out
}

func asMap(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return nil
}

func arrayAny(v any) []any {
	switch x := v.(type) {
	case []any:
		return x
	case []map[string]any:
		out := make([]any, 0, len(x))
		for _, m := range x {
			out = append(out, m)
		}
		return out
	default:
		return nil
	}
}

func extractBalanceMinor(raw any, currency string) *int64 {
	if m := asMap(raw); m != nil {
		for _, item := range mapsFromArray(arrayAny(m["balances"])) {
			for _, key := range []string{"balanceAmount.amount", "amount", "current", "available"} {
				if v, ok := nestedAny(item, key); ok {
					if f, ok := floatAny(v); ok {
						n := int64(math.Round(f * 100))
						return &n
					}
				}
			}
		}
	}
	for _, item := range flattenItems(raw) {
		for _, key := range []string{
			"balances.current", "balances.available", "balance.current", "balance.available",
			"current_balance", "available_balance", "current", "available", "ledger", "balance",
		} {
			if v, ok := nestedAny(item, key); ok {
				if f, ok := floatAny(v); ok {
					n := int64(math.Round(f * 100))
					return &n
				}
			}
		}
	}
	return nil
}

func amountMinorFromTxn(item map[string]any) int64 {
	if f, ok := floatAny(item["amount"]); ok {
		return int64(math.Round(f * 100))
	}
	for _, key := range []string{"transactionAmount.amount", "amount.value", "value", "running_balance.amount"} {
		if v, ok := nestedAny(item, key); ok {
			if f, ok := floatAny(v); ok {
				return int64(math.Round(f * 100))
			}
		}
	}
	credit := firstFloat(item, "credit", "moneyIn", "paid_in")
	debit := firstFloat(item, "debit", "moneyOut", "paid_out")
	if credit != 0 || debit != 0 {
		return int64(math.Round((credit - debit) * 100))
	}
	return 0
}

func linkBankingAccount(ctx *sdk.AppCtx, provider string, conn sdk.PlatformConnection, ba bankingAccount, financeID int64, create bool, extraMeta map[string]any) (bankingLink, error) {
	pid := projectID(ctx)
	if ba.ExternalID == "" {
		return bankingLink{}, errors.New("banking account has empty external_id")
	}
	if financeID == 0 {
		var id int64
		if err := ctx.AppDB().QueryRow(
			`SELECT finance_id FROM external_links
			 WHERE project_id=? AND provider=? AND connection_id=? AND external_type='account' AND external_id=?`,
			pid, provider, strconv.FormatInt(conn.ID, 10), ba.ExternalID,
		).Scan(&id); err == nil {
			financeID = id
		}
	}
	if financeID == 0 && !create {
		return bankingLink{}, errors.New("finance_account_id required when create_account=false")
	}
	if financeID == 0 {
		name := ba.Name
		if ba.Mask != "" {
			name += " • " + ba.Mask
		}
		res, err := ctx.AppDB().Exec(
			`INSERT INTO accounts (project_id, name, kind, source, connection_id, external_id, currency, opening_balance, color)
			 VALUES (?, ?, 'cash', ?, ?, ?, ?, ?, ?)`,
			pid, name, "integration:"+provider, strconv.FormatInt(conn.ID, 10), ba.ExternalID,
			nonempty(ba.Currency, "EUR"), int64(0), "#22c55e",
		)
		if err != nil {
			return bankingLink{}, err
		}
		financeID, _ = res.LastInsertId()
	} else {
		if _, err := readAccount(ctx, financeID); err != nil {
			return bankingLink{}, err
		}
		_, err := ctx.AppDB().Exec(
			`UPDATE accounts SET source=?, connection_id=?, external_id=?, sync_error=NULL WHERE project_id=? AND id=?`,
			"integration:"+provider, strconv.FormatInt(conn.ID, 10), ba.ExternalID, pid, financeID,
		)
		if err != nil {
			return bankingLink{}, err
		}
	}
	meta := map[string]any{
		"name": ba.Name, "currency": ba.Currency, "institution": ba.Institution, "mask": ba.Mask,
	}
	for k, v := range extraMeta {
		meta[k] = v
	}
	if ba.BalanceMinor != nil {
		meta["balance_minor"] = *ba.BalanceMinor
	}
	if provider == "saltedge" {
		if v := firstString(ba.Raw, "connection_id", "connectionId"); v != "" {
			meta["provider_connection_id"] = v
		}
	}
	rawMeta, _ := json.Marshal(meta)
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := ctx.AppDB().Exec(
		`INSERT INTO external_links
		   (project_id, provider, connection_id, external_type, external_id, finance_type, finance_id, metadata_json, last_seen_at)
		 VALUES (?, ?, ?, 'account', ?, 'account', ?, ?, ?)
		 ON CONFLICT(project_id, provider, connection_id, external_type, external_id)
		 DO UPDATE SET finance_id=excluded.finance_id, metadata_json=excluded.metadata_json,
		               last_seen_at=excluded.last_seen_at, updated_at=CURRENT_TIMESTAMP`,
		pid, provider, strconv.FormatInt(conn.ID, 10), ba.ExternalID, financeID, string(rawMeta), now,
	)
	if err != nil {
		return bankingLink{}, err
	}
	acc, err := readAccount(ctx, financeID)
	if err != nil {
		return bankingLink{}, err
	}
	return bankingLink{Account: acc, Provider: provider, Connection: conn.ID, ExternalID: ba.ExternalID, Metadata: meta}, nil
}

func bankingProviderMetadata(args map[string]any) map[string]any {
	meta := map[string]any{}
	if token := strArg(args, "access_token", ""); token != "" {
		meta["access_token"] = token
	}
	if providerConnID := strArg(args, "provider_connection_id", ""); providerConnID != "" {
		meta["provider_connection_id"] = providerConnID
	}
	return meta
}

func bankingLinks(ctx *sdk.AppCtx, provider string, connID int64, accountID int64) ([]bankingLink, error) {
	pid := projectID(ctx)
	where := []string{"l.project_id=?", "l.external_type='account'", "l.finance_type='account'"}
	args := []any{pid}
	if provider != "" {
		where = append(where, "l.provider=?")
		args = append(args, provider)
	}
	if connID != 0 {
		where = append(where, "l.connection_id=?")
		args = append(args, strconv.FormatInt(connID, 10))
	}
	if accountID != 0 {
		where = append(where, "l.finance_id=?")
		args = append(args, accountID)
	}
	rows, err := ctx.AppDB().Query(
		`SELECT l.provider, l.connection_id, l.external_id, l.metadata_json,
		        a.id, a.project_id, a.name, a.kind, a.source, COALESCE(a.connection_id,''),
		        COALESCE(a.external_id,''), a.currency, a.opening_balance, a.opening_at,
		        a.color, a.archived, COALESCE(a.last_sync_at,''), COALESCE(a.sync_error,''), a.created_at
		   FROM external_links l
		   JOIN accounts a ON a.id=l.finance_id AND a.project_id=l.project_id
		  WHERE `+strings.Join(where, " AND ")+`
		  ORDER BY a.name`,
		args...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []bankingLink{}
	for rows.Next() {
		var link bankingLink
		var conn string
		var metaRaw string
		var archived int
		if err := rows.Scan(&link.Provider, &conn, &link.ExternalID, &metaRaw,
			&link.Account.ID, &link.Account.ProjectID, &link.Account.Name, &link.Account.Kind,
			&link.Account.Source, &link.Account.ConnectionID, &link.Account.ExternalID,
			&link.Account.Currency, &link.Account.OpeningBalance, &link.Account.OpeningAt,
			&link.Account.Color, &archived, &link.Account.LastSyncAt, &link.Account.SyncError,
			&link.Account.CreatedAt); err != nil {
			return nil, err
		}
		link.Connection, _ = strconv.ParseInt(conn, 10, 64)
		link.Account.Archived = archived != 0
		_ = json.Unmarshal([]byte(metaRaw), &link.Metadata)
		out = append(out, link)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	_ = rows.Close()
	for i := range out {
		out[i].Account.CashBalance = mustCashBalance(ctx, out[i].Account.ID, out[i].Account.OpeningBalance)
		out[i].Account.TotalValue = out[i].Account.CashBalance
	}
	return out, nil
}

func importBankingTxn(ctx *sdk.AppCtx, provider string, connID int64, link bankingLink, bt bankingTxn, dry bool) (int, int, error) {
	pid := projectID(ctx)
	connStr := strconv.FormatInt(connID, 10)
	var existing int64
	if err := ctx.AppDB().QueryRow(
		`SELECT finance_id FROM external_links
		 WHERE project_id=? AND provider=? AND connection_id=? AND external_type='transaction' AND external_id=?`,
		pid, provider, connStr, bt.ExternalID,
	).Scan(&existing); err == nil {
		return 0, 1, nil
	}
	externalID := provider + ":bank:" + bt.ExternalID
	if txnExternalIDExists(ctx, link.Account.ID, externalID) {
		return 0, 1, nil
	}
	if dry {
		return 1, 0, nil
	}
	kind := bankingTxnKind(bt)
	id, err := insertTxn(ctx, txnIn{
		AccountID:  link.Account.ID,
		PostedAt:   bt.PostedAt,
		Kind:       kind,
		Amount:     bt.AmountMinor,
		Currency:   nonempty(bt.Currency, link.Account.Currency),
		Payee:      bt.Payee,
		Memo:       bt.Memo,
		ExternalID: externalID,
	})
	if err != nil {
		return 0, 0, err
	}
	meta := map[string]any{"pending": bt.Pending, "provider_account_id": bt.AccountExternalID}
	rawMeta, _ := json.Marshal(meta)
	now := time.Now().UTC().Format(time.RFC3339)
	_, err = ctx.AppDB().Exec(
		`INSERT INTO external_links
		   (project_id, provider, connection_id, external_type, external_id, finance_type, finance_id, metadata_json, last_seen_at)
		 VALUES (?, ?, ?, 'transaction', ?, 'transaction', ?, ?, ?)`,
		pid, provider, connStr, bt.ExternalID, id, string(rawMeta), now,
	)
	if err != nil {
		return 0, 0, err
	}
	return 1, 0, nil
}

func bankingTxnKind(bt bankingTxn) string {
	text := strings.ToLower(bt.Payee + " " + bt.Memo)
	if strings.Contains(text, "fee") || strings.Contains(text, "charge") {
		return "fee"
	}
	if strings.Contains(text, "tax") {
		return "tax"
	}
	if bt.AmountMinor >= 0 {
		return "income"
	}
	return "expense"
}

func reconcileBankingBalance(ctx *sdk.AppCtx, provider string, connID int64, acc Account, reported int64, dry bool) (int, int, error) {
	current := mustCashBalance(ctx, acc.ID, acc.OpeningBalance)
	delta := reported - current
	if delta == 0 {
		return 0, 1, nil
	}
	if dry {
		return 1, 0, nil
	}
	day := time.Now().UTC().Format("2006-01-02")
	externalID := provider + ":balance-reconcile:" + strconv.FormatInt(connID, 10) + ":" + day
	kind := "deposit"
	if delta < 0 {
		kind = "withdraw"
	}
	if txnExternalIDExists(ctx, acc.ID, externalID) {
		return 0, 1, nil
	}
	_, err := insertTxn(ctx, txnIn{
		AccountID:  acc.ID,
		PostedAt:   time.Now().UTC().Format(time.RFC3339),
		Kind:       kind,
		Amount:     delta,
		Currency:   acc.Currency,
		Payee:      "Bank balance reconciliation",
		Memo:       "Imported balance adjustment",
		ExternalID: externalID,
	})
	if err != nil {
		return 0, 0, err
	}
	return 1, 0, nil
}

func fallbackBankingTxnID(bt bankingTxn) string {
	h := sha256.Sum256([]byte(strings.Join([]string{
		bt.AccountExternalID, bt.PostedAt, strconv.FormatInt(bt.AmountMinor, 10),
		strings.ToLower(strings.TrimSpace(bt.Payee + " " + bt.Memo)),
	}, "|")))
	return hex.EncodeToString(h[:])
}

func boolFromAny(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return x == "true" || x == "1" || strings.EqualFold(x, "yes")
	default:
		return false
	}
}

func nonempty(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}

func (a *App) handleBankingConnections(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET", http.StatusMethodNotAllowed)
		return
	}
	out, err := a.toolBankingConnections(globalCtx, map[string]any{"provider": r.URL.Query().Get("provider")})
	writeOrErr(w, out, err)
}

func (a *App) handleBankingDiscover(w http.ResponseWriter, r *http.Request) {
	postBody(w, r, a.toolBankingDiscover)
}

func (a *App) handleBankingLink(w http.ResponseWriter, r *http.Request) {
	postBody(w, r, a.toolBankingLinkAccount)
}

func (a *App) handleBankingSync(w http.ResponseWriter, r *http.Request) {
	postBody(w, r, a.toolBankingSync)
}

func (a *App) handleBankingUnlink(w http.ResponseWriter, r *http.Request) {
	postBody(w, r, a.toolBankingUnlink)
}
