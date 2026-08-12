package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type campaignsPlatform struct {
	tk.BasePlatformClient
	calls      []campaignsPlatformCall
	callErrors map[string]error
	beforeCall func(appName, tool string, input map[string]any)
}

type campaignsPlatformCall struct {
	App   string
	Tool  string
	Input map[string]any
}

func (p *campaignsPlatform) CallAppResult(appName, tool string, input map[string]any, out any) error {
	p.calls = append(p.calls, campaignsPlatformCall{App: appName, Tool: tool, Input: input})
	if p.beforeCall != nil {
		p.beforeCall(appName, tool, input)
	}
	if err := p.callErrors[appName+"."+tool]; err != nil {
		return err
	}
	var reply any = map[string]any{}
	switch tool {
	case "segments_eval":
		reply = map[string]any{"contact_ids": []int64{101, 102}, "count": 2}
	case "lists_eval":
		reply = map[string]any{"contact_ids": []int64{201, 202, 203}, "count": 3}
	case "send_message":
		reply = map[string]any{"id": 777, "status": "sent", "provider_message_id": "provider-777"}
	case "suppression_check":
		reply = map[string]any{"suppressed": false}
	case "jobs_schedule":
		reply = map[string]any{"job": map[string]any{"id": 123}}
	case "message_get":
		reply = map[string]any{
			"found":   true,
			"message": map[string]any{"id": input["id"], "status": "opened"},
			"events": []map[string]any{
				{"message_id": input["id"], "kind": "delivered", "occurred_at": "2026-07-02T12:00:00Z"},
				{"message_id": input["id"], "kind": "opened", "occurred_at": "2026-07-02T12:01:00Z"},
			},
		}
	}
	if out == nil {
		return nil
	}
	b, err := json.Marshal(reply)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

func (p *campaignsPlatform) CallApp(appName, tool string, input map[string]any) (json.RawMessage, error) {
	p.calls = append(p.calls, campaignsPlatformCall{App: appName, Tool: tool, Input: input})
	return json.RawMessage(`{"ok":true}`), nil
}

func (p *campaignsPlatform) PlatformInfo() (*sdk.PlatformInfo, error) {
	return &sdk.PlatformInfo{PublicURL: "https://agents.example.test"}, nil
}

func (p *campaignsPlatform) callsTo(app, tool string) []campaignsPlatformCall {
	out := []campaignsPlatformCall{}
	for _, c := range p.calls {
		if c.App == app && c.Tool == tool {
			out = append(out, c)
		}
	}
	return out
}

func newCampaignsTestCtx(t *testing.T, platform *campaignsPlatform, opts ...tk.Option) *sdk.AppCtx {
	t.Helper()
	full := []tk.Option{tk.WithProjectID("test-proj")}
	if platform != nil {
		full = append(full, tk.WithPlatform(platform))
	}
	full = append(full, opts...)
	return tk.NewAppCtx(t, "apteva.yaml", full...)
}

func TestEmbeddedManifestParses(t *testing.T) {
	manifest := (&App{}).Manifest()
	if manifest.Name != "campaigns" {
		t.Fatalf("manifest name=%q, want campaigns", manifest.Name)
	}
	if manifest.Version == "" {
		t.Fatal("manifest version is empty")
	}
}

func TestSchedulerToolsAreAppOnly(t *testing.T) {
	want := map[string]bool{
		"campaigns_materialise": false,
		"campaigns_tick":        false,
	}
	for _, tool := range (&App{}).MCPTools() {
		if _, ok := want[tool.Name]; !ok {
			continue
		}
		if tool.Exposure != sdk.ToolExposureAppOnly {
			t.Errorf("tool %s exposure=%q, want app_only", tool.Name, tool.Exposure)
		}
		want[tool.Name] = true
	}
	for name, found := range want {
		if !found {
			t.Errorf("MCPTools missing private scheduler tool %s", name)
		}
	}

	manifestWant := map[string]bool{
		"campaigns_materialise": false,
		"campaigns_tick":        false,
	}
	for _, tool := range (&App{}).Manifest().Provides.MCPTools {
		if _, ok := manifestWant[tool.Name]; !ok {
			continue
		}
		if tool.Exposure != sdk.ToolExposureAppOnly {
			t.Errorf("manifest tool %s exposure=%q, want app_only", tool.Name, tool.Exposure)
		}
		manifestWant[tool.Name] = true
	}
	for name, found := range manifestWant {
		if !found {
			t.Errorf("manifest missing private scheduler tool %s", name)
		}
	}
}

func TestCampaignCreateEventIncludesCampaignIDAndStatus(t *testing.T) {
	recorder := tk.NewEmitRecorder()
	ctx := newCampaignsTestCtx(t, nil, tk.WithEmitter(recorder))

	out, err := (&App{}).toolCampaignsCreate(ctx, map[string]any{
		"_project_id": "test-proj",
		"name":        "Launch",
		"channel":     ChannelEmail,
		"subject":     "Hello",
		"body_text":   "Body",
		"segment_id":  int64(42),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	campaign := out.(map[string]any)["campaign"].(*Campaign)
	events := recorder.EventsByTopic("campaign.created")
	if len(events) != 1 {
		t.Fatalf("campaign.created events=%d, want 1", len(events))
	}
	data, ok := events[0].Data.(map[string]any)
	if !ok {
		t.Fatalf("event data type=%T, want map[string]any", events[0].Data)
	}
	if data["campaign_id"] != campaign.ID || data["id"] != campaign.ID {
		t.Fatalf("event campaign ids=%#v, want %d", data, campaign.ID)
	}
	if data["status"] != StatusDraft || data["name"] != "Launch" {
		t.Fatalf("event data=%#v, want draft Launch", data)
	}
	if !campaign.OpenTracking || !campaign.ClickTracking {
		t.Fatalf("tracking defaults open=%v click=%v, want both true", campaign.OpenTracking, campaign.ClickTracking)
	}
}

func TestCampaignCreateCanDisableTracking(t *testing.T) {
	ctx := newCampaignsTestCtx(t, nil)

	out, err := (&App{}).toolCampaignsCreate(ctx, map[string]any{
		"_project_id":    "test-proj",
		"name":           "No tracking",
		"channel":        ChannelEmail,
		"subject":        "Hello",
		"body_text":      "Body",
		"segment_id":     int64(42),
		"open_tracking":  false,
		"click_tracking": false,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	campaign := out.(map[string]any)["campaign"].(*Campaign)
	if campaign.OpenTracking || campaign.ClickTracking {
		t.Fatalf("tracking open=%v click=%v, want both false", campaign.OpenTracking, campaign.ClickTracking)
	}
}

func TestTickEmailCampaignAddsUnsubscribeLinkAndToken(t *testing.T) {
	platform := &campaignsPlatform{}
	ctx := newCampaignsTestCtx(t, platform)
	c, err := dbCampaignCreate(ctx.AppDB(), "test-proj", &Campaign{
		Name:         "Launch",
		Channel:      ChannelEmail,
		Subject:      "Hello",
		BodyText:     "Plain hello",
		BodyHTML:     "<p>HTML hello</p>",
		SegmentID:    ptrInt64(42),
		BatchSize:    10,
		ScheduleKind: "immediate",
	})
	if err != nil {
		t.Fatalf("create campaign: %v", err)
	}
	if _, err := runRecipientBulk(ctx, "test-proj", c.ID, []Recipient{{
		ContactID: 100,
		Address:   "alice@example.com",
		Status:    RecipPending,
	}}); err != nil {
		t.Fatalf("insert recipient: %v", err)
	}
	c, err = dbCampaignSetStatus(ctx.AppDB(), "test-proj", c.ID, StatusSending, "", true, false)
	if err != nil {
		t.Fatalf("set sending: %v", err)
	}

	if err := tickCampaign(ctx, "test-proj", c); err != nil {
		t.Fatalf("tick: %v", err)
	}

	sends := platform.callsTo("messaging", "send_message")
	if len(sends) != 1 {
		t.Fatalf("send_message calls=%d, want 1", len(sends))
	}
	body, _ := sends[0].Input["body"].(string)
	bodyHTML, _ := sends[0].Input["body_html"].(string)
	re := regexp.MustCompile(`https://agents\.example\.test/api/apps/campaigns/unsubscribe\?t=([A-Za-z0-9_-]+)`)
	if !re.MatchString(body) {
		t.Fatalf("body missing unsubscribe URL: %q", body)
	}
	if !strings.Contains(bodyHTML, `<a href="https://agents.example.test/api/apps/campaigns/unsubscribe?t=`) {
		t.Fatalf("body_html missing unsubscribe link: %q", bodyHTML)
	}
	var tokenCount int
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM campaign_unsubscribe_tokens`).Scan(&tokenCount); err != nil {
		t.Fatalf("count tokens: %v", err)
	}
	if tokenCount != 1 {
		t.Fatalf("tokens=%d, want 1", tokenCount)
	}
	recips, err := dbRecipientsList(ctx.AppDB(), "test-proj", c.ID, "", 10)
	if err != nil {
		t.Fatalf("list recipients: %v", err)
	}
	if len(recips) != 1 || recips[0].Status != RecipSent || recips[0].MessagingID == nil || *recips[0].MessagingID != 777 {
		t.Fatalf("recipient after tick=%#v, want sent with messaging_id=777", recips)
	}
}

func TestUnsubscribeEndpointSuppressesAddress(t *testing.T) {
	platform := &campaignsPlatform{}
	ctx := newCampaignsTestCtx(t, platform)
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = nil })
	campaignID := mustSeedSentRecipient(t, ctx, 555, "alice@example.com")
	recips, err := dbRecipientsList(ctx.AppDB(), "test-proj", campaignID, "", 10)
	if err != nil {
		t.Fatalf("list recipients: %v", err)
	}
	token, err := dbUnsubscribeTokenForRecipient(ctx.AppDB(), "test-proj", recips[0].ID, campaignID)
	if err != nil {
		t.Fatalf("token: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/unsubscribe?t="+token, nil)
	w := httptest.NewRecorder()
	(&App{}).handleHTTPUnsubscribe(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("unsubscribe status=%d body=%s", w.Code, w.Body.String())
	}
	got, err := dbRecipientByID(ctx.AppDB(), "test-proj", recips[0].ID)
	if err != nil {
		t.Fatalf("recipient get: %v", err)
	}
	if got.Status != RecipUnsubscribed {
		t.Fatalf("status=%q, want unsubscribed", got.Status)
	}
	suppressions := platform.callsTo("messaging", "suppression_add")
	if len(suppressions) != 1 {
		t.Fatalf("suppression_add calls=%d, want 1", len(suppressions))
	}
	if suppressions[0].Input["address"] != "alice@example.com" || suppressions[0].Input["reason"] != "unsubscribe" {
		t.Fatalf("suppression input=%#v", suppressions[0].Input)
	}
}

func TestMessageEventUpdatesCampaignRecipientStatus(t *testing.T) {
	recorder := tk.NewEmitRecorder()
	ctx := newCampaignsTestCtx(t, &campaignsPlatform{}, tk.WithEmitter(recorder))
	campaignID := mustSeedSentRecipient(t, ctx, 555, "alice@example.com")

	if err := (&App{}).handleMessageEvent(ctx, sdk.Event{
		Event:     "message.event",
		ProjectID: "test-proj",
		Data: map[string]any{
			"message_id":    float64(555),
			"kind":          "bounced",
			"recipient":     "alice@example.com",
			"reason":        "hard bounce",
			"occurred_at":   "2026-07-02T12:00:00Z",
			"extra_ignored": true,
		},
	}); err != nil {
		t.Fatalf("handle event: %v", err)
	}
	recips, err := dbRecipientsList(ctx.AppDB(), "test-proj", campaignID, "", 10)
	if err != nil {
		t.Fatalf("list recipients: %v", err)
	}
	if len(recips) != 1 || recips[0].Status != RecipBounced || recips[0].Error != "hard bounce" {
		t.Fatalf("recipient after bounce=%#v, want bounced with reason", recips)
	}
	if events := recorder.EventsByTopic("campaign.recipient_updated"); len(events) != 1 {
		t.Fatalf("recipient_updated events=%d, want 1", len(events))
	}

	if err := (&App{}).handleMessageEvent(ctx, sdk.Event{
		Event:     "message.event",
		ProjectID: "test-proj",
		Data:      map[string]any{"message_id": int64(555), "kind": "delivered"},
	}); err != nil {
		t.Fatalf("handle delivered after bounce: %v", err)
	}
	recips, err = dbRecipientsList(ctx.AppDB(), "test-proj", campaignID, "", 10)
	if err != nil {
		t.Fatalf("list recipients: %v", err)
	}
	if recips[0].Status != RecipBounced {
		t.Fatalf("delivered downgraded terminal status to %q", recips[0].Status)
	}

	openedCampaignID := mustSeedSentRecipient(t, ctx, 556, "bob@example.com")
	if err := (&App{}).handleMessageEvent(ctx, sdk.Event{
		Event:     "message.event",
		ProjectID: "test-proj",
		Data:      map[string]any{"message_id": int64(556), "kind": "opened"},
	}); err != nil {
		t.Fatalf("handle opened: %v", err)
	}
	recips, err = dbRecipientsList(ctx.AppDB(), "test-proj", openedCampaignID, "", 10)
	if err != nil {
		t.Fatalf("list opened recipients: %v", err)
	}
	if len(recips) != 1 || recips[0].Status != RecipOpened {
		t.Fatalf("recipient after opened=%#v, want opened", recips)
	}
	if err := (&App{}).handleMessageEvent(ctx, sdk.Event{
		Event:     "message.event",
		ProjectID: "test-proj",
		Data:      map[string]any{"message_id": int64(556), "kind": "delivered"},
	}); err != nil {
		t.Fatalf("handle delivered after opened: %v", err)
	}
	recips, err = dbRecipientsList(ctx.AppDB(), "test-proj", openedCampaignID, "", 10)
	if err != nil {
		t.Fatalf("list opened recipients after delivered: %v", err)
	}
	if recips[0].Status != RecipOpened {
		t.Fatalf("delivered downgraded opened status to %q", recips[0].Status)
	}
	if err := (&App{}).handleMessageEvent(ctx, sdk.Event{
		Event:     "message.event",
		ProjectID: "test-proj",
		Data:      map[string]any{"message_id": int64(556), "kind": "clicked"},
	}); err != nil {
		t.Fatalf("handle clicked: %v", err)
	}
	recips, err = dbRecipientsList(ctx.AppDB(), "test-proj", openedCampaignID, "", 10)
	if err != nil {
		t.Fatalf("list clicked recipients: %v", err)
	}
	if recips[0].Status != RecipClicked {
		t.Fatalf("clicked status=%q, want clicked", recips[0].Status)
	}
	if err := (&App{}).handleMessageEvent(ctx, sdk.Event{
		Event:     "message.event",
		ProjectID: "test-proj",
		Data:      map[string]any{"message_id": int64(556), "kind": "opened"},
	}); err != nil {
		t.Fatalf("handle opened after clicked: %v", err)
	}
	recips, err = dbRecipientsList(ctx.AppDB(), "test-proj", openedCampaignID, "", 10)
	if err != nil {
		t.Fatalf("list clicked recipients after opened: %v", err)
	}
	if recips[0].Status != RecipClicked {
		t.Fatalf("opened downgraded clicked status to %q", recips[0].Status)
	}
}

func TestJobsTargetsUseAppToolsWithProjectIDForGlobalInstall(t *testing.T) {
	platform := &campaignsPlatform{}
	ctx := newCampaignsTestCtx(t, platform)
	c, err := dbCampaignCreate(ctx.AppDB(), "test-proj", &Campaign{
		Name:         "Scheduled",
		Channel:      ChannelEmail,
		Subject:      "Hello",
		BodyText:     "Body",
		SegmentID:    ptrInt64(42),
		ScheduleKind: "once",
	})
	if err != nil {
		t.Fatalf("create campaign: %v", err)
	}
	if _, err := scheduleMaterialiseJob(ctx, "test-proj", c, "2026-07-02T12:00:00Z"); err != nil {
		t.Fatalf("schedule materialise: %v", err)
	}
	if err := startTickJob(ctx, "test-proj", c); err != nil {
		t.Fatalf("start tick job: %v", err)
	}
	jobs := platform.callsTo("jobs", "jobs_schedule")
	if len(jobs) != 2 {
		t.Fatalf("jobs_schedule calls=%d, want 2", len(jobs))
	}
	wantTools := []string{"campaigns_materialise", "campaigns_tick"}
	for i, job := range jobs {
		target, _ := job.Input["target"].(map[string]any)
		if target["kind"] != "app_tool" || target["app"] != "campaigns" || target["tool"] != wantTools[i] {
			t.Fatalf("job target=%#v, want campaigns app_tool %s", target, wantTools[i])
		}
		input, _ := target["input"].(map[string]any)
		if input["_project_id"] != "test-proj" || input["id"] != c.ID {
			t.Fatalf("job target input=%#v, want project test-proj campaign %d", input, c.ID)
		}
		if target["url"] != nil || target["path"] != nil {
			t.Fatalf("job target retained HTTP fields: %#v", target)
		}
	}
}

func TestCampaignScheduleJobsFailureLeavesDraftUntouched(t *testing.T) {
	platform := &campaignsPlatform{callErrors: map[string]error{
		"jobs.jobs_schedule": errors.New("http target requires an absolute url"),
	}}
	ctx := newCampaignsTestCtx(t, platform)
	c, err := dbCampaignCreate(ctx.AppDB(), "test-proj", &Campaign{
		Name:         "Scheduled",
		Channel:      ChannelEmail,
		Subject:      "Hello",
		BodyText:     "Body",
		SegmentID:    ptrInt64(42),
		ScheduleKind: "once",
	})
	if err != nil {
		t.Fatalf("create campaign: %v", err)
	}

	_, err = (&App{}).toolCampaignsSchedule(ctx, map[string]any{
		"_project_id":  "test-proj",
		"id":           c.ID,
		"scheduled_at": "2026-09-01T12:00:00Z",
	})
	if err == nil {
		t.Fatal("schedule succeeded despite Jobs error")
	}
	got, getErr := dbCampaignGet(ctx.AppDB(), "test-proj", c.ID)
	if getErr != nil {
		t.Fatalf("get campaign: %v", getErr)
	}
	if got.Status != StatusDraft || got.ScheduledAt != "" || got.JobIDs != "" {
		t.Fatalf("campaign mutated after Jobs failure: status=%q scheduled_at=%q job_ids=%q", got.Status, got.ScheduledAt, got.JobIDs)
	}
}

func TestCampaignScheduleCreatesJobBeforeTransition(t *testing.T) {
	platform := &campaignsPlatform{}
	ctx := newCampaignsTestCtx(t, platform)
	c, err := dbCampaignCreate(ctx.AppDB(), "test-proj", &Campaign{
		Name:         "Scheduled",
		Channel:      ChannelEmail,
		Subject:      "Hello",
		BodyText:     "Body",
		SegmentID:    ptrInt64(42),
		ScheduleKind: "once",
	})
	if err != nil {
		t.Fatalf("create campaign: %v", err)
	}
	checkedDraft := false
	platform.beforeCall = func(appName, tool string, _ map[string]any) {
		if appName != "jobs" || tool != "jobs_schedule" {
			return
		}
		got, getErr := dbCampaignGet(ctx.AppDB(), "test-proj", c.ID)
		if getErr != nil {
			t.Fatalf("get campaign during Jobs call: %v", getErr)
		}
		if got.Status != StatusDraft || got.ScheduledAt != "" || got.JobIDs != "" {
			t.Fatalf("campaign transitioned before Jobs confirmed: %#v", got)
		}
		checkedDraft = true
	}

	out, err := (&App{}).toolCampaignsSchedule(ctx, map[string]any{
		"_project_id":  "test-proj",
		"id":           c.ID,
		"scheduled_at": "2026-09-01T12:00:00Z",
	})
	if err != nil {
		t.Fatalf("schedule: %v", err)
	}
	if !checkedDraft {
		t.Fatal("Jobs was not called before campaign transition")
	}
	got := out.(map[string]any)["campaign"].(*Campaign)
	if got.Status != StatusScheduled || got.ScheduledAt != "2026-09-01T12:00:00Z" || got.JobIDs != "123" {
		t.Fatalf("scheduled campaign=%#v", got)
	}
}

func TestCampaignsReconcileBackfillsFromMessagingEvents(t *testing.T) {
	recorder := tk.NewEmitRecorder()
	platform := &campaignsPlatform{}
	ctx := newCampaignsTestCtx(t, platform, tk.WithEmitter(recorder))
	campaignID := mustSeedSentRecipient(t, ctx, 900, "alice@example.com")

	out, err := (&App{}).toolCampaignsReconcile(ctx, map[string]any{"_project_id": "test-proj", "id": campaignID})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	got := out.(map[string]any)
	if got["checked"] != 1 || got["updated"] != 2 {
		t.Fatalf("reconcile result=%#v, want checked=1 updated=2", got)
	}
	recips, err := dbRecipientsList(ctx.AppDB(), "test-proj", campaignID, "", 10)
	if err != nil {
		t.Fatalf("list recipients: %v", err)
	}
	if len(recips) != 1 || recips[0].Status != RecipOpened {
		t.Fatalf("recipient after reconcile=%#v, want opened", recips)
	}
	if events := recorder.EventsByTopic("campaign.recipient_updated"); len(events) != 2 {
		t.Fatalf("recipient_updated events=%d, want 2", len(events))
	}
	events := recorder.EventsByTopic("campaign.reconciled")
	if len(events) != 1 {
		t.Fatalf("campaign.reconciled events=%d, want 1", len(events))
	}
	data, ok := events[0].Data.(map[string]any)
	if !ok {
		t.Fatalf("reconciled event data type=%T, want map[string]any", events[0].Data)
	}
	if data["campaign_id"] != campaignID || data["updated"] != 2 || data["stats"] == nil {
		t.Fatalf("reconciled event data=%#v", data)
	}
}

func TestCampaignsGetIncludesAudiencePreview(t *testing.T) {
	platform := &campaignsPlatform{}
	ctx := newCampaignsTestCtx(t, platform)
	c, err := dbCampaignCreate(ctx.AppDB(), "test-proj", &Campaign{
		Name:         "Preview",
		Channel:      ChannelEmail,
		Subject:      "Hello",
		BodyText:     "Body",
		SegmentID:    ptrInt64(42),
		ScheduleKind: "immediate",
	})
	if err != nil {
		t.Fatalf("create campaign: %v", err)
	}

	out, err := (&App{}).toolCampaignsGet(ctx, map[string]any{"_project_id": "test-proj", "id": c.ID})
	if err != nil {
		t.Fatalf("campaigns_get: %v", err)
	}
	got := out.(map[string]any)["campaign"].(*Campaign)
	if got.Audience == nil {
		t.Fatal("audience is nil")
	}
	if got.Audience.Kind != "segment" || got.Audience.ID != 42 {
		t.Fatalf("audience target = %s/%d, want segment/42", got.Audience.Kind, got.Audience.ID)
	}
	if got.Audience.Count != 2 {
		t.Fatalf("audience count = %d, want 2", got.Audience.Count)
	}
	if len(got.Audience.ContactIDs) != 2 || got.Audience.ContactIDs[0] != 101 || got.Audience.ContactIDs[1] != 102 {
		t.Fatalf("audience contact ids = %+v, want [101 102]", got.Audience.ContactIDs)
	}
	calls := platform.callsTo("crm", "segments_eval")
	if len(calls) != 1 {
		t.Fatalf("segments_eval calls=%d, want 1", len(calls))
	}
	if calls[0].Input["limit"] != 10 {
		t.Fatalf("segments_eval limit=%v, want 10", calls[0].Input["limit"])
	}
}

func mustSeedSentRecipient(t *testing.T, ctx *sdk.AppCtx, messagingID int64, address string) int64 {
	t.Helper()
	c, err := dbCampaignCreate(ctx.AppDB(), "test-proj", &Campaign{
		Name:         "Seed",
		Channel:      ChannelEmail,
		Subject:      "Hello",
		BodyText:     "Body",
		SegmentID:    ptrInt64(1),
		ScheduleKind: "immediate",
	})
	if err != nil {
		t.Fatalf("create campaign: %v", err)
	}
	if _, err := runRecipientBulk(ctx, "test-proj", c.ID, []Recipient{{
		ContactID: 1,
		Address:   address,
		Status:    RecipPending,
	}}); err != nil {
		t.Fatalf("insert recipient: %v", err)
	}
	recips, err := dbRecipientsList(ctx.AppDB(), "test-proj", c.ID, "", 10)
	if err != nil {
		t.Fatalf("list recipients: %v", err)
	}
	if len(recips) != 1 {
		t.Fatalf("recipients=%d, want 1", len(recips))
	}
	if err := dbRecipientMarkSent(ctx.AppDB(), "test-proj", recips[0].ID, messagingID); err != nil {
		t.Fatalf("mark sent: %v", err)
	}
	return c.ID
}

func ptrInt64(v int64) *int64 { return &v }
