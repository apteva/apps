package main

import (
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"strings"
)

// X stores geography and OS as separate line-item criteria. Resolve both
// before creating a paused group so an unsupported target never mutates it.
func (a *App) xMobileAdSetCreate(ctx *sdk.AppCtx, acct *adAccount, tool string, args, input map[string]any) (any, error) {
	app, _ := args["_mobile_app"].(*adResource)
	if app == nil {
		return mcpError("mobile app binding required"), nil
	}
	locations, err := performanceCampaignIDs(asMap(args["targeting"])["location_ids"])
	if err != nil || len(locations) == 0 {
		return mcpError("X mobile targeting requires targeting.location_ids from targeting_catalog_search"), nil
	}
	rows, out := a.providerResourceRows(ctx, acct, "list_targeting_platforms", map[string]any{}, "x")
	if out != nil {
		return out, nil
	}
	wanted := map[string]string{"android": "Android", "ios": "iOS"}[firstString(app.Metadata, "os")]
	platformID := ""
	for _, row := range rows {
		if strings.EqualFold(firstString(row, "name", "platform"), wanted) {
			platformID = firstString(row, "id")
		}
	}
	if !safeProviderID(platformID) {
		return mcpError("X did not return the selected mobile OS in its targeting catalog"), nil
	}
	result, out := a.execIntegrationTool(ctx, acct, tool, input)
	if out != nil {
		return out, nil
	}
	id := createdProviderID(result, "ad_group")
	if id == "" {
		return mcpError("X returned no line-item id; reconcile before retrying"), nil
	}
	snapshot := mobilePublicArgs(args)
	snapshot["id"] = id
	if err := a.upsertDeliveryEntities(ctx, acct, "ad_group", []map[string]any{snapshot}, firstString(args, "campaign_id")); err != nil {
		return nil, err
	}
	criteria := []map[string]any{{"targeting_type": "PLATFORM", "targeting_value": platformID}}
	for _, location := range locations {
		criteria = append(criteria, map[string]any{"targeting_type": "LOCATION", "targeting_value": location})
	}
	for _, criterion := range criteria {
		criterion["account_id"] = acct.NativeAccountID
		criterion["line_item_id"] = id
		criterion["operator_type"] = "INCLUDE"
		if _, out := a.execIntegrationTool(ctx, acct, "create_targeting_criteria", criterion); out != nil {
			failure := mcpError(fmt.Sprintf("paused X line item %s was created, but targeting needs repair: %s", id, mcpErrorTextValue(out)))
			failure["code"] = "partial_creation"
			failure["ad_group_id"] = id
			return failure, nil
		}
	}
	return map[string]any{"id": id, "ad_group_id": id, "provider_result": result, "status": "PAUSED"}, nil
}
