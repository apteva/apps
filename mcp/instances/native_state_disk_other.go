//go:build !linux && !darwin

package main

import "errors"

func nativeDiskAvailable(string) (int64, error) { return 0, errors.New("state transfer unsupported") }
