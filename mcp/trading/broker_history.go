package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Normalize spot order snapshots without replaying their fills into broker cash.
// IDs are decoded as numbers without float conversion (exchange IDs exceed 2^53).
func cryptoHistory(slug string, raw json.RawMessage) ([]brokerHistoricOrder, error) {
	var root any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&root); err != nil {
		return nil, err
	}
	var items []any
	obj, _ := root.(map[string]any)
	switch slug {
	case "binance-trading", "bitstamp":
		items, _ = root.([]any)
	case "coinbase":
		items, _ = obj["orders"].([]any)
	case "okx":
		if refString(obj, "code") != "0" {
			return nil, fmt.Errorf("OKX history unsuccessful")
		}
		items, _ = obj["data"].([]any)
	case "bybit":
		if refString(obj, "retCode") != "0" {
			return nil, fmt.Errorf("Bybit history unsuccessful")
		}
		result, _ := obj["result"].(map[string]any)
		items, _ = result["list"].([]any)
	case "kraken":
		if errs, _ := obj["error"].([]any); len(errs) > 0 {
			return nil, fmt.Errorf("Kraken history unsuccessful")
		}
		result, _ := obj["result"].(map[string]any)
		for _, key := range []string{"open", "closed"} {
			rows, ok := result[key].(map[string]any)
			if !ok {
				continue
			}
			items = []any{}
			ids := make([]string, 0, len(rows))
			for id := range rows {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			for _, id := range ids {
				row, ok := rows[id].(map[string]any)
				if !ok {
					return nil, fmt.Errorf("invalid Kraken order")
				}
				row["id"] = id
				items = append(items, row)
			}
		}
	}
	if items == nil {
		return nil, fmt.Errorf("%s order list missing", slug)
	}
	out := make([]brokerHistoricOrder, 0, len(items))
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("invalid %s order row", slug)
		}
		r := brokerHistoricOrder{AssetClass: "crypto", TIF: "gtc"}
		switch slug {
		case "binance-trading":
			symbol := refString(m, "symbol")
			if !strings.HasSuffix(symbol, "USDT") {
				continue
			}
			r.Symbol = fromBinanceSymbol(symbol)
			r.BrokerOrderID = refString(m, "orderId")
			r.ClientOrderID = refString(m, "clientOrderId")
			r.Side = strings.ToLower(refString(m, "side"))
			r.Type = strings.ToLower(refString(m, "type"))
			r.TIF = strings.ToLower(refString(m, "timeInForce"))
			r.Qty = refFloat(m, "origQty")
			r.FilledQty = refFloat(m, "executedQty")
			if r.FilledQty > 0 {
				r.AvgFillPrice = refFloat(m, "cummulativeQuoteQty") / r.FilledQty
			}
			r.LimitPrice = refFloat(m, "price")
			r.StopPrice = refFloat(m, "stopPrice")
			r.BrokerStatus = refString(m, "status")
			r.Status = mapBinanceStatus(r.BrokerStatus)
			r.PlacedAt = historyTime(refString(m, "time", "transactTime"), true)
			r.ResolvedAt = historyTime(refString(m, "updateTime"), true)
		case "kraken":
			d, _ := m["descr"].(map[string]any)
			r.Symbol = fromKrakenSymbol(refString(d, "pair"))
			if !strings.HasSuffix(r.Symbol, "-USD") {
				continue
			}
			r.BrokerOrderID = refString(m, "id")
			r.Side = refString(d, "type")
			r.Type = refString(d, "ordertype")
			r.Qty = refFloat(m, "vol")
			r.FilledQty = refFloat(m, "vol_exec")
			r.AvgFillPrice = refFloat(m, "price")
			r.LimitPrice = refFloat(d, "price")
			r.StopPrice = refFloat(d, "price2")
			r.BrokerStatus = refString(m, "status")
			r.Status = mapKrakenStatus(r.BrokerStatus, r.Qty, r.FilledQty)
			r.PlacedAt = historyTime(refString(m, "opentm"), false)
			r.ResolvedAt = historyTime(refString(m, "closetm"), false)
		case "coinbase":
			r.Symbol = refString(m, "product_id")
			if !strings.HasSuffix(r.Symbol, "-USD") {
				continue
			}
			r.BrokerOrderID = refString(m, "order_id")
			r.ClientOrderID = refString(m, "client_order_id")
			r.Side = strings.ToLower(refString(m, "side"))
			r.Type = strings.ToLower(refString(m, "order_type"))
			r.BrokerStatus = refString(m, "status")
			r.Status = mapSimpleOrderStatus(r.BrokerStatus)
			r.FilledQty = refFloat(m, "filled_size")
			r.AvgFillPrice = refFloat(m, "average_filled_price")
			r.PlacedAt = refString(m, "created_time")
			r.ResolvedAt = refString(m, "last_fill_time")
			cfg, _ := m["order_configuration"].(map[string]any)
			for key, value := range cfg {
				v, _ := value.(map[string]any)
				r.Qty = refFloat(v, "base_size")
				r.LimitPrice = refFloat(v, "limit_price")
				r.StopPrice = refFloat(v, "stop_price")
				parts := strings.Split(key, "_")
				r.TIF = parts[len(parts)-1]
			}
			// Quote-sized market orders do not have a base quantity until execution.
			if r.Qty == 0 && r.Status != "working" {
				r.Qty = r.FilledQty
			}
			if r.Qty == 0 {
				continue
			}
		case "okx":
			symbol := refString(m, "instId")
			if !strings.HasSuffix(symbol, "-USDT") || strings.Count(symbol, "-") != 1 {
				continue
			}
			r.Symbol = strings.TrimSuffix(symbol, "-USDT") + "-USD"
			r.BrokerOrderID = refString(m, "ordId")
			r.ClientOrderID = refString(m, "clOrdId")
			r.Side = refString(m, "side")
			r.Type = refString(m, "ordType")
			r.Qty = refFloat(m, "sz")
			r.FilledQty = refFloat(m, "accFillSz")
			r.AvgFillPrice = refFloat(m, "avgPx")
			r.LimitPrice = refFloat(m, "px")
			r.BrokerStatus = refString(m, "state")
			r.Status = mapOKXStatus(r.BrokerStatus)
			if r.Type == "ioc" || r.Type == "fok" {
				r.TIF = r.Type
				r.Type = "limit"
			}
			if r.Type == "post_only" {
				r.Type = "limit"
			}
			r.PlacedAt = historyTime(refString(m, "cTime"), true)
			r.ResolvedAt = historyTime(refString(m, "uTime"), true)
		case "bybit":
			symbol := refString(m, "symbol")
			if !strings.HasSuffix(symbol, "USDT") {
				continue
			}
			r.Symbol = strings.TrimSuffix(symbol, "USDT") + "-USD"
			r.BrokerOrderID = refString(m, "orderId")
			r.ClientOrderID = refString(m, "orderLinkId")
			r.Side = strings.ToLower(refString(m, "side"))
			r.Type = strings.ToLower(refString(m, "orderType"))
			r.TIF = strings.ToLower(refString(m, "timeInForce"))
			r.Qty = refFloat(m, "qty")
			r.FilledQty = refFloat(m, "cumExecQty")
			r.AvgFillPrice = refFloat(m, "avgPrice")
			r.LimitPrice = refFloat(m, "price")
			r.StopPrice = refFloat(m, "triggerPrice")
			r.BrokerStatus = refString(m, "orderStatus")
			r.Status = mapSimpleOrderStatus(r.BrokerStatus)
			r.PlacedAt = historyTime(refString(m, "createdTime"), true)
			r.ResolvedAt = historyTime(refString(m, "updatedTime"), true)
		case "bitstamp":
			symbol := strings.ToUpper(strings.ReplaceAll(refString(m, "currency_pair", "market"), "/", ""))
			if !strings.HasSuffix(symbol, "USD") {
				continue
			}
			r.Symbol = strings.TrimSuffix(symbol, "USD") + "-USD"
			r.BrokerOrderID = refString(m, "id")
			r.ClientOrderID = refString(m, "client_order_id")
			r.Side = "buy"
			if refString(m, "type") == "1" {
				r.Side = "sell"
			}
			r.Type = "limit"
			r.Qty = refFloat(m, "amount")
			r.LimitPrice = refFloat(m, "price")
			r.Status = "working"
			r.BrokerStatus = "open"
			r.PlacedAt = refString(m, "datetime")
		}
		if r.Status == "working" {
			r.ResolvedAt = ""
		}
		if r.BrokerOrderID == "" || r.Symbol == "" || !oneOfString(r.Side, "buy", "sell") || !finite(r.Qty) || r.Qty <= 0 || !finite(r.FilledQty) || r.FilledQty < 0 || r.FilledQty > r.Qty+1e-9 || !finite(r.AvgFillPrice) || r.AvgFillPrice < 0 {
			return nil, fmt.Errorf("invalid %s historic order", slug)
		}
		out = append(out, r)
	}
	return out, nil
}

func historyTime(s string, millis bool) string {
	if s == "" {
		return ""
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v <= 0 {
		return ""
	}
	if millis {
		v /= 1000
	}
	return time.Unix(int64(v), int64((v-float64(int64(v)))*1e9)).UTC().Format(time.RFC3339Nano)
}

// Walk bounded provider pages. A partial/failed response is returned as an
// error, never reported as a complete empty account history.
func fetchBrokerHistory(ctx *sdk.AppCtx, bb *boundBroker, tool string, initial map[string]any) ([]brokerHistoricOrder, error) {
	args := map[string]any{}
	for k, v := range initial {
		args[k] = v
	}
	var out []brokerHistoricOrder
	seen := map[string]bool{}
	for page := 0; page < 100; page++ {
		res, err := ctx.PlatformAPI().ExecuteIntegrationTool(bb.ConnectionID, tool, args)
		if err != nil {
			return nil, err
		}
		if res == nil || !res.Success {
			return nil, fmt.Errorf("%s history request failed", bb.Adapter.Slug())
		}
		rows, err := bb.Adapter.ParseOrders(res.Data)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			if bb.Adapter.Slug() == "bitstamp" {
				// open_orders reports remaining amount. Capture the executed baseline
				// before supervising the order so old fills are never applied twice.
				status, err := ctx.PlatformAPI().ExecuteIntegrationTool(bb.ConnectionID, bb.toolFor("order.status"), bb.Adapter.StatusArgs(&Order{Symbol: r.Symbol}, r.BrokerOrderID))
				if err != nil {
					return nil, err
				}
				if status == nil || !status.Success {
					return nil, fmt.Errorf("Bitstamp open-order baseline unavailable")
				}
				progress, err := bb.Adapter.ParseOrder(status.Data)
				if err != nil {
					return nil, err
				}
				if progress.BrokerOrderID != r.BrokerOrderID {
					return nil, fmt.Errorf("Bitstamp status returned a different order")
				}
				// If it filled during the snapshot, defer to the next account snapshot.
				if progress.Status != "working" {
					continue
				}
				r.FilledQty = progress.ExecutedQty
				r.Qty += r.FilledQty
				if r.FilledQty > 0 {
					r.AvgFillPrice = progress.CummulativeQuoteQty / r.FilledQty
				}
			}
			if !seen[r.BrokerOrderID] {
				out = append(out, r)
				seen[r.BrokerOrderID] = true
			}
		}
		var root map[string]json.RawMessage
		_ = json.Unmarshal(res.Data, &root)
		var cursor string
		switch bb.Adapter.Slug() {
		case "coinbase":
			var more bool
			_ = json.Unmarshal(root["has_next"], &more)
			_ = json.Unmarshal(root["cursor"], &cursor)
			if !more {
				return out, nil
			}
		case "bybit":
			var result struct {
				Cursor string `json:"nextPageCursor"`
			}
			_ = json.Unmarshal(root["result"], &result)
			cursor = result.Cursor
			if cursor == "" {
				return out, nil
			}
		case "kraken":
			if tool != "get_closed_orders" {
				return out, nil
			}
			var result struct {
				Count  int                        `json:"count"`
				Closed map[string]json.RawMessage `json:"closed"`
			}
			_ = json.Unmarshal(root["result"], &result)
			offset := int(anyFloat(args["ofs"])) + len(result.Closed)
			if offset >= result.Count {
				return out, nil
			}
			if len(result.Closed) == 0 {
				return nil, fmt.Errorf("Kraken history page made no progress")
			}
			args["ofs"] = offset
			continue
		case "okx":
			var data []struct {
				ID string `json:"ordId"`
			}
			_ = json.Unmarshal(root["data"], &data)
			if len(data) < 100 {
				return out, nil
			}
			cursor = data[len(data)-1].ID
			if cursor == "" || args["after"] == cursor {
				return nil, fmt.Errorf("OKX history cursor did not advance")
			}
			args["after"] = cursor
			continue
		case "binance-trading":
			if tool != "get_all_orders" {
				return out, nil
			}
			var data []struct {
				ID json.Number `json:"orderId"`
			}
			_ = json.Unmarshal(res.Data, &data)
			limit := int(anyFloat(args["limit"]))
			if limit == 0 {
				limit = 500
			}
			if len(data) < limit {
				return out, nil
			}
			last, err := data[len(data)-1].ID.Int64()
			if err != nil || last == int64(^uint64(0)>>1) {
				return nil, fmt.Errorf("invalid Binance history cursor")
			}
			next := strconv.FormatInt(last+1, 10)
			if fmt.Sprint(args["orderId"]) == next {
				return nil, fmt.Errorf("Binance history cursor did not advance")
			}
			args["orderId"] = json.Number(next)
			continue
		default:
			return out, nil
		}
		if cursor == "" || args["cursor"] == cursor {
			return nil, fmt.Errorf("history cursor did not advance")
		}
		args["cursor"] = cursor
	}
	return nil, fmt.Errorf("history exceeded 100 pages; narrow the requested range")
}

func bybitHistoricalStatus(ctx *sdk.AppCtx, bb *boundBroker, o *Order, brokerID string) (*brokerOrderResult, error) {
	args := (bybitAdapter{}).CancelArgs(o, brokerID)
	args["limit"] = 1
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(bb.ConnectionID, "list_order_history", args)
	if err != nil {
		return nil, err
	}
	if res == nil || !res.Success {
		return nil, fmt.Errorf("Bybit historical status unavailable")
	}
	return bb.Adapter.ParseOrder(res.Data)
}

// Keep the order, cumulative fill baseline, and broker identity atomic. Imported
// executions are history, so do not debit cash already reflected by the account.
func insertImportedBrokerOrder(db *sql.DB, project string, portfolio int64, id, rationale, source string, bb *boundBroker, r brokerHistoricOrder) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := dbInsertBackfilledOrder(tx, project, portfolio, id, r.Symbol, r.AssetClass, r.Side, r.Type, r.Qty, r.FilledQty, r.AvgFillPrice, r.LimitPrice, r.StopPrice, r.TIF, r.Status, rationale, source, r.PlacedAt, r.ResolvedAt); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE orders SET broker_order_id=? WHERE id=?`, r.BrokerOrderID, id); err != nil {
		return err
	}
	if r.FilledQty > 0 && r.AvgFillPrice > 0 {
		at := firstString(r.ResolvedAt, r.PlacedAt)
		if _, err := tx.Exec(`INSERT INTO fills(project_id,order_id,portfolio_id,qty,price,fee,fee_source,filled_at) VALUES(?,?,?,?,?,0,'unknown',COALESCE(NULLIF(?,''),CURRENT_TIMESTAMP))`, project, id, portfolio, r.FilledQty, r.AvgFillPrice, at); err != nil {
			return err
		}
	}
	if err := dbInsertJournalTx(tx, project, portfolio, "rationale", rationale, map[string]any{"order_id": id, "symbol": r.Symbol, "side": r.Side, "qty": r.Qty, "type": r.Type, "broker_slug": bb.Adapter.Slug(), "broker_connection_id": bb.ConnectionID, "broker_order_id": r.BrokerOrderID, "client_order_id": r.ClientOrderID, "source": source, "backfill_status": r.BrokerStatus}); err != nil {
		return err
	}
	return tx.Commit()
}
