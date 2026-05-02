package main

import (
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
		// We don't ship a node builder yet; flag explicitly so the
		// caller can refuse with a clear error.
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
	case "blank":
		return &blankBuilder{}, nil
	case "":
		return nil, errors.New("framework not detected; set framework explicitly on the deployment")
	default:
		return nil, fmt.Errorf("framework %q not supported in v0.1 (supported: go, static, blank)", framework)
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
