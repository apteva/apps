package main

import (
	"fmt"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

type deliveryState struct {
	Entity          string
	ID              string
	ParentID        string
	CampaignID      string
	Status          string
	EffectiveStatus string
}

func (s deliveryState) active() bool {
	return strings.EqualFold(s.Status, "ACTIVE") || strings.EqualFold(s.Status, "ENABLED")
}

func (s deliveryState) response(outcome string) map[string]any {
	return map[string]any{
		"entity":           s.Entity,
		"id":               s.ID,
		"outcome":          outcome,
		"status":           normalizedDeliveryStatus(s.Status),
		"provider_status":  s.Status,
		"effective_status": s.EffectiveStatus,
	}
}

func (a *App) toolDeliveryActivate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	acct, def, errOut := a.resolveAdAccount(ctx, args)
	if errOut != nil {
		return errOut, nil
	}
	campaignID := stringArgAny(args, "campaign_id")
	adSetID := stringArgAny(args, "adset_id")
	adID := stringArgAny(args, "ad_id")
	if campaignID == "" || adSetID == "" || adID == "" {
		return mcpError("campaign_id, adset_id, and ad_id required"), nil
	}
	if scopeErr := a.requireManagedCampaign(ctx, acct, campaignID); scopeErr != nil {
		return scopeErr, nil
	}

	states, preflightErr := a.loadDeliveryHierarchy(ctx, acct, def, campaignID, adSetID, adID)
	if preflightErr != nil {
		return preflightErr, nil
	}
	steps := make([]map[string]any, 0, 3)
	order := []string{"ad", "adset", "campaign"}
	for index, entity := range order {
		state := states[entity]
		if state.active() {
			steps = append(steps, state.response("already_active"))
			continue
		}
		if updateErr := a.activateDeliveryEntity(ctx, acct, def, state); updateErr != nil {
			return activationFailure(acct, entity, "update", steps, order[index:], updateErr), nil
		}
		refreshed, readErr := a.loadDeliveryEntity(ctx, acct, def, campaignID, adSetID, adID, entity)
		if readErr != nil {
			return activationFailure(acct, entity, "read_back", steps, order[index:], readErr), nil
		}
		if !refreshed.active() {
			stateErr := mcpError(fmt.Sprintf("%s %s did not enter an active configured state", entity, state.ID))
			stateErr["code"] = "activation_state_mismatch"
			stateErr["retryable"] = true
			return activationFailure(acct, entity, "read_back", steps, order[index:], stateErr), nil
		}
		steps = append(steps, refreshed.response("activated"))
		states[entity] = refreshed
	}
	eventArgs := map[string]any{
		"campaign_id": campaignID, "adset_id": adSetID, "ad_id": adID, "status": "ACTIVE",
	}
	a.emitEntityChanged(ctx, acct, "ad", "updated", eventArgs, map[string]any{}, nil)
	a.emitEntityChanged(ctx, acct, "ad_group", "updated", eventArgs, map[string]any{}, nil)
	a.emitEntityChanged(ctx, acct, "campaign", "updated", eventArgs, map[string]any{}, nil)

	return map[string]any{
		"platform":      acct.Platform,
		"ad_account_id": acct.ID,
		"status":        "completed",
		"steps":         steps,
		"remaining":     []string{},
	}, nil
}

func (a *App) loadDeliveryHierarchy(
	ctx *sdk.AppCtx,
	acct *adAccount,
	def *platformDef,
	campaignID, adSetID, adID string,
) (map[string]deliveryState, map[string]any) {
	states := map[string]deliveryState{}
	for _, entity := range []string{"ad", "adset", "campaign"} {
		state, errOut := a.loadDeliveryEntity(ctx, acct, def, campaignID, adSetID, adID, entity)
		if errOut != nil {
			return nil, errOut
		}
		states[entity] = state
	}
	ad := states["ad"]
	adSet := states["adset"]
	if ad.CampaignID == "" && ad.ParentID == adSetID {
		ad.CampaignID = adSet.CampaignID
		states["ad"] = ad
	}
	if ad.ParentID != adSetID || ad.CampaignID != campaignID || adSet.CampaignID != campaignID {
		return nil, mcpError("ad, ad set/ad group, and campaign do not form one delivery hierarchy")
	}
	return states, nil
}

func (a *App) loadDeliveryEntity(
	ctx *sdk.AppCtx,
	acct *adAccount,
	def *platformDef,
	campaignID, adSetID, adID, entity string,
) (deliveryState, map[string]any) {
	if acct.Platform == "google" {
		return a.loadGoogleDeliveryEntity(ctx, acct, campaignID, adSetID, adID, entity)
	}
	if acct.Platform == "x" || acct.Platform == "reddit" {
		return a.loadXRedditDeliveryEntity(ctx, acct, campaignID, adSetID, adID, entity)
	}
	return a.loadMetaDeliveryEntity(ctx, acct, def, campaignID, adSetID, adID, entity)
}

func (a *App) loadXRedditDeliveryEntity(
	ctx *sdk.AppCtx,
	acct *adAccount,
	campaignID, adSetID, adID, entity string,
) (deliveryState, map[string]any) {
	tool, input, id := "", map[string]any{}, ""
	if acct.Platform == "x" {
		input["account_id"] = acct.NativeAccountID
		switch entity {
		case "ad":
			tool, id, input["promoted_tweet_id"] = "get_promoted_tweet", adID, adID
		case "adset":
			tool, id, input["line_item_id"] = "get_line_item", adSetID, adSetID
		case "campaign":
			tool, id, input["campaign_id"] = "get_campaign", campaignID, campaignID
		}
	} else {
		switch entity {
		case "ad":
			tool, id, input["ad_id"] = "get_ad", adID, adID
		case "adset":
			tool, id, input["ad_group_id"] = "get_ad_group", adSetID, adSetID
		case "campaign":
			tool, id, input["campaign_id"] = "get_campaign", campaignID, campaignID
		}
	}
	if tool == "" {
		return deliveryState{}, mcpError("unsupported activation entity " + entity)
	}
	parsed, errOut := a.execIntegrationTool(ctx, acct, tool, input)
	if errOut != nil {
		return deliveryState{}, errOut
	}
	row := asMap(asMap(parsed)["data"])
	if len(row) == 0 {
		rows := resultRows(parsed)
		if len(rows) > 0 {
			row = rows[0]
		}
	}
	if len(row) == 0 || firstString(row, "id") != id {
		return deliveryState{}, mcpError(fmt.Sprintf("%s %s does not belong to the selected ad account", entity, id))
	}
	return deliveryState{
		Entity: entity, ID: id,
		ParentID:        firstString(row, "adset_id", "ad_group_id", "line_item_id"),
		CampaignID:      firstString(row, "campaign_id"),
		Status:          firstString(row, "status", "entity_status", "configured_status"),
		EffectiveStatus: firstString(row, "effective_status", "delivery_status"),
	}, nil
}

func (a *App) loadMetaDeliveryEntity(
	ctx *sdk.AppCtx,
	acct *adAccount,
	def *platformDef,
	campaignID, adSetID, adID, entity string,
) (deliveryState, map[string]any) {
	tool, id, fields := def.CampaignListTool, campaignID, "id,status,effective_status"
	inputKey := def.AccountIDInputField
	switch entity {
	case "ad":
		tool, id, fields, inputKey = def.AdListTool, adID, "id,status,effective_status,adset_id,campaign_id", "objectId"
	case "adset":
		tool, id, fields, inputKey = def.AdSetListTool, adSetID, "id,status,effective_status,campaign_id", "objectId"
	case "campaign":
	default:
		return deliveryState{}, mcpError("unsupported activation entity " + entity)
	}
	after := ""
	for page := 0; page < 20; page++ {
		input := map[string]any{inputKey: acct.NativeAccountID, "fields": fields, "limit": 100}
		if after != "" {
			input["after"] = after
		}
		parsed, errOut := a.execIntegrationTool(ctx, acct, tool, input)
		if errOut != nil {
			return deliveryState{}, errOut
		}
		for _, row := range resultRows(parsed) {
			if firstString(row, "id") == id {
				return deliveryState{
					Entity:          entity,
					ID:              id,
					ParentID:        firstString(row, "adset_id"),
					CampaignID:      firstString(row, "campaign_id"),
					Status:          firstString(row, "status"),
					EffectiveStatus: firstString(row, "effective_status"),
				}, nil
			}
		}
		next := metaNextCursor(parsed)
		if next == "" || next == after {
			break
		}
		after = next
	}
	return deliveryState{}, mcpError(fmt.Sprintf("%s %s does not belong to the selected ad account", entity, id))
}

func (a *App) loadGoogleDeliveryEntity(
	ctx *sdk.AppCtx,
	acct *adAccount,
	campaignID, adSetID, adID, entity string,
) (deliveryState, map[string]any) {
	if !googleNumericID(campaignID) || !googleNumericID(adSetID) || !googleNumericID(adID) {
		return deliveryState{}, mcpError("Google campaign_id, adset_id, and ad_id must be numeric")
	}
	query := fmt.Sprintf(
		"SELECT campaign.id, campaign.status, ad_group.id, ad_group.status, ad_group_ad.ad.id, ad_group_ad.status FROM ad_group_ad WHERE campaign.id = %s AND ad_group.id = %s AND ad_group_ad.ad.id = %s LIMIT 1",
		campaignID, adSetID, adID,
	)
	parsed, errOut := a.execIntegrationTool(ctx, acct, "search", map[string]any{
		"customer_id": acct.NativeAccountID,
		"query":       query,
	})
	if errOut != nil {
		return deliveryState{}, errOut
	}
	rows := resultRows(parsed)
	if len(rows) == 0 {
		return deliveryState{}, mcpError("ad, ad group, and campaign do not form one delivery hierarchy")
	}
	row := rows[0]
	campaign := mapAt(row, "campaign")
	adSet := mapAt(row, "adGroup")
	if len(adSet) == 0 {
		adSet = mapAt(row, "ad_group")
	}
	ad := mapAt(row, "adGroupAd")
	if len(ad) == 0 {
		ad = mapAt(row, "ad_group_ad")
	}
	switch entity {
	case "ad":
		return deliveryState{Entity: entity, ID: adID, ParentID: adSetID, CampaignID: campaignID, Status: firstString(ad, "status")}, nil
	case "adset":
		return deliveryState{Entity: entity, ID: adSetID, CampaignID: campaignID, Status: firstString(adSet, "status")}, nil
	case "campaign":
		return deliveryState{Entity: entity, ID: campaignID, Status: firstString(campaign, "status")}, nil
	default:
		return deliveryState{}, mcpError("unsupported activation entity " + entity)
	}
}

func (a *App) activateDeliveryEntity(
	ctx *sdk.AppCtx,
	acct *adAccount,
	def *platformDef,
	state deliveryState,
) map[string]any {
	var tool string
	var input map[string]any
	if acct.Platform == "meta" {
		switch state.Entity {
		case "ad":
			tool, input = def.AdUpdateTool, map[string]any{"adId": state.ID, "status": "ACTIVE"}
		case "adset":
			tool, input = def.AdSetUpdateTool, map[string]any{"adSetId": state.ID, "status": "ACTIVE"}
		case "campaign":
			tool, input = def.CampaignUpdateTool, map[string]any{"campaignId": state.ID, "status": "ACTIVE"}
		}
	} else if acct.Platform == "google" {
		var resourceName string
		switch state.Entity {
		case "ad":
			tool = def.AdUpdateTool
			resourceName = fmt.Sprintf("customers/%s/adGroupAds/%s~%s", acct.NativeAccountID, state.ParentID, state.ID)
		case "adset":
			tool = def.AdSetUpdateTool
			resourceName = googleAdGroupResource(acct.NativeAccountID, state.ID)
		case "campaign":
			tool = def.CampaignUpdateTool
			resourceName = googleCampaignResource(acct.NativeAccountID, state.ID)
		}
		input = map[string]any{
			"customer_id": acct.NativeAccountID,
			"operations": []any{map[string]any{
				"update":     map[string]any{"resourceName": resourceName, "status": "ENABLED"},
				"updateMask": "status",
			}},
		}
	} else if acct.Platform == "x" {
		if state.Entity == "ad" {
			// A promoted Tweet association is active when it exists and cannot
			// be resumed with PUT. Parent activation is sufficient.
			return nil
		}
		switch state.Entity {
		case "adset":
			tool, input = def.AdSetUpdateTool, map[string]any{"account_id": acct.NativeAccountID, "line_item_id": state.ID, "entity_status": "ACTIVE"}
		case "campaign":
			tool, input = def.CampaignUpdateTool, map[string]any{"account_id": acct.NativeAccountID, "campaign_id": state.ID, "entity_status": "ACTIVE"}
		}
	} else if acct.Platform == "reddit" {
		switch state.Entity {
		case "ad":
			tool, input = def.AdUpdateTool, map[string]any{"ad_id": state.ID, "data": map[string]any{"configured_status": "ACTIVE"}}
		case "adset":
			tool, input = def.AdSetUpdateTool, map[string]any{"ad_group_id": state.ID, "data": map[string]any{"configured_status": "ACTIVE"}}
		case "campaign":
			tool, input = def.CampaignUpdateTool, map[string]any{"campaign_id": state.ID, "data": map[string]any{"configured_status": "ACTIVE"}}
		}
	}
	if tool == "" {
		return mcpError("unsupported activation entity " + state.Entity)
	}
	_, errOut := a.execIdempotentUpdate(ctx, acct, tool, input)
	return errOut
}

func activationFailure(
	acct *adAccount,
	entity, phase string,
	steps []map[string]any,
	remaining []string,
	providerErr map[string]any,
) map[string]any {
	out := mcpError(fmt.Sprintf("activation stopped while processing %s: %s", entity, providerFailureText(providerErr)))
	for key, value := range providerErr {
		if key != "content" && key != "isError" {
			out[key] = value
		}
	}
	out["platform"] = acct.Platform
	out["ad_account_id"] = acct.ID
	out["status"] = "partial"
	out["failed_step"] = entity
	out["failed_phase"] = phase
	out["steps"] = steps
	out["remaining"] = append([]string(nil), remaining...)
	return out
}

func normalizedDeliveryStatus(status string) string {
	if strings.EqualFold(status, "ACTIVE") || strings.EqualFold(status, "ENABLED") {
		return "ACTIVE"
	}
	if strings.EqualFold(status, "PAUSED") {
		return "PAUSED"
	}
	return status
}
