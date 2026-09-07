//go:build integration

package main

import (
	"archive/zip"
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	tk "github.com/apteva/app-sdk/testkit"
	"io"
	"net/http"
	"testing"
)

func TestSidecarSourceSnapshotTransport(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"), tk.WithEnv("CODE_REPOS_DIR", t.TempDir()), tk.WithEnv("CODE_EXPORT_INLINE_BYTES", "64"))
	repo := sc.MCP("repos_create", map[string]any{"name": "Games", "framework": "blank"})["repository"].(map[string]any)
	slug := repo["slug"].(string)
	binary := make([]byte, 3<<20)
	rand.Read(binary)
	asset := base64.StdEncoding.EncodeToString(binary)
	sc.MCP("code_write_file", map[string]any{"slug": slug, "path": "games/client/asset.txt", "content": asset})
	sc.MCP("code_write_file", map[string]any{"slug": slug, "path": "games/server/private.txt", "content": "other project"})
	desc := sc.MCP("repos_export", map[string]any{"slug": slug, "subdir": "games/client", "metadata_only": true})
	id := desc["snapshot_id"].(string)
	size := int64(desc["size"].(float64))
	if size < 2<<20 || desc["inline"] != false || desc["source_revision"] != "sha256:"+id {
		t.Fatalf("invalid receipt: %+v", desc)
	}
	sc.MCP("code_write_file", map[string]any{"slug": slug, "path": "games/client/asset.txt", "content": "later edit"})
	var downloaded bytes.Buffer
	chunks := 0
	for offset := int64(0); offset < size; {
		chunk := sc.MCP("repos_snapshot_read", map[string]any{"slug": slug, "snapshot_id": id, "offset": offset, "limit": snapshotChunkBytes})
		if chunk["snapshot_id"] != id || chunk["sha256"] != id || int64(chunk["offset"].(float64)) != offset {
			t.Fatal("chunk identity mismatch")
		}
		data, err := base64.StdEncoding.DecodeString(chunk["data_b64"].(string))
		if err != nil {
			t.Fatal(err)
		}
		downloaded.Write(data)
		next := int64(chunk["next_offset"].(float64))
		if next != offset+int64(len(data)) || next <= offset || chunk["eof"] != (next == size) {
			t.Fatal("invalid chunk offsets")
		}
		offset = next
		chunks++
	}
	digest := sha256.Sum256(downloaded.Bytes())
	if hex.EncodeToString(digest[:]) != id || downloaded.Len() != int(size) || chunks < 3 {
		t.Fatal("corrupt or incomplete snapshot")
	}
	z, err := zip.NewReader(bytes.NewReader(downloaded.Bytes()), size)
	if err != nil {
		t.Fatal(err)
	}
	if len(z.File) != 1 || z.File[0].Name != "asset.txt" {
		t.Fatal("wrong selected directory")
	}
	r, err := z.File[0].Open()
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(r)
	r.Close()
	if string(body) != asset {
		t.Fatal("snapshot followed later edits")
	}
	url := sc.URL() + "/api/repos/" + slug + "/export?snapshot_id=" + id
	req, _ := http.NewRequest("GET", url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("snapshot not auth protected: %d", resp.StatusCode)
	}
	req, _ = http.NewRequest("GET", url, nil)
	req.Header.Set("Authorization", "Bearer "+sc.Token())
	req.Header.Set("Range", "bytes=5-24")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	rangeBytes, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 206 || !bytes.Equal(rangeBytes, downloaded.Bytes()[5:25]) {
		t.Fatalf("immutable range failed: %d", resp.StatusCode)
	}
	reopened := sc.MCP("repos_export", map[string]any{"slug": slug, "snapshot_id": id, "metadata_only": true})
	if reopened["size"] != desc["size"] || reopened["sha256"] != id {
		t.Fatal("reopened snapshot changed")
	}
}
