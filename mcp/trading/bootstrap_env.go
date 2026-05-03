package main

import "os"

// Tiny wrapper around os.LookupEnv so bootstrap_test.go can swap the
// env source (avoids os.Setenv interactions when running tests in
// parallel).
func osLookupEnv(key string) (string, bool) {
	return os.LookupEnv(key)
}
