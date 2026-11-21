package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

// A data directory written by the real create/invoke paths under project
// rt_old is restored under rt_new: the function keeps its id and name, its
// built artifact is reused, it invokes under the new project, its invocation
// policy admits the caller's new installation id (and no longer the old one),
// and environment URLs, audit identities and build_dir follow the restore.
func TestRestoreMovesFunctionsToNewProject(t *testing.T) {
	requireBin(t, "node")
	const oldBase, newBase = "https://runtime.apteva.invalid/rt_old", "https://runtime.apteva.invalid/rt_new"
	src := t.TempDir()
	// Built artifacts are read-only; t.TempDir cannot remove them alone.
	t.Cleanup(func() { _ = removeTree(src) })
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("rt_old"), tk.WithEnv("APTEVA_DATA_DIR", src))
	app := mountApp(t, ctx)
	policy := map[string]any{"authenticated_callers": []any{map[string]any{"installation_id": 42, "issuers": []string{"apteva:auth", oldBase + "/api/apps/auth/orgs/default"}}}}
	fn := createFn(t, app, ctx, map[string]any{"name": "hello", "source": `export default async (e,c)=>({hello:e?.name ?? "world", api:process.env.API_BASE, identity:c.invocation});`,
		"env": map[string]any{"API_BASE": oldBase + "/api/apps/api/gw/shop"}, "invocation_policy": policy})
	authArgs := func(pid string) map[string]any {
		return map[string]any{"id": fn.ID, "_project_id": pid, "event": map[string]any{"name": "auth"}, "deadline": time.Now().Add(20 * time.Second).UTC().Format(time.RFC3339Nano),
			"request_id": "restore-1", "principal": map[string]any{"subject": "user-1", "issuer": "apteva:auth", "project_id": pid, "function_ids": []int64{fn.ID}}}
	}
	caller := func(install int64, pid string) context.Context {
		return sdk.WithCaller(context.Background(), &sdk.Caller{AppInstallID: install, AppName: "api", ProjectID: pid})
	}
	if out, err := app.toolInvokeAuthenticated(caller(42, "rt_old"), ctx, authArgs("rt_old")); err != nil || out.(map[string]any)["status"] != "ok" {
		t.Fatalf("authenticated invoke before snapshot: %v %v", out, err)
	}
	version, err := dbGetVersion(ctx.AppDB(), "rt_old", *fn.ActiveVersionID)
	if err != nil || !strings.HasPrefix(version.BuildDir, filepath.Join(src, "functions-build")+string(os.PathSeparator)) {
		t.Fatalf("build dir before snapshot: %+v %v", version, err)
	}
	if err := app.OnUnmount(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := ctx.AppDB().Exec(`VACUUM INTO ?`, filepath.Join(src, "app.db")); err != nil {
		t.Fatal(err)
	}

	rc := sdk.RestoreContext{ID: "functions-restore", OldProjectID: "rt_old", NewProjectID: "rt_new",
		InstallIDs: map[int64]int64{42: 420}, PublicBaseURLs: map[string]string{oldBase: newBase}}
	restored, report, err := tk.RunRestore(t, &App{}, src, rc, tk.WithProjectID("rt_new"))
	if err != nil {
		t.Fatal(err)
	}
	if report.ProjectRows["functions"] != 1 || report.ProjectRows["function_versions"] != 1 || report.ProjectRows["function_invocations"] != 1 {
		t.Fatalf("report %+v", report.ProjectRows)
	}
	if gone, _ := dbGetFunction(restored, "rt_old", fn.ID, ""); gone != nil {
		t.Fatal("function still under the old project")
	}
	got, err := dbGetFunction(restored, "rt_new", 0, "hello")
	if err != nil || got == nil || got.ID != fn.ID {
		t.Fatalf("function after restore: %+v %v", got, err)
	}
	if got.Env["API_BASE"] != newBase+"/api/apps/api/gw/shop" {
		t.Fatalf("env after restore: %v", got.Env)
	}
	if callers := got.InvocationPolicy.AuthenticatedCallers; len(callers) != 1 || callers[0].InstallationID != 420 ||
		callers[0].Issuers[0] != "apteva:auth" || callers[0].Issuers[1] != newBase+"/api/apps/auth/orgs/default" {
		t.Fatalf("invocation policy after restore: %+v", got.InvocationPolicy)
	}
	invocations, err := dbListInvocations(restored, "rt_new", fn.ID, 10)
	if err != nil || len(invocations) != 1 {
		t.Fatalf("invocations after restore: %v %v", invocations, err)
	}
	var identity InvocationIdentity
	if err := json.Unmarshal(invocations[0].Identity, &identity); err != nil {
		t.Fatal(err)
	}
	if identity.ProjectID != "rt_new" || identity.CallerInstallationID != 420 || identity.Subject != "user-1" {
		t.Fatalf("audit identity after restore: %+v", identity)
	}
	dataDir := os.Getenv("APTEVA_DATA_DIR")
	t.Cleanup(func() { _ = removeTree(dataDir) })
	restoredVersion, err := dbGetVersion(restored, "rt_new", *fn.ActiveVersionID)
	if err != nil || restoredVersion.BuildDir != filepath.Join(dataDir, "functions-build", filepath.Base(filepath.Dir(version.BuildDir)), filepath.Base(version.BuildDir)) {
		t.Fatalf("build dir after restore: %q (from %q) %v", restoredVersion.BuildDir, version.BuildDir, err)
	}
	if !artifactAvailable(restoredVersion.BuildDir, restoredVersion) {
		t.Fatal("copied artifact is not reusable")
	}

	// The restored data serves under the new project.
	manifest := (&App{}).Manifest()
	newCtx := sdk.NewAppCtxForTest(&manifest, restored, nil, nil, nil)
	next := mountApp(t, newCtx)
	res, err := invokeFunction(newCtx, context.Background(), got, map[string]any{"name": "restore"}, "manual")
	if err != nil || res.Status != "ok" || !strings.Contains(res.Response, `"hello":"restore"`) || !strings.Contains(res.Response, newBase) {
		t.Fatalf("invoke after restore: %+v %v", res, err)
	}
	if out, err := next.toolInvokeAuthenticated(caller(420, "rt_new"), newCtx, authArgs("rt_new")); err != nil || out.(map[string]any)["status"] != "ok" {
		t.Fatalf("authenticated invoke after restore: %v %v", out, err)
	}
	if out, err := next.toolInvokeAuthenticated(caller(42, "rt_new"), newCtx, authArgs("rt_new")); err == nil && out.(map[string]any)["status"] == "ok" {
		t.Fatal("old installation id still admitted")
	}
}
