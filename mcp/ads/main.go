// ads v0.1 — unified control plane for paid advertising.
//
// Architecture:
//   - One platform supported in v1: Meta (Facebook + Instagram), via the
//     facebook-ads integration. Other platforms (google, twitter) are
//     declared in the static `platforms` registry below — adding them is
//     "add a row + ensure the integration exposes the named tools" with
//     no other code change.
//   - Accounts are added at runtime via PlatformAPI.StartOAuth, which
//     returns an authorize URL the panel/agent hands the user. After the
//     dance, the platform 302s back to /accounts/oauth_done; we look up
//     the matching pending_accounts row and the user picks an ad account
//     from the connected platform's account list.
//   - All campaign / ad set / ad / creative / audience state lives
//     upstream — this app does NOT shadow it locally. Each tool resolves
//     a local ad_account id to {connection_id, native_account_id}, then
//     proxies through to the integration via ExecuteIntegrationTool.
//   - platform_options is the escape hatch: any field the platform
//     supports but we haven't unified gets passed through. This keeps
//     the unified API thin and honest about platform differences
//     (targeting, bid strategies, EU/DSA compliance fields).
package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

const manifestYAML = `schema: apteva-app/v1
name: ads
display_name: Ads
version: 0.1.0
scopes: [project, global]
requires:
  permissions:
    - db.write.app
    - net.egress
    - platform.connections.execute
    - platform.connections.manage
    - platform.oauth.start
    - platform.apps.call
  integrations:
    - role: storage
      kind: app
      compatible_app_names: [storage]
      capabilities: [files.read]
      required: false
provides:
  http_routes:
    - prefix: /
db:
  driver: sqlite
  path: /data/ads.db
  migrations: migrations/
runtime:
  kind: source
  source:
    repo: github.com/apteva/apps
    ref: main
    entry: mcp/ads
  port: 8080
  health_check: /health
upgrade_policy: auto-patch
`

// platformDef captures the per-network mapping from our unified MCP
// tool surface to the integration's tool names. v1 ships with Meta;
// adding google or twitter is "fill in another row".
type platformDef struct {
	Platform        string
	IntegrationSlug string
	DisplayName     string

	// Native ad-account id format documentation. Meta returns ids like
	// "act_123456789"; Google "customers/123"; X is a plain numeric.
	// Used in the picker hint so the agent knows what to pass.
	NativeIDFormat string

	// Account discovery. ListAccountsTool returns the ad accounts the
	// authorising user can manage. We surface them via
	// account_list_pending_pages and let the user/agent pick one.
	ListAccountsTool        string
	AccountListIDField      string // path within each account row
	AccountListNameField    string
	AccountListCurrencyField string
	AccountListTimezoneField string

	// Campaign tools.
	CampaignCreateTool string
	CampaignListTool   string
	CampaignUpdateTool string
	CampaignDeleteTool string

	// Ad set tools (Meta calls them ad sets; Google calls them ad
	// groups). The unified MCP surface uses "adset" for now — we'll
	// alias when Google lands.
	AdSetCreateTool string
	AdSetListTool   string
	AdSetUpdateTool string
	AdSetDeleteTool string

	// Ad tools.
	AdCreateTool string
	AdListTool   string
	AdUpdateTool string
	AdDeleteTool string

	// Creative tools.
	CreativeCreateTool      string
	CreativeListTool        string
	CreativeUploadImageTool string
	CreativeUploadVideoTool string

	// Audience tools.
	AudienceListTool             string
	AudienceCreateCustomTool     string
	AudienceCreateLookalikeTool  string

	// Field name on each integration tool that carries the ad-account
	// id. Meta uses "adAccountId" (act_*); Google uses "customer_id".
	// When set, runtime fills it from the resolved local ad_account.
	AccountIDInputField string
}

var platforms = map[string]platformDef{
	"meta": {
		Platform:                    "meta",
		IntegrationSlug:             "facebook-ads",
		DisplayName:                 "Meta Ads (Facebook + Instagram)",
		NativeIDFormat:              "act_<numeric>",
		ListAccountsTool:            "account_list",
		AccountListIDField:          "id", // facebook-ads returns "id":"act_123" + "account_id":"123"
		AccountListNameField:        "name",
		AccountListCurrencyField:    "currency",
		AccountListTimezoneField:    "timezone_name",
		CampaignCreateTool:          "campaign_create",
		CampaignListTool:            "campaign_list",
		CampaignUpdateTool:          "campaign_update",
		CampaignDeleteTool:          "campaign_delete",
		AdSetCreateTool:             "adset_create",
		AdSetListTool:               "adset_list",
		AdSetUpdateTool:             "adset_update",
		AdSetDeleteTool:             "adset_delete",
		AdCreateTool:                "ad_create",
		AdListTool:                  "ad_list",
		AdUpdateTool:                "ad_update",
		AdDeleteTool:                "ad_delete",
		CreativeCreateTool:          "creative_create",
		CreativeListTool:            "creative_list",
		CreativeUploadImageTool:     "creative_upload_image",
		CreativeUploadVideoTool:     "creative_upload_video",
		AudienceListTool:            "audience_list",
		AudienceCreateCustomTool:    "audience_create_custom",
		AudienceCreateLookalikeTool: "audience_create_lookalike",
		AccountIDInputField:         "adAccountId",
	},
}

var globalCtx *sdk.AppCtx

type App struct{}

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		panic("invalid embedded manifest: " + err.Error())
	}
	return *m
}

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx.AppDB() == nil {
		return errors.New("ads requires a db block")
	}
	globalCtx = ctx
	ctx.Logger().Info("ads mounted")
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error       { return nil }
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) Workers() []sdk.Worker             { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

func main() { sdk.Run(&App{}) }

// ─── HTTP routes ────────────────────────────────────────────────────

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		// OAuth-completion landing page; the platform 302s here with
		// ?conn_id=<id>&status=ok and ?pending=<pending_account_id>.
		{Pattern: "/accounts/oauth_done", Handler: a.handleOAuthDone},
	}
}

// handleOAuthDone is the landing page the platform 302s the user back
// to after OAuth completes. Query params: ?conn_id=<id>&status=ok&pending=<pid>.
// We update the matching pending_accounts row and redirect the browser
// into the dashboard's ads panel where the picker appears.
func (a *App) handleOAuthDone(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	connStr := q.Get("conn_id")
	pendingStr := q.Get("pending")
	status := q.Get("status")
	if connStr == "" || pendingStr == "" {
		http.Error(w, "missing conn_id or pending", http.StatusBadRequest)
		return
	}
	connID, err := strconv.ParseInt(connStr, 10, 64)
	if err != nil {
		http.Error(w, "bad conn_id", http.StatusBadRequest)
		return
	}
	pendingID, err := strconv.ParseInt(pendingStr, 10, 64)
	if err != nil {
		http.Error(w, "bad pending", http.StatusBadRequest)
		return
	}
	if status != "ok" {
		_, _ = globalCtx.AppDB().Exec(`UPDATE pending_accounts SET status='expired' WHERE id=?`, pendingID)
		http.Redirect(w, r, "/admin/apps/ads/?oauth=failed", http.StatusFound)
		return
	}
	_, _ = globalCtx.AppDB().Exec(
		`UPDATE pending_accounts SET connection_id=?, status='ready' WHERE id=?`,
		connID, pendingID,
	)
	http.Redirect(w, r, fmt.Sprintf("/admin/apps/ads/?pending=%d", pendingID), http.StatusFound)
}

// ─── MCP tools ──────────────────────────────────────────────────────

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		// ── Accounts ──
		{
			Name: "account_add",
			Description: "Begin connecting an ads account. Returns authorize_url + pending_account_id; visit the URL to authorize. " +
				"After OAuth completes, call account_list_pending_pages to pick a specific ad account, then account_finalize. " +
				"Args: platform (meta), force_new? (default false; force a fresh OAuth dance even when an existing connection is available).",
			InputSchema: schemaObject(map[string]any{
				"platform":  map[string]any{"type": "string", "enum": platformKeys()},
				"force_new": map[string]any{"type": "boolean"},
				"return_to": map[string]any{
					"type":        "string",
					"description": "Where to redirect after OAuth. Defaults to the ads panel.",
				},
			}, []string{"platform"}),
			Handler: a.toolAccountAdd,
		},
		{
			Name:        "account_list_pending_pages",
			Description: "After OAuth completes, list the ad accounts the user can manage on the connected platform. The agent or panel picks one to finalize. Args: pending_account_id.",
			InputSchema: schemaObject(map[string]any{
				"pending_account_id": map[string]any{"type": "integer"},
			}, []string{"pending_account_id"}),
			Handler: a.toolAccountListPendingPages,
		},
		{
			Name:        "account_finalize",
			Description: "Commit a pending ad account into the active list. Args: pending_account_id, page_id (the platform's native ad-account id, e.g. act_123 for Meta), name? (override display name).",
			InputSchema: schemaObject(map[string]any{
				"pending_account_id": map[string]any{"type": "integer"},
				"page_id":            map[string]any{"type": "string"},
				"name":               map[string]any{"type": "string"},
			}, []string{"pending_account_id", "page_id"}),
			Handler: a.toolAccountFinalize,
		},
		{
			Name:        "account_list",
			Description: "List connected ad accounts in this project. Args: platform? (filter), status? (active|needs_reauth).",
			InputSchema: schemaObject(map[string]any{
				"platform": map[string]any{"type": "string"},
				"status":   map[string]any{"type": "string"},
			}, nil),
			Handler: a.toolAccountList,
		},
		{
			Name:        "account_disconnect",
			Description: "Remove a connected ad account. The underlying connection is released when the last reference goes away. Args: id.",
			InputSchema: schemaObject(map[string]any{
				"id": map[string]any{"type": "integer"},
			}, []string{"id"}),
			Handler: a.toolAccountDisconnect,
		},

		// ── Campaigns ──
		{
			Name: "campaign_create",
			Description: "Create a campaign on the bound ad account. " +
				"Unified args: ad_account_id (local id from account_list), name, objective (sales|leads|traffic|engagement|awareness|app_promotion), status (PAUSED|ACTIVE — defaults to PAUSED), daily_budget_cents?, lifetime_budget_cents?, bid_strategy? (lowest_cost|cost_cap|bid_cap), start_time?, end_time?. " +
				"Pass platform_options for any field the unified surface doesn't cover (Meta requires special_ad_categories — pass [] when none).",
			InputSchema: schemaObject(map[string]any{
				"ad_account_id":         map[string]any{"type": "integer"},
				"name":                  map[string]any{"type": "string"},
				"objective":             map[string]any{"type": "string", "enum": []string{"sales", "leads", "traffic", "engagement", "awareness", "app_promotion"}},
				"status":                map[string]any{"type": "string", "enum": []string{"PAUSED", "ACTIVE"}, "default": "PAUSED"},
				"daily_budget_cents":    map[string]any{"type": "integer"},
				"lifetime_budget_cents": map[string]any{"type": "integer"},
				"bid_strategy":          map[string]any{"type": "string", "enum": []string{"lowest_cost", "cost_cap", "bid_cap"}},
				"start_time":            map[string]any{"type": "string"},
				"end_time":              map[string]any{"type": "string"},
				"platform_options":      map[string]any{"type": "object"},
			}, []string{"ad_account_id", "name", "objective"}),
			Handler: a.toolCampaignCreate,
		},
		{
			Name:        "campaign_list",
			Description: "List campaigns on a connected ad account. Args: ad_account_id, status? (filter), limit?, after?.",
			InputSchema: schemaObject(map[string]any{
				"ad_account_id": map[string]any{"type": "integer"},
				"status":        map[string]any{"type": "string"},
				"limit":         map[string]any{"type": "integer"},
				"after":         map[string]any{"type": "string"},
			}, []string{"ad_account_id"}),
			Handler: a.toolCampaignList,
		},
		{
			Name:        "campaign_update",
			Description: "Update a campaign. Args: ad_account_id, campaign_id, plus any of: name, status (PAUSED|ACTIVE), daily_budget_cents, lifetime_budget_cents, bid_strategy, start_time, end_time, platform_options.",
			InputSchema: schemaObject(map[string]any{
				"ad_account_id":         map[string]any{"type": "integer"},
				"campaign_id":           map[string]any{"type": "string"},
				"name":                  map[string]any{"type": "string"},
				"status":                map[string]any{"type": "string", "enum": []string{"PAUSED", "ACTIVE"}},
				"daily_budget_cents":    map[string]any{"type": "integer"},
				"lifetime_budget_cents": map[string]any{"type": "integer"},
				"bid_strategy":          map[string]any{"type": "string", "enum": []string{"lowest_cost", "cost_cap", "bid_cap"}},
				"start_time":            map[string]any{"type": "string"},
				"end_time":              map[string]any{"type": "string"},
				"platform_options":      map[string]any{"type": "object"},
			}, []string{"ad_account_id", "campaign_id"}),
			Handler: a.toolCampaignUpdate,
		},
		{
			Name:        "campaign_pause",
			Description: "Pause a campaign (sets status=PAUSED). Args: ad_account_id, campaign_id.",
			InputSchema: schemaObject(map[string]any{
				"ad_account_id": map[string]any{"type": "integer"},
				"campaign_id":   map[string]any{"type": "string"},
			}, []string{"ad_account_id", "campaign_id"}),
			Handler: a.toolCampaignPause,
		},
		{
			Name:        "campaign_resume",
			Description: "Resume a campaign (sets status=ACTIVE). Args: ad_account_id, campaign_id.",
			InputSchema: schemaObject(map[string]any{
				"ad_account_id": map[string]any{"type": "integer"},
				"campaign_id":   map[string]any{"type": "string"},
			}, []string{"ad_account_id", "campaign_id"}),
			Handler: a.toolCampaignResume,
		},
		{
			Name:        "campaign_delete",
			Description: "Delete a campaign upstream (also deletes its ad sets and ads). Args: ad_account_id, campaign_id.",
			InputSchema: schemaObject(map[string]any{
				"ad_account_id": map[string]any{"type": "integer"},
				"campaign_id":   map[string]any{"type": "string"},
			}, []string{"ad_account_id", "campaign_id"}),
			Handler: a.toolCampaignDelete,
		},

		// ── Ad sets ──
		{
			Name: "adset_create",
			Description: "Create an ad set under a campaign. " +
				"Unified args: ad_account_id, campaign_id, name, optimization_goal (link_clicks|conversions|leads|reach|impressions|page_likes|post_engagement), billing_event? (impressions|link_clicks; default impressions), daily_budget_cents?, lifetime_budget_cents?, bid_strategy?, bid_amount_cents?, start_time?, end_time?, status?, targeting? (object — passthrough; Meta requires geo_locations + targeting_automation), promoted_object? (object — passthrough), destination_type?, dsa_beneficiary?, dsa_payor?, platform_options.",
			InputSchema: schemaObject(map[string]any{
				"ad_account_id":         map[string]any{"type": "integer"},
				"campaign_id":           map[string]any{"type": "string"},
				"name":                  map[string]any{"type": "string"},
				"optimization_goal":     map[string]any{"type": "string", "enum": []string{"link_clicks", "conversions", "leads", "reach", "impressions", "page_likes", "post_engagement", "thruplay", "app_installs", "value", "landing_page_views"}},
				"billing_event":         map[string]any{"type": "string", "enum": []string{"impressions", "link_clicks", "thruplay"}, "default": "impressions"},
				"daily_budget_cents":    map[string]any{"type": "integer"},
				"lifetime_budget_cents": map[string]any{"type": "integer"},
				"bid_strategy":          map[string]any{"type": "string", "enum": []string{"lowest_cost", "lowest_cost_with_bid_cap", "cost_cap"}},
				"bid_amount_cents":      map[string]any{"type": "integer"},
				"start_time":            map[string]any{"type": "string"},
				"end_time":              map[string]any{"type": "string"},
				"status":                map[string]any{"type": "string", "enum": []string{"PAUSED", "ACTIVE"}, "default": "PAUSED"},
				"targeting":             map[string]any{"type": "object"},
				"promoted_object":       map[string]any{"type": "object"},
				"destination_type":      map[string]any{"type": "string"},
				"dsa_beneficiary":       map[string]any{"type": "string"},
				"dsa_payor":             map[string]any{"type": "string"},
				"platform_options":      map[string]any{"type": "object"},
			}, []string{"ad_account_id", "campaign_id", "name", "optimization_goal", "targeting"}),
			Handler: a.toolAdSetCreate,
		},
		{
			Name:        "adset_list",
			Description: "List ad sets in an ad account, optionally filtered to one campaign. Args: ad_account_id, campaign_id?, limit?, after?.",
			InputSchema: schemaObject(map[string]any{
				"ad_account_id": map[string]any{"type": "integer"},
				"campaign_id":   map[string]any{"type": "string"},
				"limit":         map[string]any{"type": "integer"},
				"after":         map[string]any{"type": "string"},
			}, []string{"ad_account_id"}),
			Handler: a.toolAdSetList,
		},
		{
			Name:        "adset_update",
			Description: "Update an ad set. Args: ad_account_id, adset_id, plus any updatable field.",
			InputSchema: schemaObject(map[string]any{
				"ad_account_id":    map[string]any{"type": "integer"},
				"adset_id":         map[string]any{"type": "string"},
				"name":             map[string]any{"type": "string"},
				"status":           map[string]any{"type": "string", "enum": []string{"PAUSED", "ACTIVE"}},
				"daily_budget_cents":    map[string]any{"type": "integer"},
				"lifetime_budget_cents": map[string]any{"type": "integer"},
				"bid_amount_cents":      map[string]any{"type": "integer"},
				"targeting":             map[string]any{"type": "object"},
				"platform_options":      map[string]any{"type": "object"},
			}, []string{"ad_account_id", "adset_id"}),
			Handler: a.toolAdSetUpdate,
		},
		{
			Name:        "adset_delete",
			Description: "Delete an ad set. Args: ad_account_id, adset_id.",
			InputSchema: schemaObject(map[string]any{
				"ad_account_id": map[string]any{"type": "integer"},
				"adset_id":      map[string]any{"type": "string"},
			}, []string{"ad_account_id", "adset_id"}),
			Handler: a.toolAdSetDelete,
		},

		// ── Ads ──
		{
			Name:        "ad_create",
			Description: "Create an ad referencing an existing creative. Args: ad_account_id, adset_id, name, creative_id, status? (PAUSED|ACTIVE), platform_options.",
			InputSchema: schemaObject(map[string]any{
				"ad_account_id":    map[string]any{"type": "integer"},
				"adset_id":         map[string]any{"type": "string"},
				"name":             map[string]any{"type": "string"},
				"creative_id":      map[string]any{"type": "string"},
				"status":           map[string]any{"type": "string", "enum": []string{"PAUSED", "ACTIVE"}, "default": "PAUSED"},
				"platform_options": map[string]any{"type": "object"},
			}, []string{"ad_account_id", "adset_id", "name", "creative_id"}),
			Handler: a.toolAdCreate,
		},
		{
			Name:        "ad_list",
			Description: "List ads under an ad set or ad account. Args: ad_account_id, adset_id?, limit?, after?.",
			InputSchema: schemaObject(map[string]any{
				"ad_account_id": map[string]any{"type": "integer"},
				"adset_id":      map[string]any{"type": "string"},
				"limit":         map[string]any{"type": "integer"},
				"after":         map[string]any{"type": "string"},
			}, []string{"ad_account_id"}),
			Handler: a.toolAdList,
		},
		{
			Name:        "ad_update",
			Description: "Update an ad. Args: ad_account_id, ad_id, plus any updatable field.",
			InputSchema: schemaObject(map[string]any{
				"ad_account_id":    map[string]any{"type": "integer"},
				"ad_id":            map[string]any{"type": "string"},
				"name":             map[string]any{"type": "string"},
				"status":           map[string]any{"type": "string", "enum": []string{"PAUSED", "ACTIVE"}},
				"creative_id":      map[string]any{"type": "string"},
				"platform_options": map[string]any{"type": "object"},
			}, []string{"ad_account_id", "ad_id"}),
			Handler: a.toolAdUpdate,
		},
		{
			Name:        "ad_delete",
			Description: "Delete an ad. Args: ad_account_id, ad_id.",
			InputSchema: schemaObject(map[string]any{
				"ad_account_id": map[string]any{"type": "integer"},
				"ad_id":         map[string]any{"type": "string"},
			}, []string{"ad_account_id", "ad_id"}),
			Handler: a.toolAdDelete,
		},

		// ── Creatives ──
		{
			Name: "creative_upload",
			Description: "Upload an image or video to the platform's creative library. " +
				"Provide either storage_id (file id from the storage app — the bytes are fetched and forwarded) OR source_url (a public URL the platform can fetch directly). " +
				"Returns the platform-side hash/id needed to reference this asset from creative_create. " +
				"Args: ad_account_id, kind (image|video), storage_id?, source_url?, name?, platform_options.",
			InputSchema: schemaObject(map[string]any{
				"ad_account_id":    map[string]any{"type": "integer"},
				"kind":             map[string]any{"type": "string", "enum": []string{"image", "video"}},
				"storage_id":       map[string]any{"type": "integer"},
				"source_url":       map[string]any{"type": "string"},
				"name":             map[string]any{"type": "string"},
				"platform_options": map[string]any{"type": "object"},
			}, []string{"ad_account_id", "kind"}),
			Handler: a.toolCreativeUpload,
		},
		{
			Name:        "creative_list",
			Description: "List creatives in the ad account. Args: ad_account_id, limit?, after?.",
			InputSchema: schemaObject(map[string]any{
				"ad_account_id": map[string]any{"type": "integer"},
				"limit":         map[string]any{"type": "integer"},
				"after":         map[string]any{"type": "string"},
			}, []string{"ad_account_id"}),
			Handler: a.toolCreativeList,
		},

		// ── Audiences ──
		{
			Name:        "audience_list",
			Description: "List audiences in the ad account. Args: ad_account_id, limit?, after?.",
			InputSchema: schemaObject(map[string]any{
				"ad_account_id": map[string]any{"type": "integer"},
				"limit":         map[string]any{"type": "integer"},
				"after":         map[string]any{"type": "string"},
			}, []string{"ad_account_id"}),
			Handler: a.toolAudienceList,
		},
		{
			Name:        "audience_create_custom",
			Description: "Create a custom audience. Pass platform-specific source data via platform_options. Args: ad_account_id, name, description?, subtype? (e.g. CUSTOM, WEBSITE, ENGAGEMENT — Meta), platform_options.",
			InputSchema: schemaObject(map[string]any{
				"ad_account_id":    map[string]any{"type": "integer"},
				"name":             map[string]any{"type": "string"},
				"description":      map[string]any{"type": "string"},
				"subtype":          map[string]any{"type": "string"},
				"platform_options": map[string]any{"type": "object"},
			}, []string{"ad_account_id", "name"}),
			Handler: a.toolAudienceCreateCustom,
		},
		{
			Name:        "audience_create_lookalike",
			Description: "Create a lookalike audience from an existing custom audience. Args: ad_account_id, name, source_audience_id, country, ratio? (Meta: 0.01–0.20), platform_options.",
			InputSchema: schemaObject(map[string]any{
				"ad_account_id":      map[string]any{"type": "integer"},
				"name":               map[string]any{"type": "string"},
				"source_audience_id": map[string]any{"type": "string"},
				"country":            map[string]any{"type": "string"},
				"ratio":              map[string]any{"type": "number"},
				"platform_options":   map[string]any{"type": "object"},
			}, []string{"ad_account_id", "name", "source_audience_id", "country"}),
			Handler: a.toolAudienceCreateLookalike,
		},
	}
}

// ─── Account tools ──────────────────────────────────────────────────

func (a *App) toolAccountAdd(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	plat, _ := args["platform"].(string)
	def, ok := platforms[plat]
	if !ok {
		return mcpError(fmt.Sprintf("unsupported platform %q — available: %s", plat, strings.Join(platformKeys(), ", "))), nil
	}
	pid := os.Getenv("APTEVA_PROJECT_ID")
	forceNew, _ := args["force_new"].(bool)

	// Reuse path (mirrors social): the access token from any active
	// Meta connection in this project covers all the user's ad
	// accounts, so a fresh OAuth dance just produces a duplicate. Skip
	// straight to the picker when an active connection already exists.
	if !forceNew {
		var existingConnID int64
		err := ctx.AppDB().QueryRow(
			`SELECT connection_id FROM ad_accounts
			 WHERE project_id=? AND platform=? AND status='active'
			 ORDER BY id DESC LIMIT 1`,
			pid, def.Platform,
		).Scan(&existingConnID)
		if err != nil {
			conns, lerr := ctx.PlatformAPI().ListConnections(sdk.ConnectionFilter{
				ProjectID: pid,
				AppSlug:   def.IntegrationSlug,
			})
			if lerr == nil {
				for _, c := range conns {
					if c.Status == "active" {
						existingConnID = c.ID
						break
					}
				}
			}
		}
		if existingConnID > 0 {
			res, err := ctx.AppDB().Exec(
				`INSERT INTO pending_accounts (project_id, platform, integration_slug, connection_id, status, expires_at)
				 VALUES (?, ?, ?, ?, 'ready', ?)`,
				pid, def.Platform, def.IntegrationSlug, existingConnID,
				time.Now().UTC().Add(10*time.Minute),
			)
			if err != nil {
				return nil, fmt.Errorf("create pending account (reuse): %w", err)
			}
			pendingID, _ := res.LastInsertId()
			return map[string]any{
				"pending_account_id": pendingID,
				"platform":           def.Platform,
				"reused_connection":  existingConnID,
				"instructions": fmt.Sprintf(
					"Reusing the existing %s connection — no new OAuth needed. Call account_list_pending_pages with pending_account_id=%d to see the ad accounts you can manage, then account_finalize.",
					def.DisplayName, pendingID,
				),
			}, nil
		}
	}

	returnTo, _ := args["return_to"].(string)
	if returnTo == "" {
		returnTo = "/api/apps/ads/accounts/oauth_done"
	}

	now := time.Now().UTC()
	res, err := ctx.AppDB().Exec(
		`INSERT INTO pending_accounts (project_id, platform, integration_slug, status, expires_at)
		 VALUES (?, ?, ?, 'pending_oauth', ?)`,
		pid, def.Platform, def.IntegrationSlug, now.Add(10*time.Minute),
	)
	if err != nil {
		return nil, fmt.Errorf("create pending account: %w", err)
	}
	pendingID, _ := res.LastInsertId()

	sep := "?"
	if strings.Contains(returnTo, "?") {
		sep = "&"
	}
	returnURL := fmt.Sprintf("%s%spending=%d", returnTo, sep, pendingID)

	out, err := ctx.PlatformAPI().StartOAuth(sdk.OAuthStartRequest{
		IntegrationSlug: def.IntegrationSlug,
		ReturnURL:       returnURL,
		Name:            fmt.Sprintf("ads:%s:%d", def.Platform, pendingID),
		ProjectID:       pid,
	})
	if err != nil {
		_, _ = ctx.AppDB().Exec(`DELETE FROM pending_accounts WHERE id=?`, pendingID)
		return mcpError("OAuth start failed: " + err.Error()), nil
	}

	return map[string]any{
		"pending_account_id": pendingID,
		"platform":           def.Platform,
		"authorize_url":      out.AuthorizeURL,
		"expires_at":         out.ExpiresAt,
		"instructions": fmt.Sprintf(
			"Open this URL to authorize %s: %s\n\nAfter clicking Allow you'll be redirected back; then call account_list_pending_pages with pending_account_id=%d.",
			def.DisplayName, out.AuthorizeURL, pendingID,
		),
	}, nil
}

func (a *App) toolAccountListPendingPages(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pendingID := int64(intArg(args, "pending_account_id", 0))
	if pendingID <= 0 {
		return nil, errors.New("pending_account_id required")
	}
	row, err := a.getPending(pendingID)
	if err != nil {
		return mcpError("pending account not found: " + err.Error()), nil
	}
	if row.connectionID == 0 {
		return mcpError("OAuth not yet complete — open the authorize_url first, then re-call this tool"), nil
	}
	def, ok := platforms[row.platform]
	if !ok {
		return mcpError("unknown platform " + row.platform), nil
	}
	if def.ListAccountsTool == "" {
		return mcpError("no account-list tool wired for " + row.platform), nil
	}

	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(row.connectionID, def.ListAccountsTool, map[string]any{})
	if err != nil {
		return mcpError("list accounts failed: " + err.Error()), nil
	}
	if res == nil || !res.Success {
		body := ""
		if res != nil {
			body = string(res.Data)
		}
		return mcpError("upstream non-2xx: " + body), nil
	}

	// Meta returns {data:[...], paging:{...}}; fall back to raw array.
	var envelope struct {
		Data []map[string]any `json:"data"`
	}
	var rows []map[string]any
	if json.Unmarshal(res.Data, &envelope) == nil && envelope.Data != nil {
		rows = envelope.Data
	} else if err := json.Unmarshal(res.Data, &rows); err != nil {
		return mcpError("parse account-list response: " + err.Error()), nil
	}

	accounts := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		accounts = append(accounts, map[string]any{
			"id":       toString(walkPath(r, def.AccountListIDField)),
			"name":     toString(walkPath(r, def.AccountListNameField)),
			"currency": toString(walkPath(r, def.AccountListCurrencyField)),
			"timezone": toString(walkPath(r, def.AccountListTimezoneField)),
		})
	}
	return map[string]any{
		"pages":           accounts, // keyed "pages" to match social's panel-side contract
		"requires_picker": true,
		"platform":        row.platform,
		"hint":            "Pick an ad account by id (e.g. " + def.NativeIDFormat + ") and pass it as page_id to account_finalize.",
	}, nil
}

func (a *App) toolAccountFinalize(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pendingID := int64(intArg(args, "pending_account_id", 0))
	if pendingID <= 0 {
		return nil, errors.New("pending_account_id required")
	}
	row, err := a.getPending(pendingID)
	if err != nil {
		return mcpError("pending account not found: " + err.Error()), nil
	}
	if row.connectionID == 0 {
		return mcpError("OAuth not yet complete"), nil
	}
	def, ok := platforms[row.platform]
	if !ok {
		return mcpError("unknown platform " + row.platform), nil
	}

	pageID, _ := args["page_id"].(string)
	if pageID == "" {
		return mcpError("page_id is required (the platform's native ad-account id, e.g. " + def.NativeIDFormat + ")"), nil
	}
	displayName, _ := args["name"].(string)
	currency := ""
	timezone := ""

	// Verify the picked id is actually one this user can manage by
	// re-fetching the upstream list. Cheap insurance against typos.
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(row.connectionID, def.ListAccountsTool, map[string]any{})
	if err != nil {
		return mcpError("verify ad-account: " + err.Error()), nil
	}
	if res == nil || !res.Success {
		return mcpError("verify ad-account: upstream non-2xx"), nil
	}
	var envelope struct {
		Data []map[string]any `json:"data"`
	}
	var rows []map[string]any
	if json.Unmarshal(res.Data, &envelope) == nil && envelope.Data != nil {
		rows = envelope.Data
	} else if err := json.Unmarshal(res.Data, &rows); err != nil {
		return mcpError("parse account-list: " + err.Error()), nil
	}
	var matched map[string]any
	for _, r := range rows {
		if toString(walkPath(r, def.AccountListIDField)) == pageID {
			matched = r
			break
		}
	}
	if matched == nil {
		return mcpError("page_id not in the user's accessible ad accounts — re-call account_list_pending_pages"), nil
	}
	if displayName == "" {
		displayName = toString(walkPath(matched, def.AccountListNameField))
	}
	if displayName == "" {
		displayName = pageID
	}
	currency = toString(walkPath(matched, def.AccountListCurrencyField))
	timezone = toString(walkPath(matched, def.AccountListTimezoneField))

	pid := os.Getenv("APTEVA_PROJECT_ID")
	insertRes, err := ctx.AppDB().Exec(
		`INSERT INTO ad_accounts (project_id, platform, connection_id, native_account_id, display_name, currency, timezone_name, status)
		 VALUES (?, ?, ?, ?, ?, ?, ?, 'active')`,
		pid, def.Platform, row.connectionID, pageID, displayName, nullable(currency), nullable(timezone),
	)
	if err != nil {
		// UNIQUE collision = same ad_account already added — re-activate.
		if strings.Contains(err.Error(), "UNIQUE") {
			_, _ = ctx.AppDB().Exec(
				`UPDATE ad_accounts SET status='active', display_name=?, currency=?, timezone_name=?
				 WHERE project_id=? AND platform=? AND native_account_id=?`,
				displayName, nullable(currency), nullable(timezone),
				pid, def.Platform, pageID,
			)
			var id int64
			_ = ctx.AppDB().QueryRow(
				`SELECT id FROM ad_accounts WHERE project_id=? AND platform=? AND native_account_id=?`,
				pid, def.Platform, pageID,
			).Scan(&id)
			_, _ = ctx.AppDB().Exec(`UPDATE pending_accounts SET status='finalized' WHERE id=?`, pendingID)
			return map[string]any{
				"ad_account_id":     id,
				"platform":          def.Platform,
				"display_name":      displayName,
				"native_account_id": pageID,
				"reactivated":       true,
			}, nil
		}
		return nil, fmt.Errorf("insert ad_account: %w", err)
	}
	id, _ := insertRes.LastInsertId()
	_, _ = ctx.AppDB().Exec(`UPDATE pending_accounts SET status='finalized' WHERE id=?`, pendingID)

	ctx.Emit("account.added", map[string]any{
		"ad_account_id":     id,
		"platform":          def.Platform,
		"native_account_id": pageID,
	})

	return map[string]any{
		"ad_account_id":     id,
		"platform":          def.Platform,
		"display_name":      displayName,
		"native_account_id": pageID,
		"currency":          currency,
		"timezone":          timezone,
	}, nil
}

func (a *App) toolAccountList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid := os.Getenv("APTEVA_PROJECT_ID")
	platformFilter, _ := args["platform"].(string)
	statusFilter, _ := args["status"].(string)

	q := `SELECT id, platform, connection_id, native_account_id, display_name,
	             COALESCE(currency,''), COALESCE(timezone_name,''), status, created_at
	      FROM ad_accounts WHERE project_id=?`
	qArgs := []any{pid}
	if platformFilter != "" {
		q += " AND platform=?"
		qArgs = append(qArgs, platformFilter)
	}
	if statusFilter != "" {
		q += " AND status=?"
		qArgs = append(qArgs, statusFilter)
	}
	q += " ORDER BY id DESC"
	rows, err := ctx.AppDB().Query(q, qArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var (
			id, connID                                                   int64
			platform, native, name, currency, timezone, status, created string
		)
		if err := rows.Scan(&id, &platform, &connID, &native, &name, &currency, &timezone, &status, &created); err != nil {
			continue
		}
		out = append(out, map[string]any{
			"id":                id,
			"platform":          platform,
			"connection_id":     connID,
			"native_account_id": native,
			"display_name":      name,
			"currency":          currency,
			"timezone":          timezone,
			"status":            status,
			"created_at":        created,
		})
	}
	return map[string]any{"accounts": out}, nil
}

func (a *App) toolAccountDisconnect(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64(intArg(args, "id", 0))
	if id <= 0 {
		return nil, errors.New("id required")
	}
	pid := os.Getenv("APTEVA_PROJECT_ID")
	var connID int64
	if err := ctx.AppDB().QueryRow(
		`SELECT connection_id FROM ad_accounts WHERE id=? AND project_id=?`,
		id, pid,
	).Scan(&connID); err != nil {
		return mcpError("account not found"), nil
	}
	if _, err := ctx.AppDB().Exec(`DELETE FROM ad_accounts WHERE id=?`, id); err != nil {
		return nil, err
	}
	var siblings int
	_ = ctx.AppDB().QueryRow(
		`SELECT COUNT(*) FROM ad_accounts WHERE connection_id=?`, connID,
	).Scan(&siblings)
	if siblings == 0 {
		if err := ctx.PlatformAPI().DisconnectConnection(connID); err != nil {
			ctx.Logger().Warn("DisconnectConnection failed", "conn", connID, "err", err)
		}
	}
	ctx.Emit("account.removed", map[string]any{"ad_account_id": id})
	return map[string]any{"deleted": id}, nil
}

// ─── Resolution helpers ────────────────────────────────────────────

type adAccount struct {
	ID              int64
	Platform        string
	ConnectionID    int64
	NativeAccountID string
}

func (a *App) resolveAdAccount(ctx *sdk.AppCtx, args map[string]any) (*adAccount, *platformDef, map[string]any) {
	pid := os.Getenv("APTEVA_PROJECT_ID")
	id := int64(intArg(args, "ad_account_id", 0))
	if id <= 0 {
		return nil, nil, mcpError("ad_account_id required")
	}
	var acct adAccount
	if err := ctx.AppDB().QueryRow(
		`SELECT id, platform, connection_id, native_account_id
		 FROM ad_accounts WHERE id=? AND project_id=? AND status='active'`,
		id, pid,
	).Scan(&acct.ID, &acct.Platform, &acct.ConnectionID, &acct.NativeAccountID); err != nil {
		return nil, nil, mcpError("ad_account not found or not active")
	}
	def, ok := platforms[acct.Platform]
	if !ok {
		return nil, nil, mcpError("unsupported platform " + acct.Platform)
	}
	return &acct, &def, nil
}

func (a *App) execIntegrationTool(ctx *sdk.AppCtx, acct *adAccount, tool string, input map[string]any) (any, map[string]any) {
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(acct.ConnectionID, tool, input)
	if err != nil {
		return nil, mcpError(tool + ": " + err.Error())
	}
	if res == nil || !res.Success {
		body := ""
		if res != nil {
			body = string(res.Data)
		}
		return nil, mcpError(tool + ": upstream non-2xx: " + body)
	}
	var parsed any
	if len(res.Data) > 0 {
		if err := json.Unmarshal(res.Data, &parsed); err != nil {
			return nil, mcpError(tool + ": parse: " + err.Error())
		}
	}
	return parsed, nil
}

// mergeOptions overlays platform_options onto the base input map. Keys
// in platform_options win — this is the escape hatch for fields the
// unified schema doesn't model. nil/empty options is a no-op.
func mergeOptions(base map[string]any, args map[string]any) map[string]any {
	opts, _ := args["platform_options"].(map[string]any)
	for k, v := range opts {
		base[k] = v
	}
	return base
}

// ─── Campaign tools ────────────────────────────────────────────────

// metaCampaignObjective maps our unified objective enum to Meta's
// OUTCOME_* enum. Future platforms get their own table.
var metaCampaignObjective = map[string]string{
	"sales":          "OUTCOME_SALES",
	"leads":          "OUTCOME_LEADS",
	"traffic":        "OUTCOME_TRAFFIC",
	"engagement":     "OUTCOME_ENGAGEMENT",
	"awareness":      "OUTCOME_AWARENESS",
	"app_promotion":  "OUTCOME_APP_PROMOTION",
}

var metaBidStrategy = map[string]string{
	"lowest_cost":              "LOWEST_COST_WITHOUT_CAP",
	"lowest_cost_with_bid_cap": "LOWEST_COST_WITH_BID_CAP",
	"cost_cap":                 "COST_CAP",
	"bid_cap":                  "BID_CAP",
}

func (a *App) toolCampaignCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	acct, def, errOut := a.resolveAdAccount(ctx, args)
	if errOut != nil {
		return errOut, nil
	}
	name, _ := args["name"].(string)
	objective, _ := args["objective"].(string)
	if name == "" || objective == "" {
		return mcpError("name and objective required"), nil
	}
	input := map[string]any{
		def.AccountIDInputField: acct.NativeAccountID,
		"name":                  name,
	}
	if acct.Platform == "meta" {
		mapped, ok := metaCampaignObjective[objective]
		if !ok {
			return mcpError("unsupported objective for meta: " + objective), nil
		}
		input["objective"] = mapped
		// Meta requires special_ad_categories; pass [] when caller didn't.
		opts, _ := args["platform_options"].(map[string]any)
		if _, hasSAC := opts["special_ad_categories"]; !hasSAC {
			input["special_ad_categories"] = []any{}
		}
	}
	if status, ok := args["status"].(string); ok && status != "" {
		input["status"] = status
	} else {
		input["status"] = "PAUSED"
	}
	if v := intArg(args, "daily_budget_cents", 0); v > 0 {
		input["daily_budget"] = strconv.Itoa(v)
	}
	if v := intArg(args, "lifetime_budget_cents", 0); v > 0 {
		input["lifetime_budget"] = strconv.Itoa(v)
	}
	if bs, _ := args["bid_strategy"].(string); bs != "" {
		if mapped, ok := metaBidStrategy[bs]; ok {
			input["bid_strategy"] = mapped
		}
	}
	if v, _ := args["start_time"].(string); v != "" {
		input["start_time"] = v
	}
	if v, _ := args["end_time"].(string); v != "" {
		input["end_time"] = v
	}
	mergeOptions(input, args)
	return a.execOrErr(ctx, acct, def.CampaignCreateTool, input)
}

func (a *App) toolCampaignList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	acct, def, errOut := a.resolveAdAccount(ctx, args)
	if errOut != nil {
		return errOut, nil
	}
	input := map[string]any{def.AccountIDInputField: acct.NativeAccountID}
	if v := intArg(args, "limit", 0); v > 0 {
		input["limit"] = v
	}
	if v, _ := args["after"].(string); v != "" {
		input["after"] = v
	}
	if v, _ := args["status"].(string); v != "" {
		// Meta supports filtering by effective_status array.
		input["filtering"] = fmt.Sprintf(`[{"field":"effective_status","operator":"IN","value":["%s"]}]`, v)
	}
	return a.execOrErr(ctx, acct, def.CampaignListTool, input)
}

func (a *App) toolCampaignUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	acct, def, errOut := a.resolveAdAccount(ctx, args)
	if errOut != nil {
		return errOut, nil
	}
	cid, _ := args["campaign_id"].(string)
	if cid == "" {
		return mcpError("campaign_id required"), nil
	}
	input := map[string]any{"campaignId": cid}
	if v, _ := args["name"].(string); v != "" {
		input["name"] = v
	}
	if v, _ := args["status"].(string); v != "" {
		input["status"] = v
	}
	if v := intArg(args, "daily_budget_cents", 0); v > 0 {
		input["daily_budget"] = strconv.Itoa(v)
	}
	if v := intArg(args, "lifetime_budget_cents", 0); v > 0 {
		input["lifetime_budget"] = strconv.Itoa(v)
	}
	if bs, _ := args["bid_strategy"].(string); bs != "" {
		if mapped, ok := metaBidStrategy[bs]; ok {
			input["bid_strategy"] = mapped
		}
	}
	if v, _ := args["start_time"].(string); v != "" {
		input["start_time"] = v
	}
	if v, _ := args["end_time"].(string); v != "" {
		input["stop_time"] = v // Meta calls it stop_time on update
	}
	mergeOptions(input, args)
	return a.execOrErr(ctx, acct, def.CampaignUpdateTool, input)
}

func (a *App) toolCampaignPause(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	args["status"] = "PAUSED"
	return a.toolCampaignUpdate(ctx, args)
}

func (a *App) toolCampaignResume(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	args["status"] = "ACTIVE"
	return a.toolCampaignUpdate(ctx, args)
}

func (a *App) toolCampaignDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	acct, def, errOut := a.resolveAdAccount(ctx, args)
	if errOut != nil {
		return errOut, nil
	}
	cid, _ := args["campaign_id"].(string)
	if cid == "" {
		return mcpError("campaign_id required"), nil
	}
	return a.execOrErr(ctx, acct, def.CampaignDeleteTool, map[string]any{"campaignId": cid})
}

// ─── Ad set tools ───────────────────────────────────────────────────

var metaOptimizationGoal = map[string]string{
	"link_clicks":          "LINK_CLICKS",
	"conversions":          "OFFSITE_CONVERSIONS",
	"leads":                "LEAD_GENERATION",
	"reach":                "REACH",
	"impressions":          "IMPRESSIONS",
	"page_likes":           "PAGE_LIKES",
	"post_engagement":      "POST_ENGAGEMENT",
	"thruplay":             "THRUPLAY",
	"app_installs":         "APP_INSTALLS",
	"value":                "VALUE",
	"landing_page_views":   "LANDING_PAGE_VIEWS",
}

var metaBillingEvent = map[string]string{
	"impressions":  "IMPRESSIONS",
	"link_clicks":  "LINK_CLICKS",
	"thruplay":     "THRUPLAY",
}

func (a *App) toolAdSetCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	acct, def, errOut := a.resolveAdAccount(ctx, args)
	if errOut != nil {
		return errOut, nil
	}
	name, _ := args["name"].(string)
	cid, _ := args["campaign_id"].(string)
	og, _ := args["optimization_goal"].(string)
	targeting, _ := args["targeting"].(map[string]any)
	if name == "" || cid == "" || og == "" || len(targeting) == 0 {
		return mcpError("name, campaign_id, optimization_goal, targeting required"), nil
	}
	mappedOG, ok := metaOptimizationGoal[og]
	if !ok {
		return mcpError("unsupported optimization_goal: " + og), nil
	}
	be, _ := args["billing_event"].(string)
	if be == "" {
		be = "impressions"
	}
	mappedBE := metaBillingEvent[be]
	input := map[string]any{
		def.AccountIDInputField: acct.NativeAccountID,
		"campaign_id":           cid,
		"name":                  name,
		"optimization_goal":     mappedOG,
		"billing_event":         mappedBE,
		"targeting":             targeting,
	}
	if v := intArg(args, "daily_budget_cents", 0); v > 0 {
		input["daily_budget"] = strconv.Itoa(v)
	}
	if v := intArg(args, "lifetime_budget_cents", 0); v > 0 {
		input["lifetime_budget"] = strconv.Itoa(v)
	}
	if bs, _ := args["bid_strategy"].(string); bs != "" {
		if mapped, ok := metaBidStrategy[bs]; ok {
			input["bid_strategy"] = mapped
		}
	}
	if v := intArg(args, "bid_amount_cents", 0); v > 0 {
		input["bid_amount"] = strconv.Itoa(v)
	}
	if v, _ := args["start_time"].(string); v != "" {
		input["start_time"] = v
	}
	if v, _ := args["end_time"].(string); v != "" {
		input["end_time"] = v
	}
	if v, _ := args["status"].(string); v != "" {
		input["status"] = v
	} else {
		input["status"] = "PAUSED"
	}
	if v, _ := args["promoted_object"].(map[string]any); len(v) > 0 {
		input["promoted_object"] = v
	}
	if v, _ := args["destination_type"].(string); v != "" {
		input["destination_type"] = v
	}
	if v, _ := args["dsa_beneficiary"].(string); v != "" {
		input["dsa_beneficiary"] = v
	}
	if v, _ := args["dsa_payor"].(string); v != "" {
		input["dsa_payor"] = v
	}
	mergeOptions(input, args)
	return a.execOrErr(ctx, acct, def.AdSetCreateTool, input)
}

func (a *App) toolAdSetList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	acct, def, errOut := a.resolveAdAccount(ctx, args)
	if errOut != nil {
		return errOut, nil
	}
	input := map[string]any{def.AccountIDInputField: acct.NativeAccountID}
	if cid, _ := args["campaign_id"].(string); cid != "" {
		input["campaign_id"] = cid
	}
	if v := intArg(args, "limit", 0); v > 0 {
		input["limit"] = v
	}
	if v, _ := args["after"].(string); v != "" {
		input["after"] = v
	}
	return a.execOrErr(ctx, acct, def.AdSetListTool, input)
}

func (a *App) toolAdSetUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	acct, def, errOut := a.resolveAdAccount(ctx, args)
	if errOut != nil {
		return errOut, nil
	}
	asid, _ := args["adset_id"].(string)
	if asid == "" {
		return mcpError("adset_id required"), nil
	}
	input := map[string]any{"adsetId": asid}
	if v, _ := args["name"].(string); v != "" {
		input["name"] = v
	}
	if v, _ := args["status"].(string); v != "" {
		input["status"] = v
	}
	if v := intArg(args, "daily_budget_cents", 0); v > 0 {
		input["daily_budget"] = strconv.Itoa(v)
	}
	if v := intArg(args, "lifetime_budget_cents", 0); v > 0 {
		input["lifetime_budget"] = strconv.Itoa(v)
	}
	if v := intArg(args, "bid_amount_cents", 0); v > 0 {
		input["bid_amount"] = strconv.Itoa(v)
	}
	if v, _ := args["targeting"].(map[string]any); len(v) > 0 {
		input["targeting"] = v
	}
	mergeOptions(input, args)
	return a.execOrErr(ctx, acct, def.AdSetUpdateTool, input)
}

func (a *App) toolAdSetDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	acct, def, errOut := a.resolveAdAccount(ctx, args)
	if errOut != nil {
		return errOut, nil
	}
	asid, _ := args["adset_id"].(string)
	if asid == "" {
		return mcpError("adset_id required"), nil
	}
	return a.execOrErr(ctx, acct, def.AdSetDeleteTool, map[string]any{"adsetId": asid})
}

// ─── Ad tools ───────────────────────────────────────────────────────

func (a *App) toolAdCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	acct, def, errOut := a.resolveAdAccount(ctx, args)
	if errOut != nil {
		return errOut, nil
	}
	asid, _ := args["adset_id"].(string)
	name, _ := args["name"].(string)
	cr, _ := args["creative_id"].(string)
	if asid == "" || name == "" || cr == "" {
		return mcpError("adset_id, name, creative_id required"), nil
	}
	input := map[string]any{
		def.AccountIDInputField: acct.NativeAccountID,
		"adset_id":              asid,
		"name":                  name,
		"creative_id":           cr,
	}
	if v, _ := args["status"].(string); v != "" {
		input["status"] = v
	} else {
		input["status"] = "PAUSED"
	}
	mergeOptions(input, args)
	return a.execOrErr(ctx, acct, def.AdCreateTool, input)
}

func (a *App) toolAdList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	acct, def, errOut := a.resolveAdAccount(ctx, args)
	if errOut != nil {
		return errOut, nil
	}
	input := map[string]any{def.AccountIDInputField: acct.NativeAccountID}
	if v, _ := args["adset_id"].(string); v != "" {
		input["adset_id"] = v
	}
	if v := intArg(args, "limit", 0); v > 0 {
		input["limit"] = v
	}
	if v, _ := args["after"].(string); v != "" {
		input["after"] = v
	}
	return a.execOrErr(ctx, acct, def.AdListTool, input)
}

func (a *App) toolAdUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	acct, def, errOut := a.resolveAdAccount(ctx, args)
	if errOut != nil {
		return errOut, nil
	}
	adID, _ := args["ad_id"].(string)
	if adID == "" {
		return mcpError("ad_id required"), nil
	}
	input := map[string]any{"adId": adID}
	if v, _ := args["name"].(string); v != "" {
		input["name"] = v
	}
	if v, _ := args["status"].(string); v != "" {
		input["status"] = v
	}
	if v, _ := args["creative_id"].(string); v != "" {
		input["creative_id"] = v
	}
	mergeOptions(input, args)
	return a.execOrErr(ctx, acct, def.AdUpdateTool, input)
}

func (a *App) toolAdDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	acct, def, errOut := a.resolveAdAccount(ctx, args)
	if errOut != nil {
		return errOut, nil
	}
	adID, _ := args["ad_id"].(string)
	if adID == "" {
		return mcpError("ad_id required"), nil
	}
	return a.execOrErr(ctx, acct, def.AdDeleteTool, map[string]any{"adId": adID})
}

// ─── Creative tools ────────────────────────────────────────────────

func (a *App) toolCreativeUpload(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	acct, def, errOut := a.resolveAdAccount(ctx, args)
	if errOut != nil {
		return errOut, nil
	}
	kind, _ := args["kind"].(string)
	if kind != "image" && kind != "video" {
		return mcpError("kind must be image or video"), nil
	}
	storageID := int64(intArg(args, "storage_id", 0))
	sourceURL, _ := args["source_url"].(string)
	if storageID == 0 && sourceURL == "" {
		return mcpError("either storage_id or source_url is required"), nil
	}

	input := map[string]any{def.AccountIDInputField: acct.NativeAccountID}
	if name, _ := args["name"].(string); name != "" {
		input["name"] = name
	}

	// Resolve bytes via the storage app when storage_id was given.
	// CallAppResult unwraps the MCP envelope and decodes the storage
	// app's return shape directly.
	if storageID > 0 {
		var fetched struct {
			ID       int64  `json:"id"`
			URL      string `json:"url"`
			Filename string `json:"filename"`
			MimeType string `json:"mime_type"`
		}
		if err := ctx.PlatformAPI().CallAppResult("storage", "files_get", map[string]any{"id": storageID}, &fetched); err != nil {
			return mcpError("storage.files_get: " + err.Error()), nil
		}
		if fetched.URL == "" {
			return mcpError("storage returned no URL for file id"), nil
		}
		// Forward to Meta as a public URL — the integration's
		// upload-by-URL path is the simple variant. For larger / private
		// files an operator can layer a signed-URL provider in front;
		// out of scope for v1.
		sourceURL = fetched.URL
	}

	tool := def.CreativeUploadImageTool
	if kind == "video" {
		tool = def.CreativeUploadVideoTool
	}
	// Meta's upload tools accept different field names for image vs
	// video. v1 sends `url` for image (matches creative_upload_image)
	// and `file_url` for video (matches creative_upload_video). Caller
	// can override via platform_options if needed.
	if kind == "image" {
		input["url"] = sourceURL
	} else {
		input["file_url"] = sourceURL
	}
	mergeOptions(input, args)
	return a.execOrErr(ctx, acct, tool, input)
}

func (a *App) toolCreativeList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	acct, def, errOut := a.resolveAdAccount(ctx, args)
	if errOut != nil {
		return errOut, nil
	}
	input := map[string]any{def.AccountIDInputField: acct.NativeAccountID}
	if v := intArg(args, "limit", 0); v > 0 {
		input["limit"] = v
	}
	if v, _ := args["after"].(string); v != "" {
		input["after"] = v
	}
	return a.execOrErr(ctx, acct, def.CreativeListTool, input)
}

// ─── Audience tools ────────────────────────────────────────────────

func (a *App) toolAudienceList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	acct, def, errOut := a.resolveAdAccount(ctx, args)
	if errOut != nil {
		return errOut, nil
	}
	input := map[string]any{def.AccountIDInputField: acct.NativeAccountID}
	if v := intArg(args, "limit", 0); v > 0 {
		input["limit"] = v
	}
	if v, _ := args["after"].(string); v != "" {
		input["after"] = v
	}
	return a.execOrErr(ctx, acct, def.AudienceListTool, input)
}

func (a *App) toolAudienceCreateCustom(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	acct, def, errOut := a.resolveAdAccount(ctx, args)
	if errOut != nil {
		return errOut, nil
	}
	name, _ := args["name"].(string)
	if name == "" {
		return mcpError("name required"), nil
	}
	input := map[string]any{
		def.AccountIDInputField: acct.NativeAccountID,
		"name":                  name,
	}
	if v, _ := args["description"].(string); v != "" {
		input["description"] = v
	}
	if v, _ := args["subtype"].(string); v != "" {
		input["subtype"] = v
	}
	mergeOptions(input, args)
	return a.execOrErr(ctx, acct, def.AudienceCreateCustomTool, input)
}

func (a *App) toolAudienceCreateLookalike(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	acct, def, errOut := a.resolveAdAccount(ctx, args)
	if errOut != nil {
		return errOut, nil
	}
	name, _ := args["name"].(string)
	src, _ := args["source_audience_id"].(string)
	country, _ := args["country"].(string)
	if name == "" || src == "" || country == "" {
		return mcpError("name, source_audience_id, country required"), nil
	}
	input := map[string]any{
		def.AccountIDInputField: acct.NativeAccountID,
		"name":                  name,
		"origin_audience_id":    src,
		"country":               country,
	}
	if v, ok := args["ratio"].(float64); ok && v > 0 {
		input["ratio"] = v
	}
	mergeOptions(input, args)
	return a.execOrErr(ctx, acct, def.AudienceCreateLookalikeTool, input)
}

// ─── Helpers ───────────────────────────────────────────────────────

// execOrErr wraps execIntegrationTool to fit the MCP-tool return contract.
func (a *App) execOrErr(ctx *sdk.AppCtx, acct *adAccount, tool string, input map[string]any) (any, error) {
	parsed, errOut := a.execIntegrationTool(ctx, acct, tool, input)
	if errOut != nil {
		return errOut, nil
	}
	return parsed, nil
}

type pendingRow struct {
	id              int64
	projectID       string
	platform        string
	integrationSlug string
	connectionID    int64
	status          string
}

func (a *App) getPending(id int64) (*pendingRow, error) {
	var p pendingRow
	err := globalCtx.AppDB().QueryRow(
		`SELECT id, project_id, platform, integration_slug, connection_id, status
		 FROM pending_accounts WHERE id=?`,
		id,
	).Scan(&p.id, &p.projectID, &p.platform, &p.integrationSlug, &p.connectionID, &p.status)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// ─── Common JSON helpers (mirrors social/main.go) ──────────────────

func mcpError(msg string) map[string]any {
	return map[string]any{
		"isError": true,
		"content": []map[string]any{{"type": "text", "text": msg}},
	}
}

func schemaObject(props map[string]any, required []string) map[string]any {
	s := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func intArg(m map[string]any, key string, def int) int {
	switch v := m[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	case string:
		s := strings.TrimSpace(v)
		if s == "" {
			return def
		}
		if n, err := strconv.Atoi(s); err == nil {
			return n
		}
	}
	return def
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func toString(v any) string {
	switch s := v.(type) {
	case string:
		return s
	case nil:
		return ""
	default:
		b, _ := json.Marshal(v)
		return strings.Trim(string(b), `"`)
	}
}

// walkPath resolves dotted paths against a JSON-shaped map. "a.b.c"
// walks into nested maps; returns nil when any segment is missing or
// not a map.
func walkPath(m map[string]any, path string) any {
	if path == "" || m == nil {
		return nil
	}
	parts := strings.Split(path, ".")
	var cur any = m
	for _, p := range parts {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = mm[p]
	}
	return cur
}

func platformKeys() []string {
	out := make([]string, 0, len(platforms))
	for k := range platforms {
		out = append(out, k)
	}
	return out
}

// quiet "imported and not used" for stdlib pkgs only used in some paths.
var _ = sql.Drivers
