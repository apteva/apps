package main

// All projects Home support. A global widget shows one view across every
// project the signed-in viewer can access. The project list always comes from
// the platform for that viewer (WithUserSession), never from the request; a
// selected project must be in that list.

import (
	"encoding/json"
	"net/http"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// globalMetricProject holds calculated metrics shared by global widgets.
const globalMetricProject = "__global__"

// noVisibleProject keeps a query scoped when the viewer can see no project: an
// empty ProjectIDs list would otherwise mean "no project condition".
const noVisibleProject = "\x00no-visible-project"

type globalProject struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// delegatedRequest reports requests made for another app's users or by an
// agent or sibling app. The platform strips these headers from browsers.
func delegatedRequest(r *http.Request) bool {
	for _, h := range []string{"X-Apteva-Issuer-App", "X-Apteva-Issuer-Install-ID", "X-Apteva-Bound-Caller-Install-ID", "X-Apteva-Caller-Agent"} {
		if strings.TrimSpace(r.Header.Get(h)) != "" {
			return true
		}
	}
	return false
}

// globalViewerProjects returns the projects visible to the viewer, or writes
// an error response and returns ok=false.
func globalViewerProjects(w http.ResponseWriter, r *http.Request) ([]globalProject, bool) {
	if delegatedRequest(r) {
		http.Error(w, "All projects is unavailable to delegated users", http.StatusForbidden)
		return nil, false
	}
	if !requireUser(r) {
		http.Error(w, "authenticated user required", http.StatusUnauthorized)
		return nil, false
	}
	if globalCtx == nil {
		http.Error(w, "platform unavailable", http.StatusServiceUnavailable)
		return nil, false
	}
	api := globalCtx.WithUserSession(r).PlatformAPI()
	if api == nil {
		http.Error(w, "platform unavailable", http.StatusServiceUnavailable)
		return nil, false
	}
	projects, err := api.ListProjects()
	if err != nil {
		http.Error(w, "unable to list projects", http.StatusInternalServerError)
		return nil, false
	}
	return uniqueProjects(projects), true
}

func uniqueProjects(projects []sdk.PlatformProject) []globalProject {
	out := make([]globalProject, 0, len(projects))
	seen := map[string]bool{}
	for _, p := range projects {
		id := strings.TrimSpace(p.ID)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, globalProject{ID: id, Name: p.Name})
	}
	return out
}

// globalProjectIDs narrows the visible projects to an optional selection.
// It never returns an empty list (see noVisibleProject).
func globalProjectIDs(w http.ResponseWriter, projects []globalProject, selected string) ([]string, bool) {
	selected = strings.TrimSpace(selected)
	ids := make([]string, 0, len(projects))
	for _, p := range projects {
		if selected == "" || p.ID == selected {
			ids = append(ids, p.ID)
		}
	}
	if selected != "" && len(ids) == 0 {
		http.Error(w, "project is not visible to this user", http.StatusForbidden)
		return nil, false
	}
	if len(ids) == 0 {
		ids = []string{noVisibleProject}
	}
	return ids, true
}

// POST /query-widget?scope=global — the standalone Trend and Ranking widgets
// on the All projects Home. Same body and result as the project path, plus
// the viewer's projects for the widget's project filter.
func (a *App) handleGlobalWidgetQuery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	projects, ok := globalViewerProjects(w, r)
	if !ok {
		return
	}
	var body struct {
		ProjectID string          `json:"project_id"`
		Widget    DashboardWidget `json:"widget"`
		Filters   map[string]any  `json:"filters"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128*1024)).Decode(&body); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	ids, ok := globalProjectIDs(w, projects, body.ProjectID)
	if !ok {
		return
	}
	result, err := evaluateWidgetAcrossProjects(requestReadDB(r), ids, body.Widget, body.Filters)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	result["projects"] = projects
	writeJSON(w, result)
}
