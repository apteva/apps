package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

func (a *App) toolReturnsUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	ret, err := dbReturnGet(ctx.AppDB(), pid, int64Arg(args, "id"))
	if err != nil || ret == nil {
		return nil, firstReturnErr(err, errors.New("return not found"))
	}
	target := strings.ToLower(strings.TrimSpace(strArg(args, "status")))
	if !returnTransitionAllowed(ret.Status, target) {
		return nil, fmt.Errorf("return cannot move from %s to %s", ret.Status, target)
	}
	sets := []string{"status=?", "updated_at=CURRENT_TIMESTAMP"}
	values := []any{target}
	if providerID := strArg(args, "provider_return_id"); providerID != "" {
		sets = append(sets, "provider_return_id=?")
		values = append(values, providerID)
	}
	if payload, ok := args["response_payload"]; ok {
		sets = append(sets, "response_payload=?")
		values = append(values, jsonOrEmpty(payload, "{}"))
	}
	if target == "received" {
		sets = append(sets, "received_at=COALESCE(received_at,CURRENT_TIMESTAMP)")
	}
	values = append(values, pid, ret.ID)
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(
		`UPDATE returns SET `+strings.Join(sets, ", ")+` WHERE project_id=? AND id=?`,
		values...); err != nil {
		return nil, err
	}
	orderStatus := "return_" + target
	if target == "cancelled" || target == "rejected" {
		orderStatus = orderStatusAfterReturnClosed(ret.OrderID, pid, tx)
	}
	if _, err := tx.Exec(
		`UPDATE orders SET order_status=?, updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND id=?`,
		orderStatus, pid, ret.OrderID); err != nil {
		return nil, err
	}
	if err := writeEventTx(tx, pid, ret.OrderID, firstNonEmpty(strArg(args, "actor"), "system"),
		"return."+target, map[string]any{"return_id": ret.ID, "note": strArg(args, "note")}); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	ret, err = dbReturnGet(ctx.AppDB(), pid, ret.ID)
	if err == nil {
		emitOrder(ctx, "return.updated", &Order{ID: ret.OrderID, ProjectID: pid})
	}
	return map[string]any{"return": ret}, err
}

func orderStatusAfterReturnClosed(orderID int64, pid string, tx *sql.Tx) string {
	var fulfillmentStatus string
	if err := tx.QueryRow(
		`SELECT fulfillment_status FROM orders WHERE project_id=? AND id=?`,
		pid, orderID).Scan(&fulfillmentStatus); err != nil {
		return "paid"
	}
	switch fulfillmentStatus {
	case "delivered":
		return "delivered"
	case "fulfilled":
		return "fulfilled"
	case "shipped", "partially_fulfilled":
		return "partially_fulfilled"
	case "picking", "packed", "submitted", "accepted":
		return "fulfilling"
	default:
		return "paid"
	}
}

func (a *App) toolReturnsComplete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	ret, err := dbReturnGet(ctx.AppDB(), pid, int64Arg(args, "id"))
	if err != nil || ret == nil {
		return nil, firstReturnErr(err, errors.New("return not found"))
	}
	if ret.Status == "completed" {
		order, _ := dbOrderGet(ctx.AppDB(), pid, ret.OrderID, true)
		return map[string]any{"return": ret, "order": order}, nil
	}
	if ret.Status != "received" {
		return nil, errors.New("return must be received before completion")
	}
	order, err := dbOrderGet(ctx.AppDB(), pid, ret.OrderID, true)
	if err != nil || order == nil {
		return nil, firstReturnErr(err, errors.New("order not found"))
	}
	refundAmount := int64Arg(args, "refund_amount_cents")
	if refundAmount == 0 {
		refundAmount = returnMerchandiseAmount(order.Items, ret.Items)
	}
	if refundAmount < 0 || refundAmount > order.TotalCents {
		return nil, errors.New("refund_amount_cents must be between zero and the order total")
	}
	var refundRequestID int64
	if boolArg(args, "refund") {
		if order.InvoiceID == nil || *order.InvoiceID == 0 {
			return nil, errors.New("order has no Billing invoice to refund")
		}
		if refundAmount == 0 {
			return nil, errors.New("refund amount is zero")
		}
		var response map[string]any
		if err := ctx.PlatformAPI().CallAppResult("billing", "invoices_refund", map[string]any{
			"_project_id": pid, "invoice_id": *order.InvoiceID, "amount_cents": refundAmount,
			"reason":          firstNonEmpty(strArg(args, "refund_reason"), "requested_by_customer"),
			"idempotency_key": fmt.Sprintf("orders-return-%s-%d", pid, ret.ID),
		}, &response); err != nil {
			return nil, fmt.Errorf("request Billing refund: %w", err)
		}
		if raw, ok := response["refund"].(map[string]any); ok {
			refundRequestID = int64Arg(raw, "id")
		}
	}
	if boolArg(args, "restock") {
		locationID := firstNonZero(int64Arg(args, "restock_location_id"), ptrInt64Value(ret.RestockLocationID))
		if locationID == 0 {
			return nil, errors.New("restock_location_id required when restock=true")
		}
		for _, item := range ret.Items {
			if item.RestockedAt != "" || item.InventoryItemID == nil || *item.InventoryItemID == 0 {
				continue
			}
			var response map[string]any
			if err := ctx.PlatformAPI().CallAppResult("inventory", "inventory_receive", map[string]any{
				"_project_id": pid, "item_id": *item.InventoryItemID, "location_id": locationID,
				"quantity": item.Quantity, "reason": "customer return",
				"reference_app": "orders", "reference_type": "return", "reference_id": fmt.Sprintf("%d", ret.ID),
				"metadata": map[string]any{"order_id": order.ID, "order_item_id": item.OrderItemID},
			}, &response); err != nil {
				_ = dbReturnProcessingError(ctx.AppDB(), pid, ret.ID, err.Error())
				return nil, fmt.Errorf("restock return item %d: %w", item.OrderItemID, err)
			}
			if _, err := ctx.AppDB().Exec(
				`UPDATE return_items SET restocked_at=CURRENT_TIMESTAMP
				 WHERE return_id=? AND order_item_id=? AND restocked_at IS NULL`,
				ret.ID, item.OrderItemID); err != nil {
				return nil, err
			}
		}
	}
	exchangeOrderID := int64Arg(args, "exchange_order_id")
	fullyReturned, err := orderFullyReturned(ctx.AppDB(), pid, order.ID)
	if err != nil {
		return nil, err
	}
	orderStatus := "partially_returned"
	fulfillmentStatus := order.FulfillmentStatus
	if fullyReturned {
		orderStatus = "returned"
		fulfillmentStatus = "returned"
	}
	paymentStatus := order.PaymentStatus
	if boolArg(args, "refund") {
		paymentStatus = "refund_pending"
	}
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(
		`UPDATE returns
		    SET status='completed', refund_request_id=?, refund_amount_cents=?,
		        exchange_order_id=?, processing_error='', completed_at=CURRENT_TIMESTAMP,
		        updated_at=CURRENT_TIMESTAMP
		  WHERE project_id=? AND id=? AND status='received'`,
		nullableInt64(refundRequestID), refundAmount, nullableInt64(exchangeOrderID), pid, ret.ID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(
		`UPDATE orders SET order_status=?, payment_status=?, fulfillment_status=?, updated_at=CURRENT_TIMESTAMP
		 WHERE project_id=? AND id=?`,
		orderStatus, paymentStatus, fulfillmentStatus, pid, order.ID); err != nil {
		return nil, err
	}
	if err := writeEventTx(tx, pid, order.ID, firstNonEmpty(strArg(args, "actor"), "system"),
		"return.completed", map[string]any{
			"return_id": ret.ID, "refund_amount_cents": refundAmount,
			"refund_request_id": refundRequestID, "exchange_order_id": exchangeOrderID,
		}); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	ret, err = dbReturnGet(ctx.AppDB(), pid, ret.ID)
	if err != nil {
		return nil, err
	}
	order, err = dbOrderGet(ctx.AppDB(), pid, order.ID, true)
	if err == nil {
		emitOrder(ctx, "return.completed", order)
	}
	return map[string]any{"return": ret, "order": order}, err
}

func normalizeReturnItems(db *sql.DB, order *Order, raw []any) ([]*ReturnItem, error) {
	if order == nil {
		return nil, errors.New("order required")
	}
	items := order.Items
	if len(items) == 0 {
		var err error
		items, err = dbOrderItemsList(db, order.ID)
		if err != nil {
			return nil, err
		}
	}
	byID := map[int64]*OrderItem{}
	for _, item := range items {
		byID[item.ID] = item
	}
	if len(raw) == 0 {
		for _, item := range items {
			raw = append(raw, map[string]any{"order_item_id": item.ID, "quantity": item.Quantity})
		}
	}
	var out []*ReturnItem
	for index, value := range raw {
		input, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("items[%d] must be an object", index)
		}
		orderItem := byID[int64Arg(input, "order_item_id")]
		if orderItem == nil {
			return nil, fmt.Errorf("items[%d] references an order item outside this order", index)
		}
		quantity := float64Arg(input, "quantity", orderItem.Quantity)
		if quantity <= 0 || quantity > orderItem.Quantity+1e-9 {
			return nil, fmt.Errorf("items[%d].quantity exceeds the ordered quantity", index)
		}
		var inventoryItemID *int64
		if value := int64Arg(input, "inventory_item_id"); value != 0 {
			inventoryItemID = &value
		} else {
			var metadata map[string]any
			_ = json.Unmarshal(orderItem.Metadata, &metadata)
			if value := int64Arg(metadata, "inventory_item_id"); value != 0 {
				inventoryItemID = &value
			}
		}
		var already float64
		if err := db.QueryRow(
			`SELECT COALESCE(SUM(ri.quantity),0)
			   FROM return_items ri JOIN returns r ON r.id=ri.return_id
			  WHERE r.project_id=? AND r.order_id=? AND ri.order_item_id=?
			    AND r.status NOT IN ('rejected','cancelled')`,
			order.ProjectID, order.ID, orderItem.ID).Scan(&already); err != nil {
			return nil, err
		}
		if already+quantity > orderItem.Quantity+1e-9 {
			return nil, fmt.Errorf("items[%d] exceeds the remaining returnable quantity", index)
		}
		out = append(out, &ReturnItem{
			OrderItemID: orderItem.ID, Quantity: quantity, InventoryItemID: inventoryItemID,
		})
	}
	return out, nil
}

func dbReturnItems(db *sql.DB, returnID int64) ([]*ReturnItem, error) {
	rows, err := db.Query(
		`SELECT return_id, order_item_id, quantity, inventory_item_id, restocked_at
		 FROM return_items WHERE return_id=? ORDER BY order_item_id`, returnID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*ReturnItem
	for rows.Next() {
		var item ReturnItem
		var inventoryItemID sql.NullInt64
		var restockedAt sql.NullString
		if err := rows.Scan(&item.ReturnID, &item.OrderItemID, &item.Quantity, &inventoryItemID, &restockedAt); err != nil {
			return nil, err
		}
		item.InventoryItemID = ptrIfValid(inventoryItemID)
		item.RestockedAt = restockedAt.String
		out = append(out, &item)
	}
	return out, rows.Err()
}

func returnTransitionAllowed(current, target string) bool {
	allowed := map[string]map[string]bool{
		"requested":  {"approved": true, "rejected": true, "cancelled": true},
		"approved":   {"in_transit": true, "received": true, "cancelled": true},
		"in_transit": {"received": true},
	}
	return allowed[current][target]
}

func returnMerchandiseAmount(orderItems []*OrderItem, returnItems []*ReturnItem) int64 {
	byID := map[int64]*OrderItem{}
	for _, item := range orderItems {
		byID[item.ID] = item
	}
	var amount int64
	for _, item := range returnItems {
		if orderItem := byID[item.OrderItemID]; orderItem != nil {
			amount += int64(math.Round(float64(orderItem.UnitAmountCents) * item.Quantity))
		}
	}
	return amount
}

func orderFullyReturned(db *sql.DB, pid string, orderID int64) (bool, error) {
	var ordered, returned float64
	if err := db.QueryRow(`SELECT COALESCE(SUM(quantity),0) FROM order_items WHERE order_id=?`, orderID).Scan(&ordered); err != nil {
		return false, err
	}
	if err := db.QueryRow(
		`SELECT COALESCE(SUM(ri.quantity),0)
		   FROM return_items ri JOIN returns r ON r.id=ri.return_id
		  WHERE r.project_id=? AND r.order_id=? AND r.status IN ('received','completed')`,
		pid, orderID).Scan(&returned); err != nil {
		return false, err
	}
	return returned+1e-9 >= ordered, nil
}

func dbReturnProcessingError(db *sql.DB, pid string, returnID int64, message string) error {
	_, err := db.Exec(
		`UPDATE returns SET processing_error=?, updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND id=?`,
		message, pid, returnID)
	return err
}

func ptrInt64Value(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

func firstReturnErr(primary, fallback error) error {
	if primary != nil {
		return primary
	}
	return fallback
}
