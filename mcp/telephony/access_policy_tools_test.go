package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

// callAccessTool calls a registered access tool through the same wrappers MCP
// uses.
func callAccessTool(t *testing.T, app *App, ctx *sdk.AppCtx, caller *sdk.Caller, name string, args map[string]any) (any, error) {
	t.Helper()
	for _, tool := range app.MCPTools() {
		if tool.Name == name {
			c := context.Background()
			if caller != nil {
				c = sdk.WithCaller(c, caller)
			}
			return tool.HandlerCtx(c, ctx, args)
		}
	}
	t.Fatalf("tool %s not registered", name)
	return nil, nil
}

// toolArgs turns a JSON literal into tool arguments, numbers as float64 the
// way MCP delivers them.
func toolArgs(t *testing.T, raw string) map[string]any {
	t.Helper()
	var args map[string]any
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		t.Fatal(err)
	}
	return args
}

const accessToolPolicy = `{"revision": %REV%,
 "providers": [{"id": "flexylead-auth", "issuer_app": "auth", "issuer_install_id": 11, "format": "apteva-auth", "actions": ["call.read", "call.answer", "call.attach", "call.hangup"]}],
 "users": [{"identity": {"issuer_app": "auth", "issuer_install_id": 11, "subject_type": "user", "subject_id": 42, "organization_id": 7},
            "enabled": true, "role": "user", "destinations": ["adviser-42"]}]}`

func accessToolArgs(t *testing.T, revision string) map[string]any {
	return toolArgs(t, strings.ReplaceAll(accessToolPolicy, "%REV%", revision))
}

// A platform caller sets the policy the way an Environments seed would: the
// Auth install, user and organization ids arrive as numbers ($runtime_install_id
// and $ref give numbers) and the provider has no URL. The
// result is what PUT /access/policy would store, and it applies at once.
func TestAccessPolicyToolsSetWhatTheOperatorRouteSets(t *testing.T) {
	ctx := softphoneTestCtx(t)
	t.Setenv("APTEVA_GATEWAY_URL", "http://127.0.0.1:5280/")
	app := &App{installID: 42}
	adviser := phoneIdentity{"auth", "11", "user", "42", "7"}

	got, err := callAccessTool(t, app, ctx, nil, "telephony_access_policy_get", nil)
	if err != nil || got.(phonePolicy).Revision != 0 {
		t.Fatalf("empty policy = %#v %v", got, err)
	}

	// Grants name destinations that must already exist.
	if _, err := callAccessTool(t, app, ctx, nil, "telephony_access_policy_set", accessToolArgs(t, "0")); err == nil || !strings.Contains(err.Error(), "enabled destination") {
		t.Fatalf("grant to a missing destination accepted: %v", err)
	}
	if _, err := app.saveRoutingDestination("project-a", "adviser-42", "Adviser 42", "browser", map[string]any{}, true); err != nil {
		t.Fatal(err)
	}

	set, err := callAccessTool(t, app, ctx, nil, "telephony_access_policy_set", accessToolArgs(t, "0"))
	if err != nil {
		t.Fatal(err)
	}
	saved := set.(phonePolicy)
	if saved.Revision != 1 || len(saved.Providers) != 1 || len(saved.Users) != 1 {
		t.Fatalf("saved = %#v", saved)
	}
	const wantURL = "http://127.0.0.1:5280/api/apps/auth/_install/11/me?project_id=project-a"
	if saved.Providers[0].URL != wantURL || saved.Providers[0].IssuerInstallID != "11" || saved.Users[0].Identity != adviser {
		t.Fatalf("provider %#v user %#v", saved.Providers[0], saved.Users[0].Identity)
	}

	// The operator route reads the same stored policy.
	w := phoneTestRequest(app, nil, "GET", "/access/policy", nil)
	var stored phonePolicy
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &stored) != nil || stored.Revision != 1 || stored.Providers[0].URL != wantURL {
		t.Fatalf("route GET = %d %s", w.Code, w.Body)
	}
	read, err := callAccessTool(t, app, ctx, nil, "telephony_access_policy_get", nil)
	if err != nil || read.(phonePolicy).Revision != 1 || read.(phonePolicy).Users[0].Identity != adviser {
		t.Fatalf("tool GET = %#v %v", read, err)
	}
	principal, err := app.phonePrincipal("project-a", adviser)
	if err != nil || !principal.Destinations["adviser-42"] {
		t.Fatalf("adviser access = %#v %v", principal, err)
	}

	// A stale revision is refused, as the route answers 409.
	if _, err := callAccessTool(t, app, ctx, nil, "telephony_access_policy_set", accessToolArgs(t, "0")); err == nil || err.Error() != "policy revision changed" {
		t.Fatalf("stale write = %v", err)
	}
	if w := phoneTestRequest(app, nil, "PUT", "/access/policy", phonePolicy{}); w.Code != 409 {
		t.Fatalf("route stale write = %d %s", w.Code, w.Body)
	}

	// Removing the adviser at the current revision applies at once.
	if _, err := callAccessTool(t, app, ctx, nil, "telephony_access_policy_set", toolArgs(t, `{"revision": 1, "users": []}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := app.phonePrincipal("project-a", adviser); !errors.Is(err, errPhoneAccessDenied) {
		t.Fatalf("removed adviser kept access: %v", err)
	}
}

// The write replaces the whole policy, so a misspelled field or a missing
// revision is refused instead of removing users.
func TestAccessPolicySetRefusesUnclearInput(t *testing.T) {
	ctx := softphoneTestCtx(t)
	t.Setenv("APTEVA_GATEWAY_URL", "http://127.0.0.1:5280")
	app := &App{installID: 42}
	for _, tc := range []struct{ args, want string }{
		{`{"users": []}`, "revision required"},
		{`{"revision": 0, "user": []}`, "unknown field"},
		{`{"revision": 0, "users": [{"identity": {"issuer_app": "auth", "issuer_install_id": 11, "subject_type": "user", "subject_id": "42"}, "enabled": true, "role": "user", "destinatons": []}]}`, "unknown field"},
		{`{"revision": 0, "providers": [{"id": "p", "issuer_app": "../x", "issuer_install_id": 11, "format": "apteva-auth"}]}`, "provider url required"},
		{`{"revision": 0, "providers": [{"id": "p", "issuer_app": "auth", "issuer_install_id": 1.5, "format": "apteva-auth"}]}`, "invalid policy"},
		{`{"revision": 0, "users": [{"identity": {"issuer_app": "auth", "issuer_install_id": 11, "subject_type": "user", "subject_id": 1.5}, "enabled": true, "role": "user"}]}`, "invalid policy"},
		{`{"revision": 0, "providers": [{"id": "p", "issuer_app": "auth", "issuer_install_id": 11, "format": "userinfo"}]}`, "identity provider"},
	} {
		if _, err := callAccessTool(t, app, ctx, nil, "telephony_access_policy_set", toolArgs(t, tc.args)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: err = %v, want %q", tc.args, err, tc.want)
		}
	}
	// Reserved platform arguments are not policy fields.
	if _, err := callAccessTool(t, app, ctx, nil, "telephony_access_policy_set", toolArgs(t, `{"revision": 0, "_project_id": "project-a"}`)); err != nil {
		t.Fatalf("reserved argument refused: %v", err)
	}

	t.Setenv("APTEVA_GATEWAY_URL", "")
	if _, err := callAccessTool(t, app, ctx, nil, "telephony_access_policy_set", toolArgs(t, `{"revision": 1, "providers": [{"id": "p", "issuer_app": "auth", "issuer_install_id": 11, "format": "apteva-auth"}]}`)); err == nil || !strings.Contains(err.Error(), "no platform gateway") {
		t.Fatalf("provider without gateway = %v", err)
	}
}

// Only platform callers reach the policy: agents could add a provider they
// control and grant themselves call access, bound apps have the scoped
// resource tools, and advisers have the softphone API.
func TestAccessPolicyToolsArePlatformOnly(t *testing.T) {
	ctx := softphoneTestCtx(t)
	app := &App{installID: 42}
	args := func() map[string]any { return toolArgs(t, `{"revision": 0}`) }
	for _, name := range []string{"telephony_access_policy_get", "telephony_access_policy_set"} {
		if _, err := callAccessTool(t, app, ctx, &sdk.Caller{AgentID: 7}, name, args()); err == nil || !strings.Contains(err.Error(), "agents cannot") {
			t.Fatalf("%s: agent allowed: %v", name, err)
		}
		result, err := callAccessTool(t, app, ctx, &sdk.Caller{AppInstallID: 3, AppName: "tests"}, name, args())
		if failure, ok := result.(map[string]any); err != nil || !ok || failure["ok"] != false {
			t.Fatalf("%s: bound app allowed: %#v %v", name, result, err)
		}
		if _, err := callAccessTool(t, app, ctx, &sdk.Caller{SubjectType: "user", SubjectID: "42"}, name, args()); err == nil || !strings.Contains(err.Error(), "application users") {
			t.Fatalf("%s: application user allowed: %v", name, err)
		}
		// The handler refuses them even without the shared wrappers.
		for _, caller := range []*sdk.Caller{{AgentID: 7}, {AppInstallID: 3}, {SubjectID: "42"}} {
			if requirePlatformPolicyCaller(sdk.WithCaller(context.Background(), caller)) == nil {
				t.Fatalf("handler check admits %#v", caller)
			}
		}
	}
	if policy, _ := app.phonePolicy("project-a"); policy.Revision != 0 {
		t.Fatalf("a refused caller wrote revision %d", policy.Revision)
	}

	// Environments seeds are platform calls without a bound app, so the tools
	// must stay on the public surface: app-only tools refuse them.
	for _, tool := range app.MCPTools() {
		if strings.HasPrefix(tool.Name, "telephony_access_policy_") && tool.Exposure == sdk.ToolExposureAppOnly {
			t.Fatalf("%s is app-only", tool.Name)
		}
	}
	if _, err := callAccessTool(t, app, ctx, &sdk.Caller{ProjectID: "project-a"}, "telephony_access_policy_set", args()); err != nil {
		t.Fatalf("platform caller refused: %v", err)
	}
}
