package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type communityDomainPlatform struct {
	tk.BasePlatformClient
	routes    []sdk.IngressRoute
	exposed   []sdk.IngressExposeRequest
	unexposed []string
	authCalls []map[string]any
}

func (p *communityDomainPlatform) PlatformInfo() (*sdk.PlatformInfo, error) {
	return &sdk.PlatformInfo{PublicURL: "http://127.0.0.1:5280"}, nil
}

func (p *communityDomainPlatform) ExposeIngress(req sdk.IngressExposeRequest) (*sdk.IngressRoute, error) {
	p.exposed = append(p.exposed, req)
	route := sdk.IngressRoute{Hostname: req.Hostname, Target: req.Target, ProjectID: req.ProjectID, OwnerKind: req.OwnerKind, TLSMode: req.TLSMode, AllowHTTP: req.AllowHTTP, Status: "active"}
	p.routes = []sdk.IngressRoute{route}
	return &p.routes[0], nil
}

func (p *communityDomainPlatform) ListIngressRoutes() ([]sdk.IngressRoute, error) {
	return p.routes, nil
}

func (p *communityDomainPlatform) UnexposeIngress(hostname string) error {
	p.unexposed = append(p.unexposed, hostname)
	p.routes = nil
	return nil
}

func (p *communityDomainPlatform) CallAppResult(app, tool string, input map[string]any, output any) error {
	if app == "auth" && tool == "auth_clients_update" {
		p.authCalls = append(p.authCalls, input)
	}
	encoded, _ := json.Marshal(map[string]any{"ok": true})
	return json.Unmarshal(encoded, output)
}

func TestCommunityDomainAttachAndDetachUseNativeIngressAndAuthOrigin(t *testing.T) {
	platform := &communityDomainPlatform{}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("test-proj"), tk.WithPlatform(platform))
	globalCtx = ctx
	community := mustCreateCommunity(t, ctx, "makecademy", "Makecademy")
	if _, err := toolCommunitiesUpdate(ctx, map[string]any{
		"id": community.ID, "auth_client_id": "akc_makecademy", "auth_organization_slug": "makecademy",
	}); err != nil {
		t.Fatal(err)
	}

	result, err := toolCommunityDomainAttach(ctx, map[string]any{
		"community_id": community.ID, "apex_domain": "makecademy.localhost", "subdomain": "",
		"dns_target": "127.0.0.1", "auto_dns": false, "allow_http": true, "http_port": float64(5280),
	})
	if err != nil {
		t.Fatal(err)
	}
	status := result.(communityDomainStatus)
	if status.PortalURL != "http://makecademy.localhost:5280" || !status.Configured {
		t.Fatalf("status=%+v", status)
	}
	if len(platform.exposed) != 1 || platform.exposed[0].Hostname != "makecademy.localhost" || !strings.HasPrefix(platform.exposed[0].Target, "app://community?") || !strings.Contains(platform.exposed[0].Target, "ingress_auth=app_token") {
		t.Fatalf("native ingress request=%+v", platform.exposed)
	}
	if len(platform.authCalls) != 1 || platform.authCalls[0]["client_id"] != "akc_makecademy" {
		t.Fatalf("Auth origin add calls=%+v", platform.authCalls)
	}

	if _, err := toolCommunityDomainDetach(ctx, map[string]any{"community_id": community.ID}); err != nil {
		t.Fatal(err)
	}
	if len(platform.unexposed) != 1 || platform.unexposed[0] != "makecademy.localhost" {
		t.Fatalf("unexposed=%v", platform.unexposed)
	}
	if len(platform.authCalls) != 2 || platform.authCalls[1]["remove_allowed_origins"] == nil {
		t.Fatalf("Auth origin remove calls=%+v", platform.authCalls)
	}
}

func TestCommunityCustomHostInjectsTenantAndCleanCheckoutRoute(t *testing.T) {
	ctx, _ := newTestCtx(t)
	community := mustCreateCommunity(t, ctx, "makecademy", "Makecademy")
	if _, err := ctx.AppDB().Exec(`UPDATE communities SET portal_host=?, portal_dns_domain=?, portal_dns_name=? WHERE id=?`, "https://courses.example.test", "example.test", "courses", community.ID); err != nil {
		t.Fatal(err)
	}
	uiDir := t.TempDir()
	indexDir := filepath.Join(uiDir, "portal", "dist")
	if err := os.MkdirAll(indexDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(indexDir, "index.html"), []byte(`<html><head><link href="./style.css"></head><body><script src="./main.js"></script></body></html>`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("APTEVA_UI_DIR", uiDir)

	req := httptest.NewRequest(http.MethodGet, "https://courses.example.test/checkout/esp32-starter?offer=21", nil)
	req.Host = "courses.example.test"
	rec := httptest.NewRecorder()
	(&App{}).httpPortalHostPage(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	for _, expected := range []string{`"community":"makecademy"`, `"project_id":"test-proj"`, `"product":"esp32-starter"`, `"offer":"21"`, `"intent":"buy"`, `href="/ui/portal/dist/style.css"`, `src="/ui/portal/dist/main.js"`} {
		if !strings.Contains(rec.Body.String(), expected) {
			t.Errorf("portal HTML missing %s: %s", expected, rec.Body.String())
		}
	}
}

func TestCommunityPortalGatewayRestoresVisitorAuthorizationAndIsAllowlisted(t *testing.T) {
	ctx, _ := newTestCtx(t)
	var gotAuthorization, gotInternalHeader string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuthorization = r.Header.Get("Authorization")
		gotInternalHeader = r.Header.Get("X-Apteva-Original-Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()
	t.Setenv("APTEVA_GATEWAY_URL", upstream.URL)
	globalCtx = ctx

	req := httptest.NewRequest(http.MethodPost, "https://courses.example.test/api/apps/community/mcp?project_id=test-proj", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer internal-app-token")
	req.Header.Set("X-Apteva-Original-Authorization", "Bearer visitor-token")
	rec := httptest.NewRecorder()
	(&App{}).httpPortalGatewayBridge(rec, req)
	if rec.Code != http.StatusOK || gotAuthorization != "Bearer visitor-token" || gotInternalHeader != "" {
		t.Fatalf("status=%d auth=%q internal=%q body=%s", rec.Code, gotAuthorization, gotInternalHeader, rec.Body.String())
	}

	blocked := httptest.NewRecorder()
	(&App{}).httpPortalGatewayBridge(blocked, httptest.NewRequest(http.MethodGet, "https://courses.example.test/api/admin/users", nil))
	if blocked.Code != http.StatusNotFound {
		t.Fatalf("unallowlisted gateway status=%d", blocked.Code)
	}
}

func TestCommunityHTTPRoutesMountWithoutServeMuxConflicts(t *testing.T) {
	mux := http.NewServeMux()
	for _, route := range (&App{}).HTTPRoutes() {
		pattern := route.Pattern
		if route.Method != "" {
			pattern = route.Method + " " + pattern
		}
		mux.HandleFunc(pattern, route.Handler)
	}
}
