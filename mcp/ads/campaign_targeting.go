package main

import (
	"fmt"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// Location and language targeting are CampaignCriterion records, the same
// resource campaign-level negative keywords already use. A Search campaign
// created without location criteria targets everywhere, which is the real
// footgun: it is a silent default, not an error.

const (
	geoConstantPrefix      = "geoTargetConstants"
	languageConstantPrefix = "languageConstants"
)

// googleConstantResource accepts either a bare numeric id (2840) or the full
// resource name (geoTargetConstants/2840), which is what
// targeting_catalog_search returns.
func googleConstantResource(raw any, prefix, field string, index int) (string, error) {
	value := strings.TrimSpace(toString(raw))
	if value == "" {
		return "", fmt.Errorf("%s[%d] is empty", field, index)
	}
	if id, found := strings.CutPrefix(value, prefix+"/"); found {
		if !googleNumericID(id) {
			return "", fmt.Errorf("%s[%d] must end in a numeric id, got %s", field, index, value)
		}
		return value, nil
	}
	if googleNumericID(value) {
		return prefix + "/" + value, nil
	}
	return "", fmt.Errorf("%s[%d] must be a numeric id or a %s/<id> resource name, got %s",
		field, index, prefix, value)
}

func googleConstantResources(raw any, prefix, field string) ([]string, error) {
	if raw == nil {
		return nil, nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("%s must be an array", field)
	}
	seen := make(map[string]bool, len(list))
	out := make([]string, 0, len(list))
	for i, entry := range list {
		resource, err := googleConstantResource(entry, prefix, field, i)
		if err != nil {
			return nil, err
		}
		if seen[resource] {
			continue
		}
		seen[resource] = true
		out = append(out, resource)
	}
	return out, nil
}

type campaignTargeting struct {
	Locations         []string
	ExcludedLocations []string
	Languages         []string
}

func (t campaignTargeting) empty() bool {
	return len(t.Locations) == 0 && len(t.ExcludedLocations) == 0 && len(t.Languages) == 0
}

// normalizedCampaignTargeting validates the targeting arguments without
// contacting the provider, so a campaign_create can reject bad ids before it
// creates anything.
func normalizedCampaignTargeting(args map[string]any) (campaignTargeting, error) {
	var targeting campaignTargeting
	var err error
	if targeting.Locations, err = googleConstantResources(args["locations"], geoConstantPrefix, "locations"); err != nil {
		return targeting, err
	}
	if targeting.ExcludedLocations, err = googleConstantResources(
		args["excluded_locations"], geoConstantPrefix, "excluded_locations"); err != nil {
		return targeting, err
	}
	if targeting.Languages, err = googleConstantResources(
		args["languages"], languageConstantPrefix, "languages"); err != nil {
		return targeting, err
	}
	overlap := map[string]bool{}
	for _, location := range targeting.Locations {
		overlap[location] = true
	}
	for _, excluded := range targeting.ExcludedLocations {
		if overlap[excluded] {
			return targeting, fmt.Errorf("%s appears in both locations and excluded_locations", excluded)
		}
	}
	return targeting, nil
}

func (t campaignTargeting) operations(customerID, campaignID string) []any {
	campaign := googleCampaignResource(customerID, campaignID)
	operations := make([]any, 0, len(t.Locations)+len(t.ExcludedLocations)+len(t.Languages))
	for _, location := range t.Locations {
		operations = append(operations, map[string]any{"create": map[string]any{
			"campaign": campaign,
			"location": map[string]any{"geoTargetConstant": location},
		}})
	}
	for _, location := range t.ExcludedLocations {
		operations = append(operations, map[string]any{"create": map[string]any{
			"campaign": campaign,
			"negative": true,
			"location": map[string]any{"geoTargetConstant": location},
		}})
	}
	// A language criterion cannot be negative: Google expresses "not this
	// language" by simply not targeting it.
	for _, language := range t.Languages {
		operations = append(operations, map[string]any{"create": map[string]any{
			"campaign": campaign,
			"language": map[string]any{"languageConstant": language},
		}})
	}
	return operations
}

func (a *App) toolCampaignTargetingAdd(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	acct, def, campaignID, errOut := a.resolveGoogleTarget(ctx, args, "campaign_targeting_add", keywordLevelCampaign)
	if errOut != nil {
		return errOut, nil
	}
	targeting, err := normalizedCampaignTargeting(args)
	if err != nil {
		return mcpError(err.Error()), nil
	}
	if targeting.empty() {
		return mcpError("supply at least one of locations, excluded_locations, or languages"), nil
	}
	operations := targeting.operations(acct.NativeAccountID, campaignID)
	out, errOut := a.execIntegrationTool(ctx, acct, def.CampaignCriterionMutateTool, map[string]any{
		"customer_id": acct.NativeAccountID,
		"operations":  operations,
	})
	if errOut != nil {
		return errOut, nil
	}
	a.emitEntityChanged(ctx, acct, "campaign_targeting", "created", args, out, nil)
	return map[string]any{
		"campaign_id":        campaignID,
		"locations":          len(targeting.Locations),
		"excluded_locations": len(targeting.ExcludedLocations),
		"languages":          len(targeting.Languages),
		"result":             out,
	}, nil
}

func (a *App) toolCampaignTargetingList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	acct, _, campaignID, errOut := a.resolveGoogleTarget(ctx, args, "campaign_targeting_list", keywordLevelCampaign)
	if errOut != nil {
		return errOut, nil
	}
	// campaign.id is selected because the filter names it; GAQL rejects a query
	// that filters on a field missing from the SELECT clause.
	query := fmt.Sprintf(
		"SELECT campaign.id, campaign_criterion.criterion_id, campaign_criterion.type, "+
			"campaign_criterion.negative, campaign_criterion.location.geo_target_constant, "+
			"campaign_criterion.language.language_constant "+
			"FROM campaign_criterion WHERE campaign.id = %s "+
			"AND campaign_criterion.type IN (LOCATION, LANGUAGE)", campaignID,
	)
	rows, errOut := a.googleSearchRows(ctx, acct, query)
	if errOut != nil {
		return errOut, nil
	}
	locations := make([]map[string]any, 0)
	languages := make([]map[string]any, 0)
	for _, row := range rows {
		criterion := mapAt(row, "campaignCriterion")
		if len(criterion) == 0 {
			criterion = mapAt(row, "campaign_criterion")
		}
		id := firstString(criterion, "criterionId", "criterion_id")
		negative := googleBool(criterion["negative"])
		if location := mapAt(criterion, "location"); len(location) > 0 {
			locations = append(locations, map[string]any{
				"criterion_id":        id,
				"geo_target_constant": firstString(location, "geoTargetConstant", "geo_target_constant"),
				"negative":            negative,
			})
			continue
		}
		if language := mapAt(criterion, "language"); len(language) > 0 {
			languages = append(languages, map[string]any{
				"criterion_id":      id,
				"language_constant": firstString(language, "languageConstant", "language_constant"),
			})
		}
	}
	// A campaign with no location criteria serves everywhere. Say so rather
	// than returning an empty list that reads like "nothing to see".
	return map[string]any{
		"campaign_id":        campaignID,
		"locations":          locations,
		"languages":          languages,
		"targets_everywhere": len(locations) == 0,
	}, nil
}

func (a *App) toolCampaignTargetingRemove(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	acct, def, campaignID, errOut := a.resolveGoogleTarget(ctx, args, "campaign_targeting_remove", keywordLevelCampaign)
	if errOut != nil {
		return errOut, nil
	}
	ids, err := keywordIDs(args["criterion_ids"])
	if err != nil {
		return mcpError(strings.Replace(err.Error(), "keyword_ids", "criterion_ids", 1)), nil
	}
	operations := make([]any, 0, len(ids))
	for _, id := range ids {
		operations = append(operations, map[string]any{
			"remove": googleCampaignCriterionResource(acct.NativeAccountID, campaignID, id),
		})
	}
	out, errOut := a.execIntegrationTool(ctx, acct, def.CampaignCriterionMutateTool, map[string]any{
		"customer_id": acct.NativeAccountID,
		"operations":  operations,
	})
	if errOut != nil {
		return errOut, nil
	}
	a.emitEntityChanged(ctx, acct, "campaign_targeting", "deleted", args, out, nil)
	return map[string]any{"campaign_id": campaignID, "removed": len(ids), "result": out}, nil
}
