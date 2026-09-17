package main

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func titles(t *testing.T, a *App, c *sdk.AppCtx, args map[string]any) []string {
	t.Helper()
	v, e := a.dispatch(c, "items_list", args)
	if e != nil {
		t.Fatal(e)
	}
	out := []string{}
	for _, i := range v.(map[string]any)["items"].([]Item) {
		out = append(out, i.Title)
	}
	return out
}

// Tags and custom fields are item data the panel shows and the API accepts, so
// a search that ignores them answers "no results" about content that is there.
func TestSearchReachesTagsAndCustomFields(t *testing.T) {
	a, c := testApp(t, nil)
	create(t, a, c, map[string]any{"title": "Harbourline rollout", "tags": []any{"case-study", "enterprise"}})
	create(t, a, c, map[string]any{"title": "Weekly roundup", "body": "Covers the newsletter cadence."})
	create(t, a, c, map[string]any{"title": "Benchmarks explainer",
		"fields": map[string]any{"desk": "Research", "effort": "large"}})

	for _, tc := range []struct{ query, want string }{
		{"case-study", "Harbourline rollout"},  // tag only
		{"enterprise", "Harbourline rollout"},  // second tag
		{"Research", "Benchmarks explainer"},   // custom field value
		{"large", "Benchmarks explainer"},      // second custom field value
		{"Harbourline", "Harbourline rollout"}, // title, as before
		{"cadence", "Weekly roundup"},          // body, as before
	} {
		got := titles(t, a, c, map[string]any{"q": tc.query})
		if len(got) != 1 || got[0] != tc.want {
			t.Fatalf("q=%q returned %v, wanted just %q", tc.query, got, tc.want)
		}
	}

	// A custom field's key is not its content, so searching it must not match.
	if got := titles(t, a, c, map[string]any{"q": "desk"}); len(got) != 0 {
		t.Fatalf("custom field keys must not be searchable, got %v", got)
	}
	// Search stays case-insensitive across every attribute it now reaches.
	if got := titles(t, a, c, map[string]any{"q": "CASE-STUDY"}); len(got) != 1 {
		t.Fatalf("expected a case-insensitive tag match, got %v", got)
	}
}

// v0.1.x rows were written before tags and custom fields existed; json_each must
// yield no rows for them rather than failing the whole query.
func TestSearchToleratesRecordsWithoutTagsOrFields(t *testing.T) {
	a, c := testApp(t, nil)
	legacy := `{"title":"Legacy brief","body":"Written before tags existed","status":"idea","format":"idea","approval":"not_required","archived":false}`
	if _, e := c.AppDB().Exec("INSERT INTO editorial_items(project_id,data) VALUES(?,?)", "alpha", legacy); e != nil {
		t.Fatal(e)
	}
	create(t, a, c, map[string]any{"title": "Modern brief", "tags": []any{"legacy"}})

	if got := titles(t, a, c, map[string]any{"q": "Written before"}); len(got) != 1 || got[0] != "Legacy brief" {
		t.Fatalf("legacy row is not searchable by body: %v", got)
	}
	if got := titles(t, a, c, map[string]any{"q": "legacy"}); len(got) != 2 {
		t.Fatalf("expected the legacy title and the tagged item, got %v", got)
	}
}

func get(t *testing.T, a *App, path string) (int, string) {
	t.Helper()
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", path, nil)
	r.Header.Set("X-Apteva-Project-Id", "alpha")
	a.http(w, r)
	return w.Code, w.Body.String()
}

// An unrecognised filter used to be ignored, answering a deliberately narrow
// query with a full unfiltered page. That is the worst way for a filter to fail.
func TestUnknownQueryParametersAreRejected(t *testing.T) {
	a, _ := testApp(t, nil)
	create(t, a, a.ctx.WithProject("alpha"), map[string]any{"title": "Only item", "tags": []any{"keep"}})

	for _, path := range []string{
		"/items?tags=keep",
		"/items?fields=anything",
		"/items?nonsense=zzz",
		"/calendar?from=2026-10-01&to=2026-10-31&tags=keep",
		"/settings?status=idea",
	} {
		code, body := get(t, a, path)
		if code != 400 {
			t.Fatalf("%s returned %d, expected 400: %s", path, code, body)
		}
		if !strings.Contains(body, "unknown query parameter") {
			t.Fatalf("%s did not explain itself: %s", path, body)
		}
	}
	// The message names what went wrong and what the operation does accept.
	_, body := get(t, a, "/items?tags=keep")
	for _, want := range []string{"tags", "approval", "brand_id", "status"} {
		if !strings.Contains(body, want) {
			t.Fatalf("error message missing %q: %s", want, body)
		}
	}

	// Documented filters and the infrastructure parameters still pass.
	for _, path := range []string{
		"/items?status=idea&archived=all&limit=5",
		"/items?q=Only&install_id=3&api_key=x",
		"/calendar?from=2026-10-01&to=2026-10-31&include_releases=false",
	} {
		if code, body := get(t, a, path); code != 200 {
			t.Fatalf("%s returned %d: %s", path, code, body)
		}
	}
}

// Writes read their arguments from the body, so a filter pinned to their query
// would silently do nothing.
func TestWriteQueryParametersAreRejected(t *testing.T) {
	a, _ := testApp(t, nil)
	i := create(t, a, a.ctx.WithProject("alpha"), map[string]any{"title": "Subject"})

	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/items?title=ignored", strings.NewReader(`{"title":"Body wins"}`))
	r.Header.Set("X-Apteva-Project-Id", "alpha")
	a.http(w, r)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "request body") {
		t.Fatalf("POST with a query argument returned %d: %s", w.Code, w.Body.String())
	}

	// The id comes from the path, so repeating it in the query is not a filter.
	if code, body := get(t, a, "/items/1?id=2"); code != 400 {
		t.Fatalf("id in query returned %d: %s", code, body)
	}
	// The same path without it still reads the record.
	code, body := get(t, a, "/items/1")
	if code != 200 {
		t.Fatalf("plain read returned %d: %s", code, body)
	}
	var out struct {
		Item Item `json:"item"`
	}
	if e := json.Unmarshal([]byte(body), &out); e != nil || out.Item.ID != i.ID {
		t.Fatalf("unexpected body %s (%v)", body, e)
	}
}

// MCP already refused unknown arguments through additionalProperties; the HTTP
// rejection has to agree with it rather than invent a second contract.
func TestMCPAndHTTPAgreeOnAcceptedArguments(t *testing.T) {
	a, _ := testApp(t, nil)
	for _, tool := range a.MCPTools() {
		schema := tool.InputSchema
		if schema["additionalProperties"] != false {
			t.Fatalf("%s accepts unknown arguments", tool.Name)
		}
	}
	// items_list still publishes the archived enum after the spec refactor.
	for _, s := range toolSpecs() {
		if s.name != "items_list" {
			continue
		}
		props := s.schema["properties"].(map[string]any)
		if _, ok := props["archived"]; !ok {
			t.Fatal("items_list lost its archived parameter")
		}
	}
	var v validationError
	if e := checkQuery("items_list", "GET", map[string][]string{"tags": {"x"}}, false); !errors.As(e, &v) {
		t.Fatalf("expected a validation error, got %v", e)
	}
	if e := checkQuery("items_list", "GET", map[string][]string{"status": {"idea"}, "project_id": {"alpha"}}, false); e != nil {
		t.Fatalf("documented parameters must pass: %v", e)
	}
}

// The MCP door. The SDK publishes inputSchema but hands a tool its arguments
// verbatim, so additionalProperties is advice to clients and dispatch is what
// has to refuse an unknown argument. Until v0.3.3 an MCP caller passing tags
// got every item back — the same silent failure that was fixed over HTTP.
func TestMCPArgumentsRejectUnknownKeys(t *testing.T) {
	a, c := testApp(t, nil)
	create(t, a, c, map[string]any{"title": "Tagged item", "tags": []any{"case-study"}})
	create(t, a, c, map[string]any{"title": "Other item"})

	for _, args := range []map[string]any{
		{"tags": "case-study"},
		{"fields": "anything"},
		{"nonsense": "zzz"},
	} {
		_, e := a.dispatch(c, "items_list", args)
		var v validationError
		if !errors.As(e, &v) {
			t.Fatalf("items_list(%v) was accepted, got %v", args, e)
		}
		if !strings.Contains(e.Error(), "unknown argument") {
			t.Fatalf("unhelpful message for %v: %v", args, e)
		}
	}
	// Writes are refused the same way, not only reads.
	if _, e := a.dispatch(c, "items_create", map[string]any{"title": "x", "bogus": 1}); e == nil {
		t.Fatal("items_create accepted an unknown argument")
	}
	// An operation that takes nothing says so rather than listing an empty set.
	_, e := a.dispatch(c, "settings_get", map[string]any{"status": "idea"})
	if e == nil || !strings.Contains(e.Error(), "takes no arguments") {
		t.Fatalf("settings_get should refuse arguments: %v", e)
	}

	// Documented arguments still work, and transport parameters are not
	// arguments: the panel sends them on every request.
	if got := titles(t, a, c, map[string]any{"q": "case-study"}); len(got) != 1 {
		t.Fatalf("q still has to work, got %v", got)
	}
	if got := titles(t, a, c, map[string]any{"project_id": "alpha", "install_id": 3, "api_key": "x"}); len(got) != 2 {
		t.Fatalf("transport parameters must not be treated as arguments, got %v", got)
	}
}
