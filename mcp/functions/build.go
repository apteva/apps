package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
	p := currentPool()
	if p == nil {
		return "", errors.New("function worker pool not initialised")
	}
	return p.buildBase, nil
}

// versionDir is the deterministic artifact path for one version.
func versionDir(base string, v *FunctionVersion) string {
	return filepath.Join(base, v.ArtifactKey, fmt.Sprintf("v%d-%s", v.Version, artifactHash(v)[:16]))
}

var harnessFingerprint = sync.OnceValue(func() string {
	return hashSource([]byte(string(nodeHarness) + string(goHarness) + runtime.Version() + runtime.GOOS + runtime.GOARCH + toolchainFingerprint()))
})

func artifactHash(v *FunctionVersion) string {
	return hashSource([]byte(v.SourceHash + "\x00" + v.PackageJSON + "\x00" + harnessFingerprint()))
}

// ensureBuilt makes sure version v's artifact dir exists and is fully
// built: the entry file written, the runtime's support files staged
// (Stage), and the runtime's build step run (Build — `npm install`
// for node deps, `go build` for go). Idempotent — a `.ready` marker
// lets it no-op on a dir that's already built, so it's safe to call
// from both deploy and the pool's cold-start path (e.g. a rebuild
// after a restart cleared an ephemeral build base).
func ensureBuilt(base string, v *FunctionVersion, spec runtimeSpec, src []byte) (string, error) {
	return ensureBuiltContext(context.Background(), base, v, spec, src)
}
func ensureBuiltContext(ctx context.Context, base string, v *FunctionVersion, spec runtimeSpec, src []byte) (string, error) {
	p := poolFrom(ctx)
	if err := ctx.Err(); err != nil {
		return "", err
	}
	dir := versionDir(base, v)
	if p != nil {
		if _, ok := p.artifacts.Load(dir); ok {
			return dir, nil
		}
	}
	release, err := lockBuild(ctx, dir)
	if err != nil {
		return "", err
	}
	defer release()
	marker := filepath.Join(dir, ".ready")
	if contents, err := os.ReadFile(marker); err == nil && string(contents) == artifactHash(v) {
		if p != nil {
			p.artifacts.Store(dir, true)
		}
		return dir, nil
	}
	if p != nil {
		ctx, cancel := context.WithTimeout(ctx, buildTimeout)
		defer cancel()
		if err := p.acquireBuild(ctx); err != nil {
			return "", fmt.Errorf("build queue: %w", err)
		}
		defer p.releaseBuild()
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
	if v.PackageLock != "" {
		if err := os.WriteFile(filepath.Join(tmp, "package-lock.json"), []byte(v.PackageLock), 0600); err != nil {
			return "", err
		}
	}
	if err := spec.Build(ctx, tmp, v.PackageJSON); err != nil {
		return "", err
	}
	_ = removeTree(filepath.Join(tmp, ".cache"))
	_ = removeTree(filepath.Join(tmp, ".sandbox-home"))
	_ = removeTree(filepath.Join(tmp, ".sandbox-tmp"))
	if _, err := treeBytes(tmp, int64(envInt("APTEVA_FUNCTIONS_ARTIFACT_MB", 512, 16, 8192))<<20); err != nil {
		return "", err
	}
	if _, err := treeBytes(base, int64(envInt("APTEVA_FUNCTIONS_TOTAL_ARTIFACT_MB", 4096, 64, 1048576))<<20); err != nil {
		return "", err
	}
	// Marker written last — its presence means "fully built".
	if err := os.WriteFile(filepath.Join(tmp, ".ready"), []byte(artifactHash(v)), 0o400); err != nil {
		return "", err
	}
	if err := makeTreeReadOnly(tmp); err != nil {
		return "", err
	}
	_ = removeTree(dir)
	if err := os.Rename(tmp, dir); err != nil {
		return "", err
	}
	if p != nil {
		p.artifacts.Store(dir, true)
	}
	return dir, nil
}

// Reference-counted, cancellation-aware singleflight; completed versions do not leak locks.
var buildLocks = struct {
	sync.Mutex
	m map[string]*buildGate
}{m: map[string]*buildGate{}}

type buildGate struct {
	token chan struct{}
	refs  int
}

func lockBuild(ctx context.Context, key string) (func(), error) {
	buildLocks.Lock()
	g := buildLocks.m[key]
	if g == nil {
		g = &buildGate{token: make(chan struct{}, 1)}
		buildLocks.m[key] = g
	}
	g.refs++
	buildLocks.Unlock()
	drop := func() {
		buildLocks.Lock()
		g.refs--
		if g.refs == 0 {
			delete(buildLocks.m, key)
		}
		buildLocks.Unlock()
	}
	select {
	case g.token <- struct{}{}:
		return func() { <-g.token; drop() }, nil
	case <-ctx.Done():
		drop()
		return nil, ctx.Err()
	}
}

// runBuildCmd runs a runtime build step in dir, capturing combined
// output for the error message. Bounded by buildTimeout.
func runBuildCmd(ctx context.Context, dir, label, bin string, args ...string) error {
	resolved, err := exec.LookPath(bin)
	if err != nil {
		return fmt.Errorf("%s needs %q on PATH", label, bin)
	}
	ctx, cancel := context.WithTimeout(ctx, buildTimeout)
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
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		return nil
	}
	cmd.Dir = dir
	cmd.Env = buildCmdEnv(dir, tmpDir)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 2 * time.Second
	stopDisk := watchDisk(ctx, dir, int64(envInt("APTEVA_FUNCTIONS_BUILD_DISK_MB", 1024, 64, 8192))<<20, cancel)
	defer stopDisk()
	out := newCapBuffer(16 * 1024)
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Run(); err != nil {
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			cleanupSandboxProcess(cmd.Process.Pid, sandboxBuild)
		}
		return fmt.Errorf("%s failed: %v\n%s", label, err, out.String())
	}
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
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

func runNpmInstall(ctx context.Context, dir string) error {
	action := "install"
	if _, err := os.Stat(filepath.Join(dir, "package-lock.json")); err == nil {
		action = "ci"
	}
	return runBuildCmd(ctx, dir, "npm "+action, "npm", action, "--no-audit", "--no-fund", "--loglevel", "error")
}

func runGoBuild(ctx context.Context, dir string) error {
	if err := seedGoCache(ctx, filepath.Join(dir, ".cache", "go-build")); err != nil {
		return err
	}
	return runBuildCmd(ctx, dir, "go build", "go", "build", "-trimpath", "-o", "worker", ".")
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

// Binary identity changes on toolchain upgrades without spawning a process on
// every invocation. One snapshot is taken per sidecar process.
var toolchainFingerprint = sync.OnceValue(func() string {
	var parts []string
	for _, name := range []string{"node", "go", "npm"} {
		path, err := exec.LookPath(name)
		if err != nil {
			continue
		}
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			continue
		}
		info, err := os.Stat(resolved)
		if err != nil {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s:%s:%d:%d", name, resolved, info.Size(), info.ModTime().UnixNano()))
	}
	return hashSource([]byte(strings.Join(parts, "\n")))
})
