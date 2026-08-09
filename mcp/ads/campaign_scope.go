package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const (
	managementModeAll      = "all"
	managementModeSelected = "selected"
	maxCampaignPickerPages = 200
)

func (a *App) toolAccountManagementModeGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	acct, _, errOut := a.resolveAdAccount(ctx, args)
	if errOut != nil {
		return errOut, nil
	}
	ids, err := a.managedCampaignIDs(ctx, acct)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ad_account_id": acct.ID, "mode": acct.ManagementMode, "managed_campaign_ids": ids}, nil
}

func (a *App) toolAccountManagementModeUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	acct, _, errOut := a.resolveAdAccount(ctx, args)
	if errOut != nil {
		return errOut, nil
	}
	mode := strings.ToLower(strings.TrimSpace(stringArgAny(args, "mode")))
	if mode != managementModeAll && mode != managementModeSelected {
		return mcpError("mode must be all or selected"), nil
	}
	if _, err := ctx.AppDB().Exec(
		`UPDATE ad_accounts SET management_mode=? WHERE id=? AND project_id=?`,
		mode, acct.ID, acct.ProjectID,
	); err != nil {
		return nil, err
	}
	acct.ManagementMode = mode
	if mode == managementModeSelected {
		if err := a.purgeUnmanagedCampaignCache(ctx, acct); err != nil {
			return nil, err
		}
	}
	ids, err := a.managedCampaignIDs(ctx, acct)
	if err != nil {
		return nil, err
	}
	ctx.EmitWithProject("account.changed", acct.ProjectID, map[string]any{
		"ad_account_id": acct.ID, "platform": acct.Platform, "action": "management_mode_updated", "management_mode": mode,
	})
	return map[string]any{"ad_account_id": acct.ID, "mode": mode, "managed_campaign_ids": ids}, nil
}

func (a *App) toolCampaignImportCandidatesList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	acct, def, errOut := a.resolveAdAccount(ctx, args)
	if errOut != nil {
		return errOut, nil
	}
	rows, providerErr := a.listAllProviderCampaigns(ctx, acct, def, args)
	if providerErr != nil {
		return providerErr, nil
	}
	managed, err := a.managedCampaignSet(ctx, acct)
	if err != nil {
		return nil, err
	}
	query := strings.ToLower(strings.TrimSpace(stringArgAny(args, "q")))
	data := make([]map[string]any, 0, len(rows))
	seen := make(map[string]bool, len(rows))
	for _, row := range rows {
		id := firstString(row, "id")
		if id == "" {
			continue
		}
		seen[id] = true
		if query != "" && !strings.Contains(strings.ToLower(firstString(row, "name")+" "+id), query) {
			continue
		}
		item := cloneMap(row)
		item["managed"] = managed[id]
		claimed, err := a.campaignClaimedElsewhere(ctx, acct, id)
		if err != nil {
			return nil, err
		}
		item["managed_elsewhere"] = claimed
		data = append(data, item)
	}
	stored, err := a.scopedEntityRows(ctx, acct, "campaign", "", "")
	if err != nil {
		return nil, err
	}
	for _, row := range stored {
		id := firstString(row, "id")
		if id == "" || seen[id] || !managed[id] {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(firstString(row, "name")+" "+id), query) {
			continue
		}
		item := cloneMap(row)
		item["managed"] = true
		item["managed_elsewhere"] = false
		item["unavailable"] = true
		data = append(data, item)
	}
	return map[string]any{"data": data, "management_mode": acct.ManagementMode, "next_cursor": nil}, nil
}

func (a *App) toolCampaignImport(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	acct, def, errOut := a.resolveAdAccount(ctx, args)
	if errOut != nil {
		return errOut, nil
	}
	ids, err := providerIDs(args["campaign_ids"], 200)
	if err != nil || len(ids) == 0 {
		if err == nil {
			err = fmt.Errorf("campaign_ids required")
		}
		return mcpError(err.Error()), nil
	}
	rows, providerErr := a.listAllProviderCampaigns(ctx, acct, def, map[string]any{})
	if providerErr != nil {
		return providerErr, nil
	}
	available := make(map[string]map[string]any, len(rows))
	for _, row := range rows {
		if id := firstString(row, "id"); id != "" {
			available[id] = row
		}
	}
	allowShared := boolArg(args, "allow_shared")
	for _, id := range ids {
		if available[id] == nil {
			return mcpError("campaign " + id + " is not accessible through this ad account"), nil
		}
		claimed, claimErr := a.campaignClaimedElsewhere(ctx, acct, id)
		if claimErr != nil {
			return nil, claimErr
		}
		if claimed && !allowShared {
			return mcpError("campaign " + id + " is managed by another project; transfer it there first or explicitly set allow_shared=true"), nil
		}
	}
	for _, id := range ids {
		if err := a.upsertManagedCampaign(ctx, acct, available[id], "imported"); err != nil {
			return nil, err
		}
	}
	if errOut := a.refreshManagedHierarchy(ctx, acct, def); errOut != nil {
		return errOut, nil
	}
	ctx.EmitWithProject("entity.changed", acct.ProjectID, map[string]any{
		"ad_account_id": acct.ID, "platform": acct.Platform, "level": "campaign", "action": "imported", "campaign_ids": ids,
	})
	return map[string]any{"imported": ids, "count": len(ids), "management_mode": acct.ManagementMode}, nil
}

func (a *App) toolCampaignRemoveFromProject(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	acct, _, errOut := a.resolveAdAccount(ctx, args)
	if errOut != nil {
		return errOut, nil
	}
	if acct.ManagementMode != managementModeSelected {
		return mcpError("switch the account to selected mode before removing individual campaigns"), nil
	}
	id := strings.TrimSpace(stringArgAny(args, "campaign_id"))
	if id == "" || !safeProviderID(id) {
		return mcpError("valid campaign_id required"), nil
	}
	if errOut := a.requireManagedCampaign(ctx, acct, id); errOut != nil {
		return errOut, nil
	}
	if err := a.removeCampaignLocal(ctx, acct, id); err != nil {
		return nil, err
	}
	ctx.EmitWithProject("entity.changed", acct.ProjectID, map[string]any{
		"ad_account_id": acct.ID, "platform": acct.Platform, "level": "campaign", "entity_id": id, "action": "removed_from_project",
	})
	return map[string]any{"removed": true, "campaign_id": id, "upstream_deleted": false}, nil
}

func (a *App) listAllProviderCampaigns(ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) ([]map[string]any, map[string]any) {
	rows := make([]map[string]any, 0)
	after := ""
	for page := 0; page < maxCampaignPickerPages; page++ {
		pageArgs := cloneMap(args)
		pageArgs["limit"] = 200
		if after != "" {
			pageArgs["after"] = after
		} else {
			delete(pageArgs, "after")
		}
		parsed, err := platformAdapters[acct.Platform].CampaignList(a, ctx, acct, def, pageArgs)
		if err != nil {
			return nil, mcpError("list campaigns: " + err.Error())
		}
		if errOut := mcpResultError(parsed); errOut != nil {
			return nil, errOut
		}
		rows = append(rows, resultRows(parsed)...)
		next := providerPageCursor(parsed, acct.Platform)
		if next == "" {
			return rows, nil
		}
		if next == after {
			return nil, mcpError("campaign pagination returned a repeated cursor")
		}
		after = next
	}
	return nil, mcpError("campaign pagination exceeded the safety limit")
}

func providerPageCursor(value any, platform string) string {
	if cursor := firstString(asMap(value), "next_cursor", "nextPageToken", "next_page_token"); cursor != "" {
		return cursor
	}
	if platform == "meta" {
		return metaNextCursor(value)
	}
	return ""
}

func providerIDs(value any, maximum int) ([]string, error) {
	var values []any
	switch typed := value.(type) {
	case []any:
		values = typed
	case []string:
		values = make([]any, len(typed))
		for i, item := range typed {
			values[i] = item
		}
	default:
		return nil, fmt.Errorf("campaign_ids must be an array")
	}
	if len(values) > maximum {
		return nil, fmt.Errorf("campaign_ids supports at most %d ids", maximum)
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		id := strings.TrimSpace(toString(value))
		if !safeProviderID(id) {
			return nil, fmt.Errorf("campaign_ids contain an invalid provider ID")
		}
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out, nil
}

func (a *App) managedCampaignIDs(ctx *sdk.AppCtx, acct *adAccount) ([]string, error) {
	rows, err := ctx.AppDB().Query(
		`SELECT native_entity_id FROM ad_entities
		 WHERE project_id=? AND ad_account_id=? AND level='campaign' AND is_managed=1
		 ORDER BY managed_at, native_entity_id`,
		acct.ProjectID, acct.ID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (a *App) managedCampaignSet(ctx *sdk.AppCtx, acct *adAccount) (map[string]bool, error) {
	ids, err := a.managedCampaignIDs(ctx, acct)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(ids))
	for _, id := range ids {
		out[id] = true
	}
	return out, nil
}

func (a *App) requireManagedCampaign(ctx *sdk.AppCtx, acct *adAccount, campaignID string) map[string]any {
	if acct.ManagementMode != managementModeSelected {
		return nil
	}
	var found int
	err := ctx.AppDB().QueryRow(
		`SELECT 1 FROM ad_entities WHERE project_id=? AND ad_account_id=?
		 AND level='campaign' AND native_entity_id=? AND is_managed=1`,
		acct.ProjectID, acct.ID, campaignID,
	).Scan(&found)
	if err == nil {
		return nil
	}
	if err != sql.ErrNoRows {
		return mcpError("could not validate campaign management scope")
	}
	return mcpError("campaign is not managed by this project; import it first")
}

func (a *App) campaignClaimedElsewhere(ctx *sdk.AppCtx, acct *adAccount, campaignID string) (bool, error) {
	var found int
	err := ctx.AppDB().QueryRow(
		`SELECT 1 FROM ad_entities e
		 JOIN ad_accounts aa ON aa.id=e.ad_account_id AND aa.project_id=e.project_id
		 WHERE e.level='campaign' AND e.native_entity_id=? AND e.is_managed=1
		   AND aa.platform=? AND aa.native_account_id=? AND e.project_id<>?
		 LIMIT 1`,
		campaignID, acct.Platform, acct.NativeAccountID, acct.ProjectID,
	).Scan(&found)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}

func (a *App) upsertManagedCampaign(ctx *sdk.AppCtx, acct *adAccount, campaign map[string]any, source string) error {
	id := firstString(campaign, "id")
	if id == "" {
		return fmt.Errorf("campaign id required")
	}
	providerJSON, _ := json.Marshal(campaign)
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := ctx.AppDB().Exec(
		`INSERT INTO ad_entities (
		 project_id, ad_account_id, platform, level, native_entity_id, name,
		 campaign_id, status, provider_data_json, last_seen_at,
		 is_managed, managed_source, managed_at
		) VALUES (?, ?, ?, 'campaign', ?, ?, ?, ?, ?, ?, 1, ?, ?)
		ON CONFLICT(project_id, ad_account_id, level, native_entity_id) DO UPDATE SET
		 name=excluded.name, campaign_id=excluded.campaign_id, status=excluded.status,
		 provider_data_json=excluded.provider_data_json, last_seen_at=excluded.last_seen_at,
			 is_managed=1, managed_source=CASE WHEN excluded.managed_source='' THEN ad_entities.managed_source ELSE excluded.managed_source END,
		 managed_at=COALESCE(ad_entities.managed_at, excluded.managed_at), updated_at=CURRENT_TIMESTAMP`,
		acct.ProjectID, acct.ID, acct.Platform, id, firstString(campaign, "name"), id,
		firstString(campaign, "effective_status", "status"), string(providerJSON), now, source, now,
	)
	return err
}

func (a *App) campaignIDForEntity(ctx *sdk.AppCtx, acct *adAccount, level, entityID string) (string, error) {
	var campaignID string
	err := ctx.AppDB().QueryRow(
		`SELECT campaign_id FROM ad_entities WHERE project_id=? AND ad_account_id=? AND level=? AND native_entity_id=?`,
		acct.ProjectID, acct.ID, level, entityID,
	).Scan(&campaignID)
	return campaignID, err
}

func (a *App) requireManagedEntity(ctx *sdk.AppCtx, acct *adAccount, def *platformDef, level, entityID string) map[string]any {
	if acct.ManagementMode != managementModeSelected {
		return nil
	}
	if strings.TrimSpace(entityID) == "" {
		return mcpError(level + " id required")
	}
	if errOut := a.refreshManagedHierarchy(ctx, acct, def); errOut != nil {
		return errOut
	}
	campaignID, err := a.campaignIDForEntity(ctx, acct, level, entityID)
	if err == sql.ErrNoRows {
		return mcpError(level + " is not part of a campaign managed by this project")
	}
	if err != nil {
		return mcpError("could not validate " + level + " management scope")
	}
	return a.requireManagedCampaign(ctx, acct, campaignID)
}

func (a *App) managedEntityIDs(ctx *sdk.AppCtx, acct *adAccount, level string) ([]string, error) {
	rows, err := ctx.AppDB().Query(
		`SELECT e.native_entity_id FROM ad_entities e
		 WHERE e.project_id=? AND e.ad_account_id=? AND e.level=?
		   AND e.campaign_id IN (
		     SELECT native_entity_id FROM ad_entities
		     WHERE project_id=? AND ad_account_id=? AND level='campaign' AND is_managed=1
		   )
		 ORDER BY e.native_entity_id`,
		acct.ProjectID, acct.ID, level, acct.ProjectID, acct.ID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (a *App) scopedEntityRows(ctx *sdk.AppCtx, acct *adAccount, level, campaignID, adGroupID string) ([]map[string]any, error) {
	query := `SELECT provider_data_json FROM ad_entities WHERE project_id=? AND ad_account_id=? AND level=?`
	params := []any{acct.ProjectID, acct.ID, level}
	if campaignID != "" {
		query += ` AND campaign_id=?`
		params = append(params, campaignID)
	}
	if adGroupID != "" {
		query += ` AND ad_group_id=?`
		params = append(params, adGroupID)
	}
	query += ` ORDER BY name, native_entity_id`
	rows, err := ctx.AppDB().Query(query, params...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]map[string]any, 0)
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		item := map[string]any{}
		if json.Unmarshal([]byte(raw), &item) == nil {
			out = append(out, item)
		}
	}
	return out, rows.Err()
}

func createdProviderID(value any, kind string) string {
	if kind == "campaign" {
		if campaign := asMap(asMap(value)["campaign"]); len(campaign) > 0 {
			if id := createdProviderID(campaign, kind); id != "" {
				return id
			}
		}
	}
	var walk func(any) string
	walk = func(current any) string {
		switch typed := current.(type) {
		case map[string]any:
			if id := firstString(typed, "id"); safeProviderID(id) {
				return id
			}
			for _, key := range []string{"resourceName", "resource_name"} {
				if resource := strings.TrimSpace(toString(typed[key])); resource != "" {
					parts := strings.Split(resource, "/")
					candidate := parts[len(parts)-1]
					if kind == "ad" && strings.Contains(candidate, "~") {
						pieces := strings.Split(candidate, "~")
						candidate = pieces[len(pieces)-1]
					}
					if safeProviderID(candidate) {
						return candidate
					}
				}
			}
			for _, key := range []string{"results", "data", "result", "response"} {
				if id := walk(typed[key]); id != "" {
					return id
				}
			}
		case []any:
			for _, item := range typed {
				if id := walk(item); id != "" {
					return id
				}
			}
		}
		return ""
	}
	return walk(value)
}

func successfulProviderResult(value any) bool {
	return mcpResultError(value) == nil
}

func (a *App) removeCampaignLocal(ctx *sdk.AppCtx, acct *adAccount, campaignID string) error {
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM ad_metric_points WHERE project_id=? AND ad_account_id=? AND campaign_id=?`, acct.ProjectID, acct.ID, campaignID); err != nil {
		return err
	}
	if _, err := tx.Exec(
		`DELETE FROM ad_entities WHERE project_id=? AND ad_account_id=? AND (campaign_id=? OR (level='campaign' AND native_entity_id=?))`,
		acct.ProjectID, acct.ID, campaignID, campaignID,
	); err != nil {
		return err
	}
	return tx.Commit()
}

func (a *App) upsertDeliveryEntities(ctx *sdk.AppCtx, acct *adAccount, level string, rows []map[string]any, fallbackCampaignID string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	for _, row := range rows {
		id := firstString(row, "id", "native_id")
		if id == "" {
			continue
		}
		campaignID := firstString(row, "campaign_id")
		adGroupID := firstString(row, "adset_id", "ad_group_id")
		if campaignID == "" {
			campaignID = fallbackCampaignID
		}
		if campaignID == "" && level == "ad" && adGroupID != "" {
			campaignID, _ = a.campaignIDForEntity(ctx, acct, "ad_group", adGroupID)
		}
		if acct.ManagementMode == managementModeSelected && a.requireManagedCampaign(ctx, acct, campaignID) != nil {
			continue
		}
		providerJSON, _ := json.Marshal(row)
		if _, err := ctx.AppDB().Exec(
			`INSERT INTO ad_entities (
			 project_id, ad_account_id, platform, level, native_entity_id, name,
			 campaign_id, ad_group_id, status, provider_data_json, last_seen_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(project_id, ad_account_id, level, native_entity_id) DO UPDATE SET
			 name=excluded.name, campaign_id=excluded.campaign_id, ad_group_id=excluded.ad_group_id,
			 status=excluded.status, provider_data_json=excluded.provider_data_json,
			 last_seen_at=excluded.last_seen_at, updated_at=CURRENT_TIMESTAMP`,
			acct.ProjectID, acct.ID, acct.Platform, level, id, firstString(row, "name"),
			campaignID, adGroupID, firstString(row, "effective_status", "status"), string(providerJSON), now,
		); err != nil {
			return err
		}
		if level == "ad" {
			if err := a.upsertCreativeReference(ctx, acct, row, campaignID); err != nil {
				return err
			}
		}
	}
	return nil
}

func (a *App) upsertCreativeReference(ctx *sdk.AppCtx, acct *adAccount, row map[string]any, campaignID string) error {
	creative := asMap(row["creative"])
	creativeID := firstString(creative, "id")
	if creativeID == "" {
		creativeID = firstString(row, "creative_id", "post_id", "tweet_id")
	}
	if creativeID == "" {
		return nil
	}
	if len(creative) == 0 {
		creative = map[string]any{"id": creativeID, "name": creativeID}
	}
	return a.upsertManagedCreative(ctx, acct, creative, campaignID, "referenced")
}

func (a *App) upsertManagedCreative(ctx *sdk.AppCtx, acct *adAccount, creative map[string]any, campaignID, source string) error {
	id := firstString(creative, "id")
	if id == "" {
		return fmt.Errorf("creative id required")
	}
	providerJSON, _ := json.Marshal(creative)
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := ctx.AppDB().Exec(
		`INSERT INTO ad_entities (
		 project_id, ad_account_id, platform, level, native_entity_id, name,
		 campaign_id, provider_data_json, last_seen_at, is_managed, managed_source, managed_at
		) VALUES (?, ?, ?, 'creative', ?, ?, ?, ?, ?, 1, ?, ?)
		ON CONFLICT(project_id, ad_account_id, level, native_entity_id) DO UPDATE SET
		 name=excluded.name, campaign_id=CASE WHEN excluded.campaign_id='' THEN ad_entities.campaign_id ELSE excluded.campaign_id END,
		 provider_data_json=excluded.provider_data_json, last_seen_at=excluded.last_seen_at,
		 is_managed=1, managed_source=CASE WHEN ad_entities.managed_source='created' THEN 'created' ELSE excluded.managed_source END,
		 managed_at=COALESCE(ad_entities.managed_at, excluded.managed_at), updated_at=CURRENT_TIMESTAMP`,
		acct.ProjectID, acct.ID, acct.Platform, id, firstString(creative, "name"), campaignID,
		string(providerJSON), now, source, now,
	)
	return err
}

func (a *App) requireManagedCreative(ctx *sdk.AppCtx, acct *adAccount, creativeID string) map[string]any {
	if acct.ManagementMode != managementModeSelected || creativeID == "" {
		return nil
	}
	var found int
	err := ctx.AppDB().QueryRow(
		`SELECT 1 FROM ad_entities WHERE project_id=? AND ad_account_id=? AND level='creative'
		 AND native_entity_id=? AND is_managed=1`,
		acct.ProjectID, acct.ID, creativeID,
	).Scan(&found)
	if err == nil {
		return nil
	}
	if err != sql.ErrNoRows {
		return mcpError("could not validate creative management scope")
	}
	return mcpError("creative is not available in this project; use one referenced by a managed ad or create it here")
}

func (a *App) purgeUnmanagedCampaignCache(ctx *sdk.AppCtx, acct *adAccount) error {
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	managed := `SELECT native_entity_id FROM ad_entities
	            WHERE project_id=? AND ad_account_id=? AND level='campaign' AND is_managed=1`
	if _, err := tx.Exec(
		`DELETE FROM ad_metric_points WHERE project_id=? AND ad_account_id=? AND campaign_id<>''
		 AND campaign_id NOT IN (`+managed+`)`,
		acct.ProjectID, acct.ID, acct.ProjectID, acct.ID,
	); err != nil {
		return err
	}
	if _, err := tx.Exec(
		`DELETE FROM ad_entities WHERE project_id=? AND ad_account_id=? AND
		 ((level='campaign' AND is_managed=0) OR (level<>'campaign' AND campaign_id NOT IN (`+managed+`)))`,
		acct.ProjectID, acct.ID, acct.ProjectID, acct.ID,
	); err != nil {
		return err
	}
	return tx.Commit()
}

func filterCampaignRows(rows []map[string]any, allowed map[string]bool) []map[string]any {
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		if allowed[firstString(row, "id")] {
			out = append(out, row)
		}
	}
	return out
}

func (a *App) refreshManagedHierarchy(ctx *sdk.AppCtx, acct *adAccount, def *platformDef) map[string]any {
	if acct.ManagementMode != managementModeSelected {
		return nil
	}
	campaignIDs, err := a.managedCampaignIDs(ctx, acct)
	if err != nil {
		return mcpError("load managed campaigns: " + err.Error())
	}
	for _, campaignID := range campaignIDs {
		adSetRows, errOut := a.listAllProviderChildren(ctx, acct, def, "ad_group", map[string]any{"campaign_id": campaignID})
		if errOut != nil {
			return errOut
		}
		if err := a.upsertDeliveryEntities(ctx, acct, "ad_group", adSetRows, campaignID); err != nil {
			return mcpError("store managed ad sets: " + err.Error())
		}
		for _, adSet := range adSetRows {
			adSetID := firstString(adSet, "id", "native_id")
			if adSetID == "" {
				continue
			}
			adRows, errOut := a.listAllProviderChildren(ctx, acct, def, "ad", map[string]any{"adset_id": adSetID})
			if errOut != nil {
				return errOut
			}
			if err := a.upsertDeliveryEntities(ctx, acct, "ad", adRows, campaignID); err != nil {
				return mcpError("store managed ads: " + err.Error())
			}
		}
	}
	return nil
}

func (a *App) listAllProviderChildren(
	ctx *sdk.AppCtx,
	acct *adAccount,
	def *platformDef,
	level string,
	args map[string]any,
) ([]map[string]any, map[string]any) {
	rows := make([]map[string]any, 0)
	after := ""
	for page := 0; page < maxCampaignPickerPages; page++ {
		pageArgs := cloneMap(args)
		pageArgs["limit"] = 500
		if after != "" {
			pageArgs["after"] = after
		}
		var parsed any
		var err error
		if level == "ad_group" {
			parsed, err = platformAdapters[acct.Platform].AdSetList(a, ctx, acct, def, pageArgs)
		} else {
			parsed, err = platformAdapters[acct.Platform].AdList(a, ctx, acct, def, pageArgs)
		}
		if err != nil {
			return nil, mcpError("list managed " + level + ": " + err.Error())
		}
		if errOut := mcpResultError(parsed); errOut != nil {
			return nil, errOut
		}
		rows = append(rows, resultRows(parsed)...)
		next := providerPageCursor(parsed, acct.Platform)
		if next == "" {
			return rows, nil
		}
		if next == after {
			return nil, mcpError(level + " pagination returned a repeated cursor")
		}
		after = next
	}
	return nil, mcpError(level + " pagination exceeded the safety limit")
}
