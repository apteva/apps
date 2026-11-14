package main

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
	"github.com/apteva/app-sdk/testkit"
)

type pagedEnvironmentPlatform struct {
	sdk.PlatformClient
	calls []recordedAppCall
}

func (p *pagedEnvironmentPlatform) CallAppResult(app, tool string, input map[string]any, out any) error {
	p.calls = append(p.calls, recordedAppCall{app: app, tool: tool, input: input})
	var value any
	switch tool {
	case "environment_catalog":
		value = map[string]any{}
	case "llm_models_list":
		value = LLMModels{}
	case "environment_list":
		cursor := "next-page"
		value = EnvironmentPage{Items: []EnvironmentSummary{{ID: "a", Name: "Alpha"}}, NextCursor: &cursor, HasMore: true}
		if input["cursor"] == "next-page" {
			value = EnvironmentPage{Items: []EnvironmentSummary{{ID: "b", Name: "Beta"}}}
		}
	case "environment_get":
		value = EnvironmentDefinition{ID: input["id"].(string), Name: "Selected", Spec: map[string]any{"protocol_fixtures": []any{map[string]any{"id": "carrier", "pack": "telephony-carrier"}}}}
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}
func TestEnvironmentCatalogAndProxyRemainPaginated(t *testing.T) {
	platform := &pagedEnvironmentPlatform{}
	ctx := testkit.NewAppCtx(t, "apteva.yaml", testkit.WithProjectID("project-one"), testkit.WithPlatform(platform))
	svc := &service{ctx: ctx, db: store{db: ctx.AppDB()}}
	app := &App{svc: svc}
	catalog, err := svc.catalog()
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog["environments"].([]EnvironmentSummary)) != 1 || catalog["environment_has_more"] != true || *catalog["environment_next_cursor"].(*string) != "next-page" {
		t.Fatalf("catalog=%+v", catalog)
	}
	for _, call := range platform.calls {
		if call.tool == "environment_get" || call.input["cursor"] != nil {
			t.Fatal("catalog drained pages or loaded details")
		}
	}
	platform.calls = nil
	w := httptest.NewRecorder()
	app.handleEnvironments(w, httptest.NewRequest("GET", "/api/environments?limit=17&cursor=next-page", nil))
	if w.Code != 200 || len(platform.calls) != 1 || platform.calls[0].input["limit"] != 17 || platform.calls[0].input["cursor"] != "next-page" {
		t.Fatalf("status=%d calls=%+v", w.Code, platform.calls)
	}
	var page EnvironmentPage
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil || page.Items[0].ID != "b" || page.HasMore {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	if strings.Contains(w.Body.String(), `"spec"`) {
		t.Fatal("spec in summary")
	}
	w = httptest.NewRecorder()
	app.handleEnvironment(w, httptest.NewRequest("GET", "/api/environments/b", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "telephony-carrier") {
		t.Fatalf("detail: %d %s", w.Code, w.Body)
	}
	for _, query := range []string{"limit=0", "limit=201", "limit=no", "limit=1&limit=2", "cursor="} {
		before := len(platform.calls)
		w := httptest.NewRecorder()
		app.handleEnvironments(w, httptest.NewRequest("GET", "/api/environments?"+query, nil))
		if w.Code != 400 || len(platform.calls) != before {
			t.Fatalf("invalid query %s reached platform: status=%d", query, w.Code)
		}
	}
}
