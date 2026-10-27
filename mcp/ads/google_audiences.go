package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	sdk "github.com/apteva/app-sdk"
)

func extendAudienceCapabilities(platform string, out map[string]any) {
	kinds := audienceCapabilities[platform].Kinds
	create := []string{"customer_list"}
	if platform == "meta" {
		create = append(create, "engagement", "lookalike")
	}
	if platform == "reddit" {
		create = append(create, "saved_targeting")
	}
	if platform == "google" {
		create = append(create, "custom_segment", "grouped", "lookalike")
	}
	out["create_kinds"] = create
	byKind := map[string]any{}
	for _, kind := range kinds {
		byKind[kind] = map[string]any{"create": containsString(create, kind), "operations": audienceResourceCapabilities(platform, kind)}
	}
	if platform == "google" {
		for _, kind := range []string{"customer_list", "custom_segment", "grouped"} {
			fields := []string{"name", "description"}
			if kind == "custom_segment" {
				fields = append(fields, "keywords", "urls", "apps", "segment_mode")
			}
			if kind == "grouped" {
				fields = append(fields, "segment_audience_ids", "user_interest_ids", "replace_segments")
			}
			asMap(byKind[kind])["update_fields"] = fields
		}
	}
	out["by_kind"] = byKind
	if platform != "google" {
		return
	}
	out["custom_segment"] = map[string]any{"member_types": []string{"keyword", "url", "app"}, "modes": []string{"interests", "search_terms"}, "member_match": "any", "required_integration_tool": "custom_audience_mutate"}
	out["lookalike"] = map[string]any{"expansion_levels": []string{"narrow", "balanced", "broad"}, "country_codes_required": true, "minimum_active_matched_seed_members": 100, "eligibility": "provider_evaluated", "campaign_types": []string{"demand_gen", "video"}, "seed_types": []string{"customer_list", "website", "app_activity", "remarketing", "rule_based"}, "ready_immediately": false, "update": false, "delete": false, "mutation_restriction": "Google v23 rejects lookalike updates and removal with USER_LIST_MUTATE_NOT_SUPPORTED, including readOnly=false lists"}
	out["engagement"] = map[string]any{"create": false, "discover": true, "seed_existing": true, "classification": "provider list types do not verify the source; use verified audience provenance", "reason": "Google Ads API cannot create YouTube viewer/subscriber lists", "setup_url": "https://ads.google.com/aw/audiences/management"}
	out["grouped"] = map[string]any{"segment_types": []string{"user_list", "custom_segment", "interest"}, "local_ids_field": "segment_audience_ids", "targeting_field": "audience_resource_ids", "delete": false}
}

func (a *App) extendAudienceTools(tools []sdk.Tool) []sdk.Tool {
	for i := range tools {
		t := &tools[i]
		p := asMap(t.InputSchema["properties"])
		switch t.Name {
		case "audience_list":
			p["type"] = map[string]any{"type": "string", "description": "Filter normalized audience type (custom_segment, grouped, lookalike, remarketing, customer_list, website, app_activity). Omit to discover all types."}
		case "audience_create", "audience_create_custom", "audience_create_lookalike", "audience_update":
			p["idempotency_key"] = map[string]any{"type": "string"}
			for _, key := range []string{"keywords", "urls", "apps", "country_codes", "user_interest_ids"} {
				p[key] = map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
			}
			for _, key := range []string{"source_audience_ids", "segment_audience_ids"} {
				p[key] = map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "Local, project-scoped audience IDs in this ad account."}
			}
			p["expansion_level"] = map[string]any{"type": "string", "enum": []string{"narrow", "balanced", "broad"}}
			p["replace_segments"] = map[string]any{"type": "boolean", "description": "Explicitly replace all grouped audience segments on update."}
			p["segment_mode"] = map[string]any{"type": "string", "enum": []string{"interests", "search_terms"}}
			if t.Name == "audience_create_custom" {
				p["audience_type"] = map[string]any{"type": "string", "enum": []string{"user_list", "grouped", "custom_segment"}}
			}
			if t.Name == "audience_create_lookalike" {
				p["source_audience_id"] = map[string]any{"anyOf": []any{map[string]any{"type": "integer"}, map[string]any{"type": "string"}}, "description": "Local audience ID. Legacy Meta native string IDs remain supported."}
				t.InputSchema["required"] = []string{"ad_account_id", "name"}
			}
			t.Description += " Google: custom_segment uses keywords/urls/apps and segment_mode; grouped combines local segment_audience_ids and discovered user_interest_ids; lookalike uses local source_audience_ids, country_codes and expansion_level. Eligibility and readiness are provider-evaluated. YouTube seed lists must first be created in Google Ads."
		case "adset_create", "adset_update":
			p["audience_resource_ids"] = map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "Local grouped audience IDs in this account. Google Demand Gen resolves these to native Audience resources; mutually exclusive with native audience_ids."}
		}
	}
	return a.extendAudienceProvenanceTools(tools)
}

var countryCode = regexp.MustCompile(`^[A-Z]{2}$`)

// Creation accepts typed member signals; updates merge omitted collections from saved provider metadata.
func googleCustomSegment(args map[string]any) (map[string]any, error) {
	mode := firstString(args, "segment_mode")
	if mode == "" {
		mode = "interests"
	}
	nativeType := map[string]string{"interests": "AUTO", "search_terms": "SEARCH"}[mode]
	if nativeType == "" {
		return nil, fmt.Errorf("segment_mode must be interests or search_terms")
	}
	members := []any{}
	for _, spec := range []struct{ key, kind, field string }{{"keywords", "KEYWORD", "keyword"}, {"urls", "URL", "url"}, {"apps", "APP", "app"}} {
		seen := map[string]bool{}
		for _, raw := range asSlice(args[spec.key]) {
			value, ok := raw.(string)
			value = strings.TrimSpace(value)
			if !ok || value == "" {
				return nil, fmt.Errorf("%s must contain nonempty strings", spec.key)
			}
			if seen[value] {
				continue
			}
			seen[value] = true
			switch spec.key {
			case "keywords":
				if utf8.RuneCountInString(value) > 80 || len(strings.Fields(value)) > 10 {
					return nil, fmt.Errorf("keywords must have at most 80 characters and 10 words")
				}
			case "urls":
				// Google canonicalizes URL signals to domains without a scheme on readback.
				if !strings.Contains(value, "://") {
					value = "https://" + value
				}
				u, err := url.Parse(value)
				if err != nil || u.Hostname() == "" || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") || len(value) > 2048 {
					return nil, fmt.Errorf("urls must contain HTTP(S) URLs without credentials, at most 2048 characters")
				}
			case "apps":
				if !androidPackage.MatchString(value) {
					return nil, fmt.Errorf("apps must contain Android package names")
				}
			}
			members = append(members, map[string]any{"memberType": spec.kind, spec.field: value})
		}
	}
	if len(members) < 1 || len(members) > 500 {
		return nil, fmt.Errorf("custom_segment requires 1–500 unique keyword, URL or app members")
	}
	return map[string]any{"type": nativeType, "members": members}, nil
}

func googleAudienceResource(acct *adAccount, resource *adResource) string {
	collection := map[string]string{"google_custom_segment": "customAudiences", "google_grouped_audience": "audiences"}[resource.ProviderType]
	if collection == "" {
		collection = "userLists"
	}
	return "customers/" + acct.NativeAccountID + "/" + collection + "/" + resource.NativeID
}

func (a *App) googleGroupedSegments(ctx *sdk.AppCtx, acct *adAccount, args map[string]any) (map[string]any, error) {
	native := cloneMap(args)
	users, customs := append([]any{}, asSlice(args["user_list_ids"])...), append([]any{}, asSlice(args["custom_audience_ids"])...)
	for _, raw := range asSlice(args["segment_audience_ids"]) {
		id := int64(intArg(map[string]any{"id": raw}, "id", 0))
		r, err := a.getResource(ctx, acct, id)
		if err != nil || r.Kind != resourceAudience || r.Status != "active" {
			return nil, fmt.Errorf("segment_audience_ids must reference available audiences in this ad account")
		}
		switch r.ProviderType {
		case "google_custom_segment":
			customs = append(customs, r.NativeID)
		case "google_user_list":
			users = append(users, r.NativeID)
		default:
			return nil, fmt.Errorf("grouped audiences cannot contain another grouped audience")
		}
	}
	native["user_list_ids"], native["custom_audience_ids"] = users, customs
	delete(native, "segment_audience_ids")
	return native, nil
}

func (a *App) googleLookalikeInfo(ctx *sdk.AppCtx, acct *adAccount, args map[string]any) (map[string]any, error) {
	if _, ok := args["ratio"]; ok {
		return nil, fmt.Errorf("Google lookalikes use expansion_level, not ratio")
	}
	expansion := strings.ToUpper(firstString(args, "expansion_level"))
	if !containsString([]string{"NARROW", "BALANCED", "BROAD"}, expansion) {
		return nil, fmt.Errorf("expansion_level must be narrow, balanced or broad")
	}
	countries := []string{}
	rawCountries := asSlice(args["country_codes"])
	if len(rawCountries) == 0 && firstString(args, "country") != "" {
		rawCountries = []any{firstString(args, "country")}
	}
	for _, raw := range rawCountries {
		c := strings.ToUpper(strings.TrimSpace(toString(raw)))
		if !countryCode.MatchString(c) {
			return nil, fmt.Errorf("country_codes requires ISO 3166-1 alpha-2 codes")
		}
		if !containsString(countries, c) {
			countries = append(countries, c)
		}
	}
	if len(countries) == 0 {
		return nil, fmt.Errorf("country_codes required for Google lookalikes")
	}
	seeds := []string{}
	rawSeeds := asSlice(args["source_audience_ids"])
	if len(rawSeeds) == 0 && args["source_audience_id"] != nil {
		rawSeeds = []any{args["source_audience_id"]}
	}
	for _, raw := range rawSeeds {
		r, err := a.getResource(ctx, acct, int64(intArg(map[string]any{"id": raw}, "id", 0)))
		if err != nil || r.Kind != resourceAudience || r.ProviderType != "google_user_list" || r.Status != "active" || !googleNumericID(r.NativeID) {
			return nil, fmt.Errorf("source_audience_ids must reference active first-party user lists in this ad account")
		}
		kind := normalizedAudienceType(firstString(r.Metadata, "type"))
		if !containsString([]string{"customer_list", "website", "app_activity", "remarketing", "rule_based"}, kind) {
			return nil, fmt.Errorf("lookalike seeds must be first-party user lists, not lookalikes or custom segments")
		}
		if !containsString(seeds, r.NativeID) {
			seeds = append(seeds, r.NativeID)
		}
	}
	if len(seeds) == 0 || len(seeds) > 10 {
		return nil, fmt.Errorf("source_audience_ids requires 1–10 first-party user lists")
	}
	sort.Strings(countries)
	sort.Strings(seeds)
	return map[string]any{"seedUserListIds": seeds, "countryCodes": countries, "expansionLevel": expansion}, nil
}

func (a *App) googleStructuredAudienceCreate(ctx *sdk.AppCtx, acct *adAccount, args map[string]any, kind string) (any, error) {
	if len(asMap(args["platform_options"])) > 0 {
		return mcpError("structured audiences cannot be combined with platform_options"), nil
	}
	create := map[string]any{"name": strings.TrimSpace(firstString(args, "name")), "description": firstString(args, "description")}
	var parsed any
	var out map[string]any
	providerType := "google_user_list"
	switch kind {
	case "custom_segment":
		spec, err := googleCustomSegment(args)
		if err != nil {
			return mcpError(err.Error()), nil
		}
		for k, v := range spec {
			create[k] = v
		}
		create["status"] = "ENABLED"
		providerType = "google_custom_segment"
		parsed, out = a.execIntegrationTool(ctx, acct, "custom_audience_mutate", map[string]any{"customer_id": acct.NativeAccountID, "operations": []any{map[string]any{"create": create}}})
	case "grouped":
		native, err := a.googleGroupedSegments(ctx, acct, args)
		if err != nil {
			return mcpError(err.Error()), nil
		}
		result, err := a.googleGroupedAudienceCreate(ctx, acct, native)
		if err != nil {
			return nil, err
		}
		if fail := mcpResultError(result); fail != nil {
			return fail, nil
		}
		parsed = map[string]any{"id": asMap(result)["id"]}
		providerType = "google_grouped_audience"
		create, err = googleGroupedAudienceDefinition(acct, native)
		if err != nil {
			return mcpError(err.Error()), nil
		}
	case "lookalike":
		spec, err := a.googleLookalikeInfo(ctx, acct, args)
		if err != nil {
			return mcpError(err.Error()), nil
		}
		// Reuse an identical definition: Google forbids duplicate seed/country/expansion combinations.
		rows, fail := a.googleResourceRows(ctx, acct, "SELECT user_list.id, user_list.name, user_list.lookalike_user_list.seed_user_list_ids, user_list.lookalike_user_list.country_codes, user_list.lookalike_user_list.expansion_level FROM user_list WHERE user_list.type = LOOKALIKE")
		if fail != nil {
			return fail, nil
		}
		for _, row := range rows {
			item := mapAt(row, "userList")
			if len(item) == 0 {
				item = mapAt(row, "user_list")
			}
			info := mapAt(item, "lookalikeUserList")
			if len(info) == 0 {
				info = mapAt(item, "lookalike_user_list")
			}
			if sameGoogleLookalike(spec, info) {
				r, e := a.saveGoogleAudience(ctx, acct, providerType, firstString(item, "id"), firstString(item, "name"), kind, map[string]any{"lookalikeUserList": spec}, false)
				if e != nil {
					return nil, e
				}
				result := normalizedAudience("google", *r)
				result["reused"] = true
				return result, nil
			}
		}
		create["lookalikeUserList"] = spec
		create["membershipStatus"] = "OPEN"
		parsed, out = a.execIntegrationTool(ctx, acct, "user_list_mutate", map[string]any{"customer_id": acct.NativeAccountID, "operations": []any{map[string]any{"create": create}}})
	}
	if out != nil {
		return googleAudienceProviderError(out, kind), nil
	}
	id := audienceNativeID(parsed)
	if id == "" {
		return mcpError("provider did not return an audience ID"), nil
	}
	r, err := a.saveGoogleAudience(ctx, acct, providerType, id, firstString(args, "name"), kind, create, true)
	if err != nil {
		return nil, err
	}
	a.emitAudienceEvent(ctx, acct, "audience.created", r, 0, "completed")
	return normalizedAudience("google", *r), nil
}

func sameGoogleLookalike(a, b map[string]any) bool {
	for _, keys := range [][2]string{{"seedUserListIds", "seed_user_list_ids"}, {"countryCodes", "country_codes"}} {
		canonical := func(m map[string]any) string {
			raw := m[keys[0]]
			if raw == nil {
				raw = m[keys[1]]
			}
			values := []string{}
			for _, v := range asSlice(raw) {
				values = append(values, toString(v))
			}
			sort.Strings(values)
			return strings.Join(values, ",")
		}
		if canonical(a) != canonical(b) {
			return false
		}
	}
	return firstString(a, "expansionLevel", "expansion_level") == firstString(b, "expansionLevel", "expansion_level")
}

func googleAudienceProviderError(out map[string]any, kind string) map[string]any {
	raw, _ := json.Marshal(out)
	if kind == "lookalike" && (strings.Contains(string(raw), "SEED") || strings.Contains(string(raw), "LOOKALIKE") || strings.Contains(string(raw), "NOT_ENOUGH") || strings.Contains(string(raw), "CUSTOMER_NOT_ALLOWED")) {
		result := cloneMap(out)
		result["code"] = "audience_ineligible"
		result["retryable"] = false
		result["guidance"] = "Google requires at least 100 active matched people across eligible first-party seeds; seed access, countries and duplicate definitions are checked by Google. New lookalikes can take 2–3 days to become usable."
		return result
	}
	return out
}

func (a *App) saveGoogleAudience(ctx *sdk.AppCtx, acct *adAccount, providerType, id, name, kind string, metadata map[string]any, managed bool) (*adResource, error) {
	metadata = cloneMap(metadata)
	metadata["native_type"] = metadata["type"]
	metadata["type"] = kind
	if kind == "lookalike" {
		metadata["eligibility"] = "provider_evaluated"
		metadata["readiness"] = "unknown"
		metadata["minimum_active_matched_seed_members"] = 100
	}
	return a.upsertResource(ctx, acct, discoveredResource{Kind: resourceAudience, ProviderType: providerType, NativeID: id, DisplayName: name, Status: "active", Capabilities: audienceResourceCapabilities("google", kind), Metadata: metadata, ManagedByApp: managed})
}

func (a *App) googleStructuredAudienceUpdate(ctx *sdk.AppCtx, acct *adAccount, r *adResource, args map[string]any) (any, error) {
	update := map[string]any{"resourceName": googleAudienceResource(acct, r)}
	mask := []string{}
	for _, key := range []string{"name", "description"} {
		if v, ok := args[key]; ok {
			if key == "name" && strings.TrimSpace(toString(v)) == "" {
				return mcpError("name cannot be empty"), nil
			}
			update[key] = v
			mask = append(mask, key)
		}
	}
	if r.ProviderType == "google_custom_segment" {
		if args["keywords"] != nil || args["urls"] != nil || args["apps"] != nil || args["segment_mode"] != nil {
			// Read current members so a partial edit cannot overwrite external changes.
			rows, fail := a.googleResourceRows(ctx, acct, "SELECT custom_audience.id, custom_audience.type, custom_audience.members FROM custom_audience WHERE custom_audience.id = "+r.NativeID)
			if fail != nil {
				return fail, nil
			}
			if len(rows) != 1 {
				return mcpError("custom segment no longer exists or cannot be read; refresh audiences before editing"), nil
			}
			current := cloneMap(mapAt(rows[0], "customAudience"))
			if len(current) == 0 {
				current = cloneMap(mapAt(rows[0], "custom_audience"))
			}
			current["native_type"] = current["type"]
			for _, raw := range asSlice(current["members"]) {
				member := asMap(raw)
				if firstString(member, "keyword", "url", "app") == "" {
					return mcpError("this custom segment contains provider member types not supported by the structured editor; its members were not changed"), nil
				}
			}
			full := googleCustomSegmentArgs(current)
			for _, key := range []string{"keywords", "urls", "apps", "segment_mode"} {
				if v, ok := args[key]; ok {
					full[key] = v
				}
			}
			spec, err := googleCustomSegment(full)
			if err != nil {
				return mcpError(err.Error()), nil
			}
			update["members"], update["type"] = spec["members"], spec["type"]
			mask = append(mask, "members", "type")
		}
	} else if r.ProviderType == "google_grouped_audience" && (args["segment_audience_ids"] != nil || args["user_interest_ids"] != nil) {
		if !boolArg(args, "replace_segments") {
			return mcpError("replace_segments=true is required to replace all grouped audience segments"), nil
		}
		native, err := a.googleGroupedSegments(ctx, acct, args)
		if err != nil {
			return mcpError(err.Error()), nil
		}
		spec, err := googleGroupedAudienceDefinition(acct, native)
		if err != nil {
			return mcpError(err.Error()), nil
		}
		update["dimensions"] = spec["dimensions"]
		mask = append(mask, "dimensions")
	} else if args["source_audience_ids"] != nil || args["source_audience_id"] != nil || args["expansion_level"] != nil || args["country_codes"] != nil || args["country"] != nil || args["ratio"] != nil {
		return mcpError("lookalike seed lists, countries and expansion are immutable; create a new audience"), nil
	}
	if len(mask) == 0 {
		return mcpError("no supported audience fields supplied"), nil
	}
	op := map[string]any{"update": update, "updateMask": strings.Join(mask, ",")}
	var parsed any
	var out map[string]any
	if r.ProviderType == "google_custom_segment" {
		parsed, out = a.execIntegrationTool(ctx, acct, "custom_audience_mutate", map[string]any{"customer_id": acct.NativeAccountID, "operations": []any{op}})
	} else {
		kind := "userListOperation"
		if r.ProviderType == "google_grouped_audience" {
			kind = "audienceOperation"
		}
		parsed, out = a.execIntegrationTool(ctx, acct, "bulk_mutate", map[string]any{"customer_id": acct.NativeAccountID, "mutateOperations": []any{map[string]any{kind: op}}, "partialFailure": false})
	}
	if out != nil {
		return out, nil
	}
	metadata := cloneMap(r.Metadata)
	for k, v := range update {
		if k == "type" {
			metadata["native_type"] = v
		} else if k != "resourceName" && k != "name" {
			metadata[k] = v
		}
	}
	name := firstString(args, "name")
	if name == "" {
		name = r.DisplayName
	}
	saved, err := a.upsertResource(ctx, acct, discoveredResource{Kind: resourceAudience, ProviderType: r.ProviderType, NativeID: r.NativeID, DisplayName: name, Status: r.Status, Capabilities: r.Capabilities, Metadata: metadata, ManagedByApp: r.ManagedByApp})
	if err != nil {
		return nil, err
	}
	a.emitAudienceEvent(ctx, acct, "audience.updated", saved, 0, "completed")
	return map[string]any{"audience": normalizedAudience("google", *saved), "provider": parsed}, nil
}

func googleCustomSegmentArgs(metadata map[string]any) map[string]any {
	out := map[string]any{"keywords": []any{}, "urls": []any{}, "apps": []any{}, "segment_mode": "interests"}
	if firstString(metadata, "segment_type", "native_type") == "SEARCH" {
		out["segment_mode"] = "search_terms"
	}
	for _, raw := range asSlice(metadata["members"]) {
		m := asMap(raw)
		for _, pair := range [][2]string{{"keyword", "keywords"}, {"url", "urls"}, {"app", "apps"}} {
			if v := firstString(m, pair[0]); v != "" {
				out[pair[1]] = append(asSlice(out[pair[1]]), v)
			}
		}
	}
	return out
}

func (a *App) googleStructuredAudienceDelete(ctx *sdk.AppCtx, acct *adAccount, r *adResource) (any, error) {
	if r.ProviderType != "google_custom_segment" {
		return mcpError("Google does not support deletion for this audience type through this API"), nil
	}
	parsed, out := a.execIntegrationTool(ctx, acct, "custom_audience_mutate", map[string]any{"customer_id": acct.NativeAccountID, "operations": []any{map[string]any{"remove": googleAudienceResource(acct, r)}}})
	if out != nil {
		return out, nil
	}
	_, err := ctx.AppDB().Exec(`DELETE FROM ad_resources WHERE id=? AND project_id=? AND ad_account_id=?`, r.ID, acct.ProjectID, acct.ID)
	if err != nil {
		return nil, err
	}
	a.emitAudienceEvent(ctx, acct, "audience.deleted", r, 0, "completed")
	return map[string]any{"deleted": true, "audience_id": r.ID, "provider": parsed}, nil
}

func (a *App) discoverGoogleAudiences(ctx *sdk.AppCtx, acct *adAccount) ([]discoveredResource, map[string]any) {
	out := []discoveredResource{}
	specs := []struct{ query, key, provider, kind string }{
		{"SELECT user_list.id, user_list.name, user_list.type, user_list.description, user_list.membership_status, user_list.read_only, user_list.eligible_for_display, user_list.eligible_for_search, user_list.size_for_display, user_list.size_for_search, user_list.lookalike_user_list.seed_user_list_ids, user_list.lookalike_user_list.country_codes, user_list.lookalike_user_list.expansion_level FROM user_list ORDER BY user_list.id", "userList", "google_user_list", ""},
		{"SELECT custom_audience.id, custom_audience.name, custom_audience.description, custom_audience.status, custom_audience.type, custom_audience.members FROM custom_audience WHERE custom_audience.status != REMOVED", "customAudience", "google_custom_segment", "custom_segment"},
		{"SELECT audience.id, audience.name, audience.description, audience.status, audience.dimensions FROM audience WHERE audience.status != REMOVED", "audience", "google_grouped_audience", "grouped"},
	}
	for _, spec := range specs {
		rows, fail := a.googleResourceRows(ctx, acct, spec.query)
		if fail != nil {
			return nil, fail
		}
		for _, row := range rows {
			item := mapAt(row, spec.key)
			if len(item) == 0 {
				snake := map[string]string{"userList": "user_list", "customAudience": "custom_audience"}[spec.key]
				item = mapAt(row, snake)
			}
			id := firstString(item, "id")
			if !googleNumericID(id) {
				continue
			}
			kind := spec.kind
			if kind == "" {
				kind = googleUserListKind(firstString(item, "type"))
			}
			metadata := cloneMap(item)
			metadata["native_type"] = item["type"]
			metadata["type"] = kind
			metadata["size_for_display"] = item["sizeForDisplay"]
			metadata["size_for_search"] = item["sizeForSearch"]
			if kind == "lookalike" {
				metadata["readiness"] = "unknown"
				metadata["eligibility"] = "provider_evaluated"
			}
			if kind == "remarketing" || kind == "rule_based" {
				metadata["engagement_source"] = "unknown"
				metadata["creation"] = "provider_managed"
			}
			status := normalizedResourceStatus(firstString(item, "membershipStatus", "membership_status", "status"))
			caps := audienceResourceCapabilities("google", kind)
			if item["readOnly"] == true || item["read_only"] == true {
				caps = []string{"get", "target", "usage"}
			}
			out = append(out, discoveredResource{Kind: resourceAudience, ProviderType: spec.provider, NativeID: id, DisplayName: firstString(item, "name"), Status: status, Capabilities: caps, Metadata: metadata})
		}
	}
	return out, nil
}

// Include composition and seed dependencies as well as direct criteria. A saved
// group can be reused later, so its references also block deleting a segment.
func (a *App) googleAudienceUsage(ctx *sdk.AppCtx, acct *adAccount, r *adResource) (any, error) {
	ref := googleAudienceResource(acct, r)
	groups := []string{}
	rows := []map[string]any{}
	if r.ProviderType != "google_grouped_audience" {
		found, fail := a.googleResourceRows(ctx, acct, "SELECT audience.id, audience.name, audience.dimensions FROM audience WHERE audience.status != REMOVED")
		if fail != nil {
			return fail, nil
		}
		for _, row := range found {
			if containsExactString(row, ref) {
				v := mapAt(row, "audience")
				groups = append(groups, firstString(v, "id"))
				rows = append(rows, map[string]any{"id": v["id"], "name": v["name"], "dependency_type": "grouped_audience"})
			}
		}
	} else {
		groups = []string{r.NativeID}
	}
	if r.ProviderType == "google_user_list" {
		seeds, fail := a.googleResourceRows(ctx, acct, "SELECT user_list.id, user_list.name, user_list.lookalike_user_list.seed_user_list_ids FROM user_list WHERE user_list.type = LOOKALIKE")
		if fail != nil {
			return fail, nil
		}
		for _, row := range seeds {
			v := mapAt(row, "userList")
			if len(v) == 0 {
				v = mapAt(row, "user_list")
			}
			info := mapAt(v, "lookalikeUserList")
			if len(info) == 0 {
				info = mapAt(v, "lookalike_user_list")
			}
			if containsExactString(info, r.NativeID) {
				rows = append(rows, map[string]any{"id": v["id"], "name": v["name"], "dependency_type": "lookalike_seed"})
			}
		}
	}
	for _, level := range []string{"ad_group", "campaign"} {
		criterion := level + "_criterion"
		filters := []string{}
		if r.ProviderType == "google_user_list" {
			filters = append(filters, criterion+".user_list.user_list = '"+ref+"'")
		}
		if r.ProviderType == "google_custom_segment" {
			filters = append(filters, criterion+".custom_audience.custom_audience = '"+ref+"'")
		}
		if level == "ad_group" {
			for _, id := range groups {
				filters = append(filters, criterion+".audience.audience = 'customers/"+acct.NativeAccountID+"/audiences/"+id+"'")
			}
		}
		// GAQL does not support arbitrary OR; issue each typed resource query separately.
		for _, filter := range filters {
			query := fmt.Sprintf("SELECT %s.resource_name, %s.%s, %s.status FROM %s WHERE %s AND %s.status != REMOVED", criterion, criterion, level, criterion, criterion, filter, criterion)
			found, fail := a.googleResourceRows(ctx, acct, query)
			if fail != nil {
				return fail, nil
			}
			rows = append(rows, found...)
		}
	}
	return map[string]any{"data": rows}, nil
}

func containsExactString(value any, want string) bool {
	switch v := value.(type) {
	case string:
		return v == want
	case map[string]any:
		for _, item := range v {
			if containsExactString(item, want) {
				return true
			}
		}
	case []any:
		for _, item := range v {
			if containsExactString(item, want) {
				return true
			}
		}
	case []string:
		for _, item := range v {
			if item == want {
				return true
			}
		}
	}
	return false
}

func (a *App) resolveGoogleAudienceTargeting(ctx *sdk.AppCtx, acct *adAccount, args map[string]any) (map[string]any, map[string]any) {
	raw, ok := args["audience_resource_ids"]
	if !ok {
		return args, nil
	}
	if _, native := args["audience_ids"]; native {
		return nil, mcpError("use audience_resource_ids or native audience_ids, not both")
	}
	if acct.Platform != "google" {
		return nil, mcpError("local grouped audience targeting currently requires Google Demand Gen")
	}
	ids := []any{}
	for _, id := range asSlice(raw) {
		r, err := a.getResource(ctx, acct, int64(intArg(map[string]any{"id": id}, "id", 0)))
		if err != nil || r.Kind != resourceAudience || r.ProviderType != "google_grouped_audience" || r.Status != "active" {
			return nil, mcpError("audience_resource_ids must reference active grouped audiences in this ad account")
		}
		ids = append(ids, r.NativeID)
	}
	copy := cloneMap(args)
	copy["audience_ids"] = ids
	delete(copy, "audience_resource_ids")
	return copy, nil
}

func googleUserListKind(native string) string {
	switch strings.ToUpper(native) {
	case "CRM_BASED":
		return "customer_list"
	case "RULE_BASED":
		return "rule_based"
	case "REMARKETING", "EXTERNAL_REMARKETING":
		return "remarketing"
	case "LOOKALIKE", "SIMILAR":
		return "lookalike"
	case "LOGICAL":
		return "combined"
	default:
		return "unknown"
	}
}
