package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func membersPage(next string, total int, members ...[2]string) *sdk.ExecuteResult {
	items := []any{}
	for _, m := range members {
		items = append(items, map[string]any{"snippet": map[string]any{"creatorChannelId": m[0], "membershipsDetails": map[string]any{"highestAccessibleLevel": m[1], "highestAccessibleLevelDisplayName": map[string]string{"level-1": "Supporters", "level-2": "Gold"}[m[1]]}}})
	}
	body := map[string]any{"items": items, "pageInfo": map[string]any{"totalResults": total, "resultsPerPage": 1000}}
	if next != "" {
		body["nextPageToken"] = next
	}
	raw, _ := json.Marshal(body)
	return &sdk.ExecuteResult{Success: true, Status: 200, Data: raw}
}

func supporters(t *testing.T, a *App, ctx *sdk.AppCtx, id int64, refresh bool) supporterSnapshot {
	t.Helper()
	out, err := a.toolAccountSupporters(ctx, map[string]any{"social_account_id": id, "refresh": refresh})
	if err != nil {
		t.Fatal(err)
	}
	snap, ok := out.(supporterSnapshot)
	if !ok {
		t.Fatalf("unexpected result %#v", out)
	}
	return snap
}

func TestSupportersCountsEveryPageAndLevel(t *testing.T) {
	a, ctx, pf, id := membershipFixture(t)
	pf.executeQueues["list_members"] = []*sdk.ExecuteResult{
		membersPage("next-1", 3, [2]string{"UCone", "level-1"}, [2]string{"UCone", "level-1"}),
		membersPage("", 3, [2]string{"UCone", "level-2"}),
	}
	snap := supporters(t, a, ctx, id, true)
	if snap.State != "available" || snap.Total == nil || *snap.Total != 3 || !snap.Complete || snap.TotalsAgree == nil || !*snap.TotalsAgree {
		t.Fatalf("snapshot = %+v", snap)
	}
	if len(snap.Levels) != 2 || snap.Levels[0] != (supporterLevelCount{ID: "level-1", Name: "Supporters", Count: 2}) || snap.Levels[1] != (supporterLevelCount{ID: "level-2", Name: "Gold", Count: 1}) {
		t.Fatalf("levels = %+v", snap.Levels)
	}
	var pages []map[string]any
	for _, c := range pf.executeCalls {
		if c.Tool == "list_members" {
			pages = append(pages, c.Input)
		}
		if c.Tool != "list_members" && c.Tool != "list_membership_levels" && c.Tool != "get_my_channel" {
			t.Fatalf("supporter count made a non-read call: %s", c.Tool)
		}
	}
	if len(pages) != 2 || pages[0]["mode"] != "all_current" || pages[1]["pageToken"] != "next-1" {
		t.Fatalf("pages = %+v", pages)
	}
	// The count is an ordinary snapshot metric with a supporter_level breakdown.
	history := loadAccountMetricHistory(ctx, "test-proj", id, 30)
	if points := history["supporters"]; len(points) != 1 || points[0].Value != 3 {
		t.Fatalf("supporters series = %+v", history["supporters"])
	}
	b := supporterBreakdown(&snap)
	if b.Status != "ok" || len(b.Rows) != 2 || b.Rows[0].Dimensions["supporter_level"] != "Supporters" || b.Rows[0].Metrics["supporters"] != 2 {
		t.Fatalf("breakdown = %+v", b)
	}
}

func TestSupportersStatesNeverReportZero(t *testing.T) {
	cases := []struct {
		name, state string
		res         *sdk.ExecuteResult
	}{
		{"scope missing", "scope_missing", &sdk.ExecuteResult{Success: false, Status: 403, Data: json.RawMessage(`{"error":{"errors":[{"reason":"insufficientPermissions"}]}}`)}},
		{"not allowlisted", "access_required", &sdk.ExecuteResult{Success: false, Status: 403, Data: json.RawMessage(`{"error":{"errors":[{"reason":"forbidden"}]}}`)}},
		{"old catalog", "unsupported", &sdk.ExecuteResult{Success: false, Status: 404, Data: json.RawMessage(`{"error":"tool not found: list_members"}`)}},
		{"transient", "error", &sdk.ExecuteResult{Success: false, Status: 500, Data: json.RawMessage(`{"error":"backend"}`)}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a, ctx, pf, id := membershipFixture(t)
			pf.executeResponses["list_members"] = c.res
			snap := supporters(t, a, ctx, id, true)
			if snap.State != c.state || snap.Total != nil || snap.Reason == "" {
				t.Fatalf("snapshot = %+v", snap)
			}
			if c.state == "scope_missing" && !strings.Contains(snap.Reason, "Reconnect") {
				t.Fatalf("scope_missing must say to reconnect: %q", snap.Reason)
			}
			if history := loadAccountMetricHistory(ctx, "test-proj", id, 30); len(history["supporters"]) != 0 {
				t.Fatalf("a failed read wrote a supporters point: %+v", history["supporters"])
			}
			if b := supporterBreakdown(&snap); b.Status != c.state || len(b.Rows) != 0 {
				t.Fatalf("breakdown = %+v", b)
			}
		})
	}
}

func TestSupportersZeroMembersIsARealZero(t *testing.T) {
	a, ctx, pf, id := membershipFixture(t)
	pf.executeResponses["list_members"] = membersPage("", 0)
	snap := supporters(t, a, ctx, id, true)
	if snap.State != "available" || snap.Total == nil || *snap.Total != 0 || !snap.Complete {
		t.Fatalf("snapshot = %+v", snap)
	}
	if len(snap.Levels) != 1 || snap.Levels[0].Count != 0 {
		t.Fatalf("known tiers should be listed with zero: %+v", snap.Levels)
	}
	var stored any
	ctx.AppDB().QueryRow(`SELECT total FROM social_supporter_snapshots WHERE social_account_id=?`, id).Scan(&stored)
	if stored == nil {
		t.Fatal("zero members stored as NULL")
	}
}

func TestSupportersTransientErrorKeepsLastGoodCount(t *testing.T) {
	a, ctx, pf, id := membershipFixture(t)
	pf.executeResponses["list_members"] = membersPage("", 1, [2]string{"UCone", "level-1"})
	supporters(t, a, ctx, id, true)
	pf.executeResponses["list_members"] = &sdk.ExecuteResult{Success: false, Status: 500, Data: json.RawMessage(`{"error":"backend"}`)}
	supporters(t, a, ctx, id, true)
	latest := supporters(t, a, ctx, id, false)
	if latest.State != "error" || latest.Total == nil || *latest.Total != 1 {
		t.Fatalf("latest = %+v", latest)
	}
}

func TestSupportersPageBudgetIsPartialAndKeptOutOfHistory(t *testing.T) {
	_, ctx, pf, id := membershipFixture(t)
	pf.executeResponses["list_members"] = membersPage("more", 5000, [2]string{"UCone", "level-1"})
	account, _ := loadAudienceAccount(ctx, "test-proj", id)
	count, err := countYouTubeSupporters(ctx, account, 2)
	if err != nil || count.Complete || count.Total != 2 {
		t.Fatalf("count=%+v err=%v", count, err)
	}
	snap := supporterSnapshot{SocialAccountID: id, Platform: "youtube", State: "available", Source: "youtube.members", ObservedAt: time.Now().UTC().Format(time.RFC3339), Total: &count.Total, Complete: false, Levels: count.Levels}
	if err := persistSupporterSnapshot(ctx, "test-proj", snap); err != nil {
		t.Fatal(err)
	}
	if history := loadAccountMetricHistory(ctx, "test-proj", id, 30); len(history["supporters"]) != 0 {
		t.Fatalf("partial count entered the series: %+v", history["supporters"])
	}
	if b := supporterBreakdown(&snap); b.Status != "partial" {
		t.Fatalf("breakdown = %+v", b)
	}
}

func TestSupportersRejectMembersOfAnotherChannel(t *testing.T) {
	a, ctx, pf, id := membershipFixture(t)
	pf.executeResponses["list_members"] = membersPage("", 1, [2]string{"UCother", "level-1"})
	if snap := supporters(t, a, ctx, id, true); snap.State != "error" || snap.Total != nil {
		t.Fatalf("snapshot = %+v", snap)
	}
}

func TestSupportersUnsupportedPlatformStoresNothing(t *testing.T) {
	_, ctx, _, _ := membershipFixture(t)
	res, _ := ctx.AppDB().Exec(`INSERT INTO social_accounts(project_id,platform,connection_id,display_name,status) VALUES('test-proj','facebook',7,'Page','active')`)
	fb, _ := res.LastInsertId()
	snap := supporters(t, &App{}, ctx, fb, true)
	var rows int
	ctx.AppDB().QueryRow(`SELECT count(*) FROM social_supporter_snapshots`).Scan(&rows)
	if snap.State != "unsupported" || snap.Total != nil || rows != 0 {
		t.Fatalf("snapshot=%+v rows=%d", snap, rows)
	}
}

func TestSupportersDailyRefreshAndAnalyticsSeparation(t *testing.T) {
	a, ctx, pf, id := membershipFixture(t)
	pf.executeResponses["list_members"] = membersPage("", 1, [2]string{"UCone", "level-1"})
	a.refreshSupportersIfDue(ctx, "test-proj", id)
	a.refreshSupportersIfDue(ctx, "test-proj", id)
	calls := 0
	for _, c := range pf.executeCalls {
		if c.Tool == "list_members" {
			calls++
		}
	}
	if calls != 1 {
		t.Fatalf("fresh snapshot was recounted: %d calls", calls)
	}
	// Supporter points must not look like provider analytics breakdowns.
	if due, err := accountBreakdownsDue(ctx, "test-proj", id, 24*time.Hour); err != nil || !due {
		t.Fatalf("supporter points suppressed the analytics breakdown refresh: due=%v err=%v", due, err)
	}
	if b := loadLatestAccountBreakdowns(ctx, "test-proj", id); len(b) != 0 {
		t.Fatalf("supporter points surfaced as provider breakdowns: %+v", b)
	}
	out := accountMetricsResult{SocialAccountID: id, Platform: "youtube"}
	applySupporterMetrics(ctx, "test-proj", &out, "native")
	if out.Supporters != 1 || out.SupportersState != "available" || !accountMetricKnown(out, "supporters") || accountMetricValue(out, "supporters") != 1 {
		t.Fatalf("metrics = %+v", out)
	}
	if canonicalDimension("membership_level") != "supporter_level" {
		t.Fatal("supporter_level dimension alias missing")
	}
}
