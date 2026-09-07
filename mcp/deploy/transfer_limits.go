package main

import (
	"os"
	"strconv"
)

// Operator limits must be configured on both Deploy and its runner.
func transferLimit(name string, fallback int64) int64 {
	n, e := strconv.ParseInt(os.Getenv(name), 10, 64)
	if e != nil || n < 1 {
		return fallback
	}
	return n
}
func sourceTransferLimit() int64 {
	return transferLimit("DEPLOY_SOURCE_MAX_BYTES", maxSourceCapsuleBytes)
}
func sourceExpandedLimit() int64 {
	return transferLimit("DEPLOY_SOURCE_MAX_EXPANDED_BYTES", maxArchiveExpandedBytes)
}
