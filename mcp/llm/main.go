package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
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
version: 0.5.3
description: Generic OpenAI-compatible text and image access with provider-cost metering for Apteva-hosted apps and agents.
author: Apteva
scopes: [project, global]
icon: https://raw.githubusercontent.com/apteva/apps/main/mcp/llm/icon.svg
requires:
  permissions:
    - db.write.app
    - platform.connections.read
    - platform.connections.read_credentials
    - platform.connections.execute
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
    - role: opencode_go_provider
      kind: integration
      compatible_slugs: [opencode-go]
      required: false
    - role: openai_codex_provider
      kind: integration
      compatible_slugs: [openai-codex]
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
      description: List models allowed by the current policy.
    - name: llm_models_sync
      description: Refresh discovered models from configured and bound providers.
    - name: llm_embeddings_create
      description: Create embeddings through an OpenAI-compatible configured provider.
    - name: llm_usage_get
      description: Return usage for a project, subject, or model in a monthly period.
    - name: llm_provider_configs_list
      description: List configured provider routes.
    - name: llm_provider_configs_upsert
      description: Create or update a provider route.
    - name: llm_provider_rates_list
      description: List provider-native model rates used for upstream cost calculation.
    - name: llm_provider_rates_upsert
      description: Create a manual provider rate override.
    - name: llm_provider_rates_delete
      description: Close an active provider rate without deleting its history.
    - name: llm_provider_rates_refresh
      description: Refresh rates from provider model metadata and the built-in catalog.
    - name: llm_policy_get
      description: Return the project or subject policy.
    - name: llm_policy_set
      description: Replace the project or subject policy.
    - name: llm_tokens_create
      description: Issue an OpenAI-compatible bearer token.
    - name: llm_tokens_list
      description: List issued LLM tokens without exposing token secrets.
    - name: llm_tokens_revoke
      description: Revoke an issued LLM token by token id, token value, or subject.
    - name: llm_subject_suspend
      description: Disable LLM access for a generic subject within a project.
    - name: llm_subject_resume
      description: Re-enable LLM access for a generic subject within a project.
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
    - name: llm.policy.limit_exceeded
      description: A request was denied because a project or subject usage limit was exceeded.
    - name: llm.provider.failed
      description: A provider route failed and may have triggered failover.
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

const (
	maxImageRequestBytes = 16 << 20
	maxDataImageBytes    = 10 << 20
	imageTokenEstimate   = 1024
)

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
	if err := ensureGatewaySchema(ctx.AppDB()); err != nil {
		return fmt.Errorf("repair llm schema: %w", err)
	}
	if err := maintainGatewayDB(ctx.AppDB()); err != nil {
		return fmt.Errorf("maintain llm db: %w", err)
	}
	if a.httpClient == nil {
		a.httpClient = &http.Client{
			Timeout: 3 * time.Minute,
			Transport: &http.Transport{
				Proxy:                 http.ProxyFromEnvironment,
				DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
				ForceAttemptHTTP2:     true,
				MaxIdleConns:          100,
				MaxIdleConnsPerHost:   20,
				IdleConnTimeout:       90 * time.Second,
				TLSHandshakeTimeout:   10 * time.Second,
				ResponseHeaderTimeout: 2 * time.Minute,
			},
		}
	}
	globalCtx = ctx
	ctx.Logger().Info("llm gateway mounted", "version", "0.5.3", "scope_project_id", os.Getenv("APTEVA_PROJECT_ID"))
	return nil
}

// ensureGatewaySchema replaces the unsafe v0.3 static migration with a
// column-aware repair. It is safe for fresh, upgraded, and partially upgraded
// databases and keeps every legacy usage row.
func ensureGatewaySchema(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	policiesExist, err := txTableExists(tx, "policies")
	if err != nil {
		return err
	}
	legacyV03Exists, err := txTableExists(tx, "policies_v02")
	if err != nil {
		return err
	}
	if !policiesExist {
		if !legacyV03Exists {
			return errors.New("policies table is missing")
		}
		if _, err := tx.Exec(`ALTER TABLE policies_v02 RENAME TO policies`); err != nil {
			return err
		}
		legacyV03Exists = false
	}

	policyScoped, err := txTableHasColumn(tx, "policies", "subject_type")
	if err != nil {
		return err
	}
	if !policyScoped {
		if _, err := tx.Exec(`ALTER TABLE policies RENAME TO policies_legacy_v04`); err != nil {
			return err
		}
		if _, err := tx.Exec(`CREATE TABLE policies (
			id INTEGER PRIMARY KEY,
			project_id TEXT NOT NULL DEFAULT '',
			subject_type TEXT NOT NULL DEFAULT '',
			subject_id TEXT NOT NULL DEFAULT '',
			allowed_models_json TEXT NOT NULL DEFAULT '[]',
			blocked_models_json TEXT NOT NULL DEFAULT '[]',
			allowed_providers_json TEXT NOT NULL DEFAULT '[]',
			limits_json TEXT NOT NULL DEFAULT '{}',
			disabled INTEGER NOT NULL DEFAULT 0,
			fallback_policy_json TEXT NOT NULL DEFAULT '{}',
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(project_id, subject_type, subject_id)
		)`); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO policies (
			id, project_id, subject_type, subject_id, allowed_models_json, blocked_models_json,
			allowed_providers_json, limits_json, disabled, fallback_policy_json, created_at, updated_at
		) SELECT id, project_id, '', '', allowed_models_json, blocked_models_json,
			allowed_providers_json, limits_json, 0, fallback_policy_json, created_at, updated_at
			FROM policies_legacy_v04`); err != nil {
			return err
		}
		if _, err := tx.Exec(`DROP TABLE policies_legacy_v04`); err != nil {
			return err
		}
	}
	// The original v0.3 migration may have stopped after creating the scoped
	// table but before copying and dropping its policies_v02 backup.
	if legacyV03Exists {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO policies (
			id, project_id, subject_type, subject_id, allowed_models_json, blocked_models_json,
			allowed_providers_json, limits_json, disabled, fallback_policy_json, created_at, updated_at
		) SELECT id, project_id, '', '', allowed_models_json, blocked_models_json,
			allowed_providers_json, limits_json, 0, fallback_policy_json, created_at, updated_at
			FROM policies_v02`); err != nil {
			return err
		}
		if _, err := tx.Exec(`DROP TABLE policies_v02`); err != nil {
			return err
		}
	}

	for _, col := range []struct {
		name string
		decl string
	}{
		{"provider_request_id", `TEXT NOT NULL DEFAULT ''`},
		{"token_id", `INTEGER NOT NULL DEFAULT 0`},
	} {
		has, err := txTableHasColumn(tx, "usage_events", col.name)
		if err != nil {
			return err
		}
		if !has {
			if _, err := tx.Exec(`ALTER TABLE usage_events ADD COLUMN ` + col.name + ` ` + col.decl); err != nil {
				return err
			}
		}
	}
	if err := ensureProviderCostSchema(tx); err != nil {
		return err
	}

	if _, err := tx.Exec(`DROP INDEX IF EXISTS ux_usage_request_id`); err != nil {
		return err
	}
	// Preserve duplicate legacy events while making their identifiers unique.
	if _, err := tx.Exec(`UPDATE usage_events
		SET request_id = request_id || ':legacy:' || id
		WHERE request_id != '' AND id NOT IN (
			SELECT MIN(id) FROM usage_events WHERE request_id != ''
			GROUP BY project_id, subject_type, subject_id, request_id
		)`); err != nil {
		return err
	}
	if _, err := tx.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS ux_usage_subject_request
		ON usage_events(project_id, subject_type, subject_id, request_id)
		WHERE request_id != ''`); err != nil {
		return err
	}
	if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS ix_usage_quota_project
		ON usage_events(project_id, period, status)`); err != nil {
		return err
	}
	if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS ix_usage_quota_subject
		ON usage_events(project_id, subject_type, subject_id, period, status)`); err != nil {
		return err
	}
	return tx.Commit()
}

type tableColumnQuerier interface {
	Query(query string, args ...any) (*sql.Rows, error)
}

type tableRowQuerier interface {
	QueryRow(query string, args ...any) *sql.Row
}

func txTableExists(q tableRowQuerier, table string) (bool, error) {
	var count int
	err := q.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&count)
	return count > 0, err
}

func txTableHasColumn(q tableColumnQuerier, table, column string) (bool, error) {
	rows, err := q.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, pk int
		var name, typ string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

func (a *App) OnUnmount(*sdk.AppCtx) error    { return nil }
func (a *App) Channels() []sdk.ChannelFactory { return nil }
func (a *App) Workers() []sdk.Worker {
	run := func(ctx context.Context, app *sdk.AppCtx) error {
		results := a.syncProviderModelsWithContext(ctx, app, app.CurrentProject(), "")
		var failures []string
		for _, result := range results {
			if result.Status == "error" {
				failures = append(failures, result.Provider+": "+result.Error)
			}
		}
		if len(failures) > 0 {
			return errors.New(strings.Join(failures, "; "))
		}
		return nil
	}
	return []sdk.Worker{
		{Name: "model-sync-initial", Run: run},
		{Name: "model-sync-refresh", Schedule: "@every 6h", Run: run},
		{Name: "gateway-maintenance", Schedule: "@every 24h", Run: func(_ context.Context, app *sdk.AppCtx) error {
			return maintainGatewayDB(app.AppDB())
		}},
	}
}

func maintainGatewayDB(db *sql.DB) error {
	if _, err := db.Exec(`UPDATE usage_events SET status='failed', response_tokens=0, total_tokens=request_tokens
		WHERE status='reserved' AND created_at < datetime('now','-1 hour')`); err != nil {
		return err
	}
	_, err := db.Exec(`DELETE FROM audit_logs WHERE created_at < datetime('now','-180 days')`)
	return err
}
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/v1/", Handler: a.handleV1, NoAuth: true},
		{Pattern: "/tokens/revoke_subject", Handler: a.handleTokensRevokeSubject},
		{Pattern: "/tokens/revoke", Handler: a.handleTokensRevoke},
		{Pattern: "/tokens", Handler: a.handleTokens},
		{Pattern: "/subjects/suspend", Handler: a.handleSubjectSuspend},
		{Pattern: "/subjects/resume", Handler: a.handleSubjectResume},
		{Pattern: "/providers", Handler: a.handleProviders},
		{Pattern: "/provider-rates/refresh", Handler: a.handleProviderRatesRefresh},
		{Pattern: "/provider-rates", Handler: a.handleProviderRates},
		{Pattern: "/models/sync", Handler: a.handleModelsSync},
		{Pattern: "/models", Handler: a.handleModels},
		{Pattern: "/test/chat", Handler: a.handleTestChat},
		{Pattern: "/policy", Handler: a.handlePolicy},
		{Pattern: "/usage/events", Handler: a.handleUsageEvents},
		{Pattern: "/usage", Handler: a.handleUsage},
		{Pattern: "/audit", Handler: a.handleAudit},
	}
}

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{Name: "llm_chat_complete", Description: "OpenAI-compatible text or image chat completion through the configured gateway.", InputSchema: schemaObject(map[string]any{
			"project_id":   map[string]any{"type": "string"},
			"subject_type": map[string]any{"type": "string"},
			"subject_id":   map[string]any{"type": "string"},
			"request_id":   map[string]any{"type": "string"},
			"model":        map[string]any{"type": "string"},
			"messages": map[string]any{
				"type":        "array",
				"description": "Chat messages. Content may be text or text/input_text and image_url/input_image blocks using an http(s) URL or base64 image data URL.",
			},
			"tools":       map[string]any{"type": "array"},
			"tool_choice": map[string]any{},
			"temperature": map[string]any{
				"type": "number",
			},
			"max_tokens": map[string]any{"type": "integer"},
		}, []string{"model", "messages"}), HandlerCtx: a.toolChatCompleteCtx},
		{Name: "llm_models_list", Description: "List models allowed by the effective project and subject policy.", InputSchema: schemaObject(map[string]any{
			"project_id": map[string]any{"type": "string"}, "subject_type": map[string]any{"type": "string"}, "subject_id": map[string]any{"type": "string"},
		}, nil), Handler: a.toolModelsList},
		{Name: "llm_models_sync", Description: "Refresh discovered models from configured and bound providers.", InputSchema: schemaObject(map[string]any{
			"project_id": map[string]any{"type": "string"}, "provider": map[string]any{"type": "string"},
		}, nil), HandlerCtx: a.toolModelsSyncCtx},
		{Name: "llm_embeddings_create", Description: "Create embeddings through an OpenAI-compatible configured provider.", InputSchema: schemaObject(map[string]any{
			"project_id": map[string]any{"type": "string"}, "subject_type": map[string]any{"type": "string"}, "subject_id": map[string]any{"type": "string"},
			"request_id": map[string]any{"type": "string"}, "model": map[string]any{"type": "string"}, "input": map[string]any{}, "encoding_format": map[string]any{"type": "string"},
		}, []string{"model", "input"}), HandlerCtx: a.toolEmbeddingsCtx},
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
		{Name: "llm_provider_rates_list", Description: "List provider-native model rates used for upstream cost calculation.", InputSchema: schemaObject(map[string]any{
			"project_id": map[string]any{"type": "string"}, "provider": map[string]any{"type": "string"}, "model_id": map[string]any{"type": "string"}, "include_history": map[string]any{"type": "boolean"},
		}, nil), Handler: a.toolProviderRatesList},
		{Name: "llm_provider_rates_upsert", Description: "Create a manual provider rate override.", InputSchema: schemaObject(map[string]any{
			"project_id": map[string]any{"type": "string"}, "provider": map[string]any{"type": "string"}, "model_id": map[string]any{"type": "string"},
			"currency": map[string]any{"type": "string"}, "input_microunits_per_million": map[string]any{"type": "integer"},
			"output_microunits_per_million": map[string]any{"type": "integer"}, "cached_input_microunits_per_million": map[string]any{"type": "integer"},
			"cache_write_microunits_per_million": map[string]any{"type": "integer"}, "request_microunits": map[string]any{"type": "integer"},
			"extra_rates": map[string]any{"type": "object"}, "source_reference": map[string]any{"type": "string"},
		}, []string{"provider", "model_id"}), Handler: a.toolProviderRateUpsert},
		{Name: "llm_provider_rates_delete", Description: "Close an active provider rate while retaining history.", InputSchema: schemaObject(map[string]any{
			"project_id": map[string]any{"type": "string"}, "id": map[string]any{"type": "integer"},
		}, []string{"id"}), Handler: a.toolProviderRateDelete},
		{Name: "llm_provider_rates_refresh", Description: "Refresh rates from provider model metadata and the built-in catalog.", InputSchema: schemaObject(map[string]any{
			"project_id": map[string]any{"type": "string"}, "provider": map[string]any{"type": "string"},
		}, nil), Handler: a.toolProviderRatesRefresh},
		{Name: "llm_policy_get", Description: "Return a project or subject policy.", InputSchema: schemaObject(map[string]any{
			"project_id": map[string]any{"type": "string"}, "subject_type": map[string]any{"type": "string"}, "subject_id": map[string]any{"type": "string"},
		}, nil), Handler: a.toolPolicyGet},
		{Name: "llm_policy_set", Description: "Replace project policy.", InputSchema: schemaObject(map[string]any{
			"project_id":        map[string]any{"type": "string"},
			"subject_type":      map[string]any{"type": "string"},
			"subject_id":        map[string]any{"type": "string"},
			"allowed_models":    map[string]any{"type": "array"},
			"blocked_models":    map[string]any{"type": "array"},
			"allowed_providers": map[string]any{"type": "array"},
			"limits":            map[string]any{"type": "object"},
			"disabled":          map[string]any{"type": "boolean"},
			"fallback_policy":   map[string]any{"type": "object"},
		}, nil), Handler: a.toolPolicySet},
		{Name: "llm_tokens_create", Description: "Issue an OpenAI-compatible bearer token.", InputSchema: schemaObject(map[string]any{
			"project_id":   map[string]any{"type": "string"},
			"subject_type": map[string]any{"type": "string"},
			"subject_id":   map[string]any{"type": "string"},
			"scopes":       map[string]any{"type": "array"},
			"expires_at":   map[string]any{"type": "string"},
		}, nil), Handler: a.toolTokenCreate},
		{Name: "llm_tokens_list", Description: "List issued LLM tokens without exposing token secrets.", InputSchema: schemaObject(map[string]any{
			"project_id": map[string]any{"type": "string"}, "subject_type": map[string]any{"type": "string"}, "subject_id": map[string]any{"type": "string"}, "include_revoked": map[string]any{"type": "boolean"},
		}, nil), Handler: a.toolTokensList},
		{Name: "llm_tokens_revoke", Description: "Revoke an LLM token by id/token, or revoke all tokens for a subject.", InputSchema: schemaObject(map[string]any{
			"project_id":   map[string]any{"type": "string"},
			"token_id":     map[string]any{"type": "integer"},
			"token":        map[string]any{"type": "string"},
			"subject_type": map[string]any{"type": "string"},
			"subject_id":   map[string]any{"type": "string"},
		}, nil), Handler: a.toolTokenRevoke},
		{Name: "llm_subject_suspend", Description: "Disable LLM access for a generic subject.", InputSchema: schemaObject(map[string]any{
			"project_id":   map[string]any{"type": "string"},
			"subject_type": map[string]any{"type": "string"},
			"subject_id":   map[string]any{"type": "string"},
		}, []string{"subject_type", "subject_id"}), Handler: a.toolSubjectSuspend},
		{Name: "llm_subject_resume", Description: "Re-enable LLM access for a generic subject.", InputSchema: schemaObject(map[string]any{
			"project_id":   map[string]any{"type": "string"},
			"subject_type": map[string]any{"type": "string"},
			"subject_id":   map[string]any{"type": "string"},
		}, []string{"subject_type", "subject_id"}), Handler: a.toolSubjectResume},
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
	SubjectType      string          `json:"subject_type,omitempty"`
	SubjectID        string          `json:"subject_id,omitempty"`
	AllowedModels    []string        `json:"allowed_models"`
	BlockedModels    []string        `json:"blocked_models"`
	AllowedProviders []string        `json:"allowed_providers"`
	Limits           Limits          `json:"limits"`
	Disabled         bool            `json:"disabled,omitempty"`
	FallbackPolicy   json.RawMessage `json:"fallback_policy,omitempty"`
}

type Limits struct {
	MonthlyRequestLimit                int64  `json:"monthly_request_limit,omitempty"`
	MonthlyInputTokenLimit             int64  `json:"monthly_input_token_limit,omitempty"`
	MonthlyOutputTokenLimit            int64  `json:"monthly_output_token_limit,omitempty"`
	MaxTokensPerRequest                int64  `json:"max_tokens_per_request,omitempty"`
	MonthlyProviderCostLimitMicrounits int64  `json:"monthly_provider_cost_limit_microunits,omitempty"`
	ProviderCostCurrency               string `json:"provider_cost_currency,omitempty"`
	UnpricedModelBehavior              string `json:"unpriced_model_behavior,omitempty"`
}

type TokenIdentity struct {
	ID          int64    `json:"id"`
	ProjectID   string   `json:"project_id"`
	SubjectType string   `json:"subject_type"`
	SubjectID   string   `json:"subject_id"`
	Scopes      []string `json:"scopes"`
}

type UsageSummary struct {
	ProjectID              string `json:"project_id"`
	Period                 string `json:"period"`
	SubjectType            string `json:"subject_type,omitempty"`
	SubjectID              string `json:"subject_id,omitempty"`
	Model                  string `json:"model,omitempty"`
	Requests               int64  `json:"requests"`
	RequestTokens          int64  `json:"request_tokens"`
	ResponseTokens         int64  `json:"response_tokens"`
	TotalTokens            int64  `json:"total_tokens"`
	EstimatedCostCents     int64  `json:"-"`
	ProviderCostMicrounits int64  `json:"provider_cost_microunits"`
	ProviderCostCurrency   string `json:"provider_cost_currency"`
	ReportedCostRequests   int64  `json:"reported_cost_requests"`
	CalculatedCostRequests int64  `json:"calculated_cost_requests"`
	PartialCostRequests    int64  `json:"partial_cost_requests"`
	UnpricedRequests       int64  `json:"unpriced_requests"`
}

type UsageEvent struct {
	ID                     int64           `json:"id"`
	ProjectID              string          `json:"project_id"`
	SubjectType            string          `json:"subject_type"`
	SubjectID              string          `json:"subject_id"`
	Provider               string          `json:"provider"`
	Model                  string          `json:"model"`
	RequestTokens          int64           `json:"request_tokens"`
	ResponseTokens         int64           `json:"response_tokens"`
	TotalTokens            int64           `json:"total_tokens"`
	EstimatedCostCents     int64           `json:"-"`
	ProviderCostMicrounits int64           `json:"provider_cost_microunits"`
	ProviderCostCurrency   string          `json:"provider_cost_currency"`
	ProviderCostStatus     string          `json:"provider_cost_status"`
	ProviderCostSource     string          `json:"provider_cost_source,omitempty"`
	ProviderRateID         int64           `json:"provider_rate_id,omitempty"`
	UsageDetails           json.RawMessage `json:"usage_details,omitempty"`
	Status                 string          `json:"status"`
	Period                 string          `json:"period"`
	RequestID              string          `json:"request_id"`
	ProviderRequestID      string          `json:"provider_request_id"`
	TokenID                int64           `json:"token_id,omitempty"`
	CreatedAt              string          `json:"created_at"`
	Duplicate              bool            `json:"duplicate,omitempty"`
}

type TokenRecord struct {
	ID          int64    `json:"id"`
	ProjectID   string   `json:"project_id"`
	SubjectType string   `json:"subject_type"`
	SubjectID   string   `json:"subject_id"`
	Scopes      []string `json:"scopes"`
	ExpiresAt   string   `json:"expires_at,omitempty"`
	CreatedAt   string   `json:"created_at"`
	RevokedAt   string   `json:"revoked_at,omitempty"`
}

type FallbackRoute struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

type FallbackPolicy struct {
	Routes []FallbackRoute `json:"routes"`
}

type ProviderModel struct {
	ID               int64           `json:"id,omitempty"`
	ProjectID        string          `json:"project_id"`
	Provider         string          `json:"provider"`
	ModelID          string          `json:"model_id"`
	DisplayName      string          `json:"display_name,omitempty"`
	GatewayModel     string          `json:"gateway_model"`
	Capabilities     json.RawMessage `json:"capabilities,omitempty"`
	ContextWindow    int64           `json:"context_window,omitempty"`
	InputModalities  json.RawMessage `json:"input_modalities,omitempty"`
	OutputModalities json.RawMessage `json:"output_modalities,omitempty"`
	Status           string          `json:"status"`
	Raw              json.RawMessage `json:"raw,omitempty"`
	LastSeenAt       string          `json:"last_seen_at,omitempty"`
	CreatedAt        string          `json:"created_at,omitempty"`
	UpdatedAt        string          `json:"updated_at,omitempty"`
}

type ModelSyncResult struct {
	Provider   string          `json:"provider"`
	Status     string          `json:"status"`
	ModelCount int             `json:"model_count"`
	Error      string          `json:"error,omitempty"`
	Models     []ProviderModel `json:"models,omitempty"`
}

type chatResult struct {
	Status                 int
	Body                   []byte
	RequestTokens          int64
	ResponseTokens         int64
	RequestID              string
	ProviderCostMicrounits int64
	ProviderCostCurrency   string
	ProviderCostReported   bool
	UsageDetails           json.RawMessage
	UsageEvent             *UsageEvent
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
		if !tokenHasScope(ident, "models") {
			writeOpenAIError(w, http.StatusForbidden, "insufficient_scope", "token does not grant models access")
			return
		}
		a.handleV1Models(ctx, ident, w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/v1/usage":
		if !tokenHasScope(ident, "usage") {
			writeOpenAIError(w, http.StatusForbidden, "insufficient_scope", "token does not grant usage access")
			return
		}
		a.handleV1Usage(ctx, ident, w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/v1/chat/completions":
		if !tokenHasScope(ident, "chat") {
			writeOpenAIError(w, http.StatusForbidden, "insufficient_scope", "token does not grant chat access")
			return
		}
		a.handleV1Chat(ctx, ident, w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/v1/responses":
		if !tokenHasScope(ident, "chat") {
			writeOpenAIError(w, http.StatusForbidden, "insufficient_scope", "token does not grant chat access")
			return
		}
		a.handleV1Responses(ctx, ident, w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/v1/embeddings":
		if !tokenHasScope(ident, "embeddings") {
			writeOpenAIError(w, http.StatusForbidden, "insufficient_scope", "token does not grant embeddings access")
			return
		}
		a.handleV1Embeddings(ctx, ident, w, r)
	default:
		writeOpenAIError(w, http.StatusNotFound, "not_found", "unknown LLM endpoint")
	}
}

func (a *App) handleV1Models(ctx *sdk.AppCtx, ident *TokenIdentity, w http.ResponseWriter, _ *http.Request) {
	policies, err := dbEffectivePolicies(ctx.AppDB(), ident)
	if err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "server_error", err.Error())
		return
	}
	data := []map[string]any{}
	seen := map[string]bool{}
	cached, err := dbProviderModelsList(ctx.AppDB(), providerModelFilter{ProjectID: ident.ProjectID, Status: "active"})
	if err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "server_error", err.Error())
		return
	}
	for _, model := range cached {
		id := model.GatewayModel
		if id == "" {
			id = gatewayModelID(model.Provider, model.ModelID)
		}
		if id == "" || seen[id] {
			continue
		}
		if err := policiesAllow(policies, model.Provider, id, 0); err != nil {
			continue
		}
		seen[id] = true
		data = append(data, map[string]any{
			"id": id, "object": "model", "owned_by": model.Provider, "display_name": model.DisplayName,
			"input_modalities": model.InputModalities, "output_modalities": model.OutputModalities, "capabilities": model.Capabilities,
		})
	}
	for _, pol := range policies {
		for _, model := range pol.AllowedModels {
			if model == "" || strings.Contains(model, "*") || seen[model] {
				continue
			}
			provider := providerFromModel(model)
			if err := policiesAllow(policies, provider, model, 0); err != nil {
				continue
			}
			seen[model] = true
			data = append(data, map[string]any{"id": model, "object": "model", "owned_by": provider})
		}
	}
	writeJSON(w, map[string]any{"object": "list", "data": data})
}

func (a *App) handleV1Usage(ctx *sdk.AppCtx, ident *TokenIdentity, w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	subjectType := ident.SubjectType
	subjectID := ident.SubjectID
	if tokenHasScope(ident, "usage:project") {
		subjectType = q.Get("subject_type")
		subjectID = q.Get("subject_id")
	} else if (q.Get("subject_type") != "" && q.Get("subject_type") != ident.SubjectType) || (q.Get("subject_id") != "" && q.Get("subject_id") != ident.SubjectID) {
		writeOpenAIError(w, http.StatusForbidden, "insufficient_scope", "token can only read usage for its own subject")
		return
	}
	filter := usageFilter{
		ProjectID:   ident.ProjectID,
		Period:      firstNonEmpty(q.Get("period"), currentPeriod()),
		SubjectType: subjectType,
		SubjectID:   subjectID,
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
	if err := json.NewDecoder(io.LimitReader(r.Body, maxImageRequestBytes)).Decode(&body); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "invalid JSON")
		return
	}
	stream := boolArg(body, "stream")
	if stream {
		// Providers are called non-streaming so usage is committed atomically
		// before a compatible SSE response is emitted.
		body["stream"] = false
		delete(body, "stream_options")
	}
	body["_llm_request_id"] = requestIDFor(r, body)
	res, err := a.executeChatContext(r.Context(), ctx, ident, body)
	if err != nil {
		status, typ := errorStatus(err)
		writeOpenAIError(w, status, typ, err.Error())
		return
	}
	if stream {
		writeBufferedChatStream(w, res.Body)
		return
	}
	copyProviderHeaders(w, res)
	w.WriteHeader(res.Status)
	_, _ = w.Write(res.Body)
}

func (a *App) handleV1Responses(ctx *sdk.AppCtx, ident *TokenIdentity, w http.ResponseWriter, r *http.Request) {
	var req map[string]any
	if err := json.NewDecoder(io.LimitReader(r.Body, maxImageRequestBytes)).Decode(&req); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "invalid JSON")
		return
	}
	stream := boolArg(req, "stream")
	input := req["input"]
	messages := []any{}
	switch v := input.(type) {
	case string:
		messages = append(messages, map[string]any{"role": "user", "content": v})
	case []any:
		messages = responsesInputToMessages(v)
	default:
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "responses.input must be a string or message array")
		return
	}
	if len(messages) == 0 {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "responses.input contains no supported messages")
		return
	}
	chatReq := map[string]any{
		"model":    req["model"],
		"messages": messages,
	}
	if instructions := strArg(req, "instructions"); instructions != "" {
		chatReq["messages"] = append([]any{map[string]any{"role": "system", "content": instructions}}, messages...)
	}
	if v, ok := req["temperature"]; ok {
		chatReq["temperature"] = v
	}
	if v, ok := req["max_output_tokens"]; ok {
		chatReq["max_tokens"] = v
	}
	for _, key := range []string{"top_p", "tool_choice", "parallel_tool_calls"} {
		if v, ok := req[key]; ok {
			chatReq[key] = v
		}
	}
	if choice, ok := chatReq["tool_choice"].(map[string]any); ok && strArg(choice, "type") == "function" && strArg(choice, "name") != "" {
		chatReq["tool_choice"] = map[string]any{"type": "function", "function": map[string]any{"name": strArg(choice, "name")}}
	}
	if tools, ok := req["tools"].([]any); ok {
		chatReq["tools"] = responsesToolsToChat(tools)
	}
	chatReq["_llm_request_id"] = requestIDFor(r, req)
	res, err := a.executeChatContext(r.Context(), ctx, ident, chatReq)
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
				Content   string `json:"content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
		Usage any `json:"usage,omitempty"`
	}
	_ = json.Unmarshal(res.Body, &chat)
	text := ""
	if len(chat.Choices) > 0 {
		text = chat.Choices[0].Message.Content
	}
	output := []any{}
	if text != "" {
		output = append(output, map[string]any{
			"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": text}},
		})
	}
	if len(chat.Choices) > 0 {
		for _, call := range chat.Choices[0].Message.ToolCalls {
			output = append(output, map[string]any{"type": "function_call", "call_id": call.ID, "name": call.Function.Name, "arguments": call.Function.Arguments})
		}
	}
	response := map[string]any{
		"id":         firstNonEmpty(chat.ID, "resp_"+randomSuffix(12)),
		"object":     "response",
		"created_at": time.Now().Unix(),
		"model":      chat.Model,
		"output":     output,
		"usage":      chat.Usage,
	}
	if stream {
		writeBufferedResponsesStream(w, response, text)
		return
	}
	writeJSON(w, response)
}

func writeBufferedChatStream(w http.ResponseWriter, raw []byte) {
	var response map[string]any
	if err := json.Unmarshal(raw, &response); err != nil {
		writeOpenAIError(w, http.StatusBadGateway, "provider_error", "provider returned an invalid chat response")
		return
	}
	id := firstNonEmpty(strAny(response["id"]), "chatcmpl_"+randomSuffix(12))
	model := strAny(response["model"])
	created := int64FromAny(response["created"])
	if created == 0 {
		created = time.Now().Unix()
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	choices, _ := response["choices"].([]any)
	if len(choices) > 0 {
		choice, _ := choices[0].(map[string]any)
		message, _ := choice["message"].(map[string]any)
		delta := map[string]any{"role": "assistant"}
		if content, exists := message["content"]; exists {
			delta["content"] = content
		}
		if calls, exists := message["tool_calls"]; exists {
			delta["tool_calls"] = calls
		}
		writeSSEData(w, map[string]any{"id": id, "object": "chat.completion.chunk", "created": created, "model": model, "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": nil}}})
		writeSSEData(w, map[string]any{"id": id, "object": "chat.completion.chunk", "created": created, "model": model, "choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": choice["finish_reason"]}}, "usage": response["usage"]})
	}
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

func writeBufferedResponsesStream(w http.ResponseWriter, response map[string]any, text string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	writeSSEEvent(w, "response.created", map[string]any{"type": "response.created", "response": response})
	if text != "" {
		writeSSEEvent(w, "response.output_text.delta", map[string]any{"type": "response.output_text.delta", "delta": text, "output_index": 0, "content_index": 0})
	}
	writeSSEEvent(w, "response.completed", map[string]any{"type": "response.completed", "response": response})
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

func writeSSEData(w io.Writer, value any) {
	b, _ := json.Marshal(value)
	_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
}

func writeSSEEvent(w io.Writer, event string, value any) {
	b, _ := json.Marshal(value)
	_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
}

func responsesInputToMessages(input []any) []any {
	out := []any{}
	for _, raw := range input {
		item, _ := raw.(map[string]any)
		switch strArg(item, "type") {
		case "function_call":
			out = append(out, map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{map[string]any{
				"id": firstNonEmpty(strArg(item, "call_id"), strArg(item, "id")), "type": "function",
				"function": map[string]any{"name": strArg(item, "name"), "arguments": strArg(item, "arguments")},
			}}})
		case "function_call_output":
			out = append(out, map[string]any{"role": "tool", "tool_call_id": strArg(item, "call_id"), "content": item["output"]})
		default:
			if strArg(item, "role") != "" {
				out = append(out, item)
			}
		}
	}
	return out
}

func responsesToolsToChat(tools []any) []any {
	out := []any{}
	for _, raw := range tools {
		tool, _ := raw.(map[string]any)
		if strArg(tool, "type") != "function" {
			continue
		}
		if _, nested := tool["function"]; nested {
			out = append(out, tool)
			continue
		}
		out = append(out, map[string]any{"type": "function", "function": map[string]any{
			"name": strArg(tool, "name"), "description": strArg(tool, "description"), "parameters": tool["parameters"],
		}})
	}
	return out
}

func (a *App) handleV1Embeddings(ctx *sdk.AppCtx, ident *TokenIdentity, w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := json.NewDecoder(io.LimitReader(r.Body, 8<<20)).Decode(&body); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "invalid JSON")
		return
	}
	body["_llm_request_id"] = requestIDFor(r, body)
	res, err := a.executeEmbeddingsContext(r.Context(), ctx, ident, body)
	if err != nil {
		status, typ := errorStatus(err)
		writeOpenAIError(w, status, typ, err.Error())
		return
	}
	copyProviderHeaders(w, res)
	w.WriteHeader(res.Status)
	_, _ = w.Write(res.Body)
}

func (a *App) executeEmbeddingsContext(reqCtx context.Context, ctx *sdk.AppCtx, ident *TokenIdentity, body map[string]any) (*chatResult, error) {
	model := strArg(body, "model")
	provider := providerFromModel(model)
	if model == "" || provider == "" {
		return nil, userError("model must include a provider prefix")
	}
	if _, exists := body["input"]; !exists {
		return nil, userError("input is required")
	}
	policies, err := dbEffectivePolicies(ctx.AppDB(), ident)
	if err != nil {
		return nil, err
	}
	if err := policiesAllow(policies, provider, model, 0); err != nil {
		_ = dbAudit(ctx.AppDB(), ident, "request.denied", provider, model, "denied", err.Error(), map[string]any{"operation": "embeddings"})
		return nil, err
	}
	requestID := firstNonEmpty(strArg(body, "_llm_request_id"), "llm_req_"+randomSuffix(16))
	estimatedInput := estimateTokens(body["input"])
	reservation, _, err := reserveUsage(ctx.AppDB(), ident, provider, model, estimatedInput, 0, requestID, policies, false)
	if err != nil {
		_ = dbAudit(ctx.AppDB(), ident, "request.denied", provider, model, "denied", err.Error(), map[string]any{"request_id": requestID, "operation": "embeddings"})
		return nil, err
	}
	attempts := fallbackAttempts(policies, provider, model)
	var result *chatResult
	var lastErr error
	used := attempts[0]
	for i, attempt := range attempts {
		if policiesAllow(policies, attempt.Provider, attempt.Model, 0) != nil {
			continue
		}
		used = attempt
		cfg, callErr := providerConfigFor(ctx, ident.ProjectID, attempt.Provider)
		if callErr == nil {
			var key string
			key, callErr = resolveProviderKey(ctx, cfg)
			if callErr == nil {
				attemptBody := cloneMap(body)
				attemptBody["model"] = attempt.Model
				result, callErr = a.callEmbeddings(reqCtx, cfg, key, attemptBody)
			}
		}
		if callErr == nil {
			break
		}
		result = nil
		lastErr = callErr
		retryable := isRetryableProviderError(callErr)
		ctx.EmitWithProject("llm.provider.failed", ident.ProjectID, map[string]any{
			"request_id": requestID, "provider": attempt.Provider, "model": attempt.Model, "error": callErr.Error(),
			"retryable": retryable, "failover": retryable && i+1 < len(attempts), "operation": "embeddings",
		})
		_ = dbAudit(ctx.AppDB(), ident, "provider.failed", attempt.Provider, attempt.Model, "error", callErr.Error(), map[string]any{"request_id": requestID, "retryable": retryable, "operation": "embeddings"})
		if !retryable {
			break
		}
	}
	if result == nil {
		if lastErr == nil {
			lastErr = errors.New("no allowed embedding provider route is available")
		}
		event, finishErr := finishUsageReservation(ctx.AppDB(), reservation.ID, used.Provider, used.Model, estimatedInput, 0, "failed", "")
		if finishErr != nil {
			return nil, fmt.Errorf("provider failed: %v; record usage: %w", lastErr, finishErr)
		}
		payload := usageEventPayload(event, map[string]any{"error": lastErr.Error(), "operation": "embeddings"})
		_ = dbAudit(ctx.AppDB(), ident, "request.failed", used.Provider, used.Model, "error", lastErr.Error(), map[string]any{"request_id": requestID, "usage_event_id": event.ID, "operation": "embeddings"})
		ctx.EmitWithProject("llm.usage.recorded", ident.ProjectID, payload)
		ctx.EmitWithProject("llm.request.failed", ident.ProjectID, payload)
		return nil, lastErr
	}
	if result.RequestTokens == 0 {
		result.RequestTokens = estimatedInput
	}
	event, err := finishUsageReservationCost(ctx.AppDB(), reservation.ID, used.Provider, used.Model, result.RequestTokens, 0, "completed", result.RequestID,
		result.ProviderCostMicrounits, result.ProviderCostCurrency, result.ProviderCostReported, result.UsageDetails)
	if err != nil {
		return nil, err
	}
	payload := usageEventPayload(event, map[string]any{"operation": "embeddings"})
	_ = dbAudit(ctx.AppDB(), ident, "request.completed", used.Provider, used.Model, "completed", "", map[string]any{"request_id": requestID, "provider_request_id": result.RequestID, "usage_event_id": event.ID, "operation": "embeddings"})
	ctx.EmitWithProject("llm.usage.recorded", ident.ProjectID, payload)
	ctx.EmitWithProject("llm.request.completed", ident.ProjectID, payload)
	result.UsageEvent = event
	return result, nil
}

func (a *App) callEmbeddings(ctx context.Context, cfg *ProviderConfig, apiKey string, body map[string]any) (*chatResult, error) {
	if cfg.Provider == "anthropic" {
		return nil, errors.New("anthropic does not expose an embeddings endpoint")
	}
	outBody := cloneMap(body)
	delete(outBody, "_llm_request_id")
	delete(outBody, "request_id")
	outBody["model"] = upstreamModel(cfg.Provider, strArg(body, "model"))
	b, err := json.Marshal(outBody)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(cfg.BaseURL, "/")+"/embeddings", bytes.NewReader(b))
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
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	result := &chatResult{Status: resp.StatusCode, Body: raw, RequestID: resp.Header.Get("X-Request-Id")}
	metering := parseProviderMetering(raw)
	result.RequestTokens = metering.InputTokens
	result.ProviderCostMicrounits = metering.CostMicrounits
	result.ProviderCostCurrency = metering.CostCurrency
	result.ProviderCostReported = metering.CostReported
	result.UsageDetails = providerCostDetails(metering)
	if result.RequestID == "" {
		result.RequestID = "llm_" + randomSuffix(16)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return result, providerError(resp.StatusCode, string(raw))
	}
	var response struct {
		Data  []json.RawMessage `json:"data"`
		Error json.RawMessage   `json:"error"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return result, providerError(http.StatusBadGateway, "provider returned malformed embedding JSON")
	}
	if len(response.Error) > 0 && string(response.Error) != "null" {
		return result, providerError(http.StatusBadGateway, "provider returned an embedding error payload with a successful HTTP status")
	}
	if len(response.Data) == 0 {
		return result, providerError(http.StatusBadGateway, "provider response contains no embeddings")
	}
	return result, nil
}

func (a *App) executeChat(ctx *sdk.AppCtx, ident *TokenIdentity, body map[string]any) (*chatResult, error) {
	return a.executeChatContext(context.Background(), ctx, ident, body)
}

type providerAttempt struct {
	Provider string
	Model    string
}

func (a *App) executeChatContext(reqCtx context.Context, ctx *sdk.AppCtx, ident *TokenIdentity, body map[string]any) (*chatResult, error) {
	if err := normalizeChatImageInputs(body); err != nil {
		return nil, err
	}
	model := strArg(body, "model")
	if model == "" {
		return nil, userError("model is required")
	}
	provider := providerFromModel(model)
	if provider == "" {
		return nil, userError("model must include provider prefix, for example openai/gpt-4.1 or openrouter/anthropic/claude-sonnet-4")
	}
	policies, err := dbEffectivePolicies(ctx.AppDB(), ident)
	if err != nil {
		return nil, err
	}
	if err := policiesAllow(policies, provider, model, int64Arg(body, "max_tokens")); err != nil {
		_ = dbAudit(ctx.AppDB(), ident, "request.denied", provider, model, "denied", err.Error(), nil)
		return nil, err
	}
	if err := ensureModelSupportsRequest(ctx.AppDB(), ident.ProjectID, provider, model, body); err != nil {
		_ = dbAudit(ctx.AppDB(), ident, "request.denied", provider, model, "denied", err.Error(), map[string]any{"input_modality": "image"})
		return nil, err
	}
	attempts := fallbackAttempts(policies, provider, model)
	estimatedInput := estimateChatTokens(body)
	requestID := firstNonEmpty(strArg(body, "_llm_request_id"), "llm_req_"+randomSuffix(16))
	reservation, grantedMax, err := reserveUsage(ctx.AppDB(), ident, provider, model, estimatedInput, int64Arg(body, "max_tokens"), requestID, policies, true)
	if err != nil {
		if isPolicyError(err) {
			ctx.EmitWithProject("llm.policy.limit_exceeded", ident.ProjectID, map[string]any{
				"request_id": requestID, "model": model, "subject_type": ident.SubjectType, "subject_id": ident.SubjectID, "error": err.Error(),
			})
		}
		_ = dbAudit(ctx.AppDB(), ident, "request.denied", provider, model, "denied", err.Error(), map[string]any{"request_id": requestID})
		return nil, err
	}
	if int64Arg(body, "max_tokens") == 0 && grantedMax > 0 {
		body["max_tokens"] = grantedMax
	}

	var lastErr error
	var result *chatResult
	used := attempts[0]
	attemptDetails := []any{}
	failedAttempts := 0
	for i, attempt := range attempts {
		if err := policiesAllow(policies, attempt.Provider, attempt.Model, int64Arg(body, "max_tokens")); err != nil {
			continue
		}
		used = attempt
		if i > 0 {
			if err := ensureModelSupportsRequest(ctx.AppDB(), ident.ProjectID, attempt.Provider, attempt.Model, body); err != nil {
				lastErr = err
				failedAttempts++
				attemptDetails = append(attemptDetails, map[string]any{"provider": attempt.Provider, "model": attempt.Model, "status": "skipped", "error": err.Error()})
				continue
			}
		}
		cfg, err := providerConfigFor(ctx, ident.ProjectID, attempt.Provider)
		if err == nil {
			attemptBody := cloneMap(body)
			attemptBody["model"] = attempt.Model
			var callResult *chatResult
			if cfg.Provider == "openai-codex" {
				callResult, err = a.callOpenAICodexIntegration(reqCtx, ctx, cfg, attemptBody)
			} else {
				var key string
				key, err = resolveProviderKey(ctx, cfg)
				if err == nil {
					callResult, err = a.callProvider(reqCtx, cfg, key, attemptBody)
				}
			}
			if err == nil {
				err = validateChatCompletion(callResult.Body)
			}
			if err == nil {
				result = callResult
			}
		}
		if err == nil {
			attemptDetails = append(attemptDetails, map[string]any{"provider": attempt.Provider, "model": attempt.Model, "status": "completed"})
			break
		}
		lastErr = err
		failedAttempts++
		attemptDetails = append(attemptDetails, map[string]any{"provider": attempt.Provider, "model": attempt.Model, "status": "failed", "error": err.Error()})
		retryable := isRetryableProviderError(err)
		ctx.EmitWithProject("llm.provider.failed", ident.ProjectID, map[string]any{
			"request_id": requestID, "provider": attempt.Provider, "model": attempt.Model,
			"subject_type": ident.SubjectType, "subject_id": ident.SubjectID,
			"error": err.Error(), "retryable": retryable, "failover": retryable && i+1 < len(attempts),
		})
		_ = dbAudit(ctx.AppDB(), ident, "provider.failed", attempt.Provider, attempt.Model, "error", err.Error(), map[string]any{"request_id": requestID, "retryable": retryable})
		if !retryable {
			break
		}
	}

	if result == nil {
		if lastErr == nil {
			lastErr = errors.New("no allowed provider route is available")
		}
		usageDetails := mergeUsageDetails(nil, attemptDetails, failedAttempts)
		usageEvent, finishErr := finishUsageReservationCost(ctx.AppDB(), reservation.ID, used.Provider, used.Model, estimatedInput, 0, "failed", "", 0, "", false, usageDetails)
		if finishErr != nil {
			return nil, fmt.Errorf("provider failed: %v; record usage: %w", lastErr, finishErr)
		}
		_ = dbAudit(ctx.AppDB(), ident, "request.failed", used.Provider, used.Model, "error", lastErr.Error(), map[string]any{"request_id": requestID, "usage_event_id": usageEvent.ID})
		event := usageEventPayload(usageEvent, map[string]any{"error": lastErr.Error()})
		ctx.EmitWithProject("llm.usage.recorded", ident.ProjectID, event)
		ctx.EmitWithProject("llm.request.failed", ident.ProjectID, event)
		return nil, lastErr
	}
	if result.RequestTokens == 0 {
		result.RequestTokens = estimatedInput
	}
	result.UsageDetails = mergeUsageDetails(result.UsageDetails, attemptDetails, failedAttempts)
	usageEvent, err := finishUsageReservationCost(ctx.AppDB(), reservation.ID, used.Provider, used.Model, result.RequestTokens, result.ResponseTokens, "completed", result.RequestID,
		result.ProviderCostMicrounits, result.ProviderCostCurrency, result.ProviderCostReported, result.UsageDetails)
	if err != nil {
		return nil, err
	}
	_ = dbAudit(ctx.AppDB(), ident, "request.completed", used.Provider, used.Model, "completed", "", map[string]any{"request_id": requestID, "provider_request_id": result.RequestID, "usage_event_id": usageEvent.ID})
	event := usageEventPayload(usageEvent, nil)
	ctx.EmitWithProject("llm.usage.recorded", ident.ProjectID, event)
	ctx.EmitWithProject("llm.request.completed", ident.ProjectID, event)
	result.UsageEvent = usageEvent
	return result, nil
}

func validateChatCompletion(raw []byte) error {
	var response struct {
		Choices []json.RawMessage `json:"choices"`
		Error   json.RawMessage   `json:"error"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return providerError(http.StatusBadGateway, "provider returned malformed JSON")
	}
	if len(response.Error) > 0 && string(response.Error) != "null" {
		return providerError(http.StatusBadGateway, "provider returned an error payload with a successful HTTP status")
	}
	if len(response.Choices) == 0 {
		return providerError(http.StatusBadGateway, "provider response contains no completion choices")
	}
	return nil
}

func (a *App) callProvider(ctx context.Context, cfg *ProviderConfig, apiKey string, body map[string]any) (*chatResult, error) {
	if cfg.Provider == "anthropic" || providerUsesAnthropicMessages(cfg.Provider, strArg(body, "model")) {
		return a.callAnthropic(ctx, cfg, apiKey, body)
	}
	return a.callOpenAICompatible(ctx, cfg, apiKey, body)
}

func (a *App) callOpenAICodexIntegration(reqCtx context.Context, appCtx *sdk.AppCtx, cfg *ProviderConfig, body map[string]any) (*chatResult, error) {
	if err := validateBoundProviderConnection(appCtx, cfg); err != nil {
		return nil, err
	}
	if err := reqCtx.Err(); err != nil {
		return nil, err
	}
	outBody := cloneMap(body)
	delete(outBody, "_llm_request_id")
	delete(outBody, "request_id")
	outBody["model"] = upstreamModel(cfg.Provider, strArg(body, "model"))
	executed, err := appCtx.PlatformAPI().ExecuteIntegrationTool(cfg.ConnectionID, "chat_completion", outBody)
	if err != nil {
		return nil, err
	}
	if err := reqCtx.Err(); err != nil {
		return nil, err
	}
	if executed == nil {
		return nil, providerError(http.StatusBadGateway, "OpenAI Codex integration returned no result")
	}
	status := executed.Status
	if status == 0 {
		if executed.Success {
			status = http.StatusOK
		} else {
			status = http.StatusBadGateway
		}
	}
	raw := append([]byte(nil), executed.Data...)
	result := &chatResult{Status: status, Body: raw}
	metering := parseProviderMetering(raw)
	result.RequestTokens = metering.InputTokens
	result.ResponseTokens = metering.OutputTokens
	result.ProviderCostMicrounits = metering.CostMicrounits
	result.ProviderCostCurrency = metering.CostCurrency
	result.ProviderCostReported = metering.CostReported
	result.UsageDetails = providerCostDetails(metering)
	var envelope struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(raw, &envelope)
	result.RequestID = firstNonEmpty(envelope.ID, "llm_"+randomSuffix(16))
	if !executed.Success || status < 200 || status >= 300 {
		return result, providerError(status, string(raw))
	}
	return result, nil
}

func validateBoundProviderConnection(ctx *sdk.AppCtx, cfg *ProviderConfig) error {
	if cfg == nil || cfg.AuthMode != "customer_owned" || cfg.ConnectionID <= 0 {
		return errors.New("OpenAI Codex provider requires a bound customer-owned integration")
	}
	if ctx == nil || ctx.PlatformAPI() == nil {
		return errors.New("OpenAI Codex provider requires platform API access")
	}
	connection, err := ctx.PlatformAPI().GetConnection(cfg.ConnectionID)
	if err != nil {
		return err
	}
	if connection == nil || connection.ID != cfg.ConnectionID || !connectionStatusUsable(connection.Status) {
		return fmt.Errorf("connection %d is not connected", cfg.ConnectionID)
	}
	if cfg.ProjectID != "" && connection.ProjectID != "" && connection.ProjectID != cfg.ProjectID {
		return fmt.Errorf("connection %d belongs to another project", cfg.ConnectionID)
	}
	if !providerConnectionCompatible(cfg.Provider, connection.AppSlug) {
		return fmt.Errorf("connection %d (%s) is not compatible with provider %q", cfg.ConnectionID, connection.AppSlug, cfg.Provider)
	}
	return nil
}

func providerUsesAnthropicMessages(provider, model string) bool {
	if normalizeProvider(provider) != "opencode-go" {
		return false
	}
	nativeModel := strings.ToLower(upstreamModel("opencode-go", model))
	return strings.HasPrefix(nativeModel, "minimax-") || strings.HasPrefix(nativeModel, "qwen")
}

func (a *App) callOpenAICompatible(ctx context.Context, cfg *ProviderConfig, apiKey string, body map[string]any) (*chatResult, error) {
	outBody := cloneMap(body)
	delete(outBody, "_llm_request_id")
	delete(outBody, "request_id")
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
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	result := &chatResult{Status: resp.StatusCode, Body: raw, RequestID: resp.Header.Get("X-Request-Id")}
	metering := parseProviderMetering(raw)
	result.RequestTokens = metering.InputTokens
	result.ResponseTokens = metering.OutputTokens
	result.ProviderCostMicrounits = metering.CostMicrounits
	result.ProviderCostCurrency = metering.CostCurrency
	result.ProviderCostReported = metering.CostReported
	result.UsageDetails = providerCostDetails(metering)
	if result.RequestID == "" {
		result.RequestID = "llm_" + randomSuffix(16)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return result, providerError(resp.StatusCode, string(raw))
	}
	return result, nil
}

func (a *App) callAnthropic(ctx context.Context, cfg *ProviderConfig, apiKey string, body map[string]any) (*chatResult, error) {
	anthropicReq, err := openAIChatToAnthropic(cfg.Provider, body)
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
	if cfg.Provider != "anthropic" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	req.Header.Set("Anthropic-Version", "2023-06-01")
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &chatResult{Status: resp.StatusCode, Body: raw, RequestID: resp.Header.Get("Request-Id")}, providerError(resp.StatusCode, string(raw))
	}
	openAI, inTok, outTok, err := anthropicToOpenAI(raw, strArg(body, "model"))
	if err != nil {
		return nil, err
	}
	metering := parseProviderMetering(raw)
	result := &chatResult{Status: resp.StatusCode, Body: openAI, RequestTokens: inTok, ResponseTokens: outTok, RequestID: resp.Header.Get("Request-Id"), UsageDetails: providerCostDetails(metering)}
	if result.RequestID == "" {
		result.RequestID = "llm_" + randomSuffix(16)
	}
	return result, nil
}

func (a *App) handleTokens(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		q := r.URL.Query()
		rows, err := dbTokensList(globalCtx.AppDB(), projectFromRequest(r), q.Get("subject_type"), q.Get("subject_id"), q.Get("include_revoked") == "true")
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"tokens": rows})
	case http.MethodPost:
		var args map[string]any
		if err := json.NewDecoder(r.Body).Decode(&args); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		args["project_id"] = projectFromRequest(r)
		out, err := createToken(globalCtx.AppDB(), args)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, out)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (a *App) handleTokensRevoke(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var args map[string]any
	if err := json.NewDecoder(r.Body).Decode(&args); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	args["project_id"] = projectFromRequest(r)
	out, err := revokeToken(globalCtx.AppDB(), args)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, out)
}

func (a *App) handleTokensRevokeSubject(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var args map[string]any
	if err := json.NewDecoder(r.Body).Decode(&args); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	args["project_id"] = projectFromRequest(r)
	out, err := revokeSubjectTokens(globalCtx.AppDB(), args)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, out)
}

func (a *App) handleSubjectSuspend(w http.ResponseWriter, r *http.Request) {
	a.handleSubjectStatus(w, r, true)
}

func (a *App) handleSubjectResume(w http.ResponseWriter, r *http.Request) {
	a.handleSubjectStatus(w, r, false)
}

func (a *App) handleSubjectStatus(w http.ResponseWriter, r *http.Request, disabled bool) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var args map[string]any
	if err := json.NewDecoder(r.Body).Decode(&args); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	args["project_id"] = projectFromRequest(r)
	pol, err := dbPolicySetDisabled(globalCtx.AppDB(), projectFromArgs(args), strArg(args, "subject_type"), strArg(args, "subject_id"), disabled)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"policy": pol})
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
		args["project_id"] = projectFromRequest(r)
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

func (a *App) handleModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	q := r.URL.Query()
	rows, err := dbProviderModelsList(globalCtx.AppDB(), providerModelFilter{
		ProjectID: projectFromRequest(r),
		Provider:  normalizeProvider(q.Get("provider")),
		Status:    firstNonEmpty(q.Get("status"), "active"),
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"models": rows})
}

func (a *App) handleModelsSync(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var args map[string]any
	if r.Body != nil && r.Body != http.NoBody {
		if err := json.NewDecoder(r.Body).Decode(&args); err != nil && err != io.EOF {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
	}
	if args == nil {
		args = map[string]any{}
	}
	args["project_id"] = projectFromRequest(r)
	projectID := projectFromArgs(args)
	provider := normalizeProvider(firstNonEmpty(strArg(args, "provider"), r.URL.Query().Get("provider")))
	results := a.syncProviderModelsWithContext(r.Context(), globalCtx, projectID, provider)
	writeJSON(w, map[string]any{"results": results})
}

func (a *App) handleTestChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var args map[string]any
	if err := json.NewDecoder(io.LimitReader(r.Body, maxImageRequestBytes)).Decode(&args); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	projectID := projectFromRequest(r)
	ident := &TokenIdentity{ProjectID: projectID, SubjectType: "app", SubjectID: "llm-panel"}
	args["_llm_request_id"] = "llm_test_" + randomSuffix(16)
	res, err := a.executeChatContext(r.Context(), globalCtx, ident, args)
	if err != nil {
		status, typ := errorStatus(err)
		writeOpenAIError(w, status, typ, err.Error())
		return
	}
	copyProviderHeaders(w, res)
	w.WriteHeader(res.Status)
	_, _ = w.Write(res.Body)
}

func (a *App) handlePolicy(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		pol, err := dbPolicyGet(globalCtx.AppDB(), projectFromRequest(r), subjectTypeFromRequest(r), subjectIDFromRequest(r))
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
		args["project_id"] = projectFromRequest(r)
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

func (a *App) handleUsageEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	q := r.URL.Query()
	rows, err := dbUsageEventsList(globalCtx.AppDB(), usageFilter{
		ProjectID: projectFromRequest(r), Period: firstNonEmpty(q.Get("period"), currentPeriod()),
		SubjectType: q.Get("subject_type"), SubjectID: q.Get("subject_id"), Model: q.Get("model"),
	}, intArgQuery(r, "limit", 100))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"usage_events": rows})
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

func (a *App) toolChatCompleteCtx(reqCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid := projectFromArgs(args)
	ident := &TokenIdentity{ProjectID: pid, SubjectType: firstNonEmpty(strArg(args, "subject_type"), "app"), SubjectID: firstNonEmpty(strArg(args, "subject_id"), "mcp")}
	args["_llm_request_id"] = firstNonEmpty(strArg(args, "request_id"), "llm_req_"+randomSuffix(16))
	res, err := a.executeChatContext(reqCtx, ctx, ident, args)
	if err != nil {
		return nil, err
	}
	var out any
	if err := json.Unmarshal(res.Body, &out); err != nil {
		return map[string]any{"status": res.Status, "body": string(res.Body)}, nil
	}
	if object, ok := out.(map[string]any); ok && res.UsageEvent != nil {
		object["apteva_usage"] = usageEventPayload(res.UsageEvent, nil)
	}
	return out, nil
}

func (a *App) toolChatComplete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return a.toolChatCompleteCtx(context.Background(), ctx, args)
}

func (a *App) toolModelsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	ident := &TokenIdentity{ProjectID: projectFromArgs(args), SubjectType: strArg(args, "subject_type"), SubjectID: strArg(args, "subject_id")}
	policies, err := dbEffectivePolicies(ctx.AppDB(), ident)
	if err != nil {
		return nil, err
	}
	models, err := dbProviderModelsList(ctx.AppDB(), providerModelFilter{ProjectID: ident.ProjectID, Status: "active"})
	if err != nil {
		return nil, err
	}
	filtered := models[:0]
	for _, model := range models {
		if policiesAllow(policies, model.Provider, model.GatewayModel, 0) == nil {
			filtered = append(filtered, model)
		}
	}
	return map[string]any{"models": filtered, "policies": policies}, nil
}

func (a *App) toolModelsSyncCtx(reqCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return map[string]any{"results": a.syncProviderModelsWithContext(reqCtx, ctx, projectFromArgs(args), normalizeProvider(strArg(args, "provider")))}, nil
}

func (a *App) toolEmbeddingsCtx(reqCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	ident := &TokenIdentity{ProjectID: projectFromArgs(args), SubjectType: firstNonEmpty(strArg(args, "subject_type"), "app"), SubjectID: firstNonEmpty(strArg(args, "subject_id"), "mcp")}
	args["_llm_request_id"] = firstNonEmpty(strArg(args, "request_id"), "llm_req_"+randomSuffix(16))
	res, err := a.executeEmbeddingsContext(reqCtx, ctx, ident, args)
	if err != nil {
		return nil, err
	}
	var out any
	if err := json.Unmarshal(res.Body, &out); err != nil {
		return nil, err
	}
	return out, nil
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
	pol, err := dbPolicyGet(ctx.AppDB(), projectFromArgs(args), strArg(args, "subject_type"), strArg(args, "subject_id"))
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

func (a *App) toolTokensList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	rows, err := dbTokensList(ctx.AppDB(), projectFromArgs(args), strArg(args, "subject_type"), strArg(args, "subject_id"), boolArg(args, "include_revoked"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"tokens": rows}, nil
}

func (a *App) toolTokenRevoke(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	if strArg(args, "subject_type") != "" || strArg(args, "subject_id") != "" {
		return revokeSubjectTokens(ctx.AppDB(), args)
	}
	return revokeToken(ctx.AppDB(), args)
}

func (a *App) toolSubjectSuspend(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pol, err := dbPolicySetDisabled(ctx.AppDB(), projectFromArgs(args), strArg(args, "subject_type"), strArg(args, "subject_id"), true)
	if err != nil {
		return nil, err
	}
	return map[string]any{"policy": pol}, nil
}

func (a *App) toolSubjectResume(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pol, err := dbPolicySetDisabled(ctx.AppDB(), projectFromArgs(args), strArg(args, "subject_type"), strArg(args, "subject_id"), false)
	if err != nil {
		return nil, err
	}
	return map[string]any{"policy": pol}, nil
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
	parsedBaseURL, err := url.Parse(baseURL)
	if err != nil || parsedBaseURL.Host == "" || (parsedBaseURL.Scheme != "http" && parsedBaseURL.Scheme != "https") {
		return nil, errors.New("base_url must be an absolute http or https URL")
	}
	authMode := firstNonEmpty(strArg(args, "auth_mode"), "platform_shared")
	switch authMode {
	case "platform_shared", "customer_owned", "provider_aggregator":
	default:
		return nil, fmt.Errorf("unsupported auth_mode %q", authMode)
	}
	connectionID := int64Arg(args, "connection_id")
	if authMode == "customer_owned" && connectionID <= 0 {
		return nil, errors.New("customer_owned provider requires connection_id")
	}
	if authMode == "customer_owned" && strings.TrimSpace(projectID) == "" {
		return nil, errors.New("customer_owned provider requires project_id")
	}
	keyRef := strArg(args, "key_ref")
	if keyRef == "" && authMode != "customer_owned" {
		keyRef = defaultProviderKeyRef(provider)
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
	_, err = db.Exec(`
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
		projectID, provider, baseURL, authMode, connectionID, keyRef, boolToInt(enabled), priority, string(metadata))
	if err != nil {
		return nil, err
	}
	return dbProviderConfigFor(db, projectID, provider)
}

func providerConfigFor(ctx *sdk.AppCtx, projectID, provider string) (*ProviderConfig, error) {
	if ctx == nil || ctx.AppDB() == nil {
		return nil, errors.New("llm app context unavailable")
	}
	if projectID != "" {
		cfg, err := dbProviderConfigFor(ctx.AppDB(), projectID, provider)
		if err == nil {
			if !cfg.Enabled {
				return nil, fmt.Errorf("provider %q is disabled for project", provider)
			}
			return cfg, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
	}
	if cfg := boundProviderConfig(ctx, projectID, provider); cfg != nil {
		return cfg, nil
	}
	if cfg, err := dbProviderConfigFor(ctx.AppDB(), "", provider); err == nil {
		if !cfg.Enabled {
			return nil, fmt.Errorf("provider %q is disabled globally", provider)
		}
		return cfg, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	return nil, fmt.Errorf("no enabled provider config or bound provider integration for %q", provider)
}

func dbProviderConfigFor(db *sql.DB, projectID, provider string) (*ProviderConfig, error) {
	row := db.QueryRow(`
		SELECT id, project_id, provider, base_url, auth_mode, connection_id, key_ref, enabled, priority, metadata_json,
		       COALESCE(created_at,''), COALESCE(updated_at,'')
		  FROM provider_configs
		 WHERE provider = ? AND project_id = ?
		 LIMIT 1`, provider, projectID)
	cfg, err := scanProviderConfig(row)
	if err != nil {
		return nil, err
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
	projectRows := map[string]ProviderConfig{}
	globalRows := map[string]ProviderConfig{}
	for _, row := range rows {
		if row.ProjectID == projectID && projectID != "" {
			projectRows[row.Provider] = row
		} else if row.ProjectID == "" {
			globalRows[row.Provider] = row
		}
	}
	selected := map[string]ProviderConfig{}
	for provider, row := range projectRows {
		selected[provider] = row
	}
	for provider := range providerIntegrationRoles() {
		if _, exists := selected[provider]; exists {
			continue
		}
		if cfg := boundProviderConfig(ctx, projectID, provider); cfg != nil {
			selected[provider] = *cfg
		}
	}
	for provider, row := range globalRows {
		if _, exists := selected[provider]; !exists {
			selected[provider] = row
		}
	}
	rows = rows[:0]
	for _, row := range selected {
		rows = append(rows, row)
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
		"anthropic":    "anthropic_provider",
		"fireworks":    "fireworks_provider",
		"openai":       "openai_provider",
		"openrouter":   "openrouter_provider",
		"opencode-go":  "opencode_go_provider",
		"openai-codex": "openai_codex_provider",
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

type providerModelFilter struct {
	ProjectID string
	Provider  string
	Status    string
}

func (a *App) syncProviderModels(ctx *sdk.AppCtx, projectID, provider string) []ModelSyncResult {
	return a.syncProviderModelsWithContext(context.Background(), ctx, projectID, provider)
}

func (a *App) syncProviderModelsWithContext(reqCtx context.Context, ctx *sdk.AppCtx, projectID, provider string) []ModelSyncResult {
	results := []ModelSyncResult{}
	if ctx == nil || ctx.AppDB() == nil {
		return []ModelSyncResult{{Provider: provider, Status: "error", Error: "llm app context unavailable"}}
	}
	providers := []ProviderConfig{}
	if provider != "" {
		cfg, err := providerConfigFor(ctx, projectID, provider)
		if err != nil {
			return []ModelSyncResult{{Provider: provider, Status: "error", Error: err.Error()}}
		}
		providers = append(providers, *cfg)
	} else {
		rows, err := providerConfigsList(ctx, projectID)
		if err != nil {
			return []ModelSyncResult{{Status: "error", Error: err.Error()}}
		}
		providers = rows
	}
	for _, cfg := range providers {
		if !cfg.Enabled {
			continue
		}
		models, err := a.discoverProviderModels(reqCtx, ctx, &cfg)
		if err != nil {
			results = append(results, ModelSyncResult{Provider: cfg.Provider, Status: "error", Error: err.Error()})
			continue
		}
		if err := dbProviderModelsReplace(ctx.AppDB(), projectID, cfg.Provider, models); err != nil {
			results = append(results, ModelSyncResult{Provider: cfg.Provider, Status: "error", Error: err.Error()})
			continue
		}
		if _, err := a.refreshProviderRates(ctx.AppDB(), projectID, cfg.Provider); err != nil {
			results = append(results, ModelSyncResult{Provider: cfg.Provider, Status: "error", Error: "refresh provider rates: " + err.Error()})
			continue
		}
		rows, err := dbProviderModelsList(ctx.AppDB(), providerModelFilter{ProjectID: projectID, Provider: cfg.Provider, Status: "active"})
		if err != nil {
			results = append(results, ModelSyncResult{Provider: cfg.Provider, Status: "error", Error: err.Error()})
			continue
		}
		results = append(results, ModelSyncResult{Provider: cfg.Provider, Status: "ok", ModelCount: len(rows), Models: rows})
	}
	return results
}

func (a *App) discoverProviderModels(reqCtx context.Context, appCtx *sdk.AppCtx, cfg *ProviderConfig) ([]ProviderModel, error) {
	if cfg.Provider == "openai-codex" {
		return a.discoverOpenAICodexModels(reqCtx, appCtx, cfg)
	}
	key, err := resolveProviderKey(appCtx, cfg)
	if err != nil {
		return nil, err
	}
	models := []ProviderModel{}
	seen := map[string]bool{}
	afterID := ""
	for page := 0; page < 20; page++ {
		endpoint, err := url.Parse(strings.TrimRight(cfg.BaseURL, "/") + "/models")
		if err != nil {
			return nil, err
		}
		if afterID != "" {
			q := endpoint.Query()
			q.Set("after_id", afterID)
			q.Set("limit", "1000")
			endpoint.RawQuery = q.Encode()
		}
		req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, endpoint.String(), nil)
		if err != nil {
			return nil, err
		}
		if cfg.Provider == "anthropic" {
			req.Header.Set("X-Api-Key", key)
			req.Header.Set("Anthropic-Version", "2023-06-01")
		} else {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		req.Header.Set("Accept", "application/json")
		resp, err := a.httpClient.Do(req)
		if err != nil {
			return nil, err
		}
		raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
		resp.Body.Close()
		if readErr != nil {
			return nil, readErr
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, providerError(resp.StatusCode, string(raw))
		}
		pageModels, hasMore, lastID := parseProviderModelsPage(cfg.Provider, raw)
		for _, model := range pageModels {
			if !seen[model.ModelID] {
				models = append(models, model)
				seen[model.ModelID] = true
			}
		}
		if !hasMore || lastID == "" || lastID == afterID {
			break
		}
		afterID = lastID
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("provider %q returned no models", cfg.Provider)
	}
	return models, nil
}

const openAICodexCatalogClientVersion = "0.144.0"

func (a *App) discoverOpenAICodexModels(reqCtx context.Context, appCtx *sdk.AppCtx, cfg *ProviderConfig) ([]ProviderModel, error) {
	if err := validateBoundProviderConnection(appCtx, cfg); err != nil {
		return nil, err
	}
	credentials, err := appCtx.PlatformAPI().GetConnectionCredentials(cfg.ConnectionID)
	if err != nil {
		return nil, err
	}
	if credentials == nil || !providerConnectionCompatible(cfg.Provider, credentials.Slug) {
		return nil, fmt.Errorf("connection %d credentials are not compatible with provider %q", cfg.ConnectionID, cfg.Provider)
	}
	accessToken := strings.TrimSpace(credentials.Fields["access_token"])
	if accessToken == "" {
		return nil, fmt.Errorf("connection %d has no access_token credential field", cfg.ConnectionID)
	}
	endpoint, err := url.Parse(strings.TrimRight(cfg.BaseURL, "/") + "/models")
	if err != nil {
		return nil, err
	}
	query := endpoint.Query()
	query.Set("client_version", openAICodexCatalogClientVersion)
	endpoint.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	if accountID := strings.TrimSpace(credentials.Fields["account_id"]); accountID != "" {
		req.Header.Set("ChatGPT-Account-ID", accountID)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "apteva-llm/codex-catalog-"+openAICodexCatalogClientVersion)
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, providerError(resp.StatusCode, string(raw))
	}
	models := parseOpenAICodexModels(cfg.Provider, raw)
	if len(models) == 0 {
		return nil, errors.New("OpenAI Codex model catalog contained no visible models")
	}
	return models, nil
}

func parseOpenAICodexModels(provider string, raw []byte) []ProviderModel {
	var payload struct {
		Models []json.RawMessage `json:"models"`
	}
	if json.Unmarshal(raw, &payload) != nil {
		return nil
	}
	models := make([]ProviderModel, 0, len(payload.Models))
	priorities := make(map[string]int64, len(payload.Models))
	for _, item := range payload.Models {
		var model map[string]any
		if json.Unmarshal(item, &model) != nil {
			continue
		}
		id := strings.TrimSpace(strAny(model["slug"]))
		visibility := strings.TrimSpace(strAny(model["visibility"]))
		if id == "" || !strings.EqualFold(visibility, "list") || strings.EqualFold(id, "gpt-5.6-luna") {
			continue
		}
		priorities[id] = int64FromAny(model["priority"])
		capabilities := map[string]any{}
		for _, key := range []string{"context_window", "max_context_window", "effective_context_window_percent", "default_reasoning_level", "supported_reasoning_levels", "supports_parallel_tool_calls", "supports_reasoning_summaries", "supports_image_detail_original", "supports_search_tool"} {
			if value, ok := model[key]; ok {
				capabilities[key] = value
			}
		}
		models = append(models, ProviderModel{
			Provider:         provider,
			ModelID:          id,
			DisplayName:      firstNonEmpty(strings.TrimSpace(strAny(model["display_name"])), id),
			GatewayModel:     gatewayModelID(provider, id),
			Capabilities:     jsonFromAny(capabilities),
			ContextWindow:    int64FromAny(model["context_window"]),
			InputModalities:  jsonFromAny(firstNonNil(model["input_modalities"], []any{})),
			OutputModalities: json.RawMessage(`["text"]`),
			Status:           "active",
			Raw:              append(json.RawMessage(nil), item...),
		})
	}
	sort.SliceStable(models, func(i, j int) bool {
		left := priorities[models[i].ModelID]
		right := priorities[models[j].ModelID]
		if left != right {
			return left < right
		}
		return models[i].ModelID < models[j].ModelID
	})
	return models
}

func parseProviderModelsPage(provider string, raw []byte) ([]ProviderModel, bool, string) {
	var envelope struct {
		HasMore bool   `json:"has_more"`
		LastID  string `json:"last_id"`
	}
	_ = json.Unmarshal(raw, &envelope)
	return parseProviderModels(provider, raw), envelope.HasMore, envelope.LastID
}

func parseProviderModels(provider string, raw []byte) []ProviderModel {
	var envelope struct {
		Data []json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil || len(envelope.Data) == 0 {
		var arr []json.RawMessage
		if err := json.Unmarshal(raw, &arr); err == nil {
			envelope.Data = arr
		}
	}
	out := []ProviderModel{}
	seen := map[string]bool{}
	for _, item := range envelope.Data {
		model, ok := providerModelFromRaw(provider, item)
		if !ok || seen[model.ModelID] {
			continue
		}
		seen[model.ModelID] = true
		out = append(out, model)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].ModelID < out[j].ModelID
	})
	return out
}

func providerModelFromRaw(provider string, raw json.RawMessage) (ProviderModel, bool) {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return ProviderModel{}, false
	}
	id := firstNonEmpty(strAny(m["id"]), strAny(m["name"]))
	if id == "" {
		return ProviderModel{}, false
	}
	display := firstNonEmpty(strAny(m["display_name"]), strAny(m["name"]), id)
	capabilities := json.RawMessage(`{}`)
	if v, ok := m["capabilities"]; ok {
		capabilities = jsonFromAny(v)
	}
	inputModalities := json.RawMessage(`[]`)
	if v, ok := m["input_modalities"]; ok {
		inputModalities = jsonFromAny(v)
	}
	outputModalities := json.RawMessage(`[]`)
	if v, ok := m["output_modalities"]; ok {
		outputModalities = jsonFromAny(v)
	}
	rawCopy := make([]byte, len(raw))
	copy(rawCopy, raw)
	return ProviderModel{
		Provider:         provider,
		ModelID:          id,
		DisplayName:      display,
		GatewayModel:     gatewayModelID(provider, id),
		Capabilities:     capabilities,
		ContextWindow:    int64FromAny(firstNonNil(m["context_window"], m["context_length"], m["max_context_length"])),
		InputModalities:  inputModalities,
		OutputModalities: outputModalities,
		Status:           "active",
		Raw:              json.RawMessage(rawCopy),
	}, true
}

func dbProviderModelsReplace(db *sql.DB, projectID, provider string, models []ProviderModel) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE provider_models SET status='inactive', updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND provider=?`, projectID, provider); err != nil {
		return err
	}
	for _, model := range models {
		if model.ModelID == "" {
			continue
		}
		if model.Capabilities == nil {
			model.Capabilities = json.RawMessage(`{}`)
		}
		if model.InputModalities == nil {
			model.InputModalities = json.RawMessage(`[]`)
		}
		if model.OutputModalities == nil {
			model.OutputModalities = json.RawMessage(`[]`)
		}
		if model.Raw == nil {
			model.Raw = json.RawMessage(`{}`)
		}
		if model.GatewayModel == "" {
			model.GatewayModel = gatewayModelID(provider, model.ModelID)
		}
		if model.Status == "" {
			model.Status = "active"
		}
		if _, err := tx.Exec(`
			INSERT INTO provider_models
				(project_id, provider, model_id, display_name, gateway_model, capabilities_json, context_window,
				 input_modalities_json, output_modalities_json, status, raw_json, last_seen_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
			ON CONFLICT(project_id, provider, model_id) DO UPDATE SET
				display_name=excluded.display_name,
				gateway_model=excluded.gateway_model,
				capabilities_json=excluded.capabilities_json,
				context_window=excluded.context_window,
				input_modalities_json=excluded.input_modalities_json,
				output_modalities_json=excluded.output_modalities_json,
				status=excluded.status,
				raw_json=excluded.raw_json,
				last_seen_at=CURRENT_TIMESTAMP,
				updated_at=CURRENT_TIMESTAMP`,
			projectID, provider, model.ModelID, model.DisplayName, model.GatewayModel, string(model.Capabilities), model.ContextWindow,
			string(model.InputModalities), string(model.OutputModalities), model.Status, string(model.Raw)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func dbProviderModelsList(db *sql.DB, f providerModelFilter) ([]ProviderModel, error) {
	conds := []string{"project_id = ?"}
	args := []any{f.ProjectID}
	if f.Provider != "" {
		conds = append(conds, "provider = ?")
		args = append(args, f.Provider)
	}
	if f.Status != "" {
		conds = append(conds, "status = ?")
		args = append(args, f.Status)
	}
	rows, err := db.Query(`
		SELECT id, project_id, provider, model_id, display_name, gateway_model, capabilities_json, context_window,
		       input_modalities_json, output_modalities_json, status, raw_json,
		       COALESCE(last_seen_at,''), COALESCE(created_at,''), COALESCE(updated_at,'')
		  FROM provider_models
		 WHERE `+strings.Join(conds, " AND ")+`
		 ORDER BY provider ASC, model_id ASC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ProviderModel{}
	for rows.Next() {
		var model ProviderModel
		var caps, input, output, raw string
		if err := rows.Scan(&model.ID, &model.ProjectID, &model.Provider, &model.ModelID, &model.DisplayName, &model.GatewayModel,
			&caps, &model.ContextWindow, &input, &output, &model.Status, &raw, &model.LastSeenAt, &model.CreatedAt, &model.UpdatedAt); err != nil {
			return nil, err
		}
		model.Capabilities = json.RawMessage(firstNonEmpty(caps, "{}"))
		model.InputModalities = json.RawMessage(firstNonEmpty(input, "[]"))
		model.OutputModalities = json.RawMessage(firstNonEmpty(output, "[]"))
		model.Raw = json.RawMessage(firstNonEmpty(raw, "{}"))
		out = append(out, model)
	}
	return out, rows.Err()
}

func ensureModelSupportsRequest(db *sql.DB, projectID, provider, model string, body map[string]any) error {
	if !requestContainsImage(body) {
		return nil
	}
	var raw string
	err := db.QueryRow(`SELECT input_modalities_json FROM provider_models
		WHERE (project_id=? OR project_id='') AND provider=? AND model_id=? AND status='active'
		ORDER BY CASE WHEN project_id=? THEN 0 ELSE 1 END LIMIT 1`,
		projectID, provider, upstreamModel(provider, model), projectID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	modalities := []string{}
	if json.Unmarshal([]byte(raw), &modalities) != nil || len(modalities) == 0 {
		return nil
	}
	for _, modality := range modalities {
		switch strings.ToLower(strings.TrimSpace(modality)) {
		case "image", "images", "vision", "image_url", "input_image":
			return nil
		}
	}
	return userError(fmt.Sprintf("model %q does not support image input according to its provider metadata", model))
}

func dbPolicyGet(db *sql.DB, projectID, subjectType, subjectID string) (*Policy, error) {
	var (
		pol             Policy
		allowedModels   string
		blockedModels   string
		allowedProvider string
		limits          string
		fallback        string
		disabled        int
	)
	err := db.QueryRow(`
		SELECT id, project_id, subject_type, subject_id, allowed_models_json, blocked_models_json,
		       allowed_providers_json, limits_json, disabled, fallback_policy_json
		  FROM policies WHERE project_id = ? AND subject_type = ? AND subject_id = ?`,
		projectID, strings.TrimSpace(subjectType), strings.TrimSpace(subjectID)).
		Scan(&pol.ID, &pol.ProjectID, &pol.SubjectType, &pol.SubjectID, &allowedModels, &blockedModels, &allowedProvider, &limits, &disabled, &fallback)
	if err == sql.ErrNoRows {
		return &Policy{ProjectID: projectID, SubjectType: strings.TrimSpace(subjectType), SubjectID: strings.TrimSpace(subjectID), FallbackPolicy: json.RawMessage(`{}`)}, nil
	}
	if err != nil {
		return nil, err
	}
	for name, raw := range map[string]string{
		"allowed_models": allowedModels, "blocked_models": blockedModels,
		"allowed_providers": allowedProvider, "limits": limits,
	} {
		var target any
		switch name {
		case "allowed_models":
			target = &pol.AllowedModels
		case "blocked_models":
			target = &pol.BlockedModels
		case "allowed_providers":
			target = &pol.AllowedProviders
		case "limits":
			target = &pol.Limits
		}
		if err := json.Unmarshal([]byte(raw), target); err != nil {
			return nil, fmt.Errorf("policy %s is invalid: %w", name, err)
		}
	}
	pol.Disabled = disabled != 0
	pol.FallbackPolicy = json.RawMessage(firstNonEmpty(fallback, "{}"))
	return &pol, nil
}

func dbPolicySet(db *sql.DB, projectID string, args map[string]any) (*Policy, error) {
	if strings.TrimSpace(projectID) == "" {
		return nil, errors.New("project_id is required")
	}
	subjectType := strArg(args, "subject_type")
	subjectID := strArg(args, "subject_id")
	if (subjectType == "") != (subjectID == "") {
		return nil, errors.New("subject_type and subject_id must be provided together")
	}
	pol := &Policy{
		ProjectID:        projectID,
		SubjectType:      subjectType,
		SubjectID:        subjectID,
		AllowedModels:    stringSlice(args["allowed_models"]),
		BlockedModels:    stringSlice(args["blocked_models"]),
		AllowedProviders: stringSlice(args["allowed_providers"]),
		Disabled:         boolArg(args, "disabled"),
		FallbackPolicy:   jsonFromAny(args["fallback_policy"]),
	}
	pol.Limits = limitsFromAny(args["limits"])
	if len(pol.FallbackPolicy) == 0 {
		pol.FallbackPolicy = json.RawMessage(`{}`)
	}
	var fallback FallbackPolicy
	if err := json.Unmarshal(pol.FallbackPolicy, &fallback); err != nil {
		return nil, errors.New("fallback_policy must be valid JSON")
	}
	for _, route := range fallback.Routes {
		if strings.TrimSpace(route.Model) == "" {
			return nil, errors.New("each fallback route requires model")
		}
		if normalizeProvider(firstNonEmpty(route.Provider, providerFromModel(route.Model))) == "" {
			return nil, errors.New("each fallback route requires provider or a provider-prefixed model")
		}
	}
	am, _ := json.Marshal(pol.AllowedModels)
	bm, _ := json.Marshal(pol.BlockedModels)
	ap, _ := json.Marshal(pol.AllowedProviders)
	lim, _ := json.Marshal(pol.Limits)
	_, err := db.Exec(`
		INSERT INTO policies
			(project_id, subject_type, subject_id, allowed_models_json, blocked_models_json, allowed_providers_json, limits_json, disabled, fallback_policy_json, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(project_id, subject_type, subject_id) DO UPDATE SET
			allowed_models_json=excluded.allowed_models_json,
			blocked_models_json=excluded.blocked_models_json,
			allowed_providers_json=excluded.allowed_providers_json,
			limits_json=excluded.limits_json,
			disabled=excluded.disabled,
			fallback_policy_json=excluded.fallback_policy_json,
			updated_at=CURRENT_TIMESTAMP`,
		projectID, subjectType, subjectID, string(am), string(bm), string(ap), string(lim), boolToInt(pol.Disabled), string(pol.FallbackPolicy))
	if err != nil {
		return nil, err
	}
	return dbPolicyGet(db, projectID, subjectType, subjectID)
}

func dbPolicySetDisabled(db *sql.DB, projectID, subjectType, subjectID string, disabled bool) (*Policy, error) {
	if projectID == "" || strings.TrimSpace(subjectType) == "" || strings.TrimSpace(subjectID) == "" {
		return nil, errors.New("project_id, subject_type, and subject_id are required")
	}
	existing, err := dbPolicyGet(db, projectID, subjectType, subjectID)
	if err != nil {
		return nil, err
	}
	fallback := existing.FallbackPolicy
	if len(fallback) == 0 {
		fallback = json.RawMessage(`{}`)
	}
	args := map[string]any{
		"project_id":        projectID,
		"subject_type":      subjectType,
		"subject_id":        subjectID,
		"allowed_models":    existing.AllowedModels,
		"blocked_models":    existing.BlockedModels,
		"allowed_providers": existing.AllowedProviders,
		"limits":            existing.Limits,
		"disabled":          disabled,
		"fallback_policy":   fallback,
	}
	return dbPolicySet(db, projectID, args)
}

func dbEffectivePolicies(db *sql.DB, ident *TokenIdentity) ([]*Policy, error) {
	projectPolicy, err := dbPolicyGet(db, ident.ProjectID, "", "")
	if err != nil {
		return nil, err
	}
	out := []*Policy{projectPolicy}
	if strings.TrimSpace(ident.SubjectType) == "" || strings.TrimSpace(ident.SubjectID) == "" {
		return out, nil
	}
	subjectPolicy, err := dbPolicyGet(db, ident.ProjectID, ident.SubjectType, ident.SubjectID)
	if err != nil {
		return nil, err
	}
	if subjectPolicy.ID != 0 || subjectPolicy.Disabled || hasPolicyRules(subjectPolicy) {
		out = append(out, subjectPolicy)
	}
	return out, nil
}

func hasPolicyRules(pol *Policy) bool {
	return len(pol.AllowedModels) > 0 || len(pol.BlockedModels) > 0 || len(pol.AllowedProviders) > 0 || pol.Limits != (Limits{})
}

func createToken(db *sql.DB, args map[string]any) (map[string]any, error) {
	projectID := projectFromArgs(args)
	if projectID == "" {
		return nil, errors.New("project_id is required")
	}
	subjectType := firstNonEmpty(strArg(args, "subject_type"), "project")
	subjectID := firstNonEmpty(strArg(args, "subject_id"), projectID)
	scopes := stringSlice(args["scopes"])
	if len(scopes) == 0 {
		scopes = []string{"chat", "embeddings", "models", "usage"}
	}
	for _, scope := range scopes {
		switch scope {
		case "chat", "embeddings", "models", "usage", "usage:project", "*":
		default:
			return nil, fmt.Errorf("unsupported token scope %q", scope)
		}
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

func dbTokensList(db *sql.DB, projectID, subjectType, subjectID string, includeRevoked bool) ([]TokenRecord, error) {
	conds := []string{"project_id = ?"}
	args := []any{projectID}
	if subjectType != "" {
		conds = append(conds, "subject_type = ?")
		args = append(args, subjectType)
	}
	if subjectID != "" {
		conds = append(conds, "subject_id = ?")
		args = append(args, subjectID)
	}
	if !includeRevoked {
		conds = append(conds, "revoked_at IS NULL")
	}
	rows, err := db.Query(`SELECT id, project_id, subject_type, subject_id, scopes_json,
		COALESCE(expires_at,''), COALESCE(created_at,''), COALESCE(revoked_at,'')
		FROM api_tokens WHERE `+strings.Join(conds, " AND ")+` ORDER BY id DESC LIMIT 500`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TokenRecord{}
	for rows.Next() {
		var token TokenRecord
		var scopesJSON string
		if err := rows.Scan(&token.ID, &token.ProjectID, &token.SubjectType, &token.SubjectID, &scopesJSON, &token.ExpiresAt, &token.CreatedAt, &token.RevokedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(scopesJSON), &token.Scopes)
		out = append(out, token)
	}
	return out, rows.Err()
}

func revokeToken(db *sql.DB, args map[string]any) (map[string]any, error) {
	tokenID := int64Arg(args, "token_id")
	token := strArg(args, "token")
	projectID := projectFromArgs(args)
	if tokenID <= 0 && token == "" {
		return nil, errors.New("token_id or token is required")
	}
	var res sql.Result
	var err error
	if tokenID > 0 {
		if projectID == "" {
			return nil, errors.New("project_id is required when revoking by token_id")
		}
		res, err = db.Exec(`UPDATE api_tokens SET revoked_at=CURRENT_TIMESTAMP WHERE id = ? AND project_id = ? AND revoked_at IS NULL`, tokenID, projectID)
	} else {
		if projectID != "" {
			res, err = db.Exec(`UPDATE api_tokens SET revoked_at=CURRENT_TIMESTAMP WHERE token_hash = ? AND project_id = ? AND revoked_at IS NULL`, hashToken(token), projectID)
		} else {
			res, err = db.Exec(`UPDATE api_tokens SET revoked_at=CURRENT_TIMESTAMP WHERE token_hash = ? AND revoked_at IS NULL`, hashToken(token))
		}
	}
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	return map[string]any{"revoked": n, "token_id": tokenID}, nil
}

func revokeSubjectTokens(db *sql.DB, args map[string]any) (map[string]any, error) {
	projectID := projectFromArgs(args)
	subjectType := strArg(args, "subject_type")
	subjectID := strArg(args, "subject_id")
	if projectID == "" || subjectType == "" || subjectID == "" {
		return nil, errors.New("project_id, subject_type, and subject_id are required")
	}
	res, err := db.Exec(`
		UPDATE api_tokens
		   SET revoked_at=CURRENT_TIMESTAMP
		 WHERE project_id = ? AND subject_type = ? AND subject_id = ? AND revoked_at IS NULL`,
		projectID, subjectType, subjectID)
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	return map[string]any{"revoked": n, "project_id": projectID, "subject_type": subjectType, "subject_id": subjectID}, nil
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
		if err != nil {
			return nil, errors.New("token has invalid expiration")
		}
		if time.Now().After(t) {
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
	conds := []string{"project_id = ?", "period = ?", "status != 'reserved'"}
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
	query := `SELECT COUNT(*), COALESCE(SUM(request_tokens),0), COALESCE(SUM(response_tokens),0), COALESCE(SUM(total_tokens),0),
		COALESCE(SUM(estimated_cost_cents),0), COALESCE(SUM(provider_cost_microunits),0),
		CASE WHEN COUNT(DISTINCT provider_cost_currency) <= 1 THEN COALESCE(MIN(provider_cost_currency),'USD') ELSE 'MIXED' END,
		COALESCE(SUM(CASE WHEN provider_cost_status='reported' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN provider_cost_status='calculated' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN provider_cost_status='partial' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN provider_cost_status NOT IN ('reported','calculated','partial') THEN 1 ELSE 0 END),0)
		FROM usage_events WHERE ` + strings.Join(conds, " AND ")
	if err := db.QueryRow(query, args...).Scan(&out.Requests, &out.RequestTokens, &out.ResponseTokens, &out.TotalTokens,
		&out.EstimatedCostCents, &out.ProviderCostMicrounits, &out.ProviderCostCurrency,
		&out.ReportedCostRequests, &out.CalculatedCostRequests, &out.PartialCostRequests, &out.UnpricedRequests); err != nil {
		return nil, err
	}
	return &out, nil
}

func reserveUsage(db *sql.DB, ident *TokenIdentity, provider, model string, input, requestedOutput int64, requestID string, policies []*Policy, enforceOutput bool) (*UsageEvent, int64, error) {
	tx, err := db.Begin()
	if err != nil {
		return nil, 0, err
	}
	defer tx.Rollback()
	if err := validateCostCurrency(policies); err != nil {
		return nil, 0, forbiddenError(err.Error())
	}
	rate, err := resolveProviderRate(tx, ident.ProjectID, provider, model)
	if err != nil {
		return nil, 0, err
	}
	reservedOutput := smallestPolicyOutputReservation(policies, requestedOutput)
	costMicrounits := int64(0)
	costCurrency := "USD"
	costStatus := "unpriced"
	costSource := ""
	rateID := int64(0)
	if rate != nil && rate.hasMeteredRate() {
		costMicrounits = calculateProviderCost(rate, input, reservedOutput, 0, 0)
		costCurrency = rate.Currency
		costStatus = "calculated"
		costSource = rate.Source
		rateID = rate.ID
	} else if !unpricedAllowed(policies) {
		return nil, 0, forbiddenError("provider rate is required while a provider cost limit is active")
	}
	period := currentPeriod()
	res, err := tx.Exec(`INSERT INTO usage_events
		(project_id, subject_type, subject_id, provider, model, request_tokens, response_tokens, total_tokens,
		 estimated_cost_cents, provider_cost_microunits, provider_cost_currency, provider_cost_status,
		 provider_cost_source, provider_rate_id, usage_details_json, status, period, request_id, provider_request_id, token_id)
		VALUES (?, ?, ?, ?, ?, ?, 0, ?, ?, ?, ?, ?, ?, ?, '{}', 'reserved', ?, ?, '', ?)`,
		ident.ProjectID, ident.SubjectType, ident.SubjectID, provider, model, input, input,
		(costMicrounits+5000)/10000, costMicrounits, costCurrency, costStatus, costSource, rateID,
		period, requestID, ident.ID)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique constraint") {
			return nil, 0, conflictError("duplicate request id for this subject")
		}
		return nil, 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, 0, err
	}

	projectUsage, err := dbUsageGetQuerier(tx, usageFilter{ProjectID: ident.ProjectID, Period: period}, true)
	if err != nil {
		return nil, 0, err
	}
	if err := enforceReservedLimits(policies[0].Limits, projectUsage); err != nil {
		return nil, 0, err
	}
	usages := []*UsageSummary{projectUsage}
	if len(policies) > 1 {
		subjectUsage, err := dbUsageGetQuerier(tx, usageFilter{ProjectID: ident.ProjectID, Period: period, SubjectType: ident.SubjectType, SubjectID: ident.SubjectID}, true)
		if err != nil {
			return nil, 0, err
		}
		if err := enforceReservedLimits(policies[1].Limits, subjectUsage); err != nil {
			return nil, 0, err
		}
		usages = append(usages, subjectUsage)
	}

	grantedOutput := int64(0)
	if enforceOutput {
		grantedOutput = requestedOutput
		for i, policy := range policies {
			if policy.Limits.MaxTokensPerRequest > 0 && requestedOutput == 0 {
				grantedOutput = minPositive(grantedOutput, policy.Limits.MaxTokensPerRequest)
			}
			if policy.Limits.MonthlyOutputTokenLimit <= 0 {
				// Cost limits can still derive a safe output ceiling when the
				// caller omitted max_tokens.
			} else {
				remaining := policy.Limits.MonthlyOutputTokenLimit - usages[i].ResponseTokens
				if remaining <= 0 || (requestedOutput > 0 && requestedOutput > remaining) {
					return nil, 0, forbiddenError("monthly output token limit exceeded")
				}
				grantedOutput = minPositive(grantedOutput, remaining)
			}
			if requestedOutput == 0 && policy.Limits.MonthlyProviderCostLimitMicrounits > 0 && rate != nil && rate.OutputMicrounitsPerMillion > 0 {
				remainingCost := policy.Limits.MonthlyProviderCostLimitMicrounits - usages[i].ProviderCostMicrounits
				if remainingCost <= 0 {
					return nil, 0, forbiddenError("monthly provider cost limit exceeded")
				}
				maxByCost := remainingCost * 1_000_000 / rate.OutputMicrounitsPerMillion
				if maxByCost <= 0 {
					return nil, 0, forbiddenError("monthly provider cost limit leaves no output capacity")
				}
				grantedOutput = minPositive(grantedOutput, maxByCost)
			}
		}
	}
	if grantedOutput > 0 {
		reservedCost := costMicrounits
		if rate != nil && rate.hasMeteredRate() {
			reservedCost = calculateProviderCost(rate, input, grantedOutput, 0, 0)
		}
		if _, err := tx.Exec(`UPDATE usage_events SET response_tokens=?, total_tokens=request_tokens+?,
			provider_cost_microunits=?, estimated_cost_cents=? WHERE id=?`,
			grantedOutput, grantedOutput, reservedCost, (reservedCost+5000)/10000, id); err != nil {
			return nil, 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, 0, err
	}
	ev, err := dbUsageEventByID(db, id)
	return ev, grantedOutput, err
}

func enforceReservedLimits(l Limits, usage *UsageSummary) error {
	if l.MonthlyRequestLimit > 0 && usage.Requests > l.MonthlyRequestLimit {
		return forbiddenError("monthly request limit exceeded")
	}
	if l.MonthlyInputTokenLimit > 0 && usage.RequestTokens > l.MonthlyInputTokenLimit {
		return forbiddenError("monthly input token limit exceeded")
	}
	if l.MonthlyProviderCostLimitMicrounits > 0 && usage.ProviderCostMicrounits > l.MonthlyProviderCostLimitMicrounits {
		return forbiddenError("monthly provider cost limit exceeded")
	}
	return nil
}

func finishUsageReservation(db *sql.DB, id int64, provider, model string, input, output int64, status, providerRequestID string) (*UsageEvent, error) {
	return finishUsageReservationCost(db, id, provider, model, input, output, status, providerRequestID, 0, "", false, nil)
}

func finishUsageReservationCost(db *sql.DB, id int64, provider, model string, input, output int64, status, providerRequestID string,
	reportedCost int64, reportedCurrency string, costReported bool, usageDetails json.RawMessage) (*UsageEvent, error) {
	current, err := dbUsageEventByID(db, id)
	if err != nil {
		return nil, err
	}
	costMicrounits := int64(0)
	costCurrency := "USD"
	costStatus := "unpriced"
	costSource := ""
	rateID := int64(0)
	if costReported {
		costMicrounits = reportedCost
		costCurrency = firstNonEmpty(strings.ToUpper(reportedCurrency), "USD")
		costStatus = "reported"
		costSource = "provider_response"
	} else if rate, resolveErr := resolveProviderRate(db, current.ProjectID, provider, model); resolveErr != nil {
		return nil, resolveErr
	} else if rate != nil && rate.hasMeteredRate() && status == "completed" {
		var details struct {
			CachedInputTokens  int64 `json:"cached_input_tokens"`
			CacheWriteTokens   int64 `json:"cache_write_tokens"`
			CacheWrite1hTokens int64 `json:"cache_write_1h_tokens"`
		}
		_ = json.Unmarshal(usageDetails, &details)
		costMicrounits = calculateProviderCostDetailed(rate, input, output, details.CachedInputTokens, details.CacheWriteTokens, details.CacheWrite1hTokens)
		costCurrency = rate.Currency
		costStatus = "calculated"
		if providerCostCalculationPartial(rate, input, details.CacheWrite1hTokens) {
			costStatus = "partial"
		}
		costSource = rate.Source
		rateID = rate.ID
	}
	if len(usageDetails) == 0 {
		usageDetails = json.RawMessage(`{}`)
	}
	if usageHasUnknownAttemptCost(usageDetails) {
		costStatus = "partial"
	}
	res, err := db.Exec(`UPDATE usage_events SET provider=?, model=?, request_tokens=?, response_tokens=?, total_tokens=?,
		estimated_cost_cents=?, provider_cost_microunits=?, provider_cost_currency=?, provider_cost_status=?,
		provider_cost_source=?, provider_rate_id=?, usage_details_json=?, status=?, provider_request_id=?
		WHERE id=? AND status='reserved'`,
		provider, model, input, output, input+output, (costMicrounits+5000)/10000, costMicrounits, costCurrency,
		costStatus, costSource, rateID, string(usageDetails), status, providerRequestID, id)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil, errors.New("usage reservation is no longer active")
	}
	return dbUsageEventByID(db, id)
}

type queryRower interface {
	QueryRow(query string, args ...any) *sql.Row
}

func dbUsageGetQuerier(db queryRower, f usageFilter, includeReserved bool) (*UsageSummary, error) {
	if f.Period == "" {
		f.Period = currentPeriod()
	}
	conds := []string{"project_id = ?", "period = ?"}
	if !includeReserved {
		conds = append(conds, "status != 'reserved'")
	}
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
	out := &UsageSummary{ProjectID: f.ProjectID, Period: f.Period, SubjectType: f.SubjectType, SubjectID: f.SubjectID, Model: f.Model}
	query := `SELECT COUNT(*), COALESCE(SUM(request_tokens),0), COALESCE(SUM(response_tokens),0), COALESCE(SUM(total_tokens),0),
		COALESCE(SUM(estimated_cost_cents),0), COALESCE(SUM(provider_cost_microunits),0),
		CASE WHEN COUNT(DISTINCT provider_cost_currency) <= 1 THEN COALESCE(MIN(provider_cost_currency),'USD') ELSE 'MIXED' END,
		COALESCE(SUM(CASE WHEN provider_cost_status='reported' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN provider_cost_status='calculated' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN provider_cost_status='partial' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN provider_cost_status NOT IN ('reported','calculated','partial') THEN 1 ELSE 0 END),0)
		FROM usage_events WHERE ` + strings.Join(conds, " AND ")
	if err := db.QueryRow(query, args...).Scan(&out.Requests, &out.RequestTokens, &out.ResponseTokens, &out.TotalTokens,
		&out.EstimatedCostCents, &out.ProviderCostMicrounits, &out.ProviderCostCurrency,
		&out.ReportedCostRequests, &out.CalculatedCostRequests, &out.PartialCostRequests, &out.UnpricedRequests); err != nil {
		return nil, err
	}
	return out, nil
}

func dbUsageRecord(db *sql.DB, ident *TokenIdentity, provider, model string, input, output, cost int64, status, requestID, providerRequestID string) (*UsageEvent, bool, error) {
	period := currentPeriod()
	costStatus := "unpriced"
	if cost > 0 {
		costStatus = "calculated"
	}
	res, err := db.Exec(`INSERT OR IGNORE INTO usage_events
		(project_id, subject_type, subject_id, provider, model, request_tokens, response_tokens, total_tokens,
		estimated_cost_cents, provider_cost_microunits, provider_cost_currency, provider_cost_status,
		provider_cost_source, usage_details_json, status, period, request_id, provider_request_id, token_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'USD', ?, ?, '{}', ?, ?, ?, ?, ?)`,
		ident.ProjectID, ident.SubjectType, ident.SubjectID, provider, model, input, output, input+output,
		(cost+5000)/10000, cost, costStatus, "recorded", status, period, requestID, providerRequestID, ident.ID)
	if err != nil {
		return nil, false, err
	}
	n, _ := res.RowsAffected()
	ev, err := dbUsageEventByRequestID(db, ident.ProjectID, ident.SubjectType, ident.SubjectID, requestID)
	if err != nil {
		return nil, false, err
	}
	return ev, n == 0, nil
}

func dbUsageEventByRequestID(db *sql.DB, projectID, subjectType, subjectID, requestID string) (*UsageEvent, error) {
	row := db.QueryRow(`
		SELECT id, project_id, subject_type, subject_id, provider, model, request_tokens, response_tokens,
		       total_tokens, estimated_cost_cents, provider_cost_microunits, provider_cost_currency,
		       provider_cost_status, provider_cost_source, provider_rate_id, usage_details_json,
		       status, period, request_id, provider_request_id, token_id, COALESCE(created_at,'')
		  FROM usage_events
		 WHERE project_id = ? AND subject_type = ? AND subject_id = ? AND request_id = ?
		 LIMIT 1`, projectID, subjectType, subjectID, requestID)
	return scanUsageEvent(row)
}

func dbUsageEventByID(db *sql.DB, id int64) (*UsageEvent, error) {
	row := db.QueryRow(`SELECT id, project_id, subject_type, subject_id, provider, model, request_tokens, response_tokens,
		total_tokens, estimated_cost_cents, provider_cost_microunits, provider_cost_currency,
		provider_cost_status, provider_cost_source, provider_rate_id, usage_details_json,
		status, period, request_id, provider_request_id, token_id, COALESCE(created_at,'')
		FROM usage_events WHERE id=?`, id)
	return scanUsageEvent(row)
}

func dbUsageEventsList(db *sql.DB, f usageFilter, limit int) ([]UsageEvent, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	if f.Period == "" {
		f.Period = currentPeriod()
	}
	conds := []string{"project_id=?", "period=?", "status!='reserved'"}
	args := []any{f.ProjectID, f.Period}
	if f.SubjectType != "" {
		conds = append(conds, "subject_type=?")
		args = append(args, f.SubjectType)
	}
	if f.SubjectID != "" {
		conds = append(conds, "subject_id=?")
		args = append(args, f.SubjectID)
	}
	if f.Model != "" {
		conds = append(conds, "model=?")
		args = append(args, f.Model)
	}
	args = append(args, limit)
	rows, err := db.Query(`SELECT id, project_id, subject_type, subject_id, provider, model, request_tokens, response_tokens,
		total_tokens, estimated_cost_cents, provider_cost_microunits, provider_cost_currency,
		provider_cost_status, provider_cost_source, provider_rate_id, usage_details_json,
		status, period, request_id, provider_request_id, token_id, COALESCE(created_at,'')
		FROM usage_events WHERE `+strings.Join(conds, " AND ")+` ORDER BY id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UsageEvent{}
	for rows.Next() {
		ev, err := scanUsageEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *ev)
	}
	return out, rows.Err()
}

type usageEventScanner interface {
	Scan(dest ...any) error
}

func scanUsageEvent(row usageEventScanner) (*UsageEvent, error) {
	var ev UsageEvent
	var usageDetails string
	if err := row.Scan(&ev.ID, &ev.ProjectID, &ev.SubjectType, &ev.SubjectID, &ev.Provider, &ev.Model,
		&ev.RequestTokens, &ev.ResponseTokens, &ev.TotalTokens, &ev.EstimatedCostCents,
		&ev.ProviderCostMicrounits, &ev.ProviderCostCurrency, &ev.ProviderCostStatus, &ev.ProviderCostSource,
		&ev.ProviderRateID, &usageDetails, &ev.Status, &ev.Period,
		&ev.RequestID, &ev.ProviderRequestID, &ev.TokenID, &ev.CreatedAt); err != nil {
		return nil, err
	}
	ev.UsageDetails = json.RawMessage(firstNonEmpty(usageDetails, "{}"))
	return &ev, nil
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
	if cfg == nil {
		return "", errors.New("provider config is required")
	}
	if cfg.AuthMode != "platform_shared" && cfg.AuthMode != "provider_aggregator" && cfg.AuthMode != "customer_owned" {
		return "", fmt.Errorf("unsupported auth_mode %q", cfg.AuthMode)
	}
	if cfg.AuthMode == "customer_owned" {
		if strings.TrimSpace(cfg.ProjectID) == "" {
			return "", errors.New("customer_owned provider requires project_id")
		}
		if cfg.ConnectionID <= 0 {
			return "", errors.New("customer_owned provider requires connection_id")
		}
		if ctx.PlatformAPI() == nil {
			return "", errors.New("customer_owned provider requires platform API")
		}
		connection, err := ctx.PlatformAPI().GetConnection(cfg.ConnectionID)
		if err != nil {
			return "", err
		}
		if connection == nil || connection.ID != cfg.ConnectionID || !connectionStatusUsable(connection.Status) {
			return "", fmt.Errorf("connection %d is not connected", cfg.ConnectionID)
		}
		if cfg.ProjectID != "" && connection.ProjectID != "" && connection.ProjectID != cfg.ProjectID {
			return "", fmt.Errorf("connection %d belongs to another project", cfg.ConnectionID)
		}
		if !providerConnectionCompatible(cfg.Provider, connection.AppSlug) {
			return "", fmt.Errorf("connection %d (%s) is not compatible with provider %q", cfg.ConnectionID, connection.AppSlug, cfg.Provider)
		}
		creds, err := ctx.PlatformAPI().GetConnectionCredentials(cfg.ConnectionID)
		if err != nil {
			return "", err
		}
		if creds == nil || !providerConnectionCompatible(cfg.Provider, creds.Slug) {
			return "", fmt.Errorf("connection %d credentials are not compatible with provider %q", cfg.ConnectionID, cfg.Provider)
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
		keys = append(keys, defaultProviderKeyRef(cfg.Provider))
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

func connectionStatusUsable(status string) bool {
	return strings.EqualFold(status, "active") || strings.EqualFold(status, "connected")
}

func providerConnectionCompatible(provider, slug string) bool {
	provider = normalizeProvider(provider)
	slug = normalizeProvider(slug)
	if provider == "" || slug == "" {
		return false
	}
	if slug == provider || slug == provider+"-api" {
		return true
	}
	return provider == "openrouter" && slug == "openrouter-api"
}

func policyAllows(pol *Policy, provider, model string, maxTokens int64) error {
	if pol.Disabled {
		return forbiddenError("access disabled by policy")
	}
	if len(pol.AllowedProviders) > 0 && !matchesAny(pol.AllowedProviders, provider) {
		return forbiddenError("provider not allowed by policy")
	}
	if len(pol.AllowedModels) > 0 && !matchesAny(pol.AllowedModels, model) {
		return forbiddenError("model not allowed by policy")
	}
	if matchesAny(pol.BlockedModels, model) {
		return forbiddenError("model blocked by policy")
	}
	if pol.Limits.MaxTokensPerRequest > 0 && maxTokens > pol.Limits.MaxTokensPerRequest {
		return forbiddenError("max_tokens exceeds policy")
	}
	return nil
}

func policiesAllow(policies []*Policy, provider, model string, maxTokens int64) error {
	for _, pol := range policies {
		if pol == nil {
			continue
		}
		if err := policyAllows(pol, provider, model, maxTokens); err != nil {
			return err
		}
	}
	return nil
}

func fallbackAttempts(policies []*Policy, provider, model string) []providerAttempt {
	out := []providerAttempt{{Provider: provider, Model: model}}
	seen := map[string]bool{provider + "\x00" + model: true}
	for i := len(policies) - 1; i >= 0; i-- {
		var fallback FallbackPolicy
		if len(policies[i].FallbackPolicy) == 0 || json.Unmarshal(policies[i].FallbackPolicy, &fallback) != nil {
			continue
		}
		for _, route := range fallback.Routes {
			route.Provider = normalizeProvider(firstNonEmpty(route.Provider, providerFromModel(route.Model)))
			if route.Provider == "" || strings.TrimSpace(route.Model) == "" {
				continue
			}
			route.Model = gatewayModelID(route.Provider, route.Model)
			key := route.Provider + "\x00" + route.Model
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, providerAttempt{Provider: route.Provider, Model: route.Model})
		}
	}
	return out
}

func openAIChatToAnthropic(provider string, body map[string]any) (map[string]any, error) {
	messagesRaw, ok := body["messages"].([]any)
	if !ok || len(messagesRaw) == 0 {
		return nil, userError("messages must be a non-empty array")
	}
	outMsgs := []map[string]any{}
	systemBlocks := []any{}
	for _, raw := range messagesRaw {
		m, ok := raw.(map[string]any)
		if !ok {
			return nil, userError("each message must be an object")
		}
		role := strArg(m, "role")
		if role == "system" || role == "developer" {
			blocks, err := openAIContentToAnthropic(m["content"])
			if err != nil {
				return nil, err
			}
			systemBlocks = append(systemBlocks, blocks...)
			continue
		}
		if role == "tool" {
			toolCallID := strArg(m, "tool_call_id")
			if toolCallID == "" {
				return nil, userError("tool message requires tool_call_id")
			}
			appendAnthropicMessage(&outMsgs, "user", []any{map[string]any{
				"type": "tool_result", "tool_use_id": toolCallID, "content": contentText(m["content"]),
			}})
			continue
		}
		if role != "user" && role != "assistant" {
			return nil, userError("message role must be system, developer, user, assistant, or tool")
		}
		blocks, err := openAIContentToAnthropic(m["content"])
		if err != nil {
			return nil, err
		}
		if role == "assistant" {
			toolCalls, _ := m["tool_calls"].([]any)
			for _, rawCall := range toolCalls {
				call, _ := rawCall.(map[string]any)
				fn, _ := call["function"].(map[string]any)
				name := strArg(fn, "name")
				if name == "" {
					continue
				}
				input := any(map[string]any{})
				switch args := fn["arguments"].(type) {
				case string:
					if strings.TrimSpace(args) != "" && json.Unmarshal([]byte(args), &input) != nil {
						return nil, userError("tool call arguments must contain valid JSON")
					}
				case map[string]any:
					input = args
				}
				blocks = append(blocks, map[string]any{"type": "tool_use", "id": firstNonEmpty(strArg(call, "id"), "toolu_"+randomSuffix(16)), "name": name, "input": input})
			}
		}
		content := any(blocks)
		if len(blocks) == 1 {
			if block, ok := blocks[0].(map[string]any); ok && strArg(block, "type") == "text" {
				content = strArg(block, "text")
			}
		}
		appendAnthropicMessage(&outMsgs, role, content)
	}
	if len(outMsgs) == 0 {
		return nil, userError("messages must contain at least one user or assistant message")
	}
	req := map[string]any{
		"model":      upstreamModel(provider, strArg(body, "model")),
		"messages":   outMsgs,
		"max_tokens": intArg(body, "max_tokens", 1024),
	}
	if len(systemBlocks) > 0 {
		req["system"] = systemBlocks
	}
	if v, ok := body["temperature"]; ok {
		req["temperature"] = v
	}
	if v, ok := body["top_p"]; ok {
		req["top_p"] = v
	}
	if v, ok := body["stop"]; ok {
		switch stops := v.(type) {
		case string:
			req["stop_sequences"] = []string{stops}
		case []any:
			req["stop_sequences"] = stops
		}
	}
	if tools, ok := body["tools"].([]any); ok {
		translated := []any{}
		for _, rawTool := range tools {
			tool, _ := rawTool.(map[string]any)
			fn, _ := tool["function"].(map[string]any)
			if strArg(tool, "type") != "function" || strArg(fn, "name") == "" {
				continue
			}
			schema := fn["parameters"]
			if schema == nil {
				schema = map[string]any{"type": "object", "properties": map[string]any{}}
			}
			translated = append(translated, map[string]any{
				"name": strArg(fn, "name"), "description": strArg(fn, "description"), "input_schema": schema,
			})
		}
		if len(translated) > 0 {
			req["tools"] = translated
		}
	}
	disableParallel := false
	if _, specified := body["parallel_tool_calls"]; specified {
		disableParallel = !boolArg(body, "parallel_tool_calls")
	}
	if choice := anthropicToolChoice(body["tool_choice"], disableParallel); choice != nil {
		req["tool_choice"] = choice
	}
	return req, nil
}

func appendAnthropicMessage(messages *[]map[string]any, role string, content any) {
	blocks := []any{}
	switch value := content.(type) {
	case string:
		if value != "" {
			blocks = append(blocks, map[string]any{"type": "text", "text": value})
		}
	case []any:
		blocks = append(blocks, value...)
	}
	if len(*messages) > 0 && strArg((*messages)[len(*messages)-1], "role") == role {
		existing := (*messages)[len(*messages)-1]["content"]
		existingBlocks := []any{}
		switch value := existing.(type) {
		case string:
			existingBlocks = append(existingBlocks, map[string]any{"type": "text", "text": value})
		case []any:
			existingBlocks = append(existingBlocks, value...)
		}
		(*messages)[len(*messages)-1]["content"] = append(existingBlocks, blocks...)
		return
	}
	*messages = append(*messages, map[string]any{"role": role, "content": content})
}

func openAIContentToAnthropic(content any) ([]any, error) {
	if content == nil {
		return nil, nil
	}
	if text, ok := content.(string); ok {
		if text == "" {
			return nil, nil
		}
		return []any{map[string]any{"type": "text", "text": text}}, nil
	}
	items, ok := content.([]any)
	if !ok {
		return nil, userError("message content must be text or an array of content blocks")
	}
	out := []any{}
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		switch strArg(item, "type") {
		case "text", "input_text":
			out = append(out, map[string]any{"type": "text", "text": strArg(item, "text")})
		case "image_url":
			imageURL := ""
			switch image := item["image_url"].(type) {
			case string:
				imageURL = image
			case map[string]any:
				imageURL = strArg(image, "url")
			}
			if imageURL == "" {
				continue
			}
			source := map[string]any{"type": "url", "url": imageURL}
			if strings.HasPrefix(imageURL, "data:") {
				header, data, found := strings.Cut(imageURL, ",")
				mediaType := strings.TrimSuffix(strings.TrimPrefix(header, "data:"), ";base64")
				if !found || !strings.HasSuffix(header, ";base64") || mediaType == "" {
					return nil, userError("image data URL must be base64 encoded")
				}
				source = map[string]any{"type": "base64", "media_type": mediaType, "data": data}
			}
			out = append(out, map[string]any{"type": "image", "source": source})
		}
	}
	return out, nil
}

func anthropicToolChoice(raw any, disableParallel bool) map[string]any {
	choice := map[string]any{}
	switch value := raw.(type) {
	case string:
		switch value {
		case "auto", "none":
			choice["type"] = value
		case "required":
			choice["type"] = "any"
		}
	case map[string]any:
		if strArg(value, "type") == "function" {
			fn, _ := value["function"].(map[string]any)
			if name := strArg(fn, "name"); name != "" {
				choice["type"] = "tool"
				choice["name"] = name
			}
		}
	}
	if len(choice) == 0 {
		return nil
	}
	if disableParallel {
		choice["disable_parallel_tool_use"] = true
	}
	return choice
}

func anthropicToOpenAI(raw []byte, requestedModel string) ([]byte, int64, int64, error) {
	var in struct {
		ID         string `json:"id"`
		Model      string `json:"model"`
		StopReason string `json:"stop_reason"`
		Content    []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
		Usage struct {
			InputTokens  int64 `json:"input_tokens"`
			OutputTokens int64 `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, 0, 0, fmt.Errorf("decode anthropic response: %w", err)
	}
	text := ""
	toolCalls := []any{}
	for _, c := range in.Content {
		switch c.Type {
		case "text":
			text += c.Text
		case "tool_use":
			arguments := "{}"
			if len(c.Input) > 0 {
				arguments = string(c.Input)
			}
			toolCalls = append(toolCalls, map[string]any{
				"id": c.ID, "type": "function", "function": map[string]any{"name": c.Name, "arguments": arguments},
			})
		}
	}
	model := requestedModel
	if model == "" {
		model = "anthropic/" + in.Model
	}
	message := map[string]any{"role": "assistant", "content": text}
	if len(toolCalls) > 0 {
		message["tool_calls"] = toolCalls
		if text == "" {
			message["content"] = nil
		}
	}
	finishReason := "stop"
	switch in.StopReason {
	case "tool_use":
		finishReason = "tool_calls"
	case "max_tokens":
		finishReason = "length"
	case "stop_sequence", "end_turn":
		finishReason = "stop"
	}
	out := map[string]any{
		"id":      firstNonEmpty(in.ID, "chatcmpl_"+randomSuffix(12)),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []any{map[string]any{
			"index":         0,
			"message":       message,
			"finish_reason": finishReason,
		}},
		"usage": map[string]any{
			"prompt_tokens":     in.Usage.InputTokens,
			"completion_tokens": in.Usage.OutputTokens,
			"total_tokens":      in.Usage.InputTokens + in.Usage.OutputTokens,
		},
	}
	b, _ := json.Marshal(out)
	return b, in.Usage.InputTokens, in.Usage.OutputTokens, nil
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
	costHeaders(w, res.UsageEvent)
}

func requestIDFor(r *http.Request, body map[string]any) string {
	for _, v := range []string{
		r.Header.Get("Idempotency-Key"),
		r.Header.Get("X-Request-Id"),
		strArg(body, "request_id"),
	} {
		if v = strings.TrimSpace(v); v != "" {
			if len(v) <= 256 {
				return v
			}
			sum := sha256.Sum256([]byte(v))
			return "llm_req_hash_" + hex.EncodeToString(sum[:])
		}
	}
	return "llm_req_" + randomSuffix(16)
}

func usageEventPayload(ev *UsageEvent, extra map[string]any) map[string]any {
	out := map[string]any{
		"schema_version":           "2026-07-llm-usage-v3",
		"usage_event_id":           int64(0),
		"request_id":               "",
		"provider_request_id":      "",
		"token_id":                 int64(0),
		"project_id":               "",
		"subject_type":             "",
		"subject_id":               "",
		"provider":                 "",
		"model":                    "",
		"period":                   currentPeriod(),
		"request_tokens":           int64(0),
		"response_tokens":          int64(0),
		"total_tokens":             int64(0),
		"provider_cost_microunits": int64(0),
		"provider_cost_currency":   "USD",
		"provider_cost_status":     "unpriced",
		"provider_cost_source":     "",
		"provider_rate_id":         int64(0),
		"usage_details":            map[string]any{},
		"status":                   "",
		"created_at":               "",
	}
	if ev != nil {
		out["usage_event_id"] = ev.ID
		out["request_id"] = ev.RequestID
		out["provider_request_id"] = ev.ProviderRequestID
		out["token_id"] = ev.TokenID
		out["project_id"] = ev.ProjectID
		out["subject_type"] = ev.SubjectType
		out["subject_id"] = ev.SubjectID
		out["provider"] = ev.Provider
		out["model"] = ev.Model
		out["period"] = ev.Period
		out["request_tokens"] = ev.RequestTokens
		out["response_tokens"] = ev.ResponseTokens
		out["total_tokens"] = ev.TotalTokens
		out["provider_cost_microunits"] = ev.ProviderCostMicrounits
		out["provider_cost_currency"] = ev.ProviderCostCurrency
		out["provider_cost_status"] = ev.ProviderCostStatus
		out["provider_cost_source"] = ev.ProviderCostSource
		out["provider_rate_id"] = ev.ProviderRateID
		out["usage_details"] = rawJSON(string(ev.UsageDetails))
		out["status"] = ev.Status
		out["created_at"] = ev.CreatedAt
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
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
func conflictError(msg string) error {
	return typedErr{status: http.StatusConflict, typ: "idempotency_error", msg: msg}
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

func isPolicyError(err error) bool {
	var te typedErr
	return errors.As(err, &te) && te.typ == "policy_error"
}

func isRetryableProviderError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var te typedErr
	if errors.As(err, &te) {
		return te.status == http.StatusTooManyRequests || te.status >= http.StatusInternalServerError
	}
	return true
}

func tokenHasScope(ident *TokenIdentity, required string) bool {
	if ident == nil {
		return false
	}
	for _, scope := range ident.Scopes {
		scope = strings.TrimSpace(scope)
		if scope == "*" || scope == required || (required == "usage" && scope == "usage:project") {
			return true
		}
	}
	return false
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
	if globalCtx != nil {
		if current := strings.TrimSpace(globalCtx.CurrentProject()); current != "" {
			return current
		}
	}
	if v := strings.TrimSpace(r.URL.Query().Get("project_id")); v != "" {
		return v
	}
	return ""
}

func subjectTypeFromRequest(r *http.Request) string {
	return strings.TrimSpace(r.URL.Query().Get("subject_type"))
}

func subjectIDFromRequest(r *http.Request) string {
	return strings.TrimSpace(r.URL.Query().Get("subject_id"))
}

func projectFromArgs(args map[string]any) string {
	if globalCtx != nil {
		if current := strings.TrimSpace(globalCtx.CurrentProject()); current != "" {
			return current
		}
	}
	if v := strArg(args, "project_id"); v != "" {
		return v
	}
	if v := strArg(args, "_project_id"); v != "" {
		return v
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
	if l, ok := v.(Limits); ok {
		return l
	}
	m, ok := v.(map[string]any)
	if !ok {
		return Limits{}
	}
	return Limits{
		MonthlyRequestLimit:                int64Arg(m, "monthly_request_limit"),
		MonthlyInputTokenLimit:             int64Arg(m, "monthly_input_token_limit"),
		MonthlyOutputTokenLimit:            int64Arg(m, "monthly_output_token_limit"),
		MaxTokensPerRequest:                int64Arg(m, "max_tokens_per_request"),
		MonthlyProviderCostLimitMicrounits: int64Arg(m, "monthly_provider_cost_limit_microunits"),
		ProviderCostCurrency:               strings.ToUpper(firstNonEmpty(strArg(m, "provider_cost_currency"), "USD")),
		UnpricedModelBehavior:              firstNonEmpty(strArg(m, "unpriced_model_behavior"), "deny"),
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

func strAny(v any) string {
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	case fmt.Stringer:
		return strings.TrimSpace(x.String())
	default:
		return ""
	}
}

func int64FromAny(v any) int64 {
	switch x := v.(type) {
	case int:
		return int64(x)
	case int64:
		return x
	case float64:
		return int64(x)
	case json.Number:
		n, _ := x.Int64()
		return n
	default:
		return 0
	}
}

func firstNonNil(values ...any) any {
	for _, v := range values {
		if v != nil {
			return v
		}
	}
	return nil
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

func gatewayModelID(provider, modelID string) string {
	provider = normalizeProvider(provider)
	modelID = strings.TrimSpace(modelID)
	if provider == "" || modelID == "" {
		return modelID
	}
	if strings.HasPrefix(modelID, provider+"/") {
		return modelID
	}
	return provider + "/" + modelID
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
	case "opencode-go":
		return "https://opencode.ai/zen/go/v1"
	case "openai-codex":
		return "https://chatgpt.com/backend-api/codex"
	default:
		return ""
	}
}

func defaultProviderKeyRef(provider string) string {
	return strings.ReplaceAll(normalizeProvider(provider), "-", "_") + "_api_key"
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

func estimateChatTokens(v any) int64 {
	characters, images := estimateChatInput(v)
	tokens := characters/4 + images*imageTokenEstimate
	if tokens < 1 {
		return 1
	}
	return tokens
}

func estimateChatInput(v any) (characters, images int64) {
	switch value := v.(type) {
	case string:
		return int64(len(value)), 0
	case []any:
		for _, item := range value {
			itemCharacters, itemImages := estimateChatInput(item)
			characters += itemCharacters
			images += itemImages
		}
	case map[string]any:
		if isImageContentType(strArg(value, "type")) {
			return 0, 1
		}
		for _, item := range value {
			itemCharacters, itemImages := estimateChatInput(item)
			characters += itemCharacters
			images += itemImages
		}
	default:
		if value != nil {
			characters = int64(len(fmt.Sprint(value)))
		}
	}
	return characters, images
}

func normalizeChatImageInputs(body map[string]any) error {
	messages, ok := body["messages"].([]any)
	if !ok {
		return nil
	}
	normalized := make([]any, 0, len(messages))
	for _, raw := range messages {
		message, ok := raw.(map[string]any)
		if !ok {
			normalized = append(normalized, raw)
			continue
		}
		copyMessage := cloneMap(message)
		content, err := normalizeChatContent(copyMessage["content"])
		if err != nil {
			return err
		}
		copyMessage["content"] = content
		normalized = append(normalized, copyMessage)
	}
	body["messages"] = normalized
	return nil
}

func normalizeChatContent(content any) (any, error) {
	blocks, ok := content.([]any)
	if !ok {
		return content, nil
	}
	normalized := make([]any, 0, len(blocks))
	for _, raw := range blocks {
		block, ok := raw.(map[string]any)
		if !ok {
			normalized = append(normalized, raw)
			continue
		}
		blockType := strings.ToLower(strings.TrimSpace(strArg(block, "type")))
		switch blockType {
		case "input_text":
			copyBlock := cloneMap(block)
			copyBlock["type"] = "text"
			normalized = append(normalized, copyBlock)
		case "image_url", "input_image":
			image, err := canonicalImageBlock(block)
			if err != nil {
				return nil, err
			}
			normalized = append(normalized, image)
		default:
			normalized = append(normalized, block)
		}
	}
	return normalized, nil
}

func canonicalImageBlock(block map[string]any) (map[string]any, error) {
	if strArg(block, "file_id") != "" {
		return nil, userError("image file_id inputs are not supported; provide an https URL or base64 data URL")
	}
	imageURL := ""
	detail := strArg(block, "detail")
	switch value := block["image_url"].(type) {
	case string:
		imageURL = strings.TrimSpace(value)
	case map[string]any:
		imageURL = strings.TrimSpace(strArg(value, "url"))
		if detail == "" {
			detail = strArg(value, "detail")
		}
	}
	if imageURL == "" {
		return nil, userError("image content block requires image_url")
	}
	normalizedURL, err := normalizeImageReference(imageURL)
	if err != nil {
		return nil, err
	}
	ref := map[string]any{"url": normalizedURL}
	if detail != "" {
		ref["detail"] = detail
	}
	return map[string]any{"type": "image_url", "image_url": ref}, nil
}

func normalizeImageReference(reference string) (string, error) {
	lower := strings.ToLower(reference)
	if strings.HasPrefix(lower, "data:") {
		header, payload, ok := strings.Cut(reference, ",")
		headerParts := strings.Split(strings.TrimPrefix(strings.ToLower(header), "data:"), ";")
		mediaType := strings.TrimSpace(headerParts[0])
		if !ok || len(headerParts) < 2 || headerParts[len(headerParts)-1] != "base64" || !strings.HasPrefix(mediaType, "image/") {
			return "", userError("image data URL must contain a base64-encoded image MIME type")
		}
		if base64.StdEncoding.DecodedLen(len(payload)) > maxDataImageBytes {
			return "", userError("base64 image exceeds the 10 MiB decoded limit")
		}
		encoding := base64.StdEncoding
		if len(payload)%4 != 0 {
			encoding = base64.RawStdEncoding
		}
		decodedBytes, err := io.Copy(io.Discard, io.LimitReader(base64.NewDecoder(encoding, strings.NewReader(payload)), maxDataImageBytes+1))
		if err != nil {
			return "", userError("image data URL contains invalid base64 data")
		}
		if decodedBytes > maxDataImageBytes {
			return "", userError("base64 image exceeds the 10 MiB decoded limit")
		}
		return "data:" + mediaType + ";base64," + payload, nil
	}
	parsed, err := url.Parse(reference)
	if err != nil || (!strings.EqualFold(parsed.Scheme, "https") && !strings.EqualFold(parsed.Scheme, "http")) || parsed.Host == "" {
		return "", userError("image_url must be an http(s) URL or base64 image data URL")
	}
	return reference, nil
}

func requestContainsImage(v any) bool {
	switch value := v.(type) {
	case []any:
		for _, item := range value {
			if requestContainsImage(item) {
				return true
			}
		}
	case map[string]any:
		if isImageContentType(strArg(value, "type")) {
			return true
		}
		for _, item := range value {
			if requestContainsImage(item) {
				return true
			}
		}
	}
	return false
}

func isImageContentType(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "image_url", "input_image":
		return true
	default:
		return false
	}
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

func minPositive(current, candidate int64) int64 {
	if candidate <= 0 {
		return current
	}
	if current <= 0 || candidate < current {
		return candidate
	}
	return current
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
