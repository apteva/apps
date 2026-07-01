package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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

type llmPlatformStub struct {
	identity    *sdk.InstallIdentity
	credentials map[int64]*sdk.ConnectionCredentials
}

func (p *llmPlatformStub) GetConnection(id int64) (*sdk.PlatformConnection, error) {
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
func (p *llmPlatformStub) ExecuteIntegrationTool(int64, string, map[string]any) (*sdk.ExecuteResult, error) {
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
