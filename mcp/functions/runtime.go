package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// runtimeSpec describes how to build and launch a warm worker for a
// runtime. node is interpreted (the harness is the entrypoint, the
// user's source is imported); go is compiled (the harness is built
// into the worker binary alongside the user's source). Both end up as
// a self-contained build dir holding everything a worker needs.
type runtimeSpec struct {
	Name string
	// EntryFile: the filename the user's source bytes are written as
	// inside the build dir.
	EntryFile string
	// ProbeBins must all be on PATH for this runtime to work; checked
	// at dispatch time so a missing toolchain fails the function, not
	// the sidecar.
	ProbeBins []string
	// Stage writes the runtime's fixed support files (the harness, a
	// go.mod, …) into buildDir alongside the entry file.
	Stage func(buildDir string) error
	// Build runs the runtime's build step in buildDir. pkgManifest is
	// the optional package_json (node dependency install); "" if none.
	// May be a no-op (node with no deps).
	Build func(buildDir, pkgManifest string) error
	// WorkerCmd returns how to launch a built worker for buildDir:
	// the binary (PATH-resolved or an absolute path) and its args.
	WorkerCmd func(buildDir string) (bin string, args []string)
}

// runtimes is the supported runtime set.
//
//   - node — interpreted; Node 18+ ships a global fetch.
//   - go   — compiled; `go build` at deploy. apteva-server already
//            needs a Go toolchain on PATH to build kind:source apps,
//            so the runtime is guaranteed present.
//
// bun is out (its node:net can't adopt the inherited socket fd);
// python is a planned follow-on.
var runtimes = map[string]runtimeSpec{
	"node": {
		Name:      "node",
		EntryFile: "entry.mjs",
		ProbeBins: []string{"node"},
		Stage: func(dir string) error {
			return os.WriteFile(filepath.Join(dir, "node_harness.mjs"), nodeHarness, 0o600)
		},
		Build: func(dir, pkgManifest string) error {
			if strings.TrimSpace(pkgManifest) == "" {
				return nil
			}
			if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(pkgManifest), 0o600); err != nil {
				return err
			}
			return runNpmInstall(dir)
		},
		WorkerCmd: func(dir string) (string, []string) {
			return "node", []string{filepath.Join(dir, "node_harness.mjs")}
		},
	},
	"go": {
		Name:      "go",
		EntryFile: "entry.go",
		ProbeBins: []string{"go"},
		Stage: func(dir string) error {
			if err := os.WriteFile(filepath.Join(dir, "harness.go"), goHarness, 0o600); err != nil {
				return err
			}
			// Minimal module so `go build` works; stdlib-only for v1.1
			// (third-party deps are a planned follow-on).
			gomod := "module aptevafn\n\ngo 1.22\n"
			return os.WriteFile(filepath.Join(dir, "go.mod"), []byte(gomod), 0o600)
		},
		Build: func(dir, _ string) error {
			return runGoBuild(dir)
		},
		WorkerCmd: func(dir string) (string, []string) {
			return filepath.Join(dir, "worker"), nil
		},
	},
}

// resolveRuntime returns the spec for a runtime name, after probing
// PATH for its toolchain. Failure means "this host can't run this
// function" — a dispatch-time error, not a sidecar-boot crash.
func resolveRuntime(name string) (runtimeSpec, error) {
	switch name {
	case "bun":
		return runtimeSpec{}, fmt.Errorf("runtime %q is not supported — bun's node:net can't adopt the worker socket fd; use node or go", name)
	case "python":
		return runtimeSpec{}, fmt.Errorf("runtime %q is not available yet — node and go are the supported runtimes", name)
	case "sh":
		return runtimeSpec{}, fmt.Errorf("runtime %q was removed — the warm-worker model needs an exported handler; use node or go", name)
	}
	spec, ok := runtimes[name]
	if !ok {
		return runtimeSpec{}, fmt.Errorf("runtime %q not supported (node|go)", name)
	}
	for _, bin := range spec.ProbeBins {
		if _, err := exec.LookPath(bin); err != nil {
			return runtimeSpec{}, fmt.Errorf("runtime %q needs %q on PATH", name, bin)
		}
	}
	return spec, nil
}
