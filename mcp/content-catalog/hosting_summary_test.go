package main

import "testing"

func TestCardsExposeRecordedHostingStatesInSessionAndSearch(t *testing.T) {
	a, ctx, platform, _, session := setupCatalog(t)
	video := attach(t, a, ctx, session, 1)
	image := attach(t, a, ctx, session, 2)
	for _, record := range []struct{ id, project, library, status, remote string }{
		{"ready-host", "project-a", "42", "ready", "existing-video"},
		{"processing-host", "project-a", "43", "processing", "processing-video"},
		{"other-project-host", "project-b", "44", "failed", "private-video"},
	} {
		_, err := ctx.AppDB().Exec(`INSERT INTO hostings(id,project_id,asset_id,provider,connection_id,library_id,source_sha256,remote_id,status,last_checked_at) VALUES(?,?,?,'bunny',21,?,'sha-1',?,?, '2026-10-01T09:00:00Z')`, record.id, record.project, video, record.library, record.remote, record.status)
		if err != nil {
			t.Fatal(err)
		}
	}
	assertHostings := func(asset Asset) {
		t.Helper()
		if asset.ID == image {
			if asset.Hostings == nil || len(asset.Hostings) != 0 {
				t.Fatalf("unhosted asset should have an empty hosting list: %+v", asset.Hostings)
			}
			return
		}
		if len(asset.Hostings) != 2 {
			t.Fatalf("expected two project-scoped hosting records: %+v", asset.Hostings)
		}
		states := map[string]HostingSummary{}
		for _, h := range asset.Hostings {
			states[h.Status] = h
		}
		if states["ready"].RemoteID != "existing-video" || states["processing"].RemoteID != "processing-video" || states["ready"].ConnectionID != 21 || states["ready"].LastCheckedAt == "" {
			t.Fatalf("hosting states lost evidence: %+v", asset.Hostings)
		}
	}
	listed, err := a.assetsList(ctx, map[string]any{"session_id": session})
	if err != nil {
		t.Fatal(err)
	}
	for _, asset := range listed.(map[string]any)["assets"].([]Asset) {
		assertHostings(asset)
	}
	for _, hit := range assetHits(t, a, ctx, map[string]any{"entity_type": "assets", "session_id": session}) {
		assertHostings(hit.Asset)
	}
	if platform.starts != 0 {
		t.Fatal("reading card badges started a hosting transfer")
	}
}
