package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"strings"
	"sync"
	"testing"
	"time"
)

type placesPlatform struct {
	platformStub
	countsMu         sync.Mutex
	pages            map[string]any
	integrationCalls []map[string]any
	searchError      bool
	failCRM          bool
	crmCalls         int
	extractCalls     int
	onExtract        func()
}

func (p *placesPlatform) GetConnection(id int64) (*sdk.PlatformConnection, error) {
	if id != 31 {
		return nil, errors.New("not found")
	}
	return &sdk.PlatformConnection{ID: 31, AppSlug: "google-places", Status: "active", ProjectID: "project-a", Name: "Test Places"}, nil
}
func (p *placesPlatform) ListConnections(f sdk.ConnectionFilter) ([]sdk.PlatformConnection, error) {
	c, _ := p.GetConnection(31)
	return []sdk.PlatformConnection{*c, {ID: 32, AppSlug: "google-places", ProjectID: "other-project"}}, nil
}
func (p *placesPlatform) ExecuteIntegrationToolContext(c context.Context, id int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
	if err := c.Err(); err != nil {
		return nil, err
	}
	if id != 31 || tool != "search_text" {
		return nil, fmt.Errorf("unexpected integration call %d %s", id, tool)
	}
	if input["fields"] != placesSearchFields || input["fields"] == "*" {
		return nil, errors.New("wrong field mask")
	}
	p.integrationCalls = append(p.integrationCalls, input)
	if p.searchError {
		return nil, errors.New("upstream unavailable")
	}
	token, _ := input["pageToken"].(string)
	raw, err := json.Marshal(p.pages[token])
	return &sdk.ExecuteResult{Success: true, Status: 200, Data: raw}, err
}
func (p *placesPlatform) CallAppResultContext(c context.Context, app, tool string, input map[string]any, out any) error {
	if err := c.Err(); err != nil {
		return err
	}
	p.countsMu.Lock()
	if tool == "contacts_upsert_by_channel" {
		p.crmCalls++
		if p.failCRM {
			p.countsMu.Unlock()
			return errors.New("CRM temporarily unavailable")
		}
	}
	if tool == "web_extract" {
		p.extractCalls++
		if p.onExtract != nil {
			p.onExtract()
		}
	}
	p.countsMu.Unlock()
	return p.platformStub.CallAppResult(app, tool, input, out)
}
func placeFixture(id, name, website, phone string) map[string]any {
	return map[string]any{"id": id, "displayName": map[string]any{"text": name}, "websiteUri": website, "internationalPhoneNumber": phone, "formattedAddress": "123 Main St, Dallas, TX 75201, USA", "primaryType": "employment_agency", "businessStatus": "OPERATIONAL", "googleMapsUri": mapsPlaceURL(id)}
}
func pipelineSetup(t *testing.T, p *placesPlatform) (*sdk.AppCtx, *TargetProfile) {
	t.Helper()
	ctx := newTestContext(t, p)
	profile, err := createProfile(ctx.AppDB(), ctx.CurrentProject(), map[string]any{"name": "US staffing", "industries": []any{"staffing"}, "locations": []any{"United States"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = (&App{}).toolDiscoverySettings(ctx, map[string]any{"places_connection_id": 31}); err != nil {
		t.Fatal(err)
	}
	return ctx, profile
}
func startPipeline(t *testing.T, ctx *sdk.AppCtx, profile *TargetProfile, extra map[string]any) int64 {
	t.Helper()
	args := map[string]any{"profile_id": profile.ID, "source": "google_places", "limit": 20, "max_pages": 1, "qualify": true, "crm_mode": "review"}
	for k, v := range extra {
		args[k] = v
	}
	out, err := (&App{}).toolRun(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	return out.(map[string]any)["run"].(*prospectingJob).ID
}
func finishPipeline(t *testing.T, ctx *sdk.AppCtx, id int64) *prospectingJob {
	t.Helper()
	for n := 0; n < 35; n++ {
		if err := pipelineWorker(context.Background(), ctx); err != nil {
			t.Fatal(err)
		}
		j, err := getProspectingJob(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(j.Status, "completed") {
			return j
		}
	}
	t.Fatal("pipeline did not complete")
	return nil
}
func websiteFixture() map[string]any {
	return map[string]any{"url": "https://acme.example", "title": "Acme Staffing", "status": 200, "text": "Staffing and recruiting services for businesses. Contact office@acme.example. Call +1 214 555 0101. 123 Main St, Dallas, TX 75201. United States. Jane Smith, COO. Online application form. Book a consultation.", "metadata": map[string]any{"og:site_name": "Acme Staffing"}}
}

func TestPlacesRunCreatesDistinctBranchesAndPreservesDetails(t *testing.T) {
	p := &placesPlatform{pages: map[string]any{"": map[string]any{"places": []any{placeFixture("a", "Acme North", "https://acme.example", "+12145550101"), placeFixture("b", "Acme South", "https://acme.example", "+12145550102"), placeFixture("a", "Acme North", "https://acme.example", "+12145550101"), placeFixture("c", "No Website", "", "+12145550103")}}}}
	p.extractPages = map[string]any{"https://acme.example": websiteFixture()}
	ctx, profile := pipelineSetup(t, p)
	id := startPipeline(t, ctx, profile, nil)
	j := finishPipeline(t, ctx, id)
	if j.Counts["created"] != 3 || j.Counts["qualified"] != 2 || j.Counts["retained"] != 1 {
		t.Fatalf("counts=%v items=%+v", j.Counts, j.Items)
	}
	c, _, err := listCandidates(ctx.AppDB(), ctx.CurrentProject(), candidateFilter{Status: "all", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(c) != 3 {
		t.Fatalf("branches collapsed: %d", len(c))
	}
	for _, i := range j.Items {
		details, err := candidatePlace(ctx.AppDB(), ctx.CurrentProject(), *i.CandidateID)
		if err != nil || details == nil {
			t.Fatalf("details: %v %v", details, err)
		}
	}
	if p.crmCalls != 0 {
		t.Fatal("review mode wrote CRM")
	}
	if len(p.integrationCalls) != 1 {
		t.Fatalf("search calls=%d", len(p.integrationCalls))
	}
}

func TestPlacesRunRetriesCRMWithoutRequalificationOrDuplicateHandoffs(t *testing.T) {
	p := &placesPlatform{pages: map[string]any{"": map[string]any{"places": []any{placeFixture("a", "Acme Staffing", "https://acme.example", "+12145550101")}}}, failCRM: true}
	p.extractPages = map[string]any{"https://acme.example": websiteFixture()}
	ctx, profile := pipelineSetup(t, p)
	args := map[string]any{"crm_mode": "auto", "min_fit_score": 0, "min_confidence_score": 0, "idempotency_key": "first", "list_ids": []any{"test-ai-consulting"}}
	id := startPipeline(t, ctx, profile, args)
	j := finishPipeline(t, ctx, id)
	if j.Status != "completed_with_errors" || j.Counts["failed"] != 1 || p.extractCalls != 1 || p.crmCalls != 1 {
		t.Fatalf("run=%+v extracts=%d crm=%d", j, p.extractCalls, p.crmCalls)
	}
	candidateID := *j.Items[0].CandidateID
	if _, err := updateCandidate(ctx.AppDB(), ctx.CurrentProject(), candidateID, map[string]any{"summary": "Operator correction; buyer need unverified."}); err != nil {
		t.Fatal(err)
	}
	p.failCRM = false
	if _, err := (&App{}).toolRunResume(ctx, map[string]any{"id": id}); err != nil {
		t.Fatal(err)
	}
	j = finishPipeline(t, ctx, id)
	if j.Counts["transferred"] != 1 || p.extractCalls != 1 || p.crmCalls != 2 {
		t.Fatalf("replayed completed step: %+v extracts=%d crm=%d", j, p.extractCalls, p.crmCalls)
	}
	c, err := getCandidate(ctx.AppDB(), ctx.CurrentProject(), candidateID)
	if err != nil {
		t.Fatal(err)
	}
	if c.Summary != "Operator correction; buyer need unverified." || c.CRMContactID == nil {
		t.Fatalf("candidate=%+v", c)
	}
	if again := startPipeline(t, ctx, profile, args); again != id {
		t.Fatal("idempotent request created another run")
	}
	id2 := startPipeline(t, ctx, profile, map[string]any{"crm_mode": "auto", "min_fit_score": 0, "min_confidence_score": 0})
	j = finishPipeline(t, ctx, id2)
	if j.Counts["created"] != 0 || j.Counts["existing"] != 1 || p.crmCalls != 2 {
		t.Fatalf("duplicate prospect/CRM write: %+v calls=%d", j, p.crmCalls)
	}
	for _, call := range p.calls {
		if call.Tool == "contacts_log_activity" && !strings.Contains(fmt.Sprint(call.Input["body"]), "https://acme.example") {
			t.Fatal("CRM note has no evidence links")
		}
	}
}

func TestPlacesRunExclusionsClosedBusinessesAndThresholds(t *testing.T) {
	closed := placeFixture("closed", "Closed Staffing", "https://closed.example", "+12145550104")
	closed["businessStatus"] = "CLOSED_PERMANENTLY"
	p := &placesPlatform{pages: map[string]any{"": map[string]any{"places": []any{closed, placeFixture("blocked", "Blocked Staffing", "https://blocked.example", "+12145550102"), placeFixture("good", "Acme Staffing", "https://acme.example", "+12145550101")}}}}
	p.extractPages = map[string]any{"https://acme.example": websiteFixture()}
	ctx, profile := pipelineSetup(t, p)
	if _, err := addExclusion(ctx.AppDB(), ctx.CurrentProject(), "domain", "blocked.example", "test exclusion"); err != nil {
		t.Fatal(err)
	}
	id := startPipeline(t, ctx, profile, map[string]any{"crm_mode": "auto", "min_fit_score": 100, "min_confidence_score": 100})
	j := finishPipeline(t, ctx, id)
	if j.Counts["excluded"] != 2 || j.Counts["created"] != 1 || j.Counts["retained"] != 1 || p.crmCalls != 0 {
		t.Fatalf("run=%+v crm=%d", j, p.crmCalls)
	}
}

func TestPlacesRunResumeUsesCheckpointAndRequestBudget(t *testing.T) {
	p := &placesPlatform{pages: map[string]any{"": map[string]any{"places": []any{placeFixture("a", "One", "", "+12145550101")}, "nextPageToken": "next"}, "next": map[string]any{"places": []any{placeFixture("b", "Two", "", "+12145550102")}}}}
	ctx, profile := pipelineSetup(t, p)
	id := startPipeline(t, ctx, profile, map[string]any{"qualify": false})
	if err := pipelineWorker(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	j, err := getProspectingJob(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if j.State.PageToken != "next" || j.Counts["created"] != 1 {
		t.Fatalf("checkpoint=%+v", j)
	}
	// Model an interrupted process whose lease expired. Resume continues with
	// the next page, without repeating the completed page or inserted prospect.
	_, err = ctx.AppDB().Exec(`UPDATE prospecting_jobs SET lease_token='dead-process',lease_until=? WHERE id=?`, time.Now().Unix()-1, id)
	if err != nil {
		t.Fatal(err)
	}
	j = finishPipeline(t, ctx, id)
	if len(p.integrationCalls) != 2 || p.integrationCalls[1]["pageToken"] != "next" || j.Counts["created"] != 2 {
		t.Fatalf("checkpoint replay: %+v calls=%v", j, p.integrationCalls)
	}
	if _, err = (&App{}).toolDiscoverySettings(ctx, map[string]any{"daily_places_request_limit": 2}); err != nil {
		t.Fatal(err)
	}
	id2 := startPipeline(t, ctx, profile, map[string]any{"qualify": false})
	if err = pipelineWorker(context.Background(), ctx); err == nil || !strings.Contains(err.Error(), "daily") {
		t.Fatalf("daily budget error=%v", err)
	}
	j, err = getProspectingJob(ctx, id2)
	if err != nil {
		t.Fatal(err)
	}
	if j.Status != "failed" || len(p.integrationCalls) != 2 {
		t.Fatalf("budget ignored: %+v", j)
	}
}

func TestPlacesRunPreservesEditsDuringQualification(t *testing.T) {
	p := &placesPlatform{pages: map[string]any{"": map[string]any{"places": []any{placeFixture("a", "Acme Staffing", "https://acme.example", "+12145550101")}}}}
	p.extractPages = map[string]any{"https://acme.example": websiteFixture()}
	ctx, profile := pipelineSetup(t, p)
	id := startPipeline(t, ctx, profile, nil)
	if err := pipelineWorker(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	j, err := getProspectingJob(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	candidateID := *j.Items[0].CandidateID
	p.onExtract = func() {
		p.onExtract = nil
		if _, e := updateCandidate(ctx.AppDB(), ctx.CurrentProject(), candidateID, map[string]any{"summary": "Keep operator note", "person_display_name": "", "job_title": ""}); e != nil {
			t.Fatal(e)
		}
	}
	j = finishPipeline(t, ctx, id)
	if j.Counts["failed"] != 1 {
		t.Fatalf("stale qualification accepted: %+v", j)
	}
	c, _ := getCandidate(ctx.AppDB(), ctx.CurrentProject(), candidateID)
	if c.Summary != "Keep operator note" || c.PersonDisplayName != "" {
		t.Fatalf("lost operator edit: %+v", c)
	}
	if _, err = (&App{}).toolRunResume(ctx, map[string]any{"id": id}); err != nil {
		t.Fatal(err)
	}
	j = finishPipeline(t, ctx, id)
	c, _ = getCandidate(ctx.AppDB(), ctx.CurrentProject(), candidateID)
	if c.Summary != "Keep operator note" || c.PersonDisplayName != "" || c.JobTitle != "" || j.Counts["qualified"] != 1 {
		t.Fatalf("operator correction overwritten on retry: %+v %+v", c, j)
	}
}

func TestPlacesReconcilesExistingWebLeadWithoutOverwritingIt(t *testing.T) {
	p := &placesPlatform{}
	ctx, profile := pipelineSetup(t, p)
	original, _, err := insertCandidate(ctx.AppDB(), ctx.CurrentProject(), candidateInput{ProfileID: profile.ID, CompanyName: "Operator Company", Website: "https://acme.example", Phone: "+12145550101", Summary: "Existing summary"}, profile)
	if err != nil {
		t.Fatal(err)
	}
	var place placeDetails
	raw, _ := json.Marshal(placeFixture("a", "Google name", "https://acme.example", "+12145550101"))
	json.Unmarshal(raw, &place)
	c, created, _, err := ingestPlace(ctx, profile, place)
	if err != nil || created || c.ID != original.ID || c.Summary != "Existing summary" || c.CompanyName != "Operator Company" {
		t.Fatalf("reconciliation: %+v %v %v", c, created, err)
	}
	place.ID = "b"
	place.Phone = "+12145550102"
	c, created, _, err = ingestPlace(ctx, profile, place)
	if err != nil || !created || c.ID == original.ID {
		t.Fatalf("second branch collapsed: %+v %v %v", c, created, err)
	}
}

func TestPipelineProjectScopeAndConcurrentWorkerLease(t *testing.T) {
	p := &placesPlatform{}
	ctx, profile := pipelineSetup(t, p)
	// A no-discovery job tests the lease independently of non-thread-safe mock
	// transports. It can be claimed by only one worker at a time.
	id := startPipeline(t, ctx, profile, map[string]any{"qualify": false})
	_, err := ctx.AppDB().Exec(`UPDATE prospecting_jobs SET state_json='{"phase":"qualify"}' WHERE id=?`, id)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for n := 0; n < 2; n++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- pipelineWorker(context.Background(), ctx) }()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	j, err := getProspectingJob(ctx, id)
	if err != nil || j.Status != "completed" {
		t.Fatalf("job=%+v err=%v", j, err)
	}
	if _, err = getProspectingJob(ctx.WithProject("other-project"), id); err == nil {
		t.Fatal("cross-project run leaked")
	}
	if _, err = (&App{}).toolDiscoverySettings(ctx.WithProject("other-project"), map[string]any{"places_connection_id": 31}); err == nil {
		t.Fatal("cross-project connection selected")
	}
}

func TestWebDiscoveryDoesNotDuplicatePlacesProspects(t *testing.T) {
	p := &placesPlatform{pages: map[string]any{"": map[string]any{"places": []any{placeFixture("a", "Acme Staffing", "https://acme.example", "+12145550101")}}}}
	p.searchPayload = map[string]any{"results": []any{map[string]any{"title": "Acme Staffing", "url": "https://acme.example", "snippet": "staffing"}}}
	ctx, profile := pipelineSetup(t, p)
	id := startPipeline(t, ctx, profile, map[string]any{"qualify": false})
	finishPipeline(t, ctx, id)
	id2 := startPipeline(t, ctx, profile, map[string]any{"source": "web", "qualify": false})
	j := finishPipeline(t, ctx, id2)
	if j.Counts["created"] != 0 || j.Counts["existing"] != 1 {
		t.Fatalf("Web duplicated Places: %+v", j)
	}
	result, err := runDiscoveryWithOptions(ctx, profile.ID, "staffing", 10, "google", "duckduckgo")
	if err != nil {
		t.Fatal(err)
	}
	if result["created"] != 0 || result["duplicates"] != 1 {
		t.Fatalf("legacy discovery duplicated Places: %v", result)
	}
	var count int
	ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM candidates`).Scan(&count)
	if count != 1 {
		t.Fatalf("candidate count=%d", count)
	}
}

func TestRunIdempotencyKeyRejectsChangedOptions(t *testing.T) {
	p := &placesPlatform{}
	ctx, profile := pipelineSetup(t, p)
	startPipeline(t, ctx, profile, map[string]any{"qualify": false, "idempotency_key": "same"})
	_, err := (&App{}).toolRun(ctx, map[string]any{"profile_id": profile.ID, "source": "google_places", "qualify": false, "idempotency_key": "same", "limit": 5})
	if err == nil || !strings.Contains(err.Error(), "different run options") {
		t.Fatalf("changed request reused idempotency key: %v", err)
	}
}
