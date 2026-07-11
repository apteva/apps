package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// buildTimeout bounds a build step. Generous — a cold `npm install`
// or a `go build` with a populated module cache can take a while —
// but finite.
const buildTimeout = 2 * time.Minute

// poolBuildBase returns the root under which version artifact dirs
// live. It lives on the pool (created per sidecar boot) so artifact
// paths stay stable for the process lifetime — and so each test's
// fresh AppCtx gets its own base instead of colliding on fn-1/v1.
func poolBuildBase() (string, error) {
	if globalPool == nil {
		return "", errors.New("function worker pool not initialised")
	}
	return globalPool.buildBase, nil
}

// versionDir is the deterministic artifact path for one version.
func versionDir(base string, v *FunctionVersion) string {
	return filepath.Join(base, fmt.Sprintf("fn-%d", v.FunctionID), fmt.Sprintf("v%d", v.Version))
}

// ensureBuilt makes sure version v's artifact dir exists and is fully
// built: the entry file written, the runtime's support files staged
// (Stage), and the runtime's build step run (Build — `npm install`
// for node deps, `go build` for go). Idempotent — a `.ready` marker
// lets it no-op on a dir that's already built, so it's safe to call
// from both deploy and the pool's cold-start path (e.g. a rebuild
// after a restart cleared an ephemeral build base).
func ensureBuilt(base string, v *FunctionVersion, spec runtimeSpec, src []byte) (string, error) {
	dir := versionDir(base, v)
	lock := buildLockFor(dir)
	lock.Lock()
	defer lock.Unlock()
	marker := filepath.Join(dir, ".ready")
	if contents, err := os.ReadFile(marker); err == nil && string(contents) == v.SourceHash {
		_ = makeTreeReadOnly(dir)
		return dir, nil
	}
	if globalPool != nil {
		ctx, cancel := context.WithTimeout(context.Background(), buildTimeout)
		defer cancel()
		if err := globalPool.acquireBuild(ctx); err != nil {
			return "", fmt.Errorf("build queue: %w", err)
		}
		defer globalPool.releaseBuild()
	}
	parent := filepath.Dir(dir)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp(parent, ".build-")
	if err != nil {
		return "", err
	}
	defer removeTree(tmp)
	if err := os.WriteFile(filepath.Join(tmp, spec.EntryFile), src, 0o600); err != nil {
		return "", err
	}
	if err := spec.Stage(tmp); err != nil {
		return "", fmt.Errorf("stage: %w", err)
	}
	if err := spec.Build(tmp, v.PackageJSON); err != nil {
		return "", err
	}
	// Marker written last — its presence means "fully built".
	if err := os.WriteFile(filepath.Join(tmp, ".ready"), []byte(v.SourceHash), 0o400); err != nil {
		return "", err
	}
	if err := makeTreeReadOnly(tmp); err != nil {
		return "", err
	}
	_ = removeTree(dir)
	if err := os.Rename(tmp, dir); err != nil {
		return "", err
	}
	return dir, nil
}

var buildLocks sync.Map

func buildLockFor(key string) *sync.Mutex {
	lock, _ := buildLocks.LoadOrStore(key, &sync.Mutex{})
	return lock.(*sync.Mutex)
}

// runBuildCmd runs a runtime build step in dir, capturing combined
// output for the error message. Bounded by buildTimeout.
func runBuildCmd(dir, label, bin string, args ...string) error {
	resolved, err := exec.LookPath(bin)
	if err != nil {
		return fmt.Errorf("%s needs %q on PATH", label, bin)
	}
	ctx, cancel := context.WithTimeout(context.Background(), buildTimeout)
	defer cancel()
	tmpDir := filepath.Join(dir, ".sandbox-tmp")
	if err := os.MkdirAll(tmpDir, 0o700); err != nil {
		return err
	}
	cmd, err := sandboxCommand(resolved, args, sandboxSpec{
		Mode: sandboxBuild, Root: dir, TempDir: tmpDir,
		MemoryMB: envInt("APTEVA_FUNCTIONS_BUILD_MEMORY_MB", 1024, 128, 8192),
	})
	if err != nil {
		return err
	}
	cmd = commandWithContext(ctx, cmd)
	cmd.Dir = dir
	cmd.Env = buildCmdEnv(dir, tmpDir)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 2 * time.Second
	out := newCapBuffer(16 * 1024)
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Run(); err != nil {
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		if cmd.Process != nil {
			cleanupSandboxProcess(cmd.Process.Pid, sandboxBuild)
		}
		return fmt.Errorf("%s failed: %v\n%s", label, err, out.String())
	}
	if cmd.Process != nil {
		cleanupSandboxProcess(cmd.Process.Pid, sandboxBuild)
	}
	return nil
}

// commandWithContext preserves the sandbox helper command line while adding
// CommandContext's timeout kill behavior.
func commandWithContext(ctx context.Context, original *exec.Cmd) *exec.Cmd {
	cmd := exec.CommandContext(ctx, original.Path, original.Args[1:]...)
	return cmd
}

// buildCmdEnv is intentionally an allowlist. package.json lifecycle scripts
// are part of the supported Node behavior, but they execute without the
// sidecar token, install id, database path, or host HOME.
func buildCmdEnv(dir, tmpDir string) []string {
	keys := []string{
		"PATH", "LANG", "LC_ALL", "TZ",
		"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY",
		"SSL_CERT_FILE", "SSL_CERT_DIR", "NODE_EXTRA_CA_CERTS",
		"GOPROXY", "GONOSUMDB", "GOPRIVATE",
	}
	env := make([]string, 0, len(keys)+8)
	for _, key := range keys {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	home := filepath.Join(dir, ".sandbox-home")
	cache := filepath.Join(dir, ".cache")
	goCache := filepath.Join(cache, "go-build")
	for _, path := range []string{home, cache, goCache, tmpDir} {
		_ = os.MkdirAll(path, 0o700)
	}
	return append(env,
		"HOME="+home,
		"TMPDIR="+tmpDir,
		"TMP="+tmpDir,
		"TEMP="+tmpDir,
		"XDG_CACHE_HOME="+cache,
		"GOCACHE="+goCache,
		"npm_config_cache="+filepath.Join(cache, "npm"),
		"npm_config_update_notifier=false",
	)
}

func runNpmInstall(dir string) error {
	return runBuildCmd(dir, "npm install", "npm",
		"install", "--no-audit", "--no-fund", "--loglevel", "error", "--no-package-lock")
}

func runGoBuild(dir string) error {
	return runBuildCmd(dir, "go build", "go", "build", "-o", "worker", ".")
}

func makeTreeReadOnly(root string) error {
	dirs := []string{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		// Build scratch directories are not needed by workers and may
		// contain registry/cache metadata.
		base := filepath.Base(path)
		if info.IsDir() && (base == ".cache" || base == ".sandbox-home" || base == ".sandbox-tmp") {
			if path != root {
				if err := os.RemoveAll(path); err != nil {
					return err
				}
				return filepath.SkipDir
			}
		}
		if info.IsDir() {
			dirs = append(dirs, path)
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 {
			resolved, resolveErr := filepath.EvalSymlinks(path)
			if resolveErr != nil {
				return resolveErr
			}
			resolved, _ = filepath.Abs(resolved)
			rootAbs, _ := filepath.Abs(root)
			if resolved != rootAbs && !strings.HasPrefix(resolved, rootAbs+string(os.PathSeparator)) {
				return fmt.Errorf("artifact symlink escapes build root: %s -> %s", path, resolved)
			}
			return nil
		}
		mode := os.FileMode(0o400)
		if info.Mode()&0o111 != 0 || strings.HasSuffix(path, "/worker") {
			mode = 0o500
		}
		return os.Chmod(path, mode)
	})
	if err != nil {
		return err
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		if err := os.Chmod(dirs[i], 0o500); err != nil {
			return err
		}
	}
	return nil
}

func removeTree(root string) error {
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			_ = os.Chmod(path, 0o700)
		} else if info.Mode()&os.ModeSymlink == 0 {
			_ = os.Chmod(path, 0o600)
		}
		return nil
	})
	return os.RemoveAll(root)
}
