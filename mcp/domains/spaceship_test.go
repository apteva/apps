package main

import (
	"encoding/json"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

const spaceshipRecordsJSON = `{
  "items": [
    {"type":"A","name":"@","address":"1.2.3.4","ttl":600},
    {"type":"CNAME","name":"www","cname":"acme.com","ttl":600},
    {"type":"MX","name":"@","exchange":"mx.acme.com","preference":10,"ttl":600}
  ],
  "total": 3
}`

const spaceshipAvailableJSON = `{"domain":"fresh-example.com","available":true}`

func newSpaceshipStub(extra map[string]*sdk.ExecuteResult) *stubPlatform {
	rep := map[string]*sdk.ExecuteResult{
		"list_dns_records":                 {Success: true, Status: 200, Data: json.RawMessage(spaceshipRecordsJSON)},
		"check_single_domain_availability": {Success: true, Status: 200, Data: json.RawMessage(spaceshipAvailableJSON)},
	}
	for k, v := range extra {
		rep[k] = v
	}
	return &stubPlatform{replyByTool: rep, connSlug: "spaceship"}
}

func spaceshipTestProvider() *spaceshipProvider {
	return &spaceshipProvider{bound: &sdk.BoundIntegration{
		Role:         "dns_provider",
		Kind:         "integration",
		ConnectionID: 1,
		AppSlug:      "spaceship",
	}}
}

func TestSpaceshipList_MapsRecords(t *testing.T) {
	plat := newSpaceshipStub(nil)
	ctx := newTestCtx(t, plat)

	recs, err := spaceshipTestProvider().List(ctx, "acme.com")
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 3 {
		t.Fatalf("want 3 records, got %d: %+v", len(recs), recs)
	}
	if recs[0].Name != "acme.com" || recs[0].Value != "1.2.3.4" || recs[0].TTL != 600 {
		t.Fatalf("A mapping wrong: %+v", recs[0])
	}
	if recs[1].Name != "www" || recs[1].Value != "acme.com" {
		t.Fatalf("CNAME mapping wrong: %+v", recs[1])
	}
	if recs[2].Type != "MX" || recs[2].Value != "mx.acme.com" || recs[2].Prio != 10 {
		t.Fatalf("MX mapping wrong: %+v", recs[2])
	}
}

func TestSpaceshipUpsert_CreatesViaBatchSave(t *testing.T) {
	plat := newSpaceshipStub(nil)
	ctx := newTestCtx(t, plat)

	action, err := spaceshipTestProvider().Upsert(ctx, "acme.com", "mail", "A", "5.6.7.8", 600)
	if err != nil {
		t.Fatal(err)
	}
	if action != "created" {
		t.Fatalf("action=%q, want created", action)
	}
	calls := plat.callsFor("save_dns_records")
	if len(calls) != 1 {
		t.Fatalf("save_dns_records called %d times, want 1", len(calls))
	}
	items, ok := calls[0].Input["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("items is not a 1-element array: %#v", calls[0].Input["items"])
	}
	item := items[0].(map[string]any)
	if item["type"] != "A" || item["name"] != "mail" || item["address"] != "5.6.7.8" || item["ttl"] != 600 {
		t.Fatalf("save item wrong: %#v", item)
	}
}

func TestSpaceshipUpsert_UnchangedShortCircuits(t *testing.T) {
	plat := newSpaceshipStub(nil)
	ctx := newTestCtx(t, plat)

	action, err := spaceshipTestProvider().Upsert(ctx, "acme.com", "www", "CNAME", "acme.com", 600)
	if err != nil {
		t.Fatal(err)
	}
	if action != "unchanged" {
		t.Fatalf("action=%q, want unchanged", action)
	}
	if n := len(plat.callsFor("save_dns_records")); n != 0 {
		t.Fatalf("save_dns_records called %d times for no-op", n)
	}
}

func TestSpaceshipDelete_UsesRawRecordShape(t *testing.T) {
	plat := newSpaceshipStub(nil)
	ctx := newTestCtx(t, plat)

	if err := spaceshipTestProvider().Delete(ctx, "acme.com", "www", "CNAME"); err != nil {
		t.Fatal(err)
	}
	calls := plat.callsFor("delete_dns_records")
	if len(calls) != 1 {
		t.Fatalf("delete_dns_records called %d times, want 1", len(calls))
	}
	records, ok := calls[0].Input["records"].([]any)
	if !ok || len(records) != 1 {
		t.Fatalf("records is not a 1-element array: %#v", calls[0].Input["records"])
	}
	rec := records[0].(map[string]any)
	if rec["type"] != "CNAME" || rec["name"] != "www" || rec["cname"] != "acme.com" {
		t.Fatalf("delete record wrong: %#v", rec)
	}
	if _, hasTTL := rec["ttl"]; hasTTL {
		t.Fatalf("delete record should omit ttl: %#v", rec)
	}
}

func TestSpaceshipAvailabilityCheck(t *testing.T) {
	plat := newSpaceshipStub(nil)
	ctx := newTestCtx(t, plat)
	app := &App{}

	out, err := app.toolDomainAvailabilityCheck(ctx, map[string]any{"domain": "fresh-example.com", "connection_id": 1})
	if err != nil {
		t.Fatal(err)
	}
	avail := out.(map[string]any)["availability"].(*DomainAvailability)
	if !avail.Available || avail.Provider != "spaceship" || avail.ConnectionID != 1 {
		t.Fatalf("availability wrong: %+v", avail)
	}
}

func TestProviderFor_RoutesSpaceshipSlug(t *testing.T) {
	plat := newSpaceshipStub(nil)
	ctx := newTestCtx(t, plat)
	app := &App{}

	prov, _, err := app.providerFor(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := prov.(*spaceshipProvider); !ok {
		t.Fatalf("providerFor routed to %T, want *spaceshipProvider", prov)
	}
}
