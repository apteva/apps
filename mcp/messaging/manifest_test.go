package main

import (
	"os"
	"reflect"
	"sort"
	"strconv"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func TestEmbeddedManifest_Valid(t *testing.T) {
	app := &App{}
	m := app.Manifest()
	if m.Name != "messaging" {
		t.Errorf("name=%q", m.Name)
	}
	if m.Version == "" {
		t.Error("version empty")
	}
	if m.DB == nil || m.DB.Migrations == "" {
		t.Error("db.migrations missing")
	}
	if len(m.Provides.UIComponents) != 1 {
		t.Fatalf("ui components=%d, want 1", len(m.Provides.UIComponents))
	}
	widget := m.Provides.UIComponents[0]
	if widget.Name != "messages" || widget.Entry != "/ui/MessagingWidget.mjs" {
		t.Fatalf("unexpected messaging widget: %+v", widget)
	}
	if widget.Native == nil || widget.Native.Schema != sdk.NativeSurfaceSchemaCurrent || widget.Native.Entry != "/ui/surfaces/messages.json" {
		t.Fatalf("messaging native widget is not advertised: %+v", widget.Native)
	}
}

func TestMCPTools_DeclaredMatchHandlers(t *testing.T) {
	app := &App{}
	declared := map[string]bool{}
	for _, t := range app.Manifest().Provides.MCPTools {
		declared[t.Name] = true
	}
	implemented := map[string]bool{}
	for _, t := range app.MCPTools() {
		implemented[t.Name] = true
	}
	for name := range declared {
		if !implemented[name] {
			t.Errorf("manifest declares %q but no handler implements it", name)
		}
	}
	for name := range implemented {
		if !declared[name] {
			t.Errorf("handler implements %q but manifest doesn't declare it", name)
		}
	}
}

func TestManifestAndYAMLAgree(t *testing.T) {
	diskBytes, err := os.ReadFile("apteva.yaml")
	if err != nil {
		t.Fatal(err)
	}
	disk, err := sdk.ParseManifest(diskBytes)
	if err != nil {
		t.Fatalf("parse disk manifest: %v", err)
	}
	embedded, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		t.Fatalf("parse embedded manifest: %v", err)
	}
	if disk.Name != embedded.Name || disk.Version != embedded.Version {
		t.Fatalf("identity drift: disk=%s@%s embedded=%s@%s", disk.Name, disk.Version, embedded.Name, embedded.Version)
	}
	if !reflect.DeepEqual(sortedToolNames(disk.Provides.MCPTools), sortedToolNames(embedded.Provides.MCPTools)) {
		t.Fatalf("MCP tool drift: disk=%v embedded=%v", sortedToolNames(disk.Provides.MCPTools), sortedToolNames(embedded.Provides.MCPTools))
	}
	if !reflect.DeepEqual(sortedPermissions(disk), sortedPermissions(embedded)) {
		t.Fatalf("permission drift: disk=%v embedded=%v", sortedPermissions(disk), sortedPermissions(embedded))
	}
	if !reflect.DeepEqual(integrationContracts(disk), integrationContracts(embedded)) {
		t.Fatalf("integration drift: disk=%v embedded=%v", integrationContracts(disk), integrationContracts(embedded))
	}
	if !reflect.DeepEqual(configContracts(disk), configContracts(embedded)) {
		t.Fatalf("config drift: disk=%v embedded=%v", configContracts(disk), configContracts(embedded))
	}
	if !reflect.DeepEqual(routeContracts(disk), routeContracts(embedded)) {
		t.Fatalf("HTTP route drift: disk=%v embedded=%v", routeContracts(disk), routeContracts(embedded))
	}
	if !reflect.DeepEqual(disk.Provides.UIComponents, embedded.Provides.UIComponents) {
		t.Fatalf("UI component drift: disk=%+v embedded=%+v", disk.Provides.UIComponents, embedded.Provides.UIComponents)
	}
}

func TestProviderWebhookRoutesArePublic(t *testing.T) {
	expected := map[string]bool{
		"/webhooks/ses-bounces":    false,
		"/webhooks/ses-inbound":    false,
		"/webhooks/twilio-inbound": false,
		"/webhooks/twilio-status":  false,
	}
	for _, route := range (&App{}).HTTPRoutes() {
		if _, ok := expected[route.Pattern]; !ok {
			if route.NoAuth {
				t.Errorf("non-webhook route %q must require app authentication", route.Pattern)
			}
			continue
		}
		if route.Method != "POST" || !route.NoAuth {
			t.Errorf("provider webhook %q must be public POST, got method=%q no_auth=%v", route.Pattern, route.Method, route.NoAuth)
		}
		expected[route.Pattern] = true
	}
	for pattern, found := range expected {
		if !found {
			t.Errorf("provider webhook route %q is not registered", pattern)
		}
	}

	manifest := (&App{}).Manifest()
	public := map[string]bool{}
	for _, route := range manifest.Provides.HTTPRoutes {
		if route.NoAuth {
			public[route.Prefix] = route.Method == "POST"
		}
	}
	for pattern := range expected {
		if !public[pattern] {
			t.Errorf("manifest must expose %q as public POST", pattern)
		}
	}
}

func sortedToolNames(tools []sdk.MCPToolSpec) []string {
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	sort.Strings(names)
	return names
}

func sortedPermissions(manifest *sdk.Manifest) []string {
	permissions := make([]string, 0, len(manifest.Requires.Permissions))
	for _, permission := range manifest.Requires.Permissions {
		permissions = append(permissions, string(permission))
	}
	sort.Strings(permissions)
	return permissions
}

func integrationContracts(manifest *sdk.Manifest) []string {
	contracts := make([]string, 0, len(manifest.Requires.Integrations))
	for _, integration := range manifest.Requires.Integrations {
		contracts = append(contracts, integration.Role+"|"+integration.Kind+"|"+strconv.FormatBool(integration.Required))
	}
	sort.Strings(contracts)
	return contracts
}

func configContracts(manifest *sdk.Manifest) []string {
	contracts := make([]string, 0, len(manifest.ConfigSchema))
	for _, field := range manifest.ConfigSchema {
		contracts = append(contracts, field.Name+"|"+field.Type)
	}
	sort.Strings(contracts)
	return contracts
}

func routeContracts(manifest *sdk.Manifest) []string {
	contracts := make([]string, 0, len(manifest.Provides.HTTPRoutes))
	for _, route := range manifest.Provides.HTTPRoutes {
		contracts = append(contracts, route.Method+"|"+route.Prefix+"|"+strconv.FormatBool(route.NoAuth))
	}
	sort.Strings(contracts)
	return contracts
}
