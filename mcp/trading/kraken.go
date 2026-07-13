package main

// Kraken broker adapter. Pure translation between local Trading orders
// and Kraken's REST private endpoints; apteva-server signs and sends the
// actual requests via the kraken integration.
//
// Scope in this first adapter: spot crypto, USD-quoted pairs,
// long-only, market/limit/stop. Kraken symbols use XBT for BTC on the
// wire; the trading app keeps BTC-USD internally.

import (
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"sort"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

func init() { registerAdapter(&krakenAdapter{}) }

type krakenAdapter struct{}

func (krakenAdapter) Slug() string { return "kraken" }

func (krakenAdapter) Capabilities() brokerCapabilities {
	return brokerCapabilities{
		AssetClasses:     []string{"crypto"},
		OrderTypes:       []string{"market", "limit", "stop"},
		TIFs:             []string{"gtc", "ioc", "gtd"},
		Fractional:       true,
		CancelByClientID: false,
		QuoteCurrency:    "USD",
	}
}

func (krakenAdapter) ToolMap() map[string]string {
	return map[string]string{
		"order.place":     "add_order",
		"order.cancel":    "cancel_order",
		"order.status":    "query_orders",
		"account.summary": "get_balance",
	}
}

func (krakenAdapter) ToBrokerSymbol(canonical string) string { return toKrakenSymbol(canonical) }

func (krakenAdapter) TranslateOrder(o *Order) (map[string]any, error) {
	if o == nil {
		return nil, errors.New("nil order")
	}
	if o.AssetClass != "crypto" {
		return nil, fmt.Errorf("Kraken adapter handles crypto only, got %q", o.AssetClass)
	}
	args := map[string]any{
		"pair":    toKrakenSymbol(o.Symbol),
		"type":    strings.ToLower(o.Side),
		"volume":  formatKrakenDecimal(o.Qty),
		"userref": krakenUserRef(o.ID),
	}
	if args["type"] != "buy" && args["type"] != "sell" {
		return nil, fmt.Errorf("unsupported side %q", o.Side)
	}

	tif := strings.ToLower(o.TIF)
	if tif == "" || tif == "day" {
		tif = "gtc"
	}
	args["timeinforce"] = strings.ToUpper(tif)

	switch o.Type {
	case "market":
		args["ordertype"] = "market"
		delete(args, "timeinforce")
	case "limit":
		if o.LimitPrice == nil {
			return nil, errors.New("limit order missing limit_price")
		}
		args["ordertype"] = "limit"
		args["price"] = formatKrakenDecimal(*o.LimitPrice)
	case "stop":
		if o.StopPrice == nil {
			return nil, errors.New("stop order missing stop_price")
		}
		args["ordertype"] = "stop-loss"
		args["price"] = formatKrakenDecimal(*o.StopPrice)
	default:
		return nil, fmt.Errorf("unsupported order type %q", o.Type)
	}
	return args, nil
}

func (krakenAdapter) ParseOrder(raw json.RawMessage) (*brokerOrderResult, error) {
	result, err := krakenResult(raw)
	if err != nil {
		return nil, err
	}

	var add struct {
		TxID []string `json:"txid"`
	}
	if err := json.Unmarshal(result, &add); err == nil && len(add.TxID) > 0 {
		return &brokerOrderResult{
			BrokerOrderID: add.TxID[0],
			Status:        "working",
			BrokerStatus:  "accepted",
		}, nil
	}

	var orders map[string]krakenOrder
	if err := json.Unmarshal(result, &orders); err != nil {
		return nil, fmt.Errorf("decode kraken order response: %w", err)
	}
	if len(orders) == 0 {
		return nil, fmt.Errorf("kraken order response missing order: %s", string(raw))
	}
	keys := make([]string, 0, len(orders))
	for k := range orders {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	id := keys[0]
	return krakenOrderResult(id, orders[id]), nil
}

func (krakenAdapter) ParseAccount(raw json.RawMessage) (*brokerAccount, error) {
	result, err := krakenResult(raw)
	if err != nil {
		return nil, err
	}
	var balances map[string]string
	if err := json.Unmarshal(result, &balances); err != nil {
		return nil, fmt.Errorf("decode kraken balance response: %w", err)
	}
	out := &brokerAccount{Holdings: map[string]brokerBalance{}, HoldingsComplete: true}
	for asset, amountText := range balances {
		amount := parseFloat(amountText)
		if amount <= 0 {
			continue
		}
		switch krakenAsset(asset) {
		case "USD":
			out.QuoteCash += amount
			out.QuoteAvailable += amount
		case "USDT", "USDC":
			out.QuoteCash += amount
			out.QuoteAvailable += amount
		default:
			canonical := fromKrakenAsset(asset) + "-USD"
			out.Holdings[canonical] = brokerBalance{
				Asset: canonical,
				Free:  amount,
				Total: amount,
			}
		}
	}
	return out, nil
}

func (krakenAdapter) HoldingsTool() string { return "" }
func (krakenAdapter) ParseHoldings(raw json.RawMessage) (map[string]brokerBalance, error) {
	return map[string]brokerBalance{}, nil
}
func (krakenAdapter) OrdersHistoryTool() (string, map[string]any) { return "", nil }
func (krakenAdapter) OpenOrdersTool() (string, map[string]any)    { return "", nil }
func (krakenAdapter) ParseOrders(raw json.RawMessage) ([]brokerHistoricOrder, error) {
	return nil, nil
}

func (krakenAdapter) CancelArgs(o *Order, brokerOrderID string) map[string]any {
	if brokerOrderID == "" && o != nil {
		return map[string]any{"txid": strconv.FormatInt(int64(krakenUserRef(o.ID)), 10)}
	}
	return map[string]any{"txid": brokerOrderID}
}

func (krakenAdapter) StatusArgs(o *Order, brokerOrderID string) map[string]any {
	return map[string]any{"txid": brokerOrderID}
}

func (krakenAdapter) IsUnknownOrderError(code, detail string) bool {
	d := strings.ToLower(detail)
	return strings.Contains(d, "unknown order") || strings.Contains(d, "unknown txid")
}

func (krakenAdapter) ErrText(res *sdk.ExecuteResult, err error) (code, detail string) {
	return krakenErrText(res, err)
}

func toKrakenSymbol(canonical string) string {
	s := strings.ToUpper(strings.TrimSpace(canonical))
	s = strings.ReplaceAll(s, "/", "")
	if strings.HasSuffix(s, "-USD") {
		base := strings.TrimSuffix(s, "-USD")
		if base == "BTC" {
			base = "XBT"
		}
		return base + "USD"
	}
	if strings.HasPrefix(s, "BTC") {
		return "XBT" + strings.TrimPrefix(s, "BTC")
	}
	return strings.ReplaceAll(s, "-", "")
}

func fromKrakenSymbol(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "/", "")
	s = strings.ReplaceAll(s, "-", "")
	s = strings.TrimSuffix(s, ".D")
	var base string
	switch {
	case strings.HasSuffix(s, "ZUSD"):
		base = strings.TrimSuffix(s, "ZUSD")
	case strings.HasSuffix(s, "USD"):
		base = strings.TrimSuffix(s, "USD")
	default:
		return s
	}
	base = strings.TrimPrefix(base, "X")
	if base == "BT" || base == "XBT" {
		base = "BTC"
	}
	return base + "-USD"
}

func fromKrakenAsset(asset string) string {
	a := krakenAsset(asset)
	if a == "XBT" {
		return "BTC"
	}
	return a
}

func krakenAsset(asset string) string {
	a := strings.ToUpper(strings.TrimSpace(asset))
	a = strings.TrimPrefix(a, "Z")
	a = strings.TrimPrefix(a, "X")
	if a == "BT" {
		return "XBT"
	}
	return a
}

func formatKrakenDecimal(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

func krakenUserRef(id string) int32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(id))
	return int32(h.Sum32() & 0x7fffffff)
}

type krakenOrder struct {
	Status  string `json:"status"`
	Vol     string `json:"vol"`
	VolExec string `json:"vol_exec"`
	Cost    string `json:"cost"`
	Price   string `json:"price"`
	Fee     string `json:"fee"`
	UserRef any    `json:"userref"`
	Descr   struct {
		Pair      string `json:"pair"`
		Type      string `json:"type"`
		OrderType string `json:"ordertype"`
		Price     string `json:"price"`
		Price2    string `json:"price2"`
	} `json:"descr"`
}

func krakenOrderResult(id string, o krakenOrder) *brokerOrderResult {
	executed := parseFloat(o.VolExec)
	cost := parseFloat(o.Cost)
	return &brokerOrderResult{
		BrokerOrderID:       id,
		BrokerStatus:        o.Status,
		Status:              mapKrakenStatus(o.Status, parseFloat(o.Vol), executed),
		ExecutedQty:         executed,
		CummulativeQuoteQty: cost,
	}
}

func mapKrakenStatus(status string, volume, executed float64) string {
	switch strings.ToLower(status) {
	case "closed":
		if volume > 0 && executed < volume {
			return "working"
		}
		return "filled"
	case "canceled", "cancelled", "expired":
		return "cancelled"
	default:
		return "working"
	}
}

func krakenResult(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		return nil, errors.New("empty kraken response")
	}
	var env struct {
		Error  []string        `json:"error"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("decode kraken response: %w", err)
	}
	if len(env.Error) > 0 {
		return nil, fmt.Errorf("kraken error: %s", strings.Join(env.Error, "; "))
	}
	if len(env.Result) == 0 {
		return nil, fmt.Errorf("kraken response missing result: %s", string(raw))
	}
	return env.Result, nil
}

func krakenErrText(res *sdk.ExecuteResult, err error) (code, detail string) {
	if err != nil {
		return "broker_call_failed", err.Error()
	}
	if res == nil {
		return "broker_no_response", "no response from broker"
	}
	var env struct {
		Error []string `json:"error"`
	}
	if len(res.Data) > 0 {
		if jerr := json.Unmarshal(res.Data, &env); jerr == nil && len(env.Error) > 0 {
			first := env.Error[0]
			codePart := strings.SplitN(first, ":", 2)[0]
			codePart = strings.ReplaceAll(codePart, " ", "_")
			return "kraken_" + codePart, strings.Join(env.Error, "; ")
		}
	}
	if !res.Success {
		return "broker_non_2xx", string(res.Data)
	}
	return "", ""
}
