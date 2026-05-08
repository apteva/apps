package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

// ─── Stub PlatformClient ──────────────────────────────────────────

type stubPlatform struct {
	tk.BasePlatformClient
	mu               sync.Mutex
	calls            []executeCall
	replyByTool      map[string]*sdk.ExecuteResult
	bindingsOverride map[string]any
}

type executeCall struct {
	ConnID int64
	Tool   string
	Input  map[string]any
}

func (s *stubPlatform) ExecuteIntegrationTool(connID int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
	s.mu.Lock()
	s.calls = append(s.calls, executeCall{ConnID: connID, Tool: tool, Input: input})
	s.mu.Unlock()
	if r, ok := s.replyByTool[tool]; ok {
		return r, nil
	}
	// Default empty success.
	return &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{"status":"SUCCESS"}`)}, nil
}
func (s *stubPlatform) CallApp(string, string, map[string]any) (json.RawMessage, error) {
	return nil, nil
}
func (s *stubPlatform) CallAppResult(string, string, map[string]any, any) error { return nil }
func (s *stubPlatform) GetConnection(id int64) (*sdk.PlatformConnection, error) {
	return &sdk.PlatformConnection{ID: id, AppSlug: "porkbun", Status: "active"}, nil
}
func (s *stubPlatform) ListConnections(sdk.ConnectionFilter) ([]sdk.PlatformConnection, error) {
	return nil, nil
}
func (s *stubPlatform) GetInstance(int64) (*sdk.PlatformInstance, error) { return nil, nil }
func (s *stubPlatform) SendEvent(int64, string) error                   { return nil }
func (s *stubPlatform) SendToChannel(string, string, string) error      { return nil }
func (s *stubPlatform) WhoAmI() (*sdk.InstallIdentity, error) {
	bindings := map[string]any{"dns_provider": float64(1)}
	if s.bindingsOverride != nil {
		bindings = s.bindingsOverride
	}
	return &sdk.InstallIdentity{
		AppName:   "domains",
		ProjectID: "test-proj",
		Bindings:  bindings,
	}, nil
}
func (s *stubPlatform) StartOAuth(sdk.OAuthStartRequest) (*sdk.OAuthStartResult, error) {
	return &sdk.OAuthStartResult{}, nil
}
func (s *stubPlatform) DisconnectConnection(int64) error                        { return nil }
func (s *stubPlatform) ListOwnedConnections() ([]sdk.PlatformConnection, error) { return nil, nil }
func (s *stubPlatform) GetGrants(int64) (*sdk.GrantsResponse, error) {
	return &sdk.GrantsResponse{DefaultEffect: "allow"}, nil
}

// ─── Test harness ─────────────────────────────────────────────────

func newTestCtx(t *testing.T, plat *stubPlatform) *sdk.AppCtx {
	t.Helper()
	opts := []tk.Option{tk.WithProjectID("test-proj")}
	if plat != nil {
		opts = append(opts, tk.WithPlatform(plat))
	}
	ctx := tk.NewAppCtx(t, "apteva.yaml", opts...)
	globalCtx = ctx
	return ctx
}

// ─── Local domain CRUD ────────────────────────────────────────────

func TestDomainAdd_RoundTrips(t *testing.T) {
	ctx := newTestCtx(t, &stubPlatform{})
	app := &App{}

	out, err := app.toolDomainAdd(ctx, map[string]any{
		"name":         "Acme.com",
		"registrar":    "porkbun",
		"dns_provider": "porkbun",
		"notes":        "primary",
	})
	if err != nil {
		t.Fatal(err)
	}
	d := out.(map[string]any)["domain"].(*Domain)
	if d.Name != "acme.com" || d.RegistrarSlug != "porkbun" || d.Notes != "primary" {
		t.Errorf("domain: %+v", d)
	}
	listed, _ := app.toolDomainList(ctx, map[string]any{})
	if listed.(map[string]any)["count"].(int) != 1 {
		t.Errorf("expected 1, got %v", listed)
	}
	got, _ := app.toolDomainGet(ctx, map[string]any{"name": "acme.com"})
	if !got.(map[string]any)["found"].(bool) {
		t.Error("not found after add")
	}
}

func TestDomainAdd_Idempotent(t *testing.T) {
	ctx := newTestCtx(t, &stubPlatform{})
	app := &App{}
	args := map[string]any{"name": "acme.com", "registrar": "porkbun"}
	if _, err := app.toolDomainAdd(ctx, args); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolDomainAdd(ctx, args); err != nil {
		t.Fatal(err)
	}
	listed, _ := app.toolDomainList(ctx, map[string]any{})
	if listed.(map[string]any)["count"].(int) != 1 {
		t.Errorf("re-add should be idempotent: %v", listed)
	}
}

func TestDomainRemove_SoftDeletes(t *testing.T) {
	ctx := newTestCtx(t, &stubPlatform{})
	app := &App{}
	if _, err := app.toolDomainAdd(ctx, map[string]any{"name": "acme.com"}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolDomainRemove(ctx, map[string]any{"name": "acme.com"}); err != nil {
		t.Fatal(err)
	}
	listed, _ := app.toolDomainList(ctx, map[string]any{})
	if listed.(map[string]any)["count"].(int) != 0 {
		t.Errorf("expected 0 after remove, got %v", listed)
	}
	// Re-adding the same name should succeed (deleted_at is set, so
	// the partial-unique index doesn't conflict).
	if _, err := app.toolDomainAdd(ctx, map[string]any{"name": "acme.com"}); err != nil {
		t.Errorf("re-add after remove should work: %v", err)
	}
}

// ─── DNS record CRUD ──────────────────────────────────────────────

const porkbunListJSON = `{
	"status": "SUCCESS",
	"records": [
		{"id": "100", "name": "acme.com",      "type": "A",     "content": "1.2.3.4",      "ttl": "600", "prio": "0", "notes": ""},
		{"id": "101", "name": "www.acme.com",  "type": "CNAME", "content": "acme.com",     "ttl": "600", "prio": "0", "notes": ""},
		{"id": "102", "name": "acme.com",      "type": "MX",    "content": "mx.acme.com",  "ttl": "600", "prio": "10", "notes": ""}
	]
}`

func newPorkbunStub(extra map[string]*sdk.ExecuteResult) *stubPlatform {
	rep := map[string]*sdk.ExecuteResult{
		"list_dns_records": {Success: true, Status: 200, Data: json.RawMessage(porkbunListJSON)},
	}
	for k, v := range extra {
		rep[k] = v
	}
	return &stubPlatform{replyByTool: rep}
}

func TestDomainRecordsList_FiltersByType(t *testing.T) {
	plat := newPorkbunStub(nil)
	ctx := newTestCtx(t, plat)
	app := &App{}

	out, err := app.toolDomainRecordsList(ctx, map[string]any{"domain": "acme.com", "type": "MX"})
	if err != nil {
		t.Fatal(err)
	}
	rs := out.(map[string]any)["records"].([]DNSRecord)
	if len(rs) != 1 || rs[0].Type != "MX" || rs[0].Prio != 10 {
		t.Errorf("filter by type MX got: %+v", rs)
	}
}

func TestDomainRecordsSet_CreatesWhenAbsent(t *testing.T) {
	plat := newPorkbunStub(map[string]*sdk.ExecuteResult{
		"create_dns_record": {Success: true, Status: 200, Data: json.RawMessage(`{"status":"SUCCESS","id":"999"}`)},
	})
	ctx := newTestCtx(t, plat)
	app := &App{}

	out, err := app.toolDomainRecordsSet(ctx, map[string]any{
		"domain": "acme.com",
		"name":   "mail",
		"type":   "A",
		"value":  "5.6.7.8",
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.(map[string]any)["action"] != "created" {
		t.Errorf("expected create, got %+v", out)
	}
	tools := []string{}
	for _, c := range plat.calls {
		tools = append(tools, c.Tool)
	}
	if len(tools) != 2 || tools[0] != "list_dns_records" || tools[1] != "create_dns_record" {
		t.Errorf("dispatch order wrong: %v", tools)
	}
}

func TestDomainRecordsSet_EditsWhenPresent(t *testing.T) {
	plat := newPorkbunStub(map[string]*sdk.ExecuteResult{
		"edit_dns_records_by_type": {Success: true, Status: 200, Data: json.RawMessage(`{"status":"SUCCESS"}`)},
	})
	ctx := newTestCtx(t, plat)
	app := &App{}

	// www.acme.com CNAME exists in porkbunListJSON; setting it again
	// should hit edit, not create.
	out, err := app.toolDomainRecordsSet(ctx, map[string]any{
		"domain": "acme.com",
		"name":   "www",
		"type":   "CNAME",
		"value":  "newtarget.acme.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.(map[string]any)["action"] != "updated" {
		t.Errorf("expected update, got %+v", out)
	}
}

func TestDomainRecordsSet_MXSplitsPrioFromValue(t *testing.T) {
	plat := newPorkbunStub(map[string]*sdk.ExecuteResult{
		"create_dns_record": {Success: true, Status: 200, Data: json.RawMessage(`{"status":"SUCCESS"}`)},
	})
	ctx := newTestCtx(t, plat)
	app := &App{}

	// Set an MX record on subdomain "inbound" — won't match the
	// existing apex MX, so we expect a create call with prio split out.
	if _, err := app.toolDomainRecordsSet(ctx, map[string]any{
		"domain": "acme.com",
		"name":   "inbound",
		"type":   "MX",
		"value":  "10 inbound-smtp.eu-west-1.amazonaws.com",
	}); err != nil {
		t.Fatal(err)
	}
	var createCall *executeCall
	for i := range plat.calls {
		if plat.calls[i].Tool == "create_dns_record" {
			createCall = &plat.calls[i]
			break
		}
	}
	if createCall == nil {
		t.Fatal("create_dns_record was not called")
	}
	if createCall.Input["prio"] != "10" {
		t.Errorf("prio not split: %v", createCall.Input["prio"])
	}
	if createCall.Input["content"] != "inbound-smtp.eu-west-1.amazonaws.com" {
		t.Errorf("content not split: %v", createCall.Input["content"])
	}
}

func TestDomainRecordsDelete_CallsByType(t *testing.T) {
	plat := newPorkbunStub(map[string]*sdk.ExecuteResult{
		"delete_dns_records_by_type": {Success: true, Status: 200, Data: json.RawMessage(`{"status":"SUCCESS"}`)},
	})
	ctx := newTestCtx(t, plat)
	app := &App{}

	out, err := app.toolDomainRecordsDelete(ctx, map[string]any{
		"domain": "acme.com",
		"name":   "old",
		"type":   "TXT",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !out.(map[string]any)["deleted"].(bool) {
		t.Errorf("deleted=false: %+v", out)
	}
	var dCall *executeCall
	for i := range plat.calls {
		if plat.calls[i].Tool == "delete_dns_records_by_type" {
			dCall = &plat.calls[i]
		}
	}
	if dCall == nil {
		t.Fatal("delete tool not called")
	}
	if dCall.Input["type"] != "TXT" || dCall.Input["subdomain"] != "old" {
		t.Errorf("delete payload: %+v", dCall.Input)
	}
}

// ─── No provider bound ────────────────────────────────────────────

func TestDomainRecordsList_NoProviderBound(t *testing.T) {
	plat := &stubPlatform{
		bindingsOverride: map[string]any{}, // empty — no dns_provider
	}
	ctx := newTestCtx(t, plat)
	app := &App{}
	_, err := app.toolDomainRecordsList(ctx, map[string]any{"domain": "acme.com"})
	if err == nil || err.Error() != "no dns_provider bound — install/select a Porkbun or Namecheap connection" {
		t.Errorf("expected unbound-provider error, got %v", err)
	}
}

// ─── Namecheap (XML) ──────────────────────────────────────────────

const namecheapHostsXML = `<?xml version="1.0" encoding="utf-8"?>
<ApiResponse Status="OK" xmlns="http://api.namecheap.com/xml.response">
  <CommandResponse>
    <DomainDNSGetHostsResult Domain="acme.com">
      <host HostId="1" Name="@" Type="A" Address="1.2.3.4" TTL="600" MXPref="0"/>
      <host HostId="2" Name="www" Type="CNAME" Address="acme.com." TTL="600" MXPref="0"/>
      <host HostId="3" Name="@" Type="MX" Address="mx.acme.com" TTL="600" MXPref="10"/>
    </DomainDNSGetHostsResult>
  </CommandResponse>
</ApiResponse>`

const namecheapErrorXML = `<?xml version="1.0" encoding="utf-8"?>
<ApiResponse Status="ERROR">
  <Errors>
    <Error Number="1010102">API Key is invalid or API access has not been enabled</Error>
  </Errors>
</ApiResponse>`

// jsonStringWrap marshals a string as a JSON string — what the
// platform runner does when the response Content-Type isn't JSON.
func jsonStringWrap(s string) json.RawMessage {
	b, _ := json.Marshal(s)
	return b
}

func newNamecheapStub(extra map[string]*sdk.ExecuteResult) *stubPlatform {
	rep := map[string]*sdk.ExecuteResult{
		"get_dns_hosts": {Success: true, Status: 200, Data: jsonStringWrap(namecheapHostsXML)},
	}
	for k, v := range extra {
		rep[k] = v
	}
	return &stubPlatform{
		replyByTool: rep,
		// Override the bound provider's slug to namecheap.
		bindingsOverride: map[string]any{"dns_provider": float64(2)},
	}
}

// stubPlatform's GetConnection returns AppSlug="porkbun" by default.
// Override per-test to namecheap when needed.
type namecheapAwarePlatform struct{ stubPlatform }

func (n *namecheapAwarePlatform) GetConnection(id int64) (*sdk.PlatformConnection, error) {
	return &sdk.PlatformConnection{ID: id, AppSlug: "namecheap", Status: "active"}, nil
}

func newCtxNamecheap(t *testing.T, plat *namecheapAwarePlatform) *sdk.AppCtx {
	t.Helper()
	ctx := tk.NewAppCtx(t, "apteva.yaml",
		tk.WithProjectID("test-proj"),
		tk.WithPlatform(plat),
	)
	globalCtx = ctx
	return ctx
}

func TestNamecheap_ListParsesXML(t *testing.T) {
	plat := &namecheapAwarePlatform{
		stubPlatform: stubPlatform{
			replyByTool: map[string]*sdk.ExecuteResult{
				"get_dns_hosts": {Success: true, Status: 200, Data: jsonStringWrap(namecheapHostsXML)},
			},
			bindingsOverride: map[string]any{"dns_provider": float64(2)},
		},
	}
	ctx := newCtxNamecheap(t, plat)
	app := &App{}
	out, err := app.toolDomainRecordsList(ctx, map[string]any{"domain": "acme.com"})
	if err != nil {
		t.Fatal(err)
	}
	rs := out.(map[string]any)["records"].([]DNSRecord)
	if len(rs) != 3 {
		t.Fatalf("expected 3 records, got %d: %+v", len(rs), rs)
	}
	if rs[0].Name != "@" || rs[0].Type != "A" || rs[0].Value != "1.2.3.4" {
		t.Errorf("row 0: %+v", rs[0])
	}
	// MX prio should be parsed from MXPref attr.
	for _, r := range rs {
		if r.Type == "MX" && r.Prio != 10 {
			t.Errorf("MX prio=%d, want 10", r.Prio)
		}
	}
}

func TestNamecheap_ListSurfacesAPIError(t *testing.T) {
	plat := &namecheapAwarePlatform{
		stubPlatform: stubPlatform{
			replyByTool: map[string]*sdk.ExecuteResult{
				"get_dns_hosts": {Success: true, Status: 200, Data: jsonStringWrap(namecheapErrorXML)},
			},
			bindingsOverride: map[string]any{"dns_provider": float64(2)},
		},
	}
	ctx := newCtxNamecheap(t, plat)
	app := &App{}
	_, err := app.toolDomainRecordsList(ctx, map[string]any{"domain": "acme.com"})
	if err == nil {
		t.Fatal("expected namecheap error to surface")
	}
	if !strings.Contains(err.Error(), "1010102") {
		t.Errorf("expected error code in message, got %v", err)
	}
}

func TestNamecheap_UpsertWritesAllHostsBack(t *testing.T) {
	plat := &namecheapAwarePlatform{
		stubPlatform: stubPlatform{
			replyByTool: map[string]*sdk.ExecuteResult{
				"get_dns_hosts": {Success: true, Status: 200, Data: jsonStringWrap(namecheapHostsXML)},
				"set_dns_hosts": {Success: true, Status: 200, Data: jsonStringWrap(`<?xml version="1.0"?><ApiResponse Status="OK"><CommandResponse><DomainDNSSetHostsResult Domain="acme.com" IsSuccess="true"/></CommandResponse></ApiResponse>`)},
			},
			bindingsOverride: map[string]any{"dns_provider": float64(2)},
		},
	}
	ctx := newCtxNamecheap(t, plat)
	app := &App{}

	// Update the existing apex A record.
	out, err := app.toolDomainRecordsSet(ctx, map[string]any{
		"domain": "acme.com",
		"name":   "@",
		"type":   "A",
		"value":  "5.6.7.8",
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.(map[string]any)["action"] != "updated" {
		t.Errorf("expected updated, got %+v", out)
	}
	// set_dns_hosts must have been called with all 3 host slots populated.
	var setCall *executeCall
	for i := range plat.calls {
		if plat.calls[i].Tool == "set_dns_hosts" {
			setCall = &plat.calls[i]
		}
	}
	if setCall == nil {
		t.Fatal("set_dns_hosts not called")
	}
	if setCall.Input["SLD"] != "acme" || setCall.Input["TLD"] != "com" {
		t.Errorf("split SLD/TLD wrong: %+v", setCall.Input)
	}
	// Three hosts → HostName1, HostName2, HostName3 should all exist.
	for i := 1; i <= 3; i++ {
		key := fmt.Sprintf("HostName%d", i)
		if setCall.Input[key] == nil {
			t.Errorf("missing %s in setHosts payload", key)
		}
	}
	// Address1 must be the new value (the apex A we updated).
	if setCall.Input["Address1"] != "5.6.7.8" {
		t.Errorf("Address1=%v, want 5.6.7.8", setCall.Input["Address1"])
	}
}

func TestNamecheap_UpsertCreatesWhenAbsent(t *testing.T) {
	plat := &namecheapAwarePlatform{
		stubPlatform: stubPlatform{
			replyByTool: map[string]*sdk.ExecuteResult{
				"get_dns_hosts": {Success: true, Status: 200, Data: jsonStringWrap(namecheapHostsXML)},
				"set_dns_hosts": {Success: true, Status: 200, Data: jsonStringWrap(`<?xml version="1.0"?><ApiResponse Status="OK"/>`)},
			},
			bindingsOverride: map[string]any{"dns_provider": float64(2)},
		},
	}
	ctx := newCtxNamecheap(t, plat)
	app := &App{}

	// Add a new MX record on a subdomain — not present in fixture.
	out, err := app.toolDomainRecordsSet(ctx, map[string]any{
		"domain": "acme.com",
		"name":   "inbound",
		"type":   "MX",
		"value":  "10 inbound-smtp.eu-west-1.amazonaws.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.(map[string]any)["action"] != "created" {
		t.Errorf("expected created, got %+v", out)
	}
	// setHosts payload must contain HostName1..HostName4 (3 originals + 1 new).
	var setCall *executeCall
	for i := range plat.calls {
		if plat.calls[i].Tool == "set_dns_hosts" {
			setCall = &plat.calls[i]
		}
	}
	if setCall == nil {
		t.Fatal("set_dns_hosts not called")
	}
	if setCall.Input["HostName4"] != "inbound" {
		t.Errorf("expected new HostName4=inbound, got %v", setCall.Input["HostName4"])
	}
	if setCall.Input["MXPref4"] != "10" {
		t.Errorf("MXPref4=%v, want 10", setCall.Input["MXPref4"])
	}
	if setCall.Input["Address4"] != "inbound-smtp.eu-west-1.amazonaws.com" {
		t.Errorf("Address4=%v", setCall.Input["Address4"])
	}
}

func TestNamecheap_DeleteOmitsMatchingHost(t *testing.T) {
	plat := &namecheapAwarePlatform{
		stubPlatform: stubPlatform{
			replyByTool: map[string]*sdk.ExecuteResult{
				"get_dns_hosts": {Success: true, Status: 200, Data: jsonStringWrap(namecheapHostsXML)},
				"set_dns_hosts": {Success: true, Status: 200, Data: jsonStringWrap(`<?xml version="1.0"?><ApiResponse Status="OK"/>`)},
			},
			bindingsOverride: map[string]any{"dns_provider": float64(2)},
		},
	}
	ctx := newCtxNamecheap(t, plat)
	app := &App{}

	if _, err := app.toolDomainRecordsDelete(ctx, map[string]any{
		"domain": "acme.com",
		"name":   "www",
		"type":   "CNAME",
	}); err != nil {
		t.Fatal(err)
	}
	// Expect setHosts with only 2 hosts (apex A + apex MX); www CNAME omitted.
	var setCall *executeCall
	for i := range plat.calls {
		if plat.calls[i].Tool == "set_dns_hosts" {
			setCall = &plat.calls[i]
		}
	}
	if setCall == nil {
		t.Fatal("set_dns_hosts not called")
	}
	if setCall.Input["HostName3"] != nil {
		t.Errorf("expected only 2 hosts in payload, found HostName3=%v", setCall.Input["HostName3"])
	}
	// HostName1 + HostName2 should NOT be www CNAME.
	for _, k := range []string{"HostName1", "HostName2"} {
		if setCall.Input[k] == "www" {
			t.Errorf("www CNAME was kept (in %s); should have been deleted", k)
		}
	}
}

func TestSplitSLDTLD(t *testing.T) {
	cases := []struct {
		in, sld, tld string
	}{
		{"acme.com", "acme", "com"},
		{"sub.acme.com", "sub", "acme.com"}, // not perfect for multi-label TLDs; v0.1 docs this
		{"x", "", ""},
		{"", "", ""},
	}
	for _, c := range cases {
		s, t2 := splitSLDTLD(c.in)
		if s != c.sld || t2 != c.tld {
			t.Errorf("splitSLDTLD(%q) = (%q, %q), want (%q, %q)", c.in, s, t2, c.sld, c.tld)
		}
	}
}
