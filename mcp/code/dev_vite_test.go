package main

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestDevNodePortArgs(t *testing.T) {
	for _, tc := range []struct {
		script, pm string
		want       bool
	}{
		{"vite", "npm", true}, {"vite --port 5173", "bun", true}, {"vite dev", "pnpm", true}, {"vite serve", "yarn", true},
		{"node server.js", "npm", false}, {"vite build", "npm", false}, {"vite preview", "npm", false}, {"vite && echo done", "npm", false},
	} {
		t.Run(tc.script+tc.pm, func(t *testing.T) {
			dir := t.TempDir()
			body, _ := json.Marshal(map[string]any{"scripts": map[string]string{"dev": tc.script}})
			if err := os.WriteFile(filepath.Join(dir, "package.json"), body, 0600); err != nil {
				t.Fatal(err)
			}
			got := devNodePortArgs(dir, tc.pm, []string{"run", "dev"}, 6189)
			want := []string{"run", "dev"}
			if tc.want {
				if tc.pm == "npm" {
					want = append(want, "--")
				}
				want = append(want, "--host", "127.0.0.1", "--port", "6189", "--strictPort")
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("got %v want %v", got, want)
			}
		})
	}
}

// An installed Vite fixture exercises the real launcher, HTTP readiness and Stop
// without downloading dependencies in the standard test suite.
func TestDevViteLivePreview(t *testing.T) {
	modules := os.Getenv("CODE_TEST_VITE_NODE_MODULES")
	if modules == "" {
		t.Skip("set CODE_TEST_VITE_NODE_MODULES to run real Vite preview test")
	}
	t.Setenv("CODE_SKIP_AUTO_INSTALL", "1")
	a, ctx, repo := reliabilityApp(t)
	root := a.storeFor(repo).(FileStoreLocalPath).RepoPath(repo.Slug)
	if err := os.Symlink(modules, filepath.Join(root, "node_modules")); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"package.json": `{"scripts":{"dev":"vite --port 5173"}}`, "index.html": "<html><body>VITE_PREVIEW_OK</body></html>"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	s := newDevSupervisor(a.dataDir, a.store, a, 36501, 36900)
	a.dev = s
	t.Cleanup(s.stopAll)
	dr, err := s.startDevRun(ctx, startDevInput{ProjectID: repo.ProjectID, Repo: repo})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		dr, err = dbGetDevRun(ctx.AppDB(), repo.ProjectID, repo.ID)
		if err != nil {
			t.Fatal(err)
		}
		if dr.Status != "starting" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if dr.Status != "live" {
		logs, _ := tailFile(dr.LogPath, 40)
		t.Fatalf("status=%s error=%s logs=%s", dr.Status, dr.Error, logs)
	}
	resp, err := http.Get("http://127.0.0.1:" + itoaDev(dr.Port))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), "VITE_PREVIEW_OK") {
		t.Fatalf("HTTP %d %s", resp.StatusCode, body)
	}
	if err := s.stopDevRun(ctx, repo.ProjectID, repo.ID); err != nil {
		t.Fatal(err)
	}
	dr, _ = dbGetDevRun(ctx.AppDB(), repo.ProjectID, repo.ID)
	if dr.Status != "stopped" {
		t.Fatalf("stop status %s", dr.Status)
	}
}
