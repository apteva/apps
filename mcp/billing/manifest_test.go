package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// The embedded manifest must always parse — sdk.Run validates it at
// boot, so a regression here means the binary won't start.
func TestEmbeddedManifest_Valid(t *testing.T) {
	app := &App{}
	m := app.Manifest()
	if m.Name != "billing" {
		t.Errorf("manifest.Name=%q, want billing", m.Name)
	}
	if m.Version == "" {
		t.Error("manifest.Version is empty")
	}
	if m.Icon != "/ui/icon.svg" || m.IconStyle != "monochrome" {
		t.Errorf("embedded manifest icon = (%q, %q), want unified monochrome icon", m.Icon, m.IconStyle)
	}
	if m.DB == nil || m.DB.Migrations == "" {
		t.Errorf("manifest.DB.Migrations missing")
	}
	perms := map[string]bool{}
	for _, p := range m.Requires.Permissions {
		perms[string(p)] = true
	}
	for _, want := range []string{"platform.connections.execute"} {
		if !perms[want] {
			t.Errorf("embedded manifest missing permission %q", want)
		}
	}
	for _, forbidden := range []string{"net.egress", "platform.connections.read_credentials"} {
		if perms[forbidden] {
			t.Errorf("embedded manifest must not request direct Stripe permission %q", forbidden)
		}
	}
	foundProcessor := false
	for _, dep := range m.Requires.Integrations {
		if dep.Role == "payment_processor" {
			foundProcessor = true
			if dep.Kind != "integration" {
				t.Errorf("payment_processor kind=%q, want integration", dep.Kind)
			}
			if dep.Tools["checkout_sessions.create"] == "" {
				t.Error("payment_processor missing checkout_sessions.create tool mapping")
			}
			if dep.Tools["payment_intents.create"] == "" {
				t.Error("payment_processor missing payment_intents.create tool mapping")
			}
			if dep.Tools["webhooks.process"] != "" {
				t.Error("payment_processor must use platform webhook verification, not an integration process_webhook tool")
			}
		}
	}
	if !foundProcessor {
		t.Error("embedded manifest missing payment_processor integration role")
	}
	foundPublicWebhook := false
	for _, route := range m.Provides.HTTPRoutes {
		if route.Prefix == "/webhooks/stripe" && route.NoAuth {
			foundPublicWebhook = true
		}
	}
	if !foundPublicWebhook {
		t.Error("embedded manifest must expose /webhooks/stripe with no_auth=true")
	}
	scopes := map[string]bool{}
	for _, s := range m.Scopes {
		scopes[string(s)] = true
	}
	for _, want := range []string{"project", "global"} {
		if !scopes[want] {
			t.Errorf("manifest missing scope %q", want)
		}
	}
}

// Counts + names must agree between the manifest's mcp_tools list and
// MCPTools(). A common mistake is adding a tool to one and forgetting
// the other; this test catches it.
//
// NOTE: the embedded manifest in main.go is the boot-time minimum
// (just enough for sdk.Run to validate); the canonical tool list
// lives in apteva.yaml. So we read apteva.yaml separately and
// cross-check against MCPTools().
func TestMCPTools_ManifestMatchesHandlers(t *testing.T) {
	app := &App{}
	implemented := map[string]bool{}
	for _, t := range app.MCPTools() {
		implemented[t.Name] = true
	}
	body, err := os.ReadFile("apteva.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, []byte(manifestYAML)) {
		t.Fatal("embedded manifest differs")
	}
	var want []string
	for _, tool := range app.Manifest().Provides.MCPTools {
		want = append(want, tool.Name)
	}
	for _, name := range want {
		if !implemented[name] {
			t.Errorf("expected tool %q to be implemented", name)
		}
	}
	if len(implemented) != len(want) {
		t.Errorf("MCPTools count = %d, want %d (likely added/removed without updating this test)",
			len(implemented), len(want))
	}
}

// Every tool registered must have a non-empty description and a
// JSON-Schema-shaped input schema. The agent reads these verbatim;
// missing descriptions degrade tool selection silently.
func TestMCPTools_AllHaveDescriptionAndSchema(t *testing.T) {
	app := &App{}
	for _, tool := range app.MCPTools() {
		if tool.Description == "" {
			t.Errorf("tool %q has empty description", tool.Name)
		}
		if tool.InputSchema["type"] != "object" {
			t.Errorf("tool %q schema type=%v, want object", tool.Name, tool.InputSchema["type"])
		}
		if _, ok := tool.InputSchema["properties"]; !ok {
			t.Errorf("tool %q schema missing properties", tool.Name)
		}
	}
}

func TestPaymentLinkTool_ExplicitlyOnDemandAndDoesNotClaimDelivery(t *testing.T) {
	var description string
	for _, tool := range (&App{}).MCPTools() {
		if tool.Name == "invoices_send_payment_link" {
			description = strings.ToLower(tool.Description)
			break
		}
	}
	for _, phrase := range []string{"on demand only", "explicitly asks", "does not send email", "separate"} {
		if !strings.Contains(description, phrase) {
			t.Errorf("payment-link description must contain %q; got %q", phrase, description)
		}
	}
}
