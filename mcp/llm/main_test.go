package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	sdk "github.com/apteva/app-sdk"
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

func TestTokenRevokeInvalidatesToken(t *testing.T) {
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
	if _, err := revokeToken(ctx.AppDB(), map[string]any{"token_id": out["id"]}); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+out["token"].(string))
	if _, err := authenticateLLMToken(ctx.AppDB(), req); err == nil || !strings.Contains(err.Error(), "revoked") {
		t.Fatalf("expected revoked token error, got %v", err)
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

func TestSubjectScopedPolicyCanSuspendSubject(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("proj-test"))
	app := &App{}
	if err := app.OnMount(ctx); err != nil {
		t.Fatalf("mount: %v", err)
	}
	if _, err := dbPolicySetDisabled(ctx.AppDB(), "proj-test", "tenant", "acme", true); err != nil {
		t.Fatalf("suspend: %v", err)
	}
	_, err := app.executeChat(ctx, &TokenIdentity{ProjectID: "proj-test", SubjectType: "tenant", SubjectID: "acme"}, map[string]any{
		"model": "openai/model-a",
		"messages": []any{
			map[string]any{"role": "user", "content": "hello"},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("expected disabled subject error, got %v", err)
	}
}

func TestCorruptPolicyFailsClosed(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("proj-test"))
	app := &App{}
	if err := app.OnMount(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := ctx.AppDB().Exec(`INSERT INTO policies(project_id, limits_json) VALUES ('proj-test', 'not-json')`); err != nil {
		t.Fatal(err)
	}
	if _, err := dbEffectivePolicies(ctx.AppDB(), &TokenIdentity{ProjectID: "proj-test", SubjectType: "tenant", SubjectID: "a"}); err == nil {
		t.Fatal("expected corrupt policy to fail closed")
	}
}

func TestUsageRecordIsIdempotentByRequestID(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("proj-test"))
	app := &App{}
	if err := app.OnMount(ctx); err != nil {
		t.Fatalf("mount: %v", err)
	}
	ident := &TokenIdentity{ProjectID: "proj-test", SubjectType: "tenant", SubjectID: "acme"}
	first, dup, err := dbUsageRecord(ctx.AppDB(), ident, "openai", "openai/model-a", 10, 5, 0, "completed", "req-1", "provider-1")
	if err != nil {
		t.Fatalf("first usage: %v", err)
	}
	if dup || first.ID == 0 || first.RequestID != "req-1" || first.ProviderRequestID != "provider-1" {
		t.Fatalf("first event = %+v duplicate=%v", first, dup)
	}
	second, dup, err := dbUsageRecord(ctx.AppDB(), ident, "openai", "openai/model-a", 100, 50, 0, "completed", "req-1", "provider-2")
	if err != nil {
		t.Fatalf("second usage: %v", err)
	}
	if !dup || second.ID != first.ID || second.RequestTokens != 10 || second.ProviderRequestID != "provider-1" {
		t.Fatalf("second event = %+v duplicate=%v first=%+v", second, dup, first)
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

func TestResponsesImageInputNormalizesForOpenAICompatibleProvider(t *testing.T) {
	var upstreamBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Fatalf("path=%s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&upstreamBody); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{"id":"vision-response","model":"vision-model","choices":[{"message":{"role":"assistant","content":"a chart"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1200,"completion_tokens":4}}`))
	}))
	defer upstream.Close()

	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("proj-test"), tk.WithConfig(map[string]string{"openai_api_key": "test"}))
	app := &App{httpClient: upstream.Client()}
	if err := app.OnMount(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := dbProviderConfigUpsert(ctx.AppDB(), "proj-test", map[string]any{"provider": "openai", "base_url": upstream.URL}); err != nil {
		t.Fatal(err)
	}
	if err := dbProviderModelsReplace(ctx.AppDB(), "proj-test", "openai", []ProviderModel{{
		ModelID: "vision-model", DisplayName: "Vision", InputModalities: json.RawMessage(`["text","image"]`), Status: "active",
	}}); err != nil {
		t.Fatal(err)
	}
	token, err := createToken(ctx.AppDB(), map[string]any{"project_id": "proj-test", "subject_type": "agent", "subject_id": "vision-agent"})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{
		"model":"openai/vision-model",
		"input":[{"type":"message","role":"user","content":[
			{"type":"input_text","text":"What is shown?"},
			{"type":"input_image","image_url":"https://images.example/chart.png","detail":"high"}
		]}]
	}`))
	req.Header.Set("Authorization", "Bearer "+token["token"].(string))
	rec := httptest.NewRecorder()
	app.handleV1(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	messages, _ := upstreamBody["messages"].([]any)
	message, _ := messages[0].(map[string]any)
	content, _ := message["content"].([]any)
	textBlock, _ := content[0].(map[string]any)
	imageBlock, _ := content[1].(map[string]any)
	imageRef, _ := imageBlock["image_url"].(map[string]any)
	if textBlock["type"] != "text" || imageBlock["type"] != "image_url" || imageRef["url"] != "https://images.example/chart.png" || imageRef["detail"] != "high" {
		t.Fatalf("upstream content=%+v", content)
	}
	var response map[string]any
	if json.Unmarshal(rec.Body.Bytes(), &response) != nil || response["object"] != "response" {
		t.Fatalf("response=%s", rec.Body.String())
	}

	modelsReq := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	modelsReq.Header.Set("Authorization", "Bearer "+token["token"].(string))
	modelsRec := httptest.NewRecorder()
	app.handleV1(modelsRec, modelsReq)
	if modelsRec.Code != http.StatusOK || !strings.Contains(modelsRec.Body.String(), `"input_modalities":["text","image"]`) {
		t.Fatalf("models status=%d body=%s", modelsRec.Code, modelsRec.Body.String())
	}
}

func TestImageInputRejectsExplicitTextOnlyModelBeforeReservation(t *testing.T) {
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"unexpected"}}]}`))
	}))
	defer upstream.Close()
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("proj-test"), tk.WithConfig(map[string]string{"openai_api_key": "test"}))
	app := &App{httpClient: upstream.Client()}
	if err := app.OnMount(ctx); err != nil {
		t.Fatal(err)
	}
	_, _ = dbProviderConfigUpsert(ctx.AppDB(), "proj-test", map[string]any{"provider": "openai", "base_url": upstream.URL})
	if err := dbProviderModelsReplace(ctx.AppDB(), "proj-test", "openai", []ProviderModel{{
		ModelID: "text-model", InputModalities: json.RawMessage(`["text"]`), Status: "active",
	}}); err != nil {
		t.Fatal(err)
	}
	_, err := app.executeChat(ctx, &TokenIdentity{ProjectID: "proj-test", SubjectType: "agent", SubjectID: "a"}, map[string]any{
		"model": "openai/text-model",
		"messages": []any{
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{"type": "image_url", "image_url": "https://images.example/a.png"},
				},
			},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "does not support image input") {
		t.Fatalf("error=%v", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("provider calls=%d", calls.Load())
	}
	usage, usageErr := dbUsageGet(ctx.AppDB(), usageFilter{ProjectID: "proj-test", Period: currentPeriod()})
	if usageErr != nil || usage.Requests != 0 {
		t.Fatalf("usage=%+v err=%v", usage, usageErr)
	}
}

func TestImageNormalizationValidationAndTokenEstimate(t *testing.T) {
	payload := strings.Repeat("A", 4<<20)
	body := map[string]any{
		"messages": []any{
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{"type": "input_text", "text": "describe"},
					map[string]any{"type": "input_image", "image_url": "data:image/png;base64," + payload},
				},
			},
		},
	}
	if err := normalizeChatImageInputs(body); err != nil {
		t.Fatal(err)
	}
	estimate := estimateChatTokens(body)
	if estimate < imageTokenEstimate || estimate > imageTokenEstimate+100 {
		t.Fatalf("image token estimate=%d", estimate)
	}

	invalid := []map[string]any{
		{"type": "input_image", "file_id": "file-1"},
		{"type": "input_image", "image_url": "data:text/plain;base64,SGVsbG8="},
		{"type": "image_url", "image_url": "file:///tmp/image.png"},
		{"type": "image_url", "image_url": "data:image/png;base64,not-valid"},
	}
	for _, block := range invalid {
		request := map[string]any{"messages": []any{map[string]any{"role": "user", "content": []any{block}}}}
		if err := normalizeChatImageInputs(request); err == nil {
			t.Fatalf("expected invalid image block to fail: %+v", block)
		}
	}
}

func TestAnthropicImageTranslationSupportsURLAndBase64(t *testing.T) {
	body := map[string]any{
		"model": "anthropic/claude-vision",
		"messages": []any{
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{"type": "image_url", "image_url": map[string]any{"url": "https://images.example/a.jpg"}},
					map[string]any{"type": "input_image", "image_url": "data:image/png;base64,iVBORw0KGgo="},
				},
			},
		},
	}
	if err := normalizeChatImageInputs(body); err != nil {
		t.Fatal(err)
	}
	translated, err := openAIChatToAnthropic("anthropic", body)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(translated)
	text := string(raw)
	if !strings.Contains(text, `"type":"url"`) || !strings.Contains(text, `"type":"base64"`) || !strings.Contains(text, `"media_type":"image/png"`) {
		t.Fatalf("translated=%s", text)
	}
}

func TestBoundAnthropicIntegrationBecomesProviderRoute(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml",
		tk.WithProjectID("proj-test"),
		tk.WithPlatform(&llmPlatformStub{
			identity: &sdk.InstallIdentity{Bindings: map[string]any{"anthropic_provider": float64(42)}},
			credentials: map[int64]*sdk.ConnectionCredentials{
				42: {ConnectionID: 42, Slug: "anthropic-api", Fields: map[string]string{"api_key": "sk-ant-test"}},
			},
		}),
	)
	app := &App{}
	if err := app.OnMount(ctx); err != nil {
		t.Fatalf("mount: %v", err)
	}

	cfg, err := providerConfigFor(ctx, "proj-test", "anthropic")
	if err != nil {
		t.Fatalf("provider config: %v", err)
	}
	if cfg.AuthMode != "customer_owned" || cfg.ConnectionID != 42 || cfg.Source != "bound_integration" {
		t.Fatalf("cfg = %+v", cfg)
	}
	rows, err := providerConfigsList(ctx, "proj-test")
	if err != nil {
		t.Fatalf("provider list: %v", err)
	}
	found := false
	for _, row := range rows {
		if row.Provider == "anthropic" && row.ConnectionID == 42 {
			found = true
		}
	}
	if !found {
		t.Fatalf("bound anthropic provider not listed: %+v", rows)
	}
}

func TestBoundOpenCodeGoIntegrationSupportsChatAndModelSync(t *testing.T) {
	var chatCalls atomic.Int64
	var messageCalls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk-opencode-test" {
			t.Errorf("authorization=%q", r.Header.Get("Authorization"))
		}
		switch r.URL.Path {
		case "/models":
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"kimi-k2.6","name":"Kimi K2.6"},{"id":"glm-5.2","name":"GLM-5.2"},{"id":"qwen3.7-plus","name":"Qwen 3.7 Plus"}]}`))
		case "/chat/completions":
			chatCalls.Add(1)
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["model"] != "kimi-k2.6" {
				t.Errorf("upstream model=%v", body["model"])
			}
			_, _ = w.Write([]byte(`{"id":"oc_test","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`))
		case "/messages":
			messageCalls.Add(1)
			if r.Header.Get("X-Api-Key") != "sk-opencode-test" {
				t.Errorf("x-api-key=%q", r.Header.Get("X-Api-Key"))
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["model"] != "qwen3.7-plus" {
				t.Errorf("message model=%v", body["model"])
			}
			_, _ = w.Write([]byte(`{"id":"oc_message","type":"message","role":"assistant","model":"qwen3.7-plus","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":4,"output_tokens":2}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	platform := &llmPlatformStub{
		identity:   &sdk.InstallIdentity{Bindings: map[string]any{"opencode_go_provider": float64(77)}},
		connection: &sdk.PlatformConnection{ID: 77, AppSlug: "opencode-go", Status: "active", ProjectID: "proj-test"},
		credentials: map[int64]*sdk.ConnectionCredentials{
			77: {ConnectionID: 77, Slug: "opencode-go", Fields: map[string]string{"api_key": "sk-opencode-test"}},
		},
	}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("proj-test"), tk.WithPlatform(platform))
	app := &App{httpClient: upstream.Client()}
	if err := app.OnMount(ctx); err != nil {
		t.Fatal(err)
	}
	cfg, err := providerConfigFor(ctx, "proj-test", "opencode-go")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Source != "bound_integration" || cfg.BaseURL != "https://opencode.ai/zen/go/v1" || cfg.ConnectionID != 77 {
		t.Fatalf("bound config=%+v", cfg)
	}
	if _, err := dbProviderConfigUpsert(ctx.AppDB(), "proj-test", map[string]any{
		"provider": "opencode-go", "base_url": upstream.URL, "auth_mode": "customer_owned", "connection_id": 77,
	}); err != nil {
		t.Fatal(err)
	}
	results := app.syncProviderModels(ctx, "proj-test", "opencode-go")
	if len(results) != 1 || results[0].Status != "ok" || results[0].ModelCount != 3 {
		t.Fatalf("sync results=%+v", results)
	}
	cfg, err = providerConfigFor(ctx, "proj-test", "opencode-go")
	if err != nil {
		t.Fatal(err)
	}
	key, err := resolveProviderKey(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	result, err := app.callProvider(context.Background(), cfg, key, map[string]any{
		"model": "opencode-go/kimi-k2.6", "messages": []any{map[string]any{"role": "user", "content": "hello"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if chatCalls.Load() != 1 || !strings.Contains(string(result.Body), `"id":"oc_test"`) {
		t.Fatalf("calls=%d result=%s", chatCalls.Load(), result.Body)
	}
	result, err = app.callProvider(context.Background(), cfg, key, map[string]any{
		"model": "opencode-go/qwen3.7-plus", "messages": []any{map[string]any{"role": "user", "content": "hello"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if messageCalls.Load() != 1 || !strings.Contains(string(result.Body), `"id":"oc_message"`) {
		t.Fatalf("message calls=%d result=%s", messageCalls.Load(), result.Body)
	}
	if defaultProviderKeyRef("opencode-go") != "opencode_go_api_key" {
		t.Fatalf("key ref=%s", defaultProviderKeyRef("opencode-go"))
	}
}

func TestBoundOpenAICodexIntegrationSupportsChatAndAccountModelSync(t *testing.T) {
	var catalogCalls atomic.Int64
	catalog := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		catalogCalls.Add(1)
		if r.URL.Path != "/models" || r.URL.Query().Get("client_version") != openAICodexCatalogClientVersion {
			t.Errorf("catalog URL=%s", r.URL.String())
		}
		if r.Header.Get("Authorization") != "Bearer codex-access" || r.Header.Get("ChatGPT-Account-ID") != "acct-1" {
			t.Errorf("catalog auth=%q account=%q", r.Header.Get("Authorization"), r.Header.Get("ChatGPT-Account-ID"))
		}
		_, _ = w.Write([]byte(`{"models":[
			{"slug":"gpt-5.6-terra","display_name":"GPT-5.6 Terra","visibility":"list","priority":1,"context_window":200000,"input_modalities":["text","image"]},
			{"slug":"gpt-5.5","display_name":"GPT-5.5","visibility":"list","priority":2,"context_window":128000,"input_modalities":["text"]},
			{"slug":"gpt-5.6-luna","display_name":"GPT-5.6 Luna","visibility":"list","priority":0},
			{"slug":"internal-model","display_name":"Internal","visibility":"hidden","priority":0}
		]}`))
	}))
	defer catalog.Close()

	var executedConnection int64
	var executedTool string
	var executedInput map[string]any
	platform := &llmPlatformStub{
		identity:   &sdk.InstallIdentity{Bindings: map[string]any{"openai_codex_provider": float64(88)}},
		connection: &sdk.PlatformConnection{ID: 88, AppSlug: "openai-codex", Status: "active", ProjectID: "proj-test"},
		credentials: map[int64]*sdk.ConnectionCredentials{
			88: {ConnectionID: 88, Slug: "openai-codex", Fields: map[string]string{"access_token": "codex-access", "account_id": "acct-1"}},
		},
		executeIntegration: func(connectionID int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
			executedConnection = connectionID
			executedTool = tool
			executedInput = cloneMap(input)
			return &sdk.ExecuteResult{Success: true, Status: http.StatusOK, Data: json.RawMessage(`{"id":"codex-response","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":6,"completion_tokens":2}}`)}, nil
		},
	}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("proj-test"), tk.WithPlatform(platform))
	app := &App{httpClient: catalog.Client()}
	if err := app.OnMount(ctx); err != nil {
		t.Fatal(err)
	}
	cfg, err := providerConfigFor(ctx, "proj-test", "openai-codex")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Source != "bound_integration" || cfg.ConnectionID != 88 || cfg.BaseURL != "https://chatgpt.com/backend-api/codex" {
		t.Fatalf("bound config=%+v", cfg)
	}
	if _, err := dbProviderConfigUpsert(ctx.AppDB(), "proj-test", map[string]any{
		"provider": "openai-codex", "base_url": catalog.URL, "auth_mode": "customer_owned", "connection_id": 88,
	}); err != nil {
		t.Fatal(err)
	}
	results := app.syncProviderModels(ctx, "proj-test", "openai-codex")
	if len(results) != 1 || results[0].Status != "ok" || results[0].ModelCount != 2 || catalogCalls.Load() != 1 {
		t.Fatalf("sync results=%+v calls=%d", results, catalogCalls.Load())
	}
	var terra *ProviderModel
	for i := range results[0].Models {
		if results[0].Models[i].ModelID == "gpt-5.6-terra" {
			terra = &results[0].Models[i]
		}
	}
	if terra == nil || terra.GatewayModel != "openai-codex/gpt-5.6-terra" || terra.ContextWindow != 200000 {
		t.Fatalf("terra model=%+v", terra)
	}
	result, err := app.executeChat(ctx, &TokenIdentity{ProjectID: "proj-test", SubjectType: "agent", SubjectID: "a"}, map[string]any{
		"model": "openai-codex/gpt-5.6-terra", "messages": []any{map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "text", "text": "hello"}, map[string]any{"type": "input_image", "image_url": "https://images.example/codex.png"},
		}}}, "max_tokens": 128, "_llm_request_id": "codex-chat",
	})
	if err != nil {
		t.Fatal(err)
	}
	if executedConnection != 88 || executedTool != "chat_completion" || executedInput["model"] != "gpt-5.6-terra" || intArg(executedInput, "max_tokens", 0) != 128 {
		t.Fatalf("execute connection=%d tool=%q input=%+v", executedConnection, executedTool, executedInput)
	}
	messages, _ := executedInput["messages"].([]any)
	message, _ := messages[0].(map[string]any)
	content, _ := message["content"].([]any)
	image, _ := content[1].(map[string]any)
	if image["type"] != "image_url" {
		t.Fatalf("codex image content=%+v", content)
	}
	if result.RequestTokens != 6 || result.ResponseTokens != 2 || result.RequestID != "codex-response" {
		t.Fatalf("result=%+v", result)
	}
	usage, err := dbUsageEventByRequestID(ctx.AppDB(), "proj-test", "agent", "a", "codex-chat")
	if err != nil || usage.Provider != "openai-codex" || usage.ProviderCostStatus != "unpriced" {
		t.Fatalf("usage=%+v err=%v", usage, err)
	}
}

func TestProviderConfigValidatesCustomerOwnedAuth(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("proj-test"))
	app := &App{}
	if err := app.OnMount(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := dbProviderConfigUpsert(ctx.AppDB(), "proj-test", map[string]any{
		"provider": "anthropic", "auth_mode": "customer_owned",
	}); err == nil {
		t.Fatal("expected customer_owned provider without connection_id to fail")
	}
	if _, err := dbProviderConfigUpsert(ctx.AppDB(), "proj-test", map[string]any{
		"provider": "anthropic", "auth_mode": "invented", "base_url": "https://example.com/v1",
	}); err == nil {
		t.Fatal("expected unsupported auth mode to fail")
	}
}

func TestCustomerOwnedProviderRejectsForeignConnection(t *testing.T) {
	platform := &llmPlatformStub{
		connection: &sdk.PlatformConnection{ID: 42, AppSlug: "openai-api", Status: "connected", ProjectID: "other-project"},
		credentials: map[int64]*sdk.ConnectionCredentials{
			42: {ConnectionID: 42, Slug: "openai-api", Fields: map[string]string{"api_key": "secret"}},
		},
	}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("proj-test"), tk.WithPlatform(platform))
	app := &App{}
	if err := app.OnMount(ctx); err != nil {
		t.Fatal(err)
	}
	cfg := &ProviderConfig{ProjectID: "proj-test", Provider: "anthropic", AuthMode: "customer_owned", ConnectionID: 42}
	if _, err := resolveProviderKey(ctx, cfg); err == nil || !strings.Contains(err.Error(), "another project") {
		t.Fatalf("expected foreign project rejection, got %v", err)
	}
}

func TestBoundAnthropicIntegrationExecutesWithoutProviderRow(t *testing.T) {
	var gotKey, gotModel string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/messages" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		gotKey = r.Header.Get("X-Api-Key")
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode upstream request: %v", err)
		}
		gotModel, _ = body["model"].(string)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"msg_test",
			"type":"message",
			"role":"assistant",
			"model":"claude-test",
			"content":[{"type":"text","text":"bound provider works"}],
			"usage":{"input_tokens":5,"output_tokens":4}
		}`))
	}))
	defer upstream.Close()

	ctx := tk.NewAppCtx(t, "apteva.yaml",
		tk.WithProjectID("proj-test"),
		tk.WithPlatform(&llmPlatformStub{
			identity: &sdk.InstallIdentity{Bindings: map[string]any{"anthropic_provider": float64(42)}},
			credentials: map[int64]*sdk.ConnectionCredentials{
				42: {ConnectionID: 42, Slug: "anthropic-api", Fields: map[string]string{"api_key": "sk-ant-test"}},
			},
		}),
	)
	app := &App{httpClient: upstream.Client()}
	if err := app.OnMount(ctx); err != nil {
		t.Fatalf("mount: %v", err)
	}
	if _, err := dbPolicySet(ctx.AppDB(), "proj-test", map[string]any{
		"allowed_models": []any{"anthropic/*"},
	}); err != nil {
		t.Fatalf("policy: %v", err)
	}
	cfg, err := providerConfigFor(ctx, "proj-test", "anthropic")
	if err != nil {
		t.Fatalf("provider config: %v", err)
	}
	cfg.BaseURL = upstream.URL

	key, err := resolveProviderKey(ctx, cfg)
	if err != nil {
		t.Fatalf("provider key: %v", err)
	}
	res, err := app.callProvider(context.Background(), cfg, key, map[string]any{
		"model": "anthropic/claude-test",
		"messages": []any{
			map[string]any{"role": "user", "content": "hello"},
		},
		"max_tokens": 64,
	})
	if err != nil {
		t.Fatalf("call provider: %v", err)
	}
	if gotKey != "sk-ant-test" {
		t.Fatalf("got key %q", gotKey)
	}
	if gotModel != "claude-test" {
		t.Fatalf("got model %q", gotModel)
	}
	if res.RequestTokens != 5 || res.ResponseTokens != 4 {
		t.Fatalf("usage tokens = %d/%d", res.RequestTokens, res.ResponseTokens)
	}
}

func TestProviderModelSyncCachesOpenAICompatibleModels(t *testing.T) {
	var gotAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"object":"list",
			"data":[
				{"id":"model-a","object":"model","owned_by":"test"},
				{"id":"model-b","object":"model","owned_by":"test","context_window":8192}
			]
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

	results := app.syncProviderModels(ctx, "proj-test", "openai")
	if len(results) != 1 || results[0].Status != "ok" || results[0].ModelCount != 2 {
		t.Fatalf("sync results = %+v", results)
	}
	if gotAuth != "Bearer sk-test" {
		t.Fatalf("upstream auth = %q", gotAuth)
	}
	rows, err := dbProviderModelsList(ctx.AppDB(), providerModelFilter{ProjectID: "proj-test", Provider: "openai", Status: "active"})
	if err != nil {
		t.Fatalf("models: %v", err)
	}
	if len(rows) != 2 || rows[0].GatewayModel != "openai/model-a" || rows[1].ContextWindow != 8192 {
		t.Fatalf("models = %+v", rows)
	}
}

func TestV1ModelsReturnsDiscoveredModels(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("proj-test"))
	app := &App{}
	if err := app.OnMount(ctx); err != nil {
		t.Fatalf("mount: %v", err)
	}
	if err := dbProviderModelsReplace(ctx.AppDB(), "proj-test", "anthropic", []ProviderModel{
		{Provider: "anthropic", ModelID: "claude-test", DisplayName: "Claude Test"},
	}); err != nil {
		t.Fatalf("replace models: %v", err)
	}
	if _, err := dbPolicySet(ctx.AppDB(), "proj-test", map[string]any{"allowed_models": []any{"anthropic/*"}}); err != nil {
		t.Fatal(err)
	}
	tok, err := createToken(ctx.AppDB(), map[string]any{
		"project_id":   "proj-test",
		"subject_type": "agent",
		"subject_id":   "agent-1",
	})
	if err != nil {
		t.Fatalf("token: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+tok["token"].(string))
	rec := httptest.NewRecorder()
	app.handleV1(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Data []struct {
			ID      string `json:"id"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Data) != 1 || body.Data[0].ID != "anthropic/claude-test" || body.Data[0].OwnedBy != "anthropic" {
		t.Fatalf("models response = %+v", body.Data)
	}
}

func TestProviderModelSyncFollowsPagination(t *testing.T) {
	var pages atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pages.Add(1)
		if r.URL.Query().Get("after_id") == "model-a" {
			_, _ = w.Write([]byte(`{"data":[{"id":"model-b"}],"has_more":false,"last_id":"model-b"}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"model-a"}],"has_more":true,"last_id":"model-a"}`))
	}))
	defer upstream.Close()
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("proj-test"), tk.WithConfig(map[string]string{"openai_api_key": "test"}))
	app := &App{httpClient: upstream.Client()}
	if err := app.OnMount(ctx); err != nil {
		t.Fatal(err)
	}
	_, _ = dbProviderConfigUpsert(ctx.AppDB(), "proj-test", map[string]any{"provider": "openai", "base_url": upstream.URL})
	results := app.syncProviderModels(ctx, "proj-test", "openai")
	if len(results) != 1 || results[0].ModelCount != 2 || pages.Load() != 2 {
		t.Fatalf("results=%+v pages=%d", results, pages.Load())
	}
}

func TestGatewaySchemaRecoversDuplicateLegacyRequestIDs(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	schema, err := os.ReadFile("migrations/001_init.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(schema)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO usage_events(project_id,subject_type,subject_id,request_id,period,status)
		VALUES ('p','tenant','a','same','2026-07','completed'),('p','tenant','a','same','2026-07','completed')`); err != nil {
		t.Fatal(err)
	}
	if err := ensureGatewaySchema(db); err != nil {
		t.Fatalf("repair: %v", err)
	}
	if err := ensureGatewaySchema(db); err != nil {
		t.Fatalf("idempotent repair: %v", err)
	}
	var rows, requestIDs int
	if err := db.QueryRow(`SELECT COUNT(*), COUNT(DISTINCT request_id) FROM usage_events`).Scan(&rows, &requestIDs); err != nil {
		t.Fatal(err)
	}
	if rows != 2 || requestIDs != 2 {
		t.Fatalf("rows=%d request_ids=%d", rows, requestIDs)
	}
	hasSubject, err := txTableHasColumn(db, "policies", "subject_type")
	if err != nil || !hasSubject {
		t.Fatalf("subject policy schema missing: %v", err)
	}
	hasTokenID, err := txTableHasColumn(db, "usage_events", "token_id")
	if err != nil || !hasTokenID {
		t.Fatalf("usage token_id missing: %v", err)
	}
}

func TestGatewaySchemaRecoversInterruptedPolicyRename(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	schema, err := os.ReadFile("migrations/001_init.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(schema)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO policies(project_id, allowed_models_json) VALUES ('p', '["anthropic/*"]')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`ALTER TABLE policies RENAME TO policies_v02`); err != nil {
		t.Fatal(err)
	}
	if err := ensureGatewaySchema(db); err != nil {
		t.Fatalf("repair interrupted rename: %v", err)
	}
	var allowed string
	if err := db.QueryRow(`SELECT allowed_models_json FROM policies WHERE project_id='p' AND subject_type='' AND subject_id=''`).Scan(&allowed); err != nil {
		t.Fatal(err)
	}
	if allowed != `["anthropic/*"]` {
		t.Fatalf("allowed_models_json=%s", allowed)
	}
	legacyExists, err := txTableExists(db, "policies_v02")
	if err != nil || legacyExists {
		t.Fatalf("legacy table still present=%v err=%v", legacyExists, err)
	}
}

func TestV1EnforcesTokenScopesAndOwnUsage(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("proj-test"))
	app := &App{}
	if err := app.OnMount(ctx); err != nil {
		t.Fatal(err)
	}
	modelsOnly, err := createToken(ctx.AppDB(), map[string]any{
		"project_id": "proj-test", "subject_type": "tenant", "subject_id": "a", "scopes": []any{"models"},
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"openai/test","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+modelsOnly["token"].(string))
	rec := httptest.NewRecorder()
	app.handleV1(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("chat scope status=%d body=%s", rec.Code, rec.Body.String())
	}

	usageToken, err := createToken(ctx.AppDB(), map[string]any{
		"project_id": "proj-test", "subject_type": "tenant", "subject_id": "a", "scopes": []any{"usage"},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _, _ = dbUsageRecord(ctx.AppDB(), &TokenIdentity{ProjectID: "proj-test", SubjectType: "tenant", SubjectID: "a"}, "openai", "openai/a", 2, 1, 0, "completed", "a-1", "")
	_, _, _ = dbUsageRecord(ctx.AppDB(), &TokenIdentity{ProjectID: "proj-test", SubjectType: "tenant", SubjectID: "b"}, "openai", "openai/b", 20, 10, 0, "completed", "b-1", "")
	req = httptest.NewRequest(http.MethodGet, "/v1/usage", nil)
	req.Header.Set("Authorization", "Bearer "+usageToken["token"].(string))
	rec = httptest.NewRecorder()
	app.handleV1(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("usage status=%d body=%s", rec.Code, rec.Body.String())
	}
	var summary UsageSummary
	if err := json.Unmarshal(rec.Body.Bytes(), &summary); err != nil {
		t.Fatal(err)
	}
	if summary.Requests != 1 || summary.SubjectID != "a" {
		t.Fatalf("usage leaked across subjects: %+v", summary)
	}
	req = httptest.NewRequest(http.MethodGet, "/v1/usage?subject_id=b", nil)
	req.Header.Set("Authorization", "Bearer "+usageToken["token"].(string))
	rec = httptest.NewRecorder()
	app.handleV1(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-subject usage status=%d", rec.Code)
	}
}

func TestDuplicateRequestIsRejectedBeforeProviderCall(t *testing.T) {
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"one","choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":2,"completion_tokens":1}}`))
	}))
	defer upstream.Close()
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("proj-test"), tk.WithConfig(map[string]string{"openai_api_key": "test"}))
	app := &App{httpClient: upstream.Client()}
	if err := app.OnMount(ctx); err != nil {
		t.Fatal(err)
	}
	_, _ = dbProviderConfigUpsert(ctx.AppDB(), "proj-test", map[string]any{"provider": "openai", "base_url": upstream.URL})
	token, _ := createToken(ctx.AppDB(), map[string]any{"project_id": "proj-test", "subject_type": "agent", "subject_id": "a"})
	for i, want := range []int{http.StatusOK, http.StatusConflict} {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"openai/test","messages":[{"role":"user","content":"hi"}]}`))
		req.Header.Set("Authorization", "Bearer "+token["token"].(string))
		req.Header.Set("Idempotency-Key", "same-request")
		rec := httptest.NewRecorder()
		app.handleV1(rec, req)
		if rec.Code != want {
			t.Fatalf("attempt %d status=%d want=%d body=%s", i, rec.Code, want, rec.Body.String())
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("provider calls=%d", calls.Load())
	}
}

func TestConcurrentRequestLimitReservesCapacity(t *testing.T) {
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer upstream.Close()
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("proj-test"), tk.WithConfig(map[string]string{"openai_api_key": "test"}))
	app := &App{httpClient: upstream.Client()}
	if err := app.OnMount(ctx); err != nil {
		t.Fatal(err)
	}
	_, _ = dbProviderConfigUpsert(ctx.AppDB(), "proj-test", map[string]any{"provider": "openai", "base_url": upstream.URL})
	_, _ = dbPolicySet(ctx.AppDB(), "proj-test", map[string]any{"limits": map[string]any{"monthly_request_limit": 1}})
	token, _ := createToken(ctx.AppDB(), map[string]any{"project_id": "proj-test", "subject_type": "agent", "subject_id": "a"})
	var wg sync.WaitGroup
	statuses := make(chan int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"openai/test","messages":[{"role":"user","content":"hi"}]}`))
			req.Header.Set("Authorization", "Bearer "+token["token"].(string))
			req.Header.Set("Idempotency-Key", string(rune('a'+id)))
			rec := httptest.NewRecorder()
			app.handleV1(rec, req)
			statuses <- rec.Code
		}(i)
	}
	wg.Wait()
	close(statuses)
	seen := map[int]int{}
	for status := range statuses {
		seen[status]++
	}
	if seen[http.StatusOK] != 1 || seen[http.StatusForbidden] != 1 || calls.Load() != 1 {
		t.Fatalf("statuses=%v provider_calls=%d", seen, calls.Load())
	}
}

func TestOutputLimitAndImplicitMaxTokens(t *testing.T) {
	var gotMax int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotMax = intArg(body, "max_tokens", 0)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer upstream.Close()
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("proj-test"), tk.WithConfig(map[string]string{"openai_api_key": "test"}))
	app := &App{httpClient: upstream.Client()}
	if err := app.OnMount(ctx); err != nil {
		t.Fatal(err)
	}
	_, _ = dbProviderConfigUpsert(ctx.AppDB(), "proj-test", map[string]any{"provider": "openai", "base_url": upstream.URL})
	_, _ = dbPolicySet(ctx.AppDB(), "proj-test", map[string]any{"limits": map[string]any{"monthly_output_token_limit": 10, "max_tokens_per_request": 3}})
	_, err := app.executeChat(ctx, &TokenIdentity{ProjectID: "proj-test", SubjectType: "agent", SubjectID: "a"}, map[string]any{
		"model": "openai/test", "messages": []any{map[string]any{"role": "user", "content": "hi"}}, "_llm_request_id": "implicit-max",
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotMax != 3 {
		t.Fatalf("upstream max_tokens=%d", gotMax)
	}
	_, _ = dbPolicySet(ctx.AppDB(), "proj-test", map[string]any{"limits": map[string]any{"monthly_output_token_limit": 3, "max_tokens_per_request": 3}})
	_, err = app.executeChat(ctx, &TokenIdentity{ProjectID: "proj-test", SubjectType: "agent", SubjectID: "a"}, map[string]any{
		"model": "openai/test", "messages": []any{map[string]any{"role": "user", "content": "hi"}}, "max_tokens": 3, "_llm_request_id": "over-output",
	})
	if err == nil || !strings.Contains(err.Error(), "output token limit") {
		t.Fatalf("expected output limit error, got %v", err)
	}
}

func TestDisabledProjectProviderDoesNotFallBackToIntegration(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("proj-test"), tk.WithPlatform(&llmPlatformStub{
		identity:    &sdk.InstallIdentity{Bindings: map[string]any{"anthropic_provider": float64(42)}},
		credentials: map[int64]*sdk.ConnectionCredentials{42: {ConnectionID: 42, Slug: "anthropic-api", Fields: map[string]string{"api_key": "test"}}},
	}))
	app := &App{}
	if err := app.OnMount(ctx); err != nil {
		t.Fatal(err)
	}
	_, err := dbProviderConfigUpsert(ctx.AppDB(), "proj-test", map[string]any{"provider": "anthropic", "base_url": "https://api.anthropic.com/v1", "enabled": false})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := providerConfigFor(ctx, "proj-test", "anthropic"); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("expected disabled provider, got %v", err)
	}
}

func TestFailoverUsesConfiguredFallbackRoute(t *testing.T) {
	var primaryCalls, fallbackCalls atomic.Int64
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		primaryCalls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"message":"unavailable"}}`))
	}))
	defer primary.Close()
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fallbackCalls.Add(1)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"fallback"}}],"usage":{"prompt_tokens":2,"completion_tokens":1}}`))
	}))
	defer fallback.Close()
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("proj-test"), tk.WithConfig(map[string]string{"openai_api_key": "a", "openrouter_api_key": "b"}))
	app := &App{}
	if err := app.OnMount(ctx); err != nil {
		t.Fatal(err)
	}
	_, _ = dbProviderConfigUpsert(ctx.AppDB(), "proj-test", map[string]any{"provider": "openai", "base_url": primary.URL})
	_, _ = dbProviderConfigUpsert(ctx.AppDB(), "proj-test", map[string]any{"provider": "openrouter", "base_url": fallback.URL})
	_, err := dbPolicySet(ctx.AppDB(), "proj-test", map[string]any{
		"allowed_models":  []any{"openai/*", "openrouter/*"},
		"fallback_policy": map[string]any{"routes": []any{map[string]any{"provider": "openrouter", "model": "openrouter/vendor/model"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = app.executeChat(ctx, &TokenIdentity{ProjectID: "proj-test", SubjectType: "agent", SubjectID: "a"}, map[string]any{
		"model": "openai/model", "messages": []any{map[string]any{"role": "user", "content": "hi"}}, "_llm_request_id": "failover",
	})
	if err != nil {
		t.Fatal(err)
	}
	if primaryCalls.Load() != 1 || fallbackCalls.Load() != 1 {
		t.Fatalf("calls primary=%d fallback=%d", primaryCalls.Load(), fallbackCalls.Load())
	}
	event, err := dbUsageEventByRequestID(ctx.AppDB(), "proj-test", "agent", "a", "failover")
	if err != nil || event.Provider != "openrouter" || event.Model != "openrouter/vendor/model" {
		t.Fatalf("usage event=%+v err=%v", event, err)
	}
}

func TestMalformedSuccessfulProviderResponseCanFailOver(t *testing.T) {
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"not":"a completion"}`))
	}))
	defer primary.Close()
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":"fallback","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1}}`))
	}))
	defer fallback.Close()
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("proj-test"), tk.WithConfig(map[string]string{
		"openai_api_key": "test", "openrouter_api_key": "test",
	}))
	app := &App{httpClient: primary.Client()}
	if err := app.OnMount(ctx); err != nil {
		t.Fatal(err)
	}
	_, _ = dbProviderConfigUpsert(ctx.AppDB(), "proj-test", map[string]any{"provider": "openai", "base_url": primary.URL})
	_, _ = dbProviderConfigUpsert(ctx.AppDB(), "proj-test", map[string]any{"provider": "openrouter", "base_url": fallback.URL})
	_, _ = dbPolicySet(ctx.AppDB(), "proj-test", map[string]any{
		"fallback_policy": map[string]any{"routes": []any{map[string]any{"provider": "openrouter", "model": "openrouter/fallback"}}},
	})
	result, err := app.executeChat(ctx, &TokenIdentity{ProjectID: "proj-test", SubjectType: "agent", SubjectID: "a"}, map[string]any{
		"model": "openai/primary", "messages": []any{map[string]any{"role": "user", "content": "hi"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(result.Body), `"id":"fallback"`) {
		t.Fatalf("result=%s", result.Body)
	}
}

func TestAnthropicTranslationPreservesToolWorkflow(t *testing.T) {
	req, err := openAIChatToAnthropic("anthropic", map[string]any{
		"model": "anthropic/claude-test", "max_tokens": 100,
		"messages": []any{
			map[string]any{"role": "user", "content": "weather"},
			map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{map[string]any{"id": "call-1", "type": "function", "function": map[string]any{"name": "weather", "arguments": `{"city":"Paris"}`}}}},
			map[string]any{"role": "tool", "tool_call_id": "call-1", "content": `{"temp":20}`},
		},
		"tools":       []any{map[string]any{"type": "function", "function": map[string]any{"name": "weather", "description": "Get weather", "parameters": map[string]any{"type": "object"}}}},
		"tool_choice": map[string]any{"type": "function", "function": map[string]any{"name": "weather"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(req["tools"].([]any)) != 1 || req["tool_choice"].(map[string]any)["type"] != "tool" {
		t.Fatalf("anthropic request=%+v", req)
	}
	messages := req["messages"].([]map[string]any)
	if len(messages) != 3 {
		t.Fatalf("messages=%+v", messages)
	}
	out, _, _, err := anthropicToOpenAI([]byte(`{"id":"msg","model":"claude-test","stop_reason":"tool_use","content":[{"type":"tool_use","id":"call-2","name":"weather","input":{"city":"Rome"}}],"usage":{"input_tokens":5,"output_tokens":3}}`), "anthropic/claude-test")
	if err != nil {
		t.Fatal(err)
	}
	var response map[string]any
	_ = json.Unmarshal(out, &response)
	choice := response["choices"].([]any)[0].(map[string]any)
	message := choice["message"].(map[string]any)
	if choice["finish_reason"] != "tool_calls" || len(message["tool_calls"].([]any)) != 1 {
		t.Fatalf("openai response=%s", out)
	}
}

func TestChatStreamingReturnsCompatibleSSE(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if boolArg(body, "stream") {
			t.Error("gateway should request an accountable non-streaming provider response")
		}
		_, _ = w.Write([]byte(`{"id":"chat-1","model":"test","choices":[{"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1}}`))
	}))
	defer upstream.Close()
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("proj-test"), tk.WithConfig(map[string]string{"openai_api_key": "test"}))
	app := &App{httpClient: upstream.Client()}
	if err := app.OnMount(ctx); err != nil {
		t.Fatal(err)
	}
	_, _ = dbProviderConfigUpsert(ctx.AppDB(), "proj-test", map[string]any{"provider": "openai", "base_url": upstream.URL})
	token, _ := createToken(ctx.AppDB(), map[string]any{"project_id": "proj-test", "subject_type": "agent", "subject_id": "a"})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"openai/test","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+token["token"].(string))
	rec := httptest.NewRecorder()
	app.handleV1(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Header().Get("Content-Type"), "text/event-stream") || !strings.Contains(rec.Body.String(), "data: [DONE]") || !strings.Contains(rec.Body.String(), "hello") {
		t.Fatalf("status=%d headers=%v body=%s", rec.Code, rec.Header(), rec.Body.String())
	}
}

func TestEmbeddingsRouteForwardsAndRecordsRawUsage(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/embeddings" {
			t.Errorf("path=%s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.1,0.2]}],"model":"embed","usage":{"prompt_tokens":4,"total_tokens":4}}`))
	}))
	defer upstream.Close()
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("proj-test"), tk.WithConfig(map[string]string{"openai_api_key": "test"}))
	app := &App{httpClient: upstream.Client()}
	if err := app.OnMount(ctx); err != nil {
		t.Fatal(err)
	}
	_, _ = dbProviderConfigUpsert(ctx.AppDB(), "proj-test", map[string]any{"provider": "openai", "base_url": upstream.URL})
	token, _ := createToken(ctx.AppDB(), map[string]any{"project_id": "proj-test", "subject_type": "agent", "subject_id": "a", "scopes": []any{"embeddings"}})
	req := httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader(`{"model":"openai/embed","input":"hello"}`))
	req.Header.Set("Authorization", "Bearer "+token["token"].(string))
	rec := httptest.NewRecorder()
	app.handleV1(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	usage, err := dbUsageGet(ctx.AppDB(), usageFilter{ProjectID: "proj-test", SubjectType: "agent", SubjectID: "a"})
	if err != nil || usage.RequestTokens != 4 || usage.ResponseTokens != 0 {
		t.Fatalf("usage=%+v err=%v", usage, err)
	}
}

type llmPlatformStub struct {
	identity           *sdk.InstallIdentity
	credentials        map[int64]*sdk.ConnectionCredentials
	connection         *sdk.PlatformConnection
	executeIntegration func(int64, string, map[string]any) (*sdk.ExecuteResult, error)
}

func (p *llmPlatformStub) GetConnection(id int64) (*sdk.PlatformConnection, error) {
	if p.connection != nil {
		return p.connection, nil
	}
	return &sdk.PlatformConnection{ID: id, AppSlug: "anthropic-api", Status: "connected", ProjectID: "proj-test"}, nil
}
func (p *llmPlatformStub) ListConnections(sdk.ConnectionFilter) ([]sdk.PlatformConnection, error) {
	return nil, nil
}
func (p *llmPlatformStub) GetInstance(int64) (*sdk.PlatformInstance, error) { return nil, nil }
func (p *llmPlatformStub) GetAgent(int64) (*sdk.PlatformAgent, error)       { return nil, nil }
func (p *llmPlatformStub) SendEvent(int64, string) error                    { return nil }
func (p *llmPlatformStub) SendToChannel(string, string, string) error       { return nil }
func (p *llmPlatformStub) WhoAmI() (*sdk.InstallIdentity, error)            { return p.identity, nil }

func (p *llmPlatformStub) ExecuteIntegrationTool(connectionID int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
	if p.executeIntegration != nil {
		return p.executeIntegration(connectionID, tool, input)
	}
	return nil, nil
}
func (p *llmPlatformStub) CallApp(string, string, map[string]any) (json.RawMessage, error) {
	return nil, nil
}
func (p *llmPlatformStub) CallAppResult(string, string, map[string]any, any) error {
	return nil
}
func (p *llmPlatformStub) StartOAuth(sdk.OAuthStartRequest) (*sdk.OAuthStartResult, error) {
	return nil, nil
}
func (p *llmPlatformStub) DisconnectConnection(int64) error { return nil }
func (p *llmPlatformStub) ListOwnedConnections() ([]sdk.PlatformConnection, error) {
	return nil, nil
}
func (p *llmPlatformStub) GetGrants(int64) (*sdk.GrantsResponse, error) { return nil, nil }
func (p *llmPlatformStub) GetConnectionCredentials(id int64) (*sdk.ConnectionCredentials, error) {
	return p.credentials[id], nil
}
func (p *llmPlatformStub) ListProjects() ([]sdk.PlatformProject, error) { return nil, nil }
func (p *llmPlatformStub) SpawnRealtimeThread(sdk.RealtimeSpawnRequest) (*sdk.RealtimeSpawnResult, error) {
	return nil, nil
}
func (p *llmPlatformStub) KillThread(string) error { return nil }
func (p *llmPlatformStub) PlatformInfo() (*sdk.PlatformInfo, error) {
	return &sdk.PlatformInfo{}, nil
}
func (p *llmPlatformStub) ExposeIngress(sdk.IngressExposeRequest) (*sdk.IngressRoute, error) {
	return nil, nil
}
func (p *llmPlatformStub) UnexposeIngress(string) error { return nil }
func (p *llmPlatformStub) ListIngressRoutes() ([]sdk.IngressRoute, error) {
	return nil, nil
}
func (p *llmPlatformStub) ListDomainGrants() ([]sdk.DomainGrant, error) { return nil, nil }
func (p *llmPlatformStub) UpsertDNSRecord(sdk.DNSRecordRequest) (*sdk.DNSRecordResult, error) {
	return nil, nil
}
func (p *llmPlatformStub) DeleteDNSRecord(sdk.DNSRecordRequest) (*sdk.DNSRecordResult, error) {
	return nil, nil
}
func (p *llmPlatformStub) ListEnvironments() ([]sdk.EnvironmentSummary, error) {
	return nil, nil
}
func (p *llmPlatformStub) CreateEnvironment(sdk.EnvironmentCreateRequest) (*sdk.EnvironmentSummary, error) {
	return nil, nil
}
func (p *llmPlatformStub) GetEnvironment(string) (*sdk.EnvironmentSummary, error) {
	return nil, nil
}
func (p *llmPlatformStub) DestroyEnvironment(string) error { return nil }
func (p *llmPlatformStub) SeedEnvironment(string, []sdk.EnvironmentSeedCall, string) ([]json.RawMessage, error) {
	return nil, nil
}
func (p *llmPlatformStub) CallEnvironmentApp(string, string, string, map[string]any) (json.RawMessage, error) {
	return nil, nil
}
func (p *llmPlatformStub) CallEnvironmentAppResult(string, string, string, map[string]any, any) error {
	return nil
}
func (p *llmPlatformStub) SnapshotEnvironment(string, sdk.EnvironmentSnapshotRequest) (*sdk.EnvironmentSnapshot, error) {
	return nil, nil
}
func (p *llmPlatformStub) ListEnvironmentAgents(string) ([]sdk.EnvironmentAgent, error) {
	return nil, nil
}
func (p *llmPlatformStub) SpawnEnvironmentAgent(string, sdk.EnvironmentAgentSpawnRequest) (*sdk.EnvironmentAgent, error) {
	return nil, nil
}
func (p *llmPlatformStub) StopEnvironmentAgent(string, string) error { return nil }
