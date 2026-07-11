package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

func TestPackageLifecycleScriptCannotSeeSidecarToken(t *testing.T) {
	requireBin(t, "npm")
	t.Setenv("APTEVA_APP_TOKEN", "top-secret-sidecar-token")
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	app := mountApp(t, ctx)
	fn := createFn(t, app, ctx, map[string]any{
		"name": "script-scrub", "source": `export default async () => "ok";`,
		"package_json": `{"name":"script-scrub","version":"1.0.0","scripts":{"preinstall":"node -e \"require('fs').writeFileSync('observed-env', process.env.APTEVA_APP_TOKEN || 'clean')\""}}`,
	})
	ver, err := dbGetVersion(ctx.AppDB(), testProj, *fn.ActiveVersionID)
	if err != nil || ver == nil {
		t.Fatalf("get version: %v", err)
	}
	observed, err := os.ReadFile(filepath.Join(ver.BuildDir, "observed-env"))
	if err != nil {
		t.Fatalf("read lifecycle output: %v", err)
	}
	if string(observed) != "clean" {
		t.Fatalf("package lifecycle script saw sidecar credential: %q", observed)
	}
}

// TestDeployBumpsVersion: functions_deploy creates v2, makes it
// active, and the next invoke runs the new code.
func TestDeployBumpsVersion(t *testing.T) {
	requireBin(t, "node")
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	app := mountApp(t, ctx)

	fn := createFn(t, app, ctx, map[string]any{
		"name": "ver", "source": `export default async () => "v1";`,
	})
	if fn.ActiveVersionID == nil {
		t.Fatal("v1 not active after create")
	}
	v1Active := *fn.ActiveVersionID

	out, err := app.toolDeploy(ctx, map[string]any{
		"name": "ver", "source": `export default async () => "v2";`,
	})
	if err != nil {
		t.Fatalf("deploy: %v", err)
	}
	ver := out.(map[string]any)["version"].(*FunctionVersion)
	if ver.Version != 2 {
		t.Errorf("version = %d, want 2", ver.Version)
	}
	fn2 := out.(map[string]any)["function"].(*Function)
	if fn2.ActiveVersionID == nil || *fn2.ActiveVersionID == v1Active {
		t.Fatalf("active version not advanced: %v", fn2.ActiveVersionID)
	}

	res, err := invokeFunction(ctx, context.Background(), fn2, nil, "manual")
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if res.Response != `"v2"` {
		t.Errorf("response = %q, want \"v2\"", res.Response)
	}
}

// TestRollback: after deploying v2, functions_rollback to v1 makes
// the next invoke run v1 again.
func TestRollback(t *testing.T) {
	requireBin(t, "node")
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	app := mountApp(t, ctx)

	createFn(t, app, ctx, map[string]any{"name": "rb", "source": `export default async () => "v1";`})
	if _, err := app.toolDeploy(ctx, map[string]any{"name": "rb", "source": `export default async () => "v2";`}); err != nil {
		t.Fatalf("deploy v2: %v", err)
	}

	out, err := app.toolRollback(ctx, map[string]any{"name": "rb", "version": 1})
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}
	fn := out.(map[string]any)["function"].(*Function)
	res, err := invokeFunction(ctx, context.Background(), fn, nil, "manual")
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if res.Response != `"v1"` {
		t.Errorf("after rollback response = %q, want \"v1\"", res.Response)
	}
}

// TestVersionsList: functions_versions returns the deploy history,
// newest first, all built.
func TestVersionsList(t *testing.T) {
	requireBin(t, "node")
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	app := mountApp(t, ctx)

	createFn(t, app, ctx, map[string]any{"name": "vl", "source": echoHandler})
	if _, err := app.toolDeploy(ctx, map[string]any{"name": "vl", "source": `export default async () => 2;`}); err != nil {
		t.Fatalf("deploy: %v", err)
	}

	out, err := app.toolVersions(ctx, map[string]any{"name": "vl"})
	if err != nil {
		t.Fatalf("versions: %v", err)
	}
	vers := out.(map[string]any)["versions"].([]*FunctionVersion)
	if len(vers) != 2 {
		t.Fatalf("versions = %d, want 2", len(vers))
	}
	if vers[0].Version != 2 || vers[1].Version != 1 {
		t.Errorf("version order = %d,%d want 2,1", vers[0].Version, vers[1].Version)
	}
	for _, v := range vers {
		if v.BuildStatus != "ready" {
			t.Errorf("v%d build_status = %q, want ready", v.Version, v.BuildStatus)
		}
	}
}

// TestRedeployCarriesPackageJSON: a source-only redeploy inherits the
// prior active version's package_json (omitting the field is not the
// same as clearing deps), while an explicit "" clears it.
func TestRedeployCarriesPackageJSON(t *testing.T) {
	requireBin(t, "node")
	requireBin(t, "npm")
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	app := mountApp(t, ctx)

	const pkg = `{"name":"carry","version":"1.0.0","dependencies":{}}`
	createFn(t, app, ctx, map[string]any{
		"name": "carry", "source": echoHandler, "package_json": pkg,
	})

	// v2: source only, no package_json key -> inherit prior.
	out, err := app.toolDeploy(ctx, map[string]any{
		"name": "carry", "source": `export default async () => 2;`,
	})
	if err != nil {
		t.Fatalf("deploy v2: %v", err)
	}
	if v2 := out.(map[string]any)["version"].(*FunctionVersion); v2.PackageJSON != pkg {
		t.Errorf("v2 package_json = %q, want carried-forward %q", v2.PackageJSON, pkg)
	}

	// v3: explicit empty string -> clear.
	out, err = app.toolDeploy(ctx, map[string]any{
		"name": "carry", "source": `export default async () => 3;`, "package_json": "",
	})
	if err != nil {
		t.Fatalf("deploy v3: %v", err)
	}
	if v3 := out.(map[string]any)["version"].(*FunctionVersion); v3.PackageJSON != "" {
		t.Errorf("v3 package_json = %q, want cleared", v3.PackageJSON)
	}
}

// TestDeployWithPackageJSON: a function shipping a package.json gets
// `npm install` run once at deploy, and then invokes normally. Uses
// an empty dependency set so the install is offline + fast.
func TestDeployWithPackageJSON(t *testing.T) {
	requireBin(t, "node")
	requireBin(t, "npm")
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	app := mountApp(t, ctx)

	fn := createFn(t, app, ctx, map[string]any{
		"name":         "deps",
		"source":       echoHandler,
		"package_json": `{"name":"deps-fn","version":"1.0.0","dependencies":{}}`,
	})
	res, err := invokeFunction(ctx, context.Background(), fn, map[string]any{"ok": true}, "manual")
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if res.Status != "ok" {
		t.Fatalf("status = %q, want ok (err=%q stderr=%q)", res.Status, res.Error, res.Stderr)
	}
}
