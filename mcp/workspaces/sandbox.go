package main

// OS sandboxes for the local driver. Both keep commands from writing anywhere
// except the workload folder (plus /dev/null and terminals), and hide the
// usual credential folders in the home directory. Network is open or, with
// local_network=none, limited to loopback.

import (
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

type sandboxKind string

const (
	sandboxNone     sandboxKind = ""
	sandboxSeatbelt sandboxKind = "seatbelt"
	sandboxBwrap    sandboxKind = "bwrap"
	// sandboxContainer runs commands directly, as a separate unprivileged
	// user per workspace, and treats the surrounding container as the
	// isolation boundary. Only used inside a container.
	sandboxContainer sandboxKind = "container"
)

const seatbeltPath = "/usr/bin/sandbox-exec"

// secretHomePaths are hidden from sandboxed commands: keys, cloud
// credentials, and Apteva's own config (which holds API keys).
var secretHomePaths = []string{
	".ssh", ".aws", ".gnupg", ".docker", ".kube", ".netrc", ".npmrc", ".pypirc",
	".apteva", ".apteva-prod", ".codex", ".claude", ".config/gh", ".config/gcloud",
	".git-credentials", "Library/Keychains",
}

// local_sandbox setting values.
const (
	sandboxModeAuto      = "auto"
	sandboxModeOS        = "os"
	sandboxModeContainer = "container"
)

// sandboxFacts is what the machine supports, probed once per process.
type sandboxFacts struct {
	Seatbelt    bool // sandbox-exec exists (macOS)
	Bwrap       bool // bwrap starts a sandbox with open network
	BwrapNoNet  bool // bwrap also starts with --unshare-net
	InContainer bool // /.dockerenv, /run/.containerenv or $container
	SetUID      bool // can start a process as another user
}

var (
	sandboxFactsOnce   sync.Once
	sandboxFactsCached sandboxFacts
	// probeSandboxFacts is replaced in tests.
	probeSandboxFacts = realSandboxFacts
)

func currentSandboxFacts() sandboxFacts {
	sandboxFactsOnce.Do(func() { sandboxFactsCached = probeSandboxFacts() })
	return sandboxFactsCached
}

func realSandboxFacts() sandboxFacts {
	var f sandboxFacts
	switch runtime.GOOS {
	case "darwin":
		_, err := os.Stat(seatbeltPath)
		f.Seatbelt = err == nil
	case "linux":
		if path, err := exec.LookPath("bwrap"); err == nil {
			base := []string{"--ro-bind", "/", "/", "--dev", "/dev", "--proc", "/proc", "--unshare-pid", "--die-with-parent"}
			f.Bwrap = probeRun(path, append(append([]string{}, base...), "true")...)
			if f.Bwrap {
				f.BwrapNoNet = probeRun(path, append(append([]string{}, base...), "--unshare-net", "true")...)
			}
		}
		f.InContainer = insideContainer()
		f.SetUID = probeSetUID()
	}
	return f
}

func probeRun(name string, args ...string) bool {
	cmd := exec.Command(name, args...)
	if err := cmd.Start(); err != nil {
		return false
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err == nil
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		return false
	}
}

func insideContainer() bool {
	for _, marker := range []string{"/.dockerenv", "/run/.containerenv"} {
		if _, err := os.Stat(marker); err == nil {
			return true
		}
	}
	return os.Getenv("container") != ""
}

// probeSetUID starts `true` as an unused uid: it works only when Workspaces
// runs as root (or holds CAP_SETUID and CAP_SETGID).
func probeSetUID() bool {
	truePath, err := exec.LookPath("true")
	if err != nil {
		return false
	}
	cmd := exec.Command(truePath)
	cmd.Dir = "/"
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: localUIDBase - 1, Gid: localUIDBase - 1, Groups: []uint32{}}}
	return cmd.Run() == nil
}

// detectSandbox is the sandbox auto mode picks with open network.
func detectSandbox() sandboxKind {
	kind, _ := resolveSandbox(sandboxModeAuto, "open")
	return kind
}

// resolveSandbox picks the sandbox for new local workspaces from the
// local_sandbox setting and local_network. It never returns sandboxNone
// without an error: a bare host without an OS sandbox is refused, and
// container mode never runs on a host.
func resolveSandbox(mode, network string) (sandboxKind, error) {
	f := currentSandboxFacts()
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		mode = sandboxModeAuto
	}
	osSandbox := func() (sandboxKind, error) {
		if f.Seatbelt {
			return sandboxSeatbelt, nil
		}
		if f.Bwrap {
			if network == "none" && !f.BwrapNoNet {
				return sandboxNone, errors.New("local_network is none, but bubblewrap cannot create a network namespace here")
			}
			return sandboxBwrap, nil
		}
		return sandboxNone, errors.New("the local driver needs an OS sandbox: sandbox-exec on macOS or a working bubblewrap (bwrap) on Linux")
	}
	container := func() (sandboxKind, error) {
		if !f.InContainer {
			return sandboxNone, errors.New("local_sandbox is container, but Workspaces is not running inside a container; container mode is refused on a host")
		}
		if !f.SetUID {
			return sandboxNone, errors.New("container mode runs each workspace as its own user, which needs Workspaces to run as root (or with CAP_SETUID and CAP_SETGID)")
		}
		if network == "none" {
			return sandboxNone, errors.New("local_network none cannot be enforced in container mode (no network namespaces); set local_network to open or use the containers driver")
		}
		return sandboxContainer, nil
	}
	switch mode {
	case sandboxModeOS:
		return osSandbox()
	case sandboxModeContainer:
		return container()
	case sandboxModeAuto:
		kind, err := osSandbox()
		if err == nil || !f.InContainer {
			return kind, err
		}
		return container()
	}
	return sandboxNone, fmt.Errorf("local_sandbox must be %s, %s or %s", sandboxModeAuto, sandboxModeOS, sandboxModeContainer)
}

// isolationLabel is what workspaces_get and the panel show.
func isolationLabel(kind sandboxKind) string {
	switch kind {
	case sandboxSeatbelt, sandboxBwrap, sandboxContainer:
		return string(kind)
	}
	return ""
}

// resetSandboxFacts is for tests that swap probeSandboxFacts.
func resetSandboxFacts() { sandboxFactsOnce = sync.Once{} }

// sandboxArgv wraps argv so it runs inside the sandbox with write access to
// workloadDir only.
func sandboxArgv(kind sandboxKind, workloadDir, network, workingDir string, argv []string) ([]string, error) {
	home, _ := os.UserHomeDir()
	if real, err := filepath.EvalSymlinks(home); err == nil {
		home = real
	}
	switch kind {
	case sandboxSeatbelt:
		return append([]string{seatbeltPath, "-p", seatbeltProfile(workloadDir, home, network), "--"}, argv...), nil
	case sandboxBwrap:
		return bwrapArgv(workloadDir, home, network, workingDir, argv), nil
	case sandboxContainer:
		// Runs as the workspace's own user (see execStart).
		return append([]string{}, argv...), nil
	}
	return nil, errors.New("no OS sandbox is available for the local driver")
}

func sbString(value string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(value) + `"`
}

func seatbeltProfile(workloadDir, home, network string) string {
	var b strings.Builder
	b.WriteString("(version 1)\n(allow default)\n")
	b.WriteString("(deny file-write*)\n")
	b.WriteString(`(allow file-write*
  (literal "/dev/null") (literal "/dev/zero") (literal "/dev/random") (literal "/dev/urandom")
  (literal "/dev/dtracehelper") (literal "/dev/tty") (literal "/dev/ptmx")
  (regex #"^/dev/ttys[0-9]+$") (regex #"^/dev/fd/[0-9]+$"))
`)
	if home != "" {
		// Contents only: denying metadata too would stop `cd` and realpath
		// from walking through ~/.apteva to the workload folder below it.
		b.WriteString("(deny file-read-data file-write*")
		for _, rel := range secretHomePaths {
			b.WriteString("\n  (subpath " + sbString(filepath.Join(home, rel)) + ")")
		}
		b.WriteString(")\n")
	}
	// Last matching rule wins: the workload folder stays usable even if an
	// operator points local_root somewhere odd (the registry refuses roots
	// inside the hidden folders, where getcwd would fail).
	fmt.Fprintf(&b, "(allow file-read* file-write* (subpath %s))\n", sbString(workloadDir))
	if network == "none" {
		b.WriteString(`(deny network*)
(allow network* (local unix-socket))
(allow network-bind (local ip "localhost:*"))
(allow network-inbound (local ip "localhost:*"))
(allow network-outbound (remote ip "localhost:*"))
`)
	}
	return b.String()
}

// rootInsideHidden reports which hidden folder contains root, if any.
// Commands there could not resolve their own working directory.
func rootInsideHidden(root, home string) string {
	if home == "" {
		return ""
	}
	if real, err := filepath.EvalSymlinks(home); err == nil {
		home = real
	}
	for _, rel := range secretHomePaths {
		if withinDir(filepath.Join(home, rel), root) {
			return "~/" + rel
		}
	}
	return ""
}

func bwrapArgv(workloadDir, home, network, workingDir string, argv []string) []string {
	args := []string{"bwrap", "--ro-bind", "/", "/", "--dev", "/dev", "--proc", "/proc", "--tmpfs", "/tmp",
		"--unshare-pid", "--die-with-parent"}
	if network == "none" {
		args = append(args, "--unshare-net")
	}
	if home != "" {
		for _, rel := range secretHomePaths {
			target := filepath.Join(home, rel)
			info, err := os.Stat(target)
			if err != nil {
				continue
			}
			if info.IsDir() {
				args = append(args, "--tmpfs", target)
			} else {
				args = append(args, "--ro-bind", "/dev/null", target)
			}
		}
	}
	// Bound after the hidden folders so it shows through even under ~/.apteva.
	args = append(args, "--bind", workloadDir, workloadDir, "--chdir", workingDir, "--")
	return append(args, argv...)
}
