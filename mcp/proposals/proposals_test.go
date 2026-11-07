package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	sdk "github.com/apteva/app-sdk"
	"github.com/apteva/app-sdk/testkit"
)

func newTestApp(t *testing.T) *App {
	t.Helper()
	ctx := testkit.NewAppCtx(t, "apteva.yaml", testkit.WithProjectID("project-a"))
	a := &App{}
	if err := a.OnMount(ctx); err != nil {
		t.Fatal(err)
	}
	return a
}

func sampleContent() content {
	return content{
		Title: "Software build", Sender: "Build Studio", ClientEmail: "client@example.com",
		Currency: "EUR", ValidityDays: 14, OpportunityID: 42,
		Categories: []category{
			{Name: "Core", Rule: "any", Items: []item{{Name: "MVP", AmountMinor: 1_200_000, Required: true}}},
			{Name: "Integrations", Rule: "any", Items: []item{
				{Name: "Stripe", AmountMinor: 250_000}, {Name: "CSV", AmountMinor: 80_000}, {Name: "Slack", AmountMinor: 120_000},
			}},
			{Name: "Support", Rule: "one_required", Items: []item{{Name: "Standard"}, {Name: "Extended", AmountMinor: 75_000}}},
		},
	}
}

func draftAndPublish(t *testing.T, a *App) revision {
	t.Helper()
	ctx := context.Background()
	d, err := a.store.create(ctx, a.project, sampleContent())
	if err != nil {
		t.Fatal(err)
	}
	r := d.Revisions[0]
	p, err := a.store.publish(ctx, a.project, d.ID, r.ID, r.Version)
	if err != nil {
		t.Fatal(err)
	}
	return p.Revisions[0]
}

func TestRevisionAndClientFlow(t *testing.T) {
	a := newTestApp(t)
	ctx := context.Background()
	original := draftAndPublish(t, a)
	oldToken := a.store.token(a.project, original)
	draft, err := a.store.revise(ctx, a.project, original.ProposalID)
	if err != nil {
		t.Fatal(err)
	}
	next := draft.Revisions[0]
	next.Content.Categories[0].Items[0].AmountMinor += 10_000
	saved, err := a.store.update(ctx, a.project, draft.ID, next.ID, next.Version, next.Content)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.store.publish(ctx, a.project, draft.ID, next.ID, next.Version); err != errConflict {
		t.Fatalf("stale publish: %v", err)
	}
	if r, err := a.store.offer(ctx, a.store.db, a.project, oldToken); err != nil || r.State != "published" {
		t.Fatalf("editing a draft changed the live revision: %+v, %v", r, err)
	}
	released, err := a.store.publish(ctx, a.project, draft.ID, next.ID, saved.Revisions[0].Version)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.store.respond(ctx, a.project, oldToken, "accepted", "Client", "", nil); err != errConflict {
		t.Fatalf("superseded link accepted: %v", err)
	}
	live := released.Revisions[0]
	_, token, err := a.store.link(ctx, a.project, draft.ID, live.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.store.offer(ctx, a.store.db, a.project, a.store.token(a.project, live)); err != errNotFound {
		t.Fatalf("rotated link was still valid: %v", err)
	}
	if _, err := a.store.offer(ctx, a.store.db, "project-b", token); err != errNotFound {
		t.Fatalf("link crossed projects: %v", err)
	}
	choices := selection{"1:0": "include", "1:1": "later", "1:2": "decline", "2:0": "include"}
	if _, err := a.store.respond(ctx, a.project, token, "accepted", "Client", "", selection{"0:0": "decline"}); err == nil {
		t.Fatal("removed a required item")
	}
	if _, err := a.store.respond(ctx, a.project, token, "accepted", "Client", "", selection{"2:0": "include", "2:1": "include"}); err == nil {
		t.Fatal("accepted two items in a pick-one category")
	}
	if _, err := a.store.respond(ctx, a.project, token, "accepted", "Client", "", nil); err == nil {
		t.Fatal("accepted without a required group choice")
	}
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			got, err := a.store.respond(ctx, a.project, token, "accepted", "Client", "", choices)
			if err != nil || got.Response == nil || got.Response.TotalMinor != 1_460_000 ||
				got.Response.Selection["1:1"] != "later" || got.Response.Selection["1:2"] != "decline" {
				t.Errorf("acceptance: %+v, %v", got.Response, err)
			}
		})
	}
	wg.Wait()
	if _, err := a.store.respond(ctx, a.project, token, "accepted", "Someone else", "", choices); err != errConflict {
		t.Fatalf("a different response was treated as an exact retry: %v", err)
	}
	var responses, events int
	if err := a.store.db.QueryRow("SELECT (SELECT COUNT(*) FROM responses), (SELECT COUNT(*) FROM outbox WHERE topic = 'proposal.accepted')").Scan(&responses, &events); err != nil || responses != 1 || events != 1 {
		t.Fatalf("responses = %d, accepted events = %d, error = %v", responses, events, err)
	}
}

func TestPublicRequestChanges(t *testing.T) {
	a := newTestApp(t)
	r := draftAndPublish(t, a)
	token := a.store.token(a.project, r)
	post := func(path string, in offerRequest) *httptest.ResponseRecorder {
		t.Helper()
		raw, err := json.Marshal(in)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, path+"?project_id="+a.project, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		res := httptest.NewRecorder()
		a.handleOffer(res, req)
		return res
	}
	res := post("/offer/view", offerRequest{Token: token})
	if res.Code != 200 || strings.Contains(res.Body.String(), "client@example.com") || strings.Contains(res.Body.String(), "opportunity_id") {
		t.Fatalf("public view leaked internal data: %d %s", res.Code, res.Body)
	}
	res = post("/offer/respond", offerRequest{Token: token, Outcome: "changes_requested"})
	if res.Code != 400 {
		t.Fatalf("empty revision request: %d %s", res.Code, res.Body)
	}
	res = post("/offer/respond", offerRequest{Token: token, Outcome: "changes_requested", Note: "Please adjust the core scope."})
	if res.Code != 200 || !strings.Contains(res.Body.String(), "changes_requested") {
		t.Fatalf("revision request: %d %s", res.Code, res.Body)
	}
	next, err := a.store.revise(context.Background(), a.project, r.ProposalID)
	if err != nil || next.Revisions[0].State != "draft" {
		t.Fatalf("start requested revision: %+v, %v", next, err)
	}
}

func TestMCPRequiresProposalGrant(t *testing.T) {
	a := newTestApp(t)
	r := draftAndPublish(t, a)
	caller := &sdk.Caller{DefaultEffect: "deny"}
	ctx := sdk.WithCaller(context.Background(), caller)
	for _, tool := range a.MCPTools() {
		if tool.Name == "proposals_get" {
			if _, err := tool.HandlerCtx(ctx, a.app, map[string]any{"proposal_id": r.ProposalID}); err != errForbidden {
				t.Fatalf("ungranted MCP read: %v", err)
			}
			return
		}
	}
	t.Fatal("proposals_get was not registered")
}
