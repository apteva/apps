package main

// Tests for v0.3 platform-proxied named-tunnel flow.
//
// Strategy: implement a fake sdk.PlatformClient that routes
// ExecuteIntegrationTool calls by tool name, returning canned JSON
// envelopes shaped like Cloudflare's API. Bindings are pre-set so
// ctx.IntegrationFor("cloudflare") returns a non-nil binding pointing
// at a synthetic connection id.

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

// ─── fake PlatformClient ──────────────────────────────────────────

const fakeCFConnID int64 = 4242

// fakePlatform implements sdk.PlatformClient with a registry of
// (tool → handler) entries the test wires up.
type fakePlatform struct {
	tk.BasePlatformClient
	bindings map[string]any
	tools    map[string]func(input map[string]any) *sdk.ExecuteResult
	calls    []fakeCall
}

type fakeCall struct {
	Tool  string
	Input map[string]any
}

func newFakePlatform() *fakePlatform {
	return &fakePlatform{
		bindings: map[string]any{"cloudflare": float64(fakeCFConnID)}, // WhoAmI bindings come from JSON; ints arrive as float64
		tools:    map[string]func(map[string]any) *sdk.ExecuteResult{},
	}
}

func (p *fakePlatform) on(tool string, h func(input map[string]any) *sdk.ExecuteResult) {
	p.tools[tool] = h
}

// PlatformClient interface — only ExecuteIntegrationTool, WhoAmI,
// GetConnection are used by live-link's code path; the rest return
// zeros so the fake satisfies the interface.

func (p *fakePlatform) WhoAmI() (*sdk.InstallIdentity, error) {
	return &sdk.InstallIdentity{
		AppName: "live-link", InstallID: 1, ProjectID: "test",
		Bindings: p.bindings,
	}, nil
}
func (p *fakePlatform) GetConnection(id int64) (*sdk.PlatformConnection, error) {
	return &sdk.PlatformConnection{ID: id, AppSlug: "cloudflare", Name: "test", Status: "active"}, nil
}
func (p *fakePlatform) ExecuteIntegrationTool(connID int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
	p.calls = append(p.calls, fakeCall{Tool: tool, Input: input})
	if connID != fakeCFConnID {
		return nil, errors.New("fake: wrong connection id")
	}
	h, ok := p.tools[tool]
	if !ok {
		return nil, errors.New("fake: no handler for tool " + tool)
	}
	return h(input), nil
}

// Stubs for the rest of the interface — return zero values.
func (p *fakePlatform) ListConnections(_ sdk.ConnectionFilter) ([]sdk.PlatformConnection, error) {
	return nil, nil
}
func (p *fakePlatform) GetInstance(_ int64) (*sdk.PlatformInstance, error)         { return nil, nil }
func (p *fakePlatform) SendEvent(_ int64, _ string) error                          { return nil }
func (p *fakePlatform) SendToChannel(_, _, _ string) error                         { return nil }
func (p *fakePlatform) CallApp(_, _ string, _ map[string]any) (json.RawMessage, error) {
	return nil, nil
}
func (p *fakePlatform) CallAppResult(_, _ string, _ map[string]any, _ any) error { return nil }
func (p *fakePlatform) StartOAuth(_ sdk.OAuthStartRequest) (*sdk.OAuthStartResult, error) {
	return nil, nil
}
func (p *fakePlatform) DisconnectConnection(_ int64) error                  { return nil }
func (p *fakePlatform) ListOwnedConnections() ([]sdk.PlatformConnection, error) { return nil, nil }
func (p *fakePlatform) GetGrants(_ int64) (*sdk.GrantsResponse, error)      { return nil, nil }

// ─── helpers ──────────────────────────────────────────────────────

// jsonResult wraps any value into Cloudflare's standard {"result": …}
// envelope, then into ExecuteResult so the platform layer parses it
// back the same way it would in production.
func jsonResult(v any) *sdk.ExecuteResult {
	body, _ := json.Marshal(map[string]any{"result": v})
	return &sdk.ExecuteResult{Success: true, Status: 200, Data: body}
}

// newTestCtxWithCF builds a context wired to a fakePlatform, with
// the cloudflare role pre-bound.
func newTestCtxWithCF(t *testing.T) (*sdk.AppCtx, *fakePlatform) {
	t.Helper()
	db := openTestDB(t)
	m := (&App{}).Manifest()
	plat := newFakePlatform()
	ctx := sdk.NewAppCtxForTest(&m, db, sdk.Config{}, plat, &silentLogger{})
	globalCtx = ctx
	return ctx, plat
}

// ─── Tests ────────────────────────────────────────────────────────

func TestCFConnectionID_RequiresBoundIntegration(t *testing.T) {
	// No platform → no bindings → no binding for "cloudflare" → error.
	db := openTestDB(t)
	m := (&App{}).Manifest()
	ctx := sdk.NewAppCtxForTest(&m, db, sdk.Config{}, nil, &silentLogger{})
	globalCtx = ctx
	if _, err := (&App{}).cfConnectionID(ctx); err == nil {
		t.Fatal("expected error when integration unbound")
	}
}

func TestCFConnectionID_ReturnsBoundID(t *testing.T) {
	ctx, _ := newTestCtxWithCF(t)
	id, err := (&App{}).cfConnectionID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if id != fakeCFConnID {
		t.Errorf("got %d, want %d", id, fakeCFConnID)
	}
}

func TestEnsureNamedTunnel_CreateFlow(t *testing.T) {
	ctx, plat := newTestCtxWithCF(t)
	plat.on("list_tunnels", func(_ map[string]any) *sdk.ExecuteResult {
		return jsonResult([]any{}) // no existing tunnel
	})
	plat.on("create_tunnel", func(input map[string]any) *sdk.ExecuteResult {
		// Verify the app asked to API-manage ingress, not local.
		if input["config_src"] != "cloudflare" {
			t.Errorf("create_tunnel config_src=%v, want cloudflare (so update_tunnel_configuration works)", input["config_src"])
		}
		return jsonResult(map[string]any{"id": "TUN", "token": "TOK", "name": input["name"]})
	})
	plat.on("update_tunnel_configuration", func(input map[string]any) *sdk.ExecuteResult {
		// Catch-all rule must be present, otherwise CF rejects the config.
		cfg, _ := input["config"].(map[string]any)
		ingress, _ := cfg["ingress"].([]map[string]any)
		if len(ingress) != 2 {
			t.Errorf("ingress len=%d, want 2 (rule + catch-all)", len(ingress))
		}
		if ingress[1]["service"] != "http_status:404" {
			t.Errorf("catch-all=%v, want http_status:404", ingress[1]["service"])
		}
		return jsonResult(map[string]any{})
	})
	plat.on("list_dns_records", func(_ map[string]any) *sdk.ExecuteResult {
		return jsonResult([]any{}) // no existing CNAME
	})
	plat.on("create_dns_record", func(input map[string]any) *sdk.ExecuteResult {
		if input["proxied"] != true {
			t.Errorf("proxied=%v — must be true or the tunnel doesn't get traffic", input["proxied"])
		}
		if input["content"] != "TUN.cfargotunnel.com" {
			t.Errorf("CNAME content=%v, want TUN.cfargotunnel.com", input["content"])
		}
		return jsonResult(map[string]any{"id": "REC", "name": "h.example.com", "type": "CNAME"})
	})

	nt, err := (&App{}).ensureNamedTunnel(ctx, "h.example.com", "ZONE")
	if err != nil {
		t.Fatal(err)
	}
	if nt.TunnelID != "TUN" || nt.TunnelToken != "TOK" || nt.DNSRecordID != "REC" {
		t.Errorf("nt=%+v", nt)
	}

	// Persisted to DB.
	got, _ := dbFirstNamedTunnel(ctx.AppDB())
	if got == nil || got.Hostname != "h.example.com" {
		t.Errorf("persisted row=%+v", got)
	}
}

func TestEnsureNamedTunnel_AdoptsExistingTunnel(t *testing.T) {
	// list_tunnels returns one match → app should NOT call create_tunnel
	// and SHOULD call get_tunnel_token to fetch the connector token.
	ctx, plat := newTestCtxWithCF(t)
	plat.on("list_tunnels", func(_ map[string]any) *sdk.ExecuteResult {
		return jsonResult([]any{
			map[string]any{"id": "EXISTING", "name": "apteva-live-link-h-example-com"},
		})
	})
	plat.on("create_tunnel", func(_ map[string]any) *sdk.ExecuteResult {
		t.Fatal("create_tunnel should not be called when a tunnel with this name exists")
		return nil
	})
	plat.on("get_tunnel_token", func(input map[string]any) *sdk.ExecuteResult {
		if input["tunnel_id"] != "EXISTING" {
			t.Errorf("tunnel_id=%v, want EXISTING", input["tunnel_id"])
		}
		body, _ := json.Marshal(map[string]any{"result": "ADOPTED-TOK"})
		return &sdk.ExecuteResult{Success: true, Status: 200, Data: body}
	})
	plat.on("update_tunnel_configuration", func(_ map[string]any) *sdk.ExecuteResult {
		return jsonResult(map[string]any{})
	})
	plat.on("list_dns_records", func(_ map[string]any) *sdk.ExecuteResult { return jsonResult([]any{}) })
	plat.on("create_dns_record", func(_ map[string]any) *sdk.ExecuteResult {
		return jsonResult(map[string]any{"id": "REC"})
	})

	nt, err := (&App{}).ensureNamedTunnel(ctx, "h.example.com", "Z")
	if err != nil {
		t.Fatal(err)
	}
	if nt.TunnelToken != "ADOPTED-TOK" {
		t.Errorf("token=%q, want ADOPTED-TOK (from get_tunnel_token)", nt.TunnelToken)
	}
}

func TestEnsureNamedTunnel_IsIdempotentOnSecondCall(t *testing.T) {
	ctx, plat := newTestCtxWithCF(t)
	createCalls := 0
	plat.on("list_tunnels", func(_ map[string]any) *sdk.ExecuteResult { return jsonResult([]any{}) })
	plat.on("create_tunnel", func(input map[string]any) *sdk.ExecuteResult {
		createCalls++
		return jsonResult(map[string]any{"id": "TUN", "token": "TOK", "name": input["name"]})
	})
	plat.on("update_tunnel_configuration", func(_ map[string]any) *sdk.ExecuteResult { return jsonResult(map[string]any{}) })
	plat.on("list_dns_records", func(_ map[string]any) *sdk.ExecuteResult { return jsonResult([]any{}) })
	plat.on("create_dns_record", func(_ map[string]any) *sdk.ExecuteResult {
		return jsonResult(map[string]any{"id": "REC"})
	})

	if _, err := (&App{}).ensureNamedTunnel(ctx, "h.example.com", "Z"); err != nil {
		t.Fatal(err)
	}
	// Second call hits the local DB cache — no CF traffic at all.
	if _, err := (&App{}).ensureNamedTunnel(ctx, "h.example.com", "Z"); err != nil {
		t.Fatal(err)
	}
	if createCalls != 1 {
		t.Errorf("create_tunnel called %d times; want exactly 1", createCalls)
	}
}

func TestDestroyNamedTunnel_HitsCFAndDropsRow(t *testing.T) {
	ctx, plat := newTestCtxWithCF(t)
	// Seed: pretend we ensured a tunnel earlier. Use the row directly
	// rather than running the full ensure flow.
	if err := dbInsertNamedTunnel(ctx.AppDB(), &NamedTunnel{
		Hostname: "h.example.com", TunnelID: "TUN", TunnelToken: "TOK",
		ZoneID: "Z", DNSRecordID: "REC",
	}); err != nil {
		t.Fatal(err)
	}

	dnsDeleted := false
	tunDeleted := false
	plat.on("delete_dns_record", func(input map[string]any) *sdk.ExecuteResult {
		dnsDeleted = true
		if input["zone_id"] != "Z" || input["record_id"] != "REC" {
			t.Errorf("delete_dns args=%v", input)
		}
		return jsonResult(map[string]any{})
	})
	plat.on("delete_tunnel", func(input map[string]any) *sdk.ExecuteResult {
		tunDeleted = true
		if input["tunnel_id"] != "TUN" {
			t.Errorf("delete_tunnel args=%v", input)
		}
		return jsonResult(map[string]any{})
	})

	destroyed, err := (&App{}).destroyNamedTunnel(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !destroyed || !dnsDeleted || !tunDeleted {
		t.Errorf("destroyed=%v dns=%v tun=%v", destroyed, dnsDeleted, tunDeleted)
	}
	if got, _ := dbFirstNamedTunnel(ctx.AppDB()); got != nil {
		t.Errorf("row should be gone, got %+v", got)
	}
}

// Mode is derived from DB state (presence of named_tunnels row), not
// from a config knob. Empty DB → quick; row → named.
func TestCurrentMode_TracksDBState(t *testing.T) {
	ctx, _ := newTestCtxWithCF(t)
	app := &App{}
	if got := app.currentMode(ctx); got != ModeQuick {
		t.Errorf("empty DB: got %q, want quick", got)
	}
	if err := dbInsertNamedTunnel(ctx.AppDB(), &NamedTunnel{
		Hostname: "h.example.com", TunnelID: "T", TunnelToken: "K",
		ZoneID: "Z", DNSRecordID: "R",
	}); err != nil {
		t.Fatal(err)
	}
	if got := app.currentMode(ctx); got != ModeNamed {
		t.Errorf("with row: got %q, want named", got)
	}
	if err := dbDeleteNamedTunnel(ctx.AppDB(), "h.example.com"); err != nil {
		t.Fatal(err)
	}
	if got := app.currentMode(ctx); got != ModeQuick {
		t.Errorf("after delete: got %q, want quick", got)
	}
}

// ─── HTTP handlers ─────────────────────────────────────────────────

func TestHandleNamedZones_ProxiesListZones(t *testing.T) {
	ctx, plat := newTestCtxWithCF(t)
	plat.on("list_zones", func(_ map[string]any) *sdk.ExecuteResult {
		return jsonResult([]any{
			map[string]any{"id": "Z1", "name": "example.com"},
			map[string]any{"id": "Z2", "name": "another.com"},
		})
	})
	app := &App{mgr: NewManager(nil, nil)}
	_ = ctx
	srv := httptest.NewServer(http.HandlerFunc(app.handleNamedZones))
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	var body struct {
		Zones []struct{ ID, Name string }
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if len(body.Zones) != 2 || body.Zones[0].Name != "example.com" {
		t.Errorf("zones=%+v", body.Zones)
	}
}

func TestHandleNamedConfigure_PersistsAndPipesArgs(t *testing.T) {
	ctx, plat := newTestCtxWithCF(t)
	plat.on("list_tunnels", func(_ map[string]any) *sdk.ExecuteResult { return jsonResult([]any{}) })
	plat.on("create_tunnel", func(_ map[string]any) *sdk.ExecuteResult {
		return jsonResult(map[string]any{"id": "TUN", "token": "TOK"})
	})
	plat.on("update_tunnel_configuration", func(_ map[string]any) *sdk.ExecuteResult { return jsonResult(map[string]any{}) })
	plat.on("list_dns_records", func(_ map[string]any) *sdk.ExecuteResult { return jsonResult([]any{}) })
	plat.on("create_dns_record", func(_ map[string]any) *sdk.ExecuteResult {
		return jsonResult(map[string]any{"id": "REC"})
	})

	app := &App{mgr: NewManager(nil, nil)}
	srv := httptest.NewServer(http.HandlerFunc(app.handleNamedConfigure))
	t.Cleanup(srv.Close)

	resp, err := http.Post(srv.URL, "application/json",
		strings.NewReader(`{"zone_id":"Z","hostname":"h.example.com"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d", resp.StatusCode)
	}

	got, _ := dbFirstNamedTunnel(ctx.AppDB())
	if got == nil || got.Hostname != "h.example.com" || got.ZoneID != "Z" {
		t.Errorf("persisted=%+v", got)
	}
}
