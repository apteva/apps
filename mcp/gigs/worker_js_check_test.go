package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkerPageJavaScriptParses(t *testing.T) {
	if _, err := exec.LookPath("bun"); err != nil {
		t.Skip("bun is not installed")
	}
	html := workerPageHTML("test-token")
	start := strings.LastIndex(html, "<script>")
	end := strings.LastIndex(html, "</script>")
	if start < 0 || end <= start {
		t.Fatal("worker page script not found")
	}
	dir := t.TempDir()
	source := filepath.Join(dir, "worker.js")
	if err := os.WriteFile(source, []byte(html[start+len("<script>"):end]), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bun", "build", source, "--target", "browser", "--outfile", filepath.Join(dir, "worker.out.js"))
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("worker page JavaScript does not parse: %v\n%s", err, output)
	}
}
