package main

import (
	"context"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

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
