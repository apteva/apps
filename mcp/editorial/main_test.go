package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

func testApp(t *testing.T, p sdk.PlatformClient) (*App, *sdk.AppCtx) {
	t.Helper()
	t.Setenv("APTEVA_PROJECT_ID", "")
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithPlatform(p)).WithProject("alpha")
	a := &App{}
	if e := a.OnMount(ctx); e != nil {
		t.Fatal(e)
	}
	return a, ctx
}
func create(t *testing.T, a *App, c *sdk.AppCtx, args map[string]any) Item {
	t.Helper()
	v, e := a.dispatch(c, "items_create", args)
	if e != nil {
		t.Fatal(e)
	}
	return v.(Item)
}
func TestStandaloneManifestAndPlanning(t *testing.T) {
	a, c := testApp(t, nil)
	m := a.Manifest()
	if len(m.Requires.Apps) != 0 {
		t.Fatal("standalone install must not auto-install apps")
	}
	if len(m.Requires.Integrations) != 2 {
		t.Fatal("expected only two optional integrations")
	}
	for _, d := range m.Requires.Integrations {
		if d.Required {
			t.Fatal("required dependency", d.Role)
		}
	}
	tools := a.MCPTools()
	if len(tools) != len(m.Provides.MCPTools) {
		t.Fatal("manifest/tool mismatch")
	}
	for _, d := range m.Provides.MCPTools {
		found := false
		for _, tool := range tools {
			if tool.Name == d.Name {
				found = true
			}
		}
		if !found {
			t.Fatal(d.Name)
		}
	}
	i := create(t, a, c, map[string]any{"title": "Independent idea"})
	if i.Status != "idea" || i.Revision != 1 {
		t.Fatal(i)
	}
	v, e := a.dispatch(c, "releases_create", map[string]any{"item_id": i.ID, "channel": "Podcast", "planned_at": "2026-10-01"})
	if e != nil {
		t.Fatal(e)
	}
	r := v.(Release)
	if r.App != "" || r.Status != "planned" {
		t.Fatal(r)
	}
	list, e := a.dispatch(c, "items_list", nil)
	if e != nil {
		t.Fatal(e)
	}
	if list.(map[string]any)["total"] != 1 {
		t.Fatal(list)
	}
	if _, e = a.dispatch(c, "releases_refresh", map[string]any{"id": r.ID}); e == nil {
		t.Fatal("manual release unexpectedly refreshed")
	}
	detail, e := itemDetail(c.AppDB(), "alpha", i.ID)
	if e != nil {
		t.Fatal(e)
	}
	if len(detail.(map[string]any)["history"].([]map[string]any)) != 2 {
		t.Fatal("history missing")
	}
}
func TestProjectIsolationAndHTTPPrecedence(t *testing.T) {
	a, c := testApp(t, nil)
	i := create(t, a, c, map[string]any{"title": "Private", "project_id": "beta", "_project_id": "beta"})
	beta := c.WithProject("beta")
	if _, e := a.dispatch(beta, "items_get", map[string]any{"id": i.ID}); !errors.Is(e, sql.ErrNoRows) {
		t.Fatal(e)
	}
	if _, e := a.dispatch(beta, "items_update", map[string]any{"id": i.ID, "revision": i.Revision, "patch": map[string]any{"title": "stolen"}}); !errors.Is(e, sql.ErrNoRows) {
		t.Fatal(e)
	}
	if _, e := a.dispatch(beta, "releases_create", map[string]any{"item_id": i.ID, "channel": "Email"}); !errors.Is(e, sql.ErrNoRows) {
		t.Fatal(e)
	}
	if _, e := a.dispatch(c.WithProject(""), "items_create", map[string]any{"title": "No scope", "project_id": "alpha"}); e == nil {
		t.Fatal("accepted untrusted scope")
	}
	req := httptest.NewRequest("GET", fmt.Sprintf("/items/%d?project_id=beta", i.ID), nil)
	res := httptest.NewRecorder()
	a.http(res, req)
	if res.Code != 200 {
		t.Fatal("pinned context lost", res.Body.String())
	}
	a.ctx = c.WithProject("")
	req = httptest.NewRequest("GET", fmt.Sprintf("/items/%d?project_id=beta", i.ID), nil)
	req.Header.Set("X-Apteva-Project-Id", "alpha")
	res = httptest.NewRecorder()
	a.http(res, req)
	if res.Code != 200 {
		t.Fatal("trusted header lost", res.Body.String())
	}
}
func TestApprovalRevisionAndClearingFields(t *testing.T) {
	a, c := testApp(t, nil)
	i := create(t, a, c, map[string]any{"title": "Reviewed", "approval": "approved", "reviewer": "Alex", "fields": map[string]any{"audience": "founders"}, "sources": []string{"https://example.com"}})
	v, e := a.dispatch(c, "items_update", map[string]any{"id": i.ID, "revision": i.Revision, "patch": map[string]any{"fields": map[string]any{}, "sources": []string{}, "approval": "approved"}})
	if e != nil {
		t.Fatal(e)
	}
	updated := v.(Item)
	if updated.Approval != "pending" || len(updated.Fields) != 0 || len(updated.Sources) != 0 {
		t.Fatal(updated)
	}
	if _, e = a.dispatch(c, "items_update", map[string]any{"id": i.ID, "revision": i.Revision, "patch": map[string]any{"title": "Stale"}}); !errors.Is(e, errConflict) {
		t.Fatal(e)
	}
	var n int
	c.AppDB().QueryRow("SELECT count(*) FROM editorial_history").Scan(&n)
	if n != 2 {
		t.Fatal("failed update wrote history", n)
	}
}
func TestValidationAndPagination(t *testing.T) {
	a, c := testApp(t, nil)
	for _, patch := range []map[string]any{{"title": ""}, {"title": "bad", "planned_at": "tomorrow"}, {"title": "bad", "sources": []string{"javascript:alert(1)"}}, {"title": "bad", "approval": "approved"}, {"title": "bad", "status": "invented"}, {"title": "bad", "unknown": 1}, {"title": "bad", "body": nil}} {
		if _, e := a.dispatch(c, "items_create", patch); e == nil {
			t.Fatal("accepted", patch)
		}
	}
	for n := 0; n < 105; n++ {
		create(t, a, c, map[string]any{"title": fmt.Sprint("Story ", n)})
	}
	v, e := a.dispatch(c, "items_list", map[string]any{"offset": 100})
	if e != nil {
		t.Fatal(e)
	}
	list := v.(map[string]any)
	if list["total"] != 105 || len(list["items"].([]Item)) != 5 {
		t.Fatal(list)
	}
	req := httptest.NewRequest("POST", "/items", strings.NewReader(`{"title":"valid"} {"title":"extra"}`))
	res := httptest.NewRecorder()
	a.http(res, req)
	if res.Code != 400 {
		t.Fatal(res.Code)
	}
	req = httptest.NewRequest("POST", "/items", strings.NewReader(`{"title":"valid","deadline":"2026-12-01"}`))
	res = httptest.NewRecorder()
	a.http(res, req)
	if res.Code != 200 {
		t.Fatal(res.Body.String())
	}
}
func TestSettingsGuardAndRestore(t *testing.T) {
	a, c := testApp(t, nil)
	s, _ := getSettings(c.AppDB(), "alpha")
	s.Statuses = append(s.Statuses, "legal_review")
	s.Formats = append(s.Formats, "webinar")
	args := map[string]any{"revision": int64(0), "statuses": s.Statuses, "formats": s.Formats, "channels": s.Channels}
	if _, e := a.dispatch(c, "settings_update", args); e != nil {
		t.Fatal(e)
	}
	i := create(t, a, c, map[string]any{"title": "Launch", "status": "legal_review", "format": "webinar"})
	args["revision"] = int64(1)
	args["statuses"] = defaultSettings().Statuses
	if _, e := a.dispatch(c, "settings_update", args); e == nil {
		t.Fatal("removed in-use status")
	}
	v, e := a.dispatch(c, "items_update", map[string]any{"id": i.ID, "revision": i.Revision, "patch": map[string]any{"archived": true}})
	if e != nil {
		t.Fatal(e)
	}
	i = v.(Item)
	if _, e = a.dispatch(c, "releases_create", map[string]any{"item_id": i.ID, "channel": "Website"}); e == nil {
		t.Fatal("edited archived item")
	}
	if _, e = a.dispatch(c, "items_update", map[string]any{"id": i.ID, "revision": i.Revision, "patch": map[string]any{"archived": false}}); e != nil {
		t.Fatal(e)
	}
	other, _ := getSettings(c.AppDB(), "beta")
	if other.Revision != 0 {
		t.Fatal("settings leaked")
	}
}
func TestConcurrentEdits(t *testing.T) {
	a, c := testApp(t, nil)
	i := create(t, a, c, map[string]any{"title": "Concurrent"})
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for n := 0; n < 2; n++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			_, e := a.dispatch(c, "items_update", map[string]any{"id": i.ID, "revision": i.Revision, "patch": map[string]any{"title": fmt.Sprint(n)}})
			errs <- e
		}(n)
	}
	wg.Wait()
	close(errs)
	conflicts, success := 0, 0
	for e := range errs {
		if errors.Is(e, errConflict) {
			conflicts++
		} else if e == nil {
			success++
		} else {
			t.Fatal(e)
		}
	}
	if conflicts != 1 || success != 1 {
		t.Fatal(conflicts, success)
	}
}

type platform struct {
	tk.BasePlatformClient
	bindings map[string]any
	calls    []string
	result   map[string]any
	fail     bool
	project  string
}

func (p *platform) WhoAmI() (*sdk.InstallIdentity, error) {
	return &sdk.InstallIdentity{Bindings: p.bindings}, nil
}
func (p *platform) CallAppResult(app, tool string, args map[string]any, out any) error {
	p.calls = append(p.calls, app+"/"+tool)
	p.project = str(args, "_project_id")
	if p.fail {
		return errors.New("upstream unavailable")
	}
	raw, e := json.Marshal(p.result)
	if e != nil {
		return e
	}
	return json.Unmarshal(raw, out)
}
func TestOptionalIntegrationReadOnlyRefresh(t *testing.T) {
	p := &platform{bindings: map[string]any{}}
	a, c := testApp(t, p)
	i := create(t, a, c, map[string]any{"title": "Social launch"})
	v, e := a.dispatch(c, "releases_create", map[string]any{"item_id": i.ID, "channel": "LinkedIn", "app": "social", "external_id": int64(21), "planned_at": "2026-10-01"})
	if e != nil {
		t.Fatal(e)
	}
	r := v.(Release)
	if _, e = a.dispatch(c, "releases_refresh", map[string]any{"id": r.ID}); e == nil {
		t.Fatal("unbound integration allowed")
	}
	if len(p.calls) != 0 {
		t.Fatal(p.calls)
	}
	p.bindings["social"] = float64(7)
	p.result = map[string]any{"posts": []any{map[string]any{"id": 21, "status": "published", "targets": []any{map[string]any{"platform_url": "https://example.com/post"}}}}}
	v, e = a.dispatch(c, "releases_refresh", map[string]any{"id": r.ID})
	if e != nil {
		t.Fatal(e)
	}
	r = v.(Release)
	if r.Results["status"] != "published" || r.SyncedAt == "" || r.PlannedAt != "2026-10-01" || r.Status != "planned" {
		t.Fatal(r)
	}
	if p.project != "alpha" || len(p.calls) != 1 || p.calls[0] != "social/post_list" {
		t.Fatal(p.calls, p.project)
	}
	p.result = map[string]any{"posts": []any{}}
	if _, e = a.dispatch(c, "releases_refresh", map[string]any{"id": r.ID}); e == nil {
		t.Fatal("missing result accepted")
	}
	saved, _ := readRelease(c.AppDB(), "alpha", r.ID)
	if saved.Revision != r.Revision || saved.Results["status"] != "published" {
		t.Fatal("lost results")
	}
	p.bindings["campaigns"] = float64(8)
	p.result = map[string]any{"campaign": map[string]any{"id": 10, "status": "sent", "stats": map[string]any{"delivered": 42}}}
	v, e = a.dispatch(c, "releases_update", map[string]any{"id": r.ID, "revision": r.Revision, "patch": map[string]any{"app": "campaigns", "external_id": int64(10)}})
	if e != nil {
		t.Fatal(e)
	}
	r = v.(Release)
	if len(r.Results) != 0 || r.SyncedAt != "" {
		t.Fatal("stale link results")
	}
	v, e = a.dispatch(c, "releases_refresh", map[string]any{"id": r.ID})
	if e != nil {
		t.Fatal(e)
	}
	if v.(Release).Results["status"] != "sent" {
		t.Fatal(v)
	}
	if p.calls[len(p.calls)-1] != "campaigns/campaigns_get" {
		t.Fatal(p.calls)
	}
}
func TestHTTPCreateUpdateConflict(t *testing.T) {
	a, _ := testApp(t, nil)
	req := httptest.NewRequest("POST", "/items", strings.NewReader(`{"title":"HTTP content"}`))
	res := httptest.NewRecorder()
	a.http(res, req)
	if res.Code != 200 {
		t.Fatal(res.Body.String())
	}
	var i Item
	json.Unmarshal(res.Body.Bytes(), &i)
	body := []byte(`{"revision":1,"patch":{"body":"Draft"}}`)
	for _, code := range []int{200, 409} {
		req = httptest.NewRequest("PATCH", fmt.Sprintf("/items/%d", i.ID), bytes.NewReader(body))
		res = httptest.NewRecorder()
		a.http(res, req)
		if res.Code != code {
			t.Fatal(res.Code, res.Body.String())
		}
	}
}

// The icon must work when source-installed binaries run from a directory
// without ./ui, including before the full panel assets have been resolved.
func TestIconRouteIndependentOfWorkingDirectory(t *testing.T) {
	a := &App{}
	var route *sdk.Route
	for _, r := range a.HTTPRoutes() {
		if r.Pattern == "/ui/icon.svg" {
			copy := r
			route = &copy
		}
	}
	if route == nil {
		t.Fatal("missing canonical icon route")
	}
	t.Chdir(t.TempDir())
	w := httptest.NewRecorder()
	route.Handler(w, httptest.NewRequest("GET", "/ui/icon.svg?v=0.1.1", nil))
	if w.Code != 200 || w.Header().Get("Content-Type") != "image/svg+xml" {
		t.Fatal(w.Code, w.Header())
	}
	if !bytes.Equal(w.Body.Bytes(), iconSVG) || !bytes.Contains(w.Body.Bytes(), []byte(`stroke="currentColor"`)) {
		t.Fatal("missing adaptive icon")
	}
	if a.Manifest().Icon != "/ui/icon.svg" || a.Manifest().IconStyle != "monochrome" {
		t.Fatal("icon manifest drift")
	}
}
