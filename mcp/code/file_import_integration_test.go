//go:build integration

package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

func TestSidecar_FileImport_ExactBytesAndSafeOverwrite(t *testing.T) {
	root := t.TempDir()
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("file-import"), tk.WithEnv("CODE_REPOS_DIR", root), tk.WithEnv("CODE_MAX_FILE_BYTES", "1024"))
	created := sc.MCP("repos_create", map[string]any{"name": "File Demo", "framework": "blank"})
	repo := created["repository"].(map[string]any)
	slug := repo["slug"].(string)
	id := strconv.FormatInt(int64(repo["id"].(float64)), 10)
	assertBytes := func(path string, want []byte) {
		t.Helper()
		got, err := os.ReadFile(filepath.Join(root, "by-id", id, "files", filepath.FromSlash(path)))
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("%s: got %x, err %v; want %x", path, got, err, want)
		}
	}
	callError := func(args map[string]any) {
		t.Helper()
		if _, err := sc.MCPRaw("tools/call", map[string]any{"name": "code_import_file", "arguments": args}); err == nil {
			t.Fatalf("invalid import succeeded: %v", args)
		}
	}
	// Core sends the rehydrated blob as a JSON envelope string, not UTF-8 content.
	want := []byte{0, 255, 254, 128, 13, 10}
	input := fileImportEnvelope(want)
	input["filename"] = "../../do-not-use-this-name.bin"
	envelope, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	result := sc.MCP("code_import_file", map[string]any{"slug": slug, "path": "assets/data.bin", "file": string(envelope)})
	meta := result["file"].(map[string]any)
	if meta["size"] != float64(len(want)) || meta["sha256"] == "" {
		t.Fatalf("metadata=%v", meta)
	}
	assertBytes("assets/data.bin", want)
	sc.MCP("code_import_file", map[string]any{"slug": slug, "path": "index.html", "file": base64.StdEncoding.EncodeToString([]byte("<html>Olá</html>\r\n")), "create_only": true})
	assertBytes("index.html", []byte("<html>Olá</html>\r\n"))
	sc.MCP("code_import_file", map[string]any{"slug": slug, "path": "empty.txt", "file": ""})
	assertBytes("empty.txt", []byte{})
	sc.MCP("code_import_file", map[string]any{"slug": slug, "path": "object.bin", "file": fileImportEnvelope(want)})
	assertBytes("object.bin", want)
	for _, extra := range []map[string]any{
		{}, // No hash must not overwrite an existing file.
		{"expected_sha256": "stale"},
		{"expected_sha256": meta["sha256"], "create_only": true},
		{"expected_sha256": meta["sha256"], "file": "blobref://missing"},
		{"expected_sha256": meta["sha256"], "file": "!!!"},
		{"expected_sha256": meta["sha256"], "file": base64.StdEncoding.EncodeToString(bytes.Repeat([]byte("x"), 1025))},
		{"path": "../../escaped"},
		{"path": ".git/config"},
	} {
		args := map[string]any{"slug": slug, "path": "assets/data.bin", "file": "YQ=="}
		for key, value := range extra {
			args[key] = value
		}
		callError(args)
		assertBytes("assets/data.bin", want)
	}
	sc.MCP("code_import_file", map[string]any{"slug": slug, "path": "assets/data.bin", "file": "YQ==", "expected_sha256": meta["sha256"]})
	assertBytes("assets/data.bin", []byte("a"))
	if status := sc.MCP("repos_version_status", map[string]any{"slug": slug}); status["dirty"] != true {
		t.Fatalf("import unexpectedly checkpointed source: %v", status)
	}
	if status := sc.MCP("repos_git_status", map[string]any{"slug": slug}); status["git_backed"] != false {
		t.Fatalf("import unexpectedly initialized Git: %v", status)
	}
	if tree := sc.MCP("code_list_files", map[string]any{"slug": slug}); tree["count"] != float64(4) {
		t.Fatalf("failed imports added files: %v", tree)
	}
}
