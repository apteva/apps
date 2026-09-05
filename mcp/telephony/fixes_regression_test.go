package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestOutboundRateReservationIsAtomicAcrossAgents(t *testing.T) {
	db := testCallsDB(t)
	t.Setenv("TELEPHONY_MAX_CALLS_PER_MINUTE", "3")
	var wg sync.WaitGroup
	var accepted atomic.Int32
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			row := testCall(fmt.Sprint("rate-", i), "initiated")
			row.ThreadID = row.ID
			row.AgentID = int64(i)
			if db.insertCall(row, true) == nil {
				accepted.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if accepted.Load() != 3 {
		t.Fatalf("accepted %d calls, want 3", accepted.Load())
	}
}

func TestLifecyclePublishFailureRemainsRetryable(t *testing.T) {
	app, ctx := withTelephonyTestContext(t, &answerPlatform{})
	var fail atomic.Bool
	fail.Store(true)
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			w.WriteHeader(503)
		} else {
			w.WriteHeader(204)
		}
	}))
	defer gateway.Close()
	t.Setenv("APTEVA_GATEWAY_URL", gateway.URL)
	t.Setenv("APTEVA_APP_TOKEN", "test-token")
	row := testCall("event-retry", "initiated")
	if err := app.db().insertCall(row); err != nil {
		t.Fatal(err)
	}
	if err := app.db().updateStatus(row.ID, "ringing", ""); err != nil {
		t.Fatal(err)
	}
	if err := app.publishLifecycleEvents(ctx, row.ID); err == nil {
		t.Fatal("failed gateway must not acknowledge event")
	}
	var pending int
	ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM call_events WHERE call_id=? AND published_at=''`, row.ID).Scan(&pending)
	if pending == 0 {
		t.Fatal("event lost after failed publish")
	}
	fail.Store(false)
	if err := app.publishLifecycleEvents(ctx, row.ID); err != nil {
		t.Fatal(err)
	}
	ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM call_events WHERE call_id=? AND published_at=''`, row.ID).Scan(&pending)
	if pending != 0 {
		t.Fatal("successful retry not acknowledged")
	}
}

func TestPlaybackCacheSharesConcurrentFetchAndRanges(t *testing.T) {
	key := t.Name()
	var count atomic.Int32
	var wg sync.WaitGroup
	fetch := func() (string, error) {
		count.Add(1)
		file, err := os.CreateTemp(t.TempDir(), "recording")
		if err != nil {
			return "", err
		}
		_, err = file.Write([]byte("0123456789"))
		file.Close()
		return file.Name(), err
	}
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			file, err := cachedRecording(context.Background(), key, fetch)
			if err != nil {
				t.Error(err)
				return
			}
			defer file.Close()
			buf := make([]byte, 3)
			if _, err := file.ReadAt(buf, 4); err != nil || string(buf) != "456" {
				t.Errorf("range %s: %v", buf, err)
			}
		}()
	}
	wg.Wait()
	if count.Load() != 1 {
		t.Fatalf("fetched %d times", count.Load())
	}
}

func TestDeletedRecordingCannotBeReclaimedOrRevived(t *testing.T) {
	app, ctx := withTelephonyTestContext(t, &answerPlatform{})
	row := testCall("deleted-import", "completed")
	if err := app.db().insertCall(row); err != nil {
		t.Fatal(err)
	}
	rec, err := app.db().upsertTwilioRecording(&row, "RE-deleted", "completed", 1000, 1)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := app.db().claimRecordingImport(row.ProjectID)
	if err != nil || claim == nil {
		t.Fatalf("claim: %v", err)
	}
	// A failing provider cleanup still tombstones the import before returning.
	_ = app.deleteRecording(ctx, rec)
	app.db().failRecordingImport(rec.ID, fmt.Errorf("late worker failure"))
	app.db().markRecordingProviderOnly(rec.ID)
	_, err = app.db().upsertTwilioRecording(&row, "RE-deleted", "completed", 1000, 1)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := app.db().findRecording(rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.DeletedAt == "" || stored.StorageStatus != "deleted" {
		t.Fatalf("recording revived: %+v", stored)
	}
}

func TestBrowserTokenCannotAccessCarrierPeerSocket(t *testing.T) {
	softphoneTestCtx(t)
	app := &App{}
	row := insertSoftphoneCall(t, app, "in-progress")
	req := httptest.NewRequest(http.MethodGet, "/peer/"+row.ID+"/"+row.PeerToken, nil)
	w := httptest.NewRecorder()
	app.handlePeerSocket(w, req)
	if w.Code != 403 {
		t.Fatalf("browser credential authorized peer: %d", w.Code)
	}
}

func TestMP3OnlyAdvertisesPlayableOriginal(t *testing.T) {
	out := recordingPublic(recordingRow{ID: "mp3", ProjectID: "p", Format: "mp3", Channels: 2, ProviderStatus: "completed"})
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), `"mix"`) || !strings.Contains(string(raw), "variant=original") {
		t.Fatalf("MP3 variants: %s", raw)
	}
}

func TestBrowserAudioQueueDoesNotBlockProducer(t *testing.T) {
	// With no consumer the queue must stay bounded and producer must still return.
	pump := &websocketWriterPump{audio: make(chan websocketWriteRequest, 6), done: make(chan struct{})}
	start := time.Now()
	for i := 0; i < 1000; i++ {
		pump.QueueAudio(make([]byte, 960))
	}
	if len(pump.audio) != 6 || time.Since(start) > time.Second {
		t.Fatal("unbounded or blocking browser audio queue")
	}
}

func TestRoutingSaveRejectsForeignIDsWithoutNilSuccess(t *testing.T) {
	app, _ := auditRoutingSetup(t)
	_, err := app.saveRoutingDestination("p1", "owned", "Desk", "browser", map[string]any{}, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.saveRoutingDestination("p2", "owned", "Other", "browser", map[string]any{}, true); err == nil {
		t.Fatal("foreign destination returned successful nil result")
	}
	_, err = app.saveRoutingFlow("p1", "owned-flow", "Flow", "", `{"entry":"end","nodes":[{"id":"end","type":"hangup"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.saveRoutingFlow("p2", "owned-flow", "Other", "", `{"entry":"end","nodes":[{"id":"end","type":"hangup"}]}`); err == nil {
		t.Fatal("foreign flow returned successful nil result")
	}
}

func TestUpgradeFreezesLegacyPublishedDestination(t *testing.T) {
	app, db := auditRoutingSetup(t)
	_, err := app.saveRoutingDestination("p1", "dest", "Desk", "browser", map[string]any{}, true)
	if err != nil {
		t.Fatal(err)
	}
	route := auditPublishedRoute(t, app, db, routingDefinition{Entry: "answer", Nodes: []routingNode{{ID: "answer", Type: "destination", Config: map[string]any{"destination_id": "dest"}}}})
	_, err = db.db.Exec(`UPDATE routing_flow_versions SET definition=json_remove(definition,'$.destinations','$.groups') WHERE id=?`, route.PublishedFlowVersionID)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.freezeExistingRoutingVersions(); err != nil {
		t.Fatal(err)
	}
	_, err = app.saveRoutingDestination("p1", "dest", "Changed", "ai", map[string]any{"agent_id": 8}, true)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := app.resolveInboundRoutingPlan(route, "+12025550101", nil)
	if err != nil {
		t.Fatal(err)
	}
	if plan.AnswerMode != answerModeHumanBrowser {
		t.Fatalf("legacy published destination changed: %s", plan.AnswerMode)
	}
}

func TestIVRDoesNotOfferCallBeforeDestinationSelection(t *testing.T) {
	app, db := auditRoutingSetup(t)
	route := auditPublishedRoute(t, app, db, routingDefinition{Entry: "menu", Nodes: []routingNode{{ID: "menu", Type: "dtmf_menu", Branches: map[string]string{"1": "end", "default": "end"}}, {ID: "end", Type: "hangup"}}})
	call, _, err := app.recordInboundCall(route, "CA-menu", "+12025550101", route.PhoneNumber)
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM inbound_event_outbox WHERE call_id=?`, call.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("IVR offered to an agent before a destination was selected")
	}
	claimed, err := db.claimPendingCallForHuman(call.ID, route.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if claimed {
		t.Fatal("browser bypassed the IVR")
	}
}

func TestDisabledDestinationBlocksNewCallsButPreservesActiveSnapshot(t *testing.T) {
	app, db := auditRoutingSetup(t)
	_, err := app.saveRoutingDestination("p1", "dest", "Desk", "browser", map[string]any{}, true)
	if err != nil {
		t.Fatal(err)
	}
	route := auditPublishedRoute(t, app, db, routingDefinition{Entry: "answer", Nodes: []routingNode{{ID: "answer", Type: "destination", Config: map[string]any{"destination_id": "dest"}}}})
	call, _, err := app.recordInboundCall(route, "CA-active-before-disable", "+12025550101", route.PhoneNumber)
	if err != nil {
		t.Fatal(err)
	}
	_, err = app.saveRoutingDestination("p1", "dest", "Desk", "browser", map[string]any{}, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.resolveInboundRoutingPlan(route, "+12025550101", nil); err == nil {
		t.Fatal("disabled destination accepted new ingress")
	}
	if _, _, err := app.routingPlanForCall(call, nil); err != nil {
		t.Fatalf("active snapshot interrupted: %v", err)
	}
}

func TestStaleImportFailureCannotReplaceNewLease(t *testing.T) {
	db := testCallsDB(t)
	call := testCall("stale-import", "completed")
	if err := db.insertCall(call); err != nil {
		t.Fatal(err)
	}
	rec, err := db.upsertTwilioRecording(&call, "RE-stale", "completed", 1000, 1)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := db.claimRecordingImport(call.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.Exec(`UPDATE recordings SET import_started_at='new-lease' WHERE id=?`, rec.ID); err != nil {
		t.Fatal(err)
	}
	db.failRecordingImport(rec.ID, fmt.Errorf("old worker failed"), claim.ImportStartedAt)
	db.markRecordingProviderOnly(rec.ID, claim.ImportStartedAt)
	current, err := db.findRecording(rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.StorageStatus != "importing" || current.ImportStartedAt != "new-lease" {
		t.Fatalf("stale worker replaced claim: %+v", current)
	}
}

func TestRecordingRetryCannotReplaceImportOrDeletion(t *testing.T) {
	db := testCallsDB(t)
	call := testCall("retry-guard", "completed")
	if err := db.insertCall(call); err != nil {
		t.Fatal(err)
	}
	rec, err := db.upsertTwilioRecording(&call, "RE-retry-guard", "completed", 1000, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.retryRecordingImport(rec.ID); err != nil {
		t.Fatal(err)
	}
	claim, err := db.claimRecordingImport(call.ProjectID)
	if err != nil || claim == nil {
		t.Fatalf("claim: %v, %v", claim, err)
	}
	if err := db.retryRecordingImport(rec.ID); err == nil {
		t.Fatal("retry replaced active import")
	}
	if _, err := db.db.Exec(`UPDATE recordings SET storage_status='deleted',deleted_at='deleted' WHERE id=?`, rec.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.retryRecordingImport(rec.ID); err == nil {
		t.Fatal("retry revived deleted recording")
	}
	current, err := db.findRecording(rec.ID)
	if err != nil || current.StorageStatus != "deleted" {
		t.Fatalf("deleted status changed: %+v, %v", current, err)
	}
}
