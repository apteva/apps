// ads v0.1 — unified control plane for paid advertising.
//
// Architecture:
//   - Platform-specific API shapes live behind adapters. The public MCP
//     surface stays generic while each adapter speaks native upstream
//     semantics (Meta Graph direct tools, Google Ads GAQL + mutate ops).
//   - Accounts are added at runtime via PlatformAPI.StartOAuth, which
//     returns an authorize URL the panel/agent hands the user. After the
//     dance, the platform 302s back to /accounts/oauth_done; we look up
//     the matching pending_accounts row and the user picks an ad account
//     from the connected platform's account list.
//   - Campaign configuration remains upstream. Locally we retain account
//     bindings, discovered resource ownership, normalized entity snapshots,
//     and daily performance facts for fast reporting and history. Each
//     mutation still resolves a local ad_account id to its provider account
//     and proxies through ExecuteIntegrationTool.
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
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

const manifestYAML = `schema: apteva-app/v1
name: ads
display_name: Ads
version: 0.1.35
scopes: [project, global]
requires:
  permissions:
    - db.write.app
    - net.egress
    - platform.connections.execute
    - platform.oauth.start
    - platform.apps.call
  apps:
    - name: storage
      version: ">=0.1.0"
      reason: "Resolve stored image and video files for creative uploads."
provides:
  http_routes:
    - prefix: /
  workers:
    - name: performance_collector
      schedule: "@every 1m"
  publishes:
    - name: account.changed
      description: "An ad-account binding was added or removed."
    - name: entity.changed
      description: "A campaign, ad group, or ad was created, updated, or deleted."
    - name: performance.updated
      description: "Normalized performance cache was refreshed for an account and level."
    - name: performance.sync_failed
      description: "A performance refresh failed and was scheduled for retry."
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

// platformDef captures per-network metadata. Behavior lives in the
// platformAdapter implementations below so platform-specific API
// semantics do not leak through every MCP handler.
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
	ListAccountsTool         string
	AccountListIDField       string // path within each account row
	AccountListNameField     string
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
	CreativeGetTool         string
	CreativeDeleteTool      string
	CreativeListTool        string
	CreativeUploadImageTool string
	CreativeUploadVideoTool string
	CreativeAssetStatusTool string
	CreativeAssetDeleteTool string

	// Audience tools.
	AudienceListTool            string
	AudienceCreateCustomTool    string
	AudienceCreateLookalikeTool string

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
		CreativeGetTool:             "creative_get",
		CreativeDeleteTool:          "creative_delete",
		CreativeListTool:            "creative_list",
		CreativeUploadImageTool:     "creative_upload_image",
		CreativeUploadVideoTool:     "creative_upload_video",
		CreativeAssetStatusTool:     "video_status",
		CreativeAssetDeleteTool:     "video_delete",
		AudienceListTool:            "audience_list",
		AudienceCreateCustomTool:    "audience_create_custom",
		AudienceCreateLookalikeTool: "audience_create_lookalike",
		AccountIDInputField:         "adAccountId",
	},
	"google": {
		Platform:                 "google",
		IntegrationSlug:          "google-ads",
		DisplayName:              "Google Ads",
		NativeIDFormat:           "<customer_id without hyphens>",
		ListAccountsTool:         "list_accounts",
		AccountIDInputField:      "customer_id",
		CampaignCreateTool:       "campaign_mutate",
		CampaignListTool:         "search",
		CampaignUpdateTool:       "campaign_mutate",
		CampaignDeleteTool:       "campaign_mutate",
		AdSetCreateTool:          "ad_group_mutate",
		AdSetListTool:            "search",
		AdSetUpdateTool:          "ad_group_mutate",
		AdSetDeleteTool:          "ad_group_mutate",
		AdCreateTool:             "ad_mutate",
		AdListTool:               "search",
		AdUpdateTool:             "ad_mutate",
		AdDeleteTool:             "ad_mutate",
		CreativeCreateTool:       "asset_mutate",
		CreativeGetTool:          "search",
		CreativeListTool:         "search",
		CreativeAssetStatusTool:  "search",
		AudienceListTool:         "search",
		AudienceCreateCustomTool: "user_list_mutate",
	},
	"x": {
		Platform:                    "x",
		IntegrationSlug:             "twitter-ads",
		DisplayName:                 "X Ads",
		NativeIDFormat:              "base-36 account id",
		ListAccountsTool:            "list_accounts",
		CampaignCreateTool:          "create_campaign",
		CampaignListTool:            "list_campaigns",
		CampaignUpdateTool:          "update_campaign",
		CampaignDeleteTool:          "delete_campaign",
		AdSetCreateTool:             "create_line_item",
		AdSetListTool:               "list_line_items",
		AdSetUpdateTool:             "update_line_item",
		AdSetDeleteTool:             "delete_line_item",
		AdCreateTool:                "create_promoted_tweet",
		AdListTool:                  "list_promoted_tweets",
		AdDeleteTool:                "delete_promoted_tweet",
		CreativeCreateTool:          "create_tweet",
		CreativeGetTool:             "list_tweets",
		CreativeListTool:            "list_tweets",
		CreativeUploadImageTool:     "upload_media",
		CreativeUploadVideoTool:     "upload_media",
		AudienceListTool:            "list_custom_audiences",
		AudienceCreateCustomTool:    "create_custom_audience",
		AudienceCreateLookalikeTool: "",
		AccountIDInputField:         "account_id",
	},
	"reddit": {
		Platform:                    "reddit",
		IntegrationSlug:             "reddit-ads",
		DisplayName:                 "Reddit Ads",
		NativeIDFormat:              "a2_<id>",
		ListAccountsTool:            "list_ad_accounts_by_business",
		CampaignCreateTool:          "create_campaign",
		CampaignListTool:            "list_campaigns",
		CampaignUpdateTool:          "update_campaign",
		CampaignDeleteTool:          "update_campaign",
		AdSetCreateTool:             "create_ad_group",
		AdSetListTool:               "list_ad_groups",
		AdSetUpdateTool:             "update_ad_group",
		AdSetDeleteTool:             "update_ad_group",
		AdCreateTool:                "create_ad",
		AdListTool:                  "list_ads",
		AdUpdateTool:                "update_ad",
		AdDeleteTool:                "update_ad",
		CreativeCreateTool:          "create_structured_post_job",
		CreativeGetTool:             "get_structured_post",
		CreativeListTool:            "list_structured_posts",
		CreativeAssetStatusTool:     "get_structured_post_job",
		AudienceListTool:            "list_custom_audiences",
		AudienceCreateCustomTool:    "create_custom_audience",
		AudienceCreateLookalikeTool: "",
		AccountIDInputField:         "ad_account_id",
	},
}

type platformAdapter interface {
	ListAccounts(a *App, ctx *sdk.AppCtx, row *pendingRow, def *platformDef) ([]map[string]any, error)
	CampaignCreate(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error)
	CampaignList(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error)
	CampaignPerformance(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error)
	CampaignUpdate(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error)
	CampaignDelete(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error)
	AdSetCreate(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error)
	AdSetList(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error)
	AdSetUpdate(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error)
	AdSetDelete(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error)
	AdCreate(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error)
	AdList(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error)
	AdUpdate(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error)
	AdDelete(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error)
	CreativeCreate(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error)
	CreativeGet(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error)
	CreativeDelete(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error)
	CreativeUpload(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error)
	CreativeAssetStatus(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error)
	CreativeAssetDelete(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error)
	CreativeList(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error)
	AudienceList(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error)
	AudienceCreateCustom(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error)
	AudienceCreateLookalike(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error)
}

var platformAdapters = map[string]platformAdapter{
	"meta":   metaAdapter{},
	"google": googleAdapter{},
	"x":      xRedditAdapter{},
	"reddit": xRedditAdapter{},
}

var globalCtx *sdk.AppCtx

type App struct {
	retryDelay        func(retry int) time.Duration
	sleep             func(ctx *sdk.AppCtx, delay time.Duration) bool
	analyticsMu       sync.Mutex
	analyticsInFlight map[string]*analyticsSyncCall
}

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

func (a *App) OnUnmount(*sdk.AppCtx) error    { return nil }
func (a *App) Channels() []sdk.ChannelFactory { return nil }
func (a *App) Workers() []sdk.Worker {
	return []sdk.Worker{{
		Name:     "performance_collector",
		Schedule: "@every 1m",
		Run:      a.runPerformanceCollector,
	}}
}
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

func main() { sdk.Run(&App{}) }

func projectScope(ctx *sdk.AppCtx, argSets ...map[string]any) string {
	if ctx != nil {
		if pid := strings.TrimSpace(ctx.CurrentProject()); pid != "" {
			return pid
		}
	}
	for _, args := range argSets {
		if pid := strings.TrimSpace(stringArgAny(args, "_project_id", "project_id")); pid != "" {
			return pid
		}
	}
	return ""
}

func requireProject(ctx *sdk.AppCtx, argSets ...map[string]any) (string, error) {
	if pid := projectScope(ctx, argSets...); pid != "" {
		return pid, nil
	}
	return "", errors.New("project_id required for the global Ads install")
}

// ─── HTTP routes ────────────────────────────────────────────────────

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/accounts", Handler: a.handleAccountsAPI},
		{Pattern: "/accounts/start", Handler: a.handleAccountsStart},
		// OAuth-completion landing page; the platform 302s here with
		// ?conn_id=<id>&status=ok and ?pending=<pending_account_id>.
		{Pattern: "/accounts/oauth_done", Handler: a.handleOAuthDone},
		{Pattern: "/accounts/finalize", Handler: a.handleAccountsFinalize},
		{Pattern: "/accounts/", Handler: a.handleAccountsItem},
		{Pattern: "/platforms", Handler: a.handlePlatforms},
	}
}

// handleOAuthDone is the landing page the platform 302s the user back
// to after OAuth completes. Query params: ?conn_id=<id>&status=ok&pending=<pid>.
// We update the matching pending_accounts row and render a tiny page that
// notifies the already-open panel popup via postMessage. This mirrors the
// Social app flow and avoids relying on a full dashboard redirect inside
// the popup.
func (a *App) handleOAuthDone(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	connStr := q.Get("conn_id")
	pendingStr := q.Get("pending")
	status := q.Get("status")
	if pendingStr == "" {
		http.Error(w, "missing pending", http.StatusBadRequest)
		return
	}
	pendingID, err := strconv.ParseInt(pendingStr, 10, 64)
	if err != nil {
		http.Error(w, "bad pending", http.StatusBadRequest)
		return
	}
	row, err := a.getPending(pendingID)
	if err != nil || row.status != "pending_oauth" || !row.expiresAt.After(time.Now().UTC()) {
		http.Error(w, "pending authorization is invalid or expired", http.StatusGone)
		return
	}
	if status != "ok" {
		_, _ = globalCtx.AppDB().Exec(
			`UPDATE pending_accounts SET status='expired' WHERE id=? AND status='pending_oauth'`,
			pendingID,
		)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!doctype html><html><body style="font-family:system-ui;background:#111;color:#eee;display:grid;place-items:center;height:100vh;margin:0">
<div style="text-align:center"><div style="font-size:20px">Authorization failed</div>
<div style="opacity:.7;margin-top:8px">You can close this window and try again.</div></div>
<script>try { if (window.opener) { window.opener.postMessage({type:"ads.oauth_failed"}, window.location.origin); } } catch(e){}</script>
</body></html>`)
		return
	}

	if connStr == "" {
		http.Error(w, "missing conn_id", http.StatusBadRequest)
		return
	}
	connID, err := strconv.ParseInt(connStr, 10, 64)
	if err != nil {
		http.Error(w, "bad conn_id", http.StatusBadRequest)
		return
	}
	validConnection := false
	conns, err := globalCtx.PlatformAPI().ListConnections(sdk.ConnectionFilter{
		ProjectID: row.projectID,
		AppSlug:   row.integrationSlug,
	})
	if err == nil {
		for _, conn := range conns {
			if conn.ID == connID && conn.Status == "active" {
				validConnection = true
				break
			}
		}
	}
	if !validConnection {
		http.Error(w, "connection does not belong to this authorization", http.StatusForbidden)
		return
	}
	res, err := globalCtx.AppDB().Exec(
		`UPDATE pending_accounts SET connection_id=?, status='ready'
		 WHERE id=? AND project_id=? AND status='pending_oauth' AND expires_at > CURRENT_TIMESTAMP`,
		connID, pendingID, row.projectID,
	)
	if err != nil {
		http.Error(w, "could not complete authorization", http.StatusInternalServerError)
		return
	}
	changed, _ := res.RowsAffected()
	if changed != 1 {
		http.Error(w, "authorization was already used or expired", http.StatusConflict)
		return
	}
	globalCtx.EmitWithProject("account.oauth_ready", row.projectID, map[string]any{
		"pending_account_id": pendingID,
		"connection_id":      connID,
	})
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!doctype html><html><body style="font-family:system-ui;background:#111;color:#eee;display:grid;place-items:center;height:100vh;margin:0">
<div style="text-align:center"><div style="font-size:20px">Authorization complete</div>
<div style="opacity:.7;margin-top:8px">You can close this window.</div></div>
<script>
try { if (window.opener) { window.opener.postMessage({type:"ads.oauth_ready",pending_account_id:%d,connection_id:%d}, window.location.origin); window.close(); } } catch(e){}
setTimeout(function(){ window.location.href = "/apps/ads/page?project_id=%s&pending=%d" }, 1500);
</script></body></html>`, pendingID, connID, url.QueryEscape(row.projectID), pendingID)
}

// ─── MCP tools ──────────────────────────────────────────────────────

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		// ── Accounts ──
		{
			Name: "account_add",
			Description: "Begin connecting an ads account. Returns authorize_url + pending_account_id; visit the URL to authorize. " +
				"After OAuth completes, call account_list_pending_pages to pick a specific ad account, then account_finalize. " +
				"Args: platform (meta|google|x|reddit), connection_id? (reuse a specific active integration connection), force_new? (default false; force a fresh OAuth dance even when an existing connection is available).",
			InputSchema: schemaObject(map[string]any{
				"platform":      map[string]any{"type": "string", "enum": platformKeys()},
				"connection_id": map[string]any{"type": "integer", "description": "Active provider connection to reuse. Use when more than one connection exists for the platform."},
				"force_new":     map[string]any{"type": "boolean"},
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
			Name:        "account_context_get",
			Description: "Discover normalized resources and configured defaults for one ad account. Refreshes provider data by default. Args: ad_account_id, refresh? (default true).",
			InputSchema: schemaObject(map[string]any{
				"ad_account_id": map[string]any{"type": "integer"},
				"refresh":       map[string]any{"type": "boolean", "default": true},
			}, []string{"ad_account_id"}),
			Handler: a.toolAccountContextGet,
		},
		{
			Name:        "resource_refresh",
			Description: "Refresh normalized provider resources for an ad account. Args: ad_account_id, kinds? (identity|tracking_source|conversion_action|lead_form|audience|funding_source).",
			InputSchema: schemaObject(map[string]any{
				"ad_account_id": map[string]any{"type": "integer"},
				"kinds": map[string]any{
					"type": "array",
					"items": map[string]any{"type": "string", "enum": []string{
						resourceIdentity, resourceTrackingSource, resourceConversionAction, resourceLeadForm, resourceAudience, resourceFundingSource,
					}},
				},
			}, []string{"ad_account_id"}),
			Handler: a.toolResourceRefresh,
		},
		{
			Name:        "resource_list",
			Description: "List normalized resources belonging to an ad account. Args: ad_account_id, kind?, refresh?.",
			InputSchema: schemaObject(map[string]any{
				"ad_account_id": map[string]any{"type": "integer"},
				"kind": map[string]any{"type": "string", "enum": []string{
					resourceIdentity, resourceTrackingSource, resourceConversionAction, resourceLeadForm, resourceAudience, resourceCreativeAsset, resourceFundingSource,
				}},
				"refresh": map[string]any{"type": "boolean"},
			}, []string{"ad_account_id"}),
			Handler: a.toolResourceList,
		},
		{
			Name:        "resource_get",
			Description: "Get one normalized resource after validating project and ad-account ownership. Args: ad_account_id, resource_id.",
			InputSchema: schemaObject(map[string]any{
				"ad_account_id": map[string]any{"type": "integer"},
				"resource_id":   map[string]any{"type": "integer"},
			}, []string{"ad_account_id", "resource_id"}),
			Handler: a.toolResourceGet,
		},
		{
			Name:        "resource_set_default",
			Description: "Set or clear a normalized resource default for an ad account. Purposes: publishing_identity, instagram_identity, conversion_source, lead_form, audience, funding_source. Pass resource_id=0 to clear.",
			InputSchema: schemaObject(map[string]any{
				"ad_account_id": map[string]any{"type": "integer"},
				"purpose": map[string]any{"type": "string", "enum": []string{
					"publishing_identity", "instagram_identity", "conversion_source", "lead_form", "audience", "funding_source",
				}},
				"resource_id": map[string]any{"type": "integer"},
			}, []string{"ad_account_id", "purpose", "resource_id"}),
			Handler: a.toolResourceSetDefault,
		},
		{
			Name:        "lead_form_create",
			Description: "Create a normalized lead form. Meta resolves a Facebook Page automatically; Google creates a reusable lead-form asset and can attach it to campaign_id.",
			InputSchema: leadFormCreateSchema(),
			Handler:     a.toolLeadFormCreate,
		},
		{
			Name:        "lead_form_list",
			Description: "List normalized lead forms for an ad account and optionally refresh them from the provider.",
			InputSchema: leadFormListSchema(),
			Handler:     a.toolLeadFormList,
		},
		{
			Name:        "lead_form_get",
			Description: "Get a normalized lead form by project-scoped resource id.",
			InputSchema: leadFormGetSchema(),
			Handler:     a.toolLeadFormGet,
		},
		{
			Name:        "lead_form_update",
			Description: "Update provider-supported fields on a normalized lead form.",
			InputSchema: leadFormUpdateSchema(),
			Handler:     a.toolLeadFormUpdate,
		},
		{
			Name:        "lead_form_archive",
			Description: "Archive a Meta lead form or remove a Google lead-form asset and clear its local default.",
			InputSchema: leadFormArchiveSchema(),
			Handler:     a.toolLeadFormArchive,
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
				"funding_source_resource_id": map[string]any{
					"type": "integer", "description": "Normalized funding source. Used automatically when only one is available.",
				},
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
			Name: "campaign_performance_get",
			Description: "Get normalized daily campaign performance from the provider. " +
				"Returns actual media spend in integer micros plus impressions, clicks, and conversions. " +
				"Queries are restricted to the selected project-owned ad account and a maximum 90-day range. " +
				"Args: ad_account_id, date_from (YYYY-MM-DD), date_to (YYYY-MM-DD), granularity? (day), campaign_ids? (up to 100 numeric provider campaign ids).",
			InputSchema: schemaObject(map[string]any{
				"ad_account_id": map[string]any{"type": "integer"},
				"date_from":     map[string]any{"type": "string", "pattern": `^\d{4}-\d{2}-\d{2}$`},
				"date_to":       map[string]any{"type": "string", "pattern": `^\d{4}-\d{2}-\d{2}$`},
				"granularity":   map[string]any{"type": "string", "enum": []string{"day"}, "default": "day"},
				"campaign_ids": map[string]any{
					"type":     "array",
					"maxItems": 100,
					"items":    map[string]any{"type": "string", "pattern": `^[A-Za-z0-9_~-]+$`},
				},
			}, []string{"ad_account_id", "date_from", "date_to"}),
			Handler: a.toolCampaignPerformanceGet,
		},
		{
			Name: "performance_get",
			Description: "Get normalized advertising performance for one project-owned account. " +
				"Supports account, campaign, ad_group, and ad reporting with integer micros for money, " +
				"derived CTR/CPC/CPM/CPA/ROAS, local caching, and bounded live refreshes. " +
				"Args: ad_account_id, level?, date_from, date_to, granularity? (day), entity_ids?, refresh? (default true).",
			InputSchema: schemaObject(map[string]any{
				"ad_account_id": map[string]any{"type": "integer"},
				"level":         map[string]any{"type": "string", "enum": []string{"account", "campaign", "ad_group", "ad"}, "default": "campaign"},
				"date_from":     map[string]any{"type": "string", "pattern": `^\d{4}-\d{2}-\d{2}$`},
				"date_to":       map[string]any{"type": "string", "pattern": `^\d{4}-\d{2}-\d{2}$`},
				"granularity":   map[string]any{"type": "string", "enum": []string{"day"}, "default": "day"},
				"entity_ids": map[string]any{
					"type":     "array",
					"maxItems": 100,
					"items":    map[string]any{"type": "string", "pattern": `^[A-Za-z0-9_~-]+$`},
				},
				"refresh": map[string]any{"type": "boolean", "default": true},
			}, []string{"ad_account_id", "date_from", "date_to"}),
			Handler: a.toolPerformanceGet,
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
			Name:        "delivery_activate",
			Description: "Safely activate one complete delivery hierarchy. Validates that the ad belongs to the ad set/ad group and campaign, then activates ad, ad set/ad group, and campaign in that order. The operation is resumable and returns each provider state. Args: ad_account_id, campaign_id, adset_id, ad_id.",
			InputSchema: schemaObject(map[string]any{
				"ad_account_id": map[string]any{"type": "integer"},
				"campaign_id":   map[string]any{"type": "string"},
				"adset_id":      map[string]any{"type": "string"},
				"ad_id":         map[string]any{"type": "string"},
			}, []string{"ad_account_id", "campaign_id", "adset_id", "ad_id"}),
			Handler: a.toolDeliveryActivate,
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
				"tracking_source_resource_id": map[string]any{
					"type":        "integer",
					"description": "Normalized conversion tracking resource, such as a Meta Pixel.",
				},
				"conversion_event": map[string]any{
					"type":        "string",
					"description": "Provider-neutral conversion event name used with tracking_source_resource_id.",
				},
				"conversion_location": map[string]any{"type": "string", "enum": []string{"website", "instant_form", "calls", "messages"}},
				"destination_type":    map[string]any{"type": "string"},
				"dsa_beneficiary":     map[string]any{"type": "string"},
				"dsa_payor":           map[string]any{"type": "string"},
				"platform_options":    map[string]any{"type": "object"},
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
				"ad_account_id":         map[string]any{"type": "integer"},
				"adset_id":              map[string]any{"type": "string"},
				"name":                  map[string]any{"type": "string"},
				"status":                map[string]any{"type": "string", "enum": []string{"PAUSED", "ACTIVE"}},
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
			Description: "Update an ad. Args: ad_account_id, ad_id, adset_id? (required by Google unless ad_id is a full resource name), plus any updatable field.",
			InputSchema: schemaObject(map[string]any{
				"ad_account_id":    map[string]any{"type": "integer"},
				"adset_id":         map[string]any{"type": "string"},
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
			Name: "creative_create",
			Description: "Create a creative from one provider-neutral specification. " +
				"Formats: link, image, video, carousel. identity_id is the publishing identity (a Facebook Page ID on Meta). " +
				"Use image_url/image_hash for images, video_id plus thumbnail_url/thumbnail_hash for video, and cards for carousel. " +
				"Args: ad_account_id, format, name, identity_id?, secondary_identity_id?, headline?, primary_text?, description?, destination_url?, call_to_action?, image_url?, image_hash?, video_id?, thumbnail_url?, thumbnail_hash?, cards?, url_tags?, platform_options.",
			InputSchema: schemaObject(map[string]any{
				"ad_account_id": map[string]any{"type": "integer"},
				"format":        map[string]any{"type": "string", "enum": []string{"link", "image", "video", "carousel"}},
				"name":          map[string]any{"type": "string"},
				"identity_id":   map[string]any{"type": "string"},
				"identity_resource_id": map[string]any{
					"type":        "integer",
					"description": "Normalized publishing identity resource. Preferred over a native identity_id.",
				},
				"secondary_identity_id": map[string]any{
					"type":        "string",
					"description": "Optional secondary publishing identity; Meta maps this to the Instagram user ID.",
				},
				"secondary_identity_resource_id": map[string]any{
					"type":        "integer",
					"description": "Optional normalized secondary identity resource.",
				},
				"lead_form_resource_id": map[string]any{
					"type":        "integer",
					"description": "Normalized Meta lead form. The account default is used for conversion_location=instant_form when omitted.",
				},
				"conversion_location": map[string]any{"type": "string", "enum": []string{"website", "instant_form"}},
				"headline":            map[string]any{"type": "string"},
				"primary_text":        map[string]any{"type": "string"},
				"description":         map[string]any{"type": "string"},
				"destination_url":     map[string]any{"type": "string"},
				"call_to_action": map[string]any{"type": "string", "enum": []string{
					"learn_more", "shop_now", "sign_up", "book_travel", "contact_us",
					"download", "get_offer", "get_quote", "subscribe", "watch_more",
				}},
				"image_url":     map[string]any{"type": "string"},
				"image_hash":    map[string]any{"type": "string"},
				"video_id":      map[string]any{"type": "string"},
				"thumbnail_url": map[string]any{"type": "string"},
				"thumbnail_hash": map[string]any{
					"type":        "string",
					"description": "Provider-side image hash used as a video thumbnail.",
				},
				"cards": map[string]any{
					"type":     "array",
					"minItems": 2,
					"maxItems": 10,
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"headline":        map[string]any{"type": "string"},
							"description":     map[string]any{"type": "string"},
							"destination_url": map[string]any{"type": "string"},
							"image_url":       map[string]any{"type": "string"},
							"image_hash":      map[string]any{"type": "string"},
							"call_to_action": map[string]any{"type": "string", "enum": []string{
								"learn_more", "shop_now", "sign_up", "book_travel", "contact_us",
								"download", "get_offer", "get_quote", "subscribe", "watch_more",
							}},
						},
					},
				},
				"url_tags":         map[string]any{"type": "string"},
				"platform_options": map[string]any{"type": "object"},
			}, []string{"ad_account_id", "format", "name"}),
			Handler: a.toolCreativeCreate,
		},
		{
			Name:        "creative_get",
			Description: "Get a creative that belongs to the selected ad account. Args: ad_account_id, creative_id.",
			InputSchema: schemaObject(map[string]any{
				"ad_account_id": map[string]any{"type": "integer"},
				"creative_id":   map[string]any{"type": "string"},
			}, []string{"ad_account_id", "creative_id"}),
			Handler: a.toolCreativeGet,
		},
		{
			Name:        "creative_delete",
			Description: "Delete a creative that belongs to the selected ad account. Remove ads using it first. Args: ad_account_id, creative_id.",
			InputSchema: schemaObject(map[string]any{
				"ad_account_id": map[string]any{"type": "integer"},
				"creative_id":   map[string]any{"type": "string"},
			}, []string{"ad_account_id", "creative_id"}),
			Handler: a.toolCreativeDelete,
		},
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
			Name:        "creative_asset_status",
			Description: "Get processing/readiness for an uploaded creative asset. Meta supports video IDs; Google accepts an asset id or full resource name. Args: ad_account_id, asset_id, kind?.",
			InputSchema: schemaObject(map[string]any{
				"ad_account_id": map[string]any{"type": "integer"},
				"asset_id":      map[string]any{"type": "string"},
				"kind":          map[string]any{"type": "string", "enum": []string{"image", "video"}},
			}, []string{"ad_account_id", "asset_id"}),
			Handler: a.toolCreativeAssetStatus,
		},
		{
			Name:        "creative_asset_delete",
			Description: "Delete an uploaded creative asset when the provider supports it. Meta currently supports ad-account video assets; remove dependent ads and creatives first. Args: ad_account_id, asset_id, kind.",
			InputSchema: schemaObject(map[string]any{
				"ad_account_id": map[string]any{"type": "integer"},
				"asset_id":      map[string]any{"type": "string"},
				"kind":          map[string]any{"type": "string", "enum": []string{"image", "video"}},
			}, []string{"ad_account_id", "asset_id", "kind"}),
			Handler: a.toolCreativeAssetDelete,
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
	pid, err := requireProject(ctx, args)
	if err != nil {
		return mcpError(err.Error()), nil
	}
	forceNew, _ := args["force_new"].(bool)
	conns, err := ctx.PlatformAPI().ListConnections(sdk.ConnectionFilter{
		ProjectID: pid,
		AppSlug:   def.IntegrationSlug,
	})
	if err != nil {
		return mcpError("could not check " + def.DisplayName + " integration setup: " + err.Error()), nil
	}
	if len(conns) == 0 {
		return integrationSetupRequired(&def), nil
	}
	requestedConnectionID := int64(intArg(args, "connection_id", 0))
	if requestedConnectionID > 0 && forceNew {
		return mcpError("connection_id cannot be combined with force_new"), nil
	}
	activeConnections := make(map[int64]sdk.PlatformConnection)
	for _, conn := range conns {
		if conn.Status == "active" {
			activeConnections[conn.ID] = conn
		}
	}
	if requestedConnectionID > 0 {
		if _, ok := activeConnections[requestedConnectionID]; !ok {
			return mcpError("connection_id is not an active " + def.DisplayName + " connection in this project"), nil
		}
	}

	// Reuse path (mirrors social): the access token from any active
	// provider connection in this project covers its accessible ad
	// accounts, so a fresh OAuth dance just produces a duplicate.
	if !forceNew {
		existingConnID := requestedConnectionID
		if existingConnID == 0 {
			_ = ctx.AppDB().QueryRow(
				`SELECT connection_id FROM ad_accounts
				 WHERE project_id=? AND platform=? AND status='active'
				 ORDER BY id DESC LIMIT 1`,
				pid, def.Platform,
			).Scan(&existingConnID)
			if _, ok := activeConnections[existingConnID]; !ok {
				existingConnID = 0
			}
		}
		if existingConnID == 0 {
			for _, c := range conns {
				if c.Status == "active" {
					existingConnID = c.ID
					break
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
		returnTo = "/api/apps/ads/accounts/oauth_done?project_id=" + url.QueryEscape(pid)
	} else if !strings.HasPrefix(returnTo, "/") || strings.HasPrefix(returnTo, "//") {
		return mcpError("return_to must be a same-origin absolute path"), nil
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

func integrationSetupRequired(def *platformDef) map[string]any {
	out := mcpError(
		def.DisplayName + " is not configured. Create the " + def.IntegrationSlug +
			" integration first, then return to Ads and add the account.",
	)
	out["code"] = "integration_setup_required"
	out["integration_slug"] = def.IntegrationSlug
	out["setup_url"] = "/integrations?app=" + url.QueryEscape(def.IntegrationSlug)
	return out
}

func (a *App) toolAccountListPendingPages(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pendingID := int64(intArg(args, "pending_account_id", 0))
	if pendingID <= 0 {
		return nil, errors.New("pending_account_id required")
	}
	row, err := a.getPendingForProject(ctx, args, pendingID, "ready")
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

	adapter, ok := platformAdapters[row.platform]
	if !ok {
		return mcpError("no adapter wired for " + row.platform), nil
	}
	accounts, err := adapter.ListAccounts(a, ctx, row, &def)
	if err != nil {
		return mcpError("list accounts failed: " + err.Error()), nil
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
	row, err := a.getPendingForProject(ctx, args, pendingID, "ready")
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

	adapter, ok := platformAdapters[row.platform]
	if !ok {
		return mcpError("no adapter wired for " + row.platform), nil
	}
	rows, err := adapter.ListAccounts(a, ctx, row, &def)
	if err != nil {
		return mcpError("verify ad-account: " + err.Error()), nil
	}
	var matched map[string]any
	for _, r := range rows {
		if toString(r["id"]) == pageID {
			matched = r
			break
		}
	}
	if matched == nil {
		return mcpError("page_id not in the user's accessible ad accounts — re-call account_list_pending_pages"), nil
	}
	if displayName == "" {
		displayName = toString(matched["name"])
	}
	if displayName == "" {
		displayName = pageID
	}
	currency = toString(matched["currency"])
	timezone = toString(matched["timezone"])

	pid := row.projectID
	var existingID int64
	reactivated := ctx.AppDB().QueryRow(
		`SELECT id FROM ad_accounts WHERE project_id=? AND platform=? AND native_account_id=?`,
		pid, def.Platform, pageID,
	).Scan(&existingID) == nil

	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	claim, err := tx.Exec(
		`UPDATE pending_accounts SET status='finalized'
		 WHERE id=? AND project_id=? AND status='ready' AND expires_at > CURRENT_TIMESTAMP`,
		pendingID, pid,
	)
	if err != nil {
		return nil, err
	}
	claimed, _ := claim.RowsAffected()
	if claimed != 1 {
		return mcpError("pending account was already finalized or expired"), nil
	}
	_, err = tx.Exec(
		`INSERT INTO ad_accounts (project_id, platform, connection_id, native_account_id, display_name, currency, timezone_name, status)
		 VALUES (?, ?, ?, ?, ?, ?, ?, 'active')
		 ON CONFLICT(project_id, platform, native_account_id) DO UPDATE SET
		   connection_id=excluded.connection_id,
		   display_name=excluded.display_name,
		   currency=excluded.currency,
		   timezone_name=excluded.timezone_name,
		   status='active'`,
		pid, def.Platform, row.connectionID, pageID, displayName, nullable(currency), nullable(timezone),
	)
	if err != nil {
		return nil, fmt.Errorf("save ad_account: %w", err)
	}
	var id int64
	if err := tx.QueryRow(
		`SELECT id FROM ad_accounts WHERE project_id=? AND platform=? AND native_account_id=?`,
		pid, def.Platform, pageID,
	).Scan(&id); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	ctx.EmitWithProject("account.added", pid, map[string]any{
		"ad_account_id":     id,
		"platform":          def.Platform,
		"native_account_id": pageID,
	})
	ctx.EmitWithProject("account.changed", pid, map[string]any{
		"ad_account_id": id, "platform": def.Platform, "action": "added",
	})

	return map[string]any{
		"ad_account_id":     id,
		"platform":          def.Platform,
		"display_name":      displayName,
		"native_account_id": pageID,
		"currency":          currency,
		"timezone":          timezone,
		"reactivated":       reactivated,
	}, nil
}

func (a *App) toolAccountList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return mcpError(err.Error()), nil
	}
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
			id, connID                                                  int64
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
	pid, err := requireProject(ctx, args)
	if err != nil {
		return mcpError(err.Error()), nil
	}
	var connID int64
	if err := ctx.AppDB().QueryRow(
		`SELECT connection_id FROM ad_accounts WHERE id=? AND project_id=?`,
		id, pid,
	).Scan(&connID); err != nil {
		return mcpError("account not found"), nil
	}
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM ad_resource_defaults WHERE ad_account_id=? AND project_id=?`, id, pid); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`DELETE FROM ad_resources WHERE ad_account_id=? AND project_id=?`, id, pid); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`DELETE FROM ad_metric_points WHERE ad_account_id=? AND project_id=?`, id, pid); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`DELETE FROM ad_entities WHERE ad_account_id=? AND project_id=?`, id, pid); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`DELETE FROM ad_sync_state WHERE ad_account_id=? AND project_id=?`, id, pid); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`DELETE FROM ad_accounts WHERE id=? AND project_id=?`, id, pid); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	ctx.EmitWithProject("account.removed", pid, map[string]any{"ad_account_id": id, "connection_id": connID})
	ctx.EmitWithProject("account.changed", pid, map[string]any{"ad_account_id": id, "action": "removed"})
	return map[string]any{"deleted": id}, nil
}

// ─── HTTP handlers (panel) ────────────────────────────────────────────

func projectArgsFromRequest(r *http.Request) map[string]any {
	args := map[string]any{}
	q := r.URL.Query()
	if v := strings.TrimSpace(q.Get("project_id")); v != "" {
		args["project_id"] = v
	}
	if v := strings.TrimSpace(q.Get("_project_id")); v != "" {
		args["_project_id"] = v
	}
	return args
}

func (a *App) handleAccountsAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	args := projectArgsFromRequest(r)
	if v := strings.TrimSpace(r.URL.Query().Get("platform")); v != "" {
		args["platform"] = v
	}
	if v := strings.TrimSpace(r.URL.Query().Get("status")); v != "" {
		args["status"] = v
	}
	out, err := a.toolAccountList(globalCtx, args)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, out)
}

func (a *App) handleAccountsStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Platform string `json:"platform"`
		ReturnTo string `json:"return_to"`
		ForceNew bool   `json:"force_new"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	args := projectArgsFromRequest(r)
	args["platform"] = body.Platform
	args["return_to"] = body.ReturnTo
	args["force_new"] = body.ForceNew
	out, err := a.toolAccountAdd(globalCtx, args)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, out)
}

func (a *App) handleAccountsFinalize(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		PendingAccountID int64  `json:"pending_account_id"`
		PageID           string `json:"page_id"`
		Name             string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	args := projectArgsFromRequest(r)
	args["pending_account_id"] = body.PendingAccountID
	args["page_id"] = body.PageID
	args["name"] = body.Name
	out, err := a.toolAccountFinalize(globalCtx, args)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, out)
}

func (a *App) handleAccountsItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/accounts/")
	if rest == "" {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	parts := strings.Split(rest, "/")
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	if len(parts) == 2 && parts[1] == "pages" && r.Method == http.MethodGet {
		args := projectArgsFromRequest(r)
		args["pending_account_id"] = id
		out, err := a.toolAccountListPendingPages(globalCtx, args)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, out)
		return
	}
	if len(parts) == 1 && r.Method == http.MethodDelete {
		args := projectArgsFromRequest(r)
		args["id"] = id
		out, err := a.toolAccountDisconnect(globalCtx, args)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, out)
		return
	}
	http.NotFound(w, r)
}

type adsPlatformOption struct {
	Platform        string
	DisplayName     string
	IntegrationSlug string
	Supported       bool
	Unavailable     string
}

var adsPlatformOptions = []adsPlatformOption{
	{Platform: "meta", DisplayName: "Meta Ads (Facebook + Instagram)", IntegrationSlug: "facebook-ads", Supported: true},
	{Platform: "google", DisplayName: "Google Ads", IntegrationSlug: "google-ads", Supported: true},
	{Platform: "x", DisplayName: "X Ads", IntegrationSlug: "twitter-ads", Supported: true},
	{Platform: "reddit", DisplayName: "Reddit Ads", IntegrationSlug: "reddit-ads", Supported: true},
}

func (a *App) handlePlatforms(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	pid, err := requireProject(globalCtx, projectArgsFromRequest(r))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	out := make([]map[string]any, 0, len(adsPlatformOptions))
	for _, opt := range adsPlatformOptions {
		connectionCount := 0
		activeConnectionID := int64(0)
		activeConnections := make([]map[string]any, 0)
		conns, listErr := globalCtx.PlatformAPI().ListConnections(sdk.ConnectionFilter{
			ProjectID: pid,
			AppSlug:   opt.IntegrationSlug,
		})
		configured := listErr == nil && len(conns) > 0
		if listErr == nil {
			for _, c := range conns {
				if c.Status != "active" {
					continue
				}
				connectionCount++
				if activeConnectionID == 0 {
					activeConnectionID = c.ID
				}
				activeConnections = append(activeConnections, map[string]any{
					"id":   c.ID,
					"name": c.Name,
				})
			}
		}

		activeAccount := false
		if opt.Supported {
			var n int
			_ = globalCtx.AppDB().QueryRow(
				`SELECT COUNT(*) FROM ad_accounts WHERE project_id=? AND platform=? AND status='active'`,
				pid, opt.Platform,
			).Scan(&n)
			activeAccount = n > 0
		}
		connected := activeAccount || connectionCount > 0
		canAdd := opt.Supported && configured && listErr == nil
		state := "setup_required"
		if listErr != nil {
			state = "unavailable"
		} else if !opt.Supported {
			state = "unsupported"
		} else if connected {
			state = "connected"
		} else if configured {
			state = "ready"
		}
		unavailable := opt.Unavailable
		if listErr != nil {
			unavailable = "Could not check integration setup. Refresh and try again."
		} else if opt.Supported && !configured {
			unavailable = "Create the " + opt.IntegrationSlug + " integration first."
		} else if !opt.Supported && unavailable == "" {
			unavailable = opt.DisplayName + " is not supported by this Ads version."
		}
		out = append(out, map[string]any{
			"platform":             opt.Platform,
			"display_name":         opt.DisplayName,
			"integration_slug":     opt.IntegrationSlug,
			"supported":            opt.Supported,
			"configured":           configured,
			"available":            connected,
			"state":                state,
			"can_add":              canAdd,
			"setup_url":            "/integrations?app=" + url.QueryEscape(opt.IntegrationSlug),
			"requires_picker":      opt.Supported,
			"connection_count":     connectionCount,
			"connections":          activeConnections,
			"active_connection_id": activeConnectionID,
			"active_account":       activeAccount,
			"unavailable_reason":   unavailable,
		})
	}
	writeJSON(w, map[string]any{"platforms": out})
}

// ─── Resolution helpers ────────────────────────────────────────────

type adAccount struct {
	ID              int64
	ProjectID       string
	Platform        string
	ConnectionID    int64
	NativeAccountID string
	Currency        string
	Timezone        string
}

func (a *App) resolveAdAccount(ctx *sdk.AppCtx, args map[string]any) (*adAccount, *platformDef, map[string]any) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, nil, mcpError(err.Error())
	}
	id := int64(intArg(args, "ad_account_id", 0))
	if id <= 0 {
		return nil, nil, mcpError("ad_account_id required")
	}
	var acct adAccount
	if err := ctx.AppDB().QueryRow(
		`SELECT id, platform, connection_id, native_account_id,
		        COALESCE(currency,''), COALESCE(timezone_name,'')
		 FROM ad_accounts WHERE id=? AND project_id=? AND status='active'`,
		id, pid,
	).Scan(
		&acct.ID,
		&acct.Platform,
		&acct.ConnectionID,
		&acct.NativeAccountID,
		&acct.Currency,
		&acct.Timezone,
	); err != nil {
		return nil, nil, mcpError("ad_account not found or not active")
	}
	acct.ProjectID = pid
	def, ok := platforms[acct.Platform]
	if !ok {
		return nil, nil, mcpError("unsupported platform " + acct.Platform)
	}
	if _, ok := platformAdapters[acct.Platform]; !ok {
		return nil, nil, mcpError("no adapter wired for " + acct.Platform)
	}
	return &acct, &def, nil
}

func (a *App) execIntegrationTool(ctx *sdk.AppCtx, acct *adAccount, tool string, input map[string]any) (any, map[string]any) {
	return a.execIntegrationToolWithPolicy(ctx, acct, tool, input, integrationExecPolicy{})
}

func providerRateLimited(res *sdk.ExecuteResult, body string) bool {
	if res != nil && res.Status == http.StatusTooManyRequests {
		return true
	}
	upper := strings.ToUpper(body)
	for _, marker := range []string{
		"RESOURCE_EXHAUSTED",
		"RATE LIMIT",
		"TOO MANY CALLS",
		"USER REQUEST LIMIT REACHED",
		"APPLICATION REQUEST LIMIT REACHED",
	} {
		if strings.Contains(upper, marker) {
			return true
		}
	}
	return false
}

// mergeOptions overlays native fields while preserving account and
// resource identifiers resolved by the app's project-scoped boundary.
func mergeOptions(base map[string]any, args map[string]any) map[string]any {
	opts, _ := args["platform_options"].(map[string]any)
	protected := map[string]bool{
		"adAccountId": true, "customer_id": true,
		"objectId":   true,
		"campaignId": true, "campaign_id": true,
		"adSetId": true, "adsetId": true, "adset_id": true,
		"adId": true, "ad_id": true,
		"creativeId": true, "creative_id": true,
		"videoId": true, "asset_id": true,
		"creative":     true,
		"resourceName": true,
	}
	for k, v := range opts {
		if protected[k] {
			continue
		}
		base[k] = v
	}
	return base
}

func (a *App) metaResourceBelongsToAccount(ctx *sdk.AppCtx, acct *adAccount, tool, resourceID string) (bool, error) {
	after := ""
	for page := 0; page < 20; page++ {
		input := map[string]any{"fields": "id", "limit": 100}
		if tool == "adset_list" || tool == "ad_list" {
			input["objectId"] = acct.NativeAccountID
		} else {
			input["adAccountId"] = acct.NativeAccountID
		}
		if after != "" {
			input["after"] = after
		}
		res, err := ctx.PlatformAPI().ExecuteIntegrationTool(acct.ConnectionID, tool, input)
		if err != nil {
			return false, err
		}
		if res == nil || !res.Success {
			return false, fmt.Errorf("%s ownership lookup failed", tool)
		}
		var payload map[string]any
		if err := json.Unmarshal(res.Data, &payload); err != nil {
			return false, err
		}
		if rows, ok := payload["data"].([]any); ok {
			for _, raw := range rows {
				if row, ok := raw.(map[string]any); ok && toString(row["id"]) == resourceID {
					return true, nil
				}
			}
		}
		paging, _ := payload["paging"].(map[string]any)
		cursors, _ := paging["cursors"].(map[string]any)
		next := toString(cursors["after"])
		if next == "" || next == after {
			return false, nil
		}
		after = next
	}
	return false, errors.New("resource ownership lookup exceeded pagination limit")
}

func (a *App) requireMetaResource(ctx *sdk.AppCtx, acct *adAccount, tool, resourceType, resourceID string) map[string]any {
	ok, err := a.metaResourceBelongsToAccount(ctx, acct, tool, resourceID)
	if err != nil {
		return mcpError("verify " + resourceType + " ownership: " + err.Error())
	}
	if !ok {
		return mcpError(resourceType + " does not belong to the selected ad account")
	}
	return nil
}

func (a *App) metaVideoBelongsToAccount(ctx *sdk.AppCtx, acct *adAccount, tool, videoID string) (bool, error) {
	after := ""
	for page := 0; page < 20; page++ {
		input := map[string]any{
			"adAccountId": acct.NativeAccountID,
			"fields":      "id,object_story_spec",
			"limit":       100,
		}
		if after != "" {
			input["after"] = after
		}
		res, err := ctx.PlatformAPI().ExecuteIntegrationTool(acct.ConnectionID, tool, input)
		if err != nil {
			return false, err
		}
		if res == nil || !res.Success {
			return false, fmt.Errorf("%s video ownership lookup failed", tool)
		}
		var payload map[string]any
		if err := json.Unmarshal(res.Data, &payload); err != nil {
			return false, err
		}
		if rows, ok := payload["data"].([]any); ok {
			for _, raw := range rows {
				row, _ := raw.(map[string]any)
				story, _ := row["object_story_spec"].(map[string]any)
				video, _ := story["video_data"].(map[string]any)
				if toString(video["video_id"]) == videoID {
					return true, nil
				}
			}
		}
		paging, _ := payload["paging"].(map[string]any)
		cursors, _ := paging["cursors"].(map[string]any)
		next := toString(cursors["after"])
		if next == "" || next == after {
			return false, nil
		}
		after = next
	}
	return false, errors.New("video ownership lookup exceeded pagination limit")
}

// ─── Campaign tools ────────────────────────────────────────────────

// metaCampaignObjective maps our unified objective enum to Meta's
// OUTCOME_* enum. Future platforms get their own table.
var metaCampaignObjective = map[string]string{
	"sales":         "OUTCOME_SALES",
	"leads":         "OUTCOME_LEADS",
	"traffic":       "OUTCOME_TRAFFIC",
	"engagement":    "OUTCOME_ENGAGEMENT",
	"awareness":     "OUTCOME_AWARENESS",
	"app_promotion": "OUTCOME_APP_PROMOTION",
}

var metaBidStrategy = map[string]string{
	"lowest_cost":              "LOWEST_COST_WITHOUT_CAP",
	"lowest_cost_with_bid_cap": "LOWEST_COST_WITH_BID_CAP",
	"cost_cap":                 "COST_CAP",
	"bid_cap":                  "BID_CAP",
}

const (
	maxPerformanceRangeDays = 90
	maxPerformancePages     = 200
	maxPerformanceCampaigns = 100
)

type performanceRequest struct {
	DateFrom    string
	DateTo      string
	CampaignIDs []string
}

func validatePerformanceRequest(args map[string]any) (*performanceRequest, error) {
	granularity := strings.ToLower(strings.TrimSpace(stringArgAny(args, "granularity")))
	if granularity == "" {
		granularity = "day"
	}
	if granularity != "day" {
		return nil, errors.New("granularity must be day")
	}

	dateFrom := strings.TrimSpace(stringArgAny(args, "date_from"))
	dateTo := strings.TrimSpace(stringArgAny(args, "date_to"))
	from, err := time.Parse("2006-01-02", dateFrom)
	if err != nil || from.Format("2006-01-02") != dateFrom {
		return nil, errors.New("date_from must be a valid YYYY-MM-DD date")
	}
	to, err := time.Parse("2006-01-02", dateTo)
	if err != nil || to.Format("2006-01-02") != dateTo {
		return nil, errors.New("date_to must be a valid YYYY-MM-DD date")
	}
	if to.Before(from) {
		return nil, errors.New("date_to must be on or after date_from")
	}
	days := int(to.Sub(from).Hours()/24) + 1
	if days > maxPerformanceRangeDays {
		return nil, fmt.Errorf("date range exceeds %d days", maxPerformanceRangeDays)
	}

	ids, err := performanceCampaignIDs(args["campaign_ids"])
	if err != nil {
		return nil, err
	}
	return &performanceRequest{
		DateFrom:    dateFrom,
		DateTo:      dateTo,
		CampaignIDs: ids,
	}, nil
}

func performanceCampaignIDs(value any) ([]string, error) {
	var raw []string
	switch ids := value.(type) {
	case nil:
		return nil, nil
	case []string:
		raw = ids
	case []any:
		raw = make([]string, 0, len(ids))
		for _, id := range ids {
			raw = append(raw, strings.TrimSpace(toString(id)))
		}
	default:
		return nil, errors.New("campaign_ids must be an array of provider IDs")
	}
	if len(raw) > maxPerformanceCampaigns {
		return nil, fmt.Errorf("campaign_ids supports at most %d ids", maxPerformanceCampaigns)
	}
	out := make([]string, 0, len(raw))
	seen := make(map[string]bool, len(raw))
	for _, id := range raw {
		id = strings.TrimSpace(id)
		if id == "" || !safeProviderID(id) {
			return nil, errors.New("campaign_ids contain an invalid provider ID")
		}
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out, nil
}

func asciiDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func safeProviderID(value string) bool {
	if value == "" || len(value) > 256 {
		return false
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '_' || char == '-' || char == '~' {
			continue
		}
		return false
	}
	return true
}

func (a *App) toolCampaignCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	acct, def, errOut := a.resolveAdAccount(ctx, args)
	if errOut != nil {
		return errOut, nil
	}
	out, err := platformAdapters[acct.Platform].CampaignCreate(a, ctx, acct, def, args)
	a.emitEntityChanged(ctx, acct, "campaign", "created", args, out, err)
	return out, err
}

func (a *App) toolCampaignList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	acct, def, errOut := a.resolveAdAccount(ctx, args)
	if errOut != nil {
		return errOut, nil
	}
	return platformAdapters[acct.Platform].CampaignList(a, ctx, acct, def, args)
}

func (a *App) toolCampaignPerformanceGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	acct, def, errOut := a.resolveAdAccount(ctx, args)
	if errOut != nil {
		return errOut, nil
	}
	if _, err := validatePerformanceRequest(args); err != nil {
		return mcpError(err.Error()), nil
	}
	return platformAdapters[acct.Platform].CampaignPerformance(a, ctx, acct, def, args)
}

func (a *App) toolCampaignUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	acct, def, errOut := a.resolveAdAccount(ctx, args)
	if errOut != nil {
		return errOut, nil
	}
	out, err := platformAdapters[acct.Platform].CampaignUpdate(a, ctx, acct, def, args)
	a.emitEntityChanged(ctx, acct, "campaign", "updated", args, out, err)
	return out, err
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
	out, err := platformAdapters[acct.Platform].CampaignDelete(a, ctx, acct, def, args)
	a.emitEntityChanged(ctx, acct, "campaign", "deleted", args, out, err)
	return out, err
}

func (a *App) emitEntityChanged(ctx *sdk.AppCtx, acct *adAccount, level, action string, args map[string]any, out any, err error) {
	if err != nil || acct == nil {
		return
	}
	if result, ok := out.(map[string]any); ok && result["isError"] == true {
		return
	}
	idKey := map[string]string{"campaign": "campaign_id", "ad_group": "adset_id", "ad": "ad_id"}[level]
	entityID := stringArgAny(args, idKey)
	if entityID == "" {
		entityID = firstString(asMap(out), "id", "resourceName", "resource_name")
	}
	ctx.EmitWithProject("entity.changed", acct.ProjectID, map[string]any{
		"ad_account_id": acct.ID,
		"platform":      acct.Platform,
		"level":         level,
		"entity_id":     entityID,
		"action":        action,
		"status":        strings.ToUpper(stringArgAny(args, "status")),
	})
}

// ─── Ad set tools ───────────────────────────────────────────────────

var metaOptimizationGoal = map[string]string{
	"link_clicks":        "LINK_CLICKS",
	"conversions":        "OFFSITE_CONVERSIONS",
	"leads":              "LEAD_GENERATION",
	"reach":              "REACH",
	"impressions":        "IMPRESSIONS",
	"page_likes":         "PAGE_LIKES",
	"post_engagement":    "POST_ENGAGEMENT",
	"thruplay":           "THRUPLAY",
	"app_installs":       "APP_INSTALLS",
	"value":              "VALUE",
	"landing_page_views": "LANDING_PAGE_VIEWS",
}

var metaBillingEvent = map[string]string{
	"impressions": "IMPRESSIONS",
	"link_clicks": "LINK_CLICKS",
	"thruplay":    "THRUPLAY",
}

const (
	metaCampaignFields    = "id,name,objective,status,effective_status,bid_strategy,daily_budget,lifetime_budget,budget_remaining,special_ad_categories,start_time,stop_time,created_time,updated_time"
	metaAdSetFields       = "id,name,campaign_id,status,effective_status,daily_budget,lifetime_budget,bid_strategy,bid_amount,optimization_goal,billing_event,targeting,promoted_object,start_time,end_time,budget_remaining,created_time,dsa_beneficiary,dsa_payor"
	metaAdFields          = "id,name,adset_id,campaign_id,status,effective_status,creative{id,name,image_url,thumbnail_url,object_story_spec},tracking_specs,created_time"
	metaCreativeFields    = "id,name,status,object_story_spec,thumbnail_url,url_tags,created_time"
	metaCreativeGetFields = "id,name,status,object_story_spec,thumbnail_url,url_tags,body,title,link_url,image_url"
	metaAudienceFields    = "id,name,subtype,approximate_count_lower_bound,approximate_count_upper_bound,delivery_status,description"
)

type metaAdapter struct{}

func (metaAdapter) ListAccounts(a *App, ctx *sdk.AppCtx, row *pendingRow, def *platformDef) ([]map[string]any, error) {
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(row.connectionID, def.ListAccountsTool, map[string]any{
		"fields": "id,name,account_id,account_status,currency,timezone_name",
		"limit":  100,
	})
	if err != nil {
		return nil, err
	}
	if res == nil || !res.Success {
		body := ""
		if res != nil {
			body = string(res.Data)
		}
		return nil, fmt.Errorf("upstream non-2xx: %s", body)
	}
	var envelope struct {
		Data []map[string]any `json:"data"`
	}
	var rows []map[string]any
	if json.Unmarshal(res.Data, &envelope) == nil && envelope.Data != nil {
		rows = envelope.Data
	} else if err := json.Unmarshal(res.Data, &rows); err != nil {
		return nil, fmt.Errorf("parse account-list response: %w", err)
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
	return accounts, nil
}

func (metaAdapter) CampaignCreate(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error) {
	name, _ := args["name"].(string)
	objective, _ := args["objective"].(string)
	if name == "" || objective == "" {
		return mcpError("name and objective required"), nil
	}
	mapped, ok := metaCampaignObjective[objective]
	if !ok {
		return mcpError("unsupported objective for meta: " + objective), nil
	}
	input := map[string]any{
		def.AccountIDInputField: acct.NativeAccountID,
		"name":                  name,
		"objective":             mapped,
	}
	opts, _ := args["platform_options"].(map[string]any)
	if _, hasSAC := opts["special_ad_categories"]; !hasSAC {
		input["special_ad_categories"] = []any{}
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
	} else if input["daily_budget"] != nil || input["lifetime_budget"] != nil {
		input["bid_strategy"] = "LOWEST_COST_WITHOUT_CAP"
	}
	if v, _ := args["start_time"].(string); v != "" {
		input["start_time"] = v
	}
	if v, _ := args["end_time"].(string); v != "" {
		input["end_time"] = v
	}
	if _, hasDaily := input["daily_budget"]; !hasDaily {
		if _, hasLifetime := input["lifetime_budget"]; !hasLifetime {
			if _, configured := opts["is_adset_budget_sharing_enabled"]; !configured {
				input["is_adset_budget_sharing_enabled"] = false
			}
		}
	}
	mergeOptions(input, args)
	return a.execOrErr(ctx, acct, def.CampaignCreateTool, input)
}

func (metaAdapter) CampaignList(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error) {
	input := map[string]any{
		def.AccountIDInputField: acct.NativeAccountID,
		"fields":                metaCampaignFields,
	}
	if v := intArg(args, "limit", 0); v > 0 {
		input["limit"] = v
	}
	if v, _ := args["after"].(string); v != "" {
		input["after"] = v
	}
	if v, _ := args["status"].(string); v != "" {
		input["filtering"] = fmt.Sprintf(`[{"field":"effective_status","operator":"IN","value":["%s"]}]`, v)
	}
	return a.execOrErr(ctx, acct, def.CampaignListTool, input)
}

func (metaAdapter) CampaignPerformance(a *App, ctx *sdk.AppCtx, acct *adAccount, _ *platformDef, args map[string]any) (any, error) {
	request, err := validatePerformanceRequest(args)
	if err != nil {
		return mcpError(err.Error()), nil
	}
	timeRange, _ := json.Marshal(map[string]string{
		"since": request.DateFrom,
		"until": request.DateTo,
	})
	input := map[string]any{
		"objectId":       acct.NativeAccountID,
		"level":          "campaign",
		"fields":         "campaign_id,campaign_name,date_start,date_stop,spend,impressions,clicks,actions",
		"time_range":     string(timeRange),
		"time_increment": "1",
		"limit":          500,
	}
	if len(request.CampaignIDs) > 0 {
		filtering, _ := json.Marshal([]map[string]any{{
			"field":    "campaign.id",
			"operator": "IN",
			"value":    request.CampaignIDs,
		}})
		input["filtering"] = string(filtering)
	}

	data := make([]map[string]any, 0)
	after := ""
	for page := 0; page < maxPerformancePages; page++ {
		if after != "" {
			input["after"] = after
		} else {
			delete(input, "after")
		}
		parsed, errOut := a.execIntegrationTool(ctx, acct, "insights_get", input)
		if errOut != nil {
			return errOut, nil
		}
		for _, row := range resultRows(parsed) {
			item, err := normalizeMetaPerformance(acct, row)
			if err != nil {
				return mcpError("normalize Meta performance: " + err.Error()), nil
			}
			data = append(data, item)
		}
		next := metaNextCursor(parsed)
		if next == "" {
			return performanceResponse(data), nil
		}
		if next == after {
			return mcpError("Meta insights pagination returned a repeated cursor"), nil
		}
		after = next
	}
	return mcpError("Meta insights pagination exceeded the safety limit"), nil
}

func (metaAdapter) CampaignUpdate(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error) {
	cid, _ := args["campaign_id"].(string)
	if cid == "" {
		return mcpError("campaign_id required"), nil
	}
	if errOut := a.requireMetaResource(ctx, acct, def.CampaignListTool, "campaign", cid); errOut != nil {
		return errOut, nil
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
		input["stop_time"] = v
	}
	mergeOptions(input, args)
	return a.execUpdateOrErr(ctx, acct, def.CampaignUpdateTool, input)
}

func (metaAdapter) CampaignDelete(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error) {
	cid, _ := args["campaign_id"].(string)
	if cid == "" {
		return mcpError("campaign_id required"), nil
	}
	if errOut := a.requireMetaResource(ctx, acct, def.CampaignListTool, "campaign", cid); errOut != nil {
		return errOut, nil
	}
	return a.execOrErr(ctx, acct, def.CampaignDeleteTool, map[string]any{"campaignId": cid})
}

func (metaAdapter) AdSetCreate(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error) {
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
	input := map[string]any{
		def.AccountIDInputField: acct.NativeAccountID,
		"campaign_id":           cid,
		"name":                  name,
		"optimization_goal":     mappedOG,
		"billing_event":         metaBillingEvent[be],
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
	} else if input["daily_budget"] != nil || input["lifetime_budget"] != nil {
		input["bid_strategy"] = "LOWEST_COST_WITHOUT_CAP"
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
	} else {
		switch strings.ToLower(stringArgAny(args, "conversion_location")) {
		case "instant_form":
			input["destination_type"] = "ON_AD"
		case "website":
			input["destination_type"] = "WEBSITE"
		case "calls":
			input["destination_type"] = "PHONE_CALL"
		case "messages":
			input["destination_type"] = "MESSENGER"
		}
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

func (metaAdapter) AdSetList(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error) {
	objectID := acct.NativeAccountID
	if cid, _ := args["campaign_id"].(string); cid != "" {
		objectID = cid
	}
	input := map[string]any{"objectId": objectID, "fields": metaAdSetFields}
	if v := intArg(args, "limit", 0); v > 0 {
		input["limit"] = v
	}
	if v, _ := args["after"].(string); v != "" {
		input["after"] = v
	}
	return a.execOrErr(ctx, acct, def.AdSetListTool, input)
}

func (metaAdapter) AdSetUpdate(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error) {
	asid, _ := args["adset_id"].(string)
	if asid == "" {
		return mcpError("adset_id required"), nil
	}
	if errOut := a.requireMetaResource(ctx, acct, def.AdSetListTool, "ad set", asid); errOut != nil {
		return errOut, nil
	}
	input := map[string]any{"adSetId": asid}
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
	return a.execUpdateOrErr(ctx, acct, def.AdSetUpdateTool, input)
}

func (metaAdapter) AdSetDelete(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error) {
	asid, _ := args["adset_id"].(string)
	if asid == "" {
		return mcpError("adset_id required"), nil
	}
	if errOut := a.requireMetaResource(ctx, acct, def.AdSetListTool, "ad set", asid); errOut != nil {
		return errOut, nil
	}
	return a.execOrErr(ctx, acct, def.AdSetDeleteTool, map[string]any{"adSetId": asid})
}

func (metaAdapter) AdCreate(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error) {
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
		"creative":              map[string]any{"creative_id": cr},
	}
	if v, _ := args["status"].(string); v != "" {
		input["status"] = v
	} else {
		input["status"] = "PAUSED"
	}
	mergeOptions(input, args)
	return a.execOrErr(ctx, acct, def.AdCreateTool, input)
}

func (metaAdapter) AdList(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error) {
	objectID := acct.NativeAccountID
	if v, _ := args["adset_id"].(string); v != "" {
		objectID = v
	}
	input := map[string]any{"objectId": objectID, "fields": metaAdFields}
	if v := intArg(args, "limit", 0); v > 0 {
		input["limit"] = v
	}
	if v, _ := args["after"].(string); v != "" {
		input["after"] = v
	}
	return a.execOrErr(ctx, acct, def.AdListTool, input)
}

func (metaAdapter) AdUpdate(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error) {
	adID, _ := args["ad_id"].(string)
	if adID == "" {
		return mcpError("ad_id required"), nil
	}
	if errOut := a.requireMetaResource(ctx, acct, def.AdListTool, "ad", adID); errOut != nil {
		return errOut, nil
	}
	input := map[string]any{"adId": adID}
	if v, _ := args["name"].(string); v != "" {
		input["name"] = v
	}
	if v, _ := args["status"].(string); v != "" {
		input["status"] = v
	}
	if v, _ := args["creative_id"].(string); v != "" {
		input["creative"] = map[string]any{"creative_id": v}
	}
	mergeOptions(input, args)
	return a.execUpdateOrErr(ctx, acct, def.AdUpdateTool, input)
}

func (metaAdapter) AdDelete(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error) {
	adID, _ := args["ad_id"].(string)
	if adID == "" {
		return mcpError("ad_id required"), nil
	}
	if errOut := a.requireMetaResource(ctx, acct, def.AdListTool, "ad", adID); errOut != nil {
		return errOut, nil
	}
	return a.execOrErr(ctx, acct, def.AdDeleteTool, map[string]any{"adId": adID})
}

func (metaAdapter) CreativeCreate(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error) {
	format := strings.ToLower(stringArgAny(args, "format"))
	name := stringArgAny(args, "name")
	identityID := stringArgAny(args, "identity_id")
	if name == "" || identityID == "" {
		return mcpError("meta creative_create requires name and identity_id (Facebook Page ID)"), nil
	}

	story := map[string]any{"page_id": identityID}
	putString(story, "instagram_user_id", args, "secondary_identity_id")
	switch format {
	case "link", "image":
		destination := stringArgAny(args, "destination_url")
		if destination == "" {
			return mcpError("link and image creatives require destination_url"), nil
		}
		linkData := map[string]any{"link": destination}
		putString(linkData, "name", args, "headline")
		putString(linkData, "message", args, "primary_text")
		putString(linkData, "description", args, "description")
		putString(linkData, "picture", args, "image_url")
		putString(linkData, "image_hash", args, "image_hash")
		if format == "image" && stringArgAny(args, "image_url") == "" && stringArgAny(args, "image_hash") == "" {
			return mcpError("image creatives require image_url or image_hash"), nil
		}
		if cta := metaCTA(args, destination); cta != nil {
			linkData["call_to_action"] = cta
		}
		story["link_data"] = linkData
	case "video":
		videoID := stringArgAny(args, "video_id")
		destination := stringArgAny(args, "destination_url")
		if videoID == "" {
			return mcpError("video creatives require video_id"), nil
		}
		videoData := map[string]any{"video_id": videoID}
		putString(videoData, "title", args, "headline")
		putString(videoData, "message", args, "primary_text")
		putString(videoData, "link_description", args, "description")
		putString(videoData, "image_url", args, "thumbnail_url")
		putString(videoData, "image_hash", args, "thumbnail_hash")
		if stringArgAny(args, "thumbnail_url") == "" && stringArgAny(args, "thumbnail_hash") == "" {
			return mcpError("video creatives require thumbnail_url or thumbnail_hash"), nil
		}
		if cta := metaCTA(args, destination); cta != nil {
			videoData["call_to_action"] = cta
		}
		story["video_data"] = videoData
	case "carousel":
		rawCards, _ := args["cards"].([]any)
		if len(rawCards) < 2 || len(rawCards) > 10 {
			return mcpError("carousel creatives require 2 to 10 cards"), nil
		}
		children := make([]any, 0, len(rawCards))
		for i, raw := range rawCards {
			card, _ := raw.(map[string]any)
			destination := stringArgAny(card, "destination_url")
			if destination == "" {
				return mcpError(fmt.Sprintf("carousel card %d requires destination_url", i+1)), nil
			}
			if stringArgAny(card, "image_url") == "" && stringArgAny(card, "image_hash") == "" {
				return mcpError(fmt.Sprintf("carousel card %d requires image_url or image_hash", i+1)), nil
			}
			child := map[string]any{"link": destination}
			putString(child, "name", card, "headline")
			putString(child, "description", card, "description")
			putString(child, "picture", card, "image_url")
			putString(child, "image_hash", card, "image_hash")
			if cta := metaCTA(card, destination); cta != nil {
				child["call_to_action"] = cta
			}
			children = append(children, child)
		}
		linkData := map[string]any{"child_attachments": children}
		putString(linkData, "message", args, "primary_text")
		putString(linkData, "link", args, "destination_url")
		story["link_data"] = linkData
	default:
		return mcpError("format must be link, image, video, or carousel"), nil
	}

	input := map[string]any{
		def.AccountIDInputField: acct.NativeAccountID,
		"name":                  name,
		"object_story_spec":     story,
	}
	putString(input, "url_tags", args, "url_tags")
	mergeOptions(input, args)
	return a.execOrErr(ctx, acct, def.CreativeCreateTool, input)
}

func (metaAdapter) CreativeGet(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error) {
	creativeID := stringArgAny(args, "creative_id")
	if creativeID == "" {
		return mcpError("creative_id required"), nil
	}
	if errOut := a.requireMetaResource(ctx, acct, def.CreativeListTool, "creative", creativeID); errOut != nil {
		return errOut, nil
	}
	return a.execOrErr(ctx, acct, def.CreativeGetTool, map[string]any{
		"creativeId": creativeID,
		"fields":     metaCreativeGetFields,
	})
}

func (metaAdapter) CreativeDelete(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error) {
	creativeID := stringArgAny(args, "creative_id")
	if creativeID == "" {
		return mcpError("creative_id required"), nil
	}
	if errOut := a.requireMetaResource(ctx, acct, def.CreativeListTool, "creative", creativeID); errOut != nil {
		return errOut, nil
	}
	return a.execOrErr(ctx, acct, def.CreativeDeleteTool, map[string]any{"creativeId": creativeID})
}

func (metaAdapter) CreativeUpload(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error) {
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
		if kind == "video" {
			input["title"] = name
		} else {
			input["name"] = name
		}
	} else if kind == "video" {
		input["title"] = "Video"
	}
	if storageID > 0 {
		var fetched struct {
			ID       int64  `json:"id"`
			URL      string `json:"url"`
			Filename string `json:"filename"`
			MimeType string `json:"mime_type"`
		}
		if err := ctx.PlatformAPI().CallAppResult("storage", "files_get_url", map[string]any{"id": storageID, "ttl_seconds": 3600}, &fetched); err != nil {
			return mcpError("storage.files_get_url: " + err.Error()), nil
		}
		if fetched.URL == "" {
			return mcpError("storage returned no URL for file id"), nil
		}
		sourceURL = fetched.URL
	}
	tool := def.CreativeUploadImageTool
	if kind == "video" {
		tool = def.CreativeUploadVideoTool
	}
	if kind == "image" {
		input["url"] = sourceURL
	} else {
		input["file_url"] = sourceURL
	}
	mergeOptions(input, args)
	parsed, errOut := a.execIntegrationTool(ctx, acct, tool, input)
	if errOut != nil {
		if kind == "image" && metaImageLibraryUnavailable(errOut) {
			return map[string]any{
				"kind": kind, "provider_upload": false, "upload_mode": "direct_url",
				"source_url": sourceURL,
				"warning":    "Meta image-library upload is unavailable for this app; pass source_url as creative_create.image_url.",
			}, nil
		}
		return errOut, nil
	}
	if assetID := creativeAssetID(parsed, kind); assetID != "" {
		recordCreativeAsset(ctx, args, acct, assetID, kind)
	}
	return parsed, nil
}

func (metaAdapter) CreativeAssetStatus(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error) {
	assetID := stringArgAny(args, "asset_id")
	kind := strings.ToLower(stringArgAny(args, "kind"))
	if assetID == "" {
		return mcpError("asset_id required"), nil
	}
	if kind != "" && kind != "video" {
		return mcpError("Meta only exposes processing status for video assets"), nil
	}
	if !creativeAssetTracked(ctx, args, acct, assetID) {
		ok, err := a.metaVideoBelongsToAccount(ctx, acct, def.CreativeListTool, assetID)
		if err != nil {
			return mcpError("verify video ownership: " + err.Error()), nil
		}
		if !ok {
			return mcpError("video asset does not belong to the selected ad account or was not uploaded through this app"), nil
		}
	}
	return a.execOrErr(ctx, acct, def.CreativeAssetStatusTool, map[string]any{
		"videoId": assetID,
		"fields":  "id,title,status,length,source,thumbnails,created_time",
	})
}

func (metaAdapter) CreativeAssetDelete(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error) {
	assetID := stringArgAny(args, "asset_id")
	kind := strings.ToLower(stringArgAny(args, "kind"))
	if assetID == "" {
		return mcpError("asset_id required"), nil
	}
	if kind != "video" {
		return mcpError("Meta asset deletion currently supports video assets; image hashes remain reusable in the account library"), nil
	}
	if !creativeAssetTracked(ctx, args, acct, assetID) {
		ok, err := a.metaVideoBelongsToAccount(ctx, acct, def.CreativeListTool, assetID)
		if err != nil {
			return mcpError("verify video ownership: " + err.Error()), nil
		}
		if !ok {
			return mcpError("video asset does not belong to the selected ad account or was not uploaded through this app"), nil
		}
	}
	parsed, errOut := a.execIntegrationTool(ctx, acct, def.CreativeAssetDeleteTool, map[string]any{
		def.AccountIDInputField: acct.NativeAccountID,
		"video_id":              assetID,
	})
	if errOut != nil {
		return errOut, nil
	}
	deleteCreativeAssetRecord(ctx, args, acct, assetID)
	return parsed, nil
}

func (metaAdapter) CreativeList(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error) {
	input := map[string]any{
		def.AccountIDInputField: acct.NativeAccountID,
		"fields":                metaCreativeFields,
	}
	if v := intArg(args, "limit", 0); v > 0 {
		input["limit"] = v
	}
	if v, _ := args["after"].(string); v != "" {
		input["after"] = v
	}
	return a.execOrErr(ctx, acct, def.CreativeListTool, input)
}

func (metaAdapter) AudienceList(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error) {
	input := map[string]any{
		def.AccountIDInputField: acct.NativeAccountID,
		"fields":                metaAudienceFields,
	}
	if v := intArg(args, "limit", 0); v > 0 {
		input["limit"] = v
	}
	if v, _ := args["after"].(string); v != "" {
		input["after"] = v
	}
	return a.execOrErr(ctx, acct, def.AudienceListTool, input)
}

func (metaAdapter) AudienceCreateCustom(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error) {
	name, _ := args["name"].(string)
	if name == "" {
		return mcpError("name required"), nil
	}
	input := map[string]any{def.AccountIDInputField: acct.NativeAccountID, "name": name}
	if v, _ := args["description"].(string); v != "" {
		input["description"] = v
	}
	if v, _ := args["subtype"].(string); v != "" {
		input["subtype"] = v
	} else {
		input["subtype"] = "CUSTOM"
	}
	if input["subtype"] == "CUSTOM" {
		input["customer_file_source"] = "USER_PROVIDED_ONLY"
	}
	mergeOptions(input, args)
	return a.execOrErr(ctx, acct, def.AudienceCreateCustomTool, input)
}

func (metaAdapter) AudienceCreateLookalike(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error) {
	name, _ := args["name"].(string)
	src, _ := args["source_audience_id"].(string)
	country, _ := args["country"].(string)
	if name == "" || src == "" || country == "" {
		return mcpError("name, source_audience_id, country required"), nil
	}
	input := map[string]any{
		def.AccountIDInputField: acct.NativeAccountID,
		"name":                  name,
		"subtype":               "LOOKALIKE",
		"origin_audience_id":    src,
		"lookalike_spec": map[string]any{
			"type":    "similarity",
			"country": country,
			"ratio":   0.01,
		},
	}
	if v, ok := args["ratio"].(float64); ok && v > 0 {
		input["lookalike_spec"].(map[string]any)["ratio"] = v
	}
	mergeOptions(input, args)
	return a.execOrErr(ctx, acct, def.AudienceCreateLookalikeTool, input)
}

type googleAdapter struct{}

func (googleAdapter) ListAccounts(a *App, ctx *sdk.AppCtx, row *pendingRow, def *platformDef) ([]map[string]any, error) {
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(row.connectionID, def.ListAccountsTool, map[string]any{})
	if err != nil {
		return nil, err
	}
	if res == nil || !res.Success {
		body := ""
		if res != nil {
			body = string(res.Data)
		}
		return nil, fmt.Errorf("upstream non-2xx: %s", body)
	}
	var payload struct {
		ResourceNames []string `json:"resourceNames"`
	}
	if err := json.Unmarshal(res.Data, &payload); err != nil {
		return nil, fmt.Errorf("parse accessible customers: %w", err)
	}
	accounts := make([]map[string]any, 0, len(payload.ResourceNames))
	seen := make(map[string]bool)
	appendAccount := func(account map[string]any) {
		id := googleCustomerID(toString(account["id"]))
		if id == "" || seen[id] || googleBool(account["manager"]) {
			return
		}
		status := strings.ToUpper(strings.TrimSpace(toString(account["status"])))
		if status == "CANCELED" || status == "CLOSED" {
			return
		}
		account["id"] = id
		if strings.TrimSpace(toString(account["name"])) == "" {
			account["name"] = id
		}
		seen[id] = true
		accounts = append(accounts, account)
	}
	for _, rn := range payload.ResourceNames {
		customerID := googleCustomerID(rn)
		if customerID == "" {
			continue
		}
		account := map[string]any{"id": customerID, "name": customerID}
		enriched, err := googleFetchCustomer(ctx, row.connectionID, customerID)
		if err == nil {
			for k, v := range enriched {
				if v != nil && (toString(v) != "" || k == "manager") {
					account[k] = v
				}
			}
		}
		if googleBool(account["manager"]) {
			clients, err := googleFetchClientAccounts(ctx, row.connectionID, customerID)
			if err != nil {
				return nil, fmt.Errorf("list client accounts for manager %s: %w", customerID, err)
			}
			for _, client := range clients {
				appendAccount(client)
			}
			continue
		}
		appendAccount(account)
	}
	return accounts, nil
}

func (googleAdapter) CampaignList(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error) {
	query := `SELECT campaign.id, campaign.name, campaign.status, campaign.advertising_channel_type, campaign_budget.amount_micros, campaign_budget.resource_name FROM campaign`
	if status, _ := args["status"].(string); status != "" {
		if !googleValidStatus(status) {
			return mcpError("status must be ACTIVE or PAUSED"), nil
		}
		query += " WHERE campaign.status = " + googleCampaignStatus(status)
	}
	if limit := intArg(args, "limit", 0); limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", limit)
	}
	input := map[string]any{"customer_id": acct.NativeAccountID, "query": query}
	if pageToken, _ := args["after"].(string); pageToken != "" {
		input["page_token"] = pageToken
	}
	parsed, errOut := a.execIntegrationTool(ctx, acct, def.CampaignListTool, input)
	if errOut != nil {
		return errOut, nil
	}
	return map[string]any{"data": normalizeGoogleCampaigns(parsed)}, nil
}

func (googleAdapter) CampaignPerformance(a *App, ctx *sdk.AppCtx, acct *adAccount, _ *platformDef, args map[string]any) (any, error) {
	request, err := validatePerformanceRequest(args)
	if err != nil {
		return mcpError(err.Error()), nil
	}
	query := "SELECT segments.date, campaign.id, campaign.name, customer.currency_code, " +
		"metrics.cost_micros, metrics.impressions, metrics.clicks, metrics.conversions " +
		"FROM campaign WHERE segments.date BETWEEN '" + request.DateFrom + "' AND '" + request.DateTo + "'"
	if len(request.CampaignIDs) > 0 {
		for _, id := range request.CampaignIDs {
			if !googleNumericID(id) {
				return mcpError("Google campaign IDs must be numeric"), nil
			}
		}
		query += " AND campaign.id IN (" + strings.Join(request.CampaignIDs, ",") + ")"
	}

	input := map[string]any{
		"customer_id": acct.NativeAccountID,
		"query":       query,
	}
	data := make([]map[string]any, 0)
	pageToken := ""
	for page := 0; page < maxPerformancePages; page++ {
		if pageToken != "" {
			input["page_token"] = pageToken
		} else {
			delete(input, "page_token")
		}
		parsed, errOut := a.execIntegrationTool(ctx, acct, "search", input)
		if errOut != nil {
			return errOut, nil
		}
		for _, row := range resultRows(parsed) {
			data = append(data, normalizeGooglePerformance(acct, row))
		}
		next := googleNextPageToken(parsed)
		if next == "" {
			return performanceResponse(data), nil
		}
		if next == pageToken {
			return mcpError("Google Ads pagination returned a repeated page token"), nil
		}
		pageToken = next
	}
	return mcpError("Google Ads pagination exceeded the safety limit"), nil
}

func (googleAdapter) CampaignCreate(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error) {
	name, _ := args["name"].(string)
	if name == "" {
		return mcpError("name required"), nil
	}
	if status := stringArgAny(args, "status"); !googleValidStatus(status) {
		return mcpError("status must be ACTIVE or PAUSED"), nil
	}
	if intArg(args, "lifetime_budget_cents", 0) > 0 {
		return mcpError("Google Ads generic campaigns support daily_budget_cents only; use native platform_options for other budget semantics"), nil
	}
	if objective := strings.ToLower(stringArgAny(args, "objective")); objective != "traffic" {
		return mcpError("Google Ads generic campaign_create currently supports objective=traffic (Search) only"), nil
	}
	budgetMicros := googleBudgetMicros(args)
	opts, _ := args["platform_options"].(map[string]any)
	if budgetMicros == "" && opts["campaignBudget"] == nil && opts["campaign_budget"] == nil {
		return mcpError("google campaign_create requires daily_budget_cents or platform_options.campaignBudget"), nil
	}

	var budget any
	budgetResource := toString(opts["campaignBudget"])
	if budgetResource == "" {
		budgetResource = toString(opts["campaign_budget"])
	}
	if budgetResource == "" {
		budgetCreate := map[string]any{
			"name":           name + " Budget",
			"amountMicros":   budgetMicros,
			"deliveryMethod": "STANDARD",
		}
		if custom, ok := opts["budget"].(map[string]any); ok {
			for k, v := range custom {
				budgetCreate[k] = v
			}
		}
		var err error
		budget, err = a.execOrErr(ctx, acct, "budget_mutate", map[string]any{
			"customer_id": acct.NativeAccountID,
			"operations":  []any{map[string]any{"create": budgetCreate}},
		})
		if err != nil {
			return nil, err
		}
		budgetResource = firstResourceName(budget)
		if budgetResource == "" {
			return mcpError("google budget_mutate returned no resourceName"), nil
		}
	}

	campaign := map[string]any{
		"name":                   name,
		"status":                 googleCampaignStatus(stringArgAny(args, "status")),
		"advertisingChannelType": "SEARCH",
		"campaignBudget":         budgetResource,
	}
	if v, _ := args["start_time"].(string); v != "" {
		campaign["startDate"] = googleDate(v)
	}
	if v, _ := args["end_time"].(string); v != "" {
		campaign["endDate"] = googleDate(v)
	}
	if custom, ok := opts["campaign"].(map[string]any); ok {
		for k, v := range custom {
			campaign[k] = v
		}
	}
	mergeOptions(campaign, args)
	delete(campaign, "budget")
	delete(campaign, "campaign")
	delete(campaign, "campaign_budget")
	out, errOut := a.execIntegrationTool(ctx, acct, def.CampaignCreateTool, map[string]any{
		"customer_id": acct.NativeAccountID,
		"operations":  []any{map[string]any{"create": campaign}},
	})
	if errOut != nil {
		if budget != nil {
			_, cleanupErr := a.execIntegrationTool(ctx, acct, "budget_mutate", map[string]any{
				"customer_id": acct.NativeAccountID,
				"operations":  []any{map[string]any{"remove": budgetResource}},
			})
			if cleanupErr != nil {
				errOut["cleanup_warning"] = "campaign creation failed and the new budget could not be removed"
			}
		}
		return errOut, nil
	}
	return map[string]any{"budget": budget, "campaign": out}, nil
}

func (googleAdapter) CampaignUpdate(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error) {
	cid, _ := args["campaign_id"].(string)
	if cid == "" {
		return mcpError("campaign_id required"), nil
	}
	if !googleNumericID(cid) {
		return mcpError("google campaign_id must be numeric"), nil
	}
	if intArg(args, "lifetime_budget_cents", 0) > 0 {
		return mcpError("Google Ads generic campaigns support daily_budget_cents only"), nil
	}
	opts, _ := args["platform_options"].(map[string]any)
	var budgetOut any
	if cents := intArg(args, "daily_budget_cents", 0); cents > 0 {
		budgetResource := firstString(opts, "campaignBudgetResource", "campaign_budget_resource")
		if budgetResource == "" {
			query := fmt.Sprintf(
				"SELECT campaign.id, campaign_budget.resource_name FROM campaign WHERE campaign.id = %s LIMIT 1",
				cid,
			)
			found, lookupErr := a.execIntegrationTool(ctx, acct, def.CampaignListTool, map[string]any{
				"customer_id": acct.NativeAccountID,
				"query":       query,
			})
			if lookupErr != nil {
				return lookupErr, nil
			}
			rows := resultRows(found)
			if len(rows) > 0 {
				budgetMap := mapAt(rows[0], "campaignBudget")
				if len(budgetMap) == 0 {
					budgetMap = mapAt(rows[0], "campaign_budget")
				}
				budgetResource = firstString(budgetMap, "resourceName", "resource_name")
			}
		}
		if budgetResource == "" {
			return mcpError("could not resolve the campaign budget; pass platform_options.campaignBudgetResource"), nil
		}
		var budgetErr map[string]any
		budgetOut, budgetErr = a.execIdempotentUpdate(ctx, acct, "budget_mutate", map[string]any{
			"customer_id": acct.NativeAccountID,
			"operations": []any{map[string]any{
				"update": map[string]any{
					"resourceName": budgetResource,
					"amountMicros": strconv.Itoa(cents * 10000),
				},
				"updateMask": "amount_micros",
			}},
		})
		if budgetErr != nil {
			return budgetErr, nil
		}
	}
	update := map[string]any{"resourceName": googleCampaignResource(acct.NativeAccountID, cid)}
	fields := []string{}
	if v, _ := args["name"].(string); v != "" {
		update["name"] = v
		fields = append(fields, "name")
	}
	if v, _ := args["status"].(string); v != "" {
		if !googleValidStatus(v) {
			return mcpError("status must be ACTIVE or PAUSED"), nil
		}
		update["status"] = googleCampaignStatus(v)
		fields = append(fields, "status")
	}
	if v, _ := args["start_time"].(string); v != "" {
		update["startDate"] = googleDate(v)
		fields = append(fields, "start_date")
	}
	if v, _ := args["end_time"].(string); v != "" {
		update["endDate"] = googleDate(v)
		fields = append(fields, "end_date")
	}
	if custom, ok := opts["campaign"].(map[string]any); ok {
		for k, v := range custom {
			if k == "resourceName" {
				continue
			}
			update[k] = v
			fields = append(fields, googleMaskField(k))
		}
	}
	if len(fields) == 0 {
		if budgetOut != nil {
			return map[string]any{"budget": budgetOut}, nil
		}
		return mcpError("no google campaign fields to update"), nil
	}
	campaignOut, err := a.execUpdateOrErr(ctx, acct, def.CampaignUpdateTool, map[string]any{
		"customer_id": acct.NativeAccountID,
		"operations": []any{map[string]any{
			"update":     update,
			"updateMask": strings.Join(fields, ","),
		}},
	})
	if err != nil {
		return nil, err
	}
	if budgetOut != nil {
		return map[string]any{"budget": budgetOut, "campaign": campaignOut}, nil
	}
	return campaignOut, nil
}

func (googleAdapter) CampaignDelete(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error) {
	cid, _ := args["campaign_id"].(string)
	if cid == "" {
		return mcpError("campaign_id required"), nil
	}
	if !googleNumericID(cid) {
		return mcpError("google campaign_id must be numeric"), nil
	}
	return a.execOrErr(ctx, acct, def.CampaignDeleteTool, map[string]any{
		"customer_id": acct.NativeAccountID,
		"operations":  []any{map[string]any{"remove": googleCampaignResource(acct.NativeAccountID, cid)}},
	})
}

func (googleAdapter) AdSetList(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error) {
	query := `SELECT ad_group.id, ad_group.name, ad_group.status, campaign.id FROM ad_group`
	if cid, _ := args["campaign_id"].(string); cid != "" {
		if !googleNumericID(cid) {
			return mcpError("google campaign_id must be numeric"), nil
		}
		query += " WHERE campaign.id = " + cid
	}
	if limit := intArg(args, "limit", 0); limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", limit)
	}
	parsed, errOut := a.execIntegrationTool(ctx, acct, def.AdSetListTool, map[string]any{"customer_id": acct.NativeAccountID, "query": query})
	if errOut != nil {
		return errOut, nil
	}
	return map[string]any{"data": normalizeGoogleAdGroups(parsed)}, nil
}

func (googleAdapter) AdSetCreate(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error) {
	name, _ := args["name"].(string)
	cid, _ := args["campaign_id"].(string)
	if name == "" || cid == "" {
		return mcpError("name and campaign_id required"), nil
	}
	if !googleNumericID(cid) {
		return mcpError("google campaign_id must be numeric"), nil
	}
	if status := stringArgAny(args, "status"); !googleValidStatus(status) {
		return mcpError("status must be ACTIVE or PAUSED"), nil
	}
	adGroup := map[string]any{
		"name":     name,
		"campaign": googleCampaignResource(acct.NativeAccountID, cid),
		"status":   googleCampaignStatus(stringArgAny(args, "status")),
	}
	if bid := intArg(args, "bid_amount_cents", 0); bid > 0 {
		adGroup["cpcBidMicros"] = strconv.Itoa(bid * 10000)
	}
	if opts, _ := args["platform_options"].(map[string]any); opts != nil {
		if custom, ok := opts["ad_group"].(map[string]any); ok {
			for k, v := range custom {
				adGroup[k] = v
			}
		}
	}
	return a.execOrErr(ctx, acct, def.AdSetCreateTool, map[string]any{
		"customer_id": acct.NativeAccountID,
		"operations":  []any{map[string]any{"create": adGroup}},
	})
}

func (googleAdapter) AdSetUpdate(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error) {
	asid, _ := args["adset_id"].(string)
	if asid == "" {
		return mcpError("adset_id required"), nil
	}
	if !googleNumericID(asid) {
		return mcpError("google adset_id must be numeric"), nil
	}
	update := map[string]any{"resourceName": googleAdGroupResource(acct.NativeAccountID, asid)}
	fields := []string{}
	if v, _ := args["name"].(string); v != "" {
		update["name"] = v
		fields = append(fields, "name")
	}
	if v, _ := args["status"].(string); v != "" {
		if !googleValidStatus(v) {
			return mcpError("status must be ACTIVE or PAUSED"), nil
		}
		update["status"] = googleCampaignStatus(v)
		fields = append(fields, "status")
	}
	if bid := intArg(args, "bid_amount_cents", 0); bid > 0 {
		update["cpcBidMicros"] = strconv.Itoa(bid * 10000)
		fields = append(fields, "cpc_bid_micros")
	}
	if len(fields) == 0 {
		return mcpError("no google ad group fields to update"), nil
	}
	return a.execUpdateOrErr(ctx, acct, def.AdSetUpdateTool, map[string]any{
		"customer_id": acct.NativeAccountID,
		"operations":  []any{map[string]any{"update": update, "updateMask": strings.Join(fields, ",")}},
	})
}

func (googleAdapter) AdSetDelete(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error) {
	asid, _ := args["adset_id"].(string)
	if asid == "" {
		return mcpError("adset_id required"), nil
	}
	if !googleNumericID(asid) {
		return mcpError("google adset_id must be numeric"), nil
	}
	return a.execOrErr(ctx, acct, def.AdSetDeleteTool, map[string]any{
		"customer_id": acct.NativeAccountID,
		"operations":  []any{map[string]any{"remove": googleAdGroupResource(acct.NativeAccountID, asid)}},
	})
}

func (googleAdapter) AdList(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error) {
	query := `SELECT ad_group_ad.resource_name, ad_group_ad.ad.id, ad_group_ad.status, ad_group.id, campaign.id FROM ad_group_ad`
	if asid, _ := args["adset_id"].(string); asid != "" {
		if !googleNumericID(asid) {
			return mcpError("google adset_id must be numeric"), nil
		}
		query += " WHERE ad_group.id = " + asid
	}
	if limit := intArg(args, "limit", 0); limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", limit)
	}
	parsed, errOut := a.execIntegrationTool(ctx, acct, def.AdListTool, map[string]any{"customer_id": acct.NativeAccountID, "query": query})
	if errOut != nil {
		return errOut, nil
	}
	return map[string]any{"data": normalizeGoogleAds(parsed)}, nil
}

func (googleAdapter) AdCreate(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error) {
	opts, _ := args["platform_options"].(map[string]any)
	if ops, ok := opts["operations"].([]any); ok && len(ops) > 0 {
		if !googlePayloadScoped(ops, acct.NativeAccountID) {
			return mcpError("google operations contain a resource from another customer"), nil
		}
		return a.execOrErr(ctx, acct, def.AdCreateTool, map[string]any{"customer_id": acct.NativeAccountID, "operations": ops})
	}
	ad, _ := opts["ad"].(map[string]any)
	asid, _ := args["adset_id"].(string)
	if asid == "" || len(ad) == 0 {
		return mcpError("google ad_create requires adset_id and platform_options.ad, or platform_options.operations"), nil
	}
	if !googleNumericID(asid) {
		return mcpError("google adset_id must be numeric"), nil
	}
	if status := stringArgAny(args, "status"); !googleValidStatus(status) {
		return mcpError("status must be ACTIVE or PAUSED"), nil
	}
	if !googlePayloadScoped(ad, acct.NativeAccountID) {
		return mcpError("google ad payload contains a resource from another customer"), nil
	}
	create := map[string]any{
		"adGroup": googleAdGroupResource(acct.NativeAccountID, asid),
		"status":  googleCampaignStatus(stringArgAny(args, "status")),
		"ad":      ad,
	}
	return a.execOrErr(ctx, acct, def.AdCreateTool, map[string]any{
		"customer_id": acct.NativeAccountID,
		"operations":  []any{map[string]any{"create": create}},
	})
}

func (googleAdapter) AdUpdate(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error) {
	opts, _ := args["platform_options"].(map[string]any)
	if ops, ok := opts["operations"].([]any); ok && len(ops) > 0 {
		if !googlePayloadScoped(ops, acct.NativeAccountID) {
			return mcpError("google operations contain a resource from another customer"), nil
		}
		return a.execOrErr(ctx, acct, def.AdUpdateTool, map[string]any{"customer_id": acct.NativeAccountID, "operations": ops})
	}
	adID := stringArgAny(args, "ad_id")
	if adID == "" {
		return mcpError("ad_id required"), nil
	}
	resource := adID
	if strings.HasPrefix(resource, "customers/") {
		if !strings.HasPrefix(resource, "customers/"+acct.NativeAccountID+"/adGroupAds/") {
			return mcpError("google ad resource belongs to another customer"), nil
		}
	} else {
		adSetID := stringArgAny(args, "adset_id")
		if !googleNumericID(adSetID) || !googleNumericID(adID) {
			return mcpError("google adset_id and ad_id must be numeric"), nil
		}
		resource = fmt.Sprintf("customers/%s/adGroupAds/%s~%s", acct.NativeAccountID, adSetID, adID)
	}
	status := stringArgAny(args, "status")
	if status == "" {
		return mcpError("google generic ad_update currently supports status; use platform_options.operations for other fields"), nil
	}
	if !googleValidStatus(status) {
		return mcpError("status must be ACTIVE or PAUSED"), nil
	}
	return a.execUpdateOrErr(ctx, acct, def.AdUpdateTool, map[string]any{
		"customer_id": acct.NativeAccountID,
		"operations": []any{map[string]any{
			"update":     map[string]any{"resourceName": resource, "status": googleCampaignStatus(status)},
			"updateMask": "status",
		}},
	})
}

func (googleAdapter) AdDelete(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error) {
	adID, _ := args["ad_id"].(string)
	if adID == "" {
		return mcpError("ad_id required"), nil
	}
	resource := adID
	if strings.HasPrefix(resource, "customers/") {
		if !strings.HasPrefix(resource, "customers/"+acct.NativeAccountID+"/") {
			return mcpError("google ad resource belongs to another customer"), nil
		}
	} else {
		asid, _ := args["adset_id"].(string)
		if asid == "" {
			return mcpError("google ad_delete requires adset_id unless ad_id is a full resourceName"), nil
		}
		if !googleNumericID(asid) || !googleNumericID(adID) {
			return mcpError("google adset_id and ad_id must be numeric"), nil
		}
		resource = fmt.Sprintf("customers/%s/adGroupAds/%s~%s", acct.NativeAccountID, asid, adID)
	}
	return a.execOrErr(ctx, acct, def.AdDeleteTool, map[string]any{
		"customer_id": acct.NativeAccountID,
		"operations":  []any{map[string]any{"remove": resource}},
	})
}

func (googleAdapter) CreativeCreate(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error) {
	format := strings.ToLower(stringArgAny(args, "format"))
	name := stringArgAny(args, "name")
	opts, _ := args["platform_options"].(map[string]any)
	asset, _ := opts["asset"].(map[string]any)
	if len(asset) == 0 {
		switch format {
		case "video":
			videoID := stringArgAny(args, "video_id")
			if videoID == "" {
				return mcpError("google video creatives require video_id containing a YouTube video ID"), nil
			}
			asset = map[string]any{
				"name": name,
				"youtubeVideoAsset": map[string]any{
					"youtubeVideoId": videoID,
				},
			}
		case "image":
			return mcpError("google image creative creation requires platform_options.asset with imageAsset.data; Google Ads does not ingest image assets from a public URL"), nil
		case "link", "carousel":
			return mcpError("Google Ads embeds link and carousel creative fields in the ad format; create reusable assets here, then supply the ad-format payload to ad_create"), nil
		default:
			return mcpError("format must be link, image, video, or carousel"), nil
		}
	}
	if name != "" {
		asset["name"] = name
	}
	if !googlePayloadScoped(asset, acct.NativeAccountID) {
		return mcpError("google asset payload contains a resource from another customer"), nil
	}
	return a.execOrErr(ctx, acct, def.CreativeCreateTool, map[string]any{
		"customer_id": acct.NativeAccountID,
		"operations":  []any{map[string]any{"create": asset}},
	})
}

func (googleAdapter) CreativeGet(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error) {
	creativeID := stringArgAny(args, "creative_id")
	if creativeID == "" {
		return mcpError("creative_id required"), nil
	}
	if strings.HasPrefix(creativeID, "customers/") && !strings.HasPrefix(creativeID, "customers/"+acct.NativeAccountID+"/assets/") {
		return mcpError("google creative resource belongs to another customer"), nil
	}
	id := creativeID
	if strings.HasPrefix(creativeID, "customers/") {
		parts := strings.Split(creativeID, "/")
		id = parts[len(parts)-1]
	}
	if !googleNumericID(id) {
		return mcpError("google creative_id must be a numeric asset id or full asset resource name"), nil
	}
	query := "SELECT asset.id, asset.name, asset.type, asset.resource_name, asset.youtube_video_asset.youtube_video_id FROM asset WHERE asset.id = " + id + " LIMIT 1"
	return a.execOrErr(ctx, acct, def.CreativeGetTool, map[string]any{
		"customer_id": acct.NativeAccountID,
		"query":       query,
	})
}

func (googleAdapter) CreativeDelete(_ *App, _ *sdk.AppCtx, _ *adAccount, _ *platformDef, _ map[string]any) (any, error) {
	return mcpError("Google Ads reusable assets cannot be removed through AssetService; remove ads and asset links that reference the asset instead"), nil
}

func (googleAdapter) CreativeUpload(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error) {
	opts, _ := args["platform_options"].(map[string]any)
	asset, _ := opts["asset"].(map[string]any)
	if len(asset) == 0 {
		return mcpError("google creative_upload requires platform_options.asset with a native Google Ads asset create payload"), nil
	}
	if name, _ := args["name"].(string); name != "" {
		asset["name"] = name
	}
	return a.execOrErr(ctx, acct, def.CreativeCreateTool, map[string]any{
		"customer_id": acct.NativeAccountID,
		"operations":  []any{map[string]any{"create": asset}},
	})
}

func (googleAdapter) CreativeAssetStatus(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error) {
	assetID := stringArgAny(args, "asset_id")
	if assetID == "" {
		return mcpError("asset_id required"), nil
	}
	return (googleAdapter{}).CreativeGet(a, ctx, acct, def, map[string]any{
		"creative_id":   assetID,
		"ad_account_id": args["ad_account_id"],
	})
}

func (googleAdapter) CreativeAssetDelete(_ *App, _ *sdk.AppCtx, _ *adAccount, _ *platformDef, _ map[string]any) (any, error) {
	return mcpError("Google Ads reusable assets cannot be removed through AssetService; remove ads and asset links that reference the asset instead"), nil
}

func (googleAdapter) CreativeList(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error) {
	query := `SELECT asset.id, asset.name, asset.type, asset.resource_name FROM asset`
	if limit := intArg(args, "limit", 0); limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", limit)
	}
	return a.execOrErr(ctx, acct, def.CreativeListTool, map[string]any{"customer_id": acct.NativeAccountID, "query": query})
}

func (googleAdapter) AudienceList(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error) {
	query := `SELECT user_list.id, user_list.name, user_list.type, user_list.size_for_display, user_list.size_for_search FROM user_list`
	if limit := intArg(args, "limit", 0); limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", limit)
	}
	return a.execOrErr(ctx, acct, def.AudienceListTool, map[string]any{"customer_id": acct.NativeAccountID, "query": query})
}

func (googleAdapter) AudienceCreateCustom(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error) {
	opts, _ := args["platform_options"].(map[string]any)
	userList, _ := opts["user_list"].(map[string]any)
	name, _ := args["name"].(string)
	if len(userList) == 0 {
		userList = map[string]any{}
	}
	if name != "" {
		userList["name"] = name
	}
	if len(userList) == 0 {
		return mcpError("google audience_create_custom requires name or platform_options.user_list"), nil
	}
	return a.execOrErr(ctx, acct, def.AudienceCreateCustomTool, map[string]any{
		"customer_id": acct.NativeAccountID,
		"operations":  []any{map[string]any{"create": userList}},
	})
}

func (googleAdapter) AudienceCreateLookalike(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error) {
	return mcpError("google audience_create_lookalike is not exposed as a generic operation; use audience_create_custom with platform_options.user_list"), nil
}

func (a *App) toolAdSetCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	acct, def, errOut := a.resolveAdAccount(ctx, args)
	if errOut != nil {
		return errOut, nil
	}
	if acct.Platform == "meta" && len(asMap(args["promoted_object"])) == 0 {
		if strings.EqualFold(stringArgAny(args, "conversion_location"), "instant_form") {
			page, selectionErr := a.resolveResourceChoice(ctx, acct, "publishing_identity", resourceIdentity, "facebook_page", 0)
			if selectionErr != nil {
				return selectionErr, nil
			}
			args["promoted_object"] = map[string]any{"page_id": page.NativeID}
		}
		resourceID := int64(intArg(args, "tracking_source_resource_id", 0))
		conversionEvent := strings.TrimSpace(stringArgAny(args, "conversion_event"))
		if resourceID > 0 || conversionEvent != "" {
			resource, resourceErr := a.resolveResourceChoice(
				ctx, acct, "conversion_source", resourceTrackingSource, "meta_pixel", resourceID,
			)
			if resourceErr != nil {
				return resourceErr, nil
			}
			promotedObject := map[string]any{"pixel_id": resource.NativeID}
			if conversionEvent != "" {
				promotedObject["custom_event_type"] = strings.ToUpper(conversionEvent)
			}
			args["promoted_object"] = promotedObject
		}
	}
	out, err := platformAdapters[acct.Platform].AdSetCreate(a, ctx, acct, def, args)
	a.emitEntityChanged(ctx, acct, "ad_group", "created", args, out, err)
	return out, err
}

func (a *App) toolAdSetList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	acct, def, errOut := a.resolveAdAccount(ctx, args)
	if errOut != nil {
		return errOut, nil
	}
	return platformAdapters[acct.Platform].AdSetList(a, ctx, acct, def, args)
}

func (a *App) toolAdSetUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	acct, def, errOut := a.resolveAdAccount(ctx, args)
	if errOut != nil {
		return errOut, nil
	}
	out, err := platformAdapters[acct.Platform].AdSetUpdate(a, ctx, acct, def, args)
	a.emitEntityChanged(ctx, acct, "ad_group", "updated", args, out, err)
	return out, err
}

func (a *App) toolAdSetDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	acct, def, errOut := a.resolveAdAccount(ctx, args)
	if errOut != nil {
		return errOut, nil
	}
	out, err := platformAdapters[acct.Platform].AdSetDelete(a, ctx, acct, def, args)
	a.emitEntityChanged(ctx, acct, "ad_group", "deleted", args, out, err)
	return out, err
}

// ─── Ad tools ───────────────────────────────────────────────────────

func (a *App) toolAdCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	acct, def, errOut := a.resolveAdAccount(ctx, args)
	if errOut != nil {
		return errOut, nil
	}
	out, err := platformAdapters[acct.Platform].AdCreate(a, ctx, acct, def, args)
	a.emitEntityChanged(ctx, acct, "ad", "created", args, out, err)
	return out, err
}

func (a *App) toolAdList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	acct, def, errOut := a.resolveAdAccount(ctx, args)
	if errOut != nil {
		return errOut, nil
	}
	return platformAdapters[acct.Platform].AdList(a, ctx, acct, def, args)
}

func (a *App) toolAdUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	acct, def, errOut := a.resolveAdAccount(ctx, args)
	if errOut != nil {
		return errOut, nil
	}
	out, err := platformAdapters[acct.Platform].AdUpdate(a, ctx, acct, def, args)
	a.emitEntityChanged(ctx, acct, "ad", "updated", args, out, err)
	return out, err
}

func (a *App) toolAdDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	acct, def, errOut := a.resolveAdAccount(ctx, args)
	if errOut != nil {
		return errOut, nil
	}
	out, err := platformAdapters[acct.Platform].AdDelete(a, ctx, acct, def, args)
	a.emitEntityChanged(ctx, acct, "ad", "deleted", args, out, err)
	return out, err
}

// ─── Creative tools ────────────────────────────────────────────────

func (a *App) toolCreativeCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	acct, def, errOut := a.resolveAdAccount(ctx, args)
	if errOut != nil {
		return errOut, nil
	}
	if acct.Platform == "meta" {
		var pageResource *adResource
		if strings.TrimSpace(stringArgAny(args, "identity_id")) == "" {
			resource, resourceErr := a.resolveResourceChoice(
				ctx, acct, "publishing_identity", resourceIdentity, "facebook_page",
				int64(intArg(args, "identity_resource_id", 0)),
			)
			if resourceErr != nil {
				return resourceErr, nil
			}
			pageResource = resource
			args["identity_id"] = resource.NativeID
		} else if resourceID := int64(intArg(args, "identity_resource_id", 0)); resourceID > 0 {
			resource, resourceErr := a.resolveResourceChoice(
				ctx, acct, "publishing_identity", resourceIdentity, "facebook_page", resourceID,
			)
			if resourceErr != nil {
				return resourceErr, nil
			}
			pageResource = resource
		}

		if strings.TrimSpace(stringArgAny(args, "secondary_identity_id")) == "" {
			secondaryID := int64(intArg(args, "secondary_identity_resource_id", 0))
			if secondaryID > 0 {
				resource, resourceErr := a.resolveResourceChoice(
					ctx, acct, "instagram_identity", resourceIdentity, "instagram_business", secondaryID,
				)
				if resourceErr != nil {
					return resourceErr, nil
				}
				args["secondary_identity_id"] = resource.NativeID
			} else if pageResource != nil {
				if linked, err := a.linkedInstagramIdentity(ctx, acct, pageResource.ID); err == nil && linked != nil {
					args["secondary_identity_id"] = linked.NativeID
				}
			}
		}
		if strings.EqualFold(stringArgAny(args, "conversion_location"), "instant_form") || intArg(args, "lead_form_resource_id", 0) > 0 {
			form, formErr := a.resolveResourceChoice(
				ctx, acct, "lead_form", resourceLeadForm, "meta_lead_form",
				int64(intArg(args, "lead_form_resource_id", 0)),
			)
			if formErr != nil {
				return formErr, nil
			}
			if pageResource != nil && form.ParentResourceID != pageResource.ID {
				return mcpError("lead form belongs to a different Facebook Page than the publishing identity"), nil
			}
			args["lead_form_id"] = form.NativeID
		}
	}
	return platformAdapters[acct.Platform].CreativeCreate(a, ctx, acct, def, args)
}

func (a *App) toolCreativeGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	acct, def, errOut := a.resolveAdAccount(ctx, args)
	if errOut != nil {
		return errOut, nil
	}
	return platformAdapters[acct.Platform].CreativeGet(a, ctx, acct, def, args)
}

func (a *App) toolCreativeDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	acct, def, errOut := a.resolveAdAccount(ctx, args)
	if errOut != nil {
		return errOut, nil
	}
	return platformAdapters[acct.Platform].CreativeDelete(a, ctx, acct, def, args)
}

func (a *App) toolCreativeUpload(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	acct, def, errOut := a.resolveAdAccount(ctx, args)
	if errOut != nil {
		return errOut, nil
	}
	return platformAdapters[acct.Platform].CreativeUpload(a, ctx, acct, def, args)
}

func (a *App) toolCreativeAssetStatus(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	acct, def, errOut := a.resolveAdAccount(ctx, args)
	if errOut != nil {
		return errOut, nil
	}
	return platformAdapters[acct.Platform].CreativeAssetStatus(a, ctx, acct, def, args)
}

func (a *App) toolCreativeAssetDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	acct, def, errOut := a.resolveAdAccount(ctx, args)
	if errOut != nil {
		return errOut, nil
	}
	return platformAdapters[acct.Platform].CreativeAssetDelete(a, ctx, acct, def, args)
}

func (a *App) toolCreativeList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	acct, def, errOut := a.resolveAdAccount(ctx, args)
	if errOut != nil {
		return errOut, nil
	}
	return platformAdapters[acct.Platform].CreativeList(a, ctx, acct, def, args)
}

// ─── Audience tools ────────────────────────────────────────────────

func (a *App) toolAudienceList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	acct, def, errOut := a.resolveAdAccount(ctx, args)
	if errOut != nil {
		return errOut, nil
	}
	return platformAdapters[acct.Platform].AudienceList(a, ctx, acct, def, args)
}

func (a *App) toolAudienceCreateCustom(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	acct, def, errOut := a.resolveAdAccount(ctx, args)
	if errOut != nil {
		return errOut, nil
	}
	return platformAdapters[acct.Platform].AudienceCreateCustom(a, ctx, acct, def, args)
}

func (a *App) toolAudienceCreateLookalike(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	acct, def, errOut := a.resolveAdAccount(ctx, args)
	if errOut != nil {
		return errOut, nil
	}
	return platformAdapters[acct.Platform].AudienceCreateLookalike(a, ctx, acct, def, args)
}

// ─── Helpers ───────────────────────────────────────────────────────

func putString(dst map[string]any, dstKey string, src map[string]any, srcKey string) {
	if value := stringArgAny(src, srcKey); value != "" {
		dst[dstKey] = value
	}
}

func metaCTA(args map[string]any, destination string) map[string]any {
	raw := strings.ToLower(stringArgAny(args, "call_to_action"))
	leadFormID := stringArgAny(args, "lead_form_id")
	if raw == "" && destination == "" && leadFormID == "" {
		return nil
	}
	if raw == "" {
		if leadFormID != "" {
			raw = "sign_up"
		} else {
			raw = "learn_more"
		}
	}
	types := map[string]string{
		"learn_more":  "LEARN_MORE",
		"shop_now":    "SHOP_NOW",
		"sign_up":     "SIGN_UP",
		"book_travel": "BOOK_TRAVEL",
		"contact_us":  "CONTACT_US",
		"download":    "DOWNLOAD",
		"get_offer":   "GET_OFFER",
		"get_quote":   "GET_QUOTE",
		"subscribe":   "SUBSCRIBE",
		"watch_more":  "WATCH_MORE",
	}
	ctaType, ok := types[raw]
	if !ok {
		ctaType = strings.ToUpper(raw)
	}
	cta := map[string]any{"type": ctaType}
	value := map[string]any{}
	if destination != "" {
		value["link"] = destination
	}
	if leadFormID != "" {
		value["lead_gen_form_id"] = leadFormID
	}
	if len(value) > 0 {
		cta["value"] = value
	}
	return cta
}

func metaImageLibraryUnavailable(errOut map[string]any) bool {
	content, _ := errOut["content"].([]map[string]any)
	text := ""
	for _, item := range content {
		text += " " + toString(item["text"])
	}
	upper := strings.ToUpper(text)
	return strings.Contains(upper, "(#3)") && strings.Contains(upper, "CAPABILITY")
}

func creativeAssetID(parsed any, kind string) string {
	payload, _ := parsed.(map[string]any)
	if payload == nil {
		return ""
	}
	if id := firstString(payload, "id", "resourceName", "resource_name"); id != "" {
		return id
	}
	if results, ok := payload["results"].([]any); ok && len(results) > 0 {
		if row, ok := results[0].(map[string]any); ok {
			return firstString(row, "resourceName", "resource_name", "id")
		}
	}
	if kind == "image" {
		if images, ok := payload["images"].(map[string]any); ok {
			for _, raw := range images {
				if image, ok := raw.(map[string]any); ok {
					if hash := firstString(image, "hash"); hash != "" {
						return hash
					}
				}
			}
		}
	}
	return ""
}

func recordCreativeAsset(ctx *sdk.AppCtx, args map[string]any, acct *adAccount, assetID, kind string) {
	pid, err := requireProject(ctx, args)
	if err != nil || assetID == "" {
		return
	}
	if _, err := ctx.AppDB().Exec(
		`INSERT INTO ad_resources
		 (project_id, ad_account_id, platform, native_asset_id, provider_type, kind,
		  display_name, status, capabilities_json, metadata_json, managed_by_app, refreshed_at)
		 VALUES (?, ?, ?, ?, ?, 'creative_asset', ?, 'active', '["inspect","delete"]', '{}', 1, CURRENT_TIMESTAMP)
		 ON CONFLICT(project_id, ad_account_id, kind, provider_type, native_asset_id)
		 DO UPDATE SET display_name=excluded.display_name, status='active', refreshed_at=CURRENT_TIMESTAMP`,
		pid, acct.ID, acct.Platform, assetID, kind, stringArgAny(args, "name"),
	); err != nil {
		ctx.Logger().Warn("record creative asset failed", "asset_id", assetID, "error", err)
	}
}

func creativeAssetTracked(ctx *sdk.AppCtx, args map[string]any, acct *adAccount, assetID string) bool {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return false
	}
	var found int
	return ctx.AppDB().QueryRow(
		`SELECT 1 FROM ad_resources
		 WHERE project_id=? AND ad_account_id=? AND platform=? AND native_asset_id=?
		   AND kind='creative_asset' AND status='active'`,
		pid, acct.ID, acct.Platform, assetID,
	).Scan(&found) == nil
}

func deleteCreativeAssetRecord(ctx *sdk.AppCtx, args map[string]any, acct *adAccount, assetID string) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return
	}
	if _, err := ctx.AppDB().Exec(
		`DELETE FROM ad_resources
		 WHERE project_id=? AND ad_account_id=? AND platform=? AND native_asset_id=?
		   AND kind='creative_asset'`,
		pid, acct.ID, acct.Platform, assetID,
	); err != nil {
		ctx.Logger().Warn("delete creative asset record failed", "asset_id", assetID, "error", err)
	}
}

// execOrErr wraps execIntegrationTool to fit the MCP-tool return contract.
func (a *App) execOrErr(ctx *sdk.AppCtx, acct *adAccount, tool string, input map[string]any) (any, error) {
	parsed, errOut := a.execIntegrationTool(ctx, acct, tool, input)
	if errOut != nil {
		return errOut, nil
	}
	return parsed, nil
}

func googleFetchCustomer(ctx *sdk.AppCtx, connID int64, customerID string) (map[string]any, error) {
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "search", map[string]any{
		"customer_id": customerID,
		"query":       "SELECT customer.id, customer.descriptive_name, customer.currency_code, customer.time_zone, customer.manager, customer.status, customer.test_account FROM customer LIMIT 1",
	})
	if err != nil {
		return nil, err
	}
	if res == nil || !res.Success {
		return nil, errors.New("customer detail lookup failed")
	}
	var parsed any
	if err := json.Unmarshal(res.Data, &parsed); err != nil {
		return nil, err
	}
	rows := resultRows(parsed)
	if len(rows) == 0 {
		return map[string]any{"id": customerID, "name": customerID}, nil
	}
	customer := mapAt(rows[0], "customer")
	name := firstString(customer, "descriptiveName", "descriptive_name", "name")
	if name == "" {
		name = customerID
	}
	return map[string]any{
		"id":           customerID,
		"name":         name,
		"currency":     firstString(customer, "currencyCode", "currency_code"),
		"timezone":     firstString(customer, "timeZone", "time_zone"),
		"manager":      googleBool(customer["manager"]),
		"status":       firstString(customer, "status"),
		"test_account": googleBool(customer["testAccount"]) || googleBool(customer["test_account"]),
	}, nil
}

func googleFetchClientAccounts(ctx *sdk.AppCtx, connID int64, managerID string) ([]map[string]any, error) {
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "search", map[string]any{
		"customer_id": managerID,
		"query": "SELECT customer_client.id, customer_client.descriptive_name, customer_client.currency_code, " +
			"customer_client.time_zone, customer_client.manager, customer_client.status, customer_client.test_account, customer_client.level " +
			"FROM customer_client WHERE customer_client.level <= 10",
	})
	if err != nil {
		return nil, err
	}
	if res == nil || !res.Success {
		body := ""
		if res != nil {
			body = string(res.Data)
		}
		return nil, fmt.Errorf("customer hierarchy lookup failed: %s", body)
	}
	var parsed any
	if err := json.Unmarshal(res.Data, &parsed); err != nil {
		return nil, err
	}
	rows := resultRows(parsed)
	accounts := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		client := mapAt(row, "customerClient")
		if len(client) == 0 {
			client = mapAt(row, "customer_client")
		}
		id := googleCustomerID(firstString(client, "id", "clientCustomer", "client_customer"))
		if id == "" {
			continue
		}
		name := firstString(client, "descriptiveName", "descriptive_name", "name")
		if name == "" {
			name = id
		}
		accounts = append(accounts, map[string]any{
			"id":           id,
			"name":         name,
			"currency":     firstString(client, "currencyCode", "currency_code"),
			"timezone":     firstString(client, "timeZone", "time_zone"),
			"manager":      googleBool(client["manager"]),
			"status":       firstString(client, "status"),
			"test_account": googleBool(client["testAccount"]) || googleBool(client["test_account"]),
		})
	}
	return accounts, nil
}

func googleBool(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		parsed, _ := strconv.ParseBool(strings.TrimSpace(typed))
		return parsed
	default:
		return false
	}
}

func googleCustomerID(resourceName string) string {
	s := strings.TrimSpace(resourceName)
	s = strings.TrimPrefix(s, "customers/")
	s = strings.ReplaceAll(s, "-", "")
	return s
}

func googleCampaignStatus(status string) string {
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case "", "PAUSED":
		return "PAUSED"
	case "ACTIVE", "ENABLED":
		return "ENABLED"
	default:
		return strings.ToUpper(strings.TrimSpace(status))
	}
}

func googleValidStatus(status string) bool {
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case "", "PAUSED", "ACTIVE", "ENABLED":
		return true
	default:
		return false
	}
}

func googleNumericID(id string) bool {
	id = strings.TrimSpace(id)
	if id == "" {
		return false
	}
	for _, r := range id {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func googlePayloadScoped(value any, customerID string) bool {
	prefix := "customers/" + customerID + "/"
	switch v := value.(type) {
	case string:
		return !strings.Contains(v, "customers/") || strings.HasPrefix(v, prefix)
	case []any:
		for _, item := range v {
			if !googlePayloadScoped(item, customerID) {
				return false
			}
		}
	case map[string]any:
		for _, item := range v {
			if !googlePayloadScoped(item, customerID) {
				return false
			}
		}
	}
	return true
}

func googleDisplayStatus(status string) string {
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case "ENABLED":
		return "ACTIVE"
	default:
		return strings.ToUpper(strings.TrimSpace(status))
	}
}

func googleBudgetMicros(args map[string]any) string {
	if cents := intArg(args, "daily_budget_cents", 0); cents > 0 {
		return strconv.Itoa(cents * 10000)
	}
	return ""
}

func googleDate(v string) string {
	s := strings.TrimSpace(v)
	if len(s) >= 10 {
		return s[:10]
	}
	return s
}

func googleCampaignResource(customerID, campaignID string) string {
	if strings.HasPrefix(campaignID, "customers/") {
		return campaignID
	}
	return fmt.Sprintf("customers/%s/campaigns/%s", customerID, campaignID)
}

func googleAdGroupResource(customerID, adGroupID string) string {
	if strings.HasPrefix(adGroupID, "customers/") {
		return adGroupID
	}
	return fmt.Sprintf("customers/%s/adGroups/%s", customerID, adGroupID)
}

func googleMaskField(field string) string {
	var out []rune
	for i, r := range field {
		if i > 0 && r >= 'A' && r <= 'Z' {
			out = append(out, '_', r+'a'-'A')
		} else {
			out = append(out, r)
		}
	}
	return strings.ToLower(string(out))
}

func firstResourceName(v any) string {
	for _, row := range resultRows(v) {
		if rn := firstString(row, "resourceName", "resource_name"); rn != "" {
			return rn
		}
	}
	if m := asMap(v); m != nil {
		if rn := firstString(m, "resourceName", "resource_name"); rn != "" {
			return rn
		}
	}
	return ""
}

func normalizeGoogleCampaigns(v any) []map[string]any {
	rows := resultRows(v)
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		campaign := mapAt(row, "campaign")
		budget := mapAt(row, "campaignBudget")
		if len(budget) == 0 {
			budget = mapAt(row, "campaign_budget")
		}
		id := firstString(campaign, "id")
		if id == "" {
			continue
		}
		item := map[string]any{
			"id":        id,
			"name":      firstString(campaign, "name"),
			"status":    googleDisplayStatus(firstString(campaign, "status")),
			"objective": firstString(campaign, "advertisingChannelType", "advertising_channel_type"),
		}
		if rn := firstString(campaign, "resourceName", "resource_name"); rn != "" {
			item["resource_name"] = rn
		}
		if brn := firstString(budget, "resourceName", "resource_name"); brn != "" {
			item["budget_resource_name"] = brn
		}
		if micros := int64ArgAny(firstString(budget, "amountMicros", "amount_micros"), budget["amountMicros"], budget["amount_micros"]); micros > 0 {
			item["daily_budget"] = strconv.FormatInt(micros/10000, 10)
		}
		out = append(out, item)
	}
	return out
}

func performanceResponse(data []map[string]any) map[string]any {
	return map[string]any{
		"data":        data,
		"next_cursor": nil,
	}
}

func normalizeGooglePerformance(acct *adAccount, row map[string]any) map[string]any {
	campaign := mapAt(row, "campaign")
	segments := mapAt(row, "segments")
	metrics := mapAt(row, "metrics")
	customer := mapAt(row, "customer")
	currency := firstString(customer, "currencyCode", "currency_code")
	if currency == "" {
		currency = acct.Currency
	}
	return map[string]any{
		"platform":      acct.Platform,
		"ad_account_id": acct.ID,
		"campaign_id":   firstString(campaign, "id"),
		"campaign_name": firstString(campaign, "name"),
		"date":          firstString(segments, "date"),
		"currency":      currency,
		"timezone":      acct.Timezone,
		"spend_micros": int64ArgAny(
			metrics["costMicros"],
			metrics["cost_micros"],
		),
		"impressions": int64ArgAny(metrics["impressions"]),
		"clicks":      int64ArgAny(metrics["clicks"]),
		"conversions": numericArgAny(metrics["conversions"]),
	}
}

func normalizeMetaPerformance(acct *adAccount, row map[string]any) (map[string]any, error) {
	spendMicros, err := decimalToMicros(row["spend"])
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"platform":      acct.Platform,
		"ad_account_id": acct.ID,
		"campaign_id":   firstString(row, "campaign_id", "campaignId"),
		"campaign_name": firstString(row, "campaign_name", "campaignName"),
		"date":          firstString(row, "date_start", "dateStart"),
		"currency":      acct.Currency,
		"timezone":      acct.Timezone,
		"spend_micros":  spendMicros,
		"impressions":   int64ArgAny(row["impressions"]),
		"clicks":        int64ArgAny(row["clicks"]),
		"conversions":   metaConversionValue(row),
	}, nil
}

func decimalToMicros(value any) (int64, error) {
	raw := strings.TrimSpace(toString(value))
	if raw == "" {
		return 0, nil
	}
	amount, ok := new(big.Rat).SetString(raw)
	if !ok || amount.Sign() < 0 {
		return 0, fmt.Errorf("invalid non-negative spend %q", raw)
	}
	numerator := new(big.Int).Mul(new(big.Int).Set(amount.Num()), big.NewInt(1_000_000))
	denominator := new(big.Int).Set(amount.Denom())
	micros, remainder := new(big.Int), new(big.Int)
	micros.QuoRem(numerator, denominator, remainder)
	if new(big.Int).Mul(remainder, big.NewInt(2)).Cmp(denominator) >= 0 {
		micros.Add(micros, big.NewInt(1))
	}
	if !micros.IsInt64() {
		return 0, fmt.Errorf("spend %q exceeds int64 micros", raw)
	}
	return micros.Int64(), nil
}

func metaConversionValue(row map[string]any) float64 {
	if direct := numericArgAny(row["conversion"]); direct != 0 {
		return direct
	}
	for _, key := range []string{"conversions", "actions"} {
		values := actionValues(row[key])
		if len(values) == 0 {
			continue
		}
		for _, aggregate := range []string{"offsite_conversion", "onsite_conversion", "conversion"} {
			if value, ok := values[aggregate]; ok {
				return value
			}
		}
		total := 0.0
		for actionType, value := range values {
			lower := strings.ToLower(actionType)
			if strings.Contains(lower, "conversion") ||
				strings.Contains(lower, "purchase") ||
				strings.Contains(lower, "lead") ||
				strings.Contains(lower, "complete_registration") {
				total += value
			}
		}
		if total != 0 {
			return total
		}
	}
	return 0
}

func actionValues(value any) map[string]float64 {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make(map[string]float64, len(items))
	for _, item := range items {
		entry := asMap(item)
		actionType := strings.ToLower(firstString(entry, "action_type", "actionType"))
		if actionType != "" {
			out[actionType] = numericArgAny(entry["value"])
		}
	}
	return out
}

func numericArgAny(values ...any) float64 {
	for _, value := range values {
		switch number := value.(type) {
		case float64:
			return number
		case float32:
			return float64(number)
		case int:
			return float64(number)
		case int64:
			return float64(number)
		case json.Number:
			if parsed, err := number.Float64(); err == nil {
				return parsed
			}
		case string:
			if parsed, err := strconv.ParseFloat(strings.TrimSpace(number), 64); err == nil {
				return parsed
			}
		}
	}
	return 0
}

func googleNextPageToken(value any) string {
	payload := asMap(value)
	return firstString(payload, "nextPageToken", "next_page_token")
}

func metaNextCursor(value any) string {
	payload := asMap(value)
	paging := mapAt(payload, "paging")
	cursors := mapAt(paging, "cursors")
	return firstString(cursors, "after")
}

func normalizeGoogleAdGroups(v any) []map[string]any {
	rows := resultRows(v)
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		adGroup := mapAt(row, "adGroup")
		if len(adGroup) == 0 {
			adGroup = mapAt(row, "ad_group")
		}
		campaign := mapAt(row, "campaign")
		id := firstString(adGroup, "id")
		if id == "" {
			continue
		}
		out = append(out, map[string]any{
			"id":          id,
			"name":        firstString(adGroup, "name"),
			"status":      googleDisplayStatus(firstString(adGroup, "status")),
			"campaign_id": firstString(campaign, "id"),
		})
	}
	return out
}

func normalizeGoogleAds(v any) []map[string]any {
	rows := resultRows(v)
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		adGroupAd := mapAt(row, "adGroupAd")
		if len(adGroupAd) == 0 {
			adGroupAd = mapAt(row, "ad_group_ad")
		}
		ad := mapAt(adGroupAd, "ad")
		adGroup := mapAt(row, "adGroup")
		if len(adGroup) == 0 {
			adGroup = mapAt(row, "ad_group")
		}
		id := firstString(ad, "id")
		if id == "" {
			id = firstString(adGroupAd, "resourceName", "resource_name")
		}
		if id == "" {
			continue
		}
		out = append(out, map[string]any{
			"id":        id,
			"name":      firstString(ad, "name"),
			"status":    googleDisplayStatus(firstString(adGroupAd, "status")),
			"adset_id":  firstString(adGroup, "id"),
			"native_id": firstString(adGroupAd, "resourceName", "resource_name"),
		})
	}
	return out
}

func resultRows(v any) []map[string]any {
	m := asMap(v)
	if m == nil {
		return nil
	}
	raw, ok := m["results"]
	if !ok {
		raw = m["data"]
	}
	items, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if row := asMap(item); row != nil {
			out = append(out, row)
		}
	}
	return out
}

func mapAt(m map[string]any, key string) map[string]any {
	if m == nil {
		return nil
	}
	if v := asMap(m[key]); v != nil {
		return v
	}
	return nil
}

func asMap(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return nil
}

func firstString(m map[string]any, keys ...string) string {
	if m == nil {
		return ""
	}
	for _, key := range keys {
		if s := toString(m[key]); s != "" {
			return s
		}
	}
	return ""
}

func int64ArgAny(vals ...any) int64 {
	for _, v := range vals {
		switch n := v.(type) {
		case int64:
			return n
		case int:
			return int64(n)
		case float64:
			return int64(n)
		case string:
			if n, err := strconv.ParseInt(strings.TrimSpace(n), 10, 64); err == nil {
				return n
			}
		}
	}
	return 0
}

type pendingRow struct {
	id              int64
	projectID       string
	platform        string
	integrationSlug string
	connectionID    int64
	status          string
	expiresAt       time.Time
}

func (a *App) getPending(id int64) (*pendingRow, error) {
	var p pendingRow
	err := globalCtx.AppDB().QueryRow(
		`SELECT id, project_id, platform, integration_slug, connection_id, status, expires_at
		 FROM pending_accounts WHERE id=?`,
		id,
	).Scan(&p.id, &p.projectID, &p.platform, &p.integrationSlug, &p.connectionID, &p.status, &p.expiresAt)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (a *App) getPendingForProject(ctx *sdk.AppCtx, args map[string]any, id int64, status string) (*pendingRow, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	row, err := a.getPending(id)
	if err != nil {
		return nil, err
	}
	if row.projectID != pid {
		return nil, errors.New("pending account belongs to another project")
	}
	if row.status != status {
		return nil, fmt.Errorf("pending account is %s, expected %s", row.status, status)
	}
	if !row.expiresAt.After(time.Now().UTC()) {
		_, _ = ctx.AppDB().Exec(
			`UPDATE pending_accounts SET status='expired' WHERE id=? AND status=?`,
			id, status,
		)
		return nil, errors.New("pending account expired")
	}
	return row, nil
}

// ─── Common JSON helpers (mirrors social/main.go) ──────────────────

func mcpError(msg string) map[string]any {
	return map[string]any{
		"isError": true,
		"content": []map[string]any{{"type": "text", "text": msg}},
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
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

func stringArgAny(m map[string]any, keys ...string) string {
	for _, key := range keys {
		switch v := m[key].(type) {
		case string:
			if s := strings.TrimSpace(v); s != "" {
				return s
			}
		case fmt.Stringer:
			if s := strings.TrimSpace(v.String()); s != "" {
				return s
			}
		}
	}
	return ""
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
