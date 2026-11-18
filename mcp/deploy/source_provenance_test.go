package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

func writeSourceFixture(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		mode := os.FileMode(0o644)
		if strings.HasSuffix(name, ".sh") {
			mode = 0o755
		}
		if err := os.WriteFile(p, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// workspacesDigestOfArchive re-implements the Workspaces test-source digest
// (mcp/workspaces inspectTestSource) over a tar.gz, independently of Deploy's
// directory walker.
func workspacesDigestOfArchive(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	lines := map[string]string{}
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		var kind, mode, sum string
		switch h.Typeflag {
		case tar.TypeReg:
			d := sha256.New()
			_, _ = io.Copy(d, tr)
			kind, mode, sum = "f", "644", hex.EncodeToString(d.Sum(nil))
			if h.Mode&0o111 != 0 {
				mode = "755"
			}
		case tar.TypeSymlink:
			s := sha256.Sum256([]byte(h.Linkname))
			kind, mode, sum = "l", "777", hex.EncodeToString(s[:])
		default:
			continue
		}
		if strings.Contains("/"+h.Name+"/", "/node_modules/") {
			continue
		}
		lines[h.Name] = h.Name + "\x00" + kind + "\x00" + mode + "\x00" + sum + "\n"
	}
	keys := make([]string, 0, len(lines))
	for k := range lines {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	d := sha256.New()
	for _, k := range keys {
		io.WriteString(d, lines[k])
	}
	return "sha256:" + hex.EncodeToString(d.Sum(nil))
}

func TestSourceDigestMatchesWorkspacesMethod(t *testing.T) {
	files := map[string]string{
		"package.json":                     `{"name":"site"}`,
		"bun.lock":                         "lock-root",
		"apps/web/package-lock.json":       "lock-web",
		"apps/web/src/index.ts":            "export {}",
		"scripts/build.sh":                 "#!/bin/sh\n",
		"node_modules/left-pad/index.js":   "ignored",
		"apps/web/node_modules/x/index.js": "ignored",
		".git/HEAD":                        "ref: refs/heads/main",
	}
	root := writeSourceFixture(t, files)
	if err := os.Symlink("apps/web/src/index.ts", filepath.Join(root, "entry.ts")); err != nil {
		t.Fatal(err)
	}
	digest, count, locks, err := sourceTreeDigest(root)
	if err != nil {
		t.Fatal(err)
	}
	if count != 6 {
		t.Fatalf("file count %d, want 6 (node_modules and .git excluded)", count)
	}
	if len(locks) != 2 || locks["bun.lock"] == "" || locks["apps/web/package-lock.json"] == "" {
		t.Fatalf("lockfiles per folder: %v", locks)
	}
	entries, _ := walkSourceTree(root)
	archive := filepath.Join(t.TempDir(), "src.tar.gz")
	sum1, _, err := writeSourceArchive(entries, archive, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := workspacesDigestOfArchive(t, archive); got != digest {
		t.Fatalf("Workspaces digest of the kept archive %s != recorded %s", got, digest)
	}
	// The archive is deterministic.
	again := filepath.Join(t.TempDir(), "src.tar.gz")
	if sum2, _, _ := writeSourceArchive(entries, again, 0); sum2 != sum1 {
		t.Fatal("source archive is not deterministic")
	}

	// Identical source elsewhere: same digest. Edit or exec bit: different.
	other := writeSourceFixture(t, files)
	_ = os.Symlink("apps/web/src/index.ts", filepath.Join(other, "entry.ts"))
	if d2, _, _, _ := sourceTreeDigest(other); d2 != digest {
		t.Fatal("identical sources produced different digests")
	}
	_ = os.Chmod(filepath.Join(other, "package.json"), 0o444)
	if d2, _, _, _ := sourceTreeDigest(other); d2 != digest {
		t.Fatal("write bits changed the source digest")
	}
	_ = os.WriteFile(filepath.Join(other, "node_modules", "left-pad", "index.js"), []byte("changed"), 0o644)
	if d2, _, _, _ := sourceTreeDigest(other); d2 != digest {
		t.Fatal("node_modules changed the source digest")
	}
	_ = os.Chmod(filepath.Join(other, "package.json"), 0o755)
	if d2, _, _, _ := sourceTreeDigest(other); d2 == digest {
		t.Fatal("exec bit did not change the source digest")
	}
	_ = os.Chmod(filepath.Join(other, "package.json"), 0o644)
	_ = os.WriteFile(filepath.Join(other, "apps", "web", "src", "index.ts"), []byte("export const x = 1"), 0o644)
	if d2, _, _, _ := sourceTreeDigest(other); d2 == digest {
		t.Fatal("edited source kept the same digest")
	}
}

func TestSourceDigestKnownVector(t *testing.T) {
	root := writeSourceFixture(t, map[string]string{"a.txt": "a"})
	digest, _, _, err := sourceTreeDigest(root)
	if err != nil {
		t.Fatal(err)
	}
	fileSum := sha256.Sum256([]byte("a"))
	line := "a.txt\x00f\x00644\x00" + hex.EncodeToString(fileSum[:]) + "\n"
	want := sha256.Sum256([]byte(line))
	if digest != "sha256:"+hex.EncodeToString(want[:]) {
		t.Fatalf("digest %s", digest)
	}
}

func TestBuildSourceIsRecordedOnceAndFrozenBySeal(t *testing.T) {
	f := newExportFixture(t, nil, "")
	db := f.ctx.AppDB()
	b := createBootRecoveryBuild(t, f.ctx, f.app.dataDir, f.dep.ID, f.env.ID, "<html>v2</html>")
	root := writeSourceFixture(t, map[string]string{"index.html": "<html>v2</html>", "bun.lock": "x"})
	d := *f.dep
	d.SourceKind = "local"
	src, err := captureBuildSource(db, &d, b.ID, f.app.buildDir(b.ID), root)
	if err != nil {
		t.Fatal(err)
	}
	if src.ArchiveSHA256 == "" || src.ArchiveNote != "" {
		t.Fatalf("local source copy not kept: %+v", src)
	}
	got, err := dbBuildSource(db, b.ID)
	if err != nil || got == nil || got.SourceDigest != src.SourceDigest || got.Lockfiles["bun.lock"] == "" {
		t.Fatalf("stored source %+v %v", got, err)
	}
	if err := sealBuildArtifact(b); err != nil {
		t.Fatal(err)
	}
	if _, err := captureBuildSource(db, &d, b.ID, f.app.buildDir(b.ID), root); err == nil || !strings.Contains(err.Error(), "after the build is sealed") {
		t.Fatalf("source changed after seal: %v", err)
	}

	// deploy_candidate_digest and deploy_build_export carry the source.
	out, err := f.app.toolCandidateDigest(f.ctx, map[string]any{"_project_id": "p1", "build_id": float64(b.ID)})
	if err != nil || out.(map[string]any)["source_digest"] != src.SourceDigest {
		t.Fatalf("candidate digest output %v %v", out, err)
	}

	// deploy_build_source returns a signed copy whose bytes match the record.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := strings.Index(r.URL.Path, sourceArchiveRoutePath)
		if i < 0 {
			http.NotFound(w, r)
			return
		}
		r.URL.Path = r.URL.Path[i:]
		f.app.handleSourceArchive(w, r)
	}))
	defer srv.Close()
	res, err := f.app.toolBuildSource(f.ctx, map[string]any{"_project_id": "p1", "build_id": float64(b.ID), "public_base_url": srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	archive := res.(map[string]any)["archive"].(map[string]any)
	resp, err := http.Get(archive["url"].(string))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	sum := sha256.Sum256(body)
	if resp.StatusCode != 200 || hex.EncodeToString(sum[:]) != src.ArchiveSHA256 {
		t.Fatalf("source download %d", resp.StatusCode)
	}
	tampered := strings.Replace(archive["url"].(string), "sha256=", "sha256=0", 1)
	if resp, _ := http.Get(tampered); resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatal("tampered source URL was served")
	}
	// Retention removes the copy with the build.
	paths := f.app.prunableArtifactPaths(*b)
	found := false
	for _, p := range paths {
		found = found || p == f.app.sourceArchivePath(b.ID)
	}
	if !found {
		t.Fatal("source copy does not follow build retention")
	}
}

func TestCodeSourceRecordsSnapshotWithoutCopy(t *testing.T) {
	f := newExportFixture(t, nil, "")
	b := createBootRecoveryBuild(t, f.ctx, f.app.dataDir, f.dep.ID, f.env.ID, "x")
	buildDir := f.app.buildDir(b.ID)
	_ = os.MkdirAll(buildDir, 0o755)
	receipt, _ := json.Marshal(sourceReceipt{SnapshotID: "snap_42", SourceRevision: "abc123", Repository: "site"})
	_ = os.WriteFile(filepath.Join(buildDir, "source-receipt.json"), receipt, 0o644)
	d := *f.dep
	d.SourceKind = "code"
	src, err := captureBuildSource(f.ctx.AppDB(), &d, b.ID, buildDir, writeSourceFixture(t, map[string]string{"a": "1"}))
	if err != nil {
		t.Fatal(err)
	}
	if src.SnapshotID != "snap_42" || src.SourceRevision != "abc123" || src.ArchiveSHA256 != "" || pathExists(f.app.sourceArchivePath(b.ID)) {
		t.Fatalf("code source %+v", src)
	}
}

// ─── Gate: source digest, required flag, override ─────────────────

type sourceTestsPlatform struct {
	sdk.PlatformClient
	sdk.AppContextClient
	state      string
	tested     string
	source     string
	sentSource any
}

func (p *sourceTestsPlatform) WhoAmI() (*sdk.InstallIdentity, error) {
	return &sdk.InstallIdentity{}, nil
}

func (p *sourceTestsPlatform) CallAppResultContext(_ context.Context, app, tool string, args map[string]any, out any) error {
	if tool == "tests_run" {
		return json.Unmarshal([]byte(`{"run":{"id":19}}`), out)
	}
	p.sentSource = args["source_digest"]
	release := map[string]any{
		"candidate_digest": p.tested, "observed_digest": p.tested,
		"suite_revision": args["suite_revision"], "fixture_revision": args["fixture_revision"],
	}
	if p.source != "" {
		release["source_digest"] = p.source
	}
	return json.Unmarshal([]byte(mustJSON(map[string]any{"allowed": p.state == "passed", "state": p.state, "run_id": 19, "release": release})), out)
}

func TestRequiredGateOverrideAndSourceDigest(t *testing.T) {
	platform := &sourceTestsPlatform{state: "missing"}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"), tk.WithPlatform(platform))
	setBootRecoveryGlobalCtx(t, ctx)
	runtime := &auditFastRuntime{}
	a := &App{dataDir: writableTempDir(t), runtime: runtime, registry: NewSupervisorRegistry(), portRangeStart: availableTestPort(t), retainRollbacks: 3}
	a.portRangeEnd = a.portRangeStart
	runtime.app = a
	d, env := createBootRecoveryDeployment(t, ctx)
	gate := &testsReleaseGate{SuiteID: 5, SuiteRevision: strings.Repeat("a", 64), FixtureRevision: strings.Repeat("b", 64), Required: true}
	if err := dbUpdateEnvironment(ctx.AppDB(), env.ID, map[string]any{"target_config_json": mustJSON(genericTargetConfig{TestsGate: gate})}); err != nil {
		t.Fatal(err)
	}
	env, _ = dbGetEnvironment(ctx.AppDB(), env.ID)
	target := effectiveDeploymentForEnvironment(d, env)
	build := createBootRecoveryBuild(t, ctx, a.dataDir, d.ID, env.ID, "good")
	local := *d
	local.SourceKind = "local"
	src, err := captureBuildSource(ctx.AppDB(), &local, build.ID, a.buildDir(build.ID), writeSourceFixture(t, map[string]string{"index.html": "good"}))
	if err != nil {
		t.Fatal(err)
	}
	if err := sealBuildArtifact(build); err != nil {
		t.Fatal(err)
	}
	digest, _, _ := verifiedBuildEvidence(build)
	platform.tested = digest

	// Required gate: no verdict, no release.
	if _, err := a.runRelease(target, build); err == nil || !strings.Contains(err.Error(), "tests_override_reason") {
		t.Fatalf("release without verdict: %v", err)
	}
	if platform.sentSource != src.SourceDigest {
		t.Fatalf("source digest not sent to Tests: %v", platform.sentSource)
	}
	// A verdict for different source blocks even with a matching artifact.
	platform.state, platform.source = "passed", "sha256:"+strings.Repeat("0", 64)
	if _, err := a.runRelease(target, build); err == nil || !strings.Contains(err.Error(), "verified source") {
		t.Fatalf("verdict for other source: %v", err)
	}
	// Override needs a real reason.
	platform.state, platform.source = "failed", ""
	if _, err := a.runReleaseWithOptions(target, build, releaseOptions{TestsOverrideReason: "urgent"}); err == nil || len(runtime.started()) != 0 {
		t.Fatalf("short override reason accepted: %v", err)
	}
	rel, err := a.runReleaseWithOptions(target, build, releaseOptions{TestsOverrideReason: "checkout outage, hotfix INC-12"})
	if err != nil || rel.Status != "live" {
		t.Fatalf("override release %+v %v", rel, err)
	}
	if o := priorTestsOverride(env.ID, build.ID); o == nil || o.Reason != "checkout outage, hotfix INC-12" || !strings.Contains(o.BlockedBy, "failed") {
		t.Fatalf("override not recorded on the release: %+v", o)
	}
	// With a passing, matching verdict the release goes ahead without one.
	platform.state, platform.source = "passed", src.SourceDigest
	if _, err := checkTestsGateOrOverride(target, build, gate, false, ""); err != nil {
		t.Fatalf("passing verdict: %v", err)
	}

	// Removing a required gate needs a reason.
	if err := dbUpdateEnvironment(ctx.AppDB(), env.ID, map[string]any{"target_config_json": "{}"}); err == nil {
		t.Fatal("required gate removed without a reason")
	}
	weaker := *gate
	weaker.Required = false
	if err := dbUpdateEnvironment(ctx.AppDB(), env.ID, map[string]any{"target_config_json": mustJSON(genericTargetConfig{TestsGate: &weaker})}); err == nil {
		t.Fatal("required turned off without a reason")
	}
	bumped := *gate
	bumped.SuiteRevision = strings.Repeat("c", 64)
	if err := dbUpdateEnvironment(ctx.AppDB(), env.ID, map[string]any{"target_config_json": mustJSON(genericTargetConfig{TestsGate: &bumped})}); err != nil {
		t.Fatalf("revision bump refused: %v", err)
	}
	if err := dbUpdateEnvironment(ctx.AppDB(), env.ID, map[string]any{"target_config_json": "{}", "tests_gate_change_reason": "suite retired, replaced by 9"}); err != nil {
		t.Fatalf("removal with reason refused: %v", err)
	}
}
