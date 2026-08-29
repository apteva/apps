package main

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type suppressionListPlatform struct {
	tk.BasePlatformClient
	items []messagingSuppression
}

func (p *suppressionListPlatform) WhoAmI() (*sdk.InstallIdentity, error) {
	return &sdk.InstallIdentity{
		AppName: "crm", InstallID: 99, ProjectID: "test-proj",
		Bindings: map[string]any{"messaging": float64(42)},
	}, nil
}

func (p *suppressionListPlatform) GetInstance(id int64) (*sdk.PlatformInstance, error) {
	return &sdk.PlatformInstance{ID: id, Name: "messaging", Status: "running", ProjectID: "test-proj"}, nil
}

func (p *suppressionListPlatform) CallAppResult(_ string, tool string, _ map[string]any, out any) error {
	payload := map[string]any{"ok": true}
	if tool == "suppression_list" {
		payload = map[string]any{
			"suppressions": p.items,
			"count":        len(p.items),
			"total":        len(p.items),
			"has_more":     false,
		}
	}
	body, _ := json.Marshal(payload)
	return json.Unmarshal(body, out)
}

func deliveryEvent(id, kind, recipient, occurred string, permanent bool) sdk.Event {
	return sdk.Event{
		Event:           "message.event",
		SourceApp:       "messaging",
		SourceInstallID: 42,
		ProjectID:       "test-proj",
		Data: map[string]any{
			"event_id":    id,
			"channel":     "email",
			"kind":        kind,
			"recipient":   recipient,
			"occurred_at": occurred,
			"permanent":   permanent,
			"reason":      kind + " test",
		},
	}
}

func emailDeliveryState(t *testing.T, ctx *sdk.AppCtx, channelID int64) ChannelDeliverability {
	t.Helper()
	states, err := loadChannelDeliverability(ctx.AppDB(), "test-proj", channelID)
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 1 {
		t.Fatalf("delivery states=%d, want 1: %#v", len(states), states)
	}
	return states[0]
}

func TestDeliveryEventsQuarantineAfterThreeTransientBouncesAndResetOnDelivery(t *testing.T) {
	platform := &crmRecordingPlatform{}
	ctx := newTestCtx(t, tk.WithPlatform(platform))
	app := &App{}
	contact := mustCreate(t, ctx, map[string]any{
		"channels": []any{map[string]any{"kind": "email", "value": "bounce@example.test", "is_primary": true}},
	})
	channelID := contact.Channels[0].ID

	for i, occurred := range []string{"2026-08-26T10:00:00Z", "2026-08-26T10:01:00Z", "2026-08-26T10:02:00Z"} {
		event := deliveryEvent("bounce-"+occurred, "bounced", "bounce@example.test", occurred, false)
		if err := app.handleMessagingDeliveryEvent(ctx, event); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			// A duplicate stable event ID must not increment the counter.
			if err := app.handleMessagingDeliveryEvent(ctx, event); err != nil {
				t.Fatal(err)
			}
		}
	}
	state := emailDeliveryState(t, ctx, channelID)
	if state.Status != "soft_bounced" || state.ConsecutiveSoftBounces != 3 || !state.Quarantined || state.Messageable {
		t.Fatalf("state after transient bounces=%+v", state)
	}
	var suppressionAdds int
	for _, call := range platform.calls {
		if call.Tool == "suppression_add" {
			suppressionAdds++
			if call.Input["reason"] != "soft-bounce-threshold" || call.Input["source"] != "crm" {
				t.Fatalf("suppression_add=%#v", call.Input)
			}
		}
	}
	if suppressionAdds != 1 {
		t.Fatalf("suppression_add calls=%d, want 1; calls=%#v", suppressionAdds, platform.calls)
	}

	if err := app.handleMessagingDeliveryEvent(ctx,
		deliveryEvent("delivered-1", "delivered", "bounce@example.test", "2026-08-26T10:03:00Z", false)); err != nil {
		t.Fatal(err)
	}
	state = emailDeliveryState(t, ctx, channelID)
	if state.Status != "active" || state.ConsecutiveSoftBounces != 0 || state.Quarantined || !state.Messageable {
		t.Fatalf("state after delivery=%+v", state)
	}
	var processed int
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM crm_processed_messaging_events`).Scan(&processed); err != nil {
		t.Fatal(err)
	}
	if processed != 4 {
		t.Fatalf("processed events=%d, want 4 unique events", processed)
	}
}

func TestHardBounceIsReturnedAndExcludedFromMessageableContacts(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	contact := mustCreate(t, ctx, map[string]any{
		"display_name": "Hard Bounce",
		"channels":     []any{map[string]any{"kind": "email", "value": "hard@example.test", "is_primary": true}},
	})
	if err := app.handleMessagingDeliveryEvent(ctx,
		deliveryEvent("hard-1", "bounced", "hard@example.test", "2026-08-26T11:00:00Z", true)); err != nil {
		t.Fatal(err)
	}

	out, err := app.toolGet(ctx, map[string]any{"id": contact.ID})
	if err != nil {
		t.Fatal(err)
	}
	got := out.(map[string]any)["contact"].(*Contact)
	state := got.Channels[0].Deliverability[0]
	if state.Status != "hard_bounced" || state.Messageable || state.StatusReason == "" {
		t.Fatalf("contacts_get delivery state=%+v", state)
	}
	messageable, err := app.toolListMessageable(ctx, map[string]any{"channel": "email"})
	if err != nil {
		t.Fatal(err)
	}
	if count := messageable.(map[string]any)["count"]; count != 0 {
		t.Fatalf("messageable count=%v, want 0", count)
	}
}

func TestChannelDeliveryRowsFollowChannelLifecycle(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	contact := mustCreate(t, ctx, map[string]any{
		"channels": []any{
			map[string]any{"kind": "email", "value": "lifecycle@example.test", "is_primary": true},
			map[string]any{"kind": "phone", "value": "+15557654321", "is_primary": true},
		},
	})
	var count int
	if err := ctx.AppDB().QueryRow(
		`SELECT COUNT(*) FROM contact_channel_delivery_state WHERE project_id = 'test-proj'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("delivery rows after email+phone create=%d, want email+sms+whatsapp", count)
	}
	if _, err := app.toolUpdate(ctx, map[string]any{
		"id": contact.ID,
		"patch": map[string]any{"channels": []any{
			map[string]any{"kind": "email", "value": "lifecycle@example.test", "is_primary": true},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := ctx.AppDB().QueryRow(
		`SELECT COUNT(*) FROM contact_channel_delivery_state WHERE project_id = 'test-proj'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("delivery rows after phone removal=%d, want 1", count)
	}
}

func TestSuppressionLifecycleSetsUnsubscribedAndExplicitRemovalRestores(t *testing.T) {
	platform := &crmRecordingPlatform{}
	ctx := newTestCtx(t, tk.WithPlatform(platform))
	app := &App{}
	contact := mustCreate(t, ctx, map[string]any{
		"channels": []any{map[string]any{"kind": "phone", "value": "+15551234567", "is_primary": true}},
	})
	add := sdk.Event{
		Event: "suppression.changed", SourceApp: "messaging", SourceInstallID: 42, ProjectID: "test-proj",
		Data: map[string]any{
			"operation": "add", "suppressed": true, "channel": "sms", "kind": "address",
			"address": "+15551234567", "reason": "stop-keyword", "source": "auto",
			"first_seen": "2026-08-26T12:00:00Z", "last_seen": "2026-08-26T12:00:00Z",
		},
	}
	if err := app.handleMessagingSuppressionEvent(ctx, add); err != nil {
		t.Fatal(err)
	}
	states, err := loadChannelDeliverability(ctx.AppDB(), "test-proj", contact.Channels[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 2 || states[0].Transport != "sms" || states[0].Status != "unsubscribed" || !states[0].Suppressed || states[0].Messageable {
		t.Fatalf("states after STOP=%+v", states)
	}
	if states[1].Transport != "whatsapp" || !states[1].Messageable {
		t.Fatalf("WhatsApp state should remain independent: %+v", states[1])
	}
	smsAudience, err := app.toolListMessageable(ctx, map[string]any{"channel": "sms"})
	if err != nil || smsAudience.(map[string]any)["count"] != 0 {
		t.Fatalf("SMS audience=%v err=%v, want empty", smsAudience, err)
	}
	whatsAppAudience, err := app.toolListMessageable(ctx, map[string]any{"channel": "whatsapp"})
	if err != nil || whatsAppAudience.(map[string]any)["count"] != 1 {
		t.Fatalf("WhatsApp audience=%v err=%v, want contact", whatsAppAudience, err)
	}

	remove := add
	remove.Data = map[string]any{
		"operation": "remove", "suppressed": false, "channel": "sms", "kind": "address",
		"address": "+15551234567", "reason": "stop-keyword", "source": "auto",
		"first_seen": "2026-08-26T12:00:00Z", "last_seen": "2026-08-26T12:05:00Z",
	}
	if err := app.handleMessagingSuppressionEvent(ctx, remove); err != nil {
		t.Fatal(err)
	}
	states, err = loadChannelDeliverability(ctx.AppDB(), "test-proj", contact.Channels[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if states[0].Status != "active" || states[0].Suppressed || !states[0].Messageable {
		t.Fatalf("SMS state after explicit removal=%+v", states[0])
	}
}

func TestAddressResolutionSkipsUnmessageablePrimaryChannel(t *testing.T) {
	ctx := newTestCtx(t)
	contact := mustCreate(t, ctx, map[string]any{
		"channels": []any{
			map[string]any{"kind": "email", "value": "primary@example.test", "is_primary": true},
			map[string]any{"kind": "email", "value": "alternate@example.test", "is_primary": false},
		},
	})
	primaryID := contact.Channels[0].ID
	if _, err := ctx.AppDB().Exec(
		`UPDATE contact_channel_delivery_state
		 SET suppressed = 1, suppression_reason = 'manual', suppression_checked_at = CURRENT_TIMESTAMP
		 WHERE project_id = 'test-proj' AND channel_id = ? AND transport = 'email'`, primaryID); err != nil {
		t.Fatal(err)
	}
	resolved, err := resolveContactAddress(ctx.AppDB(), "test-proj", contact, "email")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Address != "alternate@example.test" || resolved.ChannelID == primaryID {
		t.Fatalf("resolved=%+v, want healthy alternate", resolved)
	}
	messageable, err := (&App{}).toolListMessageable(ctx, map[string]any{"channel": "email"})
	if err != nil {
		t.Fatal(err)
	}
	contacts := messageable.(map[string]any)["contacts"].([]MessageableContact)
	if len(contacts) != 1 || contacts[0].MessageableAddresses["email"] != "alternate@example.test" {
		t.Fatalf("messageable contacts=%+v, want healthy alternate address", contacts)
	}
}

func TestSuppressionReconciliationAppliesDomainMatchAndClearsRemovedState(t *testing.T) {
	platform := &suppressionListPlatform{items: []messagingSuppression{{
		Channel: "email", Kind: "domain", Address: "example.test",
		Reason: "manual", Source: "operator", FirstSeen: "2026-08-26T13:00:00Z",
	}}}
	ctx := newTestCtx(t, tk.WithPlatform(platform))
	app := &App{}
	contact := mustCreate(t, ctx, map[string]any{
		"channels": []any{map[string]any{"kind": "email", "value": "person@example.test", "is_primary": true}},
	})
	if err := app.reconcileProjectSuppressions(ctx, "test-proj"); err != nil {
		t.Fatal(err)
	}
	state := emailDeliveryState(t, ctx, contact.Channels[0].ID)
	if !state.Suppressed || state.SuppressionKind != "domain" || state.SuppressionMatch != "example.test" || state.Messageable {
		t.Fatalf("state after domain reconcile=%+v", state)
	}

	platform.items = nil
	if err := app.reconcileProjectSuppressions(ctx, "test-proj"); err != nil {
		t.Fatal(err)
	}
	state = emailDeliveryState(t, ctx, contact.Channels[0].ID)
	if state.Suppressed || !state.Messageable {
		t.Fatalf("state after suppression removal reconcile=%+v", state)
	}
}

func TestCRMManifestRequiresMessagingDeliveryEvents(t *testing.T) {
	for source, manifest := range map[string]sdk.Manifest{
		"embedded": (&App{}).Manifest(),
		"disk":     mustParseManifestFile(t, "apteva.yaml"),
	} {
		found := false
		for _, dependency := range manifest.Requires.Apps {
			if dependency.Name != "messaging" {
				continue
			}
			found = true
			if dependency.Version != ">=0.13.46" || !dependency.Optional ||
				!reflect.DeepEqual(dependency.Events, []string{"message.event", "suppression.changed"}) {
				t.Fatalf("%s messaging dependency=%+v", source, dependency)
			}
		}
		if !found {
			t.Fatalf("%s manifest missing Messaging dependency", source)
		}
	}
}

func mustParseManifestFile(t *testing.T, path string) sdk.Manifest {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := sdk.ParseManifest(body)
	if err != nil {
		t.Fatal(err)
	}
	return *manifest
}
