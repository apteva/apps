package main

import (
	"errors"
	"testing"
	"time"
)

func TestDescriptionRecoveryBoundsRetriesAndSurvivesReads(t *testing.T) {
	ctx := newTestCtxWithPlatform(t, boundOpenAI())
	upsertMedia(ctx.AppDB(), testProj, "1", sampleImageProbe(), "sha", "", "photo.png")
	m, _ := getMedia(ctx.AppDB(), testProj, "1")
	now := time.Now().UTC()
	failure := descriptionRecoveryFailure(errors.New("Codex stream ended with error"))
	if !failure.Retryable || failure.Code != "stream_failure_unknown" {
		t.Fatalf("failure=%+v", failure)
	}
	for i := 1; i <= 3; i++ {
		claimed, e := claimDescriptionRecovery(ctx.AppDB(), m, 0, 0, now, time.Minute)
		if e != nil || !claimed {
			t.Fatalf("claim %d: %v %v", i, claimed, e)
		}
		if again, _ := claimDescriptionRecovery(ctx.AppDB(), m, 0, 0, now, time.Minute); again {
			t.Fatal("overlapping claim")
		}
		if e := finishDescriptionRecovery(ctx.AppDB(), m, 0, 0, failure, time.Time{}, now); e != nil {
			t.Fatal(e)
		}
		r := getDescriptionRecovery(ctx.AppDB(), m)
		if r.Attempts != i || r.Failure.Code != "stream_failure_unknown" {
			t.Fatalf("recovery=%+v", r)
		}
		if i < 3 {
			if r.State != "retry_wait" || r.NextAttemptAt == "" {
				t.Fatalf("retry state=%+v", r)
			}
		} else if r.State != "exhausted" {
			t.Fatalf("not exhausted: %+v", r)
		}
		now = now.Add(3 * time.Minute)
	}
	if again, _ := claimDescriptionRecovery(ctx.AppDB(), m, 0, 0, now, time.Minute); again {
		t.Fatal("unbounded fourth attempt")
	}
	if e := resetDescriptionRecovery(ctx.AppDB(), testProj, "1"); e != nil {
		t.Fatal(e)
	}
	if claimed, e := claimDescriptionRecovery(ctx.AppDB(), m, 0, 0, now, time.Minute); e != nil || !claimed {
		t.Fatal("manual retry not reset", e)
	}
	permanent := descriptionRecoveryFailure(errors.New("HTTP 401 unauthorized"))
	finishDescriptionRecovery(ctx.AppDB(), m, 0, 0, permanent, time.Time{}, now)
	if r := getDescriptionRecovery(ctx.AppDB(), m); r.State != "failed" || r.Failure.Retryable {
		t.Fatalf("permanent error=%+v", r)
	}
	if again, _ := claimDescriptionRecovery(ctx.AppDB(), m, 0, 0, now.Add(time.Hour), time.Minute); again {
		t.Fatal("permanent error retried")
	}
}
func TestDescriptionRecoveryHonorsProviderResetAndSourceChange(t *testing.T) {
	ctx := newTestCtxWithPlatform(t, boundOpenAI())
	upsertMedia(ctx.AppDB(), testProj, "1", sampleImageProbe(), "sha", "", "photo.png")
	m, _ := getMedia(ctx.AppDB(), testProj, "1")
	now := time.Now().UTC()
	claimDescriptionRecovery(ctx.AppDB(), m, 0, 0, now, time.Minute)
	reset := now.Add(2 * time.Hour)
	f := descriptionRecoveryFailure(errors.New("HTTP 429 too many requests"))
	finishDescriptionRecovery(ctx.AppDB(), m, 0, 0, f, reset, now)
	r := getDescriptionRecovery(ctx.AppDB(), m)
	at, _ := time.Parse(time.RFC3339, r.NextAttemptAt)
	if at.Before(reset) {
		t.Fatal("provider reset shortened")
	}
	if claimed, _ := claimDescriptionRecovery(ctx.AppDB(), m, 0, 0, now.Add(time.Hour), time.Minute); claimed {
		t.Fatal("early retry")
	}
	upsertMedia(ctx.AppDB(), testProj, "1", sampleImageProbe(), "new-sha", "", "photo.png")
	m, _ = getMedia(ctx.AppDB(), testProj, "1")
	if claimed, e := claimDescriptionRecovery(ctx.AppDB(), m, 0, 0, now, time.Minute); e != nil || !claimed {
		t.Fatal("new source inherited retries", e)
	}
}

func TestDescriptionRecoveryClassifiesFlattenedStreamFailure(t *testing.T) {
	f := descriptionRecoveryFailure(errors.New("read Codex response: stream error: stream ID 1; INTERNAL_ERROR; received from peer"))
	if !f.Retryable || f.Code != "transport_error" {
		t.Fatalf("description recovery missed stream failure: %+v", f)
	}
}
