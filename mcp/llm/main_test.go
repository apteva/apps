package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

func TestTokenAuthAcceptsOriginalAuthorization(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("proj-test"))
	app := &App{}
	if err := app.OnMount(ctx); err != nil {
		t.Fatalf("mount: %v", err)
	}
	out, err := createToken(ctx.AppDB(), map[string]any{
		"project_id":   "proj-test",
		"subject_type": "agent",
		"subject_id":   "agent-1",
	})
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("X-Apteva-Original-Authorization", "Bearer "+out["token"].(string))
	ident, err := authenticateLLMToken(ctx.AppDB(), req)
	if err != nil {
		t.Fatalf("auth: %v", err)
	}
	if ident.ProjectID != "proj-test" || ident.SubjectType != "agent" || ident.SubjectID != "agent-1" {
		t.Fatalf("identity = %+v", ident)
	}
}

func TestPolicyDeniesDisallowedModel(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("proj-test"))
	app := &App{}
	if err := app.OnMount(ctx); err != nil {
		t.Fatalf("mount: %v", err)
	}
	if _, err := dbPolicySet(ctx.AppDB(), "proj-test", map[string]any{
		"allowed_models": []any{"openai/gpt-4.1"},
	}); err != nil {
		t.Fatalf("policy: %v", err)
	}
	_, err := app.executeChat(ctx, &TokenIdentity{ProjectID: "proj-test", SubjectType: "agent", SubjectID: "agent-1"}, map[string]any{
		"model": "openai/gpt-3.5",
		"messages": []any{
			map[string]any{"role": "user", "content": "hello"},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "model not allowed") {
		t.Fatalf("expected model policy error, got %v", err)
	}
}

func TestOpenAICompatibleRouteForwardsAndRecordsUsage(t *testing.T) {
	var gotAuth, gotModel string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode upstream request: %v", err)
		}
		gotModel, _ = body["model"].(string)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl_test",
			"object":"chat.completion",
			"model":"gpt-4.1",
			"choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}
		}`))
	}))
	defer upstream.Close()

	ctx := tk.NewAppCtx(t, "apteva.yaml",
		tk.WithProjectID("proj-test"),
		tk.WithConfig(map[string]string{"openai_api_key": "sk-test"}),
	)
	app := &App{httpClient: upstream.Client()}
	if err := app.OnMount(ctx); err != nil {
		t.Fatalf("mount: %v", err)
	}
	if _, err := dbProviderConfigUpsert(ctx.AppDB(), "proj-test", map[string]any{
		"provider": "openai",
		"base_url": upstream.URL,
		"key_ref":  "openai_api_key",
	}); err != nil {
		t.Fatalf("provider: %v", err)
	}
	if _, err := dbPolicySet(ctx.AppDB(), "proj-test", map[string]any{
		"allowed_models": []any{"openai/*"},
	}); err != nil {
		t.Fatalf("policy: %v", err)
	}
	tok, err := createToken(ctx.AppDB(), map[string]any{
		"project_id":   "proj-test",
		"subject_type": "agent",
		"subject_id":   "agent-1",
	})
	if err != nil {
		t.Fatalf("token: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
		"model":"openai/gpt-4.1",
		"messages":[{"role":"user","content":"hello"}]
	}`))
	req.Header.Set("Authorization", "Bearer "+tok["token"].(string))
	rec := httptest.NewRecorder()
	app.handleV1(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if gotAuth != "Bearer sk-test" {
		t.Fatalf("upstream auth = %q", gotAuth)
	}
	if gotModel != "gpt-4.1" {
		t.Fatalf("upstream model = %q", gotModel)
	}
	usage, err := dbUsageGet(ctx.AppDB(), usageFilter{ProjectID: "proj-test", Period: currentPeriod()})
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	if usage.Requests != 1 || usage.RequestTokens != 7 || usage.ResponseTokens != 3 || usage.TotalTokens != 10 {
		t.Fatalf("usage = %+v", usage)
	}
}
