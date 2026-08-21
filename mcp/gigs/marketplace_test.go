package main

import (
	"encoding/json"
	"fmt"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type marketplacePlatformStub struct {
	tk.BasePlatformClient
	calls       []string
	nextProduct int64
	nextPrice   int64
}

func (p *marketplacePlatformStub) CallAppResult(app, tool string, input map[string]any, out any) error {
	p.calls = append(p.calls, app+"/"+tool)
	var payload any
	switch app + "/" + tool {
	case "catalog/catalog_products_create":
		if p.nextProduct == 0 {
			p.nextProduct = 701
		}
		payload = map[string]any{"product": map[string]any{"id": p.nextProduct}}
	case "catalog/catalog_products_update":
		payload = map[string]any{"product": map[string]any{"id": int64Cast(input["id"])}}
	case "catalog/catalog_prices_create":
		p.nextPrice++
		if p.nextPrice < 801 {
			p.nextPrice = 801
		}
		payload = map[string]any{"price": map[string]any{"id": p.nextPrice, "product_id": input["product_id"], "unit_amount_cents": input["unit_amount_cents"], "currency": input["currency"], "active": true}}
	case "catalog/catalog_prices_get":
		return fmt.Errorf("not found")
	case "catalog/catalog_prices_archive":
		payload = map[string]any{"price": map[string]any{"id": input["id"]}}
	case "crm/contacts_get":
		payload = map[string]any{"found": true, "contact": map[string]any{"id": 88, "display_name": "Ana Standard", "primary_email": "ana@example.test"}}
	case "bills/vendors_upsert_by_email":
		payload = map[string]any{"vendor": map[string]any{"id": 901}}
	case "bills/bills_create":
		payload = map[string]any{"bill": map[string]any{"id": 902, "status": "received", "total_cents": 12500}}
	case "bills/bills_get":
		payload = map[string]any{"bill": map[string]any{"id": 902, "status": "received", "total_cents": 12500}}
	default:
		payload = map[string]any{}
	}
	raw, _ := json.Marshal(payload)
	return json.Unmarshal(raw, out)
}

func marketplaceCtx(t *testing.T, platform sdk.PlatformClient) *sdk.AppCtx {
	t.Helper()
	return tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-a"), tk.WithPlatform(platform))
}

func seedPublishedTemplate(t *testing.T, ctx *sdk.AppCtx) (int64, int64) {
	t.Helper()
	res, err := ctx.AppDB().Exec(`INSERT INTO templates(project_id,slug,name,kind) VALUES('project-a','voice-recording','Voice recording','action')`)
	if err != nil {
		t.Fatal(err)
	}
	tid, _ := res.LastInsertId()
	res, err = ctx.AppDB().Exec(`INSERT INTO template_versions(template_id,version,status,title_template) VALUES(?,1,'active','Record {{quantity}} prompts')`, tid)
	if err != nil {
		t.Fatal(err)
	}
	vid, _ := res.LastInsertId()
	if _, err = ctx.AppDB().Exec(`UPDATE templates SET current_version_id=? WHERE id=?`, vid, tid); err != nil {
		t.Fatal(err)
	}
	return tid, vid
}

func TestRateResolutionSpecificityAndGigSnapshot(t *testing.T) {
	ctx := marketplaceCtx(t, &marketplacePlatformStub{})
	workerID := seedWorker(t, ctx, "project-a", 88)
	templateID, _ := seedPublishedTemplate(t, ctx)
	created, err := (&App{}).toolPayGradesCreate(ctx, map[string]any{"_project_id": "project-a", "name": "Standard", "slug": "standard", "rank": 2, "default_pricing_model": "fixed", "default_amount_minor": 5000, "currency": "EUR"})
	if err != nil {
		t.Fatal(err)
	}
	grade := created.(map[string]any)["pay_grade"].(*payGrade)
	if _, err = (&App{}).toolWorkersSetPayGrade(ctx, map[string]any{"_project_id": "project-a", "worker_id": workerID, "pay_grade_id": grade.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err = (&App{}).toolRatesSet(ctx, map[string]any{"_project_id": "project-a", "pay_grade_id": grade.ID, "template_id": templateID, "pricing_model": "fixed", "amount_minor": 9000, "currency": "EUR"}); err != nil {
		t.Fatal(err)
	}
	if _, err = (&App{}).toolRatesSet(ctx, map[string]any{"_project_id": "project-a", "worker_id": workerID, "template_id": templateID, "pricing_model": "fixed", "amount_minor": 12500, "currency": "EUR"}); err != nil {
		t.Fatal(err)
	}
	quote, err := resolveRate(ctx.AppDB(), "project-a", templateID, 0, workerID, 1, "EUR")
	if err != nil {
		t.Fatal(err)
	}
	if !quote.Configured || quote.WorkerAmountMinor != 12500 || quote.Source != "worker_template_override" {
		t.Fatalf("quote=%+v", quote)
	}
	g, _, err := createGig(ctx, "project-a", createOpts{Title: "Paid work", Derived: derivedComposition{}, WorkerID: workerID, Compensation: quote})
	if err != nil {
		t.Fatal(err)
	}
	if g.Compensation == nil || g.Compensation.WorkerAmountMinor != 12500 {
		t.Fatalf("compensation=%+v", g.Compensation)
	}
	if _, err = (&App{}).toolRatesSet(ctx, map[string]any{"_project_id": "project-a", "worker_id": workerID, "template_id": templateID, "pricing_model": "fixed", "amount_minor": 15000, "currency": "EUR"}); err != nil {
		t.Fatal(err)
	}
	stored, err := loadGigCompensation(ctx.AppDB(), "project-a", g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.WorkerAmountMinor != 12500 {
		t.Fatalf("historical snapshot changed: %+v", stored)
	}
}

func TestOfferCatalogPublicationAndRecommendation(t *testing.T) {
	platform := &marketplacePlatformStub{}
	ctx := marketplaceCtx(t, platform)
	workerID := seedWorker(t, ctx, "project-a", 88)
	templateID, _ := seedPublishedTemplate(t, ctx)
	gradeOut, err := (&App{}).toolPayGradesCreate(ctx, map[string]any{"_project_id": "project-a", "name": "Senior", "default_pricing_model": "fixed", "default_amount_minor": 12500, "currency": "EUR"})
	if err != nil {
		t.Fatal(err)
	}
	grade := gradeOut.(map[string]any)["pay_grade"].(*payGrade)
	_, _ = (&App{}).toolWorkersSetPayGrade(ctx, map[string]any{"_project_id": "project-a", "worker_id": workerID, "pay_grade_id": grade.ID})
	offerOut, err := (&App{}).toolOffersCreate(ctx, map[string]any{"_project_id": "project-a", "template_id": templateID, "name": "Voice recording", "visibility": "public"})
	if err != nil {
		t.Fatal(err)
	}
	offer := offerOut.(map[string]any)["offer"].(*standardOffer)
	setOut, err := (&App{}).toolOfferPackagesSet(ctx, map[string]any{"_project_id": "project-a", "offer_id": offer.ID, "packages": []any{map[string]any{"name": "Standard", "slug": "standard", "tier": "standard", "pricing_model": "fixed", "quantity": 100, "unit": "prompt", "delivery_days": 3, "revisions": 2, "customer_amount_minor": 20000, "currency": "EUR"}}})
	if err != nil {
		t.Fatal(err)
	}
	offer = setOut.(map[string]any)["offer"].(*standardOffer)
	pkg := offer.Packages[0]
	if _, err = (&App{}).toolRatesSet(ctx, map[string]any{"_project_id": "project-a", "pay_grade_id": grade.ID, "offer_package_id": pkg.ID, "pricing_model": "fixed", "amount_minor": 12500, "currency": "EUR"}); err != nil {
		t.Fatal(err)
	}
	pub, err := (&App{}).toolOffersPublish(ctx, map[string]any{"_project_id": "project-a", "id": offer.ID})
	if err != nil {
		t.Fatal(err)
	}
	offer = pub.(map[string]any)["offer"].(*standardOffer)
	if offer.CatalogProductID != 701 || offer.Packages[0].CatalogPriceID != 801 {
		t.Fatalf("catalog refs offer=%+v package=%+v", offer, offer.Packages[0])
	}
	rec, err := (&App{}).recommendOffer(ctx, "project-a", map[string]any{"offer_id": offer.ID, "package_slug": "standard", "worker_id": workerID, "quantity": 100})
	if err != nil {
		t.Fatal(err)
	}
	comp := rec["worker_compensation"].(*rateQuote)
	if comp.WorkerAmountMinor != 12500 || comp.CustomerAmountMinor != 20000 || rec["estimated_margin_minor"].(int64) != 7500 {
		t.Fatalf("recommendation=%+v", rec)
	}
	changed, err := (&App{}).toolOfferPackagesSet(ctx, map[string]any{"_project_id": "project-a", "offer_id": offer.ID, "packages": []any{map[string]any{"name": "Standard", "slug": "standard", "tier": "standard", "pricing_model": "fixed", "quantity": 100, "unit": "prompt", "delivery_days": 3, "revisions": 2, "customer_amount_minor": 22000, "currency": "EUR"}}})
	if err != nil {
		t.Fatal(err)
	}
	if changed.(map[string]any)["offer"].(*standardOffer).Status != "draft" {
		t.Fatal("editing a published package must require Catalog republishing")
	}
	republished, err := (&App{}).toolOffersPublish(ctx, map[string]any{"_project_id": "project-a", "id": offer.ID})
	if err != nil {
		t.Fatal(err)
	}
	offer = republished.(map[string]any)["offer"].(*standardOffer)
	if offer.Status != "active" || offer.Packages[0].CustomerAmountMinor != 22000 || offer.Packages[0].CatalogPriceID == 801 {
		t.Fatalf("republished offer=%+v", offer)
	}
}

func TestReviewedGigCreatesIdempotentBillsPayable(t *testing.T) {
	platform := &marketplacePlatformStub{}
	ctx := marketplaceCtx(t, platform)
	workerID := seedWorker(t, ctx, "project-a", 88)
	gigID := seedGig(t, ctx, "project-a", "reviewed", `{"type":"object","properties":{}}`)
	assignmentID := seedAssignment(t, ctx, gigID, workerID, "reviewed", "direct", "reviewed-token")
	_ = assignmentID
	if _, err := ctx.AppDB().Exec(`INSERT INTO gig_compensation(project_id,gig_id,worker_id,pricing_model,rate_amount_minor,quantity,worker_amount_minor,currency,rate_source) VALUES('project-a',?,?, 'fixed',12500,1,12500,'EUR','manual_test')`, gigID, workerID); err != nil {
		t.Fatal(err)
	}
	comp, bill, err := createGigPayable(ctx, "project-a", gigID)
	if err != nil {
		t.Fatal(err)
	}
	if bill == nil || bill.ID != 902 || comp.PayableStatus != "created" {
		t.Fatalf("comp=%+v bill=%+v", comp, bill)
	}
	before := len(platform.calls)
	_, bill, err = createGigPayable(ctx, "project-a", gigID)
	if err != nil {
		t.Fatal(err)
	}
	if bill.ID != 902 || len(platform.calls) != before {
		t.Fatalf("retry was not local/idempotent calls=%v", platform.calls)
	}
}

func TestJobProposalAcceptanceCreatesContractAndMilestones(t *testing.T) {
	ctx := marketplaceCtx(t, &marketplacePlatformStub{})
	workerID := seedWorker(t, ctx, "project-a", 88)
	templateID, _ := seedPublishedTemplate(t, ctx)
	jobOut, err := (&App{}).toolJobPostsCreate(ctx, map[string]any{
		"_project_id": "project-a", "title": "Record campaign", "template_id": templateID,
		"pricing_models": []any{"milestone"}, "budget_min_minor": 10000, "budget_max_minor": 30000,
		"currency": "EUR", "publish": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	job := jobOut.(map[string]any)["job_post"].(*jobPost)
	proposalOut, err := (&App{}).toolProposalsSubmit(ctx, map[string]any{
		"_project_id": "project-a", "job_post_id": job.ID, "worker_id": workerID,
		"pricing_model": "milestone", "amount_minor": 20000, "currency": "EUR",
		"milestones": []any{
			map[string]any{"title": "First batch", "worker_amount_minor": 9000, "currency": "EUR"},
			map[string]any{"title": "Final batch", "worker_amount_minor": 11000, "currency": "EUR"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	proposal := proposalOut.(map[string]any)["proposal"].(*proposal)
	accepted, err := (&App{}).toolProposalsAccept(ctx, map[string]any{"_project_id": "project-a", "id": proposal.ID})
	if err != nil {
		t.Fatal(err)
	}
	contract := accepted.(map[string]any)["contract"].(*contract)
	if contract.Status != "active" || contract.WorkerID != workerID || contract.WorkerAmountMinor != 20000 || len(contract.Milestones) != 2 {
		t.Fatalf("contract=%+v", contract)
	}
	updatedJob, err := getJobPost(ctx.AppDB(), "project-a", job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updatedJob.Status != "awarded" {
		t.Fatalf("job status=%s", updatedJob.Status)
	}

	first := contract.Milestones[0]
	dispatched, err := (&App{}).toolContractsDispatchMilestone(ctx, map[string]any{
		"_project_id": "project-a", "contract_id": contract.ID, "milestone_id": first.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	firstGig := dispatched.(map[string]any)["gig"].(*gig)
	if firstGig.Compensation == nil || firstGig.Compensation.ContractID != contract.ID || firstGig.Compensation.MilestoneID != first.ID || firstGig.Compensation.WorkerAmountMinor != 9000 {
		t.Fatalf("first milestone compensation=%+v", firstGig.Compensation)
	}
	if err = syncContractFromGig(ctx.AppDB(), "project-a", firstGig.ID, "submitted"); err != nil {
		t.Fatal(err)
	}
	if err = syncContractFromGig(ctx.AppDB(), "project-a", firstGig.ID, "reviewed"); err != nil {
		t.Fatal(err)
	}
	contract, err = loadContract(ctx.AppDB(), "project-a", contract.ID)
	if err != nil {
		t.Fatal(err)
	}
	if contract.Status != "active" || contract.Milestones[0].Status != "approved" {
		t.Fatalf("partially delivered contract=%+v", contract)
	}

	second := contract.Milestones[1]
	dispatched, err = (&App{}).toolContractsDispatchMilestone(ctx, map[string]any{
		"_project_id": "project-a", "contract_id": contract.ID, "milestone_id": second.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	secondGig := dispatched.(map[string]any)["gig"].(*gig)
	if err = syncContractFromGig(ctx.AppDB(), "project-a", secondGig.ID, "reviewed"); err != nil {
		t.Fatal(err)
	}
	contract, err = loadContract(ctx.AppDB(), "project-a", contract.ID)
	if err != nil {
		t.Fatal(err)
	}
	if contract.Status != "completed" || contract.Milestones[1].Status != "approved" {
		t.Fatalf("completed contract=%+v", contract)
	}
}
