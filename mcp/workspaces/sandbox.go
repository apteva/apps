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
)

type sandboxKind string

const (
	sandboxNone     sandboxKind = ""
	sandboxSeatbelt sandboxKind = "seatbelt"
	sandboxBwrap    sandboxKind = "bwrap"
)

const seatbeltPath = "/usr/bin/sandbox-exec"

// secretHomePaths are hidden from sandboxed commands: keys, cloud
// credentials, and Apteva's own config (which holds API keys).
var secretHomePaths = []string{
	".ssh", ".aws", ".gnupg", ".docker", ".kube", ".netrc", ".npmrc", ".pypirc",
	".apteva", ".apteva-prod", ".codex", ".claude", ".config/gh", ".config/gcloud",
	".git-credentials", "Library/Keychains",
}

func detectSandbox() sandboxKind {
	switch runtime.GOOS {
	case "darwin":
		if _, err := os.Stat(seatbeltPath); err == nil {
			return sandboxSeatbelt
		}
	case "linux":
		if _, err := exec.LookPath("bwrap"); err == nil {
			return sandboxBwrap
		}
	}
	return sandboxNone
}

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
