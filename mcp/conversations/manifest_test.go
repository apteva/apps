package main

import (
	"encoding/json"
	"net/http"
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
		schema, err := json.Marshal(component.SettingsSchema)
		if err != nil {
			t.Fatal(err)
		}
		for _, required := range []string{`"display_mode"`, `"browser"`, `"single"`, `"show_new_conversation"`, `"show_page_context"`} {
			if !strings.Contains(string(schema), required) {
				t.Fatalf("agent-conversations settings schema missing %s: %s", required, schema)
			}
		}
		bundle, err := os.ReadFile("ui/AgentConversationsWidget.mjs")
		if err != nil {
			t.Fatalf("widget bundle: %v", err)
		}
		// The browser loads the generated bundle, not the TypeScript source.
		// Keep release packaging from silently shipping an older bundle that
		// ignores a setting already declared by the manifest and covered by
		// source-level tests.
		if !strings.Contains(string(bundle), "show_page_context") {
			t.Fatal("agent-conversations bundle is stale: rebuild panels after changing widget settings")
		}
		return
	}
	t.Fatal("agent-conversations component missing")
}

func TestManifestDeclaresInboxOnProjectAndGlobalHome(t *testing.T) {
	manifest := (&App{}).Manifest()
	for _, component := range manifest.Provides.UIComponents {
		if component.Name != "inbox-overview" {
			continue
		}
		if len(component.Slots) != 1 || component.Slots[0] != sdk.UIComponentSlotDashboardHome {
			t.Fatalf("inbox-overview slots=%v", component.Slots)
		}
		wantScopes := []string{sdk.UIComponentDashboardScopeProject, sdk.UIComponentDashboardScopeGlobal}
		if len(component.DashboardScopes) != len(wantScopes) {
			t.Fatalf("inbox-overview dashboard scopes=%v want=%v", component.DashboardScopes, wantScopes)
		}
		for i := range wantScopes {
			if component.DashboardScopes[i] != wantScopes[i] {
				t.Fatalf("inbox-overview dashboard scopes=%v want=%v", component.DashboardScopes, wantScopes)
			}
		}
		return
	}
	t.Fatal("inbox-overview component missing")
}

func TestManifestDeclaresConversationsMobileSurface(t *testing.T) {
	manifest := (&App{}).Manifest()
	if len(manifest.Provides.UISurfaces) != 1 {
		t.Fatalf("ui surfaces=%+v, want one", manifest.Provides.UISurfaces)
	}
	descriptor := manifest.Provides.UISurfaces[0]
	if descriptor.ID != "conversations" || descriptor.Label != "Conversations" ||
		descriptor.Icon != "message-circle" || descriptor.Schema != sdk.NativeSurfaceSchemaCurrent ||
		descriptor.Entry != "/ui/surfaces/conversations.json" ||
		len(descriptor.Slots) != 1 || descriptor.Slots[0] != sdk.UISurfaceSlotMobileProjectApp {
		t.Fatalf("surface descriptor=%+v", descriptor)
	}

	document, err := os.ReadFile("ui/surfaces/conversations.json")
	if err != nil {
		t.Fatal(err)
	}
	surface, err := sdk.ParseNativeSurface(document)
	if err != nil {
		t.Fatalf("parse conversations surface: %v", err)
	}
	if err := sdk.ValidateNativeSurfaceForDescriptor(surface, descriptor); err != nil {
		t.Fatalf("validate conversations surface: %v", err)
	}
	if len(surface.Sections) != 1 || surface.Sections[0].Component != "chat/v1" || surface.Sections[0].Chat == nil {
		t.Fatalf("chat surface=%+v", surface.Sections)
	}
	chat := surface.Sections[0].Chat
	if chat.ConversationsSource != "conversations" || chat.MessagesSource != "messages" ||
		chat.CreateAction != "create-conversation" || chat.SendAction != "send-message" ||
		chat.MarkSeenAction != "mark-seen" || chat.Subscription == nil {
		t.Fatalf("chat contract=%+v", chat)
	}
	messageEvent := chat.Subscription.Events["message"]
	streamEvent := chat.Subscription.Events["stream"]
	if chat.Subscription.CursorQuery != "since" ||
		messageEvent.Operation != "upsert" || messageEvent.Source != "messages" || messageEvent.Value != "$" || messageEvent.ID != "$.id" ||
		streamEvent.Operation != "set_activity" || streamEvent.Source != "messages" || streamEvent.Value != "$.text" {
		t.Fatalf("subscription contract=%+v", chat.Subscription)
	}

	conversations := surface.DataSources["conversations"]
	messages := surface.DataSources["messages"]
	agents := surface.DataSources["agents"]
	if conversations.Request.Path != "/chats" || conversations.Request.Query["page"] != float64(1) ||
		conversations.Response.Items != "$.conversations" || conversations.Pagination == nil || conversations.Pagination.RequestKey != "cursor" {
		t.Fatalf("conversations source=%+v", conversations)
	}
	if messages.Request.Path != "/messages" || messages.Request.Query["page"] != float64(1) ||
		messages.Request.Query["chat_id"] != "$state.conversation_id" || messages.Response.Items != "$.messages" ||
		messages.Pagination == nil || messages.Pagination.RequestKey != "before" {
		t.Fatalf("messages source=%+v", messages)
	}
	if agents.Request.Path != "/agents" || agents.Request.Method != http.MethodGet {
		t.Fatalf("agents source=%+v", agents)
	}
	for name, expected := range map[string]struct{ method, path string }{
		"create-conversation": {http.MethodPost, "/chats"},
		"send-message":        {http.MethodPost, "/messages"},
		"mark-seen":           {http.MethodPost, "/seen"},
	} {
		action := surface.Actions[name]
		if action.Request == nil || action.Request.Method != expected.method || action.Request.Path != expected.path {
			t.Fatalf("action %s=%+v", name, action)
		}
	}
}

func TestReleaseVersionArtifactsAgree(t *testing.T) {
	const releaseVersion = "0.24.0"
	manifest := (&App{}).Manifest()
	if manifest.Version != releaseVersion {
		t.Fatalf("manifest version=%q want=%q", manifest.Version, releaseVersion)
	}
	if manifest.Runtime.Source == nil || manifest.Runtime.Source.Ref != "conversations/v"+releaseVersion {
		t.Fatalf("runtime source=%+v; release installs must use their immutable tag", manifest.Runtime.Source)
	}

	for _, path := range []string{"frontend/package.json", "ui/frontend.json"} {
		document, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var artifact struct {
			Version string `json:"version"`
		}
		if err := json.Unmarshal(document, &artifact); err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		if artifact.Version != releaseVersion {
			t.Fatalf("%s version=%q want=%q", path, artifact.Version, releaseVersion)
		}
	}
	module, err := os.ReadFile("go.mod")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(module), "github.com/apteva/app-sdk v0.85.0") {
		t.Fatal("go.mod must pin app-sdk v0.85.0 so chat/v1 and global dashboard scopes are both available")
	}
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
