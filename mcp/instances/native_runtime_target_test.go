package main

import (
	"encoding/json"
	sdk "github.com/apteva/app-sdk"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNativeInspectTargetWithoutArtifact(t *testing.T) {
	b, _, req := newNativeTest(t)
	req.Options = map[string]any{"gateway_url": "https://gateway.test"}
	b.db.Exec(`DELETE FROM native_artifacts`)
	_, w := nativeCall(t, b, "inspect-target", req)
	raw := w.Body.Bytes()
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	var target sdk.RuntimeTargetInfo
	if json.Unmarshal(raw, &target) != nil || target.Platform != "linux-amd64" || target.HostKey == "" || target.ABI != "gnu" {
		t.Fatalf("invalid target: %s", raw)
	}
	var count int
	b.db.QueryRow(`SELECT count(*) FROM native_deployments`).Scan(&count)
	if count != 0 {
		t.Fatal("target inspection created deployment")
	}
	req.Target.InstallID++
	_, w = nativeCall(t, b, "inspect-target", req)
	if w.Code == 200 {
		t.Fatal("accepted foreign target")
	}
	caps := nativeCapabilities().(map[string]any)
	if caps["package_delivery"] == nil || caps["target_inspection"] != true {
		t.Fatal("missing automatic deployment capabilities")
	}
}
func TestNativeTargetProbeParsesEnvironment(t *testing.T) {
	raw := "x86_64\n" + strings.Repeat("a", 32) + "\nssh-ed25519 identity\n2048\n4096\nABI=gnu\nLIB=libc.so.6\nLIB=libdl.so.2"
	p, err := parseNativeTargetProbe(raw)
	if err != nil || p.ABI != "gnu" || len(p.SystemLibraries) != 2 {
		t.Fatalf("%+v %v", p, err)
	}
	for _, bad := range []string{strings.Replace(raw, "ABI=gnu", "ABI=unknown", 1), raw + "\nLIB=../secret"} {
		if _, err = parseNativeTargetProbe(bad); err == nil {
			t.Fatal("accepted unsafe inventory")
		}
	}
}
func TestNativePackageReferenceCannotSubstituteExecutable(t *testing.T) {
	b, _, req := newNativeTest(t)
	a, err := b.artifact("fixture", true)
	if err != nil {
		t.Fatal(err)
	}
	normalized := a
	normalized.ID = ""
	req.Package = &nativePackageReference{ID: a.ID, Platform: a.Platform, SHA256: nativeHash(nativeJSON(normalized)), ExecutableSHA256: nativeArtifactDigest(a)}
	if _, _, _, _, err = b.validate(req); err != nil {
		t.Fatal(err)
	}
	req.Package.ExecutableSHA256 = strings.Repeat("0", 64)
	if _, _, _, _, err = b.validate(req); err == nil {
		t.Fatal("accepted substituted executable")
	}
}

func TestNativePackageRuntimeRequirementsAreSealedAndChecked(t *testing.T) {
	b, _, req := newNativeTest(t)
	a, _ := b.artifact("fixture", true)
	a.ID = "library-fixture"
	descriptor := []byte(`{"abi":"gnu","system_libraries":["libc.so.6"]}`)
	a.Files = append(a.Files, nativeArtifactFile{Path: "apteva-runtime-libraries.json", Size: int64(len(descriptor)), SHA256: nativeHash(string(descriptor))})
	if err := b.registerArtifact(a); err != nil {
		t.Fatal(err)
	}
	for _, file := range a.Files {
		data := descriptor
		if file.Path != "apteva-runtime-libraries.json" {
			data, _ = os.ReadFile(filepath.Join(b.artifactDir("fixture"), file.Path))
		}
		dest := filepath.Join(b.artifactDir(a.ID), file.Path)
		os.MkdirAll(filepath.Dir(dest), 0700)
		if err := os.WriteFile(dest, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.sealArtifact(a); err != nil {
		t.Fatal(err)
	}
	normalized := a
	normalized.ID = ""
	req.Options["artifact_id"] = a.ID
	req.Package = &nativePackageReference{ID: a.ID, Platform: a.Platform, SHA256: nativeHash(nativeJSON(normalized)), ExecutableSHA256: nativeArtifactDigest(a), ABI: "gnu", SystemLibraries: []string{"libc.so.6"}}
	if _, _, _, _, err := b.validate(req); err != nil {
		t.Fatal(err)
	}
	req.Package.SystemLibraries = []string{"libmissing.so"}
	if _, _, _, _, err := b.validate(req); err == nil {
		t.Fatal("accepted changed sealed requirements")
	}
	req.Package.SystemLibraries = []string{"libc.so.6"}
	req.TargetInfo = &sdk.RuntimeTargetInfo{HostKey: "replacement-host", Platform: "linux-amd64", ABI: "gnu"}
	if _, _, _, _, err := b.validate(req); err == nil {
		t.Fatal("accepted target identity change")
	}
}
func TestNativeVerifiedArchiveHandoffPreservesChecksum(t *testing.T) {
	b, _, req := newNativeTest(t)
	a, _ := b.artifact("fixture", true)
	normalized := a
	normalized.ID = ""
	sha := strings.Repeat("a", 64)
	req.Manifest.Runtime.Artifacts = map[string]sdk.BundleSpec{"linux-amd64": {URL: "https://release.test/package.tgz", SHA256: sha}}
	req.Package = &nativePackageReference{ID: a.ID, Platform: a.Platform, SHA256: nativeHash(nativeJSON(normalized)), ExecutableSHA256: nativeArtifactDigest(a), ArchiveSHA256: sha}
	if _, _, _, _, err := b.validate(req); err != nil {
		t.Fatal(err)
	}
	req.Package.ArchiveSHA256 = strings.Repeat("b", 64)
	if _, _, _, _, err := b.validate(req); err == nil {
		t.Fatal("accepted changed archive checksum")
	}
}

func TestNativeInspectPendingPreparationUsesNamespacedStringID(t *testing.T) {
	b, f, req := newNativeTest(t)
	gate := make(chan struct{})
	f.prepareGate = gate
	defer close(gate)
	_, w := nativeCall(t, b, "prepare", req)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	deadline := time.Now().Add(time.Second)
	held := false
	for time.Now().Before(deadline) {
		if !b.lock(req.DeploymentID).TryLock() {
			held = true
			break
		}
		b.lock(req.DeploymentID).Unlock()
		time.Sleep(time.Millisecond)
	}
	if !held {
		t.Fatal("preparation did not acquire deployment lock")
	}
	out, w := nativeCall(t, b, "inspect", req)
	if w.Code != 200 || out.State != "preparing" || out.Handle != req.DeploymentID {
		t.Fatalf("%d %+v %s", w.Code, out, w.Body.String())
	}
	req.Target.ID = "not-a-number"
	_, w = nativeCall(t, b, "inspect", req)
	if w.Code == 200 {
		t.Fatal("accepted invalid target")
	}
}
