package main

import "testing"

// writableTempDir is t.TempDir for Deploy data directories: sealed artifacts
// inside it may be read-only, so restore write access before Go removes it.
func writableTempDir(t testing.TB) string {
	t.Helper()
	dir := t.TempDir()
	t.Cleanup(func() { _ = makeTreeWritable(dir) })
	return dir
}
