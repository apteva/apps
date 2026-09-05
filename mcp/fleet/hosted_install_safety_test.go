package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestHostedInstallConcurrentRequestsPublishOneCompleteRuntime(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux remote installation test")
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node unavailable")
	}
	root := t.TempDir()
	fixtureRoot := filepath.Join(root, "fixture")
	writeFakeVersionedRuntime(t, fixtureRoot, "0.41.1")
	fakeBin := filepath.Join(root, "bin")
	if err := os.MkdirAll(fakeBin, 0700); err != nil {
		t.Fatal(err)
	}
	count := filepath.Join(root, "install-count")
	fake := `#!/bin/sh
set -eu
while [ "$#" -gt 0 ]; do
 case "$1" in --prefix) shift; prefix=$1;; esac
 shift
done
printf 'install\n' >> ` + sh(count) + `
cp -R ` + sh(filepath.Join(fixtureRoot, "0.41.1", "node_modules")) + ` "$prefix/"
`
	if err := os.WriteFile(filepath.Join(fakeBin, "npm"), []byte(fake), 0700); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(root, "versions", "0.41.1")
	script := hostedVersionInstallScript(destination, "0.41.1")
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			cmd := exec.Command("sh", "-c", script)
			cmd.Env = append(os.Environ(), "PATH="+fakeBin+":"+os.Getenv("PATH"))
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Logf("install output: %s", out)
			}
			results <- err
		}()
	}
	for i := 0; i < 2; i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(count)
	if err != nil || strings.Count(string(data), "install") != 1 {
		t.Fatalf("installation count %q %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(destination, ".npm-apteva-home")); !os.IsNotExist(err) {
		t.Fatal("installation scratch directory was published")
	}
}
