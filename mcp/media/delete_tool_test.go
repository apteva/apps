package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
)

func TestToolDelete_RemovesStorageAndMediaRows(t *testing.T) {
	ctx := newTestCtx(t)
	if err := upsertMedia(ctx.AppDB(), testProj, "1", sampleAudioProbe(), "sha", "/clips/", "bad.mp3"); err != nil {
		t.Fatal(err)
	}
	upsertDerivation(ctx.AppDB(), testProj, "1", "thumbnail", 100, 320, 240, 0)
	upsertDerivation(ctx.AppDB(), testProj, "1", "waveform", 101, 800, 100, 0)
	upsertTranscript(ctx.AppDB(), &TranscriptRow{
		FileID: "1", ProjectID: testProj, Status: "ok", Text: "x",
	})

	var deletedIDs []int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/apps/storage/mcp" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var rpc struct {
			Params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&rpc); err != nil {
			t.Fatalf("decode rpc: %v", err)
		}
		if rpc.Params.Name != "files_delete" {
			t.Fatalf("tool=%q, want files_delete", rpc.Params.Name)
		}
		id, _ := rpc.Params.Arguments["id"].(float64)
		deletedIDs = append(deletedIDs, int64(id))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":{"content":[{"type":"text","text":"{\"deleted\":true}"}]}}`))
	}))
	defer srv.Close()
	t.Setenv("APTEVA_GATEWAY_URL", srv.URL)
	t.Setenv("APTEVA_OUTBOUND_TOKEN", "tok")

	out, err := (&App{}).toolDelete(ctx, map[string]any{
		"_project_id": testProj,
		"file_id":     "1",
	})
	if err != nil {
		t.Fatal(err)
	}
	resp := out.(map[string]any)
	if resp["deleted"] != true || resp["found"] != true || resp["storage_deleted"] != true {
		t.Fatalf("unexpected response: %#v", resp)
	}

	slices.Sort(deletedIDs)
	wantIDs := []int64{1, 100, 101}
	if !slices.Equal(deletedIDs, wantIDs) {
		t.Fatalf("deleted ids=%v, want %v", deletedIDs, wantIDs)
	}
	if _, err := getMedia(ctx.AppDB(), testProj, "1"); !notFound(err) {
		t.Errorf("media row should be gone")
	}
	derivs, _ := listDerivations(ctx.AppDB(), testProj, "1")
	if len(derivs) != 0 {
		t.Errorf("derivations should be gone: %v", derivs)
	}
	if _, err := getTranscript(ctx.AppDB(), testProj, "1"); !notFound(err) {
		t.Errorf("transcript should be gone")
	}
}

func TestToolDelete_MissingMediaIsNoOp(t *testing.T) {
	ctx := newTestCtx(t)
	out, err := (&App{}).toolDelete(ctx, map[string]any{
		"_project_id": testProj,
		"file_id":     "999",
	})
	if err != nil {
		t.Fatal(err)
	}
	resp := out.(map[string]any)
	if resp["found"] != false || resp["deleted"] != false {
		t.Fatalf("unexpected response: %#v", resp)
	}
}
