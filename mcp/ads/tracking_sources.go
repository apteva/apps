package main

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	sdk "github.com/apteva/app-sdk"
)

const trackingSourceDefaultPurpose = "conversion_source"

func trackingSourceInstallationSchema() map[string]any {
	return schemaObject(map[string]any{
		"ad_account_id":               map[string]any{"type": "integer"},
		"tracking_source_resource_id": map[string]any{"type": "integer"},
	}, []string{"ad_account_id"})
}

func trackingSourceCreateSchema() map[string]any {
	return schemaObject(map[string]any{
		"ad_account_id": map[string]any{"type": "integer"},
		"name":          map[string]any{"type": "string", "minLength": 1, "maxLength": 255},
		"set_default":   map[string]any{"type": "boolean", "default": true},
		"reuse_existing": map[string]any{
			"type": "boolean", "default": true,
		},
		"category": map[string]any{
			"type": "string",
			"enum": []string{
				"purchase", "lead", "signup", "page_view", "download", "add_to_cart",
				"begin_checkout", "subscribe_paid", "submit_lead_form", "contact", "other",
			},
			"description": "Google Ads conversion category. Determines how Smart Bidding treats the action. Defaults to purchase. Ignored by Meta, which creates an untyped Pixel.",
		},
		"default_value_cents": map[string]any{
			"type": "integer", "minimum": 0,
			"description": "Google Ads only. Conversion value used when the page reports none.",
		},
	}, []string{"ad_account_id", "name"})
}

func (a *App) toolTrackingSourceCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	acct, _, errOut := a.resolveAdAccount(ctx, args)
	if errOut != nil {
		return errOut, nil
	}
	name := strings.TrimSpace(stringArgAny(args, "name"))
	if name == "" {
		return mcpError("name required"), nil
	}
	if utf8.RuneCountInString(name) > 255 {
		return mcpError("name must be 255 characters or fewer"), nil
	}
	if acct.Platform == "google" {
		return a.createGoogleConversionAction(ctx, acct, args, name)
	}
	if acct.Platform != "meta" {
		out := mcpError("tracking source creation is not supported for " + acct.Platform)
		out["code"] = "unsupported_operation"
		out["operation"] = "tracking_source_create"
		out["platform"] = acct.Platform
		return out, nil
	}

	setDefault := boolArgDefault(args, "set_default", true)
	reuseExisting := boolArgDefault(args, "reuse_existing", true)
	matches, refreshErr := a.refreshAndMatchTrackingSources(ctx, acct, name)
	if refreshErr != nil {
		return refreshErr, nil
	}
	knownMatches := make(map[string]bool, len(matches))
	for _, match := range matches {
		knownMatches[match.NativeID] = true
	}
	if reuseExisting {
		if len(matches) > 1 {
			return trackingSourceSelectionRequired(matches), nil
		}
		if len(matches) == 1 {
			return a.trackingSourceResult(ctx, acct, &matches[0], false, true, false, setDefault)
		}
	}

	parsed, createErr := a.execIntegrationTool(ctx, acct, "pixel_create", map[string]any{
		"adAccountId": acct.NativeAccountID,
		"name":        name,
	})
	if createErr != nil {
		if shouldReconcileTrackingSourceCreate(createErr) {
			matches, refreshErr := a.refreshAndMatchTrackingSources(ctx, acct, name)
			matches = trackingSourcesNotPreviouslyPresent(matches, knownMatches)
			if refreshErr == nil && len(matches) == 1 {
				resource, err := a.markTrackingSourceManaged(ctx, acct, &matches[0])
				if err != nil {
					return nil, err
				}
				result, err := a.trackingSourceResult(ctx, acct, resource, false, false, true, setDefault)
				if err == nil {
					a.emitTrackingSourceCreated(ctx, acct, resource, setDefault)
				}
				return result, err
			}
			if refreshErr == nil && len(matches) > 1 {
				return trackingSourceSelectionRequired(matches), nil
			}
			createErr["reconciliation_attempted"] = true
		}
		return createErr, nil
	}

	nativeID := trackingSourceNativeID(parsed)
	if nativeID == "" {
		out := mcpError("pixel_create returned no tracking source id")
		out["code"] = "provider_response_invalid"
		out["platform"] = acct.Platform
		return out, nil
	}
	resource, err := a.upsertResource(ctx, acct, discoveredResource{
		Kind: resourceTrackingSource, ProviderType: "meta_pixel", NativeID: nativeID,
		DisplayName: name, Status: "active",
		Capabilities: []string{"conversion_tracking", "server_events"},
		Metadata:     map[string]any{"last_fired_time": nil},
		ManagedByApp: true,
	})
	if err != nil {
		return nil, err
	}
	result, err := a.trackingSourceResult(ctx, acct, resource, true, false, false, setDefault)
	if err == nil {
		a.emitTrackingSourceCreated(ctx, acct, resource, setDefault)
	}
	return result, err
}

func (a *App) toolTrackingSourceInstallationGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	acct, _, errOut := a.resolveAdAccount(ctx, args)
	if errOut != nil {
		return errOut, nil
	}
	if acct.Platform == "google" {
		return a.googleConversionInstallation(ctx, acct, args)
	}
	if acct.Platform != "meta" {
		out := mcpError("tracking source browser installation is not supported for " + acct.Platform)
		out["code"] = "unsupported_operation"
		out["operation"] = "tracking_source_installation_get"
		out["platform"] = acct.Platform
		return out, nil
	}
	resource, resourceErr := a.resolveResourceChoice(
		ctx,
		acct,
		trackingSourceDefaultPurpose,
		resourceTrackingSource,
		"meta_pixel",
		int64(intArg(args, "tracking_source_resource_id", 0)),
	)
	if resourceErr != nil {
		return resourceErr, nil
	}
	return map[string]any{
		"resource": resource.response(),
		"installation": map[string]any{
			"provider":        "meta",
			"public_id":       resource.NativeID,
			"script_url":      "https://connect.facebook.net/en_US/fbevents.js",
			"script_origins":  []string{"https://connect.facebook.net"},
			"connect_origins": []string{"https://connect.facebook.net", "https://www.facebook.com"},
			"image_origins":   []string{"https://www.facebook.com"},
			"event_names":     []string{"PageView", "ViewContent", "Search", "AddToCart", "InitiateCheckout", "AddPaymentInfo", "Purchase"},
		},
	}, nil
}

func (a *App) markTrackingSourceManaged(ctx *sdk.AppCtx, acct *adAccount, resource *adResource) (*adResource, error) {
	return a.upsertResource(ctx, acct, discoveredResource{
		Kind: resource.Kind, ProviderType: resource.ProviderType, NativeID: resource.NativeID,
		DisplayName: resource.DisplayName, Status: resource.Status,
		Capabilities: resource.Capabilities, Metadata: resource.Metadata, ManagedByApp: true,
	})
}

func (a *App) emitTrackingSourceCreated(ctx *sdk.AppCtx, acct *adAccount, resource *adResource, isDefault bool) {
	ctx.EmitWithProject("tracking_source.created", acct.ProjectID, map[string]any{
		"ad_account_id": acct.ID,
		"platform":      acct.Platform,
		"resource_id":   resource.ID,
		"kind":          resource.Kind,
		"provider_type": resource.ProviderType,
		"default":       isDefault,
	})
}

func boolArgDefault(args map[string]any, key string, fallback bool) bool {
	value, ok := args[key].(bool)
	if !ok {
		return fallback
	}
	return value
}

func (a *App) refreshAndMatchTrackingSources(ctx *sdk.AppCtx, acct *adAccount, name string) ([]adResource, map[string]any) {
	discovered, errOut := a.discoverResources(ctx, acct, resourceTrackingSource)
	if errOut != nil {
		return nil, errOut
	}
	if err := a.replaceResources(ctx, acct, resourceTrackingSource, discovered); err != nil {
		return nil, mcpError(err.Error())
	}
	resources, err := a.listResources(ctx, acct, resourceTrackingSource)
	if err != nil {
		return nil, mcpError(err.Error())
	}
	matches := make([]adResource, 0)
	for _, resource := range resources {
		if resource.Status == "active" && strings.EqualFold(strings.TrimSpace(resource.DisplayName), name) {
			matches = append(matches, resource)
		}
	}
	return matches, nil
}

func trackingSourceSelectionRequired(matches []adResource) map[string]any {
	choices := make([]map[string]any, 0, len(matches))
	for _, resource := range matches {
		choices = append(choices, resource.response())
	}
	out := mcpError("multiple active tracking sources have this name; select one explicitly")
	out["code"] = "selection_required"
	out["purpose"] = trackingSourceDefaultPurpose
	out["resource_kind"] = resourceTrackingSource
	out["choices"] = choices
	return out
}

func trackingSourcesNotPreviouslyPresent(matches []adResource, known map[string]bool) []adResource {
	out := make([]adResource, 0, len(matches))
	for _, match := range matches {
		if !known[match.NativeID] {
			out = append(out, match)
		}
	}
	return out
}

func trackingSourceNativeID(parsed any) string {
	root := asMap(parsed)
	if id := firstString(root, "id", "pixel_id"); id != "" {
		return id
	}
	return firstString(asMap(root["data"]), "id", "pixel_id")
}

func shouldReconcileTrackingSourceCreate(errOut map[string]any) bool {
	if errOut["retryable"] == true {
		return true
	}
	status := intArg(errOut, "provider_status", 0)
	if status == 408 || status == 429 || status >= 500 {
		return true
	}
	message := strings.ToLower(mcpErrorMessage(errOut))
	return strings.Contains(message, "timeout") || strings.Contains(message, "timed out") || strings.Contains(message, "connection reset")
}

func (a *App) trackingSourceResult(
	ctx *sdk.AppCtx,
	acct *adAccount,
	resource *adResource,
	created, reused, reconciled, setDefault bool,
) (map[string]any, error) {
	if setDefault {
		if err := a.setResourceDefault(ctx, acct, trackingSourceDefaultPurpose, resource.ID); err != nil {
			return nil, err
		}
	}
	return map[string]any{
		"created": created, "reused": reused, "reconciled": reconciled,
		"default":                 setDefault,
		"default_selected":        setDefault,
		"resource":                resource.response(),
		"site_tracking_installed": false,
	}, nil
}

// Google Ads models conversion tracking as a ConversionAction rather than a
// Pixel. The write path has always existed on the provider
// (ConversionActionService.MutateConversionActions); only this app was missing
// it, which forced a Purchase action to be created in the Google UI and
// blocked Smart Bidding from being configured programmatically.

var googleConversionCategories = map[string]string{
	"purchase":         "PURCHASE",
	"lead":             "LEAD",
	"signup":           "SIGNUP",
	"page_view":        "PAGE_VIEW",
	"download":         "DOWNLOAD",
	"add_to_cart":      "ADD_TO_CART",
	"begin_checkout":   "BEGIN_CHECKOUT",
	"subscribe_paid":   "SUBSCRIBE_PAID",
	"submit_lead_form": "SUBMIT_LEAD_FORM",
	"contact":          "CONTACT",
	"other":            "DEFAULT",
}

func googleConversionCategory(raw string) (string, error) {
	value := strings.TrimSpace(strings.ToLower(raw))
	if value == "" {
		return "PURCHASE", nil
	}
	if mapped, ok := googleConversionCategories[value]; ok {
		return mapped, nil
	}
	return "", fmt.Errorf("category must be one of purchase, lead, signup, page_view, download, add_to_cart, begin_checkout, subscribe_paid, submit_lead_form, contact, other")
}

// A purchase can legitimately happen more than once per click; a lead should
// be counted once. Getting this wrong quietly distorts Smart Bidding.
func googleConversionCounting(category string) string {
	switch category {
	case "PURCHASE", "ADD_TO_CART", "BEGIN_CHECKOUT", "SUBSCRIBE_PAID", "PAGE_VIEW":
		return "MANY_PER_CLICK"
	default:
		return "ONE_PER_CLICK"
	}
}

func (a *App) refreshAndMatchConversionActions(ctx *sdk.AppCtx, acct *adAccount, name string) ([]adResource, map[string]any) {
	discovered, errOut := a.discoverResources(ctx, acct, resourceConversionAction)
	if errOut != nil {
		return nil, errOut
	}
	if err := a.replaceResources(ctx, acct, resourceConversionAction, discovered); err != nil {
		return nil, mcpError(err.Error())
	}
	resources, err := a.listResources(ctx, acct, resourceConversionAction)
	if err != nil {
		return nil, mcpError(err.Error())
	}
	matches := make([]adResource, 0)
	for _, resource := range resources {
		if resource.Status == "active" && strings.EqualFold(strings.TrimSpace(resource.DisplayName), name) {
			matches = append(matches, resource)
		}
	}
	return matches, nil
}

func (a *App) createGoogleConversionAction(
	ctx *sdk.AppCtx, acct *adAccount, args map[string]any, name string,
) (any, error) {
	category, categoryErr := googleConversionCategory(stringArgAny(args, "category"))
	if categoryErr != nil {
		return mcpError(categoryErr.Error()), nil
	}
	setDefault := boolArgDefault(args, "set_default", true)
	reuseExisting := boolArgDefault(args, "reuse_existing", true)

	if reuseExisting {
		matches, refreshErr := a.refreshAndMatchConversionActions(ctx, acct, name)
		if refreshErr != nil {
			return refreshErr, nil
		}
		if len(matches) > 1 {
			return trackingSourceSelectionRequired(matches), nil
		}
		if len(matches) == 1 {
			return a.trackingSourceResult(ctx, acct, &matches[0], false, true, false, setDefault)
		}
	}

	conversionAction := map[string]any{
		"name":           name,
		"type":           "WEBPAGE",
		"category":       category,
		"status":         "ENABLED",
		"primaryForGoal": true,
		"countingType":   googleConversionCounting(category),
	}
	if cents := intArg(args, "default_value_cents", -1); cents >= 0 {
		conversionAction["valueSettings"] = map[string]any{
			"defaultValue":          float64(cents) / 100,
			"alwaysUseDefaultValue": false,
		}
	}
	parsed, createErr := a.execIntegrationTool(ctx, acct, "conversion_action_mutate", map[string]any{
		"customer_id": acct.NativeAccountID,
		"operations":  []any{map[string]any{"create": conversionAction}},
	})
	if createErr != nil {
		return createErr, nil
	}
	resourceName := firstResourceName(parsed)
	nativeID := googleAssetID(resourceName)
	if nativeID == "" {
		out := mcpError("conversion_action_mutate returned no resourceName")
		out["code"] = "provider_response_invalid"
		out["platform"] = acct.Platform
		return out, nil
	}
	resource, err := a.upsertResource(ctx, acct, discoveredResource{
		Kind: resourceConversionAction, ProviderType: "google_conversion_action", NativeID: nativeID,
		DisplayName: name, Status: "active",
		Capabilities: []string{"conversion_tracking", "smart_bidding"},
		Metadata:     map[string]any{"category": category, "resource_name": resourceName},
		ManagedByApp: true,
	})
	if err != nil {
		return nil, err
	}
	result, err := a.trackingSourceResult(ctx, acct, resource, true, false, false, setDefault)
	if err == nil {
		a.emitTrackingSourceCreated(ctx, acct, resource, setDefault)
	}
	return result, err
}

// Google embeds the conversion id and label in the event snippet's send_to
// value (AW-123456789/AbC-D_efGh). Surfacing them separately saves every
// caller from scraping the snippet.
var googleSendToPattern = regexp.MustCompile(`(AW-[0-9]+)/([A-Za-z0-9_-]+)`)

func (a *App) googleConversionInstallation(ctx *sdk.AppCtx, acct *adAccount, args map[string]any) (any, error) {
	resource, resourceErr := a.resolveResourceChoice(
		ctx, acct, trackingSourceDefaultPurpose, resourceConversionAction,
		"google_conversion_action", int64(intArg(args, "tracking_source_resource_id", 0)),
	)
	if resourceErr != nil {
		return resourceErr, nil
	}
	if !googleNumericID(resource.NativeID) {
		return mcpError("conversion action id is not numeric: " + resource.NativeID), nil
	}
	query := fmt.Sprintf(
		"SELECT conversion_action.id, conversion_action.name, conversion_action.status, "+
			"conversion_action.category, conversion_action.tag_snippets "+
			"FROM conversion_action WHERE conversion_action.id = %s", resource.NativeID,
	)
	rows, errOut := a.googleSearchRows(ctx, acct, query)
	if errOut != nil {
		return errOut, nil
	}
	installation := map[string]any{
		"provider":                   "google",
		"public_id":                  resource.NativeID,
		"script_origins":             []string{"https://www.googletagmanager.com"},
		"connect_origins":            []string{"https://www.googletagmanager.com", "https://www.google.com", "https://googleads.g.doubleclick.net"},
		"image_origins":              []string{"https://www.google.com", "https://googleads.g.doubleclick.net"},
		"requires_site_installation": true,
	}
	if len(rows) == 0 {
		// The action exists locally but the provider returned no snippet. Say so
		// rather than returning an installation block that looks complete.
		installation["snippet_available"] = false
		return map[string]any{"resource": resource.response(), "installation": installation}, nil
	}
	action := mapAt(rows[0], "conversionAction")
	if len(action) == 0 {
		action = mapAt(rows[0], "conversion_action")
	}
	snippets, _ := action["tagSnippets"].([]any)
	if len(snippets) == 0 {
		snippets, _ = action["tag_snippets"].([]any)
	}
	for _, entry := range snippets {
		snippet := asMap(entry)
		globalTag := firstString(snippet, "globalSiteTag", "global_site_tag")
		eventSnippet := firstString(snippet, "eventSnippet", "event_snippet")
		if globalTag == "" && eventSnippet == "" {
			continue
		}
		installation["global_site_tag"] = globalTag
		installation["event_snippet"] = eventSnippet
		installation["page_format"] = firstString(snippet, "pageFormat", "page_format")
		if match := googleSendToPattern.FindStringSubmatch(globalTag + " " + eventSnippet); len(match) == 3 {
			installation["conversion_id"] = match[1]
			installation["conversion_label"] = match[2]
			installation["send_to"] = match[1] + "/" + match[2]
		}
		break
	}
	installation["snippet_available"] = installation["event_snippet"] != nil
	installation["script_url"] = "https://www.googletagmanager.com/gtag/js?id=" + toString(installation["conversion_id"])
	if installation["conversion_id"] == nil {
		delete(installation, "script_url")
	}
	installation["category"] = firstString(action, "category")
	installation["status"] = firstString(action, "status")
	return map[string]any{"resource": resource.response(), "installation": installation}, nil
}
