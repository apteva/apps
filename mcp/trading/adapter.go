package main

// Broker adapter abstraction. The trading sidecar is broker-agnostic at
// the dispatch layer — tools.go and exec.go look up the per-portfolio
// adapter via brokerFor() and call interface methods. Each broker is a
// single file (binance.go, alpaca.go, …) implementing brokerAdapter and
// self-registering in init(). Adding a broker = one file, no edits to
// the rest of the codebase beyond a one-line entry in compatible_slugs
// in the manifest.

import (
	"encoding/json"
	"fmt"
	"sort"
	"sync"

	sdk "github.com/apteva/app-sdk"
)

// brokerAdapter — pure translation between our local Order/Mark/Position
// types and one specific broker's REST shapes. No I/O: the integration
// runner in apteva-server signs and transports every request; adapters
// just build args and parse responses.
type brokerAdapter interface {
	// Slug — the integration's app_slug; matches compatible_slugs in
	// the trading app's manifest and the connection's app_slug at runtime.
	Slug() string

	// Capabilities — declared once at startup. Used by:
	//   - portfolio_create, to validate allowed_classes against the broker
	//   - brokers_list, so the agent can reason about what fits
	//   - per-broker behavior tweaks (cancel-by-client-id, fractional)
	Capabilities() brokerCapabilities

	// ToolMap — capability → upstream tool name. The trading app calls
	// capabilities by logical name (order.place, account.summary), and
	// the adapter resolves to the broker-specific tool (create_order,
	// get_account, …). Matches the manifest's `tools:` map for the
	// adapter's slug.
	ToolMap() map[string]string

	// ToBrokerSymbol — local canonical (BTC-USD, AAPL) → broker form
	// (BTCUSDT, AAPL, BTC/USD). Idempotent; called from translation and
	// from cancel/status paths.
	ToBrokerSymbol(canonical string) string

	// TranslateOrder — local Order → broker create_order args. Pure;
	// returns an error for orders the broker can't represent (e.g.
	// crypto on an equity-only adapter).
	TranslateOrder(o *Order) (map[string]any, error)

	// ParseOrder — broker create_order / get_order response → normalized
	// brokerOrderResult. Same shape regardless of broker.
	ParseOrder(raw json.RawMessage) (*brokerOrderResult, error)

	// ParseAccount — broker get_account response → quote cash + holdings.
	// Adapters whose get_account doesn't include holdings (Alpaca) return
	// an empty Holdings map here and report HoldingsTool() != "" so the
	// caller knows to fetch list_positions separately and merge via
	// ParseHoldings.
	ParseAccount(raw json.RawMessage) (*brokerAccount, error)

	// HoldingsTool — "" when ParseAccount already includes holdings
	// (Binance); the upstream tool name (e.g. "list_positions") for
	// adapters that need a second call (Alpaca). The capability key is
	// "positions.list" — adapters can route it via ToolMap or fall
	// through to this literal.
	HoldingsTool() string

	// ParseHoldings — when HoldingsTool() != "", parses the response of
	// that tool into the standard {asset → balance} map. Symbols are
	// canonical local form (AAPL, BTC-USD).
	ParseHoldings(raw json.RawMessage) (map[string]brokerBalance, error)

	// CancelArgs — args for cancel_order. Receives the local order so
	// the adapter can use whichever cancel handle works best (client id
	// preferred where supported; broker id as fallback).
	CancelArgs(o *Order, brokerOrderID string) map[string]any

	// StatusArgs — args for get_order. Same handle-selection rules.
	StatusArgs(o *Order, brokerOrderID string) map[string]any

	// IsUnknownOrderError — true when the broker has confirmed the order
	// does not exist on its side (e.g. Binance -2013). The reconciler
	// uses this to flip an "uncertain" local order from working to
	// rejected with high confidence. Anything else is treated as
	// transient.
	IsUnknownOrderError(code, detail string) bool

	// ErrText — extract (code, detail) from a failed broker call. Each
	// broker has its own error envelope shape; Binance is
	// {"code": -2010, "msg": "..."}, Alpaca is
	// {"code": 40010001, "message": "..."}. Returning ("", "") signals
	// a successful call (callers should not invoke ErrText then).
	ErrText(res *sdk.ExecuteResult, err error) (code, detail string)
}

type brokerCapabilities struct {
	AssetClasses     []string // canonical: equity, etf, crypto, option, polymarket
	OrderTypes       []string // market, limit, stop, stop_limit, trailing_stop
	TIFs             []string // day, gtc, ioc, fok, opg, cls
	Fractional       bool     // accepts fractional qty
	CancelByClientID bool     // true if cancel works without the broker order id
	QuoteCurrency    string   // USD, USDT, EUR — denomination of the cash side
}

// ─── Registry ──────────────────────────────────────────────────────

var (
	adaptersMu sync.RWMutex
	adapters   = map[string]brokerAdapter{}
)

func registerAdapter(a brokerAdapter) {
	adaptersMu.Lock()
	defer adaptersMu.Unlock()
	adapters[a.Slug()] = a
}

func adapterBySlug(slug string) brokerAdapter {
	adaptersMu.RLock()
	defer adaptersMu.RUnlock()
	return adapters[slug]
}

func allAdapters() []brokerAdapter {
	adaptersMu.RLock()
	defer adaptersMu.RUnlock()
	out := make([]brokerAdapter, 0, len(adapters))
	for _, a := range adapters {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Slug() < out[j].Slug() })
	return out
}

func adapterSupportsClass(a brokerAdapter, class string) bool {
	for _, c := range a.Capabilities().AssetClasses {
		if c == class {
			return true
		}
	}
	return false
}

// ─── Per-portfolio binding ────────────────────────────────────────
//
// brokerFor resolves the (adapter, connection) pair a live portfolio
// should use right now. Returns errBrokerUnbound if no connection of
// the portfolio's broker_slug is currently bound — callers degrade
// gracefully (order_place rejects with broker_unbound, tryReconcile
// logs and leaves the order working).

var (
	errPaper         = fmt.Errorf("paper portfolio — no broker")
	errBrokerUnbound = fmt.Errorf("broker unbound")
)

type boundBroker struct {
	Adapter      brokerAdapter
	ConnectionID int64
}

func brokerFor(ctx *sdk.AppCtx, pf *Portfolio) (*boundBroker, error) {
	if pf.Mode != "live" || pf.BrokerSlug == "" {
		return nil, errPaper
	}
	a := adapterBySlug(pf.BrokerSlug)
	if a == nil {
		return nil, fmt.Errorf("unsupported broker %q (no adapter registered)", pf.BrokerSlug)
	}
	if ctx == nil || ctx.PlatformAPI() == nil {
		return nil, errBrokerUnbound
	}
	conns, err := ctx.PlatformAPI().ListConnections(sdk.ConnectionFilter{
		AppSlug: pf.BrokerSlug,
	})
	if err != nil {
		return nil, fmt.Errorf("list connections for %s: %w", pf.BrokerSlug, err)
	}
	// One-broker-per-install: take the first active connection. If two
	// of the same slug exist, the operator made an unsupported choice;
	// we pick deterministically (lowest ID) and trust that future-us
	// disambiguates if/when we drop the one-of-each constraint.
	var pick *sdk.PlatformConnection
	for i := range conns {
		c := conns[i]
		if c.Status != "" && c.Status != "active" && c.Status != "connected" {
			continue
		}
		if pick == nil || c.ID < pick.ID {
			pick = &c
		}
	}
	if pick == nil {
		return nil, errBrokerUnbound
	}
	return &boundBroker{Adapter: a, ConnectionID: pick.ID}, nil
}

// toolFor wraps an adapter's tool map so callers don't have to nil-check
// — falls through to the capability name when the adapter doesn't
// override it (lets simple adapters skip the map entirely).
func (b *boundBroker) toolFor(capability string) string {
	if b == nil || b.Adapter == nil {
		return capability
	}
	if m := b.Adapter.ToolMap(); m != nil {
		if t, ok := m[capability]; ok {
			return t
		}
	}
	return capability
}
