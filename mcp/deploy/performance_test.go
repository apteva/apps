package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func BenchmarkTailLargeLog(b *testing.B) {
	path := filepath.Join(b.TempDir(), "runtime.log")
	file, err := os.Create(path)
	if err != nil {
		b.Fatal(err)
	}
	// Sparse allocation tests the one-GiB offset without consuming a GiB of disk.
	if err = file.Truncate(1 << 30); err != nil {
		b.Fatal(err)
	}
	if _, err = file.WriteAt([]byte("last line\n"), (1<<30)-10); err != nil {
		b.Fatal(err)
	}
	file.Close()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err = tailFile(path, 300); err != nil {
			b.Fatal(err)
		}
	}
}
func BenchmarkCopyWithExcludedDependencies(b *testing.B) {
	source, dest := b.TempDir(), b.TempDir()
	deps := filepath.Join(source, "node_modules", "large-package")
	os.MkdirAll(deps, 0755)
	for i := 0; i < 10000; i++ {
		if err := os.WriteFile(filepath.Join(deps, fmt.Sprint(i)), []byte("dependency"), 0644); err != nil {
			b.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(source, "main.txt"), []byte("source"), 0644)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := copyTree(source, dest); err != nil {
			b.Fatal(err)
		}
	}
}
