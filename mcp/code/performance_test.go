package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func BenchmarkAuditPagedRead(b *testing.B) {
	s := NewLocalFileStore(b.TempDir())
	s.CreateRepo("r")
	body := bytes.Repeat([]byte("const value = 123; // a representative source line\n"), 5000)
	os.WriteFile(filepath.Join(s.RepoPath("r"), "file.ts"), body, 0644)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.ReadPage("r", "file.ts", 1+(i%20)*200, 100); err != nil {
			b.Fatal(err)
		}
	}
}
func BenchmarkAuditWriteInLargeTree(b *testing.B) {
	s := NewLocalFileStore(b.TempDir())
	s.CreateRepo("r")
	root := s.RepoPath("r")
	for i := 0; i < 3000; i++ {
		os.WriteFile(filepath.Join(root, fmt.Sprintf("f%d.txt", i)), []byte("one"), 0644)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.Write("r", "f0.txt", []byte("two")); err != nil {
			b.Fatal(err)
		}
	}
}
func BenchmarkAuditGrep(b *testing.B) {
	s := NewLocalFileStore(b.TempDir())
	s.CreateRepo("r")
	body := bytes.Repeat([]byte("long source line without matching token\n"), 10000)
	os.WriteFile(filepath.Join(s.RepoPath("r"), "file.ts"), body, 0644)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := grepRepo(s, "r", GrepOptions{Pattern: "needle", OutputMode: "count"}); err != nil {
			b.Fatal(err)
		}
	}
}
