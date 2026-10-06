package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestChecksumRepairPreservesLegacyObjectKeyAndID(t *testing.T) {
	ctx := newTestCtx(t)
	body := []byte("legacy direct multipart bytes")
	storageKey := "legacy-video.mp4"
	legacyKey := objectKey("", storageKey)
	path := filepath.Join(os.Getenv("STORAGE_BLOBS_DIR"), legacyKey)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := ctx.AppDB().Exec(`INSERT INTO files(project_id,name,folder,storage_key,object_key,size_bytes,sha256,checksum_status,visibility) VALUES(?,?,?,?,?,?,?,'pending','private')`, "test-proj", "video.mp4", "/", storageKey, legacyKey, len(body), "")
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	if err := queueChecksumJob(ctx, &File{ID: id, ProjectID: "test-proj"}); err != nil {
		t.Fatal(err)
	}
	processChecksumJobs(context.Background(), ctx, 1)
	got, err := dbGetByID(ctx.AppDB(), "test-proj", id)
	if err != nil || got == nil {
		t.Fatalf("load repaired file: %v %+v", err, got)
	}
	want := sha256.Sum256(body)
	if got.SHA256 != hex.EncodeToString(want[:]) || got.ChecksumStatus != "verified" {
		t.Fatalf("checksum=%q status=%q", got.SHA256, got.ChecksumStatus)
	}
	if got.ID != id || got.BackendKey != legacyKey {
		t.Fatalf("identity/key changed: id=%d key=%q", got.ID, got.BackendKey)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("legacy object moved or deleted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("STORAGE_BLOBS_DIR"), got.SHA256[:2], storageKey)); !os.IsNotExist(err) {
		t.Fatalf("repair created checksum-derived replacement object")
	}
}

func TestEnsureChecksumQueuesConcurrentRepair(t *testing.T) {
	ctx := newTestCtx(t)
	f := mustUpload(t, ctx, "already.txt", "/", "verified")
	if _, err := ctx.AppDB().Exec(`UPDATE files SET sha256='',checksum_status='pending' WHERE id=?`, f.ID); err != nil {
		t.Fatal(err)
	}
	app := &App{}
	for i := 0; i < 3; i++ {
		out, err := app.toolEnsureChecksumCtx(context.Background(), ctx, map[string]any{"id": f.ID})
		if err != nil {
			t.Fatal(err)
		}
		if out.(map[string]any)["found"] != true {
			t.Fatal("repair request did not find file")
		}
	}
	var jobs int
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM checksum_jobs WHERE file_id=?`, f.ID).Scan(&jobs); err != nil || jobs != 1 {
		t.Fatalf("jobs=%d err=%v", jobs, err)
	}
}
