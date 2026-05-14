package main

import (
	"fmt"
	"os/exec"
)

// runtimeSpec describes how to launch a warm worker for a runtime.
//
// Workers are long-lived: WorkerArgv boots the language harness
// (harness/<Harness>), which imports the function's handler module
// once and then serves invocations over a socketpair fd until the
// pool reaps it. EntryFile is the filename the resolved source bytes
// are staged as inside the version's build dir.
type runtimeSpec struct {
	Name      string
	EntryFile string
	Harness   string // harness filename, written into the pool's harness dir
	// WorkerArgv builds the command line that boots a worker. Receives
	// the absolute path to the staged harness file; returns (binary,
	// args). Binary is resolved via exec.LookPath before spawn.
	WorkerArgv func(harnessPath string) (string, []string)
}

// runtimes is the supported runtime set.
//
// node only, for now. bun was a candidate (it shares node's module
// system) but its node:net can't adopt an inherited socketpair fd —
// `new net.Socket({fd})` fails with ERR_SOCKET_CLOSED — and that fd
// handoff is how the pool talks to a worker. python returns in a
// later phase; sh is gone (the warm-worker model needs an exported
// handler, which a shell script has no notion of).
var runtimes = map[string]runtimeSpec{
	"node": {
		Name: "node", EntryFile: "entry.mjs", Harness: "node.mjs",
		WorkerArgv: func(h string) (string, []string) { return "node", []string{h} },
	},
}

// resolveRuntime returns the spec for a runtime name, after probing
// exec.LookPath to confirm the binary is on this host. Failure means
// "this host can't run this function" — a dispatch-time error, not a
// sidecar-boot crash.
func resolveRuntime(name string) (runtimeSpec, error) {
	switch name {
	case "bun":
		return runtimeSpec{}, fmt.Errorf("runtime %q is not supported — bun's node:net can't adopt the worker socket fd; use node", name)
	case "python":
		return runtimeSpec{}, fmt.Errorf("runtime %q is not available yet — node is the supported runtime in this release", name)
	case "sh":
		return runtimeSpec{}, fmt.Errorf("runtime %q was removed — the warm-worker model needs an exported handler; use node", name)
	}
	spec, ok := runtimes[name]
	if !ok {
		return runtimeSpec{}, fmt.Errorf("runtime %q not supported (node)", name)
	}
	bin, _ := spec.WorkerArgv("/dev/null")
	if _, err := exec.LookPath(bin); err != nil {
		return runtimeSpec{}, fmt.Errorf("runtime %q binary %q not found in PATH", name, bin)
	}
	return spec, nil
}
