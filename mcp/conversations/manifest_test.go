package main

import (
	"os"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

// TestManifestMatchesAptevaYAML — the release mechanics require the
// version (and everything else) in apteva.yaml and main.go's embedded
// manifestYAML to move together; the source-installer reads the file,
// the running sidecar serves the constant.
func TestManifestMatchesAptevaYAML(t *testing.T) {
	onDisk, err := os.ReadFile("apteva.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(onDisk)) != strings.TrimSpace(manifestYAML) {
		t.Fatal("apteva.yaml and main.go manifestYAML have diverged — keep them byte-identical")
	}
}

// TestHTTPRoutesAvoidReservedPrefixes — the SDK owns /health /manifest
// /mcp /events /ui/ on every sidecar; an app route on one of those
// panics the sidecar at boot ("Waiting for health check…").
func TestHTTPRoutesAvoidReservedPrefixes(t *testing.T) {
	reserved := []string{"/health", "/manifest", "/mcp", "/events", "/ui/"}
	app := &App{}
	for _, route := range app.HTTPRoutes() {
		for _, prefix := range reserved {
			if route.Pattern == prefix || strings.HasPrefix(route.Pattern, strings.TrimSuffix(prefix, "/")+"/") {
				t.Errorf("route %q collides with reserved prefix %q", route.Pattern, prefix)
			}
		}
	}
}

// TestManifestToolsMatchCode — every tool the manifest advertises
// exists in MCPTools() and vice versa, so the dashboard's tool list
// never drifts from what the sidecar actually serves.
func TestManifestToolsMatchCode(t *testing.T) {
	app := &App{}
	manifest := app.Manifest()

	declared := map[string]bool{}
	for _, tool := range manifest.Provides.MCPTools {
		declared[tool.Name] = true
	}
	implemented := map[string]bool{}
	for _, tool := range app.MCPTools() {
		implemented[tool.Name] = true
	}
	for name := range declared {
		if !implemented[name] {
			t.Errorf("manifest declares %q but MCPTools() does not implement it", name)
		}
	}
	for name := range implemented {
		if !declared[name] {
			t.Errorf("MCPTools() implements %q but the manifest does not declare it", name)
		}
	}
}

func TestManifestDeclaresScopedAgentConversationWidget(t *testing.T) {
	manifest := (&App{}).Manifest()
	for _, component := range manifest.Provides.UIComponents {
		if component.Name != "agent-conversations" {
			continue
		}
		if component.Entry != "/ui/AgentConversationsWidget.mjs" ||
			len(component.Slots) != 1 || component.Slots[0] != sdk.UIComponentSlotDashboardBuild ||
			component.Visibility != sdk.UIComponentVisibilityAttached ||
			component.DefaultSize != "full" {
			t.Fatalf("agent-conversations component=%+v", component)
		}
		if _, err := os.Stat("ui/AgentConversationsWidget.mjs"); err != nil {
			t.Fatalf("widget bundle: %v", err)
		}
		return
	}
	t.Fatal("agent-conversations component missing")
}

func TestConversationOwnershipIsTaughtAtEveryModelSurface(t *testing.T) {
	app := &App{}
	descriptions := map[string]string{}
	for _, tool := range app.MCPTools() {
		descriptions[tool.Name] = tool.Description
	}
	wants := map[string][]string{
		"send":             {"originating conversation thread", "generic workers report to their parent"},
		"request_approval": {"owned by main or by the originating conversation", "Generic workers report"},
		"report":           {"Main-thread global output only", "generic workers report results"},
		"alert":            {"global alert from main", "conversation-local urgent alert", "Generic workers report"},
		"create":           {"Main-thread conversation management only", "generic workers do not create"},
		"list":             {"Main-thread conversation management only", "generic workers report"},
		"history":          {"conversation thread may read only its own exact conversation", "generic workers are not granted"},
	}
	for tool, fragments := range wants {
		for _, fragment := range fragments {
			if !strings.Contains(descriptions[tool], fragment) {
				t.Errorf("%s description missing %q:\n%s", tool, fragment, descriptions[tool])
			}
		}
	}

	skill, err := os.ReadFile("skills/using-conversations.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{
		"Threads are opaque identifiers",
		"Generic workers never publish through Conversations",
		"do not grant the Conversations MCP",
		"worker needs approval, it reports the exact",
		"same capability-ownership pattern used by Tasks",
	} {
		if !strings.Contains(string(skill), fragment) {
			t.Errorf("using-conversations skill missing %q", fragment)
		}
	}
}
