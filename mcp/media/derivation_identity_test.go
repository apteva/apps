package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestValidateDerivationStorageFile(t *testing.T) {
	d := DerivationRow{FileID: "9215", Kind: "keyframe", StorageFileID: "100", PositionMs: 46_000}
	valid := &StorageFile{
		ID: 100, Name: "9215-46000.jpg", Folder: "/.media/keyframe/",
		ContentType: "image/jpeg", Source: "media-derivation",
	}
	if err := validateDerivationStorageFile(d, valid); err != nil {
		t.Fatalf("valid derivation rejected: %v", err)
	}

	for name, file := range map[string]*StorageFile{
		"missing":               nil,
		"user output folder":    {ID: 100, Name: "portrait.png", Folder: "/hgv/monika/", ContentType: "image/png", Source: "media-render"},
		"wrong source filename": {ID: 100, Name: "9999-46000.jpg", Folder: "/.media/keyframe/", ContentType: "image/jpeg", Source: "media-derivation"},
		"wrong content type":    {ID: 100, Name: "9215-46000.mp3", Folder: "/.media/keyframe/", ContentType: "audio/mpeg", Source: "media-derivation"},
		"render source":         {ID: 100, Name: "9215-46000.png", Folder: "/.media/keyframe/", ContentType: "image/png", Source: "media-render"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateDerivationStorageFile(d, file); err == nil {
				t.Fatal("invalid storage occupant was accepted")
			}
		})
	}
}

func TestResolveValidDerivationsRejectsReusedIDsInOneBatch(t *testing.T) {
	derivs := []DerivationRow{
		{FileID: "9215", Kind: "keyframe", StorageFileID: "100", PositionMs: 46_000},
		{FileID: "9215", Kind: "keyframe", StorageFileID: "101", PositionMs: 51_000},
		{FileID: "9215", Kind: "keyframe", StorageFileID: "102", PositionMs: 56_000},
	}
	resolveCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/files" {
			http.Error(w, "unexpected request", http.StatusNotFound)
			return
		}
		resolveCalls++
		_ = json.NewEncoder(w).Encode(map[string]any{"files": []StorageFile{
			{ID: 100, Name: "9215-46000.jpg", Folder: "/.media/keyframe/", ContentType: "image/jpeg", Source: "media-derivation"},
			{ID: 101, Name: "new-portrait.png", Folder: "/hgv/monika/", ContentType: "image/png", Source: "media-render"},
		}})
	}))
	t.Cleanup(srv.Close)
	sc := &storageClient{base: srv.URL, token: "test", httpClient: srv.Client()}

	got, err := resolveValidDerivations(context.Background(), sc, testProj, derivs)
	if err != nil {
		t.Fatal(err)
	}
	if resolveCalls != 1 {
		t.Fatalf("resolve calls=%d want 1 batch", resolveCalls)
	}
	if len(got) != 1 || got[0].StorageFileID != "100" {
		t.Fatalf("valid derivations=%+v want only storage id 100", got)
	}
}

func TestDeleteOwnedDerivationsNeverDeletesReusedUserFile(t *testing.T) {
	derivs := []DerivationRow{
		{FileID: "9215", Kind: "keyframe", StorageFileID: "100", PositionMs: 46_000},
		{FileID: "9215", Kind: "keyframe", StorageFileID: "101", PositionMs: 51_000},
	}
	var deleted []int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/files":
			_ = json.NewEncoder(w).Encode(map[string]any{"files": []StorageFile{
				{ID: 100, Name: "9215-46000.jpg", Folder: "/.media/keyframe/", ContentType: "image/jpeg", Source: "media-derivation"},
				{ID: 101, Name: "redo.png", Folder: "/hgv/monika/", ContentType: "image/png", Source: "media-render"},
			}})
		case r.Method == http.MethodPost && r.URL.Path == "/mcp":
			var rpc struct {
				Params struct {
					Arguments map[string]any `json:"arguments"`
				} `json:"params"`
			}
			if err := json.NewDecoder(r.Body).Decode(&rpc); err != nil {
				t.Errorf("decode delete request: %v", err)
				return
			}
			deleted = append(deleted, int64(rpc.Params.Arguments["id"].(float64)))
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "unexpected request", http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	sc := &storageClient{base: srv.URL, token: "test", httpClient: srv.Client()}

	deleteOwnedDerivationFiles(context.Background(), nil, sc, testProj, derivs)
	if !reflect.DeepEqual(deleted, []int64{100}) {
		t.Fatalf("deleted storage ids=%v want only owned derivative 100", deleted)
	}
}

func TestDeletedDerivativeEventInvalidatesReferencesByProject(t *testing.T) {
	ctx := newTestCtx(t)
	for _, sourceID := range []string{"9215", "other"} {
		if err := upsertMedia(ctx.AppDB(), testProj, sourceID, sampleVideoProbe(), "sha", "/", sourceID+".mp4"); err != nil {
			t.Fatal(err)
		}
		if err := upsertDerivation(ctx.AppDB(), testProj, sourceID, "thumbnail", 100, 320, 180, 0); err != nil {
			t.Fatal(err)
		}
	}
	if err := upsertMedia(ctx.AppDB(), "other-project", "x", sampleVideoProbe(), "sha", "/", "x.mp4"); err != nil {
		t.Fatal(err)
	}
	if err := upsertDerivation(ctx.AppDB(), "other-project", "x", "thumbnail", 100, 320, 180, 0); err != nil {
		t.Fatal(err)
	}

	removed, err := deleteDerivationReferencesByStorageID(ctx.AppDB(), testProj, "100")
	if err != nil {
		t.Fatal(err)
	}
	if removed != 2 {
		t.Fatalf("removed=%d want 2", removed)
	}
	for _, sourceID := range []string{"9215", "other"} {
		rows, err := listDerivations(ctx.AppDB(), testProj, sourceID)
		if err != nil || len(rows) != 0 {
			t.Fatalf("source %s still has stale refs: %v err=%v", sourceID, rows, err)
		}
	}
	rows, err := listDerivations(ctx.AppDB(), "other-project", "x")
	if err != nil || len(rows) != 1 {
		t.Fatalf("other project was modified: rows=%v err=%v", rows, err)
	}
}
