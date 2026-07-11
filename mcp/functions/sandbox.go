package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

const sandboxHelperArg = "__functions_sandbox"

type sandboxMode string

const (
	sandboxWorker sandboxMode = "worker"
	sandboxBuild  sandboxMode = "build"
)

type sandboxSpec struct {
	Mode           sandboxMode
	Root           string
	TempDir        string
	MemoryMB       int
	CgroupRoot     string
	RequireCgroup  bool
	RequireSandbox bool
}

// sandboxCommand launches target through this binary's small helper on Linux.
// The helper applies limits and Landlock before syscall.Exec replaces it with
// node/go/npm. Other platforms use the target directly so local development
// keeps working; production can require Linux isolation with
// APTEVA_FUNCTIONS_REQUIRE_SANDBOX=true.
func sandboxCommand(target string, args []string, spec sandboxSpec) (*exec.Cmd, error) {
	if !platformSandboxSupported() {
		if envBool("APTEVA_FUNCTIONS_REQUIRE_SANDBOX", false) {
			return nil, errors.New("function sandbox required but unavailable on this platform")
		}
		return exec.Command(target, args...), nil
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolve sandbox helper: %w", err)
	}
	helperArgs := []string{
		sandboxHelperArg,
		"--mode=" + string(spec.Mode),
		"--root=" + spec.Root,
		"--tmp=" + spec.TempDir,
		"--memory-mb=" + strconv.Itoa(spec.MemoryMB),
		"--cgroup-root=" + strings.TrimSpace(os.Getenv("APTEVA_FUNCTIONS_CGROUP_ROOT")),
		"--require-cgroup=" + strconv.FormatBool(envBool("APTEVA_FUNCTIONS_REQUIRE_CGROUP", false)),
		"--require-sandbox=" + strconv.FormatBool(envBool("APTEVA_FUNCTIONS_REQUIRE_SANDBOX", sandboxRequiredByDefault())),
		"--",
		target,
	}
	helperArgs = append(helperArgs, args...)
	return exec.Command(exe, helperArgs...), nil
}

func maybeRunSandboxHelper() bool {
	if len(os.Args) < 2 || os.Args[1] != sandboxHelperArg {
		return false
	}
	if err := runSandboxHelper(os.Args[2:]); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "functions sandbox:", err)
		os.Exit(126)
	}
	os.Exit(0)
	return true
}

func parseSandboxArgs(args []string) (sandboxSpec, string, []string, error) {
	spec := sandboxSpec{}
	sep := -1
	for i, arg := range args {
		if arg == "--" {
			sep = i
			break
		}
		switch {
		case strings.HasPrefix(arg, "--mode="):
			spec.Mode = sandboxMode(strings.TrimPrefix(arg, "--mode="))
		case strings.HasPrefix(arg, "--root="):
			spec.Root = strings.TrimPrefix(arg, "--root=")
		case strings.HasPrefix(arg, "--tmp="):
			spec.TempDir = strings.TrimPrefix(arg, "--tmp=")
		case strings.HasPrefix(arg, "--memory-mb="):
			spec.MemoryMB, _ = strconv.Atoi(strings.TrimPrefix(arg, "--memory-mb="))
		case strings.HasPrefix(arg, "--cgroup-root="):
			spec.CgroupRoot = strings.TrimPrefix(arg, "--cgroup-root=")
		case strings.HasPrefix(arg, "--require-cgroup="):
			spec.RequireCgroup, _ = strconv.ParseBool(strings.TrimPrefix(arg, "--require-cgroup="))
		case strings.HasPrefix(arg, "--require-sandbox="):
			spec.RequireSandbox, _ = strconv.ParseBool(strings.TrimPrefix(arg, "--require-sandbox="))
		default:
			return spec, "", nil, fmt.Errorf("unknown helper argument %q", arg)
		}
	}
	if sep < 0 || sep+1 >= len(args) {
		return spec, "", nil, errors.New("sandbox target missing")
	}
	if spec.Mode != sandboxWorker && spec.Mode != sandboxBuild {
		return spec, "", nil, fmt.Errorf("invalid sandbox mode %q", spec.Mode)
	}
	root, err := filepath.Abs(spec.Root)
	if err != nil || root == "" || root == "/" {
		return spec, "", nil, errors.New("invalid sandbox root")
	}
	spec.Root = root
	if spec.TempDir != "" {
		spec.TempDir, _ = filepath.Abs(spec.TempDir)
	}
	return spec, args[sep+1], args[sep+2:], nil
}

func envBool(key string, def bool) bool {
	v := strings.TrimSpace(strings.ToLower(os.Getenv(key)))
	if v == "" {
		return def
	}
	return v == "1" || v == "true" || v == "yes" || v == "on"
}

func envInt(key string, def, lo, hi int) int {
	n, err := strconv.Atoi(strings.TrimSpace(os.Getenv(key)))
	if err != nil || n < lo || n > hi {
		return def
	}
	return n
}
