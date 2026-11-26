package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func (f *globalOverviewPlatform) ListAgents(projectID string) ([]sdk.PlatformAgent, error) {
	switch projectID {
	case "project-a":
		return []sdk.PlatformAgent{{ID: 7, Name: "Alpha writer"}}, nil
	case "project-b":
		return []sdk.PlatformAgent{{ID: 7, Name: "Beta writer"}}, nil
	}
	return nil, nil
}

func globalGET(t *testing.T, a *App, path string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("GET", path, nil)
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	mux := http.NewServeMux()
	for _, route := range a.HTTPRoutes() {
		mux.HandleFunc(route.Pattern, route.Handler)
	}
	mux.ServeHTTP(w, r)
	return w
}

func TestGlobalOverviewRouteChecksTheViewer(t *testing.T) {
	a, _ := globalOverviewSetup(t)
	user := map[string]string{"X-User-ID": "1"}

	w := globalGET(t, a, "/processes/overview?scope=global", user)
	if w.Code != 200 {
		t.Fatalf("all projects: %d %s", w.Code, w.Body.String())
	}
	var out processOverview
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Scope != "global" || len(out.Projects) != 2 || len(out.Active) != 1 || len(out.Upcoming) != 1 {
		t.Fatalf("all projects payload: %+v", out)
	}
	if x := out.Active[0]; x.ProjectName != "Alpha" || x.AgentName != "Alpha writer" {
		t.Fatalf("active row lost its project or agent name: %+v", x)
	}
	if x := out.Upcoming[0]; x.ProjectName != "Beta" || x.AgentName != "Beta writer" {
		t.Fatalf("agent names must come from the row's own project: %+v", x)
	}
	for _, x := range out.Assignments {
		if x.ProjectName == "" || x.AgentName == "" {
			t.Fatalf("assignment row lacks project or agent name: %+v", x)
		}
	}

	w = globalGET(t, a, "/processes/overview?scope=global&project_id=project-b", user)
	if err := json.Unmarshal(w.Body.Bytes(), &out); w.Code != 200 || err != nil || len(out.Active) != 0 || len(out.Upcoming) != 1 {
		t.Fatalf("one project: %d %s", w.Code, w.Body.String())
	}

	for _, tc := range []struct {
		name    string
		path    string
		headers map[string]string
		want    int
	}{
		{"hidden project", "/processes/overview?scope=global&project_id=project-secret", user, 403},
		{"no user", "/processes/overview?scope=global", nil, 401},
		{"no user, pre-0.25.2 widget", "/processes/overview", nil, 401},
		{"delegated user", "/processes/overview?scope=global", map[string]string{"X-User-ID": "1", "X-Apteva-Issuer-App": "portal", "X-Apteva-Issuer-Install-ID": "9"}, 403},
		{"bad scope", "/processes/overview?scope=other", user, 400},
	} {
		if w := globalGET(t, a, tc.path, tc.headers); w.Code != tc.want {
			t.Fatalf("%s: want %d, got %d %s", tc.name, tc.want, w.Code, w.Body.String())
		}
	}
}

func TestProjectInstallRefusesGlobalScope(t *testing.T) {
	a, _, _, _ := workflowSetup(t)
	w := globalGET(t, a, "/processes/overview?scope=global", map[string]string{"X-User-ID": "1", "X-Apteva-Project-ID": "project-a"})
	if w.Code != 400 {
		t.Fatalf("project install served all projects: %d %s", w.Code, w.Body.String())
	}
	if w := overviewGET(t, a, "/processes/overview?project_id=project-a", "project-a"); w.Code != 200 {
		t.Fatalf("project path changed: %d %s", w.Code, w.Body.String())
	}
}
