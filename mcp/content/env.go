package main

import "os"

// getEnv is the single seam render.go uses to read env. Wrapping
// os.Getenv keeps the rest of the file decoupled from the os
// package.
func getEnv(k string) string { return os.Getenv(k) }
