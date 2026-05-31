package main

import (
	"encoding/json"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

// IONOS fixtures: one zone "acme.com" (id zone-1) holding a www CNAME and
// an apex MX. list_zones returns the top-level array the real API sends.
const ionosZonesJSON = `[{"id":"zone-1","name":"acme.com","type":"NATIVE"}]`

const ionosZoneJSON = `{
  "id": "zone-1",
  "name": "acme.com",
  "type": "NATIVE",
  "records": [
    {"id":"rec-www","name":"www.acme.com","rootName":"acme.com","type":"CNAME","content":"acme.com","ttl":3600,"prio":0,"disabled":false},
    {"id":"rec-mx","name":"acme.com","rootName":"acme.com","type":"MX","content":"mail.acme.com","ttl":3600,"prio":10,"disabled":false}
  ]
}`

func newIonosStub(extra map[string]*sdk.ExecuteResult) *stubPlatform {
	rep := map[string]*sdk.ExecuteResult{
		"list_zones": {Success: true, Status: 200, Data: json.RawMessage(ionosZonesJSON)},
		"get_zone":   {Success: true, Status: 200, Data: json.RawMessage(ionosZoneJSON)},
	}
	for k, v := range extra {
		rep[k] = v
	}
	s := &stubPlatform{replyByTool: rep, connSlug: "ionos"}
	return s
}

func ionosTestProvider() *ionosProvider {
	return &ionosProvider{bound: &sdk.BoundIntegration{
		Role:         "dns_provider",
		Kind:         "integration",
		ConnectionID: 1,
		AppSlug:      "ionos",
	}}
}

func (s *stubPlatform) callsFor(tool string) []executeCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []executeCall
	for _, c := range s.calls {
		if c.Tool == tool {
			out = append(out, c)
		}
	}
	return out
}

func TestIonosList_ResolvesZoneAndMapsRecords(t *testing.T) {
	plat := newIonosStub(nil)
	ctx := newTestCtx(t, plat)

	recs, err := ionosTestProvider().List(ctx, "acme.com")
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 {
		t.Fatalf("want 2 records, got %d: %+v", len(recs), recs)
	}
	// get_zone must be called with the resolved zone id.
	gz := plat.callsFor("get_zone")
	if len(gz) != 1 || gz[0].Input["zoneId"] != "zone-1" {
		t.Fatalf("get_zone not called with zone-1: %+v", gz)
	}
	// The MX record carries its priority through the canonical mapping.
	var mx *DNSRecord
	for i := range recs {
		if recs[i].Type == "MX" {
			mx = &recs[i]
		}
	}
	if mx == nil || mx.Prio != 10 || mx.Value != "mail.acme.com" {
		t.Errorf("MX mapping wrong: %+v", mx)
	}
}

func TestIonosUpsert_CreatesViaArrayBody(t *testing.T) {
	plat := newIonosStub(nil)
	ctx := newTestCtx(t, plat)

	action, err := ionosTestProvider().Upsert(ctx, "acme.com", "mail", "A", "5.6.7.8", 3600)
	if err != nil {
		t.Fatal(err)
	}
	if action != "created" {
		t.Fatalf("action=%q, want created", action)
	}
	cr := plat.callsFor("create_records")
	if len(cr) != 1 {
		t.Fatalf("create_records called %d times, want 1", len(cr))
	}
	if cr[0].Input["zoneId"] != "zone-1" {
		t.Errorf("create zoneId=%v, want zone-1", cr[0].Input["zoneId"])
	}
	// The body must be carried as a top-level array under "records",
	// each entry a record object with the full FQDN as its name.
	arr, ok := cr[0].Input["records"].([]any)
	if !ok || len(arr) != 1 {
		t.Fatalf("records is not a 1-element array: %#v", cr[0].Input["records"])
	}
	rec := arr[0].(map[string]any)
	if rec["name"] != "mail.acme.com" || rec["type"] != "A" || rec["content"] != "5.6.7.8" {
		t.Errorf("record fields wrong: %#v", rec)
	}
	if rec["ttl"] != 3600 {
		t.Errorf("ttl=%v, want 3600", rec["ttl"])
	}
	// No update on the create path.
	if n := len(plat.callsFor("update_record")); n != 0 {
		t.Errorf("update_record called %d times on create path", n)
	}
}

func TestIonosUpsert_UpdatesWhenPresent(t *testing.T) {
	plat := newIonosStub(nil)
	ctx := newTestCtx(t, plat)

	// www.acme.com CNAME exists pointing at acme.com; repoint it.
	action, err := ionosTestProvider().Upsert(ctx, "acme.com", "www", "CNAME", "newtarget.acme.com", 3600)
	if err != nil {
		t.Fatal(err)
	}
	if action != "updated" {
		t.Fatalf("action=%q, want updated", action)
	}
	ur := plat.callsFor("update_record")
	if len(ur) != 1 {
		t.Fatalf("update_record called %d times, want 1", len(ur))
	}
	if ur[0].Input["recordId"] != "rec-www" {
		t.Errorf("update recordId=%v, want rec-www", ur[0].Input["recordId"])
	}
	if ur[0].Input["content"] != "newtarget.acme.com" {
		t.Errorf("update content=%v", ur[0].Input["content"])
	}
	if n := len(plat.callsFor("create_records")); n != 0 {
		t.Errorf("create_records called %d times on update path", n)
	}
}

func TestIonosUpsert_UnchangedShortCircuits(t *testing.T) {
	plat := newIonosStub(nil)
	ctx := newTestCtx(t, plat)

	// Identical to the existing www CNAME (content acme.com, ttl 3600).
	action, err := ionosTestProvider().Upsert(ctx, "acme.com", "www", "CNAME", "acme.com", 3600)
	if err != nil {
		t.Fatal(err)
	}
	if action != "unchanged" {
		t.Fatalf("action=%q, want unchanged", action)
	}
	if n := len(plat.callsFor("update_record")); n != 0 {
		t.Errorf("update_record called %d times for a no-op", n)
	}
	if n := len(plat.callsFor("create_records")); n != 0 {
		t.Errorf("create_records called %d times for a no-op", n)
	}
}

func TestIonosDelete_ByRecordID(t *testing.T) {
	plat := newIonosStub(nil)
	ctx := newTestCtx(t, plat)

	if err := ionosTestProvider().Delete(ctx, "acme.com", "www", "CNAME"); err != nil {
		t.Fatal(err)
	}
	dr := plat.callsFor("delete_record")
	if len(dr) != 1 {
		t.Fatalf("delete_record called %d times, want 1", len(dr))
	}
	if dr[0].Input["recordId"] != "rec-www" || dr[0].Input["zoneId"] != "zone-1" {
		t.Errorf("delete targeted wrong record: %+v", dr[0].Input)
	}
}

func TestIonosZoneMissing_Errors(t *testing.T) {
	plat := newIonosStub(map[string]*sdk.ExecuteResult{
		"list_zones": {Success: true, Status: 200, Data: json.RawMessage(`[]`)},
	})
	ctx := newTestCtx(t, plat)

	if _, err := ionosTestProvider().List(ctx, "acme.com"); err == nil {
		t.Fatal("expected error when no zone matches the domain")
	}
}

func TestIonosSplitMX(t *testing.T) {
	cases := []struct {
		name        string
		rtype       string
		value       string
		wantContent string
		wantPrio    int
	}{
		{"mx with prio", "MX", "20 mail.acme.com", "mail.acme.com", 20},
		{"mx without prio defaults 10", "MX", "mail.acme.com", "mail.acme.com", 10},
		{"non-mx untouched", "A", "1.2.3.4", "1.2.3.4", 0},
		{"srv with prio", "SRV", "5 0 443 sip.acme.com", "0 443 sip.acme.com", 5},
	}
	for _, tc := range cases {
		gotContent, gotPrio := ionosSplitMX(tc.rtype, tc.value)
		if gotContent != tc.wantContent || gotPrio != tc.wantPrio {
			t.Errorf("%s: ionosSplitMX(%q,%q) = (%q,%d), want (%q,%d)",
				tc.name, tc.rtype, tc.value, gotContent, gotPrio, tc.wantContent, tc.wantPrio)
		}
	}
}

func TestProviderFor_RoutesIonosSlug(t *testing.T) {
	plat := newIonosStub(nil)
	ctx := newTestCtx(t, plat)
	app := &App{}

	prov, _, err := app.providerFor(ctx, 1) // connID>0 → GetConnection → "ionos"
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := prov.(*ionosProvider); !ok {
		t.Fatalf("providerFor routed to %T, want *ionosProvider", prov)
	}
}
