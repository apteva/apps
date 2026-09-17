package main

import (
	"fmt"
	"strings"
	"unicode/utf8"

	sdk "github.com/apteva/app-sdk"
)

// Ad extensions are Google "assets" linked to a campaign through a
// CampaignAsset with a field type. They materially lift Search CTR, and the
// campaign_asset_mutate plumbing already existed here for lead forms — it was
// only ever reachable with fieldType LEAD_FORM.

const (
	extensionSitelink          = "sitelink"
	extensionCallout           = "callout"
	extensionStructuredSnippet = "structured_snippet"

	sitelinkTextMaxRunes        = 25
	sitelinkDescriptionMaxRunes = 35
	calloutTextMaxRunes         = 25
	snippetValueMaxRunes        = 25
	snippetMinValues            = 3
	snippetMaxValues            = 10
)

var extensionFieldTypes = map[string]string{
	extensionSitelink:          "SITELINK",
	extensionCallout:           "CALLOUT",
	extensionStructuredSnippet: "STRUCTURED_SNIPPET",
}

// Google accepts only a fixed set of structured-snippet headers; an unknown
// header is rejected by the provider after the asset is already created.
var structuredSnippetHeaders = map[string]string{
	"amenities":          "Amenities",
	"brands":             "Brands",
	"courses":            "Courses",
	"degree programs":    "Degree programs",
	"destinations":       "Destinations",
	"featured hotels":    "Featured hotels",
	"insurance coverage": "Insurance coverage",
	"models":             "Models",
	"neighborhoods":      "Neighborhoods",
	"service catalog":    "Service catalog",
	"shows":              "Shows",
	"styles":             "Styles",
	"types":              "Types",
}

func extensionFieldType(raw string) (string, error) {
	value := strings.TrimSpace(strings.ToLower(raw))
	if mapped, ok := extensionFieldTypes[value]; ok {
		return mapped, nil
	}
	return "", fmt.Errorf("type must be sitelink, callout, or structured_snippet")
}

func checkRunes(value, field string, max int) error {
	if value == "" {
		return fmt.Errorf("%s is required", field)
	}
	if utf8.RuneCountInString(value) > max {
		return fmt.Errorf("%s must be %d characters or fewer", field, max)
	}
	return nil
}

// googleExtensionAsset builds one Asset payload for asset_mutate.
func googleExtensionAsset(kind string, item map[string]any, index int) (map[string]any, error) {
	prefix := fmt.Sprintf("extensions[%d]", index)
	switch kind {
	case extensionSitelink:
		text := strings.TrimSpace(toString(item["link_text"]))
		if err := checkRunes(text, prefix+".link_text", sitelinkTextMaxRunes); err != nil {
			return nil, err
		}
		finalURL := strings.TrimSpace(toString(item["final_url"]))
		urls, err := rsaFinalURLs([]any{finalURL})
		if err != nil {
			return nil, fmt.Errorf("%s.final_url must be an absolute http(s) URL", prefix)
		}
		sitelink := map[string]any{"linkText": text}
		for field, key := range map[string]string{"description1": "description1", "description2": "description2"} {
			value := strings.TrimSpace(toString(item[key]))
			if value == "" {
				continue
			}
			if err := checkRunes(value, prefix+"."+key, sitelinkDescriptionMaxRunes); err != nil {
				return nil, err
			}
			sitelink[field] = value
		}
		// Google requires both sitelink descriptions or neither.
		_, has1 := sitelink["description1"]
		_, has2 := sitelink["description2"]
		if has1 != has2 {
			return nil, fmt.Errorf("%s: supply both description1 and description2, or neither", prefix)
		}
		return map[string]any{"name": text, "sitelinkAsset": sitelink, "finalUrls": urls}, nil

	case extensionCallout:
		text := strings.TrimSpace(toString(item["text"]))
		if err := checkRunes(text, prefix+".text", calloutTextMaxRunes); err != nil {
			return nil, err
		}
		return map[string]any{"name": text, "calloutAsset": map[string]any{"calloutText": text}}, nil

	default:
		header := strings.TrimSpace(toString(item["header"]))
		mapped, ok := structuredSnippetHeaders[strings.ToLower(header)]
		if !ok {
			return nil, fmt.Errorf("%s.header must be one of the Google structured-snippet headers, such as Brands, Models or Services", prefix)
		}
		raw, ok := item["values"].([]any)
		if !ok || len(raw) < snippetMinValues || len(raw) > snippetMaxValues {
			return nil, fmt.Errorf("%s.values must contain between %d and %d entries", prefix, snippetMinValues, snippetMaxValues)
		}
		values := make([]any, 0, len(raw))
		for i, entry := range raw {
			value := strings.TrimSpace(toString(entry))
			if err := checkRunes(value, fmt.Sprintf("%s.values[%d]", prefix, i), snippetValueMaxRunes); err != nil {
				return nil, err
			}
			values = append(values, value)
		}
		return map[string]any{
			"name":                   mapped + ": " + toString(values[0]),
			"structuredSnippetAsset": map[string]any{"header": mapped, "values": values},
		}, nil
	}
}

func (a *App) toolAdExtensionCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	acct, def, campaignID, errOut := a.resolveGoogleTarget(ctx, args, "ad_extension_create", keywordLevelCampaign)
	if errOut != nil {
		return errOut, nil
	}
	kind := strings.TrimSpace(strings.ToLower(stringArgAny(args, "type")))
	fieldType, typeErr := extensionFieldType(kind)
	if typeErr != nil {
		return mcpError(typeErr.Error()), nil
	}
	list, ok := args["extensions"].([]any)
	if !ok || len(list) == 0 {
		return mcpError("extensions must be a non-empty array"), nil
	}
	assets := make([]any, 0, len(list))
	for i, entry := range list {
		asset, err := googleExtensionAsset(kind, asMap(entry), i)
		if err != nil {
			return mcpError(err.Error()), nil
		}
		assets = append(assets, map[string]any{"create": asset})
	}
	created, errOut := a.execIntegrationTool(ctx, acct, def.CreativeCreateTool, map[string]any{
		"customer_id": acct.NativeAccountID,
		"operations":  assets,
	})
	if errOut != nil {
		return errOut, nil
	}
	resourceNames := allResourceNames(created)
	if len(resourceNames) == 0 {
		out := mcpError("asset_mutate returned no resourceName for the created extensions")
		out["code"] = "provider_response_invalid"
		return out, nil
	}
	links := make([]any, 0, len(resourceNames))
	for _, name := range resourceNames {
		links = append(links, map[string]any{"create": map[string]any{
			"campaign":  googleCampaignResource(acct.NativeAccountID, campaignID),
			"asset":     name,
			"fieldType": fieldType,
		}})
	}
	linked, errOut := a.execIntegrationTool(ctx, acct, def.CampaignAssetMutateTool, map[string]any{
		"customer_id": acct.NativeAccountID,
		"operations":  links,
	})
	if errOut != nil {
		// The assets exist but are attached to nothing. Say so explicitly so the
		// caller can retry the link instead of creating duplicate assets.
		errOut["orphaned_assets"] = resourceNames
		errOut["hint"] = "assets were created but not linked to the campaign; retry ad_extension_create with the same input after resolving the error, or remove them manually"
		return errOut, nil
	}
	a.emitEntityChanged(ctx, acct, "ad_extension", "created", args, linked, nil)
	return map[string]any{
		"type": kind, "field_type": fieldType, "campaign_id": campaignID,
		"created": len(resourceNames), "assets": resourceNames, "links": linked,
	}, nil
}

func (a *App) toolAdExtensionList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	acct, _, campaignID, errOut := a.resolveGoogleTarget(ctx, args, "ad_extension_list", keywordLevelCampaign)
	if errOut != nil {
		return errOut, nil
	}
	filter := ""
	if raw := stringArgAny(args, "type"); raw != "" {
		fieldType, typeErr := extensionFieldType(raw)
		if typeErr != nil {
			return mcpError(typeErr.Error()), nil
		}
		filter = " AND campaign_asset.field_type = " + fieldType
	} else {
		filter = " AND campaign_asset.field_type IN (SITELINK, CALLOUT, STRUCTURED_SNIPPET)"
	}
	query := fmt.Sprintf(
		"SELECT campaign.id, campaign_asset.asset, campaign_asset.field_type, campaign_asset.status, "+
			"asset.id, asset.name, asset.final_urls, asset.sitelink_asset.link_text, "+
			"asset.sitelink_asset.description1, asset.sitelink_asset.description2, "+
			"asset.callout_asset.callout_text, "+
			"asset.structured_snippet_asset.header, asset.structured_snippet_asset.values "+
			"FROM campaign_asset WHERE campaign.id = %s AND campaign_asset.status != REMOVED%s",
		campaignID, filter,
	)
	rows, errOut := a.googleSearchRows(ctx, acct, query)
	if errOut != nil {
		return errOut, nil
	}
	extensions := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		link := mapAt(row, "campaignAsset")
		if len(link) == 0 {
			link = mapAt(row, "campaign_asset")
		}
		asset := mapAt(row, "asset")
		item := map[string]any{
			"asset_resource_name": firstString(link, "asset"),
			"field_type":          firstString(link, "fieldType", "field_type"),
			"status":              firstString(link, "status"),
			"asset_id":            firstString(asset, "id"),
			"name":                firstString(asset, "name"),
		}
		if sitelink := mapAt(asset, "sitelinkAsset"); len(sitelink) > 0 {
			item["link_text"] = firstString(sitelink, "linkText", "link_text")
			if value := firstString(sitelink, "description1"); value != "" {
				item["description1"] = value
			}
			if value := firstString(sitelink, "description2"); value != "" {
				item["description2"] = value
			}
			if urls, ok := asset["finalUrls"].([]any); ok && len(urls) > 0 {
				item["final_urls"] = urls
			}
		}
		if callout := mapAt(asset, "calloutAsset"); len(callout) > 0 {
			item["text"] = firstString(callout, "calloutText", "callout_text")
		}
		if snippet := mapAt(asset, "structuredSnippetAsset"); len(snippet) > 0 {
			item["header"] = firstString(snippet, "header")
			item["values"] = snippet["values"]
		}
		extensions = append(extensions, item)
	}
	return map[string]any{"campaign_id": campaignID, "count": len(extensions), "extensions": extensions}, nil
}

func (a *App) toolAdExtensionDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	acct, def, campaignID, errOut := a.resolveGoogleTarget(ctx, args, "ad_extension_delete", keywordLevelCampaign)
	if errOut != nil {
		return errOut, nil
	}
	raw, ok := args["asset_resource_names"].([]any)
	if !ok || len(raw) == 0 {
		return mcpError("asset_resource_names must contain at least one asset resource name from ad_extension_list"), nil
	}
	fieldType, typeErr := extensionFieldType(stringArgAny(args, "type"))
	if typeErr != nil {
		return mcpError(typeErr.Error()), nil
	}
	operations := make([]any, 0, len(raw))
	for i, entry := range raw {
		name := strings.TrimSpace(toString(entry))
		if name == "" {
			return mcpError(fmt.Sprintf("asset_resource_names[%d] is empty", i)), nil
		}
		if !googlePayloadScoped(name, acct.NativeAccountID) {
			return mcpError("asset resource name belongs to another customer: " + name), nil
		}
		operations = append(operations, map[string]any{
			"remove": fmt.Sprintf("customers/%s/campaignAssets/%s~%s~%s",
				acct.NativeAccountID, campaignID, googleAssetID(name), fieldType),
		})
	}
	out, errOut := a.execIntegrationTool(ctx, acct, def.CampaignAssetMutateTool, map[string]any{
		"customer_id": acct.NativeAccountID,
		"operations":  operations,
	})
	if errOut != nil {
		return errOut, nil
	}
	a.emitEntityChanged(ctx, acct, "ad_extension", "deleted", args, out, nil)
	return map[string]any{"campaign_id": campaignID, "removed": len(operations), "result": out}, nil
}
