package main

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type performanceWindow struct {
	Start, End, Previous time.Time
	Days                 int
}

func performanceDateWindow(r *http.Request, now time.Time) (performanceWindow, error) {
	today := now.UTC().Truncate(24 * time.Hour)
	startRaw, endRaw := r.URL.Query().Get("start_date"), r.URL.Query().Get("end_date")
	if startRaw != "" || endRaw != "" {
		start, e1 := time.Parse("2006-01-02", startRaw)
		end, e2 := time.Parse("2006-01-02", endRaw)
		end = end.AddDate(0, 0, 1)
		days := int(end.Sub(start) / (24 * time.Hour))
		if e1 != nil || e2 != nil || !start.Before(end) || end.After(today) || days > 366 || r.URL.Query().Get("days") != "" {
			return performanceWindow{}, fmt.Errorf("choose start_date and end_date for 1–366 complete UTC days, or days=7,28,90")
		}
		return performanceWindow{start, end, start.AddDate(0, 0, -days), days}, nil
	}
	days := 28
	if raw := r.URL.Query().Get("days"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			return performanceWindow{}, fmt.Errorf("days must be 7, 28, or 90")
		}
		days = n
	}
	if days != 7 && days != 28 && days != 90 {
		return performanceWindow{}, fmt.Errorf("days must be 7, 28, or 90")
	}
	return performanceWindow{today.AddDate(0, 0, -days), today, today.AddDate(0, 0, -2*days), days}, nil
}
func performancePlatforms(r *http.Request) ([]string, error) {
	raw := strings.TrimSpace(r.URL.Query().Get("platforms"))
	if raw == "" || raw == "all" {
		return nil, nil
	}
	seen := map[string]bool{}
	out := []string{}
	for _, name := range strings.Split(raw, ",") {
		name = strings.TrimSpace(name)
		if name == "x" {
			name = "twitter"
		}
		if _, ok := platforms[name]; !ok {
			return nil, fmt.Errorf("unsupported platform %q", name)
		}
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	return out, nil
}
func performanceDemoRequested(r *http.Request) (bool, error) {
	raw := r.URL.Query().Get("demo_data")
	if raw == "" {
		return false, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("invalid demo_data")
	}
	return value, nil
}

type performanceProfileOption struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}
type performanceAccountOption struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Platform    string `json:"platform"`
	ProfileID   int64  `json:"profile_id"`
	ProfileName string `json:"profile_name"`
	Status      string `json:"status"`
}
type performanceOptions struct {
	Profiles  []performanceProfileOption `json:"profiles"`
	Accounts  []performanceAccountOption `json:"accounts"`
	Platforms []string                   `json:"platforms"`
	Demo      bool                       `json:"demo_data"`
}

func (a *App) handlePerformanceOptions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "method not allowed", 405)
		return
	}
	project := projectScope(globalCtx, projectArgsFromRequest(r))
	if project == "" {
		http.Error(w, "project_id required", 400)
		return
	}
	demo, err := performanceDemoRequested(r)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if demo {
		writeJSON(w, demoPerformanceOptions())
		return
	}
	options, err := loadPerformanceOptions(globalCtx, project)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, options)
}
func loadPerformanceOptions(ctx *sdk.AppCtx, project string) (performanceOptions, error) {
	out := performanceOptions{Profiles: []performanceProfileOption{}, Accounts: []performanceAccountOption{}, Platforms: []string{}}
	rows, err := ctx.AppDB().Query(`SELECT id,name FROM profiles WHERE project_id=? ORDER BY name,id`, project)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var p performanceProfileOption
		if err = rows.Scan(&p.ID, &p.Name); err != nil {
			rows.Close()
			return out, err
		}
		out.Profiles = append(out.Profiles, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	rows, err = ctx.AppDB().Query(`SELECT a.id,a.display_name,a.platform,COALESCE(p.id,0),COALESCE(p.name,'Unassigned'),a.status FROM social_accounts a LEFT JOIN profiles p ON p.id=a.profile_id AND p.project_id=a.project_id WHERE a.project_id=? ORDER BY a.display_name,a.id`, project)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	seen := map[string]bool{}
	for rows.Next() {
		var account performanceAccountOption
		if err = rows.Scan(&account.ID, &account.Name, &account.Platform, &account.ProfileID, &account.ProfileName, &account.Status); err != nil {
			return out, err
		}
		out.Accounts = append(out.Accounts, account)
		seen[account.Platform] = true
	}
	for name := range seen {
		out.Platforms = append(out.Platforms, name)
	}
	sort.Strings(out.Platforms)
	return out, rows.Err()
}
func demoPerformanceOptions() performanceOptions {
	out := performanceOptions{Profiles: []performanceProfileOption{}, Accounts: []performanceAccountOption{}, Platforms: []string{"facebook", "instagram", "tiktok", "youtube"}, Demo: true}
	for i, name := range []string{"Aurora Studio", "Cedar Outdoors", "Ember Kitchen", "Lunar Fitness"} {
		id := int64(i + 1)
		out.Profiles = append(out.Profiles, performanceProfileOption{id, "Demo · " + name})
		for j, platform := range out.Platforms {
			out.Accounts = append(out.Accounts, performanceAccountOption{int64(i*4 + j + 1), name + " · " + platforms[platform].DisplayName, platform, id, "Demo · " + name, "demo"})
		}
	}
	return out
}
func (a *App) handleDemoPerformance(w http.ResponseWriter, r *http.Request, window performanceWindow, selectedPlatforms []string) {
	// Synthetic data never creates accounts, connections or metric rows, and
	// never enters provider polling, inbox or publication workflows.
	options := demoPerformanceOptions()
	profile, err := strconv.ParseInt(nonEmpty(r.URL.Query().Get("profile_id"), "0"), 10, 64)
	if err != nil || profile < 0 || profile > 4 {
		http.Error(w, "demo profile not found", 400)
		return
	}
	ids := map[int64]bool{}
	if raw := r.URL.Query().Get("account_ids"); raw != "" {
		for _, part := range strings.Split(raw, ",") {
			id, err := strconv.ParseInt(strings.TrimSpace(part), 10, 64)
			if err != nil || id < 1 || id > 16 || profile > 0 && (id-1)/4+1 != profile {
				http.Error(w, "demo account not found in this scope", 400)
				return
			}
			ids[id] = true
		}
	}
	accounts := []widgetAccount{}
	for _, option := range options.Accounts {
		if profile > 0 && option.ProfileID != profile || len(ids) > 0 && !ids[option.ID] || !performancePlatformMatches(option.Platform, selectedPlatforms) {
			continue
		}
		followers := int64(2400 + option.ID*1370)
		acct := widgetAccount{ID: option.ID, Name: option.Name, Platform: option.Platform, ProfileID: option.ProfileID, ProfileName: option.ProfileName, Status: "demo", UpdatedAt: time.Now().UTC().Format(time.RFC3339), Metrics: map[string]widgetMetric{"followers": {Value: &followers, Accounts: 1, Trend: []widgetMetricPoint{}}}, daily: map[string]map[string]int64{}, binding: fmt.Sprint("demo:", option.ID)}
		for _, name := range []string{"views", "impressions", "interactions"} {
			acct.daily[name] = map[string]int64{}
		}
		for day := window.Previous; day.Before(window.End); day = day.AddDate(0, 0, 1) {
			stamp := day.Format("2006-01-02")
			views := int64(160+option.ID*47) + int64(day.Unix()/86400%31)*int64(5+option.ID)
			acct.daily["views"][stamp] = views
			acct.daily["impressions"][stamp] = views*3/2 + 77
			acct.daily["interactions"][stamp] = views/12 + option.ID
		}
		for _, name := range widgetMetricNames[1:] {
			acct.Metrics[name] = summarizeWidgetMetric([]widgetAccount{acct}, name, window.Start, window.End, window.Previous)
		}
		accounts = append(accounts, acct)
	}
	writePerformanceSummary(w, accounts, window, true)
}
func performancePlatformMatches(platform string, selected []string) bool {
	if len(selected) == 0 {
		return true
	}
	for _, name := range selected {
		if name == platform {
			return true
		}
	}
	return false
}
