package main

import (
	"encoding/json"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

type snapshotReadPlatform struct {
	*idempotentMessagingPlatform
}

func (p *snapshotReadPlatform) CallAppResult(app, tool string, args map[string]any, out any) error {
	if tool != "message_get" {
		return p.idempotentMessagingPlatform.CallAppResult(app, tool, args, out)
	}
	b, _ := json.Marshal(map[string]any{"found": true, "message": map[string]any{
		"id": int64Arg(args, "id"), "direction": "out", "status": "delivered",
		"template_snapshot": map[string]any{"template_id": 46, "body_text": "Hi Roxy", "availability": "complete", "content_source": "provider_response"},
	}})
	return json.Unmarshal(b, out)
}

func TestRecordedTemplateSnapshotSurvivesMessagingUnavailable(t *testing.T) {
	a := &Activity{SourceDetail: `{"template_snapshot":{"template_id":46,"body_text":"Hi Roxy","availability":"complete","content_source":"send_time_template"}}`}
	enrichActivitiesWithMessagingStatus(nil, "test-proj", []*Activity{a, nil})
	if a.TemplateSnapshot == nil || a.TemplateSnapshot.BodyText != "Hi Roxy" {
		t.Fatalf("snapshot=%+v", a.TemplateSnapshot)
	}
	legacy := &Activity{Body: "(template #46)"}
	enrichActivitiesWithMessagingStatus(nil, "test-proj", []*Activity{legacy})
	if legacy.TemplateSnapshot != nil {
		t.Fatal("legacy message reconstructed")
	}
}

func TestMessagingSnapshotEnrichmentIsBoundAndReadOnly(t *testing.T) {
	p := &snapshotReadPlatform{newIdempotentMessagingPlatform()}
	ctx := newTestCtx(t, tk.WithPlatform(p))
	a := &Activity{ID: 1, MessagingID: 44970, Body: "(template #46)", SourceDetail: `{"source_install_id":42}`}
	foreign := &Activity{ID: 2, MessagingID: 44970, Body: "(template #46)", SourceDetail: `{"source_install_id":43}`}
	enrichActivitiesWithMessagingStatus(ctx, "test-proj", []*Activity{a, foreign})
	if a.TemplateSnapshot == nil || a.TemplateSnapshot.BodyText != "Hi Roxy" || a.Body != "(template #46)" {
		t.Fatalf("snapshot/body=%+v", a)
	}
	if foreign.TemplateSnapshot != nil {
		t.Fatal("foreign install snapshot leaked")
	}
	b, err := json.Marshal(a)
	if err != nil || !json.Valid(b) {
		t.Fatalf("snapshot not exposed through REST/MCP: %s %v", b, err)
	}
}

func TestOutboundTemplateSnapshotRecordedWithoutChangingSendArguments(t *testing.T) {
	p := newIdempotentMessagingPlatform()
	p.sendResponse = map[string]any{"id": 7001, "status": "sent", "provider_message_id": "SMsnapshot", "template_snapshot": map[string]any{
		"template_id": 46, "name": "reengage", "body_text": "Hi Roxy", "availability": "complete", "content_source": "send_time_template", "variables": map[string]any{"1": "Roxy"},
	}}
	ctx := newTestCtx(t, tk.WithPlatform(p))
	c := mustCreate(t, ctx, map[string]any{"display_name": "Roxy", "channels": []any{map[string]any{"kind": "phone", "value": "+447757771608", "is_primary": true}}})
	result, err := (&App{}).toolSendMessage(ctx, map[string]any{"id": c.ID, "channel": "sms", "from": "+447380368854", "template_id": 46, "template_vars": map[string]any{"1": "Roxy"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.(map[string]any)["activity"].(*Activity).TemplateSnapshot == nil {
		t.Fatal("immediate send response omitted snapshot")
	}
	var body, detail string
	if err := ctx.AppDB().QueryRow(`SELECT body,source_detail FROM contact_activities WHERE contact_id=? AND kind='sms_sent'`, c.ID).Scan(&body, &detail); err != nil {
		t.Fatal(err)
	}
	s := recordedTemplateSnapshot(&Activity{SourceDetail: detail})
	if body != "Hi Roxy" || s == nil || s.TemplateID != 46 || s.Variables["1"] != "Roxy" {
		t.Fatalf("body=%q snapshot=%+v", body, s)
	}
	_, calls := p.snapshot()
	for _, call := range calls {
		if call.Tool == "send_message" && strArg(call.Input, "body") != "" {
			t.Fatalf("CRM sent rendered content instead of template: %v", call.Input)
		}
	}
}
