package main

import (
	"encoding/json"
	"fmt"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type recordedCall struct {
	App   string
	Tool  string
	Input map[string]any
}

type platformStub struct {
	tk.BasePlatformClient
	calls []recordedCall
}

func (p *platformStub) CallAppResult(app, tool string, input map[string]any, out any) error {
	p.calls = append(p.calls, recordedCall{App: app, Tool: tool, Input: input})
	var payload any
	switch app + "/" + tool {
	case "web/web_search":
		payload = map[string]any{
			"count": 2,
			"results": []map[string]any{
				{"title": "Acme Cloud | Automation for SaaS", "url": "https://acme.example/about", "snippet": "B2B SaaS automation company based in Spain", "source": "google", "rank": 1, "fetched_at": "2026-08-14T08:00:00Z", "confidence": "medium"},
				{"title": "Beta Systems - Operations software", "url": "https://beta.example", "snippet": "Spanish operations software company", "source": "google", "rank": 2, "fetched_at": "2026-08-14T08:00:00Z", "confidence": "medium"},
			},
		}
	case "web/web_research":
		payload = map[string]any{
			"answer":     "Research completed.",
			"confidence": "medium",
			"citations": []map[string]any{
				{"id": 1, "title": "About Acme", "url": "https://acme.example/about", "excerpt": "Acme builds B2B SaaS automation products in Madrid."},
				{"id": 2, "title": "Acme Careers", "url": "https://acme.example/careers", "excerpt": "Acme is hiring an operations manager."},
			},
			"sources": []map[string]any{
				{"url": "https://acme.example/about", "title": "About Acme", "artifact": map[string]any{"id": 91}},
				{"url": "https://acme.example/careers", "title": "Acme Careers", "artifact": map[string]any{"id": 92}},
			},
		}
	case "crm/contacts_upsert_by_channel":
		payload = map[string]any{
			"contact":     map[string]any{"id": 701, "display_name": "Alex Rivera", "primary_email": input["value"]},
			"was_created": true,
		}
	case "crm/contacts_log_activity":
		payload = map[string]any{"activity": map[string]any{"id": 88}}
	default:
		return fmt.Errorf("unexpected app call %s/%s", app, tool)
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return json.Unmarshal(encoded, out)
}

var _ sdk.PlatformClient = (*platformStub)(nil)

func newTestContext(t *testing.T, platform sdk.PlatformClient) *sdk.AppCtx {
	t.Helper()
	return tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-a"), tk.WithPlatform(platform))
}

func testProfile(t *testing.T, ctx *sdk.AppCtx) *TargetProfile {
	t.Helper()
	profile, err := createProfile(ctx.AppDB(), ctx.CurrentProject(), map[string]any{
		"name":          "European SaaS",
		"industries":    []any{"B2B SaaS"},
		"locations":     []any{"Spain"},
		"target_titles": []any{"Head of Operations", "COO"},
		"keywords":      []any{"automation"},
	})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}
	return profile
}

func TestDiscoveryDeduplicatesAndHonorsExclusions(t *testing.T) {
	platform := &platformStub{}
	ctx := newTestContext(t, platform)
	profile := testProfile(t, ctx)

	first, err := runDiscovery(ctx, profile.ID, "", 10)
	if err != nil {
		t.Fatalf("first discovery: %v", err)
	}
	if got := first["created"].(int); got != 2 {
		t.Fatalf("created=%d, want 2", got)
	}
	candidates, total, err := listCandidates(ctx.AppDB(), ctx.CurrentProject(), candidateFilter{Status: "all", Limit: 20})
	if err != nil || total != 2 {
		t.Fatalf("candidates total=%d err=%v", total, err)
	}
	if candidates[0].FitScore == 0 || candidates[0].ConfidenceScore == 0 {
		t.Fatalf("scores were not calculated: %+v", candidates[0])
	}
	if count, _ := countEvidence(ctx.AppDB(), ctx.CurrentProject(), candidates[0].ID); count != 1 {
		t.Fatalf("evidence count=%d, want 1", count)
	}

	second, err := runDiscovery(ctx, profile.ID, "", 10)
	if err != nil {
		t.Fatalf("second discovery: %v", err)
	}
	if second["created"].(int) != 0 || second["duplicates"].(int) != 2 {
		t.Fatalf("unexpected dedupe result: %#v", second)
	}

	if _, err := addExclusion(ctx.AppDB(), ctx.CurrentProject(), "domain", "acme.example", "not a fit"); err != nil {
		t.Fatalf("add exclusion: %v", err)
	}
	third, err := runDiscovery(ctx, profile.ID, "", 10)
	if err != nil {
		t.Fatalf("third discovery: %v", err)
	}
	if third["excluded"].(int) != 1 || third["duplicates"].(int) != 1 {
		t.Fatalf("unexpected exclusion result: %#v", third)
	}
}

func TestResearchPersistsEvidenceAndImprovesConfidence(t *testing.T) {
	platform := &platformStub{}
	ctx := newTestContext(t, platform)
	profile := testProfile(t, ctx)
	candidate, _, err := insertCandidate(ctx.AppDB(), ctx.CurrentProject(), candidateInput{
		ProfileID: profile.ID, CompanyName: "Acme Cloud", CompanyDomain: "acme.example", Website: "https://acme.example", Source: "manual",
	}, profile)
	if err != nil {
		t.Fatalf("insert candidate: %v", err)
	}
	before := candidate.ConfidenceScore

	result, err := researchCandidate(ctx, candidate.ID, "")
	if err != nil {
		t.Fatalf("research: %v", err)
	}
	updated := result["candidate"].(*Candidate)
	if updated.ConfidenceScore <= before {
		t.Fatalf("confidence did not improve: before=%d after=%d", before, updated.ConfidenceScore)
	}
	evidence := result["evidence"].([]Evidence)
	if len(evidence) != 2 {
		t.Fatalf("evidence=%d, want 2", len(evidence))
	}
	if updated.Summary == "" || updated.ResearchedAt == "" {
		t.Fatalf("research fields missing: %+v", updated)
	}
}

func TestCRMHandoffIsIdempotent(t *testing.T) {
	platform := &platformStub{}
	ctx := newTestContext(t, platform)
	profile := testProfile(t, ctx)
	candidate, _, err := insertCandidate(ctx.AppDB(), ctx.CurrentProject(), candidateInput{
		ProfileID: profile.ID, CompanyName: "Acme Cloud", CompanyDomain: "acme.example", Website: "https://acme.example",
		PersonFirstName: "Alex", PersonLastName: "Rivera", PersonDisplayName: "Alex Rivera", JobTitle: "Head of Operations",
		Email: "alex@acme.example", Summary: "Strong match", Source: "manual",
	}, profile)
	if err != nil {
		t.Fatalf("insert candidate: %v", err)
	}

	first, err := acceptCandidate(ctx, candidate.ID, []any{"qualified-prospects"})
	if err != nil {
		t.Fatalf("first accept: %v", err)
	}
	if first["idempotent"].(bool) {
		t.Fatal("first accept unexpectedly idempotent")
	}
	if len(platform.calls) != 2 {
		t.Fatalf("platform calls=%d, want CRM upsert + activity", len(platform.calls))
	}
	upsert := platform.calls[0]
	if upsert.App != "crm" || upsert.Tool != "contacts_upsert_by_channel" || upsert.Input["value"] != "alex@acme.example" {
		t.Fatalf("unexpected upsert: %+v", upsert)
	}

	second, err := acceptCandidate(ctx, candidate.ID, nil)
	if err != nil {
		t.Fatalf("second accept: %v", err)
	}
	if !second["idempotent"].(bool) {
		t.Fatal("second accept should be idempotent")
	}
	if len(platform.calls) != 2 {
		t.Fatalf("idempotent retry made more platform calls: %d", len(platform.calls))
	}
	stored, _ := getCandidate(ctx.AppDB(), ctx.CurrentProject(), candidate.ID)
	if stored.Status != "accepted" || stored.CRMContactID == nil || *stored.CRMContactID != 701 {
		t.Fatalf("candidate handoff not persisted: %+v", stored)
	}
}

func TestRejectCanExcludeCompany(t *testing.T) {
	ctx := newTestContext(t, &platformStub{})
	profile := testProfile(t, ctx)
	candidate, _, err := insertCandidate(ctx.AppDB(), ctx.CurrentProject(), candidateInput{
		ProfileID: profile.ID, CompanyName: "Bad Fit Ltd", CompanyDomain: "badfit.example", Website: "https://badfit.example", Source: "manual",
	}, profile)
	if err != nil {
		t.Fatalf("insert candidate: %v", err)
	}
	app := &App{}
	result, err := app.toolCandidatesReject(ctx, map[string]any{"id": candidate.ID, "reason": "competitor", "exclude_company": true})
	if err != nil {
		t.Fatalf("reject: %v", err)
	}
	if result.(map[string]any)["exclusion"] == nil {
		t.Fatal("expected exclusion")
	}
	blocked, err := isExcluded(ctx.AppDB(), ctx.CurrentProject(), candidateInput{CompanyName: "Bad Fit Ltd", CompanyDomain: "badfit.example"})
	if err != nil || !blocked {
		t.Fatalf("excluded company was not blocked: blocked=%v err=%v", blocked, err)
	}
}

func TestProjectIsolation(t *testing.T) {
	ctx := newTestContext(t, &platformStub{})
	if _, err := createProfile(ctx.AppDB(), ctx.CurrentProject(), map[string]any{"name": "Project A"}); err != nil {
		t.Fatalf("create project A profile: %v", err)
	}
	other := ctx.WithProject("project-b")
	if _, err := createProfile(other.AppDB(), other.CurrentProject(), map[string]any{"name": "Project B"}); err != nil {
		t.Fatalf("create project B profile: %v", err)
	}
	aProfiles, err := listProfiles(ctx.AppDB(), ctx.CurrentProject(), "all")
	if err != nil || len(aProfiles) != 1 || aProfiles[0].Name != "Project A" {
		t.Fatalf("project A leaked data: profiles=%+v err=%v", aProfiles, err)
	}
	bProfiles, err := listProfiles(other.AppDB(), other.CurrentProject(), "all")
	if err != nil || len(bProfiles) != 1 || bProfiles[0].Name != "Project B" {
		t.Fatalf("project B leaked data: profiles=%+v err=%v", bProfiles, err)
	}
}

func TestAcceptRequiresContactChannel(t *testing.T) {
	platform := &platformStub{}
	ctx := newTestContext(t, platform)
	profile := testProfile(t, ctx)
	candidate, _, err := insertCandidate(ctx.AppDB(), ctx.CurrentProject(), candidateInput{
		ProfileID: profile.ID, CompanyName: "No Contact Ltd", CompanyDomain: "nocontact.example", Website: "https://nocontact.example", Source: "manual",
	}, profile)
	if err != nil {
		t.Fatalf("insert candidate: %v", err)
	}
	if _, err := acceptCandidate(ctx, candidate.ID, nil); err == nil {
		t.Fatal("accept should require email or phone")
	}
	if len(platform.calls) != 0 {
		t.Fatalf("invalid accept made platform calls: %d", len(platform.calls))
	}
}
