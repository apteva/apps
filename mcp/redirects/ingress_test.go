package main

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type redirectsIngressPlatform struct {
	tk.BasePlatformClient
	exposed    []sdk.IngressExposeRequest
	unexposed  []string
	domainSets []map[string]any
	domains    []string
}

func (p *redirectsIngressPlatform) ExposeIngress(req sdk.IngressExposeRequest) (*sdk.IngressRoute, error) {
	p.exposed = append(p.exposed, req)
	return &sdk.IngressRoute{
		Hostname:  req.Hostname,
		Target:    req.Target,
		ProjectID: req.ProjectID,
		OwnerKind: req.OwnerKind,
		CertFQDN:  req.CertFQDN,
	}, nil
}

func (p *redirectsIngressPlatform) UnexposeIngress(hostname string) error {
	p.unexposed = append(p.unexposed, hostname)
	return nil
}

func (p *redirectsIngressPlatform) CallAppResult(app, tool string, args map[string]any, out any) error {
	if app != "domains" {
		return tk.ErrNotImplemented
	}
	switch tool {
	case "domain_list":
		resp := struct {
			Domains []struct {
				Name string `json:"name"`
			} `json:"domains"`
		}{}
		for _, name := range p.domains {
			resp.Domains = append(resp.Domains, struct {
				Name string `json:"name"`
			}{Name: name})
		}
		b, _ := json.Marshal(resp)
		return json.Unmarshal(b, out)
	case "domain_records_set":
		p.domainSets = append(p.domainSets, args)
		if out != nil {
			b := []byte(`{"record":{"id":"test-record"}}`)
			return json.Unmarshal(b, out)
		}
		return nil
	default:
		return tk.ErrNotImplemented
	}
}

func TestRedirectAddUsesPlatformIngress(t *testing.T) {
	pf := &redirectsIngressPlatform{}
	ctx := tk.NewAppCtx(t, "apteva.yaml",
		tk.WithProjectID("proj-1"),
		tk.WithEnv("APTEVA_APP_PORT", "9123"),
		tk.WithPlatform(pf),
	)

	_, err := (&App{}).toolRedirectAdd(ctx, map[string]any{
		"hostname":    "go.example.com",
		"destination": "https://example.com/landing",
	})
	if err != nil {
		t.Fatalf("redirect_add: %v", err)
	}
	if len(pf.exposed) != 1 {
		t.Fatalf("ExposeIngress calls = %d, want 1", len(pf.exposed))
	}
	got := pf.exposed[0]
	if got.Hostname != "go.example.com" ||
		got.Target != "http://127.0.0.1:9123" ||
		got.ProjectID != "proj-1" ||
		got.OwnerKind != "redirects" ||
		got.CertFQDN != "go.example.com" {
		t.Fatalf("unexpected ExposeIngress request: %+v", got)
	}
}

func TestRedirectRemoveUnexposesOnlyLastHostnameRule(t *testing.T) {
	pf := &redirectsIngressPlatform{}
	ctx := tk.NewAppCtx(t, "apteva.yaml",
		tk.WithProjectID("proj-1"),
		tk.WithPlatform(pf),
	)
	app := &App{}

	first, err := app.toolRedirectAdd(ctx, map[string]any{
		"hostname": "go.example.com", "path": "/a",
		"destination": "https://example.com/a",
	})
	if err != nil {
		t.Fatalf("add first: %v", err)
	}
	second, err := app.toolRedirectAdd(ctx, map[string]any{
		"hostname": "go.example.com", "path": "/b",
		"destination": "https://example.com/b",
	})
	if err != nil {
		t.Fatalf("add second: %v", err)
	}

	firstRule := first.(map[string]any)["redirect"].(*Redirect)
	if _, err := app.toolRedirectRemove(ctx, map[string]any{"id": firstRule.ID}); err != nil {
		t.Fatalf("remove first: %v", err)
	}
	if len(pf.unexposed) != 0 {
		t.Fatalf("unexposed after first remove = %#v, want none", pf.unexposed)
	}

	secondRule := second.(map[string]any)["redirect"].(*Redirect)
	if _, err := app.toolRedirectRemove(ctx, map[string]any{"id": secondRule.ID}); err != nil {
		t.Fatalf("remove second: %v", err)
	}
	if !reflect.DeepEqual(pf.unexposed, []string{"go.example.com"}) {
		t.Fatalf("unexposed = %#v", pf.unexposed)
	}
}

func TestRedirectUpdateUnexposesOldHostnameAndExposesNew(t *testing.T) {
	pf := &redirectsIngressPlatform{}
	ctx := tk.NewAppCtx(t, "apteva.yaml",
		tk.WithProjectID("proj-1"),
		tk.WithPlatform(pf),
	)
	app := &App{}

	res, err := app.toolRedirectAdd(ctx, map[string]any{
		"hostname":    "old.example.com",
		"destination": "https://example.com",
	})
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	rule := res.(map[string]any)["redirect"].(*Redirect)
	if _, err := app.toolRedirectUpdate(ctx, map[string]any{
		"id":          rule.ID,
		"hostname":    "new.example.com",
		"destination": "https://example.com",
	}); err != nil {
		t.Fatalf("update: %v", err)
	}

	if !reflect.DeepEqual(pf.unexposed, []string{"old.example.com"}) {
		t.Fatalf("unexposed = %#v", pf.unexposed)
	}
	if len(pf.exposed) != 2 {
		t.Fatalf("ExposeIngress calls = %d, want 2", len(pf.exposed))
	}
	if pf.exposed[1].Hostname != "new.example.com" {
		t.Fatalf("new exposure = %+v", pf.exposed[1])
	}
}

func TestRedirectDNSInfersARecordForIPTarget(t *testing.T) {
	pf := &redirectsIngressPlatform{domains: []string{"example.com"}}
	ctx := tk.NewAppCtx(t, "apteva.yaml",
		tk.WithProjectID("proj-1"),
		tk.WithConfig(map[string]string{"public_host": "91.99.117.197"}),
		tk.WithPlatform(pf),
	)

	warning := wireHostname(ctx, "proj-1", "go.example.com")
	if warning != "" {
		t.Fatalf("warning = %q", warning)
	}
	if len(pf.domainSets) != 1 {
		t.Fatalf("domain_records_set calls = %d, want 1", len(pf.domainSets))
	}
	got := pf.domainSets[0]
	if got["domain"] != "example.com" || got["name"] != "go" ||
		got["type"] != "A" || got["value"] != "91.99.117.197" ||
		got["_project_id"] != "proj-1" {
		t.Fatalf("unexpected domain_records_set args: %+v", got)
	}
}

func TestRedirectManifestUsesNativeIngress(t *testing.T) {
	body, err := os.ReadFile("apteva.yaml")
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	s := string(body)
	if !strings.Contains(s, "platform.ingress.write") {
		t.Fatalf("manifest missing platform.ingress.write")
	}
	for _, deprecated := range []string{"name: routes", "name: certs", "routes_register", "cert_issue"} {
		if strings.Contains(s, deprecated) {
			t.Fatalf("manifest still contains deprecated dependency %q", deprecated)
		}
	}
}

func TestEmbeddedManifestUsesReleaseFile(t *testing.T) {
	manifest := (&App{}).Manifest()
	if manifest.Name != "redirects" || manifest.Version != "0.3.5" {
		t.Fatalf("embedded manifest=%s@%s", manifest.Name, manifest.Version)
	}
	foundRead := false
	for _, permission := range manifest.Requires.Permissions {
		if permission == "platform.ingress.read" {
			foundRead = true
		}
	}
	if !foundRead {
		t.Fatalf("embedded manifest missing platform.ingress.read")
	}
}

func TestReconcilePreservesClaimProject(t *testing.T) {
	t.Setenv("APTEVA_PROJECT_ID", "")
	pf := &redirectsIngressPlatform{}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithPlatform(pf))
	if _, err := dbInsertRedirect(ctx.AppDB(), RedirectInput{
		Hostname: "a.example.com", Destination: "https://example.com/a", ProjectID: "project-a",
	}); err != nil {
		t.Fatalf("insert a: %v", err)
	}
	if _, err := dbInsertRedirect(ctx.AppDB(), RedirectInput{
		Hostname: "b.example.com", Destination: "https://example.com/b", ProjectID: "project-b",
	}); err != nil {
		t.Fatalf("insert b: %v", err)
	}

	reconcileRegisteredRoutes(ctx)
	if len(pf.exposed) != 2 {
		t.Fatalf("exposed=%d", len(pf.exposed))
	}
	projects := map[string]string{}
	for _, route := range pf.exposed {
		projects[route.Hostname] = route.ProjectID
	}
	if projects["a.example.com"] != "project-a" || projects["b.example.com"] != "project-b" {
		t.Fatalf("project ownership lost: %+v", projects)
	}
}

func TestIPv6PublicHostCreatesAAAARecord(t *testing.T) {
	pf := &redirectsIngressPlatform{domains: []string{"example.com"}}
	ctx := tk.NewAppCtx(t, "apteva.yaml",
		tk.WithProjectID("proj-1"),
		tk.WithConfig(map[string]string{"public_host": "2001:db8::1"}),
		tk.WithPlatform(pf),
	)
	if warning := wireHostname(ctx, "proj-1", "go.example.com"); warning != "" {
		t.Fatalf("warning=%q", warning)
	}
	if len(pf.domainSets) != 1 || pf.domainSets[0]["type"] != "AAAA" || pf.domainSets[0]["value"] != "2001:db8::1" {
		t.Fatalf("domain sets=%+v", pf.domainSets)
	}
}

func TestRuleEventsUseRuleProject(t *testing.T) {
	recorder := tk.NewEmitRecorder()
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithEmitter(recorder))
	rule := &Redirect{ID: 42, Hostname: "go.example.com", ProjectID: "project-a"}
	emitRuleChange(ctx, "rule.updated", rule)
	emitHit(ctx, rule, "https://example.com/landing?source=email")
	events := recorder.Events()
	if len(events) != 2 {
		t.Fatalf("events=%+v", events)
	}
	for _, event := range events {
		if event.ProjectID != "project-a" {
			t.Fatalf("event %s project=%q", event.Topic, event.ProjectID)
		}
	}
	hitData, ok := events[1].Data.(map[string]any)
	if !ok || hitData["target"] != "https://example.com/landing?source=email" {
		t.Fatalf("hit event target=%#v", events[1].Data)
	}
}
