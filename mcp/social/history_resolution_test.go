package main

import (
	"encoding/json"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

// playlistEntryFixture answers the targeted uploads-playlist lookup
// (list_playlist_items with videoId) for the persistently omitted video.
type playlistEntryFixture struct {
	*metadataHistoryFixture
	entry   string // deleted | normal | absent | foreign | denied
	lookups int
}

func newPlaylistEntryFixture(entry string) *playlistEntryFixture {
	base := newMetadataHistoryFixture()
	base.missing = map[string]bool{"v-126": true}
	base.permanent = true
	return &playlistEntryFixture{metadataHistoryFixture: base, entry: entry}
}

func (p *playlistEntryFixture) ExecuteIntegrationTool(conn int64, tool string, args map[string]any) (*sdk.ExecuteResult, error) {
	if tool != "list_playlist_items" || args["videoId"] == nil {
		return p.metadataHistoryFixture.ExecuteIntegrationTool(conn, tool, args)
	}
	p.lookups++
	id := toString(args["videoId"])
	snippet := map[string]any{"channelId": p.owner, "title": "Native title " + id, "videoOwnerChannelId": p.owner}
	details := map[string]any{"videoId": id, "videoPublishedAt": "2024-01-01T00:00:00Z"}
	status := map[string]any{"privacyStatus": "public"}
	switch p.entry {
	case "deleted":
		snippet = map[string]any{"channelId": p.owner, "title": "Deleted video"}
		details = map[string]any{"videoId": id}
		status = map[string]any{"privacyStatus": "privacyStatusUnspecified"}
	case "absent":
		return historyFixtureResult(map[string]any{"items": []any{}}), nil
	case "foreign":
		snippet["videoOwnerChannelId"] = "foreign-owner"
	case "denied":
		return &sdk.ExecuteResult{Success: false, Status: 403, Data: json.RawMessage(`{"error":"forbidden"}`)}, nil
	}
	return historyFixtureResult(map[string]any{"items": []any{map[string]any{"snippet": snippet, "contentDetails": details, "status": status}}}), nil
}

func seenResolution(t *testing.T, ctx *sdk.AppCtx, runID, id string) (string, map[string]any) {
	t.Helper()
	var resolution, raw string
	if err := ctx.AppDB().QueryRow(`SELECT metadata_resolution,metadata_evidence FROM social_history_seen WHERE run_id=? AND native_post_id=?`, runID, id).Scan(&resolution, &raw); err != nil {
		t.Fatal(err)
	}
	evidence := map[string]any{}
	json.Unmarshal([]byte(raw), &evidence)
	return resolution, evidence
}

func TestHistory127WithDeletedPlaylistEntryIsExplicitlyUnavailable(t *testing.T) {
	pf := newPlaylistEntryFixture("deleted")
	ctx := newSocialCtx(t, pf)
	account := historyAccountFixture(t, ctx, pf.owner)
	r := finishHistory(t, ctx, account)
	metadata := r.Coverage["metadata"].(map[string]any)
	if metadata["unavailable_posts"] != 1 || metadata["unresolved_posts"] != 0 || metadata["readable_posts"] != 126 {
		t.Fatalf("metadata coverage = %+v", metadata)
	}
	if r.Coverage["complete"] != true || r.Coverage["duplicate_protection"].(map[string]any)["complete"] != true {
		t.Fatalf("explained unavailability should complete coverage: %+v", r.Coverage)
	}
	resolution, evidence := seenResolution(t, ctx, r.RunID, "v-126")
	if resolution != "unavailable" || evidence["reason"] != "playlist_entry_without_video_resource" || evidence["observed_at"] == "" {
		t.Fatalf("resolution=%q evidence=%+v", resolution, evidence)
	}
	if pf.lookups != 1 || pf.retries["v-126"] != historyMetadataAttemptsPerPost {
		t.Fatalf("diagnosis must follow the bounded readbacks once: lookups=%d retries=%d", pf.lookups, pf.retries["v-126"])
	}
	if !strings.Contains(strings.Join(r.Limitations, " "), "not treated as deleted") {
		t.Fatalf("limitations = %v", r.Limitations)
	}
	var kept int
	ctx.AppDB().QueryRow(`SELECT count(*) FROM social_native_posts WHERE native_post_id='v-126'`).Scan(&kept)
	if kept != 1 {
		t.Fatal("unavailable video was removed from history")
	}
	if pf.writes != 0 {
		t.Fatalf("history sync wrote upstream %d times", pf.writes)
	}
}

func TestHistoryUnexplainedOmissionStaysVisiblyIncomplete(t *testing.T) {
	for _, entry := range []string{"normal", "absent"} {
		t.Run(entry, func(t *testing.T) {
			pf := newPlaylistEntryFixture(entry)
			ctx := newSocialCtx(t, pf)
			account := historyAccountFixture(t, ctx, pf.owner)
			r := finishHistory(t, ctx, account)
			metadata := r.Coverage["metadata"].(map[string]any)
			if r.Status != "limited" || r.Coverage["complete"] != false || metadata["unresolved_posts"] != 1 || metadata["unavailable_posts"] != 0 {
				t.Fatalf("status=%s coverage=%+v", r.Status, r.Coverage)
			}
			if r.Coverage["duplicate_protection"].(map[string]any)["complete"] != false {
				t.Fatal("matching listing counts must not complete duplicate protection")
			}
			if r.Coverage["enumeration"].(map[string]any)["listed_unique_posts"] != 127 {
				t.Fatalf("enumeration = %+v", r.Coverage["enumeration"])
			}
			resolution, evidence := seenResolution(t, ctx, r.RunID, "v-126")
			want := map[string]string{"normal": "playlist_entry_present_video_lookup_omitted", "absent": "playlist_entry_absent"}[entry]
			if resolution != "unresolved" || evidence["reason"] != want {
				t.Fatalf("resolution=%q evidence=%+v", resolution, evidence)
			}
		})
	}
}

func TestHistoryOmittedVideoLaterRecoversInNewRun(t *testing.T) {
	pf := newPlaylistEntryFixture("normal")
	ctx := newSocialCtx(t, pf)
	account := historyAccountFixture(t, ctx, pf.owner)
	if first := finishHistory(t, ctx, account); first.Status != "limited" {
		t.Fatalf("first run = %s", first.Status)
	}
	pf.missing = map[string]bool{}
	second := finishHistory(t, ctx, account)
	if second.Status != "complete" || second.Coverage["complete"] != true {
		t.Fatalf("recovered run = %s %+v", second.Status, second.Coverage)
	}
	if resolution, _ := seenResolution(t, ctx, second.RunID, "v-126"); resolution != "readable" {
		t.Fatalf("recovered resolution = %q", resolution)
	}
}

func TestHistoryResolutionSurvivesRestartWithoutRediagnosis(t *testing.T) {
	pf := newPlaylistEntryFixture("deleted")
	ctx := newSocialCtx(t, pf)
	account := historyAccountFixture(t, ctx, pf.owner)
	r := finishHistory(t, ctx, account)
	b, err := loadHistoryAccount(ctx, account)
	if err != nil {
		t.Fatal(err)
	}
	// A fresh App (restart) resumes from the persisted ledger: the resolved
	// ID is neither retried nor re-diagnosed.
	run := storedMetadataRun(t, ctx, r.RunID)
	if err := (&App{}).reconcileHistoryMetadata(ctx, b, &run); err != nil {
		t.Fatal(err)
	}
	if pf.lookups != 1 || pf.retries["v-126"] != historyMetadataAttemptsPerPost || run.MetadataUnavailable != 1 || run.MetadataRetryable != 0 {
		t.Fatalf("restart re-read a resolved ID: lookups=%d retries=%d run=%+v", pf.lookups, pf.retries["v-126"], run)
	}
}

func TestYouTubeOmissionDiagnosisRejectsWrongChannelAndKeepsAuthFailures(t *testing.T) {
	for _, entry := range []string{"foreign", "denied"} {
		pf := newPlaylistEntryFixture(entry)
		ctx := newSocialCtx(t, pf)
		account := historyAccountFixture(t, ctx, pf.owner)
		b, _ := loadHistoryAccount(ctx, account)
		run := historyRun{Owner: pf.owner, Resource: "uploads-" + pf.owner}
		if _, err := (&App{}).diagnoseOmittedYouTubeVideo(ctx, b, &run, "v-126"); err == nil {
			t.Fatalf("%s: diagnosis should fail", entry)
		}
	}
}

func TestHistoryMissingOptionalFileMetadataStaysReadable(t *testing.T) {
	pf := newHistoryFixture(12)
	pf.omitMetadata = true // no fileDetails / processingDetails
	ctx := newSocialCtx(t, pf)
	account := historyAccountFixture(t, ctx, pf.owner)
	r := finishHistory(t, ctx, account)
	metadata := r.Coverage["metadata"].(map[string]any)
	if r.Coverage["complete"] != true || metadata["unresolved_posts"] != 0 || metadata["readable_posts"] != 12 {
		t.Fatalf("optional fields made identified videos unresolved: %+v", r.Coverage)
	}
}
