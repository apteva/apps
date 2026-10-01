package main

import "testing"

func TestReleaseSourceMatchesManifestVersion(t *testing.T) {
	manifest := (&App{}).Manifest()
	if manifest.Runtime.Source == nil {
		t.Fatal("Processes must publish a pinned source runtime")
	}
	want := manifest.Name + "/v" + manifest.Version
	if manifest.Runtime.Source.Ref != want {
		t.Fatalf("source ref %q does not match release %q", manifest.Runtime.Source.Ref, want)
	}
}
