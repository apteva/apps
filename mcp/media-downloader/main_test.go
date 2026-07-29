package main

import "testing"

func TestManifestVersionAndEventContract(t *testing.T) {
	manifest := (&App{}).Manifest()
	if manifest.Version != "0.2.12" {
		t.Fatalf("manifest version = %q, want 0.2.12", manifest.Version)
	}
}
