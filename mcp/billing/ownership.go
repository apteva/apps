package main

import (
	"database/sql"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"strings"
)

func persistPaymentConnection(ctx *sdk.AppCtx, payment *Payment) error {
	b := ctx.IntegrationFor("payment_processor")
	if b == nil {
		return nil
	}
	_, err := ctx.AppDB().Exec("INSERT INTO billing_payment_connections(payment_id,connection_id) VALUES(?,?) ON CONFLICT(payment_id) DO NOTHING", payment.ID, b.ConnectionID)
	return err
}
func requirePaymentConnection(ctx *sdk.AppCtx, b *sdk.BoundIntegration, request *RefundRequest) error {
	var conn int64
	err := ctx.AppDB().QueryRow("SELECT connection_id FROM billing_payment_connections WHERE payment_id=?", request.PaymentID).Scan(&conn)
	if err == nil {
		if conn != b.ConnectionID {
			return errors.New("refund must use the original payment processor connection")
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	// Legacy payments have no connection receipt. Verify the original object.
	var pi stripePaymentIntent
	if err = executeStripeRaw(ctx, b, "get_payment_intent", map[string]any{"payment_intent_id": request.ProviderPaymentID}, &pi); err != nil {
		return err
	}
	var amount int64
	if err = ctx.AppDB().QueryRow("SELECT amount_cents FROM payments WHERE id=?", request.PaymentID).Scan(&amount); err != nil {
		return err
	}
	if pi.ID != request.ProviderPaymentID || pi.Amount != amount || !strings.EqualFold(pi.Currency, request.Currency) {
		return errors.New("legacy payment could not be verified on this processor")
	}
	_, err = ctx.AppDB().Exec("INSERT INTO billing_payment_connections(payment_id,connection_id) VALUES(?,?)", request.PaymentID, b.ConnectionID)
	return err
}
func requireMethodConnection(ctx *sdk.AppCtx, b *sdk.BoundIntegration, pm *PaymentMethod) error {
	var conn int64
	err := ctx.AppDB().QueryRow("SELECT connection_id FROM billing_method_connections WHERE method_id=?", pm.ID).Scan(&conn)
	if err == nil {
		if conn != b.ConnectionID {
			return errors.New("saved method belongs to a different processor connection")
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	fetched, err := fetchStripePaymentMethod(ctx, b, pm.ProviderPaymentMethodID)
	if err != nil {
		return err
	}
	if fetched.ProviderPaymentMethodID != pm.ProviderPaymentMethodID || fetched.ProviderCustomerID != pm.ProviderCustomerID {
		return fmt.Errorf("saved method could not be verified on the processor")
	}
	_, err = ctx.AppDB().Exec("INSERT INTO billing_method_connections(method_id,connection_id) VALUES(?,?) ON CONFLICT(method_id) DO NOTHING", pm.ID, b.ConnectionID)
	return err
}
