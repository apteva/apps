package main

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

const (
	metaMarketingProvider   = "meta"
	googleMarketingProvider = "google"
)

// marketingProviderResourceTypes maps an Ads account platform to the
// tracking-source provider_type commerce can install on a storefront.
// Adding a platform here is what makes it offerable by _options.
var marketingProviderResourceTypes = map[string]string{
	metaMarketingProvider:   "meta_pixel",
	googleMarketingProvider: "google_conversion_action",
}

// marketingProviderFor resolves an Ads account platform to a supported
// marketing provider, or "" when commerce cannot install tracking for it.
func marketingProviderFor(platform string) string {
	platform = strings.ToLower(strings.TrimSpace(platform))
	if _, ok := marketingProviderResourceTypes[platform]; ok {
		return platform
	}
	return ""
}

type MarketingChannel struct {
	ID                       int64          `json:"id"`
	StoreID                  int64          `json:"store_id"`
	Provider                 string         `json:"provider"`
	Status                   string         `json:"status"`
	AdAccountID              int64          `json:"ad_account_id"`
	TrackingSourceResourceID int64          `json:"tracking_source_resource_id"`
	TrackingSourceName       string         `json:"tracking_source_name"`
	PublicConfig             map[string]any `json:"public_config,omitempty"`
	DataSharingMode          string         `json:"data_sharing_mode"`
	SiteTrackingStatus       string         `json:"site_tracking_status"`
	SiteTrackingError        string         `json:"site_tracking_error,omitempty"`
	InstalledAt              string         `json:"installed_at,omitempty"`
	CreatedAt                string         `json:"created_at"`
	UpdatedAt                string         `json:"updated_at"`
}

func marketingChannelProps() map[string]any {
	return map[string]any{
		"store_id":                    typ("integer"),
		"ad_account_id":               typ("integer"),
		"tracking_source_resource_id": typ("integer"),
		"provider":                    typ("string"),
		"tracking_source_name":        typ("string"),
		"pixel_name":                  typ("string"),
		"set_default":                 typ("boolean"),
	}
}

func scanMarketingChannel(row interface{ Scan(...any) error }) (*MarketingChannel, error) {
	channel := &MarketingChannel{}
	var publicConfig string
	err := row.Scan(
		&channel.ID, &channel.StoreID, &channel.Provider, &channel.Status,
		&channel.AdAccountID, &channel.TrackingSourceResourceID, &channel.TrackingSourceName,
		&publicConfig, &channel.DataSharingMode, &channel.SiteTrackingStatus,
		&channel.SiteTrackingError, &channel.InstalledAt, &channel.CreatedAt, &channel.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	channel.PublicConfig = jsonMap(publicConfig)
	return channel, nil
}

func marketingChannelSelect() string {
	return `SELECT id, store_id, provider, status, ad_account_id,
		tracking_source_resource_id, tracking_source_name, public_config_json,
		data_sharing_mode, site_tracking_status, COALESCE(site_tracking_error,''),
		COALESCE(installed_at,''), created_at, updated_at
		FROM commerce_marketing_channels`
}

func dbMarketingChannelGet(db *sql.DB, pid string, storeID int64, provider string) (*MarketingChannel, error) {
	channel, err := scanMarketingChannel(db.QueryRow(marketingChannelSelect()+` WHERE project_id=? AND store_id=? AND provider=?`, pid, storeID, provider))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return channel, err
}

func dbMarketingChannelList(db *sql.DB, pid string, storeID int64) ([]*MarketingChannel, error) {
	rows, err := db.Query(marketingChannelSelect()+` WHERE project_id=? AND store_id=? ORDER BY provider`, pid, storeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	channels := make([]*MarketingChannel, 0, 2)
	for rows.Next() {
		channel, err := scanMarketingChannel(rows)
		if err != nil {
			return nil, err
		}
		channels = append(channels, channel)
	}
	return channels, rows.Err()
}

func dbMarketingChannelUpsert(db *sql.DB, pid string, storeID int64, provider string, accountID, resourceID int64, name string, publicConfig map[string]any) (*MarketingChannel, error) {
	_, err := db.Exec(`INSERT INTO commerce_marketing_channels
		(project_id, store_id, provider, status, ad_account_id, tracking_source_resource_id,
		 tracking_source_name, public_config_json, data_sharing_mode, site_tracking_status,
		 site_tracking_error, installed_at, updated_at)
		VALUES(?, ?, ?, 'active', ?, ?, ?, ?, 'browser', 'not_installed', '', NULL, CURRENT_TIMESTAMP)
		ON CONFLICT(project_id, store_id, provider) DO UPDATE SET
		 status='active', ad_account_id=excluded.ad_account_id,
		 tracking_source_resource_id=excluded.tracking_source_resource_id,
		 tracking_source_name=excluded.tracking_source_name,
		 public_config_json=excluded.public_config_json, data_sharing_mode='browser',
		 site_tracking_status='not_installed', site_tracking_error='', installed_at=NULL,
		 updated_at=CURRENT_TIMESTAMP`,
		pid, storeID, provider, accountID, resourceID, strings.TrimSpace(name), jsonText(publicConfig, "{}"))
	if err != nil {
		return nil, err
	}
	return dbMarketingChannelGet(db, pid, storeID, provider)
}

func dbMarketingChannelSiteStatus(db *sql.DB, pid string, storeID int64, provider, status, message string) error {
	installed := "NULL"
	if status == "installed" {
		installed = "CURRENT_TIMESTAMP"
	}
	_, err := db.Exec(`UPDATE commerce_marketing_channels SET site_tracking_status=?, site_tracking_error=?, installed_at=`+installed+`, updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND store_id=? AND provider=?`,
		status, message, pid, storeID, provider)
	return err
}

func dbMarketingChannelDisable(db *sql.DB, pid string, storeID int64, provider string) (*MarketingChannel, error) {
	result, err := db.Exec(`UPDATE commerce_marketing_channels SET status='disabled', site_tracking_status='not_installed', site_tracking_error='', installed_at=NULL, updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND store_id=? AND provider=?`, pid, storeID, provider)
	if err != nil {
		return nil, err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return nil, fmt.Errorf("%s marketing channel is not configured", provider)
	}
	return dbMarketingChannelGet(db, pid, storeID, provider)
}

func appToolError(result map[string]any) error {
	if result == nil || !boolArg(result, "isError") {
		return nil
	}
	message := firstNonEmpty(strArg(result, "message"), strArg(result, "error"))
	if message == "" {
		if content, ok := result["content"].([]any); ok && len(content) > 0 {
			if item, ok := content[0].(map[string]any); ok {
				message = strArg(item, "text")
			}
		}
	}
	if message == "" {
		message = "Ads returned an error"
	}
	return errors.New(message)
}

func callAds(ctx *sdk.AppCtx, pid, tool string, input map[string]any) (map[string]any, error) {
	if ctx.PlatformAPI() == nil {
		return nil, errors.New("Ads is not installed or available")
	}
	args := copyMap(input)
	args["_project_id"] = pid
	var result map[string]any
	if err := ctx.PlatformAPI().CallAppResult("ads", tool, args, &result); err != nil {
		return nil, err
	}
	if err := appToolError(result); err != nil {
		return nil, err
	}
	return result, nil
}

func marketingAccountOptions(ctx *sdk.AppCtx, pid string) (map[string]any, error) {
	// No platform filter: every active Ads account is a candidate, and the
	// account's own platform decides which tracking sources commerce can use.
	result, err := callAds(ctx, pid, "account_list", map[string]any{"status": "active"})
	if err != nil {
		return nil, err
	}
	accounts, _ := result["accounts"].([]any)
	options := make([]any, 0, len(accounts))
	for _, value := range accounts {
		account, ok := value.(map[string]any)
		if !ok {
			continue
		}
		provider := marketingProviderFor(strArg(account, "platform"))
		if provider == "" {
			// An Ads platform commerce cannot install storefront tracking for.
			continue
		}
		wantType := marketingProviderResourceTypes[provider]
		context, contextErr := callAds(ctx, pid, "resource_list", map[string]any{
			"ad_account_id": intArg(account, "id"), "kind": "tracking_source", "refresh": true,
		})
		option := copyMap(account)
		option["provider"] = provider
		option["tracking_source_type"] = wantType
		if contextErr != nil {
			option["resources"] = []any{}
			option["resource_error"] = contextErr.Error()
		} else {
			resources, _ := context["data"].([]any)
			matched := make([]any, 0)
			for _, resourceValue := range resources {
				resource, _ := resourceValue.(map[string]any)
				if strArg(resource, "provider_type") == wantType && strArg(resource, "status") == "active" {
					matched = append(matched, resource)
				}
			}
			option["resources"] = matched
		}
		options = append(options, option)
	}
	return map[string]any{"ads_available": true, "accounts": options}, nil
}

// resolveMarketingProvider determines which provider an ad account belongs to.
// Ads exposes no account_get, so the account is located in account_list; an
// explicit hint is validated against the account rather than trusted.
func resolveMarketingProvider(ctx *sdk.AppCtx, pid string, accountID int64, hint string) (string, error) {
	result, err := callAds(ctx, pid, "account_list", map[string]any{"status": "active"})
	if err != nil {
		return "", fmt.Errorf("resolve ad account %d: %w", accountID, err)
	}
	accounts, _ := result["accounts"].([]any)
	platform := ""
	found := false
	for _, value := range accounts {
		account, ok := value.(map[string]any)
		if !ok || intArg(account, "id") != accountID {
			continue
		}
		platform = strArg(account, "platform")
		found = true
		break
	}
	if !found {
		return "", fmt.Errorf("ad account %d is not an active Ads account", accountID)
	}
	provider := marketingProviderFor(platform)
	if provider == "" {
		return "", fmt.Errorf("ad account %d is on platform %q, which commerce cannot install storefront tracking for", accountID, platform)
	}
	if hint = strings.ToLower(strings.TrimSpace(hint)); hint != "" && hint != provider {
		return "", fmt.Errorf("ad account %d is a %s account, not %s", accountID, provider, hint)
	}
	return provider, nil
}

func (a *App) toolMarketingChannelOptions(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	if _, err := resolveStore(ctx.AppDB(), pid, args); err != nil {
		return nil, err
	}
	options, err := marketingAccountOptions(ctx, pid)
	if err != nil {
		return map[string]any{"ads_available": false, "accounts": []any{}, "error": err.Error()}, nil
	}
	return options, nil
}

func (a *App) toolMarketingChannelGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	store, err := resolveStore(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	channels, err := dbMarketingChannelList(ctx.AppDB(), pid, store.ID)
	if err != nil {
		return nil, err
	}
	// "channel" stays single-valued for pre-0.9.0 callers: the requested
	// provider, else Meta, else whatever single channel the store has.
	wanted := marketingProviderFor(strArg(args, "provider"))
	var selected *MarketingChannel
	for _, channel := range channels {
		switch {
		case wanted != "" && channel.Provider == wanted:
			selected = channel
		case wanted == "" && channel.Provider == metaMarketingProvider:
			selected = channel
		}
	}
	if selected == nil && wanted == "" && len(channels) == 1 {
		selected = channels[0]
	}
	return map[string]any{"channel": selected, "channels": channels}, nil
}

func (a *App) toolMarketingChannelPublicGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	store, err := resolveStore(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	channels, err := dbMarketingChannelList(ctx.AppDB(), pid, store.ID)
	if err != nil {
		return map[string]any{"enabled": false}, err
	}
	views := make([]any, 0, len(channels))
	var meta map[string]any
	for _, channel := range channels {
		if channel.Status != "active" {
			continue
		}
		view := marketingChannelPublicView(channel)
		if view == nil {
			continue
		}
		views = append(views, view)
		if channel.Provider == metaMarketingProvider {
			meta = view
		}
	}
	result := map[string]any{"enabled": len(views) > 0, "channels": views}
	// Storefronts published before 0.9.0 embed a snapshot of store.js that
	// reads public_id/script_url off the top level. Keep mirroring the Meta
	// channel there so those sites keep working until they are republished.
	if meta != nil {
		for key, value := range meta {
			result[key] = value
		}
	}
	return result, nil
}

// marketingChannelPublicViewKeys is the uniform browser-public shape. Every
// provider answers with the same keys; provider extras are appended from
// marketingChannelPublicExtras and nothing outside these lists is exposed.
var marketingChannelPublicExtras = []string{"conversion_id", "conversion_label", "send_to"}

// marketingChannelPublicView projects a channel to the browser-safe subset.
// It allow-lists keys rather than passing public_config through, because
// public_config now holds the entire Ads installation payload.
func marketingChannelPublicView(channel *MarketingChannel) map[string]any {
	publicID := strArg(channel.PublicConfig, "public_id")
	if publicID == "" {
		return nil
	}
	view := map[string]any{
		"provider":          channel.Provider,
		"public_id":         publicID,
		"script_url":        strArg(channel.PublicConfig, "script_url"),
		"data_sharing_mode": channel.DataSharingMode,
	}
	for _, key := range marketingChannelPublicExtras {
		if value := strArg(channel.PublicConfig, key); value != "" {
			view[key] = value
		}
	}
	return view
}

func (a *App) toolMarketingChannelConfigure(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	store, err := resolveStore(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	accountID := intArg(args, "ad_account_id")
	if accountID == 0 {
		return nil, errors.New("ad_account_id required")
	}
	provider, err := resolveMarketingProvider(ctx, pid, accountID, strArg(args, "provider"))
	if err != nil {
		return nil, err
	}
	resourceID := intArg(args, "tracking_source_resource_id")
	// pixel_name is the pre-0.9.0 Meta-only spelling, kept as an alias.
	name := strings.TrimSpace(firstNonEmpty(strArg(args, "tracking_source_name"), strArg(args, "pixel_name")))
	if resourceID == 0 {
		if name == "" {
			return nil, errors.New("tracking_source_resource_id or tracking_source_name required")
		}
		created, err := callAds(ctx, pid, "tracking_source_create", map[string]any{
			"ad_account_id": accountID, "name": name, "set_default": true, "reuse_existing": true,
			"provider_type": marketingProviderResourceTypes[provider], "create": true,
		})
		if err != nil {
			return nil, fmt.Errorf("create %s tracking source: %w", provider, err)
		}
		resource := unwrap(created, "resource")
		resourceID = intArg(resource, "id")
		name = firstNonEmpty(strArg(resource, "name"), strArg(resource, "display_name"), name)
		if resourceID == 0 {
			return nil, fmt.Errorf("Ads did not return the created %s tracking source", provider)
		}
	} else if !hasKey(args, "set_default") || boolArg(args, "set_default") {
		if _, err := callAds(ctx, pid, "resource_set_default", map[string]any{
			"ad_account_id": accountID, "purpose": "conversion_source", "resource_id": resourceID,
		}); err != nil {
			return nil, fmt.Errorf("select default %s tracking source: %w", provider, err)
		}
	}
	installationResult, err := callAds(ctx, pid, "tracking_source_installation_get", map[string]any{
		"ad_account_id": accountID, "tracking_source_resource_id": resourceID,
	})
	if err != nil {
		return nil, fmt.Errorf("resolve %s tracking source installation: %w", provider, err)
	}
	resource := unwrap(installationResult, "resource")
	installation := unwrap(installationResult, "installation")
	if publicID := strArg(installation, "public_id"); publicID == "" {
		return nil, fmt.Errorf("Ads returned no browser-public id for the %s tracking source", provider)
	}
	// Google can return an action whose tag snippet is not minted yet. Storing
	// that as active would publish a channel that silently emits nothing.
	if hasKey(installation, "snippet_available") && !boolArg(installation, "snippet_available") {
		return nil, fmt.Errorf("%s tracking source %d has no tag snippet yet; retry once Google has generated it", provider, resourceID)
	}
	name = firstNonEmpty(strArg(resource, "name"), strArg(resource, "display_name"), name)
	// Persist the whole installation payload. Ads already tags it with the
	// provider and returns provider-specific keys (Google: conversion_id,
	// conversion_label, send_to, global_site_tag, event_snippet), so cherry
	// picking Meta's fields is what made this surface Meta-only.
	publicConfig := copyMap(installation)
	publicConfig["provider"] = provider
	channel, err := dbMarketingChannelUpsert(ctx.AppDB(), pid, store.ID, provider, accountID, resourceID, name, publicConfig)
	if err != nil {
		return nil, err
	}
	var storefront *StorefrontStatus
	warning := ""
	if intArg(store.Metadata, "content_site_id") != 0 {
		storefront, err = a.configureContentStorefront(ctx, pid, store, intArg(store.Metadata, "content_site_id"))
		if err != nil {
			warning = err.Error()
			_ = dbMarketingChannelSiteStatus(ctx.AppDB(), pid, store.ID, provider, "error", err.Error())
		}
		channel, _ = dbMarketingChannelGet(ctx.AppDB(), pid, store.ID, provider)
	}
	ctx.Emit("commerce.marketing_channel.updated", map[string]any{"store_id": store.ID, "provider": provider, "status": "active"})
	return map[string]any{"channel": channel, "storefront": storefront, "warning": warning}, nil
}

func (a *App) toolMarketingChannelDisconnect(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	store, err := resolveStore(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	provider := marketingProviderFor(strArg(args, "provider"))
	if provider == "" {
		provider = metaMarketingProvider
	}
	channel, err := dbMarketingChannelDisable(ctx.AppDB(), pid, store.ID, provider)
	if err != nil {
		return nil, err
	}
	var storefront *StorefrontStatus
	warning := ""
	if intArg(store.Metadata, "content_site_id") != 0 {
		storefront, err = a.configureContentStorefront(ctx, pid, store, intArg(store.Metadata, "content_site_id"))
		if err != nil {
			warning = err.Error()
		}
	}
	ctx.Emit("commerce.marketing_channel.updated", map[string]any{"store_id": store.ID, "provider": provider, "status": "disabled"})
	return map[string]any{"channel": channel, "storefront": storefront, "warning": warning}, nil
}

func (a *App) handleMarketingChannel(w http.ResponseWriter, r *http.Request) {
	ctx, _, err := requestAppContext(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	args := queryArgs(r)
	switch r.Method {
	case http.MethodGet:
		channelResult, channelErr := a.toolMarketingChannelGet(ctx, args)
		if channelErr != nil {
			httpResult(w, nil, channelErr)
			return
		}
		options, _ := a.toolMarketingChannelOptions(ctx, args)
		result := channelResult.(map[string]any)
		result["options"] = options
		httpJSON(w, result)
	case http.MethodPost:
		if err := readJSON(r, &args); err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		result, err := a.toolMarketingChannelConfigure(ctx, args)
		httpResult(w, result, err)
	case http.MethodDelete:
		if r.Body != nil && r.ContentLength != 0 {
			if err := readJSON(r, &args); err != nil {
				httpErr(w, http.StatusBadRequest, err.Error())
				return
			}
		}
		result, err := a.toolMarketingChannelDisconnect(ctx, args)
		httpResult(w, result, err)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}
