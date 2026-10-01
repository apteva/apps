package main

import "testing"

func TestMediaBatchExplicitProjectScopedMetadata(t *testing.T) {
	t.Setenv("APTEVA_PROJECT_ID", "")
	ctx := newTestCtx(t)
	a := &App{}
	for _, pid := range []string{testProj} {
		if err := upsertMedia(ctx.AppDB(), pid, "1", sampleVideoProbe(), "hash", "", "clip.mp4"); err != nil {
			t.Fatal(err)
		}
	}
	_, err := ctx.AppDB().Exec(`UPDATE media SET description=?,description_source=?,description_updated_at=?,audience_rating=? WHERE project_id=?`, "current description", "human", "2026-10-01T10:00:00Z", "mature", testProj)
	if err != nil {
		t.Fatal(err)
	}
	if err := upsertMedia(ctx.AppDB(), "other-project", "3", sampleVideoProbe(), "hash", "", "private.mp4"); err != nil {
		t.Fatal(err)
	}
	result, err := a.toolGetBatch(ctx, map[string]any{"_project_id": testProj, "file_ids": []any{"1", "1", "2", "3"}})
	if err != nil {
		t.Fatal(err)
	}
	out := result.(map[string]any)
	items := out["items"].([]mediaBatchRow)
	missing := out["missing_file_ids"].([]string)
	if len(items) != 1 || len(missing) != 2 || missing[0] != "2" || missing[1] != "3" {
		t.Fatalf("result: %+v", out)
	}
	m := items[0]
	if m.Description != "current description" || m.DescriptionSource != "human" || m.DescriptionUpdatedAt != "2026-10-01T10:00:00Z" || m.ProbeStatus != "ok" || m.AudienceRating != "mature" || m.DurationMS != 12500 {
		t.Fatalf("metadata: %+v", m)
	}
	for _, bad := range []any{[]string{}, []any{1}, []string{""}, make([]string, 101)} {
		if _, err := a.toolGetBatch(ctx, map[string]any{"_project_id": testProj, "file_ids": bad}); err == nil {
			t.Fatalf("accepted invalid input: %#v", bad)
		}
	}
}
