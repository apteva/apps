package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSourceSnapshotImmutableScopedAndRanged(t *testing.T) {
	a, ctx, repo := reliabilityApp(t)
	t.Setenv("CODE_EXPORT_INLINE_BYTES", "64")
	data := make([]byte, (2<<20)+31)
	rand.Read(data)
	store := a.storeFor(repo)
	if _, err := store.Write(repo.Slug, "games/client/assets.bin", data); err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"_project_id": "p", "slug": repo.Slug}
	result, err := a.toolReposExport(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	desc := result.(*repoSourceExport)
	if desc.Inline || desc.ZipB64 != "" || desc.Size < 2<<20 || !strings.Contains(desc.DownloadURL, "snapshot_id="+desc.SnapshotID) {
		t.Fatalf("bad descriptor: %+v", desc)
	}
	again, err := a.toolReposExport(ctx, args)
	if err != nil || again.(*repoSourceExport).SnapshotID != desc.SnapshotID {
		t.Fatalf("non deterministic: %v", err)
	}
	if _, err := store.Write(repo.Slug, "games/client/assets.bin", []byte("edited after capture")); err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	for offset := int64(0); offset < desc.Size; {
		value, err := a.toolSnapshotRead(context.Background(), ctx, map[string]any{"_project_id": "p", "slug": repo.Slug, "snapshot_id": desc.SnapshotID, "offset": offset, "limit": snapshotChunkBytes})
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(value)
		var chunk struct {
			Data string `json:"data_b64"`
			Next int64  `json:"next_offset"`
			EOF  bool   `json:"eof"`
		}
		json.Unmarshal(raw, &chunk)
		decoded, err := base64.StdEncoding.DecodeString(chunk.Data)
		if err != nil {
			t.Fatal(err)
		}
		archive.Write(decoded)
		if chunk.Next <= offset || chunk.EOF != (chunk.Next == desc.Size) {
			t.Fatal("invalid offsets")
		}
		offset = chunk.Next
	}
	digest := sha256.Sum256(archive.Bytes())
	if hex.EncodeToString(digest[:]) != desc.SHA256 {
		t.Fatal("checksum mismatch")
	}
	zr, err := zip.NewReader(bytes.NewReader(archive.Bytes()), int64(archive.Len()))
	if err != nil {
		t.Fatal(err)
	}
	r, err := zr.File[0].Open()
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(r)
	r.Close()
	if !bytes.Equal(got, data) {
		t.Fatal("snapshot changed after source edit")
	}
	newer, err := a.toolReposExport(ctx, args)
	if err != nil || newer.(*repoSourceExport).SnapshotID == desc.SnapshotID {
		t.Fatal("new capture did not change revision")
	}
	for _, bad := range []map[string]any{
		{"_project_id": "other", "slug": repo.Slug, "snapshot_id": desc.SnapshotID},
		{"_project_id": "p", "slug": repo.Slug, "snapshot_id": "../outside"},
		{"_project_id": "p", "slug": repo.Slug, "snapshot_id": desc.SnapshotID, "offset": -1},
		{"_project_id": "p", "slug": repo.Slug, "snapshot_id": desc.SnapshotID, "limit": snapshotChunkBytes + 1},
	} {
		if _, err := a.toolSnapshotRead(context.Background(), ctx, bad); err == nil {
			t.Fatalf("accepted invalid read: %v", bad)
		}
	}
	req := httptest.NewRequest("GET", "/?snapshot_id="+desc.SnapshotID, nil)
	req.Header.Set("Range", "bytes=7-30")
	out := httptest.NewRecorder()
	a.serveSourceSnapshot(out, req, repo)
	if out.Code != 206 || !bytes.Equal(out.Body.Bytes(), archive.Bytes()[7:31]) {
		t.Fatalf("range: %d", out.Code)
	}
	expired := time.Now().Add(-snapshotTTL - time.Minute)
	path := filepath.Join(a.snapshotDir(repo), desc.SnapshotID+".zip")
	os.Chtimes(path, expired, expired)
	args["snapshot_id"] = desc.SnapshotID
	if _, err := a.toolReposExport(ctx, args); err == nil {
		t.Fatal("expired snapshot silently recaptured")
	}
}

func TestSourceSnapshotBudgetsCancellationAndModes(t *testing.T) {
	a, _, repo := reliabilityApp(t)
	store := a.storeFor(repo)
	root := store.(FileStoreLocalPath).RepoPath(repo.Slug)
	if err := os.WriteFile(filepath.Join(root, "run.sh"), []byte("#!/bin/sh\necho test\n"), 0755); err != nil {
		t.Fatal(err)
	}
	desc, err := a.createSourceSnapshot(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.OpenReader(filepath.Join(a.snapshotDir(repo), desc.SnapshotID+".zip"))
	if err != nil {
		t.Fatal(err)
	}
	if zr.File[0].Mode().Perm() != 0755 {
		t.Fatal("lost executable mode")
	}
	zr.Close()
	t.Setenv("CODE_SNAPSHOT_CACHE_BYTES", "1")
	store.Write(repo.Slug, "other", []byte("new"))
	if _, err := a.createSourceSnapshot(context.Background(), repo); err == nil {
		t.Fatal("ignored cache budget")
	}
	f, _, err := a.openSourceSnapshot(repo, desc.SnapshotID)
	if err != nil {
		t.Fatal("evicted live snapshot")
	}
	f.Close()
	t.Setenv("CODE_SNAPSHOT_CACHE_BYTES", "100000000")
	t.Setenv("CODE_SNAPSHOT_MAX_BYTES", "4")
	if _, err := a.createSourceSnapshot(context.Background(), repo); err == nil {
		t.Fatal("ignored source budget")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := a.createSourceSnapshot(cancelled, repo); err == nil {
		t.Fatal("ignored cancellation")
	}
	leftovers, _ := filepath.Glob(filepath.Join(a.snapshotDir(repo), "*.tmp"))
	if len(leftovers) != 0 {
		t.Fatal("left partial snapshots")
	}
}

func TestSourceSnapshotLargeAssetBeyondEditorLimit(t *testing.T) {
	a, _, repo := reliabilityApp(t)
	root := a.storeFor(repo).(FileStoreLocalPath).RepoPath(repo.Slug)
	// A binary asset may be larger than the editor's individual file limit.
	f, err := os.Create(filepath.Join(root, "large.asset"))
	if err != nil {
		t.Fatal(err)
	}
	if err = f.Truncate(12 << 20); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if _, err := a.createSourceSnapshot(context.Background(), repo); err != nil {
		t.Fatal(err)
	}
}

func TestSourceSnapshotSubdirectoryAndReopen(t *testing.T) {
	a, ctx, repo := reliabilityApp(t)
	store := a.storeFor(repo)
	store.Write(repo.Slug, "games/client/index.html", []byte("client"))
	store.Write(repo.Slug, "games/server/index.html", []byte("server"))
	args := map[string]any{"_project_id": "p", "slug": repo.Slug, "subdir": "games/client"}
	result, err := a.toolReposExport(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	desc := result.(*repoSourceExport)
	body, err := base64.StdEncoding.DecodeString(desc.ZipB64)
	if err != nil {
		t.Fatal(err)
	}
	z, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	if len(z.File) != 1 || z.File[0].Name != "index.html" {
		t.Fatalf("wrong selected archive: %+v", z.File)
	}
	restarted := &App{dataDir: a.dataDir}
	f, receipt, err := restarted.openSourceSnapshot(repo, desc.SnapshotID)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(f)
	f.Close()
	if receipt.SHA256 != desc.SHA256 || !bytes.Equal(body, got) {
		t.Fatal("snapshot changed across restart")
	}
	args["snapshot_id"] = desc.SnapshotID
	if _, err := a.toolReposExport(ctx, args); err == nil {
		t.Fatal("accepted subdir while reopening")
	}
	delete(args, "snapshot_id")
	root := store.(FileStoreLocalPath).RepoPath(repo.Slug)
	os.Symlink(filepath.Join(root, "games"), filepath.Join(root, "link"))
	for _, subdir := range []string{"../x", "/tmp", "games//client", "games/./client", "games/../client", "missing", "link/client", `games\client`, ".git"} {
		args["subdir"] = subdir
		if _, err := a.toolReposExport(ctx, args); err == nil {
			t.Errorf("accepted invalid directory %q", subdir)
		}
	}
}
