package main

import (
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"strings"
)

func (a *App) toolGeneralConversionEvents(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	acct, _, out := a.resolveAdAccount(ctx, args)
	if out != nil {
		return out, nil
	}
	if acct.Platform != "google" {
		return mcpError("select mobile_app_resource_id for this provider"), nil
	}
	if boolArgDefault(args, "refresh", true) {
		if err := a.refreshResourceKinds(ctx, acct, []string{resourceConversionAction})[resourceConversionAction]; err != "" {
			return mcpError(err), nil
		}
	}
	rows, err := a.listResources(ctx, acct, resourceConversionAction)
	if err != nil {
		return nil, err
	}
	items := []any{}
	for _, r := range rows {
		items = append(items, r.response())
	}
	return map[string]any{"events": items, "resources": items, "channel_link_status": "unknown", "channel_link_instructions": "YouTube engagement goals require the promoted channel to be linked in Google Ads. API v23 cannot directly verify the selected channel link."}, nil
}
func (a *App) validateDemandGenAssets(ctx *sdk.AppCtx, acct *adAccount, args map[string]any) map[string]any {
	fields := map[string]string{"video_asset_ids": "YOUTUBE_VIDEO", "logo_asset_ids": "IMAGE", "image_asset_ids": "IMAGE", "square_image_asset_ids": "IMAGE", "portrait_image_asset_ids": "IMAGE", "card_asset_ids": "DEMAND_GEN_CAROUSEL_CARD"}
	wanted := map[string]string{}
	ids := []string{}
	for field, typ := range fields {
		for _, raw := range asSlice(args[field]) {
			id := lastResourceSegment(toString(raw))
			if !googleNumericID(id) {
				return mcpError("invalid asset ID")
			}
			if old := wanted[id]; old != "" && old != typ {
				return mcpError("asset used with incompatible types")
			}
			if wanted[id] == "" {
				ids = append(ids, id)
			}
			wanted[id] = typ
		}
	}
	if len(ids) == 0 {
		return nil
	}
	rows, out := a.googleResourceRows(ctx, acct, "SELECT asset.id, asset.type, asset.image_asset.full_size.width_pixels, asset.image_asset.full_size.height_pixels, asset.demand_gen_carousel_card_asset.marketing_image_asset, asset.demand_gen_carousel_card_asset.square_marketing_image_asset, asset.demand_gen_carousel_card_asset.portrait_marketing_image_asset FROM asset WHERE asset.id IN ("+strings.Join(ids, ",")+")")
	if out != nil {
		return out
	}
	found := map[string]map[string]any{}
	for _, row := range rows {
		v := mapAt(row, "asset")
		id := firstString(v, "id")
		if typ, ok := wanted[id]; ok {
			if firstString(v, "type") != typ {
				return mcpError("selected asset " + id + " must have type " + typ)
			}
			found[id] = v
		}
	}
	for id := range wanted {
		if found[id] == nil {
			return mcpError("asset " + id + " is not accessible in the selected account")
		}
	}
	cardImages := map[string]bool{}
	for _, raw := range asSlice(args["card_asset_ids"]) {
		card := mapAt(found[lastResourceSegment(toString(raw))], "demandGenCarouselCardAsset")
		if len(card) == 0 {
			return mcpError("provider did not return carousel card image references")
		}
		for _, field := range []string{"marketingImageAsset", "squareMarketingImageAsset", "portraitMarketingImageAsset"} {
			if ref := firstString(card, field); ref != "" {
				if cardImages[ref] {
					return mcpError("each carousel card must use distinct image assets")
				}
				cardImages[ref] = true
			}
		}
	}
	for _, spec := range []struct {
		field      string
		ratio      float64
		minW, minH int
	}{{"logo_asset_ids", 1, 128, 128}, {"square_image_asset_ids", 1, 300, 300}, {"image_asset_ids", 1.91, 600, 314}, {"portrait_image_asset_ids", 0.8, 480, 600}} {
		for _, raw := range asSlice(args[spec.field]) {
			v := found[lastResourceSegment(toString(raw))]
			size := mapAt(mapAt(v, "imageAsset"), "fullSize")
			w, h := intArg(size, "widthPixels", 0), intArg(size, "heightPixels", 0)
			if w == 0 || h == 0 {
				return mcpError("provider did not return image dimensions for " + spec.field)
			}
			ratio := float64(w) / float64(h)
			if w < spec.minW || h < spec.minH || ratio < spec.ratio*.99 || ratio > spec.ratio*1.01 {
				return mcpError(fmt.Sprintf("%s asset requires %.2f aspect ratio and at least %dx%d pixels", spec.field, spec.ratio, spec.minW, spec.minH))
			}
		}
	}
	return nil
}
func (a *App) googleDemandGenCardCreate(ctx *sdk.AppCtx, acct *adAccount, args map[string]any) (any, error) {
	text, err := demandText([]any{firstString(args, "headline")}, "headline", 1, 1, 40)
	if err != nil {
		return mcpError(err.Error()), nil
	}
	urls, err := rsaFinalURLs([]any{firstString(args, "destination_url")})
	if err != nil {
		return mcpError(err.Error()), nil
	}
	card := map[string]any{"headline": asMap(text[0])["text"]}
	validation := map[string]any{}
	for _, spec := range []struct{ arg, field, list string }{{"image_asset_id", "marketingImageAsset", "image_asset_ids"}, {"square_image_asset_id", "squareMarketingImageAsset", "square_image_asset_ids"}, {"portrait_image_asset_id", "portraitMarketingImageAsset", "portrait_image_asset_ids"}} {
		if id := firstString(args, spec.arg); id != "" {
			refs, err := googleAssetRefs(acct.NativeAccountID, []any{id}, spec.arg, 1, 1)
			if err != nil {
				return mcpError(err.Error()), nil
			}
			card[spec.field] = asMap(refs[0])["asset"]
			validation[spec.list] = []any{id}
		}
	}
	if card["marketingImageAsset"] == nil && card["squareMarketingImageAsset"] == nil {
		return mcpError("carousel card requires an existing landscape or square image asset"), nil
	}
	if out := a.validateDemandGenAssets(ctx, acct, validation); out != nil {
		return out, nil
	}
	if c := firstString(args, "call_to_action"); c != "" {
		card["callToActionText"] = c
	}
	create := map[string]any{"name": firstString(args, "name"), "finalUrls": urls, "demandGenCarouselCardAsset": card}
	return a.execOrErr(ctx, acct, "asset_mutate", map[string]any{"customer_id": acct.NativeAccountID, "operations": []any{map[string]any{"create": create}}})
}
func (a *App) googleDemandGenAdUpdate(ctx *sdk.AppCtx, acct *adAccount, args map[string]any) (any, error) {
	group, id := firstString(args, "adset_id"), firstString(args, "ad_id")
	if !googleNumericID(group) {
		return mcpError("adset_id required for a structured ad update"), nil
	}
	ref := fmt.Sprintf("customers/%s/adGroupAds/%s~%s", acct.NativeAccountID, group, id)
	if strings.HasPrefix(id, "customers/") {
		prefix := fmt.Sprintf("customers/%s/adGroupAds/%s~", acct.NativeAccountID, group)
		if !strings.HasPrefix(id, prefix) || !googleNumericID(strings.TrimPrefix(id, prefix)) {
			return mcpError("ad_id must belong to selected account and ad group"), nil
		}
		ref = id
	} else if !googleNumericID(id) {
		return mcpError("invalid ad_id"), nil
	}
	if len(asMap(args["platform_options"])) > 0 {
		return mcpError("structured ad_format conflicts with native options"), nil
	}
	ad, err := googleDemandGenAd(args, acct.NativeAccountID)
	if err != nil {
		return mcpError(err.Error()), nil
	}
	if out := a.validateDemandGenAdParent(ctx, acct, group); out != nil {
		return out, nil
	}
	if out := a.validateDemandGenAssets(ctx, acct, args); out != nil {
		return out, nil
	}
	if !boolArg(args, "replace_creative") {
		return mcpError("Google API v23 treats Demand Gen creative fields as immutable. Set replace_creative=true and idempotency_key to create a paused replacement and pause the old ad."), nil
	}
	if firstString(args, "idempotency_key") == "" {
		return mcpError("creative replacement requires idempotency_key"), nil
	}
	if status := firstString(args, "status"); status != "" && status != "PAUSED" {
		return mcpError("creative replacements must be created PAUSED; activate the returned ad ID separately"), nil
	}
	return a.withCreateRequest(ctx, args, "ad_replacement", func(runCtx *sdk.AppCtx, _ map[string]any) (any, error) {
		ops := []any{map[string]any{"adGroupAdOperation": map[string]any{"create": map[string]any{"adGroup": googleAdGroupResource(acct.NativeAccountID, group), "status": "PAUSED", "ad": ad}}}, map[string]any{"adGroupAdOperation": map[string]any{"update": map[string]any{"resourceName": ref, "status": "PAUSED"}, "updateMask": "status"}}}
		parsed, out := a.execIntegrationTool(runCtx, acct, "bulk_mutate", map[string]any{"customer_id": acct.NativeAccountID, "mutateOperations": ops, "partialFailure": false})
		if out != nil {
			return out, nil
		}
		replacement := googleMutateID(parsed, "adGroupAdResult")
		if replacement == "" {
			return mcpError("provider returned no replacement ad ID; reconcile before retrying"), nil
		}
		cid, _ := a.campaignIDForEntity(runCtx, acct, "ad_group", group)
		if cid == "" {
			rows, out := a.googleResourceRows(runCtx, acct, "SELECT ad_group.id, campaign.id FROM ad_group WHERE ad_group.id = "+group)
			if out != nil {
				return map[string]any{"id": replacement, "replaces_ad_id": id, "reconciliation_pending": true, "result": parsed}, nil
			}
			if len(rows) == 1 {
				cid = firstString(mapAt(rows[0], "campaign"), "id")
			}
		}
		row := cloneMap(args)
		row["id"] = replacement
		row["adset_id"] = group
		row["status"] = "PAUSED"
		if err := a.upsertDeliveryEntities(runCtx, acct, "ad", []map[string]any{row}, cid); err != nil {
			return nil, err
		}
		_, err := runCtx.AppDB().Exec(`UPDATE ad_entities SET status='PAUSED' WHERE project_id=? AND ad_account_id=? AND level='ad' AND native_entity_id=?`, acct.ProjectID, acct.ID, lastResourceSegment(id))
		if err != nil {
			return nil, err
		}
		return map[string]any{"id": replacement, "replacement_ad_id": replacement, "replaces_ad_id": id, "adset_id": group, "status": "PAUSED", "result": parsed}, nil
	})
}
