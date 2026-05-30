package main

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestExtractSourceUploadZipUsesSingleTopLevelDir(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("demo/settings.gradle")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("pluginManagement {}\n")); err != nil {
		t.Fatal(err)
	}
	w, err = zw.Create("demo/app/build.gradle")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("plugins { id 'com.android.application' }\n")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	dest := t.TempDir()
	root, count, err := extractSourceUpload(buf.Bytes(), "demo.zip", dest)
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("count=%d, want 2", count)
	}
	if root != filepath.Join(dest, "demo") {
		t.Fatalf("root=%q, want single top-level dir", root)
	}
	if _, err := os.Stat(filepath.Join(root, "settings.gradle")); err != nil {
		t.Fatal(err)
	}
}

func TestExtractSourceUploadRejectsZipTraversal(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	if _, err := zw.Create("../escape.txt"); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := extractSourceUpload(buf.Bytes(), "bad.zip", t.TempDir()); err == nil {
		t.Fatal("expected traversal error")
	}
}
