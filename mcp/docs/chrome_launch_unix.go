//go:build !windows

package main

import (
	"fmt"
	"os"
	"syscall"
)

const chromeWrapperEnv = "APTEVA_DOCS_CHROME_WRAPPER"
const chromeRealPathEnv = "APTEVA_DOCS_CHROME_REAL_PATH"

func init() {
	if os.Getenv(chromeWrapperEnv) != "1" {
		return
	}
	realPath := os.Getenv(chromeRealPathEnv)
	if realPath == "" {
		_, _ = fmt.Fprintln(os.Stderr, "docs Chrome wrapper: real executable missing")
		os.Exit(127)
	}
	_ = os.Unsetenv(chromeWrapperEnv)
	_ = os.Unsetenv(chromeRealPathEnv)
	if os.Geteuid() == 0 {
		if err := syscall.Setgroups([]int{}); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, "docs Chrome wrapper: drop groups:", err)
			os.Exit(127)
		}
		if err := syscall.Setgid(65534); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, "docs Chrome wrapper: drop gid:", err)
			os.Exit(127)
		}
		if err := syscall.Setuid(65534); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, "docs Chrome wrapper: drop uid:", err)
			os.Exit(127)
		}
	}
	if err := syscall.Exec(realPath, append([]string{realPath}, os.Args[1:]...), os.Environ()); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "docs Chrome wrapper: exec:", err)
		os.Exit(127)
	}
}

func prepareChromeLaunch(realPath, profileDir string) (string, []string, error) {
	if os.Geteuid() == 0 {
		if err := os.Chown(profileDir, 65534, 65534); err != nil {
			return "", nil, fmt.Errorf("prepare non-root Chrome profile: %w", err)
		}
	}
	self, err := os.Executable()
	if err != nil {
		return "", nil, fmt.Errorf("resolve Docs executable for Chrome wrapper: %w", err)
	}
	env := append(os.Environ(),
		chromeWrapperEnv+"=1",
		chromeRealPathEnv+"="+realPath,
		"HOME="+profileDir,
	)
	return self, env, nil
}
