package main

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type exportFixture struct {
	ctx   *sdk.AppCtx
	app   *App
	dep   *Deployment
	env   *DeploymentEnvironment
	build *Build
}

func newExportFixture(t *testing.T, files map[string]string, envJSON string) exportFixture {
	t.Helper()
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"))
	t.Setenv("APTEVA_PROJECT_ID", "")
	setBootRecoveryGlobalCtx(t, ctx)
	app := &App{dataDir: writableTempDir(t), retainRollbacks: 1}
	d, env := createBootRecoveryDeployment(t, ctx)
	if envJSON != "" {
		if err := dbUpdateEnvironment(ctx.AppDB(), env.ID, map[string]any{"env_json": envJSON}); err != nil {
			t.Fatal(err)
		}
	}
	b := createBootRecoveryBuild(t, ctx, app.dataDir, d.ID, env.ID, "<html>home</html>")
	for name, body := range files {
		p := filepath.Join(b.ArtifactPath, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := sealBuildArtifact(b); err != nil {
		t.Fatal(err)
	}
	return exportFixture{ctx: ctx, app: app, dep: d, env: env, build: b}
}

func exportServer(t *testing.T, app *App) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := strings.Index(r.URL.Path, buildExportRoutePrefix)
		if i < 0 {
			http.NotFound(w, r)
			return
		}
		r.URL.Path = r.URL.Path[i:]
		app.handleBuildExport(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestBuildExportImportRoundTripKeepsDigestAndNoValues(t *testing.T) {
	f := newExportFixture(t, map[string]string{"assets/app-1a2b3c4d.js": "console.log(1)", "docs/index.html": "docs"},
		`{"STRIPE_SECRET_KEY":"sk_live_abcdefghijkl","VITE_API_URL":"https://api.example.test","PORT":"8080"}`)
	srv := exportServer(t, f.app)

	res, err := f.app.toolBuildExport(f.ctx, map[string]any{"_project_id": "p1", "build_id": float64(f.build.ID), "public_base_url": srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	out := res.(map[string]any)
	digest := out["candidate_digest"].(string)
	if len(digest) != 64 || out["archive_url"] == nil {
		t.Fatalf("export=%v", out)
	}
	rendered := mustJSON(out)
	for _, value := range []string{"sk_live_abcdefghijkl", "https://api.example.test", "8080"} {
		if strings.Contains(rendered, value) {
			t.Fatalf("export leaked env value %q: %s", value, rendered)
		}
	}
	envTemplate := out["env"].([]exportEnvVar)
	secrets := map[string]bool{}
	for _, v := range envTemplate {
		secrets[v.Key] = v.Secret
	}
	if !secrets["STRIPE_SECRET_KEY"] || secrets["VITE_API_URL"] || secrets["PORT"] {
		t.Fatalf("secret classification=%v", secrets)
	}
	files := out["static"].(map[string]any)["files"].([]exportFile)
	if len(files) < 4 {
		t.Fatalf("files=%v", files)
	}
	pins, _ := dbBuildPins(f.ctx.AppDB(), f.build.ID)
	if len(pins) != 1 || pins[0].Reason != pinReasonExport {
		t.Fatalf("export pin=%v", pins)
	}

	// Import into a second static deployment, as a runtime's Deploy would.
	target, err := dbCreateDeployment(f.ctx.AppDB(), "p1", CreateDeploymentInput{Name: "runtime-site", SourceKind: "local", SourceRef: "/src", Framework: "static"})
	if err != nil {
		t.Fatal(err)
	}
	importer := &App{dataDir: writableTempDir(t)}
	bad, err := importer.toolBuildImport(f.ctx, map[string]any{"_project_id": "p1", "id": float64(target.ID),
		"archive_url": out["archive_url"], "candidate_digest": strings.Repeat("0", 64)})
	if err != nil {
		t.Fatal(err)
	}
	if b := bad.(map[string]any)["build"].(*Build); b.Status != "failed" || !strings.Contains(b.Error, "does not match") {
		t.Fatalf("wrong-digest import=%+v", b)
	}
	imported, err := importer.toolBuildImport(f.ctx, map[string]any{"_project_id": "p1", "id": float64(target.ID),
		"archive_url": out["archive_url"], "candidate_digest": digest})
	if err != nil {
		t.Fatal(err)
	}
	ib := imported.(map[string]any)["build"].(*Build)
	if ib.Status != "succeeded" || ib.BuildBackend != buildBackendImport {
		t.Fatalf("import=%+v", ib)
	}
	got, _, err := verifiedBuildEvidence(ib)
	if err != nil || got != digest {
		t.Fatalf("imported digest=%s err=%v want %s", got, err, digest)
	}
	again, err := importer.toolBuildImport(f.ctx, map[string]any{"_project_id": "p1", "id": float64(target.ID),
		"archive_url": out["archive_url"], "candidate_digest": digest})
	if err != nil || again.(map[string]any)["already_imported"] != true {
		t.Fatalf("re-import=%v err=%v", again, err)
	}

	// Tampered signatures and expired links are refused.
	tampered := strings.Replace(out["archive_url"].(string), "sig=", "sig=00", 1)
	if resp, err := http.Get(tampered); err != nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("tampered status=%v err=%v", resp, err)
	}
}

func TestBuildExportRefusesSecretBakedIntoArtifact(t *testing.T) {
	f := newExportFixture(t, map[string]string{"assets/config.js": `window.k="sk_live_abcdefghijkl"`},
		`{"STRIPE_SECRET_KEY":"sk_live_abcdefghijkl"}`)
	_, err := f.app.toolBuildExport(f.ctx, map[string]any{"_project_id": "p1", "build_id": float64(f.build.ID)})
	if err == nil || !strings.Contains(err.Error(), "assets/config.js") {
		t.Fatalf("err=%v", err)
	}
}

func TestSecretScanFindsValuesAcrossReadChunks(t *testing.T) {
	dir := t.TempDir()
	secret := []byte("split-secret-value")
	body := make([]byte, 256<<10-5)
	for i := range body {
		body[i] = 'a'
	}
	body = append(body, secret...)
	p := filepath.Join(dir, "big.js")
	if err := os.WriteFile(p, body, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, leaked, err := hashAndScanFile(p, [][]byte{secret}, len(secret)); err != nil || !leaked {
		t.Fatalf("leaked=%v err=%v", leaked, err)
	}
}

func TestCandidateDigestResolvesByDigestAndLatest(t *testing.T) {
	f := newExportFixture(t, nil, "")
	newer := createBootRecoveryBuild(t, f.ctx, f.app.dataDir, f.dep.ID, f.env.ID, "newer")
	if err := sealBuildArtifact(newer); err != nil {
		t.Fatal(err)
	}
	first, err := f.app.toolCandidateDigest(f.ctx, map[string]any{"_project_id": "p1", "build_id": float64(f.build.ID)})
	if err != nil {
		t.Fatal(err)
	}
	digest := first.(map[string]any)["candidate_digest"].(string)
	byDigest, err := f.app.toolCandidateDigest(f.ctx, map[string]any{"_project_id": "p1", "candidate_digest": digest})
	if err != nil || byDigest.(map[string]any)["build_id"] != f.build.ID {
		t.Fatalf("by digest=%v err=%v", byDigest, err)
	}
	latest, err := f.app.toolCandidateDigest(f.ctx, map[string]any{"_project_id": "p1", "id": float64(f.dep.ID), "build": "latest_succeeded"})
	if err != nil || latest.(map[string]any)["build_id"] != newer.ID {
		t.Fatalf("latest=%v err=%v", latest, err)
	}
	if _, err := f.app.toolCandidateDigest(f.ctx, map[string]any{"_project_id": "other", "candidate_digest": digest}); err == nil {
		t.Fatal("digest resolved across projects")
	}
}

func TestRetentionKeepsPinnedBuildsUntilExpiry(t *testing.T) {
	f := newExportFixture(t, nil, "")
	var builds []*Build
	for _, m := range []string{"b", "c"} {
		builds = append(builds, createBootRecoveryBuild(t, f.ctx, f.app.dataDir, f.dep.ID, f.env.ID, m))
	}
	db := f.ctx.AppDB()
	keep := func() map[int64]bool {
		ids, err := retainedBuildIDs(db, 1)
		if err != nil {
			t.Fatal(err)
		}
		return ids
	}
	if keep()[f.build.ID] {
		t.Fatal("old build kept without a pin")
	}
	if _, err := f.app.toolBuildPin(f.ctx, map[string]any{"_project_id": "p1", "build_id": float64(f.build.ID)}); err != nil {
		t.Fatal(err)
	}
	if err := pinBuild(db, builds[0].ID, pinReasonTests, time.Now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	ids := keep()
	if !ids[f.build.ID] || ids[builds[0].ID] {
		t.Fatalf("keep=%v", ids)
	}
	// Re-pinning never shortens an expiry; manual unpin releases it.
	_ = pinBuild(db, builds[0].ID, pinReasonTests, time.Now().Add(time.Hour))
	_ = pinBuild(db, builds[0].ID, pinReasonTests, time.Now().Add(-time.Hour))
	if !keep()[builds[0].ID] {
		t.Fatal("re-pin shortened expiry")
	}
	if _, err := f.app.toolBuildPin(f.ctx, map[string]any{"_project_id": "p1", "build_id": float64(f.build.ID), "pinned": false}); err != nil {
		t.Fatal(err)
	}
	if keep()[f.build.ID] {
		t.Fatal("unpinned build still kept")
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM build_pins`).Scan(&n)
	if n == 0 {
		t.Fatal(sql.ErrNoRows)
	}
}

func TestStaticHandlerServesUnderBasePath(t *testing.T) {
	root := t.TempDir()
	for name, body := range map[string]string{"index.html": "home", "docs/index.html": "docs", "assets/app-1a2b3c4d.js": "js"} {
		p := filepath.Join(root, filepath.FromSlash(name))
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	h := staticHandlerWithBase(root, "", "/apps/site")
	cases := []struct {
		path, location, body string
		code                 int
	}{
		{"/apps/site", "/apps/site/", "", http.StatusMovedPermanently},
		{"/apps/site/", "", "home", http.StatusOK},
		{"/apps/site/docs", "/apps/site/docs/", "", http.StatusMovedPermanently},
		{"/apps/site/docs/", "", "docs", http.StatusOK},
		{"/apps/site/some/route", "", "home", http.StatusOK},
		{"/apps/site/assets/app-1a2b3c4d.js", "", "js", http.StatusOK},
		{"/apps/site/assets/missing.js", "", "", http.StatusNotFound},
		{"/", "", "", http.StatusNotFound},
		{"/apps/sitex/", "", "", http.StatusNotFound},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest(http.MethodGet, c.path, nil))
		if rec.Code != c.code || rec.Header().Get("Location") != c.location || (c.body != "" && rec.Body.String() != c.body) {
			t.Errorf("%s: code=%d location=%q body=%q", c.path, rec.Code, rec.Header().Get("Location"), rec.Body.String())
		}
	}
	plain := httptest.NewRecorder()
	staticHandlerWithBase(root, "", "")(plain, httptest.NewRequest(http.MethodGet, "/assets/app-1a2b3c4d.js", nil))
	based := httptest.NewRecorder()
	h(based, httptest.NewRequest(http.MethodGet, "/apps/site/assets/app-1a2b3c4d.js", nil))
	if plain.Header().Get("Cache-Control") != based.Header().Get("Cache-Control") || plain.Header().Get("Content-Type") != based.Header().Get("Content-Type") {
		t.Fatalf("headers differ: %v vs %v", plain.Header(), based.Header())
	}
	if err := validateStaticBasePath("/apps/../x"); err == nil {
		t.Fatal("accepted .. base path")
	}
	if err := validateStaticBasePath("/apps/site/"); err == nil {
		t.Fatal("accepted trailing slash")
	}
}

func TestTestsGatePinsCandidateWhileVerdictPending(t *testing.T) {
	platform := &releaseTestsPlatform{state: "missing"}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"), tk.WithPlatform(platform))
	setBootRecoveryGlobalCtx(t, ctx)
	dataDir := writableTempDir(t)
	d, env := createBootRecoveryDeployment(t, ctx)
	b := createBootRecoveryBuild(t, ctx, dataDir, d.ID, env.ID, "candidate")
	if err := sealBuildArtifact(b); err != nil {
		t.Fatal(err)
	}
	gate := &testsReleaseGate{SuiteID: 5, SuiteRevision: strings.Repeat("a", 64), FixtureRevision: strings.Repeat("b", 64)}
	target := effectiveDeploymentForEnvironment(d, env)
	if err := checkTestsGate(target, b, gate, true); err == nil || platform.queued != 1 {
		t.Fatalf("gate err=%v queued=%d", err, platform.queued)
	}
	pins, _ := dbBuildPins(ctx.AppDB(), b.ID)
	if len(pins) != 1 || pins[0].Reason != pinReasonTests {
		t.Fatalf("pins=%v", pins)
	}
	platform.state = "failed"
	_ = unpinBuild(ctx.AppDB(), b.ID, pinReasonTests)
	if err := checkTestsGate(target, b, gate, true); err == nil {
		t.Fatal("failed verdict allowed release")
	}
	if pins, _ = dbBuildPins(ctx.AppDB(), b.ID); len(pins) != 0 {
		t.Fatalf("failed verdict pinned build: %v", pins)
	}
}
