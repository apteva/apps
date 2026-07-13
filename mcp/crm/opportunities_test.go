package main

import (
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func TestPipelinesList_BootstrapsDefaultPipeline(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}

	out, err := app.toolPipelinesList(ctx, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	pipes := out.(map[string]any)["pipelines"].([]*Pipeline)
	if len(pipes) != 1 {
		t.Fatalf("pipelines count=%d, want 1", len(pipes))
	}
	if pipes[0].Name != "Sales" || !pipes[0].IsDefault {
		t.Fatalf("default pipeline=%+v", pipes[0])
	}
	if len(pipes[0].Stages) != 7 {
		t.Fatalf("default stages=%d, want 7", len(pipes[0].Stages))
	}
	if pipes[0].Stages[0].Name != "New" || pipes[0].Stages[5].Category != "won" || pipes[0].Stages[6].Category != "lost" {
		t.Fatalf("unexpected default stages=%+v", pipes[0].Stages)
	}
}

func TestOpportunities_AgentFlow(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	contact := createOpportunityTestContact(t, ctx)

	pipesOut, err := app.toolPipelinesList(ctx, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	pipeline := pipesOut.(map[string]any)["pipelines"].([]*Pipeline)[0]
	stageByName := map[string]int64{}
	for _, st := range pipeline.Stages {
		stageByName[st.Name] = st.ID
	}

	createOut, err := app.toolOpportunitiesCreate(ctx, map[string]any{
		"contact_id":      contact.ID,
		"title":           "AI receptionist for Bright Smile Dental",
		"offer_key":       "ai_receptionist",
		"offer_name":      "AI Receptionist",
		"sender_identity": "hello@deskora.com",
		"source_site":     "deskora.com",
		"value":           3000.0,
		"currency":        "usd",
	})
	if err != nil {
		t.Fatal(err)
	}
	opp := createOut.(map[string]any)["opportunity"].(*Opportunity)
	if opp.PipelineID != pipeline.ID || opp.StageID != stageByName["New"] || opp.Status != "open" {
		t.Fatalf("created opportunity=%+v", opp)
	}
	if opp.Currency != "USD" {
		t.Fatalf("currency=%q, want USD", opp.Currency)
	}

	updateOut, err := app.toolOpportunitiesUpdate(ctx, map[string]any{
		"opportunity_id": opp.ID,
		"stage_id":       stageByName["Contacted"],
		"note":           "First outreach sent",
	})
	if err != nil {
		t.Fatal(err)
	}
	opp = updateOut.(map[string]any)["opportunity"].(*Opportunity)
	if opp.StageName != "Contacted" || opp.Status != "open" {
		t.Fatalf("updated opportunity=%+v", opp)
	}

	searchOut, err := app.toolOpportunitiesSearch(ctx, map[string]any{
		"contact_id":      contact.ID,
		"offer_key":       "ai_receptionist",
		"sender_identity": "hello@deskora.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	found := searchOut.(map[string]any)["opportunities"].([]*Opportunity)
	if len(found) != 1 || found[0].ID != opp.ID {
		t.Fatalf("search returned %+v, want opportunity %d", found, opp.ID)
	}

	getOut, err := app.toolOpportunitiesGet(ctx, map[string]any{"id": opp.ID})
	if err != nil {
		t.Fatal(err)
	}
	history := getOut.(map[string]any)["history"].([]*OpportunityStageHistory)
	if len(history) < 2 {
		t.Fatalf("history length=%d, want create + update entries", len(history))
	}

	updateOut, err = app.toolOpportunitiesUpdate(ctx, map[string]any{
		"opportunity_id": opp.ID,
		"stage_id":       stageByName["Won"],
	})
	if err != nil {
		t.Fatal(err)
	}
	opp = updateOut.(map[string]any)["opportunity"].(*Opportunity)
	if opp.Status != opportunityStatusWon || opp.ClosedAt == "" {
		t.Fatalf("won opportunity must be closed: %+v", opp)
	}
}

func TestOpportunities_CreateUsesFirstStageOfProvidedPipeline(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	contact := createOpportunityTestContact(t, ctx)

	pipeOut, err := app.toolPipelineCreate(ctx, map[string]any{
		"name": "Consulting",
		"stages": []any{
			map[string]any{"name": "Sourced", "position": 1, "category": "open"},
			map[string]any{"name": "Closed", "position": 2, "category": "won"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	pipeline := pipeOut.(map[string]any)["pipeline"].(*Pipeline)

	createOut, err := app.toolOpportunitiesCreate(ctx, map[string]any{
		"contact_id":  contact.ID,
		"pipeline_id": pipeline.ID,
		"title":       "Consulting opportunity",
	})
	if err != nil {
		t.Fatal(err)
	}
	opp := createOut.(map[string]any)["opportunity"].(*Opportunity)
	if opp.PipelineID != pipeline.ID || opp.StageID != pipeline.Stages[0].ID || opp.StageName != "Sourced" {
		t.Fatalf("opportunity landed on wrong pipeline/stage: %+v, pipeline=%+v", opp, pipeline)
	}
}

func TestPipelineStagesRejectInvalidConfiguration(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	pipesOut, err := app.toolPipelinesList(ctx, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	pipeline := pipesOut.(map[string]any)["pipelines"].([]*Pipeline)[0]

	if _, err := app.toolPipelineStageCreate(ctx, map[string]any{
		"pipeline_id": pipeline.ID,
		"name":        "Invalid category",
		"category":    "maybe",
	}); err == nil {
		t.Fatal("invalid stage category was accepted")
	}
	if _, err := app.toolPipelineStageCreate(ctx, map[string]any{
		"pipeline_id": pipeline.ID,
		"name":        "Invalid probability",
		"probability": 1.5,
	}); err == nil {
		t.Fatal("out-of-range stage probability was accepted")
	}

	contact := createOpportunityTestContact(t, ctx)
	opp, err := dbOpportunityCreate(ctx.AppDB(), "test-proj", opportunityCreateInput{
		ContactID:  contact.ID,
		PipelineID: pipeline.ID,
		StageID:    pipeline.Stages[0].ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if opp == nil {
		t.Fatal("opportunity was not created")
	}
	if _, err := dbPipelineStageUpdate(ctx.AppDB(), "test-proj", pipeline.Stages[0].ID, map[string]any{
		"category": "won",
	}); err == nil {
		t.Fatal("category change with active opportunities was accepted")
	}
}

func createOpportunityTestContact(t *testing.T, ctx *sdk.AppCtx) *Contact {
	t.Helper()
	c, err := dbCreate(ctx.AppDB(), "test-proj", map[string]any{
		"display_name": "Bright Smile Dental",
		"company":      "Bright Smile Dental",
		"source":       "test",
		"channels": []any{
			map[string]any{"kind": "email", "value": "info@brightsmiledental.com", "is_primary": true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}
