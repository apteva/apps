package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

const (
	resourceIdentity         = "identity"
	resourceTrackingSource   = "tracking_source"
	resourceConversionAction = "conversion_action"
	resourceLeadForm         = "lead_form"
	resourceAudience         = "audience"
	resourceCreativeAsset    = "creative_asset"
	resourceFundingSource    = "funding_source"
)

var platformResourceKinds = map[string][]string{
	"meta":   {resourceIdentity, resourceTrackingSource, resourceLeadForm, resourceAudience},
	"google": {resourceConversionAction, resourceLeadForm, resourceAudience},
	"x":      {resourceIdentity, resourceFundingSource, resourceAudience},
	"reddit": {resourceIdentity, resourceTrackingSource, resourceFundingSource, resourceLeadForm, resourceAudience},
}

type discoveredResource struct {
	Kind           string
	ProviderType   string
	NativeID       string
	ParentNativeID string
	DisplayName    string
	Status         string
	Capabilities   []string
	Metadata       map[string]any
	ManagedByApp   bool
}

type adResource struct {
	ID               int64
	AdAccountID      int64
	Kind             string
	ProviderType     string
	NativeID         string
	ParentResourceID int64
	DisplayName      string
	Status           string
	Capabilities     []string
	Metadata         map[string]any
	ManagedByApp     bool
	RefreshedAt      string
}

func (r adResource) response() map[string]any {
	return map[string]any{
		"id":                 r.ID,
		"ad_account_id":      r.AdAccountID,
		"kind":               r.Kind,
		"provider_type":      r.ProviderType,
		"name":               r.DisplayName,
		"status":             r.Status,
		"capabilities":       r.Capabilities,
		"metadata":           r.Metadata,
		"parent_resource_id": nullableInt64(r.ParentResourceID),
		"managed_by_app":     r.ManagedByApp,
		"refreshed_at":       r.RefreshedAt,
	}
}

func (a *App) toolAccountContextGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
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
		errorsByKind = a.refreshResourceKinds(ctx, acct, platformResourceKinds[acct.Platform])
	}
	resources, err := a.listResources(ctx, acct, "")
	if err != nil {
		return nil, err
	}
	defaults, err := a.resourceDefaults(ctx, acct)
	if err != nil {
		return nil, err
	}
	items := make([]map[string]any, 0, len(resources))
	for _, resource := range resources {
		items = append(items, resource.response())
	}
	return map[string]any{
		"ad_account_id":  acct.ID,
		"platform":       acct.Platform,
		"resource_kinds": platformResourceKinds[acct.Platform],
		"resources":      items,
		"defaults":       defaults,
		"refresh_errors": errorsByKind,
	}, nil
}

func (a *App) toolResourceRefresh(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	acct, _, errOut := a.resolveAdAccount(ctx, args)
	if errOut != nil {
		return errOut, nil
	}
	kinds, invalid := requestedResourceKinds(args, acct.Platform)
	if invalid != "" {
		return mcpError("unsupported resource kind for " + acct.Platform + ": " + invalid), nil
	}
	errorsByKind := a.refreshResourceKinds(ctx, acct, kinds)
	resources, err := a.listResources(ctx, acct, "")
	if err != nil {
		return nil, err
	}
	items := make([]map[string]any, 0, len(resources))
	for _, resource := range resources {
		items = append(items, resource.response())
	}
	return map[string]any{"data": items, "refresh_errors": errorsByKind}, nil
}

func (a *App) toolResourceList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	acct, _, errOut := a.resolveAdAccount(ctx, args)
	if errOut != nil {
		return errOut, nil
	}
	kind := strings.TrimSpace(stringArgAny(args, "kind"))
	if refresh, _ := args["refresh"].(bool); refresh {
		kinds := platformResourceKinds[acct.Platform]
		if kind != "" {
			kinds = []string{kind}
		}
		if errorsByKind := a.refreshResourceKinds(ctx, acct, kinds); len(errorsByKind) > 0 {
			return map[string]any{"data": []any{}, "refresh_errors": errorsByKind}, nil
		}
	}
	resources, err := a.listResources(ctx, acct, kind)
	if err != nil {
		return nil, err
	}
	items := make([]map[string]any, 0, len(resources))
	for _, resource := range resources {
		items = append(items, resource.response())
	}
	return map[string]any{"data": items}, nil
}

func (a *App) toolResourceGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	acct, _, errOut := a.resolveAdAccount(ctx, args)
	if errOut != nil {
		return errOut, nil
	}
	resourceID := int64(intArg(args, "resource_id", 0))
	resource, err := a.getResource(ctx, acct, resourceID)
	if err != nil {
		return mcpError("resource not found in this ad account"), nil
	}
	return resource.response(), nil
}

func (a *App) toolResourceSetDefault(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	acct, _, errOut := a.resolveAdAccount(ctx, args)
	if errOut != nil {
		return errOut, nil
	}
	purpose := strings.TrimSpace(stringArgAny(args, "purpose"))
	if purpose == "" {
		return mcpError("purpose required"), nil
	}
	resourceID := int64(intArg(args, "resource_id", 0))
	pid, _ := requireProject(ctx, args)
	if resourceID == 0 {
		if _, err := ctx.AppDB().Exec(
			`DELETE FROM ad_resource_defaults WHERE project_id=? AND ad_account_id=? AND purpose=?`,
			pid, acct.ID, purpose,
		); err != nil {
			return nil, err
		}
		return map[string]any{"purpose": purpose, "resource_id": nil}, nil
	}
	resource, err := a.getResource(ctx, acct, resourceID)
	if err != nil || resource.Status != "active" {
		return mcpError("resource not found or not active in this ad account"), nil
	}
	if !resourceMatchesPurpose(resource, purpose) {
		return mcpError(fmt.Sprintf("%s resource cannot be used as %s", resource.Kind, purpose)), nil
	}
	_, err = ctx.AppDB().Exec(
		`INSERT INTO ad_resource_defaults (project_id, ad_account_id, purpose, resource_id)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(project_id, ad_account_id, purpose) DO UPDATE SET
		   resource_id=excluded.resource_id, updated_at=CURRENT_TIMESTAMP`,
		pid, acct.ID, purpose, resource.ID,
	)
	if err != nil {
		return nil, err
	}
	return map[string]any{"purpose": purpose, "resource": resource.response()}, nil
}

func resourceMatchesPurpose(resource *adResource, purpose string) bool {
	switch purpose {
	case "publishing_identity":
		return resource.Kind == resourceIdentity && resource.ProviderType != "instagram_business"
	case "instagram_identity":
		return resource.Kind == resourceIdentity && resource.ProviderType == "instagram_business"
	case "conversion_source":
		return resource.Kind == resourceTrackingSource || resource.Kind == resourceConversionAction
	case "lead_form":
		return resource.Kind == resourceLeadForm
	case "audience":
		return resource.Kind == resourceAudience
	case "funding_source":
		return resource.Kind == resourceFundingSource
	default:
		return false
	}
}

func requestedResourceKinds(args map[string]any, platform string) ([]string, string) {
	available := platformResourceKinds[platform]
	allowed := make(map[string]bool, len(available))
	for _, kind := range available {
		allowed[kind] = true
	}
	raw, exists := args["kinds"]
	if !exists {
		return available, ""
	}
	requested := make([]string, 0)
	switch values := raw.(type) {
	case []any:
		for _, value := range values {
			requested = append(requested, strings.TrimSpace(toString(value)))
		}
	case []string:
		requested = append(requested, values...)
	case string:
		requested = append(requested, strings.TrimSpace(values))
	}
	for _, kind := range requested {
		if !allowed[kind] {
			return nil, kind
		}
	}
	return requested, ""
}

func (a *App) refreshResourceKinds(ctx *sdk.AppCtx, acct *adAccount, kinds []string) map[string]string {
	errorsByKind := map[string]string{}
	for _, kind := range kinds {
		resources, errOut := a.discoverResources(ctx, acct, kind)
		if errOut != nil {
			errorsByKind[kind] = mcpErrorMessage(errOut)
			continue
		}
		if err := a.replaceResources(ctx, acct, kind, resources); err != nil {
			errorsByKind[kind] = err.Error()
		}
	}
	return errorsByKind
}

func mcpErrorMessage(value map[string]any) string {
	if content, ok := value["content"].([]map[string]any); ok && len(content) > 0 {
		return toString(content[0]["text"])
	}
	if content, ok := value["content"].([]any); ok && len(content) > 0 {
		return toString(asMap(content[0])["text"])
	}
	return "resource discovery failed"
}

func (a *App) discoverResources(ctx *sdk.AppCtx, acct *adAccount, kind string) ([]discoveredResource, map[string]any) {
	if acct.Platform == "meta" {
		return a.discoverMetaResources(ctx, acct, kind)
	}
	if acct.Platform == "google" {
		return a.discoverGoogleResources(ctx, acct, kind)
	}
	if acct.Platform == "x" {
		return a.discoverXResources(ctx, acct, kind)
	}
	if acct.Platform == "reddit" {
		return a.discoverRedditResources(ctx, acct, kind)
	}
	return nil, mcpError("resource discovery unsupported for " + acct.Platform)
}

func (a *App) discoverXResources(ctx *sdk.AppCtx, acct *adAccount, kind string) ([]discoveredResource, map[string]any) {
	tool := ""
	input := map[string]any{"account_id": acct.NativeAccountID, "count": 1000}
	providerType := ""
	capabilities := []string{}
	switch kind {
	case resourceIdentity:
		tool, providerType, capabilities = "list_promotable_users", "x_promotable_user", []string{"advertise", "publish"}
	case resourceFundingSource:
		tool, providerType, capabilities = "list_funding_instruments", "x_funding_instrument", []string{"fund_campaign"}
	case resourceAudience:
		tool, providerType, capabilities = "list_custom_audiences", "x_custom_audience", []string{"target"}
	default:
		return nil, mcpError("unsupported X resource kind: " + kind)
	}
	rows, errOut := a.providerResourceRows(ctx, acct, tool, input, "x")
	if errOut != nil {
		return nil, errOut
	}
	out := make([]discoveredResource, 0, len(rows))
	for _, row := range rows {
		id := firstString(row, "id", "user_id")
		if id == "" {
			continue
		}
		name := firstString(row, "name", "screen_name")
		if name == "" {
			name = id
		}
		status := "active"
		if deleted, _ := row["deleted"].(bool); deleted {
			status = "deleted"
		}
		out = append(out, discoveredResource{
			Kind: kind, ProviderType: providerType, NativeID: id, DisplayName: name,
			Status: status, Capabilities: capabilities, Metadata: row,
		})
	}
	return out, nil
}

func (a *App) discoverRedditResources(ctx *sdk.AppCtx, acct *adAccount, kind string) ([]discoveredResource, map[string]any) {
	tool := ""
	providerType := ""
	capabilities := []string{}
	switch kind {
	case resourceIdentity:
		tool, providerType, capabilities = "list_profiles", "reddit_profile", []string{"advertise", "publish"}
	case resourceTrackingSource:
		tool, providerType, capabilities = "list_pixels", "reddit_pixel", []string{"conversion_tracking"}
	case resourceFundingSource:
		tool, providerType, capabilities = "list_funding_instruments", "reddit_funding_instrument", []string{"fund_campaign"}
	case resourceLeadForm:
		tool, providerType, capabilities = "list_lead_forms", "reddit_lead_form", []string{"read_only", "lead_generation"}
	case resourceAudience:
		tool, providerType, capabilities = "list_custom_audiences", "reddit_custom_audience", []string{"target"}
	default:
		return nil, mcpError("unsupported Reddit resource kind: " + kind)
	}
	input := map[string]any{"ad_account_id": acct.NativeAccountID, "page.size": 200}
	rows, errOut := a.providerResourceRows(ctx, acct, tool, input, "reddit")
	if errOut != nil {
		return nil, errOut
	}
	out := make([]discoveredResource, 0, len(rows))
	for _, row := range rows {
		id := firstString(row, "id", "profile_id", "pixel_id")
		if id == "" {
			continue
		}
		name := firstString(row, "name", "username")
		if name == "" {
			name = id
		}
		status := normalizedResourceStatus(firstString(row, "status", "effective_status", "configured_status"))
		if kind == resourceLeadForm {
			status = "read_only"
		}
		out = append(out, discoveredResource{
			Kind: kind, ProviderType: providerType, NativeID: id, DisplayName: name,
			Status: status, Capabilities: capabilities, Metadata: row,
		})
	}
	return out, nil
}

func (a *App) providerResourceRows(ctx *sdk.AppCtx, acct *adAccount, tool string, input map[string]any, platform string) ([]map[string]any, map[string]any) {
	rows := make([]map[string]any, 0)
	continuation := ""
	for page := 0; page < 20; page++ {
		if continuation != "" {
			if platform == "x" {
				input["cursor"] = continuation
			} else {
				input["next_url"] = continuation
			}
		}
		parsed, errOut := a.execIntegrationTool(ctx, acct, tool, input)
		if errOut != nil {
			return nil, errOut
		}
		rows = append(rows, resultRows(parsed)...)
		next := xNextCursor(parsed)
		if platform == "reddit" {
			next = redditNextURL(parsed)
		}
		if next == "" {
			return rows, nil
		}
		if next == continuation {
			return nil, mcpError(tool + " returned a repeated pagination value")
		}
		continuation = next
	}
	return nil, mcpError(tool + " pagination exceeded the safety limit")
}

func xNextCursor(parsed any) string {
	root := asMap(parsed)
	return firstString(root, "next_cursor", "nextCursor")
}

func redditNextURL(parsed any) string {
	return firstString(asMap(asMap(parsed)["pagination"]), "next_url", "nextUrl")
}

func (a *App) discoverMetaResources(ctx *sdk.AppCtx, acct *adAccount, kind string) ([]discoveredResource, map[string]any) {
	switch kind {
	case resourceIdentity:
		rows, errOut := a.metaResourceRows(ctx, acct, "page_list", map[string]any{
			"fields": "id,name,tasks,instagram_business_account{id,username,name}",
			"limit":  100,
		})
		if errOut != nil {
			return nil, errOut
		}
		out := make([]discoveredResource, 0, len(rows)*2)
		for _, row := range rows {
			pageID := firstString(row, "id")
			if pageID == "" {
				continue
			}
			out = append(out, discoveredResource{
				Kind: resourceIdentity, ProviderType: "facebook_page", NativeID: pageID,
				DisplayName: firstString(row, "name"), Status: "active",
				Capabilities: []string{"advertise", "publish"},
				Metadata:     map[string]any{"tasks": row["tasks"]},
			})
			instagram := mapAt(row, "instagram_business_account")
			if instagramID := firstString(instagram, "id"); instagramID != "" {
				name := firstString(instagram, "username", "name")
				if name == "" {
					name = "Instagram " + instagramID
				}
				out = append(out, discoveredResource{
					Kind: resourceIdentity, ProviderType: "instagram_business", NativeID: instagramID,
					ParentNativeID: pageID, DisplayName: name, Status: "active",
					Capabilities: []string{"advertise", "publish"},
					Metadata:     map[string]any{},
				})
			}
		}
		return out, nil
	case resourceTrackingSource:
		rows, errOut := a.metaResourceRows(ctx, acct, "pixel_list", map[string]any{
			"adAccountId": acct.NativeAccountID,
			"fields":      "id,name,last_fired_time,is_unavailable",
			"limit":       100,
		})
		if errOut != nil {
			return nil, errOut
		}
		out := make([]discoveredResource, 0, len(rows))
		for _, row := range rows {
			id := firstString(row, "id")
			if id == "" {
				continue
			}
			status := "active"
			if unavailable, _ := row["is_unavailable"].(bool); unavailable {
				status = "unavailable"
			}
			out = append(out, discoveredResource{
				Kind: resourceTrackingSource, ProviderType: "meta_pixel", NativeID: id,
				DisplayName: firstString(row, "name"), Status: status,
				Capabilities: []string{"conversion_tracking"},
				Metadata:     map[string]any{"last_fired_time": row["last_fired_time"]},
			})
		}
		return out, nil
	case resourceAudience:
		rows, errOut := a.metaResourceRows(ctx, acct, "audience_list", map[string]any{
			"adAccountId": acct.NativeAccountID,
			"fields":      "id,name,subtype,delivery_status,description",
			"limit":       100,
		})
		if errOut != nil {
			return nil, errOut
		}
		out := make([]discoveredResource, 0, len(rows))
		for _, row := range rows {
			id := firstString(row, "id")
			if id == "" {
				continue
			}
			out = append(out, discoveredResource{
				Kind: resourceAudience, ProviderType: "meta_audience", NativeID: id,
				DisplayName: firstString(row, "name"), Status: "active",
				Capabilities: []string{"target"},
				Metadata:     map[string]any{"subtype": row["subtype"], "delivery_status": row["delivery_status"], "description": row["description"]},
			})
		}
		return out, nil
	case resourceLeadForm:
		pages, errOut := a.metaResourceRows(ctx, acct, "page_list", map[string]any{"fields": "id,name", "limit": 100})
		if errOut != nil {
			return nil, errOut
		}
		out := make([]discoveredResource, 0)
		for _, page := range pages {
			pageID := firstString(page, "id")
			rows, formErr := a.metaResourceRows(ctx, acct, "leadform_list", map[string]any{
				"pageId": pageID, "fields": "id,name,status,leads_count,created_time", "limit": 100,
			})
			if formErr != nil {
				continue
			}
			for _, row := range rows {
				id := firstString(row, "id")
				if id == "" {
					continue
				}
				out = append(out, discoveredResource{
					Kind: resourceLeadForm, ProviderType: "meta_lead_form", NativeID: id,
					ParentNativeID: pageID, DisplayName: firstString(row, "name"),
					Status:       strings.ToLower(firstString(row, "status")),
					Capabilities: []string{"lead_generation"},
					Metadata:     map[string]any{"leads_count": row["leads_count"], "created_time": row["created_time"]},
				})
			}
		}
		return out, nil
	default:
		return nil, mcpError("unsupported Meta resource kind: " + kind)
	}
}

func (a *App) metaResourceRows(ctx *sdk.AppCtx, acct *adAccount, tool string, base map[string]any) ([]map[string]any, map[string]any) {
	input := make(map[string]any, len(base)+1)
	for key, value := range base {
		input[key] = value
	}
	rows := make([]map[string]any, 0)
	after := ""
	for page := 0; page < 20; page++ {
		if after != "" {
			input["after"] = after
		} else {
			delete(input, "after")
		}
		parsed, errOut := a.execIntegrationTool(ctx, acct, tool, input)
		if errOut != nil {
			return nil, errOut
		}
		rows = append(rows, resultRows(parsed)...)
		next := metaNextCursor(parsed)
		if next == "" {
			return rows, nil
		}
		if next == after {
			return nil, mcpError(tool + " returned a repeated cursor")
		}
		after = next
	}
	return nil, mcpError(tool + " pagination exceeded the safety limit")
}

func (a *App) discoverGoogleResources(ctx *sdk.AppCtx, acct *adAccount, kind string) ([]discoveredResource, map[string]any) {
	var query string
	switch kind {
	case resourceConversionAction:
		query = "SELECT conversion_action.id, conversion_action.name, conversion_action.status, conversion_action.type, conversion_action.category, conversion_action.owner_customer FROM conversion_action ORDER BY conversion_action.id"
	case resourceLeadForm:
		query = "SELECT asset.id, asset.name, asset.resource_name, asset.type, asset.lead_form_asset.business_name, asset.lead_form_asset.headline, asset.lead_form_asset.description, asset.lead_form_asset.privacy_policy_url, asset.lead_form_asset.fields, asset.lead_form_asset.call_to_action_type, asset.lead_form_asset.call_to_action_description, asset.lead_form_asset.desired_intent, asset.lead_form_asset.post_submit_headline, asset.lead_form_asset.post_submit_description, asset.lead_form_asset.post_submit_call_to_action_type FROM asset WHERE asset.type = LEAD_FORM ORDER BY asset.id"
	case resourceAudience:
		query = "SELECT user_list.id, user_list.name, user_list.type, user_list.description, user_list.membership_status, user_list.size_for_display, user_list.size_for_search FROM user_list ORDER BY user_list.id"
	default:
		return nil, mcpError("unsupported Google resource kind: " + kind)
	}
	rows, errOut := a.googleResourceRows(ctx, acct, query)
	if errOut != nil {
		return nil, errOut
	}
	out := make([]discoveredResource, 0, len(rows))
	for _, row := range rows {
		if kind == resourceConversionAction {
			item := mapAt(row, "conversionAction")
			if len(item) == 0 {
				item = mapAt(row, "conversion_action")
			}
			id := firstString(item, "id")
			if id == "" {
				continue
			}
			out = append(out, discoveredResource{
				Kind: kind, ProviderType: "google_conversion_action", NativeID: id,
				DisplayName: firstString(item, "name"), Status: normalizedResourceStatus(firstString(item, "status")),
				Capabilities: []string{"conversion_tracking"},
				Metadata:     map[string]any{"type": item["type"], "category": item["category"]},
			})
			continue
		}
		if kind == resourceLeadForm {
			item := mapAt(row, "asset")
			id := firstString(item, "resourceName", "resource_name", "id")
			if id == "" {
				continue
			}
			leadForm := mapAt(item, "leadFormAsset")
			if len(leadForm) == 0 {
				leadForm = mapAt(item, "lead_form_asset")
			}
			out = append(out, discoveredResource{
				Kind: kind, ProviderType: "google_lead_form", NativeID: id,
				DisplayName: firstString(item, "name"), Status: "active",
				Capabilities: []string{"lead_generation"},
				Metadata: map[string]any{
					"business_name": leadForm["businessName"], "headline": leadForm["headline"],
					"description": leadForm["description"], "privacy_policy_url": leadForm["privacyPolicyUrl"],
					"questions": googleDiscoveredLeadQuestions(leadForm["fields"]), "call_to_action": leadForm["callToActionType"],
					"call_to_action_description": leadForm["callToActionDescription"], "intent": leadForm["desiredIntent"],
				},
			})
			continue
		}
		item := mapAt(row, "userList")
		if len(item) == 0 {
			item = mapAt(row, "user_list")
		}
		id := firstString(item, "id")
		if id == "" {
			continue
		}
		out = append(out, discoveredResource{
			Kind: kind, ProviderType: "google_user_list", NativeID: id,
			DisplayName: firstString(item, "name"), Status: normalizedResourceStatus(firstString(item, "membershipStatus", "membership_status")),
			Capabilities: []string{"target"},
			Metadata:     map[string]any{"type": item["type"], "description": item["description"], "size_for_display": item["sizeForDisplay"], "size_for_search": item["sizeForSearch"]},
		})
	}
	return out, nil
}

func googleDiscoveredLeadQuestions(raw any) []any {
	rows, _ := raw.([]any)
	out := make([]any, 0, len(rows))
	for _, rawRow := range rows {
		row := asMap(rawRow)
		providerType := strings.ToUpper(firstString(row, "inputType", "input_type"))
		genericType := strings.ToLower(providerType)
		switch providerType {
		case "PHONE_NUMBER":
			genericType = "phone"
		case "COMPANY_NAME":
			genericType = "company_name"
		}
		if genericType != "" {
			out = append(out, map[string]any{"type": genericType})
		}
	}
	return out
}

func (a *App) upsertResource(ctx *sdk.AppCtx, acct *adAccount, resource discoveredResource) (*adResource, error) {
	pid := strings.TrimSpace(ctx.CurrentProject())
	capabilities, _ := json.Marshal(resource.Capabilities)
	metadata, _ := json.Marshal(resource.Metadata)
	status := resource.Status
	if status == "" {
		status = "active"
	}
	managed := 0
	if resource.ManagedByApp {
		managed = 1
	}
	_, err := ctx.AppDB().Exec(
		`INSERT INTO ad_resources
		 (project_id, ad_account_id, platform, native_asset_id, provider_type, kind,
		  display_name, status, capabilities_json, metadata_json, managed_by_app, refreshed_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
		 ON CONFLICT(project_id, ad_account_id, kind, provider_type, native_asset_id) DO UPDATE SET
		  display_name=excluded.display_name, status=excluded.status,
		  capabilities_json=excluded.capabilities_json, metadata_json=excluded.metadata_json,
		  managed_by_app=MAX(ad_resources.managed_by_app, excluded.managed_by_app),
		  refreshed_at=CURRENT_TIMESTAMP`,
		pid, acct.ID, acct.Platform, resource.NativeID, resource.ProviderType, resource.Kind,
		resource.DisplayName, status, string(capabilities), string(metadata), managed,
	)
	if err != nil {
		return nil, err
	}
	var id int64
	if err := ctx.AppDB().QueryRow(
		`SELECT id FROM ad_resources
		 WHERE project_id=? AND ad_account_id=? AND kind=? AND provider_type=? AND native_asset_id=?`,
		pid, acct.ID, resource.Kind, resource.ProviderType, resource.NativeID,
	).Scan(&id); err != nil {
		return nil, err
	}
	if resource.ParentNativeID != "" {
		var parentID int64
		_ = ctx.AppDB().QueryRow(
			`SELECT id FROM ad_resources
			 WHERE project_id=? AND ad_account_id=? AND native_asset_id=? AND status!='stale'
			 ORDER BY id LIMIT 1`,
			pid, acct.ID, resource.ParentNativeID,
		).Scan(&parentID)
		if parentID > 0 {
			_, _ = ctx.AppDB().Exec(`UPDATE ad_resources SET parent_resource_id=? WHERE id=?`, parentID, id)
		}
	}
	return a.getResource(ctx, acct, id)
}

func (a *App) googleResourceRows(ctx *sdk.AppCtx, acct *adAccount, query string) ([]map[string]any, map[string]any) {
	input := map[string]any{"customer_id": acct.NativeAccountID, "query": query}
	rows := make([]map[string]any, 0)
	pageToken := ""
	for page := 0; page < 20; page++ {
		if pageToken != "" {
			input["page_token"] = pageToken
		} else {
			delete(input, "page_token")
		}
		parsed, errOut := a.execIntegrationTool(ctx, acct, "search", input)
		if errOut != nil {
			return nil, errOut
		}
		rows = append(rows, resultRows(parsed)...)
		next := googleNextPageToken(parsed)
		if next == "" {
			return rows, nil
		}
		if next == pageToken {
			return nil, mcpError("Google resource discovery returned a repeated page token")
		}
		pageToken = next
	}
	return nil, mcpError("Google resource discovery pagination exceeded the safety limit")
}

func (a *App) replaceResources(ctx *sdk.AppCtx, acct *adAccount, kind string, resources []discoveredResource) error {
	pid := strings.TrimSpace(ctx.CurrentProject())
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(
		`UPDATE ad_resources SET status='stale', refreshed_at=CURRENT_TIMESTAMP
		 WHERE project_id=? AND ad_account_id=? AND kind=? AND managed_by_app=0`,
		pid, acct.ID, kind,
	); err != nil {
		return err
	}
	idsByNative := map[string]int64{}
	for _, resource := range resources {
		if resource.NativeID == "" {
			continue
		}
		capabilities, _ := json.Marshal(resource.Capabilities)
		metadata, _ := json.Marshal(resource.Metadata)
		status := resource.Status
		if status == "" {
			status = "active"
		}
		managed := 0
		if resource.ManagedByApp {
			managed = 1
		}
		_, err := tx.Exec(
			`INSERT INTO ad_resources
			 (project_id, ad_account_id, platform, native_asset_id, provider_type, kind,
			  display_name, status, capabilities_json, metadata_json, managed_by_app, refreshed_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
			 ON CONFLICT(project_id, ad_account_id, kind, provider_type, native_asset_id) DO UPDATE SET
			  display_name=excluded.display_name, status=excluded.status,
			  capabilities_json=excluded.capabilities_json, metadata_json=excluded.metadata_json,
			  refreshed_at=CURRENT_TIMESTAMP`,
			pid, acct.ID, acct.Platform, resource.NativeID, resource.ProviderType, resource.Kind,
			resource.DisplayName, status, string(capabilities), string(metadata), managed,
		)
		if err != nil {
			return err
		}
		var id int64
		if err := tx.QueryRow(
			`SELECT id FROM ad_resources
			 WHERE project_id=? AND ad_account_id=? AND kind=? AND provider_type=? AND native_asset_id=?`,
			pid, acct.ID, resource.Kind, resource.ProviderType, resource.NativeID,
		).Scan(&id); err != nil {
			return err
		}
		idsByNative[resource.NativeID] = id
	}
	for _, resource := range resources {
		if resource.ParentNativeID == "" {
			continue
		}
		id := idsByNative[resource.NativeID]
		parentID := idsByNative[resource.ParentNativeID]
		if parentID == 0 {
			_ = tx.QueryRow(
				`SELECT id FROM ad_resources
				 WHERE project_id=? AND ad_account_id=? AND native_asset_id=? AND status!='stale'
				 ORDER BY id LIMIT 1`,
				pid, acct.ID, resource.ParentNativeID,
			).Scan(&parentID)
		}
		if id == 0 || parentID == 0 {
			continue
		}
		if _, err := tx.Exec(`UPDATE ad_resources SET parent_resource_id=? WHERE id=?`, parentID, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (a *App) listResources(ctx *sdk.AppCtx, acct *adAccount, kind string) ([]adResource, error) {
	pid := strings.TrimSpace(ctx.CurrentProject())
	query := `SELECT id, ad_account_id, kind, provider_type, native_asset_id,
	                 COALESCE(parent_resource_id,0), display_name, status,
	                 capabilities_json, metadata_json, managed_by_app,
	                 COALESCE(refreshed_at,'')
	          FROM ad_resources
	          WHERE project_id=? AND ad_account_id=? AND status!='stale'`
	args := []any{pid, acct.ID}
	if kind != "" {
		query += ` AND kind=?`
		args = append(args, kind)
	}
	query += ` ORDER BY kind, display_name, id`
	rows, err := ctx.AppDB().Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	resources := make([]adResource, 0)
	for rows.Next() {
		resource, err := scanResource(rows)
		if err != nil {
			return nil, err
		}
		resources = append(resources, resource)
	}
	return resources, rows.Err()
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanResource(row rowScanner) (adResource, error) {
	var resource adResource
	var capabilitiesJSON, metadataJSON string
	var managed int
	err := row.Scan(
		&resource.ID, &resource.AdAccountID, &resource.Kind, &resource.ProviderType,
		&resource.NativeID, &resource.ParentResourceID, &resource.DisplayName, &resource.Status,
		&capabilitiesJSON, &metadataJSON, &managed, &resource.RefreshedAt,
	)
	if err != nil {
		return resource, err
	}
	_ = json.Unmarshal([]byte(capabilitiesJSON), &resource.Capabilities)
	_ = json.Unmarshal([]byte(metadataJSON), &resource.Metadata)
	if resource.Capabilities == nil {
		resource.Capabilities = []string{}
	}
	if resource.Metadata == nil {
		resource.Metadata = map[string]any{}
	}
	resource.ManagedByApp = managed != 0
	return resource, nil
}

func (a *App) getResource(ctx *sdk.AppCtx, acct *adAccount, resourceID int64) (*adResource, error) {
	if resourceID <= 0 {
		return nil, sql.ErrNoRows
	}
	pid := strings.TrimSpace(ctx.CurrentProject())
	resource, err := scanResource(ctx.AppDB().QueryRow(
		`SELECT id, ad_account_id, kind, provider_type, native_asset_id,
		        COALESCE(parent_resource_id,0), display_name, status,
		        capabilities_json, metadata_json, managed_by_app,
		        COALESCE(refreshed_at,'')
		 FROM ad_resources WHERE id=? AND project_id=? AND ad_account_id=?`,
		resourceID, pid, acct.ID,
	))
	if err != nil {
		return nil, err
	}
	return &resource, nil
}

func (a *App) linkedInstagramIdentity(ctx *sdk.AppCtx, acct *adAccount, pageResourceID int64) (*adResource, error) {
	pid := strings.TrimSpace(ctx.CurrentProject())
	resource, err := scanResource(ctx.AppDB().QueryRow(
		`SELECT id, ad_account_id, kind, provider_type, native_asset_id,
		        COALESCE(parent_resource_id,0), display_name, status,
		        capabilities_json, metadata_json, managed_by_app,
		        COALESCE(refreshed_at,'')
		 FROM ad_resources
		 WHERE project_id=? AND ad_account_id=? AND parent_resource_id=?
		   AND kind=? AND provider_type='instagram_business' AND status='active'
		 ORDER BY id LIMIT 1`,
		pid, acct.ID, pageResourceID, resourceIdentity,
	))
	if err != nil {
		return nil, err
	}
	return &resource, nil
}

func (a *App) resourceDefaults(ctx *sdk.AppCtx, acct *adAccount) (map[string]any, error) {
	pid := strings.TrimSpace(ctx.CurrentProject())
	rows, err := ctx.AppDB().Query(
		`SELECT d.purpose, r.id, r.ad_account_id, r.kind, r.provider_type, r.native_asset_id,
		        COALESCE(r.parent_resource_id,0), r.display_name, r.status,
		        r.capabilities_json, r.metadata_json, r.managed_by_app,
		        COALESCE(r.refreshed_at,'')
		 FROM ad_resource_defaults d JOIN ad_resources r ON r.id=d.resource_id
		 WHERE d.project_id=? AND d.ad_account_id=? AND r.status='active'
		 ORDER BY d.purpose`,
		pid, acct.ID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]any{}
	for rows.Next() {
		var purpose string
		var resource adResource
		var capabilitiesJSON, metadataJSON string
		var managed int
		if err := rows.Scan(
			&purpose, &resource.ID, &resource.AdAccountID, &resource.Kind, &resource.ProviderType,
			&resource.NativeID, &resource.ParentResourceID, &resource.DisplayName, &resource.Status,
			&capabilitiesJSON, &metadataJSON, &managed, &resource.RefreshedAt,
		); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(capabilitiesJSON), &resource.Capabilities)
		_ = json.Unmarshal([]byte(metadataJSON), &resource.Metadata)
		resource.ManagedByApp = managed != 0
		out[purpose] = resource.response()
	}
	return out, rows.Err()
}

func (a *App) resolveResourceChoice(ctx *sdk.AppCtx, acct *adAccount, purpose, kind, providerType string, explicitID int64) (*adResource, map[string]any) {
	if explicitID > 0 {
		resource, err := a.getResource(ctx, acct, explicitID)
		if err != nil || resource.Status != "active" || resource.Kind != kind || (providerType != "" && resource.ProviderType != providerType) {
			return nil, mcpError("resource is not a compatible active " + kind + " for this ad account")
		}
		return resource, nil
	}
	pid := strings.TrimSpace(ctx.CurrentProject())
	var defaultID int64
	_ = ctx.AppDB().QueryRow(
		`SELECT resource_id FROM ad_resource_defaults WHERE project_id=? AND ad_account_id=? AND purpose=?`,
		pid, acct.ID, purpose,
	).Scan(&defaultID)
	if defaultID > 0 {
		return a.resolveResourceChoice(ctx, acct, purpose, kind, providerType, defaultID)
	}
	resources, err := a.listResources(ctx, acct, kind)
	if err != nil {
		return nil, mcpError(err.Error())
	}
	candidates := make([]adResource, 0)
	for _, resource := range resources {
		if resource.Status == "active" && (providerType == "" || resource.ProviderType == providerType) {
			candidates = append(candidates, resource)
		}
	}
	if len(candidates) == 0 {
		if discovered, errOut := a.discoverResources(ctx, acct, kind); errOut == nil {
			_ = a.replaceResources(ctx, acct, kind, discovered)
			return a.resolveResourceChoiceNoRefresh(ctx, acct, purpose, kind, providerType)
		}
	}
	return selectionFromCandidates(kind, purpose, candidates)
}

func (a *App) resolveResourceChoiceNoRefresh(ctx *sdk.AppCtx, acct *adAccount, purpose, kind, providerType string) (*adResource, map[string]any) {
	resources, err := a.listResources(ctx, acct, kind)
	if err != nil {
		return nil, mcpError(err.Error())
	}
	candidates := make([]adResource, 0)
	for _, resource := range resources {
		if resource.Status == "active" && (providerType == "" || resource.ProviderType == providerType) {
			candidates = append(candidates, resource)
		}
	}
	return selectionFromCandidates(kind, purpose, candidates)
}

func selectionFromCandidates(kind, purpose string, candidates []adResource) (*adResource, map[string]any) {
	if len(candidates) == 1 {
		return &candidates[0], nil
	}
	choices := make([]map[string]any, 0, len(candidates))
	for _, candidate := range candidates {
		choices = append(choices, candidate.response())
	}
	sort.Slice(choices, func(i, j int) bool { return toString(choices[i]["name"]) < toString(choices[j]["name"]) })
	out := mcpError("select a " + kind + " resource before continuing")
	out["code"] = "selection_required"
	out["purpose"] = purpose
	out["resource_kind"] = kind
	out["choices"] = choices
	return nil, out
}

func nullableInt64(value int64) any {
	if value == 0 {
		return nil
	}
	return value
}

func normalizedResourceStatus(value string) string {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "", "ACTIVE", "ENABLED", "OPEN", "READY":
		return "active"
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
}
