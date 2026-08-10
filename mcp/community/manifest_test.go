package main

import (
	"os"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

// TestManifestParses guards the embedded YAML against drift — if the
// constant in main.go stops matching apteva.yaml's schema, this fails
// before the sidecar boot does.
func TestManifestParses(t *testing.T) {
	m, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		t.Fatalf("parse embedded manifest: %v", err)
	}
	if m.Name != "community" {
		t.Fatalf("name = %q", m.Name)
	}
	if m.Version == "" {
		t.Fatal("version missing")
	}
	if m.Runtime.Kind != "source" {
		t.Fatalf("runtime.kind = %q", m.Runtime.Kind)
	}
	if m.DB == nil || m.DB.Driver != "sqlite" {
		t.Fatalf("db.driver missing")
	}
	if len(m.Provides.UIPanels) == 0 {
		t.Fatal("expected at least one UI panel")
	}
	externalRaw, err := os.ReadFile("apteva.yaml")
	if err != nil {
		t.Fatalf("read external manifest: %v", err)
	}
	external, err := sdk.ParseManifest(externalRaw)
	if err != nil {
		t.Fatalf("parse external manifest: %v", err)
	}
	if external.Name != m.Name || external.Version != m.Version || external.Icon != m.Icon {
		t.Fatalf("embedded/external manifest drift: embedded=%s@%s icon=%q external=%s@%s icon=%q",
			m.Name, m.Version, m.Icon, external.Name, external.Version, external.Icon)
	}
	if m.Provides.UIPanels[0].Entry != "/ui/CommunityPanel.mjs" {
		t.Fatalf("panel entry = %q", m.Provides.UIPanels[0].Entry)
	}
	foundPublicPortal := false
	for _, route := range m.Provides.HTTPRoutes {
		if route.Prefix == "/ui/" && route.Method == "GET" && route.NoAuth {
			foundPublicPortal = true
		}
	}
	if !foundPublicPortal {
		t.Fatal("member portal assets must be exposed as an anonymous GET /ui/ route")
	}
	if !strings.Contains(m.Description, "/api/apps/community/_install/{install_id}/ui/portal/dist/index.html") {
		t.Fatal("manifest must document the install-scoped portal URL so relative assets resolve anonymously")
	}
	required := map[string]bool{}
	events := map[string][]string{}
	versions := map[string]string{}
	for _, dep := range m.Requires.Apps {
		required[dep.Name] = !dep.Optional
		events[dep.Name] = dep.Events
		versions[dep.Name] = dep.Version
	}
	for _, name := range []string{"auth", "catalog", "billing", "subscriptions"} {
		if !required[name] {
			t.Errorf("%s must be a required Community dependency", name)
		}
	}
	if _, hasSaaS := required["saas"]; hasSaaS {
		t.Fatal("Community must orchestrate Billing and Subscriptions directly, not depend on SaaS")
	}
	if versions["auth"] != ">=0.8.0" || versions["catalog"] != ">=0.3.0" ||
		versions["billing"] != ">=0.12.2" || versions["subscriptions"] != ">=0.7.2" {
		t.Errorf("unexpected dependency versions: auth=%q catalog=%q billing=%q subscriptions=%q",
			versions["auth"], versions["catalog"], versions["billing"], versions["subscriptions"])
	}
	for _, event := range []string{"invoice.paid", "invoice.refunded", "invoice.voided", "invoice.payment_failed"} {
		if !containsString(events["billing"], event) {
			t.Errorf("Community must subscribe to billing %s, got %v", event, events["billing"])
		}
	}
	for _, event := range []string{"subscription.active", "subscription.past_due", "subscription.cancelled", "subscription.resumed", "subscription.ended", "subscription.cycle_due"} {
		if !containsString(events["subscriptions"], event) {
			t.Errorf("Community must subscribe to subscriptions %s, got %v", event, events["subscriptions"])
		}
	}
}

func TestPortalAuthRoutesAreProjectScoped(t *testing.T) {
	source, err := os.ReadFile("ui/portal/src/api.ts")
	if err != nil {
		t.Fatalf("read portal API source: %v", err)
	}
	for _, route := range []string{`projectScopedPath("/login")`, `projectScopedPath("/signup")`} {
		if !strings.Contains(string(source), route) {
			t.Errorf("portal auth route missing project scope: %s", route)
		}
	}
	if !strings.Contains(string(source), "projectId: currentProjectId()") {
		t.Error("portal AptevaClient must project-scope Community MCP calls")
	}
}

// TestToolSetMatchesManifest catches forgetting to wire a manifest-
// declared MCP tool into MCPTools(). The platform displays manifest
// tools to operators; missing handlers manifest as "tool not found"
// at MCP call time, which is too late.
func TestToolSetMatchesManifest(t *testing.T) {
	m, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	declared := map[string]bool{}
	for _, t := range m.Provides.MCPTools {
		declared[t.Name] = true
	}
	app := &App{}
	implemented := map[string]bool{}
	for _, t := range app.MCPTools() {
		implemented[t.Name] = true
	}
	for name := range declared {
		if !implemented[name] {
			t.Errorf("manifest declares %q but no handler is registered", name)
		}
	}
	for name := range implemented {
		if !declared[name] {
			t.Errorf("handler %q registered but not declared in manifest", name)
		}
	}
}
