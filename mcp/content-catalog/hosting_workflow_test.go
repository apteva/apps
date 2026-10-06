package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func waitingHosting(t *testing.T) (*App, *sdk.AppCtx, *catalogPlatform, string, string, *HostingIntent) {
	t.Helper()
	a, ctx, p, brand, session := setupCatalog(t)
	id := approvedVideo(t, a, ctx, session, 1)
	p.checksumState = "pending"
	r, err := a.hostingRequest(ctx, map[string]any{"asset_id": id})
	if err != nil {
		t.Fatal(err)
	}
	body := r.(map[string]any)
	if body["pending_checksum"] != true {
		t.Fatalf("checksum not pending: %v", body)
	}
	i := body["hosting_intent"].(*HostingIntent)
	if i.Status != "waiting_checksum" || p.starts != 0 || p.collectionCreates != 0 {
		t.Fatalf("pending request started hosting: %+v, %+v", i, p)
	}
	return a, ctx, p, brand, session, i
}

func forceHostingDue(t *testing.T, ctx *sdk.AppCtx) {
	t.Helper()
	if _, err := ctx.AppDB().Exec(`UPDATE hosting_intents SET next_check_at=''`); err != nil {
		t.Fatal(err)
	}
	if _, err := ctx.AppDB().Exec(`UPDATE hostings SET next_check_at=''`); err != nil {
		t.Fatal(err)
	}
}

func TestChecksumIntentResumesOnEventOnce(t *testing.T) {
	a, ctx, p, _, _, i := waitingHosting(t)
	r, err := a.hostingRequest(ctx, map[string]any{"asset_id": i.AssetID})
	if err != nil {
		t.Fatal(err)
	}
	if r.(map[string]any)["hosting_intent"].(*HostingIntent).ID != i.ID {
		t.Fatal("retry created another intent")
	}
	p.checksumState = "verified"
	event := sdk.Event{ProjectID: "project-a", SourceInstallID: 11, Data: map[string]any{"file_id": "1", "sha256": "sha-1"}}
	if err := a.onStorageChecksumReady(ctx, event); err != nil {
		t.Fatal(err)
	}
	if err := a.onStorageChecksumReady(ctx, event); err != nil {
		t.Fatal(err)
	}
	i, err = hostingIntentByID(ctx.AppDB(), "project-a", i.ID)
	if err != nil {
		t.Fatal(err)
	}
	if i.Status != "submitted" || i.HostingID == "" || p.starts != 1 || p.collectionCreates != 1 {
		t.Fatalf("event resume duplicated or failed: %+v, %+v", i, p)
	}
}

func TestIntentSurvivesRestartAndMissedEvent(t *testing.T) {
	_, ctx, p, _, _, i := waitingHosting(t)
	p.checksumState = "verified"
	forceHostingDue(t, ctx)
	if err := (&App{}).reconcileHosting(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	i, _ = hostingIntentByID(ctx.AppDB(), "project-a", i.ID)
	if i.Status != "submitted" || p.starts != 1 {
		t.Fatalf("restart did not resume: %+v", i)
	}
	// A later tick observes the provider; it never reissues fetch_video.
	if err := (&App{}).reconcileHosting(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	if p.starts != 1 {
		t.Fatal("worker duplicated transfer")
	}
}

func TestResumeRechecksEligibilityAndDestination(t *testing.T) {
	for _, change := range []string{"asset_archived", "session_archived", "approval_revoked", "collection_changed", "connection_changed", "asset_moved"} {
		t.Run(change, func(t *testing.T) {
			a, ctx, p, brand, session, i := waitingHosting(t)
			var err error
			switch change {
			case "asset_archived":
				_, err = ctx.AppDB().Exec(`UPDATE assets SET lifecycle='archived' WHERE id=?`, i.AssetID)
			case "session_archived":
				_, err = ctx.AppDB().Exec(`UPDATE sessions SET lifecycle='archived' WHERE id=?`, session)
			case "approval_revoked":
				_, err = a.assetReview(ctx, map[string]any{"asset_id": i.AssetID, "review_status": "pending"})
			case "collection_changed":
				_, err = a.sessionUpdate(ctx, map[string]any{"id": session, "host_collection_id": "changed"})
			case "connection_changed":
				_, err = ctx.AppDB().Exec(`UPDATE brands SET host_connection_id=99 WHERE id=?`, brand)
			case "asset_moved":
				result, e := a.sessionCreate(ctx, map[string]any{"brand_id": brand, "title": "Other session"})
				if e != nil {
					t.Fatal(e)
				}
				_, err = ctx.AppDB().Exec(`UPDATE assets SET session_id=? WHERE id=?`, result.(map[string]any)["session"].(Session).ID, i.AssetID)
			}
			if err != nil {
				t.Fatal(err)
			}
			p.checksumState = "verified"
			forceHostingDue(t, ctx)
			if err := a.reconcileHosting(context.Background(), ctx); err != nil {
				t.Fatal(err)
			}
			i, _ = hostingIntentByID(ctx.AppDB(), "project-a", i.ID)
			if i.Status != "blocked" || p.starts != 0 || p.collectionCreates != 0 {
				t.Fatalf("invalid intent resumed: %+v", i)
			}
		})
	}
}

func TestCancelledIntentAndUnrequestedEventsNeverUpload(t *testing.T) {
	a, ctx, p, _, _, i := waitingHosting(t)
	if _, err := a.hostingCancel(ctx, map[string]any{"intent_id": i.ID}); err != nil {
		t.Fatal(err)
	}
	p.checksumState = "verified"
	forceHostingDue(t, ctx)
	if err := a.onStorageChecksumReady(ctx, sdk.Event{ProjectID: "project-a", SourceInstallID: 11, Data: map[string]any{"file_id": "1", "sha256": "sha-1"}}); err != nil {
		t.Fatal(err)
	}
	if err := a.reconcileHosting(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	if p.starts != 0 || p.collectionCreates != 0 {
		t.Fatal("cancelled/unrequested event triggered hosting")
	}
}

func TestTransientChecksumReadRetriesAndFailedChecksumStops(t *testing.T) {
	a, ctx, p, _, _, i := waitingHosting(t)
	p.storageReadError = errors.New("Storage temporarily unavailable")
	forceHostingDue(t, ctx)
	if err := a.reconcileHosting(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	i, _ = hostingIntentByID(ctx.AppDB(), "project-a", i.ID)
	if i.Status != "waiting_checksum" || !strings.Contains(i.Error, "temporarily") || i.NextCheckAt == "" {
		t.Fatalf("read failure was not deferred: %+v", i)
	}
	p.storageReadError = nil
	p.checksumState = "failed"
	forceHostingDue(t, ctx)
	ensures := p.checksumEnsures
	if err := a.reconcileHosting(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	i, _ = hostingIntentByID(ctx.AppDB(), "project-a", i.ID)
	if i.Status != "failed" || p.starts != 0 || p.checksumEnsures != ensures {
		t.Fatalf("failed checksum was automatically repaired/uploaded: %+v", i)
	}
}

func TestProviderProgressIsRecordedAndTerminalPollingStops(t *testing.T) {
	a, ctx, p, _, session := setupCatalog(t)
	id := approvedVideo(t, a, ctx, session, 1)
	r, err := a.hostingRequest(ctx, map[string]any{"asset_id": id})
	if err != nil {
		t.Fatal(err)
	}
	h := r.(map[string]any)["hosting"].(*Hosting)
	p.videoBody = map[string]any{"status": 3, "encodeProgress": 42.5, "transcodingMessages": []any{map[string]any{"message": "Encoding 1080p", "level": "info"}}}
	if err := a.reconcileHosting(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	h, err = hostingByID(ctx.AppDB(), "project-a", h.ID)
	if err != nil {
		t.Fatal(err)
	}
	if h.EncodeProgress == nil || *h.EncodeProgress != 42.5 || h.ProviderStatus == nil || *h.ProviderStatus != 3 || h.ProviderStage != "transcoding" || !strings.Contains(string(h.TranscodingMessages), "Encoding 1080p") || h.Status != "processing" {
		t.Fatalf("progress discarded: %+v", h)
	}
	assets, _ := a.assetsList(ctx, map[string]any{"session_id": session})
	summary := assets.(map[string]any)["assets"].([]Asset)[0].Hostings[0]
	if summary.EncodeProgress == nil || *summary.EncodeProgress != 42.5 {
		t.Fatal("card summary dropped progress")
	}
	forceHostingDue(t, ctx)
	p.videoBody = map[string]any{"status": 4, "encodeProgress": 100}
	if err := a.reconcileHosting(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	checks := p.videoChecks
	if err := a.reconcileHosting(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	h, _ = hostingByID(ctx.AppDB(), "project-a", h.ID)
	if h.Status != "ready" || h.NextCheckAt != "" || p.videoChecks != checks {
		t.Fatalf("ready video kept polling: %+v", h)
	}
}

func TestUploadFailureIsTerminalAndProviderErrorsBackOff(t *testing.T) {
	a, ctx, p, _, session := setupCatalog(t)
	id := approvedVideo(t, a, ctx, session, 1)
	r, err := a.hostingRequest(ctx, map[string]any{"asset_id": id})
	if err != nil {
		t.Fatal(err)
	}
	h := r.(map[string]any)["hosting"].(*Hosting)
	p.videoCheckError = errors.New("Bunny temporarily unavailable")
	if err := a.reconcileHosting(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	h, _ = hostingByID(ctx.AppDB(), "project-a", h.ID)
	if h.Status != "processing" || h.CheckError == "" || h.NextCheckAt == "" {
		t.Fatalf("provider read error changed status or was not deferred: %+v", h)
	}
	checks := p.videoChecks
	if err := a.reconcileHosting(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	if p.videoChecks != checks {
		t.Fatal("backoff was ignored")
	}
	p.videoCheckError = nil
	p.videoBody = map[string]any{"status": 6, "encodeProgress": 0}
	forceHostingDue(t, ctx)
	if err := a.reconcileHosting(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	h, _ = hostingByID(ctx.AppDB(), "project-a", h.ID)
	if h.Status != "failed" || h.ProviderStage != "upload_failed" {
		t.Fatalf("upload failure was not terminal: %+v", h)
	}
}

func TestAmbiguousFetchNeverAutomaticallyRetries(t *testing.T) {
	a, ctx, p, _, _, i := waitingHosting(t)
	p.checksumState = "verified"
	p.videoFetchError = errors.New("lost fetch response")
	forceHostingDue(t, ctx)
	if err := a.reconcileHosting(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	i, _ = hostingIntentByID(ctx.AppDB(), "project-a", i.ID)
	if i.Status != "blocked" || p.starts != 1 {
		t.Fatalf("uncertain upload state lost: %+v", i)
	}
	forceHostingDue(t, ctx)
	if err := a.reconcileHosting(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := a.hostingRequest(ctx, map[string]any{"asset_id": i.AssetID}); err != nil {
		t.Fatal(err)
	}
	if p.starts != 1 {
		t.Fatal("uncertain fetch was retried")
	}
}

func TestHostingTitleUsesMediaThenExplicitOverrideAndFallback(t *testing.T) {
	for _, mode := range []string{"media", "explicit", "fallback"} {
		t.Run(mode, func(t *testing.T) {
			a, ctx, p, _, session := setupCatalog(t)
			id := approvedVideo(t, a, ctx, session, 1)
			args := map[string]any{"asset_id": id}
			p.mediaRows = map[string]map[string]any{"1": {"file_id": "1", "title": "Statue on Display"}}
			expected := "Statue on Display"
			if mode == "explicit" {
				args["title"] = "Creator title"
				expected = "Creator title"
			}
			if mode == "fallback" {
				p.mediaRows = map[string]map[string]any{}
				expected = "Shoot — clip-1.mp4"
			}
			if _, err := a.hostingRequest(ctx, args); err != nil {
				t.Fatal(err)
			}
			if p.fetchedTitles[0] != expected {
				t.Fatalf("wrong title: %q", p.fetchedTitles[0])
			}
		})
	}
}

func TestProgressAndIntentReadsAreProjectScopedAndReadOnly(t *testing.T) {
	a, ctx, p, _, session, i := waitingHosting(t)
	r, err := a.hostingsList(ctx, map[string]any{"session_id": session})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.(map[string]any)["hosting_intents"].([]HostingIntent)) != 1 {
		t.Fatal("session lost waiting intent")
	}
	if _, err := a.hostingsList(ctx.WithProject("other"), map[string]any{"asset_id": i.AssetID}); err == nil {
		t.Fatal("cross-project progress leaked")
	}
	if _, err := a.hostingCancel(ctx.WithProject("other"), map[string]any{"intent_id": i.ID}); err == nil {
		t.Fatal("cross-project intent cancelled")
	}
	if p.starts != 0 || p.videoChecks != 0 {
		t.Fatal("read-only progress contacted the provider")
	}
}

func TestApprovalRevokedDuringURLReadPreventsTransfer(t *testing.T) {
	a, ctx, p, _, session := setupCatalog(t)
	id := approvedVideo(t, a, ctx, session, 1)
	p.beforeStorageURL = func() {
		if _, err := a.assetReview(ctx, map[string]any{"asset_id": id, "review_status": "pending"}); err != nil {
			t.Error(err)
		}
	}
	if _, err := a.hostingRequest(ctx, map[string]any{"asset_id": id}); err == nil {
		t.Fatal("revoked approval was not checked before transfer")
	}
	if p.starts != 0 {
		t.Fatal("approval was revoked but the transfer still started")
	}
}

func TestConcurrentCancellationBeforeTransferIsPreserved(t *testing.T) {
	a, ctx, p, _, _, i := waitingHosting(t)
	p.checksumState = "verified"
	p.beforeStorageURL = func() {
		if _, err := (&App{}).hostingCancel(ctx, map[string]any{"intent_id": i.ID}); err != nil {
			t.Error(err)
		}
	}
	forceHostingDue(t, ctx)
	if err := a.reconcileHosting(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	i, _ = hostingIntentByID(ctx.AppDB(), "project-a", i.ID)
	if p.starts != 0 || i.Status != "cancelled" {
		t.Fatalf("concurrent cancellation was overwritten or ignored: %+v", i)
	}
}

func TestWaitingIntentReusesCollectionCreatedByAnotherRequestedAsset(t *testing.T) {
	a, ctx, p, _, session, i := waitingHosting(t)
	other := approvedVideo(t, a, ctx, session, 3)
	p.checksumState = "verified"
	if _, err := a.hostingRequest(ctx, map[string]any{"asset_id": other}); err != nil {
		t.Fatal(err)
	}
	if err := a.onStorageChecksumReady(ctx, sdk.Event{ProjectID: "project-a", SourceInstallID: 11, Data: map[string]any{"file_id": "1", "sha256": "sha-1"}}); err != nil {
		t.Fatal(err)
	}
	i, _ = hostingIntentByID(ctx.AppDB(), "project-a", i.ID)
	if i.Status != "submitted" || p.collectionCreates != 1 || p.starts != 2 {
		t.Fatalf("automatic collection was mistaken for a route change: %+v, %+v", i, p)
	}
}

func TestRestartRecoversChecksumExecutionButDoesNotRedispatchClaimedTransfer(t *testing.T) {
	for _, claimed := range []bool{false, true} {
		name := "checksum_wait"
		if claimed {
			name = "transfer_result_unknown"
		}
		t.Run(name, func(t *testing.T) {
			_, ctx, p, _, _, i := waitingHosting(t)
			if claimed {
				if _, err := ctx.AppDB().Exec(`INSERT INTO hostings(id,project_id,asset_id,provider,connection_id,library_id,source_sha256,status) VALUES('crashed','project-a',?,'bunny',21,'42','sha-1','reserved')`, i.AssetID); err != nil {
					t.Fatal(err)
				}
				if _, err := ctx.AppDB().Exec(`UPDATE hosting_intents SET status='submitted',hosting_id='crashed' WHERE id=?`, i.ID); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := ctx.AppDB().Exec(`UPDATE hosting_intents SET execution_token='abandoned' WHERE id=?`, i.ID); err != nil {
				t.Fatal(err)
			}
			p.checksumState = "verified"
			a := &App{}
			if err := a.OnMount(ctx); err != nil {
				t.Fatal(err)
			}
			forceHostingDue(t, ctx)
			if err := a.reconcileHosting(context.Background(), ctx); err != nil {
				t.Fatal(err)
			}
			i, _ = hostingIntentByID(ctx.AppDB(), "project-a", i.ID)
			if claimed {
				if i.Status != "blocked" || p.starts != 0 {
					t.Fatalf("unknown provider mutation retried after restart: %+v", i)
				}
			} else if i.Status != "submitted" || p.starts != 1 {
				t.Fatalf("abandoned checksum execution did not resume: %+v", i)
			}
		})
	}
}
