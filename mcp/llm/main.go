package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

const manifestYAML = `schema: apteva-app/v1
name: llm
display_name: LLM Gateway
version: 0.1.3
description: Managed AI API access for Apteva-hosted apps and agents.
author: Apteva
scopes: [project, global]
icon: https://raw.githubusercontent.com/apteva/apps/main/mcp/llm/icon.svg
requires:
  permissions:
    - db.write.app
    - platform.connections.read
    - platform.connections.read_credentials
  integrations:
    - role: anthropic_provider
      kind: integration
      compatible_slugs: [anthropic-api]
      required: false
    - role: fireworks_provider
      kind: integration
      compatible_slugs: [fireworks]
      required: false
    - role: openai_provider
      kind: integration
      compatible_slugs: [openai-api]
      required: false
    - role: openrouter_provider
      kind: integration
      compatible_slugs: [openrouter]
      required: false
provides:
  http_routes:
    - prefix: /
    - prefix: /v1/
      no_auth: true
  mcp_tools:
    - name: llm_chat_complete
      description: OpenAI-compatible chat completion through the configured LLM gateway.
    - name: llm_models_list
      description: List models allowed by the current project policy.
    - name: llm_usage_get
      description: Return usage for a project, subject, or model in a monthly period.
    - name: llm_provider_configs_list
      description: List configured provider routes.
    - name: llm_provider_configs_upsert
      description: Create or update a provider route.
    - name: llm_policy_get
      description: Return the project policy.
    - name: llm_policy_set
      description: Replace the project policy.
    - name: llm_tokens_create
      description: Issue an OpenAI-compatible bearer token.
  ui_panels:
    - slot: project.page
      label: LLM Gateway
      icon: brain-circuit
      entry: /ui/LLMPanel.mjs
  publishes:
    - name: llm.request.completed
      description: An LLM request completed and usage was recorded.
    - name: llm.request.failed
      description: An LLM request failed.
    - name: llm.usage.recorded
      description: LLM usage was recorded.
    - name: llm.spend_cap.exceeded
      description: A request was denied because the project spend cap was exceeded.
runtime:
  kind: source
  source:
    repo: github.com/apteva/apps
    ref: main
    entry: mcp/llm
  port: 8080
  health_check: /health
db:
  driver: sqlite
  path: /data/llm.db
  migrations: migrations/
config_schema:
  - name: openai_api_key
    type: password
    label: OpenAI API key
  - name: anthropic_api_key
    type: password
    label: Anthropic API key
  - name: fireworks_api_key
    type: password
    label: Fireworks API key
  - name: openrouter_api_key
    type: password
    label: OpenRouter API key
upgrade_policy: auto-patch
`

type App struct {
	httpClient *http.Client
}

var globalCtx *sdk.AppCtx

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		panic("invalid embedded manifest: " + err.Error())
	}
	return *m
}

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx.AppDB() == nil {
		return errors.New("llm requires a db block")
	}
	if a.httpClient == nil {
		a.httpClient = &http.Client{Timeout: 3 * time.Minute}
	}
	globalCtx = ctx
	ctx.Logger().Info("llm gateway mounted", "version", "0.1.3", "scope_project_id", os.Getenv("APTEVA_PROJECT_ID"))
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error       { return nil }
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) Workers() []sdk.Worker             { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/v1/", Handler: a.handleV1, NoAuth: true},
		{Pattern: "/tokens", Handler: a.handleTokens},
		{Pattern: "/providers", Handler: a.handleProviders},
		{Pattern: "/policy", Handler: a.handlePolicy},
		{Pattern: "/usage", Handler: a.handleUsage},
		{Pattern: "/audit", Handler: a.handleAudit},
	}
}

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{Name: "llm_chat_complete", Description: "OpenAI-compatible chat completion through the configured gateway.", InputSchema: schemaObject(map[string]any{
			"project_id": map[string]any{"type": "string"},
			"model":      map[string]any{"type": "string"},
			"messages":   map[string]any{"type": "array"},
			"temperature": map[string]any{
				"type": "number",
			},
			"max_tokens": map[string]any{"type": "integer"},
		}, []string{"model", "messages"}), Handler: a.toolChatComplete},
		{Name: "llm_models_list", Description: "List models allowed by project policy.", InputSchema: schemaObject(map[string]any{"project_id": map[string]any{"type": "string"}}, nil), Handler: a.toolModelsList},
		{Name: "llm_usage_get", Description: "Return usage by period/project/subject/model.", InputSchema: schemaObject(map[string]any{
			"project_id":   map[string]any{"type": "string"},
			"period":       map[string]any{"type": "string"},
			"subject_type": map[string]any{"type": "string"},
			"subject_id":   map[string]any{"type": "string"},
			"model":        map[string]any{"type": "string"},
		}, nil), Handler: a.toolUsageGet},
		{Name: "llm_provider_configs_list", Description: "List provider routes.", InputSchema: schemaObject(map[string]any{"project_id": map[string]any{"type": "string"}}, nil), Handler: a.toolProviderConfigsList},
		{Name: "llm_provider_configs_upsert", Description: "Create or update a provider route.", InputSchema: schemaObject(map[string]any{
			"project_id":    map[string]any{"type": "string"},
			"provider":      map[string]any{"type": "string"},
			"base_url":      map[string]any{"type": "string"},
			"auth_mode":     map[string]any{"type": "string"},
			"connection_id": map[string]any{"type": "integer"},
			"key_ref":       map[string]any{"type": "string"},
			"enabled":       map[string]any{"type": "boolean"},
			"priority":      map[string]any{"type": "integer"},
			"metadata":      map[string]any{"type": "object"},
		}, []string{"provider"}), Handler: a.toolProviderConfigUpsert},
		{Name: "llm_policy_get", Description: "Return project policy.", InputSchema: schemaObject(map[string]any{"project_id": map[string]any{"type": "string"}}, nil), Handler: a.toolPolicyGet},
		{Name: "llm_policy_set", Description: "Replace project policy.", InputSchema: schemaObject(map[string]any{
			"project_id":        map[string]any{"type": "string"},
			"allowed_models":    map[string]any{"type": "array"},
			"blocked_models":    map[string]any{"type": "array"},
			"allowed_providers": map[string]any{"type": "array"},
			"limits":            map[string]any{"type": "object"},
			"fallback_policy":   map[string]any{"type": "object"},
		}, nil), Handler: a.toolPolicySet},
		{Name: "llm_tokens_create", Description: "Issue an OpenAI-compatible bearer token.", InputSchema: schemaObject(map[string]any{
			"project_id":   map[string]any{"type": "string"},
			"subject_type": map[string]any{"type": "string"},
			"subject_id":   map[string]any{"type": "string"},
			"scopes":       map[string]any{"type": "array"},
			"expires_at":   map[string]any{"type": "string"},
		}, nil), Handler: a.toolTokenCreate},
	}
}

func main() { sdk.Run(&App{}) }

type ProviderConfig struct {
	ID           int64           `json:"id"`
	ProjectID    string          `json:"project_id"`
	Provider     string          `json:"provider"`
	BaseURL      string          `json:"base_url"`
	AuthMode     string          `json:"auth_mode"`
	ConnectionID int64           `json:"connection_id,omitempty"`
	KeyRef       string          `json:"key_ref,omitempty"`
	Enabled      bool            `json:"enabled"`
	Priority     int             `json:"priority"`
	Source       string          `json:"source,omitempty"`
	Metadata     json.RawMessage `json:"metadata,omitempty"`
	CreatedAt    string          `json:"created_at,omitempty"`
	UpdatedAt    string          `json:"updated_at,omitempty"`
}

type Policy struct {
	ID               int64           `json:"id,omitempty"`
	ProjectID        string          `json:"project_id"`
	AllowedModels    []string        `json:"allowed_models"`
	BlockedModels    []string        `json:"blocked_models"`
	AllowedProviders []string        `json:"allowed_providers"`
	Limits           Limits          `json:"limits"`
	FallbackPolicy   json.RawMessage `json:"fallback_policy,omitempty"`
}

type Limits struct {
	MonthlyRequestLimit     int64 `json:"monthly_request_limit,omitempty"`
	MonthlyInputTokenLimit  int64 `json:"monthly_input_token_limit,omitempty"`
	MonthlyOutputTokenLimit int64 `json:"monthly_output_token_limit,omitempty"`
	MonthlySpendCapCents    int64 `json:"monthly_spend_cap_cents,omitempty"`
	MaxTokensPerRequest     int64 `json:"max_tokens_per_request,omitempty"`
}

type TokenIdentity struct {
	ID          int64    `json:"id"`
	ProjectID   string   `json:"project_id"`
	SubjectType string   `json:"subject_type"`
	SubjectID   string   `json:"subject_id"`
	Scopes      []string `json:"scopes"`
}

type UsageSummary struct {
	ProjectID          string `json:"project_id"`
	Period             string `json:"period"`
	SubjectType        string `json:"subject_type,omitempty"`
	SubjectID          string `json:"subject_id,omitempty"`
	Model              string `json:"model,omitempty"`
	Requests           int64  `json:"requests"`
	RequestTokens      int64  `json:"request_tokens"`
	ResponseTokens     int64  `json:"response_tokens"`
	TotalTokens        int64  `json:"total_tokens"`
	EstimatedCostCents int64  `json:"estimated_cost_cents"`
}

type chatResult struct {
	Status             int
	Body               []byte
	RequestTokens      int64
	ResponseTokens     int64
	EstimatedCostCents int64
	RequestID          string
}

func (a *App) handleV1(w http.ResponseWriter, r *http.Request) {
	ctx := globalCtx
	if ctx == nil {
		http.Error(w, "llm not mounted", http.StatusServiceUnavailable)
		return
	}
	ident, err := authenticateLLMToken(ctx.AppDB(), r)
	if err != nil {
		writeOpenAIError(w, http.StatusUnauthorized, "invalid_api_key", err.Error())
		return
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/v1/models":
		a.handleV1Models(ctx, ident, w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/v1/usage":
		a.handleV1Usage(ctx, ident, w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/v1/chat/completions":
		a.handleV1Chat(ctx, ident, w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/v1/responses":
		a.handleV1Responses(ctx, ident, w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/v1/embeddings":
		writeOpenAIError(w, http.StatusNotImplemented, "not_implemented", "embeddings are deferred in this LLM Gateway version")
	default:
		writeOpenAIError(w, http.StatusNotFound, "not_found", "unknown LLM endpoint")
	}
}

func (a *App) handleV1Models(ctx *sdk.AppCtx, ident *TokenIdentity, w http.ResponseWriter, _ *http.Request) {
	pol, err := dbPolicyGet(ctx.AppDB(), ident.ProjectID)
	if err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "server_error", err.Error())
		return
	}
	data := []map[string]any{}
	seen := map[string]bool{}
	for _, model := range pol.AllowedModels {
		if model == "" || seen[model] {
			continue
		}
		seen[model] = true
		data = append(data, map[string]any{"id": model, "object": "model", "owned_by": providerFromModel(model)})
	}
	if len(data) == 0 {
		cfgs, _ := providerConfigsList(ctx, ident.ProjectID)
		for _, cfg := range cfgs {
			if !cfg.Enabled || seen[cfg.Provider+"/*"] {
				continue
			}
			seen[cfg.Provider+"/*"] = true
			data = append(data, map[string]any{"id": cfg.Provider + "/*", "object": "model", "owned_by": cfg.Provider})
		}
	}
	writeJSON(w, map[string]any{"object": "list", "data": data})
}

func (a *App) handleV1Usage(ctx *sdk.AppCtx, ident *TokenIdentity, w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	filter := usageFilter{
		ProjectID:   ident.ProjectID,
		Period:      firstNonEmpty(q.Get("period"), currentPeriod()),
		SubjectType: q.Get("subject_type"),
		SubjectID:   q.Get("subject_id"),
		Model:       q.Get("model"),
	}
	usage, err := dbUsageGet(ctx.AppDB(), filter)
	if err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "server_error", err.Error())
		return
	}
	writeJSON(w, usage)
}

func (a *App) handleV1Chat(ctx *sdk.AppCtx, ident *TokenIdentity, w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := json.NewDecoder(io.LimitReader(r.Body, 8<<20)).Decode(&body); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "invalid JSON")
		return
	}
	if boolArg(body, "stream") {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "streaming is deferred in this LLM Gateway version")
		return
	}
	res, err := a.executeChat(ctx, ident, body)
	if err != nil {
		status, typ := errorStatus(err)
		writeOpenAIError(w, status, typ, err.Error())
		return
	}
	copyProviderHeaders(w, res)
	w.WriteHeader(res.Status)
	_, _ = w.Write(res.Body)
}

func (a *App) handleV1Responses(ctx *sdk.AppCtx, ident *TokenIdentity, w http.ResponseWriter, r *http.Request) {
	var req map[string]any
	if err := json.NewDecoder(io.LimitReader(r.Body, 8<<20)).Decode(&req); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "invalid JSON")
		return
	}
	input := req["input"]
	messages := []any{}
	switch v := input.(type) {
	case string:
		messages = append(messages, map[string]any{"role": "user", "content": v})
	case []any:
		messages = v
	default:
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "responses.input must be a string or message array")
		return
	}
	chatReq := map[string]any{
		"model":    req["model"],
		"messages": messages,
	}
	if v, ok := req["temperature"]; ok {
		chatReq["temperature"] = v
	}
	if v, ok := req["max_output_tokens"]; ok {
		chatReq["max_tokens"] = v
	}
	res, err := a.executeChat(ctx, ident, chatReq)
	if err != nil {
		status, typ := errorStatus(err)
		writeOpenAIError(w, status, typ, err.Error())
		return
	}
	var chat struct {
		ID      string `json:"id"`
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage any `json:"usage,omitempty"`
	}
	_ = json.Unmarshal(res.Body, &chat)
	text := ""
	if len(chat.Choices) > 0 {
		text = chat.Choices[0].Message.Content
	}
	writeJSON(w, map[string]any{
		"id":         firstNonEmpty(chat.ID, "resp_"+randomSuffix(12)),
		"object":     "response",
		"created_at": time.Now().Unix(),
		"model":      chat.Model,
		"output": []any{map[string]any{
			"type":    "message",
			"role":    "assistant",
			"content": []any{map[string]any{"type": "output_text", "text": text}},
		}},
		"usage": chat.Usage,
	})
}

func (a *App) executeChat(ctx *sdk.AppCtx, ident *TokenIdentity, body map[string]any) (*chatResult, error) {
	model := strArg(body, "model")
	if model == "" {
		return nil, userError("model is required")
	}
	provider := providerFromModel(model)
	if provider == "" {
		return nil, userError("model must include provider prefix, for example openai/gpt-4.1 or openrouter/anthropic/claude-sonnet-4")
	}
	pol, err := dbPolicyGet(ctx.AppDB(), ident.ProjectID)
	if err != nil {
		return nil, err
	}
	if err := policyAllows(pol, provider, model, int64Arg(body, "max_tokens")); err != nil {
		_ = dbAudit(ctx.AppDB(), ident, "request.denied", provider, model, "denied", err.Error(), nil)
		return nil, err
	}
	period := currentPeriod()
	current, err := dbUsageGet(ctx.AppDB(), usageFilter{ProjectID: ident.ProjectID, Period: period})
	if err != nil {
		return nil, err
	}
	estimatedInput := estimateTokens(body)
	if err := enforcePreflightLimits(pol.Limits, current, estimatedInput); err != nil {
		ctx.EmitWithProject("llm.spend_cap.exceeded", ident.ProjectID, map[string]any{"model": model, "subject_type": ident.SubjectType, "subject_id": ident.SubjectID, "error": err.Error()})
		_ = dbAudit(ctx.AppDB(), ident, "request.denied", provider, model, "denied", err.Error(), nil)
		return nil, err
	}
	cfg, err := providerConfigFor(ctx, ident.ProjectID, provider)
	if err != nil {
		_ = dbAudit(ctx.AppDB(), ident, "provider.missing", provider, model, "error", err.Error(), nil)
		return nil, err
	}
	key, err := resolveProviderKey(ctx, cfg)
	if err != nil {
		_ = dbAudit(ctx.AppDB(), ident, "provider.auth", provider, model, "error", err.Error(), nil)
		return nil, err
	}
	result, err := a.callProvider(context.Background(), cfg, key, body)
	status := "completed"
	if err != nil {
		status = "failed"
		_ = dbUsageRecord(ctx.AppDB(), ident, provider, model, estimatedInput, 0, 0, status, "")
		_ = dbAudit(ctx.AppDB(), ident, "request.failed", provider, model, "error", err.Error(), nil)
		ctx.EmitWithProject("llm.request.failed", ident.ProjectID, map[string]any{"provider": provider, "model": model, "error": err.Error()})
		return nil, err
	}
	if result.RequestTokens == 0 {
		result.RequestTokens = estimatedInput
	}
	if result.Status < 200 || result.Status >= 300 {
		status = "failed"
	}
	if err := dbUsageRecord(ctx.AppDB(), ident, provider, model, result.RequestTokens, result.ResponseTokens, result.EstimatedCostCents, status, result.RequestID); err != nil {
		return nil, err
	}
	_ = dbAudit(ctx.AppDB(), ident, "request."+status, provider, model, status, "", map[string]any{"request_id": result.RequestID})
	event := map[string]any{
		"provider":             provider,
		"model":                model,
		"request_tokens":       result.RequestTokens,
		"response_tokens":      result.ResponseTokens,
		"total_tokens":         result.RequestTokens + result.ResponseTokens,
		"estimated_cost_cents": result.EstimatedCostCents,
		"subject_type":         ident.SubjectType,
		"subject_id":           ident.SubjectID,
		"period":               period,
	}
	ctx.EmitWithProject("llm.usage.recorded", ident.ProjectID, event)
	if status == "completed" {
		ctx.EmitWithProject("llm.request.completed", ident.ProjectID, event)
	}
	return result, nil
}

func (a *App) callProvider(ctx context.Context, cfg *ProviderConfig, apiKey string, body map[string]any) (*chatResult, error) {
	if cfg.Provider == "anthropic" {
		return a.callAnthropic(ctx, cfg, apiKey, body)
	}
	return a.callOpenAICompatible(ctx, cfg, apiKey, body)
}

func (a *App) callOpenAICompatible(ctx context.Context, cfg *ProviderConfig, apiKey string, body map[string]any) (*chatResult, error) {
	outBody := cloneMap(body)
	outBody["model"] = upstreamModel(cfg.Provider, strArg(body, "model"))
	b, _ := json.Marshal(outBody)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(cfg.BaseURL, "/")+"/chat/completions", bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	result := &chatResult{Status: resp.StatusCode, Body: raw, RequestID: resp.Header.Get("X-Request-Id")}
	result.RequestTokens, result.ResponseTokens = parseOpenAIUsage(raw)
	if result.RequestID == "" {
		result.RequestID = "llm_" + randomSuffix(16)
	}
	if resp.StatusCode >= 500 || resp.StatusCode == 429 {
		return result, providerError(resp.StatusCode, string(raw))
	}
	return result, nil
}

func (a *App) callAnthropic(ctx context.Context, cfg *ProviderConfig, apiKey string, body map[string]any) (*chatResult, error) {
	anthropicReq, err := openAIChatToAnthropic(body)
	if err != nil {
		return nil, err
	}
	b, _ := json.Marshal(anthropicReq)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(cfg.BaseURL, "/")+"/messages", bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Api-Key", apiKey)
	req.Header.Set("Anthropic-Version", "2023-06-01")
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if resp.StatusCode >= 500 || resp.StatusCode == 429 {
		return &chatResult{Status: resp.StatusCode, Body: raw, RequestID: resp.Header.Get("Request-Id")}, providerError(resp.StatusCode, string(raw))
	}
	openAI, inTok, outTok := anthropicToOpenAI(raw, strArg(body, "model"))
	result := &chatResult{Status: resp.StatusCode, Body: openAI, RequestTokens: inTok, ResponseTokens: outTok, RequestID: resp.Header.Get("Request-Id")}
	if result.RequestID == "" {
		result.RequestID = "llm_" + randomSuffix(16)
	}
	return result, nil
}

func (a *App) handleTokens(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var args map[string]any
	if err := json.NewDecoder(r.Body).Decode(&args); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	out, err := createToken(globalCtx.AppDB(), args)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, out)
}

func (a *App) handleProviders(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		pid := projectFromRequest(r)
		rows, err := providerConfigsList(globalCtx, pid)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"providers": rows})
	case http.MethodPut, http.MethodPost:
		var args map[string]any
		if err := json.NewDecoder(r.Body).Decode(&args); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		cfg, err := dbProviderConfigUpsert(globalCtx.AppDB(), projectFromArgs(args), args)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]any{"provider": cfg})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (a *App) handlePolicy(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		pol, err := dbPolicyGet(globalCtx.AppDB(), projectFromRequest(r))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"policy": pol})
	case http.MethodPut, http.MethodPost:
		var args map[string]any
		if err := json.NewDecoder(r.Body).Decode(&args); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		pol, err := dbPolicySet(globalCtx.AppDB(), projectFromArgs(args), args)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]any{"policy": pol})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (a *App) handleUsage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	q := r.URL.Query()
	usage, err := dbUsageGet(globalCtx.AppDB(), usageFilter{
		ProjectID:   projectFromRequest(r),
		Period:      firstNonEmpty(q.Get("period"), currentPeriod()),
		SubjectType: q.Get("subject_type"),
		SubjectID:   q.Get("subject_id"),
		Model:       q.Get("model"),
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, usage)
}

func (a *App) handleAudit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	rows, err := dbAuditList(globalCtx.AppDB(), projectFromRequest(r), intArgQuery(r, "limit", 100))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"audit_logs": rows})
}

func (a *App) toolChatComplete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid := projectFromArgs(args)
	ident := &TokenIdentity{ProjectID: pid, SubjectType: firstNonEmpty(strArg(args, "subject_type"), "app"), SubjectID: firstNonEmpty(strArg(args, "subject_id"), "mcp")}
	res, err := a.executeChat(ctx, ident, args)
	if err != nil {
		return nil, err
	}
	var out any
	if err := json.Unmarshal(res.Body, &out); err != nil {
		return map[string]any{"status": res.Status, "body": string(res.Body)}, nil
	}
	return out, nil
}

func (a *App) toolModelsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pol, err := dbPolicyGet(ctx.AppDB(), projectFromArgs(args))
	if err != nil {
		return nil, err
	}
	return map[string]any{"models": pol.AllowedModels, "policy": pol}, nil
}

func (a *App) toolUsageGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	usage, err := dbUsageGet(ctx.AppDB(), usageFilter{
		ProjectID:   projectFromArgs(args),
		Period:      firstNonEmpty(strArg(args, "period"), currentPeriod()),
		SubjectType: strArg(args, "subject_type"),
		SubjectID:   strArg(args, "subject_id"),
		Model:       strArg(args, "model"),
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"usage": usage}, nil
}

func (a *App) toolProviderConfigsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	rows, err := providerConfigsList(ctx, projectFromArgs(args))
	if err != nil {
		return nil, err
	}
	return map[string]any{"providers": rows}, nil
}

func (a *App) toolProviderConfigUpsert(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	cfg, err := dbProviderConfigUpsert(ctx.AppDB(), projectFromArgs(args), args)
	if err != nil {
		return nil, err
	}
	return map[string]any{"provider": cfg}, nil
}

func (a *App) toolPolicyGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pol, err := dbPolicyGet(ctx.AppDB(), projectFromArgs(args))
	if err != nil {
		return nil, err
	}
	return map[string]any{"policy": pol}, nil
}

func (a *App) toolPolicySet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pol, err := dbPolicySet(ctx.AppDB(), projectFromArgs(args), args)
	if err != nil {
		return nil, err
	}
	return map[string]any{"policy": pol}, nil
}

func (a *App) toolTokenCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return createToken(ctx.AppDB(), args)
}

func dbProviderConfigUpsert(db *sql.DB, projectID string, args map[string]any) (*ProviderConfig, error) {
	provider := normalizeProvider(strArg(args, "provider"))
	if provider == "" {
		return nil, errors.New("provider is required")
	}
	baseURL := strings.TrimRight(strArg(args, "base_url"), "/")
	if baseURL == "" {
		baseURL = defaultBaseURL(provider)
	}
	authMode := firstNonEmpty(strArg(args, "auth_mode"), "platform_shared")
	keyRef := strArg(args, "key_ref")
	if keyRef == "" && authMode != "customer_owned" {
		keyRef = provider + "_api_key"
	}
	enabled := true
	if _, ok := args["enabled"]; ok {
		enabled = boolArg(args, "enabled")
	}
	priority := intArg(args, "priority", 100)
	metadata := jsonFromAny(args["metadata"])
	if string(metadata) == "" {
		metadata = json.RawMessage(`{}`)
	}
	_, err := db.Exec(`
		INSERT INTO provider_configs
			(project_id, provider, base_url, auth_mode, connection_id, key_ref, enabled, priority, metadata_json, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(project_id, provider) DO UPDATE SET
			base_url=excluded.base_url,
			auth_mode=excluded.auth_mode,
			connection_id=excluded.connection_id,
			key_ref=excluded.key_ref,
			enabled=excluded.enabled,
			priority=excluded.priority,
			metadata_json=excluded.metadata_json,
			updated_at=CURRENT_TIMESTAMP`,
		projectID, provider, baseURL, authMode, int64Arg(args, "connection_id"), keyRef, boolToInt(enabled), priority, string(metadata))
	if err != nil {
		return nil, err
	}
	return dbProviderConfigFor(db, projectID, provider)
}

func providerConfigFor(ctx *sdk.AppCtx, projectID, provider string) (*ProviderConfig, error) {
	if ctx == nil || ctx.AppDB() == nil {
		return nil, errors.New("llm app context unavailable")
	}
	if cfg, err := dbProviderConfigFor(ctx.AppDB(), projectID, provider); err == nil {
		return cfg, nil
	}
	if cfg := boundProviderConfig(ctx, projectID, provider); cfg != nil {
		return cfg, nil
	}
	return nil, fmt.Errorf("no enabled provider config or bound provider integration for %q", provider)
}

func dbProviderConfigFor(db *sql.DB, projectID, provider string) (*ProviderConfig, error) {
	row := db.QueryRow(`
		SELECT id, project_id, provider, base_url, auth_mode, connection_id, key_ref, enabled, priority, metadata_json,
		       COALESCE(created_at,''), COALESCE(updated_at,'')
		  FROM provider_configs
		 WHERE provider = ? AND enabled = 1 AND (project_id = ? OR project_id = '')
		 ORDER BY CASE WHEN project_id = ? THEN 0 ELSE 1 END, priority ASC, id ASC
		 LIMIT 1`, provider, projectID, projectID)
	cfg, err := scanProviderConfig(row)
	if err != nil {
		return nil, fmt.Errorf("no enabled provider config for %q", provider)
	}
	return cfg, nil
}

func dbProviderConfigsList(db *sql.DB, projectID string) ([]ProviderConfig, error) {
	rows, err := db.Query(`
		SELECT id, project_id, provider, base_url, auth_mode, connection_id, key_ref, enabled, priority, metadata_json,
		       COALESCE(created_at,''), COALESCE(updated_at,'')
		  FROM provider_configs
		 WHERE project_id = ? OR project_id = ''
		 ORDER BY project_id DESC, priority ASC, provider ASC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ProviderConfig{}
	for rows.Next() {
		cfg, err := scanProviderConfig(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *cfg)
	}
	return out, rows.Err()
}

func providerConfigsList(ctx *sdk.AppCtx, projectID string) ([]ProviderConfig, error) {
	rows, err := dbProviderConfigsList(ctx.AppDB(), projectID)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, row := range rows {
		seen[row.Provider] = true
	}
	for provider := range providerIntegrationRoles() {
		if seen[provider] {
			continue
		}
		if cfg := boundProviderConfig(ctx, projectID, provider); cfg != nil {
			rows = append(rows, *cfg)
			seen[provider] = true
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Priority != rows[j].Priority {
			return rows[i].Priority < rows[j].Priority
		}
		return rows[i].Provider < rows[j].Provider
	})
	return rows, nil
}

func boundProviderConfig(ctx *sdk.AppCtx, projectID, provider string) *ProviderConfig {
	role := providerIntegrationRoles()[provider]
	if role == "" || ctx == nil {
		return nil
	}
	bound := ctx.IntegrationFor(role)
	if bound == nil || bound.ConnectionID <= 0 {
		return nil
	}
	return &ProviderConfig{
		ProjectID:    projectID,
		Provider:     provider,
		BaseURL:      defaultBaseURL(provider),
		AuthMode:     "customer_owned",
		ConnectionID: bound.ConnectionID,
		Enabled:      true,
		Priority:     100,
		Source:       "bound_integration",
		Metadata:     json.RawMessage(`{}`),
	}
}

func providerIntegrationRoles() map[string]string {
	return map[string]string{
		"anthropic":  "anthropic_provider",
		"fireworks":  "fireworks_provider",
		"openai":     "openai_provider",
		"openrouter": "openrouter_provider",
	}
}

type providerScanner interface {
	Scan(dest ...any) error
}

func scanProviderConfig(row providerScanner) (*ProviderConfig, error) {
	var cfg ProviderConfig
	var enabled int
	var meta string
	if err := row.Scan(&cfg.ID, &cfg.ProjectID, &cfg.Provider, &cfg.BaseURL, &cfg.AuthMode, &cfg.ConnectionID, &cfg.KeyRef, &enabled, &cfg.Priority, &meta, &cfg.CreatedAt, &cfg.UpdatedAt); err != nil {
		return nil, err
	}
	cfg.Enabled = enabled != 0
	cfg.Source = "configured"
	cfg.Metadata = json.RawMessage(firstNonEmpty(meta, "{}"))
	return &cfg, nil
}

func dbPolicyGet(db *sql.DB, projectID string) (*Policy, error) {
	var (
		pol             Policy
		allowedModels   string
		blockedModels   string
		allowedProvider string
		limits          string
		fallback        string
	)
	err := db.QueryRow(`
		SELECT id, project_id, allowed_models_json, blocked_models_json, allowed_providers_json, limits_json, fallback_policy_json
		  FROM policies WHERE project_id = ?`, projectID).
		Scan(&pol.ID, &pol.ProjectID, &allowedModels, &blockedModels, &allowedProvider, &limits, &fallback)
	if err == sql.ErrNoRows {
		return &Policy{ProjectID: projectID, FallbackPolicy: json.RawMessage(`{}`)}, nil
	}
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(allowedModels), &pol.AllowedModels)
	_ = json.Unmarshal([]byte(blockedModels), &pol.BlockedModels)
	_ = json.Unmarshal([]byte(allowedProvider), &pol.AllowedProviders)
	_ = json.Unmarshal([]byte(limits), &pol.Limits)
	pol.FallbackPolicy = json.RawMessage(firstNonEmpty(fallback, "{}"))
	return &pol, nil
}

func dbPolicySet(db *sql.DB, projectID string, args map[string]any) (*Policy, error) {
	pol := &Policy{
		ProjectID:        projectID,
		AllowedModels:    stringSlice(args["allowed_models"]),
		BlockedModels:    stringSlice(args["blocked_models"]),
		AllowedProviders: stringSlice(args["allowed_providers"]),
		FallbackPolicy:   jsonFromAny(args["fallback_policy"]),
	}
	pol.Limits = limitsFromAny(args["limits"])
	if len(pol.FallbackPolicy) == 0 {
		pol.FallbackPolicy = json.RawMessage(`{}`)
	}
	am, _ := json.Marshal(pol.AllowedModels)
	bm, _ := json.Marshal(pol.BlockedModels)
	ap, _ := json.Marshal(pol.AllowedProviders)
	lim, _ := json.Marshal(pol.Limits)
	_, err := db.Exec(`
		INSERT INTO policies
			(project_id, allowed_models_json, blocked_models_json, allowed_providers_json, limits_json, fallback_policy_json, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(project_id) DO UPDATE SET
			allowed_models_json=excluded.allowed_models_json,
			blocked_models_json=excluded.blocked_models_json,
			allowed_providers_json=excluded.allowed_providers_json,
			limits_json=excluded.limits_json,
			fallback_policy_json=excluded.fallback_policy_json,
			updated_at=CURRENT_TIMESTAMP`,
		projectID, string(am), string(bm), string(ap), string(lim), string(pol.FallbackPolicy))
	if err != nil {
		return nil, err
	}
	return dbPolicyGet(db, projectID)
}

func createToken(db *sql.DB, args map[string]any) (map[string]any, error) {
	projectID := projectFromArgs(args)
	subjectType := firstNonEmpty(strArg(args, "subject_type"), "project")
	subjectID := firstNonEmpty(strArg(args, "subject_id"), projectID)
	scopes := stringSlice(args["scopes"])
	if len(scopes) == 0 {
		scopes = []string{"chat", "models", "usage"}
	}
	token := "llm_" + randomSuffix(32)
	hash := hashToken(token)
	var expires any
	if exp := strArg(args, "expires_at"); exp != "" {
		if _, err := time.Parse(time.RFC3339, exp); err != nil {
			return nil, fmt.Errorf("expires_at must be RFC3339")
		}
		expires = exp
	}
	scopesJSON, _ := json.Marshal(scopes)
	res, err := db.Exec(`INSERT INTO api_tokens (project_id, subject_type, subject_id, token_hash, scopes_json, expires_at) VALUES (?, ?, ?, ?, ?, ?)`,
		projectID, subjectType, subjectID, hash, string(scopesJSON), expires)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return map[string]any{
		"id":           id,
		"token":        token,
		"project_id":   projectID,
		"subject_type": subjectType,
		"subject_id":   subjectID,
		"scopes":       scopes,
		"base_url":     "/api/apps/llm/v1",
	}, nil
}

func authenticateLLMToken(db *sql.DB, r *http.Request) (*TokenIdentity, error) {
	token := bearerToken(r.Header.Get("X-Apteva-Original-Authorization"))
	if token == "" {
		token = bearerToken(r.Header.Get("Authorization"))
	}
	if token == "" {
		token = strings.TrimSpace(r.Header.Get("X-LLM-API-Key"))
	}
	if token == "" {
		return nil, errors.New("missing LLM bearer token")
	}
	var (
		ident      TokenIdentity
		scopesJSON string
		expires    sql.NullString
		revoked    sql.NullString
	)
	err := db.QueryRow(`SELECT id, project_id, subject_type, subject_id, scopes_json, COALESCE(expires_at,''), COALESCE(revoked_at,'') FROM api_tokens WHERE token_hash = ?`,
		hashToken(token)).Scan(&ident.ID, &ident.ProjectID, &ident.SubjectType, &ident.SubjectID, &scopesJSON, &expires, &revoked)
	if err != nil {
		return nil, errors.New("unknown token")
	}
	if revoked.Valid && revoked.String != "" {
		return nil, errors.New("token revoked")
	}
	if expires.Valid && expires.String != "" {
		t, err := time.Parse(time.RFC3339, expires.String)
		if err == nil && time.Now().After(t) {
			return nil, errors.New("token expired")
		}
	}
	_ = json.Unmarshal([]byte(scopesJSON), &ident.Scopes)
	return &ident, nil
}

type usageFilter struct {
	ProjectID   string
	Period      string
	SubjectType string
	SubjectID   string
	Model       string
}

func dbUsageGet(db *sql.DB, f usageFilter) (*UsageSummary, error) {
	if f.Period == "" {
		f.Period = currentPeriod()
	}
	conds := []string{"project_id = ?", "period = ?"}
	args := []any{f.ProjectID, f.Period}
	if f.SubjectType != "" {
		conds = append(conds, "subject_type = ?")
		args = append(args, f.SubjectType)
	}
	if f.SubjectID != "" {
		conds = append(conds, "subject_id = ?")
		args = append(args, f.SubjectID)
	}
	if f.Model != "" {
		conds = append(conds, "model = ?")
		args = append(args, f.Model)
	}
	var out UsageSummary
	out.ProjectID = f.ProjectID
	out.Period = f.Period
	out.SubjectType = f.SubjectType
	out.SubjectID = f.SubjectID
	out.Model = f.Model
	query := `SELECT COUNT(*), COALESCE(SUM(request_tokens),0), COALESCE(SUM(response_tokens),0), COALESCE(SUM(total_tokens),0), COALESCE(SUM(estimated_cost_cents),0) FROM usage_events WHERE ` + strings.Join(conds, " AND ")
	if err := db.QueryRow(query, args...).Scan(&out.Requests, &out.RequestTokens, &out.ResponseTokens, &out.TotalTokens, &out.EstimatedCostCents); err != nil {
		return nil, err
	}
	return &out, nil
}

func dbUsageRecord(db *sql.DB, ident *TokenIdentity, provider, model string, input, output, cost int64, status, requestID string) error {
	_, err := db.Exec(`INSERT INTO usage_events
		(project_id, subject_type, subject_id, provider, model, request_tokens, response_tokens, total_tokens, estimated_cost_cents, status, period, request_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ident.ProjectID, ident.SubjectType, ident.SubjectID, provider, model, input, output, input+output, cost, status, currentPeriod(), requestID)
	return err
}

func dbAudit(db *sql.DB, ident *TokenIdentity, action, provider, model, status, message string, metadata any) error {
	meta := jsonFromAny(metadata)
	if len(meta) == 0 {
		meta = json.RawMessage(`{}`)
	}
	_, err := db.Exec(`INSERT INTO audit_logs (project_id, subject_type, subject_id, action, provider, model, status, message, metadata_json) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ident.ProjectID, ident.SubjectType, ident.SubjectID, action, provider, model, status, message, string(meta))
	return err
}

func dbAuditList(db *sql.DB, projectID string, limit int) ([]map[string]any, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := db.Query(`SELECT id, project_id, subject_type, subject_id, action, provider, model, status, message, metadata_json, COALESCE(created_at,'') FROM audit_logs WHERE project_id = ? ORDER BY id DESC LIMIT ?`, projectID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id int64
		var pid, st, sid, action, provider, model, status, msg, meta, created string
		if err := rows.Scan(&id, &pid, &st, &sid, &action, &provider, &model, &status, &msg, &meta, &created); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"id": id, "project_id": pid, "subject_type": st, "subject_id": sid, "action": action, "provider": provider, "model": model, "status": status, "message": msg, "metadata": rawJSON(meta), "created_at": created})
	}
	return out, rows.Err()
}

func resolveProviderKey(ctx *sdk.AppCtx, cfg *ProviderConfig) (string, error) {
	if cfg.AuthMode == "customer_owned" && cfg.ConnectionID > 0 {
		if ctx.PlatformAPI() == nil {
			return "", errors.New("customer_owned provider requires platform API")
		}
		creds, err := ctx.PlatformAPI().GetConnectionCredentials(cfg.ConnectionID)
		if err != nil {
			return "", err
		}
		for _, k := range []string{"api_key", "token", cfg.Provider + "_api_key", "access_token"} {
			if v := strings.TrimSpace(creds.Fields[k]); v != "" {
				return v, nil
			}
		}
		return "", fmt.Errorf("connection %d has no api_key/token credential field", cfg.ConnectionID)
	}
	keys := []string{cfg.KeyRef}
	if cfg.KeyRef == "" {
		keys = append(keys, cfg.Provider+"_api_key")
	}
	keys = append(keys, strings.ToUpper(cfg.Provider)+"_API_KEY")
	for _, k := range keys {
		if k == "" {
			continue
		}
		if v := strings.TrimSpace(ctx.Config()[k]); v != "" {
			return v, nil
		}
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v, nil
		}
		if v := strings.TrimSpace(os.Getenv(strings.ToUpper(k))); v != "" {
			return v, nil
		}
	}
	return "", fmt.Errorf("missing API key for provider %q (key_ref=%q)", cfg.Provider, cfg.KeyRef)
}

func policyAllows(pol *Policy, provider, model string, maxTokens int64) error {
	if len(pol.AllowedProviders) > 0 && !matchesAny(pol.AllowedProviders, provider) {
		return forbiddenError("provider not allowed by project policy")
	}
	if len(pol.AllowedModels) > 0 && !matchesAny(pol.AllowedModels, model) {
		return forbiddenError("model not allowed by project policy")
	}
	if matchesAny(pol.BlockedModels, model) {
		return forbiddenError("model blocked by project policy")
	}
	if pol.Limits.MaxTokensPerRequest > 0 && maxTokens > pol.Limits.MaxTokensPerRequest {
		return forbiddenError("max_tokens exceeds project policy")
	}
	return nil
}

func enforcePreflightLimits(l Limits, usage *UsageSummary, estimatedInput int64) error {
	if l.MonthlyRequestLimit > 0 && usage.Requests >= l.MonthlyRequestLimit {
		return forbiddenError("monthly request limit exceeded")
	}
	if l.MonthlyInputTokenLimit > 0 && usage.RequestTokens+estimatedInput > l.MonthlyInputTokenLimit {
		return forbiddenError("monthly input token limit exceeded")
	}
	if l.MonthlySpendCapCents > 0 && usage.EstimatedCostCents >= l.MonthlySpendCapCents {
		return forbiddenError("monthly spend cap exceeded")
	}
	return nil
}

func openAIChatToAnthropic(body map[string]any) (map[string]any, error) {
	messagesRaw, ok := body["messages"].([]any)
	if !ok || len(messagesRaw) == 0 {
		return nil, userError("messages must be a non-empty array")
	}
	outMsgs := []map[string]any{}
	system := ""
	for _, raw := range messagesRaw {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		role := strArg(m, "role")
		content := m["content"]
		if role == "system" {
			system = strings.TrimSpace(system + "\n" + contentText(content))
			continue
		}
		if role != "assistant" {
			role = "user"
		}
		outMsgs = append(outMsgs, map[string]any{"role": role, "content": contentText(content)})
	}
	if len(outMsgs) == 0 {
		return nil, userError("messages must contain at least one user or assistant message")
	}
	req := map[string]any{
		"model":      upstreamModel("anthropic", strArg(body, "model")),
		"messages":   outMsgs,
		"max_tokens": intArg(body, "max_tokens", 1024),
	}
	if system != "" {
		req["system"] = system
	}
	if v, ok := body["temperature"]; ok {
		req["temperature"] = v
	}
	return req, nil
}

func anthropicToOpenAI(raw []byte, requestedModel string) ([]byte, int64, int64) {
	var in struct {
		ID      string `json:"id"`
		Model   string `json:"model"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Usage struct {
			InputTokens  int64 `json:"input_tokens"`
			OutputTokens int64 `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return raw, 0, 0
	}
	text := ""
	for _, c := range in.Content {
		if c.Type == "text" {
			text += c.Text
		}
	}
	model := requestedModel
	if model == "" {
		model = "anthropic/" + in.Model
	}
	out := map[string]any{
		"id":      firstNonEmpty(in.ID, "chatcmpl_"+randomSuffix(12)),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []any{map[string]any{
			"index": 0,
			"message": map[string]any{
				"role":    "assistant",
				"content": text,
			},
			"finish_reason": "stop",
		}},
		"usage": map[string]any{
			"prompt_tokens":     in.Usage.InputTokens,
			"completion_tokens": in.Usage.OutputTokens,
			"total_tokens":      in.Usage.InputTokens + in.Usage.OutputTokens,
		},
	}
	b, _ := json.Marshal(out)
	return b, in.Usage.InputTokens, in.Usage.OutputTokens
}

func parseOpenAIUsage(raw []byte) (int64, int64) {
	var out struct {
		Usage struct {
			PromptTokens     int64 `json:"prompt_tokens"`
			CompletionTokens int64 `json:"completion_tokens"`
			InputTokens      int64 `json:"input_tokens"`
			OutputTokens     int64 `json:"output_tokens"`
		} `json:"usage"`
	}
	_ = json.Unmarshal(raw, &out)
	in := out.Usage.PromptTokens
	if in == 0 {
		in = out.Usage.InputTokens
	}
	outTok := out.Usage.CompletionTokens
	if outTok == 0 {
		outTok = out.Usage.OutputTokens
	}
	return in, outTok
}

func copyProviderHeaders(w http.ResponseWriter, res *chatResult) {
	w.Header().Set("Content-Type", "application/json")
	if res.RequestID != "" {
		w.Header().Set("X-Request-Id", res.RequestID)
	}
}

type typedErr struct {
	status int
	typ    string
	msg    string
}

func (e typedErr) Error() string { return e.msg }

func userError(msg string) error {
	return typedErr{status: http.StatusBadRequest, typ: "invalid_request_error", msg: msg}
}
func forbiddenError(msg string) error {
	return typedErr{status: http.StatusForbidden, typ: "policy_error", msg: msg}
}
func providerError(status int, msg string) error {
	if strings.TrimSpace(msg) == "" {
		msg = http.StatusText(status)
	}
	return typedErr{status: status, typ: "provider_error", msg: msg}
}

func errorStatus(err error) (int, string) {
	var te typedErr
	if errors.As(err, &te) {
		return te.status, te.typ
	}
	return http.StatusInternalServerError, "server_error"
}

func writeOpenAIError(w http.ResponseWriter, status int, typ, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": msg, "type": typ}})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func schemaObject(props map[string]any, required []string) map[string]any {
	if props == nil {
		props = map[string]any{}
	}
	out := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

func projectFromRequest(r *http.Request) string {
	if v := strings.TrimSpace(r.URL.Query().Get("project_id")); v != "" {
		return v
	}
	if globalCtx != nil {
		return globalCtx.CurrentProject()
	}
	return ""
}

func projectFromArgs(args map[string]any) string {
	if v := strArg(args, "project_id"); v != "" {
		return v
	}
	if v := strArg(args, "_project_id"); v != "" {
		return v
	}
	if globalCtx != nil {
		return globalCtx.CurrentProject()
	}
	return ""
}

func strArg(args map[string]any, key string) string {
	if args == nil {
		return ""
	}
	switch v := args[key].(type) {
	case string:
		return strings.TrimSpace(v)
	case fmt.Stringer:
		return strings.TrimSpace(v.String())
	default:
		return ""
	}
}

func intArg(args map[string]any, key string, dflt int) int {
	if args == nil {
		return dflt
	}
	switch v := args[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case json.Number:
		n, _ := v.Int64()
		return int(n)
	default:
		return dflt
	}
}

func int64Arg(args map[string]any, key string) int64 {
	return int64(intArg(args, key, 0))
}

func intArgQuery(r *http.Request, key string, dflt int) int {
	if v := r.URL.Query().Get(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return dflt
}

func boolArg(args map[string]any, key string) bool {
	switch v := args[key].(type) {
	case bool:
		return v
	case string:
		return v == "true" || v == "1"
	default:
		return false
	}
}

func stringSlice(v any) []string {
	switch x := v.(type) {
	case []string:
		return x
	case []any:
		out := []string{}
		for _, it := range x {
			if s, ok := it.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
		return out
	default:
		return nil
	}
}

func limitsFromAny(v any) Limits {
	m, ok := v.(map[string]any)
	if !ok {
		return Limits{}
	}
	return Limits{
		MonthlyRequestLimit:     int64Arg(m, "monthly_request_limit"),
		MonthlyInputTokenLimit:  int64Arg(m, "monthly_input_token_limit"),
		MonthlyOutputTokenLimit: int64Arg(m, "monthly_output_token_limit"),
		MonthlySpendCapCents:    int64Arg(m, "monthly_spend_cap_cents"),
		MaxTokensPerRequest:     int64Arg(m, "max_tokens_per_request"),
	}
}

func jsonFromAny(v any) json.RawMessage {
	if v == nil {
		return nil
	}
	if raw, ok := v.(json.RawMessage); ok {
		return raw
	}
	b, _ := json.Marshal(v)
	return b
}

func rawJSON(s string) any {
	var v any
	if json.Unmarshal([]byte(s), &v) == nil {
		return v
	}
	return s
}

func cloneMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func providerFromModel(model string) string {
	p, _, ok := strings.Cut(strings.TrimSpace(model), "/")
	if !ok {
		return ""
	}
	return normalizeProvider(p)
}

func normalizeProvider(p string) string {
	p = strings.ToLower(strings.TrimSpace(p))
	p = strings.ReplaceAll(p, "_", "-")
	return p
}

func upstreamModel(provider, model string) string {
	prefix := provider + "/"
	return strings.TrimPrefix(model, prefix)
}

func defaultBaseURL(provider string) string {
	switch provider {
	case "openai":
		return "https://api.openai.com/v1"
	case "anthropic":
		return "https://api.anthropic.com/v1"
	case "fireworks":
		return "https://api.fireworks.ai/inference/v1"
	case "openrouter":
		return "https://openrouter.ai/api/v1"
	default:
		return ""
	}
}

func matchesAny(patterns []string, value string) bool {
	for _, p := range patterns {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if p == value {
			return true
		}
		if strings.HasSuffix(p, "*") && strings.HasPrefix(value, strings.TrimSuffix(p, "*")) {
			return true
		}
		if strings.HasSuffix(p, "/*") && strings.HasPrefix(value, strings.TrimSuffix(p, "*")) {
			return true
		}
	}
	return false
}

func estimateTokens(v any) int64 {
	b, _ := json.Marshal(v)
	n := int64(len(b) / 4)
	if n < 1 {
		n = 1
	}
	return n
}

func contentText(v any) string {
	switch c := v.(type) {
	case string:
		return c
	case []any:
		parts := []string{}
		for _, item := range c {
			if m, ok := item.(map[string]any); ok {
				if strArg(m, "type") == "text" || strArg(m, "type") == "input_text" {
					parts = append(parts, strArg(m, "text"))
				}
			}
		}
		return strings.Join(parts, "\n")
	default:
		return fmt.Sprint(c)
	}
}

func currentPeriod() string { return time.Now().UTC().Format("2006-01") }

func bearerToken(header string) string {
	header = strings.TrimSpace(header)
	if strings.HasPrefix(strings.ToLower(header), "bearer ") {
		return strings.TrimSpace(header[7:])
	}
	return ""
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func randomSuffix(n int) string {
	if n <= 0 {
		n = 16
	}
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(buf)[:n]
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
