//go:build !linux && !darwin

package main

import "fmt"

func diskAvailable(path string) (int64, error) {
	return 0, fmt.Errorf("backup disk-space checks unsupported on this OS")
}
