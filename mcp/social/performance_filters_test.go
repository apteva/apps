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
	for _, bad := range []url.Values{{"account_ids": {fmt.Sprint(foreign)}}, {"platforms": {"unknown"}}, {"profile_id": {"999999"}}} {
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
	if w.Code != 200 || len(options.Accounts) != 1 || options.Accounts[0].ID != account {
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

func TestPerformanceReadsPersistedProfilesAccountsAndMetrics(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newSocialCtx(t, pf)
	var profileIDs, accountIDs []int64
	today := time.Now().UTC().Truncate(24 * time.Hour)
	for i, brand := range []string{"Aurora Studio", "Cedar Outdoors", "Ember Kitchen", "Lunar Fitness"} {
		res, err := ctx.AppDB().Exec(`INSERT INTO profiles(project_id,name,slug) VALUES('test-proj',?,?)`, brand, fmt.Sprintf("sample-%d", i))
		if err != nil {
			t.Fatal(err)
		}
		profile, _ := res.LastInsertId()
		profileIDs = append(profileIDs, profile)
		for _, platform := range []string{"facebook", "instagram", "tiktok", "youtube"} {
			res, err := ctx.AppDB().Exec(`INSERT INTO social_accounts(project_id,profile_id,platform,connection_id,display_name,status) VALUES('test-proj',?,?,0,?,'disconnected')`, profile, platform, brand+" "+platform)
			if err != nil {
				t.Fatal(err)
			}
			account, _ := res.LastInsertId()
			accountIDs = append(accountIDs, account)
			if err := insertSocialMetricPoint(ctx, "test-proj", profile, account, 0, 0, platform, "account", "followers", "snapshot", today.Format(time.RFC3339), 1000, "seed", "ok", ""); err != nil {
				t.Fatal(err)
			}
			for offset := 14; offset > 0; offset-- {
				for _, metric := range []string{"views", "impressions", "likes", "comments", "shares"} {
					if err := insertSocialMetricPoint(ctx, "test-proj", profile, account, 0, 0, platform, "account", metric, "day", today.AddDate(0, 0, -offset).Format(time.RFC3339), 10, "seed", "ok", ""); err != nil {
						t.Fatal(err)
					}
				}
			}
		}
	}
	options, err := loadPerformanceOptions(ctx, "test-proj")
	if err != nil || len(options.Profiles) != 4 || len(options.Accounts) != 16 || len(options.Platforms) != 4 {
		t.Fatalf("persisted choices: %+v, %v", options, err)
	}
	for _, tc := range []struct {
		query string
		count int
	}{{"days=7", 16}, {fmt.Sprintf("days=7&profile_id=%d", profileIDs[1]), 4}, {"days=7&platforms=instagram", 4}, {fmt.Sprintf("days=7&profile_id=%d&platforms=instagram", profileIDs[1]), 1}, {fmt.Sprintf("days=7&account_ids=%d,%d", accountIDs[0], accountIDs[15]), 2}} {
		w := httptest.NewRecorder()
		(&App{}).handlePerformanceWidget(w, httptest.NewRequest("GET", "/widgets/performance?"+tc.query, nil))
		var data struct {
			Accounts []widgetAccount         `json:"accounts"`
			Metrics  map[string]widgetMetric `json:"metrics"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &data); err != nil {
			t.Fatal(err)
		}
		if w.Code != 200 || len(data.Accounts) != tc.count || data.Metrics["followers"].Value == nil || *data.Metrics["followers"].Value != int64(tc.count*1000) || data.Metrics["views"].Value == nil || *data.Metrics["views"].Value != int64(tc.count*70) || data.Metrics["interactions"].Value == nil || *data.Metrics["interactions"].Value != int64(tc.count*210) {
			t.Fatalf("%s: %s", tc.query, w.Body)
		}
		for _, account := range data.Accounts {
			if account.Status != "disconnected" || account.Metrics["views"].Change == nil || *account.Metrics["views"].Change != 0 {
				t.Fatalf("persisted status or previous-period comparison lost: %+v", account)
			}
		}
	}
	if len(pf.executeCalls) != 0 || len(pf.callAppCalls) != 0 {
		t.Fatal("reading seeded records contacted a provider")
	}
}
