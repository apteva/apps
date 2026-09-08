package main

import (
	"encoding/json"
	"errors"
	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
	"strings"
	"sync"
	"testing"
	"time"
)

type financialPlatform struct {
	tk.BasePlatformClient
	bound   int64
	fail    bool
	lost    bool
	creates int
	calls   []string
	bills   map[string]*obligationBill
}

func (p *financialPlatform) WhoAmI() (*sdk.InstallIdentity, error) {
	return &sdk.InstallIdentity{InstallID: 45, Bindings: map[string]any{"bills": p.bound, "storage": 6}}, nil
}
func (p *financialPlatform) CallAppResult(app, tool string, args map[string]any, out any) error {
	p.calls = append(p.calls, tool)
	if p.fail {
		return errors.New("Bills offline")
	}
	var value any
	switch tool {
	case "vendors_upsert_by_email":
		value = map[string]any{"vendor": map[string]any{"id": 901}}
	case "bills_create_obligation":
		if _, ok := args["paid"]; ok {
			panic("paid-on-create not allowed")
		}
		key := strArg(args, "source_key")
		if p.bills == nil {
			p.bills = map[string]*obligationBill{}
		}
		b := p.bills[key]
		if b == nil {
			p.creates++
			var total int64
			for _, l := range args["line_items"].([]any) {
				total += int64Cast(l.(map[string]any)["unit_price_cents"])
			}
			b = &obligationBill{ID: int64(900 + p.creates), SourceKey: key, Status: "received", Currency: strArg(args, "currency"), Total: total, Outstanding: total}
			p.bills[key] = b
		}
		value = map[string]any{"bill": b}
		if p.lost {
			p.lost = false
			return errors.New("lost response after commit")
		}
	case "bills_get_obligation":
		value = map[string]any{"bill": p.bills[strArg(args, "source_key")]}
	default:
		return errors.New("unexpected call " + app + "/" + tool)
	}
	raw, _ := json.Marshal(value)
	return json.Unmarshal(raw, out)
}
func financeTest(t *testing.T, bound int64) (*sdk.AppCtx, *financialPlatform, int64) {
	t.Helper()
	p := &financialPlatform{bound: bound}
	ctx := marketplaceCtx(t, p)
	gid := seedGig(t, ctx, "project-a", "reviewed", `{"type":"object"}`)
	return ctx, p, gid
}
func saveFinanceAgreement(t *testing.T, ctx *sdk.AppCtx, gid int64) *financialAgreement {
	t.Helper()
	out, e := (&App{}).toolGigsAgreement(ctx, map[string]any{"gig_id": gid, "model": "rate", "settlement": "supplier", "rate_minor": int64(2500), "quantity": "2.5", "unit": "hours", "currency": "EUR", "payee_name": "Supplier", "payee_email": "supplier@example.test", "confirmed_at": time.Now().UTC().Format(time.RFC3339), "evidence": "Written agreement"})
	if e != nil {
		t.Fatal(e)
	}
	v := out.(map[string]any)["agreement"].(financialAgreement)
	return &v
}
func approvalArgs(gid int64, a *financialAgreement) map[string]any {
	return map[string]any{"gig_id": gid, "agreement_id": a.ID, "request_key": "approve-1", "accepted_quantity": "2", "work_amount_minor": int64(5000), "expenses": []any{map[string]any{"description": "Travel", "amount_minor": int64(300)}}}
}
func TestFinancialsUnknownAndOptionalBills(t *testing.T) {
	ctx, p, gid := financeTest(t, 0)
	f, e := loadFinancials(ctx, "project-a", gid)
	if e != nil || len(f.Agreements) != 0 || len(f.Obligations) != 0 {
		t.Fatalf("%+v %v", f, e)
	}
	a := saveFinanceAgreement(t, ctx, gid)
	if *a.AmountMinor != 6250 {
		t.Fatal(a)
	}
	args := approvalArgs(gid, a)
	if _, e = (&App{}).toolGigsApproveCompensation(ctx, args); e != nil {
		t.Fatal(e)
	}
	f, e = loadFinancials(ctx, "project-a", gid)
	if e != nil || len(f.Obligations) != 1 || f.Obligations[0].TotalMinor != 5300 || f.Obligations[0].Bill != nil || f.Obligations[0].SyncStatus != "not_connected" || p.creates != 0 {
		t.Fatalf("%+v %v", f, e)
	}
	if _, e = (&App{}).toolGigsApproveCompensation(ctx, args); e != nil {
		t.Fatal(e)
	}
	args["work_amount_minor"] = 6000
	if _, e = (&App{}).toolGigsApproveCompensation(ctx, args); e == nil {
		t.Fatal("conflicting replay accepted")
	}
}
func TestFinancialsLostResponseAndPartialPayment(t *testing.T) {
	ctx, p, gid := financeTest(t, 226)
	p.lost = true
	a := saveFinanceAgreement(t, ctx, gid)
	if _, e := (&App{}).toolGigsApproveCompensation(ctx, approvalArgs(gid, a)); e != nil {
		t.Fatal(e)
	}
	f, _ := loadFinancials(ctx, "project-a", gid)
	o := f.Obligations[0]
	if o.SyncStatus != "pending" || p.creates != 1 {
		t.Fatal(o, p.creates)
	}
	if e := syncFinancialObligation(ctx, "project-a", o.ID); e != nil {
		t.Fatal(e)
	}
	if p.creates != 1 {
		t.Fatal("duplicate bill")
	}
	for _, b := range p.bills {
		b.Paid = 1000
		b.Outstanding = b.Total - 1000
		b.Status = "approved"
	}
	if e := syncFinancialObligation(ctx, "project-a", o.ID); e != nil {
		t.Fatal(e)
	}
	f, _ = loadFinancials(ctx, "project-a", gid)
	if f.Obligations[0].Bill.Paid != 1000 || f.Obligations[0].Bill.Outstanding != 4300 {
		t.Fatal(f.Obligations)
	}
	p.bound = 999
	before := len(p.calls)
	if e := syncFinancialObligation(ctx, "project-a", o.ID); e == nil {
		t.Fatal("rebound bill was redirected")
	}
	if len(p.calls) != before {
		t.Fatal("called wrong Bills installation")
	}
}
func TestFinancialsApprovalGuardsAndHistory(t *testing.T) {
	ctx, _, gid := financeTest(t, 0)
	a := saveFinanceAgreement(t, ctx, gid)
	args := approvalArgs(gid, a)
	ctx.AppDB().Exec(`UPDATE gigs SET status='submitted' WHERE id=?`, gid)
	if _, e := (&App{}).toolGigsApproveCompensation(ctx, args); e == nil {
		t.Fatal("submitted work approved financially")
	}
	ctx.AppDB().Exec(`UPDATE gigs SET status='reviewed' WHERE id=?`, gid)
	args["allocations"] = []any{map[string]any{"kind": "cost_center", "reference": "A", "amount_minor": 2000}}
	if _, e := (&App{}).toolGigsApproveCompensation(ctx, args); e == nil {
		t.Fatal("partial allocation accepted")
	}
	args["allocations"] = []any{map[string]any{"kind": "cost_center", "reference": "A", "amount_minor": 2000}, map[string]any{"kind": "cost_center", "reference": "B", "amount_minor": 3300}}
	args["delivered_files"] = []any{map[string]any{"file_id": 999, "install_id": 6}}
	if _, e := (&App{}).toolGigsApproveCompensation(ctx, args); e == nil {
		t.Fatal("forged file accepted")
	}
	delete(args, "delivered_files")
	if _, e := (&App{}).toolGigsApproveCompensation(ctx, args); e != nil {
		t.Fatal(e)
	}
	if _, e := ctx.AppDB().Exec(`UPDATE gig_agreements SET snapshot_json='{}' WHERE id=?`, a.ID); e == nil {
		t.Fatal("agreement history mutable")
	}
	if _, e := ctx.AppDB().Exec(`UPDATE gig_obligations SET snapshot_json='{}' WHERE gig_id=?`, gid); e == nil {
		t.Fatal("approval mutable")
	}
	if _, e := (&App{}).toolGigsAgreement(ctx, map[string]any{"gig_id": gid, "expected_revision": 0, "model": "fixed", "settlement": "other"}); e == nil {
		t.Fatal("stale revision accepted")
	}
	out, e := (&App{}).toolGigsAgreement(ctx, map[string]any{"gig_id": gid, "expected_revision": 1, "model": "fixed", "settlement": "other", "reason": "New scope"})
	if e != nil {
		t.Fatal(e)
	}
	if out.(map[string]any)["agreement"].(financialAgreement).AmountMinor != nil {
		t.Fatal("unknown became zero")
	}
	f, _ := loadFinancials(ctx, "project-a", gid)
	if len(f.Agreements) != 2 || len(f.Obligations) != 1 || f.Obligations[0].Agreement.ID != a.ID {
		t.Fatal("history overwritten")
	}
	if _, e := loadFinancials(ctx, "other-project", gid); e == nil {
		t.Fatal("cross project read")
	}
}
func TestFinancialsConcurrentApproval(t *testing.T) {
	ctx, _, gid := financeTest(t, 0)
	a := saveFinanceAgreement(t, ctx, gid)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = (&App{}).toolGigsApproveCompensation(ctx, approvalArgs(gid, a)) }()
	}
	wg.Wait()
	var n int
	ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM gig_obligations WHERE gig_id=?`, gid).Scan(&n)
	if n != 1 {
		t.Fatalf("%d obligations", n)
	}
}
func TestFinancialDecimalAndUnknown(t *testing.T) {
	for _, tc := range []struct {
		rate int64
		q    string
		want int64
	}{{25, "0.5", 13}, {199, "2.5", 498}, {100, "0", 0}} {
		got, e := financialMultiply(tc.rate, tc.q)
		if e != nil || got != tc.want {
			t.Fatal(tc, got, e)
		}
	}
	for _, q := range []string{"-1", "1/2", "1e4", "NaN", "99999999999999999999"} {
		if _, e := financialMultiply(100, q); e == nil {
			t.Fatal(q)
		}
	}
	ctx, _, gid := financeTest(t, 0)
	a := saveFinanceAgreement(t, ctx, gid)
	args := approvalArgs(gid, a)
	delete(args, "work_amount_minor")
	if _, e := (&App{}).toolGigsApproveCompensation(ctx, args); e == nil || !strings.Contains(e.Error(), "explicit") {
		t.Fatal(e)
	}
}

func TestFinancialCreditAndFacts(t *testing.T) {
	ctx, _, gid := financeTest(t, 0)
	a := saveFinanceAgreement(t, ctx, gid)
	if _, e := (&App{}).toolGigsApproveCompensation(ctx, approvalArgs(gid, a)); e != nil {
		t.Fatal(e)
	}
	f, _ := loadFinancials(ctx, "project-a", gid)
	args := map[string]any{"gig_id": gid, "agreement_id": a.ID, "request_key": "credit", "kind": "credit", "parent_id": f.Obligations[0].ID, "accepted_quantity": "0", "work_amount_minor": int64(1000), "reason": "Reduced scope"}
	if _, e := (&App{}).toolGigsApproveCompensation(ctx, args); e != nil {
		t.Fatal(e)
	}
	args["request_key"] = "overcredit"
	args["work_amount_minor"] = int64(5000)
	if _, e := (&App{}).toolGigsApproveCompensation(ctx, args); e == nil {
		t.Fatal("overcredit accepted")
	}
	out, e := (&App{}).toolGigsFinancialFacts(ctx, map[string]any{})
	if e != nil {
		t.Fatal(e)
	}
	facts := out.(map[string]any)["facts"].([]map[string]any)
	var sum int64
	for _, f := range facts {
		sum += f["approved_amount_minor"].(int64)
		if f["payment_amount_minor"] != nil {
			t.Fatal("disconnected payment assumed zero")
		}
	}
	if sum != 4300 {
		t.Fatal(sum)
	}
}
func TestFinancialKnownZeroAndNullExpense(t *testing.T) {
	ctx, p, gid := financeTest(t, 226)
	a := saveFinanceAgreement(t, ctx, gid)
	args := approvalArgs(gid, a)
	args["expenses"] = []any{map[string]any{"description": "Unknown receipt", "amount_minor": nil}}
	if _, e := (&App{}).toolGigsApproveCompensation(ctx, args); e == nil {
		t.Fatal("null expense became zero")
	}
	args["expenses"] = []any{}
	args["work_amount_minor"] = 0
	args["accepted_quantity"] = "0"
	if _, e := (&App{}).toolGigsApproveCompensation(ctx, args); e != nil {
		t.Fatal(e)
	}
	f, _ := loadFinancials(ctx, "project-a", gid)
	if f.Obligations[0].TotalMinor != 0 || f.Obligations[0].SyncStatus != "not_applicable" || p.creates != 0 {
		t.Fatal(f)
	}
}
