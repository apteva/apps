package main

import (
	"os"
	"reflect"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

// opportunityEvent returns the payload of the first event on topic.
func opportunityEvent(t *testing.T, rec *tk.EmitRecorder, topic string) map[string]any {
	t.Helper()
	for _, ev := range rec.Events() {
		if ev.Topic != topic {
			continue
		}
		payload, ok := ev.Data.(map[string]any)
		if !ok {
			t.Fatalf("event %q payload type %T, want map[string]any", topic, ev.Data)
		}
		return payload
	}
	t.Fatalf("no %q event emitted; got %v", topic, emittedTopics(rec))
	return nil
}

func emittedTopics(rec *tk.EmitRecorder) []string {
	out := []string{}
	for _, ev := range rec.Events() {
		out = append(out, ev.Topic)
	}
	return out
}

func stageIDsByName(t *testing.T, ctx *sdk.AppCtx) map[string]int64 {
	t.Helper()
	app := &App{}
	out, err := app.toolPipelinesList(ctx, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	pipes := out.(map[string]any)["pipelines"].([]*Pipeline)
	byName := map[string]int64{}
	for _, st := range pipes[0].Stages {
		byName[st.Name] = st.ID
	}
	return byName
}

// The whole point of the change: a consumer must be able to compute
// pipeline value and weighted pipeline from the event alone.
func TestEmitOpportunity_CarriesDealFields(t *testing.T) {
	rec := tk.NewEmitRecorder()
	ctx := newTestCtx(t, tk.WithEmitter(rec))
	app := &App{}
	contact := createOpportunityTestContact(t, ctx)

	if _, err := app.toolOpportunitiesCreate(ctx, map[string]any{
		"contact_id": contact.ID,
		"title":      "Implant consult",
		"value":      4500.50,
		"currency":   "USD",
	}); err != nil {
		t.Fatal(err)
	}

	p := opportunityEvent(t, rec, "opportunity.created")
	if p["title"] != "Implant consult" {
		t.Errorf("title=%v", p["title"])
	}
	if p["value"] != 4500.50 {
		t.Errorf("value=%v (%T), want 4500.50 float64 dollars", p["value"], p["value"])
	}
	if p["currency"] != "USD" {
		t.Errorf("currency=%v", p["currency"])
	}
	// Fresh opportunities land in the first stage ("New", open, 0.05).
	if p["stage_category"] != "open" {
		t.Errorf("stage_category=%v, want open", p["stage_category"])
	}
	if p["stage_probability"] != 0.05 {
		t.Errorf("stage_probability=%v, want 0.05 from the New stage", p["stage_probability"])
	}
	// Pre-existing fields must survive.
	for _, k := range []string{"opportunity_id", "contact_id", "pipeline_id", "stage_id", "status"} {
		if _, ok := p[k]; !ok {
			t.Errorf("payload lost pre-existing field %q", k)
		}
	}
}

// Absent must stay absent: a consumer summing pipeline value has to be
// able to tell "no amount recorded" from "worth nothing".
func TestEmitOpportunity_OmitsUnsetOptionalFields(t *testing.T) {
	rec := tk.NewEmitRecorder()
	ctx := newTestCtx(t, tk.WithEmitter(rec))
	app := &App{}
	contact := createOpportunityTestContact(t, ctx)

	if _, err := app.toolOpportunitiesCreate(ctx, map[string]any{
		"contact_id": contact.ID,
		"title":      "No amount yet",
	}); err != nil {
		t.Fatal(err)
	}

	p := opportunityEvent(t, rec, "opportunity.created")
	for _, k := range []string{"value", "currency"} {
		if v, ok := p[k]; ok {
			t.Errorf("%q present as %v; unset optionals must be omitted, not zero-filled", k, v)
		}
	}
	// Required fields still ride along.
	if p["title"] != "No amount yet" || p["stage_category"] != "open" {
		t.Errorf("payload=%v", p)
	}
}

func TestEmitOpportunity_WonCarriesWonStageFacts(t *testing.T) {
	rec := tk.NewEmitRecorder()
	ctx := newTestCtx(t, tk.WithEmitter(rec))
	app := &App{}
	contact := createOpportunityTestContact(t, ctx)
	stages := stageIDsByName(t, ctx)

	out, err := app.toolOpportunitiesCreate(ctx, map[string]any{
		"contact_id": contact.ID,
		"title":      "Whitening package",
		"value":      1200.0,
		"currency":   "EUR",
	})
	if err != nil {
		t.Fatal(err)
	}
	opp := out.(map[string]any)["opportunity"].(*Opportunity)

	if _, err := app.toolOpportunitiesUpdate(ctx, map[string]any{
		"opportunity_id": opp.ID,
		"stage_id":       stages["Won"],
	}); err != nil {
		t.Fatal(err)
	}

	p := opportunityEvent(t, rec, "opportunity.won")
	if p["stage_category"] != "won" {
		t.Errorf("stage_category=%v, want won", p["stage_category"])
	}
	if p["stage_probability"] != 1.0 {
		t.Errorf("stage_probability=%v, want 1.0 from the Won stage", p["stage_probability"])
	}
	if p["value"] != 1200.0 || p["currency"] != "EUR" {
		t.Errorf("value/currency lost on won: %v", p)
	}
	if int64FromAny(p["previous_stage_id"]) != stages["New"] {
		t.Errorf("previous_stage_id=%v, want the New stage %v", p["previous_stage_id"], stages["New"])
	}
}

// Guards the drift that motivated this change: the manifest declared
// fewer fields than emitOpportunity actually emitted, so consumers
// reading the manifest were under-told what they already received.
func TestOpportunityEvents_ManifestDeclaresEveryEmittedField(t *testing.T) {
	rec := tk.NewEmitRecorder()
	ctx := newTestCtx(t, tk.WithEmitter(rec))
	app := &App{}
	contact := createOpportunityTestContact(t, ctx)
	stages := stageIDsByName(t, ctx)

	out, err := app.toolOpportunitiesCreate(ctx, map[string]any{
		"contact_id": contact.ID,
		"title":      "Full arch",
		"value":      9000.0,
		"currency":   "USD",
	})
	if err != nil {
		t.Fatal(err)
	}
	opp := out.(map[string]any)["opportunity"].(*Opportunity)
	// Walk it through a stage move and a win so every opportunity.*
	// topic gets exercised.
	if _, err := app.toolOpportunitiesUpdate(ctx, map[string]any{
		"opportunity_id": opp.ID,
		"stage_id":       stages["Proposal"],
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolOpportunitiesUpdate(ctx, map[string]any{
		"opportunity_id": opp.ID,
		"stage_id":       stages["Won"],
	}); err != nil {
		t.Fatal(err)
	}

	declared := map[string]map[string]string{}
	for _, ev := range app.Manifest().Provides.Publishes {
		declared[ev.Name] = ev.Payload
	}

	seen := map[string]bool{}
	for _, ev := range rec.Events() {
		if len(ev.Topic) < 12 || ev.Topic[:12] != "opportunity." {
			continue
		}
		seen[ev.Topic] = true
		fields, ok := declared[ev.Topic]
		if !ok {
			t.Errorf("emitted undeclared event %q", ev.Topic)
			continue
		}
		payload, ok := ev.Data.(map[string]any)
		if !ok {
			t.Fatalf("event %q payload type %T", ev.Topic, ev.Data)
		}
		for k := range payload {
			if _, declaredField := fields[k]; !declaredField {
				t.Errorf("event %q emits %q but the manifest does not declare it", ev.Topic, k)
			}
		}
	}

	for _, want := range []string{
		"opportunity.created",
		"opportunity.updated",
		"opportunity.stage.changed",
		"opportunity.status.changed",
		"opportunity.won",
	} {
		if !seen[want] {
			t.Errorf("expected the flow to exercise %q; got %v", want, emittedTopics(rec))
		}
	}
}

// apteva.yaml and main.go's manifestYAML are two hand-maintained copies
// of the same document, and the binary serves the embedded one. Editing
// only the file ships a manifest that lies about what the app emits.
// This guards the opportunity events specifically — the pair drifted
// once already and the wider document is still out of sync.
func TestOpportunityEvents_FileAndEmbeddedManifestAgree(t *testing.T) {
	raw, err := os.ReadFile("apteva.yaml")
	if err != nil {
		t.Fatal(err)
	}
	fromFile, err := sdk.ParseManifest(raw)
	if err != nil {
		t.Fatalf("parse apteva.yaml: %v", err)
	}
	embedded := (&App{}).Manifest()

	collect := func(m sdk.Manifest) map[string]map[string]string {
		out := map[string]map[string]string{}
		for _, ev := range m.Provides.Publishes {
			if strings.HasPrefix(ev.Name, "opportunity.") {
				out[ev.Name] = ev.Payload
			}
		}
		return out
	}
	fileEvents, embeddedEvents := collect(*fromFile), collect(embedded)

	if len(fileEvents) != len(embeddedEvents) {
		t.Fatalf("opportunity event count: apteva.yaml=%d, embedded=%d",
			len(fileEvents), len(embeddedEvents))
	}
	for name, fileFields := range fileEvents {
		embeddedFields, ok := embeddedEvents[name]
		if !ok {
			t.Errorf("%q declared in apteva.yaml but not in main.go's manifestYAML", name)
			continue
		}
		if !reflect.DeepEqual(fileFields, embeddedFields) {
			t.Errorf("%q payload differs:\n  apteva.yaml: %v\n  embedded:    %v",
				name, fileFields, embeddedFields)
		}
	}
}
