package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Existing request-level hits bypass Smart Crop entirely. Seed the exact
// released v0.14.7 key format, then verify that an upgrade refuses those output
// files for crop operations but also refuses the earlier approximate trim output.
func TestSmartCropUpgradeInvalidatesRequestCacheOnlyForCropping(t *testing.T) {
	app := newTestCtx(t)
	source := StorageFile{ID: 1, Name: "source.mp4", SHA256: strings.Repeat("a", 64), SizeBytes: 100}
	output := StorageFile{ID: 99, Name: "out.mp4", Folder: "/renders/", SHA256: strings.Repeat("b", 64), SizeBytes: 50}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		file := source
		if strings.HasSuffix(r.URL.Path, "/99") {
			file = output
		}
		json.NewEncoder(w).Encode(map[string]any{"file": file})
	}))
	defer srv.Close()
	sc := &storageClient{base: srv.URL, httpClient: srv.Client()}
	executor := &localExecutor{ffmpegPath: "/bin/sh"}
	for _, op := range []string{"trim", "crop", "extract_frame", "extract_reel"} {
		t.Run(op, func(t *testing.T) {
			output.Name = "out.mp4"
			if op == "extract_frame" {
				output.Name = "out.png"
			}
			params, _ := json.Marshal(map[string]any{"start_ms": 1000, "end_ms": 2000, "at_ms": 1000, "target_ratio": "9:16"})
			row := &RenderRow{ProjectID: testProj, Operation: op, SourceFileIDs: []string{"1"}, Params: params, OutputName: output.Name, OutputFolder: "/renders/"}
			// This tuple is the immutable released key contract, not the candidate
			// revision logic. A source absent from Media has null crop evidence.
			identity := []any{executableIdentity(executor.ffmpegPath), app.Config().Get("render_encoder_threads")}
			sources := []any{[]any{"1", source.SHA256, source.SizeBytes, nil}}
			legacy, _ := json.Marshal([]any{"media-audit-1", sc.base, testProj, executor.Name(), identity, op, row.Params, sources, "/renders/", output.Name})
			legacyKey := fmt.Sprintf("%x", sha256.Sum256(legacy))
			if _, err := app.AppDB().Exec(`INSERT OR REPLACE INTO render_result_cache(cache_key,project_id,storage_file_id,sha256,size_bytes) VALUES(?,?,?,?,?)`, legacyKey, testProj, output.ID, output.SHA256, output.SizeBytes); err != nil {
				t.Fatal(err)
			}
			key, folder, name := requestRenderCacheKey(context.Background(), app, sc, row, executor)
			if key == "" {
				t.Fatal("request cache key missing")
			}
			got := findCachedRender(context.Background(), app, sc, key, testProj, folder, name)
			if got != 0 {
				t.Fatalf("earlier cropped output bypassed corrected analysis: file=%d", got)
			}
		})
	}
}
