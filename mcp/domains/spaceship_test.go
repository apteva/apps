package main

import (
	"encoding/json"
	"fmt"
	"strings"
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

func TestSpaceshipList_ErrorIncludesProviderDiagnostics(t *testing.T) {
	plat := newSpaceshipStub(map[string]*sdk.ExecuteResult{
		"list_dns_records": {Success: false, Status: 422, Data: json.RawMessage(`{"code":"INVALID_DOMAIN","detail":"invalid domain"}`)},
	})
	ctx := newTestCtx(t, plat)

	_, err := spaceshipTestProvider().List(ctx, "deskorareception.com")
	if err == nil {
		t.Fatal("expected list error")
	}
	msg := err.Error()
	for _, want := range []string{
		"provider spaceship connection 1 tool list_dns_records",
		"GET /v1/dns/records/deskorareception.com?skip=0&take=500",
		"non-2xx status 422",
		"INVALID_DOMAIN",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error %q missing %q", msg, want)
		}
	}
}

func TestSpaceshipList_RejectsMalformedSuccessResponse(t *testing.T) {
	plat := newSpaceshipStub(map[string]*sdk.ExecuteResult{
		"list_dns_records": {Success: true, Status: 200, Data: json.RawMessage(`{"unexpected":true}`)},
	})
	ctx := newTestCtx(t, plat)
	_, err := spaceshipTestProvider().List(ctx, "acme.com")
	if err == nil || !strings.Contains(err.Error(), "no items or records array") {
		t.Fatalf("expected strict parse error, got %v", err)
	}
}

func TestSpaceshipList_PaginatesPastFiveHundredRecords(t *testing.T) {
	first := make([]map[string]any, 500)
	for i := range first {
		first[i] = map[string]any{"type": "TXT", "name": fmt.Sprintf("r%d", i), "value": "x", "ttl": 600}
	}
	plat := &stubPlatform{connSlug: "spaceship"}
	plat.executeFn = func(_ int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
		if tool != "list_dns_records" {
			return &sdk.ExecuteResult{Success: true, Status: 204}, nil
		}
		items := any(first)
		if intArg(input, "skip", 0) == 500 {
			items = []map[string]any{{"type": "A", "name": "@", "address": "1.2.3.4", "ttl": 600}}
		}
		body, _ := json.Marshal(map[string]any{"items": items})
		return &sdk.ExecuteResult{Success: true, Status: 200, Data: body}, nil
	}
	ctx := newTestCtx(t, plat)
	records, err := spaceshipTestProvider().List(ctx, "acme.com")
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 501 {
		t.Fatalf("records=%d, want 501", len(records))
	}
	if len(plat.calls) != 2 || intArg(plat.calls[1].Input, "skip", 0) != 500 {
		t.Fatalf("pagination calls: %+v", plat.calls)
	}
}

func TestSpaceshipUpsert_CreatesViaBatchSave(t *testing.T) {
	plat := newSpaceshipStub(nil)
	ctx := newTestCtx(t, plat)

	prov := spaceshipTestProvider()
	existing, err := prov.List(ctx, "acme.com")
	if err != nil {
		t.Fatal(err)
	}
	action, err := prov.Upsert(ctx, "acme.com", "mail", "A", "5.6.7.8", 600, "", existing)
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

	prov := spaceshipTestProvider()
	existing, err := prov.List(ctx, "acme.com")
	if err != nil {
		t.Fatal(err)
	}
	action, err := prov.Upsert(ctx, "acme.com", "www", "CNAME", "acme.com", 600, "", existing)
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

	prov := spaceshipTestProvider()
	existing, err := prov.List(ctx, "acme.com")
	if err != nil {
		t.Fatal(err)
	}
	if err := prov.Delete(ctx, "acme.com", "www", "CNAME", "", existing); err != nil {
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

	prov, _, err := app.providerFor(ctx, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := prov.(*spaceshipProvider); !ok {
		t.Fatalf("providerFor routed to %T, want *spaceshipProvider", prov)
	}
}
