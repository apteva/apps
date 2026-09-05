package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

func queueInvoiceEventTx(tx *sql.Tx, id int64, topic, key string, paymentID int64) error {
	var inv Invoice
	var meta string
	var accounting, due, paid, finalized, number sql.NullString
	err := tx.QueryRow(`SELECT project_id,customer_id,status,currency,total_cents,amount_paid_cents,metadata,accounting_date,due_date,paid_at,finalized_at,number,created_at,provider FROM invoices WHERE id=?`, id).Scan(&inv.ProjectID, &inv.CustomerID, &inv.Status, &inv.Currency, &inv.TotalCents, &inv.AmountPaidCents, &meta, &accounting, &due, &paid, &finalized, &number, &inv.CreatedAt, &inv.Provider)
	if err != nil {
		return err
	}
	payload := map[string]any{"event_id": key, "id": id, "customer_id": inv.CustomerID, "status": inv.Status, "currency": inv.Currency, "total_cents": inv.TotalCents, "amount_paid_cents": inv.AmountPaidCents, "metadata": json.RawMessage(meta), "accounting_date": accounting.String, "due_date": due.String, "paid_at": paid.String, "finalized_at": finalized.String, "number": number.String, "created_at": inv.CreatedAt, "provider": inv.Provider}
	if paymentID != 0 {
		var p Payment
		if err := tx.QueryRow("SELECT id,amount_cents,method,received_at FROM payments WHERE id=?", paymentID).Scan(&p.ID, &p.AmountCents, &p.Method, &p.ReceivedAt); err != nil {
			return err
		}
		payload["payment_id"] = p.ID
		payload["payment_amount_cents"] = p.AmountCents
		payload["payment_method"] = p.Method
		payload["received_at"] = p.ReceivedAt
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = tx.Exec("INSERT OR IGNORE INTO billing_outbox(id,project_id,topic,payload) VALUES(?,?,?,?)", key, inv.ProjectID, topic, string(raw))
	return err
}
func flushOutbox(ctx *sdk.AppCtx) error {
	unlock := operationLock(fmt.Sprintf("outbox:%p", ctx.AppDB()))
	defer unlock()
	rows, err := ctx.AppDB().Query("SELECT id,project_id,topic,payload FROM billing_outbox WHERE delivered_at IS NULL ORDER BY rowid LIMIT 100")
	if err != nil {
		return err
	}
	type event struct{ id, pid, topic, raw string }
	var events []event
	for rows.Next() {
		var ev event
		if err = rows.Scan(&ev.id, &ev.pid, &ev.topic, &ev.raw); err != nil {
			rows.Close()
			return err
		}
		events = append(events, ev)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, ev := range events {
		var payload map[string]any
		if err = json.Unmarshal([]byte(ev.raw), &payload); err != nil {
			return err
		}
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		emitter, ok := any(ctx).(interface {
			EmitWithProjectAck(context.Context, string, string, any) error
		})
		if ok {
			err = emitter.EmitWithProjectAck(c, ev.topic, ev.pid, payload)
		} else {
			err = emitGatewayAck(c, ev.topic, ev.pid, payload)
		}
		cancel()
		if err != nil {
			return err
		}
		if _, err = ctx.AppDB().Exec("UPDATE billing_outbox SET delivered_at=? WHERE id=?", nowRFC3339(), ev.id); err != nil {
			return err
		}
	}
	return nil
}
func billingWorkers(a *App) []sdk.Worker {
	return []sdk.Worker{{Name: "billing-reconcile", Schedule: "@every 30s", Run: func(c context.Context, ctx *sdk.AppCtx) error {
		if err := a.recoverUnsentClaims(c, ctx); err != nil {
			ctx.Logger().Warn("unsent billing recovery", "error", err.Error())
		}
		if err := a.recoverLegacyPayments(ctx); err != nil {
			ctx.Logger().Warn("legacy billing recovery", "error", err.Error())
		}
		if err := a.recoverProviderOperations(c, ctx); err != nil {
			ctx.Logger().Warn("billing recovery", "error", err.Error())
		}
		if err := a.drainWebhookInbox(ctx); err != nil {
			ctx.Logger().Warn("billing webhook retry", "error", err.Error())
		}
		return flushOutbox(ctx)
	}}}
}

// Compatibility for the published SDK tag until the shared acknowledged emitter
// is released. The same authenticated gateway endpoint is used by the SDK.
func emitGatewayAck(ctx context.Context, topic, pid string, payload any) error {
	base := strings.TrimRight(os.Getenv("APTEVA_GATEWAY_URL"), "/")
	token := os.Getenv("APTEVA_APP_TOKEN")
	if base == "" || token == "" {
		return fmt.Errorf("event gateway not configured")
	}
	raw, err := json.Marshal(map[string]any{"topic": topic, "project_id": pid, "data": payload})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", base+"/api/app-events/internal/emit", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	io.Copy(io.Discard, io.LimitReader(res.Body, 65536))
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("event gateway HTTP %d", res.StatusCode)
	}
	return nil
}
