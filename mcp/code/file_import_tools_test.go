package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func fileImportEnvelope(body []byte) map[string]any {
	return map[string]any{"_binary": true, "base64": base64.StdEncoding.EncodeToString(body), "size": len(body), "mimeType": "application/octet-stream"}
}

func TestDecodeImportFile_ExactBytes(t *testing.T) {
	for _, body := range [][]byte{[]byte("<html>Olá</html>\r\n"), {0, 255, 254, 128, 10, 13}, {}} {
		envelope := fileImportEnvelope(body)
		raw, err := json.Marshal(envelope)
		if err != nil {
			t.Fatal(err)
		}
		for _, arg := range []any{base64.StdEncoding.EncodeToString(body), string(raw), envelope} {
			got, err := decodeImportFile(arg)
			if err != nil || !bytes.Equal(got, body) {
				t.Fatalf("%T: got %x, err %v; want %x", arg, got, err, body)
			}
		}
	}
}

func TestDecodeImportFile_Invalid(t *testing.T) {
	for _, tc := range []struct {
		name string
		arg  any
		want string
	}{
		{"missing", nil, "file must be"},
		{"number", 42, "file must be"},
		{"handle", "blobref://missing", "through Core"},
		{"reference object", map[string]any{"_file_ref": "blobref://missing"}, "through Core"},
		{"handle object", map[string]any{"_file": true, "ref": "blobref://missing"}, "through Core"},
		{"bad json", "{", "invalid file envelope"},
		{"not binary", `{"_binary":false,"base64":"YQ=="}`, "_binary=true"},
		{"missing data", `{"_binary":true}`, "base64"},
		{"invalid data", "!!!", "invalid file base64"},
		{"envelope invalid data", `{"_binary":true,"base64":"!!!"}`, "invalid file base64"},
		{"size mismatch", `{"_binary":true,"base64":"YQ==","size":2}`, "size mismatch"},
		{"negative size", `{"_binary":true,"base64":"","size":-1}`, "size must be"},
		{"size type", `{"_binary":true,"base64":"YQ==","size":"1"}`, "invalid file envelope"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := decodeImportFile(tc.arg); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error=%v, want %q", err, tc.want)
			}
		})
	}
}

func TestDecodeImportFile_Bounded(t *testing.T) {
	for _, env := range []string{"CODE_MAX_FILE_BYTES", "CODE_IMPORT_MAX_FILE_BYTES"} {
		t.Run(env, func(t *testing.T) {
			t.Setenv(env, "4")
			if got, err := decodeImportFile(base64.StdEncoding.EncodeToString([]byte("1234"))); err != nil || string(got) != "1234" {
				t.Fatalf("at limit: %q %v", got, err)
			}
			for _, arg := range []any{base64.StdEncoding.EncodeToString([]byte("12345")), fileImportEnvelope([]byte("12345")), strings.Repeat("A", 100)} {
				if _, err := decodeImportFile(arg); err == nil {
					t.Fatalf("accepted oversized input %T", arg)
				}
			}
		})
	}
}

func TestImportFile_CreateAndConditionalOverwrite(t *testing.T) {
	a, ctx, repo := reliabilityApp(t)
	want := []byte{0, 255, 128, 13, 10}
	args := map[string]any{"_project_id": "p", "slug": repo.Slug, "path": "assets/data.bin", "file": fileImportEnvelope(want)}
	result, err := a.toolImportFile(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	meta := result.(map[string]any)["file"].(FileMeta)
	sum := sha256.Sum256(want)
	if meta.Size != int64(len(want)) || meta.SHA256 != hex.EncodeToString(sum[:]) || meta.Path != "assets/data.bin" {
		t.Fatalf("metadata=%+v", meta)
	}
	args["file"] = base64.StdEncoding.EncodeToString([]byte("replacement"))
	if _, err := a.toolImportFile(ctx, args); !errors.Is(err, errRevisionConflict) {
		t.Fatalf("unconditional overwrite accepted: %v", err)
	}
	args["expected_sha256"] = strings.Repeat("0", 64)
	if _, err := a.toolImportFile(ctx, args); !errors.Is(err, errRevisionConflict) {
		t.Fatalf("stale overwrite accepted: %v", err)
	}
	args["expected_sha256"] = meta.SHA256
	args["create_only"] = true
	if _, err := a.toolImportFile(ctx, args); !errors.Is(err, errRevisionConflict) {
		t.Fatalf("create-only overwrite accepted: %v", err)
	}
	got, err := a.storeFor(repo).Read(repo.Slug, meta.Path)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("failed calls changed destination: %x %v", got, err)
	}
	args["create_only"] = false
	if _, err := a.toolImportFile(ctx, args); err != nil {
		t.Fatal(err)
	}
	got, err = a.storeFor(repo).Read(repo.Slug, meta.Path)
	if err != nil || string(got) != "replacement" {
		t.Fatalf("overwrite=%q %v", got, err)
	}
	args["path"] = "missing.bin"
	if _, err := a.toolImportFile(ctx, args); !errors.Is(err, errRevisionConflict) {
		t.Fatalf("expected hash on missing file accepted: %v", err)
	}
}

func TestImportFile_FailedInputsDoNotModifySource(t *testing.T) {
	for _, tc := range []struct {
		name  string
		extra map[string]any
		want  string
	}{
		{"missing file", map[string]any{"file": nil}, "file must be"},
		{"blob not rehydrated", map[string]any{"file": "blobref://missing"}, "through Core"},
		{"bad base64", map[string]any{"file": "!!!"}, "invalid file base64"},
		{"wrong size", map[string]any{"file": `{"_binary":true,"base64":"YQ==","size":3}`}, "size mismatch"},
		{"traversal", map[string]any{"path": "../../escape"}, "path escapes"},
		{"reserved", map[string]any{"path": ".git/config"}, ".git is reserved"},
		{"wrong project", map[string]any{"_project_id": "other"}, "not found in this project"},
		{"missing repository", map[string]any{"slug": "missing"}, "not found in this project"},
		{"unsafe overwrite", map[string]any{"create_only": false}, "requires expected_sha256"},
		{"invalid flag", map[string]any{"create_only": "false"}, "create_only must be a boolean"},
		{"invalid hash", map[string]any{"expected_sha256": 123}, "expected_sha256 must be a string"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, ctx, repo := reliabilityApp(t)
			store := a.storeFor(repo)
			meta, err := store.Write(repo.Slug, "keep.bin", []byte("keep"))
			if err != nil {
				t.Fatal(err)
			}
			args := map[string]any{"_project_id": "p", "slug": repo.Slug, "path": "keep.bin", "file": "YQ==", "expected_sha256": meta.SHA256}
			for key, value := range tc.extra {
				args[key] = value
			}
			if tc.name == "unsafe overwrite" {
				delete(args, "expected_sha256")
			}
			if _, err := a.toolImportFile(ctx, args); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error=%v, want %q", err, tc.want)
			}
			got, err := store.Read(repo.Slug, "keep.bin")
			if err != nil || string(got) != "keep" {
				t.Fatalf("failed call changed source: %q %v", got, err)
			}
			files, err := store.List(repo.Slug, "", true)
			if err != nil || len(files) != 1 {
				t.Fatalf("failed call added files: %+v %v", files, err)
			}
		})
	}
}

func TestImportFile_SizeAndSymlinkFailuresAreNonDestructive(t *testing.T) {
	a, ctx, repo := reliabilityApp(t)
	store := a.storeFor(repo)
	meta, err := store.Write(repo.Slug, "keep", []byte("old"))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODE_MAX_FILE_BYTES", "4")
	if _, err := a.toolImportFile(ctx, map[string]any{"_project_id": "p", "slug": repo.Slug, "path": "keep", "file": "MTIzNDU=", "expected_sha256": meta.SHA256}); err == nil {
		t.Fatal("oversized overwrite accepted")
	}
	t.Setenv("CODE_MAX_REPO_BYTES", "4")
	if _, err := a.toolImportFile(ctx, map[string]any{"_project_id": "p", "slug": repo.Slug, "path": "new", "file": "MTI="}); err == nil {
		t.Fatal("repository size limit bypassed")
	}
	got, err := store.Read(repo.Slug, "keep")
	if err != nil || string(got) != "old" {
		t.Fatalf("limit failure changed source: %q %v", got, err)
	}
	if _, err := store.Stat(repo.Slug, "new"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("limit failure created file: %v", err)
	}
	outside := t.TempDir()
	root := store.(FileStoreLocalPath).RepoPath(repo.Slug)
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := a.toolImportFile(ctx, map[string]any{"_project_id": "p", "slug": repo.Slug, "path": "link/escaped", "file": "YQ=="}); err == nil {
		t.Fatal("symlink parent accepted")
	}
	if _, err := os.Stat(filepath.Join(outside, "escaped")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("import escaped root: %v", err)
	}
}

func TestImportFile_ConcurrentCreateOnly(t *testing.T) {
	a, ctx, repo := reliabilityApp(t)
	args := map[string]any{"_project_id": "p", "slug": repo.Slug, "path": "race.bin", "file": "YQ=="}
	results := make(chan error, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	start := make(chan struct{})
	for i := 0; i < 2; i++ {
		go func() {
			ready.Done()
			<-start
			_, err := a.toolImportFile(ctx, args)
			results <- err
		}()
	}
	ready.Wait()
	close(start)
	var successes, conflicts int
	for i := 0; i < 2; i++ {
		err := <-results
		if err == nil {
			successes++
		} else if errors.Is(err, errRevisionConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes=%d conflicts=%d", successes, conflicts)
	}
}
