package main

import (
	"archive/tar"
	"context"
	"os"
	"testing"
	"time"
)

func TestPreviewSourceImportPreservesUnrelatedDependencyLinks(t *testing.T) {
	if os.Getenv("RUN_CONTAINERS_TESTS") != "1" {
		t.Skip("Docker tests opt-in")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	vol := "containers-preview-test-" + newWorkloadID()[4:]
	if _, err := docker(ctx, "volume", "create", vol); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		docker(c, "volume", "rm", vol)
	})
	_, err := docker(ctx, "run", "--rm", "-v", vol+":/volume", "alpine:3.20", "sh", "-c", "mkdir -p /volume/node_modules/.bin /volume/safe; echo keep > /volume/safe/secret; ln -s ../vite/bin/vite.js /volume/node_modules/.bin/vite; ln -s safe /volume/redirect; ln /volume/safe/secret /volume/proof")
	if err != nil {
		t.Fatal(err)
	}
	archive := testTarGzip(t, []*tar.Header{{Name: "proof", Mode: 0644, Size: 2, Typeflag: tar.TypeReg}}, [][]byte{[]byte("ok")})
	if err = (LocalDocker{}).ImportVolumeArchive(ctx, vol, ".", archive); err != nil {
		t.Fatal("unrelated node_modules symlink blocked sync:", err)
	}
	out, err := docker(ctx, "run", "--rm", "-v", vol+":/volume:ro", "alpine:3.20", "sh", "-c", "test -L /volume/node_modules/.bin/vite && test \"$(cat /volume/safe/secret)\" = keep && cat /volume/proof")
	if err != nil || out != "ok" {
		t.Fatal("cache/link target modified:", out, err)
	}
	for _, entry := range []string{"redirect/secret", "node_modules/.bin/vite"} {
		archive = testTarGzip(t, []*tar.Header{{Name: entry, Mode: 0644, Size: 2, Typeflag: tar.TypeReg}}, [][]byte{[]byte("no")})
		if err = (LocalDocker{}).ImportVolumeArchive(ctx, vol, ".", archive); err == nil {
			t.Fatalf("write through symlink %q allowed", entry)
		}
	}
}
