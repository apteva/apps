package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

type stageFixture struct {
	app    *App
	ctx    *sdk.AppCtx
	api    *API
	config *APIConfiguration
	alpha  *httptest.Server
	beta   *httptest.Server
}

// newStageFixture creates one API with a templated HTTP route snapshotted
// into a single configuration.
func newStageFixture(t *testing.T) *stageFixture {
	t.Helper()
	app, ctx := mountTestApp(t)
	echo := func(tag string) *httptest.Server {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(tag + ":" + r.URL.Path))
		}))
		t.Cleanup(srv.Close)
		return srv
	}
	f := &stageFixture{app: app, ctx: ctx, alpha: echo("alpha"), beta: echo("beta")}
	created, err := app.toolAPICreate(ctx, map[string]any{"slug": "orders"})
	if err != nil {
		t.Fatal(err)
	}
	f.api = created.(map[string]any)["api"].(*API)
	if _, err := app.toolRouteAdd(ctx, map[string]any{
		"api_id": f.api.ID, "method": "GET", "path_pattern": "/orders",
		"target_kind": "http", "target_ref": "{{stage.vars.backend}}", "target_path": "/{{ stage.name }}/orders",
	}); err != nil {
		t.Fatal(err)
	}
	out, err := app.toolConfigCreate(ctx, map[string]any{"api_id": f.api.ID})
	if err != nil {
		t.Fatal(err)
	}
	f.config = out.(map[string]any)["configuration"].(*APIConfiguration)
	return f
}

func (f *stageFixture) createStage(t *testing.T, name, host string, vars map[string]any) *APIStage {
	t.Helper()
	out, err := f.app.toolStageCreate(f.ctx, map[string]any{"api_id": f.api.ID, "name": name, "configuration_id": f.config.ID, "hostname": host, "vars": vars})
	if err != nil {
		t.Fatal(err)
	}
	return out.(map[string]any)["stage"].(*APIStage)
}

func (f *stageFixture) get(host, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "http://"+host+"/gw"+path+"?project_id="+testProject, nil)
	req.Host = host
	rec := httptest.NewRecorder()
	f.app.handleGateway(rec, req)
	return rec
}

func TestStageVarsSubstitutePerStageOnSameConfiguration(t *testing.T) {
	f := newStageFixture(t)
	alpha := f.createStage(t, "alpha", "alpha.example", map[string]any{"backend": f.alpha.URL})
	beta := f.createStage(t, "beta", "beta.example", map[string]any{"backend": f.beta.URL})
	if alpha.ConfigurationID != beta.ConfigurationID || alpha.Vars["backend"] != f.alpha.URL || alpha.Rev != 1 {
		t.Fatalf("stages = %+v / %+v", alpha, beta)
	}
	if got := f.get("alpha.example", "/orders"); got.Code != 200 || got.Body.String() != "alpha:/alpha/orders" {
		t.Fatalf("alpha = %d %q", got.Code, got.Body.String())
	}
	if got := f.get("beta.example", "/orders"); got.Code != 200 || got.Body.String() != "beta:/beta/orders" {
		t.Fatalf("beta = %d %q", got.Code, got.Body.String())
	}
	// The cached configuration route is never mutated by substitution.
	route, _, err := dbMatchConfigurationRoute(f.ctx.AppReadDB(), testProject, f.api.ID, f.config.ID, "GET", "/orders")
	if err != nil || route.TargetRef != "{{stage.vars.backend}}" {
		t.Fatalf("cached route = %+v err=%v", route, err)
	}
}

func TestStageVarsMissingVarFailsClosed(t *testing.T) {
	f := newStageFixture(t)
	// Write time: a stage cannot be created or updated without referenced vars.
	if _, err := f.app.toolStageCreate(f.ctx, map[string]any{"api_id": f.api.ID, "name": "bare", "configuration_id": f.config.ID}); err == nil || !strings.Contains(err.Error(), "backend") {
		t.Fatalf("create without var err = %v", err)
	}
	stage := f.createStage(t, "alpha", "alpha.example", map[string]any{"backend": f.alpha.URL})
	if _, err := f.app.toolStageUpdate(f.ctx, map[string]any{"stage_id": stage.ID, "vars": map[string]any{}}); err == nil || !strings.Contains(err.Error(), "backend") {
		t.Fatalf("update dropping var err = %v", err)
	}
	// Request time: a var missing from stored data fails with a config error,
	// never an empty substitution.
	if _, err := f.ctx.AppDB().Exec(`UPDATE api_stages SET vars_json='{}' WHERE id=?`, stage.ID); err != nil {
		t.Fatal(err)
	}
	got := f.get("alpha.example", "/orders")
	if got.Code != http.StatusInternalServerError {
		t.Fatalf("missing var status = %d %q", got.Code, got.Body.String())
	}
	var body struct {
		Error     string `json:"error"`
		ErrorCode string `json:"error_code"`
	}
	if err := json.Unmarshal(got.Body.Bytes(), &body); err != nil || body.ErrorCode != "stage_config_error" || !strings.Contains(body.Error, `"backend"`) {
		t.Fatalf("missing var body = %q err=%v", got.Body.String(), err)
	}
}

func TestStageVarsResolvedTargetIsRevalidated(t *testing.T) {
	f := newStageFixture(t)
	stage := f.createStage(t, "alpha", "alpha.example", map[string]any{"backend": f.alpha.URL})
	if _, err := f.ctx.AppDB().Exec(`UPDATE api_stages SET vars_json=? WHERE id=?`, `{"backend":"ftp://bad.example"}`, stage.ID); err != nil {
		t.Fatal(err)
	}
	if got := f.get("alpha.example", "/orders"); got.Code != 500 || !strings.Contains(got.Body.String(), "stage_config_error") {
		t.Fatalf("invalid resolved target = %d %q", got.Code, got.Body.String())
	}
}

func TestStagePlaceholderValidation(t *testing.T) {
	f := newStageFixture(t)
	for _, bad := range []string{"{{stage.foo}}", "{{stage.vars.}}", "{{stage.vars.9x}}", "{{stage}}"} {
		_, err := f.app.toolRouteAdd(f.ctx, map[string]any{"api_id": f.api.ID, "method": "GET", "path_pattern": "/bad", "target_kind": "http", "target_ref": "https://x.example", "target_path": "/" + bad})
		if err == nil || !strings.Contains(err.Error(), "invalid stage placeholder") {
			t.Fatalf("%s err = %v", bad, err)
		}
	}
	// Non-stage templates are not ours to judge.
	if _, err := f.app.toolRouteAdd(f.ctx, map[string]any{"api_id": f.api.ID, "method": "GET", "path_pattern": "/other", "target_kind": "http", "target_ref": "https://x.example", "target_path": "/{{other}}"}); err != nil {
		t.Fatal(err)
	}
	for _, vars := range []map[string]any{{"9bad": "x"}, {"k": "a\nb"}, {"k": "{{stage.name}}"}, {"k": 1}} {
		if _, err := f.app.toolStageCreate(f.ctx, map[string]any{"api_id": f.api.ID, "name": "v", "configuration_id": f.config.ID, "vars": vars}); err == nil {
			t.Fatalf("vars %v accepted", vars)
		}
	}
}

func TestStageVarsSubstituteEventsJSONValues(t *testing.T) {
	route := &APIRoute{TargetKind: "app_events", TargetRef: "{{stage.vars.app}}", EventsJSON: `{"topics":["{{stage.vars.topic}}.*"],"output":{"type":"invalidate"},"note":"say \"{{stage.name}}\""}`}
	api := &API{StageID: 1, StageName: "beta", StageVarsJSON: `{"app":"orders-beta","topic":"order"}`}
	resolved, err := resolveStageRoute(api, route)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.TargetRef != "orders-beta" || !strings.Contains(resolved.EventsJSON, `"order.*"`) || !strings.Contains(resolved.EventsJSON, `say \"beta\"`) {
		t.Fatalf("resolved = %+v", resolved)
	}
	if !json.Valid([]byte(resolved.EventsJSON)) || route.TargetRef != "{{stage.vars.app}}" {
		t.Fatalf("resolved events invalid or source mutated: %+v", resolved)
	}
}

func TestLegacyRequestsLeavePlaceholdersUntouched(t *testing.T) {
	app, ctx := mountTestApp(t)
	var seen string
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.URL.EscapedPath()
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(origin.Close)
	created, err := app.toolAPICreate(ctx, map[string]any{"slug": "legacy"})
	if err != nil {
		t.Fatal(err)
	}
	api := created.(map[string]any)["api"].(*API)
	if _, err := app.toolRouteAdd(ctx, map[string]any{"api_id": api.ID, "method": "GET", "path_pattern": "/x", "target_kind": "http", "target_ref": origin.URL, "target_path": "/{{stage.name}}"}); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "http://gw.local/gw/legacy/x?project_id="+testProject, nil)
	rec := httptest.NewRecorder()
	app.handleGateway(rec, req)
	if rec.Code != 200 || !strings.Contains(seen, "stage.name") {
		t.Fatalf("legacy = %d path %q", rec.Code, seen)
	}
	route := &APIRoute{TargetRef: "{{stage.vars.x}}"}
	if got, err := resolveStageRoute(&API{}, route); err != nil || got != route {
		t.Fatalf("legacy resolve = %+v err=%v", got, err)
	}
}

func TestStageRollbackToPrevious(t *testing.T) {
	f := newStageFixture(t)
	stage := f.createStage(t, "alpha", "alpha.example", map[string]any{"backend": f.alpha.URL})
	if _, err := f.app.toolStageRollback(f.ctx, map[string]any{"stage_id": stage.ID}); err == nil || !strings.Contains(err.Error(), "no previous configuration") {
		t.Fatalf("rollback without previous err = %v", err)
	}
	cloneOut, err := f.app.toolConfigClone(f.ctx, map[string]any{"api_id": f.api.ID, "configuration_id": f.config.ID})
	if err != nil {
		t.Fatal(err)
	}
	clone := cloneOut.(map[string]any)["configuration"].(*APIConfiguration)
	out, err := f.app.toolStagePromote(f.ctx, map[string]any{"stage_id": stage.ID, "configuration_id": clone.ID, "expected_rev": 1})
	if err != nil {
		t.Fatal(err)
	}
	promoted := out.(map[string]any)["stage"].(*APIStage)
	if promoted.ConfigurationID != clone.ID || promoted.PreviousConfigurationID != f.config.ID || promoted.Rev != 2 {
		t.Fatalf("promoted = %+v", promoted)
	}
	// Promoting to the current configuration is a no-op.
	out, err = f.app.toolStagePromote(f.ctx, map[string]any{"stage_id": stage.ID, "configuration_id": clone.ID})
	if err != nil || out.(map[string]any)["stage"].(*APIStage).Rev != 2 {
		t.Fatalf("idempotent promote = %+v err=%v", out, err)
	}
	out, err = f.app.toolStageRollback(f.ctx, map[string]any{"stage_id": stage.ID})
	if err != nil {
		t.Fatal(err)
	}
	rolled := out.(map[string]any)["stage"].(*APIStage)
	if rolled.ConfigurationID != f.config.ID || rolled.PreviousConfigurationID != clone.ID || rolled.Rev != 3 {
		t.Fatalf("rolled back = %+v", rolled)
	}
	// Explicit target still works.
	out, err = f.app.toolStageRollback(f.ctx, map[string]any{"stage_id": stage.ID, "configuration_id": clone.ID})
	if err != nil || out.(map[string]any)["stage"].(*APIStage).ConfigurationID != clone.ID {
		t.Fatalf("explicit rollback = %+v err=%v", out, err)
	}
	if got := f.get("alpha.example", "/orders"); got.Code != 200 || got.Body.String() != "alpha:/alpha/orders" {
		t.Fatalf("after rollback = %d %q", got.Code, got.Body.String())
	}
}

func TestStageRevConflictChangesNothing(t *testing.T) {
	f := newStageFixture(t)
	stage := f.createStage(t, "alpha", "alpha.example", map[string]any{"backend": f.alpha.URL})
	cloneOut, err := f.app.toolConfigClone(f.ctx, map[string]any{"api_id": f.api.ID, "configuration_id": f.config.ID})
	if err != nil {
		t.Fatal(err)
	}
	clone := cloneOut.(map[string]any)["configuration"].(*APIConfiguration)
	var conflict *stageRevConflictError
	if _, err := f.app.toolStagePromote(f.ctx, map[string]any{"stage_id": stage.ID, "configuration_id": clone.ID, "expected_rev": 7}); !errors.As(err, &conflict) {
		t.Fatalf("promote conflict err = %v", err)
	}
	if _, err := f.app.toolStageUpdate(f.ctx, map[string]any{"stage_id": stage.ID, "vars": map[string]any{"backend": f.beta.URL}, "expected_rev": 7}); !errors.As(err, &conflict) {
		t.Fatalf("update conflict err = %v", err)
	}
	if _, err := f.app.toolStageRollback(f.ctx, map[string]any{"stage_id": stage.ID, "configuration_id": clone.ID, "expected_rev": 2}); !errors.As(err, &conflict) {
		t.Fatalf("rollback conflict err = %v", err)
	}
	current, err := dbGetStage(f.ctx.AppDB(), testProject, stage.ID)
	if err != nil || current.Rev != 1 || current.ConfigurationID != f.config.ID || current.Vars["backend"] != f.alpha.URL {
		t.Fatalf("stage changed after conflicts: %+v err=%v", current, err)
	}
	out, err := f.app.toolStageUpdate(f.ctx, map[string]any{"stage_id": stage.ID, "vars": map[string]any{"backend": f.beta.URL}, "expected_rev": 1})
	if err != nil {
		t.Fatal(err)
	}
	if updated := out.(map[string]any)["stage"].(*APIStage); updated.Rev != 2 || updated.Vars["backend"] != f.beta.URL {
		t.Fatalf("updated = %+v", updated)
	}
	if got := f.get("alpha.example", "/orders"); got.Body.String() != "beta:/alpha/orders" {
		t.Fatalf("after vars update = %q", got.Body.String())
	}
	// A stale read loses: the guarded UPDATE matches no row.
	if _, err := dbUpdateStage(f.ctx.AppDB(), stage, map[string]any{"status": "active"}, 0); !errors.As(err, &conflict) {
		t.Fatalf("stale update err = %v", err)
	}
}

func TestStageVarsMigrationOnExistingDB(t *testing.T) {
	db, err := sql.Open("sqlite", t.TempDir()+"/legacy.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	apply := func(path string) {
		script, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(script)); err != nil {
			t.Fatalf("apply %s: %v", path, err)
		}
	}
	for _, path := range []string{"migrations/001_init.sql", "migrations/002_app_events.sql", "migrations/003_reliability.sql", "migrations/004_configurations_stages.sql", "migrations/005_generic_credentials.sql"} {
		apply(path)
	}
	if _, err := db.Exec(`INSERT INTO apis(id,project_id,slug,name) VALUES(1,?,?,?)`, testProject, "legacy", "Legacy"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO api_configurations(id,project_id,api_id,version,name) VALUES(1,?,1,1,'v1')`, testProject); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO api_stages(id,project_id,api_id,name,configuration_id,hostname) VALUES(1,?,1,'prod',1,'prod.example')`, testProject); err != nil {
		t.Fatal(err)
	}
	apply("migrations/006_stage_vars_rollback_rev.sql")
	stage, err := dbGetStage(db, testProject, 1)
	if err != nil || stage == nil {
		t.Fatalf("stage after migration = %+v err=%v", stage, err)
	}
	if len(stage.Vars) != 0 || stage.PreviousConfigurationID != 0 || stage.Rev != 1 || stage.ConfigurationID != 1 {
		t.Fatalf("migrated defaults = %+v", stage)
	}
	if _, err := dbRollbackStage(db, testProject, 1, 0, 0); err == nil {
		t.Fatal("migrated stage has no previous configuration")
	}
	served, err := dbGetPublicStageAPI(db, testProject, "prod.example")
	if err != nil || served == nil || served.StageName != "prod" || served.StageVarsJSON != "{}" {
		t.Fatalf("served = %+v err=%v", served, err)
	}
}

func TestStageToolSchemasAndManifestAgree(t *testing.T) {
	app := &App{}
	declared := map[string]bool{}
	for _, spec := range app.Manifest().Provides.MCPTools {
		declared[spec.Name] = true
	}
	tools := map[string]sdk.Tool{}
	for _, tool := range app.MCPTools() {
		tools[tool.Name] = tool
		if !declared[tool.Name] {
			t.Errorf("tool %s missing from apteva.yaml", tool.Name)
		}
	}
	for name := range declared {
		if _, ok := tools[name]; !ok {
			t.Errorf("manifest tool %s has no handler", name)
		}
	}
	props := func(name string) (map[string]any, []string) {
		schema := tools[name].InputSchema
		required, _ := schema["required"].([]string)
		return schema["properties"].(map[string]any), required
	}
	for _, name := range []string{"api_stage_create", "api_stage_update"} {
		if p, _ := props(name); p["vars"] == nil {
			t.Errorf("%s lacks vars", name)
		}
	}
	for _, name := range []string{"api_stage_update", "api_stage_promote", "api_stage_rollback"} {
		if p, _ := props(name); p["expected_rev"] == nil {
			t.Errorf("%s lacks expected_rev", name)
		}
	}
	if _, required := props("api_stage_rollback"); len(required) != 1 || required[0] != "stage_id" {
		t.Errorf("api_stage_rollback required = %v, want [stage_id]", required)
	}
}
