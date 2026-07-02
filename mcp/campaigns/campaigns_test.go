package main

import (
	"encoding/json"
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
	calls []campaignsPlatformCall
}

type campaignsPlatformCall struct {
	App   string
	Tool  string
	Input map[string]any
}

func (p *campaignsPlatform) CallAppResult(appName, tool string, input map[string]any, out any) error {
	p.calls = append(p.calls, campaignsPlatformCall{App: appName, Tool: tool, Input: input})
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
}

func TestJobsTargetsIncludeProjectIDForGlobalInstall(t *testing.T) {
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
	for _, job := range jobs {
		target, _ := job.Input["target"].(map[string]any)
		path, _ := target["path"].(string)
		if !strings.Contains(path, "project_id=test-proj") {
			t.Fatalf("job target path %q missing project_id", path)
		}
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
