package main

import (
	"encoding/json"
	"fmt"
	"strings"
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
	calls          []recordedCall
	blockedEngines map[string]bool
	searchPayload  any
	extractPages   map[string]any
	disableWeb     bool
	disableCRM     bool
}

func (p *platformStub) WhoAmI() (*sdk.InstallIdentity, error) {
	bindings := map[string]any{}
	if !p.disableWeb {
		bindings["web"] = int64(101)
	}
	if !p.disableCRM {
		bindings["crm"] = int64(102)
	}
	return &sdk.InstallIdentity{AppName: "prospecting", InstallID: 1, ProjectID: "project-a", Bindings: bindings}, nil
}

func (p *platformStub) CallAppResult(app, tool string, input map[string]any, out any) error {
	p.calls = append(p.calls, recordedCall{App: app, Tool: tool, Input: input})
	var payload any
	switch app + "/" + tool {
	case "web/web_search":
		engine := fmt.Sprint(input["engine"])
		if p.blockedEngines[engine] {
			return fmt.Errorf("search_blocked: %s unavailable", engine)
		}
		if p.searchPayload != nil {
			payload = p.searchPayload
		} else {
			payload = map[string]any{
				"count": 2,
				"results": []map[string]any{
					{"title": "Acme Cloud | Automation for SaaS", "url": "https://acme.example/about", "snippet": "B2B SaaS automation company based in Spain", "source": "google", "rank": 1, "fetched_at": "2026-08-14T08:00:00Z", "confidence": "medium"},
					{"title": "Beta Systems - Operations software", "url": "https://beta.example", "snippet": "Spanish operations software company", "source": "google", "rank": 2, "fetched_at": "2026-08-14T08:00:00Z", "confidence": "medium"},
				},
			}
		}
	case "web/web_extract":
		pageURL := fmt.Sprint(input["url"])
		page, ok := p.extractPages[pageURL]
		if !ok {
			return fmt.Errorf("missing extract fixture for %s", pageURL)
		}
		payload = map[string]any{"page": page}
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

func TestManifestMakesExternalAppsOptional(t *testing.T) {
	manifest, err := sdk.ParseManifest(manifestBytes)
	if err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	if len(manifest.Requires.Apps) != 2 {
		t.Fatalf("requires apps=%d, want Web and CRM", len(manifest.Requires.Apps))
	}
	for _, dependency := range manifest.Requires.Apps {
		if !dependency.Optional {
			t.Fatalf("dependency %s is still required", dependency.Name)
		}
	}
}

func TestStandaloneImportExportAndCapabilityGating(t *testing.T) {
	platform := &platformStub{disableWeb: true, disableCRM: true}
	ctx := newTestContext(t, platform)
	app := &App{}
	capabilities := capabilitiesFor(ctx)
	if capabilities.Web || capabilities.CRM {
		t.Fatalf("capabilities=%+v, want standalone", capabilities)
	}
	imported, err := app.toolCandidatesImport(ctx, map[string]any{
		"format": "csv",
		"data":   "company,email,phone,website,notes\nAcme Dental,hello@acme.example,5125550199,https://acme.example,Seeded lead\nBeta Law,contact@beta.example,2125550142,https://beta.example,Review later\n",
	})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	result := imported.(map[string]any)
	if result["imported"] != 2 || result["profile_created"] != true {
		t.Fatalf("import result=%#v", result)
	}
	exported, err := app.toolCandidatesExport(ctx, map[string]any{"status": "all"})
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if exported.(map[string]any)["count"] != 2 {
		t.Fatalf("export result=%#v", exported)
	}
	profiles, _ := listProfiles(ctx.AppDB(), ctx.CurrentProject(), "active")
	if len(profiles) != 1 || profiles[0].Name != "Imported leads" {
		t.Fatalf("profiles=%+v", profiles)
	}
	if _, err := runDiscovery(ctx, profiles[0].ID, "dentists", 10); err == nil || !strings.Contains(err.Error(), "Web integration unavailable") {
		t.Fatalf("discovery error=%v, want optional Web guidance", err)
	}
	candidates, _, _ := listCandidates(ctx.AppDB(), ctx.CurrentProject(), candidateFilter{Status: "all", Limit: 10})
	if _, err := acceptCandidate(ctx, candidates[0].ID, nil); err == nil || !strings.Contains(err.Error(), "CRM integration unavailable") {
		t.Fatalf("accept error=%v, want optional CRM guidance", err)
	}
}

func TestPurgeRejectedCandidatesRequiresConfirmationAndKeepsReady(t *testing.T) {
	ctx := newTestContext(t, &platformStub{})
	app := &App{}
	profile, _, err := resolveSeedProfile(ctx, 0)
	if err != nil {
		t.Fatalf("profile: %v", err)
	}
	for _, company := range []string{"Keep Dental", "Remove Dental"} {
		if _, err := app.toolCandidatesCreate(ctx, map[string]any{"profile_id": profile.ID, "company_name": company}); err != nil {
			t.Fatalf("create %s: %v", company, err)
		}
	}
	candidates, _, _ := listCandidates(ctx.AppDB(), ctx.CurrentProject(), candidateFilter{Status: "all", Limit: 10})
	var rejectedID int64
	for _, candidate := range candidates {
		if candidate.CompanyName == "Remove Dental" {
			rejectedID = candidate.ID
		}
	}
	if _, err := app.toolCandidatesReject(ctx, map[string]any{"id": rejectedID, "reason": "not a fit"}); err != nil {
		t.Fatalf("reject: %v", err)
	}
	if _, err := app.toolCandidatesPurgeRejected(ctx, map[string]any{}); err == nil || !strings.Contains(err.Error(), "confirm=true") {
		t.Fatalf("purge without confirmation error=%v", err)
	}
	result, err := app.toolCandidatesPurgeRejected(ctx, map[string]any{"confirm": true})
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if result.(map[string]any)["deleted"] != int64(1) {
		t.Fatalf("purge result=%#v", result)
	}
	remaining, total, err := listCandidates(ctx.AppDB(), ctx.CurrentProject(), candidateFilter{Status: "all", Limit: 10})
	if err != nil || total != 1 || len(remaining) != 1 || remaining[0].CompanyName != "Keep Dental" {
		t.Fatalf("remaining=%+v total=%d err=%v", remaining, total, err)
	}
}

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

func TestCleanCompanyTitleRemovesMarketingTaglines(t *testing.T) {
	tests := map[string]string{
		"The Teeth Doctors™ • Worry Free, Five Star Rated Fayetteville Dentist":                  "The Teeth Doctors™",
		"Bright Smiles Dental · Family Dentistry in Austin":                                      "Bright Smiles Dental",
		"Acme Dental | Request an Appointment":                                                   "Acme Dental",
		"New Patient Forms Falko Family Dental":                                                  "Falko Family Dental",
		"New Patient Forms - Carolina Dentistry":                                                 "Carolina Dentistry",
		"Easy Online Patient Forms Great Expressions Dental https://example.com › patient-forms": "Great Expressions Dental",
		"Indian Creek Dental (+2) - Patient Forms":                                               "Indian Creek Dental",
		"Contact Our Dental Team in North Austin":                                                "Example",
		"Website":       "Example",
		"Patient Forms": "Example",
	}
	for input, want := range tests {
		if got := cleanCompanyTitle(input, "example.com"); got != want {
			t.Errorf("cleanCompanyTitle(%q)=%q, want %q", input, got, want)
		}
	}
}

func TestDiscoveryFiltersMarketplacesAndFormTemplates(t *testing.T) {
	platform := &platformStub{searchPayload: map[string]any{
		"count": 3,
		"results": []map[string]any{
			{"title": "New Patient Dental Forms", "url": "https://www.etsy.com/market/new_patient_dental_forms", "source": "google"},
			{"title": "Printable dental form template", "url": "https://forms.example/download", "snippet": "Download editable dental forms PDF", "source": "google"},
			{"title": "Bright Smiles Dental | Contact Us", "url": "https://brightsmiles.example/contact", "source": "google"},
		},
	}}
	ctx := newTestContext(t, platform)
	profile, err := createProfile(ctx.AppDB(), ctx.CurrentProject(), map[string]any{
		"name": "US Dental", "industries": []any{"Dental practice"}, "locations": []any{"United States"},
	})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}
	result, err := runDiscovery(ctx, profile.ID, "dentist contact", 10)
	if err != nil {
		t.Fatalf("discovery: %v", err)
	}
	if result["created"].(int) != 1 || result["excluded"].(int) != 2 {
		t.Fatalf("filter result: %#v", result)
	}
}

func TestContactExtractionUsesStructuredDataObfuscationAndFooterPhones(t *testing.T) {
	pages := []webExtractPage{
		{
			URL: "https://brightsmiles.example/contact", Title: "Contact Bright Smiles Dental",
			Text:           "Email info [at] brightsmiles [dot] example\n123 Main Street\n(512) 555-0199",
			StructuredData: map[string]any{"json_ld": []any{map[string]any{"@type": "Dentist", "email": "office@brightsmiles.example", "telephone": "+1 512 555 0188"}}},
		},
	}
	if got := extractBestEmail(pages, "brightsmiles.example"); got != "info@brightsmiles.example" {
		t.Fatalf("email=%q, want deobfuscated first-party email", got)
	}
	if got := extractBestPhone(pages, []string{"United States"}); got != "+15125550188" {
		t.Fatalf("phone=%q, want structured contact phone", got)
	}
}

func TestStructuredPhoneExtractionIgnoresNumericAssetHashes(t *testing.T) {
	pages := []webExtractPage{{
		URL:  "https://practice.example/contact",
		Text: "Call us at (954) 523-6525",
		StructuredData: map[string]any{
			"image": map[string]any{"url": "https://example.com/avatar/4599888728aa9ac69a03"},
		},
	}}
	if got := extractBestPhone(pages, []string{"United States"}); got != "9545236525" {
		t.Fatalf("phone=%q, want visible formatted contact phone", got)
	}
}

func TestUSPhoneExtractionRejectsLongIdentifiers(t *testing.T) {
	pages := []webExtractPage{{
		URL:  "https://example.com/contact",
		Text: "Tracking 35051546391753\nCall (512) 555-0199",
	}}
	if got := extractBestPhone(pages, []string{"United States"}); got != "5125550199" {
		t.Fatalf("phone=%q, want valid US phone", got)
	}
}

func TestContactExtractionRejectsThirdPartyEmailAndRepairsCollapsedMailbox(t *testing.T) {
	if got := normalizeQualifiedEmail("alejandro@agency.example", "practice.example"); got != "" {
		t.Fatalf("third-party email=%q, want rejected", got)
	}
	if got := normalizeQualifiedEmail("9am-5pm478-275-0630478-275-0630info@practice.example", "practice.example"); got != "info@practice.example" {
		t.Fatalf("collapsed email=%q, want repaired info mailbox", got)
	}
	if got := normalizeQualifiedEmail("practice@gmail.com", "practice.example"); got != "practice@gmail.com" {
		t.Fatalf("public mailbox=%q, want accepted", got)
	}
}

func TestUSPhoneExtractionRequiresEvidenceForPlainDigits(t *testing.T) {
	pages := []webExtractPage{{URL: "https://example.com", Text: "Tracking 4599888728"}}
	if got := extractBestPhone(pages, []string{"United States"}); got != "" {
		t.Fatalf("phone=%q, want unlabelled plain digits rejected", got)
	}
	pages[0].Text = "Phone 4599888728"
	if got := extractBestPhone(pages, []string{"United States"}); got != "4599888728" {
		t.Fatalf("phone=%q, want labelled digits accepted", got)
	}
	pages[0].Text = "Contact us " + strings.Repeat("x", 100) + " 4599888728\n(954) 523-6525"
	if got := extractBestPhone(pages, []string{"United States"}); got != "9545236525" {
		t.Fatalf("phone=%q, want distant plain digits ignored", got)
	}
}

func TestRequalificationRebuildsPreviouslyExtractedContacts(t *testing.T) {
	profile := &TargetProfile{Locations: []string{"United States"}}
	candidate := &Candidate{
		CompanyName: "Practice", CompanyDomain: "practice.example", Website: "https://practice.example",
		Email: "agency@vendor.example", Phone: "4599888728", EnrichedAt: "2026-01-01T00:00:00Z",
	}
	pages := []webExtractPage{{
		URL: "https://practice.example/contact", FinalURL: "https://practice.example/contact",
		Text: "Email info@practice.example\nPhone (512) 555-0199",
	}}
	applyDeterministicQualification(profile, candidate, pages)
	if candidate.Email != "info@practice.example" || candidate.Phone != "5125550199" {
		t.Fatalf("contacts=%q %q, want refreshed first-party values", candidate.Email, candidate.Phone)
	}
}

func TestCompanyNameKeepsUsefulDiscoveryTitle(t *testing.T) {
	candidate := &Candidate{CompanyName: "Austex Dental", CompanyDomain: "austexdental.com"}
	pages := []webExtractPage{{Title: "Bill Ding", Metadata: map[string]any{"og:site_name": "Bill Ding"}}}
	if got := extractCompanyName(candidate, pages); got != "Austex Dental" {
		t.Fatalf("company=%q, want existing discovery name", got)
	}
}

func TestDeobfuscateEmailTextDoesNotRewriteOrdinaryProse(t *testing.T) {
	input := "Contact us at info@example.com or office at brightsmiles dot example"
	want := "Contact us at info@example.com or office@brightsmiles.example"
	if got := deobfuscateEmailText(input); got != want {
		t.Fatalf("deobfuscateEmailText=%q, want %q", got, want)
	}
}

func TestDiscoveryFallsBackAndFiltersNoise(t *testing.T) {
	platform := &platformStub{
		blockedEngines: map[string]bool{"google": true},
		searchPayload: map[string]any{
			"count": 3,
			"results": []map[string]any{
				{"title": "Find a Dentist", "url": "https://www.zocdoc.com/dentists/austin-texas", "snippet": "Dentist directory", "source": "duckduckgo", "rank": 1},
				{"title": "Website Design for Dentists", "url": "https://agency.example/dental", "snippet": "Dental marketing agency for growing practices", "source": "duckduckgo", "rank": 2},
				{"title": "Bright Smiles Dental", "url": "https://brightsmiles.example", "snippet": "Family dental practice in Austin, Texas", "source": "duckduckgo", "rank": 3},
			},
		},
	}
	ctx := newTestContext(t, platform)
	profile, err := createProfile(ctx.AppDB(), ctx.CurrentProject(), map[string]any{
		"name": "US Dental", "industries": []any{"Dental practice"}, "locations": []any{"United States"},
	})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}

	result, err := runDiscoveryWithOptions(ctx, profile.ID, "dentist Austin", 10, "google", "duckduckgo")
	if err != nil {
		t.Fatalf("discovery: %v", err)
	}
	if result["engine"] != "duckduckgo" || result["fallback_used"] != true {
		t.Fatalf("fallback metadata: %#v", result)
	}
	if result["created"].(int) != 1 || result["excluded"].(int) != 2 {
		t.Fatalf("filter result: %#v", result)
	}
	if len(platform.calls) != 2 || platform.calls[0].Input["engine"] != "google" || platform.calls[1].Input["engine"] != "duckduckgo" {
		t.Fatalf("unexpected search calls: %+v", platform.calls)
	}
	exclusions, err := listExclusions(ctx.AppDB(), ctx.CurrentProject(), "domain", 10)
	if err != nil || len(exclusions) != 2 {
		t.Fatalf("noise exclusion missing: %+v err=%v", exclusions, err)
	}
}

func TestDeterministicQualificationExtractsSignalsAndScores(t *testing.T) {
	platform := &platformStub{extractPages: map[string]any{
		"https://brightsmiles.example/new-patients": map[string]any{
			"url": "https://brightsmiles.example/new-patients", "final_url": "https://brightsmiles.example/new-patients", "status": 200,
			"title": "New Patient Forms", "description": "Bright Smiles Dental welcomes new patients.",
			"metadata": map[string]any{"og:site_name": "Bright Smiles Dental"},
			"text":     "Bright Smiles Dental\n123 Main St, Austin, TX 78701\nComplete new patient forms before your appointment.\nOur office will contact you to confirm the day and time.\nWe verify insurance information.\nRequest appointment online.",
			"links":    []map[string]any{{"url": "/contact", "text": "Contact"}, {"url": "/team", "text": "Meet the team"}},
			"artifact": map[string]any{"id": 301},
		},
		"https://brightsmiles.example/contact": map[string]any{
			"url": "https://brightsmiles.example/contact", "status": 200, "title": "Contact Bright Smiles Dental",
			"text":     "Contact us for appointments in Austin, TX 78701.",
			"links":    []map[string]any{{"url": "mailto:hello@brightsmiles.example", "text": "Email"}, {"url": "tel:+15125550199", "text": "Call"}},
			"artifact": map[string]any{"id": 302},
		},
		"https://brightsmiles.example/team": map[string]any{
			"url": "https://brightsmiles.example/team", "status": 200, "title": "Our Dental Team",
			"text":     "Dr. Ada Stone\nPractice Owner\nBen Lee\nOffice Manager\nOur front desk schedules appointments and patient recalls.",
			"artifact": map[string]any{"id": 303},
		},
	}}
	ctx := newTestContext(t, platform)
	profile, err := createProfile(ctx.AppDB(), ctx.CurrentProject(), map[string]any{
		"name": "Small US Dental Practices", "industries": []any{"Dental practice"}, "locations": []any{"Austin", "Texas"},
		"employee_min": 2, "employee_max": 25, "target_titles": []any{"Practice Owner", "Office Manager"},
	})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}
	candidate, _, err := insertCandidate(ctx.AppDB(), ctx.CurrentProject(), candidateInput{
		ProfileID: profile.ID, CompanyName: "New Patient Forms", CompanyDomain: "brightsmiles.example",
		Website: "https://brightsmiles.example", SourceURL: "https://brightsmiles.example/new-patients", Source: "web_search",
	}, profile)
	if err != nil {
		t.Fatalf("insert candidate: %v", err)
	}

	result, err := qualifyCandidate(ctx, candidate.ID, 3)
	if err != nil {
		t.Fatalf("qualify: %v", err)
	}
	updated := result["candidate"].(*Candidate)
	if updated.CompanyName != "Bright Smiles Dental" || updated.Email != "hello@brightsmiles.example" || updated.Phone != "+15125550199" {
		t.Fatalf("identity/contact extraction failed: %+v", updated)
	}
	if updated.PersonDisplayName != "Ada Stone" || updated.JobTitle == "" {
		t.Fatalf("decision-maker extraction failed: %+v", updated)
	}
	if updated.Eligibility != "eligible" || updated.Location == "" || updated.EmployeeEstimate == nil || *updated.EmployeeEstimate < 2 {
		t.Fatalf("eligibility/firmographics failed: %+v", updated)
	}
	if len(updated.AutomationSignals) < 4 || updated.FitScore < 50 || updated.ConfidenceScore < 60 || updated.EnrichedAt == "" {
		t.Fatalf("qualification was not strong enough: %+v", updated)
	}
	evidence := result["evidence"].([]Evidence)
	if len(evidence) != 3 {
		t.Fatalf("evidence=%d, want 3", len(evidence))
	}
	for _, call := range platform.calls {
		if call.App != "web" || call.Tool != "web_extract" {
			t.Fatalf("qualification used a non-extraction tool: %+v", call)
		}
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
