package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const (
	financialKindShippingCost          = "shipping_cost"
	financialKindPaymentFee            = "payment_fee"
	financialKindRefund                = "refund"
	financialKindAdjustment            = "adjustment"
	financialKindProductCostAdjustment = "product_cost_adjustment"
	financialKindBillingReconciliation = "billing_reconciliation"
)

type SaleFinancialEvent struct {
	ID             int64          `json:"id"`
	SaleID         int64          `json:"sale_id"`
	Kind           string         `json:"kind"`
	AmountCents    int64          `json:"amount_cents"`
	Currency       string         `json:"currency"`
	Source         string         `json:"source"`
	IdempotencyKey string         `json:"idempotency_key"`
	ExternalID     string         `json:"external_id,omitempty"`
	Metadata       map[string]any `json:"metadata,omitempty"`
	OccurredAt     string         `json:"occurred_at,omitempty"`
	CreatedAt      string         `json:"created_at"`
	UpdatedAt      string         `json:"updated_at"`
}

type ProfitabilityReport struct {
	StoreID                 int64  `json:"store_id,omitempty"`
	Currency                string `json:"currency,omitempty"`
	SaleCount               int64  `json:"sale_count"`
	CompleteSaleCount       int64  `json:"complete_sale_count"`
	IncompleteSaleCount     int64  `json:"incomplete_sale_count"`
	GrossSalesCents         int64  `json:"gross_sales_cents"`
	TaxCents                int64  `json:"tax_cents"`
	RefundedCents           int64  `json:"refunded_cents"`
	NetRevenueCents         int64  `json:"net_revenue_cents"`
	ProductCostCents        int64  `json:"product_cost_cents"`
	ShippingRevenueCents    int64  `json:"shipping_revenue_cents"`
	ShippingCostCents       int64  `json:"shipping_cost_cents"`
	PaymentFeeCents         int64  `json:"payment_fee_cents"`
	GrossProfitCents        int64  `json:"gross_profit_cents"`
	ContributionProfitCents int64  `json:"contribution_profit_cents"`
	ContributionMarginBPS   int64  `json:"contribution_margin_bps"`
}

func (a *App) toolSaleFinancialsRecord(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	sale, err := dbSaleGet(ctx.AppDB(), pid, intArg(args, "sale_id"))
	if err != nil || sale == nil {
		return nil, firstErr(err, errors.New("sale not found"))
	}
	kind := strings.ToLower(strArg(args, "kind"))
	if !validFinancialKind(kind) || kind == financialKindBillingReconciliation {
		return nil, errors.New("kind must be shipping_cost, payment_fee, refund, adjustment, or product_cost_adjustment")
	}
	amount := intArg(args, "amount_cents")
	if kind != financialKindAdjustment && kind != financialKindProductCostAdjustment && amount < 0 {
		return nil, errors.New("amount_cents cannot be negative for this kind")
	}
	currency := strings.ToUpper(firstNonEmpty(strArg(args, "currency"), sale.Currency))
	if currency != sale.Currency {
		return nil, fmt.Errorf("currency %s does not match sale currency %s; convert before recording", currency, sale.Currency)
	}
	key := strings.TrimSpace(strArg(args, "idempotency_key"))
	if key == "" {
		return nil, errors.New("idempotency_key required")
	}
	event, err := dbFinancialEventEnsure(ctx.AppDB(), pid, sale.ID, kind, amount, currency,
		firstNonEmpty(strArg(args, "source"), "manual"), key, strArg(args, "external_id"),
		mapArg(args, "metadata"), strArg(args, "occurred_at"))
	if err != nil {
		return nil, err
	}
	if err := dbRecomputeSaleProfitability(ctx.AppDB(), pid, sale.ID); err != nil {
		return nil, err
	}
	sale, err = dbSaleGet(ctx.AppDB(), pid, sale.ID)
	return map[string]any{"event": event, "sale": sale}, err
}

func (a *App) toolSaleFinancialsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	saleID := intArg(args, "sale_id")
	if saleID == 0 {
		return nil, errors.New("sale_id required")
	}
	events, err := dbFinancialEvents(ctx.AppDB(), pid, saleID)
	return map[string]any{"events": events, "count": len(events)}, err
}

func (a *App) toolSaleFinancialsReconcile(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	sale, err := dbSaleGet(ctx.AppDB(), pid, intArg(args, "sale_id"))
	if err != nil || sale == nil {
		return nil, firstErr(err, errors.New("sale not found"))
	}
	if err := a.reconcileSaleBilling(ctx.WithProject(pid), pid, sale); err != nil {
		return nil, err
	}
	sale, err = dbSaleGet(ctx.AppDB(), pid, sale.ID)
	return map[string]any{"sale": sale}, err
}

func (a *App) toolProfitabilityReport(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	report, err := dbProfitabilityReport(ctx.AppDB(), pid, args)
	return map[string]any{"report": report}, err
}

func validFinancialKind(kind string) bool {
	switch kind {
	case financialKindShippingCost, financialKindPaymentFee, financialKindRefund,
		financialKindAdjustment, financialKindProductCostAdjustment, financialKindBillingReconciliation:
		return true
	default:
		return false
	}
}

func dbFinancialEventEnsure(db *sql.DB, pid string, saleID int64, kind string, amount int64,
	currency, source, key, externalID string, metadata map[string]any, occurredAt string) (*SaleFinancialEvent, error) {
	if !validFinancialKind(kind) {
		return nil, fmt.Errorf("unsupported financial event kind %q", kind)
	}
	if saleID == 0 || strings.TrimSpace(key) == "" {
		return nil, errors.New("sale_id and idempotency_key required")
	}
	currency = strings.ToUpper(currency)
	var saleCurrency string
	if err := db.QueryRow(`SELECT currency FROM commerce_sales WHERE project_id=? AND id=?`, pid, saleID).Scan(&saleCurrency); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errors.New("sale not found")
		}
		return nil, err
	}
	if currency != saleCurrency {
		return nil, fmt.Errorf("currency %s does not match sale currency %s", currency, saleCurrency)
	}
	source = firstNonEmpty(strings.TrimSpace(source), "manual")
	if existing, err := dbFinancialEventByKey(db, pid, key); err == nil {
		return validateFinancialEvent(existing, saleID, kind, amount, currency)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if externalID != "" {
		if existing, err := dbFinancialEventByExternalID(db, pid, saleID, kind, externalID); err == nil {
			return validateFinancialEvent(existing, saleID, kind, amount, currency)
		} else if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
	}
	var happened any
	if strings.TrimSpace(occurredAt) != "" {
		if _, err := time.Parse(time.RFC3339, occurredAt); err != nil {
			return nil, errors.New("occurred_at must be RFC3339")
		}
		happened = occurredAt
	}
	_, err := db.Exec(`INSERT INTO commerce_sale_financial_events
		(project_id, sale_id, kind, amount_cents, currency, source, idempotency_key, external_id, metadata_json, occurred_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT DO NOTHING`,
		pid, saleID, kind, amount, currency, source, key, externalID, jsonText(metadata, "{}"), happened)
	if err != nil {
		return nil, err
	}
	event, err := dbFinancialEventByKey(db, pid, key)
	if errors.Is(err, sql.ErrNoRows) && externalID != "" {
		event, err = dbFinancialEventByExternalID(db, pid, saleID, kind, externalID)
	}
	if err != nil {
		return nil, err
	}
	return validateFinancialEvent(event, saleID, kind, amount, currency)
}

func dbFinancialEventByKey(db *sql.DB, pid, key string) (*SaleFinancialEvent, error) {
	return scanFinancialEvent(db.QueryRow(financialEventSelect()+` WHERE project_id=? AND idempotency_key=?`, pid, key))
}

func dbFinancialEventByExternalID(db *sql.DB, pid string, saleID int64, kind, externalID string) (*SaleFinancialEvent, error) {
	return scanFinancialEvent(db.QueryRow(financialEventSelect()+
		` WHERE project_id=? AND sale_id=? AND kind=? AND external_id=?`, pid, saleID, kind, externalID))
}

func validateFinancialEvent(event *SaleFinancialEvent, saleID int64, kind string, amount int64, currency string) (*SaleFinancialEvent, error) {
	if event.SaleID != saleID || event.Kind != kind || event.AmountCents != amount || event.Currency != currency {
		return nil, errors.New("idempotency key or external id already belongs to a different financial event")
	}
	return event, nil
}

func dbFinancialEvents(db *sql.DB, pid string, saleID int64) ([]*SaleFinancialEvent, error) {
	rows, err := db.Query(financialEventSelect()+` WHERE project_id=? AND sale_id=? ORDER BY id`, pid, saleID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*SaleFinancialEvent
	for rows.Next() {
		event, err := scanFinancialEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, event)
	}
	return out, rows.Err()
}

func financialEventSelect() string {
	return `SELECT id, sale_id, kind, amount_cents, currency, source, idempotency_key,
		external_id, metadata_json, COALESCE(occurred_at,''), created_at, updated_at
		FROM commerce_sale_financial_events`
}

func scanFinancialEvent(row scanner) (*SaleFinancialEvent, error) {
	var event SaleFinancialEvent
	var metadata string
	if err := row.Scan(&event.ID, &event.SaleID, &event.Kind, &event.AmountCents, &event.Currency,
		&event.Source, &event.IdempotencyKey, &event.ExternalID, &metadata, &event.OccurredAt,
		&event.CreatedAt, &event.UpdatedAt); err != nil {
		return nil, err
	}
	event.Metadata = jsonMap(metadata)
	return &event, nil
}

func dbRecomputeSaleProfitability(db *sql.DB, pid string, saleID int64) error {
	var total, tax, shippingRevenue int64
	var paymentStatus, currency, paymentProvider string
	if err := db.QueryRow(`SELECT s.total_cents, s.tax_cents, s.shipping_cents, s.payment_status, s.currency,
		COALESCE(st.payment_provider,'manual')
		FROM commerce_sales s JOIN commerce_stores st ON st.id=s.store_id
		WHERE s.project_id=? AND s.id=?`, pid, saleID).
		Scan(&total, &tax, &shippingRevenue, &paymentStatus, &currency, &paymentProvider); err != nil {
		return err
	}
	var productCost int64
	var itemCount, capturedItemCount, shippableCount int64
	if err := db.QueryRow(`SELECT COUNT(*),
		COALESCE(SUM(CASE WHEN cost_captured_at IS NOT NULL AND cost_currency=currency THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN requires_shipping!=0 THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN cost_captured_at IS NOT NULL AND cost_currency=currency
			THEN CAST(ROUND(unit_cost_cents*quantity) AS INTEGER) ELSE 0 END),0)
		FROM commerce_sale_items WHERE project_id=? AND sale_id=?`, pid, saleID).
		Scan(&itemCount, &capturedItemCount, &shippableCount, &productCost); err != nil {
		return err
	}
	totals := map[string]int64{}
	counts := map[string]int64{}
	rows, err := db.Query(`SELECT kind, COALESCE(SUM(amount_cents),0), COUNT(*)
		FROM commerce_sale_financial_events WHERE project_id=? AND sale_id=? AND currency=? GROUP BY kind`, pid, saleID, currency)
	if err != nil {
		return err
	}
	for rows.Next() {
		var kind string
		var amount, count int64
		if err := rows.Scan(&kind, &amount, &count); err != nil {
			rows.Close()
			return err
		}
		totals[kind], counts[kind] = amount, count
	}
	if err := rows.Close(); err != nil {
		return err
	}
	productCost += totals[financialKindProductCostAdjustment]
	shippingCost := totals[financialKindShippingCost]
	paymentFee := totals[financialKindPaymentFee]
	refunded := totals[financialKindRefund]
	remainingTotal := total - refunded
	recognizedTax := tax
	if remainingTotal <= 0 {
		recognizedTax = 0
	} else if total > 0 && refunded > 0 {
		recognizedTax = int64(math.Round(float64(tax) * float64(remainingTotal) / float64(total)))
	}
	netRevenue := remainingTotal - recognizedTax
	grossProfit := netRevenue - productCost
	contribution := grossProfit - shippingCost - paymentFee + totals[financialKindAdjustment]
	status := "pending_payment"
	if paymentStatus == "paid" || paymentStatus == "partially_refunded" || paymentStatus == "refunded" {
		completeItems := itemCount == capturedItemCount || counts[financialKindProductCostAdjustment] > 0
		completeShipping := shippableCount == 0 || counts[financialKindShippingCost] > 0
		completeFee := strings.EqualFold(paymentProvider, "manual") || counts[financialKindPaymentFee] > 0
		completeBilling := counts[financialKindBillingReconciliation] > 0
		if completeItems && completeShipping && completeFee && completeBilling {
			status = "complete"
		} else {
			status = "partial"
		}
	}
	_, err = db.Exec(`UPDATE commerce_sales SET shipping_revenue_cents=?, product_cost_cents=?,
		shipping_cost_cents=?, payment_fee_cents=?, refunded_cents=?, net_revenue_cents=?,
		gross_profit_cents=?, contribution_profit_cents=?, profitability_status=?,
		financials_updated_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP
		WHERE project_id=? AND id=?`, shippingRevenue, productCost, shippingCost, paymentFee, refunded,
		netRevenue, grossProfit, contribution, status, pid, saleID)
	return err
}

func dbProfitabilityReport(db *sql.DB, pid string, args map[string]any) (*ProfitabilityReport, error) {
	where := []string{"project_id=?", "payment_status IN ('paid','partially_refunded','refunded')"}
	values := []any{pid}
	if storeID := intArg(args, "store_id"); storeID != 0 {
		where = append(where, "store_id=?")
		values = append(values, storeID)
	}
	if currency := strings.ToUpper(strArg(args, "currency")); currency != "" {
		where = append(where, "currency=?")
		values = append(values, currency)
	}
	if since := strArg(args, "since"); since != "" {
		parsed, err := time.Parse(time.RFC3339, since)
		if err != nil {
			return nil, errors.New("since must be RFC3339")
		}
		where = append(where, "paid_at>=?")
		values = append(values, parsed.UTC().Format("2006-01-02 15:04:05"))
	}
	if until := strArg(args, "until"); until != "" {
		parsed, err := time.Parse(time.RFC3339, until)
		if err != nil {
			return nil, errors.New("until must be RFC3339")
		}
		where = append(where, "paid_at<?")
		values = append(values, parsed.UTC().Format("2006-01-02 15:04:05"))
	}
	query := `SELECT COUNT(*),
		COALESCE(SUM(CASE WHEN profitability_status='complete' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN profitability_status!='complete' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(total_cents),0), COALESCE(SUM(tax_cents),0), COALESCE(SUM(refunded_cents),0),
		COALESCE(SUM(net_revenue_cents),0), COALESCE(SUM(product_cost_cents),0),
		COALESCE(SUM(shipping_revenue_cents),0), COALESCE(SUM(shipping_cost_cents),0),
		COALESCE(SUM(payment_fee_cents),0), COALESCE(SUM(gross_profit_cents),0),
		COALESCE(SUM(contribution_profit_cents),0), COUNT(DISTINCT currency), COALESCE(MIN(currency),'')
		FROM commerce_sales WHERE ` + strings.Join(where, " AND ")
	var report ProfitabilityReport
	var currencyCount int64
	if err := db.QueryRow(query, values...).Scan(&report.SaleCount, &report.CompleteSaleCount,
		&report.IncompleteSaleCount, &report.GrossSalesCents, &report.TaxCents, &report.RefundedCents,
		&report.NetRevenueCents, &report.ProductCostCents, &report.ShippingRevenueCents,
		&report.ShippingCostCents, &report.PaymentFeeCents, &report.GrossProfitCents,
		&report.ContributionProfitCents, &currencyCount, &report.Currency); err != nil {
		return nil, err
	}
	if currencyCount > 1 {
		return nil, errors.New("profitability report spans multiple currencies; filter by store or currency")
	}
	report.StoreID = intArg(args, "store_id")
	if report.NetRevenueCents != 0 {
		report.ContributionMarginBPS = int64(math.Round(float64(report.ContributionProfitCents) * 10000 / float64(report.NetRevenueCents)))
	}
	return &report, nil
}

func quotedProviderShippingCost(metadata map[string]any, currency string) (int64, map[string]any, bool) {
	quote := mapArg(metadata, "checkout_quote")
	var selected []any
	if len(quote) > 0 {
		selectedShipping := mapArg(quote, "selected_shipping")
		selected = anySlice(selectedShipping["components"])
	}
	if len(selected) == 0 {
		quote = mapArg(metadata, "shipping_quote")
		selected = anySlice(quote["selected"])
	}
	var total int64
	var captured []any
	for _, raw := range selected {
		component := anyMap(raw)
		provider := strings.ToLower(strArg(component, "provider"))
		if provider == "" || provider == "commerce" {
			continue
		}
		componentCurrency := strings.ToUpper(firstNonEmpty(strArg(component, "currency"), currency))
		if componentCurrency != currency {
			return 0, map[string]any{"components": captured}, false
		}
		total += intArg(component, "amount_cents")
		captured = append(captured, component)
	}
	return total, map[string]any{"components": captured, "quote": quote}, len(captured) > 0
}

func (a *App) reconcileSaleBilling(ctx *sdk.AppCtx, pid string, sale *Sale) error {
	if sale == nil || sale.InvoiceID == nil || *sale.InvoiceID == 0 || ctx.PlatformAPI() == nil {
		return nil
	}
	var response map[string]any
	if err := ctx.PlatformAPI().CallAppResult("billing", "invoices_get", map[string]any{
		"_project_id": pid, "id": *sale.InvoiceID,
	}, &response); err != nil {
		return fmt.Errorf("reconcile Billing invoice: %w", err)
	}
	invoice := unwrap(response, "invoice")
	invoiceCurrency := strings.ToUpper(firstNonEmpty(strArg(invoice, "currency"), sale.Currency))
	if invoiceCurrency != sale.Currency {
		return fmt.Errorf("Billing invoice currency %s does not match sale currency %s", invoiceCurrency, sale.Currency)
	}
	for _, raw := range anySlice(invoice["payments"]) {
		payment := anyMap(raw)
		paymentID := intArg(payment, "id")
		if paymentID == 0 {
			continue
		}
		amount := intArg(payment, "amount_cents")
		occurred := firstNonEmpty(strArg(payment, "received_at"), strArg(payment, "created_at"))
		if amount < 0 {
			if _, err := dbFinancialEventEnsure(ctx.AppDB(), pid, sale.ID, financialKindRefund, -amount,
				sale.Currency, "billing", fmt.Sprintf("billing:payment:%d:refund", paymentID),
				strArg(payment, "external_id"), payment, occurred); err != nil {
				return err
			}
		}
		fee, feePresent := optionalInt(payment, "fee_cents", "processor_fee_cents", "payment_fee_cents")
		if feePresent && fee >= 0 {
			if _, err := dbFinancialEventEnsure(ctx.AppDB(), pid, sale.ID, financialKindPaymentFee, fee,
				sale.Currency, "billing", fmt.Sprintf("billing:payment:%d:fee", paymentID),
				strArg(payment, "external_id"), payment, occurred); err != nil {
				return err
			}
		}
	}
	var refunded int64
	if err := ctx.AppDB().QueryRow(`SELECT COALESCE(SUM(amount_cents),0)
		FROM commerce_sale_financial_events
		WHERE project_id=? AND sale_id=? AND kind=? AND currency=?`, pid, sale.ID,
		financialKindRefund, sale.Currency).Scan(&refunded); err != nil {
		return err
	}
	paymentStatus := "paid"
	if refunded >= sale.TotalCents && sale.TotalCents > 0 {
		paymentStatus = "refunded"
	} else if refunded > 0 {
		paymentStatus = "partially_refunded"
	}
	if _, err := ctx.AppDB().Exec(`UPDATE commerce_sales SET payment_status=?, updated_at=CURRENT_TIMESTAMP
		WHERE project_id=? AND id=? AND payment_status IN ('paid','partially_refunded','refunded')`,
		paymentStatus, pid, sale.ID); err != nil {
		return err
	}
	reconciliationKey := fmt.Sprintf("billing:invoice:%d:reconciled:%d:%s", *sale.InvoiceID,
		intArg(invoice, "amount_paid_cents"), strArg(invoice, "updated_at"))
	if _, err := dbFinancialEventEnsure(ctx.AppDB(), pid, sale.ID, financialKindBillingReconciliation, 0,
		sale.Currency, "billing", reconciliationKey, fmt.Sprint(*sale.InvoiceID),
		map[string]any{"invoice_status": strArg(invoice, "status"), "amount_paid_cents": intArg(invoice, "amount_paid_cents")}, ""); err != nil {
		return err
	}
	return dbRecomputeSaleProfitability(ctx.AppDB(), pid, sale.ID)
}

func optionalInt(value map[string]any, keys ...string) (int64, bool) {
	for _, key := range keys {
		if _, ok := value[key]; ok {
			return intArg(value, key), true
		}
	}
	metadata := mapArg(value, "metadata")
	for _, key := range keys {
		if _, ok := metadata[key]; ok {
			return intArg(metadata, key), true
		}
	}
	return 0, false
}

func (a *App) reconcileSaleFinancials(_ context.Context, ctx *sdk.AppCtx) error {
	pid := strings.TrimSpace(ctx.CurrentProject())
	if pid == "" {
		return nil
	}
	rows, err := ctx.AppDB().Query(`SELECT id FROM commerce_sales
		WHERE project_id=? AND invoice_id IS NOT NULL AND payment_status IN ('paid','partially_refunded','refunded')
		ORDER BY COALESCE(financials_updated_at, paid_at, updated_at) ASC LIMIT 100`, pid)
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	var failures []string
	for _, id := range ids {
		sale, err := dbSaleGet(ctx.AppDB(), pid, id)
		if err == nil {
			err = a.reconcileSaleBilling(ctx, pid, sale)
		}
		if err != nil {
			failures = append(failures, fmt.Sprintf("%d: %v", id, err))
		}
	}
	if len(failures) > 0 {
		return errors.New("sale financial reconciliation: " + strings.Join(failures, "; "))
	}
	return nil
}

func (a *App) handleInvoiceFinancialChanged(ctx *sdk.AppCtx, event sdk.Event) error {
	pid := strings.TrimSpace(event.ProjectID)
	if pid == "" {
		pid = ctx.CurrentProject()
	}
	invoiceID := intArg(event.Data, "id")
	if pid == "" || invoiceID == 0 {
		return nil
	}
	sale, err := dbSaleGetByInvoice(ctx.AppDB(), pid, invoiceID)
	if err != nil || sale == nil {
		return err
	}
	return a.reconcileSaleBilling(ctx.WithProject(pid), pid, sale)
}
