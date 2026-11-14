package main

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestPerformanceCustomDateWindowsAndValidation(t *testing.T) {
	now := time.Date(2026, 11, 14, 11, 0, 0, 0, time.UTC)
	r, err := performanceDateWindow(httptest.NewRequest("GET", "/?start_date=2026-11-01&end_date=2026-11-10", nil), now)
	if err != nil || r.Days != 10 || r.Start.Format("2006-01-02") != "2026-11-01" || r.End.Format("2006-01-02") != "2026-11-11" || r.Previous.Format("2006-01-02") != "2026-10-22" {
		t.Fatalf("%+v %v", r, err)
	}
	for _, query := range []string{"start_date=2026-11-01", "end_date=2026-11-10", "start_date=2026-11-10&end_date=2026-11-01", "start_date=2026-02-30&end_date=2026-03-03", "start_date=2025-01-01&end_date=2026-11-13", "start_date=2026-11-13&end_date=2026-11-14", "start_date=2026-11-01&end_date=2026-11-10&days=7", "days=8"} {
		if _, err := performanceDateWindow(httptest.NewRequest("GET", "/?"+query, nil), now); err == nil {
			t.Fatalf("invalid range accepted: %s", query)
		}
	}
}

func TestPerformanceFiltersIntersectProfilesPlatformsAccountsAndDates(t *testing.T) {
	ctx := newSocialCtx(t, newRecordingPlatform())
	a := &App{}
	res, err := ctx.AppDB().Exec(`INSERT INTO profiles(project_id,name,slug) VALUES('test-proj','Studio A','studio-a')`)
	if err != nil {
		t.Fatal(err)
	}
	profile, _ := res.LastInsertId()
	fb := widgetAccountFixture(t, ctx, "test-proj", profile)
	ig := widgetAccountFixture(t, ctx, "test-proj", profile)
	other := widgetAccountFixture(t, ctx, "test-proj", 0)
	if _, err := ctx.AppDB().Exec(`UPDATE social_accounts SET platform='facebook',display_name='Page A' WHERE id=?`, fb); err != nil {
		t.Fatal(err)
	}
	if _, err := ctx.AppDB().Exec(`UPDATE social_accounts SET platform='instagram' WHERE id=?`, ig); err != nil {
		t.Fatal(err)
	}
	foreign := widgetAccountFixture(t, ctx, "other-project", 0)
	for _, account := range []int64{fb, ig, other} {
		for date, value := range map[string]int64{"2026-10-30": 9, "2026-11-01": 10, "2026-11-02": 20, "2026-11-03": 999} {
			if err := insertSocialMetricPoint(ctx, "test-proj", profile, account, 0, 0, "facebook", "account", "views", "day", date+"T00:00:00Z", value, "test", "ok", ""); err != nil {
				t.Fatal(err)
			}
		}
	}
	query := url.Values{"profile_id": {fmt.Sprint(profile)}, "platforms": {"facebook"}, "account_ids": {fmt.Sprint(fb)}, "start_date": {"2026-11-01"}, "end_date": {"2026-11-02"}}
	w := httptest.NewRecorder()
	a.handlePerformanceWidget(w, httptest.NewRequest("GET", "/widgets/performance?"+query.Encode(), nil))
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var response struct {
		Accounts []widgetAccount         `json:"accounts"`
		Metrics  map[string]widgetMetric `json:"metrics"`
		Days     int                     `json:"days"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Accounts) != 1 || response.Accounts[0].ID != fb || response.Accounts[0].ProfileName != "Studio A" || response.Days != 2 || response.Metrics["views"].Value == nil || *response.Metrics["views"].Value != 30 {
		t.Fatalf("%s", w.Body)
	}
	for _, bad := range []url.Values{{"account_ids": {fmt.Sprint(foreign)}}, {"platforms": {"unknown"}}, {"profile_id": {"999999"}}, {"demo_data": {"maybe"}}} {
		w = httptest.NewRecorder()
		a.handlePerformanceWidget(w, httptest.NewRequest("GET", "/widgets/performance?"+bad.Encode(), nil))
		if w.Code != 400 {
			t.Fatalf("unsafe filter accepted: %v %d", bad, w.Code)
		}
	}
}

func TestPerformanceOptionsAreProjectScoped(t *testing.T) {
	ctx := newSocialCtx(t, newRecordingPlatform())
	account := widgetAccountFixture(t, ctx, "test-proj", 0)
	widgetAccountFixture(t, ctx, "other-project", 0)
	w := httptest.NewRecorder()
	(&App{}).handlePerformanceOptions(w, httptest.NewRequest("GET", "/widgets/performance/options?project_id=other-project", nil))
	var options performanceOptions
	if err := json.Unmarshal(w.Body.Bytes(), &options); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || len(options.Accounts) != 1 || options.Accounts[0].ID != account || options.Demo {
		t.Fatalf("scope leak: %s", w.Body)
	}
	for _, method := range []string{"POST", "DELETE"} {
		w = httptest.NewRecorder()
		(&App{}).handlePerformanceOptions(w, httptest.NewRequest(method, "/widgets/performance/options", nil))
		if w.Code != 405 {
			t.Fatalf("method %s allowed", method)
		}
	}
}

func TestPerformanceKeepsInactiveAccountsAndCachedMetrics(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newSocialCtx(t, pf)
	account := widgetAccountFixture(t, ctx, "test-proj", 0)
	if _, err := ctx.AppDB().Exec(`UPDATE social_accounts SET status='inactive' WHERE id=?`, account); err != nil {
		t.Fatal(err)
	}
	if err := insertSocialMetricPoint(ctx, "test-proj", 0, account, 0, 0, "youtube", "account", "followers", "snapshot", time.Now().UTC().Format(time.RFC3339), 900, "test", "ok", ""); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	(&App{}).handlePerformanceWidget(w, httptest.NewRequest("GET", "/widgets/performance?days=7", nil))
	var response struct {
		Accounts []widgetAccount `json:"accounts"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || len(response.Accounts) != 1 || response.Accounts[0].Status != "inactive" || response.Accounts[0].Metrics["followers"].Value == nil || *response.Accounts[0].Metrics["followers"].Value != 900 {
		t.Fatalf("inactive account disappeared: %s", w.Body)
	}
	w = httptest.NewRecorder()
	(&App{}).handlePerformanceOptions(w, httptest.NewRequest("GET", "/widgets/performance/options", nil))
	var options performanceOptions
	_ = json.Unmarshal(w.Body.Bytes(), &options)
	if len(options.Accounts) != 1 || options.Accounts[0].Status != "inactive" {
		t.Fatalf("inactive account cannot be selected: %s", w.Body)
	}
	if len(pf.executeCalls) != 0 || len(pf.callAppCalls) != 0 {
		t.Fatal("read attempted to revive an inactive account")
	}
}

func TestPerformanceDemoHasFourProfilesAndSixteenIsolatedAccounts(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newSocialCtx(t, pf)
	a := &App{}
	w := httptest.NewRecorder()
	a.handlePerformanceOptions(w, httptest.NewRequest("GET", "/widgets/performance/options?demo_data=true", nil))
	var options performanceOptions
	if err := json.Unmarshal(w.Body.Bytes(), &options); err != nil {
		t.Fatal(err)
	}
	if len(options.Profiles) != 4 || len(options.Accounts) != 16 || len(options.Platforms) != 4 || !options.Demo {
		t.Fatalf("%s", w.Body)
	}
	counts := map[int64]int{}
	for _, account := range options.Accounts {
		counts[account.ProfileID]++
	}
	for _, profile := range options.Profiles {
		if counts[profile.ID] != 4 {
			t.Fatal("profile does not have four accounts")
		}
	}
	for _, tc := range []struct {
		query string
		count int
	}{{"demo_data=true&days=28", 16}, {"demo_data=true&profile_id=2&days=7", 4}, {"demo_data=true&platforms=instagram&days=90", 4}, {"demo_data=true&profile_id=2&platforms=instagram&days=28", 1}, {"demo_data=true&account_ids=1,16&days=7", 2}} {
		w = httptest.NewRecorder()
		a.handlePerformanceWidget(w, httptest.NewRequest("GET", "/widgets/performance?"+tc.query, nil))
		var data struct {
			Demo     bool                    `json:"demo_data"`
			Accounts []widgetAccount         `json:"accounts"`
			Metrics  map[string]widgetMetric `json:"metrics"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &data); err != nil {
			t.Fatal(err)
		}
		if w.Code != 200 || !data.Demo || len(data.Accounts) != tc.count || data.Metrics["followers"].Value == nil || data.Metrics["views"].Value == nil {
			t.Fatalf("%s: %s", tc.query, w.Body)
		}
		if tc.count == 16 && *data.Metrics["followers"].Value != 224720 {
			t.Fatal("incorrect demo total")
		}
	}
	for _, query := range []string{"demo_data=true&profile_id=5", "demo_data=true&account_ids=17", "demo_data=true&profile_id=2&account_ids=1"} {
		w = httptest.NewRecorder()
		a.handlePerformanceWidget(w, httptest.NewRequest("GET", "/widgets/performance?"+query, nil))
		if w.Code != 400 {
			t.Fatalf("bad demo scope: %s", query)
		}
	}
	for _, table := range []string{"social_accounts", "social_metric_points"} {
		var count int
		if err := ctx.AppDB().QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("demo wrote real %s", table)
		}
	}
	if len(pf.executeCalls) != 0 || len(pf.callAppCalls) != 0 {
		t.Fatal("demo contacted a provider")
	}
}
