package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// Shared macOS signing state is serialized across Deploy instances. The lock is
// held until build cleanup, not merely while installing the certificate.
func lockIOSSigningResources(ctx context.Context) (func(), error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	file, err := os.OpenFile(filepath.Join(home, ".apteva-ios-signing.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			file.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			file.Close()
			return nil, ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
	var once sync.Once
	return func() { once.Do(func() { _ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN); _ = file.Close() }) }, nil
}
