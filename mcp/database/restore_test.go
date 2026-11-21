package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
	"github.com/apteva/app-sdk/testkit"
	"github.com/apteva/apps/mcp/database/engine"
)

func TestRestoredScope(t *testing.T) {
	rc := sdk.RestoreContext{ID: "r", OldProjectID: "rt_old", NewProjectID: "rt_new", InstallIDs: map[int64]int64{5: 7, 7: 9}}
	for raw, want := range map[string]string{
		`["rt_old","5"]`: `["rt_new","7"]`,
		`["rt_old","7"]`: `["rt_new","9"]`,
		`["rt_old",""]`:  `["rt_new",""]`,
		`["rt_old","3"]`: `["rt_new","3"]`,
		`["other","5"]`:  `["other","5"]`,
		`not json`:       `not json`,
	} {
		if got, _ := restoredScope(raw, rc); got != want {
			t.Errorf("%s → %s, want %s", raw, got, want)
		}
	}
}

// A data directory written by the real tools under one project and caller is
// readable by the same callers under their new ids after the restore: the
// database keeps its name, collections and records, an app's private
// database follows the app's new install id, an access grant still applies,
// and another project kept in the same install is untouched.
func TestOnRestoreMovesDatabasesToNewIDs(t *testing.T) {
	root := t.TempDir()
	m, err := engine.Open(filepath.Join(root, "database"))
	if err != nil {
		t.Fatal(err)
	}
	a := &App{manager: m}
	operator := &sdk.Caller{ProjectID: "rt_old", AgentID: 1, DefaultEffect: "allow"}
	functions := &sdk.Caller{ProjectID: "rt_old", AppInstallID: 5, AppName: "functions", DefaultEffect: "allow"}
	api := &sdk.Caller{ProjectID: "rt_old", AppInstallID: 7, AppName: "api", DefaultEffect: "allow"}
	otherProject := &sdk.Caller{ProjectID: "keep", AppInstallID: 5, AppName: "functions", DefaultEffect: "allow"}
	must := func(v any, e error) any {
		t.Helper()
		if e != nil {
			t.Fatal(e)
		}
		return v
	}
	orders := map[string]any{"database": "shop", "collection": "orders"}
	seed := func(c *sdk.Caller) {
		must(invoke(a, c, "database_create", map[string]any{"database": "shop"}))
		must(invoke(a, c, "collection_create", map[string]any{"database": "shop", "collection": "orders", "fields": []any{map[string]any{"name": "total", "type": "number"}}}))
		must(invoke(a, c, "insert", map[string]any{"database": "shop", "collection": "orders", "records": []any{map[string]any{"id": "o1", "total": 5}}}))
	}
	seed(functions)
	seed(otherProject)
	seed(operator)
	must(invoke(a, operator, "collection_create", map[string]any{"database": "shop", "collection": "customers", "fields": []any{map[string]any{"name": "name", "type": "text"}}}))
	must(invoke(a, operator, "access_grant", map[string]any{"database": "shop", "app": "api", "access": "read"}))
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}

	rc := sdk.RestoreContext{ID: "db-restore", OldProjectID: "rt_old", NewProjectID: "rt_new", InstallIDs: map[int64]int64{5: 50, 7: 70}}
	if _, _, err := testkit.RunRestore(t, &App{}, root, rc); err != nil {
		t.Fatal(err)
	}
	// RunRestore works on a copy and points APTEVA_DATA_DIR at it.
	restoredRoot := filepath.Join(os.Getenv("APTEVA_DATA_DIR"), "database")
	// Applying the same restore again (a restart before the SDK recorded it)
	// moves nothing twice.
	if moved, err := engine.RemapScopes(context.Background(), restoredRoot, rc.ID, func(scope string) (string, bool) { return restoredScope(scope, rc) }); err != nil || moved != 0 {
		t.Fatalf("second application moved %d scopes (%v)", moved, err)
	}
	m2, err := engine.Open(restoredRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer m2.Close()
	b := &App{manager: m2}
	count := func(c *sdk.Caller, args map[string]any) (string, error) {
		v, err := invoke(b, c, "count", args)
		if err != nil {
			return "", err
		}
		return v.(map[string]any)["count"].(string), nil
	}
	newFunctions := &sdk.Caller{ProjectID: "rt_new", AppInstallID: 50, AppName: "functions", DefaultEffect: "allow"}
	newAPI := &sdk.Caller{ProjectID: "rt_new", AppInstallID: 70, AppName: "api", DefaultEffect: "allow"}
	newOperator := &sdk.Caller{ProjectID: "rt_new", AgentID: 1, DefaultEffect: "allow"}
	if n, err := count(newFunctions, orders); err != nil || n != "1" {
		t.Fatalf("app database after restore: %v %v", n, err)
	}
	if n, err := count(newOperator, orders); err != nil || n != "1" {
		t.Fatalf("project database after restore: %v %v", n, err)
	}
	listed := must(invoke(b, newOperator, "collections_list", map[string]any{"database": "shop"}))
	if raw, _ := json.Marshal(listed); !strings.Contains(string(raw), `"orders"`) || !strings.Contains(string(raw), `"customers"`) {
		t.Fatalf("collections after restore: %s", raw)
	}
	if n, err := count(newAPI, map[string]any{"scope": "project", "database": "shop", "collection": "orders"}); err != nil || n != "1" {
		t.Fatalf("granted app after restore: %v %v", n, err)
	}
	if n, err := count(otherProject, orders); err != nil || n != "1" {
		t.Fatalf("other project after restore: %v %v", n, err)
	}
	if _, err := count(functions, orders); err == nil {
		t.Fatal("old scope still resolves")
	}
	if _, err := count(api, map[string]any{"scope": "project", "database": "shop", "collection": "orders"}); err == nil {
		t.Fatal("old grant still resolves")
	}
}
