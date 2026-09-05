package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestIntegrityConcurrentCustomerUpsert(t *testing.T) {
	ctx := newTestCtx(t)
	var wg sync.WaitGroup
	ids := make(chan int64, 24)
	errs := make(chan error, 24)
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, _, e := dbCustomerUpsertByEmail(ctx.AppDB(), "test-proj", "same@example.com", nil)
			if e != nil {
				errs <- e
			} else {
				ids <- c.ID
			}
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for e := range errs {
		t.Error(e)
	}
	var first int64
	for id := range ids {
		if first == 0 {
			first = id
		}
		if first != id {
			t.Fatal("duplicate customers")
		}
	}
}
func TestIntegrityValidation(t *testing.T) {
	for _, v := range []any{math.NaN(), math.Inf(1), -1.0, 0.0, 1e9} {
		if validateLineInput(map[string]any{"quantity": v}) == nil {
			t.Fatalf("accepted invalid quantity %v", v)
		}
	}
	for _, v := range []any{1.2, "bad", math.Inf(1)} {
		if validateInput(map[string]any{"amount_cents": v}) == nil {
			t.Fatalf("accepted money %v", v)
		}
	}
	if e := validateInput(map[string]any{"external_id": "pi_safe", "invoice_id": int64(1)}); e != nil {
		t.Fatal(e)
	}
}
func TestIntegritySnapshotAndZeroInvoice(t *testing.T) {
	ctx := newTestCtx(t)
	c := mustCustomer(t, ctx, "before@example.com", "Original Buyer")
	i := mustFinalize(t, ctx, mustDraft(t, ctx, c.ID, []any{line("Free", 1, 0, 0)}).ID)
	if i.Status != "paid" || i.PaidAt == "" {
		t.Fatalf("zero invoice not settled: %+v", i)
	}
	if _, e := dbCustomerUpdate(ctx.AppDB(), "test-proj", c.ID, map[string]any{"name": "Changed"}); e != nil {
		t.Fatal(e)
	}
	_, saved, _, e := loadIssuedInvoiceForRender(ctx.AppDB(), "test-proj", i.ID)
	if e != nil || saved.Name != "Original Buyer" {
		t.Fatalf("snapshot=%+v err=%v", saved, e)
	}
}
func TestIntegrityCurrenciesAndPaging(t *testing.T) {
	ctx := newTestCtx(t)
	c := mustCustomer(t, ctx, "pages@example.com", "Pages")
	for i := 0; i < 63; i++ {
		inv := mustDraft(t, ctx, c.ID, []any{line(fmt.Sprintf("Item %d", i), 1, 100, 0)})
		if i == 62 {
			_, e := dbInvoiceUpdate(ctx.AppDB(), "test-proj", inv.ID, map[string]any{"currency": "EUR", "notes": "needle beyond first page"}, "test")
			if e != nil {
				t.Fatal(e)
			}
		}
		mustFinalize(t, ctx, inv.ID)
	}
	first, e := dbInvoiceSearch(ctx.AppDB(), "test-proj", invoiceFilters{limit: 50})
	if e != nil {
		t.Fatal(e)
	}
	second, e := dbInvoiceSearch(ctx.AppDB(), "test-proj", invoiceFilters{limit: 50, offset: 50})
	if e != nil {
		t.Fatal(e)
	}
	if len(first) != 50 || len(second) != 13 {
		t.Fatalf("page lengths %d %d", len(first), len(second))
	}
	found, e := dbInvoiceSearch(ctx.AppDB(), "test-proj", invoiceFilters{q: "needle"})
	if e != nil || len(found) != 1 {
		t.Fatalf("search %d %v", len(found), e)
	}
	totals, e := dbCustomerTotals(ctx.AppDB(), "test-proj", c.ID)
	if e != nil {
		t.Fatal(e)
	}
	if _, unsafe := totals["invoiced_cents"]; unsafe || totals["mixed_currencies"] != true {
		t.Fatalf("mixed totals %+v", totals)
	}
}
func TestIntegrityPaidAtChronology(t *testing.T) {
	ctx := newTestCtx(t)
	c := mustCustomer(t, ctx, "dates@example.com", "Dates")
	i := mustFinalize(t, ctx, mustDraft(t, ctx, c.ID, []any{line("X", 1, 1000, 0)}).ID)
	if _, _, e := dbPaymentRecord(ctx.AppDB(), "test-proj", i.ID, 600, "wire", "later", "2025-03-02T00:00:00Z", "", "test"); e != nil {
		t.Fatal(e)
	}
	_, paid, e := dbPaymentRecord(ctx.AppDB(), "test-proj", i.ID, 400, "wire", "earlier", "2025-03-01T01:00:00+01:00", "", "test")
	if e != nil {
		t.Fatal(e)
	}
	if paid.PaidAt != "2025-03-02T00:00:00Z" {
		t.Fatal(paid.PaidAt)
	}
}
func TestIntegrityCollectionUsesOriginalAmountAndMonotonicState(t *testing.T) {
	ctx := newTestCtx(t)
	a := &App{}
	c := mustCustomer(t, ctx, "collect-integrity@example.com", "C")
	i := mustFinalize(t, ctx, mustDraft(t, ctx, c.ID, []any{line("X", 1, 1200, 0)}).ID)
	pm, e := dbPaymentMethodUpsert(ctx.AppDB(), &PaymentMethod{ProjectID: "test-proj", CustomerID: c.ID, ProviderPaymentMethodID: "pm_c", ProviderCustomerID: "cus_c", Type: "card", Reusable: true})
	if e != nil {
		t.Fatal(e)
	}
	attempt, _, e := dbCollectionAttemptClaim(ctx.AppDB(), "test-proj", i.ID, pm.ID, "immutable", 1200, "USD")
	if e != nil {
		t.Fatal(e)
	}
	if _, _, e = dbPaymentRecord(ctx.AppDB(), "test-proj", i.ID, 500, "wire", "partial", nowRFC3339(), "", "test"); e != nil {
		t.Fatal(e)
	}
	i, _ = dbInvoiceGetByID(ctx.AppDB(), "test-proj", i.ID)
	intent := &stripePaymentIntent{ID: "pi_immutable", Status: "succeeded", Amount: 1200, Currency: "usd", Customer: "cus_c"}
	got, e := a.applyCollectionIntent(ctx, "test-proj", i, attempt.ID, intent)
	if e != nil {
		t.Fatal(e)
	}
	if got.AmountCents != 1200 {
		t.Fatal(got)
	}
	paid, _ := dbInvoiceGetByID(ctx.AppDB(), "test-proj", i.ID)
	if paid.AmountPaidCents != 1700 || paid.CreditCents != 500 {
		t.Fatalf("ledger=%+v", paid)
	}
	intent.Status = "requires_payment_method"
	got, e = a.applyCollectionIntent(ctx, "test-proj", paid, attempt.ID, intent)
	if e != nil || got.Status != "succeeded" {
		t.Fatalf("downgraded: %+v %v", got, e)
	}
}
func TestIntegrityRefundEventOrdersAndSourceAllocation(t *testing.T) {
	ctx := newTestCtx(t)
	a := &App{}
	c := mustCustomer(t, ctx, "refund-integrity@example.com", "R")
	i := mustFinalize(t, ctx, mustDraft(t, ctx, c.ID, []any{line("X", 1, 2000, 0)}).ID)
	for _, pi := range []string{"pi_one", "pi_two"} {
		if _, _, e := dbPaymentRecord(ctx.AppDB(), "test-proj", i.ID, 1000, "stripe", pi, nowRFC3339(), "", "test"); e != nil {
			t.Fatal(e)
		}
	}
	for _, event := range []string{`{"id":"ch_one","payment_intent":"pi_one","amount_refunded":300,"currency":"usd"}`, `{"id":"ch_one","payment_intent":"pi_one","amount_refunded":300,"currency":"usd","refunds":{"data":[{"id":"re_one","amount":300,"status":"succeeded"}]}}`, `{"id":"ch_one","payment_intent":"pi_one","amount_refunded":500,"currency":"usd","refunds":{"data":[{"id":"re_one","amount":300,"status":"succeeded"},{"id":"re_two","amount":200,"status":"succeeded"}]}}`} {
		if e := a.handleChargeRefunded(ctx, json.RawMessage(event)); e != nil {
			t.Fatal(e)
		}
	}
	paid, _ := dbInvoiceGetByID(ctx.AppDB(), "test-proj", i.ID)
	if paid.AmountPaidCents != 1500 || !paid.CollectionHold {
		t.Fatalf("refund counted twice or collectible: %+v", paid)
	}
	p, e := dbRefundableStripePayment(ctx.AppDB(), "test-proj", i.ID, 800)
	if e != nil || p == nil || p.ExternalID != "pi_two" {
		t.Fatalf("source allocation %+v %v", p, e)
	}
	if e = a.handleRefundObject(ctx, json.RawMessage(`{"id":"re_one","payment_intent":"pi_one","amount":300,"status":"pending","currency":"usd"}`)); e != nil {
		t.Fatal(e)
	}
	var status string
	ctx.AppDB().QueryRow("SELECT status FROM billing_provider_refunds WHERE id='re_one'").Scan(&status)
	if status != "succeeded" {
		t.Fatal("refund downgraded")
	}
}
func TestIntegrityPaymentMethodOwnershipAndTombstone(t *testing.T) {
	ctx := newTestCtx(t)
	c := mustCustomer(t, ctx, "owner@example.com", "Owner")
	other := mustCustomer(t, ctx, "other@example.com", "Other")
	pm := &PaymentMethod{ProjectID: "test-proj", CustomerID: c.ID, ProviderPaymentMethodID: "pm_owner", ProviderCustomerID: "cus_owner", Type: "card"}
	saved, e := dbPaymentMethodUpsert(ctx.AppDB(), pm)
	if e != nil {
		t.Fatal(e)
	}
	pm.CustomerID = other.ID
	if _, e = dbPaymentMethodUpsert(ctx.AppDB(), pm); e == nil {
		t.Fatal("reassigned payment method")
	}
	pm.CustomerID = c.ID
	if _, e = dbPaymentMethodDetach(ctx.AppDB(), "test-proj", saved.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = dbPaymentMethodUpsert(ctx.AppDB(), pm); e == nil {
		t.Fatal("reactivated tombstone")
	}
}

type lostResponsePlatform struct {
	stripePlatformStub
	mu      sync.Mutex
	first   bool
	intents map[string]map[string]any
	creates int
}

func (p *lostResponsePlatform) ExecuteIntegrationTool(conn int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if tool == "create_payment_intent" {
		key := strArg(input, "idempotency_key")
		if p.intents == nil {
			p.intents = map[string]map[string]any{}
		}
		r, ok := p.intents[key]
		if !ok {
			p.creates++
			r = map[string]any{"id": "pi_recovered", "amount": input["amount"], "currency": input["currency"], "metadata": input["metadata"], "status": "succeeded", "customer": input["customer"]}
			p.intents[key] = r
		}
		if !p.first {
			p.first = true
			return nil, errors.New("connection lost after provider commit")
		}
		raw, _ := json.Marshal(r)
		return &sdk.ExecuteResult{Success: true, Status: 200, Data: raw}, nil
	}
	return p.stripePlatformStub.ExecuteIntegrationTool(conn, tool, input)
}
func TestIntegrityLostProviderResponseRecoveredWithoutSecondCharge(t *testing.T) {
	p := &lostResponsePlatform{}
	ctx := newTestCtx(t, tk.WithPlatform(p))
	a := &App{}
	c := mustCustomer(t, ctx, "lost@example.com", "Lost")
	i := mustFinalize(t, ctx, mustDraft(t, ctx, c.ID, []any{line("X", 1, 1200, 0)}).ID)
	b, e := requireProcessor(ctx)
	if e != nil {
		t.Fatal(e)
	}
	input := map[string]any{"amount": int64(1200), "currency": "usd", "idempotency_key": "recover", "metadata": map[string]any{"apteva_project_id": "test-proj", "apteva_invoice_id": fmt.Sprint(i.ID)}}
	var out map[string]any
	if e = executeStripe(ctx, b, "create_payment_intent", input, &out); e == nil {
		t.Fatal("expected lost response")
	}
	if e = a.recoverProviderOperations(context.Background(), ctx); e != nil {
		t.Fatal(e)
	}
	paid, _ := dbInvoiceGetByID(ctx.AppDB(), "test-proj", i.ID)
	if paid.AmountPaidCents != 1200 || p.creates != 1 {
		t.Fatalf("paid %d provider charges %d", paid.AmountPaidCents, p.creates)
	}
	var locks int
	ctx.AppDB().QueryRow("SELECT count(*) FROM billing_payment_locks").Scan(&locks)
	if locks != 0 {
		t.Fatal("successful operation remains locked")
	}
}
func TestIntegrityCompetingProviderRequests(t *testing.T) {
	p := &lostResponsePlatform{first: true}
	ctx := newTestCtx(t, tk.WithPlatform(p))
	c := mustCustomer(t, ctx, "concurrent@example.com", "Concurrent")
	i := mustFinalize(t, ctx, mustDraft(t, ctx, c.ID, []any{line("X", 1, 1200, 0)}).ID)
	b, e := requireProcessor(ctx)
	if e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	success := make(chan bool, 12)
	for n := 0; n < 12; n++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			var out map[string]any
			e := executeStripe(ctx, b, "create_payment_intent", map[string]any{"amount": int64(1200), "currency": "usd", "idempotency_key": fmt.Sprint(n), "metadata": map[string]any{"apteva_project_id": "test-proj", "apteva_invoice_id": fmt.Sprint(i.ID)}}, &out)
			success <- e == nil
		}(n)
	}
	wg.Wait()
	close(success)
	n := 0
	for ok := range success {
		if ok {
			n++
		}
	}
	if n != 1 || p.creates != 1 {
		t.Fatalf("accepted %d / charged %d", n, p.creates)
	}
}
func TestIntegrityLongPDFBoundedPages(t *testing.T) {
	inv := sampleInvoice()
	inv.LineItems = nil
	for n := 0; n < 80; n++ {
		inv.LineItems = append(inv.LineItems, LineItem{Description: fmt.Sprintf("ROW-%03d %s", n, strings.Repeat("Long billing description ", 4)), Quantity: 1, UnitPriceCents: 100, AmountCents: 100})
	}
	inv.SubtotalCents = 8000
	inv.TaxCents = 0
	inv.TotalCents = 8000
	inv.AmountPaidCents = 0
	b, e := renderInvoicePDF(inv, sampleCustomer(), nil)
	if e != nil {
		t.Fatal(e)
	}
	pages := len(regexp.MustCompile(`/Type /Page\b`).FindAll(b, -1))
	if pages < 2 || pages > 10 {
		t.Fatalf("80 rows generated %d pages", pages)
	}
	if dir := os.Getenv("BILLING_PDF_QA_DIR"); dir != "" {
		os.MkdirAll(dir, 0755)
		if e = os.WriteFile(filepath.Join(dir, "long-invoice.pdf"), b, 0644); e != nil {
			t.Fatal(e)
		}
	}
}
func TestIntegrityNegativeInvoiceCannotFinalize(t *testing.T) {
	ctx := newTestCtx(t)
	c := mustCustomer(t, ctx, "negative@example.com", "N")
	i := mustDraft(t, ctx, c.ID, []any{line("Discount only", 1, -500, 0)})
	if _, e := dbInvoiceFinalize(ctx.AppDB(), "test-proj", i.ID, "INV-{seq}", 1, "test"); e == nil {
		t.Fatal("finalized negative invoice")
	}
}
func TestIntegrityTimeParser(t *testing.T) {
	got, e := parseBillingTime("2025-01-01T23:30:00-02:00")
	if e != nil || got.Format(time.RFC3339) != "2025-01-02T01:30:00Z" {
		t.Fatalf("%v %v", got, e)
	}
}

func TestIntegrityRefundHoldAndRetryPolicy(t *testing.T) {
	ctx := newTestCtx(t)
	a := &App{}
	db := ctx.AppDB()
	c := mustCustomer(t, ctx, "holds@example.com", "H")
	inv := mustFinalize(t, ctx, mustDraft(t, ctx, c.ID, []any{line("X", 1, 2000, 0)}).ID)
	p, _, err := dbPaymentRecord(db, "test-proj", inv.ID, 2000, "stripe", "pi_hold", nowRFC3339(), "", "test")
	if err != nil {
		t.Fatal(err)
	}
	request, err := dbRefundCreate(db, "test-proj", inv, p, 300, "requested_by_customer", "hold-key", true)
	if err != nil {
		t.Fatal(err)
	}
	if !request.ReopenInvoice {
		t.Fatal("reopen policy not persisted with reservation")
	}
	if err = dbRefundSubmitted(db, "test-proj", request.ID, "re_hold", "submitted"); err != nil {
		t.Fatal(err)
	}
	first := json.RawMessage(`{"id":"re_hold","payment_intent":"pi_hold","amount":300,"currency":"usd","status":"succeeded"}`)
	if err = a.handleRefundObject(ctx, first); err != nil {
		t.Fatal(err)
	}
	current, _ := dbInvoiceGetByID(db, "test-proj", inv.ID)
	if current.CollectionHold {
		t.Fatal("explicit reopen did not release hold")
	}
	if _, err = a.toolInvoicesRefund(ctx, map[string]any{"_project_id": "test-proj", "invoice_id": inv.ID, "idempotency_key": "hold-key", "reopen_invoice": false}); err == nil {
		t.Fatal("accepted changed retry policy")
	}
	if err = a.handleRefundObject(ctx, json.RawMessage(`{"id":"re_commercial","payment_intent":"pi_hold","amount":200,"currency":"usd","status":"succeeded"}`)); err != nil {
		t.Fatal(err)
	}
	if err = a.handleRefundObject(ctx, first); err != nil {
		t.Fatal(err)
	}
	current, _ = dbInvoiceGetByID(db, "test-proj", inv.ID)
	if !current.CollectionHold || current.AmountPaidCents != 1500 {
		t.Fatalf("old replay released later hold: %+v", current)
	}
	history, err := a.toolInvoicesHistory(ctx, map[string]any{"_project_id": "test-proj", "invoice_id": inv.ID})
	if err != nil {
		t.Fatal(err)
	}
	refunds := history.(map[string]any)["refunds"].([]*RefundRequest)
	if len(refunds) != 1 || refunds[0].Status != "succeeded" {
		t.Fatalf("refund history: %+v", refunds)
	}
}

func TestIntegrityNumberingAcrossYears(t *testing.T) {
	ctx := newTestCtx(t)
	db := ctx.AppDB()
	mint := func(format, date string) string {
		t.Helper()
		tx, e := db.Begin()
		if e != nil {
			t.Fatal(e)
		}
		defer tx.Rollback()
		at, _ := time.Parse(time.RFC3339, date)
		num, e := mintInvoiceNumberAtTx(tx, "test-proj", format, 1, at)
		if e != nil {
			t.Fatal(e)
		}
		if e = tx.Commit(); e != nil {
			t.Fatal(e)
		}
		return num
	}
	for _, tc := range []struct{ format, date, want string }{
		{"INV-{seq:04}", "2026-12-31T23:59:59Z", "INV-0001"},
		{"INV-{seq:04}", "2027-01-01T00:00:00Z", "INV-0002"},
		{"INV-{yyyy}-{seq:04}", "2026-12-31T23:59:59Z", "INV-2026-0001"},
		{"INV-{yyyy}-{seq:04}", "2027-01-01T00:00:00Z", "INV-2027-0001"},
		{"INV-{yyyy}-{seq:04}", "2027-01-01T00:00:01Z", "INV-2027-0002"},
	} {
		if got := mint(tc.format, tc.date); got != tc.want {
			t.Fatalf("number %s want %s", got, tc.want)
		}
	}
}

type retryEmitter struct {
	tk.EmitRecorder
	fail bool
	ids  []string
}

func (r *retryEmitter) EmitWithProjectAck(_ context.Context, topic, pid string, data any) error {
	r.ids = append(r.ids, data.(map[string]any)["event_id"].(string))
	if r.fail {
		return errors.New("gateway unavailable")
	}
	return nil
}
func TestIntegrityOutboxRetainsFailedDelivery(t *testing.T) {
	emitter := &retryEmitter{fail: true}
	ctx := newTestCtx(t)
	ctx.SetEmitter(emitter)
	db := ctx.AppDB()
	_, err := db.Exec(`INSERT INTO billing_outbox(id,project_id,topic,payload) VALUES('event-retry','test-proj','billing.invoice.paid','{"event_id":"event-retry"}')`)
	if err != nil {
		t.Fatal(err)
	}
	if flushOutbox(ctx) == nil {
		t.Fatal("expected delivery failure")
	}
	var pending int
	db.QueryRow("SELECT count(*) FROM billing_outbox WHERE delivered_at IS NULL").Scan(&pending)
	if pending != 1 {
		t.Fatal("failed event discarded")
	}
	emitter.fail = false
	if err = flushOutbox(ctx); err != nil {
		t.Fatal(err)
	}
	if err = flushOutbox(ctx); err != nil {
		t.Fatal(err)
	}
	if len(emitter.ids) != 2 || emitter.ids[0] != emitter.ids[1] {
		t.Fatalf("retry identities: %v", emitter.ids)
	}
	db.QueryRow("SELECT count(*) FROM billing_outbox WHERE delivered_at IS NULL").Scan(&pending)
	if pending != 0 {
		t.Fatal("successful event still pending")
	}
}

func TestIntegrityPopulatedUpgrade(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	paths, err := filepath.Glob("migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		if strings.Contains(path, "011_") {
			continue
		}
		raw, e := os.ReadFile(path)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = db.Exec(string(raw)); e != nil {
			t.Fatalf("%s: %v", path, e)
		}
	}
	for _, query := range []string{
		`INSERT INTO customers(id,project_id,name,email) VALUES(1,'p','First',' SAME@example.com '),(2,'p','Second','same@example.com')`,
		`INSERT INTO invoices(id,project_id,customer_id,currency,number,status,total_cents,amount_paid_cents) VALUES(1,'p',2,'USD','INV-OLD','paid',1000,1000)`,
		`INSERT INTO payments(project_id,invoice_id,customer_id,amount_cents,currency,method,external_id,received_at) VALUES('p',1,2,1000,'USD','stripe','pi_old','2026-01-01T00:00:00Z')`,
	} {
		if _, err = db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile("migrations/011_integrity.sql")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.Exec(string(raw)); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var conflict, customer, paid int
	db.QueryRow("SELECT email_conflict FROM customers WHERE id=2").Scan(&conflict)
	db.QueryRow("SELECT customer_id,amount_paid_cents FROM invoices WHERE id=1").Scan(&customer, &paid)
	if conflict != 1 || customer != 2 || paid != 1000 {
		t.Fatalf("upgrade rewrote financial identity: %d %d %d", conflict, customer, paid)
	}
	if _, err = db.Exec(`INSERT INTO customers(project_id,name,email) VALUES('p','Third','same@example.com')`); err == nil {
		t.Fatal("unique constraint missing")
	}
	rows, err := db.Query("PRAGMA foreign_key_check")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("upgrade left invalid foreign keys")
	}
}
