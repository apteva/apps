package main

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	sdk "github.com/apteva/app-sdk"
)

// Keyword targeting is a Google Ads primitive: an ad group is a keyword
// container, and a Search campaign serves nothing without them. Meta, X and
// Reddit have no equivalent, so the surface is gated rather than emulated.

const (
	keywordLevelAdGroup  = "ad_group"
	keywordLevelCampaign = "campaign"

	// Google caps a keyword at 80 characters and 10 words.
	keywordMaxRunes = 80
	keywordMaxWords = 10
)

var keywordMatchTypes = map[string]string{
	"exact":  "EXACT",
	"phrase": "PHRASE",
	"broad":  "BROAD",
}

func googleMatchType(raw string) (string, error) {
	value := strings.TrimSpace(strings.ToLower(raw))
	if value == "" {
		return "BROAD", nil
	}
	if mapped, ok := keywordMatchTypes[value]; ok {
		return mapped, nil
	}
	return "", fmt.Errorf("match_type must be exact, phrase, or broad")
}

// Google expresses match type as a field, not as punctuation. Text carrying
// operator syntax is reinterpreted by the provider, producing a keyword the
// caller did not ask for, so reject it instead of silently reshaping it.
func validateKeywordText(text string) error {
	if utf8.RuneCountInString(text) > keywordMaxRunes {
		return fmt.Errorf("text must be %d characters or fewer", keywordMaxRunes)
	}
	if len(strings.Fields(text)) > keywordMaxWords {
		return fmt.Errorf("text must be %d words or fewer", keywordMaxWords)
	}
	if strings.ContainsAny(text, `"[]`) || strings.HasPrefix(text, "+") {
		return fmt.Errorf(`text must not carry match-type syntax ("", [], +); use match_type instead`)
	}
	return nil
}

type keywordSpec struct {
	Text      string
	MatchType string
	BidMicros string
}

func normalizedKeywordSpecs(raw any, allowBids bool) ([]keywordSpec, error) {
	list, ok := raw.([]any)
	if !ok || len(list) == 0 {
		return nil, fmt.Errorf("keywords must be a non-empty array")
	}
	specs := make([]keywordSpec, 0, len(list))
	seen := make(map[string]bool, len(list))
	for i, entry := range list {
		item := asMap(entry)
		if len(item) == 0 {
			if text := strings.TrimSpace(toString(entry)); text != "" {
				item = map[string]any{"text": text}
			}
		}
		text := strings.TrimSpace(toString(item["text"]))
		if text == "" {
			return nil, fmt.Errorf("keywords[%d].text is required", i)
		}
		if err := validateKeywordText(text); err != nil {
			return nil, fmt.Errorf("keywords[%d]: %w", i, err)
		}
		match, err := googleMatchType(toString(item["match_type"]))
		if err != nil {
			return nil, fmt.Errorf("keywords[%d]: %w", i, err)
		}
		// Google treats match type plus normalized text as the identity of a
		// keyword, so a repeat inside one call is a duplicate, not a second row.
		key := match + "|" + strings.ToLower(text)
		if seen[key] {
			continue
		}
		seen[key] = true
		spec := keywordSpec{Text: text, MatchType: match}
		if bid := intArg(item, "cpc_bid_cents", 0); bid > 0 {
			if !allowBids {
				return nil, fmt.Errorf("keywords[%d]: negative keywords cannot carry a bid", i)
			}
			spec.BidMicros = strconv.Itoa(bid * 10000)
		}
		specs = append(specs, spec)
	}
	if len(specs) == 0 {
		return nil, fmt.Errorf("keywords must contain at least one unique entry")
	}
	return specs, nil
}

// googleOnlyError reports a surface that exists only on Google Ads. The
// message names the operation so a caller sees which capability is missing
// rather than a generic platform complaint.
func googleOnlyError(platform, operation string) map[string]any {
	out := mcpError(operation + " is supported on Google Ads only, not " + platform)
	out["code"] = "unsupported_operation"
	out["operation"] = operation
	out["platform"] = platform
	return out
}

func googleAdGroupCriterionResource(customerID, adGroupID, criterionID string) string {
	return fmt.Sprintf("customers/%s/adGroupCriteria/%s~%s", customerID, adGroupID, criterionID)
}

func googleCampaignCriterionResource(customerID, campaignID, criterionID string) string {
	return fmt.Sprintf("customers/%s/campaignCriteria/%s~%s", customerID, campaignID, criterionID)
}

// googleSearchRows runs one GAQL read and returns normalized result rows.
func (a *App) googleSearchRows(ctx *sdk.AppCtx, acct *adAccount, query string) ([]map[string]any, map[string]any) {
	parsed, errOut := a.execIntegrationTool(ctx, acct, "search", map[string]any{
		"customer_id": acct.NativeAccountID,
		"query":       query,
	})
	if errOut != nil {
		return nil, errOut
	}
	return resultRows(parsed), nil
}

// resolveGoogleTarget validates the platform, the requested level and the
// project scope of the parent entity in one place.
func (a *App) resolveGoogleTarget(
	ctx *sdk.AppCtx, args map[string]any, operation string, level string,
) (*adAccount, *platformDef, string, map[string]any) {
	acct, def, errOut := a.resolveAdAccount(ctx, args)
	if errOut != nil {
		return nil, nil, "", errOut
	}
	if acct.Platform != "google" {
		return nil, nil, "", googleOnlyError(acct.Platform, operation)
	}
	switch level {
	case keywordLevelCampaign:
		campaignID := stringArgAny(args, "campaign_id")
		if !googleNumericID(campaignID) {
			return nil, nil, "", mcpError("google campaign_id must be numeric")
		}
		if scopeErr := a.requireManagedCampaign(ctx, acct, campaignID); scopeErr != nil {
			return nil, nil, "", scopeErr
		}
		return acct, def, campaignID, nil
	default:
		adGroupID := stringArgAny(args, "adset_id")
		if !googleNumericID(adGroupID) {
			return nil, nil, "", mcpError("google adset_id must be numeric")
		}
		if scopeErr := a.requireManagedEntity(ctx, acct, def, "ad_group", adGroupID); scopeErr != nil {
			return nil, nil, "", scopeErr
		}
		return acct, def, adGroupID, nil
	}
}

// keywordIDs parses criterion ids. providerIDs exists but names campaign_ids
// in every error, which would misdirect a caller fixing a keyword payload.
func keywordIDs(raw any) ([]string, error) {
	list, ok := raw.([]any)
	if !ok || len(list) == 0 {
		return nil, fmt.Errorf("keyword_ids must contain at least one criterion id")
	}
	if len(list) > 1000 {
		return nil, fmt.Errorf("keyword_ids supports at most 1000 ids")
	}
	seen := make(map[string]bool, len(list))
	ids := make([]string, 0, len(list))
	for i, entry := range list {
		id := strings.TrimSpace(toString(entry))
		if !googleNumericID(id) {
			return nil, fmt.Errorf("keyword_ids[%d] must be a numeric criterion id", i)
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	return ids, nil
}

func keywordListSchema() map[string]any {
	return schemaObject(map[string]any{
		"ad_account_id":     map[string]any{"type": "integer"},
		"adset_id":          map[string]any{"type": "string", "description": "Google ad group id."},
		"include_negatives": map[string]any{"type": "boolean", "default": false},
		"limit":             map[string]any{"type": "integer", "minimum": 1, "maximum": 5000, "default": 500},
	}, []string{"ad_account_id", "adset_id"})
}

func keywordSpecArraySchema(allowBids bool) map[string]any {
	props := map[string]any{
		"text":       map[string]any{"type": "string", "minLength": 1, "maxLength": keywordMaxRunes},
		"match_type": map[string]any{"type": "string", "enum": []string{"exact", "phrase", "broad"}, "default": "broad"},
	}
	if allowBids {
		props["cpc_bid_cents"] = map[string]any{"type": "integer", "minimum": 0}
	}
	return map[string]any{
		"type": "array", "minItems": 1, "maxItems": 1000,
		"items": map[string]any{
			"type":       "object",
			"properties": props,
			"required":   []string{"text"},
		},
	}
}

func (a *App) toolKeywordCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	acct, def, adGroupID, errOut := a.resolveGoogleTarget(ctx, args, "keyword_create", keywordLevelAdGroup)
	if errOut != nil {
		return errOut, nil
	}
	status := stringArgAny(args, "status")
	if status == "" {
		status = "ACTIVE"
	}
	if !googleValidStatus(status) {
		return mcpError("status must be ACTIVE or PAUSED"), nil
	}
	specs, err := normalizedKeywordSpecs(args["keywords"], true)
	if err != nil {
		return mcpError(err.Error()), nil
	}
	adGroup := googleAdGroupResource(acct.NativeAccountID, adGroupID)
	operations := make([]any, 0, len(specs))
	for _, spec := range specs {
		criterion := map[string]any{
			"adGroup": adGroup,
			"status":  googleCampaignStatus(status),
			"keyword": map[string]any{"text": spec.Text, "matchType": spec.MatchType},
		}
		if spec.BidMicros != "" {
			criterion["cpcBidMicros"] = spec.BidMicros
		}
		operations = append(operations, map[string]any{"create": criterion})
	}
	out, errOut := a.execIntegrationTool(ctx, acct, def.KeywordMutateTool, map[string]any{
		"customer_id": acct.NativeAccountID,
		"operations":  operations,
	})
	if errOut != nil {
		return errOut, nil
	}
	a.emitEntityChanged(ctx, acct, "keyword", "created", args, out, nil)
	return map[string]any{"requested": len(specs), "keywords": out}, nil
}

func (a *App) toolKeywordList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	acct, _, adGroupID, errOut := a.resolveGoogleTarget(ctx, args, "keyword_list", keywordLevelAdGroup)
	if errOut != nil {
		return errOut, nil
	}
	limit := intArg(args, "limit", 500)
	if limit <= 0 || limit > 5000 {
		limit = 500
	}
	query := fmt.Sprintf(
		"SELECT ad_group.id, ad_group_criterion.criterion_id, ad_group_criterion.keyword.text, "+
			"ad_group_criterion.keyword.match_type, ad_group_criterion.status, "+
			"ad_group_criterion.negative, ad_group_criterion.cpc_bid_micros "+
			"FROM ad_group_criterion WHERE ad_group_criterion.type = KEYWORD "+
			"AND ad_group_criterion.status != REMOVED AND ad_group.id = %s LIMIT %d",
		adGroupID, limit,
	)
	rows, errOut := a.googleSearchRows(ctx, acct, query)
	if errOut != nil {
		return errOut, nil
	}
	includeNegatives := boolArgDefault(args, "include_negatives", false)
	keywords := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		criterion := mapAt(row, "adGroupCriterion")
		if len(criterion) == 0 {
			criterion = mapAt(row, "ad_group_criterion")
		}
		negative := googleBool(criterion["negative"])
		if negative && !includeNegatives {
			continue
		}
		keyword := mapAt(criterion, "keyword")
		item := map[string]any{
			"id":          firstString(criterion, "criterionId", "criterion_id"),
			"text":        firstString(keyword, "text"),
			"match_type":  firstString(keyword, "matchType", "match_type"),
			"status":      firstString(criterion, "status"),
			"negative":    negative,
			"ad_group_id": adGroupID,
		}
		if micros := firstString(criterion, "cpcBidMicros", "cpc_bid_micros"); micros != "" {
			item["cpc_bid_micros"] = micros
		}
		keywords = append(keywords, item)
	}
	return map[string]any{"ad_group_id": adGroupID, "count": len(keywords), "keywords": keywords}, nil
}

func (a *App) toolKeywordUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	acct, def, adGroupID, errOut := a.resolveGoogleTarget(ctx, args, "keyword_update", keywordLevelAdGroup)
	if errOut != nil {
		return errOut, nil
	}
	keywordID := stringArgAny(args, "keyword_id")
	if !googleNumericID(keywordID) {
		return mcpError("keyword_id must be numeric"), nil
	}
	update := map[string]any{
		"resourceName": googleAdGroupCriterionResource(acct.NativeAccountID, adGroupID, keywordID),
	}
	fields := []string{}
	if status := stringArgAny(args, "status"); status != "" {
		if !googleValidStatus(status) {
			return mcpError("status must be ACTIVE or PAUSED"), nil
		}
		update["status"] = googleCampaignStatus(status)
		fields = append(fields, "status")
	}
	if bid := intArg(args, "cpc_bid_cents", -1); bid >= 0 {
		update["cpcBidMicros"] = strconv.Itoa(bid * 10000)
		fields = append(fields, "cpc_bid_micros")
	}
	if len(fields) == 0 {
		return mcpError("supply status or cpc_bid_cents"), nil
	}
	out, errOut := a.execIntegrationTool(ctx, acct, def.KeywordMutateTool, map[string]any{
		"customer_id": acct.NativeAccountID,
		"operations":  []any{map[string]any{"update": update, "updateMask": strings.Join(fields, ",")}},
	})
	if errOut != nil {
		return errOut, nil
	}
	a.emitEntityChanged(ctx, acct, "keyword", "updated", args, out, nil)
	return out, nil
}

func (a *App) toolKeywordDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	acct, def, adGroupID, errOut := a.resolveGoogleTarget(ctx, args, "keyword_delete", keywordLevelAdGroup)
	if errOut != nil {
		return errOut, nil
	}
	ids, err := keywordIDs(args["keyword_ids"])
	if err != nil {
		return mcpError(err.Error()), nil
	}
	operations := make([]any, 0, len(ids))
	for _, id := range ids {
		operations = append(operations, map[string]any{
			"remove": googleAdGroupCriterionResource(acct.NativeAccountID, adGroupID, id),
		})
	}
	out, errOut := a.execIntegrationTool(ctx, acct, def.KeywordMutateTool, map[string]any{
		"customer_id": acct.NativeAccountID,
		"operations":  operations,
	})
	if errOut != nil {
		return errOut, nil
	}
	a.emitEntityChanged(ctx, acct, "keyword", "deleted", args, out, nil)
	return map[string]any{"removed": len(ids), "result": out}, nil
}

// Negative keywords exist at both ad group and campaign level. Google rejects
// a status or a bid on a negative criterion, so the payload differs from a
// positive keyword beyond the negative flag alone.
func (a *App) toolNegativeKeywordCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	level := strings.TrimSpace(strings.ToLower(stringArgAny(args, "level")))
	if level == "" {
		level = keywordLevelAdGroup
	}
	if level != keywordLevelAdGroup && level != keywordLevelCampaign {
		return mcpError("level must be ad_group or campaign"), nil
	}
	acct, def, parentID, errOut := a.resolveGoogleTarget(ctx, args, "negative_keyword_create", level)
	if errOut != nil {
		return errOut, nil
	}
	specs, err := normalizedKeywordSpecs(args["keywords"], false)
	if err != nil {
		return mcpError(err.Error()), nil
	}
	operations := make([]any, 0, len(specs))
	tool := def.KeywordMutateTool
	for _, spec := range specs {
		keyword := map[string]any{"text": spec.Text, "matchType": spec.MatchType}
		criterion := map[string]any{"negative": true, "keyword": keyword}
		if level == keywordLevelCampaign {
			criterion["campaign"] = googleCampaignResource(acct.NativeAccountID, parentID)
			tool = def.CampaignCriterionMutateTool
		} else {
			criterion["adGroup"] = googleAdGroupResource(acct.NativeAccountID, parentID)
		}
		operations = append(operations, map[string]any{"create": criterion})
	}
	out, errOut := a.execIntegrationTool(ctx, acct, tool, map[string]any{
		"customer_id": acct.NativeAccountID,
		"operations":  operations,
	})
	if errOut != nil {
		return errOut, nil
	}
	a.emitEntityChanged(ctx, acct, "negative_keyword", "created", args, out, nil)
	return map[string]any{"level": level, "parent_id": parentID, "requested": len(specs), "keywords": out}, nil
}

func (a *App) toolNegativeKeywordList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	level := strings.TrimSpace(strings.ToLower(stringArgAny(args, "level")))
	if level == "" {
		level = keywordLevelAdGroup
	}
	if level != keywordLevelAdGroup && level != keywordLevelCampaign {
		return mcpError("level must be ad_group or campaign"), nil
	}
	acct, _, parentID, errOut := a.resolveGoogleTarget(ctx, args, "negative_keyword_list", level)
	if errOut != nil {
		return errOut, nil
	}
	var query, container string
	if level == keywordLevelCampaign {
		container = "campaignCriterion"
		query = fmt.Sprintf(
			"SELECT campaign.id, campaign_criterion.criterion_id, campaign_criterion.keyword.text, "+
				"campaign_criterion.keyword.match_type, campaign_criterion.negative "+
				"FROM campaign_criterion WHERE campaign_criterion.type = KEYWORD "+
				"AND campaign_criterion.negative = TRUE AND campaign.id = %s", parentID,
		)
	} else {
		container = "adGroupCriterion"
		query = fmt.Sprintf(
			"SELECT ad_group.id, ad_group_criterion.criterion_id, ad_group_criterion.keyword.text, "+
				"ad_group_criterion.keyword.match_type, ad_group_criterion.negative "+
				"FROM ad_group_criterion WHERE ad_group_criterion.type = KEYWORD "+
				"AND ad_group_criterion.negative = TRUE AND ad_group_criterion.status != REMOVED "+
				"AND ad_group.id = %s", parentID,
		)
	}
	rows, errOut := a.googleSearchRows(ctx, acct, query)
	if errOut != nil {
		return errOut, nil
	}
	keywords := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		criterion := mapAt(row, container)
		if len(criterion) == 0 {
			criterion = mapAt(row, level+"_criterion")
		}
		keyword := mapAt(criterion, "keyword")
		keywords = append(keywords, map[string]any{
			"id":         firstString(criterion, "criterionId", "criterion_id"),
			"text":       firstString(keyword, "text"),
			"match_type": firstString(keyword, "matchType", "match_type"),
			"negative":   true,
		})
	}
	return map[string]any{"level": level, "parent_id": parentID, "count": len(keywords), "keywords": keywords}, nil
}

func (a *App) toolNegativeKeywordDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	level := strings.TrimSpace(strings.ToLower(stringArgAny(args, "level")))
	if level == "" {
		level = keywordLevelAdGroup
	}
	if level != keywordLevelAdGroup && level != keywordLevelCampaign {
		return mcpError("level must be ad_group or campaign"), nil
	}
	acct, def, parentID, errOut := a.resolveGoogleTarget(ctx, args, "negative_keyword_delete", level)
	if errOut != nil {
		return errOut, nil
	}
	ids, err := keywordIDs(args["keyword_ids"])
	if err != nil {
		return mcpError(err.Error()), nil
	}
	tool := def.KeywordMutateTool
	operations := make([]any, 0, len(ids))
	for _, id := range ids {
		resource := googleAdGroupCriterionResource(acct.NativeAccountID, parentID, id)
		if level == keywordLevelCampaign {
			resource = googleCampaignCriterionResource(acct.NativeAccountID, parentID, id)
			tool = def.CampaignCriterionMutateTool
		}
		operations = append(operations, map[string]any{"remove": resource})
	}
	out, errOut := a.execIntegrationTool(ctx, acct, tool, map[string]any{
		"customer_id": acct.NativeAccountID,
		"operations":  operations,
	})
	if errOut != nil {
		return errOut, nil
	}
	a.emitEntityChanged(ctx, acct, "negative_keyword", "deleted", args, out, nil)
	return map[string]any{"level": level, "removed": len(ids), "result": out}, nil
}
