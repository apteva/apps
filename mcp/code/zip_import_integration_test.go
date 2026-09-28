//go:build integration

package main

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

func testZipB64(t *testing.T, files map[string][]byte) string {
	t.Helper()
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	for path, body := range files {
		w, err := zw.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(b.Bytes())
}

func zipCallError(sc *tk.Sidecar, args map[string]any) error {
	_, err := sc.MCPRaw("tools/call", map[string]any{"name": "repos_import_zip", "arguments": args})
	return err
}

func TestSidecar_ZipImport_CreatePreviewApply(t *testing.T) {
	root := t.TempDir()
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("zip-create"), tk.WithEnv("CODE_REPOS_DIR", root))
	want := []byte{0, 1, 2, 255, 10}
	archive := testZipB64(t, map[string][]byte{"src/data.bin": want, "README.md": []byte("zip source\n")})
	preview := sc.MCP("repos_import_zip", map[string]any{"archive": archive, "target_mode": "create", "name": "ZIP Demo", "framework": "go", "dry_run": true})
	if preview["slug"] != "zip-demo" || preview["added"] != float64(2) || preview["modified"] != float64(0) {
		t.Fatalf("preview=%v", preview)
	}
	list := sc.MCP("repos_list", map[string]any{})
	if list["count"] != float64(0) {
		t.Fatalf("dry run created a repository: %v", list)
	}
	result := sc.MCP("repos_import_zip", map[string]any{"import_id": preview["import_id"], "confirm": true})
	if result["archive_sha256"] != preview["archive_sha256"] || result["files_imported"] != float64(2) {
		t.Fatalf("apply=%v", result)
	}
	id := int64(result["repository"].(map[string]any)["id"].(float64))
	actual, err := os.ReadFile(filepath.Join(root, "by-id", strconv.FormatInt(id, 10), "files", "src", "data.bin"))
	if err != nil || !bytes.Equal(actual, want) {
		t.Fatalf("applied bytes=%v, err=%v; want %v", actual, err, want)
	}
	read := sc.MCP("code_read_file", map[string]any{"slug": "zip-demo", "path": "README.md"})
	if !strings.Contains(read["content"].(string), "zip source") {
		t.Fatalf("read=%v", read)
	}
	status := sc.MCP("repos_version_status", map[string]any{"slug": "zip-demo"})
	if status["dirty"] != true {
		t.Fatalf("ZIP source was checkpointed automatically: %v", status)
	}
	if err := zipCallError(sc, map[string]any{"import_id": preview["import_id"], "confirm": true}); err == nil {
		t.Fatal("reused import_id was accepted")
	}
}

func TestSidecar_ZipImport_OverlayAndConflict(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("zip-overlay"), tk.WithEnv("CODE_REPOS_DIR", t.TempDir()))
	sc.MCP("repos_create", map[string]any{"name": "Overlay", "framework": "blank"})
	sc.MCP("code_write_file", map[string]any{"slug": "overlay", "path": "keep.txt", "content": "keep"})
	sc.MCP("code_write_file", map[string]any{"slug": "overlay", "path": "a.txt", "content": "old"})
	archive := testZipB64(t, map[string][]byte{"a.txt": []byte("new"), "b.txt": []byte("added")})
	envelope, _ := json.Marshal(map[string]any{"_binary": true, "base64": archive, "mimeType": "application/zip", "size": len(archive)})
	preview := sc.MCP("repos_import_zip", map[string]any{"archive": string(envelope), "target_mode": "overlay", "slug": "overlay", "dry_run": true})
	if preview["added"] != float64(1) || preview["modified"] != float64(1) {
		t.Fatalf("preview=%v", preview)
	}
	sc.MCP("code_write_file", map[string]any{"slug": "overlay", "path": "a.txt", "content": "concurrent"})
	if err := zipCallError(sc, map[string]any{"import_id": preview["import_id"], "confirm": true}); err == nil || !strings.Contains(err.Error(), "changed since preview") {
		t.Fatalf("expected conflict, got %v", err)
	}
	read := sc.MCP("code_read_file", map[string]any{"slug": "overlay", "path": "a.txt"})
	if !strings.Contains(read["content"].(string), "concurrent") {
		t.Fatalf("conflict changed source: %v", read)
	}
	preview = sc.MCP("repos_import_zip", map[string]any{"archive": archive, "target_mode": "overlay", "slug": "overlay", "dry_run": true})
	sc.MCP("repos_import_zip", map[string]any{"import_id": preview["import_id"], "confirm": true})
	for path, want := range map[string]string{"a.txt": "new", "b.txt": "added", "keep.txt": "keep"} {
		read := sc.MCP("code_read_file", map[string]any{"slug": "overlay", "path": path})
		if !strings.Contains(read["content"].(string), want) {
			t.Fatalf("%s=%v", path, read)
		}
	}
}

func TestSidecar_ZipImport_InvalidArchiveIsNonDestructive(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("zip-invalid"), tk.WithEnv("CODE_REPOS_DIR", t.TempDir()))
	sc.MCP("repos_create", map[string]any{"name": "Safe", "framework": "blank"})
	archive := testZipB64(t, map[string][]byte{"safe.txt": []byte("data"), "../escape.txt": []byte("bad")})
	if err := zipCallError(sc, map[string]any{"archive": archive, "target_mode": "overlay", "slug": "safe", "dry_run": true}); err == nil {
		t.Fatal("traversal ZIP was accepted")
	}
	list := sc.MCP("code_list_files", map[string]any{"slug": "safe"})
	if list["count"] != float64(0) {
		t.Fatalf("failed preview modified files: %v", list)
	}
	if err := zipCallError(sc, map[string]any{"archive": "blobref://unresolved", "target_mode": "overlay", "slug": "safe", "dry_run": true}); err == nil || !strings.Contains(err.Error(), "unresolved") {
		t.Fatalf("unresolved handle: %v", err)
	}
}
