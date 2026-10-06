package main

import (
	"testing"
	"time"
)

func TestAssetPublicationsAreIndependentAndVerifiedOnlyWithEvidence(t *testing.T) {
	a, ctx, _, _, session := setupCatalog(t)
	asset := attach(t, a, ctx, session, 1)
	first, err := a.assetPublicationRecord(ctx, map[string]any{"asset_id": asset, "destination": "instagram", "status": "planned"})
	if err != nil {
		t.Fatal(err)
	}
	firstID := first.(map[string]any)["publication_id"].(string)
	if _, err := a.assetPublicationRecord(ctx, map[string]any{"asset_id": asset, "destination": "instagram", "status": "planned"}); err != nil {
		t.Fatalf("second post on same platform rejected: %v", err)
	}
	if _, err := a.assetPublicationRecord(ctx, map[string]any{"asset_id": asset, "publication_id": firstID, "status": "verified_published", "actual_at": time.Now().UTC().Format(time.RFC3339), "external_url": "https://example.com/post"}); err == nil {
		t.Fatal("verified post accepted without evidence source")
	}
	when := time.Now().UTC().Format(time.RFC3339)
	if _, err := a.assetPublicationRecord(ctx, map[string]any{"asset_id": asset, "publication_id": firstID, "status": "verified_published", "actual_at": when, "external_url": "https://example.com/post", "evidence_source": "creator_page_manual"}); err != nil {
		t.Fatal(err)
	}
	got, err := a.assetPublicationsList(ctx, map[string]any{"asset_id": asset})
	if err != nil {
		t.Fatal(err)
	}
	items := got.(map[string]any)["publications"].([]Publication)
	if len(items) != 2 {
		t.Fatalf("publications = %#v", items)
	}
	var verified int
	for _, p := range items {
		if p.Status == "verified_published" {
			verified++
		}
	}
	if verified != 1 {
		t.Fatalf("verified publications = %d", verified)
	}
	other := ctx.WithProject("another-project")
	if _, err := a.assetPublicationsList(other, map[string]any{"asset_id": asset}); err == nil {
		t.Fatal("cross-project asset publication read succeeded")
	}
}
