package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

// projectsPlatform lists a fixed set of projects for the install.
type projectsPlatform struct {
	tk.BasePlatformClient
	projects []sdk.PlatformProject
}

func (p *projectsPlatform) ListProjects() ([]sdk.PlatformProject, error) { return p.projects, nil }

type globalInboxPage struct {
	Inbox             []*inboxRow    `json:"inbox"`
	Count             int            `json:"count"`
	Total             int            `json:"total"`
	Offset            int            `json:"offset"`
	Projects          []inboxProject `json:"projects"`
	SelectedProjectID string         `json:"selected_project_id"`
}

// globalInboxFixture: Alpha has two open conversations, Beta one, and the
// unlisted project Hidden one. Newest activity: beta, alpha-2, hidden, alpha-1.
func globalInboxFixture(t *testing.T) (*App, map[string]int64) {
	t.Helper()
	pf := &projectsPlatform{projects: []sdk.PlatformProject{
		{ID: "proj-alpha", Name: "Alpha"},
		{ID: "proj-beta", Name: "Beta"},
	}}
	ctx := newTestCtx(t, tk.WithPlatform(pf))
	t.Setenv("APTEVA_PROJECT_ID", "") // a global install has no pinned project
	globalCtx = ctx
	ids := map[string]int64{}
	add := func(key, pid, email, at string) {
		c := mustCreate(t, ctx, map[string]any{"primary_email": email, "_project_id": pid})
		id := mkConversation(t, ctx, pid, c.ID, "email")
		if _, err := ctx.AppDB().Exec(`UPDATE contact_conversations SET last_activity_at=? WHERE id=?`, at, id); err != nil {
			t.Fatal(err)
		}
		ids[key] = id
	}
	add("alpha-1", "proj-alpha", "one@alpha.test", "2026-11-20T10:00:00Z")
	add("alpha-2", "proj-alpha", "two@alpha.test", "2026-11-24T10:00:00Z")
	add("beta", "proj-beta", "one@beta.test", "2026-11-25T10:00:00Z")
	add("hidden", "proj-hidden", "one@hidden.test", "2026-11-22T10:00:00Z")
	return &App{}, ids
}

func globalInboxGet(t *testing.T, app *App, target string, headers map[string]string) (*httptest.ResponseRecorder, globalInboxPage) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.Header.Set("X-User-ID", "1")
	for k, v := range headers {
		if v == "" {
			req.Header.Del(k)
		} else {
			req.Header.Set(k, v)
		}
	}
	rec := httptest.NewRecorder()
	app.handleHTTPInbox(rec, req)
	var page globalInboxPage
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
			t.Fatalf("decode: %v body=%s", err, rec.Body.String())
		}
	}
	return rec, page
}

func rowIDs(rows []*inboxRow) []int64 {
	out := []int64{}
	for _, r := range rows {
		out = append(out, r.ID)
	}
	return out
}

func TestGlobalInbox_AllListedProjects(t *testing.T) {
	app, ids := globalInboxFixture(t)
	rec, page := globalInboxGet(t, app, "/inbox?scope=global&status=open", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	want := []int64{ids["beta"], ids["alpha-2"], ids["alpha-1"]}
	if got := rowIDs(page.Inbox); len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("rows=%v want %v (newest first, hidden project excluded)", got, want)
	}
	if page.Total != 3 || page.Count != 3 {
		t.Fatalf("total=%d count=%d", page.Total, page.Count)
	}
	names := map[string]string{"proj-alpha": "Alpha", "proj-beta": "Beta"}
	for _, row := range page.Inbox {
		if row.ProjectID == "" || row.ProjectName != names[row.ProjectID] {
			t.Fatalf("row %d project=%q name=%q", row.ID, row.ProjectID, row.ProjectName)
		}
	}
	if len(page.Projects) != 2 || page.Projects[0].Name != "Alpha" || page.Projects[1].Name != "Beta" {
		t.Fatalf("projects=%+v", page.Projects)
	}
}

func TestGlobalInbox_SelectedProject(t *testing.T) {
	app, ids := globalInboxFixture(t)
	rec, page := globalInboxGet(t, app, "/inbox?scope=global&project_id=proj-beta", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := rowIDs(page.Inbox); len(got) != 1 || got[0] != ids["beta"] || page.SelectedProjectID != "proj-beta" {
		t.Fatalf("rows=%v selected=%q", got, page.SelectedProjectID)
	}
	if len(page.Projects) != 2 {
		t.Fatalf("filter list should still hold every project: %+v", page.Projects)
	}
}

func TestGlobalInbox_ChannelFilterApplies(t *testing.T) {
	app, _ := globalInboxFixture(t)
	filters := url.QueryEscape(`[{"field":"channel","op":"is","value":"sms"}]`)
	rec, page := globalInboxGet(t, app, "/inbox?scope=global&filters="+filters, nil)
	if rec.Code != http.StatusOK || page.Total != 0 {
		t.Fatalf("status=%d total=%d body=%s", rec.Code, page.Total, rec.Body.String())
	}
}

func TestGlobalInbox_PagingAcrossProjects(t *testing.T) {
	app, ids := globalInboxFixture(t)
	seen := []int64{}
	for offset := 0; offset < 4; offset++ {
		rec, page := globalInboxGet(t, app, "/inbox?scope=global&limit=1&offset="+strconv.Itoa(offset), nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("offset %d: status=%d body=%s", offset, rec.Code, rec.Body.String())
		}
		if page.Total != 3 {
			t.Fatalf("offset %d: total=%d", offset, page.Total)
		}
		seen = append(seen, rowIDs(page.Inbox)...)
	}
	want := []int64{ids["beta"], ids["alpha-2"], ids["alpha-1"]}
	if len(seen) != 3 || seen[0] != want[0] || seen[1] != want[1] || seen[2] != want[2] {
		t.Fatalf("paged rows=%v want %v", seen, want)
	}
}

func TestGlobalInbox_Refusals(t *testing.T) {
	app, _ := globalInboxFixture(t)
	cases := []struct {
		name    string
		target  string
		headers map[string]string
		want    int
	}{
		{"hidden project", "/inbox?scope=global&project_id=proj-hidden", nil, http.StatusForbidden},
		{"no user", "/inbox?scope=global", map[string]string{"X-User-ID": ""}, http.StatusUnauthorized},
		{"delegated user", "/inbox?scope=global", map[string]string{"X-Apteva-Issuer-App": "portal", "X-Apteva-Issuer-Install-ID": "9"}, http.StatusForbidden},
		{"bad scope", "/inbox?scope=other&project_id=proj-alpha", nil, http.StatusBadRequest},
	}
	for _, tc := range cases {
		rec, _ := globalInboxGet(t, app, tc.target, tc.headers)
		if rec.Code != tc.want {
			t.Fatalf("%s: status=%d want %d body=%s", tc.name, rec.Code, tc.want, rec.Body.String())
		}
	}
}

func TestProjectInbox_UnchangedByGlobalScope(t *testing.T) {
	app, ids := globalInboxFixture(t)
	rec, _ := globalInboxGet(t, app, "/inbox?project_id=proj-alpha", map[string]string{"X-User-ID": ""})
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"projects", "selected_project_id"} {
		if _, ok := raw[key]; ok {
			t.Fatalf("project response gained %q", key)
		}
	}
	var page globalInboxPage
	_ = json.Unmarshal(rec.Body.Bytes(), &page)
	if got := rowIDs(page.Inbox); len(got) != 2 || got[0] != ids["alpha-2"] || got[1] != ids["alpha-1"] {
		t.Fatalf("rows=%v", got)
	}
	for _, row := range page.Inbox {
		if row.ProjectID != "" || row.ProjectName != "" {
			t.Fatalf("project row carries project fields: %+v", row)
		}
	}
}
