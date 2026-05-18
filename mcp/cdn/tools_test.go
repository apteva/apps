package main

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

// ─── recordingPlatform ─────────────────────────────────────────────
//
// Stub PlatformClient that records every CallApp / CallAppResult so
// tests can assert which sibling-app tools cdn invoked and with what
// args (especially the mandatory _project_id injection on global-
// scoped calls). Embeds tk.BasePlatformClient — only override what
// we exercise.

type recordingPlatform struct {
	tk.BasePlatformClient
	mu               sync.Mutex
	callAppCalls     []callAppCall
	callAppResponses map[string]json.RawMessage // key: "<app>:<tool>"
	identity         *sdk.InstallIdentity
}

type callAppCall struct {
	AppName string
	Tool    string
	Input   map[string]any
}

func newRecordingPlatform() *recordingPlatform {
	return &recordingPlatform{
		identity: &sdk.InstallIdentity{
			AppName:   "cdn",
			InstallID: 42,
			ProjectID: "test-proj",
		},
		callAppResponses: map[string]json.RawMessage{},
	}
}

func (p *recordingPlatform) WhoAmI() (*sdk.InstallIdentity, error) { return p.identity, nil }

func (p *recordingPlatform) CallApp(appName, tool string, input map[string]any) (json.RawMessage, error) {
	p.mu.Lock()
	p.callAppCalls = append(p.callAppCalls, callAppCall{AppName: appName, Tool: tool, Input: input})
	p.mu.Unlock()
	if r, ok := p.callAppResponses[appName+":"+tool]; ok {
		return r, nil
	}
	// Default — a successful, empty-result MCP envelope.
	return json.RawMessage(`{"result":{"content":[{"type":"text","text":"{}"}]}}`), nil
}

// Envelope-stripping CallAppResult — copied from the canonical
// social/main_test.go pattern (per project memory: stubs that feed
// wrapped JSON-RPC bytes must envelope-strip before decoding).
func (p *recordingPlatform) CallAppResult(appName, tool string, input map[string]any, out any) error {
	raw, err := p.CallApp(appName, tool, input)
	if err != nil {
		return err
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	var env struct {
		Result *struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if json.Unmarshal(raw, &env) == nil && env.Result != nil && len(env.Result.Content) > 0 {
		return json.Unmarshal([]byte(env.Result.Content[0].Text), out)
	}
	return json.Unmarshal(raw, out)
}

func (p *recordingPlatform) callsTo(app, tool string) []callAppCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := []callAppCall{}
	for _, c := range p.callAppCalls {
		if c.AppName == app && c.Tool == tool {
			out = append(out, c)
		}
	}
	return out
}

// ─── ctx helper ────────────────────────────────────────────────────

// newCdnCtx returns an AppCtx with the given PlatformClient and
// install config populated. APTEVA_INSTALL_ID is set so orchestrate.go
// can register routes with an owner. Accepts any sdk.PlatformClient
// so tests can pass either a vanilla recorder or a wrapper that
// intercepts CallApp (see platformWithMissingApps).
func newCdnCtx(t *testing.T, p sdk.PlatformClient, cfg map[string]string) *sdk.AppCtx {
	t.Helper()
	t.Setenv("APTEVA_INSTALL_ID", "42")
	opts := []tk.Option{
		tk.WithProjectID("test-proj"),
		tk.WithPlatform(p),
	}
	if len(cfg) > 0 {
		opts = append(opts, tk.WithConfig(cfg))
	}
	ctx := tk.NewAppCtx(t, "apteva.yaml", opts...)
	globalCtx = ctx
	return ctx
}

// ─── cdn_url_for ──────────────────────────────────────────────────

func TestURLFor_Mints(t *testing.T) {
	p := newRecordingPlatform()
	ctx := newCdnCtx(t, p, map[string]string{"server_public_host": "1.2.3.4"})
	app := &App{}

	// Seed a zone directly via the DB so we can exercise cdn_url_for
	// without dragging the full create flow's cross-app calls into a
	// test of pure URL minting.
	id, err := dbInsertZone(ctx.AppDB(), &Zone{
		ProjectID: "test-proj", Hostname: "files.acme.com",
		OriginURL: "http://127.0.0.1:8080", RecordType: "A",
		RecordValue: "1.2.3.4", Status: "active",
	})
	if err != nil {
		t.Fatalf("seed zone: %v", err)
	}

	out, err := app.toolURLFor(ctx, map[string]any{
		"zone_id":     id,
		"origin_path": "/files/42/content/receipt.pdf",
	})
	if err != nil {
		t.Fatalf("toolURLFor: %v", err)
	}
	got := out.(map[string]any)["url"].(string)
	want := "https://files.acme.com/files/42/content/receipt.pdf"
	if got != want {
		t.Errorf("url = %q, want %q", got, want)
	}
}

func TestURLFor_RejectsBadInput(t *testing.T) {
	ctx := newCdnCtx(t, newRecordingPlatform(), nil)
	app := &App{}

	cases := []struct {
		name string
		args map[string]any
	}{
		{"missing zone_id", map[string]any{"origin_path": "/x"}},
		{"missing origin_path", map[string]any{"zone_id": 1}},
		{"origin_path without leading slash", map[string]any{"zone_id": 1, "origin_path": "x"}},
		{"zone not found", map[string]any{"zone_id": 9999, "origin_path": "/x"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := app.toolURLFor(ctx, c.args); err == nil {
				t.Errorf("expected error, got nil")
			}
		})
	}
}

// ─── cdn_zone_create ──────────────────────────────────────────────

func TestZoneCreate_HappyPath_InjectsProjectIDOnAllLegs(t *testing.T) {
	p := newRecordingPlatform()
	ctx := newCdnCtx(t, p, map[string]string{
		"server_public_host":  "1.2.3.4",
		"record_type_default": "A",
	})
	app := &App{}

	out, err := app.toolZoneCreate(ctx, map[string]any{
		"hostname":   "files.acme.com",
		"origin_url": "http://127.0.0.1:8080",
	})
	if err != nil {
		t.Fatalf("toolZoneCreate: %v", err)
	}
	res := out.(map[string]any)
	if res["created"] != true {
		t.Errorf("created=%v, want true", res["created"])
	}
	z := res["zone"].(*Zone)
	if z.Status != "active" {
		t.Errorf("zone status=%q, want active (details: %s)", z.Status, z.StatusDetail)
	}
	if z.DNSStatus != "ok" || z.CertStatus != "ok" || z.RouteStatus != "ok" {
		t.Errorf("per-leg statuses dns=%q cert=%q route=%q — all should be ok", z.DNSStatus, z.CertStatus, z.RouteStatus)
	}

	// All three sibling-app calls fired exactly once.
	for _, sig := range []struct{ app, tool string }{
		{"domains", "domain_records_set"},
		{"certs", "cert_issue"},
		{"routes", "routes_register"},
	} {
		calls := p.callsTo(sig.app, sig.tool)
		if len(calls) != 1 {
			t.Errorf("%s.%s called %d times, want 1", sig.app, sig.tool, len(calls))
			continue
		}
		// _project_id MUST be injected on every global-scope call
		// (project memory: feedback_project_id_global_calls).
		if calls[0].Input["_project_id"] != "test-proj" {
			t.Errorf("%s.%s _project_id = %v, want \"test-proj\"", sig.app, sig.tool, calls[0].Input["_project_id"])
		}
	}

	// routes_register specifically needs owner_install_id from
	// APTEVA_INSTALL_ID (42, set in newCdnCtx).
	routesCall := p.callsTo("routes", "routes_register")[0]
	if routesCall.Input["owner_install_id"].(int64) != 42 {
		t.Errorf("routes_register owner_install_id = %v, want 42", routesCall.Input["owner_install_id"])
	}
	if routesCall.Input["target"] != "http://127.0.0.1:8080" {
		t.Errorf("routes_register target = %v, want http://127.0.0.1:8080", routesCall.Input["target"])
	}

	// domains.domain_records_set splits the hostname into apex+sub.
	dnsCall := p.callsTo("domains", "domain_records_set")[0]
	if dnsCall.Input["domain"] != "acme.com" {
		t.Errorf("domain = %v, want acme.com", dnsCall.Input["domain"])
	}
	if dnsCall.Input["name"] != "files" {
		t.Errorf("name = %v, want files", dnsCall.Input["name"])
	}
	if dnsCall.Input["type"] != "A" {
		t.Errorf("type = %v, want A", dnsCall.Input["type"])
	}
	if dnsCall.Input["value"] != "1.2.3.4" {
		t.Errorf("value = %v, want 1.2.3.4", dnsCall.Input["value"])
	}
}

func TestZoneCreate_RequiresServerPublicHost(t *testing.T) {
	p := newRecordingPlatform()
	ctx := newCdnCtx(t, p, nil) // no server_public_host
	app := &App{}

	_, err := app.toolZoneCreate(ctx, map[string]any{
		"hostname":   "files.acme.com",
		"origin_url": "http://127.0.0.1:8080",
	})
	if err == nil {
		t.Fatal("expected error when server_public_host is unset")
	}
	if !strings.Contains(err.Error(), "server_public_host") {
		t.Errorf("error message should mention server_public_host, got: %v", err)
	}
}

func TestZoneCreate_Idempotent(t *testing.T) {
	p := newRecordingPlatform()
	ctx := newCdnCtx(t, p, map[string]string{"server_public_host": "1.2.3.4"})
	app := &App{}
	args := map[string]any{
		"hostname":   "files.acme.com",
		"origin_url": "http://127.0.0.1:8080",
	}
	if _, err := app.toolZoneCreate(ctx, args); err != nil {
		t.Fatalf("first create: %v", err)
	}
	out, err := app.toolZoneCreate(ctx, args)
	if err != nil {
		t.Fatalf("second create: %v", err)
	}
	if out.(map[string]any)["created"] != false {
		t.Error("second create should report created=false (idempotent no-op)")
	}
	// Cross-app calls only fire on the first create.
	if got := len(p.callsTo("routes", "routes_register")); got != 1 {
		t.Errorf("routes_register called %d times across two identical creates, want 1", got)
	}
}

func TestZoneCreate_RejectsApexCNAME(t *testing.T) {
	p := newRecordingPlatform()
	ctx := newCdnCtx(t, p, map[string]string{
		"server_public_host":  "cname.target.example",
		"record_type_default": "CNAME",
	})
	app := &App{}
	out, err := app.toolZoneCreate(ctx, map[string]any{
		"hostname":   "acme.com", // apex
		"origin_url": "http://127.0.0.1:8080",
	})
	if err != nil {
		// We choose error-on-status (zone row created with dns_status=error)
		// rather than a hard tool error, so the operator can see what failed
		// in the panel rather than getting a black-box rejection.
		t.Fatalf("expected zone row even on apex-CNAME failure: %v", err)
	}
	z := out.(map[string]any)["zone"].(*Zone)
	if z.DNSStatus != "error" {
		t.Errorf("DNSStatus=%q, want error (apex CNAME isn't allowed)", z.DNSStatus)
	}
	if !strings.Contains(z.StatusDetail, "apex CNAME") {
		t.Errorf("status_detail should mention apex CNAME; got: %q", z.StatusDetail)
	}
}

// ─── local-dev path (skip_dns + allow_http) ───────────────────────

// platformWithMissingApps fakes the "app not installed" error
// shape that the platform's callback layer returns when a CallApp
// targets an app that isn't running. Embeds recordingPlatform so
// we still record calls (verifying cdn at least tried each leg).
type platformWithMissingApps struct {
	*recordingPlatform
	missing map[string]bool // app names that should error as "not installed"
}

func (p *platformWithMissingApps) CallApp(appName, tool string, input map[string]any) (json.RawMessage, error) {
	if p.missing[appName] {
		// Record the attempt then return the platform's not-installed error.
		_, _ = p.recordingPlatform.CallApp(appName, tool, input)
		return nil, errors.New("app not running: " + appName)
	}
	return p.recordingPlatform.CallApp(appName, tool, input)
}

// CallAppResult mirrors CallApp's error path so both invocation styles
// degrade the same way. recordingPlatform.CallAppResult would otherwise
// re-enter its own (un-faked) CallApp.
func (p *platformWithMissingApps) CallAppResult(appName, tool string, input map[string]any, out any) error {
	if p.missing[appName] {
		_, _ = p.recordingPlatform.CallApp(appName, tool, input)
		return errors.New("app not running: " + appName)
	}
	return p.recordingPlatform.CallAppResult(appName, tool, input, out)
}

func TestZoneCreate_AllowHTTP_SkipsCert(t *testing.T) {
	p := newRecordingPlatform()
	ctx := newCdnCtx(t, p, map[string]string{"server_public_host": "1.2.3.4"})
	app := &App{}
	out, err := app.toolZoneCreate(ctx, map[string]any{
		"hostname":   "files.acme.com",
		"origin_url": "http://127.0.0.1:8080",
		"allow_http": true,
	})
	if err != nil {
		t.Fatalf("toolZoneCreate: %v", err)
	}
	z := out.(map[string]any)["zone"].(*Zone)
	if z.CertStatus != "skipped" {
		t.Errorf("cert_status=%q, want skipped (allow_http:true should bypass cert)", z.CertStatus)
	}
	if !z.AllowHTTP {
		t.Error("allow_http not persisted on zone")
	}
	if z.Status != "active" {
		t.Errorf("status=%q, want active", z.Status)
	}
	// certs app should not have been called at all.
	if got := len(p.callsTo("certs", "cert_issue")); got != 0 {
		t.Errorf("certs.cert_issue called %d times, want 0", got)
	}
	// routes_register must propagate allow_http=true.
	routeCalls := p.callsTo("routes", "routes_register")
	if len(routeCalls) != 1 {
		t.Fatalf("routes_register called %d times, want 1", len(routeCalls))
	}
	if routeCalls[0].Input["allow_http"] != true {
		t.Errorf("routes_register allow_http=%v, want true", routeCalls[0].Input["allow_http"])
	}
}

func TestZoneCreate_SkipDNS_SkipsDomainsCall(t *testing.T) {
	p := newRecordingPlatform()
	ctx := newCdnCtx(t, p, nil) // server_public_host deliberately unset
	app := &App{}
	out, err := app.toolZoneCreate(ctx, map[string]any{
		"hostname":   "files.local.test",
		"origin_url": "http://127.0.0.1:8080",
		"skip_dns":   true,
	})
	if err != nil {
		t.Fatalf("toolZoneCreate: %v", err)
	}
	z := out.(map[string]any)["zone"].(*Zone)
	if z.DNSStatus != "skipped" {
		t.Errorf("dns_status=%q, want skipped", z.DNSStatus)
	}
	if got := len(p.callsTo("domains", "domain_records_set")); got != 0 {
		t.Errorf("domains.domain_records_set called %d times, want 0", got)
	}
	if z.Status != "active" {
		t.Errorf("status=%q, want active (route leg succeeded)", z.Status)
	}
}

func TestZoneCreate_LocalDev_DomainsAndCertsMissing(t *testing.T) {
	// Simulate "only routes + cdn installed" — domains and certs
	// calls return the platform's not-installed error.
	p := &platformWithMissingApps{
		recordingPlatform: newRecordingPlatform(),
		missing:           map[string]bool{"domains": true, "certs": true},
	}
	ctx := newCdnCtx(t, p, map[string]string{"server_public_host": "127.0.0.1"})
	app := &App{}

	out, err := app.toolZoneCreate(ctx, map[string]any{
		"hostname":   "files.local.test",
		"origin_url": "http://127.0.0.1:8080",
	})
	if err != nil {
		t.Fatalf("toolZoneCreate: %v", err)
	}
	z := out.(map[string]any)["zone"].(*Zone)
	// Both optional legs degrade to skipped (not error) when the
	// app's missing — local dev should never see them as errors.
	if z.DNSStatus != "skipped" {
		t.Errorf("dns_status=%q, want skipped (domains app not installed)", z.DNSStatus)
	}
	if z.CertStatus != "skipped" {
		t.Errorf("cert_status=%q, want skipped (certs app not installed)", z.CertStatus)
	}
	if z.RouteStatus != "ok" {
		t.Errorf("route_status=%q, want ok", z.RouteStatus)
	}
	if z.Status != "active" {
		t.Errorf("status=%q, want active (route leg landed)", z.Status)
	}
	if z.StatusDetail != "" {
		t.Errorf("status_detail=%q, want empty (skipped legs aren't errors)", z.StatusDetail)
	}
}

func TestURLFor_AllowHTTP_BuildsHTTPURL(t *testing.T) {
	p := newRecordingPlatform()
	ctx := newCdnCtx(t, p, nil)
	app := &App{}
	id, err := dbInsertZone(ctx.AppDB(), &Zone{
		ProjectID: "test-proj", Hostname: "files.local.test",
		OriginURL: "http://127.0.0.1:8080", RecordType: "A",
		AllowHTTP: true, Status: "active",
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	out, err := app.toolURLFor(ctx, map[string]any{
		"zone_id":     id,
		"origin_path": "/files/1/content",
	})
	if err != nil {
		t.Fatalf("toolURLFor: %v", err)
	}
	want := "http://files.local.test/files/1/content"
	if got := out.(map[string]any)["url"].(string); got != want {
		t.Errorf("url=%q, want %q (allow_http zone should mint http://)", got, want)
	}
}


// ─── cdn_zone_list / get / delete ─────────────────────────────────

func TestZoneList_Empty(t *testing.T) {
	p := newRecordingPlatform()
	ctx := newCdnCtx(t, p, nil)
	app := &App{}
	out, err := app.toolZoneList(ctx, nil)
	if err != nil {
		t.Fatalf("toolZoneList: %v", err)
	}
	res := out.(map[string]any)
	if res["count"].(int) != 0 {
		t.Errorf("count=%v, want 0", res["count"])
	}
}

func TestZoneDelete_Idempotent(t *testing.T) {
	p := newRecordingPlatform()
	ctx := newCdnCtx(t, p, map[string]string{"server_public_host": "1.2.3.4"})
	app := &App{}
	if _, err := app.toolZoneCreate(ctx, map[string]any{
		"hostname":   "files.acme.com",
		"origin_url": "http://127.0.0.1:8080",
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	// First delete succeeds.
	out, err := app.toolZoneDelete(ctx, map[string]any{"hostname": "files.acme.com"})
	if err != nil {
		t.Fatalf("first delete: %v", err)
	}
	if out.(map[string]any)["deleted"] != true {
		t.Error("first delete should return deleted=true")
	}
	// Second delete errors because the zone is gone (lookup fails).
	// That's fine — operators don't re-run deletes; idempotence here
	// is about cross-app legs being best-effort, not the local row.
	if _, err := app.toolZoneDelete(ctx, map[string]any{"hostname": "files.acme.com"}); err == nil {
		t.Error("second delete of a missing zone should error (zone not found)")
	}
}
