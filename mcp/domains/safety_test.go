package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func successJSON(v any) *sdk.ExecuteResult {
	b, _ := json.Marshal(v)
	return &sdk.ExecuteResult{Success: true, Status: 200, Data: b}
}

func TestSpaceshipRecoveryAndRollback(t *testing.T) {
	for _, failure := range []string{"", "delete", "save", "rollback"} {
		t.Run(failure, func(t *testing.T) {
			state := []map[string]any{{"type": "TXT", "name": "_verify", "value": "Old", "ttl": 600}}
			plat := newSpaceshipStub(nil)
			saves := 0
			plat.executeFn = func(_ int64, tool string, in map[string]any) (*sdk.ExecuteResult, error) {
				switch tool {
				case "list_dns_records":
					return successJSON(map[string]any{"items": state}), nil
				case "delete_dns_records":
					if failure == "delete" {
						return nil, errors.New("timeout")
					}
					state = []map[string]any{}
					return successJSON(nil), nil
				case "save_dns_records":
					saves++
					if failure == "rollback" || failure == "save" && saves == 1 {
						return nil, errors.New("save timeout")
					}
					for _, x := range in["items"].([]any) {
						state = append(state, x.(map[string]any))
					}
					return successJSON(nil), nil
				default:
					return nil, fmt.Errorf("unexpected %s", tool)
				}
			}
			ctx := newTestCtx(t, plat)
			p := spaceshipTestProvider()
			rows, err := p.List(ctx, "example.com")
			if err != nil {
				t.Fatal(err)
			}
			_, err = p.Upsert(ctx, "example.com", "_verify", "TXT", "New", 600, "", rows)
			if (err != nil) != (failure != "") {
				t.Fatalf("failure=%s err=%v", failure, err)
			}
			var id, status string
			if err := ctx.AppDB().QueryRow("SELECT id,status FROM dns_recoveries").Scan(&id, &status); err != nil {
				t.Fatal(err)
			}
			if failure == "" && status != "succeeded" || failure != "" && status != "unknown" {
				t.Fatalf("status=%s", status)
			}
			if failure == "rollback" && !strings.Contains(err.Error(), "rollback failed") {
				t.Fatal(err)
			}
			result, err := (&App{}).toolDNSRecovery(ctx, map[string]any{"recovery_id": id})
			if err != nil {
				t.Fatal(err)
			}
			want := "restored"
			if failure == "" {
				want = "succeeded"
			}
			if failure == "rollback" {
				want = "unknown"
			}
			if result.(map[string]any)["status"] != want {
				t.Fatal(result)
			}
		})
	}
}

func TestSpaceshipDeleteBatches(t *testing.T) {
	plat := newSpaceshipStub(nil)
	ctx := newTestCtx(t, plat)
	rows := make([]DNSRecord, 1001)
	for i := range rows {
		rows[i] = DNSRecord{ID: fmt.Sprint(i), Name: "_verify", Type: "TXT", Value: fmt.Sprint(i), TTL: 600}
	}
	if err := spaceshipTestProvider().Delete(ctx, "example.com", "_verify", "TXT", "", rows); err != nil {
		t.Fatal(err)
	}
	calls := plat.callsFor("delete_dns_records")
	if len(calls) != 3 {
		t.Fatal(len(calls))
	}
	for _, c := range calls {
		if len(c.Input["records"].([]any)) > 500 {
			t.Fatal("oversized delete")
		}
	}
}

func TestNamecheapPreservesCAAAndMailMode(t *testing.T) {
	body := `<ApiResponse Status="OK"><CommandResponse><DomainDNSGetHostsResult Domain="example.com" EmailType="FWD" IsUsingOurDNS="false"><Host HostId="caa" Name="@" Type="CAA" Address="letsencrypt.org" Flag="0" Tag="issue" TTL="600"/><Host HostId="mail" Name="@" Type="MX" Address="mail.example.com" MXPref="0" TTL="600"/></DomainDNSGetHostsResult></CommandResponse></ApiResponse>`
	plat := newNamecheapStub(map[string]*sdk.ExecuteResult{"get_dns_hosts": {Success: true, Status: 200, Data: jsonStringWrap(body)}, "set_dns_hosts": {Success: true, Status: 200, Data: jsonStringWrap(`<ApiResponse Status="OK"><CommandResponse><DomainDNSSetHostsResult IsSuccess="true"/></CommandResponse></ApiResponse>`)}})
	ctx := newTestCtx(t, plat)
	p := &namecheapProvider{bound: &sdk.BoundIntegration{AppSlug: "namecheap", ConnectionID: 1}}
	rows, err := p.List(ctx, "example.com")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Value != "0 issue letsencrypt.org" || len(rows[0].Warnings) != 1 {
		t.Fatal(rows)
	}
	_, err = p.Upsert(ctx, "example.com", "www", "A", "192.0.2.3", 600, createRecordID, rows)
	if err != nil {
		t.Fatal(err)
	}
	in := plat.callsFor("set_dns_hosts")[0].Input
	for key, want := range map[string]string{"EmailType": "FWD", "Flag1": "0", "Tag1": "issue", "Address1": "letsencrypt.org", "MXPref2": "0", "HostName3": "www"} {
		if in[key] != want {
			t.Fatalf("%s=%v want %s", key, in[key], want)
		}
	}
}

func TestNamecheapRejectsConcurrentZoneChange(t *testing.T) {
	plat := newNamecheapStub(nil)
	ctx := newTestCtx(t, plat)
	p := &namecheapProvider{bound: &sdk.BoundIntegration{AppSlug: "namecheap", ConnectionID: 1}}
	rows, err := p.List(ctx, "acme.com")
	if err != nil {
		t.Fatal(err)
	}
	rows[0].Value = "changed-after-read"
	_, err = p.Upsert(ctx, "acme.com", "new", "TXT", "token", 600, createRecordID, rows)
	if errorStatus(err) != 409 || len(plat.callsFor("set_dns_hosts")) != 0 {
		t.Fatalf("err=%v calls=%v", err, plat.calls)
	}
}

func TestRecordsRejectStaleConnectionAndValue(t *testing.T) {
	for _, args := range []map[string]any{{"expected_connection_id": 2}, {"expected_record": map[string]any{"value": "stale", "ttl": 600, "prio": 0}, "record_id": "2"}} {
		plat := newPorkbunStub(nil)
		ctx := newTestCtx(t, plat)
		args["domain"], args["name"], args["type"], args["value"] = "acme.com", "www", "CNAME", "target.example.com"
		_, err := (&App{}).toolDomainRecordsSet(ctx, args)
		if err == nil {
			t.Fatal("stale edit accepted")
		}
		for _, c := range plat.calls {
			if c.Tool != "list_dns_records" {
				t.Fatalf("unexpected write %s", c.Tool)
			}
		}
	}
}

func TestRecordCreateAndEnsureDoNotOverwrite(t *testing.T) {
	plat := newPorkbunStub(nil)
	ctx := newTestCtx(t, plat)
	a := &App{}
	_, err := a.toolDomainRecordsSet(ctx, map[string]any{"domain": "acme.com", "name": "@", "type": "A", "value": "192.0.2.42", "mode": "create"})
	if err != nil {
		t.Fatal(err)
	}
	if len(plat.callsFor("create_dns_record")) != 1 || len(plat.callsFor("edit_dns_record")) != 0 {
		t.Fatal(plat.calls)
	}
	rows, _ := (&porkbunProvider{bound: &sdk.BoundIntegration{ConnectionID: 1, AppSlug: "porkbun"}}).List(ctx, "acme.com")
	out, err := a.toolDomainRecordsSet(ctx, map[string]any{"domain": "acme.com", "name": "@", "type": "A", "value": rows[0].Value, "mode": "ensure"})
	if err != nil {
		t.Fatal(err)
	}
	if out.(map[string]any)["action"] != "unchanged" || len(plat.callsFor("create_dns_record")) != 1 {
		t.Fatal(out, plat.calls)
	}
}

func TestIonosCacheAndDisabledRoundTrip(t *testing.T) {
	plat := newIonosStub(nil)
	plat.replyByTool["get_zone"] = successJSON(map[string]any{"id": "zone-1", "name": "acme.com", "records": []map[string]any{{"id": "srv", "name": "_sip._tcp.acme.com", "type": "SRV", "content": "0 443 sip.acme.com", "ttl": 600, "prio": 0, "disabled": true}}})
	ctx := newTestCtx(t, plat)
	p := ionosTestProvider()
	rows, err := p.List(ctx, "acme.com")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ionosTestProvider().List(ctx, "acme.com"); err != nil {
		t.Fatal(err)
	}
	if len(plat.callsFor("list_zones")) != 1 || len(plat.callsFor("get_zone")) != 2 {
		t.Fatal(plat.calls)
	}
	if _, err = p.Upsert(ctx, "acme.com", "_sip._tcp", "SRV", "0 0 444 sip.acme.com", 600, "srv", rows); err != nil {
		t.Fatal(err)
	}
	in := plat.callsFor("update_record")[0].Input
	if in["disabled"] != true || in["prio"] != 0 {
		t.Fatal(in)
	}
}

func testPurchasePlatform() *stubPlatform {
	p := newPorkbunStub(nil)
	p.executeFn = func(_ int64, tool string, in map[string]any) (*sdk.ExecuteResult, error) {
		switch tool {
		case "check_availability":
			return &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(porkbunAvailableJSON)}, nil
		case "register_domain":
			body := map[string]any{"status": "SUCCESS", "domain": in["domain"], "cost": in["cost"], "orderId": 123}
			if in["dryRun"] == true {
				body["dryRun"] = true
				body["wouldSucceed"] = true
				body["duration"] = 1
			}
			return successJSON(body), nil
		default:
			return nil, fmt.Errorf("unexpected %s", tool)
		}
	}
	return p
}
func prepareTestPurchase(t *testing.T, ctx *sdk.AppCtx) string {
	t.Helper()
	v, err := (&App{}).toolDomainRegistrationPrepare(ctx, map[string]any{"domain": "fresh-example.com"})
	if err != nil {
		t.Fatal(err)
	}
	return v.(map[string]any)["confirmation_token"].(string)
}

func TestPurchaseUnknownResumeAndImmutableReplay(t *testing.T) {
	plat := testPurchasePlatform()
	ctx := newTestCtx(t, plat)
	a := &App{}
	token := prepareTestPurchase(t, ctx)
	original := plat.executeFn
	failed := false
	plat.executeFn = func(id int64, tool string, in map[string]any) (*sdk.ExecuteResult, error) {
		if tool == "register_domain" && !failed {
			failed = true
			return nil, errors.New("connection lost after charge")
		}
		return original(id, tool, in)
	}
	if _, err := a.toolDomainRegister(ctx, map[string]any{"confirmation_token": token}); err == nil {
		t.Fatal("expected unknown outcome")
	}
	in, err := dbRegistrationIntentGet(ctx.AppDB(), "test-proj", token)
	if err != nil || in.Status != "unknown" {
		t.Fatal(in, err)
	}
	if _, err := a.toolDomainRegistrationPrepare(ctx, map[string]any{"domain": "fresh-example.com"}); err == nil {
		t.Fatal("new purchase while unresolved")
	}
	if _, err := a.toolDomainRegister(ctx, map[string]any{"confirmation_token": token}); err == nil {
		t.Fatal("blind retry accepted")
	}
	if _, err := a.toolDomainRegister(ctx, map[string]any{"confirmation_token": token, "resume": true}); err != nil {
		t.Fatal(err)
	}
	calls := plat.callsFor("register_domain")
	if len(calls) != 3 || calls[1].Input["idempotency_key"] != token || calls[2].Input["idempotency_key"] != token {
		t.Fatal(calls)
	}
	if _, err := a.toolDomainRemove(ctx, map[string]any{"name": "fresh-example.com"}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.toolDomainRegister(ctx, map[string]any{"confirmation_token": token}); err != nil {
		t.Fatal(err)
	}
	d, err := dbDomainGetByName(ctx.AppDB(), "test-proj", "fresh-example.com")
	if err != nil || d != nil {
		t.Fatalf("replay recreated inventory %v %v", d, err)
	}
}

func TestPurchaseConcurrentClaim(t *testing.T) {
	plat := testPurchasePlatform()
	ctx := newTestCtx(t, plat)
	token := prepareTestPurchase(t, ctx)
	original := plat.executeFn
	started, finish := make(chan struct{}), make(chan struct{})
	var once sync.Once
	plat.executeFn = func(id int64, tool string, in map[string]any) (*sdk.ExecuteResult, error) {
		if tool == "register_domain" {
			once.Do(func() { close(started) })
			<-finish
		}
		return original(id, tool, in)
	}
	result := make(chan error, 1)
	go func() {
		_, err := (&App{}).toolDomainRegister(ctx, map[string]any{"confirmation_token": token})
		result <- err
	}()
	<-started
	_, err := (&App{}).toolDomainRegister(ctx, map[string]any{"confirmation_token": token, "resume": true})
	close(finish)
	if err == nil {
		t.Fatal("concurrent claim accepted")
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if len(plat.callsFor("register_domain")) != 2 {
		t.Fatal(plat.calls)
	}
}

func TestPurchaseIdempotencyWindowDoesNotExtend(t *testing.T) {
	plat := testPurchasePlatform()
	ctx := newTestCtx(t, plat)
	token := prepareTestPurchase(t, ctx)
	_, err := ctx.AppDB().Exec(`UPDATE registration_intents SET status='unknown',attempted_at=? WHERE token=?`, time.Now().Add(-24*time.Hour).Format(time.RFC3339), token)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (&App{}).toolDomainRegister(ctx, map[string]any{"confirmation_token": token, "resume": true}); err == nil {
		t.Fatal("expired provider key replayed")
	}
	if len(plat.callsFor("register_domain")) != 1 {
		t.Fatal(plat.calls)
	}
}

func TestSafetyMigrationPreservesPinsAndExpiresOldQuotes(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	entries, err := os.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() >= "004" {
			continue
		}
		b, _ := os.ReadFile("migrations/" + e.Name())
		if _, err := db.Exec(string(b)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO domains(project_id,name,connection_id)VALUES('p','pinned.example',7),('p','unmanaged.example',NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO registration_intents(token,project_id,domain,years,auto_renew,whois_privacy,provider_slug,connection_id,expires_at)VALUES('legacy','p','new.example',1,1,1,'porkbun',7,'2030-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile("migrations/004_safety.sql")
	if _, err := db.Exec(string(b)); err != nil {
		t.Fatal(err)
	}
	var mode, status string
	db.QueryRow(`SELECT connection_mode FROM domains WHERE name='pinned.example'`).Scan(&mode)
	if mode != "pinned" {
		t.Fatal(mode)
	}
	db.QueryRow(`SELECT status FROM registration_intents`).Scan(&status)
	if status != "expired" {
		t.Fatal(status)
	}
	db.QueryRow(`SELECT connection_mode FROM domains WHERE name='unmanaged.example'`).Scan(&mode)
	if mode != "unmanaged" {
		t.Fatal(mode)
	}
}

func TestMutationLocksReleaseAndCancel(t *testing.T) {
	unlock, err := acquireDNSMutation(nil, "lock.example")
	if err != nil {
		t.Fatal(err)
	}
	cancel := make(chan struct{})
	close(cancel)
	if _, err := acquireDNSMutation(cancel, "lock.example"); err == nil {
		t.Fatal("cancel ignored")
	}
	unlock()
	unlock()
	mutationLocks.Lock()
	defer mutationLocks.Unlock()
	if _, ok := mutationLocks.entries["lock.example"]; ok {
		t.Fatal("lock leaked")
	}
}

func TestSpaceshipSRVWireContractAndPriorityZero(t *testing.T) {
	item, err := spaceshipRecordItem("example.com", "_sip._tcp.voice", "SRV", "0 0 443 sip.example.com", 600)
	if err != nil {
		t.Fatal(err)
	}
	if item["service"] != "_sip" || item["protocol"] != "_tcp" || item["name"] != "voice" || item["priority"] != 0 {
		t.Fatal(item)
	}
	b, _ := json.Marshal(map[string]any{"items": []any{item}})
	rows, err := parseSpaceshipRecords("example.com", b)
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].Name != "_sip._tcp.voice" || rows[0].Prio != 0 || rows[0].Value != "0 443 sip.example.com" {
		t.Fatal(rows)
	}
	if err := validateRecordValue("spaceship", "A", "192.0.2.1", 3601); err == nil {
		t.Fatal("TTL exceeds provider maximum")
	}
}

func TestToolSchemaRejectsBadTypesAndUnknownFields(t *testing.T) {
	ctx := newTestCtx(t, newPorkbunStub(nil))
	for _, tool := range (&App{}).MCPTools() {
		if tool.Name != "domain_records_set" {
			continue
		}
		for _, extra := range []map[string]any{{"ttl": 1.5}, {"connection_id": 1}, {"expected_record": "bad"}, {"unexpected": true}} {
			args := map[string]any{"domain": "acme.com", "name": "www", "type": "A", "value": "192.0.2.1"}
			for k, v := range extra {
				args[k] = v
			}
			if _, err := tool.Handler(ctx, args); err == nil {
				t.Fatalf("accepted %v", extra)
			}
		}
	}
}

func TestNamecheapUnknownMailModeFailsClosed(t *testing.T) {
	plat := newNamecheapStub(nil)
	ctx := newTestCtx(t, plat)
	p := &namecheapProvider{bound: &sdk.BoundIntegration{AppSlug: "namecheap", ConnectionID: 1}}
	rows, err := p.List(ctx, "acme.com")
	if err != nil {
		t.Fatal(err)
	}
	for i := range rows {
		delete(rows[i].Raw, "email_type")
	}
	if _, err := p.Upsert(ctx, "acme.com", "other", "TXT", "token", 600, createRecordID, rows); err == nil {
		t.Fatal("lossy mail mode accepted")
	}
	if len(plat.callsFor("set_dns_hosts")) != 0 {
		t.Fatal(plat.calls)
	}
}

func TestDomainUpdateClearsPinAndNotes(t *testing.T) {
	plat := newPorkbunStub(nil)
	ctx := newTestCtx(t, plat)
	a := &App{}
	if _, err := upsertDomainInventory(ctx, "test-proj", "acme.com", "porkbun", "porkbun", "clear me", 1); err != nil {
		t.Fatal(err)
	}
	out, err := a.toolDomainUpdate(ctx, map[string]any{"name": "acme.com", "connection_id": 0, "notes": ""})
	if err != nil {
		t.Fatal(err)
	}
	d := out.(map[string]any)["domain"].(*Domain)
	if d.ConnectionMode != "unmanaged" || d.ConnectionID != 0 || d.Notes != "" {
		t.Fatal(d)
	}
	if _, err := a.toolDomainRecordsList(ctx, map[string]any{"domain": "acme.com"}); err == nil {
		t.Fatal("unmanaged domain used default")
	}
}

func TestDomainExpirySync(t *testing.T) {
	plat := newPorkbunStub(map[string]*sdk.ExecuteResult{"list_domains": successJSON(map[string]any{"status": "SUCCESS", "domains": []any{map[string]any{"domain": "acme.com", "expireDate": "2030-04-05 00:00:00"}}})})
	ctx := newTestCtx(t, plat)
	upsertDomainInventory(ctx, "test-proj", "acme.com", "porkbun", "porkbun", "", 1)
	out, err := (&App{}).toolDomainSync(ctx, map[string]any{"name": "acme.com"})
	if err != nil {
		t.Fatal(err)
	}
	if out.(map[string]any)["domain"].(*Domain).ExpiresAt == "" {
		t.Fatal(out)
	}
}

func TestNamecheapExplicitMailModePreservesUnrelatedRecords(t *testing.T) {
	body := strings.ReplaceAll(namecheapHostsXML, ` EmailType="MX"`, "")
	plat := newNamecheapStub(map[string]*sdk.ExecuteResult{"get_dns_hosts": {Success: true, Status: 200, Data: jsonStringWrap(body)}, "set_dns_hosts": {Success: true, Status: 200, Data: jsonStringWrap(`<ApiResponse Status="OK"><CommandResponse><DomainDNSSetHostsResult IsSuccess="true"/></CommandResponse></ApiResponse>`)}})
	plat.connSlug = "namecheap"
	ctx := newTestCtx(t, plat)
	_, err := (&App{}).toolDomainRecordsSet(ctx, map[string]any{"domain": "acme.com", "name": "verify", "type": "TXT", "value": "token", "mode": "create", "namecheap_email_type": "FWD"})
	if err != nil {
		t.Fatal(err)
	}
	in := plat.callsFor("set_dns_hosts")[0].Input
	if in["EmailType"] != "FWD" || in["Address3"] != "mx.acme.com" || in["Address4"] != "token" {
		t.Fatal(in)
	}
}

func TestUnresolvedDNSRecoveryBlocksAnotherWrite(t *testing.T) {
	plat := newSpaceshipStub(nil)
	ctx := newTestCtx(t, plat)
	_, err := ctx.AppDB().Exec(`INSERT INTO dns_recoveries(id,project_id,connection_id,domain,previous_json,desired_json)VALUES('pending','test-proj',1,'example.com','{}','{}')`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = (&App{}).toolDomainRecordsSet(ctx, map[string]any{"domain": "example.com", "name": "www", "type": "A", "value": "192.0.2.1"})
	if errorStatus(err) != 409 || len(plat.calls) != 0 {
		t.Fatal(err, plat.calls)
	}
}
