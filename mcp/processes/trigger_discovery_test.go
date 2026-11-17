package main

import (
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

func discoverySetup(t *testing.T) (*App, *triggerPlatform, *Process, Assignment) {
	t.Helper()
	f := &triggerPlatform{subscriptions: map[string]sdk.AppEventSubscription{}, sources: []sdk.AppEventSource{
		{InstallID: 41, App: "signup", Name: "Signup", Events: []sdk.EventDecl{{Name: "customer.signed_up", Payload: map[string]string{"page": "string", "plan": "string"}}}},
		{InstallID: 50, App: "leads", Name: "Leads", Events: []sdk.EventDecl{
			{Name: "lead.created", Description: "A lead was captured.", Payload: map[string]string{"lead_email": "string", "company": "string"}},
			{Name: "lead.qualified", Description: "A lead reached the qualification score.", Payload: map[string]string{"lead_email": "string", "score": "integer"}},
		}},
		{InstallID: 60, App: "crm", Name: "CRM", Events: []sdk.EventDecl{{Name: "contact.added", Payload: map[string]string{"email": "string", "name": "string"}}}},
		{InstallID: 61, App: "crm", Name: "CRM (old)", Events: []sdk.EventDecl{{Name: "contact.added", Payload: map[string]string{"email": "string", "name": "string"}}}},
		{InstallID: 70, App: "billing", Name: "Billing", Events: []sdk.EventDecl{{Name: "invoice.paid"}}},
		{InstallID: 80, App: "calendar", Name: "Calendar"},
	}}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-a"), tk.WithPlatform(f))
	a := &App{}
	if e := a.OnMount(ctx); e != nil {
		t.Fatal(e)
	}
	d := workflowDefinition()
	d.Name = "Qualified lead follow-up"
	d.Parameters = []Parameter{{Key: "email", Label: "Lead email", Type: "string", Required: true}, {Key: "score", Type: "number", Required: true}}
	p := create(t, a, d)
	x := p.Assignments[0]
	x.Roles = map[string]Executor{"researcher": {Kind: "agent", AgentID: 7}, "writer": {Kind: "agent", AgentID: 8}, "reviewer": {Kind: "agent", AgentID: 10}, "publisher": {Kind: "agent", AgentID: 7}}
	saved, e := a.saveAssignment(p.ProjectID, p.ID, x.ID, x.Revision, x.AssignmentConfig)
	if e != nil {
		t.Fatal(e)
	}
	return a, f, p, *saved
}

func candidates(t *testing.T, a *App, p *Process, x Assignment, extra map[string]any) []TriggerCandidate {
	t.Helper()
	args := map[string]any{"process_id": p.ID, "assignment_id": x.ID}
	for k, v := range extra {
		args[k] = v
	}
	out, e := a.execute(p.ProjectID, "agent:7:main", "trigger_sources", args)
	if e != nil {
		t.Fatal(e)
	}
	return out.(map[string]any)["candidates"].([]TriggerCandidate)
}

func TestTriggerSourcesRanksEventsForAssignment(t *testing.T) {
	a, _, p, x := discoverySetup(t)
	for _, query := range []string{"", "a lead is qualified"} {
		got := candidates(t, a, p, x, map[string]any{"query": query})
		top := got[0]
		if top.InstallID != 50 || top.Topic != "lead.qualified" || !top.Ready || top.SuggestedMappings["email"] != "data.lead_email" || top.SuggestedMappings["score"] != "data.score" {
			t.Fatalf("query %q: top candidate %+v", query, top)
		}
		for _, c := range got {
			if c.App == "signup" || c.App == "calendar" {
				t.Fatalf("unrelated source ranked: %+v", c)
			}
			if c.Topic == "contact.added" && (c.Ready || len(c.Missing) != 1 || c.Missing[0] != "score" || len(c.OtherInstalls) != 1) {
				t.Fatalf("partial candidate not flagged: %+v", c)
			}
		}
	}
	// The query outweighs field matches: "new CRM contact" puts CRM first
	// even though it cannot fill score, which is reported as missing.
	got := candidates(t, a, p, x, map[string]any{"query": "new CRM contact"})
	ready := got[0]
	if ready.Topic != "lead.qualified" {
		t.Fatalf("ready candidates must rank first: %+v", got[0])
	}
	crm := 0
	for _, c := range got {
		if c.App == "crm" && strings.Contains(strings.Join(c.Reasons, " "), "matches query") {
			crm++
		}
	}
	if crm != 2 {
		t.Fatalf("query should surface both CRM installs: %+v", got)
	}
	if only := candidates(t, a, p, x, map[string]any{"app": "crm"}); len(only) != 2 || only[0].App != "crm" {
		t.Fatalf("app filter: %+v", only)
	}
	// Undocumented events only show up when the query names them.
	paid := candidates(t, a, p, x, map[string]any{"query": "invoice paid"})
	found := false
	for _, c := range paid {
		found = found || (c.Topic == "invoice.paid" && !c.FieldsDocumented && !c.Ready)
	}
	if !found {
		t.Fatalf("query should surface undocumented event: %+v", paid)
	}
}

func TestTriggerSourcesListWithoutAssignment(t *testing.T) {
	a, _, p, _ := discoverySetup(t)
	out, e := a.execute(p.ProjectID, "agent:7:main", "trigger_sources", map[string]any{})
	if e != nil {
		t.Fatal(e)
	}
	sources := out.(map[string]any)["sources"].([]map[string]any)
	if len(sources) != 5 {
		t.Fatalf("sources without events should be skipped: %+v", sources)
	}
	if _, names := sources[0]["events"].([]any)[0].(string); !names {
		t.Fatalf("unfiltered list should hold event names only: %+v", sources[0])
	}
	out, _ = a.execute(p.ProjectID, "agent:7:main", "trigger_sources", map[string]any{"app": "leads", "query": "qualified"})
	sources = out.(map[string]any)["sources"].([]map[string]any)
	ev := sources[0]["events"].([]any)
	if len(sources) != 1 || len(ev) != 1 || ev[0].(sdk.EventDecl).Payload["score"] != "integer" {
		t.Fatalf("app filter should return event fields: %+v", sources)
	}
}

func TestTriggerCreateChecksSourceAndActivationNeedsRequiredParameters(t *testing.T) {
	a, _, p, x := discoverySetup(t)
	c := TriggerConfig{Name: "Qualified", SourceInstallID: 50, Topic: "lead.converted", Mappings: map[string]string{"email": "data.lead_email"}}
	if _, e := a.saveTrigger(p.ProjectID, p.ID, x.ID, "", 0, c); e == nil || !strings.Contains(e.Error(), "lead.qualified") {
		t.Fatalf("unpublished topic accepted: %v", e)
	}
	c.SourceInstallID, c.Topic = 99, "lead.qualified"
	if _, e := a.saveTrigger(p.ProjectID, p.ID, x.ID, "", 0, c); e == nil || !strings.Contains(e.Error(), "not an event source") {
		t.Fatalf("unknown install accepted: %v", e)
	}
	c.SourceInstallID = 50
	full := c
	full.Mappings = map[string]string{"email": "data.lead_email", "score": "data.score"}
	good, e := a.saveTrigger(p.ProjectID, p.ID, x.ID, "", 0, full)
	if e != nil || len(good.Warnings) != 0 {
		t.Fatalf("clean trigger: %+v %v", good.Warnings, e)
	}
	c.Mappings = map[string]string{"email": "data.email"}
	tr, e := a.saveTrigger(p.ProjectID, p.ID, x.ID, "", 0, c)
	if e != nil {
		t.Fatal(e)
	}
	w := strings.Join(tr.Warnings, " | ")
	if !strings.Contains(w, "data.email") || !strings.Contains(w, "score") {
		t.Fatalf("warnings: %v", tr.Warnings)
	}
	p = status(t, a, p.ID, "active")
	if _, e = a.setTriggerStatus(p.ProjectID, p.ID, tr.ID, "active", tr.Revision); e == nil || !strings.Contains(e.Error(), "score") {
		t.Fatalf("activation without score mapping: %v", e)
	}
	if good, e = a.setTriggerStatus(p.ProjectID, p.ID, good.ID, "active", good.Revision); e != nil || good.Status != "active" {
		t.Fatalf("activation with full mapping: %v", e)
	}
}

func TestTriggerSourcesIgnoresProcedureProse(t *testing.T) {
	f := &triggerPlatform{subscriptions: map[string]sdk.AppEventSubscription{}, sources: []sdk.AppEventSource{
		{InstallID: 60, App: "crm", Name: "CRM", Events: []sdk.EventDecl{
			{Name: "opportunity.stage.changed", Description: "An opportunity stage changed before close; figures are not final."},
			{Name: "contact.added", Description: "A contact was added."},
		}},
		{InstallID: 70, App: "billing", Name: "Billing", Events: []sdk.EventDecl{{Name: "invoice.changed", Description: "An invoice changed before payment."}}},
	}}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-a"), tk.WithPlatform(f))
	a := &App{}
	if e := a.OnMount(ctx); e != nil {
		t.Fatal(e)
	}
	d := workflowDefinition()
	d.Name = "CRM contact stats"
	d.Instructions = "Read the CRM contact totals. Figures are not changed before sending; only read current values."
	p := create(t, a, d)
	got := candidates(t, a, p, p.Assignments[0], nil)
	if len(got) == 0 || got[0].Topic != "contact.added" {
		t.Fatalf("procedure prose decided the ranking: %+v", got)
	}
	for _, c := range got {
		if c.App == "billing" {
			t.Fatalf("event matched only by prose words: %+v", c)
		}
		for _, r := range c.Reasons {
			if strings.Contains(r, "before") || strings.Contains(r, "changed") {
				t.Fatalf("prose word counted: %v", c.Reasons)
			}
		}
	}
}
