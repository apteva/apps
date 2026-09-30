package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strconv"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

func widgetAccountFixture(t *testing.T, ctx *sdk.AppCtx, project string, profile int64) int64 {
	t.Helper()
	r, err := ctx.AppDB().Exec(`INSERT INTO social_accounts(project_id,profile_id,platform,connection_id,display_name,status) VALUES (?,?,'youtube',42,'Studio','active')`, project, profile)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := r.LastInsertId()
	return id
}
func widgetPostFixture(t *testing.T, ctx *sdk.AppCtx, project string, profile, account int64, status string, when time.Time) int64 {
	t.Helper()
	r, err := ctx.AppDB().Exec(`INSERT INTO posts(project_id,profile_id,body,status,schedule_at) VALUES (?,?,'A scheduled post',?,?)`, project, profile, status, when.UTC().Format(time.RFC3339))
	if err != nil {
		t.Fatal(err)
	}
	id, _ := r.LastInsertId()
	if account > 0 {
		if _, err := ctx.AppDB().Exec(`INSERT INTO post_targets(post_id,social_account_id,status) VALUES (?,?,'pending')`, id, account); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

func TestPublishingWidgetScopedEarliestPostsAndAttention(t *testing.T) {
	ctx := newSocialCtx(t, newRecordingPlatform())
	app := &App{}
	profileOut, err := app.toolProfileCreate(ctx, map[string]any{"name": "Brand"})
	if err != nil {
		t.Fatal(err)
	}
	profile := profileOut.(map[string]any)["profile"].(*Profile).ID
	account := widgetAccountFixture(t, ctx, "test-proj", profile)
	now := time.Now().UTC().Truncate(time.Second)
	late := widgetPostFixture(t, ctx, "test-proj", profile, account, "scheduled", now.Add(48*time.Hour))
	first := widgetPostFixture(t, ctx, "test-proj", profile, account, "scheduled", now.Add(time.Hour))
	widgetPostFixture(t, ctx, "other-project", 0, 0, "scheduled", now.Add(30*time.Minute))
	widgetPostFixture(t, ctx, "test-proj", 0, 0, "scheduled", now.Add(45*time.Minute))
	widgetPostFixture(t, ctx, "test-proj", profile, account, "failed", now.Add(-time.Hour))
	draft := widgetPostFixture(t, ctx, "test-proj", profile, account, "in_review", now)
	if _, err := ctx.AppDB().Exec(`UPDATE posts SET approval_status='pending' WHERE id=?`, draft); err != nil {
		t.Fatal(err)
	}
	params := url.Values{"project_id": {"spoofed-project"}, "profile_id": {strconv.FormatInt(profile, 10)}, "account_ids": {strconv.FormatInt(account, 10)}, "mode": {"upcoming"}, "from": {now.Format(time.RFC3339)}, "to": {now.Add(7 * 24 * time.Hour).Format(time.RFC3339)}, "limit": {"1"}}
	w := httptest.NewRecorder()
	app.handlePublishingWidget(w, httptest.NewRequest("GET", "/widgets/publishing-calendar?"+params.Encode(), nil))
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var out struct {
		Posts     []struct{ ID int64 }
		Total     int
		Attention map[string]int
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Posts) != 1 || out.Posts[0].ID != first || out.Posts[0].ID == late || out.Total != 2 || out.Attention["failed"] != 1 || out.Attention["approval"] != 1 {
		t.Fatalf("unexpected summary %+v", out)
	}
	params.Set("mode", "calendar")
	params.Set("from", now.Add(-2*time.Hour).Format(time.RFC3339))
	w = httptest.NewRecorder()
	app.handlePublishingWidget(w, httptest.NewRequest("GET", "/widgets/publishing-calendar?"+params.Encode(), nil))
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Posts) != 4 {
		t.Fatalf("calendar omitted lifecycle posts: %s", w.Body)
	}
}

func TestWidgetScopeFailsClosedAndRoutesRemainPrivate(t *testing.T) {
	ctx := newSocialCtx(t, newRecordingPlatform())
	app := &App{}
	foreign := widgetAccountFixture(t, ctx, "other-project", 0)
	for _, query := range []string{"account_ids=garbage", "account_ids=" + strconv.FormatInt(foreign, 10), "profile_id=99999", "account_ids=0", "account_ids=1%2Cbad", "days=8"} {
		w := httptest.NewRecorder()
		app.handlePerformanceWidget(w, httptest.NewRequest("GET", "/widgets/performance?"+query, nil))
		if w.Code != 400 {
			t.Fatalf("query %s: %d %s", query, w.Code, w.Body)
		}
	}
	w := httptest.NewRecorder()
	app.handlePerformanceWidget(w, httptest.NewRequest("POST", "/widgets/performance", nil))
	if w.Code != 405 {
		t.Fatalf("POST status=%d", w.Code)
	}
	w = httptest.NewRecorder()
	app.handlePublishingWidget(w, httptest.NewRequest("GET", "/widgets/publishing-calendar?mode=calendar&from=2026-01-01T00:00:00Z&to=2027-01-01T00:00:00Z", nil))
	if w.Code != 400 {
		t.Fatal("unbounded calendar accepted")
	}
	for _, route := range app.HTTPRoutes() {
		if route.NoAuth && route.Pattern != "/accounts/oauth_done" {
			t.Fatalf("unexpected public route: %+v", route)
		}
	}
}

func TestPerformanceWidgetUsesDailyDataWithoutProviderCalls(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newSocialCtx(t, pf)
	app := &App{}
	account := widgetAccountFixture(t, ctx, "test-proj", 0)
	widgetAccountFixture(t, ctx, "test-proj", 0) // unavailable account must not turn into zero
	foreign := widgetAccountFixture(t, ctx, "foreign-project", 0)
	today := time.Now().UTC().Truncate(24 * time.Hour)
	point := func(id int64, project, metric, period string, at time.Time, value int64, source string) {
		t.Helper()
		if err := insertSocialMetricPoint(ctx, project, 0, id, 0, 0, "youtube", "account", metric, period, at.Format(time.RFC3339), value, source, "ok", ""); err != nil {
			t.Fatal(err)
		}
	}
	point(account, "test-proj", "followers", "snapshot", today, 0, "snapshot") // zero is known
	point(account, "test-proj", "views", "query:range_28d", today, 999999, "lifetime")
	point(account, "test-proj", "_refresh", "heartbeat", today, 1, "refresh")
	for day := 1; day <= 14; day++ {
		at := today.AddDate(0, 0, -day)
		value := int64(10)
		if day <= 7 {
			value = 20
		}
		point(account, "test-proj", "views", "day", at, value, "series")
		point(account, "test-proj", "impressions", "day", at, 100, "series")
		point(account, "test-proj", "likes", "day", at, 1, "series")
		point(account, "test-proj", "comments", "day", at, 2, "series")
		point(account, "test-proj", "shares", "day", at, 3, "series")
		point(foreign, "foreign-project", "views", "day", at, 10000, "series")
	}
	point(account, "test-proj", "views", "day", today.AddDate(0, 0, -1), 500, "old-source")
	// A newer observation supersedes the same day's older source.
	point(account, "test-proj", "views", "day", today.AddDate(0, 0, -1), 20, "new-source")
	w := httptest.NewRecorder()
	app.handlePerformanceWidget(w, httptest.NewRequest("GET", "/widgets/performance?days=7", nil))
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var out struct {
		Accounts []widgetAccount
		Metrics  map[string]widgetMetric
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	views := out.Metrics["views"]
	followers := out.Metrics["followers"]
	interactions := out.Metrics["interactions"]
	if len(out.Accounts) != 2 || *views.Value != 140 || views.Accounts != 1 || views.Days != 7 || views.Change == nil || *views.Change != 100 || *followers.Value != 0 || followers.Accounts != 1 || *interactions.Value != 42 {
		t.Fatalf("bad summary %s", w.Body)
	}
	if len(pf.executeCalls) != 0 || len(pf.callAppCalls) != 0 {
		t.Fatal("Home triggered provider calls")
	}
	if out.Accounts[1].Metrics["views"].Value != nil {
		t.Fatal("unavailable analytics became zero")
	}
}

func TestPerformanceGapsSuppressComparisonsAndPartialInteractions(t *testing.T) {
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 0, 2)
	accounts := []widgetAccount{{daily: map[string]map[string]int64{"views": {"2026-09-01": 20, "2026-09-02": 30, "2026-08-30": 10, "2026-08-31": 10}}}, {daily: map[string]map[string]int64{"views": {"2026-09-01": 5}}}}
	m := summarizeWidgetMetric(accounts, "views", start, end, start.AddDate(0, 0, -2))
	if *m.Value != 55 || m.Accounts != 2 || m.Days != 1 || m.Change != nil || m.Trend[1].Value != nil || m.Trend[1].Accounts != 1 {
		t.Fatalf("missing data was fabricated: %+v", m)
	}
}

func TestHomeWidgetSidecarAuthentication(t *testing.T) {
	if testing.Short() {
		t.Skip("builds an isolated sidecar")
	}
	sidecar := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"))
	for _, path := range []string{"/widgets/performance", "/widgets/publishing-calendar", "/accounts", "/posts"} {
		response, err := http.Get(sidecar.URL() + path)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("anonymous %s returned %d", path, response.StatusCode)
		}
	}
	var performance map[string]any
	if result := sidecar.GET("/widgets/performance?project_id=test-proj", &performance); result.Status != 200 {
		t.Fatalf("authenticated summary: %+v", result)
	}
}

func TestHomeWidgetDeclarationsMatchAndShipEntries(t *testing.T) {
	raw, err := os.ReadFile("apteva.yaml")
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := sdk.ParseManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	embedded := (&App{}).Manifest()
	if !reflect.DeepEqual(manifest.Provides.UIComponents, embedded.Provides.UIComponents) {
		t.Fatal("UI component declarations differ between installed and embedded manifest")
	}
	homeCount := 0
	for _, component := range manifest.Provides.UIComponents {
		for _, slot := range component.Slots {
			if slot == "dashboard.home" {
				homeCount++
				if !reflect.DeepEqual(component.SupportedSizes, []string{"half", "full"}) || component.DefaultSize != "half" {
					t.Fatalf("invalid Home sizes: %+v", component)
				}
				if _, err := os.Stat("." + component.Entry); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	if homeCount != 2 {
		t.Fatalf("want two Home widgets, got %d", homeCount)
	}
}
