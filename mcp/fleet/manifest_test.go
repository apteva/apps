package main

// Tier 1: manifest sanity. Catches the bug class where the on-disk
// apteva.yaml parses differently than the embedded manifestYAML in
// main.go, or where MCPTools() lists tools the manifest doesn't
// declare (or vice versa). Both bugs shipped during v0.2.x:
//
//   - v0.2.0: apteva.yaml had unquoted "Args: …" descriptions that
//             tripped the YAML parser at install time. The embedded
//             manifest used short descriptions and parsed fine.
//   - v0.2.0: apteva.yaml's ui_panels block was missing from the
//             embedded manifest, so the panel never mounted.
//
// These tests would have caught both.

import (
	"os"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func TestEmbeddedManifest_Valid(t *testing.T) {
	app := &App{}
	m := app.Manifest()
	if m.Name != "fleet" {
		t.Errorf("name=%q want fleet", m.Name)
	}
	if m.Version == "" {
		t.Error("version is empty")
	}
	if m.DB == nil || m.DB.Migrations == "" {
		t.Error("db.migrations missing — sidecar boot will skip migrations")
	}
}

func TestOnDiskManifest_Parses(t *testing.T) {
	// Catches v0.2.0's "Args: …" colon-in-unquoted-scalar bug.
	b, err := os.ReadFile("apteva.yaml")
	if err != nil {
		t.Fatalf("read apteva.yaml: %v", err)
	}
	m, err := sdk.ParseManifest(b)
	if err != nil {
		t.Fatalf("parse apteva.yaml: %v", err)
	}
	if m.Name != "fleet" {
		t.Errorf("apteva.yaml name=%q", m.Name)
	}
}

func TestManifestsAgree_VersionAndScopes(t *testing.T) {
	// apteva.yaml is what the registry/marketplace hands to a fresh
	// installer; main.go's embedded manifest is what apteva-server
	// reads after the binary is mounted. The two drifted in v0.2.0
	// (panel slot present in one, missing in the other). Pin the
	// fields that matter to both surfaces.
	embedded := (&App{}).Manifest()
	b, err := os.ReadFile("apteva.yaml")
	if err != nil {
		t.Fatalf("read apteva.yaml: %v", err)
	}
	disk, err := sdk.ParseManifest(b)
	if err != nil {
		t.Fatalf("parse apteva.yaml: %v", err)
	}

	if embedded.Version != disk.Version {
		t.Errorf("version drift: embedded=%q disk=%q", embedded.Version, disk.Version)
	}
	if len(embedded.Scopes) != len(disk.Scopes) {
		t.Errorf("scopes drift: embedded=%v disk=%v", embedded.Scopes, disk.Scopes)
	}
}

func TestManifestUsesServerNativeIngress(t *testing.T) {
	manifests := map[string]sdk.Manifest{"embedded": (&App{}).Manifest()}
	b, err := os.ReadFile("apteva.yaml")
	if err != nil {
		t.Fatalf("read apteva.yaml: %v", err)
	}
	disk, err := sdk.ParseManifest(b)
	if err != nil {
		t.Fatalf("parse apteva.yaml: %v", err)
	}
	manifests["disk"] = *disk

	for label, m := range manifests {
		hasIngress := false
		for _, p := range m.Requires.Permissions {
			if string(p) == "platform.ingress.write" {
				hasIngress = true
			}
		}
		if !hasIngress {
			t.Errorf("%s manifest missing platform.ingress.write", label)
		}
		for _, dep := range m.Requires.Integrations {
			if dep.Role == "certs" || dep.Role == "routes" {
				t.Errorf("%s manifest still requires legacy %q integration", label, dep.Role)
			}
		}
	}
}

func TestManifestDeclaresTemplateReadPermission(t *testing.T) {
	manifests := map[string]sdk.Manifest{"embedded": (&App{}).Manifest()}
	b, err := os.ReadFile("apteva.yaml")
	if err != nil {
		t.Fatal(err)
	}
	disk, err := sdk.ParseManifest(b)
	if err != nil {
		t.Fatal(err)
	}
	manifests["disk"] = *disk
	for label, manifest := range manifests {
		found := false
		for _, permission := range manifest.Requires.Permissions {
			found = found || permission == sdk.PermTemplatesRead
		}
		if !found {
			t.Errorf("%s manifest missing platform.templates.read", label)
		}
	}
}

func TestManifestDeclaresOptionalA2A(t *testing.T) {
	manifests := map[string]sdk.Manifest{"embedded": (&App{}).Manifest()}
	b, err := os.ReadFile("apteva.yaml")
	if err != nil {
		t.Fatalf("read apteva.yaml: %v", err)
	}
	disk, err := sdk.ParseManifest(b)
	if err != nil {
		t.Fatalf("parse apteva.yaml: %v", err)
	}
	manifests["disk"] = *disk
	for label, manifest := range manifests {
		found := false
		for _, dep := range manifest.Requires.Apps {
			if dep.Name != "a2a" {
				continue
			}
			found = true
			if !dep.Optional || dep.Version != ">=0.4.0" {
				t.Errorf("%s A2A dependency=%+v", label, dep)
			}
		}
		if !found {
			t.Errorf("%s manifest has no optional A2A dependency", label)
		}
	}
}

func TestMCPTools_DeclaredMatchHandlers(t *testing.T) {
	// Every tool the manifest declares must have a matching handler,
	// and every handler must be declared. Mismatch = the dashboard
	// renders a tool button that 404s, or the agent invokes a tool
	// the marketplace's tool-picker doesn't know about.
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

func TestFleetBackupToolSchemasAllowStreamingHandshake(t *testing.T) {
	tools := map[string]sdk.Tool{}
	for _, tool := range (&App{}).MCPTools() {
		tools[tool.Name] = tool
	}
	snapshotProps, _ := tools["fleet_tenant_snapshot"].InputSchema["properties"].(map[string]any)
	if _, ok := snapshotProps["supports_streaming"]; !ok {
		t.Fatal("fleet_tenant_snapshot schema is missing supports_streaming")
	}
	restore := tools["fleet_tenant_restore"].InputSchema
	restoreProps, _ := restore["properties"].(map[string]any)
	if _, ok := restoreProps["prepare_stream"]; !ok {
		t.Fatal("fleet_tenant_restore schema is missing prepare_stream")
	}
	if required, ok := restore["required"].([]string); ok {
		for _, field := range required {
			if field == "archive_b64" {
				t.Fatal("fleet_tenant_restore requires archive_b64 and rejects streaming handshakes")
			}
		}
	}
}

func TestPanel_DeclaredInBothManifests(t *testing.T) {
	// v0.2.0 shipped with apteva.yaml declaring the FleetPanel slot
	// but main.go's embedded manifest omitting it — net effect: the
	// panel file existed, was bundled, but the platform never wired
	// it up. Pin the slot in both.
	b, err := os.ReadFile("apteva.yaml")
	if err != nil {
		t.Fatalf("read apteva.yaml: %v", err)
	}
	disk, err := sdk.ParseManifest(b)
	if err != nil {
		t.Fatalf("parse apteva.yaml: %v", err)
	}
	if len(disk.Provides.UIPanels) == 0 {
		t.Error("apteva.yaml: no ui_panels declared")
	}
	if len((&App{}).Manifest().Provides.UIPanels) == 0 {
		t.Error("embedded manifest: no ui_panels declared")
	}
}

func TestOnlySignedCallbackRoutesBypassPlatformAuth(t *testing.T) {
	routes := (&App{}).HTTPRoutes()
	for _, route := range routes {
		switch route.Pattern {
		case "/transfers/", "/provider-grants/":
			if !route.NoAuth {
				t.Errorf("%s must bypass platform auth because it uses a scoped signed credential", route.Pattern)
			}
		default:
			if route.NoAuth {
				t.Errorf("authenticated Fleet route %s unexpectedly has NoAuth", route.Pattern)
			}
		}
	}
}
