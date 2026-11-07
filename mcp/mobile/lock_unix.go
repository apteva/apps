//go:build darwin || linux

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func lockDevice(dir, identity string) (*os.File, error) {
	h := sha256.Sum256([]byte(identity))
	p := filepath.Join(dir, hex.EncodeToString(h[:])+".lock")
	f, err := os.OpenFile(p, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, problem("device_unavailable", "another Mobile process has reserved this device")
	}
	return f, nil
}
func unlockDevice(f *os.File) {
	if f != nil {
		_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
		_ = f.Close()
	}
}
