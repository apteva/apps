//go:build integration

package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	tk "github.com/apteva/app-sdk/testkit"
	"os"
	"path/filepath"
	"testing"
)

type realCodeSnapshotPlatform struct {
	tk.BasePlatformClient
	code        *tk.Sidecar
	reads       int
	interrupted bool
}

func (p *realCodeSnapshotPlatform) CallAppResult(app, tool string, args map[string]any, out any) error {
	if tool == "repos_snapshot_read" {
		p.reads++
		if p.reads == 2 && !p.interrupted {
			p.interrupted = true
			return errors.New("simulated interrupted transfer")
		}
	}
	result := p.code.MCP(tool, args)
	raw, e := json.Marshal(result)
	if e != nil {
		return e
	}
	return json.Unmarshal(raw, out)
}
func TestRealCodeSnapshotIntoDeploy(t *testing.T) {
	code := tk.SpawnSidecar(t, "../code", tk.WithProjectID("p1"), tk.WithEnv("CODE_REPOS_DIR", t.TempDir()), tk.WithEnv("CODE_EXPORT_INLINE_BYTES", "64"))
	repo := code.MCP("repos_create", map[string]any{"name": "Snapshot fixture", "framework": "blank"})["repository"].(map[string]any)
	slug := repo["slug"].(string)
	data := make([]byte, 3<<20)
	rand.Read(data)
	asset := base64.StdEncoding.EncodeToString(data)
	code.MCP("code_write_file", map[string]any{"slug": slug, "path": "client/asset.txt", "content": asset})
	code.MCP("code_write_file", map[string]any{"slug": slug, "path": "server/private.txt", "content": "sibling"})
	p := &realCodeSnapshotPlatform{code: code}
	f := &codeFetcher{platform: p, config: sourceConfig{CacheDir: t.TempDir()}}
	d := &Deployment{ProjectID: "p1", SourceRef: slug, SourceExtraJSON: `{"subdir":"client"}`}
	dest := t.TempDir()
	if e := f.Fetch(d, dest); e == nil {
		t.Fatal("expected interruption")
	}
	code.MCP("code_write_file", map[string]any{"slug": slug, "path": "client/asset.txt", "content": "changed"})
	if e := f.Fetch(d, dest); e != nil {
		t.Fatal(e)
	}
	got, e := os.ReadFile(filepath.Join(dest, "asset.txt"))
	if e != nil || string(got) != asset {
		t.Fatal("source changed during transfer")
	}
	if _, e = os.Stat(filepath.Join(dest, "server")); !os.IsNotExist(e) {
		t.Fatal("sibling exported")
	}
}
