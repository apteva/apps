package main

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// These tests assert desired safety properties that failed on v0.5.2.
// All provider calls are stubbed; no external DNS changes or purchases occur.
func TestAuditTXTCaseChange(t *testing.T) {
	for _, slug := range []string{"porkbun", "ionos", "spaceship"} {
		t.Run(slug, func(t *testing.T) {
			plat := &stubPlatform{connSlug: slug}
			if slug == "spaceship" {
				plat.replyByTool = map[string]*sdk.ExecuteResult{"list_dns_records": {Success: true, Status: 200, Data: json.RawMessage(`{"items":[{"type":"TXT","name":"_verify","value":"TokenABC","ttl":600}]}`)}}
			}
			ctx := newTestCtx(t, plat)
			bound := &sdk.BoundIntegration{AppSlug: slug, ConnectionID: 1}
			var p dnsProviderImpl
			switch slug {
			case "porkbun":
				p = &porkbunProvider{bound}
			case "ionos":
				p = &ionosProvider{bound: bound, zoneID: "zone"}
			case "spaceship":
				p = &spaceshipProvider{bound}
			}
			action, err := p.Upsert(ctx, "example.com", "_verify", "TXT", "TokenABC", 600, "id", []DNSRecord{{ID: "id", Name: "_verify", Type: "TXT", Value: "tokenabc", TTL: 600}})
			if err != nil {
				t.Fatal(err)
			}
			if action == "unchanged" {
				t.Fatal("case-sensitive TXT change silently skipped")
			}
		})
	}
}

func TestAuditSpaceshipExactDeleteCollision(t *testing.T) {
	plat := newSpaceshipStub(nil)
	ctx := newTestCtx(t, plat)
	records, err := parseSpaceshipRecords("example.com", json.RawMessage(`{"items":[{"type":"TXT","name":"_verify","value":"ABC","ttl":600},{"type":"TXT","name":"_verify","value":"abc","ttl":600}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := spaceshipTestProvider().Delete(ctx, "example.com", "_verify", "TXT", records[0].ID, records); err != nil {
		t.Fatal(err)
	}
	items := plat.callsFor("delete_dns_records")[0].Input["records"].([]any)
	if len(items) != 1 {
		t.Fatalf("exact delete sent %d records; IDs are %q and %q", len(items), records[0].ID, records[1].ID)
	}
}

func TestAuditSpaceshipMXIDsIncludePriority(t *testing.T) {
	records, err := parseSpaceshipRecords("example.com", json.RawMessage(`{"items":[{"type":"MX","name":"@","exchange":"mx.example.com","preference":10},{"type":"MX","name":"@","exchange":"mx.example.com","preference":20}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if records[0].ID == records[1].ID {
		t.Fatal("MX records with distinct preferences share an ID")
	}
}

func TestAuditNamecheapDeleteHonorsRRset(t *testing.T) {
	plat := newNamecheapStub(map[string]*sdk.ExecuteResult{"set_dns_hosts": {Success: true, Status: 200, Data: jsonStringWrap(`<ApiResponse Status="OK"><CommandResponse><DomainDNSSetHostsResult IsSuccess="true"/></CommandResponse></ApiResponse>`)}})
	ctx := newTestCtx(t, plat)
	p := &namecheapProvider{bound: &sdk.BoundIntegration{AppSlug: "namecheap", ConnectionID: 1}}
	err := p.Delete(ctx, "example.com", "www", "A", "mail-id", []DNSRecord{{ID: "mail-id", Name: "@", Type: "MX", Value: "mail.example.com", Prio: 10, TTL: 600}})
	if err == nil {
		t.Fatal("requested www A deletion removed an unrelated apex MX record by ID")
	}
}

func TestAuditIonosZeroPriority(t *testing.T) {
	_, prio := ionosSplitMX("MX", "0 mail.example.com")
	if prio != 0 {
		t.Fatalf("priority 0 changed to %d", prio)
	}
}

func TestAuditNamecheapPreservesZeroPriority(t *testing.T) {
	plat := newNamecheapStub(map[string]*sdk.ExecuteResult{"set_dns_hosts": {Success: true, Status: 200, Data: jsonStringWrap(`<ApiResponse Status="OK"><CommandResponse><DomainDNSSetHostsResult IsSuccess="true"/></CommandResponse></ApiResponse>`)}})
	ctx := newTestCtx(t, plat)
	p := &namecheapProvider{bound: &sdk.BoundIntegration{AppSlug: "namecheap", ConnectionID: 1}}
	err := p.writeHosts(ctx, "example.com", namecheapHostsFromRecords([]DNSRecord{{Name: "@", Type: "MX", Value: "mail.example.com", Prio: 0, TTL: 600}}))
	if err != nil {
		t.Fatal(err)
	}
	if got := plat.callsFor("set_dns_hosts")[0].Input["MXPref1"]; got != "0" {
		t.Fatalf("existing priority-zero MX rewritten with MXPref1=%v", got)
	}
}

func TestAuditUnboundDomainDoesNotFallback(t *testing.T) {
	plat := newPorkbunStub(nil)
	ctx := newTestCtx(t, plat)
	a := &App{}
	_, err := a.toolDomainAdd(ctx, map[string]any{"name": "external.example", "skip_validation": true, "use_default_connection": false})
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.toolDomainRecordsSet(ctx, map[string]any{"domain": "external.example", "name": "www", "type": "A", "value": "192.0.2.1"})
	if err == nil {
		t.Fatalf("external domain mutated through default connection; calls=%+v", plat.calls)
	}
}

func TestAuditInventoryErrorFailsClosed(t *testing.T) {
	plat := newPorkbunStub(nil)
	ctx := newTestCtx(t, plat)
	if _, err := ctx.AppDB().Exec("DROP TABLE domains"); err != nil {
		t.Fatal(err)
	}
	_, err := (&App{}).toolDomainRecordsSet(ctx, map[string]any{"domain": "example.com", "name": "www", "type": "A", "value": "192.0.2.1"})
	if err == nil {
		t.Fatal("inventory SQL error ignored; DNS write used default connection")
	}
}

func TestAuditHTTPRecordsPropagatesProject(t *testing.T) {
	plat := newPorkbunStub(nil)
	ctx := newTestCtx(t, plat)
	a := &App{}
	if _, err := upsertDomainInventory(ctx, "p2", "example.com", "porkbun", "porkbun", "", 2); err != nil {
		t.Fatal(err)
	}
	t.Setenv("APTEVA_PROJECT_ID", "")
	w := httptest.NewRecorder()
	a.handleDomainItem(w, httptest.NewRequest("GET", "/domains/example.com/records?project_id=p2", nil))
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	calls := plat.callsFor("list_dns_records")
	if len(calls) != 1 || calls[0].ConnID != 2 {
		t.Fatalf("HTTP route lost project p2; calls=%+v", calls)
	}
}

func TestAuditApexFilter(t *testing.T) {
	plat := newPorkbunStub(nil)
	ctx := newTestCtx(t, plat)
	out, err := (&App{}).toolDomainRecordsList(ctx, map[string]any{"domain": "acme.com", "name": "@"})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range out.(map[string]any)["records"].([]DNSRecord) {
		if r.Name != "acme.com" && r.Name != "@" && r.Name != "" {
			t.Fatalf("apex filter included %s", r.Name)
		}
	}
}

func TestAuditPorkbunSRVPriority(t *testing.T) {
	plat := newPorkbunStub(nil)
	ctx := newTestCtx(t, plat)
	p := &porkbunProvider{bound: &sdk.BoundIntegration{AppSlug: "porkbun", ConnectionID: 1}}
	_, err := p.Upsert(ctx, "example.com", "_sip._tcp", "SRV", "10 5 443 sip.example.com", 600, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	in := plat.callsFor("create_dns_record")[0].Input
	if in["prio"] != "10" || in["content"] != "5 443 sip.example.com" {
		t.Fatalf("SRV priority not separated: %+v", in)
	}
}

func TestAuditRegistrationRequiredFields(t *testing.T) {
	plat := newPorkbunStub(map[string]*sdk.ExecuteResult{"register_domain": {Success: true, Status: 200, Data: json.RawMessage(`{"status":"SUCCESS","domain":"example.com","cost":1000,"orderId":1}`)}})
	ctx := newTestCtx(t, plat)
	p := &porkbunRegistrar{bound: &sdk.BoundIntegration{AppSlug: "porkbun", ConnectionID: 1}}
	_, err := p.Register(ctx, DomainRegistrationRequest{Domain: "example.com", Years: 1, CostCents: 1000, IdempotencyKey: "test-key"})
	if err != nil {
		t.Fatal(err)
	}
	in := plat.callsFor("register_domain")[0].Input
	if in["cost"] == nil || in["agreeToTerms"] == nil {
		t.Fatalf("Porkbun-required cost and agreeToTerms absent: %+v", in)
	}
}

func TestAuditPorkbunRejectsMissingSuccess(t *testing.T) {
	plat := newPorkbunStub(map[string]*sdk.ExecuteResult{"register_domain": {Success: true, Status: 200, Data: json.RawMessage(`{}`)}})
	ctx := newTestCtx(t, plat)
	_, err := (&porkbunRegistrar{bound: &sdk.BoundIntegration{AppSlug: "porkbun", ConnectionID: 1}}).Register(ctx, DomainRegistrationRequest{Domain: "example.com", Years: 1, CostCents: 1000, IdempotencyKey: "test-key"})
	if err == nil {
		t.Fatal("empty object accepted as completed registration")
	}
}

func TestAuditReplayPreservesNewDNSBinding(t *testing.T) {
	plat := newPorkbunStub(nil)
	ctx := newTestCtx(t, plat)
	intent := &RegistrationIntent{Token: "completed", ProjectID: "test-proj", Domain: "example.com", Years: 1, Provider: "porkbun", ConnectionID: 1, ExpiresAt: time.Now().Add(time.Hour)}
	if err := dbRegistrationIntentInsert(ctx.AppDB(), intent); err != nil {
		t.Fatal(err)
	}
	if err := dbRegistrationIntentStatus(ctx.AppDB(), intent.Token, "succeeded", json.RawMessage(`{"status":"SUCCESS"}`), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := upsertDomainInventory(ctx, "test-proj", "example.com", "porkbun", "spaceship", "moved", 2); err != nil {
		t.Fatal(err)
	}
	_, err := (&App{}).toolDomainRegister(ctx, map[string]any{"confirmation_token": intent.Token})
	if err != nil {
		t.Fatal(err)
	}
	d, err := dbDomainGetByName(ctx.AppDB(), "test-proj", "example.com")
	if err != nil {
		t.Fatal(err)
	}
	if d.ConnectionID != 2 {
		t.Fatalf("successful-token replay rerouted DNS from 2 to %d", d.ConnectionID)
	}
}

func TestAuditSpaceshipImplicitUpdateRemovesOldValue(t *testing.T) {
	plat := newSpaceshipStub(map[string]*sdk.ExecuteResult{"list_dns_records": {Success: true, Status: 200, Data: json.RawMessage(`{"items":[{"type":"A","name":"www","address":"192.0.2.2","ttl":600}]}`)}})
	ctx := newTestCtx(t, plat)
	_, err := spaceshipTestProvider().Upsert(ctx, "example.com", "www", "A", "192.0.2.2", 600, "", []DNSRecord{{ID: "old", Name: "www", Type: "A", Value: "192.0.2.1", TTL: 600}})
	if err != nil {
		t.Fatal(err)
	}
	if len(plat.callsFor("delete_dns_records")) == 0 {
		t.Fatal("value change only sends additive save; previous A address remains")
	}
}

func TestAuditNamecheapRejectsUnsuccessfulSet(t *testing.T) {
	plat := newNamecheapStub(map[string]*sdk.ExecuteResult{"set_dns_hosts": {Success: true, Status: 200, Data: jsonStringWrap(`<ApiResponse Status="OK"><CommandResponse><DomainDNSSetHostsResult IsSuccess="false"/></CommandResponse></ApiResponse>`)}})
	ctx := newTestCtx(t, plat)
	err := (&namecheapProvider{bound: &sdk.BoundIntegration{AppSlug: "namecheap", ConnectionID: 1}}).writeHosts(ctx, "example.com", nil)
	if err == nil {
		t.Fatal("IsSuccess=false accepted as successful zone replacement")
	}
}

func TestAuditNamecheapRejectsMissingHostList(t *testing.T) {
	plat := newNamecheapStub(map[string]*sdk.ExecuteResult{"get_dns_hosts": {Success: true, Status: 200, Data: jsonStringWrap(`<ApiResponse Status="OK"/>`)}})
	ctx := newTestCtx(t, plat)
	_, err := (&namecheapProvider{bound: &sdk.BoundIntegration{AppSlug: "namecheap", ConnectionID: 1}}).List(ctx, "example.com")
	if err == nil {
		t.Fatal("missing host-list response treated as empty zone; subsequent set would overwrite real records")
	}
}

func TestAuditReAddPreservesPinnedConnection(t *testing.T) {
	plat := newPorkbunStub(nil)
	ctx := newTestCtx(t, plat)
	a := &App{}
	if _, err := upsertDomainInventory(ctx, "test-proj", "example.com", "porkbun", "porkbun", "", 2); err != nil {
		t.Fatal(err)
	}
	out, err := a.toolDomainAdd(ctx, map[string]any{"name": "example.com", "notes": "new notes", "skip_validation": true})
	if err != nil {
		t.Fatal(err)
	}
	if d := out.(map[string]any)["domain"].(*Domain); d.ConnectionID != 2 {
		t.Fatalf("re-add without connection ID changed existing pin to %d", d.ConnectionID)
	}
}

func TestAuditPrepareRequiresQuote(t *testing.T) {
	plat := newPorkbunStub(map[string]*sdk.ExecuteResult{"check_availability": {Success: true, Status: 200, Data: json.RawMessage(`{"status":"SUCCESS","response":{"avail":"yes"}}`)}})
	ctx := newTestCtx(t, plat)
	_, err := (&App{}).toolDomainRegistrationPrepare(ctx, map[string]any{"domain": "example.com"})
	if err == nil {
		t.Fatal("purchase token issued without a price or currency")
	}
}
