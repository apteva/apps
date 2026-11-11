package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

func TestBrowserTestCallerOwnershipAndReservation(t *testing.T) {
	app := tk.NewAppCtx(t, "apteva.yaml").WithProject("one")
	for _, caller := range []*sdk.Caller{nil, {AppName: "other", AppInstallID: 1, ProjectID: "one"}, {AppName: "tests", AppInstallID: 0, ProjectID: "one"}, {AppName: "tests", AppInstallID: 1, ProjectID: "two"}} {
		call := context.Background()
		if caller != nil {
			call = sdk.WithCaller(call, caller)
		}
		if _, _, err := testBrowserOwner(call, app); err == nil {
			t.Fatalf("accepted caller %+v", caller)
		}
	}
	call := sdk.WithCaller(context.Background(), &sdk.Caller{AppName: "tests", AppInstallID: 1, ProjectID: "one"})
	if _, _, err := testBrowserOwner(call, app); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DB_PATH", filepath.Join(t.TempDir(), "computer.db"))
	a := &App{}
	if err := a.recoverBrowserTests(app); err != nil {
		t.Fatal(err)
	}
	_, err := app.AppDB().Exec(`INSERT INTO computer_test_sessions(id,project_id,owner_install_id,state,profile,expires_at,created_at) VALUES('bt_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','two',2,'starting','unused','future','now')`)
	if err != nil {
		t.Fatal(err)
	}
	out, err := a.browserTestSession(call, app, map[string]any{"id": "bt_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "action": "open", "ttl_seconds": 30, "options": map[string]any{"width": 1280, "height": 720, "policy": map[string]any{"runtime_id": "rt_test", "targets": map[string]string{"frontend": "http://127.0.0.1:5280/api/environment-app-public/rt_test/api/apps/site/_install/2/ui"}}}})
	if err != nil || out.(map[string]any)["state"] != "busy" {
		t.Fatal(out, err)
	}
	if _, err = a.closeBrowserTest(app, "bt_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 1, "one"); err != nil {
		t.Fatal(err)
	}
	var state string
	if err = app.AppDB().QueryRow(`SELECT state FROM computer_test_sessions`).Scan(&state); err != nil || state != "starting" {
		t.Fatal("another owner's lease changed", state, err)
	}
	entries, err := os.ReadDir(testProfileRoot())
	if err != nil || len(entries) != 0 {
		t.Fatal("busy reservation leaked a profile", entries, err)
	}
}

func TestBrowserTestRecoveryRestrictsOwnedProfiles(t *testing.T) {
	app := tk.NewAppCtx(t, "apteva.yaml")
	t.Setenv("DB_PATH", filepath.Join(t.TempDir(), "computer.db"))
	if err := os.MkdirAll(testProfileRoot(), 0700); err != nil {
		t.Fatal(err)
	}
	profile, err := os.MkdirTemp(testProfileRoot(), "profile-")
	if err != nil {
		t.Fatal(err)
	}
	_, err = app.AppDB().Exec(`INSERT INTO computer_test_sessions(id,project_id,owner_install_id,state,profile,expires_at,created_at) VALUES('bt_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','one',1,'active',?,'future','now')`, profile)
	if err != nil {
		t.Fatal(err)
	}
	a := &App{}
	if err = a.recoverBrowserTests(app); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(profile); !os.IsNotExist(err) {
		t.Fatal("orphan profile survived", err)
	}
	var state string
	if err = app.AppDB().QueryRow(`SELECT state FROM computer_test_sessions`).Scan(&state); err != nil || state != "interrupted" {
		t.Fatal(state, err)
	}
	if err = stopOrphanBrowser(filepath.Dir(testProfileRoot())); err == nil {
		t.Fatal("unowned profile accepted")
	}
}
