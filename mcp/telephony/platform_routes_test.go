package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

// ListAgents lets the shared test platform serve the panel's agent picker.
func (p *answerPlatform) ListAgents(projectID string) ([]sdk.PlatformAgent, error) {
	out := []sdk.PlatformAgent{}
	for _, agent := range p.agents {
		if projectID == "" || agent.ProjectID == "" || agent.ProjectID == projectID {
			out = append(out, *agent)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func platformRouteTestApp(t *testing.T) (*App, *sdk.AppCtx) {
	t.Helper()
	platform := &answerPlatform{
		bindings: map[string]any{"carrier": int64(9)},
		credentials: &sdk.ConnectionCredentials{
			Slug:   "twilio",
			Fields: map[string]string{"auth_token": "test-auth-token", "phone_number": "+14155550101"},
		},
		integrationResponse: map[string]json.RawMessage{
			"update_phone_number": json.RawMessage(`{"sid":"PN1"}`),
		},
		agents: map[int64]*sdk.PlatformAgent{
			5: {ID: 5, Name: "Support", ProjectID: "project-a"},
			6: {ID: 6, Name: "Elsewhere", ProjectID: "project-b"},
		},
	}
	a, ctx := withTelephonyTestContext(t, platform)
	return a, ctx
}

// toolError extracts the message from an MCP error result, or "" on success.
func toolError(result any) string {
	m, ok := result.(map[string]any)
	if !ok {
		return ""
	}
	if msg, ok := m["error"].(string); ok {
		return msg
	}
	if isErr, _ := m["isError"].(bool); isErr {
		b, _ := json.Marshal(m["content"])
		return string(b)
	}
	return ""
}

func createdRoute(t *testing.T, result any) map[string]any {
	t.Helper()
	if msg := toolError(result); msg != "" {
		t.Fatalf("route creation failed: %s", msg)
	}
	route, _ := result.(map[string]any)["route"].(map[string]any)
	if route == nil || route["id"] == "" {
		t.Fatalf("unexpected create result: %#v", result)
	}
	return route
}

func TestPlatformCallerManagesRoutesForNamedAgent(t *testing.T) {
	a, ctx := platformRouteTestApp(t)
	platformCaller := context.Background() // no agent, no delegated subject

	result, err := a.toolRoutesCreate(platformCaller, ctx, map[string]any{
		"phone_number": "+14155550102", "phone_number_id": "PN2",
		"agent_id": float64(5), "answer_mode": "human_browser",
	})
	if err != nil {
		t.Fatal(err)
	}
	route := createdRoute(t, result)
	if route["agent_id"] != int64(5) || route["answer_mode"] != "human_browser" {
		t.Fatalf("route not bound to the named agent: %#v", route)
	}
	routeID := route["id"].(string)

	listed, _ := a.toolRoutesList(platformCaller, ctx, nil)
	if msg := toolError(listed); msg != "" {
		t.Fatalf("platform caller cannot list routes: %s", msg)
	}
	if routes := listed.(map[string]any)["routes"].([]map[string]any); len(routes) != 1 || routes[0]["id"] != routeID {
		t.Fatalf("platform caller should see the project route: %#v", routes)
	}

	updated, _ := a.toolRoutesSetAnswerMode(platformCaller, ctx, map[string]any{"route_id": routeID, "answer_mode": "agent"})
	if msg := toolError(updated); msg != "" {
		t.Fatalf("platform caller cannot update a project route: %s", msg)
	}
	policy, _ := a.toolRouteRecordingPolicy(platformCaller, ctx, map[string]any{"route_id": routeID, "recording_mode": "off"})
	if msg := toolError(policy); msg != "" {
		t.Fatalf("platform caller cannot set the recording policy: %s", msg)
	}

	disabled, _ := a.toolRoutesDisable(platformCaller, ctx, map[string]any{"route_id": routeID})
	if msg := toolError(disabled); msg != "" {
		t.Fatalf("platform caller cannot disable a project route: %s", msg)
	}
	stored, _ := a.db().findRoute(routeID)
	if stored == nil || stored.Enabled {
		t.Fatalf("route was not disabled: %+v", stored)
	}
}

func TestPlatformCallerMustNameAValidProjectAgent(t *testing.T) {
	a, ctx := platformRouteTestApp(t)
	platformCaller := context.Background()

	result, _ := a.toolRoutesCreate(platformCaller, ctx, map[string]any{"phone_number": "+14155550102"})
	if msg := toolError(result); !strings.Contains(msg, "agent_id required") {
		t.Fatalf("missing agent_id accepted: %#v", result)
	}
	result, _ = a.toolRoutesCreate(platformCaller, ctx, map[string]any{"phone_number": "+14155550102", "agent_id": float64(6)})
	if msg := toolError(result); !strings.Contains(msg, "another project") {
		t.Fatalf("agent from another project accepted: %#v", result)
	}
	result, _ = a.toolRoutesCreate(platformCaller, ctx, map[string]any{"phone_number": "+14155550102", "agent_id": "99"})
	if msg := toolError(result); !strings.Contains(msg, "could not be verified") {
		t.Fatalf("unknown agent accepted: %#v", result)
	}
	if routes, _ := a.db().listRoutesForProject("project-a"); len(routes) != 0 {
		t.Fatalf("refused creations persisted routes: %#v", routes)
	}
}

func TestAgentCallersKeepExclusiveOwnership(t *testing.T) {
	a, ctx := platformRouteTestApp(t)
	agent7 := sdk.WithCaller(context.Background(), &sdk.Caller{AgentID: 7})
	agent8 := sdk.WithCaller(context.Background(), &sdk.Caller{AgentID: 8})

	spoofed, _ := a.toolRoutesCreate(agent7, ctx, map[string]any{"phone_number": "+14155550102", "agent_id": float64(5)})
	if msg := toolError(spoofed); !strings.Contains(msg, "omit agent_id") {
		t.Fatalf("agent could create a route for another agent: %#v", spoofed)
	}

	result, _ := a.toolRoutesCreate(agent7, ctx, map[string]any{"phone_number": "+14155550102", "phone_number_id": "PN2"})
	route := createdRoute(t, result)
	if route["agent_id"] != int64(7) {
		t.Fatalf("agent caller must own its route: %#v", route)
	}
	routeID := route["id"].(string)

	foreign, _ := a.toolRoutesSetAnswerMode(agent8, ctx, map[string]any{"route_id": routeID, "answer_mode": "human_browser"})
	if msg := toolError(foreign); !strings.Contains(msg, "another agent") {
		t.Fatalf("another agent could edit the route: %#v", foreign)
	}
	foreign, _ = a.toolRoutesDisable(agent8, ctx, map[string]any{"route_id": routeID})
	if msg := toolError(foreign); !strings.Contains(msg, "another agent") {
		t.Fatalf("another agent could disable the route: %#v", foreign)
	}
	if listed, _ := a.toolRoutesList(agent8, ctx, nil); len(listed.(map[string]any)["routes"].([]map[string]any)) != 0 {
		t.Fatalf("another agent can see the route: %#v", listed)
	}
	own, _ := a.toolRoutesSetAnswerMode(agent7, ctx, map[string]any{"route_id": routeID, "answer_mode": "human_browser"})
	if msg := toolError(own); msg != "" {
		t.Fatalf("owner cannot edit its own route: %s", msg)
	}
}

func numbersRequest(t *testing.T, a *App, path string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	encoded, _ := json.Marshal(body)
	r := httptest.NewRequest("POST", path, bytes.NewReader(encoded))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	a.handleNumbers(w, r)
	return w
}

func TestPanelRouteEndpointsCreateListAgentsAndDisable(t *testing.T) {
	a, _ := platformRouteTestApp(t)

	w := numbersRequest(t, a, "/numbers/agents", map[string]any{})
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"name":"Support"`) || strings.Contains(w.Body.String(), "Elsewhere") {
		t.Fatalf("agent directory = %d %s", w.Code, w.Body.String())
	}

	w = numbersRequest(t, a, "/numbers/routes/create", map[string]any{"phone_number": "+14155550103", "phone_number_id": "PN3", "configure": false})
	if w.Code != 400 || !strings.Contains(w.Body.String(), "agent_id required") {
		t.Fatalf("missing agent accepted: %d %s", w.Code, w.Body.String())
	}

	w = numbersRequest(t, a, "/numbers/routes/create", map[string]any{
		"phone_number": "+14155550103", "phone_number_id": "PN3", "agent_id": 5, "answer_mode": "human_browser", "configure": false,
	})
	if w.Code != 200 {
		t.Fatalf("create = %d %s", w.Code, w.Body.String())
	}
	var created struct {
		OK    bool `json:"ok"`
		Route struct {
			ID      string `json:"id"`
			AgentID int64  `json:"agent_id"`
			Enabled bool   `json:"enabled"`
		} `json:"route"`
		CarrierConfigured bool `json:"carrier_configured"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil || !created.OK || created.Route.AgentID != 5 || !created.Route.Enabled || created.CarrierConfigured {
		t.Fatalf("unexpected create payload: %s (err=%v)", w.Body.String(), err)
	}

	w = numbersRequest(t, a, "/numbers/routes/disable", map[string]any{"route_id": created.Route.ID})
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"ok":true`) {
		t.Fatalf("disable = %d %s", w.Code, w.Body.String())
	}
	stored, _ := a.db().findRoute(created.Route.ID)
	if stored == nil || stored.Enabled {
		t.Fatalf("route still enabled after panel disable: %+v", stored)
	}
}

func TestDelegatedApplicationUsersCannotManageRoutes(t *testing.T) {
	a, _ := platformRouteTestApp(t)
	identity := phoneTestIdentity("alice")
	for _, path := range []string{"/numbers/routes/create", "/numbers/routes/disable", "/numbers/agents"} {
		encoded, _ := json.Marshal(map[string]any{"phone_number": "+14155550103", "agent_id": 5, "route_id": "route-x"})
		r := httptest.NewRequest("POST", path, bytes.NewReader(encoded))
		r.Header.Set("X-Apteva-Issuer-App", identity.IssuerApp)
		r.Header.Set("X-Apteva-Issuer-Install-ID", identity.IssuerInstallID)
		r.Header.Set("X-Apteva-Subject-Type", "user")
		r.Header.Set("X-Apteva-Subject-ID", identity.SubjectID)
		r.Header.Set("X-Apteva-Project-ID", "project-a")
		r.Header.Set("X-Apteva-Organization-ID", identity.OrganizationID)
		r.Header.Set("X-Apteva-Scopes", `[{"type":"app_user","app":"telephony","actions":["*"]}]`)
		w := httptest.NewRecorder()
		a.applicationUserHTTP(a.handleNumbers)(w, r)
		if w.Code != 403 {
			t.Fatalf("%s: delegated application user reached route management: %d %s", path, w.Code, w.Body.String())
		}
	}
	if routes, _ := a.db().listRoutesForProject("project-a"); len(routes) != 0 {
		t.Fatalf("delegated user created routes: %#v", routes)
	}
}
