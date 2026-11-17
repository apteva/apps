package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

func protectFixture(t *testing.T, framework string) (exportFixture, *Build) {
	t.Helper()
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"))
	t.Setenv("APTEVA_PROJECT_ID", "")
	setBootRecoveryGlobalCtx(t, ctx)
	app := &App{dataDir: writableTempDir(t), retainRollbacks: 1}
	d, env := createBootRecoveryDeployment(t, ctx)
	b := createBootRecoveryBuild(t, ctx, app.dataDir, d.ID, env.ID, "<html>home</html>")
	b.Framework = framework
	for name, body := range map[string]string{"ops/main_client.py": "VALUE = 1\n", "ops/__init__.py": ""} {
		p := filepath.Join(b.ArtifactPath, filepath.FromSlash(name))
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return exportFixture{ctx: ctx, app: app, dep: d, env: env, build: b}, b
}

func TestSealedStaticArtifactCannotBeChangedByImports(t *testing.T) {
	f, b := protectFixture(t, "static")
	if err := sealBuildArtifactProtected(b, ""); err != nil {
		t.Fatal(err)
	}
	digest, _, err := verifiedBuildEvidence(b)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(b.ArtifactPath, "stray.txt"), []byte("x"), 0o644); err == nil {
		t.Fatal("wrote into a sealed static artifact")
	}
	if py, err := exec.LookPath("python3"); err == nil {
		// Bytecode writes are on by default here; only the read-only seal stops them.
		cmd := exec.Command(py, "-c", "import ops.main_client")
		cmd.Dir = b.ArtifactPath
		cmd.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("python import: %v %s", err, out)
		}
	}
	if node, err := exec.LookPath("node"); err == nil {
		_ = os.Chmod(b.ArtifactPath, 0o755)
		_ = os.WriteFile(filepath.Join(b.ArtifactPath, "probe.js"), []byte("module.exports=1"), 0o444)
		_ = os.Chmod(b.ArtifactPath, 0o555)
		if after, _, err := verifiedBuildEvidence(b); err == nil || after != "" {
			t.Fatalf("added probe.js passed verification")
		}
		_ = os.Chmod(b.ArtifactPath, 0o755)
		_ = os.Remove(filepath.Join(b.ArtifactPath, "probe.js"))
		_ = os.Chmod(b.ArtifactPath, 0o555)
		cmd := exec.Command(node, "-e", "require('./ops/main_client.py')")
		cmd.Dir = b.ArtifactPath
		_ = cmd.Run()
	}
	again, _, err := verifiedBuildEvidence(b)
	if err != nil || again != digest {
		t.Fatalf("digest changed after running code in the artifact: %v", err)
	}
	// Retention can still remove the read-only tree.
	if err := removeArtifactTree(b.ArtifactPath); err != nil || pathExists(b.ArtifactPath) {
		t.Fatalf("remove protected artifact: %v", err)
	}
	_ = f
}

func TestServiceArtifactDriftNamesChangedPaths(t *testing.T) {
	_, b := protectFixture(t, "bun")
	if err := sealBuildArtifactProtected(b, ""); err != nil {
		t.Fatal(err)
	}
	// Service artifacts run in place, so they stay writable.
	stray := filepath.Join(b.ArtifactPath, "ops", "__pycache__", "main_client.cpython-313.pyc")
	if err := os.MkdirAll(filepath.Dir(stray), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stray, []byte("bytecode"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(b.ArtifactPath, "index.html"), []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(b.ArtifactPath, "ops", "__init__.py")); err != nil {
		t.Fatal(err)
	}
	_, _, err := verifiedBuildEvidence(b)
	if err == nil {
		t.Fatal("drifted artifact verified")
	}
	for _, want := range []string{"added: ops/__pycache__, ops/__pycache__/main_client.cpython-313.pyc", "removed: ops/__init__.py", "changed: index.html"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q missing %q", err, want)
		}
	}
}

func TestLegacyDigestAttestationsStillVerify(t *testing.T) {
	f, b := protectFixture(t, "static")
	legacy, err := artifactTreeDigest(b.ArtifactPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.ctx.AppDB().Exec(`INSERT INTO build_attestations(build_id,artifact_sha256,manifest_json,target_json,created_at) VALUES(?,?,?,?,?)`, b.ID, legacy, "{}", "{}", nowUTC()); err != nil {
		t.Fatal(err)
	}
	got, _, err := verifiedBuildEvidence(b)
	if err != nil || got != legacy {
		t.Fatalf("legacy digest=%s err=%v", got, err)
	}
	if _, _, err := verifiedBuildEvidence(b); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(b.ArtifactPath, "x.txt"), []byte("x"), 0o644)
	if _, _, err := verifiedBuildEvidence(b); err == nil || !strings.Contains(err.Error(), "sealed before Deploy 0.32.1") {
		t.Fatalf("legacy drift err=%v", err)
	}
}

func TestV2DigestIgnoresOnlyWriteBits(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.txt")
	_ = os.WriteFile(p, []byte("a"), 0o644)
	before, _ := artifactTreeDigestV2(dir)
	_ = os.Chmod(p, 0o444)
	same, _ := artifactTreeDigestV2(dir)
	_ = os.Chmod(p, 0o755)
	exec, _ := artifactTreeDigestV2(dir)
	if before != same || before == exec {
		t.Fatalf("v2 digest: write bits must not matter, execute bits must (%s %s %s)", before, same, exec)
	}
}

func TestWorkloadEnvDisablesPythonBytecodeByDefault(t *testing.T) {
	if !slices.Contains(workloadEnv(nil), "PYTHONDONTWRITEBYTECODE=1") {
		t.Fatal("workload env writes Python bytecode")
	}
	if !slices.Contains(workloadEnv(map[string]string{"PYTHONDONTWRITEBYTECODE": ""}), "PYTHONDONTWRITEBYTECODE=") {
		t.Fatal("workload cannot opt back in")
	}
}

func TestArtifactRunsInPlace(t *testing.T) {
	for _, c := range []struct {
		framework, start string
		want             bool
	}{{"static", "", false}, {"ios", "", false}, {"command", "", false}, {"command", "./serve", true}, {"bun", "", true}, {"go", "", true}} {
		if got := artifactRunsInPlace(c.framework, c.start); got != c.want {
			t.Errorf("%s/%q = %v", c.framework, c.start, got)
		}
	}
}

func TestCandidateDigestAcceptsNumericBuild(t *testing.T) {
	f := newExportFixture(t, nil, "")
	got, err := f.app.toolCandidateDigest(f.ctx, map[string]any{"_project_id": "p1", "id": float64(f.dep.ID), "build": float64(f.build.ID)})
	if err != nil || got.(map[string]any)["build_id"] != f.build.ID {
		t.Fatalf("numeric build=%v err=%v", got, err)
	}
	if _, err := f.app.toolCandidateDigest(f.ctx, map[string]any{"_project_id": "p1"}); err == nil || !strings.Contains(err.Error(), "a build id") {
		t.Fatalf("error should mention numeric build: %v", err)
	}
}

func TestBuildReadToolsAreMarkedReadOnly(t *testing.T) {
	m := (&App{}).Manifest()
	want := map[string]bool{"deploy_candidate_digest": true, "deploy_artifact_download": true, "deploy_build_export": true}
	for _, tool := range m.Provides.MCPTools {
		if want[tool.Name] {
			if !tool.ReadOnly {
				t.Errorf("%s is not read_only", tool.Name)
			}
			delete(want, tool.Name)
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing tools: %v", want)
	}
}
