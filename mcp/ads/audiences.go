package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

const (
	audienceBatchMeta   = 1000
	audienceBatchGoogle = 1000
	audienceBatchX      = 2500
	audienceBatchReddit = 2500
	audienceMaxRows     = 250000
)

type audienceCapability struct {
	Kinds      []string
	Operations []string
	SyncModes  []string
	Sources    []string
}

var audienceCapabilities = map[string]audienceCapability{
	"meta": {
		Kinds:      []string{"customer_list", "website", "app_activity", "engagement", "lookalike"},
		Operations: []string{"list", "get", "create", "update", "delete", "members_sync", "usage", "refresh"},
		SyncModes:  []string{"add", "remove"}, Sources: []string{"storage", "crm_segment"},
	},
	"google": {
		Kinds:      []string{"customer_list", "website", "app_activity"},
		Operations: []string{"list", "get", "create", "update", "delete", "members_sync", "usage", "refresh"},
		SyncModes:  []string{"add", "remove"}, Sources: []string{"storage", "crm_segment"},
	},
	"x": {
		Kinds:      []string{"customer_list", "website", "app_activity"},
		Operations: []string{"list", "get", "create", "update", "delete", "members_sync", "usage", "refresh"},
		SyncModes:  []string{"add", "remove"}, Sources: []string{"storage", "crm_segment"},
	},
	"reddit": {
		Kinds:      []string{"customer_list", "saved_targeting"},
		Operations: []string{"list", "get", "create", "update", "delete", "members_sync", "usage", "refresh"},
		SyncModes:  []string{"add", "remove"}, Sources: []string{"storage", "crm_segment"},
	},
}

type audienceJob struct {
	ID                 int64
	ProjectID          string
	AdAccountID        int64
	AudienceResourceID int64
	NativeAudienceID   string
	Operation          string
	SourceKind         string
	SourceRef          string
	Mapping            map[string]string
	Consent            map[string]any
	Status             string
	TotalRows          int
	ProcessedRows      int
	AcceptedRows       int
	RejectedRows       int
	SourceChecksum     string
	ProviderRequestID  string
	LastError          string
	Attempts           int
	CreatedAt          string
	UpdatedAt          string
}

func (j audienceJob) response() map[string]any {
	return map[string]any{
		"id": j.ID, "ad_account_id": j.AdAccountID, "audience_resource_id": nullableInt64(j.AudienceResourceID),
		"operation": j.Operation, "source": map[string]any{"kind": j.SourceKind, "ref": j.SourceRef},
		"status": j.Status, "total_rows": j.TotalRows, "processed_rows": j.ProcessedRows,
		"accepted_rows": j.AcceptedRows, "rejected_rows": j.RejectedRows,
		"source_checksum": j.SourceChecksum, "provider_request_id": j.ProviderRequestID,
		"error": j.LastError, "attempts": j.Attempts, "created_at": j.CreatedAt, "updated_at": j.UpdatedAt,
	}
}

func (a *App) toolAudienceCapabilitiesGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	acct, _, errOut := a.resolveAdAccount(ctx, args)
	if errOut != nil {
		return errOut, nil
	}
	capability := audienceCapabilities[acct.Platform]
	return map[string]any{
		"platform": acct.Platform, "kinds": capability.Kinds, "operations": capability.Operations,
		"sync_modes": capability.SyncModes, "sources": capability.Sources,
		"privacy": map[string]any{"normalizes": true, "hashes_before_provider": true, "persists_identifiers": false},
	}, nil
}

func (a *App) audienceList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	acct, _, errOut := a.resolveAdAccount(ctx, args)
	if errOut != nil {
		return errOut, nil
	}
	refresh := true
	if value, ok := args["refresh"].(bool); ok {
		refresh = value
	}
	errorsByKind := map[string]string{}
	if refresh {
		errorsByKind = a.refreshResourceKinds(ctx, acct, []string{resourceAudience})
	}
	resources, err := a.listResources(ctx, acct, resourceAudience)
	if err != nil {
		return nil, err
	}
	items := make([]map[string]any, 0, len(resources))
	for _, resource := range resources {
		items = append(items, normalizedAudience(acct.Platform, resource))
	}
	return map[string]any{"data": items, "refresh_errors": errorsByKind}, nil
}

func normalizedAudience(platform string, resource adResource) map[string]any {
	metadata := resource.Metadata
	typeName := normalizedAudienceType(firstString(metadata, "subtype", "type", "list_type"))
	return map[string]any{
		"id": resource.ID, "ad_account_id": resource.AdAccountID, "platform": platform,
		"native_id": resource.NativeID, "name": resource.DisplayName, "type": typeName,
		"status": resource.Status, "capabilities": resource.Capabilities, "metadata": metadata,
		"managed_by_app": resource.ManagedByApp, "refreshed_at": resource.RefreshedAt,
	}
}

func normalizedAudienceType(nativeType string) string {
	typeName := strings.ToLower(nativeType)
	switch {
	case strings.Contains(typeName, "lookalike"), strings.Contains(typeName, "similar"):
		typeName = "lookalike"
	case strings.Contains(typeName, "website"), strings.Contains(typeName, "rule"):
		typeName = "website"
	case strings.Contains(typeName, "app"):
		typeName = "app_activity"
	case strings.Contains(typeName, "engagement"):
		typeName = "engagement"
	case strings.Contains(typeName, "saved"):
		typeName = "saved_targeting"
	default:
		typeName = "customer_list"
	}
	return typeName
}

func (a *App) toolAudienceGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	acct, _, errOut := a.resolveAdAccount(ctx, args)
	if errOut != nil {
		return errOut, nil
	}
	resource, err := a.getResource(ctx, acct, int64(intArg(args, "audience_id", 0)))
	if err != nil || resource.Kind != resourceAudience {
		return mcpError("audience not found in this ad account"), nil
	}
	return normalizedAudience(acct.Platform, *resource), nil
}

func (a *App) audienceCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	acct, def, errOut := a.resolveAdAccount(ctx, args)
	if errOut != nil {
		return errOut, nil
	}
	kind := strings.ToLower(strings.TrimSpace(stringArgAny(args, "type", "kind", "subtype")))
	if kind == "" {
		kind = "customer_list"
	}
	if !containsString(audienceCapabilities[acct.Platform].Kinds, kind) {
		return mcpError("audience type " + kind + " is not supported by " + acct.Platform), nil
	}
	name := strings.TrimSpace(stringArgAny(args, "name"))
	if name == "" {
		return mcpError("name required"), nil
	}
	var parsed any
	var providerErr map[string]any
	switch acct.Platform {
	case "meta":
		if kind == "engagement" {
			sourceID := int64(intArg(args, "source_resource_id", 0))
			source, err := a.getResource(ctx, acct, sourceID)
			if err != nil || source.Kind != resourceIdentity || source.ProviderType != "facebook_page" || source.Status != "active" {
				return mcpError("source_resource_id must reference an active Facebook Page in this ad account"), nil
			}
			retentionDays := intArg(args, "retention_days", 365)
			if retentionDays < 1 || retentionDays > 365 {
				return mcpError("retention_days must be between 1 and 365 for Meta Page engagement audiences"), nil
			}
			input := map[string]any{
				def.AccountIDInputField: acct.NativeAccountID,
				"name":                  name,
				"prefill":               true,
				"rule": map[string]any{"inclusions": map[string]any{
					"operator": "or",
					"rules": []map[string]any{{
						"event_sources":     []map[string]any{{"id": source.NativeID, "type": "page"}},
						"retention_seconds": retentionDays * 86400,
						"filter": map[string]any{"operator": "and", "filters": []map[string]any{{
							"field": "event", "operator": "eq", "value": "page_engaged",
						}}},
					}},
				}},
			}
			if description := stringArgAny(args, "description"); description != "" {
				input["description"] = description
			}
			parsed, providerErr = a.execIntegrationTool(ctx, acct, def.AudienceCreateCustomTool, input)
			break
		}
		legacy := cloneMap(args)
		legacy["subtype"] = metaAudienceSubtype(kind)
		if kind == "lookalike" {
			nativeSource, legacyNativeID := args["source_audience_id"].(string)
			sourceID := int64(intArg(args, "source_audience_id", 0))
			if !legacyNativeID && sourceID > 0 {
				source, err := a.getResource(ctx, acct, sourceID)
				if err != nil || source.Kind != resourceAudience || normalizedAudienceType(firstString(source.Metadata, "subtype", "type", "list_type")) == "lookalike" {
					return mcpError("source_audience_id must reference a source audience in this ad account"), nil
				}
				legacy["source_audience_id"] = source.NativeID
			} else if strings.TrimSpace(nativeSource) == "" {
				return mcpError("source_audience_id required"), nil
			}
			parsed, _ = platformAdapters[acct.Platform].AudienceCreateLookalike(a, ctx, acct, def, legacy)
		} else {
			parsed, _ = platformAdapters[acct.Platform].AudienceCreateCustom(a, ctx, acct, def, legacy)
		}
		providerErr = mcpResultError(parsed)
	case "google":
		if kind != "customer_list" {
			return mcpError("Google rule-based audiences are discovered and targetable but creation is provider-managed"), nil
		}
		userList := map[string]any{
			"displayName": name, "description": stringArgAny(args, "description"),
			"ingestedUserListInfo": map[string]any{"uploadKeyTypes": []string{"CONTACT_ID"}, "contactIdInfo": map[string]any{"dataSourceType": "DATA_SOURCE_TYPE_FIRST_PARTY"}},
		}
		if days := intArg(args, "retention_days", 0); days > 0 {
			userList["membershipDuration"] = fmt.Sprintf("%ds", days*86400)
		}
		input := googleDataManagerResourceInput(acct)
		input["account_type"] = "GOOGLE_ADS"
		input["account_id"] = acct.NativeAccountID
		input["userList"] = userList
		parsed, providerErr = a.execIntegrationTool(ctx, acct, "data_manager_user_list_create", input)
	case "x":
		legacy := cloneMap(args)
		legacy["subtype"] = "CRM"
		parsed, _ = platformAdapters[acct.Platform].AudienceCreateCustom(a, ctx, acct, def, legacy)
		providerErr = mcpResultError(parsed)
	case "reddit":
		if kind == "saved_targeting" {
			parsed, providerErr = a.execIntegrationTool(ctx, acct, "create_saved_audience", map[string]any{"ad_account_id": acct.NativeAccountID, "data": map[string]any{"name": name, "description": stringArgAny(args, "description"), "targeting": args["targeting"]}})
		} else {
			parsed, _ = platformAdapters[acct.Platform].AudienceCreateCustom(a, ctx, acct, def, args)
			providerErr = mcpResultError(parsed)
		}
	}
	if providerErr != nil {
		return providerErr, nil
	}
	nativeID := audienceNativeID(parsed)
	if nativeID == "" {
		return mcpError("provider created the audience without returning an id"), nil
	}
	providerType := acct.Platform + "_audience"
	if acct.Platform == "google" {
		providerType = "google_user_list"
	}
	if kind == "saved_targeting" {
		providerType = "reddit_saved_audience"
	}
	resource, err := a.upsertResource(ctx, acct, discoveredResource{
		Kind: resourceAudience, ProviderType: providerType, NativeID: nativeID, DisplayName: name,
		Status: "active", Capabilities: audienceResourceCapabilities(acct.Platform, kind),
		Metadata: map[string]any{
			"type": kind, "description": stringArgAny(args, "description"),
			"retention_days": intArg(args, "retention_days", 0), "source_resource_id": intArg(args, "source_resource_id", 0),
		}, ManagedByApp: true,
	})
	if err != nil {
		return nil, err
	}
	a.emitAudienceEvent(ctx, acct, "audience.created", resource, 0, "completed")
	return normalizedAudience(acct.Platform, *resource), nil
}

func (a *App) toolAudienceUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	acct, _, resource, errOut := a.resolveAudience(ctx, args)
	if errOut != nil {
		return errOut, nil
	}
	input := map[string]any{}
	name := strings.TrimSpace(stringArgAny(args, "name"))
	description := stringArgAny(args, "description")
	var parsed any
	switch acct.Platform {
	case "meta":
		input = map[string]any{"audience_id": resource.NativeID, "name": name, "description": description}
		if days := intArg(args, "retention_days", 0); days > 0 {
			input["retention_days"] = days
		}
		parsed, errOut = a.execIntegrationTool(ctx, acct, "audience_update", input)
	case "google":
		userList := map[string]any{"name": fmt.Sprintf("accountTypes/GOOGLE_ADS/accounts/%s/userLists/%s", acct.NativeAccountID, resource.NativeID)}
		masks := []string{}
		if name != "" {
			userList["displayName"] = name
			masks = append(masks, "display_name")
		}
		if _, exists := args["description"]; exists {
			userList["description"] = description
			masks = append(masks, "description")
		}
		if len(masks) == 0 {
			return mcpError("name or description required"), nil
		}
		input = googleDataManagerResourceInput(acct)
		input["account_type"] = "GOOGLE_ADS"
		input["account_id"] = acct.NativeAccountID
		input["user_list_id"] = resource.NativeID
		input["userList"] = userList
		input["updateMask"] = strings.Join(masks, ",")
		parsed, errOut = a.execIntegrationTool(ctx, acct, "data_manager_user_list_update", input)
	case "x":
		parsed, errOut = a.execIntegrationTool(ctx, acct, "update_custom_audience", map[string]any{"account_id": acct.NativeAccountID, "custom_audience_id": resource.NativeID, "name": name, "description": description})
	case "reddit":
		if resource.ProviderType != "reddit_saved_audience" {
			return mcpError("Reddit customer-list metadata cannot be updated after creation"), nil
		}
		parsed, errOut = a.execIntegrationTool(ctx, acct, "update_saved_audience", map[string]any{"saved_audience_id": resource.NativeID, "data": map[string]any{"name": name, "description": description, "targeting": args["targeting"]}})
	}
	if errOut != nil {
		return errOut, nil
	}
	if name == "" {
		name = resource.DisplayName
	}
	resource, err := a.upsertResource(ctx, acct, discoveredResource{Kind: resourceAudience, ProviderType: resource.ProviderType, NativeID: resource.NativeID, DisplayName: name, Status: "active", Capabilities: resource.Capabilities, Metadata: mergeAudienceMetadata(resource.Metadata, args), ManagedByApp: resource.ManagedByApp})
	if err != nil {
		return nil, err
	}
	a.emitAudienceEvent(ctx, acct, "audience.updated", resource, 0, "completed")
	return map[string]any{"audience": normalizedAudience(acct.Platform, *resource), "provider": parsed}, nil
}

func (a *App) toolAudienceDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	acct, _, resource, errOut := a.resolveAudience(ctx, args)
	if errOut != nil {
		return errOut, nil
	}
	usage, usageErr := a.audienceUsage(ctx, acct, resource)
	if usageErr == nil && len(resultRows(usage)) > 0 && !boolArg(args, "force") {
		return mcpError("audience is used by active targeting; remove it first or set force=true"), nil
	}
	tool := map[string]string{"meta": "audience_delete", "google": "data_manager_user_list_delete", "x": "delete_custom_audience", "reddit": "delete_custom_audience"}[acct.Platform]
	input := map[string]any{}
	switch acct.Platform {
	case "meta":
		input["audience_id"] = resource.NativeID
	case "google":
		input = googleDataManagerResourceInput(acct)
		input["account_type"] = "GOOGLE_ADS"
		input["account_id"] = acct.NativeAccountID
		input["user_list_id"] = resource.NativeID
	case "x":
		input = map[string]any{"account_id": acct.NativeAccountID, "custom_audience_id": resource.NativeID}
	case "reddit":
		if resource.ProviderType == "reddit_saved_audience" {
			tool = "delete_saved_audience"
			input["saved_audience_id"] = resource.NativeID
		} else {
			input["custom_audience_id"] = resource.NativeID
		}
	}
	parsed, errOut := a.execIntegrationTool(ctx, acct, tool, input)
	if errOut != nil {
		return errOut, nil
	}
	pid, _ := requireProject(ctx, args)
	_, err := ctx.AppDB().Exec(`DELETE FROM ad_resources WHERE id=? AND ad_account_id=? AND project_id=?`, resource.ID, acct.ID, pid)
	if err != nil {
		return nil, err
	}
	a.emitAudienceEvent(ctx, acct, "audience.deleted", resource, 0, "completed")
	return map[string]any{"deleted": true, "audience_id": resource.ID, "provider": parsed}, nil
}

func (a *App) toolAudienceRefresh(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	copyArgs := cloneMap(args)
	copyArgs["refresh"] = true
	return a.audienceList(ctx, copyArgs)
}

func (a *App) toolAudienceUsageGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	acct, _, resource, errOut := a.resolveAudience(ctx, args)
	if errOut != nil {
		return errOut, nil
	}
	return a.audienceUsage(ctx, acct, resource)
}

func (a *App) audienceUsage(ctx *sdk.AppCtx, acct *adAccount, resource *adResource) (any, error) {
	switch acct.Platform {
	case "x":
		return a.execOrErr(ctx, acct, "get_custom_audience_usage", map[string]any{"account_id": acct.NativeAccountID, "custom_audience_id": resource.NativeID, "with_active": true})
	case "meta":
		parsed, errOut := a.execIntegrationTool(ctx, acct, "adset_list", map[string]any{"adAccountId": acct.NativeAccountID, "fields": "id,name,status,campaign_id,targeting", "limit": 500})
		if errOut != nil {
			return errOut, nil
		}
		return map[string]any{"data": rowsContainingID(resultRows(parsed), resource.NativeID)}, nil
	case "google":
		query := fmt.Sprintf("SELECT ad_group_criterion.resource_name, ad_group_criterion.ad_group, ad_group_criterion.status, ad_group_criterion.user_list.user_list FROM ad_group_criterion WHERE ad_group_criterion.user_list.user_list = 'customers/%s/userLists/%s'", acct.NativeAccountID, resource.NativeID)
		parsed, errOut := a.execIntegrationTool(ctx, acct, "search", map[string]any{"customer_id": acct.NativeAccountID, "query": query})
		if errOut != nil {
			return errOut, nil
		}
		return map[string]any{"data": resultRows(parsed)}, nil
	case "reddit":
		parsed, errOut := a.execIntegrationTool(ctx, acct, "list_ad_groups", map[string]any{"ad_account_id": acct.NativeAccountID, "page.size": 500})
		if errOut != nil {
			return errOut, nil
		}
		return map[string]any{"data": rowsContainingID(resultRows(parsed), resource.NativeID)}, nil
	}
	return map[string]any{"data": []any{}}, nil
}

func (a *App) toolAudienceSourceList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	sourceKind := strings.ToLower(stringArgAny(args, "source_kind"))
	if sourceKind == "" || sourceKind == "storage" {
		var out map[string]any
		if err := ctx.PlatformAPI().CallAppResult("storage", "files_search", map[string]any{"q": stringArgAny(args, "q"), "limit": 200}, &out); err != nil {
			return nil, err
		}
		files := []map[string]any{}
		for _, row := range mapsFromAny(out["files"]) {
			name := strings.ToLower(firstString(row, "name"))
			contentType := strings.ToLower(firstString(row, "content_type"))
			if strings.HasSuffix(name, ".csv") || strings.Contains(contentType, "csv") {
				files = append(files, row)
			}
		}
		return map[string]any{"source_kind": "storage", "data": files}, nil
	}
	if sourceKind == "crm_segment" {
		var out map[string]any
		if err := ctx.PlatformAPI().CallAppResult("crm", "segments_list", map[string]any{}, &out); err != nil {
			return nil, err
		}
		rows := mapsFromAny(out["segments"])
		if len(rows) == 0 {
			rows = resultRows(out)
		}
		return map[string]any{"source_kind": "crm_segment", "data": rows}, nil
	}
	return mcpError("source_kind must be storage or crm_segment"), nil
}

func (a *App) toolTargetingCatalogSearch(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	acct, _, errOut := a.resolveAdAccount(ctx, args)
	if errOut != nil {
		return errOut, nil
	}
	kind := strings.ToLower(stringArgAny(args, "type"))
	query := strings.TrimSpace(stringArgAny(args, "query", "q"))
	limit := intArg(args, "limit", 100)
	if limit < 1 || limit > 500 {
		limit = 100
	}
	var parsed any
	switch acct.Platform {
	case "meta":
		searchType := map[string]string{"interest": "adinterest", "location": "adgeolocation"}[kind]
		if searchType == "" {
			return mcpError("Meta targeting search supports location and interest"), nil
		}
		input := map[string]any{"type": searchType, "q": query, "limit": limit}
		if kind == "location" {
			input["location_types"] = []string{"country", "region", "city", "zip"}
			if country := stringArgAny(args, "country_code"); country != "" {
				input["country_code"] = country
			}
		}
		parsed, errOut = a.execIntegrationTool(ctx, acct, "targeting_search", input)
	case "google":
		var gaql string
		escaped := strings.ReplaceAll(query, "'", "\\'")
		switch kind {
		case "location":
			gaql = "SELECT geo_target_constant.id, geo_target_constant.name, geo_target_constant.country_code, geo_target_constant.target_type, geo_target_constant.status FROM geo_target_constant"
			if escaped != "" {
				gaql += " WHERE geo_target_constant.name LIKE '%" + escaped + "%'"
			}
		case "language":
			gaql = "SELECT language_constant.id, language_constant.name, language_constant.code, language_constant.targetable FROM language_constant"
			if escaped != "" {
				gaql += " WHERE language_constant.name LIKE '%" + escaped + "%'"
			}
		default:
			return mcpError("Google targeting catalog search supports location and language; use keyword planning tools for keyword ideas"), nil
		}
		gaql += fmt.Sprintf(" LIMIT %d", limit)
		parsed, errOut = a.execIntegrationTool(ctx, acct, "search", map[string]any{"customer_id": acct.NativeAccountID, "query": gaql})
	case "x":
		tool := map[string]string{"location": "list_targeting_locations", "interest": "list_targeting_interests", "language": "list_targeting_languages", "device": "list_targeting_devices"}[kind]
		if tool == "" {
			return mcpError("X targeting catalog search supports location, interest, language, and device"), nil
		}
		input := map[string]any{}
		if kind == "location" {
			if query != "" {
				input["q"] = query
			}
			if country := stringArgAny(args, "country_code"); country != "" {
				input["country_code"] = country
			}
		}
		if kind == "interest" {
			input["count"] = limit
			if cursor := stringArgAny(args, "cursor"); cursor != "" {
				input["cursor"] = cursor
			}
		}
		parsed, errOut = a.execIntegrationTool(ctx, acct, tool, input)
	case "reddit":
		tool := map[string]string{"location": "list_geolocations", "interest": "list_interests", "community": "list_communities", "language": "list_languages", "device": "list_devices", "third_party": "list_third_party_audiences"}[kind]
		if tool == "" {
			return mcpError("unsupported Reddit targeting catalog type"), nil
		}
		input := map[string]any{}
		if kind == "location" {
			input["page.size"] = limit
			if query != "" {
				input["search"] = query
			}
			if country := stringArgAny(args, "country_code"); country != "" {
				input["country_code"] = country
			}
		}
		if kind == "interest" {
			input["page.size"] = limit
		}
		if kind == "community" {
			input["page.size"] = limit
			if query != "" {
				tool = "search_communities"
				input["query"] = query
			}
		}
		if kind == "third_party" {
			input["ad_account_id"] = acct.NativeAccountID
			input["page.size"] = limit
		}
		if cursor := stringArgAny(args, "cursor"); cursor != "" {
			input["next_url"] = cursor
		}
		parsed, errOut = a.execIntegrationTool(ctx, acct, tool, input)
	}
	if errOut != nil {
		return errOut, nil
	}
	items := []map[string]any{}
	for _, row := range resultRows(parsed) {
		item := row
		if acct.Platform == "google" {
			if nested := mapAt(row, "geoTargetConstant"); len(nested) > 0 {
				item = nested
			}
			if nested := mapAt(row, "languageConstant"); len(nested) > 0 {
				item = nested
			}
		}
		items = append(items, map[string]any{"platform": acct.Platform, "type": kind, "id": firstString(item, "id", "key", "code", "name"), "name": firstString(item, "name", "display_name", "label"), "metadata": item})
	}
	return map[string]any{"data": items, "next_cursor": firstString(asMap(parsed), "next_cursor", "nextCursor"), "next_url": redditNextURL(parsed)}, nil
}

func (a *App) toolAudienceMembersSync(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	acct, _, resource, errOut := a.resolveAudience(ctx, args)
	if errOut != nil {
		return errOut, nil
	}
	operation := strings.ToLower(stringArgAny(args, "operation"))
	if operation == "" {
		operation = "add"
	}
	if !containsString(audienceCapabilities[acct.Platform].SyncModes, operation) {
		return mcpError("sync operation " + operation + " is not supported by " + acct.Platform), nil
	}
	source := asMap(args["source"])
	sourceKind := strings.ToLower(firstString(source, "kind", "type"))
	sourceRef := firstString(source, "ref", "id", "storage_id", "segment_id")
	if !containsString(audienceCapabilities[acct.Platform].Sources, sourceKind) || sourceRef == "" {
		return mcpError("source requires kind storage or crm_segment and a ref"), nil
	}
	idempotencyKey := strings.TrimSpace(stringArgAny(args, "idempotency_key"))
	if idempotencyKey == "" {
		return mcpError("idempotency_key required"), nil
	}
	mapping := stringMapArg(args["mapping"])
	consent := asMap(args["consent"])
	if acct.Platform == "google" && !googleConsentValid(consent) {
		return mcpError("Google Customer Match requires consent.ad_user_data and consent.ad_personalization"), nil
	}
	mappingJSON, _ := json.Marshal(mapping)
	consentJSON, _ := json.Marshal(consent)
	pid, _ := requireProject(ctx, args)
	result, err := ctx.AppDB().Exec(
		`INSERT INTO ad_audience_jobs (project_id, ad_account_id, audience_resource_id, native_audience_id, operation, source_kind, source_ref, mapping_json, consent_json, idempotency_key)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(project_id, idempotency_key) DO NOTHING`,
		pid, acct.ID, resource.ID, resource.NativeID, operation, sourceKind, sourceRef, string(mappingJSON), string(consentJSON), idempotencyKey,
	)
	if err != nil {
		return nil, err
	}
	jobID, _ := result.LastInsertId()
	if jobID == 0 {
		_ = ctx.AppDB().QueryRow(`SELECT id FROM ad_audience_jobs WHERE project_id=? AND idempotency_key=?`, pid, idempotencyKey).Scan(&jobID)
	}
	job, err := a.getAudienceJob(ctx, pid, jobID)
	if err != nil {
		return nil, err
	}
	a.emitAudienceEvent(ctx, acct, "audience.sync.progress", resource, job.ID, job.Status)
	return job.response(), nil
}

func (a *App) toolAudienceSyncStatus(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return mcpError(err.Error()), nil
	}
	job, err := a.getAudienceJob(ctx, pid, int64(intArg(args, "job_id", 0)))
	if err != nil {
		return mcpError("audience sync job not found"), nil
	}
	if accountID := int64(intArg(args, "ad_account_id", 0)); accountID > 0 && accountID != job.AdAccountID {
		return mcpError("audience sync job not found"), nil
	}
	return job.response(), nil
}

func (a *App) runAudienceSyncProcessor(_ context.Context, ctx *sdk.AppCtx) error {
	if err := a.pollGoogleAudienceJob(ctx); err != nil && !errors.Is(err, sql.ErrNoRows) {
		ctx.Logger().Warn("poll Google audience sync failed", "error", err)
	}
	for i := 0; i < 2; i++ {
		job, err := a.claimAudienceJob(ctx)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		a.processAudienceJob(ctx, job)
	}
	return nil
}

func (a *App) claimAudienceJob(ctx *sdk.AppCtx) (*audienceJob, error) {
	var id int64
	err := ctx.AppDB().QueryRow(`SELECT id FROM ad_audience_jobs WHERE status='queued' AND available_at<=datetime('now') ORDER BY id LIMIT 1`).Scan(&id)
	if err != nil {
		return nil, err
	}
	result, err := ctx.AppDB().Exec(`UPDATE ad_audience_jobs SET status='processing', attempts=attempts+1, started_at=COALESCE(started_at,CURRENT_TIMESTAMP), updated_at=CURRENT_TIMESTAMP WHERE id=? AND status='queued'`, id)
	if err != nil {
		return nil, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return nil, sql.ErrNoRows
	}
	return a.getAudienceJobAnyProject(ctx, id)
}

func (a *App) processAudienceJob(ctx *sdk.AppCtx, job *audienceJob) {
	args := map[string]any{"ad_account_id": job.AdAccountID, "_project_id": job.ProjectID}
	acct, _, errOut := a.resolveAdAccount(ctx, args)
	if errOut != nil {
		a.failAudienceJob(ctx, job, mcpErrorMessage(errOut), false)
		return
	}
	members, checksum, rejected, err := a.loadAudienceMembers(ctx, job)
	if err != nil {
		a.failAudienceJob(ctx, job, err.Error(), false)
		return
	}
	members, providerRejected := filterAudienceMembers(acct.Platform, members)
	rejected += providerRejected
	if len(members) == 0 {
		a.failAudienceJob(ctx, job, "source contains no usable audience identifiers", false)
		return
	}
	_, _ = ctx.AppDB().Exec(`UPDATE ad_audience_jobs SET total_rows=?, rejected_rows=?, source_checksum=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`, len(members)+rejected, rejected, checksum, job.ID)
	accepted := 0
	requestID := ""
	for start := 0; start < len(members); start += audienceBatchSize(acct.Platform) {
		end := start + audienceBatchSize(acct.Platform)
		if end > len(members) {
			end = len(members)
		}
		parsed, providerErr := a.sendAudienceBatch(ctx, acct, job, members[start:end])
		if providerErr != nil {
			retry := audienceProviderRetryable(providerErr)
			a.failAudienceJob(ctx, job, mcpErrorMessage(providerErr), retry)
			return
		}
		accepted += end - start
		if id := firstString(asMap(parsed), "requestId", "request_id"); id != "" {
			requestID = id
		}
		_, _ = ctx.AppDB().Exec(`UPDATE ad_audience_jobs SET processed_rows=?, accepted_rows=?, provider_request_id=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`, end, accepted, requestID, job.ID)
		a.emitAudienceJobEvent(ctx, acct, job, "audience.sync.progress", "processing", accepted, len(members)+rejected, "")
	}
	status := "completed"
	if acct.Platform == "google" && requestID != "" {
		status = "provider_processing"
	}
	_, _ = ctx.AppDB().Exec(`UPDATE ad_audience_jobs SET status=?, available_at=CASE WHEN ?='provider_processing' THEN datetime('now','+30 minutes') ELSE available_at END, completed_at=CASE WHEN ?='completed' THEN CURRENT_TIMESTAMP ELSE NULL END, updated_at=CURRENT_TIMESTAMP WHERE id=?`, status, status, status, job.ID)
	event := "audience.ready"
	if status == "provider_processing" {
		event = "audience.sync.progress"
	}
	a.emitAudienceJobEvent(ctx, acct, job, event, status, accepted, len(members)+rejected, "")
}

func (a *App) pollGoogleAudienceJob(ctx *sdk.AppCtx) error {
	var id int64
	if err := ctx.AppDB().QueryRow(`SELECT id FROM ad_audience_jobs WHERE status='provider_processing' AND provider_request_id!='' AND available_at<=datetime('now') ORDER BY id LIMIT 1`).Scan(&id); err != nil {
		return err
	}
	job, err := a.getAudienceJobAnyProject(ctx, id)
	if err != nil {
		return err
	}
	acct, _, errOut := a.resolveAdAccount(ctx, map[string]any{"ad_account_id": job.AdAccountID, "_project_id": job.ProjectID})
	if errOut != nil {
		a.failAudienceJob(ctx, job, mcpErrorMessage(errOut), false)
		return nil
	}
	parsed, providerErr := a.execIntegrationTool(ctx, acct, "data_manager_request_status_get", map[string]any{"requestId": job.ProviderRequestID})
	if providerErr != nil {
		message := mcpErrorMessage(providerErr)
		if audienceProviderRetryable(providerErr) {
			_, _ = ctx.AppDB().Exec(`UPDATE ad_audience_jobs SET last_error=?, available_at=datetime('now','+60 minutes'), updated_at=CURRENT_TIMESTAMP WHERE id=?`, message, job.ID)
			a.emitAudienceJobEvent(ctx, acct, job, "audience.sync.progress", "provider_processing", job.AcceptedRows, job.TotalRows, message)
		} else {
			_, _ = ctx.AppDB().Exec(`UPDATE ad_audience_jobs SET status='failed', last_error=?, completed_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP WHERE id=?`, message, job.ID)
			a.emitAudienceJobEvent(ctx, acct, job, "audience.sync.failed", "failed", job.AcceptedRows, job.TotalRows, message)
		}
		return nil
	}
	statuses := googleRequestStatuses(parsed)
	if len(statuses) == 0 || containsString(statuses, "PROCESSING") || containsString(statuses, "REQUEST_STATUS_UNSPECIFIED") {
		_, _ = ctx.AppDB().Exec(`UPDATE ad_audience_jobs SET available_at=datetime('now','+60 minutes'), updated_at=CURRENT_TIMESTAMP WHERE id=?`, job.ID)
		a.emitAudienceJobEvent(ctx, acct, job, "audience.sync.progress", "provider_processing", job.AcceptedRows, job.TotalRows, "")
		return nil
	}
	if containsString(statuses, "FAILURE") {
		message := "Google Data Manager rejected the audience upload during asynchronous processing"
		_, _ = ctx.AppDB().Exec(`UPDATE ad_audience_jobs SET status='failed', last_error=?, completed_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP WHERE id=?`, message, job.ID)
		a.emitAudienceJobEvent(ctx, acct, job, "audience.sync.failed", "failed", job.AcceptedRows, job.TotalRows, message)
		return nil
	}
	_, _ = ctx.AppDB().Exec(`UPDATE ad_audience_jobs SET status='completed', completed_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP WHERE id=?`, job.ID)
	a.emitAudienceJobEvent(ctx, acct, job, "audience.ready", "completed", job.AcceptedRows, job.TotalRows, "")
	return nil
}

func googleRequestStatuses(parsed any) []string {
	root := asMap(parsed)
	rows := anySlice(root["requestStatusPerDestination"])
	if len(rows) == 0 {
		rows = anySlice(root["request_status_per_destination"])
	}
	statuses := make([]string, 0, len(rows))
	for _, raw := range rows {
		status := strings.ToUpper(firstString(asMap(raw), "requestStatus", "request_status"))
		if status != "" {
			statuses = append(statuses, status)
		}
	}
	return statuses
}

func (a *App) sendAudienceBatch(ctx *sdk.AppCtx, acct *adAccount, job *audienceJob, members []map[string]string) (any, map[string]any) {
	switch acct.Platform {
	case "meta":
		schema, data := metaAudiencePayload(members)
		tool := "audience_users_add"
		if job.Operation == "remove" {
			tool = "audience_users_remove"
		}
		return a.execIntegrationTool(ctx, acct, tool, map[string]any{"audience_id": job.NativeAudienceID, "payload": map[string]any{"schema": schema, "data": data}})
	case "google":
		rows := make([]map[string]any, 0, len(members))
		for _, member := range members {
			identifiers := []map[string]any{}
			if value := member["email"]; value != "" {
				identifiers = append(identifiers, map[string]any{"emailAddress": value})
			}
			if value := member["phone"]; value != "" {
				identifiers = append(identifiers, map[string]any{"phoneNumber": value})
			}
			if len(identifiers) > 0 {
				rows = append(rows, map[string]any{"compositeData": map[string]any{"userData": map[string]any{"userIdentifiers": identifiers}}})
			}
		}
		destination := map[string]any{"operatingAccount": map[string]any{"accountType": "GOOGLE_ADS", "accountId": acct.NativeAccountID}, "productDestinationId": job.NativeAudienceID}
		if acct.LoginAccountID != "" {
			destination["loginAccount"] = map[string]any{"accountType": "GOOGLE_ADS", "accountId": acct.LoginAccountID}
		}
		input := map[string]any{"destinations": []map[string]any{destination}, "audienceMembers": rows, "encoding": "HEX", "termsOfService": map[string]any{"customerMatchTermsOfServiceStatus": "ACCEPTED"}, "consent": googleConsentPayload(job.Consent)}
		tool := "data_manager_audience_members_ingest"
		if job.Operation == "remove" {
			tool = "data_manager_audience_members_remove"
		}
		return a.execIntegrationTool(ctx, acct, tool, input)
	case "x":
		users := []map[string]any{}
		for _, member := range members {
			user := map[string]any{}
			if member["email"] != "" {
				user["email"] = []string{member["email"]}
			}
			if member["phone"] != "" {
				user["phone_number"] = []string{member["phone"]}
			}
			if member["mobile_id"] != "" {
				user["device_id"] = []string{member["mobile_id"]}
			}
			if len(user) > 0 {
				users = append(users, user)
			}
		}
		operation := "Update"
		tool := "add_custom_audience_users"
		if job.Operation == "remove" {
			operation = "Delete"
			tool = "remove_custom_audience_users"
		}
		operations := []map[string]any{{"operation_type": operation, "params": map[string]any{"users": users}}}
		return a.execIntegrationTool(ctx, acct, tool, map[string]any{"account_id": acct.NativeAccountID, "custom_audience_id": job.NativeAudienceID, "users": operations})
	case "reddit":
		columns := []string{}
		for _, field := range []string{"email", "mobile_id"} {
			for _, member := range members {
				if member[field] != "" {
					columns = append(columns, map[string]string{"email": "EMAIL_SHA256", "mobile_id": "MAID_SHA256"}[field])
					break
				}
			}
		}
		rows := make([][]string, 0, len(members))
		for _, member := range members {
			row := make([]string, 0, len(columns))
			for _, column := range columns {
				field := map[string]string{"EMAIL_SHA256": "email", "MAID_SHA256": "mobile_id"}[column]
				row = append(row, member[field])
			}
			rows = append(rows, row)
		}
		data := map[string]any{"action_type": strings.ToUpper(job.Operation), "column_order": columns, "user_data": rows}
		return a.execIntegrationTool(ctx, acct, "update_custom_audience_users", map[string]any{"custom_audience_id": job.NativeAudienceID, "data": data})
	}
	return nil, mcpError("audience member sync unsupported")
}

func (a *App) loadAudienceMembers(ctx *sdk.AppCtx, job *audienceJob) ([]map[string]string, string, int, error) {
	if job.SourceKind == "storage" {
		return a.loadStorageAudienceMembers(ctx, job)
	}
	return a.loadCRMAudienceMembers(ctx, job)
}

func (a *App) loadStorageAudienceMembers(ctx *sdk.AppCtx, job *audienceJob) ([]map[string]string, string, int, error) {
	id, err := strconv.ParseInt(job.SourceRef, 10, 64)
	if err != nil || id <= 0 {
		return nil, "", 0, errors.New("invalid storage source ref")
	}
	var file map[string]any
	if err := ctx.PlatformAPI().CallAppResult("storage", "files_get_content", map[string]any{"id": id}, &file); err != nil {
		return nil, "", 0, fmt.Errorf("read audience source: %w", err)
	}
	raw, err := base64.StdEncoding.DecodeString(firstString(file, "content_base64"))
	if err != nil {
		return nil, "", 0, errors.New("audience source is not valid base64")
	}
	sum := sha256.Sum256(raw)
	reader := csv.NewReader(strings.NewReader(string(raw)))
	reader.FieldsPerRecord = -1
	reader.ReuseRecord = true
	headers, err := reader.Read()
	if err != nil {
		return nil, "", 0, fmt.Errorf("read CSV header: %w", err)
	}
	indexes := audienceColumnIndexes(headers, job.Mapping)
	if len(indexes) == 0 {
		return nil, "", 0, errors.New("CSV needs a mapped email, phone, or mobile_id column")
	}
	members := []map[string]string{}
	rejected := 0
	for rowNo := 0; rowNo < audienceMaxRows; rowNo++ {
		row, readErr := reader.Read()
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			rejected++
			continue
		}
		member := normalizeAudienceMember(row, indexes)
		if len(member) == 0 {
			rejected++
			continue
		}
		members = append(members, member)
	}
	if _, err := reader.Read(); err == nil {
		return nil, "", 0, fmt.Errorf("audience source exceeds %d rows", audienceMaxRows)
	}
	return members, hex.EncodeToString(sum[:]), rejected, nil
}

func (a *App) loadCRMAudienceMembers(ctx *sdk.AppCtx, job *audienceJob) ([]map[string]string, string, int, error) {
	id, err := strconv.ParseInt(job.SourceRef, 10, 64)
	if err != nil || id <= 0 {
		return nil, "", 0, errors.New("invalid CRM segment ref")
	}
	var evaluated map[string]any
	if err := ctx.PlatformAPI().CallAppResult("crm", "segments_eval", map[string]any{"id": id, "limit": 5000}, &evaluated); err != nil {
		return nil, "", 0, fmt.Errorf("evaluate CRM segment: %w", err)
	}
	ids := anySlice(evaluated["contact_ids"])
	if len(ids) == 0 {
		ids = anySlice(evaluated["data"])
	}
	if total := intArg(evaluated, "count", len(ids)); total > len(ids) {
		return nil, "", 0, fmt.Errorf("CRM segment contains %d contacts; export it to a Storage CSV to sync more than %d contacts", total, len(ids))
	}
	members := []map[string]string{}
	rejected := 0
	checksum := sha256.New()
	for _, rawID := range ids {
		contactID := int64ArgAny(rawID, asMap(rawID)["id"])
		if contactID <= 0 {
			rejected++
			continue
		}
		var contact map[string]any
		if err := ctx.PlatformAPI().CallAppResult("crm", "contacts_get", map[string]any{"id": contactID}, &contact); err != nil {
			rejected++
			continue
		}
		if nested := asMap(contact["contact"]); len(nested) > 0 {
			contact = nested
		}
		member := normalizeAudienceValues(map[string]string{"email": firstString(contact, "primary_email", "email"), "phone": firstString(contact, "primary_phone", "phone")})
		if len(member) == 0 {
			rejected++
			continue
		}
		members = append(members, member)
		_, _ = checksum.Write([]byte(strconv.FormatInt(contactID, 10) + "\n"))
	}
	return members, hex.EncodeToString(checksum.Sum(nil)), rejected, nil
}

func (a *App) failAudienceJob(ctx *sdk.AppCtx, job *audienceJob, message string, retry bool) {
	status := "failed"
	available := "CURRENT_TIMESTAMP"
	if retry && job.Attempts < 3 {
		status = "queued"
		available = "datetime('now','+' || ? || ' seconds')"
	}
	if status == "queued" {
		_, _ = ctx.AppDB().Exec(`UPDATE ad_audience_jobs SET status='queued', last_error=?, available_at=`+available+`, updated_at=CURRENT_TIMESTAMP WHERE id=?`, message, 5*job.Attempts, job.ID)
	} else {
		_, _ = ctx.AppDB().Exec(`UPDATE ad_audience_jobs SET status='failed', last_error=?, completed_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP WHERE id=?`, message, job.ID)
	}
	acct, _, _ := a.resolveAdAccount(ctx, map[string]any{"ad_account_id": job.AdAccountID, "_project_id": job.ProjectID})
	if acct != nil {
		a.emitAudienceJobEvent(ctx, acct, job, "audience.sync.failed", status, job.AcceptedRows, job.TotalRows, message)
	}
}

func (a *App) getAudienceJob(ctx *sdk.AppCtx, projectID string, id int64) (*audienceJob, error) {
	return scanAudienceJob(ctx.AppDB().QueryRow(`SELECT id, project_id, ad_account_id, COALESCE(audience_resource_id,0), native_audience_id, operation, source_kind, source_ref, mapping_json, consent_json, status, total_rows, processed_rows, accepted_rows, rejected_rows, source_checksum, provider_request_id, last_error, attempts, created_at, updated_at FROM ad_audience_jobs WHERE id=? AND project_id=?`, id, projectID))
}

func (a *App) getAudienceJobAnyProject(ctx *sdk.AppCtx, id int64) (*audienceJob, error) {
	return scanAudienceJob(ctx.AppDB().QueryRow(`SELECT id, project_id, ad_account_id, COALESCE(audience_resource_id,0), native_audience_id, operation, source_kind, source_ref, mapping_json, consent_json, status, total_rows, processed_rows, accepted_rows, rejected_rows, source_checksum, provider_request_id, last_error, attempts, created_at, updated_at FROM ad_audience_jobs WHERE id=?`, id))
}

func scanAudienceJob(row rowScanner) (*audienceJob, error) {
	var job audienceJob
	var mappingJSON, consentJSON string
	err := row.Scan(&job.ID, &job.ProjectID, &job.AdAccountID, &job.AudienceResourceID, &job.NativeAudienceID, &job.Operation, &job.SourceKind, &job.SourceRef, &mappingJSON, &consentJSON, &job.Status, &job.TotalRows, &job.ProcessedRows, &job.AcceptedRows, &job.RejectedRows, &job.SourceChecksum, &job.ProviderRequestID, &job.LastError, &job.Attempts, &job.CreatedAt, &job.UpdatedAt)
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(mappingJSON), &job.Mapping)
	_ = json.Unmarshal([]byte(consentJSON), &job.Consent)
	return &job, nil
}

func (a *App) resolveAudience(ctx *sdk.AppCtx, args map[string]any) (*adAccount, *platformDef, *adResource, map[string]any) {
	acct, def, errOut := a.resolveAdAccount(ctx, args)
	if errOut != nil {
		return nil, nil, nil, errOut
	}
	resource, err := a.getResource(ctx, acct, int64(intArg(args, "audience_id", 0)))
	if err != nil || resource.Kind != resourceAudience || resource.Status == "stale" {
		return nil, nil, nil, mcpError("audience not found in this ad account")
	}
	return acct, def, resource, nil
}

func (a *App) emitAudienceEvent(ctx *sdk.AppCtx, acct *adAccount, name string, resource *adResource, jobID int64, status string) {
	a.emitAudienceJobEvent(ctx, acct, &audienceJob{ID: jobID, AudienceResourceID: resource.ID, NativeAudienceID: resource.NativeID}, name, status, 0, 0, "")
}

func (a *App) emitAudienceJobEvent(ctx *sdk.AppCtx, acct *adAccount, job *audienceJob, name, status string, processed, total int, message string) {
	data := map[string]any{"ad_account_id": acct.ID, "platform": acct.Platform, "audience_id": job.AudienceResourceID, "job_id": nullableInt64(job.ID), "status": status, "processed_rows": processed, "total_rows": total}
	if message != "" {
		data["message"] = message
	}
	ctx.EmitWithProject(name, acct.ProjectID, data)
}

func audienceNativeID(parsed any) string {
	root := asMap(parsed)
	if data := asMap(root["data"]); len(data) > 0 {
		root = data
	}
	if results := anySlice(root["results"]); len(results) > 0 {
		root = asMap(results[0])
	}
	id := firstString(root, "id", "resourceName", "resource_name", "name")
	if strings.Contains(id, "/") {
		pieces := strings.Split(strings.TrimRight(id, "/"), "/")
		id = pieces[len(pieces)-1]
	}
	return id
}

func metaAudienceSubtype(kind string) string {
	return map[string]string{"customer_list": "CUSTOM", "website": "WEBSITE", "app_activity": "APP", "engagement": "ENGAGEMENT", "lookalike": "LOOKALIKE"}[kind]
}

func audienceResourceCapabilities(platform, kind string) []string {
	capabilities := []string{"target", "get", "delete"}
	if kind == "customer_list" {
		capabilities = append(capabilities, "members_add", "members_remove")
	}
	if platform != "reddit" || kind == "saved_targeting" {
		capabilities = append(capabilities, "update")
	}
	if platform == "x" || platform == "meta" || platform == "google" {
		capabilities = append(capabilities, "usage")
	}
	sort.Strings(capabilities)
	return capabilities
}

func mergeAudienceMetadata(current map[string]any, args map[string]any) map[string]any {
	out := cloneMap(current)
	if value, ok := args["description"]; ok {
		out["description"] = value
	}
	if value, ok := args["retention_days"]; ok {
		out["retention_days"] = value
	}
	return out
}

func mcpResultError(value any) map[string]any {
	if root := asMap(value); root != nil {
		if failed, _ := root["isError"].(bool); failed {
			return root
		}
	}
	return nil
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
func cloneMap(input map[string]any) map[string]any {
	output := map[string]any{}
	for key, value := range input {
		output[key] = value
	}
	return output
}
func anySlice(value any) []any {
	if rows, ok := value.([]any); ok {
		return rows
	}
	return nil
}

func mapsFromAny(value any) []map[string]any {
	rows := []map[string]any{}
	for _, value := range anySlice(value) {
		if row := asMap(value); row != nil {
			rows = append(rows, row)
		}
	}
	return rows
}

func stringMapArg(value any) map[string]string {
	out := map[string]string{}
	for key, raw := range asMap(value) {
		text := strings.TrimSpace(toString(raw))
		if text != "" {
			out[strings.ToLower(key)] = text
		}
	}
	return out
}

func audienceColumnIndexes(headers []string, mapping map[string]string) map[string]int {
	aliases := map[string][]string{"email": {"email", "email_address", "primary_email"}, "phone": {"phone", "phone_number", "primary_phone"}, "mobile_id": {"mobile_id", "device_id", "idfa", "aaid"}}
	indexed := map[string]int{}
	normalized := map[string]int{}
	for i, header := range headers {
		normalized[strings.ToLower(strings.TrimSpace(header))] = i
	}
	for field, names := range aliases {
		if mapped := strings.ToLower(strings.TrimSpace(mapping[field])); mapped != "" {
			if index, ok := normalized[mapped]; ok {
				indexed[field] = index
			}
			continue
		}
		for _, name := range names {
			if index, ok := normalized[name]; ok {
				indexed[field] = index
				break
			}
		}
	}
	return indexed
}

func normalizeAudienceMember(row []string, indexes map[string]int) map[string]string {
	values := map[string]string{}
	for field, index := range indexes {
		if index >= 0 && index < len(row) {
			values[field] = row[index]
		}
	}
	return normalizeAudienceValues(values)
}

func normalizeAudienceValues(values map[string]string) map[string]string {
	out := map[string]string{}
	for field, raw := range values {
		value := strings.TrimSpace(raw)
		if value == "" {
			continue
		}
		if isSHA256(value) {
			out[field] = strings.ToLower(value)
			continue
		}
		switch field {
		case "email":
			value = strings.ToLower(value)
		case "phone":
			value = normalizePhone(value)
		case "mobile_id":
			value = strings.ToLower(value)
		}
		if value == "" {
			continue
		}
		sum := sha256.Sum256([]byte(value))
		out[field] = hex.EncodeToString(sum[:])
	}
	return out
}

func filterAudienceMembers(platform string, members []map[string]string) ([]map[string]string, int) {
	supported := map[string]map[string]bool{
		"meta":   {"email": true, "phone": true, "mobile_id": true},
		"google": {"email": true, "phone": true},
		"x":      {"email": true, "phone": true, "mobile_id": true},
		"reddit": {"email": true, "mobile_id": true},
	}[platform]
	filtered := make([]map[string]string, 0, len(members))
	rejected := 0
	for _, member := range members {
		usable := map[string]string{}
		for field, value := range member {
			if supported[field] && value != "" {
				usable[field] = value
			}
		}
		if len(usable) == 0 {
			rejected++
			continue
		}
		filtered = append(filtered, usable)
	}
	return filtered, rejected
}

func normalizePhone(value string) string {
	value = strings.TrimSpace(value)
	out := strings.Builder{}
	for i, char := range value {
		if char >= '0' && char <= '9' {
			out.WriteRune(char)
		} else if char == '+' && i == 0 {
			out.WriteRune(char)
		}
	}
	return out.String()
}

func isSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func metaAudiencePayload(members []map[string]string) ([]string, [][]string) {
	fields := []string{}
	for _, candidate := range []string{"email", "phone", "mobile_id"} {
		for _, member := range members {
			if member[candidate] != "" {
				fields = append(fields, candidate)
				break
			}
		}
	}
	schemaName := map[string]string{"email": "EMAIL", "phone": "PHONE", "mobile_id": "MADID"}
	schema := make([]string, len(fields))
	for i, field := range fields {
		schema[i] = schemaName[field]
	}
	data := make([][]string, 0, len(members))
	for _, member := range members {
		row := make([]string, len(fields))
		for i, field := range fields {
			row[i] = member[field]
		}
		data = append(data, row)
	}
	return schema, data
}

func audienceBatchSize(platform string) int {
	return map[string]int{"meta": audienceBatchMeta, "google": audienceBatchGoogle, "x": audienceBatchX, "reddit": audienceBatchReddit}[platform]
}

func googleConsentValid(consent map[string]any) bool {
	return strings.TrimSpace(firstString(consent, "ad_user_data")) != "" && strings.TrimSpace(firstString(consent, "ad_personalization")) != ""
}

func googleConsentPayload(consent map[string]any) map[string]any {
	normalize := func(value string) string {
		value = strings.ToUpper(strings.TrimSpace(value))
		if value == "GRANTED" {
			return "CONSENT_GRANTED"
		}
		if value == "DENIED" {
			return "CONSENT_DENIED"
		}
		return value
	}
	return map[string]any{"adUserData": normalize(firstString(consent, "ad_user_data")), "adPersonalization": normalize(firstString(consent, "ad_personalization"))}
}

func googleDataManagerResourceInput(acct *adAccount) map[string]any {
	input := map[string]any{}
	if acct.LoginAccountID != "" {
		input["login_account"] = "accountTypes/GOOGLE_ADS/accounts/" + acct.LoginAccountID
	}
	return input
}

func rowsContainingID(rows []map[string]any, id string) []map[string]any {
	out := []map[string]any{}
	for _, row := range rows {
		encoded, _ := json.Marshal(row)
		if strings.Contains(string(encoded), id) {
			out = append(out, row)
		}
	}
	return out
}

func audienceProviderRetryable(errOut map[string]any) bool {
	message := strings.ToLower(mcpErrorMessage(errOut))
	for _, marker := range []string{"rate limit", "too many", "temporar", "retry", "timeout", "unavailable", "resource_exhausted"} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}
