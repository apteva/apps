// Email Checker — stateless email validation.
//
// Local checks cover syntax, RFC-correct MX/A/AAAA routing, Null MX,
// disposable/free/role classification, common domain typos, and an optional
// multi-MX SMTP probe with a generated-recipient catch-all control. No message
// is sent and no result is stored. Bound commercial providers remain optional.
package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const manifestYAML = `schema: apteva-app/v1
name: email-checker
display_name: Email Checker
version: 0.5.1
description: |
  Standalone email checks with RFC-correct DNS, multi-MX SMTP and catch-all detection, plus optional provider verification.
author: Apteva
icon: /ui/icon.svg
icon_style: monochrome
scopes: [project, global]
requires:
  permissions:
    - net.egress
    - platform.connections.execute
  integrations:
    - role: verification_providers
      kind: integration
      mode: multiple
      compatible_slugs: [zerobounce, bouncer, neverbounce, kickbox, millionverifier, hunter]
      capabilities: [email.verify]
      required: false
      label: "Email verification providers (optional)"
      hint: "Provider checks may consume credits; local checks remain available without a connection."
provides:
  http_routes:
    - prefix: /
  mcp_tools:
    - { name: email_check, description: "Local email checks plus optional SMTP or bound provider verification." }
    - { name: email_verification_providers, description: "List bound verification providers without consuming credits." }
runtime:
  kind: source
  source:
    repo: github.com/apteva/apps
    ref: main
    entry: mcp/email-checker
  port: 8080
  health_check: /health
upgrade_policy: auto-patch
`

type App struct {
	ctxMu sync.RWMutex
	ctx   *sdk.AppCtx
}

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		panic("invalid embedded manifest: " + err.Error())
	}
	return *m
}

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	a.ctxMu.Lock()
	a.ctx = ctx
	a.ctxMu.Unlock()
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error {
	a.ctxMu.Lock()
	a.ctx = nil
	a.ctxMu.Unlock()
	return nil
}
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) Workers() []sdk.Worker             { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

// ─── HTTP routes ────────────────────────────────────────────────────

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/check", Handler: a.httpCheck},
		{Pattern: "/providers", Handler: a.httpProviders},
	}
}

func (a *App) httpCheck(w http.ResponseWriter, r *http.Request) {
	email := r.URL.Query().Get("email")
	if email == "" {
		http.Error(w, "?email= required", http.StatusBadRequest)
		return
	}
	withSMTP := parseBoolQuery(r.URL.Query().Get("smtp"))
	timeout := parseTimeoutSeconds(r.URL.Query().Get("timeout_seconds"))
	opts := CheckOptions{
		SMTP:         withSMTP,
		Timeout:      timeout,
		Provider:     strings.TrimSpace(r.URL.Query().Get("provider")),
		ConnectionID: parseInt64Query(r.URL.Query().Get("connection_id")),
		IPAddress:    strings.TrimSpace(r.URL.Query().Get("ip_address")),
	}
	result, err := runCheck(a.requestCtx(r), email, opts)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, result)
}

func (a *App) httpProviders(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, listVerificationProviders(a.requestCtx(r)))
}

func (a *App) requestCtx(r *http.Request) *sdk.AppCtx {
	a.ctxMu.RLock()
	ctx := a.ctx
	a.ctxMu.RUnlock()
	if ctx == nil {
		return nil
	}
	if projectID := strings.TrimSpace(r.URL.Query().Get("project_id")); projectID != "" {
		return ctx.WithProject(projectID)
	}
	return ctx
}

// ─── MCP tools ─────────────────────────────────────────────────────

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{
			Name: "email_check",
			Description: "Validate an email locally with RFC-correct mail routing, classification, typo suggestions, and optional multi-MX SMTP/catch-all probing; optionally use a bound provider. " +
				"Args: email, smtp? (default false), provider? (local|auto|provider slug; default local), " +
				"connection_id?, timeout_seconds? (default 5), ip_address? (ZeroBounce only). " +
				"External provider calls may consume credits and therefore run only when provider is explicit. " +
				"Returns normalized verdict/recommendation plus local signals and provider details.",
			InputSchema: schemaObject(map[string]any{
				"email": map[string]any{"type": "string"},
				"smtp":  map[string]any{"type": "boolean"},
				"provider": map[string]any{
					"type": "string",
					"enum": []string{"local", "auto", "zerobounce", "bouncer", "neverbounce", "kickbox", "millionverifier", "hunter"},
				},
				"connection_id":   map[string]any{"type": "integer"},
				"timeout_seconds": map[string]any{"type": "integer", "minimum": 1, "maximum": 60},
				"ip_address":      map[string]any{"type": "string", "description": "Optional signup IP forwarded only to ZeroBounce."},
			}, []string{"email"}),
			Handler: func(ctx *sdk.AppCtx, args map[string]any) (any, error) {
				email, _ := args["email"].(string)
				if email == "" {
					return nil, errors.New("email required")
				}
				withSMTP, _ := args["smtp"].(bool)
				return runCheck(ctx, email, CheckOptions{
					SMTP:         withSMTP,
					Timeout:      durationArg(args, "timeout_seconds", 5*time.Second),
					Provider:     stringArg(args, "provider"),
					ConnectionID: int64Arg(args, "connection_id"),
					IPAddress:    stringArg(args, "ip_address"),
				})
			},
		},
		{
			Name:        "email_verification_providers",
			Description: "List verification-provider connections bound to this Email Checker install. This does not call providers or consume credits.",
			InputSchema: schemaObject(map[string]any{}, nil),
			Handler: func(ctx *sdk.AppCtx, _ map[string]any) (any, error) {
				return listVerificationProviders(ctx), nil
			},
		},
	}
}

func main() { sdk.Run(&App{}) }

// ─── helpers ───────────────────────────────────────────────────────

func schemaObject(props map[string]any, required []string) map[string]any {
	s := map[string]any{
		"type":       "object",
		"properties": props,
	}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	body, err := json.Marshal(v)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_, _ = w.Write(body)
}

func parseBoolQuery(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "t", "true", "yes", "y", "on":
		return true
	default:
		return false
	}
}

func parseTimeoutSeconds(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 5 * time.Second
	}
	d, err := time.ParseDuration(v + "s")
	if err != nil || d <= 0 {
		return 5 * time.Second
	}
	return d
}

func parseInt64Query(v string) int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	if n < 0 {
		return 0
	}
	return n
}

func durationArg(args map[string]any, key string, fallback time.Duration) time.Duration {
	seconds := int64Arg(args, key)
	if seconds <= 0 {
		return fallback
	}
	if seconds > 60 {
		seconds = 60
	}
	return time.Duration(seconds) * time.Second
}

func stringArg(args map[string]any, key string) string {
	v, _ := args[key].(string)
	return strings.TrimSpace(v)
}

func int64Arg(args map[string]any, key string) int64 {
	switch value := args[key].(type) {
	case int:
		return int64(value)
	case int64:
		return value
	case float64:
		return int64(value)
	case json.Number:
		n, _ := value.Int64()
		return n
	default:
		return 0
	}
}

func boolPtr(v bool) *bool { return &v }
