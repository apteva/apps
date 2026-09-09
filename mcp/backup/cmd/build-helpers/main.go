// Build immutable helper release assets and their embedded checksum manifest.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

func main() {
	out := flag.String("out", "/tmp/backup-helper-assets", "asset output directory")
	version := flag.String("version", "0.3.6", "Backup release version")
	flag.Parse()
	if e := build(*out, *version); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
func build(out, version string) error {
	if e := os.MkdirAll(out, 0755); e != nil {
		return e
	}
	manifest := map[string]map[string]string{}
	for _, platform := range [][2]string{{"darwin", "amd64"}, {"darwin", "arm64"}, {"linux", "amd64"}, {"linux", "arm64"}} {
		key := platform[0] + "-" + platform[1]
		name := "backup-helper-" + key
		file := filepath.Join(out, name)
		cmd := exec.Command("go", "build", "-trimpath", "-ldflags=-s -w -buildid=", "-o", file, "./cmd/backup-helper")
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS="+platform[0], "GOARCH="+platform[1])
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if e := cmd.Run(); e != nil {
			return e
		}
		body, e := os.ReadFile(file)
		if e != nil {
			return e
		}
		hash := sha256.Sum256(body)
		manifest[key] = map[string]string{"sha256": hex.EncodeToString(hash[:]), "url": "https://github.com/apteva/apps/releases/download/backup%2Fv" + version + "/" + name}
		fmt.Printf("%s: %d bytes\n", name, len(body))
	}
	raw, e := json.MarshalIndent(manifest, "", "  ")
	if e != nil {
		return e
	}
	return os.WriteFile("helper-assets.json", append(raw, '\n'), 0644)
}
