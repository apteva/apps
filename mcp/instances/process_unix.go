//go:build !windows

package main

import (
	"os"
	"os/exec"
	"syscall"
)

func configureCommandProcess(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	c.Cancel = func() error {
		if c.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
		if err == syscall.ESRCH {
			return os.ErrProcessDone
		}
		return err
	}
}
func localNonblockFlag() int { return syscall.O_NONBLOCK }
