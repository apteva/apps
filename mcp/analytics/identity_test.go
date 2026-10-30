package main

import (
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	tk "github.com/apteva/app-sdk/testkit"
)

func webEvent(t *testing.T, db sqlDatabase, visitor, topic string, props map[string]any) int64 {
	t.Helper()
	props["visitor_id"] = visitor
	raw, _ := json.Marshal(props)
	id, err := insertEvent(db, EventInsert{ProjectID: "p1", App: "site", Topic: topic, Source: "web", Props: string(raw)})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestIdentifyBackfillsVisitorAndDerivesAttributionAcrossDevices(t *testing.T) {
	db := testDashboardDB(t)
	// Laptop: arrived from a newsletter, then later from a paid ad.
	webEvent(t, db, "s_laptop1", "page_view", map[string]any{
		"utm_source": "newsletter", "utm_medium": "email", "touch_ts": 1000, "touch_landing": "/blog",
		"first_utm_source": "newsletter", "first_utm_medium": "email", "first_touch_ts": 1000, "first_landing": "/blog",
	})
	webEvent(t, db, "s_laptop1", "page_view", map[string]any{
		"utm_source": "google", "utm_medium": "cpc", "utm_campaign": "brand", "touch_ts": 5000,
		"first_utm_source": "newsletter", "first_utm_medium": "email", "first_touch_ts": 1000,
	})
	// Phone: organic referral, earlier than anything on the laptop.
	webEvent(t, db, "s_phone01", "page_view", map[string]any{
		"touch_referrer": "news.ycombinator.com", "touch_ts": 500, "first_referrer": "news.ycombinator.com", "first_touch_ts": 500,
	})
	// Someone else's visitor must stay untouched.
	other := webEvent(t, db, "s_stranger", "page_view", map[string]any{})

	res, err := identify(db, "p1", "user-1", "s_laptop1", []string{"cus_123", "user-1"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Backfilled != 2 || len(res.Aliases) != 1 || res.Aliases[0] != "cus_123" {
		t.Fatalf("identify laptop: %+v", res)
	}
	if res.Attribution.FirstTouch == nil || res.Attribution.FirstTouch.Source != "newsletter" || res.Attribution.FirstTouch.Landing != "/blog" {
		t.Fatalf("first touch: %+v", res.Attribution.FirstTouch)
	}
	if res.Attribution.LastTouch == nil || res.Attribution.LastTouch.Campaign != "brand" {
		t.Fatalf("last touch: %+v", res.Attribution.LastTouch)
	}
	if _, err := identify(db, "p1", "user-1", "s_phone01", nil); err != nil {
		t.Fatal(err)
	}
	got, err := userAttribution(db, "p1", resolveUser(db, "p1", "cus_123"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Visitors) != 2 || got.FirstTouch.Referrer != "news.ycombinator.com" || got.FirstTouch.VisitorID != "s_phone01" {
		t.Fatalf("cross-device attribution: %+v first=%+v", got, got.FirstTouch)
	}
	if got.LastTouch.Source != "google" {
		t.Fatalf("cross-device last touch: %+v", got.LastTouch)
	}
	var strangerUser *string
	if err := db.QueryRow(`SELECT user_id FROM events WHERE id = ?`, other).Scan(&strangerUser); err != nil || strangerUser != nil {
		t.Fatalf("stranger backfilled: %v %v", strangerUser, err)
	}
}

func TestIdentifyRelinkKeepsPreviousUsersEvents(t *testing.T) {
	db := testDashboardDB(t)
	webEvent(t, db, "s_shared1", "page_view", map[string]any{})
	if _, err := identify(db, "p1", "alice", "s_shared1", nil); err != nil {
		t.Fatal(err)
	}
	webEvent(t, db, "s_shared1", "page_view", map[string]any{})
	res, err := identify(db, "p1", "bob", "s_shared1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.PreviousUserID != "alice" || res.Backfilled != 1 {
		t.Fatalf("relink: %+v", res)
	}
	var alice int
	if err := db.QueryRow(`SELECT COUNT(*) FROM events WHERE user_id = 'alice'`).Scan(&alice); err != nil || alice != 1 {
		t.Fatalf("alice kept %d events (%v)", alice, err)
	}
	if linkedUserID(db, "p1", "s_shared1") != "bob" || linkedUserID(db, "p2", "s_shared1") != "" {
		t.Fatal("link not project-scoped to the latest user")
	}
}

func TestVisitorLookupsUseWebVisitorIndex(t *testing.T) {
	db := testDashboardDB(t)
	rows, err := db.Query(`EXPLAIN QUERY PLAN UPDATE events SET user_id = ? WHERE `+webVisitorPredicate+` = ? AND (user_id IS NULL OR user_id = '')`, "u", "p1", "s_x")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan strings.Builder
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan.WriteString(detail + "\n")
	}
	if !strings.Contains(plan.String(), "ix_events_web_visitor") {
		t.Fatalf("backfill does not use the visitor index:\n%s", plan.String())
	}
}

func TestAttributionReportJoinsLifecycleEventsThroughAliases(t *testing.T) {
	db := testDashboardDB(t)
	webEvent(t, db, "s_ads00001", "page_view", map[string]any{"first_utm_source": "google", "first_touch_ts": 10, "utm_source": "google", "touch_ts": 10})
	webEvent(t, db, "s_mail0001", "page_view", map[string]any{"first_utm_source": "newsletter", "first_touch_ts": 10, "utm_source": "newsletter", "touch_ts": 10})
	webEvent(t, db, "s_direct01", "page_view", map[string]any{})
	for _, link := range []struct{ user, visitor, alias string }{{"u1", "s_ads00001", "cus_1"}, {"u2", "s_mail0001", "cus_2"}, {"u3", "s_direct01", "cus_3"}} {
		if _, err := identify(db, "p1", link.user, link.visitor, []string{link.alias}); err != nil {
			t.Fatal(err)
		}
	}
	// Billing knows customers, not users or visitors.
	for _, customer := range []string{"cus_1", "cus_1", "cus_2", "cus_3", "cus_unknown"} {
		raw, _ := json.Marshal(map[string]any{"customer_id": customer})
		if _, err := insertEvent(db, EventInsert{ProjectID: "p1", App: "billing", Topic: "subscription.active", Source: "auto", Props: string(raw)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := insertEvent(db, EventInsert{ProjectID: "p1", App: "billing", Topic: "subscription.active", Source: "auto", Props: `{}`}); err != nil {
		t.Fatal(err)
	}
	out, err := attributionReport(db, Filter{ProjectID: "p1", Topic: "subscription.active"}, "props.customer_id", "first", "source", 0)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][2]int64{}
	for _, row := range out["rows"].([]attributionRow) {
		got[row.Value] = [2]int64{row.Events, int64(row.Users)}
	}
	// cus_3 is a direct visitor and cus_unknown was never identified: no touch.
	want := map[string][2]int64{"google": {2, 1}, "newsletter": {1, 1}, attributionNone: {2, 2}}
	if len(got) != len(want) {
		t.Fatalf("rows = %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("rows = %v, want %v", got, want)
		}
	}
	if out["events"].(int64) != 6 || out["events_without_actor"].(int64) != 1 {
		t.Fatalf("totals: %v", out)
	}
	if _, err := attributionReport(db, Filter{ProjectID: "p1"}, "customer_id; DROP TABLE events", "first", "source", 0); err == nil {
		t.Fatal("accepted unsafe actor_key")
	}
	if _, err := attributionReport(db, Filter{ProjectID: "p1"}, "user_id", "middle", "source", 0); err == nil {
		t.Fatal("accepted unknown touch")
	}
}

func TestIdentifyToolValidatesInput(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"))
	a := &App{}
	for name, args := range map[string]map[string]any{
		"missing user":     {"visitor_id": "s_abcdefgh"},
		"foreign project":  {"user_id": "u", "project_id": "p2"},
		"long visitor":     {"user_id": "u", "visitor_id": strings.Repeat("v", 300)},
		"aliases not list": {"user_id": "u", "aliases": "cus_1"},
		"alias not string": {"user_id": "u", "aliases": []any{1}},
		"too many aliases": {"user_id": "u", "aliases": make([]any, maxIdentityAliases+1)},
	} {
		if _, err := a.toolIdentify(ctx, args); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	out, err := a.toolIdentify(ctx, map[string]any{"user_id": " u1 ", "visitor_id": "s_abcdefgh", "aliases": []any{"cus_9"}})
	if err != nil {
		t.Fatal(err)
	}
	if res := out.(IdentifyResult); res.UserID != "u1" || res.Aliases[0] != "cus_9" {
		t.Fatalf("identify result: %+v", res)
	}
	attr, err := a.toolUserAttribution(ctx, map[string]any{"user_id": "cus_9"})
	if err != nil || attr.(UserAttribution).UserID != "u1" {
		t.Fatalf("alias lookup: %+v %v", attr, err)
	}
}

func TestCollectAttributionLinkedUserAndRetries(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"))
	old := globalCtx
	globalCtx = ctx
	defer func() { globalCtx = old }()
	wk, err := createWriteKey(ctx.AppDB(), "site", "p1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := identify(ctx.AppDB(), "p1", "user-7", "s_visitor7", nil); err != nil {
		t.Fatal(err)
	}
	extra, _ := json.Marshal(map[string]any{
		"visitor_id":    "s_visitor7",
		"utm_campaign":  strings.Repeat("c", 500),
		"first_landing": "/signup?invite=secret",
	})
	q := url.Values{"k": {wk.Key}, "eid": {"retry-1"}, "e": {"page_view"}, "uid": {"spoofed"}, "p": {string(extra)}, "age": {"60000"}}
	collect := func(q url.Values) string {
		rr := httptest.NewRecorder()
		(&App{}).handleCollect(rr, httptest.NewRequest("GET", "/collect?"+q.Encode(), nil))
		return rr.Header().Get("X-Apa")
	}
	before := time.Now().UnixMilli()
	if got := collect(q); got != "ok" {
		t.Fatalf("collect: %q", got)
	}
	rows, err := queryRows(ctx.AppDB(), Filter{ProjectID: "p1"}, 10)
	if err != nil || len(rows) != 1 {
		t.Fatal(len(rows), err)
	}
	row := rows[0]
	if row.UserID != "user-7" {
		t.Fatalf("linked visitor recorded as %q", row.UserID)
	}
	if d := before - row.TS; d < 59000 || d > 65000 {
		t.Fatalf("queued event dated %d ms back, want ~60000", d)
	}
	var props map[string]any
	_ = json.Unmarshal(row.Props, &props)
	if len(props["utm_campaign"].(string)) != maxAttributionValue || props["first_landing"] != "/signup" {
		t.Fatalf("attribution props not sanitized: %v", props)
	}
	// The same delivery arriving again from the retry queue is a duplicate.
	q.Set("age", "120000")
	if got := collect(q); got != "duplicate" {
		t.Fatalf("retry of delivered event: %q", got)
	}
	// Ages beyond the retry window are ignored, not trusted.
	q.Set("eid", "retry-2")
	q.Set("age", "999999999999")
	if got := collect(q); got != "ok" {
		t.Fatalf("collect old age: %q", got)
	}
	rows, _ = queryRows(ctx.AppDB(), Filter{ProjectID: "p1"}, 10)
	for _, r := range rows {
		if r.TS < before-maxRetryAge.Milliseconds()-1000 {
			t.Fatalf("event dated beyond retry window: %d", r.TS)
		}
	}
}
