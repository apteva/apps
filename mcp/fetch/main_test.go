package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

func TestManifestAndToolShape(t *testing.T) {
	app := &App{}
	m := app.Manifest()
	if m.Name != "fetch" {
		t.Fatalf("manifest name = %q", m.Name)
	}
	if len(m.Provides.MCPTools) != 1 || m.Provides.MCPTools[0].Name != "fetch_request" {
		t.Fatalf("manifest tools = %+v", m.Provides.MCPTools)
	}
	tools := app.MCPTools()
	if len(tools) != 1 {
		t.Fatalf("tool count = %d, want 1", len(tools))
	}
	if tools[0].Name != "fetch_request" || tools[0].Handler == nil {
		t.Fatalf("tool malformed: %+v", tools[0])
	}
	if tools[0].InputSchema["type"] != "object" {
		t.Fatalf("schema type = %v", tools[0].InputSchema["type"])
	}
}

func TestFetchRequest_PostJSONAndParsesResponse(t *testing.T) {
	ctx := newFetchCtx(t, map[string]string{"allow_private_networks": "true"})
	app := &App{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s", r.Method)
		}
		if r.URL.Query().Get("dry_run") != "true" {
			t.Fatalf("query = %s", r.URL.RawQuery)
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"name":"Alice"`) {
			t.Fatalf("body = %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"id":123}`))
	}))
	defer upstream.Close()

	out, err := app.toolFetchRequest(ctx, map[string]any{
		"method": "post",
		"url":    upstream.URL + "/users",
		"query": map[string]any{
			"dry_run": "true",
		},
		"body": map[string]any{
			"json": map[string]any{"name": "Alice"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	res := out.(*FetchResult)
	if res.Status != 200 || res.BodyJSON == nil || res.HistoryID == 0 {
		t.Fatalf("result malformed: %+v", res)
	}
}

func TestFetchRequest_BlocksPrivateNetworkByDefault(t *testing.T) {
	ctx := newFetchCtx(t, nil)
	app := &App{}
	_, err := app.toolFetchRequest(ctx, map[string]any{
		"method": "GET",
		"url":    "http://127.0.0.1:12345/",
	})
	if err == nil || !strings.Contains(err.Error(), "private") {
		t.Fatalf("expected private-network block, got %v", err)
	}
}

func TestFetchRequest_RedactsSecretEnvironmentValuesInHistory(t *testing.T) {
	ctx := newFetchCtx(t, map[string]string{"allow_private_networks": "true"})
	app := &App{}
	env, err := dbEnvironmentCreate(ctx.AppDB(), "test-proj", &Environment{
		Name: "Local",
		Vars: []EnvironmentVar{{
			Key:      "API_TOKEN",
			Value:    "super-secret-token",
			IsSecret: true,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer super-secret-token" {
			t.Fatalf("Authorization = %q", got)
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer upstream.Close()

	out, err := app.toolFetchRequest(ctx, map[string]any{
		"method":         "GET",
		"url":            upstream.URL,
		"environment_id": env.ID,
		"headers": map[string]any{
			"Authorization": "Bearer {{API_TOKEN}}",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	historyID := out.(*FetchResult).HistoryID
	row := ctx.AppDB().QueryRow(`SELECT redacted_request_json FROM fetch_history WHERE id=?`, historyID)
	var raw string
	if err := row.Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "super-secret-token") {
		t.Fatalf("history leaked secret: %s", raw)
	}
	if !strings.Contains(raw, "[redacted]") {
		t.Fatalf("history not redacted: %s", raw)
	}
}

func TestEnvironmentUpdate_PreservesMaskedSecret(t *testing.T) {
	ctx := newFetchCtx(t, nil)
	env, err := dbEnvironmentCreate(ctx.AppDB(), "test-proj", &Environment{
		Name: "Prod",
		Vars: []EnvironmentVar{{
			Key:      "TOKEN",
			Value:    "keep-me",
			IsSecret: true,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := dbEnvironmentUpdate(ctx.AppDB(), "test-proj", env.ID, map[string]any{
		"name": "Prod",
		"vars": []any{
			map[string]any{
				"key":       "TOKEN",
				"value":     "",
				"is_secret": true,
				"has_value": true,
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.Vars) != 1 || updated.Vars[0].Value != "" || !updated.Vars[0].HasValue {
		t.Fatalf("masked view malformed: %+v", updated.Vars)
	}
	revealed, _, err := dbEnvironmentVars(ctx.AppDB(), "test-proj", env.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(revealed) != 1 || revealed[0].Value != "keep-me" {
		t.Fatalf("secret was not preserved: %+v", revealed)
	}
}

func TestHTTP_SavedRequestRunAndHistory(t *testing.T) {
	ctx := newFetchCtx(t, map[string]string{"allow_private_networks": "true"})
	_ = ctx
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"saved":true}`))
	}))
	defer upstream.Close()
	srv := newHTTPServer(t)
	defer srv.Close()

	createBody := map[string]any{
		"name":         "Saved GET",
		"method":       "GET",
		"url_template": upstream.URL,
	}
	b, _ := json.Marshal(createBody)
	resp, err := http.Post(srv.URL+"/requests?project_id=test-proj", "application/json", bytes.NewReader(b))
	mustOK(t, resp, err)
	var created struct {
		Request SavedRequest `json:"request"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}

	resp, err = http.Post(srv.URL+"/requests/"+itoa(created.Request.ID)+"/run?project_id=test-proj", "application/json", strings.NewReader(`{}`))
	mustOK(t, resp, err)
	var run FetchResult
	if err := json.NewDecoder(resp.Body).Decode(&run); err != nil {
		t.Fatal(err)
	}
	if run.Status != 200 || run.HistoryID == 0 {
		t.Fatalf("run malformed: %+v", run)
	}

	resp, err = http.Get(srv.URL + "/history?project_id=test-proj")
	mustOK(t, resp, err)
	var hist struct {
		History []HistoryRow `json:"history"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&hist); err != nil {
		t.Fatal(err)
	}
	if len(hist.History) != 1 || hist.History[0].SavedRequestID == nil {
		t.Fatalf("history malformed: %+v", hist.History)
	}
}

func newFetchCtx(t *testing.T, cfg map[string]string) *sdk.AppCtx {
	t.Helper()
	opts := []tk.Option{tk.WithProjectID("test-proj")}
	if cfg != nil {
		opts = append(opts, tk.WithConfig(cfg))
	}
	ctx := tk.NewAppCtx(t, "apteva.yaml", opts...)
	globalCtx = ctx
	return ctx
}

func newHTTPServer(t *testing.T) *httptest.Server {
	t.Helper()
	app := &App{}
	mux := http.NewServeMux()
	for _, route := range app.HTTPRoutes() {
		rt := route
		mux.HandleFunc(rt.Pattern, func(w http.ResponseWriter, r *http.Request) {
			rt.Handler(w, r)
		})
	}
	return httptest.NewServer(mux)
}

func mustOK(t *testing.T, resp *http.Response, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("HTTP %d: %s", resp.StatusCode, body)
	}
}

func itoa(n int64) string {
	return strconvFormat(n)
}

func strconvFormat(n int64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
