package main

import (
	"fmt"
	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
	"testing"
)

func autoResponseCorrection(id, original, address string) sdk.Event {
	e := deliveryEvent(id, "auto_replied", address, "2026-10-26T10:02:00Z", false)
	e.Data["reclassified_event_id"] = original
	return e
}

func TestWrappedDeliveryRecipientMatchesCRMChannel(t *testing.T) {
	ctx := newTestCtx(t)
	contact := mustCreate(t, ctx, map[string]any{"channels": []any{map[string]any{"kind": "email", "value": "Alice <ALICE@example.test>", "is_primary": true}}})
	if contact.Channels[0].Value != "alice@example.test" {
		t.Fatalf("channel=%+v", contact.Channels[0])
	}
	e := deliveryEvent("wrapped", "bounced", "Alice <ALICE@example.test>", "2026-10-26T10:00:00Z", true)
	if err := (&App{}).handleMessagingDeliveryEvent(ctx, e); err != nil {
		t.Fatal(err)
	}
	if state := emailDeliveryState(t, ctx, contact.Channels[0].ID); state.Status != "hard_bounced" {
		t.Fatalf("wrapped event missed channel: %+v", state)
	}
}

func TestAutoResponseCorrectionOnlyUndoesItsOwnSoftBounce(t *testing.T) {
	for _, order := range []string{"bounce-first", "correction-first", "later-delivery", "later-hard-bounce", "later-complaint", "other-soft-bounces", "recovery"} {
		t.Run(order, func(t *testing.T) {
			ctx := newTestCtx(t)
			app := &App{}
			addr := "alice@example.test"
			c := mustCreate(t, ctx, map[string]any{"channels": []any{map[string]any{"kind": "email", "value": addr, "is_primary": true}}})
			cid := c.Channels[0].ID
			apply := func(e sdk.Event) {
				t.Helper()
				if err := app.handleMessagingDeliveryEvent(ctx, e); err != nil {
					t.Fatal(err)
				}
			}
			correction := autoResponseCorrection("correction", "auto-bounce", addr)
			if order == "correction-first" {
				apply(correction)
			}
			apply(deliveryEvent("auto-bounce", "bounced", "Alice <ALICE@example.test>", "2026-10-26T10:02:00Z", false))
			wantStatus := "active"
			wantCount := 0
			switch order {
			case "later-delivery":
				apply(deliveryEvent("delivery", "delivered", addr, "2026-10-26T10:03:00Z", false))
				apply(deliveryEvent("real-soft", "bounced", addr, "2026-10-26T10:04:00Z", false))
				wantStatus = "soft_bounced"
				wantCount = 1
			case "later-hard-bounce":
				apply(deliveryEvent("real-hard", "bounced", addr, "2026-10-26T10:03:00Z", true))
				wantStatus = "hard_bounced"
				wantCount = 1
			case "later-complaint":
				apply(deliveryEvent("complaint", "complained", addr, "2026-10-26T10:03:00Z", true))
				wantStatus = "complained"
				wantCount = 1
			case "other-soft-bounces":
				for i := range 3 {
					apply(deliveryEvent(fmt.Sprintf("real-soft-%d", i), "bounced", addr, fmt.Sprintf("2026-10-26T10:0%d:00Z", i+3), false))
				}
				wantStatus = "soft_bounced"
				wantCount = 3
			case "recovery":
				if err := recoverChannelDelivery(ctx, "test-proj", cid, "email", "reviewed"); err != nil {
					t.Fatal(err)
				}
				apply(deliveryEvent("new-soft", "bounced", addr, "2099-10-26T10:04:00Z", false))
				wantStatus = "soft_bounced"
				wantCount = 1
			}
			apply(correction)
			apply(correction)
			state := emailDeliveryState(t, ctx, cid)
			if state.Status != wantStatus || state.ConsecutiveSoftBounces != wantCount || state.Quarantined != (wantCount >= 3) {
				t.Fatalf("state=%+v; want %s/%d", state, wantStatus, wantCount)
			}
		})
	}
}

func TestCorrectedQuarantineRetainsOtherBouncesAndRetriesSuppressionRemoval(t *testing.T) {
	platform := &crmRecordingPlatform{}
	ctx := newTestCtx(t, tk.WithPlatform(platform))
	app := &App{}
	addr := "alice@example.test"
	c := mustCreate(t, ctx, map[string]any{"channels": []any{map[string]any{"kind": "email", "value": addr, "is_primary": true}}})
	cid := c.Channels[0].ID
	for i := range 3 {
		if err := app.handleMessagingDeliveryEvent(ctx, deliveryEvent(fmt.Sprintf("bounce-%d", i), "bounced", addr, fmt.Sprintf("2026-10-26T10:0%d:00Z", i), false)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := ctx.AppDB().Exec(`UPDATE contact_channel_delivery_state SET suppressed=1,suppression_source='crm',suppression_reason='soft-bounce-threshold',suppression_kind='address',suppression_match=? WHERE channel_id=?`, addr, cid); err != nil {
		t.Fatal(err)
	}
	if err := app.handleMessagingDeliveryEvent(ctx, autoResponseCorrection("correction", "bounce-1", addr)); err != nil {
		t.Fatal(err)
	}
	state := emailDeliveryState(t, ctx, cid)
	if state.ConsecutiveSoftBounces != 2 || state.Quarantined {
		t.Fatalf("state=%+v", state)
	}
	// Without the asynchronous suppression removal event, the retry must still
	// attempt removal even though two genuine bounces remain.
	n, err := app.retryProjectSuppressions(ctx, "test-proj")
	if err != nil || n != 1 {
		t.Fatalf("retries=%d err=%v", n, err)
	}
	removes := 0
	for _, call := range platform.calls {
		if call.Tool == "suppression_remove" {
			removes++
		}
	}
	if removes != 2 {
		t.Fatalf("removal attempts=%d", removes)
	}
}
