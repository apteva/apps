package main

import (
	"strings"
	"unicode/utf8"

	sdk "github.com/apteva/app-sdk"
)

const trackingSourceDefaultPurpose = "conversion_source"

func trackingSourceCreateSchema() map[string]any {
	return schemaObject(map[string]any{
		"ad_account_id": map[string]any{"type": "integer"},
		"name":          map[string]any{"type": "string", "minLength": 1, "maxLength": 255},
		"set_default":   map[string]any{"type": "boolean", "default": true},
		"reuse_existing": map[string]any{
			"type": "boolean", "default": true,
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
