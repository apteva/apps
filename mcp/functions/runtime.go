package main

import (
	"fmt"
	"os/exec"
)

// runtimeSpec describes how to spawn one of the supported runtimes.
//
// EntryFile is the filename the resolved source bytes are written to
// inside the per-invocation temp dir. Most runtimes just need that
// file to exist; sh additionally needs +x but we exec via the
// interpreter (`sh entry.sh`) to side-step that, keeping the spawn
// path uniform across runtimes.
//
// Bin is looked up via exec.LookPath at invoke time, so a missing
// runtime fails the function (not the sidecar boot) — install code,
// don't crash the platform.
type runtimeSpec struct {
	Name      string
	EntryFile string
	// Argv builds the full command-line. Receives the absolute entry
	// file path; returns (binary, args). Binary is passed through
	// exec.LookPath before spawn.
	Argv func(entryPath string) (string, []string)
}

var runtimes = map[string]runtimeSpec{
	"bun": {
		Name:      "bun",
		EntryFile: "entry.ts",
		Argv:      func(p string) (string, []string) { return "bun", []string{"run", p} },
	},
	"node": {
		Name:      "node",
		EntryFile: "entry.mjs",
		Argv:      func(p string) (string, []string) { return "node", []string{p} },
	},
	"python": {
		Name:      "python",
		EntryFile: "entry.py",
		// Prefer python3; runtime resolution below falls back to python.
		Argv: func(p string) (string, []string) { return "python3", []string{p} },
	},
	"sh": {
		Name:      "sh",
		EntryFile: "entry.sh",
		Argv:      func(p string) (string, []string) { return "sh", []string{p} },
	},
}

// resolveRuntime returns the runtimeSpec for the given runtime name,
// after probing exec.LookPath to either confirm the canonical binary
// is available or substitute a known fallback (python3 → python).
// Failure means "this host can't run this function"; surface a
// dispatch-time error rather than a sidecar-boot crash.
func resolveRuntime(name string) (runtimeSpec, error) {
	spec, ok := runtimes[name]
	if !ok {
		return runtimeSpec{}, fmt.Errorf("runtime %q not supported", name)
	}
	bin, _ := spec.Argv("/dev/null")
	if _, err := exec.LookPath(bin); err == nil {
		return spec, nil
	}
	// Python fallback: some hosts only ship `python` (e.g. Alpine
	// without python3 alias). Probe for it before giving up.
	if name == "python" {
		if _, err := exec.LookPath("python"); err == nil {
			fallback := spec
			fallback.Argv = func(p string) (string, []string) { return "python", []string{p} }
			return fallback, nil
		}
	}
	return runtimeSpec{}, fmt.Errorf("runtime %q binary %q not found in PATH", name, bin)
}
