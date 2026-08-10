package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestSearchAllFiles_PaginatesPastTenThousand(t *testing.T) {
	const total = 12003
	var offsets []int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/files") {
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
			return
		}
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		offsets = append(offsets, offset)
		end := offset + limit
		if end > total {
			end = total
		}
		files := make([]StorageFile, 0, end-offset)
		for i := offset; i < end; i++ {
			files = append(files, StorageFile{
				ID:          int64(i + 1),
				Name:        "video-" + strconv.Itoa(i+1) + ".mp4",
				ContentType: "video/mp4",
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"files": files})
	}))
	defer srv.Close()
	t.Setenv("APTEVA_GATEWAY_URL", srv.URL)
	t.Setenv("APTEVA_OUTBOUND_TOKEN", "test-token")

	files, err := newStorageClient().SearchAllFiles(t.Context(), testProj, 5000)
	if err != nil {
		t.Fatalf("SearchAllFiles: %v", err)
	}
	if len(files) != total {
		t.Fatalf("got %d files, want %d", len(files), total)
	}
	wantOffsets := []int{0, 5000, 10000}
	if len(offsets) != len(wantOffsets) {
		t.Fatalf("offsets=%v, want %v", offsets, wantOffsets)
	}
	for i := range wantOffsets {
		if offsets[i] != wantOffsets[i] {
			t.Fatalf("offsets=%v, want %v", offsets, wantOffsets)
		}
	}
	if files[0].ID != 1 || files[len(files)-1].ID != total {
		t.Fatalf("inventory endpoints = (%d,%d), want (1,%d)",
			files[0].ID, files[len(files)-1].ID, total)
	}
}

func TestResolvePendingIndexerFiles_BypassesInventoryWindow(t *testing.T) {
	app := newTestCtx(t)
	// Deliberately do not seed a Media row. Explicit reindex must create
	// the durable queue entry even when an outage prevented discovery.
	queued, err := (&App{}).toolReindex(app, map[string]any{
		"file_id": "11788",
		"force":   true,
	})
	if err != nil {
		t.Fatalf("toolReindex: %v", err)
	}
	if queued.(map[string]any)["queued"] != 1 {
		t.Fatalf("unexpected queue response: %#v", queued)
	}

	var resolveQueries, inventoryQueries int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ids := r.URL.Query().Get("ids")
		if ids == "" {
			inventoryQueries++
			http.Error(w, "bulk inventory is intentionally unavailable", http.StatusInternalServerError)
			return
		}
		resolveQueries++
		if ids != "11788" {
			http.Error(w, "unexpected ids="+ids, http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"files": []StorageFile{{
			ID:          11788,
			Name:        "drunk.mp4",
			Folder:      "/monika/august_2024/",
			ContentType: "video/mp4",
			SHA256:      "new-sha",
		}}})
	}))
	defer srv.Close()
	t.Setenv("APTEVA_GATEWAY_URL", srv.URL)
	t.Setenv("APTEVA_OUTBOUND_TOKEN", "test-token")

	got, err := resolvePendingIndexerFiles(
		t.Context(), newStorageClient(), app.AppDB(), testProj, directIndexerBatchSize,
	)
	if err != nil {
		t.Fatalf("resolvePendingIndexerFiles: %v", err)
	}
	if len(got) != 1 || got[0].ID != 11788 || got[0].SHA256 != "new-sha" {
		t.Fatalf("resolved rows=%+v", got)
	}
	if resolveQueries != 1 {
		t.Fatalf("exact resolve queries=%d, want 1", resolveQueries)
	}
	if inventoryQueries != 0 {
		t.Fatalf("pending dispatch made %d bulk inventory requests", inventoryQueries)
	}
}

func TestSearchAllFiles_RejectsNonPaginatingStorage(t *testing.T) {
	firstPage := []StorageFile{
		{ID: 1, Name: "a.mp4", ContentType: "video/mp4"},
		{ID: 2, Name: "b.mp4", ContentType: "video/mp4"},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulates an older Storage version that ignores offset.
		_ = json.NewEncoder(w).Encode(map[string]any{"files": firstPage})
	}))
	defer srv.Close()
	t.Setenv("APTEVA_GATEWAY_URL", srv.URL)
	t.Setenv("APTEVA_OUTBOUND_TOKEN", "test-token")

	_, err := newStorageClient().SearchAllFiles(t.Context(), testProj, len(firstPage))
	if err == nil || !strings.Contains(err.Error(), "v0.10.23+") {
		t.Fatalf("expected explicit pagination compatibility error, got %v", err)
	}
}
