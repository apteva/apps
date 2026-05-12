// finance v0.1 — unified personal-wealth tracker.
//
// One ledger for everything: cash, brokerage positions, P2P loans,
// crypto, real estate, vehicles, pensions, loans. v0.1 is manual
// entry; v0.2 adds Trading 212 + Plaid + others via the platform
// integrations engine. Same `accounts`/`instruments`/`holdings`/
// `transactions` rows whether data was typed by the user or synced
// by an adapter — manual and integrated are indistinguishable at
// query time.
//
// Money is always signed integer minor units (cents/sen/pence) keyed
// to the transaction's currency. Quantities are REAL so fractional
// shares + 0.0001 BTC work. Costs are tracked avg-cost; lots land
// in v0.4 when tax reports become real.
package main

import (
	"database/sql"
	_ "embed"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

//go:embed apteva.yaml
var manifestYAML []byte

var globalCtx *sdk.AppCtx

type App struct{}

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest(manifestYAML)
	if err != nil {
		panic("invalid embedded manifest: " + err.Error())
	}
	return *m
}

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx.AppDB() == nil {
		return errors.New("finance requires a db block")
	}
	globalCtx = ctx
	// Ensure a settings row exists for the project.
	pid := os.Getenv("APTEVA_PROJECT_ID")
	if pid != "" {
		_, _ = ctx.AppDB().Exec(
			`INSERT OR IGNORE INTO settings (project_id, base_currency) VALUES (?, 'EUR')`,
			pid,
		)
	}
	ctx.Logger().Info("finance mounted")
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error       { return nil }
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) Workers() []sdk.Worker             { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

// ─── HTTP routes ─────────────────────────────────────────────────
//
// The platform reserves /events; everything else is fair game.
func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/settings", Handler: a.handleSettings},
		{Pattern: "/accounts", Handler: a.handleAccounts},
		{Pattern: "/accounts/", Handler: a.handleAccountsItem},
		{Pattern: "/instruments", Handler: a.handleInstruments},
		{Pattern: "/instruments/", Handler: a.handleInstrumentsItem},
		{Pattern: "/holdings", Handler: a.handleHoldings},
		{Pattern: "/holdings/", Handler: a.handleHoldingsItem},
		{Pattern: "/txns", Handler: a.handleTxns},
		{Pattern: "/txns/", Handler: a.handleTxnsItem},
		{Pattern: "/txns/buy", Handler: a.handleTxnsBuy},
		{Pattern: "/txns/sell", Handler: a.handleTxnsSell},
		{Pattern: "/txns/dividend", Handler: a.handleTxnsDividend},
		{Pattern: "/txns/interest", Handler: a.handleTxnsInterest},
		{Pattern: "/txns/transfer", Handler: a.handleTxnsTransfer},
		{Pattern: "/valuations", Handler: a.handleValuations},
		{Pattern: "/categories", Handler: a.handleCategories},
		{Pattern: "/categories/", Handler: a.handleCategoriesItem},
		{Pattern: "/prices", Handler: a.handlePrices},
		{Pattern: "/fx", Handler: a.handleFX},
		{Pattern: "/reports/net-worth", Handler: a.handleReportNetWorth},
		{Pattern: "/reports/allocation", Handler: a.handleReportAllocation},
		{Pattern: "/reports/performance", Handler: a.handleReportPerformance},
		{Pattern: "/reports/cashflow", Handler: a.handleReportCashflow},
		{Pattern: "/import/csv", Handler: a.handleImportCSV},
		{Pattern: "/budgets", Handler: a.handleBudgets},
		{Pattern: "/budgets/", Handler: a.handleBudgetsItem},
		{Pattern: "/budgets/status", Handler: a.handleBudgetsStatus},
	}
}

// ─── MCP tools ───────────────────────────────────────────────────

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{Name: "settings_get", Description: "Read project settings.",
			InputSchema: schemaObject(map[string]any{}, nil),
			Handler:     a.toolSettingsGet},
		{Name: "settings_set", Description: "Update project settings. Args: base_currency?, week_starts_on?.",
			InputSchema: schemaObject(map[string]any{
				"base_currency":   map[string]any{"type": "string"},
				"week_starts_on":  map[string]any{"type": "string", "enum": []string{"mon", "sun"}},
			}, nil),
			Handler: a.toolSettingsSet},

		{Name: "accounts_list", Description: "List all accounts in the project (archived included; filter client-side).",
			InputSchema: schemaObject(map[string]any{}, nil),
			Handler:     a.toolAccountsList},
		{Name: "accounts_get", Description: "Read one account with current balance + holdings.",
			InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}}, []string{"id"}),
			Handler:     a.toolAccountsGet},
		{Name: "accounts_create", Description: "Create an account. Args: name, kind, currency?, opening_balance? (minor units, signed), opening_at?, color?.",
			InputSchema: schemaObject(map[string]any{
				"name":            map[string]any{"type": "string"},
				"kind":            map[string]any{"type": "string", "enum": accountKinds()},
				"currency":        map[string]any{"type": "string"},
				"opening_balance": map[string]any{"type": "integer"},
				"opening_at":      map[string]any{"type": "string"},
				"color":           map[string]any{"type": "string"},
			}, []string{"name", "kind"}),
			Handler: a.toolAccountsCreate},
		{Name: "accounts_update", Description: "Update an account. Args: id, name?, color?, archived?, opening_balance?, opening_at?.",
			InputSchema: schemaObject(map[string]any{
				"id":              map[string]any{"type": "integer"},
				"name":            map[string]any{"type": "string"},
				"color":           map[string]any{"type": "string"},
				"archived":        map[string]any{"type": "boolean"},
				"opening_balance": map[string]any{"type": "integer"},
				"opening_at":      map[string]any{"type": "string"},
			}, []string{"id"}),
			Handler: a.toolAccountsUpdate},
		{Name: "accounts_delete", Description: "Delete an account and all its holdings + transactions.",
			InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}}, []string{"id"}),
			Handler:     a.toolAccountsDelete},

		{Name: "instruments_search", Description: "Search instruments by symbol / ISIN / name substring. Args: query, kind?, project_only?.",
			InputSchema: schemaObject(map[string]any{
				"query":        map[string]any{"type": "string"},
				"kind":         map[string]any{"type": "string"},
				"project_only": map[string]any{"type": "boolean"},
			}, []string{"query"}),
			Handler: a.toolInstrumentsSearch},
		{Name: "instruments_get", Description: "Read one instrument.",
			InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}}, []string{"id"}),
			Handler:     a.toolInstrumentsGet},
		{Name: "instruments_create", Description: "Create an instrument. For stocks/etfs/crypto/cash this creates a shared (project-scoped NULL) row when project_only is false; for real_estate/vehicle/p2p/other it's project-scoped.",
			InputSchema: schemaObject(map[string]any{
				"kind":           map[string]any{"type": "string", "enum": instrumentKinds()},
				"symbol":         map[string]any{"type": "string"},
				"name":           map[string]any{"type": "string"},
				"quote_currency": map[string]any{"type": "string"},
				"isin":           map[string]any{"type": "string"},
				"exchange":       map[string]any{"type": "string"},
				"metadata":       map[string]any{"type": "object"},
				"project_only":   map[string]any{"type": "boolean"},
			}, []string{"kind", "symbol", "name", "quote_currency"}),
			Handler: a.toolInstrumentsCreate},
		{Name: "instruments_update", Description: "Update an instrument's display metadata. Args: id, name?, exchange?, metadata?.",
			InputSchema: schemaObject(map[string]any{
				"id":       map[string]any{"type": "integer"},
				"name":     map[string]any{"type": "string"},
				"exchange": map[string]any{"type": "string"},
				"metadata": map[string]any{"type": "object"},
			}, []string{"id"}),
			Handler: a.toolInstrumentsUpdate},

		{Name: "holdings_list", Description: "List holdings, optionally filtered.",
			InputSchema: schemaObject(map[string]any{
				"account_id":     map[string]any{"type": "integer"},
				"instrument_id":  map[string]any{"type": "integer"},
				"include_closed": map[string]any{"type": "boolean"},
			}, nil),
			Handler: a.toolHoldingsList},
		{Name: "holdings_get", Description: "Read one holding with current value + P&L.",
			InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}}, []string{"id"}),
			Handler:     a.toolHoldingsGet},
		{Name: "holdings_set", Description: "Manual upsert of a holding's quantity / cost_basis (no ledger entry — useful when reconciling). Args: account_id, instrument_id, quantity, cost_basis?.",
			InputSchema: schemaObject(map[string]any{
				"account_id":    map[string]any{"type": "integer"},
				"instrument_id": map[string]any{"type": "integer"},
				"quantity":      map[string]any{"type": "number"},
				"cost_basis":    map[string]any{"type": "integer"},
			}, []string{"account_id", "instrument_id", "quantity"}),
			Handler: a.toolHoldingsSet},

		{Name: "txns_list", Description: "List transactions.",
			InputSchema: schemaObject(map[string]any{
				"account_id":  map[string]any{"type": "integer"},
				"holding_id":  map[string]any{"type": "integer"},
				"kind":        map[string]any{"type": "string"},
				"category_id": map[string]any{"type": "integer"},
				"from":        map[string]any{"type": "string"},
				"to":          map[string]any{"type": "string"},
				"payee":       map[string]any{"type": "string"},
				"limit":       map[string]any{"type": "integer"},
			}, nil),
			Handler: a.toolTxnsList},
		{Name: "txns_get", Description: "Read one transaction.",
			InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}}, []string{"id"}),
			Handler:     a.toolTxnsGet},
		{Name: "txns_create", Description: "Create a generic cash-flow transaction (deposit/withdraw/income/expense/fee/tax). For buy/sell/dividend/interest/transfer use the dedicated tools.",
			InputSchema: schemaObject(map[string]any{
				"account_id":  map[string]any{"type": "integer"},
				"kind":        map[string]any{"type": "string", "enum": []string{"deposit", "withdraw", "income", "expense", "fee", "tax"}},
				"amount":      map[string]any{"type": "integer"},
				"posted_at":   map[string]any{"type": "string"},
				"payee":       map[string]any{"type": "string"},
				"memo":        map[string]any{"type": "string"},
				"category_id": map[string]any{"type": "integer"},
			}, []string{"account_id", "kind", "amount", "posted_at"}),
			Handler: a.toolTxnsCreate},
		{Name: "txns_update", Description: "Update a transaction's metadata (payee, memo, category). Amount/quantity edits aren't supported — delete + recreate to keep holdings consistent.",
			InputSchema: schemaObject(map[string]any{
				"id":          map[string]any{"type": "integer"},
				"payee":       map[string]any{"type": "string"},
				"memo":        map[string]any{"type": "string"},
				"category_id": map[string]any{"type": "integer"},
			}, []string{"id"}),
			Handler: a.toolTxnsUpdate},
		{Name: "txns_delete", Description: "Delete a transaction and reverse its holding impact (qty + cost_basis).",
			InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}}, []string{"id"}),
			Handler:     a.toolTxnsDelete},

		{Name: "txns_buy", Description: "Record a buy. Args: account_id, instrument_id, quantity, amount (positive — gross cash spent), posted_at, fee?, memo?.",
			InputSchema: schemaObject(map[string]any{
				"account_id":    map[string]any{"type": "integer"},
				"instrument_id": map[string]any{"type": "integer"},
				"quantity":      map[string]any{"type": "number"},
				"amount":        map[string]any{"type": "integer"},
				"posted_at":     map[string]any{"type": "string"},
				"fee":           map[string]any{"type": "integer"},
				"memo":          map[string]any{"type": "string"},
			}, []string{"account_id", "instrument_id", "quantity", "amount", "posted_at"}),
			Handler: a.toolTxnsBuy},
		{Name: "txns_sell", Description: "Record a sell. Args: account_id, instrument_id, quantity, amount (positive — gross cash received), posted_at, fee?, memo?.",
			InputSchema: schemaObject(map[string]any{
				"account_id":    map[string]any{"type": "integer"},
				"instrument_id": map[string]any{"type": "integer"},
				"quantity":      map[string]any{"type": "number"},
				"amount":        map[string]any{"type": "integer"},
				"posted_at":     map[string]any{"type": "string"},
				"fee":           map[string]any{"type": "integer"},
				"memo":          map[string]any{"type": "string"},
			}, []string{"account_id", "instrument_id", "quantity", "amount", "posted_at"}),
			Handler: a.toolTxnsSell},
		{Name: "txns_dividend", Description: "Record a dividend (cash credit on a holding). Args: account_id, instrument_id, amount, posted_at, memo?.",
			InputSchema: schemaObject(map[string]any{
				"account_id":    map[string]any{"type": "integer"},
				"instrument_id": map[string]any{"type": "integer"},
				"amount":        map[string]any{"type": "integer"},
				"posted_at":     map[string]any{"type": "string"},
				"memo":          map[string]any{"type": "string"},
			}, []string{"account_id", "instrument_id", "amount", "posted_at"}),
			Handler: a.toolTxnsDividend},
		{Name: "txns_interest", Description: "Record interest income (P2P / savings). Args: account_id, amount, posted_at, instrument_id?, memo?.",
			InputSchema: schemaObject(map[string]any{
				"account_id":    map[string]any{"type": "integer"},
				"instrument_id": map[string]any{"type": "integer"},
				"amount":        map[string]any{"type": "integer"},
				"posted_at":     map[string]any{"type": "string"},
				"memo":          map[string]any{"type": "string"},
			}, []string{"account_id", "amount", "posted_at"}),
			Handler: a.toolTxnsInterest},
		{Name: "txns_transfer", Description: "Move cash between two accounts in the same currency. Args: from_account_id, to_account_id, amount, posted_at, memo?.",
			InputSchema: schemaObject(map[string]any{
				"from_account_id": map[string]any{"type": "integer"},
				"to_account_id":   map[string]any{"type": "integer"},
				"amount":          map[string]any{"type": "integer"},
				"posted_at":       map[string]any{"type": "string"},
				"memo":            map[string]any{"type": "string"},
			}, []string{"from_account_id", "to_account_id", "amount", "posted_at"}),
			Handler: a.toolTxnsTransfer},

		{Name: "valuation_set", Description: "Re-value an illiquid holding. Writes a `prices` row + audit 'valuation' transaction. Args: instrument_id, value (per-unit minor units in instrument.quote_currency), as_of?, account_id?, memo?.",
			InputSchema: schemaObject(map[string]any{
				"instrument_id": map[string]any{"type": "integer"},
				"value":         map[string]any{"type": "integer"},
				"as_of":         map[string]any{"type": "string"},
				"account_id":    map[string]any{"type": "integer"},
				"memo":          map[string]any{"type": "string"},
			}, []string{"instrument_id", "value"}),
			Handler: a.toolValuationSet},

		{Name: "categories_list", Description: "List categories.",
			InputSchema: schemaObject(map[string]any{}, nil),
			Handler:     a.toolCategoriesList},
		{Name: "categories_create", Description: "Create a category. Args: name, kind (income|expense), parent_id?, color?.",
			InputSchema: schemaObject(map[string]any{
				"name":      map[string]any{"type": "string"},
				"kind":      map[string]any{"type": "string", "enum": []string{"income", "expense"}},
				"parent_id": map[string]any{"type": "integer"},
				"color":     map[string]any{"type": "string"},
			}, []string{"name", "kind"}),
			Handler: a.toolCategoriesCreate},
		{Name: "categories_update", Description: "Update a category. Args: id, name?, color?, archived?, parent_id?.",
			InputSchema: schemaObject(map[string]any{
				"id":        map[string]any{"type": "integer"},
				"name":      map[string]any{"type": "string"},
				"color":     map[string]any{"type": "string"},
				"archived":  map[string]any{"type": "boolean"},
				"parent_id": map[string]any{"type": "integer"},
			}, []string{"id"}),
			Handler: a.toolCategoriesUpdate},
		{Name: "categories_delete", Description: "Delete a category. Transactions referencing it get NULL category.",
			InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}}, []string{"id"}),
			Handler:     a.toolCategoriesDelete},
		{Name: "categories_seed", Description: "Idempotent seed of a default category tree.",
			InputSchema: schemaObject(map[string]any{}, nil),
			Handler:     a.toolCategoriesSeed},

		{Name: "prices_set", Description: "Manually set a price for an instrument at as_of. Args: instrument_id, price (minor units, instrument.quote_currency), as_of?, source?.",
			InputSchema: schemaObject(map[string]any{
				"instrument_id": map[string]any{"type": "integer"},
				"price":         map[string]any{"type": "integer"},
				"as_of":         map[string]any{"type": "string"},
				"source":        map[string]any{"type": "string"},
			}, []string{"instrument_id", "price"}),
			Handler: a.toolPricesSet},
		{Name: "prices_get", Description: "Get the latest price ≤ as_of for an instrument. Args: instrument_id, as_of? (default now).",
			InputSchema: schemaObject(map[string]any{
				"instrument_id": map[string]any{"type": "integer"},
				"as_of":         map[string]any{"type": "string"},
			}, []string{"instrument_id"}),
			Handler: a.toolPricesGet},

		{Name: "fx_set", Description: "Manually set an FX rate. Args: base, quote, rate (1 base = rate quote), as_of?.",
			InputSchema: schemaObject(map[string]any{
				"base":  map[string]any{"type": "string"},
				"quote": map[string]any{"type": "string"},
				"rate":  map[string]any{"type": "number"},
				"as_of": map[string]any{"type": "string"},
			}, []string{"base", "quote", "rate"}),
			Handler: a.toolFXSet},

		{Name: "reports_net_worth", Description: "Net worth at a point or as a series. Args: at?, series? ('weekly'|'monthly'), from?, to?.",
			InputSchema: schemaObject(map[string]any{
				"at":     map[string]any{"type": "string"},
				"series": map[string]any{"type": "string", "enum": []string{"weekly", "monthly"}},
				"from":   map[string]any{"type": "string"},
				"to":     map[string]any{"type": "string"},
			}, nil),
			Handler: a.toolReportsNetWorth},
		{Name: "reports_allocation", Description: "Current allocation: by account.kind, instrument.kind, currency, top instruments.",
			InputSchema: schemaObject(map[string]any{}, nil),
			Handler:     a.toolReportsAllocation},
		{Name: "reports_performance", Description: "Per-holding and portfolio P&L. Args: from?, to?.",
			InputSchema: schemaObject(map[string]any{
				"from": map[string]any{"type": "string"},
				"to":   map[string]any{"type": "string"},
			}, nil),
			Handler: a.toolReportsPerformance},
		{Name: "reports_cashflow", Description: "Income vs expense per period bucket. Args: from, to, bucket? (weekly|monthly).",
			InputSchema: schemaObject(map[string]any{
				"from":   map[string]any{"type": "string"},
				"to":     map[string]any{"type": "string"},
				"bucket": map[string]any{"type": "string", "enum": []string{"weekly", "monthly"}},
			}, []string{"from", "to"}),
			Handler: a.toolReportsCashflow},

		{Name: "import_csv", Description: "Import CSV of transactions into an account. Args: account_id, csv (string), mapping (column-name → field; supports date, amount, memo, payee, kind?).",
			InputSchema: schemaObject(map[string]any{
				"account_id": map[string]any{"type": "integer"},
				"csv":        map[string]any{"type": "string"},
				"mapping":    map[string]any{"type": "object"},
			}, []string{"account_id", "csv", "mapping"}),
			Handler: a.toolImportCSV},

		{Name: "budgets_list", Description: "List active budgets (one per (category, period) pair). category_id NULL means a top-level 'total spend' budget across all expense categories.",
			InputSchema: schemaObject(map[string]any{}, nil),
			Handler:     a.toolBudgetsList},
		{Name: "budgets_set", Description: "Upsert a budget. Args: category_id? (NULL for total-spend), period? (weekly|monthly|quarterly|yearly; default monthly), amount (minor units, base currency).",
			InputSchema: schemaObject(map[string]any{
				"category_id": map[string]any{"type": "integer"},
				"period":      map[string]any{"type": "string", "enum": []string{"weekly", "monthly", "quarterly", "yearly"}},
				"amount":      map[string]any{"type": "integer"},
			}, []string{"amount"}),
			Handler: a.toolBudgetsSet},
		{Name: "budgets_delete", Description: "Delete a budget. Args: id.",
			InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}}, []string{"id"}),
			Handler:     a.toolBudgetsDelete},
		{Name: "budgets_status", Description: "For each active budget in this period kind, return budgeted/spent/remaining/pct_used/over for the period containing as_of. Hierarchical: a budget on parent category counts spending in all descendants. Spent kinds: expense, fee, tax. Args: as_of?, period? (default monthly).",
			InputSchema: schemaObject(map[string]any{
				"as_of":  map[string]any{"type": "string"},
				"period": map[string]any{"type": "string", "enum": []string{"weekly", "monthly", "quarterly", "yearly"}},
			}, nil),
			Handler: a.toolBudgetsStatus},
	}
}

func main() { sdk.Run(&App{}) }

// ─── Constants ───────────────────────────────────────────────────

func accountKinds() []string {
	return []string{"cash", "brokerage", "p2p", "crypto", "real_estate", "vehicle", "pension", "loan", "other"}
}
func instrumentKinds() []string {
	return []string{"stock", "etf", "fund", "bond", "crypto", "cash", "p2p", "real_estate", "vehicle", "other"}
}

// cashAffectingKinds is the set of transaction kinds whose `amount`
// contributes to an account's cash balance. `valuation` is the lone
// non-affecting kind (it's an audit marker; the price-table row is
// the real revaluation).
func isCashAffecting(kind string) bool { return kind != "valuation" }

// isInstrumentShared returns true for instrument kinds that live in
// the global catalog (project_id NULL) — AAPL is AAPL for everyone.
// Real estate, vehicles, p2p, and "other" are private (project-scoped).
func isInstrumentShared(kind string) bool {
	switch kind {
	case "stock", "etf", "fund", "bond", "crypto", "cash":
		return true
	}
	return false
}

// ─── Types ───────────────────────────────────────────────────────

type Settings struct {
	ProjectID     string `json:"project_id"`
	BaseCurrency  string `json:"base_currency"`
	WeekStartsOn  string `json:"week_starts_on"`
}

type Account struct {
	ID             int64  `json:"id"`
	ProjectID      string `json:"project_id"`
	Name           string `json:"name"`
	Kind           string `json:"kind"`
	Source         string `json:"source"`
	ConnectionID   string `json:"connection_id,omitempty"`
	ExternalID     string `json:"external_id,omitempty"`
	Currency       string `json:"currency"`
	OpeningBalance int64  `json:"opening_balance"`
	OpeningAt      string `json:"opening_at"`
	Color          string `json:"color"`
	Archived       bool   `json:"archived"`
	LastSyncAt     string `json:"last_sync_at,omitempty"`
	SyncError      string `json:"sync_error,omitempty"`
	CreatedAt      string `json:"created_at"`
	// Computed
	CashBalance  int64 `json:"cash_balance"`
	HoldingsValue int64 `json:"holdings_value"`  // in account.currency
	TotalValue   int64  `json:"total_value"`     // cash + holdings, account.currency
}

type Instrument struct {
	ID            int64           `json:"id"`
	ProjectID     *string         `json:"project_id,omitempty"`
	Kind          string          `json:"kind"`
	Symbol        string          `json:"symbol"`
	Name          string          `json:"name"`
	ISIN          string          `json:"isin,omitempty"`
	Exchange      string          `json:"exchange,omitempty"`
	QuoteCurrency string          `json:"quote_currency"`
	Metadata      json.RawMessage `json:"metadata"`
	CreatedAt     string          `json:"created_at"`
}

type Holding struct {
	ID            int64   `json:"id"`
	AccountID     int64   `json:"account_id"`
	InstrumentID  int64   `json:"instrument_id"`
	Quantity      float64 `json:"quantity"`
	CostBasis     int64   `json:"cost_basis"`
	OpenedAt      string  `json:"opened_at,omitempty"`
	ClosedAt      string  `json:"closed_at,omitempty"`
	// Computed
	CurrentPrice  *int64  `json:"current_price,omitempty"`
	CurrentValue  int64   `json:"current_value"`   // in account.currency
	UnrealizedPL  int64   `json:"unrealized_pl"`
	UnrealizedPct float64 `json:"unrealized_pct"`
}

type Transaction struct {
	ID             int64   `json:"id"`
	AccountID      int64   `json:"account_id"`
	HoldingID      int64   `json:"holding_id,omitempty"`
	PostedAt       string  `json:"posted_at"`
	Kind           string  `json:"kind"`
	Amount         int64   `json:"amount"`
	Currency       string  `json:"currency"`
	Quantity       float64 `json:"quantity"`
	Price          *int64  `json:"price,omitempty"`
	CostBasisDelta int64   `json:"cost_basis_delta"`
	Payee          string  `json:"payee"`
	Memo           string  `json:"memo"`
	CategoryID     int64   `json:"category_id,omitempty"`
	TransferID     string  `json:"transfer_id,omitempty"`
	ExternalID     string  `json:"external_id,omitempty"`
	Pending        bool    `json:"pending"`
	CreatedAt      string  `json:"created_at"`
	UpdatedAt      string  `json:"updated_at"`
}

type Category struct {
	ID        int64  `json:"id"`
	ProjectID string `json:"project_id"`
	ParentID  int64  `json:"parent_id,omitempty"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Color     string `json:"color"`
	Archived  bool   `json:"archived"`
	CreatedAt string `json:"created_at"`
}

type Price struct {
	InstrumentID int64  `json:"instrument_id"`
	AsOf         string `json:"as_of"`
	Price        int64  `json:"price"`
	Source       string `json:"source"`
}

// instrumentTotal is the per-instrument row in the allocation report's
// `top_instruments` slice. Exported-ish (tests reach for the type
// name) but kept lower-case because the JSON tags are what callers
// see, not the Go field names.
type instrumentTotal struct {
	ID     int64  `json:"id"`
	Symbol string `json:"symbol"`
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Value  int64  `json:"value"`
}

type Budget struct {
	ID         int64  `json:"id"`
	ProjectID  string `json:"project_id"`
	CategoryID int64  `json:"category_id,omitempty"` // 0 means "total spend" (NULL in DB)
	Period     string `json:"period"`
	Amount     int64  `json:"amount"`
	Currency   string `json:"currency"`
	StartsAt   string `json:"starts_at"`
	Archived   bool   `json:"archived"`
	CreatedAt  string `json:"created_at"`
}

// BudgetStatus is one row of budgets_status output.
type BudgetStatus struct {
	BudgetID     int64   `json:"budget_id"`
	CategoryID   int64   `json:"category_id,omitempty"` // 0 = total
	CategoryName string  `json:"category_name"`
	Period       string  `json:"period"`
	PeriodStart  string  `json:"period_start"`
	PeriodEnd    string  `json:"period_end"`
	Budgeted     int64   `json:"budgeted"`
	Spent        int64   `json:"spent"`
	Remaining    int64   `json:"remaining"`
	PctUsed      float64 `json:"pct_used"`
	Over         bool    `json:"over"`
	Currency     string  `json:"currency"`
}

// ─── Settings ────────────────────────────────────────────────────

func (a *App) toolSettingsGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return loadSettings(ctx)
}

func (a *App) toolSettingsSet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid := projectID()
	cols, vals := []string{}, []any{}
	if v := strArg(args, "base_currency", ""); v != "" {
		cols = append(cols, "base_currency=?")
		vals = append(vals, strings.ToUpper(v))
	}
	if v := strArg(args, "week_starts_on", ""); v != "" {
		cols = append(cols, "week_starts_on=?")
		vals = append(vals, strings.ToLower(v))
	}
	if len(cols) == 0 {
		return nil, errors.New("no updatable fields supplied — pass at least one of: base_currency, week_starts_on")
	}
	cols = append(cols, "updated_at=CURRENT_TIMESTAMP")
	vals = append(vals, pid)
	_, _ = ctx.AppDB().Exec(`INSERT OR IGNORE INTO settings (project_id) VALUES (?)`, pid)
	if _, err := ctx.AppDB().Exec(
		`UPDATE settings SET `+strings.Join(cols, ", ")+` WHERE project_id=?`, vals...,
	); err != nil {
		return nil, err
	}
	return loadSettings(ctx)
}

func loadSettings(ctx *sdk.AppCtx) (Settings, error) {
	pid := projectID()
	var s Settings
	err := ctx.AppDB().QueryRow(
		`SELECT project_id, base_currency, week_starts_on FROM settings WHERE project_id=?`, pid,
	).Scan(&s.ProjectID, &s.BaseCurrency, &s.WeekStartsOn)
	if errors.Is(err, sql.ErrNoRows) {
		s = Settings{ProjectID: pid, BaseCurrency: "EUR", WeekStartsOn: "mon"}
		_, _ = ctx.AppDB().Exec(
			`INSERT OR IGNORE INTO settings (project_id, base_currency) VALUES (?, ?)`,
			pid, s.BaseCurrency)
		return s, nil
	}
	return s, err
}

// ─── Accounts ────────────────────────────────────────────────────

func (a *App) toolAccountsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid := projectID()
	rows, err := ctx.AppDB().Query(
		`SELECT id, project_id, name, kind, source, COALESCE(connection_id,''),
		        COALESCE(external_id,''), currency, opening_balance, opening_at,
		        color, archived, COALESCE(last_sync_at,''), COALESCE(sync_error,''),
		        created_at
		 FROM accounts WHERE project_id=? ORDER BY id`,
		pid,
	)
	if err != nil {
		return nil, err
	}
	// Drain the cursor BEFORE running any nested queries — testkit
	// pins MaxOpenConns(1), so holding rows open + calling QueryRow
	// inside the loop deadlocks the test pool.
	bare := []Account{}
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			continue
		}
		bare = append(bare, a)
	}
	rows.Close()
	out := make([]Account, 0, len(bare))
	for _, a := range bare {
		a.CashBalance = mustCashBalance(ctx, a.ID, a.OpeningBalance)
		a.HoldingsValue = mustHoldingsValue(ctx, a.ID, a.Currency)
		a.TotalValue = a.CashBalance + a.HoldingsValue
		out = append(out, a)
	}
	return map[string]any{"accounts": out}, nil
}

func (a *App) toolAccountsGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64(intArg(args, "id", 0))
	if id == 0 {
		return nil, errors.New("id required")
	}
	acc, err := readAccount(ctx, id)
	if err != nil {
		return nil, err
	}
	acc.CashBalance = mustCashBalance(ctx, id, acc.OpeningBalance)
	acc.HoldingsValue = mustHoldingsValue(ctx, id, acc.Currency)
	acc.TotalValue = acc.CashBalance + acc.HoldingsValue
	holdings, _ := listHoldingsRich(ctx, id, 0, true)
	return map[string]any{"account": acc, "holdings": holdings}, nil
}

func (a *App) toolAccountsCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	name := strings.TrimSpace(strArg(args, "name", ""))
	kind := strArg(args, "kind", "")
	if name == "" {
		return nil, errors.New("name required")
	}
	if !contains(accountKinds(), kind) {
		return nil, fmt.Errorf("kind must be one of %v", accountKinds())
	}
	currency := strings.ToUpper(strArg(args, "currency", "EUR"))
	color := strArg(args, "color", "#3b82f6")
	openBal := int64(intArg(args, "opening_balance", 0))
	openAt := strArg(args, "opening_at", time.Now().UTC().Format(time.RFC3339))
	pid := projectID()
	res, err := ctx.AppDB().Exec(
		`INSERT INTO accounts (project_id, name, kind, currency, opening_balance, opening_at, color)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		pid, name, kind, currency, openBal, openAt, color,
	)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	ctx.Emit("account.created", map[string]any{"account_id": id, "kind": kind, "currency": currency})
	return readAccount(ctx, id)
}

func (a *App) toolAccountsUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64(intArg(args, "id", 0))
	if id == 0 {
		return nil, errors.New("id required")
	}
	cols, vals := []string{}, []any{}
	for _, k := range []string{"name", "color", "opening_at"} {
		if v, ok := args[k].(string); ok && v != "" {
			cols = append(cols, k+"=?")
			vals = append(vals, v)
		}
	}
	if v, ok := args["archived"].(bool); ok {
		cols = append(cols, "archived=?")
		vals = append(vals, boolToInt(v))
	}
	if v, ok := args["opening_balance"]; ok {
		cols = append(cols, "opening_balance=?")
		vals = append(vals, int64(intArgFromAny(v, 0)))
	}
	if len(cols) == 0 {
		return nil, errors.New("no updatable fields supplied — pass at least one of: name, color, archived, opening_balance, opening_at")
	}
	vals = append(vals, id)
	if _, err := ctx.AppDB().Exec(
		`UPDATE accounts SET `+strings.Join(cols, ", ")+` WHERE id=?`, vals...,
	); err != nil {
		return nil, err
	}
	ctx.Emit("account.updated", map[string]any{"account_id": id})
	return readAccount(ctx, id)
}

func (a *App) toolAccountsDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64(intArg(args, "id", 0))
	if id == 0 {
		return nil, errors.New("id required")
	}
	if _, err := ctx.AppDB().Exec(`DELETE FROM accounts WHERE id=?`, id); err != nil {
		return nil, err
	}
	ctx.Emit("account.deleted", map[string]any{"account_id": id})
	return map[string]any{"deleted": id}, nil
}

func readAccount(ctx *sdk.AppCtx, id int64) (Account, error) {
	row := ctx.AppDB().QueryRow(
		`SELECT id, project_id, name, kind, source, COALESCE(connection_id,''),
		        COALESCE(external_id,''), currency, opening_balance, opening_at,
		        color, archived, COALESCE(last_sync_at,''), COALESCE(sync_error,''),
		        created_at
		 FROM accounts WHERE id=?`, id,
	)
	return scanAccount(row)
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanAccount(r rowScanner) (Account, error) {
	var a Account
	var arch int
	if err := r.Scan(&a.ID, &a.ProjectID, &a.Name, &a.Kind, &a.Source,
		&a.ConnectionID, &a.ExternalID, &a.Currency, &a.OpeningBalance,
		&a.OpeningAt, &a.Color, &arch, &a.LastSyncAt, &a.SyncError,
		&a.CreatedAt); err != nil {
		return a, err
	}
	a.Archived = arch == 1
	return a, nil
}

// mustCashBalance returns the derived cash balance, swallowing query
// errors as 0 so list endpoints don't fail the whole response on one
// bad account.
func mustCashBalance(ctx *sdk.AppCtx, accountID, opening int64) int64 {
	var sum sql.NullInt64
	err := ctx.AppDB().QueryRow(
		`SELECT COALESCE(SUM(amount), 0) FROM transactions
		 WHERE account_id=? AND kind != 'valuation'`,
		accountID,
	).Scan(&sum)
	if err != nil {
		return opening
	}
	return opening + sum.Int64
}

// mustHoldingsValue returns the sum of (quantity × latest_price) for
// every open holding on this account, converted to the account's
// currency via the latest FX snapshot.
func mustHoldingsValue(ctx *sdk.AppCtx, accountID int64, accountCcy string) int64 {
	rows, err := ctx.AppDB().Query(
		`SELECT h.instrument_id, h.quantity, i.quote_currency
		 FROM holdings h JOIN instruments i ON i.id = h.instrument_id
		 WHERE h.account_id=? AND (h.closed_at IS NULL OR h.closed_at='')`,
		accountID,
	)
	if err != nil {
		return 0
	}
	type drained struct {
		iid      int64
		qty      float64
		quoteCcy string
	}
	bare := []drained{}
	for rows.Next() {
		var d drained
		if err := rows.Scan(&d.iid, &d.qty, &d.quoteCcy); err == nil {
			bare = append(bare, d)
		}
	}
	rows.Close()
	var total int64
	now := time.Now().UTC()
	for _, d := range bare {
		price, ok := latestPrice(ctx, d.iid, now)
		if !ok {
			continue
		}
		valueInQuote := int64(math.Round(d.qty * float64(price)))
		total += convertCcy(ctx, valueInQuote, d.quoteCcy, accountCcy)
	}
	return total
}

// ─── Instruments ─────────────────────────────────────────────────

func (a *App) toolInstrumentsSearch(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	q := strings.TrimSpace(strArg(args, "query", ""))
	if q == "" {
		return nil, errors.New("query required")
	}
	pid := projectID()
	kindFilter := strArg(args, "kind", "")
	projectOnly, _ := args["project_only"].(bool)

	sqlBase := `SELECT id, project_id, kind, symbol, name, COALESCE(isin,''),
	                   COALESCE(exchange,''), quote_currency, metadata, created_at
	            FROM instruments
	            WHERE (LOWER(symbol) LIKE ? OR LOWER(name) LIKE ? OR LOWER(COALESCE(isin,'')) LIKE ?)`
	like := "%" + strings.ToLower(q) + "%"
	params := []any{like, like, like}

	if projectOnly {
		sqlBase += " AND project_id = ?"
		params = append(params, pid)
	} else {
		sqlBase += " AND (project_id IS NULL OR project_id = ?)"
		params = append(params, pid)
	}
	if kindFilter != "" {
		sqlBase += " AND kind = ?"
		params = append(params, kindFilter)
	}
	sqlBase += " ORDER BY (project_id IS NOT NULL) DESC, symbol LIMIT 50"

	rows, err := ctx.AppDB().Query(sqlBase, params...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Instrument{}
	for rows.Next() {
		ins, err := scanInstrument(rows)
		if err != nil {
			continue
		}
		out = append(out, ins)
	}
	return map[string]any{"instruments": out}, nil
}

func (a *App) toolInstrumentsGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64(intArg(args, "id", 0))
	if id == 0 {
		return nil, errors.New("id required")
	}
	return readInstrument(ctx, id)
}

func (a *App) toolInstrumentsCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	kind := strArg(args, "kind", "")
	symbol := strings.TrimSpace(strArg(args, "symbol", ""))
	name := strings.TrimSpace(strArg(args, "name", ""))
	quoteCcy := strings.ToUpper(strArg(args, "quote_currency", ""))
	if !contains(instrumentKinds(), kind) {
		return nil, fmt.Errorf("kind must be one of %v", instrumentKinds())
	}
	if symbol == "" || name == "" || quoteCcy == "" {
		return nil, errors.New("symbol, name, quote_currency required")
	}
	// Shared catalog kinds default to project_id NULL; project_only
	// flag forces a private row. Private kinds are always scoped.
	pid := projectID()
	var projectScope sql.NullString
	if isInstrumentShared(kind) {
		projectOnly, _ := args["project_only"].(bool)
		if projectOnly {
			projectScope = sql.NullString{String: pid, Valid: true}
		}
	} else {
		projectScope = sql.NullString{String: pid, Valid: true}
	}
	isin := strArg(args, "isin", "")
	exchange := strArg(args, "exchange", "")
	metaJSON := []byte("{}")
	if m, ok := args["metadata"].(map[string]any); ok {
		b, _ := json.Marshal(m)
		metaJSON = b
	}
	res, err := ctx.AppDB().Exec(
		`INSERT INTO instruments (project_id, kind, symbol, name, isin, exchange, quote_currency, metadata)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		nullableString(projectScope), kind, symbol, name,
		nullIfEmpty(isin), nullIfEmpty(exchange), quoteCcy, string(metaJSON),
	)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	ctx.Emit("instrument.created", map[string]any{"instrument_id": id, "symbol": symbol, "kind": kind})
	return readInstrument(ctx, id)
}

func (a *App) toolInstrumentsUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64(intArg(args, "id", 0))
	if id == 0 {
		return nil, errors.New("id required")
	}
	cols, vals := []string{}, []any{}
	for _, k := range []string{"name", "exchange"} {
		if v, ok := args[k].(string); ok && v != "" {
			cols = append(cols, k+"=?")
			vals = append(vals, v)
		}
	}
	if m, ok := args["metadata"].(map[string]any); ok {
		b, _ := json.Marshal(m)
		cols = append(cols, "metadata=?")
		vals = append(vals, string(b))
	}
	if len(cols) == 0 {
		return nil, errors.New("no updatable fields supplied — pass at least one of: name, exchange, metadata")
	}
	vals = append(vals, id)
	if _, err := ctx.AppDB().Exec(
		`UPDATE instruments SET `+strings.Join(cols, ", ")+` WHERE id=?`, vals...,
	); err != nil {
		return nil, err
	}
	return readInstrument(ctx, id)
}

func readInstrument(ctx *sdk.AppCtx, id int64) (Instrument, error) {
	row := ctx.AppDB().QueryRow(
		`SELECT id, project_id, kind, symbol, name, COALESCE(isin,''),
		        COALESCE(exchange,''), quote_currency, metadata, created_at
		 FROM instruments WHERE id=?`, id,
	)
	return scanInstrument(row)
}

func scanInstrument(r rowScanner) (Instrument, error) {
	var ins Instrument
	var projectID sql.NullString
	var meta string
	if err := r.Scan(&ins.ID, &projectID, &ins.Kind, &ins.Symbol, &ins.Name,
		&ins.ISIN, &ins.Exchange, &ins.QuoteCurrency, &meta, &ins.CreatedAt); err != nil {
		return ins, err
	}
	if projectID.Valid {
		s := projectID.String
		ins.ProjectID = &s
	}
	ins.Metadata = json.RawMessage(meta)
	return ins, nil
}

// ─── Holdings ────────────────────────────────────────────────────

func (a *App) toolHoldingsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	accountID := int64(intArg(args, "account_id", 0))
	instrumentID := int64(intArg(args, "instrument_id", 0))
	includeClosed, _ := args["include_closed"].(bool)
	out, err := listHoldingsRich(ctx, accountID, instrumentID, includeClosed)
	if err != nil {
		return nil, err
	}
	return map[string]any{"holdings": out}, nil
}

func (a *App) toolHoldingsGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64(intArg(args, "id", 0))
	if id == 0 {
		return nil, errors.New("id required")
	}
	rows, err := listHoldingsRich(ctx, 0, 0, true)
	if err != nil {
		return nil, err
	}
	for _, h := range rows {
		if h.ID == id {
			return h, nil
		}
	}
	return nil, fmt.Errorf("holding %d not found", id)
}

func (a *App) toolHoldingsSet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	accountID := int64(intArg(args, "account_id", 0))
	instrumentID := int64(intArg(args, "instrument_id", 0))
	qty, _ := args["quantity"].(float64)
	if accountID == 0 || instrumentID == 0 {
		return nil, errors.New("account_id and instrument_id required")
	}
	cb := int64(intArg(args, "cost_basis", 0))
	// Upsert.
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var existing int64
	row := tx.QueryRow(`SELECT id FROM holdings WHERE account_id=? AND instrument_id=?`, accountID, instrumentID)
	if err := row.Scan(&existing); err == nil {
		if _, err := tx.Exec(
			`UPDATE holdings SET quantity=?, cost_basis=?, closed_at=NULL WHERE id=?`,
			qty, cb, existing,
		); err != nil {
			return nil, err
		}
	} else {
		now := time.Now().UTC().Format(time.RFC3339)
		res, err := tx.Exec(
			`INSERT INTO holdings (account_id, instrument_id, quantity, cost_basis, opened_at) VALUES (?, ?, ?, ?, ?)`,
			accountID, instrumentID, qty, cb, now,
		)
		if err != nil {
			return nil, err
		}
		existing, _ = res.LastInsertId()
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return readHolding(ctx, existing)
}

// listHoldingsRich returns holdings with computed current_value /
// unrealised P&L populated. If accountID or instrumentID is 0 the
// corresponding filter is dropped.
func listHoldingsRich(ctx *sdk.AppCtx, accountID, instrumentID int64, includeClosed bool) ([]Holding, error) {
	q := `SELECT h.id, h.account_id, h.instrument_id, h.quantity, h.cost_basis,
	             COALESCE(h.opened_at,''), COALESCE(h.closed_at,''),
	             i.quote_currency, a.currency
	      FROM holdings h
	      JOIN instruments i ON i.id = h.instrument_id
	      JOIN accounts    a ON a.id = h.account_id
	      WHERE 1=1`
	params := []any{}
	if accountID != 0 {
		q += " AND h.account_id=?"
		params = append(params, accountID)
	}
	if instrumentID != 0 {
		q += " AND h.instrument_id=?"
		params = append(params, instrumentID)
	}
	if !includeClosed {
		q += " AND (h.closed_at IS NULL OR h.closed_at='')"
	}
	q += " ORDER BY h.id"
	rows, err := ctx.AppDB().Query(q, params...)
	if err != nil {
		return nil, err
	}
	type drained struct {
		h        Holding
		quoteCcy string
		accCcy   string
	}
	bare := []drained{}
	for rows.Next() {
		var d drained
		if err := rows.Scan(&d.h.ID, &d.h.AccountID, &d.h.InstrumentID, &d.h.Quantity,
			&d.h.CostBasis, &d.h.OpenedAt, &d.h.ClosedAt, &d.quoteCcy, &d.accCcy); err == nil {
			bare = append(bare, d)
		}
	}
	rows.Close()
	out := make([]Holding, 0, len(bare))
	now := time.Now().UTC()
	for _, d := range bare {
		h := d.h
		if price, ok := latestPrice(ctx, h.InstrumentID, now); ok {
			h.CurrentPrice = &price
			valueInQuote := int64(math.Round(h.Quantity * float64(price)))
			h.CurrentValue = convertCcy(ctx, valueInQuote, d.quoteCcy, d.accCcy)
			h.UnrealizedPL = h.CurrentValue - h.CostBasis
			if h.CostBasis != 0 {
				h.UnrealizedPct = float64(h.UnrealizedPL) / float64(absInt64(h.CostBasis)) * 100
			}
		}
		out = append(out, h)
	}
	return out, nil
}

func readHolding(ctx *sdk.AppCtx, id int64) (Holding, error) {
	rows, err := listHoldingsRich(ctx, 0, 0, true)
	if err != nil {
		return Holding{}, err
	}
	for _, h := range rows {
		if h.ID == id {
			return h, nil
		}
	}
	return Holding{}, fmt.Errorf("holding %d not found", id)
}

// upsertHolding bumps a holding's quantity and cost_basis atomically.
// Used by buy / dividend-in-kind. Returns the holding id.
func upsertHolding(tx *sql.Tx, accountID, instrumentID int64, dQty float64, dCost int64) (int64, error) {
	var id int64
	var qty float64
	var cost int64
	row := tx.QueryRow(`SELECT id, quantity, cost_basis FROM holdings WHERE account_id=? AND instrument_id=?`, accountID, instrumentID)
	if err := row.Scan(&id, &qty, &cost); err == nil {
		newQty := qty + dQty
		newCost := cost + dCost
		_, err := tx.Exec(
			`UPDATE holdings SET quantity=?, cost_basis=?, closed_at=NULL WHERE id=?`,
			newQty, newCost, id,
		)
		return id, err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := tx.Exec(
		`INSERT INTO holdings (account_id, instrument_id, quantity, cost_basis, opened_at) VALUES (?, ?, ?, ?, ?)`,
		accountID, instrumentID, dQty, dCost, now,
	)
	if err != nil {
		return 0, err
	}
	id, _ = res.LastInsertId()
	return id, nil
}

// decreaseHolding applies a sell: proportionally releases cost_basis,
// decrements quantity. Returns (released_cost_basis, holding_id) so
// the txn row can record both the cash impact and the cost-basis side.
// When the resulting quantity is ≤ 0 the row is marked closed.
func decreaseHolding(tx *sql.Tx, accountID, instrumentID int64, qtySold float64) (int64, int64, error) {
	var id int64
	var qty float64
	var cost int64
	row := tx.QueryRow(`SELECT id, quantity, cost_basis FROM holdings WHERE account_id=? AND instrument_id=?`, accountID, instrumentID)
	if err := row.Scan(&id, &qty, &cost); err != nil {
		return 0, 0, fmt.Errorf("no holding for account=%d instrument=%d — buy before selling", accountID, instrumentID)
	}
	if qtySold <= 0 || qtySold > qty+1e-9 {
		return 0, 0, fmt.Errorf("can't sell %g — holding has %g", qtySold, qty)
	}
	frac := qtySold / qty
	released := int64(math.Round(float64(cost) * frac))
	newQty := qty - qtySold
	newCost := cost - released
	closedAt := sql.NullString{}
	if newQty <= 1e-9 {
		newQty = 0
		closedAt = sql.NullString{String: time.Now().UTC().Format(time.RFC3339), Valid: true}
	}
	if _, err := tx.Exec(
		`UPDATE holdings SET quantity=?, cost_basis=?, closed_at=? WHERE id=?`,
		newQty, newCost, nullableString(closedAt), id,
	); err != nil {
		return 0, 0, err
	}
	return released, id, nil
}

// ─── Transactions: generic ───────────────────────────────────────

func (a *App) toolTxnsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	q, params := buildTxnQuery(args)
	limit := intArg(args, "limit", 200)
	if limit <= 0 || limit > 5000 {
		limit = 200
	}
	q += " ORDER BY posted_at DESC, id DESC LIMIT ?"
	params = append(params, limit)
	rows, err := ctx.AppDB().Query(q, params...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Transaction{}
	for rows.Next() {
		t, err := scanTxn(rows)
		if err != nil {
			continue
		}
		out = append(out, t)
	}
	return map[string]any{"transactions": out, "limit": limit}, nil
}

func buildTxnQuery(args map[string]any) (string, []any) {
	q := `SELECT id, account_id, COALESCE(holding_id,0), posted_at, kind, amount, currency,
	             quantity, price, cost_basis_delta, payee, memo, COALESCE(category_id,0),
	             COALESCE(transfer_id,''), COALESCE(external_id,''), pending,
	             created_at, updated_at
	      FROM transactions WHERE 1=1`
	params := []any{}
	if v := int64(intArg(args, "account_id", 0)); v != 0 {
		q += " AND account_id=?"
		params = append(params, v)
	}
	if v := int64(intArg(args, "holding_id", 0)); v != 0 {
		q += " AND holding_id=?"
		params = append(params, v)
	}
	if v := strArg(args, "kind", ""); v != "" {
		q += " AND kind=?"
		params = append(params, v)
	}
	if v := int64(intArg(args, "category_id", 0)); v != 0 {
		q += " AND category_id=?"
		params = append(params, v)
	}
	if v := strArg(args, "from", ""); v != "" {
		q += " AND posted_at >= ?"
		params = append(params, v)
	}
	if v := strArg(args, "to", ""); v != "" {
		q += " AND posted_at < ?"
		params = append(params, v)
	}
	if v := strArg(args, "payee", ""); v != "" {
		q += " AND LOWER(payee) LIKE ?"
		params = append(params, "%"+strings.ToLower(v)+"%")
	}
	return q, params
}

func (a *App) toolTxnsGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64(intArg(args, "id", 0))
	if id == 0 {
		return nil, errors.New("id required")
	}
	return readTxn(ctx, id)
}

func (a *App) toolTxnsCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	accountID := int64(intArg(args, "account_id", 0))
	kind := strArg(args, "kind", "")
	if accountID == 0 || kind == "" {
		return nil, errors.New("account_id and kind required")
	}
	if !contains([]string{"deposit", "withdraw", "income", "expense", "fee", "tax"}, kind) {
		return nil, fmt.Errorf("kind %q is for the convenience tools — use txns_create for deposit/withdraw/income/expense/fee/tax", kind)
	}
	amount := int64(intArg(args, "amount", 0))
	if amount == 0 {
		return nil, errors.New("amount required (non-zero, signed minor units)")
	}
	// Sign convention: deposit/income amounts must be positive,
	// withdraw/expense/fee/tax must be negative. We auto-correct here
	// so agents passing positive magnitudes still get the right sign.
	switch kind {
	case "deposit", "income":
		if amount < 0 {
			amount = -amount
		}
	case "withdraw", "expense", "fee", "tax":
		if amount > 0 {
			amount = -amount
		}
	}
	postedAt := strArg(args, "posted_at", "")
	if postedAt == "" {
		return nil, errors.New("posted_at required (RFC3339)")
	}
	acc, err := readAccount(ctx, accountID)
	if err != nil {
		return nil, fmt.Errorf("account %d not found", accountID)
	}
	id, err := insertTxn(ctx, txnIn{
		AccountID:  accountID,
		PostedAt:   postedAt,
		Kind:       kind,
		Amount:     amount,
		Currency:   acc.Currency,
		Payee:      strArg(args, "payee", ""),
		Memo:       strArg(args, "memo", ""),
		CategoryID: int64(intArg(args, "category_id", 0)),
	})
	if err != nil {
		return nil, err
	}
	ctx.Emit("txn.created", map[string]any{"txn_id": id, "kind": kind, "amount": amount})
	return readTxn(ctx, id)
}

func (a *App) toolTxnsUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64(intArg(args, "id", 0))
	if id == 0 {
		return nil, errors.New("id required")
	}
	cols, vals := []string{}, []any{}
	if v, ok := args["payee"].(string); ok {
		cols = append(cols, "payee=?")
		vals = append(vals, v)
	}
	if v, ok := args["memo"].(string); ok {
		cols = append(cols, "memo=?")
		vals = append(vals, v)
	}
	if v := int64(intArg(args, "category_id", 0)); v != 0 {
		cols = append(cols, "category_id=?")
		vals = append(vals, v)
	}
	if len(cols) == 0 {
		return nil, errors.New("no updatable fields supplied — payee, memo, or category_id")
	}
	cols = append(cols, "updated_at=CURRENT_TIMESTAMP")
	vals = append(vals, id)
	if _, err := ctx.AppDB().Exec(
		`UPDATE transactions SET `+strings.Join(cols, ", ")+` WHERE id=?`, vals...,
	); err != nil {
		return nil, err
	}
	return readTxn(ctx, id)
}

func (a *App) toolTxnsDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64(intArg(args, "id", 0))
	if id == 0 {
		return nil, errors.New("id required")
	}
	// Reverse holdings impact for buy/sell rows.
	t, err := readTxn(ctx, id)
	if err != nil {
		return nil, err
	}
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if (t.Kind == "buy" || t.Kind == "sell") && t.HoldingID != 0 {
		// Apply the inverse of the original effect.
		if _, err := tx.Exec(
			`UPDATE holdings SET quantity = quantity - ?, cost_basis = cost_basis - ?, closed_at = NULL WHERE id=?`,
			t.Quantity, t.CostBasisDelta, t.HoldingID,
		); err != nil {
			return nil, err
		}
	}
	if _, err := tx.Exec(`DELETE FROM transactions WHERE id=?`, id); err != nil {
		return nil, err
	}
	// Transfer pair: delete the sibling row too.
	if t.TransferID != "" {
		if _, err := tx.Exec(`DELETE FROM transactions WHERE transfer_id=?`, t.TransferID); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	ctx.Emit("txn.deleted", map[string]any{"txn_id": id, "kind": t.Kind})
	return map[string]any{"deleted": id}, nil
}

// ─── Transactions: convenience ───────────────────────────────────

func (a *App) toolTxnsBuy(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	accountID := int64(intArg(args, "account_id", 0))
	instrumentID := int64(intArg(args, "instrument_id", 0))
	qty, _ := args["quantity"].(float64)
	amount := int64(intArg(args, "amount", 0))
	postedAt := strArg(args, "posted_at", "")
	fee := int64(intArg(args, "fee", 0))
	if accountID == 0 || instrumentID == 0 || qty <= 0 || amount <= 0 || postedAt == "" {
		return nil, errors.New("account_id, instrument_id, quantity (>0), amount (>0), posted_at required")
	}
	acc, err := readAccount(ctx, accountID)
	if err != nil {
		return nil, fmt.Errorf("account %d not found", accountID)
	}
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	hid, err := upsertHolding(tx, accountID, instrumentID, qty, amount)
	if err != nil {
		return nil, err
	}
	pricePer := int64(math.Round(float64(amount) / qty))
	id, err := insertTxnTx(tx, txnIn{
		AccountID:      accountID,
		HoldingID:      hid,
		PostedAt:       postedAt,
		Kind:           "buy",
		Amount:         -amount,
		Currency:       acc.Currency,
		Quantity:       qty,
		Price:          &pricePer,
		CostBasisDelta: amount,
		Memo:           strArg(args, "memo", ""),
	})
	if err != nil {
		return nil, err
	}
	// Optional fee as a separate row so reports can isolate frictional costs.
	if fee > 0 {
		if _, err := insertTxnTx(tx, txnIn{
			AccountID: accountID,
			PostedAt:  postedAt,
			Kind:      "fee",
			Amount:    -fee,
			Currency:  acc.Currency,
			Memo:      "Trade fee",
		}); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	ctx.Emit("txn.buy", map[string]any{"txn_id": id, "holding_id": hid, "quantity": qty, "amount": amount})
	return readTxn(ctx, id)
}

func (a *App) toolTxnsSell(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	accountID := int64(intArg(args, "account_id", 0))
	instrumentID := int64(intArg(args, "instrument_id", 0))
	qty, _ := args["quantity"].(float64)
	amount := int64(intArg(args, "amount", 0))
	postedAt := strArg(args, "posted_at", "")
	fee := int64(intArg(args, "fee", 0))
	if accountID == 0 || instrumentID == 0 || qty <= 0 || amount <= 0 || postedAt == "" {
		return nil, errors.New("account_id, instrument_id, quantity (>0), amount (>0), posted_at required")
	}
	acc, err := readAccount(ctx, accountID)
	if err != nil {
		return nil, fmt.Errorf("account %d not found", accountID)
	}
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	released, hid, err := decreaseHolding(tx, accountID, instrumentID, qty)
	if err != nil {
		return nil, err
	}
	pricePer := int64(math.Round(float64(amount) / qty))
	id, err := insertTxnTx(tx, txnIn{
		AccountID:      accountID,
		HoldingID:      hid,
		PostedAt:       postedAt,
		Kind:           "sell",
		Amount:         amount,
		Currency:       acc.Currency,
		Quantity:       -qty,
		Price:          &pricePer,
		CostBasisDelta: -released,
		Memo:           strArg(args, "memo", ""),
	})
	if err != nil {
		return nil, err
	}
	if fee > 0 {
		if _, err := insertTxnTx(tx, txnIn{
			AccountID: accountID,
			PostedAt:  postedAt,
			Kind:      "fee",
			Amount:    -fee,
			Currency:  acc.Currency,
			Memo:      "Trade fee",
		}); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	ctx.Emit("txn.sell", map[string]any{"txn_id": id, "holding_id": hid, "quantity": qty, "amount": amount, "realized_pl": amount - released})
	return readTxn(ctx, id)
}

func (a *App) toolTxnsDividend(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	accountID := int64(intArg(args, "account_id", 0))
	instrumentID := int64(intArg(args, "instrument_id", 0))
	amount := int64(intArg(args, "amount", 0))
	postedAt := strArg(args, "posted_at", "")
	if accountID == 0 || instrumentID == 0 || amount <= 0 || postedAt == "" {
		return nil, errors.New("account_id, instrument_id, amount (>0), posted_at required")
	}
	acc, err := readAccount(ctx, accountID)
	if err != nil {
		return nil, fmt.Errorf("account %d not found", accountID)
	}
	// Link to the existing holding when one exists, but don't require
	// it — dividends sometimes land on closed positions.
	var hid int64
	_ = ctx.AppDB().QueryRow(`SELECT id FROM holdings WHERE account_id=? AND instrument_id=?`, accountID, instrumentID).Scan(&hid)
	id, err := insertTxn(ctx, txnIn{
		AccountID:    accountID,
		HoldingID:    hid,
		PostedAt:     postedAt,
		Kind:         "dividend",
		Amount:       amount,
		Currency:     acc.Currency,
		Memo:         strArg(args, "memo", ""),
	})
	if err != nil {
		return nil, err
	}
	ctx.Emit("txn.dividend", map[string]any{"txn_id": id, "amount": amount})
	return readTxn(ctx, id)
}

func (a *App) toolTxnsInterest(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	accountID := int64(intArg(args, "account_id", 0))
	amount := int64(intArg(args, "amount", 0))
	postedAt := strArg(args, "posted_at", "")
	if accountID == 0 || amount <= 0 || postedAt == "" {
		return nil, errors.New("account_id, amount (>0), posted_at required")
	}
	acc, err := readAccount(ctx, accountID)
	if err != nil {
		return nil, fmt.Errorf("account %d not found", accountID)
	}
	var hid int64
	if instrumentID := int64(intArg(args, "instrument_id", 0)); instrumentID != 0 {
		_ = ctx.AppDB().QueryRow(`SELECT id FROM holdings WHERE account_id=? AND instrument_id=?`, accountID, instrumentID).Scan(&hid)
	}
	id, err := insertTxn(ctx, txnIn{
		AccountID: accountID,
		HoldingID: hid,
		PostedAt:  postedAt,
		Kind:      "interest",
		Amount:    amount,
		Currency:  acc.Currency,
		Memo:      strArg(args, "memo", ""),
	})
	if err != nil {
		return nil, err
	}
	ctx.Emit("txn.interest", map[string]any{"txn_id": id, "amount": amount})
	return readTxn(ctx, id)
}

func (a *App) toolTxnsTransfer(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	from := int64(intArg(args, "from_account_id", 0))
	to := int64(intArg(args, "to_account_id", 0))
	amount := int64(intArg(args, "amount", 0))
	postedAt := strArg(args, "posted_at", "")
	if from == 0 || to == 0 || amount <= 0 || postedAt == "" {
		return nil, errors.New("from_account_id, to_account_id, amount (>0), posted_at required")
	}
	if from == to {
		return nil, errors.New("from_account_id and to_account_id must differ")
	}
	src, err := readAccount(ctx, from)
	if err != nil {
		return nil, fmt.Errorf("from account %d not found", from)
	}
	dst, err := readAccount(ctx, to)
	if err != nil {
		return nil, fmt.Errorf("to account %d not found", to)
	}
	if src.Currency != dst.Currency {
		return nil, fmt.Errorf("cross-currency transfers aren't supported in v0.1 (from=%s, to=%s) — record a withdraw + deposit with a manual FX leg", src.Currency, dst.Currency)
	}
	memo := strArg(args, "memo", "")
	tid := uuid.NewString()
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	outID, err := insertTxnTx(tx, txnIn{
		AccountID:  from,
		PostedAt:   postedAt,
		Kind:       "transfer",
		Amount:     -amount,
		Currency:   src.Currency,
		Memo:       memo,
		TransferID: tid,
	})
	if err != nil {
		return nil, err
	}
	inID, err := insertTxnTx(tx, txnIn{
		AccountID:  to,
		PostedAt:   postedAt,
		Kind:       "transfer",
		Amount:     amount,
		Currency:   dst.Currency,
		Memo:       memo,
		TransferID: tid,
	})
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	ctx.Emit("txn.transfer", map[string]any{"transfer_id": tid, "from": from, "to": to, "amount": amount})
	return map[string]any{"transfer_id": tid, "out_txn_id": outID, "in_txn_id": inID}, nil
}

// ─── Valuation ───────────────────────────────────────────────────

func (a *App) toolValuationSet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	instrumentID := int64(intArg(args, "instrument_id", 0))
	value := int64(intArg(args, "value", 0))
	if instrumentID == 0 || value <= 0 {
		return nil, errors.New("instrument_id and positive value required")
	}
	asOf := strArg(args, "as_of", time.Now().UTC().Format(time.RFC3339))
	if _, err := ctx.AppDB().Exec(
		`INSERT OR REPLACE INTO prices (instrument_id, as_of, price, source) VALUES (?, ?, ?, ?)`,
		instrumentID, asOf, value, "manual",
	); err != nil {
		return nil, err
	}
	// Optional audit transaction.
	if accountID := int64(intArg(args, "account_id", 0)); accountID != 0 {
		acc, err := readAccount(ctx, accountID)
		if err == nil {
			_, _ = insertTxn(ctx, txnIn{
				AccountID: accountID,
				PostedAt:  asOf,
				Kind:      "valuation",
				Amount:    0,
				Currency:  acc.Currency,
				Memo:      strArg(args, "memo", fmt.Sprintf("Revalued to %d", value)),
			})
		}
	}
	ctx.Emit("valuation.set", map[string]any{"instrument_id": instrumentID, "value": value, "as_of": asOf})
	return map[string]any{"instrument_id": instrumentID, "value": value, "as_of": asOf}, nil
}

// ─── Categories ──────────────────────────────────────────────────

func (a *App) toolCategoriesList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid := projectID()
	rows, err := ctx.AppDB().Query(
		`SELECT id, project_id, COALESCE(parent_id,0), name, kind, color, archived, created_at
		 FROM categories WHERE project_id=? ORDER BY kind, parent_id NULLS FIRST, name`, pid,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Category{}
	for rows.Next() {
		var c Category
		var arch int
		if err := rows.Scan(&c.ID, &c.ProjectID, &c.ParentID, &c.Name, &c.Kind, &c.Color, &arch, &c.CreatedAt); err != nil {
			continue
		}
		c.Archived = arch == 1
		out = append(out, c)
	}
	return map[string]any{"categories": out}, nil
}

func (a *App) toolCategoriesCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	name := strings.TrimSpace(strArg(args, "name", ""))
	kind := strArg(args, "kind", "")
	if name == "" {
		return nil, errors.New("name required")
	}
	if kind != "income" && kind != "expense" {
		return nil, errors.New("kind must be 'income' or 'expense'")
	}
	parentID := int64(intArg(args, "parent_id", 0))
	color := strArg(args, "color", "#94a3b8")
	pid := projectID()
	var parentVal any
	if parentID > 0 {
		parentVal = parentID
	}
	res, err := ctx.AppDB().Exec(
		`INSERT INTO categories (project_id, parent_id, name, kind, color) VALUES (?, ?, ?, ?, ?)`,
		pid, parentVal, name, kind, color,
	)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return readCategory(ctx, id)
}

func (a *App) toolCategoriesUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64(intArg(args, "id", 0))
	if id == 0 {
		return nil, errors.New("id required")
	}
	cols, vals := []string{}, []any{}
	for _, k := range []string{"name", "color"} {
		if v, ok := args[k].(string); ok && v != "" {
			cols = append(cols, k+"=?")
			vals = append(vals, v)
		}
	}
	if v, ok := args["archived"].(bool); ok {
		cols = append(cols, "archived=?")
		vals = append(vals, boolToInt(v))
	}
	if v := int64(intArg(args, "parent_id", 0)); v != 0 {
		cols = append(cols, "parent_id=?")
		vals = append(vals, v)
	}
	if len(cols) == 0 {
		return nil, errors.New("no updatable fields supplied — name, color, archived, parent_id")
	}
	vals = append(vals, id)
	if _, err := ctx.AppDB().Exec(
		`UPDATE categories SET `+strings.Join(cols, ", ")+` WHERE id=?`, vals...,
	); err != nil {
		return nil, err
	}
	return readCategory(ctx, id)
}

func (a *App) toolCategoriesDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64(intArg(args, "id", 0))
	if id == 0 {
		return nil, errors.New("id required")
	}
	if _, err := ctx.AppDB().Exec(`DELETE FROM categories WHERE id=?`, id); err != nil {
		return nil, err
	}
	return map[string]any{"deleted": id}, nil
}

func (a *App) toolCategoriesSeed(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid := projectID()
	type cat struct {
		name, kind, parent string
	}
	seeds := []cat{
		{"Income", "income", ""},
		{"Salary", "income", "Income"},
		{"Dividends", "income", "Income"},
		{"Interest", "income", "Income"},
		{"Rental", "income", "Income"},
		{"Refunds", "income", "Income"},

		{"Housing", "expense", ""},
		{"Rent / Mortgage", "expense", "Housing"},
		{"Utilities", "expense", "Housing"},
		{"Maintenance", "expense", "Housing"},

		{"Food", "expense", ""},
		{"Groceries", "expense", "Food"},
		{"Restaurants", "expense", "Food"},

		{"Transport", "expense", ""},
		{"Fuel", "expense", "Transport"},
		{"Public Transit", "expense", "Transport"},

		{"Health", "expense", ""},
		{"Lifestyle", "expense", ""},
		{"Subscriptions", "expense", "Lifestyle"},
		{"Travel", "expense", "Lifestyle"},

		{"Fees & Taxes", "expense", ""},
		{"Other", "expense", ""},
	}
	// SQLite's UNIQUE(parent_id, name) doesn't dedupe rows where
	// parent_id is NULL — NULL ≠ NULL in UNIQUE constraints. So we
	// can't rely on INSERT OR IGNORE; check existence first.
	created := 0
	parents := map[string]int64{}
	for _, s := range seeds {
		var parentID any
		if s.parent != "" {
			parentID = parents[s.parent]
		}
		var existing int64
		if parentID == nil {
			_ = ctx.AppDB().QueryRow(
				`SELECT id FROM categories WHERE project_id=? AND parent_id IS NULL AND name=?`,
				pid, s.name).Scan(&existing)
		} else {
			_ = ctx.AppDB().QueryRow(
				`SELECT id FROM categories WHERE project_id=? AND parent_id=? AND name=?`,
				pid, parentID, s.name).Scan(&existing)
		}
		if existing != 0 {
			parents[s.name] = existing
			continue
		}
		res, err := ctx.AppDB().Exec(
			`INSERT INTO categories (project_id, parent_id, name, kind) VALUES (?, ?, ?, ?)`,
			pid, parentID, s.name, s.kind,
		)
		if err != nil {
			continue
		}
		id, _ := res.LastInsertId()
		created++
		parents[s.name] = id
	}
	return map[string]any{"created": created}, nil
}

func readCategory(ctx *sdk.AppCtx, id int64) (Category, error) {
	var c Category
	var arch int
	err := ctx.AppDB().QueryRow(
		`SELECT id, project_id, COALESCE(parent_id,0), name, kind, color, archived, created_at
		 FROM categories WHERE id=?`, id,
	).Scan(&c.ID, &c.ProjectID, &c.ParentID, &c.Name, &c.Kind, &c.Color, &arch, &c.CreatedAt)
	c.Archived = arch == 1
	return c, err
}

// ─── Prices + FX ─────────────────────────────────────────────────

func (a *App) toolPricesSet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	iid := int64(intArg(args, "instrument_id", 0))
	price := int64(intArg(args, "price", 0))
	if iid == 0 || price <= 0 {
		return nil, errors.New("instrument_id and positive price required")
	}
	asOf := strArg(args, "as_of", time.Now().UTC().Format(time.RFC3339))
	src := strArg(args, "source", "manual")
	if _, err := ctx.AppDB().Exec(
		`INSERT OR REPLACE INTO prices (instrument_id, as_of, price, source) VALUES (?, ?, ?, ?)`,
		iid, asOf, price, src,
	); err != nil {
		return nil, err
	}
	return map[string]any{"instrument_id": iid, "as_of": asOf, "price": price, "source": src}, nil
}

func (a *App) toolPricesGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	iid := int64(intArg(args, "instrument_id", 0))
	if iid == 0 {
		return nil, errors.New("instrument_id required")
	}
	asOf := strArg(args, "as_of", time.Now().UTC().Format(time.RFC3339))
	asOfT, err := time.Parse(time.RFC3339, asOf)
	if err != nil {
		return nil, fmt.Errorf("as_of: %w", err)
	}
	price, ok := latestPrice(ctx, iid, asOfT)
	if !ok {
		return map[string]any{"instrument_id": iid, "as_of": asOf, "price": nil}, nil
	}
	return map[string]any{"instrument_id": iid, "as_of": asOf, "price": price}, nil
}

func (a *App) toolFXSet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	base := strings.ToUpper(strArg(args, "base", ""))
	quote := strings.ToUpper(strArg(args, "quote", ""))
	rate, _ := args["rate"].(float64)
	if base == "" || quote == "" || rate <= 0 {
		return nil, errors.New("base, quote, rate (>0) required")
	}
	asOf := strArg(args, "as_of", time.Now().UTC().Format(time.RFC3339))
	if _, err := ctx.AppDB().Exec(
		`INSERT OR REPLACE INTO fx_rates (base, quote, as_of, rate) VALUES (?, ?, ?, ?)`,
		base, quote, asOf, rate,
	); err != nil {
		return nil, err
	}
	return map[string]any{"base": base, "quote": quote, "as_of": asOf, "rate": rate}, nil
}

func latestPrice(ctx *sdk.AppCtx, instrumentID int64, asOf time.Time) (int64, bool) {
	var price int64
	err := ctx.AppDB().QueryRow(
		`SELECT price FROM prices WHERE instrument_id=? AND as_of <= ? ORDER BY as_of DESC LIMIT 1`,
		instrumentID, asOf.UTC().Format(time.RFC3339),
	).Scan(&price)
	if err != nil {
		return 0, false
	}
	return price, true
}

// convertCcy converts a minor-units amount from one currency to
// another using the latest stored FX rate. Identity (same currency)
// short-circuits. Missing rates fall back to 1.0 (a degraded mode the
// UI surfaces via a "FX missing" badge once we wire that up); this is
// a deliberate choice so single-currency users never see errors and
// multi-currency users get visibly-wrong totals they can fix.
func convertCcy(ctx *sdk.AppCtx, amount int64, from, to string) int64 {
	if from == to || from == "" || to == "" {
		return amount
	}
	var rate float64
	err := ctx.AppDB().QueryRow(
		`SELECT rate FROM fx_rates WHERE base=? AND quote=? ORDER BY as_of DESC LIMIT 1`,
		from, to,
	).Scan(&rate)
	if err == nil && rate > 0 {
		return int64(math.Round(float64(amount) * rate))
	}
	// Try the inverse.
	err = ctx.AppDB().QueryRow(
		`SELECT rate FROM fx_rates WHERE base=? AND quote=? ORDER BY as_of DESC LIMIT 1`,
		to, from,
	).Scan(&rate)
	if err == nil && rate > 0 {
		return int64(math.Round(float64(amount) / rate))
	}
	return amount
}

// ─── Reports ─────────────────────────────────────────────────────

func (a *App) toolReportsNetWorth(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	settings, _ := loadSettings(ctx)
	base := settings.BaseCurrency
	if series := strArg(args, "series", ""); series != "" {
		from := strArg(args, "from", time.Now().UTC().AddDate(-1, 0, 0).Format(time.RFC3339))
		to := strArg(args, "to", time.Now().UTC().Format(time.RFC3339))
		fromT, err := parseFlexibleTime(from)
		if err != nil {
			return nil, fmt.Errorf("from: %w", err)
		}
		toT, err := parseFlexibleTime(to)
		if err != nil {
			return nil, fmt.Errorf("to: %w", err)
		}
		buckets := buildBuckets(fromT, toT, series)
		points := make([]map[string]any, 0, len(buckets))
		for _, b := range buckets {
			points = append(points, map[string]any{
				"as_of": b.Format(time.RFC3339),
				"total": netWorthAt(ctx, b, base),
			})
		}
		return map[string]any{"series": series, "base_currency": base, "points": points}, nil
	}
	at := strArg(args, "at", time.Now().UTC().Format(time.RFC3339))
	atT, err := parseFlexibleTime(at)
	if err != nil {
		return nil, fmt.Errorf("at: %w", err)
	}
	total, byAccount, byKind, byCurrency := netWorthBreakdown(ctx, atT, base)
	return map[string]any{
		"at":             at,
		"base_currency":  base,
		"total":          total,
		"by_account":     byAccount,
		"by_kind":        byKind,
		"by_currency":    byCurrency,
	}, nil
}

func netWorthAt(ctx *sdk.AppCtx, at time.Time, base string) int64 {
	total, _, _, _ := netWorthBreakdown(ctx, at, base)
	return total
}

func netWorthBreakdown(ctx *sdk.AppCtx, at time.Time, base string) (int64, []map[string]any, []map[string]any, []map[string]any) {
	pid := projectID()
	rows, err := ctx.AppDB().Query(
		`SELECT id, name, kind, currency, opening_balance FROM accounts WHERE project_id=? AND archived=0`, pid,
	)
	if err != nil {
		return 0, nil, nil, nil
	}
	type drained struct {
		id, opening    int64
		name, kind, ccy string
	}
	bare := []drained{}
	for rows.Next() {
		var d drained
		if err := rows.Scan(&d.id, &d.name, &d.kind, &d.ccy, &d.opening); err == nil {
			bare = append(bare, d)
		}
	}
	rows.Close()

	var total int64
	byAccount := []map[string]any{}
	byKindSum := map[string]int64{}
	byCcySum := map[string]int64{}
	for _, d := range bare {
		cash := cashBalanceAt(ctx, d.id, d.opening, at)
		holdings := holdingsValueAt(ctx, d.id, d.ccy, at)
		accTotal := cash + holdings
		inBase := convertCcy(ctx, accTotal, d.ccy, base)
		total += inBase
		byAccount = append(byAccount, map[string]any{
			"account_id":     d.id,
			"name":           d.name,
			"kind":           d.kind,
			"currency":       d.ccy,
			"cash":           cash,
			"holdings_value": holdings,
			"total":          accTotal,
			"total_in_base":  inBase,
		})
		byKindSum[d.kind] += inBase
		byCcySum[d.ccy] += inBase
	}
	return total, byAccount, mapToSortedSlice(byKindSum, "kind"), mapToSortedSlice(byCcySum, "currency")
}

func cashBalanceAt(ctx *sdk.AppCtx, accountID, opening int64, at time.Time) int64 {
	var sum sql.NullInt64
	_ = ctx.AppDB().QueryRow(
		`SELECT COALESCE(SUM(amount),0) FROM transactions
		 WHERE account_id=? AND kind != 'valuation' AND posted_at <= ?`,
		accountID, at.UTC().Format(time.RFC3339),
	).Scan(&sum)
	return opening + sum.Int64
}

func holdingsValueAt(ctx *sdk.AppCtx, accountID int64, accountCcy string, at time.Time) int64 {
	// Holdings as-of `at` need to back out post-`at` txn impact. v0.1
	// approximation: use the current holdings row but price it at `at`.
	// For "now" this is exact; for historical points it ignores
	// transactions after `at`. Good enough for monthly series; a
	// proper time-travelling view lands in v0.3.
	rows, err := ctx.AppDB().Query(
		`SELECT h.instrument_id, h.quantity, i.quote_currency
		 FROM holdings h JOIN instruments i ON i.id = h.instrument_id
		 WHERE h.account_id=? AND (h.closed_at IS NULL OR h.closed_at='')`,
		accountID,
	)
	if err != nil {
		return 0
	}
	type drained struct {
		iid      int64
		qty      float64
		quoteCcy string
	}
	bare := []drained{}
	for rows.Next() {
		var d drained
		if err := rows.Scan(&d.iid, &d.qty, &d.quoteCcy); err == nil {
			bare = append(bare, d)
		}
	}
	rows.Close()
	var total int64
	for _, d := range bare {
		price, ok := latestPrice(ctx, d.iid, at)
		if !ok {
			continue
		}
		valueInQuote := int64(math.Round(d.qty * float64(price)))
		total += convertCcy(ctx, valueInQuote, d.quoteCcy, accountCcy)
	}
	return total
}

func (a *App) toolReportsAllocation(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	settings, _ := loadSettings(ctx)
	base := settings.BaseCurrency
	pid := projectID()

	accRows, err := ctx.AppDB().Query(
		`SELECT a.id, a.kind, a.currency, a.opening_balance
		 FROM accounts a WHERE a.project_id=? AND a.archived=0`, pid,
	)
	if err != nil {
		return nil, err
	}
	type accBare struct {
		id, opening int64
		kind, ccy   string
	}
	accs := []accBare{}
	for accRows.Next() {
		var a accBare
		if err := accRows.Scan(&a.id, &a.kind, &a.ccy, &a.opening); err == nil {
			accs = append(accs, a)
		}
	}
	accRows.Close()

	now := time.Now().UTC()
	byAccountKind := map[string]int64{}
	byCurrency := map[string]int64{}
	instrumentTotals := map[int64]*instrumentTotal{}
	byInstrumentKind := map[string]int64{}
	var grand int64

	for _, acc := range accs {
		cash := cashBalanceAt(ctx, acc.id, acc.opening, now)
		holdings := holdingsValueAt(ctx, acc.id, acc.ccy, now)
		accInBase := convertCcy(ctx, cash+holdings, acc.ccy, base)
		byAccountKind[acc.kind] += accInBase
		byCurrency[acc.ccy] += accInBase
		grand += accInBase

		// Per-instrument breakdown for top-N — drain cursor before
		// any nested call.
		hRows, err := ctx.AppDB().Query(
			`SELECT i.id, i.kind, i.symbol, i.name, h.quantity, i.quote_currency
			 FROM holdings h JOIN instruments i ON i.id = h.instrument_id
			 WHERE h.account_id=? AND (h.closed_at IS NULL OR h.closed_at='')`,
			acc.id,
		)
		if err != nil {
			continue
		}
		type holdBare struct {
			iid                              int64
			iKind, sym, name, quoteCcy       string
			qty                              float64
		}
		bare := []holdBare{}
		for hRows.Next() {
			var d holdBare
			if err := hRows.Scan(&d.iid, &d.iKind, &d.sym, &d.name, &d.qty, &d.quoteCcy); err == nil {
				bare = append(bare, d)
			}
		}
		hRows.Close()
		for _, d := range bare {
			price, ok := latestPrice(ctx, d.iid, now)
			if !ok {
				continue
			}
			valInBase := convertCcy(ctx, int64(math.Round(d.qty*float64(price))), d.quoteCcy, base)
			byInstrumentKind[d.iKind] += valInBase
			if t, ok := instrumentTotals[d.iid]; ok {
				t.Value += valInBase
			} else {
				instrumentTotals[d.iid] = &instrumentTotal{ID: d.iid, Symbol: d.sym, Name: d.name, Kind: d.iKind, Value: valInBase}
			}
		}
	}

	topInstruments := make([]instrumentTotal, 0, len(instrumentTotals))
	for _, t := range instrumentTotals {
		topInstruments = append(topInstruments, *t)
	}
	sort.Slice(topInstruments, func(i, j int) bool { return topInstruments[i].Value > topInstruments[j].Value })
	if len(topInstruments) > 10 {
		topInstruments = topInstruments[:10]
	}

	return map[string]any{
		"base_currency":      base,
		"total":              grand,
		"by_account_kind":    mapToSortedSlice(byAccountKind, "kind"),
		"by_instrument_kind": mapToSortedSlice(byInstrumentKind, "kind"),
		"by_currency":        mapToSortedSlice(byCurrency, "currency"),
		"top_instruments":    topInstruments,
	}, nil
}

func (a *App) toolReportsPerformance(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	settings, _ := loadSettings(ctx)
	base := settings.BaseCurrency
	from := strArg(args, "from", time.Now().UTC().AddDate(-1, 0, 0).Format(time.RFC3339))
	to := strArg(args, "to", time.Now().UTC().Format(time.RFC3339))

	holdings, err := listHoldingsRich(ctx, 0, 0, false)
	if err != nil {
		return nil, err
	}
	// Realised P&L from sells in the window. Each sell row's
	// (amount + cost_basis_delta) is its realised P&L in account.currency.
	var realized int64
	type sellRow struct {
		amt, cbd int64
		ccy      string
	}
	sells := []sellRow{}
	rows, err := ctx.AppDB().Query(
		`SELECT t.amount, t.cost_basis_delta, a.currency
		 FROM transactions t JOIN accounts a ON a.id = t.account_id
		 WHERE t.kind='sell' AND t.posted_at >= ? AND t.posted_at < ?`,
		from, to,
	)
	if err == nil {
		for rows.Next() {
			var s sellRow
			if err := rows.Scan(&s.amt, &s.cbd, &s.ccy); err == nil {
				sells = append(sells, s)
			}
		}
		rows.Close()
	}
	for _, s := range sells {
		realized += convertCcy(ctx, s.amt+s.cbd, s.ccy, base)
	}

	// Unrealised P&L sums across all open holdings, converted to base.
	var unrealized, costBase, currentValueBase int64
	type costRow struct {
		ccy string
		cb  int64
	}
	costs := []costRow{}
	hRows, err := ctx.AppDB().Query(
		`SELECT a.currency, h.cost_basis FROM holdings h JOIN accounts a ON a.id = h.account_id
		 WHERE (h.closed_at IS NULL OR h.closed_at='')`,
	)
	if err == nil {
		for hRows.Next() {
			var c costRow
			if err := hRows.Scan(&c.ccy, &c.cb); err == nil {
				costs = append(costs, c)
			}
		}
		hRows.Close()
	}
	for _, c := range costs {
		costBase += convertCcy(ctx, c.cb, c.ccy, base)
	}
	// Lookup account currencies for the rich-holdings slice in one
	// shot via map, so we don't QueryRow inside the loop.
	accCcy := map[int64]string{}
	if cRows, err := ctx.AppDB().Query(`SELECT id, currency FROM accounts`); err == nil {
		for cRows.Next() {
			var id int64
			var c string
			if err := cRows.Scan(&id, &c); err == nil {
				accCcy[id] = c
			}
		}
		cRows.Close()
	}
	for _, h := range holdings {
		currentValueBase += convertCcy(ctx, h.CurrentValue, accCcy[h.AccountID], base)
	}
	unrealized = currentValueBase - costBase

	return map[string]any{
		"base_currency":      base,
		"from":               from,
		"to":                 to,
		"holdings":           holdings,
		"cost_basis_total":   costBase,
		"current_value_total": currentValueBase,
		"unrealized_pl":      unrealized,
		"realized_pl_window": realized,
	}, nil
}

func (a *App) toolReportsCashflow(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	settings, _ := loadSettings(ctx)
	base := settings.BaseCurrency
	from := strArg(args, "from", "")
	to := strArg(args, "to", "")
	if from == "" || to == "" {
		return nil, errors.New("from and to required")
	}
	fromT, err := parseFlexibleTime(from)
	if err != nil {
		return nil, fmt.Errorf("from: %w", err)
	}
	toT, err := parseFlexibleTime(to)
	if err != nil {
		return nil, fmt.Errorf("to: %w", err)
	}
	bucket := strArg(args, "bucket", "monthly")
	buckets := buildBuckets(fromT, toT, bucket)

	type cell struct {
		Income  int64 `json:"income"`
		Expense int64 `json:"expense"`
	}
	cells := make([]cell, len(buckets))

	// Income kinds: dividend, interest, income, deposit (transfers
	// between own accounts are excluded — they're internal).
	// Expense kinds: expense, fee, tax, withdraw.
	// buy/sell/valuation/transfer are excluded.
	rows, err := ctx.AppDB().Query(
		`SELECT t.kind, t.amount, a.currency, t.posted_at
		 FROM transactions t JOIN accounts a ON a.id = t.account_id
		 WHERE t.posted_at >= ? AND t.posted_at < ?
		   AND t.kind IN ('dividend','interest','income','deposit','expense','fee','tax','withdraw')`,
		fromT.UTC().Format(time.RFC3339), toT.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return nil, err
	}
	type cfRow struct {
		kind, ccy, posted string
		amount            int64
	}
	bare := []cfRow{}
	for rows.Next() {
		var r cfRow
		if err := rows.Scan(&r.kind, &r.amount, &r.ccy, &r.posted); err == nil {
			bare = append(bare, r)
		}
	}
	rows.Close()
	var totalIncome, totalExpense int64
	for _, r := range bare {
		t, err := time.Parse(time.RFC3339, r.posted)
		if err != nil {
			continue
		}
		inBase := convertCcy(ctx, r.amount, r.ccy, base)
		idx := bucketIndex(t, buckets)
		if idx < 0 {
			continue
		}
		switch r.kind {
		case "dividend", "interest", "income", "deposit":
			cells[idx].Income += inBase
			totalIncome += inBase
		case "expense", "fee", "tax", "withdraw":
			cells[idx].Expense += inBase
			totalExpense += inBase
		}
	}
	points := make([]map[string]any, len(buckets))
	for i, b := range buckets {
		points[i] = map[string]any{
			"as_of":   b.Format(time.RFC3339),
			"income":  cells[i].Income,
			"expense": cells[i].Expense,
			"net":     cells[i].Income + cells[i].Expense,
		}
	}
	return map[string]any{
		"base_currency":  base,
		"from":           from,
		"to":             to,
		"bucket":         bucket,
		"points":         points,
		"total_income":   totalIncome,
		"total_expense":  totalExpense,
		"net":            totalIncome + totalExpense,
	}, nil
}

func buildBuckets(from, to time.Time, kind string) []time.Time {
	out := []time.Time{}
	from = from.UTC()
	to = to.UTC()
	switch kind {
	case "weekly":
		// Anchor to Monday. `to` is exclusive (matches our SQL
		// posted_at < to), so the bucket starting at exactly `to` is
		// not emitted.
		d := int(from.Weekday())
		if d == 0 {
			d = 7
		}
		t := time.Date(from.Year(), from.Month(), from.Day()-d+1, 0, 0, 0, 0, time.UTC)
		for t.Before(to) {
			out = append(out, t)
			t = t.AddDate(0, 0, 7)
		}
	case "monthly":
		t := time.Date(from.Year(), from.Month(), 1, 0, 0, 0, 0, time.UTC)
		for t.Before(to) {
			out = append(out, t)
			t = t.AddDate(0, 1, 0)
		}
	default:
		out = []time.Time{from}
	}
	return out
}

func bucketIndex(t time.Time, buckets []time.Time) int {
	for i := len(buckets) - 1; i >= 0; i-- {
		if !t.Before(buckets[i]) {
			return i
		}
	}
	return -1
}

func mapToSortedSlice(m map[string]int64, key string) []map[string]any {
	out := make([]map[string]any, 0, len(m))
	for k, v := range m {
		out = append(out, map[string]any{key: k, "value": v})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i]["value"].(int64) > out[j]["value"].(int64)
	})
	return out
}

// ─── CSV import ──────────────────────────────────────────────────

func (a *App) toolImportCSV(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	accountID := int64(intArg(args, "account_id", 0))
	csvStr, _ := args["csv"].(string)
	mapping, _ := args["mapping"].(map[string]any)
	if accountID == 0 || csvStr == "" || mapping == nil {
		return nil, errors.New("account_id, csv, mapping required")
	}
	acc, err := readAccount(ctx, accountID)
	if err != nil {
		return nil, fmt.Errorf("account %d not found", accountID)
	}
	r := csv.NewReader(strings.NewReader(csvStr))
	r.FieldsPerRecord = -1
	records, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("csv: %w", err)
	}
	if len(records) < 2 {
		return nil, errors.New("csv must have a header row + at least one data row")
	}
	header := records[0]
	colIdx := func(name any) int {
		s, _ := name.(string)
		for i, h := range header {
			if strings.EqualFold(strings.TrimSpace(h), s) {
				return i
			}
		}
		return -1
	}
	dateCol := colIdx(mapping["date"])
	amountCol := colIdx(mapping["amount"])
	if dateCol < 0 || amountCol < 0 {
		return nil, errors.New("mapping must reference 'date' and 'amount' columns by name")
	}
	memoCol := colIdx(mapping["memo"])
	payeeCol := colIdx(mapping["payee"])
	kindCol := colIdx(mapping["kind"])

	imported := 0
	skipped := 0
	for _, row := range records[1:] {
		if len(row) <= dateCol || len(row) <= amountCol {
			skipped++
			continue
		}
		dt, err := parseFlexibleTime(row[dateCol])
		if err != nil {
			skipped++
			continue
		}
		// Amount is treated as a decimal in major units (e.g. "12.34")
		// because that's what bank CSVs emit. We convert to minor units.
		amount, err := parseMoneyToMinor(row[amountCol])
		if err != nil {
			skipped++
			continue
		}
		kind := "expense"
		if amount > 0 {
			kind = "income"
		}
		if kindCol >= 0 && kindCol < len(row) {
			k := strings.ToLower(strings.TrimSpace(row[kindCol]))
			if contains([]string{"deposit", "withdraw", "income", "expense", "fee", "tax"}, k) {
				kind = k
			}
		}
		memo, payee := "", ""
		if memoCol >= 0 && memoCol < len(row) {
			memo = row[memoCol]
		}
		if payeeCol >= 0 && payeeCol < len(row) {
			payee = row[payeeCol]
		}
		if _, err := insertTxn(ctx, txnIn{
			AccountID: accountID,
			PostedAt:  dt.UTC().Format(time.RFC3339),
			Kind:      kind,
			Amount:    amount,
			Currency:  acc.Currency,
			Payee:     payee,
			Memo:      memo,
		}); err != nil {
			skipped++
			continue
		}
		imported++
	}
	return map[string]any{"imported": imported, "skipped": skipped}, nil
}

// parseMoneyToMinor accepts "12.34", "12,34", "1,234.56", "-5.00",
// "(5.00)" — common bank-export shapes — and returns 1234 / 123456 /
// −500 etc.
func parseMoneyToMinor(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("empty")
	}
	neg := false
	if strings.HasPrefix(s, "(") && strings.HasSuffix(s, ")") {
		neg = true
		s = strings.TrimSuffix(strings.TrimPrefix(s, "("), ")")
	}
	if strings.HasPrefix(s, "-") {
		neg = true
		s = strings.TrimPrefix(s, "-")
	}
	// Normalise thousands separators + decimal mark.
	s = strings.ReplaceAll(s, " ", "")
	// If both '.' and ',' present, the later one is the decimal mark.
	lastDot := strings.LastIndex(s, ".")
	lastComma := strings.LastIndex(s, ",")
	var integer, fraction string
	switch {
	case lastDot >= 0 && lastComma >= 0:
		if lastDot > lastComma {
			integer = strings.ReplaceAll(s[:lastDot], ",", "")
			fraction = s[lastDot+1:]
		} else {
			integer = strings.ReplaceAll(s[:lastComma], ".", "")
			fraction = s[lastComma+1:]
		}
	case lastDot >= 0:
		integer = s[:lastDot]
		fraction = s[lastDot+1:]
	case lastComma >= 0:
		integer = s[:lastComma]
		fraction = s[lastComma+1:]
	default:
		integer = s
		fraction = ""
	}
	if fraction != "" && len(fraction) > 2 {
		// Treat as thousands separator (e.g. "1.234" → 1234), not decimals.
		integer = integer + fraction
		fraction = ""
	}
	if fraction == "" {
		fraction = "00"
	}
	if len(fraction) == 1 {
		fraction = fraction + "0"
	}
	if len(fraction) > 2 {
		fraction = fraction[:2]
	}
	ip, err := strconv.ParseInt(integer, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse %q: %w", s, err)
	}
	fp, err := strconv.ParseInt(fraction, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse fraction %q: %w", fraction, err)
	}
	v := ip*100 + fp
	if neg {
		v = -v
	}
	return v, nil
}

// ─── Budgets ─────────────────────────────────────────────────────

func (a *App) toolBudgetsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid := projectID()
	rows, err := ctx.AppDB().Query(
		`SELECT id, project_id, COALESCE(category_id,0), period, amount, currency,
		        starts_at, archived, created_at
		 FROM budgets WHERE project_id=? AND archived=0
		 ORDER BY category_id NULLS FIRST, period`, pid,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Budget{}
	for rows.Next() {
		var b Budget
		var arch int
		if err := rows.Scan(&b.ID, &b.ProjectID, &b.CategoryID, &b.Period,
			&b.Amount, &b.Currency, &b.StartsAt, &arch, &b.CreatedAt); err != nil {
			continue
		}
		b.Archived = arch == 1
		out = append(out, b)
	}
	return map[string]any{"budgets": out}, nil
}

func (a *App) toolBudgetsSet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	amount := int64(intArg(args, "amount", 0))
	if amount <= 0 {
		return nil, errors.New("amount required (positive minor units)")
	}
	period := strArg(args, "period", "monthly")
	if !contains([]string{"weekly", "monthly", "quarterly", "yearly"}, period) {
		return nil, fmt.Errorf("period must be one of: weekly, monthly, quarterly, yearly")
	}
	pid := projectID()
	settings, _ := loadSettings(ctx)
	currency := settings.BaseCurrency

	// category_id == 0 means "total spend" → NULL in DB.
	var catVal any
	if cid := int64(intArg(args, "category_id", 0)); cid > 0 {
		catVal = cid
	}

	// Upsert: check existence, then INSERT or UPDATE. Can't rely on
	// INSERT OR REPLACE because the NULL-category case isn't covered
	// by the regular UNIQUE constraint (only by the partial index).
	var existing int64
	if catVal == nil {
		_ = ctx.AppDB().QueryRow(
			`SELECT id FROM budgets WHERE project_id=? AND category_id IS NULL AND period=?`,
			pid, period).Scan(&existing)
	} else {
		_ = ctx.AppDB().QueryRow(
			`SELECT id FROM budgets WHERE project_id=? AND category_id=? AND period=?`,
			pid, catVal, period).Scan(&existing)
	}
	if existing != 0 {
		if _, err := ctx.AppDB().Exec(
			`UPDATE budgets SET amount=?, currency=?, archived=0, updated_at=CURRENT_TIMESTAMP WHERE id=?`,
			amount, currency, existing,
		); err != nil {
			return nil, err
		}
		ctx.Emit("budget.updated", map[string]any{"budget_id": existing, "amount": amount})
		return readBudget(ctx, existing)
	}
	res, err := ctx.AppDB().Exec(
		`INSERT INTO budgets (project_id, category_id, period, amount, currency)
		 VALUES (?, ?, ?, ?, ?)`,
		pid, catVal, period, amount, currency,
	)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	ctx.Emit("budget.created", map[string]any{"budget_id": id, "amount": amount})
	return readBudget(ctx, id)
}

func (a *App) toolBudgetsDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64(intArg(args, "id", 0))
	if id == 0 {
		return nil, errors.New("id required")
	}
	if _, err := ctx.AppDB().Exec(`DELETE FROM budgets WHERE id=?`, id); err != nil {
		return nil, err
	}
	ctx.Emit("budget.deleted", map[string]any{"budget_id": id})
	return map[string]any{"deleted": id}, nil
}

func (a *App) toolBudgetsStatus(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	period := strArg(args, "period", "monthly")
	if !contains([]string{"weekly", "monthly", "quarterly", "yearly"}, period) {
		return nil, fmt.Errorf("period must be one of: weekly, monthly, quarterly, yearly")
	}
	asOf := strArg(args, "as_of", time.Now().UTC().Format(time.RFC3339))
	asOfT, err := parseFlexibleTime(asOf)
	if err != nil {
		return nil, fmt.Errorf("as_of: %w", err)
	}
	periodStart, periodEnd := periodBounds(asOfT, period)

	pid := projectID()
	settings, _ := loadSettings(ctx)
	base := settings.BaseCurrency

	// Pull budgets for this period kind.
	rows, err := ctx.AppDB().Query(
		`SELECT id, COALESCE(category_id,0), amount, currency
		 FROM budgets WHERE project_id=? AND period=? AND archived=0`,
		pid, period,
	)
	if err != nil {
		return nil, err
	}
	type budRow struct {
		id, catID, amount int64
		ccy               string
	}
	buds := []budRow{}
	for rows.Next() {
		var b budRow
		if err := rows.Scan(&b.id, &b.catID, &b.amount, &b.ccy); err == nil {
			buds = append(buds, b)
		}
	}
	rows.Close()

	// Resolve category names once.
	catNames := map[int64]string{}
	if nRows, err := ctx.AppDB().Query(`SELECT id, name FROM categories WHERE project_id=?`, pid); err == nil {
		for nRows.Next() {
			var id int64
			var name string
			if err := nRows.Scan(&id, &name); err == nil {
				catNames[id] = name
			}
		}
		nRows.Close()
	}

	out := make([]BudgetStatus, 0, len(buds))
	for _, b := range buds {
		spent := computeSpent(ctx, b.catID, periodStart, periodEnd, base)
		remaining := b.amount - spent
		pct := 0.0
		if b.amount > 0 {
			pct = float64(spent) / float64(b.amount) * 100
		}
		name := "Total spend"
		if b.catID != 0 {
			if n, ok := catNames[b.catID]; ok {
				name = n
			} else {
				name = fmt.Sprintf("#%d", b.catID)
			}
		}
		out = append(out, BudgetStatus{
			BudgetID:     b.id,
			CategoryID:   b.catID,
			CategoryName: name,
			Period:       period,
			PeriodStart:  periodStart.UTC().Format(time.RFC3339),
			PeriodEnd:    periodEnd.UTC().Format(time.RFC3339),
			Budgeted:     b.amount,
			Spent:        spent,
			Remaining:    remaining,
			PctUsed:      pct,
			Over:         spent > b.amount,
			Currency:     b.ccy,
		})
	}
	// Sort by pct_used desc — problem categories surface first.
	sort.Slice(out, func(i, j int) bool { return out[i].PctUsed > out[j].PctUsed })
	return map[string]any{
		"as_of":         asOf,
		"period":        period,
		"period_start":  periodStart.UTC().Format(time.RFC3339),
		"period_end":    periodEnd.UTC().Format(time.RFC3339),
		"base_currency": base,
		"budgets":       out,
	}, nil
}

func readBudget(ctx *sdk.AppCtx, id int64) (Budget, error) {
	var b Budget
	var arch int
	err := ctx.AppDB().QueryRow(
		`SELECT id, project_id, COALESCE(category_id,0), period, amount, currency,
		        starts_at, archived, created_at
		 FROM budgets WHERE id=?`, id,
	).Scan(&b.ID, &b.ProjectID, &b.CategoryID, &b.Period,
		&b.Amount, &b.Currency, &b.StartsAt, &arch, &b.CreatedAt)
	b.Archived = arch == 1
	return b, err
}

// periodBounds returns the [start, end) for the period containing `at`.
// Periods are calendar-anchored: monthly starts on the 1st, weekly on
// Monday, quarterly on Jan/Apr/Jul/Oct 1, yearly on Jan 1.
func periodBounds(at time.Time, period string) (time.Time, time.Time) {
	at = at.UTC()
	switch period {
	case "weekly":
		d := int(at.Weekday())
		if d == 0 {
			d = 7
		}
		start := time.Date(at.Year(), at.Month(), at.Day()-d+1, 0, 0, 0, 0, time.UTC)
		return start, start.AddDate(0, 0, 7)
	case "monthly":
		start := time.Date(at.Year(), at.Month(), 1, 0, 0, 0, 0, time.UTC)
		return start, start.AddDate(0, 1, 0)
	case "quarterly":
		q := ((int(at.Month()) - 1) / 3) * 3 // 0, 3, 6, 9 → Jan/Apr/Jul/Oct
		start := time.Date(at.Year(), time.Month(q+1), 1, 0, 0, 0, 0, time.UTC)
		return start, start.AddDate(0, 3, 0)
	case "yearly":
		start := time.Date(at.Year(), time.January, 1, 0, 0, 0, 0, time.UTC)
		return start, start.AddDate(1, 0, 0)
	}
	return at, at
}

// computeSpent returns total spending against `categoryID` in the
// window, in `base` currency. categoryID == 0 means "total spend"
// (every expense-kind txn regardless of category). Otherwise we walk
// the category tree to include descendants — a budget on Food covers
// Food → Groceries and Food → Restaurants.
//
// "Spent" kinds are expense, fee, tax. Uncategorised expenses count
// against a total budget but NOT against a category budget — if you
// want them counted, categorise them.
func computeSpent(ctx *sdk.AppCtx, categoryID int64, from, to time.Time, base string) int64 {
	var rows *sql.Rows
	var err error
	fromS := from.UTC().Format(time.RFC3339)
	toS := to.UTC().Format(time.RFC3339)
	if categoryID == 0 {
		rows, err = ctx.AppDB().Query(
			`SELECT t.amount, a.currency
			 FROM transactions t JOIN accounts a ON a.id = t.account_id
			 WHERE t.posted_at >= ? AND t.posted_at < ?
			   AND t.kind IN ('expense','fee','tax')`,
			fromS, toS,
		)
	} else {
		// Recursive descendant set via CTE.
		rows, err = ctx.AppDB().Query(
			`WITH RECURSIVE descendants(id) AS (
			   SELECT id FROM categories WHERE id = ?
			   UNION ALL
			   SELECT c.id FROM categories c JOIN descendants d ON c.parent_id = d.id
			 )
			 SELECT t.amount, a.currency
			 FROM transactions t JOIN accounts a ON a.id = t.account_id
			 WHERE t.posted_at >= ? AND t.posted_at < ?
			   AND t.kind IN ('expense','fee','tax')
			   AND t.category_id IN (SELECT id FROM descendants)`,
			categoryID, fromS, toS,
		)
	}
	if err != nil {
		return 0
	}
	type bare struct {
		amt int64
		ccy string
	}
	drained := []bare{}
	for rows.Next() {
		var b bare
		if err := rows.Scan(&b.amt, &b.ccy); err == nil {
			drained = append(drained, b)
		}
	}
	rows.Close()
	// transactions.amount is negative for expense/fee/tax; flip the
	// sign so "spent" is a positive number a budget can compare to.
	var spent int64
	for _, d := range drained {
		spent += -convertCcy(ctx, d.amt, d.ccy, base)
	}
	if spent < 0 {
		spent = 0
	}
	return spent
}

// ─── txn write helpers ──────────────────────────────────────────

type txnIn struct {
	AccountID      int64
	HoldingID      int64
	PostedAt       string
	Kind           string
	Amount         int64
	Currency       string
	Quantity       float64
	Price          *int64
	CostBasisDelta int64
	Payee          string
	Memo           string
	CategoryID     int64
	TransferID     string
	ExternalID     string
}

func insertTxn(ctx *sdk.AppCtx, in txnIn) (int64, error) {
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	id, err := insertTxnTx(tx, in)
	if err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

func insertTxnTx(tx *sql.Tx, in txnIn) (int64, error) {
	var holdingID, categoryID any
	if in.HoldingID > 0 {
		holdingID = in.HoldingID
	}
	if in.CategoryID > 0 {
		categoryID = in.CategoryID
	}
	var price any
	if in.Price != nil {
		price = *in.Price
	}
	var transferID, externalID any
	if in.TransferID != "" {
		transferID = in.TransferID
	}
	if in.ExternalID != "" {
		externalID = in.ExternalID
	}
	res, err := tx.Exec(
		`INSERT INTO transactions
		   (account_id, holding_id, posted_at, kind, amount, currency, quantity,
		    price, cost_basis_delta, payee, memo, category_id, transfer_id, external_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		in.AccountID, holdingID, in.PostedAt, in.Kind, in.Amount, in.Currency,
		in.Quantity, price, in.CostBasisDelta, in.Payee, in.Memo,
		categoryID, transferID, externalID,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func readTxn(ctx *sdk.AppCtx, id int64) (Transaction, error) {
	row := ctx.AppDB().QueryRow(
		`SELECT id, account_id, COALESCE(holding_id,0), posted_at, kind, amount, currency,
		        quantity, price, cost_basis_delta, payee, memo, COALESCE(category_id,0),
		        COALESCE(transfer_id,''), COALESCE(external_id,''), pending,
		        created_at, updated_at
		 FROM transactions WHERE id=?`, id,
	)
	return scanTxn(row)
}

func scanTxn(r rowScanner) (Transaction, error) {
	var t Transaction
	var price sql.NullInt64
	var pending int
	if err := r.Scan(&t.ID, &t.AccountID, &t.HoldingID, &t.PostedAt, &t.Kind,
		&t.Amount, &t.Currency, &t.Quantity, &price, &t.CostBasisDelta,
		&t.Payee, &t.Memo, &t.CategoryID, &t.TransferID, &t.ExternalID,
		&pending, &t.CreatedAt, &t.UpdatedAt); err != nil {
		return t, err
	}
	if price.Valid {
		v := price.Int64
		t.Price = &v
	}
	t.Pending = pending == 1
	return t, nil
}

// ─── HTTP wrappers ───────────────────────────────────────────────

func (a *App) handleSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		out, err := a.toolSettingsGet(globalCtx, nil)
		writeOrErr(w, out, err)
	case http.MethodPatch, http.MethodPut, http.MethodPost:
		body := map[string]any{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		out, err := a.toolSettingsSet(globalCtx, body)
		writeOrErr(w, out, err)
	default:
		http.Error(w, "GET or PATCH", http.StatusMethodNotAllowed)
	}
}

func (a *App) handleAccounts(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		out, err := a.toolAccountsList(globalCtx, nil)
		writeOrErr(w, out, err)
	case http.MethodPost:
		body := map[string]any{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		out, err := a.toolAccountsCreate(globalCtx, body)
		writeOrErr(w, out, err)
	default:
		http.Error(w, "GET or POST", http.StatusMethodNotAllowed)
	}
}

func (a *App) handleAccountsItem(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r.URL.Path, "/accounts/")
	if !ok {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	switch r.Method {
	case http.MethodGet:
		out, err := a.toolAccountsGet(globalCtx, map[string]any{"id": id})
		writeOrErr(w, out, err)
	case http.MethodPatch, http.MethodPut:
		body := map[string]any{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		body["id"] = id
		out, err := a.toolAccountsUpdate(globalCtx, body)
		writeOrErr(w, out, err)
	case http.MethodDelete:
		if _, err := a.toolAccountsDelete(globalCtx, map[string]any{"id": id}); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "GET, PATCH or DELETE", http.StatusMethodNotAllowed)
	}
}

func (a *App) handleInstruments(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		q := r.URL.Query().Get("query")
		args := map[string]any{"query": q}
		if k := r.URL.Query().Get("kind"); k != "" {
			args["kind"] = k
		}
		out, err := a.toolInstrumentsSearch(globalCtx, args)
		writeOrErr(w, out, err)
	case http.MethodPost:
		body := map[string]any{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		out, err := a.toolInstrumentsCreate(globalCtx, body)
		writeOrErr(w, out, err)
	default:
		http.Error(w, "GET or POST", http.StatusMethodNotAllowed)
	}
}

func (a *App) handleInstrumentsItem(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r.URL.Path, "/instruments/")
	if !ok {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	switch r.Method {
	case http.MethodGet:
		out, err := a.toolInstrumentsGet(globalCtx, map[string]any{"id": id})
		writeOrErr(w, out, err)
	case http.MethodPatch, http.MethodPut:
		body := map[string]any{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		body["id"] = id
		out, err := a.toolInstrumentsUpdate(globalCtx, body)
		writeOrErr(w, out, err)
	default:
		http.Error(w, "GET or PATCH", http.StatusMethodNotAllowed)
	}
}

func (a *App) handleHoldings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		args := map[string]any{}
		if v := r.URL.Query().Get("account_id"); v != "" {
			if n, _ := strconv.ParseInt(v, 10, 64); n > 0 {
				args["account_id"] = float64(n)
			}
		}
		if v := r.URL.Query().Get("instrument_id"); v != "" {
			if n, _ := strconv.ParseInt(v, 10, 64); n > 0 {
				args["instrument_id"] = float64(n)
			}
		}
		if r.URL.Query().Get("include_closed") == "true" {
			args["include_closed"] = true
		}
		out, err := a.toolHoldingsList(globalCtx, args)
		writeOrErr(w, out, err)
	case http.MethodPost:
		body := map[string]any{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		out, err := a.toolHoldingsSet(globalCtx, body)
		writeOrErr(w, out, err)
	default:
		http.Error(w, "GET or POST", http.StatusMethodNotAllowed)
	}
}

func (a *App) handleHoldingsItem(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r.URL.Path, "/holdings/")
	if !ok {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	out, err := a.toolHoldingsGet(globalCtx, map[string]any{"id": id})
	writeOrErr(w, out, err)
}

func (a *App) handleTxns(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		args := map[string]any{}
		for _, k := range []string{"kind", "from", "to", "payee"} {
			if v := r.URL.Query().Get(k); v != "" {
				args[k] = v
			}
		}
		for _, k := range []string{"account_id", "holding_id", "category_id", "limit"} {
			if v := r.URL.Query().Get(k); v != "" {
				if n, _ := strconv.ParseInt(v, 10, 64); n > 0 {
					args[k] = float64(n)
				}
			}
		}
		out, err := a.toolTxnsList(globalCtx, args)
		writeOrErr(w, out, err)
	case http.MethodPost:
		body := map[string]any{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		out, err := a.toolTxnsCreate(globalCtx, body)
		writeOrErr(w, out, err)
	default:
		http.Error(w, "GET or POST", http.StatusMethodNotAllowed)
	}
}

func (a *App) handleTxnsItem(w http.ResponseWriter, r *http.Request) {
	// Long-prefix routes (/txns/buy, /txns/sell, …) match before this
	// handler because Go's mux dispatches longer patterns first.
	id, ok := pathID(r.URL.Path, "/txns/")
	if !ok {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	switch r.Method {
	case http.MethodGet:
		out, err := a.toolTxnsGet(globalCtx, map[string]any{"id": id})
		writeOrErr(w, out, err)
	case http.MethodPatch, http.MethodPut:
		body := map[string]any{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		body["id"] = id
		out, err := a.toolTxnsUpdate(globalCtx, body)
		writeOrErr(w, out, err)
	case http.MethodDelete:
		if _, err := a.toolTxnsDelete(globalCtx, map[string]any{"id": id}); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "GET, PATCH or DELETE", http.StatusMethodNotAllowed)
	}
}

func (a *App) handleTxnsBuy(w http.ResponseWriter, r *http.Request) {
	postBody(w, r, a.toolTxnsBuy)
}
func (a *App) handleTxnsSell(w http.ResponseWriter, r *http.Request) {
	postBody(w, r, a.toolTxnsSell)
}
func (a *App) handleTxnsDividend(w http.ResponseWriter, r *http.Request) {
	postBody(w, r, a.toolTxnsDividend)
}
func (a *App) handleTxnsInterest(w http.ResponseWriter, r *http.Request) {
	postBody(w, r, a.toolTxnsInterest)
}
func (a *App) handleTxnsTransfer(w http.ResponseWriter, r *http.Request) {
	postBody(w, r, a.toolTxnsTransfer)
}
func (a *App) handleValuations(w http.ResponseWriter, r *http.Request) {
	postBody(w, r, a.toolValuationSet)
}

func (a *App) handleCategories(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		out, err := a.toolCategoriesList(globalCtx, nil)
		writeOrErr(w, out, err)
	case http.MethodPost:
		body := map[string]any{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		// Special: a body of {"seed": true} triggers seeding.
		if v, _ := body["seed"].(bool); v {
			out, err := a.toolCategoriesSeed(globalCtx, nil)
			writeOrErr(w, out, err)
			return
		}
		out, err := a.toolCategoriesCreate(globalCtx, body)
		writeOrErr(w, out, err)
	default:
		http.Error(w, "GET or POST", http.StatusMethodNotAllowed)
	}
}

func (a *App) handleCategoriesItem(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r.URL.Path, "/categories/")
	if !ok {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	switch r.Method {
	case http.MethodPatch, http.MethodPut:
		body := map[string]any{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		body["id"] = id
		out, err := a.toolCategoriesUpdate(globalCtx, body)
		writeOrErr(w, out, err)
	case http.MethodDelete:
		if _, err := a.toolCategoriesDelete(globalCtx, map[string]any{"id": id}); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "PATCH or DELETE", http.StatusMethodNotAllowed)
	}
}

func (a *App) handlePrices(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		args := map[string]any{}
		if v := r.URL.Query().Get("instrument_id"); v != "" {
			if n, _ := strconv.ParseInt(v, 10, 64); n > 0 {
				args["instrument_id"] = float64(n)
			}
		}
		if v := r.URL.Query().Get("as_of"); v != "" {
			args["as_of"] = v
		}
		out, err := a.toolPricesGet(globalCtx, args)
		writeOrErr(w, out, err)
	case http.MethodPost:
		postBody(w, r, a.toolPricesSet)
	default:
		http.Error(w, "GET or POST", http.StatusMethodNotAllowed)
	}
}

func (a *App) handleFX(w http.ResponseWriter, r *http.Request) {
	postBody(w, r, a.toolFXSet)
}

func (a *App) handleReportNetWorth(w http.ResponseWriter, r *http.Request) {
	args := queryToArgs(r.URL.Query(), []string{"at", "from", "to", "series"})
	out, err := a.toolReportsNetWorth(globalCtx, args)
	writeOrErr(w, out, err)
}

func (a *App) handleReportAllocation(w http.ResponseWriter, r *http.Request) {
	out, err := a.toolReportsAllocation(globalCtx, nil)
	writeOrErr(w, out, err)
}

func (a *App) handleReportPerformance(w http.ResponseWriter, r *http.Request) {
	args := queryToArgs(r.URL.Query(), []string{"from", "to"})
	out, err := a.toolReportsPerformance(globalCtx, args)
	writeOrErr(w, out, err)
}

func (a *App) handleReportCashflow(w http.ResponseWriter, r *http.Request) {
	args := queryToArgs(r.URL.Query(), []string{"from", "to", "bucket"})
	out, err := a.toolReportsCashflow(globalCtx, args)
	writeOrErr(w, out, err)
}

func (a *App) handleImportCSV(w http.ResponseWriter, r *http.Request) {
	postBody(w, r, a.toolImportCSV)
}

func (a *App) handleBudgets(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		out, err := a.toolBudgetsList(globalCtx, nil)
		writeOrErr(w, out, err)
	case http.MethodPost:
		body := map[string]any{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		out, err := a.toolBudgetsSet(globalCtx, body)
		writeOrErr(w, out, err)
	default:
		http.Error(w, "GET or POST", http.StatusMethodNotAllowed)
	}
}

func (a *App) handleBudgetsItem(w http.ResponseWriter, r *http.Request) {
	// /budgets/status is its own handler — Go's mux routes longer
	// patterns first, so this branch only fires for /budgets/<id>.
	id, ok := pathID(r.URL.Path, "/budgets/")
	if !ok {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	if r.Method != http.MethodDelete {
		http.Error(w, "DELETE", http.StatusMethodNotAllowed)
		return
	}
	if _, err := a.toolBudgetsDelete(globalCtx, map[string]any{"id": id}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) handleBudgetsStatus(w http.ResponseWriter, r *http.Request) {
	args := queryToArgs(r.URL.Query(), []string{"as_of", "period"})
	out, err := a.toolBudgetsStatus(globalCtx, args)
	writeOrErr(w, out, err)
}

// ─── helpers ─────────────────────────────────────────────────────

func postBody(w http.ResponseWriter, r *http.Request, fn func(*sdk.AppCtx, map[string]any) (any, error)) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	body := map[string]any{}
	_ = json.NewDecoder(r.Body).Decode(&body)
	out, err := fn(globalCtx, body)
	writeOrErr(w, out, err)
}

func writeOrErr(w http.ResponseWriter, v any, err error) {
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func queryToArgs(q map[string][]string, keys []string) map[string]any {
	out := map[string]any{}
	for _, k := range keys {
		if v := q[k]; len(v) > 0 && v[0] != "" {
			out[k] = v[0]
		}
	}
	return out
}

func projectID() string { return os.Getenv("APTEVA_PROJECT_ID") }

func schemaObject(props map[string]any, required []string) map[string]any {
	s := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func strArg(m map[string]any, key, def string) string {
	if v, ok := m[key].(string); ok && v != "" {
		return v
	}
	return def
}

func intArg(m map[string]any, key string, def int) int {
	return intArgFromAny(m[key], def)
}

func intArgFromAny(v any, def int) int {
	switch v := v.(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	case string:
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return n
		}
	}
	return def
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func absInt64(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullableString(s sql.NullString) any {
	if !s.Valid {
		return nil
	}
	return s.String
}

// pathID pulls the numeric id out of /prefix/<id> or /prefix/<id>/foo.
func pathID(path, prefix string) (int64, bool) {
	rest := strings.TrimPrefix(path, prefix)
	if i := strings.Index(rest, "/"); i >= 0 {
		rest = rest[:i]
	}
	id, err := strconv.ParseInt(rest, 10, 64)
	if err != nil || id == 0 {
		return 0, false
	}
	return id, true
}

func parseFlexibleTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, errors.New("empty")
	}
	formats := []string{
		time.RFC3339,
		time.RFC3339Nano,
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05",
		"2006-01-02",
	}
	for _, f := range formats {
		if t, err := time.Parse(f, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("can't parse %q (try RFC3339)", s)
}
