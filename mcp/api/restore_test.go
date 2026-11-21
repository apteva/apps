package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

// An API written under project rt_old is restored under rt_new: the API,
// its routes and its key keep their ids, the route still names its function
// and still lists Functions' function ids, the key still authenticates, the
// gateway serves the route under the new project, and URLs that pointed at
// the old runtime follow the new one. Another project's API is untouched.
func TestRestoreMovesAPIsToNewProject(t *testing.T) {
	const oldBase, newBase = "https://runtime.apteva.invalid/rt_old", "https://runtime.apteva.invalid/rt_new"
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("rt_old"))
	db := ctx.AppDB()
	api, err := dbCreateAPI(db, apiInput{ProjectID: "rt_old", Slug: "shop", Name: "Shop",
		AuthJSON: `{"kind":"any_of","function_ids":[12],"methods":[{"kind":"api_key"},{"kind":"authorizer","provider":"app","app":"identity","path":"/authorize","issuer":"` + oldBase + `/api/apps/identity"}]}`})
	if err != nil {
		t.Fatal(err)
	}
	hello, _, err := dbUpsertRoute(db, routeInput{ProjectID: "rt_old", APIID: api.ID, Method: "GET", PathPattern: "/hello", TargetKind: "function", TargetRef: "hello", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	proxy, _, err := dbUpsertRoute(db, routeInput{ProjectID: "rt_old", APIID: api.ID, Method: "GET", PathPattern: "/proxy", TargetKind: "http", TargetRef: oldBase + "/api/apps/content/public", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	key, secret, _, err := dbCreateAPIKey(db, apiKeyInput{ProjectID: "rt_old", APIID: api.ID, Name: "client", ClaimsJSON: `{}`, ScopesJSON: `[]`, MetadataJSON: `{}`})
	if err != nil {
		t.Fatal(err)
	}
	keep, err := dbCreateAPI(db, apiInput{ProjectID: "keep", Slug: "shop", Name: "Kept"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := dbUpsertRoute(db, routeInput{ProjectID: "keep", APIID: keep.ID, Method: "GET", PathPattern: "/x", TargetKind: "http", TargetRef: "https://elsewhere.example/x", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	fixture := filepath.Join(t.TempDir(), "api.db")
	if _, err := db.Exec(`VACUUM INTO ?`, fixture); err != nil {
		t.Fatal(err)
	}

	rc := sdk.RestoreContext{ID: "api-restore", OldProjectID: "rt_old", NewProjectID: "rt_new",
		InstallIDs: map[int64]int64{7: 70}, PublicBaseURLs: map[string]string{oldBase: newBase}}
	restored, report, err := tk.RunRestore(t, &App{}, fixture, rc, tk.WithProjectID("rt_new"))
	if err != nil {
		t.Fatal(err)
	}
	if report.ProjectRows["apis"] != 1 || report.ProjectRows["api_routes"] != 2 || report.ProjectRows["api_keys"] != 1 {
		t.Fatalf("report %+v", report.ProjectRows)
	}
	if gone, _ := dbGetAPIBySlug(restored, "rt_old", "shop"); gone != nil {
		t.Fatal("API still under the old project")
	}
	got, err := dbGetAPIBySlug(restored, "rt_new", "shop")
	if err != nil || got == nil || got.ID != api.ID {
		t.Fatalf("API after restore: %+v %v", got, err)
	}
	if !strings.Contains(got.AuthJSON, `"issuer":"`+newBase+`/api/apps/identity"`) || !strings.Contains(got.AuthJSON, `"function_ids":[12]`) {
		t.Fatalf("API auth after restore: %s", got.AuthJSON)
	}
	routes, err := dbListRoutes(restored, "rt_new", api.ID)
	if err != nil || len(routes) != 2 {
		t.Fatalf("routes after restore: %v %v", routes, err)
	}
	for _, r := range routes {
		switch r.ID {
		case hello.ID:
			if r.TargetKind != "function" || r.TargetRef != "hello" {
				t.Fatalf("function route after restore: %+v", r)
			}
		case proxy.ID:
			if r.TargetRef != newBase+"/api/apps/content/public" {
				t.Fatalf("http route after restore: %+v", r)
			}
		default:
			t.Fatalf("unexpected route %+v", r)
		}
	}
	if ok, err := dbValidateAPIKey(restored, "rt_new", api.ID, secret); err != nil || !ok {
		t.Fatalf("key after restore: %v %v", ok, err)
	}
	if k, err := dbGetAPIKey(restored, "rt_new", key.ID); err != nil || k == nil {
		t.Fatalf("key metadata after restore: %v", err)
	}
	kept, _ := dbGetAPIBySlug(restored, "keep", "shop")
	keptRoutes, _ := dbListRoutes(restored, "keep", keep.ID)
	if kept == nil || kept.ID != keep.ID || len(keptRoutes) != 1 || keptRoutes[0].TargetRef != "https://elsewhere.example/x" {
		t.Fatalf("other project after restore: %+v %+v", kept, keptRoutes)
	}

	// The restored gateway serves the function route under the new project,
	// passing the same function ids to Functions.
	var calls atomic.Int32
	platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/apps/callback/apps/functions/call" {
			http.Error(w, "unexpected callback", 500)
			return
		}
		calls.Add(1)
		var call struct {
			Tool  string `json:"tool"`
			Input struct {
				Name      string            `json:"name"`
				Project   string            `json:"_project_id"`
				Principal functionPrincipal `json:"principal"`
			} `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&call); err != nil {
			t.Error(err)
		}
		if call.Tool != "functions_invoke_authenticated" || call.Input.Name != "hello" || call.Input.Project != "rt_new" ||
			call.Input.Principal.ProjectID != "rt_new" || len(call.Input.Principal.FunctionIDs) != 1 || call.Input.Principal.FunctionIDs[0] != 12 {
			t.Errorf("function call after restore: %+v", call)
		}
		json.NewEncoder(w).Encode(map[string]any{"status": "ok", "response": `{"hello":"restored"}`})
	}))
	defer platform.Close()
	t.Setenv("APTEVA_GATEWAY_URL", platform.URL)
	t.Setenv("APTEVA_OUTBOUND_TOKEN", "gateway-outbound")
	t.Setenv("APTEVA_APP_TOKEN", "gateway-internal")
	manifest := (&App{}).Manifest()
	newCtx := sdk.NewAppCtxForTest(&manifest, restored, nil, nil, nil)
	app := &App{}
	if err := app.OnMount(newCtx); err != nil {
		t.Fatal(err)
	}
	defer app.OnUnmount(newCtx)
	req := httptest.NewRequest("GET", "/gw/shop/hello?project_id=rt_new", nil)
	req.Header.Set("X-API-Key", secret)
	rec := httptest.NewRecorder()
	app.handleGateway(rec, req)
	if rec.Code != http.StatusOK || calls.Load() != 1 || !strings.Contains(rec.Body.String(), "restored") {
		t.Fatalf("gateway after restore: %d %s (calls %d)", rec.Code, rec.Body.String(), calls.Load())
	}
}
