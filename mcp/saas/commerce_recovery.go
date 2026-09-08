package main

import (
	"encoding/json"
	"fmt"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func commerceBillingRequest(data map[string]any) map[string]any {
	out := map[string]any{}
	for _, key := range []string{"subscription_id", "cycle_id", "period_start", "period_end", "currency", "checkout_id", "success_url", "cancel_url"} {
		if value, ok := data[key]; ok {
			out[key] = value
		}
	}
	out["metadata"] = map[string]any{"collection_method": strArg(mapFromAny(data["metadata"]), "collection_method")}
	return out
}

func (a *App) reconcilePendingInvoices(ctx *sdk.AppCtx, pid string) error {
	operations, err := dbCommerceOperationsForReconciliation(ctx.AppDB(), pid, time.Now().UTC().Add(-5*time.Minute), 20)
	if err != nil {
		return err
	}
	var first error
	for _, op := range operations {
		// Reserve the next poll before doing I/O so unavailable invoices cannot
		// monopolize the batch, including across process restarts.
		res, err := ctx.AppDB().Exec(`UPDATE saas_commerce_operations SET next_recovery_at=datetime('now','+5 minutes')
			WHERE project_id=? AND id=? AND status<>'paid'
			AND (next_recovery_at IS NULL OR next_recovery_at<=CURRENT_TIMESTAMP)
			AND (lease_until IS NULL OR datetime(lease_until)<=CURRENT_TIMESTAMP)`, pid, op.ID)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			continue
		}
		if err := a.recoverCommerceOperation(ctx, pid, op); err != nil {
			ctx.Logger().Warn("recover SaaS commerce operation", "operation_id", op.ID, "err", err)
			if first == nil {
				first = err
			}
		}
	}
	return first
}

func (a *App) recoverCommerceOperation(ctx *sdk.AppCtx, pid string, op *CommerceOperation) error {
	invoiceID := int64PtrValue(op.InvoiceID)
	if invoiceID != 0 {
		projection, err := a.syncBillingInvoiceProjection(ctx, pid, op)
		if err != nil {
			return err
		}
		switch projection.Status {
		case "paid":
			return a.handleInvoicePaid(ctx, sdk.Event{Event: "invoice.paid", ProjectID: pid, Data: map[string]any{"id": invoiceID}})
		case "void", "voided", "uncollectible":
			return a.handleInvoiceCollectionFailed(ctx, sdk.Event{Event: "invoice." + projection.Status, ProjectID: pid, Data: map[string]any{"id": invoiceID}})
		}
	}
	if op.Status != "pending" && op.Status != "failed_billing" && op.Status != "processing_billing" {
		return nil // Collection retries belong to Billing; do not charge again.
	}
	request, err := a.recoverBillingRequest(ctx, pid, op)
	if err != nil {
		return err
	}
	return a.handleSubscriptionCycleDue(ctx, sdk.Event{Event: "subscription.cycle_due", ProjectID: pid, Data: request})
}

func (a *App) recoverBillingRequest(ctx *sdk.AppCtx, pid string, op *CommerceOperation) (map[string]any, error) {
	var raw string
	if err := ctx.AppDB().QueryRow(`SELECT billing_request_json FROM saas_commerce_operations WHERE project_id=? AND id=?`, pid, op.ID).Scan(&raw); err != nil {
		return nil, err
	}
	request := mapFromAny(json.RawMessage(raw))
	if request == nil {
		request = map[string]any{}
	}
	request["subscription_id"], request["cycle_id"] = op.SubscriptionID, op.CycleID
	prepared := mapFromAny(op.Prepared)
	for _, key := range []string{"period_start", "period_end", "currency"} {
		if strArg(request, key) == "" {
			request[key] = prepared[key]
		}
	}
	// Operations created before this migration may have no prepared invoice.
	// Read the original cycle, never fabricate a new billing period on retry.
	if strArg(request, "period_start") == "" || strArg(request, "period_end") == "" {
		found := false
		var previousLastID int64
		for offset := 0; ; offset += 200 {
			var out map[string]any
			if err := ctx.PlatformAPI().CallAppResult("subscriptions", "subscription_cycles_list", map[string]any{
				"_project_id": pid, "subscription_id": op.SubscriptionID, "limit": 200, "offset": offset,
			}, &out); err != nil {
				return nil, err
			}
			cycles := sliceFromAny(out["cycles"])
			for _, item := range cycles {
				cycle := mapFromAny(item)
				if int64Arg(cycle, "id") == op.CycleID {
					for _, key := range []string{"period_start", "period_end", "currency"} {
						request[key] = cycle[key]
					}
					found = true
					break
				}
			}
			if found || len(cycles) < 200 {
				break
			}
			// Older peers may ignore offset. Do not poll the same page forever.
			lastID := int64Arg(mapFromAny(cycles[len(cycles)-1]), "id")
			if lastID == 0 || lastID == previousLastID {
				break
			}
			previousLastID = lastID
		}
	}
	if strArg(request, "period_start") == "" || strArg(request, "period_end") == "" {
		return nil, fmt.Errorf("billing operation %d has no recoverable cycle period", op.ID)
	}
	if raw == "{}" && op.CheckoutID != "" {
		checkout, err := dbCheckoutGet(ctx.AppDB(), pid, op.CheckoutID)
		if err != nil {
			return nil, err
		}
		if checkout != nil && checkout.TrialEndsAt == "" {
			request["checkout_id"] = checkout.ID
		}
	}
	return request, nil
}
