package main

import "testing"

func TestReleaseManifestSourceMatchesVersion(t *testing.T) {
	m := (&App{}).Manifest()
	if m.Runtime.Source == nil {
		t.Fatal("release manifest must declare its source")
	}
	want := m.Name + "/v" + m.Version
	if m.Runtime.Source.Ref != want {
		t.Fatalf("source ref = %q, want %q; install would build the wrong release", m.Runtime.Source.Ref, want)
	}
}
