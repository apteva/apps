package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	"github.com/gobwas/ws/wsutil"
)

type ringTestPlatform struct {
	answerPlatform
	mu        sync.Mutex
	dialCount int
}

func (p *ringTestPlatform) ExecuteIntegrationTool(id int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if tool == "make_call" || tool == "dial_call" {
		p.dialCount++
		p.integrationCalls = append(p.integrationCalls, integrationCall{Tool: tool, Input: input})
		raw := fmt.Sprintf(`{"sid":"CA-child-%d","call_uuid":"plivo-%d","data":{"call_control_id":"telnyx-%d"}}`, p.dialCount, p.dialCount, p.dialCount)
		return &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(raw)}, nil
	}
	return p.answerPlatform.ExecuteIntegrationTool(id, tool, input)
}
func externalRingFixture(t *testing.T, strategy string) (*App, *callsDB, *sdk.AppCtx, *ringTestPlatform) {
	app, db, plan := ringFixture(t, strategy)
	platform := &ringTestPlatform{}
	ctx := sdk.NewAppCtxForTest(&sdk.Manifest{}, db.db, sdk.Config{}, platform, nil).WithProject("p1")
	globalCtx = ctx
	for i := 0; i < 3; i++ {
		dest := plan.GroupDestinations[fmt.Sprint("d", i)]
		dest.Kind = "pstn"
		dest.ConfigJSON = fmt.Sprintf(`{"phone_number":"+1202555010%d"}`, i)
		plan.GroupDestinations[dest.ID] = dest
	}
	insertRingCall(t, db, plan, "external")
	return app, db, ctx, platform
}
func TestRingExternalPlacementIsIdempotentAndCancelsLosers(t *testing.T) {
	app, db, ctx, platform := externalRingFixture(t, "simultaneous")
	parent, _ := db.findCall("external")
	offers, _ := db.activeRingOffers(parent.ID, parent.ProjectID)
	app.startOfferedRingLegs(ctx, parent, offers)
	app.startOfferedRingLegs(ctx, parent, offers)
	if platform.dialCount != 3 {
		t.Fatalf("duplicate dials=%d", platform.dialCount)
	}
	children, err := db.listWhere(`ingress_path='ring_group' ORDER BY id`)
	if err != nil || len(children) != 3 {
		t.Fatal(children, err)
	}
	// Two answer callbacks arrive before the reconciliation pass.
	for _, child := range children[:2] {
		if err = db.updateStatus(child.ID, "in-progress", ""); err != nil {
			t.Fatal(err)
		}
	}
	if err = app.runRingLegTick(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	if err = app.runRingLegTick(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	parent, _ = db.findCall(parent.ID)
	if parent.Status != "answered" || parent.PeerKind != peerKindExternal || parent.RoutingDestinationID == "" {
		t.Fatalf("parent=%+v", parent)
	}
	var claimed, canceled int
	_ = db.db.QueryRow(`SELECT COUNT(*) FROM call_offers WHERE status='claimed'`).Scan(&claimed)
	_ = db.db.QueryRow(`SELECT COUNT(*) FROM call_legs WHERE status='canceled'`).Scan(&canceled)
	if claimed != 1 || canceled != 2 {
		t.Fatalf("claimed=%d canceled=%d", claimed, canceled)
	}
	var winner string
	_ = db.db.QueryRow(`SELECT l.id FROM call_legs l JOIN call_offers o ON o.id=l.offer_id WHERE o.status='claimed'`).Scan(&winner)
	if err = db.updateStatus(winner, "completed", ""); err != nil {
		t.Fatal(err)
	}
	if err = app.runRingLegTick(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	parent, _ = db.findCall(parent.ID)
	if parent.Status != "completed" {
		t.Fatalf("winner hangup left parent %s", parent.Status)
	}
}
func TestRingExternalBridgeOnlyConnectsWinnerBothDirections(t *testing.T) {
	app, db, ctx, _ := externalRingFixture(t, "simultaneous")
	parent, _ := db.findCall("external")
	offers, _ := db.activeRingOffers(parent.ID, parent.ProjectID)
	if err := app.startRingLeg(ctx, parent, offers[0]); err != nil {
		t.Fatal(err)
	}
	children, _ := db.listWhere(`ingress_path='ring_group'`)
	child := children[0]
	if err := db.updateStatus(child.ID, "in-progress", ""); err != nil {
		t.Fatal(err)
	}
	if err := app.runRingLegTick(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	parent, _ = db.findCall(parent.ID)
	server := softphoneTestServer(t, app)
	caller := dialWS(t, server.URL+"/peer/"+parent.ID+"/"+parent.CallbackSecret)
	callee := dialWS(t, server.URL+"/peer/"+child.ID+"/"+child.CallbackSecret)
	deadline := time.Now().Add(3 * time.Second)
	for {
		hub := app.softphones.lookup(parent.ID)
		ready := false
		if hub != nil {
			hub.mu.Lock()
			ready = hub.peer != nil && hub.browser != nil
			hub.mu.Unlock()
		}
		if ready {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("winner bridge did not attach")
		}
		time.Sleep(10 * time.Millisecond)
	}
	forward := bytes.Repeat([]byte{0x12, 0x03}, 480)
	reverse := bytes.Repeat([]byte{0x34, 0x05}, 480)
	if err := wsutil.WriteClientBinary(caller, forward); err != nil {
		t.Fatal(err)
	}
	if got := readBinaryWithin(t, callee, 2*time.Second); !bytes.Equal(got, forward) {
		t.Fatal("caller waveform changed")
	}
	if err := wsutil.WriteClientBinary(callee, reverse); err != nil {
		t.Fatal(err)
	}
	if got := readBinaryWithin(t, caller, 2*time.Second); !bytes.Equal(got, reverse) {
		t.Fatal("callee waveform changed")
	}
}

func TestRingRecoveredOfferDoesNotWaitForCanceledPhone(t *testing.T) {
	app, db, ctx, platform := externalRingFixture(t, "simultaneous")
	parent, _ := db.findCall("external")
	offers, _ := db.activeRingOffers(parent.ID, parent.ProjectID)
	app.startOfferedRingLegs(ctx, parent, offers)
	if _, won, err := db.claimRingOffer(parent.ID, parent.ProjectID, offers[0].DestinationID, "external", 0); err != nil || !won {
		t.Fatalf("claim: won=%v err=%v", won, err)
	}
	if err := app.runRingLegTick(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	// The winner's setup fails after the worker has canceled the other phones.
	if err := db.resetAnswerClaim(parent.ID); err != nil {
		t.Fatal(err)
	}
	if err := app.runRingLegTick(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	ringAdvance(t, db, parent.ID, time.Now())
	remaining, err := db.activeRingOffers(parent.ID, parent.ProjectID)
	if err != nil || len(remaining) != 0 {
		t.Fatalf("canceled phones still offered: %+v err=%v", remaining, err)
	}
	if platform.dialCount != 3 {
		t.Fatalf("recovery redialed phones: %d", platform.dialCount)
	}
}

func TestRingUncertainCancellationWaitsForLateProviderIdentity(t *testing.T) {
	app, db, ctx, _ := externalRingFixture(t, "simultaneous")
	parent, _ := db.findCall("external")
	offers, _ := db.activeRingOffers(parent.ID, parent.ProjectID)
	if err := app.startRingLeg(ctx, parent, offers[0]); err != nil {
		t.Fatal(err)
	}
	children, _ := db.listWhere(`ingress_path='ring_group'`)
	child := children[0]
	for _, statement := range []string{
		`UPDATE calls SET carrier_sid='',status='canceled' WHERE ingress_path='ring_group'`,
		`UPDATE call_legs SET status='cancel_pending',ended_at='unchanged'`,
		`UPDATE call_offers SET status='canceled'`,
	} {
		if _, err := db.db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := app.runRingLegTick(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	var ended string
	if err := db.db.QueryRow(`SELECT ended_at FROM call_legs WHERE id=?`, child.ID).Scan(&ended); err != nil || ended != "unchanged" {
		t.Fatalf("unactionable cancellation consumed the work batch: %q %v", ended, err)
	}
	// The authenticated callback may arrive after the local call was canceled.
	if _, err := db.db.Exec(`UPDATE calls SET carrier_sid=? WHERE id=?`, child.CarrierSID, child.ID); err != nil {
		t.Fatal(err)
	}
	if err := app.runRingLegTick(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := db.db.QueryRow(`SELECT status FROM call_legs WHERE id=?`, child.ID).Scan(&status); err != nil || status != "canceled" {
		t.Fatalf("late identity was not cleaned up: %s %v", status, err)
	}
}
func TestRingExternalSequentialFailureAdvancesWithoutRedial(t *testing.T) {
	app, db, ctx, platform := externalRingFixture(t, "sequential")
	parent, _ := db.findCall("external")
	offers, _ := db.activeRingOffers(parent.ID, parent.ProjectID)
	if err := app.startRingLeg(ctx, parent, offers[0]); err != nil {
		t.Fatal(err)
	}
	children, _ := db.listWhere(`ingress_path='ring_group'`)
	if err := db.updateStatus(children[0].ID, "busy", ""); err != nil {
		t.Fatal(err)
	}
	if err := app.runRingLegTick(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	ringAdvance(t, db, parent.ID, time.Now())
	offers, _ = db.activeRingOffers(parent.ID, parent.ProjectID)
	if ringOfferIDs(offers) != "d1" {
		t.Fatal(offers)
	}
	if err := app.startRingLeg(ctx, parent, offers[0]); err != nil {
		t.Fatal(err)
	}
	if platform.dialCount != 2 {
		t.Fatal(platform.dialCount)
	}
}
