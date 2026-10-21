//go:build !linux && !darwin

package main

const nativeSnapshotsSupported = false

// Preserve ordinary native lifecycle operation on other controller platforms;
// stopped state transfer needs the crash-safe file lock provided on Linux/macOS.
func nativeStateFileLock(string) (func(), error) { return func() {}, nil }
