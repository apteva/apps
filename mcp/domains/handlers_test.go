package main

import (
	"encoding/json"
	"sync"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

// ─── Stub PlatformClient ──────────────────────────────────────────

type stubPlatform struct {
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
