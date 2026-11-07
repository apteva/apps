package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func mediaTiming(ms int64, sha string) map[string]map[string]any {
	return map[string]map[string]any{"1": {"file_id": "1", "duration_ms": ms, "source_sha256": sha, "probe_status": "ok", "audience_rating": "general"}}
}

// hostedVideo requests hosting and lets the worker observe Bunny as ready.
func hostedVideo(t *testing.T, length int64, media map[string]map[string]any) (*App, *sdk.AppCtx, *catalogPlatform, *Hosting) {
	t.Helper()
	a, ctx, p, _, session := setupCatalog(t)
	p.mediaRows = media
	id := approvedVideo(t, a, ctx, session, 1)
	r, err := a.hostingRequest(ctx, map[string]any{"asset_id": id})
	if err != nil {
		t.Fatal(err)
	}
	h := r.(map[string]any)["hosting"].(*Hosting)
	p.videoBody = map[string]any{"status": 4, "length": length}
	forceHostingDue(t, ctx)
	if err := a.reconcileHosting(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	h, err = hostingByID(ctx.AppDB(), "project-a", h.ID)
	if err != nil {
		t.Fatal(err)
	}
	return a, ctx, p, h
}

// forceDeliveryDue makes scheduled checks due without creating schedules for
// rows that have none (historical ready records stay unswept).
func forceDeliveryDue(t *testing.T, ctx *sdk.AppCtx) {
	t.Helper()
	if _, err := ctx.AppDB().Exec(`UPDATE hostings SET next_check_at='2000-01-01T00:00:00Z' WHERE next_check_at<>''`); err != nil {
		t.Fatal(err)
	}
}

func receiptOf(t *testing.T, h *Hosting) DeliveryReceipt {
	t.Helper()
	var r DeliveryReceipt
	if err := json.Unmarshal(h.Delivery, &r); err != nil {
		t.Fatalf("receipt: %v (%s)", err, h.Delivery)
	}
	return r
}

func TestRoundedProviderDurationVerifiesDelivery(t *testing.T) {
	// Ashley's case: Media measured 94.4 s, Bunny reports whole seconds.
	for _, tc := range []struct {
		name      string
		sourceMS  int64
		length    int64
		status    string
		reason    string
		container int64
	}{
		{"rounded_down", 94400, 94, "verified", "", 0},
		{"rounded_up", 94400, 95, "verified", "", 0},
		{"floored_almost_a_second", 94999, 94, "verified", "", 0},
		{"audio_container_tail", 95300, 94, "verified", "", 300},
		{"truncated_upload", 96600, 94, "failed", "duration_mismatch", 1600},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, p, h := hostedVideo(t, tc.length, mediaTiming(tc.sourceMS, "sha-1"))
			r := receiptOf(t, h)
			if h.Status != "ready" || h.DeliveryStatus != tc.status || h.DeliveryReason != tc.reason || r.Duration.ContainerDifferenceMS != tc.container {
				t.Fatalf("delivery %s/%s, receipt %+v", h.DeliveryStatus, h.DeliveryReason, r)
			}
			if r.Duration.SourceMS != tc.sourceMS || r.Duration.ProviderSeconds != tc.length || r.Duration.ToleranceMS != providerRoundingMS+containerToleranceMS {
				t.Fatalf("measured durations missing: %+v", r.Duration)
			}
			if tc.status == "verified" && (h.DeliveryVerifiedAt == "" || r.Source.HashMatch != "verified" || !r.Destination.GUIDConfirmed || !r.Destination.LibraryConfirmed || h.NextCheckAt != "") {
				t.Fatalf("verified receipt incomplete: %+v next=%q", r, h.NextCheckAt)
			}
			if tc.container > 0 && tc.status == "verified" && len(r.Warnings) == 0 {
				t.Fatal("container difference not reported separately")
			}
			if p.starts != 1 {
				t.Fatalf("verification uploaded again: %d", p.starts)
			}
		})
	}
}

func TestDelayedSourceTimingRetriesWithoutUploading(t *testing.T) {
	a, ctx, p, h := hostedVideo(t, 94, map[string]map[string]any{})
	if h.Status != "ready" || h.DeliveryStatus != "pending" || h.DeliveryReason != "source_timing_unavailable" || h.NextCheckAt == "" {
		t.Fatalf("missing timing not pending with retry: %+v", h)
	}
	checks := p.videoChecks
	if err := a.reconcileHosting(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	if p.videoChecks != checks {
		t.Fatal("delivery retry ignored backoff")
	}
	p.mediaRows = mediaTiming(94400, "sha-1")
	forceDeliveryDue(t, ctx)
	if err := a.reconcileHosting(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	h, _ = hostingByID(ctx.AppDB(), "project-a", h.ID)
	if h.DeliveryStatus != "verified" || h.NextCheckAt != "" || p.starts != 1 {
		t.Fatalf("retry did not verify or duplicated upload: %+v starts=%d", h, p.starts)
	}
	checks = p.videoChecks
	forceDeliveryDue(t, ctx)
	if err := a.reconcileHosting(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	if p.videoChecks != checks {
		t.Fatal("verified delivery kept polling")
	}
}

func TestTransientReadFailuresKeepDeliveryPending(t *testing.T) {
	a, ctx, p, h := hostedVideo(t, 94, mediaTiming(94400, "sha-1"))
	if h.DeliveryStatus != "verified" {
		t.Fatalf("setup: %+v", h)
	}
	// A manual re-check during a Storage outage stays pending and retries.
	p.storageReadError = errors.New("storage timeout")
	r, err := a.hostingCheck(ctx, map[string]any{"id": h.ID})
	if err != nil {
		t.Fatal(err)
	}
	receipt := r.(map[string]any)["delivery"].(DeliveryReceipt)
	if receipt.DeliveryStatus != "pending" || receipt.Reason != "storage_checksum_unavailable" || receipt.NextCheckAt == "" {
		t.Fatalf("storage outage not pending: %+v", receipt)
	}
	p.storageReadError = nil
	p.videoCheckError = errors.New("Bunny 502")
	forceDeliveryDue(t, ctx)
	if err := a.reconcileHosting(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	h, _ = hostingByID(ctx.AppDB(), "project-a", h.ID)
	if h.Status != "ready" || h.DeliveryStatus != "pending" || h.CheckError == "" || h.NextCheckAt == "" {
		t.Fatalf("provider outage changed state or was not deferred: %+v", h)
	}
	p.videoCheckError = nil
	forceDeliveryDue(t, ctx)
	if err := a.reconcileHosting(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	h, _ = hostingByID(ctx.AppDB(), "project-a", h.ID)
	if h.DeliveryStatus != "verified" || p.starts != 1 {
		t.Fatalf("recovery failed or uploaded again: %+v starts=%d", h, p.starts)
	}
}

func TestSourceHashMismatchFailsDelivery(t *testing.T) {
	a, ctx, p, h := hostedVideo(t, 94, mediaTiming(94400, "sha-1"))
	// The master in Storage was replaced after this upload.
	if _, err := ctx.AppDB().Exec(`UPDATE hostings SET source_sha256='sha-old' WHERE id=?`, h.ID); err != nil {
		t.Fatal(err)
	}
	r, err := a.hostingCheck(ctx, map[string]any{"id": h.ID})
	if err != nil {
		t.Fatal(err)
	}
	receipt := r.(map[string]any)["delivery"].(DeliveryReceipt)
	h = r.(map[string]any)["hosting"].(*Hosting)
	if h.Status != "ready" || h.DeliveryStatus != "failed" || receipt.Reason != "source_hash_mismatch" || receipt.Source.HashMatch != "mismatch" || receipt.Source.HostedSHA256 != "sha-old" || receipt.Source.CurrentSHA256 != "sha-1" || h.NextCheckAt != "" || p.starts != 1 {
		t.Fatalf("hash mismatch not failed: %+v / %+v", h, receipt)
	}
}

func TestMediaTimingForOtherBytesIsNotEvidence(t *testing.T) {
	_, _, _, h := hostedVideo(t, 94, mediaTiming(94400, "sha-other"))
	if h.DeliveryStatus != "pending" || h.DeliveryReason != "source_timing_not_hash_bound" {
		t.Fatalf("unbound timing accepted: %+v", h)
	}
}

func TestHistoricalReadyHostingsAreNotSwept(t *testing.T) {
	a, ctx, p, h := hostedVideo(t, 94, mediaTiming(94400, "sha-1"))
	if _, err := ctx.AppDB().Exec(`UPDATE hostings SET delivery_status='pending',delivery_reason='not_checked',delivery_receipt='{}',next_check_at='' WHERE id=?`, h.ID); err != nil {
		t.Fatal(err)
	}
	checks := p.videoChecks
	if err := a.reconcileHosting(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	if p.videoChecks != checks {
		t.Fatal("background checker swept a historical record")
	}
}

func TestLinkedExistingVideoIsQueuedForDelivery(t *testing.T) {
	a, ctx, p, _, session := setupCatalog(t)
	p.mediaRows = mediaTiming(34200, "sha-1")
	asset := attach(t, a, ctx, session, 1)
	r, err := a.hostingLinkExisting(ctx, map[string]any{"asset_id": asset, "remote_id": "video-1", "connection_id": int64(21)})
	if err != nil {
		t.Fatal(err)
	}
	h := r.(map[string]any)["hosting"].(*Hosting)
	if h.DeliveryStatus != "pending" || h.DeliveryReason != "queued" || h.NextCheckAt == "" {
		t.Fatalf("link not queued: %+v", h)
	}
	forceDeliveryDue(t, ctx)
	if err := a.reconcileHosting(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	h, _ = hostingByID(ctx.AppDB(), "project-a", h.ID)
	if h.DeliveryStatus != "verified" || p.starts != 0 {
		t.Fatalf("linked video not verified or fetched: %+v starts=%d", h, p.starts)
	}
	assets, _ := a.assetsList(ctx, map[string]any{"session_id": session})
	if s := assets.(map[string]any)["assets"].([]Asset)[0].Hostings[0]; s.DeliveryStatus != "verified" {
		t.Fatalf("card summary missing delivery: %+v", s)
	}
}

func TestProviderHashAndUnresolvedReasons(t *testing.T) {
	sha := strings.Repeat("ab", 32)
	h := &Hosting{Provider: "bunny", ConnectionID: 21, LibraryID: "42", RemoteID: "video-1", SourceSHA256: sha}
	obs := HostObservation{Status: "ready", GUIDConfirmed: true, ReportedLibraryID: "42", DurationSeconds: 10, DurationReported: true, ProviderHash: sha}
	ev := deliverySourceEvidence{ChecksumStatus: "verified", CurrentSHA256: sha, MediaBound: true, MediaFound: true, MediaSHA256: sha, MediaProbe: "ok", MediaMS: 10200}
	if r := evaluateDelivery(h, obs, ev, "t"); r.DeliveryStatus != "verified" || r.Source.ProviderHashMatch != "verified" {
		t.Fatalf("matching provider hash: %+v", r)
	}
	obs.ProviderHash = strings.Repeat("cd", 32)
	if r := evaluateDelivery(h, obs, ev, "t"); r.DeliveryStatus != "verified" || r.Source.ProviderHashMatch != "unrecognized" || len(r.Warnings) == 0 {
		t.Fatalf("undocumented provider hash must be recorded, not trusted: %+v", r)
	}
	obs.ProviderHash = ""
	if r := evaluateDelivery(h, obs, ev, "t"); r.Source.ProviderHashMatch != "unavailable" {
		t.Fatalf("absent provider hash: %+v", r)
	}
	noMedia := ev
	noMedia.MediaBound = false
	r := evaluateDelivery(h, obs, noMedia, "t")
	if r.DeliveryStatus != "pending" || r.Reason != "media_not_bound" || nextDeliveryCheck(&r, 1) != "" {
		t.Fatalf("unbound Media should stop retries: %+v", r)
	}
	noLength := obs
	noLength.DurationReported = false
	if r := evaluateDelivery(h, noLength, ev, "t"); r.Reason != "provider_duration_unavailable" {
		t.Fatalf("missing provider length: %+v", r)
	}
	processing := obs
	processing.Status = "processing"
	if r := evaluateDelivery(h, processing, ev, "t"); r.DeliveryStatus != "pending" || r.Reason != "provider_processing" {
		t.Fatalf("readiness and delivery conflated: %+v", r)
	}
	failedObs := obs
	failedObs.Status = "failed"
	if r := evaluateDelivery(h, failedObs, ev, "t"); r.DeliveryStatus != "failed" || r.Reason != "provider_failed" {
		t.Fatalf("provider failure: %+v", r)
	}
	pending := DeliveryReceipt{DeliveryStatus: "pending", ProviderReadiness: "ready", Reason: "source_timing_unavailable"}
	if nextDeliveryCheck(&pending, deliveryMaxAttempts) != "" || len(pending.Warnings) == 0 {
		t.Fatal("retries are not bounded")
	}
}
