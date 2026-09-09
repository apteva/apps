package main

import (
	"encoding/base64"
	"encoding/json"
	"testing"
)

func TestMediaAssetSourceUsesOwnStorageAndScopesGeneration(t *testing.T) {
	bytes := []byte("retained original source")
	file, _ := json.Marshal(map[string]any{"id": 91, "content_base64": base64.StdEncoding.EncodeToString(bytes), "content_type": "image/png", "size_bytes": len(bytes)})
	pf := &recordingPlatform{perAppCallResults: map[string]json.RawMessage{"storage:files_get_content": file}}
	ctx := newMediaStudioCtx(t, pf)
	id, e := insertGenerationWith(ctx.AppDB(), generationRecord{ProjectID: "test-proj", Kind: KindImage, Prompt: "sprite", StorageIDs: []int64{91}, Count: 1, Status: "ready", RequestJSON: `{"cache_key":"stable-marker"}`})
	if e != nil {
		t.Fatal(e)
	}
	out, e := (&App{}).toolMediaAssetSource(ctx, map[string]any{"id": id})
	if e != nil {
		t.Fatal(e)
	}
	r := out.(map[string]any)
	if r["generation_id"] != id || r["storage_id"] != int64(91) || r["content_base64"] != base64.StdEncoding.EncodeToString(bytes) {
		t.Fatal(r)
	}
	if len(pf.callAppCalls) != 1 || pf.callAppCalls[0].AppName != "storage" || pf.callAppCalls[0].Tool != "files_get_content" {
		t.Fatal("wrong binding")
	}
	other, e := insertGenerationWith(ctx.AppDB(), generationRecord{ProjectID: "other", Kind: KindImage, StorageIDs: []int64{91}, Status: "ready"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = (&App{}).toolMediaAssetSource(ctx, map[string]any{"id": other}); e == nil {
		t.Fatal("cross-project export")
	}
	if _, e = (&App{}).toolMediaAssetSource(ctx, map[string]any{"id": id, "index": -1}); e == nil {
		t.Fatal("invalid output index")
	}
	if _, e = ctx.AppDB().Exec(`UPDATE generations SET status='draft' WHERE id=?`, id); e != nil {
		t.Fatal(e)
	}
	if _, e = (&App{}).toolMediaAssetSource(ctx, map[string]any{"id": id}); e == nil {
		t.Fatal("draft exported")
	}
}
