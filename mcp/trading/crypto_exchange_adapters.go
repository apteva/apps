package main

// Spot-crypto adapters for exchange integrations that already exist in
// the platform catalog. These are deliberately conservative: they cover
// cash spot, long-only, base-quantity orders and account/balance parsing.
// Derivatives, margin, and exchange-specific algo orders stay out until
// the trading engine models those risks explicitly.

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

func init() {
	registerAdapter(&coinbaseAdapter{})
	registerAdapter(&okxAdapter{})
	registerAdapter(&bybitAdapter{})
	registerAdapter(&bitstampAdapter{})
}

// ─── Coinbase Advanced ────────────────────────────────────────────

type coinbaseAdapter struct{}

func (coinbaseAdapter) Slug() string { return "coinbase" }
func (coinbaseAdapter) Capabilities() brokerCapabilities {
	return brokerCapabilities{
		AssetClasses:     []string{"crypto"},
		OrderTypes:       []string{"market", "limit", "stop"},
		TIFs:             []string{"gtc", "ioc", "gtd"},
		Fractional:       true,
		CancelByClientID: false,
		QuoteCurrency:    "USD",
	}
}
func (coinbaseAdapter) ToolMap() map[string]string {
	return map[string]string{
		"order.place":     "create_order",
		"order.cancel":    "cancel_orders",
		"order.status":    "get_order",
		"account.summary": "list_accounts",
	}
}
func (coinbaseAdapter) ToBrokerSymbol(canonical string) string { return toDashUSD(canonical) }
func (coinbaseAdapter) HoldingsTool() string                   { return "" }
func (coinbaseAdapter) ParseHoldings(_ json.RawMessage) (map[string]brokerBalance, error) {
	return map[string]brokerBalance{}, nil
}
func (coinbaseAdapter) OrdersHistoryTool() (string, map[string]any) { return "", nil }
func (coinbaseAdapter) OpenOrdersTool() (string, map[string]any)    { return "", nil }
func (coinbaseAdapter) ParseOrders(_ json.RawMessage) ([]brokerHistoricOrder, error) {
	return nil, nil
}
func (coinbaseAdapter) CancelArgs(_ *Order, brokerOrderID string) map[string]any {
	return map[string]any{"order_ids": []string{brokerOrderID}}
}
func (coinbaseAdapter) StatusArgs(_ *Order, brokerOrderID string) map[string]any {
	return map[string]any{"order_id": brokerOrderID}
}
func (coinbaseAdapter) IsUnknownOrderError(code, detail string) bool {
	d := strings.ToLower(detail)
	return strings.Contains(code, "not_found") || strings.Contains(d, "not found") || strings.Contains(d, "unknown order")
}
func (coinbaseAdapter) ErrText(res *sdk.ExecuteResult, err error) (string, string) {
	return jsonErrText("coinbase", res, err, "error", "message")
}

func (coinbaseAdapter) TranslateOrder(o *Order) (map[string]any, error) {
	if err := requireCryptoOrder("Coinbase", o); err != nil {
		return nil, err
	}
	cfg := map[string]any{}
	base := formatExchangeDecimal(o.Qty)
	tif := strings.ToLower(o.TIF)
	if tif == "" || tif == "day" {
		tif = "gtc"
	}
	switch o.Type {
	case "market":
		cfg["market_market_ioc"] = map[string]any{"base_size": base}
	case "limit":
		if o.LimitPrice == nil {
			return nil, errors.New("limit order missing limit_price")
		}
		if tif == "ioc" {
			cfg["sor_limit_ioc"] = map[string]any{"base_size": base, "limit_price": formatExchangeDecimal(*o.LimitPrice)}
		} else {
			key := "limit_limit_gtc"
			if tif == "gtd" {
				key = "limit_limit_gtd"
			}
			cfg[key] = map[string]any{
				"base_size":   base,
				"limit_price": formatExchangeDecimal(*o.LimitPrice),
				"post_only":   false,
			}
		}
	case "stop":
		if o.StopPrice == nil {
			return nil, errors.New("stop order missing stop_price")
		}
		key := "stop_limit_stop_limit_gtc"
		if tif == "gtd" {
			key = "stop_limit_stop_limit_gtd"
		}
		direction := "STOP_DIRECTION_STOP_DOWN"
		if strings.EqualFold(o.Side, "buy") {
			direction = "STOP_DIRECTION_STOP_UP"
		}
		cfg[key] = map[string]any{
			"base_size":      base,
			"limit_price":    formatExchangeDecimal(*o.StopPrice),
			"stop_price":     formatExchangeDecimal(*o.StopPrice),
			"stop_direction": direction,
		}
	default:
		return nil, fmt.Errorf("unsupported order type %q", o.Type)
	}
	return map[string]any{
		"client_order_id":     o.ID,
		"product_id":          toDashUSD(o.Symbol),
		"side":                strings.ToUpper(o.Side),
		"order_configuration": cfg,
	}, nil
}

func (coinbaseAdapter) ParseOrder(raw json.RawMessage) (*brokerOrderResult, error) {
	var env struct {
		Success         bool            `json:"success"`
		SuccessResponse json.RawMessage `json:"success_response"`
		ErrorResponse   json.RawMessage `json:"error_response"`
		Order           json.RawMessage `json:"order"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("decode coinbase order response: %w", err)
	}
	if len(env.ErrorResponse) > 0 {
		return nil, fmt.Errorf("coinbase order error: %s", string(env.ErrorResponse))
	}
	payload := env.Order
	if len(payload) == 0 {
		payload = env.SuccessResponse
	}
	var o struct {
		OrderID            string `json:"order_id"`
		ID                 string `json:"id"`
		ClientOrderID      string `json:"client_order_id"`
		Status             string `json:"status"`
		FilledSize         string `json:"filled_size"`
		AverageFilledPrice string `json:"average_filled_price"`
		FilledValue        string `json:"filled_value"`
		TotalValue         string `json:"total_value_after_fees"`
	}
	if err := json.Unmarshal(payload, &o); err != nil {
		return nil, fmt.Errorf("decode coinbase order payload: %w", err)
	}
	id := firstString(o.OrderID, o.ID)
	if id == "" {
		return nil, fmt.Errorf("coinbase order response missing order_id: %s", string(raw))
	}
	executed := parseFloat(o.FilledSize)
	cum := parseFloat(firstString(o.FilledValue, o.TotalValue))
	if cum == 0 && executed > 0 {
		cum = executed * parseFloat(o.AverageFilledPrice)
	}
	status := o.Status
	if status == "" {
		status = "accepted"
	}
	return &brokerOrderResult{
		BrokerOrderID:       id,
		ClientOrderID:       o.ClientOrderID,
		BrokerStatus:        status,
		Status:              mapSimpleOrderStatus(status),
		ExecutedQty:         executed,
		CummulativeQuoteQty: cum,
	}, nil
}

func (coinbaseAdapter) ParseAccount(raw json.RawMessage) (*brokerAccount, error) {
	var resp struct {
		Accounts []struct {
			Currency         string `json:"currency"`
			AvailableBalance struct {
				Value string `json:"value"`
			} `json:"available_balance"`
			Hold struct {
				Value string `json:"value"`
			} `json:"hold"`
		} `json:"accounts"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("decode coinbase accounts: %w", err)
	}
	out := &brokerAccount{Holdings: map[string]brokerBalance{}, HoldingsComplete: true}
	for _, a := range resp.Accounts {
		asset := strings.ToUpper(a.Currency)
		free := parseFloat(a.AvailableBalance.Value)
		addCryptoBalance(out, asset, free, free+parseFloat(a.Hold.Value))
	}
	return out, nil
}

// ─── OKX ──────────────────────────────────────────────────────────

type okxAdapter struct{}

func (okxAdapter) Slug() string { return "okx" }
func (okxAdapter) Capabilities() brokerCapabilities {
	return brokerCapabilities{AssetClasses: []string{"crypto"}, OrderTypes: []string{"market", "limit"}, TIFs: []string{"gtc", "ioc", "fok"}, Fractional: true, CancelByClientID: true, QuoteCurrency: "USDT"}
}
func (okxAdapter) ToolMap() map[string]string {
	return map[string]string{"order.place": "place_order", "order.cancel": "cancel_order", "order.status": "get_order", "account.summary": "get_balance"}
}
func (okxAdapter) ToBrokerSymbol(canonical string) string { return toUSDTDashed(canonical) }
func (okxAdapter) HoldingsTool() string                   { return "" }
func (okxAdapter) ParseHoldings(_ json.RawMessage) (map[string]brokerBalance, error) {
	return map[string]brokerBalance{}, nil
}
func (okxAdapter) OrdersHistoryTool() (string, map[string]any) { return "", nil }
func (okxAdapter) OpenOrdersTool() (string, map[string]any)    { return "", nil }
func (okxAdapter) ParseOrders(_ json.RawMessage) ([]brokerHistoricOrder, error) {
	return nil, nil
}
func (okxAdapter) CancelArgs(o *Order, brokerOrderID string) map[string]any {
	args := map[string]any{"instId": toUSDTDashed(o.Symbol)}
	if brokerOrderID != "" {
		args["ordId"] = brokerOrderID
	} else {
		args["clOrdId"] = okxClientOrderID(o.ID)
	}
	return args
}
func (okxAdapter) StatusArgs(o *Order, brokerOrderID string) map[string]any {
	return (okxAdapter{}).CancelArgs(o, brokerOrderID)
}
func (okxAdapter) IsUnknownOrderError(code, detail string) bool {
	d := strings.ToLower(detail)
	return strings.Contains(d, "order does not exist") || strings.Contains(d, "not exist") || strings.Contains(code, "51603")
}
func (okxAdapter) ErrText(res *sdk.ExecuteResult, err error) (string, string) {
	return okxErrText(res, err)
}

func (okxAdapter) TranslateOrder(o *Order) (map[string]any, error) {
	if err := requireCryptoOrder("OKX", o); err != nil {
		return nil, err
	}
	args := map[string]any{
		"instId":  toUSDTDashed(o.Symbol),
		"tdMode":  "cash",
		"side":    strings.ToLower(o.Side),
		"sz":      formatExchangeDecimal(o.Qty),
		"clOrdId": okxClientOrderID(o.ID),
		"tgtCcy":  "base_ccy",
	}
	switch o.Type {
	case "market":
		args["ordType"] = "market"
	case "limit":
		if o.LimitPrice == nil {
			return nil, errors.New("limit order missing limit_price")
		}
		ordType := "limit"
		switch strings.ToLower(o.TIF) {
		case "ioc":
			ordType = "ioc"
		case "fok":
			ordType = "fok"
		}
		args["ordType"] = ordType
		args["px"] = formatExchangeDecimal(*o.LimitPrice)
	default:
		return nil, fmt.Errorf("OKX spot adapter supports market/limit only, got %q", o.Type)
	}
	return args, nil
}

func (okxAdapter) ParseOrder(raw json.RawMessage) (*brokerOrderResult, error) {
	data, err := okxData(raw)
	if err != nil {
		return nil, err
	}
	row := firstJSONRow(data)
	var o struct {
		OrdID     string `json:"ordId"`
		ClOrdID   string `json:"clOrdId"`
		State     string `json:"state"`
		SCode     string `json:"sCode"`
		SMsg      string `json:"sMsg"`
		AccFillSz string `json:"accFillSz"`
		AvgPx     string `json:"avgPx"`
	}
	if err := json.Unmarshal(row, &o); err != nil {
		return nil, fmt.Errorf("decode okx order: %w", err)
	}
	if o.SCode != "" && o.SCode != "0" {
		return nil, fmt.Errorf("okx order error %s: %s", o.SCode, o.SMsg)
	}
	if o.OrdID == "" {
		return nil, fmt.Errorf("okx order response missing ordId: %s", string(raw))
	}
	executed := parseFloat(o.AccFillSz)
	return &brokerOrderResult{BrokerOrderID: o.OrdID, ClientOrderID: o.ClOrdID, BrokerStatus: firstString(o.State, "accepted"), Status: mapOKXStatus(o.State), ExecutedQty: executed, CummulativeQuoteQty: executed * parseFloat(o.AvgPx)}, nil
}

func (okxAdapter) ParseAccount(raw json.RawMessage) (*brokerAccount, error) {
	data, err := okxData(raw)
	if err != nil {
		return nil, err
	}
	var rows []struct {
		Details []struct {
			Ccy      string `json:"ccy"`
			AvailBal string `json:"availBal"`
			CashBal  string `json:"cashBal"`
			Eq       string `json:"eq"`
		} `json:"details"`
	}
	if err := json.Unmarshal(data, &rows); err != nil {
		return nil, fmt.Errorf("decode okx balance: %w", err)
	}
	out := &brokerAccount{Holdings: map[string]brokerBalance{}, HoldingsComplete: true}
	for _, row := range rows {
		for _, d := range row.Details {
			available := parseFloat(firstString(d.AvailBal, d.CashBal, d.Eq))
			total := parseFloat(firstString(d.CashBal, d.Eq, d.AvailBal))
			addCryptoBalance(out, d.Ccy, available, total)
		}
	}
	return out, nil
}

// ─── Bybit ────────────────────────────────────────────────────────

type bybitAdapter struct{}

func (bybitAdapter) Slug() string { return "bybit" }
func (bybitAdapter) Capabilities() brokerCapabilities {
	return brokerCapabilities{AssetClasses: []string{"crypto"}, OrderTypes: []string{"market", "limit", "stop"}, TIFs: []string{"gtc", "ioc", "fok"}, Fractional: true, CancelByClientID: true, QuoteCurrency: "USDT"}
}
func (bybitAdapter) ToolMap() map[string]string {
	return map[string]string{"order.place": "create_order", "order.cancel": "cancel_order", "order.status": "list_orders", "account.summary": "get_wallet_balance"}
}
func (bybitAdapter) ToBrokerSymbol(canonical string) string { return toUSDTCompact(canonical) }
func (bybitAdapter) HoldingsTool() string                   { return "" }
func (bybitAdapter) ParseHoldings(_ json.RawMessage) (map[string]brokerBalance, error) {
	return map[string]brokerBalance{}, nil
}
func (bybitAdapter) OrdersHistoryTool() (string, map[string]any) { return "", nil }
func (bybitAdapter) OpenOrdersTool() (string, map[string]any)    { return "", nil }
func (bybitAdapter) ParseOrders(_ json.RawMessage) ([]brokerHistoricOrder, error) {
	return nil, nil
}
func (bybitAdapter) CancelArgs(o *Order, brokerOrderID string) map[string]any {
	args := map[string]any{"category": "spot", "symbol": toUSDTCompact(o.Symbol)}
	if brokerOrderID != "" {
		args["orderId"] = brokerOrderID
	} else {
		args["orderLinkId"] = o.ID
	}
	return args
}
func (bybitAdapter) StatusArgs(o *Order, brokerOrderID string) map[string]any {
	args := (bybitAdapter{}).CancelArgs(o, brokerOrderID)
	args["openOnly"] = 0
	args["limit"] = 1
	return args
}
func (bybitAdapter) IsUnknownOrderError(code, detail string) bool {
	return strings.Contains(code, "110001") || strings.Contains(strings.ToLower(detail), "order not exists")
}
func (bybitAdapter) ErrText(res *sdk.ExecuteResult, err error) (string, string) {
	return bybitErrText(res, err)
}

func (bybitAdapter) TranslateOrder(o *Order) (map[string]any, error) {
	if err := requireCryptoOrder("Bybit", o); err != nil {
		return nil, err
	}
	args := map[string]any{"category": "spot", "symbol": toUSDTCompact(o.Symbol), "side": titleSide(o.Side), "qty": formatExchangeDecimal(o.Qty), "orderLinkId": o.ID}
	switch o.Type {
	case "market":
		args["orderType"] = "Market"
	case "limit":
		if o.LimitPrice == nil {
			return nil, errors.New("limit order missing limit_price")
		}
		args["orderType"] = "Limit"
		args["price"] = formatExchangeDecimal(*o.LimitPrice)
		args["timeInForce"] = exchangeTIF(o.TIF)
	case "stop":
		if o.StopPrice == nil {
			return nil, errors.New("stop order missing stop_price")
		}
		args["orderType"] = "Market"
		args["triggerPrice"] = formatExchangeDecimal(*o.StopPrice)
		if strings.EqualFold(o.Side, "buy") {
			args["triggerDirection"] = 1
		} else {
			args["triggerDirection"] = 2
		}
	default:
		return nil, fmt.Errorf("unsupported order type %q", o.Type)
	}
	return args, nil
}

func (bybitAdapter) ParseOrder(raw json.RawMessage) (*brokerOrderResult, error) {
	result, err := bybitResult(raw)
	if err != nil {
		return nil, err
	}
	var create struct {
		OrderID     string `json:"orderId"`
		OrderLinkID string `json:"orderLinkId"`
	}
	_ = json.Unmarshal(result, &create)
	if create.OrderID != "" {
		return &brokerOrderResult{BrokerOrderID: create.OrderID, ClientOrderID: create.OrderLinkID, BrokerStatus: "accepted", Status: "working"}, nil
	}
	var list struct {
		List []struct {
			OrderID      string `json:"orderId"`
			OrderLinkID  string `json:"orderLinkId"`
			OrderStatus  string `json:"orderStatus"`
			CumExecQty   string `json:"cumExecQty"`
			CumExecValue string `json:"cumExecValue"`
			AvgPrice     string `json:"avgPrice"`
		} `json:"list"`
	}
	if err := json.Unmarshal(result, &list); err != nil || len(list.List) == 0 {
		return nil, fmt.Errorf("decode bybit order: %w", err)
	}
	o := list.List[0]
	executed := parseFloat(o.CumExecQty)
	cum := parseFloat(o.CumExecValue)
	if cum == 0 && executed > 0 {
		cum = executed * parseFloat(o.AvgPrice)
	}
	return &brokerOrderResult{BrokerOrderID: o.OrderID, ClientOrderID: o.OrderLinkID, BrokerStatus: o.OrderStatus, Status: mapSimpleOrderStatus(o.OrderStatus), ExecutedQty: executed, CummulativeQuoteQty: cum}, nil
}

func (bybitAdapter) ParseAccount(raw json.RawMessage) (*brokerAccount, error) {
	result, err := bybitResult(raw)
	if err != nil {
		return nil, err
	}
	var resp struct {
		List []struct {
			Coin []struct {
				Coin                string `json:"coin"`
				WalletBalance       string `json:"walletBalance"`
				AvailableToWithdraw string `json:"availableToWithdraw"`
				AvailableToBorrow   string `json:"availableToBorrow"`
				Equity              string `json:"equity"`
			} `json:"coin"`
		} `json:"list"`
	}
	if err := json.Unmarshal(result, &resp); err != nil {
		return nil, fmt.Errorf("decode bybit wallet: %w", err)
	}
	out := &brokerAccount{Holdings: map[string]brokerBalance{}, HoldingsComplete: true}
	for _, row := range resp.List {
		for _, c := range row.Coin {
			available := parseFloat(firstString(c.AvailableToWithdraw, c.WalletBalance, c.Equity))
			total := parseFloat(firstString(c.WalletBalance, c.Equity, c.AvailableToWithdraw))
			addCryptoBalance(out, c.Coin, available, total)
		}
	}
	return out, nil
}

// ─── Bitstamp ─────────────────────────────────────────────────────

type bitstampAdapter struct{}

func (bitstampAdapter) Slug() string { return "bitstamp" }
func (bitstampAdapter) Capabilities() brokerCapabilities {
	return brokerCapabilities{AssetClasses: []string{"crypto"}, OrderTypes: []string{"market", "limit"}, TIFs: []string{"day", "gtc", "ioc", "fok"}, Fractional: true, CancelByClientID: true, QuoteCurrency: "USD"}
}
func (bitstampAdapter) ToolMap() map[string]string {
	return map[string]string{"order.cancel": "cancel_order", "order.status": "order_status", "account.summary": "balance"}
}
func (bitstampAdapter) PlaceOrderTool(o *Order) string {
	side := strings.ToLower(o.Side)
	if o.Type == "market" {
		if side == "sell" {
			return "sell_market_order"
		}
		return "buy_market_order"
	}
	if side == "sell" {
		return "sell_limit_order"
	}
	return "buy_limit_order"
}
func (bitstampAdapter) ToBrokerSymbol(canonical string) string {
	return strings.ToLower(toCompactUSD(canonical))
}
func (bitstampAdapter) HoldingsTool() string { return "" }
func (bitstampAdapter) ParseHoldings(_ json.RawMessage) (map[string]brokerBalance, error) {
	return map[string]brokerBalance{}, nil
}
func (bitstampAdapter) OrdersHistoryTool() (string, map[string]any) { return "", nil }
func (bitstampAdapter) OpenOrdersTool() (string, map[string]any)    { return "", nil }
func (bitstampAdapter) ParseOrders(_ json.RawMessage) ([]brokerHistoricOrder, error) {
	return nil, nil
}
func (bitstampAdapter) CancelArgs(o *Order, brokerOrderID string) map[string]any {
	if brokerOrderID != "" {
		return map[string]any{"id": brokerOrderID}
	}
	return map[string]any{"client_order_id": o.ID}
}
func (bitstampAdapter) StatusArgs(o *Order, brokerOrderID string) map[string]any {
	args := (bitstampAdapter{}).CancelArgs(o, brokerOrderID)
	args["omit_transactions"] = false
	return args
}
func (bitstampAdapter) IsUnknownOrderError(code, detail string) bool {
	d := strings.ToLower(detail)
	return strings.Contains(d, "order not found") || strings.Contains(d, "invalid order")
}
func (bitstampAdapter) ErrText(res *sdk.ExecuteResult, err error) (string, string) {
	return jsonErrText("bitstamp", res, err, "status", "reason")
}

func (bitstampAdapter) TranslateOrder(o *Order) (map[string]any, error) {
	if err := requireCryptoOrder("Bitstamp", o); err != nil {
		return nil, err
	}
	if o.Type != "market" && o.Type != "limit" {
		return nil, fmt.Errorf("Bitstamp spot adapter supports market/limit only, got %q", o.Type)
	}
	args := map[string]any{"market_symbol": strings.ToLower(toCompactUSD(o.Symbol)), "amount": formatExchangeDecimal(o.Qty), "client_order_id": o.ID}
	if o.Type == "limit" {
		if o.LimitPrice == nil {
			return nil, errors.New("limit order missing limit_price")
		}
		args["price"] = formatExchangeDecimal(*o.LimitPrice)
		switch strings.ToLower(o.TIF) {
		case "day":
			args["daily_order"] = true
		case "ioc":
			args["ioc_order"] = true
		case "fok":
			args["fok_order"] = true
		}
	}
	return args, nil
}

func (bitstampAdapter) ParseOrder(raw json.RawMessage) (*brokerOrderResult, error) {
	var o struct {
		ID             string `json:"id"`
		OrderID        string `json:"order_id"`
		ClientOrderID  string `json:"client_order_id"`
		Status         string `json:"status"`
		Amount         string `json:"amount"`
		ExecutedAmount string `json:"executed_amount"`
		Price          string `json:"price"`
		Transactions   []struct {
			Amount string `json:"amount"`
			Price  string `json:"price"`
			Fee    string `json:"fee"`
		} `json:"transactions"`
	}
	if err := json.Unmarshal(raw, &o); err != nil {
		return nil, fmt.Errorf("decode bitstamp order: %w", err)
	}
	id := firstString(o.ID, o.OrderID)
	if id == "" {
		return nil, fmt.Errorf("bitstamp order response missing id: %s", string(raw))
	}
	executed := parseFloat(o.ExecutedAmount)
	cum := 0.0
	fills := make([]brokerFill, 0, len(o.Transactions))
	for _, tx := range o.Transactions {
		qty := parseFloat(tx.Amount)
		price := parseFloat(tx.Price)
		cum += qty * price
		if o.ExecutedAmount == "" {
			executed += qty
		}
		fills = append(fills, brokerFill{Qty: qty, Price: price, Commission: parseFloat(tx.Fee), CommissionAsset: "USD"})
	}
	if cum == 0 && executed > 0 {
		cum = executed * parseFloat(o.Price)
	}
	status := firstString(o.Status, "accepted")
	return &brokerOrderResult{BrokerOrderID: id, ClientOrderID: o.ClientOrderID, BrokerStatus: status, Status: mapBitstampStatus(status), ExecutedQty: executed, CummulativeQuoteQty: cum, Fills: fills}, nil
}

func (bitstampAdapter) ParseAccount(raw json.RawMessage) (*brokerAccount, error) {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("decode bitstamp balance: %w", err)
	}
	out := &brokerAccount{Holdings: map[string]brokerBalance{}, HoldingsComplete: true}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !strings.HasSuffix(k, "_available") {
			continue
		}
		asset := strings.ToUpper(strings.TrimSuffix(k, "_available"))
		available := anyFloat(m[k])
		total := anyFloat(m[strings.ToLower(asset)+"_balance"])
		if total <= 0 {
			total = available
		}
		addCryptoBalance(out, asset, available, total)
	}
	return out, nil
}

// ─── Shared helpers ───────────────────────────────────────────────

func requireCryptoOrder(name string, o *Order) error {
	if o == nil {
		return errors.New("nil order")
	}
	if o.AssetClass != "crypto" {
		return fmt.Errorf("%s adapter handles crypto only, got %q", name, o.AssetClass)
	}
	if !strings.EqualFold(o.Side, "buy") && !strings.EqualFold(o.Side, "sell") {
		return fmt.Errorf("%s side must be buy|sell, got %q", name, o.Side)
	}
	return nil
}

func formatExchangeDecimal(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

func firstString(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func toDashUSD(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "/", "-")
	if strings.HasSuffix(s, "USDT") {
		return strings.TrimSuffix(s, "USDT") + "-USD"
	}
	if strings.HasSuffix(s, "USD") && !strings.Contains(s, "-") {
		return strings.TrimSuffix(s, "USD") + "-USD"
	}
	return s
}

func toCompactUSD(s string) string {
	s = toDashUSD(s)
	return strings.ReplaceAll(s, "-", "")
}

func toUSDTCompact(s string) string {
	return strings.TrimSuffix(toCompactUSD(s), "USD") + "USDT"
}

func toUSDTDashed(s string) string {
	return strings.TrimSuffix(toDashUSD(s), "-USD") + "-USDT"
}

func addCryptoBalance(out *brokerAccount, asset string, free, total float64) {
	if total <= 0 {
		total = free
	}
	if total <= 0 {
		return
	}
	asset = strings.ToUpper(strings.TrimSpace(asset))
	switch asset {
	case "USD", "USDT", "USDC":
		out.QuoteCash += total
		out.QuoteAvailable += free
	default:
		canonical := asset + "-USD"
		out.Holdings[canonical] = brokerBalance{Asset: canonical, Free: free, Total: total}
	}
}

func exchangeTIF(tif string) string {
	switch strings.ToLower(tif) {
	case "ioc":
		return "IOC"
	case "fok":
		return "FOK"
	default:
		return "GTC"
	}
}

func titleSide(side string) string {
	if strings.EqualFold(side, "sell") {
		return "Sell"
	}
	return "Buy"
}

func mapSimpleOrderStatus(status string) string {
	switch strings.ToLower(status) {
	case "filled", "done", "closed", "complete", "completed":
		return "filled"
	case "cancelled", "canceled", "expired", "cancelled_by_user":
		return "cancelled"
	case "rejected", "failed", "failure":
		return "rejected"
	default:
		return "working"
	}
}

func mapOKXStatus(status string) string {
	switch strings.ToLower(status) {
	case "filled":
		return "filled"
	case "canceled":
		return "cancelled"
	default:
		return "working"
	}
}

func mapBitstampStatus(status string) string {
	switch strings.ToLower(status) {
	case "finished", "filled":
		return "filled"
	case "canceled", "cancelled", "expired":
		return "cancelled"
	default:
		return "working"
	}
}

func okxClientOrderID(id string) string {
	clean := strings.ReplaceAll(id, "-", "")
	if len(clean) > 32 {
		return clean[:32]
	}
	return clean
}

func okxData(raw json.RawMessage) (json.RawMessage, error) {
	var env struct {
		Code string          `json:"code"`
		Msg  string          `json:"msg"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("decode okx response: %w", err)
	}
	if env.Code != "" && env.Code != "0" {
		return nil, fmt.Errorf("okx error %s: %s", env.Code, env.Msg)
	}
	return env.Data, nil
}

func firstJSONRow(data json.RawMessage) json.RawMessage {
	var rows []json.RawMessage
	if err := json.Unmarshal(data, &rows); err == nil && len(rows) > 0 {
		return rows[0]
	}
	return data
}

func bybitResult(raw json.RawMessage) (json.RawMessage, error) {
	var env struct {
		RetCode int             `json:"retCode"`
		RetMsg  string          `json:"retMsg"`
		Result  json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("decode bybit response: %w", err)
	}
	if env.RetCode != 0 {
		return nil, fmt.Errorf("bybit error %d: %s", env.RetCode, env.RetMsg)
	}
	return env.Result, nil
}

func okxErrText(res *sdk.ExecuteResult, err error) (string, string) {
	if err != nil {
		return "broker_call_failed", err.Error()
	}
	if res == nil {
		return "broker_no_response", "no response from broker"
	}
	var env struct {
		Code string `json:"code"`
		Msg  string `json:"msg"`
		Data []struct {
			SCode string `json:"sCode"`
			SMsg  string `json:"sMsg"`
		} `json:"data"`
	}
	if json.Unmarshal(res.Data, &env) == nil {
		if len(env.Data) > 0 && env.Data[0].SCode != "" && env.Data[0].SCode != "0" {
			return "okx_" + env.Data[0].SCode, env.Data[0].SMsg
		}
		if env.Code != "" && env.Code != "0" {
			return "okx_" + env.Code, env.Msg
		}
	}
	if !res.Success {
		return "broker_non_2xx", string(res.Data)
	}
	return "", ""
}

func bybitErrText(res *sdk.ExecuteResult, err error) (string, string) {
	if err != nil {
		return "broker_call_failed", err.Error()
	}
	if res == nil {
		return "broker_no_response", "no response from broker"
	}
	var env struct {
		RetCode int    `json:"retCode"`
		RetMsg  string `json:"retMsg"`
	}
	if json.Unmarshal(res.Data, &env) == nil && env.RetCode != 0 {
		return fmt.Sprintf("bybit_%d", env.RetCode), env.RetMsg
	}
	if !res.Success {
		return "broker_non_2xx", string(res.Data)
	}
	return "", ""
}

func jsonErrText(prefix string, res *sdk.ExecuteResult, err error, codeField, messageField string) (string, string) {
	if err != nil {
		return "broker_call_failed", err.Error()
	}
	if res == nil {
		return "broker_no_response", "no response from broker"
	}
	var m map[string]any
	if json.Unmarshal(res.Data, &m) == nil {
		code := strings.TrimSpace(fmt.Sprint(m[codeField]))
		msg := strings.TrimSpace(fmt.Sprint(m[messageField]))
		if code != "" && code != "<nil>" && (msg != "" && msg != "<nil>") {
			return prefix + "_" + strings.ReplaceAll(code, " ", "_"), msg
		}
	}
	if !res.Success {
		return "broker_non_2xx", string(res.Data)
	}
	return "", ""
}
