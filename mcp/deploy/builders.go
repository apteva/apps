package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Builder takes an unpacked source tree and produces an artifact
// directory. Returns the relative entrypoint within artifactDir
// that the runtime should exec (binary path or static root).
type Builder interface {
	Framework() string
	// Build runs the framework's toolchain. logW is a tee — writes go
	// to the per-build log file. Returns the entrypoint, which means:
	//   - go     → relative path to the compiled binary
	//   - static → "" (the runtime serves files directly from artifactDir)
	Build(srcDir, artifactDir string, override BuildOverrides, logW io.Writer) (entrypoint string, err error)
}

type BuildOverrides struct {
	BuildCmd string // explicit override; if non-empty, runs as `sh -c <build_cmd>` in srcDir
	StartCmd string // (used by runtime, not builder; passed through for context)
}

// detectFramework picks a builder when the deployment doesn't pin
// one. Crude but effective: presence of go.mod / package.json / etc.
func detectFramework(srcDir string) string {
	if exists(filepath.Join(srcDir, "go.mod")) {
		return "go"
	}
	if exists(filepath.Join(srcDir, "package.json")) {
		return "node"
	}
	if exists(filepath.Join(srcDir, "requirements.txt")) || exists(filepath.Join(srcDir, "pyproject.toml")) {
		return "python"
	}
	if exists(filepath.Join(srcDir, "index.html")) {
		return "static"
	}
	return ""
}

func builderFor(framework string) (Builder, error) {
	switch framework {
	case "go":
		return &goBuilder{}, nil
	case "static":
		return &staticBuilder{}, nil
	case "node":
		return &nodeBuilder{}, nil
	case "blank":
		return &blankBuilder{}, nil
	case "":
		return nil, errors.New("framework not detected; set framework explicitly on the deployment")
	default:
		return nil, fmt.Errorf("framework %q not supported (supported: go, node, static, blank)", framework)
	}
}

// ─── go ───────────────────────────────────────────────────────────

type goBuilder struct{}

func (*goBuilder) Framework() string { return "go" }

func (*goBuilder) Build(srcDir, artifactDir string, ov BuildOverrides, logW io.Writer) (string, error) {
	if err := os.MkdirAll(artifactDir, 0o755); err != nil {
		return "", err
	}
	binPath := filepath.Join(artifactDir, "app")
	args := []string{"build", "-o", binPath, "."}
	if ov.BuildCmd != "" {
		// Honour the override but still write to artifactDir/app so
		// the runtime knows where to find the binary.
		return runShellInSrc(srcDir, ov.BuildCmd, logW, binPath)
	}
	cmd := exec.Command("go", args...)
	cmd.Dir = srcDir
	cmd.Stdout = logW
	cmd.Stderr = logW
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0") // static binary by default
	fmt.Fprintf(logW, "+ go %s (cwd=%s)\n", strings.Join(args, " "), srcDir)
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("go build: %w", err)
	}
	return binPath, nil
}

// ─── static ───────────────────────────────────────────────────────

type staticBuilder struct{}

func (*staticBuilder) Framework() string { return "static" }

// Static "build" copies srcDir into artifactDir verbatim (or just
// artifactDir/dist if a build_cmd was provided). The runtime serves
// from artifactDir directly.
func (*staticBuilder) Build(srcDir, artifactDir string, ov BuildOverrides, logW io.Writer) (string, error) {
	if ov.BuildCmd != "" {
		fmt.Fprintf(logW, "+ %s (cwd=%s)\n", ov.BuildCmd, srcDir)
		c := exec.Command("sh", "-c", ov.BuildCmd)
		c.Dir = srcDir
		c.Stdout = logW
		c.Stderr = logW
		if err := c.Run(); err != nil {
			return "", fmt.Errorf("static build_cmd: %w", err)
		}
		// Convention: build emits into srcDir/dist/. If it doesn't,
		// fall back to copying srcDir.
		if dist := filepath.Join(srcDir, "dist"); exists(dist) {
			return "", copyTree(dist, artifactDir)
		}
	}
	if err := copyTree(srcDir, artifactDir); err != nil {
		return "", err
	}
	return "", nil // empty entrypoint = runtime serves files from artifactDir
}

// ─── node ─────────────────────────────────────────────────────────

type nodeBuilder struct{}

func (*nodeBuilder) Framework() string { return "node" }

// Build installs dependencies and runs an optional build script, then
// copies the full source tree (including node_modules and any build
// output like .next/) into artifactDir so the runtime can exec
// `<pm> start` against it.
//
// Package manager picked by lockfile: bun.lockb → bun, pnpm-lock.yaml
// → pnpm, yarn.lock → yarn, otherwise npm. The runtime side mirrors
// this so build and start use the same tool.
func (*nodeBuilder) Build(srcDir, artifactDir string, ov BuildOverrides, logW io.Writer) (string, error) {
	if err := os.MkdirAll(artifactDir, 0o755); err != nil {
		return "", err
	}
	pm := detectPackageManager(srcDir)
	if _, err := exec.LookPath(pm); err != nil {
		return "", fmt.Errorf("%s not found on PATH; install it or set start_cmd / build_cmd to use a different toolchain", pm)
	}
	if ov.BuildCmd != "" {
		fmt.Fprintf(logW, "+ %s (cwd=%s)\n", ov.BuildCmd, srcDir)
		c := exec.Command("sh", "-c", ov.BuildCmd)
		c.Dir = srcDir
		c.Stdout = logW
		c.Stderr = logW
		if err := c.Run(); err != nil {
			return "", fmt.Errorf("node build_cmd: %w", err)
		}
	} else {
		fmt.Fprintf(logW, "+ %s install (cwd=%s)\n", pm, srcDir)
		ic := exec.Command(pm, "install")
		ic.Dir = srcDir
		ic.Stdout = logW
		ic.Stderr = logW
		if err := ic.Run(); err != nil {
			return "", fmt.Errorf("%s install: %w", pm, err)
		}
		if hasNpmScript(srcDir, "build") {
			fmt.Fprintf(logW, "+ %s run build (cwd=%s)\n", pm, srcDir)
			bc := exec.Command(pm, "run", "build")
			bc.Dir = srcDir
			bc.Stdout = logW
			bc.Stderr = logW
			if err := bc.Run(); err != nil {
				return "", fmt.Errorf("%s run build: %w", pm, err)
			}
		} else if buildScript := findBunBuildScript(srcDir); buildScript != "" {
			// Bun-script convention: package.json has no "build" but
			// there's a root-level build.ts. Run it via `bun run` —
			// only when bun is on PATH (npm/yarn/pnpm can't execute a
			// .ts file directly without a TS runner).
			if _, err := exec.LookPath("bun"); err != nil {
				fmt.Fprintf(logW, "skipping build: no \"build\" script and bun (for build.ts) not on PATH\n")
			} else {
				fmt.Fprintf(logW, "+ bun run %s (cwd=%s) — Bun-script convention\n", buildScript, srcDir)
				bc := exec.Command("bun", "run", buildScript)
				bc.Dir = srcDir
				bc.Stdout = logW
				bc.Stderr = logW
				if err := bc.Run(); err != nil {
					return "", fmt.Errorf("bun run %s: %w", buildScript, err)
				}
			}
		}
	}
	if err := copyTreeAll(srcDir, artifactDir); err != nil {
		return "", fmt.Errorf("stage artifact: %w", err)
	}
	return "", nil
}

// findBunBuildScript / findBunRunScript look for the Bun-script
// convention's canonical entry files. build.ts is the convention for
// custom build pipelines (apteva-site, several Bun examples);
// serve.ts / server.ts is the convention for runtime entries that
// drive Bun.serve directly. Returning "" leaves the caller to fall
// back to the conventional npm-scripts path or error out.
func findBunBuildScript(dir string) string {
	if exists(filepath.Join(dir, "build.ts")) {
		return "build.ts"
	}
	return ""
}

func findBunRunScript(dir string) string {
	for _, name := range []string{"serve.ts", "server.ts"} {
		if exists(filepath.Join(dir, name)) {
			return name
		}
	}
	return ""
}

// detectPackageManager picks a Node toolchain from lockfiles in dir.
// Order matters: bun first to honour the workspace's bun-by-default
// convention, then pnpm/yarn for explicit declarations, npm last as
// the default for vanilla create-next-app.
func detectPackageManager(dir string) string {
	switch {
	case exists(filepath.Join(dir, "bun.lockb")):
		return "bun"
	case exists(filepath.Join(dir, "pnpm-lock.yaml")):
		return "pnpm"
	case exists(filepath.Join(dir, "yarn.lock")):
		return "yarn"
	default:
		return "npm"
	}
}

func hasNpmScript(dir, name string) bool {
	body, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return false
	}
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	if err := json.Unmarshal(body, &pkg); err != nil {
		return false
	}
	_, ok := pkg.Scripts[name]
	return ok
}

// copyTreeAll mirrors src into dst with no skip list — used by the
// node builder so node_modules and build output (.next, dist, build)
// land in the artifact dir alongside the source. Plain copyTree would
// drop them via shouldSkipForBuild.
func copyTreeAll(src, dst string) error {
	src = filepath.Clean(src)
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		out := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(out, info.Mode())
		}
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(target, out)
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		w, err := os.OpenFile(out, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode())
		if err != nil {
			return err
		}
		if _, err := io.Copy(w, in); err != nil {
			w.Close()
			return err
		}
		return w.Close()
	})
}

// ─── blank (BYO build_cmd / start_cmd) ────────────────────────────

type blankBuilder struct{}

func (*blankBuilder) Framework() string { return "blank" }

func (*blankBuilder) Build(srcDir, artifactDir string, ov BuildOverrides, logW io.Writer) (string, error) {
	if ov.BuildCmd == "" {
		// No build step. The artifact is the source tree itself; the
		// runtime will run start_cmd against it.
		if err := copyTree(srcDir, artifactDir); err != nil {
			return "", err
		}
		return "", nil
	}
	fmt.Fprintf(logW, "+ %s (cwd=%s)\n", ov.BuildCmd, srcDir)
	c := exec.Command("sh", "-c", ov.BuildCmd)
	c.Dir = srcDir
	c.Stdout = logW
	c.Stderr = logW
	if err := c.Run(); err != nil {
		return "", fmt.Errorf("build_cmd: %w", err)
	}
	if err := copyTree(srcDir, artifactDir); err != nil {
		return "", err
	}
	return "", nil
}

// ─── helpers ──────────────────────────────────────────────────────

// runShellInSrc executes a build_cmd in srcDir. Always returns the
// binPath the caller suggested — the override is responsible for
// producing a binary at that path.
func runShellInSrc(srcDir, cmd string, logW io.Writer, expectedOutput string) (string, error) {
	fmt.Fprintf(logW, "+ %s (cwd=%s)\n", cmd, srcDir)
	c := exec.Command("sh", "-c", cmd)
	c.Dir = srcDir
	c.Stdout = logW
	c.Stderr = logW
	if err := c.Run(); err != nil {
		return "", err
	}
	return expectedOutput, nil
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
