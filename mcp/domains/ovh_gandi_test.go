package main

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func okResult(body string) *sdk.ExecuteResult {
	return &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(body)}
}

// ─── OVHcloud ──────────────────────────────────────────────────────

func ovhStub(records map[int64]string) *stubPlatform {
	return &stubPlatform{connSlug: "ovhcloud", executeFn: func(_ int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
		switch tool {
		case "zone_record_list":
			ids := make([]string, 0, len(records))
			for id := range records {
				ids = append(ids, fmt.Sprint(id))
			}
			return okResult("[" + strings.Join(ids, ",") + "]"), nil
		case "zone_record_get":
			body, ok := records[input["id"].(int64)]
			if !ok {
				return &sdk.ExecuteResult{Success: false, Status: 404, Data: json.RawMessage(`{}`)}, nil
			}
			return okResult(body), nil
		case "zone_record_create", "zone_record_update", "zone_record_delete", "zone_refresh":
			return okResult(`null`), nil
		}
		return nil, fmt.Errorf("unexpected tool %q", tool)
	}}
}

func TestOVHListCanonicalisesRecords(t *testing.T) {
	plat := ovhStub(map[int64]string{
		11: `{"id":11,"zone":"example.com","fieldType":"MX","subDomain":"","target":"10 mx.example.com.","ttl":3600}`,
		12: `{"id":12,"zone":"example.com","fieldType":"TXT","subDomain":"_verify","target":"\"token-ABC\"","ttl":0}`,
		13: `{"id":13,"zone":"example.com","fieldType":"DKIM","subDomain":"s1._domainkey","target":"v=DKIM1;k=rsa;p=abc","ttl":60}`,
	})
	ctx := newTestCtx(t, plat)
	p := &ovhProvider{bound: &sdk.BoundIntegration{AppSlug: "ovhcloud", ConnectionID: 1}}
	got, err := p.List(ctx, "example.com")
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]DNSRecord{}
	for _, r := range got {
		byID[r.ID] = r
	}
	if r := byID["11"]; r.Name != "example.com" || r.Type != "MX" || r.Value != "mx.example.com." || r.Prio != 10 || r.TTL != 3600 {
		t.Fatalf("MX: %+v", r)
	}
	if r := byID["12"]; r.Name != "_verify.example.com" || r.Value != "token-ABC" {
		t.Fatalf("TXT: %+v", r)
	}
	if r := byID["13"]; r.Type != "TXT" || !strings.Contains(r.Notes, "DKIM") {
		t.Fatalf("DKIM: %+v", r)
	}
}

func TestOVHListRejectsMalformedIDs(t *testing.T) {
	plat := &stubPlatform{executeFn: func(int64, string, map[string]any) (*sdk.ExecuteResult, error) {
		return okResult(`{"records":[]}`), nil
	}}
	ctx := newTestCtx(t, plat)
	p := &ovhProvider{bound: &sdk.BoundIntegration{AppSlug: "ovhcloud", ConnectionID: 1}}
	if _, err := p.List(ctx, "example.com"); err == nil {
		t.Fatal("expected an error for a non-array id list")
	}
}

func TestOVHUpsertUpdatesByIDAndRefreshes(t *testing.T) {
	plat := ovhStub(nil)
	ctx := newTestCtx(t, plat)
	p := &ovhProvider{bound: &sdk.BoundIntegration{AppSlug: "ovhcloud", ConnectionID: 1}}
	existing := []DNSRecord{{ID: "11", Name: "example.com", Type: "MX", Value: "mx.example.com.", Prio: 10, TTL: 3600}}
	action, err := p.Upsert(ctx, "example.com", "", "MX", "20 mx2.example.com", 3600, "", existing)
	if err != nil || action != "updated" {
		t.Fatalf("action=%q err=%v", action, err)
	}
	upd := plat.callsFor("zone_record_update")
	if len(upd) != 1 || upd[0].Input["id"] != int64(11) || upd[0].Input["target"] != "20 mx2.example.com" {
		t.Fatalf("update calls: %+v", upd)
	}
	if len(plat.callsFor("zone_refresh")) != 1 {
		t.Fatal("zone was not refreshed after update")
	}
}

func TestOVHUpsertCreatesAndUnchanged(t *testing.T) {
	plat := ovhStub(nil)
	ctx := newTestCtx(t, plat)
	p := &ovhProvider{bound: &sdk.BoundIntegration{AppSlug: "ovhcloud", ConnectionID: 1}}
	action, err := p.Upsert(ctx, "example.com", "www", "A", "192.0.2.1", 300, "", nil)
	if err != nil || action != "created" {
		t.Fatalf("action=%q err=%v", action, err)
	}
	c := plat.callsFor("zone_record_create")[0].Input
	if c["subDomain"] != "www" || c["fieldType"] != "A" || c["target"] != "192.0.2.1" || c["ttl"] != 300 {
		t.Fatalf("create input: %+v", c)
	}
	existing := []DNSRecord{{ID: "5", Name: "www.example.com", Type: "A", Value: "192.0.2.1", TTL: 300}}
	if action, _ = p.Upsert(ctx, "example.com", "www", "A", "192.0.2.1", 300, "", existing); action != "unchanged" {
		t.Fatalf("expected unchanged, got %q", action)
	}
	if len(plat.callsFor("zone_refresh")) != 1 {
		t.Fatal("unchanged upsert must not refresh the zone")
	}
}

func TestOVHRefreshFailureIsReported(t *testing.T) {
	plat := ovhStub(nil)
	base := plat.executeFn
	plat.executeFn = func(id int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
		if tool == "zone_refresh" {
			return &sdk.ExecuteResult{Success: false, Status: 500, Data: json.RawMessage(`{"message":"boom"}`)}, nil
		}
		return base(id, tool, input)
	}
	ctx := newTestCtx(t, plat)
	p := &ovhProvider{bound: &sdk.BoundIntegration{AppSlug: "ovhcloud", ConnectionID: 1}}
	_, err := p.Upsert(ctx, "example.com", "www", "A", "192.0.2.1", 300, "", nil)
	if err == nil || !strings.Contains(err.Error(), "not live") {
		t.Fatalf("expected refresh error, got %v", err)
	}
}

func TestOVHDeleteExactRecord(t *testing.T) {
	plat := ovhStub(nil)
	ctx := newTestCtx(t, plat)
	p := &ovhProvider{bound: &sdk.BoundIntegration{AppSlug: "ovhcloud", ConnectionID: 1}}
	existing := []DNSRecord{
		{ID: "1", Name: "www.example.com", Type: "A", Value: "192.0.2.1"},
		{ID: "2", Name: "www.example.com", Type: "A", Value: "192.0.2.2"},
		{ID: "3", Name: "example.com", Type: "MX", Value: "mx.example.com"},
	}
	if err := p.Delete(ctx, "example.com", "www", "A", "2", existing); err != nil {
		t.Fatal(err)
	}
	dels := plat.callsFor("zone_record_delete")
	if len(dels) != 1 || dels[0].Input["id"] != int64(2) {
		t.Fatalf("delete calls: %+v", dels)
	}
	if err := p.Delete(ctx, "example.com", "www", "A", "3", existing); err == nil {
		t.Fatal("record id from another RRset was accepted")
	}
}

// ─── Gandi ─────────────────────────────────────────────────────────

func gandiStub(pages ...string) *stubPlatform {
	return &stubPlatform{connSlug: "gandi", executeFn: func(_ int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
		switch tool {
		case "list_records":
			page := input["page"].(int)
			if page <= len(pages) {
				return okResult(pages[page-1]), nil
			}
			return okResult(`[]`), nil
		case "create_record", "replace_record", "delete_record":
			return okResult(`{"message":"ok"}`), nil
		}
		return nil, fmt.Errorf("unexpected tool %q", tool)
	}}
}

const gandiZone = `[
 {"rrset_name":"@","rrset_type":"MX","rrset_ttl":10800,"rrset_values":["10 mx1.example.net.","20 mx2.example.net."]},
 {"rrset_name":"www","rrset_type":"A","rrset_ttl":300,"rrset_values":["192.0.2.1"]},
 {"rrset_name":"@","rrset_type":"TXT","rrset_ttl":300,"rrset_values":["\"v=spf1 -all\"","\"part one\" \"part two\""]}
]`

func gandiTestProvider() *gandiProvider {
	return &gandiProvider{bound: &sdk.BoundIntegration{AppSlug: "gandi", ConnectionID: 1}}
}

func TestGandiListExpandsRRsets(t *testing.T) {
	ctx := newTestCtx(t, gandiStub(gandiZone))
	got, err := gandiTestProvider().List(ctx, "example.com")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 {
		t.Fatalf("want 5 records, got %d: %+v", len(got), got)
	}
	if got[0].Name != "example.com" || got[0].Prio != 10 || got[0].Value != "mx1.example.net." {
		t.Fatalf("MX: %+v", got[0])
	}
	if got[2].Name != "www.example.com" || got[2].Value != "192.0.2.1" {
		t.Fatalf("A: %+v", got[2])
	}
	if got[3].Value != "v=spf1 -all" || got[4].Value != "part onepart two" {
		t.Fatalf("TXT: %+v / %+v", got[3], got[4])
	}
	if got[0].ID == got[1].ID {
		t.Fatal("values in one RRset share an ID")
	}
}

func TestGandiListPaginates(t *testing.T) {
	var full []string
	for i := 0; i < gandiPageSize; i++ {
		full = append(full, fmt.Sprintf(`{"rrset_name":"h%d","rrset_type":"A","rrset_ttl":300,"rrset_values":["192.0.2.1"]}`, i))
	}
	plat := gandiStub("["+strings.Join(full, ",")+"]", `[{"rrset_name":"last","rrset_type":"A","rrset_ttl":300,"rrset_values":["192.0.2.9"]}]`)
	ctx := newTestCtx(t, plat)
	got, err := gandiTestProvider().List(ctx, "example.com")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != gandiPageSize+1 || len(plat.callsFor("list_records")) != 2 {
		t.Fatalf("got %d records over %d pages", len(got), len(plat.callsFor("list_records")))
	}
}

func TestGandiUpdateOneValuePreservesOthers(t *testing.T) {
	plat := gandiStub(gandiZone)
	ctx := newTestCtx(t, plat)
	p := gandiTestProvider()
	existing, err := p.List(ctx, "example.com")
	if err != nil {
		t.Fatal(err)
	}
	action, err := p.Upsert(ctx, "example.com", "", "MX", "30 mx3.example.net.", 10800, existing[1].ID, existing)
	if err != nil || action != "updated" {
		t.Fatalf("action=%q err=%v", action, err)
	}
	in := plat.callsFor("replace_record")[0].Input
	want := []string{"10 mx1.example.net.", "30 mx3.example.net."}
	if in["rrset_name"] != "@" || in["rrset_type"] != "MX" || !reflect.DeepEqual(in["rrset_values"], want) {
		t.Fatalf("replace input: %+v", in)
	}
}

func TestGandiAmbiguousUpsertWithoutRecordID(t *testing.T) {
	ctx := newTestCtx(t, gandiStub(gandiZone))
	p := gandiTestProvider()
	existing, _ := p.List(ctx, "example.com")
	if _, err := p.Upsert(ctx, "example.com", "", "MX", "10 mx9.example.net.", 10800, "", existing); err == nil {
		t.Fatal("multi-value RRset updated without record_id")
	}
}

func TestGandiCreateAndAppend(t *testing.T) {
	plat := gandiStub(gandiZone)
	ctx := newTestCtx(t, plat)
	p := gandiTestProvider()
	existing, _ := p.List(ctx, "example.com")

	action, err := p.Upsert(ctx, "example.com", "_verify", "TXT", `token "x"`, 300, "", existing)
	if err != nil || action != "created" {
		t.Fatalf("action=%q err=%v", action, err)
	}
	in := plat.callsFor("create_record")[0].Input
	if in["rrset_name"] != "_verify" || !reflect.DeepEqual(in["rrset_values"], []string{`"token \"x\""`}) {
		t.Fatalf("create input: %+v", in)
	}

	action, err = p.Upsert(ctx, "example.com", "www", "A", "192.0.2.2", 300, createRecordID, existing)
	if err != nil || action != "created" {
		t.Fatalf("append action=%q err=%v", action, err)
	}
	rep := plat.callsFor("replace_record")[0].Input
	if !reflect.DeepEqual(rep["rrset_values"], []string{"192.0.2.1", "192.0.2.2"}) {
		t.Fatalf("append input: %+v", rep)
	}
}

func TestGandiDeleteOneValueThenRRset(t *testing.T) {
	plat := gandiStub(gandiZone)
	ctx := newTestCtx(t, plat)
	p := gandiTestProvider()
	existing, _ := p.List(ctx, "example.com")

	if err := p.Delete(ctx, "example.com", "", "TXT", existing[3].ID, existing); err != nil {
		t.Fatal(err)
	}
	rep := plat.callsFor("replace_record")[0].Input
	if !reflect.DeepEqual(rep["rrset_values"], []string{`"part one" "part two"`}) || rep["rrset_ttl"] != 300 {
		t.Fatalf("partial delete input: %+v", rep)
	}
	if err := p.Delete(ctx, "example.com", "www", "A", existing[2].ID, existing); err != nil {
		t.Fatal(err)
	}
	del := plat.callsFor("delete_record")
	if len(del) != 1 || del[0].Input["rrset_name"] != "www" {
		t.Fatalf("delete calls: %+v", del)
	}
}

func TestTXTQuoting(t *testing.T) {
	long := strings.Repeat("a", 300)
	q := quoteTXT(long)
	if !strings.HasPrefix(q, `"`+strings.Repeat("a", 255)+`" "`) {
		t.Fatalf("long TXT not chunked: %q", q[:20])
	}
	if unquoteTXT(q) != long {
		t.Fatal("chunked TXT did not round-trip")
	}
	if quoteTXT(`"already"`) != `"already"` {
		t.Fatal("quoted TXT was double-quoted")
	}
	if unquoteTXT(`"unterminated`) != `"unterminated` {
		t.Fatal("malformed TXT was altered")
	}
}

func TestOVHAndGandiCapabilitiesAndDispatch(t *testing.T) {
	if c := capabilities("gandi"); c.MinTTL != 300 || c.MaxTTL != 2592000 {
		t.Fatalf("gandi caps: %+v", c)
	}
	if includes(capabilities("ovhcloud").WriteTypes, "ALIAS") {
		t.Fatal("OVH has no ALIAS type")
	}
	for slug, want := range map[string]string{"ovhcloud": "*main.ovhProvider", "gandi": "*main.gandiProvider"} {
		ctx := newTestCtx(t, &stubPlatform{connSlug: slug})
		prov, _, err := (&App{}).providerFor(ctx, 7, "")
		if err != nil {
			t.Fatal(err)
		}
		if got := fmt.Sprintf("%T", prov); got != want {
			t.Fatalf("%s dispatched to %s", slug, got)
		}
	}
}
