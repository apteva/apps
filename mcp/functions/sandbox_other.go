//go:build !linux

package main

import "errors"

func platformSandboxSupported() bool { return false }
func sandboxRequiredByDefault() bool { return false }

func runSandboxHelper([]string) error {
	return errors.New("sandbox helper is only available on Linux")
}

func cleanupSandboxProcess(int, sandboxMode) {}
