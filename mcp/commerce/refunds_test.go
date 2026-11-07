package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

func refundFixture(t *testing.T) (*sdk.AppCtx, *commercePlatformStub, *Sale, map[string]any) {
	t.Helper()
	platform := newCommercePlatformStub()
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("refunds"), tk.WithPlatform(platform))
	sale := createProfitabilitySale(t, ctx.AppDB(), "refunds")
	if _, err := ctx.AppDB().Exec(`UPDATE commerce_sales SET status='paid',payment_status='paid' WHERE id=?`, sale.ID); err != nil {
		t.Fatal(err)
	}
	sale, _ = dbSaleGet(ctx.AppDB(), "refunds", sale.ID)
	invoice := map[string]any{"id": int64(501), "project_id": "refunds", "currency": "USD", "amount_paid_cents": int64(3750), "total_cents": int64(3750), "status": "paid", "payments": []any{map[string]any{"id": int64(1), "amount_cents": int64(3750), "method": "stripe"}}}
	platform.responses["billing:invoices_get"] = map[string]any{"invoice": invoice}
	platform.responses["billing:invoices_refund"] = platformResponseFunc(func(input map[string]any) (any, error) {
		amount := intArg(input, "amount_cents")
		invoice["amount_paid_cents"] = intArg(invoice, "amount_paid_cents") - amount
		invoice["payments"] = append(anySlice(invoice["payments"]), map[string]any{"id": int64(2), "amount_cents": -amount, "method": "stripe"})
		return refundResponse(input, "succeeded"), nil
	})
	return ctx, platform, sale, invoice
}

func refundResponse(input map[string]any, status string) map[string]any {
	return map[string]any{"refund": map[string]any{"id": int64(77), "invoice_id": input["invoice_id"], "amount_cents": input["amount_cents"], "currency": "USD", "idempotency_key": input["idempotency_key"], "status": status}}
}
func refundCallCount(p *commercePlatformStub) int {
	n := 0
	for _, call := range p.calls {
		if call.Tool == "invoices_refund" {
			n++
		}
	}
	return n
}
func runSaleRefund(t *testing.T, ctx *sdk.AppCtx, args map[string]any) *SaleRefundResult {
	t.Helper()
	out, err := (&App{}).toolSaleRefund(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	return out.(*SaleRefundResult)
}

func TestSaleRefundExecutesAndReplaysWithoutDuplicate(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(map[bool]string{true: "partial", false: "full"}[partial], func(t *testing.T) {
			ctx, p, sale, _ := refundFixture(t)
			args := map[string]any{"sale_id": sale.ID, "idempotency_key": "authorized-1"}
			expected := int64(3750)
			status := "refunded"
			if partial {
				expected = 500
				status = "partially_refunded"
				args["amount_cents"] = expected
			}
			result := runSaleRefund(t, ctx, args)
			if result.Request.Status != "succeeded" || !result.Reconciled || result.Sale.RefundedCents != expected || result.Sale.PaymentStatus != status {
				t.Fatalf("result=%+v sale=%+v", result, result.Sale)
			}
			for _, call := range p.calls {
				if call.Input["_project_id"] != "refunds" {
					t.Fatalf("unscoped call: %+v", call)
				}
			}
			replay := runSaleRefund(t, ctx, args)
			if replay.Request.ID != result.Request.ID || refundCallCount(p) != 1 {
				t.Fatalf("duplicate execution: %+v", p.calls)
			}
			args["amount_cents"] = expected + 1
			if _, err := (&App{}).toolSaleRefund(ctx, args); err == nil {
				t.Fatal("accepted conflicting amount")
			}
			args["amount_cents"] = expected
			args["reason"] = "fraudulent"
			if _, err := (&App{}).toolSaleRefund(ctx, args); err == nil {
				t.Fatal("accepted conflicting reason")
			}
		})
	}
}

func TestSaleRefundRejectsInvalidRequestsBeforeExecution(t *testing.T) {
	tests := []struct {
		name   string
		change func(*sdk.AppCtx, *Sale, map[string]any, map[string]any)
	}{
		{"zero", func(_ *sdk.AppCtx, _ *Sale, _ map[string]any, a map[string]any) { a["amount_cents"] = 0 }},
		{"negative", func(_ *sdk.AppCtx, _ *Sale, _ map[string]any, a map[string]any) { a["amount_cents"] = -1 }},
		{"fractional", func(_ *sdk.AppCtx, _ *Sale, _ map[string]any, a map[string]any) { a["amount_cents"] = 1.5 }},
		{"null", func(_ *sdk.AppCtx, _ *Sale, _ map[string]any, a map[string]any) { a["amount_cents"] = nil }},
		{"oversized", func(_ *sdk.AppCtx, _ *Sale, _ map[string]any, a map[string]any) { a["amount_cents"] = 4000 }},
		{"missing-key", func(_ *sdk.AppCtx, _ *Sale, _ map[string]any, a map[string]any) { delete(a, "idempotency_key") }},
		{"wrong-project", func(_ *sdk.AppCtx, _ *Sale, i map[string]any, _ map[string]any) { i["project_id"] = "someone-else" }},
		{"wrong-invoice", func(_ *sdk.AppCtx, _ *Sale, i map[string]any, _ map[string]any) { i["id"] = 999 }},
		{"wrong-currency", func(_ *sdk.AppCtx, _ *Sale, i map[string]any, _ map[string]any) { i["currency"] = "EUR" }},
		{"manual-payment", func(_ *sdk.AppCtx, _ *Sale, i map[string]any, _ map[string]any) {
			i["payments"] = []any{map[string]any{"id": 1, "method": "cash", "amount_cents": 3750}}
		}},
		{"no-invoice", func(c *sdk.AppCtx, s *Sale, _ map[string]any, _ map[string]any) {
			c.AppDB().Exec("UPDATE commerce_sales SET invoice_id=NULL WHERE id=?", s.ID)
		}},
		{"unpaid", func(c *sdk.AppCtx, s *Sale, _ map[string]any, _ map[string]any) {
			c.AppDB().Exec("UPDATE commerce_sales SET payment_status='pending' WHERE id=?", s.ID)
		}},
		{"other-sale", func(_ *sdk.AppCtx, _ *Sale, _ map[string]any, a map[string]any) { a["sale_id"] = 999 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, p, sale, invoice := refundFixture(t)
			args := map[string]any{"sale_id": sale.ID, "amount_cents": 500, "idempotency_key": "key"}
			tt.change(ctx, sale, invoice, args)
			if _, err := (&App{}).toolSaleRefund(ctx, args); err == nil {
				t.Fatal("accepted invalid request")
			}
			if refundCallCount(p) != 0 {
				t.Fatal("issued refund before validation")
			}
		})
	}
	ctx, p, sale, _ := refundFixture(t)
	if _, err := (&App{}).toolSaleRefund(ctx.WithProject("other"), map[string]any{"sale_id": sale.ID, "idempotency_key": "key"}); err == nil {
		t.Fatal("accepted another project's sale")
	}
	if len(p.calls) != 0 {
		t.Fatal("queried Billing for foreign sale")
	}
}

func TestSaleRefundUnknownOutcomeRecoversOriginalFullAmount(t *testing.T) {
	ctx, p, sale, invoice := refundFixture(t)
	attempts := 0
	p.responses["billing:invoices_refund"] = platformResponseFunc(func(input map[string]any) (any, error) {
		attempts++
		if intArg(input, "amount_cents") != 3750 {
			t.Fatal("retry changed amount")
		}
		if attempts == 1 {
			invoice["amount_paid_cents"] = int64(0)
			invoice["payments"] = append(anySlice(invoice["payments"]), map[string]any{"id": 2, "amount_cents": -3750})
			return nil, errors.New("response lost after processor accepted")
		}
		return refundResponse(input, "succeeded"), nil
	})
	args := map[string]any{"sale_id": sale.ID, "idempotency_key": "full"}
	first := runSaleRefund(t, ctx, args)
	if first.Request.Status != "unknown" || first.Sale.PaymentStatus != "refunded" {
		t.Fatalf("outcome=%+v", first)
	}
	// A new App instance recovers durable state even though refundable balance is now zero.
	result := runSaleRefund(t, ctx, args)
	if result.Request.Status != "succeeded" || attempts != 2 {
		t.Fatalf("recovery=%+v", result)
	}
	if p.calls[1].Input["idempotency_key"] == "full" {
		t.Fatal("Billing key was not namespaced")
	}
	var keys []string
	for _, call := range p.calls {
		if call.Tool == "invoices_refund" {
			keys = append(keys, strArg(call.Input, "idempotency_key"))
		}
	}
	if keys[0] != keys[1] {
		t.Fatal("retry used a new Billing key")
	}
}

func TestSaleRefundPendingFailedAndMalformedOutcomes(t *testing.T) {
	for _, status := range []string{"submitted", "failed", "malformed"} {
		t.Run(status, func(t *testing.T) {
			ctx, p, sale, _ := refundFixture(t)
			p.responses["billing:invoices_refund"] = platformResponseFunc(func(input map[string]any) (any, error) { return refundResponse(input, status), nil })
			result := runSaleRefund(t, ctx, map[string]any{"sale_id": sale.ID, "amount_cents": 500, "idempotency_key": "key"})
			want := status
			if status == "malformed" {
				want = "unknown"
			}
			if result.Request.Status != want || result.Sale.RefundedCents != 0 || result.Sale.PaymentStatus != "paid" {
				t.Fatalf("outcome=%+v", result)
			}
		})
	}
}

func TestSaleRefundPreservesExecutionWhenReconciliationFails(t *testing.T) {
	ctx, p, sale, _ := refundFixture(t)
	original := p.responses["billing:invoices_refund"].(platformResponseFunc)
	p.responses["billing:invoices_refund"] = platformResponseFunc(func(input map[string]any) (any, error) {
		result, err := original(input)
		p.errors["billing:invoices_get"] = errors.New("Billing unavailable")
		return result, err
	})
	args := map[string]any{"sale_id": sale.ID, "amount_cents": 500, "idempotency_key": "key"}
	result := runSaleRefund(t, ctx, args)
	if result.Request.Status != "succeeded" || result.Reconciled || result.ReconciliationError == "" {
		t.Fatalf("outcome=%+v", result)
	}
	delete(p.errors, "billing:invoices_get")
	recovered := runSaleRefund(t, ctx, args)
	if !recovered.Reconciled || recovered.Sale.RefundedCents != 500 || refundCallCount(p) != 1 {
		t.Fatalf("recovery=%+v", recovered)
	}
}

func TestSaleRefundHTTPUsesPathSaleAndListsDurableRequests(t *testing.T) {
	ctx, p, sale, _ := refundFixture(t)
	old := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = old })
	path := "/admin/sales/" + strconv.FormatInt(sale.ID, 10) + "/refunds?project_id=refunds"
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"sale_id":999,"_project_id":"other","amount_cents":500,"idempotency_key":"http"}`))
	w := httptest.NewRecorder()
	(&App{}).handleSale(w, req)
	if w.Code != 200 || refundCallCount(p) != 1 {
		t.Fatalf("HTTP %d: %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	(&App{}).handleSale(w, httptest.NewRequest(http.MethodGet, path, nil))
	var response struct {
		Requests []SaleRefundRequest `json:"requests"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || len(response.Requests) != 1 || response.Requests[0].IdempotencyKey != "http" {
		t.Fatalf("list: %s", w.Body.String())
	}
	w = httptest.NewRecorder()
	(&App{}).handleSale(w, httptest.NewRequest(http.MethodDelete, path, nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("DELETE=%d", w.Code)
	}
}

func TestSaleRefundFullUsesRemainingConfirmedBalance(t *testing.T) {
	ctx, p, sale, invoice := refundFixture(t)
	invoice["amount_paid_cents"] = int64(3250)
	invoice["payments"] = append(anySlice(invoice["payments"]), map[string]any{"id": int64(9), "amount_cents": int64(-500), "method": "stripe"})
	result := runSaleRefund(t, ctx, map[string]any{"sale_id": sale.ID, "idempotency_key": "remaining"})
	if result.Request.AmountCents != 3250 || result.Sale.RefundedCents != 3750 || result.Sale.PaymentStatus != "refunded" {
		t.Fatalf("remaining refund=%+v sale=%+v", result, result.Sale)
	}
	if refundCallCount(p) != 1 {
		t.Fatal("unexpected refund executions")
	}
}

func TestSaleRefundHTTPRejectsNullBody(t *testing.T) {
	ctx, p, sale, _ := refundFixture(t)
	old := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = old })
	path := "/admin/sales/" + strconv.FormatInt(sale.ID, 10) + "/refunds?project_id=refunds"
	w := httptest.NewRecorder()
	(&App{}).handleSale(w, httptest.NewRequest(http.MethodPost, path, strings.NewReader("null")))
	if w.Code != http.StatusBadRequest || refundCallCount(p) != 0 {
		t.Fatalf("null body: %d %s", w.Code, w.Body.String())
	}
}
