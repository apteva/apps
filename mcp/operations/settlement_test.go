package main

import (
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
)

// cleanJob takes a regular clean from intake to completion, booked with
// the given resource at 10:00 on the test day plus dayOffset.
func cleanJob(t *testing.T, db *sql.DB, args map[string]any, resID int64, startsAt string) *Work {
	t.Helper()
	base := map[string]any{"work_type": "regular_clean", "address": "1 rue A", "customer": map[string]any{"name": "Client", "email": "c@example.com"},
		"answers": map[string]any{"property_type": "flat", "hours": 3}, "items": []any{"REGULAR_CLEAN"}}
	for k, v := range args {
		base[k] = v
	}
	w := must(intakeWork(db, pid, nil, base)).Work
	br := must(bookSlot(db, pid, w, map[string]any{"starts_at": startsAt, "resource_ids": ids(resID)}))
	must(submitForm(db, pid, map[string]any{"form_key": "clean_checklist", "work_id": float64(w.ID),
		"data": map[string]any{"kitchen": true, "bathrooms": true, "floors": true, "bins": true}}))
	return must(completeWork(db, pid, br.Work, map[string]any{})).Work
}

type billingStub struct {
	calls []map[string]any
	next  int64
}

func (b *billingStub) call(app, tool string, input map[string]any, out any) error {
	b.calls = append(b.calls, map[string]any{"app": app, "tool": tool, "input": input})
	var resp any
	switch tool {
	case "customers_upsert_by_email":
		resp = map[string]any{"customer": map[string]any{"id": 5}}
	case "invoices_create_from_prepared_lines":
		b.next++
		resp = map[string]any{"invoice": map[string]any{"id": 100 + b.next, "number": "C-" + string(rune('0'+b.next))}}
	}
	raw, _ := json.Marshal(resp)
	return json.Unmarshal(raw, out)
}

func lineStatuses(t *testing.T, db *sql.DB, workID int64) []string {
	t.Helper()
	out := []string{}
	for _, l := range must(workCommissions(db, pid, workID)) {
		out = append(out, l.Kind+":"+l.Status)
	}
	return out
}

// Both modes in one project: a resale cleaner and a commission partner.
func TestCommissionAndResaleSideBySide(t *testing.T) {
	db := testDB(t)
	fixClock(t, testNow)
	install(t, db, "cleaning")
	ana := must(upsertResource(db, pid, map[string]any{"kind": "person", "name": "Ana", "quals": []any{"cleaner"}}))
	partner := must(upsertResource(db, pid, map[string]any{"kind": "person", "name": "PartnerCo", "quals": []any{"cleaner"},
		"settlement": map[string]any{"mode": "commission", "commission_pct": 20, "commission_fixed_cents": 500, "billing_email": "ap@partner.example"}}))
	if partner.Settlement.Mode != modeCommission || partner.Settlement.CommissionBase != "net" || partner.Settlement.InvoiceCadence != "per_job" {
		t.Fatalf("terms not normalised: %+v", partner.Settlement)
	}
	if ana.Settlement.Mode != modeResale {
		t.Fatalf("default must be resale: %+v", ana.Settlement)
	}
	if _, err := upsertResource(db, pid, map[string]any{"id": float64(ana.ID), "settlement": map[string]any{"mode": "commission"}}); err == nil {
		t.Fatal("commission without pct or fixed must be refused")
	}
	shift(t, db, ana.ID, "2026-11-16T08:00:00Z", "2026-11-16T18:00:00Z")
	shift(t, db, partner.ID, "2026-11-16T08:00:00Z", "2026-11-16T18:00:00Z")

	// The partner's own price for the item; its cost would fail the
	// resale margin check, which must not apply to commission jobs.
	ri := must(getRateItem(db, pid, 0, "REGULAR_CLEAN", 0))
	ri.ResourceID, ri.Pricing.UnitCents, ri.Pricing.MinChargeCents, ri.CostCents = partner.ID, 3500, 0, 3400
	if err := upsertRateItem(db, pid, *ri); err != nil {
		t.Fatal(err)
	}
	if shared := must(getRateItem(db, pid, 0, "REGULAR_CLEAN", 0)); shared.Pricing.UnitCents != 2800 {
		t.Fatalf("shared price changed: %d", shared.Pricing.UnitCents)
	}

	// Commission job, partner known at intake.
	in := must(intakeWork(db, pid, nil, map[string]any{"work_type": "regular_clean", "address": "2 rue B", "partner_resource_id": float64(partner.ID),
		"answers": map[string]any{"property_type": "flat", "hours": 3}, "items": []any{"REGULAR_CLEAN"}}))
	q := in.Quote
	if q.TotalCents != 10500 || q.SettlementMode != modeCommission || q.MarginCheck != modeCommission || q.MarginPct != nil {
		t.Fatalf("partner quote: %+v", q)
	}
	if q.ExpectedCommissionCents == nil || *q.ExpectedCommissionCents != 2600 {
		t.Fatalf("expected commission = %v", q.ExpectedCommissionCents)
	}
	for _, f := range q.Flags {
		if strings.HasPrefix(f, "below_min_margin") {
			t.Fatalf("margin check applied to a commission job: %v", q.Flags)
		}
	}
	w := in.Work
	if w.SettlementMode != modeCommission || w.Partner != "PartnerCo" || w.Settlement.CommissionPct != 20 {
		t.Fatalf("terms not copied: %+v", w)
	}
	// Resale preview of the same item with the partner's cost fails the margin.
	resale := must(priceItems(db, pid, []ItemRequest{{Code: "REGULAR_CLEAN", Qty: ptr(3)}}, PriceContext{ResourceID: partner.ID, SettlementMode: modeResale}))
	if resale.MarginCheck != "margin" || !resale.NeedsOwner {
		t.Fatalf("resale with partner cost should hit min margin: %+v", resale.Flags)
	}

	br := must(bookSlot(db, pid, w, map[string]any{"starts_at": "2026-11-16T10:00:00Z", "resource_ids": ids(partner.ID)}))
	must(submitForm(db, pid, map[string]any{"form_key": "clean_checklist", "work_id": float64(w.ID),
		"data": map[string]any{"kitchen": true, "bathrooms": true, "floors": true, "bins": true}}))
	cw := must(completeWork(db, pid, br.Work, map[string]any{})).Work
	lines := must(workCommissions(db, pid, w.ID))
	if len(lines) != 1 || lines[0].AmountCents != 2600 || lines[0].BaseSource != "actual_lines" || lines[0].Status != "accrued" {
		t.Fatalf("accrued: %+v", lines)
	}

	// The brand does not invoice the client: the partner's invoice is recorded.
	if _, _, err := invoiceWork(db, pid, nil, cw, map[string]any{}); err == nil || !strings.Contains(err.Error(), "external_ref") {
		t.Fatalf("want contractor-invoice error, got %v", err)
	}
	iw, _ := must2(invoiceWork(db, pid, nil, cw, map[string]any{"external_ref": "PC-77", "amount_net_cents": float64(12000), "amount_gross_cents": float64(14400)}))
	if iw.Status != "invoiced" || iw.InvoiceRef != "partner:PC-77" || iw.InvoicedCents != 0 || iw.PartnerInvoice.IssuedBy != "PartnerCo" {
		t.Fatalf("partner invoice: %+v %+v", iw, iw.PartnerInvoice)
	}
	lines = must(workCommissions(db, pid, w.ID))
	if len(lines) != 1 || lines[0].AmountCents != 2900 || lines[0].BaseSource != "partner_invoice_net" {
		t.Fatalf("recomputed on the partner invoice: %+v", lines)
	}

	// Invoice the commission through Billing, once.
	stub := &billingStub{}
	res := must(invoiceCommissions(db, pid, stub.call, map[string]any{"resource_id": float64(partner.ID)}))
	invs := res["invoices"].([]map[string]any)
	if len(invs) != 1 || invs[0]["invoice_ref"] != "billing:101" || invs[0]["total_cents"].(int64) != 2900 {
		t.Fatalf("commission invoice: %+v", res)
	}
	if len(stub.calls) != 2 || stub.calls[0]["input"].(map[string]any)["email"] != "ap@partner.example" {
		t.Fatalf("billing calls: %+v", stub.calls)
	}
	res = must(invoiceCommissions(db, pid, stub.call, map[string]any{"resource_id": float64(partner.ID)}))
	if len(res["invoices"].([]map[string]any)) != 0 || len(stub.calls) != 2 {
		t.Fatalf("second run billed again: %+v", res)
	}

	// The partner corrects their invoice: an adjustment for the difference.
	iw, _ = must2(invoiceWork(db, pid, nil, iw, map[string]any{"external_ref": "PC-77", "amount_net_cents": float64(13000)}))
	got := lineStatuses(t, db, w.ID)
	if strings.Join(got, ",") != "commission:invoiced,adjustment:accrued" {
		t.Fatalf("lines after correction: %v", got)
	}
	if adj := must(workCommissions(db, pid, w.ID))[1]; adj.AmountCents != 200 {
		t.Fatalf("adjustment = %d", adj.AmountCents)
	}
	paid := must(setCommissionStatus(db, pid, map[string]any{"invoice_ref": "billing:101"}, "paid"))
	if len(paid) != 1 || paid[0].Status != "paid" {
		t.Fatalf("mark paid: %+v", paid)
	}
	if _, err := setCommissionStatus(db, pid, map[string]any{"line_id": float64(paid[0].ID), "reason": "x"}, "void"); err == nil {
		t.Fatal("a paid line cannot be voided")
	}

	// Client paid the partner: the job closes.
	must(0, confirmClientPaid(db, pid, iw, "partner email", "owner"))
	iw = must(getWork(db, pid, w.ID))
	if must(transition(db, pid, iw, "closed", "owner", "paid", nil)).Work.Phase != "closed" {
		t.Fatal("commission job should close once its partner invoice is recorded")
	}

	// Resale job with Ana: unchanged behaviour.
	rw := cleanJob(t, db, nil, ana.ID, "2026-11-16T14:00:00Z")
	if rw.SettlementMode != modeResale || rw.PartnerResourceID == nil || *rw.PartnerResourceID != ana.ID {
		t.Fatalf("booking should assign Ana in resale: %+v", rw)
	}
	rw, info := must2(invoiceWork(db, pid, nil, rw, map[string]any{"external_ref": "INV-9"}))
	if rw.InvoicedCents != 8400 || info["total_cents"].(int64) != 8400 || len(must(workCommissions(db, pid, rw.ID))) != 0 {
		t.Fatalf("resale invoice: %+v %v", rw, info)
	}

	m := must(metrics(db, pid, testNow.AddDate(0, 0, -1), testNow.AddDate(0, 0, 1), 0))
	if m["invoiced_cents"].(int64) != 8400 || m["gmv_cents"].(int64) != 8400+13000 || m["revenue_cents"].(int64) != 8400+3100 {
		t.Fatalf("metrics: invoiced %v gmv %v revenue %v", m["invoiced_cents"], m["gmv_cents"], m["revenue_cents"])
	}
	c := m["commissions"].(map[string]any)
	if c["paid_cents"].(int64) != 2900 || c["accrued_cents"].(int64) != 200 {
		t.Fatalf("commissions: %+v", c)
	}
	bc := m["by_contractor"].([]map[string]any)
	if len(bc) != 2 {
		t.Fatalf("by_contractor: %+v", bc)
	}
}

func TestCommissionMonthlyCancelAndVoid(t *testing.T) {
	db := testDB(t)
	fixClock(t, testNow)
	install(t, db, "cleaning")
	p := must(upsertResource(db, pid, map[string]any{"kind": "person", "name": "Monthly Ltd", "quals": []any{"cleaner"},
		"settlement": map[string]any{"mode": "commission", "commission_pct": 10, "invoice_cadence": "monthly"}}))
	shift(t, db, p.ID, "2026-11-16T08:00:00Z", "2026-11-16T18:00:00Z")

	// Cancelled before anything is invoiced: the line is voided.
	w1 := cleanJob(t, db, nil, p.ID, "2026-11-16T09:00:00Z")
	if s := lineStatuses(t, db, w1.ID); len(s) != 1 || s[0] != "commission:accrued" {
		t.Fatalf("w1 lines %v", s)
	}
	must(transition(db, pid, w1, "cancelled", "owner", "client disputed", nil))
	if s := lineStatuses(t, db, w1.ID); s[0] != "commission:void" {
		t.Fatalf("w1 after cancel %v", s)
	}

	// Monthly lines wait for the month to end.
	w2 := cleanJob(t, db, nil, p.ID, "2026-11-16T13:00:00Z")
	res := must(invoiceCommissions(db, pid, nil, map[string]any{"resource_id": float64(p.ID), "external_ref": "EXT-1"}))
	if len(res["invoices"].([]map[string]any)) != 0 || len(res["skipped"].([]map[string]any)) != 1 {
		t.Fatalf("monthly line invoiced early: %+v", res)
	}
	fixClock(t, testNow.AddDate(0, 0, 16)) // 2 December
	res = must(invoiceCommissions(db, pid, nil, map[string]any{"resource_id": float64(p.ID), "external_ref": "EXT-1"}))
	if inv := res["invoices"].([]map[string]any); len(inv) != 1 || inv[0]["total_cents"].(int64) != 840 {
		t.Fatalf("monthly invoice: %+v", res)
	}
	if _, err := setCommissionStatus(db, pid, map[string]any{"invoice_ref": "EXT-1"}, "paid"); err != nil {
		t.Fatal(err)
	}

	// Cancelled after the commission was invoiced: a credit adjustment.
	w2 = must(getWork(db, pid, w2.ID))
	must(transition(db, pid, w2, "cancelled", "owner", "refunded", nil))
	cl := must(workCommissions(db, pid, w2.ID))
	if len(cl) != 2 || cl[1].Kind != "adjustment" || cl[1].AmountCents != -840 || cl[1].Status != "accrued" {
		t.Fatalf("credit adjustment: %+v", cl)
	}
	res = must(invoiceCommissions(db, pid, (&billingStub{}).call, map[string]any{"resource_id": float64(p.ID), "due_only": false}))
	if inv := res["invoices"].([]map[string]any); len(inv) != 1 || inv[0]["error"] == nil {
		t.Fatalf("a negative total must not be billed: %+v", res)
	}
	if _, err := setCommissionStatus(db, pid, map[string]any{"line_id": float64(cl[1].ID)}, "void"); err == nil {
		t.Fatal("void needs a reason")
	}
	v := must(setCommissionStatus(db, pid, map[string]any{"line_id": float64(cl[1].ID), "reason": "credit issued by hand"}, "void"))
	if v[0].Status != "void" {
		t.Fatalf("void: %+v", v)
	}
	list := must(listCommissions(db, pid, map[string]any{"resource_id": float64(p.ID)}))
	if list["totals"].(map[string]int64)["paid"] != 840 {
		t.Fatalf("totals: %+v", list["totals"])
	}
}

func TestBrandTermsCopiedAndFrozen(t *testing.T) {
	db := testDB(t)
	fixClock(t, testNow)
	install(t, db, "cleaning")
	a := must(upsertBrand(db, pid, map[string]any{"key": "alpha", "name": "Alpha", "number_prefix": "AL"}))
	must(upsertBrand(db, pid, map[string]any{"key": "beta", "name": "Beta", "number_prefix": "BE"}))
	r := must(upsertResource(db, pid, map[string]any{"kind": "person", "name": "Duo", "quals": []any{"cleaner"},
		"settlement_by_brand": map[string]any{"alpha": map[string]any{"mode": "commission", "commission_pct": 15}}}))
	withBrandKeys(db, pid, r)
	if r.Settlement.Mode != modeResale || r.SettlementByBrand["alpha"].CommissionPct != 15 {
		t.Fatalf("per-brand terms: %+v", r)
	}
	intake := func(brand string) *Work {
		return must(intakeWork(db, pid, nil, map[string]any{"work_type": "regular_clean", "brand": brand, "address": "x", "partner_resource_id": float64(r.ID),
			"answers": map[string]any{"property_type": "flat", "hours": 2}})).Work
	}
	wa, wb := intake("alpha"), intake("beta")
	if wa.SettlementMode != modeCommission || wb.SettlementMode != modeResale {
		t.Fatalf("alpha %s, beta %s", wa.SettlementMode, wb.SettlementMode)
	}
	// Changing the resource's terms later leaves assigned jobs alone.
	must(upsertResource(db, pid, map[string]any{"id": float64(r.ID), "settlement_by_brand": map[string]any{"alpha": map[string]any{"mode": "commission", "commission_pct": 30}}}))
	if w := must(getWork(db, pid, wa.ID)); w.Settlement.CommissionPct != 15 {
		t.Fatalf("job terms changed with the resource: %+v", w.Settlement)
	}
	// Removing the override.
	r = must(upsertResource(db, pid, map[string]any{"id": float64(r.ID), "settlement_by_brand": map[string]any{"alpha": nil}}))
	if r.termsFor(a.ID).Mode != modeResale {
		t.Fatal("override not removed")
	}

	// min_commission_cents sends small commission quotes to the owner.
	if err := setPolicy(db, pid, 0, "min_commission_cents", 2000); err != nil {
		t.Fatal(err)
	}
	pr := must(priceItems(db, pid, []ItemRequest{{Code: "REGULAR_CLEAN", Qty: ptr(2)}}, PriceContext{SettlementMode: modeCommission,
		Terms: Settlement{Mode: modeCommission, CommissionPct: 15}}))
	if !pr.NeedsOwner || pr.Flags[0] != "below_min_commission:2000" {
		t.Fatalf("flags: %v", pr.Flags)
	}

	// Done work keeps its contractor.
	if _, err := db.Exec(`UPDATE work SET phase='bill', status='to_invoice' WHERE id=?`, wb.ID); err != nil {
		t.Fatal(err)
	}
	other := must(upsertResource(db, pid, map[string]any{"kind": "person", "name": "Other", "quals": []any{"cleaner"}}))
	if _, err := assignPartner(db, pid, must(getWork(db, pid, wb.ID)), other.ID, "owner", ""); err == nil {
		t.Fatal("assigning on done work must be refused")
	}
}

func TestCommissionAutoClose(t *testing.T) {
	db := testDB(t)
	fixClock(t, testNow)
	install(t, db, "cleaning")
	p := must(upsertResource(db, pid, map[string]any{"kind": "person", "name": "P", "quals": []any{"cleaner"},
		"settlement": map[string]any{"mode": "commission", "commission_fixed_cents": 1000}}))
	shift(t, db, p.ID, "2026-11-16T08:00:00Z", "2026-11-16T18:00:00Z")
	w := cleanJob(t, db, nil, p.ID, "2026-11-16T10:00:00Z")
	must2(invoiceWork(db, pid, nil, w, map[string]any{"external_ref": "P-1"}))
	if n := must(autoCloseCommissionJobs(db, nil)); n != 0 {
		t.Fatal("no policy: nothing closes")
	}
	if err := setPolicy(db, pid, 0, "commission_autoclose_days", 7); err != nil {
		t.Fatal(err)
	}
	fixClock(t, testNow.AddDate(0, 0, 3))
	if n := must(autoCloseCommissionJobs(db, nil)); n != 0 {
		t.Fatal("closed too early")
	}
	fixClock(t, testNow.AddDate(0, 0, 8))
	if n := must(autoCloseCommissionJobs(db, nil)); n != 1 {
		t.Fatal("should close after 7 days")
	}
	w = must(getWork(db, pid, w.ID))
	if w.Phase != "closed" || w.PartnerInvoice.ClientPaidConfirmedBy != "auto" {
		t.Fatalf("auto-closed: %s %+v", w.Phase, w.PartnerInvoice)
	}
	if l := must(workCommissions(db, pid, w.ID)); len(l) != 1 || l[0].AmountCents != 1000 {
		t.Fatalf("fixed commission: %+v", l)
	}
}
