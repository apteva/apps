package main

import (
	"database/sql"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

const resourceMobileApp = "mobile_app"
const resourceMeasurementSource = "measurement_source"
const resourceConversionEvent = "conversion_event"

// Mobile support is additive. The same account/resource ownership checks and
// provider adapters used by website advertising also govern app advertising.
var mobileEvents = map[string]map[string]string{
	"meta":   {"install": "MOBILE_APP_INSTALL", "registration": "COMPLETE_REGISTRATION", "trial": "START_TRIAL", "purchase": "PURCHASE", "subscription": "SUBSCRIBE", "add_to_cart": "ADD_TO_CART", "custom": "CUSTOM"},
	"google": {"install": "DOWNLOAD", "first_open": "DOWNLOAD", "registration": "SIGNUP", "trial": "DEFAULT", "purchase": "PURCHASE", "subscription": "SUBSCRIBE_PAID", "custom": "DEFAULT"},
	"x":      {"install": "APP_INSTALLS", "reengagement": "APP_ENGAGEMENTS"},
	"reddit": {"install": "MOBILE_CONVERSION_INSTALL", "registration": "MOBILE_CONVERSION_SIGN_UP", "trial": "MOBILE_CONVERSION_START_TRIAL", "purchase": "MOBILE_CONVERSION_PURCHASE", "subscription": "MOBILE_CONVERSION_SUBSCRIBE", "add_to_cart": "MOBILE_CONVERSION_ADD_TO_CART", "first_purchase": "MOBILE_CONVERSION_FIRST_TIME_PURCHASE", "level_achieved": "MOBILE_CONVERSION_LEVEL_ACHIEVED"},
}

func mobileCapabilities(platform string) map[string]any {
	events := []string{}
	for event := range mobileEvents[platform] {
		events = append(events, event)
	}
	sort.Strings(events)
	sources := map[string][]string{"meta": {"meta_sdk", "mmp"}, "google": {"firebase", "google_play", "mmp"}, "x": {"mmp"}, "reddit": {"mmp"}}[platform]
	goals := []string{"installs", "in_app_event"}
	if platform == "meta" || platform == "google" {
		goals = append(goals, "value")
	}
	if platform == "x" {
		goals = []string{"installs", "reengagement"}
	}
	return map[string]any{"supported": mobileEvents[platform] != nil, "os": []string{"android", "ios"}, "goals": goals, "events": events, "measurement_sources": sources, "operations": []string{"bind", "discover", "readiness", "report"}, "creates_sdk": false, "requires_external_measurement": true, "requires_pixel": platform == "reddit", "app_discovery": platform != "x", "reengagement_campaigns": platform == "x"}
}

func (a *App) extendConversionTools(tools []sdk.Tool) []sdk.Tool {
	for i := range tools {
		t := &tools[i]
		props := asMap(t.InputSchema["properties"])
		switch t.Name {
		case "campaign_create", "adset_create", "creative_create", "ad_create":
			props["mobile_app_resource_id"] = map[string]any{"type": "integer", "description": "Project-scoped mobile_app resource. Extends the existing campaign/creative API."}
			props["app_goal"] = map[string]any{"type": "string", "enum": []string{"installs", "in_app_event", "value", "reengagement"}}
			props["conversion_event_resource_id"] = map[string]any{"type": "integer", "description": "Discovered app conversion event in this account, bound to the selected app."}
			props["measurement_source_resource_id"] = map[string]any{"type": "integer"}
			t.Description += " Mobile apps: select mobile_app_resource_id and app_goal, and conversion_event_resource_id for in-app goals. Use conversion_readiness_get before activation."
			props["idempotency_key"] = map[string]any{"type": "string", "maxLength": 200}
			if t.Name == "creative_create" {
				props["app_country_code"] = map[string]any{"type": "string"}
				props["media_keys"] = map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
			}
			if t.Name == "campaign_create" {
				props["idempotency_key"] = map[string]any{"type": "string", "maxLength": 200}
				props["channel_type"].(map[string]any)["enum"] = []string{"search", "performance_max", "display", "shopping", "video", "demand_gen", "app"}
			}
			if t.Name == "adset_create" {
				props["conversion_location"].(map[string]any)["enum"] = []string{"website", "instant_form", "calls", "messages", "app"}
			}
			if t.Name == "ad_create" {
				props["ad_format"] = map[string]any{"type": "string", "enum": []string{"responsive_search", "app"}}
				props["image_asset_ids"] = map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
				props["video_asset_ids"] = map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
			}
		case "delivery_activate", "campaign_resume", "campaign_update", "adset_update", "ad_update":
			props["acknowledge_unverified_measurement"] = map[string]any{"type": "boolean", "description": "Explicit acknowledgement of externally verified mobile measurement when provider diagnostics are unknown; cannot bypass ineligible events."}
		case "resource_list":
			props["kind"].(map[string]any)["enum"] = []string{resourceIdentity, resourceTrackingSource, resourceConversionAction, resourceLeadForm, resourceAudience, resourceCreativeAsset, resourceFundingSource, resourceMobileApp, resourceMeasurementSource, resourceConversionEvent}
		}
	}
	common := func(extra map[string]any, required ...string) map[string]any {
		p := map[string]any{"ad_account_id": map[string]any{"type": "integer"}, "mobile_app_resource_id": map[string]any{"type": "integer"}, "refresh": map[string]any{"type": "boolean", "default": true}}
		for k, v := range extra {
			p[k] = v
		}
		return schemaObject(p, append([]string{"ad_account_id"}, required...))
	}
	str := func() map[string]any { return map[string]any{"type": "string"} }
	return append(tools,
		sdk.Tool{Name: "conversion_capabilities_get", Description: "Get provider-specific mobile app goals, event kinds, measurement sources and requirements.", InputSchema: common(nil), Handler: a.toolConversionCapabilities},
		sdk.Tool{Name: "mobile_app_bind", Description: "Bind an existing store app to this ad account; does not create provider apps or install SDKs. Native Meta app_id is the Meta application id, not the store id.", InputSchema: common(map[string]any{"name": str(), "os": map[string]any{"type": "string", "enum": []string{"android", "ios"}}, "store_url": str(), "app_id": str()}, "name", "os", "store_url", "app_id"), Handler: a.toolMobileAppBind},
		sdk.Tool{Name: "measurement_source_bind", Description: "Record the app's existing measurement source without claiming that events have been received. Credentials stay in provider integrations.", InputSchema: common(map[string]any{"source": str(), "name": str()}, "mobile_app_resource_id", "source"), Handler: a.toolMeasurementSourceBind},
		sdk.Tool{Name: "conversion_event_list", Description: "Discover provider conversion events and readiness for one selected mobile app. Reuses Google's conversion_action registry.", InputSchema: common(nil, "mobile_app_resource_id"), Handler: a.toolConversionEventList},
		sdk.Tool{Name: "conversion_event_enable", Description: "Enable an existing HIDDEN Google mobile conversion action. Never creates a website action or imports synthetic mobile events.", InputSchema: common(map[string]any{"conversion_event_resource_id": map[string]any{"type": "integer"}}, "mobile_app_resource_id", "conversion_event_resource_id"), Handler: a.toolConversionEventEnable},
		sdk.Tool{Name: "conversion_readiness_get", Description: "Check app access, measurement binding, event observation and optimization eligibility separately. Unknown diagnostics are never reported as ready.", InputSchema: common(map[string]any{"conversion_event_resource_id": map[string]any{"type": "integer"}, "measurement_source_resource_id": map[string]any{"type": "integer"}}, "mobile_app_resource_id"), Handler: a.toolConversionReadiness},
		sdk.Tool{Name: "conversion_performance_get", Description: "Get event-level conversions, installs, CPI and revenue separately from spend. Sources and attribution windows are not summed together. Extends performance_get for website and app events.", InputSchema: common(map[string]any{"level": str(), "date_from": str(), "date_to": str(), "entity_ids": map[string]any{"type": "array", "items": str()}, "event": str(), "measurement_source": str()}, "date_from", "date_to"), Handler: a.toolConversionPerformance},
	)
}

var androidPackage = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*(\.[A-Za-z][A-Za-z0-9_]*)+$`)

func storeApp(os, raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil {
		return "", fmt.Errorf("store_url must be an HTTPS App Store or Google Play URL")
	}
	if os == "android" && u.Hostname() == "play.google.com" && u.Path == "/store/apps/details" && androidPackage.MatchString(u.Query().Get("id")) {
		return u.Query().Get("id"), nil
	}
	if os == "ios" && u.Hostname() == "apps.apple.com" {
		for _, part := range strings.Split(u.Path, "/") {
			if strings.HasPrefix(part, "id") && asciiDigits(strings.TrimPrefix(part, "id")) {
				return strings.TrimPrefix(part, "id"), nil
			}
		}
	}
	return "", fmt.Errorf("store_url does not match the selected operating system or has no app identifier")
}

func (a *App) toolConversionCapabilities(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	acct, _, out := a.resolveAdAccount(ctx, args)
	if out != nil {
		return out, nil
	}
	return mobileCapabilities(acct.Platform), nil
}
func (a *App) mobileApp(ctx *sdk.AppCtx, acct *adAccount, id int64) (*adResource, map[string]any) {
	r, err := a.getResource(ctx, acct, id)
	if err != nil || r.Kind != resourceMobileApp || r.Status != "active" {
		return nil, mcpError("mobile app not active in this ad account")
	}
	return r, nil
}
func (a *App) toolMobileAppBind(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	acct, _, out := a.resolveAdAccount(ctx, args)
	if out != nil {
		return out, nil
	}
	os := stringArgAny(args, "os")
	store := strings.TrimSpace(stringArgAny(args, "store_url"))
	id, err := storeApp(os, store)
	if err != nil {
		return mcpError(err.Error()), nil
	}
	native := strings.TrimSpace(stringArgAny(args, "app_id"))
	name := strings.TrimSpace(stringArgAny(args, "name"))
	if name == "" || native == "" {
		return mcpError("name and a valid app_id required"), nil
	}
	if acct.Platform == "meta" {
		if !asciiDigits(native) {
			return mcpError("Meta app_id must be a numeric Meta application id"), nil
		}
	} else if native != id {
		return mcpError("app_id must match the app identifier in store_url"), nil
	}
	r, err := a.upsertResource(ctx, acct, discoveredResource{Kind: resourceMobileApp, ProviderType: acct.Platform + "_mobile_app", NativeID: native + ":" + os, DisplayName: name, Status: "active", ManagedByApp: true, Capabilities: []string{"app_promotion"}, Metadata: map[string]any{"app_id": native, "store_id": id, "os": os, "store_url": store}})
	if err != nil {
		return nil, err
	}
	ctx.Emit("mobile_app.changed", map[string]any{"ad_account_id": acct.ID, "resource_id": r.ID})
	return map[string]any{"resource": r.response(), "measurement_status": "unknown"}, nil
}
func (a *App) toolMeasurementSourceBind(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	acct, _, out := a.resolveAdAccount(ctx, args)
	if out != nil {
		return out, nil
	}
	app, out := a.mobileApp(ctx, acct, int64(intArg(args, "mobile_app_resource_id", 0)))
	if out != nil {
		return out, nil
	}
	source := stringArgAny(args, "source")
	allowed := false
	for _, v := range mobileCapabilities(acct.Platform)["measurement_sources"].([]string) {
		if v == source {
			allowed = true
		}
	}
	if !allowed {
		return mcpError("measurement source is unsupported for this provider"), nil
	}
	name := strings.TrimSpace(stringArgAny(args, "name"))
	if name == "" {
		name = source
	}
	r, err := a.upsertResource(ctx, acct, discoveredResource{Kind: resourceMeasurementSource, ProviderType: source, NativeID: fmt.Sprintf("%d:%s", app.ID, source), ParentNativeID: app.NativeID, DisplayName: name, Status: "active", ManagedByApp: true, Metadata: map[string]any{"mobile_app_resource_id": app.ID, "source": source, "verification": "unknown"}})
	if err != nil {
		return nil, err
	}
	ctx.Emit("mobile_app.changed", map[string]any{"ad_account_id": acct.ID, "resource_id": app.ID})
	return map[string]any{"resource": r.response(), "configured": true, "event_observed": "unknown", "next_action": measurementInstructions(acct.Platform, source)}, nil
}
func measurementInstructions(platform, source string) string {
	switch platform {
	case "google":
		return "Install the Firebase SDK or configure Google Play/MMP measurement, link it to this Google Ads account, import the app events in Google Ads, then refresh events."
	case "meta":
		return "Configure Meta App Events through the app SDK or your measurement partner, grant this ad account access to the Meta app, then refresh events."
	default:
		return "Configure your supported mobile measurement partner to send this app's install and in-app event postbacks to the advertiser account, then refresh events."
	}
}
func mobileEventName(platform, native string) string {
	if platform == "meta" {
		native = metaOptimizationEvent(native)
	}
	for name, v := range mobileEvents[platform] {
		if strings.EqualFold(v, native) {
			return name
		}
	}
	return "custom"
}

func (a *App) discoverMobileEvents(ctx *sdk.AppCtx, acct *adAccount, app *adResource) ([]adResource, map[string]any) {
	native := firstString(app.Metadata, "app_id")
	found := []discoveredResource{}
	switch acct.Platform {
	case "google":
		query := "SELECT conversion_action.id, conversion_action.name, conversion_action.status, conversion_action.type, conversion_action.category, conversion_action.app_id, conversion_action.owner_customer, conversion_action.primary_for_goal FROM conversion_action WHERE conversion_action.app_id = '" + native + "'"
		rows, out := a.googleResourceRows(ctx, acct, query)
		if out != nil {
			return nil, out
		}
		for _, row := range rows {
			v := mapAt(row, "conversionAction")
			if len(v) == 0 {
				v = mapAt(row, "conversion_action")
			}
			typ := firstString(v, "type")
			id := firstString(v, "id")
			if id == "" || firstString(v, "appId", "app_id") != native || !googleMobileConversionType(typ) {
				continue
			}
			owner := firstString(v, "ownerCustomer", "owner_customer")
			eligible := strings.EqualFold(firstString(v, "status"), "ENABLED")
			if primary, ok := v["primaryForGoal"].(bool); ok && !primary {
				eligible = false
			}
			if primary, ok := v["primary_for_goal"].(bool); ok && !primary {
				eligible = false
			}
			found = append(found, discoveredResource{Kind: resourceConversionAction, ProviderType: "google_conversion_action", NativeID: id, DisplayName: firstString(v, "name"), Status: normalizedResourceStatus(firstString(v, "status")), Capabilities: []string{"conversion_tracking", "mobile"}, Metadata: map[string]any{"type": typ, "category": v["category"], "app_id": native, "mobile_app_resource_id": app.ID, "event": googleConversionEvent(v), "eligible": eligible, "owner_customer": owner, "observation": "unknown"}})
		}
	case "meta":
		rows, out := a.providerResourceRows(ctx, acct, "mobile_app_list", map[string]any{"adAccountId": acct.NativeAccountID, "fields": "id,name,app_install_tracked,object_store_urls,advertisable_app_events", "limit": 100}, "meta")
		if out != nil {
			return nil, out
		}
		accessible := false
		for _, v := range rows {
			if firstString(v, "id") != native {
				continue
			}
			accessible = true
			tracked, _ := v["app_install_tracked"].(bool)
			found = append(found, discoveredResource{Kind: resourceConversionEvent, ProviderType: "meta_app_event", NativeID: app.NativeID + ":MOBILE_APP_INSTALL", DisplayName: "App install", Status: "active", ParentNativeID: app.NativeID, Metadata: map[string]any{"app_id": native, "mobile_app_resource_id": app.ID, "event": "install", "native_event": "MOBILE_APP_INSTALL", "eligible": tracked, "observation": "unknown"}})
			raw, _ := v["advertisable_app_events"].([]any)
			for _, e := range raw {
				event := toString(e)
				if m := asMap(e); len(m) > 0 {
					event = firstString(m, "event_name", "name", "event")
				}
				if event == "" {
					continue
				}
				found = append(found, discoveredResource{Kind: resourceConversionEvent, ProviderType: "meta_app_event", NativeID: app.NativeID + ":" + event, DisplayName: event, Status: "active", ParentNativeID: app.NativeID, Metadata: map[string]any{"app_id": native, "mobile_app_resource_id": app.ID, "event": mobileEventName("meta", event), "native_event": event, "eligible": true, "observation": "unknown"}})
			}
		}
		if !accessible {
			return nil, mcpError("Meta app is not advertised by this account; grant app access and refresh")
		}
	case "reddit":
		rows, out := a.providerResourceRows(ctx, acct, "list_apps", map[string]any{"ad_account_id": acct.NativeAccountID, "page.size": 200}, "reddit")
		if out != nil {
			return nil, out
		}
		accessible := false
		for _, v := range rows {
			if firstString(v, "id") == native {
				accessible = true
			}
		}
		if !accessible {
			return nil, mcpError("mobile app not found in this Reddit ad account; configure measurement and refresh")
		}
		parsed, out := a.execIntegrationTool(ctx, acct, "app_last_fired_at", map[string]any{"app_id": native})
		if out != nil {
			return nil, out
		}
		v := asMap(asMap(parsed)["data"])
		for event, provider := range mobileEvents["reddit"] {
			last := firstString(v, strings.ToLower(provider))
			eligible := false
			observation := "not_observed"
			if when, err := time.Parse(time.RFC3339, last); err == nil {
				observation = "observed"
				eligible = time.Since(when) >= 0 && time.Since(when) <= 7*24*time.Hour
			}
			found = append(found, discoveredResource{Kind: resourceConversionEvent, ProviderType: "reddit_app_event", NativeID: native + ":" + provider, DisplayName: event, Status: "active", ParentNativeID: app.NativeID, Metadata: map[string]any{"app_id": native, "mobile_app_resource_id": app.ID, "event": event, "native_event": provider, "eligible": eligible, "observation": observation, "last_received_at": last}})
		}
	case "x":
		// X's public API cannot attest measurement partner setup. Preserve this as
		// unknown rather than interpreting a locally recorded partner as verified.
		for event, provider := range mobileEvents["x"] {
			found = append(found, discoveredResource{Kind: resourceConversionEvent, ProviderType: "x_app_event", NativeID: native + ":" + provider, DisplayName: event, Status: "active", ParentNativeID: app.NativeID, Metadata: map[string]any{"app_id": native, "mobile_app_resource_id": app.ID, "event": event, "native_event": provider, "eligible": "unknown", "observation": "unknown"}})
		}
	}
	if _, err := ctx.AppDB().Exec(`UPDATE ad_resources SET status='stale' WHERE project_id=? AND ad_account_id=? AND kind IN ('conversion_event','conversion_action') AND json_extract(metadata_json,'$.mobile_app_resource_id')=?`, acct.ProjectID, acct.ID, app.ID); err != nil {
		return nil, mcpError(err.Error())
	}
	for _, r := range found {
		if _, err := a.upsertResource(ctx, acct, r); err != nil {
			return nil, mcpError(err.Error())
		}
	}
	resources, err := a.listResources(ctx, acct, "")
	if err != nil {
		return nil, mcpError(err.Error())
	}
	out := []adResource{}
	for _, r := range resources {
		if (r.Kind == resourceConversionEvent || r.Kind == resourceConversionAction) && int64(intArg(r.Metadata, "mobile_app_resource_id", 0)) == app.ID {
			out = append(out, r)
		}
	}
	return out, nil
}
func googleMobileConversionType(typ string) bool {
	return strings.Contains(typ, "APP") || strings.HasPrefix(typ, "GOOGLE_PLAY_") || strings.HasPrefix(typ, "FIREBASE_") || strings.HasPrefix(typ, "THIRD_PARTY_APP_")
}
func googleMobileEvent(category, name string) string {
	if category == "DOWNLOAD" {
		if strings.Contains(strings.ToLower(name), "first_open") {
			return "first_open"
		}
		return "install"
	}
	for k, v := range mobileEvents["google"] {
		if v == category && category != "DEFAULT" {
			return k
		}
	}
	return "custom"
}

func (a *App) toolConversionEventList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	acct, _, out := a.resolveAdAccount(ctx, args)
	if out != nil {
		return out, nil
	}
	app, out := a.mobileApp(ctx, acct, int64(intArg(args, "mobile_app_resource_id", 0)))
	if out != nil {
		return out, nil
	}
	var resources []adResource
	if boolArgDefault(args, "refresh", true) {
		var out map[string]any
		resources, out = a.discoverMobileEvents(ctx, acct, app)
		if out != nil {
			return out, nil
		}
	} else {
		all, err := a.listResources(ctx, acct, "")
		if err != nil {
			return nil, err
		}
		for _, r := range all {
			if (r.Kind == resourceConversionEvent || r.Kind == resourceConversionAction) && int64(intArg(r.Metadata, "mobile_app_resource_id", 0)) == app.ID {
				resources = append(resources, r)
			}
		}
	}
	items := []map[string]any{}
	for _, r := range resources {
		items = append(items, r.response())
	}
	return map[string]any{"data": items}, nil
}
func (a *App) mobileEvent(ctx *sdk.AppCtx, acct *adAccount, app *adResource, id int64) (*adResource, map[string]any) {
	r, err := a.getResource(ctx, acct, id)
	if err != nil || (r.Kind != resourceConversionEvent && r.Kind != resourceConversionAction) || int64(intArg(r.Metadata, "mobile_app_resource_id", 0)) != app.ID {
		return nil, mcpError("conversion event does not belong to this mobile app and ad account")
	}
	return r, nil
}
func (a *App) toolConversionEventEnable(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	acct, _, out := a.resolveAdAccount(ctx, args)
	if out != nil {
		return out, nil
	}
	if acct.Platform != "google" {
		return mcpError("enabling imported conversion actions is supported by Google only"), nil
	}
	app, out := a.mobileApp(ctx, acct, int64(intArg(args, "mobile_app_resource_id", 0)))
	if out != nil {
		return out, nil
	}
	event, out := a.mobileEvent(ctx, acct, app, int64(intArg(args, "conversion_event_resource_id", 0)))
	if out != nil {
		return out, nil
	}
	if !googleMobileConversionType(firstString(event.Metadata, "type")) || !asciiDigits(event.NativeID) {
		return mcpError("event is not a supported Google mobile conversion action"), nil
	}
	owner := firstString(event.Metadata, "owner_customer")
	if owner != "" && owner != "customers/"+acct.NativeAccountID {
		return mcpError("conversion action belongs to a manager; enable it in the owning account"), nil
	}
	if event.Status != "active" {
		parsed, out := a.execIdempotentUpdate(ctx, acct, "conversion_action_mutate", map[string]any{"customer_id": acct.NativeAccountID, "operations": []any{map[string]any{"update": map[string]any{"resourceName": "customers/" + acct.NativeAccountID + "/conversionActions/" + event.NativeID, "status": "ENABLED"}, "updateMask": "status"}}})
		if out != nil {
			return out, nil
		}
		_ = parsed
	}
	_, out = a.discoverMobileEvents(ctx, acct, app)
	if out != nil {
		return out, nil
	}
	event, err := a.getResource(ctx, acct, event.ID)
	if err != nil {
		return nil, err
	}
	return event.response(), nil
}
func (a *App) measurementSource(ctx *sdk.AppCtx, acct *adAccount, app *adResource, id int64) (*adResource, map[string]any) {
	all, err := a.listResources(ctx, acct, resourceMeasurementSource)
	if err != nil {
		return nil, mcpError(err.Error())
	}
	matches := []adResource{}
	for _, r := range all {
		if int64(intArg(r.Metadata, "mobile_app_resource_id", 0)) == app.ID && r.Status == "active" && (id == 0 || id == r.ID) {
			matches = append(matches, r)
		}
	}
	if len(matches) != 1 {
		return nil, mcpError("select one measurement_source_resource_id bound to this app")
	}
	return &matches[0], nil
}
func (a *App) toolConversionReadiness(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	acct, _, out := a.resolveAdAccount(ctx, args)
	if out != nil {
		return out, nil
	}
	app, out := a.mobileApp(ctx, acct, int64(intArg(args, "mobile_app_resource_id", 0)))
	if out != nil {
		return out, nil
	}
	diagnostics := []string{}
	access := "unknown"
	events := []map[string]any{}
	if boolArgDefault(args, "refresh", true) {
		resources, errOut := a.discoverMobileEvents(ctx, acct, app)
		for _, r := range resources {
			events = append(events, r.response())
		}
		if errOut != nil {
			diagnostics = append(diagnostics, toolErrorString(errOut))
		} else if acct.Platform != "x" {
			access = "verified"
		}
	}
	source, sourceOut := a.measurementSource(ctx, acct, app, int64(intArg(args, "measurement_source_resource_id", 0)))
	configured := sourceOut == nil
	next := measurementInstructions(acct.Platform, "")
	observation := "unknown"
	eligibility := any("unknown")
	last := ""
	if id := int64(intArg(args, "conversion_event_resource_id", 0)); id > 0 {
		e, out := a.mobileEvent(ctx, acct, app, id)
		if out != nil {
			return out, nil
		}
		observation = firstString(e.Metadata, "observation")
		eligibility = e.Metadata["eligible"]
		if e.Status != "active" {
			eligibility = false
		}
		last = firstString(e.Metadata, "last_received_at")
	}
	sourceName := ""
	if source != nil {
		sourceName = firstString(source.Metadata, "source")
	}
	ready := configured && access == "verified" && eligibility == true && len(diagnostics) == 0
	return map[string]any{"events": events, "mobile_app": app.response(), "configured": configured, "app_access": access, "measurement_source": sourceName, "event_observed": observation, "last_received_at": last, "eligible_for_optimization": eligibility, "ready": ready, "can_create_paused": configured, "diagnostics": diagnostics, "next_action": next, "checked_at": time.Now().UTC().Format(time.RFC3339)}, nil
}

// prepareMobileArgs translates only explicitly selected mobile resources. It
// never changes the behavior of legacy website/native calls.
func (a *App) prepareMobileArgs(ctx *sdk.AppCtx, acct *adAccount, args map[string]any, operation string) (map[string]any, map[string]any) {
	args, inheritedError := a.inheritMobileArgs(ctx, acct, args, operation)
	if inheritedError != nil {
		return nil, inheritedError
	}
	id := int64(intArg(args, "mobile_app_resource_id", 0))
	if id == 0 {
		return args, nil
	}
	app, out := a.mobileApp(ctx, acct, id)
	if out != nil {
		return nil, out
	}
	args = cloneMap(args)
	goal := firstString(args, "app_goal")
	if goal == "" {
		goal = "installs"
	}
	allowed := false
	for _, g := range mobileCapabilities(acct.Platform)["goals"].([]string) {
		if g == goal {
			allowed = true
		}
	}
	if !allowed {
		return nil, mcpError("unsupported app_goal for " + acct.Platform)
	}
	source, sourceOut := a.measurementSource(ctx, acct, app, int64(intArg(args, "measurement_source_resource_id", 0)))
	if sourceOut != nil {
		return nil, sourceOut
	}
	if v := strings.ToUpper(firstString(args, "status")); v != "" && v != "PAUSED" {
		return nil, mcpError("create mobile campaigns, groups and ads PAUSED; inspect conversion readiness before activation")
	}
	if operation == "campaign" {
		if v := firstString(args, "objective"); v != "" && v != "app_promotion" {
			return nil, mcpError("selected mobile app requires objective=app_promotion")
		}
		args["objective"] = "app_promotion"
	}
	args["app_goal"] = goal
	args["status"] = "PAUSED"
	args["_mobile_app"] = app
	args["conversion_location"] = "app"
	native := firstString(app.Metadata, "app_id")
	store := firstString(app.Metadata, "store_url")
	os := firstString(app.Metadata, "os")
	var event *adResource
	if eventID := int64(intArg(args, "conversion_event_resource_id", 0)); eventID > 0 {
		event, out = a.mobileEvent(ctx, acct, app, eventID)
		if out != nil {
			return nil, out
		}
		if event.Status != "active" {
			return nil, mcpError("selected mobile event is not enabled")
		}
		args["_mobile_event"] = event
	}
	if (goal == "in_app_event" || goal == "value") && event == nil {
		return nil, mcpError("conversion_event_resource_id required for in-app event/value optimization")
	}
	if event != nil {
		if acct.Platform == "google" {
			actual := googleMeasurementSource(firstString(event.Metadata, "type"))
			if actual != "provider" && actual != firstString(source.Metadata, "source") {
				return nil, mcpError("selected event and measurement source differ")
			}
		}
		kind := firstString(event.Metadata, "event")
		if goal == "installs" && kind != "install" && kind != "first_open" {
			return nil, mcpError("install goal requires an install/first-open conversion event")
		}
		if (goal == "in_app_event" || goal == "value") && (kind == "install" || kind == "first_open") {
			return nil, mcpError("in-app goals require a post-install event")
		}
		if eligible, ok := event.Metadata["eligible"].(bool); ok && !eligible {
			return nil, mcpError("selected event is not currently eligible for optimization; check conversion_readiness_get")
		}
	}
	opts := cloneMap(asMap(args["platform_options"]))
	if opts == nil {
		opts = map[string]any{}
	}
	if acct.Platform == "x" && operation == "creative" && len(opts) > 0 {
		return nil, mcpError("native creative overrides are unavailable for a selected X app card")
	}
	for _, values := range []map[string]any{opts, asMap(opts["data"]), asMap(opts["ad_group"]), asMap(opts["ad"])} {
		for _, key := range []string{"status", "entity_status", "configured_status"} {
			if v := firstString(values, key); v != "" && strings.ToUpper(v) != "PAUSED" {
				return nil, mcpError("mobile creation native options must also be PAUSED")
			}
		}
	}
	args["platform_options"] = opts
	switch acct.Platform {
	case "meta":
		if operation != "ad_group" {
			break
		}
		promoted := map[string]any{"application_id": native, "object_store_url": store}
		optimization := "app_installs"
		if goal == "in_app_event" {
			optimization = "conversions"
		}
		if goal == "value" {
			optimization = "value"
		}
		if event != nil && goal != "installs" {
			p := firstString(event.Metadata, "native_event")
			if p == "" {
				return nil, mcpError("selected Meta event has no provider event name")
			}
			if mobileEventName("meta", p) == "custom" {
				promoted["custom_event_type"] = "OTHER"
				promoted["custom_event_str"] = p
			} else {
				promoted["custom_event_type"] = metaOptimizationEvent(p)
			}
		}
		args["promoted_object"] = promoted
		args["destination_type"] = "APP"
		args["optimization_goal"] = optimization
		targeting := cloneMap(asMap(args["targeting"]))
		if targeting == nil {
			targeting = map[string]any{}
		}
		targeting["user_os"] = []any{map[string]string{"android": "Android", "ios": "iOS"}[os]}
		args["targeting"] = targeting
		if opts["targeting"] != nil {
			return nil, mcpError("use normalized targeting for a selected mobile app")
		}
		for k, v := range promoted {
			if override := asMap(opts["promoted_object"])[k]; override != nil && toString(override) != toString(v) {
				return nil, mcpError("native promoted_object conflicts with selected mobile app/event")
			}
		}
		opts["promoted_object"] = promoted
		opts["destination_type"] = "APP"
		opts["optimization_goal"] = metaOptimizationGoal[optimization]
	case "google":
		args["channel_type"] = "app"
		if operation == "ad" {
			args["ad_format"] = "app"
		}
	case "x":
		if operation != "ad_group" {
			break
		}
		field := "android_app_store_identifier"
		if os == "ios" {
			field = "ios_app_store_identifier"
		}
		otherField := "ios_app_store_identifier"
		if os == "ios" {
			otherField = "android_app_store_identifier"
		}
		if firstString(opts, otherField) != "" {
			return nil, mcpError("native options specify a different mobile OS")
		}
		if opts["targeting"] != nil {
			return nil, mcpError("use normalized targeting.location_ids for selected X mobile apps")
		}
		opts[field] = firstString(app.Metadata, "store_id")
		opts["objective"] = "APP_INSTALLS"
		opts["goal"] = "APP_INSTALLS"
		if goal == "reengagement" {
			opts["objective"] = "APP_ENGAGEMENTS"
			opts["goal"] = "APP_CLICKS"
		}
		if opts["bid_strategy"] == nil {
			opts["bid_strategy"] = "AUTO"
		}
		args["optimization_goal"] = "app_installs"
	case "reddit":
		data := cloneMap(asMap(opts["data"]))
		if data == nil {
			data = cloneMap(opts)
		}
		if data == nil {
			data = map[string]any{}
		}
		data["app_id"] = native
		if operation != "campaign" && operation != "ad_group" {
			break
		}
		optimization := "MOBILE_CONVERSION_INSTALL"
		if event != nil {
			optimization = firstString(event.Metadata, "native_event")
		}
		args["optimization_goal"] = optimization
		data["optimization_goal"] = optimization
		if firstString(data, "bid_strategy") == "" {
			data["bid_strategy"] = "BIDLESS"
		}
		if firstString(data, "bid_type") == "" {
			data["bid_type"] = "CPM"
		}
		if firstString(data, "bid_strategy") == "BIDLESS" {
			data["bid_value"] = nil
		}
		if firstString(data, "bid_strategy") == "TARGET_CPX" {
			return nil, mcpError("TARGET_CPX needs provider eligibility; use BIDLESS or verified native configuration")
		}
		if operation == "ad_group" {
			targeting := cloneMap(asMap(args["targeting"]))
			if override := asMap(data["targeting"]); len(override) > 0 {
				targeting = cloneMap(override)
			}
			if targeting == nil {
				targeting = map[string]any{}
			}
			device := map[string]any{"type": "MOBILE", "os": strings.ToUpper(os)}
			if os == "ios" {
				device["min_version"] = "14"
			}
			targeting["devices"] = []any{device}
			targeting["platforms"] = []any{"MOBILE_NATIVE", "MOBILE_WEB"}
			args["targeting"] = targeting
			data["targeting"] = targeting
		}
		opts["data"] = data
	}
	if destination := firstString(args, "destination_url"); destination != "" {
		selected, err := storeApp(os, destination)
		if err != nil || selected != firstString(app.Metadata, "store_id") {
			return nil, mcpError("mobile destination_url must identify the selected store app")
		}
	}
	args["destination_url"] = store
	if firstString(args, "call_to_action") == "" {
		args["call_to_action"] = "download"
	}
	return args, nil
}

// Sanitized snapshots contain only public arguments, never internal pointers.
func mobilePublicArgs(args map[string]any) map[string]any {
	out := cloneMap(args)
	delete(out, "_mobile_app")
	delete(out, "_mobile_event")
	return out
}
func toolErrorString(out map[string]any) string { return mcpErrorTextValue(out) }

func (a *App) persistMobileCampaign(ctx *sdk.AppCtx, acct *adAccount, campaignID string, args map[string]any) error {
	app, _ := args["_mobile_app"].(*adResource)
	if app == nil {
		return nil
	}
	source, out := a.measurementSource(ctx, acct, app, int64(intArg(args, "measurement_source_resource_id", 0)))
	if out != nil {
		return fmt.Errorf("measurement binding disappeared")
	}
	_, err := ctx.AppDB().Exec(`INSERT INTO ad_mobile_campaigns(project_id,ad_account_id,campaign_id,mobile_app_resource_id,measurement_source_resource_id,conversion_event_resource_id,app_goal) VALUES(?,?,?,?,?,?,?) ON CONFLICT DO UPDATE SET mobile_app_resource_id=excluded.mobile_app_resource_id,measurement_source_resource_id=excluded.measurement_source_resource_id,conversion_event_resource_id=excluded.conversion_event_resource_id,app_goal=excluded.app_goal`, acct.ProjectID, acct.ID, campaignID, app.ID, source.ID, intArg(args, "conversion_event_resource_id", 0), stringArgAny(args, "app_goal"))
	return err
}
func (a *App) inheritMobileArgs(ctx *sdk.AppCtx, acct *adAccount, args map[string]any, operation string) (map[string]any, map[string]any) {
	if operation != "ad_group" && operation != "ad" {
		return args, nil
	}
	campaignID := firstString(args, "campaign_id")
	if campaignID == "" && operation == "ad" {
		var err error
		campaignID, err = a.campaignIDForEntity(ctx, acct, "ad_group", firstString(args, "adset_id"))
		if err != nil {
			campaignID = ""
		}
	}
	var appID, sourceID, eventID int64
	var goal string
	err := ctx.AppDB().QueryRow(`SELECT mobile_app_resource_id,measurement_source_resource_id,conversion_event_resource_id,app_goal FROM ad_mobile_campaigns WHERE project_id=? AND ad_account_id=? AND campaign_id=?`, acct.ProjectID, acct.ID, campaignID).Scan(&appID, &sourceID, &eventID, &goal)
	if errors.Is(err, sql.ErrNoRows) {
		return args, nil
	}
	if err != nil {
		return nil, mcpError("could not read mobile campaign binding: " + err.Error())
	}
	if id := int64(intArg(args, "mobile_app_resource_id", 0)); id > 0 && id != appID {
		return nil, mcpError("selected mobile app differs from the parent campaign")
	}
	for _, binding := range []struct {
		key string
		id  int64
	}{{"measurement_source_resource_id", sourceID}, {"conversion_event_resource_id", eventID}} {
		if selected := int64(intArg(args, binding.key, 0)); selected > 0 && selected != binding.id {
			return nil, mcpError("selected measurement differs from parent campaign")
		}
	}
	if selected := firstString(args, "app_goal"); selected != "" && selected != goal {
		return nil, mcpError("selected app goal differs from parent campaign")
	}
	args = cloneMap(args)
	args["mobile_app_resource_id"] = appID
	if intArg(args, "measurement_source_resource_id", 0) == 0 {
		args["measurement_source_resource_id"] = sourceID
	}
	if intArg(args, "conversion_event_resource_id", 0) == 0 {
		args["conversion_event_resource_id"] = eventID
	}
	if firstString(args, "app_goal") == "" {
		args["app_goal"] = goal
	}
	return args, nil
}

func (a *App) checkMobileActivation(ctx *sdk.AppCtx, acct *adAccount, args map[string]any, campaignID string) map[string]any {
	var appID, sourceID, eventID int64
	err := ctx.AppDB().QueryRow(`SELECT mobile_app_resource_id,measurement_source_resource_id,conversion_event_resource_id FROM ad_mobile_campaigns WHERE project_id=? AND ad_account_id=? AND campaign_id=?`, acct.ProjectID, acct.ID, campaignID).Scan(&appID, &sourceID, &eventID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return mcpError("could not verify mobile campaign binding: " + err.Error())
	}
	result, err := a.toolConversionReadiness(ctx, map[string]any{"ad_account_id": acct.ID, "mobile_app_resource_id": appID, "measurement_source_resource_id": sourceID, "conversion_event_resource_id": eventID, "refresh": true})
	if err != nil {
		return mcpError(err.Error())
	}
	if out := mcpResultError(result); out != nil {
		return out
	}
	readiness := asMap(result)
	if readiness["ready"] == true {
		return nil
	}
	diagnostics, _ := readiness["diagnostics"].([]string)
	if len(diagnostics) == 0 && readiness["configured"] == true && readiness["eligible_for_optimization"] == "unknown" && boolArgDefault(args, "acknowledge_unverified_measurement", false) {
		return nil
	}
	out := mcpError("mobile measurement readiness must be checked before activation; unknown measurement needs explicit acknowledgement, ineligible events must be repaired")
	out["code"] = "mobile_measurement_not_ready"
	out["readiness"] = readiness
	return out
}

func metaOptimizationEvent(native string) string {
	names := map[string]string{"fb_mobile_complete_registration": "COMPLETE_REGISTRATION", "fb_mobile_start_trial": "START_TRIAL", "fb_mobile_purchase": "PURCHASE", "fb_mobile_subscribe": "SUBSCRIBE", "fb_mobile_add_to_cart": "ADD_TO_CART"}
	if canonical := names[strings.ToLower(native)]; canonical != "" {
		return canonical
	}
	return native
}
func googleMeasurementSource(typ string) string {
	if strings.Contains(typ, "FIREBASE") {
		return "firebase"
	}
	if strings.Contains(typ, "THIRD_PARTY_APP") {
		return "mmp"
	}
	if strings.Contains(typ, "GOOGLE_PLAY") {
		return "google_play"
	}
	return "provider"
}

func mobileActivationRequested(args map[string]any) bool {
	for _, m := range []map[string]any{args, asMap(args["platform_options"]), asMap(asMap(args["platform_options"])["data"]), asMap(asMap(args["platform_options"])["campaign"]), asMap(asMap(args["platform_options"])["ad_group"]), asMap(asMap(args["platform_options"])["ad"])} {
		for _, k := range []string{"status", "entity_status", "configured_status"} {
			v := strings.ToUpper(firstString(m, k))
			if v == "ACTIVE" || v == "ENABLED" {
				return true
			}
		}
	}
	return false
}

func googleConversionEvent(v map[string]any) string {
	typ := firstString(v, "type")
	if strings.HasSuffix(typ, "_FIRST_OPEN") {
		return "first_open"
	}
	if strings.HasSuffix(typ, "_IN_APP_PURCHASE") {
		return "purchase"
	}
	return googleMobileEvent(firstString(v, "category"), firstString(v, "name"))
}
