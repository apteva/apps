package main

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

const metaMarketingProvider = "meta"

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

func dbMarketingChannelUpsert(db *sql.DB, pid string, storeID, accountID, resourceID int64, name string, publicConfig map[string]any) (*MarketingChannel, error) {
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
		pid, storeID, metaMarketingProvider, accountID, resourceID, strings.TrimSpace(name), jsonText(publicConfig, "{}"))
	if err != nil {
		return nil, err
	}
	return dbMarketingChannelGet(db, pid, storeID, metaMarketingProvider)
}

func dbMarketingChannelSiteStatus(db *sql.DB, pid string, storeID int64, status, message string) error {
	installed := "NULL"
	if status == "installed" {
		installed = "CURRENT_TIMESTAMP"
	}
	_, err := db.Exec(`UPDATE commerce_marketing_channels SET site_tracking_status=?, site_tracking_error=?, installed_at=`+installed+`, updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND store_id=? AND provider=?`,
		status, message, pid, storeID, metaMarketingProvider)
	return err
}

func dbMarketingChannelDisable(db *sql.DB, pid string, storeID int64) (*MarketingChannel, error) {
	result, err := db.Exec(`UPDATE commerce_marketing_channels SET status='disabled', site_tracking_status='not_installed', site_tracking_error='', installed_at=NULL, updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND store_id=? AND provider=?`, pid, storeID, metaMarketingProvider)
	if err != nil {
		return nil, err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return nil, errors.New("Meta marketing channel is not configured")
	}
	return dbMarketingChannelGet(db, pid, storeID, metaMarketingProvider)
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

func metaAccountOptions(ctx *sdk.AppCtx, pid string) (map[string]any, error) {
	result, err := callAds(ctx, pid, "account_list", map[string]any{"platform": "meta", "status": "active"})
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
		context, contextErr := callAds(ctx, pid, "resource_list", map[string]any{
			"ad_account_id": intArg(account, "id"), "kind": "tracking_source", "refresh": true,
		})
		option := copyMap(account)
		if contextErr != nil {
			option["resources"] = []any{}
			option["resource_error"] = contextErr.Error()
		} else {
			resources, _ := context["data"].([]any)
			pixels := make([]any, 0)
			for _, resourceValue := range resources {
				resource, _ := resourceValue.(map[string]any)
				if strArg(resource, "provider_type") == "meta_pixel" && strArg(resource, "status") == "active" {
					pixels = append(pixels, resource)
				}
			}
			option["resources"] = pixels
		}
		options = append(options, option)
	}
	return map[string]any{"ads_available": true, "accounts": options}, nil
}

func (a *App) toolMarketingChannelOptions(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	if _, err := resolveStore(ctx.AppDB(), pid, args); err != nil {
		return nil, err
	}
	options, err := metaAccountOptions(ctx, pid)
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
	channel, err := dbMarketingChannelGet(ctx.AppDB(), pid, store.ID, metaMarketingProvider)
	return map[string]any{"channel": channel}, err
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
	channel, err := dbMarketingChannelGet(ctx.AppDB(), pid, store.ID, metaMarketingProvider)
	if err != nil || channel == nil || channel.Status != "active" {
		return map[string]any{"enabled": false}, err
	}
	return map[string]any{
		"enabled":           true,
		"provider":          metaMarketingProvider,
		"public_id":         strArg(channel.PublicConfig, "public_id"),
		"script_url":        strArg(channel.PublicConfig, "script_url"),
		"data_sharing_mode": channel.DataSharingMode,
	}, nil
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
	resourceID := intArg(args, "tracking_source_resource_id")
	name := strings.TrimSpace(strArg(args, "pixel_name"))
	if resourceID == 0 {
		if name == "" {
			return nil, errors.New("tracking_source_resource_id or pixel_name required")
		}
		created, err := callAds(ctx, pid, "tracking_source_create", map[string]any{
			"ad_account_id": accountID, "name": name, "set_default": true, "reuse_existing": true,
		})
		if err != nil {
			return nil, fmt.Errorf("create Meta Pixel: %w", err)
		}
		resource := unwrap(created, "resource")
		resourceID = intArg(resource, "id")
		name = firstNonEmpty(strArg(resource, "name"), strArg(resource, "display_name"), name)
		if resourceID == 0 {
			return nil, errors.New("Ads did not return the created Pixel resource")
		}
	} else if !hasKey(args, "set_default") || boolArg(args, "set_default") {
		if _, err := callAds(ctx, pid, "resource_set_default", map[string]any{
			"ad_account_id": accountID, "purpose": "conversion_source", "resource_id": resourceID,
		}); err != nil {
			return nil, fmt.Errorf("select default Meta Pixel: %w", err)
		}
	}
	installationResult, err := callAds(ctx, pid, "tracking_source_installation_get", map[string]any{
		"ad_account_id": accountID, "tracking_source_resource_id": resourceID,
	})
	if err != nil {
		return nil, fmt.Errorf("resolve Meta Pixel installation: %w", err)
	}
	resource := unwrap(installationResult, "resource")
	installation := unwrap(installationResult, "installation")
	if publicID := strArg(installation, "public_id"); publicID == "" {
		return nil, errors.New("Ads returned no browser-public Meta Pixel id")
	}
	name = firstNonEmpty(strArg(resource, "name"), strArg(resource, "display_name"), name)
	publicConfig := map[string]any{
		"public_id": strArg(installation, "public_id"), "script_url": strArg(installation, "script_url"),
		"script_origins": installation["script_origins"], "connect_origins": installation["connect_origins"],
		"image_origins": installation["image_origins"],
	}
	channel, err := dbMarketingChannelUpsert(ctx.AppDB(), pid, store.ID, accountID, resourceID, name, publicConfig)
	if err != nil {
		return nil, err
	}
	var storefront *StorefrontStatus
	warning := ""
	if intArg(store.Metadata, "content_site_id") != 0 {
		storefront, err = a.configureContentStorefront(ctx, pid, store, intArg(store.Metadata, "content_site_id"))
		if err != nil {
			warning = err.Error()
			_ = dbMarketingChannelSiteStatus(ctx.AppDB(), pid, store.ID, "error", err.Error())
		}
		channel, _ = dbMarketingChannelGet(ctx.AppDB(), pid, store.ID, metaMarketingProvider)
	}
	ctx.Emit("commerce.marketing_channel.updated", map[string]any{"store_id": store.ID, "provider": metaMarketingProvider, "status": "active"})
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
	channel, err := dbMarketingChannelDisable(ctx.AppDB(), pid, store.ID)
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
	ctx.Emit("commerce.marketing_channel.updated", map[string]any{"store_id": store.ID, "provider": metaMarketingProvider, "status": "disabled"})
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
