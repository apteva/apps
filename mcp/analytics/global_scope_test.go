package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

// viewerPlatform returns the projects the platform would list for the viewer.
type viewerPlatform struct {
	tk.BasePlatformClient
	projects []sdk.PlatformProject
}

func (p *viewerPlatform) ListProjects() ([]sdk.PlatformProject, error) { return p.projects, nil }

func globalFixture(t *testing.T, projects ...sdk.PlatformProject) *viewerPlatform {
	t.Helper()
	platform := &viewerPlatform{projects: projects}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithPlatform(platform))
	old := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = old })
	for _, ev := range []struct{ project, country string }{
		{"p1", "FR"}, {"p1", "FR"}, {"p2", "DE"}, {"p2", "FR"}, {"hidden", "US"}, {"hidden", "US"}, {"hidden", "US"},
	} {
		if _, err := insertEvent(ctx.AppDB(), EventInsert{ProjectID: ev.project, App: "site", Topic: "page_view", Source: "web", Props: `{"country":"` + ev.country + `"}`}); err != nil {
			t.Fatal(err)
		}
	}
	return platform
}

const countryRanking = `{"widget":{"type":"top","config":{"app":"site","topic":"page_view","aggregation":"count","window":"all","by":"props.country","limit":5,"where":{}}}%s}`

func widgetQuery(t *testing.T, target, body string, headers map[string]string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	rr := httptest.NewRecorder()
	(&App{}).handleWidgetQuery(rr, r)
	var out map[string]any
	if rr.Code == http.StatusOK {
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatal(err, rr.Body.String())
		}
	}
	return rr, out
}

func topCounts(out map[string]any) map[string]float64 {
	counts := map[string]float64{}
	rows, _ := out["top"].([]any)
	for _, row := range rows {
		m := row.(map[string]any)
		counts[m["value"].(string)] = m["count"].(float64)
	}
	return counts
}

var viewer = map[string]string{"X-User-ID": "1"}

func TestGlobalWidgetQueryCoversOnlyViewerProjects(t *testing.T) {
	globalFixture(t, sdk.PlatformProject{ID: "p1", Name: "Alpha"}, sdk.PlatformProject{ID: "p2", Name: "Beta"})

	rr, out := widgetQuery(t, "/query-widget?scope=global", strings.Replace(countryRanking, "%s", "", 1), viewer)
	if rr.Code != http.StatusOK {
		t.Fatalf("all projects: %d %s", rr.Code, rr.Body.String())
	}
	if got := topCounts(out); got["FR"] != 3 || got["DE"] != 1 || got["US"] != 0 {
		t.Fatalf("all projects counts=%v, want FR 3, DE 1 and nothing from the hidden project", got)
	}
	projects, _ := out["projects"].([]any)
	if len(projects) != 2 || projects[0].(map[string]any)["name"] != "Alpha" {
		t.Fatalf("projects for the filter=%v", out["projects"])
	}

	rr, out = widgetQuery(t, "/query-widget?scope=global", strings.Replace(countryRanking, "%s", `,"project_id":"p2"`, 1), viewer)
	if got := topCounts(out); rr.Code != http.StatusOK || got["FR"] != 1 || got["DE"] != 1 {
		t.Fatalf("one project: %d counts=%v", rr.Code, got)
	}

	rr, _ = widgetQuery(t, "/query-widget?scope=global", strings.Replace(countryRanking, "%s", `,"project_id":"hidden"`, 1), viewer)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("hidden project: %d, want 403", rr.Code)
	}
}

func TestGlobalWidgetQueryRefusesAnonymousDelegatedAndBadScope(t *testing.T) {
	globalFixture(t, sdk.PlatformProject{ID: "p1", Name: "Alpha"})
	body := strings.Replace(countryRanking, "%s", "", 1)
	if rr, _ := widgetQuery(t, "/query-widget?scope=global", body, nil); rr.Code != http.StatusUnauthorized {
		t.Fatalf("no user: %d, want 401", rr.Code)
	}
	for _, h := range []string{"X-Apteva-Issuer-App", "X-Apteva-Issuer-Install-ID", "X-Apteva-Bound-Caller-Install-ID", "X-Apteva-Caller-Agent"} {
		if rr, _ := widgetQuery(t, "/query-widget?scope=global", body, map[string]string{"X-User-ID": "1", h: "x"}); rr.Code != http.StatusForbidden {
			t.Fatalf("delegated via %s: %d, want 403", h, rr.Code)
		}
	}
	if rr, _ := widgetQuery(t, "/query-widget?scope=other&project_id=p1", body, viewer); rr.Code != http.StatusBadRequest {
		t.Fatalf("bad scope: %d, want 400", rr.Code)
	}
}

// A viewer with no projects must see nothing, not every project's events.
func TestGlobalWidgetQueryWithNoVisibleProjectsIsEmpty(t *testing.T) {
	globalFixture(t)
	rr, out := widgetQuery(t, "/query-widget?scope=global", strings.Replace(countryRanking, "%s", "", 1), viewer)
	if rr.Code != http.StatusOK || len(topCounts(out)) != 0 {
		t.Fatalf("no projects: %d counts=%v", rr.Code, topCounts(out))
	}
}

func TestProjectWidgetQueryIsUnchanged(t *testing.T) {
	globalFixture(t, sdk.PlatformProject{ID: "p1", Name: "Alpha"}, sdk.PlatformProject{ID: "p2", Name: "Beta"})
	rr, out := widgetQuery(t, "/query-widget?project_id=p1", strings.Replace(countryRanking, "%s", "", 1), map[string]string{"X-User-ID": "1", trustedProjectHeader: "p1"})
	if rr.Code != http.StatusOK {
		t.Fatalf("project path: %d %s", rr.Code, rr.Body.String())
	}
	if got := topCounts(out); got["FR"] != 2 || len(got) != 1 {
		t.Fatalf("project counts=%v, want only p1", got)
	}
	if _, ok := out["projects"]; ok {
		t.Fatal("project path grew a projects list")
	}
}

// The existing global routes now list projects for the viewer, too.
func TestGlobalSummaryUsesViewerProjects(t *testing.T) {
	globalFixture(t, sdk.PlatformProject{ID: "p1", Name: "Alpha"})
	get := func(target string, headers map[string]string) (*httptest.ResponseRecorder, map[string]any) {
		r := httptest.NewRequest(http.MethodGet, target, nil)
		for k, v := range headers {
			r.Header.Set(k, v)
		}
		rr := httptest.NewRecorder()
		(&App{}).handleGlobalSummary(rr, r)
		var out map[string]any
		_ = json.Unmarshal(rr.Body.Bytes(), &out)
		return rr, out
	}
	rr, out := get("/global-summary", viewer)
	if rr.Code != http.StatusOK || out["total"] != float64(2) {
		t.Fatalf("summary: %d total=%v, want 2 events from p1 only", rr.Code, out["total"])
	}
	if rr, _ := get("/global-summary?project_id=hidden", viewer); rr.Code != http.StatusForbidden {
		t.Fatalf("hidden: %d", rr.Code)
	}
	if rr, _ := get("/global-summary", nil); rr.Code != http.StatusUnauthorized {
		t.Fatalf("no user: %d", rr.Code)
	}
	if rr, _ := get("/global-summary", map[string]string{"X-User-ID": "1", "X-Apteva-Caller-Agent": "7"}); rr.Code != http.StatusForbidden {
		t.Fatalf("delegated: %d", rr.Code)
	}
}
