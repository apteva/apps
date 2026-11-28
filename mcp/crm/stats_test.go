package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

var statsNow = time.Date(2026, 11, 28, 15, 0, 0, 0, time.UTC)

type statsFixture struct {
	ctx *sdk.AppCtx
	t   *testing.T
}

func (f statsFixture) contact(pid, email, createdAt string, automated bool) int64 {
	f.t.Helper()
	c := mustCreate(f.t, f.ctx, map[string]any{"primary_email": email, "_project_id": pid})
	if _, err := f.ctx.AppDB().Exec(`UPDATE contacts SET created_at=?, source='import', primary_email=? WHERE id=?`, createdAt, email, c.ID); err != nil {
		f.t.Fatal(err)
	}
	if automated {
		if _, err := f.ctx.AppDB().Exec(`INSERT INTO contact_tags(project_id, contact_id, tag_name) VALUES (?,?,?)`, pid, c.ID, tagAutomated); err != nil {
			f.t.Fatal(err)
		}
	}
	return c.ID
}

func (f statsFixture) conversation(pid string, contactID int64, startedAt, status, changedAt string) {
	f.t.Helper()
	id := mkConversation(f.t, f.ctx, pid, contactID, "email")
	var changed any
	if changedAt != "" {
		changed = changedAt
	}
	if _, err := f.ctx.AppDB().Exec(`UPDATE contact_conversations SET started_at=?, status=?, status_changed_at=? WHERE id=?`, startedAt, status, changed, id); err != nil {
		f.t.Fatal(err)
	}
}

func (f statsFixture) activity(pid string, contactID int64, kind, at string) {
	f.t.Helper()
	if _, err := f.ctx.AppDB().Exec(`INSERT INTO contact_activities(project_id, contact_id, kind, body, occurred_at, source) VALUES (?,?,?,?,?,'test')`,
		pid, contactID, kind, "x", at); err != nil {
		f.t.Fatal(err)
	}
}

// Alpha: two people this week, one older person, one automated sender.
// Beta: one person this week. Hidden: one person (never listed).
func newStatsFixture(t *testing.T) statsFixture {
	t.Helper()
	pf := &projectsPlatform{projects: []sdk.PlatformProject{{ID: "proj-alpha", Name: "Alpha"}, {ID: "proj-beta", Name: "Beta"}}}
	ctx := newTestCtx(t, tk.WithPlatform(pf))
	t.Setenv("APTEVA_PROJECT_ID", "")
	globalCtx = ctx
	f := statsFixture{ctx: ctx, t: t}

	ana := f.contact("proj-alpha", "ana@alpha.test", "2026-11-27T10:00:00Z", false)
	ben := f.contact("proj-alpha", "ben@alpha.test", "2026-11-22T08:00:00Z", false)
	old := f.contact("proj-alpha", "old@alpha.test", "2026-11-18T09:00:00Z", false) // previous 7-day window
	bot := f.contact("proj-alpha", "news@alpha.test", "2026-11-28T07:00:00Z", true)
	cleo := f.contact("proj-beta", "cleo@beta.test", "2026-11-28T09:30:00Z", false)
	hid := f.contact("proj-hidden", "x@hidden.test", "2026-11-28T09:00:00Z", false)

	f.activity("proj-alpha", ana, "email_received", "2026-11-27T10:00:00Z")
	f.activity("proj-alpha", ana, "email_sent", "2026-11-27T11:00:00Z")
	f.activity("proj-alpha", ben, "sms_received", "2026-11-22T08:05:00Z")
	f.activity("proj-alpha", ben, "sms_send_failed", "2026-11-23T08:05:00Z")
	f.activity("proj-alpha", old, "email_received", "2026-11-19T09:00:00Z") // previous window
	f.activity("proj-alpha", bot, "email_received", "2026-11-28T07:00:00Z")
	f.activity("proj-alpha", ana, "note", "2026-11-27T12:00:00Z") // not a message
	f.activity("proj-beta", cleo, "whatsapp_received", "2026-11-28T09:30:00Z")
	f.activity("proj-hidden", hid, "email_received", "2026-11-28T09:00:00Z")

	f.conversation("proj-alpha", ana, "2026-11-27T10:00:00Z", "open", "")
	f.conversation("proj-alpha", ben, "2026-11-22T08:05:00Z", "closed", "2026-11-24T10:00:00Z")
	f.conversation("proj-alpha", old, "2026-11-19T09:00:00Z", "closed", "2026-11-20T10:00:00Z")
	f.conversation("proj-alpha", bot, "2026-11-28T07:00:00Z", "open", "")
	f.conversation("proj-beta", cleo, "2026-11-28T09:30:00Z", "open", "")
	return f
}

func TestActivityStats_TotalsPreviousAndDaily(t *testing.T) {
	f := newStatsFixture(t)
	stats, err := dbActivityStats(f.ctx.AppDB(), []string{"proj-alpha"}, 7, false, statsNow)
	if err != nil {
		t.Fatal(err)
	}
	want := statsTotals{NewContacts: 3, MessagesReceived: 3, MessagesSent: 1, SendFailures: 1, ConversationsOpened: 3, ConversationsClosed: 1}
	if stats.Totals != want {
		t.Fatalf("totals=%+v want %+v", stats.Totals, want)
	}
	prev := statsTotals{NewContacts: 1, MessagesReceived: 1, ConversationsOpened: 1, ConversationsClosed: 1}
	if stats.Previous != prev {
		t.Fatalf("previous=%+v want %+v", stats.Previous, prev)
	}
	if stats.OpenNow != 2 || stats.Since != "2026-11-22T00:00:00Z" || stats.Until != "2026-11-29T00:00:00Z" {
		t.Fatalf("open=%d since=%s until=%s", stats.OpenNow, stats.Since, stats.Until)
	}
	if len(stats.Daily) != 7 || stats.Daily[0].Date != "2026-11-22" || stats.Daily[6].Date != "2026-11-28" {
		t.Fatalf("daily=%+v", stats.Daily)
	}
	if d := stats.Daily[0]; d.NewContacts != 1 || d.MessagesReceived != 1 {
		t.Fatalf("2026-11-22=%+v", d)
	}
	if d := stats.Daily[5]; d.NewContacts != 1 || d.MessagesReceived != 1 || d.MessagesSent != 1 {
		t.Fatalf("2026-11-27=%+v", d)
	}
	if len(stats.LatestContacts) != 4 || stats.LatestContacts[0].Email != "news@alpha.test" || stats.LatestContacts[0].Source != "import" {
		t.Fatalf("latest=%+v", stats.LatestContacts)
	}
}

func TestActivityStats_HideAutomated(t *testing.T) {
	f := newStatsFixture(t)
	stats, err := dbActivityStats(f.ctx.AppDB(), []string{"proj-alpha"}, 7, true, statsNow)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Totals.NewContacts != 2 || stats.Totals.MessagesReceived != 2 || stats.Totals.ConversationsOpened != 2 || stats.OpenNow != 1 {
		t.Fatalf("totals=%+v open=%d", stats.Totals, stats.OpenNow)
	}
	if stats.Daily[6].MessagesReceived != 0 || stats.LatestContacts[0].Email != "ana@alpha.test" {
		t.Fatalf("automated sender still counted: day=%+v latest=%+v", stats.Daily[6], stats.LatestContacts[0])
	}
}

func TestActivityStats_LongerPeriodsAndEmptyProjects(t *testing.T) {
	f := newStatsFixture(t)
	stats, err := dbActivityStats(f.ctx.AppDB(), []string{"proj-alpha"}, 30, false, statsNow)
	if err != nil {
		t.Fatal(err)
	}
	if len(stats.Daily) != 30 || stats.Totals.NewContacts != 4 || stats.Previous.NewContacts != 0 {
		t.Fatalf("30d: days=%d totals=%+v previous=%+v", len(stats.Daily), stats.Totals, stats.Previous)
	}
	empty, err := dbActivityStats(f.ctx.AppDB(), nil, 7, false, statsNow)
	if err != nil || len(empty.Daily) != 7 || empty.Totals != (statsTotals{}) || len(empty.LatestContacts) != 0 {
		t.Fatalf("empty=%+v err=%v", empty, err)
	}
}

type statsResponse struct {
	Stats             activityStats  `json:"stats"`
	Projects          []inboxProject `json:"projects"`
	SelectedProjectID string         `json:"selected_project_id"`
}

func statsGet(t *testing.T, target string, headers map[string]string) (*httptest.ResponseRecorder, statsResponse) {
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
	(&App{}).handleHTTPStats(rec, req)
	var out statsResponse
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode: %v body=%s", err, rec.Body.String())
		}
	}
	return rec, out
}

func TestStatsHTTP_GlobalAndProjectScopes(t *testing.T) {
	newStatsFixture(t)
	// Real clock: the fixture dates are recent enough for a 90-day window.
	rec, all := statsGet(t, "/stats?scope=global&period=90", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("global status=%d body=%s", rec.Code, rec.Body.String())
	}
	if all.Stats.Totals.NewContacts != 5 || len(all.Projects) != 2 {
		t.Fatalf("global totals=%+v projects=%+v (hidden project must not count)", all.Stats.Totals, all.Projects)
	}
	for _, c := range all.Stats.LatestContacts {
		if c.ProjectID == "" || c.ProjectName == "" {
			t.Fatalf("global contact without project: %+v", c)
		}
	}
	rec, beta := statsGet(t, "/stats?scope=global&period=90&project_id=proj-beta", nil)
	if rec.Code != http.StatusOK || beta.Stats.Totals.NewContacts != 1 || beta.SelectedProjectID != "proj-beta" {
		t.Fatalf("beta status=%d totals=%+v", rec.Code, beta.Stats.Totals)
	}
	rec, project := statsGet(t, "/stats?project_id=proj-alpha&period=90&hide_automated=1", map[string]string{"X-User-ID": ""})
	if rec.Code != http.StatusOK || project.Stats.Totals.NewContacts != 3 || !project.Stats.HideAutomated || project.Projects != nil {
		t.Fatalf("project status=%d totals=%+v body=%s", rec.Code, project.Stats.Totals, rec.Body.String())
	}
	for _, c := range project.Stats.LatestContacts {
		if c.ProjectID != "" {
			t.Fatalf("project contact carries project_id: %+v", c)
		}
	}
}

func TestStatsHTTP_Refusals(t *testing.T) {
	newStatsFixture(t)
	cases := []struct {
		name    string
		target  string
		headers map[string]string
		want    int
	}{
		{"hidden project", "/stats?scope=global&project_id=proj-hidden", nil, http.StatusForbidden},
		{"no user", "/stats?scope=global", map[string]string{"X-User-ID": ""}, http.StatusUnauthorized},
		{"delegated", "/stats?scope=global", map[string]string{"X-Apteva-Issuer-App": "portal", "X-Apteva-Issuer-Install-ID": "9"}, http.StatusForbidden},
		{"bad scope", "/stats?scope=other&project_id=proj-alpha", nil, http.StatusBadRequest},
		{"bad period", "/stats?project_id=proj-alpha&period=14", nil, http.StatusBadRequest},
	}
	for _, tc := range cases {
		if rec, _ := statsGet(t, tc.target, tc.headers); rec.Code != tc.want {
			t.Fatalf("%s: status=%d want %d body=%s", tc.name, rec.Code, tc.want, rec.Body.String())
		}
	}
}
