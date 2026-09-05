package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

// Only trusted standard-library compilation populates the shared seed. Each
// untrusted build receives independent files, never writable shared cache inodes.
func seedGoCache(ctx context.Context, dst string) error {
	p := poolFrom(ctx)
	if p == nil {
		return nil
	}
	p.goCacheMu.Lock()
	defer p.goCacheMu.Unlock()
	if p.goCache == "" {
		root := os.Getenv("APTEVA_FUNCTIONS_STDLIB_CACHE")
		if root == "" {
			root = filepath.Join(p.buildBase, ".trusted-stdlib")
		}
		dir := filepath.Join(root, hashSource([]byte(runtime.Version() + runtime.GOOS + runtime.GOARCH + "-trimpath"))[:16])
		release, err := lockBuild(ctx, "stdlib-"+dir)
		if err != nil {
			return err
		}
		defer release()
		cache := filepath.Join(dir, "cache")
		if err = os.MkdirAll(cache, 0700); err != nil {
			return err
		}
		if _, err = os.Stat(filepath.Join(dir, ".ready")); err != nil {
			compiler, err := exec.LookPath("go")
			if err != nil {
				return err
			}
			cmd := exec.CommandContext(ctx, compiler, "build", "-trimpath", "fmt", "encoding/json", "net", "runtime/debug")
			cmd.Dir = dir
			cmd.Env = append(buildCmdEnv(dir, filepath.Join(dir, "tmp")), "GOCACHE="+cache, "GO111MODULE=off")
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			cmd.Cancel = func() error {
				if cmd.Process != nil {
					return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
				}
				return nil
			}
			cmd.WaitDelay = 2 * time.Second
			logs := newCapBuffer(stderrCap)
			cmd.Stdout = logs
			cmd.Stderr = logs
			if err = cmd.Run(); err != nil {
				return fmt.Errorf("prepare standard library cache: %w: %s", err, logs.String())
			}
			if err = os.WriteFile(filepath.Join(dir, ".ready"), []byte("ready"), 0600); err != nil {
				return err
			}
		}
		p.goCache = cache
	}

	return filepath.WalkDir(p.goCache, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		rel, err := filepath.Rel(p.goCache, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0700)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		_, err = io.Copy(out, in)
		closeErr := out.Close()
		if err != nil {
			return err
		}
		return closeErr
	})
}

func (p *pool) retainArtifacts() {
	p.mu.Lock()
	if time.Since(p.lastArtifactRetention) < time.Hour {
		p.mu.Unlock()
		return
	}
	p.lastArtifactRetention = time.Now()
	p.mu.Unlock()
	keep := envInt("APTEVA_FUNCTIONS_KEEP_VERSIONS", 20, 1, 1000)
	rows, err := p.ctx.AppDB().Query(`SELECT `+fnVerColumns+` FROM function_versions v WHERE build_status IN ('ready','failed') AND created_at < datetime('now','-7 days') AND id NOT IN (SELECT active_version_id FROM functions WHERE active_version_id IS NOT NULL) AND (SELECT count(*) FROM function_versions newer WHERE newer.function_id=v.function_id AND newer.version>v.version)>=?`, keep)
	if err != nil {
		return
	}
	var candidates []*FunctionVersion
	for rows.Next() {
		v, err := scanVersion(rows)
		if err != nil {
			rows.Close()
			return
		}
		candidates = append(candidates, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return
	}
	for _, v := range candidates {
		p.mu.Lock()
		used := false
		for w := range p.all {
			if w.versionID == v.ID {
				used = true
				break
			}
		}
		p.mu.Unlock()
		if used {
			continue
		}
		// A concurrent rollback must not race artifact collection.
		release, err := lockBuild(p.life, fmt.Sprintf("activation-%d", v.FunctionID))
		if err != nil {
			return
		}
		var active bool
		err = p.ctx.AppDB().QueryRow(`SELECT EXISTS(SELECT 1 FROM functions WHERE active_version_id=?)`, v.ID).Scan(&active)
		if err == nil && !active {
			dir := versionDir(p.buildBase, v)
			p.mu.Lock()
			if p.versionRefs[dir] > 0 {
				p.mu.Unlock()
				release()
				continue
			}
			p.collecting[dir] = true
			p.mu.Unlock()
			if removeTree(dir) == nil {
				// Older harness generations and the pre-1.7.1 layout are no longer runnable.
				variants, _ := filepath.Glob(filepath.Join(p.buildBase, v.ArtifactKey, fmt.Sprintf("v%d-*", v.Version)))
				for _, old := range variants {
					_ = removeTree(old)
				}
				old := filepath.Clean(v.BuildDir)
				root := filepath.Clean(p.buildBase) + string(os.PathSeparator)
				if old != dir && strings.HasPrefix(old, root) {
					_ = removeTree(old)
				}
				_, _ = p.ctx.AppDB().Exec(`DELETE FROM function_versions WHERE id=?`, v.ID)
				p.versions.Delete(v.ID)
				p.artifacts.Delete(dir)
			}
		}
		release()
	}
}

func treeBytes(root string, limit int64) (int64, error) {
	var size int64
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		size += info.Size()
		if size > limit {
			return fmt.Errorf("storage exceeds %d bytes", limit)
		}
		return nil
	})
	return size, err
}

// Admission + periodic enforcement bound normal growth. A filesystem quota is
// required for a hard instantaneous limit against concurrent hostile writers.
func watchDisk(ctx context.Context, root string, limit int64, exceeded func()) func() {
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			case <-ticker.C:
				if _, err := treeBytes(root, limit); err != nil {
					exceeded()
					return
				}
			}
		}
	}()
	return func() { close(done) }
}

func (p *pool) recoverLegacySnapshots() error {
	rows, err := p.ctx.AppDB().Query(`SELECT ` + fnVerColumns + ` FROM function_versions WHERE source_kind='repo' AND COALESCE(source,'')='' AND build_status='ready'`)
	if err != nil {
		return err
	}
	var versions []*FunctionVersion
	for rows.Next() {
		v, err := scanVersion(rows)
		if err != nil {
			rows.Close()
			return err
		}
		versions = append(versions, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	root, err := filepath.EvalSymlinks(p.buildBase)
	if err != nil {
		return err
	}
	for _, v := range versions {
		dir, err := filepath.EvalSymlinks(v.BuildDir)
		if err != nil || !strings.HasPrefix(dir, root+string(os.PathSeparator)) {
			continue
		}
		for _, entry := range []string{"entry.mjs", "entry.go"} {
			file, err := filepath.EvalSymlinks(filepath.Join(dir, entry))
			if err != nil || !strings.HasPrefix(file, dir+string(os.PathSeparator)) {
				continue
			}
			src, err := os.ReadFile(file)
			if err != nil || hashSource(src) != v.SourceHash {
				continue
			}
			_, err = p.ctx.AppDB().Exec(`UPDATE function_versions SET source=? WHERE id=? AND artifact_key=?`, string(src), v.ID, v.ArtifactKey)
			if err != nil {
				return err
			}
			break
		}
	}
	return nil
}
func (a *App) handleHTTPCapabilities(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		httpErr(w, 405, "GET required")
		return
	}
	p := currentPool()
	if p == nil {
		httpErr(w, 503, "not mounted")
		return
	}
	p.mu.Lock()
	live := len(p.all)
	memory := p.liveMB
	p.mu.Unlock()
	httpJSON(w, map[string]any{"sandbox_platform_supported": platformSandboxSupported(), "sandbox_required": envBool("APTEVA_FUNCTIONS_REQUIRE_SANDBOX", sandboxRequiredByDefault()), "cgroup_required": envBool("APTEVA_FUNCTIONS_REQUIRE_CGROUP", sandboxRequiredByDefault()), "live_workers": live, "reserved_memory_mb": memory, "max_workers": cap(p.globalSem), "protocol_frame_bytes": maxFrame, "protocol_reserved_bytes": protocolBytes.Load(), "downstream_inflight": len(p.downstream), "hard_disk_quota": "configure on the artifact and temporary filesystems"})
}
